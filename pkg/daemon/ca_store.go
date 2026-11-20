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
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"time"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/pkgcache"
	"github.com/ai-on-gke/grain/pkg/sandboxaccess"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

// The two CA bundles the daemon alone reads -- certificate and private key
// -- kept in the secrets store rather than as <data-dir>/*.pem files
// (design/design/secrets-key-protection.design.md §5, Phase 3). Either key
// lets someone on-path impersonate TLS to sandboxes.
const (
	SandboxAccessCASecretName = "sandbox-access-ca"
	PackageCacheCASecretName  = "package-cache-ca"
	caBundleKey               = "bundle.pem"
)

// SecretsCAStore is a sandboxaccess.CAStore over the secrets store: the
// bundle is secret Secret, key bundle.pem. Legacy is the file the bundle
// was kept in before; the first Load that finds the store empty and that
// file present imports it, reads it back, and renames the file to
// <file>.migrated, which the next start deletes (deleteMigratedCopies).
type SecretsCAStore struct {
	Store  *secrets.Store
	Secret string
	Legacy string
}

// Load implements sandboxaccess.CAStore.
func (s SecretsCAStore) Load(ctx context.Context) ([]byte, error) {
	has, err := s.Store.Has(s.Secret, caBundleKey)
	if err != nil {
		return nil, err
	}
	if has {
		v, err := s.Store.Resolve(ctx, s.Secret+"/"+caBundleKey)
		if err != nil {
			return nil, err
		}
		return []byte(v), nil
	}
	if s.Legacy == "" {
		return nil, sandboxaccess.ErrNoCA
	}
	data, err := os.ReadFile(s.Legacy)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, sandboxaccess.ErrNoCA
	}
	if err != nil {
		return nil, err
	}
	if err := setVerified(s.Store, s.Secret, caBundleKey, string(data)); err != nil {
		log.Printf("grain: importing %s into the secrets store: %v -- still reading the file", s.Legacy, err)
		return data, nil
	}
	if err := os.Rename(s.Legacy, s.Legacy+gitproxy.MigratedSuffix); err != nil {
		log.Printf("grain: renaming %s after importing it: %v", s.Legacy, err)
		return data, nil
	}
	log.Printf("grain: moved the CA bundle %s into secret %s (kept as %s until the next start "+
		"finds the store holding it)", s.Legacy, s.Secret, s.Legacy+gitproxy.MigratedSuffix)
	return data, nil
}

// Save implements sandboxaccess.CAStore.
func (s SecretsCAStore) Save(_ context.Context, bundle []byte) error {
	return s.Store.Set(s.Secret, caBundleKey, bundle)
}

// sandboxAccessCAStore is where the sandbox-access CA is kept: Secret
// Manager when -sandbox-access-ca-secret names one, else the secrets store
// (importing <data-dir>/sandbox-access-ca.pem), else that file.
func sandboxAccessCAStore(cfg Config, legacy string) sandboxaccess.CAStore {
	switch {
	case cfg.SandboxAccessCASecret != "":
		return sandboxaccess.SecretManagerStore{Secret: cfg.SandboxAccessCASecret}
	case cfg.caSecrets != nil:
		return SecretsCAStore{Store: cfg.caSecrets, Secret: SandboxAccessCASecretName, Legacy: legacy}
	}
	return sandboxaccess.FileCAStore{Path: legacy}
}

// loadPackageCacheCA is the package cache's Root CA, from the secrets store
// (importing legacy, <data-dir>/package-cache-ca.pem) and regenerated into
// it once missing, unreadable or within its renewal window of expiry.
func loadPackageCacheCA(store *secrets.Store, legacy string, now time.Time) (*pkgcache.CA, error) {
	cas := SecretsCAStore{Store: store, Secret: PackageCacheCASecretName, Legacy: legacy}
	bundle, err := cas.Load(context.Background())
	switch {
	case err == nil:
		ca, perr := pkgcache.ParseCA(bundle)
		if perr == nil && !ca.NeedsRenewal(now) {
			return ca, nil
		}
		if perr != nil {
			log.Printf("grain daemon: the stored package cache internal CA does not parse: %v -- generating a new CA", perr)
		}
	case !errors.Is(err, sandboxaccess.ErrNoCA):
		return nil, fmt.Errorf("reading the package cache internal CA from the secrets store: %w", err)
	}
	ca, err := pkgcache.NewCA()
	if err != nil {
		return nil, err
	}
	if err := cas.Save(context.Background(), ca.PEM()); err != nil {
		return nil, fmt.Errorf("saving the package cache internal CA to the secrets store: %w", err)
	}
	return ca, nil
}
