package extension

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/protocol/mcp"
)

// 事件类型；Payload 为 *model.Extension（removed 事件携带删除前的记录）。
const (
	EventInstalled    = "extension.installed"
	EventUpdated      = "extension.updated"
	EventRemoved      = "extension.removed"
	EventToolsChanged = "extension.tools_changed"
)

const (
	// BundledSourcePrefix 是内置扩展的来源前缀："bundled:<name>"。
	BundledSourcePrefix = "bundled:"
	// PluginSourcePrefix 是插件工具在 ToolSpec.Source 中的前缀。
	PluginSourcePrefix = "extension:"
	// MaxSkillFileBytes 是 SKILL.md 的大小上限。
	MaxSkillFileBytes = 1 << 20
	// MaxManifestBytes 是 extension.json 的大小上限。
	MaxManifestBytes = 256 << 10

	bundledSkillsDir  = "skills"
	bundledPluginsDir = "plugins"
)

// Skill 是暴露给智能体的技能摘要：系统提示只放名称与描述，正文用 LoadSkill 按需加载。
type Skill struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Dir 是技能目录的绝对路径，供工具按相对路径读取技能附带的资源文件。
	Dir string `json:"dir"`
}

// SeedReport 是 SeedBundled 的结果。
type SeedReport struct {
	Installed []string `json:"installed"`
	Upgraded  []string `json:"upgraded"`
	// Failed 记录处理失败的条目（"<kind>/<name>: 原因"），不影响其它条目。
	Failed []string `json:"failed,omitempty"`
}

// toolRoute 把暴露给模型的工具名映射回扩展与原始工具名。
type toolRoute struct {
	extID string
	name  string
}

// Manager 管理扩展的生命周期并向智能体汇总它们的能力。并发安全。
type Manager struct {
	repo    model.ExtensionRepo
	dataDir string
	log     *slog.Logger
	dial    MCPDialer
	runner  ProcessRunner
	bus     *event.Bus
	now     func() time.Time

	pluginTimeout   time.Duration
	maxPluginOutput int
	lookupEnv       func(string) (string, bool)

	mu     sync.Mutex
	conns  map[string]*mcpConn
	routes map[string]map[string]toolRoute // agentID → 暴露名 → 路由
}

// Option 配置 Manager 的可选依赖。
type Option func(*Manager)

// WithBus 让 Manager 发布 extension.* 事件。
func WithBus(bus *event.Bus) Option {
	return func(m *Manager) { m.bus = bus }
}

// WithProcessRunner 替换插件子进程的启动方式（集成阶段换成沙箱实现）。
func WithProcessRunner(r ProcessRunner) Option {
	return func(m *Manager) {
		if r != nil {
			m.runner = r
		}
	}
}

// WithPluginTimeout 设置单次插件调用的超时（默认 60s）。
func WithPluginTimeout(d time.Duration) Option {
	return func(m *Manager) {
		if d > 0 {
			m.pluginTimeout = d
		}
	}
}

// WithMaxPluginOutput 设置插件响应的大小上限（默认 1 MiB）。
func WithMaxPluginOutput(n int) Option {
	return func(m *Manager) {
		if n > 0 {
			m.maxPluginOutput = n
		}
	}
}

// WithClock 替换时钟（测试用）。
func WithClock(now func() time.Time) Option {
	return func(m *Manager) {
		if now != nil {
			m.now = now
		}
	}
}

// New 创建 Manager。dataDir 是数据根目录（扩展放在 <dataDir>/extensions/）；
// dial 为 nil 时 MCP 扩展无法连接（ToolsFor 跳过并记录日志）。
func New(repo model.ExtensionRepo, dataDir string, logger *slog.Logger, dial MCPDialer, opts ...Option) *Manager {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	m := &Manager{
		repo:            repo,
		dataDir:         dataDir,
		log:             logger,
		dial:            dial,
		now:             func() time.Time { return time.Now().UTC() },
		pluginTimeout:   DefaultPluginTimeout,
		maxPluginOutput: DefaultMaxPluginOutput,
		lookupEnv:       os.LookupEnv,
		conns:           map[string]*mcpConn{},
		routes:          map[string]map[string]toolRoute{},
	}
	m.runner = &execRunner{log: logger}
	for _, o := range opts {
		o(m)
	}
	if m.dial == nil {
		m.dial = func(context.Context, *model.Extension) (MCPClient, error) {
			return nil, model.NewError(model.CodeUnavailable, "没有配置 MCP 拨号器")
		}
	}
	return m
}

