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

package ui_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/golang-jwt/jwt/v5"
)

const (
	testOAuthWebClientID = "123456-web.apps.googleusercontent.com"
	testOAuthCLIClientID = "123456-cli.apps.googleusercontent.com"
	testOAuthKeyID       = "google-oauth-rsa-key-1"
)

func setupOAuthJWKSServer(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	n := base64.RawURLEncoding.EncodeToString(priv.PublicKey.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(priv.PublicKey.E)).Bytes())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{{
				"kty": "RSA",
				"alg": "RS256",
				"use": "sig",
				"kid": testOAuthKeyID,
				"n":   n,
				"e":   e,
			}},
		})
	}))
	t.Cleanup(srv.Close)
	return priv, srv.URL
}

func signOAuthIDToken(t *testing.T, priv *rsa.PrivateKey, aud, email, hd, iss string, verified *bool, exp time.Time) string {
	t.Helper()
	if iss == "" {
		iss = ui.GoogleOIDCIssuer
	}
	claims := ui.OAuthClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    iss,
			Subject:   "sub-" + email,
			Audience:  jwt.ClaimStrings{aud},
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-time.Minute)),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
		Email:         email,
		EmailVerified: verified,
		HD:            hd,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = testOAuthKeyID
	s, err := tok.SignedString(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func boolPtr(b bool) *bool { return &b }

func TestOAuthBearerIDTokenVerificationAndHostedDomains(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)

	priv, jwksURL := setupOAuthJWKSServer(t)

	srv, client := testServer(t)
	client.Config.OAuthClientID = testOAuthWebClientID
	client.Config.OAuthCLIClientID = testOAuthCLIClientID
	client.Config.OAuthKeysURL = jwksURL
	client.Config.OAuthHostedDomains = "example.com"
	client.Config.AllowedPrincipals = []string{
		"user:alice@example.com=admin",
		"serviceAccount:ci-bot@my-proj.iam.gserviceaccount.com=developer",
	}
	ui.SetAccessCodeForTest(srv, testAccessCode)

	// 1. Valid Web Client ID token with https://accounts.google.com issuer -> 200.
	webTok := signOAuthIDToken(t, priv, testOAuthWebClientID, "Alice@Example.com", "example.com", "https://accounts.google.com", boolPtr(true), time.Now().Add(5*time.Minute))
	rec := serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/auth/user", "", map[string]string{
		"Authorization": "Bearer " + webTok,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("valid web ID token: got %d, want 200: %s", rec.Code, rec.Body)
	}
	var authResp ui.AuthUserResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &authResp); err != nil {
		t.Fatal(err)
	}
	if !authResp.Authenticated || authResp.User != "alice@example.com" || authResp.AuthMethod != ui.AuthMethodOAuth {
		t.Fatalf("web ID token /api/auth/user = %+v, want alice@example.com via oauth", authResp)
	}
	if !slices.Contains(authResp.Roles, "admin") {
		t.Fatalf("expected roles to contain admin, got %+v", authResp.Roles)
	}

	// 2. Valid CLI Client ID token with bare accounts.google.com issuer -> 200.
	cliTok := signOAuthIDToken(t, priv, testOAuthCLIClientID, "alice@example.com", "example.com", "accounts.google.com", boolPtr(true), time.Now().Add(5*time.Minute))
	rec = serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/tasks", "", map[string]string{
		"Authorization": "Bearer " + cliTok,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("valid CLI ID token: got %d, want 200: %s", rec.Code, rec.Body)
	}

	// 3. Service account token without hd or email_verified -> 200 (service accounts have no Workspace hd).
	saTok := signOAuthIDToken(t, priv, testOAuthCLIClientID, "ci-bot@my-proj.iam.gserviceaccount.com", "", "", nil, time.Now().Add(5*time.Minute))
	rec = serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/auth/user", "", map[string]string{
		"Authorization": "Bearer " + saTok,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("valid SA ID token: got %d, want 200: %s", rec.Code, rec.Body)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &authResp)
	if authResp.User != "ci-bot@my-proj.iam.gserviceaccount.com" || !slices.Contains(authResp.Roles, "developer") {
		t.Fatalf("SA /api/auth/user = %+v, want developer role", authResp)
	}

	// 4. Rejections: wrong audience, wrong issuer, expired, unverified human email, wrong hosted domain.
	badCases := map[string]string{
		"wrong audience":        signOAuthIDToken(t, priv, "other-client.apps.googleusercontent.com", "alice@example.com", "example.com", "", boolPtr(true), time.Now().Add(5*time.Minute)),
		"wrong issuer":          signOAuthIDToken(t, priv, testOAuthWebClientID, "alice@example.com", "example.com", "https://evil.example.com", boolPtr(true), time.Now().Add(5*time.Minute)),
		"expired":               signOAuthIDToken(t, priv, testOAuthWebClientID, "alice@example.com", "example.com", "", boolPtr(true), time.Now().Add(-5*time.Minute)),
		"email_verified=false":  signOAuthIDToken(t, priv, testOAuthWebClientID, "alice@example.com", "example.com", "", boolPtr(false), time.Now().Add(5*time.Minute)),
		"missing verified flag": signOAuthIDToken(t, priv, testOAuthWebClientID, "alice@example.com", "example.com", "", nil, time.Now().Add(5*time.Minute)),
		"wrong hosted domain":   signOAuthIDToken(t, priv, testOAuthWebClientID, "alice@example.com", "other.com", "", boolPtr(true), time.Now().Add(5*time.Minute)),
	}
	for name, tok := range badCases {
		rec := serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/tasks", "", map[string]string{
			"Authorization": "Bearer " + tok,
		})
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: got %d, want 401: %s", name, rec.Code, rec.Body)
		}
	}

	// 5. Non-JWT Bearer tokens (access code) are ignored by oauthVerifier so the access-code gate still handles them.
	rec = serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/tasks", "", map[string]string{
		"Authorization": "Bearer " + testAccessCode,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("access code Bearer alongside OAuth config: got %d, want 200: %s", rec.Code, rec.Body)
	}
}

