# Task intake, Inbox and lifecycle UI walkthrough — 2026-10-26

- **Date:** 2026-10-26, 08:10–09:30 UTC
- **Task:** grain/049f1e09/task-1144, base `main`
- **Commit tested:** `905ce6f55` (`main` at the start of the run, "Merge pull
  request #4849"). The fix below was then checked on the same tree with the
  fix applied (`896d1893e`, `09b234af1` on `grain/049f1e09/task-1144`).
- **Environment:**
  - Debian GNU/Linux 12 (bookworm), kernel `6.1.0-54-amd64`, 7 vCPUs, `linux/amd64`
  - Go `go1.26.10`, Node.js `v20.19.0`, npm `10.8.2`
  - Playwright `1.62.1`, headless Chromium `chromium-1234`
    (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC, 1400×900
    unless stated; 1440×900 and 390×844 in light and dark for the fix checks
  - Server: `cd ui && npm ci && npm run build`, then `go build ./cmd/grain` and
    `grain demo -addr 127.0.0.1:<port> -open=false -github-sim -fake-ci -merge-rules
    -secrets-store -access-code-file <file>`, with `TMPDIR` under `/var/tmp`, so
    each server had its own throwaway embedded SQLite store. There were two servers:
    `:8420` built from the unmodified commit (the Inbox steps and the "before"
    checks) and `:8421` built with the fix (everything from New task on, and the
    "after" checks).
  - There was no `/home/debian/.gemini-api-key`, so no model was called, and
    `LOOK=1` could not describe the screenshots. The demo's one agent connection,
    `gemini-pro` (Antigravity), advertises `gemini-2.5-pro` and `gemini-2.5-flash`.
- **How it was driven:** throwaway Playwright scripts (not committed). Each one
  signed in with `POST /api/auth/access-code` and then acted only through visible
  controls. Every page recorded `pageerror`, console errors and warnings, HTTP
  responses of 400 or above, failed requests, and every non-GET `/api/` request
  with its body. The REST API was used only to read back what a gesture had
  persisted, except where a stub below says otherwise. I also ran the committed
  `ui/e2e/task_intake_inbox_lifecycle_walkthrough.spec.js`. On the unmodified
  commit its 10 tests passed. With the new test it has 11, and all 11 pass.

## Stubs

Everything ran against the real server, store, built frontend and browser. Only
these boundaries were replaced:

1. **Grain CI run for task 13.** `-fake-ci` accepts runs but never executes them.
   I started one with `POST /api/ci/runs {"taskId":"13"}` (202, `fake-ci-1`),
   because no pane control starts a run for a task in that state.
2. **Pending secret and suspended session.** Only an agent run writes these. With
   `sqlite3` (installed with apt) on the throwaway store I set
   `task_observation.pending_secret='stripe-api-key'` for task 4, and the
   `suspended_session_token/framework/run_id/at` columns for task 8
   (`sess-tok-walk-8`, `antigravity`, `demo-run-8-1`).
3. **An agent's push.** I committed `walkthrough.go` on `grain/task-15` and pushed
   it into the `-github-sim` bare repository (`githubsim/acme/widgets.git`). That
   gives the Internal PR link a branch to compare.
4. **A failed filing.** `page.route` answered `POST /api/tasks` with a 500 once, to
   see the form's error state.
5. **The new e2e test only.** It reads the real `GET /api/config` and rewrites
   `gemini-key` to `ready: false`, because the spec's own demo reports it as ready.
   Everything else in that test is real.

## Steps and outcomes

### Inbox (`:8420`)

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 1 | Open `/inbox` directly | Groups in order: *answer a question 2* (#4, #7, "acknowledge all (2)"), *submit for merge 2* (#6, #13, "submit all (2)"), *unblock a merge 1* (#14), *approve a proposal 1* (#1). Rail Inbox `6`. No unnamed buttons on the page. | PASS |
| 2 | From `/`, rail Inbox | `/inbox`. | PASS |
| 3 | Approve #1 from its row | `POST /api/tasks/1/approve`. The proposal group goes away; #1 is `queued`, `approved: true`. | PASS |
| 4 | Reply to #4: Send is disabled while empty; type a reply; Send | `POST /api/tasks/4/comments`. #4 leaves the question group and is `queued`. After reload the rail Inbox shows `4` and the reply is on #4's timeline. | PASS |
| 5 | (`:8421`) Acknowledge #7, Submit all (confirm), open #14 and Close it, Escape | `close` for 7; `submit` for 6 and 13, behind the confirm "Submit all 2 finished changes for merge?…"; `close {"close_pull_request":false}` for 14. Inbox shows "Nothing is waiting on you. Every task is either running, queued or done." The rail Inbox chip drops its count, and the empty state survives a reload. | PASS |

### New task (`:8421`)

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 6 | Rail "Create options" | Menu: New task, New session, New board. New task opens `/new`. | PASS |
| 7 | Validation | Create is disabled with a whitespace title, and with a title but no repo choice (`—`). | PASS (O1 of 10-23 still holds: nothing says why) |
| 8 | Capabilities: untick the pre-ticked, not-ready **Gemini key** | **Bug 1** on the unmodified commit (`:8420`): the row disappears from the open menu, it cannot be re-ticked, focus leaves the list, and in the long scripted run Escape no longer closed the menu. | FAIL → PASS |
| 9 | Queued path with every field: title, Markdown description, attachment `attach.txt`, Priority high, Area frontend, Requested by, Due date, Estimate 3, repo acme/widgets, base `release/1.0`, auto-merge off, untick Gemini key, model gemini-2.5-flash, Advanced: connection Gemini 2.5 Pro, network none, 4 vCPUs / 4096 MiB / 30 GiB | Task 15 is filed `queued` with exactly those values: `base: release/1.0`, `autoMerge: false`, `model: gemini-2.5-flash`, `agentBackendId: gemini-pro`, `networkAccess: none`, sandbox 4/4096/30, capabilities `gcp-key, grain-ci, self-debug`, all five custom fields, attachment id 1. | PASS |
| 10 | Model picker | Options are `Deployment default (gemini-2.5-pro)`, `gemini-2.5-pro`, `gemini-2.5-flash`, exactly what the one enabled connection advertises. Typing `flash` leaves only `gemini-2.5-flash`. | PASS |
| 11 | Proposed path: No repo, untick "Queue immediately" | Task 16 filed `proposed`, `repo: ""`, `noRepo: true`. | PASS (O2) |
| 12 | `/t/default/new?field.priority=low&field.area=docs&field.area=infra&field.requested-by=Support%20team` | Lands on `/new?…` with Priority low, Area docs + infra, and Requested by "Support team". | PASS |
| 13 | Cancel: ✕, reopen `/new`; Discard draft | The draft is kept and "Discard draft" is offered; discarding empties the title. | PASS |
| 14 | Escape after picking an Area | The Area popup closes on pick, so this Escape closes the dialog. The draft is kept. That is consistent: an Escape with an open popup closes only the popup. | PASS |
| 15 | Error state (stub 4) | The dialog stays open with the alert "stubbed failure", the title is kept, and Create is enabled again. | PASS |
| 16 | Keyboard | From Title, 29 Tabs reach Create task in reading order, through every field. Enter on it files the task. | PASS |

### Task list (`:8421`)

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 17 | Search `pagination` | #8, #11. URL `?q=pagination&f=state:none:deferred|closed&f=kind:none:interactive`. | PASS |
| 18 | Filters › Filter by… › Priority › high, with and without the search | Nothing matches with the search. Without it: #2, #4, #5, #15. `f=custom.priority:any:high` is in the URL. | PASS |
| 19 | Sort by… › Title | The rows read F…, I…, R…, W…, and `sort=title:asc` is in the URL. The sort offers 28 attributes, custom fields included. | PASS |
| 20 | Rail Blocked, then Queued | Each view is its own URL (`?state=blocked…`, `?state=queued…`) and keeps the sort and the Priority filter. Queued shows #2 and #15. | PASS |
| 21 | Back, Back, Forward, reload | Each returns the matching URL and rows; the reload restores the Blocked view with its narrowing. | PASS |

### Task detail (`:8421`)

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 22 | Open #15 from its list row, then Escape | `/tasks/15`; Escape returns to `/`. Inbox rows and the work tree's links also open panes. | PASS |
| 23 | Edit → change title → Cancel | Nothing is saved. | PASS |
| 24 | Edit title and description → Save; Base branch `main` + Tab; tick auto-merge; Edit fields: Priority low, Requested by; Save fields; reload | `PATCH /api/tasks/15` each time, and everything is there after reload (`base: main`, `autoMerge: true`, the fields). The auto-merge box follows the server about 100 ms after the click rather than flipping at once. | PASS |
| 25 | Repo picker | `acme/widgets`, `Other…`. | PASS |
| 26 | Defer #16 until 2030-01-15 → Undefer → Defer with no date → reload → Undefer → Approve | `deferred`, `deferredUntil 2030-01-15T00:00:00Z`, with the note "Picks itself back up on 1/15/2030 … back into the proposals". Undefer: `proposed`, and the Until box is empty. Dateless Defer: `deferredUntil ""`, which survives the reload. Undefer: `proposed`. Approve: `queued`, `approved: true`. | PASS |
| 27 | Dependency: type `toolchain` in "Add a dependency…" on #15, pick #3; hover the chip; open the Blocked view; Delete on the focused chip; reload | The chip's name and tooltip read "3 Bump the Go toolchain to 1.24 — Running (blocking this task)". #15: `dependsOn ["3"]`, `blocked: true`, state oval "blocked". The rail's Blocked count goes 1 → 2, and the Blocked view lists #11 and #15. Delete: `attach:false`, `blocked: false`, rail back to 1, and after reload "No dependencies." | PASS |
| 28 | Work tree on #11 | Collapsed toggle "Provenance · Work from task 12: **0 of 4 done**"; expanded, "generated from 8 · origin 12", nested 12 › {1 planned, 3 running, 8 PR open (acme/widgets#104) › 11 queued}, every number a link. | PASS |
| 29 | Merge policy on #13 | Every requirement shows the rule behind it: `ci.mode: grain -- from repository setting`, then `ci.mode: both`, `auto_merge: never`, `stall_timeout: 30m`, `ci.grain: full suite`, `ci.github: build` and `ci.github: cla/google`, each `from rule deploy`. The pane adds "GitHub CI is narrowed to build, cla/google", and the reason line names the missing `cla/google` check. | PASS |
| 30 | Grain CI on #13 (stub 1) → Show the run → Escape | "grain CI is running against this task's branch…". The run pane opens (pending, acme/widgets, `grain/task-13`, "No jobs: this repository declares no workflows…"). Escape returns to `/tasks/13`. The URL stays `/tasks/13` while the run is open, which `App.jsx` documents as deliberate for an overlay opened from a task. | PASS |
| 31 | Pending secret on #4 (stub 2): Set secret is disabled while empty; set a value; reload | The box is `type=password`, labelled "Value for stripe-api-key". `PUT /api/tasks/4/secret`; the task is `queued`. The timeline says "Set stripe-api-key. Its value is in grain's secret store, not in this conversation -- resolve it by name." The value is in neither the DOM nor the task JSON, and `grep -rF` finds it in no file under the demo directory (store, WAL, logs, secrets store). | PASS |
| 32 | Suspended session on #8 (stub 2) | Group "Suspended session": "Left by demo-run-8-1 on 10/26/2026, 8:00:00 AM (Antigravity). Any further run of this task resumes this conversation…", with the token shown. | PASS |
| 33 | Cross-team approvals | Not in this build: nothing in `ui/src` or `pkg/ui` mentions them (the single-tenant reset removed them, as both 10-23 runs found). | N/A |
| 34 | Prompt on #3; live transcript | "prompt for 3 · attempt #1 · 8 kb · ~2,049 tokens · 1% of a 200,000-token window", beginning with the title and the repo instructions. Escape returns to the pane. "Live attempt #1 transcript" is inline. | PASS |
| 35 | Internal PR on #15 (stub 3) | Once the server's 5-minute branch cache had refreshed (O5 of 10-23), "Internal PR" opened `/repos/acme/widgets/compare/main...grain/task-15` in a new tab: "grain/task-15 · open · Into main on acme/widgets", with `walkthrough.go +1 -0` in the diff. | PASS |
| 36 | Chat | The demo seeds no chat for these tasks. The interactive Director Session (#12) is left out of All tasks by the default `kind:none:interactive` filter. | N/A |

### Selection and bulk edit (`:8421`)

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 37 | Queued view, tick #2, #11, #15 | "3 selected". | PASS |
| 38 | Batch Close, dismiss the confirm "Close 3 task(s)?" | No requests are sent. | PASS |
| 39 | Bulk edit… → Cancel | No requests; the selection is kept. Rows where the tasks differ say "(mixed across the selection)": Base branch, Auto-merge, Model, Agent framework, Priority, Area, Requested by, Due date, Estimate. | PASS |
| 40 | Bulk edit, change Priority, Escape | "You have unsaved changes here. Leave and discard them?". Dismissing it keeps the dialog open. | PASS |
| 41 | Apply Priority medium, Requested by "Bulk walker", Auto-merge Off to 3 tasks; reload | Three PATCHes carry the full field set, so untouched fields are kept (#11 keeps its estimate; #15 keeps its due date and estimate). The selection clears, and the row chips read `priority medium` after reload. | PASS |

### Console and network

There were no uncaught page errors and no console errors on any page, except the
500 that stub 4 caused on purpose. Every 4xx and 5xx response was either deliberate
or one of my own probes of API paths that do not exist. The
`net::ERR_ABORTED` entries on the 3-second polls are 304 answers to `If-None-Match`
and were filtered out.

## Bug 1: unticking an unready default capability removed it from the menu (fixed here)

- **Reproduction (deterministic, `:8420`):**
  1. Start `grain demo -github-sim …` and open `/new`. Capabilities opens pre-ticked
     with Self debug, GCP key and Gemini key, and the last two say "Not ready".
  2. Open Capabilities and click **Gemini key**.
- **Before:** the Gemini key row leaves the open list at once (`geminiRowAfterUntick: 0`
  at 1440×900 and 390×844, light and dark). Clicking a row from that menu, the one
  thing that ticks it, is no longer possible, so the untick cannot be undone short of
  Discard draft. The focused row unmounts, so focus falls to the menu paper or to
  `<body>`. In the long scripted run, Escape then left the menu open and the
  New task dialog aria-hidden behind it.
- **Cause:** `configuredCapabilities` (`state.js`) hides a capability with
  `ready === false` unless the selection holds it. That rule is meant to keep a held
  grant removable. New task, Template and Schedule passed it the live selection, so a
  row stopped being "held" the moment it was unticked.
- **Fix:** `ui/src/useHeldIds.js` returns every id the form has held since it
  opened. `NewTaskOverlay.jsx`, `TemplateOverlay.jsx` and `ScheduleOverlay.jsx` filter
  against that, so a row stays offered, and tickable, while the form is open. A
  capability that was never held is still hidden, as before. The pickers that save
  on every click (task pane, Settings, Repo defaults) were left alone. For them,
  "held" is what is stored.
- **After (`:8421`):** at both widths and in both themes the row stays, sits inside
  the menu paper, keeps focus, and is ticked again by a second click. Escape closes
  the menu, the paper stays within the viewport, and there are no page errors.
- **Regression coverage:**
  - unit: `NewTaskOverlay.capabilities.test.jsx` › "keeps an unticked unready default
    in the picker so it can be re-ticked". It fails without the fix.
  - e2e: `task_intake_inbox_lifecycle_walkthrough.spec.js` › "keeps an unticked
    unready capability in the open menu" (stub 5). It fails on the unfixed form with
    `toBeVisible` → element not found, and passes with the fix. The whole file passes
    (11/11), as do the New task, Template, Schedule and Bulk edit unit suites
    (102 tests).
- **Rendered:** `node e2e/screenshot.mjs --pages /new --click
  'role=combobox[name="Capabilities"]' --click 'role=option[name=/Gemini key/]'`,
  run against both binaries at desktop and phone widths in light and dark. The
  shots were not committed. The layout claims above come from bounding boxes:
  this sandbox has no image viewer, and there was no Gemini key for `LOOK=1`.

## Observations (not changed)

- **O1:** the rail's **Merging** count includes #14, whose merge the queue has given
  up on ("Merge failed · given up", listed under *unblock a merge*). It is still
  `completed`, the state the rail now calls Merging (PR #4709). A given-up merge
  counted as "Merging" can mislead.
- **O2:** with "No repo" picked, New task still shows Base branch, Create a new
  branch and This fixes a red main. A standalone task sends `base: ""`, so they
  do nothing there.
- **O3:** the task pane's Capabilities chips show `grain-ci` as a raw id beside
  named chips ("GCP key", "Self debug"). It is not in `/api/config`'s
  `capabilities` list, which is where the chip names come from.
- **O4:** the dependency suggestions under "Add a dependency…" are `menuitem`s, not
  `option`s of a combobox. A screen reader hears a menu rather than a list of
  suggestions for the text box.
- **O5:** the #13 timeline still lists "Awaiting submit" (07:38) before
  "Proposed/Queued" (08:28), the seed quirk task 933 recorded.
- **Still true from 2026-10-23:** O1 (Create disabled with no reason given), O3
  (Close in the pane has no confirm while the Inbox row's does), O4 (narrowing edits
  replace the history entry), and O5 (5-minute branch cache before Internal PR
  appears).