func (m *Manager) publish(typ string, ext *model.Extension) {
	if m.bus == nil {
		return
	}
	cp := *ext
	m.bus.Publish(event.Event{Type: typ, Payload: &cp})
}

// ---- 安装 ----

// Install 根据目录内容安装扩展。有 SKILL.md 时按技能安装；否则有 extension.json 时按插件安装。
// 两者都在时按技能处理，避免同一目录被装成两种记录。path 必须是绝对路径。
// 登记 MCP 不走这里：那是配置而不是目录布局。
func (m *Manager) Install(ctx context.Context, path string) (*model.Extension, error) {
	if !filepath.IsAbs(path) {
		return nil, &model.Error{
			Code:    model.CodeInvalid,
			Message: "path 必须是绝对路径",
			Details: map[string]any{"reason": "path_not_absolute", "path": path},
		}
	}
	abs, fsys, err := m.openSourceDir(path)
	if err != nil {
		return nil, err
	}
	switch {
	case regularSourceFile(fsys, SkillFile):
		ext, _, err := m.installSkillFS(ctx, fsys, abs)
		return ext, err
	case regularSourceFile(fsys, ManifestFile):
		ext, _, err := m.installPluginFS(ctx, fsys, abs)
		return ext, err
	default:
		return nil, &model.Error{
			Code:    model.CodeInvalid,
			Message: "目录中既没有 SKILL.md 也没有 extension.json",
			Details: map[string]any{"reason": "unknown_extension_layout", "path": path},
		}
	}
}

func regularSourceFile(fsys fs.FS, name string) bool {
	info, err := fs.Stat(fsys, name)
	return err == nil && info.Mode().IsRegular()
}

// InstallSkill 从本地目录安装技能：校验 SKILL.md，复制到 <dataDir>/extensions/<id>/，计算摘要，写记录。
// 同名技能已存在时：内容相同则直接返回已有记录；不同则原地升级（保留 ID 与启用状态）。
// 新安装的技能默认启用（它只是文本，是否生效仍由 Agent.Extensions 决定）。
func (m *Manager) InstallSkill(ctx context.Context, srcDir string) (*model.Extension, error) {
	abs, fsys, err := m.openSourceDir(srcDir)
	if err != nil {
		return nil, err
	}
	ext, _, err := m.installSkillFS(ctx, fsys, abs)
	return ext, err
}

// installSkillFS 是技能安装的核心；source 写入记录的 Source。返回的 bool 表示是否新建了记录（false 为升级或未变）。
func (m *Manager) installSkillFS(ctx context.Context, fsys fs.FS, source string) (*model.Extension, bool, error) {
	raw, err := readFSFile(fsys, SkillFile, MaxSkillFileBytes)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, model.Errorf(model.CodeInvalid, "来源目录缺少 %s", SkillFile)
		}
		return nil, false, wrapInvalid("读取 "+SkillFile+" 失败", err)
	}
	manifest, _, err := ParseSkill(string(raw))
	if err != nil {
		return nil, false, err
	}
	digest, stats, err := digestFS(fsys)
	if err != nil {
		return nil, false, err
	}
	manifestJSON, _ := json.Marshal(manifest)

	existing, err := m.findByName(ctx, model.ExtensionSkill, manifest.Name)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		if existing.Digest == digest {
			m.log.Debug("技能内容未变，跳过安装", "extension_id", existing.ID, "name", manifest.Name)
			return existing, false, nil
		}
		ext, err := m.upgradeFiles(ctx, existing, fsys, source, manifest.Version, manifestJSON, digest)
		return ext, false, err
	}

	now := m.now()
	ext := &model.Extension{
		ID:          model.NewID(),
		Kind:        model.ExtensionSkill,
		Name:        manifest.Name,
		Version:     manifest.Version,
		Source:      source,
		Manifest:    manifestJSON,
		Enabled:     true,
		Digest:      digest,
		InstalledAt: now,
		UpdatedAt:   now,
	}
	ext, err = m.commitInstall(ctx, ext, fsys, stats)
	return ext, err == nil, err
}

// InstallPlugin 从本地目录安装插件：校验 extension.json，复制，计算摘要，写记录。
// 同名插件已存在时：内容相同则直接返回已有记录；不同则原地升级。
// 新安装的插件默认停用：它会运行第三方代码，必须由用户显式启用。
func (m *Manager) InstallPlugin(ctx context.Context, srcDir string) (*model.Extension, error) {
	abs, fsys, err := m.openSourceDir(srcDir)
	if err != nil {
		return nil, err
	}
	ext, _, err := m.installPluginFS(ctx, fsys, abs)
	return ext, err
}

