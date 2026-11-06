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
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// A browser whose signed identity header does not verify (an expired IAP
// assertion, a rotated key) is shown a page saying so when it loads the
// UI, still with a 401 and with the access code not consulted; API
// callers keep the 401 JSON. Found by tests/iap/walkthrough.sh, where a
// browser navigation got the API's raw JSON error.
func TestUnverifiedAssertionGetsAPageForABrowserAndJSONForTheAPI(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	unreg := ui.RegisterIdentityVerifier(&fakeHeaderVerifier{
		name:     "corp-proxy",
		headers:  []string{"X-Corp-Proxy-Assertion"},
		verifyFn: parseSimpleSignedHeader,
	})
	t.Cleanup(unreg)

	srv, _ := testServer(t)
	ui.SetAccessCodeForTest(srv, testAccessCode)
	bad := map[string]string{"X-Corp-Proxy-Assertion": "bad:mallory@example.com"}
	withCode := map[string]string{"X-Corp-Proxy-Assertion": "bad:mallory@example.com", "Authorization": "Bearer " + testAccessCode}

	for _, h := range []map[string]string{bad, withCode} {
		rec := serveFrom(srv, "198.51.100.8:1234", http.MethodGet, "/", "", h)
		body := rec.Body.String()
		if rec.Code != http.StatusUnauthorized || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("browser GET / (%d headers): got %d %q, want a 401 HTML page: %s", len(h), rec.Code, rec.Header().Get("Content-Type"), body)
		}
		for _, want := range []string{"Sign-in could not be verified", "signature verification failed", "-ui-iap-keys-url"} {
			if !strings.Contains(body, want) {
				t.Errorf("assertion-refused page lacks %q:\n%s", want, body)
			}
		}
	}

	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/tasks"},
		{http.MethodPost, "/"},
	} {
		rec := serveFrom(srv, "198.51.100.8:1234", c.method, c.path, "", bad)
		if rec.Code != http.StatusUnauthorized || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") ||
			!strings.Contains(rec.Body.String(), "identity assertion was not accepted: ") {
			t.Fatalf("%s %s: got %d %q, want the 401 JSON: %s", c.method, c.path, rec.Code, rec.Header().Get("Content-Type"), rec.Body)
		}
	}
}

// verifierNotReady is the error a verifier whose backend is not reachable
// yet (just after a daemon restart) returns: ui.VerifierUnavailableError.
type verifierNotReady struct{}

func (verifierNotReady) Error() string     { return "identity backend is not reachable yet" }
func (verifierNotReady) Unavailable() bool { return true }

// A verifier that could not check the header yet refuses the request just
// as a bad signature does -- the access code does not get it in -- but
// with 503 and Retry-After, and a page that says to reload rather than
// advice about audiences and clocks.
func TestUnavailableVerifierGets503StartingUpPageAndIsNotAdmitted(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	unreg := ui.RegisterIdentityVerifier(&fakeHeaderVerifier{
		name:    "corp-proxy",
		headers: []string{"X-Corp-Proxy-Assertion"},
		verifyFn: func(r *http.Request, raw string) (ui.VerifiedIdentity, error) {
			if raw == "unavailable" {
				return ui.VerifiedIdentity{}, fmt.Errorf("checking the assertion: %w", verifierNotReady{})
			}
			return parseSimpleSignedHeader(r, raw)
		},
	})
	t.Cleanup(unreg)
	var _ ui.VerifierUnavailableError = verifierNotReady{}

	srv, _ := testServer(t)
	ui.SetAccessCodeForTest(srv, testAccessCode)
	unavail := map[string]string{"X-Corp-Proxy-Assertion": "unavailable"}
	withCode := map[string]string{"X-Corp-Proxy-Assertion": "unavailable", "Authorization": "Bearer " + testAccessCode}

	for _, h := range []map[string]string{unavail, withCode} {
		rec := serveFrom(srv, "198.51.100.8:1234", http.MethodGet, "/", "", h)
		body := rec.Body.String()
		if rec.Code != http.StatusServiceUnavailable || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("browser GET / (%d headers): got %d %q, want a 503 HTML page: %s", len(h), rec.Code, rec.Header().Get("Content-Type"), body)
		}
		if got := rec.Header().Get("Retry-After"); got != "5" {
			t.Errorf("browser GET / Retry-After = %q, want 5", got)
		}
		for _, want := range []string{"sign-in verification is starting up", "Reload the page in a few seconds", "not reachable yet"} {
			if !strings.Contains(body, want) {
				t.Errorf("starting-up page lacks %q:\n%s", want, body)
			}
		}
		for _, unwanted := range []string{"-ui-iap-audience", "clock", "Sign-in could not be verified"} {
			if strings.Contains(body, unwanted) {
				t.Errorf("starting-up page has %q, the advice for a bad assertion:\n%s", unwanted, body)
			}
		}
	}

	for _, h := range []map[string]string{unavail, withCode} {
		rec := serveFrom(srv, "198.51.100.8:1234", http.MethodGet, "/api/tasks", "", h)
		if rec.Code != http.StatusServiceUnavailable || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") ||
			rec.Header().Get("Retry-After") != "5" {
			t.Fatalf("GET /api/tasks (%d headers): got %d %q Retry-After %q, want the 503 JSON: %s",
				len(h), rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Retry-After"), rec.Body)
		}
	}

	// A plain verification failure is still an immediate 401.
	rec := serveFrom(srv, "198.51.100.8:1234", http.MethodGet, "/api/tasks", "", map[string]string{"X-Corp-Proxy-Assertion": "bad:mallory@example.com"})
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("Retry-After") != "" {
		t.Fatalf("bad assertion: got %d Retry-After %q, want 401 with none", rec.Code, rec.Header().Get("Retry-After"))
	}
}

