package tools

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/sandbox"
)

func TestRegistryRegisterValidates(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&stubTool{name: "Bad-Name"}); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("非法工具名应为 CodeInvalid，得到 %v", err)
	}
	if err := r.Register(&stubTool{name: "x", risk: "weird"}); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("非法风险等级应为 CodeInvalid，得到 %v", err)
	}
	if err := r.Register(nil); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("nil 工具应为 CodeInvalid，得到 %v", err)
	}
	if err := r.Register(&stubTool{name: "dup"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(&stubTool{name: "dup"}); !model.IsCode(err, model.CodeConflict) {
		t.Fatalf("重名应为 CodeConflict，得到 %v", err)
	}
}

func TestRegistryFiltersByAgentTools(t *testing.T) {
	r := NewRegistry()
	for _, n := range []string{"alpha", "beta", "gamma"} {
		if err := r.Register(&stubTool{name: n}); err != nil {
			t.Fatal(err)
		}
	}
	names := func(ts []Tool) []string {
		out := make([]string, 0, len(ts))
		for _, tt := range ts {
			out = append(out, tt.Spec().Name)
		}
		return out
	}
	if got := names(r.Enabled(nil)); !reflect.DeepEqual(got, []string{"alpha", "beta", "gamma"}) {
		t.Fatalf("nil agent 应得到全部工具，得到 %v", got)
	}
	if got := names(r.Enabled(testAgent(nil))); len(got) != 3 {
		t.Fatalf("Tools 为 nil 应得到全部工具，得到 %v", got)
	}
	if got := names(r.Enabled(testAgent([]string{}))); len(got) != 0 {
		t.Fatalf("空列表应没有工具，得到 %v", got)
	}
	if got := names(r.Enabled(testAgent([]string{"gamma", "alpha", "unknown"}))); !reflect.DeepEqual(got, []string{"alpha", "gamma"}) {
		t.Fatalf("应按注册顺序只返回已知且启用的工具，得到 %v", got)
	}
	if _, ok := r.Lookup(testAgent([]string{"alpha"}), "beta"); ok {
		t.Fatal("未启用的工具不应被 Lookup 找到")
	}
	if _, ok := r.Lookup(testAgent([]string{"alpha"}), "alpha"); !ok {
		t.Fatal("已启用的工具应被找到")
	}
	if _, ok := r.Lookup(nil, "nope"); ok {
		t.Fatal("未注册的工具不应被找到")
	}
	specs := r.Specs(testAgent([]string{"beta"}))
	if len(specs) != 1 || specs[0].Name != "beta" {
		t.Fatalf("Specs 应按启用过滤，得到 %+v", specs)
	}
}

func TestBuiltinRegistrySchemasAreValid(t *testing.T) {
	r, err := NewBuiltinRegistry(BuiltinOptions{Runner: sandbox.NewDirect(nil)})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"read_file", "write_file", "edit_file", "delete_file", "list_dir", "search_files", "run_command", "fetch_url"}
	if got := r.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("内置工具应为 %v，得到 %v", want, got)
	}
	risks := map[string]model.ToolRisk{
		"read_file": model.RiskRead, "write_file": model.RiskWrite, "edit_file": model.RiskWrite,
		"delete_file": model.RiskDestructive, "list_dir": model.RiskRead,
		"search_files": model.RiskRead, "run_command": model.RiskExecute, "fetch_url": model.RiskNetwork,
	}
	for _, spec := range r.Specs(nil) {
		if spec.Risk != risks[spec.Name] {
			t.Errorf("%s 的风险等级应为 %s，得到 %s", spec.Name, risks[spec.Name], spec.Risk)
		}
		if spec.Source != SourceBuiltin {
			t.Errorf("%s 的来源应为 builtin", spec.Name)
		}
		var schema struct {
			Type       string                    `json:"type"`
			Properties map[string]map[string]any `json:"properties"`
			Required   []string                  `json:"required"`
			Additional *bool                     `json:"additionalProperties"`
		}
		if err := json.Unmarshal(spec.InputSchema, &schema); err != nil {
			t.Errorf("%s 的 InputSchema 不是合法 JSON: %v", spec.Name, err)
			continue
		}
		if schema.Type != "object" || len(schema.Properties) == 0 || schema.Required == nil {
			t.Errorf("%s 的 InputSchema 结构不完整: %s", spec.Name, spec.InputSchema)
		}
		for name, p := range schema.Properties {
			if _, ok := p["type"]; !ok {
				t.Errorf("%s.%s 缺少 type", spec.Name, name)
			}
			if _, ok := p["description"]; !ok {
				t.Errorf("%s.%s 缺少 description", spec.Name, name)
			}
		}
		for _, req := range schema.Required {
			if _, ok := schema.Properties[req]; !ok {
				t.Errorf("%s 的 required 字段 %s 不在 properties 中", spec.Name, req)
			}
		}
	}
}

func TestBuiltinRegistryWithoutRunnerSkipsRunCommand(t *testing.T) {
	r, err := NewBuiltinRegistry(BuiltinOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Get("run_command"); ok {
		t.Fatal("没有 Runner 时不应注册 run_command")
	}
	if _, ok := r.Get("fetch_url"); !ok {
		t.Fatal("fetch_url 应始终注册")
	}
}
