# Task Intake, Inbox, and Lifecycle UI Walkthrough — 2026-10-22

- **Date:** 2026-10-22
- **Commit tested:** `747f8245502f719f8fca60f125e9bc545128819e` (branch `grain/049f1e09/task-176`, base `candidate`)
- **Environment:**
  - OS: Debian GNU/Linux 12 (bookworm), `linux/amd64` (kernel `6.1.0-45-cloud-amd64`)
  - Go: `go1.26.2 linux/amd64`
  - Node.js / npm: `v20.19.0` / `10.8.2`
  - Browser: Playwright Chromium (`@playwright/test` 1.58.2, headless Chromium `1400x900` viewport; `UTC` default and `Asia/Tokyo` for cross-timezone deferral check)
  - Server under test: a wrapper distribution's `cmd/grain` demo -addr 127.0.0.1:<port> -open=false -merge-rules -access-code-file <path>` serving the production Vite bundle built into `pkg/ui/static` over a throwaway embedded SQLite store (`$TMPDIR/grain-demo-*/grain.db`) and local `githubsim` git repositories (`$TMPDIR/grain-demo-*/repos/acme/widgets.git` and `acme/docs-site.git`).

---

## Summary

We walked through the complete operator workflow from incoming work in `/inbox` and task intake (`/new` and prefill links) through list narrowing, state/blocked views, task detail inspection/editing, dependencies, work-tree provenance, merge policy, grain CI run overlay, pending secrets, local-script approvals, suspended agent sessions, internal pull-request diff view, and multi-row bulk operations in a real Chromium browser against a real `grain demo` server.

During the walkthrough we found, reproduced, and fixed **two bugs** in this branch:

1. **Rail state views (`Queued`, `Running`, `Deferred`, `Closed`, `Merged`) treated their own baseline as narrowed and wiped `Deferred`/`Closed`/`Merged` rows when clicking `Clear`** (`ui/src/components/TaskList.jsx`).
2. **`GET /api/tasks/{id}` (`taskDetailCache`) keyed cached `TaskDetail` bodies and `ETag`s solely on SQLite's `Store.ChangeToken(ctx)`, omitting in-memory `LocalScriptApprovals` transitions** (`pkg/ui/task_detail_cache.go`, `pkg/ui/local_script_approvals.go`), so requesting or expiring a local-script approval on an already-open task pane returned a stale cached response (`304 Not Modified`) and failed to render `.local-script-approval` until an unrelated database write bumped `data_version`.

Both fixes are covered by unit tests (`ui/src/components/TaskList.test.jsx`, `ui/src/App.narrowing.test.jsx`, `pkg/ui/task_detail_cache_test.go`) and Playwright e2e regression tests (`ui/e2e/task_intake_inbox_lifecycle_walkthrough.spec.js`).

---

## External / System Boundary Stubs

All browser interactions ran against the real Go server, real SQLite store, real local git repositories, and real built React frontend. Only boundaries requiring a live external forge or live container dispatch were stubbed:

1. **Stub 1 — Real git branch `grain/task-15` pushed to `githubsim` bare repository before first branch-list cache fill:**
   - Before opening the UI (which caches `/api/repos/acme/widgets/branches` for 60 seconds), we cloned `$TMPDIR/grain-demo-*/repos/acme/widgets.git`, checked out `grain/task-15` from `origin/release/1.0`, committed `widget.go` (`// Walkthrough diff line added on grain/task-15`), and pushed `grain/task-15` back to `origin`.
   - This let us select `grain/task-15` in the Task 15 detail pane's `Branch` autocomplete and follow the **Internal PR** link (`/repos/acme/widgets/compare/release/1.0...grain/task-15`) to render a real git diff in `PullRequestPage`.
2. **Stub 2 — `local-scripts:deploy` grant & simulated `grain mcpserver` approval hop (`POST /api/tasks/3/local-script-approvals`):**
   - Because `grain demo` does not dispatch container sandboxes or fork `grain mcpserver`, we granted `local-scripts:deploy` to Task 3 in SQLite (`INSERT OR REPLACE INTO task_grant (task_id, capability, via) VALUES ('3', 'local-scripts:deploy', 'label')`) and issued the same `POST /api/tasks/3/local-script-approvals` requests that `daemonLocalScriptApprover` sends from `grain mcpserver` (`call-inbox-1` for `/inbox` and `call-detail-1` for `/tasks/3`).
