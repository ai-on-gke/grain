# Test run: port settings, port-types warnings, revert porting and tuning refusals (2026-10-14)

Walkthrough of `docs/test-plans/2026-10-13.md` §9 against `candidate`, by task 3555 (filed by task 3492).

## Environment

| | |
|---|---|
| Base | `candidate` at `d2f5bb8aa` (Merge pull request #3740) |
| Commits under test | `fef715f23` (port pairs/filters on repo settings, `PortTypesWarnings`), `c67d32915` (no port for a revert of a change the target never got; `reverts_absent` in the triage), `141aee490` (tuning refusal comment). `git branch -r --contains` lists `origin/candidate` (and `origin/main`) for all three |
| Go / Node | go1.26.6 linux/amd64; Node v20, Playwright Chromium headless shell 151.0.7922.34 |
| git / bash | git 2.39.5, GNU bash 5.2 |
| UI | `cd ui && npm ci && npm run build` (built into `pkg/ui/static`) |
| Demo | `grain demo -addr 127.0.0.1:0 -open=false -github-sim -mechanical-port -fake-ci`, embedded SQLite in a throwaway directory |
| Merge queue | `tests/e2e` `TestPortTypesWalkthrough`: real `orchestrator.RunCycle` (dispatch, close-out, merge queue, `filePorts`/`decidePort`/`portSkip`), real declaration sync, `model.Store` on SQLite, real git |

## Stubs

| Boundary | Stub | Why |
|---|---|---|
| GitHub (demo) | `-github-sim` simulated forge | external forge |
| grain CI (demo) | `-fake-ci` | the demo has no executor; nothing in §9's UI half reads a CI verdict |
| GitHub (merge queue) | `githubsim` behind `github.NewClient`, `git http-backend` over a bare repository, `markPullRequestClean` between cycles | external forge; GitHub computes mergeability asynchronously |
| grain CI (merge queue) | `greenCI` | the executor runs jobs in sandboxes the harness does not have; the queue's reading of the verdict is real |
| Model session | `scriptAgent` (antigravity test steps running shell; port tasks follow the port body's `git cherry` + `cherry-pick -x` recipe) | live model provider |

Not stubbed: grain's HTTP API, React, the declaration parser (`repodecl.LoadSettings`), the merge queue, `mcp.GitTaskReverter`
(revert_task's own reverter, run for real in both the e2e test and the triage history), and `scripts/backport-triage.sh`.

The operator columns (`RepoConfig.PortBranches`, `PortTypes`) still have no API setter. In both the demo and the e2e test they
are written to the store the way the grain/config state repository import writes them (`store.PutRepoConfig`).

## Step 1: environment

`grain demo -mechanical-port` now also seeds acme/widgets' port configuration (`seedPortSettingsDemo`):

- operator: `PortBranches [main candidate]`, `PortTypes {main->candidate: [fix]}`;
- declared, read by `repodecl.LoadSettings` and stored by `SyncRepoDeclarations`:

```yaml
port-types:
  main->candidate: [fix, revert]   # widens the operator's [fix]
  candidate->main: [fix]           # a list on an operator pair with no list of its own
  main->candidtae: [fix]           # a typo direction
```

## Step 2: repo page and `GET .../settings`

`GET /api/repos/acme/widgets/settings` (Admin mode on):

| Field | Value |
|---|---|
| `portBranches` / `pairedBranches` | `[main, candidate]` / `[main, candidate]` |
| `portDirections` | `candidate->main` paired, operator —, declared `[fix]`, types — (every merge); `main->candidate` paired, operator `[fix]`, declared `[fix, revert]`, types `[fix, revert]`; `main->candidtae` **not paired**, declared `[fix]`, types `[fix]` |
| `portWarnings` | `port-types in .grain/settings.yml: candidate->main is ignored: the operator pairs candidate and main with no port-types list for this direction, so every merge is ported -- …`; `port-types in .grain/settings.yml: main->candidtae names candidtae, which is not a paired branch (the paired branches are main, candidate), so the entry has no effect` |
| `declarationsStatus.warnings` | the same two sentences |

**In a browser, before the fix:** the repository page showed no port table at all. The two declared warnings appeared only as
"Warning: …" lines under Advanced › Declarations, because `declarationsStatus.warnings` carries the declared ones. An operator
port-types warning would have appeared nowhere. See F1.

**After the fix** (`ui/e2e/port-settings-walkthrough.spec.js`): Advanced › Ports shows "Grain ports each merge between main,
candidate", the operator's and the declared branch lists, a table with one row per direction (Direction / Operator / Declared /
Ports: `every merge`, `fix, revert`, `nothing (not a paired branch)`), and both warnings. The spec checks this as:

- a system admin with Admin mode on;
- team `admin`, `member`, `proposer` and `viewer` users, each signed in with Admin mode off.

For every role, the section has no `input`, `textarea`, `select` or button, and that role's own `GET .../settings` returns the
same `portDirections` and `portWarnings`. Every role got 200 on the settings read.

## Steps 3–4: reverts through the real merge queue (`TestPortTypesWalkthrough`)

The repository declares `port-branches: [main, candidate]` and `port-types: {main->candidate: [fix, revert]}`. Each row is a
task whose pull request the real queue merged onto `main` with a merge commit. Rows marked † are new in this run.

| # | Change | Expected | Observed |
|---|---|---|---|
| 1 | `feat: add alpha` | no port | no port; ``not porting acme/porttypes#9000 (task 2): `candidate`: not a fix/revert change and no `Port: yes` footer (`main` ports only those types to `candidate`); it reaches `candidate` some other way, such as the next release cut`` |
| 2 | `git revert` of alpha's commit (`Revert "feat: add alpha"`) | no port | no port; ``not porting acme/porttypes#9007 (task 10): `candidate`: a revert of a change `candidate` does not have, and no `Port: yes` footer (`main` ports only fix/revert changes to `candidate`); there is nothing on `candidate` to revert`` |
| 3† | `feat: add mu`, then **revert_task** on its task (reverts the merge commit with `-m 1`; `revert: task 11 (…)` + `Reverts-Task` / `Reverts-Commit`) | no port | no port; the same revert skip line (task 12, `#9010`) |
| 4† | `feat: add nu`, then `git revert --no-edit -m 1 <merge commit>` (**merge-commit revert**) | no port | no port; the same revert skip line (task 14, `#9012`) |
| 5† | `fix: mend xi` → port landed on candidate → `git revert` of the fix on main | port the revert | port filed onto `candidate`, no skip line |
| 6† | `fix: mend omicron` → port landed → **revert_task** on its task (merge-commit revert) | port the revert | port filed onto `candidate`, no skip line |
| — | `Revert "feat: add legacy"` (candidate was cut with legacy) | port | port filed |

The skip line explains why in every case. It is only a daemon log line, though, and the merged task's own conversation says
nothing (F3).

While row 3 was being written, revert_task turned out to resolve a task id by substring. See F2: the walkthrough's helper now
asserts that revert_task reverted the task's own merge commit.

## Step 5: `scripts/backport-triage.sh` on the same history

The scratch bare origin plus clone in `/tmp/triage.sh` had the same settings and the same merges, as `--no-ff` merge commits
from `grain/eu/task-<n>` branches. Ports are `cherry-pick -x` with `Backported-from`. Both revert_task reverts were made by the
real `mcp.GitTaskReverter`, built from this tree.

```
skip 461e5aa1b60e (feat, not fix/revert: reaches candidate at the next cut): feat: add mu
skip 350ebeeb27c3 (feat, not fix/revert: reaches candidate at the next cut): feat: add nu
skip c2b4324e0358 (a revert of a change candidate never received: nothing there to revert): revert: task 11 (7be28a0e)
skip 1b371e8c0b24 (feat, not fix/revert: reaches candidate at the next cut): feat: add alpha
skip ea9aac093f73 (a revert of a change candidate never received: nothing there to revert): Revert "Merge pull request #13 from acme/grain/eu/task-13"
skip 82250ed8e579 (a revert of a change candidate never received: nothing there to revert): Revert "feat: add alpha"
PORT eb8ad5de53e9… main -> candidate: Revert "fix: mend xi"
PORT 666f931b4174… main -> candidate: fix: mend pi
PORT 6de8b7bc0d69… main -> candidate: revert: task 19 (267b7214)
main -> candidate: 3 missing, 6 skipped
candidate -> main: 0 missing, 0 skipped
```

This is the same decision as the queue for every revert: the revert_task revert and the merge-commit revert of a feature are
skipped, and both reverts of ported fixes are reported. `fix: mend pi` is a control fix that was never ported. The ported
fixes xi and omicron are left out as equivalent patches.

## Step 6: tuning runner refusal

The demo runs no orchestrator, so no runner can be dispatched there. The step ran in `tests/e2e`
`TestTuningRunnerRefusesAParameterOutsideTheAllowlist`, which dispatches a real infra-review runner through `RunCycle` with a
scripted session proposing `merge_batch_size=4` (allowed) and `merge_window_secs=9` (not allowed):

- filed: `["Tune merge_batch_size to 4"]`; `tuning_recommendation` rows: `["merge_batch_size=4"]`;
- one comment on the runner's task:

  > grain did not file 1 proposal from this run: it is not a change to a parameter grain recognizes, so nothing was filed and
  > no tuning recommendation was recorded for it.
  >
  > - "Tune merge_window_secs to 9" (parameter `merge_window_secs`): unrecognized grain_config parameter "merge_window_secs"

`TestRelayProposedTasksTuningValidationAndRecording` passes as well. This run did not show the comment in a browser. It is an
ordinary automation comment, which the task pane renders like any other, but no demo flag seeds a tuning runner.

## Findings

**F1. The repository page did not show the port configuration (fixed).** `fef715f23` added `pairedBranches`, `portDirections`
and `portWarnings` to the repo settings response, but nothing in `ui/src` read them. Which filter is in force was still not
visible in a browser. The declared warnings appeared only inside the declarations status, and an operator port-types warning
(for example the operator's own `main->release`) appeared nowhere.

Reproduction: `grain demo -mechanical-port` (with this change's seed), then open `/repos/acme/widgets#advanced`. Before the fix
there is no port table.

Fix: a read-only Ports section in the Advanced tab of `RepoPage.jsx`. Test: `ui/e2e/port-settings-walkthrough.spec.js` (all
roles, no controls, matches the API). It fails on the old page because `.repo-ports` does not exist.

**F2. `revert_task` by task id could revert another task's change (fixed).** `GitTaskReverter.findCommitForTask` ran
`git log -n 1 --fixed-strings --grep=task-<id>`, a substring match that returns the newest hit. Task 355 therefore matched
`grain/eu/task-3555`'s later merge and reverted the wrong change, under a commit message naming task 355. Every id that is a
prefix of a later id is affected (1 → 10–19, 35 → 350–359, …).

Reproduction: `TestGitTaskReverterTaskIDIsNotAPrefixMatch`. With merges of `grain/eu/task-355` and then `grain/eu/task-3555`,
the old code reverts 3555's merge for `355`, `task-355` and `grain/eu/task-355`.

Fix: the patterns are bounded on both sides (`--extended-regexp`, id quoted). The test passes, as do the rest of `pkg/mcp`'s
tests.

**F3. The "not porting" reason never reaches the task (follow-up proposed).** `filePorts` writes the skip reason only to the
daemon log (`orchestrator: not porting … (task N): …`). The plan expected it in the task's comments. A person reading a merged
`feat:` or revert on main cannot see why it was not ported without the log. Whether every non-port should post a comment is a
noise trade-off, so this run did not change it. Instead it proposed a task.

**F4. `portDirections` gives a composed `types` for an unpaired direction (minor, noted).** `main->candidtae` reports
`"types": ["fix"]` alongside `"paired": false`. API readers could take it for a filter in force. The new UI shows "nothing (not
a paired branch)" for such rows, and the warning names it, so this run left the API as it is.

## Fixes and regression tests in this change

- `ui/src/components/RepoPage.jsx`: Advanced › Ports, read-only (F1). Test: `ui/e2e/port-settings-walkthrough.spec.js`.
- `pkg/demo/demo_mechanical_port.go`: `-mechanical-port` seeds the operator pair and the declared port-types with both bad entries.
  Test: `TestMechanicalPortDemoSeedsAPortWithNoRun` now asserts the composed filter and both warnings.
- `pkg/mcp/git_reverter.go`: bounded task-id search (F2). Test: `TestGitTaskReverterTaskIDIsNotAPrefixMatch`.
- `tests/e2e/port_mechanical_walkthrough_test.go`: `TestPortTypesWalkthrough` gains rows 3–6 above: revert_task (via the real
  reverter), merge-commit revert, and reverts of ported fixes by `git revert` and by revert_task.

## CI verdict

Grain CI run `317c92bca6764892` on `grain/eu/task-3555` at `7201bbe` (the fixes, the tests and this report; this
verdict note is the only later change): **success**, every job green on the first run with no rechecks. That includes
`go` (16m53s, with `tests/e2e`, `pkg/mcp`, `pkg/demo` and `pkg/orchestrator`), `ui e2e` (19m32s, with the new
`port-settings-walkthrough.spec.js`), `frontend`, `lint`, `postgres`, `installer`, `run host`, `package cache`,
`relay image` and `terraform`. The GitHub `sandbox guest image` / `Kubernetes sandbox image` checks were red. They are
not this repository's gate (`ci_mode: grain`).
