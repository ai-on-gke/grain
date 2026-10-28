# Task Intake, Inbox, and Lifecycle Operator Walkthrough Report

**Test Date:** 2026-09-30  
**Target Branch:** `grain/task-1224`  
**Commit Tested:** `329a5d34d8fe876c7c943bc430ebe57b1a2b0390`  
**Execution Environment:** Debian 12 (bookworm) amd64, Linux 6.1.0-53-amd64, Node.js v20.19.0, Go 1.26.6, Playwright / Chromium & WebKit (headless)  
**Server Under Test:** Live `grain demo` backend server over throwaway embedded SQLite store  
**Automated Specs:** `ui/e2e/task_intake_inbox_lifecycle_walkthrough.spec.js` (supported by `ui/e2e/inbox.spec.js`, `ui/e2e/tasks.spec.js`, `ui/e2e/task_detail_workflows.spec.js`, `ui/e2e/batch_actions_workflows.spec.js`, `ui/e2e/fields.spec.js`, `ui/e2e/narrow.spec.js`)  
**Stubs / Mocks:** None (executed against live backend binary, SQLite store, and Playwright Chromium/WebKit browsers)

---

## 1. Executive Summary

This test run rigorously exercises and validates the end-to-end operator workflow from incoming work through task disposition across the Grain application in a real browser against a real server.

Testing covered:
1. **Inbox & Triage:** Cold loading `/inbox` directly and navigating from the rail, group categorization (`Approve a proposal`, `Answer a question`, `Submit for merge`, `Read an answer`), counts, detail overlay navigation from inbox rows, proposal approval directly from the row, answering parked questions inline, and empty-state behavior.
2. **Global Task Intake:** Form validation (requiring title), draft discard/cancellation, queued and proposed task creation paths, repository selection (and "No repo"), base branch specification, auto-merge configuration, capabilities assignment, custom field entry (Priority, Area, Requested by, Estimate, Customer-facing), and advanced configuration options (prompt extension overrides, network access, sandbox shape resources: vCPUs, Memory, Disk).
3. **All Tasks & State Views:** Search filtering with real-time narrowing, URL parameter persistence (`?q=...`), full page reload persistence, rail state views (`Queued`, `Tasks`, `Blocked`), compound narrowing filters (`Priority = high`), sort dropdown application, and browser history navigation (`page.goBack()`, `page.goForward()`).
4. **Task Detail & Lifecycle:** In-place Title and Description editing, Base branch editing, custom fields in-place mutations (`Edit fields` -> `Save fields`), Auto-merge checkbox toggling in Actions, untimed and scheduled wake-date deferral lifecycles (`Defer` -> `Undefer`), proposal approval from detail overlay, prompt dialog inspection, chat message posting, and execution transcript / attempt log inspection in the modal monospace viewer.
5. **Bulk Operations:** Multi-row checkbox selection, floating batch actions counter (`2 selected`), bulk edit dialog cancellation preserving selection, and applying bulk field patches across multiple tasks while preserving unrelated custom fields.
6. **Resilience & Error Handling:** Deep-linking to non-existent task IDs (`/tasks/non-existent-task-id-99999`) with graceful fallback to the task list, and zero unhandled console errors or layout breaks.

All operator workflows, UI components, state transitions, and database mutations functioned cleanly with zero regressions.

---

### Verification Matrix

| Workflow Area | Key Interactions Tested | Result | Notes / Observed Behavior |
| :--- | :--- | :--- | :--- |
| **1. Inbox & Triage** | Group categorization (`Approve a proposal`, `Answer a question`, `Submit for merge`, `Read an answer`), counts, row navigation, proposal approval, parked question inline response, empty state | **PASS** | Seeded proposal ("Add a dark mode toggle") was approved directly from its inbox row and transitioned to `queued`. Seeded question ("Investigate the merge queue stall") was answered inline and transitioned to `queued`. Row navigation and empty states behaved correctly. |
| **2. Task Intake Drawer** | Title validation, draft cancellation / discard, queued task creation (repo, branch, auto-merge, capabilities, custom fields, advanced options), proposed task creation ("No repo") | **PASS** | Required title validation blocks submission when empty; "Discard draft" resets form inputs; queued and proposed tasks persist to SQLite store and render immediately with appropriate state badges and custom field chips. |
| **3. Search, Sort & Views** | Keyword search, rail state filtering (`Queued`), sort selection, compound narrowing filters (`Priority = high`), URL query sync, browser history traversal (Back/Forward), page refresh | **PASS** | Search and filter criteria synchronize to URL query parameters (`?q=...`, `?f=...`) and persist across browser reloads and history traversal. |
| **4. Task Detail & Lifecycle** | In-place Title/Description editing, Target repo & base branch editing, custom fields in-place mutations, auto-merge toggle, indefinite & timed deferrals, proposal approval, chat comments, Attempt transcript overlay | **PASS** | Title/description, base branch, auto-merge checkbox, and custom fields update in-place; deferral with wake date renders scheduled wake time; proposal transitions to `queued`; attempt transcripts render preformatted execution logs in modal. |
| **5. Bulk Operations** | Multi-row checkbox selection, floating batch actions bar counter, modal cancel (selection retention), bulk field patch | **PASS** | Floating batch bar accurately reflects selected task count; modal cancellation retains selections; bulk PATCH updates targeted tasks without clobbering other custom fields. |
| **6. Resilience & 404s** | Deep-linking to non-existent task ID (`/tasks/non-existent-task-id-99999`), zero unhandled console errors | **PASS** | Invalid task route falls back gracefully to the task list view without application errors or blank screens. |

