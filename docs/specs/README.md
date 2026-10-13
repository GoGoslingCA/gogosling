# gogosling local module plans

| Item | Description |
|---|---|
| Status | Draft |
| Date | 2026-10-12 |
| Audience | Product, design, and acceptance review |
| Product | An open-source workbench for one person working on their own computer |
| Authoritative detail | Complete requirements, stories, and acceptance criteria are in [the personal workbench PRD](../prd/personal-workbench.md), draft v0.5. Resolve wording differences here by aligning this directory with that PRD, rather than creating another rule set |
| User guide | [Personal workbench guide](../user/personal-workbench.md) |

This directory contains draft product requirements, not a release checklist. It does not remove existing features or override the implemented permission model. See the user guide for current workflows; proposed exclusions below apply only to this local-edition plan.

---

## Outline

1. Project goal
2. Local edition boundaries
3. Module map
4. Module handoffs
5. Specification standards

---

## 1. Project goal

A person can open the app, bring materials into a task, see its actual progress, save a result as a reopenable version, and separately confirm write-back to one text file on a supported system. Returning later exposes drafts, versions, and runtime state for review. The app does not resume execution automatically.

Usage displays only token counts actually supplied by the model service. Missing counts are shown as not provided.

## 2. Local edition boundaries

The application's own networking capabilities are outside these module plans. The only additional external navigation entry proposed here is [the mobile app](mobile-link/README.md): a browser link, without login, synchronization, or uploads. The official URL has not been supplied; hide the link until it is available.

These capabilities are excluded from this directory and have no specification folders here:

| Capability | Planning decision |
|---|---|
| Messaging connections | Excluded from workbench navigation/settings in this plan; their independent rules are not rewritten here |
| Team repositories and knowledge | Covered by the separate team workflow; absent from the default local flow |
| Cloud assistants, cloud continuation, account synchronization, online conversion, sharing links, skill markets, and web retrieval | No entry points planned |

[Model connections](model-connection/README.md) still cover endpoints supplied by the user. These are the user's chosen services, not an application-provided cloud.

## 3. Module map

| Module | Specification | Purpose |
|---|---|---|
| Personal workbench | [personal-workbench](personal-workbench/README.md) | Start tasks, organize projects, retain results, and identify where work stopped |
| Assistant | [assistant](assistant/README.md) | Select a model, approval mode, workspace, and available skills |
| Model connection | [model-connection](model-connection/README.md) | Save and test the user's model service |
| Approval | [approval](approval/README.md) | Explain the task, action, and target before a person decides |
| Scheduled jobs | [schedule](schedule/README.md) | Run on this computer at the scheduled time, without posting to other software |
| Skills and extensions | [extensions](extensions/README.md) | Use skills, services, and plugins already installed on this computer |
| Activity records | [audit](audit/README.md) | Review past operations and decisions chronologically |
| Desktop | [desktop](desktop/README.md) | Window, tray, and unsaved-content handling on exit |
| Mobile app | [mobile-link](mobile-link/README.md) | Proposed link to the official mobile-app page |

## 4. Module handoffs

The workbench hands a specific request to a neighboring module and displays the returned result. If a result cannot be confirmed, show Unknown rather than success or zero.

If one stage has a confirmed result and the next has none or is unknown, record the issue at that handoff. See [workbench troubleshooting](personal-workbench/troubleshooting.md).

| From | To | Sent | Required workbench display |
|---|---|---|---|
| Workbench | Model connection | Connection used by the current assistant | Model name; missing usage shown as not provided |
| Workbench | Assistant | Selected assistant | Name, model, workspace, and approval mode |
| Workbench | Approval | Owning task, action, and target | Approved, denied, or still waiting; never decide when task ownership is unclear |
| Workbench | Scheduled jobs | No task body; only shared execution capacity | Whether scheduled work occupies capacity or the current task is waiting |
| Workbench | Mobile app | Open-link action | Desktop tasks remain intact if opening fails |
| Workbench | Messaging connections | No handoff | No channel-failure notification |

## 5. Specification standards

Each module begins with an outline and covers goal, responsibilities, workflow, inputs/outputs, dependencies, detectable failures, verification, and assumptions/open questions.

- Use product terms: assistant, task, material, project, result, version, copy, file change, and mobile app. An agent in Settings is the same object as an assistant in the workbench.
- Use one label per state: not started, queued, processing, awaiting approval, completed, incomplete, stopped, recovering, or unknown.
- A failure explains where it stopped, what was retained, what did not happen, and one next action.
- Do not invent quantities, URLs, or effects. Put missing facts under open questions.
- Do not prescribe implementation or change another module's rules to satisfy an acceptance case.
