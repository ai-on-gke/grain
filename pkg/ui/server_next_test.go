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

// The Angular UI at /next/ (design/design/angular-ui.design.md) has no
// authentication path of its own: signed out, a deep link into it gets
// the same "Enter access code" page as the React UI, and signing in
// returns to that /next/ link -- the session cookie it then carries is
// the one the old UI uses.
func TestNextUIIsBehindTheSameAccessCodeGate(t *testing.T) {
	srv, _ := testServer(t)
	ui.SetAccessCodeForTest(srv, testAccessCode)

	for _, p := range []string{"/next/", "/next/inbox", "/next"} {
		rec := serveFrom(srv, networkAddr, http.MethodGet, p, "", nil)
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "Enter access code") {
			t.Fatalf("GET %s signed out: code %d, want 401 with the access code form", p, rec.Code)
		}
	}

	form := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	body := url.Values{"code": {testAccessCode}, "name": {"owner@example.com"}, "next": {"/next/inbox"}}.Encode()
	res := serveFrom(srv, networkAddr, http.MethodPost, "/api/auth/access-code", body, form).Result()
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/next/inbox" {
		t.Fatalf("sign in from /next/inbox: code %d Location %q, want 303 to /next/inbox", res.StatusCode, res.Header.Get("Location"))
	}
	var cookie string
	for _, c := range res.Cookies() {
		cookie = c.Name + "=" + c.Value
	}
	if cookie == "" {
		t.Fatal("sign in set no cookie")
	}
	rec := serveFrom(srv, networkAddr, http.MethodGet, "/next", "", map[string]string{"Cookie": cookie})
	if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != "/next/" {
		t.Fatalf("GET /next signed in: code %d Location %q, want 307 to /next/", rec.Code, rec.Header().Get("Location"))
	}
}