// installPluginFS 是插件安装的核心；返回的 bool 表示是否新建了记录。
func (m *Manager) installPluginFS(ctx context.Context, fsys fs.FS, source string) (*model.Extension, bool, error) {
	raw, err := readFSFile(fsys, ManifestFile, MaxManifestBytes)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, model.Errorf(model.CodeInvalid, "来源目录缺少 %s", ManifestFile)
		}
		return nil, false, wrapInvalid("读取 "+ManifestFile+" 失败", err)
	}
	manifest, err := ParsePluginManifest(raw)
	if err != nil {
		return nil, false, err
	}
	digest, stats, err := digestFS(fsys)
	if err != nil {
		return nil, false, err
	}
	manifestJSON, _ := json.Marshal(manifest)

	existing, err := m.findByName(ctx, model.ExtensionPlugin, manifest.Name)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		if existing.Digest == digest {
			m.log.Debug("插件内容未变，跳过安装", "extension_id", existing.ID, "name", manifest.Name)
			return existing, false, nil
		}
		ext, err := m.upgradeFiles(ctx, existing, fsys, source, manifest.Version, manifestJSON, digest)
		return ext, false, err
	}

	now := m.now()
	ext := &model.Extension{
		ID:          model.NewID(),
		Kind:        model.ExtensionPlugin,
		Name:        manifest.Name,
		Version:     manifest.Version,
		Source:      source,
		Manifest:    manifestJSON,
		Enabled:     false,
		Digest:      digest,
		InstalledAt: now,
		UpdatedAt:   now,
	}
	ext, err = m.commitInstall(ctx, ext, fsys, stats)
	return ext, err == nil, err
}

// commitInstall 复制文件并写入新记录；写记录失败时回滚文件。
func (m *Manager) commitInstall(ctx context.Context, ext *model.Extension, fsys fs.FS, stats dirStats) (*model.Extension, error) {
	dir, err := m.stageFiles(fsys, ext.ID)
	if err != nil {
		return nil, err
	}
	ext.InstallDir = dir
	if err := m.repo.CreateExtension(ctx, ext); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("写入扩展记录失败: %w", err)
	}
	m.log.Info("扩展已安装", "extension_id", ext.ID, "kind", ext.Kind, "name", ext.Name,
		"version", ext.Version, "source", ext.Source, "files", stats.Files, "bytes", stats.Bytes)
	m.publish(EventInstalled, ext)
	return ext, nil
}

// upgradeFiles 用新内容替换已安装扩展的文件并更新记录（保留 ID、启用状态与安装时间）。
func (m *Manager) upgradeFiles(ctx context.Context, ext *model.Extension, fsys fs.FS, source, version string, manifest json.RawMessage, digest string) (*model.Extension, error) {
	dir, err := m.stageFiles(fsys, ext.ID)
	if err != nil {
		return nil, err
	}
	ext.InstallDir = dir
	ext.Source = source
	ext.Version = version
	ext.Manifest = manifest
	ext.Digest = digest
	ext.UpdatedAt = m.now()
	if err := m.repo.UpdateExtension(ctx, ext); err != nil {
		return nil, fmt.Errorf("更新扩展记录失败: %w", err)
	}
	m.invalidateRoutes()
	m.log.Info("扩展已升级", "extension_id", ext.ID, "kind", ext.Kind, "name", ext.Name, "version", ext.Version, "source", ext.Source)
	m.publish(EventUpdated, ext)
	return ext, nil
}

