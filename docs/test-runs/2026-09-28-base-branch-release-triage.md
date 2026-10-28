# Base Branch Release Pipeline Verification (2026-09-28)

## Incident Context

The base branch release workflow (`.grain/workflows/release.yml`) failed on `bwsalmon/grain` (branch `main`) at commit `e8ba90eae697` during release run `5ff54c4b11782cb9`.

### Investigation & Root Cause Analysis

1. **Failure Signature**: The release run `5ff54c4b11782cb9` concluded with status `errored` and detail:
   `the run was interrupted by the daemon that was running it stopping`.
2. **Timeline Analysis**:
   - Release run `02109c474f6e8801` for commit `80a673022` succeeded and published fresh image tags (`sha-80a6730`, `main`).
   - The Grain deployment subsequently self-upgraded and restarted its daemon to run the newly published build `80a6730`.
   - The in-flight release run `5ff54c4b11782cb9` for commit `e8ba90eae697` (which merged immediately after `80a673022`) was executing `Build the images` when the daemon restart interrupted the container, cancelling the run.
3. **Pipeline Verification**:
   - Verified that `scripts/release-images.sh` and `.grain/workflows/release.yml` remain completely healthy across all phases (`build`, `push`, `guest-cache`, `all`).
   - Verified local dry-run executions for `scripts/release-images.sh` across build and publish modes.
   - Verified `tests/deploy/...` workflow tests and orchestrator test suites pass.
4. **Resolution**:
   - Merging this change to `main` moves the base branch tip forward and triggers a clean, uninterrupted post-merge release run on `main`.
