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

package gitproxy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

// storeBackedDir lays out <data-dir>/secrets with a minted store key and
// an empty secrets/github beside it, the shape a deployment has.
func storeBackedDir(t *testing.T) (string, *secrets.Store) {
	t.Helper()
	secretsDir := filepath.Join(t.TempDir(), "secrets")
	store := secrets.New(secretsDir)
	if err := store.Check(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(secretsDir, "github")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir, store
}

func mustLoad(t *testing.T, dir string) *CredentialSet {
	t.Helper()
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func resolve(t *testing.T, store *secrets.Store, name string) (string, bool) {
	t.Helper()
	v, err := store.Resolve(context.Background(), name)
	if err != nil {
		return "", false
	}
	return v, true
}

func TestACredentialSetWithNoStoreKeyBesideItStaysOnFiles(t *testing.T) {
	dir := t.TempDir()
	set := mustLoad(t, dir)
	if set.InStore() {
		t.Fatal("a directory with no secrets.key beside it opened a store")
	}
	if err := set.SetToken("bot", "ghp_files"); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(dir, "bot.token")) {
		t.Fatal("SetToken on a file-only set wrote no file")
	}
	if moved, err := set.MigrateFilesToStore(); err != nil || len(moved) != 0 {
		t.Fatalf("migrating with no store: %v, %v", moved, err)
	}
}

func TestMigrateFilesToStoreImportsVerifiesAndRenamesEachFile(t *testing.T) {
	dir, store := storeBackedDir(t)
	writeFile(t, filepath.Join(dir, "bot.token"), "ghp_bot\n")
	writeAppCredential(t, dir, "ci-app")
	appBytes, _ := os.ReadFile(filepath.Join(dir, "ci-app.app.json"))
	writeFile(t, filepath.Join(dir, "deploy.ssh.json"), `{"host":"git.example.com"}`+"\n")
	writeFile(t, filepath.Join(dir, "bot.host.json"), `{"host":"github.com"}`+"\n")
	writeFile(t, filepath.Join(dir, "me.ambient.json"), `{"source":"git"}`+"\n")

	set := mustLoad(t, dir)
	if !set.InStore() {
		t.Fatal("a directory with a secrets.key beside it did not open the store")
	}
	moved, err := set.MigrateFilesToStore()
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 3 {
		t.Fatalf("moved %+v, want the three secret-bearing files", moved)
	}
	for _, f := range []string{"bot.token", "ci-app.app.json", "deploy.ssh.json"} {
		if exists(filepath.Join(dir, f)) {
			t.Errorf("%s is still there after migrating", f)
		}
		if !exists(filepath.Join(dir, f+MigratedSuffix)) {
			t.Errorf("%s was not kept as %s", f, f+MigratedSuffix)
		}
	}
	for _, f := range []string{"bot.host.json", "me.ambient.json"} {
		if !exists(filepath.Join(dir, f)) {
			t.Errorf("%s holds no secret and should have stayed a file", f)
		}
	}
	if v, ok := resolve(t, store, "github-credential-bot/token"); !ok || v != "ghp_bot\n" {
		t.Errorf("store holds %q for bot, want the file's bytes", v)
	}
	if v, ok := resolve(t, store, "github-credential-ci-app/app.json"); !ok || v != string(appBytes) {
		t.Errorf("store's ci-app App credential is not the file's bytes")
	}

	cred, ok := set.Get("bot")
	if !ok || cred.Token == nil || *cred.Token != "ghp_bot" {
		t.Fatalf("Get(bot) after migrating: %+v, %v", cred, ok)
	}
	if !set.IsApp("ci-app") || !set.IsSSH("deploy") {
		t.Error("the migrated App and SSH credentials are no longer recognised")
	}
	names := strings.Join(set.Names(), ",")
	if names != "bot,ci-app,deploy,me" {
		t.Errorf("Names() = %s", names)
	}
}

func TestARestartWithAlreadyMigratedFilesMovesNothingAndStillReads(t *testing.T) {
	dir, _ := storeBackedDir(t)
	writeFile(t, filepath.Join(dir, "bot.token"), "ghp_bot\n")
	if _, err := mustLoad(t, dir).MigrateFilesToStore(); err != nil {
		t.Fatal(err)
	}

	restarted := mustLoad(t, dir)
	moved, err := restarted.MigrateFilesToStore()
	if err != nil || len(moved) != 0 {
		t.Fatalf("second start moved %+v, %v", moved, err)
	}
	if !exists(filepath.Join(dir, "bot.token"+MigratedSuffix)) {
		t.Error("the rollback copy went missing on a restart")
	}
	cred, ok := restarted.Get("bot")
	if !ok || cred.Token == nil || *cred.Token != "ghp_bot" {
		t.Fatalf("Get(bot) after a restart: %+v, %v", cred, ok)
	}
}

func TestAFileThatComesBackAfterMigratingIsTheNewerWord(t *testing.T) {
	dir, store := storeBackedDir(t)
	writeFile(t, filepath.Join(dir, "bot.token"), "ghp_old\n")
	if _, err := mustLoad(t, dir).MigrateFilesToStore(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "bot.token"), "ghp_new\n")

	set := mustLoad(t, dir)
	if moved, err := set.MigrateFilesToStore(); err != nil || len(moved) != 1 {
		t.Fatalf("re-seeded file: moved %+v, %v", moved, err)
	}
	if v, _ := resolve(t, store, "github-credential-bot/token"); v != "ghp_new\n" {
		t.Errorf("store holds %q, want the re-seeded value", v)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "bot.token"+MigratedSuffix)); string(b) != "ghp_new\n" {
		t.Errorf("rollback copy holds %q, want the re-seeded value", b)
	}
}

