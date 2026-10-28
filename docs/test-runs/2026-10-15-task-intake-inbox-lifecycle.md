# Task intake, Inbox and lifecycle walkthrough — 2026-10-15

## What was tested

- **Commit tested:** `9788ecb1e3aa4edf76f0596aa4600caf57b828ae`, the tip of
  `candidate` when this ran and the base of branch `grain/eu/task-3750`. The
  rail's version line read
  `v1.12.0-rc.1.0.20261014232547-9788ecb1e3aa · 9788ecb · 2026-10-14 23:25Z`.
  This base already has the 2026-10-13 run's fix (a No repo task files no
  base branch), and that fix still holds.
- **Fix commit:** `fix(ui): focus the Title box when New task opens`, on
  `grain/eu/task-3750` (Finding 1 below).
- **Environment:**
  - Debian 12 amd64, with Go 1.26.6 and Node v20.19.0.
  - Playwright (the version in `ui/package-lock.json`) driving Chromium
    headless shell 151 (`chromium-1234`).
  - Viewport 1400×900, browser timezone UTC.
  - Downloading Chromium needed
    `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`.
- **Server:** the real `grain demo` server.
  - Built with `cd ui && npm ci && npm run build`, then
    `go build -o /tmp/grain ./cmd/grain` once the frontend build had
    finished, since the binary embeds `pkg/ui/static`.
  - Started as `grain demo -addr 127.0.0.1:7788 -open=false`, with `HOME`
    pointed at an empty temp directory, so each start had its own
    throwaway embedded SQLite store.
  - It ran three times: the main walkthrough (store 1), the outage check
    (store 2), and a hand check of the fix on a rebuilt binary (store 3).
- **Browser:** ad-hoc Playwright scripts, kept out of the tree, using only
  visible controls: role/label locators, clicks, typing, and
  Tab/Enter/Escape.
  - Each script logged console errors and warnings, page errors, failed
    requests, HTTP ≥ 400 responses, and the body of every mutating `/api`
    request.
  - The poll aborts noted on 10-13 (`GET /api/tasks` → 304 →
    `net::ERR_ABORTED`) were filtered out as expected.
  - After each mutation the script re-read the task with
    `GET /api/tasks/{id}` and reloaded or deep-linked the screen.
- **Sign-in:** a fresh browser gets demo's Create account dialog. I created
  `operator` through it on each store.
- **Gemini:** this sandbox had no `/home/debian/.gemini-api-key`, and
  nothing in the walkthrough calls a model. For the queued task I picked
  `gemini-2.5-pro` in the New task Model search, and for an edited one
  `gemini-2.5-flash`. Both stored `agentFramework: antigravity`.

### Stubs

None. As on 10-13, some external boundaries are simply absent in
`grain demo`. I recorded those rather than stubbing them:

- Every Base branch box for `acme/widgets` requests
  `GET /api/repos/acme/widgets/github-branches`. It answers **404**
  because demo has no GitHub client, and logs one console "Failed to load
  resource" line. The box still works as free text.
- Pull-request links (`acme/widgets#101`, `#104`) point at github.com,
  which this sandbox can't reach. I checked the `href`
  (`https://github.com/acme/widgets/pull/104`) but didn't follow it.
- Demo seeds no grain CI runs (the rail reads "CI 0"), so the panes had no
  CI links to follow.
- To simulate an outage I stopped the demo process with `SIGSTOP`/`SIGCONT`
  and then `SIGKILL` (step 7). The process was signalled, not mocked.

## Steps and outcomes

### 1. Inbox

| Step | Outcome |
| --- | --- |
| Cold `/inbox` | **Pass.** It redirects to `/t/default/inbox` with count 4. The groups are Answer a question (task 4), Submit for merge (6), Read an answer (7) and Approve a proposal (1), each with its count and hint. The rail badge reads "Inbox 4". |
| Open from the rail (`/` → Tasks → Inbox) | **Pass.** |
| Row navigation | **Pass.** Clicking task 1's link opens `/t/default/tasks/1`, and Back returns to the Inbox. Focusing task 4's link and pressing Enter opens its pane, and Escape returns to the Inbox. |
| Approve the seeded proposal (task 1) | **Pass.** `POST /approve`. The Approve a proposal group disappears, the badge goes to 3, and task 1 is `queued`, `approved: true`. |
| Answer the seeded question (task 4) | **Pass.** Reply shows the question inline. Send stays disabled for an empty or whitespace-only reply. Escape inside the reply box leaves it open; the row offers its own Cancel instead. Sending posts the comment and task 4 goes to `queued`. The badge reads 2 after a reload. |
| Empty state | **Pass.** Submit (task 6) and Acknowledge (task 7) empty the Inbox. It reads "Nothing is waiting on you. Every task is either running, queued or done.", with count 0 and no number on the badge, both before and after a reload. Task 6 → `completed` (listed as Queued for merge), task 7 → `closed`. |
| Several proposals: group actions | **Pass.** With proposals 16 and 17 waiting, the group shows "Approve all (2)" and "Defer all (2)". Approve all asks "Approve all 2 proposals? …", and dismissing it leaves both `proposed`. Decline asks "Close 17 without running it? It can be reopened.", and dismissing it leaves 17 `proposed`. Accepting Defer all sends two `/defer` requests. Both tasks become `deferred`, the Inbox is empty, and the rail reads "Deferred 3". |
| Unknown team | **Pass.** `/t/nope/inbox` redirects to `/t/default/inbox`. |

