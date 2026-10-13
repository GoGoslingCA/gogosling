package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// EventJobTriggered 是任务被触发（到点或手动）时发布到总线的事件类型；payload 为 model.Job 的值拷贝。
const EventJobTriggered = "job.triggered"

// DefaultTick 是调度循环的默认节拍。
const DefaultTick = 15 * time.Second

const (
	// MaxNameRunes 是任务名的最大长度（按字符计）。
	MaxNameRunes = 80
	// MaxInputRunes 是任务输入（推送文本或给智能体的提示）的最大长度（按字符计）。
	MaxInputRunes = 4000
	// nameFromInputRunes 是从输入第一行推导任务名时保留的长度。
	nameFromInputRunes = 40
	// maxErrorRunes 是写入 Job.LastError 的错误文本上限。
	maxErrorRunes = 500
	// bookkeepingTimeout 是执行结束后写回状态的超时；它脱离运行 ctx，关机时也要把结果落库。
	bookkeepingTimeout = 5 * time.Second
)

// Options 是 Service 的可选配置；零值字段使用默认值。
type Options struct {
	// DefaultTimezone 是 Job.Timezone 为空时使用的 IANA 时区名；为空时使用系统时区。
	DefaultTimezone string
	// Clock 是时间来源；为 nil 时使用 SystemClock。
	Clock Clock
	// Tick 是调度循环的节拍；不大于 0 时使用 DefaultTick。
	Tick time.Duration
	// RunTimeout 是单次执行的最长时间；不大于 0 表示不限制（由 Runner 自行控制）。
	RunTimeout time.Duration
}

// JobSpec 是创建任务的输入。字段含义同 model.Job；ID、时间戳与运行状态由本包填充。
type JobSpec struct {
	AgentID  string
	Name     string
	Schedule string
	Timezone string
	Kind     model.JobKind
	Input    string
	// SessionKey 为空时由 Runner 解析为该智能体的默认桌面会话。
	SessionKey string
	// MissedPolicy 为空时默认 skip。
	MissedPolicy model.MissedPolicy
	Enabled      bool
}

// JobPatch 是部分更新的输入：nil 字段表示不改。AgentID 不可变。
type JobPatch struct {
	Name         *string
	Schedule     *string
	Timezone     *string
	Kind         *model.JobKind
	Input        *string
	SessionKey   *string
	MissedPolicy *model.MissedPolicy
	Enabled      *bool
}

// entry 是内存中一条任务的权威副本与其解析结果。
type entry struct {
	job *model.Job
	// sched 为 nil 表示持久化的表达式或时区当前无法解析（见 Service.load）；此类任务不会被调度。
	sched *Schedule
}

// Service 管理定时任务并驱动调度循环。并发安全；零值不可用，请用 New 创建。
type Service struct {
	repo       model.JobRepo
	runner     Runner
	bus        *event.Bus
	log        *slog.Logger
	clock      Clock
	tick       time.Duration
	runTimeout time.Duration
	defaultLoc *time.Location

	mu      sync.Mutex
	entries map[string]*entry
	running map[string]struct{}
	// lastTick 是上一次节拍的时间，用于休眠与时钟跳变检测；零值表示尚未执行过节拍。
	lastTick time.Time
	// nextDue 仅缓存最早到期时间，不保存另一份任务或调度顺序。
	// 任务或触发时间变更时使缓存失效，正常空闲节拍无需遍历全部任务。
	nextDue      time.Time
	hasNextDue   bool
	nextDueValid bool
	// runCtx 是执行任务用的上下文：Start 之后为 Start 的 ctx，之前为 Background。
	runCtx  context.Context
	started bool
	wg      sync.WaitGroup
}

