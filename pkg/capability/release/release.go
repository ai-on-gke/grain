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

// Package release is the in-process https://grain.dev/capabilities/release
// server: the CI credentials a repository's release publishes and signs
// with, which the retired image-signing, registry-push and package-publish
// providers placed (design/design/unified-grants.design.md, task 1061).
//
// Each method's mint is the retired provider's own Resolve and
// Materialize, for the CI job the mint's context names (model.CIMint):
// the same refusals, the same files with the same content, and for
// package-publish the same refresh route and lease, revoked when the
// job's pass is released.
//
// When each may be handed out is not the deployment's to decide, so it
// is declared here, in code (capability.Method.CI), and grain enforces it
// ahead of the capability policy (model.CIGrantRule): every method is
// CI-only; image-signing answers the release event alone, registry-push
// and package-publish release and workflow_dispatch, and package-publish
// also the schedule run grain itself fired -- the retired providers'
// SupportsCIEvent and SupportsTrustedSchedule. All three are granted by
// a repository's CI capabilities list, which an operator edits, and
// narrowed by a job's declaration, as the providers were.
package release

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability"
	"github.com/ai-on-gke/grain/pkg/capability/imagesigning"
	"github.com/ai-on-gke/grain/pkg/capability/packagepublish"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/capability/registrypush"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

// ServerID is the release server's capability_id.
const ServerID = model.ReleaseCapabilityServerID

// The rules each method declares (model.CIGrantRule).
var (
	ImageSigningRule = model.CIGrantRule{Events: []string{"release"}, RepoGranted: true}
	RegistryPushRule = model.CIGrantRule{Events: []string{"release", "workflow_dispatch"}, RepoGranted: true}
	// PackagePublishRule also admits grain's own scheduled run, as
	// packagepublish.Provider.SupportsTrustedSchedule does.
	PackagePublishRule = model.CIGrantRule{Events: []string{"release", "workflow_dispatch"}, TrustedSchedule: true, RepoGranted: true}
)

// packagePublish is the package-publish provider the daemon configured
// (Configure), built afresh whenever its configuration changes; nil
// until then, which refuses every mint as a deployment resolving no
// GitHub credentials does.
var packagePublish atomic.Pointer[packagepublish.Provider]

// Configure gives the package-publish method the deployment's
// configuration: its credential ladder and refresh route. The daemon
// calls it whenever it builds its capability providers, since the
// ladder is live configuration and the methods are registered once.
func Configure(cfg packagepublish.Config) {
	packagePublish.Store(packagepublish.New(cfg))
}

// provider is a retired in-process provider a method mints through.
type provider interface {
	Resolve(context.Context, model.CapabilityContext) (model.Resolution, error)
	Materialize(context.Context, model.CapabilityContext) (model.Materialization, error)
	Revoke(context.Context, model.CapabilityContext, model.Lease) error
}

// Methods are the release server's three methods, as capability.Register
// takes them.
func Methods() []capability.Method {
	signing := imagesigning.New(imagesigning.Config{})
	push := registrypush.New()
	return []capability.Method{
		method(imagesigning.CapabilityName, "Image signing",
			"Sign this repository's release images with the deployment's cosign key, on the post-merge release event only",
			ImageSigningRule, func() provider { return signing }),
		method(registrypush.CapabilityName, "Registry push",
			"Publish artifacts with an explicitly granted OCI registry credential from release or manual CI",
			RegistryPushRule, func() provider { return push }),
		method(packagepublish.CapabilityName, "Package publish",
			"Publish this repository's packages from release or manual CI, as this deployment's own GitHub credential for it",
			PackagePublishRule, func() provider {
				if p := packagePublish.Load(); p != nil {
					return p
				}
				return packagepublish.New(packagepublish.Config{})
			}),
	}
}

func method(name, display, description string, rule model.CIGrantRule, current func() provider) capability.Method {
	type held struct {
		p     provider
		lease *model.Lease
		resp  *protocol.MintResponse
	}
	var mu sync.Mutex
	leases := map[string]held{}
	return capability.Method{
		Server: ServerID,
		Name:   display,
		CI:     &rule,
		Spec: protocol.MethodSpec{
			Name:        name,
			Description: description,
			Args:        map[string]any{"type": "object", "additionalProperties": false},
			// The pass's deadline is the CI job's own (ciPassDeadline),
			// from grain's clock, as the grain server's methods take it.
			Now: func() time.Time { return time.Time{} },
			Mint: func(ctx context.Context, attemptID string, deadline time.Time, _ map[string]any) (*protocol.MintResponse, error) {
				job, ok := model.CIMintFrom(ctx)
				if !ok {
					// Grain enforces the rule before a mint is asked for;
					// this is the method refusing to be the only check.
					return &protocol.MintResponse{Status: protocol.StatusRefused, Reason: model.CIGrantRuleCIOnlyRefusal(ServerID+"#"+name, rule)}, nil
				}
				if job.WithholdEventGated || !rule.Admits(job.Event, job.TrustedSchedule) {
					// The same for the job's event: grain leaves the grant
					// out of such a job's pass (ciCapabilityRefs) and
					// Permits refuses it, and so does the method.
					reason := model.CIGrantRuleRefusal(ServerID+"#"+name, model.CapabilitySubject{
						Kind: model.CapabilityRunCI, Event: job.Event, TrustedSchedule: job.TrustedSchedule})
					if reason == "" {
						reason = ServerID + "#" + name + " is withheld from this run: it places no event-gated capability"
					}
					return &protocol.MintResponse{Status: protocol.StatusRefused, Reason: reason}, nil
				}
				mu.Lock()
				h, renewing := leases[attemptID]
				mu.Unlock()
				if renewing {
					return h.resp, nil
				}
				p := current()
				cc := CapabilityContext(job, time.Now().UTC(), deadline)
				// The secret store's audit names the job, as it did
				// for the provider (model.ResolveGrants).
				ctx = secrets.WithRun(ctx, job.RunID)
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
				resp := &protocol.MintResponse{Status: protocol.StatusOK}
				for _, pl := range mat.Placements {
					resp.Placements = append(resp.Placements, protocol.Placement{Path: pl.Path, Mode: pl.ProtocolMode(), Content: pl.Content})
				}
				mu.Lock()
				leases[attemptID] = held{p: p, lease: mat.Lease, resp: resp}
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
				return h.p.Revoke(ctx, model.CapabilityContext{Now: time.Now(), CI: true}, *h.lease)
			},
		},
	}
}

// CapabilityContext is the context the retired provider resolved a CI
// job's grant in (daemon.ResolveCICapabilities), for job at now with the
// job's deadline.
func CapabilityContext(job model.CIMint, now, deadline time.Time) model.CapabilityContext {
	repo := job.Repo
	return model.CapabilityContext{
		Task: model.Task{Target: &repo}, Run: model.Run{ID: job.RunID, Sandbox: job.RunID, StartedAt: now},
		Now: now, Deadline: deadline, Credentials: job.Credentials, CI: true,
	}
}

func init() {
	for _, m := range Methods() {
		capability.Register(m)
	}
}
