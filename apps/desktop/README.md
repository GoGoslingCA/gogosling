# @gogosling/desktop

The Electron shell handles daemon discovery and launch, windows, the tray, autostart, and deep links. It stores UI preferences but no business state. It renders the same build as `apps/web` and communicates with `gogoslingd` through the local HTTP API.

In production, the page requests same-origin `app://gogosling/api/...` URLs. The main process proxies these requests to the daemon and adds the access token; the token never enters the page. In development (`GOGOSLING_WEB_URL`), the page connects directly to the daemon with connection details held in memory.

Closing the window hides it to the tray and leaves tasks running. Quitting the app stops only a daemon launched by that shell; an independently started daemon remains running.

## Structure

```
src/main/        Main process (Node, CommonJS)
  main.ts          Entry point: single-instance lock, window, app:// protocol, CSP, tray, autostart, deep linking, IPC
  api-proxy.ts     app://gogosling/api/* → Same-origin proxy to the daemon process (token injection, streaming responses)
  autostart.ts     Platform differences for autostart (consistent read/write of Windows args, macOS login launch hidden)
  deep-link.ts     Staging for deep links arriving before ready
  daemon.ts        DaemonManager: Discover/launch/supervise gogoslingd (all I/O injectable)
  daemon-logic.ts  Pure logic: data directory, executable location, daemon.json parsing, backoff and restart decisions
  daemon-stop.ts   Stop workflow: identity verification → POST /api/v1/system/shutdown → fallback signal (side-effect injection)
  app-protocol.ts  app://gogosling → secure path mapping for apps/web/dist
  csp.ts           Content-Security-Policy
  shell-security.ts IPC sender verification, external link whitelist, deep link parsing, dev server URL parsing
  tray.ts          Tray menu model
  i18n.ts          zh/en/fr copy for the shell itself
  png.ts           Generates the tray icon at runtime
  log.ts           Sanitized logs (<dataDir>/logs/desktop.log, 0600)
src/preload/     Sandboxed preload: connection functions and optional diagnostics under window.gogosling
src/shared/      IPC contracts shared between main process and preload (preload imports types only)
test/            vitest (without launching Electron)
```

## Run from source

Install the toolchains and configure the development daemon and worker using the [quickstart](../../docs/development/quickstart.md). The shell commands below do not themselves install a daemon or Python runtime.

```bash
npm install
npm run build -w @gogosling/web          # Generate apps/web/dist (app:// protocol fetches files from here)
npm run build -w @gogosling/desktop
npm run start -w @gogosling/desktop
```

Environment variables:

| Variable | Description |
|---|---|
| `GOGOSLING_HOME` | Data directory (defaults to `~/.gogosling`), shared by shell and daemon |
| `GOGOSLING_DAEMON_PATH` | Daemon executable; if unset, searches for `<resources>/bin/gogoslingd[.exe]` and `PATH` in order |
| `GOGOSLING_RESOURCES` | Automatically set to `<resources>/share/gogosling` when shell launches bundled daemon (built-in extensions are under `extensions/bundled`); does not override an existing value |
| `GOGOSLING_WEB_URL` | Points to Vite dev server in development (loopback addresses only), page connects directly to daemon, CSP relaxed accordingly |

Daemon discovery first reads `<dataDir>/daemon.json`; if `GET /healthz` returns 200 and `GET /api/v1/version` accepts its token,
the shell reuses it without taking ownership. If no live daemon is found, the shell launches one. A live daemon that rejects the handle token is reported as a stale-handle error rather than starting a second instance. Child process crashes trigger exponential backoff restarts (starting at 500ms, capped at 15s, up to 5 attempts; count resets after 60s of stable running).

