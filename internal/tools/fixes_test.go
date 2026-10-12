package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/sandbox"
)

func specNames(specs []model.ToolSpec) []string {
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.Name)
	}
	return out
}

// LAY-03："没有技能时隐藏 load_skill"由 Executor.ToolsFor 决定；GET /agents/{id}/tools 与下发给工作进程的清单一致。
func TestToolsForHidesLoadSkillWithoutSkills(t *testing.T) {
	skills := &fakeSkills{bodies: map[string]string{}}
	f := newFixture(t, NewReadFile(), NewLoadSkill(skills))
	e := newExecutor(t, f, 0)
	agent := testAgent([]string{"read_file"})

	specs, err := e.ToolsFor(context.Background(), agent)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(specNames(specs), ","); got != "read_file" {
		t.Fatalf("没有技能时不应声明 load_skill，得到 %s", got)
	}

	skills.has = true
	specs, _ = e.ToolsFor(context.Background(), agent)
	if got := strings.Join(specNames(specs), ","); got != "read_file,"+LoadSkillName {
		t.Fatalf("有技能时应声明 load_skill，得到 %s", got)
	}

	skills.hasErr = errors.New("db down")
	specs, err = e.ToolsFor(context.Background(), agent)
	if err != nil || strings.Join(specNames(specs), ",") != "read_file" {
		t.Fatalf("查询技能失败时本次不声明 load_skill 且不报错，得到 %v / %v", specNames(specs), err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.ToolsFor(ctx, agent); !model.IsCode(err, model.CodeCancelled) {
		t.Fatalf("ctx 已取消时应返回 CodeCancelled，得到 %v", err)
	}

	// 隐藏的 load_skill 不能被同名扩展工具顶替：执行时内置工具优先，声明也不应出现扩展版本。
	skills.hasErr = nil
	skills.has = false
	f.external = &fakeExternal{specs: []model.ToolSpec{{Name: LoadSkillName, Description: "x", InputSchema: json.RawMessage(`{"type":"object"}`), Risk: model.RiskNetwork}}}
	e = newExecutor(t, f, 0)
	specs, _ = e.ToolsFor(context.Background(), agent)
	if got := strings.Join(specNames(specs), ","); got != "read_file" {
		t.Fatalf("同名扩展工具不应出现在清单中，得到 %s", got)
	}
}

// DOC-01：Executor 把工具的隔离事实交给权限层。
func TestExecutePassesIsolationToPermissionChecker(t *testing.T) {
	f := newFixture(t, NewRunCommand(&isolatedRunner{}), NewReadFile())
	mustWrite(t, f.ws, "a.txt", "x")
	e := newExecutor(t, f, 0)
	if _, err := e.Execute(context.Background(), "01RUN", call("run_command", `{"command":"ls"}`)); err != nil {
		t.Fatal(err)
	}
	if !f.checker.last(t).Isolated {
		t.Fatal("隔离 Runner 下的 run_command 应报告 Isolated")
	}
	if _, err := e.Execute(context.Background(), "01RUN", call("read_file", `{"path":"a.txt"}`)); err != nil {
		t.Fatal(err)
	}
	if f.checker.last(t).Isolated {
		t.Fatal("read_file 不经沙箱，不应报告 Isolated")
	}

	f = newFixture(t, NewRunCommand(sandbox.NewDirect(nil)))
	e = newExecutor(t, f, 0)
	f.checker.verdict = Verdict{Allowed: false, Reason: "no"}
	if _, err := e.Execute(context.Background(), "01RUN", call("run_command", `{"command":"echo hi"}`)); err != nil {
		t.Fatal(err)
	}
	if f.checker.last(t).Isolated {
		t.Fatal("Direct 下的 run_command 不应报告 Isolated")
	}
}

// C10：执行器截断结果时 ToolResult.Truncated 为 true；工具自己报告的截断也保留。
func TestExecuteMarksTruncatedResults(t *testing.T) {
	big := &stubTool{name: "big", execute: func(context.Context, RunContext, json.RawMessage) (model.ToolResult, error) {
		return model.ToolResult{Content: strings.Repeat("x", 500)}, nil
	}}
	small := &stubTool{name: "small", execute: func(context.Context, RunContext, json.RawMessage) (model.ToolResult, error) {
		return model.ToolResult{Content: "short"}, nil
	}}
	self := &stubTool{name: "self", execute: func(context.Context, RunContext, json.RawMessage) (model.ToolResult, error) {
		return model.ToolResult{Content: "partial", Truncated: true}, nil
	}}
	f := newFixture(t, big, small, self)
	e := newExecutor(t, f, 100)
	for name, want := range map[string]bool{"big": true, "small": false, "self": true} {
		res, err := e.Execute(context.Background(), "01RUN", call(name, `{}`))
		if err != nil {
			t.Fatal(err)
		}
		if res.Truncated != want {
			t.Errorf("%s: Truncated = %v，期望 %v", name, res.Truncated, want)
		}
	}
	raw, _ := json.Marshal(model.ToolResult{CallID: "c", Content: "x", Truncated: true})
	if !strings.Contains(string(raw), `"truncated":true`) {
		t.Fatalf("序列化结果应带 truncated: %s", raw)
	}
}

// LAY-16：工具参数 Schema 只有一套规则；不合格的扩展工具既不声明也不可调用。
func TestValidateInputSchema(t *testing.T) {
	for _, ok := range []string{`{"type":"object"}`, `{"type":"object","properties":{"a":{"type":"string"}}}`} {
		if err := ValidateInputSchema(json.RawMessage(ok)); err != nil {
			t.Errorf("%s 应合格: %v", ok, err)
		}
	}
	for _, bad := range []string{``, `null`, `[]`, `"object"`, `{}`, `{"type":"array"}`, `{"type":["object"]}`, `{"properties":{}}`} {
		if err := ValidateInputSchema(json.RawMessage(bad)); !model.IsCode(err, model.CodeInvalid) {
			t.Errorf("%q 应为 CodeInvalid，得到 %v", bad, err)
		}
	}
}

func TestExternalToolsWithInvalidSchemaAreSkipped(t *testing.T) {
	f := newFixture(t, &stubTool{name: "alpha"})
	f.external = &fakeExternal{
		specs: []model.ToolSpec{
			{Name: "plugin_ok", Description: "ok", InputSchema: json.RawMessage(`{"type":"object"}`), Source: "plugin:p", Risk: model.RiskNetwork},
			{Name: "plugin_array", Description: "bad", InputSchema: json.RawMessage(`{"type":"array"}`), Source: "plugin:p", Risk: model.RiskNetwork},
		},
		res: model.ToolResult{Content: "ran"},
	}
	e := newExecutor(t, f, 0)
	specs, err := e.ToolsFor(context.Background(), f.rc.Agent)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(specNames(specs), ","); got != "alpha,plugin_ok" {
		t.Fatalf("Schema 不合格的扩展工具应被跳过，得到 %s", got)
	}
	res, err := e.Execute(context.Background(), "01RUN", call("plugin_array", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content, string(model.CodeNotFound)) || len(f.external.calls) != 0 {
		t.Fatalf("Schema 不合格的扩展工具不应被调用: %+v calls=%v", res, f.external.calls)
	}
}
