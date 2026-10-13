# Workbench Stage Three Contract Change Proposal

Status: The user authorized P3A on 2026-10-12 and subsequently authorized P3B file preview, apply, revert, and compatibility validation; both stages have been implemented locally. This document retains the design rationale; final fields and signatures are governed by internal/model/workbench.go, change.go, operation.go, repository.go, and OpenAPI. Verification baseline: `b8bbd0d` and the UI changes in this working tree.

This document adopts the style of existing Chinese engineering notes to enable leads to explicitly approve the change scope. It does not replace `docs/architecture.md`.

## 1. Scope Recommended for Approval

Implement P3A first: a project organizes sessions and materials under the same agent; input and deliverable drafts are persisted by the kernel; deliverables have reopenable indexes and immutable revisions; users can read, download, and continue modifying these revisions. The full flow is "add requirement doc → select materials for this run → dialogue to form initial draft → edit → save new revision → resume after restart".

P3A does not provide arbitrary workspace file writing, overwriting, execution, or on-disk revert buttons. Existing "Save Copy" is retained; new revision saving and file downloads are named distinctly to avoid characterizing revision saving as having modified the original file.

Single-file changes and standalone Operation entities for P3B have received subsequent user authorization. Capabilities are declared after full assembly; unsupported platforms reject in-place modifications. UI requests still go through `tools.Executor` and `permission`, and each frozen preview permits only a single approval.

The only shared files requiring modification for P3A are:

- `internal/model/session.go`: Add optional `project_id` to Session, without altering ID, Key, Origin, or existing status.
- `internal/model/repository.go`: Add project, material, draft, and deliverable repository interfaces, plus an independent WorkbenchStore; extend SessionFilter with optional project filtering. The original `model.Store` is not forced to embed the new interfaces to prevent unrelated tests and consumers from expanding dependencies.
- New `internal/model/workbench.go`: Shared types and input constraints for Project, ProjectMaterial, Draft, Artifact, and ArtifactRevision; P3A does not add ChangeSet types unsupported at runtime.
- `api/openapi/openapi.yaml`: Add resource routes, fields, capability declarations, and events; legacy fields and route semantics remain unchanged.
- `docs/architecture.md`: Register responsibilities, dependencies, and data ownership of `internal/workbench`, stating that P3A adds no direct file modification channels; clarify actual implementation requirements for pre-upgrade backups.

No modifications required for `go.mod`, `Makefile`, `AGENTS.md`, `api/worker-rpc/*`, `internal/model/run.go`, or `internal/model/approval.go`. The latter two will be listed separately only after the P3B approval entity design is finalized.

Standard implementation files are expected to involve `internal/workbench/*`, `internal/store/*`, `internal/session/*`, `internal/server/*`, `internal/app/*`, necessary `internal/i18n/*`, and frontend API/state layers. Actual sequence numbers for new SQL will be assigned during merge and must not preempt `002` or `003` in parallel team draft proposals; already released `001_init.sql` will not be modified.

## 2. Verified Current State and Reuse Boundaries

