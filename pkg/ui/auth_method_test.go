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
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// serveFrom sends one request to srv as if it arrived from remoteAddr.
func serveFrom(srv *ui.Server, remoteAddr, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = remoteAddr
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// grain/task-2142 & grain/task-2309: the frontend shapes what it offers by how
// the caller authenticated, which it can only do if the server says. The access
// code's caller is the trusted operator (signed in as nobody); an allowed IAP
// caller is admitted as themselves with authMethod "iap"; an unallowed IAP
// caller gets 403 naming the email; and a caller with no credential gets 401.
func TestAuthUserReportsHowTheCallerAuthenticated(t *testing.T) {
	priv, pemStr := generateECDSATestKey(t)
	assertionKeys(t, priv, pemStr, false)
	ui.SetIAPAudience(tunnelAudience)
	srv, client := testServer(t)
	client.Config.AllowedPrincipals = []string{"user:iap@example.com"}
	ui.SetAccessCodeForTest(srv, testAccessCode)
	admittedAssertion := signIAPToken(t, priv, tunnelKid, tunnelAudience, "iap@example.com", "x", ui.GoogleIAPIssuer, time.Now().Add(5*time.Minute))
	strangerAssertion := signIAPToken(t, priv, tunnelKid, tunnelAudience, "stranger@example.com", "y", ui.GoogleIAPIssuer, time.Now().Add(5*time.Minute))
	code := map[string]string{"Authorization": "Bearer " + testAccessCode}

	for _, path := range []string{"/api/auth/user", "/api/config"} {
		rec := serveFrom(srv, "127.0.0.1:1", http.MethodGet, path, "", code)
		if rec.Code != http.StatusOK {
			t.Errorf("access code: GET %s: code %d: %s", path, rec.Code, rec.Body)
			continue
		}
		var got struct {
			Authenticated bool          `json:"authenticated"`
			User          string        `json:"user"`
			Actor         string        `json:"actor"`
			AuthMethod    ui.AuthMethod `json:"authMethod"`
			Tenancy       string        `json:"tenancy"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
			t.Fatalf("decoding %s: %v", path, err)
		}
		// /api/auth/user still says "single" for the cloudtop installer;
		// /api/config dropped the field, which had no other value.
		wantTenancy := "single"
		if path == "/api/config" {
			wantTenancy = ""
		}
		if got.AuthMethod != ui.AuthMethodTrusted || got.Authenticated || got.User != "" || got.Tenancy != wantTenancy {
			t.Errorf("access code: GET %s = %+v, want the trusted operator, nobody signed in, tenancy %q", path, got, wantTenancy)
		}

		// Admitted IAP caller gets 200 with authMethod "iap" and their email.
		recIAP := serveFrom(srv, "127.0.0.1:1", http.MethodGet, path, "", map[string]string{ui.IAPAssertionHeader: admittedAssertion})
		if recIAP.Code != http.StatusOK {
			t.Errorf("admitted IAP assertion: GET %s: code %d, want 200: %s", path, recIAP.Code, recIAP.Body)
		} else {
			var gotIAP struct {
				Authenticated bool          `json:"authenticated"`
				User          string        `json:"user"`
				Actor         string        `json:"actor"`
				AuthMethod    ui.AuthMethod `json:"authMethod"`
			}
			if err := json.NewDecoder(recIAP.Body).Decode(&gotIAP); err != nil {
				t.Fatalf("decoding IAP %s: %v", path, err)
			}
			wantWho := "iap@example.com"
			gotWho := gotIAP.User
			if path == "/api/config" {
				gotWho = gotIAP.Actor
			}
			if !gotIAP.Authenticated || gotIAP.AuthMethod != ui.AuthMethodIAP || gotWho != wantWho {
				t.Errorf("admitted IAP assertion: GET %s = %+v, want authenticated iap@example.com via iap", path, gotIAP)
			}
		}

		// Verified IAP caller not on the allowlist gets 403 naming the email.
		recStranger := serveFrom(srv, "127.0.0.1:1", http.MethodGet, path, "", map[string]string{ui.IAPAssertionHeader: strangerAssertion})
		if recStranger.Code != http.StatusForbidden || !strings.Contains(recStranger.Body.String(), "stranger@example.com") {
			t.Errorf("unallowed IAP assertion: GET %s: code %d, want 403 naming stranger@example.com: %s", path, recStranger.Code, recStranger.Body)
		}

		// Caller with no credential gets 401.
		if rec := serveFrom(srv, "127.0.0.1:1", http.MethodGet, path, "", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("no credential: GET %s: code %d, want 401: %s", path, rec.Code, rec.Body)
		}
	}
}
