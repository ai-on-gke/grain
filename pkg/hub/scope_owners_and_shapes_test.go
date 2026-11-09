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
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/hub/api"
	"github.com/ai-on-gke/grain/pkg/hub/store"
	"github.com/ai-on-gke/grain/pkg/webauth"
)

func TestUnknownShapeReturnsCleanBadRequestWithoutSQLLeak(t *testing.T) {
	dsn := startEphemeralPostgresForRegistryTest(t)
	ctx := context.Background()

	st, err := store.Open(ctx, dsn, store.DefaultThresholds())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	admitter, err := webauth.NewAdmitter(webauth.AdmissionConfig{
		Verifier:       fakeFrontIdentity{},
		AllowedDomains: []string{"example.com"},
		Admins:         []string{"user:admin@example.com"},
	})
	if err != nil {
		t.Fatalf("NewAdmitter: %v", err)
	}
	signer, err := NewEphemeralLocalSigner("shape-test-k1")
	if err != nil {
		t.Fatalf("NewEphemeralLocalSigner: %v", err)
	}
	cfg, err := ParseConfig([]byte(`
host: registry.example.com
instance_domain: instances.example.com
auth:
  provider: iap
  iap_audience: /projects/123/global/backendServices/456
  allowed_domains:
    - example.com
signing:
  provider: local
  key_id: local-dev-1
compute:
  provider: none
`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	srv, err := NewServer(ServerOptions{
		Config:   *cfg,
		Store:    st,
		Admitter: admitter,
		Signer:   signer,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	cli, err := api.NewClient(api.ClientConfig{
		BaseURL:           ts.URL,
		AllowInsecureHTTP: true,
		ExtraHeaders:      http.Header{"X-Test-Email": []string{"alice@example.com"}},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	assertCleanUnknownShape := func(t *testing.T, op string, gotErr error, shape string) {
		t.Helper()
		if !isAPIStatus(gotErr, http.StatusBadRequest) {
			t.Fatalf("%s: expected 400 Bad Request, got %v", op, gotErr)
		}
		msg := gotErr.Error()
		want := `unknown shape "` + shape + `"`
		if !strings.Contains(msg, want) {
			t.Errorf("%s: error %q missing %q", op, msg, want)
		}
		if strings.Contains(msg, "sql: no rows") {
			t.Errorf("%s: error leaked raw SQL text: %q", op, msg)
		}
	}

	// 1. CreateEntry with an unknown shape when no shapes are seeded (`compute.provider: none`).
	_, err = cli.CreateEntry(ctx, api.CreateEntryRequest{
		Name:  "vm-unknown-shape",
		Kind:  api.KindInstance,
		Shape: "standard",
	})
	assertCleanUnknownShape(t, "CreateEntry(standard)", err, "standard")

	// Seed one valid shape ("small") so we can create an instance and test PATCH + replace.
	if err := st.SeedShapes(ctx, []store.ShapeRow{
		{Name: "small", MachineType: "n2-standard-4", BootDiskGB: 50, QuotaUnits: 1},
	}); err != nil {
		t.Fatalf("SeedShapes: %v", err)
	}
	if _, err := cli.CreateEntry(ctx, api.CreateEntryRequest{
		Name:  "vm-small",
		Kind:  api.KindInstance,
		Shape: "small",
	}); err != nil {
		t.Fatalf("CreateEntry(small): %v", err)
	}

	// 2. PATCH /api/entries/{name} with an unknown shape.
	badShape := "nonexistent-xl"
	_, err = cli.UpdateEntry(ctx, "vm-small", api.UpdateEntryRequest{Shape: &badShape})
	assertCleanUnknownShape(t, "UpdateEntry(nonexistent-xl)", err, badShape)

	// 3. POST /api/entries/{name}/replace with an unknown shape.
	_, err = cli.ReplaceEntry(ctx, "vm-small", api.ReplaceEntryRequest{Shape: badShape})
	assertCleanUnknownShape(t, "ReplaceEntry(nonexistent-xl)", err, badShape)
}

func TestParseConfigScopeOwnersValidationAndMatching(t *testing.T) {
	t.Parallel()
	cfg, err := ParseConfig([]byte(`
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
catalog:
  scope_owners:
    - repo: Alpha/*
      principals:
        - group:Alpha-Eng@Alpha.test
    - repo: Alpha/Special
      principals:
        - user:Lead@Alpha.test
`))
	if err != nil {
		t.Fatalf("ParseConfig(valid scope_owners): %v", err)
	}
	if len(cfg.Catalog.ScopeOwners) != 2 {
		t.Fatalf("ScopeOwners len = %d, want 2", len(cfg.Catalog.ScopeOwners))
	}

	// Exact rule wins over org/* prefix rule.
	if r, ok := MatchScopeOwnerRule(cfg.Catalog.ScopeOwners, "alpha/special"); !ok || r.Principals[0] != "user:lead@alpha.test" {
		t.Fatalf("MatchScopeOwnerRule(alpha/special) = (%+v, %v), want exact user:lead@alpha.test", r, ok)
	}
	if r, ok := MatchScopeOwnerRule(cfg.Catalog.ScopeOwners, "alpha/app"); !ok || r.Principals[0] != "group:alpha-eng@alpha.test" {
		t.Fatalf("MatchScopeOwnerRule(alpha/app) = (%+v, %v), want group:alpha-eng@alpha.test", r, ok)
	}
	if r, ok := MatchScopeOwnerRule(cfg.Catalog.ScopeOwners, "beta/app"); ok || r != nil {
		t.Fatalf("MatchScopeOwnerRule(beta/app) = (%+v, %v), want unmatched", r, ok)
	}
	// Host-qualified claims of the same path are governed by the bare-form
	// rules, so `alpha/*` cannot be sidestepped by claiming
	// `github.com/alpha/app` (which /api/route matches as its own key).
	if r, ok := MatchScopeOwnerRule(cfg.Catalog.ScopeOwners, "https://GitHub.com/Alpha/App.git"); !ok || r.Principals[0] != "group:alpha-eng@alpha.test" {
		t.Fatalf("MatchScopeOwnerRule(github.com/alpha/app) = (%+v, %v), want group:alpha-eng@alpha.test", r, ok)
	}
	if r, ok := MatchScopeOwnerRule(cfg.Catalog.ScopeOwners, "github.com/alpha/special"); !ok || r.Principals[0] != "user:lead@alpha.test" {
		t.Fatalf("MatchScopeOwnerRule(github.com/alpha/special) = (%+v, %v), want exact user:lead@alpha.test", r, ok)
	}
	if r, ok := MatchScopeOwnerRule(cfg.Catalog.ScopeOwners, "github.com/beta/app"); ok || r != nil {
		t.Fatalf("MatchScopeOwnerRule(github.com/beta/app) = (%+v, %v), want unmatched", r, ok)
	}
	// A rule naming the host-qualified form takes precedence for that host.
	hostRules := []ScopeOwnerRule{
		{Repo: "alpha/*", Principals: []string{"group:alpha-eng@alpha.test"}},
		{Repo: "git.example.com/alpha/*", Principals: []string{"user:mirror@alpha.test"}},
	}
	if r, ok := MatchScopeOwnerRule(hostRules, "git.example.com/alpha/app"); !ok || r.Principals[0] != "user:mirror@alpha.test" {
		t.Fatalf("MatchScopeOwnerRule(git.example.com/alpha/app) = (%+v, %v), want user:mirror@alpha.test", r, ok)
	}
	if r, ok := MatchScopeOwnerRule(hostRules, "github.com/alpha/app"); !ok || r.Principals[0] != "group:alpha-eng@alpha.test" {
		t.Fatalf("MatchScopeOwnerRule(github.com/alpha/app) with host rules = (%+v, %v), want group:alpha-eng@alpha.test", r, ok)
	}

	for _, badYAML := range []struct {
		name  string
		block string
		want  string
	}{
		{
			name: "bare star",
			block: `
  scope_owners:
    - repo: "*"
      principals: ["user:alice@alpha.test"]`,
			want: "invalid repo pattern",
		},
		{
			name: "empty prefix wildcard",
			block: `
  scope_owners:
    - repo: "/*"
      principals: ["user:alice@alpha.test"]`,
			want: "invalid repo wildcard",
		},
		{
			name: "empty principals",
			block: `
  scope_owners:
    - repo: "alpha/*"
      principals: []`,
			want: "principals (alpha/*) must be non-empty",
		},
		{
			name: "duplicate repo pattern",
			block: `
  scope_owners:
    - repo: "alpha/app"
      principals: ["user:alice@alpha.test"]
    - repo: "ALPHA/APP"
      principals: ["user:bob@alpha.test"]`,
			want: "duplicate catalog.scope_owners repo pattern",
		},
	} {
		_, err := ParseConfig([]byte(`
host: registry.example.com
instance_domain: instances.example.com
auth:
  provider: iap
  iap_audience: /projects/123/global/backendServices/456
  allowed_domains: [alpha.test]
signing:
  provider: local
  key_id: local-dev-1
compute:
  provider: none
catalog:` + badYAML.block))
		if err == nil || !strings.Contains(err.Error(), badYAML.want) {
			t.Errorf("%s: ParseConfig err = %v, want substring %q", badYAML.name, err, badYAML.want)
		}
	}
}

func TestDefaultScopeClaimsWalkthroughAndSecondaryRoute(t *testing.T) {
	dsn := startEphemeralPostgresForRegistryTest(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

	st, err := store.Open(ctx, dsn, store.DefaultThresholds())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	st.SetNow(func() time.Time { return now })

	authCfg := AuthConfig{
		AllowedDomains:    []string{"alpha.test", "beta.test"},
		ServicePrincipals: []string{"serviceAccount:router@proj.iam.gserviceaccount.com"},
	}
	delPolicy, err := NewConsentDelegationPolicy(authCfg, st)
	if err != nil {
		t.Fatalf("NewConsentDelegationPolicy: %v", err)
	}
	delPolicy.SetNow(func() time.Time { return now })

	admitter, err := webauth.NewAdmitter(webauth.AdmissionConfig{
		Verifier:          fakeFrontIdentity{},
		AllowedDomains:    authCfg.AllowedDomains,
		ServicePrincipals: authCfg.ServicePrincipals,
	})
	if err != nil {
		t.Fatalf("NewAdmitter: %v", err)
	}
	signer, err := NewEphemeralLocalSigner("walk-k1")
	if err != nil {
		t.Fatalf("NewEphemeralLocalSigner: %v", err)
	}
	cfg, err := ParseConfig([]byte(`
host: registry.example.com
instance_domain: instances.example.com
auth:
  provider: iap
  iap_audience: /projects/123/global/backendServices/456
  allowed_domains:
    - alpha.test
    - beta.test
  service_principals:
    - serviceAccount:router@proj.iam.gserviceaccount.com
signing:
  provider: local
  key_id: local-dev-1
compute:
  provider: none
`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	srv, err := NewServer(ServerOptions{
		Config:     *cfg,
		Store:      st,
		Admitter:   admitter,
		Delegation: delPolicy,
		Signer:     signer,
		ReplicaID:  "rep-walk",
		PeerAddr:   "127.0.0.1:18082",
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.Router().SetNow(func() time.Time { return now })
	_ = st.UpsertReplica(ctx, "rep-walk", "127.0.0.1:18082")

	ts := httptest.NewServer(srv.Handler())
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
	aliceCli := clientFor("alice@alpha.test")
	bobCli := clientFor("bob@beta.test")
	routerCli := clientFor("router@proj.iam.gserviceaccount.com")

	// 1. Alice lists conn-alice with primary scope alpha/app:services/api.
	listedTrue := true
	listedFalse := false
	if _, err := aliceCli.CreateEntry(ctx, api.CreateEntryRequest{
		Name:   "conn-alice",
		Kind:   api.KindConnected,
		Listed: &listedTrue,
		Scopes: []api.ScopeInput{{Repo: "alpha/app", PathPrefix: "services/api", Role: api.RolePrimary}},
	}); err != nil {
		t.Fatalf("CreateEntry conn-alice: %v", err)
	}

	// 2. Walkthrough case: Bob creates unlisted bob-ws (`listed=false`) and sets
	// whole-repo primary scope `alpha/app`. Unlisted entries are allowed to hold
	// private scopes, and never appear in /api/route.
	if _, err := bobCli.CreateEntry(ctx, api.CreateEntryRequest{
		Name:   "bob-ws",
		Kind:   api.KindConnected,
		Listed: &listedFalse,
	}); err != nil {
		t.Fatalf("CreateEntry bob-ws: %v", err)
	}
	if _, err := bobCli.SetScopes(ctx, "bob-ws", []api.ScopeInput{
		{Repo: "alpha/app", PathPrefix: "", Role: api.RolePrimary},
	}); err != nil {
		t.Fatalf("SetScopes on unlisted bob-ws: %v", err)
	}
	if err := srv.Router().CatalogCache().Reload(ctx); err != nil {
		t.Fatalf("CatalogCache.Reload: %v", err)
	}
	rtUnlisted, err := routerCli.Route(ctx, api.RouteQuery{Repo: "alpha/app", Path: "README.md", IncludeOffline: true})
	if err != nil {
		t.Fatalf("Route while bob-ws unlisted: %v", err)
	}
	if len(rtUnlisted.Candidates) != 0 || rtUnlisted.Primary != nil {
		t.Fatalf("expected unlisted bob-ws excluded from /api/route, got %+v", rtUnlisted)
	}

	// 3. Flipping bob-ws to listed=true while holding whole-repo `alpha/app` ("")
	// fails with 409 Conflict because "" overlaps `services/api`.
	if _, err := bobCli.UpdateEntry(ctx, "bob-ws", api.UpdateEntryRequest{Listed: &listedTrue}); !isAPIStatus(err, http.StatusConflict) {
		t.Fatalf("expected 409 Conflict listing bob-ws with overlapping repo-root primary, got %v", err)
	}

	// 4. If bob-ws holds only a `secondary` repo-root scope and is listed,
	// `/api/route` for a path outside `services/api` returns bob-ws as a secondary
	// candidate with `Primary == nil` (never promoting a lone secondary to Primary).
	if _, err := bobCli.SetScopes(ctx, "bob-ws", []api.ScopeInput{
		{Repo: "alpha/app", PathPrefix: "", Role: api.RoleSecondary},
	}); err != nil {
		t.Fatalf("SetScopes secondary on bob-ws: %v", err)
	}
	if _, err := bobCli.UpdateEntry(ctx, "bob-ws", api.UpdateEntryRequest{Listed: &listedTrue}); err != nil {
		t.Fatalf("UpdateEntry listed=true on secondary bob-ws: %v", err)
	}
	if err := srv.Router().CatalogCache().Reload(ctx); err != nil {
		t.Fatalf("CatalogCache.Reload: %v", err)
	}
	rtSecondaryOnly, err := routerCli.Route(ctx, api.RouteQuery{Repo: "alpha/app", Path: "README.md", IncludeOffline: true})
	if err != nil {
		t.Fatalf("Route secondary-only: %v", err)
	}
	if rtSecondaryOnly.Primary != nil {
		t.Fatalf("expected Primary=nil when only a secondary candidate matches, got %+v", rtSecondaryOnly.Primary)
	}
	if len(rtSecondaryOnly.Candidates) != 1 || rtSecondaryOnly.Candidates[0].Name != "bob-ws" || rtSecondaryOnly.Candidates[0].MatchedScope.Role != api.RoleSecondary {
		t.Fatalf("unexpected secondary route candidates: %+v", rtSecondaryOnly.Candidates)
	}

	// 5. Without `catalog.scope_owners`, Bob CAN claim a disjoint primary prefix
	// (`services/web`), and `/api/route` surfaces `owner: bob@beta.test`.
	// Downstream delegated dispatch on behalf of Alice is still blocked (`403 no_grant`)
	// because Alice has not granted `router@` consent on `bob-ws`.
	if _, err := bobCli.SetScopes(ctx, "bob-ws", []api.ScopeInput{
		{Repo: "alpha/app", PathPrefix: "services/web", Role: api.RolePrimary},
	}); err != nil {
		t.Fatalf("SetScopes disjoint primary on bob-ws: %v", err)
	}
	if err := srv.Router().CatalogCache().Reload(ctx); err != nil {
		t.Fatalf("CatalogCache.Reload: %v", err)
	}
	rtDisjoint, err := routerCli.Route(ctx, api.RouteQuery{Repo: "alpha/app", Path: "services/web/index.ts", IncludeOffline: true})
	if err != nil {
		t.Fatalf("Route disjoint primary: %v", err)
	}
	if rtDisjoint.Primary == nil || rtDisjoint.Primary.Name != "bob-ws" || rtDisjoint.Primary.Owner != "bob@beta.test" {
		t.Fatalf("expected bob-ws as disjoint primary in open mode, got %+v", rtDisjoint.Primary)
	}

	// Delegated proxy request to bob-ws on behalf of alice@alpha.test is refused (403) without Alice's grant.
	delReq := httptest.NewRequest(http.MethodGet, "https://bob-ws.instances.example.com/api/whoami", nil)
	delReq.Host = "bob-ws.instances.example.com"
	delReq.Header.Set("X-Test-Email", "router@proj.iam.gserviceaccount.com")
	delReq.Header.Set("X-Grain-On-Behalf-Of", "alice@alpha.test")
	delRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusForbidden {
		t.Fatalf("delegated request to ungranted bob-ws = %d %s, want 403 Forbidden", delRec.Code, delRec.Body.String())
	}
}

func TestCatalogScopeOwnersEnforcesPrimaryClaimsAndDemotesHeartbeats(t *testing.T) {
	dsn := startEphemeralPostgresForRegistryTest(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 7, 13, 0, 0, 0, time.UTC)

	st, err := store.Open(ctx, dsn, store.DefaultThresholds())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	st.SetNow(func() time.Time { return now })

	groups := &toggleableGroupResolverForTest{
		inner: webauth.NewStaticGroups(map[string][]string{
			"group:alpha-eng@alpha.test": {"alice@alpha.test"},
		}),
	}
	admitter, err := webauth.NewAdmitter(webauth.AdmissionConfig{
		Verifier:          fakeFrontIdentity{},
		Groups:            groups,
		AllowedDomains:    []string{"alpha.test", "beta.test"},
		Admins:            []string{"user:admin@alpha.test"},
		ServicePrincipals: []string{"serviceAccount:router@proj.iam.gserviceaccount.com"},
	})
	if err != nil {
		t.Fatalf("NewAdmitter: %v", err)
	}
	signer, err := NewEphemeralLocalSigner("scope-owners-k1")
	if err != nil {
		t.Fatalf("NewEphemeralLocalSigner: %v", err)
	}
	cfg, err := ParseConfig([]byte(`
host: registry.example.com
instance_domain: instances.example.com
auth:
  provider: iap
  iap_audience: /projects/123/global/backendServices/456
  allowed_domains:
    - alpha.test
    - beta.test
  admins:
    - user:admin@alpha.test
  service_principals:
    - serviceAccount:router@proj.iam.gserviceaccount.com
signing:
  provider: local
  key_id: local-dev-1
compute:
  provider: none
catalog:
  scope_owners:
    - repo: alpha/*
      principals:
        - group:alpha-eng@alpha.test
    - repo: alpha/special
      principals:
        - user:lead@alpha.test
`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	srv, err := NewServer(ServerOptions{
		Config:    *cfg,
		Store:     st,
		Admitter:  admitter,
		Groups:    groups,
		Signer:    signer,
		ReplicaID: "rep-so",
		PeerAddr:  "127.0.0.1:18082",
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.Router().SetNow(func() time.Time { return now })
	_ = st.UpsertReplica(ctx, "rep-so", "127.0.0.1:18082")

	mainTS := httptest.NewServer(srv.Handler())
	defer mainTS.Close()
	pubTS := httptest.NewServer(srv.PublicHandler())
	defer pubTS.Close()

	clientFor := func(email string) *api.Client {
		c, cErr := api.NewClient(api.ClientConfig{
			BaseURL:           mainTS.URL,
			AllowInsecureHTTP: true,
			ExtraHeaders:      http.Header{"X-Test-Email": []string{email}},
		})
		if cErr != nil {
			t.Fatalf("NewClient(%s): %v", email, cErr)
		}
		return c
	}
	aliceCli := clientFor("alice@alpha.test")
	leadCli := clientFor("lead@alpha.test")
	bobCli := clientFor("bob@beta.test")
	adminCli := clientFor("admin@alpha.test")
	routerCli := clientFor("router@proj.iam.gserviceaccount.com")

	listedTrue := true
	listedFalse := false

	// 1. Bob (not in alpha-eng) is rejected with 403 scope_owner_forbidden on POST /api/entries
	// when claiming primary on alpha/app, even when listed=false.
	_, err = bobCli.CreateEntry(ctx, api.CreateEntryRequest{
		Name:   "bob-ws",
		Kind:   api.KindConnected,
		Listed: &listedFalse,
		Scopes: []api.ScopeInput{{Repo: "alpha/app", PathPrefix: "", Role: api.RolePrimary}},
	})
	if !isAPIStatus(err, http.StatusForbidden) || !strings.Contains(err.Error(), "not authorized to claim a primary scope") {
		t.Fatalf("expected 403 scope_owner_forbidden on Bob CreateEntry(alpha/app primary), got %v", err)
	}

	// 1b. Spelling the same repository host-qualified does not sidestep the
	// bare-form `alpha/*` rule.
	_, err = bobCli.CreateEntry(ctx, api.CreateEntryRequest{
		Name:   "bob-ws",
		Kind:   api.KindConnected,
		Listed: &listedFalse,
		Scopes: []api.ScopeInput{{Repo: "https://github.com/alpha/app.git", PathPrefix: "", Role: api.RolePrimary}},
	})
	if !isAPIStatus(err, http.StatusForbidden) || !strings.Contains(err.Error(), "not authorized to claim a primary scope") {
		t.Fatalf("expected 403 scope_owner_forbidden on Bob CreateEntry(github.com/alpha/app primary), got %v", err)
	}

	// 2. Bob CAN create bob-ws with a secondary scope on alpha/app and a primary scope
	// on unrestricted beta/tool; PUT /scopes attempting primary on alpha/app is rejected with 403.
	bobCreated, err := bobCli.CreateEntry(ctx, api.CreateEntryRequest{
		Name:   "bob-ws",
		Kind:   api.KindConnected,
		Listed: &listedTrue,
		Scopes: []api.ScopeInput{
			{Repo: "alpha/app", PathPrefix: "", Role: api.RoleSecondary},
			{Repo: "beta/tool", PathPrefix: "", Role: api.RolePrimary},
		},
	})
	if err != nil {
		t.Fatalf("Bob CreateEntry(secondary alpha/app + primary beta/tool): %v", err)
	}
	if _, err := bobCli.SetScopes(ctx, "bob-ws", []api.ScopeInput{
		{Repo: "alpha/app", PathPrefix: "", Role: api.RolePrimary},
	}); !isAPIStatus(err, http.StatusForbidden) {
		t.Fatalf("expected 403 on Bob SetScopes(alpha/app primary), got %v", err)
	}

	// 3. Asynchronous heartbeat from bob-ws reporting `alpha/app` as `primary`
	// succeeds (`200 OK`, preserving liveness) while demoting `alpha/app` to `secondary`.
	// First clear declared scopes so reported scopes drive effective role directly.
	if _, err := bobCli.SetScopes(ctx, "bob-ws", []api.ScopeInput{}); err != nil {
		t.Fatalf("Bob clear declared scopes: %v", err)
	}
	hbBody, _ := json.Marshal(api.HeartbeatRequest{
		GrainVersion: "v1.5.0",
		Health:       api.HealthOK,
		Scopes: []api.HeartbeatScope{
			{Repo: "alpha/app", PathPrefix: "", Role: api.RolePrimary},
			{Repo: "beta/tool", PathPrefix: "", Role: api.RolePrimary},
		},
	})
	hbReq, _ := http.NewRequest(http.MethodPut, pubTS.URL+"/api/entries/bob-ws/heartbeat", bytes.NewReader(hbBody))
	hbReq.Header.Set("Authorization", "Bearer "+bobCreated.RegistrationToken)
	hbReq.Header.Set("Content-Type", "application/json")
	hbResp, err := pubTS.Client().Do(hbReq)
	if err != nil {
		t.Fatalf("Bob heartbeat: %v", err)
	}
	_ = hbResp.Body.Close()
	if hbResp.StatusCode != http.StatusOK {
		t.Fatalf("Bob heartbeat status = %d, want 200 OK", hbResp.StatusCode)
	}

	bobRow, err := st.GetEntryByName(ctx, "bob-ws")
	if err != nil {
		t.Fatalf("GetEntryByName bob-ws: %v", err)
	}
	_, _ = st.AcquireTunnelSessionWithCred(ctx, bobRow.ID, "rep-so", "198.51.100.10:4000", bobCreated.Credential.CredentialID)
	if err := srv.Router().CatalogCache().Reload(ctx); err != nil {
		t.Fatalf("CatalogCache.Reload: %v", err)
	}

	rtAlpha, err := routerCli.Route(ctx, api.RouteQuery{Repo: "alpha/app", Path: "services/web/main.go", IncludeOffline: true})
	if err != nil {
		t.Fatalf("Route alpha/app: %v", err)
	}
	if rtAlpha.Primary != nil {
		t.Fatalf("expected Primary=nil for alpha/app when bob-ws was demoted to secondary, got %+v", rtAlpha.Primary)
	}
	if len(rtAlpha.Candidates) != 1 || rtAlpha.Candidates[0].Name != "bob-ws" || rtAlpha.Candidates[0].MatchedScope.Role != api.RoleSecondary {
		t.Fatalf("expected bob-ws demoted to secondary in candidates, got %+v", rtAlpha.Candidates)
	}

	rtBeta, err := routerCli.Route(ctx, api.RouteQuery{Repo: "beta/tool", Path: "cmd/main.go", IncludeOffline: true})
	if err != nil {
		t.Fatalf("Route beta/tool: %v", err)
	}
	if rtBeta.Primary == nil || rtBeta.Primary.Name != "bob-ws" {
		t.Fatalf("expected bob-ws as primary for unrestricted beta/tool, got %+v", rtBeta.Primary)
	}

	// 4. GroupResolver outage fails closed with 503 + Retry-After: 5 on group-backed primary claim.
	groups.failNow.Store(true)
	_, err = aliceCli.CreateEntry(ctx, api.CreateEntryRequest{
		Name:   "conn-alice",
		Kind:   api.KindConnected,
		Listed: &listedTrue,
		Scopes: []api.ScopeInput{{Repo: "alpha/app", PathPrefix: "services/api", Role: api.RolePrimary}},
	})
	if !isAPIStatus(err, http.StatusServiceUnavailable) {
		t.Fatalf("expected 503 when GroupResolver fails during scope_owners check, got %v", err)
	}
	// Direct user match (`user:lead@alpha.test` on `alpha/special`) still succeeds during group outage!
	if _, err := leadCli.CreateEntry(ctx, api.CreateEntryRequest{
		Name:   "lead-special",
		Kind:   api.KindConnected,
		Listed: &listedTrue,
		Scopes: []api.ScopeInput{{Repo: "alpha/special", PathPrefix: "", Role: api.RolePrimary}},
	}); err != nil {
		t.Fatalf("expected direct user match on alpha/special to succeed during GroupResolver outage, got %v", err)
	}
	groups.failNow.Store(false)

	// 5. Alice (member of alpha-eng) succeeds on alpha/app, but is rejected on alpha/special
	// because the exact rule `alpha/special` overrides `alpha/*`.
	if _, err := aliceCli.CreateEntry(ctx, api.CreateEntryRequest{
		Name:   "alice-special-denied",
		Kind:   api.KindConnected,
		Listed: &listedTrue,
		Scopes: []api.ScopeInput{{Repo: "alpha/special", PathPrefix: "sub", Role: api.RolePrimary}},
	}); !isAPIStatus(err, http.StatusForbidden) {
		t.Fatalf("expected 403 for Alice on exact-overridden alpha/special, got %v", err)
	}
	if _, err := aliceCli.CreateEntry(ctx, api.CreateEntryRequest{
		Name:   "conn-alice",
		Kind:   api.KindConnected,
		Listed: &listedTrue,
		Scopes: []api.ScopeInput{{Repo: "alpha/app", PathPrefix: "services/api", Role: api.RolePrimary}},
	}); err != nil {
		t.Fatalf("Alice CreateEntry(alpha/app:services/api primary): %v", err)
	}

	// 6. Offering transfer of conn-alice (holding primary on alpha/app) to unauthorized Bob is rejected (403);
	// admin force-transfer to Bob succeeds (200) and demotes alpha/app:services/api to secondary.
	if _, err := aliceCli.OfferTransfer(ctx, "conn-alice", api.TransferRequest{To: "bob@beta.test"}); !isAPIStatus(err, http.StatusForbidden) {
		t.Fatalf("expected 403 offering transfer of restricted primary entry to Bob, got %v", err)
	}
	forced, err := adminCli.OfferTransfer(ctx, "conn-alice", api.TransferRequest{To: "bob@beta.test", Force: true})
	if err != nil {
		t.Fatalf("admin force-transfer conn-alice to Bob: %v", err)
	}
	if forced.Owner != "bob@beta.test" {
		t.Fatalf("forced transfer Owner = %q, want bob@beta.test", forced.Owner)
	}
	if len(forced.Scopes) != 1 || forced.Scopes[0].Role != api.RoleSecondary {
		t.Fatalf("expected admin force-transfer to demote restricted primary scope to secondary, got %+v", forced.Scopes)
	}
}
