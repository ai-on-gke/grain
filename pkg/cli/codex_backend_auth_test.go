package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/daemon"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/agent/codex"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/secrets"
)

func TestCodexBackendAuthentication(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, mode, login       string
		key, wantKey, wantError bool
	}{
		{"subscription ignores keys", model.CodexAuthSubscription, "backend", true, false, false},
		{"subscription ignores the deployment's login", model.CodexAuthSubscription, "shared", true, false, true},
		{"subscription ignores the host's login", model.CodexAuthSubscription, "host", false, false, true},
		{"automatic preserves API precedence", "", "backend", true, true, false},
		{"automatic uses backend login", "", "backend", false, false, false},
		{"API mode uses key", model.CodexAuthAPIKey, "backend", true, true, false},
		{"API mode does not silently use subscription", model.CodexAuthAPIKey, "shared", false, false, true},
		{"subscription requires login", model.CodexAuthSubscription, "", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store, creds := testStore(t), testSecrets(t)
			cfg := daemon.Config{DataDir: t.TempDir(), CodexPath: codexVersionStub(t)}
			mc := model.DefaultConfig()
			mc.AgentBackends = []model.AgentBackend{{ID: "account", Framework: model.AgentFrameworkCodex, AuthMode: tc.mode, Models: []string{"gpt-5.1-codex"}}}
			if err := store.PutConfig(ctx, mc); err != nil {
				t.Fatal(err)
			}
			if tc.key {
				for _, name := range []string{secrets.OpenAIAPIKeySecret, secrets.AgentBackendCredentialSecret("account")} {
					if err := creds.Set(name, secrets.AgentCredentialKey, []byte("sk-test")); err != nil {
						t.Fatal(err)
					}
				}
			}
			var login string
			switch tc.login {
			case "backend":
				login = codex.BackendAuthFile(daemon.ManagedCodexAuthFile(cfg.DataDir), "account")
			case "shared":
				login = daemon.ManagedCodexAuthFile(cfg.DataDir)
			case "host":
				login = filepath.Join(t.TempDir(), "auth.json")
				cfg.CodexAuthFile = login
			}
			if login != "" {
				if err := codex.WriteAuthFile(login, []byte(`{"tokens":{"refresh_token":"fake"}}`)); err != nil {
					t.Fatal(err)
				}
			}
			f, err := daemon.AgentFrameworks(cfg, store, creds)(ctx, orchestrator.AgentChoice{Framework: "codex"})
			if tc.wantError {
				if err == nil {
					t.Fatal("missing credential unexpectedly accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			cmd, err := f.(agent.TerminalFramework).TerminalCommand(ctx, agent.RunConfig{SandboxRoot: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			defer cmd.Cleanup()
			var home, key string
			for _, env := range cmd.Env {
				if v, ok := strings.CutPrefix(env, "CODEX_HOME="); ok {
					home = v
				}
				if v, ok := strings.CutPrefix(env, "OPENAI_API_KEY="); ok {
					key = v
				}
			}
			if tc.wantKey {
				if key != "sk-test" {
					t.Fatal("expected API key in command environment")
				}
				if _, err := os.Lstat(filepath.Join(home, "auth.json")); !os.IsNotExist(err) {
					t.Fatal("API mode exposed subscription login")
				}
			} else {
				if key != "" {
					t.Fatal("subscription command received API key")
				}
				target, err := os.Readlink(filepath.Join(home, "auth.json"))
				if err != nil || target != login {
					t.Fatalf("auth target = %q, %v; want %q", target, err, login)
				}
			}
		})
	}
}

func TestCodexBackendLoginsRemainIndependent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testStore(t)
	cfg := daemon.Config{DataDir: t.TempDir(), CodexPath: codexVersionStub(t)}
	mc := model.DefaultConfig()
	for _, id := range []string{"one", "two"} {
		mc.AgentBackends = append(mc.AgentBackends, model.AgentBackend{ID: id, Framework: "codex", AuthMode: model.CodexAuthSubscription, Models: []string{"gpt-5.1-codex"}})
		if err := codex.WriteAuthFile(codex.BackendAuthFile(daemon.ManagedCodexAuthFile(cfg.DataDir), id), []byte(`{"account":"`+id+`"}`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutConfig(ctx, mc); err != nil {
		t.Fatal(err)
	}
	build := daemon.AgentFrameworks(cfg, store, testSecrets(t))
	for _, id := range []string{"one", "two", "one"} {
		f, err := build(ctx, orchestrator.AgentChoice{Framework: "codex"})
		if err != nil {
			t.Fatal(err)
		}
		cmd, err := f.(agent.TerminalFramework).TerminalCommand(ctx, agent.RunConfig{SandboxRoot: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		defer cmd.Cleanup()
		for _, env := range cmd.Env {
			if home, ok := strings.CutPrefix(env, "CODEX_HOME="); ok {
				target, err := os.Readlink(filepath.Join(home, "auth.json"))
				if err != nil || target != codex.BackendAuthFile(daemon.ManagedCodexAuthFile(cfg.DataDir), id) {
					t.Fatalf("backend %s used wrong login", id)
				}
			}
		}
	}
}
