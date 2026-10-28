# Test run: per-direction `port-types` port filter (2026-10-11)

Walkthrough of `docs/test-plans/2026-10-11.md` §1 against `candidate`, by task 3137 (filed by the
nightly test-plan task 2955).

## Environment

| | |
|---|---|
| Base | `candidate` at `b2ce41d09` (Merge pull request #3485) |
| Commits under test | `0a3426079` (feature: `port-types`, `decidePort`/`portSkip`, `RepoConfig.PortTypeFilter`), `8f0b6c410` (this repository's `port-types` declaration and `scripts/backport-triage.sh`). `git branch -r --contains` lists `origin/candidate` for both |
| Go | go1.26.6 linux/amd64 |
| Node | v20.19.0 (not used: no UI in scope) |
| git / bash / awk | git 2.39.5, GNU bash 5.2.15, mawk 1.3.4 20200120 (Debian's default `awk`, which the CI runner uses too) |
| OS | Linux 6.1.0-53-amd64 (grain sandbox) |
| Store | embedded SQLite (`sqlite.Open(sqlite.DefaultConfig(tmp))`), real `model.Store` |
| Flags | `go test ./tests/e2e/ -run 'TestPortTypesWalkthrough$' -count=1 -v`; `go test ./tests/deploy/ -run TestBackportTriage -count=1` |

## Stubs

| Boundary | Stub | Why |
|---|---|---|
| GitHub REST API | `githubsim` behind `github.NewClient` (`syncedSim`), served by `githubHostServer` | external forge |
| GitHub git endpoint | `git http-backend` over a local bare repository (`gitHTTPBackend`) | external forge |
| GitHub mergeability | `markPullRequestClean` on every open pull request between cycles | GitHub computes it asynchronously |
| Grain CI executor | `greenCI`, which answers every run green at once | the executor runs jobs in sandboxes this harness does not have. The merge queue's reading of the verdict is real |
| Model session | `scriptAgent` (antigravity test steps running a shell script; port tasks follow the port body's `git cherry` + `cherry-pick -x` recipe) | live model provider |

Real components: `orchestrator.RunCycle` (dispatch, close-out, merge queue, `filePorts`/`decidePort`/`portSkip`), the
declaration sync (`repodecl.Load` on a clone of the bare repository via `bareDeclarations`), `model.Store` on SQLite, git.
`scripts/backport-triage.sh` ran unmodified against a scratch bare origin and clone. Nothing was stubbed for it.

## Step 1: base check

```
git fetch origin
git branch -r --contains 0a3426079   # origin/candidate, origin/grain-merge-queue/candidate, ...
git branch -r --contains 8f0b6c410   # origin/candidate, ...
```

Both are in the base.

## Step 2: merges through the real queue (`TestPortTypesWalkthrough`)

New test in `tests/e2e/port_mechanical_walkthrough_test.go`. The repository `acme/porttypes` declares on `main`:

```yaml
port-branches: [main, candidate]
port-types:
  main->candidate: [fix, revert]
```

`candidate` is cut from `main` after a seed `feat: add legacy`. After the first cycle's declaration sync,
`RepoConfig.PortTypeFilter("main","candidate") = [fix revert]` and `("candidate","main") = nil`, as expected.
The sync logged `port-types: (blank) -> "main->candidate:fix,revert"`.

Each row is one task whose scripted agent commits as shown. Its pull request is merged by the real merge queue.

| # | Merged onto | Commits in the pull request | Expected | Observed |
|---|---|---|---|---|
| 1 | main | `feat: add alpha` | no port; type skip | no port. Log: ``orchestrator: not porting acme/porttypes#9000 (task 2): `candidate`: not a fix/revert change and no `Port: yes` footer (`main` ports only those types to `candidate`); it reaches `candidate` some other way, such as the next release cut`` |
| 2 | main | `feat: add beta` + `fix(beta): mend beta` | port to candidate | port task filed (base `candidate`), no skip line |
| 3 | main | `feat: add gamma` + `Port: yes` footer | port | port filed |
| 4 | main | `fix: mend delta` + `Port: no` footer | no port; opt-out skip | no port. Log: ``not porting acme/porttypes#9004 (task 7): `candidate`: the change carries a `Port: no` footer`` |
| 5 | main | `Revert "feat: add legacy"` (`git revert`, candidate has `legacy`) | port | port filed |
| 5b | main | `Revert "feat: add alpha"` (alpha never reached candidate) | see finding F3 | port filed. Its agent run finds nothing to pick and the task parks in `awaiting_ack` |
| 6 | main | `fix!: mend epsilon incompatibly` | port | port filed |
| 7 | main | `feat: add zeta` + `fix: a hotfix made on candidate` with `Backported-from: <candidate-only sha>` | no port: the only fix is itself a port | no port. Log: the type-skip line for task 14 (`#9010`), as in row 1 |
| 8 | candidate | `feat: add eta on candidate` | port to **main** (no `candidate->main` entry) | port filed with base `main` |

The port tasks that were filed were themselves dispatched and merged onto `candidate` (and `main` for row 8). Each one logged
`not porting …: the task is itself a port`, so nothing bounced back.

## Step 3: operator layer and malformed declarations

**API surface.** No HTTP route, CLI verb or UI pane reads or writes the operator columns `port_types` or `port_branches`.
`SetRepoSettingsRequest`/`RepoDefaults` in `pkg/ui/repos.go` carry neither, and `GET /api/repos/{owner}/{name}/settings`
does not show them. The grain/config state repository (staterepo import of the `repo_config` row) is the only writer. The
walkthrough therefore wrote the column the way that import does, with `store.PutRepoConfig` on the stored row, inside the
same running harness. See finding F2.

| Operator config | Declared | `PortTypeFilter(main,candidate)` | Merge on main | Expected | Observed |
|---|---|---|---|---|---|
| `PortBranches [main candidate]`, `PortTypes {main->candidate: [fix]}` | `[fix, revert]` | `[fix revert]`: the declaration widens it | `revert: drop theta` | port | port filed |
| same | same | same | `feat: add iota` | no port | no port, type-skip line (task 17) |
| `PortBranches [main candidate]`, no `PortTypes` | `[fix, revert]` | `nil`: the declaration cannot narrow an operator pair | `feat: add kappa` | port | port filed. Nothing logs or shows that the declared filter was ignored (F2) |

**Malformed declarations.** Each was pushed to `main`'s `.grain/settings.yml` and followed by one cycle.

| Declaration | Sync result (`RepoDeclarationSync.LastError`, also returned by `RunCycle`) | `DeclaredPortTypes` afterwards |
|---|---|---|
| `main-candidate: [fix, revert]` | `reading acme/porttypes's declarations: .grain/settings.yml: port-types: "main-candidate" is not a direction: write it <from>-><to>, for example main->candidate` | last good value kept: `main->candidate: [fix revert]` |
| `main->candidate: []` | `… port-types: main->candidate names no types: leave the direction out to port every merge, or set port-branches to port none` | last good value kept |
| `main->release: [fix]` (unknown branch) | **no error**: accepted | `{main->release: [fix]}`. The `main->candidate` filter is gone (nil), and nothing says `release` is not a paired branch (F4) |

## Step 4: `scripts/backport-triage.sh` on a scratch clone

Crafted history: a bare origin with `main` and `candidate` cut from it. The settings file is in the clone's working tree,
as in the triage workflow.

- candidate: C1 `fix: hotfix made on candidate`, C2 `feat: add eta on candidate`, C3 `chore: candidate-only tweak` + `Port: no`
- main: M1 `feat: add alpha`, M2 `fix(beta): mend beta`, M3 `feat: add gamma` + `Port: yes`, M4 `fix: mend delta` + `Port: no`,
  M5 `Revert "feat: add alpha"`, M6 `fix!: mend epsilon incompatibly`, M7 `fix: hotfix made on candidate` + `Backported-from: C1`
  (a different patch), M8 `Update the README` (untyped), M9 `FIX: shouting fix`, M10 `revert: drop theta`

Command: `cd clone && [PORT_TYPES=…] bash ../backport-triage.sh`

| Run | Expected missing (main→candidate) | Expected skipped | Observed | |
|---|---|---|---|---|
| settings `main->candidate: [fix, revert]` (flow list) | M2 M3 M5 M6 M9 M10 | M1 (feat) M4 (Port: no) M7 (port of C1) M8 (untyped) | `6 missing, 4 skipped`, same sets | ✅ |
| same, candidate→main | C2 | C1 (already ported), C3 (Port: no) | `1 missing, 2 skipped` | ✅ |
| `PORT_TYPES=` (empty) | M1 M2 M3 M5 M6 M8 M9 M10 | M4 M7 | `8 missing, 2 skipped` | ✅ |
| `PORT_TYPES='main->candidate:fix'` | M2 M3 M6 M9 | M1 M4 M5 M7 M8 M10 | `4 missing, 6 skipped` | ✅ |
| `PORT_TYPES='candidate->main:fix main->candidate:fix,revert'` | main→candidate as row 1; candidate→main none | C1 C2 (feat) C3 | `6/4` and `0 missing, 3 skipped` | ✅ |
| settings as a **block list** (`main->candidate:` / `- fix` / `- revert`) | as row 1 | as row 1 | **`8 missing, 2 skipped`**: no filter read | ❌ F1 |
| settings `port-types: {main->candidate: [fix, revert]}` (flow mapping, the plan's own spelling) | as row 1 | as row 1 | **`8 missing, 2 skipped`** | ❌ F1 |
| settings `"main -> candidate": [Fix, revert]  # c` | as row 1 | as row 1 | `6 missing, 4 skipped` | ✅ |

With the fix below, the block-list and flow-mapping rows give `6 missing, 4 skipped`. So do a two-direction flow
mapping and a block list whose `-` items are not indented (the second direction then gives `0 missing, 3 skipped`).
An empty list `[]` reads as no entry, which is what grain's parser refuses outright.

## Findings

**F1. The triage script ignored block-list and flow-mapping `port-types` (fixed).** The script's awk read only a flow list
on each direction's own line. `port-types: {main->candidate: [fix, revert]}` and the block form both parsed as "no entry",
so the nightly triage reported every feature missing from `candidate`, while grain (a real YAML parser) ported only fixes.

Reproduction: see the step 4 table. In short, with that settings file in a clone that has a `feat:` on main,
`bash scripts/backport-triage.sh` prints `PORT <feat sha> main -> candidate`.

Fix: `scripts/backport-triage.sh` now reads all three spellings. Regression test:
`tests/deploy/release_workflows_test.go` `TestBackportTriageScript_ReadsEveryPortTypesSpelling` (5 spellings). It fails
for 3 of them on the old script and passes on the new one.

**F2. The operator layer has no API, and an ignored declaration is silent (follow-up filed).** `RepoConfig.PortTypes` and
`PortBranches` can only be set through the grain/config state repository. No route reads or writes them, and the Repo
settings response does not show them. When an operator pairs the branches without a list, a repository's
`port-types` is ignored (correctly: it cannot narrow). But nothing tells the repository or the operator: the declaration
sync logs the declared value as accepted.

Reproduction: `TestPortTypesWalkthrough`'s "feat: under an operator pair" case.

*Addressed by task 3192:* `GET /api/repos/{owner}/{name}/settings` now reports `portBranches`, `declaredPortBranches`,
`pairedBranches`, `portDirections` (each direction's operator list, declared list and the composed filter) and
`portWarnings`, all read-only. `RepoConfig.PortTypesWarnings` names an ignored declaration, and the sync logs it and the
repository page's declaration warnings show it. There are still no setters: the state repository remains the only writer.

**F3. A revert of a change the target never received is still ported (follow-up filed).** `portSkip` (and the triage
script) count `Revert "…"` as `revert` without checking whether the target has the reverted commit. Under
`main->candidate: [fix, revert]`, a feature merged on main is not ported, and its revert then is. The port has nothing to
apply on candidate, and in the walkthrough the scripted agent's cherry-pick came out empty and the task parked in
`awaiting_ack`. A real agent would at best end `nothing_needed`, after spending a dispatch.

Reproduction: `TestPortTypesWalkthrough` case 5b (`revertUnported`), and M5 in the step 4 history (listed as `PORT`).

Fixed by task 3191: a revert naming the commit it reverts (`This reverts commit <sha>` or `Reverts-Commit: <sha>`)
counts as `revert` only if the target has that commit (or, for a merge, a commit it brought in), by ancestry or by a
port's footer (`revertsAbsent` in `pkg/orchestrator/port.go`, `reverts_absent` in the triage). A comparison GitHub will
not answer still ports. Case 5b now asserts no port, and M5 is now a `skip`.

**F4. A direction naming a branch that is not paired is accepted silently (in the F2 follow-up).**
`main->release: [fix]` syncs with no error. A typo such as `main->candidtae` therefore drops the real filter without a
word.

*Addressed by task 3192:* still accepted, since an entry grants nothing and the effective pairing includes the operator's
branches, which the parser cannot see. The sync now warns that the direction names a branch that is not paired, and the
same warning is in `portWarnings` and the declaration warnings.

**F5. The e2e GitHub stub dropped `Content-Encoding` (fixed).** `tests/e2e` `gitHTTPBackend` did not pass
`HTTP_CONTENT_ENCODING` to `git http-backend`. git gzips request bodies over 1 KB, which happens once a repository has a few
dozen refs. The clone then failed with `fatal: protocol error: bad line length character` and an HTTP 500, and the
agent's run failed. The walkthrough tripped it deterministically on its 18th task. Production's `pkg/gitproxy/local.go`
already passes the header.

Reproduction: `TestGitHTTPBackendServesAGzippedRequest` (40 branches, `clone --mirror`, asserts git really sent
`Content-Encoding: gzip`). It fails without the fix and passes with it.

Other copies of the stub have the same gap: `pkg/gitproxy/live_test.go`, `pkg/gitproxy/hosts_walkthrough_test.go`,
`tests/security/gitproxy_real_push_test.go`, `cmd/grain/daemon_live_test.go`, `ui/e2e/directorserver/main.go` and
`tests/installer/installer_test.go`. They are latent until a test there clones a repository with many refs, so this run
did not change them (noted in the follow-up).

## Fixes and regression tests in this change

- `tests/e2e/harness_test.go`: `gitHTTPBackend` passes `HTTP_CONTENT_ENCODING` (F5). Test: `TestGitHTTPBackendServesAGzippedRequest`.
- `scripts/backport-triage.sh`: the settings reader takes flow lists, block lists and flow mappings (F1). Test: `TestBackportTriageScript_ReadsEveryPortTypesSpelling`.
- `tests/e2e/port_mechanical_walkthrough_test.go`: `TestPortTypesWalkthrough`, this walkthrough as a standing test. It is not
  `t.Parallel`, because it reads the skip lines off the global logger (`captureLog`) and uses `MaxWorkers: 1`.

## CI verdict

Grain CI on `grain/eu/task-3137` at `8abf27d` (the walkthrough, both fixes and this report; this verdict note is
the only later change):

- Run `7ac542bbc1ae458f`: every job green except `go`, which failed only `cmd/grain`
  `TestRelayManagerReportsStoppedOnShutdown` ("timed out waiting for the status file to report the relay connected").
  That job was red the same way on `main`, `candidate` and `grain-merge-queue/candidate` at the time ("Shared fault"). The
  change does not touch `cmd/grain`, and the test passes locally. The uncached recheck `30e64b7aee5938f0` passed, with
  `tests/e2e` and `tests/deploy` green.
- Run `bd5c2e25eeac6bfc`: `go` green. `ui e2e` failed once on `inbox-read-only-teams.spec.js` (a strict-mode
  double match of "Nothing is testing" on the repo page). That spec passed on the same commit in `7ac542bbc1ae458f`, was
  failing the same way on two other branches, and this change touches no UI. The uncached recheck `43f6bf5443935325`
  passed (523 passed, 1 skipped).

Full-suite verdict after the rechecks: green. The final `wait_for_checks` on the branch head confirms it.
