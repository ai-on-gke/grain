# Task intake, Inbox and lifecycle UI walkthrough — 2026-11-01

- **Date:** 2026-11-01, 03:11–04:00 UTC
- **Task:** grain/049f1e09/task-2085, base `main`
- **Commit tested:** `8688c0cf` (`main` at the start of the run, "Merge pull
  request #282"). The fix below was then checked on the same tree with it
  applied (branch `grain/049f1e09/task-2085`).
- **Environment:**
  - Debian sandbox (`kubernetes-granule` backend), `linux/amd64`
  - Go `/usr/local/bin/go`, Node.js `v20.19.0`
  - Playwright from `ui/package-lock.json`, headless Chromium `chromium-1234`
    (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC, 1400×900
    (and 390×844 for the phone checks)
  - Frontend `cd ui && npm ci && npm run build`; server `go build ./cmd/grain`
    (after the frontend build, so the embedded `pkg/ui/static` is current).
  - `:8420`, the unmodified commit: `grain demo -addr 127.0.0.1:8420 -open=false
-github-sim -fake-ci -scheduled-workflows -merge-rules -merge-queue-states
-secrets-store -stalled-tasks -sensitive-hold -review-questions
-notifications -access-code-file <file>`, `TMPDIR=/var/tmp/walk`, so its
    store was a throwaway embedded SQLite file. `:8421`, the fixed build, plain
    `grain demo -access-code-file <file>`, for re-checking the fix and the
    after screenshots.
  - There was no `/home/debian/.gemini-api-key`, so no model was called and
    `LOOK=1` could not describe screenshots. `sqlite3` was installed with apt
    for the stubs.
- **How it was driven:** throwaway Playwright scripts (not committed) that sign in
  with `POST /api/auth/access-code` and then act only through visible controls.
  Every page recorded `pageerror`, console errors and warnings, responses of 400
  or more, failed requests, every dialog (its text, and whether it was accepted
  or dismissed), and every non-GET `/api/` request with its body. The REST API
  was used only to read back what a gesture persisted, except where a stub says
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
   with `suspended_session_*` set. Timestamps must be written in Go's
   `2026-11-01 01:00:00 +0000 UTC` form; an RFC 3339 string does not read back.
4. **Host-script approvals** (a run's runhost): grant
   `local-scripts:deploy-tools` on #2 (sqlite `task_grant`), then two
   `POST /api/tasks/2/local-script-approvals` with a stdin preview.
5. **A grain CI run and a failed verdict for #6**: `POST /api/ci/runs
{"taskId":"6"}` (`fake-ci-1`), then sqlite `ci_run_id`, `ci_conclusion='failure'`,
   `ci_started_at`, `ci_finished_at`, `ci_detail`.
6. **A merged pull request** (nothing in the demo merges): `pr_merged_at` and
   `closed_at` on #19, so the rail's Merged view has a row.
7. **An agent's push**: `grain/task-29` (one file, `walkthrough.go`) pushed into
   the `-github-sim` bare repository from `release/1.0`, for the Internal PR link.
8. **An interactive task for the chat pane**: `interactive=1` on #26.
9. **Config-repo sync failure** (no state repository in `grain demo`):
   `page.route` added `stateRepoSyncFailure` to the real `GET /api/config`.
10. **Inbox empty and error states**: `page.route` emptied the arrays in
    `GET /api/tasks`, `/api/notifications` and `/api/local-script-approvals`,
    then separately answered `GET /api/tasks` with a 500.
11. **An edit target**: proposal #31 filed with `POST /api/tasks` (no repo,
    unapproved), after an sqlite reset of #30 left it with a broken `/` repo
    (that reset is also where the only `404 /api/repos/github-branches` of the
    run came from; it is not a product fault). Priorities 3, 10 and 1 were
    `PATCH`ed onto #1, #2 and #4 for the sort and filter checks.

## Steps and outcomes

### Inbox

| #   | Step                                                | Outcome                                                                                                                                                                                                                                                                      | Verdict |
| --- | --------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | `/inbox` directly, then the rail's Inbox from `/`   | Both land on `/inbox`. Groups and counts: Notifications 3, Asked: reply 2, Review needs your decision 1, Asked: submit for merge 9, Unblock a merge 1, Approve on GitHub 1, Look at a stuck task 3, Approve a proposal 2; rail `22`.                                         | PASS    |
| 2   | Click proposal #1's title, then Back                | `/tasks/1`; Back returns to `/inbox`.                                                                                                                                                                                                                                        | PASS    |
| 3   | Stuck group: sub-cases and controls                 | #21 queued >72h (`reconcileDispatch`) offers **Run now**; #22 running >24h15m (`ProcessResult`) offers **Cancel**; #5 on the merge queue >12h15m (`syncEntry`) offers only Open task. Each row shows its note.                                                               | PASS    |
| 4   | Run now on #21                                      | `POST …/21/run-now`, no confirm. Row leaves; group 3→2.                                                                                                                                                                                                                      | PASS    |
| 5   | Cancel on #22: dismiss, then accept                 | Confirm "Cancel 22? The run in flight is stopped and the task is closed; it can be reopened later." Dismiss sends nothing (#22 still running). Accept: `POST …/22/close`, row leaves, #22 `closed`.                                                                          | PASS    |
| 6   | Retry merge on #24 (Unblock a merge)                | `POST …/24/retry-merge`, no confirm. Group gone; #24's list oval reads "Merging".                                                                                                                                                                                            | PASS    |
| 7   | Undefer sub-case                                    | Not reachable: `liveness.go` still gives `deferred` `noPatience`, so a deferred task never carries a stall note (unchanged since 10-29; `InboxPage.jsx`'s comment still names Undefer).                                                                                      | N/A     |
| 8   | Approve #1 from its row (Approve / Defer / Decline) | `POST …/1/approve`; proposal group 2→1; #1 `queued`, `approved:true`.                                                                                                                                                                                                        | PASS    |
| 9   | Reply on #4: Send while empty, then type and Send   | Send disabled when empty. `POST …/4/comments`; #4 leaves Asked: reply (2→1) and is `queued`. Rail `17`, title "14 tasks waiting on you, 3 notifications"; a reload shows the same groups.                                                                                    | PASS    |
| 10  | Config-repo sync (stub 9)                           | "Settings are not being exported 1" with the repo link and "Settings have not been exported to acme/grain-config since 11/1/2026, 1:00:00 AM: git fetch origin: authentication required". Rail `18`, title "…, settings are not being exported".                             | PASS    |
| 11  | Local scripts (stub 4)                              | "Local scripts waiting for approval", the rail title counts them; Approve from the Inbox sends `…/lsa-1/answer {"approve":true}` and the row leaves (step 37 below).                                                                                                         | PASS    |
| 12  | Empty and error states (stub 10)                    | Empty: "Nothing is waiting on you. Every task is either running, waiting or done." and no Inbox count. 500 on `/api/tasks`: after the poll settles, the page says "500 Internal Server Error" and still lists the notifications (the rail's Tasks reads `0` meanwhile — O2). | PASS    |
| 13  | Keyboard                                            | Tab reaches the rail items, Alerts, System, Settings, every group toggle, each row's title link, chips and controls in order. Enter and Space on a group toggle flip `aria-expanded`.                                                                                        | PASS    |

### New task

| #   | Step                                                                                                                                                                                                                                                                                 | Outcome                                                                                                                                                                                                                                                                                                                         | Verdict |
| --- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 14  | Rail "Create options" and "+ new task"                                                                                                                                                                                                                                               | Menu: New task, New session, New board; both open `/new`.                                                                                                                                                                                                                                                                       | PASS    |
| 15  | Queued path, everything filled: title, description, attachment `notes.txt`, repo acme/widgets, base `release/1.0`, auto-merge, Priority=high, Requested by, untick Self debug, Grain API grant `ci-read`, model `gemini-2.5-flash`, network none, 4 vCPU / 8192 MiB, Task priority 7 | One `POST /api/tasks`; #29 `queued`, `approved:true`, every field as entered (`priority:7`, `capabilities: api:ci-read, gcp-key, gemini-key, grain-ci`, `attachments: notes.txt 17 B`). The "this fixes a red …" box follows the base ("this fixes a red release/1.0").                                                         | PASS    |
| 16  | Not-ready capability stays ticked                                                                                                                                                                                                                                                    | GCP key and Gemini key read "not ready -- needs gcp-key-minter…"; unticking Self debug leaves the menu open with both still selected, and both are filed.                                                                                                                                                                       | PASS    |
| 17  | Grain API group                                                                                                                                                                                                                                                                      | "Add grant…" lists Grain API presets (task-admin, ci-read, dispatch-control, deployment-ops _dangerous_, release-manager, …) then scopes, as `menuitemcheckbox`es; ci-read is filed as `api:ci-read` and shown as "api preset: ci-read" on the row.                                                                             | PASS    |
| 18  | Model picker                                                                                                                                                                                                                                                                         | Deployment default (gemini-2.5-pro), gemini-2.5-pro, gemini-2.5-flash: exactly `/api/config`'s one connection (`gemini-pro`, models `gemini-2.5-pro`, `gemini-2.5-flash`). Specific connection: "any enabled connection serving the model", "gemini 2.5 pro · antigravity". Network: deployment default (egress), egress, none. | PASS    |
| 19  | Proposed path from `/new?field.priority=low&field.requested-by=Link%20Sender&field.area=api`, No repo, "queue immediately" unticked                                                                                                                                                  | Prefilled Priority low and Requested by; the notice says `Area: “api” is not one of its values`. #30 filed `proposed`, `noRepo`, customFields as linked.                                                                                                                                                                        | PASS    |
| 20  | Link values notice, more cases                                                                                                                                                                                                                                                       | `field.area=api&field.area=backend&field.nosuch=1` keeps backend and names "api" and "nosuch"; `field.area=backend,docs` is named as not one of its values (the 10-30 fix holds).                                                                                                                                               | PASS    |
| 21  | Cancel                                                                                                                                                                                                                                                                               | Close dialog with a typed title files nothing and asks nothing; reopening restores the draft (title, Task priority 9, Priority high, unticked capability, auto-merge off, repo). "Discard draft" is on the form.                                                                                                                | PASS    |
| 22  | Validation                                                                                                                                                                                                                                                                           | Create disabled for an empty or whitespace title. vCPUs `-1` inside collapsed Advanced options: Create opens Advanced, focuses vCPUs, "Value must be greater than or equal to 0." Task priority `2.5`: "Please enter a valid value. The two nearest valid values are 2 and 3.", nothing filed.                                  | PASS    |
| 23  | Repo change after edits                                                                                                                                                                                                                                                              | Changing the repo (— → acme/widgets → No repo → acme/widgets) keeps edited Priority, Task priority, auto-merge and the unticked capability (`formEdited` / `capabilitiesEdited`).                                                                                                                                               | PASS    |
| 24  | Keyboard                                                                                                                                                                                                                                                                             | Tab walks title → description → attach → fields → repo → base → … → Advanced options → queue immediately → backlog end → Task priority → Discard draft → Create → Close and wraps; Escape closes.                                                                                                                               | PASS    |

### Tasks list

| #   | Step                                    | Outcome                                                                                                                                                                                                                                                                                                                                                                 | Verdict                                                                                                                      |
| --- | --------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------- |
| 25  | Rail Tasks, Blocked, Merged             | Tasks → `/?f=state:none:deferred                                                                                                                                                                                                                                                                                                                                        | closed`; Blocked → `?state=blocked`(#11); Merged →`?state=merged&sort=merged:desc` (#19, stub 6; the rail gains "merged 1"). | PASS |
| 26  | Row ovals and chips                     | #11 "Blocked on: #8 (running)"; #24 "Merging"; #3 "Running" with sub-case chip "live run" whose title is "running/live_run · Driven by ProcessResult · Waiting on the run in flight · "Cancel" ends this wait"; #12 "held for approval by 3" until approved, then gone (step 34). #29 shows "p7".                                                                       | PASS                                                                                                                         |
| 27  | Search, sort, compound, URL, history    | Search "pagination" → `q=pagination` (#8, #11); sort by created → `sort=created:desc`; add Priority is any of low → `&f=custom.priority:any:low`; reload keeps all of it and the search box; Back/Back/Forward/Forward walk merged view ↔ `/` ↔ the narrowed list; removing filter 2 and clearing search drop their params; "No tasks match your search." for no match. | PASS                                                                                                                         |
| 28  | Numeric Task priority (new since 10-30) | Sort by task priority → `sort=priority:asc`, rows 1,3,5…,7,8,10 in order, kept on reload. Filter comparisons is / is not / is more than / is less than / is empty / is set; `priority:gt:5`, `lt:5`, `eq:5`, `ne:5` each give the right rows.                                                                                                                           | PASS                                                                                                                         |

### Task detail

| #   | Step                                                                                                                                            | Outcome                                                                                                                                                                                                                                                                                                                   | Verdict          |
| --- | ----------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------- |
| 29  | Edit #31: title, description, Task priority 8 (Save), repo — → acme/widgets, base `release/1.0`, auto-merge, Edit fields (Requested by); reload | Five PATCHes, each persisted after a reload; the pane shows "priority: 8".                                                                                                                                                                                                                                                | PASS             |
| 30  | Edit Task priority 0, 2.5, -2, empty (Bug 1)                                                                                                    | 0 saved **5**, 2.5 saved **2**, both with no message; -2 refused by the server ("priority must be a positive integer"); empty leaves it. Fixed here.                                                                                                                                                                      | **FAIL → fixed** |
| 31  | Defer (no date) → Undefer → Defer until 2026-11-20 → reload → Undefer → Approve                                                                 | `defer {"deferredUntil":""}` → `deferred`, offering Approve and Undefer; dated defer stores `2026-11-20T00:00:00Z` and says "Picks itself back up on 11/20/2026, 12:00:00 AM, back into the proposals…" after a reload; Approve → `queued`.                                                                               | PASS             |
| 32  | Attempts and capability audit on #8                                                                                                             | "live attempt #2 transcript"; "3 capability audit records" opens "capability audit · 3 records" (argv, exit code, duration, one `refused: argument 4 holds a NUL byte`); Escape stays on `/tasks/8`.                                                                                                                      | PASS             |
| 33  | Work tree on #8                                                                                                                                 | "provenance · work from task 13: 0 of 4 done"; expanded: "generated from 13 · origin 13", then 13, 1, 3, 8 (acme/widgets#104), 11, each with state and link.                                                                                                                                                              | PASS             |
| 34  | Approve held proposal #12                                                                                                                       | `queued`; the "held for approval by 3" chip leaves its row.                                                                                                                                                                                                                                                               | PASS             |
| 35  | Full prompt and live transcript on #3                                                                                                           | "prompt for 3 · attempt #1 · 10 kb · ~2,615 tokens · 1% of a 200,000-token window"; Escape stays on `/tasks/3`; "live attempt #1 transcript" inline.                                                                                                                                                                      | PASS             |
| 36  | Parked question #7                                                                                                                              | `.pending-question` "Waiting for your reply", the question, a reply box and Send.                                                                                                                                                                                                                                         | PASS             |
| 37  | Host-script approvals on #2 (stub 4)                                                                                                            | Both calls in the pane: `deploy.sh "--env" "staging-1" (and 42 bytes on stdin)`, "On stdin: 42 bytes, sha256 abc123" and the preview. Deny from the pane (`{"approve":false}`), Approve from the Inbox; timeline "Approved local script call walk-1", "Denied local script call walk-2".                                  | PASS             |
| 38  | Merge policy on #23                                                                                                                             | `ci.mode: grain -- from repository setting`, then `ci.mode: both`, `auto_merge: never`, `stall_timeout: 30m`, `ci.grain: full suite`, `ci.github: build`, `ci.github: cla/google`, each "from rule deploy", and "GitHub CI is narrowed to build, cla/google…".                                                            | PASS             |
| 39  | Grain CI verdict on #6 (stub 5), Re-run CI                                                                                                      | "grain CI: failure. 1 job failed: go test…" with show the run, Re-run CI, Re-run failed jobs, Force merge. Re-run CI sends `POST /api/ci/runs {"taskId":"6"}` at once and the pane says "grain CI is running…" without a reload.                                                                                          | PASS             |
| 40  | Pending secret on #21 (stub 2)                                                                                                                  | `type=password` "Value for stripe-api-key"; Set secret disabled while empty; `PUT /api/tasks/21/secret`; timeline "Set stripe-api-key. Its value is in grain's secret store, not in this conversation…". The value is in neither the DOM nor the task JSON, and `grep -rlF` finds it in no file under the demo directory. | PASS             |
| 41  | Suspended session on #2 (stub 3)                                                                                                                | "Left by demo-run-2-1 on 11/1/2026, 1:00:00 AM (Antigravity). Any further run of this task resumes this conversation…" with the token.                                                                                                                                                                                    | PASS             |
| 42  | Dependency #29 → #3, then remove                                                                                                                | The picker offers "Bump the Go toolchain to 1.24 · 3"; `POST …/depends-on {"id":"3","attach":true}`. Chip "3 bump the go toolchain to 1.24 (open) ×", tooltip "3 Bump the Go toolchain to 1.24 — Running (blocking this task)". Rail Blocked 1→2; Blocked view #29, #11. × sends `attach:false`; Blocked back to 1.       | PASS             |
| 43  | Pause / Resume, queued #2 from its page                                                                                                         | `POST …/pause`; still `queued` with `paused.fromState queued`, same place in the queued order; "Paused by debian." after a reload; Resume clears it, order unchanged.                                                                                                                                                     | PASS             |
| 44  | Pause now on running #3                                                                                                                         | `POST …/pause {"immediate":true}` with no confirm (O4 of 10-29, unchanged); stays `running`, Resume clears it.                                                                                                                                                                                                            | PASS             |
| 45  | Chat pane on interactive #26 (stub 8)                                                                                                           | The chat carries Stop and Pause; Pause → `paused`, Resume clears, queue order unchanged.                                                                                                                                                                                                                                  | PASS             |
| 46  | Force merge on #23 (waiting on CI)                                                                                                              | Prompt "Force this merge? …Say why. It is required…". Dismiss sends nothing; a blank reason sends `{"reason":""}` and the snackbar says "forcing a merge needs a reason…"; a reason sends it and the pane says "Asked grain to force the merge of acme/widgets#131 at a1b2c3d4e5f6…" without a reload.                    | PASS             |
| 47  | Withdraw approval on #2 → Approve                                                                                                               | `withdraw-approval` → `proposed`; Approve → `queued`.                                                                                                                                                                                                                                                                     | PASS             |
| 48  | Internal PR on #29 (stub 7)                                                                                                                     | The link (`target=_blank`) opens `/repos/acme/widgets/compare/release/1.0...grain/task-29`: "Into release/1.0 on acme/widgets -- forked at d6a3bf68", `walkthrough.go +1 -0` and its diff; the same URL opened cold shows the same.                                                                                       | PASS             |
| 49  | Cross-team approvals                                                                                                                            | No such section in this tree (nothing under `ui/src` or `pkg/ui`); the nearest control, withdraw approval, is step 47.                                                                                                                                                                                                    | N/A              |

### Selection, bulk edit, manual mode

| #   | Step                                                                | Outcome                                                                                                                                                                                                              | Verdict |
| --- | ------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 50  | Queued view, tick #2, #11, #21; Close; dismiss                      | Bar "3 selected" with approve, run selected now, submit, defer, undefer, close, reopen, bulk edit…, attach, detach, clear selection. Confirm "Close 3 task(s)?"; dismissing sends nothing and keeps the selection.   | PASS    |
| 51  | Bulk edit… → Cancel                                                 | Fields that differ say "(mixed across the selection)". Cancel sends nothing and keeps the selection.                                                                                                                 | PASS    |
| 52  | Bulk edit Requested by="Bulk walker", Apply to 3 tasks              | Three PATCHes with `customFields` only, merged per task (#11 keeps priority, area and estimate); base, auto-merge, capabilities and Task priority unchanged; selection cleared. Bulk edit has no Task priority (O3). | PASS    |
| 53  | System → Pipeline → Pause pipeline                                  | Confirm, then "Paused — only tasks you Run now will start…". Queued rows 31, 29, 1, 2, 4, 12, 28 carry "paused — waits for Run now"; blocked #11 and #21 (Run now already pressed in step 4) do not.                 | PASS    |
| 54  | Select #1, #2 → Run selected now; reload after 5 s; Resume pipeline | `POST …/run-now` for each; their markers go and stay gone after the reload. Resume confirms, `DELETE /api/pipeline-pause`, every marker goes.                                                                        | PASS    |
| 55  | Phone width (390 px)                                                | `/inbox`, `/new`, `/tasks/29`, `/?state=queued`: `scrollWidth` 390, no horizontal overflow.                                                                                                                          | PASS    |

### Console and network

No uncaught page errors. The only console errors and 4xx/5xx responses were the
ones caused on purpose (the stubbed 500 in step 12, the refused force-merge and
priority in steps 30 and 46) and one `404 /api/repos/github-branches` caused by
stub 11's broken reset of #30. No failed requests.

## Bug 1: the task Edit form saved a different Task priority from the one typed (fixed here)

- **Reproduction:** on any `grain demo`, open `/tasks/2`, press Edit, set Task
  priority to `0` and Save: the task's priority becomes 5. Set it to `2.5` and
  Save: it becomes 2. Neither says anything. (New task refuses both, because
  it is a `<form>` and the browser enforces the box's `min=1 step=1`.)
- **Cause:** `EditTaskForm.jsx` is not a `<form>`, so the box's `min` and
  `step` are never checked on Save. It sends `parseInt(value)`: `2.5` becomes
  2, and `0` reaches `UpdateTask`, which reads 0 as "unset" and stores the
  default 5.
- **Fix:** the form only accepts a whole number from 1, or empty to leave the
  priority alone. Anything else marks the box invalid with "a whole number, 1
  or more; default 5, higher numbers run first" under it and disables Save.
- **Coverage:** `EditTaskForm.test.jsx` › "refuses a Task priority of %s
  rather than saving another" (0, 2.5, -2; fails without the fix), and
  `ui/e2e/edit-task-priority.spec.js` against the real demo, including a
  bounding-box check that the reason sits under the box. Crops of the box with
  `0` typed, before (`:8420`) and after (`:8421`), at 1400 and 390 px in light
  and dark, were taken with Playwright (not committed): before, Save enabled
  and `aria-invalid=false`; after, Save disabled and `aria-invalid=true`.

## Observations (not changed)

- **O2:** while `GET /api/tasks` fails, the rail's Tasks count reads `0`
  instead of keeping the last count or showing that it is unknown.
- **O3:** bulk edit offers no Task priority, so reprioritising several tasks
  is one Edit per task (filed as a follow-up).
- **O4 (10-29):** Pause now stops a running agent with no confirm while Cancel
  asks. Reversible by Resume.
- **O5 (10-29):** New task still shows Base branch with No repo, and a no-repo
  task is filed with `autoMerge: true` (the deployment default).
- While a task pane is open it is an `aria-modal` dialog over the list, so the
  rail is `aria-hidden` and does not take clicks; Escape or ← back closes it
  first. This is by design, but it means the rail counts cannot be clicked
  from a task.
- Narrowing edits (search, sort, filters) still replace the history entry
  (known since 10-23); Back steps between views.

---

This second walkthrough ran on `ea9bceb0`, before task-2085's fix above had merged. Its own fix is the New task form's number boxes; the Edit form's priority check above already refuses `1e3` there.

## Second run of the day — task-2233, 04:07–05:40 UTC

- **Date:** 2026-11-01, 04:07–05:40 UTC
- **Task:** grain/049f1e09/task-2233, base `main`
- **Commit tested:** `ea9bceb0` (`main` at the start of the run, "Merge pull
  request #338"). The fix below was then checked on the same tree with it
  applied (branch `grain/049f1e09/task-2233`).
- **Environment:**
  - Debian sandbox, `linux/amd64`; Go `/usr/local/bin/go`, Node.js `v20.19.0`
  - Playwright from `ui/package-lock.json`, headless Chromium `chromium-1234`
    (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC, 1400×900
  - Frontend `cd ui && npm ci && npm run build`; server `go build ./cmd/grain`
    after it, so the embedded `pkg/ui/static` was current.
  - `:8420`, the unmodified commit: `grain demo -addr 127.0.0.1:8420 -open=false
-github-sim -fake-ci -scheduled-workflows -merge-rules -merge-queue-states
-secrets-store -stalled-tasks -sensitive-hold -review-questions
-access-code-file <file>`, `TMPDIR=/var/tmp/walk`, so the store was a
    throwaway embedded SQLite file. The fixed build was run through
    `npx playwright test` (its own per-worker demo server).
  - There was no `/home/debian/.gemini-api-key`, so no model was called.
    `sqlite3` was installed with apt for the stubs.
- **How it was driven:** throwaway Playwright scripts (not committed). They sign
  in with `POST /api/auth/access-code`, then act only through visible controls.
  Every page logged `pageerror`, console errors and warnings, responses of 400
  or more, failed requests, every dialog (its text and whether it was
  accepted), and every non-GET `/api/` request with its body. The REST API was
  used only to read back what a gesture had persisted, except where a stub
  below says otherwise.

### Stubs

The server, store, built frontend, browser and gestures were real throughout.
Each stub stands in for something that only an agent run, a reconciler or an
external system writes:

1. **Held-for-approval proposal** (an agent's `propose_task
hold_for_approval`): sqlite `origin_behalf_kind='task', origin_behalf_id='3'`
   on #12, and a `task_tag` row `held-for-approval`.
2. **A merged pull request** (nothing in the demo merges): `pr_merged_at` and
   `closed_at` on #19's `task_observation`.
3. **Pending secret** (an agent's `request_secret`):
   `task_observation.pending_secret='stripe-api-key'` on #21.
4. **Suspended session** (a run that suspends): an inserted `task_observation`
   row for #2 with `suspended_session_*` set.
5. **A grain CI run and a failed verdict for #6**: `POST /api/ci/runs
{"taskId":"6"}` (`fake-ci-1`), then sqlite `ci_conclusion='failure'`,
   `ci_finished_at` and `ci_detail`.
6. **A deferral whose wake time has passed** (probing the Undefer stall
   sub-case): `deferred_until='2026-10-28'` on #9. This was reverted afterwards.

### Steps and outcomes

#### Inbox

| #   | Step                                              | Outcome                                                                                                                                                                                                                                                                                          | Verdict          |
| --- | ------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ---------------- |
| 1   | The rail's Inbox from `/`, then `/inbox` directly | Lands on `/inbox`. Groups: Notifications 3, Asked: reply 2, Review needs your decision 1, Asked: submit for merge 9, Unblock a merge 1, Approve on GitHub 1, Look at a stuck task 3, Approve a proposal 2. They sum to 22, the rail's count.                                                     | PASS             |
| 2   | Click proposal #1's title, then Back              | `/tasks/1`; Back returns to `/inbox`.                                                                                                                                                                                                                                                            | PASS             |
| 3   | Stuck group: sub-cases                            | #21 queued >72h (`reconcileDispatch`) offers **Run now**; #22 running >24h15m (`ProcessResult`) offers **Cancel**; #5 on the merge queue >12h15m (`syncEntry`) offers only Open task (its sub-case names no control).                                                                            | PASS             |
| 4   | Run now on #21                                    | `POST …/21/run-now`. The row leaves and the group goes 3→2; #21 is `queued`, with `runNow` set.                                                                                                                                                                                                  | PASS             |
| 5   | Cancel on #22: dismiss, then accept               | Confirm "Cancel 22? The run in flight is stopped and the task is closed; it can be reopened later." Dismissing sends nothing (#22 is still `running`). Accepting sends `POST …/22/close`; the group goes to 1 and #22 is `closed`.                                                               | PASS             |
| 6   | Retry merge on #24 (Unblock a merge)              | `POST …/24/retry-merge`; the group is gone and #24 is `completed`, its row oval reading "Merging".                                                                                                                                                                                               | PASS             |
| 7   | Undefer sub-case (stub 6)                         | Unreachable. #9's `reason` reads `deferred/wake_time`, driven by `reconcileWake`, control Undefer, but `stall` is `null`, because `liveness.go` gives `deferred` `noPatience`. `InboxPage.jsx`'s comment promises "Undefer on a deferral whose wake time has passed". Filed as a follow-up (O1). | FAIL (follow-up) |
| 8   | Approve #1 from its row                           | `POST …/1/approve`; the proposal group goes 2→1; #1 is `queued` and approved.                                                                                                                                                                                                                    | PASS             |
| 9   | Reply on #4: Send while empty, then type and Send | Send is disabled while empty. Then `POST …/4/comments`, and #4 leaves Asked: reply and is `queued`. The rail reads `17` ("14 tasks waiting on you, 3 notifications"). A reload shows the same groups.                                                                                            | PASS             |
| 10  | Keyboard                                          | Tab walks the rail, then Alerts, System, Settings, then each Inbox group toggle and its rows in order.                                                                                                                                                                                           | PASS             |

#### New task

| #   | Step                                                                                      | Outcome                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         | Verdict     |
| --- | ----------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------- |
| 11  | The rail's "Create options"                                                               | Menu: new task / new session / new board. New task opens `/new`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                | PASS        |
| 12  | Validation                                                                                | Create task is disabled with an empty title, a whitespace title, and while Repos is `—`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        | PASS        |
| 13  | Model picker                                                                              | `Deployment default (gemini-2.5-pro)`, `gemini-2.5-pro`, `gemini-2.5-flash`. That is exactly `/api/config`'s single agent backend `gemini-pro`, models `gemini-2.5-pro` and `gemini-2.5-flash`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 | PASS        |
| 14  | A refused field inside Advanced options                                                   | Advanced closed, vCPUs `-3`, Create task: no POST is sent, and Advanced options opens (`aria-expanded` false→true).                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             | PASS        |
| 15  | Cancel: ✕, reopen, Discard draft                                                          | The title is kept as a draft; Discard draft empties it.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         | PASS        |
| 16  | `/new?field.priority=high&field.requested-by=Link%20Sender` → queued task with everything | Prefilled Priority high and Requested by "Link Sender". Added a description, an attachment, acme/widgets, base `release/1.0` and auto-merge. Unticked Self debug; GCP key and Gemini key, both "not ready -- needs gcp-key-minter", stayed ticked. Added Grain API preset ci-read from Add grant (the menu lists task-admin, ci-read, dispatch-control, deployment-ops _dangerous_, …). Advanced: network "egress", 4 vCPUs, 8192 MiB, model gemini-2.5-flash, Task priority 7. Filed #29 `queued`. Read back: `api:ci-read, gcp-key, gemini-key, grain-ci`, model, `networkAccess: open`, cpus, memory, priority 7, custom fields and `attach.txt` all persisted. The row shows "p7", "api preset: ci-read" and `release/1.0`. | PASS        |
| 17  | Proposed path, no repo, empty Task priority                                               | Unticking "queue immediately" files #30 `proposed`, `noRepo`, priority 5 (an empty box falls back to the default). Base branch still shows with No repo (O5 of 10-29, unchanged).                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               | PASS        |
| 18  | `/new?field.priority=bogus&field.nosuch=1`                                                | The 10-30 notice names both dropped values.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     | PASS        |
| 19  | Task priority `-4`, `0`, `2.7`                                                            | The browser's own validity check (`min=1 step=1`) blocks the submit; nothing is sent.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           | PASS        |
| 20  | Task priority `1e3`                                                                       | **Bug 2:** the box accepts it as valid (it is 1000), and the task is filed with `priority: 1`. Fixed here.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      | FAIL → PASS |

#### Task list

| #   | Step                                    | Outcome                                                                                                                                                                                                                                                                                                                                         | Verdict |
| --- | --------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 21  | Rail views                              | Tasks `/?f=state:none:deferred\|closed` (25 rows); Proposed 12, 30; Merging 5, 20, 24; Blocked 11; Deferred 9; Closed 22, 10; Merged (stub 2) `/?state=merged&sort=merged:desc` 19, and the same from a deep link.                                                                                                                              | PASS    |
| 22  | Ovals and chips                         | #11 `Blocked on: #8 (running)`; #5 and #24 "Merging", #5 also stalled with its note as the title; #3 "Running", chip title "running/live_run Driven by ProcessResult … "Cancel" ends this wait"; #12 (stub 1) "held for approval by 3", hover "task 3 could have queued this on its own permission…". The chip is gone after Approve (step 31). | PASS    |
| 23  | Search `retry`, sort by title, reload   | `?q=retry` → 2, 14–18; `sort=title:asc` reorders them; a reload keeps the URL, the rows and the search text.                                                                                                                                                                                                                                    | PASS    |
| 24  | Open #2, Back, Forward                  | Back returns to the narrowed URL with the same rows; Forward returns to `/tasks/2`.                                                                                                                                                                                                                                                             | PASS    |
| 25  | Clear, then deep link `/?state=running` | Clear drops the search back to the default state filter and keeps the sort. The deep link lists 3, 8.                                                                                                                                                                                                                                           | PASS    |

#### Task detail

| #   | Step                                                                                                                                  | Outcome                                                                                                                                                                                                                                                                                                                                                          | Verdict |
| --- | ------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 26  | Pause → reload → Resume → Pause now → Resume on running #3                                                                            | `…/pause`, `…/resume`, `…/pause {"immediate":true}`, `…/resume`. The pane follows without a reload; Resume is still offered after a reload; #3 stays `running` and the backlog order is unchanged.                                                                                                                                                               | PASS    |
| 27  | Pause → Resume on queued #29                                                                                                          | Stays `queued` in the same place.                                                                                                                                                                                                                                                                                                                                | PASS    |
| 28  | Force merge on #8 (attempt #2 running)                                                                                                | A `prompt` explains the override and that the run in flight is stopped. Dismissing sends nothing. Accepting sends `{"reason":"walkthrough: CI is stuck"}`; without a reload the pane says "…force the merge of acme/widgets#104 at 000000000000…", and #8 is `completed`.                                                                                        | PASS    |
| 29  | Attempt capability audit, work tree on #8                                                                                             | "capability audit records · the run made 14 tool call(s)"; Escape stays on `/tasks/8`. Provenance "work from task 13: 0 of 4 done", expanded: generated from 13, then 13, 1, 3, 8 (acme/widgets#104), 11, each with its state and a link.                                                                                                                        | PASS    |
| 30  | Defer (no date) → Undefer → Defer until 2026-11-15 → reload → Approve → Undefer, on #30                                               | `defer {"deferredUntil":""}` → `deferred`; Undefer → `proposed`; the dated defer stores `2026-11-15T00:00:00Z`, and after a reload the pane says "Picks itself back up on 11/15/2026, 12:00:00 AM, back into the proposals…". Approve keeps it `deferred` and approved; Undefer → `queued`.                                                                      | PASS    |
| 31  | Approve held proposal #12                                                                                                             | `queued`; the held chip is gone from its row.                                                                                                                                                                                                                                                                                                                    | PASS    |
| 32  | Dependency #29 → #3, then remove                                                                                                      | Typing "bump" plus Enter sends `depends-on {"id":"3","attach":true}`. Chip "3 bump the go toolchain to 1.24 (open)"; hover "3 Bump the Go toolchain to 1.24 — Running (blocking this task)". The rail's Blocked goes 1→2, the Blocked view lists 29 and 11, and #29's oval reads "Blocked on: #3 (running)". × sends `attach:false`, and Blocked goes back to 1. | PASS    |
| 33  | Merge policy on #23                                                                                                                   | `ci.mode: grain -- from repository setting`; then `ci.mode: both`, `auto_merge: never`, `stall_timeout: 30m`, `ci.grain: full suite`, `ci.github: build`, `ci.github: cla/google`, each from rule `deploy`; then "GitHub CI is narrowed to build, cla/google…".                                                                                                  | PASS    |
| 34  | Grain CI verdict on #6 (stub 5), then Re-run CI                                                                                       | "grain CI: failure. 1 job failed: go test…", with show the run, Re-run CI and Re-run failed jobs. Re-run CI sends `POST /api/ci/runs` at once, and the pane goes back to "grain CI is running…" without a reload.                                                                                                                                                | PASS    |
| 35  | Pending secret on #21 (stub 3)                                                                                                        | A password box "Value for stripe-api-key"; Set secret is disabled while empty; then `PUT …/21/secret`. The timeline says "Set stripe-api-key. Its value is in grain's secret store, not in this conversation…". The value is in neither the DOM (before or after a reload) nor the task JSON, and `grep -rlF` finds it in no file under the demo directory.      | PASS    |
| 36  | Suspended session on #2 (stub 4)                                                                                                      | "Left by demo-run-2-1 on 11/1/2026, 2:00:00 AM (Antigravity). Any further run of this task resumes this conversation…"                                                                                                                                                                                                                                           | PASS    |
| 37  | Parked question #7                                                                                                                    | `.pending-question` "Waiting for your reply", the question, a reply box and Send.                                                                                                                                                                                                                                                                                | PASS    |
| 38  | Full prompt and live transcript on #3                                                                                                 | "prompt for 3 · attempt #1 · 10 kb · ~2,615 tokens · 1% of a 200,000-token window"; Escape stays on `/tasks/3`. "live attempt #1 transcript" inline.                                                                                                                                                                                                             | PASS    |
| 39  | Edit #30: title and description (Save), Repo `—`→acme/widgets, base `release/1.0`, auto-merge off, Edit fields (Requested by); reload | Each sends its own PATCH and is persisted after the reload. Enter followed by blur in Base branch sends the same `{"base":"release/1.0"}` PATCH twice (harmless, O2).                                                                                                                                                                                            | PASS    |
| 40  | Withdraw approval                                                                                                                     | Offered on queued #2 and #29 (the cross-team approval section the brief names does not exist in this tree; 10-30's step 47 still holds).                                                                                                                                                                                                                         | N/A     |
| 41  | Chat pane Pause/Resume, host-script approvals, Internal PR link                                                                       | Not re-run this time for lack of time. They passed on 10-30 (steps 30, 43, 46 there), and nothing under those components has changed since.                                                                                                                                                                                                                      | NOT RUN |

#### Selection, bulk edit, manual mode

| #   | Step                                                                       | Outcome                                                                                                                                                                                                 | Verdict |
| --- | -------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 42  | Queued view, tick #2, #21, #29                                             | "3 selected" with approve / run selected now / submit / defer / undefer / close / reopen. Close confirms "Close 3 task(s)?"; dismissing sends nothing and keeps the selection.                          | PASS    |
| 43  | Bulk edit… → Cancel                                                        | Fields that differ say "(mixed across the selection)". Cancel sends nothing and keeps the selection.                                                                                                    | PASS    |
| 44  | Bulk edit: tick Requested by, type "Bulk walker", Apply to 3 tasks; reload | Three PATCHes with `customFields` only, merged into each task's own (#2 keeps its priority and area, #29 its priority). Base, auto-merge and capability counts are unchanged; the selection is cleared. | PASS    |
| 45  | System → Pipeline → Pause pipeline                                         | Confirm, then `POST /api/pipeline-pause`. Queued 29, 1, 2, 4, 12, 26, 28, 30 carry "waits for Run now"; blocked #11 and #21 (Run now pressed in step 4) do not.                                         | PASS    |
| 46  | Select #29 and #1 → Run selected now; reload after 5s                      | `…/run-now` for each; their markers are gone and stay gone after the reload (the others keep theirs). Resume pipeline confirms, then `DELETE /api/pipeline-pause`, and every marker goes.               | PASS    |

#### Console and network

There were no uncaught page errors, no console errors or warnings, and no
failed requests. Nothing returned 400 or more.

### Bug 2 (second run): New task filed a number box's exponent entry as its leading digit (fixed here)

- **Reproduction:** on any `grain demo`, open `/new`, give it a title and a
  repo, and type `1e3` into Task priority. The box reports itself valid,
  because `1e3` is 1000. Create task sends `"priority":1`, and the task is
  filed at priority 1, the bottom of the backlog, instead of the top. Advanced
  options' Memory (MiB) `8e3` sends `8`, and so do vCPUs and disk.
- **Cause:** `NewTaskOverlay.jsx` and `EditTaskForm.jsx` read those boxes with
  `parseInt(value, 10)`, which stops at the `e`.
- **Fix:** `ui/src/wholeNumber.js` reads the whole string as the input does
  (`Number`), and answers NaN for anything that is not a whole number. Every
  caller already treats NaN as "not set", so empty and invalid entries behave
  as before.
- **Coverage:** `ui/src/wholeNumber.test.js`; `NewTaskOverlay.test.jsx` ›
  "files a Task priority typed in exponent notation as the number it is";
  `ui/e2e/new-task-number-boxes.spec.js`, which files `1e3` and `8e3` through
  the real demo, checks the request body and reads back priority 1000, then
  closes the task. That spec and `new-task-link-values.spec.js` pass on the
  fixed build. The four affected vitest files (47 tests) pass. Nothing about
  the layout changed, so no screenshots were taken.

### Observations

- **O1 (follow-up filed):** the Inbox's stuck group can never offer Undefer.
  A deferral whose wake time `reconcileWake` has missed (stub 6: four days
  past) has `reason.class=driven` but no stall note, because `deferred` has
  `noPatience`. The Inbox and `livenessreasons.go` both describe this
  sub-case, so the rule and the UI disagree.
- **O2:** Base branch on the task pane sends its PATCH on Enter and again on
  blur. The second one is identical and harmless.
- **O5 (10-29)**, unchanged: New task still shows Base branch with No repo
  selected.
- Notes 10-30 recorded and still true: Pause now asks nothing (O4 of 10-29).
  While a task pane is open the rail is `aria-hidden` (the pane is modal), so
  rail counts read from a task page have to be read through the DOM.
