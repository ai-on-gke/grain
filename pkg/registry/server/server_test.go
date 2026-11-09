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

package server

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
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

	"github.com/ai-on-gke/grain/pkg/registry/api"
	"github.com/ai-on-gke/grain/pkg/registry/store"
	"github.com/ai-on-gke/grain/pkg/tunnel/mux"
	"github.com/ai-on-gke/grain/pkg/tunnel/wsupgrade"
	"github.com/ai-on-gke/grain/pkg/webauth"
)

type testDelegationAuthorizer struct {
	allowedActor string
	access       string
}

func (d testDelegationAuthorizer) AuthorizeOnBehalfOf(_ context.Context, servicePrincipal, targetUserEmail, _ string) (*AssertionActor, error) {
	if strings.EqualFold(servicePrincipal, d.allowedActor) || strings.EqualFold("serviceAccount:"+servicePrincipal, d.allowedActor) {
		access := d.access
		if access == "" {
			access = "write"
		}
		return &AssertionActor{
			Sub:    d.allowedActor,
			Kind:   "service",
			App:    "board",
			Grant:  "dlg_board_1",
			Access: access,
		}, nil
	}
	return nil, errors.New("on-behalf-of delegation is not permitted")
}

type testIdentityVerifier struct{}

func (testIdentityVerifier) Authenticate(r *http.Request) (*webauth.Caller, error) {
	email := strings.TrimSpace(r.Header.Get("X-Test-Email"))
	if email == "" {
		return nil, errors.New("missing X-Test-Email")
	}
	sub := strings.TrimSpace(r.Header.Get("X-Test-Sub"))
	if sub == "" {
		sub = "sub:" + email
	}
	return &webauth.Caller{
		Email:   email,
		Subject: sub,
		Kind:    webauth.PrincipalKindForEmail(email),
	}, nil
}

type testAssertionSigner struct {
	priv   *ecdsa.PrivateKey
	kid    string
	mu     sync.Mutex
	signed []AssertionClaims
}

func newTestAssertionSigner(t *testing.T) *testAssertionSigner {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey: %v", err)
	}
	return &testAssertionSigner{
		priv: priv,
		kid:  "test-kid-1",
	}
}

func (s *testAssertionSigner) SignAssertion(_ context.Context, claims AssertionClaims) (string, error) {
	s.mu.Lock()
	s.signed = append(s.signed, claims)
	s.mu.Unlock()

	now := time.Now().UTC()
	payload := map[string]any{
		"iss":   "grain-registry",
		"aud":   claims.Audience,
		"sub":   claims.Subject,
		"email": claims.Email,
		"kind":  claims.Kind,
		"iat":   now.Unix(),
		"nbf":   now.Add(-30 * time.Second).Unix(),
		"exp":   now.Add(60 * time.Second).Unix(),
	}
	if claims.Actor != nil {
		payload["act"] = claims.Actor
	}
	return s.signMap(payload)
}

func (s *testAssertionSigner) SignPeerToken(_ context.Context, replicaID string, ttl time.Duration) (string, error) {
	now := time.Now().UTC()
	payload := map[string]any{
		"iss":  "grain-registry",
		"aud":  PeerAuthAudience,
		"sub":  replicaID,
		"kind": "service",
		"iat":  now.Unix(),
		"nbf":  now.Add(-30 * time.Second).Unix(),
		"exp":  now.Add(ttl).Unix(),
	}
	return s.signMap(payload)
}

func (s *testAssertionSigner) VerifyPeerToken(_ context.Context, token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("invalid jwt segments")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return "", errors.New("invalid signature")
	}
	rInt := new(big.Int).SetBytes(sig[:32])
	sInt := new(big.Int).SetBytes(sig[32:])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(&s.priv.PublicKey, digest[:], rInt, sInt) {
		return "", errors.New("signature verification failed")
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", err
	}
	var claims struct {
		Aud string `json:"aud"`
		Sub string `json:"sub"`
		Exp int64  `json:"exp"`
	}
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return "", err
	}
	if claims.Aud != PeerAuthAudience {
		return "", fmt.Errorf("unexpected peer aud %q", claims.Aud)
	}
	if time.Now().Unix() >= claims.Exp {
		return "", errors.New("peer token expired")
	}
	return claims.Sub, nil
}

