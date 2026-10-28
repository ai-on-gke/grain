# Release cut replay, upgrade allow-list, suite-run team refusal: 2026-10-07

Section 8 ("Release and suite fixes") of `docs/test-plans/2026-10-06.md`, run by task 2386
(filed by task 2231).

## Headline: the daily cut cannot succeed, because the back-merge conflicts

**`candidate` has not been cut since `v1.8.0-rc.1` (2026-10-05 00:38 UTC, `98f2a031d`), and
today's heads show why.** Before the cut moves `candidate`, `scripts/release-candidate-cut.sh`
merges `origin/candidate` into `main` (`git merge --no-ff`). That merge **conflicts in 45 files**
at today's heads. The script then stops with "Cannot cut release candidate until conflicts between
candidate and main are resolved" and exits 1, before tagging or pushing anything. The same merge
already conflicted at the 18:00 UTC cut time on 10-05 (12 files) and on 10-06 (38 files). The scratch
setup had nothing to do with it. Every cut since `v1.8.0-rc.1` has hit this wall, and it gets
worse each day.

Cause: `candidate` carries work with no equivalent patch on `main`. The script's own count at today's
heads is "228 commit(s) on origin/candidate not on main, 81 of them with no equivalent patch on main
(missed ports)". Of those 81, I classified them by subject and trailer:
- 28 have a commit with the same subject on `main`. These are ports whose conflict resolution
  changed the patch.
- 5 carry a `Backported-from:` / cherry-pick trailer.
- 48 have no counterpart on `main` at all.

Some of the 48 are candidate-only reports, but many are code:
- `f993eaa37` (image signing optional);
- `ac6b5c067` (relay WebSocket handshake);
- `d321da096` (postgres `OCTET_LENGTH`);
- `ec699459a` / `83a51754f` (ignored identity header);
- `da163b114` (schedule on a repo-bound template);
- `2f8e13f54` (GitHub host validation);
- a dozen `fix(ui)` commits from the 2026-10-05 walkthroughs.

Conflicting files at today's heads include `cmd/grain/init.go`, `cmd/grain/demo.go`, `pkg/daemon/*`,
`pkg/demo/*`, `pkg/relay/client.go`, `pkg/ui/{auth,principal,server,tasks}.go`, `pkg/upgrade/upgrade.go`,
`scripts/setup.sh` and `ui/src/App.jsx`. The package moves (`pkg/daemon`, `pkg/demo`) landed on both
branches in different shapes, so this will not resolve itself.

This is not something this task can fix on its branch. It needs either a human-resolved reconciliation
of `candidate` into `main`, or a decision to change the back-merge model. It is filed as a follow-up
(see "Follow-ups"). Without `gh` and a token in the cut's sandbox, the script prints the reconcile
PR's body but files nothing. So the only trace of each failed cut is that task's closing note.

## Environment

- Sandbox: Debian VM with systemd as PID 1, docker, Go, Node, 4 vCPU / 7.6 GiB, no `/dev/kvm`.
  It served as the throwaway host for `grain init` and `setup.sh`.
- Tested commit: `candidate` `dafd69778` ("Merge pull request #3156", 2026-10-07 15:45 UTC). This
  branch is cut from it. `main` was `7819e615d` (2026-10-07 16:28 UTC).
- Every commit in scope is on `origin/candidate` (`git branch -r --contains`): `32c862616`,
  `f993eaa37`, `b00d50ddf`, `cccea68b3`, `85ece1c7a`, `0c1283c2b`, `2d304de13`, `13cef3a26`,
  `fe9f874d8`, `8c0895b34`.
- Has a cut moved `candidate` since the plan? No. `git merge-base origin/candidate origin/main` is still
  `98f2a031d`. The newest `v1.*` tag is `v1.8.0-rc.1` (`98f2a031d`), with `v1.7.0` at `a9469b26f`
  and `latest` → `a9469b26f`. At 18:05 UTC, today's 18:00 cut had not moved `candidate` either.

## 1. Release cut replay (`32c862616`)

