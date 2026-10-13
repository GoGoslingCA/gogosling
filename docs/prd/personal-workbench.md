# PRD: gogosling Personal Workbench

| Item | Content |
|---|---|
| Status | Draft v0.5 |
| Date | 2026-10-12 |
| Audience | Product, design, acceptance testing, and user documentation |
| Product Scope | An individual using an assistant on their own computer to complete tasks and manage reference materials and deliverables |
| Excluded | Internal rules of team repositories. For how personal workbench interfaces with team capabilities, see Section 12.7; for the team itself, see [Team Workflow PRD](team-workflow.md) |
| Implementation Status | Sections 2–10 define the baseline and acceptance requirements; they are not a release checklist with every item passed. The local-only navigation changes in Section 4.3 and mobile app link are proposals, not current behaviour. Section 12 plans future work; Sections 13–15 define module boundaries and follow-up requirements. Signed installers are not yet available |

This draft describes the personal workbench developed by Gogosling.ca, a Canadian company. It specifies user actions and expected outcomes, not technical implementation. For the current application, see the [user guide](../user/personal-workbench.md); proposed restrictions here do not remove existing messaging, network tools, or settings.

---

## Outline

1. Document Notes, Research, and Assumptions
2. Project Background and Goals
3. Target Users and Core Pain Points
4. Requirements Scope and Priorities
5. Feature List
6. User Stories and Business Flows
7. State Transitions and Rule Boundaries
8. Exceptions and Edge Cases
9. Permissions Description
10. Acceptance Criteria
11. Launch and Iteration Plan
12. Future Product Design
13. Module Planning Notes
14. Key Questions List
15. Cross-Module Troubleshooting Recommendations

---

## 1. Document Notes, Research, and Assumptions

### 1.1 Adopted Baselines

- Product interface copy (Workbench, Projects, Deliverables, File Changes, Preview, Settings).
- User outcomes verified via real operations in the workbench, file modification, prompt optimization, navigation, and preview delivery records dated 2026-10-12.

Product constraints:

- When reopening a task, separately display the objective, draft, deliverable version, and run status; continuing execution is explicitly initiated by the user.
- Confirmation behavior depends on the actual approval process, not substituting permission checks with mode names.
- Unknown usage is displayed as "Not yet provided", not as zero, nor with estimated monetary costs.
- Undoing applies only to a single recorded file modification.

### 1.2 Assumptions

The following items lack user research samples or official release data; they are included in this document to close the scope. If they conflict with subsequent decisions, the new decisions prevail, and this document will be updated accordingly.

| Number | Assumption |
|---|---|
| A1 | Primary users of the first version are individuals needing to produce documents, spreadsheets, or code, including product managers, operations, and indie developers. No demographic or industry distribution data is available |
| A2 | One computer, one user. Drafts are scoped to the current access identity, but this document does not design for multi-user simultaneous editing |
| A3 | The desktop application is the primary entry point. When opened via browser, the local address and access token must be obtained separately; this path does not serve as the primary five-minute onboarding flow |
| A4 | Models are provided by the user's own provider account. This product does not sell packages or calculate billing |
| A5 | "Assistant" and "Agent" in Settings refer to the same object. The workbench uses the term "Assistant" with users |
| A6 | The verified environment for file write-back is macOS. Whether Linux is usable on physical machines is marked in this document as unverified and not included in this release's commitments. Windows cannot write changes back to original files in this release |
| A7 | The proposed local-only edition removes the navigation entries listed in Section 4.3 and retains a future "Mobile App" link. These changes are not implemented in the current application; they must not be described as existing behaviour |
| A8 | The model address filled in by the user will still communicate according to that address. This is the model chosen by the user, not cloud services provided by this application. Local models can likewise be selected |

### 1.3 Decisions and Open Questions

| Number | Question | How Handled in This Document |
|---|---|---|
| Q-1 | Under the three confirmation levels, does writing back to original files always pop up a confirmation | Resolved in the current implementation: every permitted user-initiated file apply or undo requires approval for that operation only, regardless of the assistant tier. Deny rules still take precedence. "Applied" confirms success; an unknown or verification-required result does not prove that no write occurred |
| Q-2 | Reply quality, speed, and cost on real providers | Not included as acceptance items for this release |
| Q-3 | How the current interface paginates when exceeding 200 items | Task history loads pages of 200 and supports loading more; search covers only loaded tasks. Project, material, and deliverable lists currently expose only their first 200 items. Section 12.3 plans broader browsing and full-text search |
| Q-4 | Window restoration for installation packages across multi-monitor hot-plugging, Windows, and Linux | Not validated on physical machines; not included in this release's launch commitments |
| Q-5 | Official address of the mobile app | There is no verifiable store or download address in the repository. Before the address is determined, the interface will not display the link or use placeholder URLs |

---

## 2. Project Background and Goals

### 2.1 Background

gogosling enables users on their own computers to hand over a task to an assistant: reading materials, drafting content, viewing progress, and modifying files in the workspace when needed. Models and files remain in the user's own connections and directories.

When users return to a task, they need to distinguish four things: whether files are still available, whether drafts were saved, which requirements are current, and how far execution has progressed. Permission descriptions and whether confirmations are actually prompted may also disagree. When costs are unknown, displaying them as zero leads to mistaken conclusions.

This workbench aims to turn these differences into visible states, rather than adding yet another layer of automatic continuation.

### 2.2 Goals

| Number | Goal | How It Is Considered Achieved |
|---|---|---|
| G1 | Start a task with one action | After writing requirements and selecting materials, a single confirmation creates the task, adds materials, and submits. If any step fails, tell the user where it stopped, and completed portions remain intact |
| G2 | Show task state on return; resume only on request | After reopening, the user can see the task, unsent draft, deliverable edits, latest deliverable version, and whether the run is in progress, completed, or unknown. Does not automatically resend unknown results |
| G3 | Deliverables and original files are separate items | Saving versions, saving copies, exporting, and writing back to original files each produce distinct results, with consequences explicitly stated in the interface |
| G4 | Files can be inspected before write-back, and write failures do not overwrite others' edits | Inspect diffs and target paths first. If the file was modified after the preview, halt write-back and preserve external content. Enter "Needs Review" when confirmation is impossible, without automatic re-execution |
| G5 | Truthful usage metrics | Display input, output, and cache read tokens when available for this turn. Display not yet provided when unavailable. Do not estimate monetary costs |

### 2.3 Non-Goals

- Team sharing, team knowledge bases, and multi-user concurrent editing of the same deliverable are not included in this release.
- Projects are not treated as isolated file workspaces. Projects under the same assistant share that assistant's workspace.
- No commitment is made to restore an assistant's internal execution context at an arbitrary intermediate step, nor will a task automatically resume running upon opening.
- Commands executed, network calls made, or other file side effects caused by an assistant will not be rolled up into a single "Undo All".
- No account quotas, billing, or spending caps are provided.
- Legacy office documents are not disguised as previewing successfully when local conversion capabilities are missing.
- Writing back to the original file is not supported on Windows.
- The proposed local-only edition excludes the navigation entries in Section 4.3. This is a future scope boundary, not a statement that current network capabilities have been removed.

---

## 3. Target Users and Core Pain Points

### 3.1 Users

| User | Goal | Prerequisite Skills Not Required |
|---|---|---|
| Personal knowledge worker | Bring materials, draft or revise a document, and save a version they can reopen | Does not require command-line or version control experience |
| Individual needing to edit local files | Clearly see which file will be changed, write only after consent, and know whether the original file is intact upon failure | Does not require understanding internal execution processes |
| Returning user with existing tasks | Retrieve previous input, deliverables, and exact stopping points | Does not require remembering specialized object names |