- `internal/model/session.go:27`: Session is a persistent dialogue thread bound to an Agent; Key is used for routing, not as a project identifier.
- `internal/model/repository.go:131`: Already has `Store.Tx`; `Send` in `internal/session/run.go` invokes the model only after placing the user message and Run in the same transaction.
- `internal/server/handlers_sessions.go:199`: Attachments must be real files under `inbound/` of the corresponding Agent workspace; `sessionWorkspace` locates workspaces by Agent.
- `internal/workspace/inbound.go:32`: Upload sanitizes names, saves duplicates under new names, enforces size limits, and atomically replaces via temporary files; it is not file version management.
- `internal/workspace/workspace.go:105`: The current Workspace concurrent write semantics follow last-rename-wins; it cannot be treated as a compare-and-swap with baseline validation.
- `internal/tools/executor.go:139`: The current Executor resolves context via actual RunID, then checks tool enablement status, permissions, and execution paths.
- `internal/permission/service.go:306`: `Check` centrally executes guards, rules, and approvals; requests can block waiting for decisions.
- `internal/store/migrations/001_init.sql:99`: `approvals.run_id` is a non-null foreign key; `internal/store/approvals.go:14` similarly relies on real runs. Manual file operations cannot leave RunID empty or forge a model Run to reuse it.
- `apps/web/src/lib/artifacts.ts:50`: Deliverables are currently assistant message derived views; `ArtifactPanel` save invokes attachment upload, not a deliverable store.
- `apps/web/src/pages/chat/Composer.tsx:53` and `ArtifactPanel.tsx:53`: Unsent and unsaved body texts exist only in React memory. Leave confirmation cannot restore drafts after crashes.
- `internal/store/migrate.go:86`: Already features versioned transactional migrations and rejects old kernels opening new schemas; `internal/update` currently only has `doc.go` and cannot yet claim an automated backup and rollback implementation.

## 3. Minimal Domain Boundaries

Only add one new domain package `internal/workbench`, split internally by project, material, draft, and artifact files, without building generic knowledge platforms, team permission platforms, or new model execution frameworks.

This package only directly imports leaf packages. The session, workspace, and permission capabilities it depends on are described via consumer interfaces within this package and adapted by `internal/app`, avoiding direct imports of `session`, `workspace`, `permission`, and `store`. The transport layer handles auth, HTTP, size limits, and download responses; the domain layer handles ownership, revisions, lineage, and consistency; store handles SQL and transactions.

In P3A, Project fixes `agent_id` and serves merely as an organizational container for materials and tasks; it does not own an independent workspace or alter tool accessible scopes. Different projects under the same Agent still share the Agent workspace, which should be documented in the project file scope description. Moving projects across Agents, mixing multiple Agents within a project, and isolated project sandboxes are out of scope for this version.

New business state resides in the kernel database; layout-only preferences are still saved by Web/Electron. Body text, material references, and deliverable revisions are not placed in localStorage, Electron userData, or IPC.

## 4. P3A Resource Definitions

All IDs continue to use ULID from `model.NewID()`, timestamps are UTC, and listings reuse `model.Page`. All `version` are positive integers starting from 1; updates use database conditional writes, rejecting "read-then-unconditionally-overwrite". Content limits below are proposal defaults, announced via capability responses upon release.

### 4.1 Project and Session Associations

Project fields: `id`, immutable `agent_id`, `name` (1–120 Unicode characters), `description` (up to 2,000 characters), `status: active|archived`, `version`, `created_at`, `updated_at`.

Project has no writable `workspace_dir`; responses can reference existing Agent info, with real file scopes provided by existing Agent interfaces. Project archiving only hides organizational entry points, without archiving, stopping, or deleting existing sessions, and without altering legacy client operational capabilities on those sessions. This version has no hard delete API for projects.

Session adds `project_id?: string`. Legacy rows are all NULL, and legacy JSON omits this field; new sessions may omit projects. Upon association, the server validates project existence and `project.agent_id == session.agent_id`. Moving into or out of a project does not alter `session.id/key/origin`, replay messages, migrate disk files, or create new Runs.

Association changes are handled via a dedicated interface to prevent mutual overwrites with concurrent title/status PATCH updates:

```text
PUT /api/v1/sessions/{id}/project
{ "project_id": "<id>" | null, "expected_project_id": "<id>" | null }
```

Both fields are required; mismatch with expected returns `conflict`. Upon success, existing `session.updated` is published. Session creation adds optional `project_id`, validating Agent consistency before persistence; legacy creation flows without this field remain as-is.

### 4.2 ProjectMaterial and Material Selection for Current Run

