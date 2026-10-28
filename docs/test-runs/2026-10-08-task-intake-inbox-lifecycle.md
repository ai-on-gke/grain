# Task Intake, Inbox, and Lifecycle Operator Walkthrough

**Date:** 2026-10-08  
**Scope:** Operator workflow from incoming work through task disposition in a real browser against a real server.

## What was tested

- **Commit:** `50b2bd3cf` (tip of `grain/task-2518`, cleanly merged with current `origin/candidate`).
- **Focus areas:**
  - Full end-to-end operator workflow from incoming work through task disposition.
  - Inbox navigation, inspection, triage groups, row navigation, approval, replies, empty states.
  - Global New task form intake (`/new`) covering queued and proposed paths, repo/no-repo choices, base branch, auto-merge, capability inheritance, custom fields, advanced agent/model/network/resource options, validation, and cancel/discard.
  - All tasks list and state views (`/`, `state=queued`, `state=blocked`, `state=proposed`, `state=deferred`, `state=closed`), search, sort, compound narrowing filters, URL persistence across page reload, and browser Back/Forward history navigation.
  - Task detail pane lifecycle: title/description in-place edits, declared custom fields modification, base branch updates, auto-merge toggling, indefinite deferral, wake date deferrals in local/remote timezones, undeferral, proposal approval, attempt timeline inspection, and transcript overlays.
  - Multi-selection and bulk operations: row selection, floating batch actions bar, off-screen selection warnings, bulk field edits, bulk cancellation preserving selection, and bulk patch persistence.
  - Error boundaries, 404 deep linking, and accessibility tree coverage for modal headers and alert announcements.
- **Environment:**
  - Debian GNU/Linux 12 (bookworm), Linux 6.1.0-53-amd64 x86_64.
  - Go 1.26.6, Node.js 20.19.0.
  - Playwright 1.62.1 with Chromium (`chromium_headless_shell-1234` from `/var/cache/playwright`).
  - Browser time zones: UTC and Asia/Tokyo (`en-US` locale), viewport 1400×900.
- **Server:**
  - Real `grain demo` server built from repository source:
    - Frontend compiled with `npm run build` in `ui/` into `pkg/ui/static`.
    - Server binary built with `go build -o /tmp/grain ./cmd/grain`.
    - Started with `-open=false` over a clean throwaway SQLite store on a dedicated worker loopback port.
- **Driver:**
  - Real browser automation driven by Playwright test runner and dedicated execution suites:
    - `ui/e2e/task_intake_inbox_lifecycle_walkthrough.spec.js` (6/6 passing).
    - `ui/e2e/inbox.spec.js` (3/3 passing).
    - `ui/e2e/tasks.spec.js` (7/7 passing).
    - `ui/e2e/fields.spec.js` (6/6 passing).
    - `ui/e2e/batch_actions_workflows.spec.js` (1/1 passing).
    - `ui/e2e/task_detail_workflows.spec.js` (5/5 passing).
    - `ui/e2e/keyboard-open-rows.spec.js` (4/4 passing across chromium and webkit).
  - Network and console monitoring: zero uncaught page errors, zero unhandled promise rejections, zero CSS layout overflows.
- **Account:** Authenticated operator session (`operator`).

## Stubs

Boundary conditions and external interfaces:
- **GitHub:** Not reachable from local sandbox test runner. External branch lookup (`GET /api/repos/<repo>/github-branches`) returns 404, gracefully handled by UI as "no suggestions".
- **Gemini Key / Live LLM Agents:** No live API key configured at `/home/debian/.gemini-api-key`; tasks use seeded execution runs and transcripts.
- **External notification permissions:** Mocked `window.Notification` in notification-specific E2E suites to simulate granted/denied states.

## Steps and outcomes

### 1. Inbox Triage and Actioning

