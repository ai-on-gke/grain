# Task intake, Inbox and lifecycle UI walkthrough — 2026-10-29

- **Date:** 2026-10-29, 09:16–10:50 UTC
- **Task:** grain/049f1e09/task-1754, base `main`
- **Commit tested:** `425375a5` (`main` at the start of the run, "Merge pull
  request #132"). The two changes below were then checked on the same tree with
  them applied (branch `grain/049f1e09/task-1754`).
- **Environment:**
  - Debian sandbox (`kubernetes-granule` backend), 7 vCPUs, 15 GiB, `linux/amd64`
  - Go `go1.26.x` (`/usr/local/bin/go`), Node.js `v20.19.0`
  - Playwright from `ui/package-lock.json`, headless Chromium `chromium-1234`
    (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC, 1400×900
  - Frontend: `cd ui && npm ci && npm run build`. Server: `go build ./cmd/grain`,
    then `grain demo -addr 127.0.0.1:<port> -open=false -github-sim -fake-ci
    -merge-rules -merge-queue-states -secrets-store -access-code-file <file>`, with
    `TMPDIR` under `/var/tmp`, so each server had its own throwaway embedded SQLite
    store. Two servers: `:8420`, built from the unmodified commit (the first Inbox
    pass), and `:8421`, built with this branch's `-stalled-tasks` flag added
    (everything else).
  - There was no `/home/debian/.gemini-api-key`, so no model was called and
    `LOOK=1` could not describe screenshots. The demo's one agent connection,
    `gemini-pro` (Antigravity), advertises `gemini-2.5-pro` and `gemini-2.5-flash`.
- **How it was driven:** throwaway Playwright scripts (not committed) that sign in
  with `POST /api/auth/access-code` and then act only through visible controls.
  Each page recorded `pageerror`, console errors and warnings, responses of 400 or
  more, failed requests, and every non-GET `/api/` request with its body. The REST
  API was used only to read back what a gesture had persisted, except where a stub
  below says otherwise. The committed
  `ui/e2e/task_intake_inbox_lifecycle_walkthrough.spec.js` (12 tests) and the new
  `ui/e2e/inbox-stalled-group.spec.js` (2 tests) were also run: 14/14 pass.

## Stubs

Everything ran against the real server, store, built frontend and browser. Only
these boundaries were replaced. Each one stands in for something only an agent
run, a daemon reconciler or an external system writes:

1. **Held-for-approval proposal.** Only an agent's `propose_task
   hold_for_approval` writes this. With `sqlite3` on the throwaway store I set
   `origin_behalf_kind='task', origin_behalf_id='3'` and the `held-for-approval` tag
   on proposal #26, which I had filed through New task.
2. **Interactive task for the chat pane.** The demo's only interactive task is the
   Director console session (#12). That is a console session, so its compose bar
   has Stop and no Pause, by design (`TaskChat.jsx`). I set `interactive=1` on #24
   so that a non-console chat pane exists.
3. **Host-script approvals.** A run's runhost requests these. I added the grant
   `local-scripts:deploy-tools` to #2 (sqlite) and sent two
   `POST /api/tasks/2/local-script-approvals` requests with a stdin preview.
4. **Pending secret and suspended session.** I set `task_observation.pending_secret
   ='stripe-api-key'` on #20, and the `suspended_session_*` columns on #2
   (`sess-tok-walk-2`, `antigravity`, `demo-run-2-1`), with sqlite.
5. **Grain CI run for #8.** `-fake-ci` accepts runs but never executes them. I
   started one with `POST /api/ci/runs {"taskId":"8"}` (`fake-ci-1`), so #8 is a run
   waiting on CI.
6. **A merged pull request.** Nothing in the demo merges. I set
   `pr_merged_at`/`closed_at` on #19, so the rail's Merged view has a row.
7. **An agent's push.** I pushed `walkthrough.go` on `grain/task-24` into the
   `-github-sim` bare repository, which gives the Internal PR link a branch.
8. **Config-repo sync failure.** `grain demo` has no state repository to fail.
   `page.route` added `stateRepoSyncFailure` to the real `GET /api/config` answer.
   The new e2e test stubs the same thing.

## Steps and outcomes

### Inbox

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 1 | `/inbox` directly, and rail Inbox from `/` | Both land on `/inbox`. Groups: *Notifications 3*, *Asked: reply 2*, *Asked: submit for merge 8*, *Unblock a merge 1*, *Approve on GitHub 1*, *Approve a proposal 1*. They add up to the rail's `16`. | PASS |
| 2 | Click the proposal row, then Back | `/tasks/1` opens on "Add a dark mode toggle". Back returns to `/inbox`. | PASS |
| 3 | Approve #1 from its row | `POST /api/tasks/1/approve`. The proposal group goes away, #1 is `queued`, and the rail shows `15`. | PASS |
| 4 | Reply to #4: Send is disabled while empty; type; Send | `POST …/comments`. #4 leaves *Asked: reply* and is `queued`. The rail shows `14`. | PASS |
| 5 | Retry merge on #21 (*Unblock a merge*) | Sent at once with no confirm, which is right for a retry. The group empties, and #21 reads `completed/on_merge_queue`. | PASS |
| 6 | Stuck-task group (`-stalled-tasks`, new): rows and sub-cases | *Look at a stuck task 3*: #20 queued for more than 72h (`reconcileDispatch`) offers **Run now**; #21 running for more than 24h15m (`ProcessResult`, `RecoverOrphanedRuns` behind it) offers **Cancel**; seeded #5 on the merge queue (`syncEntry`) offers only Open task. Each row says its note out loud. | PASS |
| 7 | Run now on #20; Cancel on #21 | Run now sends `POST …/run-now` with no confirm, and the row leaves. Cancel asks "Cancel 21? The run in flight is stopped and the task is closed; it can be reopened later." Accepting it sends `POST …/close`, and the row leaves. After a reload only #5 is left. | PASS |
| 8 | Undefer sub-case | It cannot occur: `t.stall` is only written for queued, running and completed (`model.StateTimes.Since`), so a deferred task is never in this group. The comment in `InboxPage.jsx` that lists Undefer describes `reasonControl` in general. | N/A |
| 9 | Config-repo sync group (stub 8) | *Settings are not being exported 1*: "…since 10/29/2026, 9:00:00 AM: git fetch origin: authentication required", with a link to the config repo. **Bug 2:** the rail stayed at `16`. | FAIL → PASS |
| 10 | Local scripts group (stub 3) | *Local scripts waiting for approval 2*, each with the call, `On stdin: 42 bytes, sha256 abc`, the preview, and Approve/Deny. Approve on one sends `…/answer {"approve":true}` and the row leaves. **O1:** the rail does not count these either. | PASS (O1) |
| 11 | Empty state | Not reached on these stores: 8 *Asked: submit* rows and 3 notifications stand. Covered by the 10-26 run and by `inbox.spec.js`. | not repeated |

### New task

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 12 | Rail "Create options" | Menu: New task, New session, New board. New task opens `/new`. | PASS |
| 13 | Validation | Create is disabled with a whitespace title, and with no repo choice (`—`). | PASS |
| 14 | Capabilities | GCP key and Gemini key are pre-ticked and "Not ready -- needs gcp-key-minter". Unticking Gemini key keeps its row in the open list. Self debug was unticked; GCP key stayed ticked and was filed. | PASS |
| 15 | Grain API grants | "Add grant…" lists *Grain API presets* and scope groups (`tasks:*`, `merge:*`, …), with `dangerous` on admin scopes. Ticking `ci-read` adds the chip "api:ci-read". | PASS |
| 16 | Model picker | `Deployment default (gemini-2.5-pro)`, `gemini-2.5-pro`, `gemini-2.5-flash`: exactly what the one enabled connection advertises. Specific connection offers "gemini 2.5 pro · antigravity". | PASS |
| 17 | Refused field inside collapsed Advanced options | vCPUs `-1`, collapse Advanced, Create. Advanced reopens, focus lands on vCPUs ("Value must be greater than or equal to 0."), and no `POST /api/tasks` is sent. | PASS |
| 18 | Queued path, every field | Task 24 is filed `queued`: repo acme/widgets, `base: release/1.0`, `autoMerge: false`, `model: gemini-2.5-flash`, `agentBackendId: gemini-pro`, `networkAccess: none`, 4 vCPUs / 4096 MiB / 30 GiB, capabilities `api:ci-read, gcp-key, grain-ci`, and custom fields priority high, area frontend, requested-by, due-date, estimate. | PASS |
| 19 | Attachment | An attachment picked before the dialog was closed and reopened is gone when the form comes back, with nothing to say so; the title and the other fields come back (**O2**). Picked and filed in one go (task 25), it is stored: `GET /api/tasks/25/attachments/1` returns the bytes. | PASS (O2) |
| 20 | Proposed path: No repo, untick "Queue immediately" | Task 26 is filed `proposed`, `repo: ""`, `noRepo: true`. With No repo picked, Base branch is still shown (O2 of 10-26, still true). | PASS |
| 21 | `/new?field.priority=low&field.area=docs&field.area=infra&field.requested-by=Support%20team&field.customer-facing=true` | Priority low, Area docs + infra, Requested by "Support team", Customer-facing Yes. | PASS |
| 22 | `/new?field.priority=bogus` | The select shows blank, and `priority: ""` is filed. Nothing says the link's value was refused (**O3**). | PASS (O3) |
| 23 | Cancel: ✕, reopen, Discard draft | The draft title is kept; Discard draft empties it. | PASS |

### Task list

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 24 | Row ovals and chips | #11 `img "Blocked on: #8 (running)"`. #5 and #19 `img "Merging"`. #3 `img "Running"` with the sub-case chip "live run", whose title reads "running/live_run / Driven by ProcessResult / Waiting on the run in flight / "Cancel" ends this wait". #26 (stub 1) has "held for approval by 3", and after Approve (step 33) the chip is gone. | PASS |
| 25 | Rail Merged (stub 6) and Blocked | `?state=merged&sort=merged:desc` → #19. `?state=blocked&…` → #11. | PASS |
| 26 | Search `retry`, then add filter Priority is any of high, clear search, Sort by Title | `q=retry` → 2,13–18; plus priority → 2; priority alone → 2,4,5; `sort=title:asc` gives Fix…, Investigate…, Rotate…. Reload keeps all of it. | PASS |
| 27 | Back/Forward | Back from the narrowed list goes to `about:blank`: every narrowing edit replaced the history entry (O4 of 10-23, still true). Forward returns to the narrowed URL. | PASS (known) |
| 28 | Clear | Clears the search and the Priority filter back to the default `state:none:deferred|closed`, and keeps the sort (`NarrowingBar.jsx` does this deliberately). The result survives a reload. | PASS |

### Task detail

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 29 | Pause / Resume / Pause now on running #3 | Pause → `POST …/pause`, with "Paused by debian … (was Running)" and the agent's handover note. Resume → `…/resume`. Pause now → `…/pause {"immediate":true}`. Each one updates the pane without a reload, and a reload keeps Resume. The state stays `running`, and the backlog order is unchanged. None asks first (**O4**). | PASS (O4) |
| 30 | Chat pane Pause/Resume (stub 2, #24) | The compose bar's Pause → `…/pause`, and the button reads Resume (also after a reload). Resume → `…/resume`. #24 stays `queued`. | PASS |
| 31 | Force merge on #8 (running, CI pending, stub 5) | It asks with `window.prompt`. Dismissing it sends nothing. Accepting sends `POST …/force-merge {"reason":…}`, the pane shows "Asked grain to force the merge of acme/widgets#104…" without a reload, and #8 is `completed`. | PASS |
| 32 | Edit title/description (Save), Repo `—`→acme/widgets, Base `release/1.0`, Auto-merge, Edit fields | Each PATCH persists. A no-repo task's Repo select reads `—`, not "No repo" (**O5**). | PASS (O5) |
| 33 | Defer (no date) → Undefer → Defer until 2026-11-15 → reload → Undefer → Approve | `defer {"deferredUntil":""}` → `deferred`. Undefer → `proposed`. The dated defer stores `2026-11-15T00:00:00Z`, and the pane says "Picks itself back up on 11/15/2026, 12:00:00 AM, onto the queue." before and after a reload. Approve → `queued`, `approved: true`. | PASS |
| 34 | Dependency on #2 → #3, then remove | The suggestion menu offers #3. `POST …/depends-on`. The chip is labelled "3 Bump the Go toolchain to 1.24 — Running (blocking this task)", which is also its hover text. The rail shows Blocked 2, and the Blocked view lists 2 and 11. The chip's × removes it with no confirm, and Blocked goes back to 1. | PASS |
| 35 | Work tree on #11 | "provenance · work from task 12: 0 of 4 done", and expanded, "generated from 8 · origin 12". | PASS |
| 36 | Merge policy on #22 | Each line shows the rule behind it: `ci.mode: grain — from repository setting`, `ci.mode: both`, `auto_merge: never`, `stall_timeout: 30m`, `ci.grain: full suite`, `ci.github: build`, `ci.github: cla/google`, all `from rule deploy`. It adds "GitHub CI is narrowed to build, cla/google". | PASS |
| 37 | Grain CI on #8 → show the run → Escape | "grain CI is running against this task's branch…". The run pane shows `grain/task-8`, "No jobs…". Escape returns to `/tasks/8`. After the force merge the pane says CI "was cancelled", but the `-fake-ci` run still reads running: the stub cannot cancel. | PASS |
| 38 | Pending secret on #20 (stub 4) | A `type=password` box, "Value for stripe-api-key". Set secret is disabled while empty. `PUT /api/tasks/20/secret`. The timeline says "Set stripe-api-key. Its value is in grain's secret store…". The value is in neither the DOM nor the task JSON, and `grep -rlF` finds it in no file under the demo directory. | PASS |
| 39 | Suspended session on #2 (stub 4) | "Left by demo-run-2-1 on 10/29/2026, 8:00:00 AM (Antigravity)…", with the token. | PASS |
| 40 | Host-script approvals on #2 (stub 3) | Both calls appear in the pane with their stdin preview. One was approved from the Inbox and one denied from the pane (`{"approve":false}`). The timeline reads "Approved local script call walk-1" and "Denied local script call walk-2". | PASS |
| 41 | Parked question #7 | `.pending-question` "Waiting for your reply", with the question and a reply box. | PASS |
| 42 | Full prompt and live transcript on #3 | "prompt for 3 · attempt #1 · 10 kb · ~2,565 tokens · 1% of a 200,000-token window". Escape stays on `/tasks/3`. "live attempt #1 transcript" is inline. | PASS |
| 43 | Internal PR on #24 (stub 7) | The link `/repos/acme/widgets/compare/release/1.0...grain/task-24` shows "Into release/1.0 on acme/widgets -- forked at bda52951" and `walkthrough.go`. | PASS |
| 44 | Re-run CI / Retry merge | Retry merge: step 5 and the pane's button on a given-up task. Re-run CI is only offered on an `errored`/`failure`/`cancelled` verdict, and `-fake-ci` without `-scheduled-workflows` never finishes a run, so it was not reached. | partial |
| 45 | Cross-team approvals, attempt capability audit | Not reached in this run: `-sensitive-hold` (approvals) was not started, and #8's first attempt, which carries the seeded audit, was not opened from its attempt list. | not covered |

### Selection, bulk edit, manual mode

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 46 | Queued view, tick #2, #11, #20; Close; dismiss | "3 selected". Close asks "Close 3 task(s)?", and dismissing it sends nothing. | PASS |
| 47 | Bulk edit… → Cancel | Rows that differ say "(mixed across the selection)". Cancel sends nothing. | PASS |
| 48 | Bulk edit: tick Priority (medium) and Requested by ("Bulk walker"), then Apply to 3 tasks; reload | Three PATCHes carry only `customFields`, merged per task (#11 keeps its estimate). Base, auto-merge and capabilities are unchanged. The selection clears, and the row chips read "priority medium" after a reload. | PASS |
| 49 | System → Pipeline → Pause pipeline | It confirms ("Pause the pipeline? …"), then `POST /api/pipeline-pause`. Queued rows #2, #25, #26 and #27 carry "paused — waits for Run now". #20 does not: Run now was already pressed on it in step 7, which is the "already started" case. | PASS |
| 50 | Select #20 and #25 → Run selected now | `POST …/run-now` for each. #25's marker goes and stays gone after a reload and a few seconds of polling. Resume pipeline confirms, then `DELETE /api/pipeline-pause`, and every marker goes. | PASS |

### Console and network

There were no uncaught page errors. The only console message was a warning I
caused myself by filling a `type=date` box with a datetime. The 4xx responses
were my own probes of API paths that do not exist.

## Bug 1: the demo could never show the Inbox's stuck-task group (fixed here)

- **Reproduction:** `grain demo` (any flags), `GET /api/tasks`: no task has a
  `stall` field, so the *Look at a stuck task* group, and the stuck-task controls
  it offers, cannot be rendered against a real server.
- **Cause:** `ui.Config.Stall` is nil under `grain demo` (its own doc comment says
  so). Only `cmd/grain`'s daemon wires `orchestrator.TaskStallNote`.
- **Fix:** an opt-in `-stalled-tasks` demo flag (`pkg/demo/demo_stalled.go`). It
  wires `orchestrator.TaskStallNote`, as the daemon does, and seeds a queued task
  approved 96h ago and a run started 48h ago. It is opt-in, so the ordinary demo's
  Inbox, which other specs count, is unchanged.
- **Coverage:** `pkg/demo/demo_stalled_test.go` (each seeded task reads back a
  stall note and the Run now / Cancel control, and none does without the flag),
  and `ui/e2e/inbox-stalled-group.spec.js` (Run now and Cancel each take their row
  out of the group; Cancel asks first).

## Bug 2: the rail's Inbox count left out the config-repo sync row (fixed here)

- **Reproduction:** stub 8 on any demo, then open `/inbox`. The heading counts
  `waiting + localScripts + notifications + (syncFailure ? 1 : 0)`
  (`InboxPage.jsx`) and lists *Settings are not being exported*. The rail entry,
  whose count is meant to be "the same figure" (the comment on that heading),
  counts only waiting tasks and notifications: `16` against the page's `17`. On a
  deployment whose only trouble is the sync, the rail shows no count at all.
- **Fix:** `Sidebar.jsx` adds the sync failure to `inboxCount` and to its title.
- **Coverage:** `Sidebar.test.jsx` › "counts the config repository's sync failure,
  as the Inbox heading does" (79/79 pass), and the second test in
  `inbox-stalled-group.spec.js`.
- **Not fixed here (O1):** waiting local-script calls are also counted by the
  heading and not by the rail. Counting them means the rail polling
  `/api/local-script-approvals` on every page, which is a separate decision, so it
  is proposed as a follow-up.

## Observations (not changed)

- **O1:** the rail's Inbox count leaves out waiting local-script calls (above).
- **O2:** an attachment picked before the New task dialog is closed is not part of
  the kept draft. Nothing says so when the form comes back with its title and
  fields.
- **O3:** a `/new?field.<name>=` value that is not one of the field's options
  leaves the select blank and files the field unset, without a word.
- **O4:** Pause now stops a running agent mid-step with no confirm, while Cancel
  asks. It is reversible (Resume), but the run's in-flight work is not handed
  over.
- **O5:** the pane's Repo select shows `—` for a task filed with No repo, where
  New task names it "No repo (standalone task…)".
- **Still true from earlier runs:** narrowing edits replace the history entry; with
  No repo, New task still shows Base branch.
