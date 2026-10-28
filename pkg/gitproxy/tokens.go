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

// Sandbox identity: a per-sandbox bearer token, consumed via HTTP Basic
// auth. docs/design.md is specific that this is "not an SSH key" and that
// git consumes it via a credential helper -- a sandbox's git credential
// helper supplies the token as the password half of HTTP Basic auth; the
// username is irrelevant, and no command grain writes has to carry it.
//
// That is not the same as the agent never being able to see it: the
// helper's backing file (~/.git-credentials, mcp.ConfigureGitCredentials)
// is plaintext and readable by the agent's own shell. So nothing here
// relies on the token staying secret from the sandbox it identifies. A
// copied token authorizes only what Authorizer already lets that sandbox
// do, stops resolving once its run ends (Revoke, and Authorizer reading
// the live run), and expires after its TTL -- the backstop for a token
// whose Revoke is never called. The goroutine running a live run keeps
// extending that TTL (RefreshToken), so a run that outlives it keeps its
// access, while a token orphaned by a dead daemon still lapses.
//
// This identity is deliberately separate from Authorizer (authorize.go):
// SandboxTokens answers "who is calling" from a secret only the proxy and
// the sandbox share, minted once and never touched by the task model.
// Authorizer then answers "what may they touch" by reading the model live
// -- the token is how the proxy learns *which* sandbox to ask the model
// about, not what the model itself contains.
//
// Tokens are stored encrypted at rest (pkg/secrets), sealed to a key of
// their own -- sandbox-tokens.key, beside the file; see tokenKeyPath --
// with a configurable TTL / expiration. With the key beside the file, that
// protects against the file being copied on its own (a backup, a state
// dump), not against anyone who can read the whole directory.

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/secrets"
)

const (
	// DefaultSandboxTokenTTL is how long a minted sandbox token is valid for.
	DefaultSandboxTokenTTL = 24 * time.Hour
)

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// Grant is a per-attempt sandbox credential issued for one sandbox run.
// When TaskID and Attempt are bound at token issuance, the proxy refuses the
// token if the sandbox is later running a different task or a newer attempt.
type Grant struct {
	Sandbox string    `json:"sandbox,omitempty"`
	TaskID  string    `json:"taskId,omitempty"`
	Attempt int       `json:"attempt,omitempty"`
	Token   string    `json:"token"`
	Expires time.Time `json:"expires,omitempty"`
}

// tokenRecord is the on-disk and in-memory representation of a token.
type tokenRecord = Grant

// tokenKeyPath is the dedicated key sandbox-tokens.json is sealed with:
// sandbox-tokens.key, beside the file it seals.
//
// It used to be sealed with whatever secrets.key sat in that directory,
// which is the operator's deployment secrets key (cmd/grain's own
// secretsConfig). That key is the operator's to import or replace --
// secrets.Store.ImportKey archives it and writes another in its place --
// and every replacement left this file encrypted to a key nobody held any
// more: every mint failed, and so did the proxy's startup. The tokens are
// the proxy's own short-lived secret, re-minted per run, so they get a key
// of their own that nothing else touches; and gitproxy no longer mints the
// operator's secrets.key itself when there is none yet, which skipped the
// "back this file up" notice the state repo prints when it creates one.
func tokenKeyPath(tokenFilePath string) string {
	return strings.TrimSuffix(tokenFilePath, filepath.Ext(tokenFilePath)) + ".key"
}

