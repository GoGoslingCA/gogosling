# Module plan: Model connection

| Item | Description |
|---|---|
| Status | Draft |
| Date | 2026-10-12 |
| Module | Model connections in Settings; individual connections are listed as providers |

Manage the user's local or remote model endpoint. A chosen remote endpoint does not mean the app provides a cloud service.

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

Save a model service and show a connectivity test before use. Saved credentials are never displayed again.

## 2. Responsibilities

| In scope | Out of scope |
|---|---|
| Create, edit, test, and delete connections | Quality of a particular reply |
| Store credentials without echoing | Provider billing |
| Test connectivity and list discovered models | Workbench task drafts |
| Reject deletion while an assistant references it | Select a paid plan |

Return not provided for absent usage, never zero or an estimated charge.

## 3. Key workflow

1. Enter name, type, full service address, and a credential if required.
2. Save before testing; discovery uses the saved connection.
3. After saving, select the connection and model when configuring an assistant. Test first to check connectivity; the saved provider, not a successful test result, is what the assistant references.
4. A blank credential during editing retains the saved one; supplying a value replaces it.
5. Reject deletion while an assistant uses it and retain the connection.

Addresses must be complete, without embedded username/password or extra fragments. Use existing UI validation wording.

## 4. Inputs and outputs

| Input | Output |
|---|---|
| Name, type, address, credential, default model | Saved connection or precise validation failure |
| Test | Connectivity and discovered-model count, or failure |
| Workbench usage request | Input/output/cache-read tokens, or not provided |

Credentials are not outputs; no page echoes saved credentials.

## 5. Dependencies

| Module | Relationship |
|---|---|
| Assistant | Uses its connection/model; replace a disabled or missing connection before saving |
| Workbench | Reads model name, connectivity, and usage; no duplicate credential storage |
| Prompt enhancement | Uses the current assistant connection; returns failure without task messages |

No messaging, skill-market, or online-conversion connection is added.

## 6. Potential failures

| Failure | Visible fact |
|---|---|
| Invalid/incomplete address | Cannot save; previous connection intact |
| Test fails | Remain in preparation; no task created; do not infer exhausted quota without evidence |
| Old failure still visible | Timestamp the record; do not treat it as the latest test |
| No credential ever saved | A credential-requiring service cannot connect; blank retention applies only to an existing credential |
| No usage returned | Workbench says not provided |
| Delete referenced connection | Deletion rejected |

## 7. Verification

| Case | Expected result |
|---|---|
| Inspect after saving | No credential displayed |
| Test invalid address | Connectivity failure in preparation, neither quota-exhaustion claim nor task started |
| Create assistant after successful test | Connection selectable |
| Provider omits usage | No zero monetary value |
| Delete referenced connection | Rejected; assistant still points to the original |

## 8. Assumptions and open questions

| ID | Detail |
|---|---|
| A4 | The app neither sells plans nor calculates bills |
| A8 | Communicate with the user-supplied endpoint; local models are selectable |
| Q-MC1 | Real-provider quality, speed, and charges are outside this module's acceptance |
