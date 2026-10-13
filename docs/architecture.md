# gogosling Architecture

> This document is the authoritative description of the architecture. If the implementation conflicts with this document, the implementation must be corrected to match it; if this document needs to be changed, update the documentation before the code.

## 1. Summary

**Thin shell + Independent daemon + Versioned protocol.** All business state lives entirely inside the Go daemon `gogoslingd`; the desktop shell, Web UI, and CLI are merely its clients; the agent reasoning loop runs inside a Python worker process supervised by the daemon.

## 2. Process Model

```
┌───────────────────────────┐   ┌───────────────────────────┐   ┌──────────────┐
│ apps/desktop (Electron)   │   │ apps/web (React, same source) │   │ cmd/gogosling│
│ Tray · Windows · Auto-launch · Update │   │ Browser or in-shell rendering │   │ CLI          │
└─────────────┬─────────────┘   └─────────────┬─────────────┘   └──────┬───────┘
              │ Local HTTP + SSE (Bearer token, loopback only)           │
┌─────────────▼─────────────────────────────────────────────────────────▼───────┐
│ cmd/gogoslingd  ──  internal/app composition root                                     │
│  server ─► identity ─► { agent · session · provider · workspace · scheduler } │
│                        permission ─► tools ─► sandbox                        │
│                        extension ─► protocol/mcp                             │
│                        gateway (IM channels)   engine (worker process supervision) │
│  Foundation: store(SQLite) · secret(keychain) · event · observability · i18n     │
└───────────────────────────────────────────┬──────────────────────────────────┘
                                            │ stdio · Line-delimited JSON-RPC 2.0 (api/worker-rpc)
                                ┌───────────▼───────────┐
                                │ worker (Python)        │
                                │ runtime · providers    │
                                │ tools declarations · memory │
                                │ knowledge              │
                                └───────────┬───────────┘
                                            │ HTTPS
                                       Model service / Local model
```

- **Daemon outlives windows**: Required by scheduled tasks and IM channels. The desktop shell is responsible for launching, discovery, and reconnection, and owns no state.
- **Worker process is a replaceable engine**: The core relies only on the interface defined by `internal/engine` and the `api/worker-rpc` protocol; it can later be replaced by a Go implementation or another Python framework.
- **Side effects do not occur in the worker process**: The worker process only "declares" tool calls; actual file, command, and network operations are executed by the daemon after permission checks and sandboxing (reverse requests to `host.tool.call`).

## 3. Code Layout

```
cmd/gogoslingd        Daemon entrypoint (arguments, logs, signals)
cmd/gogosling         CLI entrypoint (HTTP client, touches no data directories)
internal/app          Composition root: constructs modules, lifecycle, runtime handle files
internal/server       Local HTTP API + SSE (contract api/openapi); servertest/ subpackage is the test harness (the only transport layer code allowed to open the store directly)
internal/client       Go API client (reused by CLI and integration tests)
internal/channel      IM channel management service (credentials go only to secret; adapted to server.ChannelService via composition root)
internal/archcheck    Automated tests for dependency directions (executable version of §4 in this document)
tests/e2e             End-to-end tests (build tag e2e: real Python worker process + mock model service)
internal/identity     Access tokens, Principals
internal/permission   Rule engine, approval proxy, auditing
internal/tools        Declaration and execution of built-in tools (via permission, workspace, sandbox)
internal/sandbox      OS-level isolated execution
internal/agent        Agent definitions, template library
internal/session      Sessions, messages, runtime state machine
internal/provider     Vendors and model configuration, connectivity testing, local model discovery
internal/workspace    Workspace file access and path constraints
internal/workbench    Project and material indexing, recoverable drafts, text deliverables, and immutable versions; reuses session and workspace capabilities
internal/engine       Runtime interface + Python worker process supervision + JSON-RPC client
internal/scheduler    Scheduled tasks and missed task compensation
internal/gateway      IM channel adapter interfaces and routing
internal/extension    Installation and enablement of skills, MCP services, and plugins
internal/protocol/mcp MCP host-side implementation (official go-sdk)
internal/protocol/acp ACP bidirectional integration
internal/store        SQLite persistence, migrations, repository implementations
internal/secret       Credential storage (OS keychain, fallback to encrypted files)
internal/event        In-process event bus (Seq, replay)
internal/observability Logs, tracing, metrics
internal/i18n         Core-side copy (zh / en / fr)
internal/update       Core and runtime environment upgrades
internal/model        Shared domain types, error codes, repository interfaces (leaves)
internal/config       Data directories and process configuration (leaves)
internal/version      Build information (leaves)
worker/               Python worker process (uv project)
apps/web              React + TypeScript + Vite
apps/desktop          Electron shell
api/openapi           Local HTTP API contract (OpenAPI 3.1)
api/worker-rpc        Daemon ↔ Worker process protocol
extensions/bundled    Built-in extensions distributed with releases (content assets, not code)
docs/                 Documentation
```

