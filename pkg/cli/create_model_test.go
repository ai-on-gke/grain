package cli

// grain/task-110: `grain create -model <id>` is the shell's half of the
// per-task model pin the UI's own pickers offer -- including a model
// taken out of the rotation, which is the only way work reaches one.

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/github/githubsim"
	"github.com/bwsalmon/grain/pkg/gitproxy"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/sqlite"
	"github.com/bwsalmon/grain/pkg/ui"
)

// modelPoolServer is a deployment with two configured models, one of them
// out of the rotation.
func modelPoolServer(t *testing.T) string {
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
		{ID: "codex-day", Name: "Day account", Framework: model.AgentFrameworkCodex},
		{ID: "claude-deep", Name: "Deep account", Framework: model.AgentFrameworkClaude, Disabled: true},
	}}); err != nil {
		t.Fatalf("saving the model pool: %v", err)
	}
	srv := httptest.NewServer(accessCodeServer(ui.NewClient(
		ui.Config{Actor: ui.DefaultActor("tester"), Capabilities: ui.OfferedCapabilities()}, store)))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestCreatePinsATaskToADisabledModel(t *testing.T) {
	t.Parallel()
	server := modelPoolServer(t)

	if err := runCLIWithAccessCode([]string{"-server", server, "create", "-title", "One expensive job",
		"-no-repo", "-model", "claude-deep"}); err != nil {
		t.Fatalf("grain create -model: %v", err)
	}

	task, err := accessCodeClient(server).Task(context.Background(), "1")
	if err != nil {
		t.Fatalf("reading the task back: %v", err)
	}
	if task.AgentBackendID != "claude-deep" {
		t.Fatalf("task pinned to %q, want claude-deep", task.AgentBackendID)
	}
	// And the framework came with it, without the flag having to say so
	// twice (ui.CreateTaskRequest.AgentBackendID).
	if task.AgentFramework != model.AgentFrameworkClaude {
		t.Fatalf("task framework = %q, want claude", task.AgentFramework)
	}
}

func TestCreateRefusesAModelThisDeploymentDoesNotHave(t *testing.T) {
	t.Parallel()
	server := modelPoolServer(t)

	err := runCLIWithAccessCode([]string{"-server", server, "create", "-title", "Nowhere to run",
		"-no-repo", "-model", "codex-night"})
	if err == nil {
		t.Fatal("grain create -model with an unknown model succeeded")
	}
	if !strings.Contains(err.Error(), "codex-night") {
		t.Fatalf("error = %v, want it to name the model", err)
	}
}

func TestCreateOnANonGitHubHostWithNoConnectionDoesNotMentionGitHub(t *testing.T) {
	t.Parallel()
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
	creds, err := gitproxy.LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sim := githubsim.New("acme", "widgets", "", "main")
	reach := &gitproxy.Reach{Credentials: creds, API: sim, Host: "github.com"}
	srv := httptest.NewServer(accessCodeServer(ui.NewClient(
		ui.Config{Actor: ui.DefaultActor("tester"), Capabilities: ui.OfferedCapabilities(), GitReach: reach}, store)))
	t.Cleanup(srv.Close)

	err = runCLIWithAccessCode([]string{"-server", srv.URL, "create", "-title", "Walkthrough demo",
		"-repo", "127.0.0.1:8088/walk/demo"})
	if err == nil {
		t.Fatal("grain create on an unconnected host succeeded")
	}
	for _, want := range []string{"the deployment has no git connection on 127.0.0.1:8088", "127.0.0.1:8088/walk/demo", "Settings → Git"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err.Error(), want)
		}
	}
	if strings.Contains(err.Error(), "GitHub") {
		t.Errorf("error for a non-GitHub host mentions GitHub: %q", err.Error())
	}
}
