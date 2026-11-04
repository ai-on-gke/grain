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
	"fmt"
	"net/http"
	"strings"
	"testing"

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
