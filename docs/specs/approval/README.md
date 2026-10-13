# Module Plan: Approval

| Item | Content |
|---|---|
| Status | Draft |
| Date | 2026-10-12 |
| Module | Approval. UI title is "Approval Required" |

This document plans the decisions humans see before operations execute. It does not create a new set of long-term authorizations.

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

For every operation requiring human decision, ensure the human clearly sees before execution: which task it belongs to, what is to be done, and to which target. The decision covers only this single instance, unless the human explicitly selects allow for this session.

## 2. Responsibility Boundaries

| This Module Is Responsible For | This Module Is Not Responsible For |
|---|---|
| Displaying risks and targets | Rewriting the assistant's confirmation tier |
| Accepting approve once, allow for this session, and reject | Approving inside messaging software |
| Providing only "Approve current preview of this file only" for file modifications | Covering all future files with a single approval |
| Not executing when rules evaluate to reject | Explaining why the model proposed this operation |

Risks are labeled in text: read-only, write, execute, network, destructive. Color is not used alone.

The workbench provides no entry points for fetching web pages or sending externally automatically. If network- or send-type approvals still appear during runtime, this module displays the task, action, and target as usual for a human decision. Confirmations that have already appeared are not hidden just because the local edition removed those entry points.

## 3. Key Flows

1. The operation halts at pending approval. The target has not yet been modified according to this request.
2. The human reviews the task name, action, target, and risk.
3. Approve once: executes only this instance. The assistant's long-term tier remains unchanged.
4. Allow for this session: exists only for approvals during task runtime, not used for file modifications.
5. Reject: this instance is not executed.
6. The approval copy for file modifications is "Approve current preview of this file only". When expired or the target has changed, this approval cannot be applied to another piece of content.

When multiple approvals are pending, display them one by one and state how many remain. When two tasks are waiting concurrently, each remains independent. Handling one does not overwrite the other's wait state, nor does it record the decision against whichever task is currently open.

## 4. Inputs and Outputs

| Input | Output |
|---|---|
| Associated task, action, target, risk | A single decision: approve once, allow for this session, reject |
| Current preview of file modification | Approve this version, or reject. Target remains unchanged before approval |
| Task name is still loading or failed to load | Do not display as unnamed task. Approve is unavailable, reject remains available |

## 5. Dependencies

| Counterpart | Relationship |
|---|---|
| Assistant | Provides current tier. Which operations require asking humans is determined by tier and rejection rules |
| Personal Workbench | Provides task name, and displays run or file status after decision |
| File Modification | Only when status changes to "Applied" does it indicate the preview has been written to the target |

Waiting for approval consumes an execution slot. This module does not release scheduled task plans; it only terminates this single wait state.

## 6. Potential Exceptions

| Exception | Facts Visible to the Human |
|---|---|
| Task name not yet read or failed to load | Shows confirming or failed to confirm. Approve is unavailable, reject remains available |
| Target file changed before approval | File module displays "File has changed", external content is retained |
| Preview expired | Requires regeneration of preview. Old decisions cannot be applied |
| Rule rejected | Operation does not execute |
| Human leaves without handling | Operation remains waiting or is treated as expired; not treated as approved |
| Two tasks waiting simultaneously | Retained independently. Whichever is currently open does not alter decision attribution |
| Tier name inconsistent with confirmation dialog | Governed by whether a confirmation dialog exists and whether file status changes to "Applied" |

## 7. Verification

| Verification | Seen When Passing |
|---|---|
| Normal approval | Task, action, target are readable. Operation does not execute after rejection |
| Approve once | Assistant tier remains unchanged |
| File modification | Button applies to this current preview only. File remains unchanged upon rejection |
| Allow for this session | Does not appear on file modifications |
| Multiple consecutive items | Remaining count shown, handled item by item |
| Two tasks waiting simultaneously, only one handled | The other remains waiting; decision is not recorded on the currently open task |

## 8. Assumptions and Pending Confirmations

| ID | Content |
|---|---|
| Q-1 | Whether human-initiated writebacks always prompt under all three tiers. Current edition is governed by whether a confirmation dialog is present at the time and whether status is "Applied". Tightening to "always prompt" is not yet a rule in the current edition |
