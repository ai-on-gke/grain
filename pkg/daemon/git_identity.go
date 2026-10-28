package daemon

import (
	"context"
	"errors"
	"log"

	"github.com/bwsalmon/grain/pkg/gitproxy"
	"github.com/bwsalmon/grain/pkg/mcp"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// credentialGitIdentity is orchestrator.Deps.CredentialGitIdentity over
// creds and store: for a task whose git credential for its target
// (model.Store.TaskGitCredentialOn -- the task's own grant, then its
// team's connection for that repository, owner or host), the commit
// identity of the account that credential acts as
// (gitproxy.CredentialSet.CredentialGitIdentity): a PAT's own GitHub
// login, a GitHub App's bot account, an SSH key's paired API
// credential's, or -- for an ambient credential -- this host's own login.
//
// The connection is the deployment's whichever team the task belongs to,
// and there is no credentials.json ladder behind it -- the same answer
// the git proxy pushes with (docs/single-tenant-reset.md, stage 1).
//
// A task with no target, a credential that names nobody (anonymous, an
// unpaired SSH key, a plain git host), and a lookup that fails all
// answer false: the sandbox then commits as the team's legacy identity
// or grain's own default. An error is logged rather than returned,
// since a wrong author is a thing to fix on the pull request and not a
// reason to refuse the run its sandbox.
func credentialGitIdentity(creds *gitproxy.CredentialSet, store *model.Store) orchestrator.CredentialGitIdentityFunc {
	if creds == nil || store == nil {
		return nil
	}
	return func(ctx context.Context, taskID string, target *model.RepoRef) (mcp.GitIdentity, bool) {
		if target == nil || target.Owner == "" || target.Name == "" {
			return mcp.GitIdentity{}, false
		}
		name, err := store.TaskGitCredentialOn(ctx, taskID, target.Host, target.Owner, target.Name)
		if err != nil {
			if !errors.Is(err, model.ErrNoTeamGitCredential) {
				log.Printf("grain daemon: resolving task %s's git credential for its commit identity: %v", taskID, err)
			}
			return mcp.GitIdentity{}, false
		}
		if name == "" || name == "anonymous" {
			return mcp.GitIdentity{}, false
		}
		id, err := creds.CredentialGitIdentity(name)
		if err != nil {
			log.Printf("grain daemon: task %s's commit identity could not be read from its git credential %s: %v; it commits as its team's or grain's own default instead",
				taskID, name, err)
			return mcp.GitIdentity{}, false
		}
		return mcp.GitIdentity{Name: id.Name, Email: id.Email}, !id.Empty()
	}
}
