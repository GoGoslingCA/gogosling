package engine

import (
	"encoding/json"
	"fmt"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// ProtocolVersion 是本包实现的 worker-rpc 协议主版本（api/worker-rpc/protocol.md）。
const ProtocolVersion = "1"

// jsonrpcVersion 是 JSON-RPC 信封里固定的版本字段。
const jsonrpcVersion = "2.0"

// 协议定义的方法名。
const (
	methodHello    = "worker.hello"
	methodPing     = "worker.ping"
	methodInfo     = "worker.info"
	methodShutdown = "worker.shutdown"

	methodRunStart  = "run.start"
	methodRunCancel = "run.cancel"

	notifyRunEvent = "run.event"
	notifyLog      = "log"

	methodHostToolCall  = "host.tool.call"
	methodHostSecretGet = "host.secret.get"
)

// 协议定义的错误码（protocol.md §5）。
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603

	codeIncompatibleVersion = -32001
	codeDuplicateRun        = -32002
	codeRunCancelled        = -32003
	codePermissionDenied    = -32004
	codeProviderFailed      = -32005
)

// RPCError 是对端返回的 JSON-RPC 错误对象；作为 error 向上传递时由 toModelError 映射为 model.Code。
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("jsonrpc %d: %s", e.Code, e.Message)
}

// wireMessage 是 JSON-RPC 信封；请求、通知、响应共用一个结构，按字段有无区分。
type wireMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// helloParams 是 worker.hello 的参数。
type helloParams struct {
	ProtocolVersion string `json:"protocol_version"`
	DaemonVersion   string `json:"daemon_version"`
	Locale          string `json:"locale"`
}

// helloResult 是 worker.hello 的结果。
type helloResult struct {
	ProtocolVersion string   `json:"protocol_version"`
	WorkerVersion   string   `json:"worker_version"`
	Python          string   `json:"python"`
	Capabilities    []string `json:"capabilities"`
}

// runStartParams 是 run.start 的参数；形状与协议 §2 一致。
type runStartParams struct {
	RunID     string                    `json:"run_id"`
	SessionID string                    `json:"session_id"`
	Agent     AgentSpec                 `json:"agent"`
	Provider  model.ProviderCredentials `json:"provider"`
	Messages  []model.Message           `json:"messages"`
	Tools     []model.ToolSpec          `json:"tools"`
	Options   RunOptions                `json:"options"`
}

type runStartResult struct {
	Accepted bool `json:"accepted"`
}

type runCancelParams struct {
	RunID string `json:"run_id"`
}

type runCancelResult struct {
	Cancelled bool `json:"cancelled"`
}

// runEventParams 是 run.event 通知的参数。
type runEventParams struct {
	RunID string         `json:"run_id"`
	Event model.RunEvent `json:"event"`
}

// logParams 是 log 通知的参数。
type logParams struct {
	Level   string         `json:"level"`
	Message string         `json:"message"`
	Fields  map[string]any `json:"fields,omitempty"`
}

// toolCallParams 是反向请求 host.tool.call 的参数。
type toolCallParams struct {
	RunID string         `json:"run_id"`
	Call  model.ToolCall `json:"call"`
}

type toolCallResult struct {
	Result model.ToolResult `json:"result"`
}

// toolCallErrorData 是 -32004 的 data 字段。
type toolCallErrorData struct {
	Reason string `json:"reason"`
}
