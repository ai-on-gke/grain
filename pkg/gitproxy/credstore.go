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

package gitproxy

// Where a credential's secret material is kept: the encrypted secrets
// store (pkg/secrets), under secret github-credential-<name>, rather
// than a plaintext 0600 file in the secrets directory
// (design/design/secrets-key-protection.design.md §5, Phase 3).
//
//	github-credential-bot     token      # was secrets/github/bot.token
//	github-credential-ci-app  app.json   # was secrets/github/ci-app.app.json
//	github-credential-deploy  ssh.json   # was secrets/github/deploy.ssh.json
//
// Each value is the old file's bytes, unchanged, so a credential reads
// the same whichever of the two it came from. The store is the file
// store beside the secrets directory (<data-dir>/secrets/secrets.enc and
// secrets.key) -- the one every process that builds a CredentialSet can
// already open, since each of them could already read the plaintext files
// it replaces. A directory with no secrets.key beside it (a test, a
// Secret Manager deployment that never minted one) keeps reading and
// writing files, as before.
//
// Files are still read when the store does not hold a credential, so a
// tree an older setup.sh just seeded, or one an operator restored for a
// rollback, keeps working until the daemon's next start imports it
// (MigrateFilesToStore). *.host.json and *.ambient.json hold nothing
// secret and stay files.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/secrets"
)

// CredentialSecretPrefix is the store secret a credential named <name>
// is kept under: github-credential-<name>.
const CredentialSecretPrefix = "github-credential-"

// CredentialSecretName is the store secret that holds credential name.
func CredentialSecretName(name string) string { return CredentialSecretPrefix + name }

// MigratedSuffix is appended to a credential file once its contents are
// in the store: the rollback copy, deleted one release after this one.
const MigratedSuffix = ".migrated"

// CredentialsInFilesEnv, set to "files", keeps every CredentialSet in this
// process on the plaintext files -- the rollback switch. The daemon,
// started with it, writes every store-held credential back out to its file
// (RestoreFilesFromStore), which is what an older build reads.
const CredentialsInFilesEnv = "GRAIN_GITHUB_CREDENTIALS"

// storedSuffixes are the credential kinds whose files hold a secret, and
// so are kept in the store. The store key is the suffix without its dot.
var storedSuffixes = []string{".token", ".app.json", ".ssh.json"}

func storeKeyFor(suffix string) string { return strings.TrimPrefix(suffix, ".") }

func isStoredSuffix(suffix string) bool {
	for _, s := range storedSuffixes {
		if s == suffix {
			return true
		}
	}
	return false
}

// credentialStore is a CredentialSet's view of the store. The snapshot
// is every github-credential-* value, decrypted once and reused until the
// store's file changes (stamp), since exists and load ask on every
// request and a decrypt per request is a cost the files never had.
type credentialStore struct {
	store *secrets.Store

	mu    sync.Mutex
	stamp storeStamp
	snap  map[string]map[string][]byte
}

type storeStamp struct {
	ok      bool
	size    int64
	modTime time.Time
}

// storeBeside opens the file store whose key sits in dir's parent
// (<data-dir>/secrets for <data-dir>/secrets/github), or nil when there is
// no key there or files were asked for. It never mints a key: a reader of
// the credential ladder has no business creating the deployment's
// secrets key.
func storeBeside(dir string) *secrets.Store {
	if dir == "" || os.Getenv(CredentialsInFilesEnv) == "files" {
		return nil
	}
	return openStoreBeside(dir)
}

func openStoreBeside(dir string) *secrets.Store {
	parent := filepath.Dir(filepath.Clean(dir))
	cfg := secrets.Config{
		File:    filepath.Join(parent, secrets.DefaultFileName),
		KeyFile: filepath.Join(parent, secrets.DefaultKeyFileName),
	}
	if _, err := os.Stat(cfg.KeyFile); err != nil {
		return nil
	}
	// One Store per directory per process: a Store loads its key once and
	// keeps it, and a KMS-wrapped key (secrets.KMSKeySource) is a network
	// unwrap, so a fresh Store for every LoadCredentialSet would be one
	// unwrap per call. A Store notices a key installed under it by another
	// (ErrWrongKey, then a reload), so sharing it loses nothing.
	if v, ok := besideStores.Load(cfg.KeyFile); ok {
		return v.(*secrets.Store)
	}
	store := secrets.Open(cfg)
	if err := store.Check(); err != nil {
		// Not cached: an unreadable store (a key not yet imported) is
		// worth opening afresh next time.
		return store
	}
	v, _ := besideStores.LoadOrStore(cfg.KeyFile, store)
	return v.(*secrets.Store)
}

// besideStores is openStoreBeside's per-key-file cache.
var besideStores sync.Map

// UseStore backs this set with store instead of the one beside its
// directory (nil: files only). For a caller that opened the deployment's
// store itself, and for tests.
func (c *CredentialSet) UseStore(store *secrets.Store) {
	if store == nil {
		c.creds = nil
	} else {
		c.creds = &credentialStore{store: store}
	}
	c.mu.Lock()
	c.cache = map[string]cacheEntry{}
	c.mu.Unlock()
}