// RegisterMCP 登记一个 MCP 服务。cfg.Env 与 cfg.Headers（仅 http 传输）的值只能是字面量或
// "secret:<ref>" 引用，不要写入密钥本身。新登记的服务默认停用；Enable 时才会连接并固定工具摘要。
func (m *Manager) RegisterMCP(ctx context.Context, name string, cfg model.MCPServerConfig) (*model.Extension, error) {
	name = strings.TrimSpace(name)
	if err := validateDisplayName(name); err != nil {
		return nil, err
	}
	if err := mcp.ValidateConfig(cfg); err != nil {
		return nil, err
	}
	if existing, err := m.findByName(ctx, model.ExtensionMCPServer, name); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, &model.Error{Code: model.CodeConflict, Message: "同名的 MCP 服务已存在", Details: map[string]any{"id": existing.ID}}
	}
	if err := m.ensureRoot(); err != nil {
		return nil, err
	}
	cfgCopy := cloneMCPConfig(cfg)
	manifestJSON, _ := json.Marshal(cfgCopy)
	now := m.now()
	ext := &model.Extension{
		ID:          model.NewID(),
		Kind:        model.ExtensionMCPServer,
		Name:        name,
		Source:      "mcp:" + string(cfg.Transport),
		Manifest:    manifestJSON,
		MCP:         &cfgCopy,
		Enabled:     false,
		InstalledAt: now,
		UpdatedAt:   now,
	}
	ext.InstallDir = m.installDir(ext.ID)
	// 为 stdio 服务提供一个受控的工作目录。
	if err := os.MkdirAll(ext.InstallDir, dirPerm); err != nil {
		return nil, model.Wrap(model.CodeInternal, "创建 MCP 工作目录失败", err)
	}
	if err := m.repo.CreateExtension(ctx, ext); err != nil {
		_ = os.RemoveAll(ext.InstallDir)
		return nil, fmt.Errorf("写入扩展记录失败: %w", err)
	}
	m.log.Info("MCP 服务已登记", "extension_id", ext.ID, "name", name, "transport", cfg.Transport)
	m.publish(EventInstalled, ext)
	return ext, nil
}

// ---- 生命周期 ----

// Get 返回扩展记录。
func (m *Manager) Get(ctx context.Context, id string) (*model.Extension, error) {
	return m.repo.GetExtension(ctx, id)
}

// List 列出扩展；kind 为空表示全部。
func (m *Manager) List(ctx context.Context, kind model.ExtensionKind) ([]*model.Extension, error) {
	return m.repo.ListExtensions(ctx, kind)
}

// Enable 启用扩展。
//   - 插件：核对磁盘内容与安装时的摘要，被改动过则返回 CodeConflict（用 AcceptTools 确认或重新安装）；
//   - MCP 服务：建立连接并列出工具；首次启用时固定工具摘要，之后若已变化则返回 CodeConflict；
//   - 技能：直接启用。
func (m *Manager) Enable(ctx context.Context, id string) (*model.Extension, error) {
	ext, err := m.repo.GetExtension(ctx, id)
	if err != nil {
		return nil, err
	}
	switch ext.Kind {
	case model.ExtensionPlugin:
		if err := m.verifyDir(ext); err != nil {
			return nil, err
		}
	case model.ExtensionMCPServer:
		c, err := m.connectMCP(ctx, ext)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		changed := c.changed
		c.mu.Unlock()
		if changed {
			return nil, &model.Error{Code: model.CodeConflict, Message: "MCP 工具声明与首次启用时不同，请先确认（AcceptTools）", Details: map[string]any{"id": ext.ID}}
		}
	}
	if ext.Enabled {
		return ext, nil
	}
	ext.Enabled = true
	ext.UpdatedAt = m.now()
	if err := m.repo.UpdateExtension(ctx, ext); err != nil {
		return nil, err
	}
	m.invalidateRoutes()
	m.log.Info("扩展已启用", "extension_id", ext.ID, "kind", ext.Kind, "name", ext.Name)
	m.publish(EventUpdated, ext)
	return ext, nil
}

// Disable 停用扩展，并关闭它的 MCP 连接（若有）。
func (m *Manager) Disable(ctx context.Context, id string) (*model.Extension, error) {
	ext, err := m.repo.GetExtension(ctx, id)
	if err != nil {
		return nil, err
	}
	m.dropSlot(id)
	if !ext.Enabled {
		return ext, nil
	}
	ext.Enabled = false
	ext.UpdatedAt = m.now()
	if err := m.repo.UpdateExtension(ctx, ext); err != nil {
		return nil, err
	}
	m.invalidateRoutes()
	m.log.Info("扩展已停用", "extension_id", ext.ID, "kind", ext.Kind, "name", ext.Name)
	m.publish(EventUpdated, ext)
	return ext, nil
}

// Uninstall 删除扩展记录与文件，并关闭它的连接。记录先删、文件后删：文件删除失败只记录日志。
func (m *Manager) Uninstall(ctx context.Context, id string) error {
	ext, err := m.repo.GetExtension(ctx, id)
	if err != nil {
		return err
	}
	m.dropSlot(id)
	if err := m.repo.DeleteExtension(ctx, id); err != nil {
		return err
	}
	m.invalidateRoutes()
	if err := m.removeInstallDir(ext); err != nil {
		m.log.Warn("删除扩展文件失败，目录已成为孤儿", "extension_id", id, "dir", ext.InstallDir, "error", err)
	}
	m.log.Info("扩展已卸载", "extension_id", ext.ID, "kind", ext.Kind, "name", ext.Name)
	m.publish(EventRemoved, ext)
	return nil
}