func TestSetTokenAndRemoveOnAStoreBackedSetLeaveNoPlaintext(t *testing.T) {
	dir, store := storeBackedDir(t)
	writeFile(t, filepath.Join(dir, "bot.token"), "ghp_old\n")
	set := mustLoad(t, dir)
	if _, err := set.MigrateFilesToStore(); err != nil {
		t.Fatal(err)
	}
	if err := set.SetToken("extra", "ghp_extra"); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(dir, "extra.token")) {
		t.Fatal("SetToken on a store-backed set wrote a plaintext file")
	}
	if v, _ := resolve(t, store, "github-credential-extra/token"); v != "ghp_extra\n" {
		t.Fatalf("store holds %q for extra", v)
	}
	if cred, ok := set.Get("extra"); !ok || cred.Token == nil || *cred.Token != "ghp_extra" {
		t.Fatalf("Get(extra): %+v, %v", cred, ok)
	}

	if err := set.Remove("bot"); err != nil {
		t.Fatal(err)
	}
	if _, ok := resolve(t, store, "github-credential-bot/token"); ok {
		t.Error("Remove left the credential in the store")
	}
	if exists(filepath.Join(dir, "bot.token"+MigratedSuffix)) {
		t.Error("Remove left the plaintext rollback copy behind")
	}
	if _, ok := set.Get("bot"); ok {
		t.Error("a removed credential is still served")
	}
}

// Rotating a migrated credential from Settings drops its rollback copy:
// that copy is the superseded value, and nothing should keep it in
// plaintext once the store holds its replacement.
func TestRotatingAMigratedCredentialDropsItsPlaintextRollbackCopy(t *testing.T) {
	dir, store := storeBackedDir(t)
	writeFile(t, filepath.Join(dir, "bot.token"), "ghp_old\n")
	set := mustLoad(t, dir)
	if _, err := set.MigrateFilesToStore(); err != nil {
		t.Fatal(err)
	}
	if err := set.SetToken("bot", "ghp_rotated"); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(dir, "bot.token"+MigratedSuffix)) {
		t.Error("SetToken left the superseded token's plaintext rollback copy behind")
	}
	if v, _ := resolve(t, store, "github-credential-bot/token"); v != "ghp_rotated\n" {
		t.Errorf("store holds %q for bot, want the rotated token", v)
	}
}