## 4. Dependency Rules

Dependencies must flow **inward** only. Each row in the table below may only import packages listed to its right (along with the standard library and approved third-party libraries).

| Layer | Package | May Import |
|---|---|---|
| Leaves | `model` `config` `version` `event` `i18n` `observability` `secret` | Standard library and third-party libraries; **must not import any `internal/` packages** (`secret`, `i18n` therefore use sentinel errors / mirrored lists, mapped to `model.Code` by callers) |
| Foundation | `store` | `model` |
| Domain | `agent` `session` `provider` `workspace` `workbench` `permission` `scheduler` `extension` `protocol/*` `tools` `sandbox` `engine` `gateway` `identity` `channel` `update` | Leaf layer + packages in the same layer **allowed by the table below** |
| Transport | `server` `client` | Leaf layer + Domain layer; **must not import `store`** (exception for `server/servertest` test harness) |
| Composition Root | `app` `cmd/*` | Any package |

Allowed dependencies within the domain layer (arrows mean "imports"):_

```
tools      → permission, workspace, sandbox, extension (only uses ToolProvider interface)
engine     → (no domain dependencies; callbacks to tools and session via interfaces)
session    → agent, provider, engine (interface), permission (interface)
scheduler  → session (interface)
extension  → protocol/mcp
gateway    → session (interface)
permission → secret? No. permission does not depend on other domain packages.
workbench  → (no domain dependencies; reuses capabilities like session and workspace via interfaces injected by the composition root)
```

**Strict Prohibitions**

- `model`, `config`, `event`, `secret`, `i18n`, and `observability` must not import any non-leaf packages under `internal/`.
- `store` can only import `model`; domain packages must not import `store` (accessed via interfaces such as `model.Store`).
- Domain packages must not import `server`, `client`, or `app`.
- `worker/` does not read or write the data directory, listen on ports, or persist secrets.
- `apps/*` accesses the kernel only via local HTTP API and does not directly open databases or workspaces.
- No package may write plaintext credentials into `store`, logs, or events.

Dependency direction is enforced by tests in `internal/archcheck`: it parses imports in all files under `cmd/` and `internal/`, comparing against the table above; layer-crossing imports will cause `go test ./...` to fail.

## 5. Core Data Flows

### 5.1 A Single Conversation

1. The client `POST /api/v1/sessions/{id}/messages`, `server` validates the token and Origin, and calls `session.Service.Send`.
2. `session` appends the user message, creates `Run(queued)`, publishes `message.created`, and hands over execution to `engine.Runtime.StartRun` (carrying Agent, message history, `ProviderCredentials`, and available `ToolSpec`).
3. `engine` passes to the worker process via JSON-RPC `run.start`; the worker process streams back `run.event` notifications, `engine` converts them into `model.RunEvent` and publishes them to `event.Bus`; `session` subscribes to and persists state changes.
4. When the worker process needs to invoke a tool, it issues a reverse request `host.tool.call`; `engine` calls `tools.Executor`, which first passes through `permission.Check` (may suspend waiting for approval and publish `approval.requested`), then executes within `sandbox`, and returns `ToolResult` to the worker process.
5. Execution completes: `session` writes the assistant message, updates `Run` final state and usage, and publishes `run.completed`.
6. The client receives events via `GET /api/v1/events?since=<seq>`'s SSE throughout; upon disconnection, it reconnects carrying the last Seq.

### 5.2 Scheduled Tasks

`scheduler` parses the target session key when due and calls `session.Service.Send(trigger=job)`; if missed due to device sleep, it is handled according to `MissedPolicy`.

### 5.3 IM Messages

The `gateway` adapter normalizes platform messages into `(session key, text, attachments, principal)`, processes them via `session.Service.Send(trigger=channel)`, and sends responses back through the same adapter. All inbound content is treated as untrusted data.

### 5.4 Workbench Persistent State

