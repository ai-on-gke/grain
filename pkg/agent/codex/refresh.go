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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Grain renews ChatGPT subscription logins itself, ahead of the CLI.
//
// codex 0.153.0 refreshes a login when its access token is under five
// minutes from expiry (or on a 401), guarded only by an in-process
// semaphore: several processes sharing one auth.json -- every run on a
// backend, and runs of different daemon generations -- can each spend the
// same rotating refresh token. docs/codex-authentication.md ("Several
// processes, one login") has the measurement. So grain refreshes each
// login well before codex's window opens, under a lock per login file,
// and writes the result atomically; a codex process then always finds a
// fresh access token and never reaches its own refresh.

const (
	// refreshClientID and refreshTokenURL are codex's own grant
	// (codex-rs/login/src/auth/manager.rs): the login was issued to that
	// client, so only that client can renew it.
	refreshClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	refreshTokenURL = "https://auth.openai.com/oauth/token"
	// RefreshTokenURLOverrideEnv is the variable codex itself reads to
	// point its refresh at another authority. Grain honours it too, so a
	// test's stub authority serves both.
	RefreshTokenURLOverrideEnv = "CODEX_REFRESH_TOKEN_URL_OVERRIDE"

	// refreshUserAgent identifies grain's refreshes to the authority, and
	// lets a stub authority tell them from codex's own.
	refreshUserAgent = "grain-codex-login-refresher"

	// minRefreshLead is the least time before exp grain refreshes at:
	// twice codex's five-minute window, so one missed tick never hands
	// the refresh back to the CLI.
	minRefreshLead = 10 * time.Minute
	// unknownLifetimeLead applies when the token's lifetime cannot be
	// read (no iat and no last_refresh).
	unknownLifetimeLead = time.Hour
	// staleWithoutExp mirrors codex's own rule for a token whose exp it
	// cannot read (last_refresh over 8 days old), at half the age.
	staleWithoutExp = 4 * 24 * time.Hour

	// DefaultRefreshInterval is how often a Refresher looks at its files.
	// Looking is a file read; only a due login costs a request.
	DefaultRefreshInterval = time.Minute
)

// RefreshOutcome says what one RefreshAuthFile call did.
type RefreshOutcome int

const (
	// RefreshNotDue: the access token has enough life left.
	RefreshNotDue RefreshOutcome = iota
	// RefreshNotApplicable: the file holds no refreshable login (an API
	// key auth.json, or no refresh token).
	RefreshNotApplicable
	// RefreshDone: grain renewed the login and rewrote the file.
	RefreshDone
	// RefreshBlocked: an earlier refresh of this same refresh token was
	// refused permanently; nothing is sent until a new login is uploaded.
	RefreshBlocked
)

// PermanentRefreshError is an authority refusal no retry can fix: the
// refresh token is spent, expired or revoked, and only a new login (a
// fresh `codex login` uploaded to grain) brings the file back.
type PermanentRefreshError struct {
	Code    string
	Message string
}

func (e *PermanentRefreshError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("codex: the login's refresh token was refused (%s): %s", e.Code, e.Message)
	}
	return fmt.Sprintf("codex: the login's refresh token was refused (%s)", e.Code)
}

// permanentRefreshCodes are the refusals codex itself treats as final for
// a cached token (RefreshTokenFailedReason in codex-rs), plus OAuth's own
// invalid_grant.
var permanentRefreshCodes = map[string]bool{
	"refresh_token_expired":     true,
	"refresh_token_reused":      true,
	"refresh_token_invalidated": true,
	"invalid_grant":             true,
}

// RefreshFailure is a permanent refusal recorded beside a login file, so
// the UI can say the login needs replacing without anybody deleting it.
type RefreshFailure struct {
	Code    string    `json:"code"`
	Message string    `json:"message,omitempty"`
	At      time.Time `json:"at"`
	// RefreshToken is a fingerprint of the refused refresh token. The
	// failure applies only while the file still holds that token, so a
	// login replaced by any route clears it.
	RefreshToken string `json:"refreshTokenSha256"`
}