// AcceptTools 让用户确认扩展的当前内容：
//   - MCP 服务：重新列出工具并把当前摘要固定为新的基准；
//   - 插件 / 技能：重新读取磁盘，更新清单与摘要。
func (m *Manager) AcceptTools(ctx context.Context, id string) (*model.Extension, error) {
	ext, err := m.repo.GetExtension(ctx, id)
	if err != nil {
		return nil, err
	}
	switch ext.Kind {
	case model.ExtensionMCPServer:
		c := m.slot(ext.ID)
		c.mu.Lock()
		if !c.alive() {
			c.closeLocked()
			client, err := m.dial(ctx, ext)
			if err != nil {
				c.lastErr = err.Error()
				c.mu.Unlock()
				return nil, err
			}
			c.client = client
		}
		specs, digest, _, err := c.client.ListToolsPinned(ctx, "")
		if err != nil {
			c.lastErr = err.Error()
			c.mu.Unlock()
			return nil, err
		}
		c.specs, c.digest, c.changed, c.lastErr = specs, digest, false, ""
		c.mu.Unlock()
		ext.Digest = digest
	case model.ExtensionPlugin, model.ExtensionSkill:
		fsys := os.DirFS(ext.InstallDir)
		digest, _, err := digestFS(fsys)
		if err != nil {
			return nil, err
		}
		manifestJSON, _, version, err := readManifest(ext.Kind, fsys)
		if err != nil {
			return nil, err
		}
		ext.Manifest = manifestJSON
		ext.Version = version
		ext.Digest = digest
	default:
		return nil, model.Errorf(model.CodeInvalid, "未知的扩展类型 %q", ext.Kind)
	}
	ext.UpdatedAt = m.now()
	if err := m.repo.UpdateExtension(ctx, ext); err != nil {
		return nil, err
	}
	m.invalidateRoutes()
	m.log.Info("扩展内容已确认", "extension_id", ext.ID, "kind", ext.Kind, "name", ext.Name, "digest", ext.Digest)
	m.publish(EventUpdated, ext)
	return ext, nil
}

// verifyDir 核对磁盘内容与记录的摘要。
func (m *Manager) verifyDir(ext *model.Extension) error {
	digest, _, err := digestFS(os.DirFS(ext.InstallDir))
	if err != nil {
		return err
	}
	if ext.Digest != "" && digest != ext.Digest {
		m.log.Warn("扩展文件与安装时不一致", "extension_id", ext.ID, "name", ext.Name, "expected", ext.Digest, "actual", digest)
		return &model.Error{
			Code:    model.CodeConflict,
			Message: "扩展文件与安装时不一致，请确认（AcceptTools）或重新安装",
			Details: map[string]any{"id": ext.ID, "expected": ext.Digest, "actual": digest},
		}
	}
	return nil
}

// Close 关闭全部 MCP 连接；守护进程退出时调用。
func (m *Manager) Close() error {
	m.mu.Lock()
	conns := m.conns
	m.conns = map[string]*mcpConn{}
	m.mu.Unlock()
	for _, c := range conns {
		c.mu.Lock()
		c.closeLocked()
		c.mu.Unlock()
	}
	return nil
}

// ---- 智能体视角 ----

// SkillsFor 返回智能体启用的技能摘要（按 Agent.Extensions 的顺序）。
func (m *Manager) SkillsFor(ctx context.Context, agent *model.Agent) ([]Skill, error) {
	if agent == nil {
		return nil, model.NewError(model.CodeInvalid, "agent 不能为空")
	}
	var out []Skill
	for _, ext := range m.enabledExtensions(ctx, agent) {
		if ext.Kind != model.ExtensionSkill {
			continue
		}
		var manifest SkillManifest
		if err := json.Unmarshal(ext.Manifest, &manifest); err != nil {
			m.log.Warn("技能清单无法解析，已跳过", "extension_id", ext.ID, "error", err)
			continue
		}
		out = append(out, Skill{ID: ext.ID, Name: manifest.Name, Description: manifest.Description, Dir: ext.InstallDir})
	}
	return out, nil
}

