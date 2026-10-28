# Operator Review: UI Changes Between v1.7.0 and Candidate

**Date:** 2026-10-10  
**Base Release:** `v1.7.0` (commit `a9469b26f3be2d37b4b781a4048d6640973d3663`, tagged `v1.7.0`, `latest`)  
**Target Candidate:** `origin/candidate` (commit `fa12e18b31597c5f2b0a148db117c34aa1263646`, candidate tip)  
**Evaluation Scope:** 946 commits across core UI components, admin workflows, team scoping, work tree visualization, and system health surfaces.  
**Test Environment:** Live `grain demo` server (`/tmp/grain demo -addr 127.0.0.1:0 -open=false -merge-rules -merge-queue-states -sensitive-hold -mechanical-port -observability`) driven via Playwright Chromium (1400x900 viewport).

---

## 1. Executive Summary

This document presents a comprehensive visual and operational walkthrough of all user-facing interface changes introduced between release `v1.7.0` and the current release candidate.

During this cycle, major additions and enhancements were made across 7 primary functional domains:
1. **System Identity & Administration:** Admin Mode toggle & protection banner, version string display in user settings, and remote rendezvous relay configuration.
2. **System Health & Network Resiliency Banners:** Disconnected auto-retry countdown banner, loopback trust security warning banner, and state repository sync failure alerts.
3. **Work Trees & Task Lifecycle:** Hierarchical task origin work tree visualizer in task detail, work tree board view, unsaved changes dirty guards, and read-only protections for active tasks.
4. **Team Settings, Git Identity & Multi-Host Forges:** Typed Git credentials (PAT, SSH Key, GitHub App, None), multi-host forge URL support (`ghe.internal.acme/org/repo`), credential reachability testing, and bulk-add team member onboarding.
5. **Granular Team Dispatch Control & Roles:** Team-scoped pipeline pause controls with dedicated pause banner, and the new `proposer` team role.
6. **Task Intake & Execution Controls:** Searchable combobox model selectors with real-time filtering, path-scoped merge policy displays, and declared push set indicators.
7. **Releases, Merges & Notifications:** Release and candidate deletion lifecycles with safety confirmation modals, repository merge rules status indicators, and inbox desktop notifications.

---

## 2. Visual Walkthrough & Component Analysis

### 2.1 System Identity & Administration

#### Feature 1: Version Display in User Settings
- **Component:** `ui/src/components/SettingsOverlay.jsx` (`[data-testid="settings-version-display"]`)
- **Commits:** `7f7564082`, `22188ff6e`
- **Visual Evidence:**
  ![Version Display in User Settings](2026-10-10-candidate-ui-changes/01-version-display-settings.png)
- **Before (`v1.7.0`):** The Settings overlay displayed user preference options (actor, timezone, theme) with no indication of the currently running server binary version, build date, or commit SHA. Operators diagnosing issues had to query the daemon binary via CLI.
- **After (Candidate):** The header of the Settings dialog prominently renders the active release version, commit hash, and build timestamp (e.g., `v1.8.0-rc.1 · fa12e18b · 2026-10-10`).
- **Operator Impact:** Fast, unambiguous confirmation of the exact build running in staging or production during triage, without needing SSH or shell access.

---

#### Feature 2: Admin Mode Switch & Standing Banner
- **Component:** `ui/src/components/AdminModeBanner.jsx`, `ui/src/components/Sidebar.jsx`
- **Commits:** `5487e358e`, `b0be5d41c`, `cbe71f2f0`
- **Visual Evidence:**
  ![Admin Mode Switch and Banner](2026-10-10-candidate-ui-changes/02-admin-mode-banner.png)
- **Before (`v1.7.0`):** System administrators always operated with full privileges enabled at all times. High-risk administrative actions (deleting teams, clearing caches, reconfiguring runner sandboxes) were directly exposed alongside everyday actions, increasing the risk of accidental configuration changes.
- **After (Candidate):** System admins operate as standard users by default. An explicit "Admin mode" switch in the sidebar toggles elevated privileges on demand. When active, a prominent amber top banner warns: *"Admin mode is on. System administration actions are enabled."*
- **Operator Impact:** Protects against accidental destructive actions and provides continuous visual awareness whenever elevated privileges are enabled.

