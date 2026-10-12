package provider

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// 事件类型：Payload 均为 View（不含密钥），deleted 事件的 Payload 为 map{"id"}。
const (
	EventCreated = "provider.created"
	EventUpdated = "provider.updated"
	EventDeleted = "provider.deleted"
)

const (
	// SecretRefPrefix 是供应商密钥在 secret 存储中的引用前缀；完整引用见 SecretRef。
	SecretRefPrefix = "provider:"
	// MaxNameLength 是供应商名称的最大字符数。
	MaxNameLength = 64
	// MaxModelLength 是模型名的最大字符数。
	MaxModelLength = 128
)

// SecretRef 返回供应商 id 对应的密钥引用名。
func SecretRef(id string) string { return SecretRefPrefix + id }

// SecretStore 是本包对凭据存储的最小依赖，由 secret 包在组合根适配。
// Get 在引用不存在时返回 CodeNotFound；Delete 对不存在的引用可以返回 CodeNotFound 或 nil。
type SecretStore interface {
	Get(ctx context.Context, ref string) (string, error)
	Set(ctx context.Context, ref, value string) error
	Delete(ctx context.Context, ref string) error
	Has(ctx context.Context, ref string) (bool, error)
}

// AgentLister 用于删除前检查引用；model.AgentRepo 直接满足该接口。
type AgentLister interface {
	ListAgents(ctx context.Context) ([]*model.Agent, error)
}

// Input 是创建供应商的输入。APIKey 只写入 secret 存储，不进入 Provider 记录。
type Input struct {
	Name         string
	Kind         model.ProviderKind
	BaseURL      string
	APIKey       string
	DefaultModel string
	// Enabled 为 nil 时默认启用。
	Enabled *bool
}

// UpdateInput 是部分更新的输入：nil 字段保持不变。
// APIKey 为非空字符串时轮换密钥；为空字符串时删除密钥；nil 不动。
type UpdateInput struct {
	Name         *string
	Kind         *model.ProviderKind
	BaseURL      *string
	APIKey       *string
	DefaultModel *string
	Enabled      *bool
}

// View 是对外（API、事件）展示的供应商：用 HasSecret 代替 SecretRef，永不包含密钥。
type View struct {
	ID           string             `json:"id"`
	Name         string             `json:"name"`
	Kind         model.ProviderKind `json:"kind"`
	BaseURL      string             `json:"base_url,omitempty"`
	HasSecret    bool               `json:"has_secret"`
	DefaultModel string             `json:"default_model,omitempty"`
	Models       []string           `json:"models,omitempty"`
	Enabled      bool               `json:"enabled"`
	CreatedAt    time.Time          `json:"created_at"`
	UpdatedAt    time.Time          `json:"updated_at"`
}

// Service 管理模型供应商配置。并发安全：没有共享可变状态，持久化由仓储保证。
type Service struct {
	repo    model.ProviderRepo
	secrets SecretStore
	bus     *event.Bus
	client  *http.Client
	log     *slog.Logger
	agents  AgentLister
	now     func() time.Time
	// localOllamaURL 是 DiscoverLocal 探测的地址，测试可替换。
	localOllamaURL string
}

// Option 配置 Service 的可选依赖。
type Option func(*Service)

// WithAgentLister 提供引用检查所需的智能体列表；未提供时 Delete 不做引用检查。
func WithAgentLister(l AgentLister) Option {
	return func(s *Service) { s.agents = l }
}

// WithClock 替换时间源（测试用）。
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// WithLocalOllamaURL 替换本机 Ollama 的探测地址（测试用）。
func WithLocalOllamaURL(u string) Option {
	return func(s *Service) { s.localOllamaURL = u }
}