### 2. New task (global form)

| Step | Outcome |
| --- | --- |
| Open | **Pass, after the fix.** "+ New task" gives `/t/default/new`, and a cold `/new` opens the form. **Finding 1:** before the fix, focus sat on the dialog's paper, not on Title. |
| Validation | **Pass.** Create task stays disabled for a whitespace-only title, and while Repos is "—". With "Other…" and `not a repo name`, the server answers 400 `repo must be owner/name, got "not a repo name" (one on another git host is host/owner/name)`. That shows in an alert, and the form stays open. With vCPUs at -2 the browser's own check blocks the submit ("Value must be greater than or equal to 0."), so no request is sent. |
| Queued path, every option (task 13) | **Pass.** I set title, a two-paragraph description, repo `acme/widgets`, base `release-1.0`, auto-merge off, "This fixes a red release-1.0", Priority high, Area `back`+Enter and `infra`+Enter, Requested by, Due date, Estimate 3, Customer-facing Yes, GCP key unticked, model `gemini-2.5-pro`, Network access None, nothing-to-do "Leave for acknowledgement", a prompt extension, and vCPUs/memory/disk 4/8192/40. The stored task matched every value: `capabilities: [gemini-key, self-debug]`, `area: [backend, infra]`, `fixesBaseBranch: true`, `networkAccess: none`, `nothingNeededAction: ack`. I also opened Specific connection and Review, which offer their defaults only in demo, and Escaped them. |
| Proposed path, No repo (task 14) | **Pass.** I picked "Other…", then "Choose from list" to get the dropdown back, then picked `acme/widgets` and typed Base `release-9`. I switched to No repo, unticked Queue immediately, and set Priority low and Area docs. The request carried `noRepo: true, base: ""`, and the task is `proposed` with `{area: docs, priority: low}`. "This fixes a red" is hidden for No repo. |
| Depends on (task 15) | **Pass.** Typing "Walk" lists 14 and 13, and ArrowDown+Enter picks 13 as a chip. Searching "#13" then shows "No matching tasks", because 13 is already picked. Escape closes only the results. Task 15 was filed `proposed`, `dependsOn: [13]`, `blocked: true`. |
| Cancel and draft | **Pass.** After the 400, Close dialog keeps the draft, and reopening restores the title and the "Other…" text. After a create the form is empty. Discard draft empties it too. |
| Keyboard-only filing (task 17) | **Pass, after the fix.** On the unfixed build, Title was reached by Tab → "Close dialog" → Tab, then Tab through to the Repos select in 12 steps. The tab order then runs Base branch, Another repo, Read-only repos, the checkboxes, Capabilities, Target team, Model, Depends on, Advanced options, Queue immediately, Add to backlog, Discard draft and Create task. Enter on Create task filed the proposal. |
| Prefill links | **Pass.** Task 3's "File a task with these fields" (`/new?field.area=backend&field.area=infra&field.priority=medium`) prefills both Area chips and the priority. A hand-made `/new?field.priority=bogus&…` leaves Priority blank, and the task is filed with `priority: ""` (see Observations). |

### 3. All tasks, state views, narrowing

