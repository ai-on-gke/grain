package ui_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// executableIn writes an executable named name into dir.
func executableIn(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// cliPathTeam is a store whose deployment owns connection infra-claude,
// whose actor -- the operator every request runs as -- is the
// deployment's admin.
func cliPathTeam(t *testing.T) (*ui.Server, *ui.Client) {
	t.Helper()
	srv, client := testServer(t)
	client.Config.Secrets = secrets.New(t.TempDir())
	ctx := context.Background()
	if err := client.Store.PutConfig(ctx, model.Config{
		PollInterval:  model.DefaultPollInterval,
		AgentBackends: []model.AgentBackend{{ID: "infra-claude", Framework: model.AgentFrameworkClaude, Team: model.DefaultTeamID}},
	}); err != nil {
		t.Fatal(err)
	}
	return srv, client
}

func putConnection(t *testing.T, srv *ui.Server, user, body string) (int, string) {
	t.Helper()
	rec := doWithHeaders(t, srv, http.MethodPut, "/api/deployment/agent-backends/infra-claude", body,
		map[string]string{"X-Grain-User": user})
	return rec.Code, rec.Body.String()
}

func TestConnectionCLIPathMustBeABinaryTheDaemonCanRun(t *testing.T) {
	srv, _ := cliPathTeam(t)
	dir := t.TempDir()
	notExecutable := filepath.Join(dir, "claude-noexec")
	if err := os.WriteFile(notExecutable, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pathDir := t.TempDir()
	executableIn(t, pathDir, "claude-beta")
	t.Setenv("PATH", pathDir)

	for _, tc := range []struct {
		name, body, want string
	}{
		{"relative path", `{"framework":"claude","cliPath":"bin/claude"}`, "absolute"},
		{"missing file", `{"framework":"claude","cliPath":"` + filepath.Join(dir, "nope") + `"}`, "not an executable"},
		{"not executable", `{"framework":"claude","cliPath":"` + notExecutable + `"}`, "not an executable"},
		{"directory", `{"framework":"claude","cliPath":"` + dir + `"}`, "not an executable"},
		{"bare name off PATH", `{"framework":"claude","cliPath":"claude-gamma"}`, "PATH"},
		{"custom backend", `{"framework":"custom","command":"run.sh","cliPath":"` + notExecutable + `"}`, "command"},
	} {
		code, out := putConnection(t, srv, "bob", tc.body)
		if code != http.StatusBadRequest || !strings.Contains(out, "cliPath") || !strings.Contains(out, tc.want) {
			t.Errorf("%s: code %d, body %s; want a 400 about cliPath saying %q", tc.name, code, out, tc.want)
		}
	}

	if code, out := putConnection(t, srv, "bob", `{"framework":"claude","cliPath":"claude-beta"}`); code != http.StatusOK {
		t.Fatalf("a bare name on the daemon's PATH: code %d: %s", code, out)
	}
	// Once saved, a path is not checked again on an unrelated edit: a
	// binary removed since fails that connection's runs, naming it, not
	// every save of the connection's name.
	t.Setenv("PATH", t.TempDir())
	if code, out := putConnection(t, srv, "bob", `{"framework":"claude","name":"Beta","cliPath":"claude-beta"}`); code != http.StatusOK {
		t.Fatalf("re-saving an unchanged CLI path: code %d: %s", code, out)
	}
}
