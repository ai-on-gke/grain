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
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/hub/api"
	"github.com/ai-on-gke/grain/pkg/hub/store"
	"github.com/ai-on-gke/grain/pkg/webauth"
)

// newDelegationAdminTestServer starts a Postgres-backed registry Server with
// `delegation_policy: consent` wired to the same store, so a test can drive
// the admin and grant endpoints and then ask the policy what they changed.
func newDelegationAdminTestServer(t *testing.T) (http.Handler, *store.Store, *ConsentDelegationPolicy, *CachingSigner) {
	t.Helper()
	pgURL := startEphemeralPostgresForHubTest(t)
	ctx := context.Background()
	st, err := store.Open(ctx, pgURL, store.DefaultThresholds())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	groups := staticGroupResolverForTest{StaticGroupChecker: webauth.NewStaticGroups(nil)}
	authCfg := AuthConfig{
		AllowedDomains:    []string{"example.com"},
		ServicePrincipals: []string{"serviceAccount:scanner@proj.iam.gserviceaccount.com"},
	}
	admitter, err := webauth.NewAdmitter(webauth.AdmissionConfig{
		Verifier:          fakeFrontIdentity{},
		Groups:            groups,
		AllowedDomains:    authCfg.AllowedDomains,
		Admins:            []string{"user:admin@example.com"},
		ServicePrincipals: authCfg.ServicePrincipals,
	})
	if err != nil {
		t.Fatalf("NewAdmitter: %v", err)
	}
	pol, err := NewConsentDelegationPolicy(authCfg, st)
	if err != nil {
		t.Fatalf("NewConsentDelegationPolicy: %v", err)
	}
	local, err := NewEphemeralLocalSigner("dlg-admin-k1")
	if err != nil {
		t.Fatalf("NewEphemeralLocalSigner: %v", err)
	}
	signer := NewCachingSigner(local, 30*time.Second)
	cfg, err := ParseConfig([]byte(validTestYAML()))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	srv, err := NewServer(ServerOptions{
		Config:     *cfg,
		Store:      st,
		Admitter:   admitter,
		Groups:     groups,
		Signer:     signer,
		Delegation: pol,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return srv.Handler(), st, pol, signer
}

func doDelegationJSON(t *testing.T, h http.Handler, method, path, email string, body any) (int, []byte) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(buf)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, "https://registry.example.com"+path, rdr)
	if email != "" {
		req.Header.Set("X-Test-Email", email)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func refusalReason(err error) string {
	var ref *DelegationRefusedError
	if errors.As(err, &ref) {
		return ref.Reason
	}
	return ""
}

// TestDelegatingAppAdminAPIAndPersonGrants walks the whole consent setup
// through HTTP: an admin registers an app by its bare service-account email,
// a person grants it, the consent policy then authorises that app for that
// person, and every refusal on the way holds -- non-admins cannot manage
// apps, nobody (admins included) can grant for someone else, and one
// person cannot see or revoke another's grant.
func TestDelegatingAppAdminAPIAndPersonGrants(t *testing.T) {
	h, st, pol, signer := newDelegationAdminTestServer(t)
	ctx := context.Background()
	const (
		admin   = "admin@example.com"
		alice   = "alice@example.com"
		bob     = "bob@example.com"
		scanner = "scanner@proj.iam.gserviceaccount.com"
		planner = "planner@proj.iam.gserviceaccount.com"
	)
	createReq := api.CreateDelegatingAppRequest{
		Name:               "planner",
		Principal:          planner, // bare email: the registry must add serviceAccount:
		DisplayName:        "Planner",
		MaxAccess:          "write",
		MaxGrantTTLSeconds: 7 * 24 * 3600,
	}

	// 1. Only admins manage delegating apps.
	for _, who := range []string{alice, scanner, ""} {
		if code, body := doDelegationJSON(t, h, http.MethodPost, "/api/admin/delegating-apps", who, createReq); code != http.StatusForbidden && code != http.StatusUnauthorized {
			t.Fatalf("POST delegating-apps as %q = %d %s, want 403/401", who, code, body)
		}
		if code, body := doDelegationJSON(t, h, http.MethodGet, "/api/admin/delegating-apps", who, nil); code != http.StatusForbidden && code != http.StatusUnauthorized {
			t.Fatalf("GET delegating-apps as %q = %d %s, want 403/401", who, code, body)
		}
	}
	if apps, err := st.ListDelegatingApps(ctx); err != nil || len(apps) != 0 {
		t.Fatalf("refused creates registered apps: %+v (err=%v)", apps, err)
	}

	// 2. Bad input is refused before it reaches the table.
	for _, bad := range []api.CreateDelegatingAppRequest{
		{Name: "planner", Principal: ""},
		{Name: "planner", Principal: "bob@example.com"}, // a person, not a service account
		{Name: "planner", Principal: "not-an-email"},
		{Name: "Bad_Name!", Principal: planner},
		{Name: "planner", Principal: planner, MaxAccess: "admin"},
	} {
		if code, body := doDelegationJSON(t, h, http.MethodPost, "/api/admin/delegating-apps", admin, bad); code != http.StatusBadRequest {
			t.Fatalf("POST delegating-apps %+v = %d %s, want 400", bad, code, body)
		}
	}

	// 3. Admin registers the app; the principal comes back normalised.
	code, body := doDelegationJSON(t, h, http.MethodPost, "/api/admin/delegating-apps", admin, createReq)
	if code != http.StatusCreated {
		t.Fatalf("POST delegating-apps as admin = %d %s", code, body)
	}
	var app api.DelegatingApp
	if err := json.Unmarshal(body, &app); err != nil {
		t.Fatal(err)
	}
	if app.Principal != "serviceAccount:"+planner || app.Owner != admin || app.CreatedBy != admin || app.MaxGrantTTLSeconds != 7*24*3600 {
		t.Fatalf("created app = %+v", app)
	}
	dup := createReq
	dup.Name = "planner-two"
	dup.Principal = "serviceAccount:" + planner
	if code, body := doDelegationJSON(t, h, http.MethodPost, "/api/admin/delegating-apps", admin, dup); code != http.StatusConflict {
		t.Fatalf("POST duplicate principal = %d %s, want 409", code, body)
	}
	code, body = doDelegationJSON(t, h, http.MethodGet, "/api/admin/delegating-apps", admin, nil)
	var list api.ListDelegatingAppsResponse
	if code != http.StatusOK || json.Unmarshal(body, &list) != nil || len(list.Apps) != 1 || list.Apps[0].Name != "planner" {
		t.Fatalf("GET delegating-apps as admin = %d %s", code, body)
	}
	if ok, err := pol.IsServicePrincipalReader(ctx, planner); err != nil || !ok {
		t.Fatalf("registered app by bare email is not admitted: ok=%v err=%v", ok, err)
	}
	if _, err := pol.AuthorizeOnBehalfOf(ctx, planner, alice, "alice-vm"); refusalReason(err) != "no_grant" {
		t.Fatalf("before consent: err=%v, want no_grant (not app_not_registered)", err)
	}

	// 4. A person grants for themselves only -- admins included.
	for _, tc := range []struct{ caller, person string }{{alice, bob}, {admin, alice}, {bob, "BOB@example.com "}} {
		req := api.CreateDelegationGrantRequest{App: "planner", Entries: []string{"*"}, Person: tc.person}
		code, body := doDelegationJSON(t, h, http.MethodPost, "/api/delegation-grants", tc.caller, req)
		if tc.caller == bob {
			// Naming yourself (in any case) is fine; tidy it away again.
			if code != http.StatusOK {
				t.Fatalf("bob granting for himself = %d %s", code, body)
			}
			var g api.DelegationGrant
			_ = json.Unmarshal(body, &g)
			if err := st.RevokeDelegationGrant(ctx, g.ID, bob); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if code != http.StatusForbidden {
			t.Fatalf("%s granting for %s = %d %s, want 403", tc.caller, tc.person, code, body)
		}
		if _, err := st.GetLiveDelegationGrant(ctx, "planner", tc.person); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("refused grant for %s left a live grant (err=%v)", tc.person, err)
		}
	}
	if code, body := doDelegationJSON(t, h, http.MethodPost, "/api/delegation-grants", scanner,
		api.CreateDelegationGrantRequest{App: "planner", Entries: []string{"*"}}); code != http.StatusForbidden {
		t.Fatalf("service account granting = %d %s, want 403", code, body)
	}
	for _, bad := range []api.CreateDelegationGrantRequest{
		{App: "planner"}, // no entries
		{App: "planner", Entries: []string{"Not An Entry"}},   // bad entry name
		{App: "planner", Entries: []string{"*"}, Access: "x"}, // bad access
	} {
		if code, body := doDelegationJSON(t, h, http.MethodPost, "/api/delegation-grants", alice, bad); code != http.StatusBadRequest {
			t.Fatalf("POST grant %+v = %d %s, want 400", bad, code, body)
		}
	}
	if code, body := doDelegationJSON(t, h, http.MethodPost, "/api/delegation-grants", alice,
		api.CreateDelegationGrantRequest{App: "nope", Entries: []string{"*"}}); code != http.StatusNotFound {
		t.Fatalf("grant to unknown app = %d %s, want 404", code, body)
	}

	code, body = doDelegationJSON(t, h, http.MethodPost, "/api/delegation-grants", alice,
		api.CreateDelegationGrantRequest{App: "planner", Entries: []string{"alice-vm"}, Access: "read", TTLSeconds: 30 * 24 * 3600})
	if code != http.StatusOK {
		t.Fatalf("alice granting = %d %s", code, body)
	}
	var grant api.DelegationGrant
	if err := json.Unmarshal(body, &grant); err != nil {
		t.Fatal(err)
	}
	if grant.Person != alice || grant.App != "planner" || grant.Access != "read" || len(grant.Entries) != 1 {
		t.Fatalf("alice's grant = %+v", grant)
	}
	if got := grant.ExpiresAt.Sub(grant.CreatedAt); got > 7*24*3600*1e9 {
		t.Fatalf("grant TTL %v exceeds the app's 7-day max_grant_ttl", got)
	}
	act, err := pol.AuthorizeOnBehalfOf(ctx, planner, alice, "alice-vm")
	if err != nil || act == nil || act.Access != "read" {
		t.Fatalf("after consent: act=%+v err=%v", act, err)
	}
	if _, err := pol.AuthorizeOnBehalfOf(ctx, planner, bob, "alice-vm"); refusalReason(err) != "no_grant" {
		t.Fatalf("alice's grant authorised the app for bob: err=%v", err)
	}

	// 5. Bob cannot see or revoke alice's grant, by ID or by app name.
	code, body = doDelegationJSON(t, h, http.MethodGet, "/api/delegation-grants", bob, nil)
	var bobs api.ListDelegationGrantsResponse
	if code != http.StatusOK || json.Unmarshal(body, &bobs) != nil {
		t.Fatalf("bob listing grants = %d %s", code, body)
	}
	for _, g := range bobs.Grants {
		if g.Person != bob {
			t.Fatalf("bob's grant list shows %s's grant %+v", g.Person, g)
		}
	}
	for _, ref := range []string{grant.ID, "planner"} {
		if code, body := doDelegationJSON(t, h, http.MethodDelete, "/api/delegation-grants/"+ref, bob, nil); code != http.StatusNotFound {
			t.Fatalf("bob revoking %s = %d %s, want 404", ref, code, body)
		}
	}
	if code, body := doDelegationJSON(t, h, http.MethodDelete, "/api/delegation-grants/"+grant.ID, admin, nil); code != http.StatusNotFound {
		t.Fatalf("admin revoking alice's grant through the person API = %d %s, want 404", code, body)
	}
	if _, err := pol.AuthorizeOnBehalfOf(ctx, planner, alice, "alice-vm"); err != nil {
		t.Fatalf("alice's grant stopped working after refused revokes: %v", err)
	}

	// 6. Disabling the app stops it acting, stops new grants, and drops cached
	// assertions for that app on the local replica; enabling restores it.
	claims := HubClaims{
		Audience: "https://alice-vm.instances.example.com",
		Subject:  alice, Email: alice, Kind: "human",
		Actor: &DelegationActor{Sub: planner, Kind: "service", App: "planner", Grant: grant.ID, Access: "read"},
	}
	otherAppClaims := HubClaims{
		Audience: "https://alice-vm.instances.example.com",
		Subject:  alice, Email: alice, Kind: "human",
		Actor: &DelegationActor{Sub: "triage@proj.iam.gserviceaccount.com", Kind: "service", App: "triage", Grant: "dlg_other", Access: "read"},
	}
	preDisableTok, err := signer.SignAssertion(ctx, claims)
	if err != nil {
		t.Fatalf("SignAssertion before disable: %v", err)
	}
	otherAppTok, err := signer.SignAssertion(ctx, otherAppClaims)
	if err != nil {
		t.Fatalf("SignAssertion otherAppClaims: %v", err)
	}
	if again, _ := signer.SignAssertion(ctx, claims); again != preDisableTok {
		t.Fatal("expected a cache hit before app disable")
	}
	if code, body := doDelegationJSON(t, h, http.MethodPatch, "/api/admin/delegating-apps/planner", alice, api.SetDelegatingAppDisabledRequest{Disabled: true}); code != http.StatusForbidden {
		t.Fatalf("alice disabling app = %d %s, want 403", code, body)
	}
	if code, body := doDelegationJSON(t, h, http.MethodPatch, "/api/admin/delegating-apps/nope", admin, api.SetDelegatingAppDisabledRequest{Disabled: true}); code != http.StatusNotFound {
		t.Fatalf("disabling unknown app = %d %s, want 404", code, body)
	}
	code, body = doDelegationJSON(t, h, http.MethodPatch, "/api/admin/delegating-apps/planner", admin, api.SetDelegatingAppDisabledRequest{Disabled: true})
	if code != http.StatusOK || !bytes.Contains(body, []byte(`"disabled_at"`)) {
		t.Fatalf("admin disabling app = %d %s", code, body)
	}
	if _, err := pol.AuthorizeOnBehalfOf(ctx, planner, alice, "alice-vm"); refusalReason(err) != "app_disabled" {
		t.Fatalf("disabled app: err=%v, want app_disabled", err)
	}
	if afterDisable, _ := signer.SignAssertion(ctx, claims); afterDisable == preDisableTok {
		t.Fatal("disabling the app left its cached assertion in place on the local replica")
	}
	if otherAfterDisable, _ := signer.SignAssertion(ctx, otherAppClaims); otherAfterDisable != otherAppTok {
		t.Fatal("disabling planner evicted another app's cached assertion")
	}
	if code, body := doDelegationJSON(t, h, http.MethodPost, "/api/delegation-grants", bob,
		api.CreateDelegationGrantRequest{App: "planner", Entries: []string{"*"}}); code != http.StatusForbidden {
		t.Fatalf("grant to disabled app = %d %s, want 403", code, body)
	}
	if code, body := doDelegationJSON(t, h, http.MethodPatch, "/api/admin/delegating-apps/planner", admin, api.SetDelegatingAppDisabledRequest{Disabled: false}); code != http.StatusOK {
		t.Fatalf("admin enabling app = %d %s", code, body)
	}

	// 7. Alice revokes her own grant by app name; it shows only with include_revoked,
	// and this replica's cached assertions minted under it are dropped at once.
	cachedTok, err := signer.SignAssertion(ctx, claims)
	if err != nil {
		t.Fatalf("SignAssertion: %v", err)
	}
	if again, _ := signer.SignAssertion(ctx, claims); again != cachedTok {
		t.Fatal("expected a cache hit before revoke")
	}
	code, body = doDelegationJSON(t, h, http.MethodDelete, "/api/delegation-grants/planner", alice, nil)
	if code != http.StatusOK || !bytes.Contains(body, []byte(`"revoked_at"`)) {
		t.Fatalf("alice revoking her grant = %d %s", code, body)
	}
	if _, err := pol.AuthorizeOnBehalfOf(ctx, planner, alice, "alice-vm"); refusalReason(err) != "no_grant" {
		t.Fatalf("after revoke: err=%v, want no_grant", err)
	}
	if after, _ := signer.SignAssertion(ctx, claims); after == cachedTok {
		t.Fatal("revoking the grant left its cached assertion in place")
	}
	var mine api.ListDelegationGrantsResponse
	_, body = doDelegationJSON(t, h, http.MethodGet, "/api/delegation-grants", alice, nil)
	if json.Unmarshal(body, &mine) != nil || len(mine.Grants) != 0 {
		t.Fatalf("live grants after revoke = %s", body)
	}
	_, body = doDelegationJSON(t, h, http.MethodGet, "/api/delegation-grants?include_revoked=true", alice, nil)
	if json.Unmarshal(body, &mine) != nil || len(mine.Grants) != 1 || mine.Grants[0].ID != grant.ID || mine.Grants[0].RevokedAt == nil {
		t.Fatalf("include_revoked grants = %s", body)
	}
}

func TestDemoDelegatingAppsAndPersonGrants(t *testing.T) {
	h := newDemoState().handler()

	// 1. Seeded delegating apps are returned sorted, including enabled and disabled fixtures.
	code, body := doDelegationJSON(t, h, http.MethodGet, "/api/admin/delegating-apps", "", nil)
	var appsResp api.ListDelegatingAppsResponse
	if code != http.StatusOK || json.Unmarshal(body, &appsResp) != nil || len(appsResp.Apps) != 3 {
		t.Fatalf("GET demo /api/admin/delegating-apps = %d %s", code, body)
	}

	// 2. Registering with a bare service-account email stores the serviceAccount: form;
	// human principals and duplicate names/principals are rejected.
	if code, body := doDelegationJSON(t, h, http.MethodPost, "/api/admin/delegating-apps", "", api.CreateDelegatingAppRequest{
		Name:      "bad-human",
		Principal: "alice@example.com",
	}); code != http.StatusBadRequest {
		t.Fatalf("POST human principal = %d %s, want 400", code, body)
	}
	code, body = doDelegationJSON(t, h, http.MethodPost, "/api/admin/delegating-apps", "", api.CreateDelegatingAppRequest{
		Name:               "release-bot",
		Principal:          "release-bot@acme-prod.iam.gserviceaccount.com",
		DisplayName:        "Release Bot",
		MaxAccess:          "read",
		MaxGrantTTLSeconds: 86400,
	})
	var createdApp api.DelegatingApp
	if code != http.StatusCreated || json.Unmarshal(body, &createdApp) != nil {
		t.Fatalf("POST demo /api/admin/delegating-apps = %d %s", code, body)
	}
	if createdApp.Principal != "serviceAccount:release-bot@acme-prod.iam.gserviceaccount.com" {
		t.Fatalf("stored principal = %q, want serviceAccount: prefix", createdApp.Principal)
	}
	if code, body := doDelegationJSON(t, h, http.MethodPost, "/api/admin/delegating-apps", "", api.CreateDelegatingAppRequest{
		Name:      "release-bot",
		Principal: "other-bot@acme-prod.iam.gserviceaccount.com",
	}); code != http.StatusConflict {
		t.Fatalf("duplicate app name = %d %s, want 409", code, body)
	}

	// 3. Disable and re-enable the app; while disabled, granting consent returns 403 app_disabled.
	code, body = doDelegationJSON(t, h, http.MethodPatch, "/api/admin/delegating-apps/release-bot", "", api.SetDelegatingAppDisabledRequest{Disabled: true})
	if code != http.StatusOK || !bytes.Contains(body, []byte(`"disabled_at"`)) {
		t.Fatalf("PATCH disable = %d %s", code, body)
	}
	if code, body := doDelegationJSON(t, h, http.MethodPost, "/api/delegation-grants", "", api.CreateDelegationGrantRequest{
		App: "release-bot", Entries: []string{"platform-vm"},
	}); code != http.StatusForbidden {
		t.Fatalf("grant to disabled app = %d %s, want 403", code, body)
	}
	code, body = doDelegationJSON(t, h, http.MethodPatch, "/api/admin/delegating-apps/release-bot", "", api.SetDelegatingAppDisabledRequest{Disabled: false})
	if code != http.StatusOK || bytes.Contains(body, []byte(`"disabled_at"`)) {
		t.Fatalf("PATCH enable = %d %s", code, body)
	}

	// 4. Grant consent, verify max_access ceiling, list with/without include_revoked, and revoke by ID.
	if code, body := doDelegationJSON(t, h, http.MethodPost, "/api/delegation-grants", "", api.CreateDelegationGrantRequest{
		App: "release-bot", Entries: []string{"platform-vm"}, Access: "write",
	}); code != http.StatusForbidden {
		t.Fatalf("write grant on read-only app = %d %s, want 403", code, body)
	}
	code, body = doDelegationJSON(t, h, http.MethodPost, "/api/delegation-grants", "", api.CreateDelegationGrantRequest{
		App: "release-bot", Entries: []string{"platform-vm"}, Access: "read", TTLSeconds: 3600,
	})
	var grant api.DelegationGrant
	if code != http.StatusOK || json.Unmarshal(body, &grant) != nil || grant.ID == "" {
		t.Fatalf("POST demo /api/delegation-grants = %d %s", code, body)
	}

	code, body = doDelegationJSON(t, h, http.MethodGet, "/api/delegation-grants", "", nil)
	var liveResp api.ListDelegationGrantsResponse
	if code != http.StatusOK || json.Unmarshal(body, &liveResp) != nil || len(liveResp.Grants) != 3 {
		t.Fatalf("GET live demo grants = %d %s", code, body)
	}

	code, body = doDelegationJSON(t, h, http.MethodDelete, "/api/delegation-grants/"+grant.ID, "", nil)
	if code != http.StatusOK || !bytes.Contains(body, []byte(`"revoked_at"`)) {
		t.Fatalf("DELETE demo grant = %d %s", code, body)
	}

	code, body = doDelegationJSON(t, h, http.MethodGet, "/api/delegation-grants", "", nil)
	if json.Unmarshal(body, &liveResp) != nil || len(liveResp.Grants) != 2 {
		t.Fatalf("GET live demo grants after revoke = %d %s", code, body)
	}
	code, body = doDelegationJSON(t, h, http.MethodGet, "/api/delegation-grants?include_revoked=true", "", nil)
	var allResp api.ListDelegationGrantsResponse
	if code != http.StatusOK || json.Unmarshal(body, &allResp) != nil || len(allResp.Grants) != 4 {
		t.Fatalf("GET all demo grants = %d %s", code, body)
	}
}
