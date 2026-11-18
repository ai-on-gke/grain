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
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type stubStagingSandboxes struct {
	upstream string
}

func (s stubStagingSandboxes) List(context.Context) ([]model.PersistentSandboxStatus, error) {
	return []model.PersistentSandboxStatus{{Name: "preview", Repo: "acme/web", State: "running", UISubpath: "/staging/preview"}}, nil
}
func (s stubStagingSandboxes) Logs(string) (string, bool)                    { return "", false }
func (s stubStagingSandboxes) Control(context.Context, string, string) error { return nil }
func (s stubStagingSandboxes) StripPrefix(name string) (bool, bool)          { return true, name == "preview" }
func (s stubStagingSandboxes) Dial(ctx context.Context, name string) (net.Conn, error) {
	if name != "preview" {
		return nil, fmt.Errorf("unknown sandbox %s", name)
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", s.upstream)
}

type customSessionVerifier struct{}

func (customSessionVerifier) Name() string              { return "custom-sso" }
func (customSessionVerifier) Headers() []string         { return []string{"X-Custom-SSO-Sig"} }
func (customSessionVerifier) SessionTTL() time.Duration { return time.Minute }
func (customSessionVerifier) Verify(r *http.Request) (ui.VerifiedIdentity, bool, error) {
	raw := strings.TrimSpace(r.Header.Get("X-Custom-SSO-Sig"))
	if raw == "" {
		return ui.VerifiedIdentity{}, false, nil
	}
	return ui.VerifiedIdentity{Email: raw, Kind: model.PrincipalHuman}, true, nil
}

// A persistent sandbox runs untrusted or pre-merge staging code behind grain's
// own origin (/staging/<name>/). Authenticating through IAP or a registered
// IdentityVerifier must never forward the signed assertion header, proxy/tunnel
// bearer headers, or grain_* session cookies into the staging environment,
// where they could be logged or replayed against the controller within the
// assertion's validity window.
func TestStagingProxyNeverForwardsSignedIdentityAssertionsOrGrainCookies(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	t.Cleanup(ui.RegisterIdentityVerifier(customSessionVerifier{}))

	var captured http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Clone()
		w.Header().Add("Set-Cookie", ui.VerifierSessionCookieName+"=planted; Path=/; Secure; HttpOnly")
		w.Header().Add("Set-Cookie", "GCP_IAAP_AUTH_TOKEN_ABC=planted-iap; Path=/; Secure; HttpOnly")
		w.Header().Add("Set-Cookie", "__Host-edge_sso=planted-edge; Path=/; Secure; HttpOnly")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	store, _ := sqlitetest.Open(t)
	ctx := context.Background()
	if err := store.PutConfig(ctx, model.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AddAllowedPrincipal(ctx, "user:alice@example.com", "seed", "", time.Now()); err != nil {
		t.Fatal(err)
	}

	cfg := ui.Config{
		Actor:               ui.DefaultActor("alice@example.com"),
		PersistentSandboxes: stubStagingSandboxes{upstream: strings.TrimPrefix(upstream.URL, "http://")},
		RequireVerifier:     "iap",
	}
	sharedIdentityProxy().configure(&cfg)
	srv := httptest.NewServer(ui.NewServer(cfg, store))
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/staging/preview/check", nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, vals := range sessionHeader(t, "alice@example.com") {
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("X-Custom-SSO-Sig", "alice@example.com")
	req.Header.Set("Proxy-Authorization", "Bearer oidc-edge-token")
	req.Header.Set("X-Grain-Tunnel-Token", "tunnel-token")
	req.Header.Set("X-Grain-Hub-Token", "grc_hub_token")
	req.Header.Set("X-Grain-Subject-Token", "subject-token")
	req.Header.Set(ui.ActingRunHeader, "run-123")
	req.Header.Set(ui.ActingReasonHeader, "audit-reason")
	req.Header.Set(ui.ChannelHeader, ui.SandboxCLIChannel)
	req.Header.Set(ui.HubAssertionHeader, "hub-jwt")
	req.Header.Set(ui.RegistryAssertionHeader, "registry-jwt")
	req.Header.Set("X-Goog-Iap-Attr-Sub", "iap-attr")
	req.Header.Set("X-Goog-Authenticated-User-Email", "accounts.google.com:alice@example.com")
	req.AddCookie(&http.Cookie{Name: ui.VerifierSessionCookieName, Value: "live-session"})
	req.AddCookie(&http.Cookie{Name: ui.AccessCookieName, Value: "live-access"})
	req.AddCookie(&http.Cookie{Name: "GCP_IAAP_AUTH_TOKEN_ABC", Value: "live-iap-cookie"})
	req.AddCookie(&http.Cookie{Name: "__Host-GCP_IAP_AUTH_TOKEN_ABC", Value: "live-host-iap-cookie"})
	req.AddCookie(&http.Cookie{Name: "GCP_IAPLOGIN", Value: "live-iap-login"})
	req.AddCookie(&http.Cookie{Name: "__Host-edge_sso", Value: "live-edge-sso"})
	req.AddCookie(&http.Cookie{Name: "preview_theme", Value: "dark"})

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /staging/preview/check status = %d, want 200", resp.StatusCode)
	}

	for _, secretHeader := range []string{
		ui.IAPAssertionHeader,
		"X-Goog-Iap-Attr-Sub",
		ui.HubAssertionHeader,
		ui.RegistryAssertionHeader,
		"X-Custom-SSO-Sig",
		"Authorization",
		"Proxy-Authorization",
		"X-Grain-Tunnel-Token",
		"X-Grain-Hub-Token",
		"X-Grain-Subject-Token",
		ui.AccessCodeHeader,
		ui.ActingTaskHeader,
		ui.ActingRunHeader,
		ui.ActingReasonHeader,
		ui.ChannelHeader,
		"X-Goog-Authenticated-User-Email",
	} {
		if got := captured.Get(secretHeader); got != "" {
			t.Errorf("staging sandbox received %s=%q; credentials and assertions must be stripped", secretHeader, got)
		}
	}
	if got := captured.Get("Cookie"); got != "preview_theme=dark" {
		t.Errorf("staging sandbox Cookie=%q, want only preview_theme=dark", got)
	}
	for _, sc := range resp.Header.Values("Set-Cookie") {
		lower := strings.ToLower(sc)
		if strings.Contains(lower, "grain_") || strings.Contains(lower, "gcp_ia") || strings.Contains(lower, "__host-") {
			t.Errorf("staging response leaked protected Set-Cookie %q", sc)
		}
	}
}

// A caller can spell a credential header the way its edge never does --
// lower case, underscores for dashes, sent twice -- and split or pad its
// Cookie header, and the staging app behind a CGI-style server still reads
// X_Goog_IAP_JWT_Assertion as HTTP_X_GOOG_IAP_JWT_ASSERTION. None of those
// spellings may reach the sandbox, while the app's own cookies arrive as
// the browser sent them.
func TestStagingProxyStripsEverySpellingOfCredentials(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	t.Cleanup(ui.RegisterIdentityVerifier(customSessionVerifier{}))

	var captured http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	store, _ := sqlitetest.Open(t)
	ctx := context.Background()
	if err := store.PutConfig(ctx, model.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AddAllowedPrincipal(ctx, "user:alice@example.com", "seed", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	cfg := ui.Config{
		Actor:               ui.DefaultActor("alice@example.com"),
		PersistentSandboxes: stubStagingSandboxes{upstream: strings.TrimPrefix(upstream.URL, "http://")},
		RequireVerifier:     "iap",
	}
	sharedIdentityProxy().configure(&cfg)
	srv := httptest.NewServer(ui.NewServer(cfg, store))
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/staging/preview/check", nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, vals := range sessionHeader(t, "alice@example.com") {
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}
	// Set directly, so the client sends the names exactly as written.
	req.Header["X_Goog_IAP_JWT_Assertion"] = []string{"underscore-iap-jwt"}
	req.Header["x_goog_authenticated_user_email"] = []string{"accounts.example.com:mallory@example.com"}
	req.Header["X_Grain_Access_Code"] = []string{"underscore-access-code"}
	req.Header["X_Custom_SSO_Sig"] = []string{"mallory@example.com"}
	req.Header["X_Forwarded_User"] = []string{"mallory"}
	req.Header["Proxy_Authorization"] = []string{"Bearer underscore-edge-token"}
	req.Header["x-grain-hub-assertion"] = []string{"lower-hub-jwt", "lower-hub-jwt-2"}
	req.Header["Cookie"] = []string{
		`app_json={"a":1}; grain_session=split-1`,
		`  __Host-edge_sso = split-2 ;app_ws=  padded;; GCP_IAP_UID="split-3"`,
		`__secure-GRAIN_access=split-4; app_utf=héllo; app_bs=a\b`,
	}
	req.Header.Set("X_App_Header", "keep-me")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /staging/preview/check status = %d, want 200", resp.StatusCode)
	}
	for k, vals := range captured {
		key := strings.ToLower(strings.ReplaceAll(k, "_", "-"))
		switch {
		case strings.HasPrefix(key, "x-goog-"), strings.HasPrefix(key, "x-grain-"),
			key == "x-custom-sso-sig", key == "x-forwarded-user", key == "proxy-authorization":
			t.Errorf("staging sandbox received %s=%q; every spelling of a credential must be stripped", k, vals)
		}
	}
	if got := captured.Get("X_App_Header"); got != "keep-me" {
		t.Errorf("X_App_Header = %q, want keep-me", got)
	}
	want := `app_json={"a":1}; app_ws=  padded; app_utf=héllo; app_bs=a\b`
	if got := captured.Values("Cookie"); len(got) != 1 || got[0] != want {
		t.Errorf("staging sandbox Cookie = %q, want [%q]", got, want)
	}
}

// A staging app builds its absolute URLs, redirects and Secure cookies
// from X-Forwarded-Proto and X-Forwarded-For. Behind the identity-aware
// edge the daemon is configured for, those are the edge's: a request whose
// signed assertion verified gets the edge's scheme and client chain. A
// caller that reaches the daemon directly -- with the access code, or with
// an assertion that does not verify -- cannot pass its own off as the
// edge's: the app hears the daemon's plain-http hop from the caller.
func TestStagingProxyTrustsForwardingHeadersOnlyFromTheVerifiedEdge(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)

	var captured http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	store, _ := sqlitetest.Open(t)
	ctx := context.Background()
	if err := store.PutConfig(ctx, model.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AddAllowedPrincipal(ctx, "user:alice@example.com", "seed", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	const accessCode = "staging-forwarding-access-code"
	cfg := ui.Config{
		Actor:               ui.DefaultActor("alice@example.com"),
		PersistentSandboxes: stubStagingSandboxes{upstream: strings.TrimPrefix(upstream.URL, "http://")},
		LocalAPIToken:       accessCode,
	}
	sharedIdentityProxy().configure(&cfg)
	srv := httptest.NewServer(ui.NewServer(cfg, store))
	t.Cleanup(srv.Close)

	edgeHeaders := http.Header{
		"X-Forwarded-Proto": {"https"},
		"X-Forwarded-For":   {"198.51.100.42"},
	}
	for _, tc := range []struct {
		name       string
		credential http.Header
		wantStatus int
		wantProto  string
		wantFor    string
	}{{
		name:       "signed assertion from the edge",
		credential: sessionHeader(t, "alice@example.com"),
		wantStatus: http.StatusOK,
		wantProto:  "https",
		wantFor:    "198.51.100.42, 127.0.0.1",
	}, {
		name:       "access code sent straight to the daemon",
		credential: http.Header{"Authorization": {"Bearer " + accessCode}},
		wantStatus: http.StatusOK,
		wantProto:  "http",
		wantFor:    "127.0.0.1",
	}, {
		name:       "unsigned assertion sent straight to the daemon",
		credential: http.Header{ui.IAPAssertionHeader: {"forged.assertion.value"}},
		wantStatus: http.StatusUnauthorized,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			captured = nil
			req, err := http.NewRequest(http.MethodGet, srv.URL+"/staging/preview/check", nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, h := range []http.Header{edgeHeaders, tc.credential} {
				for k, vals := range h {
					for _, v := range vals {
						req.Header.Add(k, v)
					}
				}
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("GET /staging/preview/check status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if tc.wantStatus != http.StatusOK {
				if captured != nil {
					t.Fatalf("staging sandbox was reached by a refused request: %v", captured)
				}
				return
			}
			if got := captured.Get("X-Forwarded-Proto"); got != tc.wantProto {
				t.Errorf("staging sandbox X-Forwarded-Proto = %q, want %q", got, tc.wantProto)
			}
			if got := strings.Join(captured.Values("X-Forwarded-For"), ", "); got != tc.wantFor {
				t.Errorf("staging sandbox X-Forwarded-For = %q, want %q", got, tc.wantFor)
			}
		})
	}
}
