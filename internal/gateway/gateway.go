package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// 默认时限与队列长度。
const (
	// DefaultInboundTimeout 是处理一条入站消息（查找会话、启动运行）的时限。
	DefaultInboundTimeout = 30 * time.Second
	// DefaultSendTimeout 是向平台发送一个分片的时限。
	DefaultSendTimeout = 30 * time.Second
	// DefaultOutboundQueue 是每个通道待发送回复的队列长度；队列满时丢弃并记录错误。
	DefaultOutboundQueue = 256
	// eventBuffer 是事件总线订阅的缓冲长度；只订阅低频的终态与审批事件。
	eventBuffer = 1024
	// replyToMax 是"运行 → 待引用消息"表的上限，超过时清理最旧的条目。
	replyToMax = 4096
	// metaPushed 是推送消息 Meta 中的标记键，与 session.MetaPushed 相同（本包不导入 session）。
	metaPushed = "pushed"
)

// Texts 是网关发回通道的提示文案；占位符用 {name} 表示。零值字段使用内置中文默认值，
// 集成阶段由组合根按 i18n 填充。
type Texts struct {
	// Busy 在同一会话仍有在途运行时回复。
	Busy string
	// Unavailable 在无法找到会话或启动运行时回复；不包含内部错误细节。
	Unavailable string
	// ApprovalRequired 在收到 approval.requested 时发送；占位符 {summary}、{risk}。
	ApprovalRequired string
	// RunFailed 在运行失败时发送；占位符 {error} 替换为 RunErrorMap 中该失败原因的本地化文案。
	// 原因不在 RunErrorMap 中时（运行时给出的任意文本可能带内部细节，不发给 IM 对端），
	// 发送去掉 {error} 及其两侧分隔符后的通用失败文案，例如 "处理失败：{error}" → "处理失败"。
	RunFailed string
	// RunCancelled 在运行被取消时发送。
	RunCancelled string
	// EmptyReply 在运行完成但最终消息没有文本时发送。
	EmptyReply string
	// RunErrorMap 把 Run.Error 的机器可读常量（session 包的 Error* 常量，如 "run timeout"、"daemon restarted"）
	// 映射为本地化文案，由组合根从 i18n 的 run.error.* 填充。为 nil 时所有失败都只发送通用失败文案。
	RunErrorMap map[string]string
}

// DefaultTexts 是内置的中文文案。
var DefaultTexts = Texts{
	Busy:             "上一条消息还在处理中，请稍后再发。",
	Unavailable:      "智能体暂时无法处理消息，请稍后再试。",
	ApprovalRequired: "智能体请求执行一项需要审批的操作：{summary}（风险等级：{risk}）。请在桌面端完成审批。",
	RunFailed:        "处理失败：{error}",
	RunCancelled:     "本次处理已取消。",
	EmptyReply:       "（本轮没有文本回复）",
}

// withDefaults 用默认文案填补空字段。
func (t Texts) withDefaults() Texts {
	fill := func(dst *string, def string) {
		if *dst == "" {
			*dst = def
		}
	}
	fill(&t.Busy, DefaultTexts.Busy)
	fill(&t.Unavailable, DefaultTexts.Unavailable)
	fill(&t.ApprovalRequired, DefaultTexts.ApprovalRequired)
	fill(&t.RunFailed, DefaultTexts.RunFailed)
	fill(&t.RunCancelled, DefaultTexts.RunCancelled)
	fill(&t.EmptyReply, DefaultTexts.EmptyReply)
	if t.RunErrorMap != nil {
		// 复制一份：调用方之后修改自己的 map 不会与事件循环的读取竞争。
		m := make(map[string]string, len(t.RunErrorMap))
		for k, v := range t.RunErrorMap {
			m[k] = v
		}
		t.RunErrorMap = m
	}
	return t
}

// runFailedText 返回运行失败时发给通道的文本：已知的机器常量换成本地化原因，其他原因不外发。
func (t Texts) runFailedText(reason string) string {
	if msg := t.RunErrorMap[strings.TrimSpace(reason)]; msg != "" {
		return replaceTokens(t.RunFailed, map[string]string{"error": msg})
	}
	if generic := withoutToken(t.RunFailed, "error"); generic != "" {
		return generic
	}
	return withoutToken(DefaultTexts.RunFailed, "error")
}

