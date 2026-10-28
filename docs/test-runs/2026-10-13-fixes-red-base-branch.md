# Walkthrough: mark a task as fixing a red base branch (2026-10-13)

Executes §1 of `docs/test-plans/2026-10-13.md` (task 3547, filed by nightly test-plan task 3492).

## Environment

- Base: `candidate` at `4f7f76858`. `git branch -r --contains` puts `6d3587d75`, `4b9fe38eb` and
  `19c83cad9` on `origin/candidate` (and `origin/main`).
- Server: `grain demo -addr 127.0.0.1:18420 -open=false -github-sim -fake-ci -merge-queue-states`. The
  binary was built with `-tags grain_e2e`, as `ui/e2e/global-setup.js` builds it, over
  `cd ui && npm ci && npm run build`. Store: the demo's own embedded SQLite.
- Browser: Playwright 1.62.1, Chromium (headless shell 151.0.7922.34), driven by node scripts against
  the demo. The browser needs `localStorage["grain.username"]` set, as the e2e fixtures' pages
  already have it, or the first-visit sign-in dialog covers the page. Admin requests send
  `X-Grain-Admin-Mode: on`. Viewer, proposer and member requests sign in with a real
  `/api/auth/login` session and send no admin header.
- Installing Playwright's Chromium needed `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`.
  Without it the CDN download failed with `SELF_SIGNED_CERT_IN_CHAIN`, because the package cache
  re-signs TLS.
- **The orchestrator half:** `grain demo` runs no orchestrator cycle; `-reconcile` only syncs
  releases and suites. So nothing in it turns a branch red, links waiters, files a repair or closes
  one. Steps 2, 3, 4 (counting as the repair) and 8 used a scratch `package orchestrator` test (not
  committed). It opened the running demo's SQLite store from a second process, as
  `ui/e2e/releasehelper` does, and called the real `fileBaseBranchRepair`,
  `linkBrokenBaseDependents` (dispatch's own step), `closeBaseRepairsOnGreen` and
  `relayProposedTasks` on it. The browser and the HTTP API read the result from the live demo. See
  "Stubs" for the one thing it wrote itself.

## Steps

| # | Step | Outcome |
|---|------|---------|
| 1 | Start the demo and Playwright | Pass. Starts in about 1s; `/api/sandboxes` returns 200. |
| 2 | Make `main` red | Pass, but simulated (see Stubs). Grain filed repair #16 itself, by the real `fileBaseBranchRepair`: "filed 16 to fix acme/widgets main, red at 82f8ef967733". |
| 3 | Ready tasks on `main` get a `base-ci-wait:` link to grain's repair | Pass. #2, #12 and #13 got `depends-on 16` and tag `base-ci-wait:16`. #11 was already blocked on #8, so it was not ready and was not linked. The pane shows "depends on 16 …" and blocked. |
| 4 | New task with "This fixes a red main" ticked | Pass. The checkbox label reads "This fixes a red main". The POST carried `"fixesBaseBranch":true` and #17 was created marked. Linking left #17 unlinked and commented once that grain's #16 runs alongside it. With #16 closed by a person and `main` red at a new commit, grain filed **no** repair: the row named #17 as `RepairTaskID`, #12 and #13 waited on #17, and pat's unapproved marked proposal #18 was passed over. |
| 5 | Mark an already-waiting task from the pane | Pass. Clicking the box on #2 sent `PATCH {"fixesBaseBranch":true}` and got 200. The `depends-on 16` link and the `base-ci-wait:16` tag were removed. The pane shows the comment "Marked as fixing its red base branch, so this task no longer waits on the branch's repair: removed its dependency on task 16." |
| 6 | Roles | Pass. Viewer `vic` sees no checkbox, and their PATCH gets 403 "you can read team "default"'s tasks but not change them". Proposer `pat` sees no checkbox on #13 or on their own proposal #18, and their PATCH gets 403. Member `mo` sees an enabled checkbox. Viewer create gets 403. Proposer create with `fixesBaseBranch:true` gets 201 as an unapproved proposal, which step 4 showed does not count as the repair. |
| 7 | Unmark grain's own repair | Pass. The pane's box is checked and disabled. `PATCH {"fixesBaseBranch":false}` gets 400 "16 is grain's own repair of its base branch, so it cannot be unmarked: close it instead", as admin and as member. (Finding 2 came up here.) |
| 8 | Green: grain's repair closes, marked task stays open with one comment, waiters released, person's link stays | Pass. On `main`: #17 stayed open with one green comment ("… The 2 task(s) grain had waiting on it … have been released"). #12 and #13 lost their links to #17. #11 kept the `depends-on 17` a person added through `POST /api/tasks/11/depends-on` and stays blocked on it. Grain's own #16 had already been closed by hand, so grain closing its repair was walked on `release/1.0`: repair #21 closed as superseded, and its waiter #20 was unblocked (`blocked: false`). |
| 9 | Retarget a marked task to `candidate` | Pass, after creating the branch. The simulated forge has no `candidate`, so the PATCH got 400 `base "candidate" does not exist on acme/widgets`. Once `refs/heads/candidate` was added to the sim's bare repo, the same PATCH moved #17's tag to `base-ci-repair:acme/widgets:candidate`. On `release/1.0` the pane label read "This fixes a red release/1.0". |
| 10 | Overview states the unjudged gate once | Pass. `-fake-ci` seeds a row for `main`, so the plan's `grain-ci-off` gate (no row) cannot occur. The other unjudged gate, `judged-nothing`, was made with an errored run 3h old; `/api/base-branch-gates` returns it with `judged:false`. On `/repos/acme/widgets` the sentence appears once, in `.repo-page-header`, with none in `.repo-page-body`. Releases shows it once. |
| 11 | `propose_task` with `fixes_base_branch` | Pass for the relayed path. `pkg/mcp`'s real `propose_task` tool (`NewMockTools`) accepted the call ("Recorded (1 proposed task(s)…"). `relayProposedTasks` filed it under parent #3 as #22: proposed, `fixesBaseBranch: true`, tags `base-ci-repair:acme/widgets:main fixes-base-branch`. The pane shows the box ticked. The live path (`pkg/daemon`'s filer passing `FixesBaseBranch` into `ui.CreateTask`) was read rather than driven; its create half is the same `CreateTask` step 4 drove. |

