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
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/apierror"
	"github.com/ai-on-gke/grain/pkg/registry/api"
	"github.com/ai-on-gke/grain/pkg/registry/store"
	"github.com/ai-on-gke/grain/pkg/tunnel/mux"
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
      machine_type: n2-standard-4
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

	t.Run("e2 machine type rejected for gce", func(t *testing.T) {
		c := *cfg
		c.Compute.Shapes = []Shape{{Name: "standard", MachineType: "e2-standard-4", BootDiskGB: 50, QuotaUnits: 1}}
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), "nested virtualization") || !strings.Contains(err.Error(), "N2 or C4") {
			t.Fatalf("expected e2 nested-virtualization error, got %v", err)
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
	t.Cleanup(func() {
		regMu.Lock()
		delete(frontIdentityFactories, "test-custom-auth")
		delete(groupResolverFactories, "test-custom-groups")
		delete(delegationFactories, "test-custom-delegation")
		delete(connectTransports, "test-custom-mux")
		regMu.Unlock()
	})
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
	if _, hasEdition := verMap["edition"]; hasEdition {
		t.Fatalf("expected core build version --json to omit edition, got %s", out.String())
	}
	if _, hasCap := verMap["Edition"]; hasCap {
		t.Fatalf("expected lowercase JSON keys in version --json, got %s", out.String())
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

func TestServerAuthBoundariesMetricsAndErrorSanitization(t *testing.T) {
	signer, err := NewEphemeralLocalSigner("reg-sec-1")
	if err != nil {
		t.Fatalf("NewEphemeralLocalSigner: %v", err)
	}
	admitter, err := webauth.NewAdmitter(webauth.AdmissionConfig{
		Verifier:          fakeFrontIdentity{},
		AllowedDomains:    []string{"example.com"},
		Admins:            []string{"user:admin@example.com"},
		ServicePrincipals: []string{"serviceAccount:scanner@proj.iam.gserviceaccount.com"},
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
	h := srv.Handler()

	// 1. /metrics requires admin on the front :8080 listener.
	for _, tc := range []struct {
		name  string
		email string
		want  int
	}{
		{"unauthenticated", "", http.StatusUnauthorized},
		{"regular human", "alice@example.com", http.StatusForbidden},
		{"service principal", "scanner@proj.iam.gserviceaccount.com", http.StatusForbidden},
		{"admin human", "admin@example.com", http.StatusOK},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "https://registry.example.com/metrics", nil)
		if tc.email != "" {
			req.Header.Set("X-Test-Email", tc.email)
		}
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("/metrics for %s: got %d, want %d (body=%s)", tc.name, rec.Code, tc.want, rec.Body.String())
		}
	}

	// 2. Service principals may read /api/directory and /api/route; GET /api/entries, /api/me and /api/shapes reject them with 403.
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/api/directory", http.StatusOK},
		{"/api/route?repo=acme/repo", http.StatusOK},
		{"/api/entries", http.StatusForbidden},
		{"/api/me", http.StatusForbidden},
		{"/api/shapes", http.StatusForbidden},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "https://registry.example.com"+tc.path, nil)
		req.Header.Set("X-Test-Email", "scanner@proj.iam.gserviceaccount.com")
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("service principal %s: got %d, want %d (body=%s)", tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}

	// 3. Error format matches pkg/registry/api.Client and 500 internal details are sanitized.
	ts := httptest.NewServer(h)
	defer ts.Close()
	cli, err := api.NewClient(api.ClientConfig{
		BaseURL:           ts.URL,
		AllowInsecureHTTP: true,
		ExtraHeaders:      http.Header{"X-Test-Email": []string{"scanner@proj.iam.gserviceaccount.com"}},
	})
	if err != nil {
		t.Fatalf("api.NewClient: %v", err)
	}
	_, listErr := cli.ListEntries(context.Background(), api.ListEntriesQuery{})
	if !api.IsForbidden(listErr) {
		t.Fatalf("expected api.IsForbidden on service principal ListEntries, got %v", listErr)
	}
	var ae *api.APIError
	if !errors.As(listErr, &ae) || ae.Code != "forbidden" || !strings.Contains(ae.Message, "service principals") {
		t.Fatalf("unexpected decoded APIError: %+v", ae)
	}

	rec500 := httptest.NewRecorder()
	writeInternalError(rec500, errors.New("pq: dial tcp 10.8.0.5:5432: secret-dsn-detail"))
	if rec500.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec500.Code)
	}
	if strings.Contains(rec500.Body.String(), "10.8.0.5") || strings.Contains(rec500.Body.String(), "secret-dsn-detail") {
		t.Fatalf("500 response leaked internal error string: %s", rec500.Body.String())
	}
	var resp500 apierror.Response
	if err := json.Unmarshal(rec500.Body.Bytes(), &resp500); err != nil || resp500.Ref == "" || resp500.Code == "" {
		t.Fatalf("expected sanitized apierror.Response with ref, got %+v (err=%v)", resp500, err)
	}
}

func TestCachingSignerClampsShortJWTTTLAndBypassesRequestID(t *testing.T) {
	if got := CacheTTLForJWTTTL(10 * time.Second); got != 5*time.Second {
		t.Fatalf("CacheTTLForJWTTTL(10s) = %s, want 5s", got)
	}
	if got := CacheTTLForJWTTTL(60 * time.Second); got != 30*time.Second {
		t.Fatalf("CacheTTLForJWTTTL(60s) = %s, want 30s", got)
	}

	ctx := context.Background()
	signer, err := BuildSigner(ctx, SigningConfig{
		Provider: "local",
		KeyID:    "short-ttl-k1",
		JWTTTL:   6 * time.Second,
	})
	if err != nil {
		t.Fatalf("BuildSigner: %v", err)
	}
	cs, ok := signer.(*CachingSigner)
	if !ok || cs.cacheTTL != 3*time.Second {
		t.Fatalf("expected CachingSigner with 3s cacheTTL for 6s JWTTTL, got %#v", signer)
	}

	// Explicit per-request RequestID must never be aliased across calls.
	t1, err := signer.SignAssertion(ctx, RegistryClaims{
		Audience:  "grain-instance:dev",
		Subject:   "sub-alice",
		Email:     "alice@example.com",
		Kind:      "human",
		RequestID: "req-1",
	})
	if err != nil {
		t.Fatalf("SignAssertion req-1: %v", err)
	}
	t2, err := signer.SignAssertion(ctx, RegistryClaims{
		Audience:  "grain-instance:dev",
		Subject:   "sub-alice",
		Email:     "alice@example.com",
		Kind:      "human",
		RequestID: "req-2",
	})
	if err != nil {
		t.Fatalf("SignAssertion req-2: %v", err)
	}
	if t1 == t2 {
		t.Fatalf("SignAssertion returned cached token for distinct RequestIDs")
	}

	// A cached assertion is only reused for exactly the same claims: an
	// empty delegation actor is not the same token as no actor at all.
	base := RegistryClaims{
		Audience: "grain-instance:dev",
		Subject:  "sub-alice",
		Email:    "alice@example.com",
		Kind:     "human",
	}
	plain, err := signer.SignAssertion(ctx, base)
	if err != nil {
		t.Fatalf("SignAssertion plain: %v", err)
	}
	withActor := base
	withActor.Actor = &DelegationActor{}
	acted, err := signer.SignAssertion(ctx, withActor)
	if err != nil {
		t.Fatalf("SignAssertion with actor: %v", err)
	}
	if plain == acted {
		t.Fatalf("SignAssertion returned the actor-less cached token for claims carrying an act claim")
	}
}

