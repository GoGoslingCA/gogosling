# gogosling user guide: Personal workbench

For people completing documents, spreadsheets, or file changes on their own computer. No internal architecture knowledge is needed.

The guide uses assistant, task, material, project, result, version, copy, and file change. An agent in Settings is the same object as an assistant in the workbench.

Sections 1–6 describe the local workbench flow recorded on 2026-10-12, before a signed installer release. Section 7 separates planned additions from current limits. Module-specific local-edition plans remain drafts; they do not remove existing capabilities by themselves.

---

## Outline

1. Before starting
2. Your first task
3. Interface areas
4. Core capabilities
5. Step-by-step operations
6. Questions and troubleshooting
7. Future capabilities

---

## 1. Before starting

You need gogosling running on your computer, a model endpoint and model name, any required credential, and a goal. Reference files are optional.

Three distinct actions matter:

1. **Save version** keeps the result inside gogosling for reopening, without changing the original file.
2. **Save copy/export** creates another file, without overwriting the original.
3. **File change** applies a saved version to one workspace file after preview and approval. Previewing does not change the file. Wait for Applied to confirm success; if the outcome is unknown, inspect the file and operation status before retrying.

Guarded file changes are implemented for Linux and macOS; the workflow below was manually verified on macOS. Windows supports versions and copies/exports. If your service does not advertise file-change support, use copy/export.

If a connection screen appears, follow your setup instructions to enter the local service address and access token. The desktop app normally connects automatically. The browser keeps the token in memory only.

---

## 2. Your first task

Follow the sequence; use the wording actually present in your build if a label differs.

### Step one: Connect a model

1. Open Settings → Model connections.
2. Create a provider and enter name, type, endpoint, and credential if required.
3. Save the connection, then test connectivity using the saved provider. Saved credentials are not echoed.
4. If testing fails, resolve the connection before creating an assistant; see section 6.

### Step two: Prepare an assistant

1. In Settings → Agents, create an agent.
2. Enter a name and select the saved provider/model.
3. Select an approval mode. Ask for everything requests approval for tool calls, including reads, unless an existing rule or grant applies. Ask for risky actions allows ordinary workspace reads and writes but asks for commands, network access, messaging, and deletion. Auto inside sandbox allows eligible operations within the permitted boundary; execution and network access still require approval without OS isolation. Protective rules remain in force in every mode.
4. Save. If uncertain, choose Ask for everything; changing the mode affects all tasks using that assistant. Applying or undoing a file change from Results always needs separate approval for that preview.

### Step three: Start work

1. Return to the workbench and choose New task.
2. Write your goal. A start example fills the draft without sending.
3. Attach/drop reference files if needed; inspect the selected-material count.
4. Check assistant, model, and workspace near the input.
5. Start task. With no text/files, Create empty task lets you add requirements later.

### Step four: Handle approval

Inspect the owning task, action, and target. Approve once permits this operation. Use session approval only when explicitly allowing later matching operations in that session; deny to prevent the operation.

### Step five: Keep a result

1. Choose Save draft and edit on a reply you want to keep. A chat reply becomes a saved result only when you choose this action.
2. Edit in Results. Saved confirms that the current draft has been stored.
3. Save a new version when ready.
4. Reopen the task to inspect it later.

---

## 3. Interface areas

The sidebar provides New task, Find a task (loaded tasks only), Projects, Jobs, Recent tasks, and Settings. Recent tasks support continuation, pinning, renaming, and archiving. Settings groups appearance, model connections, agents, skills and extensions, messaging, file access, activity records, and resource controls. Draft local-edition plans describe proposed boundaries, not the removal of existing features.

A planned Mobile app entry would open the official download page in the system browser, without signing in, synchronizing, or sending desktop files. It is not available until an official URL is supplied.

The center shows the conversation and progress above the input. Results open on the right. A narrow window uses Conversation/progress and Results tabs sharing one edit state. Enter sends; Shift+Enter inserts a new line.

---

## 4. Core capabilities

### 4.1 Tasks

You can add follow-ups to a task, stop its current run, archive it, and restore it later. States include not started, queued, processing, awaiting approval, completed, incomplete, and stopped. For Recovering or Unknown, wait or inspect history before resending.

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
| Request apply | Request approval to change one workspace file according to its preview |