func (s *testAssertionSigner) signMap(payload map[string]any) (string, error) {
	hdrBytes, _ := json.Marshal(map[string]string{"alg": "ES256", "typ": "JWT", "kid": s.kid})
	payBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	input := base64.RawURLEncoding.EncodeToString(hdrBytes) + "." + base64.RawURLEncoding.EncodeToString(payBytes)
	digest := sha256.Sum256([]byte(input))
	rInt, sInt, err := ecdsa.Sign(rand.Reader, s.priv, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	rInt.FillBytes(sig[:32])
	sInt.FillBytes(sig[32:])
	return input + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func startEphemeralPostgres(t *testing.T) string {
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
		t.Skip("skipping Postgres test: initdb/pg_ctl not installed")
	}
	if u, err := user.Current(); err == nil && u.Uid == "0" {
		t.Skip("skipping Postgres test: running as root")
	}
	baseDir, err := os.MkdirTemp("/tmp", "grain-reg-server-pg-*")
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

func dialWSUpgrade(ctx context.Context, rawConn net.Conn, host, requestURI string, headers http.Header) (net.Conn, *http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+host+requestURI, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Host = host
	for k, vv := range headers {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}
	wsupgrade.SetRequestHeaders(req.Header)
	secKey := req.Header.Get("Sec-WebSocket-Key")
	if err := req.Write(rawConn); err != nil {
		return nil, nil, err
	}
	br := bufio.NewReader(rawConn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return nil, resp, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	if got := strings.TrimSpace(resp.Header.Get("Sec-WebSocket-Accept")); got != wsupgrade.Accept(secKey) {
		return nil, resp, fmt.Errorf("Sec-WebSocket-Accept mismatch: got %q, want %q", got, wsupgrade.Accept(secKey))
	}
	return wsupgrade.NewClientConn(wsupgrade.BufferedConn(rawConn, br)), resp, nil
}

func dialTestTunnelClient(
	t *testing.T,
	connectURL string,
	entryName string,
	headers http.Header,
	handler http.Handler,
) (*mux.Mux, *http.Response) {
	t.Helper()
	u, err := url.Parse(connectURL)
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	rawConn, err := net.DialTimeout("tcp", u.Host, 5*time.Second)
	if err != nil {
		t.Fatalf("net.Dial: %v", err)
	}
	reqHeaders := make(http.Header)
	reqHeaders.Set(api.HeaderConnectProtocol, api.ConnectProtocolVersion)
	reqHeaders.Set(api.HeaderEntry, entryName)
	for k, vv := range headers {
		for _, v := range vv {
			reqHeaders.Add(k, v)
		}
	}
	wsConn, resp, err := dialWSUpgrade(context.Background(), rawConn, u.Host, u.RequestURI(), reqHeaders)
	if err != nil {
		_ = rawConn.Close()
		return nil, resp
	}
	clientMux := mux.NewMux(wsConn, mux.Config{
		IsServer:   false,
		MaxStreams: 256,
	})
	if handler != nil {
		ln := api.NewMuxListener(clientMux)
		srv := &http.Server{Handler: handler}
		go func() {
			_ = srv.Serve(ln)
		}()
		t.Cleanup(func() {
			_ = srv.Close()
			_ = clientMux.Close()
		})
	} else {
		t.Cleanup(func() {
			_ = clientMux.Close()
		})
	}
	return clientMux, resp
}

func makeFakeOIDCToken(aud string) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payBytes, _ := json.Marshal(map[string]any{"aud": aud, "sub": "123"})
	pay := base64.RawURLEncoding.EncodeToString(payBytes)
	return hdr + "." + pay + ".sig"
}

func TestClassifyHostAndSubdomainRouting(t *testing.T) {
	regHost := "registry.example.com"
	instDom := "instances.example.com"

	cases := []struct {
		host     string
		wantKind HostRouteKind
		wantName string
	}{
		{"registry.example.com", HostRouteRegistry, ""},
		{"REGISTRY.EXAMPLE.COM:8443", HostRouteRegistry, ""},
		{"127.0.0.1:41234", HostRouteRegistry, ""},
		{"alice-vm.instances.example.com", HostRouteInstance, "alice-vm"},
		{"ALICE-VM.instances.example.com:443", HostRouteInstance, "alice-vm"},
		{"a.b.instances.example.com", HostRouteInvalidInstance, "a.b"},
		{"instances.example.com", HostRouteInvalidInstance, ""},
		{"_bad.instances.example.com", HostRouteInvalidInstance, "_bad"},
		{"unknown.other.org", HostRouteUnknown, ""},
	}
	for _, tc := range cases {
		got := ClassifyHost(tc.host, regHost, instDom)
		if got.Kind != tc.wantKind || got.EntryName != tc.wantName {
			t.Fatalf("ClassifyHost(%q) = %+v, want kind=%v name=%q", tc.host, got, tc.wantKind, tc.wantName)
		}
	}
}

func TestReverseProxyHeadersCookiesCSRFStreamingAndUpgrades(t *testing.T) {
	// 1. Start a fake hosted instance VM backend that inspects received headers/cookies,
	// sets cookies with Domain attributes, serves SSE streams, and upgrades WebSockets.
	var lastReceivedReq struct {
		mu      sync.Mutex
		Host    string
		Headers http.Header
		Cookies []*http.Cookie
	}

	instanceMux := http.NewServeMux()
	instanceMux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		lastReceivedReq.mu.Lock()
		lastReceivedReq.Host = r.Host
		lastReceivedReq.Headers = r.Header.Clone()
		lastReceivedReq.Cookies = r.Cookies()
		lastReceivedReq.mu.Unlock()

		// Read body to trigger MaxBytesReader if body exceeds 64 MiB.
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				http.Error(w, "too large", http.StatusRequestEntityTooLarge)
				return
			}
		}

		w.Header().Add("Set-Cookie", "grain_sess=xyz; Domain=instances.example.com; Path=/")
		w.Header().Add("Set-Cookie", "__Host-grain_csrf=tok123; Domain=alice-vm.instances.example.com; Secure")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("instance-ok"))
	})
	instanceMux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for i := 1; i <= 3; i++ {
			fmt.Fprintf(w, "data: chunk-%d\n\n", i)
			if fl != nil {
				fl.Flush()
			}
		}
	})
	instanceMux.HandleFunc("/api/hostd/connect", func(w http.ResponseWriter, r *http.Request) {
		wsConn, err := wsupgrade.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer wsConn.Close()
		buf := make([]byte, 64)
		n, err := wsConn.Read(buf)
		if err != nil {
			return
		}
		_, _ = wsConn.Write([]byte("echo:" + string(buf[:n])))
	})
	instanceMux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(250 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})

	backendSrv := httptest.NewServer(instanceMux)
	defer backendSrv.Close()

	admitter, err := webauth.NewAdmitter(webauth.AdmissionConfig{
		Verifier:       testIdentityVerifier{},
		AllowedDomains: []string{"example.com"},
		Admins:         []string{"user:admin@example.com"},
	})
	if err != nil {
		t.Fatalf("NewAdmitter: %v", err)
	}
	signer := newTestAssertionSigner(t)
	rt, err := NewRouter(RouterOptions{
		RegistryHost:   "registry.example.com",
		InstanceDomain: "instances.example.com",
		IAPAudience:    "/projects/123/global/backendServices/456",
		Admitter:       admitter,
		Signer:         signer,
		InstanceHTTPTransport: &http.Transport{
			ResponseHeaderTimeout: 80 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	defer rt.Close()

	rt.HostCache().Put(&store.EntryRow{
		ID:             "e-alice",
		Name:           "alice-vm",
		Kind:           api.KindInstance,
		LifecycleState: api.StateRunning,
		OwnerEmail:     "alice@example.com",
		Visibility:     store.VisibilityOwner,
		BackendURL:     backendSrv.URL,
	})

	mainHandler := rt.WrapMainHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("control-plane"))
	}))
	frontSrv := httptest.NewServer(mainHandler)
	defer frontSrv.Close()

	// A. Pre-strip CSRF check: POST with IAP cookie from sibling subdomain Origin must be rejected (403) BEFORE cookie stripping.
	{
		req := httptest.NewRequest(http.MethodPost, "https://alice-vm.instances.example.com/echo", strings.NewReader("x=1"))
		req.Host = "alice-vm.instances.example.com"
		req.Header.Set("X-Test-Email", "alice@example.com")
		req.Header.Set("Cookie", "__Host-GCP_IAP_AUTH_TOKEN_1=iap-secret; grain_theme=dark")
		req.Header.Set("Origin", "https://bob-vm.instances.example.com")
		rec := httptest.NewRecorder()
		mainHandler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403 on cross-subdomain CSRF POST, got %d (%s)", rec.Code, rec.Body.String())
		}
	}

	// B. Same-origin POST with edge credentials, IAP cookies, and Authorization: Bearer <edge-oidc> vs gma_ token.
	{
		edgeOIDC := makeFakeOIDCToken("/projects/123/global/backendServices/456")
		req := httptest.NewRequest(http.MethodPost, "https://alice-vm.instances.example.com/echo", strings.NewReader("hello"))
		req.Host = "alice-vm.instances.example.com"
		req.Header.Set("X-Test-Email", "alice@example.com")
		req.Header.Set("Origin", "https://alice-vm.instances.example.com")
		req.Header.Set("Cookie", "__Host-GCP_IAP_AUTH_TOKEN_1=iap-secret; GCP_IAAP_AUTH_TOKEN_ABC=iap2; grain_theme=dark")
		req.Header.Set("X-Goog-Authenticated-User-Email", "accounts.google.com:alice@example.com")
		req.Header.Set("X-Goog-IAP-JWT-Assertion", "raw-iap-jwt")
		req.Header.Set("X-Forwarded-User", "spoofed")
		req.Header.Set("Proxy-Authorization", "Bearer proxy-sec")
		req.Header.Set("X-Grain-Registry-Assertion", "spoofed-assertion")
		req.Header.Set("Authorization", "Bearer "+edgeOIDC)

		rec := httptest.NewRecorder()
		mainHandler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK on same-origin POST, got %d (%s)", rec.Code, rec.Body.String())
		}

		lastReceivedReq.mu.Lock()
		gotHost := lastReceivedReq.Host
		gotHdr := lastReceivedReq.Headers.Clone()
		gotCookies := lastReceivedReq.Cookies
		lastReceivedReq.mu.Unlock()

		if gotHost != "alice-vm.instances.example.com" {
			t.Fatalf("expected preserved Host alice-vm.instances.example.com, got %q", gotHost)
		}
		if gotHdr.Get("X-Forwarded-Host") != "alice-vm.instances.example.com" {
			t.Fatalf("unexpected X-Forwarded-Host: %q", gotHdr.Get("X-Forwarded-Host"))
		}
		if gotHdr.Get("X-Forwarded-Proto") != "https" {
			t.Fatalf("unexpected X-Forwarded-Proto: %q", gotHdr.Get("X-Forwarded-Proto"))
		}
		for _, forbidden := range []string{
			"X-Goog-Authenticated-User-Email",
			"X-Goog-Iap-Jwt-Assertion",
			"X-Forwarded-User",
			"Proxy-Authorization",
			"Authorization",
		} {
			if v := gotHdr.Get(forbidden); v != "" {
				t.Fatalf("expected header %s to be stripped, but backend received %q", forbidden, v)
			}
		}
		if gotHdr.Get(api.HeaderRegistryAssertion) == "" || gotHdr.Get(api.HeaderRegistryAssertion) == "spoofed-assertion" {
			t.Fatalf("expected signed X-Grain-Registry-Assertion, got %q", gotHdr.Get(api.HeaderRegistryAssertion))
		}
		if len(gotCookies) != 1 || gotCookies[0].Name != "grain_theme" || gotCookies[0].Value != "dark" {
			t.Fatalf("expected IAP cookies stripped and grain_theme preserved, got %+v", gotCookies)
		}

		// Verify Set-Cookie Domain stripping and Secure + HttpOnly enforcement.
		setCookies := rec.Result().Header.Values("Set-Cookie")
		if len(setCookies) != 2 {
			t.Fatalf("expected 2 hardened Set-Cookie headers, got %v", setCookies)
		}
		for _, sc := range setCookies {
			lower := strings.ToLower(sc)
			if strings.Contains(lower, "domain=") {
				t.Fatalf("expected Domain stripped from Set-Cookie, got %q", sc)
			}
			if !strings.Contains(lower, "secure") || !strings.Contains(lower, "httponly") {
				t.Fatalf("expected Secure and HttpOnly on Set-Cookie, got %q", sc)
			}
		}
	}

	// C. Verify gma_ bearer token (and X-Grain-Tunnel-Token) is preserved while
	// Google-managed IAP ya29. access tokens and custom OAuth client ID tokens are stripped.
	{
		req := httptest.NewRequest(http.MethodGet, "https://alice-vm.instances.example.com/echo", nil)
		req.Host = "alice-vm.instances.example.com"
		req.Header.Set("X-Test-Email", "alice@example.com")
		req.Header.Set("Authorization", "Bearer gma_live_token_123")
		req.Header.Set("X-Grain-Tunnel-Token", "gma_live_token_123")
		rec := httptest.NewRecorder()
		mainHandler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK with gma_ token, got %d", rec.Code)
		}
		lastReceivedReq.mu.Lock()
		gotAuth := lastReceivedReq.Headers.Get("Authorization")
		gotTun := lastReceivedReq.Headers.Get("X-Grain-Tunnel-Token")
		lastReceivedReq.mu.Unlock()
		if gotAuth != "Bearer gma_live_token_123" {
			t.Fatalf("expected gma_ token preserved, got %q", gotAuth)
		}
		if gotTun != "gma_live_token_123" {
			t.Fatalf("expected gma_ X-Grain-Tunnel-Token preserved, got %q", gotTun)
		}

		for _, tc := range []struct {
			name    string
			authHdr string
			tunHdr  string
		}{
			{
				name:    "google-managed-iap-ya29",
				authHdr: "Bearer ya29.a0AfB_byC_live_iap_access_token",
				tunHdr:  "ya29.a0AfB_byC_live_iap_access_token",
			},
			{
				name:    "bearer-tab-ya29",
				authHdr: "Bearer\tya29.a0AfB_byC_tab_delimited",
				tunHdr:  "Bearer\tya29.a0AfB_byC_tab_delimited",
			},
			{
				name:    "custom-iap-oauth-client",
				authHdr: "Bearer " + makeFakeOIDCToken("123456789-abcdef.apps.googleusercontent.com"),
				tunHdr:  makeFakeOIDCToken("123456789-abcdef.apps.googleusercontent.com"),
			},
			{
				name:    "cloud-run-direct-iap-wildcard-registry-aud",
				authHdr: "Bearer " + makeFakeOIDCToken("https://registry.example.com/*"),
				tunHdr:  makeFakeOIDCToken("https://registry.example.com/*"),
			},
			{
				name:    "adc-entry-host-aud",
				authHdr: "Bearer " + makeFakeOIDCToken("https://alice-vm.instances.example.com"),
				tunHdr:  makeFakeOIDCToken("https://alice-vm.instances.example.com"),
			},
			{
				name:    "adc-entry-host-wildcard-aud",
				authHdr: "Bearer " + makeFakeOIDCToken("https://alice-vm.instances.example.com/*"),
				tunHdr:  makeFakeOIDCToken("https://alice-vm.instances.example.com/*"),
			},
			{
				name:    "registry-cred-token",
				authHdr: "Bearer grc_ accidental_entry_token",
				tunHdr:  "grc_ accidental_entry_token",
			},
		} {
			edgeReq := httptest.NewRequest(http.MethodGet, "https://alice-vm.instances.example.com/echo", nil)
			edgeReq.Host = "alice-vm.instances.example.com"
			edgeReq.Header.Set("X-Test-Email", "alice@example.com")
			edgeReq.Header.Set("Authorization", tc.authHdr)
			edgeReq.Header.Set("X-Grain-Tunnel-Token", tc.tunHdr)
			edgeRec := httptest.NewRecorder()
			mainHandler.ServeHTTP(edgeRec, edgeReq)
			if edgeRec.Code != http.StatusOK {
				t.Fatalf("%s: expected 200 OK, got %d", tc.name, edgeRec.Code)
			}
			lastReceivedReq.mu.Lock()
			leakedAuth := lastReceivedReq.Headers.Get("Authorization")
			leakedTun := lastReceivedReq.Headers.Get("X-Grain-Tunnel-Token")
			lastReceivedReq.mu.Unlock()
			if leakedAuth != "" {
				t.Fatalf("%s: expected Authorization stripped before reaching backend, got %q", tc.name, leakedAuth)
			}
			if leakedTun != "" {
				t.Fatalf("%s: expected X-Grain-Tunnel-Token stripped before reaching backend, got %q", tc.name, leakedTun)
			}
		}
	}

	// D. SSE streaming (`FlushInterval: -1`).
	{
		req := httptest.NewRequest(http.MethodGet, "https://alice-vm.instances.example.com/sse", nil)
		req.Host = "alice-vm.instances.example.com"
		req.Header.Set("X-Test-Email", "alice@example.com")
		rec := httptest.NewRecorder()
		mainHandler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "data: chunk-3") {
			t.Fatalf("unexpected SSE response: %d %q", rec.Code, rec.Body.String())
		}
	}

	// E. WebSocket / `/api/hostd/connect` 101 Switching Protocols upgrade through proxy.
	{
		u, _ := url.Parse(frontSrv.URL)
		rawConn, err := net.DialTimeout("tcp", u.Host, 5*time.Second)
		if err != nil {
			t.Fatalf("Dial frontSrv: %v", err)
		}
		defer rawConn.Close()
		hdr := http.Header{
			"X-Test-Email": []string{"alice@example.com"},
		}
		wsConn, _, err := dialWSUpgrade(context.Background(), rawConn, "alice-vm.instances.example.com", "/api/hostd/connect", hdr)
		if err != nil {
			t.Fatalf("dialWSUpgrade through proxy: %v", err)
		}
		defer wsConn.Close()
		if _, err := wsConn.Write([]byte("ping-hostd")); err != nil {
			t.Fatalf("wsConn.Write: %v", err)
		}
		reply := make([]byte, 64)
		n, err := wsConn.Read(reply)
		if err != nil {
			t.Fatalf("wsConn.Read: %v", err)
		}
		if string(reply[:n]) != "echo:ping-hostd" {
			t.Fatalf("unexpected upgraded reply %q", string(reply[:n]))
		}
	}

	// F. 64 MiB request body cap returns 413 Payload Too Large, and slow backend returns 504 Gateway Timeout.
	{
		req := httptest.NewRequest(http.MethodPost, "https://alice-vm.instances.example.com/echo", strings.NewReader("oversized"))
		req.Host = "alice-vm.instances.example.com"
		req.ContentLength = MaxProxiedRequestBodyBytes + 1
		req.Header.Set("X-Test-Email", "alice@example.com")
		rec := httptest.NewRecorder()
		mainHandler.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("expected 413 for >64MiB ContentLength, got %d", rec.Code)
		}

		reqSlow := httptest.NewRequest(http.MethodGet, "https://alice-vm.instances.example.com/slow", nil)
		reqSlow.Host = "alice-vm.instances.example.com"
		reqSlow.Header.Set("X-Test-Email", "alice@example.com")
		recSlow := httptest.NewRecorder()
		mainHandler.ServeHTTP(recSlow, reqSlow)
		if recSlow.Code != http.StatusGatewayTimeout {
			t.Fatalf("expected 504 on slow backend response headers, got %d (%s)", recSlow.Code, recSlow.Body.String())
		}
	}
}

