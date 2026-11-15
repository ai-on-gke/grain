# Walkthrough: `resume_later` CI waits released by repairs, the backstop and closing

**Plan:** [`docs/test-plans/2026-11-15.md`](../test-plans/2026-11-15.md) §2 (PR #1411, [Task #5118](/tasks/5118); PR #1507).
**Task:** [Task #5914](/tasks/5914), filed by [Task #5885](/tasks/5885).
**Date:** 2026-11-15, 12:10–13:20 UTC.
**Harness:** [`2026-11-15-resume-later-release/`](2026-11-15-resume-later-release/) — `env.sh` sources the
[2026-11-13 health harness](2026-11-13-grain-health-exit-codes/) (real `grain daemon -sandbox-port-base=0`,
`-host-sandboxes`, the fake `agy` CLI calling the sandbox MCP), and `ghsim.go` is that harness's GitHub sim
plus four control routes (`/_sim/review-comment`, `/_sim/conversation-resolution`, `/_sim/mergeable`,
`/_sim/pulls`). `run_walkthrough.sh [scenario...]` prints one `RESULT` line per scenario and leaves each
task's JSON, conversation and transitions under `$WORK/captures/`. Only external boundaries are stubbed:
GitHub (the sim) and the agent CLI.

## Summary

| Item | SQLite | PostgreSQL | Notes |
|---|---|---|---|
| 1(c) new unresolved review thread | **FAIL → fixed** | **FAIL → fixed** | Bug 1: never reached a task resting on `resume_later` |
| 1(e) review-questions decision | **FAIL → fixed** | **FAIL → fixed** | Same bug, same fix |
| 1(c)/(e) control: a person's Defer survives | PASS | PASS | |
| 1(a) merge-queue repair | partly walked | — | Queue sends repair 1; repair run rests; queue waits for it (see below) |
| 1(b) overlap re-check repair, 1(d) GitHub refusal | not walked live | not walked live | Unit-covered; see "Not walked" |
| 2 backstop: verdict never arrives | PASS | PASS | Rows back-dated 6h05m |
| 2 backstop: verdict unreadable (GitHub down) | PASS | PASS | |
| 2 backstop: repo CI mode unresolvable | PASS | SKIP | Needs an unreadable `repo_config` row, which only SQLite can store |
| 2 `ci-wake-stalled` raised and cleared | PASS | PASS | Clears on a send-back release, or when the task closes |
| 3 close → leaves `ciWaits` / `grain health`; reopen → hold restored, wakes on CI | PASS | PASS | |
| 3 resting console session wakes on its first reply | PASS | PASS | |
| 4 `/next/tasks` Deferred tab and task page follow close/reopen | PASS | — | 0.3 s, no reload |
| 4 `/next/system` CI waits | **no such view** | — | Finding 2: no UI shows `ciWaits` |

## Bug 1 — review threads and review decisions never reached a task resting on `resume_later`

PR #1411 added `sendBackFromResumeLater` to `sendRunForReviewThreads` (new review threads) and
`settleReviewQuestions` (review-questions decision). On a live daemon, neither path could reach a
task deferred by its own `resume_later(ci: my-branch)`:

- `SyncReviewThreads` and `SyncReviewQuestions` walk `Store.OpenPullRequestLinks`. That query returns
  only tasks that are `completed` or `awaiting_submit`, plus those under a merge-queue repair
  (`merge_queue_repair_at` set, `completed_at` unset).
- A `resume_later` run leaves `CompletedAt` nil, so `model.StateOf` reads such a task as `deferred`.
  It was not in the list at all.
- Even if it had been, both functions then skip a task not in those states.
- `SyncReviewThreads`' own doc comment says "never a running, parked or closed one".

**Reproduction (pre-fix `HEAD` `c7987fe4b`, both stores).** I ran
`run_walkthrough.sh c e` with `GRAIN=/home/debian/bin/grain-base`:

1. The task's run pushes, runs `grain pr open` and calls `resume_later(when: {ci: "my-branch"})`. It rests
   `deferred` with PR #9000 open in the sim.
2. `POST /_sim/review-comment` adds a person's (`bob`) inline thread on the PR.
3. After 30 s the task is still `deferred`, with no run and nothing in its conversation. It stays that way
   until CI reports or the 6h backstop fires.
4. For (e), a `review-questions` link is seeded on the resting task. This is what
   `noteFindingsOnReviewedTask` writes when a review leaves questions; it is seeded with
   `INSERT INTO task_link` because filing a real review here would need a review-template run. The owner
   then replies. After 30 s the task is still `deferred`.

```
RESULT [sqlite]   c: FAIL task 2 (PR #9000) still deferred 30s after a new review thread
RESULT [sqlite]   e: FAIL task 3 still deferred 30s after its owner answered the review's questions
RESULT [postgres] c: FAIL task 2 (PR #9000) still deferred 30s after a new review thread
RESULT [postgres] e: FAIL task 2 still deferred 30s after its owner answered the review's questions
```

**Regression tests, written first and failing before the fix:**

- `pkg/orchestrator/resume_later_owner_sendback_test.go`
  - `TestANewReviewThreadTakesATaskOffItsResumeLater` (`resume_later` and `a person` sub-tests). Before the
    fix: `after a new review thread the task is "deferred", want "queued"`.
  - `TestAnOwnersReviewDecisionTakesATaskOffItsResumeLater`. Before the fix:
    `after its owner's decision the task is "deferred", want "queued"`.
- `pkg/model/resume_later_pr_links_test.go`
  - `TestResumeLaterPullRequestLinksAreTheTasksRestingOnTheirOwnWait` covers the new query. It returns a
    task resting on its own wait, and leaves out a person-deferred task and a closed one.
  - `pkg/model`'s `open()` runs it on PostgreSQL whenever PostgreSQL is available. It passed with
    `GRAIN_REQUIRE_POSTGRES=1`.

**Fix (scoped):**

- `Store.RestsOnResumeLater` is split out of `ReleaseResumeLater`: the same test (a held suspension, or
  `resume_later`'s comment at the deferral's instant), without the release.
- New `Store.ResumeLaterPullRequestLinks` returns the fixes-links of deferred tasks that rest on their own
  `resume_later`.
- `SyncReviewThreads` and `SyncReviewQuestions` read those links beside `OpenPullRequestLinks`, through
  `openAndResumeLaterPullRequestLinks`, de-duplicated. They treat such a task as still waiting on its owner.
- A person's Defer is still passed over.
- `OpenPullRequestLinks` itself is unchanged, because the merge queue, CI polling and holds all read it.

**After the fix:**

```
RESULT [sqlite]   c: PASS task 2 (PR #9000) left deferred after 0.3s, ..., daemon log release lines=1
RESULT [sqlite]   e: PASS task 4 left deferred after 0.6s on its owner's answer (review 5), ..., release lines=1
RESULT [postgres] c: PASS task 2 (PR #9000) left deferred after 30.4s, runs=parked,succeeded, ...
RESULT [postgres] e: PASS task 4 left deferred after 0.3s on its owner's answer (review 5), runs=parked,succeeded, ...
```

The daemon logs
`task N was resting on resume_later; new review threads sends it back to an agent now`. The conversation
gets the review-thread note ("1 new review thread(s) on acme/widgets#…") or, for (e), the decision note
("carries out that decision").

**Latency is the poll schedule's, not this path's.** Review comments are read on
`github.PollSchedule`: every 30 s (`PollBase`) while a PR's comments keep changing, doubling up to
8 min (`PollCap`) while idle. This applies equally to `awaiting_submit` tasks. So a thread on a PR that
has been resting quietly is noticed within 8 min:

- PostgreSQL's (c) took 30.4 s, one backed-off read.
- `backstop_then_sendback` took 57.8 s on PostgreSQL and 3 m 45 s on an earlier SQLite run.

## Finding 2 — no UI shows CI waits (`/next/system` included)

The plan expects `/next/system` to list CI waits. `ciWaits` is served by `GET /api/sandboxes`
(`pkg/ui/sandbox_health.go`) and printed by `grain health`, but neither `ui-ng/` nor the classic `ui/`
reads the field: `grep -rn ciWaits ui ui-ng ui-shared` finds nothing outside `node_modules`. The rendered
`/next/system` (Fleet & Host Operations) has no CI-waits section or warning, before or after a close.
Close and reopen were therefore checked on `/api/sandboxes` and `grain health`, which both update at
once. This is filed as a follow-up rather than fixed here: it is a new `ui-ng/` feature under the design
book's review mandate.

## Item 1 — send-back paths

Common setup (`park_on_ci`):

- `acme/widgets` gets one finished Actions run on `main`. Without it `pkg/ui/wait_task.go` refuses a CI
  wait on a repository with no CI at all.
- The task's only run pushes, runs `grain pr open` and calls `resume_later(ci: my-branch)`.
- A task suspended this way gets no pull request of its own at run end. One whose first run called
  `resume_later` without `grain pr open` stayed PR-less, so `grain pr open` is part of the setup.
- The task's own branch never gets a verdict unless a scenario seeds one.

- **(c) new unresolved review thread:** Bug 1, fixed. On both stores the task now leaves `deferred` and
  runs with the thread in its conversation.
  - **Control:** the task is deferred by a person (`POST /api/tasks/{id}/defer`, 1.2 s after parking so
    it is not at `resume_later`'s instant), then a thread arrives. It stays `deferred` for 15 s with no
    run, on both stores.
- **(e) review-questions decision:** Bug 1, fixed. The owner's answer releases it within 0.3–0.6 s, and
  the link is dropped.
  - **Control:** with a person's Defer it stays `deferred`, on both stores.
- **(a) merge-queue repair:** `DAEMON_ARGS="-max-mergers 1" run_walkthrough.sh a`, SQLite.
  1. An auto-merge task finishes; the sim reports its PR not mergeable.
  2. The queue sends repair 1 ("it has conflicts with `main`, so the merge queue has sent this task back
     to an agent…").
  3. The repair run calls `resume_later(ci: my-branch)` without pushing.
  4. The queue then holds `repair_queued` — "waiting for a worker to pick this task up and repair the
     branch (it has conflicts with `main`; automatic repair 1 of 2)" — and asks for nothing more while the
     repair run's own CI wait stands. No second `askForRepair` came in 4 min, so no release happened.
  - The release `askForRepair` performs needs the queue to ask for a repair of a task already resting.
    That is the case `TestAMergeQueueRepairTakesATaskOffItsResumeLater` drives directly, for both repairs
    and against a person's deferral, but I could not produce it cheaply on a live daemon.
  - The "waiting for a worker" wording is inaccurate while the task is resting on its own CI wait, but
    the task does go back once that wait ends.
- **Not walked live:**
  - **(b) the overlap re-check repair** (`sendRecheckFailureBack`) needs a grain-CI repository with
    `merge_recheck` and two overlapping batches.
  - **(d) GitHub's refusal over unresolved threads** reaches a deferred task only while a merge-queue
    repair is open on it (the same `OpenPullRequestLinks` rule as (a)), with green CI on the head.
  - Both are covered by `TestAMergeQueueRepairTakesATaskOffItsResumeLater` (`sendRecheckFailureBack`) and
    the PR #1411 tests. `ghsim.go` carries `/_sim/conversation-resolution` for a later walk of (d).

## Item 2 — the 6h backstop (rows back-dated)

Waiting six hours was impractical. Each backstop scenario instead back-dates the wait 6h05m with
`sqlite3` / `psql`: the parked run's `started_at`/`finished_at`, `task.deferred_at`,
`task_suspension.created_at`, and `resume_later`'s comment. The daemon then runs with the default
backstop (`-ci-wake-backstop` unset, so 6h).

- **Verdict never arrives** (no run on the task's branch): woken on the next cycle (0.3–0.6 s), with
  `grain: this task waited on CI (`my-branch`) … past the 6h0m0s backstop …` in its conversation.
  `pipeline.ci-wake-stalled:<id>` is raised (warning, `clearsWhen: {task, until: closed}`).
- **Verdict unreadable:** the sim is killed for the whole wait, so every CI read fails (connection
  refused). The wake still reaches the backstop and wakes the task. This is PR #1411's "a CI-verdict read
  error no longer skips the backstop".
- **CI mode unresolvable:** the task is moved onto `acme/ghost-widgets`, whose `repo_config` row has `'yes'`
  in the INTEGER `merge_on_green` column. SQLite's type affinity stores it; the scan into a bool fails, so
  `TaskCIMode` errors every cycle. The wake logs "task N's CI wake (my-branch) cannot be resolved, holding it for the backstop alone" each cycle, and the backstop still wakes the task (see Final runs). Dispatch then fails on the same unreadable row, as anything else reading that repository's settings would, so the scenario deletes the row afterwards.
  - PostgreSQL's typed columns refuse that row. There is no other way to make one repository's settings
    unreadable without breaking every task, so this variant is SQLite's alone.
  - An earlier attempt, a task re-targeted to a repository with no settings row, resolved to `github` and
    so exercised the unreadable-verdict path instead.
- **`ci-wake-stalled` clears** both ways it can:
  - when the task it names closes (the next notifications sweep);
  - and, in `backstop_then_sendback`, at the exact instant a send-back takes the task off a later
    `resume_later`. In that scenario the backstop-woken run parks on CI again, a review thread then
    releases it, and `sendBackFromResumeLater` clears the notification at the release instant
    (`clearedAt` equal to the daemon's release log line on both stores).

## Item 3 — closed and reopened; console sessions

- `close_reopen`: the wait is back-dated 2h so it is past `CIWaitHealthAge` (1h). Before the close,
  `GET /api/sandboxes` `ciWaits` and `grain health -json` both list the task.
  - Right after `POST /api/tasks/{id}/close`, both read `[]`.
  - `POST /api/tasks/{id}/reopen` puts the task back `deferred` and in `ciWaits`.
  - Seeding a completed Actions run on its branch head then wakes it within a cycle ("has the CI verdict it
    waited for"). Same result on both stores.
- `console`: an interactive task with no repository rests on `resume_later(message: true)`. A person's
  first reply wakes it within 0.1 s on both stores. PR #1507's `closed_at IS NULL` filter did not stop
  resting sessions waking.

## Item 4 — `/next/` pages (Playwright over the live SQLite daemon)

Screenshots were taken with `ui/e2e/screenshot.mjs --url … --code …` against the walkthrough daemon at
1440 px (desktop) and 390 px (phone), light and dark: `/next/tasks?tab=deferred`, `/next/tasks/16` (resting
on a CI wait, back-dated 2h) and `/next/tasks/17` (a person's Defer after a `resume_later`), and
`/next/system`. The contact sheet is attached to the task's closing note.

This sandbox's file reader shows PNGs as raw bytes and `LOOK=1` has no Gemini key here, so the shots were
not looked at directly. Instead, a Playwright script over the same pages checked two things:

- **Copy.**
  - The Deferred tab lists both tasks under "Deferred", each with "Bring back".
  - The "… CI" chip on both rows is the pull request's gate chip (`ui-ng/src/app/shared/chips.ts`, a
    required check still running), not a wait indicator, so it is right on both.
  - The task page shows the state "Deferred". Its conversation carries "grain: this task's run stopped
    with resume_later. It goes back in the queue once CI (my-branch) has a verdict; Undefer releases it
    sooner.", and its timeline shows "Waiting" then "Deferred".
  - The person-deferred task carries the same `resume_later` note, followed by its later "Deferred"
    transition.
- **Overflow.** `document.documentElement.scrollWidth` equals the viewport at 1440 and 390 px on all four
  pages. The only elements past 390 px are tab links inside their horizontally scrolling tab strips.

Live update without a reload: with `/next/tasks?tab=deferred` and `/next/tasks/16` open, closing #16
through the API dropped its row and turned the page to "Closed" within 0.3 s. Reopening brought the row
back and the page to "Deferred" within 0.3 s.

Classic was not walked: every page in scope has a `/next/` page. The CI-waits view exists in neither UI
(Finding 2).

## Final runs (fixed build, both stores)

`run_walkthrough.sh` with no arguments on each store (SQLite on 4280, PostgreSQL on 4290, run side by side), 12:55–13:02 UTC:

```
RESULT [sqlite] c: PASS task 2 (PR #9000) left deferred after 0.3s, runs=parked,succeeded, conversation mentions review thread x2, daemon log release lines=1
RESULT [sqlite] c_person: PASS task 3 stayed deferred 15s after a review thread; runs=parked
RESULT [sqlite] e: PASS task 4 left deferred after 0.3s on its owner's answer (review 5), runs=parked,succeeded, release lines=1, says decision x2
RESULT [sqlite] e_person: PASS task 6 stayed deferred 15s after its owner's answer
RESULT [sqlite] backstop_never: PASS task 8 woke 0.6s after back-dating 6h05m; ci-wake-stalled open after wake and after its run, cleared at 2026-11-15T12:57:39.155203464Z after close; says backstop x2
RESULT [sqlite] backstop_unreadable: PASS task 10 woke 0.6s after back-dating 6h05m; ci-wake-stalled open after wake and after its run, cleared at 2026-11-15T12:58:50.256809853Z after close; says backstop x1
RESULT [sqlite] backstop_nomode: PASS task 11 woke 0.6s after back-dating 6h05m; ci-wake-stalled open after wake and after its run, cleared at 2026-11-15T13:00:00.852804849Z after close; says backstop x1
RESULT [sqlite] backstop_then_sendback: task 12 ci-wake-stalled after backstop=[{"open":true,"clearedAt":null}]; re-parked=deferred; review thread released it after 59.2s; ci-wake-stalled now=[{"open":false,"clearedAt":"2026-11-15T13:01:03.976035277Z"}]
RESULT [sqlite] close_reopen: task 13 ciWaits before close=["13"] health=["13"]; after close=[] health=[]; reopened state=deferred ciWaits=["13"]; woke on CI after 0.3s (log: 1)
RESULT [sqlite] console: PASS task 14 (interactive=true) woke 0.0s after its first reply
model: task 11's CI wake (my-branch) cannot be resolved, holding it for the backstop alone: reading repo config for acme/ghost-widgets: sql: Scan error on column index 25, name "merge_on_green": sql/driver: couldn't convert "yes" into type bool   (SQLite daemon log, 12:58:53)
RESULT [postgres] c: PASS task 2 (PR #9000) left deferred after 0.3s, runs=parked,succeeded, conversation mentions review thread x2, daemon log release lines=1
RESULT [postgres] c_person: PASS task 3 stayed deferred 15s after a review thread; runs=parked
RESULT [postgres] e: PASS task 4 left deferred after 0.3s on its owner's answer (review 5), runs=parked,succeeded, release lines=1, says decision x2
RESULT [postgres] e_person: PASS task 6 stayed deferred 15s after its owner's answer
RESULT [postgres] backstop_never: PASS task 8 woke 0.0s after back-dating 6h05m; ci-wake-stalled open after wake and after its run, cleared at 2026-11-15T12:57:54.471159Z after close; says backstop x2
RESULT [postgres] backstop_unreadable: PASS task 10 woke 0.3s after back-dating 6h05m; ci-wake-stalled open after wake and after its run, cleared at 2026-11-15T12:59:06.238559Z after close; says backstop x1
RESULT [postgres] backstop_nomode: SKIP on postgres (no way to store an unreadable repo_config row without breaking every task)
RESULT [postgres] backstop_then_sendback: task 11 ci-wake-stalled after backstop=[{"open":true,"clearedAt":null}]; re-parked=deferred; review thread released it after 58.6s; ci-wake-stalled now=[{"open":false,"clearedAt":"2026-11-15T13:00:09.219931Z"}]
RESULT [postgres] close_reopen: task 12 ciWaits before close=["12"] health=["12"]; after close=[] health=[]; reopened state=deferred ciWaits=["12"]; woke on CI after 0.3s (log: 1)
RESULT [postgres] console: PASS task 13 (interactive=true) woke 0.0s after its first reply
```

## Environment

- Debian GNU/Linux 12 (bookworm) sandbox, 7 vCPU / 30 GB.
- Go 1.26.10, SQLite 3.40.1, PostgreSQL 15.20 (`postgresql` installed with `apt-get`; role `grain`,
  database `grain_walk`), Node 20.19.0, Playwright chromium-1234.
- Builds:
  - `grain-base`: `HEAD` `c7987fe4b` before the fix, used for the reproductions.
  - `grain`: this branch, used for every other run.
  - Both built with `make frontend && go build ./cmd/grain`.
- Daemon flags: `grain daemon -sandbox-port-base=0 -host-sandboxes -poll-interval 500ms -max-workers 1
  -max-mergers 0` (`1` for scenario (a)), with `-postgres-url` for PostgreSQL.
- Ports: SQLite 4280 (sim 4284), PostgreSQL 4290 (sim 4294).

## Tests run

- `go test ./pkg/orchestrator/...` passed (230 s) with the fix.
- `GRAIN_REQUIRE_POSTGRES=1 go test ./pkg/model/...` passed (`pkg/model` 59.7 s, on PostgreSQL).
- `GRAIN_REQUIRE_POSTGRES=1 go test ./pkg/model/postgres -run TestResumeLaterPullRequestLinks` passed
  (a one-off copy of the store-level test, removed again because `pkg/model` already runs it on
  PostgreSQL).

## Follow-ups

- Show CI waits (`ciWaits`, "tasks waiting on CI for more than 1h (resume_later)") on `/next/system`
  (Finding 2).
