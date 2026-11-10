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

package secrets

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RunSecretsDir is the tmpfs some installs fetch credentials into at
// boot (scripts/grain-instance-init.sh). A copy of the key there is one
// wrap does not remove, so it is reported.
var RunSecretsDir = "/run/grain-secrets"

// KeyWrapReport is what `grain state key wrap` and `unwrap` tell the
// operator: where the key is now, how it is held, and every plaintext
// copy of it this host can see that the change left alone.
type KeyWrapReport struct {
	KeyFile    string        `json:"keyFile"`
	Public     string        `json:"public"`
	Protection KeyProtection `json:"protection"`
	// PlaintextCopies are files that still hold the private key in the
	// clear, for the operator to destroy once they have an escrow they
	// trust.
	PlaintextCopies []string `json:"plaintextCopies,omitempty"`
	// Notes are copies this host cannot see but the operator should
	// think about: an escrow's version history, above all.
	Notes []string `json:"notes,omitempty"`
}

// WrapKeyWithKMS replaces the key file with the key wrapped by the Cloud
// KMS key name (design §6.2). The key in memory is the one wrapped, so
// nothing restarts and secrets.enc is untouched. It wraps, unwraps the
// result and compares before it writes, and writes through a rename, so
// a failure at any point leaves the file as it was. c nil is
// DefaultKMSClient.
func (s *Store) WrapKeyWithKMS(ctx context.Context, name string, c *KMSClient) (KeyWrapReport, error) {
	if c == nil {
		c = DefaultKMSClient()
	}
	if err := ValidateKMSKeyName(name); err != nil {
		return KeyWrapReport{}, err
	}
	key, info, err := s.keyForWrap()
	if err != nil {
		return KeyWrapReport{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if info.Protection == ProtectionKMS && info.KMSKey == name {
		return KeyWrapReport{}, fmt.Errorf("secrets: %s is already wrapped by %s", s.cfg.KeyFile, name)
	}
	w, err := WrapKey(ctx, c, name, key)
	if err != nil {
		return KeyWrapReport{}, err
	}
	if err := writeKeyFileData(s.cfg.KeyFile, w.String()); err != nil {
		return KeyWrapReport{}, err
	}
	s.keyMu.Lock()
	s.lastUnwrap = s.clock() // WrapKey has just unwrapped it to check
	s.keyMu.Unlock()
	rep := s.wrapReport(key)
	rep.Notes = append(rep.Notes,
		"an escrow of the plaintext key -- the secrets-key secret in Secret Manager on terraform/gcp, "+
			"or wherever GRAIN_SECRETS_KEY came from -- still holds it: replace it with the wrapped file's "+
			"contents and destroy the plaintext versions",
		"every restart from now on unwraps through "+name+"; run `grain state key unwrap` before "+
			"downgrading to a build that cannot read a wrapped key")
	return rep, nil
}

// UnwrapKeyFromKMS is wrap's reversal: the key file goes back to the
// plaintext form. It asks KMS for the key the file holds and checks it
// is the one in memory, then writes the plaintext and reads it back
// before it is done. c nil is DefaultKMSClient.
func (s *Store) UnwrapKeyFromKMS(ctx context.Context, c *KMSClient) (KeyWrapReport, error) {
	if c == nil {
		c = DefaultKMSClient()
	}
	key, info, err := s.keyForWrap()
	if err != nil {
		return KeyWrapReport{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if info.Protection != ProtectionKMS {
		return KeyWrapReport{}, fmt.Errorf("secrets: %s is not wrapped", s.cfg.KeyFile)
	}
	wrapped, err := os.ReadFile(s.cfg.KeyFile)
	if err != nil {
		return KeyWrapReport{}, err
	}
	w, err := ParseWrappedKey(string(wrapped))
	if err != nil {
		return KeyWrapReport{}, err
	}
	fromKMS, _, err := UnwrapKey(ctx, c, w)
	if err != nil {
		return KeyWrapReport{}, err
	}
	if fromKMS.String() != key.String() {
		return KeyWrapReport{}, fmt.Errorf("secrets: %s unwraps to a different key than the one this store holds; nothing was written", s.cfg.KeyFile)
	}
	if err := writeKeyFileData(s.cfg.KeyFile, key.String()); err != nil {
		return KeyWrapReport{}, err
	}
	if back, err := ReadKeyFile(s.cfg.KeyFile); err != nil || back.String() != key.String() {
		// Put the wrapped form back rather than leave a key file that
		// does not read as this key.
		_ = writeKeyFileData(s.cfg.KeyFile, string(wrapped))
		if err == nil {
			err = errors.New("the plaintext key read back as a different key")
		}
		return KeyWrapReport{}, fmt.Errorf("secrets: writing %s: %w; the wrapped key was put back", s.cfg.KeyFile, err)
	}
	rep := s.wrapReport(key)
	rep.Notes = append(rep.Notes,
		"the key file is plaintext again: back it up, and replace any escrow that holds the wrapped form "+
			"(the secrets-key secret on terraform/gcp) if the host it seeds may not be able to reach "+w.KMSKey)
	return rep, nil
}

// keyForWrap is the key in memory and what the file says, checked to
// agree: a file that has changed under the store since it loaded -- an
// import from another process -- is not something to wrap or unwrap
// on the strength of a stale cache.
func (s *Store) keyForWrap() (Key, KeyFileInfo, error) {
	if s.backend != BackendFile {
		return Key{}, KeyFileInfo{}, errors.New("secrets: key wrapping applies only to the encrypted-file backend, not secretmanager")
	}
	if err := s.ready(); err != nil {
		return Key{}, KeyFileInfo{}, err
	}
	key, err := s.currentKey()
	if err != nil {
		return Key{}, KeyFileInfo{}, err
	}
	info, err := ReadKeyFileInfo(s.cfg.KeyFile)
	if err != nil {
		return Key{}, KeyFileInfo{}, err
	}
	if info.Public != key.Public() {
		s.forgetKey()
		return Key{}, KeyFileInfo{}, fmt.Errorf("secrets: %s now holds %s, not the %s this store loaded; try again", s.cfg.KeyFile, info.Public, key.Public())
	}
	return key, info, nil
}

func (s *Store) wrapReport(key Key) KeyWrapReport {
	return KeyWrapReport{
		KeyFile:         s.cfg.KeyFile,
		Public:          key.Public(),
		Protection:      s.KeyProtection(),
		PlaintextCopies: PlaintextKeyCopies(s.cfg.KeyFile, key),
	}
}

// PlaintextKeyCopies lists the files this host can see that hold key in
// the clear, other than keyFile itself: the archives `grain state key
// import` leaves beside it, and a copy fetched into RunSecretsDir.
// Archives of other keys are listed too -- each is a whole private key
// that opens whatever was sealed to it.
func PlaintextKeyCopies(keyFile string, key Key) []string {
	var out []string
	if retired, err := RetiredKeys(keyFile); err == nil {
		for _, rk := range retired {
			if _, err := ReadKeyFile(rk.Path); err == nil {
				out = append(out, rk.Path)
			}
		}
	}
	if entries, err := os.ReadDir(RunSecretsDir); err == nil {
		for _, e := range entries {
			p := filepath.Join(RunSecretsDir, e.Name())
			if e.IsDir() || p == keyFile {
				continue
			}
			if data, err := os.ReadFile(p); err == nil && strings.Contains(string(data), key.String()) {
				out = append(out, p)
			}
		}
	}
	return out
}
