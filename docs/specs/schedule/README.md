# Module plan: Scheduled jobs

| Item | Description |
|---|---|
| Status | Draft |
| Date | 2026-10-12 |
| Module | Scheduled jobs |

Jobs run on this computer at the planned time. They do not post to other software or represent an ordinary workbench chat.

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

Schedule work, then show last result and next time. A missed execution follows the selected missed-run policy.

## 2. Responsibilities

| In scope | Out of scope |
|---|---|
| Create, edit, delete, run now | Workbench draft-saving rules |
| Run an assistant or push text into a local task | Messaging, email, cloud push |
| Configure time/timezone | Explain model reply quality |
| Skip missed runs or catch up once after recovery | Rewrite saved result versions during catch-up |
| Show next/last time and success/failure/skipped | Hide workbench queue causes |

Push text inserts text into a local target task without invoking a model or leaving this computer.

## 3. Key workflow

1. Enter name, schedule, timezone, and content.
2. Select assistant execution or text-only push.
3. Select skip or catch up once after recovery.
4. Saving exposes the next execution time.
5. Due work shares normal execution capacity; immediate user tasks have priority, and the same workspace runs serially.
6. Run now triggers once without changing the next scheduled time.
7. For a disabled assistant, prompt reselection, enablement, or text-only push.

Use the existing five-field schedule or hourly/daily descriptor syntax. Invalid schedules cannot be saved.

## 4. Inputs and outputs

| Input | Output |
|---|---|
| Name, assistant/text push, schedule, timezone, content, missed policy | Saved job or precise invalid-field notice |
| Due/run now | Local execution or text inserted into the target task |
| Workbench capacity request | Whether scheduled work occupies capacity |

Reject oversized content and retain the existing job; use the current UI limit notice.

## 5. Dependencies

| Module | Relationship |
|---|---|
| Assistant | Uses that assistant's model and approval mode |
| Approval | Required decisions still appear; unattended waiting is never approval |
| Workbench | Shares slots; workbench does not edit schedule times |
| Desktop | May run while hidden in tray; do not pretend jobs continue after actual exit |

## 6. Potential failures

| Failure | Visible fact |
|---|---|
| Invalid schedule | Cannot save |
| Assistant unavailable | Never silently switch to another assistant |
| Missed with skip selected | Last state shows skipped |
| Capacity full | Wait rather than starting an unbounded execution pool |
| Awaiting approval | Occupy one slot until decided or the approval ends |

## 7. Verification

| Case | Expected result |
|---|---|
| Save daily job | Next time shown |
| Run now | One local execution; schedule retained |
| Assistant disabled | Clear notice, no silent success |
| Workbench/job overlap with limit 1 | Later work queued; workbench distinguishes waiting from processing |
| Look for external sending | No option to send to other software |

## 8. Assumptions and open questions

| ID | Detail |
|---|---|
| Q-SC1 | Immediate catch-up timing after sleep is unverified. Current choices are skip or catch up once after recovery; no separate delay promise |
