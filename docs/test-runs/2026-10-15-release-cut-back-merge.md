# Walkthrough: Release Cut and Promotion (2026-10-15 plan, §10)

Task 3953, filed by the nightly test-plan task 3749. Executes `docs/test-plans/2026-10-15.md` §10
("Release Cut and Promotion: Fast-Forward Back-Merge, Held Candidate Notes, Force-Move Refusal")
against a real `grain demo` with grain's own git proxy in front of a simulated GitHub.

## Base check

Branch base: `origin/candidate` at `591f0f35f`.

| Commit | On `candidate` |
| --- | --- |
| `c2f972a77`, `790db777d`, `ddb956200`, `a929c668b`, `5dc349eb7`, `def78391f`, `aab2ad35f`, `3c68f4657`, `5547adfd4` | yes |
| `acd835d11` (comment on a merged task grain did not port) | **no**, `main` only |

The `acd835d11` step was **not run**, because the commit is not on `candidate`. `-mechanical-port`
was therefore not needed.

## Environment

- Sandbox: Debian, go1.26.6, git from the image. The binary was built from this branch's base with
  `go build -o /tmp/wt/grain ./cmd/grain`.
- Server:

  ```
  grain demo -addr 127.0.0.1:18080 -open=false -default-target-repo scratch/release-cut \
    -github-sim -git-proxy -fake-ci -scheduled-workflows -reconcile 2s
  ```

  - `-default-target-repo scratch/release-cut` is used so that the simulated repository can never be
    mistaken for bwsalmon/grain. Nothing in this walkthrough pushed anywhere except the demo's
    loopback forge.
  - `-scheduled-workflows` was added to the plan's flags. It is the only demo mode that runs the
    `declarations` reconciler against the simulated repo, and the git proxy can only grant a declared
    `push:` set to a task filed from a repository-declared template.
- Git proxy: `http://127.0.0.1:38075`. It forwards to the simulated forge on `127.0.0.1:33609`. The
  decisions are read from `<demo dir>/state/git-proxy/audit.log`.
- The demo needed two operator settings, both made through grain's own API:
  - `PUT /api/repos/scratch/release-cut/declarations {"declarations":true}`.
  - `PUT /api/teams/default {"targetRepos":["acme/widgets","scratch/release-cut"]}`. The demo's
    team is restricted to `acme/widgets`, and the declarations reconciler skips repos outside a
    team's target repos. Until this was set it skipped the scratch repo without logging anything.
- The cut task was task 13, filed with
  `POST /api/tasks {"team":"default","repo":"scratch/release-cut","templateId":"repo:template:scratch:release-cut:release-candidate"}`.
  - Its run and sandbox token came from `POST /api/e2e/git-proxy/runs {"taskId":"13"}`, giving
    sandbox `demo-sandbox-13-1` and branch `grain/task-13`.
  - The scratch clone used `http.extraHeader=Authorization: Basic base64(x:<token>)`.

## Scratch repo layout

The repo was seeded straight into the demo's bare repo (`file://`, as `-scheduled-workflows`
documents).

- `main` (`2329198`) holds:
  - `.grain/templates/release-candidate.yml`, carrying the same `push:` set as this repo's real
    template: `candidate` with force, `main`, `reconcile-candidate-to-main` with force, `refs/tags/v*`,
    and `latest` with force.
  - `scripts/{release-candidate-cut,release-push-main,release-notes,candidate-gate}.sh`, copied from
    this branch.
  - `app.txt` and `tests/security/routes_test.go`.
  - `fix: guard empty config`, an **adapted port** of the candidate fix. It has the same subject, a
    different patch to `app.txt`, and a `(cherry picked from commit 0518bc5…)` footer.
  - `feat: main-only feature`.
- `candidate` (`768229d`) holds:
  - `fix: guard empty config` (`0518bc5`), the original fix.
  - `fix: candidate-only timeout`, a plain fix that exists only on candidate.
- Tag `v1.0.0-rc.1` is annotated, dated 2026-10-13, on the common base.

## Steps and outcomes

### 1. Seed

Done as above. `main` and `candidate` diverge, the adapted port and the candidate-only fix are both
present, and the template is declared. The demo log shows
`scratch/release-cut at 232919806595 declares 1 template(s)`.

### 2. The cut moves `main` by fast-forward through the proxy; a real force-push is refused

**2a.** `CANDIDATE_GATE=off scripts/release-candidate-cut.sh` was run in the proxy clone. **Pass.**