func TestBrowserOAuthLoginCallbackSessionCSRFAndLogout(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)

	priv, jwksURL := setupOAuthJWKSServer(t)

	var (
		issuedEmail    = "alice@example.com"
		gotVerifier    string
		expectedCode   = "auth-code-xyz"
		oauthTokenSrv  *httptest.Server
	)
	oauthTokenSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if r.FormValue("code") != expectedCode {
			http.Error(w, "bad code", http.StatusBadRequest)
			return
		}
		gotVerifier = r.FormValue("code_verifier")
		idTok := signOAuthIDToken(t, priv, testOAuthWebClientID, issuedEmail, "example.com", "", boolPtr(true), time.Now().Add(time.Hour))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "test-access-token",
			"id_token":     idTok,
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	}))
	defer oauthTokenSrv.Close()

	srv, client := testServer(t)
	client.Config.OAuthClientID = testOAuthWebClientID
	client.Config.OAuthClientSecret = "web-secret"
	client.Config.OAuthCLIClientID = testOAuthCLIClientID
	client.Config.OAuthKeysURL = jwksURL
	client.Config.OAuthAuthURL = "https://accounts.google.com/o/oauth2/v2/auth"
	client.Config.OAuthTokenURL = oauthTokenSrv.URL
	client.Config.OAuthHostedDomains = "example.com"
	client.Config.RequireOAuth = true
	client.Config.AllowedPrincipals = []string{"user:alice@example.com=developer"}
	ui.SetAccessCodeForTest(srv, testAccessCode)

	// 1. CLI discovery endpoint is open and returns the OAuth configuration.
	recDisc := serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/auth/oauth/cli-config", "", nil)
	if recDisc.Code != http.StatusOK {
		t.Fatalf("GET /api/auth/oauth/cli-config: got %d, want 200: %s", recDisc.Code, recDisc.Body)
	}
	var disc map[string]any
	_ = json.Unmarshal(recDisc.Body.Bytes(), &disc)
	if disc["enabled"] != true || disc["cliClientId"] != testOAuthCLIClientID {
		t.Fatalf("cli-config = %+v", disc)
	}

	// 2. With RequireOAuth=true, GET / from the network renders the OAuth sign-in page, and access code over network is refused.
	recRoot := serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/", "", map[string]string{"Accept": "text/html"})
	if recRoot.Code != http.StatusUnauthorized || !strings.Contains(recRoot.Body.String(), "/api/auth/oauth/login") {
		t.Fatalf("GET / under RequireOAuth: got %d, want 401 with /api/auth/oauth/login: %s", recRoot.Code, recRoot.Body)
	}
	recNetCode := serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/tasks", "", map[string]string{
		"Authorization": "Bearer " + testAccessCode,
	})
	if recNetCode.Code != http.StatusUnauthorized {
		t.Fatalf("access code over network under RequireOAuth: got %d, want 401", recNetCode.Code)
	}
	// Unforwarded loopback break-glass still works!
	recLoopback := serveFrom(srv, "127.0.0.1:4242", http.MethodGet, "/api/tasks", "", map[string]string{
		"Authorization": "Bearer " + testAccessCode,
	})
	if recLoopback.Code != http.StatusOK {
		t.Fatalf("unforwarded loopback break-glass under RequireOAuth: got %d, want 200: %s", recLoopback.Code, recLoopback.Body)
	}

	// 3. GET /api/auth/oauth/login redirects to Google with PKCE S256 and sets grain_oauth_state cookie.
	recLogin := serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/auth/oauth/login?next=/boards", "", nil)
	if recLogin.Code != http.StatusFound {
		t.Fatalf("GET /api/auth/oauth/login: got %d, want 302: %s", recLogin.Code, recLogin.Body)
	}
	loc, err := url.Parse(recLogin.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	state := loc.Query().Get("state")
	if state == "" || loc.Query().Get("code_challenge_method") != "S256" || loc.Query().Get("hd") != "example.com" {
		t.Fatalf("redirect URL missing state/PKCE/hd: %s", loc)
	}
	var stateCookie *http.Cookie
	for _, c := range recLogin.Result().Cookies() {
		if c.Name == ui.OAuthStateCookieName {
			stateCookie = c
		}
	}
	if stateCookie == nil || stateCookie.Value == "" {
		t.Fatalf("missing %s cookie", ui.OAuthStateCookieName)
	}

	// 4. GET /api/auth/oauth/callback exchanges code, verifies ID token & allowlist, and sets grain_oauth_session cookie.
	cbURL := "/api/auth/oauth/callback?code=" + url.QueryEscape(expectedCode) + "&state=" + url.QueryEscape(state)
	recCB := serveFrom(srv, "203.0.113.10:443", http.MethodGet, cbURL, "", map[string]string{
		"Cookie": stateCookie.Name + "=" + stateCookie.Value,
	})
	if (recCB.Code != http.StatusFound && recCB.Code != http.StatusSeeOther) || recCB.Header().Get("Location") != "/boards" {
		t.Fatalf("GET /api/auth/oauth/callback: got %d (Location %q), want 302/303 /boards: %s", recCB.Code, recCB.Header().Get("Location"), recCB.Body)
	}
	if gotVerifier == "" {
		t.Fatalf("expected PKCE code_verifier to be sent to token endpoint")
	}
	var sessionCookie *http.Cookie
	for _, c := range recCB.Result().Cookies() {
		if c.Name == ui.OAuthSessionCookieName && c.Value != "" {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatalf("expected %s cookie from callback", ui.OAuthSessionCookieName)
	}

	// 5. Authenticated requests using grain_oauth_session cookie succeed and enforce CSRF on mutations.
	recUser := serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/auth/user", "", map[string]string{
		"Cookie": sessionCookie.Name + "=" + sessionCookie.Value,
	})
	if recUser.Code != http.StatusOK {
		t.Fatalf("GET /api/auth/user with session cookie: got %d, want 200: %s", recUser.Code, recUser.Body)
	}
	var uResp ui.AuthUserResponse
	_ = json.Unmarshal(recUser.Body.Bytes(), &uResp)
	if !uResp.Authenticated || uResp.User != "alice@example.com" || uResp.AuthMethod != ui.AuthMethodOAuth {
		t.Fatalf("/api/auth/user with session cookie = %+v", uResp)
	}

	// Cross-site POST with grain_oauth_session cookie is blocked by CSRF middleware (403).
	recCSRF := serveFrom(srv, "203.0.113.10:443", http.MethodPost, "/api/tasks", `{"title":"csrf","noRepo":true}`, map[string]string{
		"Cookie":         sessionCookie.Name + "=" + sessionCookie.Value,
		"Content-Type":   "application/json",
		"Sec-Fetch-Site": "cross-site",
	})
	if recCSRF.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST with grain_oauth_session: got %d, want 403", recCSRF.Code)
	}

	// Same-origin POST with grain_oauth_session cookie succeeds (201).
	recPost := serveFrom(srv, "203.0.113.10:443", http.MethodPost, "/api/tasks", `{"title":"oauth browser task","noRepo":true}`, map[string]string{
		"Cookie":         sessionCookie.Name + "=" + sessionCookie.Value,
		"Content-Type":   "application/json",
		"Sec-Fetch-Site": "same-origin",
	})
	if recPost.Code != http.StatusCreated {
		t.Fatalf("same-origin POST with grain_oauth_session: got %d, want 201: %s", recPost.Code, recPost.Body)
	}

	// 6. Unallowed user completing OAuth callback gets 403 and no session cookie.
	issuedEmail = "intruder@example.com"
	recCBForbidden := serveFrom(srv, "203.0.113.10:443", http.MethodGet, cbURL, "", map[string]string{
		"Cookie": stateCookie.Name + "=" + stateCookie.Value,
	})
	if recCBForbidden.Code != http.StatusForbidden || !strings.Contains(recCBForbidden.Body.String(), "intruder@example.com") {
		t.Fatalf("callback for unallowed user: got %d, want 403 naming intruder@example.com: %s", recCBForbidden.Code, recCBForbidden.Body)
	}

	// 7. POST /api/auth/logout clears grain_oauth_session.
	recLogout := serveFrom(srv, "203.0.113.10:443", http.MethodPost, "/api/auth/logout", "", map[string]string{
		"Cookie":         sessionCookie.Name + "=" + sessionCookie.Value,
		"Sec-Fetch-Site": "same-origin",
	})
	if recLogout.Code != http.StatusOK {
		t.Fatalf("POST /api/auth/logout: got %d, want 200", recLogout.Code)
	}
	cleared := false
	for _, c := range recLogout.Result().Cookies() {
		if c.Name == ui.OAuthSessionCookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatalf("expected POST /api/auth/logout to clear %s cookie", ui.OAuthSessionCookieName)
	}
}

