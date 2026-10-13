# Prompt Optimization and On-Demand Loading

> History: This document describes the first version of the interaction. It has now been changed to one-click backfill, restore, and on-demand supplementary feedback; for the real API and cancellation acceptance, see the [latest record ](2026-10-12-live-prompt-validation.md)].

## Implemented

Added "Optimize Prompt" to the start page and chat input area. It uses the current assistant's model to optimize only the input text; it does not read history or attachments, execute tools, or send chat tasks. The result is previewed first, can be further edited, and then "Replace Input" can be selected; it supports keeping the original text, retrying, canceling, and undoing. The focus returns to the input box after replacement, undoing, or closing.

Internal system prompts preserve intent, constraints, and language, without requiring the model to answer the original task; the source code is embedded only by the Go kernel and does not enter frontend resources, chat history, drafts, or events. The original text and the applied candidate are still saved according to the existing draft mechanism. The candidate is rejected when an empty result, obviously incomplete text, system prompt, or language analysis tag is detected.

The optimization module is dynamically loaded only after being clicked. The final build separately generates JS/CSS for `PromptEnhancementDialog`; approximately 2KB JS and 0.9KB CSS (uncompressed, subject to this build), without adding runtime dependencies. If module loading fails, the original text is retained. Actual startup and energy consumption benefits have not been hardware-benchmarked, so the code splitting cannot be directly converted into a speed improvement percentage.

## Interfaces and Execution Boundaries

```text
POST /api/v1/agents/{id}/prompt-enhancement
Request:  {"text":"User Input"}
Response: {"text":"Optimized Candidate"}

session.Service.EnhancePrompt(context.Context, string, string) (string, error)
```

Added optional capability `prompt.enhance: 1`. The entry point is hidden when the old kernel does not have this capability; text optimization can also be used independently without configuring workbench storage. It reuses existing authentication, provider credentials, and worker RPC, without adding database or RPC schema migrations.

A single input is limited to a maximum of 16,000 UTF-8 bytes, and the candidate to a maximum of 32,000 bytes, with a 45-second total timeout, a maximum of one model loop, and 2,048 output tokens. Globally, there is at most one optimization request, which counts towards the existing concurrency budget; it explicitly returns busy when fully loaded, without starting an additional resident process. Execution uses a temporary inference identity, does not create a business Session/Run, and has no tool authorization or assistant persona injection. Cancellation follows the request lifecycle, releasing resources upon receiving the final run state; if cancellation fails, the request still in transit cannot be treated as released.

## Actual Verification

```bash
make lint
make test
make e2e
make build
npm run build
go test -race ./internal/session
npm test --workspace @gogosling/web -- src/pages/chat/PromptEnhancement.test.tsx
```

All of the above have been run. The initial new test mock types were incompatible with the current Vitest, which has been corrected and re-passed the checks. Full Go, 176 Python, 231 desktop, and 340 Web tests passed; subsequent focus and style adjustments corresponding to 5 optimization-specific items passed again. The original tool approval passed end-to-end.

- Kernel-specific: Chinese, English, and mixed-language candidates; no history, tools, or session events; blank/overlength/disabled assistants; concurrency budget and queue release; cancellation, deadline, closing, and startup failure; no leakage of internal instructions/error bodies; oversized output, out-of-bounds tools; final state with text-only increments and source indentation preservation.
- HTTP-specific: Authentication, input boundaries, rejecting client-side overrides of system prompts, capability declarations, returning only candidates, and disabling caching.
- Frontend-specific: Requesting only on click, StrictMode preventing duplicate calls; preview not modifying original text, not submitting, and not losing attachments; candidate editing/applying/undoing; aborting requests on close/session switch; protection for late results and when the original text changes and then changes back; error retry, empty input, and fallback for old capabilities.
- Real Python worker + local mock model calls passed for Chinese, English, and mixed languages respectively; this is transmission and workflow acceptance, not a guarantee of generation quality from real providers. User-provided keys were not written to tests or the repository.
- Browser acceptance: Preview, replace, undo, and original text retention after closing in the chat area; switching to the start page confirms the same entry point. Acceptance chat always has 0 messages, and running/queued tasks are both 0 after optimization ends; no tasks were sent by mistake.
- OpenAPI 53 paths and 233 references are resolvable. Production frontend resource search confirmed that it does not contain internal system prompt markers. The latest screenshot is `24-prompt-enhancement-final.jpg`.

Optimization uses the inference quota of the current model; current session usage statistics still only count formal chat runs. Candidate quality and language stability depend on the selected model, and no cross-provider real network quality evaluation has been performed. Existing users' model connections, shortcuts, sending behaviors, and draft interfaces remain compatible.
