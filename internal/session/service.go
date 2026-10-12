package session

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// 会话生命周期事件的类型；事件 payload 为 View（会话加上当前在途运行的 ID）的值拷贝。
const (
	EventSessionCreated = "session.created"
	EventSessionUpdated = "session.updated"
	EventSessionDeleted = "session.deleted"
)

const (
	// DefaultHistoryLimit 是组装运行请求时默认携带的历史消息条数（含本次用户消息）。
	DefaultHistoryLimit = 50
	// DefaultMessageLimit 是 Messages 未指定 limit 时的默认条数。
	DefaultMessageLimit = 100
	// MaxMessageLimit 是 Messages 单次返回的上限，与 api/openapi 中 limit 的 maximum（500）一致；
	// 这是全仓唯一的定义，传输层不再另设更小的上限。
	MaxMessageLimit = 500
	// maxAutoTitleRunes 是根据首条用户消息自动生成标题时保留的最大字符数。
	maxAutoTitleRunes = 60
)

// Options 是 Service 的可选配置；零值字段使用默认值。
type Options struct {
	// MaxParallelRuns 限制同时进入工作进程的运行；0 保留旧调用方行为。
	MaxParallelRuns int
	// MaxQueuedRuns 限制待执行身份数量，不为每份排队任务预读历史。
	MaxQueuedRuns int
	// WorkspaceGroup 返回实际工作区身份，同一工作区同时只派发一个任务。
	WorkspaceGroup func(context.Context, *model.Agent) (string, error)
	// HistoryLimit 是每次运行携带的最近历史消息条数（含本次用户消息），默认 DefaultHistoryLimit。
	HistoryLimit int
	// RunTimeout 是单次运行的最长时长；0 表示不限制。超时的运行会被取消并标记为失败。
	RunTimeout time.Duration
	// Run 是传给运行时的推理参数；零值字段使用 DefaultRunOptions。
	Run RunOptions
	// PromptBuilder 可选：在智能体系统提示的基础上生成最终系统提示（如追加技能清单）。
	PromptBuilder PromptBuilder
	// Now 用于测试注入时钟；默认返回 UTC 的当前时间。
	Now func() time.Time
}

// Service 实现会话、消息与运行的管理，并发安全。
type Service struct {
	store   Store
	agents  AgentSource
	creds   CredentialSource
	tools   ToolSource
	runtime Runtime
	bus     *event.Bus
	log     *slog.Logger
	opts    Options
	now     func() time.Time

	// keyMu 串行化所有会改变"路由键 → active 会话"映射的操作（创建、接管、归档与取消归档），
	// 让"同一时刻最多一个 active 会话持有某个键"在进程内无竞争地成立；持久层的事务保证落盘一致。
	keyMu sync.Mutex

	// mu 保护运行表、正在删除的智能体集合与关闭标志。
	mu        sync.Mutex
	active    map[string]*activeRun // runID → 在途运行
	bySession map[string]string     // sessionID → runID，同一会话同时只允许一个
	// deleting 是 DeleteByAgent 进行中的智能体；期间 reserve 拒绝为它们发起新运行。
	deleting    map[string]struct{}
	closed      bool
	maxParallel int
	maxQueued   int
	slots       map[string]bool
	groups      map[string]int
	queue       []admittedRun
	// promptRuns 是不落库的文字优化请求，仅保存取消句柄，并共享执行槽预算。
	promptRuns map[string]context.CancelFunc
}

// View 是会话加上当前在途运行的 ID，形状与 OpenAPI 的 Session（含 active_run_id）一致。
// 它是 session.* 事件的 payload（值类型），server 与 client 可直接复用它作为会话的响应视图。
type View struct {
	model.Session
	// ActiveRunID 是会话当前在途运行的 ID；没有时为空并在 JSON 中省略。
	ActiveRunID string `json:"active_run_id,omitempty"`
}

// ViewOf 返回会话的视图：附上本进程中该会话当前在途运行的 ID。sess 为 nil 时返回零值。
func (s *Service) ViewOf(sess *model.Session) View {
	if sess == nil {
		return View{}
	}
	v := View{Session: *sess}
	if runID, ok := s.ActiveRunID(sess.ID); ok {
		v.ActiveRunID = runID
	}
	return v
}