| Step | Outcome |
| --- | --- |
| Default view | **Pass.** "All tasks 11", with the default filters "State is none of Deferred, Closed" and "Kind is none of Interactive". |
| Search | **Pass.** "walkthrough" narrows to 13, 14 and 15 and writes `q=walkthrough`. `q=13` matches task 13. `q=%` and `q=#13` read "No tasks match your search." (see Observations). |
| Sort | **Pass.** Backlog order → Sort by… lists 29 attributes. I picked Priority, set Direction to "Last to first", then Then by… → Title. The URL reads `sort=custom.priority:desc,title:asc`. The rows come out high 2, 4, 5, 13, then low 8, 11, 14, each group by title. |
| Compound filters | **Pass.** Add filter → Priority → values high and low gives `f=custom.priority:any:high\|low` on top of the defaults, and rows 2, 4, 5, 8, 11, 13, 14. Adding a search kept the result and wrote all three parameters. |
| Reload and deep link | **Pass.** A reload keeps the box, the chips ("Priority Last to first, then Title A–Z", "Priority is any of high, low") and the rows. The same URL in a new tab gives the same. |
| Back and Forward | **Pass.** Opening row 2 and pressing Back restores the exact narrowed URL and rows. Editing a filter, sort or search replaces the history entry rather than adding one (see Observations). Across rail views, Back goes Blocked → Closed → Deferred, Forward returns to Closed, and a reload keeps Closed. |
| State views from the rail | **Pass.** Proposed (14, 15), Queued (1, 2, 4, 11, 13), Running (3, 8), Queued for merge (5, 6), Deferred (9), Closed (7, 10) and Blocked (11, 15) each list exactly their tasks. Blocked + "walkthrough" narrows to 15. Clearing the box restores 11 and 15. A search with no match reads "No tasks match your search." |
| Keyboard reach | **Pass.** Tab from the search box goes Filters, sort, each filter chip, Select all, then each row's Move handle, Select checkbox and title link. |
| Malformed URLs | **Pass.** These are tolerated rather than crashing: `/tasks/999` and `/tasks/abc` show the list with an alert "no task 999" / "no task abc". `?f=garbage` is dropped. `?f=custom.nosuch:any:x` shows a chip "Unknown condition: custom.nosuch". `?sort=nonsense:sideways` becomes `nonsense:asc` with the default order. `?sort=custom.priority` becomes `:asc`. `?state=bogus` shows an empty view headed "bogus" (see Observations). |

### 4. Task detail

| Step | Outcome |
| --- | --- |
| Entry points | **Pass.** Inbox row, an Inbox row by keyboard, All tasks row, Proposed and Deferred view rows, and cold `/tasks/{id}` deep links (3, 5, 8, 12, 13, 14, 15, 16, 17). |
| Title and description | **Pass.** Cancel sends nothing, and reopening shows the stored title. Save is disabled for a blank title. Save sends one PATCH with the title, the description and model `gemini-2.5-flash`. The timeline records "This task was just edited…" with the new description rendered as markdown. |
| Repo, base, fixes-red | **Pass.** Picking `acme/widgets` on no-repo task 14 sends `PATCH {repo}`, with no base carried in. Base `release-2.0` + Enter sends `PATCH {base}`. The pane then offers "This fixes a red release-2.0". |
| Auto-merge | **Pass.** One `PATCH {autoMerge: false}`, which persists. |
| Custom fields | **Pass.** Edit fields → Cancel sends nothing. Saving Priority high, Area `fro`+Enter, Requested by and Estimate sends one PATCH and stores `area: [docs, frontend]`. |
| Close and Reopen | **Pass, by design.** Close on a proposed task closes it at once, with no confirm (only Cancel of a running task confirms; `detail/Actions.jsx`). Reopen returns it to `proposed`. |
| Defer with no date, Undefer | **Pass.** `POST /defer {deferredUntil: ""}` → `deferred`. It survives a reload. Undefer → `proposed`. |
| Defer with a date | **Pass.** 2026-01-01, and today (2026-10-15, whose UTC start has passed), each give a 400 alert "wake time … is not in the future…", and the task stays `proposed`. 2026-12-31 gives `deferredUntil: 2026-12-31T00:00:00Z`, and the pane reads "Picks itself back up on 12/31/2026, 12:00:00 AM, back into the proposals for someone to approve." before and after a reload. |
| Pick back up and approve | **Pass.** From the Deferred view → Undefer → Approve, task 14 is `queued`, `approved`, with no `deferredUntil`. Approve is gone after a reload. |
| Run now on a blocked proposal (15) | **Pass, by design.** `POST /run-now` approves it. It is `queued`, still blocked, `runNowAt` set, the button disabled and the hint "Asked for a run…" shown. |
| Attempts and transcripts | **Pass, as far as the seed allows.** Task 3 shows "Live Attempt #1 Transcript", and its Running chip opens an "Attempt #1 transcript" dialog. Task 8 shows attempt #2. Cancel on running task 8 asks "Cancel this job? Its run will be abandoned: no pull request will be opened for it.", and dismissing it leaves the task `running`. |
| Prompt | **Pass.** Task 13's Prompt reads "No prompt recorded yet -- nothing has been dispatched for this task…". |
| Chat | **Pass.** On director session 12, a Message + Send posts a comment, which shows in Chat and in the Timeline (seeded messages appear in both too) and survives a reload. |
| CI and PR links | **Pass.** Task 8 links `acme/widgets#104` to github.com. Demo has no CI runs. |

### 5. Bulk selection and edit

