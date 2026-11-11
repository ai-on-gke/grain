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

package intree

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/gcpsets"
)

// A permission set over an existing service account with credential:
// token (gcpsets.PermissionSet.TokenMode) places no key. Its grant places
// a workload-identity (external_account) config whose token exchange is
// this server's data-plane listener, forwarded into the sandbox (§5.3):
// gcloud, ADC and the client libraries post the grant's per-attempt token
// to /runs/<attempt_id>/token whenever they need an access token, and the
// server answers with a fresh one it mints by impersonating the account
// (generateAccessToken), living until the grant's deadline and at most an
// hour. Nothing in the sandbox outlives the attempt: the per-attempt token
// is good only on this listener, only until release or the deadline, and
// GCP never sees it.

// DefaultGCPTokenPort is the gcp server's data-plane port unless -listen
// says otherwise: beside the gchat server's 4251 (docs/ports.md).
const DefaultGCPTokenPort = 4252

// gcpTokenDir is where a token-mode grant's per-attempt files live.
func gcpTokenDir(attemptID string) string { return "/home/debian/.gcp-token/" + attemptID }

// mintedAccessToken records the SHA-256 digest and expiry of one access token
// minted for a token-mode attempt so /runs/<attempt_id>/introspect can answer
// gcloud's RFC 7662 token_info_url check locally without contacting STS.
type mintedAccessToken struct {
	digest  [32]byte
	expires time.Time
}

// gcpTokenAttempt is one token-mode grant: the set, its account, the
// per-attempt token the sandbox presents, and the grant's deadline.
type gcpTokenAttempt struct {
	set, account, secret string
	deadline             time.Time
	minted               []mintedAccessToken
}

// gcpTokens is the data-plane side of token-mode grants. Its attempts
// survive a reconfigure, so a running sandbox keeps refreshing; each
// token is minted under whatever configuration is current then.
type gcpTokens struct {
	mu        sync.Mutex
	attempts  map[string]*gcpTokenAttempt
	current   func() *gcpsets.Manager
	log       *log.Logger
	port      int
	listening bool
}

func newGCPTokens(current func() *gcpsets.Manager, logger *log.Logger) *gcpTokens {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &gcpTokens{attempts: map[string]*gcpTokenAttempt{}, current: current, log: logger}
}

// grant records attemptID's grant of set, keeping the per-attempt token a
// renewal of the same grant already placed: a renewal's placements are
// not written again, so the sandbox's copy must stay good.
func (t *gcpTokens) grant(attemptID, set, account string, deadline time.Time) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if a := t.attempts[attemptID]; a != nil && a.set == set && a.account == account {
		a.deadline = deadline
		return a.secret, nil
	}
	secret, err := randomToken()
	if err != nil {
		return "", err
	}
	t.attempts[attemptID] = &gcpTokenAttempt{set: set, account: account, secret: secret, deadline: deadline}
	return secret, nil
}

// release forgets attemptID: its per-attempt token is good for nothing
// from now on.
func (t *gcpTokens) release(attemptID string) {
	t.mu.Lock()
	delete(t.attempts, attemptID)
	t.mu.Unlock()
}

// lookup is attemptID's grant when presented is its per-attempt token.
func (t *gcpTokens) lookup(attemptID, presented string) (gcpTokenAttempt, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	a := t.attempts[attemptID]
	if a == nil || presented == "" || subtle.ConstantTimeCompare([]byte(a.secret), []byte(presented)) != 1 {
		return gcpTokenAttempt{}, false
	}
	return gcpTokenAttempt{set: a.set, account: a.account, secret: a.secret, deadline: a.deadline}, true
}

const maxTrackedMintedTokens = 64

// recordMinted remembers an access token minted for attemptID so local
// RFC 7662 introspection (token_info_url) recognizes it until release or expiry.
func (t *gcpTokens) recordMinted(attemptID, tok string, exp, now time.Time) {
	if tok == "" {
		return
	}
	sum := sha256.Sum256([]byte(tok))
	t.mu.Lock()
	defer t.mu.Unlock()
	a := t.attempts[attemptID]
	if a == nil {
		return
	}
	kept := a.minted[:0]
	for _, m := range a.minted {
		if now.Before(m.expires) {
			kept = append(kept, m)
		}
	}
	kept = append(kept, mintedAccessToken{digest: sum, expires: exp})
	if len(kept) > maxTrackedMintedTokens {
		kept = append([]mintedAccessToken(nil), kept[len(kept)-maxTrackedMintedTokens:]...)
	}
	a.minted = kept
}

