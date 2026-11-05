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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestRefusedAssertionLoggedOncePerKindNotOncePerAudience pins the
// 2026-11-02 verified-identity walkthrough's F1: the refused-assertion
// log line was deduplicated on issuer and audience alone, so after one
// expired assertion for this deployment's own audience, every forged
// one (bad signature, unknown kid) for that audience was refused with
// no log line at all for the life of the daemon.
func TestRefusedAssertionLoggedOncePerKindNotOncePerAudience(t *testing.T) {
	logs := ui.LogBufferForTest(t)
	priv, pemStr := generateECDSATestKey(t)
	assertionKeys(t, priv, pemStr, true)
	ui.SetIAPAudience(tunnelAudience)
	other, _ := generateECDSATestKey(t)

	srv, client := testServer(t)
	client.Config.AllowedPrincipals = []string{"user:alice@example.com"}
	ui.SetAccessCodeForTest(srv, testAccessCode)

	exp := time.Now().Add(5 * time.Minute)
	expired := signIAPToken(t, priv, tunnelKid, tunnelAudience, "alice@example.com", "s", ui.GoogleIAPIssuer, time.Now().Add(-10*time.Minute))
	forged := signIAPToken(t, other, tunnelKid, tunnelAudience, "alice@example.com", "s", ui.GoogleIAPIssuer, exp)
	unknownKid := signIAPToken(t, priv, "not-published", tunnelAudience, "alice@example.com", "s", ui.GoogleIAPIssuer, exp)

	for _, c := range []struct{ name, tok, want string }{
		{"expired", expired, "token is expired"},
		{"forged", forged, "token signature is invalid"},
		{"unknown kid", unknownKid, `unknown IAP key ID "not-published"`},
		{"forged again", forged, ""},
	} {
		rec := serveFrom(srv, "203.0.113.20:443", http.MethodGet, "/api/tasks", "", map[string]string{ui.IAPAssertionHeader: c.tok})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: got %d, want 401: %s", c.name, rec.Code, rec.Body)
		}
		if c.want != "" && !strings.Contains(logs.String(), c.want) {
			t.Errorf("%s: no refused-assertion log line naming %q; log:\n%s", c.name, c.want, logs.String())
		}
	}
	if n := strings.Count(logs.String(), "token signature is invalid"); n != 1 {
		t.Errorf("forged assertion logged %d times, want once:\n%s", n, logs.String())
	}
}

// TestRefusedAssertionForOwnAudienceDoesNotSayToSetTheAudience pins the
// 2026-11-03 IAP deployment walkthrough's F2: an expired or forged
// assertion for the audience this daemon already checks was logged with
// "set -ui-iap-audience ... to match", sending the operator after a
// setting that was already right.
func TestRefusedAssertionForOwnAudienceDoesNotSayToSetTheAudience(t *testing.T) {
	logs := ui.LogBufferForTest(t)
	priv, pemStr := generateECDSATestKey(t)
	assertionKeys(t, priv, pemStr, true)
	// A second audience, so the one assertions carry is one of several and
	// the dedup key (issuer, audience, kind) is not one an earlier test used.
	const own = "/projects/111/global/backendServices/222"
	ui.SetIAPAudience(own + "," + tunnelAudience)
	other, _ := generateECDSATestKey(t)

	srv, client := testServer(t)
	client.Config.AllowedPrincipals = []string{"user:alice@example.com"}

	forged := signIAPToken(t, other, tunnelKid, own, "alice@example.com", "s", ui.GoogleIAPIssuer, time.Now().Add(5*time.Minute))
	rec := serveFrom(srv, "203.0.113.20:443", http.MethodGet, "/api/tasks", "", map[string]string{ui.IAPAssertionHeader: forged})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("forged: got %d, want 401: %s", rec.Code, rec.Body)
	}
	var line string
	for _, l := range strings.Split(logs.String(), "\n") {
		if strings.Contains(l, own) && strings.Contains(l, "token signature is invalid") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("no refused-assertion line for the forged assertion; log:\n%s", logs.String())
	}
	if strings.Contains(line, "set -ui-iap-audience") {
		t.Errorf("forged assertion for the configured audience told the operator to set it:\n%s", line)
	}
	if !strings.Contains(line, "this deployment's own") || !strings.Contains(line, "did not sign") {
		t.Errorf("line does not say the audience is this deployment's own and the issuer did not sign it:\n%s", line)
	}
}