| Step | Outcome |
| --- | --- |
| Select rows | **Pass.** Ticking 13 and 14 shows "2 selected" with Approve, Submit, Defer, Undefer, Close, Reopen, Bulk edit…, the capability picker and Clear selection. |
| Bulk edit: Cancel and Escape | **Pass.** Ticking Requested by and typing into it, then Cancel, sends nothing and leaves both rows ticked. Reopening shows the field unticked again. Escape does the same. Mixed fields are labelled "(mixed across the selection)". |
| Bulk edit: apply | **Pass.** Priority low, Base `release-3.0`, Due date 2026-12-15 and Area `inf`+Enter send one PATCH per task. Enter in Area does not submit the dialog. Each task keeps its own repo, model, prompt extension, capabilities, requested-by, estimate and customer-facing. Area is replaced, not added to (see Observations). Task 13 keeps its fixes-red mark, which now follows `release-3.0`; that re-tagging on a base edit is deliberate (`Client.UpdateTask`). The selection clears, and nothing comes back after a reload. |
| Off-screen selection | **Pass.** With 13 and 15 ticked and the list narrowed to "dependent", Close confirms "Close 2 task(s)? 1 of them is not shown in this view.". Dismissing it leaves both `queued`. Clear selection empties the bar. |
| Bulk Undefer and capabilities | **Pass.** From the Deferred view, Undefer on 16 and 17 → both `proposed`. On the Proposed view, Capability "Memory" → Attach sends one request per task, and both carry `memory`. |
| Bulk Submit on PR-less proposals | **Pass, by design.** Both requests answer 200 and only set auto-merge (already on). Nothing visible changes (`Client.Submit`). |

### 6. Loading, error and outage states

| Step | Outcome |
| --- | --- |
| Server paused (`SIGSTOP`, 12 s) | **Observation.** Requests simply hang, and nothing on screen says so. An Approve clicked during the pause landed when the process resumed (task 16 → `queued`). |
| Server killed with a task pane open | **Finding 2 (follow-up filed).** The "This deployment cannot be reached… retrying automatically" banner appears within seconds. Every poll fails with `ERR_CONNECTION_REFUSED`, and an Approve fails the same way. But while the task pane is open the banner is missing from the accessibility tree: `getByRole("status")` finds nothing, and it is absent from the aria snapshot. Closing the pane brings it back. |

## Findings

### Finding 1 — New task opened with focus on nothing typeable (fixed)

**Reproduction:** deterministic, in Chromium against `grain demo`:

1. Click "+ New task", or load `/new` cold.
2. Type a title without clicking anywhere.
3. Nothing lands in Title. `document.activeElement` is the dialog's paper
   `DIV`. Title is the second Tab stop, after "Close dialog".

The form's whole job starts with a title. Its siblings New session
(`NewSessionDialog.jsx`) and New team (`NewTeamDialog.jsx`) both focus
their first field on open.

**Fix:** `autoFocus` on the Title `TextField` in `NewTaskOverlay.jsx`. MUI's
focus trap leaves a focus already inside the dialog alone, so Title keeps
it.

**Regression coverage:**

- `ui/src/components/NewTaskOverlay.test.jsx`, new test "opens with the
  Title box focused, so typing starts the title". Without the fix it fails
  with "Expected element with focus". With the fix it passes, and the four
  NewTaskOverlay files pass (79 tests).
- `ui/e2e/task_intake_inbox_lifecycle_walkthrough.spec.js` now asserts
  that Title is focused after "+ New task", and that typed keys land in
  it. Against a build without the fix it fails at `toBeFocused()`
  (`Received: inactive`). With the fix, that file, `inbox.spec.js`,
  `task_inputs_verification_walkthrough.spec.js` and
  `new-task-off-list-repo.spec.js` pass (13 tests, Chromium).
- I re-checked by hand on a fresh store with the rebuilt binary. Typing
  straight after "+ New task" fills Title. Escape still closes the form and
  keeps the draft. Reopening restores the draft with Title focused, and a
  cold `/new` focuses Title too.

### Finding 2 — the outage banner is hidden from assistive tech under a task pane (follow-up)

**Reproduction:** deterministic:

1. Open `/tasks/3`.
2. Kill the server.
3. The "cannot be reached" banner is visible, but `getByRole("status")`
   finds nothing.
4. Press Escape to close the pane, and the banner is announced.

MUI's modal sets `aria-hidden` on its siblings, which include the banner,
so a screen-reader user working in a task pane never hears that the
deployment went away. Any other modal pane (New task, Bulk edit) presumably hides it the same
way; I checked only the task pane.

**Why it is not fixed here:** the fix is a structural choice: portal the
banner beside the modal, mirror it into the pane, or announce it through
a live region that a modal does not hide. It touches every pane rather
than this area alone, and it needs its own e2e beside
`disconnection.spec.js`. I filed it as a follow-up against `candidate`.

### Observations (no change made)

- **No feedback while a request hangs.** With the server paused, nothing
  marks the hanging request (step 6). The banner only arrives once
  connections are refused.