Scratch bare remote under `/tmp/cut`: today's `origin/main` and `origin/candidate`, every `v1.*` tag
and `latest`. The script ran from a fresh clone of it, with `RELEASE_NOTES_HIGHLIGHTS` set to a
placeholder file. Nothing was pushed to the real remote's `candidate`, `main` or tags.

| Run | Remote | Result |
| --- | --- | --- |
| A, today's heads as they are | unmodified | **exit 1**: back-merge conflict, 45 files (above). Remote refs unchanged. Nothing was tagged or pushed. |
| B, success path | `main` = `origin/main` plus a scratch-only `merge -s ours origin/candidate`, so the back-merge has nothing to do | exit 0. `v1.8.0` → `dafd69778` (candidate head), `latest` → `dafd69778`, `v1.9.0-rc.1` → the new candidate head. `main` = `candidate` = scratch main plus `docs(release): add release notes for v1.8.0`. "Back-merge count: 0 … 0 … (missed ports)". |
| C, rc tag refused | B plus a `pre-receive` hook refusing `refs/tags/v1.9.0-rc.1` | **exit 1**. `! [remote rejected] v1.9.0-rc.1 -> v1.9.0-rc.1 (pre-receive hook declined)` then `Error: could not push v1.9.0-rc.1 to origin; candidate moved but is untagged.` No rc tag on the remote. `candidate` moved. |
| D, candidate refused | B plus a hook refusing `refs/heads/candidate` | **exit 1**. `! [remote rejected] candidate -> candidate (pre-receive hook declined)` then `Error: could not push candidate to origin; v1.9.0-rc.1 was not cut.` `candidate` unchanged. No rc tag. |

In C and D the promotion (`v1.8.0`, `latest`, the notes on `main`) had already landed when the cut
failed. That is by design: the promotion runs first, and a rerun keeps a pushed release tag
(`TestReleaseCandidateCutScript_KeepsAPushedReleaseTag`).

Driver: `/tmp/cut/replay.sh <name> [hook body]` builds the remote, runs the script and diffs the remote's
refs before and after. It is reproduced here so the run can be repeated:

```bash
git init -q --bare remote.git
git -C ~/work push -q $PWD/remote.git origin/candidate:refs/heads/candidate 'refs/tags/v1.*:refs/tags/v1.*' refs/tags/latest:refs/tags/latest
git clone -q --no-checkout ~/work seed && git -C seed checkout -q --detach $(git -C ~/work rev-parse origin/main)
git -C seed merge -q -s ours --no-edit $(git -C ~/work rev-parse origin/candidate)   # B-D only
git -C seed push -q $PWD/remote.git HEAD:refs/heads/main
printf '#!/bin/sh\nwhile read o n r; do [ "$r" = refs/heads/candidate ] && exit 1; done; exit 0\n' > remote.git/hooks/pre-receive; chmod +x remote.git/hooks/pre-receive   # D
git clone -q remote.git clone && cd clone && RELEASE_NOTES_HIGHLIGHTS=/tmp/hl.md bash ~/work/scripts/release-candidate-cut.sh
```

Reproducing the conflict on its own, with no remote:
`git checkout --detach origin/main && git merge --no-ff --no-commit origin/candidate`. At the 10-05 and
10-06 cut times, use `git rev-list -1 --first-parent --before='2026-10-0N 18:00 +0000' origin/{main,candidate}`.

### Finding R1 (fixed here): a refused back-merge push to `main` was hidden

The back-merge pushed `main` with `git push origin main 2>/dev/null || echo "Main branch push handled."`,
which says the same thing whether the push was accepted or refused. It stays non-fatal, as the
step 3 notes push is, because the merge rides on the new candidate. But git's reason now reaches
the log, and a `Warning: could not push the back-merge to main on origin; …` names the ref. New test:
`TestReleaseCandidateCutScript_ReportsARefusedBackMergePush` (`tests/deploy`). It fails against the old
script with "the refused back-merge push was reported as handled".
`TestReleaseCandidateCutScript_FailsWhenTheRemoteRefusesTheCut` now asserts that the error names the
refused ref (`could not push candidate to origin`, `could not push v1.1.0-rc.1 to origin`). Before, it
accepted any "could not push".

