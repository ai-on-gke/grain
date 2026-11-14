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
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/ai-on-gke/grain/pkg/attest"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

// The signing key is kept in the secrets store beside dataDir
// (<dataDir>/secrets/secrets.enc), as secret KeySecret, key KeySecretKey,
// rather than as <dataDir>/release-approval-key.pem
// (design/design/secrets-key-protection.design.md §5, Phase 3). A
// deployment with no store key there keeps the file, as before.
const (
	KeySecret    = "release-approval-key"
	KeySecretKey = "key.pem"
	// migratedSuffix matches gitproxy.MigratedSuffix: the copy a moved
	// file is kept as until the daemon's next start finds the store
	// holding it and deletes it (pkg/daemon deleteMigratedCopies).
	migratedSuffix = ".migrated"
	// filesEnv matches gitproxy.CredentialsInFilesEnv, the rollback switch.
	filesEnv = "GRAIN_GITHUB_CREDENTIALS"
)

// keyStore is the file store beside dataDir, or nil when it has no key or
// files were asked for. It never mints the store's key.
func keyStore(dataDir string) *secrets.Store {
	if os.Getenv(filesEnv) == "files" {
		return nil
	}
	return storeIn(dataDir)
}

func storeIn(dataDir string) *secrets.Store {
	cfg := secrets.Config{
		File:    filepath.Join(dataDir, "secrets", secrets.DefaultFileName),
		KeyFile: filepath.Join(dataDir, "secrets", secrets.DefaultKeyFileName),
	}
	if _, err := os.Stat(cfg.KeyFile); err != nil {
		return nil
	}
	// Shared, so a KMS-wrapped key is unwrapped once per process rather
	// than once per signature.
	return secrets.OpenShared(cfg)
}

// loadStoredSigner is the signer the store holds, importing the key file
// the first time (read back, then renamed to <file>.migrated) and minting
// a key into the store when there is neither.
func loadStoredSigner(store *secrets.Store, dataDir string) (*attest.FileSigner, error) {
	ctx := context.Background()
	has, err := store.Has(KeySecret, KeySecretKey)
	if err != nil {
		return nil, err
	}
	if has {
		v, err := store.Resolve(ctx, KeySecret+"/"+KeySecretKey)
		if err != nil {
			return nil, err
		}
		return parseSigner([]byte(v))
	}
	path := filepath.Join(dataDir, KeyFile)
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		signer, err := parseSigner(data)
		if err != nil {
			return nil, err
		}
		if err := store.Set(KeySecret, KeySecretKey, data); err != nil {
			log.Printf("release approval: importing %s into the secrets store: %v -- still reading the file", path, err)
			return signer, nil
		}
		if got, err := store.Resolve(ctx, KeySecret+"/"+KeySecretKey); err != nil || got != string(data) {
			log.Printf("release approval: %s did not read back from the secrets store -- still reading the file", path)
			return signer, nil
		}
		if err := os.Rename(path, path+migratedSuffix); err != nil {
			log.Printf("release approval: renaming %s after importing it: %v", path, err)
			return signer, nil
		}
		log.Printf("release approval: moved the signing key %s into secret %s (kept as %s for a rollback)",
			path, KeySecret, path+migratedSuffix)
		return signer, nil
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := store.Set(KeySecret, KeySecretKey, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})); err != nil {
		return nil, err
	}
	return attest.NewFileSigner(key)
}

func parseSigner(data []byte) (*attest.FileSigner, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("the stored key holds no PEM block")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("the stored key: %w", err)
	}
	return attest.NewFileSigner(key)
}

// RestoreKeyFile writes the store's signing key back to
// <dataDir>/release-approval-key.pem and drops the store's copy -- the
// rollback an older build needs. A store holding none does nothing.
func RestoreKeyFile(dataDir string) error {
	store := storeIn(dataDir)
	if store == nil {
		return nil
	}
	has, err := store.Has(KeySecret, KeySecretKey)
	if err != nil || !has {
		return err
	}
	v, err := store.Resolve(context.Background(), KeySecret+"/"+KeySecretKey)
	if err != nil {
		return err
	}
	path := filepath.Join(dataDir, KeyFile)
	if err := os.WriteFile(path, []byte(v), 0o600); err != nil {
		return err
	}
	_ = os.Remove(path + migratedSuffix)
	return store.DeleteKey(KeySecret, KeySecretKey)
}