- `origin/candidate conflicts with main`, and the script printed `Kept main's version of these files …: app.txt`.
- `main`'s `app.txt` stayed `line2 fixed-on-main-adapted`.
- `Pushed main to 9d52d87`, then v1.0.0 and `latest` were tagged, and `candidate` and v1.1.0-rc.1
  were cut.
- Proxy decision:

  ```
  {"ref":"refs/heads/main","outcome":"allowed: declared push (update)","task":"13",
   "template":"repo:template:scratch:release-cut:release-candidate","declaration":".grain/templates/release-candidate.yml"}
  ```

  `candidate` was recorded as `allowed: declared push (force)`, and each tag as `(create)`.
- The count line read `2 commit(s) … 2 of them with no equivalent patch … (missed ports)`. One of
  those two is main's recorded adapted port, so this is **F1** (fixed).

**2b. Empty pack.** This step follows the flow `c2f972a77`/`ddb956200` were written for. **Pass.**

1. A new candidate fix was back-merged by hand.
2. The merge was pushed to the run's own branch (`grain/task-13`) first.
3. Main was then moved with `scripts/release-push-main.sh main`. The proxy allowed it as
   `allowed: declared push (update)`.
4. The step was repeated with `git push --progress` (`fb06548`). git printed
   `Total 0 (delta 0), reused 0 (delta 0), pack-reused 0`, and main moved `8dfdc00..fb06548`.

The proxy showed the fast-forward from the remote's own commits.

**2c. Real non-fast-forward.** **Pass.** Main stayed at `8dfdc00`.

- `git push --force origin <new commit on main~1>:main` sent a pack of 3 objects. It was refused
  with `refs/heads/main: could not show b16e6cc41757 is a fast-forward of 8dfdc007bcca from the
  commits this push carries or the repository already has, and its entry does not allow a force-push
  (`force: true`)`.
- `git push --force origin origin/main~1:main` (empty pack, a rewind) was refused the same way.
- The audit log recorded both refusals as `"outcome":"denied: ref not in scope"`, with no `ref`.
  This is **F2** (proposed).

### 3. A candidate change to `tests/security/` stops the cut

`fix: relax route expectation on candidate` changed `tests/security/routes_test.go` on candidate,
and `FORCE=true CANDIDATE_GATE=off` was run again. **Pass.**

- The script exited 1 with `The back-merge of origin/candidate would change tests/security on main;
  refusing to finish it without a human: tests/security/routes_test.go`.
- It wrote a reconciliation body listing the guarded change.
- Nothing was pushed: `main`, `candidate` and the tags were unchanged.
- The reconciliation commit's subject says "conflict markers" although nothing conflicted. This is
  cosmetic, and is noted only.

The change was then reverted on candidate, as a human resolution would do.

### 4. A held cut, then a second cut: the held candidate's fixes are listed once

Two fixes were added to candidate v1.1. One was `fix: held fix 2 ported to main`, cherry-picked to
main with `-x`. Main also gained `feat: main feature during hold`.

- **Cut A** (`FORCE=true`, no gate file): `Promotion held by the candidate gate: v1.1.0 is not tagged
  and latest stays where it is`. The back-merge was pushed and v1.2.0-rc.1 was cut.
- **Cut B** (`FORCE=true CANDIDATE_GATE=off`), with a new fix on the v1.2 candidate: v1.2.0 was
  promoted, and `Writing release notes for v1.2.0 (v1.0.0..v1.2.0)` committed
  `docs/releases/v1.2.0.md` to main through the proxy.

The notes' Fixes section:

```
- fix: guard empty config (560a6f615)
- fix: retry on 502 (held-candidate fix 1) (f70a4ecd3)
- fix: held fix 2 ported to main (454dd656c)
- fix: relax route expectation on candidate (07fef0d18)
- fix: v1.2 candidate fix (40da8a28f)
```

**Pass for the plan's check.** Each held-candidate fix appears exactly once, including the one that
is on main as a port.

However, `fix: guard empty config` was already released in **v1.0.0**, as candidate's `0518bc5`. It
is listed again because main's adapted port `560a6f6` is not reachable from v1.0.0. This is **F3**
(proposed).

### 5. A release named `candidate` over a branch with its own commits

A commit was pushed to `candidate` that `main` lacks, then
`POST /api/repos/scratch/release-cut/releases {"name":"candidate"}`. **Pass.**

