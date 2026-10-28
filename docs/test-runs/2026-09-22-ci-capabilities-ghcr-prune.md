# Test run: CI capability credential mounting and ghcr-prune execution

The pass `docs/test-plans/2026-09-22.md` §6 specifies: an end-to-end walkthrough and test of CI capability credential mounting, permission enforcement, and tool execution in a Grain CI sandbox environment with declared capabilities (`gcp-key`, `github-sandbox`) and `cmd/ghcr-prune` invocation.

## Result

Pass after scoped repository and token resolution corrections in `cmd/ghcr-prune` and permission hardening in `pkg/orchestrator`. All credential placement assertions, permissions checks (0600 mode), target repository resolutions, mounted authentication flows, and capability exclusion guarantees passed cleanly.

Commit `grain/task-323` covers the fixes and regression test suites.

## Environment and setup

- Platform: Debian GNU/Linux 12 (bookworm), Linux x86_64 sandbox.
- Go runtime: Go 1.26.
- Sandboxing & execution model: Kontur VM / Grain CI sandbox execution with capability placements applied via `ApplyCapabilityPlacements` and `PlaceFileOverSSH`.
- Declared capabilities under test:
  - `gcp-key`: Provisions a temporary GCP service account key for the job, placed at `/home/debian/.gcp-service-account.json`.
  - `github-sandbox`: Provisions an installation access token and repository metadata, placed at `/home/debian/.github-sandbox-token` and `/home/debian/.github-sandbox-repo`.

## Walkthrough steps and observations

### 1. Job definition with declared capabilities

A Grain CI job request was configured with declared capabilities:
```yaml
capabilities:
  - gcp-key
  - github-sandbox
```

In the test runner adapter:
```go
job, err := adapter.Acquire(ctx, ci.SandboxRequest{
    Name:         "ci-walkthrough",
    Repo:         "bwsalmon/grain",
    Ref:          "grain/task-323",
    Capabilities: []string{"gcp-key", "github-sandbox"},
})
```

Upon acquisition, the capability providers materialized their leases and placements into the sandbox workspace.

### 2. Standard credential placement and permission assertions

All credential files were verified at standard paths with non-empty content and strict `0600` permissions:

| Path | Capability | Expected Mode | Verified Content | Status |
| --- | --- | --- | --- | --- |
| `/home/debian/.gcp-service-account.json` | `gcp-key` | `0600` | JSON service account credentials | Pass |
| `/home/debian/.github-sandbox-token` | `github-sandbox` | `0600` | Minted GitHub App installation token | Pass |
| `/home/debian/.github-sandbox-repo` | `github-sandbox` | `0600` | Target repository name (`bwsalmon/grain`) | Pass |

Assertions confirmed:
- Files exist at the root paths without path traversal or nested directory leakages.
- Permissions are strictly `0600` (`-rw-------`), preventing world or group read access.
- File contents are populated and valid for downstream SDKs and tools.

### 3. `cmd/ghcr-prune` execution with `GRAIN_CI_REPO` and mounted credentials

`cmd/ghcr-prune` was executed with `GRAIN_CI_REPO=bwsalmon/grain` without `GITHUB_REPOSITORY` or `GITHUB_TOKEN` set.

Execution verified:
- Repository owner and name are correctly parsed from `GRAIN_CI_REPO` (owner: `bwsalmon`, repo: `grain`).
- When `GRAIN_CI_REPO` is unset, repository information is cleanly read from `/home/debian/.github-sandbox-repo`.
- Authentication token is resolved from `/home/debian/.github-sandbox-token` (and fallback to `/home/debian/.package-publish-token`) when `GITHUB_TOKEN` is unset in the environment.
- Proper error reporting when neither environment variables nor mounted credentials are present.

### 4. Verification of jobs omitting declared capabilities

An ordinary Grain CI job was acquired with `Capabilities: nil`.

Assertions confirmed:
- `/home/debian/.gcp-service-account.json` was NOT created.
- `/home/debian/.github-sandbox-token` was NOT created.
- `/home/debian/.github-sandbox-repo` was NOT created.
- The sandbox filesystem contained zero credential artifacts from unselected capabilities.

### 5. Cleanup and lease revocation

Releasing the job (`job.Release(ctx)`) verified:
- `gcp-key` lease was revoked and the service account key deleted.
- `github-sandbox` lease was revoked.
- Ephemeral read scopes and sandbox allocations were cleaned up immediately upon completion.

## Bugs found and fixed

1. **`cmd/ghcr-prune` failed to resolve repositories from `GRAIN_CI_REPO` or mounted files.**
   - *Deterministic reproduction*: Run `ghcr-prune` in a Grain CI sandbox where `GRAIN_CI_REPO=bwsalmon/grain` is provided instead of `GITHUB_REPOSITORY`. Before the fix, `run()` returned `error: -owner and -repo are required when GITHUB_REPOSITORY is not set`.
   - *Fix*: `cmd/ghcr-prune/main.go` now inspects `GRAIN_CI_REPO`, `GITHUB_REPOSITORY`, and `/home/debian/.github-sandbox-repo`.
   - *Regression coverage*: `TestRunResolvesRepoAndTokenFromEnvironmentAndPlacements` in `cmd/ghcr-prune/main_test.go`.

2. **`cmd/ghcr-prune` failed to authenticate using mounted capability credentials.**
   - *Deterministic reproduction*: Run `ghcr-prune` in a sandbox with `github-sandbox` capability without `GITHUB_TOKEN` env var. Before the fix, `run()` returned `error: GITHUB_TOKEN is empty: listing package versions needs read:packages...`.
   - *Fix*: `cmd/ghcr-prune/main.go` now falls back to reading `/home/debian/.github-sandbox-token` or `/home/debian/.package-publish-token`.
   - *Regression coverage*: `TestRunResolvesRepoAndTokenFromEnvironmentAndPlacements` in `cmd/ghcr-prune/main_test.go`.

3. **Placement mode hardening in local sandbox execution.**
   - *Fix*: `pkg/orchestrator/run.go` `applyPlacements` now explicitly executes `os.Chmod(full, os.FileMode(mode))` following `os.WriteFile`, ensuring permissions are strictly enforced regardless of host umask or pre-existing files.
