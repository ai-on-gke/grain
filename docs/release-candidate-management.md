# Releasing grain from trunk

This document describes how this repository (`ai-on-gke/grain`) is released: from `main`, by the
release train (`scripts/release-from-trunk.sh`), continuously -- a grain task every 20 minutes
(`.grain/schedules/release-train.yml`) -- and, since 2026-11-04, with no person's approval: an rc that
passes two-stage qualification is tagged `vX.Y.0` and becomes `latest` on its own. A person approves
only what the train may not release itself (a major bump, breaking changes, `RELEASE_AUTO_PROMOTE=false`). The design and the reasons for it are in
`design/playbook/releases.playbook.md` (which supersedes the staging soak of
`design/design/release-from-trunk.design.md`). This document is the operating manual.

There is no long-lived stabilization branch. Nothing is merged into `candidate`, nothing
is back-merged from it, and no fix is ported between branches (except onto a
short-lived `release/vX.Y.0` convergence branch when `rc.3` is needed). Every trunk release
tag names a commit on `main`'s first-parent history, or on that short-lived convergence branch.
`refs/heads/candidate` is only a pointer: the train force-moves it onto the
newest release candidate. Pull requests that target `candidate`
are refused.

## 1. The pipeline at a glance

```
main ──merge queue──▶ tests.yml / release.yml on every merge
  │
  │  the release-train task (.grain/templates/release-train.yml)
  │  (scripts/release-from-trunk.sh)
  ▼
Phase 2  if next_n > 1: check grain/rc-hold on the dropped rc's commit (floor)
           not completed ──▶ held (rc_held=vX.Y.0-rc.(N+1)), cut nothing
         newest known-good commit (LKG) ──▶ tag vX.Y.0-rc.N
         force-move refs/heads/candidate onto it, publish :vX.Y.0-rc.N, :candidate, :rc
  │
  ▼  Stage 1 (nightly.yml on the rc) ──▶ Stage 2 (grain/release-qualification)
Phase 1  qualification verdict:
           PENDING / INFRA ──▶ wait (and emit qualification_rerun when needed)
           FAIL    ──▶ tag vX.Y.0-rc.N-discarded, mark images; reconciler raises
                       pipeline.rc-failed:vX.Y.0-rc.N and keeps grain/rc-hold on
                       the commit while [rc-blocker vX.Y.0-rc.N] tasks are open;
                       once completed, Phase 2 cuts vX.Y.0-rc.(N+1)
           (the day in RELEASE_DAY_TZ has already released, or the rc was cut on
            an earlier day ──▶ tag vX.Y.0-rc.N-discarded, Discard-Reason:
            daily-cap / held-over; not red, nothing holds the next cut)
           PASS    ──▶ (RELEASE_MIN_INTERVAL_MINUTES since the last release, else wait;
                        only on a day that may release more than once)
                       tag vX.Y.0 on the rc's commit, move latest onto it; on that tag
                       publish-release.yml verifies it, promotes the images and writes
                       the GitHub Release
           PASS, but a major bump, breaking changes or RELEASE_AUTO_PROMOTE=false
                   ──▶ tag vX.Y.0-pending, publish :vX.Y.0-pending; the deployment
                       raises "vX.Y.0 is ready to promote" (Approve / Reject)
  │
  ▼  a person, for a staged release only
Approve ──▶ .grain/workflows/promote-release.yml: tag vX.Y.0, move latest, retag images
Reject  ──▶ the failed-rc path; the next run cuts vX.Y.0-rc.(N+1)
```

| Piece | What it does |
| --- | --- |
| `.grain/templates/release-train.yml` | The train: a grain task, filed every 20 minutes by `.grain/schedules/release-train.yml`, that runs `scripts/release-train-task.sh` (one tick of `scripts/release-from-trunk.sh`, in a scratch clone, with CI verdicts grain reads for it). It pushes the rc, `-pending` and `-discarded` tags, `candidate`, and -- for a qualified rc the train may release (section 5) -- `vX.Y.0` and `latest`, with the template's narrow `push:` grant (playbook §0.4). |
| `.github/workflows/release-train.yml` / `.grain/workflows/release-train.yml` | Retired as trains: `workflow_dispatch` only. The GitHub Actions one always runs dry and says what it would cut. |
| `scripts/release-from-trunk.sh` | The train: Phase 1 (judge the qualifying rc) and Phase 2 (wait on `grain/rc-hold` if held, then cut the next rc). Also has `approve`, `reject` and `rollback` modes, which only `promote-release.yml` / `promote.yml` run, for a person. |
| `scripts/candidate-gate.sh` | `select-lkg` (which commit is known-good) and `check-qualification` (Stage 1 `nightly.yml` and Stage 2 `grain/release-qualification` on the rc). |
| `pkg/orchestrator/releasequalification.go`, `releaseredrc.go` | Runs the `release-qualification` suite once Stage 1 passes, posts `grain/release-qualification`, raises `pipeline.rc-failed:<rc_tag>` (or `pipeline.release-escalated:<version>`), and keeps check run `grain/rc-hold` on a red rc's commit while `[rc-blocker <rc_tag>]` tasks are open (`docs/pipeline-steward.md`). |
| `scripts/release-images.sh` | `rc`, `discard-rc`, `pending`, `discard-pending` and `promote`: what each step publishes to GHCR. |
| `.grain/templates/release-notes.yml` | Draft `## Highlights`, run `scripts/release-notes.sh`, open a pull request for `docs/releases/vX.Y.0.md`. The train no longer files it per release: a release the train makes gets its notes from `publish-release.yml` (section 5). File it by hand for prose highlights or a staged release. |
| `.grain/workflows/promote-release.yml` | A person's Approve or Reject. `workflow_dispatch` only, and `human: required` (`.grain/triggers/release-promotion.yml`). |
| `.grain/workflows/promote.yml` | Publishes whatever release the git `latest` tag names as the `latest` image. Runs on every `release` of `main`, after each release-train run, and as the last job of an approval. |

