# gogosling-worker

gogosling 的 Python 智能体工作进程。它不独立对外提供服务，而是由 Go 守护进程
`gogoslingd` 作为子进程启动，并通过标准输入输出交换行分隔的 JSON-RPC 2.0 消息
（契约见 [`api/worker-rpc/protocol.md`](../api/worker-rpc/protocol.md)，协议版本 `1`）。

把智能体运行时留在 Python，是为了继续使用成熟的模型客户端与文档解析、检索类库；
把它放进独立进程，是为了让内核可以监管、重启和替换它。工作进程**不读写数据目录、
不监听端口、不持久化密钥**；一切有副作用的操作（文件、命令、网络）都通过反向请求
`host.tool.call` 交给守护进程，在权限判定与沙箱之后执行。

## 目录

| 路径 | 职责 |
|------|------|
| `src/gogosling_worker/rpc.py` | 全双工 JSON-RPC：分帧（单行 16 MiB 上限）、分发、反向请求、错误码 |
| `src/gogosling_worker/transport.py` | stdio 传输：守护线程把阻塞的管道 I/O 接到事件循环（读线程直接 `os.read` 文件描述符） |
| `src/gogosling_worker/protocol.py` | 协议数据形状（与 Go `internal/model` 对应）、`run.start` 参数校验、事件构造 |
| `src/gogosling_worker/methods.py` | `worker.hello / ping / info / shutdown`、`run.start / cancel`，运行表与兜底 |
| `src/gogosling_worker/runtime/` | `Engine` 抽象、默认推理循环 `loop.py`、宿主适配 `context.py` |
| `src/gogosling_worker/providers/` | `openai_compatible`（含 Ollama `/v1`）、`anthropic`、SSE 解析、消息格式转换 |
| `src/gogosling_worker/i18n.py` | 面向模型与用户的少量文案（zh / en / fr），按 `worker.hello` 的 `locale` 选择 |
| `src/gogosling_worker/tools/`、`memory/`、`knowledge/` | 待实现（见各包注释） |

## 一次运行的生命周期

```
daemon ── run.start {run_id, agent, provider, messages, tools, options} ──► worker
       ◄── {"accepted": true}                                              │ 后台 task
       ◄── run.event run.started                                           │
       ◄── run.event text.delta … / reasoning.delta …                      │ 供应商流式输出
       ◄── run.event tool.call.started {tool_call}                         │
       ◄── host.tool.call {run_id, call}（反向请求，不设超时）              │
       ── {"result": ToolResult} / error -32003|-32004 ──────────────────► │
       ◄── run.event tool.call.completed {tool_result}                     │ 回填 tool 消息，进入下一步
       ◄── run.event usage（每次模型调用一条，本次调用的增量）              │
       ◄── run.event run.completed {message, usage} | run.failed | run.cancelled
```

- `run.completed` 的 `message` 是完整的助手消息：本次运行全部步骤的文本、`tool_call`、
  `tool_result` 片段按发生顺序交错；`usage` 是各步累计（`usage` 事件是每步的增量，守护进程累加，
  两者一致）。`cache_write_tokens` 在供应商报告时填入。
- 调用在流中断或取消前已报告的用量也以一条 `usage` 增量保留，随后仍只发送一个失败或取消终态；
  没有报告用量的失败调用不推测 token 数。
  Anthropic 的 `message_start` / `message_delta` 用量收到后立即交给推理循环合并，避免在等待
  `message_stop` 时遇到服务端错误、断连或取消而丢失已经报告的输入、缓存和输出用量。
- `run.completed.message` 必须能被本进程的 `run.start` 解析器接受（协议 §3）：模型给出缺少名称的
  工具调用时不产生 `tool_call` 片段，只留一条带 `call_id` 的错误结果，并以一条说明让模型重试或直接作答；
  解析历史时，不完整的 `tool_call` / `tool_result` 片段记日志后忽略。
- `reasoning.delta` 携带模型思考内容（OpenAI 兼容服务的 `reasoning_content` / `reasoning`、
  Anthropic 的 `thinking_delta`），只用于展示，不进入最终消息，也不回填给模型。
- 达到 `options.max_steps`（默认 32）仍有工具调用时以 `run.failed` 收尾并说明。
- 模型以 `length` / `max_tokens` 结束纯文本输出时，在同一运行内自动续写，最多续写两次，
  每次调用仍计入 `max_steps` 并累计用量；再次截断时报告失败。长度截断的工具调用不执行，
  避免把不完整的操作交给宿主。纯文本流意外中断没有完成标记时报告失败，不自动重放。
- `run.cancel` 让运行 task 被取消：正在进行的供应商流立即关闭、等待中的 `host.tool.call`
  撤销登记，随后发出 `run.cancelled`。守护进程以 `-32003` 结束反向请求时效果相同。
