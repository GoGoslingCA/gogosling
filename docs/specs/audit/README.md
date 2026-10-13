# Module plan: Activity records

| Item | Description |
|---|---|
| Status | Draft |
| Date | 2026-10-12 |
| Module | Activity records; shown as Activity log in Settings |

Review completed operations and decisions. This module neither applies new file changes nor connects to external services.

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

Review tool operations and approval decisions from newest to oldest, narrowing by assistant, operation, and time.

## 2. Responsibilities

| In scope | Out of scope |
|---|---|
| List, filter, paginate, and inspect records | Undo the recorded file change |
| Explain an empty filtered result | Invent a successful operation |
| Refresh | Upload records or publish sharing links |

Records support inspection; they are neither billing nor a second usage counter.

## 3. Key workflow

1. Open the latest page by default.
2. Filter by assistant, actor identity, operation type, task, run, or start time.
3. Times include a timezone. Reject invalid filters and retain the current list.
4. Show an empty state when no records match. Activity records do not report spending.
5. Inspect details without echoing keys or tokens.

## 4. Inputs and outputs

| Input | Output |
|---|---|
| Filters | A matching page, empty result, or invalid-filter notice |
| Refresh | Reloaded list |
| Detail request | Operation, target, and result without plaintext credentials |

## 5. Dependencies

| Module | Relationship |
|---|---|
| Approval | Completed decisions appear with the operation; records do not change decisions |
| Workbench | Records do not replace the workbench's four-part failure explanation; users may inspect further here |
| Assistant | Filter by assistant; deleted assistants retain their historical identity, without reassignment |

## 6. Potential failures

| Failure | Visible fact |
|---|---|
| Invalid time filter | Do not apply; explain the format |
| No records | Empty state, not a fault unless refresh failed |
| Refresh failed | Retain the previously displayed list and explain this refresh failed |
| Missing detail fields | Show unknown, never invent |

## 7. Verification

| Case | Expected result |
|---|---|
| Inspect after denying approval | Find that denial |
| Invalid time | Filter rejected with notice |
| Clear filters | Return to the latest unfiltered records |
| Inspect details | No keys or tokens visible |

## 8. Assumptions and open questions

| ID | Detail |
|---|---|
| Q-AU1 | No retention-duration promise exists; do not invent a number of days |
