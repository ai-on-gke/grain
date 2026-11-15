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

// Package secrets is a model.CredentialResolver backed by a single
// encrypted file, which lives in grain's state repository
// (pkg/staterepo) beside everything else grain knows.
//
// It used to be a SQLite database of its own, deliberately separate from
// the task store so that an operator could back up, ship or lock down
// their credentials without also handling every task grain had ever
// filed. The separation stays, and is now stronger rather than weaker:
// the secrets are the one thing in the state repository that is
// ciphertext, encrypted to a public key whose private half grain reads
// from one file the operator manages and never copies anywhere else --
// not into the repository, and not into any backup of it. Cloning the
// repository gets you everything grain knows and none of what it can
// authenticate as.
//
// docs/data-model.md's "no secret store in the model" governs the task
// model (model): a Task, a Grant, a Lease never carry material, only
// a CredentialRef naming one. This package is where the name actually
// resolves -- read fresh off disk on every call, matching
// gitproxy/credentials.go's "replace a file and restart the one
// service that reads it".
//
// Agents do not read this file and cannot. A run gets a secret only
// through the secret input a human asked for, exactly as before: nothing
// here is exposed to a sandbox, and the private key never leaves the
// host's data directory.
//
// Set, DeleteKey and DeleteSecret exist for a different caller
// (bwsalmon/agents#357): a UI or a CLI running on the same host as the
// server, which the file is local disk to, and which therefore needs no
// daemon RPC to edit it. List reports which secrets and keys currently
// exist, never their values, so a caller can render what is set without
// ever being able to read one back through this package.
package secrets

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The well-known names an agent framework's own credential is stored
// under -- one per framework, resolved by cmd/grain's daemon before every
// dispatch and pkg/ui's Settings pane writes (its "Agent frameworks"
// section) and reports the presence of. Named here, in the one package
// both sides already depend on, so neither has to repeat a string the
// other has to match exactly for a key pasted into the UI to be the key
// a run authenticates with.
//
// Each holds a single key, AgentCredentialKey, so a caller resolving one
// can name the bare secret ("gemini-api-key") and let Resolve's
// sole-key form find it.
const (
	// GeminiAPIKeySecret holds the Gemini API key agent/gemini's own
	// client is built with -- the daemon's operating key, distinct from
	// the short-lived keys the gemini-key capability mints per task.
	GeminiAPIKeySecret = "gemini-api-key"
	// ClaudeOAuthTokenSecret holds the Claude Code OAuth token
	// agent/claude passes its subprocess as CLAUDE_CODE_OAUTH_TOKEN.
	ClaudeOAuthTokenSecret = "claude-oauth-token"
	// OpenAIAPIKeySecret holds the OpenAI API key agent/codex passes its
	// subprocess as OPENAI_API_KEY. Named for the credential rather than
	// for the framework, the way the other two are: it is an OpenAI
	// account's key, and a second framework built on one would want this
	// same secret rather than a copy of it under another name.
	OpenAIAPIKeySecret = "openai-api-key"
	// HubRegistrationTokenSecret holds the per-entry registration
	// credential ("grc_...") a connected grain authenticates its registry
	// heartbeats with (design/design/hub.design.md). Stored under
	// AgentCredentialKey ("value") and never exported into grain/config.
	HubRegistrationTokenSecret = "registry-registration-token"
	// AgentCredentialKey is the one key each of the secrets above holds.
	// "value" rather than a per-secret name ("api-key", "token")
	// so nothing has to remember which is which: the secret's own name
	// already says what the value is.
	AgentCredentialKey = "value"
)

// grainHeldSecrets are the store secrets grain keeps its own key material
// in (design/design/secrets-key-protection.design.md §5, Phase 3): the
// sandbox-access and package-cache CAs (pkg/daemon/ca_store.go), the
// release-approval signing key (pkg/releaseapproval/keystore.go), the
// state and secrets repositories' tokens and the imported relay and
// registry-registration tokens (pkg/daemon/loose_credentials.go). Beside
// them are the credentials that were in the store before that: the agent
// frameworks' own keys (above), the default GCP minter key
// (gcpkey.DefaultMinterCredential), the cosign key and password
// (imagesigning.KeyCredential/PasswordCredential, one "image-signing"
// secret) and the GitHub App github-sandbox mints through
// (githubsandbox.DefaultAppIDCredential/PrivateKeyCredential, one
// "github-app" secret). pkg/daemon's tests check these against the
// constants they are spelled out from.
var grainHeldSecrets = []string{
	"sandbox-access-ca",
	"package-cache-ca",
	"release-approval-key",
	"state-repo-token",
	"secrets-repo-token",
	"relay",
	HubRegistrationTokenSecret,
	GeminiAPIKeySecret,
	ClaudeOAuthTokenSecret,
	OpenAIAPIKeySecret,
	"gcp-key-minter",
	"image-signing",
	"github-app",
}

