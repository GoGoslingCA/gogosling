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
src/preload/     Preload script (sandboxed, single file): four functions under window.gogosling
src/shared/      IPC contracts shared between main process and preload (preload imports types only)
test/            vitest (without launching Electron)
```

## Running

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

## Verification

```bash
npm run typecheck -w @gogosling/desktop
npm run test -w @gogosling/desktop
npm run build -w @gogosling/desktop
npm run package -w @gogosling/desktop     # electron-builder, unsigned; requires web to be built first
```
