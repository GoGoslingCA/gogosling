# gogosling user guide: Personal workbench

For people completing documents, spreadsheets, or file changes on their own computer. No internal architecture knowledge is needed.

Product terms are assistant, task, material, project, result, version, copy, and file change. An agent in Settings is the same object as an assistant.

Sections 1–6 describe the local workbench flow recorded on 2026-10-12, before a signed installer release. Section 7 describes future design, not currently available buttons. Module-specific local-edition plans remain drafts; they do not remove existing capabilities by themselves.

---

## Outline

1. Before starting
2. Your first task in ten minutes
3. Interface areas
4. Core capabilities
5. Step-by-step operations
6. Questions and troubleshooting
7. Future capabilities

---

## 1. Before starting

You need a computer running gogosling; your own model endpoint/name and any required credential; a goal and optional reference files. A task can start without files.

Three distinct actions matter:

1. **Save version** keeps the result inside gogosling for reopening, without changing the original file.
2. **Save copy/export** creates another file, without overwriting the original.
3. **File change** targets one workspace file after preview. Until Applied, the target has not changed according to that preview.

Windows supports versions and copies/exports; guarded original-file write-back is unavailable. The instructions below are verified for macOS. Linux availability depends on the kernel capability and platform acceptance; use copy/export when unavailable.

If a connection screen appears, enter the local endpoint/token supplied with your setup instructions and connect. This is separate from desktop automatic attachment; the token stays in page memory only.

---

## 2. Your first task in ten minutes

Follow the sequence; use the wording actually present in your build if a label differs.

### Step one: Connect a model

1. Open Settings → Model connections.
2. Create a provider and enter name, type, endpoint, and credential if required.
3. Save the connection, then test connectivity using the saved provider. Saved credentials are not echoed.
4. If testing fails, resolve the connection before creating an assistant; see section 6.

### Step two: Prepare an assistant

1. In Settings → Agents, create an agent.
2. Enter a name and select the saved provider/model.
3. Select an approval mode: ask for everything before external changes; ask for risky command/network/message/deletion actions; or automatic operation within the allowed sandbox, with approval outside it.
4. Save. If uncertain, choose Ask for everything; changing the mode affects all tasks using that assistant.

### Step three: Start work

1. Return to the workbench and choose New task.
2. Write your goal. A start example fills the draft without sending.
3. Attach/drop reference files if needed; inspect the selected-material count.
4. Check assistant, model, and workspace near the input.
5. Start task. With no text/files, Create empty task lets you add requirements later.

### Step four: Handle approval

Inspect the owning task, action, and target. Approve once permits this operation. Use session approval only when explicitly allowing later matching operations in that session; deny to prevent the operation.

### Step five: Keep a result

1. After a substantive reply, choose Save draft and edit. Short clarifying replies do not automatically become formal results.
2. Edit in Results. Saved confirms that edit draft was persisted.
3. Save a new version when ready.
4. Reopen the task to inspect it later.

---

## 3. Interface areas

The sidebar provides New task, Find a task (loaded tasks only), Projects, Jobs, Recent tasks, and Settings. Recent tasks support continuation, pinning, renaming, and archiving. Settings groups appearance, models, assistants, skills/extensions, records, and resource controls. Draft local-edition plans specify their own networking exclusions; they are separate from existing connection capabilities.

The planned Mobile app entry opens a published URL in the system browser without login/synchronization or sending desktop files. It stays hidden until an official URL is supplied.

The center contains the goal/progress above and input below. Results open on the right. A narrow window uses Conversation/progress and Results tabs sharing one edit state. Enter sends; Shift+Enter inserts a new line.

---

## 4. Core capabilities

### 4.1 Tasks

A task is ongoing work that can receive follow-ups, stop, archive, and restore. States include not started, queued, processing, awaiting approval, completed, incomplete, and stopped. For Recovering or Unknown, wait or inspect history before resending.

An unfinished run prevents concurrent sending in the same task. Draft the next message while waiting.

### 4.2 Materials

Projects do not select every material automatically. Only checked files accompany the next message. Unchecking/removing a reference does not delete original files or alter sent messages. Selection neither expands workspace access nor removes already authorized access.

### 4.3 Projects

A project groups one assistant's materials, tasks, and results and retains its originally selected assistant. Archiving prevents new tasks from that project while existing tasks remain accessible; restore to start again.

### 4.4 Results, copies, and originals

| Action | Result |
|---|---|
| Save draft and edit | First formal result in gogosling |
| Save new version | Another immutable revision; earlier ones remain readable |
| Save copy | Another file in the workspace attachment directory |
| Export | Download/save to the selected location |
| Request file application | Change one workspace target according to its preview |

