# Resource scheduling, tool guidance, and navigation

Historical local delivery record, 2026-10-12. At that stage the changes were uncommitted, unpushed, and did not replace the user's normal data directory. Conflict/recovery/legacy migration evidence follows [P3B delivery](2026-10-12-p3b-delivery.md); the full Go checks passed again.

## Completed work

- Navigation focuses on New task, Find a task, Projects, Jobs, and Recent tasks. Brand/New task still open Start; the duplicate Start entry was removed.
- Agent/model/skill/extension/messaging/activity management is hosted inside Settings using existing components, forms, permissions, and approvals. Modules/queries load on demand rather than remaining resident.
- Settings sections live in URLs with search, arrow navigation, refresh, and history. Five legacy routes redirect to sections. Returning preserves task/result parameters; existing preferences, history, drafts, and results remain readable.
- Default global parallelism 2, configurable 1–8; at most 32 queued, same resolved workspace serialized, immediate user priority, aged queue promotion. Approval occupies a slot; reducing the limit does not terminate accepted work.
- Waiting does not load full history, create worker tasks, or retain credential copies. Dispatch rechecks availability. Tests cover cancellation, start failure, shutdown, workspace competition, dynamic limits, and restart configuration.
- Live state is session-scoped and batches text/reasoning. Critical states flush immediately; Seq replay deduplicates and daemon restart resets sequence. Completed-session caches are released only after persistence is confirmed and no active subscribers remain; unsaved content is retained.
- Reuse message/tool indexes, mount folded details only on expansion, and query full history only when its drawer opens.
- Tool guidance names actual tools/permissions, preserves saved personality, and is appended for Chinese/English runtime use. It injects no foreign identity, fixed model, external path, or nonexistent tool. The runtime guidance has a tested 2,200-byte limit.
- Product line: Work locally. Travel light. Architecture, authentication, and result storage remain intact.

## Interfaces and compatibility

Optional `runtime.resources: 1`; older daemons hide the entry.

```text
GET /api/v1/resources
PUT /api/v1/resources  {"max_parallel": 2}
```

Return `max_parallel / max_queued / running / queued / awaiting_approval`. Authenticate through the existing API; persist limits in settings without a migration. Public domain methods: `Resources() ResourceState` and `SetParallelLimit(int) error`. Zero configuration retains earlier domain behavior; desktop composition applies the default cap.

Legacy `/agents /providers /extensions /channels /audit` redirect to `/settings?section=agents|models|extensions|channels|audit`, preserving other query/return parameters and using replace to avoid back-button loops. Users do not recreate assistants, models, or authorization.

## Commands and results

Use the toolchain PATH described in AGENTS and installed uv:

```bash
make lint
make test
make e2e
make build
npm run build
go test -race ./internal/session
npm run typecheck
npm test --workspace @gogosling/web
npm test --workspace @gogosling/web -- src/pages/SettingsPage.test.tsx src/pages/SettingsRedirect.test.tsx src/layout/NavigationSidebar.test.tsx
```

- Go vet, all packages, architecture checks, and queue race checks passed.
- Python Ruff/format/Mypy and 176 tests passed.
- Desktop 231 tests; Web 333 after navigation consolidation; later return-context/query adjustments passed 30 targeted cases, including 2 additions.
- End-to-end used a real Python worker, local fake model, and tool approval. Go/Web/desktop builds passed; management stayed split into modules.
- Synthetic live-state replay: 8000 interleaved events, 4000 characters per session, 80 notifications per session; background-only increments did not notify the current session. Other tests cover deduplication, immediate approval updates, caps, failed unsaved text, restart sequence, and timer cleanup.
- Browser checks persisted limits 2 → 1 → 2; Settings model list/create/cancel worked. Navigation fit 900×760 without horizontal overflow; the preview daemon used the rebuilt code.
- Existing js-yaml verified 52 paths, 55 schemas, and 232 references. Missing PyYAML/older system YAML parser failures were not counted as successful contract validation.

## Implementation boundaries

No new runtime dependencies. Notification reduction does not establish CPU/energy/RSS percentages; no long-duration same-device energy comparison was performed. Future delegation must share budget and avoid parents holding slots while awaiting children; no delegation tools were added. Disk statistics, content deduplication, and hanging-stream connection checks were planned next.

UI evidence used isolated data and a fake model, not comprehensive compatibility with the user's production API.

## Chat focus and typography follow-up

The blue outline came from both textarea focus and container focus-within styles. CSS removed the duplicate textarea outline, retaining a thin gray container and neutral keyboard focus for reading/buttons. System UI fonts use local Chinese fallbacks without remote downloads. Chat/input use 15px, text line height 27px, heading weight 600, clearer paragraph/list spacing, and monospaced code.

Typecheck, Web build, and scoped whitespace checks passed. Browser checks found no textarea outline/shadow, a neutral border, and 2px gray keyboard focus on Attach. No API/data migration was added; unrelated unit tests were not rerun for this low-impact visual change.
