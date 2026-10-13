# Prompt Optimization: Live Gateway Acceptance and Interaction Simplification

## Completed

By default, clicking optimizes and fills back, providing "Restore". The single-input modal is opened only when further modifications are needed, updating the input directly after filling in feedback. Retain original text, existing attachments, draft saving, and sending methods; optimization will not send chat messages. Requests are bound to draft versions, tasks, and assistants; late results cannot be filled back after manual modification, switching, sending, or cancellation.

The interface remains `POST /api/v1/agents/{id}/prompt-enhancement`, with optional `draft` and `feedback` added; old `{text}` calls remain compatible. The Go interface is:

```go
EnhancePrompt(ctx context.Context, agentID, input string) (string, error)
EnhancePromptWithFeedback(ctx context.Context, agentID, input, draft, feedback string) (string, error)
```

Internal instruction prompts remain in the Go kernel. Inputs use structured boundaries and add mixed Chinese-English examples, strictly prohibiting direct answering, fabricating emails/code, or discarding original constraints. Regression protection checks known answer prefixes, obvious Chinese/English misalignments, and numerical constraints; this is not a complete semantic proof, and users can still restore and adjust.

## Live Call Results

Using user-authorized `https://ai.gogosling.ca/v1` and `auto:fast`, calls were executed via actual local HTTP → Go app/session → Python worker → gateway. This is not just testing curl, nor using fake models to replace quality results. Credentials enter the temporary process's in-memory secret store only via stdin, and were not written to configurations, databases, logs, events, or firmware; they are cleaned up after the temporary instance terminates.

The initial test revealed direct answering/fabricated emails, a single timeout, and leaked connections after TLS handshake cancellation. The [baseline ](2026-10-12-live-prompt-baseline.json) was retained, and the initial HTTP 200 was not treated as passing acceptance. The first cancellation sample in the baseline did not observe an upstream connection and cannot be counted as a valid cancellation test.

The corrected [8 samples ](2026-10-12-live-prompt-results.json):

- Chinese code explanation: 2.73s, retained Chinese, 200 characters, did not modify code.
- Chinese weekly report: 15.27s, retained two parts, no fabrication, pending completion.
- English code explanation: 11.90s, retained English, 120 words, no modification and edge cases.
- English email: 1.12s, retained English, polite and direct, names, dates, numbers, and commitments unchanged, returning optimization requirements.
- Mixed Chinese-English code: 15.84s, retained natural mix, root cause, minimal fix, did not change interface/did not add dependencies.
- Mixed Chinese-English checklist: 12.05s, retained terminology, max 6 items, no fabricated assignees/dates.
- Chinese supplementary feedback: 16.63s, changed 200 characters to 100 characters per feedback, described key features only without changing code.
- Mixed supplementary feedback: 3.38s, retained mixed expression, no added dependencies, added minimal regression tests.

This is a single round of sample results and does not guarantee semantic equivalence for all models and future requests. Elapsed time is the end-to-end time from request to complete candidate; it is not referred to as time-to-first-token or p95.

## Cancellation and Resource Release

Traced to installed httpcore 1.0.9: AnyIO `start_tls` captures Exception only, whereas Python task cancellation belongs to BaseException. The underlying stream during handshake has not been transferred to the connection pool yet, so simply closing the client may leave leaked sockets. Established streams for this instance were tracked using the HTTPX public trace extension and boundedly closed upon exception/cancellation; no third-party packages were modified nor global monkey patches used.

Referenced [httpcore trace extension ](https://github.com/encode/httpcore/blob/master/docs/extensions.md); relevant cancellation discussions include [#1053 ](https://github.com/encode/httpcore/discussions/1053) and closed [#961 ](https://github.com/encode/httpcore/issues/961). These discussions relate to post-cancellation connection state, but are not identical to this TLS gap.

[Post-fix live cancellation records ](2026-10-12-live-prompt-cancellation.json):

- In both cases, an actual upstream TCP connection was observed before cancellation; requests did not terminate prematurely.
- Connection reclamation took ~26ms and 48ms; execution slot reclamation took ~79ms and 100ms.
- Final result: 0 leaked connections, 0 business sessions; subsequent request after cancellation completed normally (1.41s).

The above proves local connection and execution slot release, but does not prove that the remote vendor's computation or billing has stopped.

## Verification Commands

```bash
go test ./internal/session ./internal/server
go test -race ./internal/session ./internal/workbench/preview ./internal/server
cd worker && uv run pytest tests/test_stream_lifetime.py tests/test_provider_resilience.py -q
cd worker && uv run ruff check . && uv run ruff format --check . && uv run mypy && uv run pytest
npm test --workspace @gogosling/web -- src/pages/chat/PromptEnhancement.test.tsx
```

All passed. Cancellation regression used a local real TCP server to cover waiting for response headers and TLS handshake respectively, without accessing the actual network. 178 full Python tests passed. 7 frontend tests covered click-invocation, optional feedback, original restore, cancellation, switching, reverting after manual editing, late response after sending, failure preservation, and legacy fallback.

The live gateway was executed separately using a temporary acceptance runner, saving sanitized inputs, outputs, elapsed times, and resource observations; secret scanning passed. This runner and temporary configurations will not be submitted as production code.

This record supersedes the old interaction description of "preview before replacing" in the previous [prompt optimization record ](2026-10-12-prompt-enhancement.md); test figures from that time in the old record do not serve as current version figures.