---

#### Feature 3: Rendezvous Relay Settings Panel
- **Component:** `ui/src/components/RelayPanel.jsx`, `ui/src/components/AdminOverlay.jsx`
- **Commits:** `d55f5a274`, `7f03fa1ee`
- **Visual Evidence:**
  ![Admin Relay Settings](2026-10-10-candidate-ui-changes/14-admin-relay-settings.png)
- **Before (`v1.7.0`):** Remote daemon tunneling and relay settings could only be configured through CLI arguments or daemon startup flags.
- **After (Candidate):** System administrators can view and manage the Rendezvous Relay connection in **Admin ▸ Access & Accounts ▸ Relay**. The interface provides fields for Relay Connection URL, daemon token, User IAP JWT audience, and real-time connection status verification.
- **Operator Impact:** Operators can manage secure remote agent and UI access through Cloud Run relays directly from the web interface.

---

### 2.2 System Health & Network Resiliency Banners

#### Feature 4: Disconnected Auto-Retry Banner
- **Component:** `ui/src/components/DisconnectedBanner.jsx` (`[data-testid="disconnected-banner"]`)
- **Commits:** `8864baf1e`, `800fbc14f`
- **Visual Evidence:**
  ![Disconnected Banner](2026-10-10-candidate-ui-changes/17-disconnected-banner.png)
- **Before (`v1.7.0`):** Connection drops produced transient error toasts or failed fetch notifications, leaving users unsure whether the page was attempting reconnection or if a hard browser refresh was required.
- **After (Candidate):** A full-width sticky banner appears immediately upon connection interruption: *"Can't reach grain. Retrying in Xs"*, featuring an active countdown timer and an immediate **Retry now** button.
- **Operator Impact:** Transparent connection state during daemon restarts or network glitches, preventing unnecessary manual refreshes and lost form state.

---

#### Feature 5: State Sync Failing Banner & Loopback Trust Warning
- **Component:** `ui/src/components/StateSyncFailingBanner.jsx`, `ui/src/components/LoopbackTrustBanner.jsx`
- **Commits:** `0c2d1fce3`, `cbe71f2f0`
- **Visual Evidence:**
  ![State Sync and Loopback Banners](2026-10-10-candidate-ui-changes/16-state-sync-and-loopback-banners.png)
- **Before (`v1.7.0`):** Failed settings exports to the upstream git state repository failed silently in daemon logs, risking database overwrites on subsequent daemon restarts. Additionally, loopback-authenticated daemons gave no visual cue that anyone reaching localhost had administrative privileges.
- **After (Candidate):**
  - **State Sync Failing Banner:** Warns: *"Settings have not been exported to [repo] since [time]: [error]. Changes made in the UI since then exist only in this deployment's database until the sync is fixed."*
  - **Loopback Trust Banner:** Alerts administrators when `-ui-trust-loopback=true` is enabled on multi-user deployments.
- **Operator Impact:** Early detection of infrastructure configuration discrepancies and security perimeter misconfigurations before data divergence occurs.

---

### 2.3 Work Trees & Task Lifecycle

#### Feature 6: Work Tree Visualization on Task Detail
- **Component:** `ui/src/components/detail/WorkTree.jsx`, `ui/src/components/TaskTree.jsx`
- **Commits:** `8fa57d578`, `2566ec484`, `3d33ef4a7`
- **Visual Evidence:**
  ![Work Tree on Task Detail](2026-10-10-candidate-ui-changes/03-work-tree-task-detail.png)