- Project only organizes sessions and materials for the same Agent without creating an independent file permission boundary. The optional `project_id` of Session does not change ID, Key, Origin, or execution state; legacy sessions remain unattached to projects. Archiving a project does not stop existing sessions.
- Project materials are uploaded reusing the Agent workspace `inbound/`, with name, path, summary, and ownership saved in the database. Each message explicitly selects references, and the kernel validates ownership and current content prior to sending; selecting materials does not grant or tighten tool file permissions.
- Drafts are saved by the kernel, unique by authenticated principal and scope; updates and deletions must check optimistic versioning. The body text does not enter browser or Electron UI preference files. Cross-window conflicts are returned to the client and must not be silently overwritten.
- Artifacts reference real assistant messages, appending an immutable Revision on each save. Artifact and draft bodies are stored in SQLite; the version list does not carry body text, and reading/downloading goes through authenticated HTTP APIs. Saving a version is not equivalent to overwriting workspace files.
- `model.WorkbenchStore` is an independent repository collection providing `WorkbenchTx`; the original interface collection of `model.Store` is not expanded as a result. Conditional updates of associations and versions are guaranteed by store within a transaction.
- P3B uses an independent persistent Operation as the subject of manual file operations; approval can only bind to either a real Run or a real Operation, without faking model runs. Both application and undo pass through tools.Executor and existing permission guard/deny/one-time approval, and operations are not provided to the model. ChangeSet binds subject, artifact version, target, workspace identity, pre-image, and expiration summary; application does not accept new body text.
- Pre-image, proposal, and execution intent for single-file UTF-8 changes are stored in a protected database. Creation, replacement, and undo with independent preview are supported; undoing a newly created file requires destructive approval. The workspace uses descriptor resolution, same-directory original preservation, and no-replace installation; if external writing is detected, it will not overwrite the new target. Linux/macOS provide controlled implementations; unsupported platforms or file systems reject in-place modification and retain export entries.
- Files and databases cannot be atomically committed across media. Real file results are persistently recorded first, and the operation terminates only after original cleanup completes; cleanup failures also enter a visible recovery_required state. On restart, check complete directory, inode, permissions, and content identity; successful application is acknowledged only if the preserved log or persistently verified post-installation identity matches completely. When both log and post-installation identity are missing, application cannot be considered successful even if body text is identical. If the target still completely matches the pre-image, it is considered unapplied; if necessary, restore preserved originals to vacant targets strictly via no-replace, keeping the rest in recovery state. Operations pending recovery confirmation are forbidden from further writes. Waiting for approval does not hold database transactions or file locks; during application, concurrent model tools for the same Agent are blocked, and starting file side-effects is rejected if an active model run exists.
- This implementation does not claim atomic CAS or security isolation against arbitrary external processes with the same UID; cases where same-UID processes hold old file descriptors or proactively move parent directories still require detection and manual recovery. hash+rename, directory 0700, or frontend confirmation cannot be treated as such a guarantee.

### 5.5 Local Resource Scheduling

The desktop composition root defaults to a maximum of two parallel runs, with a pending queue of up to 32 items; users can adjust parallelism between 1–8. Existing same-session mutual exclusion remains in effect, and tasks in the same actual workspace are dispatched serially. User-initiated tasks take priority over scheduled tasks; tasks waiting longer than 30 seconds receive a boost to prevent background tasks from being blocked indefinitely. Queuing does not create Python tasks, pre-read full message history, or retain credential copies; configuration is re-parsed when execution begins. Canceling queuing does not start the model, and lowering the cap does not kill currently executing tasks.

Body text and execution results are still persisted by the kernel; the client subscribes by session and merges incremental updates for display; approvals and final states do not wait for display timers. Real-time final state caches can only be released after being persisted to the DB and no longer subscribed by pages. No performance optimization may skip approvals, discard unsaved content, or simulate execution progress using percentages. Energy savings and speed comparisons require empirical testing under the same load; the queue cap itself does not equal energy saving percentages.

### 5.6 Pre-sending Prompt Optimization

The input area's optimization module loads on click, discovering kernel interfaces via the optional capability `prompt.enhance`. Optimization carries only current text and internal instructions, reusing the current model and worker RPC; the temporary inference identity does not create business Session/Run, publish chat events, or hold tool permissions. It shares the execution slot budget, limited to at most one globally, cancels along with requests, and releases resources upon reaching final state. Results directly populate existing drafts and offer restoration; the modification suggestions popup opens only when the user chooses to adjust. Internal instructions do not enter frontend assets, logs, or business persistent state.

### 5.7 Content Preview

Workbench preview is registered by `internal/workbench/preview` and calls independent format modules; see [Content Preview Architecture ](design/content-preview-architecture.md) for interfaces and directories. The composition root injects a controlled read-only workspace handle, and modules do not depend on server, store, or inference worker. Content parsing is single-concurrency with bounded reading; original files are output only via authenticated APIs. The frontend mounts only the current viewer, canceling requests and revoking Blob URLs when closed. HTML is displayed in an unprivileged iframe after sanitizing active content, with CSP rejecting external resources.