// New 创建供应商服务。client 为 nil 时使用带 ProbeTimeout 的默认客户端；logger 为 nil 时丢弃日志。
func New(repo model.ProviderRepo, secrets SecretStore, bus *event.Bus, client *http.Client, logger *slog.Logger, opts ...Option) *Service {
	if client == nil {
		client = &http.Client{Timeout: ProbeTimeout}
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	s := &Service{
		repo:           repo,
		secrets:        secrets,
		bus:            bus,
		client:         client,
		log:            logger,
		now:            time.Now,
		localOllamaURL: DefaultOllamaURL,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Create 校验输入、把密钥写入 secret 存储（引用 "provider:<id>"）并持久化记录。
// 密钥先写后建记录；记录创建失败时删除刚写入的密钥，避免孤儿密钥。
func (s *Service) Create(ctx context.Context, in Input) (*model.Provider, error) {
	name, err := validateName(in.Name)
	if err != nil {
		return nil, err
	}
	if err := validateKind(in.Kind); err != nil {
		return nil, err
	}
	base, err := normalizeBaseURL(in.Kind, in.BaseURL)
	if err != nil {
		return nil, err
	}
	defaultModel, err := validateModel(in.DefaultModel)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	p := &model.Provider{
		ID:           model.NewID(),
		Name:         name,
		Kind:         in.Kind,
		BaseURL:      base,
		DefaultModel: defaultModel,
		Enabled:      true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if in.Enabled != nil {
		p.Enabled = *in.Enabled
	}
	if key := strings.TrimSpace(in.APIKey); key != "" {
		p.SecretRef = SecretRef(p.ID)
		if err := s.secrets.Set(ctx, p.SecretRef, key); err != nil {
			return nil, fmt.Errorf("provider: 写入密钥: %w", err)
		}
	}
	if err := s.repo.CreateProvider(ctx, p); err != nil {
		if p.SecretRef != "" {
			if derr := s.secrets.Delete(ctx, p.SecretRef); derr != nil && !model.IsCode(derr, model.CodeNotFound) {
				s.log.Warn("回滚供应商密钥失败", "provider_id", p.ID, "error", derr)
			}
		}
		return nil, fmt.Errorf("provider: 创建供应商: %w", err)
	}
	s.publish(EventCreated, p)
	return p, nil
}

// Update 做部分更新。密钥轮换时先写 secret 再更新记录；若记录更新失败且此前没有密钥，
// 删除刚写入的密钥；此前已有密钥时轮换已经生效，只记录日志并返回错误。
func (s *Service) Update(ctx context.Context, id string, in UpdateInput) (*model.Provider, error) {
	current, err := s.repo.GetProvider(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("provider: 读取供应商: %w", err)
	}
	next := *current
	next.Models = append([]string(nil), current.Models...)

	if in.Name != nil {
		if next.Name, err = validateName(*in.Name); err != nil {
			return nil, err
		}
	}
	if in.Kind != nil {
		if err := validateKind(*in.Kind); err != nil {
			return nil, err
		}
		next.Kind = *in.Kind
	}
	if in.BaseURL != nil || in.Kind != nil {
		raw := next.BaseURL
		if in.BaseURL != nil {
			raw = *in.BaseURL
		}
		if next.BaseURL, err = normalizeBaseURL(next.Kind, raw); err != nil {
			return nil, err
		}
	}
	if in.DefaultModel != nil {
		if next.DefaultModel, err = validateModel(*in.DefaultModel); err != nil {
			return nil, err
		}
	}
	if in.Enabled != nil {
		next.Enabled = *in.Enabled
	}

	hadSecret := current.SecretRef != ""
	rotated, cleared := false, false
	if in.APIKey != nil {
		key := strings.TrimSpace(*in.APIKey)
		if key == "" {
			cleared = hadSecret
			next.SecretRef = ""
		} else {
			next.SecretRef = SecretRef(id)
			if err := s.secrets.Set(ctx, next.SecretRef, key); err != nil {
				return nil, fmt.Errorf("provider: 写入密钥: %w", err)
			}
			rotated = true
		}
	}
	next.UpdatedAt = s.now().UTC()
	if err := s.repo.UpdateProvider(ctx, &next); err != nil {
		if rotated && !hadSecret {
			if derr := s.secrets.Delete(ctx, next.SecretRef); derr != nil && !model.IsCode(derr, model.CodeNotFound) {
				s.log.Warn("回滚供应商密钥失败", "provider_id", id, "error", derr)
			}
		} else if rotated {
			s.log.Warn("密钥已轮换但记录更新失败", "provider_id", id, "error", err)
		}
		return nil, fmt.Errorf("provider: 更新供应商: %w", err)
	}
	if cleared {
		if err := s.secrets.Delete(ctx, current.SecretRef); err != nil && !model.IsCode(err, model.CodeNotFound) {
			s.log.Warn("删除供应商密钥失败", "provider_id", id, "error", err)
		}
	}
	s.publish(EventUpdated, &next)
	return &next, nil
}

// Get 返回供应商记录；记录本身不含密钥，SecretRef 只是引用名。
func (s *Service) Get(ctx context.Context, id string) (*model.Provider, error) {
	p, err := s.repo.GetProvider(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("provider: 读取供应商: %w", err)
	}
	return p, nil
}

// List 返回全部供应商，按创建时间升序。
func (s *Service) List(ctx context.Context) ([]*model.Provider, error) {
	items, err := s.repo.ListProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("provider: 列出供应商: %w", err)
	}
	return items, nil
}

// ProviderExists 判断供应商是否存在；供 agent 包的 ProviderChecker 使用。
func (s *Service) ProviderExists(ctx context.Context, id string) (bool, error) {
	if _, err := s.repo.GetProvider(ctx, id); err != nil {
		if model.IsCode(err, model.CodeNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("provider: 读取供应商: %w", err)
	}
	return true, nil
}

// HasSecret 查询 secret 存储确认密钥确实存在（而不只是记录里有引用）。
func (s *Service) HasSecret(ctx context.Context, p *model.Provider) (bool, error) {
	if p == nil || p.SecretRef == "" {
		return false, nil
	}
	ok, err := s.secrets.Has(ctx, p.SecretRef)
	if err != nil {
		return false, fmt.Errorf("provider: 查询密钥: %w", err)
	}
	return ok, nil
}

// View 构造对外视图；HasSecret 取自 secret 存储的实际状态。
func (s *Service) View(ctx context.Context, p *model.Provider) (View, error) {
	has, err := s.HasSecret(ctx, p)
	if err != nil {
		return View{}, err
	}
	return viewOf(p, has), nil
}

// Delete 删除供应商：仍被智能体引用时返回 CodeConflict（Details.agents 列出引用者）；
// 记录删除后再删除密钥。
func (s *Service) Delete(ctx context.Context, id string) error {
	p, err := s.repo.GetProvider(ctx, id)
	if err != nil {
		return fmt.Errorf("provider: 读取供应商: %w", err)
	}
	if s.agents != nil {
		agents, err := s.agents.ListAgents(ctx)
		if err != nil {
			return fmt.Errorf("provider: 检查引用: %w", err)
		}
		var refs []map[string]string
		for _, a := range agents {
			if a.Model.ProviderID == id {
				refs = append(refs, map[string]string{"id": a.ID, "name": a.Name})
			}
		}
		if len(refs) > 0 {
			return &model.Error{
				Code:    model.CodeConflict,
				Message: "供应商仍被智能体引用",
				Details: map[string]any{"agents": refs},
			}
		}
	}
	if err := s.repo.DeleteProvider(ctx, id); err != nil {
		return fmt.Errorf("provider: 删除供应商: %w", err)
	}
	if p.SecretRef != "" {
		if err := s.secrets.Delete(ctx, p.SecretRef); err != nil && !model.IsCode(err, model.CodeNotFound) {
			s.log.Warn("删除供应商密钥失败", "provider_id", id, "error", err)
		}
	}
	if s.bus != nil {
		s.bus.Publish(event.Event{Type: EventDeleted, Payload: map[string]string{"id": id}})
	}
	return nil
}

// Credentials 把模型引用解析为交给工作进程的一次性凭据。
// 供应商停用或密钥缺失返回 CodeUnavailable；未指定模型且没有默认模型返回 CodeInvalid。
// 只有 anthropic 严格要求密钥：openai_compatible 兼容本地无鉴权服务，ollama 默认不需要密钥。
func (s *Service) Credentials(ctx context.Context, ref model.ModelRef) (model.ProviderCredentials, error) {
	p, err := s.repo.GetProvider(ctx, ref.ProviderID)
	if err != nil {
		return model.ProviderCredentials{}, fmt.Errorf("provider: 读取供应商: %w", err)
	}
	if !p.Enabled {
		return model.ProviderCredentials{}, model.Errorf(model.CodeUnavailable, "供应商 %s 已停用", p.Name)
	}
	modelName := strings.TrimSpace(ref.Model)
	if modelName == "" {
		modelName = p.DefaultModel
	}
	if modelName == "" {
		return model.ProviderCredentials{}, model.Errorf(model.CodeInvalid, "供应商 %s 未指定模型", p.Name)
	}
	creds := model.ProviderCredentials{Kind: p.Kind, BaseURL: p.BaseURL, Model: modelName}
	if p.SecretRef != "" {
		key, err := s.secrets.Get(ctx, p.SecretRef)
		if err != nil {
			if model.IsCode(err, model.CodeNotFound) {
				return model.ProviderCredentials{}, model.Errorf(model.CodeUnavailable, "供应商 %s 的密钥缺失", p.Name)
			}
			return model.ProviderCredentials{}, fmt.Errorf("provider: 读取密钥: %w", err)
		}
		creds.APIKey = key
	}
	if creds.APIKey == "" && p.Kind == model.ProviderAnthropic {
		return model.ProviderCredentials{}, model.Errorf(model.CodeUnavailable, "供应商 %s 未配置密钥", p.Name)
	}
	return creds, nil
}

// publish 发布不含密钥的视图；事件路径不访问 secret 存储，HasSecret 以引用是否存在为准。
func (s *Service) publish(typ string, p *model.Provider) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(event.Event{Type: typ, Payload: viewOf(p, p.SecretRef != "")})
}

func viewOf(p *model.Provider, hasSecret bool) View {
	return View{
		ID:           p.ID,
		Name:         p.Name,
		Kind:         p.Kind,
		BaseURL:      p.BaseURL,
		HasSecret:    hasSecret,
		DefaultModel: p.DefaultModel,
		Models:       append([]string(nil), p.Models...),
		Enabled:      p.Enabled,
		CreatedAt:    p.CreatedAt,
		UpdatedAt:    p.UpdatedAt,
	}
}

func validateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", model.NewError(model.CodeInvalid, "供应商名称不能为空")
	}
	if utf8.RuneCountInString(name) > MaxNameLength {
		return "", model.Errorf(model.CodeInvalid, "供应商名称不能超过 %d 个字符", MaxNameLength)
	}
	return name, nil
}

func validateKind(kind model.ProviderKind) error {
	switch kind {
	case model.ProviderOpenAICompatible, model.ProviderAnthropic, model.ProviderOllama:
		return nil
	}
	return model.Errorf(model.CodeInvalid, "不支持的供应商类型 %q", string(kind))
}

func validateModel(name string) (string, error) {
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > MaxModelLength {
		return "", model.Errorf(model.CodeInvalid, "模型名不能超过 %d 个字符", MaxModelLength)
	}
	return name, nil
}

// normalizeBaseURL 校验并规范化服务地址：为空时按类型取默认值；只允许 http/https；
// 不允许带用户信息、查询串或片段；去掉末尾斜杠。
func normalizeBaseURL(kind model.ProviderKind, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if kind == model.ProviderOpenAICompatible {
			// 协议规定不得静默回退到某个服务商：用户必须显式给出地址（预设会自动填好）。
			return "", model.NewError(model.CodeInvalid, "openai_compatible 供应商必须提供 base_url（可从预设选择）")
		}
		raw = defaultBaseURL(kind)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", model.Wrap(model.CodeInvalid, "服务地址不合法", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", model.Errorf(model.CodeInvalid, "服务地址 %q 必须是 http(s) 绝对地址", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", model.Errorf(model.CodeInvalid, "服务地址 %q 不能包含用户信息、查询串或片段", raw)
	}
	return strings.TrimRight(raw, "/"), nil
}

// defaultBaseURL 返回有官方/本机默认地址的类型的服务地址；openai_compatible 没有默认值（见 normalizeBaseURL）。
func defaultBaseURL(kind model.ProviderKind) string {
	switch kind {
	case model.ProviderAnthropic:
		return "https://api.anthropic.com"
	case model.ProviderOllama:
		return DefaultOllamaURL
	default:
		return ""
	}
}
