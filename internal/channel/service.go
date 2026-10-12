package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// MaxNameRunes 是通道名称的最大字符数。
const MaxNameRunes = 64

// MaxSecretBytes 是单个通道凭据的上限；平台令牌远小于此，过长通常是粘贴错误。
const MaxSecretBytes = 8 << 10

// 通道运行状态（与 OpenAPI Channel.status 的取值一致）。
const (
	StateStopped  = "stopped"
	StateStarting = "starting"
	StateRunning  = "running"
	StateFailed   = "failed"
)

// SecretRef 返回通道凭据在 secret 存储中的名称。
func SecretRef(channelID string) string { return "channel:" + channelID + ":secret" }

// Repo 是本包需要的持久化能力：通道仓储，加上用于校验归属的 GetAgent。*store.Store 直接满足。
type Repo interface {
	model.ChannelRepo
	GetAgent(ctx context.Context, id string) (*model.Agent, error)
}

// Secrets 是凭据存储（期望由组合根的 secretAdapter 实现）。
//
// 返回的错误必须已经带 model.Code：不存在为 CodeNotFound，名称或内容不合法为 CodeInvalid，
// 后端故障为 CodeUnavailable。secret 包哨兵错误到 model.Code 的映射全仓只在组合根做一次，
// 本包不再识别哨兵错误；收到不带 model.Code 的错误一律按 unavailable 处理。
type Secrets interface {
	Get(ctx context.Context, name string) (string, error)
	Set(ctx context.Context, name, value string) error
	Delete(ctx context.Context, name string) error
	Has(ctx context.Context, name string) (bool, error)
}

// RuntimeStatus 是网关报告的单个通道运行状态（gateway.ChannelStatus 的最小子集）。
type RuntimeStatus struct {
	// Running 表示适配器已启动；Connected 表示适配器报告与平台连接正常。
	Running   bool
	Connected bool
	// LastError 是适配器自报的最近错误（已脱敏）。
	LastError string
}

// Gateway 是本包对 IM 网关的最小依赖（期望由 gateway.Gateway 在组合根适配）。
type Gateway interface {
	// Reload 按最新记录重启（或停止）该通道的适配器；记录已删除时返回 CodeNotFound。
	Reload(ctx context.Context, channelID string) error
	// ChannelStatus 返回通道的运行状态；没有在运行时 ok 为 false。
	ChannelStatus(channelID string) (RuntimeStatus, bool)
}

// KindChecker 报告网关支持的通道类型；*gateway.Factory 直接满足。
type KindChecker interface {
	Kinds() []model.ChannelKind
}

// Input 是创建通道的输入。Secret 只写入 secret 存储。
type Input struct {
	AgentID string
	Kind    model.ChannelKind
	Name    string
	Secret  string
	// Config 是平台相关的非敏感配置（JSON 对象）；为空表示没有配置。
	Config []byte
	// Enabled 为 nil 时默认启用。
	Enabled *bool
}

// Patch 是部分更新；nil 表示不修改。Secret 指向空串表示清除凭据，指向非空串表示轮换。
type Patch struct {
	Name    *string
	Secret  *string
	Config  []byte
	Enabled *bool
}

// Options 是 Service 的可选依赖；零值字段使用默认行为。
type Options struct {
	// Gateway 为 nil 表示通道功能未启用：CRUD 照常，Reload 返回 CodeUnavailable，Status 恒为 stopped。
	Gateway Gateway
	// Kinds 为 nil 时接受 model 中定义的全部类型。
	Kinds KindChecker
	// ValidateConfig 在保存前校验非敏感配置（组合根传入 gateway.ParseChannelConfig 的包装）；可为 nil。
	ValidateConfig func(raw json.RawMessage) error
	// Logger 为 nil 时丢弃日志。
	Logger *slog.Logger
	// Now 用于测试注入时钟；默认返回 UTC 的当前时间。
	Now func() time.Time
}

// Service 实现通道管理，并发安全。
type Service struct {
	repo    Repo
	secrets Secrets
	gw      Gateway
	kinds   KindChecker
	valid   func(json.RawMessage) error
	log     *slog.Logger
	now     func() time.Time

	// writeMu 串行化所有写操作（创建、更新、删除、重载）：写操作很少，
	// 串行化让"轮换凭据 → 写记录 → 重载"不会与另一次修改交错。
	writeMu sync.Mutex

	// mu 保护 failures：最近一次由本服务触发的重载失败原因（通道 ID → 已脱敏的消息）。
	mu       sync.Mutex
	failures map[string]string
}

