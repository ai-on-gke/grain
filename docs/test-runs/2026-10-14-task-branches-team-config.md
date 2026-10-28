# Walkthrough: task-branch settings on System, the unique default branch, team config repositories (2026-10-14)

Executes §8 of `docs/test-plans/2026-10-13.md` (task 3554, filed by nightly test-plan task 3492).

## Environment

- Base: `candidate` at `d2f5bb8aa`. `git branch -r --contains` puts `a2a23a78a`, `ebaaf3880` and
  `3fed08f41` on `origin/candidate` (and `origin/main`).
- Binary: `go build -tags grain_e2e ./cmd/grain` over `cd ui && npm ci && npm run build`, as
  `ui/e2e/global-setup.js` builds it.
- **Legacy deployment (steps 1–2):** `grain demo -addr 127.0.0.1:18420 -open=false -github-sim`, on the
  demo's embedded SQLite. Its deployment ID default is `0dc35ba9`. The demo plays a deployment from before
  the unique default, so its template is empty (`grain/task-{id}`).
- **New deployment (step 3):** `grain daemon -data-dir <fresh dir> -sandbox-dir <fresh dir> -as alice
  -max-workers 1 -github-host 127.0.0.1:9 -github-insecure-http`, with no settings ever saved.
- **Team config repos (step 4):** the same real daemon, started by the e2e helper `ui/e2e/realdaemon.js`
  (moved out of `config.spec.js`), with its local state repository, two teams `alpha` and `beta`, and a
  local-directory sandbox backend (`cmd/grain/local_sandboxes_e2e.go`).
- Browser: Playwright 1.62.1, Chromium headless shell 151.0.7922.34. Installing it needed
  `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`.
- Roles in step 1 are real `/api/auth/register` + `/api/auth/login` sessions:
  - `sam`: a system admin, added to `adminAccounts`;
  - `tara`: admin of team `default`;
  - `mia`: member of team `default`.

  An unsessioned loopback request is `AuthMethodTrusted`, not a signed-in admin. It is unaffected by
  Admin mode and was not used as a role.

## Steps

| # | Step | Outcome |
|---|------|---------|
| 1a | System → Agents & Task Branches, as a system admin with Admin mode on | Pass. `/admin/agents` selects the tab. It shows Max agent turns, the prompt extension, Branch name template, Deployment ID, and the Commit identity section. Commit identity is now a pointer to each team's Git identity tab (`commit-identity-pointer`); since `f386abe49` there is no deployment-wide identity to edit. `PUT /api/settings` returns 200. |
| 1b | The same admin without Admin mode | Pass. No Agents tab and none of the fields. `PUT /api/settings` returns 403 "you are a system admin; turn on Admin mode to do this". |
| 1c | Team admin (`tara`) and member (`mia`) | Pass. No Agents tab and none of the fields. `PUT /api/settings` returns 403 "does not have administrator privileges". `GET /api/settings` returns 200 to both, with `taskBranchTemplateDefault`. |
| 1d | Models has the pointer only | **Plan out of date.** Team settings → "Models & connections" has none of the deployment-wide fields for any role (0 of 4 inputs). It also has no pointer: `3dbf603ce` ("merge Models & connections", 10-13) removed the `deployment-settings-note` on purpose, and its unit test asserts the note's absence. Not a bug. |
| 2a | "Use it" | Pass. The suggestion reads "New tasks here go on grain/task-{id} … Switch to grain/{deployment}/task-{id} …". Clicking "Use it" removes it, fills the box, and `GET /api/settings` returns `taskBranchTemplate: "grain/{deployment}/task-{id}"`. |
| 2b | Existing tasks keep their branches; new tasks use the new form | Pass. All 12 seeded tasks were on `grain/task-N` before and after (0 changed). A task filed through "+ New task" got `grain/0dc35ba9/task-13`, and still had it after a reload. The demo has no forge PR list (the GitHub sim serves branches only), so "open PRs untouched" was checked as stored `Task.Branch`, which is what the merge queue and the PR code read. |
| 2c | `revert_task` on such a branch | **Fail, fixed (finding 1).** `grain/0dc35ba9/task-13` was pushed and merged as PR #21 in the sim's real bare repo (`githubsim/acme/widgets.git`), then `grain/task-130` as #22. The real `mcp.GitTaskReverter` on a clone, given `grain/0dc35ba9/task-13` or `13`, reverted **#22, task 130's merge**. With the fix it reverts #21. |
| 2d | A port on such a branch | Pass. `tests/e2e`'s `TestPortMechanicalWalkthrough` (real porter, git proxy, `RunCycle`s) was rerun with the store seeded from `model.NewDeploymentConfig()`. The ports were filed on `grain/318c0acb/task-3`, `-5`, `-7` and `-10`. They cherry-picked, skipped already-ported commits, and fell back to an agent on a conflict, exactly as on `grain/task-N`. The run's only assertion failures were the test's own assumptions: `model.BranchName(id)` for source branches, and stray `grain/task-N` branches planted to collide with the port, which the unique form does not collide with. Not committed: the test is about the legacy form. |
| 3 | Fresh data directory: first task on the unique form | Pass. With no settings saved, `GET /api/settings` returns `taskBranchTemplate: "grain/{deployment}/task-{id}"` and `deploymentIdDefault: "e644d63c"`. The first task filed (#2; #1 is the Director session) is on `grain/e644d63c/task-2`. Filing it needed the team's Git credential set ("anonymous"), which is a team edit, not a settings save. |
| 4a | `grain/config-<team>` exists after one tick with that team's exports | Pass. Within one StateManager cycle (about 17s on the timer, or at once via `POST /api/state-repo/sync`), `data/team-state-repos/{alpha,beta}` each held one commit, "grain: export team <t> configuration". Each has `settings/templates.yml` with only its own template, plus `schedules.yml` (the team's memory-agent schedule), `suites.yml`, `README.md` and `schema-version`. Default and archived teams get none. |
| 4b | A task targeting it clones, pushes and merges its config PR | **Fail, fixed (findings 2 and 3).** Alpha's task cloned `http://127.0.0.1:<proxy>/grain/config-alpha.git` through the git proxy and pushed `grain/<dep>/task-4`, which became native PR `grain/config-alpha#4`. Its grain CI never started: the workflow reader cloned `grain/config-alpha` from the forge, on every tick. The PR never merged. Its diff view returned 502. With both fixes, it reaches `awaiting_submit`, the compare view shows the diff, and Submit merges it. |
| 4c | The change applies to that team only | Pass. After the merge, `/api/templates` has `alpha:alpha after` and `beta:beta before`, and no `alpha:alpha before`. |
| 4d | Memory documents unchanged | Pass. `GET /api/memory/documents` is identical before and after the team apply (the deployment note `deploy-note`). |
| 4e | Archiving the team stops its sync | Pass. After `PUT /api/teams/beta {"archived":true}`, a new template was added to each team and a cycle forced. Alpha's repository gained a commit with `alpha later`. Beta's HEAD did not move and does not contain `beta later`. |

