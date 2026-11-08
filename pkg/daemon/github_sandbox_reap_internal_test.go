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

package daemon

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/githubsandbox"
)

// mockGitHubSandboxAPI is the slice of GitHub's REST API a GitHub App
// installed on one organization answers github#sandbox-repo's provider
// with: its installation, installation tokens, and the org's repositories.
type mockGitHubSandboxAPI struct {
	mu    sync.Mutex
	repos map[string]time.Time
	now   time.Time
}

func startMockGitHubSandboxAPI(t *testing.T) (*mockGitHubSandboxAPI, string, releaseTestCredentials) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	api := &mockGitHubSandboxAPI{repos: map[string]time.Time{}, now: time.Now().UTC().Truncate(time.Second)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/app/installations"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 77, "account": map[string]any{"login": "sandbox-org"}}})
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/77/access_tokens":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs-tok", "expires_at": api.now.Add(time.Hour).Format(time.RFC3339)})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/repos/sandbox-org/"):
			name := strings.TrimPrefix(r.URL.Path, "/repos/sandbox-org/")
			api.mu.Lock()
			_, exists := api.repos[name]
			delete(api.repos, name)
			api.mu.Unlock()
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/orgs/sandbox-org/repos":
			type item struct {
				Name      string    `json:"name"`
				CreatedAt time.Time `json:"created_at"`
			}
			api.mu.Lock()
			var list []item
			for name, created := range api.repos {
				list = append(list, item{Name: name, CreatedAt: created})
			}
			api.mu.Unlock()
			_ = json.NewEncoder(w).Encode(list)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	creds := releaseTestCredentials{
		githubsandbox.DefaultAppIDCredential:      "app-77",
		githubsandbox.DefaultPrivateKeyCredential: string(pemBytes),
	}
	return api, srv.Listener.Addr().String(), creds
}

func (a *mockGitHubSandboxAPI) hasRepo(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.repos[name]
	return ok
}

// TestReapCapabilitiesBeforeSweepsSandboxReposWithoutARegistry: the
// grain-sandbox-* repositories github#sandbox-repo leaks are swept through
// githubsandbox.Current(), not the provider registry, so a daemon with no
// registry still reaps them -- only those created before the boundary.
func TestReapCapabilitiesBeforeSweepsSandboxReposWithoutARegistry(t *testing.T) {
	api, host, creds := startMockGitHubSandboxAPI(t)
	t.Cleanup(githubsandbox.Configure(githubsandbox.Config{Host: host, InsecureHTTP: true}))

	api.mu.Lock()
	api.repos["grain-sandbox-stale"] = api.now.Add(-2 * time.Hour)
	api.repos["grain-sandbox-live"] = api.now.Add(-2 * time.Minute)
	api.repos["unrelated"] = api.now.Add(-2 * time.Hour)
	api.mu.Unlock()

	ReapCapabilitiesBefore(context.Background(), nil, creds, api.now.Add(-10*time.Minute))

	if api.hasRepo("grain-sandbox-stale") {
		t.Error("grain-sandbox-stale, created before the boundary, survived a sweep with no registry")
	}
	if !api.hasRepo("grain-sandbox-live") {
		t.Error("grain-sandbox-live, created after the boundary, was deleted")
	}
	if !api.hasRepo("unrelated") {
		t.Error("a repository that is not a sandbox was deleted")
	}
}
