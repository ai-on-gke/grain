package gitproxy

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/secrets"
)

func writeTokensFile(t *testing.T, dir string, tokens map[string]string) string {
	t.Helper()
	path := filepath.Join(dir, "sandbox-tokens.json")
	data, err := json.Marshal(tokens)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAuthenticateKnownToken(t *testing.T) {
	path := writeTokensFile(t, t.TempDir(), map[string]string{"sandbox-0": "tok0", "sandbox-1": "tok1"})
	tokens, err := LoadSandboxTokens(path)
	if err != nil {
		t.Fatal(err)
	}
	if name, ok := tokens.Authenticate("tok0"); !ok || name != "sandbox-0" {
		t.Errorf("Authenticate(tok0) = %q, %v", name, ok)
	}
	if name, ok := tokens.Authenticate("tok1"); !ok || name != "sandbox-1" {
		t.Errorf("Authenticate(tok1) = %q, %v", name, ok)
	}
}

func TestAuthenticateUnknownToken(t *testing.T) {
	path := writeTokensFile(t, t.TempDir(), map[string]string{"sandbox-0": "tok0"})
	tokens, err := LoadSandboxTokens(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tokens.Authenticate("not-a-real-token"); ok {
		t.Error("unknown token should not authenticate")
	}
}

func TestMissingFileAuthenticatesNothing(t *testing.T) {
	tokens, err := LoadSandboxTokens(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tokens.Authenticate("anything"); ok {
		t.Error("a missing file should authenticate nothing")
	}
}

func basicAuth(username, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
}

func TestExtractBasicAuthTokenIgnoresUsername(t *testing.T) {
	if tok, ok := ExtractBasicAuthToken(basicAuth("whatever", "the-token")); !ok || tok != "the-token" {
		t.Errorf("got %q, %v", tok, ok)
	}
	if tok, ok := ExtractBasicAuthToken(basicAuth("", "the-token")); !ok || tok != "the-token" {
		t.Errorf("got %q, %v", tok, ok)
	}
}

func TestExtractBasicAuthTokenRejectsMalformedHeader(t *testing.T) {
	cases := []string{
		"",
		"Bearer sometoken",
		"Basic not-valid-base64!!!",
		"Basic " + base64.StdEncoding.EncodeToString([]byte("no-colon-here")),
	}
	for _, header := range cases {
		if _, ok := ExtractBasicAuthToken(header); ok {
			t.Errorf("ExtractBasicAuthToken(%q) should fail", header)
		}
	}
}

// --- SandboxTokenStore -------------------------------------------------

func TestEnsureTokenMintsANewTokenForAnUnknownSandbox(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sandbox-tokens.json")
	store := NewSandboxTokenStore(path)
	token, err := store.EnsureToken("sandbox-0")
	if err != nil {
		t.Fatal(err)
	}
	if token == "" {
		t.Fatal("expected a non-empty token")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// File must be encrypted at rest with Grain secrets armor
	if !strings.HasPrefix(strings.TrimSpace(string(data)), secrets.ArmourBegin) {
		t.Fatalf("expected encrypted file starting with %s, got: %s", secrets.ArmourBegin, string(data))
	}
	// Direct unmarshal of ciphertext should fail
	var directMap map[string]string
	if err := json.Unmarshal(data, &directMap); err == nil {
		t.Fatal("expected unmarshal of encrypted data to fail")
	}
	// Loading via LoadSandboxTokens or readTokenFile should succeed and yield the token
	tokens, err := LoadSandboxTokens(path)
	if err != nil {
		t.Fatalf("LoadSandboxTokens failed: %v", err)
	}
	if sandbox, ok := tokens.Authenticate(token); !ok || sandbox != "sandbox-0" {
		t.Errorf("Authenticate = %q, %v, want sandbox-0, true", sandbox, ok)
	}
}

func TestEnsureTokenIsIdempotent(t *testing.T) {
	store := NewSandboxTokenStore(filepath.Join(t.TempDir(), "sandbox-tokens.json"))
	first, err := store.EnsureToken("sandbox-0")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.EnsureToken("sandbox-0")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("first = %q, second = %q, want equal", first, second)
	}
}

func TestEnsureTokenMintsDistinctTokensPerSandbox(t *testing.T) {
	store := NewSandboxTokenStore(filepath.Join(t.TempDir(), "sandbox-tokens.json"))
	t0, err := store.EnsureToken("sandbox-0")
	if err != nil {
		t.Fatal(err)
	}
	t1, err := store.EnsureToken("sandbox-1")
	if err != nil {
		t.Fatal(err)
	}
	if t0 == t1 {
		t.Error("expected distinct tokens per sandbox")
	}
}

func TestEnsureTokenPreservesOtherSandboxesAlreadyOnFile(t *testing.T) {
	dir := t.TempDir()
	path := writeTokensFile(t, dir, map[string]string{"sandbox-0": "existing-token"})
	store := NewSandboxTokenStore(path)
	if _, err := store.EnsureToken("sandbox-1"); err != nil {
		t.Fatal(err)
	}
	tokens, err := LoadSandboxTokens(path)
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := tokens.Authenticate("existing-token"); !ok || s != "sandbox-0" {
		t.Errorf("sandbox-0 authenticate = %q, %v, want sandbox-0, true", s, ok)
	}
	s1Token, err := store.EnsureToken("sandbox-1")
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := tokens.Authenticate(s1Token); !ok || s != "sandbox-1" {
		t.Errorf("sandbox-1 authenticate = %q, %v, want sandbox-1, true", s, ok)
	}
}

func TestATokenMintedByTheStoreAuthenticatesViaSandboxTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	token, err := NewSandboxTokenStore(path).EnsureToken("sandbox-0")
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := LoadSandboxTokens(path)
	if err != nil {
		t.Fatal(err)
	}
	if name, ok := tokens.Authenticate(token); !ok || name != "sandbox-0" {
		t.Errorf("got %q, %v", name, ok)
	}
}

func TestRotateReplacesAnExistingToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	store := NewSandboxTokenStore(path)
	original, err := store.EnsureToken("sandbox-0")
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := store.Rotate("sandbox-0")
	if err != nil {
		t.Fatal(err)
	}
	if rotated == original {
		t.Fatal("rotate should mint a different token")
	}
	tokens, err := LoadSandboxTokens(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tokens.Authenticate(original); ok {
		t.Error("the original token should no longer authenticate")
	}
	if name, ok := tokens.Authenticate(rotated); !ok || name != "sandbox-0" {
		t.Errorf("got %q, %v", name, ok)
	}
}

func TestSaveIsAtomicNoPartialFileLeftBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sandbox-tokens.json")
	if _, err := NewSandboxTokenStore(path).EnsureToken("sandbox-0"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected %s to exist: %v", path, err)
	}
	// Globbed rather than checked under one fixed name: save's temp file
	// carries a random suffix (its own doc comment), so a leftover would
	// not be sitting at path+".tmp" to be found.
	leftovers, err := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Errorf("expected no leftover temp file, found %v", leftovers)
	}
}

