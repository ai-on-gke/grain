# Scheduled Upgrades on PostgreSQL & `/next/` Upgrade Cancel — 2026-11-18

This report covers [`docs/test-plans/2026-11-18.md`](../test-plans/2026-11-18.md)
§4, "Upgrades: cancel, cleanup and scheduled upgrades on PostgreSQL"
(PR #1799 and PR #1464), walked by task 7055 on a real image-mode
daemon over PostgreSQL and then SQLite. It also re-checks findings F5–F7
of [`2026-11-13-upgrade-cancel.md`](2026-11-13-upgrade-cancel.md). No fix
for them was on `main` (`git log origin/main -- pkg/upgrade` ends at
`a1a64f573`, the F1 doc comment).

## Summary

| §4 "not proved" item | Verdict |
| --- | --- |
| 1. Scheduled upgrade on PostgreSQL, two runs and an interactive session | **Pass on PostgreSQL and SQLite.** `busyRuns: 2` (the session is not counted), the queue is held (a new task stays `queued`), the count drops to 1, then the upgrade starts one tick after the second run finishes while the session still runs. PR #1799 fixes F4. |
| 2. Cancel from `/next/` at each phase | **Pass on both stores** for scheduled, grace window, pull and health check (plus the signature step). Each cancel sent one `DELETE` to the right route, reported `cancelled` with the reason, left the ref file alone, left `docker ps -a --filter label=grain.upgrade-check` empty and released the queue. The page also showed a finished upgrade as "Upgrade in progress: ok" (**F9, fixed**). |
| 3. F5–F7 | **F7 reproduced and fixed** (`96beb11eb`). **F5 and F6 reproduced on `HEAD`**, still open. They are proposed as one follow-up. |
| 4. Counting failure | **The queue stays held and the upgrade never starts**, but the status showed "waiting on 0 active run(s)" with no reason (**F8, fixed** in `b1b5fff93`; the classic pane is a follow-up). |
| 5. Viewer | **Pass, with a note.** A viewer gets 403 from `DELETE /api/upgrade/current` and `/scheduled`. Cancel upgrade is shown but **disabled**, not hidden as the plan worded it. That matches the 2026-11-13 walk and the page's other gated controls. |

Fix commits on this branch: `96beb11eb` (F7), `b1b5fff93` (F8, F9),
`87a28ee3b` (the banner layout the F8 message exposed, the e2e spec,
the design book rule and the harness).

## Environment & build tested

- **Commit walked:** `b0b27e281` (`main`, the merge of PR #1836). The
  fixes were then rebuilt on top of it.
- **Binaries:** `make frontend && go build` (Go 1.26.10).
  - `grain-head`: `HEAD` unmodified, used for the F7 reproduction and
    the "before" screenshots.
  - `grain`: with `96beb11eb`.
  - `grain-ui`: with all three commits.
- **Docker:** Engine 29.10.0 (containerd image store). `registry:2` runs
  on `localhost:5000`. [`slowreg.py`](2026-11-18-upgrade-scheduled-cancel/slowreg.py)
  is a 256 KiB/s TCP proxy in front of it on `localhost:5001`. Both
  are in `/etc/docker/daemon.json`'s `insecure-registries`, and docker
  was restarted to read them: this sandbox's docker refuses plain HTTP
  even to `localhost`.
- **Images:** [`2026-11-13-upgrade-cancel/images.sh`](2026-11-13-upgrade-cancel/images.sh)
  pushes `:v1`, `:v2` and `:slow`. Its `ENTRYPOINT` sleeps 60 s, so the
  signature step and the health check each run a container for a
  minute. For a pull a cancel can land in,
  [`pushblob.py`](2026-11-18-upgrade-scheduled-cancel/pushblob.py)
  uploads `:pull1`/`:pull2` (one 40 MiB random layer) straight to the
  registry API. A `docker push`ed layer stays in containerd's content
  store even after `docker image rm`, so pulling it again took 0.6 s
  however slow the proxy was.
- **Stores:**
  - **PostgreSQL:** Debian `postgresql` 15.20, database `grain_walk`
    owned by role `grain` on `127.0.0.1:5432`. `ss -ltnp` showed
    nothing on `5432` before the cluster was started (F3's leftover
    server is gone).
  - **SQLite:** 3.40.1 (`sqlite3` CLI), the daemon's default store.
- **Daemons:** [`env.sh`](2026-11-18-upgrade-scheduled-cancel/env.sh)
  sources `../2026-11-13-grain-health-exit-codes/env.sh`. That gives a
  real `grain daemon -host-sandboxes` with real dispatch, the fake
  `agy` CLI and the GitHub sim. On top of it, `env.sh` adds
  `-upgrade-image localhost:5001/acme/grain -upgrade-image-ref-file
  $W/image.env -upgrade-allowed-branches '**' -upgrade-grace-window 30s
  -upgrade-restart-cmd=$W/restart.sh`.
  - `restart.sh` only appends the time to `$W/restarted`. "Cut-over"
    is therefore observable, and the daemon is restarted by hand where
    an item needs it.
  - PostgreSQL runs on `127.0.0.1:4280`, SQLite on `:4290`.
  - The harness passes the retired `-agent-framework`, so every upgrade
    correctly warns that the new image "does not define
    -agent-framework" and will ignore it.
- **Busy runs are real runs:** `busy_task N` files a task whose fake
  agent makes one MCP `run_command` that waits for `$W/release-N`.
  `busy_task N true` files it `interactive`.
- **Browser:** Playwright 1.62.1, Chromium 151.0.7922.34,
  [`shot.mjs`](2026-11-18-upgrade-scheduled-cancel/shot.mjs) at 1440×900
  and 390×844, light (and dark for the before/after). It logs every
  non-GET `/api/upgrade*` request, its status, and the banners and
  buttons with their bounding boxes. It clicks a button by `CLICK=`.
- **Viewer:** `tests/iap/iapsim.go` as the identity proxy on
  `:4286`. The PostgreSQL daemon is restarted with `-ui-iap-audience`,
  `-ui-iap-issuer`, `-ui-iap-keys-url` and
  `-allow-unisolated-host-sandboxes`. Then `grain access allow --role
  grain/viewer user:viewer@example.com`.

## Item 1: a scheduled upgrade behind two runs and a session

**PostgreSQL:**
- Tasks 2 and 3 (busy) and 4 (interactive) were `running`.
- `grain upgrade start -after-tasks-finish v2` printed `scheduled
  upgrade to v2 (waiting on 2 active run(s), 0 CI run(s))`. The daemon
  logged `holding the queue until this deployment is idle, then
  upgrading to v2 (2 runs still to finish)`.
- `GET /api/upgrade` had `scheduled.busyRuns: 2` (and `inFlightRuns: 3`,
  which counts the session).
- Task 5, filed and approved while the schedule waited, stayed `queued`.
- Run 1 was released: `busyRuns: 1` and still scheduled.
- Run 2 was released at 05:47:47. At 05:47:48 the daemon logged `this
  deployment is idle -- starting the upgrade to v2`, with the
  interactive task 4 still `running`.
- The upgrade (8 steps, no grace window) ended `phase=ok`, the ref file
  read `:v2@sha256:cbe900f9…`, and `restart.sh` ran at 05:47:54.
- `/next/settings/upgrade` showed "Upgrade scheduled (waiting for running
  tasks): v2" with Cancel scheduled upgrade enabled, at both widths.

**SQLite:** the same steps on a fresh daemon gave `waiting on 2 active
run(s)`, then 1, then `running` 2 s after run 2 was released. It ended
`phase=ok` with task 4 (interactive) still running and task 5 held.
**Pass on both.**

## Item 2: Cancel from `/next/` at each phase

Each row is one Playwright click on `/next/settings/upgrade` at 1440 px.
After each one, on both stores:
- `GET /api/upgrade` read `phase=cancelled`,
  `cancelReason=operator_cancelled` and
  `detail=cancelled by alice@example.com: operator_cancelled`.
- The ref file was unchanged.
- `docker ps -a --filter label=grain.upgrade-check` was empty.
- The daemon logged `… the pipeline is picking work up again`.
- The banner disappeared only after the `DELETE` answered 200, and the
  toast read "Cancelled active upgrade" (or "Cancelled scheduled
  upgrade").

| Phase | Request sent | What the log and host showed |
| --- | --- | --- |
| Scheduled (a busy run in flight) | `DELETE /api/upgrade/scheduled` | `no upgrade is scheduled any more`. The task held behind it went `queued` → `running`. |
| Grace window (`step 1 of 9`) | `DELETE /api/upgrade/current?reason=operator_cancelled` | `step 1/9 grace window: cancelled after 5s`. No image pulled. |
| Pull (`step 2 of 9: pull image`, `:pull1`/`:pull2` through the proxy) | same | `step 2/9 pull image: cancelled after 10s`. `docker pull` was killed, so cleanup found `No such image` and nothing was left. |
| Signature (`:slow`, a container up) | same | `removed check container(s) 261a2d0982f3`, then `removed …:slow`. |
| Health check (`step 4 of 9`, `:slow`) | same | `removed check container(s) 7b7470002e6e` (PostgreSQL), `0de922c289b5` (SQLite). |

The Cancel buttons stayed inside the viewport at 390 px (x=49, 199×36
and 128×36), with no horizontal overflow. **Pass**, apart from what the
page showed between upgrades (F9 below).

## Item 3: F5–F7 on `HEAD`

- **F7, reproduced and fixed.** On the PostgreSQL daemon with
  `localhost:5001/acme/grain:slow` pulled beforehand:
  - **`grain-head`:** cancelling `:slow` in its health check logged
    `cancel cleanup: removed localhost:5001/acme/grain:slow`, and the tag
    was gone from `docker images`.
  - **Fixed `grain`:** the same steps removed the check container and
    left the tag in place.

  **Fix (`96beb11eb`, tightened in security review):** `stageImage` asks
  `docker image inspect` for the image ID the target tag resolves to
  before it pulls, and cancel cleanup leaves the tag only when it still
  resolves to that same image ID (so a mutable branch or channel tag
  that `docker pull` moved onto a newly fetched image is still cleaned
  up on cancel, and a cancel during the pre-pull inspect never deletes a
  tag when `docker pull` has not started). The question is asked only
  when docker both pulls and removes images, so a custom `PullCmd` or
  `RemoveCmd` keeps the old behaviour. Regression tests:
  `TestCancelKeepsATargetTagThatWasAlreadyHere`,
  `TestCancelRemovesATargetTagThatMovedDuringPull`, and
  `TestCancelDuringPrePullInspectKeepsPreExistingTargetTag`
  (`pkg/upgrade`). With the tag absent or moved onto a new image,
  cleanup removes what the upgrade pulled.
- **F6, still present.** After the scheduled upgrade from a fresh ref file
  (`:v1`, a tag), `grain upgrade status` printed `replaced:
  localhost:5000/acme/grain:v1 (run "grain upgrade rollback" to go
  back)`. `grain upgrade rollback` then exited 2 with `the last upgrade
  recorded no previous build to roll back to: the previous image
  localhost:5000/acme/grain:v1 is not pinned by digest`. The classic pane
  offers "Roll back to …" from the same status. `/next/` has no rollback
  control.
- **F5, still present.** Running `:v2@sha256:cbe900f9…` with
  `previousImage: :v1`, I cancelled `-image :slow` in its grace window.
  `previousImage` then read `localhost:5000/acme/grain:v2@sha256:cbe900f9…`,
  the image that was running.

F5 and F6 need the status file to keep the last good rollback target
apart from the latest record, and a server-side "rollback available"
the CLI and the classic pane can read. That is a model change, so it is
proposed as a follow-up rather than made here.

## Item 4: the count fails while a scheduled upgrade waits

On PostgreSQL I put a busy run in flight, scheduled `slow`, and ran
`REVOKE SELECT ON task_run FROM grain` (as `postgres`). Then I released
the run:
- The upgrade **never started**. The `pipeline_pause` row stayed
  `automation / upgrade-schedule`, "waiting for this deployment to
  finish its work before upgrading to v2". The reconciler logged
  `scheduled-upgrade: orchestrator: counting busy runs: ERROR:
  permission denied for table task_run (SQLSTATE 42501)` every cycle.
  Dispatch failed too (`counting live runs: … permission denied`), so
  no task could start either way.
- **But nothing visible said why (F8):** `GET /api/upgrade` returned
  `scheduled.busyRuns: 0`. `grain upgrade status` printed `scheduled:
  v2 (waiting on 0 active run(s), 0 CI run(s))`. `/next/` showed an
  ordinary "Upgrade scheduled" banner, and the classic pane said
  "nothing is running, so the upgrade starts within seconds".
- **Fix (`b1b5fff93`):**
  - The scheduled object carries `countError`.
  - `grain upgrade status` prints `scheduled: v2 (the runs in flight
    could not be counted, so the queue stays held and the upgrade waits
    until they can be; the daemon log has the error)`.
  - `/next/` shows the same sentence in the scheduled banner. Placing it
    there needed the banner layout fix in `87a28ee3b`: the long message
    had pushed Cancel scheduled upgrade from x=1167 to x=425 at 1440 px.
  - Tests: `TestAScheduledUpgradeSaysWhenItsRunsCannotBeCounted`
    (`pkg/ui`), `TestScheduledWaitingOnSaysWhenTheRunsCannotBeCounted`
    (`pkg/cli`), a settings-page spec and
    `ui-ng/e2e/next-upgrade-status.spec.js`.
  - The classic pane's sentence is a follow-up.
- After `GRANT SELECT` was restored, the next tick counted 0 and the
  upgrade started.

## Item 5: a viewer

Signed in through the IAP sim as `viewer@example.com` (`grain/viewer`)
while an upgrade was in its signature step:
- `/next/settings/upgrade` shows "Upgrade in progress: running" with
  **Cancel upgrade visible and disabled**, at both widths. The page's
  read-only banner says the role lacks `settings:write`.
- The page sent no `DELETE`.
- `curl -X DELETE …/api/upgrade/current?reason=viewer` answered 403:
  `viewer@example.com (role grain/viewer) holds no scope
  deployment:upgrade, which DELETE /api/upgrade/current needs`.
- `DELETE /api/upgrade/scheduled` answered the same 403.
- The upgrade carried on.

**Pass**, except that the button is disabled, not absent. The plan's
"sees no Cancel" asks for more than the page's own access rule
(controls disabled with the denied reason), so this is recorded rather
than changed.

## F9: a finished upgrade read "Upgrade in progress: ok" (fixed, `b1b5fff93`)

**Reproduction:** on any image-mode daemon, run an upgrade to the end
(`phase=ok`) and open `/next/settings/upgrade`. It shows "Upgrade in
progress: ok" beside a disabled Cancel upgrade, and keeps showing it.
`pkg/upgrade` keeps `ok` in its status file across the restart, so
this lasts until the next upgrade.

**Cause:** `applyUpgradeStatus`'s terminal-phase set lists `done`,
`succeeded`, `complete` and others, but not `ok`, the only success phase
`pkg/upgrade` writes.

**Fix:** `ok` is terminal. A settings-page spec and the e2e spec cover
it.

## Screenshots

Rendered on the PostgreSQL daemon in one state: last upgrade `phase=ok`,
plus a schedule whose count fails. The before shots use `grain-head`
and the after shots `grain-ui`, each at 1440 and 390 px, light and
dark.
- **Before:** two banners, "Upgrade in progress: ok" and an ordinary
  "Upgrade scheduled".
- **After:** one scheduled banner carrying the count error, with
  Cancel scheduled upgrade at x=1167 (1440 px) and x=49 (390 px). No
  horizontal overflow at either width.

`ui-ng/e2e/next-full-system.spec.js` (desktop and phone, light and
dark, and the contrast check) and `next-upgrade-status.spec.js` pass:
8/8. The settings-page unit spec passes 31/31.

The PNGs were not opened in this sandbox, whose file reader returns
their bytes. Each claim above comes from the DOM text and bounding
boxes `shot.mjs` printed. The contact sheets are attached to the task's
closing note for a reviewer to look at.

## Classic Settings → Upgrade (comparison only)

In the item 4 state, the classic pane at 1440 px showed:
- `Replaced localhost:5001/acme/grain:v2@sha256:…` with a "Roll back
  to localhost:5001/acme/grain:v2" button (F5's record).
- "Waiting to upgrade to v2 … nothing is running, so the upgrade starts
  within seconds", while the count was failing (F8's classic half).
- A cancel control.

It does not show the "in progress: ok" banner that F9 fixed on `/next/`.

## Not walked

- Kubernetes mode, and source mode.
- A real restart onto the new image. `restart.sh` only records the
  time, so the daemon was restarted by hand. The host-sandbox runs in
  flight across those restarts were re-queued, not re-adopted.
- Viewer pass on SQLite. The access rules do not depend on the store.