// grainHeldSecretPrefixes name the families of grain-held secrets with a
// name per instance: the git credentials (gitproxy.CredentialSecretPrefix),
// custom agent backends' credentials (AgentBackendCredentialSecret), the
// minter keys named GCP keys fall back to (model.GCPKeyMinterCredential)
// and repository-scoped release secrets (ci.ReleaseSecretAddress). A GCP
// key may also name a minter secret of any other name; pkg/ui's
// SetPendingSecret refuses those against the deployment's configured keys.
var grainHeldSecretPrefixes = []string{
	"github-credential-",
	"agent-backend-",
	"gcp-key-minter-",
	"repo-release-",
}

// GrainHeld reports whether secret -- a secret's name, or a
// "<secret>/<key>" reference -- is one grain keeps its own credentials or
// signing material in. Each has its own place to be set (Settings ->
// GitHub, Agent frameworks, GCP keys, CI, the State pane, the hub pane)
// or is minted by grain itself, so an agent's request_secret may not
// name one: a human pasting what such a request suggests would replace
// the deployment's CA, signing key, model credential or git credential
// with a value the agent chose.
func GrainHeld(secret string) bool {
	secret, _, _ = strings.Cut(secret, "/")
	for _, p := range grainHeldSecretPrefixes {
		if strings.HasPrefix(secret, p) {
			return true
		}
	}
	for _, s := range grainHeldSecrets {
		if secret == s {
			return true
		}
	}
	return false
}

// ErrNoSecret is what Resolve reports when the store opened and
// decrypted perfectly well and simply holds nothing under the name
// asked for. It is the difference a caller for whom a credential is
// optional needs: "nobody has set one" is a deployment running exactly
// as configured (cmd/grain's custom agent backends, which may
// authenticate some other way or not at all), while a key file that is
// missing, a file that will not decrypt or a name that is not a name at
// all are all still errors that caller must report rather than swallow.
// Test for it with errors.Is; a secret that exists but has no key by
// the name given stays a plain error, because that names something
// specific which should have been there.
var ErrNoSecret = errors.New("secrets: no secret")

// ErrGoneUpstream reports that a key the Secret Manager backend's
// catalogue still lists has no readable value in Secret Manager: the secret
// was deleted out of band (HTTP 404), or every version of it was destroyed or
// disabled. It is deliberately not ErrNoSecret -- the catalogue says the key
// should be there, and an operator has to reconcile the two with
// `grain secrets delete` (or set the key again) -- and KnownValues skips such
// entries rather than failing redaction as a whole (see MissingUpstream).
var ErrGoneUpstream = errors.New("secrets: catalogued key is gone from Secret Manager")

type taskContextKey struct{}
type runContextKey struct{}
type principalContextKey struct{}

// WithTask returns a context tagged with the task ID.
func WithTask(ctx context.Context, taskID string) context.Context {
	if taskID == "" {
		return ctx
	}
	return context.WithValue(ctx, taskContextKey{}, taskID)
}

// TaskFromContext returns the task ID stored in ctx, or "" if none.
func TaskFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(taskContextKey{}).(string); ok {
		return v
	}
	return ""
}

// WithRun returns a context tagged with the run ID.
func WithRun(ctx context.Context, runID string) context.Context {
	if runID == "" {
		return ctx
	}
	return context.WithValue(ctx, runContextKey{}, runID)
}

// RunFromContext returns the run ID stored in ctx, or "" if none.
func RunFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(runContextKey{}).(string); ok {
		return v
	}
	return ""
}

// WithPrincipal returns a context tagged with the principal identifier.
func WithPrincipal(ctx context.Context, principal string) context.Context {
	if principal == "" {
		return ctx
	}
	return context.WithValue(ctx, principalContextKey{}, principal)
}

// PrincipalFromContext returns the principal stored in ctx, or "" if none.
func PrincipalFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(principalContextKey{}).(string); ok {
		return v
	}
	return ""
}

// AuditEntry records a secret resolution event.
type AuditEntry struct {
	Timestamp  time.Time
	TaskID     string
	RunID      string
	SecretName string
	Key        string
	Principal  string
}

