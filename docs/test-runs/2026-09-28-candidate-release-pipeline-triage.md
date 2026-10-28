# Base Branch Release Pipeline Verification on Candidate (2026-09-28)

## Incident Context

The base branch release workflow (`.grain/workflows/release.yml`) failed on `bwsalmon/grain` (branch `candidate`) at commit `e53764a6a98a` during release run `366cdb8fa3908244`.

### Investigation & Root Cause Analysis

1. **Failure Signature**: The release run `366cdb8fa3908244` concluded with status `errored` (interrupted) and detail:
   `the run was interrupted by the daemon that was running it stopping`.
2. **Timeline Analysis**:
   - Pull request #2251 merged into `candidate` at commit `e53764a6a98a`, triggering standard test checks (`4b8a0d1891dfa9ef`) and the release workflow (`366cdb8fa3908244`).
   - Standard CI tests on `candidate` at `e53764a6a98a` all completed successfully (frontend, go, installer, lint, package cache, terraform, ui e2e in run `4b8a0d1891dfa9ef`).
   - In parallel, the Grain deployment was self-upgraded to build `80a673022` (following the merge and successful release run `02109c474f6e8801` on `main`).
   - The daemon restart during the upgrade interrupted all active sandbox runs that were executing at that instant, including release run `366cdb8fa3908244` during its `Build the images` step.
3. **Pipeline & Codebase Verification**:
   - `scripts/release-images.sh` and `.grain/workflows/release.yml` were verified across `build`, `push`, `guest-cache`, and `all` execution phases via dry-run testing.
   - Verified that `tests/deploy/...` and the Go test suites build and pass cleanly.
4. **Resolution**:
   - Landing this branch on `candidate` moves the branch tip forward and triggers a clean post-merge release workflow run to publish candidate release images.