func TestStatePagesHTMLAndJSONAndDBFailoverGrace(t *testing.T) {
	pgURL := startEphemeralPostgres(t)
	ctx := context.Background()
	st, err := store.Open(ctx, pgURL, store.DefaultThresholds())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	if err := st.SetQuota(ctx, "alice@example.com", 20, 20, "admin@example.com"); err != nil {
		t.Fatalf("SetQuota: %v", err)
	}

	// Seed entries in various states: creating, deleting, failed, deleted, offline connected, and running instance.
	creatingEntry, err := st.CreateEntry(ctx, store.CreateEntryInput{
		Name:       "vm-creating",
		Kind:       api.KindInstance,
		OwnerEmail: "alice@example.com",
		Visibility: store.VisibilityPublic,
	})
	if err != nil {
		t.Fatalf("CreateEntry vm-creating: %v", err)
	}
	_ = creatingEntry

	deletingEntry, err := st.CreateEntry(ctx, store.CreateEntryInput{
		Name:       "vm-deleting",
		Kind:       api.KindInstance,
		OwnerEmail: "alice@example.com",
		Visibility: store.VisibilityPublic,
	})
	if err != nil {
		t.Fatalf("CreateEntry vm-deleting: %v", err)
	}
	if _, err := st.TransitionEntryState(ctx, deletingEntry.ID, deletingEntry.Version, api.StateDeleting, ""); err != nil {
		t.Fatalf("TransitionEntryState deleting: %v", err)
	}

	failedEntry, err := st.CreateEntry(ctx, store.CreateEntryInput{
		Name:       "vm-failed",
		Kind:       api.KindInstance,
		OwnerEmail: "alice@example.com",
		Visibility: store.VisibilityPublic,
	})
	if err != nil {
		t.Fatalf("CreateEntry vm-failed: %v", err)
	}
	if _, err := st.TransitionEntryState(ctx, failedEntry.ID, failedEntry.Version, api.StateFailed, "GCE quota exceeded in us-central1-a"); err != nil {
		t.Fatalf("TransitionEntryState failed: %v", err)
	}

	toDeleteEntry, err := st.CreateEntry(ctx, store.CreateEntryInput{
		Name:       "vm-deleted",
		Kind:       api.KindInstance,
		OwnerEmail: "alice@example.com",
		Visibility: store.VisibilityPublic,
	})
	if err != nil {
		t.Fatalf("CreateEntry vm-deleted: %v", err)
	}
	if _, err := st.SoftDeleteEntry(ctx, toDeleteEntry.ID, toDeleteEntry.Version); err != nil {
		t.Fatalf("SoftDeleteEntry: %v", err)
	}

	offlineEntry, err := st.CreateEntry(ctx, store.CreateEntryInput{
		Name:        "conn-offline",
		DisplayName: "Alice Workstation",
		HostKind:    string(api.HostKindWorkstation),
		Kind:        api.KindConnected,
		OwnerEmail:  "alice@example.com",
		OwnerGroup:  "group:team-a@example.com",
		Visibility:  store.VisibilityPublic,
	})
	if err != nil {
		t.Fatalf("CreateEntry conn-offline: %v", err)
	}
	_, _ = st.RecordHeartbeat(ctx, offlineEntry.ID, api.HeartbeatRequest{Health: api.HealthOK})

	admitter, err := webauth.NewAdmitter(webauth.AdmissionConfig{
		Verifier:       testIdentityVerifier{},
		AllowedDomains: []string{"example.com"},
		Admins:         []string{"user:admin@example.com"},
	})
	if err != nil {
		t.Fatalf("NewAdmitter: %v", err)
	}
	rt, err := NewRouter(RouterOptions{
		RegistryHost:   "registry.example.com",
		InstanceDomain: "instances.example.com",
		Store:          st,
		Admitter:       admitter,
		Signer:         newTestAssertionSigner(t),
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	defer rt.Close()

	h := rt.WrapMainHandler(http.NotFoundHandler())

	// 1. 503 creating state page (HTML & JSON)
	{
		req := httptest.NewRequest(http.MethodGet, "https://vm-creating.instances.example.com/", nil)
		req.Host = "vm-creating.instances.example.com"
		req.Header.Set("X-Test-Email", "bob@example.com")
		req.Header.Set("Accept", "text/html")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "5" {
			t.Fatalf("expected 503 with Retry-After: 5 for creating, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "vm-creating") || strings.Contains(rec.Body.String(), "<script") {
			t.Fatalf("unexpected creating HTML body: %s", rec.Body.String())
		}
	}

	// 2. 410 deleting state page
	{
		req := httptest.NewRequest(http.MethodGet, "https://vm-deleting.instances.example.com/", nil)
		req.Host = "vm-deleting.instances.example.com"
		req.Header.Set("X-Test-Email", "bob@example.com")
		req.Header.Set("Accept", "text/html")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusGone {
			t.Fatalf("expected 410 Gone for deleting, got %d", rec.Code)
		}
	}

	// 3. 502 failed state page: non-manager does NOT see status_reason/Replace VM; owner/manager DOES see Replace VM.
	{
		reqBob := httptest.NewRequest(http.MethodGet, "https://vm-failed.instances.example.com/", nil)
		reqBob.Host = "vm-failed.instances.example.com"
		reqBob.Header.Set("X-Test-Email", "bob@example.com")
		reqBob.Header.Set("Accept", "text/html")
		recBob := httptest.NewRecorder()
		h.ServeHTTP(recBob, reqBob)
		if recBob.Code != http.StatusBadGateway {
			t.Fatalf("expected 502 for failed entry, got %d", recBob.Code)
		}
		if strings.Contains(recBob.Body.String(), "Replace VM") || strings.Contains(recBob.Body.String(), "GCE quota exceeded") {
			t.Fatalf("non-manager should not see Replace VM or failure reason: %s", recBob.Body.String())
		}

		reqAlice := httptest.NewRequest(http.MethodGet, "https://vm-failed.instances.example.com/", nil)
		reqAlice.Host = "vm-failed.instances.example.com"
		reqAlice.Header.Set("X-Test-Email", "alice@example.com")
		reqAlice.Header.Set("Accept", "text/html")
		recAlice := httptest.NewRecorder()
		h.ServeHTTP(recAlice, reqAlice)
		if !strings.Contains(recAlice.Body.String(), "Replace VM") || !strings.Contains(recAlice.Body.String(), "GCE quota exceeded") {
			t.Fatalf("owner should see Replace VM and failure reason: %s", recAlice.Body.String())
		}
	}

	// 4. 404 deleted entry: owner sees retention/reservation info; non-manager sees standard 404.
	{
		reqAlice := httptest.NewRequest(http.MethodGet, "https://vm-deleted.instances.example.com/", nil)
		reqAlice.Host = "vm-deleted.instances.example.com"
		reqAlice.Header.Set("X-Test-Email", "alice@example.com")
		reqAlice.Header.Set("Accept", "application/json")
		recAlice := httptest.NewRecorder()
		h.ServeHTTP(recAlice, reqAlice)
		if recAlice.Code != http.StatusNotFound || !strings.Contains(recAlice.Body.String(), "entry_deleted") {
			t.Fatalf("expected 404 entry_deleted for owner, got %d %s", recAlice.Code, recAlice.Body.String())
		}
	}

	// 5. 503 offline connected entry: names owner, owner_group, display_name, host_kind, and last_seen_at.
	{
		reqHTML := httptest.NewRequest(http.MethodGet, "https://conn-offline.instances.example.com/", nil)
		reqHTML.Host = "conn-offline.instances.example.com"
		reqHTML.Header.Set("X-Test-Email", "bob@example.com")
		reqHTML.Header.Set("Accept", "text/html")
		recHTML := httptest.NewRecorder()
		h.ServeHTTP(recHTML, reqHTML)
		if recHTML.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 for offline connected entry, got %d", recHTML.Code)
		}
		body := recHTML.Body.String()
		for _, want := range []string{"conn-offline", "alice@example.com", "group:team-a@example.com", "Alice Workstation", "workstation"} {
			if !strings.Contains(body, want) {
				t.Fatalf("offline connected HTML missing %q: %s", want, body)
			}
		}

		reqJSON := httptest.NewRequest(http.MethodGet, "https://conn-offline.instances.example.com/api/status", nil)
		reqJSON.Host = "conn-offline.instances.example.com"
		reqJSON.Header.Set("X-Test-Email", "bob@example.com")
		reqJSON.Header.Set("Accept", "application/json")
		recJSON := httptest.NewRecorder()
		h.ServeHTTP(recJSON, reqJSON)
		if recJSON.Code != http.StatusServiceUnavailable || !strings.Contains(recJSON.Body.String(), `"entry_offline"`) {
			t.Fatalf("expected JSON entry_offline 503, got %d %s", recJSON.Code, recJSON.Body.String())
		}
	}

	// 6. Verify 10-minute read-only `hostCache` grace window during PostgreSQL failover:
	// Close the DB and advance clock by 30s (past the 5s normal TTL, within the 10m failover grace).
	// `vm-creating` must still be resolved from `hostCache` rather than failing with 500!
	{
		_ = st.Close()
		future := time.Now().Add(30 * time.Second)
		rt.HostCache().SetNow(func() time.Time { return future })

		reqFailover := httptest.NewRequest(http.MethodGet, "https://vm-creating.instances.example.com/", nil)
		reqFailover.Host = "vm-creating.instances.example.com"
		reqFailover.Header.Set("X-Test-Email", "bob@example.com")
		reqFailover.Header.Set("Accept", "application/json")
		recFailover := httptest.NewRecorder()
		h.ServeHTTP(recFailover, reqFailover)
		if recFailover.Code != http.StatusServiceUnavailable || !strings.Contains(recFailover.Body.String(), "entry_provisioning") {
			t.Fatalf("expected cached vm-creating to survive DB outage inside 10m grace window, got %d (%s)", recFailover.Code, recFailover.Body.String())
		}
	}
}

func TestOutboundTunnelConnectPeerForwardingReconnectGraceAndImmediateRevocation(t *testing.T) {
	pgURL := startEphemeralPostgres(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st, err := store.Open(ctx, pgURL, store.DefaultThresholds())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	// Create a connected entry owned by alice@example.com with owner_group and connected_principal.
	connEntry, err := st.CreateEntry(ctx, store.CreateEntryInput{
		Name:               "alice-tunnel",
		Kind:               api.KindConnected,
		OwnerEmail:         "alice@example.com",
		OwnerGroup:         "group:team-a@example.com",
		ConnectedPrincipal: "serviceAccount:runner@proj.iam.gserviceaccount.com",
		Visibility:         store.VisibilityPublic,
	})
	if err != nil {
		t.Fatalf("CreateEntry alice-tunnel: %v", err)
	}
	mintedToken, _, err := st.IssueCredential(ctx, connEntry.ID)
	if err != nil {
		t.Fatalf("IssueCredential: %v", err)
	}

	groups := webauth.NewStaticGroups(map[string][]string{
		"group:team-a@example.com": {"member@example.com"},
	})
	admitter, err := webauth.NewAdmitter(webauth.AdmissionConfig{
		Verifier:       testIdentityVerifier{},
		Groups:         groups,
		AllowedDomains: []string{"example.com"},
		Admins:         []string{"user:admin@example.com"},
	})
	if err != nil {
		t.Fatalf("NewAdmitter: %v", err)
	}
	sharedSigner := newTestAssertionSigner(t)

	var peerRejectedStreams atomic.Int64
	var peerForwards atomic.Int64

	// Start two replicas (Replica A and Replica B), each with its own :8080, :8081 public, and :8082 peer server.
	makeReplica := func(id string) (*Router, *httptest.Server, *httptest.Server, *httptest.Server) {
		rt, err := NewRouter(RouterOptions{
			RegistryHost:   "registry.example.com",
			InstanceDomain: "instances.example.com",
			ReplicaID:      id,
			DatabaseURL:    pgURL,
			ReconnectGrace: 2 * time.Second,
			TunnelOptions: TunnelOptions{
				MaxConcurrentStreams: 4,
				OnPeerStreamRejected: func(_ string, _ uint32) {
					peerRejectedStreams.Add(1)
				},
			},
			Store:         st,
			Admitter:      admitter,
			Groups:        groups,
			Signer:        sharedSigner,
			OnPeerForward: func() { peerForwards.Add(1) },
		})
		if err != nil {
			t.Fatalf("NewRouter %s: %v", id, err)
		}
		peerSrv := httptest.NewServer(rt.PeerHandler())
		rt.SetPeerAddr(peerSrv.Listener.Addr().String())

		mainMux := http.NewServeMux()
		mainMux.HandleFunc("GET /api/entries/{name}/connect", func(w http.ResponseWriter, r *http.Request) {
			rt.HandleConnect(w, r, r.PathValue("name"), false)
		})
		mainSrv := httptest.NewServer(rt.WrapMainHandler(mainMux))
		pubSrv := httptest.NewServer(rt.PublicHandler(nil, nil))
		rt.StartBackground(ctx)

		t.Cleanup(func() {
			rt.Close()
			pubSrv.CloseClientConnections()
			pubSrv.Close()
			mainSrv.CloseClientConnections()
			mainSrv.Close()
			peerSrv.CloseClientConnections()
			peerSrv.Close()
		})
		return rt, mainSrv, pubSrv, peerSrv
	}

	rtA, mainA, pubA, peerA := makeReplica("replica-a")
	rtB, mainB, pubB, _ := makeReplica("replica-b")
	_ = pubA

	// 1. Verify public listener (:8081) rejects non-whitelisted paths and rejects FrontIdentity without grc_ token.
	{
		res, err := http.Get(pubB.URL + "/api/entries")
		if err != nil {
			t.Fatalf("GET pubB /api/entries: %v", err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("expected 404 on public listener for /api/entries, got %d", res.StatusCode)
		}

		// Attempting /connect on public listener with X-Test-Email but no grc_ token must fail with 401.
		_, resp := dialTestTunnelClient(t, pubB.URL+"/api/entries/alice-tunnel/connect", "alice-tunnel", http.Header{
			"X-Test-Email": []string{"alice@example.com"},
		}, nil)
		if resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401 on public connect without grc_ token, got %+v", resp)
		}
	}

	// 2. Connect to Replica B's public listener using the valid `grc_...` token.
	grainHandler := http.NewServeMux()
	grainHandler.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "grain_ui=1; Domain=alice-tunnel.instances.example.com")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "from-tunnel:%s:assertion=%t", r.Host, r.Header.Get(api.HeaderRegistryAssertion) != "")
	})
	grainHandler.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		ws, err := wsupgrade.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		b := make([]byte, 32)
		n, err := ws.Read(b)
		if err != nil {
			return
		}
		_, _ = ws.Write([]byte("ws-tunnel:" + string(b[:n])))
	})
	blockCh := make(chan struct{})
	grainHandler.HandleFunc("/hold", func(w http.ResponseWriter, r *http.Request) {
		<-blockCh
		w.WriteHeader(http.StatusOK)
	})

	clientMuxB, respB := dialTestTunnelClient(t, pubB.URL+"/api/entries/alice-tunnel/connect", "alice-tunnel", http.Header{
		api.HeaderAuth:          []string{api.AuthModeToken},
		api.HeaderRegistryToken: []string{mintedToken},
	}, grainHandler)
	if clientMuxB == nil || respB.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("expected 101 Switching Protocols on pubB with grc_ token, got %+v", respB)
	}
	if respB.Header.Get(api.HeaderTunnelSession) == "" {
		t.Fatalf("expected X-Grain-Tunnel-Session header on 101 response")
	}

	// Wait briefly for LISTEN registry_tunnels / RefreshEntry on Replica A.
	_, _ = rtA.TunnelCache().RefreshEntry(ctx, "alice-tunnel")

	// 3. Send HTTP request to Replica A (`alice-tunnel.instances.example.com`) and verify it forwards over :8082 to Replica B!
	{
		req := httptest.NewRequest(http.MethodGet, "https://alice-tunnel.instances.example.com/hello?x=1", nil)
		req.Host = "alice-tunnel.instances.example.com"
		req.Header.Set("X-Test-Email", "bob@example.com")
		rec := httptest.NewRecorder()
		mainA.Config.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK via peer forwarding A->B, got %d (%s)", rec.Code, rec.Body.String())
		}
		if rec.Body.String() != "from-tunnel:alice-tunnel.instances.example.com:assertion=true" {
			t.Fatalf("unexpected tunnel response body %q", rec.Body.String())
		}
		if peerForwards.Load() == 0 {
			t.Fatalf("expected peerForwards metric > 0")
		}
	}

	// 4. Verify WebSocket upgrade across Replica A -> Replica B (:8082) -> connected tunnel!
	{
		u, _ := url.Parse(mainA.URL)
		rawConn, err := net.DialTimeout("tcp", u.Host, 5*time.Second)
		if err != nil {
			t.Fatalf("Dial mainA: %v", err)
		}
		defer rawConn.Close()
		wsConn, _, err := dialWSUpgrade(ctx, rawConn, "alice-tunnel.instances.example.com", "/ws", http.Header{
			"X-Test-Email": []string{"bob@example.com"},
		})
		if err != nil {
			t.Fatalf("dialWSUpgrade across peer forward A->B: %v", err)
		}
		defer wsConn.Close()
		_, _ = wsConn.Write([]byte("hey"))
		buf := make([]byte, 64)
		n, err := wsConn.Read(buf)
		if err != nil {
			t.Fatalf("wsConn.Read: %v", err)
		}
		if string(buf[:n]) != "ws-tunnel:hey" {
			t.Fatalf("unexpected ws reply across peer forward: %q", string(buf[:n]))
		}
	}

	// 5. Verify :8082 peer listener security: rejects missing auth (401) and X-Registry-Hop > 1 (508 Loop Detected).
	{
		resNoAuth, err := http.Get(peerA.URL + "/internal/tunnel/alice-tunnel")
		if err != nil {
			t.Fatalf("GET peerA no auth: %v", err)
		}
		_ = resNoAuth.Body.Close()
		if resNoAuth.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401 on :8082 without peer token, got %d", resNoAuth.StatusCode)
		}

		peerTok, err := rtA.peerTokens.GetToken(ctx)
		if err != nil {
			t.Fatalf("GetToken: %v", err)
		}
		reqLoop, _ := http.NewRequest(http.MethodGet, peerA.URL+"/internal/tunnel/alice-tunnel", nil)
		reqLoop.Header.Set(HeaderPeerAuth, peerTok)
		reqLoop.Header.Set(HeaderRegistryHop, "2")
		resLoop, err := http.DefaultClient.Do(reqLoop)
		if err != nil {
			t.Fatalf("Do peerA loop: %v", err)
		}
		_ = resLoop.Body.Close()
		if resLoop.StatusCode != http.StatusLoopDetected {
			t.Fatalf("expected 508 Loop Detected for X-Registry-Hop: 2, got %d", resLoop.StatusCode)
		}
	}

	// 6. Verify MaxPeerInitiatedStreams = 0: clientMuxB opening a stream to the registry gets reset immediately
	// and increments peerRejectedStreams!
	{
		stStream, err := clientMuxB.OpenStream(ctx)
		if err == nil {
			_, _ = stStream.Write([]byte("illegal-peer-stream"))
			_, readErr := stStream.Read(make([]byte, 8))
			_ = stStream.Close()
			if readErr == nil {
				t.Fatalf("expected peer-initiated stream from connected grain to be reset by registry")
			}
		}
		deadline := time.Now().Add(2 * time.Second)
		for peerRejectedStreams.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if peerRejectedStreams.Load() == 0 {
			t.Fatalf("expected peerRejectedStreams metric to increment when grain attempts to open stream")
		}
	}

	// 7. Verify concurrent stream cap (MaxConcurrentStreams = 4 on test router): 5th concurrent request returns 503 + Retry-After: 2.
	{
		var wg sync.WaitGroup
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				req := httptest.NewRequest(http.MethodGet, "https://alice-tunnel.instances.example.com/hold", nil)
				req.Host = "alice-tunnel.instances.example.com"
				req.Header.Set("X-Test-Email", "bob@example.com")
				rec := httptest.NewRecorder()
				mainB.Config.Handler.ServeHTTP(rec, req)
			}()
		}
		tunB := rtB.GetLocalTunnel("alice-tunnel")
		deadline := time.Now().Add(2 * time.Second)
		for tunB.ActiveStreams() < 4 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}

		// 5th stream must fail immediately with 503 + Retry-After: 2.
		req5 := httptest.NewRequest(http.MethodGet, "https://alice-tunnel.instances.example.com/hello", nil)
		req5.Host = "alice-tunnel.instances.example.com"
		req5.Header.Set("X-Test-Email", "bob@example.com")
		rec5 := httptest.NewRecorder()
		mainB.Config.Handler.ServeHTTP(rec5, req5)
		if rec5.Code != http.StatusServiceUnavailable || rec5.Header().Get("Retry-After") != "2" {
			t.Fatalf("expected 503 with Retry-After: 2 when stream cap reached, got %d (Retry-After=%q)", rec5.Code, rec5.Header().Get("Retry-After"))
		}

		close(blockCh)
		wg.Wait()
	}

	// 8. Verify 2s `reconnect_grace`: close tunnel on Replica B, start a request on Replica A,
	// and reconnect within 150ms on Replica A using `connected_principal` (`runner@proj.iam.gserviceaccount.com`)!
	{
		_ = clientMuxB.Close()
		deadline := time.Now().Add(2 * time.Second)
		for rtB.GetLocalTunnel("alice-tunnel") != nil && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}

		go func() {
			time.Sleep(120 * time.Millisecond)
			_, respA := dialTestTunnelClient(t, mainA.URL+"/api/entries/alice-tunnel/connect", "alice-tunnel", http.Header{
				"X-Test-Email": []string{"runner@proj.iam.gserviceaccount.com"},
			}, grainHandler)
			if respA == nil || respA.StatusCode != http.StatusSwitchingProtocols {
				t.Errorf("expected reconnect on mainA as connected_principal to succeed, got %+v", respA)
			}
		}()

		reqGrace := httptest.NewRequest(http.MethodGet, "https://alice-tunnel.instances.example.com/hello", nil)
		reqGrace.Host = "alice-tunnel.instances.example.com"
		reqGrace.Header.Set("X-Test-Email", "bob@example.com")
		recGrace := httptest.NewRecorder()
		mainA.Config.Handler.ServeHTTP(recGrace, reqGrace)
		if recGrace.Code != http.StatusOK {
			t.Fatalf("expected request during 2s reconnect_grace to succeed once reconnected, got %d (%s)", recGrace.Code, recGrace.Body.String())
		}
	}

	// 9. Verify immediate tunnel termination when `connected_principal` is changed (or owner transferred).
	{
		if rtA.GetLocalTunnel("alice-tunnel") == nil {
			t.Fatalf("expected active tunnel on Replica A before principal change")
		}
		curEntry, err := st.GetEntryByID(ctx, connEntry.ID)
		if err != nil {
			t.Fatalf("GetEntryByID: %v", err)
		}
		if _, err := st.UpdateConnectedPrincipal(ctx, connEntry.ID, curEntry.Version, "serviceAccount:other@proj.iam.gserviceaccount.com"); err != nil {
			t.Fatalf("UpdateConnectedPrincipal: %v", err)
		}
		rtA.ReconcileLocalTunnel(ctx, "alice-tunnel")
		deadline := time.Now().Add(2 * time.Second)
		for rtA.GetLocalTunnel("alice-tunnel") != nil && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if rtA.GetLocalTunnel("alice-tunnel") != nil {
			t.Fatalf("expected tunnel on Replica A to be terminated immediately after connected_principal change")
		}
	}
}

