package cli

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func issueTestServer(t *testing.T) string {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(t.TempDir(), "data")))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	srv := httptest.NewServer(accessCodeServer(ui.NewClient(
		ui.Config{Actor: ui.DefaultActor("tester"), Capabilities: ui.OfferedCapabilities()}, store)))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestCLICreateAndUpdateIssue(t *testing.T) {
	t.Parallel()
	server := issueTestServer(t)

	if err := runCLIWithAccessCode([]string{"-server", server, "create", "-title", "Task with issue",
		"-no-repo", "-issue", "456"}); err != nil {
		t.Fatalf("grain create -issue: %v", err)
	}

	task, err := accessCodeClient(server).Task(context.Background(), "1")
	if err != nil {
		t.Fatalf("reading the task back: %v", err)
	}
	if task.Issue != 456 {
		t.Fatalf("task.Issue = %d, want 456", task.Issue)
	}

	if err := runCLIWithAccessCode([]string{"-server", server, "update", "1", "-issue", "789"}); err != nil {
		t.Fatalf("grain update -issue: %v", err)
	}

	task, err = accessCodeClient(server).Task(context.Background(), "1")
	if err != nil {
		t.Fatalf("reading the updated task back: %v", err)
	}
	if task.Issue != 789 {
		t.Fatalf("task.Issue = %d, want 789", task.Issue)
	}
}