---

## 2. Detailed Workflow Verification & Findings

### Phase 1: Inbox Triage and Parked Item Disposition

1. **Direct Navigation & Group Categorization**:
   - Navigated directly to `/inbox` on cold load.
   - Verified that all triage groups rendered with correct headings:
     - `Approve a proposal` (containing seeded proposal *"Add a dark mode toggle"*)
     - `Answer a question` (containing seeded parked question *"Investigate the merge queue stall"*)
     - `Submit for merge` (*"Cache the capability readiness probe"*)
     - `Read an answer` (*"Why did last week's nightly suite slow down?"*)
2. **Row Navigation & Rail Synchronization**:
   - Clicked the proposal item; the `DetailOverlay` slid open displaying proposal details.
   - Navigated back using `.overlay-pane-back button`, returning directly to `/inbox`.
   - Switched between `Tasks` and `Inbox` using navigation rail buttons, confirming view state synchronization.
3. **Seeded Proposal Approval**:
   - Clicked `Approve` on the seeded proposal row (*"Add a dark mode toggle"*).
   - Verified the item immediately left the inbox proposal group.
   - Verified on `Tasks` view that *"Add a dark mode toggle"* landed in `queued` state with `.badge-queued`.
4. **Seeded Parked Question Inline Reply**:
   - Returned to `/inbox` and clicked `Reply` on *"Investigate the merge queue stall"*.
   - Verified inline `.inbox-question` expanded displaying the agent's blocked question prompt.
   - Filled response (*"Fix both PRs and unblock the queue immediately."*) and clicked `Send`.
   - Verified the item unparked and left the inbox, transitioning to `queued` on the task list.

---

### Phase 2: Global Task Intake (New Task Drawer)

1. **Validation & Draft Cancellation**:
   - Opened `+ New task` drawer from the global rail button.
   - Confirmed `Create task` button remained disabled while `Title` was empty.
   - Entered draft text (*"Discardable draft task"*), clicked `Discard draft`, and verified all form inputs reset.
   - Closed drawer using `Close dialog`.
2. **Queued Task Creation with Extensive Fields**:
   - Opened `+ New task` drawer and filled:
     - **Title:** `E2E Queued Intake Task <timestamp>`
     - **Description:** `End-to-end task intake verification with extensive options and parameters.`
     - **Target Repo & Base Branch:** `acme/widgets` targeting `main`.
     - **Auto-merge:** Checked `Auto-merge once checks pass`.
     - **Custom Fields:** `Priority: high`, `Area: frontend`, `Requested by: OperatorTeam`, `Estimate (days): 4`, `Customer-facing: Yes`.
     - **Advanced Options:** Expanded accordion and added `Prompt extension override: Strictly verify all code style and types.`, configured sandbox shape resource fields (`vCPUs: 2`, `Memory: 4096`, `Disk: 20`).
     - **Queueing Mode:** Kept `Queue immediately` checked.
   - Submitted task; verified drawer closed and task appeared in list with `.badge-queued` and `Priority: high` chip.
3. **Proposed Task Creation ("No repo")**:
   - Opened `+ New task` drawer and filled:
     - **Title:** `E2E Proposed Architecture Task <timestamp>`
     - **Description:** `Unassigned architectural proposal for distributed queue design.`
     - **Repos:** Selected `No repo` (`none`).
     - **Queueing Mode:** Unchecked `Queue immediately`.
   - Submitted task; verified task landed with `.badge-proposed`.