3. **Stub 3 — `POST /api/demo/fake-ci/trigger` for Task 13:**
   - Used `grain demo`'s built-in fake-CI endpoint (`POST /api/demo/fake-ci/trigger` with `{"taskId":"13"}`) to start a `fake-ci-1` run on Task 13 (`Roll the ingress controller to v2`), surfacing the grain CI verdict banner and `Show the run` button on `/tasks/13`.
4. **Stub 4 — `task_observation` state for `pending_secret` (Task 4) and `suspended_session_*` (Task 8):**
   - Updated `task_observation` in the throwaway SQLite DB (`pending_secret='stripe-api-key'` on Task 4; `suspended_session_token='sess-walk-9a8b7c'`, `suspended_session_framework='antigravity'`, `suspended_session_run_id='demo-run-8-1'`, `suspended_session_at='2026-10-22 09:15:00'` on Task 8) so `PendingSecret` and the **Suspended session** fieldset could be exercised against the real server and encrypted secrets store.
5. **Stub 5 — Playwright route interception (`503 Service Unavailable`) for `DisconnectedBanner`:**
   - Used `page.route("**/api/**", ...)` to return `503 Service Unavailable` across two 3-second poll ticks, confirming `DisconnectedBanner` (`data-testid="disconnected-banner"`) appears while preserving on-screen task state, and clears automatically once `page.unroute("**/api/**")` restores connectivity.
6. **Note on Cross-team approvals:**
   - Since the single-tenant reset (`e2bcdd105`), `pkg/ui` no longer populates `crossTeamApprovals` on `TaskDetail`. The frontend component (`ui/src/components/detail/CrossTeamApprovals.jsx`) remains covered by `ui/src/components/detail/CrossTeamApprovals.test.jsx` and renders nothing when `t.crossTeamApprovals` is empty.

---

## Step-by-Step Walkthrough & Outcomes

### 1. Access-Code Gate & Inbox (`/inbox`)

