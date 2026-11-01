# Task intake, Inbox and lifecycle UI walkthrough — 2026-10-23

- **Date:** 2026-10-23 (UTC)
- **Task:** grain/eu/task-4671, base `candidate`
- **Commit tested:** `6f656699c` (`origin/candidate` tip at the start of the run). The
  fix below was then verified against the same tree with the fix applied (branch
  `grain/eu/task-4671`).
- **Environment:**
  - Debian GNU/Linux 12 (bookworm), kernel `6.1.0-53-amd64`, `linux/amd64`
  - Go `go1.26.6`, Node.js `v20.19.0`, npm `10.8.2`
  - Playwright `1.62.1`, headless Chromium (`chromium-1234`), 1400×900 unless stated;
    390×844 for the phone checks
  - Server: a wrapper distribution's `cmd/grain` demo -addr 127.0.0.1:<port> -open=false -github-sim
    -fake-ci -merge-rules -secrets-store -access-code-file <file>`, serving the
    production Vite bundle (`cd ui && npm ci && npm run build`, embedded from
    `pkg/ui/static`) over a throwaway embedded SQLite store under `$TMPDIR`. Two
    servers: `:8420` built from the unmodified commit (the walkthrough and the
    "before" shots) and `:8421` built with the fix (verification and "after" shots).
  - No Gemini key was present at `/home/debian/.gemini-api-key`, so nothing here
    used Gemini (the demo's own seeded connection advertises `gemini-2.5-pro` and
    `gemini-2.5-flash`, but no model was ever called).
- **How it was driven:** throwaway Playwright scripts (not committed) that sign in
  through `POST /api/auth/access-code` and then act only through visible controls,
  with `pageerror`, console errors/warnings, HTTP ≥ 400 and failed requests
  recorded on every page. The REST API was read (never written, except where a
  stub below says so) to confirm what each gesture persisted.

## Stubs

Everything ran against the real server, store, built frontend and browser. Only
these boundaries were stood in for:

1. **Grain CI run for task 13.** `grain demo -fake-ci` wires a CI stub that accepts
   runs but never executes anything. A run was started for task 13 through the
   real API (`POST /api/ci/runs {"taskId":"13"}` → `fake-ci-1`), since the pane
   has no control to start one for a task in that state.
2. **Pending secret and suspended session.** Neither is something the demo seeds,
   and both are written by an agent run. With `sqlite3` (installed with apt) on the
   throwaway store: `task_observation.pending_secret='stripe-api-key'` for task 4,
   and `suspended_session_token/framework/run_id/at` for task 8.
3. **A pushed task branch.** `grain/task-15` was committed (`walkthrough.go`, one
   line) and pushed into the `-github-sim` bare repository
   (`$TMPDIR/grain-demo-*/githubsim/acme/widgets.git`), standing in for an
   agent's push so the Internal PR link had a branch to compare.
4. **API outage.** `page.route("**/api/**")` answered 503 for ~8 s and was then
   removed, to see the disconnected banner and its recovery.

## Steps and outcomes

### Inbox

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 1 | Open `/inbox` directly | Groups in order: *answer a question (2)* #4, #7; *submit for merge (2)* #6, #13; *unblock a merge (1)* #14; *approve a proposal (1)* #1. Rail Inbox chip `6`. | PASS |
| 2 | From `/`, click the rail's Inbox | `/inbox`. (Rail items are `role=button` divs, not links.) | PASS |
| 3 | Click #1's title, then Escape | Task pane at `/tasks/1`; Escape returns to `/inbox`. | PASS |
| 4 | Approve #1 | `POST /api/tasks/1/approve`; the proposal group disappears; #1 is `queued`, `approved: true`. | PASS |
| 5 | Reply to #4 ("Fix both, please.") with Send | Send is disabled while empty. #4 leaves the question group, becomes `queued`, and the reply is on its timeline after reload. | PASS |
| 6 | Acknowledge #7 | `POST /api/tasks/7/close`, no confirm (the hint says acknowledge closes it); #7 `closed`. | PASS |
| 7 | Submit all (2): dismiss, then accept | Dismiss sends nothing; accept sends `POST /api/tasks/6/submit` and `/13/submit`; group disappears. | PASS |
| 8 | Close #14 from its pane | Inbox shows "Nothing is waiting on you. Every task is either running, queued or done."; rail Inbox chip disappears. | PASS |
| 9 | Keyboard: Tab through `/inbox` | Row titles and actions are reachable in reading order. | PASS |

### New task

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 10 | Rail "Create options" menu | `new task`, `new session`, `new board`. `+ New task` opens `/new`. | PASS |
| 11 | Validation | Create is disabled with an empty or whitespace title, and with no repo choice (the `—` row). | PASS (see observation O1) |
| 12 | Queued path, every field: title, Markdown description, attachment `attach.txt`, Priority high, Area frontend, Requested by, Due date, Estimate, Customer-facing no, repo acme/widgets, base release/1.0, auto-merge, untick the Gemini key capability, model gemini-2.5-flash, Advanced: connection, network none, vCPUs 4, memory 4096, disk 30 | Task 15 filed `queued` with exactly those values (`base: release/1.0`, `model: gemini-2.5-flash`, `agentBackendId: gemini-pro`, `networkAccess: none`, sandbox 4/4096/30, capabilities `gcp-key, grain-ci, self-debug`, the custom fields, attachment id 1). | PASS |
| 13 | Model picker options | `Deployment default (gemini-2.5-pro)`, `gemini-2.5-pro`, `gemini-2.5-flash`: exactly what the one enabled connection advertises. Typing `flash` narrows it to `gemini-2.5-flash`. | PASS |
| 14 | Proposed path: "No repo", untick "Queue immediately" | Filed `proposed`, `repo: ""`, `noRepo: true`. **First attempt silently did nothing: see Bug 1.** | PASS after fix |
| 15 | Prefill link `/new?field.priority=low&field.area=docs&field.area=infra&field.requested-by=Support%20team` | Priority low, Area docs + infra, Requested by "Support team". | PASS |
| 16 | A task pane's "File a task with these fields" link | `/new?field.area=…&field.priority=…` with that task's values. | PASS |
| 17 | `/t/default/new?field.priority=high` | Lands on the task list with no form and no prefill. | Observation O2 / follow-up |
| 18 | Cancel: close with ✕, reopen | The draft is kept ("Discard draft" offered); Escape in an open Select or Autocomplete closes only the popup. | PASS |

### Task list

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 19 | Search `pagination` | #8, #11; URL `?q=pagination&f=state:none:deferred|closed&f=kind:none:interactive`. | PASS |
| 20 | Add filter Priority is any of high (with and without the search) | Compound narrowing gives `[]` with the search and #2, #4, #5, #15 without; chips list all three conditions. | PASS |
| 21 | Sort by Title | F…, I…, R…, W… order; `sort=title:asc` in URL. | PASS |
| 22 | Back ×2, Forward ×2, reload, deep link in a new page | Narrowing edits replace the history entry rather than pushing one (Back leaves the page); Forward, reload and the deep link all restore the identical list and chips. | PASS |
| 23 | Rail Queued/Running/Deferred/Closed/Blocked | Each is its own URL (`?state=…`) and keeps the sort and priority filter. | PASS |
| 24 | `/tasks/9999` | Back on the list with "no task 9999" (one expected 404). | PASS |
| 25 | API outage stub | The disconnected banner appears with the 11 rows still on screen and clears once the API answers again. | PASS |

### Task detail

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 26 | Edit → change title → Cancel | Nothing saved. | PASS |
| 27 | Edit title + description → Save; Base branch `main`; toggle auto-merge; Edit fields: Priority low, Requested by; reload | All persisted and shown after reload. | PASS |
| 28 | Repo picker | `acme/widgets`, `Other…`. | PASS |
| 29 | File a proposal, then on its pane: Defer until 2030-01-15 | `deferred`, `deferredUntil 2030-01-15T00:00:00Z`; note "Picks itself back up on 1/15/2030 … back into the proposals". | PASS |
| 30 | Undefer; Defer with the box left as it was; reload; Undefer; Approve | Back to `proposed`; then `queued`, `approved: true`. Undefer keeps the date in the box, so the second Defer re-sent it (visible, so expected). A dateless Defer on a fresh pane (task 2) stores no date. | PASS |
| 31 | Dependencies: add #3 to #15 by typing `toolchain` | Chip "3 bump the go toolchain to 1.24 (open)" whose name/tooltip is "3 Bump the Go toolchain to 1.24 — Running (blocking this task)"; #15 `blocked: true`; rail Blocked 1 → 2; Blocked view lists #11 and #15. | PASS |
| 32 | Remove it (chip ✕, and Delete on the focused chip) | `dependsOn` empty; rail Blocked back to 1; Blocked view lists only #11. | PASS |
| 33 | Work tree on #11 | "Provenance · Work from task 12: 0 of 4 done"; expands to the tree under task 12 with links to #8, #12, #1, #3, #11 and PR acme/widgets#104; following #8 opens it and Back returns. | PASS |
| 34 | Merge policy on #13 | Merge-queue comment and alert list each requirement with its source (`ci.mode: grain — from repository setting`, `ci.mode: both`, `auto_merge: never`, `stall_timeout: 30m`, `ci.grain: full suite`, `ci.github: build` and `cla/google` — from rule `deploy`) and "GitHub CI is narrowed to build, cla/google". | PASS |
| 35 | Grain CI on #13 (stub 1) | "grain CI is running against this task's branch…" with Show the run; it opens the run (pending, `grain/task-13`, "No jobs: this repository declares no workflows"); Escape returns to the pane. | PASS |
| 36 | Pending secret on #4 (stub 2) | Password box labelled `stripe-api-key`. Set secret → `PUT /api/tasks/4/secret`; task `queued`; timeline says "Set stripe-api-key. Its value is in grain's secret store…". The value is absent from the DOM, the task JSON, and (grep) every file under the demo directory, logs and WAL included. | PASS |
| 37 | Suspended session on #8 (stub 2) | Group "Suspended session": "Left by demo-run-8-1 on 10/23/2026, 1:00:00 AM (Antigravity)…", token in a code element. | PASS |
| 38 | Cross-team approvals | Not in this build: the single-tenant reset removed `crossTeamApprovals` from `pkg/ui` and the component from `ui/src`. Nothing to exercise. | N/A |
| 39 | Prompt on #3 | Full prompt, "8 kb · ~2,049 tokens · 1% of a 200,000-token window", beginning with the task title and repo instructions. | PASS |
| 40 | Live transcript on #3 | "Live Attempt #1 Transcript" inline; the attempt row opens "attempt #1 transcript"; Escape returns. | PASS |
| 41 | Internal PR on #15 (stub 3) | After the server's 5-minute GitHub branch cache refreshed, "Internal PR" → `/repos/acme/widgets/compare/main...grain/task-15` in a new tab, with heading `grain/task-15`, Open, "Into main on acme/widgets", and `walkthrough.go +1 -0`. | PASS |
| 42 | Comment on #2 | Stored; an empty Comment click sends nothing. | PASS |
| 43 | #12 (Director Session) | Opens `/session/director`. | PASS |

### Selection and bulk edit

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 44 | Queued view, tick #1, #2, #16 | "3 selected"; Approve/Defer/Undefer/Close/Bulk edit…/Capability/Clear selection. | PASS |
| 45 | Batch Close, dismiss confirm "Close 3 task(s)?" | No requests. | PASS |
| 46 | Bulk edit… → Cancel | No requests, selection kept. Mixed fields say "(mixed across the selection)". | PASS |
| 47 | Bulk edit, change fields, Escape | "You have unsaved changes here. Leave and discard them?"; dismissing keeps the dialog. | PASS |
| 48 | Apply Priority low, Requested by "Bulk walker", Auto-merge Off to 3 tasks | Three PATCHes carry the full field set (untouched fields kept: #1 still has area, due date, estimate, customer-facing). Selection cleared; persisted across reload. | PASS |

### Console and network

No uncaught page errors and no console errors on any page, except the one Bug 1 produces (`An invalid form control with name='sandboxCpus' is not focusable.`). The 4xx responses were all deliberate (`/api/tasks/9999` 404, outage-stub 503s). `net::ERR_ABORTED` lines on the 3-second polls of `/api/tasks` and `/api/tasks/{id}` are 304 Not Modified answers to `If-None-Match`, which Chromium reports as aborted; they are not failures.

## Bugs

### Bug 1: an invalid value in collapsed Advanced options silently blocks Create (fixed here)

- **Reproduction:**
  1. Open `/new`, give a title, and pick a repo.
  2. Open Advanced options, set vCPUs to `-1` (or memory `1.5`), and close Advanced options.
     A saved draft does the same on its own: it keeps the value and reopens the
     form with the section closed.
  3. Click Create task, or press Enter in Title.
- **Before:** nothing happens. No POST, no message, the section stays shut. The
  console logs `An invalid form control with name='sandboxCpus' is not focusable.`
  Every later attempt from the same draft fails the same way, which is how it first
  showed up: the "proposed, no repo" task in step 14 was never filed.
- **Cause:** the `min: 0, step: 1` constraints on the sandbox-shape fields are
  enforced by the browser, which cannot focus a field hidden in the collapsed
  `Accordion` to show its message.
- **Fix (`ui/src/components/NewTaskOverlay.jsx`):** the Advanced options accordion is
  now controlled. An `invalid` event from inside it, during a submit attempt (a
  click on Create, which Enter also produces), opens the section. When the
  transition has finished, the field is focused and `reportValidity()` shows the
  browser's message. A first version focused the field straight away, mid-animation,
  and that left it below the fold at both widths (field at y≈1274 in a 900px
  viewport). The submit-attempt flag is there because jsdom also fires `invalid`
  events while matching `:invalid` style rules.
- **Regression coverage:**
  - unit: `NewTaskOverlay.advanced.test.jsx` › "opens Advanced options when a field
    inside it refuses the submit". It seeds the draft, fails without the fix and
    passes with it.
  - e2e: `task_intake_inbox_lifecycle_walkthrough.spec.js` › "opens Advanced options
    on a refused value hidden inside it". It covers the click and Enter paths, the
    focus, the field in the viewport, no POST while refused, and one correct POST
    once fixed.
- **Rendered:** `/new` in this state, before and after, at 1400×900 and 390×844, in
  light and dark. After the fix the section is open and vCPUs is focused and in the
  viewport at every size (y≈790 on desktop, y≈727 on phone). The dialog has no
  horizontal overflow (scrollWidth equals clientWidth: 560/560 on desktop and
  390/390 on phone), and the paper is `rgb(20, 22, 26)` in dark mode. These were
  checked with bounding boxes rather than by eye: this sandbox has no image viewer
  and no Gemini key for `LOOK=1`, and `make ui-screenshots` does not exist on this
  branch.

## Observations (not changed)

- **O1:** with no repo picked (the `—` row, which is what the form opens on from the
  task list), Create is disabled and nothing says why. This is intentional per
  bwsalmon/agents#614 (an explicit choice rather than a silent default), but a hint
  would help.
- **O2:** `/t/<team>/new?field.…` links from before the single-tenant reset land on
  the task list and drop the form and its prefill. `docs/single-tenant-reset.md`
  (3e-ii) chose that deliberately ("land on the task list like any unknown path").
  Elsewhere `paths.js` keeps old links working (legacy sorts, the old repo-comparison
  query). Filed as a follow-up for a human decision rather than changed here.
- **O3:** Close in the task pane closes without a confirm, while the same action from
  an Inbox row confirms ("Close 14? It can be reopened later."). Both are reversible.
- **O4:** filtering, searching and sorting replace the history entry, so Back leaves
  the narrowed list instead of undoing the last change. Rail views do push entries.
- **O5:** the server caches the GitHub branch listing for 5 minutes, so a branch
  pushed after a pane was first opened gets its Internal PR link only after that.
  Filing re-checks after 10 s (`githubBranchRecheckAfter`); the pane does not.
- **O6:** Bulk edit offers "Agent framework" next to "Model", although New task
  treats the framework as implied by the model.
- **O7:** the empty-comment Comment button is enabled; pressing it does nothing.

# Second run: task 271, 2026-10-23 (afternoon)

- **Task:** grain/049f1e09/task-271, base `candidate`. This was the second attempt.
  The first attempt was paused by a provider usage limit before it pushed anything.
- **Commit tested:** the first attempt walked `5c39c2173`. This attempt re-checked
  everything on `66b743a5d` (`origin/candidate` at the start of this attempt), first
  unmodified and then with the fixes below.
- **Environment:** Debian 12, kernel `6.1.0-54-amd64`, Go `go1.26.9`, Node `v20.19.0`,
  Playwright `1.62.1` with headless Chromium `chromium-1234`
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`). The viewport was 1400×900 in UTC.
  The server was `go build ./cmd/grain` then `grain demo -open=false -github-sim
  -fake-ci -merge-rules -secrets-store -access-code-file <file>`, serving the
  production bundle (`cd ui && npm ci && npm run build`) over a throwaway SQLite store
  under `$TMPDIR`. There was no `/home/debian/.gemini-api-key`, so no model was ever
  called.