- **Filter, sort and search edits replace the history entry.** Back from a
  narrowed list leaves the list rather than undoing the last edit. Rail
  views and opening a row do push entries. This looks deliberate (one
  entry per keystroke would be worse); I'm recording it only because the
  task asked about Back/Forward.
- **The timeline forgets superseded transitions.** After Close → Reopen
  the "Closed" line is gone. After Defer → Undefer → Defer only the latest
  "Deferred" remains. `model.Transitions` documents that it rebuilds the
  timeline from current timestamps, so the history is incomplete by
  design.
- **Bulk edit replaces multi-value fields.** Ticking Area in Bulk edit and
  adding `infra` sets Area to `[infra]` on every task, dropping each
  task's own values. The dialog says "set on every selected task", so
  this is the documented meaning. A person expecting "add" would lose
  values.
- **Prefill with an unknown option shows a blank select.**
  `/new?field.priority=bogus` shows Priority with no value rather than
  "none" or the default, and the task is filed with `priority: ""`. Only a
  hand-made link reaches this.
- **List search doesn't read `#13` as an id.** `q=13` finds task 13, but
  `q=#13` finds nothing. The Depends on picker does read `#13` as an id.
- **`?state=bogus` heads the view "bogus".** It shows an empty list
  instead of falling back to All tasks. Nothing in the UI links there.
- **Closing New task opened from a pane's prefill link returns to the list
  behind, not the pane.** Browser Back does return to the pane.
- **Still open from earlier runs:** No repo still shows Base branch and
  auto-merge, and Base branch stays in the tab order; Base branch boxes
  still log a 404 for `github-branches` in demo.

---

# Second run — task 4024, 2026-10-15 (afternoon)

The same walkthrough again, the same day, on a newer `candidate`. This run
leaned on what the morning run did not reach: the task pane's sections
(dependencies, work tree, merge rules, the grain CI verdict, a pending
secret, a suspended session, cross-team approvals, the prompt, the live
transcript and the Internal PR link), and the model picker against the
team's connections. It also repeated the Inbox, New task, list and bulk
passes more briefly.

## What was tested

- **Commit tested:** `2a58638d0a06`, the tip of `candidate` and the base of
  `grain/eu/task-4024`. The rail read
  `v1.12.0-rc.1.0.20261015142353-2a58638d0a06 · 2a58638 · 2026-10-15 14:23Z`.
- **Fix commit:** `fix(ui): name cross-team approval buttons after the
  dependency they decide`, on `grain/eu/task-4024` (Finding 3 below).
- **Environment:** Debian 12.15 amd64, Go 1.26.6, Node v20.19.0,
  Playwright from `ui/package-lock.json` driving Chromium headless shell
  151 (`chromium-1234`, downloaded with
  `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`).
  Viewport 1400×900, browser timezone UTC. `sqlite3` was installed with
  apt for the one stub below.
- **Server:** `cd ui && npm ci && npm run build`, then
  `go build -o /tmp/grain ./cmd/grain`, started as
  `HOME=/tmp/h1 grain demo -addr 127.0.0.1:7788 -open=false -fake-ci
  -github-sim -merge-rules -secrets-store` over its own throwaway store
  (`/tmp/grain-demo-*/grain.db`). The extra flags bring in the merge-rules
  tasks (13, 14), a grain CI stub, a simulated `acme/widgets` with
  `main` and `release/1.0`, and a real secrets store for the pending-secret
  box.
- **Browser:** ad-hoc Playwright scripts kept out of the tree, using
  role/label locators, clicks and the keyboard. Each logged console
  errors and warnings, page errors, failed requests, every HTTP ≥ 400 and
  the body of every mutating `/api` request. After each mutation the
  script re-read `GET /api/tasks/{id}` and reloaded or deep-linked.
  A fresh browser got demo's Create account dialog; I created `operator`
  through it. `operator` is a system admin and a member of team `default`
  (shown as "admin"), and works with Admin mode off unless a step says
  otherwise.
- **Gemini:** there was no `/home/debian/.gemini-api-key` in this sandbox,
  and nothing here calls a model. The only connection is demo's
  `gemini-pro` (Antigravity), advertising `gemini-2.5-pro` and
  `gemini-2.5-flash`; the queued task was filed on `gemini-2.5-flash`.

### Stubs

Each stands in for an agent run, which `grain demo` does not have:

1. **A run's `request_secret` and a parked agent session** were written
   straight into the store, since only `orchestrator/finish.go` sets them
   at the end of a real run:
   `UPDATE task_observation SET pending_secret='stripe-api-key' WHERE task_id='4'`
   and `suspended_session_token='sess-7f3a9c', suspended_session_framework='antigravity',
   suspended_session_run_id='run-8-2', suspended_session_at='2026-10-15 14:30:00'`
   on task 8. The server, the pane and the secret write were all real.
