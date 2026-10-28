# Test Run Report: Task Provenance Disclosure, Deferred Task Approval & `supersedes`, Relative UI Links, and Board/List/Tree Interactions (2026-10-22 §6)

- **Date:** 2026-10-23
- **Test Plan:** `docs/test-plans/2026-10-22.md` — Section 6: _Task Provenance Disclosure, Deferred Task Approval & `supersedes`, Relative UI Links, and Board/List/Tree Interactions_
- **Target Branch:** `candidate` (`grain/049f1e09/task-217`)
- **Commits Under Test:**
  - `d23f96d33` — `feat(ui): collapse task provenance and work tree by default`
  - `be3bc8e16` — `fix(ui): enlarge .provenance-toggle touch target on coarse pointers`
  - `2ea857d5b` (backported to `candidate` as `7f52ecc8a`) — `fix(ui): allow approving unapproved deferred tasks in place`
  - `428cfbd64` (backported to `candidate` as `89f3c002d`) — `fix(model): allow propose_task supersedes to close deferred tasks`
  - `c713b9bef` (backported to `candidate` as `86239909b`) — `fix(mcp): emit relative /tasks/<id> path in propose_task output`
  - `a4b7cac89` (backported to `candidate` as `1fbf7751d`) — `feat(ui): support Shift-click range selection in TaskList and TaskBoard`
  - `0b296198a` (backported to `candidate` as `0e2f1dfbc`) — `fix(ui): warn in BatchActionsBar when selected Kanban cards are hidden`
  - `ea3ec549a` — `fix(ui): disable move handle in single-card Kanban columns and align card chips`
  - `af122a620` (backported to `candidate` as `97e1d55df`) — `fix(ui): hide no-op section move handles on un-sectioned report boards`
  - `4877c6b67` (backported to `candidate` as `23e7a7e2f`) — `fix(ui): guard unsaved edits in BulkEditOverlay, CustomizePinnedOverlay, and DetailOverlay`
  - `2fc026c20` (backported to `candidate` as `84bf3efef`) — `fix(ui): open TaskTree row on click and restore focus to .task-tree-id on Escape`
  - `f61b5e78d` (backported to `candidate` as `c14fc7047`) — `fix(ui): restore PageHeader padding, typography, and section icons`
  - `cc7658c13` (backported to `candidate` as `fc1a4c878`) — `feat(ui): require agents to render and look at every UI change`

---

## 1. Environment Details

- **OS / Kernel:** Debian GNU/Linux (`linux/amd64`)
- **Go Toolchain:** `go1.25.0 linux/amd64`
- **Node / npm:** Node `v22.x`, Playwright `v1.58.2` (Chromium headless shell at `/var/cache/playwright`), Vitest `v4.1.11`
- **Viewports Exercised:**
  - Desktop: `1280x800`
  - Phone (coarse pointer): `390x844` (`isMobile: true`, `hasTouch: true`, `@media (pointer: coarse)`)

---

## 2. Walkthrough Execution Steps & Findings

### Step 1: Deferred Task In-Place Approval & `propose_task` `supersedes` (`2ea857d5b` / `7f52ecc8a`, `428cfbd64` / `89f3c002d`, `c713b9bef` / `86239909b`)

1. **In-place approval and withdrawal on a deferred task (`Actions.jsx`, `pkg/ui/client.go`):**
   - Filed an unapproved task `T1` (`POST /api/tasks`) with a future wake timestamp (`deferredUntil: "2099-05-01T09:00:00Z"`), `reads: ["acme/docs"]`, and `writes: [{ repo: "acme/widgets", base: "main" }]`.
   - Navigated to `/tasks/<T1>` in `DetailOverlay`:
     - Verified the `.detail-state .badge-deferred` badge displays `Deferred` and `.actions` renders the `Approve` button (`t.state === "deferred" && !t.approved`) alongside the wake hint `Picks itself back up on …, back into the proposals for someone to approve.`
     - Clicked `Approve` (`POST /api/tasks/<T1>/approve`): verified `GET /api/tasks/<T1>` reports `state: "deferred"`, `approved: true`, and `deferredUntil: "2099-05-01T09:00:00Z"` preserved intact (`Client.Approve` accepts `model.StateDeferred` without clearing `DeferredAt` or `DeferredUntil`). Verified `.actions` switches to `Withdraw approval` and updates the wake hint to `Picks itself back up on …, onto the queue.`
     - Clicked `Withdraw approval` (`POST /api/tasks/<T1>/withdraw-approval`): verified `GET /api/tasks/<T1>` returns to `approved: false` while `state: "deferred"` and `deferredUntil: "2099-05-01T09:00:00Z"` remain intact.
