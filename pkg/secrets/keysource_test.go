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

package secrets

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// countingSource is a FileKeySource that counts its loads.
type countingSource struct {
	FileKeySource
	loads atomic.Int32
}

func (c *countingSource) Load(ctx context.Context) (Key, error) {
	c.loads.Add(1)
	return c.FileKeySource.Load(ctx)
}

func TestStoreReadsTheKeyOnceAcrossManyResolves(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, DefaultKeyFileName)
	seedSecret(t, New(dir), "db", map[string]string{"password": "hunter2"})

	src := &countingSource{FileKeySource: FileKeySource{Path: keyFile}}
	store := Open(Config{File: filepath.Join(dir, DefaultFileName), KeyFile: keyFile, KeySource: src})
	for i := 0; i < 50; i++ {
		if got, err := store.Resolve(context.Background(), "db/password"); err != nil || got != "hunter2" {
			t.Fatalf("Resolve #%d = %q, %v", i, got, err)
		}
	}
	if err := store.Set("db", "user", []byte("app")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PublicKey(); err != nil {
		t.Fatal(err)
	}
	if n := src.loads.Load(); n != 1 {
		t.Fatalf("key loaded %d times, want 1", n)
	}

	// The key is in memory now: the file going away does not stop the
	// store, which is what makes a KMS-wrapped source cost one unwrap.
	if err := os.Remove(keyFile); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(context.Background(), "db/password"); err != nil {
		t.Fatalf("Resolve after the file went: %v", err)
	}
}

func TestImportKeyReloadsTheKey(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, DefaultKeyFileName)
	src := &countingSource{FileKeySource: FileKeySource{Path: keyFile}}
	store := Open(Config{File: filepath.Join(dir, DefaultFileName), KeyFile: keyFile, KeySource: src})
	before, err := store.PublicKey()
	if err != nil {
		t.Fatal(err)
	}
	k, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ImportKey(k); err != nil {
		t.Fatal(err)
	}
	after, err := store.PublicKey()
	if err != nil {
		t.Fatal(err)
	}
	if after != k.Public() || after == before {
		t.Fatalf("PublicKey after import = %s, want %s (was %s)", after, k.Public(), before)
	}
	retired, err := RetiredKeys(keyFile)
	if err != nil || len(retired) != 1 || retired[0].Public != before {
		t.Fatalf("RetiredKeys = %+v, %v; want the one key import replaced (%s)", retired, err, before)
	}
}

func TestStoreReloadsAKeyAnotherProcessInstalled(t *testing.T) {
	dir := t.TempDir()
	daemon := New(dir)
	if _, err := daemon.PublicKey(); err != nil { // cache the minted key
		t.Fatal(err)
	}
	// Another process (the CLI, say) installs a different key and seals
	// the file to it.
	other := New(dir)
	k, _ := GenerateKey()
	if err := other.ImportKey(k); err != nil {
		t.Fatal(err)
	}
	seedSecret(t, other, "db", map[string]string{"password": "p"})
	if got, err := daemon.Resolve(context.Background(), "db/password"); err != nil || got != "p" {
		t.Fatalf("Resolve = %q, %v; want the stale cache to be reloaded", got, err)
	}
}

func TestDeleteRetiredKeyRefusesTheLiveKeyAndOtherFiles(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, DefaultKeyFileName)
	archive := keyFile + RetiredKeyMarker + "20261110-052600"
	for _, p := range []string{keyFile, archive, filepath.Join(dir, "other")} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{DefaultKeyFileName, "other", "../" + filepath.Base(archive), DefaultKeyFileName + RetiredKeyMarker} {
		if err := DeleteRetiredKey(keyFile, name); err == nil {
			t.Errorf("DeleteRetiredKey(%q) succeeded, want a refusal", name)
		}
	}
	if err := DeleteRetiredKey(keyFile, filepath.Base(archive)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Fatalf("archive still there: %v", err)
	}
	if _, err := os.Stat(keyFile); err != nil {
		t.Fatalf("live key gone: %v", err)
	}
}
