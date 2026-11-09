# Test run — 2026-11-08 §5: OSS release assets, `RELEASE_QUALIFICATION_APP=none`, neutral RC re-qualification, pending-release persistence & merge-queue unresolved review conversations

**Plan:** [`docs/test-plans/2026-11-08.md` §5](../test-plans/2026-11-08.md)
**Branch:** `grain/098073e4/task-3640-walkthrough-oss-release-assets` (task 3640, filed by [Task #3495](/tasks/3495)), based on `origin/main` `ed863dc6`
**Harnesses & transcripts:** [`2026-11-08-releases-qualification-mergequeue/`](2026-11-08-releases-qualification-mergequeue/)
**Outcome:** I walked items 1–3 against live processes: release scripts in `/tmp` scratch clones, a writable fake GitHub Releases API, `githubsim` over HTTP, and real `grain daemon`s on SQLite and PostgreSQL. I walked item 4 cycle by cycle on the orchestrator's merge queue and review-thread relay, with `githubsim` enforcing conversation resolution. **I found one bug (F1) and fixed it here, with a regression test.** Everything else the plan asks about behaved as specified, apart from one place where the plan is out of date: `main` deliberately reversed "unset means none" (see item 2). The PostgreSQL transcript predates a wording change in the harness: its "note: watcher polled in between" line is the approve's own tag read (see §5.3).

## Environment

| | |
|---|---|
| Host | grain sandbox, Debian 12 |
| Toolchain | Go (repo `go.mod`), Node 20.19.0, Docker (`dockerd`), jq, shellcheck; PostgreSQL 15 from `apt-get install postgresql` (not preinstalled) |
| grain | `cd ui && npm ci && npm run build`, then `go build -o /home/debian/bin/grain ./cmd/grain` at this branch's base |
| Local servers | [`releases_fake.go`](2026-11-08-releases-qualification-mergequeue/releases_fake.go): a stateful, writable GitHub Releases + git refs REST API with an ordered request log (`127.0.0.1:4380`). [`githubsim_server.go`](2026-11-08-releases-qualification-mergequeue/githubsim_server.go): `pkg/github/githubsim` served as a GitHub Enterprise host (`/api/v3`, `/api/graphql`). Live daemons: `:4383` (item 1), `:4393` SQLite and `:4493` PostgreSQL (item 3) |
| Safety | I ran every `scripts/release-*.sh` and `scripts/candidate-gate.sh` invocation in a `git clone --no-local` scratch clone under `/tmp`, never in `/home/debian/work`. Both harnesses check afterwards that `/home/debian/work/.git/config` and its tag list are unchanged, and both checks pass |

To reproduce, from the repository root:

```bash
bash docs/test-runs/2026-11-08-releases-qualification-mergequeue/run-item1.sh            # §5.1 (needs dockerd)
bash docs/test-runs/2026-11-08-releases-qualification-mergequeue/run-item2.sh            # §5.2
bash docs/test-runs/2026-11-08-releases-qualification-mergequeue/run-item3.sh sqlite     # §5.3, SQLite
PG_URL='postgres://grain:grain@127.0.0.1:5432/grain_s5?sslmode=disable' \
  bash docs/test-runs/2026-11-08-releases-qualification-mergequeue/run-item3.sh postgres /tmp/s5-item3/postgres 4493
go test ./pkg/orchestrator/ -run 'TestWalkthroughUnresolved' -v                         # §5.4
```

## Findings

| # | Area (PR) | Bug | Fix | Regression |
|---|---|---|---|---|
| F1 | Merge queue, unresolved conversations (#877) | Suppose a task sent back for unresolved review threads finishes with one still open, and the queue hands the refusal to a person (`merge-blocked`). From then on, every refusal poll asked GitHub to merge again. `mergeRefusalWait`'s "ask once more, so the refusal is handed to a person" branch keyed only on `CompletedAt > SentBack`, which stays true forever. Five cycles after the hand-off cost two more doomed `PUT …/merge` calls, and the calls repeat at the poll cadence for as long as the thread stays open. | [`pkg/orchestrator/githubrefusal.go`](../../pkg/orchestrator/githubrefusal.go): the branch now also needs `!r.Escalated`. After the hand-off, open threads are waited on like any other unresolved conversation. A resolve still merges on its own, and Retry merge is still a fresh start. | [`walkthrough_unresolved_threads_test.go`](../../pkg/orchestrator/walkthrough_unresolved_threads_test.go) `TestWalkthroughUnresolvedConversationLeftOpenIsHandedOffWithoutLooping`. Without the fix it fails with `merge asked for 2 more time(s) after the hand-off` |

> [!NOTE]
> **The plan is out of date on unset `RELEASE_QUALIFICATION_APP`.** The plan (from #922) says unset or empty defaults to `none`. A later commit on `main`, `49e6e8f9` ("make RELEASE_QUALIFICATION_APP=none an explicit opt-in", from task 3585's security review), reversed that on purpose: defaulting to `none` turned the release gate from fail-closed into fail-open. Unset or empty now fails closed: `candidate-gate.sh` exits 64, and `release-from-trunk.sh` exits 1 and refuses to trust `grain/rc-hold`. `none` qualifies on Stage 1 only when it is set explicitly, in the environment or in `scripts/release.conf`. I tested what `main` does now, and it is correct. This is not a bug.

## §5.1 Release assets, CLI archives, moving channels & the daemon prober — PASS

Harness: [`run-item1.sh`](2026-11-08-releases-qualification-mergequeue/run-item1.sh). Transcript: [`item1.txt`](2026-11-08-releases-qualification-mergequeue/transcripts/item1.txt), with fake-server request logs `item1-log-*.txt`.

The scratch clone's history is `c0 → c1 → c2` on `main` plus a side commit `x` off `c0`. The images are tiny `FROM scratch` images built per commit: `grain`, `guest` and `k8s-sandbox`. The `grain` image also carries a static `grain` binary, because the daemon health-checks the image it loads. The state files are `RELEASE_IMAGE_TARGET=assets`.

- **Package & publish `sha-<c1>` + `channel-main`.** `release-assets.sh package` writes 3 gzipped `docker save` tarballs, 3 `.sha256` sidecars and a schema-2 `images.json`, and `validate` accepts them. `publish sha-<c1> channel-main` creates the `sha-` holder with all 7 assets, uploading the tarballs and sidecars first and `images.json` last. `channel-main` holds `images.json` alone, as contract v2 requires: it points at `sha-<c1>`, and its tag is created at `c1`.
- **CLI archives.** `RELEASE=v3.2.0 scripts/build-cli.sh` builds `grain-{linux,darwin}-{amd64,arm64}.tar.gz` and `checksums.txt`, and `cli-validate` accepts them. After `git tag v3.2.0 c1; git tag latest c1`, `promote` (no argument) publishes `v3.2.0` with its own copies of the tarballs and marks it GitHub's latest release. `cli-publish v3.2.0` uploads the 4 archives followed by `checksums.txt`. A second `cli-publish` leaves the existing `checksums.txt` alone.
- **A backward / non-ancestor move is refused.** `publish channel-main` with `x`'s manifest prints `channel-main: holds <c1>, which is not an ancestor of <x> …; leaving it where it is`. The fake server's log shows **no write at all**: no upload, delete, rename or tag move. The channel still names `c1`. The script exits 0 here by design: the release workflow publishes `sha-X channel-<branch>` in one call, and a re-run of an older commit must not fail the job. The refusal is the channel staying put.
- **`latest`.** `publish latest` dies with `there is no release named latest: the latest channel is GitHub's latest-release marker on a vX.Y.Z (contract v2)`. I tagged `v3.2.1` at `c2` while the git tag `latest` still names `c1`, then ran `promote v3.2.1`. It published `v3.2.1` but left GitHub's latest marker on `v3.2.0`. `promote-version` prints `v3.2.0`, the version the git tag `latest` names.
- **Moving `channel-main` forward onto `sha-<c2>`.** In the request log, every tarball and sidecar upload to `sha-<c2>` comes first, then `sha-<c2>`'s `images.json`. Next comes the `channel-main` tag `PATCH` to `c2`, then `POST images.json.next`, then `DELETE` of the old `images.json`, then `PATCH` renaming `.next` to `images.json`. The new manifest therefore lands last, and no reader can see a manifest naming tarballs that are not there yet. Under contract v2 a channel carries no tarballs of its own, so the plan's "old `images.json` deleted before the tarball uploads" corresponds to holder-first ordering plus the `.next` swap.
- **The live daemon prober** ran as `grain daemon -upgrade-image-source release-assets -upgrade-release-repo 127.0.0.1:4380/ai-on-gke/grain -github-host 127.0.0.1:4380 -github-insecure-http`.
  - `/api/upgrade/targets` lists `latest`, `main` and `v3.2.1`.
  - `POST /api/upgrade {"branch":"main"}` loads `grain-image.tar.gz` from `release channel-main` after checking it against its `images.json` sha256 and sidecar. Every remaining step then runs: health check, unit flags (all 14 accepted), schema (unchanged at 128), record image, restart.
  - The recorded image's ID is `c2`'s `imageId` from `sha-<c2>`'s manifest.

## §5.2 `RELEASE_QUALIFICATION_APP`, the triage-pending `grain/rc-hold` race & neutral Stage-2 re-qualification — PASS (36/36)

Harness: [`run-item2.sh`](2026-11-08-releases-qualification-mergequeue/run-item2.sh). Transcript: [`item2.txt`](2026-11-08-releases-qualification-mergequeue/transcripts/item2.txt). The harness runs the real `release-from-trunk.sh` and `candidate-gate.sh` from a scratch clone. It uses `scripts/release-from-trunk_test.sh`'s own fixture helpers, sourced with the version renamed to `v3.2.0` (floor 3.2, latest `v3.1.0`). The `grain/rc-hold` check runs it feeds in have exactly the shape `syncRedRCHolds` posts.

- **Gate:**
  - Unset: exit 64, `CHECK_QUALIFICATION_APP is unset`.
  - `none` with a green `nightly.yml`: exit 0, `SKIPPED grain/release-qualification: CHECK_QUALIFICATION_APP=none (qualified by Stage 1 nightly.yml)`.
  - `none` with a red `nightly.yml`: does not qualify.
- **Train (auto-promote on):**
  - Unset: exit 1, nothing promoted.
  - `RELEASE_QUALIFICATION_APP=none`: `Qualification: PASS v3.2.0-rc.1`, and `v3.2.0` is pushed on rc.1's commit without any GitHub App check run.
  - `RELEASE_QUALIFICATION_APP=none` in `release.conf`, with the environment unset: also promotes.
  - Red `nightly.yml` with `none`: no `v3.2.0` and no `-pending`.
- **The rc-hold race (#849).** Stage 2 has failed on rc.1, and the train ticks before any `[rc-blocker v3.2.0-rc.1]` task exists. Grain's hold is `in_progress`, titled `Holding v3.2.0-rc.2 (triage pending)`. The train exits 0, says `v3.2.0-rc.2 is held by grain/rc-hold on <rc.1>`, emits `rc_held=v3.2.0-rc.2` and **burns no rc.2**. A second tick still burns none.
  - When the hold completes with `Triaged with no blocker` (the steward's closing report), the next tick cuts rc.2 on `c3`.
  - A completion forged by another app (`github-actions`) is not trusted, so no rc.2 is cut.
- **Neutral Stage 2 (#848):**
  - `completed/neutral` alone: no rc.2, nothing staged.
  - Attempt `#2` (`external_id …@rc#2`) `in_progress`: PENDING on the same rc.1.
  - `#2` `success`: `Qualification: PASS v3.2.0-rc.1` and `v3.2.0-pending` on rc.1's commit, with no rc.2 burned.
  - `#2` `failure`: does not qualify.
- **Daemon side.** These walk the real `releasequalification`/`releaseredrc` state machine against a fake forge ([`go-tests.txt`](2026-11-08-releases-qualification-mergequeue/transcripts/go-tests.txt)):
  - `TestARedCandidateRaisesRCFailedAndHoldsTheNextUntilItsBlockersLand`, `TestTriagePendingHoldCompletesWhenStewardTriagesWithNoBlockerOrCleared` and `TestTriagePendingHoldCompletesAfterGraceWindowOrInboxClear` cover `raiseRCFailed` posting the hold before the failed qualification, and the hold completing early on a steward report or an Inbox clear.
  - `TestANeutralCandidateRequalifiesOnceTheInfraStallIsAckedOrCleared` covers `QualificationAttemptTag(base, 2)` launching on the same commit after the stall clears, then `completed/success`.

> [!NOTE]
> I did not stand up a live daemon running `releasequalification` with a GitHub App: that would need a nightly run, qualification fixtures and an App identity. Instead, the train was fed the exact check runs the daemon posts, and the daemon's side is the Go state-machine tests above.

## §5.3 Durable `release_approval` on an unset-`ci_mode` Actions repo, SQLite & PostgreSQL — PASS (after harness fixes)

Harness: [`start-item3.sh`](2026-11-08-releases-qualification-mergequeue/start-item3.sh) and [`run-item3.sh`](2026-11-08-releases-qualification-mergequeue/run-item3.sh). Transcripts: [`item3-sqlite.txt`](2026-11-08-releases-qualification-mergequeue/transcripts/item3-sqlite.txt) and [`item3-postgres.txt`](2026-11-08-releases-qualification-mergequeue/transcripts/item3-postgres.txt).

Setup:

- A real `grain daemon -github-host 127.0.0.1:<sim> -github-insecure-http` talks to `githubsim`'s `acme/widgets`, which has `.github/workflows/promote-release.yml` and no `.grain/workflows`. The repo was added with `grain repo add` and never given a `ci_mode`.
- The daemon's GitHub credential is a token file under `-data-dir/secrets/github`.
- The release-pending watcher polls a repository's tags once every 10 minutes, and remembers when it last did in memory. So each daemon restart is one more watcher tick.

Both backends gave the same results: SQLite 12/12, PostgreSQL 12/12.

- **The watcher runs with `ci_mode` unset (#845/#846).** Once `acme/widgets` is on the allowlist, the daemon reads `GET /repos/acme/widgets/git/matching-refs/tags/v` with no `ci_mode` ever set. `ReleasesFromGitHubActions` falls through to "no grain CI workflows → Actions".
- **Case A, approve before the watcher's poll (#843/#844).**
  - `v1.4.0-pending` was tagged after the boot poll. Two concurrent `grain release approve -repo acme/widgets v1.4.0` ran within the 10-minute window. Exactly one succeeded (`approved v1.4.0: promote-release is tagging it …`, which signed and dispatched `promote-release.yml`). The other was refused with exit 2: `v1.4.0 was already answered: debian dispatched promote-release.yml for it at …; watch that run rather than starting another`.
  - Then three daemon restarts, each of which polls the tags immediately, then a later approve. After all of that, `release.pending:v1.4.0` was **never raised**, `githubsim` holds **one** `promote-release.yml` dispatch for `v1.4.0`, and the later approve is still refused.
  - The decision lives in the `release_approval` table: in SQLite under `-data-dir`, in PostgreSQL through `-postgres-url`.
- **Case B, raised first, then approved.**
  - After `v1.4.1-pending` and a restart, the watcher raised `release.pending:v1.4.1` ("v1.4.1 is ready to promote", warning).
  - `grain release approve v1.4.1` dispatched once and cleared the notification.
  - Two more restarts, each with a poll and with `v1.4.1-pending` still on the remote, did not re-raise it. Total dispatches: `v1.4.0` ×1 and `v1.4.1` ×1.

Two corrections to my first harness, neither of them a grain bug:

- **`githubsim` has to be told the workflow exists.** It answers `404` to a dispatch of a workflow it was not told about, until you set `WorkflowFiles`. The daemon handled that failure correctly: both approves failed with a 500 naming the 404, and no decision was recorded, so a fixed retry worked.
- **The harness's poll counter is shared with the approve.** The approve reads the `-pending` tag through the same `matching-refs` endpoint the watcher polls, so that counter shows the approve's own reads. That the approve came before the watcher's poll rests on the 10-minute `releasePendingPollInterval` and on the boot-time poll.

## §5.4 Merge queue: `RefusalUnresolvedThreads`, outdated threads, `grain pr threads` / `grain pr resolve` — PASS (after F1)

Harness: [`walkthrough_unresolved_threads_test.go`](../../pkg/orchestrator/walkthrough_unresolved_threads_test.go). It runs the real `SyncPullRequests` and `SyncReviewThreads` together every cycle, as the daemon's cycle does, against `githubsim` with `RequireConversationResolution`.

The pull request carries three threads:

- an **outdated** thread: comment 234726, `line: null`;
- an **active** thread: comment 234727, `scripts/run.sh:14`;
- a thread from grain's own draft review for a review task: comment 234728, linked via `LinkDraftReview`.

What happened:

1. The first merge attempt hits GitHub's `Repository rule violations found … A conversation must be resolved …`, which is classified as `RefusalUnresolvedThreads` (`TestClassifyMergeRefusal`).
2. The task goes back to `queued`, not to a person. Over that cycle and two more, it gets exactly **one** comment: no second review-relay run. The comment lists `thread-234726` `(outdated)` and `thread-234727` `scripts/run.sh:14`, and leaves out grain's own `thread-234728`.
3. In turn 2, the thread view the sandbox CLI reads (`list_review_threads`, behind `grain pr threads --unresolved`) shows both external threads open, with the outdated one flagged outdated.
4. `resolve_review_thread` (behind `grain pr resolve --thread-id`) resolves both. The task finishes, and the next cycle merges the pull request.
5. In the second task, turn 2 leaves `thread-234726` open. The next refusal is handed to a person (`merge-blocked`), and the task is not sent back again. Before F1, five more cycles asked GitHub to merge twice more. After F1 they ask nothing.

`grain pr threads` needs no `api:ci-read`: `TestCLIActionsRefuseAndRoute` in `pkg/gitproxy` sends `GET /cli/pr/threads?unresolved=1` with a token that has no CI grant, gets 200, and gets its own task's threads. That passes, as do `go test ./pkg/sandboxcli ./pkg/mcp ./pkg/forge -run 'Thread|Resolve|Merge'`.

> [!NOTE]
> I did not dispatch a scripted agent through a real daemon to run the in-sandbox `grain` binary: these daemons have no sandbox backend. The CLI → git proxy → `list_review_threads` / `resolve_review_thread` route is covered by `pkg/gitproxy`, `pkg/sandboxcli` and `pkg/mcp` tests. The queue's side is the cycle-level walkthrough above.

## Verification

- `go test ./pkg/orchestrator/ -run 'TestWalkthroughUnresolved|UnresolvedConversation|MergeRefus|Refusal|RedCandidate|TriagePendingHold|NeutralCandidate|ReleaseQualificationRerunsAnInfraVerdict|ReleasePending'`: ok.
- `go test ./pkg/gitproxy ./pkg/sandboxcli ./pkg/mcp ./pkg/forge -run 'CLIActions|Thread|Resolve|Merge'`: ok.
- `shellcheck -S info docs/test-runs/2026-11-08-releases-qualification-mergequeue/*.sh`: clean.
- Without F1's fix, `TestWalkthroughUnresolvedConversationLeftOpenIsHandedOffWithoutLooping` fails with `merge asked for 2 more time(s) after the hand-off`.
- No CLI flag or help text changed, so the CLI spec was not regenerated. No `scripts/release-*.sh` or `.github/workflows/*` file changed, so no `.grain/triggers/release-*` review is needed.
- GitHub Actions on the pull request: see the PR checks.
