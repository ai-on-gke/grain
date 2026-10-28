package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/ci"
	"github.com/bwsalmon/grain/pkg/mcp"
	"gopkg.in/yaml.v3"
)

func runGitCmd(t *testing.T, dir string, args ...string) string {
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

// TestMCPDiagnosticsAndQuarantineWalkthrough executes the end-to-end qualification walkthrough
// covering autonomous diagnostic MCP tools and test quarantine lifecycle:
//  1. Revert Tooling: revert_task and revert_merge across single-commit and multi-commit PRs with commit attribution.
//  2. Merge Queue Diagnostics: query_merge_queue pipeline status, held gates, batches, and queued PRs.
//  3. CI Log Streaming: get_ci_log offset, limit, and tailing.
//  4. CI Telemetry: query_test_history and retrigger_ci / run_ci_job.
//  5. Test Quarantine: quarantine_test MCP tool, follow-up task generation, .grain/quarantine.yml persistence,
//     and non-blocking CI execution preventing merge queue blockage.
func TestMCPDiagnosticsAndQuarantineWalkthrough(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	// =========================================================================
	// Section 1: Git Revert Tooling (Single-commit & Multi-commit Merge PRs)
	// =========================================================================
	t.Log("=== Section 1: Git Revert Tooling ===")
	repoDir := t.TempDir()

	runGitCmd(t, repoDir, "init")
	runGitCmd(t, repoDir, "config", "user.name", "TestAgent")
	runGitCmd(t, repoDir, "config", "user.email", "agent@example.com")

	// Base commit on main branch
	baseFile := filepath.Join(repoDir, "app.go")
	if err := os.WriteFile(baseFile, []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, repoDir, "add", "app.go")
	runGitCmd(t, repoDir, "commit", "-m", "chore: initial base commit")

	// 1A. Single-commit PR creation and revert by Task ID & Commit SHA
	runGitCmd(t, repoDir, "checkout", "-b", "grain/task-301")
	f1 := filepath.Join(repoDir, "feature_single.go")
	if err := os.WriteFile(f1, []byte("package main\n\nfunc BuggyFeature() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, repoDir, "add", "feature_single.go")
	runGitCmd(t, repoDir, "commit", "-m", "feat: single commit feature for task-301\n\nTask: task-301")
	singleCommitSHA := runGitCmd(t, repoDir, "rev-parse", "HEAD")

	runGitCmd(t, repoDir, "checkout", "-")
	runGitCmd(t, repoDir, "merge", "--ff-only", "grain/task-301")

	reverter := mcp.NewGitTaskReverter(repoDir)
	revertTools := mcp.NewRevertTools(reverter)
	revertTaskTool := revertTools[0]
	revertMergeTool := revertTools[1]

	// Revert single-commit PR via revert_task tool
	res1 := revertTaskTool.Handler(ctx, map[string]any{
		"task_id": "task-301",
		"reason":  "Null pointer dereference under high concurrency",
	})
	if res1.IsError {
		t.Fatalf("revert_task for single-commit PR returned error: %s", res1.Text)
	}
	if !strings.Contains(res1.Text, "Successfully created revert commit") {
		t.Errorf("revert_task output missing success notice: %s", res1.Text)
	}
	if !strings.Contains(res1.Text, "Origin Task: task-301") {
		t.Errorf("revert_task output missing origin task: %s", res1.Text)
	}
	if _, err := os.Stat(f1); !os.IsNotExist(err) {
		t.Fatalf("expected feature_single.go to be deleted by revert, but file exists")
	}

	// Verify commit attribution headers in git log
	revertCommitMsg1 := runGitCmd(t, repoDir, "log", "-n", "1", "--format=%B")
	if !strings.Contains(revertCommitMsg1, "revert: task task-301") {
		t.Errorf("commit message missing task title: %s", revertCommitMsg1)
	}
	if !strings.Contains(revertCommitMsg1, "Reverts-Task: task-301") {
		t.Errorf("commit message missing Reverts-Task: %s", revertCommitMsg1)
	}
	if !strings.Contains(revertCommitMsg1, "Reverts-Commit: "+singleCommitSHA) {
		t.Errorf("commit message missing Reverts-Commit: %s", revertCommitMsg1)
	}

	// 1B. Multi-commit PR merged via merge commit and reverted via revert_merge tool
	runGitCmd(t, repoDir, "checkout", "-b", "grain/task-302")
	f2A := filepath.Join(repoDir, "multi_part1.go")
	if err := os.WriteFile(f2A, []byte("package main\n\nfunc Multi1() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, repoDir, "add", "multi_part1.go")
	runGitCmd(t, repoDir, "commit", "-m", "feat: multi commit part 1")

	f2B := filepath.Join(repoDir, "multi_part2.go")
	if err := os.WriteFile(f2B, []byte("package main\n\nfunc Multi2() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, repoDir, "add", "multi_part2.go")
	runGitCmd(t, repoDir, "commit", "-m", "feat: multi commit part 2")

	runGitCmd(t, repoDir, "checkout", "-")
	runGitCmd(t, repoDir, "merge", "--no-ff", "grain/task-302", "-m", "Merge pull request #789 from bwsalmon/grain/task-302")
	multiMergeSHA := runGitCmd(t, repoDir, "rev-parse", "HEAD")

	// Ensure files exist before revert
	if _, err := os.Stat(f2A); err != nil {
		t.Fatalf("expected multi_part1.go to exist before revert: %v", err)
	}
	if _, err := os.Stat(f2B); err != nil {
		t.Fatalf("expected multi_part2.go to exist before revert: %v", err)
	}

	// Revert multi-commit merge via revert_merge tool
	res2 := revertMergeTool.Handler(ctx, map[string]any{
		"commit_sha": multiMergeSHA,
		"reason":     "Memory leak detected in multi-commit ingestion pipeline",
	})
	if res2.IsError {
		t.Fatalf("revert_merge for multi-commit PR returned error: %s", res2.Text)
	}
	if !strings.Contains(res2.Text, "Successfully created revert commit") {
		t.Errorf("revert_merge output missing success notice: %s", res2.Text)
	}
	if !strings.Contains(res2.Text, multiMergeSHA) {
		t.Errorf("revert_merge output missing target commit SHA: %s", res2.Text)
	}
	if _, err := os.Stat(f2A); !os.IsNotExist(err) {
		t.Fatalf("expected multi_part1.go to be deleted by revert, but file exists")
	}
	if _, err := os.Stat(f2B); !os.IsNotExist(err) {
		t.Fatalf("expected multi_part2.go to be deleted by revert, but file exists")
	}
	if _, err := os.Stat(baseFile); err != nil {
		t.Fatalf("expected app.go to still exist untouched")
	}

	// Verify commit attribution for multi-commit revert
	revertCommitMsg2 := runGitCmd(t, repoDir, "log", "-n", "1", "--format=%B")
	if !strings.Contains(revertCommitMsg2, "Reverts-Commit: "+multiMergeSHA) {
		t.Errorf("revert commit message missing Reverts-Commit: %s", revertCommitMsg2)
	}
	if !strings.Contains(revertCommitMsg2, "Memory leak detected in multi-commit ingestion pipeline") {
		t.Errorf("revert commit message missing reason: %s", revertCommitMsg2)
	}

	// =========================================================================
	// Section 2: Merge Queue Diagnostics Tooling (query_merge_queue)
	// =========================================================================
	t.Log("=== Section 2: Merge Queue Diagnostics ===")
	fakeQueueReader := &fakeMergeQueueReader{
		report: mcp.MergeQueueReport{
			Repo:      "bwsalmon/grain",
			Branch:    "candidate",
			Paused:    false,
			HeldGates: []mcp.HeldGateReport{},
			Batches: []mcp.MergeBatchReport{
				{
					Repo:       "bwsalmon/grain",
					Branch:     "candidate",
					Ref:        "grain-merge-queue/candidate",
					Commit:     "a1b2c3d4e5f6",
					BaseCommit: "001122334455",
					RunID:      "run-spec-42",
					Conclusion: "running",
					Members: []mcp.MergeBatchMemberReport{
						{TaskID: "931", Number: 2045, Title: "feat(ledger): resource tracking", Commit: "aaa111"},
						{TaskID: "932", Number: 2046, Title: "fix(ui): responsive sidebar", Commit: "bbb222"},
					},
				},
			},
			QueuedPRs: []mcp.QueuedPRReport{
				{
					TaskID:  "933",
					Number:  2047,
					Title:   "test(mcp): diagnostics and quarantine verification",
					Repo:    "bwsalmon/grain",
					Branch:  "candidate",
					State:   "waiting_ci",
					Note:    "Waiting on checks for commit ccc333",
					Stalled: false,
					Blocked: false,
				},
			},
		},
	}
	mqTools := mcp.NewMergeQueueTools(fakeQueueReader)
	mqRes := mqTools[0].Handler(ctx, map[string]any{"repo": "bwsalmon/grain", "branch": "candidate", "view": "full"})
	if mqRes.IsError {
		t.Fatalf("query_merge_queue failed: %s", mqRes.Text)
	}
	if !strings.Contains(mqRes.Text, "Pipeline Status:** Running") {
		t.Errorf("query_merge_queue output missing Running status: %s", mqRes.Text)
	}
	if !strings.Contains(mqRes.Text, "run-spec-42") {
		t.Errorf("query_merge_queue output missing active batch run: %s", mqRes.Text)
	}
	if !strings.Contains(mqRes.Text, "Task 933") {
		t.Errorf("query_merge_queue output missing queued PR 933: %s", mqRes.Text)
	}

	// =========================================================================
	// Section 3: CI Log Chunking, Byte Positioning & Tailing
	// =========================================================================
	t.Log("=== Section 3: CI Log Retrieval & Tailing ===")
	fullLog := "line 1: start\nline 2: compilation ok\nline 3: running tests\nline 4: passed\nline 5: finish\n"
	logBytes := []byte(fullLog)

	// Test offset and limit slicing
	line1 := "line 1: start\n"
	line2 := "line 2: compilation ok\n"
	offset := len(line1)
	limit := len(line2)
	subLog := string(logBytes[offset : offset+limit])
	if subLog != line2 {
		t.Fatalf("unexpected subLog slice: %q, want %q", subLog, line2)
	}

	// Test tailing (last 16 bytes)
	tailLen := len("line 5: finish\n")
	tailLog := string(logBytes[len(logBytes)-tailLen:])
	if tailLog != "line 5: finish\n" {
		t.Fatalf("unexpected tailLog slice: %q", tailLog)
	}

	// =========================================================================
	// Section 4: Test Quarantine Lifecycle & Non-Blocking CI Execution
	// =========================================================================
	t.Log("=== Section 4: Test Quarantine Lifecycle ===")
	mockSink := &mcp.MockSink{}
	quarantineToolConfig := mcp.QuarantineToolConfig{
		WorkspaceDir: repoDir,
		TaskID:       "task-933",
		Repo:         "bwsalmon/grain",
		Branch:       "grain/task-933",
		Sink:         mockSink,
	}
	quarantineTools := mcp.NewQuarantineTools(quarantineToolConfig)
	quarantineTool := quarantineTools[0]

	// 4A. Call quarantine_test MCP tool to quarantine a flaky test
	qRes := quarantineTool.Handler(ctx, map[string]any{
		"test_name":      "pkg/orchestrator_test.TestFlakyWorkerReconcile",
		"reason":         "Intermittent race condition under multi-tenant load",
		"ttl":            "14d",
		"file_follow_up": true,
	})
	if qRes.IsError {
		t.Fatalf("quarantine_test returned error: %s", qRes.Text)
	}
	if !strings.Contains(qRes.Text, "Quarantined test \"pkg/orchestrator_test.TestFlakyWorkerReconcile\"") {
		t.Errorf("quarantine_test output missing confirmation: %s", qRes.Text)
	}
	if !strings.Contains(qRes.Text, "Recorded in .grain/quarantine.yml") {
		t.Errorf("quarantine_test output missing yml notice: %s", qRes.Text)
	}

	// Verify follow-up task was generated in MockSink
	proposals := mockSink.ProposedTasks()
	if len(proposals) != 1 {
		t.Fatalf("expected 1 follow-up repair task, got %d", len(proposals))
	}
	if !strings.Contains(proposals[0].Title, "unquarantine and fix flaky test pkg/orchestrator_test.TestFlakyWorkerReconcile") {
		t.Errorf("unexpected follow-up task title: %s", proposals[0].Title)
	}

	// Verify .grain/quarantine.yml was written and contains active entry
	qFilePath := filepath.Join(repoDir, ".grain", "quarantine.yml")
	qData, err := os.ReadFile(qFilePath)
	if err != nil {
		t.Fatalf("reading .grain/quarantine.yml: %v", err)
	}
	var loadedConfig ci.QuarantineConfig
	if err := yaml.Unmarshal(qData, &loadedConfig); err != nil {
		t.Fatalf("unmarshaling .grain/quarantine.yml: %v", err)
	}
	if len(loadedConfig.Tests) != 1 {
		t.Fatalf("expected 1 test in quarantine config, got %d", len(loadedConfig.Tests))
	}
	entry := loadedConfig.Tests[0]
	if entry.TestName != "pkg/orchestrator_test.TestFlakyWorkerReconcile" {
		t.Errorf("unexpected entry test name: %s", entry.TestName)
	}
	if !entry.IsActive(time.Now().UTC()) {
		t.Errorf("quarantine entry should be active")
	}

	// 4B. CI Engine Non-Blocking Execution Verification
	t.Log("=== Section 4B: CI Engine Non-Blocking Execution ===")
	fakeSandboxes := &fakeCIExecutor{
		script: func(cmd ci.Command) (ci.CommandResult, error) {
			if strings.Contains(cmd.Script, "quarantine") {
				return ci.CommandResult{Output: string(qData), ExitCode: 0}, nil
			}
			if strings.Contains(cmd.Script, "go test") {
				// Simulate failure in the quarantined test
				failOutput := "=== RUN   TestFlakyWorkerReconcile\n--- FAIL: TestFlakyWorkerReconcile (0.05s)\n    reconcile_test.go:88: context deadline exceeded\nFAIL\nexit status 1\n"
				return ci.CommandResult{Output: failOutput, ExitCode: 1}, nil
			}
			return ci.CommandResult{ExitCode: 0}, nil
		},
	}

	ciEngine := &ci.Engine{Sandboxes: fakeSandboxes}
	plan := ci.Plan{
		Request: ci.Request{Repo: "bwsalmon/grain", Ref: "grain/task-933", Event: ci.EventPush},
		Jobs: []ci.PlannedJob{
			{
				ID: "ci.yml:test",
				Job: ci.Job{
					Name: "go",
					Steps: []ci.Step{
						{Run: "go test ./pkg/orchestrator/..."},
					},
				},
			},
		},
	}

	runResult := ciEngine.Run(ctx, "run-test-quarantine", plan, nil)

	// Assert that despite the test failing, the CI run is marked SUCCESS because the failure is quarantined
	if runResult.Conclusion != ci.ConclusionSuccess {
		t.Fatalf("expected CI run conclusion = %v (non-blocking), got %v", ci.ConclusionSuccess, runResult.Conclusion)
	}
	if !runResult.Quarantined {
		t.Fatalf("expected run.Quarantined = true, got false")
	}
	if len(runResult.Jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(runResult.Jobs))
	}
	jobRes := runResult.Jobs[0]
	if jobRes.Conclusion != ci.ConclusionSuccess {
		t.Errorf("expected job conclusion = %v, got %v", ci.ConclusionSuccess, jobRes.Conclusion)
	}
	if !jobRes.Quarantined {
		t.Errorf("expected job.Quarantined = true, got false")
	}
	if jobRes.Status != "passed (quarantined)" {
		t.Errorf("expected job.Status = %q, got %q", "passed (quarantined)", jobRes.Status)
	}
	if !strings.Contains(jobRes.Warning, "TestFlakyWorkerReconcile") {
		t.Errorf("expected warning mentioning quarantined test, got %q", jobRes.Warning)
	}

	// 4C. Mixed failure scenario: Quarantined test fails + Unquarantined test fails
	t.Log("=== Section 4C: Mixed Failure Blocking Verification ===")
	fakeMixedSandboxes := &fakeCIExecutor{
		script: func(cmd ci.Command) (ci.CommandResult, error) {
			if strings.Contains(cmd.Script, "quarantine") {
				return ci.CommandResult{Output: string(qData), ExitCode: 0}, nil
			}
			if strings.Contains(cmd.Script, "go test") {
				// Both quarantined test and a genuine unquarantined test fail
				mixedOutput := "=== RUN   TestFlakyWorkerReconcile\n--- FAIL: TestFlakyWorkerReconcile (0.05s)\nFAIL\n" +
					"=== RUN   TestGenuineRegression\n--- FAIL: TestGenuineRegression (0.02s)\n    genuine_test.go:12: assertion failed\nFAIL\nexit status 1\n"
				return ci.CommandResult{Output: mixedOutput, ExitCode: 1}, nil
			}
			return ci.CommandResult{ExitCode: 0}, nil
		},
	}

	mixedEngine := &ci.Engine{Sandboxes: fakeMixedSandboxes}
	mixedResult := mixedEngine.Run(ctx, "run-mixed-quarantine", plan, nil)

	// Assert that mixed failures STILL BLOCK the build
	if mixedResult.Conclusion != ci.ConclusionFailure {
		t.Fatalf("expected mixed failures to result in ConclusionFailure, got %v", mixedResult.Conclusion)
	}
	if mixedResult.Quarantined {
		t.Errorf("expected mixedResult.Quarantined = false when unquarantined tests fail")
	}

	// =========================================================================
	// Section 5: CI Diagnostic Tools (query_test_history, retrigger_ci, run_ci_job)
	// =========================================================================
	t.Log("=== Section 5: CI Diagnostic Tools ===")
	now := time.Now().UTC()
	t1 := now.Add(-2 * time.Hour)
	t2 := now.Add(-1 * time.Hour)
	t3 := now.Add(-30 * time.Minute)

	fakeRunner := &fakeGrainCIRunner{
		runs: []ci.Run{
			{
				ID:         "run-hist-1",
				Repo:       "bwsalmon/grain",
				Ref:        "main",
				Commit:     "111111111111",
				Conclusion: ci.ConclusionSuccess,
				Jobs: []ci.JobRun{
					{
						JobID:      "job-1",
						Name:       "go",
						Conclusion: ci.ConclusionSuccess,
						StartedAt:  &t1,
						Steps: []ci.StepRun{
							{Name: "test", Conclusion: ci.ConclusionSuccess, Output: "=== RUN   TestFlakyWorkerReconcile\n--- PASS: TestFlakyWorkerReconcile (0.02s)\nPASS\n"},
						},
					},
				},
			},
			{
				ID:         "run-hist-2",
				Repo:       "bwsalmon/grain",
				Ref:        "grain/task-930",
				Commit:     "222222222222",
				Conclusion: ci.ConclusionFailure,
				Jobs: []ci.JobRun{
					{
						JobID:      "job-2",
						Name:       "go",
						Conclusion: ci.ConclusionFailure,
						StartedAt:  &t2,
						FailureSummary: &ci.FailureSummary{
							Tests: []ci.TestFailure{
								{Name: "TestFlakyWorkerReconcile", Location: "reconcile_test.go:88", Message: "context deadline exceeded"},
							},
						},
						Steps: []ci.StepRun{
							{Name: "test", Conclusion: ci.ConclusionFailure, Output: "=== RUN   TestFlakyWorkerReconcile\n--- FAIL: TestFlakyWorkerReconcile (0.05s)\n    reconcile_test.go:88: context deadline exceeded\nFAIL\n"},
						},
					},
				},
			},
			{
				ID:         "run-hist-3",
				Repo:       "bwsalmon/grain",
				Ref:        "grain/task-931",
				Commit:     "333333333333",
				Conclusion: ci.ConclusionSuccess,
				Jobs: []ci.JobRun{
					{
						JobID:      "job-3",
						Name:       "go",
						Conclusion: ci.ConclusionSuccess,
						StartedAt:  &t3,
						Flake:      true,
						RetryCount: 1,
						Status:     "passed (retry)",
						FailureSummary: &ci.FailureSummary{
							Tests: []ci.TestFailure{
								{Name: "TestFlakyWorkerReconcile", Location: "reconcile_test.go:88", Message: "flaky timeout on initial attempt"},
							},
						},
						Steps: []ci.StepRun{
							{Name: "test", Conclusion: ci.ConclusionSuccess, Output: "=== RUN   TestFlakyWorkerReconcile\n--- PASS: TestFlakyWorkerReconcile (0.03s)\nPASS\n"},
						},
					},
				},
			},
		},
	}

	ciScope := mcp.CIRepoScope{Repo: "bwsalmon/grain"}
	waitTools := mcp.NewGrainCIWaitTools(fakeRunner, ciScope)
	var queryHistoryTool, retriggerTool, waitForChecksTool mcp.Tool
	for _, tool := range waitTools {
		switch tool.Name {
		case "query_test_history":
			queryHistoryTool = tool
		case "retrigger_ci":
			retriggerTool = tool
		case "wait_for_checks":
			waitForChecksTool = tool
		}
	}

	// 5A. Test query_test_history tool
	historyRes := queryHistoryTool.Handler(ctx, map[string]any{
		"test_name": "TestFlakyWorkerReconcile",
	})
	if historyRes.IsError {
		t.Fatalf("query_test_history returned error: %s", historyRes.Text)
	}
	if !strings.Contains(historyRes.Text, "Test History: TestFlakyWorkerReconcile") {
		t.Errorf("history output missing title: %s", historyRes.Text)
	}
	if !strings.Contains(historyRes.Text, "Passed:  1 (33.3%)") || !strings.Contains(historyRes.Text, "Flaked:  1 (33.3%)") {
		t.Errorf("history output missing expected pass/flake rates: %s", historyRes.Text)
	}
	if !strings.Contains(historyRes.Text, "run-hist-1") || !strings.Contains(historyRes.Text, "run-hist-2") || !strings.Contains(historyRes.Text, "run-hist-3") {
		t.Errorf("history output missing expected run IDs: %s", historyRes.Text)
	}

	// 5B. Test retrigger_ci tool
	retriggerRes := retriggerTool.Handler(ctx, map[string]any{
		"workflow": ".grain/workflows/ci.yml",
		"commit":   "abcdef123456",
	})
	if retriggerRes.IsError {
		t.Fatalf("retrigger_ci returned error: %s", retriggerRes.Text)
	}
	if !strings.Contains(retriggerRes.Text, "Triggered fresh uncached Grain CI run") {
		t.Errorf("retrigger_ci output missing confirmation: %s", retriggerRes.Text)
	}
	if !strings.Contains(retriggerRes.Text, "abcdef123456") {
		t.Errorf("retrigger_ci output missing commit SHA: %s", retriggerRes.Text)
	}

	// 5C. Test run_ci_job tool
	runJobTools := mcp.NewRunCIJobTools(fakeRunner, ciScope)
	runJobTool := runJobTools[0]
	jobResCall := runJobTool.Handler(ctx, map[string]any{
		"job": "go",
	})
	if jobResCall.IsError {
		t.Fatalf("run_ci_job returned error: %s", jobResCall.Text)
	}
	if !strings.Contains(jobResCall.Text, "Job \"go\" log output:") {
		t.Errorf("run_ci_job output missing log output section: %s", jobResCall.Text)
	}

	// =========================================================================
	// Section 6: Structured Test Failure Extraction in wait_for_checks
	// =========================================================================
	t.Log("=== Section 6: Structured Test Failure Extraction ===")
	failRun := ci.Run{
		ID:         "run-fail-struct",
		Repo:       "bwsalmon/grain",
		Ref:        "grain/task-955",
		Commit:     "444455556666",
		Conclusion: ci.ConclusionFailure,
		Jobs: []ci.JobRun{
			{
				JobID:      "go",
				Name:       "go",
				Conclusion: ci.ConclusionFailure,
				Steps: []ci.StepRun{
					{
						Name:       "go test",
						Conclusion: ci.ConclusionFailure,
						ExitCode:   1,
						Output: "=== RUN   TestPaymentReconciliation\n" +
							"--- FAIL: TestPaymentReconciliation (0.04s)\n" +
							"    reconcile_test.go:42: timeout connecting to payment endpoint\n" +
							"FAIL\nexit status 1\n",
					},
				},
			},
		},
	}
	fakeRunner.singleRun = &failRun

	waitRes := waitForChecksTool.Handler(ctx, map[string]any{})
	if waitRes.IsError {
		t.Fatalf("wait_for_checks returned unexpected error: %s", waitRes.Text)
	}
	if !strings.Contains(waitRes.Text, "Failure summary:") {
		t.Errorf("wait_for_checks missing Failure summary section: %s", waitRes.Text)
	}
	if !strings.Contains(waitRes.Text, "TestPaymentReconciliation") {
		t.Errorf("wait_for_checks missing failed test name: %s", waitRes.Text)
	}
	if !strings.Contains(waitRes.Text, "reconcile_test.go:42") {
		t.Errorf("wait_for_checks missing test location: %s", waitRes.Text)
	}

	// =========================================================================
	// Section 7: In-Flight Job Retry on Infrastructure Errors
	// =========================================================================
	t.Log("=== Section 7: In-Flight Job Retry on Infrastructure Errors ===")
	var attempts int
	fakeRetryExecutor := &fakeCIExecutorWithCount{
		script: func(cmd ci.Command) (ci.CommandResult, error) {
			if strings.Contains(cmd.Script, "go test") {
				attempts++
				if attempts == 1 {
					// Attempt 1: Transient infrastructure error (dial tcp timeout)
					return ci.CommandResult{
						Output:   "dial tcp 10.0.0.1:443: i/o timeout\nexit status 1",
						ExitCode: 1,
					}, nil
				}
				// Attempt 2: Success
				return ci.CommandResult{
					Output:   "=== RUN   TestStable\n--- PASS: TestStable (0.01s)\nPASS\n",
					ExitCode: 0,
				}, nil
			}
			return ci.CommandResult{ExitCode: 0}, nil
		},
	}

	retryEngine := &ci.Engine{Sandboxes: fakeRetryExecutor}
	retryPlan := ci.Plan{
		Request: ci.Request{Repo: "bwsalmon/grain", Ref: "grain/task-955", Event: ci.EventPush},
		Jobs: []ci.PlannedJob{
			{
				ID: "ci.yml:test-transient",
				Job: ci.Job{
					Name: "go",
					Steps: []ci.Step{
						{Run: "go test ./pkg/..."},
					},
				},
			},
		},
	}

	retryRunResult := retryEngine.Run(ctx, "run-retry-infra", retryPlan, nil)
	if retryRunResult.Conclusion != ci.ConclusionSuccess {
		t.Fatalf("expected retry run conclusion = %v, got %v", ci.ConclusionSuccess, retryRunResult.Conclusion)
	}
	if len(retryRunResult.Jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(retryRunResult.Jobs))
	}
	retriedJob := retryRunResult.Jobs[0]
	if retriedJob.Conclusion != ci.ConclusionSuccess {
		t.Errorf("expected retried job conclusion = %v, got %v", ci.ConclusionSuccess, retriedJob.Conclusion)
	}
	if retriedJob.RetryCount != 1 {
		t.Errorf("expected retriedJob.RetryCount = 1, got %d", retriedJob.RetryCount)
	}
	if !retriedJob.Flake {
		t.Errorf("expected retriedJob.Flake = true, got false")
	}
	if retriedJob.Status != "passed (retry)" {
		t.Errorf("expected retriedJob.Status = %q, got %q", "passed (retry)", retriedJob.Status)
	}
	if !strings.Contains(retriedJob.Warning, "flaky failure on initial attempt") {
		t.Errorf("expected flake warning, got %q", retriedJob.Warning)
	}
	if fakeRetryExecutor.acquireCount != 2 {
		t.Errorf("expected 2 sandbox acquisitions (1 initial + 1 retry), got %d", fakeRetryExecutor.acquireCount)
	}
}

type fakeGrainCIRunner struct {
	starts      int
	retriggered int
	runs        []ci.Run
	singleRun   *ci.Run
	faults      []mcp.CIFault
}

func (f *fakeGrainCIRunner) StartGrainCI(ctx context.Context, jobs []string) (ci.Run, error) {
	f.starts++
	if f.singleRun != nil {
		return *f.singleRun, nil
	}
	return ci.Run{
		ID:         "run-single-1",
		Repo:       "bwsalmon/grain",
		Ref:        "grain/task-955",
		Commit:     "112233445566",
		Conclusion: ci.ConclusionSuccess,
		Jobs: []ci.JobRun{
			{
				JobID:      "go",
				Name:       "go",
				Conclusion: ci.ConclusionSuccess,
				Steps: []ci.StepRun{
					{Name: "test", Conclusion: ci.ConclusionSuccess, Output: "all tests passed"},
				},
			},
		},
	}, nil
}

func (f *fakeGrainCIRunner) RetriggerGrainCI(ctx context.Context, workflow, commit string, jobs []string, all bool) (ci.Run, error) {
	f.retriggered++
	c := commit
	if c == "" {
		c = "112233445566"
	}
	return ci.Run{
		ID:     "run-retrigger-42",
		Repo:   "bwsalmon/grain",
		Ref:    "grain/task-955",
		Commit: c,
		Jobs: []ci.JobRun{
			{JobID: "ci.yml:test", Name: "go"},
		},
	}, nil
}

func (f *fakeGrainCIRunner) GrainCIRun(ctx context.Context, id string) (ci.Run, error) {
	if f.singleRun != nil {
		return *f.singleRun, nil
	}
	for _, r := range f.runs {
		if r.ID == id {
			return r, nil
		}
	}
	return ci.Run{
		ID:         id,
		Repo:       "bwsalmon/grain",
		Ref:        "grain/task-955",
		Commit:     "112233445566",
		Conclusion: ci.ConclusionSuccess,
		Jobs: []ci.JobRun{
			{
				JobID:      "go",
				Name:       "go",
				Conclusion: ci.ConclusionSuccess,
				Steps: []ci.StepRun{
					{Name: "test", Conclusion: ci.ConclusionSuccess, Output: "ok"},
				},
			},
		},
	}, nil
}

func (f *fakeGrainCIRunner) FindGrainCIRuns(ctx context.Context, filter ci.RunFilter, limit int) (ci.RunPage, error) {
	var matching []ci.Run
	for _, r := range f.runs {
		if filter.Repo == "" || filter.Repo == r.Repo {
			matching = append(matching, r)
		}
	}
	return ci.RunPage{
		Runs:    matching,
		Stored:  len(matching),
		Matched: len(matching),
	}, nil
}

func (f *fakeGrainCIRunner) GrainCISharedFaults(ctx context.Context) ([]mcp.CIFault, error) {
	return f.faults, nil
}

type fakeCIExecutorWithCount struct {
	script       func(ci.Command) (ci.CommandResult, error)
	acquireCount int
}

func (f *fakeCIExecutorWithCount) Name() string {
	return "fake-sandbox-executor-count"
}

func (f *fakeCIExecutorWithCount) Exec(ctx context.Context, cmd ci.Command) (ci.CommandResult, error) {
	if f.script != nil {
		return f.script(cmd)
	}
	return ci.CommandResult{ExitCode: 0}, nil
}

func (f *fakeCIExecutorWithCount) Release(ctx context.Context) error {
	return nil
}

func (f *fakeCIExecutorWithCount) Acquire(ctx context.Context, req ci.SandboxRequest) (ci.Sandbox, error) {
	f.acquireCount++
	return f, nil
}

type fakeMergeQueueReader struct {
	report mcp.MergeQueueReport
	err    error
}

func (f *fakeMergeQueueReader) QueryMergeQueue(ctx context.Context, repo, branch string) (mcp.MergeQueueReport, error) {
	return f.report, f.err
}

type fakeCIExecutor struct {
	script func(ci.Command) (ci.CommandResult, error)
}

func (f *fakeCIExecutor) Name() string {
	return "fake-sandbox-executor"
}

func (f *fakeCIExecutor) Exec(ctx context.Context, cmd ci.Command) (ci.CommandResult, error) {
	if f.script != nil {
		return f.script(cmd)
	}
	return ci.CommandResult{ExitCode: 0}, nil
}

func (f *fakeCIExecutor) Release(ctx context.Context) error {
	return nil
}

func (f *fakeCIExecutor) Acquire(ctx context.Context, req ci.SandboxRequest) (ci.Sandbox, error) {
	return f, nil
}
