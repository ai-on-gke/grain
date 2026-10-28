package cli

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

// withDefaultTeamBackends gives the default team one connection on each
// framework, for a test about building a framework rather than about
// which connection is picked: a team runs only on its own connections
// (grain/task-1601), so a config with none dispatches nothing at all.
// A config that already names connections is left as it is.
func withDefaultTeamBackends(cfg daemon.Config) daemon.Config {
	if len(cfg.AgentBackends) > 0 {
		return cfg
	}
	// Each lists a model: grain has no default model of its own, and a
	// connection with none waits rather than guessing one.
	for _, b := range []struct{ framework, served string }{
		{model.AgentFrameworkAntigravity, "gemini-3.1-pro"},
		{model.AgentFrameworkClaude, "claude-sonnet-5"},
		{model.AgentFrameworkCodex, "gpt-5.1-codex"},
	} {
		framework, served := b.framework, b.served
		cfg.AgentBackends = append(cfg.AgentBackends, model.AgentBackend{ID: defaultTeamBackendID(framework), Framework: framework, Team: model.DefaultTeamID, Models: []string{served}})
	}
	return cfg
}

// defaultTeamBackendID is the ID withDefaultTeamBackends gives the
// default team's connection on framework.
func defaultTeamBackendID(framework string) string {
	return model.NormalizeAgentFrameworkName(framework) + "-default"
}

// setDefaultTeamBackendKey stores value as the key of the default team's
// connection on framework: a connection runs on its own key and never on
// the deployment's (grain/task-1656).
func setDefaultTeamBackendKey(t *testing.T, store *secrets.Store, framework, value string) {
	t.Helper()
	if err := store.Set(secrets.AgentBackendCredentialSecret(defaultTeamBackendID(framework)), secrets.AgentCredentialKey, []byte(value)); err != nil {
		t.Fatal(err)
	}
}

// unwrapBackend is the framework a connection's backendFramework wraps,
// or f itself when it is not one.
func unwrapBackend(f agent.Framework) agent.Framework {
	if pooled, ok := f.(*daemon.BackendFramework); ok {
		return pooled.Framework
	}
	return f
}
