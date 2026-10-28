# Test run: per-team merge batch size, halved-batch re-admission, propose_task `not_before` (2026-10-02 plan §4)

Task 1576, run 2026-10-03 00:34–01:25 UTC. Plan: `docs/test-plans/2026-10-02.md` §4. The plan file is on `candidate` (it landed with task 1528), not yet on `main`.

## Code under test

All five commits are on `main` (`git branch -r --contains 977026938` and `… 7589687b4` list `origin/main`). This branch is based on `main` at `5a63bb2d1`.

| Commit | What |
| --- | --- |
| `977026938`, `9b3c34631` | team `mergeBatchSize`, Team settings → Capacity |
| `e48ccaf12` | a halved batch re-admits its members through `batchable` |
| `7589687b4` | `propose_task not_before`, `POST /api/tasks deferredUntil` |
| `4faa942b7` | pause auto-resume / candidate auto-promotion act on the row they read (store-level; covered by its own tests, not walked here) |

## Environment

- Debian sandbox, Go 1.26.6, Node 20.19.0, Playwright Chromium from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`.
- Production UI bundle: `cd ui && npm ci && npm run build` into `pkg/ui/static`. Binary: `go build -o /tmp/grain ./cmd/grain`.
- Demo server: `/tmp/grain demo -addr 127.0.0.1:0 -open=false`. This is the real `pkg/ui.Server` over embedded SQLite in a throwaway directory. The kernel picked the port (42095).
- Browser scripts drove that server with Playwright's library API (`ui/node_modules/playwright`), using the default actor (`debian`, an admin) and `X-Grain-User: bob` for the non-admin member.

### Where the plan's environment did not hold

`grain demo` runs **no orchestrator, no githubsim and no agent** (`cmd/grain/demo.go`: "no daemon, no orchestrator, no sandbox"). That changes three parts of the plan:

- **Steps 2–3 (merge queue).** These ran as Go scenarios in `pkg/orchestrator`. They use real githubsim pull requests and a real bare git repo, and drive the cycle's own `merge-batch` reconciler and `SyncPullRequestsGuarding` over a real SQLite store. Only the CI engine is faked.
- **Step 4 (scripted agent).** The agent is a Go harness calling the real MCP `propose_task` handler (`mcp.NewLiveProposalTools`). Behind it is the real live filer (`daemonProposalFiler` over `ui.HTTPClient`), pointed at the running demo. No model is involved.
- **Wake after the clock passes.** The demo has no clock control and no `reconcileWake`. A near-future task stays `deferred` past its wake time there; task #15 still read "Picks itself back up on 10/3/2026, 12:42:44 AM" at 00:43:39. The wake itself was walked in Go through the cycle's own `wake` reconciler.

## Stubs (external boundaries only)

| Boundary | Stub | Where |
| --- | --- | --- |
| GitHub | `githubsim` (real HTTP, real bare repo) | merge-queue scenarios |
| grain CI engine | `fakeCI` from `pkg/orchestrator`'s tests. Runs are started for real, and the test sets each conclusion. | merge-queue scenarios |
| Push-triggered CI on a moved head | the test starts the run (`greenGrainCI`) where the git proxy's push hook would | step 3 |
| GitHub credentials | the demo's own fake token file | demo |
| Agent / model | a Go harness calling the real `propose_task` handler | step 4 |
| Clock | `client.Now` and the reconciler's `now` argument | wake scenario |

Nothing of grain's own (REST API, React, store) was mocked.

## Commands

```
cd ui && npm ci && npm run build && cd .. && go build -o /tmp/grain ./cmd/grain
/tmp/grain demo -addr 127.0.0.1:0 -open=false &
node /tmp/walk/step1.mjs        # Capacity pane (step 1)
go test ./pkg/orchestrator -run 'TestMixedTeamBatchSizes|TestInterleavedTeamBatchSizes|TestAFailedFourMemberBatchHalves|TestANearFutureNotBeforeWakes' -count=3 -v
WALK_URL=http://127.0.0.1:42095 go test ./cmd/grain -run TestZZWalkProposeNotBefore -v   # scratch harness, not committed
node /tmp/walk/step45.mjs 14,15,16,18,19,20; AS=bob node /tmp/walk/step45.mjs 21,22
python3 /tmp/walk/step5.py      # POST /api/tasks as bob
cd ui && npx playwright test e2e/merge-batch-not-before-walkthrough.spec.js --project=chromium
```

## Outcomes

### Step 1: Capacity pane

Teams `solo` and `quad` were created over `POST /api/teams`. Then in the browser, at `/teams/<id>/settings/capacity`:

| Input | Box after blur | Stored (`GET /api/teams/<id>`) | Message shown |
| --- | --- | --- | --- |
| solo `1` | 1 | 1 | none (saved) |
| quad `4` | 4 | 4 | none (saved) |
| quad `21` (`MaxMergeBatchSize`+1) | **21** | 4 | **none**, `aria-invalid=false` (finding 1) |
| quad `-1` | **-1** | 4 | **none** (finding 1) |
| quad `abc` (typed key by key) | 4 | 4 | none. Chromium's number box takes no letters. |
| solo `0` | 0 | 0 | none (saved). The helper text says 0 means "the deployment's batch size". |

- Reload: the boxes read 1 and 4. `GET /api/teams` reported `default 0, quad 4, solo 1`.
- Server-side refusals, which are readable:
  - `PUT mergeBatchSize 21` returned 400 `mergeBatchSize cannot be more than 20`.
  - `-1` returned 400 `mergeBatchSize cannot be negative (0 inherits the deployment's)`.
  - `"abc"` returned 400 `json: cannot unmarshal string into Go struct field UpdateTeamRequest.mergeBatchSize of type int`.
  - `POST /api/teams` with 21 returned 400, with the same message as the PUT.
- The pane does not show what the deployment's inherited size actually is. 0 is described only as "the deployment's batch size" (observation 2).

### Step 2: six queued changes, deployment size 3, teams at 4 and 1

`TestMixedTeamBatchSizesKeepQueueOrder`: t1–t3 are in `quad` (4), t4–t6 are in `solo` (1), all individually green auto-merge PRs on githubsim.

- Batch 1 = **t1,t2,t3**. All three of quad's changes went in: the team's own 4 replaces the deployment's 3, and collection ended at t4. It passed, and all three merged on one suite.
- t4, t5 and t6 were never batched (no batch row on any cycle). Each merged on its own, strictly in queue order. No later change merged ahead of an earlier one.

`TestInterleavedTeamBatchSizesNeverBatchPastASizeOneChange`: the same six changes, alternating quad/solo/quad/…. No batch formed: t2 (solo) ends collection where it stands rather than being passed over, so queue order holds.

### Step 3: four-member batch fails, a member's head moves mid-suite

`TestAFailedFourMemberBatchHalvesWithoutAdoptingAMovedHead`, with t1–t4 all in `quad`:

1. Batch = t1,t2,t3,t4 (4 members under the team's size, above the deployment's 3).
2. A new commit was pushed to t2's branch while the suite ran (`5545c4e` → `d76ef6b`). Then the suite failed on `go`.
3. The orchestrator logged: `re-testing the first 2 of 4 change(s)` and `acme/widgets#9001 (t2) moved from 5545c4ea9bf2 to d76ef6bfb365 while its batch was failing; leaving it out of the halved batch`.
4. The halved batch was **t1 alone**. t2's `HeadSHA` was not adopted, and its `batching` state was cleared.
5. The halved batch passed and t1 merged. t2 did **not** merge across three further queue syncs on its old head's verdict.
6. Only once a run against `d76ef6b` passed on its own did t2 merge.

e48ccaf12 holds.

### Step 4: `propose_task not_before` from a scripted agent

Two parents were filed over the API: #13 (auto-merge, approved) and #17 (not auto-merge, approved). Results, verbatim where it matters:

| Parent | `not_before` | Result | Stored |
| --- | --- | --- | --- |
| #13 auto | `2026-10-04` (tomorrow) | "…queued already… It is filed deferred and cannot start before 2026-10-04T00:00:00Z…" | #14 `deferred`, approved |
| #13 auto | now+2m | same, with 00:42:44Z | #15 `deferred`, approved |
| #13 auto | now−1h | "…queued already…" (no deferral sentence) | #16 `queued` |
| #13 auto | `next tuesday` | **refused**: `not_before "next tuesday": want a date (2026-10-14) or an RFC 3339 time (2026-10-14T09:00:00Z)` | nothing filed |
| #17 human | tomorrow | **"It is queued already -- your own task's permission covers it -- so it is dispatched in its turn without anybody approving it."** | #18 `deferred`, **unapproved** (finding 2) |
| #17 human | now+2m | same wrong sentence | #19 `deferred`, **unapproved** |
| #17 human | now−1h | "It remains unapproved until a human approves it." | #20 `proposed` |
| #17 human | `next tuesday` | refused, same message | nothing filed |

- The task pane showed "Picks itself back up on 10/4/2026, 12:00:00 AM." on #14 and #18, and "…12:42:44 AM." on #15 and #19 (browser in UTC). It gave no hint on #16 or #20, which offer Defer instead.
- Before the fix, nothing in the pane told #14 (wakes onto the queue) apart from #18 (wakes into proposals) (finding 2).
- **Wake.** `TestANearFutureNotBeforeWakesIntoTheStateItWasFiledIn` covers it:
  - Two tasks were filed through `ui.Client.CreateTask` with a wake time 2 minutes ahead.
  - At +1m the cycle's `wake` reconciler left both `deferred`.
  - At +3m the approved task was `queued` and the unapproved one was `proposed`.

### Step 5: `POST /api/tasks` with `deferredUntil` as a member

`bob` is a `member` of team `solo` and not a deployment admin.

- `deferredUntil` set to tomorrow 09:00Z returned 201: #21 `deferred`, `deferredUntil 2026-10-04T09:00:00Z`, creator `bob`, team `solo`. The pane (as bob) showed "Picks itself back up on 10/4/2026, 9:00:00 AM."
- `2020-01-01T00:00:00Z` returned 201: #22 `queued`, no `deferredUntil`. Filed normally.
- `next tuesday` returned 400 `parsing time "next tuesday" as "2006-01-02T15:04:05Z07:00": cannot parse "next tuesday" as "2006"`.
- `2026-10-04` returned 400 `parsing time "2026-10-04" as "2006-01-02T15:04:05Z07:00": cannot parse "" as "T"` (observation 1).

## Findings

1. **Capacity pane silently drops an out-of-range value and keeps showing it.**
   - Repro: Team settings → Capacity, type `21` (or `-1`) in Merge batch size, press Tab.
   - No request is sent and no message is shown, and the box goes on reading 21 while the stored value is 4, until a reload.
   - The same is true of the three other Capacity boxes for negative numbers.
   - **Fixed** (UI only).
2. **`propose_task` tells the agent a deferred, unapproved proposal is queued.**
   - Repro: from a run whose task is not auto-merge, call `propose_task` with `not_before` in the future.
   - The reply says "It is queued already … without anybody approving it", but the task waits for a human (no approval). The undeferred equivalent correctly says "It remains unapproved".
   - Cause: `daemonProposalFiler` read `Queued` as `state != proposed`, and a deferred task reads `deferred` whatever its approval.
   - In the same way, the task pane could not say whether a deferred task wakes onto the queue or back into proposals.
   - **Fixed.**

3. **e2e state leaking between files on a shared demo (found in this branch's own CI).**
   - The first version of the new spec added `bob` to a team with tasks. `system-settings-scoped-access-walkthrough` ran later on the same worker's demo, and it expects bob to belong to no team. It failed: `.task-row` count 1, want 0.
   - Fix: the new spec now asks for a demo of its own (a distinct worker-scoped `demoArgs`) and uses names no other spec does.
   - Giving it a worker reshuffled which files share a demo. That exposed `suites_workflows` choosing the Run-a-suite repo by position (`{ index: 1 }`): with `acme/innovate-app` added earlier by `non-admin-team-setup-walkthrough`, `POST /api/suite-runs` answered 400.
   - This is very likely the "suites_workflows e2e flake" the 2026-10-02 plan notes.
   - Fix: it selects `acme/widgets` by name. `main` landed the same fix independently (`7c21504e7`), and the merge takes `main`'s version.

### Observations (not fixed)

1. `POST /api/tasks` `deferredUntil` answers a bad value with Go's raw time-parse error. It also refuses a bare date (`2026-10-04`) that `propose_task not_before` accepts. `POST /api/tasks/{id}/defer` says it more plainly ("is not an RFC 3339 time"). The docs do say `deferredUntil` is RFC 3339, so this is a consistency nit, not a bug.
2. The Capacity pane does not say what number "0 — the deployment's batch size" currently means for the team.
3. By design (977026938, "ends collection"), one size-1 team's change near the head of the queue stops every batch until it has merged. In the interleaved run no batch formed at all. Worth knowing when giving one team batch size 1 on a busy repo.
4. In the demo a deferred task whose wake time has passed stays `deferred`, because the demo runs no `reconcileWake`. A walkthrough that wants to watch the wake in a browser needs a demo with a clock or a wake loop.

## Fixes on this branch

- `pkg/ui/tasks.go`: `Task.Approved` (`approved`, from `ApprovedAt`) is now on every task read.
- `cmd/grain/mcpserver.go`: `daemonProposalFiler` sets `FiledProposal.Queued` from approval. It falls back to the state, other than `proposed` or `deferred`, for a daemon that does not report approval.
- `ui/src/components/detail/Actions.jsx`: the deferred hint now ends ", onto the queue." or ", back into the proposals for someone to approve."
- `ui/src/components/TeamCapacityPanel.jsx`: a refused value is marked invalid with "Not saved: use a whole number from 0 to 20." (or "…0 or more."), and a good value clears it. A number box whose content is not a number (`validity.badInput`) is refused rather than read as an empty 0.
- `docs/schedules.md`: says how approval shows for a deferred task.
- `ui/e2e/suites_workflows.spec.js`: picks the suite run's repo by name (finding 3). This is now identical to `main`'s `7c21504e7`.

Grain CI passed the full suite on `2f6a651` (run `346f69a1c301817f`): go, ui e2e, frontend, postgres, installer, lint, package cache and terraform.

### Regression tests

- `cmd/grain/proposal_not_before_test.go` uses a real `ui.Server` and the real MCP tool and filer. It fails on `main` with the wrong sentence and passes with the fix.
- `ui/src/components/TeamCapacityPanel.test.jsx` and `ui/src/components/detail/Actions.test.jsx` cover the two UI changes.
- `ui/e2e/merge-batch-not-before-walkthrough.spec.js` is the browser half of steps 1 and 5 against the real demo.
- The walkthrough scenarios are kept as tests: `pkg/orchestrator/mergebatch_walkthrough_test.go` and `pkg/orchestrator/notbefore_walkthrough_test.go`. These are test-only, with no orchestrator code changed.

The `cmd/grain/mcpserver.go` change is on the MCP filing path. If the trust-boundary v2 rule covers it, it will be held for security review, which is expected.