| Step | Action | Expected & Observed Outcome | Verdict |
| :--- | :--- | :--- | :--- |
| 1a. Access-code gate | Open `/inbox` unauthenticated; submit wrong code `wrong-code-12345`, then valid demo access code | Unauthenticated `GET /inbox` served the access-code gate (`401`). Submitting `wrong-code-12345` returned `401` and rendered `[role="alert"]` `"That access code was not accepted."` Submitting the valid code signed in and redirected to `/inbox`. | PASS |
| 1b. Cold load & rail badge | Inspect `/inbox` and left navigation rail | Rail button showed `Inbox 6` with `aria-current="page"`. Clicking `All tasks` navigated to `/` and clicking `Inbox` in the rail returned to `/inbox`. | PASS |
| 1c. Seeded groups & counts | Inspect group headings and row counts in `/inbox` | Four seeded groups rendered in priority order: **Answer a question (2)** (Task 4 `#4`, Task 7 `#7`), **Submit for merge (2)** (Task 5 `#5`, Task 13 `#13`), **Unblock a merge (1)** (Task 14 `#14`), and **Approve a proposal (1)** (Task 1 `#1`). | PASS |
| 1d. Row navigation (mouse & keyboard) | Click Task 1 title link in `/inbox`, then browser `Back`; focus Task 4 title link, press `Enter`, then press `Escape` | Clicking Task 1 opened `DetailOverlay` at `/tasks/1`; `Back` returned to `/inbox`. Focusing `/tasks/4` and pressing `Enter` opened `/tasks/4`; pressing `Escape` closed the dialog and restored `document.activeElement` to the `/tasks/4` link in `/inbox`. | PASS |
| 1e. Approve seeded proposal | Click `Approve` on Task 1 (`Add a dark mode toggle`) in **Approve a proposal** | `POST /api/tasks/1/approve` returned `200`. **Approve a proposal** group disappeared, rail badge decremented `6 -> 5`, and `GET /api/tasks/1` confirmed `state: "queued"`, `approved: true`. | PASS |
| 1f. Answer seeded parked question | Click `Reply` on Task 4 (`Investigate the merge queue stall`); test blank/whitespace validation, `Escape`, `Cancel`, and `Send` | Question text (`"Two PRs are both stuck failing the same lint check..."`) rendered. `Send` stayed disabled when empty or whitespace-only (`"   "`). Pressing `Escape` inside the reply box kept it open; `Cancel` closed it. Re-opening and sending `"Fix both PRs and unblock the merge queue."` posted `POST /api/tasks/4/comments` (`200`), removed Task 4 from **Answer a question** (`2 -> 1`), decremented rail badge `5 -> 4`, and moved Task 4 to `state: "queued"` in the **Queued** rail view (persisted across reload). | PASS |
| 1g. Local scripts waiting for approval | Trigger `call-inbox-1` on Task 3 (`deploy_check`: `/usr/local/bin/deploy-check "--env" "staging"`), reload `/inbox`, click `Deny` | **Local scripts waiting for approval (1)** appeared at the top of `/inbox` showing the exact quoted command preview and `Task 3` navigation button. Clicking `Deny` sent `POST /api/tasks/3/local-script-approvals/.../answer` (`{"approve":false}`), removed the group, and recorded the denial comment on Task 3. | PASS |
| 1h. Remaining Inbox groups & empty state | Click `Submit all (2)` in **Submit for merge**, dismiss `window.confirm`, then accept; click `Acknowledge` on Task 7; click `Open pull request` & `Retry merge` on Task 14; reload `/inbox` | Dismissing `window.confirm("Turn auto-merge back on for all 2 pull requests waiting for submission?")` sent no requests. Accepting submitted Tasks 5 and 13 and cleared **Submit for merge**. Clicking `Acknowledge` on Task 7 (`nothing_needed` ack) closed Task 7. On Task 14, **Open pull request** linked to `https://github.com/acme/widgets/pull/132` (`target="_blank"`), and clicking `Retry merge` cleared **Unblock a merge**. `/inbox` rendered `"Nothing waiting on you. Parked questions, proposals and stuck merges show up here."`, the rail badge cleared (`Inbox`), and the empty state persisted across reload. | PASS |

### 2. New Task Intake (`/new`, Rail Menu, Prefill Links, Model Picker)

