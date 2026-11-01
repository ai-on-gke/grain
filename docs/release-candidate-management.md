# Releasing grain from trunk

This document describes how `bwsalmon/grain` is released: from `main`, by a
daily release train, after a staging soak, with a person's approval as the
last step. The design and the reasons for it are in
`design/design/release-from-trunk.design.md`. This document is the operating manual.

There is no stabilization branch. Nothing is merged into `candidate`, nothing
is back-merged from it, and no fix is ported between branches. Every release
tag names a commit on `main`'s first-parent history.
`refs/heads/candidate` is only a pointer: the train force-moves it onto the
release candidate that staging runs. Pull requests that target `candidate`
are refused.

## 1. The pipeline at a glance

```
main ──merge queue──▶ tests.yml / release.yml on every merge, nightly.yml daily
  │
  │  04:15 UTC daily: .grain/workflows/release-train.yml
  │  (scripts/release-from-trunk.sh)
  ▼
Phase 2  newest known-good main commit (LKG) ──▶ tag vX.Y.0-rc.N
         force-move refs/heads/candidate onto it, publish :vX.Y.0-rc.N, :candidate, :rc
  │
  ▼  grain-candidate (staging, :4280) refreshes onto it and soaks for 24h
Phase 1  soak verdict:
           PENDING ──▶ ask again tomorrow
           FAIL    ──▶ tag vX.Y.0-rc.N-discarded, mark images; next run cuts vX.Y.0-rc.(N+1)
           PASS    ──▶ tag vX.Y.0-pending, publish :vX.Y.0-pending, file the release-notes
                       task, raise "vX.Y.0 is ready to promote" (Approve / Reject)
  │
  ▼  a person
Approve ──▶ .grain/workflows/promote-release.yml: tag vX.Y.0, move latest, retag images
Reject  ──▶ the failed-soak path; the next run cuts vX.Y.0-rc.(N+1)
```

| Piece | What it does |
| --- | --- |
| `.grain/workflows/release-train.yml` | Runs the train daily at 04:15 UTC (and on `workflow_dispatch`). Holds `repo-write:bwsalmon/grain`, `release-gate-read` and `package-publish`, and optionally `image-signing`. Never pushes `vX.Y.0` and never moves `latest`. |
| `scripts/release-from-trunk.sh` | The train: Phase 1 (judge the soaking rc) and Phase 2 (cut the next rc). Also has `approve` and `reject` modes, which only `promote-release.yml` runs, for a person. |
| `scripts/candidate-gate.sh` | `select-lkg` (which `main` commit is known-good) and `check-soak` (is the rc on staging healthy). |
| `scripts/release-images.sh` | `rc`, `discard-rc`, `pending`, `discard-pending` and `promote`: what each step publishes to GHCR. |
| `.grain/templates/release-notes.yml` | The task the train files when a release is staged: draft `## Highlights`, run `scripts/release-notes.sh`, open a pull request for `docs/releases/vX.Y.0.md`. |
| `.grain/workflows/promote-release.yml` | A person's Approve or Reject. `workflow_dispatch` only, and `human: required` (`.grain/triggers/release-promotion.yml`). |
| `.grain/workflows/promote.yml` | Publishes whatever release the git `latest` tag names as the `latest` image. Runs on every `release` of `main`, after each release-train run, and as the last job of an approval. |

Each train run is idempotent. A run that finds nothing to do does nothing, so
running it again by hand is always safe:

```
grain ci run -repo bwsalmon/grain -branch main -event workflow_dispatch \
  -job .grain/workflows/release-train.yml:train -watch
```

**Scheduled runs and `package-publish`.** `package-publish` and
`image-signing` answer `release` and `workflow_dispatch` only. They have not
opted in to grain's trusted schedules, as `repo-write` and
`release-gate-read` have (`docs/ci-capabilities.md`). So a cron run of the
train gets no registry credential. Its first step then stops the run before
it tags anything, and the run's review says why. Until that is settled, the
train publishes only when it is dispatched.