func TestWSMuxSessionRoundTripClosesStreams(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	serverMux := mux.NewMux(c1, mux.Config{
		IsServer:   true,
		MaxStreams: 2,
	})
	defer serverMux.Close()
	clientMux := mux.NewMux(c2, mux.Config{
		IsServer:   false,
		MaxStreams: 2,
	})
	defer clientMux.Close()

	// handled hears from each peer handler once it has closed its end. The
	// peer closes only after writing its response, so a requester that has
	// already read that response can open the next stream before the peer
	// frees a slot under its MaxStreams cap, and the peer resets it (CI saw
	// "RoundTrip 4 failed: tunnel mux: stream reset by peer"). Waiting here
	// keeps the peer's cap out of what this test measures.
	handled := make(chan struct{}, 8)
	go func() {
		for {
			st, err := clientMux.AcceptStream()
			if err != nil {
				return
			}
			go func(s *mux.Stream) {
				defer func() { handled <- struct{}{} }()
				defer s.Close()
				req, err := http.ReadRequest(bufio.NewReader(s))
				if err != nil {
					return
				}
				_, _ = io.Copy(io.Discard, req.Body)
				_ = req.Body.Close()
				if req.URL.Path == "/drop" {
					return
				}
				_, _ = io.WriteString(s, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
			}(st)
		}
	}()

	waitHandled := func(i int) {
		t.Helper()
		select {
		case <-handled:
		case <-time.After(5 * time.Second):
			t.Fatalf("request %d: peer handler never finished", i)
		}
	}

	sess := &wsMuxSession{m: serverMux}
	// Run 6 sequential requests, one of them failing, and check after each
	// that RoundTrip's side closed its stream on Body.Close() or on the error.
	for i := 0; i < 6; i++ {
		path := "/ping"
		if i == 2 {
			path = "/drop"
		}
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://entry.local"+path, nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		resp, err := sess.RoundTrip(req)
		if path == "/drop" {
			if err == nil {
				_ = resp.Body.Close()
				t.Fatalf("expected error on /drop")
			}
			waitHandled(i)
			if n := serverMux.ActiveStreams(); n != 0 {
				t.Fatalf("after failed RoundTrip %d: %d streams still open", i, n)
			}
			continue
		}
		if err != nil {
			t.Fatalf("RoundTrip %d failed: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		if err := resp.Body.Close(); err != nil {
			t.Fatalf("resp.Body.Close %d: %v", i, err)
		}
		if string(body) != "ok" {
			t.Fatalf("unexpected body %q", string(body))
		}
		waitHandled(i)
		if n := serverMux.ActiveStreams(); n != 0 {
			t.Fatalf("after RoundTrip %d: %d streams still open", i, n)
		}
	}
}

func TestServerCatalogUnlistedAndEntryManagerIsolationPostgres(t *testing.T) {
	pgURL := startEphemeralPostgresForRegistryTest(t)
	ctx := context.Background()
	st, err := store.Open(ctx, pgURL, store.DefaultThresholds())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	unlistedFalse := false
	// Create a listed entry owned by alice (with internal BackendURL) and an unlisted entry owned by bob (with group team-b).
	if _, err := st.CreateEntry(ctx, store.CreateEntryInput{
		Name:       "alice-listed",
		Kind:       api.KindConnected,
		OwnerEmail: "alice@example.com",
		BackendURL: "http://10.128.0.42:8080",
		DeclaredScopes: []store.ScopeClaim{
			{Repo: "acme/repo", PathPrefix: "pkg/a", Primary: true},
		},
	}); err != nil {
		t.Fatalf("CreateEntry alice-listed: %v", err)
	}
	if _, err := st.CreateEntry(ctx, store.CreateEntryInput{
		Name:       "bob-unlisted",
		Kind:       api.KindConnected,
		OwnerEmail: "bob@example.com",
		OwnerGroup: "group:team-b@example.com",
		Listed:     &unlistedFalse,
		Visibility: store.VisibilityPublic,
		BackendURL: "http://10.128.0.99:8080",
		DeclaredScopes: []store.ScopeClaim{
			{Repo: "acme/repo", PathPrefix: "", Primary: false},
		},
	}); err != nil {
		t.Fatalf("CreateEntry bob-unlisted: %v", err)
	}

	groups := staticGroupResolverForTest{
		StaticGroupChecker: webauth.NewStaticGroups(map[string][]string{
			"group:team-b@example.com": {"coowner@example.com"},
		}),
	}
	admitter, err := webauth.NewAdmitter(webauth.AdmissionConfig{
		Verifier:          fakeFrontIdentity{},
		Groups:            groups,
		AllowedDomains:    []string{"example.com"},
		Admins:            []string{"user:admin@example.com"},
		ServicePrincipals: []string{"serviceAccount:scanner@proj.iam.gserviceaccount.com"},
	})
	if err != nil {
		t.Fatalf("NewAdmitter: %v", err)
	}
	signer, err := NewEphemeralLocalSigner("pg-sec-k1")
	if err != nil {
		t.Fatalf("NewEphemeralLocalSigner: %v", err)
	}
	cfg, err := ParseConfig([]byte(validTestYAML()))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	srv, err := NewServer(ServerOptions{
		Config:   *cfg,
		Store:    st,
		Admitter: admitter,
		Groups:   groups,
		Signer:   signer,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	h := srv.Handler()

	// 1. /api/directory and /api/route return ONLY listed entries, even for bob/admin/service principal.
	for _, email := range []string{"bob@example.com", "admin@example.com", "scanner@proj.iam.gserviceaccount.com"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "https://registry.example.com/api/directory", nil)
		req.Header.Set("X-Test-Email", email)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/directory (%s): %d %s", email, rec.Code, rec.Body.String())
		}
		var dirResp api.DirectoryResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &dirResp); err != nil {
			t.Fatalf("decode DirectoryResponse: %v", err)
		}
		if len(dirResp.Entries) != 1 || dirResp.Entries[0].Name != "alice-listed" {
			t.Fatalf("expected only alice-listed in directory for %s, got %+v", email, dirResp.Entries)
		}

		recRoute := httptest.NewRecorder()
		reqRoute := httptest.NewRequest(http.MethodGet, "https://registry.example.com/api/route?repo=acme/repo&path=README.md", nil)
		reqRoute.Header.Set("X-Test-Email", email)
		h.ServeHTTP(recRoute, reqRoute)
		if recRoute.Code != http.StatusOK {
			t.Fatalf("GET /api/route (%s): %d %s", email, recRoute.Code, recRoute.Body.String())
		}
		var routeResp api.RouteResponse
		if err := json.Unmarshal(recRoute.Body.Bytes(), &routeResp); err != nil {
			t.Fatalf("decode RouteResponse: %v", err)
		}
		if len(routeResp.Candidates) != 0 {
			t.Fatalf("expected unlisted entry excluded from /api/route for %s, got %+v", email, routeResp.Candidates)
		}
	}

	// 2. GET /api/entries returns ONLY entries managed by caller (owner or owner_group), never leaking alice-listed's BackendURL to carol or coowner.
	for _, tc := range []struct {
		email string
		want  []string
	}{
		{"carol@example.com", nil},
		{"coowner@example.com", []string{"bob-unlisted"}},
		{"alice@example.com", []string{"alice-listed"}},
		{"admin@example.com", []string{"alice-listed", "bob-unlisted"}},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "https://registry.example.com/api/entries", nil)
		req.Header.Set("X-Test-Email", tc.email)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/entries (%s): %d %s", tc.email, rec.Code, rec.Body.String())
		}
		var listResp api.ListEntriesResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
			t.Fatalf("decode ListEntriesResponse: %v", err)
		}
		var got []string
		for _, e := range listResp.Entries {
			got = append(got, e.Name)
		}
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Fatalf("GET /api/entries (%s): got %v, want %v", tc.email, got, tc.want)
		}
	}
}

type staticGroupResolverForTest struct {
	*webauth.StaticGroupChecker
}

func (r staticGroupResolverForTest) NormalizeGroup(raw string) (string, error) {
	return defaultNormalizeGroup(raw)
}

type toggleableGroupResolverForTest struct {
	inner   *webauth.StaticGroupChecker
	failNow atomic.Bool
}

func (r *toggleableGroupResolverForTest) IsMember(ctx context.Context, groupEmail, memberEmail string) (bool, error) {
	if r.failNow.Load() {
		return false, errors.New("simulated group directory outage")
	}
	return r.inner.IsMember(ctx, groupEmail, memberEmail)
}

func (r *toggleableGroupResolverForTest) NormalizeGroup(raw string) (string, error) {
	return defaultNormalizeGroup(raw)
}

func isAPIStatus(err error, code int) bool {
	var ae *api.APIError
	if errors.As(err, &ae) {
		return ae.StatusCode == code
	}
	return false
}

func TestConsentDelegationPolicyAuthorizeOnBehalfOf(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

	pol, err := NewConsentDelegationPolicy(AuthConfig{
		AllowedDomains:    []string{"example.com"},
		ServicePrincipals: []string{"serviceAccount:scanner@proj.iam.gserviceaccount.com"},
	}, nil)
	if err != nil {
		t.Fatalf("NewConsentDelegationPolicy: %v", err)
	}
	pol.SetNow(func() time.Time { return now })

	assertRefusedReason := func(t *testing.T, err error, wantReason string) {
		t.Helper()
		if !errors.Is(err, ErrDelegationDenied) {
			t.Fatalf("expected ErrDelegationDenied, got %v", err)
		}
		var refErr *DelegationRefusedError
		if !errors.As(err, &refErr) {
			t.Fatalf("expected *DelegationRefusedError, got %T (%v)", err, err)
		}
		if refErr.Reason != wantReason {
			t.Fatalf("reason = %q, want %q (err=%v)", refErr.Reason, wantReason, err)
		}
	}

	// 1. Unregistered app -> app_not_registered.
	_, err = pol.AuthorizeOnBehalfOf(ctx, "planning-app@proj.iam.gserviceaccount.com", "alice@example.com", "alice-vm")
	assertRefusedReason(t, err, "app_not_registered")

	// Register planning-app.
	pol.RegisterApp(store.DelegatingAppRow{
		Name:        "planning-app",
		Principal:   "serviceAccount:planning-app@proj.iam.gserviceaccount.com",
		DisplayName: "Planning App",
		Owner:       "alice@example.com",
		MaxAccess:   "write",
	})
	if ok, err := pol.IsServicePrincipalReader(ctx, "planning-app@proj.iam.gserviceaccount.com"); err != nil || !ok {
		t.Fatalf("expected registered delegating app to be admitted as catalog reader, got ok=%v err=%v", ok, err)
	}

	// 2. Disabled app -> app_disabled.
	disabledAt := now
	pol.RegisterApp(store.DelegatingAppRow{
		Name:        "planning-app",
		Principal:   "serviceAccount:planning-app@proj.iam.gserviceaccount.com",
		DisplayName: "Planning App",
		Owner:       "alice@example.com",
		MaxAccess:   "write",
		DisabledAt:  &disabledAt,
	})
	_, err = pol.AuthorizeOnBehalfOf(ctx, "planning-app@proj.iam.gserviceaccount.com", "alice@example.com", "alice-vm")
	assertRefusedReason(t, err, "app_disabled")
	if ok, _ := pol.IsServicePrincipalReader(ctx, "planning-app@proj.iam.gserviceaccount.com"); ok {
		t.Fatalf("expected disabled delegating app to be refused as catalog reader")
	}

	// Re-enable planning-app.
	pol.RegisterApp(store.DelegatingAppRow{
		Name:        "planning-app",
		Principal:   "serviceAccount:planning-app@proj.iam.gserviceaccount.com",
		DisplayName: "Planning App",
		Owner:       "alice@example.com",
		MaxAccess:   "write",
	})

	// 3. Target not in allowed domain -> person_not_admitted.
	_, err = pol.AuthorizeOnBehalfOf(ctx, "planning-app@proj.iam.gserviceaccount.com", "outsider@other.org", "alice-vm")
	assertRefusedReason(t, err, "person_not_admitted")

	// 4. No grant -> no_grant.
	_, err = pol.AuthorizeOnBehalfOf(ctx, "planning-app@proj.iam.gserviceaccount.com", "alice@example.com", "alice-vm")
	assertRefusedReason(t, err, "no_grant")

	// 5. Expired grant -> grant_expired.
	pol.UpsertGrant(store.DelegationGrantRow{
		ID:        "dlg_expired",
		App:       "planning-app",
		Person:    "alice@example.com",
		Entries:   []string{"*"},
		Access:    "write",
		ExpiresAt: now.Add(-time.Minute),
	})
	_, err = pol.AuthorizeOnBehalfOf(ctx, "planning-app@proj.iam.gserviceaccount.com", "alice@example.com", "alice-vm")
	assertRefusedReason(t, err, "grant_expired")

	// 6. Entry out of reach -> entry_out_of_reach.
	pol.UpsertGrant(store.DelegationGrantRow{
		ID:        "dlg_scoped",
		App:       "planning-app",
		Person:    "alice@example.com",
		Entries:   []string{"other-vm"},
		Access:    "write",
		ExpiresAt: now.Add(time.Hour),
	})
	_, err = pol.AuthorizeOnBehalfOf(ctx, "planning-app@proj.iam.gserviceaccount.com", "alice@example.com", "alice-vm")
	assertRefusedReason(t, err, "entry_out_of_reach")

	// 7. Live grant covering alice-vm -> returns DelegationActor with App, Grant, Access.
	pol.UpsertGrant(store.DelegationGrantRow{
		ID:        "dlg_live",
		App:       "planning-app",
		Person:    "alice@example.com",
		Entries:   []string{"alice-vm"},
		Access:    "write",
		ExpiresAt: now.Add(time.Hour),
	})
	act, err := pol.AuthorizeOnBehalfOf(ctx, "planning-app@proj.iam.gserviceaccount.com", "alice@example.com", "alice-vm")
	if err != nil {
		t.Fatalf("AuthorizeOnBehalfOf valid: %v", err)
	}
	if act.Sub != "planning-app@proj.iam.gserviceaccount.com" || act.Kind != "service" || act.App != "planning-app" || act.Grant != "dlg_live" || act.Access != "write" {
		t.Fatalf("unexpected DelegationActor: %+v", act)
	}
}

