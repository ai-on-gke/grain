package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type testTaskReverter struct {
	lastReq RevertTaskRequest
	called  bool
	res     RevertTaskResult
	err     error
}

func (r *testTaskReverter) RevertTask(ctx context.Context, req RevertTaskRequest) (RevertTaskResult, error) {
	r.lastReq = req
	r.called = true
	return r.res, r.err
}

func TestRevertToolsNilReverter(t *testing.T) {
	tools := NewRevertTools(nil)
	if len(tools) != 2 {
		t.Fatalf("len(tools) = %d, want 2", len(tools))
	}
	for _, tool := range tools {
		res := tool.Handler(t.Context(), map[string]any{"reason": "test", "task_id": "123"})
		if res.Text != noTaskReverter {
			t.Fatalf("got %q, want %q", res.Text, noTaskReverter)
		}
	}
}

func TestRevertToolsValidation(t *testing.T) {
	reverter := &testTaskReverter{}
	tools := NewRevertTools(reverter)

	for _, tool := range tools {
		// Missing reason
		res := tool.Handler(t.Context(), map[string]any{"task_id": "123"})
		if !strings.Contains(res.Text, "reason is required") {
			t.Errorf("%s: expected reason is required error, got: %s", tool.Name, res.Text)
		}

		// Missing both task_id and commit_sha
		res = tool.Handler(t.Context(), map[string]any{"reason": "some reason"})
		if !strings.Contains(res.Text, "either task_id or commit_sha must be provided") {
			t.Errorf("%s: expected target error, got: %s", tool.Name, res.Text)
		}

		// Option-shaped values never reach the reverter.
		for _, args := range []map[string]any{
			{"reason": "r", "commit_sha": "--output=/tmp/x"},
			{"reason": "r", "commit_sha": "HEAD~1"},
			{"reason": "r", "task_id": "--all"},
			{"reason": "r", "task_id": "task 1"},
		} {
			res = tool.Handler(t.Context(), args)
			if !res.IsError || !strings.Contains(res.Text, "is not a") {
				t.Errorf("%s(%v): expected shape error, got: %s", tool.Name, args, res.Text)
			}
		}
		if reverter.called {
			t.Errorf("%s: reverter was called with an invalid target", tool.Name)
		}
	}
}

func TestRevertToolsSuccess(t *testing.T) {
	reverter := &testTaskReverter{
		res: RevertTaskResult{
			NewCommitSHA: "abc999",
			TaskID:       "task-789",
			CommitSHA:    "def123",
			Reason:       "breaking change on production",
			Summary:      "feat: add broken feature",
		},
	}
	tools := NewRevertTools(reverter)
	for _, tool := range tools {
		res := tool.Handler(t.Context(), map[string]any{
			"task_id": "task-789",
			"reason":  "breaking change on production",
		})
		if reverter.lastReq.TaskID != "task-789" || reverter.lastReq.Reason != "breaking change on production" {
			t.Fatalf("%s passed bad request: %+v", tool.Name, reverter.lastReq)
		}
		if !strings.Contains(res.Text, "Successfully created revert commit `abc999`") {
			t.Errorf("%s: missing success output: %s", tool.Name, res.Text)
		}
		if !strings.Contains(res.Text, "Origin Task: task-789") {
			t.Errorf("%s: missing origin task in output: %s", tool.Name, res.Text)
		}
		if !strings.Contains(res.Text, "Target Commit: `def123`") {
			t.Errorf("%s: missing target commit in output: %s", tool.Name, res.Text)
		}
	}
}

func TestRevertToolsError(t *testing.T) {
	reverter := &testTaskReverter{
		err: errors.New("merge conflict in file.go"),
	}
	tools := NewRevertTools(reverter)
	for _, tool := range tools {
		res := tool.Handler(t.Context(), map[string]any{
			"commit_sha": "def123",
			"reason":     "bad commit",
		})
		if !strings.Contains(res.Text, "merge conflict in file.go") {
			t.Errorf("%s: expected conflict error in result, got: %s", tool.Name, res.Text)
		}
	}
}