- **Before (`v1.7.0`):** Parent tasks, child subtasks, and director sessions were presented only as flat task lists. Operators had to inspect individual task descriptions to trace origins or follow subtask trees.
- **After (Candidate):** A dedicated **Work tree** section in the task detail pane renders the full hierarchical tree of tasks. It displays the origin session, nested subtasks, completion progress (e.g., *"Work from task 1: 0 of 4 done"*), current lifecycle statuses, and direct links to generated GitHub pull requests (e.g., `acme/widgets#104`).
- **Operator Impact:** Operators can evaluate overall progress of complex multi-agent workflows at a glance.

---

#### Feature 7: Work Tree Board View
- **Component:** `ui/src/components/TaskTreeBoard.jsx`
- **Commits:** `eef734baf`, `91b56b3e7`
- **Visual Evidence:**
  ![Work Tree Board View](2026-10-10-candidate-ui-changes/04-work-tree-board.png)
- **Before (`v1.7.0`):** Boards supported standard kanban, list, and gantt projections, but lacked hierarchical organization by task ancestry.
- **After (Candidate):** The new **Tree** board view groups and nests tasks by filing origin and subtask lineage across the entire workspace.
- **Operator Impact:** High-level project-wide visibility into parent workflows, delegations, and dependent task executions.

---

#### Feature 8: Unsaved Changes Guard in Task Edit
- **Component:** `ui/src/components/detail/EditTaskForm.jsx`, `ui/src/components/detail/CustomFieldsSection.jsx`
- **Commits:** `131c8f003`, `8dd9070c8`
- **Visual Evidence:**
  ![Unsaved Changes in Task Edit](2026-10-10-candidate-ui-changes/15-task-edit-dirty.png)
- **Before (`v1.7.0`):** Clicking outside an active task edit form or pressing Escape immediately dismissed the overlay, silently discarding all unsaved changes to task titles, descriptions, and custom fields.
- **After (Candidate):** Modifying any field sets a dirty state. Attempting to close the form via Cancel, backdrop click, Escape, or browser navigation triggers an explicit confirmation dialog asking whether to discard unsaved modifications.
- **Operator Impact:** Eliminates accidental loss of long task prompts, reproduction steps, or custom metadata during review.

---

### 2.4 Team Settings, Git Identity & Multi-Host Forges

#### Feature 9: Typed Git Credentials & Multi-Host Forges
- **Component:** `ui/src/components/TeamGitPanel.jsx`
- **Commits:** `b7e86f787`, `3c0b66dea`, `3273e970b`
- **Visual Evidence:**
  ![Team Git Connections](2026-10-10-candidate-ui-changes/05-team-git-connections.png)
- **Before (`v1.7.0`):** Team git credentials only supported a single default GitHub token. Enterprise GitHub or secondary git hosts required global daemon-level credentials.
- **After (Candidate):** Team Settings ▸ Git Identity supports typed credentials:
  - **Personal Access Token (PAT)**
  - **SSH Private Key**
  - **GitHub App**
  - **None (Read-only anonymous)**
  It also allows mapping custom forge hosts (e.g., `ghe.internal.acme`) scoped to specific organizations (`acme-org`).
- **Operator Impact:** Seamless multi-tenant enterprise configurations without leaking global credentials across teams.

---

#### Feature 10: Git Credential Reachability Testing
- **Component:** `ui/src/components/TeamGitPanel.jsx`
- **Commits:** `9e6a5066f`, `41d9df348`
- **Visual Evidence:**
  ![Git Connection Test](2026-10-10-candidate-ui-changes/06-team-git-connection-test.png)
- **Before (`v1.7.0`):** Operators had no mechanism within the UI to verify whether configured tokens or SSH keys were valid until an agent task attempted a git clone or push and failed in CI.
- **After (Candidate):** Each configured credential connection includes an interactive **Test** button. Clicking it executes an immediate check against the target forge API and displays reachability status and token validity. Non-admin viewers are granted read-only visibility with test and edit controls safely hidden.
- **Operator Impact:** Immediate validation during credential onboarding, eliminating task failures caused by expired tokens or improper OAuth scopes.

---

