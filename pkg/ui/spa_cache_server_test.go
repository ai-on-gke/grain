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

func assertNoStoreNeverImmutable(t *testing.T, label string, recCode int, wantCode int, cc string) {
	t.Helper()
	if recCode != wantCode {
		t.Fatalf("%s: status = %d, want %d", label, recCode, wantCode)
	}
	if strings.Contains(cc, "immutable") || strings.Contains(cc, "max-age") {
		t.Fatalf("%s: Cache-Control = %q, error/refusal path must not be long-lived", label, cc)
	}
	if cc != "no-store" {
		t.Fatalf("%s: Cache-Control = %q, want no-store", label, cc)
	}
}

func TestRefusedAndErrorPathsCarryNoStoreNeverImmutable(t *testing.T) {
	// 1. No access code: static hashed-looking paths, routes, and API calls
	// all refuse with 401 and Cache-Control: no-store.
	srv, _ := testServer(t)
	ui.SetAccessCodeForTest(srv, testAccessCode)

	for _, target := range []string{
		"/",
		"/next/",
		"/next/inbox",
		"/next/main-UCKK7PSP.js",
		"/next/styles-P323WRHF.css",
		"/assets/index-co56kW-3.js",
		"/api/tasks",
	} {
		rec := serveFrom(srv, networkAddr, http.MethodGet, target, "", nil)
		assertNoStoreNeverImmutable(t, "no access code GET "+target, rec.Code, http.StatusUnauthorized, rec.Header().Get("Cache-Control"))
	}

	// 2. Authenticated missing asset & API error paths return 404/405 with no-store.
	authHdr := map[string]string{"Authorization": "Bearer " + testAccessCode}
	for _, tc := range []struct {
		method   string
		path     string
		wantCode int
	}{
		{http.MethodGet, "/next/chunk-ZZZZZZZZ.js", http.StatusNotFound},
		{http.MethodGet, "/next/chunk-abc.js.map", http.StatusNotFound},
		{http.MethodGet, "/assets/missing-12345678.js", http.StatusNotFound},
		{http.MethodGet, "/api/no-such-thing", http.StatusNotFound},
		{http.MethodDelete, "/api/tasks/1", http.StatusMethodNotAllowed},
	} {
		rec := serveFrom(srv, networkAddr, tc.method, tc.path, "", authHdr)
		assertNoStoreNeverImmutable(t, tc.method+" "+tc.path, rec.Code, tc.wantCode, rec.Header().Get("Cache-Control"))
	}
}

