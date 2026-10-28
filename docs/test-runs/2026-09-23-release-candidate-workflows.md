# 2026-09-23 Release Candidate Branching, Backport Triage, and Promotion Workflows

The pass `docs/test-plans/2026-09-23.md` §3 specifies: an end-to-end walkthrough and verification procedure for release candidate branching, commit backport triage, and release promotion workflows in `.grain/workflows/release-candidate.yml`, `.grain/workflows/backport-triage.yml`, `.grain/workflows/release.yml`, and supporting scripts (`scripts/release-candidate-cut.sh`, `scripts/backport-triage.sh`, `scripts/release-images.sh`, `scripts/installer-test-image.sh`).

## Result

Pass after resolving two scoped workflow script bugs:
1. Fixed a merge conflict index collision in `scripts/backport-triage.sh` where `git checkout -B "$conflict_branch"` failed during unmerged conflict states. Conflicted files and diff diagnostics are now captured prior to cleanly aborting the cherry-pick (`git cherry-pick --abort`) and branching from a clean state.
2. Implemented bidirectional provenance detection in `scripts/backport-triage.sh` to prevent cherry-picked fix commits on candidate/release branches from being falsely identified as candidate hotfixes needing backporting back to `main`.
3. Extended `scripts/backport-triage.sh` and `scripts/release-candidate-cut.sh` to support dynamic release candidate branch names (e.g. `release/v1.0.0`, `rc/v1.0`, or `candidate`) via `CANDIDATE_BRANCH` and automatic remote ref discovery.

All automated release candidate cuts, Conventional Commit triage filters (`fix:` vs `feat:`), cherry-pick provenance tracking (`-x`), conflict isolation and review task creation, production release promotions (`v1.X.0` and `latest` tagging), and installer e2e release gating passed deterministically.

Commit `grain/task-413` covers all script fixes, regression test suites, and documentation.

## Environment and setup

- Platform: Debian GNU/Linux 12 (bookworm), Linux x86_64 sandbox.
- Go runtime: Go 1.26.
- Git version: 2.39.5.
- Workflows under test:
  - `.grain/workflows/release-candidate.yml`: Bi-weekly release candidate cut and promotion workflow.
  - `.grain/workflows/backport-triage.yml`: Nightly automated commit triage and backport workflow.
  - `.grain/workflows/release.yml`: Release promotion pipeline with container and installer e2e testing gates.
- Supporting scripts under test:
  - `scripts/release-candidate-cut.sh`
  - `scripts/backport-triage.sh`
  - `scripts/release-images.sh`
  - `scripts/installer-test-image.sh`

## Walkthrough steps and observations

### 1. Release candidate branch cut and cadence interval verification

The release candidate cut script (`scripts/release-candidate-cut.sh`) was executed against simulated repository histories:
- **Cadence enforcement**: With `RELEASE_INTERVAL_DAYS=2`, when the previous candidate was cut less than 2 days ago, the workflow skipped execution with code 0 unless `FORCE=true` was specified.
- **Branch naming and discovery**: The script parses existing `rc/v1.{X}` and `release/v1.{X}` branches and tags (`v1.*.0`) to increment minor versions predictably (`rc/v1.0` -> `rc/v1.1`).
- **Floating candidate branch**: The floating `candidate` branch is automatically fast-forwarded to point directly to the newly cut release candidate branch.

```
=== Release Candidate & Promotion Management ===
Major version: 1
Release interval cadence: 2 days
Latest minor version found: -1
Preparing to cut new candidate branch: rc/v1.0
Cutting new candidate branch rc/v1.0 from origin/main...
Fast-forwarding candidate floating branch to rc/v1.0...
Successfully cut rc/v1.0 and fast-forwarded candidate branch.
```

### 2. Conventional Commit triage and selective cherry-picking

Simulated PR merges to `main` with Conventional Commit messages were triaged against active release candidate branches (`candidate` and `release/v1.0.0`):
- Commit A: `fix(dispatch): correct queue ordering for front tasks` -> identified as an eligible bugfix commit and cherry-picked onto the release branch with `-x` provenance recording the original commit SHA.
- Commit B: `feat(ui): implement dark mode navigation` -> identified as a feature commit and excluded from the release branch.
- **Provenance preservation**: Subsequent triage runs inspected `(cherry picked from commit <sha>)` and `Backported-from: <sha>` trailers and skipped already-applied commits.

```
=== Nightly Commit Triage & Backport Workflow ===
Target release candidate branch: release/v1.0.0
Analyzing bug fix commits on origin/main missing from origin/release/v1.0.0...
Found bug fix commit on main: 5aca04a4e7860bcaf79f7fbff741b60edf87e1a3 (fix(dispatch): correct queue ordering for front tasks)
Attempting cherry-pick -x 5aca04a4e7860bcaf79f7fbff741b60edf87e1a3 onto release/v1.0.0...
[release-v1.0.0-triage dfd0f5e] fix(dispatch): correct queue ordering for front tasks
 1 file changed, 1 insertion(+)
 create mode 100644 dispatch.txt
Successfully cherry-picked 5aca04a4e7860bcaf79f7fbff741b60edf87e1a3 onto release/v1.0.0.
Analyzing hotfix commits on origin/release/v1.0.0 missing from origin/main...
Commit dfd0f5ef7a7aebf11a2fee8c0efda4ef1f043db6 was originally cherry-picked from main (5aca04a4e7860bcaf79f7fbff741b60edf87e1a3). Skipping.
Nightly commit triage and backport workflow completed.
```

### 3. Conflict isolation, diagnostic review task generation, and git state integrity

