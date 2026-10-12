package tools

import (
	"encoding/json"
	"log/slog"
	"regexp"
	"sync"

	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/sandbox"
)

// toolNameRe 限制工具名为小写字母、数字与下划线，与模型供应商对函数名的要求兼容。
var toolNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// SourceBuiltin 是内置工具的来源标识。
const SourceBuiltin model.ToolSource = "builtin"

// knownRisks 是 model.ToolRisk 的全部合法取值。
var knownRisks = map[model.ToolRisk]bool{
	model.RiskRead: true, model.RiskWrite: true, model.RiskExecute: true,
	model.RiskNetwork: true, model.RiskDestructive: true,
}

// Registry 登记内置工具，并按智能体配置过滤。可以并发使用。
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	order []string
}

// NewRegistry 创建空的注册表。
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register 登记一个工具：名称非法或声明无效返回 CodeInvalid，重名返回 CodeConflict。
func (r *Registry) Register(t Tool) error {
	if t == nil {
		return model.NewError(model.CodeInvalid, "工具不能为空")
	}
	spec := t.Spec()
	if !toolNameRe.MatchString(spec.Name) {
		return model.Errorf(model.CodeInvalid, "工具名 %q 不合法", spec.Name)
	}
	if spec.Description == "" {
		return model.Errorf(model.CodeInvalid, "工具 %s 缺少描述", spec.Name)
	}
	if !knownRisks[spec.Risk] {
		return model.Errorf(model.CodeInvalid, "工具 %s 的风险等级 %q 不合法", spec.Name, spec.Risk)
	}
	if err := ValidateInputSchema(spec.InputSchema); err != nil {
		return model.Wrap(model.CodeInvalid, "工具 "+spec.Name+" 的参数 Schema 无效", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.tools[spec.Name]; dup {
		return model.Errorf(model.CodeConflict, "工具 %s 已注册", spec.Name)
	}
	r.tools[spec.Name] = t
	r.order = append(r.order, spec.Name)
	return nil
}

// ValidateInputSchema 是工具参数 Schema 的唯一校验规则：必须是 JSON 对象，且 type 为 "object"
// （工具参数总是 JSON 对象；主流供应商也拒绝非 object 的 parameters / input_schema）。
// 内置工具在 Registry.Register 时校验；扩展工具（插件、MCP）在 Executor.ToolsFor 与查找时校验，
// 不合格的跳过并记录日志，因此不会下发给工作进程，也无法被调用。不合格返回 CodeInvalid。
func ValidateInputSchema(raw json.RawMessage) error {
	var s struct {
		Type json.RawMessage `json:"type"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return model.Wrap(model.CodeInvalid, "Schema 必须是 JSON 对象", err)
	}
	var typ string
	if len(s.Type) == 0 || json.Unmarshal(s.Type, &typ) != nil || typ != "object" {
		return model.NewError(model.CodeInvalid, "Schema 的 type 必须是 object")
	}
	return nil
}

// Get 按名称取工具，不考虑智能体配置。
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Names 按注册顺序返回全部工具名。
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.order...)
}

// Enabled 返回智能体可用的工具（按注册顺序）：
// agent 为 nil 或 agent.Tools 为 nil 时为全部工具；否则只有名字出现在 agent.Tools 里的工具，
// 未知名字被忽略；空的非 nil 列表表示没有工具。实现了 Implicit 的工具不受 agent.Tools 过滤。
func (r *Registry) Enabled(agent *model.Agent) []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if agent == nil || agent.Tools == nil {
		out := make([]Tool, 0, len(r.order))
		for _, n := range r.order {
			out = append(out, r.tools[n])
		}
		return out
	}
	want := make(map[string]bool, len(agent.Tools))
	for _, n := range agent.Tools {
		want[n] = true
	}
	out := make([]Tool, 0, len(want))
	for _, n := range r.order {
		if t := r.tools[n]; want[n] || isImplicit(t) {
			out = append(out, t)
		}
	}
	return out
}

// Lookup 返回智能体可用的指定工具；未注册或未启用都返回 false。
func (r *Registry) Lookup(agent *model.Agent, name string) (Tool, bool) {
	t, ok := r.Get(name)
	if !ok {
		return nil, false
	}
	if agent != nil && agent.Tools != nil && !isImplicit(t) {
		enabled := false
		for _, n := range agent.Tools {
			if n == name {
				enabled = true
				break
			}
		}
		if !enabled {
			return nil, false
		}
	}
	return t, true
}

// Specs 返回智能体可用工具的声明。
func (r *Registry) Specs(agent *model.Agent) []model.ToolSpec {
	tools := r.Enabled(agent)
	out := make([]model.ToolSpec, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Spec())
	}
	return out
}

// BuiltinOptions 是构造内置工具集的依赖。
type BuiltinOptions struct {
	// Locale 是新增文件工具的文案语言；省略时使用中文。
	Locale string
	// Runner 执行 run_command；为 nil 时不注册 run_command。
	Runner sandbox.Runner
	// Skills 读取技能正文；为 nil 时不注册 load_skill。
	Skills SkillLoader
	// Logger 可为 nil。
	Logger *slog.Logger
}

// NewBuiltinRegistry 创建并登记全部内置工具：
// read_file、write_file、edit_file、delete_file、list_dir、search_files、run_command（需要 Runner）、fetch_url、
// load_skill（需要 Skills）、read_skill_resource（需要可选 SkillResourceLoader）；
// 两个技能工具不受 agent.Tools 过滤，是否可用由 Conditional 决定。
func NewBuiltinRegistry(o BuiltinOptions) (*Registry, error) {
	logger := o.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	r := NewRegistry()
	list := []Tool{NewReadFile(), NewWriteFile(), NewEditFile(o.Locale), NewDeleteFile(o.Locale), NewListDir(), NewSearchFiles()}
	if o.Runner != nil {
		list = append(list, NewRunCommand(o.Runner))
	} else {
		logger.Warn("未提供沙箱 Runner，run_command 不可用")
	}
	list = append(list, NewFetchURL(logger))
	if o.Skills != nil {
		list = append(list, NewLoadSkill(o.Skills))
		if resources, ok := o.Skills.(SkillResourceLoader); ok {
			list = append(list, NewReadSkillResource(resources, o.Locale))
		}
	}
	for _, t := range list {
		if err := r.Register(t); err != nil {
			return nil, err
		}
	}
	return r, nil
}
