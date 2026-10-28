package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/agent/claude"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

func TestClaudeModelListerListsWithTheBackendsOwnToken(t *testing.T) {
	creds := testSecrets(t)
	if err := creds.Set(secrets.AgentBackendCredentialSecret("account"), secrets.AgentCredentialKey, []byte("sk-ant-oat-account")); err != nil {
		t.Fatal(err)
	}
	var asked string
	lister := &daemon.ClaudeModelLister{
		Live: testLiveConfig(daemon.Config{
			AgentBackends: []model.AgentBackend{{ID: "account", Framework: model.AgentFrameworkClaude}},
		}),
		Creds: creds,
		List: func(_ context.Context, token string) ([]claude.Model, error) {
			asked = token
			return []claude.Model{{ID: "claude-opus-5", Label: "Claude Opus 5"}}, nil
		},
	}

	got, err := lister.ListModels(context.Background(), "account")
	if err != nil {
		t.Fatal(err)
	}
	if asked != "sk-ant-oat-account" {
		t.Errorf("listed as %q, want this backend's own credential", asked)
	}
	if len(got.Models) != 1 || got.Models[0].ID != "claude-opus-5" || got.Models[0].Label != "Claude Opus 5" {
		t.Fatalf("catalog: %+v", got)
	}
	if len(got.Efforts) != 0 {
		t.Errorf("efforts = %v, want none: claude takes no effort flag", got.Efforts)
	}
}

// With nothing to authenticate as there is no listing to make, and the
// sentence has to name what to set rather than leaving a refusal from
// Anthropic to explain grain's own configuration.
func TestClaudeModelListerNamesTheMissingToken(t *testing.T) {
	lister := &daemon.ClaudeModelLister{
		Live: testLiveConfig(daemon.Config{
			AgentBackends: []model.AgentBackend{{ID: "account", Framework: model.AgentFrameworkClaude}},
		}),
		Creds: testSecrets(t),
		List: func(context.Context, string) ([]claude.Model, error) {
			return nil, errors.New("asked Anthropic with no credential")
		},
	}

	_, err := lister.ListModels(context.Background(), "account")
	if err == nil || !strings.Contains(err.Error(), "Claude Code OAuth token") {
		t.Fatalf("error = %v, want the missing token named", err)
	}
}

func TestClaudeModelListerReturnsVertexModelsWithoutToken(t *testing.T) {
	lister := &daemon.ClaudeModelLister{
		Live: testLiveConfig(daemon.Config{
			AgentBackends: []model.AgentBackend{{
				ID:        "vertex-account",
				Framework: model.AgentFrameworkClaude,
				AuthMode:  model.ClaudeAuthVertex,
			}},
		}),
		Creds: testSecrets(t),
	}

	got, err := lister.ListModels(context.Background(), "vertex-account")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(got.Models) == 0 {
		t.Fatal("got 0 models for vertex Claude backend, want standard vertex models")
	}
	found := false
	for _, m := range got.Models {
		if m.ID == "claude-sonnet-5" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("got models %v, want claude-sonnet-5 in vertex catalog", got.Models)
	}
}