// TestReconnectGraceSurvivesAStaleWake pins down the race behind the
// reconnect_grace flake in
// TestOutboundTunnelConnectPeerForwardingReconnectGraceAndImmediateRevocation.
// The tunnel cache is refreshed from PostgreSQL asynchronously, so a refresh
// that read the old session before its replica released it can re-mark that
// dead session active while a request is waiting out reconnect_grace, waking
// it. The woken request forwards to the old replica, which says the tunnel is
// gone; it must go back to waiting for the real reconnect within the same
// grace rather than answer 503 entry_offline at once.
func TestReconnectGraceSurvivesAStaleWake(t *testing.T) {
	admitter, err := webauth.NewAdmitter(webauth.AdmissionConfig{
		Verifier:       testIdentityVerifier{},
		AllowedDomains: []string{"example.com"},
	})
	if err != nil {
		t.Fatalf("NewAdmitter: %v", err)
	}
	const grace = 10 * time.Second
	rt, err := NewRouter(RouterOptions{
		RegistryHost:   "registry.example.com",
		InstanceDomain: "instances.example.com",
		ReplicaID:      "replica-a",
		ReconnectGrace: grace,
		Admitter:       admitter,
		Signer:         newTestAssertionSigner(t),
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	defer rt.Close()

	// The old replica no longer holds the tunnel: every forward to it is told the tunnel is gone.
	var goneForwards atomic.Int64
	oldPeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		goneForwards.Add(1)
		w.Header().Set(HeaderTunnelGone, "1")
		writeJSONError(w, http.StatusServiceUnavailable, "tunnel_gone", "tunnel session is not active on this replica")
	}))
	defer oldPeer.Close()
	// The replica the tunnel reconnects to.
	newPeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "reconnected")
	}))
	defer newPeer.Close()

	tc := rt.TunnelCache()
	oldSession := TunnelCacheEntry{Name: "alice-tunnel", SessionEpoch: 1, ReplicaID: "replica-b", PeerAddr: oldPeer.Listener.Addr().String()}
	tc.SetActive(oldSession)

	entry := &store.EntryRow{Name: "alice-tunnel", Kind: api.KindConnected, OwnerEmail: "alice@example.com"}
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		req := httptest.NewRequest(http.MethodGet, "https://alice-tunnel.instances.example.com/hello", nil)
		req.Host = "alice-tunnel.instances.example.com"
		rt.routeConnectedEntry(rec, req, entry, false, time.Time{})
	}()

	waitForWaiter := func(forwards int64) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for (tc.ActiveWaiters("alice-tunnel") == 0 || goneForwards.Load() < forwards) && time.Now().Before(deadline) {
			select {
			case <-done:
				t.Fatalf("request finished before its reconnect_grace ran out: %d %s", rec.Code, rec.Body.String())
			case <-time.After(5 * time.Millisecond):
			}
		}
		if tc.ActiveWaiters("alice-tunnel") == 0 {
			t.Fatalf("request never entered reconnect_grace (forwards to old replica: %d)", goneForwards.Load())
		}
	}

	// The request forwards to the old replica, learns the tunnel is gone and waits.
	waitForWaiter(1)
	// A stale refresh re-marks the dead session active and wakes the waiter; the
	// old replica says gone again, and the request must keep waiting.
	tc.SetActive(oldSession)
	waitForWaiter(2)
	// The tunnel really reconnects, on another replica.
	tc.SetActive(TunnelCacheEntry{Name: "alice-tunnel", SessionEpoch: 2, ReplicaID: "replica-c", PeerAddr: newPeer.Listener.Addr().String()})

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("request still waiting after the tunnel reconnected")
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "reconnected" {
		t.Fatalf("expected request held across a stale wake to reach the reconnected tunnel, got %d (%s)", rec.Code, rec.Body.String())
	}
	if elapsed := time.Since(start); elapsed >= grace {
		t.Fatalf("request took %s, longer than its %s reconnect_grace", elapsed, grace)
	}
}

