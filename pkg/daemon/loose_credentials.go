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
	"os"
	"path/filepath"
	"strings"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/releaseapproval"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

// MoveLooseCredentialsIntoStore imports the plaintext credential files a
// deployment still has into the secrets store, once per start
// (design/design/secrets-key-protection.design.md §5, Phase 3):
//
//   - secrets/github/<name>.token, .app.json and .ssh.json, into
//     github-credential-<name> (gitproxy.CredentialSet.MigrateFilesToStore),
//     each renamed to <file>.migrated once the store reads it back. With
//     GRAIN_GITHUB_CREDENTIALS=files the move is reversed instead
//     (RestoreFilesFromStore), which is what a downgrade runs first;
//   - -hub-token-file, into registry-registration-token, when that secret
//     is unset. The file is the operator's own path and is left alone;
//     the store is read before it, so it is inert from here on;
//   - secrets/relay/token, into relay/token, renamed to token.migrated;
//   - secrets/state-repo-token and secrets-repo-token, into secrets of the
//     same names (key token), renamed to <file>.migrated. RepoToken serves
//     them from there to git's askpass helper.
//
// The CA bundles move on first use instead (SecretsCAStore), and the
// rollback writes them back too (restoreCAFiles).
//
// Each step logs what it did and warns about what is left; none stops the
// daemon from starting, since every reader still falls back to the file
// it would have read before.
func MoveLooseCredentialsIntoStore(cfg Config, store *secrets.Store) {
	moveGitCredentialsIntoStore(cfg.DataDir)
	if store == nil {
		return
	}
	if os.Getenv(gitproxy.CredentialsInFilesEnv) == "files" {
		restoreCAFiles(store, cfg.DataDir)
		if err := releaseapproval.RestoreKeyFile(cfg.DataDir); err != nil {
			log.Printf("grain: restoring %s: %v", releaseapproval.KeyFile, err)
		}
		if fileStore := fileSecretsBeside(cfg.DataDir); fileStore != nil {
			restoreRepoTokenFiles(fileStore, cfg.DataDir)
		}
		return
	}
	if fileStore := repoTokenSecrets(cfg.DataDir); fileStore != nil {
		for _, name := range repoTokenNames {
			importRepoTokenFile(fileStore, cfg.DataDir, name)
		}
	}
	importHubTokenFile(store, cfg.HubTokenFile)
	importRelayTokenFile(store, cfg.DataDir)
}

func moveGitCredentialsIntoStore(dataDir string) {
	if dataDir == "" {
		return
	}
	dir := filepath.Join(SecretsDir(dataDir), "github")
	set, err := gitproxy.LoadCredentialSet(dir)
	if err != nil {
		log.Printf("grain: not moving git credentials into the secrets store: %v", err)
		return
	}
	if os.Getenv(gitproxy.CredentialsInFilesEnv) == "files" {
		restored, err := set.RestoreFilesFromStore()
		for _, path := range restored {
			log.Printf("grain: %s=files: wrote git credential %s back out of the secrets store", gitproxy.CredentialsInFilesEnv, path)
		}
		if err != nil {
			log.Printf("grain: restoring git credential files from the secrets store: %v", err)
		}
		return
	}
	if !set.InStore() {
		return
	}
	if _, err := set.MigrateFilesToStore(); err != nil {
		log.Printf("grain: some git credentials are still plaintext files under %s: %v", dir, err)
	}
}

// restoreCAFiles writes each CA bundle the store holds back to its
// <data-dir>/*.pem file and drops the store's copy: the CA half of the
// GRAIN_GITHUB_CREDENTIALS=files rollback, so sandboxes keep trusting the
// same CA across a downgrade.
func restoreCAFiles(store *secrets.Store, dataDir string) {
	if dataDir == "" {
		return
	}
	for secret, file := range map[string]string{
		SandboxAccessCASecretName: "sandbox-access-ca.pem",
		PackageCacheCASecretName:  "package-cache-ca.pem",
	} {
		has, err := store.Has(secret, caBundleKey)
		if err != nil || !has {
			continue
		}
		v, err := store.Resolve(context.Background(), secret+"/"+caBundleKey)
		if err != nil {
			log.Printf("grain: restoring %s: %v", file, err)
			continue
		}
		path := filepath.Join(dataDir, file)
		if err := writeFileAtomic0600(path, []byte(v)); err != nil {
			log.Printf("grain: restoring %s: %v", path, err)
			continue
		}
		_ = store.DeleteKey(secret, caBundleKey)
		_ = os.Remove(path + gitproxy.MigratedSuffix)
		log.Printf("grain: %s=files: wrote the CA bundle %s back out of the secrets store", gitproxy.CredentialsInFilesEnv, path)
	}
}

