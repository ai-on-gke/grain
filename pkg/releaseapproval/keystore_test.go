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

package releaseapproval

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ai-on-gke/grain/pkg/attest"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

func TestLoadSignerMovesTheKeyFileIntoTheStoreAndRollsBack(t *testing.T) {
	dataDir := t.TempDir()
	if err := secrets.New(filepath.Join(dataDir, "secrets")).Check(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataDir, KeyFile)
	legacy, err := attest.LoadOrCreateFileSigner(path)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := PublicKeyPEM(legacy.Public())

	for i := 0; i < 2; i++ { // the second load is a restart with the file already moved
		signer, err := LoadSigner(dataDir)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := signer.PublicKeyPEM(); got != want {
			t.Fatalf("load %d: the signing key changed across the move", i)
		}
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("the plaintext key file is still there")
	}
	if _, err := os.Stat(path + migratedSuffix); err != nil {
		t.Errorf("no rollback copy: %v", err)
	}

	if err := RestoreKeyFile(dataDir); err != nil {
		t.Fatal(err)
	}
	t.Setenv(filesEnv, "files")
	signer, err := LoadSigner(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := signer.PublicKeyPEM(); got != want {
		t.Fatal("the rolled-back file is not the same key")
	}
}

func TestLoadSignerMintsIntoTheStoreWhenThereIsNoKey(t *testing.T) {
	dataDir := t.TempDir()
	if err := secrets.New(filepath.Join(dataDir, "secrets")).Check(); err != nil {
		t.Fatal(err)
	}
	a, err := LoadSigner(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, KeyFile)); err == nil {
		t.Fatal("a fresh key was written to a plaintext file")
	}
	b, err := LoadSigner(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	pa, _ := a.PublicKeyPEM()
	pb, _ := b.PublicKeyPEM()
	if pa != pb {
		t.Fatal("a second load minted a different key")
	}
}
