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
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
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
	// AccessCodeHeader carries the access code when Authorization is
	// already occupied by an upstream IAP or OIDC proxy's bearer token.
	AccessCodeHeader = "X-Grain-Access-Code"
	// AccessNameHeader names who is presenting the access code as a
	// bearer token or in AccessCodeHeader -- the host CLI sends
	// $GRAIN_OWNER, else its local account -- so the request is recorded
	// as access-code:<name> rather than as nobody. It counts only beside
	// the code: anybody who holds the code is the owner already, so the
	// name attributes, it does not authorize.
	AccessNameHeader = "X-Grain-Access-Name"
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
// at issued that names nobody: "v1.<unix seconds>.<HMAC>", the HMAC keyed
// by the code. Only tests and sessions signed before names existed carry
// one; a sign-in now names its person (accessSessionTokenFor).
func accessSessionToken(code string, issued time.Time) string {
	return accessSessionTokenFor(code, "", issued)
}

// accessSessionTokenFor is the session cookie's value for name's session
// issued at issued: "v2.<unix seconds>.<name, base64url>.<HMAC>", the HMAC
// over everything before it, so the name is as unforgeable as the time.
// An empty name is a v1 token.
func accessSessionTokenFor(code, name string, issued time.Time) string {
	payload := "v1." + strconv.FormatInt(issued.Unix(), 10)
	if name != "" {
		payload = "v2." + strconv.FormatInt(issued.Unix(), 10) + "." + base64.RawURLEncoding.EncodeToString([]byte(name))
	}
	return payload + "." + base64.RawURLEncoding.EncodeToString(accessSessionMAC(code, payload))
}

// accessSession is a verified access session cookie: when it was issued
// and who signed in with it, "" for a v1 session that names nobody.
type accessSession struct {
	Issued time.Time
	Name   string
}

// verifyAccessSession reports whether token is an access session the
// current code signed, issued no longer than lifetime ago, and when and
// for whom.
func verifyAccessSession(token, code string, lifetime time.Duration, now time.Time) (accessSession, bool) {
	if canonicalAccessCode(code) == "" || token == "" {
		return accessSession{}, false
	}
	i := strings.LastIndexByte(token, '.')
	if i < 0 {
		return accessSession{}, false
	}
	payload, sig := token[:i], token[i+1:]
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, accessSessionMAC(code, payload)) {
		return accessSession{}, false
	}
	var secs, name string
	if rest, ok := strings.CutPrefix(payload, "v1."); ok {
		secs = rest
	} else if rest, ok := strings.CutPrefix(payload, "v2."); ok {
		var enc string
		secs, enc, ok = strings.Cut(rest, ".")
		if !ok {
			return accessSession{}, false
		}
		raw, err := base64.RawURLEncoding.DecodeString(enc)
		if err != nil {
			return accessSession{}, false
		}
		if name, ok = cleanAccessName(string(raw)); !ok || name == "" {
			return accessSession{}, false
		}
	} else {
		return accessSession{}, false
	}
	n, err := strconv.ParseInt(secs, 10, 64)
	if err != nil {
		return accessSession{}, false
	}
	issued := time.Unix(n, 0)
	if lifetime <= 0 {
		lifetime = DefaultAccessSessionLifetime
	}
	if issued.After(now.Add(5*time.Minute)) || now.Sub(issued) > lifetime {
		return accessSession{}, false
	}
	return accessSession{Issued: issued, Name: name}, true
}

// maxAccessNameLen is the longest name an access-code session may carry.
const maxAccessNameLen = 128

// cleanAccessName is name as an access-code session records it: trimmed,
// and refused (false) if it is longer than maxAccessNameLen or holds
// anything but letters, digits and "@._+-" -- an email address or a
// "user@host" account, never something that could pass for another
// principal's syntax or break a log line. "" is no name, and fine.
func cleanAccessName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if len(name) > maxAccessNameLen {
		return "", false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("@._+-", r):
		default:
			return "", false
		}
	}
	return name, true
}

// accessLifetime is cfg's access session lifetime, defaulted.
func accessLifetime(cfg IAPConfig) time.Duration {
	if cfg.AccessSessionLifetime > 0 {
		return cfg.AccessSessionLifetime
	}
	return DefaultAccessSessionLifetime
}