Scheduled tasks can still be accessed via the main navigation and run on this machine at the designated time. Removing message-channel entry points is part of the Section 4.3 proposal; the current application still exposes Messaging in Settings.

### 3.2 Pain Points

| ID | Pain Point | Workbench Solution |
|---|---|---|
| P1 | Starting an action requires multiple clicks, and files must be added after the fact | The start page and task page share the same input system; start in one go when content is present |
| P2 | Upon returning, drafts, goals, and run status validity are unclear | Separately display save status, goals, recent additions, deliverable versions, and run status |
| P3 | Unclear which materials were used or which directories will be touched | Centrally display assistant, model, working directory, current materials, and confirmation rules before sending |
| P4 | Replies, copies, and original files are jumbled into a single save action | Four actions are separated, with consequences stated next to each action |
| P5 | Consequences are unclear when an assistant attempts to modify files | Preview diffs first; a single approval covers only the currently previewed file |
| P6 | Usage and billing are conflated into the same concept | Only display tokens actually provided by the service; monetary amounts are subject to provider invoices |

---

## 4. Requirements Scope and Priorities

### 4.1 In-Scope for This Release

| Priority | Capability | Description |
|---|---|---|
| P0 | Connect model and prepare assistant | When no assistant is present, the start page points to configuration instead of pretending it can start |
| P0 | Start a task in one go | Creation, attaching materials, and submission happen sequentially; retry an existing task on failure |
| P0 | View progress and handle confirmations | Status reflects actual execution. Stop when needed, approve or reject when confirmation is required |
| P0 | Draft preservation for entered text | Show as saved only after service confirmation. Preserve both copies on conflict for manual resolution |
| P0 | Formal deliverable versions | Created only after the user explicitly saves a preliminary draft. Subsequent saves append new versions without mutating old ones |
| P1 | Projects | Group materials, tasks, and deliverables under the same assistant. Existing tasks can be assigned to a project without duplicating history |
| P1 | Optimize prompts | Only modifies text in the input box without submitting the task |
| P1 | Open and read files | Read supported formats directly; explain reasons and provide downloads for unreadable formats |
| P1 | Write saved version back to single text file | Committed only for macOS in this release. Preview first, decide by permissions; preview undo independently |
| P2 | Scheduled tasks, concurrency limits, and charts | Scheduled tasks run locally on schedule. Default concurrency is 2, adjustable from 1 to 8; the queue limit is 32. The mobile app link is a separate proposal in Section 4.3 |

### 4.2 Explicitly Out of Scope for This Release

- Team workflows.
- Pagination beyond the first 200 projects, materials, or deliverables, and full-text search across unloaded tasks.
- Automatically summarizing the "latest active goal" into a new task specification. Currently shows the initial goal and the latest addendum without claiming they are merged.
- Cross-assistant project sharing or using projects to restrict file permissions.
- Windows write-back to original files. Linux guarded writes exist in the core, but physical-machine desktop acceptance is still pending.
- Billing, quotas, automatic outbound transmission, or cloud continuation after computer sleep.
- Internet capabilities listed in Section 4.3.

### 4.3 Proposed Local-Only Navigation and Mobile App Link

This proposal would remove the following entry points from the start page, task page, projects, and settings. It has not been implemented. Existing capabilities remain available until a separately reviewed change defines migration and compatibility behaviour:

| Removed Capability | User-Facing Result |
|---|---|
| Messaging channels | Settings contain no inbound/outbound entry points for Telegram, Lark, DingTalk, WeChat Work, Discord, etc. |
| Cloud assistant, cloud continuation, account sync | No login-gated entry point to migrate tasks to the cloud |
| Online conversion, online preview, share links | Files are read only on this machine. Download if conversion fails; files are never sent online |
| Connector marketplace, skill marketplace | Skills use only what is already installed on this machine |
| Web page fetching, automatic outbound messaging | Workbench provides no entry points to "fetch content online" or "send messages on your behalf" |

The proposed edition would retain a "Mobile App" link:

- Located in Settings under About, or as a single line in the start page footer, labeled "Mobile App".
- Clicking would open the verified download or store URL in the system browser. It does not open in-app and requires no login.
- This link does not upload tasks, drafts, deliverables, or files, nor does it pull mobile content back to the computer.
- The official URL is not yet provided (Q-5). Before the URL is finalized, hide this line; do not place empty buttons or placeholder store links.

User-configured model endpoints are not included in the table above. That belongs to the model connection module settings; this module only displays the model name currently used by the assistant. This module does not add a second cloud backend to the application.

If message channels still have their own pages in other modules, this module will not link to them from navigation or settings, nor will it display their failures in workbench notifications. This document does not modify that module.

---

## 5. Feature List

| ID | Feature | User-Facing Result |
|---|---|---|
| F01 | Start Page | A welcome message, an input area, and three examples. Examples populate drafts only and do not send automatically |
| F02 | Create Task | Blank input creates a task only; content or materials trigger processing immediately |
| F03 | Recent Tasks | Resume, pin, search, rename, archive, restore. Search scope is limited to loaded tasks |
| F04 | Task Conditions | Assistant, model, working directory, confirmation rules, current materials, and current-round usage placed near input |
| F05 | Supplemental Requirements | The next sentence can be drafted while running and sent after the current round finishes |
| F06 | Progress | Displays goals, recent supplements, and real steps. Does not fabricate progress when there are no steps |
| F07 | Confirmation | Identifies which task it belongs to, what to do, and on which target. Approving once does not change long-term rules |
| F08 | Stop | Stops the current run. Written deliverable versions and saved drafts are retained |
| F09 | Projects | Create, edit, archive, and restore. New tasks cannot be started from a project after archiving; existing tasks can still continue |
| F10 | Materials | Unchecked by default after adding to a project. Checking only indicates inclusion in this message; unchecking does not delete files |
| F11 | Deliverable Versions | Save first draft, edit, automatically retain working draft, save new version, view history, and save another version using historical content |
| F12 | Copies and Export | Leaves a copy in the workspace attachments directory, or exports one. Does not overwrite original files |
| F13 | Optimize Prompt | Apply an enhanced prompt directly to the input. Restore returns to the original; an optional Adjust dialog accepts feedback. The action does not send a task message or change attachments |
| F14 | File Preview | Read documents, spreadsheets, presentations, text, images, audio/video, and archive directories. Does not continue holding this content after closing |
| F15 | File Modification | Generate diffs for a single text target. Wait for results after application. Applied creations can be previewed again as deletions; applied replacements can be previewed again as reverts |
| F16 | Settings | Preferences, features, data, and security. Back navigation returns to the original task and deliverable. Removal of messaging and online-service entries belongs to the Section 4.3 proposal |
| F19 | Mobile App | Proposed: one link to a verified external address, hidden until configured. No synchronization is planned |
| F17 | Concurrency | View running and queued counts. After lowering, actively executing items finish first |
| F18 | Charts | Data in messages meeting specifications can be viewed as bar or line charts with an accompanying data table. Invalid data still displays as raw text |

---

## 6. User Stories and Business Workflows

All personas are fictional, used solely to anchor workflows, and do not represent existing user research samples.

### 6.1 Stories

**Story A: the product lead writes a briefing**

The product lead opens Gogosling, where a Document Assistant already exists. On the start page, she drags in two documents and enters "Write an internal memo based on these two documents, listing the questions to be confirmed first." She clicks "Start Task." After the task opens, the input area displays the assistant, working directory, and 2 documents. If a confirmation prompt appears before the assistant reads the documents, she verifies the object and approves it once. Once the reply is generated, she clicks "Save First Draft and Edit", finishes editing in the output area, sees that the draft is saved, and then clicks "Save New Version". Opening the same task the next day, the versions are still present, and any unsent additions confirmed as saved by the service will also be there.