func TestRestoreFilesFromStoreRollsBackToFiles(t *testing.T) {
	dir, store := storeBackedDir(t)
	writeFile(t, filepath.Join(dir, "bot.token"), "ghp_old\n")
	writeFile(t, filepath.Join(dir, "spare.token"), "ghp_spare\n")
	set := mustLoad(t, dir)
	if _, err := set.MigrateFilesToStore(); err != nil {
		t.Fatal(err)
	}
	// Changed in Settings after the migration: the rollback must keep
	// the current value, not the stale .migrated copy.
	if err := set.SetToken("bot", "ghp_current"); err != nil {
		t.Fatal(err)
	}
	// A rollback copy whose store value is gone (deleted by hand): renamed back.
	if err := store.DeleteSecret("github-credential-spare"); err != nil {
		t.Fatal(err)
	}

	t.Setenv(CredentialsInFilesEnv, "files")
	files := mustLoad(t, dir)
	if files.InStore() {
		t.Fatalf("%s=files still opened the store", CredentialsInFilesEnv)
	}
	restored, err := files.RestoreFilesFromStore()
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 2 {
		t.Fatalf("restored %v", restored)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "bot.token")); string(b) != "ghp_current\n" {
		t.Errorf("bot.token holds %q, want the store's current value", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "spare.token")); string(b) != "ghp_spare\n" {
		t.Errorf("spare.token holds %q, want its rollback copy", b)
	}
	for _, f := range []string{"bot.token", "spare.token"} {
		if exists(filepath.Join(dir, f+MigratedSuffix)) {
			t.Errorf("%s%s is still there after a rollback", f, MigratedSuffix)
		}
	}
	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range list {
		if strings.HasPrefix(info.Name, CredentialSecretPrefix) {
			t.Errorf("store still holds %s after a rollback", info.Name)
		}
	}
	if cred, ok := files.Get("bot"); !ok || cred.Token == nil || *cred.Token != "ghp_current" {
		t.Fatalf("Get(bot) after a rollback: %+v, %v", cred, ok)
	}
}

func TestAnAppCredentialHeldInTheStoreIsReMintedFromItsKey(t *testing.T) {
	dir, store := storeBackedDir(t)
	set := mustLoad(t, dir)
	key := testAppKey(t)
	if err := set.SetApp("ci-app", "12345", "67890", string(pkcs1PEM(key))); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(dir, "ci-app.app.json")) {
		t.Fatal("SetApp on a store-backed set wrote the private key to a plaintext file")
	}
	if v, ok := resolve(t, store, "github-credential-ci-app/app.json"); !ok || !strings.Contains(v, "PRIVATE KEY") {
		t.Fatal("the App's private key is not in the store")
	}

	// A fresh set, as after a restart: nothing on disk but the store.
	set = mustLoad(t, dir)
	transport := github.NewFakeTransport(
		mintedTokenResponse("ghs_first", "2026-08-30T13:00:00Z"),
		mintedTokenResponse("ghs_second", "2026-08-30T14:00:00Z"),
	)
	set.AppTransport = transport
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	set.Now = func() time.Time { return now }

	cred, ok := set.Get("ci-app")
	if !ok || cred.Token == nil || *cred.Token != "ghs_first" {
		t.Fatalf("first mint: %+v, %v", cred, ok)
	}
	now = now.Add(59 * time.Minute)
	cred, _ = set.Get("ci-app")
	if cred.Token == nil || *cred.Token != "ghs_second" {
		t.Fatalf("near expiry: got %+v, want a re-minted token", cred)
	}
	if len(transport.Calls) != 2 {
		t.Fatalf("expected two mints, got %d", len(transport.Calls))
	}
}