Reading history retains current edits. Restoring historical content creates a new saved version without deleting the old one.

### 4.5 Enhance prompt

With input text, Enhance prompt rewrites only the unsent text using the current assistant's model. It does not read attachments or send a task. The current interaction fills the input directly, offers Undo, and opens a feedback field only when you choose to refine. You can edit the draft yourself.

Enhancement uses provider resources, but its usage is separate from the current task round.

### 4.6 File preview

Open an attachment, material, or workspace file to read it; closing releases that preview. Readers cover text/code, static web content, modern Office, OpenDocument, PDF, images, supported audio/video, and archive member listings.

- Spreadsheet values/formula text are cached in the file, not recalculated.
- Complex layouts, animations, and macros may be omitted.
- Legacy Office needs a local converter; when unavailable, download rather than online conversion.
- Decrypt protected PDFs in the original software first.
- Long content may be truncated; download retains the complete original.

### 4.7 Usage

Inspect input, output, and cache-read tokens for the current round. Missing data is Not provided, not free usage. Provider bills determine charges; the app does not estimate money.

---

## 5. Step-by-step operations

### 5.1 Start from a project

1. Create a project with name, description, and assistant.
2. Open Materials and attach/drop files.
3. Select only this task's references; inspect the count.
4. Start task, enter the goal, and send.
5. Saved versions appear under project Results.

Remove unavailable material selections before starting; typed requirements remain.

### 5.2 Refine before sending

1. Write the original request and click Enhance prompt.
2. Review the text filled into the input.
3. Use Refine for a shorter response or a specific format; enter feedback and apply that refinement.
4. Inspect the final draft before Enter/Send.
5. Undo restores the original if needed. Cancellation/failure retains the original; editing, switching tasks, or sending prevents late results from overwriting newer work.

### 5.3 Edit and version a result

1. Open a saved result and edit it.
2. Wait for Saving → Saved, then Save new version.
3. Version history preserves your current edit.
4. Explicit restoration of old content saves a new revision.

A newer-version conflict retains edits. Load latest adopts that content; retain edits and continue explicitly uses the newest baseline to save another revision.

### 5.4 Write one text file on macOS

Use copy/export on unsupported platforms.

1. Save a formal version first.
2. Open File changes and enter a workspace-relative target, such as `notes/description.txt`, never an absolute computer path.
3. Select Create or Replace and generate a preview.
4. Read added/deleted lines; the file is still unchanged.
5. Request application and approve this preview when asked.
6. Wait for Applied before inspecting the target in your file browser.

To undo, preview undo on the applied record, inspect it, then separately confirm. Undoing creation deletes the file and is approved as deletion. A file changed afterward cannot be undone using the old preview.

### 5.5 Pin, archive, and find tasks

Use task actions under Recent tasks. Pin keeps a convenient sidebar entry; unpin does not delete. Archive prevents sending until restored. Search covers loaded tasks; load more before concluding a task is missing.

### 5.6 Adjust concurrent work

In Settings → Resources, select parallelism from 1 to 8 (default 2) and save. In-flight work finishes rather than being killed. Queue capacity is 32; waiting does not call the model. Immediate user work precedes jobs, same-workspace work is serial, and awaiting approval occupies a slot.

### 5.7 Leave and exit

Unsent text, uploads, or unfinished result saves trigger confirmation before leaving. Desktop window closure hides to tray while accepted work continues. Actual exit handles unsaved content before stopping the local service. Unuploaded local files require reselection; confirmed text and uploaded references do not.

---

## 6. Questions and troubleshooting

Act on the stopping stage once, inspect the result, and avoid repeated sending, saving, or write-back requests.

| Notice | Stage | First action |
|---|---|---|
| Connection failed/invalid token | Connection | Check endpoint/token; create no task |
| Configure assistant/connectivity failed | Preparation | Test connection, check enabled assistant |
| Creating/adding materials/submitting/outcome unknown | Start | Inspect the existing task for the message before another Start |
| Queued/processing/incomplete/stopped/unknown | Execution | Wait, stop, or inspect; never resend an unknown outcome automatically |
| Approval needed | Approval | Check task/action/target; deny if inappropriate |
| Saving/save failed/draft changed | Draft | Keep window open; retry or inspect both conflict sides |
| Version conflict/copy not saved | Result | Retain edits; load latest or save on its explicit baseline |
| Previewed/file changed/needs review/unknown application | File | Preview is not write-back; refresh before another request |
| Module not loaded/enhancing/busy | Enhancement | Original retained; retry after capacity frees |
| Unsupported connection | Capability | Keep legacy sending/copy; hidden entries are not faults |