**Story B: the team administrator modifies only one file**

The team administrator already has a saved text deliverable. He opens "File Modification," enters the relative path within the workspace, selects replacement, and generates a preview. He sees added and deleted lines alongside "Not Yet Written." When the confirmation dialog appears, he approves only this preview. The target file updates to the new content only after the status changes to "Applied." If the file was modified by someone else before his approval, the status changes to "File Changed," and external content is retained.

**Story C: the operations member optimizes a sentence before deciding whether to send**

The operations member writes a short request and clicks "Optimize Prompt." The enhanced text replaces the input without sending a task message. She can edit it, choose "Restore" to return to the original, or open "Adjust" to provide feedback and select "Apply feedback." Closing Adjust leaves the current input intact; canceling an in-progress enhancement prevents its result from replacing the input. Attachments remain unchanged.

### 6.2 Main Workflow: From Idea to Reopenable Deliverable

1. If no assistant is available yet, the start page provides a configuration entry point. The user first saves the model connection, then saves the assistant.
2. Write goals on the start page, optionally adding materials. Materials appear under "Current Reference."
3. Click "Start Task." The sequence is fixed as: create task → add selected materials → submit requirements. Blank content creates only an empty task.
4. Failure at any step: stays at the completed stage, with text preserved. If a task was already created, retrying reuses that task instead of creating another.
5. If the submission outcome is unknown, ask the user to check the existing task before resubmitting.
6. While running, one can view steps, stop, or write the next supplement.
7. When confirmation is required, review the task name, action, and target first, then choose approve once, allow for this session, or deny. File modifications do not use "allow for all future instances in this session."
8. After the reply finishes, the user actively selects "Save First Draft and Edit." If not selected, no formal deliverable is created.
9. The editing process automatically saves working drafts. When the user saves again, a new formal version is created. Older versions can still be viewed.
10. Prompt before closing the deliverable area, switching tasks, or exiting if there is unsaved content pending confirmation.

### 6.3 Workflow: Writing a Formal Version Back to a File

1. A saved formal version must exist first. A write-back preview cannot be generated directly while viewing historical versions.
2. Fill in a relative path within the assistant's workspace and select Create New or Replace.
3. Generate a preview. At this point, the target file remains unchanged, with the status "Previewed · Not Written."
4. The user requests Apply and approves that preview once. Rejection or cancellation before writing leaves the target unchanged. If execution has started and its outcome is unknown, verify the recorded operation before retrying.
5. Only when the status becomes "Applied" does it indicate this preview has been written to the target.
6. To revert later, a separate revert preview must be generated. Reverting a creation equals deleting the file, requiring separate confirmation based on deletion risk. Reverts execute only if the file still matches the last applied result.

### 6.4 Workflow: Starting from a Project

1. Select an assistant when creating a project. It cannot be changed to another assistant after creation.
2. Add materials. New materials are not checked automatically.
3. Check files to reference this time, then start the task. Unchecked files are not attached to this message.
4. Removing materials from a project removes only the reference within the project. Original files and previously sent messages are retained.
5. Older tasks can be reassigned to this project; the tasks themselves, messages, and titles are not recreated as a result.

---

## 7. State Transitions and Rule Boundaries

### 7.1 Single Run

| State | Meaning | What the User Can Do |
|---|---|---|
| Pending | Task exists, but this round is not yet submitted | Compose requirements and start |
| Queued | Accepted, but processing has not yet started | Wait, or stop |
| Processing | Currently processing | View existing steps, write the next supplement, or stop |
| Awaiting Confirmation | Paused at an operation requiring human decision | Approve once, allow for this session, or deny. File modifications allow only this current preview |
| Completed | This round finished with results | Save first draft, continue supplements |
| Failed | This round failed | View failure details, resend requirements. Does not rerun automatically |
| Stopped | The user stopped this round | Completed steps are preserved; requirements can be resent |
| Recovering | Reading previous state | Wait for the status check. Do not label the run as completed or generating until its state is known |
| Unknown State | The current state cannot be confirmed | Verify first; does not resend automatically |

Rules:

- When the same task has an unfinished run, another message cannot be sent concurrently. Supplements remain in the input area first and are sent after completion.
- Reconnection recovers states that already occurred. Lost intermediate steps must be noted as potentially incomplete; after completion, the full reply prevails.
- When opening a run started earlier that has not yet finished, indicate it started before this page was opened, and display the full reply once finished.

### 7.2 Drafts

There are two types of drafts: unsubmitted inputs, and edits within deliverables that have not yet been saved as official versions.

| Display | Meaning |
|---|---|
| Checking draft | Reading in progress |
| Saving | Submitted; service confirmation pending |
| Saved | Service has confirmed this version |
| Save failed | In-window text remains intact; closing or refreshing may lose unconfirmed portions |
| Draft updated | Both current content and another saved copy are retained; manual selection required |
| Restored | Text or uploaded material references recovered. Check whether this message already exists in the task before sending |
| Files pending upload | Local files not fully uploaded cannot resume after a restart. Text and uploaded references can be restored |
| Current connection does not support autosave | Cannot be restored after closing or refreshing |

Rules:

- Saved status is displayed only after service confirmation.
- Conflicts are not silently overwritten. Options are "Keep Current and Save" or "Use Remote Draft".
- Upon successful sending, the draft version active at the time of sending is cleared. Text entered after sending is not cleared.
- Save results returned after navigating away from the page must not erase content currently being written by the user.

### 7.3 Deliverable Versions

- Saved deliverables must originate from actual replies and be initiated by the user clicking "Save Initial Draft and Edit" or through corresponding code saving.
- Once a version is saved, it is not rewritten. Subsequent saves create new versions.
- Viewing history does not alter the current draft being edited.
- When restoring using historical content, require confirmation first, then create a new version upon saving without deleting old versions.
- If a newer official version exists, the current edit remains intact. The user must either load the latest content or explicitly use the latest version as a baseline before continuing to save.
- Saving versions, saving copies, exporting, and writing back to original files are mutually independent. The first three do not overwrite original files in the workspace.

### 7.4 File Modifications

| Status | Target File |
|---|---|
| Previewed · Not written | Unchanged |
| Pending approval | Unchanged |
| Applying | Not yet presented to user as completed |
| Applied | Consistent with this approved preview |
| Rejected, Canceled, Preview Expired | Unchanged. Repreview required after expiration |
| File modified | Not written. External content retained |
| Apply failed | Not presented as success |
| Verification required | Do not continue writing, do not auto-retry. If necessary, only restore the retained original copy to its vacated original location without overwriting a new external file |
| Request status unknown, Verifying | Query status first, do not automatically resubmit |

Rules:

- A preview applies to only one target file with content derived from the version saved at that time; the preview remains bound to that saved version. To apply newer content, generate a new preview.
- This version of write-back accepts only plain text, with a maximum size of 512 KiB.
- Creation and replacement are supported. Deletion appears in previews that undo new creations, not as an entry point for arbitrary workspace cleanup.
- When a workspace has an active runtime, file write operations are not started, and a prompt to retry later is shown.
- When completion of a write operation cannot be confirmed, do not mark it as applied simply because contents appear identical.

### 7.5 Lifespans of Projects, Tasks, and Assistants