// AuditLogger records secret resolution events.
type AuditLogger interface {
	RecordSecretResolution(ctx context.Context, entry AuditEntry) error
}

// AgentBackendCredentialSecret names the write-only credential for one
// configured backend. Keeping the ID in the secret name lets two backends of
// the same framework authenticate independently.
func AgentBackendCredentialSecret(id string) string { return "agent-backend-" + id }

// DefaultFileName is what the encrypted file is called inside the state
// repository, and DefaultKeyFileName what the private key is called in
// the data directory. Spelled out here so cmd/grain and pkg/staterepo's
// .gitignore agree with this package without repeating a literal.
const (
	DefaultFileName    = "secrets.enc"
	DefaultKeyFileName = "secrets.key"
)

// Config says where the two files are.
type Config struct {
	// File is the encrypted secrets file. It belongs in the state
	// repository's working tree, where grain's sync commits and pushes it
	// along with everything else.
	File string
	// KeyFile is the private key. It belongs outside the repository --
	// under the data directory -- and is the operator's to manage, back
	// up and, if they choose, keep only on a machine that is not this
	// one (in which case grain cannot start; there is no half state where
	// it runs without being able to read its own secrets).
	KeyFile string
	// KeySource is where the key is loaded from. Nil means
	// FileKeySource{Path: KeyFile}, which is every install so far.
	// KeyFile is still where Open mints a fresh install's key and where
	// ImportKey writes one.
	KeySource KeySource
}

// keySource is cfg's KeySource, defaulted to the key file.
func (cfg Config) keySource() KeySource {
	if cfg.KeySource != nil {
		return cfg.KeySource
	}
	return FileKeySource{Path: cfg.KeyFile}
}

// KeySource is where this store loads its key from.
func (s *Store) KeySource() KeySource { return s.cfg.keySource() }

// Store resolves a name to secret material held in an encrypted file or in
// Google Cloud Secret Manager. The zero value is not useful; build one with
// Open, New, or OpenSecretManager.
type Store struct {
	backend string
	cfg     Config
	sm      *smBackend
	openErr error // set instead of a usable store when Open could not prepare one
	// created records that Open minted the key itself, which the daemon
	// reports once at startup: a key nobody has backed up yet is the one
	// thing about a fresh install that is urgent.
	created     bool
	auditLogger AuditLogger
	// mu serialises the read-modify-write a Set or a Delete is. The
	// daemon is the only writer (cmd/grain's own note on the UI and the
	// CLI reaching it over REST), but its own handlers are concurrent.
	mu sync.Mutex
	// ensureMu makes Ensure's check-then-set one step, so two attaches
	// of the same server never generate two values.
	ensureMu sync.Mutex
	// keys is the key as cfg's KeySource last gave it, and the state of
	// loading it. ShareKey hands the same keys to another Store.
	keys *keyCache
	// now is time.Now, replaceable in tests.
	now func() time.Time
}

// keyCache is a Store's private key, loaded once and kept for the life
// of the Store rather than re-read on every load and save: a source may
// be a KMS unwrap, and even a file is one more open of the key per
// Resolve. ImportKey drops it, and so does a load that finds the file
// sealed to some other key (another process -- `grain state key import`
// with the daemon running -- may have installed one). Stores made by
// ShareKey hold one keyCache between them, so a drop by either is a
// drop for both.
type keyCache struct {
	mu  sync.Mutex
	key *Key
	// err is the last KMS failure loading the key, and retryAt when
	// the next attempt may be made: until then every use of the store
	// answers err rather than calling KMS again, and each failure
	// doubles backoff up to maxKeyBackoff. Only KMS failures (ErrKMS)
	// are held like this; they are the ones that put themselves right.
	err     error
	retryAt time.Time
	backoff time.Duration
	// lastUnwrap is when KMS last handed this key over.
	lastUnwrap time.Time
}

const (
	minKeyBackoff = 5 * time.Second
	maxKeyBackoff = 5 * time.Minute
)

func (s *Store) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// currentKey is the cached key, loading it on first use.
func (s *Store) currentKey() (Key, error) {
	k := s.keys
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.key != nil {
		return *k.key, nil
	}
	if k.err != nil && s.clock().Before(k.retryAt) {
		return Key{}, k.err
	}
	key, err := s.cfg.keySource().Load(context.Background())
	if err != nil {
		if errors.Is(err, ErrKMS) {
			k.backoff = min(max(2*k.backoff, minKeyBackoff), maxKeyBackoff)
			k.err, k.retryAt = err, s.clock().Add(k.backoff)
		}
		return Key{}, err
	}
	if k.err != nil {
		log.Printf("grain: the secrets key unwrapped after earlier failures; the store is readable again")
	}
	k.err, k.backoff = nil, 0
	if info, ierr := ReadKeyFileInfo(s.cfg.KeyFile); ierr == nil && info.Protection == ProtectionKMS {
		k.lastUnwrap = s.clock()
	}
	k.key = &key
	return key, nil
}