2. **A run's push of its branch:** I cloned demo's simulated repo
   (`/tmp/grain-demo-*/githubsim/acme/widgets.git`), committed one change on
   `grain/task-15` off `release/1.0` and pushed it back, as an agent's push
   would land.
3. **A run's CI trigger:** `POST /api/ci/runs {"taskId": "13"}` started
   the grain CI run an agent's push would have started, on demo's
   `-fake-ci` stub (which accepts runs and never finishes them).
4. **Cross-team setup:** the `producers` team, its task and the
   dependency were all created through the UI. Only the policy switch went
   through the Team settings → Audit / Security page, also in the UI.

## Steps and outcomes

### 1. Inbox

| Step | Outcome |
| --- | --- |
| Cold `/inbox` and from the rail | **Pass.** `/inbox` → `/t/default/inbox`, "Inbox 6". The groups: Answer a question (4), Submit for merge (6, 13 — "Submit all (2)"), Read an answer (7), Unblock a merge (14, with an "Open pull request" link) and Approve a proposal (1). From `/t/default/tasks`, the rail's Inbox button lands on the same page. |
| Row navigation | **Pass.** Clicking task 4's link opens `/t/default/tasks/4`; Back returns to the Inbox. Focusing task 14's link and pressing Enter opens its pane; Escape returns. |
| Approve the proposal (1) | **Pass.** One `POST /approve`. The Approve a proposal group goes away, the badge drops to 5, and task 1 is `queued`, `approved: true`. |
| Answer the question (4) | **Pass.** Reply shows the question inline; Send is disabled for a blank reply. Sending posts the comment, the group goes away, task 4 is `queued`, and the badge reads 4 after a reload. |
| Empty state | **Pass.** Submit all (2) asks "Submit all 2 finished changes for merge? …"; accepting submits 6 and 13. Acknowledge closes 7 without a prompt. 14's pane → Retry merge clears the last group. "Nothing is waiting on you…", count 0, before and after a reload. |
| Keyboard | **Pass.** Tab moves through each row's title link and its action, then out to the header. |

### 2. New task

