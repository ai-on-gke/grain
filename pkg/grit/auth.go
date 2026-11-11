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

// Package grit implements grit's standalone and embedded CI execution service
// (/v1 API, durable event stream, tree store, client authentication/scopes,
// and in-process grit.Local backend; see design/design/grit-split.design.md).
package grit

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/gritapi"
)

// ClientScope binds a permission scope (`submit`, `read`, `cancel`,
// `cache-admin`, `release`) to a repository pattern (`*`, `owner/*`, or
// `owner/repo`).
type ClientScope struct {
	Scope gritapi.Scope `json:"scope"`
	Repo  string        `json:"repo"`
}

// ClientRecord is the stored metadata for one grit API client.
type ClientRecord struct {
	ID                       string        `json:"id"`
	SecretSHA256             string        `json:"-"`
	Scopes                   []ClientScope `json:"scopes"`
	MaxConcurrentJobs        int           `json:"maxConcurrentJobs,omitempty"`
	RunsPerMinute            int           `json:"runsPerMinute,omitempty"`
	TreeUploadBytesPerMinute int64         `json:"treeUploadBytesPerMinute,omitempty"`
	Revoked                  bool          `json:"revoked"`
	CreatedAt                time.Time     `json:"createdAt"`
}

// HasScope reports whether c holds wantScope for repo. For non-repository
// operations (such as `cache-admin` on `/pkg/v1/*`), pass repo == "*" or "".
func (c ClientRecord) HasScope(wantScope gritapi.Scope, repo string) bool {
	if c.Revoked {
		return false
	}
	for _, sc := range c.Scopes {
		if sc.Scope != wantScope {
			continue
		}
		if MatchRepoPattern(sc.Repo, repo) {
			return true
		}
	}
	return false
}

// MatchRepoPattern reports whether pattern (`*`, `owner/*`, or `owner/repo`)
// matches repo.
func MatchRepoPattern(pattern, repo string) bool {
	pattern = strings.TrimSpace(pattern)
	repo = strings.TrimSpace(repo)
	if pattern == "*" {
		return true
	}
	if repo == "" || repo == "*" {
		return false
	}
	if prefix, ok := strings.CutSuffix(pattern, "/*"); ok {
		owner, _, hasSlash := strings.Cut(repo, "/")
		return hasSlash && strings.EqualFold(owner, prefix)
	}
	return strings.EqualFold(pattern, repo)
}

// GenerateClientToken generates a new random `<id>.<secret>` token for clientID
// and returns both the full token and the hex SHA-256 of `<secret>`.
func GenerateClientToken(clientID string) (token string, secretSHA256 string, err error) {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" || strings.ContainsAny(clientID, ". \t\r\n") {
		return "", "", fmt.Errorf("grit: invalid client id %q", clientID)
	}
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", err
	}
	secret := hex.EncodeToString(raw[:])
	return clientID + "." + secret, HashSecret(secret), nil
}

// HashSecret returns the lowercase hex SHA-256 of secret.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// ParseClientToken splits `<id>.<secret>` into `(id, secret, true)`.
func ParseClientToken(raw string) (id, secret string, ok bool) {
	raw = strings.TrimSpace(raw)
	id, secret, ok = strings.Cut(raw, ".")
	if !ok || id == "" || secret == "" {
		return "", "", false
	}
	return id, secret, true
}

// ExtractClientToken reads `<id>.<secret>` from `Grit-Client` (preferred) or
// `Authorization: Bearer <id>.<secret>`.
func ExtractClientToken(r *http.Request) (id, secret string, ok bool) {
	if hdr := strings.TrimSpace(r.Header.Get(gritapi.ClientHeader)); hdr != "" {
		return ParseClientToken(hdr)
	}
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(auth, "Bearer ") {
		return ParseClientToken(strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")))
	}
	return "", "", false
}

// VerifySecret compares secret against wantHexSHA256 in constant time.
func VerifySecret(secret, wantHexSHA256 string) bool {
	got := HashSecret(secret)
	if len(got) != len(wantHexSHA256) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(wantHexSHA256)) == 1
}

// ParseScopeSpec parses a CLI scope specification such as `submit:owner/*`,
// `read:*`, or `cache-admin` (which defaults to `*`).
func ParseScopeSpec(spec string) (ClientScope, error) {
	spec = strings.TrimSpace(spec)
	scopeStr, repo, hasColon := strings.Cut(spec, ":")
	sc := gritapi.Scope(strings.TrimSpace(scopeStr))
	if !gritapi.ValidScope(sc) {
		return ClientScope{}, fmt.Errorf("unknown scope %q (valid: submit, read, cancel, cache-admin, release)", scopeStr)
	}
	if !hasColon || strings.TrimSpace(repo) == "" {
		repo = "*"
	} else {
		repo = strings.TrimSpace(repo)
	}
	return ClientScope{Scope: sc, Repo: repo}, nil
}

// clientRateLimiter enforces per-client sliding-window rate limits on run
// submissions and tree upload bytes: a request counts against everything the
// client did in the minute before it, not against a wall-clock minute, so a
// burst straddling a minute boundary cannot get twice the limit through.
type clientRateLimiter struct {
	mu      sync.Mutex
	windows map[string]*clientWindow
}

type windowEvent struct {
	at time.Time
	n  int64
}

type clientWindow struct {
	runs    []windowEvent
	uploads []windowEvent
}

const rateWindow = time.Minute

func newClientRateLimiter() *clientRateLimiter {
	return &clientRateLimiter{windows: make(map[string]*clientWindow)}
}

func (l *clientRateLimiter) window(clientID string) *clientWindow {
	w, ok := l.windows[clientID]
	if !ok {
		w = &clientWindow{}
		l.windows[clientID] = w
	}
	return w
}

// prune drops events older than the window before now and returns the sum of
// what is left.
func prune(events []windowEvent, now time.Time) ([]windowEvent, int64) {
	cutoff := now.Add(-rateWindow)
	i := 0
	for i < len(events) && !events[i].at.After(cutoff) {
		i++
	}
	events = events[i:]
	var total int64
	for _, e := range events {
		total += e.n
	}
	return events, total
}

func (l *clientRateLimiter) allowRun(c ClientRecord, now time.Time) bool {
	if c.RunsPerMinute <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.window(c.ID)
	var runs int64
	w.runs, runs = prune(w.runs, now)
	if runs >= int64(c.RunsPerMinute) {
		return false
	}
	w.runs = append(w.runs, windowEvent{at: now, n: 1})
	return true
}

func (l *clientRateLimiter) allowTreeUpload(c ClientRecord, nBytes int64, now time.Time) bool {
	if c.TreeUploadBytesPerMinute <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.window(c.ID)
	var uploaded int64
	w.uploads, uploaded = prune(w.uploads, now)
	if uploaded+nBytes > c.TreeUploadBytesPerMinute {
		return false
	}
	// An empty upload counts for nothing, so it is not kept either: a
	// client sending them as fast as it can would otherwise grow the
	// window by one event a request.
	if nBytes > 0 {
		w.uploads = append(w.uploads, windowEvent{at: now, n: nBytes})
	}
	return true
}