| Action | What Happens | What Does Not Happen |
|---|---|---|
| Archive task | Cannot send further messages until restored | Does not delete messages and deliverables |
| Archive project | Cannot initiate new tasks from here | Does not stop existing tasks |
| Delete task | Deletes messages, drafts, and deliverable versions of this task | Does not delete workspace files |
| Delete assistant | Deletes its tasks, projects, drafts, and deliverable versions | Does not delete workspace files; handled according to existing constraints if still used by scheduled tasks |
| Remove material reference | No longer listed in the project | Does not delete original files, does not rewrite sent messages |

A task can belong to a project or be "Unassigned to project". Affiliation changes neither the task identity nor file permissions.

### 7.6 Concurrency

- Defaults to 2 concurrent executions, configurable from 1 to 8.
- Up to 32 queued items. Queued items do not consume model allocations.
- Tasks initiated manually on-demand take priority over scheduled tasks. Tasks with longer wait times receive priority elevation.
- Tasks within the same workspace execute sequentially.
- Waiting for confirmation consumes one execution slot.
- Reducing the execution limit does not interrupt already executing tasks.

### 7.7 Boundaries of Previews and Optimizations

- Prompt optimization processes only the current input text, without reading task history or attachments, executing tools, or creating new messages. It uses the current assistant's model. Usage metrics for official tasks exclude this optimization.
- Previews are read-only. Refreshing rereads content. If a file changes during the reading process, a conflict prompt is shown instead of treating outdated displays as the latest file.
- Spreadsheets display saved values or formula text without executing formulas, macros, or external links.
- Web previews are static, running no scripts and loading no remote resources.
- Encrypted PDFs require the user to remove encryption in the native application first.
- When content exceeds the preview limit, only a partial display is shown along with a notice that the complete file can be downloaded.

---

## 8. Exceptions and Edge Cases

When issues arise, first determine which stage failed according to Section 15, then verify the expected outcome for that stage against the table below.

| Scenario | Product Behavior |
|---|---|
| Creation succeeded but upload failed during initiation | Retain task and drafted text, display prompt allowing material modifications and retry, do not create a second task |
| Connection lost after submission, unknown whether it was sent | Prompt to open existing tasks to verify first. Do not automatically resend before confirming there are no duplicates |
| Materials replaced or unavailable before sending | Reject the entire submission. Retain draft text and prompt to remove unavailable materials |
| Unsent text or ongoing upload when closing window | Ask. For desktop version, if choosing to stay in tray when closing window, tasks continue; still handle unsaved confirmation before actual exit |
| Closed while deliverable is being saved | Ask. No guarantee that the unconfirmed save has succeeded |
| Power outage before auto-save is confirmed | Only guarantee content that previously displayed "Saved". Do not promise that the last character has been written |
| Editing the same deliverable concurrently in two places | The person saving later sees a conflict, both copies of content are present, and a human chooses |
| Target has changed when approving file modifications | Stop and mark as file modified; external bytes are retained |
| Outcome cannot be confirmed during Apply | Mark as requiring verification. Do not automatically rewrite, and do not delete originals of unknown source |
| File is no longer the result of the last apply when undoing | Do not execute undo |
| Refinement result is empty, clearly unfinished, or contains internal notes that should not appear | Do not adopt this candidate; retain original text; retry allowed |
| No execution slot available for refinement | Explicitly prompt busy; do not start another queue to dispatch tasks |
| Legacy local service lacks projects, drafts, versions, file modifications, preview, or refinement | Hide corresponding entry points; existing tasks, attachments, and copy saving remain available |
| Project, material, or deliverable list exceeds 200 items | These lists expose only the first 200 items. Task history can load additional pages. Do not describe an item that is not displayed as deleted |
| Search tasks | Search only tasks that have already been loaded; interface keeps this statement |
| Chart data invalid or exceeds 50 valid values | Do not render chart; retain original text |
| Missing local office conversion capability | Legacy office files indicate preview unavailable, download allowed. Do not upload to online services for conversion |
| Windows | Can preview, save versions, save copies, and export. Cannot write back to original files |
| Switch assistant on start page | Input area switches to this assistant's own unsent draft. Unsent sentences from the previous assistant are not retained here |
| Result of a start arrives late | Content still belongs to that original task; do not navigate away from the page the human is currently viewing |
| Two files with the same name added concurrently | Each file is retained individually; the latter does not overwrite the former |
| View deliverable or preview after task has ended | Still accessible. Cannot refuse reading just because the assistant is not running |
| Two tasks awaiting confirmation simultaneously | Kept independent. Processing one will not modify the other, nor will it be recorded onto the currently open task |
| Close page or minimize to tray | Accepted runs do not stop because of this. If the final reply is missing and the run status cannot be confirmed, show an unknown outcome; do not automatically start another run |
| Messages remain after reopening | Only indicates records remain. Does not indicate assistant has resumed with all earlier content, nor does it automatically resume |
| Connection failed | Stay on connecting or preparing. Do not describe as quota exhausted without evidence. Do not treat old failures as the result of this recent attempt |
| Navigate away after a draft save fails | Preserve the unsaved text in memory and warn before an action that could discard it |
| Deliverable versions, originals before write-back, operation logs | Not automatically deleted based on age |

When assistant names, model names, or directory names are truncated, users must be able to view full names. When a human is reading earlier messages, new content must not forcibly scroll the reading position to the bottom. These two items are not yet represented as completed in the current version and are first recorded in the module issue list.

---

## 9. Permissions

### 9.1 Who Can Do What

This version is for local single-user use. A person who can open the application and holds the current access identity can manage their own assistants, tasks, projects, and deliverables. There are no separate guest, approver, or administrator roles.

### 9.2 Assistant Confirmation Tiers

Each assistant has one tier, affecting whether to ask a human when the assistant executes operations. Modifying an assistant affects all tasks using it.

| Tier | Rules User Should Understand |
|---|---|
| Approve All | Every operation with side effects requires approval |
| High-Risk Approval | Executing commands, accessing network, sending messages, deleting, etc. must ask first |
| Automatic in Sandbox | Can complete autonomously within permitted scope; must still ask beyond that scope |

Rules:

- "Read-only" and "write, execute, network, delete" are not the same risk. The interface must label them with text, not just colors.
- An "Approve once" decision covers only that operation and does not change the assistant's long-term tier.
- "Allow within this session" exists only for confirmations during task execution, not for file modifications. The approval copy for file modification is "Approve current preview of this file only".
- If rules deny an operation, it is not executed. User-initiated file apply and undo otherwise require their own once-only approval, even in Automatic in Sandbox mode.
- Selecting materials only determines which references this message carries; it neither grants new directories nor revokes workspaces the assistant originally had access to.
- Project descriptions must retain this statement: Projects use the workspace of the same assistant; attribution does not expand or isolate file permissions.

### 9.3 What Data Remains Local

- Model keys only enter the local credential store; settings page does not echo them.
- Access tokens are not written to browser persistent storage.
- Task bodies, attachment contents, and deliverable bodies are not written to local preferences used only for appearance.
- Preview does not send files to external conversion websites.
- Usage numbers come from model service responses; this product does not construct a separate bill.

The proposed local-only edition would omit messaging entry points. Any future mobile app link must not carry access tokens, tasks, or files.

---

## 10. Acceptance Criteria

The following items must all be actionable by a human on the interface and display the specified results. Use a test model and sample files. Response quality of real providers is outside this checklist.

