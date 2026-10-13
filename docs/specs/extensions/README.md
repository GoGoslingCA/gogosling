# Module plan: Skills and extensions

| Item | Description |
|---|---|
| Status | Draft |
| Date | 2026-10-12 |
| Module | Skills and extensions |

Manage skills, local services, and plugins already on this computer. No market or one-click online installation is provided.

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

Install, enable, or remove local skills/extensions, showing enabled state and tool-declaration changes before assistant use.

## 2. Responsibilities

| In scope | Out of scope |
|---|---|
| Install skills/plugins from a local directory | Browse/search a community market |
| Register a local extension service | Workbench web-retrieval entry |
| Enable, disable, uninstall | Delete result files during uninstall |
| Require confirmation after tool declarations change | Silently enable unconfirmed tools |
| Store credential references without plaintext display | Provision cloud accounts |

## 3. Key workflow

1. Select and install a local skill/plugin directory.
2. For a service, supply display name and local startup method; use credential references without plaintext in descriptions.
3. An assistant may bind it only after enablement.
4. Changed tool declarations remain unavailable pending confirmation.
5. Name the item before uninstalling; assistant bindings then show not installed/removed.

## 4. Inputs and outputs

| Input | Output |
|---|---|
| Local directory | Installed item or a clear failure reason |
| Enable/disable | Assistant binding availability |
| Tool-declaration change | Tools unavailable until confirmed |
| Confirm uninstall | Item removed from list; existing result files retained |

## 5. Dependencies

| Module | Relationship |
|---|---|
| Assistant | Binds installed/enabled items |
| Approval | External side effects still require normal approval; this module grants no separate bypass |
| Workbench | Do not advertise nonexistent shortcuts in start-page placeholders |

Whether user-registered remote service addresses count as excluded networking remains open. This plan neither recommends nor bans them.

## 6. Potential failures

| Failure | Visible fact |
|---|---|
| Required directory instructions absent | Installation fails rather than creating an empty skill |
| Service disconnected | Show disconnected/latest error; assistant cannot treat it as available |
| Tool declarations changed | Unavailable to assistant before confirmation |
| Plaintext credential entered | Require a reference; never echo saved values |

## 7. Verification

| Case | Expected result |
|---|---|
| Install local skill | Item appears without a market entry |
| Bind before enablement | Binding rejected or no enabled extensions shown |
| Change tool declarations | Assistant cannot use new declarations before confirmation |
| Uninstall | Binding marked missing; workspace files remain |

## 8. Assumptions and open questions

| ID | Detail |
|---|---|
| Q-EX1 | Whether a user-registered remote extension is application networking remains unresolved. No workbench entry or new allow/deny rule is added |
