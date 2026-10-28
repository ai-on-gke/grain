# Walkthrough: Plain-git hosts and forge API client refusal

This run executes §1 of `docs/test-plans/2026-10-10.md` (task 2839, filed by task 2825) covering multi-host forge API clients and plain-git host refusal handling introduced in commits `607b5db86` and `b757cafbc`.

## Summary

- **Multi-Host Forge API Client and Plain Git Host Support Verified:**
  - Tested across `gitproxy`, `mcpserver`, and `orchestrator` boundaries for repositories on plain git hosts (`Kind: model.GitHostGit`, where `APIBase()` returns `ok: false`).
  - Plain git hosts lack forge REST APIs (no pull requests, issues, or remote CI check queries).
  - All components handle plain git host targets cleanly with informative diagnostic messages and graceful fallbacks rather than unhandled errors or panics.
- **Bugs Identified and Resolved During Walkthrough:**
  1. **StoredOwner host stripping in `OpenPullRequestForTask` and `salvagePushedBranch` (`pkg/orchestrator`):**
     - `task.Target.Owner` was passed instead of `task.Target.StoredOwner()` to `src.GitOnly(owner, repo)`.
     - Because `model.RepoRef.Owner` contains only the bare organization/user without the host prefix (e.g., `"acme"` instead of `"git.example.com/acme"`), `model.RepoFromStored` assumed the default host (`github.com`), queried credentials for `github.com` instead of the plain git host, and failed to detect plain git hosts.
     - Fixed in `pkg/orchestrator/pullrequest.go` and `pkg/orchestrator/finish.go` by passing `task.Target.StoredOwner()`.
  2. **StoredOwner host stripping in `QueryMergeQueue` and `queuedPRBranch` (`pkg/daemon/mcpserver.go`):**
     - `ref.Owner` was passed instead of `ref.StoredOwner()` when calling `d.defaultBranch`.
     - `githubDefaultBranchReader` uses `model.RepoFromStored(owner, repo)` and `repoTransport(credentials, insecureHTTP, githubHost, stored)`. Stripping the host caused it to look up the default host rather than the plain git host.
     - Fixed in `pkg/daemon/mcpserver.go` by passing `ref.StoredOwner()`.
  3. **Off-default host record resolution in `repoTransport` (`pkg/daemon/mcpserver.go`):**
     - When no repository credential was selected, looking up `credentials.HostOf(host)` was guarded by `SameGitHost(host, defaultHost, credentials.DefaultHost())`. This prevented loading host records (e.g., `git.example.com.host.json`) for off-default plain git hosts when unauthenticated.
     - Fixed by querying `credentials.HostOf(host)` whenever `!credHost.Recorded`.
- **Walkthrough Scenarios Verified:**
  1. **MCP Server Tools & Readers:**
     - `PullRequestReader`: cleanly refuses with:
       `repository "git.example.com/acme/widgets" is on git.example.com, a plain git host with no pull request or CI checks API`
     - `issueReader`: cleanly refuses with:
       `repository "git.example.com/acme/widgets" is on a plain git host with no issues API: repository "git.example.com/acme/widgets" is on git.example.com, a plain git host with no forge API`
     - `githubDefaultBranchReader`: cleanly refuses with:
       `repository "git.example.com/acme/widgets" is on git.example.com, a plain git host with no forge API`
     - `PullRequestTools` and `issueTools`: degrade gracefully to registered stubs with clear diagnostics rather than crashing or returning unknown-tool errors.
  2. **Merge Queue Diagnostics:**
     - `QueryMergeQueue`: detects that the repository is on a plain git host via default branch probe failure and returns `Paused: true` with:
       `PauseInfo: "Merge queue is unavailable for git.example.com/acme/widgets: repository is on git.example.com, a plain git host"`
  3. **Orchestrator Mid-Run PR Creation Refusal:**
     - `OpenPullRequestForTask`: rejects pull request creation cleanly with:
       `orchestrator: repository git.example.com/acme/widgets is on git.example.com, a plain git host with no forge API or pull requests`
  4. **Task Completion on Plain Git Host:**
     - `salvagePushedBranch`: detects pushed branch on remote, discovers the plain git host via `src.GitOnly`, marks the task completed (`CompletedAt` set, `RetryRequestedAt` cleared) without creating a pull request, and posts the expected comment on the task card:
       `"Branch grain/task-t1 pushed to git.example.com/acme/widgets; no pull request opened because git.example.com is a plain git host."`

## Environment

- Host: Linux (Debian 12), kernel 6.6.137+
- Go: go version go1.24.0 linux/amd64
- Working directory: `/home/debian/work`
- Branch: `grain/task-2839` (base `origin/candidate`)
- Target repository: `bwsalmon/grain`

## Test Execution Details

### 1. MCP Server Plain Git Host Readers & Tools
- **Test file:** `pkg/daemon/mcpserver_plain_git_test.go`
- **Tests executed:**
  - `TestPullRequestReaderPlainGitHostRefusal`: Confirms `PullRequestReader` fails with descriptive refusal when target repository is configured on a plain git host (`model.GitHostGit`).
  - `TestIssueReaderPlainGitHostRefusal`: Confirms `issueReader` fails with descriptive refusal indicating lack of issues API on plain git host.
  - `TestGithubDefaultBranchReaderPlainGitHostRefusal`: Confirms `githubDefaultBranchReader` fails with descriptive refusal indicating lack of forge API.
  - `TestCredentialTokenSourceGitOnlyPlainGitHost`: Confirms `credentialTokenSource.GitOnly` returns exact refusal explanation.
  - `TestPullRequestToolsPlainGitHost`: Confirms MCP pull request tool handlers return structured error responses cleanly.
  - `TestIssueToolsPlainGitHost`: Confirms MCP issue tool handlers return structured error responses cleanly.
- **Result:** PASS (0.017s)

### 2. Merge Queue Diagnostics
- **Test file:** `pkg/daemon/mcpserver_mergequeue_test.go`
- **Test executed:**
  - `TestDaemonMergeQueuePlainGitHost`: Configured merge queue daemon with plain git repository `git.example.com/acme/widgets`. Verified `QueryMergeQueue` reports `Paused: true` and `PauseInfo: "Merge queue is unavailable for git.example.com/acme/widgets: repository is on git.example.com, a plain git host"`.
- **Result:** PASS (0.016s)

### 3. Orchestrator PR Refusal & Salvage Completion
- **Test file:** `pkg/orchestrator/pullrequest_test.go`
- **Tests executed:**
  - `TestOpenPullRequestForTaskRefusesPlainGitHost`: Verified that `OpenPullRequestForTask` invokes `src.GitOnly` with `StoredOwner()` (`"git.example.com/acme"`) and refuses PR creation with an error containing `"plain git host"`.
  - `TestSalvagePushedBranchCompletesTaskOnPlainGitHostWithoutPullRequest`: Verified that `salvagePushedBranch` completes a pushed task on a plain git host without attempting to open a PR, and leaves the exact comment:
    `"Branch grain/task-t1 pushed to git.example.com/acme/widgets; no pull request opened because git.example.com is a plain git host."`
- **Result:** PASS (0.179s)

### 4. Regression & Security Verification
- Ran full test suite across touched packages:
  - `pkg/daemon`: PASS (0.605s)
  - `pkg/orchestrator`: PASS (75.269s)
  - `pkg/gitproxy`: PASS (9.634s)
  - `cmd/grain`: PASS (37.828s)
  - `tests/security/...`: PASS (210.729s)