// New 创建服务；repo 与 secrets 不得为 nil。
func New(repo Repo, secrets Secrets, opts Options) (*Service, error) {
	if repo == nil || secrets == nil {
		return nil, model.NewError(model.CodeInvalid, "channel: repo 与 secrets 不能为空")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		repo:     repo,
		secrets:  secrets,
		gw:       opts.Gateway,
		kinds:    opts.Kinds,
		valid:    opts.ValidateConfig,
		log:      logger,
		now:      now,
		failures: make(map[string]string),
	}, nil
}

// Create 创建通道：先写凭据，再写记录（失败时删除刚写入的凭据），启用的通道随后交给网关启动。
// 网关启动失败不会让创建失败：记录已经存在，失败原因写入 LastError 并由 Status 报告。
func (s *Service) Create(ctx context.Context, in Input) (*model.Channel, error) {
	if err := s.checkKind(in.Kind); err != nil {
		return nil, err
	}
	name, err := cleanName(in.Name)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.AgentID) == "" {
		return nil, model.NewError(model.CodeInvalid, "缺少智能体 ID")
	}
	if _, err := s.repo.GetAgent(ctx, in.AgentID); err != nil {
		return nil, err
	}
	cfg, err := s.cleanConfig(in.Config)
	if err != nil {
		return nil, err
	}
	if err := checkSecret(in.Secret); err != nil {
		return nil, err
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	now := s.now()
	ch := &model.Channel{
		ID:        model.NewID(),
		AgentID:   in.AgentID,
		Kind:      in.Kind,
		Name:      name,
		Config:    cfg,
		Enabled:   enabled,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if in.Secret != "" {
		ch.SecretRef = SecretRef(ch.ID)
		if err := s.secrets.Set(ctx, ch.SecretRef, in.Secret); err != nil {
			return nil, mapSecretErr(err, "写入通道凭据")
		}
	}
	if err := s.repo.CreateChannel(ctx, ch); err != nil {
		if ch.SecretRef != "" {
			s.deleteSecret(context.WithoutCancel(ctx), ch.SecretRef)
		}
		return nil, fmt.Errorf("创建通道记录: %w", err)
	}
	s.log.Info("通道已创建", "channel_id", ch.ID, "kind", string(ch.Kind), "agent_id", ch.AgentID, "has_secret", ch.SecretRef != "")
	if ch.Enabled {
		s.reloadQuiet(ctx, ch.ID)
	}
	return s.latest(ctx, ch), nil
}

// Get 返回通道记录；不存在时返回 CodeNotFound。
func (s *Service) Get(ctx context.Context, id string) (*model.Channel, error) {
	return s.repo.GetChannel(ctx, id)
}

// List 列出通道；agentID 为空时返回全部。
func (s *Service) List(ctx context.Context, agentID string) ([]*model.Channel, error) {
	return s.repo.ListChannels(ctx, agentID)
}

// Update 部分更新通道。凭据轮换先写新值、记录写入失败时恢复旧值；清除凭据在记录写入成功后才删除。
// 只有凭据、配置或启用状态变化时才让网关重载（只改名称不打断连接）。没有任何变化时不写库。
func (s *Service) Update(ctx context.Context, id string, p Patch) (*model.Channel, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	cur, err := s.repo.GetChannel(ctx, id)
	if err != nil {
		return nil, err
	}
	next := *cur
	changed, reload := false, false

	if p.Name != nil {
		name, err := cleanName(*p.Name)
		if err != nil {
			return nil, err
		}
		if name != cur.Name {
			next.Name, changed = name, true
		}
	}
	if p.Config != nil {
		cfg, err := s.cleanConfig(p.Config)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(cfg, cur.Config) {
			next.Config, changed, reload = cfg, true, true
		}
	}
	if p.Enabled != nil && *p.Enabled != cur.Enabled {
		next.Enabled, changed, reload = *p.Enabled, true, true
	}

	var (
		clearRef   string // 记录写入成功后要删除的凭据
		restore    func() // 记录写入失败时恢复凭据
		secretEdit bool
	)
	if p.Secret != nil {
		if *p.Secret == "" {
			if cur.SecretRef != "" {
				clearRef = cur.SecretRef
				next.SecretRef = ""
				secretEdit = true
			}
		} else {
			if err := checkSecret(*p.Secret); err != nil {
				return nil, err
			}
			ref := cur.SecretRef
			if ref == "" {
				ref = SecretRef(id)
			}
			restore, err = s.rotateSecret(ctx, ref, *p.Secret)
			if err != nil {
				return nil, err
			}
			next.SecretRef = ref
			secretEdit = true
		}
	}
	if secretEdit {
		changed, reload = true, true
	}
	if !changed {
		return cur, nil
	}

	next.UpdatedAt = s.now()
	if err := s.repo.UpdateChannel(ctx, &next); err != nil {
		if restore != nil {
			restore()
		}
		return nil, fmt.Errorf("更新通道记录: %w", err)
	}
	if clearRef != "" {
		s.deleteSecret(ctx, clearRef)
	}
	s.log.Info("通道已更新", "channel_id", id, "reload", reload, "secret_changed", secretEdit)
	if reload {
		s.reloadQuiet(ctx, id)
	}
	return s.latest(ctx, &next), nil
}

// Delete 删除通道记录与凭据，并让网关停止它的适配器。
func (s *Service) Delete(ctx context.Context, id string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	cur, err := s.repo.GetChannel(ctx, id)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteChannel(ctx, id); err != nil {
		return fmt.Errorf("删除通道记录: %w", err)
	}
	if cur.SecretRef != "" {
		s.deleteSecret(ctx, cur.SecretRef)
	}
	if s.gw != nil {
		// 记录已删除：网关先停掉旧实例，再因找不到记录返回 not_found，这就是期望的清理结果。
		if err := s.gw.Reload(ctx, id); err != nil && !model.IsCode(err, model.CodeNotFound) && !model.IsCode(err, model.CodeUnavailable) {
			s.log.Warn("删除通道后停止适配器失败", "channel_id", id, "error", err)
		}
	}
	s.setFailure(id, "")
	s.log.Info("通道已删除", "channel_id", id)
	return nil
}

// Reload 重启通道的适配器并返回最新记录；适配器启动失败时返回该错误（错误文本已由网关脱敏）。
func (s *Service) Reload(ctx context.Context, id string) (*model.Channel, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	ch, err := s.repo.GetChannel(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.gw == nil {
		return nil, model.NewError(model.CodeUnavailable, "通道功能未启用")
	}
	if err := s.reload(ctx, id); err != nil {
		return nil, err
	}
	return s.latest(ctx, ch), nil
}

// HasSecret 报告通道是否已配置凭据；查询失败时记日志并按未配置处理。
func (s *Service) HasSecret(ctx context.Context, ch *model.Channel) bool {
	if ch == nil || ch.SecretRef == "" {
		return false
	}
	ok, err := s.secrets.Has(ctx, ch.SecretRef)
	if err != nil {
		s.log.Warn("查询通道凭据失败", "channel_id", ch.ID, "error", err)
		return false
	}
	return ok
}

// Status 返回适配器运行状态（stopped / starting / running / failed）与最近的错误。
//
// 适配器在运行：连接正常为 running；未连接且有错误为 failed（适配器仍在后台重试）；
// 未连接且无错误为 starting。适配器不在运行：本服务最近一次重载失败为 failed，否则 stopped。
// 网关启动时就失败的通道，其原因持久化在记录的 LastError 中。
func (s *Service) Status(id string) (state string, lastError string) {
	if s.gw != nil {
		if st, ok := s.gw.ChannelStatus(id); ok && st.Running {
			switch {
			case st.Connected:
				return StateRunning, ""
			case st.LastError != "":
				return StateFailed, st.LastError
			default:
				return StateStarting, ""
			}
		}
	}
	s.mu.Lock()
	msg, failed := s.failures[id]
	s.mu.Unlock()
	if failed {
		return StateFailed, msg
	}
	return StateStopped, ""
}

// reload 让网关重载通道并记录结果。
func (s *Service) reload(ctx context.Context, id string) error {
	err := s.gw.Reload(ctx, id)
	if err != nil {
		s.setFailure(id, failureMessage(err))
		return err
	}
	s.setFailure(id, "")
	return nil
}

// reloadQuiet 在写操作之后重载通道；网关未启用时跳过，失败只记录不返回。
func (s *Service) reloadQuiet(ctx context.Context, id string) {
	if s.gw == nil {
		return
	}
	if err := s.reload(ctx, id); err != nil {
		s.log.Warn("通道重载失败", "channel_id", id, "error", failureMessage(err))
	}
}

// latest 重新读取记录（网关可能刚写入 LastError）；读取失败时退回 fallback。
func (s *Service) latest(ctx context.Context, fallback *model.Channel) *model.Channel {
	if ch, err := s.repo.GetChannel(ctx, fallback.ID); err == nil {
		return ch
	}
	return fallback
}

// rotateSecret 写入新凭据并返回恢复函数：旧值存在则写回，否则删除新值。
func (s *Service) rotateSecret(ctx context.Context, ref, value string) (func(), error) {
	old, err := s.secrets.Get(ctx, ref)
	hadOld := true
	if err != nil {
		if !model.IsCode(err, model.CodeNotFound) {
			return nil, mapSecretErr(err, "读取旧凭据")
		}
		hadOld = false
	}
	if err := s.secrets.Set(ctx, ref, value); err != nil {
		return nil, mapSecretErr(err, "写入通道凭据")
	}
	return func() {
		rctx := context.WithoutCancel(ctx)
		if hadOld {
			if err := s.secrets.Set(rctx, ref, old); err != nil {
				s.log.Error("恢复旧通道凭据失败", "secret_ref", ref, "error", err)
			}
			return
		}
		s.deleteSecret(rctx, ref)
	}, nil
}

// deleteSecret 尽力删除凭据；失败只记日志（孤立凭据不影响功能，下次同名写入会覆盖）。
func (s *Service) deleteSecret(ctx context.Context, ref string) {
	if err := s.secrets.Delete(ctx, ref); err != nil && !model.IsCode(err, model.CodeNotFound) {
		s.log.Warn("删除通道凭据失败", "secret_ref", ref, "error", err)
	}
}

func (s *Service) setFailure(id, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if msg == "" {
		delete(s.failures, id)
		return
	}
	s.failures[id] = msg
}

// checkKind 校验通道类型受网关支持。
func (s *Service) checkKind(kind model.ChannelKind) error {
	supported := s.supportedKinds()
	for _, k := range supported {
		if k == kind {
			return nil
		}
	}
	names := make([]string, len(supported))
	for i, k := range supported {
		names[i] = string(k)
	}
	return &model.Error{
		Code:    model.CodeInvalid,
		Message: fmt.Sprintf("不支持的通道类型 %q", string(kind)),
		Details: map[string]any{"kind": string(kind), "supported": names},
	}
}

func (s *Service) supportedKinds() []model.ChannelKind {
	if s.kinds != nil {
		return s.kinds.Kinds()
	}
	return []model.ChannelKind{
		model.ChannelConsole, model.ChannelTelegram, model.ChannelFeishu,
		model.ChannelDingTalk, model.ChannelWeCom, model.ChannelDiscord,
	}
}

// cleanConfig 校验配置是 JSON 对象（空输入视为没有配置）并压缩空白，再交给 ValidateConfig。
func (s *Service) cleanConfig(raw []byte) (json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	if raw[0] != '{' || !json.Valid(raw) {
		return nil, model.NewError(model.CodeInvalid, "通道配置必须是 JSON 对象")
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return nil, model.Wrap(model.CodeInvalid, "通道配置不是合法 JSON", err)
	}
	cfg := json.RawMessage(buf.Bytes())
	if s.valid != nil {
		if err := s.valid(cfg); err != nil {
			if model.CodeOf(err) == model.CodeInternal {
				return nil, model.Wrap(model.CodeInvalid, "通道配置无效", err)
			}
			return nil, err
		}
	}
	return cfg, nil
}

// cleanName 去首尾空白并校验名称：非空、不超过 MaxNameRunes、不含控制字符。
func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", model.NewError(model.CodeInvalid, "通道名称不能为空")
	}
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > MaxNameRunes {
		return "", model.Errorf(model.CodeInvalid, "通道名称不能超过 %d 个字符", MaxNameRunes)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", model.NewError(model.CodeInvalid, "通道名称不能包含控制字符")
		}
	}
	return name, nil
}

