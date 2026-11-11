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

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/secrets"
)

func TestStateKeyRetiredListsAndDeletes(t *testing.T) {
	dir := t.TempDir()
	cfg := secrets.Config{File: filepath.Join(dir, secrets.DefaultFileName), KeyFile: filepath.Join(dir, secrets.DefaultKeyFileName)}
	store := secrets.Open(cfg)
	old, _ := store.PublicKey()
	k, _ := secrets.GenerateKey()
	if err := store.ImportKey(k); err != nil {
		t.Fatal(err)
	}
	if err := store.Set("db", "password", []byte("p")); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := stateKeyRetired(cfg, &out, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), old) {
		t.Fatalf("list = %q, want the replaced key %s", out.String(), old)
	}
	var status bytes.Buffer
	reportRetiredKeys(t.TempDir(), &status)
	if status.Len() != 0 {
		t.Fatalf("a data dir with no retired keys reported %q", status.String())
	}

	for _, bad := range [][]string{{"delete"}, {"delete", "-all", "x"}, {"delete", secrets.DefaultKeyFileName}} {
		if err := stateKeyRetired(cfg, &bytes.Buffer{}, bad); err == nil {
			t.Errorf("retired %q succeeded, want a refusal", bad)
		}
	}
	out.Reset()
	if err := stateKeyRetired(cfg, &out, []string{"delete", "-all"}); err != nil {
		t.Fatal(err)
	}
	if left, _ := secrets.RetiredKeys(cfg.KeyFile); len(left) != 0 {
		t.Fatalf("retired keys left: %+v", left)
	}
	if _, err := os.Stat(cfg.KeyFile); err != nil {
		t.Fatalf("live key: %v", err)
	}
}

// The archive the secrets file is still sealed to is the only key that
// opens it, so delete refuses it.
func TestStateKeyRetiredRefusesTheKeyTheFileIsSealedTo(t *testing.T) {
	dir := t.TempDir()
	cfg := secrets.Config{File: filepath.Join(dir, secrets.DefaultFileName), KeyFile: filepath.Join(dir, secrets.DefaultKeyFileName)}
	store := secrets.Open(cfg)
	if err := store.Set("db", "password", []byte("p")); err != nil {
		t.Fatal(err)
	}
	archive := cfg.KeyFile + secrets.RetiredKeyMarker + "20260101-000000"
	if err := os.Rename(cfg.KeyFile, archive); err != nil {
		t.Fatal(err)
	}
	k, _ := secrets.GenerateKey()
	if err := secrets.WriteKeyFile(cfg.KeyFile, k); err != nil {
		t.Fatal(err)
	}
	if err := stateKeyRetired(cfg, &bytes.Buffer{}, []string{"delete", filepath.Base(archive)}); err == nil {
		t.Fatal("deleted the only key the secrets file opens with")
	}
	if _, err := os.Stat(archive); err != nil {
		t.Fatalf("archive: %v", err)
	}
}