ProjectMaterial fields: `id`, `project_id`, `agent_id`, `name`, `mime`, `size`, `sha256`, internally validated `attachment` (reusing existing Attachment), `created_at`. In this version, each upload is a new material and does not overwrite old materials; material names are not identities.

Upload reuses `Workspace.SaveInbound`, resolving workspace via the project's Agent; responses follow the final server-sanitized name and relative path. File writes must complete before submitting the index, and failures must error; cleanup is restricted to newly created files from this request provably unreferenced elsewhere, without scanning and deleting other historical inbound files.

Project materials are listed in index right upon upload, without requiring a chat message to be sent first. Uploaded but unindexed historical files will not be scanned and added to any project by default.

`POST /sessions/{id}/messages` adds optional `material_ids: string[]`; legacy `attachments` remains valid. New clients explicitly submit selections for the current run, not automatically including the entire project material library by default. The server validates that material project matches session project, Agent matches, file exists, current size and SHA-256 match material records, expands into existing Attachment, and invokes `Session.Send`. If any reference is invalid, the entire send is rejected; no silent skips, and no covert use of changed files.

If both `material_ids` and `attachments` appear, deduplicate by normalized relative path; total count is still constrained by existing attachment limits. Source material ID and ingestion summary are stored as message non-inference metadata; actual inference still uses existing attachment parts, without altering worker RPC.

Material selection here is context selection, not a tool file permission allowlist. Selected materials carry provenance markers and cannot be laundered into system instructions. Adding the capability for "model can only access selected materials" requires separate run-level file capability constraints, which cannot rely on prompts or hidden lists.

`DELETE /projects/{project_id}/materials/{id}` only removes project index, without deleting inbound files or modifying sent historical messages. Invalid references in drafts are retained upon restore as "material unavailable", requiring user handling before sending; draft body text is not discarded.

### 4.3 Drafts and Optimistic Versioning

Draft fields: `id`, `owner_id` (obtained by server from Principal), `kind: composer|artifact`, `agent_id`, optional `project_id/session_id/artifact_id/base_revision_id`, `text`, optional `filename`, `material_ids`, compatible `attachments`, `version`, `created_at`, `updated_at`.

Scopes are generated by the server and do not accept arbitrary strings:

- Start page input: `owner + composer + agent + project-or-none`, no session.
- Session input: `owner + composer + session`.
- Artifact editing: `owner + artifact + artifact_id`; references an immutable artifact version via `base_revision_id`. Baseline references can be modified via Draft expected_version CAS, but the new revision must belong to the same artifact; owner or scope cannot be modified through this.

The database establishes unique constraints on the aforementioned scopes. On initial creation POST, if the scope already exists, return conflict and the existing draft ID, and the client provides restore/replace choices after reading. Subsequent PUTs must include `expected_version`; on success, return version+1; on mismatch, return 409 and `reason=stale_version,current_version`, without stuffing the full body into the error. Deletions must likewise include expected_version.

Body limit is 512 KiB UTF-8, file names up to 160 characters, and attachment uploads still go through the standalone file endpoint; drafts cannot embed file binaries, connection tokens, or model credentials. Field associations are all verified on the server side, and any owner_id passed by the client is rejected.

Auto-save must clearly distinguish between "Saving / Saved / Save failed". The client advances the confirmed version only after the server returns; it retains the in-memory body when offline or when requests fail. Cross-window conflicts retain both sides' content for the user to choose, without automatic last-write-wins.

After creating a session from the start page, bind the draft to the new session with version verification. If binding fails, do not delete the start page draft; upon recovery, prompt with existing session and draft, without automatically re-sending. The first version does not commit to atomic cross-API "create session and send".

Deleting the old draft after a successful send must use the draft version from the time of sending; if the user continues editing during sending, deletion fails due to version mismatch, and the new draft is retained. If responses are lost or the client crashes, drafts may persist: re-opening first displays "May have been sent, please check history", and must not automatically re-send. If strictly-once sending is required in the future, a client message idempotency key must be separately added; this version does not claim to have this capability.

