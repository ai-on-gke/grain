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
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/ai-on-gke/grain/pkg/model"
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
	// 1. No access code: non-exempt routes, unhashed or missing hashed-looking
	// paths, and API calls all refuse with 401 and Cache-Control: no-store.
	srv, _ := testServer(t)
	ui.SetAccessCodeForTest(srv, testAccessCode)

	for _, target := range []string{
		"/",
		"/next/",
		"/next/index.html",
		"/next/inbox",
		"/next/main-ZZZZZZZZ.js",
		"/next/styles-ZZZZZZZZ.css",
		"/assets/index-ZZZZZZZZ.js",
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
			path:     "/next/inbox",
			headers:  map[string]string{"X-Corp-Proxy-Assertion": "bad:mallory@example.com"},
			wantCode: http.StatusUnauthorized,
		},
		{
			name:     "bad assertion missing hashed bundle HTML",
			method:   http.MethodGet,
			path:     "/next/main-ZZZZZZZZ.js",
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
			path:     "/next/inbox",
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

	recDelegateHTML := h.do(http.MethodGet, "/next/inbox", "", map[string]string{ui.RegistryAssertionHeader: tok})
	assertNoStoreNeverImmutable(t, "delegate refused HTML", recDelegateHTML.Code, http.StatusForbidden, recDelegateHTML.Header().Get("Cache-Control"))

	// RequireIAP over network without assertion (401 HTML and JSON).
	iapSrv, _, _ := requireIAPServer(t, true)
	recIAPHTML := serveFrom(iapSrv, networkAddr, http.MethodGet, "/next/inbox", "", nil)
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

func seedTestFrontendFS(t *testing.T, srv *ui.Server) {
	t.Helper()
	legacyFS := fstest.MapFS{
		"index.html":                {Data: []byte("<html>legacy</html>")},
		"assets/app.js":             {Data: []byte("console.log('unhashed');")},
		"assets/index-co56kW-3.js":  {Data: []byte("console.log('legacy-bundle');")},
		"assets/index-CL8eXieL.css": {Data: []byte("body{margin:0}")},
		"assets/index-co56kW-3.js.map": {
			Data: []byte(`{"version":3}`),
		},
		"assets/dir-12345678.js":         {Mode: fs.ModeDir | 0o755},
		"assets/dir-12345678.js/keep.js": {Data: []byte("nested")},
	}
	nextFS := fstest.MapFS{
		"index.html":            {Data: []byte("<html>next</html>")},
		"main-UCKK7PSP.js":      {Data: []byte("console.log('next-main');")},
		"polyfills-5CFQRCPP.js": {Data: []byte("console.log('next-polyfills');")},
		"chunk-OMVX5472.js":     {Data: []byte("console.log('next-chunk');")},
		"styles-P323WRHF.css":   {Data: []byte("body{color:#111}")},
		"chunk-OMVX5472.js.map": {Data: []byte(`{"version":3}`)},
		"sub/chunk-OMVX5472.js": {Data: []byte("console.log('nested-chunk');")},
		"dir-12345678.js":       {Mode: fs.ModeDir | 0o755},
	}
	ui.SetFrontendFSForTest(srv, legacyFS, nextFS)
}

func TestHashedFrontendBundleExemptionServesWithoutCredentialsOrWithInvalidHeaders(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	logs := ui.LogBufferForTest(t)

	unreg := ui.RegisterIdentityVerifier(&fakeHeaderVerifier{
		name:    "corp-proxy",
		headers: []string{"X-Corp-Proxy-Assertion"},
		verifyFn: func(r *http.Request, raw string) (ui.VerifiedIdentity, error) {
			if raw == "unavailable" {
				return ui.VerifiedIdentity{}, fmt.Errorf("checking assertion: %w", verifierNotReady{})
			}
			return parseSimpleSignedHeader(r, raw)
		},
	})
	t.Cleanup(unreg)

	srv, client := testServer(t)
	client.Config.IAPAudience = "https://grain.example.com"
	client.Config.RequireIAP = true
	client.Config.HubIssuer = "https://hub.example.com"
	client.Config.HubAudience = "grain"
	client.Config.HubKeysURL = "http://127.0.0.1:1/jwks"
	client.Config.AllowedPrincipals = []string{"user:alice@example.com"}
	ui.SetAccessCodeForTest(srv, testAccessCode)
	seedTestFrontendFS(t, srv)

	bundles := []struct {
		path            string
		wantBody        string
		wantContentType string
	}{
		{
			path:            "/next/chunk-OMVX5472.js",
			wantBody:        "console.log('next-chunk');",
			wantContentType: "javascript",
		},
		{
			path:            "/next/main-UCKK7PSP.js",
			wantBody:        "console.log('next-main');",
			wantContentType: "javascript",
		},
		{
			path:            "/next/styles-P323WRHF.css",
			wantBody:        "body{color:#111}",
			wantContentType: "text/css",
		},
		{
			path:            "/assets/index-co56kW-3.js",
			wantBody:        "console.log('legacy-bundle');",
			wantContentType: "javascript",
		},
		{
			path:            "/assets/index-CL8eXieL.css",
			wantBody:        "body{margin:0}",
			wantContentType: "text/css",
		},
	}

	callerCases := []struct {
		name    string
		headers map[string]string
	}{
		{
			name:    "no credentials",
			headers: nil,
		},
		{
			name:    "forged IAP assertion",
			headers: map[string]string{ui.IAPAssertionHeader: "forged.iap.jwt"},
		},
		{
			name:    "forged hub assertion",
			headers: map[string]string{ui.RegistryAssertionHeader: "forged.hub.jwt"},
		},
		{
			name:    "forged registered verifier assertion",
			headers: map[string]string{"X-Corp-Proxy-Assertion": "bad:mallory@example.com"},
		},
		{
			name:    "unavailable registered verifier",
			headers: map[string]string{"X-Corp-Proxy-Assertion": "unavailable"},
		},
		{
			name:    "invalid access-code cookie",
			headers: map[string]string{"Cookie": ui.AccessCookieName + "=v2.1700000000.bWFsbG9yeQ.forged-mac"},
		},
		{
			name:    "plaintext identity header",
			headers: map[string]string{"X-Forwarded-User": "mallory@example.com", "X-Grain-User": "mallory@example.com"},
		},
	}

	for _, b := range bundles {
		for _, cc := range callerCases {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				label := fmt.Sprintf("%s %s (%s)", method, b.path, cc.name)
				rec := serveFrom(srv, networkAddr, method, b.path, "", cc.headers)
				if rec.Code != http.StatusOK {
					t.Fatalf("%s: status = %d, want 200 (body=%q)", label, rec.Code, rec.Body.String())
				}
				if got := rec.Header().Get("Cache-Control"); got != "private, max-age=31536000, immutable" {
					t.Fatalf("%s: Cache-Control = %q, want private, max-age=31536000, immutable", label, got)
				}
				if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, b.wantContentType) {
					t.Fatalf("%s: Content-Type = %q, want substring %q", label, ct, b.wantContentType)
				}
				if sc := rec.Header().Values("Set-Cookie"); len(sc) != 0 {
					t.Fatalf("%s: unexpected Set-Cookie headers %v", label, sc)
				}
				if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
					t.Fatalf("%s: X-Content-Type-Options = %q, want nosniff", label, got)
				}
				if method == http.MethodGet {
					if rec.Body.String() != b.wantBody {
						t.Fatalf("%s: body = %q, want %q", label, rec.Body.String(), b.wantBody)
					}
				} else if rec.Body.Len() != 0 {
					t.Fatalf("%s: HEAD body len = %d, want 0", label, rec.Body.Len())
				}
			}
		}
	}

	if got := logs.String(); got != "" {
		t.Fatalf("exempt bundle requests logged unexpected output:\n%s", got)
	}
}

