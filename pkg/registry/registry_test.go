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

package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/registry/store"
	"github.com/ai-on-gke/grain/pkg/webauth"
)

type shorthandGroupResolver struct {
	defaultDomain string
}

func (r shorthandGroupResolver) IsMember(_ context.Context, groupEmail, memberEmail string) (bool, error) {
	return false, nil
}

// NormalizeGroup implements Core follow-up C-1: canonicalizes shorthand
// "group:<name>" to "group:<name>@<defaultDomain>" before validation.
func (r shorthandGroupResolver) NormalizeGroup(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "group:") {
		return "", errors.New("must start with group:")
	}
	name := strings.TrimPrefix(raw, "group:")
	if !strings.Contains(name, "@") && name != "" {
		raw = "group:" + name + "@" + r.defaultDomain
	}
	return defaultNormalizeGroup(raw)
}

func validTestYAML() string {
	return `
host: registry.example.com
instance_domain: instances.example.com
auth:
  provider: iap
  iap_audience: /projects/123/global/backendServices/456
  allowed_domains:
    - example.com
  admins:
    - user:admin@example.com
    - group:platform-admins@example.com
  creators:
    - group:builders@example.com
  service_principals:
    - serviceAccount:scanner@proj.iam.gserviceaccount.com
signing:
  provider: local
  key_id: local-dev-1
compute:
  provider: gce
  project: my-gcp-project
  region: us-central1
  zones:
    - us-central1-a
    - us-central1-b
  default_shape: standard
  shapes:
    - name: standard
      machine_type: e2-standard-4
      boot_disk_gb: 50
      quota_units: 1
instance_sql:
  sql_instances:
    - name: pool-01
      connection_name: my-gcp-project:us-central1:pool-01
      private_ip: 10.8.0.5
      max_databases: 50
`
}

func TestConfigValidationInvariants(t *testing.T) {
	cfg, err := ParseConfig([]byte(validTestYAML()))
	if err != nil {
		t.Fatalf("ParseConfig valid YAML failed: %v", err)
	}
	if cfg.Signing.JWTTTL != 60*time.Second {
		t.Fatalf("expected default 60s JWT TTL, got %s", cfg.Signing.JWTTTL)
	}
	if cfg.Tunnel.Transport != "ws-mux" {
		t.Fatalf("expected default ws-mux transport, got %q", cfg.Tunnel.Transport)
	}
	if cfg.Compute.SSHBreakGlass == nil || !*cfg.Compute.SSHBreakGlass {
		t.Fatalf("expected ssh_break_glass default true")
	}
	if cfg.Observability.FleetLogs {
		t.Fatalf("expected fleet_logs default false")
	}
	if cfg.Quotas.ReservationPeriod != 720*time.Hour {
		t.Fatalf("expected 30d (720h) reservation_period, got %s", cfg.Quotas.ReservationPeriod)
	}

	t.Run("host equal to instance_domain rejected", func(t *testing.T) {
		c := *cfg
		c.Host = "instances.example.com"
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "must not be equal") {
			t.Fatalf("expected equal host/instance_domain error, got %v", err)
		}
	})

	t.Run("host single-label child of instance_domain rejected", func(t *testing.T) {
		c := *cfg
		c.Host = "hub.instances.example.com"
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "single-label child") {
			t.Fatalf("expected single-label child collision error, got %v", err)
		}
	})

	t.Run("catalog threshold ordering enforced", func(t *testing.T) {
		c := *cfg
		c.Catalog.DegradedAfter = 60 * time.Second
		c.Catalog.OfflineAfter = 45 * time.Second
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "catalog.offline_after") {
			t.Fatalf("expected catalog.offline_after error, got %v", err)
		}
	})

	t.Run("tunnel threshold ordering enforced", func(t *testing.T) {
		c := *cfg
		c.Tunnel.ReplicaHeartbeat = 10 * time.Second
		c.Tunnel.ReplicaTTL = 10 * time.Second
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "tunnel.replica_ttl") {
			t.Fatalf("expected tunnel.replica_ttl error, got %v", err)
		}
	})

	t.Run("zone outside region rejected", func(t *testing.T) {
		c := *cfg
		c.Compute.Zones = []string{"europe-west1-b"}
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "does not belong to compute.region") {
			t.Fatalf("expected zone/region mismatch error, got %v", err)
		}
	})

	t.Run("empty sql_instances rejected when compute enabled", func(t *testing.T) {
		c := *cfg
		c.InstanceSQL.SQLInstances = nil
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "sql_instances") {
			t.Fatalf("expected sql_instances error, got %v", err)
		}
	})

	t.Run("vm_prefix format enforced", func(t *testing.T) {
		c := *cfg
		c.Compute.VMPrefix = "Invalid_Prefix"
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "compute.vm_prefix") {
			t.Fatalf("expected compute.vm_prefix error, got %v", err)
		}
	})
}