// KeyRetryIn is how long until the store will next try to load a key
// KMS failed to unwrap, or 0 when it is not waiting on one.
func (s *Store) KeyRetryIn() time.Duration {
	k := s.keys
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.key != nil || k.err == nil {
		return 0
	}
	return max(k.retryAt.Sub(s.clock()), 0)
}

// WaitingOnKMS reports whether the store's key is not loaded because
// KMS last failed to unwrap it.
func (s *Store) WaitingOnKMS() bool {
	k := s.keys
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.key == nil && k.err != nil
}

// KeyProtection is how this store's key is protected at rest, for
// `grain state status` and Settings -> State.
type KeyProtection struct {
	// Mode is ProtectionFile (a plaintext key file) or ProtectionKMS.
	Mode string `json:"mode"`
	// KMSKey is the Cloud KMS key that wraps it, in KMS mode.
	KMSKey string `json:"kmsKey,omitempty"`
	// LastUnwrap is when this process last unwrapped the key, zero if
	// it has not.
	LastUnwrap time.Time `json:"lastUnwrap,omitempty"`
	// Error is why the key could not be unwrapped, while that lasts.
	Error string `json:"error,omitempty"`
}

// KeyProtection reports how the key is held. It reads the key file's
// header and never calls KMS.
func (s *Store) KeyProtection() KeyProtection {
	out := KeyProtection{Mode: ProtectionFile}
	if s.backend != BackendFile || s.cfg.KeyFile == "" {
		return out
	}
	if info, err := ReadKeyFileInfo(s.cfg.KeyFile); err == nil {
		out.Mode, out.KMSKey = info.Protection, info.KMSKey
	}
	k := s.keys
	k.mu.Lock()
	defer k.mu.Unlock()
	out.LastUnwrap = k.lastUnwrap
	if k.key == nil && k.err != nil {
		out.Error = k.err.Error()
	}
	return out
}

// forgetKey drops the cached key, so the next use loads it again.
func (s *Store) forgetKey() {
	k := s.keys
	k.mu.Lock()
	k.key = nil
	k.mu.Unlock()
}

// Backend reports which storage backend this Store uses (BackendFile or
// BackendSecretManager).
func (s *Store) Backend() string {
	if s.backend == BackendSecretManager {
		return BackendSecretManager
	}
	return BackendFile
}

// SetAuditLogger sets the audit logger for secret resolution events.
func (s *Store) SetAuditLogger(l AuditLogger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auditLogger = l
}

// Open prepares the store, minting a key if this is a fresh install.
//
// "Fresh" means precisely: no key file and no secrets file. A key file
// that is missing while a secrets file exists is never quietly replaced
// -- that is an operator who has lost their key, the secrets are
// unrecoverable, and saying so beats starting over with a new key and
// leaving an undecryptable file in the repository for grain to trip over
// later.
//
// Minting without being asked is what lets a local-only install need no
// input at all: `grain daemon` on a laptop comes up with working secret
// storage and a key on disk, and the operator can go and put that key
// somewhere safe whenever they like.
func Open(cfg Config) *Store {
	s := &Store{backend: BackendFile, cfg: cfg, keys: &keyCache{}}
	if cfg.File == "" || cfg.KeyFile == "" {
		s.openErr = errors.New("secrets: both a file and a key file are required")
		return s
	}
	_, err := s.currentKey()
	switch {
	case err == nil:
	case errors.Is(err, ErrNoKey):
		if fileExists(cfg.File) {
			s.openErr = fmt.Errorf(
				"secrets: %s holds encrypted secrets but %s is missing -- "+
					"restore that key file, or delete the secrets file to start over",
				cfg.File, cfg.KeyFile)
			return s
		}
		key, genErr := GenerateKey()
		if genErr != nil {
			s.openErr = genErr
			return s
		}
		if writeErr := WriteKeyFile(cfg.KeyFile, key); writeErr != nil {
			s.openErr = writeErr
			return s
		}
		s.keys.key = &key
		s.created = true
	case errors.Is(err, ErrKMS):
		// KMS is down, or has been told not to answer this host. The
		// store stays open and every use says so; currentKey tries
		// again with backoff, so the store unlocks when KMS answers
		// without anything restarting.
	default:
		s.openErr = err
	}
	return s
}

