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

package modelproxy

import (
	"context"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/modelproxy"
)

// ServerID is the canonical capability_id of the in-process model-proxy
// server, and MethodName its one method: model-proxy {vendor}, which the
// retired claude-proxy, openai-proxy and gemini-proxy grants became
// (schema migration 100, model.ModelProxyGrant).
const (
	ServerID   = model.ModelProxyCapabilityServerID
	MethodName = model.ModelProxyMethod
)

// Method is the in-process model-proxy method over endpoints (keyed by
// modelproxy.Vendor.Name, as Providers takes them): what
// capability.Register serves on ServerID.
//
// It is answered by grain itself, as the grain server's methods are,
// because what it spends is the run's own team connection -- the very one
// the run's agent spends, which the orchestrator hands it in the mint's
// context (model.RunConnection) -- and nothing outside grain knows that.
// The external `grain capability model-proxy` server forwards with one
// deployment-wide key per vendor instead, which would change whose
// credential pays. So each mint is the retired Provider's own Resolve,
// Materialize and PromptSection for the vendor named, with the run's
// connection: the same refusals, the same token on the same proxy, the
// same two files and the same prompt.
func Method(endpoints map[string]*modelproxy.Endpoint) capability.Method {
	vendors := modelproxy.Vendors()
	providers := map[string]*Provider{}
	enum := make([]any, 0, len(vendors))
	for _, v := range vendors {
		providers[v.Name] = New(v, endpoints[v.Name])
		enum = append(enum, v.Name)
	}
	var mu sync.Mutex
	type held struct {
		vendor string
		lease  model.Lease
		resp   *protocol.MintResponse
	}
	leases := map[string]held{}
	return capability.Method{
		Server: ServerID,
		Name:   "Model proxy",
		Spec: protocol.MethodSpec{
			Name: MethodName,
			Description: "Let this task call one model vendor's inference API through grain's own proxy -- " +
				"a per-run token in its sandbox, forwarding with its team's connection for that vendor, " +
				"whose credential never leaves the controller.",
			Args: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"vendor"},
				"properties": map[string]any{
					"vendor": map[string]any{"type": "string", "enum": enum},
				},
			},
			// The orchestrator set the pass's deadline from its own
			// clock, so there is no second one to hold it to (as the
			// grain server's methods).
			Now: func() time.Time { return time.Time{} },
			Mint: func(ctx context.Context, attemptID string, deadline time.Time, args map[string]any) (*protocol.MintResponse, error) {
				name, _ := args["vendor"].(string)
				p := providers[name]
				if p == nil {
					return &protocol.MintResponse{Status: protocol.StatusRefused, Reason: "unknown vendor " + name}, nil
				}
				// A renewal (protocol.Pass.Renew) mints the same attempt
				// again: the run keeps the token it was given, bounded
				// as the retired provider's was by the deadline it was
				// minted under.
				mu.Lock()
				h, renewing := leases[attemptID]
				mu.Unlock()
				if renewing && h.vendor == name {
					return h.resp, nil
				}
				now := time.Now()
				// The token's owner is the run, as the retired provider's
				// was: a pass is not persisted, so after a restart only
				// the daemon's sweep of finished runs' tokens
				// (revokeFinishedRunTokens, keyed by run id) revokes it.
				runID := model.MintRunID(ctx)
				if runID == "" {
					runID = attemptID
				}
				cc := model.CapabilityContext{
					Run:        model.Run{ID: runID},
					Now:        now,
					Deadline:   deadline,
					Connection: model.RunConnection(ctx),
				}
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
					p.Revoke(ctx, cc, *mat.Lease)
					return nil, err
				}
				resp := &protocol.MintResponse{Status: protocol.StatusOK, PromptSection: prompt}
				for _, pl := range mat.Placements {
					resp.Placements = append(resp.Placements, protocol.Placement{Path: pl.Path, Mode: pl.ProtocolMode(), Content: pl.Content})
				}
				mu.Lock()
				leases[attemptID] = held{vendor: name, lease: *mat.Lease, resp: resp}
				mu.Unlock()
				return resp, nil
			},
			Release: func(ctx context.Context, attemptID string) error {
				mu.Lock()
				h, ok := leases[attemptID]
				delete(leases, attemptID)
				mu.Unlock()
				if !ok {
					return nil
				}
				return providers[h.vendor].Revoke(ctx, model.CapabilityContext{Now: time.Now()}, h.lease)
			},
		},
	}
}
