# P3B and UI Unification Delivery

Historical validation record for 2026-10-12, written before publication. The counts and limitations below describe the P3B checkpoint. Later scheduling changes are documented in [resources and navigation](2026-10-12-resources-and-navigation.md); current release priorities are in the [roadmap](../../ROADMAP.md).

## Completed Workflows

- Saved artifact → single-file diff preview → original permission engine evaluation → single approval → conditional application → independent undo preview. Undoing creation is handled as a destructive deletion risk.
- Manual changes use independent persistent `Operation` records rather than synthetic model `Run` records; the model tool list does not expose manual file operations. Rejection rules take precedence, and legacy Run approvals are retained.
- Originals, proposals, and actual execution results are persisted by the daemon. File results are recorded first, followed by cleaning up retained originals, and finally confirming the terminal state; failure to confirm at any step enters recovery_required.
- External edits, directory identity, file inodes, permissions, and digest changes are all verified. Unknown originals are not deleted, and success cannot be determined solely based on identical content.
- Frontend retries use fixed idempotency keys; unknown responses re-read the state first without automatically re-applying. Expired undos can regenerate previews from the original operation.
- Legacy tasks, messages, drafts, artifact versions, and keyboard shortcuts remain functional; services without changesets capability retain legacy save/export paths.

## Visuals and Interaction

The interface unifies grayscale hierarchy, fine dividers, rounded corners, and control spacing; primary actions retain brand blue. Settings uses a single two-column layout, switching to top tabs on narrow windows, with support for search and keyboard navigation.

Long explanations are collapsed by default, and actual execution steps appear in a compact progress view. Messages and artifacts support safe chart JSON bar/line charts with data tables and source code; up to 50 finite numeric values, invalid content retains original code without executing scripts or external resources. Default output suggestions are more concise, without overriding detailed content explicitly requested by the user.

## Public Interfaces

```go
workbench.NewChanges(model.ChangeStore, workbench.ChangeFiles, *event.Bus) (*workbench.ChangeService, error)
tools.Executor.ExecuteUserOperation(context.Context, string, model.ToolCall) (model.ToolResult, error)
workspace.Workspace.Snapshot(context.Context, string, int64) (workspace.Snapshot, error)
workspace.Workspace.Apply(context.Context, string, string, workspace.Snapshot, *string) (workspace.Receipt, error)
workspace.Workspace.Recover(context.Context, string, string, workspace.Snapshot, *string) (workspace.Receipt, error)
workspace.Workspace.Finalize(context.Context, string, string) error
```

HTTP adds changesets prepare, list, read, diff, apply, undo preview, cancel, and recovery verification at `/api/v1`. `apply` returning 202 indicates acceptance only. See OpenAPI for the full schema; at this checkpoint, 51 paths, 54 schemas, and 228 references are all resolvable.

The database is upgraded to schema 4: 003 adds Operation and compatible approval associations, while 004 saves ChangeSet and independent idempotency records. Consistent pre-upgrade snapshots are reused; terminal state operations are retained, and entities pending approval/execution/recovery prevent deletion of their associated tasks or assistants.

## Practical Verification

First use the repository-specified Go/Node PATH (with `$HOME/.local/bin` additionally added for e2e) and execute:

```bash
make lint
make test
make e2e
make build
npm test
npm run build
go test -race ./internal/tools ./internal/permission ./internal/store
go test -race ./internal/workspace
go test -race ./internal/server -run 'Test文件|Test外部|Test拒绝|Test应用|Test两个|Test清理|Test保留|Test启动恢复|Test显式|Test活跃|Test执行|Test旧工作' -count=1
```

- Go format/vet/repo-wide tests and Python ruff/format/mypy passed; 176 Python tests.
- Final counts: Web 313, Desktop 231, for a total of 544 passing tests; Web/Electron and Go builds passed.
- Real Python worker + mock model e2e passed without skipping; paid models were not re-invoked this time.
- Real file tests covered creation/replacement/undo, external rewrites, rule rejections, cancellations, expirations, unknown responses, single approvals, dual concurrent recoveries, original cleanup failures, final commit failures, state recovery after database reopening, and legacy capability fallback.
- macOS practical verification confirmed 0644/0755 permission retention and rejection of ACLs/special permissions/unsupported attributes; cross-compilation for workspace Linux/Windows passed.
- Browser tests completed preview, approval, creation, and independent deletion undo, confirming file disappearance; approving application after external rewrites yielded conflicts with external bytes remaining unchanged. Charts, settings search, real theme switching, and 900x760 layout without horizontal overflow were all checked.
- Full frontend checks previously encountered fixed old timestamp test data due to newly added expiration protection; updated to test preview within validity period, after which all passed.

## Boundaries

Supports single-file, plain UTF-8 text up to 512 KiB. Linux/macOS use descriptor-based path resolution and installation that refuses to overwrite an occupied target; Windows does not currently support in-place modification, retaining export and copy saving. At this checkpoint, Linux conditional writes and native installers had not been manually validated on real devices. Later cross-platform CI is separate from installer acceptance.

Atomic CAS is not guaranteed for arbitrary external writers under the same UID. Uncertain scenarios such as continuous writes via legacy file descriptors or external directory moves require verification and retention of the original file. Recovery verification will restore retained originals to vacant targets when necessary, without overwriting new external files or automatically redoing proposals.

Local previews use isolated test data and mock models, which do not represent real model task quality. Keys were not written to repository, events, tests, or logs. Existing modifications to team-workflow documentation are preserved.
