# Test run — close/reopen attribution and grain's own `task_event` closes (2026-11-02)

Plan: [`docs/test-plans/2026-11-02.md`](../test-plans/2026-11-02.md) §5. That covers PR #242 (`task_event`, the timeline's Reopened step and meta line, `/api/pipeline/changes`, `grain get`) and PR #404 (grain's own closes through `Store.ObserveClose`).

## Environment

- Checkout: `main` at `8726f7f7`; nothing newer had landed on `origin/main` for these files when the run started. Run by task 2447 (filed by task 2399).
- Binary: `cd ui && npm ci && npm run build && go build -o /home/debian/bin/grain ./cmd/grain`, so the real UI is embedded. I rebuilt it after each fix and repeated the live checks on the fixed binary.
- **`grain demo -github-sim -fake-ci -reconcile 2s -addr 127.0.0.1:4290 -open=false -access-code-file …`**, driven over the API with the demo's access code (a human principal, `debian`). Covers a person's close and reopen (browser, API and CLI), the agent API, an external agent token, five-cycle cycling, the no-op close and the meta line.
- **`go run ./ui/e2e/consolerestserver`**, built as `bin/crs` with `GRAIN_E2E_ACCESS_CODE` set. It runs the real store, UI server and `orchestrator.RunCycle`, with a scripted agent. Covers supersede, goal met, console-session restart and console-session delete. No real agent backend was used; the sandbox has no credentials for one.
- `sqlite3` was installed with apt for reading `task_event`.
- Playwright: `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`. Screenshots came from `node e2e/screenshot.mjs --url … --code … --pages /tasks/1` and from a bounding-box script run against the live demo.

## Close path × event row × timeline × pipeline-changes

Rows read with `sqlite3 <store> 'select * from task_event'`. Timeline text is the task page's step plus its meta line (the `transitions` of `GET /api/tasks/<id>`, also printed by `grain get <id>`). The pipeline-changes column is `GET /api/pipeline/changes?since=…`, which backs `deploy://pipeline-changes`.

| Close path | Where | `task_event` row (kind · actor_kind · actor_id · reason · detail) | Timeline / `grain get` | pipeline-changes entry |
|---|---|---|---|---|
| Person, **Close button in the browser** | demo (Playwright spec) | `closed · human · <operator> · "" · ""` | "Closed", meta "by human …" | `filed → closed`, "by human …" |
| Person, API/CLI with a reason (`grain close -reason …`) | demo, task 2 | `closed · human · debian · cli close with a reason` | `-> closed … by human debian (cli close with a reason)` | `… → closed`, "by human debian: …" |
| Agent API `api_close` (`X-Grain-Acting-Task: 3`, `X-Grain-Acting-Reason`) on running task 8 | demo | `closed · agent · 3 · duplicate of task 11 (agent api_close) · cancelled run demo-run-8 (attempt 2)` | `-> closed … by agent 3 (duplicate of task 11 (agent api_close)); cancelled run demo-run-8 (attempt 2)` | `running → closed`, "by agent 3: …; cancelled run demo-run-8 (attempt 2)" |
| External agent token (`walk-ext`), close and then reopen | demo, task 11 | `closed · agent · external:walk-ext · external triage: stale` and `reopened · agent · external:walk-ext · external: not stale after all` | "Closed" / "Reopened", each "by agent external:walk-ext (…)" | `filed → closed`, `closed → reopened`, each "by agent external:walk-ext: …" |
| Supersede (file B with `supersedes: A`) | consolerestserver, task 2 | `closed · automation · grain · superseded by task 3` | "Closed", meta "by automation grain · superseded by task 3" | `filed → closed`, "by automation grain: superseded by task 3" |
| Goal met (`tasks-closed` on a task a person then closed) | consolerestserver, task 5 | `closed · automation · grain · goal met (tasks-closed)`; the dependency's own close is `human · console-rest-e2e-operator · dependency done` | "Closed", meta "by automation grain · goal met (tasks-closed)" | `filed → closed`, "by automation grain: goal met (tasks-closed)" |
| Console session **restart** (Director) | consolerestserver, task 1 | `closed · human · console-rest-e2e-operator · console session director was restarted` | "Closed", meta "by human … · console session director was restarted" | `filed → closed`, "by human …: console session director was restarted" |
| Console session **delete** | consolerestserver, task 7 | `closed · human · console-rest-e2e-operator · console session walk-session was deleted` | "Closed", meta "by human … · console session walk-session was deleted" | `filed → closed`, "by human …: console session walk-session was deleted" |
| Merged PR ("pull request merged") | **not live**: `TestSyncPullRequestsClosesOutAMergedPullRequest`, which now asserts it | `closed · automation · grain · pull request merged` | (unit) | (unit) |
| Review finished | **not live**: `TestSyncReviewsClosesAReviewThatFinishedWithNothingToLand`, which now asserts it | `closed · automation · grain · review finished` | (unit) | (unit) |

