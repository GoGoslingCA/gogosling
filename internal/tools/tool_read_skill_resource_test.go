package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

type fakeSkillResources struct {
	fakeSkills
	resources map[string]string
	calls     int
}

func (f *fakeSkillResources) HasSkillResources(ctx context.Context, agent *model.Agent) (bool, error) {
	return f.HasSkills(ctx, agent)
}

func (f *fakeSkillResources) LoadSkillResource(_ context.Context, _ *model.Agent, name, path string) (string, error) {
	f.calls++
	content, ok := f.resources[name+"/"+path]
	if !ok {
		return "", model.NotFound("skill_resource", path)
	}
	return content, nil
}

func TestSkillResourceToolIsConditionalAndChecksPermission(t *testing.T) {
	loader := &fakeSkillResources{resources: map[string]string{"notes/references/guide.md": "source instructions"}}
	registry, err := NewBuiltinRegistry(BuiltinOptions{Skills: loader})
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	f.registry = registry
	f.rc.Agent = testAgent([]string{})
	e := newExecutor(t, f, 0)
	specs, err := e.ToolsFor(context.Background(), f.rc.Agent)
	if err != nil || len(specs) != 0 {
		t.Fatalf("无技能时隐式资源工具应隐藏：%+v / %v", specs, err)
	}
	loader.has = true
	specs, err = e.ToolsFor(context.Background(), f.rc.Agent)
	if err != nil || strings.Join(specNames(specs), ",") != LoadSkillName+","+ReadSkillResourceName {
		t.Fatalf("启用技能后无需再手动选择资源工具：%+v / %v", specs, err)
	}
	f.checker.verdict = Verdict{Allowed: false, Reason: "denied"}
	invocation := call(ReadSkillResourceName, `{"name":"notes","resource_path":"references/guide.md"}`)
	res, err := e.Execute(context.Background(), "01RUN", invocation)
	if err != nil || !res.IsError || loader.calls != 0 {
		t.Fatalf("权限拒绝时不得读取资源：%+v / %v / %d", res, err, loader.calls)
	}
	f.checker.verdict = Verdict{Allowed: true}
	res, err = e.Execute(context.Background(), "01RUN", invocation)
	if err != nil || res.IsError || !strings.Contains(res.Content, "source instructions") || !strings.Contains(res.Content, "SHA-256:") || !strings.Contains(res.Content, "19 字节") {
		t.Fatalf("批准后应返回正文与精确文件元数据：%+v / %v", res, err)
	}
	req := f.checker.last(t)
	if req.Tool.Risk != model.RiskRead || !strings.Contains(string(req.Details), "resource_path") {
		t.Fatalf("资源调用应使用只读审批和资源路径元数据：%+v", req)
	}
	for _, path := range []string{"../secret.key", "/etc/passwd", `C:\secret`, "."} {
		invocation.Arguments = rawArgs(t, map[string]any{"name": "notes", "resource_path": path})
		res, err := e.Execute(context.Background(), "01RUN", invocation)
		if err != nil || !res.IsError || loader.calls != 1 {
			t.Fatalf("非法资源路径应在调用加载器前被拒绝：%s / %+v / %v", path, res, err)
		}
	}
}

func TestSkillResourceToolRequiresOptionalLoaderAndReportsTruncation(t *testing.T) {
	registry, err := NewBuiltinRegistry(BuiltinOptions{Skills: &fakeSkills{has: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get(ReadSkillResourceName); ok {
		t.Fatal("没有可选资源能力时不应注册资源工具")
	}
	loader := &fakeSkillResources{fakeSkills: fakeSkills{has: true}, resources: map[string]string{"notes/large.md": strings.Repeat("text", 300)}}
	f := newFixture(t, NewReadSkillResource(loader, "en"))
	res, err := newExecutor(t, f, 300).Execute(context.Background(), "01RUN", call(ReadSkillResourceName, `{"name":"notes","resource_path":"large.md"}`))
	if err != nil || res.IsError || !res.Truncated || !strings.Contains(res.Content, "1200 bytes") {
		t.Fatalf("结果截断应保留原文件大小并声明 truncated：%+v / %v", res, err)
	}
}
