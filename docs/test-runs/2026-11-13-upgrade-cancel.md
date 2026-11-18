# Upgrade Cancel & Rollback Walkthrough — 2026-11-13

This report covers [`docs/test-plans/2026-11-13.md`](../test-plans/2026-11-13.md)
§10, "Upgrades: cancel and roll back". Task 4725 walked it first, on a
real image-mode daemon against a local `registry:2`. That walk found F1,
F2 and F3 and fixed them on branch
`grain/098073e4/task-5362-walkthrough-upgrade-cancel` (PR #1464), but it
never committed this report. Task 6631 then wrote the report and re-ran
every step below on that branch, so each verdict here is one this run
saw, not one copied from the earlier walk. The re-run found four more
defects (F4–F7). They are filed as follow-ups on `main` rather than
fixed on this branch.

## Summary

| §10 item | Verdict |
| --- | --- |
| 1. Image-mode cancel, inside the grace window and during staging | **Pass with the F1 fix**, on SQLite and PostgreSQL. Before `fc931e85a` a cancel during the health check left the check's container running, and that container kept the pulled image on the host (F1). |
| 2. Cancel past the point of no return | **Pass.** The daemon refuses with 409 and a clear message. |
| 3. `-after-tasks-finish` and `grain upgrade cancel -scheduled` | **Pass on SQLite. Fails on PostgreSQL (F4):** the busy-run count errors, so a scheduled upgrade never starts and the queue is never held. |
| 4. Rollback (CLI and API), image mode | **Partly passes.** Rollback works when the previous image is pinned by digest. Any cancelled upgrade, a cancelled rollback included, replaces the rollback target with the running image (F5). The status line offers a rollback the daemon then refuses (F6). Source-mode rollback by object id was **not walked**. |
| 5. `/next/` and classic UI | **F2 fixed** (`2d9393cfb`): Cancel on `/next/` called the wrong route. Task 4725 walked it with Playwright. This run re-ran only the unit spec. |
| 6. KMS unwrap once | **Not walked** in this report (`pkg/secrets` unit tests only). |

Also found: cancel cleanup removes the target tag even when it was on the host before the upgrade pulled it (F7). PostgreSQL-backed `pkg/cli` tests left their embedded server running (F3, fixed in `6a914ec11`).

## Environment & build tested

- **Repository:** `ai-on-gke/grain`, branch
  `grain/098073e4/task-5362-walkthrough-upgrade-cancel` at `6a914ec11`
  (the fixes for F1–F3 on `main` at the time of its last merge), plus the
  report-only commits of task 6631.
- **Pre-fix binary:** built from `fc931e85a^`. It reproduced F1 and F3.
- **Build:** `go build -o /home/debian/bin/grain ./cmd/grain` (Go 1.26.10).
  Every daemon ran this binary on the host. The images it upgraded
  between carry the same binary.
- **Docker:** Engine 29.9.3 (containerd image store), with `registry:2` on
  `localhost:5000`.
- **Images:** [`images.sh`](2026-11-13-upgrade-cancel/images.sh) pushes
  three tags of `localhost:5000/acme/grain`, each with `grain` as its
  `ENTRYPOINT`:
  - `:v1`: the image the ref file starts on.
  - `:v2`: an ordinary target.
  - `:slow`: its entrypoint sleeps 60s before `exec grain "$@"`, so the
    upgrade's version probe and health check each take a minute and a
    cancel can land while the check's container is up.

  The script deletes `:v2` and `:slow` locally after pushing them, so an
  upgrade really pulls them.
- **Daemons:** one image-mode daemon per store, for example:

  ```sh
  grain daemon -data-dir $D/data -sandbox-port-base=0 -disable-native-sandboxes \
    -ui-addr 127.0.0.1:4281 -upgrade-image localhost:5000/acme/grain \
    -upgrade-image-ref-file $D/image.env -upgrade-allowed-branches '**' \
    -upgrade-grace-window 30s -upgrade-restart-cmd=$D/restart.sh
  ```

  `$D/image.env` starts as `GRAIN_IMAGE=localhost:5000/acme/grain:v1`.
  `restart.sh` only appends the time to `$D/restarted`. "Cut-over" is
  therefore observable, and the walk restarts the daemon by hand where
  an item needs the new image "running".
  - **SQLite:** `/tmp/d1`, the default store.
  - **PostgreSQL:** `/tmp/d2`, the same flags plus
    `-postgres-url postgres://walk:walk@127.0.0.1:5432/grainwalk?sslmode=disable`,
    on the Debian `postgresql` 15 cluster.
- **Driving the daemon:** `GRAIN_SERVER=http://127.0.0.1:4281
  GRAIN_DATA_DIR=$D/data grain upgrade …`. The API calls used `curl`
  with the data dir's `local-api-token`.
  [`walk.sh`](2026-11-13-upgrade-cancel/walk.sh) runs items 1–3 in
  order. The PostgreSQL pass below is its output.
- **A run in flight:** there is no sandbox backend here, so a fake run
  stands in for one. It is one `task_run` row with no `finished_at`, the
  exact row `Store.BusyRunCount` counts. Finishing the run means setting
  `finished_at`.

## Item 1: image-mode cancel

**Inside the grace window** (`grain upgrade start -image …:v2`, then
`grain upgrade cancel -reason "test: inside grace window"` 3s later).
The status read `step 1 of 9: grace window`,
`cancellable: yes (27s grace window remaining)`. The cancel answered
`cancelled upgrade to v2 (cancelled by access-code:debian@…: test: inside grace window)`.
Afterwards:
- `phase=cancelled`, and the detail and `cancelReason` carry the reason.
- The ref file still names `:v1`, and the restart command never ran.
- No `:v2` image is on the host and no container is left.

Same result on SQLite and PostgreSQL.

**During staging** (`grain upgrade start -image …:slow`, then cancel
once the status reads `step 4 of 9: health check`). While the check ran,
`docker ps` showed its container, labelled
`grain.upgrade-check=5b818baaca6f`. The daemon log after the cancel:

```
upgrade: slow: cancel cleanup: removed check container(s) ed93dc41e046
upgrade: slow: cancel cleanup: removed localhost:5000/acme/grain:slow
upgrade: slow: step 4/9 health check: cancelled after 2s (cancelled by …: test: during staging)
```

Afterwards:
- The ref file still names `:v1`.
- No `:slow` image and no labelled container remain.
- The orchestrator logged `the upgrade to slow is over -- the pipeline is picking work up again`.

Same result on PostgreSQL.

**No lost runs:** the daemon never restarted during a cancel, and the
stand-in run row was untouched. The plan's "fake-agent run in flight
keeps running or is re-adopted" was **not walked** with a real agent,
because this sandbox has no sandbox backend.

## Item 2: past the point of no return

I let `grain upgrade start -image …:v2` run to the end:
- `phase=ok` and `restarting`.
- The restart command ran (`$D/restarted` got a line).
- The ref file read `GRAIN_IMAGE=localhost:5000/acme/grain:v2@sha256:c274c024…`.

The status then said `cancellable: no (restarting onto v2 — too late to
cancel; upgrade back to localhost:5000/acme/grain:v1 afterwards)`.
`grain upgrade cancel -reason "test: too late"` exited 2 with
`restarting onto v2 — too late to cancel; upgrade back to
localhost:5000/acme/grain:v1 afterwards (409 Conflict)`. **Pass.**

## Item 3: `-after-tasks-finish`

- `grain upgrade start -after-tasks-finish -image …:v2` is refused with
  `an upgrade onto an explicit image cannot be scheduled for after tasks
  finish: start it now, or schedule the branch alone`. That is the
  documented rule, so the walk schedules the branch (`v2`).
- **SQLite, run in flight:** the CLI answered
  `scheduled upgrade to v2 (waiting on 1 active run(s), 0 CI run(s))`.
  The orchestrator logged `holding the queue until this deployment is
  idle, then upgrading to v2 (1 run still to finish)`.
- **Cancel while waiting:** `grain upgrade cancel -scheduled -reason …`
  printed `cancelled scheduled upgrade`. The next cycle logged `no
  upgrade is scheduled any more -- the pipeline is picking work up
  again`, and no upgrade ran. **Pass.**
- **Run finishes:** I scheduled again and stamped the run finished at
  07:57:27. The upgrade was running by 07:57:37, one reconcile tick
  later. It has no grace window (8 steps, not 9). A cancel during its
  health check cleaned up as in item 1. **Pass on SQLite.**
- **PostgreSQL: fails (F4).** See below.

## Item 4: rollback

- The ref file of a fresh deployment names a tag (`:v1`), not a digest.
  After the first upgrade, `grain upgrade status` printed `replaced:
  localhost:5000/acme/grain:v1 (run "grain upgrade rollback" to go
  back)`. `grain upgrade rollback` then refused: `the last upgrade
  recorded no previous build to roll back to: the previous image
  localhost:5000/acme/grain:v1 is not pinned by digest` (F6).
- From a digest-pinned previous image it works. Upgrade `:v2@sha256…` →
  `:v1`, then restart. `previousImage` was `:v2@sha256:c274c024…`.
  `grain upgrade rollback` printed `started rollback to
  localhost:5000/acme/grain:v2`.
- **Cancelling that rollback** in its grace window left the ref file on
  `:v1@sha256…` and kept both images. The `:v2` target was already on
  the host, and cleanup keeps the running and previous images. But the
  cancelled rollback then became the "last upgrade". `previousImage`
  now named the image already running (`:v1@…`). So the next rollback,
  through the API (`POST /api/upgrade {"rollback":true}`), "rolled back"
  to the running image and ended `the version on v1 has not changed`
  (F5). An ordinary cancelled upgrade does the same thing.
- `grain state check` was not run. It checks a state repository
  directory, and nothing in this walk changed one.

## Item 5: UI

The `/next/` Upgrade card's Cancel button sent
`DELETE /api/upgrade/scheduled`. That route only forgets an upgrade
still waiting for tasks, so on a real image-mode daemon the card said
"Cancelled active upgrade" while the upgrade carried on (F2). Task 4725
walked the fix with Playwright on `/next/settings/upgrade` at 1440×900
and 390×844, light. After the fix:
- The daemon reports `phase=cancelled` with the reason.
- The button stays in the viewport at both widths.
- A viewer's button is disabled, and the route answers a viewer 403.

This run re-ran `npx vitest run src/app/settings/settings-page.component.spec.ts`
(12/12 pass). That spec now asserts `DELETE /api/upgrade/current` and
that the scheduled route is not called. This run did not render the page
again, and the classic pane was not re-walked.

## Findings

### F1: a cancelled health check left its container, and the pulled image (fixed, `fc931e85a`)

**Reproduction:** run the pre-fix daemon (`fc931e85a^`), start
`grain upgrade start -image localhost:5000/acme/grain:slow`, and wait
for `step 4 of 9: health check`. Then run
`grain upgrade cancel -reason "pre-fix: during staging"`. Five seconds
later:
- `docker ps -a` still shows the check's container `Up 6 seconds`.
- `:slow` is still in `docker images`.
- The daemon log reads:

  ```
  cancel cleanup: keeping or could not remove localhost:5000/acme/grain:slow: exit status 1 (Error response from daemon: conflict: unable to delete localhost:5000/acme/grain:slow (must be forced) - container 2b8c33bf22c7 is using its referenced image e8b3db12abf3)
  ```

Killing `docker run` stops the client, not the container.

**Fix:** the default run command labels its container
`grain.upgrade-check=<hash of the ref file>`, and cancel's cleanup
force-removes this deployment's labelled containers before and after it
removes the pulled images. A custom `ImageConfig.RunCmd` gets no label,
so it gets no cleanup. Its doc comment now says so (review 5415, task
6631). Regression test:
`TestCancelDuringHealthCheckRemovesTheCheckContainer`. Verified on the
fixed daemon, SQLite and PostgreSQL, in item 1.

### F2: `/next/` Cancel called the scheduled route (fixed, `2d9393cfb`)

**Reproduction:** on a real image-mode daemon, start an upgrade and
press "Cancel upgrade" on `/next/settings/upgrade` during the grace
window. The card shows "Cancelled active upgrade" and hides the banner,
but `GET /api/upgrade` still reports `phase=running`, and the pull and
health check go ahead.

**Fix:** the card sends `DELETE /api/upgrade/current` with the reason,
as the classic pane and `grain upgrade cancel` do, and gates the button
on that route.

### F3: `pkg/cli`'s tests left their embedded PostgreSQL running (fixed, `6a914ec11`)

The PostgreSQL-backed tests in `pkg/cli` start postgrestest's shared
server, but `TestMain` never called `postgrestest.Shutdown()`. The
server outlived the test binary, listening on `127.0.0.1:5432` with its
data under `/tmp`. Task 4725's walk tripped over it: a daemon pointed at
`127.0.0.1:5432` reached that leftover server and not the system
cluster.

**Reproduction:** with the system cluster stopped, run
`GRAIN_REQUIRE_POSTGRES=1 go test ./pkg/cli -run
TestRunBackupSnapshotsPostgresStoreFromRecordedConfig -count=1` at
`fc931e85a^`. Afterwards `pgrep -a postgres` shows
`postgres -D /tmp/grain-postgres-test-…/postgres … -p 5432`.

**Fix:** with `TestMain` calling `postgrestest.Shutdown()`, the same run
leaves no postgres process.

### F4: `-after-tasks-finish` never starts on PostgreSQL (open, follow-up filed)

**Reproduction:** on the PostgreSQL daemon, run
`grain upgrade start -after-tasks-finish v2`. The status says
`waiting on 0 active run(s)` whether or not a run is in flight. The
upgrade never starts: 2 minutes after the run finished, it was still
`scheduled`. Every reconcile cycle and every status poll logs:

```
reconcile cycle: scheduled-upgrade: orchestrator: counting busy runs: ERROR: COALESCE types boolean and integer cannot be matched (SQLSTATE 42804)
```

**Cause:** `Store.BusyRunCount`
(`pkg/model/scheduledupgrade_store.go`) filters on
`COALESCE(t.interactive, 0) = 0`. `task.interactive` is an integer on
SQLite and a boolean on PostgreSQL. `reconcileScheduledUpgrade` returns
that error before it sets the pipeline pause. So on PostgreSQL the
queue is never held, the upgrade never starts, and the API reports 0
busy runs while the reconciler is failing.
`grain upgrade cancel -scheduled` still clears the schedule. The query
is unchanged on `main`, and PostgreSQL is the default store for new
installs.

### F5: a cancelled upgrade replaces the rollback target (open, follow-up filed)

**Reproduction:** first set up a rollback target:
- Upgrade to `:v2` and restart. `previousImage` is
  `:v1@sha256:56499f3f…`.
- `grain upgrade rollback`, then cancel it in the grace window.

`GET /api/upgrade` now has `previousImage` =
`:v2@sha256:c274c024…`, the image that is running. The next
`grain upgrade rollback` "rolls back" to the running image and ends
`the version on v2 has not changed`. A plain cancelled upgrade does the
same: run `:v2` → `:v1` and restart (`previousImage` = `:v2@…`), then
start `-image …:v2` and cancel it. `previousImage` becomes `:v1@…`, the
running image.

**Cause:** the status file keeps one record. `Start` writes
`PreviousImage` (the image running when this upgrade began) into it, and
`StartRollback` reads it whatever the record's phase. So any cancelled
or failed upgrade loses the target of the last successful one.

### F6: status offers a rollback that is refused (open, folded into F5's follow-up)

**Reproduction:** start from a ref file naming a tag (a fresh
deployment's `:v1`) and upgrade once. `grain upgrade status` prints
`replaced: localhost:5000/acme/grain:v1 (run "grain upgrade rollback"
to go back)`, but `grain upgrade rollback` refuses because that image
`is not pinned by digest`. The hint should only appear when
`StartRollback` would accept the target.

### F7: cancel removes a target image that was already on the host (open, folded into F5's follow-up)

**Reproduction:** with `:v2` already pulled, an earlier rollback's
target, and `:v1` running, schedule `v2` and cancel it during its health
check. The log reads
`cancel cleanup: removed localhost:5000/acme/grain:v2`. The plan says a
cancel removes *newly pulled* images. `cleanupOnCancel` keeps only the
running and previous refs, and removes the target tag whether or not
this upgrade pulled it. Low impact, since the next upgrade pulls it
again. The fix would record whether the tag was present before the pull.

## Not walked

- **Source mode** (cancel, and rollback by object id).
- **Kubernetes mode** (check pods).
- **A real agent run** re-adopted after a cancel.
- **Item 6** (KMS unwrap once).
- **The classic UI** for item 5.