// presentsAccessCode reports whether r carries the access code as a
// bearer token or in AccessCodeHeader (so an upstream IAP/OIDC proxy can
// keep Authorization). Unlike presentsLocalAPIToken it does not ask where
// the request came from: on a single-tenant daemon the code is the
// credential wherever it is presented from.
func presentsAccessCode(r *http.Request, cfg IAPConfig) bool {
	if !accessCodeAccepted(r, cfg) {
		return false
	}
	if got, ok := bearerToken(r); ok && matchesAccessCode(got, cfg.LocalAPIToken) {
		return true
	}
	if got, ok := accessCodeHeader(r); ok && matchesAccessCode(got, cfg.LocalAPIToken) {
		return true
	}
	return false
}

// requiredVerifier returns the verifier name configured by
// -ui-require-verifier (cfg.RequireVerifier) or its -ui-require-iap alias
// ("iap" when cfg.RequireIAP is true), or "" when none is set.
func requiredVerifier(cfg IAPConfig) string {
	if v := strings.TrimSpace(cfg.RequireVerifier); v != "" {
		return v
	}
	if cfg.RequireIAP {
		return string(AuthMethodIAP)
	}
	return ""
}

// accessCodeAccepted reports whether r may authenticate with the access
// code at all. Without -ui-require-verifier (or its -ui-require-iap alias)
// it always may. With a required verifier, the code counts only for a
// request straight from loopback that no proxy relayed (relayedByProxy: no
// X-Forwarded-*, Forwarded or verifier header), which keeps loopback
// (`kubectl exec deploy/grain -- grain ...`, SSH tunnel) as a break-glass
// path that does not depend on the proxy; or for one a registered verifier
// decides that declares AccessCodeWins, whose proxy keeps the code as its
// own break-glass path.
func accessCodeAccepted(r *http.Request, cfg IAPConfig) bool {
	if requiredVerifier(cfg) == "" {
		return true
	}
	if isLoopbackRequest(r) && !relayedByProxy(r) {
		return true
	}
	cv := cachedOrVerifyCaller(r)
	return cv.present && cv.err == nil && cv.accessCodeWins
}

func bearerToken(r *http.Request) (string, bool) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return "", false
	}
	tok := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	return tok, tok != ""
}

func accessCodeHeader(r *http.Request) (string, bool) {
	tok := strings.TrimSpace(r.Header.Get(AccessCodeHeader))
	return tok, tok != ""
}

// accessCodeCandidate reports whether r presents a credential to be
// rate-limited and checked as an access code: either a Bearer token whose
// shape could be the code (leaving OIDC JWTs and gma_ tokens alone) or an
// explicit AccessCodeHeader.
func accessCodeCandidate(r *http.Request, code string) bool {
	if tok, ok := bearerToken(r); ok && couldBeAccessCode(tok, code) {
		return true
	}
	if tok, ok := accessCodeHeader(r); ok && (couldBeAccessCode(tok, code) || canonicalAccessCode(code) != "") {
		return true
	}
	return false
}

// accessSessionIssued is when r's access session cookie was issued, if
// it carries one the current code signed and it has not expired.
func accessSessionIssued(r *http.Request, cfg IAPConfig) (time.Time, bool) {
	sess, ok := accessSessionOf(r, cfg)
	return sess.Issued, ok
}

// accessSessionOf is r's access session, if it carries one the current
// code signed and it has not expired.
func accessSessionOf(r *http.Request, cfg IAPConfig) (accessSession, bool) {
	if !accessCodeAccepted(r, cfg) {
		return accessSession{}, false
	}
	c, err := r.Cookie(AccessCookieName)
	if err != nil || c == nil {
		return accessSession{}, false
	}
	return verifyAccessSession(c.Value, cfg.LocalAPIToken, accessLifetime(cfg), time.Now())
}

// accessCodeName is who r's access code is presented by: the name in
// AccessNameHeader beside a bearer or header code, or the one its session
// was signed in with; "" when it names nobody (or carries no code).
func accessCodeName(r *http.Request, cfg IAPConfig) string {
	if presentsAccessCode(r, cfg) {
		name, _ := cleanAccessName(r.Header.Get(AccessNameHeader))
		return name
	}
	if sess, ok := accessSessionOf(r, cfg); ok {
		return sess.Name
	}
	return ""
}

