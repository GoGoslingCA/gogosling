# Personal workbench: Troubleshooting and verification

Date: 2026-10-12. Other module rules remain intact. This document defines clear workbench handoffs only.

---

## Outline

1. Failure notices
2. Process records
3. Diagnostic path
4. Verification

---

## 1. Failure notices

Use four factual statements consistent with [flows/states](flows.md):

1. The stopping stage: connection, preparation, start, execution, approval, draft, result, file, enhancement, capability, or capacity.
2. What was retained.
3. What did not happen, such as another task, original-file write, or new message.
4. One next action.

If a module returns only Failure without facts, show that stage's outcome as Unknown, rather than guessing success or zero.

## 2. Process records

Record four inspectable screen values: time, stage, task name, and state. Use Completed/Failed/Unknown plus existing flow states. Notices for the same operation must agree on all four.

Exclude full requirements/results, credentials, tokens, and file contents. If no centralized record exists, extract visible values from current notices; missing values remain Not shown. This is the incomplete K2 item in [issues](issues.md).

## 3. Diagnostic path

Stop at the first matching row.

| Order | Observation | Stage | Next action |
|---|---|---|---|
| 1 | Connection failure/invalid token | Connection | Check local service; create no task |
| 2 | Assistant configuration requested/connectivity failure | Preparation | Inspect model test, then assistant enabled state |
| 3 | Creation/material/submission failure or unknown | Start | Inspect existing task; do not click Start again while unknown |
| 4 | Queued/processing/incomplete/stopped/unknown | Execution | Inspect state; reply quality outside scope |
| 5 | Approval prompt | Approval | Check task/action/target; deny mismatched ownership |
| 6 | Draft failure/conflict | Draft | Keep window open to retry or choose a side |
| 7 | Version conflict/copy failure | Result | Retain edits; no original-file write implied |
| 8 | File state not Applied | File | Refresh only when review/unknown; no new request |
| 9 | Enhancement alone failed | Enhancement | Check original retained/message count unchanged |
| 10 | Entry absent | Capability | Check old-service capability or platform write-back support |

Mobile-link failure retains desktop tasks, drafts, and files.

## 4. Verification

Use a practice model and synthetic files. Fix this module's notice/state when a case fails; do not alter neighboring rules to force a pass.

| Case | Action | Expected result |
|---|---|---|
| Connection | Open with invalid token | Connection stage, no new task |
| Preparation | Start page without assistant | Configuration entry only |
| Interrupted start | Fail material addition | One task, retained text, precise stage |
| Unknown result | Withhold submission outcome | Inspection notice, no automatic duplicate |
| Approval ownership | Trigger approval | Readable task/action/target; denial prevents operation |
| Draft | Observe unconfirmed save | No Saved claim; failed text retained |
| Result | Fail saving copy | Copy named; edits retained, original unchanged |
| File | Edit target after preview, then approve | File changed; external content retained |
| Unknown file outcome | Withhold final state | Unknown/checking; another click does not become another write |
| Enhancement | Fail/cancel optimization | Original retained, message count unchanged |
| Capacity | Limit 1 occupied by other work | Later task queued, distinguishable from processing |
| Capability | Connect to older service | New entries absent, legacy send/copy retained |
| Local boundary | Inspect start/tasks/projects/settings | No messaging/cloud synchronization/online conversion/sharing/web retrieval |
| Mobile | No official URL | No empty Mobile app button |
| Assistant switch | Draft under assistant A, switch to B | B's draft shown; A's input does not leak into B |
| Late result | Start, then open another task | Current page retained; earlier content remains in original task |
| Same-name materials | Add differing files concurrently | Both retained with correct independent content |
| Preview after ending | Open saved result after Stop | Readable without an active assistant requirement |
| Two approvals | Decide only one waiting task | Other remains waiting; decision not assigned to currently open task |
| Start without body | Accept run, withhold content | Unknown or still processing; no automatic rerun |
| Leave page | Hide accepted run to tray | Does not become Stopped |
| Persistent settings | Save limit 1, reopen | Still 1 |
| Failed body | Leave/reopen unconfirmed failed output | Retained or explicit recovery failure; never silently empty success |
