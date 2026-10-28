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

package ui

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The access code is a single-tenant daemon's one
// credential (grain/task-4343). It is the local API token
// (localtoken.go) under another name: one random secret, generated at
// first start, kept in LocalAPITokenPath(dataDir) mode 0600 and owned
// by the account grain runs as. A person types it into the "Enter
// access code" page and gets a session cookie tied to it
// (accessSessionToken); the grain CLI, and the processes the daemon
// starts that are handed LocalAPITokenFileEnv (a run's `grain
// mcpserver`), read it from the file and send it as a bearer token. A
// single-tenant daemon trusts nothing else -- not loopback, not a
// network -- so the code, plus a URL that
// reaches the daemon, is the whole of owner access.
//
// One file rather than a second secret beside the local API token: the
// two would be the same thing (whoever holds either is the operator),
// readable by the same accounts, and two would only mean two to rotate.
const (
	// AccessCookieName is the cookie a single-tenant daemon's access
	// session lives in.
	AccessCookieName = "grain_access"
	// DefaultAccessSessionLifetime is how long an access session lasts
	// without being used (Config.AccessSessionLifetime).
	DefaultAccessSessionLifetime = 90 * 24 * time.Hour
	// accessSessionRenewAfter is how old an access session gets before a
	// request carrying it is handed a fresh one, so a session in use
	// does not expire while not setting a cookie on every response.
	accessSessionRenewAfter = time.Hour
	// accessCodeBytes is the access code's entropy: 160 bits.
	accessCodeBytes = 20
)

// AccessSessionLifetimeUsage is the daemon's -ui-access-session-lifetime
// help text.
const AccessSessionLifetimeUsage = "on this single-tenant daemon, how long a browser stays signed in after entering " +
	"the access code without using the UI; each use renews it. Rotating the code (grain access-code rotate) ends every session at once"

var accessCodeEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// newAccessCode is a fresh random access code: accessCodeBytes of
// crypto/rand in base32, in dash-separated groups of four, so it can be
// read aloud and pasted without a character going astray.
func newAccessCode() (string, error) {
	var raw [accessCodeBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	enc := accessCodeEncoding.EncodeToString(raw[:])
	var groups []string
	for len(enc) > 4 {
		groups = append(groups, enc[:4])
		enc = enc[4:]
	}
	groups = append(groups, enc)
	return strings.Join(groups, "-"), nil
}

// canonicalAccessCode is code as it is compared: case, dashes and
// whitespace ignored, so a code typed in lower case or pasted with a
// trailing newline is the same code. A local API token from before
// access codes ("grain-local-<hex>") canonicalizes the same way on both
// sides, so an installation keeps its token across the upgrade.
func canonicalAccessCode(code string) string {
	var b strings.Builder
	for _, r := range code {
		switch {
		case r == '-' || r == ' ' || r == '\t' || r == '\n' || r == '\r':
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 'a' + 'A')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// matchesAccessCode reports whether got is want, in constant time: both
// are hashed first, so neither the comparison nor an early length check
// says anything about how close a guess came.
func matchesAccessCode(got, want string) bool {
	g, w := canonicalAccessCode(got), canonicalAccessCode(want)
	if g == "" || w == "" {
		return false
	}
	gh, wh := sha256.Sum256([]byte(g)), sha256.Sum256([]byte(w))
	return subtle.ConstantTimeCompare(gh[:], wh[:]) == 1
}

// writeAccessCodeFile writes code to path atomically, with mode perm.
func writeAccessCodeFile(path, code string, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+LocalAPITokenFileName+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(code + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// RotateLocalAPIToken replaces the access code (the local API token) in
// the file at path with a new random one and returns it. A running
// daemon reads the file again within accessCodeRecheck, after which the
// old code, and every UI session signed with it, is refused. The
// file's mode is kept if it is already there (0640 where scripts/
// setup.sh granted the account's group read, never more), else 0600.
func RotateLocalAPIToken(path string) (string, error) {
	perm := os.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		perm = fi.Mode().Perm()&0o640 | 0o600
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	code, err := newAccessCode()
	if err != nil {
		return "", err
	}
	if err := writeAccessCodeFile(path, code, perm); err != nil {
		return "", err
	}
	return code, nil
}

// accessCodeRecheck is how often a daemon looks at its token file again
// for a rotation (accessCodeFile.current).
const accessCodeRecheck = time.Second

// accessCodeFile is the daemon's view of its token file: what it holds,
// read again when the file changes, so `grain access-code rotate` takes
// effect without a restart. A file that has gone, or will not read,
// holds no code at all -- every caller is then refused rather than the
// old code kept.
type accessCodeFile struct {
	path     string
	fallback string

	mu      sync.Mutex
	checked time.Time
	info    os.FileInfo
	code    string
}

func (f *accessCodeFile) current() string {
	if f == nil {
		return ""
	}
	if f.path == "" {
		return f.fallback
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	if !f.checked.IsZero() && now.Sub(f.checked) < accessCodeRecheck {
		return f.code
	}
	f.checked = now
	fi, err := os.Stat(f.path)
	if err != nil {
		f.code, f.info = "", nil
		return ""
	}
	// Rotation renames a new file into place, so it is a different file
	// even where the clock has not moved on enough to change its mtime.
	if f.code != "" && f.info != nil && os.SameFile(f.info, fi) && fi.ModTime().Equal(f.info.ModTime()) && fi.Size() == f.info.Size() {
		return f.code
	}
	b, err := os.ReadFile(f.path)
	if err != nil {
		f.code = ""
		return ""
	}
	f.code, f.info = strings.TrimSpace(string(b)), fi
	return f.code
}

// localAPITokenNow is the local API token (the access code) as it
// stands: re-read from Config.LocalAPITokenFile where the daemon named
// one, else Config.LocalAPIToken.
func (s *Server) localAPITokenNow() string {
	if s == nil || s.tasks == nil {
		return ""
	}
	f := s.accessCode.Load()
	if f == nil {
		s.accessCode.CompareAndSwap(nil, &accessCodeFile{path: s.tasks.Config.LocalAPITokenFile, fallback: s.tasks.Config.LocalAPIToken})
		f = s.accessCode.Load()
	}
	return f.current()
}

// accessSessionKey is the key access sessions are signed with: derived
// from the code, so rotating the code ends every session signed with
// the old one, and the cookie carries a MAC rather than the code itself.
func accessSessionKey(code string) []byte {
	sum := sha256.Sum256([]byte("grain access session\x00" + canonicalAccessCode(code)))
	return sum[:]
}

func accessSessionMAC(code, payload string) []byte {
	mac := hmac.New(sha256.New, accessSessionKey(code))
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}

// accessSessionToken is the session cookie's value for a session issued
// at issued: "v1.<unix seconds>.<HMAC>", the HMAC keyed by the code.
func accessSessionToken(code string, issued time.Time) string {
	payload := "v1." + strconv.FormatInt(issued.Unix(), 10)
	return payload + "." + base64.RawURLEncoding.EncodeToString(accessSessionMAC(code, payload))
}

// verifyAccessSession reports whether token is an access session the
// current code signed, issued no longer than lifetime ago, and when.
func verifyAccessSession(token, code string, lifetime time.Duration, now time.Time) (time.Time, bool) {
	if canonicalAccessCode(code) == "" || token == "" {
		return time.Time{}, false
	}
	i := strings.LastIndexByte(token, '.')
	if i < 0 {
		return time.Time{}, false
	}
	payload, sig := token[:i], token[i+1:]
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, accessSessionMAC(code, payload)) {
		return time.Time{}, false
	}
	secs, ok := strings.CutPrefix(payload, "v1.")
	if !ok {
		return time.Time{}, false
	}
	n, err := strconv.ParseInt(secs, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	issued := time.Unix(n, 0)
	if lifetime <= 0 {
		lifetime = DefaultAccessSessionLifetime
	}
	if issued.After(now.Add(5*time.Minute)) || now.Sub(issued) > lifetime {
		return time.Time{}, false
	}
	return issued, true
}

// accessLifetime is cfg's access session lifetime, defaulted.
func accessLifetime(cfg IAPConfig) time.Duration {
	if cfg.AccessSessionLifetime > 0 {
		return cfg.AccessSessionLifetime
	}
	return DefaultAccessSessionLifetime
}

// presentsAccessCode reports whether r carries the access code as a
// bearer token. Unlike presentsLocalAPIToken it does not ask where the
// request came from: on a single-tenant daemon the code is the
// credential wherever it is presented from.
func presentsAccessCode(r *http.Request, cfg IAPConfig) bool {
	got, ok := bearerToken(r)
	return ok && matchesAccessCode(got, cfg.LocalAPIToken)
}

func bearerToken(r *http.Request) (string, bool) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return "", false
	}
	tok := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	return tok, tok != ""
}

// accessSessionIssued is when r's access session cookie was issued, if
// it carries one the current code signed and it has not expired.
func accessSessionIssued(r *http.Request, cfg IAPConfig) (time.Time, bool) {
	c, err := r.Cookie(AccessCookieName)
	if err != nil || c == nil {
		return time.Time{}, false
	}
	return verifyAccessSession(c.Value, cfg.LocalAPIToken, accessLifetime(cfg), time.Now())
}

// singleTenantTrusted reports whether r came from somewhere this daemon
// trusts as much as it trusts itself, so that a request naming nobody
// runs as the deployment's own actor: a request carrying the access code, as a bearer token or an access
// session, from wherever it came; or one handed to Server in-process
// (no RemoteAddr). Loopback and a network address
// count for nothing here: every caller on the network,
// this host's own included, shows the code (grain/task-4343). A
// request carrying the code is judged by the code alone, so a proxy's
// forwarding or identity headers beside it neither help nor hinder.
func singleTenantTrusted(r *http.Request) bool {
	if externalTokenFromContext(r.Context()) != nil {
		return true
	}
	cfg, _ := IAPConfigFromContext(r.Context())
	if presentsAccessCode(r, cfg) {
		return true
	}
	if _, ok := accessSessionIssued(r, cfg); ok {
		return true
	}
	if relayedByProxy(r) {
		return false
	}
	remoteAddr := strings.TrimSpace(r.RemoteAddr)
	if remoteAddr == "" {
		return true
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	if ip := net.ParseIP(strings.TrimSpace(host)); ip != nil {
		for _, n := range testSourceNets {
			if n.Contains(ip) {
				return true
			}
		}
	}
	return false
}

// accessLimiter rate-limits wrong access codes, so that neither one
// client nor many can guess at speed, without letting anybody's wrong
// codes keep the owner out (grain/task-4430). Token buckets: each wrong
// code spends one, per source and across all sources, and they refill
// at a steady rate.
//
//   - Before a code is compared, only its own source's bucket is asked
//     (sourceLimited): a source over its limit is refused whether its
//     code is right or wrong, so the refusal says nothing about the
//     guess. A loopback caller is never asked (accessLimitExempt).
//   - The global bucket is asked and spent only for a code that has
//     already failed (failed). It never refuses a right code: a source
//     that sends a wrong one while it is spent has its own bucket
//     emptied for a minute (accessGlobalPenalty), so many sources
//     together are held to about one guess a minute each, and a source
//     that has sent no wrong code is never held at all.
type accessLimiter struct {
	mu      sync.Mutex
	sources map[string]*accessBucket
	global  accessBucket
}

type accessBucket struct {
	tokens float64
	last   time.Time
}

const (
	accessSourceBurst  = 10
	accessSourceRefill = 6 * time.Second // ten a minute
	accessGlobalBurst  = 100
	accessGlobalRefill = 600 * time.Millisecond // a hundred a minute
	// accessGlobalPenalty is where a source's bucket is put by a wrong
	// code it sends while the global bucket is spent: a minute's refill
	// below the one token its next attempt needs.
	accessGlobalPenalty = 1 - accessSourceBurst
	accessMaxSources    = 10000
)

func (b *accessBucket) level(now time.Time, burst float64, refill time.Duration) float64 {
	if b.last.IsZero() {
		b.tokens, b.last = burst, now
		return b.tokens
	}
	b.tokens += float64(now.Sub(b.last)) / float64(refill)
	if b.tokens > burst {
		b.tokens = burst
	}
	b.last = now
	return b.tokens
}

// sourceLimited reports whether source is over its own limit, and so is
// refused before its code is looked at. It does not ask the global
// bucket: what other sources have sent never refuses this one.
func (l *accessLimiter) sourceLimited(source string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.sources[source]
	if b == nil {
		return false
	}
	return b.level(now, accessSourceBurst, accessSourceRefill) < 1
}

// failed records a wrong code from source, and reports whether source
// is now over its limit, which is then what it is told.
func (l *accessLimiter) failed(source string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sources == nil {
		l.sources = map[string]*accessBucket{}
	}
	b := l.sources[source]
	if b == nil {
		if len(l.sources) >= accessMaxSources {
			// Forget the full ones: a bucket that has refilled says
			// nothing a fresh one would not.
			for k, v := range l.sources {
				if v.level(now, accessSourceBurst, accessSourceRefill) >= accessSourceBurst {
					delete(l.sources, k)
				}
			}
		}
		b = &accessBucket{}
		l.sources[source] = b
	}
	b.level(now, accessSourceBurst, accessSourceRefill)
	b.tokens--
	if l.global.level(now, accessGlobalBurst, accessGlobalRefill) < 1 && b.tokens > accessGlobalPenalty {
		b.tokens = accessGlobalPenalty
	}
	l.global.tokens--
	if l.global.tokens < -accessGlobalBurst {
		// Bounded, so the bucket refills within a minute or two of the
		// guessing stopping however long it went on.
		l.global.tokens = -accessGlobalBurst
	}
	return b.tokens < 1
}

// accessSource is the key r's wrong codes are counted under: its
// address -- an IPv6 one by its /64, which one host commonly holds the
// whole of.
func accessSource(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil && !ip.IsLoopback() {
		return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
	}
	return host
}

// accessLimitExempt reports whether r is never refused for its source's
// wrong codes: one straight from loopback, which is how the grain CLI
// and every run's `grain mcpserver` reach the daemon with the code from
// its file. All of this host's processes share that one source, so a
// limit on it would let any local account stall every run by sending
// wrong codes. Its wrong codes are still counted, and still answered
// 429 once it is over its limit; only a right one goes through. That
// does let a local process guess at whatever speed it likes, which the
// code's 160 random bits make hopeless, and which nothing could stop
// anyway from an account that can also make loopback connections
// faster than any limit allows. A request a proxy on loopback relayed
// is that proxy's callers, not this host's, and is limited like any
// remote source.
func accessLimitExempt(r *http.Request) bool {
	return isLoopbackRequest(r) && !relayedByProxy(r)
}

// couldBeAccessCode reports whether got could be code at all, and so
// counts as a guess at it when it is wrong. A bearer token of another
// length once case, dashes and whitespace are set aside, or with a
// character no access code has (the dots of a JWT, an executor token's
// "gxt_"), is somebody's other credential sent to the wrong place, not
// a guess: it can never match, so it is refused without being counted.
// An access code's length and alphabet are not secret: every generated
// code has the same ones.
func couldBeAccessCode(got, code string) bool {
	g, w := canonicalAccessCode(got), canonicalAccessCode(code)
	if g == "" || len(g) != len(w) {
		return false
	}
	for _, r := range g {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune(w, r) {
			continue
		}
		return false
	}
	return true
}

func setAccessCookie(w http.ResponseWriter, r *http.Request, code string, lifetime time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     AccessCookieName,
		Value:    accessSessionToken(code, time.Now()),
		Path:     "/",
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(lifetime / time.Second),
		Expires:  time.Now().Add(lifetime),
	})
}

func clearAccessCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     AccessCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

// The routes a single-tenant daemon serves to a caller without the code:
// the login page's own, logging out, and remote execution's
// (executorBootstrapRoute), which authenticates in its own protocol.
const (
	accessCodePath       = "/api/auth/access-code"
	accessCodeScriptPath = "/api/auth/access-code/login.js"
)

func singleTenantOpenRoute(r *http.Request) bool {
	switch r.URL.Path {
	case accessCodePath, accessCodeScriptPath, "/api/auth/logout":
		return true
	}
	return executorBootstrapRoute(r)
}

const errAccessCodeNeeded = "this daemon is single-tenant and every caller shows its access code: " +
	"sign in with it on the UI's \"Enter access code\" page, or send it as a bearer token " +
	"(`grain access-code` prints it, to the account grain runs as and root)"

const errAccessCodeLimited = "too many wrong access codes: wait a minute and try again"

// singleTenantGate is a single-tenant daemon's access gate, ahead of
// everything else serveHTTP does: a caller with the access code (or an
// in-process one, singleTenantTrusted) goes on, with its access session
// renewed when it is due; anybody else gets the "Enter access code" page
// or, for the API, a 401 -- never the app. It reports whether serveHTTP
// should go on.
func (s *Server) singleTenantGate(w http.ResponseWriter, r *http.Request, cfg IAPConfig) bool {
	if singleTenantOpenRoute(r) {
		return true
	}
	if tok, ok := bearerToken(r); ok && couldBeAccessCode(tok, cfg.LocalAPIToken) {
		// A source over its own limit is refused before its code is
		// looked at, so the refusal is the same whether the code was
		// right; a wrong one is counted. Loopback is never refused a
		// right one (accessLimitExempt).
		source := accessSource(r)
		if !accessLimitExempt(r) && s.access.sourceLimited(source, time.Now()) {
			writeError(w, http.StatusTooManyRequests, errors.New(errAccessCodeLimited))
			return false
		}
		if !presentsAccessCode(r, cfg) && s.access.failed(source, time.Now()) {
			writeError(w, http.StatusTooManyRequests, errors.New(errAccessCodeLimited))
			return false
		}
	}
	if singleTenantTrusted(r) {
		if issued, ok := accessSessionIssued(r, cfg); ok && time.Since(issued) > accessSessionRenewAfter {
			setAccessCookie(w, r, cfg.LocalAPIToken, accessLifetime(cfg))
		}
		return true
	}
	if c, err := r.Cookie(AccessCookieName); err == nil && c.Value != "" {
		// Expired, or signed with a code since rotated.
		clearAccessCookie(w, r)
	}
	if strings.HasPrefix(r.URL.Path, "/api/") || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		writeError(w, http.StatusUnauthorized, errors.New(errAccessCodeNeeded))
		return false
	}
	writeAccessCodePage(w, http.StatusUnauthorized, "")
	return false
}

// AccessCodeRequest is POST /api/auth/access-code's JSON body; a form
// post's "code" field says the same.
type AccessCodeRequest struct {
	Code string `json:"code"`
}

// handleAccessCodeStatus is GET /api/auth/access-code: whether this
// request carries a live access session, which the login page asks to
// find out whether it was shown only because the browser held back a
// SameSite=Strict cookie on a navigation from another site.
func (s *Server) handleAccessCodeStatus(w http.ResponseWriter, r *http.Request) {
	cfg, _ := IAPConfigFromContext(r.Context())
	_, ok := accessSessionIssued(r, cfg)
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": ok})
}

// handleAccessCodeLogin is POST /api/auth/access-code: the access code,
// as a form field or JSON, for an access session cookie. A form post
// (the login page's, which works without script) is redirected to the
// UI on success and shown the page again otherwise.
func (s *Server) handleAccessCodeLogin(w http.ResponseWriter, r *http.Request) {
	cfg, _ := IAPConfigFromContext(r.Context())
	isJSON := strings.HasPrefix(strings.TrimSpace(r.Header.Get("Content-Type")), "application/json")
	fail := func(status int, msg string) {
		if isJSON {
			writeError(w, status, errors.New(msg))
			return
		}
		writeAccessCodePage(w, status, msg)
	}
	source := accessSource(r)
	if !accessLimitExempt(r) && s.access.sourceLimited(source, time.Now()) {
		fail(http.StatusTooManyRequests, errAccessCodeLimited)
		return
	}
	var code string
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if isJSON {
		var req AccessCodeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			fail(http.StatusBadRequest, "the request body is not an access code")
			return
		}
		code = req.Code
	} else {
		if err := r.ParseForm(); err != nil {
			fail(http.StatusBadRequest, "the request body is not an access code")
			return
		}
		code = r.PostForm.Get("code")
	}
	if !matchesAccessCode(code, cfg.LocalAPIToken) {
		if couldBeAccessCode(code, cfg.LocalAPIToken) && s.access.failed(source, time.Now()) {
			fail(http.StatusTooManyRequests, errAccessCodeLimited)
			return
		}
		fail(http.StatusUnauthorized, "That access code was not accepted.")
		return
	}
	setAccessCookie(w, r, cfg.LocalAPIToken, accessLifetime(cfg))
	if isJSON {
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": true})
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// accessCodeScript is the login page's one script, served from
// accessCodeScriptPath because the CSP admits no inline script. A
// SameSite=Strict cookie is not sent on a navigation another site
// started -- the corp proxy's sign-in redirect back to the UI is one --
// so a browser holding a live session can land on this page; asking
// from here is a same-site request, which does carry it, and loading
// the page again from here is a same-site navigation.
const accessCodeScript = `fetch("/api/auth/access-code", {credentials: "same-origin"})
  .then(function (r) { return r.ok ? r.json() : {}; })
  .then(function (s) { if (s && s.authenticated) { window.location.replace(window.location.href); } })
  .catch(function () {});
`

func (s *Server) handleAccessCodeScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, accessCodeScript)
}

var accessCodePage = template.Must(template.New("access").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>grain: enter access code</title>
<script src="` + accessCodeScriptPath + `" defer></script>
<style>
body { font-family: system-ui, sans-serif; display: flex; min-height: 100vh; margin: 0; align-items: center; justify-content: center; background: #f5f5f5; }
form { background: #fff; padding: 2rem; border-radius: 8px; box-shadow: 0 1px 4px rgba(0,0,0,.15); max-width: 26rem; width: 100%; }
h1 { font-size: 1.25rem; margin: 0 0 1rem; }
input { width: 100%; box-sizing: border-box; font: 1rem monospace; padding: .5rem; margin: .5rem 0 1rem; }
button { font-size: 1rem; padding: .5rem 1rem; }
.error { color: #b00020; }
p { color: #444; font-size: .9rem; }
</style>
</head>
<body>
<form method="post" action="` + accessCodePath + `">
<h1>Enter access code</h1>
{{if .}}<p class="error" role="alert">{{.}}</p>{{end}}
<label for="code">Access code</label>
<input id="code" name="code" type="password" autocomplete="current-password" autofocus required>
<button type="submit">Continue</button>
<p>Run <code>grain access-code</code> on the machine grain is installed on to see it.</p>
</form>
</body>
</html>
`))

func writeAccessCodePage(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := accessCodePage.Execute(w, msg); err != nil {
		fmt.Fprintln(w, "enter access code")
	}
}