// sealKey returns the key the token file is written with: the explicit
// key or key file when one is configured, otherwise the dedicated key
// (tokenKeyPath), generated on first use.
func sealKey(explicitKey *secrets.Key, keyFile, tokenFilePath string) (secrets.Key, error) {
	if explicitKey != nil && explicitKey.Public() != "" {
		return *explicitKey, nil
	}
	target := keyFile
	if target == "" {
		target = tokenKeyPath(tokenFilePath)
	}
	if k, err := secrets.ReadKeyFile(target); err == nil {
		return k, nil
	} else if !errors.Is(err, secrets.ErrNoKey) {
		return secrets.Key{}, fmt.Errorf("gitproxy: %w", err)
	}
	k, err := secrets.GenerateKey()
	if err != nil {
		return secrets.Key{}, fmt.Errorf("gitproxy: generating a key for %s: %w", tokenFilePath, err)
	}
	// Written aside and linked into place, so two stores minting at once
	// cannot each seal a file to a key the other then overwrites: the
	// loser of the link reads back the winner's key instead.
	suffix, err := randomHex(8)
	if err != nil {
		return secrets.Key{}, err
	}
	tmp := target + "." + suffix + ".tmp"
	if err := secrets.WriteKeyFile(tmp, k); err != nil {
		return secrets.Key{}, fmt.Errorf("gitproxy: saving the key for %s to %s: %w", tokenFilePath, target, err)
	}
	defer os.Remove(tmp)
	if err := os.Link(tmp, target); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return secrets.ReadKeyFile(target)
		}
		return secrets.Key{}, fmt.Errorf("gitproxy: saving the key for %s to %s: %w", tokenFilePath, target, err)
	}
	return k, nil
}

// openKeys lists the keys the token file may have been sealed with, in
// the order they are tried: the explicit key or key file when one is
// configured (and nothing else, since that is what the file is written
// with), otherwise the dedicated key and then the operator's secrets.key
// the file was sealed with before it had one -- read so a deployment
// upgrading with runs in flight keeps their tokens, never written with.
func openKeys(explicitKey *secrets.Key, keyFile, tokenFilePath string) []secrets.Key {
	if explicitKey != nil && explicitKey.Public() != "" {
		return []secrets.Key{*explicitKey}
	}
	var paths []string
	if keyFile != "" {
		paths = []string{keyFile}
	} else {
		dir := filepath.Dir(tokenFilePath)
		paths = []string{
			tokenKeyPath(tokenFilePath),
			filepath.Join(dir, secrets.DefaultKeyFileName),
			filepath.Join(filepath.Dir(dir), "secrets", secrets.DefaultKeyFileName),
		}
	}
	var keys []secrets.Key
	for _, p := range paths {
		if k, err := secrets.ReadKeyFile(p); err == nil {
			keys = append(keys, k)
		}
	}
	return keys
}

func parseTokensJSON(data []byte) (map[string]tokenRecord, error) {
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawMap); err != nil {
		return nil, err
	}
	out := make(map[string]tokenRecord, len(rawMap))
	for sandbox, raw := range rawMap {
		var strToken string
		if err := json.Unmarshal(raw, &strToken); err == nil {
			out[sandbox] = tokenRecord{
				Sandbox: sandbox,
				Token:   strToken,
			}
			continue
		}
		var rec tokenRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return nil, fmt.Errorf("parsing token record for %q: %w", sandbox, err)
		}
		if rec.Sandbox == "" {
			rec.Sandbox = sandbox
		}
		out[sandbox] = rec
	}
	return out, nil
}

// readTokenFile reads the token map at path. A file sealed to a key none
// of openKeys holds reads as empty, logged, rather than as an error: its
// tokens are short-lived and re-minted per run, so losing them costs each
// live sandbox its git access until its next mint, where failing would
// cost every future mint and the proxy's own startup (see tokenKeyPath).
// The next write replaces the file, sealed to a key that is there.
func readTokenFile(path string, explicitKey *secrets.Key, keyFile string) (map[string]tokenRecord, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]tokenRecord{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("gitproxy: reading %s: %w", path, err)
	}
	return decodeTokenFile(path, data, explicitKey, keyFile)
}