// TestEnsureTokenIsSafeForConcurrentCallers pins what stopped being true
// when a token stopped being minted per slot at startup and started being
// minted per run, from one goroutine per dispatch (orchestrator's own
// reconcileDispatch). Unguarded, the read-modify-write here loses tokens
// two ways at once: whichever save renames last drops every entry the
// others added, and concurrent writes through one fixed temp-file name
// publish half-written JSON, so a mint can fail outright with a parse
// error or an ENOENT rename. Every token this hands back has to be one
// the proxy can then authenticate -- a run holding a token that never
// reached the file fails every git operation it makes for as long as it
// lives.
func TestEnsureTokenIsSafeForConcurrentCallers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	store := NewSandboxTokenStore(path)

	const sandboxes = 16
	names := make([]string, sandboxes)
	minted := make([]string, sandboxes)
	errs := make([]error, sandboxes)
	var wg sync.WaitGroup
	for i := range names {
		names[i] = fmt.Sprintf("t%d-1", i)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			minted[i], errs[i] = store.EnsureToken(names[i])
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("EnsureToken(%s): %v", names[i], err)
		}
	}

	tokens, err := LoadSandboxTokens(path)
	if err != nil {
		t.Fatalf("LoadSandboxTokens after concurrent minting: %v", err)
	}
	for i, token := range minted {
		name, ok := tokens.Authenticate(token)
		if !ok {
			t.Errorf("the token minted for %s never reached the file -- that run's git would fail closed", names[i])
			continue
		}
		if name != names[i] {
			t.Errorf("token minted for %s authenticates as %q", names[i], name)
		}
	}
}