---

### Phase 3: List Views, Filtering, Search, and URL Persistence

1. **Search & URL Persistence**:
   - Entered task title into the search input.
   - Verified list filtered instantly to 1 row and updated URL with `?q=...`.
   - Reloaded the page; verified search query and filtered list state persisted.
   - Cleared search input; list returned to full view.
2. **State Filtering via Rail**:
   - Selected `Queued` from the sidebar navigation rail.
   - Verified only queued tasks were displayed, excluding proposals and closed items.
3. **Sort Controls**:
   - Opened sort menu from `NarrowingBar` (`Choose the order this list is shown in`).
   - Applied sort key (`Priority`), confirmed sort key application and dropdown dismiss.
4. **Compound Narrowing Filters**:
   - Opened filter editor (`Add and edit the conditions this list is narrowed by`).
   - Added condition `Priority = high`.
   - Confirmed list filtered down to high-priority items and synchronized URL query parameters.
5. **Browser History & Clear**:
   - Executed `page.goBack()` and `page.goForward()`; confirmed filter criteria restored accurately.
   - Clicked `Clear` to restore default view state.

---

### Phase 4: Task Detail, Lifecycle Mutations, and Transcripts

1. **In-Place Title & Description Editing**:
   - Opened task detail overlay from row click.
   - Clicked `Edit`, updated title to `E2E Queued Intake Task (Updated)` and clicked `Save`.
   - Verified header title updated immediately.
2. **Target Repo & Base Branch In-Place Edit**:
   - Updated `Base branch` input to `release-v2` and pressed Enter.
   - Verified value persisted in the detail view.
3. **Custom Fields In-Place Edit**:
   - Clicked `Edit fields`, updated `Priority` to `medium` and `Requested by` to `CoreEngineering`, clicked `Save fields`.
   - Verified updated values rendered in declared fields section.
4. **Auto-Merge Toggling**:
   - Toggled `Auto-merge once checks pass` checkbox in the Actions section.
   - Verified checkbox state flipped and persisted immediately.
5. **Deferral Lifecycles (Untimed & Timed)**:
   - Clicked `Defer` without date -> verified state badge transitioned to `.badge-deferred`.
   - Clicked `Undefer` -> verified state badge restored to `.badge-queued`.
   - Filled wake date `2026-12-31` and clicked `Defer` -> verified scheduled wake indicator (*"Picks itself back up on 12/31/2026"*).
   - Clicked `Undefer` -> verified clean restoration to `queued`.
6. **Proposal Approval in Detail View**:
   - Opened proposed task in detail overlay.
   - Clicked `Approve` button; verified immediate transition from `.badge-proposed` to `.badge-queued`.
7. **Execution Transcripts & Monospace Logs**:
   - Opened running seeded task (*"Bump the Go toolchain to 1.24"*).
   - Clicked `Attempt #1 transcript` in timeline.
   - Verified `AttemptTranscriptOverlay` opened displaying preformatted monospace execution logs (`pre.logs-view`).
   - Closed modal cleanly via Escape key.

---

### Phase 5: Multi-Select Bulk Management

1. **Selection Counter & Floating Bar**:
   - Created two target tasks via API with `priority: low`.
   - Selected checkboxes for both tasks.
   - Verified floating `BatchActionsBar` appeared displaying `2 selected`.
2. **Modal Cancellation**:
   - Clicked `Bulk edit…` to open `BulkEditOverlay`.
   - Clicked `Cancel`; verified modal dismissed while both checkboxes remained checked and bar showed `2 selected`.
3. **Bulk Field Patch**:
   - Re-opened `Bulk edit…`.
   - Checked `Priority` and selected `high`.
   - Clicked `Apply to 2 tasks`.
   - Verified floating bar dismissed, both tasks updated on board with `Priority: high` chips, and independent custom fields (`requested-by`) remained intact upon GET inspection.

---

### Phase 6: Resilience and Error Recovery

1. **Non-Existent Task 404 Deep Link**:
   - Navigated directly to `/tasks/non-existent-task-id-99999`.
   - Verified UI gracefully defaulted to the task list without unhandled errors or blank page rendering.
2. **Console Error Invariants**:
   - Verified zero unhandled application exceptions throughout the entire test run.

---

## 3. Conclusion

The Grain operator workflows for task intake, inbox triage, task detail lifecycles, and bulk operations are robust, intuitive, and performant. All automated regression specs pass against the built frontend and live Go backend.