`scripts/release-from-trunk.sh` with `DRY_RUN=true` prints what it would push,
publish and file, and does none of it. The script's header lists every
setting (`RELEASE_*`, `FORCE`, `DRY_RUN`).

## 2. Choosing the release candidate: the LKG commit

With nothing soaking, Phase 2 asks `scripts/candidate-gate.sh select-lkg` for
the **last known-good (LKG)** commit: the newest commit on `main`'s
first-parent history, strictly after the floor, whose required suites are
green. The floor is the commit of the rc that was just dropped, or of the
release just staged or promoted, so a new rc is always newer than the last.

- **Required** (`CANDIDATE_GATE_REQUIRED`): `.grain/workflows/tests.yml`,
  `release.yml` and `nightly.yml`. A suite's verdict at a commit combines
  the latest result of each of its jobs across every run at that commit,
  which is the same combination the merge gate reads. So a recheck that
  turns one job green turns the suite green.
- **Nightly runs once a day.** A commit merged after the nightly also
  qualifies when its own `tests.yml` and `release.yml` are green, the newest
  `nightly.yml` verdict on its first-parent ancestry passed within the last
  24 hours, and no `fixes_base_branch` repair task is open on `main`.
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

The train reads `main`'s CI runs, staging's state and the exploration tasks
through the `release-gate-read` capability: the `api:release-gate-read`
preset over `ci:read`, `tasks:read`, `sandboxes:read` and
`tasks:from-template` (limited to `release-notes`). It is held through a
per-job token (`docs/ci-capabilities.md`, `docs/agent-api.md` "CI jobs").

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

**Retries never skip a minor.** An rc that fails its soak, or that a person
rejects, is dropped and the next run cuts `vX.Y.0-rc.(N+1)` of the same minor
from a newer LKG commit. A minor number is consumed only by a promoted
release. While a release is pending, the train counts it as the newest
release when choosing the next target. So the next rc is
`vX.(Y+1).0-rc.1`, cut strictly after the pending commit.

| Tag | Made by | Means |
| --- | --- | --- |
| `vX.Y.0-rc.N` | the train | a release candidate, soaking or soaked |
| `vX.Y.0-rc.N-discarded` | the train (failed soak), or a person's Reject | that rc will never ship |
| `vX.Y.0-pending` | the train (passed soak) | staged for a person's approval; deleted on Approve or Reject |
| `vX.Y.0` | `promote-release.yml`, on a person's Approve | the release |
| `latest` | `promote-release.yml`, on a person's Approve | the newest promoted release |

## 4. The 24h staging soak on `grain-candidate`

Staging is the `grain-candidate` persistent sandbox, which runs `grain demo`
on port `4280`. It follows `refs/heads/candidate` and refreshes onto each new
rc by itself. Once the rc is 24 hours old (less
`RELEASE_INTERVAL_SLACK_HOURS`, 2 by default, so a daily 04:15 run never
misses the boundary by a minute of jitter; `FORCE=true` skips the wait),
Phase 1 runs `scripts/candidate-gate.sh check-soak`, which reads four
signals and the operator's veto:

1. **Supervisor.** `grain-candidate` is running the rc's commit, with no
   refresh crash, rollback or exit since it was deployed (read from
   `GET /api/persistent-sandboxes` and its events).
2. **HTTP and version.** `RELEASE_STAGING_URL` (default
   `http://127.0.0.1:4280`) serves the HTML shell at `/`, `/api/config`
   reports the rc as its version, and each core API endpoint answers 200
   with JSON.
3. **Exploration.** The newest `staging-ui-exploration` task that started
   after the rc was deployed has finished and did not fail. If none has
   finished yet, the verdict is `PENDING`.
4. **Defects.** No task is open that was filed from one of those
   exploration runs, or that names the rc tag or `rc-blocker` in its title.
5. **Never.** Neither the rc nor the release it would become is in
   `RELEASE_NEVER_PROMOTE`.

