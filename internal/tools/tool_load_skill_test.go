package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// fakeSkills 是按智能体扩展列表过滤的 SkillLoader。
type fakeSkills struct {
	bodies map[string]string // name → 正文
	agents []*model.Agent
	names  []string
	// has / hasErr 是 HasSkills 的返回值。
	has    bool
	hasErr error
}

func (f *fakeSkills) HasSkills(_ context.Context, _ *model.Agent) (bool, error) {
	return f.has, f.hasErr
}

func (f *fakeSkills) LoadSkill(_ context.Context, agent *model.Agent, name string) (string, error) {
	f.agents = append(f.agents, agent)
	f.names = append(f.names, name)
	body, ok := f.bodies[name]
	if !ok {
		return "", model.NotFound("skill", name)
	}
	return body, nil
}

func TestLoadSkillReturnsBodyForAgent(t *testing.T) {
	skills := &fakeSkills{bodies: map[string]string{"git-commit-message": "# 提交信息\n先看 diff。", "empty": " "}}
	f := newFixture(t, NewLoadSkill(skills))
	e := newExecutor(t, f, 0)

	res, err := e.Execute(context.Background(), "01RUN", call(LoadSkillName, `{"name":" git-commit-message "}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content, "先看 diff") {
		t.Fatalf("应返回技能正文，得到 %+v", res)
	}
	if len(skills.agents) != 1 || skills.agents[0] != f.rc.Agent || skills.names[0] != "git-commit-message" {
		t.Fatalf("应以运行上下文中的智能体与去空白的名称加载，得到 %v %v", skills.agents, skills.names)
	}
	req := f.checker.last(t)
	if req.Tool.Risk != model.RiskRead || !strings.Contains(req.Summary, "git-commit-message") {
		t.Fatalf("权限检查应收到 read 风险与技能摘要，得到 %+v", req)
	}

	res, err = e.Execute(context.Background(), "01RUN", call(LoadSkillName, `{"name":"empty"}`))
	if err != nil || res.IsError || !strings.Contains(res.Content, "没有正文") {
		t.Fatalf("空正文应给出说明，得到 %+v / %v", res, err)
	}
}

func TestLoadSkillErrorsAreToolResults(t *testing.T) {
	f := newFixture(t, NewLoadSkill(&fakeSkills{bodies: map[string]string{}}))
	e := newExecutor(t, f, 0)
	for _, args := range []string{`{"name":"missing"}`, `{"name":""}`, `{"name":"` + strings.Repeat("x", 200) + `"}`, `{}`} {
		res, err := e.Execute(context.Background(), "01RUN", call(LoadSkillName, args))
		if err != nil {
			t.Fatalf("参数 %s 不应返回 error: %v", args, err)
		}
		if !res.IsError {
			t.Fatalf("参数 %s 应得到错误结果，得到 %+v", args, res)
		}
	}
}

func TestLoadSkillIsImplicitlyEnabled(t *testing.T) {
	r := NewRegistry()
	read := NewReadFile()
	for _, tt := range []Tool{read, NewLoadSkill(&fakeSkills{})} {
		if err := r.Register(tt); err != nil {
			t.Fatal(err)
		}
	}
	// agent.Tools 只列了 read_file：load_skill 仍然可用（不受内置工具开关过滤）。
	agent := testAgent([]string{"read_file"})
	names := map[string]bool{}
	for _, s := range r.Specs(agent) {
		names[s.Name] = true
	}
	if !names["read_file"] || !names[LoadSkillName] || len(names) != 2 {
		t.Fatalf("应启用 read_file 与隐式的 load_skill，得到 %v", names)
	}
	if _, ok := r.Lookup(agent, LoadSkillName); !ok {
		t.Fatal("Lookup 应找到隐式启用的 load_skill")
	}
	// 普通工具的过滤语义不变：空列表时 read_file 不可用。
	none := testAgent([]string{})
	if _, ok := r.Lookup(none, "read_file"); ok {
		t.Fatal("空工具列表时 read_file 不应可用")
	}
	if specs := r.Specs(none); len(specs) != 1 || specs[0].Name != LoadSkillName {
		t.Fatalf("空工具列表时只应剩隐式工具，得到 %v", specs)
	}
}

func TestBuiltinRegistryRegistersLoadSkillOnlyWithLoader(t *testing.T) {
	r, err := NewBuiltinRegistry(BuiltinOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Get(LoadSkillName); ok {
		t.Fatal("没有 SkillLoader 时不应注册 load_skill")
	}
	r, err = NewBuiltinRegistry(BuiltinOptions{Skills: &fakeSkills{}})
	if err != nil {
		t.Fatal(err)
	}
	tool, ok := r.Get(LoadSkillName)
	if !ok {
		t.Fatal("提供 SkillLoader 时应注册 load_skill")
	}
	if spec := tool.Spec(); spec.Risk != model.RiskRead || spec.Source != SourceBuiltin {
		t.Fatalf("load_skill 应为 builtin / read，得到 %+v", spec)
	}
}