| Number | Scenario | Passing Criteria |
|---|---|---|
| AC1 | New user without assistant | Can enter configuration from start page; cannot pretend task has started before configuration is complete |
| AC2 | Single start with two materials | Only one task is generated; these two materials can be verified on the message; input area conditions match selections |
| AC3 | Upload phase failure | Text remains; retry does not generate a second task |
| AC4 | Submission outcome unknown | Verification prompt appears; a second identical request will not appear automatically |
| AC5 | In-run confirmation | Can identify which task it belongs to, what to do, and on what target; operation does not execute after rejection |
| AC6 | Stop | Status changes to stopped; saved deliverable versions can still be opened |
| AC7 | Save initial draft and modify again | Obtains version 1 and version 2; when opening version 1, content being edited remains unchanged |
| AC8 | Re-enter after closing | Previously confirmed saved input drafts and deliverable edits remain; unuploaded local files have clear prompts to reselect |
| AC9 | Two drafts conflict | Both can be viewed; no automatic overwriting |
| AC10 | Archive | Archived tasks cannot send; can send after restoration. Archived projects cannot start new tasks; old tasks can still be opened |
| AC11 | Delete task or assistant | Has clear consequence descriptions; original workspace files remain |
| AC12 | Replace a text file on macOS | The file remains unchanged during preview; after approval, the status is applied and the content matches the preview; the file remains unchanged upon rejection |
| AC13 | External modification after preview before approval | Status is file changed; external content remains unchanged |
| AC14 | Undo file creation | Preview separately and confirm again via delete; the file remains intact before confirmation |
| AC15 | Optimize prompt | Enhancement fills the input without adding a message. Canceling an in-progress request retains the input; Restore returns to the original after replacement. Closing Adjust does not discard the current text |
| AC16 | Unsupported preview | Reason and download provided; other tasks and files remain usable |
| AC17 | Narrow window | Conversations and artifacts can be toggled; save and stop remain clickable; toggling does not lose edits |
| AC18 | Legacy service lacking new capabilities | New entry point does not appear; existing send, attachment, and save copy actions remain usable |
| AC19 | Usage | If numbers exist, break down into input, output, and cache; if not, display not yet provided without showing an amount of 0 |

This version does not mark the following scenarios as passed: Linux physical-machine write-back, Windows write-back, formal installer packages, multi-monitor hot-plugging, complete browsing of project, material, and deliverable lists beyond 200 items, and real-world model quality.

---

## 11. Launch and Iteration Plan

### 11.1 Current Phase

The implemented local capabilities can be evaluated against Section 10. This draft does not establish that all acceptance criteria have passed. A packaged desktop release requires separate installer and platform validation.

### 11.2 Recommended Release Sequence

| Phase | Target Audience | Included | Excluded |
|---|---|---|---|
| Closed Beta | Single users willing to use the macOS desktop version | Section 4.1 P0 and P1; file write-back limited to macOS text | Teams, billing, Windows write-back, and Linux write-back marketing |
| Public Desktop Release | Same as above, with installation instructions provided | Closed Beta items plus physical-machine logs for window recovery and exit confirmation | Still excludes unfinished items from Q-3 and Q-4 |
| Subsequent Minor Releases | Existing users | See table below; entry points appear only after separate acceptance | Do not expose unfinished entry points prematurely |

Release notes must clearly state in user terms: saving a version does not modify the original file; writing back files requires separate preview and confirmation; tasks remain after closing to tray, and unsaved content is prompted before exit.

### 11.3 Subsequent Release Sequence

The sequence is ordered by "eliminating one misjudgment upon return." The full implementation of each item is detailed in Section 12. Entry points will not appear, nor will multiple items be bundled into a single update, before passing acceptance in that section.

| Sequence | Design | Misjudgment Resolved First |
|---|---|---|
| 1 | Current Goal (Section 12.2) | Treating the first message as the ongoing task to do now |
| 2 | Continue Browsing, Separated from Full-Text Search (Section 12.3) | Equating not displayed with non-existent; calling the loaded list the full set |
| 3 | Usage Explanation, Still Without Estimating Amounts (Section 12.4) | Treating current-turn tokens, optimized prompts, and provider bills as the same figure |
| 4 | System Scope for File Write-back (Section 12.5) | Exposing write-back on unverified systems, or adding multi-file writes without separate review |
| 5 | Single Click Generates Only One Message (Section 12.6) | Clicking again while the result is unknown emits a second message |
| 6 | Integration with Team Capabilities (Section 12.7) | Personal drafts and projects being transferred or exposed immediately upon joining a team |

The once-only approval rule for user-initiated write-back is already implemented (Q-1). Section 12.5 carries that existing rule into future platform acceptance.

---

## 12. Future Product Design

This section covers designs not yet delivered. It corrects several proposals in the original "candidates for next round" that were grouped into the same table despite being distinct matters: organizing goals does not mean automatic continuation; pagination does not mean full-text search; clarifying usage does not mean introducing pricing; file write-back on a given system does not mean modifying many files at once; joining a team does not mean handing over existing drafts.

Resuming a task cannot replace resuming a goal; checkpoints cannot revert all operations; unknown costs must not be displayed as zero. With no user scale data available, this section does not list adoption rates.

### 12.1 Rules That Every Future Item Must Satisfy

1. Do not alter the semantics of existing actions in Section 7. Saving a version still does not modify original files, and optimizing prompts still does not dispatch tasks.
2. New entry points remain invisible before passing acceptance. Legacy local services continue to hide unrecognized capabilities.
3. Upon failure, preserve user-entered content and state which step the process stopped at.
4. Results that cannot be confirmed remain "Unknown"; they must not be automatically retried, nor displayed as completed or zero.
5. A design must resolve only one type of misjudgment. Unaccepted portions must not be exposed alongside accepted portions.

### 12.2 Current Goal

**Misjudgment to eliminate:** The goal in today's task progress originates from the first message, with recent additions listed on a separate line. If "Continue" only carries back the first message, subsequent requirement modifications will be lost. Passing the entire history to the assistant to continue running automatically likewise mistakes an old goal for a new one.

**Future objects:** Each task maintains three distinct lines simultaneously that cannot overwrite one another.

| Name | Origin | Editable By |
|---|---|---|
| Initial Goal | The first human-sent message in this task; not rewritten after saving | Immutable. To rephrase, draft a new current goal |
| Recent Addition | The latest human-sent message | Updated with the next send |
| Current Goal | Exists only after human confirmation | Human can edit and re-confirm |

When not yet confirmed, Current Goal displays "Not yet organized" and must not display as a paraphrase of Initial Goal, preventing both lines from appearing as if they were "already verified."

**Workflow:**

1. The user clicks "Organize Current Goal." The assistant drafts a paragraph based solely on human-sent messages within this task, without reading files, executing tools, adding chat messages, or initiating execution.
2. The drafted result is previewed first. The user can edit the text and click "Use as Current Goal." Clicking "Cancel" retains the original "Not yet organized" status or the last confirmed statement.
3. The confirmed statement is rendered at the top of the task. It does not delete any messages or trigger a send.
4. If the user subsequently sends an addition, Recent Addition updates while Current Goal remains unchanged, annotated with "Confirmed prior to this addition." The user can re-organize it.
5. When Current Goal exists, "Continue" references it while still requiring prior verification of existing results to avoid repeating completed file operations. When Current Goal does not exist, "Continue" includes both Initial Goal and Recent Addition without pretending they have been consolidated.

**What Not to Do:**

- Do not automatically organize or confirm upon opening a task, reconnecting after disconnection, or finishing execution.
- Do not write Current Goal as a system instruction to override the assistant's original configuration.
- On organization failure, timeout, or empty result: preserve pre-confirmation state, retain text in the draft area, and allow retries.
- Do not restore an in-progress state of a tool execution from Current Goal.

