// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
func NewPorter(dataDir, proxyURL string, store *model.Store, tokens *gitproxy.SandboxTokenStore, creds ...*gitproxy.CredentialSet) orchestrator.Porter {
	if proxyURL == "" || tokens == nil {
		return nil
	}
	var credSet *gitproxy.CredentialSet
	if len(creds) > 0 {
		credSet = creds[0]
	}
	if credSet == nil && dataDir != "" {
		credSet, _ = gitproxy.LoadCredentialSet(filepath.Join(dataDir, "secrets", "github"))
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
			var committerName, committerEmail, signingKey string
			if resolveID := credentialGitIdentity(credSet, store); resolveID != nil {
				if id, ok := resolveID(ctx, taskID, &repo); ok {
					committerName = id.Name
					committerEmail = id.Email
					signingKey = id.SigningKey
				}
			} else if credSet != nil {
				if credName := credSet.PrimaryAmbientCredential(); credName != "" {
					if cid, err := credSet.CredentialGitIdentity(credName); err == nil {
						committerName = cid.Name
						committerEmail = cid.Email
						signingKey = cid.SigningKey
					}
				}
			}
			log.Printf("porter: %s may fetch and push %s through the git proxy for this port (team %q)", identity, repo, team)
			return portpick.Remote{
				URL:            orchestrator.CloneURL(proxyURL, repo),
				Token:          token,
				CommitterName:  committerName,
				CommitterEmail: committerEmail,
				SigningKey:     signingKey,
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
