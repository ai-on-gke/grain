# Test Run: GitHub Issue Correlation, PR Auto-Close & `read-issues` MCP Capability

**Date**: 2026-09-30  
**Test Plan Reference**: `docs/test-plans/2026-09-30.md` §1 ("Walkthrough: GitHub Issue Correlation, PR Auto-Close & `read-issues` MCP Capability")  
**Target Branch**: `grain/task-1234`  
**Environment**: Debian Linux x86_64, Go 1.26.6  

---

## 1. Executive Summary

This walkthrough validates the end-to-end integration of GitHub issue correlation, issue viewing capabilities within agent sandboxes, automated pull request closing directive injection, and automated issue lifecycle reconciliation upon pull request merge.

Specifically, the verification proves:
1. **GitHub Issue Seeding & Mock Transport**: Issues and conversation comments seeded in `githubsim` are properly served via GitHub REST API endpoints (`/repos/:owner/:repo/issues` and `/repos/:owner/:repo/issues/:number/comments`).
2. **Task Intake & Capability Gating**: Tasks dispatched with `-issue <number>` and `--capability read-issues` store the issue reference and grant permissions to the MCP server.
3. **Prompt Injection**: The orchestrator injects issue correlation context (`"This task is associated with GitHub issue #<number>"`) into the agent's task prompt.
4. **Live MCP Issue Tools**: When granted `read-issues`, `grain mcpserver` advertises and successfully executes:
   - `list_issues`: Lists open repository issues with author, labels, state, and URLs.
   - `read_issue`: Retrieves full issue title, state, author, body description, and threaded conversation comments with author association badges.
5. **Security & Permission Enforcement**: When dispatched without the `read-issues` capability grant, `grain mcpserver` omits the issue tools and cleanly denies invocation attempts with a clear error informing the agent that the capability was not granted.
6. **PR Auto-Close Directive Injection**: When the task completes with a pushed git branch, the orchestrator constructs the pull request description and automatically appends `Fixes #<number>` (unless an issue closing directive is already present).
7. **Reconciliation & Auto-Closure**: When the pull request is merged, `githubsim` / GitHub reconciler parses `Fixes #<number>` and automatically transitions the linked issue from `open` to `closed`.
8. **CLI & API Updates**: Task issue correlation is mutable and correctly updated via `grain update -issue` and UI client API endpoints.

All verification steps defined in `docs/test-plans/2026-09-30.md` §1 were implemented and verified with the automated test suite in `tests/e2e/github_issue_correlation_walkthrough_test.go`.

---

## 2. Test Architecture & Verification Scope

### A. End-to-End Walkthrough Verification Suite (`tests/e2e/github_issue_correlation_walkthrough_test.go`)

A dedicated test suite `TestGitHubIssueCorrelationAndAutoCloseWalkthrough` was implemented covering the entire walkthrough:

- **Upstream Git & GitHub Simulation**:
  - Initialized bare upstream repository for `example/repo` with default branch `main`.
  - Configured `githubsim.New("example", "repo", bareRepoPath, "main")`.
  - Seeded Issue `#101`:
    - **Title**: `Fix button contrast in dark mode`
    - **Body**: `Contrast on dark theme action buttons does not meet WCAG AA standards.`
    - **Author**: `@design-lead`
    - **Labels**: `ui`, `accessibility`, `bug`
    - **State**: `open`
  - Seeded Issue Comments on `#101`:
    - Comment 1 by `@alice` (`MEMBER`): `Reproduced on high-DPI displays in Chrome dark mode.`
    - Comment 2 by `@bob` (`CONTRIBUTOR`): `Need at least 4.5:1 contrast ratio on .btn-primary in dark theme.`
  - Served via `githubHostServer` supporting smart HTTP git and GitHub REST API emulation.

- **Step 1 & 2: Environment Setup & Seeding**:
  - Configured token credentials in `secrets/github/credentials.json` and `secrets/github/default.token`.

- **Step 3: Task Intake & Dispatch**:
  - Invoked `uiClient.CreateTask` with `Issue: 101` and `Capabilities: ["read-issues"]`.
  - Verified stored task metadata and prompt generation via `orchestrator.BuildPrompt`, confirming issue attribution notice.

- **Step 4: Live MCP `list_issues` Execution**:
  - Started `grain mcpserver` subprocess with `-grant read-issues`, `-github-host`, `-github-insecure-http`, and `-pr-repo example/repo`.
  - Verified tool advertisement and executed `list_issues`.
  - Output verified: `#101 [open] Fix button contrast in dark mode (by @design-lead) -- labels: accessibility, bug, ui`.

- **Step 5: Live MCP `read_issue` Execution**:
  - Executed `read_issue(issue_number=101)`.
  - Verified retrieval of full title, body description, author, and comments from `@alice` and `@bob`.