// New 构造 Service。store、agents、creds、tools、runtime、bus 不得为 nil；logger 为 nil 时丢弃日志。
func New(store Store, agents AgentSource, creds CredentialSource, tools ToolSource, runtime Runtime, bus *event.Bus, logger *slog.Logger, opts Options) *Service {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if opts.HistoryLimit <= 0 {
		opts.HistoryLimit = DefaultHistoryLimit
	}
	opts.Run = opts.Run.withDefaults()
	if opts.MaxQueuedRuns < 1 {
		opts.MaxQueuedRuns = DefaultQueuedRuns
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		store:       store,
		agents:      agents,
		creds:       creds,
		tools:       tools,
		runtime:     runtime,
		bus:         bus,
		log:         logger,
		opts:        opts,
		now:         now,
		active:      make(map[string]*activeRun),
		bySession:   make(map[string]string),
		deleting:    make(map[string]struct{}),
		maxParallel: opts.MaxParallelRuns,
		maxQueued:   opts.MaxQueuedRuns,
		slots:       make(map[string]bool),
		groups:      make(map[string]int),
		promptRuns:  make(map[string]context.CancelFunc),
	}
}

// Create 为智能体创建一个新的 active 会话，路由键为 "<agent_id>:<origin>:local"
// （例如 POST /sessions 的 origin=cli 得到 "<agent_id>:cli:local"，与桌面会话的键互不干扰）。
//
// 若已有 active 会话持有该键（例如桌面上再次"新建对话"），新会话接管该键：旧会话保持 active、
// 可以按 ID 继续使用，但它的键改写为 "<agent_id>:<origin>:<旧会话 ID>"，并发布 session.updated。
// 这样定时任务按默认键投递时总是落到最新的桌面会话，而不会因为键冲突拒绝"新建对话"，
// 也不会把旧对话归档。
func (s *Service) Create(ctx context.Context, agentID string, origin model.Origin, title string) (*model.Session, error) {
	return s.CreateInProject(ctx, agentID, origin, title, "")
}

// CreateInProject 原子创建带项目关联的会话；项目必须属于同一智能体。
func (s *Service) CreateInProject(ctx context.Context, agentID string, origin model.Origin, title, projectID string) (*model.Session, error) {
	if !validAgentID(agentID) {
		return nil, model.Errorf(model.CodeInvalid, "非法的智能体 ID %q", agentID)
	}
	if !validOrigin(origin) {
		return nil, model.Errorf(model.CodeInvalid, "未知的会话来源 %q", string(origin))
	}
	if _, err := s.agents.GetAgent(ctx, agentID); err != nil {
		return nil, err
	}
	key := Key{AgentID: agentID, Origin: origin, Peer: DefaultPeer}
	return s.ensure(ctx, key, strings.TrimSpace(title), true, projectID)
}

// EnsureByKey 返回持有 key 的 active 会话；没有时按 key 创建一个。
//
// key 的 agent 段必须等于 agentID；origin 非空时必须与 key 的 origin 段一致。
// 这是 IM 网关与调度器的入口：它们只知道路由键，不关心会话 ID。
func (s *Service) EnsureByKey(ctx context.Context, agentID, key string, origin model.Origin) (*model.Session, error) {
	k, err := ParseKey(key)
	if err != nil {
		return nil, err
	}
	if k.AgentID != agentID {
		return nil, &model.Error{
			Code:    model.CodeInvalid,
			Message: "路由键属于其他智能体",
			Details: map[string]any{"key": key, "agent_id": agentID},
		}
	}
	if origin != "" && k.Origin != origin {
		return nil, &model.Error{
			Code:    model.CodeInvalid,
			Message: "路由键的来源与请求不一致",
			Details: map[string]any{"key": key, "origin": string(origin)},
		}
	}
	if _, err := s.agents.GetAgent(ctx, agentID); err != nil {
		return nil, err
	}
	return s.ensure(ctx, k, "", false, "")
}

