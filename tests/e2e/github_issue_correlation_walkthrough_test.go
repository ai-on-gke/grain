// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestGitHubIssueCorrelationAndAutoCloseWalkthrough executes the comprehensive end-to-end
// verification walkthrough from docs/test-plans/2026-09-30.md §1 (GitHub Issue Correlation,
// PR Auto-Close & read-issues MCP Capability).
//
// Walkthrough Steps:
//  1. Launch test environment with mock GitHub simulator (githubsim) and real git backend.
//  2. Seed mock GitHub repository example/repo with open issue #101 ("Fix button contrast in dark mode")
//     along with discussion comments from team members.
//  3. Dispatch a new task targeting example/repo with -issue 101 and --capability read-issues.
//  4. Inside agent sandbox MCP session, invoke list_issues and verify issue #101 is listed with correct metadata.
//  5. Invoke read_issue(issue_number=101) and verify full issue description and discussion comments are retrieved.
//  6. Verify capability gating: task dispatched without read-issues fails cleanly when calling issue tools.
//  7. Make a commit resolving the issue, push the branch, and verify the orchestrator PR body injects "Fixes #101".
//  8. Merge the pull request and verify GitHub simulator / reconciler transitions issue #101 to closed.
//  9. Verify CLI / UI task creation and updating with -issue flags.
func TestGitHubIssueCorrelationAndAutoCloseWalkthrough(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not installed")
	}
	bin := buildGrainBinary(t)

	const (
		owner       = "example"
		repoName    = "repo"
		targetRepo  = owner + "/" + repoName
		issueNumber = 101
		botToken    = "ghp_mock_github_issue_test_token_12345"
	)

	// -------------------------------------------------------------------------
	// Step 1: Initialize Bare Git Upstream and GitHub Simulator Backend
	// -------------------------------------------------------------------------
	t.Log("=== Step 1: Initialize Bare Git Upstream and GitHub Simulator ===")
	upstreamRoot := t.TempDir()
	bareRepoPath := filepath.Join(upstreamRoot, owner, repoName+".git")
	initBareRepo(t, bareRepoPath, "main")

	// Create initial commit on main branch
	seedDir := t.TempDir()
	runGitCmdInDir(t, seedDir, "clone", bareRepoPath, ".")
	runGitCmdInDir(t, seedDir, "config", "user.name", "SeedUser")
	runGitCmdInDir(t, seedDir, "config", "user.email", "seed@example.com")
	initFile := filepath.Join(seedDir, "README.md")
	if err := os.WriteFile(initFile, []byte("# Example Repository\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmdInDir(t, seedDir, "add", "README.md")
	runGitCmdInDir(t, seedDir, "commit", "-m", "chore: initial commit")
	runGitCmdInDir(t, seedDir, "branch", "-M", "main")
	runGitCmdInDir(t, seedDir, "push", "origin", "main")

	sim := githubsim.New(owner, repoName, bareRepoPath, "main")

	// -------------------------------------------------------------------------
	// Step 2: Seed Issue #101 and Discussion Comments
	// -------------------------------------------------------------------------
	t.Log("=== Step 2: Seed Open Issue #101 with Discussion Comments ===")
	issueTitle := "Fix button contrast in dark mode"
	issueBody := "Contrast on dark theme action buttons does not meet WCAG AA standards."
	sim.Issues[issueNumber] = &githubsim.Issue{
		Title:  issueTitle,
		Body:   issueBody,
		Author: "design-lead",
		Labels: map[string]struct{}{
			"ui":            {},
			"accessibility": {},
			"bug":           {},
		},
		State: "open",
	}

	sim.Comments[issueNumber] = []github.Comment{
		{
			ID:                1,
			User:              "alice",
			Body:              "Reproduced on high-DPI displays in Chrome dark mode.",
			AuthorAssociation: "MEMBER",
		},
		{
			ID:                2,
			User:              "bob",
			Body:              "Need at least 4.5:1 contrast ratio on .btn-primary in dark theme.",
			AuthorAssociation: "CONTRIBUTOR",
		},
	}

	// Serve GitHub HTTP & Git smart HTTP backend
	synced := &syncedSim{sim: sim}
	githubHost := githubHostServer(t, synced, upstreamRoot)

	// Setup controller data directory with GitHub credentials ladder
	dataDir := t.TempDir()
	githubSecretsDir := filepath.Join(dataDir, "secrets", "github")
	if err := os.MkdirAll(githubSecretsDir, 0o700); err != nil {
		t.Fatalf("creating secrets dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(githubSecretsDir, "credentials.json"), []byte(`{"*": "default"}`), 0o600); err != nil {
		t.Fatalf("writing credentials.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(githubSecretsDir, "default.token"), []byte(botToken+"\n"), 0o600); err != nil {
		t.Fatalf("writing default.token: %v", err)
	}

	// Initialize Store and UI Client
	storeDir := t.TempDir()
	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		creds, err := gitproxy.LoadCredentialSet(githubSecretsDir)
		if err != nil {
			t.Fatalf("loading credentials: %v", err)
		}

		uiConfig := ui.Config{
			Actor:        ui.DefaultActor("operator@example.com"),
			Capabilities: ui.OfferedCapabilities(),
			Credentials:  creds,
		}
		uiClient := ui.NewClient(uiConfig, store)

		// ---------------------------------------------------------------------
		// Step 3: Dispatch Task with -issue 101 and --capability read-issues
		// ---------------------------------------------------------------------
		t.Log("=== Step 3: Dispatch Task with -issue 101 and --capability read-issues ===")
		caps := []string{"read-issues"}
		taskReq := ui.CreateTaskRequest{
			Title:        "Fix button contrast in dark mode",
			Description:  "Resolve contrast ratio issues on dark mode buttons",
			Repo:         targetRepo,
			Issue:        issueNumber,
			Capabilities: &caps,
			Approved:     true,
		}
		createdTask, err := uiClient.CreateTask(ctx, taskReq)
		if err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		if createdTask.Issue != issueNumber {
			t.Fatalf("createdTask.Issue = %d, want %d", createdTask.Issue, issueNumber)
		}
		if len(createdTask.Capabilities) != 1 || createdTask.Capabilities[0] != "read-issues" {
			t.Fatalf("createdTask.Capabilities = %v, want ['read-issues']", createdTask.Capabilities)
		}

		// Verify Prompt Injection for Issue Correlation
		tasks, err := store.ListTasks(ctx)
		if err != nil || len(tasks) == 0 {
			t.Fatalf("loading task from store: %v", err)
		}
		var dbTask model.Task
		for _, tk := range tasks {
			if tk.ID == createdTask.ID {
				dbTask = tk
				break
			}
		}
		prompt := orchestrator.BuildPrompt(dbTask, "repo", "/tmp/sandbox", true, time.Hour, orchestrator.History{}, nil)
		if !strings.Contains(prompt, "GitHub issue #101") {
			t.Errorf("prompt missing issue correlation notice: %s", prompt)
		}

		// ---------------------------------------------------------------------
		// Step 4: Live MCP Invocation: list_issues with read-issues grant
		// ---------------------------------------------------------------------
		t.Log("=== Step 4: Live MCP Tool list_issues Execution ===")
		sandboxDir := t.TempDir()
		mcpProc := startMCPServer(t, bin,
			"-sandbox-root", sandboxDir,
			"-data-dir", dataDir,
			"-github-host", githubHost,
			"-github-insecure-http",
			"-pr-repo", targetRepo,
			"-grant", "read-issues",
		)

		// Verify advertised tools
		tools := toolNames(t, mcpProc)
		if !tools["list_issues"] || !tools["read_issue"] {
			t.Fatalf("mcpserver with read-issues grant missing issue tools. advertised: %v", tools)
		}

		listRes, err := mcpProc.CallTool(ctx, "list_issues", map[string]any{})
		if err != nil {
			t.Fatalf("CallTool(list_issues) returned rpc error: %v", err)
		}
		if listRes.IsError {
			t.Fatalf("CallTool(list_issues) returned tool error: %s", listRes.Text())
		}
		listText := listRes.Text()
		t.Logf("list_issues output:\n%s", listText)
		if !strings.Contains(listText, "#101") || !strings.Contains(listText, "Fix button contrast in dark mode") {
			t.Errorf("list_issues output missing issue #101 details: %s", listText)
		}
		if !strings.Contains(listText, "open") {
			t.Errorf("list_issues output missing open state: %s", listText)
		}

		// ---------------------------------------------------------------------
		// Step 5: Live MCP Invocation: read_issue(issue_number=101)
		// ---------------------------------------------------------------------
		t.Log("=== Step 5: Live MCP Tool read_issue(101) Execution ===")
		readRes, err := mcpProc.CallTool(ctx, "read_issue", map[string]any{
			"issue_number": issueNumber,
		})
		if err != nil {
			t.Fatalf("CallTool(read_issue) returned rpc error: %v", err)
		}
		if readRes.IsError {
			t.Fatalf("CallTool(read_issue) returned tool error: %s", readRes.Text())
		}
		readText := readRes.Text()
		t.Logf("read_issue(101) output:\n%s", readText)

		if !strings.Contains(readText, "#101: Fix button contrast in dark mode") {
			t.Errorf("read_issue output missing title header: %s", readText)
		}
		if !strings.Contains(readText, issueBody) {
			t.Errorf("read_issue output missing issue body text: %s", readText)
		}
		if !strings.Contains(readText, "design-lead") {
			t.Errorf("read_issue output missing author attribution: %s", readText)
		}
		if !strings.Contains(readText, "alice") || !strings.Contains(readText, "Reproduced on high-DPI displays") {
			t.Errorf("read_issue output missing alice comment: %s", readText)
		}
		if !strings.Contains(readText, "bob") || !strings.Contains(readText, "Need at least 4.5:1 contrast ratio") {
			t.Errorf("read_issue output missing bob comment: %s", readText)
		}

		mcpProc.Close()

		// ---------------------------------------------------------------------
		// Step 6: Negative Test: Refusal Without read-issues Capability
		// ---------------------------------------------------------------------
		t.Log("=== Step 6: Negative Test - Deny Issue Tools Without Capability Grant ===")
		mcpNoGrantProc := startMCPServer(t, bin,
			"-sandbox-root", t.TempDir(),
			"-data-dir", dataDir,
			"-github-host", githubHost,
			"-github-insecure-http",
			"-pr-repo", targetRepo,
		)

		noGrantTools := toolNames(t, mcpNoGrantProc)
		if noGrantTools["list_issues"] || noGrantTools["read_issue"] {
			t.Errorf("mcpserver without grant should not advertise issue tools: %v", noGrantTools)
		}

		_, err = mcpNoGrantProc.CallTool(ctx, "list_issues", map[string]any{})
		if err == nil || !strings.Contains(err.Error(), `does not hold the "read-issues" capability`) {
			t.Errorf("expected capability denial RPC error for list_issues, got: %v", err)
		}

		_, err = mcpNoGrantProc.CallTool(ctx, "read_issue", map[string]any{"issue_number": issueNumber})
		if err == nil || !strings.Contains(err.Error(), `does not hold the "read-issues" capability`) {
			t.Errorf("expected capability denial RPC error for read_issue, got: %v", err)
		}

		mcpNoGrantProc.Close()

		// ---------------------------------------------------------------------
		// Step 7: Commit Fix, Push Branch & Verify "Fixes #101" Injection in PR
		// ---------------------------------------------------------------------
		t.Log("=== Step 7: Commit Fix, Push Branch, Verify Fixes #101 in PR Description ===")
		clientCloneDir := t.TempDir()
		runGitCmdInDir(t, clientCloneDir, "clone", bareRepoPath, ".")
		runGitCmdInDir(t, clientCloneDir, "config", "user.name", "TestAgent")
		runGitCmdInDir(t, clientCloneDir, "config", "user.email", "agent@example.com")
		branchName := "grain/task-" + createdTask.ID
		runGitCmdInDir(t, clientCloneDir, "checkout", "-b", branchName)

		cssFile := filepath.Join(clientCloneDir, "button.css")
		cssContent := `.btn-primary {
    background-color: #0056b3;
    color: #ffffff; /* Contrast ratio 5.1:1 */
}
`
		if err := os.WriteFile(cssFile, []byte(cssContent), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitCmdInDir(t, clientCloneDir, "add", "button.css")
		runGitCmdInDir(t, clientCloneDir, "commit", "-m", "fix(ui): improve button contrast in dark mode\n\nEnsure .btn-primary meets WCAG AA contrast ratio.")
		runGitCmdInDir(t, clientCloneDir, "push", "origin", branchName)

		// Create PR via GitHub client
		transport := github.NewRealTransport(githubHost)
		transport.UseTLS = false
		tokenVal := botToken
		ghClient := github.NewClient(transport, github.StaticToken{Token: &tokenVal})

		// Process orchestrator result
		res := &agent.Result{}
		now := baseTime.Add(30 * time.Minute)
		if err := orchestrator.ProcessResult(ctx, store, ghClient, dbTask, res, "run-1", now); err != nil {
			t.Fatalf("ProcessResult: %v", err)
		}

		if len(sim.PullRequests) == 0 {
			t.Fatalf("expected a pull request to be created, but Sim has none")
		}

		createdPR := sim.PullRequests[len(sim.PullRequests)-1]
		prNumber := createdPR.Number
		t.Logf("Created PR #%d with body:\n%s", prNumber, createdPR.Body)

		if !strings.Contains(createdPR.Body, fmt.Sprintf("Fixes #%d", issueNumber)) {
			t.Errorf("PR body missing 'Fixes #%d' directive:\n%s", issueNumber, createdPR.Body)
		}

		// Also verify via ghClient GetPullRequest
		prDetail, err := ghClient.GetPullRequest(owner, repoName, prNumber)
		if err != nil {
			t.Fatalf("GetPullRequest: %v", err)
		}
		if !strings.Contains(prDetail.Body, fmt.Sprintf("Fixes #%d", issueNumber)) {
			t.Errorf("GitHub PR body missing 'Fixes #%d':\n%s", issueNumber, prDetail.Body)
		}

		// ---------------------------------------------------------------------
		// Step 8: Merge Pull Request & Verify Issue #101 Auto-Closed
		// -------------------------------------------------------------------------
		t.Log("=== Step 8: Merge PR and Verify GitHub Issue Auto-Closed ===")
		// Ensure issue is currently open
		if sim.Issues[issueNumber].State != "open" {
			t.Fatalf("issue #%d should be open before merge, got %s", issueNumber, sim.Issues[issueNumber].State)
		}

		// Merge the PR through GitHub API
		mergeURL := fmt.Sprintf("/repos/%s/%s/pulls/%d/merge", owner, repoName, prNumber)
		mergeResp, err := synced.Request(http.MethodPut, mergeURL, map[string]string{
			"Authorization": "token " + botToken,
			"Content-Type":  "application/json",
		}, []byte(`{"commit_title":"Merge PR #1"}`))
		if err != nil {
			t.Fatalf("merging PR failed: %v", err)
		}
		if mergeResp.Status != http.StatusOK {
			t.Fatalf("merging PR returned status %d: %s", mergeResp.Status, string(mergeResp.Body))
		}

		// Verify simulator transitioned issue #101 to closed
		if sim.Issues[issueNumber].State != "closed" {
			t.Errorf("expected issue #%d to transition to closed after PR merge, got %s", issueNumber, sim.Issues[issueNumber].State)
		}

		// Verify read_issue reflects closed state
		mcpPostMergeProc := startMCPServer(t, bin,
			"-sandbox-root", t.TempDir(),
			"-data-dir", dataDir,
			"-github-host", githubHost,
			"-github-insecure-http",
			"-pr-repo", targetRepo,
			"-grant", "read-issues",
		)
		closedReadRes, err := mcpPostMergeProc.CallTool(ctx, "read_issue", map[string]any{
			"issue_number": issueNumber,
		})
		if err != nil {
			t.Fatalf("CallTool(read_issue) after merge failed: %v", err)
		}
		if !strings.Contains(closedReadRes.Text(), "closed") {
			t.Errorf("read_issue after merge expected state closed, got: %s", closedReadRes.Text())
		}
		mcpPostMergeProc.Close()

		// ---------------------------------------------------------------------
		// Step 9: Verify Task Updates with -issue
		// ---------------------------------------------------------------------
		t.Log("=== Step 9: Verify Task Issue Updates via UI API ===")
		updatedIssue := 202
		updatedTask, err := uiClient.UpdateTask(ctx, createdTask.ID, ui.UpdateTaskRequest{
			Issue: &updatedIssue,
		})
		if err != nil {
			t.Fatalf("UpdateTask: %v", err)
		}
		if updatedTask.Issue != 202 {
			t.Errorf("updatedTask.Issue = %d, want 202", updatedTask.Issue)
		}

		fetchedTask, err := uiClient.GetTask(ctx, createdTask.ID)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		if fetchedTask.Issue != 202 {
			t.Errorf("fetchedTask.Issue = %d, want 202", fetchedTask.Issue)
		}
	})
}

func runGitCmdInDir(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmdArgs := []string{"-C", dir}
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.Command("git", cmdArgs...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=TestAgent",
		"GIT_AUTHOR_EMAIL=agent@example.com",
		"GIT_COMMITTER_NAME=TestAgent",
		"GIT_COMMITTER_EMAIL=agent@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\nOutput: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}
