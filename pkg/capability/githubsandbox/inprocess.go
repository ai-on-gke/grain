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

package githubsandbox

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

// ServerID is the in-process github server's capability_id, and
// MethodName its sandbox-repo method, which github-sandbox grants became
// (schema migration 103, model.GitHubSandboxRepoGrant).
const (
	ServerID   = model.GitHubCapabilityServerID
	MethodName = model.GitHubSandboxRepoMethod
)

// current is the provider the daemon configured (Configure), built
// afresh whenever its configuration changes; until then the default
// one, on github.com.
var current atomic.Pointer[Provider]

// Configure gives the sandbox-repo method the deployment's GitHub host.
// The daemon calls it whenever it builds its capability providers, since
// the methods are registered once.
func Configure(cfg Config) { current.Store(NewProvider(cfg)) }

// Current is the provider sandbox-repo mints through: what the daemon's
// hourly sweep reaps leaked sandbox repositories with (model.Reaper) and
// Settings checks the App's credentials with, now that no registry holds
// it.
func Current() *Provider {
	if p := current.Load(); p != nil {
		return p
	}
	return NewProvider(Config{})
}

// Method is github#sandbox-repo as grain answers it in-process: what
// capability.Register serves on ServerID.
//
// Each mint is the retired Provider's own Resolve, Materialize and
// PromptSection for the run the mint's context names (model.MintRunID),
// with the secrets resolver its context carries (model.MintCredentials):
// the same refusal on a deployment with no GitHub App, the same
// grain-sandbox-<run> repository on the App's one installation, the same
// two files with the same content and the same prompt. The pass's
// release revokes the lease -- deletes the repository -- as the
// provider's Revoke did when the run ended, and the daemon's sweep still
// reaps any a lost pass leaked (Current).
func Method() capability.Method {
	type held struct {
		p     *Provider
		creds model.CredentialResolver
		lease *model.Lease
		resp  *protocol.MintResponse
	}
	var mu sync.Mutex
	leases := map[string]held{}
	return capability.Method{
		Server:      ServerID,
		Name:        "GitHub sandbox",
		Description: "Create a private, single-use GitHub sandbox repo for this task",
		Spec: protocol.MethodSpec{
			Name: MethodName,
			Description: "Create a private, single-use GitHub repository for this run with this deployment's " +
				"GitHub App, and place a token scoped to only that repository (formerly github-sandbox). " +
				"The repository is deleted when the run ends.",
			Args: map[string]any{"type": "object", "additionalProperties": false},
			// The orchestrator set the pass's deadline from its own
			// clock, so there is no second one to hold it to.
			Now: func() time.Time { return time.Time{} },
			Mint: func(ctx context.Context, attemptID string, deadline time.Time, _ map[string]any) (*protocol.MintResponse, error) {
				// A renewal (protocol.Pass.Renew) mints the same attempt
				// again: the run keeps the repository and token it was
				// given.
				mu.Lock()
				h, renewing := leases[attemptID]
				mu.Unlock()
				if renewing {
					return h.resp, nil
				}
				runID := model.MintRunID(ctx)
				if runID == "" {
					runID = attemptID
				}
				// A nil resolver is refused by the provider's own Resolve,
				// in its own words.
				creds := model.MintCredentials(ctx)
				p := Current()
				cc := model.CapabilityContext{Run: model.Run{ID: runID}, Now: time.Now().UTC(), Deadline: deadline, Credentials: creds}
				// The secret store's audit names the run, as it did for
				// the provider (model.ResolveGrants).
				ctx = secrets.WithRun(ctx, runID)
				res, err := p.Resolve(ctx, cc)
				if err != nil {
					return nil, err
				}
				if res.Refused {
					return &protocol.MintResponse{Status: protocol.StatusRefused, Reason: res.Reason}, nil
				}
				mat, err := p.Materialize(ctx, cc)
				if err != nil {
					return nil, err
				}
				prompt, err := p.PromptSection(ctx, cc, mat.Placements)
				if err != nil {
					if mat.Lease != nil {
						_ = p.Revoke(ctx, cc, *mat.Lease)
					}
					return nil, err
				}
				resp := &protocol.MintResponse{Status: protocol.StatusOK, PromptSection: prompt}
				for _, pl := range mat.Placements {
					resp.Placements = append(resp.Placements, protocol.Placement{Path: pl.Path, Mode: "0" + pl.EffectiveMode(), Content: pl.Content})
				}
				mu.Lock()
				leases[attemptID] = held{p: p, creds: creds, lease: mat.Lease, resp: resp}
				mu.Unlock()
				return resp, nil
			},
			Release: func(ctx context.Context, attemptID string) error {
				mu.Lock()
				h, ok := leases[attemptID]
				delete(leases, attemptID)
				mu.Unlock()
				if !ok || h.lease == nil {
					return nil
				}
				return h.p.Revoke(ctx, model.CapabilityContext{Now: time.Now(), Credentials: h.creds}, *h.lease)
			},
		},
	}
}

func init() { capability.Register(Method()) }
