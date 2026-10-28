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

package modelproxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A token minted by one daemon is honoured by the next, which is what a
// run adopted across a restart needs; and what the file holds is never
// the token itself.
func TestTokensPersistRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets", "modelproxy-claude-tokens.json")

	before := NewTokens()
	if err := before.PersistTo(path); err != nil {
		t.Fatalf("PersistTo: %v", err)
	}
	id, token, err := before.Mint("run-1", testConnection)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	revokedID, _, err := before.Mint("run-2", testConnection)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	before.Revoke(revokedID)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("token file mode = %o, want 600", mode)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.TrimPrefix(token, id+".")
	if strings.Contains(string(data), secret) {
		t.Errorf("token file holds the token's secret:\n%s", data)
	}
	if strings.Contains(string(data), revokedID) {
		t.Errorf("token file still holds revoked id %s:\n%s", revokedID, data)
	}

	after := NewTokens()
	if err := after.PersistTo(path); err != nil {
		t.Fatalf("PersistTo after restart: %v", err)
	}
	if owner, ok := after.Authenticate(token); !ok || owner != "run-1" {
		t.Errorf("Authenticate after restart = %q, %v; want run-1, true", owner, ok)
	}
	// And it still spends the team connection it was minted with: an
	// adopted run must not lose its team's key, nor gain another's.
	if _, connection, _ := after.Lookup(token); connection != testConnection {
		t.Errorf("the token forwards with %+v after restart, want %+v", connection, testConnection)
	}
	if _, ok := after.Authenticate(id + "." + strings.Repeat("0", len(secret))); ok {
		t.Error("the right id with the wrong secret authenticated after restart")
	}
	if after.Len() != 1 {
		t.Errorf("Len after restart = %d, want 1", after.Len())
	}

	// A revoke in the new daemon reaches the file too.
	after.Revoke(id)
	again := NewTokens()
	if err := again.PersistTo(path); err != nil {
		t.Fatal(err)
	}
	if _, ok := again.Authenticate(token); ok {
		t.Error("a token revoked after the restart came back after another")
	}
}

// An expired token is not brought back by a restart, and is dropped from
// the file the next time it is written.
func TestTokensPersistDropsExpired(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	before := &Tokens{Now: func() time.Time { return now }}
	if err := before.PersistTo(path); err != nil {
		t.Fatal(err)
	}
	id, token, err := before.MintUntil("run-1", testConnection, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	later := now.Add(2 * time.Hour)
	after := &Tokens{Now: func() time.Time { return later }}
	if err := after.PersistTo(path); err != nil {
		t.Fatal(err)
	}
	if _, ok := after.Authenticate(token); ok {
		t.Error("an expired token authenticated after restart")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), id) {
		t.Errorf("expired id %s still in the file:\n%s", id, data)
	}
}

// A file that will not parse is reported, not silently replaced.
func TestTokensPersistRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	tokens := NewTokens()
	if err := tokens.PersistTo(path); err == nil {
		t.Fatal("PersistTo accepted a corrupt file")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "not json" {
		t.Errorf("corrupt file was overwritten: %q", data)
	}
	// Still a working in-memory registry.
	if _, token, err := tokens.Mint("run", testConnection); err != nil {
		t.Fatal(err)
	} else if _, ok := tokens.Authenticate(token); !ok {
		t.Error("in-memory registry stopped authenticating after a failed load")
	}
}
