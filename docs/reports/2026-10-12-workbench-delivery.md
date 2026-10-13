# Workbench Refactoring Implementation Delivery Record

This document records the delivery point of P3A; the user has subsequently authorized and advanced P3B. For the latest status, see [P3B and Style Unification Delivery](2026-10-12-p3b-delivery.md).

Date: 2026-10-12. Status: Local implementation and verification completed, uncommitted, unpushed, and no installation package published. The baseline is `b8bbd0d`, preserving the original two uncommitted team-workflow document modifications in the repository.

In this session, the user explicitly authorized workbench P3A shared model, repository interface, OpenAPI, and architecture document changes. File overwriting, atomic application, and disk undo are deferred to P3B, with no uncompleted interfaces or placeholder buttons enabled.

## Completed End-to-End Workflows

1. The start page and task page share the input component. On the home page, select files first, then complete creation, uploading, and sending in a single step; empty tasks can still be created directly. Input method editor (IME) and Enter/Shift+Enter behaviors remain consistent.
2. Persistent recent tasks, pinning, search, renaming, archiving, and restoration retain the old routes. Context menus and keyboard controls are operable; configuration entry points remain in collapsible groups.
3. A project permanently belongs to an assistant, organizing materials, tasks, and deliverables. Material uploads do not auto-select all items; current selections are explicitly included in the message. Legacy tasks can be associated with projects in place, keeping IDs, Keys, and message histories unchanged.
4. The input area centrally displays the assistant/model, working directory, assistant approval/autonomous mode, material references, and current-turn token usage. When usage data is unavailable, it shows "not reported" rather than fabricating costs.
5. Run display is based on real Run/SSE, tool calls, and results. The initial goal and recent supplements are inspectable; during state restoration reading, running or completion is not falsely claimed. Approvals also indicate their associated task.
6. A formal deliverable is created only when the user explicitly chooses "Save draft and edit". Full documents and standalone code snippets are supported; syntax previewing, editing, diffing, exporting, and saving copies are retained.
7. Formal deliverable versions are immutable, and saving edits appends a new version. Previewing history does not modify the current draft; restoring using historical content also generates a new version. Concurrent version conflicts require explicit choice and are not silently overwritten.
8. Input and deliverable drafts are persisted by the daemon, isolated by subject and scope. Auto-save indicates completion only after successful service confirmation; conflicts preserve both local and remote content. Sending completion cleans up only the draft version at the time of sending.
9. The task initiation phase saves checkpoints for created sessions and uploaded materials; resuming after failure avoids duplicate creation. When creation/sending response status is unknown, history is checked first instead of automatically retransmitting. Late callbacks do not hijack the user's current page.
10. Desktop window size, maximized state, deliverable pane width, theme, pinned tasks, and last opened location are restorable. The client stores only UI preferences and resource IDs; content body, attachment data, and credentials are not written to browser persistent storage. Upon exit, un-saved confirmations are completed before stopping the daemon.

## Data and Protocols

- Added `Project`, `ProjectMaterial`, `Draft`, `Artifact`, and `ArtifactRevision`, retained original `model.Store`, and separately set `WorkbenchStore`.
- The schema upgraded from 1 to 2. Prior to upgrading legacy databases, an SQLite consistent snapshot, integrity/version verification, and digest logging are executed; file permissions are 0600 and directory permissions are 0700. Migration is rejected if backup fails, and new databases do not create empty backups.
- Project associations and draft updates use expected value/version conditions; deliverable appends use `expected_latest_revision_id`. Draft baselines can be explicitly updated via CAS within the same deliverable.
- Materials undergo checks for Agent/project ownership, existence, size, and SHA-256 before sending; failure results in overall rejection without silently using altered files.
- Creating new deliverables validates the authentic assistant message source; creation idempotency keys are processed based on subject and endpoint scope.
- Added 22 workbench HTTP operations; content reading and downloading are independently authenticated, and metadata excludes the body. Plain text preview, attachment downloads, filename sanitization, and nosniff/CSP boundaries remain effective.
- `/version` declares completed capabilities; when a legacy daemon lacks capability fields, existing session and attachment copy flows are preserved without probing or calling new write endpoints.
- New events transmit only metadata such as ID/version; original SSE Seq, gap, hello, and reconnection handling remain unchanged. Workbench resources invalidate corresponding queries upon receiving events.

## Key Public Interfaces

```go
workbench.New(store model.WorkbenchStore, files workbench.Files, bus *event.Bus) (*workbench.Service, error)
```

For complete shared resource/repository signatures, see `internal/model/workbench.go` and `internal/model/repository.go`. For new HTTP paths and input constraints, see `api/openapi/openapi.yaml`: project/materials, session-project associations, drafts, deliverables and versions, version body, and downloading. No worker RPC, go.mod, Makefile, or AGENTS.md modifications.