// writeFileAtomic0600 writes data to path through a temporary file and a
// rename, owner-only.
func writeFileAtomic0600(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".restore-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// importHubTokenFile stores -hub-token-file's token as the registration
// secret, when the secret is not set yet.
func importHubTokenFile(store *secrets.Store, path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	if ok, err := store.Has(secrets.HubRegistrationTokenSecret, secrets.AgentCredentialKey); err != nil || ok {
		if ok {
			log.Printf("grain: -hub-token-file %s is ignored: secret %s is set and is read first -- "+
				"delete the file and drop the flag", path, secrets.HubRegistrationTokenSecret)
		}
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	tok := strings.TrimSpace(string(data))
	if tok == "" {
		return
	}
	if err := setVerified(store, secrets.HubRegistrationTokenSecret, secrets.AgentCredentialKey, tok); err != nil {
		log.Printf("grain: importing -hub-token-file %s into the secrets store: %v -- still reading the file", path, err)
		return
	}
	log.Printf("grain: imported -hub-token-file %s into secret %s; the file is no longer read -- "+
		"delete it and drop the flag", path, secrets.HubRegistrationTokenSecret)
}

// importRelayTokenFile stores secrets/relay/token as relay/token, when the
// store does not hold one, and renames the file to token.migrated.
func importRelayTokenFile(store *secrets.Store, dataDir string) {
	if dataDir == "" {
		return
	}
	path := filepath.Join(SecretsDir(dataDir), "relay", "token")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if ok, err := store.Has("relay", "token"); err != nil {
		return
	} else if !ok {
		if err := setVerified(store, "relay", "token", strings.TrimSpace(string(data))); err != nil {
			log.Printf("grain: importing %s into the secrets store: %v", path, err)
			return
		}
	}
	if err := os.Rename(path, path+gitproxy.MigratedSuffix); err != nil {
		log.Printf("grain: renaming %s after importing it: %v", path, err)
		return
	}
	log.Printf("grain: moved %s into secret relay/token (kept as %s for a rollback); "+
		"the relay has been removed, so it can be deleted from Settings -> Secrets", path, path+gitproxy.MigratedSuffix)
}

// setVerified sets secret/key and reads it back.
func setVerified(store *secrets.Store, secret, key, value string) error {
	if err := store.Set(secret, key, []byte(value)); err != nil {
		return err
	}
	got, err := store.Resolve(context.Background(), secret+"/"+key)
	if err != nil {
		return err
	}
	if got != value {
		return errors.New("the store read back something other than what was written")
	}
	return nil
}

// repoTokenNames are the state and secrets repositories' explicit push
// tokens. Each is kept in the secrets store as secret <name>, key token;
// the settings' TokenFile still names <data-dir>/secrets/<name>, which is
// how RepoToken knows to look in the store for it.
var repoTokenNames = []string{"state-repo-token", "secrets-repo-token"}

const repoTokenKey = "token"

// fileSecretsBeside is the file store in <data-dir>/secrets when it has a
// key, whatever CredentialsInFilesEnv says: what a rollback reads from.
func fileSecretsBeside(dataDir string) *secrets.Store {
	cfg := SecretsConfig(dataDir)
	if _, err := os.Stat(cfg.KeyFile); err != nil {
		return nil
	}
	return secrets.Open(cfg)
}

// repoTokenSecrets is the file store a repository token is kept in: the
// one in <data-dir>/secrets, when it has a key and files were not asked
// for (gitproxy.CredentialsInFilesEnv). Never minted here.
func repoTokenSecrets(dataDir string) *secrets.Store {
	if dataDir == "" || os.Getenv(gitproxy.CredentialsInFilesEnv) == "files" {
		return nil
	}
	cfg := SecretsConfig(dataDir)
	if _, err := os.Stat(cfg.KeyFile); err != nil {
		return nil
	}
	return secrets.Open(cfg)
}

// storedRepoTokenName is the repoTokenNames entry tokenFile is the
// canonical path of, or "".
func storedRepoTokenName(dataDir, tokenFile string) string {
	if dataDir == "" || tokenFile == "" {
		return ""
	}
	for _, name := range repoTokenNames {
		if filepath.Clean(tokenFile) == filepath.Join(SecretsDir(dataDir), name) {
			return name
		}
	}
	return ""
}

// storedRepoToken is the token behind tokenFile when it is one the store
// holds; ok is false otherwise, and the caller reads the file.
func storedRepoToken(ctx context.Context, dataDir, tokenFile string) (string, bool) {
	name := storedRepoTokenName(dataDir, tokenFile)
	if name == "" {
		return "", false
	}
	store := repoTokenSecrets(dataDir)
	if store == nil {
		return "", false
	}
	if has, err := store.Has(name, repoTokenKey); err != nil || !has {
		importRepoTokenFile(store, dataDir, name)
		if has, err = store.Has(name, repoTokenKey); err != nil || !has {
			return "", false
		}
	}
	v, err := store.Resolve(ctx, name+"/"+repoTokenKey)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(v), true
}

// importRepoTokenFile moves <data-dir>/secrets/<name> into the store and
// renames it to <name>.migrated.
func importRepoTokenFile(store *secrets.Store, dataDir, name string) {
	path := filepath.Join(SecretsDir(dataDir), name)
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if err := setVerified(store, name, repoTokenKey, strings.TrimSpace(string(data))); err != nil {
		log.Printf("grain: importing %s into the secrets store: %v -- still reading the file", path, err)
		return
	}
	if err := os.Rename(path, path+gitproxy.MigratedSuffix); err != nil {
		log.Printf("grain: renaming %s after importing it: %v", path, err)
		return
	}
	log.Printf("grain: moved %s into secret %s (kept as %s for a rollback)", path, name, path+gitproxy.MigratedSuffix)
}

// writeRepoToken stores a repository's explicit token and returns the
// path its settings' TokenFile records: into the store when there is one
// (removing a plaintext copy), else into that file.
func writeRepoToken(dataDir, name, token string) (string, error) {
	dir := SecretsDir(dataDir)
	path := filepath.Join(dir, name)
	token = strings.TrimSpace(token)
	if store := repoTokenSecrets(dataDir); store != nil {
		if err := store.Set(name, repoTokenKey, []byte(token)); err != nil {
			return "", fmt.Errorf("storing %s: %w", name, err)
		}
		for _, p := range []string{path, path + gitproxy.MigratedSuffix} {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				return "", err
			}
		}
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("preparing %s: %w", dir, err)
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	return path, nil
}

// restoreRepoTokenFiles writes each stored repository token back to its
// file and drops the store's copy: the rollback half for these two.
func restoreRepoTokenFiles(store *secrets.Store, dataDir string) {
	for _, name := range repoTokenNames {
		has, err := store.Has(name, repoTokenKey)
		if err != nil || !has {
			continue
		}
		v, err := store.Resolve(context.Background(), name+"/"+repoTokenKey)
		if err != nil {
			continue
		}
		path := filepath.Join(SecretsDir(dataDir), name)
		if err := writeFileAtomic0600(path, []byte(strings.TrimSpace(v)+"\n")); err != nil {
			log.Printf("grain: restoring %s: %v", path, err)
			continue
		}
		_ = store.DeleteKey(name, repoTokenKey)
		_ = os.Remove(path + gitproxy.MigratedSuffix)
		log.Printf("grain: %s=files: wrote %s back out of the secrets store", gitproxy.CredentialsInFilesEnv, path)
	}
}
