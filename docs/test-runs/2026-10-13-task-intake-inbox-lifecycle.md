# Task intake, Inbox and lifecycle walkthrough — 2026-10-13

## What was tested

- **Commit tested:** `25cd6f2640ea3d8bf296dd94d0a4cce039ef25d8`, the tip of
  `candidate` when this ran and the base of branch `grain/eu/task-3493`. The
  rail's version line read
  `v1.10.0-rc.1.0.20261013090548-25cd6f2640ea · 25cd6f2`. Since the
  2026-10-12 run this base gained "This fixes a red <base>" (6d3587d75,
  4b9fe38eb) in the New task form and the task pane. This run covered that
  checkbox along with everything else.
- **Fix commit:** `fix(ui): file no base branch for a task switched to No
  repo` on `grain/eu/task-3493` (Finding 1 below).
- **Environment:**
  - Debian 12.15 amd64 on Linux 6.1.0-53, with Go 1.26.6 and Node v20.19.0.
  - Playwright 1.62.1 driving Chromium headless shell 151 (`chromium-1234`).
  - Viewport 1400×900, browser timezone UTC.
  - Downloading Chromium needed
    `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`.
- **Server:** the real `grain demo` server.
  - Built with `cd ui && npm ci && npm run build`, then
    `go build -o /tmp/grain ./cmd/grain` once the frontend build had
    finished, since the binary embeds `pkg/ui/static`.
  - Started as `grain demo -addr 127.0.0.1:7788 -open=false`, with `HOME`
    pointed at an empty temp directory so it had its own throwaway
    embedded SQLite store.
  - After the fix, a second fresh store on a rebuilt binary.
- **Browser:** ad-hoc Playwright scripts, kept out of the tree, using only
  visible controls: role/label locators, clicks, typing, and
  Tab/Enter/Escape.
  - Each script logged console errors and warnings, page errors, failed
    requests, HTTP ≥ 400 responses, and the body of every mutating `/api`
    request.
  - After each mutation the script re-read the task with
    `GET /api/tasks/{id}` and reloaded or deep-linked the screen.
- **Sign-in:** a fresh browser gets demo's Create account dialog. I created
  `operator` through it and reused the session for the rest of the run.
- **Gemini:** this sandbox had no `/home/debian/.gemini-api-key`, and
  nothing in the walkthrough calls a model. For the queued task I picked
  `gemini-2.5-pro` from the New task Model search. Its only connection was
  "gemini 2.5 pro · antigravity".

### Stubs

None. Some external boundaries are simply absent in `grain demo`. I
recorded those rather than stubbing them:

- Every Base branch box for `acme/widgets` requests
  `GET /api/repos/acme/widgets/github-branches`. It answers **404** because
  demo has no GitHub client. The only symptom is one console "Failed to
  load resource" line, and the box still works as free text.
- Pull-request links (`acme/widgets#101`, `#104`) point at github.com, which
  this sandbox can't reach. I checked their `href`s but didn't follow them.
- Demo seeds no grain CI runs, so the panes had no CI links to follow.

## Steps and outcomes

### 1. Inbox

| Step | Outcome |
| --- | --- |
| Cold load of `/inbox` | **Pass.** It redirects to `/t/default/inbox` with count 4. The groups are Answer a question (task 4), Submit for merge (6), Read an answer (7) and Approve a proposal (1), each with its count and hint. The rail badge reads 4. |
| Open from the rail (`/` → Inbox) | **Pass.** |
| Row navigation | **Pass.** Clicking task 1's row opens `/t/default/tasks/1`, and Back returns to the Inbox. |
| Decline, then dismiss the confirm | **Pass.** The confirm reads "Close 1 without running it? It can be reopened.". Dismissing it leaves task 1 `proposed`. |
| Defer from the Inbox | **Pass.** It sends `POST /tasks/1/defer` straight away, with no confirm. A defer is reversible, so I don't count that as a bug. Task 1 is `deferred` and leaves the Inbox. I picked it back up from the rail's Deferred view → task pane → Undefer, and it returned to `proposed` and to the Inbox. |
| Approve the seeded proposal (task 1) | **Pass.** `POST /approve`. The group disappears, and task 1 is `queued`, `approved: true`, and listed under Queued. |
| Answer the seeded question (task 4) | **Pass.** Reply shows the question inline ("Two PRs are both stuck failing the same lint check…"). Send is disabled for an empty or whitespace-only reply. Sending posts the comment, and task 4 goes to `queued`. The badge goes to 2, both survive a reload, and the reply is on task 4's timeline. |
| Keyboard triage, then empty state | **Pass.** I focused Submit (task 6) and pressed Enter, then clicked Acknowledge (task 7). Task 6 → `completed`, task 7 → `closed`. The page reads "Nothing is waiting on you. Every task is either running, queued or done." and the badge has no number, both before and after a reload. |
| Keyboard reach | **Pass.** Tab goes through the rail, then each row's title link and its action buttons in order (Reply; Submit; Acknowledge; Approve, Defer, Decline). |