func TestNormalizeGroupC1FollowUp(t *testing.T) {
	resolver := shorthandGroupResolver{defaultDomain: "example.com"}

	// Shorthand "group:eng-leads" fails default validation, but succeeds with a GroupResolver
	// that implements NormalizeGroup (Core follow-up C-1).
	if _, err := NormalizeGroup(nil, "group:eng-leads"); err == nil {
		t.Fatalf("expected shorthand group without domain to fail default NormalizeGroup")
	}

	got, err := NormalizeGroup(resolver, "group:eng-leads")
	if err != nil {
		t.Fatalf("NormalizeGroup with custom resolver failed: %v", err)
	}
	if got != "group:eng-leads@example.com" {
		t.Fatalf("expected canonicalized group:eng-leads@example.com, got %q", got)
	}

	yamlWithShorthand := strings.Replace(
		validTestYAML(),
		"group:platform-admins@example.com",
		"group:platform-admins",
		1,
	)
	cfg, err := ParseConfigWithResolver([]byte(yamlWithShorthand), resolver)
	if err != nil {
		t.Fatalf("ParseConfigWithResolver failed: %v", err)
	}
	if cfg.Auth.Admins[1] != "group:platform-admins@example.com" {
		t.Fatalf("expected canonicalized admin group, got %q", cfg.Auth.Admins[1])
	}
}

func TestDelegationPolicyRefuseAllStubAndExtensionHooks(t *testing.T) {
	ctx := context.Background()
	policy, err := NewStaticDelegationPolicy([]string{"serviceAccount:reader@proj.iam.gserviceaccount.com"})
	if err != nil {
		t.Fatalf("NewStaticDelegationPolicy: %v", err)
	}
	ok, err := policy.IsServicePrincipalReader(ctx, "reader@proj.iam.gserviceaccount.com")
	if err != nil || !ok {
		t.Fatalf("expected reader@proj.iam.gserviceaccount.com to be recognized, ok=%v err=%v", ok, err)
	}
	if _, err := policy.AuthorizeOnBehalfOf(ctx, "reader@proj.iam.gserviceaccount.com", "alice@example.com", "alice-vm"); !errors.Is(err, ErrDelegationDenied) {
		t.Fatalf("expected ErrDelegationDenied from refuse-all stub, got %v", err)
	}

	// Register custom FrontIdentity, GroupResolver, DelegationPolicy, and ConnectTransport.
	RegisterFrontIdentity("test-custom-auth", func(_ context.Context, _ AuthConfig) (FrontIdentity, error) {
		return fakeFrontIdentity{}, nil
	})
	RegisterGroupResolver("test-custom-groups", func(_ context.Context, _ GroupsConfig) (GroupResolver, error) {
		return shorthandGroupResolver{defaultDomain: "example.com"}, nil
	})
	RegisterDelegationPolicy("test-custom-delegation", func(_ context.Context, cfg AuthConfig) (DelegationPolicy, error) {
		return NewStaticDelegationPolicy(cfg.ServicePrincipals)
	})
	RegisterConnectTransport("test-custom-mux", func(cfg TunnelConfig) (ConnectTransport, error) {
		return NewWSMuxTransport(cfg), nil
	})

	if _, err := BuildFrontIdentity(ctx, AuthConfig{Provider: "test-custom-auth"}); err != nil {
		t.Fatalf("BuildFrontIdentity custom: %v", err)
	}
	if _, err := BuildGroupResolver(ctx, GroupsConfig{Provider: "test-custom-groups"}); err != nil {
		t.Fatalf("BuildGroupResolver custom: %v", err)
	}
	if _, err := BuildDelegationPolicy(ctx, AuthConfig{DelegationPolicy: "test-custom-delegation"}); err != nil {
		t.Fatalf("BuildDelegationPolicy custom: %v", err)
	}
	tr, err := BuildConnectTransport(TunnelConfig{})
	if err != nil || tr.Name() != "ws-mux" {
		t.Fatalf("expected default ConnectTransport ws-mux, got tr=%v err=%v", tr, err)
	}
}

type fakeFrontIdentity struct{}

func (fakeFrontIdentity) Authenticate(r *http.Request) (*webauth.Caller, error) {
	email := strings.TrimSpace(r.Header.Get("X-Test-Email"))
	if email == "" {
		return nil, errors.New("unauthenticated")
	}
	return &webauth.Caller{Email: email, Subject: "sub:" + email}, nil
}

type countingSigner struct {
	inner Signer
	calls atomic.Int64
}