## 2. Image signing optional, and host tuning (`f993eaa37`, `b00d50ddf`, `cccea68b3`, `85ece1c7a`)

These pass in `tests/deploy`:
- `TestTheReleaseDoesNotRequireImageSigning`
- `TestARequiredSignatureWithNoKeyStopsBeforeTheBuild` (`REQUIRE_IMAGE_SIGNING=1` still forces signing)
- `TestTheReleaseSignsEveryImageBeforeMovingATag`
- `TestSetupRefusesAnUnsignedImageAndWarnsWithoutAKey`
- `TestAHandRunTouchesNoHostNetworking` and `TestGrainCIAndAnExplicitOptInStillTuneTheHost` (`85ece1c7a`)
- `TestTheMTUClampRestartsDockerOnlyWhenDaemonJSONChanged`
- the `grain_workflows` parse and release-workflow tests

From reading `scripts/release-images.sh`: `1`, `true` and `yes` all mean "required", and with no key
it says so before the build. I did not run a real release-images job: it publishes images, and
`package-publish` is CI-only.

## 3. `grain init` and `setup.sh` keep `-upgrade-allowed-branches` (`0c1283c2b`, `2d304de13`)

Host: this sandbox VM, as root, with a separate checkout (`/opt/grainsrc`) so root's build output
stayed out of the working tree.

`grain init` (`-platform=linux-container`, binary built from the tested commit):

| Step | Command | Outcome |
| --- | --- | --- |
| install | `grain init -platform=linux-container -repo-root /opt/grainsrc -data-dir /var/lib/grain -upgrade-allowed-branches='main,release/*'` | Unit `ExecStart` carries `-upgrade-allowed-branches main,release/*`. Daemon running. Exit 10 for an unrelated `[manual]` step: the 7.6 GiB host cannot hold one 8192 MiB sandbox. |
| stop, re-run without flag | `systemctl stop grain-daemon`, then the same command minus the flag | `[done] start the daemon … grain-daemon.service is running, with the -upgrade-allowed-branches main,release/* the unit it replaced passed`. The unit keeps the value. |
| malformed | `… -upgrade-allowed-branches='['` | **exit 1** before any step: `hostinit: upgrade allowlist pattern "[" is malformed (syntax error in pattern): each must be a path.Match pattern, e.g. main or release/*, or ** for any branch`. The unit is byte-identical and the daemon still active. |

`setup.sh` (`GRAIN_INSTALL_MODE=host`, `GRAIN_HOST_BINARY` = the same build, over the `grain init` unit after removing it):

| Step | Command | Outcome |
| --- | --- | --- |
| install | `GRAIN_UPGRADE_ALLOWED_BRANCHES='main,release/*' setup.sh` | `==> Upgrade allowlist: main,release/* (as given)`. Unit carries it. Exit 0. |
| stop, re-run without | `systemctl stop grain-daemon; setup.sh` | `==> Upgrade allowlist: keeping main,release/* from the installed /etc/systemd/system/grain-daemon.service`. Unit keeps it. Daemon active. |
| malformed (env) | `GRAIN_UPGRADE_ALLOWED_BRANCHES='[' setup.sh` | **exit 2**: `setup.sh: upgrade allowlist pattern "[" (…) is malformed: a '[' is never closed.` The unit is byte-identical. |
| malformed (flag) | `setup.sh --upgrade-allowed-branches='main,['` | **exit 2**, same message. |

Observation only, not filed: when `grain init` installs fresh with the flag, no step line mentions the
allow-list. Only the keep path says which value it used. `setup.sh` says "(as given)" either way.
The existing `pkg/hostinit` and `tests/deploy/upgrade_allowlist_test.go` tests also pass.

## 4. Suite run into another team's config repo (`13cef3a26`)