// InStore reports whether this set keeps credentials in the secrets
// store rather than in files.
func (c *CredentialSet) InStore() bool { return c != nil && c.creds != nil }

func (s *credentialStore) currentStamp() storeStamp {
	info, err := os.Stat(s.store.File())
	if err != nil {
		return storeStamp{ok: true}
	}
	return storeStamp{ok: true, size: info.Size(), modTime: info.ModTime()}
}

// snapshot is the store's credentials, reloaded when its file changed.
// A store that cannot be read is logged and answers nothing, so reads
// fall back to files rather than failing every request outright.
func (s *credentialStore) snapshot() map[string]map[string][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	stamp := s.currentStamp()
	if s.snap != nil && stamp == s.stamp {
		return s.snap
	}
	values, err := s.store.KnownValues(context.Background())
	if err != nil {
		log.Printf("gitproxy: reading git credentials from the secrets store: %v", err)
		return nil
	}
	snap := map[string]map[string][]byte{}
	for _, v := range values {
		name, ok := strings.CutPrefix(v.SecretName, CredentialSecretPrefix)
		if !ok || name == "" {
			continue
		}
		if snap[name] == nil {
			snap[name] = map[string][]byte{}
		}
		snap[name][v.Key] = []byte(v.Value)
	}
	s.snap, s.stamp = snap, stamp
	return snap
}

func (s *credentialStore) invalidate() {
	s.mu.Lock()
	s.snap = nil
	s.mu.Unlock()
}

// readKind is name's credential material of the given kind (".token",
// ".app.json", ".ssh.json"): from the store when it holds it, else from
// the file. ok is false when neither has it; err is a file that is there
// and cannot be read.
func (c *CredentialSet) readKind(name, suffix string) ([]byte, bool, error) {
	if c.creds != nil && isStoredSuffix(suffix) {
		if v, ok := c.creds.snapshot()[name][storeKeyFor(suffix)]; ok {
			return v, true, nil
		}
	}
	data, err := os.ReadFile(filepath.Join(c.dir, name+suffix))
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	return data, true, nil
}

// hasKind reports whether name has credential material of that kind.
func (c *CredentialSet) hasKind(name, suffix string) bool {
	if c.creds != nil && isStoredSuffix(suffix) {
		if _, ok := c.creds.snapshot()[name][storeKeyFor(suffix)]; ok {
			return true
		}
	}
	_, err := os.Stat(filepath.Join(c.dir, name+suffix))
	return err == nil
}

