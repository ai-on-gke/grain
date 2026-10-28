# Walkthrough: team config repositories checked by `grain state check -team` (2026-10-15)

Executes §11 of `docs/test-plans/2026-10-15.md` (task 3954, filed by nightly test-plan task 3749).

## Environment

- Base: `candidate` at `591f0f35f`. `git branch -r --contains` puts `6bcbf8844` on `origin/candidate`
  only (not yet on `main`), and `50119846a` and `be76a3050` on both `origin/candidate` and `origin/main`.
- **Daemon, not demo.** `grain demo -github-sim -git-proxy -fake-ci` cannot run this walk. The demo
  wires no StateManager (nothing in `pkg/demo` or `cmd/grain/demo*.go` refers to it), so it has no
  `grain/config-<team>` repositories. Its `-fake-ci` is also "a grain CI stub that accepts and reports
  runs but never executes anything", so the team config CI workflow would never run. Every step ran on
  a real `grain daemon` instead: `go build -tags grain_e2e ./cmd/grain` over `cd ui && npm ci && npm run
  build`, started by `ui/e2e/realdaemon.js` with its own data directory, `-sandbox-port-base 0`, the
  local-directory sandbox backend (`cmd/grain/local_sandboxes_e2e.go`), and `-github-host` pointed at a
  recording forge stub.
- Teams `alpha`, `beta` and `gamma` (gamma is archived during the walk), each with its own agy
  connection. One StateManager cycle (`POST /api/state-repo/sync`) creates
  `data/team-state-repos/{alpha,beta,gamma}`.
- Browser: Playwright's Chromium headless shell. Installing it needed
  `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`.

## Steps

| # | Step | Outcome |
|---|------|---------|
| 1 | Daemon with alpha and beta, each with a config repository | Pass, on a daemon rather than the demo (see Environment). |
| 2a | Alpha's task adds to `settings/schedules.yml` a copy of alpha's memory schedule under the id `system:memory-agent:beta`, beta's own | Pass. The run is red: `grain CI: failure … failure: grain config/grain can load this`. The job was `state check -team alpha -taken schedule=system:infra-review,system:memory-agent:beta,system:memory-agent:default,system:memory-agent:gamma,system:release-notes /state`. The step output, in the run the pane's "show the run" opens, reads: `cannot be applied as team "alpha"'s configuration: settings/schedules.yml: schedule id "system:memory-agent:beta" is already another team's schedule; choose another id`. |
| 2b | Message visible in the task pane | Pass with a caveat. The pane itself shows the verdict and the job name. The taken-id sentence is one click away, in the run overlay. The task's grain CI comment is the one-line verdict only (by design since `43a612cbb`). |
| 2c | Submit held | Pass. Submit on the red PR returns 200 and records auto-merge; it does not merge. The task stays open while the queue sends it back to an agent (automatic repair 1 of 2). The pane's Submit button stays enabled, with that meaning. |
| 2d | Fixed, it goes green and applies after merge | Pass. In the repair run's checkout the id was changed to `alpha-second-memory` and pushed. The check printed `loaded: 8 rows across 3 tables for team alpha … ok: grain can load this as team alpha's configuration`. The PR merged with no second Submit (the first one stood), and `/api/schedules` lists `alpha-second-memory` with `team: alpha` beside `system:memory-agent:beta` with `team: beta`. |
| 2e | The repair's prompt | **Flaw, fixed (finding 2).** |
| 3 | `grain state check -team alpha DIR` on broken copies of alpha's real export | Pass. Every tree is refused with a readable message (below). One flaw, fixed (finding 4). |
| 4 | Through the proxy with a run token: `grain/config-..`, `grain/config-default`, an archived team's repository | Pass: none of them can be read or pushed, and the forge stub saw no request. Details below. Note that for `..` and `default` the scope check refuses the request before the new guard is reached. |
| 5 | The team config task's pull-request view renders its diff | Pass for `/repos/grain/config-alpha/compare/main...<branch>` (side by side, `settings/schedules.yml +61 -0`). **Flaw, fixed (finding 3):** the pane's PR link went to github.com. **Flaw, fixed (finding 1):** a further compare of the branch went to the forge. |

### Step 3: the messages an operator reads

These trees are copies of `data/team-state-repos/alpha` after the walk, with `.git` removed. They were
checked with `grain state check -team alpha <dir>`. The trees are not committed.

| Tree | Exit | Message |
|------|------|---------|
| alpha's own export | 0 | `schema: 17 … loaded: 8 rows across 3 tables for team alpha … ok: grain can load this as team alpha's configuration` |
| bad YAML (`schedules:\n  - id: [unclosed`) | 1 | `grain state: staterepo: parsing settings/schedules.yml: yaml: line 1: did not find expected ',' or ']'` |
| a `beta` row (`team: beta`) | 1 | `… cannot be applied as team "alpha"'s configuration:` then `settings/schedules.yml: schedule "alpha-second-memory" belongs to team "beta", and a team's configuration repository can only carry its own rows` (and the same for `system:memory-agent:alpha`) |
| a row with no team | 1 | `settings/schedules.yml: schedule "alpha-second-memory" names no team, which is the default team, so applying this repository would skip it; set team: alpha` |
| a duplicate id | 1 | `settings/schedules.yml: schedule id "system:memory-agent:alpha" is used twice` |
| a `tables/` dump | 1 | `grain state: staterepo: t/tables carries a tables/ dump, which only a deployment's state repository has; a team's configuration repository holds settings/ documents and repos/ files` |
| `-team ..` | 1 | `grain state: team ID ".." is not a team` |

