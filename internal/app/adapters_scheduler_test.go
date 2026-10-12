package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/session"
)

// fakeJobSessions 记录调用；Send 之后按 outcome 在总线上发布终态事件（或不发布，交给轮询）。
type fakeJobSessions struct {
	mu      sync.Mutex
	bus     *event.Bus
	keys    []string
	origins []model.Origin
	pushed  []string
	sent    []session.SendRequest
	cancels []string

	outcome  model.RunStatus // 终态；为空表示运行一直不结束
	silent   bool            // 为 true 时不发布终态事件，只更新运行记录（模拟订阅溢出）
	runError string
	runs     map[string]*model.Run
}

func (f *fakeJobSessions) EnsureByKey(_ context.Context, agentID, key string, origin model.Origin) (*model.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, key)
	f.origins = append(f.origins, origin)
	return &model.Session{ID: "s-" + key, AgentID: agentID, Key: key, Origin: origin}, nil
}

func (f *fakeJobSessions) Push(_ context.Context, sessionID, text string) (*model.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pushed = append(f.pushed, sessionID+"|"+text)
	return &model.Message{ID: "m"}, nil
}

func (f *fakeJobSessions) Send(_ context.Context, req session.SendRequest) (*model.Run, error) {
	f.mu.Lock()
	f.sent = append(f.sent, req)
	run := &model.Run{ID: model.NewID(), SessionID: req.SessionID, Status: model.RunRunning}
	if f.runs == nil {
		f.runs = map[string]*model.Run{}
	}
	f.runs[run.ID] = run
	outcome, silent := f.outcome, f.silent
	cp := *run
	f.mu.Unlock()
	if outcome != "" {
		go func() {
			time.Sleep(10 * time.Millisecond)
			f.mu.Lock()
			run.Status, run.Error = outcome, f.runError
			f.mu.Unlock()
			if !silent {
				typ := map[model.RunStatus]model.RunEventType{
					model.RunCompleted: model.EventRunCompleted, model.RunFailed: model.EventRunFailed, model.RunCancelled: model.EventRunCancelled,
				}[outcome]
				f.bus.Publish(event.Event{Type: string(typ), SessionID: req.SessionID, RunID: run.ID})
			}
		}()
	}
	return &cp, nil
}

func (f *fakeJobSessions) Cancel(_ context.Context, runID string) (*model.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels = append(f.cancels, runID)
	return f.runs[runID], nil
}

func (f *fakeJobSessions) GetRun(_ context.Context, runID string) (*model.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.runs[runID]
	if !ok {
		return nil, model.NotFound("run", runID)
	}
	cp := *r
	return &cp, nil
}

func newJobRunner(outcome model.RunStatus) (jobRunner, *fakeJobSessions) {
	bus := event.NewBus(64)
	fs := &fakeJobSessions{bus: bus, outcome: outcome}
	return jobRunner{sessions: fs, bus: bus, log: discardLogger(), poll: 20 * time.Millisecond}, fs
}

func TestJobRunnerPushTextUsesDefaultDesktopSession(t *testing.T) {
	r, fs := newJobRunner("")
	job := &model.Job{ID: "j1", AgentID: "a1", Kind: model.JobPushText, Input: "喝水"}
	if err := r.RunJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if fs.keys[0] != "a1:desktop:local" || fs.origins[0] != model.OriginDesktop {
		t.Fatalf("SessionKey 为空时应使用默认桌面会话，得到 %v %v", fs.keys, fs.origins)
	}
	if len(fs.pushed) != 1 || fs.pushed[0] != "s-a1:desktop:local|喝水" || len(fs.sent) != 0 {
		t.Fatalf("push_text 应直接推送而不发起运行，得到 pushed=%v sent=%v", fs.pushed, fs.sent)
	}

	job.SessionKey = "a1:channel:telegram:42"
	if err := r.RunJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if fs.origins[1] != model.OriginChannel {
		t.Fatalf("应按路由键的来源段确保会话，得到 %v", fs.origins)
	}
	job.SessionKey = "坏键"
	if err := r.RunJob(context.Background(), job); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("畸形路由键应 invalid，得到 %v", err)
	}
	if err := r.RunJob(context.Background(), &model.Job{AgentID: "a1", Kind: "weird"}); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("未知任务类型应 invalid，得到 %v", err)
	}
}

func TestJobRunnerRunAgentWaitsForTerminalEvent(t *testing.T) {
	r, fs := newJobRunner(model.RunCompleted)
	job := &model.Job{ID: "j2", Name: "问候", AgentID: "a1", Kind: model.JobRunAgent, Input: "早上好"}
	if err := r.RunJob(context.Background(), job); err != nil {
		t.Fatalf("运行完成时应返回 nil，得到 %v", err)
	}
	req := fs.sent[0]
	if req.Trigger != model.TriggerJob || req.Text != "早上好" || req.Untrusted {
		t.Fatalf("SendRequest 字段错误: %+v", req)
	}
	if req.Principal.ID != "job:j2" || req.Principal.Kind != model.PrincipalSystem {
		t.Fatalf("Principal 应为 job:<id> 的系统身份，得到 %+v", req.Principal)
	}
}

func TestJobRunnerReportsFailureAndCancellation(t *testing.T) {
	r, fs := newJobRunner(model.RunFailed)
	fs.runError = "模型服务 401"
	err := r.RunJob(context.Background(), &model.Job{ID: "j", AgentID: "a1", Kind: model.JobRunAgent, Input: "x"})
	var me *model.Error
	if !errors.As(err, &me) || me.Message != "模型服务 401" {
		t.Fatalf("失败的运行应把原因作为错误消息，得到 %v", err)
	}

	r, _ = newJobRunner(model.RunCancelled)
	if err := r.RunJob(context.Background(), &model.Job{ID: "j", AgentID: "a1", Kind: model.JobRunAgent, Input: "x"}); !model.IsCode(err, model.CodeCancelled) {
		t.Fatalf("取消的运行应返回 cancelled，得到 %v", err)
	}
}

func TestJobRunnerFallsBackToPollingWithoutEvents(t *testing.T) {
	r, fs := newJobRunner(model.RunCompleted)
	fs.silent = true
	if err := r.RunJob(context.Background(), &model.Job{ID: "j", AgentID: "a1", Kind: model.JobRunAgent, Input: "x"}); err != nil {
		t.Fatalf("没有终态事件时应靠轮询发现完成，得到 %v", err)
	}
}

func TestJobRunnerCancelsRunWhenAborted(t *testing.T) {
	r, fs := newJobRunner("") // 运行永不结束
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := r.RunJob(ctx, &model.Job{ID: "j", AgentID: "a1", Kind: model.JobRunAgent, Input: "x"})
	if !model.IsCode(err, model.CodeTimeout) {
		t.Fatalf("超时应返回 timeout，得到 %v", err)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if len(fs.cancels) != 1 {
		t.Fatalf("任务中止时应取消在途运行一次，得到 %v", fs.cancels)
	}
	if _, ok := fs.runs[fs.cancels[0]]; !ok {
		t.Fatalf("取消的应是本任务发起的运行，得到 %v", fs.cancels)
	}
}

func TestAgentSourceDelegates(t *testing.T) {
	a := &model.Agent{ID: "a1"}
	src := agentSource{agents: fakeAgents{"a1": a}}
	if got, err := src.GetAgent(context.Background(), "a1"); err != nil || got != a {
		t.Fatalf("GetAgent 应转交，得到 %v / %v", got, err)
	}
	if _, err := src.GetAgent(context.Background(), "x"); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("不存在应 not_found，得到 %v", err)
	}
}
