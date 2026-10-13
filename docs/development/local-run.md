# Local Development and Execution

Requires Go 1.26, Node 22 (at least 22.12), Python 3.12, uv, and make. The following commands are executed using Bash at the repository root and apply to macOS / Linux; Windows can run the Web development environment in WSL2. The repository does not provide prebuilt binaries; please use the freshly built `bin/` file on your local machine. English: [Quickstart](quickstart.md).

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

The default data directory is `$HOME/.gogosling-dev`, kept separate from the daily-use `.gogosling`. The daemon binds to `127.0.0.1:46750`, and the real Python worker starts from source. The terminal should show the worker ready status; uv must be in PATH.

Terminal two:

```bash
npm run dev -w @gogosling/web -- --strictPort
```

Open `http://127.0.0.1:5173` in a local browser. Leaving the address blank on the development connection page will use the Vite proxy; the token is taken from the `token` field of the local `$HOME/.gogosling-dev/daemon.json` and filled only into the local connection page. The UI will not write the token to browser persistent storage.

Starting the service does not require a model API key. Before actual conversations, configure available models under "Model Providers", then create or configure an agent, and finally start a new session.

## Start Electron Desktop Shell

Keep the daemon in terminal one running, then use another terminal after the build finishes:

```bash
GOGOSLING_HOME="$HOME/.gogosling-dev" npm run start -w @gogosling/desktop
```

The desktop shell reuses the daemon in the same data directory, injects authentication via a built-in proxy, and uses the built Web interface. Re-run `npm run build -w @gogosling/web` after Web code changes. The Electron graphical interface and installer packages still need to be verified on your local machine; what has been verified in the cloud is the desktop management logic and Chromium Web interaction.

## Inspect and Stop

```bash
bin/gogosling --home "$HOME/.gogosling-dev" daemon status
bin/gogosling --home "$HOME/.gogosling-dev" daemon stop
```

Stop Vite using Ctrl-C in its running terminal. If customizing `DEV_HOME`, the desktop shell's `GOGOSLING_HOME` and the CLI's `--home` must also use the same directory.

Verification commands:

```bash
make lint
make test
make build
npm run build
make e2e
```

For completed features, actual test results, and unfinished scope, see [Personal Workbench Documentation](../user/personal-workbench.md). Linux bwrap requires the host to allow user namespaces; the presence of helper programs does not equal isolation availability, and startup logs will show the actual selected mechanism.