func TestVerifierRefusalPagesAndJSONCarryNoStore(t *testing.T) {
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

	srv, client := testServer(t)
	client.Config.AllowedPrincipals = []string{"user:alice@example.com"}
	ui.SetAccessCodeForTest(srv, testAccessCode)

	cases := []struct {
		name     string
		method   string
		path     string
		headers  map[string]string
		wantCode int
	}{
		{
			name:     "bad assertion HTML",
			method:   http.MethodGet,
			path:     "/next/main-UCKK7PSP.js",
			headers:  map[string]string{"X-Corp-Proxy-Assertion": "bad:mallory@example.com"},
			wantCode: http.StatusUnauthorized,
		},
		{
			name:     "bad assertion JSON",
			method:   http.MethodGet,
			path:     "/api/tasks",
			headers:  map[string]string{"X-Corp-Proxy-Assertion": "bad:mallory@example.com"},
			wantCode: http.StatusUnauthorized,
		},
		{
			name:     "disallowed principal HTML",
			method:   http.MethodGet,
			path:     "/next/inbox",
			headers:  map[string]string{"X-Corp-Proxy-Assertion": "ok:mallory@example.com"},
			wantCode: http.StatusForbidden,
		},
		{
			name:     "disallowed principal JSON",
			method:   http.MethodGet,
			path:     "/api/tasks",
			headers:  map[string]string{"X-Corp-Proxy-Assertion": "ok:mallory@example.com"},
			wantCode: http.StatusForbidden,
		},
		{
			name:     "unavailable verifier HTML",
			method:   http.MethodGet,
			path:     "/next/main-UCKK7PSP.js",
			headers:  map[string]string{"X-Corp-Proxy-Assertion": "unavailable"},
			wantCode: http.StatusServiceUnavailable,
		},
		{
			name:     "unavailable verifier JSON",
			method:   http.MethodGet,
			path:     "/api/tasks",
			headers:  map[string]string{"X-Corp-Proxy-Assertion": "unavailable"},
			wantCode: http.StatusServiceUnavailable,
		},
	}

	for _, tc := range cases {
		rec := serveFrom(srv, "198.51.100.8:1234", tc.method, tc.path, "", tc.headers)
		assertNoStoreNeverImmutable(t, tc.name, rec.Code, tc.wantCode, rec.Header().Get("Cache-Control"))
	}

	// Delegate not allowed (403 HTML and JSON).
	h := newDelegationHarness(t)
	const (
		alice   = "alice@example.com"
		slackSA = "slack-bot@my-proj.iam.gserviceaccount.com"
	)
	if _, err := h.client.AllowPrincipal(r503Ctx(t), ui.AllowPrincipalRequest{
		Entry:  "user:" + alice,
		Role:   "grain/operator",
		Reason: "owner",
	}); err != nil {
		t.Fatal(err)
	}
	tok := h.mintDelegated(t, alice, slackSA, "slack-bot", "dg_01", "write")
	recDelegateAPI := h.do(http.MethodGet, "/api/tasks", "", map[string]string{ui.RegistryAssertionHeader: tok})
	assertNoStoreNeverImmutable(t, "delegate refused JSON", recDelegateAPI.Code, http.StatusForbidden, recDelegateAPI.Header().Get("Cache-Control"))

	recDelegateHTML := h.do(http.MethodGet, "/next/main-UCKK7PSP.js", "", map[string]string{ui.RegistryAssertionHeader: tok})
	assertNoStoreNeverImmutable(t, "delegate refused HTML", recDelegateHTML.Code, http.StatusForbidden, recDelegateHTML.Header().Get("Cache-Control"))

	// RequireIAP over network without assertion (401 HTML and JSON).
	iapSrv, _, _ := requireIAPServer(t, true)
	recIAPHTML := serveFrom(iapSrv, networkAddr, http.MethodGet, "/next/main-UCKK7PSP.js", "", nil)
	assertNoStoreNeverImmutable(t, "require-iap HTML", recIAPHTML.Code, http.StatusUnauthorized, recIAPHTML.Header().Get("Cache-Control"))

	recIAPAPI := serveFrom(iapSrv, networkAddr, http.MethodGet, "/api/tasks", "", nil)
	assertNoStoreNeverImmutable(t, "require-iap JSON", recIAPAPI.Code, http.StatusUnauthorized, recIAPAPI.Header().Get("Cache-Control"))
}

// TestServerStaticGzipETagRevalidation verifies that an authenticated browser
// sending Accept-Encoding: gzip gets a Cache-Control: no-cache response whose
// (possibly weak after gzip) ETag still earns 304 Not Modified on If-None-Match.
func TestServerStaticGzipETagRevalidation(t *testing.T) {
	srv, _ := testServer(t)
	ui.SetAccessCodeForTest(srv, testAccessCode)

	headers := map[string]string{
		"Authorization":   "Bearer " + testAccessCode,
		"Accept-Encoding": "gzip",
	}
	rec := serveFrom(srv, networkAddr, http.MethodGet, "/placeholder.html", "", headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /placeholder.html: status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("GET /placeholder.html: Cache-Control = %q, want no-cache", got)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("GET /placeholder.html: missing ETag")
	}

	condHeaders := map[string]string{
		"Authorization":   "Bearer " + testAccessCode,
		"Accept-Encoding": "gzip",
		"If-None-Match":   etag,
	}
	cond := serveFrom(srv, networkAddr, http.MethodGet, "/placeholder.html", "", condHeaders)
	if cond.Code != http.StatusNotModified {
		t.Fatalf("conditional GET /placeholder.html with If-None-Match=%s: status = %d, want 304", etag, cond.Code)
	}
	if cond.Body.Len() != 0 {
		t.Fatalf("304 response carried non-empty body (%d bytes)", cond.Body.Len())
	}
	if got := cond.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("304 Cache-Control = %q, want no-cache", got)
	}
}