// A token minted while the proxy is already serving has to authenticate,
// which is the whole reason SandboxTokens re-reads on a miss -- and it has
// to keep doing so when several sandboxes mint at once, where a reload
// that read the file earlier must not be able to install its map over one
// that read it later.
func TestAuthenticateSeesTokensMintedConcurrentlyAfterLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	store := NewSandboxTokenStore(path)

	// Loaded against a file that does not exist yet, so every token below
	// is one minted after this proxy started serving.
	tokens, err := LoadSandboxTokens(path)
	if err != nil {
		t.Fatal(err)
	}

	const sandboxes = 16
	results := make([]bool, sandboxes)
	var wg sync.WaitGroup
	for i := 0; i < sandboxes; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("t%d-1", i)
			token, err := store.EnsureToken(name)
			if err != nil {
				t.Errorf("EnsureToken(%s): %v", name, err)
				return
			}
			got, ok := tokens.Authenticate(token)
			results[i] = ok && got == name
		}(i)
	}
	wg.Wait()

	for i, ok := range results {
		if !ok {
			t.Errorf("t%d-1's freshly minted token did not authenticate", i)
		}
	}
}

func TestRevokeDropsOneSandboxAndLeavesTheRest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	store := NewSandboxTokenStore(path)

	gone, err := store.EnsureToken("t1-1")
	if err != nil {
		t.Fatal(err)
	}
	kept, err := store.EnsureToken("t2-1")
	if err != nil {
		t.Fatal(err)
	}

	tokens, err := LoadSandboxTokens(path)
	if err != nil {
		t.Fatal(err)
	}
	if name, ok := tokens.Authenticate(gone); !ok || name != "t1-1" {
		t.Fatalf("Authenticate before Revoke = %q, %v; want t1-1, true", name, ok)
	}
	if name, ok := tokens.Authenticate(kept); !ok || name != "t2-1" {
		t.Fatalf("Authenticate before Revoke = %q, %v; want t2-1, true", name, ok)
	}

	if err := store.Revoke("t1-1"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	// The existing cached SandboxTokens instance must stop authenticating the
	// revoked token immediately, without requiring a fresh LoadSandboxTokens.
	if name, ok := tokens.Authenticate(gone); ok {
		t.Errorf("cached Authenticate after Revoke = %q, true; want unauthenticated", name)
	}
	if name, ok := tokens.Authenticate(kept); !ok || name != "t2-1" {
		t.Errorf("cached Authenticate for kept token = %q, %v; want t2-1, true", name, ok)
	}

	reloaded, err := LoadSandboxTokens(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.Authenticate(gone); ok {
		t.Error("a revoked sandbox's token should no longer authenticate after reload")
	}
	if name, ok := reloaded.Authenticate(kept); !ok || name != "t2-1" {
		t.Errorf("revoking one sandbox took another's token with it: got %q, %v", name, ok)
	}
}

// A revoked token stops authenticating immediately, even if it was previously
// cached by SandboxTokens.
func TestRevokedTokenCachedHitStopsAuthenticating(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	store := NewSandboxTokenStore(path)

	tok, err := store.EnsureToken("sandbox-revoke")
	if err != nil {
		t.Fatal(err)
	}

	tokens, err := LoadSandboxTokens(path)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Authenticate and populate cache.
	if name, ok := tokens.Authenticate(tok); !ok || name != "sandbox-revoke" {
		t.Fatalf("first Authenticate = %q, %v; want sandbox-revoke, true", name, ok)
	}

	// 2. Revoke the token.
	if err := store.Revoke("sandbox-revoke"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	// 3. Authenticate with the same cached SandboxTokens instance must fail.
	if name, ok := tokens.Authenticate(tok); ok {
		t.Fatalf("Authenticate after Revoke = %q, true; want false", name)
	}
}

// Revoking a sandbox that never minted a token is what a run which failed
// before it got that far leaves behind, and revoking twice is what a
// retried cleanup does. Neither is a fault.
func TestRevokeAnUnknownSandboxIsNotAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	store := NewSandboxTokenStore(path)
	if err := store.Revoke("never-minted"); err != nil {
		t.Fatalf("Revoke against a missing file: %v", err)
	}
	if _, err := store.EnsureToken("t1-1"); err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke("t1-1"); err != nil {
		t.Fatalf("first Revoke: %v", err)
	}
	if err := store.Revoke("t1-1"); err != nil {
		t.Fatalf("second Revoke: %v", err)
	}
}

func TestTokenExpiryStopsAuthenticating(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	store := NewSandboxTokenStore(path)
	store.TTL = time.Hour
	store.Now = func() time.Time { return now }

	token, err := store.EnsureToken("sandbox-0")
	if err != nil {
		t.Fatal(err)
	}

	tokens, err := LoadSandboxTokens(path)
	if err != nil {
		t.Fatal(err)
	}
	tokens.Now = func() time.Time { return now }

	if name, ok := tokens.Authenticate(token); !ok || name != "sandbox-0" {
		t.Fatalf("expected valid token, got %q, %v", name, ok)
	}

	// Move clock forward past expiry
	now = now.Add(61 * time.Minute)

	if _, ok := tokens.Authenticate(token); ok {
		t.Fatal("expected expired token to fail authentication")
	}
}

func TestTokenRefreshExtendsExpiry(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	store := NewSandboxTokenStore(path)
	store.TTL = time.Hour
	store.Now = func() time.Time { return now }

	token, err := store.EnsureToken("sandbox-0")
	if err != nil {
		t.Fatal(err)
	}

	tokens, err := LoadSandboxTokens(path)
	if err != nil {
		t.Fatal(err)
	}
	tokens.Now = func() time.Time { return now }

	// Advance 50 minutes, then refresh
	now = now.Add(50 * time.Minute)
	refreshedToken, err := store.RefreshToken("sandbox-0")
	if err != nil {
		t.Fatalf("RefreshToken failed: %v", err)
	}
	if refreshedToken != token {
		t.Errorf("expected same token on refresh, got %q vs %q", refreshedToken, token)
	}

	// Advance 30 more minutes (80m from start, 30m from refresh)
	now = now.Add(30 * time.Minute)
	if name, ok := tokens.Authenticate(token); !ok || name != "sandbox-0" {
		t.Fatalf("expected token to remain valid after refresh, got %q, %v", name, ok)
	}

	// Advance past refreshed expiry (70m from refresh)
	now = now.Add(40 * time.Minute)
	if _, ok := tokens.Authenticate(token); ok {
		t.Fatal("expected token to expire past refreshed TTL")
	}
}

// A refresh brings back the token the sandbox already holds, even one
// that lapsed before it came, and never mints one in place of a revoked
// token: the sandbox could not use a token it was never given.
func TestRefreshTokenRevivesTheSandboxsOwnTokenAndNeverMints(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	store := NewSandboxTokenStore(path)
	store.TTL = time.Hour
	store.Now = func() time.Time { return now }
	token, err := store.EnsureToken("sandbox-0")
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := LoadSandboxTokens(path)
	if err != nil {
		t.Fatal(err)
	}
	tokens.Now = func() time.Time { return now }

	now = now.Add(2 * time.Hour)
	if _, ok := tokens.Authenticate(token); ok {
		t.Fatal("a token past its TTL still authenticates")
	}
	if got, err := store.RefreshToken("sandbox-0"); err != nil || got != token {
		t.Fatalf("RefreshToken = %q, %v, want the sandbox's own token", got, err)
	}
	if name, ok := tokens.Authenticate(token); !ok || name != "sandbox-0" {
		t.Fatalf("Authenticate after refreshing a lapsed token = %q, %v", name, ok)
	}

	if err := store.Revoke("sandbox-0"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RefreshToken("sandbox-0"); !errors.Is(err, ErrNoSandboxToken) {
		t.Fatalf("RefreshToken after Revoke = %v, want ErrNoSandboxToken", err)
	}
	if _, err := store.RefreshToken("sandbox-never"); !errors.Is(err, ErrNoSandboxToken) {
		t.Fatalf("RefreshToken for an unknown sandbox = %v, want ErrNoSandboxToken", err)
	}
	reread, err := LoadSandboxTokens(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reread.byToken) != 0 {
		t.Fatalf("refreshing a revoked sandbox left %d tokens on file, want none", len(reread.byToken))
	}
}

func TestEnsureTokenRemintsExpiredToken(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	store := NewSandboxTokenStore(path)
	store.TTL = time.Hour
	store.Now = func() time.Time { return now }

	tok1, err := store.EnsureToken("sandbox-0")
	if err != nil {
		t.Fatal(err)
	}

	// Advance clock past expiration
	now = now.Add(2 * time.Hour)

	tok2, err := store.EnsureToken("sandbox-0")
	if err != nil {
		t.Fatal(err)
	}
	if tok1 == tok2 {
		t.Fatal("expected fresh token minted after previous token expired")
	}

	tokens, err := LoadSandboxTokens(path)
	if err != nil {
		t.Fatal(err)
	}
	tokens.Now = func() time.Time { return now }

	if _, ok := tokens.Authenticate(tok1); ok {
		t.Fatal("old expired token should not authenticate")
	}
	if name, ok := tokens.Authenticate(tok2); !ok || name != "sandbox-0" {
		t.Fatalf("new token should authenticate: %q, %v", name, ok)
	}
}

func TestEncryptionWithExplicitKey(t *testing.T) {
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	store := NewSandboxTokenStore(path)
	store.Key = &key

	token, err := store.EnsureToken("sandbox-custom-key")
	if err != nil {
		t.Fatal(err)
	}

	tokens := &SandboxTokens{path: path, Key: &key}
	if err := tokens.reload(); err != nil {
		t.Fatalf("failed to reload with explicit key: %v", err)
	}
	if name, ok := tokens.Authenticate(token); !ok || name != "sandbox-custom-key" {
		t.Fatalf("Authenticate = %q, %v, want sandbox-custom-key, true", name, ok)
	}

	// A wrong key reads the file as holding no tokens: nothing
	// authenticates, but nothing fails either (see readTokenFile).
	wrongKey, _ := secrets.GenerateKey()
	wrongTokens := &SandboxTokens{path: path, Key: &wrongKey}
	if err := wrongTokens.reload(); err != nil {
		t.Fatalf("reload with a wrong key = %v, want the file read as empty", err)
	}
	if name, ok := wrongTokens.Authenticate(token); ok {
		t.Fatalf("Authenticate under a wrong key = %q, true, want false", name)
	}
}

// The operator's secrets.key is theirs to import or replace
// (secrets.Store.ImportKey). Replacing it used to leave this file sealed
// to a key nobody held, failing every mint and the proxy's own startup.
func TestReplacingTheOperatorsSecretsKeyLeavesTokensReadable(t *testing.T) {
	dir := t.TempDir()
	operatorKey := filepath.Join(dir, secrets.DefaultKeyFileName)
	first, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.WriteKeyFile(operatorKey, first); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sandbox-tokens.json")
	store := NewSandboxTokenStore(path)
	token, err := store.EnsureToken("sandbox-0")
	if err != nil {
		t.Fatal(err)
	}

	second, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.WriteKeyFile(operatorKey, second); err != nil {
		t.Fatal(err)
	}

	again, err := store.EnsureToken("sandbox-0")
	if err != nil {
		t.Fatalf("EnsureToken after the operator key was replaced: %v", err)
	}
	if again != token {
		t.Fatalf("EnsureToken = %q after the key change, want the token already minted %q", again, token)
	}
	tokens, err := LoadSandboxTokens(path)
	if err != nil {
		t.Fatalf("LoadSandboxTokens after the operator key was replaced: %v", err)
	}
	if name, ok := tokens.Authenticate(token); !ok || name != "sandbox-0" {
		t.Fatalf("Authenticate = %q, %v, want sandbox-0, true", name, ok)
	}
	got, err := secrets.ReadKeyFile(operatorKey)
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != second.String() {
		t.Fatal("the token store rewrote the operator's secrets.key")
	}
}

// The token store never mints the operator's secrets.key itself: that is
// the state repo's to create, with its own "back this file up" notice.
func TestMintingDoesNotCreateTheOperatorsSecretsKey(t *testing.T) {
	dir := t.TempDir()
	store := NewSandboxTokenStore(filepath.Join(dir, "sandbox-tokens.json"))
	if _, err := store.EnsureToken("sandbox-0"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, secrets.DefaultKeyFileName)); !os.IsNotExist(err) {
		t.Fatalf("minting a token created %s (stat: %v)", secrets.DefaultKeyFileName, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sandbox-tokens.key")); err != nil {
		t.Fatalf("minting a token did not create sandbox-tokens.key: %v", err)
	}
}

// A file sealed with the operator's key, as every deployment's was
// before sandbox-tokens.key, still reads -- runs in flight across an
// upgrade keep their tokens -- and is resealed to the dedicated key on
// the next write.
func TestAFileSealedWithTheLegacyOperatorKeyStillReads(t *testing.T) {
	dir := t.TempDir()
	operator, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.WriteKeyFile(filepath.Join(dir, secrets.DefaultKeyFileName), operator); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sandbox-tokens.json")
	plain, _ := json.Marshal(map[string]tokenRecord{
		"sandbox-0": {Sandbox: "sandbox-0", Token: "legacy-token", Expires: time.Now().Add(time.Hour)},
	})
	sealed, err := secrets.Encrypt(operator, plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, sealed, 0o600); err != nil {
		t.Fatal(err)
	}

	tokens, err := LoadSandboxTokens(path)
	if err != nil {
		t.Fatal(err)
	}
	if name, ok := tokens.Authenticate("legacy-token"); !ok || name != "sandbox-0" {
		t.Fatalf("Authenticate = %q, %v, want sandbox-0, true", name, ok)
	}

	store := NewSandboxTokenStore(path)
	if _, err := store.EnsureToken("sandbox-1"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Decrypt(operator, data); !errors.Is(err, secrets.ErrWrongKey) {
		t.Fatalf("after a write the file still opens with the operator key (err %v)", err)
	}
	if name, ok := tokens.Authenticate("legacy-token"); !ok || name != "sandbox-0" {
		t.Fatalf("after reseal, Authenticate = %q, %v, want sandbox-0, true", name, ok)
	}
}

// Losing the dedicated key costs the tokens sealed to it, not every mint
// after: the store re-mints and reseals to a fresh key.
func TestReplacingTheTokenKeyRemintsInsteadOfFailing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sandbox-tokens.json")
	store := NewSandboxTokenStore(path)
	old, err := store.EnsureToken("sandbox-0")
	if err != nil {
		t.Fatal(err)
	}
	other, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.WriteKeyFile(filepath.Join(dir, "sandbox-tokens.key"), other); err != nil {
		t.Fatal(err)
	}

	tokens, err := LoadSandboxTokens(path)
	if err != nil {
		t.Fatalf("LoadSandboxTokens with the token key replaced: %v", err)
	}
	if _, ok := tokens.Authenticate(old); ok {
		t.Fatal("a token sealed to a key that is gone still authenticates")
	}
	fresh, err := store.EnsureToken("sandbox-0")
	if err != nil {
		t.Fatalf("EnsureToken with the token key replaced: %v", err)
	}
	if fresh == old {
		t.Fatal("EnsureToken returned the unreadable token rather than minting a new one")
	}
	if name, ok := tokens.Authenticate(fresh); !ok || name != "sandbox-0" {
		t.Fatalf("Authenticate(re-minted) = %q, %v, want sandbox-0, true", name, ok)
	}
}

// Legacy plaintext records carry no expiry; the first write stamps a
// full TTL on them rather than leaving them valid forever.
func TestLegacyRecordsGetAnExpiryOnTheNextWrite(t *testing.T) {
	dir := t.TempDir()
	path := writeTokensFile(t, dir, map[string]string{"sandbox-0": "legacy-token"})
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store := NewSandboxTokenStore(path)
	store.Now = func() time.Time { return now }
	if _, err := store.EnsureToken("sandbox-1"); err != nil {
		t.Fatal(err)
	}

	tokens, err := LoadSandboxTokens(path)
	if err != nil {
		t.Fatal(err)
	}
	tokens.Now = func() time.Time { return now.Add(time.Hour) }
	if _, ok := tokens.Authenticate("legacy-token"); !ok {
		t.Fatal("a legacy token stopped authenticating inside its new TTL")
	}
	tokens.Now = func() time.Time { return now.Add(DefaultSandboxTokenTTL + time.Minute) }
	if _, ok := tokens.Authenticate("legacy-token"); ok {
		t.Fatal("a legacy token still authenticates past the TTL stamped on it")
	}
}

// ageTokenFile sets path's mtime well outside tokenFileRacyWindow, as a
// file minted a while before the proxy is asked about it would be.
func ageTokenFile(t *testing.T, path string) time.Time {
	t.Helper()
	old := time.Now().Add(-time.Minute).Truncate(time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	return old
}

// A flood of distinct bogus tokens used to cost a read and decryption of
// the token file each, under the write lock. A miss now re-reads only a
// file that has changed, so the flood costs no reads at all -- while a
// token minted just before its first use still authenticates.
func TestBogusTokensCauseABoundedNumberOfFileReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	store := NewSandboxTokenStore(path)
	if _, err := store.EnsureToken("t1-1"); err != nil {
		t.Fatal(err)
	}
	ageTokenFile(t, path)

	tokens, err := LoadSandboxTokens(path)
	if err != nil {
		t.Fatal(err)
	}
	if tokens.reads != 1 {
		t.Fatalf("loading read the file %d times, want 1", tokens.reads)
	}

	const bogus = 1000
	var wg sync.WaitGroup
	for i := 0; i < bogus; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, ok := tokens.Authenticate(fmt.Sprintf("bogus-%d", i)); ok {
				t.Errorf("bogus-%d authenticated", i)
			}
		}(i)
	}
	wg.Wait()
	if tokens.reads != 1 {
		t.Errorf("%d bogus tokens read the file %d more times, want none", bogus, tokens.reads-1)
	}

	fresh, err := store.EnsureToken("t2-1")
	if err != nil {
		t.Fatal(err)
	}
	if name, ok := tokens.Authenticate(fresh); !ok || name != "t2-1" {
		t.Fatalf("a token minted just before its use = %q, %v; want t2-1, true", name, ok)
	}
	if tokens.reads != 2 {
		t.Errorf("seeing a freshly minted token took %d reads, want 1", tokens.reads-1)
	}

	ageTokenFile(t, path)
	if _, ok := tokens.Authenticate("bogus-after-aging"); ok {
		t.Error("bogus token authenticated")
	}
	for i := 0; i < bogus; i++ {
		tokens.Authenticate(fmt.Sprintf("bogus-again-%d", i))
	}
	// One read for the file whose stat aging changed, then none.
	if tokens.reads != 3 {
		t.Errorf("%d more bogus tokens took %d reads, want 1", bogus+1, tokens.reads-2)
	}
}

