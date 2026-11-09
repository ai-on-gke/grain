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

package api_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/hub/api"
	"github.com/ai-on-gke/grain/pkg/tunnel/mux"
	"github.com/ai-on-gke/grain/pkg/tunnel/wsupgrade"
)

func TestValidationAndScopeHelpers(t *testing.T) {
	if !api.KindInstance.Valid() || !api.KindConnected.Valid() {
		t.Fatal("instance and connected kinds must be valid")
	}
	if api.EntryKind("redirect").Valid() {
		t.Fatal("redirect kind must not be valid in v1")
	}
	if !api.StateStopping.Valid() || !api.StateStopped.Valid() {
		t.Fatal("stopped/stopping states must be valid in step 17")
	}
	if !api.AutoUpgradeOff.Valid() || !api.AutoUpgradeNotify.Valid() || !api.AutoUpgradeMinor.Valid() {
		t.Fatal("off/notify/minor auto_upgrade policies must be valid")
	}
	if api.AutoUpgradePolicy("major").Valid() {
		t.Fatal("unexpected valid AutoUpgradePolicy for major")
	}

	for _, okName := range []string{"alice", "payments-prod-01", "a1b"} {
		if err := api.ValidateEntryName(okName); err != nil {
			t.Errorf("ValidateEntryName(%q) unexpected error: %v", okName, err)
		}
	}
	for _, badName := range []string{"ab", "api", "registry", "admin", "1alice", "Alice", "alice-", strings.Repeat("a", 33)} {
		if err := api.ValidateEntryName(badName); err == nil {
			t.Errorf("ValidateEntryName(%q) expected error, got nil", badName)
		}
	}

	repo, err := api.NormalizeRepo("https://GitHub.com/AI-on-GKE/Grain.git/")
	if err != nil || repo != "github.com/ai-on-gke/grain" {
		t.Fatalf("NormalizeRepo = (%q, %v), want github.com/ai-on-gke/grain", repo, err)
	}
	if _, err := api.NormalizeRepo("github.com/../evil"); err == nil {
		t.Fatal("NormalizeRepo should reject .. segment")
	}

	pfx, err := api.NormalizePathPrefix("/services/payments/")
	if err != nil || pfx != "services/payments" {
		t.Fatalf("NormalizePathPrefix = (%q, %v), want services/payments", pfx, err)
	}
	if root, err := api.NormalizePathPrefix("/"); err != nil || root != "" {
		t.Fatalf("NormalizePathPrefix(/) = (%q, %v), want empty string", root, err)
	}
	if _, err := api.NormalizePathPrefix("services/../secret"); err == nil {
		t.Fatal("NormalizePathPrefix should reject ..")
	}

	if ok, spec := api.PathMatchesPrefix("services/payments/handler.go", "services/payments"); !ok || spec != len("services/payments") {
		t.Fatalf("PathMatchesPrefix = (%v, %d), want (true, %d)", ok, spec, len("services/payments"))
	}
	if ok, _ := api.PathMatchesPrefix("services/payments-billing/handler.go", "services/payments"); ok {
		t.Fatal("PathMatchesPrefix must enforce path-segment boundaries")
	}
	if !api.PrimaryPrefixesOverlap("services", "services/payments") {
		t.Fatal("expected ancestor prefixes to overlap")
	}
	if api.PrimaryPrefixesOverlap("services/pay", "services/payments") {
		t.Fatal("non-segment prefix must not overlap")
	}

	if err := api.ValidateCatalogMetadata("Payments", "Handles billing", []string{"payments", "tier-1"}); err != nil {
		t.Fatalf("ValidateCatalogMetadata valid: %v", err)
	}
	if err := api.ValidateCatalogMetadata("", "", []string{"dup", "dup"}); err == nil {
		t.Fatal("ValidateCatalogMetadata should reject duplicate tags")
	}
}

