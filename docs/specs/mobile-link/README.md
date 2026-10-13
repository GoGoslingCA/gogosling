# Module plan: Mobile app link

| Item | Description |
|---|---|
| Status | Draft |
| Date | 2026-10-12 |
| Module | Mobile app link |

This draft proposes a link to the official mobile-app page. It does not provide synchronization or a second workbench, and it does not remove existing connection settings.

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

After an official URL is published, open the mobile-app download page from desktop. Desktop tasks, drafts, results, and files stay on this computer.

## 2. Responsibilities

| In scope | Out of scope |
|---|---|
| Display the Mobile app row | Login, pairing, QR synchronization |
| Open the published URL in the system browser | Embed the page in the app |
| Hide the entry until a URL is published | Invent store links or QR codes |
| Explain an opening failure | Delete or upload tasks |

Never upload tasks, drafts, results, or files, or pull mobile content onto the computer.

## 3. Key workflow

1. Before publication of a URL, hide the row in Settings/start page and show no empty button.
2. Once published, add Mobile app to Settings About or the start-page footer.
3. Clicking opens that URL in the system browser.
4. If opening fails, explain the link failed; returning to the workbench retains the original task.

## 4. Inputs and outputs

| Input | Output |
|---|---|
| Published URL | Clickable Mobile app row |
| No URL | No row |
| Click | System browser opens, or a failure notice |
| Workbench tasks/files | Not inputs; no token, body, or file is carried in the link |

## 5. Dependencies

| Module | Relationship |
|---|---|
| Workbench | Places the row; mobile is not described as a task source |
| Desktop | Tray closure/exit does not trigger synchronization |
| Model connection, assistant, scheduled jobs | No handoff |

Messaging connections, cloud synchronization, and sharing links are not substitute entries.

## 6. Potential failures

| Failure | Visible fact |
|---|---|
| URL unpublished | Entry absent; not a fault |
| Browser fails to open | Only the link failed; tasks intact |
| Destination inaccessible | Browser-side failure; desktop results unchanged |

## 7. Verification

| Case | Expected result |
|---|---|
| No published URL | No Mobile app row in Settings/start page |
| Publish a test URL and click | System browser opens; no in-app login page |
| Block browser opening | Notice appears; tasks/drafts/files remain |
| Inspect link | No token, task body, or files |

## 8. Assumptions and open questions

| ID | Detail |
|---|---|
| A7 | Link opens the official mobile-app page only |
| Q-5 | Official URL not supplied; hide the link and do not assume a store address |