// LoadSkill 返回技能 SKILL.md 的正文（不含 frontmatter）。技能未启用时返回 CodeDenied。
func (m *Manager) LoadSkill(ctx context.Context, id string) (string, error) {
	ext, err := m.repo.GetExtension(ctx, id)
	if err != nil {
		return "", err
	}
	if ext.Kind != model.ExtensionSkill {
		return "", model.Errorf(model.CodeInvalid, "扩展 %s 不是技能", id)
	}
	if !ext.Enabled {
		return "", model.Errorf(model.CodeDenied, "技能 %s 未启用", ext.Name)
	}
	raw, err := readFileLimited(filepath.Join(ext.InstallDir, SkillFile), MaxSkillFileBytes)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", model.NotFound("skill_file", id)
		}
		return "", wrapInvalid("读取 "+SkillFile+" 失败", err)
	}
	_, body, err := ParseSkill(string(raw))
	if err != nil {
		return "", err
	}
	return body, nil
}

// ToolsFor 汇总智能体启用的 MCP 与插件工具。
//
// 工具名经过规范化（只含字母、数字、下划线与连字符）并在智能体范围内去重（冲突者加 _2、_3 后缀）；
// 原名记录在路由表里供 CallTool 分发。某个 MCP 服务连不上或工具声明已变化时，它的工具被整体跳过，
// 不影响其它扩展。
func (m *Manager) ToolsFor(ctx context.Context, agent *model.Agent) ([]model.ToolSpec, error) {
	if agent == nil {
		return nil, model.NewError(model.CodeInvalid, "agent 不能为空")
	}
	used := map[string]struct{}{}
	routes := map[string]toolRoute{}
	var specs []model.ToolSpec
	add := func(ext *model.Extension, spec model.ToolSpec) {
		exposed := uniqueToolName(sanitizeToolName(spec.Name), used)
		routes[exposed] = toolRoute{extID: ext.ID, name: spec.Name}
		if exposed != spec.Name {
			m.log.Debug("工具名已改写", "extension_id", ext.ID, "original", spec.Name, "exposed", exposed)
		}
		spec.Name = exposed
		specs = append(specs, spec)
	}

	for _, ext := range m.enabledExtensions(ctx, agent) {
		switch ext.Kind {
		case model.ExtensionPlugin:
			var manifest PluginManifest
			if err := json.Unmarshal(ext.Manifest, &manifest); err != nil {
				m.log.Warn("插件清单无法解析，已跳过", "extension_id", ext.ID, "error", err)
				continue
			}
			for _, t := range manifest.Tools {
				add(ext, model.ToolSpec{
					Name:        t.Name,
					Description: t.Description,
					InputSchema: t.InputSchema,
					Source:      model.ToolSource(PluginSourcePrefix + ext.ID),
					Risk:        manifest.Permissions.Risk(),
				})
			}
		case model.ExtensionMCPServer:
			c, err := m.connectMCP(ctx, ext)
			if err != nil {
				continue
			}
			c.mu.Lock()
			changed := c.changed
			list := append([]model.ToolSpec(nil), c.specs...)
			c.mu.Unlock()
			if changed {
				continue
			}
			for _, s := range list {
				add(ext, s)
			}
		}
	}

	m.mu.Lock()
	m.routes[agent.ID] = routes
	m.mu.Unlock()
	return specs, nil
}

// CallTool 把调用分发到对应扩展：插件走子进程协议，MCP 服务走连接。
// 路由表由 ToolsFor 建立；找不到时会重建一次，仍找不到返回 CodeNotFound。
func (m *Manager) CallTool(ctx context.Context, agent *model.Agent, call model.ToolCall) (model.ToolResult, error) {
	if agent == nil {
		return model.ToolResult{}, model.NewError(model.CodeInvalid, "agent 不能为空")
	}
	// 路由表可能是用同一智能体早先的扩展集合建立的；路由指向的扩展不在当前集合里就重建。
	route, ok := m.lookupRoute(agent.ID, call.Name)
	if !ok || !containsString(agent.Extensions, route.extID) {
		if _, err := m.ToolsFor(ctx, agent); err != nil {
			return model.ToolResult{}, err
		}
		if route, ok = m.lookupRoute(agent.ID, call.Name); !ok {
			return model.ToolResult{}, model.NotFound("tool", call.Name)
		}
	}
	ext, err := m.repo.GetExtension(ctx, route.extID)
	if err != nil {
		return model.ToolResult{}, err
	}
	if !ext.Enabled {
		return model.ToolResult{}, model.Errorf(model.CodeDenied, "扩展 %s 未启用", ext.Name)
	}

	var result model.ToolResult
	switch ext.Kind {
	case model.ExtensionPlugin:
		var manifest PluginManifest
		if err := json.Unmarshal(ext.Manifest, &manifest); err != nil {
			return model.ToolResult{}, model.Wrap(model.CodeInternal, "插件清单无法解析", err)
		}
		found := false
		for _, t := range manifest.Tools {
			if t.Name == route.name {
				found = true
				break
			}
		}
		if !found {
			return model.ToolResult{}, model.NotFound("tool", call.Name)
		}
		result, err = m.callPlugin(ctx, ext, manifest, route.name, call.Arguments)
	case model.ExtensionMCPServer:
		c, cerr := m.connectMCP(ctx, ext)
		if cerr != nil {
			return model.ToolResult{}, cerr
		}
		c.mu.Lock()
		client, changed := c.client, c.changed
		c.mu.Unlock()
		if changed {
			return model.ToolResult{}, model.NewError(model.CodeConflict, "MCP 工具声明已变化，等待用户确认")
		}
		// 调用本身不加锁：连接是并发安全的；连接在调用中断开时直接返回 unavailable，不重试，
		// 以免重复执行有副作用的工具（下一次调用会重连）。
		result, err = client.CallTool(ctx, route.name, call.Arguments)
	default:
		return model.ToolResult{}, model.Errorf(model.CodeInvalid, "扩展类型 %q 不提供工具", ext.Kind)
	}
	if err != nil {
		return model.ToolResult{}, err
	}
	result.CallID = call.ID
	return result, nil
}