// readTokenFileVersion is readTokenFile, also returning the stat of the
// very file it read (nil when there is none) -- taken from the open
// descriptor, so it describes those bytes even if the path is replaced
// between the open and the read.
func readTokenFileVersion(path string, explicitKey *secrets.Key, keyFile string) (map[string]tokenRecord, fs.FileInfo, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return map[string]tokenRecord{}, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("gitproxy: reading %s: %w", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, fmt.Errorf("gitproxy: reading %s: %w", path, err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, nil, fmt.Errorf("gitproxy: reading %s: %w", path, err)
	}
	records, err := decodeTokenFile(path, data, explicitKey, keyFile)
	if err != nil {
		return nil, nil, err
	}
	return records, info, nil
}

// decodeTokenFile parses the token file's contents, decrypting them
// first when they are sealed.
func decodeTokenFile(path string, data []byte, explicitKey *secrets.Key, keyFile string) (map[string]tokenRecord, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return map[string]tokenRecord{}, nil
	}

	rawJSON := data
	if strings.HasPrefix(trimmed, secrets.ArmourBegin) {
		rawJSON = nil
		for _, k := range openKeys(explicitKey, keyFile, path) {
			plain, err := secrets.Decrypt(k, data)
			if err == nil {
				rawJSON = plain
				break
			}
			if !errors.Is(err, secrets.ErrWrongKey) {
				return nil, fmt.Errorf("gitproxy: decrypting %s: %w", path, err)
			}
		}
		if rawJSON == nil {
			log.Printf("gitproxy: %s is sealed to a key this deployment no longer has "+
				"(was the key beside it replaced or removed?) -- treating it as holding no tokens; "+
				"each sandbox gets a fresh one at its next mint", path)
			return map[string]tokenRecord{}, nil
		}
	}

	records, err := parseTokensJSON(rawJSON)
	if err != nil {
		return nil, fmt.Errorf("gitproxy: parsing %s: %w", path, err)
	}
	return records, nil
}

// SandboxTokens maps a bearer token to the sandbox it identifies.
//
// File format: encrypted JSON containing {"sandbox-0": {"token": "<token>", "expires": "<time>"}, ...},
// or legacy plaintext JSON, minted as each sandbox is created -- see docs/design.md, "sandbox identity."
//
// It re-reads that file when it is shown a token it does not recognise
// and the file has changed since it last read it. That used to be unnecessary and was deliberately not done:
// every sandbox was a slot, every slot's token was minted before the
// proxy started, and the map could be read once and then treated as
// fixed for the life of the process. A sandbox created per run mints its
// token while the proxy is already serving, so a map pinned at startup
// would reject every git operation from every run -- fail-closed, but
// closed against everything.
//
// Re-reading when the file has changed rather than on every request keeps
// the ordinary path an os.Stat and a map lookup with no read/decrypt I/O.
// It re-reads only when the file has changed since it was last read: an
// os.Stat, outside the lock, compared with the stat of the file that
// read came from (tokenFileVersion). Without that, every unknown token
// (or check against a modified file) cost a read and a decryption under
// the write lock, so a flood of distinct bogus tokens serialized all of
// the proxy's authentication behind file I/O; with it, the flood costs
// one stat each and no reads, while a revoked token or freshly minted token
// reloads immediately as soon as the file changes.
// A token that is unknown because it is new or revoked is never missed: the store
// writes by rename (SandboxTokenStore.save), so the minting or revoking write
// replaces the file the stat describes, and tokenFileVersion says when a
// stat alone cannot be trusted to show that.
type SandboxTokens struct {
	path    string
	KeyFile string
	Key     *secrets.Key
	Now     func() time.Time

	mu      sync.RWMutex
	byToken map[string]tokenRecord
	version tokenFileVersion
	// reads counts the file reads this has made, for tests.
	reads int
}

// tokenFileRacyWindow is how long after a file's mtime its stat is not
// trusted to tell it apart from a replacement. It has to exceed the
// filesystem's mtime granularity -- a jiffy on ext4, xfs and tmpfs, a
// second or two on older and network filesystems.
const tokenFileRacyWindow = 2 * time.Second

// tokenFileVersion identifies the token file a SandboxTokens last read,
// so a miss can tell whether reading it again could say anything new.
type tokenFileVersion struct {
	read   bool        // a read has succeeded at all
	info   fs.FileInfo // the stat of the file read; nil when there was none
	readAt time.Time   // wall-clock time taken just before that read
}