**Acceptance:** Display "Not yet organized" when only Initial Goal exists. Editing Recent Addition after confirmation keeps the original Current Goal text intact alongside a timestamp note. Canceling organization produces no messages. The statements referenced by "Continue" across both scenarios match the table above.

### 12.3 Continue Browsing, and Full-Text Search

Task history already supports loading additional pages of 200; its search still covers only loaded tasks. The remaining work is pagination for project, material, and deliverable lists, plus full-text task search. These must be separate requirements: pagination does not mean a search covers the entire dataset.

**Continue Browsing:** Extend the task-history pattern to projects, materials, and deliverables. These lists should display "Count displayed" and "More available" when exceeding the current page. Loading more will not replace already viewed items with a new batch and cause previously viewed items to be lost. Upon load failure, the currently displayed list remains intact. If the count cannot be determined temporarily, display "Total count unknown" instead of 0.

**Full-Text Search:** Released separately from Continue Browsing. The search box clearly states "Search across all tasks." Results must be able to open tasks not yet loaded into the current list. Search failure must not describe an empty result as "Task not found," but rather "Search incomplete this time." Before this capability is released, the search box continues to state "Search loaded tasks only."

Archived items still only appear after a user opens "Archived," preventing archived tasks from mixing into in-progress items upon resuming browsing.

**Acceptance:** Construct a list exceeding 200 items, verify the last item is visible, and ensure mid-process failures do not lose loaded items. Before full-text search release, the search box retains the scope explanation. After release, unloaded tasks can be retrieved by title and opened.

### 12.4 Usage Explanation

The original direction was "discuss amounts only once price sources exist." This is tightened here to prevent creating a billing page that calculates unknown costs as 0.

The usage section is always divided into three blocks and cannot be aggregated into a single figure:

| Block | What to display | When there is no data |
|---|---|---|
| This task turn | Input, output, and cache-read tokens, as well as the corresponding timestamp and source for the figures | Not yet provided. Do not display 0 |
| Prompt optimization | Explicitly note separately: "This optimization is not counted toward the above turn" | Do not silently add optimizations into task turns |
| Cost | Only display "amount returned by provider" and timestamp when the model service explicitly returns an amount | Do not estimate; do not display 0 CNY |

Unit prices manually entered by users appear only when "Estimation" is explicitly enabled in the future, which is off by default. Estimation results must include the word "Estimated"; items missing a unit price display "Unit price not set" and are excluded from summation. When estimation is off, amount input fields do not appear on the interface.

This product still does not sell plans, maintain account balances, or support "stop once a certain amount is spent." If spending must be constrained, limit only user-understandable actions, such as concurrent execution count; that belongs to existing resource settings, not a spending limit.

**Acceptance:** When the service does not return usage, do not display 0. After optimizing once, the task turn numbers remain unchanged, and the optimization has its own description. No price appears when provider amounts are missing. Unit prices cannot be entered when estimation is off.

### 12.5 Expanding File Write-back to More Systems

**Preserve the existing approval rule when expanding platform coverage:** For user-initiated file modifications, regardless of whether the assistant is set to "Approve all", "High-risk approval", or "Automatic in sandbox", "Approve current preview of this file only" must appear. Assistant tiers affect only assistant-initiated operations. Rejection rules still take precedence; files remain unchanged when a rejection occurs.

This approval rule is already enforced by the permission service for operations with an `OperationID`. Section 9 and Q-1 describe the same current behaviour.

**Maintain today's status table when expanding scope.** Previewed, Pending approval, Applying, Applied, Rejected, Expired, File changed, Apply failed, Canceled, Verification required, and Unknown result retain their meanings.

| System | When write-back may appear | Before that time |
|---|---|---|
| macOS | Text files already accepted according to Section 10 AC12–AC14 | — |
| Linux | The core implementation supports guarded writes; desktop release acceptance still requires the same scenarios in a real Linux environment | Do not describe CI coverage as completed physical-machine acceptance |
| Windows | After guarded writes are implemented and pass the same scenarios in a real Windows environment | Preview, copies, and export remain available; Apply is unsupported |

The same release does not add these capabilities: modifying multiple files at once, modifying folders, renaming, modifying binaries, modifying permissions, or framing undo as reverting all actions the assistant ever performed. Undoing creation is still deletion and must be previewed separately and confirmed again.

**Acceptance:** Assistants set to Automatic in sandbox still trigger current preview confirmation when writing back files. Do not advertise unverified desktop platform acceptance. Unsupported systems, including Windows, must not offer Apply. After passing, external modifications still result in "File changed" while retaining original content.

### 12.6 A Single Click Produces Only One Message

Today, if the result after submission is unknown, the product requires the user to verify in history first, which remains correct. The gap is: if the user misses something during verification and clicks again, a second identical message might be sent.

**Future rules:**

1. One "Start task" click or one send corresponds to one attempt. After a connection drops, if the same attempt is resubmitted and has already produced a message, open that message instead of sending a second one.
2. Sending after the user edits text or materials constitutes a new attempt, which may produce a new message.
3. When it cannot yet be determined whether the previous attempt succeeded, the button remains "Verifying" and cannot be clicked into a second send.
4. When verification fails, preserve the input and explain that it was not resent. Do not automatically retry on behalf of the user.

Scheduled tasks and user sends remain two separate categories of sources. This rule constrains only that single click by the user on the start page and in the task input box.

**Acceptance:** Artificially induce "sent but result not received"; clicking again still results in only one corresponding message in the task. Modifying text before sending produces a second message. No second sentence is sent during verification.

### 12.7 Interfacing with Team Capabilities

How teams create repositories, conduct reviews, and feed back experience follows the team workflow PRD. This section specifies only what cannot happen when interfacing with the personal workbench, preventing the two documents from contradicting each other.

| Existing items in personal workbench | After joining or using team |
|---|---|
| Projects, tasks, drafts, deliverable versions | Remain in their original locations. Will not automatically move to the team or become public |
| Assistant confirmation tier | Remains unchanged unless manually modified by the user in assistant settings |
| Unsent drafts | Will not become team experience or team assets |
| Confirmation of file modifications | Still completed on this computer; not approved within chat tools |

Team entry points appear in navigation only when team capabilities are available. Without a team, the workbench operates as it does now. After a user turns off certain team data sharing, personal tasks can still continue.

Skills, rules, and experience originating from the team must include sources. They serve as references, not new system instructions, and cannot replace the current goal confirmed by the user in Section 12.2. Executable content still requires consent from the user on this computer before running.

**Acceptance:** Users who have not joined a team do not see team entry points, and existing task workflows remain unchanged. After joining, drafts and deliverables created prior to joining remain only in original local tasks on this machine. Unconfirmed public content does not appear in submission previews requiring peer review.

### 12.8 Proposals Not Included in the Above Versions

The following ideas surfaced during research. They are documented here to prevent them from being treated as "convenient additions" later.

| Proposal | Decision | Rationale |
|---|---|---|
| Automatically resume execution upon opening a task | Do not implement | Contradicts Section 12.2. Check status first upon returning |
| Selecting materials means the assistant can only read these files | Not in the above versions | Currently, selection only determines what this message attaches. Truly restricting readable scope requires a standalone toggle disabled by default; if restrictions cannot be enforced, do not display the toggle. Cannot rely solely on prompts |
| Using multiple assistants in one project, or file isolation between projects | Do not implement | Would alter the principle that "projects neither expand nor tighten permissions" |
| Writing back multiple files at once | Do not implement | Cannot be coupled with the system expansion in Section 12.5 |
| Resuming in cloud after closing the laptop lid | Do not implement | Tasks in this product run on this computer |
| Assistant invoking another assistant | Not in the above versions | If implemented in the future, subtasks must share the same execution quotas; parent tasks cannot hold onto a quota while waiting. Do not introduce "delegation" before this rule exists |
| Treating office documents as editable deliverables | Do not implement | Previews remain read-only. Modifying content still goes through text deliverables or user-owned software |
| Automatic deliverable deduplication, disk usage statistics | Do not promise users how much space is saved | May serve as maintenance information later, but cannot be framed as files being safely deleted |
| Calculating plan balances within this product | Do not implement | No reliable pricing and account sources. See Section 12.4 |