func (m *Manager) lookupRoute(agentID, name string) (toolRoute, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.routes[agentID][name]
	return r, ok
}

// invalidateRoutes 在扩展集合变化后清空路由表；下一次 CallTool 会重建。
func (m *Manager) invalidateRoutes() {
	m.mu.Lock()
	m.routes = map[string]map[string]toolRoute{}
	m.mu.Unlock()
}

// enabledExtensions 按 Agent.Extensions 的顺序返回存在且已启用的扩展（去重）。
func (m *Manager) enabledExtensions(ctx context.Context, agent *model.Agent) []*model.Extension {
	seen := make(map[string]struct{}, len(agent.Extensions))
	out := make([]*model.Extension, 0, len(agent.Extensions))
	for _, id := range agent.Extensions {
		if _, dup := seen[id]; dup || id == "" {
			continue
		}
		seen[id] = struct{}{}
		ext, err := m.repo.GetExtension(ctx, id)
		if err != nil {
			if model.IsCode(err, model.CodeNotFound) {
				m.log.Debug("智能体引用的扩展不存在，已跳过", "agent_id", agent.ID, "extension_id", id)
			} else {
				m.log.Warn("读取扩展失败，已跳过", "agent_id", agent.ID, "extension_id", id, "error", err)
			}
			continue
		}
		if !ext.Enabled {
			continue
		}
		out = append(out, ext)
	}
	return out
}

// findByName 按类型与名称查找扩展；不存在返回 nil。
func (m *Manager) findByName(ctx context.Context, kind model.ExtensionKind, name string) (*model.Extension, error) {
	list, err := m.repo.ListExtensions(ctx, kind)
	if err != nil {
		return nil, err
	}
	for _, e := range list {
		if e.Name == name {
			return e, nil
		}
	}
	return nil, nil
}

// cloneMCPConfig 深拷贝 MCP 连接配置（切片与映射都复制），记录不与调用方共享可变状态。
func cloneMCPConfig(cfg model.MCPServerConfig) model.MCPServerConfig {
	out := cfg
	out.Args = append([]string(nil), cfg.Args...)
	out.Env = maps.Clone(cfg.Env)
	out.Headers = maps.Clone(cfg.Headers)
	return out
}

// ---- 内置扩展 ----