- 服务层保证"每次被接受的运行恰好一个终态事件"：引擎异常 → `run.failed`，引擎没发终态 → 补发。
- `worker.shutdown`（通知）：取消全部在途运行、等它们收尾（至多 3 秒）、把队列里的事件写完后退出（码 0）。
  stdin EOF 时先让已收到的请求完成（至多 1.5 秒）、flush 输出，再取消在途运行并退出。两条路径都在
  5 秒内结束（协议 §6）。退出时 flush stdout / stderr 后以 `os._exit` 结束，跳过解释器收尾——读线程可能
  仍阻塞在 stdin 上（守护进程发出 shutdown 后不关闭管道），正常收尾会因争用标准流的锁以退出码 134 中止。
- 运行 task 在迈出第一步前就被取消（`run.start` 之后紧接着 `run.cancel` / `worker.shutdown`）时，
  服务层仍会补发 `run.started` + `run.cancelled`，守护进程总能收到终态。

## 供应商

| `provider.kind` | 端点 | 说明 |
|---|---|---|
| `openai_compatible` | `{base_url}/chat/completions`，`stream=true` | `base_url` 必须非空（不会静默回退到 OpenAI）；默认发送 `stream_options.include_usage`，服务端以 4xx 拒绝该参数或要求 `max_completion_tokens` 时按响应提示回退重试一次；服务端忽略 `stream` 直接返回 JSON 也能处理 |
| `anthropic` | `{base_url}/v1/messages`，`stream: true` | `base_url` 为空时用官方地址；`max_tokens` 缺省 4096；工具参数按 `input_json_delta` 拼接 |
| `ollama` | `{base_url}/v1/chat/completions` | 走 OpenAI 兼容接口；`base_url` 为空时用 `http://127.0.0.1:11434` |

错误一律映射为 `run.failed.error`（或 `run.start` 阶段的 `-32005`），文本经过脱敏：密钥替换为 `***`，
响应体只保留压缩后的前 200 字符。用量口径统一为 `input_tokens` 含缓存读写、`cache_read_tokens` /
`cache_write_tokens` 为其中命中缓存与写入缓存的子集（Anthropic 的 `input_tokens` 原本不含缓存部分，
这里会加回去）。

两种接口对明确的 HTTP 429 / 临时 5xx 最多重试两次（501 / 505 不重试）。默认等待 0.5 秒、1 秒；
优先遵守 `Retry-After` 秒数或 HTTP 日期，服务要求等待超过 30 秒时保留原错误，避免提前重试。
等待可以立即取消；流式内容、思考或工具片段发出后发生的错误不重试，避免重复输出或操作。
OpenAI 接口必须收到 `[DONE]` 或有效 `finish_reason`；Anthropic 必须收到 `message_stop` 或
有效 `stop_reason`，缺少结束信号的 EOF 视为中断。非流式 JSON 必须包含相应消息结构。
工具结果的 `truncated` 标记从宿主回复到进度事件、最终消息及后续历史均保持一致。

网络出口：守护进程按白名单把 `HTTP(S)_PROXY`、`NO_PROXY`、`ALL_PROXY`（含小写形式）与
`SSL_CERT_FILE`、`SSL_CERT_DIR`、`REQUESTS_CA_BUNDLE` 传给工作进程，httpx 按这些变量走代理与校验证书。

消息转换（`providers/messages.py`）会修复历史里的工具配对：助手消息中交错的调用与结果拆成合法顺序、
孤儿结果丢弃、缺失结果补一条错误结果、空用户消息用占位文案代替、附件变成一行结构化提示。

## 开发

```bash
uv sync                      # 安装依赖（运行时只有 httpx）
uv run ruff check .          # 静态检查
uv run ruff format --check . # 格式
uv run mypy                  # 类型检查（strict）
uv run pytest                # 测试（全部离线：内存传输、httpx.MockTransport）
```

手动与工作进程对话：

```bash
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"worker.hello","params":{"protocol_version":"1","locale":"zh"}}' \
  '{"jsonrpc":"2.0","id":2,"method":"worker.ping"}' \
  '{"jsonrpc":"2.0","method":"worker.shutdown"}' | uv run gogosling-worker
```

环境变量：`GOGOSLING_LOCALE`（`zh` / `en`，`worker.hello` 的 `locale` 会覆盖）、
`GOGOSLING_WORKER_LOG_LEVEL`（默认 `INFO`，日志只写 stderr）。

## 扩展点

- 换一种推理循环：实现 `runtime.engine.Engine`，在 `__main__.run_worker` 里替换 `DefaultEngine`。
  引擎只依赖 `Host`（发事件、申请工具）与 `Provider`（流式分片），测试可用假对象驱动。
- 新增供应商：实现 `providers.base.Provider`（`stream_chat` 产出 `TextDelta | ReasoningDelta |
  ToolCallDelta | UsageChunk | Done`），在 `providers.build_provider` 里按 `kind` 接入；协议侧需要同步在
  `protocol.PROVIDER_KINDS` 与 Go 的 `model.ProviderKind` 增加取值。
- 新增方法：在 `methods.WorkerService.register` 注册；处理器可以是同步或 `async` 函数，
  抛 `rpc.JsonRpcError` 即可返回带码错误。