Legacy Office and layout previews rely on local LibreOffice and a conversion sandbox with restricted read and network prohibition, without falling back to online conversion or unisolated processes. The current conversion adapter targets macOS; other platforms retain built-in content reading and downloading. Previews are read-only and do not alter P3B application modifications, undos, conflict checks, or approvals.

## 6. Security Invariants

1. **Loopback address is not a security boundary.** Local APIs bind only to `127.0.0.1`; all require Bearer tokens except `/healthz`; validate `Host` and `Origin` (allowing only empty Origin, desktop shell custom protocols, and dev server whitelists).
2. **Credentials never enter the database.** Model keys, channel credentials, and OAuth tokens live in `secret` storage. The database and configuration contain references only; credentials are supplied to worker memory through RPC at the point of use.
3. **Side effects must undergo approval and sandboxing.** Worker processes have no permission to directly touch the file system, shell, or network; all tool executions go through the `permission → tools → sandbox` pipeline and write audit logs.
4. **Untrusted inputs carry a source.** IM messages, web content, and files are tagged when added to context. Permission rules may tighten behavior by source. A session must never automatically combine untrusted input, access to sensitive data, and outbound communication.
5. **Third-party code runs out-of-process.** Plugins and MCP services run as subprocesses; no third-party code is loaded inside the kernel process.
6. **Upgrades preserve recovery points.** When an existing database has pending migrations, the store backs up the old schema with an atomic SQLite snapshot before opening business logic, verifying integrity, version, and digest; if backup fails, migration is rejected. New empty databases require no backup. Downgrading to a kernel lower than the data directory version is forbidden; recovering an older kernel requires stopping writes, preserving current data, and then restoring a matching verified snapshot, without automatically discarding post-upgrade work.

## 7. Data Directory (default `~/.gogosling`, environment variable `GOGOSLING_HOME`)

```
config.json              Process configuration (optional)
gogosling.db(-wal,-shm)  SQLite control plane (WAL)
daemon.json              Runtime handle: {pid, endpoint, token, version, started_at}, permissions 0600; deleted on exit
daemon.lock              Single-instance file lock (contains pid)
secret.backend           Initially selected credential backend (keyring | file), not silently switched afterwards
secrets.enc / secret.key Encrypted fallback storage and its key when no keychain is available (both 0600)
security/rules.json      Permission rules (persisted from built-in defaults on first use; ScopeAlways decisions are written here)
agents/<id>/             Agent workspace (inbound/ stores attachments)
extensions/<id>/         Installed extensions (ULID directory names)
logs/gogoslingd.log      Logs (rotated by size, old files gzipped)
backups/                 Pre-upgrade snapshots
```

All files and directories are visible only to the current user (files 0600, directories 0700); tool calls referencing these kernel files will be hard-rejected by the permission engine.

Migration snapshots are located in `backups/` under the database directory, using SQLite `VACUUM INTO` containing committed data in the WAL, accompanied by a JSON manifest recording the source/target schema, filenames, UTC time, and SHA-256. This is not an automated recovery tool, nor does it roll back user workspace files. Business records for Project, Draft, and Artifact/Revision reside entirely in the database; Electron userData stores only non-business UI preferences such as window positions.

## 8. Contracts

| Contract | Location | Versioning Policy |
|---|---|---|
| Local HTTP API | `api/openapi/openapi.yaml` | Path prefix `/api/v1`; breaking changes bump to v2 |
| Daemon ↔ Worker Process | `api/worker-rpc/protocol.md` | `worker.hello` negotiates `protocol_version`; startup is refused if incompatible |
| Event Stream | `Event` + `model.RunEvent` in OpenAPI | Append-only event types; existing field semantics remain unchanged |
| Errors | `model.Code` ↔ HTTP status ↔ `i18n` key `errors.<code>` | Additive only, no removals |

## 9. Decisions Made (ADR Summary)

- **ADR-001 Daemon-First**: UI is a client, not the host. Rationale: Scheduled tasks and IM channels need to survive window closures; multiple frontends share a single set of APIs.
- **ADR-002 Go Kernel + Python Worker Process**: Go handles the control plane, gateway, scheduling, sandboxing, updates, and distribution (single binary, cross-compilation); Python preserves the agent framework and ML/RAG ecosystem. The runtime environment is distributed with the installation package and is not downloaded over the network on first launch.
- **ADR-003 Electron Shell**: Consistent rendering across three platforms with a mature ecosystem; no business logic resides within the shell, making it replaceable.
- **ADR-004 Single-File SQLite Control Plane**: Local-first, zero operations; multi-device synchronization is a future capability that does not alter the local data model.
- **ADR-005 Tool Execution in Kernel**: Only a single security boundary exists, facilitating auditing, approval, and sandboxing.
