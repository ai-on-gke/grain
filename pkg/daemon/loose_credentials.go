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
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
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
//   - secrets/relay/token, into relay/token, renamed to token.migrated.
//
// Each step logs what it did and warns about what is left; none stops the
// daemon from starting, since every reader still falls back to the file
// it would have read before.
func MoveLooseCredentialsIntoStore(cfg Config, store *secrets.Store) {
	moveGitCredentialsIntoStore(cfg.DataDir)
	if store == nil {
		return
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
