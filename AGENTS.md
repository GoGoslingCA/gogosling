# AGENTS.md — Contributor and agent handbook

This handbook is for contributors and coding agents working in this repository. See [docs/architecture.md](docs/architecture.md) for the architecture itself; this file explains how to contribute.

## 1. Working Principles

- Read the dependency rules and security invariants in `docs/architecture.md` before getting started; implementations must not conflict with them.
- Only modify files directly related to the task; do not perform opportunistic refactoring, unify styles, or delete others' TODO comments.
- For uncertain interface shapes: check `internal/model` (types, error codes, repository interfaces) and the contracts under `api/` first, then check the usage in sibling packages; if still unsure, state assumptions clearly in the delivery notes and do not silently invent a second set of types.
- Shared files (`internal/model/*`, `go.mod`, `Makefile`, `AGENTS.md`, `docs/architecture.md`, `api/*`) belong to the architecture lead; request changes in the delivery notes rather than modifying them directly. Exception: dependencies within the `go get` approval list.
- All user-facing text must go through `internal/i18n` (daemon) or `apps/web/src/locales` (UI); do not hardcode Chinese or English prompts in the code.
- Secrets, tokens, and API keys must not enter logs, databases, events, or test fixtures.

## 2. Toolchain

Install Go, Node, and uv first, and ensure the tools are in the PATH. When using user-directory installations for Go and uv, you can add:

```bash
export PATH="$HOME/.local/go/bin:$HOME/.local/bin:$PATH"
```

| Stack | Version | Verification Command (at repository root) |
|---|---|---|
| Go | 1.26 (`go.mod` declares 1.26.0) | `test -z "$(gofmt -l cmd internal)" && go vet ./... && go test ./...` (any output from `gofmt -l` indicates unformatted files) |
| Python | 3.12, managed by uv | `cd worker && uv sync && uv run ruff check . && uv run ruff format --check . && uv run mypy && uv run pytest` |
| Node | 22 LTS, npm workspaces | `npm install && npm run typecheck && npm test && npm run build` |
| End-to-End | uv required | `make e2e` (real Python worker process + fake model service) |

`make lint` (gofmt/vet/ruff/mypy/tsc), `make test` (Go + Python + vitest), `make build` (Go binary), and `make e2e` aggregate the commands above; `make dev` starts the source development daemon (data directory `DEV_HOME`, worker process uses `uv run`, allowing Vite's Origin). Report the commands you actually ran and their results in the delivery notes; explain any checks you did not run.

## 3. Go Conventions

- Package layout and dependency direction: see architecture documentation §3, §4. New packages must have `doc.go` or a package comment at the top of the main file explaining responsibilities, items to be implemented, and boundaries.
- Approved third-party dependencies (already in `go.mod`): `modernc.org/sqlite`, `github.com/modelcontextprotocol/go-sdk`, `github.com/robfig/cron/v3`, `github.com/zalando/go-keyring`, `golang.org/x/sys`, `golang.org/x/sync`. Propose any new dependencies first; do not add them beforehand.
- Do not run `go mod tidy` (it will conflict with parallel work); the architecture lead handles dependency reconciliation during integration.
- Errors: domain errors use `model.NewError / Errorf / Wrap / NotFound` carrying `model.Code`; use `fmt.Errorf("...: %w", err)` to preserve the chain when returning upward. Do not use `panic` (except for random source failures in `model.NewID`).
- Context: the first parameter of all functions that may block or perform I/O must be `ctx context.Context`, and must respect cancellation.
- Concurrency: document in comments if exported types are concurrency-safe; use mutexes or channels for shared state, avoiding global variables; long-running goroutines must be stoppable by `ctx`.
- Logging: accept a caller-supplied `*slog.Logger`; use lowercase snake_case keys; do not print secrets or complete request bodies.
- Time: use UTC internally; convert to the user’s time zone for display.
- File permissions: new files within data directories `0o600`, directories `0o700`.
- Testing: `go test` standard library is sufficient, do not introduce assertion libraries; use `t.TempDir()`, `t.Setenv`, `httptest`; do not access the real network; must run across platforms (Linux, macOS, Windows), use `runtime.GOOS` to skip POSIX-specific behaviors; describe expectations in Chinese for test names and failure messages.
- Comments: identifiers in English, comments in Chinese; exported symbols must have comments.

## 4. Python Conventions (worker/)

- Strict typing (`mypy --strict`), `ruff` rules for `E F I B UP`; line width 100.
- Use stdout only for protocol messages, route logs to stderr (`logging` configured to stderr).
- Do not read or write data directories, do not listen on ports, do not persist secrets; credentials should exist only in memory and at the point of use.
- Add dependencies to `pyproject.toml` under `dependencies` with a stated purpose; prioritize standard library and `httpx`.
- Use `pytest` for tests, and local fake servers (`http.server` or `httpx.MockTransport`) for networking.

## 5. Frontend Conventions (apps/)

- TypeScript `strict`; React functional components + hooks; manage remote data with `@tanstack/react-query`, and local UI state with `useState`/`useReducer`.
- All network access must go through `apps/web/src/api/`; types must match `api/openapi/openapi.yaml` (handwritten or generated, but source must be noted).
- UI copy belongs in `apps/web/src/locales/{zh,en,fr}.json`; the existing application fallback remains Chinese.
- Electron: `contextIsolation: true`, `nodeIntegration: false`, `sandbox: true`; preload exposes minimal IPC only; tokens are passed into renderer memory exclusively via IPC and are not written to `localStorage`.

## 6. Commit and Delivery

- Commit messages: `<type>(<scope>): <English summary>`, where type ∈ feat / fix / refactor / docs / test / chore / build / ci; example: `feat(store): implement SQLite repository and migrations`.
- Delivery notes for each task must include: what was implemented, public interfaces (signatures), executed verification commands and output summaries, deviations from contracts or architecture documentation with reasons, and remaining TODOs.
- Do not commit build artifacts, `node_modules`, `.venv`, local configurations, or any secrets.
- Local release commits use the `Gogosling.ca` project signature, without automatically appending personal profiles or automatic attribution trailers.
