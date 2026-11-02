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