2. **Live `propose_task` with `supersedes` and relative UI paths (`pkg/model/supersede.go`, `pkg/daemon/mcpserver.go`, `pkg/mcp/mock_tools.go`):**
   - Invoked `grain mcpserver -sandbox-root <dir> -server <url> -task <parentChatId> -interactive` over JSON-RPC stdio to call `propose_task` with `supersedes: "#<T1>"`.
   - Verified the MCP tool output reads:
     `Proposed task #<T2> is visible now: /tasks/<T2>. It remains unapproved until a human approves it. Task <T1> is closed as superseded by it.`
   - Confirmed the returned link is root-relative (`/tasks/<T2>`) with no loopback `http://127.0.0.1` or `http://localhost` prefix (`daemonProposalFiler.FileProposal` and `relativeProposalURL`).
   - Verified via `GET /api/tasks/<T1>` and `GET /api/tasks/<T2>` that `T1` transitioned to `state: "closed"` with `supersededBy: "<T2>"`, and `T2` carries `supersedes: "<T1>"`, `generatedFrom: "<parentChatId>"`, and `origin: "task:<parentChatId>"`.
   - Called `propose_task` a second time targeting the already-closed `T1` (`T3`): verified `model.SupersedeRefusal` refuses to close `T1` a second time (`Task <T1> is still open: grain would not close it`) and appends the refusal note (`is already closed`) to `T3`'s description.
   - **Outcome:** **PASS**

### Step 2: `.provenance-toggle` Collapse, Expand, Reset, and Coarse-Pointer Touch Target (`d23f96d33`, `be3bc8e16`, `4877c6b67` / `23e7a7e2f`)

1. **Default collapsed state & visible freshness/PR links (`DetailOverlay.jsx`, `WorkTree.jsx`):**
   - Opened the seeded task with an open pull request (`/tasks/3`, _Add pagination to the tasks API_) and `/tasks/<T2>`:
     - Verified `.freshness` directly renders the task age (`as of …`) and the pull request link (`acme/widgets#104` -> `https://github.com/acme/widgets/pull/104`) outside the disclosure while `.provenance-toggle` starts collapsed (`aria-expanded="false"`, `.provenance-panel` absent from the DOM).
2. **Coarse-pointer touch target at `390x844` (`ui/src/style.css`):**
   - Created a Playwright browser context with `viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true`.
   - Verified `window.matchMedia("(pointer: coarse)").matches === true` and measured `.provenance-toggle`'s bounding box height (`>= 40px` via `@media (pointer: coarse) { .freshness .provenance-toggle { display: inline-flex; align-items: center; min-height: 40px; } }`).
   - Tapped `.provenance-toggle` on the phone viewport and verified `aria-expanded="true"` and `.provenance-panel` visibility.