// lookupMinted checks whether presented is an active, unexpired access token
// minted for attemptID.
func (t *gcpTokens) lookupMinted(attemptID, presented string, now time.Time) (account string, expires time.Time, ok bool) {
	if presented == "" {
		return "", time.Time{}, false
	}
	sum := sha256.Sum256([]byte(presented))
	t.mu.Lock()
	defer t.mu.Unlock()
	a := t.attempts[attemptID]
	if a == nil {
		return "", time.Time{}, false
	}
	for i := len(a.minted) - 1; i >= 0; i-- {
		m := a.minted[i]
		if subtle.ConstantTimeCompare(m.digest[:], sum[:]) == 1 {
			exp := m.expires
			if !a.deadline.IsZero() && a.deadline.Before(exp) {
				exp = a.deadline
			}
			if !now.Before(exp) {
				return "", time.Time{}, false
			}
			return a.account, exp, true
		}
	}
	return "", time.Time{}, false
}

// stsError is an OAuth error body (RFC 6749 §5.2), which the client
// libraries quote when a token exchange fails.
func stsError(w http.ResponseWriter, status int, code, desc string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": desc})
}

// ServeHTTP serves /runs/<attempt_id>/token, the token exchange the
// placed external_account config names (an RFC 8693 request whose
// subject_token is the per-attempt token), and /runs/<attempt_id>/id-token
// ?audience=<aud>, an ID token for the account under
// "Authorization: Bearer <per-attempt token>".
func (t *gcpTokens) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/runs/")
	attemptID, op, _ := strings.Cut(rest, "/")
	if !ok || !pathSafe(attemptID) {
		http.NotFound(w, r)
		return
	}
	switch op {
	case "token":
		if r.Method != http.MethodPost {
			stsError(w, http.StatusMethodNotAllowed, "invalid_request", "POST the token exchange")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if err := r.ParseForm(); err != nil {
			stsError(w, http.StatusBadRequest, "invalid_request", "the token exchange is not a form")
			return
		}
		a, ok := t.lookup(attemptID, r.PostForm.Get("subject_token"))
		if !ok {
			stsError(w, http.StatusUnauthorized, "invalid_grant", "this grant's token is unknown or released")
			return
		}
		t.accessToken(w, r, attemptID, a)
	case "id-token":
		bearer, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		a, ok := t.lookup(attemptID, bearer)
		if !ok {
			stsError(w, http.StatusUnauthorized, "invalid_grant", "this grant's token is unknown or released")
			return
		}
		aud := r.URL.Query().Get("audience")
		if aud == "" {
			stsError(w, http.StatusBadRequest, "invalid_request", "audience is required")
			return
		}
		t.idToken(w, r, attemptID, a, aud)
	case "introspect":
		if r.Method != http.MethodPost {
			stsError(w, http.StatusMethodNotAllowed, "invalid_request", "POST the token introspection")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if err := r.ParseForm(); err != nil {
			stsError(w, http.StatusBadRequest, "invalid_request", "the token introspection is not a form")
			return
		}
		m := t.manager(w)
		if m == nil {
			return
		}
		account, exp, ok := t.lookupMinted(attemptID, r.PostForm.Get("token"), m.Now())
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if !ok {
			// RFC 7662 §2.2: a token that is unknown, expired, released or
			// another attempt's is answered {"active": false}, not an
			// error, and the answer says nothing more about it.
			json.NewEncoder(w).Encode(map[string]any{"active": false})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"active":   true,
			"username": account,
			"exp":      exp.Unix(),
		})
	default:
		http.NotFound(w, r)
	}
}

func (t *gcpTokens) manager(w http.ResponseWriter) *gcpsets.Manager {
	m := t.current()
	if m == nil {
		stsError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "the gcp server is not configured")
	}
	return m
}

// failed answers a mint that did not happen: a refusal (a deadline past,
// a set no longer configured for tokens) is the grant's end; anything
// else may pass.
func (t *gcpTokens) failed(w http.ResponseWriter, attemptID string, a gcpTokenAttempt, what string, err error) {
	t.log.Printf("gcp: audit: attempt %s: permission set %s: %s for %s failed: %v", attemptID, a.set, what, a.account, err)
	var refusal *gcpsets.Refusal
	if errors.As(err, &refusal) {
		stsError(w, http.StatusUnauthorized, "invalid_grant", refusal.Reason)
		return
	}
	stsError(w, http.StatusBadGateway, "temporarily_unavailable", fmt.Sprintf("minting %s for %s failed: %v", what, a.account, err))
}