### 4.4 Artifacts, Revisions, and Provenance

Artifact fields: `id`, `agent_id`, `session_id`, derived display `project_id?`, `title`, `kind: document|code|text`, `filename`, `media_type`, `source`, `latest_revision_id`, `version`, `created_at`, `updated_at`.

source must reference a real assistant Message in the session: `message_id`, `run_id?` obtained by the server from the message, `message_seq`, `source_sha256`, `partial`, and optional `fragment` (full text or code snippet index, for UI positioning only). Clients cannot reference messages from other sessions and cannot submit tool outputs to impersonate assistant artifacts. The server validates the source message and records the original provenance; the body saved by the user is explicitly a copy edited from the source, with no commitment to code snippet parsing or existence of original file paths.

ArtifactRevision fields: `id`, `artifact_id`, `number`, `parent_revision_id?`, `content`, `sha256`, `byte_size`, `filename`, `media_type`, `created_by`, `created_at`. Each version is immutable, `UNIQUE(artifact_id, number)`. Maximum 512 KiB UTF-8, initially supporting documents and code text, without expanding into an office document conversion platform.

Initial save explicitly creates Artifact and Revision 1, both within the same database transaction as provenance validation. Modification save POSTs a new revision and must include `expected_latest_revision_id`; insert and advance latest only if the baseline still matches. Conflicts return the current latest ID, without overwriting others' versions. Reading older versions does not change latest.

Restoring an older version uses "save a new version with old version content", retaining full audit trails and version chains; newer versions are not deleted. Editor undo for unsaved edits affects only Draft; both are named separately from P3B disk undo.

Historical assistant messages are not automatically migrated into artifacts. Derived IDs used by the legacy UI remain available, establishing `source message + fragment → artifact ID` mapping upon the user's first save; creation supports `Idempotency-Key`, where retries with the same principal, path, and request content return the original resource, while reusing the key with different content returns a conflict.

List and metadata responses do not contain the body. Content read and download endpoints must pass through the same API authentication, without returning `file://`, system absolute paths, or URLs with tokens. Downloads enforce `Content-Disposition: attachment`, using sanitized file names and `X-Content-Type-Options: nosniff`; text preview fixes a safe Content-Type, and must not execute model HTML as same-origin pages. Byte count and SHA-256 match the revision; exceeding limits is rejected rather than silently truncated.

## 5. P3A HTTP, Events, and Capability Discovery

Candidate endpoints (all backward-compatible extensions under `/api/v1`):

```text
GET, POST   /projects
GET, PATCH  /projects/{id}                  PATCH requires expected_version
GET, POST   /projects/{id}/materials        POST is multipart file upload
DELETE      /projects/{id}/materials/{material_id}
PUT         /sessions/{id}/project          CAS updates project association

GET, POST   /drafts                         GET can filter by scope field, owner is implicit
GET, PUT    /drafts/{id}                    PUT requires expected_version
DELETE      /drafts/{id}?expected_version=N

GET, POST   /sessions/{id}/artifacts         Creation includes initial revision
GET         /projects/{id}/artifacts        Aggregated by session's current project association
GET         /artifacts/{id}
GET, POST   /artifacts/{id}/revisions        POST requires expected_latest_revision_id
GET         /artifacts/{id}/revisions/{revision_id}
GET         /artifacts/{id}/revisions/{revision_id}/content
GET         /artifacts/{id}/revisions/{revision_id}/download
```

Metadata GET for ArtifactRevision does not include content; `/content` returns `text/plain; charset=utf-8` raw UTF-8 bytes; `/download` returns `application/octet-stream` and attachment. Both include `Cache-Control: no-store`. The initial version does not support range downloads.

Reads return 200, creates return 201, and successful deletions return 204. Continue using existing `invalid/unauthorized/forbidden/not_found/conflict/unavailable/internal`, without adding a duplicate set of error codes alongside the existing model. Candidate reason for conflict details is `stale_version/source_changed/material_changed/scope_mismatch`, containing only resource ID, version, and necessary relative paths.