- The release stayed `provisioning` with:

  ```
  branch "candidate" already exists and carries 1 commit "main" does not (newest d6805e7e58ca);
  release "candidate" would force-move it and drop them, so nothing is done for it -- create the
  release under another name, or delete or merge "candidate" if it is a leftover
  ```

- The refusal is one field on the release. In the following cycles the demo log carried no repeated
  refusal (0 lines).
- `candidate` was then merged into `main` in the sim (`d380093`). The next poll, 2 s later, read
  `active`.
- `candidate.latest` and `candidate.rc.1` were created at `d380093`. `candidate` itself was left at
  `d6805e7`, not force-moved.

### 6. `revert_task` for task 12 with tasks 12 and 123 merged

`grain demo` serves no MCP endpoint, because `revert_task` is served by the daemon's sandbox MCP
server (`pkg/daemon/mcpserver.go` wires `mcp.NewGitTaskReverter`). So the same `GitTaskReverter` was
driven directly, by a throwaway `go run` program that was not committed, against a clone of the
scratch repo. The clone had these merges:

- `Merge pull request #512 from scratch/grain/task-12`
- then, newer, `Merge pull request #623 from scratch/grain/task-123`

`RevertTask{TaskID: "12"}` gave `CommitSHA: fe148cc…` (#512's merge), and
`revert: task 12 (fe148cc9)` with `Reverts-Task: 12`. `t12.txt` was removed and `t123.txt` was
kept. **Pass.**

An extra probe added another deployment's newer `grain/us/task-12` merge:

- `12` and the unqualified `grain/task-12` both reverted the `us` merge.
- `grain/eu/task-12`, a deployment-qualified id with no merge of its own in this repo, fell back to
  the bare patterns and also reverted the `us` merge.

`TestGitTaskReverterDoesNotTakeAnotherTasksMerge` pins the bare-id case as a known limitation ("a
bare id cannot say which deployment it means"). This is reported as **F4** (proposed).

## Findings

### F1: the back-merge count reports adapted ports as missed (fixed here)

**Reproduction.** Run the cut with an adapted port of a candidate fix on main: same subject, a
different patch, and a `Backported-from` or `cherry picked from` footer. The line
`Back-merge count: N … M of them with no equivalent patch … (missed ports)` counts the adapted port
in M. It counts by patch-id alone, although the same script's `backmerge_classify_ports` recognises
the port and keeps main's file. In step 2a it gave 2 where the real number was 1.

**Fix.** `scripts/release-candidate-cut.sh` now classifies ports before counting, and drops recorded
ports from the second number. The line keeps its prefix, which
`tests/deploy/release_workflows_test.go` reads. `docs/release-candidate-management.md` says what is
counted.

**Test.** `TestReleaseCandidateCutScript_BackMergeKeepsMainsAdaptedPorts` now asserts
`5 commit(s) … 1 of them`. It failed on the old script ("the cut counted adapted ports as missed")
and passes on the new one. `go test ./tests/deploy/` passes in full (172 s), and so do
`go test ./pkg/gitproxy -run 'Release|BackMerge'`.

### F2: the proxy audits a refused force-push of a declared ref as "ref not in scope" (proposed)

**Reproduction.** This is step 2c. From a run whose template declares `refs/heads/main` without
`force`, push a non-fast-forward to main. The client is told precisely why ("could not show … is a
fast-forward … does not allow a force-push"). The audit entry, however, is
`{"action":"git-receive-pack","outcome":"denied: ref not in scope"}` with no `ref`. That is wrong,
because main is in scope, and it is incomplete, because the entry does not say which ref.

`pkg/gitproxy/core.go` `refuseRefUpdates` records a fixed outcome. Changing the outcome or adding
`Ref` alters what the team audit (`teamaudit.go`, whose `resource` takes `Ref`) and its exact-match
tests (`tests/e2e/team_credential_audit_walkthrough_test.go`, `ui/e2e/team-credential-audit-push.spec.js`)
show, so it is proposed rather than changed here.

### F3: release notes list a fix again when its port reached main after the release that shipped it (proposed)

**Reproduction.** The script below is standalone and needs no proxy. It needs a checkout at
`$GRAIN`.

```bash
#!/usr/bin/env bash
set -euo pipefail
G=${GRAIN:-$HOME/work}; d=$(mktemp -d); cd "$d"
git init -q --bare origin.git; git clone -q origin.git c 2>/dev/null; cd c
git config user.name t; git config user.email t@e
git checkout -q -b main; echo 0 > a; git add a; git commit -qm "chore: base"; git push -q origin main main:candidate
git tag -a v1.0.0-rc.1 -m rc HEAD -f >/dev/null; git push -q origin v1.0.0-rc.1
git checkout -q -b candidate origin/candidate; echo c > a; git commit -qam "fix: guard empty config"; C=$(git rev-parse HEAD); git push -q origin candidate
git checkout -q main; echo m > a; git commit -qam "fix: guard empty config" -m "(cherry picked from commit $C)"; git push -q origin main
run() { FORCE=true RELEASE_INTERVAL_DAYS=0 "$@" bash "$G/scripts/release-candidate-cut.sh" >/dev/null 2>&1; }
CANDIDATE_GATE=off run env   # promotes v1.0.0 (carries the candidate fix), cuts v1.1.0-rc.1
git fetch -q origin; git checkout -q -B candidate origin/candidate; echo x > b; git add b; git commit -qm "fix: new in v1.1"; git push -q origin candidate
CANDIDATE_GATE=off run env   # promotes v1.1.0 and writes its notes
git fetch -q origin; git show origin/main:docs/releases/v1.1.0.md | sed -n '/## Fixes/,/## Other/p'
```

The v1.1.0 notes list `fix: guard empty config`, which v1.0.0 already shipped. The same happens in
the other direction, to a main change released through its candidate port. Main's original sits on
the next release's first-parent line, unreachable from the previous release.

`scripts/release-notes.sh` dedupes titles only within one release. This repo's own
`docs/releases/v1.*.md` carry titles repeated across releases: one title appears in 4 files, and
several in 3.

A correct fix has to follow PR merges to the commits a port's footer names, since real ports name
the commit inside the PR rather than the merge. That is more than a scoped change, so it is
proposed.

### F4: `revert_task` with a bare id can revert another deployment's task (proposed)

**Reproduction.** This is the step 6 extra probe. Put merges of `grain/eu/task-12` (ours) and a
newer `grain/us/task-12` (another deployment) on one repo, and call `revert_task` with
`task_id: "12"`. It reverts the `us` merge.

A deployment-qualified id with no exact match also falls back to the bare `task-12` patterns and
takes the other deployment's merge. Agents usually pass the bare id, and the tool's own description
offers `"123"` as the example. The daemon knows its own branch template, so it could qualify bare ids
before the search and refuse a fallback match on another deployment's branch.

## Stubs

| Stub | Stands in for | How |
| --- | --- | --- |
| `githubsim` (demo `-github-sim`), with `githubsim.GitUpstream` + `git http-backend` over a bare repo | GitHub's REST API and smart-HTTP git host for `scratch/release-cut` | `-github-sim -git-proxy` |
| demo `-fake-ci` | grain CI runs (never executed; not needed by any step) | `-fake-ci` |
| `POST /api/e2e/git-proxy/runs` | dispatch starting the cut task's run and minting its sandbox token | step 2 onward |
| No `gh` / `GITHUB_TOKEN` in the cut | GitHub check-runs lookup and draft-release creation | the script's own "not available" branch |
| No registries | `promote.yml` image retagging is outside the script | — |

Grain itself was not mocked: the git proxy, its ref policy and audit, the store, the declarations
reconciler and the releases reconciler were all real. No registry was contacted.

## Not run

- **`acd835d11` step** (comment on a merged task grain did not port): `acd835d11` is not on
  `candidate`.
- **`revert_task` through MCP:** run through the same `GitTaskReverter` the daemon wires, because
  `grain demo` has no MCP endpoint.
- **CI on the cut's own branch before moving main:** the cut's "run grain CI on the task branch"
  phase is the cutting agent's job, and `-fake-ci` executes nothing. The empty-pack push that follows
  it was walked (2b).

## Proposed follow-ups

- The proxy audit records the refused ref and the real reason for a declared ref's refusal (F2).
- The release notes do not relist changes an earlier release shipped through a port (F3).
- `revert_task` resolves bare and qualified ids within this deployment's own branches (F4).

## CI verdict

Grain CI full suite, run `62c5aad41eefb6ff` on `c3103c0` (the fix and this report): **success**.
The jobs were frontend, go, installer, lint, package cache, postgres, relay image, run host, terraform
and ui e2e, all green on the first run. The commit that adds this line changes only this report.
