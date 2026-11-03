# Task intake, Inbox and lifecycle UI walkthrough — 2026-10-30

- **Date:** 2026-10-30, 11:20–12:20 UTC
- **Task:** grain/049f1e09/task-1900, base `main`
- **Commit tested:** `8bdfa3a5` (`main` at the start of the run, "Merge pull
  request #182"). The fix below was then checked on the same tree with it
  applied (branch `grain/049f1e09/task-1900`).
- **Environment:**
  - Debian sandbox (`kubernetes-granule` backend), `linux/amd64`
  - Go `/usr/local/bin/go`, Node.js `v20.19.0`
  - Playwright from `ui/package-lock.json`, headless Chromium `chromium-1234`
    (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC, 1400×900
  - Frontend `cd ui && npm ci && npm run build`; server `go build ./cmd/grain`
    (after the frontend build, so the embedded `pkg/ui/static` is current).
  - `:8420`, the unmodified commit: `grain demo -addr 127.0.0.1:8420 -open=false
    -github-sim -fake-ci -scheduled-workflows -merge-rules -merge-queue-states
    -secrets-store -stalled-tasks -sensitive-hold -review-questions
    -access-code-file <file>`, `TMPDIR=/var/tmp/walk`, so its store was a
    throwaway embedded SQLite file. `:8421`, the fixed build, plain
    `grain demo -access-code-file <file>`, for re-checking the fix.
  - There was no `/home/debian/.gemini-api-key`, so no model was called and
    `LOOK=1` could not describe screenshots. `sqlite3` was installed with apt
    for the stubs.
- **How it was driven:** throwaway Playwright scripts (not committed) that sign in
  with `POST /api/auth/access-code` and then act only through visible controls.
  Every page recorded `pageerror`, console errors and warnings, responses of 400
  or more, failed requests, every dialog (confirm/prompt text and whether it was
  accepted), and every non-GET `/api/` request with its body. The REST API was
  used only to read back what a gesture persisted, except where a stub says
  otherwise.

## Stubs

The server, store, built frontend, browser and gestures were real throughout.
Each stub stands in for something only an agent run, a reconciler or an external
system writes:

1. **Held-for-approval proposal** (an agent's `propose_task hold_for_approval`):
   sqlite `origin_behalf_kind='task', origin_behalf_id='3'` and the
   `held-for-approval` tag on seeded proposal #12.
2. **Pending secret** (an agent's `request_secret`): sqlite
   `task_observation.pending_secret='stripe-api-key'` on #21.
3. **Suspended session** (a run that suspends): a `task_observation` row for #2
   with `suspended_session_*` set (#2 had no observation row, so an `UPDATE`
   alone does nothing — insert one).
4. **Host-script approvals** (a run's runhost): grant
   `local-scripts:deploy-tools` on #2 (sqlite), then two
   `POST /api/tasks/2/local-script-approvals` with a stdin preview.
5. **A grain CI run and a failed verdict for #6**: `POST /api/ci/runs
   {"taskId":"6"}` (`fake-ci-1`; `-fake-ci` never settles a task branch's run),
   then sqlite `ci_conclusion='failure'`, `ci_finished_at`, `ci_detail`.
6. **A merged pull request** (nothing in the demo merges): `pr_merged_at` and
   `closed_at` on #19, so the rail's Merged view has a row.
7. **An agent's push**: `grain/task-29` (one file) pushed into the
   `-github-sim` bare repository, for the Internal PR link.
8. **An interactive task for the chat pane**: `interactive=1` on #26. (This
   also takes #26 out of the task list, which is right for a session.)
9. **Config-repo sync failure** (no state repository in `grain demo`):
   `page.route` added `stateRepoSyncFailure` to the real `GET /api/config`.
10. **Inbox empty and error states**: `page.route` emptied the arrays in
    `GET /api/tasks`, `/api/notifications` and `/api/local-script-approvals`,
    then separately answered `GET /api/tasks` with a 500.

## Steps and outcomes

### Inbox

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 1 | `/inbox` directly, then the rail's Inbox from `/` | Both land on `/inbox`. Groups and counts: Notifications 3, Asked: reply 2, Review needs your decision 1, Asked: submit for merge 9, Unblock a merge 1, Approve on GitHub 1, Look at a stuck task 3, Approve a proposal 2. Sum 22 = the rail's `22` ("19 tasks waiting on you, 3 notifications"). | PASS |
| 2 | Click proposal #1's title, then Back | `/tasks/1` "Add a dark mode toggle"; Back returns to `/inbox`. | PASS |
| 3 | Stuck group: sub-cases and controls | #21 queued >72h (`reconcileDispatch`) offers **Run now**; #22 running >24h15m (`ProcessResult`, `RecoverOrphanedRuns` behind it) offers **Cancel**; #5 on the merge queue >12h15m (`syncEntry`) offers only Open task. Each row's note is shown. | PASS |
| 4 | Run now on #21 | `POST …/21/run-now`, no confirm. Row leaves; group 3→2; #21 `queued/ready`. | PASS |
| 5 | Cancel on #22: dismiss, then accept | Confirm "Cancel 22? The run in flight is stopped and the task is closed; it can be reopened later." Dismiss sends nothing (#22 still running). Accept: `POST …/22/close`, row leaves, #22 `closed`. | PASS |
| 6 | Retry merge on #24 (Unblock a merge) | `POST …/24/retry-merge`, no confirm (a retry). Group gone; #24 `completed/on_merge_queue`, row oval "Merging". | PASS |
| 7 | Undefer sub-case | Not reachable: `liveness.go` gives `deferred` `noPatience`, so a deferred task never carries a stall note. Unchanged since 10-29. | N/A |
| 8 | Approve #1 from its row | `POST …/1/approve`; proposal group 2→1 (its Approve all goes with the second row); #1 `queued`. | PASS |
| 9 | Reply on #4: Send while empty, then type and Send | Send disabled when empty. `POST …/4/comments`; #4 leaves Asked: reply and is `queued`. Rail `17` ("14 tasks…, 3 notifications"); a reload shows the same groups. | PASS |
| 10 | Config-repo sync (stub 9) | "Settings are not being exported 1", with the repo link and "…since 10/30/2026, 9:00:00 AM: git fetch origin: authentication required". The rail counts it (`16`, title "…, settings are not being exported"): the 10-29 fix holds. | PASS |
| 11 | Local scripts group (stub 4) | "Local scripts waiting for approval 2", each with the call, `On stdin: 42 bytes, sha256 abc123` and the preview. The rail now counts them ("…, 2 local scripts waiting for approval"): 10-29's O1 is fixed on `main`. Approve on one: `…/lsa-1/answer {"approve":true}`, the row leaves. | PASS |
| 12 | Empty and error states (stub 10) | Empty: "Nothing is waiting on you. Every task is either running, waiting or done." and no Inbox count. A 500 on `/api/tasks`: the page says "500 Internal Server Error" and still lists the notifications. | PASS |
| 13 | Keyboard | Tab reaches the rail, every group toggle, each row's title link, chips and controls in order. Enter on a group toggle flips `aria-expanded` true→false and back. | PASS |

### New task

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 14 | Rail "Create options" | Menu: New task, New session, New board; New task opens `/new`. The rail's "+ New task" from `/tasks` also opens it. | PASS |
| 15 | Validation | Create task is disabled for an empty title, a whitespace title, and with no repo choice (`—`). | PASS |
| 16 | Model picker | `Deployment default (gemini-2.5-pro)`, `gemini-2.5-pro`, `gemini-2.5-flash`: exactly `/api/config`'s one enabled connection (`gemini-pro`, models `gemini-2.5-pro`, `gemini-2.5-flash`). Specific connection: "any enabled connection serving the model", "gemini 2.5 pro · antigravity". | PASS |
| 17 | Capabilities | GCP key and Gemini key are pre-ticked and read "not ready -- needs gcp-key-minter…". Unticking Self debug leaves the menu open with Gemini key still selected; Gemini key and GCP key are filed. | PASS |
| 18 | Grain API grants | "Add grant…" lists Grain API presets (task-admin, ci-read, dispatch-control, deployment-ops *dangerous*, …) then scope groups. Picking ci-read files `api:ci-read`, shown on the row as "api preset: ci-read". | PASS |
| 19 | Refused field inside collapsed Advanced | vCPUs `-2`, collapse Advanced (`aria-expanded=false`), Create. Advanced reopens (`aria-expanded=true`), focus is on vCPUs ("Value must be greater than or equal to 0."), no `POST /api/tasks`. | PASS |
| 20 | Queued path, every field (from `/tasks` via "+ New task") | Task 29: `queued`, acme/widgets, `base release/1.0`, `autoMerge:false`, capabilities `api:ci-read, gcp-key, gemini-key, grain-ci`, `model gemini-2.5-flash`, `networkAccess none`, 4 vCPU / 4096 MiB / 30 GiB, custom fields priority high, requested-by, estimate, and the attachment `att.txt` (17 bytes). | PASS |
| 21 | Proposed path, No repo, from `/new?field.priority=low&field.area=frontend&field.requested-by=Linker` | All three prefilled; "Queue immediately" unticked; task 30 filed `proposed` with those three fields. Base branch is still shown with No repo (known since 10-23). | PASS |
| 22 | Cancel: ✕, reopen, Discard draft | The title is kept as a draft; Discard draft empties it. | PASS |
| 23 | `/new?field.priority=bogus`, `/new?field.nosuch=1` | **Bug 1** (10-29's O3): the select shows blank and nothing says the link's value was dropped; an unknown field name is ignored silently. Fixed here. | FAIL → PASS |

### Task list

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 24 | Ovals and chips | #11 `img "Blocked on: #8 (running)"`; #5, #20, #24 `img "Merging"`; #3 "Running" with chip "live run" whose title reads "running/live_run Driven by ProcessResult Waiting on the run in flight "Cancel" ends this wait"; #5 also "stalled" with its note; #12 (stub 1) "held for approval by 3", gone after Approve (step 35). | PASS |
| 25 | Rail views | Merging → `/?state=completed&f=none` (5,20,24); Blocked → `/?state=blocked&…` (11); Proposed (12,30); Closed (22,10); Deferred (9); Merged (stub 6) → `/?state=merged&sort=merged:desc` (19). | PASS |
| 26 | Search `retry`, add filter Priority is any of high, clear search, sort by Title | `q=retry` → 2,14–19; + priority → 2; priority alone → 2,4,5,29; `sort=title:asc` orders them Fix…, Investigate…, Rotate…, Walkthrough…. A reload keeps all of it. | PASS |
| 27 | Back/Forward | Back from the narrowed list goes to the entry before `/` (narrowing edits replace the history entry; known since 10-23). Forward returns to the narrowed URL with the same rows. | PASS (known) |
| 28 | Clear, reload; deep link `/?state=running` | Clear drops search and Priority back to the default state filter and keeps the sort; a reload keeps it. The deep link lists 3, 8. | PASS |

### Task detail

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 29 | Pause → Resume → Pause now → Resume on running #3 | `…/pause`, `…/resume`, `…/pause {"immediate":true}`, `…/resume`; the pane follows without a reload, a reload after Pause keeps Resume and the agent's handover note. #3 stays `running`; backlog order unchanged. None asks first (O4 of 10-29, still true). | PASS |
| 30 | Chat pane Pause/Resume (stub 8, #26) | Compose bar: Stop, Pause. Pause → `…/pause`, button reads Resume, also after a reload; Resume → `…/resume`. #26 stays `queued`. | PASS |
| 31 | Force merge on #8 (repairing, attempt #2 running) | `window.prompt` explains the override and requires a reason. Dismiss sends nothing. Accept sends `POST …/8/force-merge {"reason":"walkthrough: CI is stuck"}`; the pane says "Asked grain to force the merge of acme/widgets#104 at 000000000000…" without a reload and #8 is `completed`. (The zero SHA is the demo's synthetic head `000…068`.) | PASS |
| 32 | Attempt and capability audit on #8 | "3 capability audit records" opens attempt #1's transcript with "capability audit · 3 records": two `github.com › host-script` calls with argv, exit code, duration, one `refused: argument 4 holds a NUL byte`, and "the capability server left out 1 further record(s)". Escape closes it and stays on `/tasks/8`. | PASS |
| 33 | Edit #30: title/description (Save), Repo `—`→acme/widgets, Base `release/1.0`, Edit fields, Auto-merge off; reload | Five PATCHes, each persisted after a reload (`customFields` merged; area kept). The no-repo Repo select reads `—` (O5 of 10-29, still true). | PASS |
| 34 | Defer (no date) → Undefer → Defer until 2026-11-15 → reload → Undefer → Approve | `defer {"deferredUntil":""}` → `deferred`; Undefer → `proposed`; dated defer stores `2026-11-15T00:00:00Z` and says "Picks itself back up on 11/15/2026, 12:00:00 AM, back into the proposals…" after a reload; the deferred unapproved task offers Approve and Undefer; Approve → `queued`, `approved:true`. | PASS |
| 35 | Approve held proposal #12 | `queued`; the row's "held for approval by 3" chip is gone. | PASS |
| 36 | Withdraw approval on #26 (a security-review task) → Approve | `…/withdraw-approval` with no confirm (Approve undoes it, `Actions.jsx`); #26 → `proposed`; Approve → `queued`. | PASS |
| 37 | Dependency #29 → #3, then remove | The picker ("Add a dependency…") offers #3; `POST …/depends-on {"attach":true}`. Chip "3 bump the go toolchain to 1.24 (open)", hover "3 Bump the Go toolchain to 1.24 — Running (blocking this task)". Rail Blocked 1→2; Blocked view 11, 29, #29's oval "Blocked on: #3 (running)". × sends `{"attach":false}`, Blocked back to 1. | PASS |
| 38 | Work tree on #8 | "provenance · work from task 13: 0 of 4 done"; expanded: "generated from 13 · origin 13", then 13, 1, 3, 8 (with acme/widgets#104), 11 with states and links to each. | PASS |
| 39 | Merge policy on #23/#24 | `ci.mode: grain — from repository setting`, then `ci.mode: both`, `auto_merge: never`, `stall_timeout: 30m`, `ci.grain: full suite`, `ci.github: build`, `ci.github: cla/google`, each "from rule deploy", and "GitHub CI is narrowed to build, cla/google…". | PASS |
| 40 | Grain CI verdict on #6 (stub 5) and Re-run CI | "grain CI: failure. 1 job failed: go test…" with show the run, Re-run CI, Re-run failed jobs. Re-run CI sends at once (non-destructive, no confirm by design) and starts `fake-ci-2`; the pane goes back to "grain CI is running…" without a reload. "show the run" opens the run pane (pending, `grain/task-6`, cancel run); Escape returns to `/tasks/6`. | PASS |
| 41 | Pending secret on #21 (stub 2) | `type=password` box "Value for stripe-api-key"; Set secret disabled while empty; `PUT /api/tasks/21/secret`; timeline "Set stripe-api-key. Its value is in grain's secret store, not in this conversation…". The value is in neither the DOM nor the task JSON, and `grep -rlF` finds it in no file under the demo directory. | PASS |
| 42 | Suspended session on #2 (stub 3) | "Left by demo-run-2-1 on 10/30/2026, 8:00:00 AM (Antigravity)…" with the token. | PASS |
| 43 | Host-script approvals on #2 (stub 4) | Both calls in the pane with stdin preview; one approved from the Inbox (step 11), one denied from the pane (`{"approve":false}`). Timeline: "Approved local script call walk-1", "Denied local script call walk-2". | PASS |
| 44 | Parked question #7 | `.pending-question` "Waiting for your reply", the question, a reply box and Send. | PASS |
| 45 | Full prompt and live transcript on #3 | "prompt for 3 · attempt #1 · 10 kb · ~2,570 tokens · 1% of a 200,000-token window"; Escape stays on `/tasks/3`. "live attempt #1 transcript" inline. | PASS |
| 46 | Internal PR on #29 (stub 7) | The link (`target=_blank`) opens `/repos/acme/widgets/compare/release/1.0...grain/task-29` in a new tab: "Into release/1.0 on acme/widgets -- forked at ed54370c", `walkthrough.go +1 -0` and its diff. The same URL opened cold shows the same. | PASS |
| 47 | Cross-team approvals | This tree has no cross-team approval section (no such component under `ui/src` or `pkg/ui`); the nearest controls, a repository gating hold (#25, "held by repository gating triggers (human: required)") and a review task's withdraw approval (step 36), were exercised. | N/A |

### Selection, bulk edit, manual mode

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 48 | Queued view, tick #2, #11, #21; Close; dismiss | "3 selected"; confirm "Close 3 task(s)?"; dismissing sends nothing and keeps the selection. | PASS |
| 49 | Bulk edit… → Cancel | Fields that differ say "(mixed across the selection)". Cancel sends nothing and keeps the selection. | PASS |
| 50 | Bulk edit Priority=medium, Requested by="Bulk walker", Apply to 3 tasks; reload | Three PATCHes with `customFields` only, merged per task (#11 keeps estimate 3 and area). Base, auto-merge and capability counts unchanged. Selection cleared; rows read "priority medium" after a reload. | PASS |
| 51 | System → Pipeline → Pause pipeline | Confirm, then paused ("Paused — only tasks you Run now will start…"). Queued rows 1, 2, 4, 12, 28, 29, 30 carry "paused — waits for Run now"; blocked #11 and #21 (Run now already pressed in step 4) do not. | PASS |
| 52 | Select #1, #2 → Run selected now; reload after 5s | `POST …/run-now` for each; their markers go and stay gone after the reload. Resume pipeline confirms, then `DELETE /api/pipeline-pause`; every marker goes. | PASS |

### Console and network

No uncaught page errors and no console errors except those caused on purpose
(the stubbed 500 in step 12). No failed requests.

## Bug 1: a `/new?field.` link's dropped values were dropped without a word (fixed here)

- **Reproduction:** on any `grain demo`, open `/new?field.priority=bogus` (or
  `/new?field.nosuch=1`). The Priority select is blank and nothing on the form
  says the link asked for anything; filing it files `priority: ""`. The person
  who followed a link someone sent them files a different task from the one the
  sender meant, with nothing to tell them (10-29's O3).
- **Cause:** `NewTaskOverlay.jsx` seeds the fields with `fileableFieldValues`,
  which drops values a field no longer offers on purpose (filing should not be
  refused for them, as its comment says). Nothing reported what it dropped.
- **Fix:** `unfiledLinkValues` (`CustomFields.jsx`) names each value the link
  carried that `fileableFieldValues` leaves out, and each name no declared field
  answers to; New task shows them in a warning above the fields ("This link
  asked for values this form has left out: Priority: “bogus” is not one of its
  values; “nosuch” is not a field on this deployment."). Filing is unchanged.
- **Coverage:** `CustomFields.test.jsx` › `unfiledLinkValues` (4 cases),
  `NewTaskOverlay.test.jsx` › "says which of a link's field values it left
  out", and `ui/e2e/new-task-link-values.spec.js` against the real demo,
  including bounding-box checks that the notice sits inside the dialog and above
  Priority at 1400 and 390 px. Screenshots of `/new?field.priority=bogus&field.nosuch=1`
  before (`:8420`) and after, desktop and phone, light and dark, were taken with
  `ui/e2e/screenshot.mjs` (not committed).

## Observations (not changed)

- **O4 (10-29):** Pause now stops a running agent with no confirm while Cancel
  asks. Reversible by Resume.
- **O5 (10-29):** a no-repo task's Repo select reads `—` on its pane; New task
  calls it "No repo (standalone task…)". New task still shows Base branch with
  No repo.
- **Fixed since 10-29:** O1 (the rail now counts waiting local-script calls).
- Narrowing edits still replace the history entry (known since 10-23).