func TestConfigMapRBACRolesAndRepoScopedAuthorization(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)

	priv, jwksURL := setupOAuthJWKSServer(t)

	var groupCalls atomic.Int32
	srv, client := testServer(t)
	client.Config.OAuthClientID = testOAuthWebClientID
	client.Config.OAuthKeysURL = jwksURL
	// Multi-line ConfigMap format with comments, built-in roles, custom scopes, and @owner/repo restrictions.
	rawConfigMapPrincipals := `
# Platform admins
user:admin@example.com=admin

# Repository maintainers
user:maint@example.com=maintainer

# Developers (via group)
group:devs@example.com=developer

# Contractor scoped to acme/widgets only
user:contractor@example.com=developer@acme/widgets

# CI trigger service account
serviceAccount:ci-runner@proj.iam.gserviceaccount.com=ci:write+tasks:read@acme/widgets

# Whole domain gets viewer
domain:example.com=viewer
`
	client.Config.AllowedPrincipals = ui.SplitAllowedPrincipals(rawConfigMapPrincipals)
	client.Config.GroupChecker = groupCheckerFunc(func(ctx context.Context, groupEmail, memberEmail string) (bool, error) {
		groupCalls.Add(1)
		if groupEmail == "devs@example.com" && memberEmail == "dev@example.com" {
			return true, nil
		}
		return false, nil
	})
	ui.SetAccessCodeForTest(srv, testAccessCode)

	bearer := func(email string) map[string]string {
		hd := "example.com"
		var verified *bool = boolPtr(true)
		if strings.HasSuffix(email, ".gserviceaccount.com") {
			hd = ""
			verified = nil
		}
		tok := signOAuthIDToken(t, priv, testOAuthWebClientID, email, hd, "", verified, time.Now().Add(time.Hour))
		return map[string]string{
			"Authorization": "Bearer " + tok,
			"Content-Type":  "application/json",
		}
	}

	// 1. Direct admin entry short-circuits without calling Cloud Identity GroupChecker and can access admin routes.
	beforeCalls := groupCalls.Load()
	rec := serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/settings", "", bearer("admin@example.com"))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin GET /api/settings: got %d, want 200: %s", rec.Code, rec.Body)
	}
	if groupCalls.Load() != beforeCalls {
		t.Fatalf("expected direct admin match not to call GroupChecker, called %d times", groupCalls.Load()-beforeCalls)
	}

	// 2. Viewer (matched via domain:example.com=viewer):
	// Can GET /api/tasks and GET /api/metrics, but cannot POST /api/tasks (403) or PUT /api/settings (403).
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/tasks", "", bearer("viewer@example.com")); rec.Code != http.StatusOK {
		t.Fatalf("viewer GET /api/tasks: got %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/metrics", "", bearer("viewer@example.com")); rec.Code != http.StatusOK {
		t.Fatalf("viewer GET /api/metrics: got %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodPost, "/api/tasks", `{"title":"viewer write","noRepo":true}`, bearer("viewer@example.com")); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer POST /api/tasks: got %d, want 403: %s", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodPut, "/api/settings", `{}`, bearer("viewer@example.com")); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer PUT /api/settings: got %d, want 403: %s", rec.Code, rec.Body)
	}

	// 3. Developer (matched via group:devs@example.com=developer + domain:example.com=viewer):
	// Can create, comment on, and approve tasks, but cannot pause deployment or edit settings.
	recCreate := serveFrom(srv, "203.0.113.10:443", http.MethodPost, "/api/tasks", `{"title":"dev task","repo":"acme/widgets"}`, bearer("dev@example.com"))
	if recCreate.Code != http.StatusCreated {
		t.Fatalf("developer POST /api/tasks: got %d, want 201: %s", recCreate.Code, recCreate.Body)
	}
	var createdTask model.Task
	_ = json.Unmarshal(recCreate.Body.Bytes(), &createdTask)

	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodPost, "/api/tasks/"+createdTask.ID+"/comments", `{"body":"dev comment"}`, bearer("dev@example.com")); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("developer POST comment: got %d, want 200/201: %s", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodPost, "/api/repos", `{"repo":"acme/widgets"}`, bearer("dev@example.com")); rec.Code != http.StatusForbidden {
		t.Fatalf("developer POST /api/repos: got %d, want 403: %s", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodDelete, "/api/pause", "", bearer("dev@example.com")); rec.Code != http.StatusForbidden {
		t.Fatalf("developer DELETE /api/pause: got %d, want 403: %s", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodPut, "/api/settings", `{}`, bearer("dev@example.com")); rec.Code != http.StatusForbidden {
		t.Fatalf("developer PUT /api/settings: got %d, want 403: %s", rec.Code, rec.Body)
	}

	// 4. Maintainer (user:maint@example.com=maintainer):
	// Can manage repositories (POST /api/repos), but cannot edit global settings (PUT /api/settings -> 403).
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodPost, "/api/repos", `{"repo":"acme/widgets"}`, bearer("maint@example.com")); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("maintainer POST /api/repos: got %d, want 200/201: %s", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodPut, "/api/settings", `{}`, bearer("maint@example.com")); rec.Code != http.StatusForbidden {
		t.Fatalf("maintainer PUT /api/settings: got %d, want 403: %s", rec.Code, rec.Body)
	}

	// 5. Repo-scoped developer (user:contractor@example.com=developer@acme/widgets):
	//Note: contractor@other.org has only developer@acme/widgets (without domain:example.com=viewer).
	client.Config.AllowedPrincipals = append(client.Config.AllowedPrincipals, "user:contractor@other.org=developer@acme/widgets")
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodPost, "/api/tasks", `{"title":"allowed repo","repo":"acme/widgets"}`, bearer("contractor@other.org")); rec.Code != http.StatusCreated {
		t.Fatalf("repo-scoped developer POST /api/tasks in acme/widgets: got %d, want 201: %s", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodPost, "/api/tasks", `{"title":"forbidden repo","repo":"acme/other"}`, bearer("contractor@other.org")); rec.Code != http.StatusForbidden {
		t.Fatalf("repo-scoped developer POST /api/tasks in acme/other: got %d, want 403: %s", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/tasks?repo=acme/widgets", "", bearer("contractor@other.org")); rec.Code != http.StatusOK {
		t.Fatalf("repo-scoped developer GET /api/tasks?repo=acme/widgets: got %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/tasks", "", bearer("contractor@other.org")); rec.Code != http.StatusOK {
		t.Fatalf("repo-scoped developer auto-defaulted GET /api/tasks: got %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, "203.0.113.10:443", http.MethodGet, "/api/tasks?repo=acme/other", "", bearer("contractor@other.org")); rec.Code != http.StatusForbidden {
		t.Fatalf("repo-scoped developer GET /api/tasks?repo=acme/other: got %d, want 403: %s", rec.Code, rec.Body)
	}
}

func TestHTTPClientStoredOAuthCredentialsAndAutoRefresh(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)

	priv, jwksURL := setupOAuthJWKSServer(t)

	var refreshCalls atomic.Int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if r.FormValue("grant_type") != "refresh_token" || r.FormValue("refresh_token") != "refresh-secret-1" {
			http.Error(w, "invalid refresh request", http.StatusBadRequest)
			return
		}
		refreshCalls.Add(1)
		freshIDTok := signOAuthIDToken(t, priv, testOAuthCLIClientID, "alice@example.com", "example.com", "", boolPtr(true), time.Now().Add(time.Hour))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "test-refreshed-access-token",
			"id_token":     freshIDTok,
			"expires_in":   3600,
		})
	}))
	defer tokenSrv.Close()

	srv, client := testServer(t)
	client.Config.OAuthCLIClientID = testOAuthCLIClientID
	client.Config.OAuthKeysURL = jwksURL
	client.Config.RequireOAuth = true
	client.Config.AllowedPrincipals = []string{"user:alice@example.com=admin"}
	ui.SetAccessCodeForTest(srv, testAccessCode)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	credFile := filepath.Join(t.TempDir(), "oauth-credentials.json")
	t.Setenv(ui.OAuthCredentialsFileEnv, credFile)
	t.Setenv(ui.LocalAPITokenFileEnv, "")
	t.Setenv("GRAIN_DATA_DIR", "")

	// Seed an expired ID token + valid refresh token.
	expiredIDTok := signOAuthIDToken(t, priv, testOAuthCLIClientID, "alice@example.com", "example.com", "", boolPtr(true), time.Now().Add(-10*time.Minute))
	if err := ui.SaveOAuthCredential(credFile, ui.OAuthStoredCredential{
		ServerURL:    ts.URL,
		ClientID:     testOAuthCLIClientID,
		TokenURL:     tokenSrv.URL,
		RefreshToken: "refresh-secret-1",
		IDToken:      expiredIDTok,
		Expiry:       time.Now().Add(-10 * time.Minute),
		Email:        "alice@example.com",
	}); err != nil {
		t.Fatal(err)
	}

	hc := ui.NewHTTPClient(ts.URL)
	tasks, err := hc.ListTasks(context.Background())
	if err != nil {
		t.Fatalf("ListTasks with auto-refreshed OAuth credential: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected 0 tasks, got %d", len(tasks))
	}
	if refreshCalls.Load() != 1 {
		t.Fatalf("expected 1 token refresh call, got %d", refreshCalls.Load())
	}

	// Verify the refreshed ID token was persisted to disk.
	stored, err := ui.LoadOAuthCredentials(credFile)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Servers[ts.URL].IDToken == expiredIDTok {
		t.Fatalf("expected refreshed id_token to be saved to %s", credFile)
	}
}

func TestValidateAllowedPrincipalsAcceptsRolesAndRejectsUnknownRoles(t *testing.T) {
	valid := []string{
		"user:alice@example.com=admin",
		"group:eng@example.com=developer",
		"user:contractor@example.com=developer@acme/widgets",
		"serviceAccount:ci@proj.iam.gserviceaccount.com=ci:write+tasks:read@acme/widgets",
		"domain:example.com=viewer",
	}
	entries, err := ui.ValidateAllowedPrincipals(valid, nil)
	if err != nil {
		t.Fatalf("ValidateAllowedPrincipals(valid): %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("expected 5 entries, got %d", len(entries))
	}

	if _, err := ui.ValidateAllowedPrincipals([]string{"user:alice@example.com=superadmin"}, nil); err == nil {
		t.Fatalf("expected unknown role 'superadmin' to be rejected")
	}
}
