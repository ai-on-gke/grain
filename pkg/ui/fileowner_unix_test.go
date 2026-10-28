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

package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileOwnerNamesTheOwningAccount(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Skipf("no current user: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, LocalAPITokenFileName)
	if err := os.WriteFile(path, []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := FileOwner(path); got != me.Username {
		t.Errorf("FileOwner(%s) = %q, want %q", path, got, me.Username)
	}
	// A file that is not there yet is its directory's owner's.
	if got := FileOwner(filepath.Join(dir, "absent")); got != me.Username {
		t.Errorf("FileOwner of a missing file = %q, want its directory's owner %q", got, me.Username)
	}
	if got := FileOwner(filepath.Join(dir, "no", "such", "file")); got != "grain" {
		t.Errorf("FileOwner of an unknowable path = %q, want the fallback \"grain\"", got)
	}
}

// The loopback 401 hint names the account that owns the daemon's data,
// not a "grain" account a host installed under its owner's own account
// does not have.
func TestLoopbackUnauthorizedHintNamesTheDataOwner(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Skipf("no current user: %v", err)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": "sign in to use this server's API"}`))
	}))
	defer ts.Close()
	dir := t.TempDir()
	path := LocalAPITokenPath(dir)
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(LocalAPITokenFileEnv, "")
	t.Setenv("GRAIN_DATA_DIR", dir)

	_, err = NewHTTPClient(ts.URL).ListTasks(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	want := "sudo -u " + me.Username + " env GRAIN_DATA_DIR=" + dir + " grain"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not say %q", err, want)
	}
}