// ensure 在一个事务内查找持有 key 的 active 会话：找到且 takeover 为 false 时直接复用；
// takeover 为 true 时改写旧会话的键再创建新会话；没有时创建。
func (s *Service) ensure(ctx context.Context, key Key, title string, takeover bool, projectID string) (*model.Session, error) {
	s.keyMu.Lock()
	defer s.keyMu.Unlock()

	var created, displaced, reused *model.Session
	err := s.store.Tx(ctx, func(tx model.Store) error {
		created, displaced, reused = nil, nil, nil
		if projectID != "" {
			projects, ok := tx.(model.ProjectRepo)
			if !ok {
				return model.NewError(model.CodeUnavailable, i18n.T("en", "workbench.unavailable", nil))
			}
			project, err := projects.GetProject(ctx, projectID)
			if err != nil {
				return err
			}
			if project.AgentID != key.AgentID {
				return &model.Error{Code: model.CodeInvalid, Message: i18n.T("en", "workbench.scope_mismatch", nil), Details: map[string]any{"reason": "scope_mismatch"}}
			}
		}
		now := s.now()
		prev, err := tx.GetActiveSessionByKey(ctx, key.String())
		switch {
		case err == nil:
			if !takeover {
				reused = prev
				return nil
			}
			prev.Key = MakeKey(prev.AgentID, prev.Origin, prev.ID)
			prev.UpdatedAt = now
			if err := tx.UpdateSession(ctx, prev); err != nil {
				return fmt.Errorf("改写旧会话的路由键: %w", err)
			}
			displaced = prev
		case model.IsCode(err, model.CodeNotFound):
		default:
			return fmt.Errorf("按路由键查找会话: %w", err)
		}
		sess := &model.Session{
			ID:        model.NewID(),
			ProjectID: projectID,
			AgentID:   key.AgentID,
			Title:     title,
			Origin:    key.Origin,
			Key:       key.String(),
			Status:    model.SessionActive,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := tx.CreateSession(ctx, sess); err != nil {
			return fmt.Errorf("创建会话: %w", err)
		}
		created = sess
		return nil
	})
	if err != nil {
		return nil, err
	}
	if reused != nil {
		return reused, nil
	}
	if displaced != nil {
		s.publishSession(EventSessionUpdated, displaced)
	}
	s.publishSession(EventSessionCreated, created)
	return created, nil
}

// Get 返回会话；不存在时返回 CodeNotFound。
func (s *Service) Get(ctx context.Context, id string) (*model.Session, error) {
	return s.store.GetSession(ctx, id)
}

// List 按过滤条件分页列出会话；仓储支持 RecentSessionLister 时优先返回最近活动的会话。
// 不支持该可选能力的仓储沿用 ListSessions 的原有顺序。
func (s *Service) List(ctx context.Context, f model.SessionFilter, p model.Page) ([]*model.Session, error) {
	if recent, ok := s.store.(RecentSessionLister); ok {
		return recent.ListRecentSessions(ctx, f, p)
	}
	return s.store.ListSessions(ctx, f, p)
}

// SessionUpdate 是 Update 的可选字段；nil 表示不修改该字段。
type SessionUpdate struct {
	Title  *string
	Status *model.SessionStatus
}

// Update 修改标题或归档状态，并发布 session.updated。
//
// 归档要求会话没有在途运行（先 Cancel）；取消归档要求路由键当前没有被其他 active 会话持有，
// 否则返回 CodeConflict。没有任何改变时不写库也不发事件。
func (s *Service) Update(ctx context.Context, id string, upd SessionUpdate) (*model.Session, error) {
	s.keyMu.Lock()
	defer s.keyMu.Unlock()

	sess, err := s.store.GetSession(ctx, id)
	if err != nil {
		return nil, err
	}
	changed := false
	if upd.Title != nil {
		if t := strings.TrimSpace(*upd.Title); t != sess.Title {
			sess.Title = t
			changed = true
		}
	}
	if upd.Status != nil && *upd.Status != sess.Status {
		switch *upd.Status {
		case model.SessionArchived:
			if runID, busy := s.ActiveRunID(id); busy {
				return nil, &model.Error{
					Code:    model.CodeConflict,
					Message: "会话仍有未结束的运行，不能归档",
					Details: map[string]any{"run_id": runID},
				}
			}
		case model.SessionActive:
			other, err := s.store.GetActiveSessionByKey(ctx, sess.Key)
			switch {
			case err == nil:
				return nil, &model.Error{
					Code:    model.CodeConflict,
					Message: "路由键已被另一个活动会话持有，不能取消归档",
					Details: map[string]any{"session_id": other.ID, "key": sess.Key},
				}
			case !model.IsCode(err, model.CodeNotFound):
				return nil, fmt.Errorf("按路由键查找会话: %w", err)
			}
		default:
			return nil, model.Errorf(model.CodeInvalid, "未知的会话状态 %q", string(*upd.Status))
		}
		sess.Status = *upd.Status
		changed = true
	}
	if !changed {
		return sess, nil
	}
	sess.UpdatedAt = s.now()
	if err := s.store.UpdateSession(ctx, sess); err != nil {
		return nil, fmt.Errorf("更新会话: %w", err)
	}
	s.publishSession(EventSessionUpdated, sess)
	return sess, nil
}

// Delete 删除会话及其消息与运行记录，并发布 session.deleted。有在途运行时返回 CodeConflict。
//
// 检查与删除之间仍可能有 Send 挤入：那次 Send 会在写库时因会话不存在而失败并释放占位，
// 不会产生悬挂的运行，因此这里不额外加锁。
func (s *Service) Delete(ctx context.Context, id string) error {
	sess, err := s.store.GetSession(ctx, id)
	if err != nil {
		return err
	}
	if runID, busy := s.ActiveRunID(id); busy {
		return &model.Error{
			Code:    model.CodeConflict,
			Message: "会话仍有未结束的运行，不能删除",
			Details: map[string]any{"run_id": runID},
		}
	}
	if err := s.store.DeleteSession(ctx, id); err != nil {
		return fmt.Errorf("删除会话: %w", err)
	}
	s.publishSession(EventSessionDeleted, sess)
	return nil
}

// deleteByAgentPage 是 DeleteByAgent 列出会话时的分页大小。
const deleteByAgentPage = 200

// DeleteByAgent 删除智能体名下的全部会话（含归档的）及其消息与运行记录，逐个发布 session.deleted。
// 供删除智能体前调用：外键级联也会删掉这些会话，但不会发布事件，客户端缓存与会话授权撤销都依赖该事件。
//
// 该智能体任一会话有在途运行时返回 CodeConflict（Details 含 run_id 与 session_id），不删除任何会话。
// 删除进行期间拒绝为该智能体发起新运行（reserve 返回 CodeConflict），并持有 keyMu 阻止新建会话，
// 所有删除在一个事务内完成；智能体没有会话时返回 nil。
func (s *Service) DeleteByAgent(ctx context.Context, agentID string) error {
	if agentID == "" {
		return model.NewError(model.CodeInvalid, "缺少智能体 ID")
	}
	s.mu.Lock()
	for _, ar := range s.active {
		if ar.agent.ID == agentID {
			runID, sessionID := ar.id, ar.sessionID
			s.mu.Unlock()
			return &model.Error{
				Code:    model.CodeConflict,
				Message: "智能体仍有未结束的运行，不能删除",
				Details: map[string]any{"run_id": runID, "session_id": sessionID, "agent_id": agentID},
			}
		}
	}
	s.deleting[agentID] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.deleting, agentID)
		s.mu.Unlock()
	}()

	s.keyMu.Lock()
	defer s.keyMu.Unlock()

	var all []*model.Session
	for offset := 0; ; offset += deleteByAgentPage {
		page, err := s.store.ListSessions(ctx, model.SessionFilter{AgentID: agentID}, model.Page{Limit: deleteByAgentPage, Offset: offset})
		if err != nil {
			return fmt.Errorf("列出智能体的会话: %w", err)
		}
		all = append(all, page...)
		if len(page) < deleteByAgentPage {
			break
		}
	}
	if len(all) == 0 {
		return nil
	}
	var deleted []*model.Session
	err := s.store.Tx(ctx, func(tx model.Store) error {
		deleted = deleted[:0]
		for _, sess := range all {
			if err := tx.DeleteSession(ctx, sess.ID); err != nil {
				if model.IsCode(err, model.CodeNotFound) {
					continue // 已被并发的 Delete 删除，那次删除自己会发布事件
				}
				return fmt.Errorf("删除会话 %s: %w", sess.ID, err)
			}
			deleted = append(deleted, sess)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, sess := range deleted {
		s.publishSession(EventSessionDeleted, sess)
	}
	return nil
}

// Messages 返回会话中 Seq 大于 afterSeq 的消息，按 Seq 升序；limit 为 0 时用 DefaultMessageLimit，
// 超过 MaxMessageLimit（与契约的 maximum 相同）时截断。会话不存在时返回 CodeNotFound。
func (s *Service) Messages(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]*model.Message, error) {
	if _, err := s.store.GetSession(ctx, sessionID); err != nil {
		return nil, err
	}
	if afterSeq < 0 {
		afterSeq = 0
	}
	switch {
	case limit <= 0:
		limit = DefaultMessageLimit
	case limit > MaxMessageLimit:
		limit = MaxMessageLimit
	}
	msgs, err := s.store.ListMessages(ctx, sessionID, afterSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("读取消息: %w", err)
	}
	return msgs, nil
}

// publishSession 把会话生命周期事件发布到总线；payload 是 View 的值拷贝（含当前在途运行的 ID），
// 客户端可以直接用它更新本地缓存而不会误以为会话空闲。
func (s *Service) publishSession(typ string, sess *model.Session) {
	if s.bus == nil || sess == nil {
		return
	}
	s.bus.Publish(event.Event{
		Type:      typ,
		At:        s.now(),
		AgentID:   sess.AgentID,
		SessionID: sess.ID,
		Payload:   s.ViewOf(sess),
	})
}

// publishRun 把运行事件发布到总线：Type 为 RunEventType，路由字段取自事件，payload 为事件本身。
func (s *Service) publishRun(ev model.RunEvent) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(event.Event{
		Type:      string(ev.Type),
		At:        ev.At,
		AgentID:   ev.AgentID,
		SessionID: ev.SessionID,
		RunID:     ev.RunID,
		Payload:   ev,
	})
}

// autoTitle 从首条用户消息的第一行生成标题，超长时截断并加省略号；没有可用文本时返回空。
func autoTitle(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if utf8.RuneCountInString(line) <= maxAutoTitleRunes {
			return line
		}
		runes := []rune(line)
		return string(runes[:maxAutoTitleRunes]) + "…"
	}
	return ""
}
