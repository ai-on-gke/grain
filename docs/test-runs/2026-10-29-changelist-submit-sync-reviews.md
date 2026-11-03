# 2026-10-29: Changelist tasks II — Submit, Sync, landing, the cl-reviews gate and the `cl-submitted` goal

Task 1796 (`grain/049f1e09/task-1796`): §2 of
[`docs/test-plans/2026-10-29.md`](../test-plans/2026-10-29.md) ("Changelist
tasks II"), PRs #114 and #119. It walked a CL task's Submit and Sync in the
browser, landing, outside-grain submission and abandonment through the
poll, the `SyncPlanner`, the `cl-submitted` goal, the `cl-reviews` gate
over snapshots and the agent refusals. It ran against a real `grain demo`
and its built frontend, and drove the rest with Go tests over the real
`ui.Client`, the orchestrator and the demo's own CL provider where the
demo cannot get there itself (see "What the demo cannot do").

## Commit and environment

- **Base tested**: `2f53e63a` (`main`, "Merge pull request #130 from
  ai-on-gke/grain/049f1e09/task-1732"). The fixes below were checked again
  in the browser against a demo rebuilt from this branch.
- **Build**: `cd ui && npm ci && npm run build && cd .. && go build -o
  /home/debian/bin/grain ./cmd/grain`. Vite writes the bundle to
  `pkg/ui/static`, which the binary embeds.
- **Server**: `grain demo -github-sim -fake-ci -reconcile 2s -addr
  127.0.0.1:18080 -open=false -access-code-file /var/tmp/walk/code`, on a
  throwaway embedded store. The plan's `-sandbox-port-base=0` is not a
  `grain demo` flag (`flag provided but not defined`). It is a `grain
  daemon` flag, and the plan is corrected here. API calls used `Authorization: Bearer
  <access code>`, which the demo reads as the person `debian`.
- **Browser**: Playwright (`ui/node_modules/playwright`) driving Chromium
  `chromium-1234` from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`. It
  signed in through `POST /api/auth/access-code`. Widths were 1440×900 and
  390×844, and the Metrics shots were taken in light and dark. The scripts
  were ad hoc Node files kept outside the repository (`/var/tmp/walk/submit.mjs`,
  `/var/tmp/walk/shoot.mjs`). What they found is pinned in
  `pkg/demo/demo_changelists_test.go`, `ui/e2e/cl-submit.spec.js` and
  `ui/src/components/MetricsSections.merges.test.jsx`.
- **Toolchain**: Debian, Go (`/usr/local/bin/go`), Node v20, Vitest 4.1.
- **Stub**: the only stub is the demo's in-process change provider
  (`demoChangelists`, `pkg/demo/demo.go`). Nothing left the process.

## What the demo cannot do

The demo's reconcile loop (`runDemoReconcile`) runs only the release and
suite arms. The goal (`task-goals`), `cl-reviews`, `review-questions` and
change-poll arms never run in it. Its provider is neither a
`change.Poller` nor a `change.ReviewFiler`, so it never polls and never
files a review. A goal therefore never flips in the running demo, no CL
review is ever filed, and nothing is "submitted outside grain".
`demoChangelists` is not a `change.SubmitGranter` either, so a demo Submit
records no capability grant to release.

Those parts were driven by `pkg/demo/demo_changelists_test.go`. It seeds the
real demo (`seedDemo`) into a fresh store and acts on the same seeded tasks
and CLs: "Move the depot retry budget behind a flag" (`cl/612400101`),
"Sync the depot retry budget with head" (`cl/612400105`), "Port the retry
budget to the depot" (`cl/612400001`) and "Add a TAP project for the retry
budget" (`cl/612400102`). Each test runs through `ui.Client` and the
orchestrator's own reconcilers (`orchestrator.Reconcilers()`, picked by
name), `orchestrator.ProcessResult` and `orchestrator.PollChangesOnce`.
For the review and poll checks the demo provider is wrapped by
`reviewingDemoCLs`, which embeds `demoChangelists` and adds `ReviewGrant`,
`SaveReviewFindings` and `PollTick`, as an edition's provider would.

## Outcome

| # | Plan item | How | Outcome |
|---|---|---|---|
| 1 | Identify the three seeded CL tasks | `GET /api/tasks/{13,14,18}` → `clSubmit` | Pass. 14 submittable. 13 blocked: review changes requested, presubmit failed on snapshot 3. 18 behind head. |
| 2a | Blocked CL lists every reason under the buttons (1440, 390) | Browser | Pass |
| 2b | Inline inbox Submit only on the submittable CL (1440, 390) | Browser | Pass |
| 2c | Sync on the behind CL; badge clears after | Browser | **Fail on base**: the badge never cleared. Fixed (F4). Pass after. |
| 2d | Submit the submittable CL; the task closes merged | Browser + API | Pass. Closed, `PrMergedAt` set (metrics `landed: 1`). |
| 2e | Merges metrics count it under the provider's forge | `GET /api/metrics` | **Fail on base**: no by-forge figure on the wire or the page. Fixed (F3). Pass after: `landedByForge: {"demo": 1}`. |
| 2f | Submit grant released | API + existing tests | Not reachable in the demo: its provider names no Submit grant. `GET /api/tasks/14` shows no grant after Submit. Release is covered by `pkg/ui` tests (`78a12901`). |
| 3 | `cl-submitted` goal met after the landing | Go test (demo cannot) | `{task}` goal: pass. `{cl: N}` goal: **fail on base** (demo provider never read as submitted). Fixed (F4). The plan's `{"cl": "<n>"}` string is refused with a 400. |
| 4a | A finished CL gets its `cl-reviews` review on the next cycle | Go test | Pass (`LinkCLReviewOf` on the review task, `LinkCLReviewFiled` on the reviewed one). |
| 4b | Submit refused while the review is open | Go test | **Fail on base** where the provider files reviews but has no `ReviewGate`. Fixed (F1). |
| 4c | Finding → open question, drafts saved, Submit still refused | Go test | Pass |
| 4d | Owner's answer releases the hold | Go test | **Fail on base**: never released for a CL task. Fixed (F2). |
| 4e | New snapshot re-files, clears nothing carried over; clean review lets Submit through | Go test | Pass after F1 and F2 |
| 5a | CL submitted outside grain → task closed merged | Go test (poll) | Pass |
| 5b | CL abandoned → task with no live CL back to `awaiting_reply` | Go test (poll) | Pass |
| 5c | `SyncPlanner` does not sync during a live run | Go test (poll over a running task) | Pass (0 syncs) |
| 6a | Agent `POST /api/tasks/{id}/submit` and `.../change-sync` | HTTP against the demo with `X-Grain-Acting-Task` | Pass. 403 in every case (below). |
| 6b | `review_of` refused by a proposal | HTTP + existing tests | Pass |

## Steps and observations

### Browser: blocked, inbox, Sync, Submit (1440 and 390 px)

- **Blocked (task 13)**: Submit was disabled. The caption under the
  buttons read "Not submittable yet: review is not approved (changes
  requested); required presubmit has not passed on snapshot 3 (failed)."
  It matches `clSubmit.reasons` exactly. Sync was enabled. There was no
  horizontal overflow at either width.
- **Inbox**: the submittable row (14) had one inline Submit. The blocked
  (13) and behind (18) rows had none. There was no overflow.
- **Behind (task 18)**: the "behind head" badge showed and Submit was
  disabled with "behind head: sync it". Pressing Sync posted "Synced
  cl/612400105: synced to head (demo)." On base, after a reload, the badge
  and the reason were still there and `presubmit` still said `health:
  behind, snapshot 2`. The demo provider's `Sync` answered "synced" but
  recorded nothing, although its doc comment says "a sync reports a new
  snapshot". After the fix the badge was gone. `clSubmit.reasons` became
  "required presubmit has not passed on snapshot 3 (not run)" and
  `presubmit` became `{state: not_run, health: ok, snapshot: 3}`. A real
  provider's poll would read the new snapshot back the same way.
- **Submittable (task 14)**: Submit was enabled. Pressing it posted the
  person's "Submitted cl/612400101." and the provider's "debian submitted
  cl/612400101 (demo: nothing left this process).", and the task was
  `closed` within the first poll (<0.5 s). After a reload the Submit and
  Sync buttons were gone and there was no overflow. `GET /api/metrics`
  showed `merges.landed: 1`, with filed→landed and completed→landed
  distributions over the task (`n: 1`). On base, `landedByForge` was
  absent.

### `cl-submitted` goals

- `PUT /api/tasks/2/goal {"kind":"cl-submitted","spec":{"cl":"612400101"}}`
  (the plan's form) answered `400 json: cannot unmarshal string into Go
  struct field GoalSpec.spec.cl of type int64`. With a number it was
  accepted. `{"task":"14"}` was accepted on task 3.
- In the running demo neither goal ever gained a reading. The demo does
  not run the goal arm (see above).
- `TestDemoCLSubmitLandsAndMeetsCLSubmittedGoals` presses Submit through
  `ui.Client` and then runs `task-goals`. The `{task: 14}` goal was met on
  base. The `{cl: 612400101}` goal was not: "cl/612400101: 0/1 submitted;
  still waiting on cl/612400101 (unknown)". A CL-number goal asks the
  provider (`change.Observe`), and the demo provider answered with the
  observation it had before the Submit. After F4 both goals are met.

### Review gate over snapshots (`TestDemoCLReviewGateOverSnapshots`)

The submittable demo task was given a review template (`cl-review`), and
the provider was wrapped to file reviews.

1. Before any review, base allowed Submit: `clSubmit.canSubmit: true`.
   `change.GateSubmit` only gates through a provider's `ReviewGate`. A
   provider that only files reviews (`ReviewFiler`, with core falling back
   to `Task.ReviewTemplateID`) was never gated. Yet `fileCLReview`'s
   comment tells the owner "Submit waits until it has passed". After F1:
   "grain has not reviewed snapshot 1 yet".
2. One `cl-reviews` pass filed one review holding the provider's read-only
   grant, with `LinkCLReviewOf cl/612400101@1#cl-review`. Submit was then
   refused with "grain's review of snapshot 1 has not passed yet".
3. The review finished with one `add_review_comment` finding on
   `devtools/grain/retry/flag.go:12`. The finding was saved as a draft on
   the CL at that line, and the reviewed task was held on its question
   (`LinkReviewQuestions`). Submit was refused with "a grain review left
   questions open".
4. The owner replied "Default it to off." and `review-questions` ran. On
   base the hold stayed, permanently: `SyncReviewQuestions` walks only
   tasks with an open pull request link (`OpenPullRequestLinks`, i.e.
   `LinkFixes`), which a CL task never has. CL Submit refuses while the
   hold is there, and no other path removes it. After F2 the hold was
   released. The task was queued, and its run was told to "upload it as a
   new snapshot of its CL with open_pull_request" rather than "push to this
   task's branch".
5. Snapshot 2 was then observed and the run finished. Submit was refused
   with "grain has not reviewed snapshot 2 yet", because nothing carried
   over from snapshot 1. `cl-reviews` filed a second review of snapshot 2,
   and Submit was then refused with "...snapshot 2 has not passed yet".
   That review finished clean (`comment_on_issue` only) and cleared
   `@2`. Submit then went through and the task closed.

### Poll: outside landing, abandonment, no sync during a run (`TestDemoCLPollLandsAbandonsAndNeverSyncsDuringARun`)

One `PollChangesOnce` over the seeded demo tasks, with the wrapped provider
reporting `cl/612400101` submitted, `cl/612400102` abandoned and
`cl/612400105` behind. The behind CL's task had a real run started
(`store.StartRun`), so its state was `running`.

- Task 14 closed with `PrMergedAt` and the comment "cl/612400101 was
  submitted outside grain."
- Task 15 went to `awaiting_reply` with "…This task has no live CL left, so
  it is waiting on a person…".
- The provider's `Sync` was called 0 times for the behind CL with a live
  run. The planner's per-snapshot and after-failure rules are already
  pinned by `TestAutoSyncFiresOncePerSnapshotNeverDuringARunAndNotAfterAFailure`.

### Agent refusal

Against the running demo, as task 19 holding `api:tasks:write`
(`X-Grain-Acting-Task: 19`, `X-Grain-Channel: sandbox-cli`, with a reason):

| Call | Answer |
|---|---|
| `POST /api/tasks/18/submit` | 403 "an agent cannot turn on auto-merge or submit work: letting a change merge unreviewed is a person's decision" |
| `POST /api/tasks/18/change-sync` | 403 "an agent cannot mail a CL, publish its replies or sync it: who reviews a change, and when, is a person's decision" |
| `POST /api/tasks/19/submit`, `.../19/change-sync` (own task) | 403 "a run does not end, orphan or regrant its own task" |
| `POST /api/tasks` with a workspace grant naming `review_of` (object and string forms) | 403 "an agent cannot grant …, which its own task does not hold" |
| The same calls from a task without the scope (18) | 403 "holds no api grant for scope tasks:write" |

Task 18 stayed `awaiting_submit` throughout.
`TestDemoCLSubmitAndSyncRefuseAnAgent` pins the `ui.Client` side ("only a
person may submit/sync a CL"). The case where a proposer already holds a
review grant (so the escalation check passes) and still may not hand it on
is covered by `TestCreateTaskRefusesAProposalAskingForAReviewGrant` (live
`propose_task`), `TestAProposalCannotAskForAReviewGrant` (end-of-run
replay) and `pkg/mcp`'s mock tool test.

A person filing a task with an unconfigured capability URL got `400
capability https://caps.example/workspace is not configured in
mcpServers`. The demo has no workspace capability, so a person-filed
`review_of` grant could not be tried here.

## Findings, fixes and follow-ups

| # | Finding | Reproduction | Fix |
|---|---|---|---|
| F1 | CL Submit was not gated on grain's own CL review when the provider files reviews (`ReviewFiler`) but has no `ReviewGate`. Grain still told the owner "Submit waits until it has passed". | `TestDemoCLReviewGateOverSnapshots` fails on base: `canSubmit: true` before any review. | `change.GateSubmit` gates such a CL on `Task.ReviewTemplateID` having cleared the snapshot last observed (`LinkCLReviewCleared` for `target@snapshot#template`). It reads "grain has not reviewed snapshot N yet" or "grain's review of snapshot N has not passed yet". A provider with its own `ReviewGate` is unchanged. |
| F2 | A CL task held on its review's questions was never released by the owner's answer, so with CL Submit refused it was stuck for good. | Same test, step 4, on base: "the owner's answer did not release the CL task's review-questions hold". | `SyncReviewQuestions` also settles tasks whose change is a CL (when a CL provider is registered). The run it queues is told to upload a new snapshot. |
| F3 | Merges by forge (PR #114, `metrics.Merges.LandedByForge`) was computed but never reached `/api/metrics` or the Metrics page. | `GET /api/metrics` after a CL Submit: `merges` has no by-forge key. | `MetricsMerges.landedByForge` is now on the wire. The Landings section shows a "<forge>: N landed" chip row once anything has landed outside a pull request (`MetricsSections.merges.test.jsx`). |
| F4 | The demo provider forgot its own Submit and Sync: a synced CL stayed behind, and a submitted one never read as submitted, so a `{cl: N}` goal could not be met. | Browser 2c; `TestDemoCLSyncClearsBehind` and the goal test fail on base. | `demoChangelists.Sync` records a new snapshot, up to date with head, with presubmit not run. `Submit` records the CL submitted. `cl-submit.spec.js` checks that the badge clears. |
| D1 | The plan's demo flag `-sandbox-port-base=0` and its goal example `{"cl": "<n>"}` are wrong. | See above | Corrected in `docs/test-plans/2026-10-29.md`. |

**Screenshots** (`/system/metrics`, the Landings & Lead Time section, at
1440 and 390 px in light and dark, before on base and after on this
branch): the after row's box was `x=256, width=1160` at 1440 and `x=12,
width=366` at 390. Both are inside the viewport, and `scrollWidth −
clientWidth = 0` at every width. Before, there was no row. No e2e spec
asserts the row, because landing a CL on the shared worker demo would
change what the other CL specs read. The rendering is pinned by the unit
test.

**Not proved here**:
- A Submit grant actually released on a provider that names one. The demo's
  provider names none, so this rests on the `pkg/ui` tests.
- An edition's real `ReviewGate`. F1 changes nothing for a provider that
  has one.
- The goal arm, `cl-reviews`, `review-questions` and the change poll inside
  a running daemon's loop. They ran here as the orchestrator's own
  reconcilers called directly, over the demo's seeded store.
