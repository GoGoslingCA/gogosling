package tools

import (
	"context"
	"encoding/json"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"testing"
)

type fakeOperationResolver struct {
	context RunContext
	err     error
}

func (r fakeOperationResolver) ResolveOperationContext(context.Context, string) (RunContext, error) {
	return r.context, r.err
}
func userContext(t *testing.T, ws *fakeWorkspace) RunContext {
	rc := testRunContext(t, ws, nil)
	rc.RunID = ""
	rc.OperationID = "operation-1"
	rc.ChangeSetID = "change-1"
	rc.PreviewDigest = "preview-digest"
	rc.OperationRisk = model.RiskWrite
	rc.OperationSummary = "确认文件变更"
	rc.OperationDetails = json.RawMessage(`{"path":"notes.md","change_id":"change-1","preview_digest":"preview-digest","operation_kind":"replace"}`)
	return rc
}

func Test用户操作只允许绑定的变更工具且不暴露给模型(t *testing.T) {
	f := newFixture(t)
	manual := &stubTool{name: UserFileChangeTool, risk: model.RiskWrite}
	rc := userContext(t, f.ws)
	// 即使误注册到普通 Registry，模型发现与执行入口也必须排除它。
	if err := f.registry.Register(manual); err != nil {
		t.Fatal(err)
	}
	e, err := NewExecutor(Deps{Registry: f.registry, Resolver: fakeResolver{rc: f.rc}, Permissions: f.checker, OperationResolver: fakeOperationResolver{context: rc}, UserOperationTool: manual})
	if err != nil {
		t.Fatal(err)
	}
	specs, err := e.ToolsFor(context.Background(), rc.Agent)
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range specs {
		if spec.Name == UserFileChangeTool {
			t.Fatal("人工工具不能声明给模型")
		}
	}
	modelResult, err := e.Execute(context.Background(), "run-1", call(UserFileChangeTool, `{"change_id":"change-1","preview_digest":"preview-digest"}`))
	if err != nil || !modelResult.IsError || manual.calls != 0 {
		t.Fatal("模型Run不能执行人工文件工具")
	}
	result, err := e.ExecuteUserOperation(context.Background(), rc.OperationID, call(UserFileChangeTool, `{"change_id":"change-1","preview_digest":"preview-digest"}`))
	if err != nil || result.IsError || manual.calls != 1 {
		t.Fatalf("有效绑定应经权限执行: %v %+v", err, result)
	}
	checked := f.checker.last(t)
	if checked.OperationID != rc.OperationID || checked.RunID != "" || checked.Tool.Risk != model.RiskWrite {
		t.Fatalf("权限请求必须保留真实Operation而非Run: %+v", checked)
	}
}

func Test用户操作拒绝预览串换正文参数及伪造运行主体(t *testing.T) {
	for _, tc := range []struct {
		name, args string
		patch      func(*RunContext)
	}{
		{"目标变更不一致", `{"change_id":"other","preview_digest":"preview-digest"}`, nil},
		{"摘要不一致", `{"change_id":"change-1","preview_digest":"other"}`, nil},
		{"禁止内嵌正文", `{"change_id":"change-1","preview_digest":"preview-digest","content":"不应落入审批"}`, nil},
		{"混入模型Run", `{"change_id":"change-1","preview_digest":"preview-digest"}`, func(rc *RunContext) { rc.RunID = "fake-run" }},
		{"详情禁止正文", `{"change_id":"change-1","preview_digest":"preview-digest"}`, func(rc *RunContext) {
			rc.OperationDetails = json.RawMessage(`{"path":"notes.md","change_id":"change-1","preview_digest":"preview-digest","content":"private"}`)
		}},
		{"禁止网络风险", `{"change_id":"change-1","preview_digest":"preview-digest"}`, func(rc *RunContext) { rc.OperationRisk = model.RiskNetwork }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			rc := userContext(t, f.ws)
			if tc.patch != nil {
				tc.patch(&rc)
			}
			manual := &stubTool{name: UserFileChangeTool, risk: model.RiskWrite}
			e, err := NewExecutor(Deps{Registry: f.registry, Resolver: fakeResolver{rc: f.rc}, Permissions: f.checker, OperationResolver: fakeOperationResolver{context: rc}, UserOperationTool: manual})
			if err != nil {
				t.Fatal(err)
			}
			_, err = e.ExecuteUserOperation(context.Background(), rc.OperationID, call(UserFileChangeTool, tc.args))
			if !model.IsCode(err, model.CodeInvalid) || manual.calls != 0 || len(f.checker.reqs) != 0 {
				t.Fatalf("无效上下文不能进入审批或文件操作: %v", err)
			}
		})
	}
}

func Test用户操作传递删除风险并保留拒绝和冲突错误码(t *testing.T) {
	f := newFixture(t)
	rc := userContext(t, f.ws)
	rc.OperationRisk = model.RiskDestructive
	manual := &stubTool{name: UserFileChangeTool, risk: model.RiskWrite, execute: func(context.Context, RunContext, json.RawMessage) (model.ToolResult, error) {
		return model.ToolResult{}, model.NewError(model.CodeConflict, "基线变化")
	}}
	e, err := NewExecutor(Deps{Registry: f.registry, Resolver: fakeResolver{rc: f.rc}, Permissions: f.checker, OperationResolver: fakeOperationResolver{context: rc}, UserOperationTool: manual})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.ExecuteUserOperation(context.Background(), rc.OperationID, call(UserFileChangeTool, `{"change_id":"change-1","preview_digest":"preview-digest"}`))
	if !model.IsCode(err, model.CodeConflict) || f.checker.last(t).Tool.Risk != model.RiskDestructive {
		t.Fatalf("实际删除风险和冲突代码必须向上传递: %v", err)
	}
	f.checker.verdict.Allowed = false
	_, err = e.ExecuteUserOperation(context.Background(), rc.OperationID, call(UserFileChangeTool, `{"change_id":"change-1","preview_digest":"preview-digest"}`))
	if !model.IsCode(err, model.CodeDenied) || manual.calls != 1 {
		t.Fatalf("拒绝应直接返回denied且不得执行: %v", err)
	}
}
