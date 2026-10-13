# Workspace tabs: validation record

Date: 2026-10-13. Platform: macOS arm64. Native runtime: Electron 44.7.0. Tests used isolated local data and synthetic documents. No user document, model credential, or real model request was used.

## Implemented behavior

The sidebar holds up to 12 opened file and webpage entries. One read-only file viewer is mounted at a time. Desktop owns at most one webpage view in an isolated memory session; hiding it keeps the page reachable, while switching webpages closes the old view after the leave-page check. Task drafts remain in the existing persistence flow.

The native bridge adds `workspaceBrowser.current/show/hide/close/navigate/action/onState`. `FileBrowser` adds optional `onOpenFile`, `initialLocation`, and `onLocationChange` props. Existing daemon APIs, model types, worker protocols, permission checks, and schema migrations are unchanged. See [the module guide](../development/workspace-tabs.md).

## Automated checks

The following checks passed:

```bash
test -z "$(gofmt -l cmd internal)" && go vet ./... && go test ./...
(cd worker && uv run ruff check . && uv run ruff format --check . && uv run mypy && uv run pytest)
npm run typecheck
npm test -w @gogosling/web
npm test -w @gogosling/desktop
npm run build
make e2e
npm run test:browser-runtime -w @gogosling/desktop
git diff --check
```

Results: 228 worker tests, 543 Web tests, 384 Desktop tests, six packaging/runtime checks, and nine additional native browser/lifecycle checks passed. Go tests, formatting, static checks, and real-worker end-to-end checks passed.

The final visual adjustment hid secondary header labels only while the reading pane is open; its accessible names remain. Type checking and builds were rerun after that adjustment.

Tests cover tab limits and identity, canceling inactive file reads, late responses, separate Agent file scopes, reconnect cleanup, pending webpage restoration, closing by keyboard, menu occlusion, address editing, rapid tab changes, unsaved-page cancellation, invalid URL recovery, renderer recovery, network address checks, bounded DNS work, trusted IPC, and native application exit.

## Interface and public-page checks

The actual Web interface loaded synthetic Markdown and DOCX files, switched between them and a webpage entry, and retained both the unsent chat draft and an edited result draft. Closing the active file selected a neighboring item. The browser-only client displayed an explicit external-browser action. Both separate reading and side-by-side layouts were observed.

The native host loaded `https://gogosling.ca/` and `https://example.com/` through its normal restricted network path. Each returned a page title and HTTPS location; switching destroyed the earlier view and retained the one-view limit. The public-page check was read-only. The native lifecycle fixture separately verified actual Electron behavior with synthetic pages, including canceling a close, retrying it, recovering a crashed renderer, and completing the real quit sequence.

These native host checks are separate from a complete end-to-end UI walkthrough in a packaged installer. Automated accessibility state and pixels from the source Electron test window were not consistently synchronized, so that window was not used as visual proof of a public-page interaction.

## Limits

No percentage saving in RAM, CPU, energy, or latency was measured. The one-view policy bounds the number of app-managed pages, not Chromium's total process count or the resource use of one website. Hidden pages may still perform throttled background work. Third-party forms depend on their own leave-page handler; this feature is not a form autosave system.

Some websites need workers, popups, downloads, private network access, or other features intentionally unavailable in the reader. The external-browser action is the supported fallback. Source/browser clients and older shells keep that fallback without enabling remote HTML embeds.

Schema-2 diagnostic upload was verified separately using the earlier unmodified packaged client; see [the diagnostics record](2026-10-13-desktop-diagnostics-validation.md).