---

## 13. Module Planning Notes

This section plans only the single module: "Personal Workbench." Model connections, assistant settings, runtime execution, confirmation rules, scheduled tasks, and team repositories are all external modules. Only handoffs with them are specified here, without altering their rules. Message connections fall outside this module's handoffs.

### 13.1 Module Objectives

Enable an individual to clearly see every step of a workflow from input to deliverable, and know when any step fails: where it stopped, what has been preserved, what did not occur, and what single step to take next.

Corresponds to G1–G5 in Section 2.2. This module is not responsible for model response quality, nor for whether internal configurations of other modules are correct.

### 13.2 Responsibility Boundaries

| This module is responsible for | This module is not responsible for |
|---|---|
| How the start page, task list, and project materials/tasks/deliverables are organized together | Model addresses, API keys, and connectivity itself |
| Displaying assistant, model, working directory, current materials, confirmation tier, and current-turn usage before sending | Changes occurring in other tasks after modifying assistant tiers |
| Decomposing a start action into "create, attach materials, submit", and remembering which stage it stopped at | What the assistant actually read and executed |
| Whether drafts are confirmed by the service, and preserving both versions during conflicts | Concurrent multi-user editing |
| Versions, duplicates, and exports of formal deliverables, and distinctions between these actions and write-backs to original files | Files outside the workspace |
| Previews, statuses, and "not yet written" explanations for file modifications | Converting a single approval into a permanent rule |
| Not adding task messages when optimizing prompts | Quality of optimized phrasing |
| Explaining reasons and offering downloads when previews fail to open | Turning previews into editing, or recalculating spreadsheets for users |
| Explaining quota status visible to this module when queuing | Why scheduled tasks launch at a specific moment |
| Hide entry points when the legacy service lacks a capability | Upgrade or install the local service |

When deleting a task or assistant, this module only guarantees stating the consequences: messages, drafts, and deliverable versions will be removed, while files in the workspace remain. Whether files are still used by scheduled tasks is governed by the scheduled task module's own constraints.

### 13.3 Key Flows

This module has only five primary flows. Each must be able to indicate its current stage on the interface.

1. **Preparation:** When no assistant is available, the start page navigates only to configuration without creating a task.
2. **Start:** Create task → Add reference materials → Submit request. Blank content only creates a task. If any segment fails, stop at that segment.
3. **Processing:** Display running status, receive confirmation decisions, allow stopping, and allow composing the next follow-up message in advance.
4. **Retention:** Save edited drafts, and a human then saves formal versions; copies and exports follow separate flows.
5. **Writeback:** Only when the current system provides this capability: Preview → Request → Confirm → Applied. Previews do not write files by themselves.

Prompt optimization and file previews are side-channel flows: they can fail, but they cannot alter items 2–4 to sent or written status.

### 13.4 Inputs and Outputs

| Input | Required Output Once Received by This Module |
|---|---|
| Target text, selected assistant, checked reference materials | One task, or paused at a segment among creation/adding materials/submission; text remains |
| Stop, approve once, allow within session, reject | Running status updates accordingly; the operation is not executed after rejection |
| Deliverable edits, save new version, save copy, export | The corresponding single result. The four actions cannot substitute for one another |
| Target and saved version for file modification | One preview and the subsequent file status. Only "Applied" confirms the preview was written. Unknown or verification-required states need a status check and must not be treated as proof that the file is unchanged |
| Optimize prompt | One candidate, or failure details. The task message count remains unchanged |
| Open preview | Readable content, or failure with reason and download option |

When no task exists, the task name in the output reads "Not yet created". Do not use blanks that lead users to believe a task already exists.

### 13.5 Dependencies

Arrows indicate handoffs, not that this module can modify the counterparty.

| Counterparty Module | This Module Hands Off | Counterparty Returns | How This Module Displays Unclear Returns |
|---|---|---|---|
| Local service | Drafts, versions, and file operations to save | Confirmed, conflict, or failure | Save failure or unknown result. Do not display saved or applied |
| Model connection | Connection used by current assistant | Connected or not; current round tokens or not provided | Connection failure halts at preparation. Usage displays not yet provided |
| Assistant | Assistant selected by user | Name, model, working directory, confirmation level | Halts at preparation when no assistant exists |
| Runtime | A pending request | Queued, processing, completed, incomplete, stopped, or unknown | Unknown requires verification first, no automatic resending |
| Confirmation | Associated task, action, and target | Approve once, allow within session, or reject | When it is unclear which task it belongs to, decisions cannot be made on the user's behalf |
| Workspace | Reference materials, copy destination, writeback target | Whether file still matches the preview | If inconsistent, show "File has changed" and do not overwrite |
| Scheduled tasks | No direct content. Only shares execution quota | Whether quota is occupied by scheduled tasks | Queue notice must specify whether scheduled tasks occupy it or the current task is waiting |
| Mobile app | A browser link | No data returned | When unable to open, only indicate the link did not open; workbench tasks remain unchanged |
| Messaging connection | No handoff | No reception | The proposed local-only edition would omit these entries; current Messaging settings are unchanged by this draft |
| Team | None. No such handoff before Section 12.7 takes effect | None | No team entry point appears |

### 13.6 Potential Failure Points

| Stage | Exception | Facts This Module Must Make Immediately Visible |
|---|---|---|
| Connection | Service not running, invalid token | No task exists yet. Text written on the page and unconfirmed for saving may exist only in the current window |
| Preparation | No assistant, or model test failed | Task not created |
| Start | Upload failed after successful creation | Task exists, text remains, no second task |
| Start | Unknown whether sent after submission | Status unknown. No duplicate request automatically appears |
| Runtime | Same task still has an unfinished round | New sentence stays in input area, waiting for this round to finish |
| Runtime | Disconnection caused missing segment in process | Note that it may be incomplete; defer to the complete reply once finished |
| Confirmation | Confirmation dialog does not show task | This is a display defect of this module. Do not approve yet |
| Draft | Navigating away before service confirmation | Only the last "Saved" status is reliable |
| Draft | Conflict between two drafts | Both drafts remain; a human must choose |
| Deliverables | A newer formal version already exists | Current edits remain |
| Files | Target modified after preview | External content is preserved |
| Files | Unable to confirm midway through writing | "Needs verification". Do not automatically rewrite, do not delete originals of unknown origin |
| Optimization | Result empty, incomplete, or service busy | Original text remains, message count unchanged |
| Capabilities | Legacy service lacks a capability | Entry point hidden. Existing send and save copy operations remain available |
| Quotas | Execution quota occupied by other tasks or scheduled tasks | Clarify where occupancy originates. Do not state that this task is already processing |
| List | Project/material/deliverable list exceeds 200 items, or task search covers only loaded pages | Clarify that display is incomplete. Do not state that no more exist |

---

## 14. Key Issues List

The following issues are all within the scope of this module's display and descriptions. Those marked "To be added" are not yet uniformly achieved by the current interface; before they are added, troubleshooting remains based on existing status terms on screen, without describing them as already deployed.

