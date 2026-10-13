# gogosling-worker

The Python inference worker runs as a child of the Go `gogoslingd` daemon, not as an independent public service. It exchanges line-delimited JSON-RPC 2.0 over stdin/stdout; see [the worker protocol](../api/worker-rpc/protocol.md), version `1`.

Python retains the model-client, document, and retrieval ecosystem. Process separation lets the daemon supervise, restart, and replace the engine. The worker **does not access the data directory, listen on ports, or persist credentials**. File, command, and network tools are requested through `host.tool.call`; the daemon applies permissions and the relevant workspace and sandbox constraints before execution. The worker makes provider inference requests directly.

## Directory

| Path | Responsibility |
|---|---|
| `src/gogosling_worker/rpc.py` | Full-duplex JSON-RPC: framing (16 MiB per line), dispatch, reverse calls, error codes |
| `src/gogosling_worker/transport.py` | Bridge blocking stdio I/O into the event loop using a daemon thread and direct `os.read` |
| `src/gogosling_worker/protocol.py` | Go-compatible data shapes, `run.start` validation, event construction |
| `src/gogosling_worker/methods.py` | `worker.hello/ping/info/shutdown`, `run.start/cancel`, run registry, terminal-state guarantees |
| `src/gogosling_worker/runtime/` | `Engine`, default loop, host adapter |
| `src/gogosling_worker/providers/` | OpenAI-compatible (including Ollama `/v1`), Anthropic, SSE, message conversion |
| `src/gogosling_worker/i18n.py` | Model/user messages in zh/en/fr, selected by `worker.hello.locale` |
| `src/gogosling_worker/tools/`, `memory/`, `knowledge/` | Planned; see package comments |

## Run lifecycle

```
daemon -- run.start {run_id, agent, provider, messages, tools, options} --> worker
       <-- {"accepted": true}                                        background task
       <-- run.event run.started
       <-- run.event text.delta / reasoning.delta                   provider stream
       <-- run.event tool.call.started {tool_call}
       <-- host.tool.call {run_id, call}                             reverse call, no timeout
       -- {"result": ToolResult} / error -32003|-32004 -->
       <-- run.event tool.call.completed {tool_result}               feed tool result into next step
       <-- run.event usage                                          incremental model-call usage
       <-- run.event run.completed {message, usage} | run.failed | run.cancelled
```

- Final `message` interleaves text, tool calls, and results from all steps. Each `usage` event is one call's increment; terminal `usage` is cumulative. Populate `cache_write_tokens` when supplied.
- Retain usage already reported before cancellation/interruption as an increment, then emit only one failed/cancelled terminal event. Never estimate unreported usage. Anthropic `message_start`/`message_delta` usage is merged immediately so waiting for `message_stop` cannot lose reported input/cache/output.
- Final messages must pass the worker's own history parser. A nameless call produces no `tool_call` part; retain a `call_id` error and ask the model to retry or answer directly. Log/ignore incomplete historical call/result parts.
- `reasoning.delta` (OpenAI-compatible reasoning fields or Anthropic `thinking_delta`) is display-only, absent from final messages and model history.
- Remaining calls at `options.max_steps` (default 32) fail with an explanation.
- Pure-text `length`/`max_tokens` completion may continue twice within the run, consuming steps and accumulating usage. A further truncation fails. Never execute truncated tool calls; interrupted text without a completion marker fails without automatic replay.
- Cancellation immediately closes the provider stream and unregisters pending reverse calls, then emits `run.cancelled`; host error `-32003` has the same effect.
- Every accepted run has exactly one terminal event. Engine exceptions fail; a missing terminal event is synthesized.
- `worker.shutdown` cancels in-flight work, waits up to 3 seconds, drains events, and exits 0. EOF first lets received requests finish up to 1.5 seconds and flushes before cancellation. Both finish within 5 seconds. Flush stdout/stderr and use `os._exit`: a reader may remain blocked on stdin, and normal interpreter cleanup could abort with code 134 while competing for stream locks.
- Cancellation before the first step still emits `run.started` plus `run.cancelled`, including immediate `run.start` → cancel/shutdown sequences.

## Providers

| Kind | Endpoint | Behavior |
|---|---|---|
| `openai_compatible` | `{base_url}/chat/completions`, `stream=true` | Nonempty URL required; no silent default. Request usage, retry a rejected usage parameter or required `max_completion_tokens` once according to the response. Accept valid nonstreaming JSON if streaming is ignored |
| `anthropic` | `{base_url}/v1/messages`, `stream: true` | Official endpoint if unset; default `max_tokens` 4096; assemble `input_json_delta` tool arguments |
| `ollama` | `{base_url}/v1/chat/completions` | OpenAI-compatible route; default `http://127.0.0.1:11434` |

Errors map to `run.failed.error` or start error `-32005`. Replace credentials with `***` and retain only the first 200 normalized response characters. `input_tokens` includes cache reads/writes; cache counters are subsets. Add Anthropic cache portions back to its otherwise exclusive input count.

Explicit HTTP 429 and temporary 5xx retry at most twice (not 501/505), normally after 0.5 seconds and 1 second. Honor `Retry-After` seconds/date; if over 30 seconds, return the original error rather than retrying early. Waiting is cancellable. Errors after content/reasoning/tool output never retry.

OpenAI requires `[DONE]` or valid `finish_reason`; Anthropic requires `message_stop` or valid `stop_reason`. EOF without completion is interrupted. Nonstreaming JSON requires its message structure. Tool-result `truncated` passes unchanged through progress, final output, and history.

The daemon forwards allowlisted proxy and certificate variables: `HTTP(S)_PROXY`, `NO_PROXY`, `ALL_PROXY` and lowercase variants; `SSL_CERT_FILE`, `SSL_CERT_DIR`, and `REQUESTS_CA_BUNDLE`. HTTPX uses them for proxying/certificate verification.

Message conversion orders interleaved calls/results, drops orphan results, fills missing results with errors, substitutes empty-user placeholders, and represents attachments with structured text.

## Development

```bash
uv sync                      # Install dependencies; httpx is the runtime dependency
uv run ruff check .           # Static checks
uv run ruff format --check .  # Formatting
uv run mypy                  # Strict typing
uv run pytest                # Offline memory transports and MockTransport tests
```

Manual protocol interaction:

```bash
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"worker.hello","params":{"protocol_version":"1","locale":"en"}}' \
  '{"jsonrpc":"2.0","id":2,"method":"worker.ping"}' \
  '{"jsonrpc":"2.0","method":"worker.shutdown"}' | uv run gogosling-worker
```

`GOGOSLING_LOCALE` supports zh/en/fr and is overridden by `worker.hello.locale`. `GOGOSLING_WORKER_LOG_LEVEL` defaults to INFO; logs go only to stderr.

## Extension points

- Implement `runtime.engine.Engine` and replace `DefaultEngine` in `__main__.run_worker`. Engines depend only on `Host` and `Provider`, with fake test implementations.
- Implement `providers.base.Provider`: `stream_chat` emits `TextDelta | ReasoningDelta | ToolCallDelta | UsageChunk | Done`. Register in `build_provider`, `protocol.PROVIDER_KINDS`, and Go `model.ProviderKind`.
- Register methods in `methods.WorkerService.register`; handlers may be synchronous/async and raise `rpc.JsonRpcError` for coded errors.