Also observed, for the plan's "What is NOT proved" list: neither the Inbox nor the repository page
mentions a red base branch at all, whoever is repairing it. The wait shows only on the waiting task,
as "depends on #N … (open)" and blocked, and on its pull request's grain CI status.

## Findings

### 1. A marked task's run was told not to fix the base fault it was marked to fix (fixed)

`RunDispatch` added this note to every run on a red base unless the task was the CI row's
`RepairTaskID`:

> NOTE: Base branch `main` currently has an active incident / failing CI (task 16 is repairing it).
> Do not attempt to fix shared base faults locally; focus only on your change.

The feature's own headline case is a marked de-flake task running alongside grain's repair (step 4:
#17 beside #16). That task, and any marked task the row does not name (#2 while the row named #17),
got this note in its prompt. So did grain's own repair from an earlier commit.

**Reproduction:** red `main` with grain's repair as the row's `RepairTaskID`, and a marked task
`deflake` on `main`. `baseIncidentNote` for `deflake` (formerly the inline block in `RunDispatch`)
returned the note. `TestBaseIncidentNoteIsNotForARepairOfTheBranch` fails on the old condition with:

```
older-repair: told to leave main's fault alone = true, want false (note "NOTE: Base branch `main` currently has an active incident / failing CI (task grain-repair is repairing it). …")
```

**Fix:** the note is now `baseIncidentNote` in `pkg/orchestrator/run.go`. It skips any repair of the
branch (`isBaseRepair`: the branch tag, grain's or a person's, or the row's id). The same
`RepairTaskID` comparison in the merge queue's head handling (`advanceMergeQueueHead`'s
`baseIsBroken`, `pkg/orchestrator/sync.go`) skipped the base refresh for any repair but the row's. It
now uses `isBaseRepair` too. That one-line change has no test of its own; it uses the same predicate
the new test pins.

### 2. Editing the base of grain's own repair moved it onto another branch (fixed)

`UpdateTask` re-marks a marked task when its repo or base is edited, so that a person's mark follows
the base. It did the same to grain's own repair. Grain's repair of `main` became grain's repair of
the new base. A green `main` then no longer found it to close (`closeBaseRepairsOnGreen` reads the
branch tag), so the work grain had linked to wait on it stayed blocked until somebody closed it by
hand.

**Reproduction** (live demo, step 7):

```
PATCH /api/tasks/16 {"base":"release/1.0"}   -> 200
tags: [base-ci-repair:acme/widgets:release/1.0 base-ci:acme/widgets:main:82f8ef96…]
```

`TestEditingTheBaseOfGrainsOwnRepairKeepsItsBranchTag` (`pkg/ui/fixes_base_branch_test.go`) fails
without the fix with `tags [base-ci-repair:acme/widgets:release-1 base-ci:acme/widgets:main:bad0000]
after moving grain's repair of main to release-1, want main's repair tag kept`.

**Fix:** a base edit re-marks only a person's mark. Grain's own repair keeps the branch it was filed
for, and ticking it again is a no-op.

### 3. The plan's claim that `pkg/ui/fixes_base_branch_test.go` covers permissions (not a code bug)

That file tests create, PATCH, the dropped wait, the moved mark and the refused unmark, but no roles.
The role checks in step 6 hold because `UpdateTask`'s existing proposer and viewer authorization
applies to every field. No test pins it for `fixesBaseBranch` specifically.

### 4. Smaller observations (not fixed)

- On green, every open task carrying the mark gets the "grain has left it open" comment, including
  an **unapproved** proposal that never counted as the repair (#18 and #22 here). It is noise rather
  than harm.
- `base-ci-green-noted` survives a retarget. A marked task moved to another branch that is already
  green when it goes red will only comment after that branch has been red once (`forgetGreenNoted`).
- A Director console session (#12, `console-session:director`) is a ready task, so it was linked to
  wait on the base repair like any other. This predates the feature.
- A person closing grain's repair leaves its waiters' `base-ci-wait:<id>` tag and the link to the
  closed task in place. It is harmless: a closed dependency blocks nothing.

## Fixes and their tests

| Fix | Test |
|-----|------|
| `baseIncidentNote`: no "leave the base fault alone" note for a repair of the branch | `pkg/orchestrator/baseincident_internal_test.go`, `TestBaseIncidentNoteIsNotForARepairOfTheBranch` |
| `advanceMergeQueueHead`: `baseIsBroken` uses `isBaseRepair` | same predicate as above; no separate test |
| `UpdateTask`: grain's own repair keeps its branch tag on a base edit | `pkg/ui/fixes_base_branch_test.go`, `TestEditingTheBaseOfGrainsOwnRepairKeepsItsBranchTag` |

## Stubs

- **Forge:** the demo's `-github-sim` (`githubsim.Sim` over a bare repo holding `main` and
  `release/1.0`). `refs/heads/candidate` was added to that bare repo by hand for step 9.
- **CI:** the demo's `-fake-ci` (`demoFakeCI`), which never concludes a run. So the base-branch
  verdict was written by the scratch driver into the demo's `base_branch_ci` row: a confirmed
  `failure` for red, `success` for green, and an `errored` run started 3h ago for step 10's gate.
  The orchestrator steps then ran unmodified against that row.
- **Agent:** step 11's `agent.Result` (one `propose_task` tool call) was constructed rather than
  produced by a model. The tool's own handler and grain's relay were real.
- Nothing of grain's own was mocked: no API routes, no React.

## CI verdict

Full suite **success**: grain CI run `ed8f643789a8c75e`, at `1d595839b`. That commit is `c73c1d6a5`,
the fixes plus this report, merged with `candidate` at `a4674d57b`. Jobs: frontend, go, installer,
lint, package cache, postgres, relay image, run host, terraform and ui e2e all passed. The commit
that adds this paragraph changes only this file.
