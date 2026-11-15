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
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/pkgcache"
	"github.com/ai-on-gke/grain/pkg/sandboxaccess"
)

func TestSecretsCAStoreImportsTheLegacyFileOnceAndKeepsARollbackCopy(t *testing.T) {
	dataDir := t.TempDir()
	store := OpenSecrets(dataDir)
	ctx := context.Background()
	legacy := filepath.Join(dataDir, "sandbox-access-ca.pem")
	ca, err := sandboxaccess.NewCA("grain", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, ca.PEM(), 0o600); err != nil {
		t.Fatal(err)
	}

	cas := sandboxAccessCAStore(Config{DataDir: dataDir, caSecrets: store}, legacy)
	got, err := sandboxaccess.LoadOrCreateCA(ctx, cas, "grain", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.PEM(), ca.PEM()) {
		t.Fatal("the imported CA is not the one in the legacy file")
	}
	if _, err := os.Stat(legacy); err == nil {
		t.Error("the legacy CA file is still there after importing it")
	}
	if _, err := os.Stat(legacy + gitproxy.MigratedSuffix); err != nil {
		t.Errorf("no rollback copy: %v", err)
	}

	// A restart: the store answers, and the same CA comes back.
	again, err := sandboxaccess.LoadOrCreateCA(ctx, cas, "grain", time.Now())
	if err != nil || !bytes.Equal(again.PEM(), ca.PEM()) {
		t.Fatalf("after a restart: %v, same CA %v", err, err == nil && bytes.Equal(again.PEM(), ca.PEM()))
	}

	// With no store, the file store is what is used, as before.
	if _, ok := sandboxAccessCAStore(Config{DataDir: dataDir}, legacy).(sandboxaccess.FileCAStore); !ok {
		t.Error("a deployment with no store did not keep the file CA store")
	}

	// Rollback writes it back.
	t.Setenv(gitproxy.CredentialsInFilesEnv, "files")
	MoveLooseCredentialsIntoStore(Config{DataDir: dataDir}, store)
	data, err := os.ReadFile(legacy)
	if err != nil || !bytes.Equal(data, ca.PEM()) {
		t.Fatalf("rollback: %s = %v", legacy, err)
	}
	if _, err := (SecretsCAStore{Store: store, Secret: SandboxAccessCASecretName}).Load(ctx); !errors.Is(err, sandboxaccess.ErrNoCA) {
		t.Errorf("the store still holds the CA after a rollback: %v", err)
	}
}

func TestPackageCacheCAIsKeptInTheStoreAndRenewedNearExpiry(t *testing.T) {
	dataDir := t.TempDir()
	store := OpenSecrets(dataDir)
	legacy := filepath.Join(dataDir, "package-cache-ca.pem")
	now := time.Now()

	first, err := loadPackageCacheCA(store, legacy, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); err == nil {
		t.Error("a fresh package cache CA was written to a plaintext file")
	}
	second, err := loadPackageCacheCA(store, legacy, now)
	if err != nil || !bytes.Equal(first.PEM(), second.PEM()) {
		t.Fatalf("a restart did not load the stored CA: %v", err)
	}
	parsed, err := pkgcache.ParseCA(first.PEM())
	if err != nil {
		t.Fatal(err)
	}
	var later time.Time
	for later = now; !parsed.NeedsRenewal(later); later = later.Add(30 * 24 * time.Hour) {
	}
	renewed, err := loadPackageCacheCA(store, legacy, later)
	if err != nil || bytes.Equal(renewed.PEM(), first.PEM()) {
		t.Fatalf("a CA inside its renewal window was not replaced: %v", err)
	}
}