// New 创建调度服务。repo 与 runner 必填；bus 为 nil 时不发布事件；logger 为 nil 时丢弃日志。
func New(repo model.JobRepo, runner Runner, bus *event.Bus, logger *slog.Logger, opts Options) (*Service, error) {
	if repo == nil {
		return nil, model.NewError(model.CodeInvalid, "scheduler: 仓储不能为空")
	}
	if runner == nil {
		return nil, model.NewError(model.CodeInvalid, "scheduler: Runner 不能为空")
	}
	loc, err := LoadLocation(opts.DefaultTimezone)
	if err != nil {
		return nil, err
	}
	if loc == nil {
		loc = time.Local
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	clock := opts.Clock
	if clock == nil {
		clock = SystemClock{}
	}
	tick := opts.Tick
	if tick <= 0 {
		tick = DefaultTick
	}
	return &Service{
		repo:       repo,
		runner:     runner,
		bus:        bus,
		log:        logger,
		clock:      clock,
		tick:       tick,
		runTimeout: opts.RunTimeout,
		defaultLoc: loc,
		entries:    make(map[string]*entry),
		running:    make(map[string]struct{}),
		runCtx:     context.Background(),
	}, nil
}

// DefaultLocation 返回未指定时区的任务所使用的时区。
func (s *Service) DefaultLocation() *time.Location { return s.defaultLoc }

// Create 校验并持久化一条新任务；启用时同时计算 NextRunAt。返回持久化后的副本。
func (s *Service) Create(ctx context.Context, spec JobSpec) (*model.Job, error) {
	now := s.now()
	job := &model.Job{
		ID:           model.NewID(),
		AgentID:      strings.TrimSpace(spec.AgentID),
		Name:         spec.Name,
		Schedule:     spec.Schedule,
		Timezone:     spec.Timezone,
		Kind:         spec.Kind,
		Input:        spec.Input,
		SessionKey:   spec.SessionKey,
		MissedPolicy: spec.MissedPolicy,
		Enabled:      spec.Enabled,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if job.AgentID == "" {
		return nil, model.NewError(model.CodeInvalid, "agent_id 不能为空")
	}
	sched, err := s.normalize(job)
	if err != nil {
		return nil, err
	}
	if job.Enabled {
		job.NextRunAt = s.nextPtr(sched, now)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.repo.CreateJob(ctx, job); err != nil {
		return nil, err
	}
	s.entries[job.ID] = &entry{job: job, sched: sched}
	s.nextDueValid = false
	s.log.Info("定时任务已创建", "job_id", job.ID, "agent_id", job.AgentID, "schedule", job.Schedule,
		"timezone", job.Timezone, "enabled", job.Enabled)
	return clone(job), nil
}

// Update 部分更新任务；表达式、时区或启用状态变化时重新计算 NextRunAt。
// 任务不存在返回 CodeNotFound；校验失败返回 CodeInvalid 且不改动任何字段。
func (s *Service) Update(ctx context.Context, id string, patch JobPatch) (*model.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, err := s.lookupLocked(ctx, id)
	if err != nil {
		return nil, err
	}

	next := clone(e.job)
	if patch.Name != nil {
		next.Name = *patch.Name
	}
	if patch.Schedule != nil {
		next.Schedule = *patch.Schedule
	}
	if patch.Timezone != nil {
		next.Timezone = *patch.Timezone
	}
	if patch.Kind != nil {
		next.Kind = *patch.Kind
	}
	if patch.Input != nil {
		next.Input = *patch.Input
	}
	if patch.SessionKey != nil {
		next.SessionKey = *patch.SessionKey
	}
	if patch.MissedPolicy != nil {
		next.MissedPolicy = *patch.MissedPolicy
	}
	if patch.Enabled != nil {
		next.Enabled = *patch.Enabled
	}
	sched, err := s.normalize(next)
	if err != nil {
		return nil, err
	}

	now := s.now()
	scheduleChanged := e.sched == nil || next.Schedule != e.job.Schedule || next.Timezone != e.job.Timezone
	switch {
	case !next.Enabled:
		next.NextRunAt = nil
	case scheduleChanged || !e.job.Enabled || next.NextRunAt == nil:
		next.NextRunAt = s.nextPtr(sched, now)
	}
	if e.sched == nil {
		// 之前因表达式或时区无法解析而被标记失败；修复后清除该标记。
		next.LastStatus = ""
		next.LastError = ""
	}
	next.UpdatedAt = now
	if err := s.repo.UpdateJob(ctx, next); err != nil {
		return nil, err
	}
	e.job = next
	e.sched = sched
	s.nextDueValid = false
	s.log.Info("定时任务已更新", "job_id", id, "schedule", next.Schedule, "timezone", next.Timezone, "enabled", next.Enabled)
	return clone(next), nil
}

// Enable 启用任务并从现在起计算 NextRunAt。
func (s *Service) Enable(ctx context.Context, id string) (*model.Job, error) {
	enabled := true
	return s.Update(ctx, id, JobPatch{Enabled: &enabled})
}

// Disable 停用任务并清空 NextRunAt；正在执行的一轮不受影响。
func (s *Service) Disable(ctx context.Context, id string) (*model.Job, error) {
	enabled := false
	return s.Update(ctx, id, JobPatch{Enabled: &enabled})
}

// Get 返回任务；不存在时返回 CodeNotFound。
func (s *Service) Get(ctx context.Context, id string) (*model.Job, error) {
	return s.repo.GetJob(ctx, id)
}

// List 返回某智能体的任务（agentID 为空表示全部），按创建时间升序。
func (s *Service) List(ctx context.Context, agentID string) ([]*model.Job, error) {
	return s.repo.ListJobs(ctx, agentID)
}

// Delete 删除任务；正在执行的一轮会继续到结束，但其结果不再写回。
func (s *Service) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.repo.DeleteJob(ctx, id); err != nil {
		return err
	}
	delete(s.entries, id)
	s.nextDueValid = false
	s.log.Info("定时任务已删除", "job_id", id)
	return nil
}

// Forget 从内存中移除某个智能体的全部任务，供删除智能体后调用：数据库中的任务已由外键级联删除，
// 不移除的话调度循环会继续触发它们并反复写库失败。正在执行的一轮会继续到结束，但结果不再写回。
// 只改内存，不访问仓储；agentID 没有任务时是空操作。
func (s *Service) Forget(agentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, e := range s.entries {
		if e.job.AgentID == agentID {
			delete(s.entries, id)
			n++
		}
	}
	if n > 0 {
		s.nextDueValid = false
		s.log.Info("已移除被删除智能体的定时任务", "agent_id", agentID, "jobs", n)
	}
}

// TriggerNow 立即异步执行一次任务（无论是否启用），不改变 NextRunAt。
// 任务正在执行时返回 CodeConflict；调度器已停止时返回 CodeUnavailable。
func (s *Service) TriggerNow(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runCtx.Err() != nil {
		return model.NewError(model.CodeUnavailable, "调度器已停止")
	}
	e, err := s.lookupLocked(ctx, id)
	if err != nil {
		return err
	}
	if _, busy := s.running[id]; busy {
		return model.Errorf(model.CodeConflict, "任务 %s 正在执行", id)
	}
	s.launchLocked(e, s.now(), "manual")
	return nil
}

// lookupLocked 返回内存中的任务；未加载时从仓储读取并缓存。调用方持有 s.mu。
func (s *Service) lookupLocked(ctx context.Context, id string) (*entry, error) {
	if e, ok := s.entries[id]; ok {
		return e, nil
	}
	job, err := s.repo.GetJob(ctx, id)
	if err != nil {
		return nil, err
	}
	e := &entry{job: job}
	if sched, err := ParseSchedule(job.Schedule, job.Timezone, s.defaultLoc); err == nil {
		e.sched = sched
	}
	s.entries[id] = e
	s.nextDueValid = false
	return e, nil
}

// normalize 整理并校验任务的可编辑字段，返回解析后的触发规则。
func (s *Service) normalize(job *model.Job) (*Schedule, error) {
	job.Name = collapseSpaces(job.Name)
	job.Schedule = strings.TrimSpace(job.Schedule)
	job.Timezone = strings.TrimSpace(job.Timezone)
	job.SessionKey = strings.TrimSpace(job.SessionKey)
	job.Input = strings.TrimSpace(job.Input)

	switch job.Kind {
	case model.JobPushText, model.JobRunAgent:
	case "":
		return nil, model.NewError(model.CodeInvalid, "kind 不能为空，取值为 push_text 或 run_agent")
	default:
		return nil, model.Errorf(model.CodeInvalid, "未知的任务类型 %q", string(job.Kind))
	}
	switch job.MissedPolicy {
	case model.MissedSkip, model.MissedRunOnce:
	case "":
		job.MissedPolicy = model.MissedSkip
	default:
		return nil, model.Errorf(model.CodeInvalid, "未知的错过策略 %q", string(job.MissedPolicy))
	}
	if job.Input == "" {
		return nil, model.NewError(model.CodeInvalid, "input 不能为空")
	}
	if utf8.RuneCountInString(job.Input) > MaxInputRunes {
		return nil, model.Errorf(model.CodeInvalid, "input 不能超过 %d 个字符", MaxInputRunes)
	}
	if job.Name == "" {
		job.Name = nameFromInput(job.Input)
	}
	if utf8.RuneCountInString(job.Name) > MaxNameRunes {
		return nil, model.Errorf(model.CodeInvalid, "name 不能超过 %d 个字符", MaxNameRunes)
	}
	sched, err := ParseSchedule(job.Schedule, job.Timezone, s.defaultLoc)
	if err != nil {
		return nil, err
	}
	job.Schedule = sched.Spec()
	return sched, nil
}

// now 返回当前时间（UTC）。
func (s *Service) now() time.Time { return s.clock.Now().UTC() }

// nextPtr 计算 after 之后的下一次触发时间；没有下一次时返回 nil。
func (s *Service) nextPtr(sched *Schedule, after time.Time) *time.Time {
	next := sched.Next(after)
	if next.IsZero() {
		return nil
	}
	return &next
}

// persistLocked 把内存副本写回仓储并返回写入结果；调用方决定失败后的回滚或重试。调用方持有 s.mu。
func (s *Service) persistLocked(ctx context.Context, e *entry) error {
	e.job.UpdatedAt = s.now()
	if err := s.repo.UpdateJob(ctx, e.job); err != nil {
		s.log.Error("写回定时任务状态失败", "job_id", e.job.ID, "error", err)
		return err
	}
	return nil
}

// publish 发布 job.triggered 事件；payload 为任务的值拷贝。
func (s *Service) publish(job model.Job) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(event.Event{
		Type:    EventJobTriggered,
		At:      s.now(),
		AgentID: job.AgentID,
		Payload: job,
	})
}

// clone 返回任务的深拷贝（时间指针也复制），避免调用方与内存副本互相影响。
func clone(j *model.Job) *model.Job {
	c := *j
	if j.NextRunAt != nil {
		t := *j.NextRunAt
		c.NextRunAt = &t
	}
	if j.LastRunAt != nil {
		t := *j.LastRunAt
		c.LastRunAt = &t
	}
	return &c
}

// collapseSpaces 去除首尾空白并把内部连续空白压成一个空格。
func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// nameFromInput 取输入的第一个非空行作为任务名，超长时截断并加省略号。
func nameFromInput(input string) string {
	for _, line := range strings.Split(input, "\n") {
		line = collapseSpaces(line)
		if line == "" {
			continue
		}
		return truncateRunes(line, nameFromInputRunes)
	}
	return ""
}

// truncateRunes 按字符数截断并在截断处追加省略号。
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// errorText 把执行错误转为可写入 LastError 的文本：*model.Error 只取 Message（底层 Err 只进日志）。
func errorText(err error) string {
	var me *model.Error
	if errors.As(err, &me) {
		return truncateRunes(string(me.Code)+": "+me.Message, maxErrorRunes)
	}
	return truncateRunes(err.Error(), maxErrorRunes)
}