// New opens a store whose two files sit in one directory, which is what
// a test wants and what a single-directory install gets. A deployment
// uses Open instead, because the point of this package's layout is that
// the two files live in different places: the encrypted one inside the
// state repository, the key outside it.
func New(dir string) *Store {
	return Open(Config{
		File:    filepath.Join(dir, DefaultFileName),
		KeyFile: filepath.Join(dir, DefaultKeyFileName),
	})
}

// KeyCreated reports whether Open minted the key rather than finding it.
func (s *Store) KeyCreated() bool { return s.created }

// KeyFile is where the private key is read from, for a caller that has
// to tell an operator which file to go and back up.
func (s *Store) KeyFile() string { return s.cfg.KeyFile }

// File is the encrypted file this store reads and writes.
func (s *Store) File() string { return s.cfg.File }

// PublicKey renders the public half of the key, which is safe to display
// and is what the bootstrap shows an operator so they can confirm the
// key they hold is the key this deployment encrypts to.
func (s *Store) PublicKey() (string, error) {
	if err := s.ready(); err != nil {
		return "", err
	}
	if s.backend == BackendSecretManager {
		return "", nil
	}
	key, err := s.currentKey()
	if err != nil {
		return "", err
	}
	return key.Public(), nil
}

// ImportKey installs a private key the operator already holds, which is
// the other half of restoring an installation: a backup carries the
// encrypted file, and this carries the key, which travels by hand and is
// deliberately in no backup of anything else. Without it, that file on a
// new host is every secret grain has in a form nothing on that host can
// open -- which is the correct security property and a dead end as a
// restore path.
//
// It refuses a key that does not match the file it would have to read,
// naming both public keys, rather than installing it and leaving the
// mismatch to surface later as a run that could not resolve a
// credential. A key it replaces is moved aside with a timestamped name
// and never deleted: key material is the one thing here that cannot be
// regenerated, and an operator who pastes yesterday's key over today's
// must be able to get today's back.
func (s *Store) ImportKey(k Key) error {
	if s.backend == BackendSecretManager {
		return errors.New("secrets: key import applies only to the encrypted-file backend, not secretmanager")
	}
	if k.priv == nil {
		return errors.New("secrets: no key to import")
	}
	if s.cfg.KeyFile == "" {
		return errors.New("secrets: no key file configured to import into")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.cfg.File)
	switch {
	case err == nil && len(strings.TrimSpace(string(data))) > 0:
		if _, err := Decrypt(k, data); err != nil {
			if errors.Is(err, ErrWrongKey) {
				want, _ := Recipient(data)
				return fmt.Errorf("%w: %s is encrypted to %s, and this key is %s",
					ErrWrongKey, s.cfg.File, want, k.Public())
			}
			return err
		}
	case err != nil && !os.IsNotExist(err):
		return fmt.Errorf("secrets: reading %s: %w", s.cfg.File, err)
	}
	if existing, err := os.ReadFile(s.cfg.KeyFile); err == nil && strings.TrimSpace(string(existing)) != k.String() {
		archived := s.cfg.KeyFile + ".replaced-" + time.Now().UTC().Format("20060102-150405")
		if err := os.Rename(s.cfg.KeyFile, archived); err != nil {
			return fmt.Errorf("secrets: keeping the key %s replaces: %w", s.cfg.KeyFile, err)
		}
	}
	if err := WriteKeyFile(s.cfg.KeyFile, k); err != nil {
		s.forgetKey()
		return err
	}
	s.forgetKey()
	// Whatever Open could not do -- and the one thing it refuses to do is
	// mint a key over an encrypted file it cannot read -- it can do now.
	s.openErr, s.created = nil, false
	return nil
}

// Check reports whether this host can actually read its own secrets
// file: the key is there, and it is the key the file was sealed to. A
// store with nothing stored yet passes, because an absent file is an
// empty store rather than a broken one.
//
// The bootstrap asks this, so that "the secrets on this host are
// encrypted to a key it does not have" is something an operator is told
// where they can fix it, rather than something they discover when a run
// fails to resolve a credential.
func (s *Store) Check() error {
	if s.backend == BackendSecretManager {
		if err := s.ready(); err != nil {
			return err
		}
		return s.sm.check(context.Background())
	}
	_, err := s.load()
	return err
}