New Playwright spec `ui/e2e/suite-run-team-config-refusal.spec.js` runs against the real
`grain demo` (its own instance, `-addr 127.0.0.1:0 -open=false`) and the built bundle. The setup:
- `admin` (the default team's seeded admin) and `carol` register;
- the deployment admin list becomes `["sysadmin"]`, so `admin` is a team admin only;
- team `payments` is created with `carol` as admin, and its config repo is `grain/config-payments`.

As `admin`, the spec:
- opens Run… on "Smoke Verification Suite";
- checks that the repo picker offers `acme/widgets` and not `grain/config-payments`;
- types `grain/config-payments` through "Other…", branch `main`, and clicks Run.

Outcome:
- `POST /api/suite-runs` answers 400 `{"error":"only team \"payments\" can submit to grain/config-payments"}`;
- the page shows that text, and the dialog stays open with the typed repo;
- `GET /api/tasks` lists the same task ids as before, and `GET /api/suite-runs` the same count.

Chromium: 1 passed. No stubs: no GitHub or session path is touched.

Reading the code, the refusal also applies when the named team does not exist: `IsTeamConfigRepo`
parses the name, so the check needs no team row. The demo's API answered the same 400 before the
`payments` team was created.

## 5. `query_merge_queue {branch: "main"}` (`8c0895b34`)

New e2e test `tests/e2e/mergequeue_query_branch_test.go`
(`TestQueryMergeQueueListsADefaultBranchPullRequestUnderItsBranch`):
- a real orchestrator cycle opens and queues a pull request for a task with **no Base**
  (`waiting_ci`, behind a running check on githubsim);
- the real `grain mcpserver` subprocess (`-server`, `-task`, `-data-dir` with a `ci` credential,
  `-github-host` → githubsim) reads it through a real `pkg/ui` server.

What it asserts:
- `{branch: "main"}` lists it with `Repo: acme/widgets | Branch: main | State: \`waiting_ci\``;
- `{}` names `Branch: main` too;
- `{branch: "release"}` leaves it out.

Reverting the reader to print `t.Base` fails all three subtests with `Branch:  `. The existing unit
test (`pkg/daemon/mcpserver_mergequeue_test.go`) stubs the daemon's HTTP API; this test does not.

Real tool, read-only, from this run (2026-10-07 ~18:40 UTC):
- `query_merge_queue {repo: bwsalmon/grain, branch: main}`: pipeline Running, no batches in
  flight, 14 queued pull requests. All are `Branch: main`, `[BLOCKED]`, `merge_failed`: tasks
  660, 557, 554, 560, 572, 569, 571, 578, 579, 587, 582 ("Fix base branch release on bwsalmon/grain
  main"), 553, 195, 194. So default-branch PRs are listed under `main` live, which is the fix
  working.
- `query_merge_queue {}`: 20 entries across `gke-labs/bouncer` (`main`, `ant-release`,
  `feature/ui-dashboard-routing`) and `bwsalmon/grain` (`main`). Every one has a branch. None is on
  `candidate`.

Observation, not filed: those 11 "Fix base branch release" tasks (553-660) are weeks-old blocked
leftovers that a human could close. Retrying them is a human's call.

## 6. Relayed `grain-ci` (`fe9f874d8`)

`TestProcessResultPassesOverARequestedGrainCI` drives the real `orchestrator.ProcessResult` with a
real store and a githubsim client. The relay (`relayProposedTasks`) is the code under test, not a
stub, so per the plan no e2e case was added. It passes.

## Stubs

- Release cut: a local bare remote with pre-receive hooks stands in for GitHub. In runs B-D, the
  scratch `main` was given a `-s ours` merge of `candidate` so the back-merge was empty. That is the
  only way to reach the push paths while finding A holds.
- `query_merge_queue` e2e: githubsim behind an httptest server (`githubHostServer`) as GitHub.
  Everything else is real.
- Suite run: none.
- `grain init` / `setup.sh`: none. Real systemd, real units, on a throwaway VM.

## Follow-ups

- Filed: reconcile `candidate` into `main` so the daily cut can run, and stop the back-merge from
  blocking the cut indefinitely (finding A above). It needs a human decision on how.
- Release-watchdog: since no cut has happened for three days, the watchdog should already have
  raised this. It is worth checking why it did not. That belongs to the same follow-up.

## CI verdict

Recorded in the commit that adds this file. See the pull request for the final `wait_for_checks`
result.
