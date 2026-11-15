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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/modelproxy"
)

// The model package's vendor list, which the retired names and the
// migration are written from, is pkg/modelproxy's.
func TestModelVendorsAreTheProxysVendors(t *testing.T) {
	var names []string
	for _, v := range modelproxy.Vendors() {
		names = append(names, v.Name)
		if got := model.LegacyModelProxyCapability(v.Name); got != CapabilityName(v) {
			t.Errorf("model.LegacyModelProxyCapability(%q) = %q, want %q", v.Name, got, CapabilityName(v))
		}
	}
	if !slices.Equal(names, model.ModelProxyVendors) {
		t.Errorf("model.ModelProxyVendors = %v, want pkg/modelproxy's %v", model.ModelProxyVendors, names)
	}
}

// fidelityEndpoints is one endpoint per vendor whose team has two
// connections of it: the run's own, when the run's agent spends one of
// that vendor, and "team-<vendor>" otherwise -- with no deployment-wide
// key, so a run whose team has none is refused.
func fidelityEndpoints(teamHasConnection bool) map[string]*modelproxy.Endpoint {
	out := map[string]*modelproxy.Endpoint{}
	for _, v := range modelproxy.Vendors() {
		vendor := v.Name
		ep := modelproxy.NewEndpoint()
		ep.SetURL("http://10.0.0.1:41" + vendor)
		ep.SetConnections(func(_ context.Context, own string) (modelproxy.Connection, error) {
			if !teamHasConnection {
				return modelproxy.Connection{}, nil
			}
			if strings.HasPrefix(own, vendor+"-") {
				return modelproxy.Connection{ID: own}, nil
			}
			return modelproxy.Connection{ID: "team-" + vendor}, nil
		})
		ep.SetCredential(func(_ context.Context, c modelproxy.Connection) (string, error) { return "key-of-" + c.ID, nil })
		out[vendor] = ep
	}
	return out
}

