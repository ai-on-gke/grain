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

package security

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/hub"
	"github.com/ai-on-gke/grain/pkg/hub/api"
	"github.com/ai-on-gke/grain/pkg/hub/store"
	"github.com/ai-on-gke/grain/pkg/webauth"
)

// The registry's consent surface -- delegating apps an admin registers, and
// grants a person makes to one -- decides which service accounts may act as
// which people. These tests hold its boundaries:
//
//   - nobody outside auth.allowed_domains reaches any of it (cross-tenant);
//   - only registry admins create, list, disable or enable apps;
//   - a grant is only ever made by the person it is for, whoever asks
//     (admins included), and service accounts cannot make one at all;
//   - one person cannot see or revoke another's grant, and a probe for
//     somebody else's grant ID reads exactly like a missing one.

type securityHubFrontIdentity struct{}

func (securityHubFrontIdentity) Authenticate(r *http.Request) (*webauth.Caller, error) {
	email := strings.TrimSpace(r.Header.Get("X-Test-Email"))
	if email == "" {
		return nil, errors.New("unauthenticated")
	}
	return &webauth.Caller{Email: email, Subject: "sub:" + email}, nil
}

type securityHubGroups struct{}

func (securityHubGroups) IsMember(context.Context, string, string) (bool, error) {
	return false, nil
}

func (securityHubGroups) NormalizeGroup(raw string) (string, error) {
	return strings.ToLower(strings.TrimSpace(raw)), nil
}

const securityHubYAML = `
host: registry.example.com
instance_domain: instances.example.com
auth:
  provider: iap
  iap_audience: /projects/123/global/backendServices/456
  allowed_domains:
    - example.com
  admins:
    - user:admin@example.com
  service_principals:
    - serviceAccount:scanner@proj.iam.gserviceaccount.com
signing:
  provider: local
  key_id: local-dev-1
compute:
  provider: gce
  project: example-project
  region: us-central1
  zones:
    - us-central1-a
  default_shape: standard
  shapes:
    - name: standard
      machine_type: n2-standard-4
      boot_disk_gb: 50
      quota_units: 1
instance_sql:
  sql_instances:
    - name: pool-01
      connection_name: example-project:us-central1:pool-01
      private_ip: 10.8.0.5
      max_databases: 50
`

