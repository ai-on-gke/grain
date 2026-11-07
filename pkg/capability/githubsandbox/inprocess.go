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

// ServerID is the canonical capability_id of the in-process GitHub capability
// server, and MethodName its sandbox-repo method, which the retired
// github-sandbox provider became (design/design/unified-grants.design.md §4,
// schema migration 120).
const (
	ServerID   = model.GitHubCapabilityServerID
	MethodName = model.GitHubSandboxRepoMethod
)

// SandboxRepoRule declares that a CI job receives #sandbox-repo only when the
// repository's CI capabilities list grants it (repo_config.ci_capabilities),
// narrowed by the job's own declaration -- while keeping #sandbox-repo
// available to task runs too (CIOnly is false).
var SandboxRepoRule = model.CIGrantRule{RepoGranted: true}

var configuredProvider atomic.Pointer[Provider]

// Configure sets the deployment's GitHub API host and transport settings for
// the in-process #sandbox-repo method and its reaper.
func Configure(cfg Config) {
	configuredProvider.Store(NewProvider(cfg))
}

// ConfigureProvider sets p as the active #sandbox-repo provider until the
// returned restore func is called. Intended for tests substituting a fake
// GitHub App client.
func ConfigureProvider(p *Provider) (restore func()) {
	prev := configuredProvider.Swap(p)
	return func() {
		configuredProvider.Store(prev)
	}
}

func currentProvider() *Provider {
	if p := configuredProvider.Load(); p != nil {
		return p
	}
	return NewProvider(Config{})
}

// Method returns the in-process https://grain.dev/capabilities/github#sandbox-repo
// method, as capability.Register serves it.
func Method() capability.Method {
	type held struct {
		p       *Provider
		creds   model.CredentialResolver
		ci      bool
		auditID string
		lease   *model.Lease
		resp    *protocol.MintResponse
	}
	var mu sync.Mutex
	leases := map[string]held{}
	return capability.Method{
		Server: ServerID,
		Name:   "GitHub sandbox",
		CI:     &SandboxRepoRule,
		Reap: func(ctx context.Context, creds model.CredentialResolver, before time.Time) ([]string, error) {
			return currentProvider().Reap(ctx, creds, before)
		},
		Spec: protocol.MethodSpec{
			Name:        MethodName,
			Description: "Create a private, single-use GitHub sandbox repo for this run",
			Args:        map[string]any{"type": "object", "additionalProperties": false},
			// The pass's deadline comes from grain's clock (task or CI pass).
			Now: func() time.Time { return time.Time{} },
			Mint: func(ctx context.Context, attemptID string, deadline time.Time, _ map[string]any) (*protocol.MintResponse, error) {
				mu.Lock()
				h, renewing := leases[attemptID]
				mu.Unlock()
				if renewing {
					return h.resp, nil
				}

				now := time.Now().UTC()
				p := currentProvider()
				cc := model.CapabilityContext{
					Run:      model.Run{ID: attemptID, Sandbox: attemptID, StartedAt: now},
					Now:      now,
					Deadline: deadline,
				}
				var auditID string
				if job, isCI := model.CIMintFrom(ctx); isCI {
					repo := job.Repo
					cc.Task = model.Task{Target: &repo}
					cc.Credentials = job.Credentials
					cc.CI = true
					auditID = job.RunID
				} else {
					cc.Credentials = model.MintCredentials(ctx)
					auditID = model.MintRunID(ctx)
				}
				if auditID == "" {
					auditID = attemptID
				}
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
				resp := &protocol.MintResponse{
					Status:        protocol.StatusOK,
					PromptSection: prompt,
				}
				for _, pl := range mat.Placements {
					resp.Placements = append(resp.Placements, protocol.Placement{
						Path:    pl.Path,
						Mode:    "0" + pl.EffectiveMode(),
						Content: pl.Content,
					})
				}
				mu.Lock()
				leases[attemptID] = held{
					p:       p,
					creds:   cc.Credentials,
					ci:      cc.CI,
					auditID: auditID,
					lease:   mat.Lease,
					resp:    resp,
				}
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
				if h.auditID != "" {
					ctx = secrets.WithRun(ctx, h.auditID)
				}
				return h.p.Revoke(ctx, model.CapabilityContext{
					Now:         time.Now().UTC(),
					CI:          h.ci,
					Credentials: h.creds,
				}, *h.lease)
			},
		},
	}
}

func init() {
	capability.Register(Method())
}