Before the fix, the beta-row and no-team cases each printed six `warning: settings/schedules.yml: a
schedule_read row for schedule "…", which is not one of this team's, is ignored` lines (and the same
for `schedule_grant`) ahead of the refusal (finding 4).

### Step 4: the git proxy

Each request was made with `git ls-remote` from the run's own sandbox `HOME`, using its proxy credential.

- **Alpha's run token:**
  - `grain/config-..`, `grain/config-%2e%2e`, `grain/config-default`, `grain/config-gamma` (archived)
    and `grain/config-beta` each get `remote: grain/config-<x> is not in scope for this sandbox`
    (HTTP 403).
  - `grain/config-alpha` and `grain/config-ALPHA` list `main`.
- **Gamma's run token, with gamma archived while the run was live:**
  - `ls-remote` of its own `grain/config-gamma` (and `-GAMMA`), and a push to it, get `fatal:
    repository 'http://127.0.0.1:<port>/grain/config-gamma.git/' not found`. This is the
    `6bcbf8844` guard: an archived team's directory answers not found.
  - `grain/config-..` and `grain/config-default` are refused as not in scope.
- **Filing a task** on `grain/config-..`, `grain/config-default` or `grain/config-nobody` is refused
  400: `only team ".." can submit to grain/config-..` (and likewise for the others). So no run token can
  carry those repositories in scope. The guard against `..` and `default` is defence in depth behind
  the scope check, and a sandbox cannot reach it.
- **The forge stub** (an HTTP server on `-github-host` that records every request) saw no request for
  any of these. It saw four for `grain/config-alpha` elsewhere in the walk (finding 1).

## Findings

### 1. A team config branch's compare went to the forge (fixed)

`StateRepoPullRequestClient` embeds the forge client and overrides `CompareCommits`, but not
`CompareRefs`. For a team config task, the finish path's `branchIsBehind` (`pkg/orchestrator/finish.go`)
therefore sent the forge requests like these, which the forge stub recorded:

```
GET /repos/grain/config-alpha/compare/main...grain/fc76f67d/task-6   (x4)
```

On a real deployment that is GitHub, asked with the deployment's credential about a repository under
an owner grain does not control. The 404 was read as "not behind", so a branch behind its base was never
caught. `landed.go` and the path triggers use the same method.

**Reproduction:** the new test in `team-config-repo.spec.js` asserts that the forge stub saw no
`config-` request. It fails on the old code with the four requests above.
`TestStateManagerKeepsEachTeamsConfigRepository` calls `CompareRefs` on a native client that has no
forge behind it, and panics on the old code.

**Fix** (`pkg/daemon/statepullrequests.go`): `CompareRefs` is answered from the config repository's
checkout:
- status (`ahead` / `behind` / `diverged` / `identical`), merge base, commits, and the files changed
  from the merge base;
- a ref that does not resolve gets GitHub's 404, which callers already handle.

The commit listing is shared with `CompareCommits` (`localCommits`), so no lock is taken twice.

### 2. A team config repair pointed its agent at GitHub Actions and at a quote that is not there (fixed)

The merge queue's repair comment for alpha's red said:

> grain's own CI (`.grain/workflows`) is what is red here … the comment grain CI left on this task quotes
> the failing step's output …
>
> ## What CI says
>
> GitHub Actions has no workflow run at all against this commit, so there is no job log to hand you: the
> failing check named above comes through the Checks API from something else -- Buildkite, CircleCI, a
> review bot …

Both parts are wrong:
- **The Actions section.** A config repository never reaches GitHub. That section is the native
  client's empty `ActionsState` rendered as evidence. It appeared because a config repository's gate
  is not counted as grain CI unless the repository declares it.
- **The quote.** The task's grain CI comment is `grain CI failure at <sha>.` and nothing more. It
  has quoted no step output since `43a612cbb` ("Keep task CI updates terse").

**Reproduction:** `TestAConfigRepositoryRepairPointsAtGrainCIAlone`
(`pkg/orchestrator/github_ci_not_read_test.go`). It fails on the old code with exactly the text above.