// TestReconnectGraceIsOneWindowAcrossStaleWakes checks the other half of the
// fix: a request woken by stale refreshes waits out what is left of the grace
// it started, never a fresh grace per wake.
func TestReconnectGraceIsOneWindowAcrossStaleWakes(t *testing.T) {
	admitter, err := webauth.NewAdmitter(webauth.AdmissionConfig{
		Verifier:       testIdentityVerifier{},
		AllowedDomains: []string{"example.com"},
	})
	if err != nil {
		t.Fatalf("NewAdmitter: %v", err)
	}
	const grace = 400 * time.Millisecond
	rt, err := NewRouter(RouterOptions{
		RegistryHost:   "registry.example.com",
		InstanceDomain: "instances.example.com",
		ReplicaID:      "replica-a",
		ReconnectGrace: grace,
		Admitter:       admitter,
		Signer:         newTestAssertionSigner(t),
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	defer rt.Close()

	oldPeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(HeaderTunnelGone, "1")
		writeJSONError(w, http.StatusServiceUnavailable, "tunnel_gone", "tunnel session is not active on this replica")
	}))
	defer oldPeer.Close()

	tc := rt.TunnelCache()
	oldSession := TunnelCacheEntry{Name: "alice-tunnel", SessionEpoch: 1, ReplicaID: "replica-b", PeerAddr: oldPeer.Listener.Addr().String()}
	tc.SetActive(oldSession)

	// Keep re-marking the dead session active, as a stream of stale refreshes would.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				tc.SetActive(oldSession)
			}
		}
	}()
	defer func() {
		close(stop)
		wg.Wait()
	}()

	entry := &store.EntryRow{Name: "alice-tunnel", Kind: api.KindConnected, OwnerEmail: "alice@example.com"}
	req := httptest.NewRequest(http.MethodGet, "https://alice-tunnel.instances.example.com/hello", nil)
	req.Host = "alice-tunnel.instances.example.com"
	rec := httptest.NewRecorder()
	start := time.Now()
	rt.routeConnectedEntry(rec, req, entry, false, time.Time{})
	elapsed := time.Since(start)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 once reconnect_grace ran out with no reconnect, got %d (%s)", rec.Code, rec.Body.String())
	}
	if elapsed < grace-50*time.Millisecond {
		t.Fatalf("request answered after %s, inside its %s reconnect_grace", elapsed, grace)
	}
	if elapsed > 4*grace {
		t.Fatalf("request held %s across stale wakes; reconnect_grace is %s", elapsed, grace)
	}
}