// SeedBundled 安装或升级随发行包分发的内置扩展。
//
// fsys 的布局：skills/<name>/SKILL.md、plugins/<name>/extension.json。每个条目的来源记为
// "bundled:<name>"；已存在时按摘要判断是否升级（保留 ID 与启用状态）。被用户用别的来源覆盖过
// （Source 不再以 bundled: 开头）的同名扩展不会被动回退。单个条目失败只记录，不影响其它条目。
func (m *Manager) SeedBundled(ctx context.Context, fsys fs.FS) (SeedReport, error) {
	var report SeedReport
	if fsys == nil {
		return report, model.NewError(model.CodeInvalid, "内置扩展文件系统不能为空")
	}
	type entry struct {
		kind model.ExtensionKind
		dir  string
		file string
	}
	groups := []entry{
		{model.ExtensionSkill, bundledSkillsDir, SkillFile},
		{model.ExtensionPlugin, bundledPluginsDir, ManifestFile},
	}
	for _, g := range groups {
		names, err := fs.ReadDir(fsys, g.dir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return report, model.Wrap(model.CodeInvalid, "读取内置扩展目录失败", err)
		}
		sort.Slice(names, func(i, j int) bool { return names[i].Name() < names[j].Name() })
		for _, d := range names {
			if !d.IsDir() || strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_") {
				continue
			}
			label := g.dir + "/" + d.Name()
			sub, err := fs.Sub(fsys, label)
			if err != nil {
				report.Failed = append(report.Failed, label+": "+err.Error())
				continue
			}
			if _, err := fs.Stat(sub, g.file); err != nil {
				m.log.Debug("内置扩展目录缺少清单，已跳过", "entry", label, "file", g.file)
				continue
			}
			source := BundledSourcePrefix + d.Name()
			if err := ctx.Err(); err != nil {
				return report, model.Wrap(model.CodeCancelled, "内置扩展安装被取消", err)
			}
			ext, installed, err := m.seedOne(ctx, g.kind, sub, source)
			if err != nil {
				m.log.Warn("内置扩展处理失败", "entry", label, "error", err)
				report.Failed = append(report.Failed, label+": "+err.Error())
				continue
			}
			switch {
			case ext == nil:
				// 未变化。
			case installed:
				report.Installed = append(report.Installed, ext.ID)
			default:
				report.Upgraded = append(report.Upgraded, ext.ID)
			}
		}
	}
	return report, nil
}

// seedOne 处理一个内置条目；返回 (记录, 是否新安装)；未变化或被用户接管时记录为 nil。
func (m *Manager) seedOne(ctx context.Context, kind model.ExtensionKind, sub fs.FS, source string) (*model.Extension, bool, error) {
	manifestJSON, name, version, err := readManifest(kind, sub)
	if err != nil {
		return nil, false, err
	}
	existing, err := m.findByName(ctx, kind, name)
	if err != nil {
		return nil, false, err
	}
	if existing == nil {
		var ext *model.Extension
		var created bool
		switch kind {
		case model.ExtensionSkill:
			ext, created, err = m.installSkillFS(ctx, sub, source)
		default:
			ext, created, err = m.installPluginFS(ctx, sub, source)
		}
		if err != nil {
			return nil, false, err
		}
		return ext, created, nil
	}
	if !strings.HasPrefix(existing.Source, BundledSourcePrefix) {
		m.log.Info("同名扩展已由用户从其它来源安装，内置版本不覆盖", "extension_id", existing.ID, "name", name, "source", existing.Source)
		return nil, false, nil
	}
	digest, _, err := digestFS(sub)
	if err != nil {
		return nil, false, err
	}
	if digest == existing.Digest {
		return nil, false, nil
	}
	ext, err := m.upgradeFiles(ctx, existing, sub, source, version, manifestJSON, digest)
	if err != nil {
		return nil, false, err
	}
	return ext, false, nil
}

// readManifest 读取并校验某类扩展的清单，返回规范化的 JSON、名称与版本。
func readManifest(kind model.ExtensionKind, fsys fs.FS) (json.RawMessage, string, string, error) {
	switch kind {
	case model.ExtensionSkill:
		raw, err := readFSFile(fsys, SkillFile, MaxSkillFileBytes)
		if err != nil {
			return nil, "", "", wrapInvalid("读取 "+SkillFile+" 失败", err)
		}
		manifest, _, err := ParseSkill(string(raw))
		if err != nil {
			return nil, "", "", err
		}
		out, _ := json.Marshal(manifest)
		return out, manifest.Name, manifest.Version, nil
	case model.ExtensionPlugin:
		raw, err := readFSFile(fsys, ManifestFile, MaxManifestBytes)
		if err != nil {
			return nil, "", "", wrapInvalid("读取 "+ManifestFile+" 失败", err)
		}
		manifest, err := ParsePluginManifest(raw)
		if err != nil {
			return nil, "", "", err
		}
		out, _ := json.Marshal(manifest)
		return out, manifest.Name, manifest.Version, nil
	default:
		return nil, "", "", model.Errorf(model.CodeInvalid, "扩展类型 %q 没有清单", kind)
	}
}

// wrapInvalid 把文件读取错误归一为 CodeInvalid（已带错误码的原样返回）。
func wrapInvalid(msg string, err error) error {
	var me *model.Error
	if errors.As(err, &me) {
		return err
	}
	return model.Wrap(model.CodeInvalid, msg, err)
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