| Step | Action | Expected & Observed Outcome | Verdict |
| :--- | :--- | :--- | :--- |
| 2a. Entry points & focus | Open via top-bar `+ New task`, rail `Create options` (`aria-label="Create options"`) -> `New task`, and direct `/new` navigation | All three entry points opened `dialog` `"New task"` at `/new` and automatically focused the `Title` input (`document.activeElement.getAttribute("placeholder") === "Short summary of what needs doing"`). | PASS |
| 2b. Validation & draft persistence | Check `Create task` disabled state on empty/whitespace `Title`; test `Create a new branch` empty-name validation; close via `Escape` and reopen; click `Discard draft` | `Create task` remained disabled with empty or whitespace-only `Title`. Switching Base branch mode to `Create a new branch` with a blank branch name disabled `Create task` and displayed `"Name the branch to create."` Closing with `Escape` preserved the draft in `sessionStorage` (`grain:new-task-draft:v1`) and showed `(draft)` on `+ New task (draft)`; clicking `Discard draft` cleared the draft and closed `/new`. | PASS |
| 2c. Queued task creation (Task 15) | Fill Title (`"Walkthrough queued intake task"`), Description, attach `walkthrough-note.txt` (`18 B`), select `acme/widgets`, Base branch `release/1.0`, Read-only repo `acme/docs-site`, Queue directly, Auto-merge, Priority `high`, Area `backend` + `frontend`, Requested by `"Ada Lovelace"`, Due date `2026-11-30`, Estimate `3`, Customer-facing `Yes`, Capabilities `Self-debug` + `GCP service account key`, Agent `Antigravity`, Model `gemini-2.5-pro (Antigravity)` pinned to `team-gemini`, Network `None (this deployment only)`, When nothing is needed `Leave for acknowledgement`, Prompt extension override, Resource overrides (`2` vCPUs, `4096` MiB, `20` GiB), and `Jump the queue`; click `Create task` | `POST /api/tasks` (`201`) followed by `POST /api/tasks/15/attachments` (`201`). `GET /api/tasks/15` verified every persisted field (`state: "queued"`, `approved: true`, `repo: "acme/widgets"`, `base: "release/1.0"`, `reads: ["acme/docs-site"]`, `autoMerge: true`, `customFields`, `capabilities: ["gcp-key", "self-debug"]`, `agentFramework: "antigravity"`, `model: "gemini-2.5-pro"`, `agentBackendId: "team-gemini"`, `networkAccess: "none"`, `nothingNeededAction: "ack"`, `promptExtension`, `sandboxCpus: 2`, `sandboxMemoryMb: 4096`, `sandboxDiskGb: 20`, and `attachments: [{filename: "walkthrough-note.txt"}]`). | PASS |
| 2d. Proposed No-repo task creation (Task 16) | Open `/new`, fill Title (`"Walkthrough proposed no-repo task"`), select `No repo`, verify Base branch and Auto-merge controls hide, uncheck `Queue directly (skip proposal review)` (`Create task` button relabeled to `Propose task`), add dependency `#15` in `Depends on`, and submit | `POST /api/tasks` (`201`) created Task 16 with `state: "proposed"`, `approved: false`, `noRepo: true`, `repo: ""`, `base: ""`, `dependsOn: ["15"]`, `blocked: true`. | PASS |
| 2e. Prefill links & legacy `/t/<team>/new` | Navigate to `/new?field.priority=low&field.area=docs&field.area=infra&field.requested-by=Ops+desk`; also test `/t/default/new?field.priority=high` | `/new?field...` opened the `New task` dialog with `Priority = low`, `Area = [docs, infra]`, and `Requested by = "Ops desk"` prefilled. Legacy `/t/default/new` fell back cleanly to `/` in single-tenant routing without console or page errors. | PASS |
| 2f. Model picker vs. enabled connections | Inspect `/new` Model picker options against `/settings/models`; disable `team-gemini` connection in `/settings/models` (`Switch`), re-inspect `/new` Model picker, then re-enable `team-gemini` | With `team-gemini` enabled, `gemini-2.5-pro (Antigravity)` was selectable without warning and offered the `team-gemini` connection pin. When `team-gemini` was disabled in `/settings/models`, `/new` labeled those models `(disabled connections only)` and hid `team-gemini` from the connection pin dropdown. Re-enabling `team-gemini` restored normal availability. | PASS |

### 3. All Tasks, State Views, Blocked View, Search, Sort, and Compound Narrowing

| Step | Action | Expected & Observed Outcome | Verdict |
| :--- | :--- | :--- | :--- |
| 3a. Search & URL persistence | Type `"Walkthrough queued intake"` in `Search tasks…`, check URL `?q=`, reload page, clear search via `Clear the search box` | List narrowed to Task 15 (`1` row), URL updated with `q=Walkthrough+queued+intake`, reload preserved the search query and single row, and clearing restored the full list. | PASS |
| 3b. Sort order | Open `Sort tasks` menu (`button[title^="Sort tasks"]`), select `Priority`, verify URL `sort=priority`, reload, then restore `Queue order` | Selecting `Priority` updated the URL with `sort=priority`, persisted across `page.reload()`, and ordered high-priority tasks ahead of medium/low tasks. | PASS |
| 3c. Compound narrowing & Back/Forward | Open `Filters` (`Add and edit the conditions this list is narrowed by`), add `Priority = high` (`f=field.priority:is:high`), then `Area = backend` (`f=field.area:is:backend`); reload; exercise `page.goBack()` and `page.goForward()`; click `Clear` | Both filter chips rendered and narrowed the list to matching tasks. `page.reload()` preserved both `f=` URL parameters. `page.goBack()` removed the second filter (`f=field.area:is:backend`) and `page.goForward()` restored it. Clicking `Clear` reset the view to its baseline filters. | PASS |
| 3d. Rail state views & Blocked view | Visit `Queued`, `Running`, `Awaiting reply`, `Failed`, `Completed`, `Deferred`, `Closed`, `/?state=merged`, and `Blocked` (`/?blocked=1`) | Every view displayed only tasks in that state (or blocked tasks with unresolved dependencies in `Blocked`, matching the rail count `2`). With Bug #1 fixed (see below), arriving on `Deferred`, `Closed`, or `Merged` showed no spurious `Clear` button, narrowing and clicking `Clear` on `Deferred` and `Closed` restored those states' rows instead of wiping them out, and an empty state view (`Awaiting reply` after all questions were answered) displayed `"No tasks in this state."` rather than `"No tasks match these filters."` | PASS |