func TestClientAuthHeadersEndpointsAndErrors(t *testing.T) {
	if _, err := api.NewClient(api.ClientConfig{BaseURL: "http://grain.example.com"}); err == nil {
		t.Fatal("expected non-loopback http:// BaseURL to be refused")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/me":
			if got := r.Header.Get("Authorization"); got != "Bearer oidc-tok" {
				t.Errorf("Authorization = %q, want Bearer oidc-tok", got)
			}
			_ = json.NewEncoder(w).Encode(api.MeResponse{
				Email:     "alice@example.com",
				Kind:      "human",
				CanCreate: true,
				Quota:     api.QuotaUsage{QuotaUnitsUsed: 1, QuotaUnitsLimit: 4, ConnectedLimit: 5},
			})
		case r.Method == http.MethodPut && r.URL.Path == "/api/entries/alice/heartbeat":
			if r.Header.Get(api.HeaderAuth) != api.AuthModeToken {
				t.Errorf("X-Grain-Auth = %q, want token", r.Header.Get(api.HeaderAuth))
			}
			if r.Header.Get(api.HeaderRegistryToken) != "grc_secret123" {
				t.Errorf("X-Grain-Registry-Token = %q, want grc_secret123", r.Header.Get(api.HeaderRegistryToken))
			}
			if r.Header.Get("Authorization") != "Bearer oidc-tok" {
				t.Errorf("Authorization = %q, want Bearer oidc-tok", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(api.HeartbeatResponse{
				Status:              api.StatusOnline,
				NextIntervalSeconds: 30,
			})
		case r.Method == http.MethodPut && r.URL.Path == "/api/entries/alice/scopes":
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": "primary scope already claimed",
				"conflict": api.ScopeConflict{
					Repo:             "github.com/ai-on-gke/grain",
					PathPrefix:       "pkg/hub",
					ConflictingEntry: "payments",
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/route":
			if r.URL.Query().Get("repo") != "github.com/ai-on-gke/grain" {
				t.Errorf("repo query = %q", r.URL.Query().Get("repo"))
			}
			cand := api.RouteCandidate{
				Name:   "alice",
				URL:    "https://alice.grain.example.com",
				Kind:   api.KindConnected,
				Status: api.StatusOnline,
				Owner:  "alice@example.com",
			}
			_ = json.NewEncoder(w).Encode(api.RouteResponse{
				Repo:       "github.com/ai-on-gke/grain",
				Path:       r.URL.Query().Get("path"),
				Primary:    &cand,
				Candidates: []api.RouteCandidate{cand},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/entries/alice-vm/stop":
			_ = json.NewEncoder(w).Encode(api.Entry{
				Name:  "alice-vm",
				Kind:  api.KindInstance,
				State: api.StateStopping,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/entries/alice-vm/start":
			_ = json.NewEncoder(w).Encode(api.Entry{
				Name:  "alice-vm",
				Kind:  api.KindInstance,
				State: api.StateCreating,
			})
		default:
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "slow down"})
		}
	}))
	defer srv.Close()

	cli, err := api.NewClient(api.ClientConfig{
		BaseURL:           srv.URL,
		RegistrationToken: "grc_secret123",
		IDToken: func(context.Context) (string, error) {
			return "oidc-tok", nil
		},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	me, err := cli.Me(context.Background())
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if me.Email != "alice@example.com" || !me.CanCreate || me.Quota.QuotaUnitsLimit != 4 {
		t.Fatalf("Me = %+v", me)
	}

	hb, err := cli.Heartbeat(context.Background(), "alice", api.HeartbeatRequest{
		GrainVersion: "v0.9.0",
		HostKind:     api.HostKindWorkstation,
		Health:       api.HealthOK,
	})
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if hb.Status != api.StatusOnline || hb.NextIntervalSeconds != 30 {
		t.Fatalf("Heartbeat = %+v", hb)
	}

	routeResp, err := cli.Route(context.Background(), api.RouteQuery{
		Repo: "github.com/ai-on-gke/grain",
		Path: "pkg/hub/api/client.go",
	})
	if err != nil || routeResp.Primary == nil || routeResp.Primary.Name != "alice" {
		t.Fatalf("Route = (%+v, %v)", routeResp, err)
	}

	_, err = cli.SetScopes(context.Background(), "alice", []api.ScopeInput{{
		Repo:       "github.com/ai-on-gke/grain",
		PathPrefix: "pkg/hub",
		Role:       api.RolePrimary,
	}})
	if !api.IsConflict(err) {
		t.Fatalf("SetScopes err = %v, want 409 conflict", err)
	}
	var ae *api.APIError
	if !errors.As(err, &ae) || ae.Conflict == nil || ae.Conflict.ConflictingEntry != "payments" {
		t.Fatalf("conflict details = %+v", ae)
	}

	_, err = cli.GetEntry(context.Background(), "rate-limited")
	if !api.IsRateLimited(err) || !errors.As(err, &ae) || ae.RetryAfter != 7*time.Second {
		t.Fatalf("rate limit err = %v (RetryAfter=%v), want 429 with 7s", err, ae.RetryAfter)
	}

	stopped, err := cli.StopEntry(context.Background(), "alice-vm")
	if err != nil || stopped.State != api.StateStopping {
		t.Fatalf("StopEntry = (%+v, %v), want state=stopping", stopped, err)
	}
	started, err := cli.StartEntry(context.Background(), "alice-vm")
	if err != nil || started.State != api.StateCreating {
		t.Fatalf("StartEntry = (%+v, %v), want state=creating", started, err)
	}
}

func TestConnectMuxThroughTLSAndHTTPProxyAndMuxListener(t *testing.T) {
	// 1. Start a mock grain-registry TLS server that upgrades /api/entries/alice/connect,
	//    runs server-side mux, and exposes an HTTP reverse-proxy handler over the mux.
	serverMuxCh := make(chan *mux.Mux, 1)
	registryTLS := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/entries/alice/connect" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get(api.HeaderConnectProtocol) != api.ConnectProtocolVersion ||
			r.Header.Get(api.HeaderEntry) != "alice" ||
			r.Header.Get(api.HeaderAuth) != api.AuthModeToken ||
			r.Header.Get(api.HeaderRegistryToken) != "grc_live_tok" ||
			r.Header.Get("Authorization") != "Bearer proxy-id-tok" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad connect headers"})
			return
		}
		wsConn, err := wsupgrade.Upgrade(w, r, http.Header{
			api.HeaderTunnelSession:   []string{"sess-42"},
			api.HeaderConnectProtocol: []string{api.ConnectProtocolVersion},
		})
		if err != nil {
			return
		}
		m := mux.NewMux(wsConn, mux.Config{
			IsServer:         true,
			PingInterval:     5 * time.Second,
			PingTimeout:      5 * time.Second,
			MaxBufferedBytes: 1 << 20,
		})
		serverMuxCh <- m
		<-m.Done()
	}))
	defer registryTLS.Close()

	// 2. Start an HTTP CONNECT forward proxy requiring Basic Proxy-Authorization.
	var proxyConnects atomic.Int32
	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen proxy: %v", err)
	}
	defer proxyLn.Close()
	go func() {
		for {
			c, err := proxyLn.Accept()
			if err != nil {
				return
			}
			go func(clientConn net.Conn) {
				defer clientConn.Close()
				br := bufio.NewReader(clientConn)
				req, err := http.ReadRequest(br)
				if err != nil || req.Method != http.MethodConnect {
					return
				}
				if req.Header.Get("Proxy-Authorization") == "" {
					_, _ = clientConn.Write([]byte("HTTP/1.1 407 Proxy Authentication Required\r\n\r\n"))
					return
				}
				proxyConnects.Add(1)
				up, err := net.Dial("tcp", req.Host)
				if err != nil {
					_, _ = clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
					return
				}
				defer up.Close()
				_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
				done := make(chan struct{}, 2)
				cp := wsupgrade.BufferedConn(clientConn, br)
				go func() { _, _ = io.Copy(up, cp); done <- struct{}{} }()
				go func() { _, _ = io.Copy(cp, up); done <- struct{}{} }()
				<-done
			}(c)
		}
	}()

	rootPool := x509.NewCertPool()
	rootPool.AddCert(registryTLS.Certificate())
	proxyURL, _ := url.Parse("http://corpuser:corppass@" + proxyLn.Addr().String())

	// 3. Connect from simulated local grain daemon via ConnectMux + MuxListener!
	sess, err := api.ConnectMux(context.Background(), api.ConnectConfig{
		BaseURL:           registryTLS.URL,
		Entry:             "alice",
		RegistrationToken: "grc_live_tok",
		IDToken:           func(context.Context) (string, error) { return "proxy-id-tok", nil },
		GrainVersion:      "v1.2.3",
		GrainCommit:       "deadbeef",
		HostKind:          api.HostKindWorkstation,
		TLSConfig:         &tls.Config{RootCAs: rootPool},
		Proxy:             http.ProxyURL(proxyURL),
	}, nil)
	if err != nil {
		t.Fatalf("ConnectMux: %v", err)
	}
	defer sess.Close()

	if sess.SessionID != "sess-42" || sess.Protocol != "1" {
		t.Fatalf("session = (%q, %q), want (sess-42, 1)", sess.SessionID, sess.Protocol)
	}
	if proxyConnects.Load() != 1 {
		t.Fatalf("proxyConnects = %d, want 1", proxyConnects.Load())
	}

	// Serve simulated grain daemon HTTP handler on MuxListener and verify RemoteAddr is NOT loopback!
	ln := sess.Listener()
	defer ln.Close()
	if ln.Addr().Network() != "mux" {
		t.Fatalf("ln.Addr().Network() = %q, want mux", ln.Addr().Network())
	}

	backendSrv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.RemoteAddr, "127.0.0.1") || strings.Contains(r.RemoteAddr, "::1") {
				t.Errorf("tunneled request RemoteAddr %q must never look like loopback", r.RemoteAddr)
			}
			w.Header().Set("X-Seen-RemoteAddr", r.RemoteAddr)
			w.Header().Set("X-Seen-Assertion", r.Header.Get(api.HeaderRegistryAssertion))
			_, _ = w.Write([]byte("hello from alice workstation"))
		}),
	}
	defer backendSrv.Close()
	go func() { _ = backendSrv.Serve(ln) }()

	// 4. Registry side opens mux stream and sends an HTTP request down the tunnel.
	var srvMux *mux.Mux
	select {
	case srvMux = <-serverMuxCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for server mux")
	}
	defer srvMux.Close()

	tunnelHTTPClient := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return srvMux.OpenStream(ctx)
			},
		},
	}
	req, err := http.NewRequest(http.MethodGet, "http://alice.grain.example/api/tasks", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set(api.HeaderRegistryAssertion, "signed-jwt-assertion")
	resp, err := tunnelHTTPClient.Do(req)
	if err != nil {
		t.Fatalf("tunnelHTTPClient.Do: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello from alice workstation" {
		t.Fatalf("tunneled body = %q", body)
	}
	if !strings.HasPrefix(resp.Header.Get("X-Seen-RemoteAddr"), "stream-") {
		t.Fatalf("X-Seen-RemoteAddr = %q, want stream-<id>-remote", resp.Header.Get("X-Seen-RemoteAddr"))
	}
	if resp.Header.Get("X-Seen-Assertion") != "signed-jwt-assertion" {
		t.Fatalf("X-Seen-Assertion = %q", resp.Header.Get("X-Seen-Assertion"))
	}
}

type stubConnectDialer struct {
	called atomic.Int32
}

func (s *stubConnectDialer) DialConnect(_ context.Context, _ api.ConnectConfig) (api.ConnectResult, error) {
	s.called.Add(1)
	c1, c2 := net.Pipe()
	_ = c2.Close()
	return api.ConnectResult{Conn: c1, SessionID: "stub-sess", Protocol: "1"}, nil
}

func TestRegisterConnectDialerAndUnregister(t *testing.T) {
	stub := &stubConnectDialer{}
	unreg := api.RegisterConnectDialer("custom-relay", stub)
	t.Cleanup(unreg)

	res, err := api.DialConnect(context.Background(), api.ConnectConfig{
		Transport: "custom-relay",
	})
	if err != nil {
		t.Fatalf("DialConnect(custom-relay): %v", err)
	}
	_ = res.Conn.Close()
	if stub.called.Load() != 1 || res.SessionID != "stub-sess" {
		t.Fatalf("stub called=%d session=%q", stub.called.Load(), res.SessionID)
	}

	unreg()
	unreg() // idempotent
	if _, ok := api.LookupConnectDialer("custom-relay"); ok {
		t.Fatal("expected custom-relay dialer to be unregistered")
	}
}

func TestClientRejectsMetadataURLsAndDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{
		"http://grain.example.com",
		"https://user:pass@grain.example.com",
		"http://169.254.169.254/latest",
		"https://[::ffff:169.254.169.254]",
		"https://[fe80::1%25eth0]",
		"http://[fe80::1%25eth0]:8080",
		"https://metadata.internal",
		"https://metadata.google.internal",
		"https://grain.example.com?foo=1",
	} {
		if _, err := api.ValidateRegistryURL(bad, false); err == nil {
			t.Errorf("ValidateRegistryURL(%q) = nil, want error", bad)
		}
		if res, err := api.DialConnect(context.Background(), api.ConnectConfig{
			BaseURL:           bad,
			Entry:             "team-k8s",
			RegistrationToken: "grc_dialconnectsecrettok1234",
		}); err == nil {
			_ = res.Conn.Close()
			t.Errorf("DialConnect(%q) = nil, want error", bad)
		}
	}
	for _, badWS := range []string{
		"ws://grain.example.com",
		"wss://user:pass@grain.example.com",
		"wss://[::ffff:169.254.169.254]",
		"wss://[fe80::1%25eth0]",
		"wss://metadata.google.internal",
		"wss://grain.example.com?foo=1",
		"wss://grain.example.com#frag",
	} {
		if res, err := api.DialConnect(context.Background(), api.ConnectConfig{
			BaseURL:           badWS,
			Entry:             "team-k8s",
			RegistrationToken: "grc_dialconnectsecrettok1234",
		}); err == nil {
			_ = res.Conn.Close()
			t.Errorf("DialConnect(%q) = nil, want error", badWS)
		}
	}

	targetHit := false
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHit = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"online"}`))
	}))
	defer targetSrv.Close()

	redirectSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, targetSrv.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redirectSrv.Close()

	hbClient, err := api.NewClient(api.ClientConfig{
		BaseURL:           redirectSrv.URL,
		RegistrationToken: "grc_redirectsecrettoken12345",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = hbClient.Heartbeat(context.Background(), "team-k8s", api.HeartbeatRequest{Health: api.HealthOK})
	var apiErr *api.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("Heartbeat redirect error = %#v, want APIError{StatusCode: 307}", err)
	}
	if targetHit {
		t.Fatal("Client followed HTTP redirect and forwarded registration credentials to redirect target")
	}
}
