package app

import (
	"context"
	"log/slog"
	"strings"
	"unicode"

	"github.com/GoGoslingCA/gogosling/internal/engine"
	"github.com/GoGoslingCA/gogosling/internal/extension"
	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/session"
	"github.com/GoGoslingCA/gogosling/internal/tools"
)

// 本文件把 session 包的消费者接口接到 agent、engine、extension 的实现上。
// session.ToolSource 不需要适配：*tools.Executor 直接满足，会话与 GET /agents/{id}/tools 共用同一个 ToolsFor。

// agentGetter 是 *agent.Service 的读取能力。
type agentGetter interface {
	Get(ctx context.Context, id string) (*model.Agent, error)
}

// agentSource 实现 session.AgentSource。
type agentSource struct {
	agents agentGetter
}

// GetAgent 返回智能体；不存在时为 CodeNotFound。
func (a agentSource) GetAgent(ctx context.Context, id string) (*model.Agent, error) {
	return a.agents.Get(ctx, id)
}

// engineRuntime 是 *engine.Supervisor 的运行能力。
type engineRuntime interface {
	StartRun(ctx context.Context, req engine.RunRequest, sink engine.EventSink) error
	CancelRun(ctx context.Context, runID string) error
}

// runtimeAdapter 实现 session.Runtime：把 session.RunRequest 翻译成 engine.RunRequest。
type runtimeAdapter struct {
	rt engineRuntime
}

// StartRun 交给 engine；事件回调原样转交（两者都是 func(model.RunEvent)）。
//
// 注意不要再叠加 engine.BusSink：session 在回调里推进状态并自行发布到总线，叠加会重复发布。
func (r runtimeAdapter) StartRun(ctx context.Context, req session.RunRequest, sink func(model.RunEvent)) error {
	return r.rt.StartRun(ctx, toEngineRequest(req), engine.EventSink(sink))
}

// CancelRun 原样转交。
func (r runtimeAdapter) CancelRun(ctx context.Context, runID string) error {
	return r.rt.CancelRun(ctx, runID)
}

// toEngineRequest 做字段映射：智能体只取推理循环需要的四个字段（SystemPrompt 已由 session
// 换成 PromptBuilder 生成的最终提示），推理参数逐项拷贝，凭据只在这次调用的内存里经过。
func toEngineRequest(req session.RunRequest) engine.RunRequest {
	out := engine.RunRequest{
		RunID:       req.RunID,
		SessionID:   req.SessionID,
		Credentials: req.Credentials,
		Messages:    req.Messages,
		Tools:       req.Tools,
		Options: engine.RunOptions{
			MaxSteps:        req.Options.MaxSteps,
			Temperature:     req.Options.Temperature,
			MaxOutputTokens: req.Options.MaxOutputTokens,
		},
	}
	if req.Agent != nil {
		out.Agent = engine.AgentSpec{
			ID:           req.Agent.ID,
			Name:         req.Agent.Name,
			SystemPrompt: req.Agent.SystemPrompt,
			Autonomy:     req.Agent.Autonomy,
		}
	}
	return out
}

// skillLister 是 *extension.Manager 列出智能体技能的能力。
type skillLister interface {
	SkillsFor(ctx context.Context, agent *model.Agent) ([]extension.Skill, error)
}

// 技能清单写进系统提示时的长度上限：名称与描述来自第三方 SKILL.md，限制长度并压成单行，
// 既控制 token 也减少提示注入的表面。
const (
	maxSkillNameRunes = 64
	maxSkillDescRunes = 300
)

// skillPrompt 实现 session.PromptBuilder：在智能体的系统提示末尾追加"可用技能"清单与 load_skill 用法。
// 技能正文不进系统提示（渐进式披露），模型需要时调用 load_skill。
type skillPrompt struct {
	skills skillLister
	locale string
	log    *slog.Logger
}

// workbenchPrompt 提供简洁、可视化的默认输出建议，不修改用户保存的智能体提示或消息。
type workbenchPrompt struct{ skillPrompt }

func (p workbenchPrompt) BuildSystemPrompt(ctx context.Context, agent *model.Agent, sess *model.Session) (string, error) {
	base, err := p.skillPrompt.BuildSystemPrompt(ctx, agent, sess)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(base) + "\n\n" + i18n.T(p.locale, "prompt.tool_guidance", nil) + "\n\n" + i18n.T(p.locale, "prompt.output_style", nil), nil
}

// BuildSystemPrompt 返回最终系统提示。列技能失败只记日志并退回原提示，不让一次对话因此失败。
func (p skillPrompt) BuildSystemPrompt(ctx context.Context, agent *model.Agent, _ *model.Session) (string, error) {
	base := agent.SystemPrompt
	skills, err := p.skills.SkillsFor(ctx, agent)
	if err != nil {
		p.log.Warn("列出技能失败，系统提示不含技能清单", "agent_id", agent.ID, "error", err)
		return base, nil
	}
	if len(skills) == 0 {
		return base, nil
	}
	var b strings.Builder
	if trimmed := strings.TrimRight(base, " \t\r\n"); trimmed != "" {
		b.WriteString(trimmed)
		b.WriteString("\n\n")
	}
	b.WriteString(i18n.T(p.locale, "prompt.skills_header", nil))
	b.WriteString("\n")
	for _, s := range skills {
		b.WriteString(i18n.T(p.locale, "prompt.skill_item", map[string]any{
			"name":        oneLine(s.Name, maxSkillNameRunes),
			"id":          s.ID,
			"description": oneLine(s.Description, maxSkillDescRunes),
		}))
		b.WriteString("\n")
	}
	b.WriteString(i18n.T(p.locale, "prompt.skills_usage", map[string]any{"tool": tools.LoadSkillName}))
	return b.String(), nil
}

// oneLine 把文本压成单行（控制字符与换行替换为空格、合并空白）并按字符截断。
func oneLine(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}
