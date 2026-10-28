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
	// AgentCredentialKey is the one key each of the secrets above holds.
	// "value" rather than a per-secret name ("api-key", "token")
	// so nothing has to remember which is which: the secret's own name
	// already says what the value is.
	AgentCredentialKey = "value"
)

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
}

// Store resolves a name to secret material held in an encrypted file.
// The zero value is not useful; build one with Open or New.
type Store struct {
	cfg     Config
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
	s := &Store{cfg: cfg}
	if cfg.File == "" || cfg.KeyFile == "" {
		s.openErr = errors.New("secrets: both a file and a key file are required")
		return s
	}
	_, err := ReadKeyFile(cfg.KeyFile)
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
		s.created = true
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
	key, err := ReadKeyFile(s.cfg.KeyFile)
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
	if existing, err := ReadKeyFile(s.cfg.KeyFile); err == nil && existing.String() != k.String() {
		archived := s.cfg.KeyFile + ".replaced-" + time.Now().UTC().Format("20060102-150405")
		if err := os.Rename(s.cfg.KeyFile, archived); err != nil {
			return fmt.Errorf("secrets: keeping the key %s replaces: %w", s.cfg.KeyFile, err)
		}
	}
	if err := WriteKeyFile(s.cfg.KeyFile, k); err != nil {
		return err
	}
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
	if os.IsNotExist(err) {
		return &contents{Version: 1, Secrets: map[string]map[string]string{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("secrets: reading %s: %w", s.cfg.File, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return &contents{Version: 1, Secrets: map[string]map[string]string{}}, nil
	}
	key, err := ReadKeyFile(s.cfg.KeyFile)
	if err != nil {
		return nil, err
	}
	plain, err := Decrypt(key, data)
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
	key, err := ReadKeyFile(s.cfg.KeyFile)
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
	resolvedVal := string(value)
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

// SecretInfo names one secret and the keys it currently holds.
type SecretInfo struct {
	Name string   `json:"name"`
	Keys []string `json:"keys"`
}

// List reports every secret currently stored, the keys each one
// holds, sorted by name for a stable, diffable rendering.
func (s *Store) List() ([]SecretInfo, error) {
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
