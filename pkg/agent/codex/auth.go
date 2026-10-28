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

package codex

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// BackendAuthFile isolates refreshable logins by backend ID. Hashing the ID
// keeps even manually configured identifiers inside the secrets directory.
func BackendAuthFile(sharedPath, backendID string) string {
	if sharedPath == "" || backendID == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(sharedPath), fmt.Sprintf("codex-auth-%x.json", sha256.Sum256([]byte(backendID))))
}

// ValidateAuth checks the stable outer contract Grain relies on without
// taking ownership of Codex's evolving credential schema. It deliberately
// validates only the outer object and never returns credential contents.
func ValidateAuth(data []byte) error {
	var object map[string]json.RawMessage
	if len(data) == 0 || json.Unmarshal(data, &object) != nil || object == nil {
		return fmt.Errorf("codex: auth.json is not a JSON object")
	}
	return nil
}

// WriteAuthFile validates and atomically installs a UI-supplied auth.json.
// The file stays plaintext because Codex must update it when OAuth tokens
// rotate; mode 0600 keeps it private to the daemon account.
func WriteAuthFile(path string, data []byte) error {
	if err := ValidateAuth(data); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("codex: preparing shared auth directory: %w", err)
	}
	// The new login replaces the file path resolves to, as a refresh
	// does: renaming over a symlink would cut whatever else shares the
	// login it points at, and leave that file on the old tokens.
	target := resolvedOrSelf(path)
	// Under the refresh lock, so a refresh already in flight cannot
	// rename the old login back over this one.
	unlock, err := lockLogin(context.Background(), target)
	if err != nil {
		return err
	}
	defer unlock()
	tmp, err := os.CreateTemp(filepath.Dir(target), ".auth-*")
	if err != nil {
		return fmt.Errorf("codex: creating shared auth file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("codex: protecting shared auth file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("codex: writing shared auth file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("codex: closing shared auth file: %w", err)
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return fmt.Errorf("codex: installing shared auth file: %w", err)
	}
	os.Remove(failurePath(target))
	return nil
}

// RemoveAuthFile deletes a login, under the refresh lock so an in-flight
// refresh cannot write it back, along with any refusal recorded for it.
// It reports os.ErrNotExist (wrapped) when there was no login.
func RemoveAuthFile(path string) error {
	target := resolvedOrSelf(path)
	unlock, err := lockLogin(context.Background(), target)
	if err != nil {
		if _, statErr := os.Lstat(path); os.IsNotExist(statErr) {
			return statErr
		}
		return err
	}
	defer unlock()
	os.Remove(failurePath(target))
	return os.Remove(path)
}

// resolvedOrSelf is path with symlinks resolved, or path itself when it
// does not exist yet.
func resolvedOrSelf(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// ValidateAuthFile checks the same contract on a file and also proves that
// the Codex subprocess can persist refreshed tokens through it.
func ValidateAuthFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("codex: reading shared auth file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("codex: shared auth file %q is not a regular file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("codex: reading shared auth file: %w", err)
	}
	if err := ValidateAuth(data); err != nil {
		return fmt.Errorf("codex: shared auth file %q is not a JSON object", path)
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("codex: shared auth file must be writable for token refresh: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("codex: closing shared auth file after checking it: %w", err)
	}
	return nil
}
