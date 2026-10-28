# Operator Walkthrough: Task Intake, Inbox, and Lifecycle Management

**Test Date:** 2026-09-24  
**Target Commit:** `dcfb5a3e4d559fd6d63289dda4291c88f790e2a0` (`grain/task-491`)  
**Execution Environment:** Debian 12 (bookworm) amd64, Linux 6.1.0-53-amd64, Node.js v20.19.0, Go 1.26.6, Playwright / Chromium (headless)  
**Server Under Test:** `grain demo` embedded server and SQLite store  
**Automated Spec:** `ui/e2e/task_intake_inbox_lifecycle_walkthrough.spec.js`  
**Stubs / Mocks:** None (executed against real backend binary, real SQLite store, and real Chromium browser)

---

## 1. Executive Summary

This report documents a comprehensive operator walkthrough covering task intake, inbox triage, discovery, lifecycle progression, detail management, and bulk operations in the Grain web interface. The testing was conducted in an actual Playwright-driven Chromium browser interacting directly with the built React frontend served by a live `grain demo` server over an embedded SQLite store.

All operator workflows, user interactions, responsive controls, and edge cases completed successfully with zero functional regressions, layout defects, or unhandled console/network errors.

### Summary Verification Matrix

| Workflow Area | Core Operations Tested | Verification Result | Observations |
| :--- | :--- | :--- | :--- |
| **1. Inbox & Triage** | Group categorization, counts, row inspection, proposal approval, parked question reply, merge submit, ack, empty state | **PASS** | Inbox groups accurately categorize awaiting actions (`Approve a proposal`, `Answer a question`, `Submit for merge`, `Read an answer`). Transitions cleanly remove processed rows and render the empty state when cleared. |
| **2. Task Intake** | Form validation, discard draft, queued task creation (repo + branch + custom fields + prompt override), proposed creation ("No repo") | **PASS** | Title required validation prevents premature submission; "Discard draft" restores pristine initial state; queued and proposed tasks land on server and appear immediately in lists. |
| **3. List, Filter & Search** | Keyword search, rail state filtering, compound multi-criteria narrowing, URL sync, Back/Forward browser history | **PASS** | URL query parameters (`?q=...`, `?f=...`) stay in sync with UI filters; hard refresh and history traversal reliably reconstruct exact filtering state. |
| **4. Task Detail & Lifecycle** | In-place Title/Desc editing, custom field mutations, Defer (indefinite + timed wake date), Undefer, Proposal approval, Attempt transcripts | **PASS** | Real-time state badge updates; wake date persists and renders friendly resume hints; Attempt transcript drawer cleanly streams agent execution transcripts. |
| **5. Bulk Operations** | Multi-row checkbox selection, floating batch actions bar counter, modal cancel (selection retention), bulk field patch | **PASS** | Multi-selection floating bar accurately tracks selection count; modal cancellation preserves checked rows; batch PATCH updates all selected items without clobbering unmentioned fields. |
| **6. Resilience & 404s** | Non-existent task ID deep-linking (`/tasks/non-existent-task-id-99999`) | **PASS** | UI gracefully falls back to task list without application crash or blank screen. |

---

## 2. Detailed Phase Walkthroughs & Test Evidence

### Phase 1: Inbox Triage and Disposition

1. **Direct Navigation & Layout Verification**:
   - Navigated directly to `/inbox`.
   - Verified that the page heading "Inbox" renders alongside four populated triage groups:
     - `Approve a proposal` (containing *"Add a dark mode toggle"*)
     - `Answer a question` (containing *"Investigate the merge queue stall"*)
     - `Submit for merge` (containing *"Cache the capability readiness probe"*)
     - `Read an answer` (containing *"Why did last week's nightly suite slow down?"*)
2. **Row Navigation**:
   - Clicked *"Add a dark mode toggle"* from the proposal group.
   - DetailOverlay drawer slid out displaying the full proposal details (`.detail-header h2`).
   - Clicked `.overlay-pane-back button` to return seamlessly to `/inbox`.
3. **Rail Navigation**:
   - Navigated between `All tasks` and `Inbox` via sidebar rail buttons, confirming route and view synchronization.
4. **Action Disposition**:
   - **Approve Proposal**: Clicked `Approve` on the proposal row. The task immediately transitioned out of the inbox into the queued state.
   - **Answer Question**: Clicked `Reply` on the parked question row. Expanded inline `.inbox-question` prompt box, entered operator response (*"Fix both PRs and unblock the queue immediately."*), and clicked `Send`. The question unparked and left the inbox.
   - **Submit for Merge & Acknowledge**: Submitted the completed PR task for merge and acknowledged the completed investigation task.
5. **Empty State**:
   - Verified that upon clearing all pending triage items, the Inbox displays the friendly empty state:
     > *"Nothing is waiting on you. Every task is either running, queued or done."*

---

### Phase 2: Global Task Intake (New Task Drawer)

1. **Validation & State Enforcement**:
   - Opened the `+ New task` side panel.
   - Verified `Create task` button is disabled when `Title` is empty.
2. **Discard Draft**:
   - Populated the form with draft text (*"Discardable draft task"*), clicked `Discard draft`, and verified all inputs reset. Closed drawer using `Close dialog`.