func TestCachingSignerActClaimAndInvalidation(t *testing.T) {
	ctx := context.Background()
	local, err := NewEphemeralLocalSigner("k1")
	if err != nil {
		t.Fatalf("NewEphemeralLocalSigner: %v", err)
	}
	caching := NewCachingSigner(local, 30*time.Second)

	plainTok, err := caching.SignAssertion(ctx, RegistryClaims{
		Audience: "https://alice-vm.instances.example.com",
		Subject:  "alice@example.com",
		Email:    "alice@example.com",
		Kind:     "human",
	})
	if err != nil {
		t.Fatalf("SignAssertion plain: %v", err)
	}

	delTok1, err := caching.SignAssertion(ctx, RegistryClaims{
		Audience: "https://alice-vm.instances.example.com",
		Subject:  "alice@example.com",
		Email:    "alice@example.com",
		Kind:     "human",
		Actor: &DelegationActor{
			Sub:    "planning-app@proj.iam.gserviceaccount.com",
			Kind:   "service",
			App:    "planning-app",
			Grant:  "dlg_1",
			Access: "write",
		},
	})
	if err != nil {
		t.Fatalf("SignAssertion delTok1: %v", err)
	}
	if delTok1 == plainTok {
		t.Fatalf("expected delegated assertion not to alias plain assertion in cache")
	}

	// Repeat with same grant -> cache hit.
	delTok1Hit, err := caching.SignAssertion(ctx, RegistryClaims{
		Audience: "https://alice-vm.instances.example.com",
		Subject:  "alice@example.com",
		Email:    "alice@example.com",
		Kind:     "human",
		Actor: &DelegationActor{
			Sub:    "planning-app@proj.iam.gserviceaccount.com",
			Kind:   "service",
			App:    "planning-app",
			Grant:  "dlg_1",
			Access: "write",
		},
	})
	if err != nil || delTok1Hit != delTok1 {
		t.Fatalf("expected cache hit for same (sub, act.sub, act.grant, aud)")
	}

	// Different grant -> different token.
	delTok2, err := caching.SignAssertion(ctx, RegistryClaims{
		Audience: "https://alice-vm.instances.example.com",
		Subject:  "alice@example.com",
		Email:    "alice@example.com",
		Kind:     "human",
		Actor: &DelegationActor{
			Sub:    "planning-app@proj.iam.gserviceaccount.com",
			Kind:   "service",
			App:    "planning-app",
			Grant:  "dlg_2",
			Access: "read",
		},
	})
	if err != nil || delTok2 == delTok1 {
		t.Fatalf("expected distinct cached assertion for dlg_2")
	}

	// InvalidateGrant("dlg_1") evicts dlg_1 while preserving dlg_2.
	caching.InvalidateGrant("dlg_1")
	delTok1After, err := caching.SignAssertion(ctx, RegistryClaims{
		Audience: "https://alice-vm.instances.example.com",
		Subject:  "alice@example.com",
		Email:    "alice@example.com",
		Kind:     "human",
		Actor: &DelegationActor{
			Sub:    "planning-app@proj.iam.gserviceaccount.com",
			Kind:   "service",
			App:    "planning-app",
			Grant:  "dlg_1",
			Access: "write",
		},
	})
	if err != nil || delTok1After == delTok1 {
		t.Fatalf("expected fresh token after InvalidateGrant(dlg_1)")
	}
	delTok2Hit, err := caching.SignAssertion(ctx, RegistryClaims{
		Audience: "https://alice-vm.instances.example.com",
		Subject:  "alice@example.com",
		Email:    "alice@example.com",
		Kind:     "human",
		Actor: &DelegationActor{
			Sub:    "planning-app@proj.iam.gserviceaccount.com",
			Kind:   "service",
			App:    "planning-app",
			Grant:  "dlg_2",
			Access: "read",
		},
	})
	if err != nil || delTok2Hit != delTok2 {
		t.Fatalf("expected dlg_2 to remain cached after InvalidateGrant(dlg_1)")
	}

	// Cache a token for a second app ("triage-app"), then InvalidateApp("planning-app"):
	// both planning-app grants (dlg_1 and dlg_2) are evicted while triage-app and plainTok remain cached.
	otherAppClaims := RegistryClaims{
		Audience: "https://alice-vm.instances.example.com",
		Subject:  "alice@example.com",
		Email:    "alice@example.com",
		Kind:     "human",
		Actor: &DelegationActor{
			Sub:    "triage-app@proj.iam.gserviceaccount.com",
			Kind:   "service",
			App:    "triage-app",
			Grant:  "dlg_3",
			Access: "read",
		},
	}
	otherAppTok, err := caching.SignAssertion(ctx, otherAppClaims)
	if err != nil {
		t.Fatalf("SignAssertion otherAppTok: %v", err)
	}
	caching.InvalidateApp("planning-app")
	delTok1AfterApp, err := caching.SignAssertion(ctx, RegistryClaims{
		Audience: "https://alice-vm.instances.example.com",
		Subject:  "alice@example.com",
		Email:    "alice@example.com",
		Kind:     "human",
		Actor: &DelegationActor{
			Sub:    "planning-app@proj.iam.gserviceaccount.com",
			Kind:   "service",
			App:    "planning-app",
			Grant:  "dlg_1",
			Access: "write",
		},
	})
	if err != nil || delTok1AfterApp == delTok1After {
		t.Fatalf("expected fresh token for dlg_1 after InvalidateApp(planning-app)")
	}
	delTok2AfterApp, err := caching.SignAssertion(ctx, RegistryClaims{
		Audience: "https://alice-vm.instances.example.com",
		Subject:  "alice@example.com",
		Email:    "alice@example.com",
		Kind:     "human",
		Actor: &DelegationActor{
			Sub:    "planning-app@proj.iam.gserviceaccount.com",
			Kind:   "service",
			App:    "planning-app",
			Grant:  "dlg_2",
			Access: "read",
		},
	})
	if err != nil || delTok2AfterApp == delTok2 {
		t.Fatalf("expected fresh token for dlg_2 after InvalidateApp(planning-app)")
	}
	if otherHit, err := caching.SignAssertion(ctx, otherAppClaims); err != nil || otherHit != otherAppTok {
		t.Fatalf("expected triage-app assertion to remain cached after InvalidateApp(planning-app)")
	}
	if plainHit, err := caching.SignAssertion(ctx, RegistryClaims{
		Audience: "https://alice-vm.instances.example.com",
		Subject:  "alice@example.com",
		Email:    "alice@example.com",
		Kind:     "human",
	}); err != nil || plainHit != plainTok {
		t.Fatalf("expected non-delegated assertion to remain cached after InvalidateApp(planning-app)")
	}
}