// singleTenantTrusted reports whether r came from somewhere this daemon
// trusts as much as it trusts itself, so that a request naming nobody
// runs as the deployment's own actor: a request carrying the access code, as a bearer token or an access
// session, from wherever it came; one a live run's own credential admitted
// (authenticateRunCredential); or one handed to Server in-process
// (no RemoteAddr). Loopback and a network address
// count for nothing here: every caller on the network,
// this host's own included, shows the code (grain/task-4343). A
// request carrying the code is judged by the code alone, so a proxy's
// forwarding or identity headers beside it neither help nor hinder.
func singleTenantTrusted(r *http.Request) bool {
	if externalTokenFromContext(r.Context()) != nil {
		return true
	}
	if _, ok := runCredentialFromContext(r.Context()); ok {
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
// character no access code has (the dots of a JWT, the underscore of a
// "gma_" token), is somebody's other credential sent to the wrong place, not
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

// setAccessCookie signs name in with the access code: a fresh session
// cookie naming them ("" for a renewed session that named nobody).
func setAccessCookie(w http.ResponseWriter, r *http.Request, code, name string, lifetime time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     AccessCookieName,
		Value:    accessSessionTokenFor(code, name, time.Now()),
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
// the login page's own, logging out, and a grain hostd dialing in
// (hostdConnectRoute), which authenticates in its own protocol, with the
// script that installs one (hostdInstallRoute), and the script that
// installs the grain CLI (cliInstallRoute).
const (
	accessCodePath       = "/api/auth/access-code"
	accessCodeScriptPath = "/api/auth/access-code/login.js"
)

func singleTenantOpenRoute(r *http.Request) bool {
	switch r.URL.Path {
	case accessCodePath, accessCodeScriptPath, "/api/auth/logout", "/api/auth/whoami":
		return true
	}
	return hostdConnectRoute(r) || hostdInstallRoute(r) || hostdEnrollOpenRoute(r) || cliInstallRoute(r)
}

const errAccessCodeNeeded = "this daemon is single-tenant and every caller shows its access code: " +
	"sign in with it on the UI's \"Enter access code\" page, or send it as a bearer token " +
	"(`grain access-code` prints it, to the account grain runs as and root)"

const errIAPRequired = "this daemon requires IAP (-ui-require-iap): the access code is not accepted over the network, " +
	"so sign in through the deployment's IAP URL; the code still works from the daemon's own host over loopback " +
	"(kubectl exec deploy/grain -- grain ...)"

func errVerifierRequired(verifier string) string {
	if verifier == "" || verifier == string(AuthMethodIAP) {
		return errIAPRequired
	}
	return fmt.Sprintf("this daemon requires %s (-ui-require-verifier=%s): the access code is not accepted over the network, "+
		"so sign in through %s; the code still works from the daemon's own host over loopback", verifier, verifier, verifier)
}

// errAccessCodeBehindVerifier is POST /api/auth/access-code's refusal
// for a caller a verifier has already identified, whose verifier does not
// declare AccessCodePrecedence: the identity's role applies, not the code's.
func errAccessCodeBehindVerifier(cv callerVerification) string {
	return fmt.Sprintf("you are signed in as %s through %s, and that sign-in decides your role here (Settings -> Access): "+
		"the access code does not change it, so it was not used. Ask an operator for a wider role, "+
		"or use the code from the daemon's own host over loopback", cv.id.Email, cv.method)
}

const errAccessCodeLimited = "too many wrong access codes: wait a minute and try again"

// singleTenantGate is a single-tenant daemon's access gate, ahead of
// everything else serveHTTP does: a caller authenticated by an
// IdentityVerifier (IAP or a wrapper's proxy) and admitted by the allowlist,
// or a caller with the access code (or an in-process one, singleTenantTrusted),
// goes on; anybody else gets a 401/403 or, for the browser UI without an
// assertion, the "Enter access code" page. It reports whether serveHTTP
// should go on.
func (s *Server) singleTenantGate(w http.ResponseWriter, r *http.Request, cfg IAPConfig) bool {
	cv := cachedOrVerifyCaller(r)
	if cv.present {
		if cv.err != nil {
			writeAssertionRefused(w, r, cv.err)
			return false
		}
		if cv.accessCodeWins {
			if externalTokenFromContext(r.Context()) != nil {
				return true
			}
			if accessCodeCandidate(r, cfg.LocalAPIToken) {
				source := accessSource(r)
				if !accessLimitExempt(r) && s.access.sourceLimited(source, time.Now()) {
					writeError(w, http.StatusTooManyRequests, errors.New(errAccessCodeLimited))
					return false
				}
				if !presentsAccessCode(r, cfg) {
					if s.access.failed(source, time.Now()) {
						writeError(w, http.StatusTooManyRequests, errors.New(errAccessCodeLimited))
						return false
					}
					writeError(w, http.StatusUnauthorized, errors.New(errAccessCodeNeeded))
					return false
				}
				return true
			}
			if sess, ok := accessSessionOf(r, cfg); ok {
				if time.Since(sess.Issued) > accessSessionRenewAfter {
					setAccessCookie(w, r, cfg.LocalAPIToken, sess.Name, accessLifetime(cfg))
				}
				return true
			}
		}
		if singleTenantOpenRoute(r) {
			return true
		}
		if err := s.admitVerifiedPrincipal(r.Context(), cv.id); err != nil {
			var de *delegateNotAllowedError
			if errors.As(err, &de) {
				writeDelegateRefused(w, r, de)
				return false
			}
			if browserNavigation(r) {
				s.writePrincipalRefusedPage(w, r, cv, err)
				return false
			}
			writeError(w, http.StatusForbidden, err)
			return false
		}
		if !checkVerifiedIdentityCSRF(w, r) {
			return false
		}
		return true
	}

	if singleTenantOpenRoute(r) {
		return true
	}
	codeAccepted := accessCodeAccepted(r, cfg)
	if codeAccepted && accessCodeCandidate(r, cfg.LocalAPIToken) {
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
		if sess, ok := accessSessionOf(r, cfg); ok && time.Since(sess.Issued) > accessSessionRenewAfter {
			setAccessCookie(w, r, cfg.LocalAPIToken, sess.Name, accessLifetime(cfg))
		}
		return true
	}
	if c, err := r.Cookie(AccessCookieName); err == nil && c.Value != "" {
		// Expired, or signed with a code since rotated.
		clearAccessCookie(w, r)
	}
	if !codeAccepted {
		// No code form to offer: behind a required verifier a browser that
		// got here without an assertion bypassed the proxy, and signs in by
		// going through it.
		reqVerifier := requiredVerifier(cfg)
		if strings.HasPrefix(r.URL.Path, "/api/") || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			writeError(w, http.StatusUnauthorized, errors.New(errVerifierRequired(reqVerifier)))
			return false
		}
		writeVerifierRequiredPage(w, reqVerifier)
		return false
	}
	if strings.HasPrefix(r.URL.Path, "/api/") || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		writeError(w, http.StatusUnauthorized, errors.New(errAccessCodeNeeded))
		return false
	}
	// No return path from here: the page's script fills it in from the
	// address bar, so a refusal repeats nothing the request said.
	writeAccessCodePage(w, http.StatusUnauthorized, "", "")
	return false
}

// AccessCodeRequest is POST /api/auth/access-code's JSON body; a form
// post's "code" field says the same.
//
// Name is who is signing in -- an email address the allowlist admits
// when it names anybody (accessNameAllowed) -- and is what the session
// records every request it makes as (access-code:<name>). A caller a
// verifier has identified beside the code signs in as that identity
// instead.
type AccessCodeRequest struct {
	Code string `json:"code"`
	Name string `json:"name,omitempty"`
}

// handleAccessCodeStatus is GET /api/auth/access-code: whether this
// request carries a live access session, which the login page asks to
// find out whether it was shown only because the browser held back a
// SameSite=Strict cookie on a navigation from another site.
func (s *Server) handleAccessCodeStatus(w http.ResponseWriter, r *http.Request) {
	cfg, _ := IAPConfigFromContext(r.Context())
	_, ok := accessSessionIssued(r, cfg)
	resp := map[string]any{"authenticated": ok}
	if req := requiredVerifier(cfg); req != "" {
		// What `grain access-code` reads to say the code it prints works
		// over loopback only.
		resp["requireVerifier"] = req
		if req == string(AuthMethodIAP) {
			resp["requireIAP"] = true
		}
		resp["accessCodeAccepted"] = accessCodeAccepted(r, cfg)
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleAccessCodeLogin is POST /api/auth/access-code: the access code,
// as a form field or JSON, for an access session cookie. A form post
// (the login page's, which works without script) is redirected to the
// UI on success and shown the page again otherwise.
func (s *Server) handleAccessCodeLogin(w http.ResponseWriter, r *http.Request) {
	cfg, _ := IAPConfigFromContext(r.Context())
	if !accessCodeAccepted(r, cfg) {
		writeError(w, http.StatusForbidden, errors.New(errVerifierRequired(requiredVerifier(cfg))))
		return
	}
	mode := s.accessCodeModeNow()
	isJSON := strings.HasPrefix(strings.TrimSpace(r.Header.Get("Content-Type")), "application/json")
	fail := func(status int, msg string) {
		if isJSON {
			writeError(w, status, errors.New(msg))
			return
		}
		writeAccessCodePage(w, status, msg, r.PostForm.Get(accessReturnField))
	}
	if cv := cachedOrVerifyCaller(r); cv.present && cv.err == nil && !cv.accessCodeWins {
		// The verified identity, not the code, decides every request this
		// browser sends through the proxy (singleTenantGate), so a cookie
		// set here would be accepted and then never used
		// (docs/test-runs/2026-11-07-rbac-roles.md F1). Say so instead.
		// The code is not looked at, so this tells nobody whether it was
		// right.
		fail(http.StatusForbidden, errAccessCodeBehindVerifier(cv))
		return
	}
	source := accessSource(r)
	if !accessLimitExempt(r) && s.access.sourceLimited(source, time.Now()) {
		fail(http.StatusTooManyRequests, errAccessCodeLimited)
		return
	}
	var code, name string
	r.Body = http.MaxBytesReader(w, r.Body, 4096+3*maxAccessReturnPath)
	if isJSON {
		var req AccessCodeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			fail(http.StatusBadRequest, "the request body is not an access code")
			return
		}
		code, name = req.Code, req.Name
	} else {
		if err := r.ParseForm(); err != nil {
			fail(http.StatusBadRequest, "the request body is not an access code")
			return
		}
		code, name = r.PostForm.Get("code"), r.PostForm.Get("name")
	}
	if mode == AccessCodeOff {
		// Said before the code is compared, and the same whether it was
		// right, so this tells nobody anything about it.
		fail(http.StatusForbidden, errAccessCodeOff)
		return
	}
	verifiedName := false
	if cv := cachedOrVerifyCaller(r); cv.present && cv.err == nil && cv.id.Email != "" {
		// A verifier that lets the code win (AccessCodeWins) still says
		// who this is: the session is theirs, not whatever was typed --
		// even when the allowlist refuses them, which is what the code is
		// break-glass for.
		name, verifiedName = cv.id.Email, true
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if !matchesAccessCode(code, cfg.LocalAPIToken) {
		if couldBeAccessCode(code, cfg.LocalAPIToken) && s.access.failed(source, time.Now()) {
			fail(http.StatusTooManyRequests, errAccessCodeLimited)
			return
		}
		fail(http.StatusUnauthorized, "That access code was not accepted.")
		return
	}
	// The name is judged only once the code is right, so a wrong code is
	// told nothing about who is on the allowlist.
	clean, ok := cleanAccessName(name)
	if !ok {
		fail(http.StatusBadRequest, "that name is not an email address")
		return
	}
	if verifiedName && clean == "" {
		fail(http.StatusBadRequest, "that name is not an email address")
		return
	}
	if err := s.accessNameAllowed(r.Context(), clean); err != nil && !verifiedName {
		fail(http.StatusForbidden, err.Error())
		return
	}
	recovery := s.accessCodeRestricted(r.Context(), cfg)
	ctx := withAccessCodeName(withRequestSource(r.Context(), model.SourceAccessCode), clean)
	if s.tasks != nil {
		ctx = s.tasks.withOperatorActor(ctx)
	}
	s.recordAccessCodeUse(ctx, accessCodeUse{Name: clean, Source: accessSource(r), Method: r.Method, Path: r.URL.Path}, recovery)
	setAccessCookie(w, r, cfg.LocalAPIToken, clean, accessLifetime(cfg))
	if isJSON {
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": true})
		return
	}
	// Back to the page the browser was trying to open when it was shown
	// the login page, which the form carries as a hidden field; anything
	// but a path on this origin is "/".
	http.Redirect(w, r, localReturnPath(r.PostForm.Get(accessReturnField)), http.StatusSeeOther)
}

const (
	// accessReturnField is the login form's hidden field holding the
	// path and query the page was served for.
	accessReturnField = "next"
	// maxAccessReturnPath is the longest return path kept; a longer one
	// is "/".
	maxAccessReturnPath = 2048
)

// localReturnPath is next if it is a path and query on this origin, the
// only place a sign-in may send a browser back to, and "/" otherwise:
// next is whatever the form post carried, so it is no more trusted than
// a query parameter would be. A path must start with one "/", not "//"
// or "/\" (which a browser reads as another host), and has no
// backslash, control character, scheme, host or fragment anywhere.
// /api/ paths are "/" too: a browser signing in wants a page.
func localReturnPath(next string) string {
	if next == "" || len(next) > maxAccessReturnPath || next[0] != '/' {
		return "/"
	}
	if len(next) > 1 && (next[1] == '/' || next[1] == '\\') {
		return "/"
	}
	for i := 0; i < len(next); i++ {
		if c := next[i]; c < 0x20 || c == 0x7f || c == '\\' || c == '#' {
			return "/"
		}
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" || !strings.HasPrefix(u.Path, "/") {
		return "/"
	}
	if strings.HasPrefix(u.Path, "/api/") {
		return "/"
	}
	return next
}

// accessCodeScript is the login page's one script, served from
// accessCodeScriptPath because the CSP admits no inline script. A
// SameSite=Strict cookie is not sent on a navigation another site
// started -- the corp proxy's sign-in redirect back to the UI is one --
// so a browser holding a live session can land on this page; asking
// from here is a same-site request, which does carry it, and loading
// the page again from here is a same-site navigation.
//
// It also fills in the form's return path from the address bar, which
// is the deep link the browser was trying to open, unless the page
// already carries one (a wrong code shown it again).
const accessCodeScript = `(function () {
  var next = document.querySelector('form input[name="` + accessReturnField + `"]');
  if (next && !next.value && window.location.pathname.indexOf("/api/") !== 0) {
    next.value = window.location.pathname + window.location.search;
  }
})();
fetch("/api/auth/access-code", {credentials: "same-origin"})
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
{{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}
<input type="hidden" name="` + accessReturnField + `" value="{{.Next}}">
<label for="name">Your email</label>
<input id="name" name="name" type="email" autocomplete="username" autofocus required>
<label for="code">Access code</label>
<input id="code" name="code" type="password" autocomplete="current-password" required>
<button type="submit">Continue</button>
<p>Run <code>grain access-code</code> on the machine grain is installed on to see it.
What you do is recorded as you; use the email you are on the allowlist under.</p>
</form>
</body>
</html>
`))

// writeAccessCodePage serves the login page with msg as its error, if
// any, and next -- a return path a wrong code's form post carried -- as
// where a sign-in sends it back to, once localReturnPath has vetted it
// (the gate passes none; accessCodeScript fills it in).
func writeAccessCodePage(w http.ResponseWriter, status int, msg, next string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	page := struct{ Error, Next string }{msg, ""}
	if next != "" {
		page.Next = localReturnPath(next)
	}
	if err := accessCodePage.Execute(w, page); err != nil {
		fmt.Fprintln(w, "enter access code")
	}
}

var iapRequiredPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>grain: sign in through IAP</title>
</head>
<body>
<h1>Sign in through IAP</h1>
<p>This grain requires Identity-Aware Proxy and does not take an access code over the network.
Open it at the deployment's IAP URL to sign in with your Google account.</p>
</body>
</html>
`

var verifierRequiredPage = template.Must(template.New("verifier-required").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>grain: sign in through {{.Verifier}}</title>
</head>
<body>
<h1>Sign in through {{.Verifier}}</h1>
<p>This grain requires verified sign-in ({{.Verifier}}) and does not take an access code over the network.
Open it at the deployment's URL to sign in through {{.Verifier}}.</p>
</body>
</html>
`))

// writeVerifierRequiredPage is what a browser without an assertion gets from a
// -ui-require-verifier (or -ui-require-iap) daemon in place of the "Enter
// access code" page.
func writeVerifierRequiredPage(w http.ResponseWriter, verifier string) {
	if verifier == "" || verifier == string(AuthMethodIAP) {
		writeIAPRequiredPage(w)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	if err := verifierRequiredPage.Execute(w, struct{ Verifier string }{Verifier: verifier}); err != nil {
		fmt.Fprintln(w, "sign in through required verifier")
	}
}

// writeIAPRequiredPage is what a browser without an assertion gets from a
// -ui-require-iap daemon in place of the "Enter access code" page.
func writeIAPRequiredPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	io.WriteString(w, iapRequiredPage)
}
