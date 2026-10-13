# Files and webpages in the workspace

gogosling keeps opened files and webpages in a small sidebar list. Selecting an item shows it beside the current task; narrow windows use a separate reading view with a return button. Opening a file does not attach it to a model request.

## Ownership and modules

- `apps/web/src/state/WorkspaceTabsProvider.tsx` owns transient UI state. File tabs are scoped to the current daemon connection; webpage tabs remain available while the app is open, even after a daemon reconnect. A tab stores its kind, title, and opening location in memory. Files retain their own Agent ID and workspace-relative path, independent of the current conversation.
- `apps/web/src/workspace/` renders the sidebar, resizable reading pane, and webpage controls. Task routes and the current artifact editor stay mounted while a reading pane is displayed.
- `apps/web/src/content/` remains the format registry and viewer layer. Only the selected file viewer is mounted; changing files cancels its read and releases format-specific resources. The daemon still enforces file access and parsing limits.
- `apps/desktop/src/shared/workspace-browser.ts` defines the optional native browser bridge. The main process hosts the active webpage. Remote pages receive no app preload, daemon token, or model tools.

There is no new daemon endpoint, database table, worker protocol, or content cache. Opened file paths, page URLs, page contents, and authentication state are not written to UI preferences. A connection change clears file tabs and hides the reading pane; it preserves webpage descriptors so a page with an unsaved form remains reachable. After the app renderer reloads, the optional `current` method restores an entry for the native page without taking focus. Existing draft and artifact persistence remains in the daemon.

## Resource policy

The UI holds at most 12 tab descriptors. Reopening the same Agent/path selects its existing tab. Reaching the limit asks the user to close a tab; it never silently discards an edited artifact.

There is one mounted file preview and at most one native webpage view. Opening a different webpage releases the previous view after its leave-page check. Returning to an unloaded webpage reloads it. A hidden current webpage may retain its state; it is not a promise of zero background resource use. Cookies and other session state are held in an isolated, temporary browser session and disappear when the app exits.

These are explicit resource bounds, not measured claims of a percentage improvement in memory, energy use, or speed. Browser processes and PDF workers have their own overhead, and a single webpage can still be resource-intensive.

## Browser boundary

The optional bridge exposes `show`, `hide`, `close`, `navigate`, `action`, `current`, and `onState`. Every invocation is accepted only from the trusted app's top-level frame. The view bounds are checked against the app window; overlays hide the native view so it cannot cover approval or settings controls.

The native host accepts public HTTP(S) pages on standard ports. Private networks, loopback addresses, credentials in URLs, file URLs, and app URLs are refused. Network connections use checked public IP addresses to prevent a later DNS resolution from reaching a private host. Permission requests, downloads, and new windows are not granted by this reader. Sites requiring those features should be opened in the user's regular browser.

Source browser clients and older desktop shells retain file previews and show an explicit external-browser action for webpage tabs. They do not embed remote sites in the app's HTML preview or weaken its content policy.

## Verification

Tests cover tab deduplication and limits, Agent isolation, active-tab closing, stale navigation responses, preview cleanup, and the optional-bridge fallback. Desktop checks cover protocol and address validation, bounds, trusted IPC, one-view lifecycle, cancellation, and the isolated network path. UI checks use synthetic Markdown and DOCX files, task drafts, narrow-window navigation, and public read-only pages.

Browser leave-page protection relies on the page's normal `beforeunload` signal. It is not an autosave system for arbitrary third-party forms.
