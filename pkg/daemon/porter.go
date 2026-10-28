package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"path/filepath"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/portpick"
)

// NewPorter is the mechanical porter the orchestrator tries before it
// files a port as an agent task (orchestrator.Config.Porter, pkg/portpick).
//
// It reaches GitHub the way every sandbox does: through this
// deployment's own git proxy, so every fetch and push it makes is in the
// proxy's audit log beside everybody else's. Each attempt is its own
// proxy identity -- a token minted for a fresh name, granted write to
// the one repository being ported in (the same WriteScopes grant a
// repo-write CI job holds), and, for a team's task, sent out with that
// team's credential (SandboxTeams). All three are dropped when the
// attempt ends. What it may push is held by portpick itself: one new
// branch, no force, never either branch of the pair.
//
// A deployment with no proxy URL or no token store gets nil, which files
// every port as an agent task as before.
func NewPorter(dataDir, proxyURL string, store *model.Store, tokens *gitproxy.SandboxTokenStore) orchestrator.Porter {
	if proxyURL == "" || tokens == nil {
		return nil
	}
	return &portpick.Porter{
		Dir:            filepath.Join(dataDir, "port-cache"),
		CommitterName:  "grain",
		CommitterEmail: "grain@users.noreply.github.com",
		Remote: func(ctx context.Context, repo model.RepoRef, taskID string) (portpick.Remote, error) {
			team := ""
			if store != nil && store.SingleTenant {
				team = model.DefaultTeamID
			}
			suffix := make([]byte, 6)
			if _, err := rand.Read(suffix); err != nil {
				return portpick.Remote{}, err
			}
			identity := "port-" + hex.EncodeToString(suffix)
			token, err := tokens.EnsureToken(identity)
			if err != nil {
				return portpick.Remote{}, fmt.Errorf("minting a git proxy token: %w", err)
			}
			CiWriteScopes.Grant(identity, repo)
			if team != "" {
				CiSandboxTeams.Grant(identity, team)
			}
			log.Printf("porter: %s may fetch and push %s through the git proxy for this port (team %q)", identity, repo, team)
			return portpick.Remote{
				URL:   orchestrator.CloneURL(proxyURL, repo),
				Token: token,
				Release: func() {
					CiWriteScopes.Drop(identity)
					if team != "" {
						CiSandboxTeams.Drop(identity)
					}
					if err := tokens.Revoke(identity); err != nil {
						log.Printf("porter: revoking %s's git proxy token: %v", identity, err)
					}
				},
			}, nil
		},
	}
}