func TestHashedFrontendBundleExemptionRejectsNonExemptRequests(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)

	srv, _ := testServer(t)
	ui.SetAccessCodeForTest(srv, testAccessCode)
	seedTestFrontendFS(t, srv)

	nonExempt := []struct {
		method string
		target string
	}{
		{http.MethodGet, "/next/"},
		{http.MethodGet, "/next/index.html"},
		{http.MethodGet, "/next/inbox"},
		{http.MethodGet, "/api/config"},
		{http.MethodGet, "/assets/app.js"},
		{http.MethodGet, "/next/chunk-OMVX5472.js?x=1"},
		{http.MethodGet, "/next/chunk-OMVX5472.js?"},
		{http.MethodGet, "/next//chunk-OMVX5472.js"},
		{http.MethodGet, "/next/./chunk-OMVX5472.js"},
		{http.MethodGet, "/next/../api/config"},
		{http.MethodGet, "/next/%2e%2e/api/config"},
		{http.MethodGet, "/next/%63hunk-OMVX5472.js"},
		{http.MethodGet, "/next/chunk-OMVX5472.js/"},
		{http.MethodGet, "/next/sub/chunk-OMVX5472.js"},
		{http.MethodGet, "/next/chunk-ZZZZZZZZ.js"},
		{http.MethodGet, "/assets/index-ZZZZZZZZ.js"},
		{http.MethodGet, "/next/chunk-OMVX5472.js.map"},
		{http.MethodGet, "/assets/index-co56kW-3.js.map"},
		{http.MethodGet, "/next/dir-12345678.js"},
		{http.MethodGet, "/assets/dir-12345678.js"},
		{http.MethodGet, "/next/index-co56kW-3.js"},
		{http.MethodGet, "/assets/chunk-OMVX5472.js"},
		{http.MethodPost, "/next/chunk-OMVX5472.js"},
		{http.MethodPost, "/assets/index-co56kW-3.js"},
	}

	for _, tc := range nonExempt {
		label := tc.method + " " + tc.target
		rec := serveFrom(srv, networkAddr, tc.method, tc.target, "", nil)
		assertNoStoreNeverImmutable(t, label, rec.Code, http.StatusUnauthorized, rec.Header().Get("Cache-Control"))
	}

	// Hub tunnel requests for a hashed bundle must keep their current verification:
	// refused with 401 when missing/invalid registry assertion, and refused with 403
	// when the registry assertion is valid but its principal is not allowlisted.
	tunnelHandler := srv.HubTunnelHandler()
	for _, target := range []string{"/next/chunk-OMVX5472.js", "/assets/index-co56kW-3.js"} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.RemoteAddr = networkAddr
		rec := httptest.NewRecorder()
		tunnelHandler.ServeHTTP(rec, req)
		assertNoStoreNeverImmutable(t, "hub tunnel unauthenticated GET "+target, rec.Code, http.StatusUnauthorized, rec.Header().Get("Cache-Control"))
	}

	h := newDelegationHarness(t)
	seedTestFrontendFS(t, h.srv)
	unallowedTok := h.mintDirect(t, "mallory@example.com", model.PrincipalHuman)
	for _, target := range []string{"/next/chunk-OMVX5472.js", "/assets/index-co56kW-3.js"} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.RemoteAddr = networkAddr
		req.Header.Set(ui.RegistryAssertionHeader, unallowedTok)
		rec := httptest.NewRecorder()
		h.srv.HubTunnelHandler().ServeHTTP(rec, req)
		assertNoStoreNeverImmutable(t, "hub tunnel disallowed principal GET "+target, rec.Code, http.StatusForbidden, rec.Header().Get("Cache-Control"))
	}
}