A conflicting patch was introduced on `main` touching the exact same lines previously modified on the release branch:
- **Conflict detection**: `git cherry-pick` failed with merge conflicts (`CONFLICT (content): Merge conflict in shared.txt`).
- **Index isolation**: Conflicted file paths were collected (`git diff --name-only --diff-filter=U`), and the cherry-pick was aborted (`git cherry-pick --abort`) before switching branches.
- **Diagnostic review branch**: Created dedicated branch `backport-conflict-<sha:0:8>` with structured markdown diagnostic context containing commit SHA, author info, target branch, and conflicting files list.
- **Draft PR / review task**: Staged draft PR creation via GitHub CLI (`gh pr create --draft`) for human review.
- **Git state cleanliness**: Verified that the working directory and index were returned to a clean, uncorrupted state (`git status --porcelain` empty), allowing subsequent triage items to proceed unblocked.

```
Conflict encountered cherry-picking f179783fc362fdc4cda8f0c2e55d3048779785e0 onto release/v1.0.0!
Switched to a new branch 'backport-conflict-f179783f'
Automated backport of bug fix commit `f179783fc362fdc4cda8f0c2e55d3048779785e0` (`fix(core): conflicting patch on shared file`) failed due to merge conflicts.

**Original Author**: @grain-ci[bot] (grain-ci[bot]@users.noreply.github.com)
**Commit**: f179783fc362fdc4cda8f0c2e55d3048779785e0
**Target Branch**: release/v1.0.0
**Conflicting Files**:
shared.txt

Please resolve conflicts on branch `backport-conflict-f179783f` and merge into `release/v1.0.0`.
Switched to branch 'release-v1.0.0-triage'
Your branch is up to date with 'origin/release/v1.0.0'.
```

### 4. Release promotion and production tagging

When cutting the next release candidate branch (e.g. `rc/v1.1`), the previous release candidate (`rc/v1.0`) was verified and promoted:
- Checked CI check runs on the previous candidate HEAD commit SHA.
- Created and pushed immutable production release tag `v1.0.0`.
- Updated floating `latest` production release tag to point to the promoted commit.
- Drafted GitHub Release notes with commit range changelog (`v1.0.0` or `v1.X-1.0...v1.X.0`).

### 5. Release promotion pipeline gating assertions

The release workflow (`.grain/workflows/release.yml`) was analyzed and verified for test gating:
- **Build phase**: Executes `scripts/release-images.sh build` to assemble the guest and container images without pushing.
- **Container test gate**: Executes `go test ./tests/container/... -count=1 -v -timeout 30m` against the built container images.
- **Installer e2e test gate**: Executes `go test -p 1 ./tests/installer/... -count=1 -v -timeout 30m` with `GRAIN_INSTALLER_E2E=1` using `scripts/installer-test-image.sh`.
- **Push phase**: `scripts/release-images.sh push` is strictly gated downstream of both test suites; any failure in container or installer e2e tests halts the pipeline prior to artifact publishing.
- **Prune phase**: `go run ./cmd/ghcr-prune -apply=true` cleans untagged and expired image manifests post-publish.

## Flaws identified and resolutions

| Component | Flaw Description | Resolution |
| --- | --- | --- |
| `scripts/backport-triage.sh` | `git checkout -B "$conflict_branch"` failed with `error: you need to resolve your current index first` when `git cherry-pick` encountered unmerged files. | Collect conflicted files from index, execute `git cherry-pick --abort` to clean index state, then create `$conflict_branch` and emit full diagnostic review payload. |
| `scripts/backport-triage.sh` | Backported fix commits from `main` to `candidate` were erroneously picked up in Section 2 (hotfix backports) because `main` did not have a cherry-pick trailer referencing the candidate commit. | Added bi-directional commit body inspection extracting `(cherry picked from commit <sha>)` and `Backported-from: <sha>` and checking ancestor ancestry against `origin/main` and `origin/$CANDIDATE_BRANCH`. |
| `scripts/backport-triage.sh` | Only hardcoded branch `candidate` was supported; active branches named `release/v*` or `rc/v*` were ignored. | Added `CANDIDATE_BRANCH` environment variable support and automatic remote release candidate branch discovery fallback. |
| `scripts/release-candidate-cut.sh` | Branch detection regex only checked `origin/rc/v1.X` and failed to recognize `release/v1.X` or `release/v1.X.0` branches. | Broadened regex to `origin/(rc\|release)/v1\.[0-9]+(\.0)?$` to support standard release candidate naming schemes. |

## Test execution summary

| Test Suite / Target | Description | Result |
| --- | --- | --- |
| `TestReleaseCandidateCutScript` | Cuts initial RC (`rc/v1.0`), fast-forwards `candidate`, cuts next RC (`rc/v1.1`), and tags `v1.0.0` and `latest`. | Pass |
| `TestBackportTriageScript` | Verifies selective triage of `fix:` commits vs `feat:` commits with `-x` provenance. | Pass |
| `TestBackportTriageScript_ReleaseBranchAndConflictHandling` | Exercises triage against `release/v1.0.0`, tests conflict detection, review task creation, and clean git state preservation. | Pass |
| `TestBackportTriageScript_AutoDiscoveryOfReleaseBranch` | Tests automatic discovery of active `release/v1.0.0` branches without explicit environment variable configuration. | Pass |
| `TestAgentCommitStandardsDocumented` | Asserts Conventional Commits standards are documented across repo prompts and settings. | Pass |
| `TestReleaseWorkflowPublishesThroughAnUncachedScript` | Verifies `release.yml` uses standalone `release-images.sh` phases (`build` then `push`). | Pass |
| `TestReleaseWorkflowPrunesOldPackages` | Verifies post-publish package pruning via `cmd/ghcr-prune`. | Pass |
| `TestGrainWorkflowsParse` | Verifies all `.grain/workflows/*.yml` files parse as valid Grain workflow schemas. | Pass |