func TestReconnectWaiterCap32AndDelegationAndSIGTERMDrain(t *testing.T) {
	admitter, err := webauth.NewAdmitter(webauth.AdmissionConfig{
		Verifier:          testIdentityVerifier{},
		AllowedDomains:    []string{"example.com"},
		ServicePrincipals: []string{"serviceAccount:board@proj.iam.gserviceaccount.com"},
	})
	if err != nil {
		t.Fatalf("NewAdmitter: %v", err)
	}
	signer := newTestAssertionSigner(t)

	// 1. Test WaitForReconnect bounded to 32 concurrent waiters (33rd fails immediately with ErrTooManyReconnectWaiters).
	tc := NewTunnelCache(nil, 15*time.Second)
	tc.MarkDisconnected("busy-entry", 1, time.Now(), "graceful shutdown")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < MaxReconnectWaiters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _ = tc.WaitForReconnect(ctx, "busy-entry", 5*time.Second)
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for tc.ActiveWaiters("busy-entry") < MaxReconnectWaiters && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if _, _, err := tc.WaitForReconnect(ctx, "busy-entry", 5*time.Second); !errors.Is(err, ErrTooManyReconnectWaiters) {
		t.Fatalf("expected 33rd waiter to fail immediately with ErrTooManyReconnectWaiters, got %v", err)
	}
	cancel()
	wg.Wait()

	// 2. Test X-Grain-On-Behalf-Of delegation (registry host -> 400 delegation_not_supported; human caller -> 400 delegation_requires_service_caller; browser headers -> 403 delegation_from_browser; upgrade -> 403 not_delegable; unauthorized service principal -> 403 delegation_refused; read_only_grant POST -> 403 read_only_grant).
	rt, err := NewRouter(RouterOptions{
		RegistryHost:   "registry.example.com",
		InstanceDomain: "instances.example.com",
		Admitter:       admitter,
		Signer:         signer,
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	defer rt.Close()

	rt.HostCache().Put(&store.EntryRow{
		ID:             "e-1",
		Name:           "alice-vm",
		Kind:           api.KindInstance,
		LifecycleState: api.StateRunning,
		OwnerEmail:     "alice@example.com",
		Visibility:     store.VisibilityOwner,
		BackendURL:     "http://127.0.0.1:1",
	})
	h := rt.WrapMainHandler(http.NotFoundHandler())

	reqRegistryOBO := httptest.NewRequest(http.MethodGet, "https://registry.example.com/api/directory", nil)
	reqRegistryOBO.Host = "registry.example.com"
	reqRegistryOBO.Header.Set("X-Test-Email", "board@proj.iam.gserviceaccount.com")
	reqRegistryOBO.Header.Set(HeaderOnBehalfOf, "alice@example.com")
	recRegistryOBO := httptest.NewRecorder()
	h.ServeHTTP(recRegistryOBO, reqRegistryOBO)
	if recRegistryOBO.Code != http.StatusBadRequest || !strings.Contains(recRegistryOBO.Body.String(), "delegation_not_supported") {
		t.Fatalf("expected 400 delegation_not_supported on registry host, got %d %s", recRegistryOBO.Code, recRegistryOBO.Body.String())
	}

	reqHumanOBO := httptest.NewRequest(http.MethodGet, "https://alice-vm.instances.example.com/", nil)
	reqHumanOBO.Host = "alice-vm.instances.example.com"
	reqHumanOBO.Header.Set("X-Test-Email", "alice@example.com")
	reqHumanOBO.Header.Set(HeaderOnBehalfOf, "bob@example.com")
	recHumanOBO := httptest.NewRecorder()
	h.ServeHTTP(recHumanOBO, reqHumanOBO)
	if recHumanOBO.Code != http.StatusBadRequest || !strings.Contains(recHumanOBO.Body.String(), "delegation_requires_service_caller") {
		t.Fatalf("expected 400 delegation_requires_service_caller when human sends X-Grain-On-Behalf-Of, got %d %s", recHumanOBO.Code, recHumanOBO.Body.String())
	}

	reqBrowserOBO := httptest.NewRequest(http.MethodGet, "https://alice-vm.instances.example.com/", nil)
	reqBrowserOBO.Host = "alice-vm.instances.example.com"
	reqBrowserOBO.Header.Set("X-Test-Email", "board@proj.iam.gserviceaccount.com")
	reqBrowserOBO.Header.Set(HeaderOnBehalfOf, "alice@example.com")
	reqBrowserOBO.Header.Set("Origin", "https://evil.example.com")
	recBrowserOBO := httptest.NewRecorder()
	h.ServeHTTP(recBrowserOBO, reqBrowserOBO)
	if recBrowserOBO.Code != http.StatusForbidden || !strings.Contains(recBrowserOBO.Body.String(), "delegation_from_browser") {
		t.Fatalf("expected 403 delegation_from_browser when browser headers accompany X-Grain-On-Behalf-Of, got %d %s", recBrowserOBO.Code, recBrowserOBO.Body.String())
	}

	reqUpgradeOBO := httptest.NewRequest(http.MethodGet, "https://alice-vm.instances.example.com/api/hostd/connect", nil)
	reqUpgradeOBO.Host = "alice-vm.instances.example.com"
	reqUpgradeOBO.Header.Set("X-Test-Email", "board@proj.iam.gserviceaccount.com")
	reqUpgradeOBO.Header.Set(HeaderOnBehalfOf, "alice@example.com")
	reqUpgradeOBO.Header.Set("Connection", "Upgrade")
	reqUpgradeOBO.Header.Set("Upgrade", "websocket")
	recUpgradeOBO := httptest.NewRecorder()
	h.ServeHTTP(recUpgradeOBO, reqUpgradeOBO)
	if recUpgradeOBO.Code != http.StatusForbidden || !strings.Contains(recUpgradeOBO.Body.String(), "not_delegable") {
		t.Fatalf("expected 403 not_delegable on upgrade with X-Grain-On-Behalf-Of, got %d %s", recUpgradeOBO.Code, recUpgradeOBO.Body.String())
	}

	reqServiceOBO := httptest.NewRequest(http.MethodGet, "https://alice-vm.instances.example.com/", nil)
	reqServiceOBO.Host = "alice-vm.instances.example.com"
	reqServiceOBO.Header.Set("X-Test-Email", "board@proj.iam.gserviceaccount.com")
	reqServiceOBO.Header.Set(HeaderOnBehalfOf, "alice@example.com")
	recServiceOBO := httptest.NewRecorder()
	h.ServeHTTP(recServiceOBO, reqServiceOBO)
	if recServiceOBO.Code != http.StatusForbidden || !strings.Contains(recServiceOBO.Body.String(), "delegation_refused") {
		t.Fatalf("expected 403 delegation_refused, got %d %s", recServiceOBO.Code, recServiceOBO.Body.String())
	}

	// Read-only grant refuses POST with 403 read_only_grant.
	rt.delegation = testDelegationAuthorizer{
		allowedActor: "serviceAccount:board@proj.iam.gserviceaccount.com",
		access:       "read",
	}
	reqReadOnlyPost := httptest.NewRequest(http.MethodPost, "https://alice-vm.instances.example.com/api/tasks", strings.NewReader(`{}`))
	reqReadOnlyPost.Host = "alice-vm.instances.example.com"
	reqReadOnlyPost.Header.Set("X-Test-Email", "board@proj.iam.gserviceaccount.com")
	reqReadOnlyPost.Header.Set(HeaderOnBehalfOf, "alice@example.com")
	recReadOnlyPost := httptest.NewRecorder()
	h.ServeHTTP(recReadOnlyPost, reqReadOnlyPost)
	if recReadOnlyPost.Code != http.StatusForbidden || !strings.Contains(recReadOnlyPost.Body.String(), "read_only_grant") {
		t.Fatalf("expected 403 read_only_grant on POST with read grant, got %d %s", recReadOnlyPost.Code, recReadOnlyPost.Body.String())
	}

	// Authorized service principal delegation (DelegationAuthorizer permits board@proj.iam.gserviceaccount.com, target user is owner):
	// Mints X-Grain-Registry-Assertion with `sub = "alice@example.com"` and `act.sub = "serviceAccount:board@proj.iam.gserviceaccount.com"`,
	// and strips X-Grain-On-Behalf-Of and X-Grain-Subject-Token before forwarding.
	rt.delegation = testDelegationAuthorizer{
		allowedActor: "serviceAccount:board@proj.iam.gserviceaccount.com",
		access:       "write",
	}
	var gotBackendOBO, gotBackendSubjectToken string
	delegatedBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBackendOBO = r.Header.Get(HeaderOnBehalfOf)
		gotBackendSubjectToken = r.Header.Get(HeaderSubjectToken)
		w.WriteHeader(http.StatusOK)
	}))
	defer delegatedBackend.Close()

	rt.HostCache().Put(&store.EntryRow{
		ID:             "e-1",
		Name:           "alice-vm",
		Kind:           api.KindInstance,
		LifecycleState: api.StateRunning,
		OwnerEmail:     "alice@example.com",
		Visibility:     store.VisibilityShared,
		AccessList:     []string{"serviceAccount:board@proj.iam.gserviceaccount.com"},
		BackendURL:     delegatedBackend.URL,
	})
	reqValidOBO := httptest.NewRequest(http.MethodGet, "https://alice-vm.instances.example.com/", nil)
	reqValidOBO.Host = "alice-vm.instances.example.com"
	reqValidOBO.Header.Set("X-Test-Email", "board@proj.iam.gserviceaccount.com")
	reqValidOBO.Header.Set(HeaderOnBehalfOf, "alice@example.com")
	reqValidOBO.Header.Set(HeaderSubjectToken, "subject-jwt-should-be-stripped")
	recValidOBO := httptest.NewRecorder()
	h.ServeHTTP(recValidOBO, reqValidOBO)
	if recValidOBO.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on authorized X-Grain-On-Behalf-Of, got %d %s", recValidOBO.Code, recValidOBO.Body.String())
	}
	if gotBackendOBO != "" || gotBackendSubjectToken != "" {
		t.Fatalf("expected X-Grain-On-Behalf-Of and X-Grain-Subject-Token stripped, got obo=%q subjectToken=%q", gotBackendOBO, gotBackendSubjectToken)
	}
	signer.mu.Lock()
	var lastClaims AssertionClaims
	if len(signer.signed) > 0 {
		lastClaims = signer.signed[len(signer.signed)-1]
	}
	signer.mu.Unlock()
	if lastClaims.Email != "alice@example.com" || lastClaims.Actor == nil || lastClaims.Actor.Sub != "serviceAccount:board@proj.iam.gserviceaccount.com" {
		t.Fatalf("unexpected delegated assertion claims: %+v (actor=%+v)", lastClaims, lastClaims.Actor)
	}

	// 3. Test RateLimiters (`FailedAuthPerIP` burst 5 -> 6th returns 429 with Retry-After) and `BeginDrain` rejecting `/connect` with 503.
	for i := 0; i < 5; i++ {
		if ok, _ := rt.limiters.FailedAuthPerIP.Allow("203.0.113.9"); !ok {
			t.Fatalf("expected FailedAuthPerIP attempt %d to be allowed", i+1)
		}
	}
	if ok, retryAfter := rt.limiters.FailedAuthPerIP.Allow("203.0.113.9"); ok || retryAfter < 1 {
		t.Fatalf("expected 6th FailedAuthPerIP attempt to be rate-limited, got ok=%v retryAfter=%d", ok, retryAfter)
	}

	rt.BeginDrain(context.Background())
	if !rt.IsDraining() {
		t.Fatalf("expected rt.IsDraining() == true after BeginDrain")
	}
	reqDrainConnect := httptest.NewRequest(http.MethodGet, "https://registry.example.com/api/entries/alice-vm/connect", nil)
	reqDrainConnect.RemoteAddr = "198.51.100.10:12345"
	recDrainConnect := httptest.NewRecorder()
	rt.HandleConnect(recDrainConnect, reqDrainConnect, "alice-vm", true)
	if recDrainConnect.Code != http.StatusServiceUnavailable || recDrainConnect.Header().Get("Retry-After") != "2" {
		t.Fatalf("expected 503 with Retry-After: 2 on /connect while draining, got %d", recDrainConnect.Code)
	}
}

