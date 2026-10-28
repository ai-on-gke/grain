# Walkthrough: work-tree origins at the tree's root, and the on-open repair (2026-10-07)

Task 2420, for `docs/test-plans/2026-10-07.md` §1 (filed by task 2411).

## Environment and tested commit

- Base: `candidate` at `6634d5c81` (the branch `grain/task-2420` was cut from).
- Feature: `527548296` (`main` `13765a492`, Fixes #2311). `git branch -r --contains 527548296`
  lists `origin/candidate`, so the commit is in the base.
- Sandbox: Debian, 4 vCPU, Go 1.26.6, Node 20.19.0. PostgreSQL 15 and `sqlite3` installed with
  apt, the way the `postgres` CI job installs PostgreSQL. Playwright Chromium from
  `/var/cache/playwright`. Frontend built with `cd ui && npm ci && npm run build`.
- Older builds, each in a `git worktree` outside the task's branch:
  - `/home/debian/preorigin` at `c060a8acc^`, the parent of the commit that added
    `task.origin_ref` (`c060a8acc`, "feat(model): give every task an inherited work origin").
  - `/home/debian/prefix` at `527548296^` (`fc7ba6f99`).

## 1. Pre-fix data, real path (SQLite)

The driver (`cmd/walkorigins`, built in each worktree and not committed) opens a data directory
the way the daemon does: `sqlite.Open(sqlite.DefaultConfig(<data-dir>/store))`, `model.New`,
`Store.Init`. It files proposals with the calls `relayProposedTasks` (`pkg/orchestrator/finish.go`)
makes for each `propose_task` call: `GetTask` of the parent, `model.ExplicitOrigin`, `NewTaskID`,
and `PutTask` with a `proposed-by` link.

**The plan's expectation does not hold for a store built only by `527548296^`.** That build
gives B `task:A` and C `task:A` too: C inherits B's stored origin. The stale shape only arises
under a parent with no stored origin at all, which means a tree from before origins existed.
So the data was made in three steps:

```
preorigin-walk <dir>/store                 # c060a8acc^: files A (1), B (2) proposed by A
prefix-walk propose <dir>/store 2 C        # 527548296^: Init migrates + backfills, B proposes C
sqlite3 <dir>/store/grain.db "select id, origin_ref from task"
  1|
  2|
  3|task:2                                 # the stale row
fixed-walk open <dir>/store                # this branch: Init runs repairWorkOrigins
  task 3 parent="2" origin="task:1"        # repaired
fixed-walk open <dir>/store                # second open
```

Results:
- C (3) moves from `task:2` to `task:1`. A and B keep `""`.
- A second open changes nothing. The `sha256sum` of `sqlite3 grain.db .dump` is identical
  before and after it, and so is the checksum of `select id, origin_ref from task`
  (`e2821686…`).
- `Init` took 10-11 ms on each open.
- `grain daemon -data-dir <copy>` **does start locally** with no GitHub credentials, given
  `-disable-native-sandboxes -ui-addr 127.0.0.1:18990`. Without a sandbox flag it exits 2
  ("no sandbox backend selected"). Step 4 used this daemon.

## 2. PostgreSQL

`TestOpeningAPostgresStoreRepairsOriginsThatNameAParent` is new in
`pkg/model/postgres/work_origin_repair_test.go` and is kept. It runs on the `postgres` job's
`postgrestest.Open` schema. It seeds A→B→C→D with nothing at the root, plus E, filed by A, naming
a PR, with F below E. It writes the pre-fix `origin_ref` values directly, then runs `Init`.
- It asserts C and D become `task:1`, and F keeps the PR origin.
- It asserts a second `Init` changes no task row's `xmin`, so nothing was written.
- It asserts work filed from the repaired middle joins the root.

Result: **passes**. The `LIKE`/`||`/`IN (SELECT DISTINCT …)` query and the walk's correlated
`MIN(target)` subquery both run correctly on PostgreSQL 15.

## 3. Scale

`TestOriginRepairScale` is in the same file. It is skipped unless `GRAIN_ORIGIN_REPAIR_SCALE=1`
is set. It seeds 20,000 tasks in 2,000 trees of ten tasks, three levels deep, straight into
`task`/`task_link`. In the stale variant, half the trees have the pre-fix shape: 7,000 stale rows
in total.

```
GRAIN_REQUIRE_POSTGRES=1 GRAIN_ORIGIN_REPAIR_SCALE=1 go test ./pkg/model/postgres/ -run TestOriginRepairScale -v
```

| backend | no stale rows | no stale rows, again | repairing 7,000 rows | already repaired |
|---|---|---|---|---|
| SQLite | 64 ms | 59 ms | 491 ms | 56 ms |
| PostgreSQL 15 | 51 ms | 53 ms | 1.05 s | 45 ms |

These times are for the whole `Store.Init`. An already-repaired open is far under the ~1 s
threshold, so no follow-up was filed. The one-time repair of 7,000 rows takes about 1 s on
PostgreSQL, in one transaction.

## 4. UI

The store was the step 1 store, copied, under the fixed `grain daemon` (not `grain demo`). The
driver was Playwright Chromium against the built frontend (script
`/home/debian/walk/walk-origin.mjs`, not committed).
- The daemon answers loopback API calls as its own user.
- Its UI still shows the first-account/password dialog over the page.
- The script reads `section.work-tree` from the DOM, and hides that dialog with an injected style
  only to take the screenshots.

Before the fix (`before-task-*.png` in `2026-10-07-work-tree-origin-repair/`):
- C (3): origin line "generated from 2 · origin 1". Work tree "Work from task 1: 0 of 2 done",
  1 › 2 › 3. **Correct.**
- B (2): origin line "generated from 1", with no origin. Work tree **"Work from task 2: 0 of 1
  done"**, rooted at B. **Wrong.** This is finding F1.

After the fix (`after-task-*.png`): B's page draws "Work from task 1: 0 of 2 done", 1 › 2 › 3.
`GET /api/tasks/2/tree` answers `origin: task:1, rootTask: 1`.

## 5. Progress comments

On `candidate` this used a Go test with a fake GitHub client, since `-github-sim` is on `main`
only. The test is `TestTheOriginCommentListsTasksTheRepairMovedIntoTheTree` in
`pkg/orchestrator/worktree_internal_test.go`. It uses the package's existing `treeClient` fake.
- The tree is acme/widgets#5's: tasks 10-13. Task 14 is stored with the stale named-task origin
  `task:11`, which is what `527548296^` stored when a person named task 11 as the origin.
- After `Init`, 14 carries `acme/widgets#5`.
- `reconcileWorkTrees` posts exactly one comment, "0 of 5 done", listing tasks 10-14.
- `task:` origins never get a comment: `GitHubWorkOrigins` excludes them. So the old one-task
  tree gets no comment.

Result: **passes**.

## 6. Edge cases (Go tests, all pass)

- `pkg/model/work_origin_repair_test.go`:
  - **cycle**: 70 → 71 → 72 → 70, with origins naming tasks in the cycle. `Init` terminates. A
    proposal from inside the cycle is filed with a task origin. A second open moves nothing.
  - **deleted task**: 81 is deleted by hand, with its link left behind. 82 keeps `task:81`, and
    work 82 files gets `task:81`. A task that is not stored is taken as a root.
  - **chat-rooted tree**: chat 90 (interactive), with stale 92/93. Both are repaired to `task:90`,
    and the tree is 90 › 91 › 92 › 93.
  - **mid-tree PR origin**: 101 named `#3001` from chat 100 (`ExplicitOrigin`). 101 keeps the PR,
    102 inherits it, and stale 103 (`task:101`) is repaired to the PR.
- `pkg/orchestrator/work_origin_proposal_test.go`: **`propose_task` naming its own task**,
  through `orchestrator.ProcessResult`. The caller is mid-tree with no stored origin.
  - `origin: "task <caller>"` files at `task:root`.
  - `origin: "#7"` files at `acme/widgets#7`.
  - Both proposals have the caller as their parent.

## Findings

**F1 (fixed): an origin-less task part way down a tree draws a tree rooted at itself.**
`ui.Client.WorkTree` took `task.WorkOrigin == ""` to mean "this task is a root", and drew
`task:<id>`. Tasks filed before origins existed, in trees whose root had no issue, are stored with
no origin even though another task filed them (B above). The repair only rewrites `task:` rows,
so it leaves them that way. Their pages showed a separate tree rooted at them.
- Reproduction: step 1's data, then `GET /api/tasks/2/tree` returns `origin: task:2`. Or run
  `TestAnOriginlessTaskPartWayDownATreeIsDrawnInItsFilersTree` without the fix.
- Fix: a new `Store.TreeOrigin(ctx, id)` runs `treeOrigin` in its own transaction.
  `ui.Client.WorkTree` uses it for an origin-less task. A task nobody filed still comes out as
  `task:<id>`.

**F2 (fixed): the same task's pull-request lineage says "Origin: this task".**
`lineageSection` (`pkg/orchestrator/worktree.go`) had the same assumption.
- Reproduction: `TestTheLineageOfAnOriginlessFiledTaskNamesItsTreesRoot` without the fix.
- Fix: for a task with a parent and no origin, the section names `Store.TreeOrigin`.

**Noted, not fixed:**
- The plan's §1 step 1 expects `527548296^` to store B=`task:A`, C=`task:B`. It stores
  C=`task:A`. The stale shape needs a pre-origins parent (see step 1).
- For a pre-origins mid-tree task like B, the task view's origin line shows nothing (only
  "generated from"), and the merge trailers (`lineageTrailers`) carry no `Origin:`. Both read
  the stored `""`. Filling these rows is a data decision: `backfillWorkOrigins` deliberately left
  them empty. It is proposed as a follow-up rather than made here.
- The daemon's UI shows the password dialog even though its loopback API serves the daemon user,
  so a local walkthrough cannot use the page without creating an account. A new non-admin
  account cannot see tasks filed with no team (404). This was not investigated further.

No overlapping finding was found in `docs/test-runs/`. No report there mentions work-tree origins.

## Stubs

- GitHub: the `treeClient` fake in `pkg/orchestrator/worktree_internal_test.go`, for step 5 and
  the lineage test. Step 6's `ProcessResult` test uses the package's `githubsim`.
- Live model sessions: none. Proposals came from a recorded `agent.Result` with `propose_task`
  calls.
- Step 4 hid the daemon's login dialog with a CSS rule, for screenshots only. Grain's API and
  React were not mocked.

## CI

The full grain CI suite passed on `5e8bd58` (run `75bc59687d14bca6`): frontend, go, installer,
lint, package cache, postgres, terraform and ui e2e were all green. The `postgres` job ran the new
PostgreSQL test. The commit that adds this section changes only this report and is checked
with `wait_for_checks` again.

## Follow-up proposed

- Give pre-origins mid-tree tasks their tree's origin: the task view's origin line and the
  merge-commit `Origin:` trailer. Depends on this task, not auto-merged.
