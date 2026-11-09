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

package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ai-on-gke/grain/pkg/hub/api"
	"github.com/ai-on-gke/grain/pkg/hub/store"
	"github.com/ai-on-gke/grain/pkg/webauth"
)

// Primaries claimed before catalog.scope_owners was configured are not
// grandfathered: the startup sweep demotes the ones the policy does not
// authorize, so /api/route stops returning them as primary, while it keeps
// authorized ones and does not demote anything on a GroupResolver outage.
func TestScopeOwnersStartupSweepDemotesPrePolicyPrimaries(t *testing.T) {
	dsn := startEphemeralPostgresForHubTest(t)
	ctx := context.Background()

	st, err := store.Open(ctx, dsn, store.DefaultThresholds())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	groups := &toggleableGroupResolverForTest{
		inner: webauth.NewStaticGroups(map[string][]string{
			"group:alpha-eng@alpha.test": {"alice@alpha.test"},
		}),
	}
	admitter, err := webauth.NewAdmitter(webauth.AdmissionConfig{
		Verifier:       fakeFrontIdentity{},
		Groups:         groups,
		AllowedDomains: []string{"alpha.test", "beta.test"},
	})
	if err != nil {
		t.Fatalf("NewAdmitter: %v", err)
	}
	signer, err := NewEphemeralLocalSigner("scope-owners-sweep-k1")
	if err != nil {
		t.Fatalf("NewEphemeralLocalSigner: %v", err)
	}
	const base = `
host: registry.example.com
instance_domain: instances.example.com
auth:
  provider: iap
  iap_audience: /projects/123/global/backendServices/456
  allowed_domains:
    - alpha.test
    - beta.test
signing:
  provider: local
  key_id: local-dev-1
compute:
  provider: none
`
	newServer := func(yaml string) *Server {
		cfg, err := ParseConfig([]byte(yaml))
		if err != nil {
			t.Fatalf("ParseConfig: %v", err)
		}
		srv, err := NewServer(ServerOptions{Config: *cfg, Store: st, Admitter: admitter, Groups: groups, Signer: signer})
		if err != nil {
			t.Fatalf("NewServer: %v", err)
		}
		return srv
	}

	// Before the policy: bob (outside alpha-eng) and alice both self-serve
	// primary claims on alpha/app.
	open := newServer(base)
	ts := httptest.NewServer(open.Handler())
	defer ts.Close()
	clientFor := func(email string) *api.Client {
		c, cErr := api.NewClient(api.ClientConfig{
			BaseURL:           ts.URL,
			AllowInsecureHTTP: true,
			ExtraHeaders:      http.Header{"X-Test-Email": []string{email}},
		})
		if cErr != nil {
			t.Fatalf("NewClient(%s): %v", email, cErr)
		}
		return c
	}
	listed := true
	if _, err := clientFor("bob@beta.test").CreateEntry(ctx, api.CreateEntryRequest{
		Name: "bob-ws", Kind: api.KindConnected, Listed: &listed,
		Scopes: []api.ScopeInput{{Repo: "alpha/app", PathPrefix: "web", Role: api.RolePrimary}},
	}); err != nil {
		t.Fatalf("bob CreateEntry before policy: %v", err)
	}
	if _, err := clientFor("alice@alpha.test").CreateEntry(ctx, api.CreateEntryRequest{
		Name: "alice-ws", Kind: api.KindConnected, Listed: &listed,
		Scopes: []api.ScopeInput{{Repo: "alpha/app", PathPrefix: "api", Role: api.RolePrimary}},
	}); err != nil {
		t.Fatalf("alice CreateEntry before policy: %v", err)
	}

	restricted := newServer(base + `
catalog:
  scope_owners:
    - repo: alpha/*
      principals:
        - group:alpha-eng@alpha.test
`)

	primaryOf := func(name string) bool {
		t.Helper()
		e, err := st.GetEntryByName(ctx, name)
		if err != nil {
			t.Fatalf("GetEntryByName(%s): %v", name, err)
		}
		for _, sc := range e.Scopes {
			if sc.PrimaryClaim {
				return true
			}
		}
		return false
	}

	// A GroupResolver outage during the sweep demotes nothing: neither
	// alice's claim (which the resolver would have authorized) nor bob's.
	groups.failNow.Store(true)
	if n, err := restricted.ReconcilePrimaryScopeOwners(ctx); err != nil || n != 0 {
		t.Fatalf("sweep during resolver outage: changed=%d err=%v, want 0, nil", n, err)
	}
	if !primaryOf("alice-ws") || !primaryOf("bob-ws") {
		t.Fatalf("sweep during resolver outage rewrote a claim: alice=%v bob=%v", primaryOf("alice-ws"), primaryOf("bob-ws"))
	}
	groups.failNow.Store(false)

	n, err := restricted.ReconcilePrimaryScopeOwners(ctx)
	if err != nil {
		t.Fatalf("ReconcilePrimaryScopeOwners: %v", err)
	}
	if n != 1 {
		t.Fatalf("sweep changed %d entries, want 1 (bob-ws)", n)
	}
	if primaryOf("bob-ws") {
		t.Fatalf("bob-ws still holds a primary scope on restricted alpha/app after the sweep")
	}
	if !primaryOf("alice-ws") {
		t.Fatalf("sweep demoted alice-ws, whose owner is in alpha-eng")
	}
	if err := restricted.Router().CatalogCache().Reload(ctx); err != nil {
		t.Fatalf("CatalogCache.Reload: %v", err)
	}
	rt, err := restricted.Router().CatalogCache().Route(ctx, "alpha/app", "web/index.html", "true")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if rt.Primary != nil {
		t.Fatalf("route for alpha/app web/ still names a primary after the sweep: %+v", rt.Primary)
	}
}
