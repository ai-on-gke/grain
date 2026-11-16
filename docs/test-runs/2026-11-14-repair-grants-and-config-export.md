# Test run: repair tasks' CI grant and `grain/config` `repo#read`/`repo#write` export (2026-11-14 plan §2)

- **Plan:** `docs/test-plans/2026-11-14.md` §2 (PR #1492 `b17336c4e`, PR #1447 `f9ebd5c87`)
- **Run by:** [Task #5727](/tasks/5727), filed by [Task #5629](/tasks/5629), 2026-11-15 UTC
- **Commit walked:** `origin/main` at `39e33c00d`, then the fix on this branch (`grain/53549e42/task-5727-walkthrough-repair-tasks-ci`)
- **Environment:**
  - grain sandbox VM (Debian bookworm), Go 1.26.10.
  - SQLite 3.40.1, and PostgreSQL 15.20 (`postgres://grain:grain@127.0.0.1:5432/…`, one fresh database per walk).
  - Each walk ran a real `grain daemon -sandbox-port-base=0 -disable-native-sandboxes` with `-github-webhook-addr` and `-github-host 127.0.0.1:4230 -github-insecure-http`.
  - The GitHub API was `pkg/github/githubsim` served over HTTP (`simgh/`). CI verdicts arrived as signed `workflow_job`/`workflow_run` webhooks (`send-webhook.py`).
- **Helpers:** `docs/test-runs/2026-11-14-repair-grants-and-config-export/`. These are `start.sh`, `walk-repairs.sh`, `deny.sh`, `asrun.sh`, `simgh/` and `runproxy/`. Raw output is in `output/`. The two Go helpers are `//go:build ignore`, so `go test ./...` skips them. Build them with `go build -o /home/debian/bin/simgh <dir>/simgh/main.go`, and likewise `runproxy/main.go`.

## Summary

| # | Item | SQLite | PostgreSQL |
|---|------|--------|------------|
| 1a | Red `Tests` push on `main` files a repair holding `api#ci-read={"repo":"acme/widgets"}` | Pass | Pass |
| 1b | Red Stage 1 (Nightly on `v1.2.0-rc.1`) files a repair holding the same grant | Pass | Pass |
| 1c | Each repair's run reads CI with the real in-sandbox `grain ci log` / `grain read ci://…`, and no person grants anything | Pass | Pass |
| 1d | A repair filed before #1492 (grant row deleted) is updated by the next red run | **Bug: stayed forbidden.** Fixed here; passes after the fix | Pass after fix |
| 1e | Policy deny of `api#ci-read`: the repair is filed without the grant | Pass | Pass |
| 1e′ | …and says why | **Gap: said nothing.** Fixed here | Pass after fix |
| 1f | `ci_mode: grain` repository: the Stage 1 repair holds `grain#ci` and reads CI | Pass | Pass |
| 2a | Export of reads/writes on `acme/…`, `ghe.example.com/acme/widgets` and a non-default `base` | Pass, with a **plan inaccuracy** (see below) | n/a (format only) |
| 2b | `grants:` `repo#write` base disagreeing with `writes:` | Union: both bases imported, with no warning | — |
| 2c | Delete one spelling only | Row survives, either way round | — |
| 2d | Task filed from the template gets the matching git-proxy push set | **Not done**: filing was refused, because the walkthrough deployment has no GitHub credential for the targets | — |

## 1. Repair tasks' default CI grant (PR #1492)

### How the run was driven

`walk-repairs.sh NAME DRIVER [DSN] [CIMODE]` does the following:

1. Starts the simulated GitHub and the daemon (`start.sh`), with `acme/widgets` as its repository.
2. Sets `PUT /api/repos/acme/widgets/ci-mode {"ciMode":"github"}`. The base-branch repairer acts only on an *explicit* `github` mode (`gitHubActionsJudges`). The effective default `github` that a fresh deployment reports is not enough, so step 2 is required.
3. Creates a Stage 1 schedule: `github_workflow_run` on `nightly.yml`, branches `v*-rc.*`.
4. Adds run 9002 (`Tests`, `main`, `go` failing) to the simulator and delivers it by webhook. The daemon reads the jobs and the default branch from the simulator and logs ``orchestrator: `main` run 9002 on acme/widgets is red; filed repair 2 at priority 9``.
5. Does the same for run 9101 (`Nightly`, `v1.2.0-rc.1`), which logs `Stage 1 run 9101 on acme/widgets is red; filed repair 4`.

Both repairs, read with `GET /api/tasks/{id}`:

```
{"id":"2","title":"Fix the red tests.yml on main at 7f42afc89657","capabilities":["https://grain.dev/capabilities/api#ci-read={\"repo\":\"acme/widgets\"}"]}
{"id":"4","title":"Fix the red Stage 1 (Nightly) at ef9127434db1","capabilities":["https://grain.dev/capabilities/api#ci-read={\"repo\":\"acme/widgets\"}"]}
```

### The repair's own `grain ci` commands

This sandbox has no model credential, so the daemon cannot dispatch an agent. `runproxy/` stands in for dispatch only, and everything after the token is the production path:

- It stops the daemon, opens its store, and records a live run of the repair on a sandbox (`Store.StartRun`, as dispatch does).
- It hands that sandbox a token and serves the daemon's own git proxy (`daemon.StartGitProxy`) over that store.
- The real `grain` binary then runs in sandbox mode (`GRAIN_SANDBOX_PROXY`, `GRAIN_SANDBOX_TOKEN`).

So `SandboxNamespaceScope` reads the task's stored grants, and the `/cli/ci/*` routes answer from the stored GitHub CI rows.

Output on both stores, for repairs 2 and 4:

```
--- [task 4] grain ci log 9101 --job stage-1
attempts/
go test ._....log
--- [task 4] grain read 'ci://acme/widgets/9101/stage-1/go test ._....log'
job "stage-1" concluded failure (https://github.com/acme/widgets/actions/runs/9101/job/91011)
--- [task 4] grain ci list --repo acme/other
grain: forbidden: acme/other is outside this sandbox's CI read scope
```

The repair can read its own repository's CI and nothing beyond it.

`grain ci list --ref main` answered `0 of 0 matching run(s)` through this helper. The helper's proxy falls back to `ci.NewStoredRunStore`, whereas a real daemon lists from its embedded CI service (`LiveCI`). Nothing was refused; I did not chase the listing difference further.

### Bug: a repair filed before #1492 stays forbidden (fixed)

**Reproduction** (deterministic; `walk-repairs.sh` does it on both stores):

1. File repair 2 as above.
2. Stop the daemon and delete its grant: `DELETE FROM task_grant WHERE task_id = '2'`. This is the row a repair filed before #1492 has, like task 5426.
3. Its run's `grain ci log 9002 --job go` answers `forbidden: grain ci: this run may not read CI runs and logs … request_access for https://grain.dev/capabilities/api#ci-read with {repo: "acme/widgets"}`.
4. Restart the daemon on the same data (`KEEP=1 start.sh`) and deliver another red run, 9003. The daemon logs ``run 9003 on acme/widgets is red; appended it to the open repair 2``.

**Before the fix** (`39e33c00d`, SQLite): after step 4, repair 2 still showed `"capabilities":[]`. `fileOrUpdateRepair`'s update path appended the comment and kept the priority, but never touched the grants. A repair that was open across the upgrade therefore stayed forbidden for as long as it was open: exactly task 5426's parking.

**Fix** (`pkg/orchestrator/stage1repair.go`): the update path applies `model.AdjustRepoCIGrants(t.Grants, repo, RepoCIDefaults)` to the open repair. This is the same adjustment a new repair (and every other filing path) gets. It also brings the grant in line if the repository's CI mode changed: `grain#ci` is added or stripped.

**After the fix**, on SQLite and PostgreSQL:

```
{"id":"2","title":"Fix the red tests.yml on main at 7f42afc89657","capabilities":[]}
== another red Tests push on main (run 9003)
… `main` run 9003 on acme/widgets is red; appended it to the open repair 2
{"id":"2","title":"Fix the red tests.yml on main at 7f42afc89657","capabilities":["https://grain.dev/capabilities/api#ci-read={\"repo\":\"acme/widgets\"}"]}
--- [task 2] grain ci log 9003 --job go
attempts/
go test ._....log
```

**Regression test:** `TestAnOpenRepairWithoutTheCIGrantGainsItOnTheNextRedRun` covers Stage 1 and the GitHub base repair, on a GitHub-CI and a grain-CI repository. It fails on all four subtests without the fix.

### Gap: a policy deny said nothing (fixed)

`deny.sh` sets the capability policy to `{"rules":[{"capability":"https://grain.dev/capabilities/api#ci-read","deny":true}]}` and turns `main` red. The repair is filed with `"capabilities": []`, which is correct.

On `39e33c00d` nothing in the repair said why. Its run would hit `forbidden` and be told to `request_access` for a grant the policy refuses.

**Fix:** a repair whose repository's default CI read is denied now ends its body, and each red run appended to it, with this note:

> This repair holds no CI read: this deployment's capability policy denies https://grain.dev/capabilities/api#ci-read on acme/widgets, so `grain ci list` and `grain ci log` answer forbidden here. Work from the failing jobs above and `grain ci wait` on your own branch, and do not request https://grain.dev/capabilities/api#ci-read -- the policy refuses it.

This was seen live on both stores (`output/deny-sq.txt`, `output/deny-pg.txt`).

**Regression test:** `TestARepairDeniedCIReadSaysWhy` (denied, permitted and grain-CI). It fails without the fix.

**Follow-up from security review [Task #6183](/tasks/6183):** as first written, the note was decided from the repository's defaults alone. The update path's `AdjustRepoCIGrants` never strips an `api:ci-read` the repair already holds. So an open repair that still held one would have been told on every appended run that `grain ci log` answers forbidden and not to ask for the grant, which was untrue. That covers two cases:

- the default it was filed with before a deny rule was added;
- one a person granted it.

Nobody answered the review's question, so I took its conservative option. The note is now decided from the grants the repair ends up with: the new `model.GrantsReadCI` matches `SandboxNamespaceScope` (`grain#ci`, `grain#source`, or an api grant whose `ci:read` reaches the repo). No grant is stripped.

**Regression test:** `TestAnAppendedRunDoesNotDenyACIReadTheRepairStillHolds` covers filed-before-the-deny and granted-by-a-person. Both cases fail without the check.

### `ci_mode: grain`

`walk-repairs.sh … grain`, on SQLite and PostgreSQL:

- The Stage 1 repair is filed holding `https://grain.dev/capabilities/grain#ci={}` and reads run 9101's log through the proxy.
- The red `Tests` push on `main` files no base-branch repair. This is by design: on a grain-CI repository, grain CI and not GitHub Actions judges `main` (`gitHubActionsJudges`).

## 2. `grain/config` export of reads/writes as `repo#read`/`repo#write` (PR #1447)

### Steps

1. A fresh SQLite daemon (`start.sh cfg`) was pointed at a local bare repository: `POST /api/state-repo/remote {"remote":"file:///home/debian/wt/config.git","branch":"main"}`. That put it in `external` mode.
   - The bare repository was empty, so it was seeded with the daemon's own initial state commit (`git -C <data-dir>/state-repo push …/config.git HEAD:refs/heads/main`).
2. A template was created: target `acme/widgets`; reads `acme/docs` and `ghe.example.com/acme/widgets`; writes `acme/widgets-deploy@release/2.0`, `acme/site` (no base) and `ghe.example.com/acme/widgets@main`.
   - In external mode the API answers 409 `config_managed_externally`, together with the exact edit to commit (`output/tpl-resp.json`). I committed that edit to the bare repo and pushed it.
   - The daemon pulled it within its sync interval, about 30 s.

### The exported YAML

```yaml
    target: acme/widgets
    reads:
      - ghe.example.com/acme/widgets
    writes:
      - repo: ghe.example.com/acme/widgets
        base: main
    grants:
      - https://grain.dev/capabilities/repo#read={"repo":"acme/docs"}
      - https://grain.dev/capabilities/repo#write={"base":"","repo":"acme/site"}
      - https://grain.dev/capabilities/repo#write={"base":"release/2.0","repo":"acme/widgets-deploy"}
```

A schedule with the same reads/writes exports the same way (`output/sched-resp.json`).

**Plan inaccuracy:** the plan (and #1447's message) say default-host rows are written as grants *and still* under `reads:`/`writes:`. On `main` they are written under `grants:` **alone**. PR #1511 (`28907dd1c`, "write plain repository access under grants: alone in grain/config") merged after the plan's audit and changed this deliberately. The non-default-host rows (`ghe.example.com/…`) stay under `reads:`/`writes:` only, as the plan says.

After import, `GET /api/templates` shows `reads: [acme/docs, ghe.example.com/acme/widgets]` and all three writes with their bases, with `capabilities: []`. No CI-only `#read` reaches the template's grants.

### Disagreeing spellings

I added a hand-written `writes: - repo: acme/widgets-deploy / base: release/2.0` and changed the `grants:` row to `release/3.0`, then pushed. Import kept **both**:

```
"writes":[{"repo":"acme/site"},{"repo":"acme/widgets-deploy","base":"release/2.0"},{"repo":"acme/widgets-deploy","base":"release/3.0"},{"repo":"ghe.example.com/acme/widgets","base":"main"}]
```

Neither spelling is silently dropped. The rows are the union of what the commit says (`appendUniqueRows`), so the template's tasks may push to both bases. The daemon gives no warning that the two spellings disagree. I left this as it is: it grants nothing beyond what the committed file names. A lint note could be a follow-up if an operator would rather be told.

### Delete one spelling only

These steps ran as separate pushes (`output/config-repo-log.txt`):

1. Make both spellings agree on `release/3.0`: one row, `acme/widgets-deploy@release/3.0`.
2. Delete the `grants:` line: the row survives (from `writes:`).
3. Put the `grants:` line back and delete the `writes:` entry: the row survives (from `grants:`).

### Template-filed task's push set: not done

`POST /api/tasks {"templateId":"template-1"}` was refused with `the deployment has no GitHub credential, so it cannot reach acme/widgets; … no git connection on ghe.example.com …`. The walkthrough deployment holds no forge credential, and I ran out of run time before configuring one. The write rows the task would inherit are the imported ones above.

What remains is checking the git proxy's `GitScope` push set on a dispatched task. It needs a GitHub token (any value works against the simulator) under `<data-dir>/secrets/github` and a git connection for `ghe.example.com`.

## Tests run

- `go test ./pkg/orchestrator -run 'TestRepairsHoldTheirRepositorysDefaultCIGrant|TestAnOpenRepairWithoutTheCIGrantGainsItOnTheNextRedRun|TestARepairDeniedCIReadSaysWhy'`: pass, and the two new tests fail without the fix.
- `GRAIN_REQUIRE_POSTGRES=1 go test ./pkg/orchestrator` passed. After the review fix it was re-run, together with `./pkg/model -run CIRead|CIGrant`, also with `GRAIN_REQUIRE_POSTGRES=1`.