// unchanged reports whether info -- a fresh stat of the path, nil when
// nothing is there -- is certainly the file v was read from, unmodified.
//
// The file is only ever replaced whole, by rename, and a file cannot be
// renamed over by one sharing its inode while it is still linked, so a
// replacement differs in identity -- unless two replacements have come
// since and the second reused the inode the first freed. Size and mtime
// separate that case except when its mtime lands in the same tick as the
// file v read. Both replacements came after that read began, so this
// happens only if the file read was itself written within a tick of
// readAt: within tokenFileRacyWindow of it, a stat is never trusted, and
// a miss re-reads. (The same "racy" rule git applies to its index.)
func (v tokenFileVersion) unchanged(info fs.FileInfo) bool {
	if !v.read {
		return false
	}
	if v.info == nil || info == nil {
		return v.info == nil && info == nil
	}
	if !os.SameFile(v.info, info) || v.info.Size() != info.Size() || !v.info.ModTime().Equal(info.ModTime()) {
		return false
	}
	return v.readAt.Sub(v.info.ModTime()) >= tokenFileRacyWindow
}

// statTokenFile stats path for tokenFileVersion.unchanged: nil info for
// a file that is not there, and ok false when the stat itself failed.
func statTokenFile(path string) (info fs.FileInfo, ok bool) {
	info, err := os.Stat(path)
	if err == nil {
		return info, true
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, true
	}
	return nil, false
}

// LoadSandboxTokens reads the token map from path. A missing file yields
// an empty map, matching the Python original: the git-proxy service can
// be enabled before any sandbox has ever minted a token.
func LoadSandboxTokens(path string) (*SandboxTokens, error) {
	t := &SandboxTokens{path: path}
	if err := t.reload(); err != nil {
		return nil, err
	}
	return t, nil
}

func (t *SandboxTokens) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

// Authenticate returns the sandbox name owning this token, and false if it
// is unknown or expired -- unknown meaning "not in the file as of just now,"
// since a miss against the cached map (or a cached hit whose backing file has
// changed since it was read) re-reads the file before answering whenever it
// has changed since it was last read (see the type's own doc comment).
func (t *SandboxTokens) Authenticate(token string) (string, bool) {
	g, ok := t.AuthenticateGrant(token)
	if !ok {
		return "", false
	}
	return g.Sandbox, true
}

// AuthenticateGrant returns the full Grant owning this token (including any
// bound TaskID and Attempt), and false if the token is unknown or expired.
func (t *SandboxTokens) AuthenticateGrant(token string) (Grant, bool) {
	if info, statted := statTokenFile(t.path); statted {
		t.mu.RLock()
		grant, ok := t.lookupGrantLocked(token)
		unchanged := t.version.unchanged(info)
		t.mu.RUnlock()
		if unchanged {
			return grant, ok
		}
	}
	return t.reloadAndLookupGrant(token)
}

// lookupLocked answers for token from the cached map. The caller holds
// t.mu.
func (t *SandboxTokens) lookupLocked(token string) (string, bool) {
	g, ok := t.lookupGrantLocked(token)
	if !ok {
		return "", false
	}
	return g.Sandbox, true
}

func (t *SandboxTokens) lookupGrantLocked(token string) (Grant, bool) {
	rec, ok := t.byToken[token]
	if !ok {
		return Grant{}, false
	}
	if !rec.Expires.IsZero() && t.now().After(rec.Expires) {
		return Grant{}, false
	}
	return rec, true
}

// reloadAndLookup re-reads the file and answers for token, both under one
// hold of the write lock. It stats the file again once it holds the lock,
// so callers queued behind one reload do not each repeat it.
func (t *SandboxTokens) reloadAndLookup(token string) (string, bool) {
	g, ok := t.reloadAndLookupGrant(token)
	if !ok {
		return "", false
	}
	return g.Sandbox, true
}

func (t *SandboxTokens) reloadAndLookupGrant(token string) (Grant, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if info, statted := statTokenFile(t.path); statted && t.version.unchanged(info) {
		return t.lookupGrantLocked(token)
	}
	if err := t.reloadLocked(); err != nil {
		return Grant{}, false
	}
	return t.lookupGrantLocked(token)
}