// FileRecipient reports the public key the secrets file is encrypted to,
// or "" when there is no file yet. Shown beside PublicKey in the
// bootstrap: seeing the two differ is what tells an operator holding
// several keys which one this deployment needs.
func (s *Store) FileRecipient() (string, error) {
	if s.cfg.File == "" {
		return "", nil
	}
	data, err := os.ReadFile(s.cfg.File)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("secrets: reading %s: %w", s.cfg.File, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return "", nil
	}
	return Recipient(data)
}

func (s *Store) ready() error {
	if s.openErr != nil {
		return s.openErr
	}
	return nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// contents is the plaintext under the ciphertext: every secret, every
// key, every value. Values are base64 because a value is arbitrary bytes
// -- a PEM, a binary token -- and JSON strings are not.
//
// A file written before the single-tenant reset may also carry "teams",
// a per-secret team restriction. Nothing reads it any more: decoding
// skips it, so every secret resolves regardless, and the next save
// writes the file without it.
type contents struct {
	Version int                          `json:"version"`
	Secrets map[string]map[string]string `json:"secrets"`
}

// load decrypts the file. A file that is not there is an empty store,
// not an error: "missing is empty, not broken" is the same answer
// Resolve gives for one name at a time, and it is what makes a fresh
// install work before anything has been set.
func (s *Store) load() (*contents, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.cfg.File)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("secrets: reading %s: %w", s.cfg.File, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		// No ciphertext to check the cached key against, so nothing
		// would notice if another process (`grain state key import`
		// on an empty store, with the daemon running) had installed a
		// different key since: the next save would seal the file to
		// the replaced one, which the key file no longer is, and the
		// store would not open after a restart. Drop the cache so that
		// save loads the key afresh. A store is empty only until its
		// first write, so this costs a source nothing in steady state.
		s.forgetKey()
		return &contents{Version: 1, Secrets: map[string]map[string]string{}}, nil
	}
	key, err := s.currentKey()
	if err != nil {
		return nil, err
	}
	plain, err := Decrypt(key, data)
	if errors.Is(err, ErrWrongKey) {
		// The key this Store cached is not the one the file is sealed
		// to. Something else may have installed the right one since;
		// look once more before calling the store unreadable.
		s.forgetKey()
		if key, err = s.currentKey(); err != nil {
			return nil, err
		}
		plain, err = Decrypt(key, data)
	}
	if err != nil {
		return nil, err
	}
	var c contents
	if err := json.Unmarshal(plain, &c); err != nil {
		return nil, fmt.Errorf("secrets: %s decrypted to something that is not a secrets file: %w", s.cfg.File, err)
	}
	if c.Secrets == nil {
		c.Secrets = map[string]map[string]string{}
	}
	return &c, nil
}

// save encrypts and writes the file atomically, 0600. The write goes
// through a temporary file in the same directory so a reader -- or a git
// commit racing the daemon's own sync -- never sees a half-written
// ciphertext, which would decrypt to nothing at all.
func (s *Store) save(c *contents) error {
	key, err := s.currentKey()
	if err != nil {
		return err
	}
	c.Version = 1
	plain, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	sealed, err := Encrypt(key, plain)
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.cfg.File)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("secrets: preparing %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".secrets-*")
	if err != nil {
		return fmt.Errorf("secrets: creating a temporary file next to %s: %w", s.cfg.File, err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(sealed); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.cfg.File)
}

// Resolve reads one secret by name. "<secret>/<key>" names one key in
// one secret directly. "<secret>" alone resolves only if that secret has
// exactly one key, so a capability naming a single-value secret (a
// token, a minted key) does not also have to know and repeat its key's
// name.
//
// The returned value is the key's exact bytes, with no trimming: a
// value that legitimately ends in whitespace round-trips unchanged.
//
// A name nothing is stored under reports ErrNoSecret, which a caller
// that can do without the value tells apart from a store it could not
// read at all.
func (s *Store) Resolve(ctx context.Context, name string) (string, error) {
	secret, key, explicit := strings.Cut(name, "/")
	if !validComponent(secret) {
		return "", fmt.Errorf("secrets: %q is not a valid secret name", secret)
	}
	if explicit && !validComponent(key) {
		return "", fmt.Errorf("secrets: %q names no valid key after %q/", name, secret)
	}
	var resolvedVal string
	if s.backend == BackendSecretManager {
		if err := s.ready(); err != nil {
			return "", err
		}
		var err error
		key, resolvedVal, err = s.sm.resolve(ctx, secret, key, explicit)
		if err != nil {
			return "", err
		}
	} else {
		c, err := s.load()
		if err != nil {
			return "", err
		}
		keys, ok := c.Secrets[secret]
		if !ok || len(keys) == 0 {
			return "", fmt.Errorf("%w named %q", ErrNoSecret, secret)
		}
		if !explicit {
			if len(keys) != 1 {
				names := make([]string, 0, len(keys))
				for k := range keys {
					names = append(names, k)
				}
				sort.Strings(names)
				return "", fmt.Errorf(
					"secrets: secret %q has %d keys (%s), name one explicitly as %q",
					secret, len(keys), strings.Join(names, ", "), secret+"/<key>")
			}
			for k := range keys {
				key = k
			}
		}
		encoded, ok := keys[key]
		if !ok {
			return "", fmt.Errorf("secrets: no key %q in secret %q", key, secret)
		}
		value, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return "", fmt.Errorf("secrets: %q in secret %q is corrupt: %w", key, secret, err)
		}
		resolvedVal = string(value)
	}
	s.mu.Lock()
	logger := s.auditLogger
	s.mu.Unlock()
	if logger != nil {
		_ = logger.RecordSecretResolution(ctx, AuditEntry{
			Timestamp:  time.Now().UTC(),
			TaskID:     TaskFromContext(ctx),
			RunID:      RunFromContext(ctx),
			SecretName: secret,
			Key:        key,
			Principal:  PrincipalFromContext(ctx),
		})
	}
	return resolvedVal, nil
}

