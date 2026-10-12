package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/session"
)

// jobSessions 是 scheduler.Runner 适配器需要的会话能力（*session.Service 满足）。
type jobSessions interface {
	EnsureByKey(ctx context.Context, agentID, key string, origin model.Origin) (*model.Session, error)
	Push(ctx context.Context, sessionID, text string) (*model.Message, error)
	Send(ctx context.Context, req session.SendRequest) (*model.Run, error)
	Cancel(ctx context.Context, runID string) (*model.Run, error)
	GetRun(ctx context.Context, runID string) (*model.Run, error)
}

// defaultJobPoll 是等待运行终态时轮询运行记录的间隔：总线订阅是主路径，轮询兜底订阅溢出被关闭的情况。
const defaultJobPoll = 2 * time.Second

// cancelGrace 是任务被中止时取消在途运行的等待上限。
const cancelGrace = 5 * time.Second

// jobRunner 实现 scheduler.Runner。
//
//   - push_text：把 Input 作为助手消息直接写入目标会话（session.Push，消息 Meta 带 pushed），不经过模型；
//     目标是 IM 通道会话时，网关订阅带 pushed 标记的 message.created 并送回对应的聊天；
//   - run_agent：以 Input 为用户输入发起运行（trigger=job），等到终态再返回；
//     运行失败或取消返回带错误码的错误，scheduler 把 Message 写进 Job.LastError。
//
// 目标会话由 Job.SessionKey 决定；为空时是该智能体的默认桌面会话（"<agent>:desktop:local"）。
type jobRunner struct {
	sessions jobSessions
	bus      *event.Bus
	log      *slog.Logger
	poll     time.Duration
}

// RunJob 执行一次定时任务。
func (r jobRunner) RunJob(ctx context.Context, job *model.Job) error {
	key := job.SessionKey
	if key == "" {
		key = session.MakeKey(job.AgentID, model.OriginDesktop, session.DefaultPeer)
	}
	k, err := session.ParseKey(key)
	if err != nil {
		return err
	}
	sess, err := r.sessions.EnsureByKey(ctx, job.AgentID, key, k.Origin)
	if err != nil {
		return err
	}
	switch job.Kind {
	case model.JobPushText:
		_, err := r.sessions.Push(ctx, sess.ID, job.Input)
		return err
	case model.JobRunAgent:
		return r.runAndWait(ctx, job, sess)
	default:
		return model.Errorf(model.CodeInvalid, "未知的任务类型 %q", string(job.Kind))
	}
}

// runAndWait 发起运行并等待终态。订阅先于 Send 建立，避免终态事件在订阅之前发布而丢失。
func (r jobRunner) runAndWait(ctx context.Context, job *model.Job, sess *model.Session) error {
	subCtx, stop := context.WithCancel(ctx)
	defer stop()
	sub := r.bus.Subscribe(subCtx, r.bus.LastSeq(), 64, func(ev event.Event) bool {
		return ev.SessionID == sess.ID && isTerminalEvent(ev.Type)
	})

	run, err := r.sessions.Send(ctx, session.SendRequest{
		SessionID: sess.ID,
		Text:      job.Input,
		Trigger:   model.TriggerJob,
		Principal: jobPrincipal(job),
	})
	if err != nil {
		return err
	}
	if run.Status.Terminal() {
		return runOutcome(run)
	}

	poll := r.poll
	if poll <= 0 {
		poll = defaultJobPoll
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	events := sub.C
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				// 订阅因慢消费被总线关闭：改由轮询判断终态。
				events = nil
				continue
			}
			if ev.RunID != run.ID {
				continue
			}
			return r.finalOutcome(ctx, run.ID)
		case <-ticker.C:
			cur, err := r.sessions.GetRun(ctx, run.ID)
			if err != nil {
				r.log.Warn("查询任务运行状态失败", "job_id", job.ID, "run_id", run.ID, "error", err)
				continue
			}
			if cur.Status.Terminal() {
				return runOutcome(cur)
			}
		case <-ctx.Done():
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cancelGrace)
			defer cancel()
			if _, err := r.sessions.Cancel(cctx, run.ID); err != nil {
				r.log.Warn("任务中止时取消运行失败", "job_id", job.ID, "run_id", run.ID, "error", err)
			}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return model.Wrap(model.CodeTimeout, "任务运行超时", ctx.Err())
			}
			return model.Wrap(model.CodeCancelled, "任务被中止", ctx.Err())
		}
	}
}

// finalOutcome 读取终态后的运行记录；终态事件发布时记录已经落库。
func (r jobRunner) finalOutcome(ctx context.Context, runID string) error {
	run, err := r.sessions.GetRun(context.WithoutCancel(ctx), runID)
	if err != nil {
		return fmt.Errorf("读取任务运行结果: %w", err)
	}
	return runOutcome(run)
}

// runOutcome 把运行终态映射为任务结果。
func runOutcome(run *model.Run) error {
	switch run.Status {
	case model.RunCompleted:
		return nil
	case model.RunCancelled:
		return &model.Error{Code: model.CodeCancelled, Message: "运行已取消", Details: map[string]any{"run_id": run.ID}}
	case model.RunFailed:
		msg := run.Error
		if msg == "" {
			msg = session.ErrorRunFailed
		}
		return &model.Error{Code: model.CodeUnavailable, Message: msg, Details: map[string]any{"run_id": run.ID}}
	default:
		return &model.Error{Code: model.CodeInternal, Message: "运行没有到达终态", Details: map[string]any{"run_id": run.ID}}
	}
}

// isTerminalEvent 判断事件类型是否是运行终态。
func isTerminalEvent(t string) bool {
	switch model.RunEventType(t) {
	case model.EventRunCompleted, model.EventRunFailed, model.EventRunCancelled:
		return true
	}
	return false
}

// jobPrincipal 是定时任务发起运行时的身份：内核自身，ID 为 "job:<id>"，便于审计按任务追溯。
func jobPrincipal(job *model.Job) model.Principal {
	return model.Principal{ID: "job:" + job.ID, Kind: model.PrincipalSystem, Name: job.Name}
}