// End-to-end regression for the built-in IAP verifier (defaultIAPVerifier ->
// webauth.IAPVerifier -> jwks.Fetch): when -ui-iap-keys-url is unreachable or
// returning 503 on startup, API callers get 503 + Retry-After: 5, browsers get
// the starting-up page, the access code does not bypass the unverified header,
// and logRefusedAssertion records an "unavailable" warning without burning the
// "unverifiable" slot needed to log a subsequent unknown-kid assertion.
func TestBuiltInIAPVerifierReturns503WhenJWKSDownAndLogsUnknownKidAfterRecovery(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)

	priv, pemStr := generateECDSATestKey(t)
	var jwksUp atomic.Bool
	jwksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !jwksUp.Load() {
			http.Error(w, "upstream key server starting", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{tunnelKid: pemStr})
	}))
	t.Cleanup(jwksSrv.Close)
	ui.SetIAPHTTPClient(jwksSrv.Client())

	const aud = "/projects/987654/global/backendServices/321"
	const iss = "https://iap-down-test.example"

	logBuf := ui.LogBufferForTest(t)

	store, _ := sqlitetest.Open(t)
	client := ui.NewClient(ui.Config{
		Actor:             ui.DefaultActor("owner@example.com"),
		LocalAPIToken:     testAccessCode,
		IAPAudience:       aud,
		IAPIssuer:         iss,
		IAPKeysURL:        jwksSrv.URL,
		AllowedPrincipals: []string{"user:alice@example.com"},
	}, store)
	if err := client.MigrateAllowedPrincipals(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv := ui.NewServerWithClient(client)

	aliceTok := signIAPToken(t, priv, tunnelKid, aud, "alice@example.com", "accounts.google.com:111", iss, time.Now().Add(10*time.Minute))
	unknownKidTok := signIAPToken(t, priv, "unknown-kid-99", aud, "mallory@example.com", "accounts.google.com:999", iss, time.Now().Add(10*time.Minute))

	// 1. JWKS returns 503 -> both API and browser return 503 + Retry-After: 5, even with access code.
	for _, h := range []map[string]string{
		{ui.IAPAssertionHeader: aliceTok},
		{ui.IAPAssertionHeader: aliceTok, "Authorization": "Bearer " + testAccessCode},
	} {
		apiRec := serveFrom(srv, "198.51.100.8:1234", http.MethodGet, "/api/tasks", "", h)
		if apiRec.Code != http.StatusServiceUnavailable || apiRec.Header().Get("Retry-After") != "5" {
			t.Fatalf("API while JWKS 503: got %d Retry-After %q, want 503 Retry-After 5: %s",
				apiRec.Code, apiRec.Header().Get("Retry-After"), apiRec.Body.String())
		}
		htmlRec := serveFrom(srv, "198.51.100.8:1234", http.MethodGet, "/", "", h)
		if htmlRec.Code != http.StatusServiceUnavailable || htmlRec.Header().Get("Retry-After") != "5" ||
			!strings.Contains(htmlRec.Body.String(), "Sign-in verification is starting up") {
			t.Fatalf("Browser while JWKS 503: got %d Retry-After %q:\n%s",
				htmlRec.Code, htmlRec.Header().Get("Retry-After"), htmlRec.Body.String())
		}
	}

	firstLogs := logBuf.String()
	if !strings.Contains(firstLogs, "could not verify X-Goog-IAP-JWT-Assertion yet") {
		t.Errorf("expected unavailable diagnostic in logs, got:\n%s", firstLogs)
	}
	if strings.Contains(firstLogs, "not a configuration mismatch") {
		t.Errorf("transient JWKS 503 logged clock-skew/bad-signature hint:\n%s", firstLogs)
	}

	// 2. JWKS recovers -> alice is admitted (200), and unknown-kid returns 401 AND logs its refusal.
	jwksUp.Store(true)
	okRec := serveFrom(srv, "198.51.100.8:1234", http.MethodGet, "/api/auth/whoami", "", map[string]string{ui.IAPAssertionHeader: aliceTok})
	if okRec.Code != http.StatusOK {
		t.Fatalf("alice after JWKS recovery: got %d: %s", okRec.Code, okRec.Body.String())
	}

	beforeUnknown := len(logBuf.String())
	badRec := serveFrom(srv, "198.51.100.8:1234", http.MethodGet, "/api/tasks", "", map[string]string{ui.IAPAssertionHeader: unknownKidTok})
	if badRec.Code != http.StatusUnauthorized || badRec.Header().Get("Retry-After") != "" {
		t.Fatalf("unknown-kid after JWKS recovery: got %d Retry-After %q, want 401", badRec.Code, badRec.Header().Get("Retry-After"))
	}
	if afterLogs := logBuf.String()[beforeUnknown:]; !strings.Contains(afterLogs, "not a configuration mismatch") {
		t.Errorf("unknown-kid refusal was suppressed after prior JWKS outage; logs:\n%s", afterLogs)
	}
}