// SecretValue holds one stored key's resolved value, returned by KnownValues
// for journal and namespace output redaction without emitting a secret
// resolution audit event.
type SecretValue struct {
	SecretName string
	Key        string
	Value      string
}

// KnownValues reads every stored secret key and value for redaction, without
// recording an AuditEntry on the configured AuditLogger.
//
// On the Secret Manager backend a catalogued key whose value is gone upstream
// (ErrGoneUpstream) is logged, not returned as an error: one stale catalogue
// entry would otherwise refuse every transcript upload, and its database
// fallback, until someone noticed. A value that no longer exists cannot be
// handed to a new run, but one handed out before it went may still be in a
// running sandbox's output, so the value this process last read for such a
// key (while the read cache is on) is still returned; one it never read is
// skipped. Any other failure, including a 404 that is not a Secret Manager
// error, still fails the whole call, closed.
func (s *Store) KnownValues(ctx context.Context) ([]SecretValue, error) {
	if s.backend == BackendSecretManager {
		if err := s.ready(); err != nil {
			return nil, err
		}
		return s.sm.knownValues(ctx)
	}
	c, err := s.load()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(c.Secrets))
	for name := range c.Secrets {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []SecretValue
	for _, name := range names {
		keysMap := c.Secrets[name]
		keys := make([]string, 0, len(keysMap))
		for k := range keysMap {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			val, err := base64.StdEncoding.DecodeString(keysMap[k])
			if err != nil {
				return nil, fmt.Errorf("secrets: %q in secret %q is corrupt: %w", k, name, err)
			}
			out = append(out, SecretValue{SecretName: name, Key: k, Value: string(val)})
		}
	}
	return out, nil
}

// SecretKeyRef names one key of one secret.
type SecretKeyRef struct {
	SecretName string `json:"secret"`
	Key        string `json:"key"`
}

// MissingUpstream reports the catalogued keys whose values are gone from
// Secret Manager (ErrGoneUpstream), probing each one there rather than
// trusting the read cache. Values read along the way are discarded and no
// AuditEntry is recorded. The file backend has no upstream to drift from and
// always reports none.
func (s *Store) MissingUpstream(ctx context.Context) ([]SecretKeyRef, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if s.backend != BackendSecretManager {
		return nil, nil
	}
	return s.sm.missingUpstream(ctx)
}

// SecretInfo names one secret and the keys it currently holds.
type SecretInfo struct {
	Name string   `json:"name"`
	Keys []string `json:"keys"`
}