3. **Queued Task Creation with Full Options**:
   - Opened `+ New task` drawer and filled:
     - **Title:** `E2E Queued Intake Task <timestamp>`
     - **Description:** `End-to-end task intake verification with extensive options and parameters.`
     - **Target Repo & Branch:** `acme/widgets` targeting `main`.
     - **Custom Fields:** `Priority: high`, `Area: frontend`, `Requested by: OperatorTeam`, `Estimate (days): 4`, `Customer-facing: Yes`.
     - **Advanced Options:** Expanded accordion and added `Prompt extension override: Strictly verify all code style and types.`.
   - Submitted task with default `Queue immediately` checked.
   - Verified the task appeared immediately in `All tasks` with `.badge-queued` and `Priority: high` chip.
4. **Proposed Task Creation ("No repo")**:
   - Opened `+ New task` drawer and filled:
     - **Title:** `E2E Proposed Architecture Task <timestamp>`
     - **Description:** `Unassigned architectural proposal for distributed queue design.`
     - **Repos:** Selected `No repo` (`none`).
     - **Queueing Mode:** Unchecked `Queue immediately` to file as a proposal.
   - Verified the task landed in the task store with `.badge-proposed`.

---

### Phase 3: List Views, Filtering, Search, and URL Persistence

1. **Search Filtering**:
   - Entered the unique task title into the search input.
   - List dynamically filtered down to exactly 1 matching task.
   - URL updated to reflect `?q=...`.
   - Executed a full browser reload; verified search query and single-row list state persisted.
2. **Rail State Filter**:
   - Clicked the `Queued` button on the navigation rail.
   - Verified only tasks in the `queued` state displayed, excluding proposals.
3. **Compound Narrowing Filter**:
   - Opened the NarrowingBar filter editor (`Add and edit the conditions this list is narrowed by`).
   - Added filter condition `Priority = high`.
   - Confirmed list filtered correctly to high-priority items.
4. **Browser History**:
   - Executed `page.goBack()` and `page.goForward()`. Confirmed filters and selection states restored cleanly.

---

### Phase 4: Task Detail & Lifecycle Transitions

1. **Inline Metadata Editing**:
   - Opened task detail for the queued task.
   - Clicked `Edit`, modified title to `<Title> (Updated)`, and saved. Header updated in place without page refresh.
2. **Custom Fields Mutation**:
   - Clicked `Edit fields`, changed `Priority` from `high` to `medium`, and updated `Requested by` to `CoreEngineering`.
   - Confirmed updated values reflected in `.declared-row`.
3. **Indefinite Deferral & Undeferral**:
   - Clicked `Defer` with no wake date.
   - Task state transitioned to `deferred` (`.badge-deferred`).
   - Clicked `Undefer`. Task returned immediately to `queued` (`.badge-queued`).
4. **Timed Deferral with Wake Date**:
   - Set wake date (`Until (optional)`) to `2026-12-31` and clicked `Defer`.
   - Verified hint text rendered: `Picks itself back up on Dec 31, 2026`.
   - Clicked `Undefer` to resume task.
5. **Proposal Approval in Detail View**:
   - Opened the unassigned proposed task.
   - Verified initial state `.badge-proposed`.
   - Clicked `Approve` button in header. State transitioned directly to `.badge-queued`.
6. **Timeline & Transcript Inspection**:
   - Opened running task *"Bump the Go toolchain to 1.24"*.
   - Clicked `Attempt #1` in the timeline list (`.timeline-item-attempt`).
   - Verified `AttemptTranscriptOverlay` opened displaying `Attempt #1 transcript` and live preformatted log output (`pre.logs-view`).
   - Dismissed overlay with `Escape` key.

---

### Phase 5: Bulk Management

1. **Multi-Selection & Action Bar**:
   - Seeded two distinct tasks (`E2E Bulk Target A` and `E2E Bulk Target B`) with `Priority: low`.
   - Selected both rows via checkboxes.
   - Verified floating `BatchActionsBar` appeared with count `2 selected`.
2. **Modal Cancellation**:
   - Clicked `Bulk edit…`.
   - Clicked `Cancel` in the dialog. Dialog dismissed while maintaining the active 2-item selection.
3. **Applying Batch Modifications**:
   - Reopened `Bulk edit…`, checked `Priority`, selected `high`, and clicked `Apply to 2 tasks`.
   - Floating action bar dismissed upon successful update.
   - Verified both table rows displayed updated chip `Priority: high`.
   - Queried REST API (`GET /api/tasks/:id`) and verified both records updated `priority` to `high` while preserving distinct `requested-by` attributes (`BulkAlpha` and `BulkBeta`).

---

### Phase 6: Deep Links & Error Recovery

1. **404 Handling**:
   - Navigated to `/tasks/non-existent-task-id-99999`.
   - Verified application remained functional and returned operator safely to the main tasks list.

---

## 3. Automated Test Spec

The automated regression test verifying all above workflows is located in the repository at:
`ui/e2e/task_intake_inbox_lifecycle_walkthrough.spec.js`

### Execution Command
```bash
cd ui && npx playwright test e2e/task_intake_inbox_lifecycle_walkthrough.spec.js
```

### Test Results
```text
Running 1 test using 1 worker

  ✓  1 [chromium] › e2e/task_intake_inbox_lifecycle_walkthrough.spec.js:6:3
     › Task Intake, Inbox, and Lifecycle Operator Walkthrough
     › thoroughly exercises inbox, new task intake, search/filter, task detail lifecycle, and bulk edit in a real browser (19.1s)

  1 passed (31.4s)
```

---

## 4. Conclusion & Recommendations

The Grain task intake, inbox triage, detail lifecycle, and bulk editing subsystems exhibit strong usability, consistent styling, robust error recovery, and reliable real-time state synchronization. All core operator workflows operate as expected in a real browser against a real server.
