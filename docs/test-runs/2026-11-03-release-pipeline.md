# Test run — 2026-11-03 — the release pipeline (§5)

Walks §5 of [`docs/test-plans/2026-11-03.md`](../test-plans/2026-11-03.md): breaking-change notes and gate (PR #526), short soaks (#515, #525), the patch-tag guard and `release-patch.sh` (#519), the release workflow (#546), nightly as Stage 1 (#529), the watchdog and review schedule (#513), and the playbook (#544). Task 2711.

## Environment

- Debian sandbox, Go from `/usr/local/bin/go`, `jq`, `python3`. `shellcheck` 0.9.0 came from apt. `actionlint` v1.7.12 came from `go install github.com/rhysd/actionlint/cmd/actionlint@latest`.
- Base `origin/main` at `ac1f0e6d3` (2026-11-03). Tags on the real remote: `v3.0.0`, `v3.0.1`, `v3.0.2`, `v3.1.0-rc.1`, `v3.1.0` and `latest` (on `v3.1.0`). The remote has **no `v3.1.1` tag and no `release/v3.1.1` branch** (see finding 6).
- Every release script ran in a **scratch clone whose `origin` is a local bare repository**. That repository was seeded by pushing this checkout's `origin/main` and tags into it. Nothing was pushed to the real remote. `GIT_CONFIG_GLOBAL=/dev/null`, and each script ends by printing the scratch clone's local `user.*` keys: there were none.
- Items 4 and 6: `grain demo -github-sim -fake-ci -reconcile 2s` on `127.0.0.1:4391`, built from this branch. `grain release approve` used the demo's access code (`GRAIN_SERVER`, `GRAIN_TOKEN`), which acts as a person. Item 6 used `pkg/orchestrator` with `githubsim`, as explained under item 6.
- Scripts and their full output are in [`2026-11-03-release-pipeline/`](2026-11-03-release-pipeline/). Each `itemN-*.sh` takes the checkout and a scratch directory. Each `itemN.log` is that script's output after the fixes below, with `/home/debian` shortened to `~`.

## Results

| # | Item | Outcome |
|---|---|---|
| 1 | FORCE short-soak dry run on real history | Passes, but the plan's expectations are stale (finding 1). The `-pending` tag now says `Qualification: PASS`. The soak trailers are only read now, so they were walked on a hand-made soak-era tag. |
| 2 | Breaking-change section, `check-breaking`, `write` | Sections, exit 3, overrides and `write` behave as described. **Bug** in reading the previous major (finding 2, fixed). **Gap**: nothing ran the gate (finding 3, fixed in `approve`). |
| 3 | Patch guard on real tags; `release-patch.sh v3.1.1` | Guard refuses `v3.0.1`/`v3.0.2` and passes `v3.1.0`. `release-patch.sh` produced a branch and tag the guard passes. **Conflict** between the train's own patch releases and the guard (finding 4, for a person). |
| 4 | grain's approve route live | `short_soak_ok` from the route and `grain release approve -short-soak-ok` both reach `promote-release.yml:promote`. **Bug**: patch releases the train stages could be neither announced nor approved (finding 5, fixed). Agent and workflow-`env:` refusal are covered by unit tests, not live (see item 4). |
| 5 | Workflows | `actionlint` is clean. The tag filter fires on exactly the intended tags. `stage-1` needs exactly the 14 jobs in the playbook table and in `candidate-gate.sh`. **Gap**: rc tags pushed with the fallback `GITHUB_TOKEN` never start `nightly.yml` (finding 7, docs fixed). |
| 6 | `nightly-ci-review.yml` loads and fires | The checked-in file loads as a `github_workflow_run` trigger, files one review for a red nightly on `main`, ignores an rc tag's red run, and skips while a review is open. Added as a test. |

## Item 1 — the train, FORCE, and short soaks

Ran [`item1-train-dry-run.sh`](2026-11-03-release-pipeline/item1-train-dry-run.sh) ([log](2026-11-03-release-pipeline/item1.log)) on real history (`main` at `ac1f0e6d3`), with `RELEASE_MAJOR=9 RELEASE_FLOOR_MINOR=1`, so the throwaway release is `v9.1.0`. `RELEASE_MIN_INTERVAL_MINUTES` was set huge so that only FORCE could cut. CI verdicts came from fixtures (`RELEASE_RUNS_JSON`, `RELEASE_QUALIFICATION_*`). Images, notes and notify were stubbed.

| Step | Exit | What happened |
|---|---|---|
| 1a no FORCE | 0 | `v3.1.0 was tagged 1140m ago, under RELEASE_MIN_INTERVAL_MINUTES (100000000); nothing to cut yet (FORCE=true cuts anyway)` |
| 1b `FORCE=true` | 0 | `cutting v9.1.0-rc.1 on ac1f0e6d3b41`. `candidate` was moved onto it, and the stub was asked for `rc v9.1.0-rc.1`. Tag message: `Release candidate v9.1.0-rc.1` / `Cut from main.` |
| 1c `DRY_RUN=true` | 0 | `PENDING nightly: nightly.yml has not run on v9.1.0-rc.1`; `qualification=pending`. Nothing was cut. |
| 1d 14 Stage 1 jobs + `grain/release-qualification` green | 0 | Staged `v9.1.0-pending` (output `pending_version=v9.1.0`). |
| 1e approve, `GRAIN_CI_REQUESTED_BY_KIND=agent` | 1 | `only a person may approve v9.1.0 ... nothing was done` |
| 1f approve, person, no `RELEASE_SHORT_SOAK_OK` | 0 | `... (v9.1.0-rc.1, ac1f0e6d3b41, qualified)`. After the fix, the log also shows `v9.1.0: no breaking changes since v3.1.0`. |
| 1g soak-era `v9.2.0-pending`, no `RELEASE_SHORT_SOAK_OK` | 1 | `v9.2.0 soaked 1h00m of 24h, under the 22h minimum: approve it with short_soak_ok: true if that is meant, or reject it; nothing was done` |
| 1h same, `RELEASE_SHORT_SOAK_OK=1` | 1 | Refused the same way. Only the literal `true` is accepted. |
| 1i same, `RELEASE_SHORT_SOAK_OK=true` | 0 | Promoted. |

The `-pending` tag the train staged (1d):

```
Release v9.1.0 pending approval

From v9.1.0-rc.1, which passed Stage 1 (nightly.yml) and Stage 2 (grain/release-qualification).

A person approves it (grain release approve v9.1.0) or rejects it (grain release reject v9.1.0).

Qualification: PASS
```

The release tags `approve` wrote (1f, 1i):

```
Release v9.1.0

Promoted from v9.1.0-rc.1, approved by walkthrough@example.com
```

```
Release v9.2.0

Promoted from v9.2.0-rc.1, approved by walkthrough@example.com; short soak (soaked 1h00m of 24h, under the 22h minimum) accepted with short_soak_ok: true
```

The soak-era tag used in 1g–1i was made by hand in the shape PR #515's train wrote:

```
Release v9.2.0 pending approval

From v9.2.0-rc.1, soaked 1h00m of 24h (FORCE=true).

SHORT SOAK: under the 22h minimum; approving it needs short_soak_ok: true.

Soak-Seconds: 3600
Soak-Forced: true
```

## Item 2 — breaking-change notes and gate

Ran [`item2-breaking-gate.sh`](2026-11-03-release-pipeline/item2-breaking-gate.sh) ([log](2026-11-03-release-pipeline/item2.log)). It works on a scratch branch off real `main` with these additions:
- a direct `feat(api)!: drop the v1 task API`;
- a `--no-ff` merge `Merge pull request #9001 ...` whose body (the PR title) is `refactor(store): rename the run table`, and whose first commit carries `BREAKING CHANGE: a v3.1 state dump no longer imports without grain state migrate.`;
- a trailing `fix:`.

| Step | Exit | Result |
|---|---|---|
| 2a notes | 0 | `## Breaking changes` comes first and lists both. The PR's trailer text is shown beneath it. Both also appear in their type's sections. |
| 2b `check-breaking v3.2.0 v3.1.0 HEAD` | **3** | `refusing to promote v3.2.0 ... RELEASE_MAJOR=3 ... or have a person set RELEASE_ALLOW_BREAKING=v3.2.0` |
| 2c `RELEASE_ALLOW_BREAKING=v3.2.0` | 0 | `overrides the gate` |
| 2d `RELEASE_ALLOW_BREAKING=v3.3.0` | 3 | An override for another version does not count. |
| 2e `v4.0.0`, release.conf `RELEASE_MAJOR=3` | 3 | A major nobody asked for is refused. |
| 2f `v4.0.0`, `RELEASE_MAJOR=4` | 0 | `a major release over v3.1.0, as RELEASE_MAJOR=4 asks` |
| 2g `v3.2.0`, `RELEASE_MAJOR=4` | 3 | Raising RELEASE_MAJOR does not let a minor release through. |
| 2h `v4.0.0 latest HEAD`, `RELEASE_MAJOR=4` | **3 before the fix, 0 after** | `latest is major ?` — finding 2. |
| 2i on real `v3.1.0..origin/main` | 0 | `no breaking changes since v3.1.0` |
| 2j–2l `write` | 0 | `docs/releases/v3.2.0.md` minus its title and blank line is byte-identical to the body. A second `write` keeps the file and gives the same body. The default body path is `$TMPDIR/release-body-v3.2.0.md`. |

## Item 3 — the patch guard, and `release-patch.sh v3.1.1`

Ran [`item3-patch-guard.sh`](2026-11-03-release-pipeline/item3-patch-guard.sh) with fix `65b9f5b0a` (#545) and feat `eb12e842` ([log](2026-11-03-release-pipeline/item3.log)). A stub `gh` reported `release.yml` green on the branch head.

| Step | Exit | Result |
|---|---|---|
| guard `v3.0.1` | 1 | `no release/v3.0.1 branch, and v3.0.0..v3.0.1 is not fixes only (120 commits)`, then the 25 `feat:` subjects and `48 other commits that are not fix:` |
| guard `v3.0.2` | 1 | Same, with 176 commits. Run as `refs/tags/v3.0.2` under `GITHUB_ACTIONS=true`, it also prints `::error::`. |
| guard `v3.1.0`, `--latest` | 0 | `not a patch tag ... nothing to guard` |
| guard `refs/heads/main ""` | 0 | Nothing to guard. |
| `release-patch.sh --no-wait v3.1.1 eb12e842` | 1 | `is a feature, not a fix ... ALLOW_FEAT=1 to pick it anyway` |
| `release-patch.sh` with a commit `v3.1.0` already has | 1 | `v3.1.0 already carries ...` |
| `release-patch.sh v3.2.0 ...` | 1 | `not a patch release` |
| `release-patch.sh v3.1.1 65b9f5b0a` | 0 | Pushed `release/v3.1.1` (`0865f4ef182c`, v3.1.0 plus 1 cherry-pick), waited for `gh`, ran the guard (`cut on release/v3.1.1 from v3.1.0; ok`), pushed `v3.1.1`, and left `latest` on `v3.1.0`. |
| guard on that `v3.1.1` | 0 | `was cut on release/v3.1.1 from v3.1.0; ok` |
| `release-patch.sh v3.1.1` again | 1 | `origin already has the tag v3.1.1` |

Tag message: `Release v3.1.1: v3.1.0 plus cherry-picked fixes from release/v3.1.1`.

Comparison with PR #547's notes for the real `v3.1.1`:
- **Same:** the pick is `65b9f5b0a` cherry-picked with `-x`. The `git range-diff` is empty apart from the `(cherry picked from commit ...)` line. It touches `pkg/model/schedule.go`, `pkg/staterepo/decl.go` and `pkg/workflows/recurrence.go` plus three test files, which matches the notes.
- **Different:** only the SHA (`0865f4ef1` here, `0e5f0baf3` in the notes), since the committer and time differ.
- `0e5f0baf3`, `release/v3.1.1` and the `v3.1.1` tag are not on the remote at all (finding 6).

## Item 4 — grain's approve route, live

Ran [`item4-approve-route.sh`](2026-11-03-release-pipeline/item4-approve-route.sh) ([log](2026-11-03-release-pipeline/item4.log)) on the demo. Each approval first re-raises a `release.pending:<v>` notification with a SHORT SOAK body.

| Step | Result |
|---|---|
| `POST .../pending-releases/v2.8.0/approve {"short_soak_ok":true}` | `202`, `"shortSoakOk":true`. Starts `.grain/workflows/promote-release.yml:promote` on `main`, `workflow_dispatch`. |
| `POST .../v2.8.0/reject {"short_soak_ok":true}` | `400 short_soak_ok is an approval's answer: rejecting v2.8.0 needs no soak` |
| `grain release approve -short-soak-ok v2.9.0` | exit 0, `approved v2.9.0, short soak accepted: promote-release is tagging it and moving latest onto it` (`fake-ci-2`) |
| `grain release reject -short-soak-ok v2.9.0` | exit 1, `-short-soak-ok is for grain release approve` |
| `POST .../v3.1.1/approve` | **before the fix:** `400 "v3.1.1" is not a release a person is asked about: name it vX.Y.0`; after: `202` (finding 5) |
| `grain release approve v3.1.1` | before the fix: exit 1 with the same message; after: exit 0 |
| `POST .../latest/approve` | `400 ... name it vX.Y.Z` |

Not shown live:
- **Agent refusal.** The demo hands out only the operator's access code and a git-proxy sandbox token. Neither is an agent API token, so the refusal was not exercised live. `TestAnAgentMayNotApproveAPendingRelease` (`pkg/ui`) passes and covers it.
- **Workflow `env:` refusal.** `-fake-ci` runs no steps, so whether `GRAIN_CI_RELEASE_SHORT_SOAK_OK` reaches a step cannot be seen from the run. `TestRequestedByKindReachesStepsOnlyFromGrainsOwnCallers` (`pkg/ci`) passes. Note what it actually checks: a workflow, job or step `env:` naming `GRAIN_CI_RELEASE_SHORT_SOAK_OK` is **silently dropped** by `StepEnv` (`pkg/grit/engine/engine.go`), not refused when the workflow loads, as the plan says. Dropping it is safe, but nothing tells the workflow's author.

## Item 5 — workflows

Ran [`item5-workflows.sh`](2026-11-03-release-pipeline/item5-workflows.sh) ([log](2026-11-03-release-pipeline/item5.log)).

- `actionlint` (with shellcheck) on `nightly.yml`, `release.yml`, `release-train.yml`, `promote.yml` and `promote-release.yml`: exit 0, no findings. `shellcheck -S warning` on the four release scripts and their tests is also clean.
- `nightly.yml` `push.tags: ["v*-rc.*", "!v*-rc.*-*"]` under GitHub's glob rules (`*` matches anything but `/`):
  - fires on `v3.2.0-rc.1`, `v3.2.0-rc.12` and `v10.0.0-rc.3`;
  - does not fire on `v3.2.0-rc.1-discarded`, `-rc.1-retracted`, `v3.2.0-pending`, `v3.2.0`, `v3.2.1`, `v3.2.0-retracted` or `latest`.
  - Every name passes `git check-ref-format`.
- `stage-1`'s `needs:` holds 14 jobs. They are identical to the playbook's Stage 1 "Required now" row and to `candidate-gate.sh`'s `CHECK_QUALIFICATION_JOBS`, and each is a job of `nightly.yml`.
  - Jobs of `nightly.yml` outside it: `kvm-sandboxes`, `kvm-route-stress` and `real-kubernetes`. The playbook makes these required only once self-hosted runners exist.
  - All 17 job concurrency groups are keyed on `github.ref` and never cancel on `main` or a `v*` tag, so a later rc does not cancel an earlier one.

## Item 6 — the review schedule

`grain state check DIR` reads a state-repository dump (`tables/`, `schema-version`), not a repository's `.grain/schedules/`. So the checked-in file was loaded the way grain loads a repository's own declarations: `repodecl.Load`, then the `github-ci-tails` reconciler against `githubsim`. That is the new `TestTheRepositorysNightlyCIReviewScheduleFilesAReviewForARedNightlyOnMain` (`pkg/orchestrator`). It finds:
- `.grain/schedules/nightly-ci-review.yml` parses as `github_workflow_run` on `nightly.yml`, with `[failure, timed_out, startup_failure]`, `[main]`, template `workflow-review:nightly` and `overlap: skip`;
- one review task is filed for run 9103 (red on `main`), naming the run, the job and the failing test;
- nothing is filed for run 9101 (red on `v9.1.0-rc.1`, Stage 1, which the train judges) or 9102 (green);
- a second red night (9104, `timed_out`) while the first review is open files nothing.

It passes with `-count=2 -shuffle=on`. The 26 h watchdog (`pipeline.nightly-missing`) was not walked beyond its existing tests.

## Findings

1. **The plan's item 1 describes PR #515's soak train, which `aa55d3a1` (the qualification-driven train, merged in the same range) replaced.**
   - The train no longer writes `Soak-Seconds`/`Soak-Forced`, a SHORT SOAK line or `pending_soak=`. Its `-pending` tag says `Qualification: PASS`, and approving a qualified release asks nothing about soaks.
   - `RELEASE_SHORT_SOAK_OK` now only matters for a `-pending` tag left from the soak era, and still behaves as #515 describes (1g–1i).
   - Stale text that still describes the trailers as current: `design/design/release-from-trunk.design.md` (approve paragraph), the comments in `pkg/ui/release_approval.go`, `ui/src/notifications.js` (`Soak:`/`SHORT SOAK` parsing), and `pkg/capability/ciapi` (the notification title `(<rc> passed its soak)`). These were left as they are; no behaviour depends on them.
2. **Fixed — `check-breaking` misread the previous major when `<from-ref>` was a commit or `latest`.**
   - It used `git describe --tags --exact-match`. On this repository that names `latest` for `v3.1.0`'s commit, so the previous major was `?` and a major release RELEASE_MAJOR asked for was refused (2h, exit 3).
   - It now takes the newest `vX.Y.Z` tag on the commit. There are two new cases in `scripts/release-notes_test.sh`, and both fail without the fix.
3. **Fixed — the breaking-change gate ran nowhere.**
   - Playbook §8 says promotion halts on breaking changes that are not in the major release RELEASE_MAJOR asks for. But `check-breaking` was called by nothing: not the train, its `approve`, or any workflow.
   - `release-from-trunk.sh approve` now runs it, from the release the notes are written from to the pending commit. On exit 3 it refuses: nothing is tagged, `latest` is not moved, nothing is published. `RELEASE_ALLOW_BREAKING=<version>` lets the release through and is recorded in the release tag.
   - Five new cases in `scripts/release-from-trunk_test.sh`; 11 assertions fail without the fix.
   - Neither `promote-release.yml` passes `RELEASE_ALLOW_BREAKING` through yet, so for now the way past the gate is reject plus a RELEASE_MAJOR bump. Plumbing it through is a `promote*.yml` change, proposed separately.
   - Real history since `v3.1.0` has no breaking markers (2i), so the next release is unaffected.
4. **For a person — the train's own patch releases conflict with the patch-tag guard and the playbook.**
   - Since `aa55d3a1`, `fix:`/`perf:`/`refactor:`/`revert:` commits alone since a release make the train cut `vX.Y.(Z+1)-rc.1` on `main`. `docs:`/`chore:`/`test:` commits ride along.
   - `patch-tag-guard.sh`, which `release.yml` and `promote.yml` run, refuses any patch tag that is not on a `release/vX.Y.Z` branch, unless every commit since `vX.Y.0` is `fix:`.
   - [`item3b-train-patch-vs-guard.sh`](2026-11-03-release-pipeline/item3b-train-patch-vs-guard.sh) ([log](2026-11-03-release-pipeline/item3b.log)) shows this: `fix:`, `refactor:` and `docs:` since `v3.2.0` give `v3.2.1-rc.1` (exit 0), and the guard then refuses `v3.2.1` (exit 1, `and 2 other commits that are not fix:`).
   - Playbook line 273 and `docs/release-candidate-management.md` §9 also say patches are hand-made hotfixes on `release/vX.Y.Z`. Which side moves is a policy call, so this is not changed here.
5. **Fixed — a patch release the train stages could be neither announced nor approved through grain.**
   - The release-pending notification route (`pkg/capability/ciapi`), grain's approve and reject routes (`pkg/ui/release_approval.go`) and `grain release` all took only `vX.Y.0`. They now take `vX.Y.Z`, and the tests accept `v2.4.1` where they asserted it was refused.
   - The Inbox's Approve button (`ui/src/notifications.js`, `pendingReleaseOf`) still offers only `vX.Y.0`. That is a UI change needing screenshots, so it is proposed separately.
6. **`v3.1.1` is not on the remote.**
   - `docs/releases/v3.1.1.md` (PR #547) says it was cut as `0e5f0baf3` on `release/v3.1.1`, and that release.yml and the long suites are run by a person "before tagging".
   - The remote has no `v3.1.1` tag, no `release/v3.1.1` branch and no object `0e5f0baf3`, so that person's step has not happened yet, or was done elsewhere. Reported, not acted on.
7. **Docs fixed — rc tags pushed with `GITHUB_TOKEN` start no workflow.**
   - `release-train.yml` checks out with `secrets.REPO_WRITE_TOKEN || secrets.GITHUB_TOKEN`. A tag pushed with `GITHUB_TOKEN` fires no `on: push: tags` workflow, so without `REPO_WRITE_TOKEN` (or an App token) `nightly.yml` never runs on the rc, and qualification stays `PENDING nightly`.
   - The playbook says this. `docs/github-actions-ci.md` called the secret optional; its row now says what is lost without it.
8. Observations, not changed:
   - The train says `the commits since the start make a minor release` when RELEASE_MAJOR has no release yet, although the notes are from `v3.1.0`. This is wording only.
   - `approve` does not check that the pending commit is ahead of `latest`. 1i moved `latest` back onto an older commit, but only because that `-pending` tag was made by hand behind `v9.1.0`; the train never stages one there.
   - `.github/workflows/promote-release.yml` writes `inputs.version` into `$GITHUB_ENV` unvalidated. Only a person can dispatch it, but a format check would be cheap. That is a `promote*.yml` change, included in the proposal.

## Changes on this branch

- `scripts/release-notes.sh`, `_test.sh`: finding 2.
- `scripts/release-from-trunk.sh`, `_test.sh`: finding 3.
- `docs/github-actions-ci.md`: finding 7.
- `pkg/capability/ciapi`, `pkg/ui/release_approval.go`, `pkg/ui/agent_api_tools.go`, `pkg/ui/httpclient.go`, `pkg/cli/release.go` and their tests: finding 5.
- `pkg/orchestrator/nightly_ci_review_schedule_test.go`: item 6.

No `promote*.yml` or `scripts/release-images.sh` was touched.