func TestStopStartStatePagesAndAutoWakeOnVisit(t *testing.T) {
	pgURL := startEphemeralPostgres(t)
	ctx := context.Background()
	st, err := store.Open(ctx, pgURL, store.DefaultThresholds())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	if err := st.SeedShapes(ctx, []store.ShapeRow{
		{Name: "small", MachineType: "e2-standard-4", BootDiskGB: 50, QuotaUnits: 1},
	}); err != nil {
		t.Fatalf("SeedShapes: %v", err)
	}

	autoWakeFalse := false
	autoWakeTrue := true

	vmStopping, err := st.CreateEntry(ctx, store.CreateEntryInput{
		Name:       "vm-stopping",
		Kind:       api.KindInstance,
		OwnerEmail: "alice@example.com",
		Visibility: store.VisibilityPublic,
		Shape:      "small",
	})
	if err != nil {
		t.Fatalf("CreateEntry vm-stopping: %v", err)
	}
	vmStopping, err = st.TransitionEntryState(ctx, vmStopping.ID, vmStopping.Version, api.StateRunning, "")
	if err != nil {
		t.Fatalf("TransitionEntryState vm-stopping running: %v", err)
	}
	if _, err := st.BeginStopEntry(ctx, vmStopping.ID, vmStopping.Version, "stopping by alice"); err != nil {
		t.Fatalf("BeginStopEntry vm-stopping: %v", err)
	}

	vmStoppedNoWake, err := st.CreateEntry(ctx, store.CreateEntryInput{
		Name:       "vm-stopped-nowake",
		Kind:       api.KindInstance,
		OwnerEmail: "carol@example.com",
		Visibility: store.VisibilityPublic,
		Shape:      "small",
		AutoWake:   &autoWakeFalse,
	})
	if err != nil {
		t.Fatalf("CreateEntry vm-stopped-nowake: %v", err)
	}
	vmStoppedNoWake, err = st.TransitionEntryState(ctx, vmStoppedNoWake.ID, vmStoppedNoWake.Version, api.StateRunning, "")
	if err != nil {
		t.Fatalf("TransitionEntryState vm-stopped-nowake: %v", err)
	}
	vmStoppedNoWake, err = st.BeginStopEntry(ctx, vmStoppedNoWake.ID, vmStoppedNoWake.Version, "stopping by alice")
	if err != nil {
		t.Fatalf("BeginStopEntry vm-stopped-nowake: %v", err)
	}
	if _, err := st.CompleteStopEntry(ctx, vmStoppedNoWake.ID, vmStoppedNoWake.Version); err != nil {
		t.Fatalf("CompleteStopEntry vm-stopped-nowake: %v", err)
	}

	vmStoppedWake, err := st.CreateEntry(ctx, store.CreateEntryInput{
		Name:       "vm-stopped-wake",
		Kind:       api.KindInstance,
		OwnerEmail: "alice@example.com",
		Visibility: store.VisibilityOwner,
		Shape:      "small",
		AutoWake:   &autoWakeTrue,
	})
	if err != nil {
		t.Fatalf("CreateEntry vm-stopped-wake: %v", err)
	}
	vmStoppedWake, err = st.TransitionEntryState(ctx, vmStoppedWake.ID, vmStoppedWake.Version, api.StateRunning, "")
	if err != nil {
		t.Fatalf("TransitionEntryState vm-stopped-wake: %v", err)
	}
	vmStoppedWake, err = st.BeginStopEntry(ctx, vmStoppedWake.ID, vmStoppedWake.Version, "stopping by alice")
	if err != nil {
		t.Fatalf("BeginStopEntry vm-stopped-wake: %v", err)
	}
	if _, err := st.CompleteStopEntry(ctx, vmStoppedWake.ID, vmStoppedWake.Version); err != nil {
		t.Fatalf("CompleteStopEntry vm-stopped-wake: %v", err)
	}

	admitter, err := webauth.NewAdmitter(webauth.AdmissionConfig{
		Verifier:       testIdentityVerifier{},
		AllowedDomains: []string{"example.com"},
		Admins:         []string{"user:admin@example.com"},
	})
	if err != nil {
		t.Fatalf("NewAdmitter: %v", err)
	}
	rt, err := NewRouter(RouterOptions{
		RegistryHost:   "registry.example.com",
		InstanceDomain: "instances.example.com",
		ReplicaID:      "replica-step17",
		Store:          st,
		Admitter:       admitter,
		Signer:         newTestAssertionSigner(t),
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	defer rt.Close()

	h := rt.WrapMainHandler(http.NotFoundHandler())

	// 1. Stopping entry returns 503 + Retry-After: 5 (both HTML and JSON).
	{
		req := httptest.NewRequest(http.MethodGet, "https://vm-stopping.instances.example.com/", nil)
		req.Host = "vm-stopping.instances.example.com"
		req.Header.Set("X-Test-Email", "bob@example.com")
		req.Header.Set("Accept", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "5" || !strings.Contains(rec.Body.String(), "entry_stopping") {
			t.Fatalf("expected 503 entry_stopping for vm-stopping, got %d %s", rec.Code, rec.Body.String())
		}
	}

	// 2. Stopped entry with auto_wake=false returns 503 entry_stopped and stays stopped.
	{
		req := httptest.NewRequest(http.MethodGet, "https://vm-stopped-nowake.instances.example.com/", nil)
		req.Host = "vm-stopped-nowake.instances.example.com"
		req.Header.Set("X-Test-Email", "bob@example.com")
		req.Header.Set("Accept", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "entry_stopped") {
			t.Fatalf("expected 503 entry_stopped for vm-stopped-nowake, got %d %s", rec.Code, rec.Body.String())
		}
		got, err := st.GetEntryByName(ctx, "vm-stopped-nowake")
		if err != nil || got.LifecycleState != api.StateStopped {
			t.Fatalf("expected vm-stopped-nowake to stay stopped, got %+v (err=%v)", got, err)
		}
	}

	// 3. Stopped entry with auto_wake=true and visibility=owner:
	// Unauthorized visitor (bob) gets 403 Forbidden and does NOT wake the instance.
	{
		reqBob := httptest.NewRequest(http.MethodGet, "https://vm-stopped-wake.instances.example.com/", nil)
		reqBob.Host = "vm-stopped-wake.instances.example.com"
		reqBob.Header.Set("X-Test-Email", "bob@example.com")
		reqBob.Header.Set("Accept", "text/html")
		recBob := httptest.NewRecorder()
		h.ServeHTTP(recBob, reqBob)
		if recBob.Code != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden for bob on owner-private stopped instance, got %d", recBob.Code)
		}
		gotAfterBob, err := st.GetEntryByName(ctx, "vm-stopped-wake")
		if err != nil || gotAfterBob.LifecycleState != api.StateStopped {
			t.Fatalf("expected vm-stopped-wake to remain stopped after unauthorized visit, got %+v (err=%v)", gotAfterBob, err)
		}

		// Authorized owner (alice) visits -> atomically wakes stopped -> creating and serves 503 Waking state page!
		reqAlice := httptest.NewRequest(http.MethodGet, "https://vm-stopped-wake.instances.example.com/", nil)
		reqAlice.Host = "vm-stopped-wake.instances.example.com"
		reqAlice.Header.Set("X-Test-Email", "alice@example.com")
		reqAlice.Header.Set("Accept", "text/html")
		recAlice := httptest.NewRecorder()
		h.ServeHTTP(recAlice, reqAlice)
		if recAlice.Code != http.StatusServiceUnavailable || recAlice.Header().Get("Retry-After") != "5" {
			t.Fatalf("expected 503 with Retry-After: 5 on auto-wake visit, got %d", recAlice.Code)
		}
		if !strings.Contains(recAlice.Body.String(), "waking up") {
			t.Fatalf("expected waking up state page on auto-wake visit, got %s", recAlice.Body.String())
		}
		gotAfterAlice, err := st.GetEntryByName(ctx, "vm-stopped-wake")
		if err != nil || gotAfterAlice.LifecycleState != api.StateCreating {
			t.Fatalf("expected vm-stopped-wake to transition stopped -> creating on owner visit, got %+v (err=%v)", gotAfterAlice, err)
		}
	}
}

func TestServeInstanceHostAssertionAcceptedByWebauthRegistryVerifier(t *testing.T) {
	signer := newTestAssertionSigner(t)
	jwksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		pub := signer.priv.PublicKey
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{{
				"kty": "EC",
				"crv": "P-256",
				"alg": "ES256",
				"use": "sig",
				"kid": signer.kid,
				"x":   base64.RawURLEncoding.EncodeToString(pub.X.FillBytes(make([]byte, 32))),
				"y":   base64.RawURLEncoding.EncodeToString(pub.Y.FillBytes(make([]byte, 32))),
			}},
		})
	}))
	defer jwksSrv.Close()

	var gotAssertion string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAssertion = r.Header.Get(api.HeaderRegistryAssertion)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	admitter, err := webauth.NewAdmitter(webauth.AdmissionConfig{
		Verifier:       testIdentityVerifier{},
		AllowedDomains: []string{"example.com"},
		Creators:       []string{"user:alice@example.com"},
	})
	if err != nil {
		t.Fatalf("NewAdmitter: %v", err)
	}
	rt, err := NewRouter(RouterOptions{
		RegistryHost:   "registry.example.com",
		InstanceDomain: "instances.example.com",
		IAPAudience:    "/projects/123/global/backendServices/456",
		Admitter:       admitter,
		Signer:         signer,
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	defer rt.Close()

	rt.HostCache().Put(&store.EntryRow{
		ID:             "entry-iap-sub",
		Name:           "alice-vm",
		Kind:           api.KindInstance,
		LifecycleState: api.StateRunning,
		OwnerEmail:     "alice@example.com",
		Visibility:     store.VisibilityOwner,
		BackendURL:     backend.URL,
	})

	h := rt.WrapMainHandler(http.NotFoundHandler())
	req := httptest.NewRequest(http.MethodGet, "https://alice-vm.instances.example.com/api/tasks", nil)
	req.Host = "alice-vm.instances.example.com"
	req.Header.Set("X-Test-Email", "alice@example.com")
	// Cloud IAP puts a numeric Gaia ID in `sub`, not the user's email address.
	req.Header.Set("X-Test-Sub", "accounts.google.com:10492837492837492")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from routed request, got %d (%s)", rec.Code, rec.Body.String())
	}
	if gotAssertion == "" {
		t.Fatal("expected non-empty X-Grain-Registry-Assertion")
	}

	verifyCtx := webauth.WithRegistryConfig(context.Background(), webauth.RegistryConfig{
		Issuer:   "grain-registry",
		Audience: "https://alice-vm.instances.example.com",
		KeysURL:  jwksSrv.URL,
	})
	claims, err := webauth.ValidateRegistryToken(verifyCtx, gotAssertion, "https://alice-vm.instances.example.com")
	if err != nil {
		t.Fatalf("ValidateRegistryToken: %v", err)
	}
	id, err := webauth.IdentityFromRegistryClaims(claims)
	if err != nil {
		t.Fatalf("IdentityFromRegistryClaims failed on IAP numeric sub: %v", err)
	}
	if id.Email != "alice@example.com" || id.Kind != webauth.PrincipalHuman {
		t.Fatalf("unexpected verified identity: %+v", id)
	}
}
