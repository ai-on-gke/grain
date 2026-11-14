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
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/releaseapproval"
	"github.com/ai-on-gke/grain/pkg/secrets/kmsfake"
)

// TestADaemonUnwrapsAKMSWrappedKeyOnce: the daemon's own store, the git
// credential ladder's, the repository tokens' and the release-approval
// key's are four opens of one file store, and with a KMS-wrapped key
// each that loaded its own key was a Cloud KMS decrypt -- three at every
// start, before any reader asked again (docs/test-runs/
// 2026-11-11-secrets-store.md, F1). A start and any number of reads after
// it unwrap the key once.
func TestADaemonUnwrapsAKMSWrappedKeyOnce(t *testing.T) {
	fake := kmsfake.Start(t)
	ctx := context.Background()

	// A deployment with a git credential, a secrets-repository token
	// and a release-approval key in its store, then wrapped.
	setup := t.TempDir()
	store := OpenSecrets(setup)
	creds, err := gitproxy.LoadCredentialSet(filepath.Join(SecretsDir(setup), "github"))
	if err != nil {
		t.Fatal(err)
	}
	if err := creds.SetToken("bot", "ghp_bot"); err != nil {
		t.Fatal(err)
	}
	if _, err := writeSecretsRepoToken(setup, "ghp_secrets_repo"); err != nil {
		t.Fatal(err)
	}
	signer, err := releaseapproval.LoadSigner(setup)
	if err != nil {
		t.Fatal(err)
	}
	wantPub, err := signer.PublicKeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.WrapKeyWithKMS(ctx, "projects/example-project/locations/us/keyRings/grain/cryptoKeys/secrets-key", nil); err != nil {
		t.Fatalf("wrap: %v", err)
	}

	// The same deployment at paths this process has not opened: what a
	// restart finds, with nothing cached.
	dataDir := filepath.Join(t.TempDir(), "data")
	copyTree(t, setup, dataDir)
	tokenFile := filepath.Join(SecretsDir(dataDir), "secrets-repo-token")
	before := fake.Decrypts()

	cfg := Config{DataDir: dataDir}
	started, err := OpenSecretsForConfig(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	MoveLooseCredentialsIntoStore(cfg, started)
	if err := started.Check(); err != nil {
		t.Fatalf("the started daemon's store: %v", err)
	}
	for i := 0; i < 3; i++ {
		set, err := gitproxy.LoadCredentialSet(filepath.Join(SecretsDir(dataDir), "github"))
		if err != nil {
			t.Fatal(err)
		}
		if cred, ok := set.Get("bot"); !ok || cred.Token == nil || *cred.Token != "ghp_bot" {
			t.Fatalf("credential read %d = %+v, %v", i, cred, ok)
		}
		if tok, err := RepoToken(dataDir, "", tokenFile)(ctx); err != nil || tok != "ghp_secrets_repo" {
			t.Fatalf("RepoToken %d = %q, %v", i, tok, err)
		}
		s, err := releaseapproval.LoadSigner(dataDir)
		if err != nil {
			t.Fatalf("approval key read %d: %v", i, err)
		}
		if pub, _ := s.PublicKeyPEM(); pub != wantPub {
			t.Fatalf("approval key read %d is another key", i)
		}
		if v, err := started.Resolve(ctx, gitproxy.CredentialSecretName("bot")+"/token"); err != nil || strings.TrimSpace(v) != "ghp_bot" {
			t.Fatalf("daemon store read %d = %q, %v", i, v, err)
		}
	}
	if got := fake.Decrypts() - before; got != 1 {
		t.Fatalf("a daemon start and 3 rounds of reads made %d KMS decrypts, want 1", got)
	}
}

// copyTree copies the directory src to dst, keeping each file's mode: a
// key file is refused if it is readable by others.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
	if err != nil {
		t.Fatal(err)
	}
}
