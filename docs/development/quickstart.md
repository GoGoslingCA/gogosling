# Run gogosling from source

Start a local development workspace without a gogosling account. A model endpoint is only needed when generating responses.

## Install dependencies

Use Git, Go 1.26, Node.js 22.12+ in the 22 LTS line, Python 3.12, uv, and make. Put `go`, `node`, `npm`, and `uv` on PATH. Commands below use Bash on macOS/Linux; use WSL2 for Web development on Windows.

```bash
git clone https://github.com/GoGoslingCA/gogosling.git
cd gogosling
npm ci
uv sync --project worker --frozen --python 3.12
```

## Start Web development

Terminal one:

```bash
make dev
```

The daemon listens on `127.0.0.1:46750` and uses `~/.gogosling-dev`, separate from normal `~/.gogosling` data. Keep it running.

Terminal two, in the repository root:

```bash
npm run dev -w @gogosling/web -- --strictPort
```

Open **http://127.0.0.1:5173**. Leave the connection address empty and enter the `token` from local `~/.gogosling-dev/daemon.json`. Do not share that file. Browser credentials are in memory only, so a fresh visit may require reconnecting.

In **Settings → Models**, add a compatible endpoint/model, then configure an agent and start a task. Select only the files needed for context. Remote requests follow your provider's pricing and data handling.

## Open the desktop shell

Keep the daemon running, then use another terminal:

```bash
make build
npm run build
GOGOSLING_HOME="$HOME/.gogosling-dev" npm run start -w @gogosling/desktop
```

The shell attaches to the same daemon and displays the built UI. Rebuild Web after changes with `npm run build -w @gogosling/web`. Native desktop behavior needs real-OS validation; WSL2 setup is not a packaged Windows release.

## Inspect and stop

After `make build`:

```bash
bin/gogosling --home "$HOME/.gogosling-dev" daemon status
bin/gogosling --home "$HOME/.gogosling-dev" daemon stop
```

Stop Vite with Ctrl-C. If you customize `DEV_HOME`, use that same path for `GOGOSLING_HOME` and CLI `--home`.

## Troubleshooting

- **Missing command:** check versions and PATH. The machine-specific PATH in `AGENTS.md` is not an installation command for your computer.
- **Port occupied:** stop the conflicting process, or configure daemon address, Vite proxy, and allowed origins consistently. Keep authentication and Origin checks enabled.
- **Worker unavailable:** rerun `uv sync --project worker --frozen --python 3.12` and inspect startup output. Redact secrets/private data before sharing logs.
- **Connected but no response:** check provider URL/model and agent configuration. UI connection does not prove model connectivity.
- **Legacy Office/layout unavailable:** optional conversion currently requires macOS, LibreOffice, and a functioning sandbox; built-in content readers work separately.
- **Apply unavailable on Windows:** guarded write-back currently supports Linux/macOS; use preview/export.

See [CONTRIBUTING.md](../../CONTRIBUTING.md) for validation commands.
