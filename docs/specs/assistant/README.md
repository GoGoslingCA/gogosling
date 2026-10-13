# Module plan: Assistant

| Item | Description |
|---|---|
| Status | Draft |
| Date | 2026-10-12 |
| Module | The agent in Settings is the same object as the workbench assistant |

This plan covers the assistant module only. It does not change existing behavior or replace its rules with workbench rules.

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

Prepare an assistant that can start a task: a name, usable model, explicit approval mode, and one fixed workspace.

## 2. Responsibilities

| In scope | Out of scope |
|---|---|
| Create, edit, enable, disable, and delete assistants | A particular task's draft/result body |
| Select a model connection and model name | Validate credentials or connectivity |
| Configure approval mode | Decide a particular approval prompt |
| Select available local skills and tools | Skill markets or online installation |
| Explain deletion of tasks, projects, drafts, and result versions while retaining workspace files | Delete workspace files |

| Mode | Rule |
|---|---|
| Ask for everything | Ask before every operation that changes the outside world |
| Ask for risky actions | Ask before command execution, networking, sending messages, or deletion |
| Automatic in sandbox | Act within the allowed boundary; ask outside it |

Changing an assistant affects every task using it. The workbench must display this fact; this module stores the mode.

## 3. Key workflow

1. Without a usable model connection, direct the user to model connections rather than claiming the assistant is ready.
2. Enter a name, select connection/model and approval mode, then save.
3. Bind only installed, enabled local skills.
4. A disabled assistant is unavailable to new tasks. Existing-task continuation follows the notice shown when opening that task.
5. Explain deletion before confirmation. Remove its tasks, projects, drafts, and result versions; retain workspace files.

## 4. Inputs and outputs

| Input | Output |
|---|---|
| Name, description, model connection/name, approval mode, enabled flag | A selectable assistant or a precise missing-field notice |
| Confirmed deletion | Assistant and its tasks/projects/drafts/versions removed; workspace files retained |

The workbench reads name, model, workspace, approval mode, and enabled state. Truncated names must expose the full name.

Switching assistants restores that assistant's own unsent draft rather than carrying the previous assistant's text into it.

## 5. Dependencies

| Module | Relationship |
|---|---|
| Model connection | Must reference an existing connection; reject deletion or require reselection rather than retaining an empty reference |
| Skills and extensions | Bind installed/enabled items; mark absent items as missing rather than downloading |
| Workbench | Selects the assistant and displays its mode without changing it inside a task |
| Scheduled jobs | Disabling an assistant prompts reselection or disabling dependent agent jobs; scheduling rules remain unchanged |

## 6. Potential failures

| Failure | Visible fact |
|---|---|
| No model connection | An assistant ready to start cannot be saved; direct the user to configuration |
| Selected connection disabled/missing | Reject save and retain the existing assistant |
| Bound skill removed | Show not installed/removed rather than silently treating it as available |
| Deletion while content exists | Explain task/project/draft/version deletion and file retention |

## 7. Verification

| Case | Expected result |
|---|---|
| Create without a connection | Cannot start; configuration entry provided |
| Change mode, return to workbench | Display matches saved mode and explains its effect on other tasks |
| Delete assistant | Related tasks cannot continue; original workspace files remain |
| Missing skill | Missing state shown without a market-install action |
| Switch with unsent text | New assistant's input does not contain the previous assistant's sentence |

## 8. Assumptions and open questions

| ID | Detail |
|---|---|
| A5 | Assistant and agent are the same object |
| Q-AG1 | Whether disabling an assistant immediately blocks sending in an already-open task remains unverified; follow the task-opening notice without inventing a state |
