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

package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/registry"
)

type fakeSecretWriter struct {
	mu      sync.Mutex
	secrets map[string][]byte
}

func (f *fakeSecretWriter) PutSecret(_ context.Context, entryName, secretSuffix string, payload []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.secrets == nil {
		f.secrets = make(map[string][]byte)
	}
	name := fmt.Sprintf("projects/test-proj/secrets/grain-inst-%s-%s/versions/1", entryName, secretSuffix)
	f.secrets[entryName+"/"+secretSuffix] = append([]byte(nil), payload...)
	return name, nil
}

func TestGitHubConfigReposProvisionDeployKeyAndCleanup(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	pkcs8Bytes, err := x509.MarshalPKCS8PrivateKey(privKey)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8Bytes})

	var (
		mu             sync.Mutex
		repos          = make(map[string]map[string]any)
		deployKeys     = make(map[string][]map[string]any)
		archived       = make(map[string]bool)
		jwtExchanges   int
		lastAuthHeader string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		lastAuthHeader = r.Header.Get("Authorization")
		path := r.URL.Path

		// 1. GitHub App installation token exchange.
		if path == "/app/installations/9988/access_tokens" && r.Method == http.MethodPost {
			if !strings.HasPrefix(lastAuthHeader, "Bearer eyJ") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			jwtExchanges++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      "ghs_test_installation_token",
				"expires_at": time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339),
			})
			return
		}

		if lastAuthHeader != "Bearer ghs_test_installation_token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		// 2. Create repository in org: POST /orgs/{org}/repos
		if path == "/orgs/acme-configs/repos" && r.Method == http.MethodPost {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			name, _ := body["name"].(string)
			fullName := "acme-configs/" + name
			body["ssh_url"] = fmt.Sprintf("git@github.com:%s.git", fullName)
			repos[fullName] = body
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(body)
			return
		}

		// 3. Get / Patch / Delete repository: /repos/{org}/{repo}
		if strings.HasPrefix(path, "/repos/acme-configs/") {
			rest := strings.TrimPrefix(path, "/repos/acme-configs/")
			parts := strings.Split(rest, "/")
			repoName := parts[0]
			fullName := "acme-configs/" + repoName

			if len(parts) == 1 {
				switch r.Method {
				case http.MethodGet:
					repo, ok := repos[fullName]
					if !ok {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					_ = json.NewEncoder(w).Encode(repo)
					return
				case http.MethodPatch:
					archived[fullName] = true
					_ = json.NewEncoder(w).Encode(map[string]any{"archived": true})
					return
				case http.MethodDelete:
					delete(repos, fullName)
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}

			// 4. Deploy keys: /repos/{org}/{repo}/keys
			if len(parts) == 2 && parts[1] == "keys" {
				switch r.Method {
				case http.MethodGet:
					_ = json.NewEncoder(w).Encode(deployKeys[fullName])
					return
				case http.MethodPost:
					var keyBody map[string]any
					_ = json.NewDecoder(r.Body).Decode(&keyBody)
					keyBody["id"] = len(deployKeys[fullName]) + 1
					deployKeys[fullName] = append(deployKeys[fullName], keyBody)
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(keyBody)
					return
				}
			}
		}

		http.NotFound(w, r)
	}))
	defer srv.Close()

	secWriter := &fakeSecretWriter{}
	cfg := registry.ConfigReposConfig{
		Provider:      "github",
		GitHubOrg:     "acme-configs",
		RepoPrefix:    "grain-cfg-",
		AppID:         112233,
		Installation:  9988,
		PrivateKeyPEM: string(pemBytes),
		APIBaseURL:    srv.URL,
	}

	prov, err := NewGitHubConfigRepos(cfg,
		WithHTTPClient(srv.Client()),
		WithSecretWriter(secWriter),
	)
	if err != nil {
		t.Fatalf("NewGitHubConfigRepos: %v", err)
	}

	// 1. CheckRepoScope rejects repositories outside `grain-cfg-`.
	if err := prov.CheckRepoScope("other-repo"); !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("expected ErrOutOfScope for non-prefixed repo, got %v", err)
	}
	if err := prov.CheckRepoScope("grain-cfg-alice-vm"); err != nil {
		t.Fatalf("CheckRepoScope valid: %v", err)
	}

	// 2. EnsureRepo creates private repo `acme-configs/grain-cfg-alice-vm`, writes private key to SecretWriter,
	// and registers a read-write deploy key (`read_only: false`).
	ctx := context.Background()
	bundle, err := prov.EnsureRepo(ctx, "alice-vm", "alice@example.com")
	if err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}
	if bundle.RepoURL != "git@github.com:acme-configs/grain-cfg-alice-vm.git" {
		t.Fatalf("unexpected RepoURL: %q", bundle.RepoURL)
	}
	if !strings.Contains(bundle.DeployKey, "BEGIN PRIVATE KEY") {
		t.Fatalf("expected private key PEM in bundle.DeployKey, got %q", bundle.DeployKey)
	}

	secWriter.mu.Lock()
	savedSecret := string(secWriter.secrets["alice-vm/config-repo-key"])
	secWriter.mu.Unlock()
	if savedSecret != bundle.DeployKey {
		t.Fatalf("expected SecretWriter to receive private deploy key under alice-vm/config-repo-key")
	}

	mu.Lock()
	repoObj := repos["acme-configs/grain-cfg-alice-vm"]
	keys := deployKeys["acme-configs/grain-cfg-alice-vm"]
	exchanges := jwtExchanges
	mu.Unlock()

	if exchanges != 1 {
		t.Fatalf("expected 1 GitHub App JWT exchange, got %d", exchanges)
	}
	if repoObj["private"] != true {
		t.Fatalf("expected repository to be private, got %+v", repoObj)
	}
	if len(keys) != 1 || keys[0]["read_only"] != false || !strings.HasPrefix(keys[0]["key"].(string), "ssh-ed25519 ") {
		t.Fatalf("unexpected registered deploy keys: %+v", keys)
	}

	// 3. ArchiveRepo deletes the repository on final purge.
	if err := prov.ArchiveRepo(ctx, "alice-vm"); err != nil {
		t.Fatalf("ArchiveRepo: %v", err)
	}
	mu.Lock()
	_, stillExists := repos["acme-configs/grain-cfg-alice-vm"]
	mu.Unlock()
	if stillExists {
		t.Fatal("expected repository acme-configs/grain-cfg-alice-vm to be deleted by ArchiveRepo")
	}
}
