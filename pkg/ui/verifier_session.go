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
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// SessionBinder may be implemented by an IdentityVerifier whose Verify is
// too slow to run on every request (one that asks a remote service about
// each signed header, say). SessionTTL > 0 opts in: after Verify admits an
// identity, core sets an HMAC-signed, HttpOnly, Secure, SameSite=Strict,
// host-only cookie (VerifierSessionCookieName) binding the verifier's
// name, the identity, the request's Host and an expiry SessionTTL away
// (held to MaxVerifierSessionTTL). A later request that carries both the
// verifier's header and that cookie is admitted without Verify when the
// verifier also implements LocalVerifier and VerifyLocal finds the header
// well-formed for the same principal; any other request is verified in
// full, as it would be without a session, and every full verification
// re-issues the cookie.
//
// The cookie only ever stands in for Verify: allowlist admission, roles
// and CSRF checks run on every request whatever admitted its identity.
type SessionBinder interface {
	SessionTTL() time.Duration
}

// LocalVerifier is the check a SessionBinder's header has to pass, on a
// request that carries a live session, before core admits it on that
// session: whatever the verifier can check in-process -- the header's
// shape, its host, its timestamp window, its email -- without the remote
// call Verify makes. ok=false or an error sends the request to Verify,
// which decides it as it would have without a session; VerifyLocal never
// refuses a request by itself.
type LocalVerifier interface {
	VerifyLocal(r *http.Request) (id VerifiedIdentity, ok bool, err error)
}

const (
	// VerifierSessionCookieName is the cookie a SessionBinder's session
	// rides in. The __Host- prefix has the browser keep it host-only,
	// Secure and Path=/ whatever a response says.
	VerifierSessionCookieName = "__Host-grain_verified"
	// MaxVerifierSessionTTL caps SessionTTL. It matches the replay
	// window of the signed headers this is for, so a sniffed cookie is
	// worth no more than a replayed header.
	MaxVerifierSessionTTL = 5 * time.Minute
	// VerifierSessionGrace is how long past its expiry a session is
	// still reported to its verifier (VerifierSessionFromRequest), for
	// admitting its identity while the verifier's backend is down.
	VerifierSessionGrace = 5 * time.Minute
)

// VerifierSession is a session core issued a SessionBinder's identity.
type VerifierSession struct {
	Verifier string // the verifier's Name()
	Identity VerifiedIdentity
	Host     string // the request Host it was issued on, lowercased
	Expires  time.Time
}

// Live reports whether s has not yet expired at now.
func (s VerifierSession) Live(now time.Time) bool {
	return now.Before(s.Expires)
}

type verifierSessionContextKey struct{}

// VerifierSessionFromRequest returns the session r carried for the
// verifier whose Verify is running, when it is signed by this daemon,
// was issued by that verifier on r's Host, and is at most
// VerifierSessionGrace past its expiry. A verifier whose backend cannot
// be reached (its error would be a VerifierUnavailableError) may admit
// the identity a live-or-grace session names instead of failing -- once
// its own local checks find r's header names the same principal -- by
// returning that identity with SessionGrace set; core then admits it
// without re-issuing the cookie, so a grace period cannot extend itself.
// A verifier must never use a session to override a refusal.
func VerifierSessionFromRequest(r *http.Request) (VerifierSession, bool) {
	if r == nil {
		return VerifierSession{}, false
	}
	s, ok := r.Context().Value(verifierSessionContextKey{}).(VerifierSession)
	return s, ok
}

// verifierSessionKey is the HMAC key sessions are signed with: random per
// daemon process and never written anywhere, so a restart ends every
// session (each lasts at most MaxVerifierSessionTTL anyway).
var verifierSessionKey = sync.OnceValue(func() []byte {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic("ui: generating the verifier session key: " + err.Error())
	}
	return k
})

// verifierSessionNow is the clock sessions are issued and checked on.
var verifierSessionNow = time.Now

type verifierSessionPayload struct {
	Verifier string `json:"v"`
	Email    string `json:"e"`
	Sub      string `json:"s,omitempty"`
	Kind     string `json:"k"`
	Host     string `json:"h"`
	Expires  int64  `json:"x"`
}

const verifierSessionVersion = "v1"

func signVerifierSession(key []byte, p verifierSessionPayload) string {
	raw, _ := json.Marshal(p)
	body := verifierSessionVersion + "." + base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func parseVerifierSession(key []byte, token string) (verifierSessionPayload, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != verifierSessionVersion {
		return verifierSessionPayload{}, errors.New("malformed session")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return verifierSessionPayload{}, errors.New("malformed session signature")
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return verifierSessionPayload{}, errors.New("session signature does not verify")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return verifierSessionPayload{}, errors.New("malformed session payload")
	}
	var p verifierSessionPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return verifierSessionPayload{}, fmt.Errorf("malformed session payload: %w", err)
	}
	return p, nil
}

