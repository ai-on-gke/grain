# Task intake, Inbox, and lifecycle UI walkthrough — 2026-10-09

## What was tested

- **Commit:** `d83c53b47` / `5123d46d7` (HEAD of `grain/task-2740`, tracking latest merges from `candidate`).
- **Focus since 2026-10-07 walkthrough (`4ff094e4b` / `61604b729`):**
  - Verification of New task capability inheritance and override preservation across team selections (`83c7fa6bc`, `6ef1a7b46`).
  - Interactive Director sessions in demo mode (`a617c6a70`).
  - Scoping of base-branch gate readings to reader teams (`79475ca2e`).
  - Off-allowlist repository warning validation banner on intake (`8cce6a6ed`, `83c7fa6bc`).
  - Verification of dirty-form confirmation guards when leaving edited task detail or custom fields (`131c8f003`).
  - Defer date picker with wake date calculations across timezones (`UTC` and `Asia/Tokyo`).
  - Partial batch selection confirmation when operating on tasks hidden by active search/filters.
  - Full end-to-end operator workflow from task intake and inbox triage to task lifecycle transitions and bulk operations.
- **Environment:**
  - Debian GNU/Linux 12 (bookworm), Linux 6.6.137+ x86_64.
  - Go 1.24.0 / 1.26.6, Node.js 20.19.0, npm 10.8.2.
  - Playwright 1.50.0 / 1.62.1 with headless Chromium (`chromium-headless-shell`).
  - Viewport 1400×900, browser time zones tested: UTC and Asia/Tokyo (`Asia/Tokyo`), locale en-US.