// List reports every secret currently stored, the keys each one
// holds, sorted by name for a stable, diffable rendering.
func (s *Store) List() ([]SecretInfo, error) {
	if s.backend == BackendSecretManager {
		if err := s.ready(); err != nil {
			return nil, err
		}
		return s.sm.list(context.Background())
	}
	c, err := s.load()
	if err != nil {
		return nil, err
	}
	var out []SecretInfo
	for name, keys := range c.Secrets {
		info := SecretInfo{Name: name}
		for k := range keys {
			info.Keys = append(info.Keys, k)
		}
		sort.Strings(info.Keys)
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Set writes one key's value. An existing key is overwritten outright.
func (s *Store) Set(secret, key string, value []byte) error {
	if !validComponent(secret) {
		return fmt.Errorf("secrets: %q is not a valid secret name", secret)
	}
	if !validComponent(key) {
		return fmt.Errorf("secrets: %q is not a valid key name", key)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backend == BackendSecretManager {
		if err := s.ready(); err != nil {
			return err
		}
		return s.sm.set(context.Background(), secret, key, value)
	}
	c, err := s.load()
	if err != nil {
		return err
	}
	if c.Secrets[secret] == nil {
		c.Secrets[secret] = map[string]string{}
	}
	c.Secrets[secret][key] = base64.StdEncoding.EncodeToString(value)
	return s.save(c)
}

// Has reports whether secret holds key, without reading its value.
func (s *Store) Has(secret, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backend == BackendSecretManager {
		if err := s.ready(); err != nil {
			return false, err
		}
		return s.sm.has(context.Background(), secret, key)
	}
	c, err := s.load()
	if err != nil {
		return false, err
	}
	_, ok := c.Secrets[secret][key]
	return ok, nil
}

// DeleteKey removes one key from a secret.
func (s *Store) DeleteKey(secret, key string) error {
	if !validComponent(secret) {
		return fmt.Errorf("secrets: %q is not a valid secret name", secret)
	}
	if !validComponent(key) {
		return fmt.Errorf("secrets: %q is not a valid key name", key)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backend == BackendSecretManager {
		if err := s.ready(); err != nil {
			return err
		}
		return s.sm.deleteKey(context.Background(), secret, key)
	}
	c, err := s.load()
	if err != nil {
		return err
	}
	if _, ok := c.Secrets[secret][key]; !ok {
		return fmt.Errorf("secrets: no key %q in secret %q", key, secret)
	}
	delete(c.Secrets[secret], key)
	if len(c.Secrets[secret]) == 0 {
		delete(c.Secrets, secret)
	}
	return s.save(c)
}

// DeleteSecret removes a secret and every key in it.
func (s *Store) DeleteSecret(secret string) error {
	if !validComponent(secret) {
		return fmt.Errorf("secrets: %q is not a valid secret name", secret)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backend == BackendSecretManager {
		if err := s.ready(); err != nil {
			return err
		}
		return s.sm.deleteSecret(context.Background(), secret)
	}
	c, err := s.load()
	if err != nil {
		return err
	}
	if len(c.Secrets[secret]) == 0 {
		return fmt.Errorf("%w named %q", ErrNoSecret, secret)
	}
	delete(c.Secrets, secret)
	return s.save(c)
}

// validComponent reports whether s is safe to use as a secret or key
// name: non-empty, not "." or "..", and free of path separators. Nothing
// here puts a name on disk directly the way the old file-tree store did,
// but keeping the same restriction means a name that was invalid before
// this package moved off a file tree stays invalid now, rather than
// quietly starting to work.
func validComponent(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\")
}

// Ensure makes name (<secret>/<key>) hold a value, storing generate's
// when it holds none, and never returns it: what a value grain makes up
// itself and only grain reads -- a capability server's data-plane token
// (protocol.WithManagedSecrets) -- is kept by. A value already there,
// from an earlier start, is kept.
func (s *Store) Ensure(name string, generate func() (string, error)) error {
	secret, key, ok := strings.Cut(name, "/")
	if !ok || !validComponent(secret) || !validComponent(key) {
		return fmt.Errorf("secrets: %q is not a <secret>/<key> name", name)
	}
	s.ensureMu.Lock()
	defer s.ensureMu.Unlock()
	if has, err := s.Has(secret, key); err != nil || has {
		return err
	}
	v, err := generate()
	if err != nil {
		return err
	}
	return s.Set(secret, key, []byte(v))
}

// Rotate replaces whatever name (<secret>/<key>) holds with a fresh
// value from generate.
func (s *Store) Rotate(name string, generate func() (string, error)) error {
	secret, key, ok := strings.Cut(name, "/")
	if !ok || !validComponent(secret) || !validComponent(key) {
		return fmt.Errorf("secrets: %q is not a <secret>/<key> name", name)
	}
	s.ensureMu.Lock()
	defer s.ensureMu.Unlock()
	v, err := generate()
	if err != nil {
		return err
	}
	return s.Set(secret, key, []byte(v))
}