### 2. New task (global form)

| Step | Outcome |
| --- | --- |
| Open from "+ New task", and cold `/new` | **Pass.** The URL becomes `/t/default/new`. A cold `/new` opens the form with the saved draft. |
| Validation | **Pass.** Create task stays disabled for an empty or whitespace-only title, and while Repos is "—". With "Other…" and `not a repo name`, the server answers 400 `repo must be owner/name…`, which shows in an alert, and the form stays open. With vCPUs at -2 the browser's own check blocks the submit ("Value must be greater than or equal to 0."), so no request is sent. |
| Queued path, every option | **Pass.** I set title, a two-paragraph description, base `release-1.0`, auto-merge off, **This fixes a red release-1.0**, Priority high, Area `backe`+Enter and then `infra`+Tab, Requested by, Due date, Estimate, Customer-facing Yes, GCP key unticked, model `gemini-2.5-pro`, Network access None, nothing-to-do "Leave for acknowledgement", a prompt extension, and vCPUs/memory/disk 4/8192/40. Task 13 came back with every value as entered, including `fixesBaseBranch: true`, `area: [backend, infra]` (yesterday's fix holds), `capabilities: [gemini-key, self-debug]` and `agentFramework: antigravity`. |
| "Fixes a red" label | **Pass.** It follows the Base branch box ("…a red release-1.0"). It is hidden for No repo, and a ticked box is not sent once No repo is chosen. With "Create a new branch" and no name typed, it reads "…a red main" (see Observations). |
| Proposed path, No repo | **Fail, then fixed (Finding 1).** I picked `acme/widgets`, which prefilled Base `release-1.0`, then switched to No repo, unticked Queue immediately, set Priority low and picked Area docs. Task 14 was `proposed` with `noRepo` and Area docs as expected, but it carried `base: release-1.0`. |
| Escape and draft | **Pass.** Escape with nothing open closes the form and keeps the draft. Reopening restores the title, No repo and the unticked Queue immediately. Escape with the Area list open closes only the list. Picking an option closes the list, so the next Escape closes the form, as designed. After a create, the form is empty. Discard draft empties the title. |

### 3. All tasks, state views, narrowing

| Step | Outcome |
| --- | --- |
| Search | **Pass.** "walkthrough" narrows to 13 and 14 and writes `q=walkthrough`. A reload keeps both the box and the result. |
| State views from the rail | **Pass.** Proposed (14), Queued (1, 2, 4, 11, 13), Running (3, 8), Deferred (9), Closed (7, 10) and Blocked (11) each list exactly their tasks. |
| Back and Forward | **Pass.** Back goes Blocked → Closed → Deferred, and Forward returns to Closed, each with its rows. |
| Compound narrowing via deep link | **Pass.** `?f=custom.priority:any:high&q=walkthrough` lists 13 only. `?f=custom.priority:any:high\|low&sort=custom.priority:desc,title:asc` lists 2, 4, 5, 13 (high, by title) and then 8, 11, 9, 14 (low, by title). Opening row 2 and pressing Back restores that exact URL and those rows. |

### 4. Task detail

| Step | Outcome |
| --- | --- |
| Entry points | **Pass.** Inbox row, All tasks row, a Deferred-view row, and cold `/tasks/{id}` deep links (3, 4, 5, 8, 10, 11, 13, 14). |
| Title and description | **Pass.** Cancel sends nothing. Save sends one PATCH, and the header shows the new title after a reload. |
| Repo and base | **Pass, with Finding 1's consequence.** Giving no-repo task 14 the repo `acme/widgets` in its pane kept the leaked `release-1.0` as its base, unasked. Base `release-2.0` + Enter then persisted. On the fixed build, a no-repo task gets a repo with no base. |
| "This fixes a red <base>" in the pane | **Pass.** The label follows the base ("…a red release-2.0"). Each click sends one `PATCH {fixesBaseBranch}` and the value persists. It is hidden on closed task 10. |
| Auto-merge and custom fields | **Pass.** Auto-merge off is one PATCH. In Edit fields, Priority high, Requested by, and Area `fro`+Enter saved `area: [docs, frontend]`. |
| Proposal lifecycle (task 14) | **Pass.** Defer with no date → `deferred`; Undefer → `proposed`. A past date (2026-01-01) is a 400 "wake time … is not in the future" alert, and the task stays `proposed`. Defer until 2026-12-31 → `deferredUntil: 2026-12-31T00:00:00Z`, and the pane reads "Picks itself back up on 12/31/2026, 12:00:00 AM, back into the proposals…" before and after a reload. Undefer, then Approve → `queued`, which survives a reload. |
| Attempts, transcripts, chat, CI | **Pass, as far as the seed allows.** Task 3 shows its live attempt #1 transcript, and task 8 its attempt #2 ("Still running") with the merge-queue repair comment. A comment on task 3 survives a reload. Cancel on task 3 asks "Cancel this job? Its run will be abandoned: no pull request will be opened for it.", and dismissing it leaves the task `running`. Tasks 5 and 8 link their PRs to github.com. Director task 12's work list links 1, 3, 8 and 11. There were no CI runs to link. |

### 5. Bulk selection and edit

| Step | Outcome |
| --- | --- |
| Select rows | **Pass.** Ticking 13 and 14 shows "2 selected" with Approve, Submit, Defer, Undefer, Close, Reopen, Bulk edit…, and the capability picker. |
| Cancel a bulk edit | **Pass.** Cancel sends no PATCH and leaves both rows ticked. Mixed fields are labelled "(mixed across the selection)". |
| Apply | **Pass.** Auto-merge (Off), Due date 2026-12-15 and Area `inf`+Enter send one PATCH per task. Each task keeps its own priority, requested-by, estimate, base, model, capabilities and fixes-red mark, and the selection clears. |
| Off-screen selection | **Pass.** With task 13 searched out of view, Close confirms "Close 2 task(s)? 1 of them is not shown in this view.". Dismissing it leaves both tasks `queued`. Clear selection empties the bar, and nothing comes back after a reload. |

## Findings

### Finding 1 — a no-repo task kept the base branch of a repo picked before it (fixed)

**Reproduction:** deterministic, in Chromium against `grain demo`:

1. Open + New task and fill a title.
2. Pick `acme/widgets` under Repos. Base branch fills in from that repo's
   last task (or type one, e.g. `release-1.0`).
3. Switch Repos to "No repo (standalone task…)", then click Create task.
4. The request carries `"noRepo": true, "base": "release-1.0"`, and the
   task is stored with that base. Its row shows a `release-1.0` chip, and
   its pane reads "target: release-1.0" with the box holding
   `release-1.0`.
5. Pick `acme/widgets` for that task in its pane. It now lands on
   `release-1.0`, a branch nobody chose for it.

**Cause:** `NewTaskOverlay.submit` sent `base` whatever the repo choice. Its
own comment said the box "means nothing" for a task with no repo, and
`newBranch` was already left out in that case. The server stores `Base` as
given for `noRepo`.

**Fix:** the form sends `base: ""` for a No repo task. The server is
unchanged, because the task pane still lets a person give a no-repo task a
base on purpose (see Observations).

**Regression coverage:**

- `ui/src/components/NewTaskOverlay.repos.test.jsx`: a new test, "sends no
  base branch for a task switched to No repo after picking a repo". It
  fails without the fix (`Received: "release-1.0"`) and passes with it. The
  four NewTaskOverlay files pass (77 tests).
- `ui/e2e/task_intake_inbox_lifecycle_walkthrough.spec.js`: the proposed
  task now picks `acme/widgets`, types Base `release-9`, and then switches
  to No repo. The spec asserts that the filed task has no repo and no base,
  and that its row shows no `release-9`.
  - Against a build without the fix it fails (`Expected: "" / Received:
    "release-9"`).
  - With the fix, this file and `inbox.spec.js` pass (9 tests, Chromium).
- I re-checked by hand on a fresh store with the rebuilt binary. The same
  steps send `base: ""`. The pane has no target line, and adding
  `acme/widgets` in the pane gives the task that repo with no base.

### Observations (no change made)

- **Aborted polls are expected.** Each `GET /api/tasks` poll that comes
  back 304 shows in Playwright as `requestfailed … net::ERR_ABORTED`,
  roughly one every 3s. That is by design: `api.js` returns `UNCHANGED` on
  a 304 without reading a body. No error appears in the UI or the console.
  A future walkthrough should filter these lines out rather than chase
  them.
- **The "fixes a red" label falls back to "main".** With "Create a new
  branch" ticked and no name typed yet, the label reads "…a red main". The
  same fallback applies with no base typed, even though the repo's default
  branch may not be `main`. A branch being created can't be red yet, so
  the checkbox means little there. This is cosmetic, and Create stays
  disabled until a name is typed.
- **No repo still shows Base branch and auto-merge.** This was noted on
  10-11 and 10-12. A no-repo task's pane also still offers a Base branch
  box, so a person can still set one deliberately. Finding 1 only stops
  the form from carrying one over silently.
- **Task 5 links an undeclared field.** Its "File a task with these
  fields" link carries `field.sprint=2026-Q2`, but `sprint` is not a
  declared field. The form ignores it (seed data, as on 10-12).
- **The deep link `/queued` is not a route.** It lands on All tasks, not
  the Queued view. The rail's views use `/tasks?state=…`, and nothing in
  the UI links to `/queued`.