`GET /version` adds an optional `capabilities` object, with key and value examples:

```json
{
  "workbench.projects": 1,
  "workbench.materials": 1,
  "workbench.drafts": 1,
  "workbench.artifacts": 1,
  "workbench.limits": { "text_bytes": 524288 }
}
```

Only complete capabilities that are implemented and pass migration and initialization checks can appear. Missing fields on legacy daemons are treated as unsupported; the new UI hides corresponding write entry points and continues to provide legacy sessions, attachment uploads, and message copy exports. Feature guessing using version strings is prohibited, and fields unrecognized by legacy services must not be sent to them. P3B's `workbench.changesets` remains absent until subsequent complete implementation.

New events: `project.created/updated`, `project.material.created/removed`, `draft.updated/deleted`, `artifact.created/revision.created`. Emit only metadata such as ID, version, and source ID, without sending draft/document full text, secrets, or uploaded binaries. The client invalidates corresponding queries upon receiving events; new versions are displayed only after the corresponding resource is successfully re-read.

Retain existing `session.*`, `run.*`, approval events, global Seq, `hello.started_at`, `since`, and `gap` semantics. A daemon restart must discard the old Seq and perform a full refresh. New events are simply ignored by legacy clients, and run.* is not reused to represent draft auto-saving.

## 6. P3A Repository and Consumer Interface Candidates

Below are the signature outlines planned for inclusion after review approval. Input/Filter are explicitly defined within shared workbench types, and quietly adding another set of domain types during implementation is not permitted.

```go
type ProjectRepo interface {
    CreateProject(context.Context, *Project) error
    GetProject(context.Context, string) (*Project, error)
    ListProjects(context.Context, ProjectFilter, Page) ([]*Project, error)
    UpdateProject(context.Context, *Project, int64) error // expectedVersion
    SetSessionProject(context.Context, string, string, string) error // session, expected, next
    CreateProjectMaterial(context.Context, *ProjectMaterial) error
    ListProjectMaterials(context.Context, string, Page) ([]*ProjectMaterial, error)
    GetProjectMaterial(context.Context, string) (*ProjectMaterial, error)
    DeleteProjectMaterial(context.Context, string) error // Only deletes reference
}

type DraftRepo interface {
    CreateDraft(context.Context, *Draft) error
    GetDraft(context.Context, string) (*Draft, error)
    ListDrafts(context.Context, DraftFilter, Page) ([]*Draft, error)
    UpdateDraft(context.Context, *Draft, int64) error
    DeleteDraft(context.Context, string, int64) error
}

type ArtifactRepo interface {
    CreateArtifact(context.Context, *Artifact) error
    GetArtifact(context.Context, string) (*Artifact, error)
    ListArtifacts(context.Context, ArtifactFilter, Page) ([]*Artifact, error)
    AppendArtifactRevision(context.Context, *ArtifactRevision, string) error // expectedLatest
    GetArtifactRevision(context.Context, string, string) (*ArtifactRevision, error)
    ListArtifactRevisions(context.Context, string, Page) ([]*ArtifactRevision, error)
}

type WorkbenchStore interface {
    ProjectRepo
    DraftRepo
    ArtifactRepo
    SessionRepo
    MessageRepo
    AuditRepo
    WorkbenchTx(context.Context, func(WorkbenchStore) error) error
}
```

`store.Store` satisfies both the original Store and WorkbenchStore; WorkbenchTx shares transaction implementations with existing Tx, and within callbacks it is non-reentrant and holds no network requests or user approvals. New repository methods use existing CodeNotFound/CodeConflict conventions. Additional request idempotency records target only newly created resources, restricted to principal and endpoint scopes, storing request digests rather than credentials.