// reload takes the write lock and re-reads the file -- LoadSandboxTokens'
// own first read, where nothing else holds a reference yet.
func (t *SandboxTokens) reload() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.reloadLocked()
}

// reloadLocked replaces the cached map with the file's current contents.
// The caller holds t.mu for writing.
func (t *SandboxTokens) reloadLocked() error {
	readAt := time.Now()
	t.reads++
	records, info, err := readTokenFileVersion(t.path, t.Key, t.KeyFile)
	if err != nil {
		return err
	}
	t.version = tokenFileVersion{read: true, info: info, readAt: readAt}
	byToken := make(map[string]tokenRecord, len(records))
	now := t.now()
	for _, rec := range records {
		if rec.Expires.IsZero() || !now.After(rec.Expires) {
			byToken[rec.Token] = rec
		}
	}
	t.byToken = byToken
	return nil
}

// SandboxTokenStore is the write side of the same sandbox-tokens.json file
// SandboxTokens reads -- called as each run's sandbox is prepared, to mint
// that sandbox's token (orchestrator's own runOne). Split from
// SandboxTokens because the two do different things to the same file: the
// proxy only ever looks tokens up, caching what it has read, while
// minting one is a read-modify-write against the file the proxy trusts.
type SandboxTokenStore struct {
	path    string
	KeyFile string
	Key     *secrets.Key
	TTL     time.Duration
	Now     func() time.Time

	mu sync.Mutex
}

// NewSandboxTokenStore returns a SandboxTokenStore writing the token file
// at path.
func NewSandboxTokenStore(path string) *SandboxTokenStore {
	return &SandboxTokenStore{path: path}
}

func (s *SandboxTokenStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *SandboxTokenStore) ttl() time.Duration {
	if s.TTL > 0 {
		return s.TTL
	}
	return DefaultSandboxTokenTTL
}

// EnsureToken returns the sandbox's existing token if valid, or a freshly minted
// one recorded to disk. Idempotent per sandbox name, and safe to call
// from several goroutines at once -- see the type's own doc comment.
func (s *SandboxTokenStore) EnsureToken(sandbox string) (string, error) {
	return s.EnsureGrant(sandbox, "", 0)
}

// EnsureGrant returns the sandbox's token bound to (taskID, attempt), reusing an
// unexpired token only when its bound TaskID and Attempt match (or were unbound),
// and minting a fresh token when the sandbox is starting a different task or attempt.
func (s *SandboxTokenStore) EnsureGrant(sandbox, taskID string, attempt int) (string, error) {
	g, err := s.Issue(sandbox, taskID, attempt)
	if err != nil {
		return "", err
	}
	return g.Token, nil
}

// Issue mints or reuses a per-attempt sandbox token Grant bound to (sandbox, taskID, attempt).
func (s *SandboxTokenStore) Issue(sandbox, taskID string, attempt int) (Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tokens, err := readTokenFile(s.path, s.Key, s.KeyFile)
	if err != nil {
		return Grant{}, err
	}
	now := s.now()
	ttl := s.ttl()
	if rec, ok := tokens[sandbox]; ok {
		if rec.Expires.IsZero() || !now.After(rec.Expires) {
			sameTask := taskID == "" || rec.TaskID == "" || rec.TaskID == taskID
			sameAttempt := attempt == 0 || rec.Attempt == 0 || rec.Attempt == attempt
			if sameTask && sameAttempt {
				updated := false
				if rec.TaskID == "" && taskID != "" {
					rec.TaskID = taskID
					updated = true
				}
				if rec.Attempt == 0 && attempt != 0 {
					rec.Attempt = attempt
					updated = true
				}
				if updated {
					tokens[sandbox] = rec
					if err := s.save(tokens); err != nil {
						return Grant{}, err
					}
				}
				return rec, nil
			}
		}
	}
	token, err := randomHex(32)
	if err != nil {
		return Grant{}, err
	}
	grant := Grant{
		Sandbox: sandbox,
		TaskID:  taskID,
		Attempt: attempt,
		Token:   token,
		Expires: now.Add(ttl),
	}
	tokens[sandbox] = grant
	return grant, s.save(tokens)
}

