# Module plan: Personal workbench

| Item | Description |
|---|---|
| Status | Draft, aligned with personal workbench PRD v0.5 |
| Date | 2026-10-12 |
| Module | Personal workbench |
| Full requirements | [PRD](../../prd/personal-workbench.md) |
| User guide | [Guide](../../user/personal-workbench.md) |

---

## Outline

1. Goal
2. Responsibilities
3. Key workflow
4. Inputs and outputs
5. Dependencies
6. Potential failures
7. Related specifications
8. Assumptions and open questions

See [flows](flows.md), [issues](issues.md), and [troubleshooting/verification](troubleshooting.md).

---

## 1. Goal

Make every step from input to result visible. When work stops, explain where, what was retained, what did not happen, and one next action.

| ID | Goal |
|---|---|
| G1 | One confirmation creates the task, adds selected materials, and submits requirements; a failure names its stage and retains completed work |
| G2 | Reopening separately exposes task, unsent draft, result edits, latest version, and run state; unknown outcomes are not resent automatically |
| G3 | Save version, save copy, export, and write-back are four different actions |
| G4 | Inspect differences before write-back; stop and retain external edits if the file changed after preview |
| G5 | Display returned token categories; otherwise not provided, without estimated charges |

## 2. Responsibilities

| In scope | Out of scope |
|---|---|
| Start page, tasks, project materials/tasks/results | Endpoint, credential, and connectivity validation |
| Show assistant, model, workspace, selected materials, approval mode, and current usage | Effects of assistant changes on other tasks |
| Separate creation, material addition, and submission | What the assistant actually read/executed |
| Confirmed draft saves and retaining both conflict sides | Multiuser editing |
| Result versions, copies, exports, and their distinction from write-back | Files outside the workspace |
| File-change preview/state | Converting one approval into a lasting rule |
| Enhance prompts without task messages | Enhancement quality |
| Preview failure cause/download | Editing in preview or recalculating spreadsheets |
| Understandable queue capacity | Why a scheduled job started at that time |
| Hide unsupported entries for older services | Install/upgrade the local service |

Deleting a task/assistant removes messages, drafts, and result versions while retaining workspace files.

This draft proposes no messaging, cloud synchronization, online conversion, sharing links, skill marketplace, or web-retrieval entry points. Those exclusions are planning scope, not a claim that existing connection or tool capabilities have been removed. Mobile only links to [the mobile app](../mobile-link/README.md).

## 3. Key workflow

1. **Prepare:** without a usable assistant, offer configuration without creating a task.
2. **Start:** create, add materials, submit; blank input creates only an empty task.
3. **Process:** show real run state; users may stop, draft a follow-up, or handle approval.
4. **Retain:** a formal result exists after Save draft and edit; subsequent saves create versions; copies/exports are separate.
5. **Write back:** only when available on the current system, preview, request, approve, and wait for Applied. Preview itself does not write. Guarded write-back is implemented for one text file on Linux/macOS. The recorded manual workflow was verified on macOS; broader platform acceptance remains separate. Windows retains copy/export.

Enhancement and preview are side paths. Their failures do not send the task or write the file.

## 4. Inputs and outputs

| Input | Required output |
|---|---|
| Goal, assistant, selected materials | Task or a specific creation/material/submission stop; text retained |
| Stop, approve once, allow this session, deny | Corresponding run state; denied operation not executed |
| Edit/save version/save copy/export | Only the selected result |
| File target and saved version | Preview and file state; previewing does not write, and Applied confirms success. Unknown outcomes require inspection before retry |
| Enhance prompt | Candidate or failure; task-message count unchanged |
| Open preview | Readable content or reason/download |

Before creation, the task name is Not created.

## 5. Dependencies

| Module | Sent | Returned | If unclear |
|---|---|---|---|
| Local persistence | Drafts, versions, file operations | Confirmed, conflict, failure | Never show Saved/Applied |
| [Model connection](../model-connection/README.md) | Current assistant connection | Connectivity, tokens or absent usage | Connectivity failure remains in preparation; missing usage not provided |
| [Assistant](../assistant/README.md) | Selection | Name, model, workspace, approval mode | Remain in preparation if missing |
| Execution | Requirement | Queued, processing, completed, incomplete, stopped, unknown | Inspect unknown outcome; do not resend |
| [Approval](../approval/README.md) | Owning task/action/target | Once/session/deny decision | Never decide with unclear task ownership |
| Workspace files | Materials, copy location, write target | Whether file still matches preview | Changed file retained, not overwritten |
| [Scheduled jobs](../schedule/README.md) | No body, shared slots only | Capacity occupied/free | Explain the source of waiting |
| [Mobile app](../mobile-link/README.md) | Open link | No returned data | Link failure does not alter tasks |
| Messaging | No handoff | No input | No navigation/settings entry |

Projects share their assistant's workspace. Material selection controls message references, not file permissions.

## 6. Potential failures

| Stage | Failure | Immediately visible fact |
|---|---|---|
| Connection | Service down/invalid token | No task exists yet |
| Preparation | No assistant/model test failed | Task not created |
| Start | Upload fails after creation | Exactly one task; text retained |
| Start | Submission outcome unknown | Unknown; no automatic duplicate requirement |
| Execution | Same task already running | Follow-up remains in input |
| Approval | Owning task unclear | Do not approve |
| Draft | Leave before confirmation | Only the last confirmed save is reliable |
| Draft | Conflicting copies | Both retained; user chooses |
| Result | Newer formal version exists | Current edits retained |
| File | Target changed after preview | External content retained |
| File | Interrupted write cannot be verified | Needs review; no automatic rewrite |
| Enhancement | Empty/incomplete result or no slot | Original retained; message count unchanged |
| Capability | Older service lacks feature | Hide entry; legacy sending/copy remains |
| Capacity | Other task/job occupies slots | Do not call this task processing |
| List | A list is capped at 200, or title search covers only loaded tasks | Explain the limit; task history can load more, while other lists need pagination |
| Start | Switch assistant | That assistant's own unsent draft |
| Start | Late response | Content stays with original task; current page retained |
| Material | Concurrent same-name files | Each retained with its own correct content |
| Result | Task finished | Result/preview still readable |
| Approval | Two tasks waiting | Decision belongs only to its requesting task |
| Execution | Start signal without body | Unknown; no automatic new run |

See [troubleshooting](troubleshooting.md) for wording/tests and [issues](issues.md) for incomplete presentation work.

## 7. Related specifications

States and four main flows are in [flows](flows.md).

The three start examples populate drafts without sending. Assistant, model, workspace, materials, and approval mode must be visible before submission. More specific examples, pre-start project selection, and footer verification wording remain separate proposals outside the decided PRD scope, not accepted features.

## 8. Assumptions and open questions

| ID | Detail |
|---|---|
| A1 | Individuals producing documents/file changes; no population/industry data |
| A2 | One computer/user; no multiuser editing |
| A7 | Mobile link is the sole external entry, without synchronization |
| A8 | User-supplied model endpoints communicate; this is not the app's cloud |
| Q-1 | Resolved in the current implementation: user-requested apply/undo always requires one-time approval for its preview, including Auto inside sandbox; protective deny rules still take precedence |
| Q-3 | Task history supports Load more. Project/material/result lists still stop at 200; pagination for those lists and full-text search remain future work |
| Q-5 | No official mobile URL; hide the link until supplied |
