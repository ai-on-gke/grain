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
	"os"
	"path/filepath"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
)

// TestCiClonesUnderARelativeTempDir is the regression for the 2026-10-29
// walkthrough's F6: `grain daemon -data-dir d2` makes every clone's
// TempDir d2/state, and git -- run from inside the temporary directory --
// was then handed a target relative to the daemon's own working
// directory, so every embedded CI run failed with "git rev-parse: chdir
// d2/state/grain-ci-workflows-.../repo: no such file or directory".
func TestCiClonesUnderARelativeTempDir(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)

	data := filepath.Join(root, "data")
	repo := StateRepoDir(data)
	if err := os.MkdirAll(filepath.Join(repo, ".grain", "workflows"), 0o700); err != nil {
		t.Fatal(err)
	}
	workflow := "on: push\njobs:\n  test:\n    steps:\n      - run: make test\n"
	if err := os.WriteFile(filepath.Join(repo, ".grain", "workflows", "ci.yml"), []byte(workflow), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "workflows")
	head := gitIn(t, repo, "rev-parse", "HEAD")

	tempDir := filepath.Join("d2", "state")
	if err := os.MkdirAll(tempDir, 0o700); err != nil {
		t.Fatal(err)
	}

	src := CiWorkflowSource{ConfigRepoDir: repo, TempDir: tempDir}
	workflows, commit, err := src.Workflows(t.Context(), "grain/config", "main")
	if err != nil {
		t.Fatalf("Workflows under the relative TempDir %s: %v", tempDir, err)
	}
	if commit != head || len(workflows) != 1 {
		t.Errorf("Workflows = %d workflow(s) at %s, want 1 at %s", len(workflows), commit, head)
	}
	if found, err := src.OnBranch(t.Context(), "grain/config", "main", head); err != nil || !found {
		t.Errorf("OnBranch(%s) under the relative TempDir = %t, %v; want true", head, found, err)
	}

	checks := StateCheckSandboxes{ConfigRepoDir: repo, TempDir: tempDir}
	sb, err := checks.Acquire(t.Context(), ci.SandboxRequest{Builtin: StateCheckBuiltin, Repo: "grain/config", Commit: head})
	if err != nil {
		t.Fatalf("the state check's checkout under the relative TempDir %s: %v", tempDir, err)
	}
	if err := sb.Release(t.Context()); err != nil {
		t.Fatal(err)
	}

	if left, _ := os.ReadDir(tempDir); len(left) != 0 {
		t.Errorf("%s still holds %d entr(ies) after every clone was cleaned up", tempDir, len(left))
	}
}
