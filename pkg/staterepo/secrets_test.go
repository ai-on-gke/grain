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

package staterepo_test

// Whether a state repository is one a sandbox may be let near, which is
// the whole of HasSecrets: a repository that holds grain's encrypted
// secrets file, or ever did, is one a clone reads it out of.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/staterepo"
)

func openRepo(t *testing.T) *staterepo.Repo {
	t.Helper()
	repo, err := staterepo.Open(context.Background(), staterepo.Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	return repo
}

func TestHasSecretsIsFalseForARepositoryThatNeverHeldOne(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := openRepo(t)
	if err := os.WriteFile(filepath.Join(repo.Dir(), "README.md"), []byte("state\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Commit(ctx, "seed"); err != nil {
		t.Fatalf("committing: %v", err)
	}
	held, err := repo.HasSecrets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if held {
		t.Error("a repository with no secrets file in it reported one")
	}
}

func TestHasSecretsSeesAFileInTheWorkingTree(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := openRepo(t)
	if err := os.WriteFile(filepath.Join(repo.Dir(), staterepo.SecretsFile), []byte("ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}
	held, err := repo.HasSecrets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !held {
		t.Error("a secrets file sitting in the working tree was not seen")
	}
}

// The case the whole thing exists for: the file is gone from the tip and
// still perfectly readable from any clone. Removing it is not a remedy,
// and HasSecrets must not pretend it is.
func TestHasSecretsRemembersAFileThatWasRemoved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := openRepo(t)
	path := filepath.Join(repo.Dir(), staterepo.SecretsFile)
	if err := os.WriteFile(path, []byte("ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Commit(ctx, "an older build committed the secrets file"); err != nil {
		t.Fatalf("committing: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Commit(ctx, "and this one removed it"); err != nil {
		t.Fatalf("committing the removal: %v", err)
	}
	held, err := repo.HasSecrets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !held {
		t.Error("a secrets file removed from the tip is still in the history, and was reported gone")
	}
}

// EnsureIgnored has to reach a repository an earlier build created, or
// the line that keeps a stray copy out is only ever written for
// deployments that did not need it.
func TestEnsureIgnoredAddsTheSecretsLineToAnOlderFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(path, []byte("# Written by grain.\n*.swp\nmy-own-line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.EnsureIgnored(dir); err != nil {
		t.Fatalf("EnsureIgnored: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), staterepo.SecretsFile) {
		t.Errorf(".gitignore never learned about %s:\n%s", staterepo.SecretsFile, data)
	}
	if !strings.Contains(string(data), "my-own-line") {
		t.Errorf("EnsureIgnored dropped what somebody else had written:\n%s", data)
	}
	if strings.Count(string(data), "*.swp") != 1 {
		t.Errorf("EnsureIgnored duplicated a line it already had:\n%s", data)
	}
}
