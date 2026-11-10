# Release train walkthrough — 2026-11-10 (test plan §8)

Task 3955 walked [§8 of `docs/test-plans/2026-11-10.md`](../test-plans/2026-11-10.md#8-continuous-release-train-daily-cap-stage-1-holds-release-notes-and-the-inboxs-pending-releases)
against `main` at `37015c37`. Everything ran in scratch clones under `/tmp`,
with local bare remotes, a stub `grain` CLI and a local HTTP server standing
in for GitHub. No tag was pushed anywhere real, nothing was published, and
no release script ran inside the task checkout.

Harnesses and logs are in [`2026-11-10-release-train/`](2026-11-10-release-train/):

| File | Item |
|---|---|
| `daycap-ticks.sh`, `daycap-ticks.log` | 1: `release-train-task.sh` ticks under `faketime` |
| `stage1-gate.sh`, `stage1-gate.log` | 2: the same Stage 1 histories judged by `candidate-gate.sh` |
| `promote-cli-decision.sh`, `promote-cli-decision.log` | 6: `promote.yml`'s CLI build decision with real `curl` |

The Go and Playwright coverage is committed as regression tests:
`pkg/orchestrator/releasestage1_walkthrough_test.go` (items 2, 3),
`pkg/orchestrator/releasenotes_walkthrough_test.go` (item 4), and
`ui/e2e/release-train-pending-walkthrough.spec.js` (item 5).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| F1 | high (stuck train) | When nightly.yml failed on its `stage 1 qualification verdict` roll-up job alone, `candidate-gate.sh` passed Stage 1 (it ignores the roll-up), but grain went by the run's conclusion. Grain started no Stage 2 and raised nothing, so the train waited without end for a `grain/release-qualification` check that never came. | **Fixed** in `releasestage1.go`: when every job passed, Stage 1 counts as green whatever the run's conclusion, and the settled verdict is remembered. Covered by `TestStage1JobHistoriesFromTheReleaseTrainWalkthrough/the_roll-up_job_failing_alone…`, which fails without the fix. |
| F2 | build break on `main` | `pkg/ui/access_roles.go:1031` called `s.templateDeclaredPush` inside a `*Client` method: PR #1069 and the Client split (#1056/#1051) merged cleanly but crossed. `main` stopped compiling, so every `go` CI job was red. | **Fixed** (`s.` → `c.`), as its own commit. |
| F3 | low (log clarity) | `promote.yml`'s CLI step decides correctly in all five cases. When it does build, though, its log shows only `curl: (22) … 404` lines and never says why. A 403/000 from the API (rate limit, network) also builds without saying so. | Not fixed here: editing `promote.yml` waits on the human-required `release-promotion` trigger. Filed as a follow-up. |

## 1. Daily cap across a simulated day (`daycap-ticks.log`)

Each tick ran `scripts/release-train-task.sh` under `faketime` with no
`RELEASE_NOW`, so both the scripts and git read libfaketime's clock.
`RELEASE_MAX_PER_DAY=1`, `RELEASE_DAY_TZ=America/Los_Angeles`, and
`RELEASE_QUALIFICATION_APP=none` (Stage 1 alone qualifies).

| Fake time (Pacific) | Case | `RELEASE_TRAIN_RESULT` | Tags / outputs |
|---|---|---|---|
| 10-31 08:00 PDT | cut | acted | `v2.2.0-rc.1` |
| 10-31 10:00 PDT | rc.1 green → released on day 1 | acted | `v2.2.0`, `day_done=2026-10-31` |
| 10-31 12:00 PDT | day done | noop | `day_done=2026-10-31` |
| 10-31 14:00 PDT | hand-cut `v2.3.0-rc.1`, green the same day | acted | `v2.3.0-rc.1-discarded`, `Discard-Reason: daily-cap` |
| 11-01 00:05 PDT | first tick after midnight | acted | `v2.3.0-rc.2` on main's newest green commit (c2) |
| 11-01 01:30 PDT → 01:30 PST | qualifies before fall-back; same Pacific day after it | acted, then noop | `v2.3.0`, `day_done=2026-11-01` twice |
| 11-01 23:45 PST (07:45Z 11-02) | still 11-01 under UTC−8 | noop | `day_done=2026-11-01` |
| 11-02 05:00 → 23:30 PST | cut, then still qualifying | acted, then noop | `v2.4.0-rc.1`; PENDING |
| 11-03 00:30 PST | rc cut before midnight, green after it | acted | `v2.4.0-rc.1-discarded` (`held-over`), `v2.4.0-rc.2` cut |
| 11-03 02:00 PST | rc.2 green, `RELEASE_AUTO_PROMOTE=false` | acted | `v2.4.0-pending` (counts as the day's release) |
| 11-04 09:30 PST | approve `v2.4.0` after a `v2.2.1` patch released that day | (approve) | `day_cap_bypassed=v2.4.0`; tag says "released past RELEASE_MAX_PER_DAY (1): 2026-11-04 … had already released v2.2.1" |
| 03-14 01:30 PST → 03:30 PDT (2027) | cut, then release across spring-forward | acted ×2 | `v2.5.0` |
| 03-14 07:00 PDT | hand-cut `v2.6.0-rc.1` green, `RELEASE_DAY_CAP_EXEMPT=v2.6.0-rc.1` | acted | "RELEASE_DAY_CAP_EXEMPT names v2.6.0-rc.1; the cap does not hold it" → `v2.6.0` |
| 03-14 23:30 PDT (06:30Z 03-15) | still 03-14 | noop | `day_done=2027-03-14` |
| 03-15 00:30 PDT (07:30Z) | first tick of the new day | acted | `v2.7.0-rc.1` |

Every case behaves as documented, and both DST transitions are counted in
Pacific calendar days rather than at a fixed offset.

## 2. Stage 1 judgments

`TestStage1JobHistoriesFromTheReleaseTrainWalkthrough` feeds each history to
`SyncReleaseQualification` through a stub `WorkflowRunsOnCommit`.
`stage1-gate.sh` feeds the same histories to `candidate-gate.sh check-qualification`.

| History | grain (after F1) | `candidate-gate.sh` |
|---|---|---|
| one job fails once, then passes | flake noted; no rc-failed; Stage 2 starts; flake budget not spent by one candidate | PASS |
| one job fails twice | `rc-failed` ("failed again on its retry (failure,failure): confirmed red"), `grain/rc-hold` triage pending | FAIL |
| three jobs fail once each | `rc-failed` "systemic: 3 jobs failed …", hold | FAIL systemic |
| matrix shards (1),(2) fail once | one job, waiting on its retry: nothing raised | PENDING, RERUN flake |
| shard (1) fails its retry | `rc-failed`, hold | FAIL |
| roll-up fails alone | **before F1: nothing raised, Stage 2 never started**; now Stage 2 starts | PASS |
| cancelled ×3 | `release-infra-stalled` "… 3 attempts in a row"; no rc-failed | INFRA, stalled |
| cancelled, then queued for 2h+ | `release-infra-stalled` "blocked on infrastructure for 2h1m" | PENDING |

Flake budget: two candidates' retries of one job raise `pipeline.flake-budget`
(`TestAStage1FlakeRetrySpendsTheFlakeBudget`); one candidate's retry never does.
When a red rc is dropped in the same tick and no hold has been posted yet,
the train waits one run before cutting `rc.N+1`. That is
`release-from-trunk_test.sh` ("waits for the next run …") and case 2 of
`release-train-task_test.sh`, both green.

A job that stays `queued` with no infra attempt is not flagged by grain's
Stage 1 tracker: it counts as still running, as it does in the gate. The
`releasestall.go` reconciler covers a workflow that never finishes, so this
was not changed.

## 3. Steward and escalation lifecycle

`TestRedRC3ToRC5HoldEachAndEscalateOnceUntilTheRelease`: rc.3, rc.4 and
rc.5, each red in Stage 1, each post a triage-pending `grain/rc-hold` on
their own commit. `pipeline.release-escalated:v1.4.0` is raised once, on
rc.3 (the third attempt); rc.4 and rc.5 keep the same notification id, and
`v1.4.0`'s release clears it. Neither the steward prompt
(`pkg/model/pipeline_steward_template.go`) nor `docs/pipeline-steward.md`
still tells anyone to backport to `release/vX.Y.Z`. The only mention left
says there is no such branch.

## 4. Release-notes reconciler on this repository's tags

`TestReleaseNotesOnThisRepositorysTagsFilesV330AndV340Once` mirrors the
repository as of today: `v3.0.0..v3.4.0` with their rcs, and `docs/releases/`
documenting v3.1.0, v3.1.1 and v3.2.0. It runs the repository's own
`.grain/templates/release-notes.yml`. Over three passes, the third after
the in-memory watch is cleared to simulate a restart, it files exactly
`docs(releases): write the release notes for v3.3.0` (previous v3.2.0) and
`… v3.4.0` (previous v3.3.0). Both are ready to run, none is filed for
v3.0.x, and no duplicates appear. A patch on a documented minor (`v3.1.2`)
and a template with a person-only grant (held) are already covered by
`TestReleaseNotesSkipsSupersededHistoricalTagsWhileFilingHotfixPatches`
and `TestReleaseNotesRespectsRepoEligibilityAndPersonOnlyGrantGuard`.

**Live deployment: not verified.** This run holds neither `self-debug` nor
any read of other tasks (`grain grep … task://` sees only task 3955), so it
could not see whether the live deployment has filed the v3.3.0/v3.4.0
tasks or whether its daemon build includes `2d94a55e`. No task was filed
by hand. An operator should check the task list for those two titles.

## 5. Inbox pending releases

Over `grain demo -github-sim -fake-ci` in Chromium:

- `release-approval-actions-sim.spec.js`: approve (inputs
  `short_soak_ok/allow_breaking: "false"`, a signed record that verifies
  against `/api/release-approval-key`, a second press refused), reject, and
  a failed `promote-release.yml` dispatch (`GRAIN_RELEASE_APPROVAL_KEY`
  unset) listed under Asked with its run id and remedy. **BREAKING** with
  Approve breaking confirms and sends `allow_breaking: "true"`.
- `inbox-notifications-layout.spec.js` and
  `release-from-trunk-approval-walkthrough.spec.js` pass.
- New `release-train-pending-walkthrough.spec.js` renders the four kinds
  (normal, breaking, short soak & breaking, failed dispatch) at 1440 and
  390 px in light and dark, with bounding-box checks that no row starts
  off-screen or overflows. Approve short soak & breaking confirms first,
  with a message naming both, and dispatches `short_soak_ok: "true"` and
  `allow_breaking: "true"`.

Not done here: driving the simulator to fail a dispatched run *after* the
approve, to time the "marked failed within seconds" poll and the re-raise.
The failed-dispatch state was seeded as a notification instead. This is
filed as a follow-up. The screenshots were written by the spec
(`RELEASE_TRAIN_SHOTS`) and attached to the task; this sandbox could not
show the images, so they were checked by the bounding-box assertions only.

## 6. CLI build skip (`promote-cli-decision.log`)

The step runs with real `curl` against a local server:

| Case | build | Log |
|---|---|---|
| no GitHub Release, no `sha-<commit>` assets | false | "GitHub Release v9.9.0 does not exist and sha-… has no release assets to create it; skipping grain CLI build" |
| release assets only | **true** | only `curl: (22) … 404` |
| full release, no CLI yet | **true** | only `curl: (22) … 404` |
| release already carries `checksums.txt` | false | "v9.9.0 already carries the grain CLI" |
| asked for by name | true | only `curl: (22) … 404` |

The decisions are right. The build cases' logs are F3.

## Triggers

This branch changes `pkg/orchestrator/releasestage1.go`, `pkg/ui/access_roles.go`,
tests and docs. It does not touch `scripts/release*`, `promote*.yml` or
`publish-release.yml`, so the human-required `release-promotion` trigger
does not apply. `pkg/ui/access_roles.go` may draw the automated security
review.