type countingVerifier struct {
	name    string
	headers []string
	calls   atomic.Int64
}

func (v *countingVerifier) Name() string      { return v.name }
func (v *countingVerifier) Headers() []string { return v.headers }
func (v *countingVerifier) Verify(r *http.Request) (ui.VerifiedIdentity, bool, error) {
	v.calls.Add(1)
	return ui.VerifiedIdentity{}, false, fmt.Errorf("counting verifier should not have been called")
}

func TestHashedFrontendBundleExemptionNeverCallsIdentityVerifier(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)

	headerVerifier := &countingVerifier{
		name:    "counting-header",
		headers: []string{"X-Counting-Assertion"},
	}
	headerlessVerifier := &countingVerifier{
		name:    "counting-headerless",
		headers: nil,
	}
	unreg1 := ui.RegisterIdentityVerifier(headerVerifier)
	t.Cleanup(unreg1)
	unreg2 := ui.RegisterIdentityVerifier(headerlessVerifier)
	t.Cleanup(unreg2)

	srv, _ := testServer(t)
	ui.SetAccessCodeForTest(srv, testAccessCode)
	seedTestFrontendFS(t, srv)

	for _, target := range []string{
		"/next/chunk-OMVX5472.js",
		"/next/styles-P323WRHF.css",
		"/assets/index-co56kW-3.js",
		"/assets/index-CL8eXieL.css",
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			for _, hdrs := range []map[string]string{
				nil,
				{"X-Counting-Assertion": "present-assertion"},
			} {
				rec := serveFrom(srv, networkAddr, method, target, "", hdrs)
				if rec.Code != http.StatusOK {
					t.Fatalf("%s %s: status = %d, want 200", method, target, rec.Code)
				}
				if got := rec.Header().Get("Cache-Control"); got != "private, max-age=31536000, immutable" {
					t.Fatalf("%s %s: Cache-Control = %q, want immutable", method, target, got)
				}
			}
		}
	}

	if got := headerVerifier.calls.Load(); got != 0 {
		t.Fatalf("headerVerifier.Verify calls = %d, want 0 on exempt bundle requests", got)
	}
	if got := headerlessVerifier.calls.Load(); got != 0 {
		t.Fatalf("headerlessVerifier.Verify calls = %d, want 0 on exempt bundle requests", got)
	}

	// Sanity check: a non-exempt request DOES invoke the registered verifier.
	rec := serveFrom(srv, networkAddr, http.MethodGet, "/next/index.html", "", map[string]string{"X-Counting-Assertion": "present-assertion"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /next/index.html with counting assertion: status = %d, want 401", rec.Code)
	}
	if got := headerVerifier.calls.Load(); got != 1 {
		t.Fatalf("headerVerifier.Verify calls after /next/index.html = %d, want 1", got)
	}
}