Daemon shutdown (app exit and the tray's "Restart Daemon" command share `daemon-stop.ts`; restart is available only for a daemon owned by the shell):

1. **Instances launched by shell**: First verify daemon.json identity—the pid in the handle matches the child process pid, and `GET /api/v1/version` using its token
   returns 200 with `started_at` matching the handle; after confirmation, `POST /api/v1/system/shutdown`, if 202 is returned, wait for self-exit (up to 30s,
   the daemon will delete daemon.json itself), or force kill on timeout (SIGKILL / `taskkill /T /F`).
2. Fall back to signals only when identity is unverified, shutdown endpoint is unreachable, or response is not 202: POSIX `SIGTERM`, if not exited within 30s then `SIGKILL`; on Windows,
   windowless console processes cannot receive gentle signals, so direct `taskkill /PID <pid> /T /F` is used without waiting. Signals are sent only to child processes spawned by the shell itself.
3. After child process exit is confirmed, a daemon.json still bearing its pid is residual (killed before deletion) and deleted by the shell, leaving no token on disk.
4. **Instances not launched by shell** (reused, or only daemon.json remains): Verify identity only. If verified, disconnect and allow it to keep running; if token rejected,
   `started_at` mismatches, or endpoint unreachable, delete daemon.json as a stale file **without requesting shutdown or sending any signal**; keep as-is if timeout prevents determination.

## Interfaces Available to the Renderer Process

```ts
// In production build: { endpoint: "app://gogosling", token: "", mode: "proxy" }; Dev mode: { endpoint, token, mode: "direct" }
window.gogosling.getConnection(): Promise<{ endpoint: string; token: string; mode: "proxy" | "direct" } | null>
window.gogosling.onDaemonStatus(cb: (status: DaemonStatus) => void): () => void
window.gogosling.openExternal(url: string): Promise<void>   // http / https / mailto only
window.gogosling.getVersion(): Promise<ShellVersion>
```

See `src/shared/ipc.ts` for types.

## Build a self-contained desktop package

gogosling is developed by Gogosling.ca, a Canadian company. Both the source workflow and native packaging remain available to contributors.

On the target operating system and architecture, install Go, Node 22, and uv (plus Xcode Command Line Tools on macOS), then run from the repository root:

```bash
npm ci
npm run package -w @gogosling/desktop
```

This command builds the web UI and shell, stages the native Go daemon and CLI, and downloads a pinned CPython runtime through uv. Production Python dependencies come from `worker/uv.lock` and are verified against its hashes. Build tools and network access are required on the build machine; the installed app does not require globally installed Go, Node, Python, or uv.

The staging process uses a fresh temporary Python installation, not the developer's virtual environment. It copies only the worker package, runtime, production dependencies, and required licenses. It does not package user data, logs, credentials, or model settings. Go binaries use `-trimpath`; Python bytecode and unused console wrappers containing build paths are excluded. Upstream runtime and dependency licenses remain in the bundle.

Before replacing the previous staged runtime, the script copies the bundle to a new path with spaces and verifies it with an empty `PATH`: Python and TLS imports, a real worker calling a local mock model, automatic worker discovery by the bundled daemon, and orderly shutdown. No real provider credential is used. A failed stage leaves the previous staged runtime intact.

Generated files are ignored by Git:

- `apps/desktop/resources/runtime/`: native binaries, Python runtime, licenses, and `runtime-manifest.json`.
- `apps/desktop/release/`: the desktop app and installer produced by electron-builder.

The manifest records platform, architecture, version, commit, dirty-tree state, build time, the worker lockfile hash, and smoke-check results. Set `SOURCE_DATE_EPOCH` to control the build timestamp. It contains no developer paths or credentials. Packaging rejects a runtime that does not match the Electron target or has not passed the smoke checks.

The staging implementation targets native macOS x64/arm64, glibc Linux x64/arm64, and Windows x64 hosts; it does not cross-compile Python runtimes. Installer targets are DMG, AppImage, and NSIS respectively. A target listed in the configuration is not evidence of a completed installer or clean-machine validation on that platform. The default configuration is unsigned and does not publish artifacts. Signing, notarization, and platform acceptance remain release tasks.

You can stage or recheck the runtime separately:

```bash
npm run stage:runtime -w @gogosling/desktop
npm run verify:runtime -w @gogosling/desktop
```

A new installation uses local settings; existing data is preserved. Configure a model connection if none exists. Optional LibreOffice conversion is not bundled. Packaging adds no automatic diagnostic uploads.

## Optional release diagnostics

Source development, browser clients, and default packages keep normal failure messages but do not expose diagnostic uploads. A distributor can explicitly enable the feature for a native package:

```bash
npm run package -w @gogosling/desktop -- --diagnostics
```

The main process requires both a packaged app and the release marker, and rejects development-server overrides. The marker is a feature switch, not proof of client identity. It contains no credential. Source builds do not register the preview or upload IPC handlers.

In enabled releases, a failed task shows **Report problem** above the composer. There is no general upload entry in Settings and no category to choose: the report is bound to that failed run. The main process verifies the run through the authenticated local daemon before preparing a preview. The user reviews the exact report and explicitly agrees before sending. If the daemon cannot start, its actual native failure dialog still offers **Review diagnostics**; an error-free snapshot cannot produce a report.

Task reports contain app/platform metadata, local run/session IDs, the recorded failure time, and the fixed classification `run_failed`. They exclude the free-form failure reason, raw logs, upstream response bodies, user IDs, paths, chats, files, and credentials. Run IDs are not labeled as upstream Request IDs or Trace IDs. Those upstream fields are not currently available through the run API. Existing native application reports retain the limited schema-1 lifecycle codes.

Sending first obtains a short-lived ticket bound to that report, then uploads it to the fixed Gogosling.ca endpoint. Tickets remain in main-process memory. There is one active request, a 15-second deadline, no background queue or automatic retry, and a 16 KiB report limit. Explicit retries preserve the report UUID; successful receipt releases the preview. Cancel stops the local request but cannot retract a report already received. The server retention period is 90 days.

The deployed OpenAPI 1.1.0 supports schema-1 reports. Its earlier packaged-client consent, upload, cancellation, and bundled-module retry checks are recorded in the [validation record](../../docs/reports/2026-10-13-desktop-diagnostics-validation.md). Task-specific reports require the [schema-2 server extension](../../docs/development/diagnostics-server-handoff.md#task-failure-reports-schema-2), which is integration-pending. A schema-2 ticket rejection is displayed as unsupported reporting; the client does not silently upload a generic substitute. There is no long-lived backend credential fallback.

The optional IPC methods are `diagnosticsStatus`, `diagnosticsTaskPreview({kind, requestId, sessionId, runId})`, `diagnosticsCancelPreparation(requestId)`, `diagnosticsSend(previewId)`, and `diagnosticsCancel(previewId, discard)`. The legacy `diagnosticsPreview(category)` remains error-gated and cannot create a generic task report. Types are defined in [the shared diagnostic contract](src/shared/diagnostics.ts). These methods accept no arbitrary URL, file path, log body, or credential. Existing daemon HTTP and worker RPC contracts are unchanged.

## Verification

```bash
npm run typecheck -w @gogosling/desktop
npm test -w @gogosling/desktop           # Shell tests and offline staging-boundary checks
npm run build -w @gogosling/desktop
npm run package -w @gogosling/desktop   # Native staging, relocation smoke checks, unsigned package
```
