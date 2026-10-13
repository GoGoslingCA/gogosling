# Module plan: Desktop

| Item | Description |
|---|---|
| Status | Draft |
| Date | 2026-10-12 |
| Module | Desktop window, tray, and exit |

This plan covers opening, hiding, and leaving the app. Task content remains the workbench responsibility.

---

## Outline

1. Goal
2. Responsibilities
3. Key workflow
4. Inputs and outputs
5. Dependencies
6. Potential failures
7. Verification
8. Assumptions and open questions

---

## 1. Goal

After closing the window, users know whether tasks continue. Exiting first checks unsaved content. Reopening restores window size and the last task.

## 2. Responsibilities

| In scope | Out of scope |
|---|---|
| Hide to tray on window close | Synchronize tasks to mobile/cloud |
| Resolve unsaved-content confirmation before exit | Save unconfirmed text on behalf of the workbench |
| Restore size, position, and result-panel width | Restore an assistant's intermediate execution state |
| Place appearance/language entries | Store model credentials |
| Packaged tray launch-at-login switch | Promise installation on unverified real platforms |

A browser connection may request endpoint/token, held only in page memory. This is not the primary desktop flow.

## 3. Key workflow

1. Open the desktop app directly into the workbench, without account login.
2. Closing hides to the tray while tasks continue.
3. Reopen from the tray; restore task/result positions from confirmed UI preferences.
4. On Exit, resolve unsent text, uploads, and unconfirmed result edits. Stop the local service only if this desktop app started it; detach from a separately started service without stopping it.
5. System/light/dark appearance affects only this device.

## 4. Inputs and outputs

| Input | Output |
|---|---|
| Close window | Hidden window; accepted work continues rather than becoming stopped |
| Exit | App closes after unsaved-content confirmation; only its own service process is stopped |
| Reopen | Size and location restored; body text follows confirmed workbench saves |
| Local file not uploaded | Reselect next time, with an explicit notice |

## 5. Dependencies

| Module | Relationship |
|---|---|
| Workbench | Supplies unsaved state; desktop never silently discards or sends on exit |
| Scheduled jobs | Continue while their local service is running. Exit stops a desktop-owned service; a separately started service remains active |
| Mobile app | Closing/exiting does not start synchronization |

Recovery after a monitor is disconnected, and window restoration in packaged Windows/Linux builds, still need platform validation.

## 6. Potential failures

| Failure | Visible fact |
|---|---|
| Unsaved edits on exit | Ask first; cancellation keeps the app open |
| Exit while saving | Ask; an unconfirmed save is not guaranteed |
| Power loss/forced termination | Only content last confirmed saved is guaranteed |
| Position restoration failed | The workbench can still be opened; do not clear tasks |

## 7. Verification

| Case | Expected result |
|---|---|
| Close window | App in tray; accepted run not stopped by closure |
| Save parallelism and reopen | Saved value restored, rather than a false saved notice followed by the old value |
| Exit with unsent text | Confirmation appears; cancelling retains text |
| Reopen after confirmed exit | Confirmed drafts retained; unuploaded files explicitly require reselection |
| Look for login | No account-login gate to use the app |

## 8. Assumptions and open questions

| ID | Detail |
|---|---|
| A3 | Desktop is the primary entry |
| Q-4 | Monitor unplugging and packaged Windows/Linux window restoration lack real-device acceptance |
