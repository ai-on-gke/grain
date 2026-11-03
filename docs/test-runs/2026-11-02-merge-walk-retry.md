# 2026-11-02 — Merge walk without ordering, Retry on a stopped turn, base-repair confirm (test plan §4)

Plan: `docs/test-plans/2026-11-02.md` §4 (PRs #415, #416, #356). Task 2446,
filed by task 2399. Read with `docs/merge-queue-behind.md` ("No order
between pull requests").

## Environment

- Task sandbox, Debian, Go 1.26.10, Node 20.19, Chromium from
  `/var/cache/playwright` (chromium-1234). Branch based on `origin/main`
  at `8726f7f7`.
- UI built with `npm ci && npm run build`, then `grain` built from the
  checkout so the UI is embedded.
- `grain demo -github-sim -fake-ci -reconcile 2s -merge-queue-states`
  (ports 4391 and 4392), for the Merge queue tab, the base-repair confirm
  and the screenshots. API calls used the demo's access code.
- `ui/e2e/consolerestserver` (real store, UI server and orchestrator
  cycle every 50 ms, scripted agent) for stop → Retry. Its run for the
  task was held open with `POST /e2e/interactive-control
  {"taskId":N,"holdNext":true}`.
- The merge walk itself ran over `githubsim` in
  `pkg/orchestrator/mergewalk_walkthrough_test.go`, because
  **`grain demo` never runs the pull-request reconciler**. Its `-reconcile`
  loop runs the suite, release, schedule, review-question,
  scheduled-workflow and github-events/takeovers reconcilers, not
  `SyncPullRequests`. Pull requests built through
  `/api/e2e/github-sim/repos/...` are never walked or merged there, and
  the only merge-queue rows the demo can show are the ones
  `-merge-queue-states` seeds. Proposed as a follow-up (a demo flag that
  runs the merge walk against the sim each tick).
- The new tests make GitHub's `mergeable` real: a wrapper transport runs
  `git merge-tree --write-tree <base> <head>` against the sim's bare repo
  before every `GET /repos/{o}/{r}/pulls/{n}`. Every branch carries real
  file content, so a merge that moves `main` really does make a sibling
  conflict. The same wrapper logs every pull-request read and merge, and
  that log stands in for "the sim's request log". GitHub is the only
  thing stubbed.

## The pull-request set

| Task | What it is | Branch content | Checks | Mergeability |
|---|---|---|---|---|
| u1 | UNKNOWN mergeability | `u1.txt` | green | pinned `null` (GitHub still computing) |
| p2 | check that never reports | `p2.txt` | `cla/google` queued forever | computed (clean) |
| c3 | conflicted with main | `c3.txt` + its own `README.md` (main has another) | green | computed (conflicts) |
| g4–g7 | four green PRs, queued behind the three above | `gN.txt` each | green | computed (clean) |
| x1, x2 | conflict **with each other** | each writes its own `README.md` | green | computed: both clean until one lands |
| x3 | green bystander | `x3.txt` | green | computed |
| w1–w3 / k1–k3 | three green PRs in each of `acme/widgets` and `acme/gadgets` | own file each | green | computed |

`max_merges_per_repo` is passed to `SyncPullRequestsGuarding` the way the
daemon passes `Config.MaxMergesPerRepo`. Merge-on-green is off (the
default) throughout.

## Per-cycle results

### 1. Cap 1 — `TestMergeWalkWalkthroughCapOneMergesOneGreenPerCycle` (cycles 4 minutes apart)

| Cycle | Merged | g-rows still waiting | u1 | p2 | c3 |
|---|---|---|---|---|---|
| 1 | g4 | g5–g7: "merges next cycle: this cycle's 1 merge into acme/widgets main is used (task g4 merged)" | "waiting for GitHub to work out whether this branch merges cleanly" | "waiting on checks that have not finished (`cla/google`)" | "it has conflicts with `main`, so grain is sending this task back to an agent to repair it" |
| 2 | g5 | g6, g7: "… (task g5 merged)" | "… merges cleanly for 4m" | "… (`cla/google`) for 4m" | "waiting for a worker to pick this task up and repair the branch (it has conflicts with `main`; automatic repair 1 of 2)" |
| 3 | g6 | g7: "… (task g6 merged)" | "… for 8m" | "… for 8m" | same |
| 4 | g7 | — | "… for 12m" | "… for 12m" | same |
| 5 | — | — | "… for 16m" | "… for 16m" | same |

Requests on cycle 1: `GET #9002 (c3), GET #9003 (g4), PUT #9003/merge, GET
#9003, GET #9004, GET #9005, GET #9006, GET #9001 (p2), GET #9000 (u1)`.
The three blocked members hold only themselves, and no row says "behind
task". **Pass.**

### 2. Cap 2 and the re-read — `TestMergeWalkWalkthroughCapTwoReReadsBeforeTheSecondMerge`

| Cycle | Merged | Waiting green rows | u1 / p2 |
|---|---|---|---|
| 1 (t) | g4, g5 | g6, g7: "merges next cycle: this cycle's 2 merges into acme/widgets main are used (tasks g4 and g5 merged)" | no duration yet |
| 2 (t+4m) | g6, g7 | — | "for 4m" |
| 3 (t+20m) | — | — | "waiting for GitHub to work out whether this branch merges cleanly for 20m", "waiting on checks that have not finished (`cla/google`) for 20m" |

The request log for cycle 1 shows the re-read:

```
GET #29002, GET #29003, PUT #29003/merge, GET #29003,
GET #29004, GET #29004, PUT #29004/merge, GET #29004, GET #29005, ...
```

g5 (#29004) is read twice after g4's merge and before its own. The first
read is the walk's ordinary read of the member, and the second is the
pre-merge re-read (`mergeWalk`'s "base as it now stands"). Both come after
the first merge, so the re-read is correct. The first read is redundant on
a lane that has already merged once this cycle. It costs one GET per
extra merge, which is noted, not filed. **Pass.**

### 3. Two PRs that conflict with each other — `TestMergeWalkWalkthroughTwoMergesThatConflictWithEachOther` (cap 3)

| Cycle | Merged | x2's row |
|---|---|---|
| 1 | x1, x3 | `repair_needed`: "it has conflicts with `main`, so grain is sending this task back to an agent to repair it" |

Requests: `GET #49000, PUT #49000/merge, GET #49000, GET #49001, GET
#49002, GET #49002, PUT #49002/merge, GET #49002`. x2 (#49001) is read
once after x1's merge. That read already sees the conflict x1 made, so x2
gets **no merge attempt at all**. x3 still merges in the same cycle. That
is the behaviour the plan asked for: the second merge is judged against
the moved base, is not merged, and its row says why. **Pass.**

### 4. Lane independence — `TestMergeWalkWalkthroughTwoRepositoriesMergeToTheirOwnCaps` (cap 2)

| Cycle | Merged | Held rows |
|---|---|---|
| 1 | k1, k2 (gadgets), w1, w2 (widgets) | k3: "merges next cycle: this cycle's 2 merges into acme/gadgets main are used (tasks k1 and k2 merged)"; w3: "… into acme/widgets main are used (tasks w1 and w2 merged)" |

Each repository spends its own cap in the same cycle. Two bases in one
repository are covered by `TestLanesStayIndependentWithoutOrder`. **Pass.**

### 5. Stop → Retry in the browser (consolerestserver, Chromium)

| Step | Before the fixes | After |
|---|---|---|
| Running task page (1440 px) | controls: pause, pause now, cancel. There is no Stop button: the chat composer only shows Stop for interactive/console tasks, so Stop was sent to `POST /api/tasks/N/stop`, the call the composer makes | same |
| After Stop | `awaiting_reply`, `turnStopped: true`, status line "Waiting for your reply · Turn stopped by user"; attempt 1 recorded **"cancelled by user"** | same, attempt 1 recorded **"turn stopped by user"** |
| Retry on the stopped page | **no Retry button** (only "run now" and the reply box) | Retry shown at 1440 and 390 px, light and dark, inside the viewport |
| After Retry | `queued`, stop lifted, **still not dispatched 47 s later** | attempt 2 started within milliseconds of the click (3 ms between `queued` and `running`) |

The agent's Retry being refused, and the task staying stopped, is pinned by
`TestRetryOfAStoppedTurnIsAPersonsToMake`. It was run here and passes. It
was not driven live, because consolerestserver mints no agent token.

The stopped base-repair at a queue's head is
`TestSyncPullRequestsMovesPastAQueueHeadWhoseRepairWasStopped`. It was run
here and passes. The row reads "stopped by an operator -- reply or press
Retry to resume", the head goes to `merge_failed` at once rather than after
the six-hour `RepairDeadline`, and the next entry merges two cycles later.
Since #415 the head no longer holds anyone else's merge anyway. What the
move-off still releases is the head's CI priority (`MergeQueueHeads`).

### 6. The base-repair bypass confirm (PR #356)

`ui/e2e/merge-on-green-base-repair-walkthrough.spec.js` was run over a
real `grain demo` in Chromium, and all 3 tests pass. Both switch locations
(the repository's Merge queue tab, and Settings → Repositories) were
checked for each of these:

- turning it on asks a `confirm` naming the repository and "trust-boundary
  code";
- Cancel sends no PUT and leaves it off after a reload;
- OK turns it on, and it survives a reload;
- turning it off asks nothing.

**Pass.**

### `grain merge-queue` (this deployment, read-only, at the time of the run)

`grain merge-queue` is the in-sandbox CLI and reaches this deployment's
real queue, not the demo. The host binary has no such command. 15 rows,
none saying "behind task N". Examples:

- "an agent is repairing this branch now (automatic repair 1 of 2)"
- "waiting for a worker to pick this task up and repair the branch (it has
  conflicts with `main`; automatic repair 2 of 2, allowed because `main`
  has new commits since the first repair …)"

The `Branch:` column is blank for most rows (tasks that name no base).
For two old `bwsalmon/grain` security-review rows it shows the *head*
branch (`grain/049f1e09/task-633`), and for one row it shows `main`. The
column means different things on different rows. This is minor and was
not filed.

## Findings

1. **Stop recorded a run it finished itself as "cancelled by user", so
   Retry waited out the failure backoff.** When `ui.Client.Stop` has no
   `TaskCanceller` that reaches the run (consolerestserver, or any run this
   process did not dispatch), `cancelActiveRuns` finished the stored run
   as `cancelled` / "cancelled by user". `model.EndingOf` reads that as
   `EndingCancelled`, which counts toward the failure streak (only
   `EndingTaskClosed` and `EndingTurnStopped` are passed over). The
   person's Retry then left the task `queued` behind
   `dispatch.retryEligible`'s backoff.
   - Reproduction: `TestRetryOfAStoppedTurnWithNoCancellerIsNotHeldByBackoff`
     (`pkg/ui/client_test.go`). Without the fix it fails with `Stop recorded
     "cancelled"/"cancelled by user", which reads as "cancelled"; want
     "turn_stopped"`.
   - **Fixed**: Stop now records `model.TurnStoppedDetail`. ForceMerge
     keeps its own sentence.
2. **The task pane had no Retry for a stopped turn.** PR #416 says Retry
   works from the UI, and the queue row says "reply or press Retry to
   resume", but `Actions.jsx` drew Retry only for `state === "failed"`.
   - Reproduction: the new `Actions.test.jsx` case "shows Retry for a
     turn an operator stopped".
   - **Fixed**: Retry is shown for `awaiting_reply` with `turnStopped`.
3. **consolerestserver's Stop did not reach the held run.** It wired no
   `TaskCanceller`, so Stop finished the stored row while the scripted
   agent went on holding the task, and the orchestrator never dispatched it
   again.
   - **Fixed**: it now wires `orchestrator.TaskCancellers` into both the
     UI and the orchestrator, as `pkg/daemon` does.
   - `ui/e2e/stopped-turn-retry.spec.js` (1440 and 390 px) pins
     stop → Retry → a new attempt within ten seconds.
4. **The "Merge when green and clean" switch is inert, and its copy said
   otherwise.** Since #415, `queueEntry.mergeOnGreen` is set
   (`sync.go:1198`) and read nowhere. The cap-1 walk above merged four
   green PRs past UNKNOWN, pending and conflicted ones with the switch off.
   The repository's Merge queue tab still said that with the switch off
   "acme/widgets's queue merges strictly in order".
   - **Fixed (copy)**: the text now says the switch no longer changes
     anything, and the field comment no longer describes a strict lane.
   - **Follow-up filed**: remove the switch, its API, its setting and its
     tests.
5. **`grain demo` cannot walk a merge** (see Environment). **Follow-up
   filed.**

Harness note, not a product bug: after a stop is lifted (by Retry *or* by a
person's reply), consolerestserver's scripted interactive answer, a
`comment_on_issue` tool call with a resumable session, re-queues the
non-interactive task every cycle. That is 75 succeeded attempts in 4 s. The
spec closes its task in `finally`. A control run lifting the stop with a
reply loops the same way, so this is not Retry's doing.

## Screenshots

These were shot and checked with bounding boxes. This sandbox's file
reader shows PNGs as bytes, and it has no Gemini key for `LOOK=1`.

- **Merge queue tab** (`/repos/acme/widgets/settings/merge-queue`),
  before and after the copy fix, at 1440 and 390 px in light and dark
  (`ui/e2e/screenshot.mjs --full-page`). At every combination the
  checkbox sits left of its text (box x+width ≤ text x), the text's right
  edge is inside the viewport, and the page has no horizontal overflow.
  Text colour is rgb(110,106,96) in light and rgb(155,152,143) in dark.
- **Stopped task page** on consolerestserver: before at 1440 light (no
  Retry), and after at 1440/390 in light and dark (Retry visible, inside
  the viewport).

## Commands run

- `go test ./pkg/orchestrator/...`: ok.
- `go test -race -count=2` on the merge-walk tests (`TestMergeWalk*`,
  `TestEveryCleanMember…`, `TestACleanMemberPastTheCap…`,
  `TestTheSecondMerge…`, `TestLanesStayIndependent…`,
  `TestAWaitingMember…`): ok.
- `go test -race -count=2 -run 'TestRetry|TestStop|TestForceMerge'
  ./pkg/ui/`: ok. `go test ./pkg/ui/`: ok.
- `npx vitest run src/components/RepoPage.test.jsx
  src/components/detail/Actions.test.jsx`: 133 passed.
- Playwright: `console-resting-notes-walkthrough`,
  `task-chat-optimistic-sends`, `merge-on-green`,
  `merge-on-green-base-repair-walkthrough` and `stopped-turn-retry`: 19
  passed.

## Not run

- The merge walk was not driven live in `grain demo` (finding 5). It is
  driven over the same sim in Go instead.
- GitHub's own merge-queue hand-off counting as a merge was not exercised
  here. It is covered by the existing `mergewalk` and `mergequeue` tests.
- An agent-token Retry was not made against a live server (finding 1's
  neighbour `TestRetryOfAStoppedTurnIsAPersonsToMake` covers it).
- A `grain daemon` with a Docker or Granule run host was not used.
  consolerestserver stood in for it.