The verdict is `PASS`, `FAIL` or `PENDING`. Any `FAIL` beats a `PENDING`.

- **`PENDING`**: the run stops, and the next run asks again.
- **`FAIL`**: the rc is dropped. `vX.Y.0-rc.N-discarded` is tagged and
  pushed, `scripts/release-images.sh discard-rc` marks its images, and
  Phase 2 cuts `rc.(N+1)`. A failure that no filed defect accounts for
  (supervisor, HTTP probe, a failed exploration run) also makes the run
  exit 1, so the scheduled workflow's review task files soak triage.
- **`PASS`**: the release is staged for a person (section 5).

`nightly.yml` and `release.yml` on `main` cover what `grain demo` does not,
including VMs, databases, containers and installers. They are already green
for the commit before it is cut (section 2).

## 5. The `vX.Y.0-pending` stage and a person's approval

**The pipeline does everything up to the final tag, and a person makes the
release official** (task 1236, `design/design/release-from-trunk.design.md` RFT-8).

1. **The soak passes, so the release is staged, not promoted.** When
   `vX.Y.0-rc.N` passes its soak, the train:
   - tags its commit `vX.Y.0-pending` (annotated, naming the rc and the
     soak verdict);
   - builds and publishes the `vX.Y.0`-stamped images of all four packages
     as `:vX.Y.0-pending` (`scripts/release-images.sh pending vX.Y.0`), so
     approving builds and tests nothing;
   - files the release-notes task (`.grain/templates/release-notes.yml`),
     so `docs/releases/vX.Y.0.md` is merged before anybody approves. The
     notes run from the newest `vX.Y.0` that `RELEASE_NEVER_PROMOTE` does
     not name, never from a placeholder;
   - raises the operator notification **"vX.Y.0 is ready to promote"**
     (`release.pending:vX.Y.0`) in the Inbox and the Alerts bell, with the
     soak summary, the notes task, and **Approve** and **Reject** buttons.
     Each daily run raises it again while the release waits.

   `vX.Y.0` and `latest` are not touched. `release-train.yml` has no step
   that could touch them (`TestTheReleaseTrainNeverPromotesARelease`,
   `tests/deploy`).
2. **Approve**: the notification's Approve button, or
   `grain release approve vX.Y.0`. This runs
   `.grain/workflows/promote-release.yml`, which tags `vX.Y.0`, force-moves
   `latest`, retags `:vX.Y.0-pending` as `:vX.Y.0` and `:latest`
   (`scripts/release-images.sh promote vX.Y.0`), deletes `vX.Y.0-pending`,
   and runs `promote.yml`'s publish step. Only a person may approve. The
   route (`POST /api/repos/{owner}/{name}/pending-releases/vX.Y.0/approve`,
   under the release-manager scope `releases:write`) refuses an agent
   whatever it holds. The workflow's jobs refuse any run grain did not
   start for a person (`GRAIN_CI_REQUESTED_BY_KIND=human`). The workflow
   and the release scripts are `human: required`
   (`.grain/triggers/release-promotion.yml`, and the `release-promotion`
   trust-boundary category).
3. **Reject**: the Reject button, or `grain release reject vX.Y.0`. This
   takes the failed-soak path: `vX.Y.0-rc.N-discarded` and the
   `discard-rc` image marker, the staged images marked `:vX.Y.0-rejected`
   (`discard-pending`), any next-minor rc cut on top of it dropped too, and
   `vX.Y.0-pending` deleted. The next run cuts `vX.Y.0-rc.(N+1)`.
4. **While a release is pending**, the train keeps cutting and soaking the
   next minor's rc on staging, but never promotes or supersedes the pending
   release. A next-minor rc whose soak passes waits until a person answers.
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
| `latest` | `:latest` | the newest release a person approved |
| `candidate`, or its alias `rc` | `:candidate` / `:rc` | the rc soaking on staging |
| `main` | `:main` | `main`'s head |
| a pin | `:vX.Y.Z`, `:vX.Y.0-rc.N`, `:sha-<short>` | that build only |

