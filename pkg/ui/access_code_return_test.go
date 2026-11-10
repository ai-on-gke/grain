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
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// task 2727: the "Enter access code" page served at a deep link fills
// that link into its form from the address bar -- the server repeats
// nothing the request said -- and signing in goes back to it rather
// than to "/"; a return path that is not a path on this origin goes to
// "/".
func TestAccessCodeSignInReturnsToTheDeepLink(t *testing.T) {
	srv, _ := testServer(t)
	ui.SetAccessCodeForTest(srv, testAccessCode)

	rec := serveFrom(srv, networkAddr, http.MethodGet, "/tasks/42?tab=runs&q=a%20b", "", nil)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "Enter access code") {
		t.Fatalf("GET deep link signed out: code %d, want 401 with the access code form: %s", rec.Code, rec.Body)
	}
	if want := `<input type="hidden" name="next" value="">`; !strings.Contains(rec.Body.String(), want) {
		t.Errorf("login page at a deep link lacks %s: %s", want, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "tasks/42") || strings.Contains(rec.Body.String(), "tab=runs") {
		t.Errorf("login page at a deep link repeats the request: %s", rec.Body)
	}
	rec = serveFrom(srv, networkAddr, http.MethodGet, "/api/auth/access-code/login.js", "", nil)
	if !strings.Contains(rec.Body.String(), "window.location.pathname + window.location.search") {
		t.Errorf("the login page's script does not fill in the return path: %s", rec.Body)
	}

	form := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	post := func(next string) *http.Response {
		t.Helper()
		body := url.Values{"code": {testAccessCode}, "name": {"owner@example.com"}, "next": {next}}.Encode()
		return serveFrom(srv, networkAddr, http.MethodPost, "/api/auth/access-code", body, form).Result()
	}
	for _, tc := range []struct{ next, want string }{
		{"/settings/access", "/settings/access"},
		{"/tasks/42?tab=runs&q=a%20b", "/tasks/42?tab=runs&q=a%20b"},
		{"", "/"},
		{"//evil.example", "/"},
		{"https://evil.example", "/"},
		{"/\\evil", "/"},
		{"/" + strings.Repeat("a", 4096), "/"},
	} {
		res := post(tc.next)
		if res.StatusCode != http.StatusSeeOther {
			t.Errorf("sign in with next %.40q: code %d, want 303", tc.next, res.StatusCode)
			continue
		}
		if got := res.Header.Get("Location"); got != tc.want {
			t.Errorf("sign in with next %.40q: Location %q, want %q", tc.next, got, tc.want)
		}
	}

	// A wrong code shows the page again, still carrying where to go.
	body := url.Values{"code": {"AAAA-BBBB"}, "next": {"/settings/access"}}.Encode()
	rec = serveFrom(srv, networkAddr, http.MethodPost, "/api/auth/access-code", body, form)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `name="next" value="/settings/access"`) {
		t.Errorf("wrong code: code %d, want 401 with the return path kept: %s", rec.Code, rec.Body)
	}
	body = url.Values{"code": {"AAAA-BBBB"}, "next": {"//evil.example"}}.Encode()
	rec = serveFrom(srv, networkAddr, http.MethodPost, "/api/auth/access-code", body, form)
	if strings.Contains(rec.Body.String(), "evil.example") {
		t.Errorf("wrong code with an off-origin return path echoes it: %s", rec.Body)
	}
}
