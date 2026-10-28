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
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/ai-on-gke/grain/pkg/secretsrepo"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// secretsRepoDir is the working tree, under -data-dir beside the secrets
// it backs up. It holds one derived file and can be thrown away: what
// makes it worth keeping between starts is that a clone is slower than a
// fetch, not that anything in it is state.
func SecretsRepoDir(dataDir string) string { return filepath.Join(dataDir, "secrets-repo") }

// openSecretsRepo prepares the repository named by
// <data-dir>/secrets-repo.json, cloning it the first time this host sees
// it.
//
// A deployment that has named no secrets repository gets
// secretsrepo.ErrNoRemote, which every caller here treats as the
// ordinary state it is: keeping the ciphertext on this host alone is
// what grain did before this existed and is still a supported answer for
// a laptop. It is only a *risk* worth naming out loud, which
// `grain state status` does.
func openSecretsRepo(ctx context.Context, dataDir string) (*secretsrepo.Repo, error) {
	settings, err := secretsrepo.LoadSettings(dataDir)
	if err != nil {
		return nil, err
	}
	if settings.Remote == "" {
		return nil, secretsrepo.ErrNoRemote
	}
	state, err := staterepo.LoadSettings(dataDir)
	if err != nil {
		return nil, err
	}
	if err := RefuseStateRepoAsSecretsRepo(settings.Remote, state.Remote); err != nil {
		return nil, err
	}
	if err := checkRefStorage(SecretsRepoDir(dataDir)); err != nil {
		return nil, err
	}
	return secretsrepo.Open(ctx, secretsrepo.Config{
		Dir:    SecretsRepoDir(dataDir),
		Remote: settings.Remote,
		Branch: settings.Branch,
		// Derived from the state repository unless an operator named the
		// file themselves: a secrets repository is assumed to be shared,
		// so the name has to say that the file is grain's and which
		// grain's (secretsrepo.FileName).
		File:   settings.FileNameFor(state.Remote),
		Source: SecretsConfig(dataDir).File,
		// grain's own commits, attributed to grain, for the same reason
		// the state repository's are: a reader of that history should be
		// able to tell which deployment wrote a file it cannot open.
		AuthorName:  "grain",
		AuthorEmail: "grain@" + hostnameOrLocalhost(),
		Token:       RepoToken(dataDir, settings.Remote, settings.TokenFile),
	})
}

// RefuseStateRepoAsSecretsRepo refuses pointing both the config mirror and
// the encrypted secrets backup at the same repository.
func RefuseStateRepoAsSecretsRepo(secretsRemote, stateRemote string) error {
	if stateRemote == "" || secretsRemote == "" || !sameRemote(secretsRemote, stateRemote) {
		return nil
	}
	return fmt.Errorf("the secrets repository (%s) is this deployment's state repository, "+
		"which is somewhere tasks are dispatched: a sandbox that may clone it reads every object "+
		"in it, ciphertext included. Point %s at a repository of its own",
		secretsRemote, secretsrepo.SettingsFileName)
}

// SameRemote compares two git URLs the way an operator means them: the
// same repository reached over https and over ssh is the same
// repository, and a trailing .git changes nothing.
func SameRemote(a, b string) bool { return sameRemote(a, b) }

// sameRemote compares two git URLs the way an operator means them: the
// same repository reached over https and over ssh is the same
// repository, and a trailing .git changes nothing. A different host is
// a different repository whatever its path says: SetSecretsRepo keeps a
// named token only for the same repository, and a token kept across a
// change of host is a token sent to that new host (#2372).
func sameRemote(a, b string) bool {
	if remoteHost(a) != remoteHost(b) {
		return false
	}
	ao, an, aok := repoFromRemote(a)
	bo, bn, bok := repoFromRemote(b)
	if aok && bok {
		return strings.EqualFold(ao, bo) && strings.EqualFold(an, bn)
	}
	return strings.EqualFold(strings.TrimSuffix(strings.TrimSpace(a), ".git"),
		strings.TrimSuffix(strings.TrimSpace(b), ".git"))
}

// syncSecretsRepo opens the repository and reconciles it once, saying in
// the journal only what is worth a line: a restore, or a push. A cycle
// that carried nothing is the ordinary tick and says nothing at all.
//
// It returns the opened repository so a caller that syncs again -- the
// daemon, on its timer -- does not re-clone, and nil when there is
// nothing configured. The error is the sync's, never "no repository":
// see openSecretsRepo.
func SyncSecretsRepo(ctx context.Context, dataDir string) (*secretsrepo.Repo, error) {
	repo, err := openSecretsRepo(ctx, dataDir)
	if errors.Is(err, secretsrepo.ErrNoRemote) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return repo, syncOpenSecretsRepo(ctx, repo)
}

// syncOpenSecretsRepo is one reconcile of an already-open repository,
// with what it did reported.
func syncOpenSecretsRepo(ctx context.Context, repo *secretsrepo.Repo) error {
	if repo == nil {
		return nil
	}
	out, err := repo.Sync(ctx)
	switch {
	case out.Restored:
		// The rebuilt host, and the whole reason the repository exists. It
		// is worth a line every time it happens: a data directory that
		// arrived without its ciphertext is a deployment somebody is in
		// the middle of restoring, and the next thing they need to know is
		// whether the key beside it can open what just came back -- which
		// openSecrets says immediately afterwards.
		log.Printf("grain: restored this deployment's encrypted secrets from %s (%s) -- "+
			"the private key that opens them is not in that repository and never was",
			repo.Remote(), repo.File())
	case out.Pushed:
		log.Printf("grain: pushed this deployment's encrypted secrets to %s (%s)", repo.Remote(), repo.File())
	}
	return err
}

// startupSecretsRepoSync is the sync that runs before anything opens the
// secret store, and it is the restore.
//
// Before, and not on the timer's first tick, because of what the store
// does with an empty data directory: secrets.Open mints a key when it
// finds neither a key nor a file, and a host restored onto a fresh disk
// that minted itself one before its ciphertext arrived would be a
// deployment holding a key that opens nothing and a file nothing opens.
// Fetching first makes the ordering unambiguous -- the file is there
// before the question "can this host read it" is ever asked, and the
// answer is reported at startup either way.
//
// Never fatal. A secrets repository that cannot be reached is a backup
// that did not happen, which is worth a line in the journal and is not a
// reason to refuse to run: the deployment's own secrets are on its own
// disk, which is where they were before any of this existed.
func StartupSecretsRepoSync(ctx context.Context, dataDir string) *secretsrepo.Repo {
	repo, err := SyncSecretsRepo(ctx, dataDir)
	if err != nil {
		log.Printf("grain: could not sync this deployment's encrypted secrets with %s: %v",
			secretsRepoRemote(dataDir), err)
	}
	return repo
}

// secretsRepoRemote is what to call the repository in a message when
// opening it is what failed, so an error that could not name it through
// the repository still names it.
func secretsRepoRemote(dataDir string) string {
	settings, err := secretsrepo.LoadSettings(dataDir)
	if err != nil || settings.Remote == "" {
		return "the secrets repository"
	}
	return settings.Remote
}