```ts
workbenchApi(client: ApiClient): WorkbenchApi
usePersistentDraft(scope: DraftScope | null, content: DraftContent, onRestore: (content: DraftContent) => void)
TaskStarter.start(input: StartTaskInput, onStage: (stage: StartStage) => void): Promise<StartTaskResult>
SavedArtifactEditor({ artifactId, sessionId, visible, onClose, onDirtyChange })
```

`Composer.onSend` is backward compatible with the original text/attachments parameters, where the third optional parameter is the material ID. StartPage's fourth optional parameter carries restored uploaded references, excluding local File binaries. ApiClient adds an optional text/blob response type, keeping default JSON behavior unchanged.

## Executed Verifications

Commands were all executed in the repository root directory, setting the repository-specified PATH first:

```bash
make lint
make test
make e2e
make build
npm run typecheck
npm test
npm run build
go test -race ./internal/store
git diff --check -- apps internal api docs/architecture.md docs/design/workbench-contract-proposal.md docs/reports docs/research
```

- Go formatting, vet, and full repository tests passed; store race passed.
- Python ruff, formatting, and mypy passed; 176 tests passed.
- Web and desktop TypeScript type checks passed.
- Final frontend tests: 231 desktop items, 275 Web items, total 506 items passed.
- Go binary and Web/Electron builds passed. Existing locked dependencies were reused, with no new dependencies added or go mod tidy executed.
- `make e2e` passed fully without skips, using real Python worker, local mock model, approval, and tool calls.
- OpenAPI verified using the existing parser: 44 paths, 48 schemas, all references resolvable.
- Repository-wide diff check still contains whitespace issues in original team-workflow documents; scope of files relevant to this work passed without modifying those documents in passing.

### Actual Process and Browser Verification

- Executed dual-process verification: closed daemon 1 under the same data directory, then started daemon 2 with a different PID and token; identical draft body, version, and owner were restored, old token returned 401, and expired version write returned 409.
- Actual HTTP concurrency of 16 uploads with the same name yielded 16 independent paths with correct content in each; message submission returned 409 after material content changed; owner injection and missing/null/oversized body were rejected, leaving the original draft intact.
- In-browser actions all succeeded: creating projects, uploading materials, explicitly checking boxes, starting tasks, approving reads, generating initial draft, saving version 1, editing and auto-saving, refreshing to restore, appending version 2, reading version 1, as well as editing again post-restart and saving version 3.
- Stopped the entire test daemon again, launched a new process, and entered from the application root entry point; automatically returned to original task/deliverable, fully restoring uncompleted edits marked with `P3-20261012`.
- Checked 1440×900 side-by-side and 900×760 tab layouts; save buttons remained inside visible windows, and deliverables were retained when switching back to conversation. The project deliverable list displayed the latest version 3. Exported files and copy contents in the daemon were compared byte-for-byte and matched.
- Final acceptance revealed the temporary proxy did not forward PUT; after fixing the acceptance environment, "Retry Save" recovered state without modifying product interfaces; window edits were preserved during failure.

The complete tasks above used a local mock model and cannot serve as conclusions on real model quality. A minimal text request for `auto:fast` via the user-provided gateway independently returned HTTP 200; no claim is made that the complete tool loop or all protocols on the real gateway have been verified. Keys were not written to the repository, tests, config files, or logs.

## Design and Research Deliverables

- [Phase-three contract proposal](../design/workbench-contract-proposal.md): preserves the authorized scope and deferred P3B design.

## Explicitly Retained Boundaries

- The initial version of lists for new projects, materials, in-project tasks, and deliverables displays up to 200 items, with full pagination interactions not yet added; original historical task pagination is retained.
- Local Files that have not been started or uploaded cannot be restored across restarts; UI instructions require re-selection. Content text and uploaded references can be restored.
- A project is not an isolated file sandbox: projects under the same Agent share its workspace, and selecting materials does not revoke access to other authorized files.
- Text/code deliverables are supported, excluding Office format conversion platforms. Saving versions, saving copies, exporting, and modifying original files are distinct actions.
- Original file overwriting, atomic application, and disk undo remain unimplemented, with corresponding capabilities not enabled.
- No cross-process exactly-once sending guarantee; unknown results must be verified rather than automatically retransmitted.
- No formal account quota, cost accounting, or spending limit dashboard; currently displays token usage actually provided by the service.
- Electron native exit confirmation, multi-monitor hot-plugging, and Windows/Linux installation packages have not been verified on actual physical devices.
- Auto-save still has a time window prior to write confirmation; upon failure, in-window edits are preserved with prompts, but saving the very last character before power loss cannot be guaranteed.