func TestStep12CatalogCRUDGroupAuthHeartbeatsDirectoryAndRouteEndToEnd(t *testing.T) {
	pgURL := startEphemeralPostgresForRegistryTest(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 6, 14, 0, 0, 0, time.UTC)
	st, err := store.Open(ctx, pgURL, store.DefaultThresholds())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	st.SetNow(func() time.Time { return now })

	if err := st.SeedShapes(ctx, []store.ShapeRow{
		{Name: "small", MachineType: "n2-standard-4", BootDiskGB: 50, QuotaUnits: 1},
		{Name: "large", MachineType: "n2-standard-8", BootDiskGB: 100, QuotaUnits: 2},
	}); err != nil {
		t.Fatalf("SeedShapes: %v", err)
	}

	groups := &toggleableGroupResolverForTest{
		inner: webauth.NewStaticGroups(map[string][]string{
			"group:team-alpha@example.com": {"alice@example.com", "coowner@example.com"},
			"group:team-other@example.com": {"outsider@example.com"},
		}),
	}
	admitter, err := webauth.NewAdmitter(webauth.AdmissionConfig{
		Verifier:          fakeFrontIdentity{},
		Groups:            groups,
		AllowedDomains:    []string{"example.com"},
		Admins:            []string{"user:admin@example.com"},
		ServicePrincipals: []string{"serviceAccount:planner@proj.iam.gserviceaccount.com"},
	})
	if err != nil {
		t.Fatalf("NewAdmitter: %v", err)
	}
	signer, err := NewEphemeralLocalSigner("step12-k1")
	if err != nil {
		t.Fatalf("NewEphemeralLocalSigner: %v", err)
	}
	cfg, err := ParseConfig([]byte(validTestYAML()))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	srv, err := NewServer(ServerOptions{
		Config:    *cfg,
		Store:     st,
		Admitter:  admitter,
		Groups:    groups,
		Signer:    signer,
		ReplicaID: "rep-step12",
		PeerAddr:  "127.0.0.1:18082",
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.Router().SetNow(func() time.Time { return now })
	_ = st.UpsertReplica(ctx, "rep-step12", "127.0.0.1:18082")

	mainTS := httptest.NewServer(srv.Handler())
	defer mainTS.Close()
	pubTS := httptest.NewServer(srv.PublicHandler())
	defer pubTS.Close()

	clientFor := func(email string) *api.Client {
		c, err := api.NewClient(api.ClientConfig{
			BaseURL:           mainTS.URL,
			AllowInsecureHTTP: true,
			ExtraHeaders:      http.Header{"X-Test-Email": []string{email}},
		})
		if err != nil {
			t.Fatalf("NewClient(%s): %v", email, err)
		}
		return c
	}

	aliceCli := clientFor("alice@example.com")
	coownerCli := clientFor("coowner@example.com")
	bobCli := clientFor("bob@example.com")
	plannerCli := clientFor("planner@proj.iam.gserviceaccount.com")

	// 1. Setting an owner_group to which the caller does not belong is rejected with 403.
	if _, err := aliceCli.CreateEntry(ctx, api.CreateEntryRequest{
		Name:       "bad-group-entry",
		Kind:       api.KindConnected,
		OwnerGroup: "group:team-other@example.com",
	}); !isAPIStatus(err, http.StatusForbidden) {
		t.Fatalf("expected 403 when creating entry with non-member owner_group, got %v", err)
	}

	// 2. Create a connected entry with owner_group=group:team-alpha@example.com (omitting listed -> defaults to false).
	createAlpha, err := aliceCli.CreateEntry(ctx, api.CreateEntryRequest{
		Name:               "alpha-conn",
		Kind:               api.KindConnected,
		HostKind:           api.HostKindWorkstation,
		DisplayName:        "Alpha Workstation",
		Purpose:            "Payments & autoscaler controller",
		OwnerGroup:         "group:team-alpha@example.com",
		ConnectedPrincipal: "serviceAccount:ci-bot@proj.iam.gserviceaccount.com",
		Tags:               []string{"payments", "prod"},
		Scopes: []api.ScopeInput{
			{Repo: "acme/monorepo", PathPrefix: "services/payments", Role: api.RolePrimary},
		},
	})
	if err != nil {
		t.Fatalf("CreateEntry alpha-conn: %v", err)
	}
	if createAlpha.Entry.Listed {
		t.Fatalf("expected POST /api/entries to default listed=false, got listed=true")
	}
	if !strings.HasPrefix(createAlpha.RegistrationToken, "grc_") || createAlpha.Credential == nil {
		t.Fatalf("expected grc_ token and Credential metadata for connected entry, got %+v", createAlpha)
	}

	// Unlisted entry is never in CatalogCache.Snapshot() or /api/directory.
	dirBeforeList, err := plannerCli.Directory(ctx, api.DirectoryQuery{})
	if err != nil || len(dirBeforeList.Entries) != 0 {
		t.Fatalf("expected 0 directory entries while unlisted, got %+v (err=%v)", dirBeforeList, err)
	}

	// 3. Verified owner_group member (coowner@example.com) can co-manage alpha-conn:
	// flip listed=true, update display_name, and resize/create instance entries where quota stays on owner (alice).
	listedTrue := true
	newDisplay := "Alpha Team Controller"
	updatedAlpha, err := coownerCli.UpdateEntry(ctx, "alpha-conn", api.UpdateEntryRequest{
		DisplayName: &newDisplay,
		Listed:      &listedTrue,
	})
	if err != nil {
		t.Fatalf("coowner UpdateEntry alpha-conn: %v", err)
	}
	if !updatedAlpha.Listed || updatedAlpha.DisplayName != newDisplay {
		t.Fatalf("unexpected updated entry: %+v", updatedAlpha)
	}

	// Non-member bob cannot update or view credentials on alpha-conn.
	if _, err := bobCli.UpdateEntry(ctx, "alpha-conn", api.UpdateEntryRequest{DisplayName: &newDisplay}); !isAPIStatus(err, http.StatusForbidden) {
		t.Fatalf("expected 403 for bob updating alpha-conn, got %v", err)
	}

	// 4. GroupResolver failure fails closed with 503 + Retry-After: 5 for group-dependent checks,
	// while direct owner (alice) remains unaffected!
	groups.failNow.Store(true)
	if _, err := coownerCli.GetEntry(ctx, "alpha-conn"); !isAPIStatus(err, http.StatusServiceUnavailable) {
		t.Fatalf("expected 503 for coowner when GroupResolver fails, got %v", err)
	}
	if _, err := aliceCli.GetEntry(ctx, "alpha-conn"); err != nil {
		t.Fatalf("expected direct owner alice to succeed even when GroupResolver fails, got %v", err)
	}
	groups.failNow.Store(false)

	// 5. Synchronous 409 primary-prefix conflict on POST /api/entries and PUT /api/entries/<name>/scopes.
	_, err = bobCli.CreateEntry(ctx, api.CreateEntryRequest{
		Name:   "bob-overlap",
		Kind:   api.KindConnected,
		Listed: &listedTrue,
		Scopes: []api.ScopeInput{
			{Repo: "acme/monorepo", PathPrefix: "services/payments/sub", Role: api.RolePrimary},
		},
	})
	if !isAPIStatus(err, http.StatusConflict) {
		t.Fatalf("expected 409 Conflict on overlapping listed primary scope, got %v", err)
	}

	// 6. Heartbeats on :8081 (grc_ token) and :8080 (FrontIdentity + dual-binding) and live tunnel liveness.
	// Simulate active tunnel session for alpha-conn so connected entry is online once heartbeat lands.
	alphaRow, err := st.GetEntryByName(ctx, "alpha-conn")
	if err != nil {
		t.Fatalf("GetEntryByName alpha-conn: %v", err)
	}
	if _, err := st.AcquireTunnelSessionWithCred(ctx, alphaRow.ID, "rep-step12", "198.51.100.20:5000", createAlpha.Credential.CredentialID); err != nil {
		t.Fatalf("AcquireTunnelSessionWithCred: %v", err)
	}
	_, _ = srv.Router().TunnelCache().RefreshEntry(ctx, "alpha-conn")

	pubHbCli, err := api.NewClient(api.ClientConfig{
		BaseURL:           pubTS.URL,
		AllowInsecureHTTP: true,
		RegistrationToken: createAlpha.RegistrationToken,
	})
	if err != nil {
		t.Fatalf("NewClient pubHbCli: %v", err)
	}
	hbResp, err := pubHbCli.Heartbeat(ctx, "alpha-conn", api.HeartbeatRequest{
		GrainVersion:       "v1.5.0",
		GrainCommit:        "deadbeef",
		HostKind:           api.HostKindWorkstation,
		Health:             api.HealthOK,
		ActiveTasks:        1,
		AwaitingReplyTasks: 2,
		Scopes: []api.HeartbeatScope{
			{Repo: "acme/monorepo", PathPrefix: "", Role: api.RolePrimary},
		},
	})
	if err != nil {
		t.Fatalf("Heartbeat on :8081: %v", err)
	}
	if hbResp.Status != api.StatusOnline || hbResp.NextIntervalSeconds != 15 {
		t.Fatalf("expected online and next_interval_seconds=15, got %+v", hbResp)
	}
	// Declared scope narrowed acme/monorepo "" -> "services/payments".
	if len(hbResp.ActiveScopes) != 1 || hbResp.ActiveScopes[0].PathPrefix != "services/payments" {
		t.Fatalf("expected active scope narrowed to services/payments, got %+v", hbResp.ActiveScopes)
	}

	// Also verify FrontIdentity heartbeat as connected_principal (`ci-bot@proj.iam.gserviceaccount.com`) on :8080.
	ciBotCli := clientFor("ci-bot@proj.iam.gserviceaccount.com")
	if _, err := ciBotCli.Heartbeat(ctx, "alpha-conn", api.HeartbeatRequest{
		GrainVersion: "v1.5.0",
		GrainCommit:  "deadbeef",
		HostKind:     api.HostKindWorkstation,
		Health:       api.HealthOK,
		ActiveTasks:  1,
		Scopes: []api.HeartbeatScope{
			{Repo: "acme/monorepo", PathPrefix: "", Role: api.RolePrimary},
		},
	}); err != nil {
		t.Fatalf("Heartbeat as connected_principal on :8080: %v", err)
	}

	// 7. Create a second listed connected entry (`bob-catchall`) with secondary repo-wide scope on acme/monorepo.
	createBob, err := bobCli.CreateEntry(ctx, api.CreateEntryRequest{
		Name:     "bob-catchall",
		Kind:     api.KindConnected,
		HostKind: api.HostKindKubernetes,
		Listed:   &listedTrue,
		Tags:     []string{"catchall"},
		Scopes: []api.ScopeInput{
			{Repo: "acme/monorepo", PathPrefix: "", Role: api.RoleSecondary},
		},
	})
	if err != nil {
		t.Fatalf("CreateEntry bob-catchall: %v", err)
	}
	bobRow, _ := st.GetEntryByName(ctx, "bob-catchall")
	bobTS, err := st.AcquireTunnelSessionWithCred(ctx, bobRow.ID, "rep-step12", "198.51.100.21:5000", createBob.Credential.CredentialID)
	if err != nil {
		t.Fatalf("AcquireTunnelSessionWithCred bob: %v", err)
	}
	_, _ = srv.Router().TunnelCache().RefreshEntry(ctx, "bob-catchall")
	_, err = bobCli.Heartbeat(ctx, "bob-catchall", api.HeartbeatRequest{
		GrainVersion: "v1.5.0",
		HostKind:     api.HostKindKubernetes,
		Health:       api.HealthOK,
		ActiveTasks:  0,
		Scopes: []api.HeartbeatScope{
			{Repo: "acme/monorepo", PathPrefix: "", Role: api.RoleSecondary},
		},
	})
	if err != nil {
		t.Fatalf("Heartbeat bob-catchall: %v", err)
	}

	// 8. Verify CatalogCache-backed GET /api/directory and GET /api/route (and zero-SQL hot path!).
	dirAll, err := plannerCli.Directory(ctx, api.DirectoryQuery{Repo: "acme/monorepo"})
	if err != nil {
		t.Fatalf("Directory: %v", err)
	}
	if len(dirAll.Entries) != 2 {
		t.Fatalf("expected 2 listed entries in directory, got %+v", dirAll.Entries)
	}
	reloadsBeforeHotPath := srv.Router().CatalogCache().ReloadCount()

	// Multiple /api/directory and /api/route queries must execute with ZERO additional SQL reloads!
	for i := 0; i < 5; i++ {
		d, err := plannerCli.Directory(ctx, api.DirectoryQuery{Tag: "payments", HostKind: api.HostKindWorkstation})
		if err != nil || len(d.Entries) != 1 || d.Entries[0].Name != "alpha-conn" {
			t.Fatalf("filtered Directory failed: %+v (err=%v)", d, err)
		}
		rtRes, err := plannerCli.Route(ctx, api.RouteQuery{Repo: "acme/monorepo", Path: "services/payments/charge.go"})
		if err != nil || len(rtRes.Candidates) != 2 || rtRes.Candidates[0].Name != "alpha-conn" || rtRes.Candidates[1].Name != "bob-catchall" {
			t.Fatalf("unexpected route ranking: %+v (err=%v)", rtRes, err)
		}
	}
	if srv.Router().CatalogCache().ReloadCount() != reloadsBeforeHotPath {
		t.Fatalf("expected zero SQL reloads on /api/directory and /api/route hot path, reloads changed %d -> %d", reloadsBeforeHotPath, srv.Router().CatalogCache().ReloadCount())
	}

	// Path traversal (`..`) on /api/route is rejected with 400 Bad Request.
	if _, err := plannerCli.Route(ctx, api.RouteQuery{Repo: "acme/monorepo", Path: "services/../secrets"}); !isAPIStatus(err, http.StatusBadRequest) {
		t.Fatalf("expected 400 Bad Request on .. path traversal in /api/route, got %v", err)
	}

	// Advance clock by 2 minutes so alpha-conn becomes offline while bob-catchall stays online:
	// By default, offline primary (alpha-conn) stays in rank 1 with status=offline ahead of online secondary (bob-catchall);
	// with include_offline=false, only online secondary (bob-catchall) is returned!
	now = now.Add(2 * time.Minute)
	_ = st.UpsertReplica(ctx, "rep-step12", "127.0.0.1:18082")
	_, _ = st.RecordHeartbeat(ctx, bobRow.ID, api.HeartbeatRequest{
		GrainVersion: "v1.5.0",
		HostKind:     api.HostKindKubernetes,
		Health:       api.HealthOK,
		Scopes: []api.HeartbeatScope{
			{Repo: "acme/monorepo", PathPrefix: "", Role: api.RoleSecondary},
		},
	})
	_ = st.TouchTunnelSession(ctx, bobRow.ID, bobTS.SessionEpoch, 5)
	_, _ = srv.Router().TunnelCache().RefreshEntry(ctx, "bob-catchall")

	rtOfflinePrimary, err := plannerCli.Route(ctx, api.RouteQuery{Repo: "acme/monorepo", Path: "services/payments/charge.go"})
	if err != nil {
		t.Fatalf("Route with offline primary: %v", err)
	}
	if len(rtOfflinePrimary.Candidates) != 2 ||
		rtOfflinePrimary.Candidates[0].Name != "alpha-conn" ||
		rtOfflinePrimary.Candidates[0].Status != api.StatusOffline ||
		rtOfflinePrimary.Candidates[1].Name != "bob-catchall" ||
		rtOfflinePrimary.Candidates[1].Status != api.StatusOnline {
		t.Fatalf("expected offline primary in rank 1 ahead of online secondary, got %+v", rtOfflinePrimary.Candidates)
	}

	// With include_offline=false, even offline primaries are excluded so only online secondary is returned.
	reqOnlineOnly := httptest.NewRequest(http.MethodGet, "https://registry.example.com/api/route?repo=acme/monorepo&path=services/payments/charge.go&include_offline=false", nil)
	reqOnlineOnly.Header.Set("X-Test-Email", "planner@proj.iam.gserviceaccount.com")
	recOnlineOnly := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recOnlineOnly, reqOnlineOnly)
	if recOnlineOnly.Code != http.StatusOK {
		t.Fatalf("GET /api/route?include_offline=false: %d %s", recOnlineOnly.Code, recOnlineOnly.Body.String())
	}
	var rtOnlineOnly api.RouteResponse
	if err := json.Unmarshal(recOnlineOnly.Body.Bytes(), &rtOnlineOnly); err != nil {
		t.Fatalf("decode RouteResponse: %v", err)
	}
	if len(rtOnlineOnly.Candidates) != 1 || rtOnlineOnly.Candidates[0].Name != "bob-catchall" {
		t.Fatalf("expected only online secondary when include_offline=false, got %+v", rtOnlineOnly.Candidates)
	}

	// 9. Two-step ownership transfer (`POST /api/entries/<name>/transfer` -> `.../transfer/accept`).
	offered, err := coownerCli.OfferTransfer(ctx, "alpha-conn", api.TransferRequest{
		To: "bob@example.com",
	})
	if err != nil || offered.PendingOwner != "bob@example.com" {
		t.Fatalf("OfferTransfer by coowner failed: %+v (err=%v)", offered, err)
	}
	// Non-recipient (coowner) cannot accept transfer on bob's behalf.
	if _, err := coownerCli.AcceptTransfer(ctx, "alpha-conn"); !isAPIStatus(err, http.StatusBadRequest) && !isAPIStatus(err, http.StatusForbidden) {
		t.Fatalf("expected non-recipient AcceptTransfer to fail, got %v", err)
	}
	// Recipient (bob) accepts transfer; owner_group is preserved across transfer unless cleared.
	accepted, err := bobCli.AcceptTransfer(ctx, "alpha-conn")
	if err != nil || accepted.Owner != "bob@example.com" || accepted.OwnerGroup != "group:team-alpha@example.com" {
		t.Fatalf("AcceptTransfer by bob failed: %+v (err=%v)", accepted, err)
	}

	// 10. Status & AdminSummary authorization, unlisted privacy, and replica TTL health.
	listedFalse := false
	secretConn, err := aliceCli.CreateEntry(ctx, api.CreateEntryRequest{
		Name:   "alice-unlisted-secret",
		Kind:   api.KindConnected,
		Listed: &listedFalse,
	})
	if err != nil {
		t.Fatalf("CreateEntry alice-unlisted-secret: %v", err)
	}
	secretRow, err := st.GetEntryByName(ctx, "alice-unlisted-secret")
	if err != nil {
		t.Fatalf("GetEntryByName alice-unlisted-secret: %v", err)
	}
	_, _ = st.RecordHeartbeat(ctx, secretRow.ID, api.HeartbeatRequest{
		GrainVersion: "v9.9.9-unlisted-private",
		HostKind:     api.HostKindWorkstation,
		Health:       api.HealthOK,
	})
	_, _ = st.AcquireTunnelSessionWithCred(ctx, secretRow.ID, "rep-step12", "198.51.100.99:5000", secretConn.Credential.CredentialID)

	// Register a second replica whose heartbeat is fresh now, then advance past ReplicaTTL for stale-replica check.
	_ = st.UpsertReplica(ctx, "rep-stale", "10.0.0.99:8082")
	now = now.Add(20 * time.Second)
	_ = st.UpsertReplica(ctx, "rep-step12", "127.0.0.1:18082")
	_, _ = srv.Router().TunnelCache().RefreshEntry(ctx, "alice-unlisted-secret")
	secretDetail, err := aliceCli.GetEntry(ctx, "alice-unlisted-secret")
	if err != nil || secretDetail.TunnelConnected == nil || !*secretDetail.TunnelConnected {
		t.Fatalf("expected enrichAPIEntry TunnelConnected=true under store clock, got %+v (err=%v)", secretDetail, err)
	}

	// Non-admin cannot call /api/admin/summary (403) and does NOT see unlisted versions, counts, tunnels, or peer_addr on /api/status.
	if _, err := bobCli.AdminSummary(ctx); !isAPIStatus(err, http.StatusForbidden) {
		t.Fatalf("expected 403 Forbidden for non-admin AdminSummary, got %v", err)
	}
	bobStatus, err := bobCli.Status(ctx)
	if err != nil {
		t.Fatalf("bob Status: %v", err)
	}
	if _, leaked := bobStatus.VersionCounts["v9.9.9-unlisted-private"]; leaked {
		t.Fatalf("non-admin /api/status leaked unlisted entry version: %+v", bobStatus.VersionCounts)
	}
	for _, rep := range bobStatus.Replicas {
		if rep.PeerAddr != "" {
			t.Fatalf("non-admin /api/status leaked replica peer_addr: %+v", rep)
		}
		if rep.ReplicaID == "rep-stale" && rep.Healthy {
			t.Fatalf("stale replica marked healthy in /api/status: %+v", rep)
		}
		if rep.ReplicaID == "rep-step12" && !rep.Healthy {
			t.Fatalf("fresh replica marked unhealthy in /api/status: %+v", rep)
		}
	}

	adminCli := clientFor("admin@example.com")
	adminStatus, err := adminCli.Status(ctx)
	if err != nil {
		t.Fatalf("admin Status: %v", err)
	}
	if adminStatus.VersionCounts["v9.9.9-unlisted-private"] != 1 {
		t.Fatalf("expected admin /api/status to include unlisted entry version, got %+v", adminStatus.VersionCounts)
	}
	if adminStatus.TotalEntries != bobStatus.TotalEntries+1 {
		t.Fatalf("expected admin TotalEntries (%d) == non-admin TotalEntries (%d) + 1", adminStatus.TotalEntries, bobStatus.TotalEntries)
	}
	if adminStatus.ActiveTunnels != bobStatus.ActiveTunnels+1 {
		t.Fatalf("expected admin ActiveTunnels (%d) == non-admin ActiveTunnels (%d) + 1", adminStatus.ActiveTunnels, bobStatus.ActiveTunnels)
	}

	adminSum, err := adminCli.AdminSummary(ctx)
	if err != nil {
		t.Fatalf("admin AdminSummary: %v", err)
	}
	for _, rep := range adminSum.Replicas {
		if rep.ReplicaID == "rep-stale" && rep.Healthy {
			t.Fatalf("stale replica marked healthy in /api/admin/summary: %+v", rep)
		}
		if rep.ReplicaID == "rep-step12" && !rep.Healthy {
			t.Fatalf("fresh replica marked unhealthy in /api/admin/summary: %+v", rep)
		}
	}
}

func TestDemoRefusesNonLoopbackAddr(t *testing.T) {
	for _, bad := range []string{"0.0.0.0:0", ":0", "10.0.0.5:8080", "example.com:8080"} {
		var out, errBuf strings.Builder
		err := Run(context.Background(), []string{"demo", "-addr", bad}, &out, &errBuf)
		if err == nil || !strings.Contains(err.Error(), "loopback") {
			t.Fatalf("Run(demo -addr %q) = %v, want loopback refusal", bad, err)
		}
	}
}

func TestStep17StopStartAutoUpgradeAndMaintenanceWindowEndToEnd(t *testing.T) {
	// 1. Validate ComputeConfig auto_upgrade and maintenance_window parsing (same-day and overnight UTC windows).
	if _, _, err := ParseMaintenanceWindow("02:00-06:00"); err != nil {
		t.Fatalf("ParseMaintenanceWindow 02:00-06:00: %v", err)
	}
	if !InMaintenanceWindow("02:00-06:00", time.Date(2026, 11, 7, 3, 30, 0, 0, time.UTC)) {
		t.Fatalf("expected 03:30 UTC inside 02:00-06:00")
	}
	if InMaintenanceWindow("02:00-06:00", time.Date(2026, 11, 7, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("expected 06:00 UTC outside [02:00, 06:00)")
	}

	if _, _, err := ParseMaintenanceWindow("22:00-02:00"); err != nil {
		t.Fatalf("ParseMaintenanceWindow 22:00-02:00: %v", err)
	}
	if !InMaintenanceWindow("22:00-02:00", time.Date(2026, 11, 7, 23, 15, 0, 0, time.UTC)) || !InMaintenanceWindow("22:00-02:00", time.Date(2026, 11, 8, 1, 45, 0, 0, time.UTC)) {
		t.Fatalf("expected 23:15 and 01:45 UTC inside overnight window 22:00-02:00")
	}
	if InMaintenanceWindow("22:00-02:00", time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("expected 12:00 UTC outside overnight window 22:00-02:00")
	}

	if _, _, err := ParseMaintenanceWindow("25:00-06:00"); err == nil {
		t.Fatalf("expected invalid maintenance_window hour to fail")
	}

	badCfgYAML := strings.Replace(validTestYAML(), "default_shape: standard", "default_shape: standard\n  auto_upgrade: major", 1)
	if _, err := ParseConfig([]byte(badCfgYAML)); err == nil {
		t.Fatalf("expected invalid compute.auto_upgrade=major to fail validation")
	}

	// 2. End-to-end HTTP test for POST /api/entries/{name}/stop, POST /api/entries/{name}/start, and auto_upgrade/auto_wake fields.
	pgURL := startEphemeralPostgresForRegistryTest(t)
	ctx := context.Background()
	st, err := store.Open(ctx, pgURL, store.DefaultThresholds())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	if err := st.SeedShapes(ctx, []store.ShapeRow{
		{Name: "small", MachineType: "n2-standard-4", BootDiskGB: 50, QuotaUnits: 1},
	}); err != nil {
		t.Fatalf("SeedShapes: %v", err)
	}

	admitter, err := webauth.NewAdmitter(webauth.AdmissionConfig{
		Verifier:       fakeFrontIdentity{},
		AllowedDomains: []string{"example.com"},
		Admins:         []string{"user:admin@example.com"},
	})
	if err != nil {
		t.Fatalf("NewAdmitter: %v", err)
	}
	signer, err := NewEphemeralLocalSigner("step17-k1")
	if err != nil {
		t.Fatalf("NewEphemeralLocalSigner: %v", err)
	}
	cfg, err := ParseConfig([]byte(validTestYAML()))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	srv, err := NewServer(ServerOptions{
		Config:    *cfg,
		Store:     st,
		Admitter:  admitter,
		Signer:    signer,
		ReplicaID: "rep-step17",
		PeerAddr:  "127.0.0.1:18082",
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	clientFor := func(email string) *api.Client {
		c, err := api.NewClient(api.ClientConfig{
			BaseURL:           ts.URL,
			AllowInsecureHTTP: true,
			ExtraHeaders:      http.Header{"X-Test-Email": []string{email}},
		})
		if err != nil {
			t.Fatalf("NewClient(%s): %v", email, err)
		}
		return c
	}

	aliceCli := clientFor("alice@example.com")
	bobCli := clientFor("bob@example.com")

	autoWakeFalse := false
	created, err := aliceCli.CreateEntry(ctx, api.CreateEntryRequest{
		Name:        "alice-vm17",
		Kind:        api.KindInstance,
		Shape:       "small",
		AutoUpgrade: string(api.AutoUpgradeMinor),
		AutoWake:    &autoWakeFalse,
	})
	if err != nil {
		t.Fatalf("CreateEntry alice-vm17: %v", err)
	}
	if created.Entry.AutoUpgrade != string(api.AutoUpgradeMinor) || created.Entry.AutoWake {
		t.Fatalf("expected auto_upgrade=minor and auto_wake=false, got %+v", created.Entry)
	}

	// Update auto_wake=true and auto_upgrade=notify via PATCH /api/entries/alice-vm17.
	autoWakeTrue := true
	notifyPol := string(api.AutoUpgradeNotify)
	updated, err := aliceCli.UpdateEntry(ctx, "alice-vm17", api.UpdateEntryRequest{
		AutoUpgrade: &notifyPol,
		AutoWake:    &autoWakeTrue,
	})
	if err != nil {
		t.Fatalf("UpdateEntry alice-vm17: %v", err)
	}
	if updated.AutoUpgrade != string(api.AutoUpgradeNotify) || !updated.AutoWake {
		t.Fatalf("expected auto_upgrade=notify and auto_wake=true after update, got %+v", updated)
	}

	// Transition alice-vm17 to running so it can be stopped.
	row, err := st.GetEntryByName(ctx, "alice-vm17")
	if err != nil {
		t.Fatalf("GetEntryByName: %v", err)
	}
	if _, err := st.TransitionEntryState(ctx, row.ID, row.Version, api.StateRunning, ""); err != nil {
		t.Fatalf("TransitionEntryState running: %v", err)
	}

	// Non-manager bob cannot stop alice-vm17 (403 Forbidden).
	if _, err := bobCli.StopEntry(ctx, "alice-vm17"); !isAPIStatus(err, http.StatusForbidden) {
		t.Fatalf("expected 403 Forbidden when bob stops alice-vm17, got %v", err)
	}

	// Owner alice stops alice-vm17 -> state transitions to stopping.
	stoppingEntry, err := aliceCli.StopEntry(ctx, "alice-vm17")
	if err != nil {
		t.Fatalf("StopEntry: %v", err)
	}
	if stoppingEntry.State != api.StateStopping {
		t.Fatalf("expected state=stopping after StopEntry, got %s", stoppingEntry.State)
	}

	// Complete stop in store (as reconciler does) -> state=stopped.
	row, err = st.GetEntryByName(ctx, "alice-vm17")
	if err != nil {
		t.Fatalf("GetEntryByName: %v", err)
	}
	if _, err := st.CompleteStopEntry(ctx, row.ID, row.Version); err != nil {
		t.Fatalf("CompleteStopEntry: %v", err)
	}

	// Owner alice starts alice-vm17 -> state transitions stopped -> creating.
	startingEntry, err := aliceCli.StartEntry(ctx, "alice-vm17")
	if err != nil {
		t.Fatalf("StartEntry: %v", err)
	}
	if startingEntry.State != api.StateCreating {
		t.Fatalf("expected state=creating after StartEntry, got %s", startingEntry.State)
	}
}

type fakeOrphanProviderForStep16 struct {
	mu      sync.Mutex
	orphans []api.OrphanVMInfo
}

func (f *fakeOrphanProviderForStep16) ListOrphans() []api.OrphanVMInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]api.OrphanVMInfo, len(f.orphans))
	copy(out, f.orphans)
	return out
}

func (f *fakeOrphanProviderForStep16) DeleteOrphan(ctx context.Context, zone, instanceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, o := range f.orphans {
		if o.Zone == zone && o.InstanceID == instanceID {
			f.orphans = append(f.orphans[:i], f.orphans[i+1:]...)
			return nil
		}
	}
	return store.ErrNotFound
}

func TestStep16TwoStepTransferRestoreHardPurgeAndAdminRecoveryEndToEnd(t *testing.T) {
	pgURL := startEphemeralPostgresForRegistryTest(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	st, err := store.Open(ctx, pgURL, store.DefaultThresholds())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	st.SetNow(func() time.Time { return now })

	if err := st.SeedShapes(ctx, []store.ShapeRow{
		{Name: "small", MachineType: "n2-standard-4", BootDiskGB: 50, QuotaUnits: 1},
		{Name: "large", MachineType: "n2-standard-8", BootDiskGB: 100, QuotaUnits: 2},
	}); err != nil {
		t.Fatalf("SeedShapes: %v", err)
	}
	if err := st.SeedSQLInstances(ctx, []store.SQLInstanceRow{
		{
			Name:           "sql-pool-1",
			ConnectionName: "proj:us-central1:sql-pool-1",
			PrivateIP:      "10.128.0.51",
			MaxDatabases:   40,
		},
	}); err != nil {
		t.Fatalf("SeedSQLInstances: %v", err)
	}
	if _, err := st.AcquireOrRenewLease(ctx, "reconciler", "rep-step16", 30*time.Second); err != nil {
		t.Fatalf("AcquireOrRenewLease: %v", err)
	}

	groups := staticGroupResolverForTest{
		StaticGroupChecker: webauth.NewStaticGroups(map[string][]string{
			"group:team-alpha@example.com": {"alice@example.com", "coowner@example.com", "bob@example.com"},
		}),
	}
	admitter, err := webauth.NewAdmitter(webauth.AdmissionConfig{
		Verifier:       fakeFrontIdentity{},
		Groups:         groups,
		AllowedDomains: []string{"example.com"},
		Admins:         []string{"user:admin@example.com"},
	})
	if err != nil {
		t.Fatalf("NewAdmitter: %v", err)
	}
	signer, err := NewEphemeralLocalSigner("step16-k1")
	if err != nil {
		t.Fatalf("NewEphemeralLocalSigner: %v", err)
	}
	cfg, err := ParseConfig([]byte(validTestYAML()))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	fakeOrphans := &fakeOrphanProviderForStep16{
		orphans: []api.OrphanVMInfo{
			{
				InstanceID: "grain-orphan-01",
				Zone:       "us-central1-a",
				State:      "RUNNING",
				InternalIP: "10.128.15.77",
				EntryName:  "orphan-01",
				DetectedAt: now.Add(-15 * time.Minute),
			},
		},
	}
	srv, err := NewServer(ServerOptions{
		Config:    *cfg,
		Store:     st,
		Admitter:  admitter,
		Groups:    groups,
		Signer:    signer,
		Orphans:   fakeOrphans,
		ReplicaID: "rep-step16",
		PeerAddr:  "127.0.0.1:18082",
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.Router().SetNow(func() time.Time { return now })
	_ = st.UpsertReplica(ctx, "rep-step16", "127.0.0.1:18082")

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	clientFor := func(email string) *api.Client {
		c, err := api.NewClient(api.ClientConfig{
			BaseURL:           ts.URL,
			AllowInsecureHTTP: true,
			ExtraHeaders:      http.Header{"X-Test-Email": []string{email}},
		})
		if err != nil {
			t.Fatalf("NewClient(%s): %v", email, err)
		}
		return c
	}

	aliceCli := clientFor("alice@example.com")
	coownerCli := clientFor("coowner@example.com")
	bobCli := clientFor("bob@example.com")
	carolCli := clientFor("carol@example.com")
	adminCli := clientFor("admin@example.com")

	// 1. Two-step transfer with 7-day expiry, cancel, credential/tunnel/SSH revocation, and expired offer rejection.
	listedTrue := true
	createdConn, err := aliceCli.CreateEntry(ctx, api.CreateEntryRequest{
		Name:       "step16-conn",
		Kind:       api.KindConnected,
		OwnerGroup: "group:team-alpha@example.com",
		Listed:     &listedTrue,
	})
	if err != nil {
		t.Fatalf("CreateEntry step16-conn: %v", err)
	}
	connRow, err := st.GetEntryByName(ctx, "step16-conn")
	if err != nil {
		t.Fatalf("GetEntryByName step16-conn: %v", err)
	}
	if _, err := st.AcquireTunnelSessionWithCred(ctx, connRow.ID, "rep-step16", "198.51.100.55:4100", createdConn.Credential.CredentialID); err != nil {
		t.Fatalf("AcquireTunnelSessionWithCred: %v", err)
	}

	// Offer transfer to bob; verify transfer_expires_at is ~7 days from now.
	offered, err := aliceCli.OfferTransfer(ctx, "step16-conn", api.TransferRequest{
		To: "bob@example.com",
	})
	if err != nil {
		t.Fatalf("OfferTransfer: %v", err)
	}
	if offered.PendingOwner != "bob@example.com" || offered.TransferExpiresAt == nil {
		t.Fatalf("expected pending_owner=bob and transfer_expires_at set, got %+v", offered)
	}
	if offered.TransferExpiresAt.Sub(now) != 7*24*time.Hour {
		t.Fatalf("expected 7-day transfer_expires_at, got %v", offered.TransferExpiresAt.Sub(now))
	}

	// Cancel pending transfer via cancel=true.
	cancelled, err := aliceCli.OfferTransfer(ctx, "step16-conn", api.TransferRequest{
		Cancel: true,
	})
	if err != nil {
		t.Fatalf("OfferTransfer(cancel): %v", err)
	}
	if cancelled.PendingOwner != "" || cancelled.TransferExpiresAt != nil {
		t.Fatalf("expected pending_owner and transfer_expires_at cleared on cancel, got %+v", cancelled)
	}

	// Re-offer to bob, then advance clock 8 days (>7d) and verify bob's accept is rejected with 409 Conflict.
	if _, err := aliceCli.OfferTransfer(ctx, "step16-conn", api.TransferRequest{To: "bob@example.com"}); err != nil {
		t.Fatalf("re-OfferTransfer: %v", err)
	}
	now = now.Add(8 * 24 * time.Hour)
	if _, err := bobCli.AcceptTransfer(ctx, "step16-conn"); !isAPIStatus(err, http.StatusConflict) {
		t.Fatalf("expected 409 Conflict when accepting expired transfer offer, got %v", err)
	}

	// Re-offer at current clock; coowner (an owner_group peer) cannot accept on bob's behalf; bob accepts within 7 days.
	if _, err := coownerCli.OfferTransfer(ctx, "step16-conn", api.TransferRequest{To: "bob@example.com"}); err != nil {
		t.Fatalf("coowner OfferTransfer: %v", err)
	}
	if _, err := coownerCli.AcceptTransfer(ctx, "step16-conn"); !isAPIStatus(err, http.StatusForbidden) && !isAPIStatus(err, http.StatusConflict) {
		t.Fatalf("expected coowner AcceptTransfer to be rejected with 403/409, got %v", err)
	}
	accepted, err := bobCli.AcceptTransfer(ctx, "step16-conn")
	if err != nil {
		t.Fatalf("bob AcceptTransfer: %v", err)
	}
	if accepted.Owner != "bob@example.com" || accepted.PendingOwner != "" || accepted.TransferExpiresAt != nil {
		t.Fatalf("unexpected accepted entry: %+v", accepted)
	}
	if accepted.OwnerGroup != "group:team-alpha@example.com" {
		t.Fatalf("expected owner_group preserved on intra-team transfer, got %q", accepted.OwnerGroup)
	}
	// Verify grc_ credentials were revoked and active tunnel session terminated on transfer acceptance.
	if len(accepted.Credentials) == 0 || accepted.Credentials[0].RevokedAt == nil {
		t.Fatalf("expected grc_ credential revoked on transfer acceptance, got %+v", accepted.Credentials)
	}
	if tsRow, _ := st.GetActiveTunnelSession(ctx, connRow.ID); tsRow != nil {
		t.Fatalf("expected active tunnel session terminated on transfer acceptance, got %+v", tsRow)
	}

	// 2. Admin force-transfer with clear_owner_group=true.
	if _, err := bobCli.ForceTransfer(ctx, "step16-conn", api.TransferRequest{To: "carol@example.com"}); !isAPIStatus(err, http.StatusForbidden) {
		t.Fatalf("expected 403 Forbidden for non-admin ForceTransfer, got %v", err)
	}
	forced, err := adminCli.ForceTransfer(ctx, "step16-conn", api.TransferRequest{
		To:              "carol@example.com",
		ClearOwnerGroup: true,
	})
	if err != nil {
		t.Fatalf("admin ForceTransfer: %v", err)
	}
	if forced.Owner != "carol@example.com" || forced.OwnerGroup != "" {
		t.Fatalf("expected owner=carol and owner_group cleared after ForceTransfer, got %+v", forced)
	}

	// 3. Soft-delete, retention restore, and admin immediate hard purge.
	vmCreated, err := aliceCli.CreateEntry(ctx, api.CreateEntryRequest{
		Name:  "alice-retain-vm",
		Kind:  api.KindInstance,
		Shape: "small",
	})
	if err != nil {
		t.Fatalf("CreateEntry alice-retain-vm: %v", err)
	}
	if err := aliceCli.DeleteEntry(ctx, vmCreated.Entry.Name, api.DeleteEntryRequest{}); err != nil {
		t.Fatalf("DeleteEntry alice-retain-vm: %v", err)
	}
	// Restore within db_retention_period succeeds and re-charges quota.
	restored, err := aliceCli.RestoreEntry(ctx, "alice-retain-vm")
	if err != nil {
		t.Fatalf("RestoreEntry alice-retain-vm: %v", err)
	}
	if restored.State != api.StateCreating || restored.DeletedAt != nil {
		t.Fatalf("expected restored entry in state=creating with nil deleted_at, got %+v", restored)
	}

	// Delete again and perform admin immediate hard purge with release_reservation=true.
	if err := aliceCli.DeleteEntry(ctx, "alice-retain-vm", api.DeleteEntryRequest{}); err != nil {
		t.Fatalf("DeleteEntry second time: %v", err)
	}
	if _, err := aliceCli.PurgeEntry(ctx, "alice-retain-vm", api.HardPurgeRequest{ReleaseReservation: true}); !isAPIStatus(err, http.StatusForbidden) {
		t.Fatalf("expected 403 Forbidden for non-admin PurgeEntry, got %v", err)
	}
	purged, err := adminCli.PurgeEntry(ctx, "alice-retain-vm", api.HardPurgeRequest{
		Confirm:            "alice-retain-vm",
		ReleaseReservation: true,
	})
	if err != nil {
		t.Fatalf("admin PurgeEntry: %v", err)
	}
	if purged.PurgedAt == nil {
		t.Fatalf("expected purged_at set after PurgeEntry, got %+v", purged)
	}
	// Restore after hard purge is rejected with 409 Conflict.
	if _, err := aliceCli.RestoreEntry(ctx, "alice-retain-vm"); !isAPIStatus(err, http.StatusConflict) {
		t.Fatalf("expected 409 Conflict restoring hard-purged entry, got %v", err)
	}
	// Because release_reservation=true was passed, another owner (carol) can immediately reuse the name!
	avail, err := carolCli.CheckName(ctx, "alice-retain-vm")
	if err != nil || avail.Status != api.NameFree {
		t.Fatalf("expected alice-retain-vm available after hard purge with release_reservation=true, got %+v (err=%v)", avail, err)
	}

	// 4. Admin recovery endpoints: Quotas, Owner disable, Scope conflict resolution, SQL draining, Orphan VM deletion, Tunnel disconnect, and AdminSummary.
	// Create a reported scope conflict between two listed entries.
	_, err = aliceCli.CreateEntry(ctx, api.CreateEntryRequest{
		Name:   "conflict-incumbent",
		Kind:   api.KindConnected,
		Listed: &listedTrue,
	})
	if err != nil {
		t.Fatalf("CreateEntry conflict-incumbent: %v", err)
	}
	_, err = bobCli.CreateEntry(ctx, api.CreateEntryRequest{
		Name:   "conflict-contender",
		Kind:   api.KindConnected,
		Listed: &listedTrue,
	})
	if err != nil {
		t.Fatalf("CreateEntry conflict-contender: %v", err)
	}
	if _, err := aliceCli.Heartbeat(ctx, "conflict-incumbent", api.HeartbeatRequest{
		GrainVersion: "v1.6.0",
		HostKind:     api.HostKindWorkstation,
		Health:       api.HealthOK,
		Scopes:       []api.HeartbeatScope{{Repo: "acme/shared", PathPrefix: "", Role: api.RolePrimary}},
	}); err != nil {
		t.Fatalf("Heartbeat conflict-incumbent: %v", err)
	}
	if _, err := bobCli.Heartbeat(ctx, "conflict-contender", api.HeartbeatRequest{
		GrainVersion: "v1.6.0",
		HostKind:     api.HostKindWorkstation,
		Health:       api.HealthOK,
		Scopes:       []api.HeartbeatScope{{Repo: "acme/shared", PathPrefix: "", Role: api.RolePrimary}},
	}); err != nil {
		t.Fatalf("Heartbeat conflict-contender: %v", err)
	}

	if _, err := st.AcquireOrRenewLease(ctx, "reconciler", "rep-step16", 30*time.Second); err != nil {
		t.Fatalf("AcquireOrRenewLease after clock advance: %v", err)
	}
	sum, err := adminCli.AdminSummary(ctx)
	if err != nil {
		t.Fatalf("adminCli.AdminSummary: %v", err)
	}
	if len(sum.ScopeConflicts) != 1 || sum.ScopeConflicts[0].EntryName != "conflict-contender" || sum.ScopeConflicts[0].ConflictWith != "conflict-incumbent" {
		t.Fatalf("expected 1 active scope conflict in AdminSummary, got %+v", sum.ScopeConflicts)
	}
	if len(sum.SQLInstances) != 1 || sum.SQLInstances[0].Name != "sql-pool-1" {
		t.Fatalf("expected sql-pool-1 in AdminSummary.SQLInstances, got %+v", sum.SQLInstances)
	}
	if len(sum.Orphans) != 1 || sum.Orphans[0].InstanceID != "grain-orphan-01" {
		t.Fatalf("expected grain-orphan-01 in AdminSummary.Orphans, got %+v", sum.Orphans)
	}
	if sum.ReconcilerLease == nil || sum.ReconcilerLease.HolderID != "rep-step16" || !sum.ReconcilerLease.Active {
		t.Fatalf("expected active reconciler lease in AdminSummary, got %+v", sum.ReconcilerLease)
	}

	// Resolve scope conflict by demoting conflict-incumbent -> conflict-contender promotes to active primary!
	if err := adminCli.ResolveScopeConflict(ctx, api.ResolveConflictRequest{
		EntryName:  "conflict-incumbent",
		Repo:       "acme/shared",
		PathPrefix: "",
		Resolution: "demote",
	}); err != nil {
		t.Fatalf("ResolveScopeConflict: %v", err)
	}
	contenderAfter, err := bobCli.GetEntry(ctx, "conflict-contender")
	if err != nil {
		t.Fatalf("GetEntry conflict-contender: %v", err)
	}
	if len(contenderAfter.Scopes) != 1 || !contenderAfter.Scopes[0].Active || contenderAfter.Scopes[0].EffectiveRole != api.RolePrimary {
		t.Fatalf("expected conflict-contender promoted to active primary after incumbent demotion, got %+v", contenderAfter.Scopes)
	}

	// Set and reset custom quota via admin endpoints.
	qSet, err := adminCli.SetQuota(ctx, "bob@example.com", api.SetQuotaRequest{
		MaxInstanceUnits: 16,
		MaxConnected:     25,
	})
	if err != nil || !qSet.HasOverride || qSet.MaxInstanceUnits != 16 || qSet.MaxConnected != 25 {
		t.Fatalf("SetQuota failed: %+v (err=%v)", qSet, err)
	}
	qReset, err := adminCli.ResetQuota(ctx, "bob@example.com")
	if err != nil || qReset.HasOverride {
		t.Fatalf("ResetQuota failed: %+v (err=%v)", qReset, err)
	}

	// Disable owner carol@example.com and verify carol cannot create new entries (403 Forbidden owner_disabled).
	disabledOwner, err := adminCli.SetOwnerDisabled(ctx, "carol@example.com", true)
	if err != nil || !disabledOwner.Disabled {
		t.Fatalf("SetOwnerDisabled failed: %+v (err=%v)", disabledOwner, err)
	}
	if _, err := carolCli.CreateEntry(ctx, api.CreateEntryRequest{
		Name: "carol-blocked",
		Kind: api.KindConnected,
	}); !isAPIStatus(err, http.StatusForbidden) {
		t.Fatalf("expected 403 Forbidden when disabled owner creates entry, got %v", err)
	}

	// Toggle SQL instance draining flag.
	sqlDraining, err := adminCli.SetSQLInstanceDraining(ctx, "sql-pool-1", true)
	if err != nil || !sqlDraining.Draining {
		t.Fatalf("SetSQLInstanceDraining failed: %+v (err=%v)", sqlDraining, err)
	}

	// Delete orphan VM via admin endpoint.
	if err := adminCli.DeleteOrphanVM(ctx, "us-central1-a", "grain-orphan-01"); err != nil {
		t.Fatalf("DeleteOrphanVM: %v", err)
	}
	if len(fakeOrphans.ListOrphans()) != 0 {
		t.Fatalf("expected 0 orphans after DeleteOrphanVM")
	}

	// Disconnect active tunnel session via admin endpoint.
	incRow, _ := st.GetEntryByName(ctx, "conflict-incumbent")
	if _, err := st.AcquireTunnelSession(ctx, incRow.ID, "rep-step16", "198.51.100.70:4200"); err != nil {
		t.Fatalf("AcquireTunnelSession conflict-incumbent: %v", err)
	}
	if err := adminCli.DisconnectTunnel(ctx, "conflict-incumbent"); err != nil {
		t.Fatalf("DisconnectTunnel: %v", err)
	}
	if tsAfter, _ := st.GetActiveTunnelSession(ctx, incRow.ID); tsAfter != nil {
		t.Fatalf("expected tunnel session removed after DisconnectTunnel, got %+v", tsAfter)
	}
}

func TestStep18DocsRegistryAndAccessCode(t *testing.T) {
	regDocBytes, err := os.ReadFile(filepath.Join("..", "..", "docs", "registry.md"))
	if err != nil {
		t.Fatalf("reading docs/registry.md: %v", err)
	}
	regDoc := string(regDocBytes)

	for _, want := range []string{
		"docker.io/grainai/registry",
		"kind = instance",
		"kind = connected",
		"backend_url",
		"X-Grain-Registry-Assertion",
		"-registry-connect",
		"/api/entries/<name>/connect",
		"/api/entries/<name>/heartbeat",
		"owner_group",
		"GET /api/directory",
		"GET /api/route",
		"grain registry directory",
		"grain registry route",
		"grain registry register",
		"grain registry scopes",
		"primary_conflict",
		"/api/admin/delegating-apps",
		"/api/delegation-grants",
		"grain registry delegating-apps",
		"grain registry grants",
	} {
		if !strings.Contains(regDoc, want) {
			t.Errorf("docs/registry.md missing %q", want)
		}
	}

	// Extract the YAML config example from docs/registry.md and validate it via ParseConfig.
	const yamlStart = "```yaml\n"
	startIdx := strings.Index(regDoc, yamlStart)
	if startIdx < 0 {
		t.Fatalf("docs/registry.md has no ```yaml block")
	}
	yamlBody := regDoc[startIdx+len(yamlStart):]
	endIdx := strings.Index(yamlBody, "\n```")
	if endIdx < 0 {
		t.Fatalf("docs/registry.md has unterminated ```yaml block")
	}
	yamlSnippet := yamlBody[:endIdx]
	cfg, err := ParseConfig([]byte(yamlSnippet))
	if err != nil {
		t.Fatalf("ParseConfig on docs/registry.md reference config failed: %v", err)
	}
	if cfg.Signing.JWTTTL != 60*time.Second {
		t.Fatalf("docs/registry.md config jwt_ttl = %s, want 60s", cfg.Signing.JWTTTL)
	}
	if cfg.Compute.SSHBreakGlass == nil || !*cfg.Compute.SSHBreakGlass {
		t.Fatalf("docs/registry.md config ssh_break_glass = %v, want true", cfg.Compute.SSHBreakGlass)
	}
	if cfg.Observability.FleetLogs {
		t.Fatalf("docs/registry.md config fleet_logs = true, want false")
	}

	acDocBytes, err := os.ReadFile(filepath.Join("..", "..", "docs", "access-code.md"))
	if err != nil {
		t.Fatalf("reading docs/access-code.md: %v", err)
	}
	acDoc := string(acDocBytes)
	for _, want := range []string{
		"-ui-require-verifier=registry",
		"-ui-registry-issuer",
		"-ui-registry-audience",
		"-ui-registry-keys-url",
		"X-Grain-Registry-Assertion",
		"--tunnel-through-iap",
		"roles/compute.osAdminLogin",
		"roles/iap.tunnelResourceAccessor",
	} {
		if !strings.Contains(acDoc, want) {
			t.Errorf("docs/access-code.md missing %q", want)
		}
	}
}

func startEphemeralPostgresForRegistryTest(t *testing.T) string {
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
	initdbBin := findBin("initdb")
	pgCtlBin := findBin("pg_ctl")
	if initdbBin == "" || pgCtlBin == "" {
		if os.Getenv("GRAIN_REQUIRE_POSTGRES") == "1" {
			t.Fatalf("initdb/pg_ctl not found while GRAIN_REQUIRE_POSTGRES=1")
		}
		t.Skip("skipping Postgres registry server test: initdb/pg_ctl not installed")
	}
	if u, err := user.Current(); err == nil && u.Uid == "0" {
		t.Skip("skipping Postgres registry server test: running as root")
	}
	baseDir, err := os.MkdirTemp("/tmp", "grain-reg-srv-pg-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	dataDir := filepath.Join(baseDir, "data")
	sockDir := filepath.Join(baseDir, "sock")
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