1. **Direct Navigation and Rail Entry:**
   - Navigated directly to `/inbox` and switched between `/tasks` and `/inbox` via the navigation rail.
   - Verified initial seeded triage groups with accurate counts:
     - **Answer a question** (Task #4: "Investigate the merge queue stall")
     - **Submit for merge** (Task #6: "Cache the capability readiness probe")
     - **Read an answer** (Task #7: "Why did last week's nightly suite slow down?")
     - **Approve a proposal** (Task #1: "Add a dark mode toggle")
   - Rail badge correctly displayed initial inbox count (4).
2. **Row Navigation:**
   - Clicking a triage item row opened the task detail overlay pane (`/tasks/1`).
   - Back navigation (`.overlay-pane-back button`) cleanly returned to `/inbox`.
3. **Approve Seeded Proposal:**
   - Clicked **Approve** on Task #1 ("Add a dark mode toggle").
   - Dispatched `POST /api/tasks/1/approve`.
   - Task immediately transitioned from `proposed` to `queued`.
   - Row cleanly departed the "Approve a proposal" group.
4. **Answer Seeded Parked Question:**
   - Clicked **Reply** on Task #4 ("Investigate the merge queue stall").
   - Inline reply text area opened; typed operator response ("Fix both PRs and unblock the queue immediately.").
   - Clicked **Send**; dispatched `POST /api/tasks/4/reply`.
   - Task left "Answer a question" and transitioned to `queued`.
5. **Disposition Verification:**
   - Navigated to the main Tasks list view.
   - Verified both Task #1 and Task #4 appeared with the `.badge-queued` state pill.

**Result:** Pass.

### 2. Global New Task Intake (`/new`)

1. **Form Validation & Draft Discard:**
   - Opened "+ New task" side panel overlay.
   - Verified **Create task** button remained disabled when title was empty or only whitespace.
   - Populated title with draft text and clicked **Discard draft**; verified form state was completely reset.
   - Closed modal via dialog close button.
2. **Fully-Specified Queued Task Path:**
   - Opened "+ New task" side panel.
   - Populated title and rich Markdown description with fenced code blocks.
   - Selected target repository `acme/widgets` and specified base branch `main`.
   - Checked "Auto-merge once checks pass".
   - Verified capability pre-selections based on target repo rules; modified selections via capability multi-select.
   - Configured custom fields:
     - Priority: `high`
     - Area: `frontend`
     - Requested by: `OperatorTeam`
     - Estimate (days): `4`
     - Customer-facing: `Yes`
   - Expanded "Advanced options":
     - Prompt extension override: "Strictly verify UI responsiveness."
     - Network access: `open`
     - Sandbox resources: 2 vCPUs, 4096 MiB Memory, 20 GiB Disk.
   - Clicked **Create task**. Verified submission dispatched `POST /api/tasks` with all parameters accurately serialized.
   - Confirmed task appeared in task list with `queued` status and `high` priority pill.
3. **Proposed Task Path (No Repository):**
   - Opened "+ New task" side panel.
   - Populated title and proposal description.
   - Selected repository option "none" (No repository).
   - Unticked "Queue immediately" (filing as proposal).
   - Clicked **Create task**.
   - Verified task was filed in `proposed` state with no repository binding.

**Result:** Pass.

### 3. All Tasks, Search, Filter, Sort, and URL Persistence

1. **Search & URL Persistence:**
   - Typed unique task title substring into "Search tasks…" filter input.
   - Filtered view narrowed immediately to the single matching task row.
   - Confirmed query parameter `q=<term>` persisted in URL.
   - Reloaded browser page; verified search query and narrowed row list persisted across reload.
   - Cleared search input; full list restored.
2. **Browser Back/Forward History:**
   - Executed browser Back (`goBack()`); verified prior filtered search URL and matching row were restored.
   - Executed browser Forward (`goForward()`); verified state restored accordingly.
3. **Rail State Views:**
   - Clicked **Queued** in rail navigation; verified URL updated to include `state=queued` and only queued tasks displayed.
   - Clicked **Blocked** in rail navigation; verified URL updated to include `state=blocked` and blocked dependency tasks displayed (e.g. Task #11 blocked on #8).
   - Tested **Deferred** and **Closed** rail navigation; verified state filter query param in URL persisted across reloads and history steps.

**Result:** Pass.

### 4. Task Detail Lifecycle

1. **Detail Panes & In-Place Editing:**
   - Opened task detail for the newly created queued task.
   - Clicked header **Edit** button, modified title and description, and clicked **Save**. Verified title updated in header and persisted across page refresh.
   - Clicked **Edit fields**, updated Priority from `high` to `medium` and Requested-by to `CoreEngineering`, and saved fields. Verified updated declared field chips rendered immediately.
   - Updated Base branch input and toggled Auto-merge setting; verified changes persisted.
2. **Defer and Undefer Transitions:**
   - Clicked **Defer** (without date); task transitioned to `deferred` state (`.badge-deferred`).
   - Clicked **Undefer**; task restored to `queued` state (`.badge-queued`).
   - Specified future deferral wake date (`2026-12-31`) and clicked **Defer**; verified "Picks itself back up on..." notice displayed.
   - Tested deferral from a browser in `Asia/Tokyo` timezone, verifying wake date defers to midnight at the start of the picked day in deployment UTC rather than shifting backwards by local offset.
   - Clicked **Undefer**; task returned to `queued`.
3. **Execution Transcripts & Links:**
   - Opened seeded running task (Task #3: "Bump the Go toolchain to 1.24").
   - Clicked timeline attempt item (`.timeline-item-attempt`); verified Attempt transcript dialog displayed seeded execution logs. Dismissed with Escape.
   - Verified PR links rendered properly for tasks with associated pull requests (e.g. Task #5 linking `acme/widgets#101`).

**Result:** Pass.

### 5. Multi-Selection and Bulk Operations

1. **Row Selection:**
   - Selected multiple task rows via row-level checkboxes.
   - Verified floating bulk actions bar appeared indicating "2 selected" with available batch actions: Approve, Defer, Close, Bulk edit…, and Clear selection.
2. **Off-Screen Selection Notice & Confirmation:**
   - Selected tasks under `state=proposed` filter, navigated to `state=running`.
   - Floating batch actions bar indicated "2 selected (none of them shown here)".
   - Clicking Close prompted confirmation warning that selected tasks were not shown in current view.
3. **Bulk Edit Dialog & Cancellation:**
   - Clicked **Bulk edit…**; verified bulk edit dialog opened displaying shared and multi-value fields (e.g. "Requested by (mixed across the selection)").
   - Clicked **Cancel** in dialog; verified modal dismissed and row selection was preserved ("2 selected").
4. **Bulk Edit Application:**
   - Re-opened **Bulk edit…** dialog.
   - Selected Priority checkbox and chose `high`.
   - Clicked **Apply to 2 tasks**; verified batch PATCH requests dispatched and applied to both tasks.
   - Verified selection automatically cleared post-application and updated custom field values persisted.

**Result:** Pass.

## Findings and Bug Assessment

- All operator workflows across Inbox, Task Intake, Lifecycle transitions, and Bulk actions functioned deterministically without regressions.
- No UI crashes, unhandled promise rejections, accessibility focus traps, or layout overflow issues were observed.
- Previous fixes around capability inheritance, off-allowlist repo warnings, timezone-normalized wake dates, and accessible modal announcements remain fully functional.
