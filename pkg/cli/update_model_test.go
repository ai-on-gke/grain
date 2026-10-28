package cli

// `grain update -model` / `-connection` are the shell's half of the task
// pane's Edit -> Model and Specific connection pickers: how a task already
// filed is moved onto another model, most often because the one it was on
// met its usage limit, without filing it again.

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// twoFrameworkServer is a deployment whose default team has one Claude
// connection and one Codex connection, each advertising a model of its own.
func twoFrameworkServer(t *testing.T) string {
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
	if err := store.PutConfig(ctx, model.Config{AgentBackends: []model.AgentBackend{
		{ID: "claude-day", Name: "Claude account", Framework: model.AgentFrameworkClaude, Models: []string{"claude-sonnet-5"}},
		{ID: "codex-day", Name: "Codex account", Framework: model.AgentFrameworkCodex, Models: []string{"gpt-6"}},
	}}); err != nil {
		t.Fatalf("saving the connections: %v", err)
	}
	srv := httptest.NewServer(accessCodeServer(ui.NewClient(
		ui.Config{Actor: ui.DefaultActor("tester"), Capabilities: ui.OfferedCapabilities()}, store)))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestUpdateMovesATaskOntoAnotherModel(t *testing.T) {
	t.Parallel()
	server := twoFrameworkServer(t)
	ctx := context.Background()
	client := accessCodeClient(server)

	if err := runCLIWithAccessCode([]string{"-server", server, "create", "-title", "Long job",
		"-no-repo", "-model", "claude-sonnet-5"}); err != nil {
		t.Fatalf("grain create -model: %v", err)
	}
	task, err := client.Task(ctx, "1")
	if err != nil {
		t.Fatalf("reading the task back: %v", err)
	}
	if task.Model != "claude-sonnet-5" {
		t.Fatalf("filed on model %q, want claude-sonnet-5", task.Model)
	}

	// The Claude account ran out: move the task onto the Codex model.
	if err := runCLIWithAccessCode([]string{"-server", server, "update", "1", "-model", "gpt-6"}); err != nil {
		t.Fatalf("grain update -model: %v", err)
	}
	task, err = client.Task(ctx, "1")
	if err != nil {
		t.Fatalf("reading the task back: %v", err)
	}
	if task.Model != "gpt-6" {
		t.Fatalf("task model = %q after update, want gpt-6", task.Model)
	}
	// The framework follows the model, as it does from the task pane.
	if task.AgentFramework != model.AgentFrameworkCodex {
		t.Fatalf("task framework = %q after update, want codex", task.AgentFramework)
	}

	// Pin a connection, then take both back off.
	if err := runCLIWithAccessCode([]string{"-server", server, "update", "1", "-connection", "codex-day"}); err != nil {
		t.Fatalf("grain update -connection: %v", err)
	}
	task, err = client.Task(ctx, "1")
	if err != nil {
		t.Fatalf("reading the task back: %v", err)
	}
	if task.AgentBackendID != "codex-day" {
		t.Fatalf("task pinned to %q, want codex-day", task.AgentBackendID)
	}
	if err := runCLIWithAccessCode([]string{"-server", server, "update", "1", "-model", "", "-connection", ""}); err != nil {
		t.Fatalf("grain update -model '' -connection '': %v", err)
	}
	task, err = client.Task(ctx, "1")
	if err != nil {
		t.Fatalf("reading the task back: %v", err)
	}
	if task.Model != "" || task.AgentBackendID != "" {
		t.Fatalf("task model/pin = %q/%q after clearing, want both empty", task.Model, task.AgentBackendID)
	}
}

func TestUpdateRefusesAModelNoConnectionServes(t *testing.T) {
	t.Parallel()
	server := twoFrameworkServer(t)

	if err := runCLIWithAccessCode([]string{"-server", server, "create", "-title", "Long job", "-no-repo"}); err != nil {
		t.Fatalf("grain create: %v", err)
	}
	if err := runCLIWithAccessCode([]string{"-server", server, "update", "1", "-connection", "claude-day", "-model", "gpt-6"}); err == nil {
		t.Fatal("grain update pinned a connection to a model it does not serve")
	}
}