- **How it was driven:** throwaway Playwright scripts (not committed) signed in with the
  access code and then used visible controls only. The API was used only to read state
  back, except where the stubs below say otherwise. Every page recorded `pageerror`,
  console errors and HTTP responses of 400 or above. I also ran the committed
  `ui/e2e/task_intake_inbox_lifecycle_walkthrough.spec.js`, which now has 10 tests. It
  drives the inbox, intake, list, detail and bulk flows end to end, and all 10 tests
  pass.

## Stubs (this run)

1. **An agent's push.** I committed `walkthrough.go` on `grain/task-15` and pushed it into
   the `-github-sim` bare repo before the UI first listed branches. That gives the
   Internal PR link something to compare. Task 15 is not seeded on this commit, so I
   filed it first through `POST /api/tasks` (`repo: acme/widgets`, approved). It got
   id 15.
2. **Pending secret and suspended session.** No UI can create these; only an agent
   run sets them. I wrote them with `sqlite3` into the throwaway store:
   `task_observation.pending_secret='stripe-api-key'` on task 4, and the
   `suspended_session_*` columns on task 8.
3. **Grain CI run for task 13.** I started it with `POST /api/ci/runs {"taskId":"13"}`,
   which answered 202. That started the `-fake-ci` stub, which accepts runs and never
   executes them.