func (t *gcpTokens) accessToken(w http.ResponseWriter, r *http.Request, attemptID string, a gcpTokenAttempt) {
	m := t.manager(w)
	if m == nil {
		return
	}
	tok, exp, err := m.AccessToken(r.Context(), a.set, a.account, a.deadline)
	if err != nil {
		t.failed(w, attemptID, a, "an access token", err)
		return
	}
	t.recordMinted(attemptID, tok, exp, m.Now())
	t.log.Printf("gcp: audit: attempt %s: permission set %s: minted an access token for %s, expiring %s", attemptID, a.set, a.account, exp.UTC().Format(time.RFC3339))
	secs := max(int(exp.Sub(m.Now()).Seconds()), 1)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]any{
		"access_token":      tok,
		"issued_token_type": "urn:ietf:params:oauth:token-type:access_token",
		"token_type":        "Bearer",
		"expires_in":        secs,
	})
}

func (t *gcpTokens) idToken(w http.ResponseWriter, r *http.Request, attemptID string, a gcpTokenAttempt, aud string) {
	m := t.manager(w)
	if m == nil {
		return
	}
	tok, err := m.IDToken(r.Context(), a.set, a.account, aud, a.deadline)
	if err != nil {
		t.failed(w, attemptID, a, "an ID token", err)
		return
	}
	t.log.Printf("gcp: audit: attempt %s: permission set %s: minted an ID token for %s, audience %q", attemptID, a.set, a.account, aud)
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, tok)
}

// gcpExternalAccount is the external_account config a token-mode grant
// places where a key would go: its subject token is the per-attempt token
// in subjectPath, exchanged and introspected at this server's forwarded listener.
// Setting token_info_url keeps gcloud's credential_file_override loader
// (GetExternalAccountId) on the local listener instead of posting the
// impersonated service-account access token to https://sts.googleapis.com/v1/introspect,
// which fails in closed-network sandboxes and returns {"active": false} for
// IAM-minted tokens on open networks.
func gcpExternalAccount(port int, attemptID, set, subjectPath string) string {
	base := "http://127.0.0.1:" + strconv.Itoa(port) + "/runs/" + attemptID
	b, _ := json.MarshalIndent(map[string]any{
		"type":               "external_account",
		"audience":           "//grain.dev/capabilities/gcp/permission-sets/" + set,
		"subject_token_type": "urn:ietf:params:oauth:token-type:jwt",
		"token_url":          base + "/token",
		"token_info_url":     base + "/introspect",
		"credential_source":  map[string]any{"file": subjectPath},
		"universe_domain":    "googleapis.com",
	}, "", "  ")
	return string(b) + "\n"
}

// gcpIDTokenScript prints an ID token for the grant's account whose
// audience is its one argument.
func gcpIDTokenScript(port int, attemptID, subjectPath string) string {
	return "#!/bin/sh\n# usage: " + "id-token AUDIENCE -- prints an ID token for this grant's service account\nset -eu\n" +
		"[ $# -eq 1 ] || { echo \"usage: $0 AUDIENCE\" >&2; exit 2; }\n" +
		"exec curl -fsS -G -H \"Authorization: Bearer $(cat " + shellQuote(subjectPath) + ")\" --data-urlencode \"audience=$1\" " +
		shellQuote("http://127.0.0.1:"+strconv.Itoa(port)+"/runs/"+attemptID+"/id-token") + "\n"
}

// ensureListen starts the data-plane listener the first time cfg has a
// token-mode set; it then serves every later configuration's.
func (t *gcpTokens) ensureListen(s *Server, cfg GCPConfig, c *gcpsets.Config) error {
	need := false
	for i := range c.Sets {
		if c.Sets[i].TokenMode() {
			need = true
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !need || t.listening {
		return nil
	}
	addr := cfg.Listen
	if addr == "" {
		addr = "127.0.0.1:" + strconv.Itoa(DefaultGCPTokenPort)
	}
	ln, err := s.listen(addr, t)
	if err != nil {
		return fmt.Errorf("gcp: token data plane: %w", err)
	}
	t.port, t.listening = ln.Port, true
	return nil
}

// ready reports whether the listener is up.
func (t *gcpTokens) ready() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.listening
}

// listenPort is the listener's port.
func (t *gcpTokens) listenPort() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.port
}