#### Feature 11: Team Members Bulk-Add Modal
- **Component:** `ui/src/components/TeamMembersBulkAdd.jsx`, `ui/src/components/TeamMembersPanel.jsx`
- **Commits:** `5fb884a53`, `202a0a2df`
- **Visual Evidence:**
  ![Team Members Bulk Add](2026-10-10-candidate-ui-changes/07-team-members-bulk-add.png)
- **Before (`v1.7.0`):** Adding team members required entering usernames one-by-one through individual input fields.
- **After (Candidate):** The **Add several at once** action opens a multiline paste modal. Operators can paste raw member rosters copied directly from Google Groups, mailing lists, or LDAP exports (handling names, email addresses, duplicates, and role assignments automatically in one operation).
- **Operator Impact:** Dramatically accelerates team onboarding and member management for enterprise deployments.

---

#### Feature 12: Team Dispatch Pause Control & Banner
- **Component:** `ui/src/components/TeamPauseBanner.jsx`
- **Commits:** `ba57807bf`, `2467d58a1`
- **Visual Evidence:**
  ![Team Dispatch Pause Banner](2026-10-10-candidate-ui-changes/08-team-dispatch-pause.png)
- **Before (`v1.7.0`):** Pausing dispatch was an all-or-nothing deployment-wide action that halted work across every team.
- **After (Candidate):** Team administrators can pause task dispatch for their specific team independently. When paused, a dedicated top banner alerts: *"Team default paused: none of its tasks are being picked up. Other teams keep dispatching, and runs already in flight finish."*
- **Operator Impact:** Enables targeted maintenance, credential rotation, or quota mitigation for a single team without disrupting active pipelines elsewhere.

---

### 2.5 Task Intake & Execution Controls

#### Feature 13: Searchable Model Pickers
- **Component:** `ui/src/components/ModelSelect.jsx`, `ui/src/components/ModelPicker.jsx`
- **Commits:** `ef877ca0b`, `ea1da7310`
- **Visual Evidence:**
  ![Searchable Model Picker](2026-10-10-candidate-ui-changes/09-model-picker-searchable.png)
- **Before (`v1.7.0`):** Model selection used standard HTML select dropdowns. As backend models and provider aliases expanded, navigating long lists became cumbersome.
- **After (Candidate):** Model selectors across New Task, Task Edit, and Team Defaults feature searchable comboboxes. Typing filters the model list instantly, displaying provider identifiers, context windows, and cost tiers.
- **Operator Impact:** Faster model selection and elimination of selection errors across large multi-provider model fleets.

---

#### Feature 14: Task Merge Policy & Sensitive Path Rules
- **Component:** `ui/src/components/detail/MergePolicy.jsx`, `ui/src/components/detail/Declared.jsx`
- **Commits:** `aed6fbdc4`, `283384c87`
- **Visual Evidence:**
  ![Task Merge Policy](2026-10-10-candidate-ui-changes/10-merge-policy-task-pane.png)
- **Before (`v1.7.0`):** Merge rules and path policies existed solely within repository YAML configurations (`.grain/merge-rules.yml`). Operators reviewing tasks had no visual indication of required review tiers or path triggers.
- **After (Candidate):** The task detail pane renders an explicit **Merge policy** section. It highlights whether changes touch sensitive paths, indicates required review thresholds, and notes any holds on automatic merges.
- **Operator Impact:** Transparent policy enforcement and clear expectations before tasks reach the merge queue.

---

### 2.6 Releases, Declarations & Inbox

#### Feature 15: Release & Candidate Deletion Lifecycle
- **Component:** `ui/src/components/RepoReleases.jsx`
- **Commits:** `ed99d7879`, `37207c433`
- **Visual Evidence:**
  ![Repo Releases and Deletion](2026-10-10-candidate-ui-changes/11-repo-releases-deletion.png)
- **Before (`v1.7.0`):** Creating a release candidate was permanent within the UI. Abandoned releases or faulty candidates could only be pruned via direct database/git intervention.
- **After (Candidate):** The Releases view for each repository provides dedicated **Delete release** and **Delete candidate** controls with safety confirmations, permitting clean removal and name reuse.
- **Operator Impact:** Safe, standard release hygiene and straightforward recovery from discarded release branches.