// writeKind stores name's credential material of that kind: into the
// store, removing a plaintext file of the same kind it supersedes, or
// into the file when this set has no store.
func (c *CredentialSet) writeKind(name, suffix string, data []byte) error {
	if c.creds == nil || !isStoredSuffix(suffix) {
		return writeFileAtomic(c.dir, name+suffix, data)
	}
	if err := c.creds.store.Set(CredentialSecretName(name), storeKeyFor(suffix), data); err != nil {
		return err
	}
	c.creds.invalidate()
	// The rollback copy goes too: it is the value this write replaces,
	// and leaving it would keep a superseded credential in plaintext.
	path := filepath.Join(c.dir, name+suffix)
	for _, p := range []string{path, path + MigratedSuffix} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// removeKind deletes name's material of that kind wherever it is: the
// store, the file, and the file's rollback copy, so a credential an
// operator removes leaves no plaintext behind. removed is whether any of
// them was there.
func (c *CredentialSet) removeKind(name, suffix string) (bool, error) {
	var removed bool
	if c.creds != nil && isStoredSuffix(suffix) {
		if _, ok := c.creds.snapshot()[name][storeKeyFor(suffix)]; ok {
			if err := c.creds.store.DeleteKey(CredentialSecretName(name), storeKeyFor(suffix)); err != nil {
				return false, err
			}
			c.creds.invalidate()
			removed = true
		}
	}
	path := filepath.Join(c.dir, name+suffix)
	switch err := os.Remove(path); {
	case err == nil:
		removed = true
	case !os.IsNotExist(err):
		return removed, err
	}
	if c.creds != nil && isStoredSuffix(suffix) {
		if err := os.Remove(path + MigratedSuffix); err != nil && !os.IsNotExist(err) {
			return removed, err
		}
	}
	return removed, nil
}

// storedNames is every credential name the store holds material for.
func (c *CredentialSet) storedNames() []string {
	if c.creds == nil {
		return nil
	}
	snap := c.creds.snapshot()
	names := make([]string, 0, len(snap))
	for name := range snap {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// MigratedFile is one credential file MigrateFilesToStore moved.
type MigratedFile struct {
	Name string // credential name
	File string // the file's path before it was renamed to File+MigratedSuffix
}

// MigrateFilesToStore imports every <name>.token, <name>.app.json and
// <name>.ssh.json file in this set's directory into the store: each is
// Set, read back from a fresh decrypt of the store's file and compared
// byte for byte, and only then renamed to <file>.migrated, the copy a
// rollback restores. A file that fails any step is left where it is, and
// logged, and is still read as before.
//
// It is safe to run on every start. A tree already migrated has no such
// files and does nothing. A file that is back (an older setup.sh re-seeded
// it, or an operator rolled back and forward again) is the newer word and
// overwrites what the store holds, and its earlier .migrated copy.
//
// A set with no store migrates nothing.
func (c *CredentialSet) MigrateFilesToStore() ([]MigratedFile, error) {
	if c.creds == nil {
		return nil, nil
	}
	entries, err := os.ReadDir(c.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var moved []MigratedFile
	var errs []error
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		name, suffix, ok := storedFileName(e.Name())
		if !ok || namesOneFile(name) != nil {
			continue
		}
		path := filepath.Join(c.dir, e.Name())
		if err := c.migrateOne(name, suffix, path); err != nil {
			log.Printf("gitproxy: not moving %s into the secrets store: %v -- it is still read from there", path, err)
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			continue
		}
		log.Printf("gitproxy: moved git credential %q from %s into the secrets store (secret %s, key %s); "+
			"the file is kept as %s for a rollback and can be deleted once this release is settled",
			name, path, CredentialSecretName(name), storeKeyFor(suffix), path+MigratedSuffix)
		moved = append(moved, MigratedFile{Name: name, File: path})
		c.forget(name)
	}
	return moved, errors.Join(errs...)
}

func (c *CredentialSet) migrateOne(name, suffix, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	secret, key := CredentialSecretName(name), storeKeyFor(suffix)
	if err := c.creds.store.Set(secret, key, data); err != nil {
		return err
	}
	c.creds.invalidate()
	// Verified through a Store of its own, which decrypts the file on
	// disk afresh, rather than through anything this process cached.
	verify := c.creds.store
	if c.creds.store.KeyFile() != "" {
		verify = secrets.Open(secrets.Config{File: c.creds.store.File(), KeyFile: c.creds.store.KeyFile()})
	}
	got, err := verify.Resolve(context.Background(), secret+"/"+key)
	if err != nil {
		return fmt.Errorf("reading it back: %w", err)
	}
	if !bytes.Equal([]byte(got), data) {
		return errors.New("the store read back something other than what was written")
	}
	return os.Rename(path, path+MigratedSuffix)
}

// RestoreFilesFromStore is MigrateFilesToStore's reversal: every
// credential the store holds is written back to its file, from the
// store's current value (which a Settings edit since the migration may
// have changed), a .migrated copy with no store value is renamed back,
// and the store's copy is deleted, so the files are once more the only
// answer -- what an older build reads. It opens the store beside the
// directory even when CredentialsInFilesEnv is set, since that is when it
// is run.
func (c *CredentialSet) RestoreFilesFromStore() ([]string, error) {
	store := openStoreBeside(c.dir)
	if c.creds != nil {
		store = c.creds.store
	}
	var restored []string
	var errs []error
	if store != nil {
		cs := &credentialStore{store: store}
		for name, kinds := range cs.snapshot() {
			if namesOneFile(name) != nil {
				continue
			}
			for key, value := range kinds {
				suffix := "." + key
				if !isStoredSuffix(suffix) {
					continue
				}
				if err := writeFileAtomic(c.dir, name+suffix, value); err != nil {
					errs = append(errs, err)
					continue
				}
				if err := store.DeleteKey(CredentialSecretName(name), key); err != nil {
					errs = append(errs, err)
					continue
				}
				_ = os.Remove(filepath.Join(c.dir, name+suffix+MigratedSuffix))
				restored = append(restored, filepath.Join(c.dir, name+suffix))
			}
		}
		if c.creds != nil {
			c.creds.invalidate()
		}
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil && !os.IsNotExist(err) {
		errs = append(errs, err)
	}
	for _, e := range entries {
		orig, ok := strings.CutSuffix(e.Name(), MigratedSuffix)
		if !ok || e.IsDir() {
			continue
		}
		if _, _, ok := storedFileName(orig); !ok {
			continue
		}
		from, to := filepath.Join(c.dir, e.Name()), filepath.Join(c.dir, orig)
		if _, err := os.Stat(to); err == nil {
			_ = os.Remove(from)
			continue
		}
		if err := os.Rename(from, to); err != nil {
			errs = append(errs, err)
			continue
		}
		restored = append(restored, to)
	}
	sort.Strings(restored)
	c.mu.Lock()
	c.cache = map[string]cacheEntry{}
	c.mu.Unlock()
	return restored, errors.Join(errs...)
}

// storedFileName splits a credential file whose kind is kept in the
// store into its name and suffix.
func storedFileName(file string) (string, string, bool) {
	for _, suffix := range storedSuffixes {
		if name, ok := strings.CutSuffix(file, suffix); ok && name != "" {
			return name, suffix, true
		}
	}
	return "", "", false
}