## Steps and outcomes (this run)

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 1 | Cold `/inbox` | Groups: *answer a question 2* (Acknowledge all (2)), *submit for merge 2* (Submit all (2)), *unblock a merge 1*, *approve a proposal 1*. | PASS |
| 2 | Inbox → approve the seeded proposal, reply to the seeded question (spec) | Both rows leave their groups. "Add a dark mode toggle" and "Investigate the merge queue stall" are `queued` in the list. | PASS |
| 3 | New task: Other… → type `unlisted/custom-repo` → Choose from list | **Bug A** on the unmodified commit: the select shows `—`, the off-allowlist warning stays, Create stays enabled, and submitting files the typed repo. Fixed. | FAIL → PASS |
| 4 | New task, queued and proposed paths, `/t/default/new?field.priority=high` prefill, Advanced options refusal, model picker (spec) | Each passes as the spec asserts. The prefill link opens `/new?field.priority=high` with Priority high. | PASS |
| 5 | Search, filter, sort, rail state views, reload, Back/Forward (spec, plus `keeps a rail state view…`) | URL-persisted, and restored on reload and history. | PASS |
| 6 | Task pane: Defer until 2030-01-15 → Undefer → Defer with no date | **Bug C** on the unmodified commit: after Undefer the box still holds `2030-01-15`, and the "dateless" Defer stores `deferredUntil 2030-01-15T00:00:00Z`. Fixed: the box is empty and nothing is stored. | FAIL → PASS |
| 7 | Dependency: type `toolchain` into "Add a dependency…" on #15, pick #3 | Chip accessible name/tooltip "3 Bump the Go toolchain to 1.24 — Running (blocking this task)"; #15 `blocked: true`, `dependsOn ["3"]`; rail Blocked 1 → 2. Delete on the focused chip: `dependsOn []`, rail Blocked back to 1. | PASS |
| 8 | Work tree on #11 | "provenance · work from task 12: 0 of 4 done"; links to #8, #12, #1, #3, #11. | PASS |
| 9 | Merge policy on #13 | Each line with its source: `ci.mode: grain -- from repository setting`, `ci.mode: both`, `auto_merge: never`, `stall_timeout: 30m`, `ci.grain: full suite`, `ci.github: build`, `ci.github: cla/google` -- `from rule deploy`. | PASS |
| 10 | Grain CI on #13 (stub 3): Show the run | Opens the run pane (pending, acme/widgets, `grain/task-13`); Escape returns. | PASS |
| 11 | Pending secret on #4 (stub 2) | Set the value, then reload. The timeline says "Set stripe-api-key. Its value is in grain's secret store…". The value is in neither the DOM nor the task JSON, and `grep -rF` finds it in no file under the demo directory. | PASS |
| 12 | Suspended session on #8 (stub 2) | "Suspended session" group shows the token. | PASS |
| 13 | Prompt and live transcript on #3 | "8 kb · ~2,049 tokens · 1% of a 200,000-token window"; one Live Attempt transcript block. | PASS |
| 14 | Internal PR on #15 (stub 1) | `/repos/acme/widgets/compare/grain/task-15` opens with `walkthrough.go` in the diff. | PASS |
| 15 | Cross-team approvals | Not in this build: the single-tenant reset removed them, as the morning run found. | N/A |
| 16 | Bulk edit: tick Repo → Other… → type → Choose from list; type again → untick, re-tick Repo; edit Priority on a mixed selection → untick | **Bug B** on the unmodified commit: Choose from list shows `—` (not the selection's `acme/widgets`), the re-tick reopens on the typed `custom/bulk-repo`, and Priority loses "(mixed across the selection)". Fixed: `acme/widgets`, `acme/widgets`, and the note is kept. | FAIL → PASS |
| 17 | Bulk edit Cancel and Apply (spec) | Cancel keeps the selection and sends nothing. Apply PATCHes both tasks, and the untouched fields are kept. | PASS |

**Console and network:** no page errors and no console errors. The only HTTP 4xx
responses came from my own probe asking for a task 15 that had not been filed yet.

## Bugs (this run), all fixed here

- **A: "Choose from list" kept the abandoned repo** (`RepoField.jsx`,
  `NewTaskOverlay.jsx`). The button swapped the input for the select without calling
  `onChange`, so the caller kept the typed repo. Submit then read
  `data.get("repo") || repo`, which turned the select's `""` back into the typed repo.
  Now the field reports the value the list shows, and submit uses `??`.
- **B: Bulk edit read live state where it meant the selection's**
  (`BulkEditOverlay.jsx`). The Repo picker's seed and every row's mixed note came from
  the edited state. They now come from a snapshot taken when the pane opens, and
  re-ticking Repo resets its value to that snapshot.
- **C: the wake date survived its deferral** (`detail/Actions.jsx`). The pane stays
  mounted through Defer and Undefer, so the next plain Defer sent the old date again.
  The first run recorded this as "visible, so expected", but Undefer's own tooltip
  says it "forgets any date it was going to wake on". The field now clears once a
  deferral succeeds.
- **Reproduction:** steps 3, 6 and 16 above. On the unmodified commit they gave
  `{warningStillVisible: true, createEnabled: true}`,
  `{bulkSelectValAfterChoose: "", retickValue: "custom/bulk-repo", priorityMixedAfterUncheck: false}`
  and `{untilValueAfterUndefer: "2030-01-15", secondDeferDeferredUntil: "2030-01-15T00:00:00Z"}`.
  With the fixes they give `{false, false}`, `{"acme/widgets", "acme/widgets", true}` and
  `{"", ""}`.
- **Regression coverage:**
  - Unit tests: `RepoField.test.jsx` (2 tests), `BulkEditOverlay.test.jsx` (2) and
    `detail/Actions.test.jsx` (1). All five fail on the unmodified sources and pass
    with the fixes.
  - E2E: `task_intake_inbox_lifecycle_walkthrough.spec.js` › "acts on neither an
    abandoned repo nor an abandoned wake date".