- **Step 6: Negative Capability Gating Verification**:
  - Started `grain mcpserver` subprocess without `-grant read-issues`.
  - Confirmed `list_issues` and `read_issue` are not advertised.
  - Attempted execution of both tools and verified clean RPC refusal: `mcp: unknown tool <tool>: this run does not hold the "read-issues" capability...`.

- **Step 7: Code Fix, Commit & PR Auto-Close Injection**:
  - Cloned repository, created branch `grain/task-1`, and committed `button.css` fix for button contrast.
  - Pushed branch to remote upstream `example/repo`.
  - Processed run result via `orchestrator.ProcessResult`.
  - Verified generated Pull Request body contains `Fixes #101`.

- **Step 8: PR Merge & Auto-Closure**:
  - Merged Pull Request via `PUT /repos/example/repo/pulls/9000/merge`.
  - Confirmed `githubsim` transitioned Issue `#101` from `open` to `closed`.
  - Invoked `read_issue(issue_number=101)` via MCP server and confirmed reported status is `closed`.

- **Step 9: UI & CLI Task Issue Updating**:
  - Updated task issue reference to `202` via `uiClient.UpdateTask`.
  - Confirmed database persistence and retrieval via `uiClient.GetTask`.

---

## 3. Detailed Execution Results

```
=== RUN   TestGitHubIssueCorrelationAndAutoCloseWalkthrough
=== PAUSE TestGitHubIssueCorrelationAndAutoCloseWalkthrough
=== CONT  TestGitHubIssueCorrelationAndAutoCloseWalkthrough
    github_issue_correlation_walkthrough_test.go:56: === Step 1: Initialize Bare Git Upstream and GitHub Simulator ===
    github_issue_correlation_walkthrough_test.go:80: === Step 2: Seed Open Issue #101 with Discussion Comments ===
    github_issue_correlation_walkthrough_test.go:145: === Step 3: Dispatch Task with -issue 101 and --capability read-issues ===
    github_issue_correlation_walkthrough_test.go:186: === Step 4: Live MCP Tool list_issues Execution ===
    github_issue_correlation_walkthrough_test.go:211: list_issues output:
        Open issues for example/repo (1):
          #101 [open] Fix button contrast in dark mode (by @design-lead) -- labels: accessibility, bug, ui
            https://github.example/example/repo/issues/101
    github_issue_correlation_walkthrough_test.go:222: === Step 5: Live MCP Tool read_issue(101) Execution ===
    github_issue_correlation_walkthrough_test.go:233: read_issue(101) output:
        Issue #101: Fix button contrast in dark mode
        State: open | Author: @design-lead
        URL: https://github.example/example/repo/issues/101
        Labels: accessibility, bug, ui
        
        Description:
        Contrast on dark theme action buttons does not meet WCAG AA standards.
        
        Comments (2):
        
        --- Comment 1 by @alice (MEMBER) ---
        Reproduced on high-DPI displays in Chrome dark mode.
        
        --- Comment 2 by @bob (CONTRIBUTOR) ---
        Need at least 4.5:1 contrast ratio on .btn-primary in dark theme.
    github_issue_correlation_walkthrough_test.go:256: === Step 6: Negative Test - Deny Issue Tools Without Capability Grant ===
    github_issue_correlation_walkthrough_test.go:285: === Step 7: Commit Fix, Push Branch, Verify Fixes #101 in PR Description ===
    github_issue_correlation_walkthrough_test.go:325: Created PR #9000 with body:
        fix(ui): improve button contrast in dark mode
        
        Ensure .btn-primary meets WCAG AA contrast ratio.
        
        Fixes #101
        
        ---
        Automated change for grain task 1.
    github_issue_correlation_walkthrough_test.go:343: === Step 8: Merge PR and Verify GitHub Issue Auto-Closed ===
    github_issue_correlation_walkthrough_test.go:390: === Step 9: Verify Task Issue Updates via UI API ===
--- PASS: TestGitHubIssueCorrelationAndAutoCloseWalkthrough (2.14s)
PASS
ok  	github.com/bwsalmon/grain/tests/e2e	2.160s
```

---

## 4. Regression & Full Test Suite Results

- `go test ./pkg/... ./cmd/...`: Passed (0 failures).
- `go test ./tests/e2e -run TestGitHubIssueCorrelationAndAutoCloseWalkthrough`: Passed.

---

## 5. Conclusion

The GitHub Issue Correlation, PR Auto-Close, and `read-issues` MCP Capability system is verified and operates correctly according to specification. All security boundaries, capability gating, prompt injection, MCP tool outputs, PR body decoration, and issue auto-closure behaviors have been validated end-to-end.
