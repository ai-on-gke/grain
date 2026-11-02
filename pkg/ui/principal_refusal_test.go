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
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// A person an IdentityVerifier vouches for but the allowlist refuses is shown
// a page, not JSON, when their browser loads the UI: who they are, how the
// owner lets them in, and -- for a verifier an access code overrides -- the
// access-code form, which signs the owner in. API callers keep the 403 JSON.
func TestRefusedPrincipalGetsAPageNamingThemWithTheAccessCodeForm(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	t.Setenv("GRAIN_ALLOWED_PRINCIPALS", "")
	unreg := ui.RegisterIdentityVerifier(&fakePrecedenceVerifier{
		fakeHeaderVerifier: &fakeHeaderVerifier{
			name:           "corp-proxy",
			headers:        []string{"X-Corp-Proxy-Assertion"},
			accessCodeWins: true,
			verifyFn:       parseSimpleSignedHeader,
		},
	})
	t.Cleanup(unreg)

	srv, client := testServer(t)
	client.Config.AllowedPrincipals = []string{"user:alice@example.com"}
	ui.SetAccessCodeForTest(srv, testAccessCode)
	assertion := map[string]string{"X-Corp-Proxy-Assertion": "ok:mallory@example.com"}

	rec := serveFrom(srv, "198.51.100.8:1234", http.MethodGet, "/", "", assertion)
	body := rec.Body.String()
	if rec.Code != http.StatusForbidden || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("browser GET /: got %d %q, want a 403 HTML page: %s", rec.Code, rec.Header().Get("Content-Type"), body)
	}
	for _, want := range []string{
		"mallory@example.com",
		"verified by corp-proxy",
		"-ui-allowed-principals",
		"GRAIN_ALLOWED_PRINCIPALS",
		"-ui-allowed-principals=&#39;user:alice@example.com,user:mallory@example.com&#39;",
		`<form method="post" action="/api/auth/access-code">`,
		`name="code"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("refusal page lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "takes precedence") {
		t.Errorf("refusal page for an AccessCodeWins verifier says the assertion takes precedence:\n%s", body)
	}

	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/tasks"},
		{http.MethodPost, "/"},
	} {
		rec := serveFrom(srv, "198.51.100.8:1234", c.method, c.path, "", assertion)
		if rec.Code != http.StatusForbidden || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") ||
			!strings.Contains(rec.Body.String(), `principal \"mallory@example.com\" is not allowed`) {
			t.Fatalf("%s %s: got %d %q, want the 403 JSON: %s", c.method, c.path, rec.Code, rec.Header().Get("Content-Type"), rec.Body)
		}
	}

	// Submitting the page's form signs in by code, and the session then
	// gets the UI past the refused assertion.
	hdrs := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	for k, v := range assertion {
		hdrs[k] = v
	}
	rec = serveFrom(srv, "198.51.100.8:1234", http.MethodPost, "/api/auth/access-code", url.Values{"code": {testAccessCode}}.Encode(), hdrs)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("form post: got %d, want 303: %s", rec.Code, rec.Body)
	}
	var session string
	for _, c := range rec.Result().Cookies() {
		if c.Name == ui.AccessCookieName {
			session = c.Value
		}
	}
	if session == "" {
		t.Fatal("form post set no access session cookie")
	}
	hdrs = map[string]string{"Cookie": ui.AccessCookieName + "=" + session}
	for k, v := range assertion {
		hdrs[k] = v
	}
	if rec := serveFrom(srv, "198.51.100.8:1234", http.MethodGet, "/api/tasks", "", hdrs); rec.Code != http.StatusOK {
		t.Fatalf("after signing in by code: got %d, want 200: %s", rec.Code, rec.Body)
	}
}

// For a verifier whose assertion beats the access code, the page says so
// instead of offering a form that could not get anybody in.
func TestRefusedPrincipalPageSaysTheAssertionTakesPrecedence(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	t.Setenv("GRAIN_ALLOWED_PRINCIPALS", "")
	unreg := ui.RegisterIdentityVerifier(&fakeHeaderVerifier{
		name:     "strict-proxy",
		headers:  []string{"X-Strict-Assertion"},
		verifyFn: parseSimpleSignedHeader,
	})
	t.Cleanup(unreg)

	srv, client := testServer(t)
	client.Config.AllowedPrincipals = []string{"user:alice@example.com", "group:ops@example.com"}
	client.Config.GroupChecker = groupCheckerFunc(func(ctx context.Context, group, member string) (bool, error) {
		return false, errors.New("cloud identity outage")
	})
	ui.SetAccessCodeForTest(srv, testAccessCode)

	rec := serveFrom(srv, "198.51.100.8:1234", http.MethodGet, "/tasks/7", "", map[string]string{"X-Strict-Assertion": "ok:oncall@example.com"})
	body := rec.Body.String()
	if rec.Code != http.StatusForbidden || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("browser GET: got %d %q, want a 403 HTML page: %s", rec.Code, rec.Header().Get("Content-Type"), body)
	}
	for _, want := range []string{"oncall@example.com", "verified by strict-proxy", "takes precedence", "grain access-code", "cloud identity outage"} {
		if !strings.Contains(body, want) {
			t.Errorf("refusal page lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "<form") {
		t.Errorf("refusal page offers a form the assertion would override:\n%s", body)
	}
}

// An environment's layer registers its own advice, which replaces the
// default's naming of the daemon flag.
func TestRefusedPrincipalPageUsesRegisteredAdvice(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	t.Setenv("GRAIN_ALLOWED_PRINCIPALS", "")
	t.Cleanup(ui.RegisterIdentityVerifier(&fakeHeaderVerifier{
		name:     "corp-proxy",
		headers:  []string{"X-Corp-Proxy-Assertion"},
		verifyFn: parseSimpleSignedHeader,
	}))
	var gotEntry, gotRerun string
	t.Cleanup(ui.RegisterPrincipalAdvice(func(entry, rerun string) string {
		gotEntry, gotRerun = entry, rerun
		return "Re-run scripts/cloudtop-install.sh with GRAIN_ALLOWED_PRINCIPALS=" + rerun + "."
	}))

	srv, client := testServer(t)
	client.Config.AllowedPrincipals = []string{"user:alice@example.com"}
	ui.SetAccessCodeForTest(srv, testAccessCode)

	rec := serveFrom(srv, "198.51.100.8:1234", http.MethodGet, "/", "", map[string]string{"X-Corp-Proxy-Assertion": "ok:Bob@Example.com"})
	if gotEntry != "user:bob@example.com" || gotRerun != "user:alice@example.com,user:bob@example.com" {
		t.Fatalf("advice given (%q, %q), want (user:bob@example.com, user:alice@example.com,user:bob@example.com)", gotEntry, gotRerun)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Re-run scripts/cloudtop-install.sh with GRAIN_ALLOWED_PRINCIPALS=user:alice@example.com,user:bob@example.com.") {
		t.Fatalf("refusal page lacks the registered advice:\n%s", body)
	}
	if strings.Contains(body, "grain init -daemon-arg") {
		t.Fatalf("refusal page still gives the default advice:\n%s", body)
	}
}
