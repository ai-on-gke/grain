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
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// seedSecret writes every key in data into name via Store.Set -- the
// only way material ever gets into this package's database, now that it
// is not a directory tree a test can seed by writing files directly.
func seedSecret(t *testing.T, store *Store, name string, data map[string]string) {
	t.Helper()
	for key, value := range data {
		if err := store.Set(name, key, []byte(value)); err != nil {
			t.Fatalf("seeding %s/%s: %v", name, key, err)
		}
	}
}

func TestResolveExplicitKeyInMultiKeySecret(t *testing.T) {
	store := New(t.TempDir())
	seedSecret(t, store, "db", map[string]string{"username": "app", "password": "hunter2"})

	got, err := store.Resolve(context.Background(), "db/password")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hunter2" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveBareNameForSingleKeySecret(t *testing.T) {
	store := New(t.TempDir())
	seedSecret(t, store, "gcp-key-minter", map[string]string{"key.json": `{"type":"service_account"}`})

	got, err := store.Resolve(context.Background(), "gcp-key-minter")
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"type":"service_account"}` {
		t.Fatalf("got %q", got)
	}
}

func TestResolveBareNameForMultiKeySecretFailsAmbiguous(t *testing.T) {
	store := New(t.TempDir())
	seedSecret(t, store, "db", map[string]string{"username": "app", "password": "hunter2"})

	_, err := store.Resolve(context.Background(), "db")
	if err == nil {
		t.Fatal("expected an error for an ambiguous bare name")
	}
	// The error names the keys to choose between, and never a value.
	if msg := err.Error(); !strings.Contains(msg, "password, username") || strings.Contains(msg, "hunter2") {
		t.Errorf("error = %q, want it to list the key names and no value", msg)
	}
}

func TestResolveMissingSecretFailsClosed(t *testing.T) {
	store := New(t.TempDir())
	if _, err := store.Resolve(context.Background(), "nope"); err == nil {
		t.Fatal("expected an error for a missing secret")
	}
}

// A name nothing is stored under is ErrNoSecret specifically, so a
// caller for whom the value is optional -- cmd/grain's custom agent
// backends -- can carry on with nothing without also swallowing a store
// it could not read.
func TestResolveMissingSecretReportsErrNoSecret(t *testing.T) {
	store := New(t.TempDir())
	_, err := store.Resolve(context.Background(), "nope")
	if !errors.Is(err, ErrNoSecret) {
		t.Fatalf("got %v, want an error matching ErrNoSecret", err)
	}
	if got, want := err.Error(), `secrets: no secret named "nope"`; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// The other half of that distinction: a store that cannot be opened at
// all reports something ErrNoSecret does not match, so an optional
// credential never hides a secrets file this host cannot read. A
// secrets file with no key beside it is exactly that state -- Open
// refuses to mint a key over secrets it would then be unable to
// decrypt.
func TestResolveOnAnUnreadableStoreIsNotErrNoSecret(t *testing.T) {
	dir := t.TempDir()
	seedSecret(t, New(dir), "agent-backend-custom", map[string]string{"value": "sk-real"})
	if err := os.Remove(filepath.Join(dir, DefaultKeyFileName)); err != nil {
		t.Fatal(err)
	}

	_, err := New(dir).Resolve(context.Background(), "agent-backend-custom")
	if err == nil {
		t.Fatal("expected an error from a store whose key is gone")
	}
	if errors.Is(err, ErrNoSecret) {
		t.Fatalf("got %v, want a store-level error rather than ErrNoSecret", err)
	}
}

func TestResolveMissingKeyFailsClosed(t *testing.T) {
	store := New(t.TempDir())
	seedSecret(t, store, "db", map[string]string{"username": "app"})
	if _, err := store.Resolve(context.Background(), "db/password"); err == nil {
		t.Fatal("expected an error for a missing key")
	}
}

func TestResolveEmptyNameFailsClosed(t *testing.T) {
	store := New(t.TempDir())
	if _, err := store.Resolve(context.Background(), ""); err == nil {
		t.Fatal("expected an error for an empty name")
	}
}

func TestResolveDoesNotTrimValue(t *testing.T) {
	store := New(t.TempDir())
	seedSecret(t, store, "token", map[string]string{"value": "  padded\n"})

	got, err := store.Resolve(context.Background(), "token/value")
	if err != nil {
		t.Fatal(err)
	}
	if got != "  padded\n" {
		t.Fatalf("got %q, want exact bytes preserved", got)
	}
}

func TestResolveRejectsPathTraversalInKey(t *testing.T) {
	store := New(t.TempDir())
	seedSecret(t, store, "secret", map[string]string{"key": "value"})

	if _, err := store.Resolve(context.Background(), "secret/../../etc/passwd"); err == nil {
		t.Fatal("expected an error for a traversal attempt in the key")
	}
}

func TestListReportsNamesAndKeysNotValues(t *testing.T) {
	store := New(t.TempDir())
	seedSecret(t, store, "db", map[string]string{"username": "app", "password": "hunter2"})
	seedSecret(t, store, "token", map[string]string{"value": "abc"})

	got, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	want := []SecretInfo{
		{Name: "db", Keys: []string{"password", "username"}},
		{Name: "token", Keys: []string{"value"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestListOnFreshDatabaseReturnsNoSecrets(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "does-not-exist-yet"))
	got, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v, want none", got)
	}
}

func TestSetThenResolve(t *testing.T) {
	store := New(t.TempDir())

	if err := store.Set("github", "token", []byte("ghp_abc123")); err != nil {
		t.Fatal(err)
	}
	got, err := store.Resolve(context.Background(), "github/token")
	if err != nil {
		t.Fatal(err)
	}
	if got != "ghp_abc123" {
		t.Fatalf("got %q", got)
	}
}

func TestSetOverwritesExistingValue(t *testing.T) {
	store := New(t.TempDir())
	seedSecret(t, store, "db", map[string]string{"password": "old"})

	if err := store.Set("db", "password", []byte("new")); err != nil {
		t.Fatal(err)
	}
	got, err := store.Resolve(context.Background(), "db/password")
	if err != nil {
		t.Fatal(err)
	}
	if got != "new" {
		t.Fatalf("got %q, want overwritten value", got)
	}
}

func TestSetRejectsTraversalNames(t *testing.T) {
	store := New(t.TempDir())
	if err := store.Set("../escape", "key", []byte("x")); err == nil {
		t.Fatal("expected an error for a traversal secret name")
	}
	if err := store.Set("secret", "../escape", []byte("x")); err == nil {
		t.Fatal("expected an error for a traversal key name")
	}
}

func TestDeleteKeyRemovesJustThatKey(t *testing.T) {
	store := New(t.TempDir())
	seedSecret(t, store, "db", map[string]string{"username": "app", "password": "hunter2"})

	if err := store.DeleteKey("db", "password"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(context.Background(), "db/password"); err == nil {
		t.Fatal("expected the deleted key to be gone")
	}
	got, err := store.Resolve(context.Background(), "db/username")
	if err != nil || got != "app" {
		t.Fatalf("username should still resolve: %q, %v", got, err)
	}
}

func TestDeleteKeyRemovesEmptySecretFromList(t *testing.T) {
	store := New(t.TempDir())
	seedSecret(t, store, "token", map[string]string{"value": "abc"})

	if err := store.DeleteKey("token", "value"); err != nil {
		t.Fatal(err)
	}
	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("got %+v, want the now-empty secret gone from List", list)
	}
}

func TestDeleteKeyMissingFailsClosed(t *testing.T) {
	store := New(t.TempDir())
	seedSecret(t, store, "db", map[string]string{"username": "app"})
	if err := store.DeleteKey("db", "password"); err == nil {
		t.Fatal("expected an error for a missing key")
	}
}

func TestDeleteSecretRemovesEveryKey(t *testing.T) {
	store := New(t.TempDir())
	seedSecret(t, store, "db", map[string]string{"username": "app", "password": "hunter2"})

	if err := store.DeleteSecret("db"); err != nil {
		t.Fatal(err)
	}
	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("got %+v, want no secrets left", list)
	}
}

func TestDeleteSecretMissingFailsClosed(t *testing.T) {
	store := New(t.TempDir())
	if err := store.DeleteSecret("nope"); err == nil {
		t.Fatal("expected an error for a missing secret")
	}
}

// A secrets file written before the single-tenant reset can carry a
// per-secret team restriction. It is read and ignored -- the secret
// resolves for any caller -- and the next write leaves it out.
func TestLegacyTeamRestrictionIsIgnoredAndDroppedOnWrite(t *testing.T) {
	dir := t.TempDir()
	store := New(dir)
	seedSecret(t, store, "deploy-key", map[string]string{"token": "tok"})

	key, err := ReadKeyFile(filepath.Join(dir, DefaultKeyFileName))
	if err != nil {
		t.Fatal(err)
	}
	legacy := `{"version":1,"secrets":{"deploy-key":{"token":"dG9r"}},"teams":{"deploy-key":["infra"]}}`
	sealed, err := Encrypt(key, []byte(legacy))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, DefaultFileName)
	if err := os.WriteFile(file, sealed, 0o600); err != nil {
		t.Fatal(err)
	}

	if got, err := store.Resolve(context.Background(), "deploy-key/token"); err != nil || got != "tok" {
		t.Fatalf("Resolve = %q, %v; want the value, the restriction ignored", got, err)
	}

	if err := store.Set("other", "k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Decrypt(key, data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), "teams") {
		t.Fatalf("the next write kept the team restriction: %s", plain)
	}
}

type mockAuditLogger struct {
	mu      sync.Mutex
	entries []AuditEntry
}

func (m *mockAuditLogger) RecordSecretResolution(ctx context.Context, entry AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, entry)
	return nil
}

func TestSecretAuditLogger(t *testing.T) {
	dir := t.TempDir()
	store := New(dir)
	mock := &mockAuditLogger{}
	store.SetAuditLogger(mock)

	if err := store.Set("db-cred", "password", []byte("s3cret")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	ctx := context.Background()
	ctx = WithTask(ctx, "task-123")
	ctx = WithRun(ctx, "run-456")
	ctx = WithPrincipal(ctx, "alice@example.com")

	val, err := store.Resolve(ctx, "db-cred/password")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if val != "s3cret" {
		t.Fatalf("unexpected val: %q", val)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(mock.entries))
	}
	e := mock.entries[0]
	if e.TaskID != "task-123" || e.RunID != "run-456" || e.Principal != "alice@example.com" {
		t.Fatalf("unexpected entry metadata: %+v", e)
	}
	if e.SecretName != "db-cred" || e.Key != "password" {
		t.Fatalf("unexpected entry secret/key: %+v", e)
	}
	if e.Timestamp.IsZero() {
		t.Fatalf("audit entry timestamp is zero")
	}
}
