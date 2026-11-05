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

package localscripts

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ai-on-gke/grain/pkg/localexec"
)

// stateRepo writes a declaration named name into a new state repository
// checkout and returns the checkout.
func stateRepo(t *testing.T, name, decl string, scripts map[string]string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, StateRepoDir, name)
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, DeclarationFile), []byte(decl), 0o644); err != nil {
		t.Fatal(err)
	}
	for file, content := range scripts {
		if err := os.WriteFile(filepath.Join(dir, "scripts", file), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestNameRefusesPaths(t *testing.T) {
	for _, id := range []string{"local-scripts:", "local-scripts:../x", "local-scripts:a/b", "local-scripts:/etc", "local-scripts:Ops", "repo-write:a/b"} {
		if _, ok := Name(id); ok {
			t.Errorf("Name(%q) accepted", id)
		}
	}
	if _, err := (Config{StateDir: t.TempDir()}).Path("../x"); err == nil {
		t.Error("Path(../x) accepted")
	}
}

func TestPathPrefersHostManaged(t *testing.T) {
	root := stateRepo(t, "ops", "scripts: {}\n", nil)
	host := filepath.Join(t.TempDir(), DeclarationFile)
	if err := os.WriteFile(host, []byte("scripts: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var cfg Config
	cfg.StateDir = root
	if err := cfg.ParseHost("ops=" + host); err != nil {
		t.Fatal(err)
	}
	if err := cfg.ParseHost("ops=relative/path"); err == nil {
		t.Error("ParseHost accepted a relative path")
	}
	if got, err := cfg.Path("ops"); err != nil || got != host {
		t.Fatalf("Path = %q, %v; want the host-managed %q", got, err, host)
	}
}

func TestOpenDeliversDeclaredSecrets(t *testing.T) {
	root := stateRepo(t, "ops", `scripts:
  list:
    - path: scripts/show
      env: {TOKEN: ops-token}
`, map[string]string{"show": "#!/bin/sh\nprintf '%s:%s' \"$TOKEN\" \"$1\"\n"})
	decl, err := (Config{StateDir: root}).Path("ops")
	if err != nil {
		t.Fatal(err)
	}
	execs, err := Open([]string{decl}, Credentials{"ops-token": "s3cret"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, e := range execs {
			e.Close()
		}
	}()
	tools := execs[0].Tools()
	if len(tools) != 1 || tools[0].Name != "local_show" {
		t.Fatalf("tools %+v: want local_show", tools)
	}
	res := tools[0].Handler(context.Background(), map[string]any{"args": []any{"x"}})
	var out localexec.Result
	if err := json.Unmarshal([]byte(res.Text), &out); err != nil || out.Stdout != "s3cret:x" {
		t.Fatalf("result %q (%v): want the secret delivered to the script", res.Text, err)
	}
	if _, err := Open([]string{decl}, Credentials{}, nil); err == nil {
		t.Error("Open served a script whose secret was not delivered")
	}
}