| Step | Outcome |
| --- | --- |
| Open from "+ New task", from Create options → New task, cold `/t/default/new` | **Pass.** Title has focus each time (the morning run's fix holds). |
| Queued path, every option (task 15) | **Pass.** Title, a two-paragraph description, an attachment (`notes.txt`), `acme/widgets`, Base `release/1.0`, auto-merge off, Priority high, Area `back`+Enter, Self debug unticked, Model `gemini-2.5-flash`, Network None, nothing-to-do Leave for acknowledgement, vCPUs 2, memory 4096. The stored task matched; the attachment is listed with size 32. `grain-ci` was added to its capabilities by the repo's merge rules (`ci.mode: grain`), not by the form. |
| Proposed path, No repo, Depends on (task 16) | **Pass.** No repo, Depends on #15 picked by keyboard, Queue immediately off: `proposed`, `noRepo`, `dependsOn: [15]`, `blocked`. |
| Prefill link | **Pass.** `/t/default/new?field.priority=low&field.area=docs&field.area=infra&field.requested-by=Ops%20desk` prefills Priority low, both Area chips and Requested by. Close dialog sends nothing and returns to the list. |
| Target team (task 17) | **Pass.** Target team lists `admin (default)`, `operator`, `producers`. On `producers`, which has no connection, the Model picker is not shown at all, the repo list gains `grain/config-producers`, and GCP key drops out of the defaults. The task lands in `producers`. |
| Model picker vs connections | **Pass, with a note.** With `gemini-pro` enabled the picker offers exactly Team default, `gemini-2.5-pro`, `gemini-2.5-flash` — what the one connection advertises. Disabled (Team settings → Models & connections → Disable, Admin mode on) it still offers both, labelled "(disabled connections only)"; re-enabled, the labels go. Offering disabled-only models is deliberate (`state.js` `teamModels`: pinning a disabled connection is how such a task runs), so the picker is "what the team's connections advertise, marked when only disabled ones do", not strictly "enabled only". |
| Escape inside the form | **Pass.** Escape with a select or autocomplete open closes only that popup (Priority, Target team, Customer-facing, Area, Model, Capabilities). Escape in Base branch with no suggestions open closes the form and keeps the draft. See Observations for unticking a not-ready capability. |

### 3. Lists, views and bulk

| Step | Outcome |
| --- | --- |
| Search and sort | **Pass.** "walkthrough" → 15, 16 and `q=walkthrough` plus the default filters in the URL. Sort by… lists 29 attributes; Title gives `sort=title:asc` and the order 16, 15. |
| Compound narrowing, reload, Back/Forward | **Pass.** Rail Queued (`state=queued`, rows 1, 2, 4, 11, 15, 16) + Add filter → Priority → high (2, 4, 15) + search "walk" (15). Reload keeps URL, rows and box. Opening 15 and Back restores the exact URL. Rail Deferred keeps the search and the filter (an empty list); Back returns to the narrowed Queued view and Forward to Deferred. |
| Blocked view and rail count | **Pass.** See §4. |
| Bulk edit | **Pass.** With 15 and 16 ticked: Bulk edit lists Auto-merge, Model, Agent framework, Priority, Area and Requested by as mixed. Cancel keeps both ticked and sends nothing. Requested by "Platform team" + Due date 2026-12-15 → one PATCH per task; each keeps its own priority, area, base and model. The selection clears. |
| Bulk Close, dismissed | **Pass.** "Close 2 task(s)?" — dismissing leaves both `queued`. Clear selection empties the bar; nothing is ticked after a reload. |

### 4. Task detail sections

| Step | Outcome |
| --- | --- |
| Entry points | **Pass.** Inbox rows (click and keyboard), list rows, Deferred view rows, the work tree's links, cold `/t/default/tasks/{id}` and bare `/tasks/{id}`. A cold `/t/default/tasks/17` (a `producers` task) and `/tasks/17` both land on `/t/producers/tasks/17`. |
| Dependencies | **Pass.** On 15, "Add a dependency…" with `#2` + ArrowDown + Enter → `POST /depends-on {id: 2, attach: true}`. The chip reads "2 Fix flaky retry test (open) ×", is named "2 Fix flaky retry test — Queued (blocking this task)", and shows that as its tooltip on hover. The rail goes Blocked 2 → 3, the Blocked view lists 11, 15, 16, and the chip survives a reload. Backspace on the focused chip removes it (`attach: false`), 15 is unblocked and the rail is back to 2. |
| Work tree | **Pass.** Task 3's pane shows "Work from task 12: 0 of 4 done", 12 → 1, 3, 8 → 11, nested by who filed what, with each state, 8's PR link and working task links (Back returns to 3). |
| Merge rules | **Pass.** Tasks 13 and 14 list each requirement with its source (`ci.mode: grain — from repository setting`, `ci.mode: both`, `auto_merge: never`, `stall_timeout: 30m`, `ci.grain: full suite`, `ci.github: build`/`cla/google` — from rule `deploy`), plus "GitHub CI is narrowed to build, cla/google". The same list is in the merge queue's timeline comment. |
| grain CI verdict | **Pass.** After stub 3, 13's pane shows "grain CI is running against this task's branch. Its pull request is held pending…" with Show the run. That opens "CI run fake-ci-1" (Pending, `grain/task-13`, "asked by task 13", Cancel run) over the task; its ← Back returns to the task. The overlay is not in the URL — deliberate (`App.jsx` `openCIRun`). |
| Pending secret | **Pass.** After stub 1, 4's pane says "This task's run asked for a secret. Nothing is stored as stripe-api-key/value yet…", with a password box "Value for stripe-api-key" and Set secret disabled until it has a value. Set sends `PUT /api/tasks/4/secret`; the box is replaced by the timeline line "Set stripe-api-key. Its value is in grain's secret store, not in this conversation". `pendingSecret` is gone, and the value is in neither the task JSON, the page after a reload, nor anywhere in `grain.db`/`-wal`. |
| Suspended session | **Pass.** After stub 1, 8's pane has a "Suspended session" group: "Left by run-8-2 on 10/15/2026, 2:30:00 PM (Antigravity). Any further run of this task resumes this conversation…", token `sess-7f3a9c`, read-only. |
| Cross-team approvals | **Pass after the fix (Finding 3).** With `producers`' "Require authorization for cross-team dependencies" switched on, 15 (depending on `producers`' 17) reads "Held: … team producers requires an admin's approval…" with "Approve for team producers". Approving shows "Team producers approved this task's dependency on 17 (operator)" with a withdraw control; 17's pane lists "Task 15 of team default depends on this task, and team producers approved the link". Withdrawing from 15 brings the hold back, and it survives a reload. |
| Edit title/description/repo/base/fields/auto-merge | **Pass.** Edit → Save sends one PATCH with title and description. Repo `acme/widgets` and Base `release/1.0`+Enter persist. Edit fields → Priority low + Requested by sends one PATCH. Auto-merge toggles with one PATCH (the box follows once the server answers, not at the click). |
| Defer, pick up, approve | **Pass.** Until 2026-01-01 → 400 "wake time … is not in the future" in an alert, still `proposed`. 2026-12-31 → `deferred` until `2026-12-31T00:00:00Z`; after a reload: "Picks itself back up on 12/31/2026, 12:00:00 AM, back into the proposals…". Undefer → `proposed`; Defer with no date → `deferred`, no wake time. From the Deferred view: Undefer → Approve → `queued`, `approved`, still blocked on 15. |
| Prompt | **Pass.** Task 3's Prompt shows "prompt for 3 · attempt #1 · 7 kb · ~1,910 tokens · 1% of a 200,000-token window" and the full prompt. Escape closes only the prompt. |
| Live transcript | **Pass.** Task 3's "Live Attempt #1 Transcript" Running chip opens "Attempt #1 transcript" with the run's narration and tool calls. Escape returns to the pane. |
| Internal PR | **Partial (demo limit).** After stub 2, 15's pane shows "Internal PR" → `/repos/acme/widgets/compare/release/1.0...grain/task-15`, which opens the pull request page headed `grain/task-15`, "Into release/1.0 on acme/widgets". It took about five minutes to appear, the server's `githubBranchCacheTTL`. The diff is not drawn: `GET /api/repos/acme/widgets/compare` answers 404 "repository comparison is not available on this UI server", because `grain demo` wires no `RepoComparer` even with `-github-sim`. Follow-up proposed. |