func (f *RefreshFailure) String() string {
	if f.Message != "" {
		return fmt.Sprintf("the subscription login can no longer be refreshed (%s: %s); upload a new auth.json from `codex login`", f.Code, f.Message)
	}
	return fmt.Sprintf("the subscription login can no longer be refreshed (%s); upload a new auth.json from `codex login`", f.Code)
}

// RefreshOptions tunes RefreshAuthFile. The zero value is production.
type RefreshOptions struct {
	// Client defaults to a client with a 30-second timeout.
	Client *http.Client
	// TokenURL defaults to $CODEX_REFRESH_TOKEN_URL_OVERRIDE, else
	// codex's own authority.
	TokenURL string
	// Now defaults to time.Now.
	Now func() time.Time
	// Force refreshes even when the token is not yet due.
	Force bool
}

func (o RefreshOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o RefreshOptions) tokenURL() string {
	if o.TokenURL != "" {
		return o.TokenURL
	}
	if url := os.Getenv(RefreshTokenURLOverrideEnv); url != "" {
		return url
	}
	return refreshTokenURL
}

var defaultRefreshClient = &http.Client{Timeout: 30 * time.Second}

func (o RefreshOptions) client() *http.Client {
	if o.Client != nil {
		return o.Client
	}
	return defaultRefreshClient
}

// refreshLocks serializes refreshes of one login inside this process; the
// lock file serializes them across processes (daemon generations).
var refreshLocks sync.Map // resolved path -> *sync.Mutex

// lockLogin takes both locks for target, the resolved login file. The
// lock file sits beside it, in the shared credential directory, and is
// never removed: removing a lock file races the next locker.
func lockLogin(ctx context.Context, target string) (func(), error) {
	value, _ := refreshLocks.LoadOrStore(target, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	lockPath := filepath.Join(filepath.Dir(target), "."+filepath.Base(target)+".refresh.lock")
	file, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		mu.Unlock()
		return nil, fmt.Errorf("codex: opening the login's refresh lock: %w", err)
	}
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			file.Close()
			mu.Unlock()
			return nil, fmt.Errorf("codex: locking the login for refresh: %w", err)
		}
		select {
		case <-ctx.Done():
			file.Close()
			mu.Unlock()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return func() {
		syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		file.Close()
		mu.Unlock()
	}, nil
}

// failurePath is where a permanent refusal of target is recorded.
func failurePath(target string) string {
	return filepath.Join(filepath.Dir(target), "."+filepath.Base(target)+".refresh-failure.json")
}

func fingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ReadRefreshFailure reports the permanent refusal recorded for the login
// at path, or nil when there is none or the file has since been given a
// different refresh token.
func ReadRefreshFailure(path string) *RefreshFailure {
	if path == "" {
		return nil
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(failurePath(target))
	if err != nil {
		return nil
	}
	var failure RefreshFailure
	if json.Unmarshal(data, &failure) != nil {
		return nil
	}
	login, err := readLogin(target)
	if err != nil || login.refreshToken == "" || fingerprint(login.refreshToken) != failure.RefreshToken {
		return nil
	}
	return &failure
}

// login is an auth.json read for refresh: the whole object, so unknown
// keys survive the rewrite, plus the fields the refresh needs.
type login struct {
	object       map[string]json.RawMessage
	tokens       map[string]json.RawMessage
	accessToken  string
	refreshToken string
	lastRefresh  time.Time
}

func readLogin(path string) (*login, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	l := &login{}
	if err := json.Unmarshal(data, &l.object); err != nil || l.object == nil {
		return nil, fmt.Errorf("codex: %s is not a JSON object", path)
	}
	if raw, ok := l.object["tokens"]; ok && json.Unmarshal(raw, &l.tokens) == nil && l.tokens != nil {
		json.Unmarshal(l.tokens["access_token"], &l.accessToken)
		json.Unmarshal(l.tokens["refresh_token"], &l.refreshToken)
	}
	var lastRefresh string
	if json.Unmarshal(l.object["last_refresh"], &lastRefresh) == nil {
		l.lastRefresh, _ = time.Parse(time.RFC3339Nano, lastRefresh)
	}
	return l, nil
}

// jwtTimes reads exp and iat from a JWT's payload without verifying it:
// grain only schedules by them, the authority is what checks them.
func jwtTimes(token string) (exp, iat time.Time, ok bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return exp, iat, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return exp, iat, false
	}
	var claims struct {
		Exp float64 `json:"exp"`
		Iat float64 `json:"iat"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == 0 {
		return exp, iat, false
	}
	exp = time.Unix(int64(claims.Exp), 0)
	if claims.Iat != 0 {
		iat = time.Unix(int64(claims.Iat), 0)
	}
	return exp, iat, true
}

// refreshDueAt is when grain renews this login: halfway through the
// access token's life (issued at iat, else at last_refresh), and never
// later than minRefreshLead before exp. Without a readable exp it falls
// back to the age of last_refresh, as codex does.
func (l *login) refreshDueAt() time.Time {
	exp, iat, ok := jwtTimes(l.accessToken)
	if !ok {
		if l.lastRefresh.IsZero() {
			return time.Time{}
		}
		return l.lastRefresh.Add(staleWithoutExp)
	}
	issued := iat
	if issued.IsZero() {
		issued = l.lastRefresh
	}
	lead := unknownLifetimeLead
	if !issued.IsZero() && exp.After(issued) {
		lead = exp.Sub(issued) / 2
	}
	if lead < minRefreshLead {
		lead = minRefreshLead
	}
	return exp.Add(-lead)
}

// RefreshAuthFile renews the ChatGPT login at path if it is due (or if
// opts.Force), under the login's lock, and writes the renewed tokens
// atomically over the file path resolves to. A permanent refusal is
// recorded for ReadRefreshFailure and returned as *PermanentRefreshError;
// the login file itself is left as it was.
func RefreshAuthFile(ctx context.Context, path string, opts RefreshOptions) (RefreshOutcome, error) {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return RefreshNotApplicable, fmt.Errorf("codex: resolving login file: %w", err)
	}
	// Look before locking: almost every look finds nothing due.
	if l, err := readLogin(target); err != nil {
		return RefreshNotApplicable, err
	} else if outcome, ok := precheck(l, target, opts); !ok {
		return outcome, nil
	}
	unlock, err := lockLogin(ctx, target)
	if err != nil {
		return RefreshNotApplicable, err
	}
	defer unlock()
	// Read again under the lock: another generation may have just
	// refreshed, and its tokens are the ones to keep.
	l, err := readLogin(target)
	if err != nil {
		return RefreshNotApplicable, err
	}
	if outcome, ok := precheck(l, target, opts); !ok {
		return outcome, nil
	}

	tokens, err := requestRefresh(ctx, opts, l.refreshToken)
	if err != nil {
		var permanent *PermanentRefreshError
		if errors.As(err, &permanent) {
			recordFailure(target, l.refreshToken, permanent, opts.now())
		}
		return RefreshNotApplicable, err
	}
	for key, value := range tokens {
		encoded, _ := json.Marshal(value)
		l.tokens[key] = encoded
	}
	l.object["tokens"], _ = json.Marshal(l.tokens)
	l.object["last_refresh"], _ = json.Marshal(opts.now().UTC().Format(time.RFC3339Nano))
	data, err := json.MarshalIndent(l.object, "", "  ")
	if err != nil {
		return RefreshNotApplicable, err
	}
	if err := writeFileAtomic(target, data); err != nil {
		return RefreshNotApplicable, err
	}
	os.Remove(failurePath(target))
	return RefreshDone, nil
}

// precheck reports whether l needs a refresh request; when it does not,
// outcome says why.
func precheck(l *login, target string, opts RefreshOptions) (RefreshOutcome, bool) {
	if l.tokens == nil || l.refreshToken == "" {
		return RefreshNotApplicable, false
	}
	if failure := ReadRefreshFailure(target); failure != nil {
		return RefreshBlocked, false
	}
	if !opts.Force && opts.now().Before(l.refreshDueAt()) {
		return RefreshNotDue, false
	}
	return 0, true
}

func recordFailure(target, refreshToken string, refusal *PermanentRefreshError, now time.Time) {
	data, _ := json.Marshal(RefreshFailure{Code: refusal.Code, Message: refusal.Message, At: now.UTC(), RefreshToken: fingerprint(refreshToken)})
	if err := writeFileAtomic(failurePath(target), data); err != nil {
		log.Printf("codex: recording the refused refresh of %s: %v", target, err)
	}
}

// requestRefresh is codex's refresh grant. It returns only the tokens the
// authority sent, keyed as auth.json's tokens object keys them.
func requestRefresh(ctx context.Context, opts RefreshOptions, refreshToken string) (map[string]string, error) {
	body, _ := json.Marshal(map[string]string{
		"client_id":     refreshClientID,
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, opts.tokenURL(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("codex: building refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", refreshUserAgent)
	resp, err := opts.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("codex: refreshing login: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		code, message := refusalCode(data)
		if permanentRefreshCodes[code] || (resp.StatusCode == http.StatusUnauthorized && code != "") {
			return nil, &PermanentRefreshError{Code: code, Message: message}
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, &PermanentRefreshError{Code: "unauthorized", Message: message}
		}
		return nil, fmt.Errorf("codex: refreshing login: authority answered %s", resp.Status)
	}
	var answer struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(data, &answer); err != nil || answer.AccessToken == "" {
		return nil, fmt.Errorf("codex: refreshing login: the authority's answer carries no access token")
	}
	tokens := map[string]string{"access_token": answer.AccessToken}
	if answer.IDToken != "" {
		tokens["id_token"] = answer.IDToken
	}
	if answer.RefreshToken != "" {
		tokens["refresh_token"] = answer.RefreshToken
	}
	return tokens, nil
}

// refusalCode reads an OAuth error body in either shape the authority
// uses: {"error":"invalid_grant","error_description":…} or
// {"error":{"code":"refresh_token_reused","message":…}}.
func refusalCode(data []byte) (code, message string) {
	var body struct {
		Error       json.RawMessage `json:"error"`
		Description string          `json:"error_description"`
		Code        string          `json:"code"`
		Message     string          `json:"message"`
	}
	if json.Unmarshal(data, &body) != nil {
		return "", ""
	}
	if json.Unmarshal(body.Error, &code) == nil && code != "" {
		return code, body.Description
	}
	var nested struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body.Error, &nested) == nil && nested.Code != "" {
		return nested.Code, nested.Message
	}
	return body.Code, body.Message
}

// writeFileAtomic replaces target with data, mode 0600, by a synced
// temporary file renamed over it. target must already be resolved: the
// rename replaces whatever is at that path, so renaming over a symlink
// would cut every run's link to the shared login.
func writeFileAtomic(target string, data []byte) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(target)+".*")
	if err != nil {
		return fmt.Errorf("codex: creating refreshed login: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("codex: protecting refreshed login: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("codex: writing refreshed login: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("codex: syncing refreshed login: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("codex: closing refreshed login: %w", err)
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return fmt.Errorf("codex: installing refreshed login: %w", err)
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// Refresher keeps a set of login files renewed. Files is asked afresh on
// every pass, so logins uploaded or cleared through the UI are picked up
// without restarting anything.
type Refresher struct {
	Files    func() []string
	Interval time.Duration
	Options  RefreshOptions
	// Logf defaults to log.Printf.
	Logf func(format string, args ...any)
}

func (r *Refresher) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// RefreshDue makes one pass over the files, renewing each that is due.
// A missing file is skipped: it is a login nobody has uploaded.
func (r *Refresher) RefreshDue(ctx context.Context) {
	seen := map[string]bool{}
	for _, path := range r.Files() {
		if path == "" {
			continue
		}
		target, err := filepath.EvalSymlinks(path)
		if err != nil || seen[target] {
			continue
		}
		seen[target] = true
		outcome, err := RefreshAuthFile(ctx, target, r.Options)
		switch {
		case err != nil:
			r.logf("codex: renewing subscription login %s: %v", path, err)
		case outcome == RefreshDone:
			r.logf("codex: renewed subscription login %s", path)
		}
	}
}

// Run makes a pass every Interval until ctx ends.
func (r *Refresher) Run(ctx context.Context) {
	interval := r.Interval
	if interval <= 0 {
		interval = DefaultRefreshInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		r.RefreshDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