func TestHashedFrontendBundleExemptionGzipCacheAndRangeIgnored(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)

	srv, _ := testServer(t)
	ui.SetAccessCodeForTest(srv, testAccessCode)

	bundleBody := strings.Repeat("export const chunkValue = 'immutable-bundle-payload';\n", 64)
	gatedAssetBody := strings.Repeat("console.log('gated-unhashed-asset');\n", 64)
	legacyFS := fstest.MapFS{
		"index.html":               {Data: []byte("<html>legacy</html>")},
		"assets/app.js":            {Data: []byte(gatedAssetBody)},
		"assets/index-co56kW-3.js": {Data: []byte(bundleBody)},
	}
	nextFS := fstest.MapFS{
		"index.html":        {Data: []byte("<html>next</html>")},
		"chunk-OMVX5472.js": {Data: []byte(bundleBody)},
	}
	ui.SetFrontendFSForTest(srv, legacyFS, nextFS)

	// 1. Identity variant when gzip is not accepted (lazy gzip cache not computed yet).
	for _, acceptEnc := range []string{"", "identity", "gzip;q=0"} {
		hdrs := map[string]string{}
		if acceptEnc != "" {
			hdrs["Accept-Encoding"] = acceptEnc
		}
		idRec := serveFrom(srv, networkAddr, http.MethodGet, "/next/chunk-OMVX5472.js", "", hdrs)
		if idRec.Code != http.StatusOK {
			t.Fatalf("identity GET (Accept-Encoding=%q): status = %d, want 200", acceptEnc, idRec.Code)
		}
		if got := idRec.Header().Get("Content-Encoding"); got != "" {
			t.Fatalf("identity GET (Accept-Encoding=%q): Content-Encoding = %q, want empty", acceptEnc, got)
		}
		strongETag := idRec.Header().Get("ETag")
		if !strings.HasPrefix(strongETag, `"`) || strings.HasPrefix(strongETag, "W/") {
			t.Fatalf("identity GET (Accept-Encoding=%q): ETag = %q, want strong ETag", acceptEnc, strongETag)
		}
		if idRec.Body.String() != bundleBody {
			t.Fatalf("identity GET (Accept-Encoding=%q): body mismatch (len=%d, want %d)", acceptEnc, idRec.Body.Len(), len(bundleBody))
		}
	}
	if got := ui.FrontendGzipComputationsForTest(srv); got != 0 {
		t.Fatalf("FrontendGzipComputationsForTest after identity requests = %d, want 0 (lazy gzip)", got)
	}

	// Capture strong ETag of /next/chunk-OMVX5472.js for weak ETag comparison.
	idRec := serveFrom(srv, networkAddr, http.MethodGet, "/next/chunk-OMVX5472.js", "", nil)
	strongETag := idRec.Header().Get("ETag")

	// 2. Gzip served from per-file cache with correct headers and identical bytes on a second request,
	// and not gzipped a second time by withCompression.
	gzRec1 := serveFrom(srv, networkAddr, http.MethodGet, "/next/chunk-OMVX5472.js", "", map[string]string{
		"Accept-Encoding": "gzip",
	})
	if gzRec1.Code != http.StatusOK {
		t.Fatalf("first gzip GET: status = %d, want 200", gzRec1.Code)
	}
	if got := gzRec1.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("first gzip GET: Content-Encoding = %q, want gzip", got)
	}
	if got := strings.Join(gzRec1.Header().Values("Vary"), ","); !strings.Contains(got, "Accept-Encoding") {
		t.Fatalf("first gzip GET: Vary = %q, want Accept-Encoding", got)
	}
	if got := gzRec1.Header().Get("Cache-Control"); got != "private, max-age=31536000, immutable" {
		t.Fatalf("first gzip GET: Cache-Control = %q, want immutable", got)
	}
	wantWeakETag := "W/" + strongETag
	if got := gzRec1.Header().Get("ETag"); got != wantWeakETag {
		t.Fatalf("first gzip GET: ETag = %q, want %q", got, wantWeakETag)
	}
	if got := ui.FrontendGzipComputationsForTest(srv); got != 1 {
		t.Fatalf("FrontendGzipComputationsForTest after first gzip GET = %d, want 1", got)
	}
	// Decompress once: if withCompression had gzipped a second time, this would yield gzip bytes instead of bundleBody.
	zr, err := gzip.NewReader(bytes.NewReader(gzRec1.Body.Bytes()))
	if err != nil {
		t.Fatalf("gzip.NewReader on first gzip GET: %v", err)
	}
	decompressed, err := io.ReadAll(zr)
	_ = zr.Close()
	if err != nil {
		t.Fatalf("decompressing first gzip GET: %v", err)
	}
	if string(decompressed) != bundleBody {
		t.Fatalf("decompressed first gzip GET body mismatch (len=%d, want %d)", len(decompressed), len(bundleBody))
	}

	// Second gzip request for the same bundle must return identical bytes and headers without re-compressing.
	gzRec2 := serveFrom(srv, networkAddr, http.MethodGet, "/next/chunk-OMVX5472.js", "", map[string]string{
		"Accept-Encoding": "deflate, gzip;q=1",
	})
	if gzRec2.Code != http.StatusOK {
		t.Fatalf("second gzip GET: status = %d, want 200", gzRec2.Code)
	}
	if got := gzRec2.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("second gzip GET: Content-Encoding = %q, want gzip", got)
	}
	if got := strings.Join(gzRec2.Header().Values("Vary"), ","); !strings.Contains(got, "Accept-Encoding") {
		t.Fatalf("second gzip GET: Vary = %q, want Accept-Encoding", got)
	}
	if got := gzRec2.Header().Get("ETag"); got != wantWeakETag {
		t.Fatalf("second gzip GET: ETag = %q, want %q", got, wantWeakETag)
	}
	if !bytes.Equal(gzRec2.Body.Bytes(), gzRec1.Body.Bytes()) {
		t.Fatalf("second gzip GET body bytes differ from first cached response (%d vs %d bytes)", gzRec2.Body.Len(), gzRec1.Body.Len())
	}
	if got := ui.FrontendGzipComputationsForTest(srv); got != 1 {
		t.Fatalf("FrontendGzipComputationsForTest after second gzip GET = %d, want 1 (must serve from cache)", got)
	}

	// 3. Range requests (single and multi-range, identity and gzip) on an exempt bundle are ignored
	// and always return 200 OK with the full body.
	for _, rangeHdr := range []string{"bytes=0-3", "bytes=0-3,5-8"} {
		rangeId := serveFrom(srv, networkAddr, http.MethodGet, "/next/chunk-OMVX5472.js", "", map[string]string{
			"Range": rangeHdr,
		})
		if rangeId.Code != http.StatusOK {
			t.Fatalf("exempt identity GET with Range %q: status = %d, want 200", rangeHdr, rangeId.Code)
		}
		if got := rangeId.Header().Get("Content-Range"); got != "" {
			t.Fatalf("exempt identity GET with Range %q: unexpected Content-Range = %q", rangeHdr, got)
		}
		if ct := rangeId.Header().Get("Content-Type"); strings.Contains(ct, "multipart") {
			t.Fatalf("exempt identity GET with Range %q: unexpected multipart Content-Type = %q", rangeHdr, ct)
		}
		if rangeId.Body.String() != bundleBody {
			t.Fatalf("exempt identity GET with Range %q: body len = %d, want full %d", rangeHdr, rangeId.Body.Len(), len(bundleBody))
		}

		rangeGz := serveFrom(srv, networkAddr, http.MethodGet, "/next/chunk-OMVX5472.js", "", map[string]string{
			"Range":           rangeHdr,
			"Accept-Encoding": "gzip",
		})
		if rangeGz.Code != http.StatusOK {
			t.Fatalf("exempt gzip GET with Range %q: status = %d, want 200", rangeHdr, rangeGz.Code)
		}
		if got := rangeGz.Header().Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("exempt gzip GET with Range %q: Content-Encoding = %q, want gzip", rangeHdr, got)
		}
		if got := rangeGz.Header().Get("ETag"); got != wantWeakETag {
			t.Fatalf("exempt gzip GET with Range %q: ETag = %q, want %q", rangeHdr, got, wantWeakETag)
		}
		if !bytes.Equal(rangeGz.Body.Bytes(), gzRec1.Body.Bytes()) {
			t.Fatalf("exempt gzip GET with Range %q: body bytes differ from cached full gzip body", rangeHdr)
		}
	}
	if got := ui.FrontendGzipComputationsForTest(srv); got != 1 {
		t.Fatalf("FrontendGzipComputationsForTest after Range gzip GETs = %d, want 1", got)
	}

	// 4. Gated paths keep their behaviour unchanged: refused without credentials, and honouring Range
	// and withCompression when authenticated.
	unauthGated := serveFrom(srv, networkAddr, http.MethodGet, "/assets/app.js", "", map[string]string{
		"Range": "bytes=0-3",
	})
	assertNoStoreNeverImmutable(t, "unauthenticated Range GET /assets/app.js", unauthGated.Code, http.StatusUnauthorized, unauthGated.Header().Get("Cache-Control"))

	authRange := serveFrom(srv, networkAddr, http.MethodGet, "/assets/app.js", "", map[string]string{
		"Authorization": "Bearer " + testAccessCode,
		"Range":         "bytes=0-3",
	})
	if authRange.Code != http.StatusPartialContent {
		t.Fatalf("authenticated Range GET /assets/app.js: status = %d, want 206", authRange.Code)
	}
	if authRange.Body.String() != gatedAssetBody[:4] {
		t.Fatalf("authenticated Range GET /assets/app.js: body = %q, want %q", authRange.Body.String(), gatedAssetBody[:4])
	}

	authGzip := serveFrom(srv, networkAddr, http.MethodGet, "/assets/app.js", "", map[string]string{
		"Authorization":   "Bearer " + testAccessCode,
		"Accept-Encoding": "gzip",
	})
	if authGzip.Code != http.StatusOK {
		t.Fatalf("authenticated gzip GET /assets/app.js: status = %d, want 200", authGzip.Code)
	}
	if got := authGzip.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("authenticated gzip GET /assets/app.js: Content-Encoding = %q, want gzip", got)
	}
	if got := authGzip.Header().Get("ETag"); !strings.HasPrefix(got, `W/"`) {
		t.Fatalf("authenticated gzip GET /assets/app.js: ETag = %q, want weak W/\"...\"", got)
	}
	if got := ui.FrontendGzipComputationsForTest(srv); got != 1 {
		t.Fatalf("FrontendGzipComputationsForTest after gated gzip GET = %d, want 1", got)
	}
}
