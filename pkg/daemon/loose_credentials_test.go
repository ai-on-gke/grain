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
	"os"
	"path/filepath"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/gcpkey"
	"github.com/ai-on-gke/grain/pkg/capability/githubsandbox"
	"github.com/ai-on-gke/grain/pkg/capability/imagesigning"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/releaseapproval"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

func TestMoveLooseCredentialsIntoStoreImportsEachFileOnce(t *testing.T) {
	dataDir := t.TempDir()
	store := OpenSecrets(dataDir)
	ghDir := filepath.Join(SecretsDir(dataDir), "github")
	relayDir := filepath.Join(SecretsDir(dataDir), "relay")
	for _, d := range []string{ghDir, relayDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(ghDir, "bot.token"), []byte("ghp_bot\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(relayDir, "token"), []byte("relay-tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hubFile := filepath.Join(t.TempDir(), "hub-token")
	if err := os.WriteFile(hubFile, []byte("hub-tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{DataDir: dataDir, HubTokenFile: hubFile}

	for i := 0; i < 2; i++ { // a second start finds everything already moved
		MoveLooseCredentialsIntoStore(cfg, store)
	}

	ctx := context.Background()
	for name, want := range map[string]string{
		gitproxy.CredentialSecretName("bot") + "/token": "ghp_bot\n",
		"relay/token": "relay-tok",
		secrets.HubRegistrationTokenSecret + "/" + secrets.AgentCredentialKey: "hub-tok",
	} {
		if got, err := store.Resolve(ctx, name); err != nil || got != want {
			t.Errorf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
	for _, path := range []string{filepath.Join(ghDir, "bot.token"), filepath.Join(relayDir, "token")} {
		if _, err := os.Stat(path); err == nil {
			t.Errorf("%s is still there", path)
		}
		if _, err := os.Stat(path + gitproxy.MigratedSuffix); err != nil {
			t.Errorf("%s was not kept as a rollback copy: %v", path, err)
		}
	}
	if _, err := os.Stat(hubFile); err != nil {
		t.Errorf("the operator's own -hub-token-file was touched: %v", err)
	}

	// Rollback: the git credential is written back out of the store.
	t.Setenv(gitproxy.CredentialsInFilesEnv, "files")
	MoveLooseCredentialsIntoStore(cfg, store)
	if b, err := os.ReadFile(filepath.Join(ghDir, "bot.token")); err != nil || string(b) != "ghp_bot\n" {
		t.Fatalf("rollback: bot.token = %q, %v", b, err)
	}
}

func TestRepoTokensAreKeptInTheStoreAndServedToGitFromThere(t *testing.T) {
	dataDir := t.TempDir()
	store := OpenSecrets(dataDir)
	ctx := context.Background()

	path, err := writeStateRepoToken(dataDir, " ghp_state \n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("writeStateRepoToken left a plaintext token file")
	}
	if tok, err := RepoToken(dataDir, "", path)(ctx); err != nil || tok != "ghp_state" {
		t.Fatalf("RepoToken = %q, %v", tok, err)
	}

	// A file an older build wrote is imported on first use.
	legacy := filepath.Join(SecretsDir(dataDir), "secrets-repo-token")
	if err := os.WriteFile(legacy, []byte("ghp_secrets\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if tok, err := RepoToken(dataDir, "", legacy)(ctx); err != nil || tok != "ghp_secrets" {
		t.Fatalf("RepoToken(legacy) = %q, %v", tok, err)
	}
	if _, err := os.Stat(legacy + gitproxy.MigratedSuffix); err != nil {
		t.Errorf("the legacy token was not kept as a rollback copy: %v", err)
	}
	if v, err := store.Resolve(ctx, "secrets-repo-token/token"); err != nil || v != "ghp_secrets" {
		t.Errorf("stored secrets-repo-token = %q, %v", v, err)
	}

	// Rollback writes both back to the paths the settings name.
	t.Setenv(gitproxy.CredentialsInFilesEnv, "files")
	MoveLooseCredentialsIntoStore(Config{DataDir: dataDir}, store)
	for p, want := range map[string]string{path: "ghp_state\n", legacy: "ghp_secrets\n"} {
		if b, err := os.ReadFile(p); err != nil || string(b) != want {
			t.Errorf("rollback: %s = %q, %v", p, b, err)
		}
	}
	if tok, err := RepoToken(dataDir, "", path)(ctx); err != nil || tok != "ghp_state" {
		t.Fatalf("RepoToken after a rollback = %q, %v", tok, err)
	}
}

// TestGrainHeldCoversEverySecretGrainKeepsItsOwnKeysIn keeps
// secrets.GrainHeld, which request_secret refuses, in step with the names
// grain's own credentials are kept under: those this package moved into
// the store, and those the agent frameworks, the GCP minter, image
// signing, github-sandbox and release secrets already kept there.
func TestGrainHeldCoversEverySecretGrainKeepsItsOwnKeysIn(t *testing.T) {
	releaseSecret, _ := ci.ReleaseSecretAddress("acme/widgets", "DOCKERHUB_TOKEN")
	names := append([]string{
		gitproxy.CredentialSecretName("bot"),
		SandboxAccessCASecretName,
		PackageCacheCASecretName,
		releaseapproval.KeySecret,
		secrets.HubRegistrationTokenSecret,
		"relay",
		secrets.GeminiAPIKeySecret,
		secrets.ClaudeOAuthTokenSecret,
		secrets.OpenAIAPIKeySecret,
		secrets.AgentBackendCredentialSecret("vertex"),
		gcpkey.DefaultMinterCredential,
		model.GCPKeyMinterCredential("research"),
		model.GCPKey{Name: "research"}.EffectiveMinterCredential(),
		imagesigning.KeyCredential,
		imagesigning.PasswordCredential,
		githubsandbox.DefaultAppIDCredential,
		githubsandbox.DefaultPrivateKeyCredential,
		releaseSecret,
	}, repoTokenNames...)
	for _, name := range names {
		if !secrets.GrainHeld(name) {
			t.Errorf("secrets.GrainHeld(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"stripe-api-key", "github-app-token", "deploy-bot/token", "gcp-key"} {
		if secrets.GrainHeld(name) {
			t.Errorf("secrets.GrainHeld(%q) = true, want false", name)
		}
	}
}