3. **Expand, task-switch reset, and in-pane dirty-close guard (`WorkTree.jsx`, `DetailOverlay.jsx`):**
   - On desktop (`1280x800`) at `/tasks/<T2>`, clicked `.provenance-toggle`: verified `aria-expanded="true"`, `.provenance-links` displays `generated from`, `origin`, and `supersedes #<T1>` (`href="/tasks/<T1>"`), and `.provenance-panel .task-tree` renders the nested work tree.
   - Clicked `#<T1>` inside `.provenance-links` to navigate to `/tasks/<T1>`: verified `.provenance-toggle` automatically resets to `aria-expanded="false"` with `.provenance-panel` collapsed.
   - Clicked `.provenance-toggle` on `/tasks/<T1>`: verified `.provenance-links` renders `superseded by #<T2>` (`href="/tasks/<T2>"`).
   - Opened `/tasks/<T3>`, clicked `Edit`, modified the `Title` field, and clicked the in-pane dependency chip for `T2` in `.detail-side`: verified `openTaskFromDetail` triggers `guardLeaving()` (`"You have unsaved changes here. Leave and discard them?"`) and stays on `/tasks/<T3>` when dismissed.
   - **Outcome:** **PASS**

### Step 3: `BoardCard` Chip Parity, Single-Card Disabled Move Handle, Shift-Click Range Selection, Hidden Kanban Selection Warning, Un-Sectioned `ReportBoard`, Dirty-Close Guards, and `PageHeader` (`a4b7cac89`, `0b296198a`, `ea3ec549a`, `af122a620`, `4877c6b67`, `f61b5e78d`)

1. **`TaskRow` and `BoardCard` chip parity & single-card disabled move handle (`TaskList.jsx`, `TaskBoard.jsx`, `ReorderPickup.jsx`):**
   - Note on routing: `ui/src/paths.js` routes `TaskList` at `/`, saved Kanban boards (`TaskBoard`) at `/boards/<kanbanBoardId>`, saved Report boards (`ReportBoard`) at `/boards/<reportBoardId>`, and saved Tree boards (`TaskTreeBoard` / `TaskTree`) at `/boards/<treeBoardId>`.
   - On `/?state=closed` (`TaskList`) and `/boards/<kanbanBoardId>` (`TaskBoard`), verified `T1`'s `.task-row .chips` and `.board-card .board-card-chips` both render `superseded by <T2>`, `acme/docs (read)`, and `acme/widgets (write)`.
   - Verified `T1`'s state badge on the multi-state `"Closed"` column (`states: ["closed", "failed"]`) carries `role="img"` and `aria-label="Closed"`.
   - Verified that because `T1` is the sole card in the `"Closed"` column (`canMove={columnCards.length > 1}`), its move handle (`button[aria-label="Move task <T1>"]`) is `disabled` and carries `title="Nowhere to move this: it is the only one here"`.
2. **Shift-click contiguous range selection (`TaskList.jsx`, `TaskBoard.jsx`, `rangeSelection.js`):**
   - On `/` (`TaskList`), clicked the 1st visible `.task-row` checkbox and Shift-clicked the 4th visible `.task-row` checkbox: verified all 4 contiguous rows (indices `0..3`) are checked and `BatchActionsBar` displays `4 selected`.
   - On `/boards/<kanbanBoardId>` (`TaskBoard`), clicked the 1st `.board-card` checkbox in the `"Backlog"` column and Shift-clicked the 4th `.board-card` checkbox: verified all 4 contiguous cards are checked and `BatchActionsBar` displays `4 selected`.
3. **Hidden Kanban selection warning on `BatchActionsBar` (`TaskBoard.jsx`, `BatchActionsBar.jsx`):**
   - Filtered the Kanban board's search box by `"canary-batch"` so only 2 of the 4 selected cards remained visible: verified `BatchActionsBar` displays `4 selected` alongside the warning `2 not shown here`.
   - Filtered by `"no-matching-tasks-xyz"` so 0 of the 4 selected cards remained visible: verified `BatchActionsBar` displays `none of them shown here`.
4. **Dirty-close guards on `BulkEditOverlay` and `CustomizePinnedOverlay` (`BulkEditOverlay.jsx`, `CustomizePinnedOverlay.jsx`):**
   - Opened `Bulk edit…` (`BulkEditOverlay`), checked the `Auto-merge` field checkbox, and pressed `Escape`: verified `"You have unsaved changes here. Leave and discard them?"` fires and dismissing keeps the dialog open.
   - Opened `Customize pinned items` (`CustomizePinnedOverlay`), clicked `Unpin` on the first item, and pressed `Escape`: verified `"You have unsaved changes here. Leave and discard them?"` fires and dismissing keeps the dialog open.