Steps 4a–4e are committed as `ui/e2e/team-config-repo.spec.js` and pass on the fixed tree. Before the fix
it failed at "the team config pull request did not merge".

## Findings

### 1. `revert_task` reverted another task's merge (fixed)

`GitTaskReverter.findCommitForTask` ran `git log -n1 --fixed-strings --grep=task-<n>`, so the newest
commit naming any id that *starts with* the same digits won. Reverting task 13 took task 130's merge, or
a commit with an `Origin: task 1300` trailer. Ids in this deployment are four digits, so `task-355`
already matches `task-3554`. It also dropped the deployment segment before searching, so
`grain/0dc35ba9/task-13` matched another deployment's `grain/5e6f7a8b/task-13` merge on the same repo.
Telling those two apart is the purpose of the new default form.

**Reproduction:** step 2c, or `TestGitTaskReverterDoesNotTakeAnotherTasksMerge`, which fails on the old code:

```
RevertTask(grain/0dc35ba9/task-13) reverted 239649bd… ("Merge pull request #9 from acme/grain/0dc35ba9/task-130"), want 2470ac01…
```

**Fix** (`pkg/mcp/git_reverter.go`): a deployment-qualified id is searched for as it stands, before the
bare forms. Every pattern is an ERE that must end where the id does (`([^A-Za-z0-9_-]|$)`). The id is
already limited to `[A-Za-z0-9_/-]` by `validateRevertTarget`.

### 2. A team config task's grain CI never started, so its PR never merged (fixed)

`CiWorkflowSource.cloneURL` treated only `grain/config` as controller-local. `grain/config-<team>` was
cloned from `https://<github host>/grain/config-<team>.git`, which no forge has. The daemon logged this
on every reconcile tick:

```
ci: orchestrator: starting grain CI for task 4 at 25fd06412dcb: ci: reading grain/config-alpha's workflows at grain/5788d4b8/task-4: … git clone: exit status 128: fatal: unable to access 'http://127.0.0.1:9/grain/config-alpha.git/'
```