Failure notices should identify stage, retained work, what did not happen, and one next action. Include task/stage/state/time when reporting rather than only Failed. The table takes priority over the examples below.

Expected behavior includes separate assistant drafts, late results staying with their original tasks, independent same-name files, readable results after Stop, independent approvals, continued tray execution, and connectivity failures not being labeled quota exhaustion without evidence.

### Start does nothing or requests assistant configuration

Check the saved/tested provider under Settings → Model connections, then the enabled assistant using it, and return to Start.

### Connectivity test fails

Verify the full provider endpoint and credential. Blank retains an existing credential, not a missing first credential. Save before testing; model discovery uses saved settings. If failure remains, inspect the provider's own account/service status. This guide cannot inspect its bill or outage.

### Unsure whether Start created the task

Do not click again. Inspect creation/material/submission stage and existing task/history for the requirement. Decide about retry only after checking duplicates.

### Session already has an unfinished run

Wait for Completed/Incomplete/Stopped or Stop that round. Stop does not delete the task.

### Draft save failed

Keep the window open, Retry, and leave after Saved. If another draft exists, inspect it before retaining yours or adopting the other.

### Selected files disappeared after reopening

Unfinished uploads do not survive restart. Reselect them; uploaded references already sent remain in the original task.

### Result text differs from the original file

Expected: Save version changes only the result in gogosling. Follow section 5.4 and wait for Applied to change the workspace file.

### File stays Previewed, not written

Request application and approve when prompted. An active workspace run requires waiting and retrying afterward.

### File changed

The target changed after preview. External content was not overwritten. Inspect the current file and generate a new preview.

### Needs review

The write could not be confirmed. Query status instead of repeatedly requesting. The system neither automatically rewrites nor overwrites with an unidentified file. Retain preserved originals until you can identify the correct copy.

### Application outcome unknown

Query/refresh first; do not submit again or assume either success or no change before review.

### Enhancement failed or takes too long

Original text/message count remain intact. Retry busy work after capacity frees; reload after module-load failure; request another enhancement if needed. Empty/incomplete candidates never replace the original.

### Preview unavailable

Inspect unsupported/oversized/encrypted/corrupt/converter-unavailable cause. Download for your usual software or refresh. File changed means modification occurred during reading. One failed preview does not break other tasks.

### Earlier task missing

Check archives, load more before searching, and remember the current 200-item limit. Not displayed does not mean deleted.

### Empty usage: was it free?

No. This round supplied no usage count; consult the provider bill.

### Does closing stop work?

Desktop close hides to tray with work continuing. Actual tray exit first checks unsaved content.

### Does task deletion delete my files?

No workspace-file deletion. Messages, drafts, and result versions are removed; save/discard unfinished result edits first. Assistant deletion similarly removes its tasks/projects/drafts/versions while retaining files.

---

## 7. Future capabilities

These designs are not current buttons. Each becomes available only after its own delivery.

### Current goal

Current tasks expose the original requirement/latest follow-up. A future Current goal row counts only after user confirmation. Organizing it produces a draft without sending/resuming. Cancel retains Not organized or the last confirmed goal; later follow-ups mark that confirmation as older rather than silently rewriting it. Until this row exists, the first request does not imply all later changes were merged.

### Lists and search

Tasks/projects/materials/results currently expose at most 200 items. Future More results enables browsing; full-text search is separate. Until then, search remains loaded-task-only, and incomplete searching must not claim absence.

### Usage and charges

Current counts cover the returned round. Future displays separate task, enhancement, and provider-returned charges. Missing charges are not zero; no editable unit price, balance, or money-based auto-stop is provided.

### Original files on Linux/Windows

Platform acceptance is separate. Equivalent future write-back still previews one text file, approves this preview, and requires Applied. A new OS does not grant multifile writes. A future rule will require separate manual preview approval even under Automatic in sandbox; until delivered, inspect the actual prompt/state rather than assuming the stricter rule exists.

### Clicking an unknown outcome again

Inspect existing tasks first. A future control prevents the same click from creating another send before review completes; changed text/materials make a new request.

### Teams

Joining a team does not automatically publish or move personal projects, drafts, or results. File changes still require local confirmation. Without a team entry, use sections 1–6.

### Excluded buttons

No automatic resume on opening, selection-based restriction of all file access, multiple assistants per project, cloud continuation after lid closure, assistant delegation, Office editing in preview, calculated plan balances, messaging, online conversion, sharing links, or web retrieval is promised by this local module plan. Phone/computer synchronization is absent; a future mobile entry only opens the acquisition page.
