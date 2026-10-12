package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"strings"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// ReadSkillResourceName 是技能引用资源的只读工具名。
const ReadSkillResourceName = "read_skill_resource"

// SkillResourceLoader 是 SkillLoader 的可选扩展，读取已为智能体启用的技能资源。
type SkillResourceLoader interface {
	LoadSkillResource(ctx context.Context, agent *model.Agent, name, path string) (string, error)
	HasSkillResources(ctx context.Context, agent *model.Agent) (bool, error)
}

type readSkillResource struct {
	loader SkillResourceLoader
	locale string
}

// NewReadSkillResource 创建只读资源工具，永不执行脚本；语言省略时使用中文。
func NewReadSkillResource(loader SkillResourceLoader, locale ...string) Tool {
	return readSkillResource{loader: loader, locale: toolLocale(locale)}
}

type readSkillResourceArgs struct {
	Name string `json:"name"`
	Path string `json:"resource_path"`
}

func (t readSkillResource) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name: ReadSkillResourceName, Description: toolText(t.locale, "resource_description", nil),
		InputSchema: objectSchema(map[string]any{
			"name":          stringProp(toolText(t.locale, "resource_name_argument", nil)),
			"resource_path": stringProp(toolText(t.locale, "resource_path_argument", nil)),
		}, "name", "resource_path"),
		Source: SourceBuiltin, Risk: model.RiskRead,
	}
}

func (readSkillResource) Implicit() bool { return true }

func (t readSkillResource) AvailableFor(ctx context.Context, agent *model.Agent) (bool, error) {
	if t.loader == nil || agent == nil {
		return false, nil
	}
	return t.loader.HasSkillResources(ctx, agent)
}

func (t readSkillResource) parse(raw json.RawMessage) (readSkillResourceArgs, error) {
	var a readSkillResourceArgs
	if err := decodeArgs(raw, &a); err != nil {
		return a, err
	}
	a.Name = strings.TrimSpace(a.Name)
	a.Path = strings.ReplaceAll(strings.TrimSpace(a.Path), "\\", "/")
	if a.Name == "" || len([]rune(a.Name)) > maxSkillNameRunes {
		return a, model.NewError(model.CodeInvalid, toolText(t.locale, "resource_name_invalid", nil))
	}
	if a.Path == "." || !fs.ValidPath(a.Path) || strings.ContainsAny(a.Path, ":\x00") {
		return a, model.NewError(model.CodeForbidden, toolText(t.locale, "resource_path_invalid", nil))
	}
	return a, nil
}

func (t readSkillResource) Describe(raw json.RawMessage) (Description, error) {
	a, err := t.parse(raw)
	if err != nil {
		return Description{}, err
	}
	return Description{
		Summary: toolText(t.locale, "resource_summary", map[string]any{"name": a.Name, "path": a.Path}),
		// resource_path 不是工作区路径；避免被权限层误认为来自工作区的文件。
		Details: map[string]any{"skill": a.Name, "resource_path": a.Path},
	}, nil
}

func (t readSkillResource) Execute(ctx context.Context, rc RunContext, raw json.RawMessage) (model.ToolResult, error) {
	a, err := t.parse(raw)
	if err != nil {
		return model.ToolResult{}, err
	}
	if t.loader == nil {
		return model.ToolResult{}, model.NewError(model.CodeUnavailable, toolText(t.locale, "resource_unavailable", nil))
	}
	content, err := t.loader.LoadSkillResource(ctx, rc.Agent, a.Name, a.Path)
	if err != nil {
		return model.ToolResult{}, err
	}
	sum := sha256.Sum256([]byte(content))
	header := toolText(t.locale, "resource_header", map[string]any{
		"name": a.Name, "path": a.Path, "bytes": len(content), "sha256": hex.EncodeToString(sum[:]),
	})
	return model.ToolResult{Content: header + "\n" + content}, nil
}