func (c *countingSigner) SignAssertion(ctx context.Context, claims RegistryClaims) (string, error) {
	c.calls.Add(1)
	return c.inner.SignAssertion(ctx, claims)
}

func (c *countingSigner) JWKS(ctx context.Context) (*JSONWebKeySet, error) {
	return c.inner.JWKS(ctx)
}

func TestLocalSignerAndCachingSigner30s(t *testing.T) {
	ctx := context.Background()
	ls, err := NewEphemeralLocalSigner("local-k1")
	if err != nil {
		t.Fatalf("NewEphemeralLocalSigner: %v", err)
	}
	now := time.Date(2026, 11, 5, 21, 0, 0, 0, time.UTC)
	ls.nowFn = func() time.Time { return now }

	cs := &countingSigner{inner: ls}
	cached := NewCachingSigner(cs, 30*time.Second)
	cached.nowFn = func() time.Time { return now }

	claimsIn := RegistryClaims{
		Audience: "grain-instance:alice-dev",
		Subject:  "sub-alice",
		Email:    "alice@example.com",
		Kind:     "human",
	}

	tok1, err := cached.SignAssertion(ctx, claimsIn)
	if err != nil {
		t.Fatalf("SignAssertion 1: %v", err)
	}
	now = now.Add(15 * time.Second)
	tok2, err := cached.SignAssertion(ctx, claimsIn)
	if err != nil {
		t.Fatalf("SignAssertion 2: %v", err)
	}
	if tok1 != tok2 || cs.calls.Load() != 1 {
		t.Fatalf("expected cached token within 30s (calls=%d)", cs.calls.Load())
	}

	// After 31s, cache expires and a fresh token is signed.
	now = now.Add(16 * time.Second)
	tok3, err := cached.SignAssertion(ctx, claimsIn)
	if err != nil {
		t.Fatalf("SignAssertion 3: %v", err)
	}
	if tok3 == tok1 || cs.calls.Load() != 2 {
		t.Fatalf("expected re-signed token after 31s (calls=%d)", cs.calls.Load())
	}

	jwksDoc, err := cached.JWKS(ctx)
	if err != nil {
		t.Fatalf("JWKS: %v", err)
	}
	verified, err := VerifyRegistryAssertion(tok3, jwksDoc, "grain-instance:alice-dev", now)
	if err != nil {
		t.Fatalf("VerifyRegistryAssertion: %v", err)
	}
	if verified.Email != "alice@example.com" {
		t.Fatalf("unexpected verified email %q", verified.Email)
	}
}

