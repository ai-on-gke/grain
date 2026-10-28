package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/agent/codex"
	"github.com/bwsalmon/grain/pkg/daemon"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/secrets"
)

// codexStubIn writes a codex that answers --version as the reviewed CLI
// into dir, and returns its path.
func codexStubIn(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "codex")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho 'codex-cli "+codex.ReviewedCLIVersion+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// terminalPath builds the next codex run and answers the binary it would
// exec, with the connection it was built on.
func terminalPath(t *testing.T, build func(context.Context, orchestrator.AgentChoice) (agent.Framework, error)) (backendID, path string) {
	t.Helper()
	ctx := context.Background()
	framework, err := build(ctx, orchestrator.AgentChoice{Framework: model.AgentFrameworkCodex})
	if err != nil {
		t.Fatalf("building codex: %v", err)
	}
	cmd, err := framework.(agent.TerminalFramework).TerminalCommand(ctx, agent.RunConfig{SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("TerminalCommand: %v", err)
	}
	t.Cleanup(cmd.Cleanup)
	return framework.(*daemon.BackendFramework).Id, cmd.Path
}

// Two connections of one framework run two different builds of its CLI,
// and a third with no path of its own runs the deployment's -codex-path:
// connection, then flag.
func TestAgentFrameworksRunEachConnectionsOwnCLI(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testStore(t)
	secretStore := testSecrets(t)
	pinned, trial, flag := codexStubIn(t, t.TempDir()), codexStubIn(t, t.TempDir()), codexStubIn(t, t.TempDir())
	backends := []model.AgentBackend{
		{ID: "codex-pinned", Framework: model.AgentFrameworkCodex, CLIPath: pinned, Models: []string{"gpt-5.1-codex"}},
		{ID: "codex-trial", Framework: model.AgentFrameworkCodex, CLIPath: trial, Models: []string{"gpt-5.1-codex"}},
		{ID: "codex-default", Framework: model.AgentFrameworkCodex, Models: []string{"gpt-5.1-codex"}},
	}
	if err := store.PutConfig(ctx, model.Config{AgentBackends: backends}); err != nil {
		t.Fatal(err)
	}
	for _, b := range backends {
		if err := secretStore.Set(secrets.AgentBackendCredentialSecret(b.ID), secrets.AgentCredentialKey, []byte("sk-"+b.ID)); err != nil {
			t.Fatal(err)
		}
	}
	build := daemon.AgentFrameworks(daemon.Config{CodexPath: flag}, store, secretStore)
	want := map[string]string{"codex-pinned": pinned, "codex-trial": trial, "codex-default": flag}
	got := map[string]string{}
	for range backends {
		id, path := terminalPath(t, build)
		got[id] = path
	}
	for id, path := range want {
		if got[id] != path {
			t.Errorf("connection %s ran %q, want %q (all: %v)", id, got[id], path, got)
		}
	}
}

// With neither a connection path nor a flag, the daemon's PATH decides,
// exactly as before connections could carry one.
func TestAgentFrameworksFallBackToPATHWithoutAConnectionPath(t *testing.T) {
	dir := t.TempDir()
	onPath := codexStubIn(t, dir)
	t.Setenv("PATH", dir)
	ctx := context.Background()
	store := testStore(t)
	secretStore := testSecrets(t)
	if err := store.PutConfig(ctx, model.Config{AgentBackends: []model.AgentBackend{{ID: "codex-a", Framework: model.AgentFrameworkCodex, Models: []string{"gpt-5.1-codex"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := secretStore.Set(secrets.AgentBackendCredentialSecret("codex-a"), secrets.AgentCredentialKey, []byte("sk-a")); err != nil {
		t.Fatal(err)
	}
	if _, path := terminalPath(t, daemon.AgentFrameworks(daemon.Config{}, store, secretStore)); path != onPath {
		t.Errorf("ran %q, want PATH's %q", path, onPath)
	}
}

// A connection whose binary has gone since it was saved fails its own
// runs naming the connection -- not as "not installed, set -claude-path",
// which would send the operator to the wrong place.
func TestAgentFrameworksBlameAMissingConnectionCLIOnTheConnection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testStore(t)
	secretStore := testSecrets(t)
	missing := filepath.Join(t.TempDir(), "claude-gone")
	if err := store.PutConfig(ctx, model.Config{AgentBackends: []model.AgentBackend{
		{ID: "claude-old", Framework: model.AgentFrameworkClaude, CLIPath: missing, Models: []string{"claude-sonnet-5"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := secretStore.Set(secrets.AgentBackendCredentialSecret("claude-old"), secrets.AgentCredentialKey, []byte("sk-ant-oat01-fake")); err != nil {
		t.Fatal(err)
	}
	cfg := daemon.Config{ClaudePath: filepath.Join(t.TempDir(), "claude")}
	_, err := daemon.AgentFrameworks(cfg, store, secretStore)(ctx, orchestrator.AgentChoice{Framework: model.AgentFrameworkClaude})
	if err == nil {
		t.Fatal("built a claude run on a connection whose CLI path does not exist")
	}
	for _, want := range []string{"claude-old", missing, "Settings -> Models"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v; want it to name %q", err, want)
		}
	}
}

// codexCatalogStub is a codex app-server answering with one model, id.
func codexCatalogStub(t *testing.T, id string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "codex")
	script := `#!/bin/sh
test "$1" = app-server || exit 1
read -r line
echo '{"id":1,"result":{}}'
read -r line
read -r line
echo '{"id":2,"result":{"data":[{"id":"` + id + `","model":"` + id + `","displayName":"` + id + `"}],"nextCursor":null}}'
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return binary
}

// Discovery asks each connection's own binary, so two connections of one
// framework can report two different model lists.
func TestCodexModelListerAsksEachConnectionsOwnCLI(t *testing.T) {
	t.Parallel()
	creds := testSecrets(t)
	for _, id := range []string{"old", "new"} {
		if err := creds.Set(secrets.AgentBackendCredentialSecret(id), secrets.AgentCredentialKey, []byte("sk-"+id)); err != nil {
			t.Fatal(err)
		}
	}
	lister := &daemon.CodexModelLister{Live: testLiveConfig(daemon.Config{
		CodexPath: codexCatalogStub(t, "flag-model"),
		AgentBackends: []model.AgentBackend{
			{ID: "old", Framework: model.AgentFrameworkCodex, AuthMode: model.CodexAuthAPIKey, CLIPath: codexCatalogStub(t, "old-model")},
			{ID: "new", Framework: model.AgentFrameworkCodex, AuthMode: model.CodexAuthAPIKey, CLIPath: codexCatalogStub(t, "new-model")},
		},
	}), Creds: creds}
	for id, want := range map[string]string{"old": "old-model", "new": "new-model"} {
		got, err := lister.ListModels(context.Background(), id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if len(got.Models) != 1 || got.Models[0].ID != want {
			t.Errorf("%s listed %+v, want %s", id, got.Models, want)
		}
	}
}

// And a connection whose binary is missing says so, naming the
// connection, rather than falling back to the deployment's binary and
// listing models it would not run.
func TestCodexModelListerDoesNotFallBackPastAConnectionPath(t *testing.T) {
	t.Parallel()
	creds := testSecrets(t)
	if err := creds.Set(secrets.AgentBackendCredentialSecret("gone"), secrets.AgentCredentialKey, []byte("sk-gone")); err != nil {
		t.Fatal(err)
	}
	lister := &daemon.CodexModelLister{Live: testLiveConfig(daemon.Config{
		CodexPath: codexCatalogStub(t, "flag-model"),
		AgentBackends: []model.AgentBackend{
			{ID: "gone", Framework: model.AgentFrameworkCodex, AuthMode: model.CodexAuthAPIKey, CLIPath: filepath.Join(t.TempDir(), "codex")},
		},
	}), Creds: creds}
	if got, err := lister.ListModels(context.Background(), "gone"); err == nil || !strings.Contains(err.Error(), "gone") {
		t.Fatalf("ListModels = %+v, %v; want an error naming the connection", got, err)
	}
}

// Discovery reads the connections as stored, the way a dispatch does,
// not only what the daemon applied at its last reconcile tick: the
// connection page asks for models the moment a connection is saved, and
// until the next tick (10s by default) a new connection was asked of
// PATH's codex ("the Codex CLI is not installed") and a changed CLIPath
// of the old binary (grain/task-3945).
func TestCodexModelListerAsksAConnectionsCLIBeforeTheNextTick(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testStore(t)
	creds := testSecrets(t)
	for _, id := range []string{"saved-now", "repointed"} {
		if err := creds.Set(secrets.AgentBackendCredentialSecret(id), secrets.AgentCredentialKey, []byte("sk-"+id)); err != nil {
			t.Fatal(err)
		}
	}
	// What the daemon applied at its last tick: "repointed" on its old
	// binary, and "saved-now" not saved yet.
	live := daemon.NewLiveConfig(store, nil, daemon.Config{AgentBackends: []model.AgentBackend{
		{ID: "repointed", Framework: model.AgentFrameworkCodex, AuthMode: model.CodexAuthAPIKey, CLIPath: codexCatalogStub(t, "old-binary-model")},
	}}, nil)
	if err := store.PutConfig(ctx, model.Config{AgentBackends: []model.AgentBackend{
		{ID: "repointed", Framework: model.AgentFrameworkCodex, AuthMode: model.CodexAuthAPIKey, CLIPath: codexCatalogStub(t, "new-binary-model")},
		{ID: "saved-now", Framework: model.AgentFrameworkCodex, AuthMode: model.CodexAuthAPIKey, CLIPath: codexCatalogStub(t, "saved-now-model")},
	}}); err != nil {
		t.Fatal(err)
	}
	lister := &daemon.CodexModelLister{Live: live, Creds: creds}
	for id, want := range map[string]string{"saved-now": "saved-now-model", "repointed": "new-binary-model"} {
		got, err := lister.ListModels(ctx, id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if len(got.Models) != 1 || got.Models[0].ID != want {
			t.Errorf("%s listed %+v, want %s from the CLI it was saved with", id, got.Models, want)
		}
	}
}
