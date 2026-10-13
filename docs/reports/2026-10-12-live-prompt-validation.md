# Prompt enhancement: live validation and interaction updates

## Completed

Clicking **Enhance prompt** replaces the input with the enhanced text and offers **Restore**. **Adjust** opens a single-field feedback dialog for further revisions. Attachments, draft saving, and sending work as before; enhancement does not send a chat message. Requests are bound to the draft version, task, and agent, so late responses cannot overwrite the input after an edit, navigation, send, or cancellation.

The interface remains `POST /api/v1/agents/{id}/prompt-enhancement`, with optional `draft` and `feedback` added; old `{text}` calls remain compatible. The Go interface is:

```go
EnhancePrompt(ctx context.Context, agentID, input string) (string, error)
EnhancePromptWithFeedback(ctx context.Context, agentID, input, draft, feedback string) (string, error)
```

Internal instructions remain in the Go daemon. They separate user input from instructions and include mixed Chinese-English examples to discourage answering the task, inventing content, or dropping constraints. Regression checks cover known answer prefixes, obvious language mismatches, and numerical constraints. These checks do not prove semantic equivalence; users can still undo or adjust the result.

## Live Call Results

The live checks used the authorized `https://ai.gogosling.ca/v1` endpoint and `auto:fast` through the full local HTTP → Go session service → Python worker → gateway path. Credentials were passed through stdin to the temporary process's in-memory secret store. They were not written to configuration, databases, logs, events, or test fixtures; the temporary instance was stopped after validation.

The initial test revealed direct answering/fabricated emails, a single timeout, and leaked connections after TLS handshake cancellation. The [baseline](2026-10-12-live-prompt-baseline.json) was retained, and the initial HTTP 200 was not treated as passing acceptance. The first cancellation sample in the baseline did not observe an upstream connection and cannot be counted as a valid cancellation test.

The [eight language and intent samples](2026-10-12-live-prompt-results.json) passed manual review. That file also retains the failed cancellation samples from before the TLS fix; the separate post-fix results are linked below.

- Chinese code explanation: 2.73s; preserved Chinese, the 200-character limit, and the instruction not to modify the code.
- Chinese weekly report: 15.27s; preserved the two sections, the ban on fabricated data, and the requirement to mark missing information.
- English code explanation: 11.90s; preserved English, the 120-word limit, relevant edge cases, and the instruction not to modify the code.
- English email: 1.12s; preserved the requested tone and the requirement to keep names, dates, numbers, and commitments unchanged. Returned a revised prompt rather than an invented email.
- Mixed-language code: 15.84s; preserved the language mix and the request for a root-cause explanation and minimal fix, with no API changes or new dependencies.
- Mixed-language checklist: 12.05s; preserved terminology, the six-item limit, and the ban on fabricated assignees or dates.
- Chinese feedback: 16.63s; changed the requested limit from 200 to 100 characters and narrowed the explanation to the code's main purpose, as requested.
- Mixed-language feedback: 3.38s; preserved the language mix and dependency restriction, and added the requested minimal regression test to the prompt.

This is a single round of sample results and does not guarantee semantic equivalence for all models and future requests. Elapsed time is the end-to-end time from request to complete candidate; it is not referred to as time-to-first-token or p95.

## Cancellation and Resource Release

The leak was traced to httpcore 1.0.9: its AnyIO `start_tls` handler catches `Exception`, while task cancellation raises a `BaseException`. During the handshake, the connection pool does not yet own the stream, so closing the client alone can leave a socket open. The fix tracks each request's established streams through the public trace extension and closes them within a bounded time on error or cancellation. It does not modify third-party packages or apply global monkey patches.

Referenced [httpcore trace extension](https://github.com/encode/httpcore/blob/master/docs/extensions.md); relevant cancellation discussions include [#1053](https://github.com/encode/httpcore/discussions/1053) and closed [#961](https://github.com/encode/httpcore/issues/961). These discussions relate to post-cancellation connection state, but are not identical to this TLS gap.

[Post-fix live cancellation records](2026-10-12-live-prompt-cancellation.json):

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

All passed. Cancellation regression tests used a loopback TCP server to exercise waits for response headers and TLS handshakes without contacting external services. The full Python suite passed 178 tests. Seven frontend tests covered explicit invocation, optional feedback, undo, cancellation, navigation, edits that return to the original text, late responses after sending, failure preservation, and legacy fallback.

The live gateway checks ran separately in a temporary validation harness. Sanitized inputs, outputs, durations, and resource observations were retained, and the secret scan passed. The harness and its temporary configuration are not part of the product.

This record supersedes the old interaction description of "preview before replacing" in the previous [initial enhancement record](2026-10-12-prompt-enhancement.md); test figures from that time in the old record do not serve as current version figures.
