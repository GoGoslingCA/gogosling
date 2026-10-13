# Local Development and Execution

Requires Go 1.26, Node 22 (at least 22.12), Python 3.12, uv, and make. Run these Bash commands from the repository root on macOS or Linux. On Windows, use WSL2 for web development. Build the executables into `bin/`; no prebuilt release binaries are currently provided. For a shorter walkthrough, see [Quickstart](quickstart.md).

## Installation and Build

```bash
npm ci
uv sync --project worker --frozen --python 3.12
make build
npm run build
```

## Start Web Development Environment

Terminal one:

```bash
make dev
```

The default data directory is `$HOME/.gogosling-dev`, separate from the normal `$HOME/.gogosling` directory. The daemon binds to `127.0.0.1:46750` and starts the Python worker from source. Check the terminal for the worker-ready message; `uv` must be on PATH.

Terminal two:

```bash
npm run dev -w @gogosling/web -- --strictPort
```

Open `http://127.0.0.1:5173` in a local browser. Leave the connection address blank to use the Vite proxy. Copy the `token` value from `$HOME/.gogosling-dev/daemon.json` into the local connection page. Do not share that file or token. The UI keeps the token in memory, not persistent browser storage.

Starting the service does not require a model API key. Before generating a response, save and test a provider under **Settings → Model connections**, configure an agent under **Settings → Agents**, and start a task.

## Start Electron Desktop Shell

Keep the daemon in terminal one running, then use another terminal after the build finishes:

```bash
GOGOSLING_HOME="$HOME/.gogosling-dev" npm run start -w @gogosling/desktop
```

The desktop shell reuses the daemon in the same data directory, injects authentication via a built-in proxy, and uses the built Web interface. Re-run `npm run build -w @gogosling/web` after Web code changes. Automated checks cover desktop management logic and web interactions. Native window behavior and installer packages still need validation on each target operating system.

## Inspect and Stop

```bash
bin/gogosling --home "$HOME/.gogosling-dev" daemon status
bin/gogosling --home "$HOME/.gogosling-dev" daemon stop
```

Stop Vite with Ctrl-C in its terminal. Exiting the desktop shell does not stop a daemon it attached to; use the command above or stop the `make dev` process. If customizing `DEV_HOME`, the desktop shell's `GOGOSLING_HOME` and the CLI's `--home` must also use the same directory.

Verification commands:

```bash
make lint
make test
make build
npm run build
make e2e
```

See the [personal workbench guide](../user/personal-workbench.md) for current workflows and limits, and [validation reports](../README.md#validation-records) for recorded test results. On Linux, `bwrap` requires user namespaces to be available. Installing the helper alone does not guarantee isolation; startup logs identify the mechanism actually selected.