// mintRewritten mints grant, as migration 100 rewrote it, on the
// in-process model-proxy server for a run whose agent spends connection,
// the way the orchestrator mints a capability pass: for run 42-1.
func mintRewritten(t *testing.T, grant, connection string) (*protocol.Pass, error) {
	t.Helper()
	lookup := func(id string) (*protocol.Server, error) {
		srv, ok, err := capability.MethodServer(id)
		if !ok {
			t.Fatalf("no in-process server %s", id)
		}
		return srv, err
	}
	recorded, err := model.NormalizeCapabilityGrants([]model.Grant{{Capability: grant}}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(recorded) != 1 || recorded[0].Capability != grant {
		t.Fatalf("%s normalizes to %+v, want itself: migration 100 writes recorded grants", grant, recorded)
	}
	grants, err := model.CapabilityGrants(recorded, lookup)
	if err != nil {
		t.Fatal(err)
	}
	ctx := model.WithMintRunID(model.WithRunConnection(context.Background(), connection), "42-1")
	return protocol.MintPass(ctx, grants, func(id string) (protocol.Minter, error) { return lookup(id) }, protocol.PassOptions{
		Deployment: "grain", Task: "42", Attempt: 1, Deadline: time.Now().Add(time.Hour),
	})
}

// A run holding a retired <vendor>-proxy grant, minted by the retired
// in-process provider, and a run holding the model-proxy {vendor} grant
// migration 100 rewrote it to, minted by the in-process model-proxy
// server, receive the same two files at the same paths with the same mode
// and address, a token forwarding with the same team connection, and the
// same prompt text -- whether the run's own agent spends a connection of
// that vendor or not. A team with no connection of the vendor is refused
// by both, with the same reason, and no deployment-wide key stands in.
func TestModelProxyMigrationFidelity(t *testing.T) {
	endpoints := fidelityEndpoints(true)
	unregister := capability.Register(Method(endpoints))
	defer unregister()

	for _, vendor := range modelproxy.Vendors() {
		for _, own := range []string{vendor.Name + "-own", "other-framework-conn", ""} {
			t.Run(vendor.Name+"/"+own, func(t *testing.T) {
				legacyName := CapabilityName(vendor)
				rewritten := model.RetiredGrantsBecame(legacyName)[0]
				if rewritten != model.ModelProxyGrant(vendor.Name) {
					t.Fatalf("%s is rewritten to %q, want %q", legacyName, rewritten, model.ModelProxyGrant(vendor.Name))
				}

				legacy := New(vendor, endpoints[vendor.Name])
				cc := model.CapabilityContext{Run: model.Run{ID: "42-1"}, Now: time.Now(), Deadline: time.Now().Add(time.Hour), Connection: own}
				if res, err := legacy.Resolve(context.Background(), cc); err != nil || res.Refused {
					t.Fatalf("legacy resolve = %+v, %v", res, err)
				}
				mat, err := legacy.Materialize(context.Background(), cc)
				if err != nil {
					t.Fatal(err)
				}
				defer legacy.Revoke(context.Background(), cc, *mat.Lease)
				legacyPrompt, err := legacy.PromptSection(context.Background(), cc, mat.Placements)
				if err != nil {
					t.Fatal(err)
				}

				pass, err := mintRewritten(t, rewritten, own)
				if err != nil {
					t.Fatalf("minting %s: %v", rewritten, err)
				}
				placements := pass.Placements()
				if len(placements) != len(mat.Placements) {
					t.Fatalf("rewritten grant placed %d files, legacy %d", len(placements), len(mat.Placements))
				}
				var legacyConn, newConn modelproxy.Connection
				var legacyOwner, newOwner string
				for i, pl := range mat.Placements {
					got := placements[i]
					if got.Path != pl.Path || got.FileMode() != 0o600 || pl.EffectiveMode() != "600" {
						t.Errorf("placement %d = %s %o, legacy %s %s", i, got.Path, got.FileMode(), pl.Path, pl.EffectiveMode())
					}
					if pl.Path == TokenPath(vendor) {
						var ok bool
						if legacyOwner, legacyConn, ok = endpoints[vendor.Name].Tokens.Lookup(strings.TrimSpace(pl.Content)); !ok {
							t.Error("legacy token is not honoured")
						}
						if newOwner, newConn, ok = endpoints[vendor.Name].Tokens.Lookup(strings.TrimSpace(got.Content)); !ok {
							t.Error("rewritten grant's token is not honoured")
						}
						continue
					}
					if got.Content != pl.Content {
						t.Errorf("%s = %q, legacy %q", pl.Path, got.Content, pl.Content)
					}
				}
				if legacyConn.ID == "" || newConn != legacyConn {
					t.Errorf("rewritten grant's token forwards with %+v, legacy with %+v", newConn, legacyConn)
				}
				// Owned by the run, not the attempt id, so a restarted
				// daemon's revokeFinishedRunTokens still finds it.
				if legacyOwner != "42-1" || newOwner != legacyOwner {
					t.Errorf("rewritten grant's token is owned by %q, legacy by %q, want run 42-1", newOwner, legacyOwner)
				}
				if got := pass.Grants[0].Response.PromptSection; got != legacyPrompt {
					t.Errorf("prompt differs:\nrewritten: %q\nlegacy:    %q", got, legacyPrompt)
				}

				// The pass's release revokes its token, as the retired
				// provider's lease revocation did.
				token := ""
				for _, pl := range placements {
					if pl.Path == TokenPath(vendor) {
						token = strings.TrimSpace(pl.Content)
					}
				}
				if _, err := pass.Release(context.Background()); err != nil {
					t.Fatal(err)
				}
				if _, _, ok := endpoints[vendor.Name].Tokens.Lookup(token); ok {
					t.Error("the rewritten grant's token outlived the pass's release")
				}
			})
		}
	}
}

func TestModelProxyMigrationFidelityRefusesATeamWithNoConnection(t *testing.T) {
	endpoints := fidelityEndpoints(false)
	unregister := capability.Register(Method(endpoints))
	defer unregister()
	for _, vendor := range modelproxy.Vendors() {
		legacy := New(vendor, endpoints[vendor.Name])
		res, err := legacy.Resolve(context.Background(), model.CapabilityContext{Now: time.Now()})
		if err != nil || !res.Refused {
			t.Fatalf("%s legacy resolve = %+v, %v, want refused", vendor.Name, res, err)
		}
		_, err = mintRewritten(t, model.ModelProxyGrant(vendor.Name), "")
		var refused *protocol.RefusedError
		if !asRefused(err, &refused) || refused.Reason != res.Reason {
			t.Errorf("%s rewritten grant = %v, want refused with %q", vendor.Name, err, res.Reason)
		}
		if n := endpoints[vendor.Name].Tokens.Len(); n != 0 {
			t.Errorf("%s: %d tokens minted for a refused grant", vendor.Name, n)
		}
	}
}

func asRefused(err error, out **protocol.RefusedError) bool {
	r, ok := err.(*protocol.RefusedError)
	if ok {
		*out = r
	}
	return ok
}

// A renewal mints the same attempt again and keeps the token the run
// already holds rather than minting one nobody placed.
func TestModelProxyRenewalKeepsTheRunsToken(t *testing.T) {
	endpoints := fidelityEndpoints(true)
	unregister := capability.Register(Method(endpoints))
	defer unregister()
	pass, err := mintRewritten(t, model.ModelProxyGrant("claude"), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := pass.Renew(context.Background(), time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := endpoints["claude"].Tokens.Len(); n != 1 {
		t.Errorf("%d tokens after a renewal, want the run's one", n)
	}
	if _, err := pass.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := endpoints["claude"].Tokens.Len(); n != 0 {
		t.Errorf("%d tokens after release, want none", n)
	}
}