Viewing version history preserves your current edits. Restoring historical content creates a new saved version without deleting the old one.

### 4.5 Enhance prompt

With input text, Enhance prompt rewrites only the unsent text using the current assistant's model. It does not read attachments or send a task. The enhanced text replaces the input directly. Restore brings back the original text; Adjust opens an optional feedback field. You can also edit the draft yourself.

Enhancement calls the selected provider and may incur charges. Its usage is separate from the current task run.

### 4.6 File preview

Open an attachment, material, or workspace file to read it. Closing the viewer releases its preview resources. Readers cover text/code, static web content, modern Office, OpenDocument, PDF, images, supported audio/video, and archive member listings.

- Spreadsheets show stored values and formula text; formulas are not recalculated.
- Complex layouts, animations, and macros may be omitted.
- Legacy Office and full-layout conversion require a separately installed local converter on macOS. When unavailable, download the file and open it in your usual software.
- Decrypt protected PDFs in the original software first.
- Long content may be truncated; download retains the complete original.

### 4.7 Usage

Inspect input, output, and cache-read tokens for the current run. Missing data appears as Not provided; it does not mean the request was free. Provider bills determine charges; the app does not estimate money.

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
3. Choose Adjust to request a shorter prompt or a specific format, then enter your feedback and choose Apply feedback.
4. Inspect the final draft before Enter/Send.
5. Restore brings back the original text if needed. Cancellation or failure keeps the text you had before that request. Editing, switching tasks, or sending prevents late results from overwriting newer work.

### 5.3 Edit and version a result

1. Open a saved result and edit it.
2. Wait for Saving → Saved, then Save new version.
3. Viewing version history preserves your current edit.
4. Restoring an earlier version saves a new revision.

If another version was saved meanwhile, your edits are retained. You can load the latest version, or keep your edits and explicitly use the latest version as the baseline for a new save.

### 5.4 Apply a change to one text file

The flow below was manually verified on macOS; Linux uses the same guarded operation. Use copy/export if file changes are unavailable on your platform.

1. Save a formal version first.
2. Open File changes and enter a workspace-relative target, such as `notes/description.txt`, never an absolute computer path.
3. Select Create or Replace and generate a preview.
4. Read added/deleted lines; the file is still unchanged.
5. Choose Request apply and approve this specific preview.
6. Wait for Applied before inspecting the target in your file browser.

To undo, choose Preview undo on the applied record, inspect the preview, then approve it separately. Undoing creation deletes the file and is approved as deletion. A file changed afterward cannot be undone using the old preview.

### 5.5 Pin, archive, and find tasks

Use task actions under Recent tasks. Pin keeps a convenient sidebar entry; unpin does not delete. Archive prevents sending until restored. Search covers loaded tasks; load more before concluding a task is missing.

### 5.6 Adjust concurrent work

In Settings → Resources, choose 1–8 concurrent tasks (default 2). The selection saves automatically. Lowering the limit lets running tasks finish. Up to 32 tasks can wait in the queue without calling the model. Interactive work has priority, with a priority boost for long waits. Tasks in the same workspace run in sequence, and a run awaiting approval keeps its slot.

### 5.7 Leave and exit

Unsent text, uploads, or unfinished result saves trigger confirmation before leaving. Closing the desktop window hides it to the tray while accepted work continues. Exit checks unsaved content, then stops the local service only if the desktop app started it. A service started separately keeps running. After reopening, reselect files whose uploads never finished; confirmed drafts and uploaded references remain available.

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

Failure notices should identify stage, retained work, what did not happen, and one next action. Include task/stage/state/time when reporting rather than only Failed. Use the examples below to choose the next step.

### Start does nothing or requests assistant configuration

Check the saved/tested provider under Settings → Model connections, then the enabled assistant using it, and return to Start.

### Connectivity test fails

Verify the full provider endpoint and credential. Leaving the credential field blank keeps a previously saved credential; it cannot supply one that was never saved. Save before testing; model discovery uses saved settings. If failure remains, inspect the provider's own account/service status. The app cannot determine billing or outage status from a generic connection failure.

### Unsure whether Start created the task

Do not click again immediately. Check the reported stage—task creation, material upload, or submission—and inspect the existing task for your message. Retry only after checking whether it was already accepted.