// A coarse-mtime filesystem can give a replacement file the very size
// and mtime of the one it replaced. The store writes by rename, so the
// replacement is a different file all the same, and is read.
func TestAReplacementWithTheSameSizeAndMtimeIsStillRead(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	store := NewSandboxTokenStore(path)
	store.Now = func() time.Time { return now }
	if _, err := store.EnsureToken("t1-1"); err != nil {
		t.Fatal(err)
	}
	mtime := ageTokenFile(t, path)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	tokens, err := LoadSandboxTokens(path)
	if err != nil {
		t.Fatal(err)
	}
	tokens.Now = func() time.Time { return now }

	rotated, err := store.Rotate("t1-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("the replacement differs in size or mtime (%d %v vs %d %v), so this tests nothing",
			after.Size(), after.ModTime(), before.Size(), before.ModTime())
	}

	if name, ok := tokens.Authenticate(rotated); !ok || name != "t1-1" {
		t.Errorf("Authenticate(rotated) = %q, %v; want t1-1, true", name, ok)
	}
}

// A file written within tokenFileRacyWindow of its read cannot be told
// apart by stat from one written in the same mtime tick just after it, so
// it is never trusted to be unchanged.
func TestARecentlyWrittenFileIsNeverTrustedUnchanged(t *testing.T) {
	path := writeTokensFile(t, t.TempDir(), map[string]string{"sandbox-0": "tok0"})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	racy := tokenFileVersion{read: true, info: info, readAt: info.ModTime().Add(tokenFileRacyWindow / 2)}
	if racy.unchanged(info) {
		t.Error("a file read within the racy window of its mtime was trusted unchanged")
	}
	settled := tokenFileVersion{read: true, info: info, readAt: info.ModTime().Add(tokenFileRacyWindow)}
	if !settled.unchanged(info) {
		t.Error("a file read well after its mtime, statted the same, was not trusted unchanged")
	}
	if settled.unchanged(nil) {
		t.Error("a file that has gone was trusted unchanged")
	}
	if !(tokenFileVersion{read: true}).unchanged(nil) {
		t.Error("a file still missing was not trusted unchanged")
	}
	if (tokenFileVersion{}).unchanged(nil) {
		t.Error("a file never read was trusted unchanged")
	}
}