func TestRunVersionAndValidateCLI(t *testing.T) {
	ctx := context.Background()
	var out, errBuf bytes.Buffer
	if err := Run(ctx, []string{"version", "--json"}, &out, &errBuf); err != nil {
		t.Fatalf("Run version --json: %v", err)
	}
	var verMap map[string]any
	if err := json.Unmarshal(out.Bytes(), &verMap); err != nil {
		t.Fatalf("Unmarshal version JSON: %v (%s)", err, out.String())
	}

	cfgFile := filepath.Join(t.TempDir(), "registry.yaml")
	if err := os.WriteFile(cfgFile, []byte(validTestYAML()), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	out.Reset()
	errBuf.Reset()
	if err := Run(ctx, []string{"validate", "-config", cfgFile}, &out, &errBuf); err != nil {
		t.Fatalf("Run validate: %v (stderr=%s)", err, errBuf.String())
	}
	if !strings.Contains(out.String(), "is valid") {
		t.Fatalf("unexpected validate output: %s", out.String())
	}
}

func TestServerEndToEndInRegistryPackage(t *testing.T) {
	signer, err := NewEphemeralLocalSigner("reg-test-1")
	if err != nil {
		t.Fatalf("NewEphemeralLocalSigner: %v", err)
	}
	admitter, err := webauth.NewAdmitter(webauth.AdmissionConfig{
		Verifier:       fakeFrontIdentity{},
		AllowedDomains: []string{"example.com"},
		Admins:         []string{"user:admin@example.com"},
	})
	if err != nil {
		t.Fatalf("NewAdmitter: %v", err)
	}
	cfg, err := ParseConfig([]byte(validTestYAML()))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	srv, err := NewServer(ServerOptions{
		Config:   *cfg,
		Admitter: admitter,
		Signer:   signer,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://registry.example.com/.well-known/jwks.json", nil)
	srv.JWKSHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from JWKSHandler, got %d", rec.Code)
	}
}

type staticTestGroups map[string][]string

func (g staticTestGroups) IsMember(_ context.Context, group, member string) (bool, error) {
	for _, m := range g[group] {
		if m == member {
			return true, nil
		}
	}
	return false, nil
}

func (staticTestGroups) NormalizeGroup(raw string) (string, error) { return raw, nil }

// Service principals read the catalog and nothing else: GET /api/entries
// and GET /api/shapes are control plane and refuse them, while people
// still reach both.
func TestServicePrincipalsReachOnlyTheCatalog(t *testing.T) {
	signer, err := NewEphemeralLocalSigner("reg-test-sp")
	if err != nil {
		t.Fatalf("NewEphemeralLocalSigner: %v", err)
	}
	admitter, err := webauth.NewAdmitter(webauth.AdmissionConfig{
		Verifier:          fakeFrontIdentity{},
		AllowedDomains:    []string{"example.com"},
		ServicePrincipals: []string{"serviceAccount:board@proj.iam.gserviceaccount.com"},
	})
	if err != nil {
		t.Fatalf("NewAdmitter: %v", err)
	}
	cfg, err := ParseConfig([]byte(validTestYAML()))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	srv, err := NewServer(ServerOptions{Config: *cfg, Admitter: admitter, Signer: signer, Metrics: NewMetrics()})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	h := srv.Handler()
	get := func(path, email string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "https://"+cfg.Host+path, nil)
		req.Host = cfg.Host
		req.Header.Set("X-Test-Email", email)
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	const sp = "board@proj.iam.gserviceaccount.com"
	for path, want := range map[string]int{
		"/api/directory":      http.StatusOK,
		"/api/route?repo=a/b": http.StatusOK,
		"/api/entries":        http.StatusForbidden,
		"/api/shapes":         http.StatusForbidden,
	} {
		if got := get(path, sp); got != want {
			t.Errorf("service principal GET %s = %d, want %d", path, got, want)
		}
	}
	for _, path := range []string{"/api/directory", "/api/entries", "/api/shapes"} {
		if got := get(path, "alice@example.com"); got != http.StatusOK {
			t.Errorf("person GET %s = %d, want 200", path, got)
		}
	}
}

// The catalog holds listed entries only, for every caller; GET
// /api/entries holds the caller's own entries (owner or owner_group), or
// every entry for an admin -- never another owner's entry merely because
// it is listed, public or shared.
func TestCatalogAndEntryListFilters(t *testing.T) {
	now := time.Now()
	listed := &store.EntryRow{Name: "listed", OwnerEmail: "bob@example.com", Listed: true}
	unlistedPublic := &store.EntryRow{Name: "pub", OwnerEmail: "bob@example.com", Visibility: store.VisibilityPublic}
	shared := &store.EntryRow{Name: "shared", OwnerEmail: "bob@example.com", Visibility: store.VisibilityShared, AccessList: []string{"user:alice@example.com"}}
	own := &store.EntryRow{Name: "own", OwnerEmail: "alice@example.com"}
	team := &store.EntryRow{Name: "team", OwnerEmail: "bob@example.com", OwnerGroup: "group:team@example.com"}
	deleted := &store.EntryRow{Name: "gone", OwnerEmail: "bob@example.com", Listed: true, DeletedAt: &now}

	for _, c := range []struct {
		e    *store.EntryRow
		want bool
	}{{listed, true}, {unlistedPublic, false}, {shared, false}, {own, false}, {team, false}, {deleted, false}} {
		if got := inCatalog(c.e); got != c.want {
			t.Errorf("inCatalog(%s) = %v, want %v", c.e.Name, got, c.want)
		}
	}

	srv := &Server{groups: staticTestGroups{"team@example.com": {"alice@example.com"}}}
	ctx := context.Background()
	alice := &webauth.AdmittedCaller{Email: "alice@example.com", Kind: webauth.PrincipalHuman}
	admin := &webauth.AdmittedCaller{Email: "root@example.com", Kind: webauth.PrincipalHuman, IsAdmin: true}
	sp := &webauth.AdmittedCaller{Email: "board@proj.iam.gserviceaccount.com", Kind: webauth.PrincipalService}
	for _, c := range []struct {
		caller *webauth.AdmittedCaller
		e      *store.EntryRow
		want   bool
	}{
		{alice, own, true},
		{alice, team, true},
		{alice, listed, false},
		{alice, unlistedPublic, false},
		{alice, shared, false},
		{admin, listed, true},
		{admin, unlistedPublic, true},
		{sp, listed, false},
		{sp, unlistedPublic, false},
	} {
		got, err := srv.canCallerListEntry(ctx, c.caller, c.e)
		if err != nil {
			t.Fatalf("canCallerListEntry(%s, %s): %v", c.caller.Email, c.e.Name, err)
		}
		if got != c.want {
			t.Errorf("canCallerListEntry(%s, %s) = %v, want %v", c.caller.Email, c.e.Name, got, c.want)
		}
	}
}