### Session already has an unfinished run

Wait for the current run to finish, fail, or stop, or choose Stop yourself. Stopping a run does not delete the task.

### Draft save failed

Keep the window open, choose Retry, and wait for Saved before leaving. If another saved draft conflicts with yours, inspect both before choosing which to keep.

### Selected files disappeared after reopening

Unfinished uploads do not survive restart. Reselect them; uploaded references already sent remain in the original task.

### Result text differs from the original file

This is expected: Save version updates only the result stored in gogosling. Follow section 5.4 and wait for Applied to change the workspace file.

### File stays Previewed, not written

Choose Request apply, then approve the preview. If a task is running in that workspace, wait for it to finish before retrying.

### File changed

The target changed after preview. External content was not overwritten. Inspect the current file and generate a new preview.

### Needs review

The write could not be confirmed. Refresh its status instead of repeatedly requesting the change. Automatic retries stop in this state. Keep the preserved original and inspect the files before deciding how to recover.

### Application outcome unknown

Query/refresh first; do not submit again or assume either success or no change before review.

### Enhancement failed or takes too long

Your text is retained and no task message is sent. If the service is busy, retry when capacity is available. If the enhancement module failed to load, refresh the page. Empty or incomplete suggestions do not replace your input.

### Preview unavailable

Check the reported reason: unsupported format, file too large, encryption, corruption, or an unavailable converter. Download the file to open it in your usual software, or refresh after resolving the problem. File changed means the file was modified during reading. One failed preview does not affect other tasks.

### Earlier task missing

Check archived tasks and use Load more in task history before searching again. Search covers only loaded task titles; project, material, and result lists currently show at most 200 items. A missing search result does not mean the task was deleted.

### Empty usage: was it free?

No. This round supplied no usage count; consult the provider bill.

### Does closing stop work?

Desktop close hides to tray with work continuing. Actual tray exit first checks unsaved content.

### Does task deletion delete my files?

Deleting a task removes its messages, drafts, and result versions, but leaves workspace files intact. Export anything you need to keep before deleting the task. Deleting an assistant similarly removes its tasks, projects, drafts, and versions while retaining workspace files.

---

## 7. Future capabilities

The items below distinguish planned additions from current limits. Planned controls become available only after implementation and validation.

### Current goal

Tasks currently show the original request and later messages. A planned Current goal summary would become authoritative only after you confirm it. Generating the summary would not send a message or resume work. Cancelling would preserve the previous state; later follow-ups would mark the confirmed summary as needing review. Until this feature exists, the original request is not a summary of all subsequent changes.

### Lists and search

Task history already supports Load more, and search matches loaded task titles only. Project, material, and result lists currently show at most 200 items; pagination for those lists and full-text search remain future work. A partial search cannot establish that an item does not exist.

### Usage and charges

Current counts cover the usage returned for a run. A future display would distinguish task usage, prompt-enhancement usage, and charges returned by the provider. Missing charges would not be shown as zero. Editable unit prices, account balances, and spending-based automatic stops are not available.

### Platform validation

Linux and macOS have guarded write-back implementations, but packaged-app and filesystem validation remain separate work. Windows currently uses preview/export. Any future platform support must preserve the one-file preview, approval, and Applied confirmation; it does not imply multi-file writes. Separate approval for the current preview is already required in every mode, including Auto inside sandbox. A deny rule can still block the operation.

### Clicking an unknown outcome again

Inspect the existing task before retrying an uncertain submission. A planned safeguard would prevent the same request from being sent again until its outcome has been checked. Changed text or materials would count as a new request.

### Teams

The team workflow is a separate draft. It requires explicit choices before personal projects, drafts, or results become shared, and preserves local confirmation for file changes. Team controls are not part of the current personal-workbench flow.

### Excluded buttons

The local-edition plan adds no automatic resume, multiple assistants per project, cloud continuation, assistant delegation, Office editing in preview, or calculated account balances. Selecting materials does not redefine file permissions. Messaging and web-retrieval shortcuts, online conversion, and sharing links are outside that draft's scope; this does not disable existing connections or tools. Phone/computer synchronization is unavailable, and the proposed mobile entry would open only the official download page.
