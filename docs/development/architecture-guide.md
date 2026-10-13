# Architecture guide for contributors

gogosling separates clients, a Go daemon, and a Python inference worker. Business state belongs to the daemon. Replacing a client does not move that state.

## Find your module

- **UI:** `apps/web/src`, React/TypeScript shared by browser and desktop. HTTP goes through `apps/web/src/api/`; React Query manages remote state.
- **Desktop:** `apps/desktop/src`, Electron windows, tray, startup, and daemon discovery. Preserve renderer isolation and narrow IPC.
- **API:** `internal/server`, with contracts in `api/openapi`. Local requests require authentication and Host/Origin checks.
- **Execution:** `internal/session`, provider configuration in `internal/provider`, schedules in `internal/scheduler`.
- **Results:** `internal/workbench`; parsers in `internal/workbench/preview`; lazy viewers in `apps/web/src/content`.
- **Side effects:** `internal/workspace`, `internal/permission`, `internal/tools`, and `internal/sandbox`.
- **Inference:** `worker/src/gogosling_worker`; versioned stdio JSON-RPC connects provider clients and the agent loop to the daemon.
- **State and wiring:** `internal/store` owns SQLite; `internal/app` assembles implementations; `internal/model` defines shared types/interfaces.

## Preserve the boundaries

Clients use the API, not SQLite or direct workspace access. The worker makes inference requests to the configured provider and submits tool calls to the daemon, which checks permissions and executes their effects. Credentials must not enter logs, events, database rows, or fixtures.

Domain code uses interfaces rather than importing persistence or transport. `internal/archcheck` enforces the import rules. Read the [authoritative architecture](../architecture.md) before changing a boundary.

Blocking work must respect cancellation, long-lived tasks need shutdown paths, and expensive parsing needs limits and cleanup. Performance changes must preserve approvals, unsaved drafts, and recovery records.

## Extend a format or contract

For a format, follow the [preview design](../design/content-preview-architecture.md): implement a parser producing the shared document shape, register it, and reuse an existing viewer or add a lazy viewer. Test valid, malformed, oversized, and cancelled input with synthetic fixtures.

Propose shared types, dependencies, OpenAPI, and architecture changes before editing, as required by [AGENTS.md](../../AGENTS.md). Preserve protocol negotiation and old-client behavior; do not invent duplicate domain types.

The repository and Go module use `github.com/GoGoslingCA/gogosling`. Clone this repository and build locally.

## Languages and validation

User-visible text belongs in locale packs. Current languages are `zh`, `en`, and `fr`; preserve placeholder names and existing fallback rules. English is the primary public repository language; code conventions remain in `AGENTS.md`.

See [Contributing](../../CONTRIBUTING.md) for checks and first tasks.