---

#### Feature 16: Repository Declarations & Merge Rules Status
- **Component:** `ui/src/components/RepoPage.jsx`
- **Commits:** `043d39d65`, `cc2ded94d`
- **Visual Evidence:**
  ![Repository Declarations Status](2026-10-10-candidate-ui-changes/12-repo-declarations-status.png)
- **Before (`v1.7.0`):** Repository configuration errors or invalid merge policy definitions failed silently during merge evaluations.
- **After (Candidate):** The Repository overview displays active declaration status cards, alerting operators immediately to syntax errors or policy warnings in `.grain/merge-rules.yml`.
- **Operator Impact:** Immediate visual feedback when repository governance files become invalid or desynchronized.

---

#### Feature 17: Inbox Desktop Notifications & GitHub Approval Tracking
- **Component:** `ui/src/components/InboxPage.jsx`
- **Commits:** `017527a85`, `ca3c79074`
- **Visual Evidence:**
  ![Inbox Notifications and Approvals](2026-10-10-candidate-ui-changes/13-inbox-notifications.png)
- **Before (`v1.7.0`):** The Inbox only showed tasks requiring operator action within grain, lacking browser desktop notifications and visibility into pull requests stalled on upstream GitHub approvals.
- **After (Candidate):** The Inbox includes a native desktop notifications toggle button and a dedicated section tracking **Pull requests waiting on GitHub approval**.
- **Operator Impact:** Faster response times for tasks requiring operator intervention, preventing PRs from stalling unnoticed.

---

## 3. Operator Review Checklist & Sign-off

| Area | Feature | Operational Impact | Recommended Verification Step | Status |
| :--- | :--- | :--- | :--- | :--- |
| **Admin** | Version Display | Clear build identification | Open Settings overlay; verify version SHA matches deployment. | Verified |
| **Admin** | Admin Mode Switch | Guardrails on system actions | Verify banner appears when switch is toggled; check admin tabs. | Verified |
| **Admin** | Relay Settings | Remote tunnel configuration | Check Admin ▸ Access ▸ Relay panel for connection status. | Verified |
| **Health** | Disconnected Banner | Resilient auto-retry | Stop daemon; observe countdown banner; click "Retry now". | Verified |
| **Health** | State Sync Alert | Upstream sync failure visibility | Review standing banner if git state repo export encounters error. | Verified |
| **Health** | Loopback Warning | Insecure loopback warning | Verify banner if running with `-ui-trust-loopback=true`. | Verified |
| **Work Trees** | Task Work Tree | Ancestry & PR traceability | Open parent task; inspect subtask completion count and PR links. | Verified |
| **Work Trees** | Work Tree Board | Workspace-wide hierarchy | Navigate to Boards ▸ Tree view to review project hierarchies. | Verified |
| **Work Trees** | Unsaved Guard | Prevents data loss | Modify task description; press Escape; verify prompt blocks exit. | Verified |
| **Teams** | Typed Git Credentials | Multi-host forge isolation | Open Team Settings ▸ Git; configure PAT or SSH key for forge. | Verified |
| **Teams** | Connection Test | Live credential reachability | Click "Test" button on Git credential; verify reach verdict. | Verified |
| **Teams** | Bulk Member Add | Batch member onboarding | Paste member list in "Add several at once" modal; confirm preview. | Verified |
| **Teams** | Team Dispatch Pause | Scoped maintenance pause | Trigger team pause; verify other teams continue dispatching. | Verified |
| **Intake** | Model Search | Quick model filtering | Open New Task ▸ Advanced; type model name in combobox. | Verified |
| **Intake** | Merge Policy | Policy transparency | Check task detail pane for sensitive path & approval badges. | Verified |
| **Releases** | Release Deletion | Release candidate lifecycle | Create test release candidate; click "Delete release". | Verified |
| **Inbox** | Desktop Notifications | Timely approval notifications | Toggle desktop notifications; check PR approval tracking group. | Verified |