Capabilities reused by the `workbench` consumer interface: Agent lookup, Session lookup, Message lookup, workspace `SaveInbound/ReadFile/Stat/Resolve`, event bus, and audit appending. File reading and writing are performed by the workspace adapter injected by app; existing HTTP handler upload logic can extract shared helpers, but server does not directly import store.

Recommended database tables are `projects`, `project_materials`, `drafts`, `artifacts`, `artifact_revisions`, and a new resource request idempotency table, along with adding a nullable project_id to sessions. Text content can initially use SQLite TEXT to avoid introducing dual-write consistency issues with external blobs in this version; content size and total volume must have explicit limits. Content of revisions is serialized only through dedicated read endpoints.

Project attribution must remain within the same Agent: beyond domain validation, migrations prevent cross-Agent associations via composite constraints or triggers. Projects have no hard deletion; existing deletion semantics for Agents and Sessions remain in effect, newly added child records cascade according to confirmed foreign keys, and UI deletion confirmations must explain that drafts and artifacts will also be deleted. P3A does not automatically clean up existing inbound files on disk. Moving a session out of a project only changes the association and does not delete artifacts; the project artifact list updates with session attribution changes.

## 7. P3B Single-File ChangeSet Contract

On 2026-10-12, the user authorized continuing P3B, adopting an independent persistent Operation subject. The initial version is restricted to ordinary UTF-8 text files within a workspace, up to 512 KiB, supporting creation, complete replacement, and undo with independent previews; undoing creation requires approval as destructive deletion. Multi-file transactions, directories, arbitrary deletion, renaming, binaries, permission bit modifications, out-of-workspace paths, script execution, or automatic publishing are not supported.

Candidate ChangeSet fields: `id`, `session_id/agent_id`, `artifact_id/revision_id`, `target_path` (normalized relative path only), `operation: create|replace`, `base_exists/base_sha256`, `proposed_sha256`, `preview_digest`, `version`, `status`, `created_by/created_at/expires_at`, along with completed `applied_sha256/applied_at` and `undo_of?`. All targets, baselines, and sources are computed/validated by the server; content passed in by the client is read only from saved revisions and replacing body content at apply time is rejected.

Candidate endpoints:

```text
POST /sessions/{id}/changesets
  { artifact_id, revision_id, target_path, operation }
  → 201 ChangeSet; only reads target, records snapshot and preview, does not write target
GET /changesets/{id}
GET /changesets/{id}/diff
POST /changesets/{id}/apply
  { expected_version, preview_digest } + Idempotency-Key
  → 202 Operation accepted; may await approval, cannot immediately display as applied
POST /changesets/{id}/undo-preview
  { expected_version }
  → 201 New undo preview ChangeSet; created only if the current target still matches applied_sha256
POST /changesets/{undo_id}/apply
  → The same execution, approval, and baseline validation pipeline, with no bypass paths provided
```

Undoing file creation is a deletion side effect, and its risk should be escalated to destructive; if controlled deletion is not implemented in the first version, explicitly open undo only for replace, and provide manual handling guidance for file retention on create operations. Ordinary write risk must not be used to mask deletion.

Candidate states: `prepared → awaiting_approval → applying → applied`; rejection/timeout is `denied/expired`; baseline change is `conflict`; indeterminable write or accounting is `recovery_required`. After prepared expires, preview must be redone. Each side effect is an independent ChangeSet; undo does not tamper an applied record back to prepared.

preview_digest binds Agent, session, operator, genuine workspace root identity, target relative path, baseline digest, revision ID, proposed digest, and expiration time; it is a content consistency digest, not an access token, nor can it replace authentication or approval. After approval passes, re-verify all bindings; "preview A, apply B" is disallowed.

### 7.1 Approval Conditions That Cannot Be Directly Reused

The existing Executor locates execution context by RunID, and run_id in the approval table is a non-null foreign key. Therefore, "directly calling Workspace.WriteFile upon user click" and "fabricating an empty RunID to call permission.Check" are both unacceptable.