`rc` is an alias for `candidate`: `GRAIN_CHANNEL=rc` in `scripts/setup.sh`,
or the `rc` target in the Upgrade pane, resolves the same image.
`scripts/setup.sh` does not compare a `candidate`/`rc` install against a
branch head, because `candidate` is force-moved and is not a branch anybody
develops on.

**When the rc a deployment runs is discarded.** A failed soak or a person's
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

- **Two daemon images** share the `guest` and `k8s-sandbox` images.
  `ghcr.io/bwsalmon/grain/grain` is the core build (`./cmd/grain`, the
  `oss` install track). `ghcr.io/bwsalmon/grain/google-grain` is the Google
  build (`./google/cmd/grain`, the `google` track; `pkg/installtrack`).
  Both get the same tags. A Google-track deployment that still names
  `grain/grain` upgrades onto `google-grain`.
- **What the tags mean:** `latest` is the last promoted release and never
  `main`'s head. `main` is the head of `main`. `candidate`/`rc` is the
  current rc. `vX.Y.Z` is one release, kept forever by the GHCR pruner.
- **`latest` follows the git tag.** `.grain/workflows/promote.yml`
  (`scripts/release-images.sh promote`) publishes the release the git
  `latest` tag names, as `vX.Y.Z` and `latest`. It runs on every `release`
  of `main`, after each release-train run, and as the last job of an
  approval. When nothing changed it costs a few manifest copies. It fails,
  moving nothing, if any `sha-<short>` image it needs is missing.
- **By hand** (to republish): `grain ci run -repo bwsalmon/grain -branch main
  -event workflow_dispatch -job .grain/workflows/promote.yml:latest -watch`,
  or, as a repository admin, `POST /api/repos/bwsalmon/grain/release-runs`
  with `{"branch":"main","jobs":[".grain/workflows/promote.yml:latest"]}`.
  Both republish whatever the git `latest` tag names. Moving `latest` to
  another release is a person's act, through Approve or a patch release
  (section 9).

### What the release workflows may push

`repo-write:bwsalmon/grain`, held by a CI job of this repository, lets grain's
git proxy (`pkg/gitproxy/ciref.go`) take any tag or branch update except a
rewrite of trunk. `refs/heads/main` may only be fast-forwarded and never
deleted. These are the refs the release workflows push:

| Ref | Pushed by |
| --- | --- |
| `refs/tags/vX.Y.0-rc.N`, `-rc.N-discarded` | the train |
| `refs/tags/vX.Y.0-pending` (created, later deleted) | the train creates it; `promote-release.yml` deletes it |
| `refs/tags/vX.Y.0` | `promote-release.yml` only |
| `refs/tags/latest` (force-moved) | `promote-release.yml` only |
| `refs/heads/candidate` (force-moved) | the train |
| `refs/heads/main` | nothing in the release pipeline |

The proxy does not stop the train from pushing `vX.Y.0` or `latest`. The
workflow and the review of changes to it do. `release-train.yml` has no step
that pushes them, every `.grain/**` change waits for a person
(`.grain/triggers/grain-declarations.yml`), and the scripts that push those
tags are `human: required` (`.grain/triggers/release-promotion.yml`).

## 9. Patch releases (`release/vX.Y.Z`)

A patch release `vX.Y.Z` (`Z > 0`) is a released tag plus named fixes from
`main`, and nothing else. The train makes only minor releases from `main`'s
head. A patch is made by hand and is a person's act from start to finish.
The first one was `v2.1.1` (task 4984): `v2.1.0` plus #4980's header-rail
fixes.

1. **Pick the commits.** Find the fix's commits on `main`. For each
   prerequisite, check whether the release already carries it
   (`git log vX.Y.<Z-1> --grep '<subject>'`).
2. **Branch and pick.** Run
   `git checkout -b release/vX.Y.Z vX.Y.<Z-1>`, then `git cherry-pick -x`
   each commit. Use `git range-diff` against the originals to show the
   patches are unchanged. Push the branch. Pushing a `release/v*` branch
   needs `git-push-any-ref`.