### 4. Task Detail Lifecycle & Sections (`/tasks/<id>`)

| Step | Action | Expected & Observed Outcome | Verdict |
| :--- | :--- | :--- | :--- |
| 4a. Entry points & `"File a task with these fields"` | Open `/tasks/15` from list row, from `/inbox`, and via direct URL; click `"File a task with these fields"` | `DetailOverlay` opened `dialog` `"Task 15: Walkthrough queued intake task"`. Clicking `"File a task with these fields"` navigated to `/new?repo=acme%2Fwidgets&base=release%2F1.0&field.area=backend&field.area=frontend&field.customer-facing=true&field.due-date=2026-11-30&field.estimate=3&field.priority=high&field.requested-by=Ada+Lovelace` with all fields prefilled. | PASS |
| 4b. Edit Title, Description, Model & unsaved-changes guard | Click `Edit` on `/tasks/15`, modify Title, press `Escape` and dismiss `window.confirm` (`"Discard your unsaved changes?"`), then click `Save` | Dismissing the unsaved-changes confirmation kept the editor open with the draft intact. Clicking `Save` sent `PATCH /api/tasks/15` (`200`), updated the heading to `"Task 15: Walkthrough queued intake task (edited)"` and model to `gemini-2.5-flash`, and persisted across reload. | PASS |
| 4c. Edit Repo, Base branch suggestion, Custom fields & Auto-merge | On `/tasks/16` (originally `No repo`), change Repo to `acme/widgets`, click `"Use release/1.0"` on the base-branch suggestion banner (`"other tasks on acme/widgets branch off release/1.0"`), edit Custom fields (`Priority = medium`, `Requested by = "Grace Hopper"`), and toggle `Auto-merge` | Changing Repo to `acme/widgets` surfaced the base-branch suggestion alert; clicking `Use release/1.0` set `base: "release/1.0"`. Custom field edits and `Auto-merge` checkbox toggles persisted via `PATCH /api/tasks/16` across reload. | PASS |
| 4d. Proposal approval & Defer / Undefer lifecycle | On `/tasks/16` (`proposed`), try deferring to past date `2026-01-01`; defer indefinitely (`Defer`); `Undefer`; defer with future date `2030-12-31`; click `Approve` while deferred; click `Withdraw approval`; click `Undefer`; click `Approve` | Deferring to `2026-01-01` returned `400 Bad Request` and displayed alert `"wake time 2026-01-01T00:00:00Z is not in the future..."`. Indefinite `Defer` transitioned Task 16 to `deferred`. Dated `Defer` (`2030-12-31`) set `deferredUntil: "2030-12-31T00:00:00Z"` and displayed `"Picks itself back up on 12/31/2030, 12:00:00 AM."` Clicking `Approve` while deferred set `approved: true` while keeping `state: "deferred"` and `deferredUntil: "2030-12-31T00:00:00Z"`, displaying `"Approved — will enter the queue when picked back up"` and `Withdraw approval`. Withdrawing approval, undeferring back to `proposed`, and clicking `Approve` transitioned Task 16 to `queued`. | PASS |
| 4e. Dependencies, hover tooltip & Blocked rail count | On `/tasks/15`, add `#2` in `Depends on`, hover over the `#2` dependency chip, check `Blocked` rail count and `/blocked` view, then remove `#2` | Adding `#2` (`POST /api/tasks/15/dependencies`) rendered chip `#2` with `.badge-queued` dot, added a `.badge-blocked` `"blocked"` badge to Task 15, incremented the `Blocked` rail count `2 -> 3`, and showed `"2 Fix flaky retry test — Queued (blocking this task)"` on hover (`[role="tooltip"]`). Removing `#2` (`DELETE /api/tasks/15/dependencies/2`) removed the chip and decremented `Blocked` rail count `3 -> 2`. | PASS |
| 4f. Work tree & provenance disclosure | Open `/tasks/3`, expand `.provenance-toggle` (`"Work from task 12: 0 of 4 done"`), inspect nested work tree and PR chips, click `#8` in the tree, then browser `Back` | Expanding the disclosure revealed the full hierarchy rooted at `#12 Split the monolith's session store out of pkg/ui` (`merged`, with GitHub PR link `acme/widgets#104`) -> children `#1`, `#3 (this task)`, and `#8` -> grandchild `#11`. Clicking `#8` navigated to `/tasks/8`, and `Back` returned to `/tasks/3`. | PASS |
| 4g. Full prompt & Live / Timeline Attempt Transcripts | On `/tasks/3`, click `Prompt` to inspect `PromptOverlay`; inspect `Live Attempt #1 Transcript` on `/tasks/3`; click `Transcript` on Attempt #1 in the timeline | `PromptOverlay` (`dialog` `"Prompt for task 3"`) rendered token breakdown (`~183 tokens across 2 sections`) and the full assembled prompt markdown. On `/tasks/3`, both the inline `Live Attempt #1 Transcript` viewer and the `Attempt #1 transcript` modal rendered the structured agent tool call (`run_command`) and reasoning turns. | PASS |
| 4h. Local script approval on cached task detail pane | With `/tasks/3` already open and cached, trigger `call-detail-1` (`./scripts/deploy.sh "--env=staging"`, `42 bytes on stdin`) and click `Approve` | With Bug #2 fixed (see below), the next poll on `/tasks/3` invalidated `taskDetailCache` and rendered `.local-script-approval` displaying `./scripts/deploy.sh "--env=staging"` and `42 bytes on stdin`. Clicking `Approve` resolved the call, cleared the prompt, and appended the approval comment to the timeline. | PASS |
| 4i. Merge policy & grain CI verdict link | Trigger `fake-ci` on Task 13; inspect `data-testid="merge-policy"` on `/tasks/13` and `/tasks/14`; click `Show the run` on `/tasks/13` | Both Task 13 (`for a1b2c3d4e5f6`) and Task 14 (`for b2c3d4e5f607`) rendered `Merge rules (matched deploy)` with requirements `ci.mode: both — from rule deploy`, `auto_merge: never — from rule deploy`, `stall_timeout: 30m — from rule deploy`, and `GitHub CI is narrowed to build, cla/google`. On `/tasks/13`, clicking `Show the run` opened `CIRunOverlay` (`dialog` `"CI run fake-ci-1"`, `"asked by task 13"`), and its Back button returned to `/tasks/13`. | PASS |
| 4j. Pending secret request box | Set `pending_secret='stripe-api-key'` on Task 4, open `/tasks/4`, verify `Set secret` disabled when empty and input `type="password"`, submit `sk_live_walkthrough_never_in_timeline_778899`, reload, and audit DOM, API JSON, and SQLite dump | Submitting the secret called `PUT /api/tasks/4/secret` (`200`), cleared the secret box, un-parked Task 4 to `queued`, and posted `"Set stripe-api-key. Its value is in grain's secret store, not in this conversation -- resolve it by name."` The secret value never appeared in `page.content()`, `GET /api/tasks/4`, or `sqlite3 grain.db .dump`. | PASS |
| 4k. Suspended agent session | Open `/tasks/8` and inspect the `Suspended session` fieldset | Rendered `"Left by demo-run-8-1 on 10/22/2026, 9:15:00 AM (Antigravity)."` and read-only session token `sess-walk-9a8b7c`. | PASS |
| 4l. Internal PR link & PullRequestPage diff | On `/tasks/15`, select branch `grain/task-15`, click `Internal PR` (`/repos/acme/widgets/compare/release/1.0...grain/task-15`) | `PullRequestPage` rendered heading `grain/task-15`, chip `Open`, subtitle `Into release/1.0 on acme/widgets`, and the `Branch changes` diff region (`widget.go`, `+3 -0`, `// Walkthrough diff line added on grain/task-15`). | PASS |

