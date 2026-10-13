# Module Plan: Approval

| Item | Content |
|---|---|
| Status | Draft |
| Date | 2026-10-12 |
| Module | Approval. UI title is "Approval Required" |

This draft describes how users review operations before execution. It does not add long-term permissions or override the current permission engine.

---

## Outline

1. Objectives
2. Responsibility Boundaries
3. Key Flows
4. Inputs and Outputs
5. Dependencies
6. Potential Exceptions
7. Verification
8. Assumptions and Pending Confirmations

---

## 1. Objectives

Before an operation runs, show the task it belongs to, the proposed action, and its target. Approve once covers only that operation. A session-scoped approval covers later matching operations only when the user explicitly chooses it; this option is unavailable for user-requested file changes.

## 2. Responsibility Boundaries

| This Module Is Responsible For | This Module Is Not Responsible For |
|---|---|
| Displaying risks and targets | Changing the assistant's approval mode |
| Accepting one-time approval, session approval, or denial | Approving inside messaging software |
| Providing only "Approve current preview of this file only" for file modifications | Covering all future files with a single approval |
| Not executing when rules evaluate to reject | Explaining why the model proposed this operation |

Risks are labeled in text: read-only, write, execute, network, destructive. Color is not used alone.

The proposed local-edition navigation excludes web-retrieval and messaging shortcuts. Existing tools can still request network or messaging approval. Show their task, action, and target as usual; a draft navigation plan must never hide a pending decision.

## 3. Key Flows

1. An operation that requires approval waits before making the proposed change.
2. The user reviews the task name, action, target, and risk.
3. Approve once permits only this operation. It does not change the assistant's approval mode.
4. Allow for this session applies to matching tool operations during task execution. It is never offered for file changes requested from Results.
5. Deny prevents this operation from executing.
6. The approval copy for file modifications is "Approve current preview of this file only". If the preview expires or the target changes, the approval cannot authorize different content.

When multiple approvals are pending, display them one by one and state how many remain. When two tasks are waiting concurrently, each remains independent. Deciding one leaves the other pending and records the decision against the requesting task, even if a different task is open.

## 4. Inputs and Outputs

| Input | Output |
|---|---|
| Associated task, action, target, risk | A decision: one-time approval, session approval where available, or denial |
| Current preview of file modification | Approve this version, or reject. Target remains unchanged before approval |
| Task name is still loading or failed to load | Show that task details are unavailable. Disable approval but keep denial available |

## 5. Dependencies

| Counterpart | Relationship |
|---|---|
| Assistant | Provides the approval mode. Permission rules and the operation context determine whether approval is required |
| Personal Workbench | Provides task name, and displays run or file status after decision |
| File Modification | Only when status changes to "Applied" does it indicate the preview has been written to the target |

A running task waiting for tool approval keeps its execution slot. A file-change request is an independent operation, not an inference run. Resolving an approval does not delete or reschedule a job.

## 6. Potential Exceptions

| Exception | Facts Visible to the Human |
|---|---|
| Task name not yet read or failed to load | Show loading or unavailable; disable approval but keep denial available |
| Target file changed before approval | File module displays "File has changed", external content is retained |
| Preview expired | Requires regeneration of preview. Old decisions cannot be applied |
| Rule rejected | Operation does not execute |
| User leaves without deciding | Operation remains waiting or is treated as expired; not treated as approved |
| Two tasks waiting simultaneously | Retained independently. The open task does not change which task owns a decision |
| Approval-mode summary differs from an operation prompt | Follow the operation-specific decision; a mode label is not proof that a file was written |

## 7. Verification

| Verification | Seen When Passing |
|---|---|
| Normal approval | Task, action, target are readable. Operation does not execute after rejection |
| Approve once | Assistant approval mode remains unchanged |
| File modification | Button applies to this current preview only. File remains unchanged upon rejection |
| Allow for this session | Does not appear on file modifications |
| Multiple consecutive items | Remaining count shown, handled item by item |
| Two tasks waiting simultaneously, only one handled | The other remains waiting; decision is not recorded on the currently open task |

## 8. Assumptions and Pending Confirmations

| ID | Content |
|---|---|
| Q-1 | Resolved in the current implementation: apply and undo requested from Results always require one-time approval for the current preview, even under Auto inside sandbox or an allow rule. Protective guards and deny rules can block the operation before approval |