Each train run is idempotent. A run that finds nothing to do does nothing, so
filing another one by hand (a task from the `release-train` template) is
always safe. The task has no GitHub token: grain reads `main`'s runs, the rc's
`nightly.yml` jobs and its `grain/release-qualification` check run for it
(`grain ci list --format github|github-jobs`, `grain ci checks --format
github`), and the task hands them to the script as files. It holds neither
`package-publish` nor `release-gate-read`, so it runs the script with
`RELEASE_IMAGES=off RELEASE_NOTES_TASK=off RELEASE_NOTIFY=off`: it pushes git
refs and nothing else. `publish-release.yml` publishes the images and the
notes of a `vX.Y.0` the train tags, and the deployment raises
`release.pending` itself when it sees a `-pending` tag
(`pkg/orchestrator/releasepending.go`).

`scripts/release-from-trunk.sh` with `DRY_RUN=true` prints what it would push,
publish and file, and does none of it. The script's header lists every
setting (`RELEASE_*`, `FORCE`, `DRY_RUN`).

## 2. Choosing the release candidate: the LKG commit

With nothing in qualification, Phase 2 asks `scripts/candidate-gate.sh select-lkg` for
the **last known-good (LKG)** commit: the newest commit on `main`'s
first-parent history, strictly after the floor, whose required suites are
green. The floor is the commit of the rc that was just dropped, or of the
release just staged or promoted, so a new rc is always newer than the last.

- **Required** (`CANDIDATE_GATE_REQUIRED`): `tests.yml` and `release.yml` on
  `main` (and `release.yml` on a `release/vX.Y.0` convergence branch,
  `RELEASE_BRANCH_REQUIRED`). A suite's verdict at a commit combines the
  latest result of each of its jobs across every run at that commit, which is
  the same combination the merge gate reads. So a recheck that turns one job
  green turns the suite green. `nightly.yml` runs on the rc after it is cut,
  as Stage 1 of qualification (section 4).
- **The daily cap, and commit types before `rc.1`.** Nothing is cut once
  the day (`RELEASE_DAY_TZ`, default `America/Los_Angeles`) has had
  `RELEASE_MAX_PER_DAY` (default 1) releases (`day_done=<day>`); the first
  run after midnight there cuts again (playbook §7). Only on a day that may
  release more than once, `RELEASE_MIN_INTERVAL_MINUTES` (default 0;
  `FORCE=true` skips it) must have passed since the newest promoted or
  pending release was tagged. `<floor>..<lkg>` must contain at least one `feat:` or `fix:` commit:
  `docs:`, `chore:`, `test:`, `ci:`, `style:` and `build:` alone wait
  (`release_waits=<lkg>`).
- **Advisory** (`CANDIDATE_GATE_ADVISORY`): `gcp-smoke`, `live-*` and
  `post-reset-e2e`. They talk to live providers, and most of their failures
  are credentials, quotas and outages, so they are reported and never block.
- **`infra` is not red.** A required suite that was cancelled or errored
  with nothing failing is reported as needing a re-run, not as a
  regression. Grain re-runs `infra` verdicts of `main`'s gates on its own
  (up to two uncached re-runs) before filing a failure review, and trunk
  release gates run at `PriorityReleaseGate`, ahead of the merge queue for
  sandbox capacity (`design/design/release-from-trunk.design.md`, RFT-2).

If no commit after the floor qualifies, the run cuts nothing. A `main` that
stays red for a week ships nothing, and `latest` stays where it is.

With `RELEASE_CI_SOURCE=github` (the default in GitHub Actions), the train
reads workflow runs and jobs from GitHub Actions with `GITHUB_TOKEN`, and
reads the `grain/release-qualification` check run on the rc's commit and the
`grain/rc-hold` check run on a dropped rc's commit (checks: read). Without a
GitHub token neither can be read: no rc qualifies, and Phase 2 says it has no
`grain/rc-hold` to wait on rather than holding. With `RELEASE_CI_SOURCE=grain`, it reads CI runs through the
`release-gate-read` capability (`api:release-gate-read`,
`docs/ci-capabilities.md`).

## 3. Cutting the rc and numbering retries

The LKG commit is tagged `vX.Y.0-rc.N` (annotated) and pushed.
`refs/heads/candidate` is force-moved onto it, and its images are published
with `scripts/release-images.sh rc vX.Y.0-rc.N`, which:

- rebuilds `grain` and `google-grain` from the commit's `:sha-<short>`
  images with `RELEASE=vX.Y.0-rc.N`, so the running version reads as the rc;
- publishes all four images (`grain`, `google-grain`, `guest`,
  `k8s-sandbox`) as `:vX.Y.0-rc.N`, `:candidate` and `:rc`.

**The target release** is `vM.(P+1).0`. `P` is the highest promoted minor of
`RELEASE_MAJOR`, raised to `RELEASE_FLOOR_MINOR`, stepping over any
`vM.Y.0` that `RELEASE_NEVER_PROMOTE` names (`scripts/release.conf`). This
is the same rule `scripts/build-version.sh` uses to version `main`'s builds,
so `main` builds `vX.Y.0-dev.<n>` for the release the train is working
towards.

**Retries never skip a minor, and wait on `grain/rc-hold`.** An rc that fails
qualification, or that a person rejects, is dropped and a later run cuts
`vX.Y.0-rc.(N+1)` of the same minor from a newer LKG commit:

- **Holding `rc.(N+1)` until triage and blockers complete (`grain/rc-hold`).** When
  `vX.Y.0-rc.N` (`N < RELEASE_MAX_RC_ATTEMPTS`) is confirmed red, the release
  reconciler raises `pipeline.rc-failed:vX.Y.0-rc.N` to wake the pipeline steward
  (`docs/pipeline-steward.md`) and immediately posts check run `grain/rc-hold`
  (`in_progress`, "triage pending") on the red rc's commit — the floor Phase 2
  cuts `rc.(N+1)` after — before posting the completed
  `grain/release-qualification` failure. The steward titles its fix and revert
  tasks `[rc-blocker vX.Y.0-rc.N] ...`. While triage is pending (up to `1h`,
  `model.ReleaseHoldTriageGrace` / `GRAIN_RC_HOLD_GRACE`) or any open task
  carries that marker, the reconciler keeps `grain/rc-hold` `in_progress`, and
  completes it once every blocker has landed (`success`), when the steward
  triages `vX.Y.0-rc.N` with no blocker or a person clears the notification
  (`success`), when the triage window elapses with no blocker (`neutral`), or if
  superseded (`neutral`). Before cutting `rc.(N+1)` (`next_n > 1`), Phase 2 reads
  `commits/<floor>/check-runs?check_name=grain%2Frc-hold&filter=all` (or
  `RELEASE_HOLD_CHECKS_JSON` in tests): if `RELEASE_QUALIFICATION_APP` (the
  environment, else `scripts/release.conf`) is unset it refuses rather than
  trust another app's check run; if it is explicitly `none` no check run is
  trusted, so nothing holds; and if the newest check
  run from `RELEASE_QUALIFICATION_APP` is not `completed`, Phase 2 cuts nothing, logs
  `phase 2: vX.Y.0-rc.(N+1) is held by grain/rc-hold on <floor>: <summary>`,
  emits `rc_held=vX.Y.0-rc.(N+1)` and exits 0. No check run from
  `RELEASE_QUALIFICATION_APP` means no hold, except in the run whose Phase 1
  dropped `rc.N` itself: grain has not read the red verdict yet, so that run
  logs `phase 2: vX.Y.0-rc.(N+1) waits for the next run: ...`, emits
  `rc_held=vX.Y.0-rc.(N+1)` and cuts nothing, and the next run reads the hold
  (or, with grain down and still no hold, cuts). Stage 1 is watched as well as
  Stage 2 (`pkg/orchestrator/releasestage1.go`): a `nightly.yml` job that
  fails its one retry raises `pipeline.rc-failed` and posts the same hold.
