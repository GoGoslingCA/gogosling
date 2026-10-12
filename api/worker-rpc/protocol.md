# 守护进程 ↔ 工作进程协议（worker-rpc）

**协议版本：`1`**。守护进程 `gogoslingd` 以子进程方式启动 Python 工作进程，双方通过工作进程的 **stdin / stdout** 交换 **行分隔的 JSON-RPC 2.0** 消息（每条消息一行，UTF-8，`\n` 结尾，消息内不得出现裸换行）。stderr 只用于日志。

两个方向都可以发请求：守护进程 → 工作进程是"正向请求"，工作进程 → 守护进程是"反向请求"（前缀 `host.`）。双方都必须能在等待自己发出的请求时继续处理对方的请求（全双工）。

## 1. 生命周期

```
daemon                                  worker
  │── spawn ─────────────────────────────►│
  │── worker.hello {protocol_version} ───►│
  │◄── result {protocol_version, …} ──────│   版本不兼容：daemon 发 worker.shutdown 并报错
  │── worker.ping / run.start / … ───────►│
  │◄── run.event 通知 / host.* 反向请求 ──│
  │── worker.shutdown（通知）────────────►│   worker 结束在途运行后退出，超时则 daemon 杀进程
```

- 工作进程崩溃或 stdout 关闭：守护进程把该进程上的所有在途运行标记为 `failed`，按退避重启进程。
- 守护进程退出：工作进程读到 EOF 即退出。

## 2. 正向请求（daemon → worker）

### `worker.hello`
```json
{"protocol_version": "1", "daemon_version": "0.1.0", "locale": "zh"}
```
结果：
```json
{"protocol_version": "1", "worker_version": "0.1.0", "python": "3.12.14", "capabilities": ["run", "cancel", "providers.openai_compatible"]}
```
工作进程若不支持该主版本，返回错误码 `-32001`（见 §5）。

### `worker.ping` → `"pong"`

### `worker.info` → `{"version", "python", "pid"}`

### `worker.shutdown`（通知，无 id）
工作进程完成清理后退出，退出码 0。

### `run.start`
```json
{
  "run_id": "01…", "session_id": "01…",
  "agent": {"id": "01…", "name": "助理", "system_prompt": "…", "autonomy": "ask_risky"},
  "provider": {"kind": "openai_compatible", "base_url": "https://…/v1", "api_key": "…", "model": "gpt-…"},
  "messages": [ Message… ],
  "tools": [ ToolSpec… ],
  "options": {"max_steps": 32, "temperature": 0.7, "max_output_tokens": 4096}
}
```
- `messages` 与 `tools` 的 JSON 形状与 Go 的 `model.Message` / `model.ToolSpec` 相同（`snake_case`）。
- `messages` 只包含会话历史，**不含 `system` 角色消息**；系统提示只通过 `agent.system_prompt` 传递，由工作进程构造自己的系统消息（若收到 `system` 消息，工作进程应忽略它而不是重复注入）。
- `provider.api_key` 只在本次请求中出现，工作进程不得记录。
- `provider.kind` 为 `openai_compatible` 时 `base_url` 必须非空；工作进程不得静默回退到某个默认服务商。
- 结果立即返回 `{"accepted": true}`；进展通过 `run.event` 通知回传。同一 `run_id` 重复提交返回错误 `-32002`。

### `run.cancel`
`{"run_id": "01…"}` → `{"cancelled": true}`；工作进程随后发出 `run.cancelled` 事件。对未知 run_id 返回 `{"cancelled": false}`。

## 3. 通知（worker → daemon）

### `run.event`
```json
{"run_id": "01…", "event": RunEvent}
```
`RunEvent` 的形状与 Go 的 `model.RunEvent` 相同。工作进程需要发出的类型与顺序：

