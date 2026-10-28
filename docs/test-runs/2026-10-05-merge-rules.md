# Path-scoped merge rules walkthrough (2026-10-05)

Plan: `docs/test-plans/2026-10-05.md` §2 (`.grain/merge-rules.yml`). Task: grain/task-1993.

## Environment and tested commit

- Base `candidate` at `20d7a78ce` when the walk ran (first attempt, 13:46-14:10 UTC). The branch was
  later merged with `candidate` at `d05659203`, which is the commit CI judged.
- `git branch -r --contains` lists `origin/candidate` for all three feature commits: `507b850a0`,
  `c18447aac` and `687b27821`. It also lists `origin/candidate` for the tested base `20d7a78ce`.
- Debian sandbox, Go toolchain from `go.mod`, Node/npm from `ui/`. Playwright Chromium from
  `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, running against the production bundle built by
  `npm ci && npm run build`.

## Commands

```
go test ./tests/e2e -run TestMergeRulesWalkthrough -count=1 -v
go test ./pkg/model ./pkg/forge ./pkg/orchestrator -count=1
cd ui && npx vitest run src/components/DetailOverlay src/components/detail
cd ui && npm run build && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
  npx playwright test e2e/merge-rules-walkthrough.spec.js
cd ui && npm run format;  gofmt -l;  make security
```

## Stubs

- **GitHub:** githubsim, through the e2e GitHub host server. Check runs (`build`, `cla/google`)
  are seeded with `CheckRuns`.
- **Grain CI executor:** `rulesCI` in the test. It answers every run at once with the jobs `lint`,
  `test` and `ui e2e`, each green unless the case scripts a red, and a branch can be scripted to
  hang. A recheck reruns only the jobs it is asked for, as `ci.Service` does. Everything that reads
  a verdict is real: narrowing, rechecks, repairs, batching, stalls and ports.
- **Model sessions:** a scripted antigravity agent, which edits files and pushes.
- Nothing of grain's own code is stubbed. The orchestrator, merge queue, declaration sync,
  `ui.Server`, `grain mcpserver` subprocess, `grain demo` and React are all real.
- The demo states for step 5 are seeded through the store (`grain demo -merge-rules`), using the
  queue's own policy computation and comment text (`orchestrator.MergePolicyNote`).

## Outcomes

`tests/e2e/merge_rules_walkthrough_test.go` uses the docs/declarations.md example verbatim. It
syncs the rules off a real git server.

| Step | Case | Result |
|---|---|---|
| 1 | Docs-only PR with `test` red merges on `lint` alone | Pass after fix F1 |
| 1 | `docs/**` + `pkg/**`: `ci.grain: full suite -- from defaults`; red `test` holds it | Pass |
| 1 | `deploy/**`: auto-merge withdrawn, with the note "`auto_merge: never` (from `deploy`)" | Pass |
| 1 | `deploy/**`: the queue row names "`cla/google` (required by `deploy`)" | Pass after F2 wording fix |
| 1 | `deploy/**`: merges after an owner's Submit once `build` and `cla/google` are green | Pass |
| 1 | `stall_timeout: 30m`, grain CI hung: one automatic retry, then a person | Pass |
| 1 | `stall_timeout: 30m`, `cla/google` queued: handed to a person at 30m, with no retry | Pass, but see F9 |
| 1 | `batching: alone`: risky merges alone, and the two docs PRs behind it merge in one batch | Pass after fix F3 |
| 1 | `repairs: 0`: blocked for a person, no fix task filed, note names rule `fragile` | Pass |
| 1 | `flake_recheck: false`: no recheck run, and the PR does not merge on the red | Pass |
| 1 | `port: false`: log says "not porting … its merge rules say `port: false` (from rule `local`)"; no port task. Other merges still file ports. | Pass |
| 2 | A PR editing `.grain/merge-rules.yml` is held under `deploy, merge-rules` and judged by the base's copy. bob's Submit is refused; the owner's releases it. | Pass |
| 3 | Broken YAML on the base: sync error names the file, last good rules are kept, and a docs PR is held under `unparseable-triggers` | Pass, but see F6 (fixed since) and F7 |
| 4 | Both files present: `merge-rules.yml` wins, and the warning is stored and logged | Pass (F5 fixed) |
| 5 | `grain demo -merge-rules` in Playwright: the task pane lists every requirement with its rule, the comment is in the timeline, Submit is offered on the waiting change, and Retry merge on the stalled one | Pass after F8 UI addition |
| 6 | `query_merge_queue` through a real `grain mcpserver` against a real `ui.Server` | Pass, see F10 |

Text recorded in step 6 (`query_merge_queue`):

```
1. (Task 2): Deploy change
   Repo: acme/widgets | Branch:  | State: `waiting_ci`
   Note: waiting on required GitHub check(s) not reported on this commit at all: `cla/google` (required by `deploy`)
   Merge rules:
     - `ci.mode: grain` -- from repository setting
     - `ci.mode: both` -- from rule `deploy`
     - `auto_merge: never` -- from rule `deploy`
     - `stall_timeout: 30m` -- from rule `deploy`
     - `ci.grain: full suite` -- from rule `deploy`
     - `ci.github: build` -- from rule `deploy`
     - `ci.github: cla/google` -- from rule `deploy`
     - GitHub CI is narrowed to `build`, `cla/google`: every rule this change matched names its checks
```

Text recorded in step 5 (browser):
- **List row:** "Roll the ingress controller to v2 · Waiting for CI · 20m ago". The queue's note is
  in the tooltip only.
- **Task pane:** "Merge rules (matched deploy) for a1b2c3d4e5f6:". Then one line per requirement,
  from "ci.mode: grain — from repository setting" to "ci.github: cla/google — from rule deploy", and
  "GitHub CI is narrowed to build, cla/google".
- **Timeline comment:** "Merge rules: .grain/merge-rules.yml at 5e1d2c3b4a59 matched rule(s) deploy
  for this change's head a1b2c3d4e5f6. It needs: …"

## Findings

No earlier report in `docs/test-runs/` covers any of these.

- **F1 (fixed): a narrowed suite was never read through a flake recheck.**
  - *Symptom.* A docs-only PR (narrowed to `lint`) with `test` red went to repair, and then to a
    person.
  - *Cause.* The recheck reruns only the failing jobs, and its run id replaces the verdict's.
    `narrowedGrainCI` then read a run with no `lint` in it.
  - *Repro.* `TestMergeRulesWalkthroughDocsAndMixed`, or the unit test
    `TestANarrowedSuiteIsReadThroughAFlakeRecheck`.
  - *Fix.* `verdictRunOf` lays the recheck over the run it rechecked (`pkg/orchestrator/mergerules.go`).
- **F2 (fixed): under `ci.mode: both`, waits said "no check reported against it at all".**
  - *Symptom.* `cla/google` was queued, and `build` green, while the row and the stall hand-off
    said nothing had reported.
  - *Cause.* A required grain verdict set GitHub's checks aside.
  - *Fix.* `forge.GrainCI.GitHubToo` and `readsGitHubChecks` name GitHub's checks in that case. A
    required check missing from the commit is now the whole of the wait.
  - *Repro.* `TestMergeRulesWalkthroughDeploy`, or `TestABothModeWaitNamesGitHubsUnfinishedChecks`.
- **F3 (fixed): merge rules switched batching off after any merge.**
  - *Cause.* `repo_merge_rules.commit` was restamped with every new base head. Every recorded
    classification is checked against that commit, so each merge invalidated them all.
  - *Effect.* The queue re-posted the "Merge rules" note on every open PR. It also refused to batch
    them (`batchLimitOf` reported "unknown") and merged the head alone.
  - *Fix.* `putMergeRules` keeps the stored commit when the rules are unchanged.
  - *Repro.* `TestMergeRulesWalkthroughQueueOptions/batching_alone`, or
    `TestUnchangedMergeRulesKeepTheirCommit`.
  - A no-rules control batched all three PRs, before and after the fix.
- **F4 (fixed): durations in the policy text.** It said `stall_timeout: 30m0s`. It now says `30m`,
  covered by `TestMergePolicySaysWhichRuleCausedEachRequirement`.
- **F5 (fixed, grain/task-2107): merge-rules warnings were visible nowhere but the daemon log.**
  - This covers the both-files warning and the `ci.grain` names no workflow declares.
  - They were stored in the rules JSON, but no API or UI showed them, and the repo's declaration
    status carried `LastError` only.
  - The declarations status now carries `mergeRules` and `warnings`, and the Repos page lists each
    warning. `TestMergeRulesWalkthroughTheFileItself/both_files` asserts the warning is on it.
- **F6 (fixed by task 2108): fixing a broken base did not release the changes it held.**
  - The `unparseable-triggers` hold is an owner-release hold, and nothing judged it again once the
    base parsed. The plan, and docs/declarations.md ("until it's fixed"), expect the fix to release
    them.
  - *Fix.* A change held only for the broken rules is judged again at the same head under the fixed
    rules once they parse (`releaseFixedRulesHold` in `pkg/orchestrator/repoguard.go`). If they hold
    nothing, it merges itself.
  - The decision stays an owner's when another owner category held it, when its head moved, when a
    review left findings, or when an owner had already decided.
  - *Repro.* `TestMergeRulesWalkthroughTheFileItself/a_broken_base` now asserts the release.
- **F7 (fixed): the broken-rules hold credited the wrong file.** The category read "(built in, from
  this repository's `.grain/triggers`)" beside an error naming `merge-rules.yml`. It now names both
  files, asserted in the same subtest.
- **F8 (fixed): the task pane did not show the policy.** The task API's `mergePolicy` was rendered
  nowhere, so the requirements were only in a comment that scrolls away.
  - *Fix.* A new `MergePolicy` section in the detail pane, with its own Vitest.
  - The demo seed `-merge-rules` and the spec `ui/e2e/merge-rules-walkthrough.spec.js` cover it.
- **F9 (follow-up): a stalled GitHub check gets no retry.** docs/declarations.md says
  `stall_timeout` "retries it once and then hands it to a person". The retry (`retryStalledCI`)
  exists only for grain CI. A stall on GitHub checks goes straight to a person. *Repro:* the
  `cla/google` queued case in `TestMergeRulesWalkthroughDeploy`.
- **F10 (follow-up): `query_merge_queue` uses the wrong branch.**
  - It prints `Branch:` empty, and filters by the task's `Base`, which is empty for default-branch
    work.
  - So `branch: main` drops those PRs (`daemonMergeQueue` in `cmd/grain/mcpserver.go`).
- **Not walked:** `ci.mode: github` and `both` on an `ignore_github_ci` repository, beyond the repo
  floor appearing as `ci.mode: grain -- from repository setting` under `both`.

## Fixes on this branch

- `pkg/orchestrator/mergerules.go` (F1).
- `pkg/forge/health.go`, `pkg/orchestrator/sync.go` and `queuestatus.go` (F2).
- `pkg/model/merge_rules_store.go` (F3).
- `pkg/model/merge_rules.go` (F4).
- `pkg/orchestrator/repoguard.go` (F7).
- `ui/src/components/detail/MergePolicy.jsx` and `cmd/grain/demo_merge_rules.go` (F8).
- The `pkg/orchestrator` changes are trust-boundary paths and are expected to be held for security
  review.

## Checks

- `gofmt` is clean.
- `npm run format` made no changes.
- `make security` passed: `go vet ./...` and `tests/security` with `-race`.
- `go test` of `pkg/model`, `pkg/forge`, `pkg/orchestrator` and the walkthrough passed.
- Vitest for the detail pane passed: 189 tests.
- The Playwright spec passed: 2 tests.
- The first grain CI run on `fc706e3` failed on `go`/Format only, because this test file was
  unformatted. Every other job passed. The grain CI verdict for the final commit is in the task's
  closing note.