| Number | Issue | Impact | This Module's Handling |
|---|---|---|---|
| K1 | Failure notices are scattered; some only state failure without explaining what was preserved | Users will repeatedly start or write back | To be added. Standardize failure notices around four facts: which stage stopped, what has been preserved, what did not occur, and only one action for the next step |
| K2 | Multiple notices for the same operation do not match the task name | Cannot tell which issue it is across modules | To be added. Notices for the same operation carry the same task name, stage, status, and timestamp. Show "Not yet created" when no task exists. Do not log body text, secret keys, or full file contents |
| K3 | Confirmation dialog displays as unnamed before the task name is read | Users may approve the wrong task | When the name is not read or reading fails, approval is blocked. Rejection remains available. Approval appears only after reading the name |
| K4 | Queuing is easily mistaken for active processing | Users will send another message | When queuing, explain that processing has not started and is waiting for execution quota. When single-occupancy source is unknown, do not fabricate whether it is a scheduled task or another task |
| K5 | Save version, copy, export, and writeback share the vague "Save failed" | Users assume original file was changed or lost | Failure sentences must include the action name. Writeback failure is not labeled copy failure |
| K6 | When results are unknown, users may still click again | May send a duplicate identical message | Currently require verification first. Completely preventing duplicates belongs to Section 12.6 and is not in the current release |
| K7 | Whether writeback always prompts under three confirmation levels | Cannot judge whether files changed based on level name | Resolved in Q-1: each permitted manual file apply or undo requires once-only approval. Troubleshooting checks the operation status independently; an unknown outcome must be verified before retrying |
| K8 | Scope of list and search is easily mistaken for "All" | Users assume old tasks are lost | Describe each list accurately: task history supports more pages, while projects, materials, and deliverables currently expose their first 200 items. Task search covers loaded tasks only; full-text search is planned in Section 12.3 |
| K9 | "Needs verification" does not clarify preview, confirmation, writing, or verification itself | File issues lead to repeated guesswork | To be added. Include a sub-step in file stage notices: preview, confirmation, writing, verification |
| K10 | Optimization failure and task failure appear to be the same thing | Users search within the task for a nonexistent message | Optimization notices form a separate stage and state explicitly that message count is unchanged |
| K11 | Assistant names are truncated on the start page | Users may select the wrong assistant | Selection box uses the full name as hover tooltip |
| K12 | Pulled to the newest message while reading earlier messages | Users lose their reading position | Stop following once scrolled away from the bottom. Resume from the newest content when reopening a task |

K1, K2, and K9 remain planned improvements to status messages. K4 only explains that it is still waiting, without inventing an occupant. Do not add buttons before completion, nor add descriptions in other modules to substitute for it.

---

## 15. Cross-Module Troubleshooting Recommendations

Documentation and rules of other modules remain as they are. When troubleshooting, inspect only along handoff boundaries: if the previous stage yields a clear result while the next stage is empty or unknown, the issue lies in this specific handoff, rather than at the end of the entire chain.

### 15.1 Error Messages

Planned failure messages should cover the following four facts, using as few sentences as clarity allows. Each fact must agree with Section 8; do not claim this format is already applied throughout the interface.

1. Which stage it stopped at: Connection, Preparation, Start, Running, Confirmation, Draft, Outcome, File, Optimization, Capability, Quota.
2. What has been preserved: for example, the text is still intact, the task has been created, edits are still preserved, external files are still intact.
3. What did not happen: for example, no second task was created, the original file was not modified, there are no new messages.
4. Take only one single action next: check, retry, reject, refresh status, or navigate to corresponding settings. Do not present three concurrent actions.

When the peer module only returns "Failure" without facts, this module displays "The result of this stage is unknown", rather than guessing to fill in success or zero.

### 15.2 Log Traces

The trace here is a process record that can be transcribed directly from the screen, not another set of internal records.

The process record for a single operation contains four items: time, stage, task name, and status. The status only uses "Completed, Failed, Unknown" along with the existing status terms from Section 7. When multiple prompts belong to the same operation, these four items must be identical.

The record does not include the full text of requirements, full text of outcomes, keys, tokens, or file contents. When diagnosing a failure verbally, transcribing these four items is sufficient to pinpoint which handoff it is.

If the current version does not yet have centralized process records in the details view, extract these four items from the current prompt; write "Not in prompt" for missing items, and do not fabricate information.

### 15.3 Troubleshooting Paths

Proceed from top to bottom, stopping at the first matching row.

| Order | What is Seen | Assessment | Next Step |
|---|---|---|---|
| 1 | Unable to connect or invalid token | Stopped at Connection. Workbench has not started yet | Check whether the local service can be opened. Do not create tasks |
| 2 | Requires configuring assistant or connectivity failed | Stopped at Preparation | Go to Model Connections to check test results, then go to Assistants to check if enabled. Workbench does not explain why keys were rejected |
| 3 | Creation, adding materials, or submission has a segment that failed or is unknown | Stopped at Start | Open existing task to see if this sentence is present. When unknown, do not click start again |
| 4 | Queued, processing, incomplete, stopped, status unknown | Stopped at Running | Check status. When queued, check who is occupying the quota. Reply quality is not within this module |
| 5 | Confirmation dialog appears | Stopped at Confirmation | Check task name, action, and target. If names do not match, reject; do not modify confirmation rules |
| 6 | Draft save failed or conflict occurred | Stopped at Draft | Stay in the window to retry or choose between the two. Do not close the window |
| 7 | Version conflict or duplicate copy failed | Stopped at Outcome | Keep the edits first. This does not indicate that the original file was modified |
| 8 | File status is not "Applied" | Stopped at File | Handle according to status terms. When check is needed or unknown, only refresh; do not reapply |
| 9 | Only optimization prompt failed | Stopped at Optimization | Check if original text is still intact and if message count increased. Do not return to the running stage to re-check |
| 10 | Entry point itself does not exist | Stopped at Capability | Confirm whether legacy services hide the capability or the current system does not provide write-back in the first place |

Under the proposed local-only navigation, messaging entries would be omitted. Failure to open a future mobile app link must not affect local tasks, drafts, or files.

### 15.4 Verification Methods

Each item only verifies whether this module displays the handoff correctly. Practice models and practice files are sufficient.

| Verification | How to Do | What is Seen When Passed |
|---|---|---|
| Connection | Open using invalid token | Stops at Connection, no new task |
| Preparation | Open start page without creating assistant | Only the entry point to configuration |
| Interrupted Start | Fail when adding materials | Only one task exists, text is intact, prompt stops at adding materials |
| Unknown Result | Provide no result after submission | A verification prompt appears. A second item does not appear automatically |
| Confirm Ownership | Cause confirmation dialog to appear | Can read task name, action, and target. Operation does not execute after rejection |
| Draft | Check prompt when saving is not yet confirmed | Does not display saved. Text is intact after failure |
| Outcome | Saving duplicate copy fails | Prompt indicates duplicate copy, edits are intact, original file remains unchanged |
| File | Modify target after preview then approve | Status shows file has changed, external content is intact |
| Unknown File | Do not provide final status after application | Displays unknown or checking. Clicking again will not become a second write |
| Optimization | Cause optimization to fail or cancel | Message count unchanged, original text is intact |
| Quota | Set concurrent execution to 1 and let another item occupy it | Subsequent tasks display queued, and can distinguish that it is not "processing" |
| Capability | Connect to a service without these capabilities | New entry point does not appear, legacy send and duplicate copy remain available |

If any row in the table above does not match, modify the prompt or status display of this module first. Do not modify model connections, confirmation rules, scheduled tasks, or team modules just to make that row pass.
