package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/ci"
)

func runCIJobTool(runner GrainCIRunner) Tool {
	tools := NewRunCIJobTools(runner, CIRepoScope{Repo: testCIRepo})
	return tools[0]
}

func TestRunCIJobRequiresJobArgument(t *testing.T) {
	runner := &fakeGrainCIRunner{}
	tool := runCIJobTool(runner)

	for _, args := range []map[string]any{
		nil,
		{},
		{"job": ""},
		{"job": "   "},
	} {
		res := tool.Handler(context.Background(), args)
		if !res.IsError || !strings.Contains(res.Text, "job is required") {
			t.Fatalf("args=%v: res=%+v, want job is required error", args, res)
		}
	}
	if runner.starts != 0 {
		t.Fatalf("runner.starts = %d, want 0", runner.starts)
	}
}

func TestRunCIJobNilRunner(t *testing.T) {
	tools := NewRunCIJobTools(nil, CIRepoScope{Repo: testCIRepo})
	res := tools[0].Handler(context.Background(), map[string]any{"job": "go"})
	if !res.IsError || !strings.Contains(res.Text, "Grain CI is unavailable") {
		t.Fatalf("res=%+v, want unavailable error", res)
	}
}

func TestRunCIJobSuccessWithLogs(t *testing.T) {
	now := time.Now()
	runner := &fakeGrainCIRunner{
		run: &ci.Run{
			ID:         "ci-1",
			Repo:       testCIRepo,
			Ref:        "grain/task-1",
			Commit:     "abcdef012345",
			Conclusion: ci.ConclusionSuccess,
			StartedAt:  &now,
			Jobs: []ci.JobRun{
				{
					JobID:      "lint",
					Name:       "lint",
					Conclusion: ci.ConclusionSuccess,
					StartedAt:  &now,
					Steps: []ci.StepRun{
						{
							Name:       "golangci-lint",
							Conclusion: ci.ConclusionSuccess,
							Output:     "golangci-lint passed with 0 errors",
						},
					},
				},
			},
		},
	}
	tool := runCIJobTool(runner)
	res := tool.Handler(context.Background(), map[string]any{"job": "lint"})
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Text)
	}
	if !strings.Contains(res.Text, "is success") {
		t.Errorf("text = %q, want success verdict", res.Text)
	}
	if !strings.Contains(res.Text, "golangci-lint passed with 0 errors") {
		t.Errorf("text = %q, want step log output in verdict", res.Text)
	}
	if !strings.Contains(res.Text, "This was a selective run") {
		t.Errorf("text = %q, want selective run reminder", res.Text)
	}
	if runner.starts != 1 || len(runner.jobs) != 1 || runner.jobs[0] != "lint" {
		t.Fatalf("runner starts=%d, jobs=%v, want [lint]", runner.starts, runner.jobs)
	}
}

func TestRunCIJobFailureWithLogs(t *testing.T) {
	now := time.Now()
	runner := &fakeGrainCIRunner{
		run: &ci.Run{
			ID:         "ci-1",
			Repo:       testCIRepo,
			Ref:        "grain/task-1",
			Commit:     "abcdef012345",
			Conclusion: ci.ConclusionFailure,
			StartedAt:  &now,
			Jobs: []ci.JobRun{
				{
					JobID:      "go",
					Name:       "go",
					Conclusion: ci.ConclusionFailure,
					StartedAt:  &now,
					Steps: []ci.StepRun{
						{
							Name:       "go test",
							Conclusion: ci.ConclusionFailure,
							ExitCode:   1,
							Output:     "--- FAIL: TestFoo (0.01s)\n    foo_test.go:42: expected true, got false",
						},
					},
				},
			},
		},
	}
	tool := runCIJobTool(runner)
	res := tool.Handler(context.Background(), map[string]any{"job": "go"})
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Text)
	}
	if !strings.Contains(res.Text, "is failure") {
		t.Errorf("text = %q, want failure verdict", res.Text)
	}
	if !strings.Contains(res.Text, "--- FAIL: TestFoo") {
		t.Errorf("text = %q, want failure log output in verdict", res.Text)
	}
	if !strings.Contains(res.Text, "This was a selective run") {
		t.Errorf("text = %q, want selective run reminder", res.Text)
	}
}

func TestRunCIJobFallsBackToFailedJobLog(t *testing.T) {
	now := time.Now()
	runner := &fakeGrainCIRunner{
		run: &ci.Run{
			ID:         "ci-1",
			Repo:       testCIRepo,
			Ref:        "grain/task-1",
			Commit:     "abcdef012345",
			Conclusion: ci.ConclusionFailure,
			StartedAt:  &now,
			Jobs: []ci.JobRun{
				{
					JobID:      "build",
					Name:       "build",
					Conclusion: ci.ConclusionFailure,
					StartedAt:  &now,
					Steps: []ci.StepRun{
						{
							Name:       "compile",
							Conclusion: ci.ConclusionFailure,
							Error:      "syntax error in main.go",
						},
					},
				},
				{
					JobID:      "test",
					Name:       "test",
					Conclusion: ci.ConclusionSkipped,
				},
			},
		},
	}
	tool := runCIJobTool(runner)
	res := tool.Handler(context.Background(), map[string]any{"job": "test"})
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Text)
	}
	if !strings.Contains(res.Text, "syntax error in main.go") {
		t.Errorf("text = %q, want failed dependency build log output", res.Text)
	}
}