### 5. Console and network

Apart from the expected refusals below, the scripts logged no console
errors, page errors or failed requests:

- the 400 from the past-date Defer;
- a 403 from Disable on a connection with Admin mode off ("you are a
  system admin; turn on Admin mode to do this"), shown in an alert;
- the 404 from `compare` above.

## Findings

### Finding 3 — two "Approve" buttons on a producer's proposal (fixed)

**Reproduction:** deterministic, against `grain demo`:

1. Create team `producers`. In its Team settings → Audit / Security, turn
   on "Require authorization for cross-team dependencies".
2. File a proposal in `producers` (17), and a `default` task (15) that
   depends on it.
3. Open 17. The pane shows the proposal's own Approve and, in the
   dependents box, a second button also named "Approve", which approves
   15's dependency, not the proposal.
   `getByRole("button", { name: "Approve", exact: true })` finds two.
   Approve the proposal (or "Withdraw approval" on it once queued) and the
   same doubling happens with "Withdraw approval".

A screen-reader or keyboard user who lists the buttons hears two
identical controls that do different things, and on a task with several
dependents every dependent's control has the same name too.

**Fix:** `CrossTeamDependents.jsx` gives each button an accessible name
naming the dependent ("Approve task 15's dependency on this task",
"Withdraw approval of task 15's dependency on this task"), keeping the
short visible text, so the name still contains what is shown. The
consumer side (`CrossTeamApprovals.jsx`) names the dependency its
withdraw control acts on in the same way.

**Regression coverage:**

- `ui/src/components/detail/CrossTeamDependents.test.jsx` (new): no
  button is named plain "Approve" or "Withdraw approval", and each named
  control still posts for its own dependent. Both tests fail without
  the fix.
- `ui/e2e/cross-team-approval-withdraw.spec.js`: new case "the producer's
  pane tells its own Approve from a dependent's". It expects exactly one
  "Approve" on the producer's proposal, approves the link through the
  named control, and checks that the proposal stays `proposed`. On a build
  without the fix it fails with `Expected: 1, Received: 2`. With the fix
  both tests in the file pass (Chromium). The existing
  `teams-server-fixes-walkthrough.spec.js` lookups ("Withdraw approval",
  "Approve", "Withdraw team …'s approval") are substring matches and
  still resolve.

### Observations (no change made)

- **Unticking a capability that is not ready drops its row from the open
  menu.** In New task, the Capabilities menu lists not-ready capabilities
  (GCP key, Gemini key in demo) only while they are ticked
  (`configuredCapabilities`). Unticking one by mouse makes its row vanish
  under the pointer. Focus is then on nothing, and Escape stops closing
  the menu until something else is clicked. By keyboard, focus moves to
  the menu list and Escape still works. Either way the untick can't be
  undone in that form, since the row is gone. Hiding not-ready rows is a
  deliberate choice, so I've left it, but keeping a row that was ticked
  when the menu opened would remove the trap.
- **The Internal PR link lags a push by up to five minutes**, the server's
  branch-list cache (`githubBranchCacheTTL`), plus the client's own minute
  (`branchRefs.js`).
- **A narrowing follows you across rail views.** Rail Deferred with
  "walk" and Priority high from the Queued view showed an empty Deferred
  list, while the rail said "Deferred 1".
- **A URL with only `?q=` carries no default filters.** A hand-made
  `/t/default/tasks?q=walkthrough` includes deferred and closed tasks.
  Typing the same search in the box writes the defaults into the URL.
- **Team settings offers Disable, Edit and Remove on a connection to a
  system admin with Admin mode off,** and the press then fails with a
  403 alert telling them to turn Admin mode on. That is consistent with
  how Admin mode works elsewhere.
- **The merge-rules seed's timeline** lists "Queued" and "Proposed" at
  14:43 above "Awaiting submit" at 13:53. That is the morning run's
  "timeline is rebuilt from timestamps" note again, with demo's seed
  times.
