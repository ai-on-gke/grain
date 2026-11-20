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

//go:build unix

package daemon

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/releaseapproval"
)

// A start that finds hostile files where its rollback copies should be --
// a symlink out of the data directory, a FIFO, a copy it cannot delete --
// neither follows, blocks on, nor fails over any of them.
func TestAStartNeitherFollowsNorBlocksOnHostileRollbackCopies(t *testing.T) {
	dataDir := t.TempDir()
	store := OpenSecrets(dataDir)
	const keyPEM = "-----BEGIN PRIVATE KEY-----\nwalk\n-----END PRIVATE KEY-----\n"
	const bundle = "-----BEGIN CERTIFICATE-----\nwalk\n-----END CERTIFICATE-----\n"
	if err := store.Set(releaseapproval.KeySecret, releaseapproval.KeySecretKey, []byte(keyPEM)); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(PackageCacheCASecretName, caBundleKey, []byte(bundle)); err != nil {
		t.Fatal(err)
	}

	// The key's copy is a symlink to a file outside the data directory
	// that holds the stored key, so reading through it would match.
	outside := filepath.Join(t.TempDir(), "not-grains.pem")
	if err := os.WriteFile(outside, []byte(keyPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	keyCopy := filepath.Join(dataDir, releaseapproval.KeyFile) + gitproxy.MigratedSuffix
	if err := os.Symlink(outside, keyCopy); err != nil {
		t.Fatal(err)
	}
	// The CA bundle's copy is a FIFO nobody writes to.
	caCopy := filepath.Join(dataDir, "package-cache-ca.pem") + gitproxy.MigratedSuffix
	if err := syscall.Mkfifo(caCopy, 0o600); err != nil {
		t.Fatal(err)
	}
	// The relay token's copy sits in a directory the daemon cannot write.
	relayDir := filepath.Join(SecretsDir(dataDir), "relay")
	if err := os.MkdirAll(relayDir, 0o700); err != nil {
		t.Fatal(err)
	}
	relayCopy := filepath.Join(relayDir, "token") + gitproxy.MigratedSuffix
	if err := os.WriteFile(relayCopy, []byte("relay-tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(relayDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(relayDir, 0o700) })
	t.Setenv(gitproxy.CredentialsInFilesEnv, "")

	done := make(chan struct{})
	go func() {
		defer close(done)
		MoveLooseCredentialsIntoStore(Config{DataDir: dataDir}, store)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("MoveLooseCredentialsIntoStore had not returned after 5s")
	}

	if fi, err := os.Lstat(keyCopy); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the symlinked key copy was not left as it was: %v, %v", fi, err)
	}
	if b, err := os.ReadFile(outside); err != nil || string(b) != keyPEM {
		t.Errorf("the symlink's target changed: %q, %v", b, err)
	}
	if fi, err := os.Lstat(caCopy); err != nil || fi.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("the FIFO was not left as it was: %v, %v", fi, err)
	}
	if os.Geteuid() != 0 {
		if _, err := os.Stat(relayCopy); err != nil {
			t.Errorf("the relay copy in a read-only directory is gone: %v", err)
		}
	}
}