// tokenSeparators 是去掉占位符时一并去掉的相邻分隔符。
const tokenSeparators = " \t:：-—,，;；"

// withoutToken 去掉模板中的 {name} 占位符、与它相邻的分隔符以及因此变空的括号，得到不含该参数的通用文案。
func withoutToken(tmpl, name string) string {
	ph := "{" + name + "}"
	for {
		i := strings.Index(tmpl, ph)
		if i < 0 {
			return strings.TrimSpace(tmpl)
		}
		before := strings.TrimRight(tmpl[:i], tokenSeparators)
		after := tmpl[i+len(ph):]
		for _, pair := range [][2]string{{"(", ")"}, {"（", "）"}, {"[", "]"}, {"【", "】"}} {
			if strings.HasSuffix(before, pair[0]) && strings.HasPrefix(after, pair[1]) {
				before = strings.TrimRight(strings.TrimSuffix(before, pair[0]), tokenSeparators)
				after = after[len(pair[1]):]
			}
		}
		if before == "" {
			after = strings.TrimLeft(after, tokenSeparators)
		}
		tmpl = before + after
	}
}

// Options 是 Gateway 的可选配置；零值字段使用默认值。
type Options struct {
	InboundTimeout time.Duration
	SendTimeout    time.Duration
	OutboundQueue  int
	Texts          Texts
	// Now 用于测试注入时钟；默认返回 UTC 的当前时间。
	Now func() time.Time
}

func (o Options) withDefaults() Options {
	if o.InboundTimeout <= 0 {
		o.InboundTimeout = DefaultInboundTimeout
	}
	if o.SendTimeout <= 0 {
		o.SendTimeout = DefaultSendTimeout
	}
	if o.OutboundQueue <= 0 {
		o.OutboundQueue = DefaultOutboundQueue
	}
	o.Texts = o.Texts.withDefaults()
	if o.Now == nil {
		o.Now = func() time.Time { return time.Now().UTC() }
	}
	return o
}

