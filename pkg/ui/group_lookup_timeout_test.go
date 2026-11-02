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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestGroupAllowlistRefusesWhenTokenEndpointNeverAnswers pins the
// 2026-11-02 verified-identity walkthrough's F2: with Application Default
// Credentials present but their token endpoint accepting the connection
// and never answering, a verified caller who could only be admitted by a
// group: entry hung for as long as the client waited (well past 150s
// against a real daemon), because the default checker's ADC client had no
// timeout. It must be refused, naming the group lookup, instead.
func TestGroupAllowlistRefusesWhenTokenEndpointNeverAnswers(t *testing.T) {
	release := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(hang.Close)
	t.Cleanup(func() { close(release) })

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	creds, _ := json.Marshal(map[string]string{
		"type":           "service_account",
		"project_id":     "my-proj-123",
		"private_key_id": "k",
		"private_key":    string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email":   "fake@my-proj-123.iam.gserviceaccount.com",
		"client_id":      "1",
		"token_uri":      hang.URL + "/token",
	})
	path := filepath.Join(t.TempDir(), "adc.json")
	if err := os.WriteFile(path, creds, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)
	ui.SetCloudIdentityTimeoutForTest(t, 200*time.Millisecond)

	priv, pemStr := generateECDSATestKey(t)
	assertionKeys(t, priv, pemStr, true)
	ui.SetIAPAudience(tunnelAudience)
	srv, client := testServer(t)
	client.Config.AllowedPrincipals = []string{"user:alice@example.com", "group:ops@example.com"}
	ui.SetAccessCodeForTest(srv, testAccessCode)

	tok := signIAPToken(t, priv, tunnelKid, tunnelAudience, "mallory@example.com", "s", ui.GoogleIAPIssuer, time.Now().Add(5*time.Minute))
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- serveFrom(srv, "203.0.113.20:443", http.MethodGet, "/api/tasks", "", map[string]string{ui.IAPAssertionHeader: tok})
	}()
	select {
	case rec := <-done:
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "group allowlist") {
			t.Fatalf("got %d, want 403 naming the group allowlist: %s", rec.Code, rec.Body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a caller depending on a group: entry was still waiting after 10s for a token endpoint that never answers")
	}
}