This phase uses option one to avoid expanding model Run semantics. The original alternatives are retained below:

1. Add a formal local file operation execution subject and its approval association to the kernel; Executor supports this subject, permission continues using the same guard/deny/rule/once-approval logic, and approval persistence can reference genuine operation records; legacy run approval lists and legacy SSE semantics remain unchanged. Shared fields for newly added Operation/Approval or independent resources and schema migrations must be clarified.
2. Formally extend Run into a run capable of expressing non-model user operations instead of spoofing inference runs; RunKind, context resolution, messages/usage/statistics, cancellation, restart recovery, and legacy client behavior must be clarified. Only then can execution via the existing Executor path be allowed. This option may alter more existing semantics and should not be introduced incidentally in P3A.

Whichever is chosen, apply and undo must enter `tools.Executor`, executing under authenticated actual user identity using the same workspace boundary and permission engine; not establishing an independent confirmation system that only trusts booleans. Deny rules in existing rules and kernel protected paths take precedence. Session-level or permanent passes cannot expand this previewed target or cause arbitrary versions to be automatically replaced.

The new interface requiring preliminary review here can be expressed as `ExecuteUserOperation(ctx, operationID, call)`, but operationID must come from the aforementioned formal persistent subject; before completing models, approval storage, and parsers, this signature cannot be added to the code as a runnable bypass.

### 7.2 Baseline, Atomic Writes, and Recovery Responsibilities

The source, baseline, and state transitions of ChangeSet are managed by workbench; actual side effects invoke the conditional write capabilities of workspace via controlled tools. Candidate workspace capabilities are:

```text
Snapshot(ctx, rel, maxBytes) -> exists, sha256, fileIdentity, content
ReplaceIfUnchanged(ctx, rel, expectedSnapshot, proposedContent) -> newSnapshot
RemoveIfUnchanged(ctx, rel, expectedSnapshot) -> removedSnapshot  // Only after delete undo is approved
```

The existing WriteFile cannot replace ReplaceIfUnchanged. Target path writes by the daemon must be serialized centrally in the workspace adapter, re-resolving root and symlink boundaries, verifying current content digests, writing temp files, fsyncing, atomically replacing, and validating results. All relevant tool paths must also comply with the same coordination mechanism. File locks or SQL transactions must not be held while waiting for user approval.

Same-process locks cannot prevent concurrent writes by external editors or external processes. The implementation review must clarify platform-level guarantees and limitations for the race condition "after check, before replace"; "hash + rename" must not be advertised as an atomic CAS against arbitrary external writers. Unresolved paths/platforms can only degrade to saving as copies, not silent overwrites. Symlink replacement, case aliasing, hard links, external edits, and writes by running tools must all be included in testing and support scopes.

Database transactions cannot atomically overwrite the filesystem. Operation intent and the original file pre-image must be persisted before execution, and the applied state and audit must be recorded after successful execution; original image snapshots reside in a kernel-protected directory, not in an ordinary workspace writable by the Agent. Crash recovery is evaluated against the target's actual hash: if still base, it is not applied; if equal to proposed, backfill successful accounting; if neither, recovery_required, forbidding automatic overwrite or automatic undo.

Undo must take the applied hash as the new baseline before preview and approval; if other edits are detected, return conflict and retain the current file and pre-image for user comparison. Audit logs record only resource IDs, relative targets, digests, risk, operator, result, and reason, without storing full bodies or tokens. Successful application must be displayed after confirming a re-readable final state, not solely based on HTTP 202.

## 8. Compatibility, Migration, and Backup

Preserve HashRouter, `/chat/:sessionId`, and legacy queries; new project entry points serve only as supplementary navigation. Preserve sessionId, message seq, runId, Key, and Origin; do not rebuild any sessions due to UI renames. P3A introduces no new IPC; packaged editions continue injecting tokens via app:// same-origin proxy, and development editions pass connection information in memory only. New content endpoints are likewise subject to Host/Origin/Bearer validation.

