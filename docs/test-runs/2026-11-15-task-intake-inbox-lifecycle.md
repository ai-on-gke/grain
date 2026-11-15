# Task intake, Inbox and lifecycle walkthrough — 2026-11-15 (task 5886)

- **Date:** 2026-11-15, 03:55–04:50 UTC, one run.
- **Task:** task 5886, branch
  `grain/53549e42/task-5886-walk-through-task-intake-inbox`, base `main`.
- **Commit tested:** `3cc8bc207` ("Merge pull request #1532"). Each fix
  below was checked again on a rebuild of that commit with it applied.
- **Focus:** the 2026-11-14 report walked `/next/` Inbox groups, the New
  task sheet's queued/proposed paths, the Deferred tab and the basic task
  lifecycle, and listed what it did not cover. This run went after that
  list on `/next/` first: `request_access` cards, Pause now, Stop/Resume,
  dependencies, the goal card, attachments, Mermaid, the grant picker,
  Retry merge / Force merge, the config-change card, bulk approval,
  alert acknowledgement and the release row.
- **Environment:**
  - Debian sandbox, `linux/amd64`; Go 1.26.10; Node.js v20.19.0; Playwright
    1.62.1 from `ui-ng/package-lock.json` with headless Chromium
    (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC,
    viewport 1400×900 unless a step says 390×844.
  - Built with `make frontend` and `go build -o /home/debian/grain
    ./cmd/grain`. The fixed builds re-ran `cd ui-ng && npm run build` and
    `go build`.
  - Server: `grain demo -addr 127.0.0.1:<port> -open=false -github-sim
    -fake-ci -scheduled-workflows -fork-pull-request -git-proxy
    -base-branch-ci -merge-rules -merge-queue-states -stalled-tasks
    -sensitive-hold -review-questions -human-review -secrets-store
    -notifications -qualification -mechanical-port -config-change
    -access-code-file <file>`. `TMPDIR` pointed at a fresh scratch
    directory for each start, so each store was a throwaway embedded SQLite
    file. Three servers ran: `:8430` (base build), `:8431` (first fixes,
    fewer flags) and `:8432` (all task-page fixes, full flags).
  - There was no `/home/debian/.gemini-api-key`, so no model was called.
- **How it was driven:** throwaway Playwright scripts kept outside the
  checkout. Each signs in with the run's access code
  (`ui-shared/e2e/accesscode.js` `signIn`, which goes through the
  access-code sign-in as a person does) and then acts only through visible
  controls. Each page logged uncaught page errors, console errors and
  warnings, failed requests, `/api/` responses of 400 or more (with
  bodies), native dialogs, and every non-GET `/api/` request with its
  body. The REST API (bearer access code) read back what a gesture
  persisted.
- **Console baseline:** no uncaught page error, console error or 4xx/5xx
  response appeared on any `/next/` page. The only failed requests were
  `GET /api/config … net::ERR_ABORTED`, where a script navigated away
  while a read was still in flight.

## Stubs and seeds

The server, store, built frontend, browser and gestures were real
throughout. Nothing was stubbed in Playwright this run.

1. **`request_access` parks:** the demo's loopback-only seeding route
   `POST /api/e2e/tasks/{id}/pending-access` (`pkg/demo/demo_access_request.go`,
   the route `ui/e2e/grant-matrix-ui.spec.js` uses) parked #2 on
   `read acme/gadgets` + `network open` + `api#ci-read {repo:
   acme/widgets}` and #20 on `network open` + `api#tasks:admin`. The demo
   seeds no parked agent of its own that asks for access.
2. **Host-script approvals and pending secrets** have no demo seed and no
   e2e seeding route, so neither was walked (see "Not covered").

## Which UI each area ran against

| Area                                          | UI                                   |
| --------------------------------------------- | ------------------------------------ |
| Inbox                                         | `/next/inbox`                        |
| New task sheet                                | `/next/` sheet (rail **New**)        |
| Tasks list, tabs, search, history             | `/next/tasks`                        |
| Task detail, gates, lifecycle, Changes and CI | `/next/task/<id>`                    |
| System → Pipeline manual mode, Run selected now | no `/next/` page (`system/:tab` redirects to classic) |
| `/new?field.<name>=` links                    | no `/next/` page                     |
| Boards                                        | classic only (not walked)            |

No step was walked on the classic UI this run. The only classic code
read was to compare behaviour (Pause now, Retry, goal fields, open-alert
counting).

## Steps and outcomes

### 1. Inbox (`/next/inbox`)

| #    | Step | Outcome |
| ---- | ---- | ------- |
| 1.1  | Open `/next/tasks`, then the rail's **Inbox**. | Lands on `/next/inbox`. Headline "29 decisions waiting on you", rail badge 29. Groups: release 1, reply 4, review 1, submit 10, merge-blocked 1, GitHub approval 1, stalled 5, proposal 3, alerts 3. |
| 1.2  | Select #1 (proposal) → **Approve**. | `POST /api/tasks/1/approve`; row leaves; headline and rail 28; API `queued`. |
| 1.3  | Select #4 (parked question), type a reply, **Send**. | `POST /api/tasks/4/comments`; row leaves; API `queued`. |
| 1.4  | **Approve all 2** on the proposals group. | Confirm "Approve 2 proposals… 2 in acme/widgets, 1 owned by debian, 1 owned by operator". **Approve 2** stays disabled until "I am approving work owned by someone else" is ticked. Two approves; group gone; both `queued`. |
| 1.5  | Select the "A merge has been stalled" alert → **Acknowledge**. | `POST /api/notifications/1/ack`; row stays, marked "acknowledged". **Bug 5:** headline and rail stayed at 24. Fixed: both drop by one. |
| 1.6  | Select the seeded acknowledged alert (flaky webkit e2e). | Offers only Investigate and Clear. |
| 1.7  | Select the v2.4.0 release row. | Approve (`approve-release`) and Reject (`reject-release`). The soak is already complete (25h10m of 24h), so there is no short-soak choice to walk. |
| 1.8  | Reload. | Counts and rows keep what the gestures did. |

### 2. Creating tasks (`/next/` New task sheet)

| #   | Step | Outcome |
| --- | ---- | ------- |
| 2.1 | Rail **New**. | Template, title, description, attachments, the demo's custom fields, repository, Create a new branch, base, auto-merge, model ("Deployment default (gemini-2.5-pro)", the only option), Depends on, Capabilities & repo context (3 grants), More options, Approve and start now, Put at front of queue. |
| 2.2 | Attach two PNGs through **Attach file**. | Both listed as staged chips with a remove `×`. |
| 2.3 | Capabilities → **+ Add API scope or method grant** → open **API scope set**, press `Escape`. | Only the menu closes; the sheet and builder stay. A second `Escape` closes the sheet. |
| 2.4 | Reopen **New**. | Title and both staged attachments are restored. |
| 2.5 | **Create and start**. | `POST /api/tasks`; #40 `queued`, with both attachments stored (`pic.png`, `pic2.png`). |

### 3. Tasks (`/next/tasks`)

| #   | Step | Outcome |
| --- | ---- | ------- |
| 3.1 | Type `retry` in **Filter this view**. | URL `?q=retry`. |
| 3.2 | Open **Submit & land**, Back, Forward, reload. | URL and box follow: `?q=retry&tab=merging` ↔ `?q=retry`; reload keeps both. |
| 3.3 | `?tab=closed`. | Merged / Not merged / All filter; closed rows show the rollover chip ("Rolled over to #22"). |
| 3.4 | Add a dependency (step 4.5), return to Tasks. | Backlog still says "1 waiting on a dependency" (#11 already waited on #8). |

### 4. Task detail and lifecycle (`/next/task/<id>`)

| #    | Step | Outcome |
| ---- | ---- | ------- |
| 4.1  | Visit every seeded task's page. | All render with no console error and no sideways overflow at 1400 px. |
| 4.2  | #2 parked on a `request_access` (Seed 1). | Card "Waiting for an access grant: …" with the reason. The refused `read acme/gadgets` row is unticked and disabled with the server's reason; `ci-read` shows "already held", unticked and disabled; `network: open` is ticked. **Grant all** becomes **Grant selected (0)** and is disabled once network is unticked; `Space` re-ticks it. **Deny…** needs a reason before **Deny request** turns on. Deny posts the reason; card gone; API `queued`; the timeline comment lists what was declined; still gone after reload. |
| 4.3  | #20 parked on `network open` + `api#tasks:admin` → **Grant all**. | `tasks:admin` carries a "dangerous" mark. Grant posts; API shows `networkAccess: open` and `api#tasks:admin` among the capabilities; task `queued`. |
| 4.4  | #3 (running) → **Pause**. | `{"immediate":false}`; Paused pill, **Resume**; state stays `running` during the grace period. |
| 4.5  | More ▾ → **Pause immediately**. | **Bug 1:** posted `{"immediate":true}` at once, with no question, though it throws away unpushed work (classic Pause now asks). Fixed: a confirm dialog with classic's wording; Cancel sends nothing. |
| 4.6  | #8 (running) → **Stop**, then **Resume** (the stop's). | `POST …/stop`; `awaiting_reply`, `turnStopped`; Resume asks "alice@example.com (access code) stopped it"; `POST …/retry`; `queued`. |
| 4.7  | #3, paused and then stopped. | **Bug 2:** two buttons both labelled **Resume**. The one calling Retry is always refused (`3 is paused by …: Resume it to put it back in the queue`). Fixed: not offered while paused. |
| 4.8  | #11 → type `9` → **Add**. | `POST …/depends-on {"id":"9","attach":true}`; `dependsOn` `["8","9"]`. **Bug 3:** chips said only `#9`, with no state and no title on hover. Fixed: "#9 Deferred", tip "#9 Replace the settings pane with a form library (Deferred)". |
| 4.9  | **Edit**: title, priority (High 8), auto-merge off → **Save changes**, reload. | `PATCH` carries all three; persisted; header shows the new title. The priority picker offers only 9/8/5/3/1. |
| 4.10 | Goal: Base branch green, at limit **Notify & back off** → **Set goal**; reopen the editor and save again; **Clear goal**. | **Bug 4:** the reopened editor showed "Hold on limit", and the re-save sent the stored row back in camelCase, so the server stored `onLimit: hold`. Fixed: the editor shows Notify, and the re-save keeps `notify`. Clear goal: `goal` null and the Details "Goal" line gone after reload. |
| 4.11 | #40 (two images, two Mermaid blocks). | Mermaid blocks render as plain code blocks with Copy. Thumbnails are links to the file; there is no full-screen viewer. (Follow-up F1.) |
| 4.12 | #28 (merge blocked) → Changes and CI. | Merge-queue line "gave up waiting on CI after 30m: checks that have not finished (`cla/google`)". Submit is off ("Approval: needed"). **Retry merge** posts and the page follows. **Force merge…** opens a dialog with gate ticks ("CI (running)") and a required reason; `Escape` closes it. |
| 4.13 | #38 config change → **Approve & merge config**. | `POST …/config-change/approve` with the head SHA; card gone; task `queued`. |
| 4.14 | At 390×844: #20 → **Close**, **Reopen**. | Close acts at once, with no reason field and no confirmation (as O1 in the 2026-11-14 report). History reads "Closed · alice@example.com (access code)", then "reopened · alice@example.com (access code)". No sideways overflow. |
| 4.15 | #20 → **Defer**, **Bring back**. | `{"deferredUntil":""}` → `deferred` → `queued`. There is no date control on `/next/`. |

## Bugs found and fixed

### Bug 1 — Pause immediately discarded unpushed work without asking

**Reproduction:** `grain demo`; open `/next/task/3` (running); More ▾ →
**Pause immediately**. `POST /api/tasks/3/pause {"immediate":true}` goes
out at once.

**Fix:** `pause(true)` opens the page's confirm dialog ("Pause #N now? Its
run is stopped at once, without the grace period … whatever it has not
pushed is lost"), as the classic Pause now's `confirm` does. Plain Pause
still acts directly.

**Coverage:** `ui-ng/e2e/next-task-lifecycle-regressions.spec.js` "Pause
immediately asks first, and Cancel leaves the run going". Fails on the
base build.

### Bug 2 — a dead second "Resume" on a paused, stopped task

**Reproduction:** `/next/task/8` → **Stop** → **Pause**. Two **Resume**
buttons show. The `act-retry` one gets 400 `… is paused by …: Resume it to
put it back in the queue` (`ui.Client.retry`).

**Fix:** `act-retry` is not offered while `t.paused`. Once the pause is
lifted it comes back and works.

**Coverage:** "a stopped task that is also paused offers only the pause's
Resume". Fails on the base build. Classic offers its **Retry** in the same
state and shows the server's refusal. It is labelled differently, so it
was left alone.

### Bug 3 — dependency chips said only "#N"

**Reproduction:** `/next/task/2` → add dependency `9` → the chip reads
`#9`, with nothing on hover.

**Fix:** superseded. `main` fixed the same fault independently in
`02fdfc741` ("name a dependency chip's task and state on hover"), with the
Details chip's title reading the server's `dependsOnStates` and
`blockedBy`, so it covers closed dependencies too. When this branch was
merged with `main`, the branch's own feed-based chip labels were dropped
in favour of that fix.

**Coverage:** `ui-ng/e2e/next-intake-walkthrough.spec.js` (from `main`).

### Bug 4 — the goal editor reset `on_limit` / `on_met` on every re-save

**Reproduction:** `/next/task/2` → Edit → Goal kind **Base branch green**,
At limit **Notify & back off** → **Set goal**. Reload → Edit: the editor
shows "Hold on limit". **Set goal** again: the stored goal reads
`onLimit: "hold"`.

**Cause:** `TaskGoalView` extended the snake_case `GoalDecl` (`on_met`,
`on_limit`), but `GET /api/tasks/{id}` returns the `task_goal` row
(`model.TaskGoal`), which is camelCase. The editor read `on_limit`
(always undefined), fell back to its defaults, and spread the whole row
(`taskId`, `declaredBy`, progress, and the previous kind's `spec`) into
the PUT body.

**Fix:** `TaskGoalView` is now the row's own camelCase shape. The editor
reads `onMet`/`onLimit`, and a save carries only a same-kind goal's spec
and its progress limit (`max_resumes`, `max_stall`). Classic
`GoalSection.jsx` already read the camelCase fields.

**Coverage:** "the goal editor keeps a goal's at-limit choice across a
re-save, and Clear goal clears it". Fails on the base build ("Hold on
limit").

### Bug 5 — acknowledged alerts still counted as waiting

**Reproduction:** `grain demo -notifications`; `/next/inbox` → select "A
merge has been stalled for 2 hours" → **Acknowledge**. The row is marked
"acknowledged", but the headline and rail badge do not move. The seeded,
already-acknowledged flaky-e2e alert is counted from the first paint.

**Fix:** `InboxStore.decisions`/`scopedDecisions`, which both counts read,
skip acknowledged entries. The row stays in Alerts, marked, until it is
cleared. This matches the group's own hint ("An alert stays open until
somebody acknowledges or clears it") and the classic open-alert filter
(`ui/src/notifications.js`). The 2026-11-14 report's step 1.9 recorded
the counts dropping on acknowledge. This build did not, and the seeded
acknowledged alert was counted at 29.

**Coverage:** "acknowledging an alert takes it out of the Inbox count, and
its row stays marked". Fails on the base build (badge 14, expected 13).

## Tests run

- `npx vitest run src/app/inbox src/app/task src/app/tasks` in `ui-ng/`:
  7 files, 133 tests pass.
- `npx playwright test e2e/next-task-lifecycle-regressions.spec.js
  --project=desktop`: 5 pass (4 once the dependency test was dropped in
  favour of `main`'s). Each test was run against the unfixed
  frontend and failed there.
- `npx playwright test e2e/next-full-system.spec.js
  e2e/next-task-interactions.spec.js e2e/next-intake-lifecycle.spec.js
  e2e/next-inbox-intake-regressions.spec.js e2e/next-inbox.spec.js
  e2e/next-task-lifecycle-regressions.spec.js`: 24 pass.
- `npx playwright test` (the whole `ui-ng` e2e suite, all fixes applied):
  83 pass.
- `npm run format:check` and `tsc -p tsconfig.app.json --noEmit` in
  `ui-ng/` pass.

## Rendering

`/next/task/11` with the dependency tip shown, and `/next/task/3` with the
Pause immediately dialog open, were shot at 1400×900 and 390×844 in light
and dark. The file reader here cannot render PNGs and there was no Gemini
key, so the layout was checked by bounding boxes:

- Chip: 110×24 at x=1081 (desktop) and x=12 (phone).
- Tip: 288×28 directly under the chip, starting at x=954 (desktop) and
  x=4 (phone).
- Dialog: 570×258 centred (desktop), 358×318 with 16 px gutters (phone).
- No sideways overflow at either width.

None of the shots were committed.

## Observations (not treated as defects)

- **O1:** **Run now** is offered on proposed, awaiting-reply,
  awaiting-submit and completed tasks. On a proposal it also approves,
  and on a parked question it un-parks. This matches classic
  (`Actions.jsx`: "offered on every state but the two where it…") and
  `ui.Client.RunNow`.
- **O2:** `/next/` Close still acts with no confirmation and takes no
  reason. It is reversible with Reopen.
- **O3:** On a phone, the task page renders the Details block twice (the
  tab and the hidden side column), so each Details `data-testid` matches
  two elements, one of them hidden. Scripts need `:visible`.
- **O4:** A comment written by the `pending-access` seeding route shows its
  author as "(unverified)" with no name. That is the seed, not the UI.
- **O5:** While running the access-card step, a careless selector pressed
  the `×` on #11's `ci-read` capability, which really detached it (`POST
  …/capabilities {"attach":false}`). The control worked as labelled
  ("Remove grant …"). It is recorded here because #11's later state
  reflects it.

## Gaps on `/next/` (no page or control yet)

These steps of the walkthrough could not be run on `/next/`, and only
classic offers them:

- **Task detail:** Mermaid rendering (zoom, fullscreen, invalid-syntax
  fallback), and the full-screen image viewer (`X`/backdrop/`Escape`,
  arrows, `i / N`, Download). Follow-up F1.
- **Tasks selection bar:** Submit, Retry merge, Reopen, capability
  attach/detach, Bulk edit, Run selected now, and the manual-pipeline
  "waits for Run now" row marker. System → Pipeline itself is classic-only
  (`/next/system/:tab` redirects). Follow-up F2.
- **Task edit and create:** Owner, External ref, Reviewers, Keep running,
  custom fields on an existing task, free whole-number Task priority (the
  `1e3`/zero/negative rules), Defer with a date, the Admission (`Standard`,
  `Bypass queue`, `Durable`) and Hosting controls, `This fixes a red
  <base>` on an open task, no-repo filing, and the fork head label.
  Follow-up F3.
- Already listed in 2026-11-14: `/new?field.<name>=` links and the
  `settings/new-task-form.yml` notice, the Tasks State/Admission/Human
  review/External ref/Origin filters, server-side closed search and
  `owner:any:me`, collapsible Inbox sections.

## Not covered

- Host-script approvals and the pending-secret box: no demo seed or
  seeding route.
- Suspended `resume_later` sessions, cross-team approvals, the live
  transcript and the capability audit: not seeded in this demo
  configuration.
- The chat pane's Pause/Resume, and pipeline manual mode (classic-only, see
  above).
- Boards: classic only.
