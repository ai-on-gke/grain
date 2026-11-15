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

package installtrack

import (
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"
)

func TestReadSecretsDirSkipsNonManifestFiles(t *testing.T) {
	secretsDir := t.TempDir()
	// A FIFO blocks if opened for reading; skipping non-manifest files before
	// os.ReadFile prevents touching credential tokens or blocking files.
	fifoPath := filepath.Join(secretsDir, "github-pat.token")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(secretsDir, "hosts.json"), []byte(`{"forge.example":{"kind":"github"}}`), 0o600); err != nil {
		t.Fatalf("write hosts.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(secretsDir, "corp.ambient.json"), []byte(`{"host":"forge.example","source":"gh"}`), 0o600); err != nil {
		t.Fatalf("write ambient json: %v", err)
	}

	type result struct {
		ambient []AmbientSecret
		hosts   []string
	}
	done := make(chan result, 1)
	go func() {
		a, h := readSecretsDir(secretsDir)
		done <- result{ambient: a, hosts: h}
	}()
	select {
	case got := <-done:
		if len(got.ambient) != 1 || got.ambient[0].Source != "gh" || got.ambient[0].Host != "forge.example" {
			t.Fatalf("ambient = %+v, want 1 gh ambient secret", got.ambient)
		}
		if !slices.Equal(got.hosts, []string{"forge.example"}) {
			t.Fatalf("hosts = %v, want [forge.example]", got.hosts)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("readSecretsDir blocked opening unrelated file in secrets directory")
	}
}
