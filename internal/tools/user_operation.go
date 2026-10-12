package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"io"
	"strings"
	"time"
)

// UserFileChangeTool 是只接收持久化预览标识的人工文件工具，禁止声明给模型。
const UserFileChangeTool = model.OperationFileChangeTool

func operationError(code model.Code, key string) error {
	return model.NewError(code, i18n.T("en", "operation."+key, nil))
}
func executionError(rc RunContext, err error) (model.ToolResult, error) {
	if rc.OperationID != "" {
		return model.ToolResult{}, err
	}
	return errorResult(err), nil
}

// ExecuteUserOperation 校验持久操作上下文及预览绑定，再进入与模型工具相同的权限执行入口。
// 人工操作直接返回领域错误码，不把失败转成供模型阅读的字符串。
func (e *Executor) ExecuteUserOperation(ctx context.Context, operationID string, call model.ToolCall) (model.ToolResult, error) {
	if err := ctxErr(ctx); err != nil {
		return model.ToolResult{}, err
	}
	if e.operationResolver == nil || e.userOperationTool == nil {
		return model.ToolResult{}, operationError(model.CodeUnavailable, "unavailable")
	}
	if operationID == "" || strings.TrimSpace(call.ID) == "" || call.Name != UserFileChangeTool {
		return model.ToolResult{}, operationError(model.CodeInvalid, "unsupported_tool")
	}
	var args struct {
		ChangeID      string `json:"change_id"`
		PreviewDigest string `json:"preview_digest"`
	}
	decoder := json.NewDecoder(bytes.NewReader(call.Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil || args.ChangeID == "" || args.PreviewDigest == "" {
		return model.ToolResult{}, operationError(model.CodeInvalid, "invalid_arguments")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return model.ToolResult{}, operationError(model.CodeInvalid, "invalid_arguments")
	}
	// 重新序列化已识别字段，避免重复 JSON 键在审批记录中夹带被解析器覆盖的正文。
	call.Arguments, _ = json.Marshal(args)
	rc, err := e.operationResolver.ResolveOperationContext(ctx, operationID)
	if err != nil {
		return model.ToolResult{}, err
	}
	if rc.OperationID != operationID || rc.RunID != "" || rc.Agent == nil || rc.Workspace == nil || rc.SessionID == "" || rc.Principal.ID == "" || rc.Principal.Kind != model.PrincipalLocalUser || rc.ChangeSetID != args.ChangeID || rc.PreviewDigest != args.PreviewDigest || (rc.OperationRisk != model.RiskWrite && rc.OperationRisk != model.RiskDestructive) {
		return model.ToolResult{}, operationError(model.CodeInvalid, "invalid_context")
	}
	if err = checkOperationDetails(rc); err != nil {
		return model.ToolResult{}, err
	}
	start := time.Now()
	result, err := e.execute(ctx, rc, call)
	// 不记录工具返回正文或任意错误文本，日志只描述真实主体和错误码。
	if err != nil {
		e.logger.Warn("用户操作中止", "operation_id", operationID, "change_id", rc.ChangeSetID, "code", model.CodeOf(err))
		return model.ToolResult{}, err
	}
	if result.IsError {
		return model.ToolResult{}, operationError(model.CodeInternal, "failed")
	}
	result.CallID = call.ID
	e.logger.Info("用户操作完成", "operation_id", operationID, "change_id", rc.ChangeSetID, "duration_ms", time.Since(start).Milliseconds())
	return result, nil
}

func checkOperationDetails(rc RunContext) error {
	if len(rc.OperationDetails) > 8192 || len(rc.OperationSummary) > 1024 {
		return operationError(model.CodeInvalid, "invalid_context")
	}
	var details map[string]any
	if json.Unmarshal(rc.OperationDetails, &details) != nil || details["change_id"] != rc.ChangeSetID || details["preview_digest"] != rc.PreviewDigest {
		return operationError(model.CodeInvalid, "invalid_context")
	}
	path, ok := details["path"].(string)
	if !ok || strings.TrimSpace(path) == "" {
		return operationError(model.CodeInvalid, "invalid_context")
	}
	allowed := map[string]bool{"path": true, "change_id": true, "preview_digest": true, "operation_kind": true, "base_sha256": true, "proposed_sha256": true, "expected_sha256": true, "revision_id": true, "expires_at": true, "byte_size": true, "before_size": true, "after_size": true, "target_exists": true, "restore_of": true, "version": true}
	for key, value := range details {
		if !allowed[key] {
			return operationError(model.CodeInvalid, "invalid_context")
		}
		switch value.(type) {
		case string, float64, bool, nil:
		default:
			return operationError(model.CodeInvalid, "invalid_context")
		}
	}
	return nil
}