A verifiable backup process must be in place prior to migration execution, rather than merely writing "rollback supported" in release notes:

1. Detect whether there are pending schema migrations after acquiring the daemon single-instance lock and before receiving business writes. Existing Store.Open automatically migrates, so the backup check must be scheduled before that migration; backups cannot be performed after Open completes.
2. Use SQLite consistency backup mechanisms on the current database, or ensure all connections are closed and WAL processing is complete before performing an offline backup of the complete database state; do not copy only active `.db` files while missing the WAL. Backups are completed by store/update capabilities; the HTTP layer does not open the database.
3. Backup directories 0700, files 0600, recording schema version, application version, backup timestamp, file manifest, and checksums, excluding plaintext access tokens. P3A text resides in DB, existing inbound files remain in place; configuration files requiring backup include only those necessary for recovery.
4. Verify that the backup can be opened along with `integrity_check`; reject migration on failure. Each SQL version continues using existing transactions; failures must not start new capabilities.
5. Rehearse recovery using the backup and a matching legacy kernel. The existing behavior where a legacy kernel rejects a new schema after migration completes must be preserved. Legacy code being able to ignore fields does not mean a legacy kernel can open a new-version database.
6. Rollback is not automatically wiping post-upgrade new work: stop writes first, retain a copy of current data and explain the recovery point, then restore the verified matching snapshot. P3B also requires backing up operation records/pre-images and reconciling file states beforehand; modified files cannot be undone by rolling back the DB alone.

P3A only adds new tables and nullable fields, without scanning files for re-attribution, copying all existing messages into versions, or executing historical tool calls. Deleting legacy UI preferences or rolling back frontends should not affect kernel drafts and artifacts. When a new client connects to an older daemon, it degrades via capabilities; capability call failures retain current edits and provide export, without clearing drafts.

## 9. Implementation Sequence and Acceptance

Review splitting is recommended in the following order:

1. Shared types/API/architecture review, plus recoverable backup prerequisite capabilities; no UI behavior changes.
2. Project and material indexing, session association, strict attribution validation; enable projects/materials after legacy session/upload regressions pass.
3. Draft CAS, restart recovery, cross-window conflicts, failure retention; enable drafts after confirming no new edits are cleared by legacy send responses.
4. Artifact and immutable versions, provenance verification, reopen, read/download, export safety; enable artifacts after completing the shortest user journey.
5. Separately review P3B execution subject and approval storage scheme, file conditional write implementations, and race condition boundaries; implement ChangeSet only after passing. Without this decision, retain copy saving and do not mount dummy apply/undo entry points.

Mandatory P3A acceptance coverage: legacy database upgrades and backup recovery, legacy client basic CRUD, new clients connecting to older daemons, cross-Agent project association rejection, invalid material whole-transmission failure, material change detection, same-scope concurrent draft creation, only one success for same-version updates from two windows, retention of new edits during transmission, offline recovery without auto-resend, unauthorized artifact source message rejection, partial output flagging, concurrent new version conflicts, download byte/digest consistency, HTML not executing in same-origin, association cleanup on session deletion, SSE gap/restart refetching, not placing full text/tokens into events and logs.

P3B additionally requires rejecting out-of-bounds/symlinks/aliases/protected paths, rule deny precedence, approval target and version binding, duplicate request idempotency, apply/undo external modification conflicts, recovery across crash cut-points, cancellation and timeouts, failure auditing, and cross-platform file semantics verification. Without such evidence, safe completion of coverage and undo cannot be claimed.

On 2026-10-12, the user again explicitly authorized P3B file preview, apply, undo, and compatibility verification; independent Operation, approval associations, and shared contracts all fall within this implementation scope. Capabilities are declared only after completing service assembly and recovery.
