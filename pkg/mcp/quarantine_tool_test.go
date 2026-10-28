package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
)

func TestQuarantineTool(t *testing.T) {
	tempDir := t.TempDir()
	sink := &MockSink{}

	tools := NewQuarantineTools(QuarantineToolConfig{
		WorkspaceDir: tempDir,
		TaskID:       "task-744",
		Repo:         "bwsalmon/grain",
		Branch:       "grain/task-744",
		Sink:         sink,
	})

	if len(tools) != 1 || tools[0].Name != "quarantine_test" {
		t.Fatalf("unexpected tools: %v", tools)
	}
	tool := tools[0]

	// 1. Missing test_name
	res := tool.Handler(context.Background(), map[string]any{
		"reason": "flaky",
	})
	if !res.IsError {
		t.Errorf("expected error for missing test_name, got %v", res)
	}

	// 2. Missing reason
	res = tool.Handler(context.Background(), map[string]any{
		"test_name": "TestFoo",
	})
	if !res.IsError {
		t.Errorf("expected error for missing reason, got %v", res)
	}

	// 3. Successful quarantine
	res = tool.Handler(context.Background(), map[string]any{
		"test_name":      "pkg/orchestrator_test.TestFlakyReconcile",
		"reason":         "Timeout waiting on mock state",
		"file_follow_up": true,
		"ttl":            "14d",
	})
	if res.IsError {
		t.Fatalf("expected success, got %v", res)
	}
	if !strings.Contains(res.Text, "Quarantined test \"pkg/orchestrator_test.TestFlakyReconcile\"") {
		t.Errorf("unexpected output text: %s", res.Text)
	}
	if !strings.Contains(res.Text, "Follow-up repair task filed:") {
		t.Errorf("expected mention of follow up task: %s", res.Text)
	}

	// Verify sink recorded follow-up task
	proposedTasks := sink.ProposedTasks()
	if len(proposedTasks) != 1 {
		t.Fatalf("expected 1 proposed task recorded, got %d", len(proposedTasks))
	}
	proposed := proposedTasks[0]
	if !strings.Contains(proposed.Title, "TestFlakyReconcile") {
		t.Errorf("unexpected proposal title: %s", proposed.Title)
	}

	// Verify file written
	data, err := os.ReadFile(filepath.Join(tempDir, ".grain/quarantine.yml"))
	if err != nil {
		t.Fatalf("reading quarantine.yml: %v", err)
	}
	qCfg, err := ci.LoadQuarantine(data)
	if err != nil {
		t.Fatalf("loading quarantine: %v", err)
	}
	if len(qCfg.Tests) != 1 {
		t.Fatalf("expected 1 test entry, got %d", len(qCfg.Tests))
	}
	entry := qCfg.Tests[0]
	if entry.TestName != "pkg/orchestrator_test.TestFlakyReconcile" {
		t.Errorf("unexpected test name: %s", entry.TestName)
	}
	if entry.TaskID != "task-744" {
		t.Errorf("unexpected task ID: %s", entry.TaskID)
	}
	if entry.FollowUpTaskID == "" {
		t.Errorf("expected non-empty follow up task ID")
	}
	if entry.TTL != "14d" {
		t.Errorf("expected TTL 14d, got %s", entry.TTL)
	}

	// 4. Update existing entry with new reason
	res = tool.Handler(context.Background(), map[string]any{
		"test_name":      "pkg/orchestrator_test.TestFlakyReconcile",
		"reason":         "Updated reason",
		"file_follow_up": false,
	})
	if res.IsError {
		t.Fatalf("expected update success, got %v", res)
	}

	data, _ = os.ReadFile(filepath.Join(tempDir, ".grain/quarantine.yml"))
	qCfg, _ = ci.LoadQuarantine(data)
	if len(qCfg.Tests) != 1 {
		t.Fatalf("expected 1 test entry after update, got %d", len(qCfg.Tests))
	}
	if qCfg.Tests[0].Reason != "Updated reason" {
		t.Errorf("unexpected updated reason: %s", qCfg.Tests[0].Reason)
	}
}
