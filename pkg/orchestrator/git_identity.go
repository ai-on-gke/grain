package orchestrator

// Which git identity a run's commits carry (mcp.GitIdentity, written
// into the sandbox's .gitconfig by ConfigureGitCredentials): the
// account the run's git credential acts as (Deps.CredentialGitIdentity
// -- a PAT's own GitHub login, a GitHub App's bot account, an ambient
// connection's host login), then, for whichever half that leaves empty,
// the team's legacy commit author (model.Team.AgentGitName/
// AgentGitEmail, no longer offered in the UI), then grain's own default
// for whatever is still empty (mcp.GitIdentity.OrDefault, at the
// sandbox). runOne (cycle.go) and sandbox recreation (recreate.go) both
// go through runGitIdentity so the rebuilt sandbox commits as the first
// one did.

import (
	"context"
	"log"
	"strings"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// CredentialGitIdentityFunc answers the commit identity that goes with
// the git credential task taskID pushes target with
// (gitproxy.CredentialSet.CredentialGitIdentity): the account that
// credential's token acts as. ok is false for a task whose credential
// names nobody (anonymous, an SSH key with no API credential, a plain
// git host), whose target needs none, or whose lookup failed -- the
// caller then falls back to the deployment's legacy identity and the
// sandbox's default. Either half may be empty with ok true.
type CredentialGitIdentityFunc func(ctx context.Context, taskID string, target *model.RepoRef) (identity mcp.GitIdentity, ok bool)

// runGitIdentity is the identity task's run commits under: the
// credential's own where it names one, with any explicit AgentGitName or
// AgentGitEmail in model.Config overriding the corresponding half. What is
// still empty after that is the sandbox's to default.
func runGitIdentity(ctx context.Context, store *model.Store, fromCredential CredentialGitIdentityFunc, task *model.Task) mcp.GitIdentity {
	var id mcp.GitIdentity
	if task == nil {
		return id
	}
	if fromCredential != nil {
		if cred, ok := fromCredential(ctx, task.ID, task.Target); ok {
			id = cred
		}
	}
	derived := id.Name != "" || id.Email != ""
	if store != nil {
		if cfg, err := store.GetConfig(ctx); err == nil && cfg != nil {
			if name := strings.TrimSpace(cfg.AgentGitName); name != "" {
				id.Name = name
			}
			if email := strings.TrimSpace(cfg.AgentGitEmail); email != "" {
				id.Email = email
			}
		}
	}
	if derived {
		log.Printf("orchestrator: task %s commits as %s <%s>, the account its git credential acts as",
			task.ID, id.OrDefault().Name, id.OrDefault().Email)
	}
	return id
}
