# gogosling Architecture

> This document is the authoritative description of the architecture. If the implementation conflicts with this document, the implementation must be corrected to match it; if this document needs to be changed, update the documentation before the code.

## 1. Summary

**Thin clients, an independent daemon, and versioned protocols.** The Go daemon `gogoslingd` owns all business state. The desktop shell, web UI, and CLI are clients; a Python worker supervised by the daemon runs the agent reasoning loop.

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

- **Closing a window keeps the daemon running**: The desktop shell hides the window to the tray so scheduled tasks and messaging channels can continue. Quitting the app stops a daemon launched by that shell; an independently started daemon remains running. The shell handles launch, discovery, and reconnection and stores only UI preferences, not business state.
- **Worker process is a replaceable engine**: The core relies only on the interface defined by `internal/engine` and the `api/worker-rpc` protocol; it can later be replaced by a Go implementation or another Python framework.
- **Tool side effects run in the daemon**: The worker requests file, command, and network tools through reverse `host.tool.call` requests. The daemon checks permissions and applies the tool's workspace and sandbox constraints before execution. Provider inference requests are made directly by the worker.

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

Allowed dependencies within the domain layer (arrows mean "imports"):

```
tools      → permission, workspace, sandbox, extension (only uses ToolProvider interface)
engine     → (no domain dependencies; callbacks to tools and session via interfaces)
session    → agent, provider, engine (interface), permission (interface)
scheduler  → session (interface)
extension  → protocol/mcp
gateway    → session (interface)
permission → (no domain dependencies)
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

1. The client sends `POST /api/v1/sessions/{id}/messages`. `server` validates the token, Host, and Origin, then calls `session.Service.Send`.
2. `session` appends the user message, creates `Run(queued)`, publishes `message.created`, and hands over execution to `engine.Runtime.StartRun` (carrying Agent, message history, `ProviderCredentials`, and available `ToolSpec`).
3. `engine` sends the request to the worker through JSON-RPC `run.start`. The worker streams back `run.event` notifications; `engine` converts these into `model.RunEvent` values, and the session service persists state changes and publishes events.
4. When the worker process needs to invoke a tool, it issues a reverse request `host.tool.call`; `engine` calls `tools.Executor`, which first passes through `permission.Check` (may suspend waiting for approval and publish `approval.requested`), then executes with the tool's workspace and sandbox constraints and returns `ToolResult` to the worker.
5. Execution completes: `session` writes the assistant message, updates `Run` final state and usage, and publishes `run.completed`.
6. The client receives SSE events through `GET /api/v1/events?since=<seq>` and reconnects using the last sequence number. A daemon restart or a gap in retained events requires a full refresh.

### 5.2 Scheduled Tasks

`scheduler` parses the target session key when due and calls `session.Service.Send(trigger=job)`; if missed due to device sleep, it is handled according to `MissedPolicy`.

### 5.3 IM Messages

The `gateway` adapter normalizes platform messages into `(session key, text, attachments, principal)`, processes them via `session.Service.Send(trigger=channel)`, and sends responses back through the same adapter. All inbound content is treated as untrusted data.

### 5.4 Workbench Persistent State

- A Project organizes sessions and materials belonging to the same Agent; it does not create a separate file permission boundary. The optional `project_id` of Session does not change ID, Key, Origin, or execution state; legacy sessions remain unattached to projects. Archiving a project does not stop existing sessions.
- Project materials are uploaded reusing the Agent workspace `inbound/`, with name, path, summary, and ownership saved in the database. Each message explicitly selects references, and the kernel validates ownership and current content prior to sending; selecting materials does not grant or tighten tool file permissions.
- Drafts are saved by the kernel, unique by authenticated principal and scope; updates and deletions must check optimistic versioning. The body text does not enter browser or Electron UI preference files. Cross-window conflicts are returned to the client and must not be silently overwritten.
- Artifacts reference real assistant messages, appending an immutable Revision on each save. Artifact and draft bodies are stored in SQLite; the version list does not carry body text, and reading/downloading goes through authenticated HTTP APIs. Saving a version is not equivalent to overwriting workspace files.
- `model.WorkbenchStore` is an independent repository collection providing `WorkbenchTx`; the original interface collection of `model.Store` is not expanded as a result. Conditional updates of associations and versions are guaranteed by store within a transaction.
- P3B uses an independent persistent Operation as the subject of manual file operations; approval can only bind to either a real Run or a real Operation, without faking model runs. Both apply and undo pass through `tools.Executor` and the existing permission guards, deny rules, and one-time approvals. Operations are not exposed to the model. A ChangeSet binds the execution subject, artifact version, target, workspace identity, pre-image, and expiry to the preview digest; apply does not accept replacement content.
- Pre-image, proposal, and execution intent for single-file UTF-8 changes are stored in a protected database. Creation, replacement, and undo each require a separate preview; undoing a newly created file requires destructive approval. The workspace uses descriptor resolution, same-directory original preservation, and no-replace installation; if external writing is detected, it will not overwrite the new target. Linux and macOS have guarded implementations; unsupported platforms or filesystems reject in-place modification and offer export instead.
- Filesystem changes and database records cannot be committed in a single atomic transaction. The file result is recorded durably before temporary originals are cleaned up. The operation completes only after cleanup; a cleanup failure leaves it visibly in `recovery_required`. On restart, verify directory, inode, permission, and content identity; successful apply is recorded only if the retained execution journal or persisted post-installation identity matches completely. When both the journal and post-installation identity are missing, apply cannot be considered successful even if the content is identical. If the target still completely matches the pre-image, it is considered unapplied; if necessary, restore preserved originals to vacant targets strictly via no-replace, keeping the rest in recovery state. Operations pending recovery confirmation are forbidden from further writes. Waiting for approval does not hold database transactions or file locks; while applying, concurrent model tools for the same Agent are blocked, and starting file side-effects is rejected if an active model run exists.
- This implementation does not claim atomic CAS or security isolation against arbitrary external processes with the same UID; cases where same-UID processes hold old file descriptors or move parent directories during an operation still require detection and manual recovery. A hash check followed by rename, a directory mode of 0700, or a frontend confirmation cannot provide that guarantee.

### 5.5 Local Resource Scheduling

The daemon composition root defaults to a maximum of two parallel runs, with a pending queue of up to 32 items; users can adjust parallelism between 1–8. Existing same-session mutual exclusion remains in effect, and tasks in the same actual workspace are dispatched serially. User-initiated tasks take priority over scheduled tasks; tasks waiting longer than 30 seconds receive a boost to prevent background tasks from being blocked indefinitely. Queuing does not create Python tasks, pre-read full message history, or retain credential copies; configuration is re-parsed when execution begins. Canceling a queued run does not start the model, and lowering the cap does not kill currently executing tasks.

The kernel persists content and execution results; the client subscribes by session and merges incremental updates for display; approvals and final states do not wait for display timers. A terminal-state cache can be released only after its contents have been persisted and no page subscribes to it. No performance optimization may skip approvals, discard unsaved content, or simulate execution progress using percentages. Claims about energy savings or speed require measurements under equivalent loads; a queue limit alone does not establish an energy-saving percentage.

### 5.6 Prompt Enhancement Before Sending

The composer loads the enhancement module on demand and discovers support through the optional `prompt.enhance` capability. Requests use the current text, optional refinement feedback, and internal instructions with the selected model and existing worker RPC. The temporary inference identity creates no business Session or Run, publishes no chat events, and has no tool permissions. Enhancement shares the execution-slot budget, allows at most one request globally, and releases resources on cancellation or completion. The result replaces the composer text and offers Undo; the Refine dialog opens only on request. Internal instructions are excluded from frontend assets, logs, and persisted business records.

### 5.7 Content Preview

Workbench preview is registered by `internal/workbench/preview` and calls independent format modules; see [Content Preview Architecture](design/content-preview-architecture.md) for interfaces and directories. The composition root injects a controlled read-only workspace handle, and modules do not depend on server, store, or inference worker. One file is parsed at a time, with bounded reads; original files are served only through authenticated APIs. The frontend mounts only the current viewer, canceling requests and revoking Blob URLs when closed. HTML is displayed in an unprivileged iframe after sanitizing active content, with CSP rejecting external resources.

Legacy Office and layout previews use a local LibreOffice installation in a sandbox with restricted read access and no network access. They never fall back to online conversion or an unsandboxed process. The current conversion adapter targets macOS; other platforms retain built-in content reading and downloading. Previews are read-only and do not change P3B apply, undo, conflict detection, or approval behavior.

## 6. Security Invariants

1. **Loopback address is not a security boundary.** Local APIs bind only to loopback addresses (`127.0.0.1:46750` by default); all require Bearer tokens except `/healthz`; validate `Host` and `Origin` (allowing only empty Origin, desktop shell custom protocols, and dev server whitelists).
2. **Credentials never enter the database.** Model keys, channel credentials, and OAuth tokens live in `secret` storage. The database and configuration contain references only; credentials are supplied to worker memory through RPC at the point of use.
3. **Tool side effects follow the permission boundary.** The worker must not execute workspace file, shell, or network tools directly; it submits them to the daemon. Every tool call passes permission evaluation, receives any required approval, follows its workspace and sandbox constraints, and is audited. Direct provider inference traffic is separate from tool execution.
4. **Untrusted inputs carry a source.** IM messages, web content, and files are tagged when added to context. Permission rules may tighten behavior by source. A session must never automatically combine untrusted input, access to sensitive data, and outbound communication.
5. **Third-party code runs out-of-process.** Plugins and MCP services run as subprocesses; no third-party code is loaded inside the kernel process.
6. **Upgrades preserve recovery points.** When an existing database has pending migrations, the store backs up the old schema with an atomic SQLite snapshot before accepting application writes, verifying integrity, version, and digest; if backup fails, migration is rejected. New empty databases require no backup. Downgrading to a kernel lower than the data directory version is forbidden; recovering an older kernel requires stopping writes, preserving current data, and then restoring a matching verified snapshot, without automatically discarding post-upgrade work.

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
- **ADR-002 Go Kernel + Python Worker Process**: Go handles the control plane, gateway, scheduling, sandboxing, updates, and distribution (single binary, cross-compilation); Python preserves the agent framework and ML/RAG ecosystem. Packaged releases are designed to include the worker runtime without a first-launch download; source builds require the local Python/uv setup described in the [quickstart](development/quickstart.md).
- **ADR-003 Electron Shell**: Consistent rendering across three platforms with a mature ecosystem; no business logic resides within the shell, making it replaceable.
- **ADR-004 Single-File SQLite Control Plane**: Local-first, with no separate database service to administer; multi-device synchronization is a future capability that does not alter the local data model.
- **ADR-005 Tool Execution in Kernel**: Centralize permission checks, auditing, and sandbox dispatch for tool execution.