// ChannelStatus 是一个通道的运行时状态。
type ChannelStatus struct {
	ChannelID string            `json:"channel_id"`
	Kind      model.ChannelKind `json:"kind"`
	// Running 表示适配器已启动；Connected 表示适配器报告与平台连接正常（不报告状态的适配器等于 Running）。
	Running   bool      `json:"running"`
	Connected bool      `json:"connected"`
	LastError string    `json:"last_error,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
}

// route 记录一个会话的回复目标。
type route struct {
	agentID   string
	channelID string
	chatID    string
}

// outboundJob 是一条待发送的回复（分片前）。
type outboundJob struct {
	chatID  string
	text    string
	replyTo string
}

// runningChannel 是一个已启动通道的进程内状态。
type runningChannel struct {
	id        string
	kind      model.ChannelKind
	agentID   string
	cfg       ChannelConfig
	adapter   Adapter
	limiter   *limiter
	startedAt time.Time

	// ctx 覆盖该通道的出站发送；cancel 在停止通道时调用。
	ctx    context.Context
	cancel context.CancelFunc
	// stopped 置位后丢弃迟到的入站消息。
	stopped atomic.Bool

	qmu    sync.Mutex
	queue  chan outboundJob
	closed bool
	done   chan struct{}
}

// enqueue 把回复放入队列；队列已关闭或已满时返回 false。
func (rc *runningChannel) enqueue(job outboundJob) bool {
	rc.qmu.Lock()
	defer rc.qmu.Unlock()
	if rc.closed {
		return false
	}
	select {
	case rc.queue <- job:
		return true
	default:
		return false
	}
}

// closeQueue 关闭队列；可重复调用。
func (rc *runningChannel) closeQueue() {
	rc.qmu.Lock()
	defer rc.qmu.Unlock()
	if !rc.closed {
		rc.closed = true
		close(rc.queue)
	}
}

// Gateway 是 IM 通道路由器；并发安全。
type Gateway struct {
	channels model.ChannelRepo
	sender   Sender
	// sessions 在 sender 实现了 SessionGetter 时非 nil，用于在内存路由缺失时按会话键推导回复目标。
	sessions SessionGetter
	secrets  SecretReader
	bus      *event.Bus
	factory  *Factory
	log      *slog.Logger
	opts     Options
	now      func() time.Time

	// lifecycle 串行化 Start / Reload / Stop。
	lifecycle sync.Mutex
	// mu 保护下面的运行时表。
	mu      sync.Mutex
	started bool
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	running map[string]*runningChannel // channelID → 已启动通道
	routes  map[string]route           // sessionID → 回复目标
	replyTo map[string]replyRef        // runID → 触发该运行的入站消息
}

// replyRef 记录触发运行的入站消息，用于回复时引用。
type replyRef struct {
	messageID string
	at        time.Time
}

// New 构造 Gateway。channels、sender、factory 不得为 nil；sender 同时实现 SessionGetter 时启用按会话键推导回复路由；
// bus 为 nil 时不发送回复（只接收）；
// secrets 为 nil 时只能启动没有 SecretRef 的通道；logger 为 nil 时丢弃日志。
func New(channels model.ChannelRepo, sender Sender, secrets SecretReader, bus *event.Bus, factory *Factory, logger *slog.Logger, opts Options) *Gateway {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	opts = opts.withDefaults()
	sessions, _ := sender.(SessionGetter)
	return &Gateway{
		channels: channels,
		sender:   sender,
		sessions: sessions,
		secrets:  secrets,
		bus:      bus,
		factory:  factory,
		log:      logger,
		opts:     opts,
		now:      opts.Now,
		running:  make(map[string]*runningChannel),
		routes:   make(map[string]route),
		replyTo:  make(map[string]replyRef),
	}
}

// Start 加载所有启用的通道并启动适配器，然后开始消费事件总线上的回复事件。
//
// 单个通道启动失败只写入该通道的 LastError，不影响其他通道，也不让 Start 失败；
// 只有读取通道列表失败才返回错误。ctx 只约束 Start 本身；网关的生命周期由 Stop 结束。
func (g *Gateway) Start(ctx context.Context) error {
	g.lifecycle.Lock()
	defer g.lifecycle.Unlock()

	g.mu.Lock()
	if g.started {
		g.mu.Unlock()
		return model.NewError(model.CodeConflict, "网关已经启动")
	}
	g.started = true
	g.ctx, g.cancel = context.WithCancel(context.WithoutCancel(ctx))
	g.mu.Unlock()

	if g.bus != nil {
		// 在返回前完成首次订阅：Start 返回后发布的事件一定会被看到。
		sub := g.bus.Subscribe(g.ctx, g.bus.LastSeq(), eventBuffer, replyEventFilter)
		g.wg.Add(1)
		go g.eventLoop(g.ctx, sub)
	}

	list, err := g.channels.ListChannels(ctx, "")
	if err != nil {
		// 回滚到未启动状态，让调用方可以在修复后重试 Start。
		g.cancel()
		g.wg.Wait()
		g.mu.Lock()
		g.started = false
		g.mu.Unlock()
		return fmt.Errorf("读取通道列表: %w", err)
	}
	var wg sync.WaitGroup
	for _, ch := range list {
		if !ch.Enabled {
			continue
		}
		wg.Add(1)
		go func(ch *model.Channel) {
			defer wg.Done()
			g.startAndRecord(ctx, ch)
		}(ch)
	}
	wg.Wait()
	g.log.Info("网关已启动", "channels", len(list), "running", len(g.Statuses()))
	return nil
}

// Reload 热更新单个通道：停止旧适配器，重新读取通道记录；启用则按新配置启动。
// 通道不存在时返回 CodeNotFound（旧适配器已停止，可用于删除后的清理）；启动失败返回该错误并写入 LastError。
func (g *Gateway) Reload(ctx context.Context, channelID string) error {
	g.lifecycle.Lock()
	defer g.lifecycle.Unlock()

	g.mu.Lock()
	started := g.started
	rc := g.running[channelID]
	g.mu.Unlock()
	if !started {
		return model.NewError(model.CodeUnavailable, "网关尚未启动")
	}
	if rc != nil {
		g.stopChannel(ctx, rc)
	}
	ch, err := g.channels.GetChannel(ctx, channelID)
	if err != nil {
		return err
	}
	if !ch.Enabled {
		g.log.Info("通道已停用", "channel_id", channelID)
		return nil
	}
	return g.startAndRecord(ctx, ch)
}

// ForgetAgent 停止并移除某个智能体的全部通道适配器，并清除它的回复路由；供删除智能体后调用：
// 数据库中的通道记录已由外键级联删除，适配器若继续运行，入站消息会全部失败。
// 已排队的回复在 ctx 时限内发完。网关未启动或该智能体没有运行中的通道时是空操作；agentID 为空返回 CodeInvalid。
func (g *Gateway) ForgetAgent(ctx context.Context, agentID string) error {
	if agentID == "" {
		return model.NewError(model.CodeInvalid, "缺少智能体 ID")
	}
	g.lifecycle.Lock()
	defer g.lifecycle.Unlock()

	g.mu.Lock()
	var victims []*runningChannel
	for _, rc := range g.running {
		if rc.agentID == agentID {
			victims = append(victims, rc)
		}
	}
	for sid, rt := range g.routes {
		if rt.agentID == agentID {
			delete(g.routes, sid)
		}
	}
	g.mu.Unlock()

	var wg sync.WaitGroup
	for _, rc := range victims {
		wg.Add(1)
		go func(rc *runningChannel) {
			defer wg.Done()
			g.stopChannel(ctx, rc)
		}(rc)
	}
	wg.Wait()
	if len(victims) > 0 {
		g.log.Info("已停止被删除智能体的通道", "agent_id", agentID, "channels", len(victims))
	}
	return nil
}

// Stop 停止全部通道（先在 ctx 时限内把已排队的回复发完）并结束事件消费。可重复调用。
func (g *Gateway) Stop(ctx context.Context) error {
	g.lifecycle.Lock()
	defer g.lifecycle.Unlock()

	g.mu.Lock()
	if !g.started {
		g.mu.Unlock()
		return nil
	}
	all := make([]*runningChannel, 0, len(g.running))
	for _, rc := range g.running {
		all = append(all, rc)
	}
	g.mu.Unlock()

	var wg sync.WaitGroup
	for _, rc := range all {
		wg.Add(1)
		go func(rc *runningChannel) {
			defer wg.Done()
			g.stopChannel(ctx, rc)
		}(rc)
	}
	wg.Wait()

	g.cancel()
	g.wg.Wait()
	g.mu.Lock()
	g.started = false
	g.routes = make(map[string]route)
	g.replyTo = make(map[string]replyRef)
	g.mu.Unlock()
	g.log.Info("网关已停止")
	return nil
}

// Status 返回通道的运行时状态；未启动的通道返回 ok=false。
func (g *Gateway) Status(channelID string) (ChannelStatus, bool) {
	g.mu.Lock()
	rc := g.running[channelID]
	g.mu.Unlock()
	if rc == nil {
		return ChannelStatus{}, false
	}
	return rc.status(), true
}

// Statuses 返回全部已启动通道的状态。
func (g *Gateway) Statuses() []ChannelStatus {
	g.mu.Lock()
	out := make([]ChannelStatus, 0, len(g.running))
	for _, rc := range g.running {
		out = append(out, rc.status())
	}
	g.mu.Unlock()
	return out
}

// Adapter 返回通道当前的适配器实例（例如 CLI 需要拿到 *ConsoleAdapter 注入消息）。
func (g *Gateway) Adapter(channelID string) (Adapter, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	rc := g.running[channelID]
	if rc == nil {
		return nil, false
	}
	return rc.adapter, true
}

func (rc *runningChannel) status() ChannelStatus {
	st := ChannelStatus{ChannelID: rc.id, Kind: rc.kind, Running: true, Connected: true, StartedAt: rc.startedAt}
	if r, ok := rc.adapter.(StatusReporter); ok {
		as := r.Status()
		st.Connected = as.Connected
		st.LastError = as.LastError
	}
	return st
}

// startAndRecord 启动通道并把结果写回通道记录的 LastError。返回启动错误。
func (g *Gateway) startAndRecord(ctx context.Context, ch *model.Channel) error {
	secret := ""
	startErr := func() error {
		if ch.SecretRef != "" {
			if g.secrets == nil {
				return model.NewError(model.CodeUnavailable, "没有可用的凭据存储")
			}
			s, err := g.secrets.Get(ctx, ch.SecretRef)
			if err != nil {
				return model.Wrap(model.CodeOf(err), "读取通道凭据失败", err)
			}
			secret = s
		}
		return g.startChannel(ctx, ch, secret)
	}()

	msg := ""
	if startErr != nil {
		msg = redact(startErr.Error(), secret)
		g.log.Error("通道启动失败", "channel_id", ch.ID, "kind", string(ch.Kind), "error", msg)
	} else {
		g.log.Info("通道已启动", "channel_id", ch.ID, "kind", string(ch.Kind))
	}
	if ch.LastError != msg {
		updated := *ch
		updated.LastError = msg
		updated.UpdatedAt = g.now()
		if err := g.channels.UpdateChannel(ctx, &updated); err != nil {
			g.log.Error("写入通道状态失败", "channel_id", ch.ID, "error", err)
		}
	}
	if startErr != nil {
		return &model.Error{Code: model.CodeOf(startErr), Message: msg}
	}
	return nil
}

// startChannel 解析配置、创建适配器并启动；成功后登记到运行表。
func (g *Gateway) startChannel(ctx context.Context, ch *model.Channel, secret string) error {
	cfg, err := ParseChannelConfig(ch.Config)
	if err != nil {
		return err
	}
	adapter, err := g.factory.New(ch.Kind)
	if err != nil {
		return err
	}
	g.mu.Lock()
	if !g.started {
		g.mu.Unlock()
		return model.NewError(model.CodeUnavailable, "网关尚未启动")
	}
	parent := g.ctx
	g.mu.Unlock()

	rctx, cancel := context.WithCancel(parent)
	rc := &runningChannel{
		id:        ch.ID,
		kind:      ch.Kind,
		agentID:   ch.AgentID,
		cfg:       cfg,
		adapter:   adapter,
		limiter:   newLimiter(cfg.RatePerMinute, cfg.RateBurst, g.now),
		startedAt: g.now(),
		ctx:       rctx,
		cancel:    cancel,
		queue:     make(chan outboundJob, g.opts.OutboundQueue),
		done:      make(chan struct{}),
	}
	go g.worker(rc)

	snapshot := *ch
	inbox := func(in Inbound) { g.handleInbound(rc, in) }
	if err := adapter.Start(ctx, &snapshot, secret, inbox); err != nil {
		rc.stopped.Store(true)
		rc.closeQueue()
		cancel()
		<-rc.done
		return err
	}

	g.mu.Lock()
	prev := g.running[ch.ID]
	g.running[ch.ID] = rc
	g.mu.Unlock()
	if prev != nil {
		// 同一通道被并发启动了两次：保留后者，停掉前者。
		g.stopChannel(ctx, prev)
	}
	return nil
}

// stopChannel 停止一个通道：不再接收入站、在 ctx 时限内发完队列里的回复、停止适配器。
func (g *Gateway) stopChannel(ctx context.Context, rc *runningChannel) {
	rc.stopped.Store(true)
	g.mu.Lock()
	if g.running[rc.id] == rc {
		delete(g.running, rc.id)
	}
	g.mu.Unlock()

	rc.closeQueue()
	select {
	case <-rc.done:
	case <-ctx.Done():
		g.log.Warn("等待回复队列发完超时，放弃剩余回复", "channel_id", rc.id)
		rc.cancel()
		<-rc.done
	}
	rc.cancel()
	if err := rc.adapter.Stop(ctx); err != nil {
		g.log.Error("停止适配器失败", "channel_id", rc.id, "error", err)
	}
	g.log.Info("通道已停止", "channel_id", rc.id)
}

// handleInbound 处理一条入站消息：净化、白名单、群聊规则、限流，然后交给会话服务。
// 它在适配器的接收 goroutine 里同步执行，返回即代表消息已被受理或丢弃。
func (g *Gateway) handleInbound(rc *runningChannel, in Inbound) {
	if rc.stopped.Load() {
		return
	}
	if in.ChannelID == "" {
		in.ChannelID = rc.id
	}
	if in.Kind == "" {
		in.Kind = rc.kind
	}
	if in.At.IsZero() {
		in.At = g.now()
	}
	log := g.log.With("channel_id", rc.id, "chat_id", in.ChatID, "peer_id", in.PeerID)

	if in.ChatID == "" || in.PeerID == "" {
		log.Warn("入站消息缺少 chat_id 或 peer_id，丢弃")
		return
	}
	text := SanitizeText(in.Text, MaxInboundBytes)
	if !rc.cfg.PeerAllowed(in.PeerID) {
		log.Info("发送者不在白名单内，忽略")
		return
	}
	prefixed := false
	if p := rc.cfg.TriggerPrefix; p != "" && strings.HasPrefix(text, p) {
		prefixed = true
		text = strings.TrimSpace(strings.TrimPrefix(text, p))
	}
	if in.IsGroup && rc.cfg.GroupMode == GroupModeMention && !in.Mentioned && !prefixed {
		log.Debug("群消息未提及机器人，忽略")
		return
	}
	if text == "" && len(in.Attachments) == 0 {
		log.Debug("入站消息没有可用内容，忽略")
		return
	}
	if !rc.limiter.Allow(in.PeerID) {
		log.Info("对端触发限流，忽略")
		return
	}

	ctx, cancel := context.WithTimeout(rc.ctx, g.opts.InboundTimeout)
	defer cancel()
	key := SessionKey(rc.agentID, in.Kind, in.ChatID)
	sess, err := g.sender.EnsureByKey(ctx, rc.agentID, key, model.OriginChannel)
	if err != nil {
		log.Error("解析会话失败", "key", key, "error", err)
		g.notify(rc, in, g.opts.Texts.Unavailable)
		return
	}
	g.mu.Lock()
	g.routes[sess.ID] = route{agentID: rc.agentID, channelID: rc.id, chatID: in.ChatID}
	g.mu.Unlock()

	run, err := g.sender.Send(ctx, SendRequest{
		SessionID:   sess.ID,
		Text:        text,
		Attachments: in.Attachments,
		Trigger:     model.TriggerChannel,
		Principal: model.Principal{
			ID:   string(in.Kind) + ":" + in.PeerID,
			Kind: model.PrincipalChannelPeer,
			Name: in.PeerName,
		},
		Untrusted: true,
	})
	if err != nil {
		if model.IsCode(err, model.CodeConflict) {
			log.Info("会话仍有在途运行，提示稍后再发", "session_id", sess.ID)
			g.notify(rc, in, g.opts.Texts.Busy)
			return
		}
		log.Error("启动运行失败", "session_id", sess.ID, "error", err)
		g.notify(rc, in, g.opts.Texts.Unavailable)
		return
	}
	if in.MessageID != "" {
		g.rememberReply(run.ID, in.MessageID)
	}
	log.Debug("入站消息已受理", "session_id", sess.ID, "run_id", run.ID)
}

// notify 直接向入站消息所在的会话发送一条提示（不经过运行）。
func (g *Gateway) notify(rc *runningChannel, in Inbound, text string) {
	if !rc.enqueue(outboundJob{chatID: in.ChatID, text: text, replyTo: in.MessageID}) {
		g.log.Warn("回复队列已满或已关闭，丢弃提示", "channel_id", rc.id, "chat_id", in.ChatID)
	}
}

// rememberReply 记录运行对应的入站消息 ID，并在表过大时清理最旧的条目。
func (g *Gateway) rememberReply(runID, messageID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.replyTo) >= replyToMax {
		oldest, oldestAt := "", time.Time{}
		for id, ref := range g.replyTo {
			if oldest == "" || ref.at.Before(oldestAt) {
				oldest, oldestAt = id, ref.at
			}
		}
		delete(g.replyTo, oldest)
	}
	g.replyTo[runID] = replyRef{messageID: messageID, at: g.now()}
}

// replyEventFilter 只放行需要回复到通道的事件：运行终态、审批请求，以及主动推送的助手消息
// （push_text 定时任务经 session.Push 写入、带 Meta.pushed=true 的 message.created）。
// 它在发布方的 goroutine 里同步执行，因此只做内存判断。
func replyEventFilter(ev event.Event) bool {
	switch ev.Type {
	case string(model.EventRunCompleted), string(model.EventRunFailed),
		string(model.EventRunCancelled), string(model.EventApprovalRequested):
		return true
	case string(model.EventMessageCreated):
		re, ok := runEventOf(ev.Payload)
		return ok && isPushed(re.Message)
	}
	return false
}

// isPushed 判断消息是否是不经模型直接写入会话的主动推送：助手消息、不属于任何运行、Meta.pushed 为 true。
// 运行产出的助手消息随 run.completed 送达，不在这里重复发送。
func isPushed(m *model.Message) bool {
	if m == nil || m.Role != model.RoleAssistant || m.RunID != "" || len(m.Meta) == 0 {
		return false
	}
	var meta map[string]any
	if err := json.Unmarshal(m.Meta, &meta); err != nil {
		return false
	}
	pushed, _ := meta[metaPushed].(bool)
	return pushed
}

// eventLoop 消费 sub 上的终态与审批事件并把回复分发到各通道的发送队列；订阅因溢出被关闭时从最后的 Seq 重新订阅。
func (g *Gateway) eventLoop(ctx context.Context, sub *event.Subscription) {
	defer g.wg.Done()
	var since uint64
	for {
		delivered := 0
		for ev := range sub.C {
			since = ev.Seq
			delivered++
			g.handleEvent(ctx, ev)
		}
		if ctx.Err() != nil {
			return
		}
		if !sub.Lost() {
			return
		}
		if delivered == 0 {
			// 回放缓冲区已经没有 since 之后的事件：接受丢失，从最新位置继续，避免空转。
			latest := g.bus.LastSeq()
			g.log.Warn("事件订阅落后于回放缓冲区，跳过中间事件", "since", since, "latest", latest)
			since = latest
		} else {
			g.log.Warn("事件订阅因缓冲溢出被关闭，重新订阅", "since", since)
		}
		sub = g.bus.Subscribe(ctx, since, eventBuffer, replyEventFilter)
	}
}

// runEventOf 从事件 payload 中取出 RunEvent（值或指针）。
func runEventOf(payload any) (model.RunEvent, bool) {
	switch v := payload.(type) {
	case model.RunEvent:
		return v, true
	case *model.RunEvent:
		if v != nil {
			return *v, true
		}
	}
	return model.RunEvent{}, false
}

// handleEvent 把一条事件转成回复文本并放入对应通道的队列。
// 内存路由缺失时（守护进程重启后、定时任务写入的通道会话）按会话键推导路由，见 deriveRoute。
func (g *Gateway) handleEvent(ctx context.Context, ev event.Event) {
	re, ok := runEventOf(ev.Payload)
	if !ok {
		return
	}
	sessionID := firstNonEmpty(ev.SessionID, re.SessionID)
	runID := firstNonEmpty(ev.RunID, re.RunID)
	agentID := firstNonEmpty(ev.AgentID, re.AgentID)
	terminal := ev.Type == string(model.EventRunCompleted) || ev.Type == string(model.EventRunFailed) ||
		ev.Type == string(model.EventRunCancelled)

	g.mu.Lock()
	rt, routed := g.routes[sessionID]
	rc := g.running[rt.channelID]
	var (
		ref    replyRef
		hasRef bool
	)
	if runID != "" {
		ref, hasRef = g.replyTo[runID]
		if terminal && hasRef {
			delete(g.replyTo, runID)
		}
	}
	g.mu.Unlock()
	if !routed || rc == nil {
		if rt, rc = g.deriveRoute(ctx, sessionID, agentID); rc == nil {
			return
		}
	}

	var text string
	switch ev.Type {
	case string(model.EventRunCompleted):
		if re.Message != nil {
			text = strings.TrimSpace(re.Message.Text())
		}
		if text == "" {
			text = g.opts.Texts.EmptyReply
		}
	case string(model.EventRunFailed):
		text = g.opts.Texts.runFailedText(re.Error)
	case string(model.EventRunCancelled):
		text = g.opts.Texts.RunCancelled
	case string(model.EventApprovalRequested):
		summary, risk := "", ""
		if re.Approval != nil {
			summary, risk = re.Approval.Summary, string(re.Approval.Risk)
		}
		text = replaceTokens(g.opts.Texts.ApprovalRequired, map[string]string{"summary": summary, "risk": risk})
	case string(model.EventMessageCreated):
		if !isPushed(re.Message) {
			return
		}
		if text = strings.TrimSpace(re.Message.Text()); text == "" {
			return
		}
	default:
		return
	}
	job := outboundJob{chatID: rt.chatID, text: text}
	if hasRef {
		job.replyTo = ref.messageID
	}
	if !rc.enqueue(job) {
		g.log.Error("回复队列已满或已关闭，丢弃回复", "channel_id", rc.id, "session_id", sessionID, "run_id", runID, "type", ev.Type)
	}
}

// deriveRoute 在内存路由缺失时按会话的路由键推导回复目标，成功后写回路由表。
//
// 键形如 "<agent_id>:channel:<kind>:<chat_id>"（见 ParseSessionKey），目标是该智能体、该类型的已启动通道；
// 同类型有多个通道时取 ID 最小的，保证确定性。只有 Sender 实现了 SessionGetter、且事件所属智能体
// 有已启动的通道时才读取会话，避免为没有通道的智能体的每次运行读库。
func (g *Gateway) deriveRoute(ctx context.Context, sessionID, agentID string) (route, *runningChannel) {
	if g.sessions == nil || sessionID == "" || !g.hasChannelFor(agentID) {
		return route{}, nil
	}
	lctx, cancel := context.WithTimeout(ctx, g.opts.InboundTimeout)
	defer cancel()
	sess, err := g.sessions.Get(lctx, sessionID)
	if err != nil {
		if !model.IsCode(err, model.CodeNotFound) {
			g.log.Warn("读取会话以推导回复路由失败", "session_id", sessionID, "error", err)
		}
		return route{}, nil
	}
	aid, kind, chatID, ok := ParseSessionKey(sess.Key)
	if !ok || aid != sess.AgentID {
		return route{}, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	var pick *runningChannel
	for _, rc := range g.running {
		if rc.agentID == aid && rc.kind == kind && (pick == nil || rc.id < pick.id) {
			pick = rc
		}
	}
	if pick == nil {
		return route{}, nil
	}
	rt := route{agentID: aid, channelID: pick.id, chatID: chatID}
	g.routes[sessionID] = rt
	g.log.Debug("按会话键推导出回复路由", "session_id", sessionID, "channel_id", pick.id, "chat_id", chatID)
	return rt, pick
}

// hasChannelFor 报告是否有已启动的通道属于 agentID；agentID 为空时只要有任何已启动通道即为 true。
func (g *Gateway) hasChannelFor(agentID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, rc := range g.running {
		if agentID == "" || rc.agentID == agentID {
			return true
		}
	}
	return false
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// worker 顺序发送一个通道的回复，保证同一通道内回复按产生顺序到达。
func (g *Gateway) worker(rc *runningChannel) {
	defer close(rc.done)
	for job := range rc.queue {
		g.deliver(rc, job)
	}
}

// deliver 把一条回复分片后逐片发送；某一片失败时放弃剩余分片，避免对平台反复重试。
func (g *Gateway) deliver(rc *runningChannel, job outboundJob) {
	chunks := SplitText(job.text, rc.cfg.MaxChunk)
	for i, chunk := range chunks {
		if rc.ctx.Err() != nil {
			return
		}
		out := Outbound{ChannelID: rc.id, ChatID: job.chatID, Text: chunk}
		if i == 0 {
			out.ReplyTo = job.replyTo
		}
		ctx, cancel := context.WithTimeout(rc.ctx, g.opts.SendTimeout)
		err := rc.adapter.Send(ctx, out)
		cancel()
		if err != nil {
			g.log.Error("发送回复失败", "channel_id", rc.id, "chat_id", job.chatID, "chunk", i, "chunks", len(chunks), "error", err)
			return
		}
	}
}

// redact 把错误信息里出现的凭据替换为星号。
func redact(msg, secret string) string {
	if secret == "" {
		return msg
	}
	return strings.ReplaceAll(msg, secret, "***")
}