// ErrNoSandboxToken is what RefreshToken reports for a sandbox holding
// no token: never minted one, or already revoked.
var ErrNoSandboxToken = errors.New("gitproxy: no token recorded for this sandbox")

// RefreshToken extends the sandbox's token to now + TTL and returns it --
// the orchestrator's own keepSandboxTokenFresh, on a timer, for as long as
// the run on that sandbox is live, so a run that outlives the TTL keeps
// its git access. The TTL stays the backstop for a token whose Revoke
// never comes (the daemon dying mid-run): nothing refreshes that one.
//
// A token already past its expiry is extended all the same, since the
// sandbox still holds it and whoever is refreshing it says its run is
// live. A sandbox with no token is ErrNoSandboxToken rather than a fresh
// mint: a new token is one the sandbox's credential helper has never
// been given, and minting one here would also resurrect an entry Revoke
// just dropped.
func (s *SandboxTokenStore) RefreshToken(sandbox string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tokens, err := readTokenFile(s.path, s.Key, s.KeyFile)
	if err != nil {
		return "", err
	}
	rec, ok := tokens[sandbox]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrNoSandboxToken, sandbox)
	}
	rec.Expires = s.now().Add(s.ttl())
	tokens[sandbox] = rec
	return rec.Token, s.save(tokens)
}

// Rotate mints and records a fresh token unconditionally, replacing any
// existing one.
func (s *SandboxTokenStore) Rotate(sandbox string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tokens, err := readTokenFile(s.path, s.Key, s.KeyFile)
	if err != nil {
		return "", err
	}
	token, err := randomHex(32)
	if err != nil {
		return "", err
	}
	tokens[sandbox] = tokenRecord{
		Sandbox: sandbox,
		Token:   token,
		Expires: s.now().Add(s.ttl()),
	}
	return token, s.save(tokens)
}

// Revoke drops a sandbox's token, so this file holds one entry per
// sandbox that still exists rather than one per run the deployment has
// ever dispatched.
func (s *SandboxTokenStore) Revoke(sandbox string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tokens, err := readTokenFile(s.path, s.Key, s.KeyFile)
	if err != nil {
		return err
	}
	if _, ok := tokens[sandbox]; !ok {
		return nil
	}
	delete(tokens, sandbox)
	return s.save(tokens)
}

// save writes encrypted tokens atomically: a temp file plus rename.
// Callers hold s.mu.
//
// A record with no expiry is a legacy plaintext one, from before tokens
// had a TTL; it is stamped with a full TTL from now, so it stops being a
// token that never expires once this file is next written.
func (s *SandboxTokenStore) save(tokens map[string]tokenRecord) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	expires := s.now().Add(s.ttl())
	for name, rec := range tokens {
		if rec.Expires.IsZero() {
			rec.Expires = expires
			tokens[name] = rec
		}
	}
	data, err := json.MarshalIndent(tokens, "", "  ")
	if err != nil {
		return err
	}
	key, err := sealKey(s.Key, s.KeyFile, s.path)
	if err != nil {
		return err
	}
	sealed, err := secrets.Encrypt(key, data)
	if err != nil {
		return fmt.Errorf("gitproxy: encrypting tokens: %w", err)
	}
	suffix, err := randomHex(8)
	if err != nil {
		return err
	}
	tmp := s.path + "." + suffix + ".tmp"
	if err := os.WriteFile(tmp, sealed, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ExtractBasicAuthToken pulls the token out of an
// "Authorization: Basic base64(user:token)" header. The username is
// ignored -- a credential helper is configured with an arbitrary username
// and the token as the password, so the token is what identifies the
// caller.
func ExtractBasicAuthToken(header string) (string, bool) {
	const prefix = "Basic "
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(header[len(prefix):])
	if err != nil {
		return "", false
	}
	_, token, ok := strings.Cut(string(decoded), ":")
	if !ok {
		return "", false
	}
	return token, true
}
