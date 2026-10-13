package server_test

import (
	"context"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/server"
	"github.com/GoGoslingCA/gogosling/internal/tools"
)

// documentCapabilityDeps 记录意外运行的发现、权限、上下文或解析操作。
type documentCapabilityDeps struct{ calls int }

func (d *documentCapabilityDeps) ResolveRunContext(context.Context, string) (tools.RunContext, error) {
	d.calls++
	return tools.RunContext{}, nil
}
func (d *documentCapabilityDeps) Check(context.Context, tools.CheckRequest) (tools.Verdict, error) {
	d.calls++
	return tools.Verdict{}, nil
}
func (d *documentCapabilityDeps) ReadDocument(context.Context, tools.Workspace, string, tools.DocumentWindow) (tools.DocumentText, error) {
	d.calls++
	return tools.DocumentText{}, nil
}
func (d *documentCapabilityDeps) ToolsFor(context.Context, *model.Agent) ([]model.ToolSpec, error) {
	d.calls++
	return nil, nil
}
func (d *documentCapabilityDeps) CallTool(context.Context, *model.Agent, model.ToolCall) (model.ToolResult, error) {
	d.calls++
	return model.ToolResult{}, nil
}

func Test版本接口只宣告已接入的文档阅读器且不启动工具(t *testing.T) {
	for _, tc := range []struct {
		name       string
		registered bool
		ready      bool
	}{
		{"旧宿主未登记", false, false},
		{"登记但未配置阅读器", true, false},
		{"已接入阅读器", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			probe := &documentCapabilityDeps{}
			registry := tools.NewRegistry()
			if tc.registered {
				var reader tools.DocumentReader
				if tc.ready {
					reader = probe
				}
				if err := registry.Register(tools.NewReadDocument(reader)); err != nil {
					t.Fatal(err)
				}
			}
			executor, err := tools.NewExecutor(tools.Deps{Registry: registry, Resolver: probe, Permissions: probe, External: probe})
			if err != nil {
				t.Fatal(err)
			}
			deps := h.env.Deps
			deps.Tools = executor
			srv, err := server.New(deps, server.Options{ListenAddr: "127.0.0.1:0"})
			if err != nil {
				t.Fatal(err)
			}
			h.h = srv.Handler()
			rec := h.do("GET", "/api/v1/version", nil)
			mustStatus(t, rec, 200)
			var result struct {
				Capabilities map[string]any `json:"capabilities"`
			}
			result = decodeBody[struct {
				Capabilities map[string]any `json:"capabilities"`
			}](t, rec)
			capability, present := result.Capabilities["tools.read_document"]
			if present != tc.ready || (present && capability != float64(1)) || probe.calls != 0 {
				t.Fatalf("能力必须与静态接线一致且不触发任何工具：%v / %d", result.Capabilities, probe.calls)
			}
		})
	}
}