### 5. Multi-Row Selection & Bulk Edit

| Step | Action | Expected & Observed Outcome | Verdict |
| :--- | :--- | :--- | :--- |
| 5a. Multi-select & off-screen selection warning | In `Queued` view, tick checkboxes on Tasks 15 and 16 (`2 selected`); switch to `Running` view; click batch `Close` and dismiss `window.confirm` | Switching to `Running` preserved `2 selected` and displayed `"none of them shown here"`. Clicking `Close` prompted `"Close 2 tasks? 2 of them are not shown in this view."`; dismissing left both tasks `queued`. | PASS |
| 5b. Bulk edit mixed labels, Cancel & Apply | Return to `Queued`, click `Bulk edit…`, verify `(mixed across the selection)` labels on `Priority` and `Requested by`; click `Cancel`; reopen `Bulk edit…`, tick and set `Priority = low`, `Requested by = "BulkTeam"`, `Due date = "2026-12-15"`, and click `Apply to 2 tasks` | `Cancel` closed the dialog with zero `PATCH` requests and kept `2 selected`. Clicking `Apply to 2 tasks` sent `PATCH /api/tasks/15` and `PATCH /api/tasks/16`, cleared the selection, and preserved Task 15's untouched custom fields (`area: ["backend", "frontend"]`, `estimate: "3"`, `customer-facing: "true"`) alongside the updated fields across reload. | PASS |