**Fix** (`pkg/orchestrator/sync.go`):
- Both repair paths skip `readBuildEvidence` for `model.IsConfigRepo`.
- `grainCINote` now says the failing step's output is in the grain CI run, which `get_ci_log` reads
  and `wait_for_checks` reruns after a push.

### 3. The task pane linked a team config PR to github.com (fixed)

`pullRequestUrl` sent only `grain/config` to grain's own PR page. Alpha's pane linked
`grain/config-alpha#6` to `https://github.com/grain/config-alpha/pull/6`.

**Fix** (`ui/src/state.js`): `grain/config-<team>` refs go to `/repos/grain/config-<team>/compare/main...<branch>`.
- **Unit test** (`ui/src/state.test.js`): the new case for a team config ref, plus the near misses
  `grain/config-` and `grain/configuration`.
- **E2E:** the spec asserts the pane link's `href`.

### 4. `state check -team` buried a refusal under warnings about its own companion rows (fixed)

A schedule refused for naming another team, or none, also had its `schedule_read` and `schedule_grant`
rows reported one by one as "not one of this team's, is ignored". With alpha's two schedules that was six
warnings before the two-line refusal, and they read like a separate problem.

**Fix** (`pkg/staterepo/team_check.go`): companions of a refused row are not warned about; the refusal
already covers them. **Test:** `TestCheckTeamConfigRefusalIsNotBuriedUnderItsCompanionRows`, which
fails on the old code with `a schedule_read row for schedule "alpha-nightly", which is not one of
this team's, is ignored`.

### Observations (not changed)

- **The pane shows the verdict, not the sentence.** The taken-id message is in the run overlay rather
  than in the pane itself. That follows from the terse task comments (`43a612cbb`), and finding 2's note
  now sends a repair to the run.
- **Odd message for a repository with no team.** Filing a task on `grain/config-..` or
  `grain/config-nobody` is refused with "only team ".." can submit to grain/config-..". It refuses
  correctly, but it reads as though such a team existed.
- **`-team default`.** `grain state check -team default` is accepted and checks the tree as the
  default team's. The default team has no `grain/config-default` (its configuration is `grain/config`),
  so CI never runs this; it is harmless.
- **A test race, not a product bug.** A team's memory schedule is seeded after its repository's first
  export. The new spec waits for a cycle that carries it before cloning.

## Fixes and tests

| Change | Test |
|--------|------|
| `pkg/daemon/statepullrequests.go`: local `CompareRefs` for config repositories | `TestStateManagerKeepsEachTeamsConfigRepository` (extended) |
| `pkg/orchestrator/sync.go`: no Actions read for a config repo's repair; `grainCINote` names `get_ci_log` | `TestAConfigRepositoryRepairPointsAtGrainCIAlone` (new) |
| `ui/src/state.js`: team config PR links stay on grain | `state.test.js` (new case) |
| `pkg/staterepo/team_check.go`: no companion warnings for refused rows | `TestCheckTeamConfigRefusalIsNotBuriedUnderItsCompanionRows` (new) |
| `ui/e2e/realdaemon.js`: the stub docker prints the check's output and exits with its status, so it reaches the job log | Exercised by both team-config specs and by `config.spec.js` (passes) |
| Steps 2, 4 and 5 end to end | `ui/e2e/team-config-repo.spec.js`, new test "a team config change grain CI refuses is held, repaired and applied; an archived team's repository is not found". Both tests in the file pass (17s); `config.spec.js` passes. |

## Stubs

Only external boundaries:
- **Forge:** `-github-host` points at a recording HTTP stub that answers 404 to everything. It is how
  "the forge sees no request" was checked, and how finding 1 surfaced.
- **Model provider (agy):** `realdaemon.js`'s stub `agy` keeps each run's sandbox alive until the test
  writes `agent-finish`. The test makes the run's edits and pushes in that checkout, including the
  repair run's fix. The Gemini key is a placeholder that reaches no provider.
- **Docker:** `realdaemon.js`'s stub `docker` runs the real `grain state …` command the workflow gave
  it, against the mounted checkout.
- **Not stubbed:** the StateManager, the git proxy and its scope check, grain CI, the native PR
  client, the merge queue and its repair, the team apply, and grain's own APIs and UI.

Scratch drivers (not committed):
- a Playwright script that recorded the probes, pane text and overlay text above;
- the step 3 trees under `/tmp`.

## Not run

- `grain demo` as the plan names it: it has no team config repositories and its fake CI executes
  nothing (see Environment).
- A real GitHub: everything forge-side is the stub.

## CI verdict

The full grain CI suite was run with `wait_for_checks` (no `jobs`) on this branch's final commit. Its
result is in the task's closing note and on the pull request.