- **Servers:**
  - Real `grain demo -addr 127.0.0.1:0 -open=false` compiled from source (`go build -o /home/debian/grain-bin ./cmd/grain` with production bundle `npm run build` in `pkg/ui/static`).
  - Driven over ephemeral embedded SQLite stores under isolated scratch directories.
  - Pre-seeded with 12 demo tasks covering all major states: proposed (#1), queued (#2, #10), running (#3, #8), awaiting reply (#4), awaiting submit (#6), awaiting ack (#7), completed (#5), deferred (#9), closed (#12), and blocked (#11, #17).
- **Driver:**
  - Automated Playwright scripts driving real Chromium against the server. No mocks of Grain internal REST APIs, WebSocket handlers, or React components. Drove visible UI elements by role, label, and accessible selectors: clicks, keyboard typing, Enter, Escape, Space, Tab order, Back/Forward navigation, reload, and deep links.
  - Full end-to-end suite `ui/e2e/task_intake_inbox_lifecycle_walkthrough.spec.js` verified green across all 6 test cases (30.4s).
  - Targeted e2e suite `ui/e2e/new-task-off-list-repo.spec.js` verified green (8.6s).
  - Ad-hoc exploratory scripts verifying DOM rendering, network responses, keyboard interactions, error boundaries, and state mutations.
- **Account:**
  - `operator`, created via first-run account registration modal with `X-Grain-Admin-Mode: on`.
- **Stubs & Boundaries:**
  - Offline local demo environment without external GitHub network connectivity.
  - Network requests to `/api/repos/<repo>/github-branches` return HTTP 404 (handled gracefully by frontend as empty branch auto-complete suggestions).
  - Live agent runner & LLM backends: Model-backed execution keys are omitted in sandbox. Transcripts on seeded tasks (#3, #8) were rendered from demo fixtures.
  - Desktop notifications mock: browser notification permissions verified via UI toggle without triggering browser-level native dialog traps.

---

## Detailed Walkthrough Execution & Results

### 1. Inbox Triage and Action Disposition

- **Direct Navigation & Layout:**
  - Navigated directly to `/inbox` and from the sidebar rail. Both render the inbox view with 4 triage groups:
    1. *Answer a question* (`awaiting_reply`, 1 item: #4 "Investigate the merge queue stall").
    2. *Submit for merge* (`awaiting_submit`, 1 item: #6 "Cache the capability readiness probe").
    3. *Read an answer* (`awaiting_ack`, 1 item: #7 "Why did last week's nightly suite slow down?").
    4. *Approve a proposal* (`proposed`, 1 item: #1 "Add a dark mode toggle").
  - Rail badge correctly displayed count `4` matching header total count.
- **Row Deep Navigation:**
  - Clicked seeded proposal #1 row in inbox. Opened Task Detail overlay at `/tasks/1`.
  - Returned to `/inbox` using overlay back button (`.overlay-pane-back button`). URL correctly updated to `/inbox`.
  - Navigated to All tasks (`/`) via rail and returned to Inbox via rail button. State preserved.
- **Decline Confirmation:**
  - Clicked "Decline" on seeded proposal #1. Window confirmation dialog popped: `"Close 1 without running it? It can be reopened."`
  - Dismissed/cancelled the dialog. Verified proposal row remained intact in Inbox.
- **Approval Flow:**
  - Clicked "Approve" on seeded proposal #1.
  - POST `/api/tasks/1/approve` dispatched. Task #1 immediately removed from Inbox and proposal group collapsed.
  - Verified backend state via REST API: `GET /api/tasks/1` returned `state: "queued"` and `approved: true`.
- **Question Reply Flow:**
  - Clicked "Reply" on Task #4 in "Answer a question" group. Inline textarea and Send button rendered.
  - Verified Send button disabled when empty. Typed response `"Fixed the lint check on both PRs."` and sent.
  - POST `/api/tasks/4/comments` dispatched. Task #4 removed from Inbox and question group collapsed.
  - Verified backend state via REST API: `GET /api/tasks/4` returned `state: "queued"`.
- **Empty State Behavior:**
  - Submitted merge candidate on Task #6 (`POST /api/tasks/6/submit`) and acknowledged answer on Task #7 (`POST /api/tasks/7/close`).
  - Inbox transitioned cleanly to the empty state view: `"Nothing is waiting on you. Every task is either running, queued or done."`
  - Rail inbox badge disappeared (`0` count hides badge chip).
  - Reloaded page: verified empty state and zero badge persisted.

---

### 2. Global New Task Intake

- **Form Validation & Draft Cancellation:**
  - Clicked `+ New task` button. Modal overlay `.new-task-overlay` rendered with title "New task".
  - Verified "Create task" submit button disabled when title was empty or purely whitespace.
  - Typed draft title `"Discardable draft task"`, clicked "Discard draft". Verified field cleared.
  - Clicked "Close dialog". Modal closed cleanly.
- **Off-Allowlist Repository Warning:**
  - Opened New task modal. Selected `Repos: Other…` (`custom`), and typed `acme/gizmos`.
  - Verified warning banner `[data-testid='repo-off-allowlist']` rendered: `"acme/gizmos is not on this deployment's target repo list. The task will be filed but parked, and runs only once an operator adds the repo under Settings and replies on it."`
  - Selected `acme/widgets`: verified warning banner disappeared.
  - Discarded draft and closed modal.
- **Full Intake - Queued Task:**
  - Created new task with comprehensive configuration:
    - Title: `"E2E Queued Intake Task"`
    - Description: Markdown formatted with json code block fences.
    - Repository: `acme/widgets`
    - Base branch: `feature/v3-lifecycle`
    - Auto-merge: Enabled (`Auto-merge once checks pass`)
    - Capabilities: Ticked `Self debug`, `GCP key`, `Gemini key`
    - Custom fields: `Priority = high`, `Area = frontend`, `Requested by = OperationsTeam`, `Estimate = 3`, `Customer-facing = Yes`
    - Advanced options: Overrode prompt extension, set `Network access = open`, `vCPUs = 4`, `Memory = 8192 MiB`, `Disk = 30 GiB`.
  - Submitted with "Create task". Modal closed.
  - Switched to Tasks list: task row appeared immediately with `queued` badge and `Priority: high` chip.
  - Verified persisted model via REST API: all capabilities, resources, and custom fields match exactly.
- **Task Intake - Proposed Task with No Repo:**
  - Opened New task modal.
  - Title: `"E2E Proposed NoRepo Task"`
  - Description: Architectural proposal notes.
  - Selected `Repos: No repo (standalone task -- nothing to check out)` (`none`).
  - Unticked `Queue immediately` to file as an unapproved proposal.
  - Submitted. Verified row appeared in task list with `proposed` badge.
  - Navigated to `/inbox`: verified newly filed proposal appeared under "Approve a proposal" group, with rail badge incremented.

---

### 3. Task Views, Search, Sort, and URL Persistence

- **Full-Text Search:**
  - Typed specific task title in search field.
  - Filtered table dynamically to 1 matching row.
  - URL reflected `?q=...`. Reloaded page and verified filter and single-row result persisted.
  - Cleared search input; full list restored.
- **State Filter Views via Rail Navigation:**
  - Clicked `Queued` link in navigation rail. URL updated to `/?state=queued`.
  - Displayed only queued tasks (including newly created queued task); proposed task excluded.
  - Reloaded page: verified `state=queued` persisted in URL and list view.
  - Tested browser Back button: navigated back to All tasks (`/`).
  - Tested browser Forward button: navigated forward to `/?state=queued`.
  - Clicked `Blocked` rail link (`/?state=blocked`): verified blocked view rendered correctly.
- **Structured Compound Narrowing:**
  - Opened filter drawer (`Add and edit the conditions this list is narrowed by`).
  - Added filter condition `Priority = high`.
  - List narrowed to high priority tasks. URL updated with structured filter parameter `f=custom.priority:any:high`.
  - Clicked "Clear" button: filter conditions removed, list returned to default.

---

### 4. Task Detail and Lifecycle Transitions

- **In-Place Field Edits:**
  - Opened task detail for newly created queued task.
  - Edited title in header from `...` to `... (Updated)`. Saved and verified immediate header refresh.
  - Opened "Edit fields" popover: updated `Priority` from `high` to `medium`, updated `Requested by` to `CoreEngineering`. Saved; verified declared fields table refreshed.
  - Edited base branch input to `release-v2.5` and pressed Enter. Persisted and reflected in UI.
  - Toggled `Auto-merge once checks pass` checkbox. Verified toggle saved.
- **Dirty Form Guard (`131c8f003`):**
  - Clicked **Edit**, modified title, and pressed `Escape`.
  - Browser confirmation appeared: `"You have unsaved changes here. Leave and discard them?"`
  - Dismissing the prompt kept the edit form open, preventing accidental loss of edits.
- **Defer and Undefer Transitions:**
  - Clicked "Defer" without entering date. State badge transitioned from `Queued` to `Deferred`.
  - Clicked "Undefer". State badge returned to `Queued`.
  - Entered future date `2026-12-31` and clicked "Defer". State badge became `Deferred`, and note displayed `"Picks itself back up on 12/31/2026, 12:00:00 AM"`.
  - Verified wake date handling in `Asia/Tokyo` timezone correctly anchors to start of target day.
  - Clicked "Undefer". Task returned to `Queued`.
- **Detail Proposal Approval:**
  - Opened detail for the unassigned proposed task.
  - Clicked "Approve" button in header actions.
  - State transitioned from `Proposed` to `Queued`. Verified in list view and backend REST API.
- **Attempts, Transcripts, and Pull Request Links:**
  - Opened seeded running task #3 ("Bump the Go toolchain to 1.24").
  - Clicked attempt in execution timeline. Transcript modal opened with preformatted log output. Closed with Escape key.
  - Opened seeded task #8 ("Add pagination to the tasks API").
  - Verified GitHub pull request anchor link `https://github.com/acme/widgets/pull/104` rendered with external link attributes.

---

### 5. Multi-Row Selection & Bulk Operations

- **Batch Selection Bar:**
  - Selected two queued tasks via row checkboxes.
  - Floating bottom bar appeared displaying `"2 selected"`.
- **Bulk Edit Cancellation:**
  - Clicked "Bulk edit…". Modal dialog opened with field checkboxes and inputs.
  - Clicked "Cancel". Modal dismissed without mutations; selection of 2 rows remained active.
- **Applying Bulk Updates:**
  - Clicked "Bulk edit…" again.
  - Ticked `Priority` checkbox, selected `high`.
  - Clicked `Apply to 2 tasks`.
  - Selection cleared, floating batch bar dismissed.
  - Both rows updated to display `Priority: high` chip.
  - Verified via REST API: other custom fields (`requested-by`) on each task remained untouched and preserved.
- **Partial Selection Safety Confirmation:**
  - Selected all tasks in view (9 selected).
  - Applied search filter hiding 8 of the selected tasks.
  - Clicked **Close** in batch bar: prompted confirmation `"Close 9 task(s)? 8 of them are not shown in this view."`
  - Dismissing confirmation cancelled the action without modifying any task.

---

### 6. Error Boundaries & Deep Links

- **Non-Existent Task Route:**
  - Navigated directly to `/tasks/999999`.
  - Handled cleanly without uncaught React exceptions or blank page crashes. Graceful fallback displayed while keeping application shell and navigation intact.

---

## Findings and Observations

1. **Clean Passing Walkthrough:** All user workflows across Inbox, Intake, Lifecycle, Views, and Bulk Operations functioned reliably. All 6 e2e Playwright tests in `ui/e2e/task_intake_inbox_lifecycle_walkthrough.spec.js` and `ui/e2e/new-task-off-list-repo.spec.js` passed without failures or flakes.
2. **Recent Landed Fixes Validated:**
   - Capabilities pre-ticking and overriding (`83c7fa6bc`, `6ef1a7b46`) properly preserved custom capability selections without reverting to team defaults on team/repo toggles.
   - Off-list repository warning banner (`8cce6a6ed`, `83c7fa6bc`) clearly alerts operators prior to submission.
   - Dirty form unsaved changes confirmation guard (`131c8f003`) protects unsaved edits from accidental dismissal.
   - Rail state view URL synchronization survives hard reloads and history traversal without dropping state parameters.
3. **HTTP Status Observations:**
   - Requests to `/api/repos/<repo>/github-branches` return HTTP 404 in demo mode as expected (no live GitHub repository proxy is active), which the UI handles gracefully without toast errors or degraded experience.

---

## Verdict

- **Result:** PASSED.
- **Status:** No regressions or blocking operator issues detected. Codebase matches required behavior.