- **Every rc from `main` (since 2026-11-09).** There is no `rc.3`
  convergence branch any more: each `rc.(N+1)` is cut from `main`'s newest
  known-good commit on the run after `rc.N` is dropped, so a day that has
  not released keeps cutting until one qualifies (playbook §5). Approving a
  release cut from a `release/vX.Y.0` a person made still deletes it.
- **Escalation, not a stop.** After `RELEASE_MAX_RC_ATTEMPTS` (3) dropped
  candidates of one version, the reconciler raises
  `pipeline.release-escalated:vX.Y.0` and the train emits
  `escalated_version=vX.Y.0` as it cuts the next one, and goes on cutting.

A minor number is consumed only by a promoted
release. While a release is pending, the train counts it as the newest
release when choosing the next target. So the next rc is
`vX.(Y+1).0-rc.1`, cut strictly after the pending commit.

| Tag | Made by | Means |
| --- | --- | --- |
| `vX.Y.0-rc.N` | the train | a release candidate, in qualification or qualified |
| `vX.Y.0-rc.N-discarded` | the train (failed qualification), or a person's Reject | that rc will never ship |
| `vX.Y.0-pending` | the train (passed qualification, but not the train's to release: section 5) | staged for a person's approval; deleted on Approve or Reject |
| `vX.Y.0` | the train, on the qualified rc's commit (`Promoted-From: <rc>`); or `promote-release.yml`, on a person's Approve | the release |
| `vX.Y.0-retracted` | `promote.yml` / `rollback`, on a person's Rollback | that release was retracted; `latest` moved back |
| `latest` | the train with `vX.Y.0`; `promote-release.yml` / `promote.yml`, on a person's Approve or Rollback | the newest promoted (non-retracted) release |

## 4. Two-stage qualification and red release candidates

Phase 1 runs `scripts/candidate-gate.sh check-qualification` against the
current candidate `vX.Y.0-rc.N` (unless `RELEASE_NEVER_PROMOTE` names it, in
which case it is dropped without qualifying):

1. **Stage 1 (`nightly.yml`).** Every required job of `nightly.yml` must
   succeed on the rc's commit.
2. **Stage 2 (`grain/release-qualification`).** Once Stage 1 is green, the
   deployment's release-qualification reconciler
   (`pkg/orchestrator/releasequalification.go`) runs
   `.grain/suites/release-qualification.yml` against the rc and posts check
   run `grain/release-qualification` on its commit. Only a check run created
   by `RELEASE_QUALIFICATION_APP` (grain's own GitHub App) counts; same-named
   check runs from any other app are ignored, and with no
   `RELEASE_QUALIFICATION_APP` configured none is trusted and the rc never
   passes. With `RELEASE_QUALIFICATION_APP` set explicitly to `none` (in the
   environment or `scripts/release.conf`), as for a forge connection that is a
   personal access token rather than a GitHub App (which cannot post check
   runs), Stage 2 is skipped (`SKIPPED grain/release-qualification`) and Stage
   1 alone qualifies the rc. It is never `none` by default.

A first product failure of any Stage 1 job or Stage 2 item gets one retry on
the same rc (`flake`); cancelled, timed-out or capacity-starved runs are
re-run without counting as failures (`infra`). A Stage 1 retry is a
`nightly.yml` dispatch whose `inputs.jobs` names each job by its job key,
never its display name: `check-qualification` maps every job it sees through
`CHECK_QUALIFICATION_JOBS` (required) or `CHECK_QUALIFICATION_OPTIONAL`
(judged only when it ran, such as `grant-matrix-ui`), and
`scripts/release-train-actions.sh` refuses, loudly and with a failing exit, to
dispatch a name that is not a job key in the rc's `nightly.yml`. A retry run in
which a job awaiting its re-run ended `skipped` ran nothing, so that attempt
counts as `infra` (grain's own Stage 1 judge in
`pkg/orchestrator/releasestage1.go` agrees) and three in a row stall the rc
rather than leave it pending on its first failure. `check-qualification`
returns:

- **`PENDING` / `INFRA`**: the run stops here (`qualification=pending|infra`,
  `qualification_rerun=...`), because only one rc qualifies at a time.
- **`FAIL`**: confirmed red after retry. The rc is tagged
  `vX.Y.0-rc.N-discarded` and pushed, `scripts/release-images.sh discard-rc`
  marks its images, and Phase 2 checks `grain/rc-hold` on its commit before
  cutting `rc.(N+1)` (section 3, `docs/pipeline-steward.md`).
- **`PASS`**: Stage 1 and Stage 2 both passed; the release is staged for a
  person (section 5).

## 5. Automatic promotion, and the `vX.Y.0-pending` stage for a person

**Since 2026-11-04 the train releases a qualified rc itself**
(`design/playbook/releases.playbook.md` §1). When `vX.Y.0-rc.N` passes Stage 1
and Stage 2, the next tick tags `vX.Y.0` on the rc's commit (annotated:
`Qualification: PASS`, `Promoted-From: vX.Y.0-rc.N`) and force-moves `latest`
onto it. `.github/workflows/publish-release.yml` runs on that tag: it runs
`scripts/release-from-trunk.sh verify vX.Y.0` (the tag names its rc, is on that
rc's commit, the rc is not dropped, the commit is on `main` or
`release/vX.Y.0`, the rc still passes both stages, and none of the cases below
that make the train stage it for a person applies), then
`release-images.sh promote vX.Y.0` and `gh release create` with the notes from
`scripts/release-notes.sh write`. Nothing waits on a person. The train does
**not** release it, and stages it as below instead, when:

- `RELEASE_AUTO_PROMOTE=false` in `scripts/release.conf` (every release goes to a person);
- it is the first release of a new `RELEASE_MAJOR` (a major bump);
- it carries breaking changes since the last release (`release-notes.sh check-breaking`);
- its commit is on neither `main` nor `release/vX.Y.0`.

`RELEASE_NEVER_PROMOTE` drops a named rc and skips a named version, so neither
is ever released or staged. Once the day has released, a qualified rc is
dropped (`Discard-Reason: daily-cap`), not held for the next day, and an rc
cut on an earlier day is never released (`Discard-Reason: held-over`);
a person's approve goes past the cap and says so (`day_cap_bypassed=`).
On a day that may release more than once, a qualified rc inside
`RELEASE_MIN_INTERVAL_MINUTES` of the last release waits
(`promotion_waits=` in the output), and a later tick releases it. To stop all automatic releases at once, set
`RELEASE_AUTO_PROMOTE=false` (a person's Submit, like every `release.conf`
change); to undo one, roll it back (section 6 and the playbook's §11).

**For a release staged for a person, the pipeline does everything up to the final tag, and a person makes the
release official** (task 1236, `design/playbook/releases.playbook.md` §0; `design/design/release-from-trunk.design.md` RFT-8).

1. **Qualification passes, so the release is staged, not promoted.** When
   `vX.Y.0-rc.N` passes qualification, the train:
   - tags its commit `vX.Y.0-pending` (annotated, naming the rc and
     `Qualification: PASS`);
   - builds and publishes the `vX.Y.0`-stamped images of all four packages
     as `:vX.Y.0-pending` (`scripts/release-images.sh pending vX.Y.0`), so
     approving builds and tests nothing;
   - files no per-release release-notes agent task: the task train runs
     with `RELEASE_NOTES_TASK=off`. A release's notes are mechanical
     (`scripts/release-notes.sh`, playbook §8), from the newest `vX.Y.Z` that
     `RELEASE_NEVER_PROMOTE` does not name and that is not retracted, never
     from a placeholder. `publish-release.yml` writes them for a release the
     train makes; for a staged release, file the `release-notes` template by
     hand if `docs/releases/vX.Y.0.md` should be on `main` before approving;
   - leaves the operator notification **"vX.Y.0 is ready to promote"**
     (`release.pending:vX.Y.0`) to the deployment, which raises it in the
     Inbox and the Alerts bell for every `-pending` tag with no release,
     with the tag's qualification summary, `docs/releases/vX.Y.0.md` if
     `main` has it, and **Approve** and **Reject** buttons.

   `vX.Y.0` and `latest` are not touched. `release-train.yml` has no step
   that could touch them (`TestTheReleaseTrainNeverPromotesARelease`,
   `tests/deploy`).
2. **Approve**: the notification's Approve button, or
   `grain release approve vX.Y.0`. This runs
   `.grain/workflows/promote-release.yml`, which tags `vX.Y.0`, force-moves
   `latest`, retags `:vX.Y.0-pending` as `:vX.Y.0` and `:latest`
   (`scripts/release-images.sh promote vX.Y.0`), deletes `vX.Y.0-pending`
   (and `release/vX.Y.0` if `rc.3` used a convergence branch),
   and runs `promote.yml`'s publish step. Only a person may approve. The
   route (`POST /api/repos/{owner}/{name}/pending-releases/vX.Y.0/approve`,
   under the release-manager scope `releases:write`) refuses an agent
   whatever it holds. The workflow's jobs refuse any run grain did not
   start for a person (`GRAIN_CI_REQUESTED_BY_KIND=human`). The workflow
   and the approval route are `human: required`
   (`.grain/triggers/release-promotion.yml`, and the `release-promotion`
   trust-boundary category). The release scripts and jobs clear on grain's
   security review alone, which checks they cannot move `vX.Y.Z` or
   `latest` without the approval (`.grain/triggers/release-machinery.yml`,
   and the `release-machinery` category).
3. **Reject**: the Reject button, or `grain release reject vX.Y.0`. This
   takes the failed-rc path: `vX.Y.0-rc.N-discarded` and the
   `discard-rc` image marker, the staged images marked `:vX.Y.0-rejected`
   (`discard-pending`), any next-minor rc cut on top of it dropped too, and
   `vX.Y.0-pending` deleted. The next run cuts `vX.Y.0-rc.(N+1)` (waiting on
   `grain/rc-hold` if blockers are open).
4. **While a release is pending**, the train keeps cutting and qualifying the
   next minor's rc, but never promotes or supersedes the pending
   release. A next-minor rc whose qualification passes waits until a person answers.
   Only one release is ever pending.
5. **Pending is not a release.** `scripts/build-version.sh` ignores
   `-pending` tags, so `main` keeps versioning from the last promoted
   release. No upgrade channel offers `:vX.Y.0-pending`, and `latest` moves
   only on approval.

## 6. Upgrade channels and discarded release candidates

Image tags, and the channels a deployment follows (`GRAIN_CHANNEL`,
`pkg/upgrade`, the Upgrade pane):

| Channel | Image tag | Follows |
| --- | --- | --- |
| `latest` | `:latest` | the newest release (the train's, or one a person approved) |
| `candidate`, or its alias `rc` | `:candidate` / `:rc` | the newest rc the train cut, qualifying or qualified |
| `main` | `:main` | `main`'s head |
| a pin | `:vX.Y.Z`, `:vX.Y.0-rc.N`, `:sha-<short>` | that build only |

`rc` is an alias for `candidate`: `GRAIN_CHANNEL=rc` in `scripts/setup.sh`,
or the `rc` target in the Upgrade pane, resolves the same image.
`scripts/setup.sh` does not compare a `candidate`/`rc` install against a
branch head, because `candidate` is force-moved and is not a branch anybody
develops on.

**When the rc a deployment runs is discarded.** A failed qualification or a person's
Reject does not move `:candidate` or `:rc` backwards.
`release-images.sh discard-rc` pushes a marker, `:vX.Y.0-rc.N-discarded`, on
`grain` and `google-grain`. `pkg/upgrade` treats the running
`vX.Y.0-rc.N` as discarded when that marker exists in the registry's tag
list, or when a higher rc of the same minor exists and `vX.Y.0` has not been
promoted. It reads only the tag list it already fetches. Then:

- **It never downgrades on its own.** An upgrade to `candidate`/`rc` with no
  replacement rc published yet completes as a no-op ("…was discarded and no
  newer release candidate has been published yet; staying on the current
  build"). Re-running `scripts/setup.sh` on a discarded rc does not
  downgrade either.
- **The Upgrade pane warns.** A warning below "Running now" says the rc was
  discarded (`DiscardedRC` on `/api/upgrade`) and offers one click to move to
  `latest`, or to the replacement rc once one is published.
- **The next rc is an ordinary upgrade.** When `vX.Y.0-rc.(N+1)` is
  published, `:candidate` and `:rc` move to it, and a deployment on those
  channels upgrades onto it as usual.

## 7. Version numbers: consumed minors and the `v2.5.0` placeholder

Version numbers never repeat or go backwards. By the time the train took
over, the legacy candidate cut had tagged `v2.3.0-rc.1` and `v2.4.0-rc.1`,
`v2.4.0` had been promoted from `candidate`'s history, and `main` had been
building `v2.5.0-dev`. So v2.3, v2.4 and v2.5 are all consumed.

`v2.5.0` is an annotated tag on `main` that is **a version-number
placeholder, not a release** (task 1410). It is the highest `vM.P.0` in
`main`'s history, so `main` builds `v2.6.0-dev.<n>+<sha>`, the train's first
cut is `v2.6.0-rc.1`, and **the first release from trunk is `v2.6.0`**.
`RELEASE_NEVER_PROMOTE` in `scripts/release.conf` names `v2.5.0`. That keeps
its minor counted but tells the train it was never released, so the notes
for `v2.6.0` run from `v2.4.0`. Nothing is published for the placeholder:
there is no `v2.5.0` image, GitHub release or upgrade target, and the
`latest` tag stays on `v2.4.0` until `v2.6.0` is approved.

**A major version** is an explicit decision: raise `RELEASE_MAJOR` in
`scripts/release.conf`. The train's next target becomes
`v{RELEASE_MAJOR}.{RELEASE_FLOOR_MINOR}.0`. `RELEASE_NEVER_PROMOTE` also
lists releases that are never made (`v1.14.0`, whose candidate held only
the single-tenant reset's first stage). The 1.x line ended at `v1.13.0`,
and `v2.0.0` was the first single-tenant release. From `v2.0.0` on, every
release upgrades a 2.0 deployment in place (`docs/data-model.md`, "Schema
changes after 2.0").

## 8. Images

Every merge to `main` runs `.grain/workflows/release.yml`, which builds,
signs, tests (`tests/container`, `tests/installer`) and publishes the
commit's images as `:sha-<short>` and `:main`. Releases are published from
those images, so the train and the approval build no new artifact. They
only re-stamp the daemon images with the release name and retag.

- **Daemon and sandbox images** are published to Docker Hub under `docker.io/grainai/*`
  (`grain`, `sandbox`, `sandbox-base`, `sandbox-k8s`, `sandbox-k8s-base`, with
  and `docker.io/grainai/hub`, the `grain-hub` image (also published as its pre-rename `docker.io/grainai/registry` for one release), which `promote`
  and `rc` rebuild and stamp alongside `grain`).
  `docker.io/grainai/grain` is the core build (`./cmd/grain`, the
  `oss` install track). A wrapper distribution's
  build is published beside it under its own image name and install track
  (`pkg/installtrack`), with the same tags.
- **What the tags mean:** `latest` is the last promoted release and never
  `main`'s head. `main` is the head of `main`. `candidate`/`rc` is the
  current rc. `vX.Y.Z` is one release, kept forever by `cmd/ghcr-prune`.
  A staged `vX.Y.Z-pending` build is kept whatever its age until the same
  version also carries `vX.Y.Z-rejected` (`discard-pending`); approving it
  retags it `vX.Y.Z`, which is kept from then on. `latest`, `vX.Y.Z`,
  `sha-<short>` and signature tags are kept the same way on the base
  packages (`sandbox-base`, `guest-base`) as on every other image.
- **`latest` follows the git tag.** `.grain/workflows/promote.yml`
  (`scripts/release-images.sh promote`) publishes the release the git
  `latest` tag names, as `vX.Y.Z` and `latest`. It runs on every `release`
  of `main`, after each release-train run, and as the last job of an
  approval. When nothing changed it costs a few manifest copies. It fails,
  moving nothing, if any `sha-<short>` image it needs is missing.
- **By hand** (to republish): `grain ci run -repo ai-on-gke/grain -branch main
  -event workflow_dispatch -job .grain/workflows/promote.yml:latest -watch`,
  or, as a repository admin, `POST /api/repos/ai-on-gke/grain/release-runs`
  with `{"branch":"main","jobs":[".grain/workflows/promote.yml:latest"]}`.
  Both republish whatever the git `latest` tag names. Moving `latest` to
  another release is a person's act, through Approve or a patch release
  (section 9).

### What the release workflows may push

`repo-write:ai-on-gke/grain`, held by a CI job of this repository, lets grain's
git proxy (`pkg/gitproxy/ciref.go`) take any tag or branch update except a
rewrite of trunk. `refs/heads/main` may only be fast-forwarded and never
deleted. These are the refs the release workflows push:

| Ref | Pushed by |
| --- | --- |
| `refs/tags/vX.Y.0-rc.N`, `-rc.N-discarded` | the train |
| `refs/tags/vX.Y.0-pending` (created, later deleted) | the train creates it; `promote-release.yml` deletes it |
| `refs/tags/vX.Y.0` | the train (a task, under its template's `push:` grant), on a qualified rc's commit; or `promote-release.yml` |
| `refs/tags/latest` (force-moved) | the train, with `vX.Y.0`; or `promote-release.yml` |
| `refs/heads/candidate` (force-moved) | the train |
| `refs/heads/release/vX.Y.0` (created for `rc.3`, deleted on Approve) | the train creates it; `promote-release.yml` deletes it |
| `refs/heads/main` | nothing in the release pipeline |

The train's task pushes through its template's `push:` grant
(`.grain/templates/release-train.yml`; the playbook's §0.4), which reaches
`vM.Y.0` (created only) and `latest`, and `scripts/release-from-trunk.sh`
pushes them only for the exact commit of the rc that qualified in that run.
`.github/workflows/release-train.yml` has no step that pushes them. The
grant, the train script, `scripts/release.conf`, the train's schedule,
`publish-release.yml` and the approval workflow all wait for a person's
Submit (`.grain/triggers/release-promotion.yml`, grain's template-push
category), and a change to the other release jobs or scripts is held for
grain's security review, which reports as a finding -- leaving the change
for a person -- any way it could push them other than as the approval or
the train's promotion of a qualified rc
(`.grain/triggers/release-machinery.yml`).

## 9. Patch releases (`release/vX.Y.Z`)

A patch release `vX.Y.Z` (`Z > 0`) is a released tag plus named fixes from
`main`, and nothing else. The train makes only minor releases from `main`'s
head. A patch is made by hand and is a person's act from start to finish.
The first one was `v2.1.1` (task 4984): `v2.1.0` plus #4980's header-rail
fixes.

`scripts/release-patch.sh` does steps 2, 5 and the tagging half of 6, and
refuses to tag until `release.yml` is green on the branch head:

```bash
scripts/release-patch.sh vX.Y.Z <sha>...   # cut, pick, push, wait, tag
scripts/release-patch.sh --no-wait vX.Y.Z <sha>...   # cut, pick, push
scripts/release-patch.sh vX.Y.Z            # later: wait for green, tag
```

It refuses a `feat:` pick unless `ALLOW_FEAT=1`. It asks GitHub through
`gh run list` whether `release.yml` is green, and it never moves `latest`.

1. **Pick the commits.** Find the fix's commits on `main`. For each
   prerequisite, check whether the release already carries it
   (`git log vX.Y.<Z-1> --grep '<subject>'`).
2. **Branch and pick.** Run
   `git checkout -b release/vX.Y.Z vX.Y.<Z-1>`, then `git cherry-pick -x`
   each commit. Use `git range-diff` against the originals to show the
   patches are unchanged. Push the branch. Pushing a `release/v*` branch
   needs the repo server's any-ref grant
   (`https://grain.dev/capabilities/repo#write`, formerly `git-push-any-ref`).
3. **Pull-request suite.** A task's CI tools only run on its own branch.
   Force-push the release tree there
   (`git push -f origin release/v…:<task branch>`), get a full
   `wait_for_checks` green, then reset the task branch to `main` for the
   docs PR.
4. **Long suites.** A task cannot dispatch `nightly` or the live suites, so
   a person runs them on the release branch, or decides the picks do not
   need them and says so in the notes.
5. **Release run.** `.github/workflows/release.yml` runs on every push to
   `release/v*`. It builds both daemon images, runs `tests/container` and
   `tests/installer` against them, and publishes `sha-<short>`. Don't tag
   until this is green.
6. **Tag and promote.** Tag the release commit `vX.Y.Z` and push the tag.
   Move `latest` onto it (`git tag -fa latest`) only if no newer release
   exists, then dispatch `promote.yml` (with `version: vX.Y.Z`, or as in
   section 8). Don't move `latest` before step 5 publishes, because
   promotion fails while the `sha-` images are missing.
7. **Notes and GitHub release.** Hand-write `docs/releases/vX.Y.Z.md`,
   listing only the picked fixes, and land it on `main` in a normal PR. The
   GitHub release is `gh release create vX.Y.Z --notes-file …`, run by
   somebody with GitHub credentials, since task sandboxes have none.
8. **Keep the branch.** Leave `release/vX.Y.Z` on the remote. It is what
   the patch-tag guard below checks the tag against, and `promote.yml`
   runs that guard on every merge while `latest` names the patch. The
   fixes are already on `main`, and the next minor carries them. Nothing is
   ported anywhere.

### The patch-tag guard

A tag on `main`'s head is not a patch. `v3.0.1` and `v3.0.2` were
hand-tagged on `main`, carrying 215 and then 117 more commits past `v3.0.0`.
Dozens of them were `feat:`, and there was no rc, soak or release notes.
`scripts/patch-tag-guard.sh` stops that. `release.yml` and `promote.yml`, in
both `.github/` and `.grain/`, run it before they build or publish. It
looks at the tag the run is for, at the `version` a promote dispatch names,
and (`--latest`) at the `vX.Y.Z` tags on the commit the git tag `latest`
names. A tag `vX.Y.Z` with `Z >= 1` passes only if one of these holds:

- **It came from its release branch.** `release/vX.Y.Z` contains the
  tagged commit and `vX.Y.<Z-1>` is an ancestor of it. Also, none of the
  commits in `vX.Y.<Z-1>..vX.Y.Z` is on `main`. Cherry-picks have SHAs of
  their own, so a release branch that is just `main` fails.
- **It carries only fixes.** Every non-merge commit in `vX.Y.0..vX.Y.Z`
  has a `fix:` or `fix(<scope>):` subject.

Otherwise the run fails before it publishes anything, and it names the
`feat:` subjects and counts the other non-fix commits the tag carries. To
check a tag by hand, run `bash scripts/patch-tag-guard.sh vX.Y.Z`.
`vX.Y.0`, rc, `-pending` tags and branch refs pass without a look.

The guard works at publish time. It cannot stop somebody pushing the tag.
A GitHub tag ruleset on `v*.*.*` (restrict creation to release managers,
or require the tagged commit on a `release/v*` branch) is the stronger
guard. That is a repository setting for an operator to make, and it is not
in this tree.

## 10. Build versions

Every build carries a semver version, not only a promotion. So the
versions of builds from different branches order the way the branches
relate. `scripts/build-version.sh` works it out from the git tags and the
branch, and it is the only place that does:

| Branch | Version | Today |
| --- | --- | --- |
| `main` (and any branch not listed below) | `v{M}.{P+1}.0-dev.{n}+{sha7}`. `v{M}.{P}.0` is the highest `vX.Y.0` tag in the build's history, and `n` counts commits since it. rc and patch tags do not move it. `RELEASE_FLOOR_MINOR` raises the minor, and a `vX.Y.0` that `RELEASE_NEVER_PROMOTE` names is stepped over. A `RELEASE_MAJOR` above `M` makes it `v{RELEASE_MAJOR}.{floor}.0-dev.{n}`. | `v2.6.0-dev.12+8e1a209` |
| `candidate`, legacy `rc/v*` and `release/v{M}.{X}` | `v{M}.{m}.0-rc.{k}.{n}+{sha7}`. `n` counts commits since the newest rc tag `v{M}.{m}.0-rc.{k}`. The rc tag's own commit is exactly `v{M}.{m}.0-rc.{k}`. | `v2.2.0-rc.1.48+6f65669` |
| `release/v{M}.{X}.{P}`, a patch branch ("Patch releases", above) | `v{M}.{X}.{P}-rc.0.{n}+{sha7}`. `n` counts the picks since `v{M}.{X}.{P-1}`, the release the branch was cut from. `rc.0` sorts below any `rc.1` somebody might tag, and below `v{M}.{X}.{P}`. | `v2.1.1-rc.0.3+3373f53` (at `v2.1.1`'s commit) |
| a promotion | `vX.Y.Z`, exactly. `promote` rebuilds with `RELEASE=vX.Y.Z`, which wins over the computed version. | `v2.1.1` |

v2.3, v2.4 and v2.5 are consumed, because version numbers never repeat or go
backwards. The legacy cut tagged `v2.3.0-rc.1` and `v2.4.0-rc.1`, `v2.4.0`
was promoted off `candidate`'s history, and `main` built `v2.5.0-dev` before
release from trunk. `v2.5.0` is an annotated placeholder tag on `main`, a
version number and not a release: nothing is published for it, and
`RELEASE_NEVER_PROMOTE` names it so the train writes the next release's notes
from `v2.4.0`. Because of it, `main` builds `v2.6.0-dev`, and the first
release from trunk is `v2.6.0` (`design/design/release-from-trunk.design.md`, "Version
Numbering Rules").

Semver orders these correctly:
`v2.1.1 < v2.2.0-rc.1 < v2.2.0-rc.1.48 < v2.2.0 < v2.3.0-dev.114 < v2.3.0-rc.1 < v2.3.0`.
Pre-release identifiers compare field by field, `dev` sorts below `rc`,
and a longer pre-release with an equal prefix sorts higher. The build
metadata `+{sha7}` says which commit a build is. It plays no part in
ordering. `TestBuildVersionsOrderTheWayTheBranchesRelate`
(`pkg/releasenotes`) holds the order, and `tests/deploy`'s
`TestBuildVersionOfEachBranch` holds the script.

**Where it is stamped:**

- **Locally.** `make build` and `make image` set `RELEASE` from the script
  unless one was given. That fills the `-X …/pkg/version.Release` ldflag,
  and `make image` also fills the Dockerfile's `ARG RELEASE`, which labels
  the image `org.opencontainers.image.version`.
- **In release jobs.** `scripts/release-images.sh`'s `build` phase runs
  the script with `--fetch origin`, which fetches the tags first (and
  unshallows a shallow checkout), since a CI checkout is not promised to
  have them. It stamps both `grain` and `google-grain`, and
  `check_release_stamp` then checks each image's `grain version --json`
  against the version.
- **Dry run.** `release-images.sh --dry-run build` prints the version,
  ldflag and label it would use.
- **Overrides.** A `RELEASE` in the environment or on make's command line
  wins, empty included. The Dockerfile's `make build RELEASE=${RELEASE}`
  therefore never asks the builder's git.

A build with no git, or with no rc tag in its history, gets no version.
It keeps the old fallback, Go's module pseudo-version. `pkg/version`
marks it `Unversioned`, and `grain version` prints it as `<pseudo-version>
(unversioned)` (JSON `"unversioned": true`, also on `/api/config`'s
`version`). The pseudo-version is not a place among releases: it names
whatever tag Go found, and on `main` that was `v1.14.0-rc.1.0.<time>-<sha>`,
which sorts below `v2.1.1`.

What reads the version, and what it does with these strings:

- **`grain version` and `grain version --json`** print the release
  whole, `+{sha7}` included. Unstamped builds are marked unversioned.
- **The UI's sidebar footer and the Upgrade pane's running version**
  (`/api/config`'s `version.release`, `/api/upgrade`'s
  `running.release`) show the string as it is. A main build reads
  `v2.3.0-dev.123+8e1a209 · 8e1a209 · <date>`.
- **The Upgrade pane's release-notes preview** (`pkg/upgrade`'s
  `fillPreview`) compares versions with `releasenotes.Compare`, which is
  semver. A target that is not newer than the running build is reported
  as "not newer than … which this deployment runs", and that check
  comes first. So a `main` deployment asked about `latest`, about
  `candidate`, or about an older `main` build is told it would go
  backwards, and is not offered a downgrade dressed as an upgrade. A
  newer `-dev` or rc build is an upgrade whose own notes are not written
  yet, which is reported as `Missing` `v{M}.{m}.0`. `candidate` onto its
  release shows that release's notes.
- **The release-notes list** (`releasenotes.Between`, `/api/release-notes/versions`)
  shows the notes newer than the running version. A main build already
  has every promoted release's notes, so it is shown none of them. A
  version with no notes of its own is shown as "runs …, which has no
  notes of its own in this build".
- **"Already running this"** (`sameImageVersion`, `sameBinaryVersion`,
  the pane's `isSameRunningVersion`) is exact string equality. The
  version names its commit, so equal strings are the same build.
- **Channel and edition** (`ChannelForRef`, `tagEdition`) read image
  tags, which are unchanged. On a version string, `+{sha7}` is never read
  as an edition, because only `+google`/`+oss` are.
- **Install track** (`pkg/installtrack`) does not read the version.
- **Migrations and in-place upgrade guards** compare schema versions
  (`pkg/upgrade/schema.go`, `SchemaVersion`), not releases.
- **Promotion** (`published_release` in `release-images.sh`) rebuilds
  `grain:vX.Y.Z` unless its label is exactly `vX.Y.Z`. A release job's
  label is a build version, never a bare release, so a promotion still
  rebuilds and stamps.

## 11. Schedules and suites around a release

Every schedule and suite runs against `main`. Nothing declares
`base: candidate` any more, and `candidate`'s own nightly and live-suite
schedules are gone.

- **On `main`**: `tests.yml` on every pull request and merge, `release.yml`
  on every merge, `nightly.yml` daily at 02:11 UTC, and the live-provider
  workflows (`gcp-smoke`, `live-agent`, `live-frameworks`,
  `live-secrets-repo`, `post-reset-e2e`) on their own crons. The first two
  decide the LKG (section 2), `nightly.yml` also runs on every rc as Stage 1
  of its qualification (section 4), and the rest are advisory.
- **Live real-model suites** (`.github/workflows/live-agent.yml`,
  `live-frameworks.yml`): advisory, with a freshness policy. Every LKG
  selection (`scripts/candidate-gate.sh select-lkg`) records each suite's
  newest verdict and newest pass on `main`, and prints a `WARNING live`
  line when the newest verdict is not a pass, or when no pass is newer than
  `CANDIDATE_GATE_LIVE_MAX_AGE_DAYS` (3 days). The policy is to warn and
  not refuse. A provider outage or an exhausted key must not stall the
  train. But a release cut under a warning was cut without a fresh
  live-model verdict, and the train's report says so. v3.5.0 shipped a
  tool schema Gemini refuses because these suites had stopped running for
  two weeks and nothing said so. The warning and `grain health`'s stale
  schedules (`docs/github-actions-ci.md`) are how that is seen now.
- **No staging soak.** There is no persistent staging sandbox, no
  `staging-ui-exploration` or `staging-state-cleanup` schedule and no
  24-hour wait: an rc is qualified by Stage 1 and Stage 2 (section 4), and
  the train judges it as soon as both have a verdict.
- **Release train** (`.grain/schedules/release-train.yml`): every 20
  minutes, `overlap: skip`, one tick of `.grain/templates/release-train.yml`
  (`scripts/release-train-task.sh`): judge the rc, release or drop it, or cut
  the next. A tick with nothing to do closes as `nothing_needed`.
- **Release manager** (`.grain/templates/release-manager.yml`): one task
  per version, which checks in every hour from the first rc to published
  core and Google edition images. It is started by
  `.grain/schedules/release-manager.yml` when `release-train-assets.yml`
  concludes on an rc tag, and by `.grain/schedules/release-manager-patch.yml`
  when `release.yml` concludes on a `release/vX.Y.Z` branch (a patch,
  section 9). See section 12 and the playbook's section 16.
- **Release watchdog** (`.grain/templates/release-watchdog.yml`, every 6
  hours): see section 12.
- **Release process review** (`.grain/templates/release-process-review.yml`,
  weekly): see section 12.
- **Suites**: `.grain/suites/release-ui-walkthrough.yml` (the 7-area UI
  walkthrough) and `.grain/suites/release-qualification.yml` (merge queue,
  CI diagnostics, base-branch triage and system smoke) can be run before a
  person approves a release. Its templates target `main`, stage their
  deliberate failures and test batches on a `qualification/<task id>` scratch
  branch, and system smoke runs the commit `candidate` points at.
  `.grain/suites/main-ui-smoke.yml` is the fast UI check during development.

**Before approving** a pending release, read the qualification summary in
the notification and look at the newest `post-reset-e2e` run. `docs/test-plans/2026-10-20-post-reset-e2e.md` lists
the manual rows a sandbox cannot do (Workstation install, depot, real
github.com, a real upgrade through the Upgrade pane, live agents). Do these
after any change to the installers or the git-host code.

## 12. Watching the pipeline

A continuous train is healthy when rcs qualify quickly, flakes stay rare and
no release has to be taken back. Two schedules watch for that and report; red
rcs themselves are the pipeline steward's (`docs/pipeline-steward.md`).

- **Release manager** (one per version, hourly) is the one of these that
  acts, not only reports. It owns its release until both core and the
  edition are published. It files one fix for each red check nobody owns,
  and unsticks stuck tasks: it reprioritises, undefers, retries, answers
  and rewrites them. It notifies a person (`release-manager:vX.Y.Z`) for
  what only a person can do, and drives or files the edition's release
  task. Its first check-in pins the version (a `Release manager pin:
  vX.Y.Z` comment) and ends in `nothing_needed` if another manager already
  has that version or the version is already out. v3.6.0's was filed by
  hand (#4747). It never force-merges, bypasses a gate, pushes or deletes
  a ref. Its full brief is `design/playbook/releases.playbook.md` section 16.

- **Release watchdog** (every 6 hours) reports the newest release, the rc in
  qualification, which stage it is in and how long it has been there, and
  `main`'s `nightly.yml` and live suites. It proposes a repair task when an rc
  has been qualifying for more than 4 hours, when a stage has not started 30
  minutes after the one before it finished, or when `main` has releasable
  commits but the train has cut nothing. It also checks that a flake budget
  that was overspent, and a retracted release, have their notification and
  their follow-up task.
- **Release process review** (Mondays, 07:00 UTC) measures the week:
  - **qualification lead time**: per rc, cut to Stage 1 to Stage 2 to
    `vX.Y.0`, `-pending` or `-discarded`, and per release, landing on `main`
    to `rc.1` to `vX.Y.0` to the downstream sync. Time a qualified rc waited
    on `RELEASE_MIN_INTERVAL_MINUTES`, and time a staged release waited for a
    person's Approve, are counted separately.
  - **rcs per release**, and any `rc.3` convergence branch or escalation.
  - **flake rate**: qualification jobs and items that passed only on their one
    retry, against the playbook's budget (more than 2% over 7 days, or two rc
    retries in 24 hours, raises `pipeline.flake-budget`).
  - **infra re-runs**, and any `pipeline.release-infra-stalled`.
  - **retracted releases** (`vX.Y.Z-retracted`): why, how long the release
    was `latest`, and which stage should have caught it.

  It files follow-up tasks, held for a person's approval, for the fixes it
  recommends.

## 13. Commit standards

All agent and developer commits follow Conventional Commits:
- `fix:` / `fix(<scope>):` for bug fixes and patches.
- `feat:` / `feat(<scope>):` for new features or functional enhancements.
- `refactor:`, `test:`, `chore:`, `docs:` for non-functional updates.
- Structured footers: `Fixes: #<id>`, and `Backported-from: <commit_sha>` on
  a pick onto a `release/vX.Y.Z` patch branch.

`scripts/release-notes.sh` sorts a release's notes by these types. The
instructions are kept in `PROMPT.md` and `.grain/settings.yml`.
