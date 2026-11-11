# Release train and supply chain walkthrough — 2026-11-11 (test plan §7)

Task 4520 walked [§7 of `docs/test-plans/2026-11-11.md`](../test-plans/2026-11-11.md#7-release-train-and-supply-chain-continuous-loop-stage-1-repairs-loop-idle-alert-nightly-idle-gate-image-vulnerability-gate-reviewed-merge-publishing)
against `main` at `83c3a1c1b`. It covers the continuous loop, Stage 1 repairs,
loop-idle, the nightly idle-gate, the image vulnerability gate and
reviewed-merge publishing.

Every release script ran in a scratch clone or bare remote under `/tmp`. No
release script ran in the task checkout, and its `.git/config` still has no
`user.*` entry. GitHub was always the GitHub simulator
(`pkg/github/githubsim`), a fake Actions API or a stub `grain` CLI. Nothing
was pushed to a real registry or remote: no tag, no image.

## Environment

| | |
|---|---|
| Sandbox | Debian, Go 1.26.10, Docker Engine 29.9.3 (Docker **was** available) |
| Added packages | `faketime` (item 1), `e2fsprogs` for `mkfs.ext4` (item 5), `sqlite3` |
| Daemon | `grain daemon -sandbox-port-base=0 -disable-native-sandboxes`, SQLite, built from this branch, `-github-host` pointed at `trainsim.go` (the GitHub simulator over HTTP, git smart HTTP included), `GRAIN_RELEASE_LOOP_IDLE_AFTER=1m` |
| Trivy | v0.76.2, the version `scripts/scan-images.sh` pins, downloaded and sha256-checked by the script itself |

## Helpers and logs

Everything is in [`2026-11-11-release-supply-chain/`](2026-11-11-release-supply-chain/):

| File | Item |
|---|---|
| `loop-day.sh`, `loop-day.log` | 1: a simulated Pacific day of `release-train-task.sh` ticks under `faketime`, `RELEASE_MAX_PER_DAY=0` |
| `trainsim.go`, `start-daemon.sh`, `run-daemon-items.sh`, `daemon-items.log` | 2, 3: a real daemon reading simulated `nightly.yml` runs and tags |
| `idlegate.go`, `idlegate.log` | 4: the committed `idle-gate` step of `nightly.yml`, run against a fake Actions API |
| `scan-images.sh`, `scan-images.log` | 5: `make scan-images` over two locally built images (one an image, one a granule pack) |
| `reviewed-merge-tests.log`, `reviewed-merge-skip.log` | 6: `pkg/reviewedmerge` against the simulator, and `REQUIRE_REVIEWED_MERGE=false` |

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| F1 | medium (walkthrough blocker) | The GitHub simulator could not stand in for GitHub under `cmd/reviewed-merge`. `GET …/commits/{sha}/pulls` listed only `number` and `merged_at`, with no `merge_commit_sha`, `base`, `head` or `user`. The pull request detail had no `merged_by`. Reviews had no `commit_id` or `author_association`. `GET …/git/commits/{sha}` was unanswered. Every commit therefore read as "pushed onto the branch", so item 6 could not be run against the simulator. | **Fixed** in `pkg/github/githubsim`: the full pull request for a merge commit, `merged_by` (`Sim.MergeAs`), approvals as reviews of the current head by a `COLLABORATOR`, `SubmittedReviews` for other cases, `git/commits` parents, and opt-in `RecordMergeCommits`. Covered by `TestReviewedMergeAgainstTheGitHubSimulator` (`pkg/reviewedmerge`). |
| F2 | low (operator clarity) | When a `github_workflow_run` schedule (the train's `release-train-on-stage1.yml`) dropped a firing behind its own open tick, the record read "#2 from the previous firing is still open **(state unknown)**" while #2 was plainly queued. Timed schedules name the state; the workflow-run path did not read it. Seen on the real daemon. | **Fixed** in `pkg/orchestrator/scheduledworkflow.go`. `TestWorkflowRunScheduleSkipNamesTheOpenFiringsState` fails without the fix. |
| F3 | low (stale comment) | `nightly.yml`'s `stage-1` comment says "The KVM and real-kubernetes jobs are not required yet and are left out". But `stage-1` `needs` them, and `candidate-gate.sh`'s `CHECK_QUALIFICATION_JOBS` requires them (`kvm-sandboxes`, `kvm-route-stress`, `real-kubernetes`). An rc whose run lacks them fails Stage 1 ("did not run on …, and Stage 1 requires it"). For the same reason the 2026-11-10 `daycap-ticks.sh` helper, which seeds 14 jobs, now judges every rc red. | Not fixed here: editing `.github/workflows/*` brings in the security review. Filed as a follow-up. |
| F4 | note (design) | A passing flake retry (`workflow_dispatch` with `jobs=<one job>`) closes the open Stage 1 repair, because `nightlyGreenReading` counts the skipped jobs as green (2f). That is right when the retried job was the only red one, as here. It would also close a repair that still holds a red from another run on another job, such as 2b's `sustained load` on `main`. The playbook says "the next fully green Stage 1". | Reported, not changed: a retry passing is how the train qualifies an rc (`candidate-gate.sh`), so this is a design call. |
| F5 | low | `scan-images.sh` copies a pack's guest disk out with `docker create <ref>`, which fails ("no command specified") for an image with neither `CMD` nor `ENTRYPOINT`. Real packs have `ENTRYPOINT granule-shim`, checked on `docker.io/grainai/sandbox:latest`, so only the walkthrough's synthetic pack hit it. | Not fixed; the harness gives its pack an `ENTRYPOINT`. |
| F6 | cosmetic | `REQUIRE_REVIEWED_MERGE=0` and `=no` skip correctly, but the warning always says `REQUIRE_REVIEWED_MERGE=false`. | Not fixed (`release-images.sh` waits for a person's Submit through `release-promotion`). |

Nothing else misbehaved.

## 1. A simulated day of the loop (`loop-day.log`)

`scripts/release-train-task.sh` ran on each tick under `faketime`. It ran
against a bare remote in `/tmp` with `RELEASE_MAX_PER_DAY=0`,
`RELEASE_QUALIFICATION_APP=none`, and a stub `grain` serving `tests.yml` /
`release.yml` and the 18 Stage 1 jobs.

| Pacific time | Case | Result | Tags afterwards |
|---|---|---|---|
| 06:40 | cut | acted | `v2.2.0-rc.1` |
| 07:00 | no Stage 1 yet | noop (PENDING) | — |
| 08:00 | rc.1 green → **release and cut in the same tick** | acted | `v2.2.0`, `v2.3.0-rc.1` (on the newer green c2) |
| 09:00 | `v2.3.0-rc.1` red → **drop and cut from a newer commit** | acted | `v2.3.0-rc.1-discarded` (no `Discard-Reason`: a red drop), `v2.3.0-rc.2` on c3 |
| 11:00 | rc.2 green → **second release of the day**; nothing newer | acted | `v2.3.0`; "no new LKG commit on main since …; nothing to cut" |
| 11:20, 11:40 | **no new commit → wait** | noop | — |
| 12:40 | a newer green commit | acted | `v2.4.0-rc.1` |
| 15:00 | green → **third release the same Pacific day** | acted | `v2.4.0`, `latest` on it |

Every line says "RELEASE_MAX_PER_DAY is 0, so there is no daily cap".

**Overlap group.** Both schedules declare `overlap: {skip: true, group:
release-train}`, and the daemon loaded them that way (`overlapGroup:
"release-train"` on both in `/api/schedules`). On the real daemon, Stage 1
verdicts arrived while tick #2 was open: the Stage 1-triggered schedule fired
once (#2) and recorded each later verdict as a dropped skip behind #2, never a
second tick (`daemon-items.log`, "release-train ticks: 1"). The 20-minute
backstop was not due within the run, so the cross-schedule hold rests on
`TestAnOverlapGroupHoldsAnotherSchedulesFiringUntilTheFirstCloses`
(`pkg/model`) and the `OverlapGroupInFlight` branch of
`scheduledworkflow.go`, which this run read but did not trigger.

## 2. One repair per red Stage 1 (`daemon-items.log`, items 2a–2f)

`start-daemon.sh` starts `trainsim.go` and a real daemon, then turns on
`acme/widgets`' declarations. The daemon reads the train's own
`.grain/schedules/release-train{,-on-stage1}.yml` and
`.grain/templates/release-train.yml` from the simulated repository over git
("declares 1 template(s), 0 suite(s), 2 schedule(s)"). Every Stage 1 verdict
below is a concluded `nightly.yml` run added to the simulator. The daemon
found each one through its own poller (`-poll-interval 2s`); nothing was
injected into its store.

| Step | Simulated run | Daemon |
|---|---|---|
| 2a | rc.1 red (`go, shuffled and repeated` failed) | **repair 3 filed**, priority 9, goal `nightly-green` / on_met `close`, evidence naming the failing jobs; one train tick (#2) |
| 2b | scheduled run on `main` red (`sustained load`) | **appended** to repair 3 as a comment ("appended it to the open repair 3"); no second repair; goal evidence moves to run 9004 |
| 2c | scheduled run on `main`, idle-gated: `idle-gate` success, every other job `skipped`, run `success` | **repair 3 stays open**; goal evidence unchanged (still run 9004). Neither a repair nor a goal reading |
| 2d | rc.2 fully green | **repair 3 closed** "goal met (nightly-green)", evidence "`nightly` run 9006 on `v1.1.0-rc.2` … is green" |
| 2e | a later red on rc.2 | a **new** repair 4 (the old one is closed) |
| 2f | a flake retry (`workflow_dispatch`): the failed job green, the rest `skipped` | repair 4 closed (F4) |

Throughout, the train's Stage 1-triggered schedule fired once (#2). Each
later verdict was recorded as a dropped skip behind #2, the record F2 fixes.
`pipeline.nightly-red-unattended` (1h queued / 3h waiting) was not walked in
real time. `TestAnUnattendedStage1RepairRaisesANotification` covers it.

## 3. Loop-idle with `GRAIN_RELEASE_LOOP_IDLE_AFTER=1m` (`daemon-items.log`, items 3a–3e)

Same daemon, after item 2. To leave no live rc, rc.2 was released as
`v1.1.0` and rc.1 was marked `-discarded`.

| Step | Change | Daemon |
|---|---|---|
| 3a | a green `chore:` commit on `main` | nothing raised after 140s: a chore owes no release |
| 3b | a green `fix:` commit | **raised** `pipeline.release-loop-idle:acme/widgets` (warning) after the 1m threshold plus the 1m poll. The body names the commit, the floor `v1.1.0`, and "newest tick [task 2](/tasks/2) ("Release train (Stage 1 verdict)") is queued (waiting 5m, priority 9)" |
| 3c | `v1.2.0-pending` staged | **cleared** within the poll interval |
| 3d | the `-pending` deleted | **raised again**. By then tick #2 had failed: the sandbox-less daemon has no agent backend, and the body says "newest tick [task 2](/tasks/2) … failed" |
| 3e | `v1.2.0-rc.1` cut | **cleared** |

Run on this daemon, the other documented suppressions are covered by
`TestReleaseLoopIdleIsQuietWhileSomethingExplainsTheWait`, not walked: an open
`[rc-blocker <rc>]`, `grain/rc-hold` in progress, and the triage window of
`pipeline.rc-failed`. "Starved" is covered by
`TestReleaseLoopIdleSaysWhetherTheNewestTickIsStarvedOrFailed`.
`/api/notifications` lists cleared notifications too, with `clearedAt` set,
so the harness counts only the uncleared ones.

## 4. Idle-gate (`idlegate.log`)

`idlegate.go` pulls the `gate` step out of `.github/workflows/nightly.yml` as
committed and runs it with `bash`. It points `GITHUB_API_URL` at a loopback
fake Actions API and pins `IDLE_GATE_NOW_EPOCH`. All 14 scenarios pass:

- `push` and `workflow_dispatch` always run (`run=true`).
- `schedule` with an rc run that concluded in the last 24h skips (`run=false`). This holds whatever the verdict: a red rc counts too. It also holds with a `refs/tags/` prefix, with fractional-second timestamps, and with the rc run on page 2 behind 100 newer `main` runs.
- These run instead: a cancelled rc run, an rc run 25h old, `main`-only runs, and `{}`.
- **The gate fails open.** HTTP 500, 403 or an HTML body all give `run=true` with an `::warning title=idle-gate::` line. curl's `--retry 3` costs about 7s per error.

When `run=false`, every job skips, `stage-1` included (`if: always() && needs.idle-gate.outputs.run == 'true'`), so the run concludes green. On the daemon (2c), such a run neither filed a repair nor met the goal.

## 5. `make scan-images` (`scan-images.log`)

Docker was available, so the real path ran. `make scan-images` called
`scripts/scan-images.sh` with its pinned Trivy, then `cmd/image-vulns`, over:

- `walk-grain:vuln`: `FROM scratch` with one Go binary built against `golang.org/x/crypto v0.16.0`, the injected finding.
- `walk-sandbox:pack`: labelled `dev.granule.granulefile.kind=pack`, with an ext4 `/var/lib/granule/guest/disk.img` holding the same binary. The script scanned that disk as `sandbox-rootfs` with `trivy vm`.

| Allowlist | Exit | Gate |
|---|---|---|
| none | 2 | **FAIL**: 26 fixable HIGH, 13 in `grain` and the same 13 in `sandbox-rootfs` (CVE-2024-45337 among them) |
| one entry, `image: "*"`, every id above, expires in 30 days | 0 | **PASS** |
| the same entry, expired yesterday | 2 | **FAIL** again; every finding says "allowlist entry (owner walkthrough) expired 2026-11-10", and the entry is listed under "Allowlist entries that have expired" |
| the waiver plus an unmatched `grain` entry and a `hub` entry | 0 | PASS; the `grain` entry is listed as stale. The `hub` entry is not listed, because `hub` was not scanned, as `cmd/image-vulns` intends ("An entry for an image this run did not scan says nothing either way") |

## 6. `cmd/reviewed-merge` (`reviewed-merge-tests.log`, `reviewed-merge-skip.log`)

Against the stock simulator, every case was refused for the wrong reason
(F1). With F1 fixed, `TestReviewedMergeAgainstTheGitHubSimulator` drives real
merges through `PUT …/pulls/N/merge` on the simulator. It reads them back
over HTTP with `reviewedmerge.REST` and the `Policy.CheckChain` that
`cmd/reviewed-merge` calls:

| Case | Verdict |
|---|---|
| self-merged (author alice merges, no approval) | refused: "pull request #1 was merged by alice, with no approval …" |
| reviewed merge (bob approved) on top of the self-merged one, chain from before both | refused, on the self-merge |
| approved by another, chain from the self-merge | passed, `via: review, by: bob` |
| grain-merged (`-mergers grain-bot`) | passed, `via: grain` |
| grain-merged with no `-mergers` | refused |
| approval of an earlier head | refused: "bob's approval was of an earlier head …" |
| approval by someone with no standing (`NONE`) | refused |
| a direct push to `main` | refused: "not the merge commit of any pull request" |

`cmd/reviewed-merge`'s own flag and exit-code handling is
`TestPrintsTheRecordOrRefuses`. `check_reviewed_merge` from
`release-images.sh`, run with `REQUIRE_REVIEWED_MERGE=false|0|no`, printed
the WARNING, returned 0 and left no record to attest (F6 for the message).

**Cosign pins.** Not walked separately: nothing was signed or published, so no cosign was downloaded.

## Gates on this change

- No `scripts/release*`, `scripts/release-train-task.sh`, `promote*.yml` or `.github/workflows/*` file changed, so neither the `release-promotion` Submit nor the workflow security review applies. F3 and F6 are left as follow-ups for that reason.
- `pkg/orchestrator/scheduledworkflow.go` is not a classified path.
