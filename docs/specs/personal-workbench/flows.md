# Personal workbench: Flows and states

Use the same states as [the module plan](README.md), without adding new ones.

---

## Outline

1. A run
2. Drafts
3. Result versions
4. File changes
5. Projects, tasks, and assistants
6. Execution capacity

---

## 1. A run

| State | Meaning | User action |
|---|---|---|
| Not started | Task exists, round not submitted | Write requirements and start |
| Queued | Accepted, not processing yet | Wait or stop |
| Processing | Work in progress | Inspect steps, draft follow-up, stop |
| Awaiting approval | Operation needs a decision | Use [approval](../approval/README.md) choices |
| Completed | Round ended with a result | Save draft or follow up |
| Incomplete | Round failed | Inspect failure, send requirements; no automatic rerun |
| Stopped | User stopped the round | Completed steps retained |
| Recovering | Reading previous state | Wait; neither Completed nor Generating |
| Unknown | Cannot determine state yet | Inspect; no automatic resend |

An unfinished run prevents another concurrent send in the same task. Keep follow-up text in the input.

A disconnection does not erase a recorded completion. Explain potentially incomplete intermediate events and use the full reply after completion. Retained messages prove records exist, not that the assistant resumed with all earlier context. Opening a task never resumes automatically.

Closing a page, switching tasks, or hiding to tray does not stop accepted work. User Stop or the run's own termination changes its state. A start event without reply content is not proof of failure or completion. Inspect the run state and never start a duplicate automatically.

Late start results belong to the original task, without redirecting the current page. Switching assistants restores each assistant's own unsent draft; the previous assistant's text stays under that assistant.

## 2. Drafts

Drafts include unsent input and result edits not yet saved as a formal version.

| Display | Meaning |
|---|---|
| Checking draft | Reading |
| Saving | Submitted, not confirmed |
| Saved | This revision confirmed |
| Save failed | Window text retained; closing/refreshing may lose unconfirmed edits |
| Draft updated elsewhere | Both sides retained; user chooses |
| Restored | Text/uploaded references recovered; inspect existing messages before sending |
| File pending upload | Unfinished local-file upload cannot continue after restart |
| Connection lacks autosave | Closing/refreshing cannot restore it |

Only confirmed saves show Saved. Successful sending clears only the submitted revision, preserving text typed afterward.

Confirmed, inactive real-time state may be released. Unconfirmed failed output survives leaving the page. Result versions, retained write-back originals, and activity records are not automatically deleted by age.

## 3. Result versions

- A formal result comes from a real reply and explicit Save draft and edit.
- Saved versions are immutable; saving again creates another version.
- Historical preview leaves the current edit intact.
- Restoring historical content creates a version, without deleting old ones.
- If a newer formal version exists, retain edits; load the latest or explicitly adopt its baseline before saving.
- Save version/copy/export never overwrites the original workspace file.
- Results/previews remain readable after a task ends or while no assistant runs.
- Concurrent same-name uploads retain both files with independent, correct contents.

## 4. File changes

| State | Target |
|---|---|
| Previewed, not written | Unchanged |
| Awaiting approval | Unchanged |
| Applying | Not declared complete |
| Applied | Matches the approved preview |
| Denied/cancelled/expired | Unchanged; expired previews require a new preview |
| File changed | No write; external content retained |
| Apply failed | No success claim |
| Needs review | No further write or automatic retry |
| Request outcome unknown/checking | Query state before any resubmission |

One preview targets one file and uses a saved version. Current write-back accepts UTF-8 text up to 512 KiB, with create/replace. Undoing creation deletes the file and requires a separate preview and confirmation. Do not begin the write while the workspace has an active run.

## 5. Projects, tasks, and assistants

| Action | Effect | Retained |
|---|---|---|
| Archive task | Sending disabled until restored | Messages/results |
| Archive project | No new tasks from it | Existing tasks continue |
| Delete task | Remove messages/drafts/result versions | Workspace files |
| Delete assistant | Remove tasks/projects/drafts/result versions | Workspace files |
| Remove material reference | Remove project listing | Original file/sent messages |

A task may be in one project or unassigned. Association changes neither identity nor permissions. A project uses its originally selected assistant.

## 6. Execution capacity

- Default 2 concurrent runs, configurable from 1 to 8.
- At most 32 queued; waiting does not invoke the model.
- Interactive requests have priority; long-waiting work gains priority to prevent starvation.
- Same-workspace tasks execute serially.
- Awaiting approval occupies a slot.
- Lowering capacity does not interrupt accepted running work.
