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

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

// ServerID is the in-process github server's capability_id, and
// MethodName its sandbox-repo method, which github-sandbox grants became
// (schema migration 124, model.GitHubSandboxRepoGrant).
const (
	ServerID   = model.GitHubCapabilityServerID
	MethodName = model.GitHubSandboxRepoMethod
)

// current is the provider the daemon configured (Configure), built
// afresh whenever its configuration changes; until then the default
// one, on github.com.
var current atomic.Pointer[Provider]

// Configure gives the sandbox-repo method the deployment's GitHub host.
// The daemon calls it whenever it builds its capability providers, and
// tests use the returned restore function to reset package state.
func Configure(cfg Config) func() {
	return SetCurrent(NewProvider(cfg))
}

// SetCurrent installs p as the active sandbox-repo provider and returns
// a restore function resetting the previous provider.
func SetCurrent(p *Provider) func() {
	prev := current.Swap(p)
	return func() { current.Store(prev) }
}

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

type heldLease struct {
	p       *Provider
	creds   model.CredentialResolver
	ci      bool
	auditID string
	lease   *model.Lease
	resp    *protocol.MintResponse
}

var (
	leasesMu sync.Mutex
	leases   = map[string]heldLease{}
)

// MethodSpec is github#sandbox-repo on grainserver.GitHubServer.
//
// Each mint is the retired Provider's own Resolve, Materialize and
// PromptSection for the task run (model.MintRunID, model.MintCredentials)
// or CI job (model.CIMintFrom) in the mint's context: the same refusal on
// a deployment with no GitHub App, a grain-sandbox-<attempt> repository
// on the App's one installation, the same two files with the same content
// and mode, and the same prompt. Releasing the pass revokes the lease --
// deletes the repository -- as the provider's Revoke did when the run
// ended, and the daemon's sweep still reaps any a lost pass leaked
// (Current).
func MethodSpec() protocol.MethodSpec {
	return protocol.MethodSpec{
		Name: MethodName,
		Description: "Create a private, single-use GitHub repository for this run with this deployment's " +
			"GitHub App, and place a token scoped to only that repository (formerly github-sandbox). " +
			"The repository is deleted when the run ends.",
		Args: map[string]any{"type": "object", "additionalProperties": false},
		// The orchestrator and CI engine set the pass's deadline from their
		// own clocks, so there is no second clock to hold it to.
		Now: func() time.Time { return time.Time{} },
		Mint: func(ctx context.Context, attemptID string, deadline time.Time, _ map[string]any) (*protocol.MintResponse, error) {
			// A renewal (protocol.Pass.Renew) mints the same attempt again:
			// the run keeps the repository and token it was given.
			leasesMu.Lock()
			h, renewing := leases[attemptID]
			leasesMu.Unlock()
			if renewing {
				return h.resp, nil
			}

			var (
				auditID string
				creds   model.CredentialResolver
				repo    *model.RepoRef
				isCI    bool
			)
			if job, ok := model.CIMintFrom(ctx); ok {
				auditID = job.RunID
				creds = job.Credentials
				r := job.Repo
				repo = &r
				isCI = true
			} else {
				auditID = model.MintRunID(ctx)
				creds = model.MintCredentials(ctx)
			}
			if auditID == "" {
				auditID = attemptID
			}

			p := Current()
			now := time.Now().UTC()
			cc := model.CapabilityContext{
				Task: model.Task{Target: repo},
				// The repository is named for the attempt (repoName), not the
				// run: an attempt ID carries the deployment, task, attempt and
				// grant index (protocol.AttemptID), so two deployments sharing
				// one GitHub App installation cannot both claim one
				// grain-sandbox-<run> name for runs that share an ID.
				Run:         model.Run{ID: attemptID, Sandbox: attemptID, StartedAt: now},
				Now:         now,
				Deadline:    deadline,
				Credentials: creds,
				CI:          isCI,
			}
			// The secret store's audit names the run, as it did for the
			// provider (model.ResolveGrants).
			ctx = secrets.WithRun(ctx, auditID)
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
				resp.Placements = append(resp.Placements, protocol.Placement{
					Path:    pl.Path,
					Mode:    "0" + pl.EffectiveMode(),
					Content: pl.Content,
				})
			}
			leasesMu.Lock()
			leases[attemptID] = heldLease{p: p, creds: creds, ci: isCI, auditID: auditID, lease: mat.Lease, resp: resp}
			leasesMu.Unlock()
			return resp, nil
		},
		Release: func(ctx context.Context, attemptID string) error {
			leasesMu.Lock()
			h, ok := leases[attemptID]
			delete(leases, attemptID)
			leasesMu.Unlock()
			if !ok || h.lease == nil {
				return nil
			}
			// Deleting the repository reads the App's key from the secret
			// store too, so its audit names the run the mint did.
			if h.auditID != "" {
				ctx = secrets.WithRun(ctx, h.auditID)
			}
			return h.p.Revoke(ctx, model.CapabilityContext{Now: time.Now().UTC(), Credentials: h.creds, CI: h.ci}, *h.lease)
		},
	}
}