// checkSecret 拒绝明显错误的凭据（过长、含换行等控制字符）；空串表示没有凭据。
func checkSecret(v string) error {
	if v == "" {
		return nil
	}
	if len(v) > MaxSecretBytes {
		return model.NewError(model.CodeInvalid, "通道凭据过长")
	}
	if strings.TrimSpace(v) != v {
		return model.NewError(model.CodeInvalid, "通道凭据首尾不能有空白")
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return model.NewError(model.CodeInvalid, "通道凭据不能包含控制字符")
		}
	}
	return nil
}

// failureMessage 提取写入状态的失败原因：带错误码的错误只取 Message，避免底层细节。
func failureMessage(err error) string {
	var me *model.Error
	if errors.As(err, &me) && me.Message != "" {
		return me.Message
	}
	return err.Error()
}

// mapSecretErr 给凭据错误加上操作说明并保留 model.Code；不带 model.Code 的错误
// （不符合 Secrets 约定的实现）按 unavailable 处理。消息不含凭据值。
func mapSecretErr(err error, op string) error {
	if err == nil {
		return nil
	}
	var me *model.Error
	if errors.As(err, &me) {
		return fmt.Errorf("%s: %w", op, err)
	}
	return model.Wrap(model.CodeUnavailable, op+"：凭据存储不可用", err)
}
