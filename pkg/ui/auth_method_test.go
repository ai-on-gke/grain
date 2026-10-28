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

// grain/task-2142: the frontend shapes what it offers by how the caller
// authenticated, which it can only do if the server says. A daemon that
// keeps no accounts has one answer for whoever gets in: the access code's
// caller is the operator, signed in as nobody. Anybody else -- a person
// IAP vouched for included -- does not get as far as asking.
func TestAuthUserReportsHowTheCallerAuthenticated(t *testing.T) {
	priv, pemStr := generateECDSATestKey(t)
	assertionKeys(t, priv, pemStr, false)
	ui.SetIAPAudience(tunnelAudience)
	srv, _ := testServer(t)
	ui.SetAccessCodeForTest(srv, testAccessCode)
	assertion := signIAPToken(t, priv, tunnelKid, tunnelAudience, "iap@example.com", "x", ui.GoogleIAPIssuer, time.Now().Add(5*time.Minute))
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

		for name, headers := range map[string]map[string]string{
			"verified IAP assertion": {ui.IAPAssertionHeader: assertion},
			"no credential":          nil,
		} {
			if rec := serveFrom(srv, "127.0.0.1:1", http.MethodGet, path, "", headers); rec.Code != http.StatusUnauthorized {
				t.Errorf("%s: GET %s: code %d, want 401: %s", name, path, rec.Code, rec.Body)
			}
		}
	}
}