Each live close path wrote exactly one row, and that row is attributed.

## "What is NOT proved", item by item

1. **Every close path records exactly one attributed event.** Confirmed live for eight paths (table above). The merged-PR and review-finished closes cannot be reached on `grain demo`: it runs no `SyncPullRequests` or `SyncReviews`. `consolerestserver` can't reach them either, since it has no GitHub. So the existing orchestrator tests for those two paths now read back the event. `deploy://pipeline-changes` itself was not read: the sandbox CLI reaches only this deployment's namespace, and that needs the `self-debug` capability, which this run does not hold. I read the endpoint behind it instead.
2. **Close/reopen cycling.** Task 2 was closed and reopened five times in about 100 ms. The result was ten `task_event` rows (ids 4–13) in order, alternating closed and reopened, each "by human debian" with its own reason. The timeline showed exactly those ten steps after "queued", with no inferred requeue and nothing marked `inferred`. The pipeline-changes entries go `filed → closed`, `closed → reopened`, `reopened → closed`, and so on.
3. **A close that changes nothing records nothing.** True of the row: re-closing an already-closed task added no `task_event`. **But the timeline was wrong (finding 1).**
4. **The meta line with a long reason, at 1440/390 px, light and dark.** At 1440 it wraps inside its item. **At 390 the reason's URL ran out of its item (finding 2).**
5. **State dump round-trip.** `task_event` is an operational table (`pkg/staterepo/tables_internal_test.go`). It travels in the full `staterepo.Export` dump that a schema upgrade or migration imports, not in the config export committed to the state repository. I backed up the consolerestserver store, which held 5 events: supersede, two human closes, goal met, and console restart/delete. I ran `staterepo.Export` on the backup, then `model.Store.Init` plus `staterepo.Import` into a fresh data dir. The tables dump held `task_event.json`, and `select * from task_event` was identical row for row on both sides.

## Findings

### 1. Re-closing a closed task re-stamped its closure: a second, unattributed "Closed" step (fixed)

**Reproduction** (`grain demo`, before the fix):

```sh
curl -XPOST …/api/tasks/8/close -H 'X-Grain-Acting-Task: 3' -H 'X-Grain-Acting-Reason: duplicate of task 11' -d '{}'
sleep 2
curl -XPOST …/api/tasks/8/close -d '{"reason":"closing again"}'
grain get 8
#   -> closed   2026-11-02T06:00:13Z by agent 3 (duplicate of task 11 (agent api_close)); cancelled run …
#   -> closed   2026-11-02T06:00:40Z                      <- nobody on it
# /api/pipeline/changes: {"id":"8","from":"closed","to":"closed"} at 06:00:40, no detail
```

`task_event` correctly had one row. But `ui.Client.setClosed` set `ClosedAt = now` whether or not the task was already closed. `model.MergeTaskEvents` only folds a recorded close into the closed step at `ClosedAt` when the two are within a second. So the moved stamp became a step of its own with nobody on it, and "closed at" moved later than the actual close.

`Store.ObserveClose` has the same exposure. Almost every orchestrator close site (`review.go`, `taskgoals.go`, `sync.go`, `obsolete.go`, …) and `Store.Supersede`'s `closeInTx` stamp `ClosedAt = &now` without checking it first. Had one of them reached a closed task, it would have recorded a second close with no reopen between. I could not reach that live: `SyncPullRequests` merging a PR after a person's close leaves the task alone even on the base. The fix there is defensive.