3. **Pull-request suite.** A task's CI tools only run on its own branch.
   Force-push the release tree there
   (`git push -f origin release/v…:<task branch>`), get a full
   `wait_for_checks` green, then reset the task branch to `main` for the
   docs PR.
4. **Long suites.** A task cannot dispatch `nightly` or the live suites, so
   a person runs them on the release branch, or decides the picks do not
   need them and says so in the notes.
5. **Release run (person).** `.grain/workflows/release.yml` answers
   `release` on `release/v*`. It builds both daemon images, runs
   `tests/container` and `tests/installer` against them, and publishes
   `sha-<short>`. A repository admin starts it:
   `POST /api/repos/bwsalmon/grain/release-runs` with
   `{"branch":"release/vX.Y.Z","jobs":[".grain/workflows/release.yml:images"]}`.
   Don't tag until this is green.
6. **Tag and promote.** Tag the release commit `vX.Y.Z` and push the tag.
   Move `latest` onto it (`git tag -fa latest`) only if no newer release
   exists, then dispatch `promote.yml:latest` as in section 8. Don't move
   `latest` before step 5 publishes, because promotion fails while the
   `sha-` images are missing.
7. **Notes and GitHub release.** Hand-write `docs/releases/vX.Y.Z.md`,
   listing only the picked fixes, and land it on `main` in a normal PR. The
   GitHub release is `gh release create vX.Y.Z --notes-file …`, run by
   somebody with GitHub credentials, since task sandboxes have none.
8. **Clean up.** Delete `release/vX.Y.Z` once the tag and its images exist.
   The tag is the release's permanent record. The fixes are already on
   `main`, and the next minor carries them. Nothing is ported anywhere.

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
  `live-secrets-repo`, `post-reset-e2e`) on their own crons. The first three
  decide the LKG (section 2), and the rest are advisory.
- **On staging** (`grain-candidate`, `:4280`): `staging-ui-exploration`, the
  daily Playwright exploration whose finished run is a soak signal, and
  whose defect tasks, filed against `main`, hold the soak until they close.
  `staging-state-cleanup` keeps staging's data in shape.
- **Release watchdog** (`.grain/templates/release-watchdog.yml`): reports the
  newest rc tag and its age, how far `main` is ahead of it, and the
  verdicts of `main`'s nightly and live suites. It proposes a repair task
  when rcs stop advancing.
- **Suites**: `.grain/suites/release-ui-walkthrough.yml` (the 7-area UI
  walkthrough) and `.grain/suites/release-qualification.yml` (merge queue,
  CI diagnostics, base-branch triage and system smoke) can be run before a
  person approves a release. Its templates target `main`, stage their
  deliberate failures and test batches on a `qualification/<task id>` scratch
  branch, and system smoke runs the commit `candidate` points at.
  `.grain/suites/main-ui-smoke.yml` is the fast UI check during development.

**Before approving** a pending release, check the soak summary in the
notification, merge the release-notes pull request, and look at the newest
`post-reset-e2e` run. `docs/test-plans/2026-10-20-post-reset-e2e.md` lists
the manual rows a sandbox cannot do (Cloudtop install, depot, real
github.com, a real upgrade through the Upgrade pane, live agents). Do these
after any change to the installers or the git-host code.

## 12. Commit standards

All agent and developer commits follow Conventional Commits:
- `fix:` / `fix(<scope>):` for bug fixes and patches.
- `feat:` / `feat(<scope>):` for new features or functional enhancements.
- `refactor:`, `test:`, `chore:`, `docs:` for non-functional updates.
- Structured footers: `Fixes: #<id>`, and `Backported-from: <commit_sha>` on
  a pick onto a `release/vX.Y.Z` patch branch.

`scripts/release-notes.sh` sorts a release's notes by these types. The
instructions are kept in `PROMPT.md` and `.grain/settings.yml`.
