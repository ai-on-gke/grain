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
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// requireIAPServer is a single-tenant server with IAP configured, the
// access code testAccessCode and -ui-require-iap set to requireIAP, plus
// a live access session cookie for that code (minted over loopback, the
// one place a require-IAP daemon still takes the code).
func requireIAPServer(t *testing.T, requireIAP bool) (srv *ui.Server, assertion, cookie string) {
	t.Helper()
	priv, pemStr := generateECDSATestKey(t)
	assertionKeys(t, priv, pemStr, false)
	ui.SetIAPAudience(tunnelAudience)
	srv, client := testServer(t)
	client.Config.AllowedPrincipals = []string{"user:iap@example.com"}
	client.Config.RequireIAP = requireIAP
	ui.SetAccessCodeForTest(srv, testAccessCode)
	assertion = signIAPToken(t, priv, tunnelKid, tunnelAudience, "iap@example.com", "x", ui.GoogleIAPIssuer, time.Now().Add(5*time.Minute))

	rec := serveFrom(srv, "127.0.0.1:5555", http.MethodPost, "/api/auth/access-code", `{"code":"`+testAccessCode+`"}`,
		map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusOK {
		t.Fatalf("loopback login: code %d: %s", rec.Code, rec.Body)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == ui.AccessCookieName {
			cookie = c.Name + "=" + c.Value
		}
	}
	if cookie == "" {
		t.Fatal("loopback login set no access cookie")
	}
	return srv, assertion, cookie
}

const networkAddr = "203.0.113.7:4444"

// Task 2318 (authentication.design.md, rollout step 4): with
// -ui-require-iap the network listener takes no access code, bearer or
// cookie, and serves no code form; loopback with no forwarding headers
// keeps the code as break-glass; a verified IAP assertion still gets in.
func TestRequireIAPRefusesTheAccessCodeOverTheNetwork(t *testing.T) {
	srv, assertion, cookie := requireIAPServer(t, true)
	bearer := "Bearer " + testAccessCode

	if rec := serveFrom(srv, networkAddr, http.MethodGet, "/api/tasks", "", map[string]string{"Authorization": bearer}); rec.Code != http.StatusUnauthorized ||
		!strings.Contains(rec.Body.String(), "-ui-require-iap") {
		t.Errorf("bearer code over the network: code %d, want 401 naming -ui-require-iap: %s", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, networkAddr, http.MethodGet, "/api/tasks", "", map[string]string{"Cookie": cookie}); rec.Code != http.StatusUnauthorized {
		t.Errorf("access cookie over the network: code %d, want 401: %s", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, "127.0.0.1:5555", http.MethodGet, "/api/tasks", "", map[string]string{"Authorization": bearer}); rec.Code != http.StatusOK {
		t.Errorf("bearer code over loopback, no forwarding headers: code %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, "[::1]:5555", http.MethodGet, "/api/tasks", "", map[string]string{"Cookie": cookie}); rec.Code != http.StatusOK {
		t.Errorf("access cookie over loopback, no forwarding headers: code %d, want 200: %s", rec.Code, rec.Body)
	}
	for _, h := range []string{"X-Forwarded-For", "Forwarded", "X-Real-Ip"} {
		rec := serveFrom(srv, "127.0.0.1:5555", http.MethodGet, "/api/tasks", "", map[string]string{"Authorization": bearer, h: "198.51.100.9"})
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("bearer code over loopback with %s: code %d, want 401: %s", h, rec.Code, rec.Body)
		}
	}
	if rec := serveFrom(srv, "127.0.0.1:5555", http.MethodGet, "/api/tasks", "", map[string]string{"Cookie": cookie, "X-Forwarded-For": "198.51.100.9"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("access cookie over loopback with X-Forwarded-For: code %d, want 401: %s", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, networkAddr, http.MethodGet, "/api/tasks", "", map[string]string{ui.IAPAssertionHeader: assertion}); rec.Code != http.StatusOK {
		t.Errorf("valid IAP assertion over the network: code %d, want 200: %s", rec.Code, rec.Body)
	}

	// The login page is gone: a browser gets the IAP refusal, and the
	// form's own endpoint mints no cookie.
	rec := serveFrom(srv, networkAddr, http.MethodGet, "/", "", nil)
	if rec.Code != http.StatusUnauthorized || strings.Contains(rec.Body.String(), "Enter access code") || strings.Contains(rec.Body.String(), "<form") {
		t.Errorf("GET / over the network: code %d, want 401 without the access code form: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "IAP") {
		t.Errorf("GET / over the network should point at IAP: %s", rec.Body)
	}
	rec = serveFrom(srv, networkAddr, http.MethodPost, "/api/auth/access-code", "code="+testAccessCode,
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "Enter access code") {
		t.Errorf("POST login form over the network: code %d, want 403 without the form: %s", rec.Code, rec.Body)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == ui.AccessCookieName && c.MaxAge >= 0 {
			t.Errorf("POST login form over the network set %s", c.Name)
		}
	}

	// GET /api/auth/access-code tells `grain access-code` the server
	// requires IAP.
	rec = serveFrom(srv, "127.0.0.1:5555", http.MethodGet, "/api/auth/access-code", "", nil)
	var status struct {
		RequireIAP         bool `json:"requireIAP"`
		AccessCodeAccepted bool `json:"accessCodeAccepted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil || !status.RequireIAP || !status.AccessCodeAccepted {
		t.Errorf("GET /api/auth/access-code over loopback = %s (%v), want requireIAP and accessCodeAccepted", rec.Body, err)
	}
	rec = serveFrom(srv, networkAddr, http.MethodGet, "/api/auth/access-code", "", nil)
	status.AccessCodeAccepted = true
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil || !status.RequireIAP || status.AccessCodeAccepted {
		t.Errorf("GET /api/auth/access-code over the network = %s (%v), want requireIAP and not accessCodeAccepted", rec.Body, err)
	}

	// Executor bootstrap routes stay open.
	if rec := serveFrom(srv, networkAddr, http.MethodGet, "/install-executor.sh", "", nil); rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
		t.Errorf("GET /install-executor.sh over the network: code %d, want it open", rec.Code)
	}
}

// -ui-require-iap governs IAP only: a wrapper's verifier that declares
// AccessCodeWins keeps the code as break-glass over its own proxy.
func TestRequireIAPKeepsTheCodeForAVerifierWhoseCodeWins(t *testing.T) {
	srv, _, cookie := requireIAPServer(t, true)
	v := &fakePrecedenceVerifier{&fakeHeaderVerifier{
		name:           "corp-proxy",
		headers:        []string{"X-Corp-Proxy"},
		accessCodeWins: true,
		verifyFn:       parseSimpleSignedHeader,
	}}
	t.Cleanup(ui.RegisterIdentityVerifier(v))

	proxied := map[string]string{"X-Corp-Proxy": "ok:stranger@example.com", "X-Forwarded-For": "198.51.100.9"}
	withCode := map[string]string{"Authorization": "Bearer " + testAccessCode}
	for k, val := range proxied {
		withCode[k] = val
	}
	if rec := serveFrom(srv, networkAddr, http.MethodGet, "/api/tasks", "", withCode); rec.Code != http.StatusOK {
		t.Errorf("bearer code through a code-wins verifier's proxy: code %d, want 200: %s", rec.Code, rec.Body)
	}
	withCookie := map[string]string{"Cookie": cookie}
	for k, val := range proxied {
		withCookie[k] = val
	}
	if rec := serveFrom(srv, networkAddr, http.MethodGet, "/api/tasks", "", withCookie); rec.Code != http.StatusOK {
		t.Errorf("access cookie through a code-wins verifier's proxy: code %d, want 200: %s", rec.Code, rec.Body)
	}
	// Without the code that proxy's caller is judged by the allowlist.
	if rec := serveFrom(srv, networkAddr, http.MethodGet, "/api/tasks", "", proxied); rec.Code != http.StatusForbidden {
		t.Errorf("unlisted caller through a code-wins verifier's proxy: code %d, want 403: %s", rec.Code, rec.Body)
	}
}

// Without -ui-require-iap, IAP configured is the migration state: the
// assertion and the code are both accepted from anywhere, and the code
// form is served.
func TestWithoutRequireIAPTheCodeStillWorksOverTheNetwork(t *testing.T) {
	srv, assertion, cookie := requireIAPServer(t, false)
	if rec := serveFrom(srv, networkAddr, http.MethodGet, "/api/tasks", "", map[string]string{"Authorization": "Bearer " + testAccessCode}); rec.Code != http.StatusOK {
		t.Errorf("bearer code over the network: code %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, networkAddr, http.MethodGet, "/api/tasks", "", map[string]string{"Cookie": cookie, "X-Forwarded-For": "198.51.100.9"}); rec.Code != http.StatusOK {
		t.Errorf("forwarded access cookie: code %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, networkAddr, http.MethodGet, "/api/tasks", "", map[string]string{ui.IAPAssertionHeader: assertion}); rec.Code != http.StatusOK {
		t.Errorf("valid IAP assertion: code %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, networkAddr, http.MethodGet, "/", "", nil); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "Enter access code") {
		t.Errorf("GET / without credentials: code %d, want 401 with the access code form: %s", rec.Code, rec.Body)
	}
	rec := serveFrom(srv, networkAddr, http.MethodGet, "/api/auth/access-code", "", nil)
	if strings.Contains(rec.Body.String(), "requireIAP") {
		t.Errorf("GET /api/auth/access-code without -ui-require-iap mentions requireIAP: %s", rec.Body)
	}
}