func sessionHost(r *http.Request) string {
	return strings.ToLower(strings.TrimSpace(r.Host))
}

// verifierSessionTTL is v's session lifetime, 0 when v does not bind
// sessions.
func verifierSessionTTL(v IdentityVerifier) time.Duration {
	b, ok := v.(SessionBinder)
	if !ok {
		return 0
	}
	ttl := b.SessionTTL()
	if ttl <= 0 {
		return 0
	}
	return min(ttl, MaxVerifierSessionTTL)
}

// requestVerifierSession is the session r's cookie carries for v on r's
// Host, signed by this daemon and no more than VerifierSessionGrace past
// its expiry.
func requestVerifierSession(r *http.Request, v IdentityVerifier) (VerifierSession, bool) {
	c, err := r.Cookie(VerifierSessionCookieName)
	if err != nil || c.Value == "" {
		return VerifierSession{}, false
	}
	p, err := parseVerifierSession(verifierSessionKey(), c.Value)
	if err != nil {
		return VerifierSession{}, false
	}
	if p.Verifier != v.Name() || p.Host == "" || p.Host != sessionHost(r) || p.Email == "" {
		return VerifierSession{}, false
	}
	s := VerifierSession{
		Verifier: p.Verifier,
		Identity: VerifiedIdentity{Email: p.Email, Sub: p.Sub, Kind: model.PrincipalKind(p.Kind)},
		Host:     p.Host,
		Expires:  time.Unix(p.Expires, 0),
	}
	if !verifierSessionNow().Before(s.Expires.Add(VerifierSessionGrace)) {
		return VerifierSession{}, false
	}
	return s, true
}

// sameSessionPrincipal reports whether id, as a verifier returned it,
// names the principal s was issued to.
func sameSessionPrincipal(s VerifierSession, id VerifiedIdentity) bool {
	if id.Via != nil {
		return false
	}
	email := normalizeIdentityEmail(id.Email)
	if email == "" || email != s.Identity.Email {
		return false
	}
	kind := id.Kind
	if kind == "" {
		kind = principalKindForEmail(email)
	}
	if kind != s.Identity.Kind {
		return false
	}
	return id.Sub == "" || id.Sub == s.Identity.Sub
}

// verifyWithSession decides r for a verifier whose header it carries,
// through its session when it has one: a live session whose header
// VerifyLocal accepts for the same principal is admitted without Verify;
// anything else goes to Verify, which sees the session (live or within
// its grace) through VerifierSessionFromRequest.
func verifyWithSession(r *http.Request, v IdentityVerifier, header string) (callerVerification, bool) {
	ttl := verifierSessionTTL(v)
	if ttl == 0 {
		return verifyWithVerifier(r, v, header)
	}
	sess, hasSess := requestVerifierSession(r, v)
	if hasSess && sess.Live(verifierSessionNow()) {
		if lv, ok := v.(LocalVerifier); ok {
			id, ok, err := lv.VerifyLocal(r)
			if err == nil && ok && sameSessionPrincipal(sess, id) {
				return callerVerification{
					present:        true,
					verifier:       v,
					id:             sess.Identity,
					method:         AuthMethod(v.Name()),
					accessCodeWins: verifierAccessCodeWins(v),
				}, true
			}
		}
	}
	vr := r
	if hasSess {
		vr = r.WithContext(context.WithValue(r.Context(), verifierSessionContextKey{}, sess))
	}
	cv, matched := verifyWithVerifier(vr, v, header)
	if !matched || cv.err != nil {
		return cv, matched
	}
	if cv.id.SessionGrace {
		// verifyWithVerifier held it to the session; never extend it.
		cv.id = sess.Identity
		return cv, true
	}
	if cv.id.Via == nil {
		cv.sessionTTL = ttl
	}
	return cv, true
}

// issueVerifierSession sets the session cookie for a request cv admitted
// through a full Verify of a SessionBinder.
func issueVerifierSession(w http.ResponseWriter, r *http.Request, cv callerVerification) {
	if cv.sessionTTL <= 0 || cv.verifier == nil || cv.err != nil || cv.id.Email == "" || sessionHost(r) == "" {
		return
	}
	expires := verifierSessionNow().Add(cv.sessionTTL)
	http.SetCookie(w, &http.Cookie{
		Name: VerifierSessionCookieName,
		Value: signVerifierSession(verifierSessionKey(), verifierSessionPayload{
			Verifier: cv.verifier.Name(),
			Email:    cv.id.Email,
			Sub:      cv.id.Sub,
			Kind:     string(cv.id.Kind),
			Host:     sessionHost(r),
			Expires:  expires.Unix(),
		}),
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		// Outlive the session by its grace, so the verifier can still
		// see it through an outage (VerifierSessionFromRequest).
		MaxAge: int((cv.sessionTTL + VerifierSessionGrace) / time.Second),
	})
}

func clearVerifierSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     VerifierSessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}
