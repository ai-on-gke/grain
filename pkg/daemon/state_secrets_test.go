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

// Where the encrypted secrets file lives, and what the git proxy makes
// of a state repository that holds one.
//
// The two are one subject: the file moved out of the state repository
// (secretsConfig) precisely because that repository is now somewhere
// agents are dispatched to work, and the repositories that already carry
// it in their history are the ones forbiddenRepos refuses to every
// sandbox.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

func TestSecretsLiveBesideTheKeyNotInTheStateRepository(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	cfg := SecretsConfig(dataDir)
	if got, want := filepath.Dir(cfg.File), filepath.Dir(cfg.KeyFile); got != want {
		t.Errorf("the encrypted file is in %s and the key in %s; they belong together", got, want)
	}
	if filepath.Dir(cfg.File) == StateRepoDir(dataDir) {
		t.Error("the encrypted file is still inside the state repository, where a sandbox can clone it")
	}
}

// An installation written by a build that kept the file in the state
// repository has to catch up on its own, without an operator running
// anything: opening the store is what every path that touches a secret
// does first.
func TestOpeningSecretsMovesAnOlderFileOutOfTheStateRepository(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	if err := os.MkdirAll(StateRepoDir(dataDir), 0o700); err != nil {
		t.Fatal(err)
	}
	inRepo := filepath.Join(StateRepoDir(dataDir), secrets.DefaultFileName)
	if err := os.WriteFile(inRepo, []byte("ciphertext an older build wrote"), 0o600); err != nil {
		t.Fatal(err)
	}

	OpenSecrets(dataDir)

	if _, err := os.Stat(inRepo); !os.IsNotExist(err) {
		t.Errorf("the secrets file is still in the state repository (err=%v)", err)
	}
	moved, err := os.ReadFile(SecretsConfig(dataDir).File)
	if err != nil {
		t.Fatalf("reading the moved file: %v", err)
	}
	if string(moved) != "ciphertext an older build wrote" {
		t.Errorf("the moved file = %q, want the bytes that were in the repository", moved)
	}
}

// A secret written through one path must be readable through another,
// which is the whole reason secretsConfig is one function -- and the
// thing a move would break if it were done anywhere else.
func TestASecretSurvivesTheMove(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	if err := OpenSecrets(dataDir).Set("github-app", "app-id", []byte("12345")); err != nil {
		t.Fatalf("setting: %v", err)
	}
	got, err := OpenSecrets(dataDir).Resolve(context.Background(), "github-app/app-id")
	if err != nil {
		t.Fatalf("resolving: %v", err)
	}
	if got != "12345" {
		t.Errorf("secret = %q, want 12345", got)
	}
}

// stateRepoWithSecrets stands up a real state repository holding a real
// commit, with a remote configured so that there is an owner/repo for a
// sandbox to ask the proxy about at all.
func stateRepoWithSecrets(t *testing.T, dataDir string, holdSecrets bool) {
	t.Helper()
	ctx := context.Background()
	if err := staterepo.SaveSettings(dataDir, staterepo.Settings{
		Remote: "https://github.com/acme/grain-state.git",
	}); err != nil {
		t.Fatal(err)
	}
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: StateRepoDir(dataDir)})
	if err != nil {
		t.Fatalf("opening a state repository: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo.Dir(), "README.md"), []byte("state\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if holdSecrets {
		if err := os.WriteFile(filepath.Join(repo.Dir(), staterepo.SecretsFile), []byte("ciphertext"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.Commit(ctx, "seed"); err != nil {
		t.Fatalf("committing: %v", err)
	}
	if holdSecrets {
		// Removed from the tip, exactly as this build's own migration
		// leaves it -- and still readable from every clone.
		if err := os.Remove(filepath.Join(repo.Dir(), staterepo.SecretsFile)); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.Commit(ctx, "remove it"); err != nil {
			t.Fatalf("committing the removal: %v", err)
		}
	}
}

func TestForbiddenReposAllowsAStateRepositoryWithNoSecretsInIt(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	stateRepoWithSecrets(t, dataDir, false)

	forbidden, err := ForbiddenRepos(context.Background(), dataDir)
	if err != nil {
		t.Fatalf("forbiddenRepos: %v", err)
	}
	if len(forbidden) != 0 {
		t.Fatalf("forbidden = %v, want nothing refused", forbidden)
	}
}

// A local-only installation has no owner/repo on any host for a sandbox
// to ask for, so there is nothing to refuse.
func TestForbiddenReposIsEmptyForALocalOnlyInstallation(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	forbidden, err := ForbiddenRepos(context.Background(), dataDir)
	if err != nil {
		t.Fatalf("forbiddenRepos: %v", err)
	}
	if len(forbidden) != 0 {
		t.Fatalf("forbidden = %v, want nothing refused", forbidden)
	}
}