**Fix** (`pkg/daemon/ci.go`, `run.go`): `CiWorkflowSource.TeamConfigReposDir` (`TeamStateReposDir`)
serves a team config repo from the team's checkout. That is the same directory the StateManager syncs
and the git proxy serves. The repository-declarations reader shares the type and gets it too. A suffix
that is not a valid single-segment team id is not turned into a path.
`TestCiWorkflowSourceReadsTeamConfigReposLocally` fails without the fix with the clone error above.

### 3. A team config task's diff view returned 502 (fixed)

`GET /api/repos/grain/config-alpha/compare?base=main&head=<branch>` returned 502 `upstream_unavailable`:
- `repoComparisonGate` sent every repo but `grain/config` to the GitHub client;
- `StateManager.Compare` refused anything but `grain/config`.

A reviewer could not see a team config change before pressing Submit.

**Fix** (`pkg/daemon/globals.go`, `statemanager.go`): every config repository (`model.IsConfigRepo`)
goes to the local comparer, which diffs a team's repo in the checkout it opened for that team
(`teamRepo`). An archived team's repo is not opened, so comparing it is still refused.
`TestStateManagerKeepsEachTeamsConfigRepository` now asserts the diff and the refusal, and fails on the
old code with "native comparison is not available for grain/config-bouncer". The e2e spec asserts the
200 and the diff.

### 4. Team config PRs are not validated by grain CI (follow-up proposed)

Once the run starts (finding 2), it concludes `success` with `jobs: null`. `ConfigRepoCISource` declares
the "grain can load this" (`grain state check`) workflow only for `grain/config`. So a team config change
that does not load merges unchecked, and then fails `applyTeamConfigRepo` on every tick, logged only.
`grain state check` cannot just be pointed at a team tree. On `team-state-repos/alpha` it says "ok"
after 24 warnings that applying it would empty deployment tables (`grain_config`, `team`, `repo_config`,
…), because it reads the tree as a whole grain/config dump. It needs a team mode. Proposed as a
follow-up, together with a hardening note: the git proxy's `ResolveDir` maps any `grain/config-<suffix>`,
`..` included, to `TeamStateRepoDir` and runs `MkdirAll` on it.

### Plan notes

- "Models has the pointer only" (step 1) is out of date: `3dbf603ce` removed the pointer.
- "the five settings" counts the commit identity. Since `f386abe49` it is a pointer to each team's Git
  identity tab, not an input.
- The 10-13 `config.spec.js` already proves a fresh daemon's branch form for `grain/config` tasks
  (`^grain/[0-9a-f]{8}/task-<id>$`).

## Fixes and tests

| Change | Test |
|--------|------|
| `pkg/mcp/git_reverter.go`: exact, deployment-first task lookup | `TestGitTaskReverterDoesNotTakeAnotherTasksMerge` (new) |
| `pkg/daemon/ci.go`, `run.go`: team config workflows read locally | `TestCiWorkflowSourceReadsTeamConfigReposLocally` (new) |
| `pkg/daemon/globals.go`, `statemanager.go`: team config compare | `TestStateManagerKeepsEachTeamsConfigRepository` (extended) |
| Step 4 end to end on a real daemon | `ui/e2e/team-config-repo.spec.js` (new); `config.spec.js`'s daemon helpers moved to `ui/e2e/realdaemon.js` and shared. The new spec passes `-sandbox-port-base 0`, so its git proxy does not contend with `config.spec.js`'s daemon for the reserved port block; the two passed together on two workers. |

## Stubs

Only external boundaries:
- **GitHub:** the demo's `-github-sim` (a `githubsim.Sim` over a real bare repo, branches only) for steps
  1–2. Step 3 and the real-daemon spec point `-github-host` at `127.0.0.1:9`, where nothing listens.
- **Model provider (agy):** `realdaemon.js`'s stub `agy` keeps the run's sandbox alive until the test
  writes `agent-finish`. The test makes the run's edit and push in that checkout itself. The team
  connection's Gemini key is a placeholder that reaches no provider.
- **Docker:** `realdaemon.js`'s stub `docker` runs `grain state check` on the mounted checkout, for
  `grain/config`'s check job. No job ran for the team repo (finding 4).
- **Not stubbed:** the git proxy, the StateManager, grain CI, the native PR client, the merge, the team
  apply, and grain's own APIs and UI.

Scratch drivers (not committed):
- the Playwright scripts for steps 1–2;
- a `package rv` test that ran `mcp.GitTaskReverter` against the sim's repo;
- a temporary edit of `TestPortMechanicalWalkthrough` to seed `NewDeploymentConfig()` (step 2d).

## CI verdict

See the pull request for this branch. The full grain CI suite is run with `wait_for_checks` on the final
commit, and its result is given in the task's closing note.
