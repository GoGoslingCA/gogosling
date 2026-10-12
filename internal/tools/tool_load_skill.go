package tools

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// LoadSkillName 是 load_skill 工具的名称（系统提示里的技能清单用它引导模型调用）。
const LoadSkillName = "load_skill"

// maxSkillNameRunes 限制技能名参数的长度，防止模型把大段文本当参数传入。
const maxSkillNameRunes = 128

// SkillLoader 读取技能正文（期望由 extension 包在组合根适配实现）。
type SkillLoader interface {
	// LoadSkill 返回技能正文。name 是系统提示"可用技能"清单里给出的技能名或 ID。
	// 实现必须只返回对该智能体启用的技能：不存在或未对该智能体启用返回 CodeNotFound，
	// 技能被全局停用返回 CodeDenied。
	LoadSkill(ctx context.Context, agent *model.Agent, name string) (string, error)
	// HasSkills 报告该智能体当前是否启用了至少一个技能。Executor.ToolsFor 据此决定是否声明 load_skill：
	// 没有技能时系统提示里没有技能清单，模型也就不该看到这个工具。
	HasSkills(ctx context.Context, agent *model.Agent) (bool, error)
}

// Implicit 是 Tool 的可选扩展：Implicit 返回 true 的工具不受 agent.Tools 过滤，对所有智能体可用。
//
// agent.Tools 是"内置能力"的开关；像 load_skill 这样只为其他配置服务的辅助工具，是否有意义
// 由别处决定（技能由 agent.Extensions 启用），因此不应要求用户再在工具列表里勾选一次。
type Implicit interface {
	Implicit() bool
}

// isImplicit 判断工具是否实现了 Implicit 并返回 true。
func isImplicit(t Tool) bool {
	i, ok := t.(Implicit)
	return ok && i.Implicit()
}

// Conditional 是 Tool 的可选扩展（通常与 Implicit 一起实现）：报告该工具对某个智能体当前是否有意义。
// Executor.ToolsFor 只声明 AvailableFor 返回 true 的工具；查询失败时本次不声明该工具并记录日志。
// 这是"工具清单"唯一的过滤点：GET /agents/{id}/tools 与下发给工作进程的清单因此一致。
type Conditional interface {
	AvailableFor(ctx context.Context, agent *model.Agent) (bool, error)
}

// loadSkill 按需读取技能正文（渐进式披露：系统提示只列名称与描述，正文用到时再取）。
type loadSkill struct {
	loader SkillLoader
}

// NewLoadSkill 创建 load_skill 工具。
func NewLoadSkill(loader SkillLoader) Tool { return loadSkill{loader: loader} }

type loadSkillArgs struct {
	Name string `json:"name"`
}

func (loadSkill) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name: LoadSkillName,
		Description: "读取系统提示中“可用技能”清单里某个技能的完整说明（SKILL.md 正文）。" +
			"当任务与某个技能相关时，先用它读取说明再按说明行事；清单里没有的技能不要调用。",
		InputSchema: objectSchema(map[string]any{
			"name": stringProp("技能名称（或清单中给出的 ID）"),
		}, "name"),
		Source: SourceBuiltin,
		Risk:   model.RiskRead,
	}
}

// Implicit 让 load_skill 不受 agent.Tools 过滤：是否可用由智能体启用的技能决定。
func (loadSkill) Implicit() bool { return true }

// AvailableFor 实现 Conditional：智能体启用了至少一个技能时才声明 load_skill。
func (t loadSkill) AvailableFor(ctx context.Context, agent *model.Agent) (bool, error) {
	if t.loader == nil || agent == nil {
		return false, nil
	}
	return t.loader.HasSkills(ctx, agent)
}

func (loadSkill) parse(raw json.RawMessage) (loadSkillArgs, error) {
	var a loadSkillArgs
	if err := decodeArgs(raw, &a); err != nil {
		return a, err
	}
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" {
		return a, model.NewError(model.CodeInvalid, "name 不能为空")
	}
	if len([]rune(a.Name)) > maxSkillNameRunes {
		return a, model.NewError(model.CodeInvalid, "name 过长")
	}
	return a, nil
}

func (t loadSkill) Describe(raw json.RawMessage) (Description, error) {
	a, err := t.parse(raw)
	if err != nil {
		return Description{}, err
	}
	return Description{Summary: "读取技能说明 " + a.Name, Details: map[string]any{"skill": a.Name}}, nil
}

func (t loadSkill) Execute(ctx context.Context, rc RunContext, raw json.RawMessage) (model.ToolResult, error) {
	a, err := t.parse(raw)
	if err != nil {
		return model.ToolResult{}, err
	}
	if t.loader == nil {
		return model.ToolResult{}, model.NewError(model.CodeUnavailable, "技能加载不可用")
	}
	body, err := t.loader.LoadSkill(ctx, rc.Agent, a.Name)
	if err != nil {
		return model.ToolResult{}, err
	}
	if strings.TrimSpace(body) == "" {
		return model.ToolResult{Content: "技能 " + a.Name + " 没有正文。"}, nil
	}
	return model.ToolResult{Content: body}, nil
}