5. **Un-sectioned `ReportBoard` (`ReportBoard.jsx`, `af122a620`):**
   - Created an un-sectioned report board (`kind: "report"` with no `sections`) and navigated to `/boards/<reportBoardId>`: verified `.report-sections` renders the full metrics report with `0` section move handles (`button[aria-label^="Move section"]`), since `namedSections` is `false`.
6. **`PageHeader` padding, typography, and section icons (`PageHeader.jsx`, `f61b5e78d`):**
   - Verified `[data-testid="page-header"]` renders `.header-icon svg` and `[data-testid="top-bar-page"]` with `font-size: 16px` (`1rem`) and `font-weight: 600` (and verified `e2e/page-header-rail-walkthrough.spec.js` passes across desktop and phone viewports).
   - **Outcome:** **PASS**

### Step 4: `TaskTree` Row Click & Escape Focus Return (`TaskTree.jsx`, `2fc026c20` / `84bf3efef`)

- Created a saved Tree board (`kind: "tree"`) and navigated to `/boards/<treeBoardId>`.
- Located `T2`'s `.task-tree-row` and clicked `.task-tree-title` (outside `a.task-tree-id`): verified `.task-tree-row`'s row-click handler focuses `a.task-tree-id` prior to opening `DetailOverlay` (`/tasks/<T2>`).
- Pressed `Escape` to close `DetailOverlay`: verified `MUI Dialog` restores focus (`document.activeElement`) directly to `a.task-tree-id` inside `T2`'s `.task-tree-row`.
- **Outcome:** **PASS**

### Step 5: Screenshot Tool Check (`cc7658c13` / `fc1a4c878`)

- Checked whether `cc7658c13` is present on `candidate`: commit `cc7658c13` on `main` was backported to `candidate` as commit `fc1a4c878` (`feat(ui): require agents to render and look at every UI change`, `Backported-from: cc7658c134e7def17b09f5a99fa781204bb89737`).
- Executed:
  ```bash
  PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright make ui-screenshots PAGES=/,/inbox OUT=screenshots/check
  ```
- Verified all 8 individual screenshots (`01-root` and `02-inbox` across `desktop-light`, `desktop-dark`, `phone-light`, and `phone-dark`) and the labelled composite `ui/screenshots/check/contact-sheet.png` (`370,572` bytes) were generated cleanly, then cleaned up the temporary output directory.
- **Outcome:** **PASS**

---

## 3. Automated Test Coverage Added & Executed

- **Added Playwright E2E spec:** `ui/e2e/task-provenance-deferred-and-boards-walkthrough.spec.js`
  - Exercises all §6 walkthrough steps end-to-end against a real `grain` server and `grain mcpserver` over Chromium at desktop (`1280x800`) and coarse-pointer phone (`390x844`) viewports.
  - Executed 3 consecutive runs across fresh stores (`npx playwright test --project=chromium e2e/task-provenance-deferred-and-boards-walkthrough.spec.js`): **3/3 passed** (~9–11s per run).
- **Existing Playwright E2E suites verified:**
  - `e2e/board.spec.js`, `e2e/batch_actions_workflows.spec.js`, `e2e/work-tree.spec.js`, `e2e/page-header-rail-walkthrough.spec.js`, `e2e/task_edit_custom_fields_unsaved_guard_walkthrough.spec.js`: **30/30 passed**.
- **Frontend unit/component tests (`cd ui && npm test -- --run`):** **All suites passed**.
- **Go unit/integration tests (`go test ./pkg/model/... ./pkg/ui/... ./pkg/mcp/... ./pkg/daemon/... -run 'Test.*Supersede|Test.*NotBefore|Test.*Deferred|Test.*LiveProposal'`):** **All packages passed**.
- **Bugs Found:** None. All 13 commits in §6 behave as specified.