**Fix:**
- `setClosed` stamps only an open task.
- `ObserveClose` and `closeInTx` keep an existing stamp. A `set` that clears `ClosedAt` still reopens.

**Regression tests:**
- `TestClosingAClosedTaskLeavesItsOneAttributedClose` (`pkg/ui/task_event_test.go`) fails on the base with two closed steps, the second with `By:""`.
- `TestObserveCloseOfAClosedTaskKeepsItsStamp` (`pkg/model/observe_close_test.go`) fails on the base: `ClosedAt` moved to the second call.
- `TestSyncPullRequestsMergedAfterAPersonClosedTheTaskKeepsTheirClose` (`pkg/orchestrator/sync_test.go`) pins that path. It passes on the base too.
- The Playwright spec below checks it in the browser.

On the fixed binary, the same reproduction leaves one event, one "Closed" step at the first close, and no `closed → closed` entry.

### 2. A long reason ran out of its timeline item at phone width (fixed)

**Reproduction:** close a task with a reason containing an unbroken URL, for example `…superseded by the rewrite tracked at https://github.com/acme/widgets/pull/104/files#diff-4f2a9c…e0f and nothing here is worth keeping…`, then open `/tasks/<id>` at 390 px.

The `.timeline-transition-by` box stays inside its item: x 40–374, against the item's 17–374. But its `scrollWidth` was 431 against a `clientWidth` of 334, so the URL ran 97 px past it. At 1440 the line was 840 = 840 wide, so fine. Light and dark measured the same.

**Fix:** `.timeline-transition-by { overflow-wrap: anywhere; }` in `ui/src/style.css`. After it, 390 light and dark measure `scrollWidth` 334 = `clientWidth` 334, with the box inside the item.

**Regression spec:** `ui/e2e/close-reopen-attribution.spec.js`. It:
- clicks **Close** on a task page and asserts "by human …";
- reopens the task, closes it with the long reason, then closes it again as a no-op;
- at 1440×900 and 390×844, asserts exactly two "Closed" steps, one "Reopened", three meta lines and no "closing again";
- asserts the meta line's bounding box sits inside its `.timeline-item` and its `scrollWidth ≤ clientWidth + 1`.

**Screenshots:** `/tasks/1` with the long-reason close, at 1440 and 390 px in light and dark, before and after the fix (kept in the sandbox, not committed). The 390 px light crops of the step, before and after, are attached to the task's closing note.

### 3. The browser's Close takes no reason (not a bug in this range, follow-up)

The plan's example meta line, "by human bob · cycling in-flight tasks · cancelled run …", can only be produced through the API or `grain close -reason`. The task page's **Close** button (`ui/src/components/detail/Actions.jsx`) posts only `close_pull_request`, so a person's close in the browser always reads just "by human …". I filed a follow-up to offer an optional reason there.

## Fixes on this branch

- `fix(model): keep a standing closure's stamp when a task is closed again`: `pkg/ui/client.go`, `pkg/model/task_event.go`, with tests.
- `fix(ui): wrap a long close or reopen reason inside its timeline item`: `ui/src/style.css`, `ui/e2e/close-reopen-attribution.spec.js`.
- `test(orchestrator): pin grain's merged-PR and review-finished close events`, plus `closeInTx` in `pkg/model/supersede.go`.

Ran locally, all ok:
- `go test ./pkg/model/ ./pkg/orchestrator/ ./pkg/ui/ ./pkg/staterepo/ -count=1`;
- `npx playwright test e2e/close-reopen-attribution.spec.js`.

## Follow-ups

- Offer an optional reason on the task page's Close (and Reopen), sent as `reason` on the existing bodies (finding 3).

## Not run

- Merged-PR and review-finished closes in a live deployment. Neither surface runs those sync steps; covered by the orchestrator unit tests instead.
- `grain read deploy://pipeline-changes` itself. It needs `self-debug`, which this run does not hold; `/api/pipeline/changes`, which serves it, was read instead.
- Release-triage closes keeping the triager's principal. Not listed in "What is NOT proved"; covered by PR #404's own tests.
- `tests/e2e/close_reopen_streak_walkthrough_test.go` was left to CI (`Tests / go e2e`).