1. `run.started`
2. 任意次：`text.delta`（增量文本）、`reasoning.delta`（可选，模型思考内容的增量，不进入最终消息）、`tool.call.started`（模型决定调用工具，随后发反向请求 `host.tool.call`）、`tool.call.completed`（带 `tool_result`）、`usage`
3. 恰好一个终态：`run.completed`（带 `message`：最终助手消息，含全部 `parts`；带累计 `usage`）、`run.failed`（带 `error`）、`run.cancelled`

用量口径：每个 `usage` 事件是**一次模型调用的增量**（守护进程累加），`run.completed.usage` 是整次运行的**累计值**；`cache_write_tokens` 若供应商报告则必须填入。`tool.call.completed.tool_result.truncated` 原样透传守护进程返回的值。

工作进程发出的 `run.completed.message` 必须能被自己的 `run.start` 解析器接受（例如不得产生 `name` 为空的 `tool_call` 片段），否则会话历史会把后续运行全部拒绝。

`approval.*` 与 `message.created` 由守护进程产生，工作进程不发送。

### `log`
`{"level": "info|warn|error", "message": "…", "fields": {…}}` —— 可选，守护进程写入日志。

## 4. 反向请求（worker → daemon）

### `host.tool.call`
```json
{"run_id": "01…", "call": {"id": "call_1", "name": "read_file", "arguments": {"path": "notes.md"}}}
```
结果：`{"result": {"call_id": "call_1", "content": "…", "is_error": false, "truncated": false}}`。

守护进程在此完成权限判定（可能长时间等待用户审批）、沙箱执行与审计。工作进程必须允许该请求等待很久（不设短超时），并且在等待期间仍能响应 `run.cancel`；被取消时守护进程会以错误 `-32003` 结束该反向请求。

### `host.secret.get`（保留，当前版本不提供）

## 5. 错误码

| 码 | 含义 |
|---|---|
| -32700 / -32600 / -32601 / -32602 / -32603 | JSON-RPC 标准：解析、请求、方法、参数、内部错误 |
| -32001 | 协议版本不兼容 |
| -32002 | run_id 重复 |
| -32003 | 请求因运行被取消而中止 |
| -32004 | 权限被拒绝（`host.tool.call`），`data.reason` 为面向模型的说明 |
| -32005 | 供应商调用失败，`data.provider_error` 为脱敏后的原因 |

## 6. 工作进程的定位

守护进程按以下顺序找到工作进程命令：

1. 环境变量 `GOGOSLING_WORKER_CMD`（整条命令，按 shell 词法拆分；开发时为 `uv run --project worker gogosling-worker`）。
2. 随安装包分发的运行环境：`<可执行文件所在目录>/../worker/bin/python -m gogosling_worker`（macOS 为 `Contents/Resources/worker/bin/python`）。
3. 找不到则守护进程照常启动但 `engine` 报告 `unavailable`，运行请求返回 `unavailable` 错误。

工作进程启动时的环境变量：`GOGOSLING_PROTOCOL_VERSION=1`、`GOGOSLING_LOCALE`、`PYTHONUNBUFFERED=1`、`PYTHONIOENCODING=utf-8`。不传递守护进程的其他环境变量，白名单如下：
- 基础：`PATH`、`HOME`、`TMPDIR`、`TEMP`、`TMP`、`LANG`、`LC_ALL`；Windows 定位变量 `SystemRoot`、`SystemDrive`、`ComSpec`、`PATHEXT`、`USERPROFILE`、`APPDATA`、`LOCALAPPDATA`（权威定义：`internal/config.ChildEnvAllowlist`）
- 网络出口（非凭据，但代理 URL 可能含 userinfo，日志须脱敏）：`HTTP_PROXY`、`HTTPS_PROXY`、`NO_PROXY`、`ALL_PROXY` 及其小写形式，`SSL_CERT_FILE`、`SSL_CERT_DIR`、`REQUESTS_CA_BUNDLE`

退出：工作进程收到 `worker.shutdown` 或 stdin EOF 后必须在 5 秒内以退出码 0 结束，且不得因解释器关闭时的守护线程（例如阻塞在 stdin 上的读线程）崩溃；守护进程把非零退出码记为异常。