// startSecurityHubPostgres starts a throwaway Postgres for one test, or
// skips when the binaries are missing (unless GRAIN_REQUIRE_POSTGRES=1).
func startSecurityHubPostgres(t *testing.T) string {
	t.Helper()
	if envURL := strings.TrimSpace(os.Getenv("GRAIN_TEST_POSTGRES_URL")); envURL != "" {
		return envURL
	}
	findBin := func(name string) string {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
		matches, _ := filepath.Glob(filepath.Join("/usr/lib/postgresql", "*", "bin", name))
		if len(matches) > 0 {
			sort.Strings(matches)
			return matches[len(matches)-1]
		}
		return ""
	}
	initdbBin, pgCtlBin := findBin("initdb"), findBin("pg_ctl")
	if initdbBin == "" || pgCtlBin == "" {
		if os.Getenv("GRAIN_REQUIRE_POSTGRES") == "1" {
			t.Fatalf("initdb/pg_ctl not found while GRAIN_REQUIRE_POSTGRES=1")
		}
		t.Skip("skipping registry consent security test: initdb/pg_ctl not installed")
	}
	if u, err := user.Current(); err == nil && u.Uid == "0" {
		t.Skip("skipping registry consent security test: running as root")
	}
	baseDir, err := os.MkdirTemp("/tmp", "grain-sec-reg-pg-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	dataDir, sockDir := filepath.Join(baseDir, "data"), filepath.Join(baseDir, "sock")
	if err := os.MkdirAll(sockDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if out, err := exec.Command(initdbBin, "-D", dataDir, "-A", "trust", "-U", "postgres", "--no-sync", "--encoding=UTF8", "--locale=C").CombinedOutput(); err != nil {
		_ = os.RemoveAll(baseDir)
		t.Fatalf("initdb: %v\n%s", err, out)
	}
	opts := fmt.Sprintf("-F -k %s -h ''", sockDir)
	if out, err := exec.Command(pgCtlBin, "-D", dataDir, "-l", filepath.Join(baseDir, "pg.log"), "-o", opts, "-w", "start").CombinedOutput(); err != nil {
		_ = os.RemoveAll(baseDir)
		t.Fatalf("pg_ctl start: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command(pgCtlBin, "-D", dataDir, "-m", "immediate", "-w", "stop").Run()
		_ = os.RemoveAll(baseDir)
	})
	dsn := (&url.URL{
		Scheme:   "postgres",
		User:     url.User("postgres"),
		Host:     "localhost",
		Path:     "/postgres",
		RawQuery: "host=" + url.QueryEscape(sockDir) + "&sslmode=disable",
	}).String()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		t.Fatalf("db.Ping: %v", err)
	}
	return dsn
}

func newSecurityHub(t *testing.T) (http.Handler, *store.Store, *hub.ConsentDelegationPolicy) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, startSecurityHubPostgres(t), store.DefaultThresholds())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	cfg, err := hub.ParseConfig([]byte(securityHubYAML))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	admitter, err := webauth.NewAdmitter(webauth.AdmissionConfig{
		Verifier:          securityHubFrontIdentity{},
		Groups:            securityHubGroups{},
		AllowedDomains:    cfg.Auth.AllowedDomains,
		Admins:            cfg.Auth.Admins,
		ServicePrincipals: cfg.Auth.ServicePrincipals,
	})
	if err != nil {
		t.Fatalf("NewAdmitter: %v", err)
	}
	pol, err := hub.NewConsentDelegationPolicy(cfg.Auth, st)
	if err != nil {
		t.Fatalf("NewConsentDelegationPolicy: %v", err)
	}
	signer, err := hub.NewEphemeralLocalSigner("sec-dlg-k1")
	if err != nil {
		t.Fatalf("NewEphemeralLocalSigner: %v", err)
	}
	srv, err := hub.NewServer(hub.ServerOptions{
		Config:     *cfg,
		Store:      st,
		Admitter:   admitter,
		Groups:     securityHubGroups{},
		Signer:     signer,
		Delegation: pol,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return srv.Handler(), st, pol
}

func securityHubCall(t *testing.T, h http.Handler, method, path, email string, body any) (int, []byte) {
	t.Helper()
	var buf []byte
	if body != nil {
		var err error
		if buf, err = json.Marshal(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, "https://registry.example.com"+path, bytes.NewReader(buf))
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

// TestHubConsentSurfaceRefusesOtherTenantsAndNonAdmins holds who may call
// each delegating-app and delegation-grant route at all.
func TestHubConsentSurfaceRefusesOtherTenantsAndNonAdmins(t *testing.T) {
	h, st, _ := newSecurityHub(t)
	ctx := context.Background()
	if _, err := st.CreateDelegatingApp(ctx, store.DelegatingAppRow{
		Name: "planner", Principal: "planner@proj.iam.gserviceaccount.com",
		DisplayName: "Planner", Owner: "admin@example.com", CreatedBy: "admin@example.com",
	}); err != nil {
		t.Fatalf("CreateDelegatingApp: %v", err)
	}
	g, err := st.UpsertDelegationGrant(ctx, "planner", "alice@example.com", []string{"*"}, "read", 0)
	if err != nil {
		t.Fatalf("UpsertDelegationGrant: %v", err)
	}

	newApp := api.CreateDelegatingAppRequest{Name: "intruder", Principal: "intruder@proj.iam.gserviceaccount.com"}
	newGrant := api.CreateDelegationGrantRequest{App: "planner", Entries: []string{"*"}}
	routes := []struct {
		method, path string
		body         any
		adminOnly    bool
	}{
		{http.MethodGet, "/api/admin/delegating-apps", nil, true},
		{http.MethodPost, "/api/admin/delegating-apps", newApp, true},
		{http.MethodPatch, "/api/admin/delegating-apps/planner", api.SetDelegatingAppDisabledRequest{Disabled: true}, true},
		{http.MethodGet, "/api/delegation-grants", nil, false},
		{http.MethodPost, "/api/delegation-grants", newGrant, false},
		{http.MethodDelete, "/api/delegation-grants/" + g.ID, nil, false},
	}
	callers := []struct {
		email  string
		admin  bool
		person bool
	}{
		{"", false, false}, // unauthenticated
		{"mallory@other-tenant.example", false, false},             // another tenant's person
		{"admin@other-tenant.example", false, false},               // another tenant's "admin"
		{"robot@other-proj.iam.gserviceaccount.com", false, false}, // unlisted service account
		{"scanner@proj.iam.gserviceaccount.com", false, false},     // admitted service account: catalog reads only
		{"bob@example.com", false, true},                           // a person here, not an admin
	}
	for _, rt := range routes {
		for _, c := range callers {
			if c.person && !rt.adminOnly {
				continue // a person's own grant routes are exercised below
			}
			code, body := securityHubCall(t, h, rt.method, rt.path, c.email, rt.body)
			if code != http.StatusUnauthorized && code != http.StatusForbidden {
				t.Errorf("%s %s as %q = %d %s, want 401/403", rt.method, rt.path, c.email, code, body)
			}
		}
	}
	// None of those calls changed anything.
	apps, err := st.ListDelegatingApps(ctx)
	if err != nil || len(apps) != 1 || apps[0].Name != "planner" || apps[0].Disabled() {
		t.Fatalf("refused calls changed delegating apps: %+v (err=%v)", apps, err)
	}
	live, err := st.GetLiveDelegationGrant(ctx, "planner", "alice@example.com")
	if err != nil || live.ID != g.ID {
		t.Fatalf("refused calls touched alice's grant: %+v (err=%v)", live, err)
	}
	for _, who := range []string{"mallory@other-tenant.example", "scanner@proj.iam.gserviceaccount.com"} {
		if _, err := st.GetLiveDelegationGrant(ctx, "planner", who); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("refused grant left a live grant for %s (err=%v)", who, err)
		}
	}
}

// TestHubDelegationGrantIsOnlyEverThePersonsOwn holds that a grant is
// consent the signed-in person gives for themselves, and cannot be made,
// seen, or withdrawn on anybody else's behalf.
func TestHubDelegationGrantIsOnlyEverThePersonsOwn(t *testing.T) {
	h, st, pol := newSecurityHub(t)
	ctx := context.Background()
	const planner = "planner@proj.iam.gserviceaccount.com"
	if code, body := securityHubCall(t, h, http.MethodPost, "/api/admin/delegating-apps", "admin@example.com",
		api.CreateDelegatingAppRequest{Name: "planner", Principal: planner}); code != http.StatusCreated {
		t.Fatalf("admin registering app = %d %s", code, body)
	}

	// Granting for somebody else is refused, for a person and an admin alike,
	// and so is naming a person in another tenant.
	for _, tc := range []struct{ caller, person string }{
		{"bob@example.com", "alice@example.com"},
		{"admin@example.com", "alice@example.com"},
		{"bob@example.com", "alice@other-tenant.example"},
	} {
		code, body := securityHubCall(t, h, http.MethodPost, "/api/delegation-grants", tc.caller,
			api.CreateDelegationGrantRequest{App: "planner", Entries: []string{"*"}, Person: tc.person})
		if code != http.StatusForbidden {
			t.Errorf("%s granting for %s = %d %s, want 403", tc.caller, tc.person, code, body)
		}
		if _, err := pol.AuthorizeOnBehalfOf(ctx, planner, tc.person, "alice-vm"); err == nil {
			t.Errorf("refused grant by %s still lets planner act as %s", tc.caller, tc.person)
		}
	}

	// Alice's own grant works, and is recorded as hers whatever the body says
	// about anyone else (it says nothing here).
	code, body := securityHubCall(t, h, http.MethodPost, "/api/delegation-grants", "alice@example.com",
		api.CreateDelegationGrantRequest{App: "planner", Entries: []string{"alice-vm"}, Access: "read"})
	if code != http.StatusOK {
		t.Fatalf("alice granting = %d %s", code, body)
	}
	var grant api.DelegationGrant
	if err := json.Unmarshal(body, &grant); err != nil || grant.Person != "alice@example.com" {
		t.Fatalf("alice's grant = %s (err=%v)", body, err)
	}
	if _, err := pol.AuthorizeOnBehalfOf(ctx, planner, "alice@example.com", "alice-vm"); err != nil {
		t.Fatalf("alice's grant does not authorise planner: %v", err)
	}

	// Bob sees none of it and cannot withdraw it, by ID or by app name; the
	// answer for her grant ID is the same as for an ID that does not exist.
	code, body = securityHubCall(t, h, http.MethodGet, "/api/delegation-grants?include_revoked=true", "bob@example.com", nil)
	if code != http.StatusOK || bytes.Contains(body, []byte(grant.ID)) || bytes.Contains(body, []byte("alice@")) {
		t.Fatalf("bob's grant list = %d %s, must not show alice's grant", code, body)
	}
	missingCode, missingBody := securityHubCall(t, h, http.MethodDelete, "/api/delegation-grants/dlg_ffffffffffffffff", "bob@example.com", nil)
	for _, ref := range []string{grant.ID, "planner"} {
		code, body := securityHubCall(t, h, http.MethodDelete, "/api/delegation-grants/"+ref, "bob@example.com", nil)
		if code != http.StatusNotFound {
			t.Errorf("bob revoking alice's %s = %d %s, want 404", ref, code, body)
		}
		if ref == grant.ID && (code != missingCode || bytes.Contains(body, []byte("alice"))) {
			t.Errorf("bob probing alice's grant ID got %d %s; a missing ID gets %d %s", code, body, missingCode, missingBody)
		}
	}
	live, err := st.GetLiveDelegationGrant(ctx, "planner", "alice@example.com")
	if err != nil || live.RevokedAt != nil {
		t.Fatalf("bob's refused revokes touched alice's grant: %+v (err=%v)", live, err)
	}
	if _, err := pol.AuthorizeOnBehalfOf(ctx, planner, "bob@example.com", "alice-vm"); err == nil {
		t.Fatal("alice's grant lets planner act as bob")
	}
}