### 6. Non-UTC Browser Timezone Deferral & DisconnectedBanner Resilience

| Step | Action | Expected & Observed Outcome | Verdict |
| :--- | :--- | :--- | :--- |
| 6a. `Asia/Tokyo` browser wake-date deferral | Open `/tasks/16` in a Playwright context with `timezoneId: "Asia/Tokyo"`, pick `2030-12-31` in `Until (optional)`, and click `Defer` | Stored `deferredUntil: "2030-12-31T00:00:00Z"` (midnight in the deployment's `UTC` timezone rather than Tokyo local midnight). | PASS |
| 6b. `DisconnectedBanner` outage & recovery | Intercept `**/api/**` with `503 Service Unavailable` for two poll ticks, then unroute | `data-testid="disconnected-banner"` appeared after the second consecutive failed poll tick while keeping the task list on screen, and automatically disappeared on the next healthy poll tick. | PASS |

---

## Console & Network Error Audit

- **Uncaught page errors (`pageerror`):** `0`
- **Unexpected console errors or warnings:** `0`
- **Expected negative-test HTTP responses (`>= 400`) observed:**
  1. `GET /inbox` -> `401 Unauthorized` (initial unauthenticated navigation to access-code gate)
  2. `POST /api/auth/access-code` -> `401 Unauthorized` (deliberate invalid access code test)
  3. `POST /api/tasks/16/defer` -> `400 Bad Request` (`{"error":"wake time 2026-01-01T00:00:00Z is not in the future: a deferral until a moment that has passed would be undone by the next cycle"}`)
  4. `GET /api/*` -> `503 Service Unavailable` (deliberate outage simulation for `DisconnectedBanner`)

---

## Bugs Found & Fixed

### Bug 1: Rail state views (`Queued`, `Running`, `Deferred`, `Closed`, `Merged`) treated their own baseline as narrowed and wiped `Deferred`/`Closed`/`Merged` rows on `Clear`

- **Location:** `ui/src/components/TaskList.jsx` (`defaults`)
- **Deterministic reproduction:**
  1. Start `grain demo` and click `Deferred` or `Closed` in the left rail (or navigate to `/?state=deferred` or `/?state=closed`).
  2. Observe that `Clear` is immediately visible in the filter bar even though no search, sort, or filter was applied.
  3. Click `Clear` (or navigate to an empty state view such as `Awaiting reply` after answering all questions).
  4. On `Deferred`, `Closed`, and `Merged`, clicking `Clear` wiped out every task in the view (`No tasks match these filters.`). On any empty state view, the empty-state message read `"No tasks match these filters."` with a `"Clear filters"` button instead of `"No tasks in this state."`
- **Root cause:**
  - `App.jsx`'s `listDefault()` and `selectStateFilter()` strip `c.attr === "state"` from `defaultFilters(config)` whenever `stateFilter !== "all" && stateFilter !== "blocked"`, because All tasks' default `state:none:deferred|closed` exclusion would otherwise hide `Deferred`, `Closed`, and `Merged` tasks in their own rail views.
  - However, `TaskList.jsx` computed `const defaults = defaultFilters(config);` without stripping `c.attr === "state"` on state views. Consequently, `isNarrowed(narrowing, defaults)` always saw a mismatch between `narrowing.filters` (no `state` clause) and `defaults` (`state:none:deferred|closed`), and clicking `Clear` called `onNarrowingChange({ ...emptyNarrowing(), filters: defaults })`, injecting `state:none:deferred|closed` into the state view.
- **Fix:**
  - Updated `defaults` in `ui/src/components/TaskList.jsx` to strip `c.attr === "state"` when `stateFilter !== "all" && stateFilter !== "blocked"`, matching `App.jsx`.
- **Regression coverage:**
  - Unit tests in `ui/src/components/TaskList.test.jsx` (`keeps a state view un-narrowed on arrival and restores its rows on Clear`) and `ui/src/App.narrowing.test.jsx` (`clears a narrowed state view back to that state's own defaults`).
  - Playwright e2e test in `ui/e2e/task_intake_inbox_lifecycle_walkthrough.spec.js` (`keeps a rail state view across reload, Back and Forward`).

### Bug 2: `GET /api/tasks/{id}` (`taskDetailCache`) served stale cached `TaskDetail` (or `304 Not Modified`) when in-memory `LocalScriptApprovals` changed

- **Location:** `pkg/ui/task_detail_cache.go`, `pkg/ui/local_script_approvals.go`
- **Deterministic reproduction:**
  1. Open `/tasks/3` in the browser so `GET /api/tasks/3` populates `taskDetailCache` and the browser stores its `ETag`.
  2. Have a run request a local-script approval (`POST /api/tasks/3/local-script-approvals`) without any concurrent SQLite write.
  3. Wait for `App.jsx`'s 3-second conditional poll (`GET /api/tasks/3` with `If-None-Match`) or reload `/tasks/3`.
  4. Observe that `GET /api/tasks/3` answered `304 Not Modified` (or returned the cached body without `localScriptApprovals`) and `.local-script-approval` never appeared on `/tasks/3`.
- **Root cause:**
  - `taskDetailCache.response` keyed cached `TaskDetail` bodies and `ETag`s solely on `Store.ChangeToken(ctx)` (SQLite's `PRAGMA data_version`).
  - `TaskDetail.LocalScriptApprovals` comes from `LocalScriptApprovals` in memory (`pkg/ui/local_script_approvals.go`), not SQLite. Requesting or expiring a local-script approval does not write to SQLite, so `Store.ChangeToken(ctx)` did not advance.
- **Fix:**
  - Added a monotonic `ver int64` counter and `version(now time.Time) int64` method to `LocalScriptApprovals` in `pkg/ui/local_script_approvals.go` (incremented whenever an approval is requested, settled, or pruned on deadline expiry) and folded `:lsa=<ver>` into `taskDetailCache.response`'s cache token in `pkg/ui/task_detail_cache.go`.
- **Regression coverage:**
  - Go unit test `TestTaskDetailRebuildsWhenLocalScriptApprovalRequestedOrExpired` in `pkg/ui/task_detail_cache_test.go`.
  - Playwright e2e test `surfaces a local-script approval on an already-cached task pane` in `ui/e2e/task_intake_inbox_lifecycle_walkthrough.spec.js`.
