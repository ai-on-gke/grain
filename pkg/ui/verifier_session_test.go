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

package ui_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ui"
)

const slowHeader = "X-Slow-Proxy-Sig"

type unavailableErr struct{}

func (unavailableErr) Error() string             { return "backend unreachable" }
func (unavailableErr) Unavailable() bool         { return true }
func (unavailableErr) UnavailableReason() string { return "backend_unreachable" }

// slowSessionVerifier models a verifier that has to ask a remote service
// about every header ("ok:<email>" verifies, "bad:..." does not), and
// binds sessions so it need not.
type slowSessionVerifier struct {
	ttl      time.Duration
	verifies atomic.Int32
	// down has Verify fail as unavailable, admitting on the request's
	// session when it names the header's principal.
	down atomic.Bool
	// lie has Verify claim SessionGrace whatever the request carries.
	lie atomic.Bool
}

func (v *slowSessionVerifier) Name() string              { return "slow-proxy" }
func (v *slowSessionVerifier) Headers() []string         { return []string{slowHeader} }
func (v *slowSessionVerifier) SessionTTL() time.Duration { return v.ttl }

func (v *slowSessionVerifier) VerifyLocal(r *http.Request) (ui.VerifiedIdentity, bool, error) {
	raw := strings.TrimSpace(r.Header.Get(slowHeader))
	if raw == "" {
		return ui.VerifiedIdentity{}, false, nil
	}
	if strings.HasPrefix(raw, "bad:") {
		return ui.VerifiedIdentity{}, false, errors.New("malformed header")
	}
	email, ok := strings.CutPrefix(raw, "ok:")
	if !ok {
		return ui.VerifiedIdentity{}, false, nil
	}
	return ui.VerifiedIdentity{Email: email}, true, nil
}

func (v *slowSessionVerifier) Verify(r *http.Request) (ui.VerifiedIdentity, bool, error) {
	raw := strings.TrimSpace(r.Header.Get(slowHeader))
	if raw == "" {
		return ui.VerifiedIdentity{}, false, nil
	}
	v.verifies.Add(1)
	if v.lie.Load() {
		return ui.VerifiedIdentity{Email: strings.TrimPrefix(raw, "ok:"), SessionGrace: true}, true, nil
	}
	if v.down.Load() {
		local, ok, err := v.VerifyLocal(r)
		if sess, has := ui.VerifierSessionFromRequest(r); has && err == nil && ok && sess.Identity.Email == local.Email {
			return ui.VerifiedIdentity{Email: local.Email, SessionGrace: true}, true, nil
		}
		return ui.VerifiedIdentity{}, false, unavailableErr{}
	}
	id, err := parseSimpleSignedHeader(r, raw)
	if err != nil {
		return ui.VerifiedIdentity{}, false, err
	}
	return id, true, nil
}

func sessionFixture(t *testing.T) (*ui.Server, *slowSessionVerifier, *time.Time) {
	t.Helper()
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	v := &slowSessionVerifier{ttl: time.Minute}
	t.Cleanup(ui.RegisterIdentityVerifier(v))
	now := time.Now()
	ui.SetVerifierSessionClockForTest(t, func() time.Time { return now })
	srv, client := testServer(t)
	client.Config.AllowedPrincipals = []string{"user:alice@example.com"}
	ui.SetAccessCodeForTest(srv, testAccessCode)
	return srv, v, &now
}

func sessionRequest(srv *ui.Server, host, header, cookie string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/auth/user", nil)
	req.RemoteAddr = "198.51.100.5:1234"
	if host != "" {
		req.Host = host
	}
	if header != "" {
		req.Header.Set(slowHeader, header)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: ui.VerifierSessionCookieName, Value: cookie})
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func issuedSession(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == ui.VerifierSessionCookieName && c.MaxAge > 0 {
			return c
		}
	}
	return nil
}

func wantUser(t *testing.T, rec *httptest.ResponseRecorder, email string) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 as %s: %s", rec.Code, email, rec.Body)
	}
	var got ui.AuthUserResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if !got.Authenticated || got.User != email || got.AuthMethod != "slow-proxy" {
		t.Fatalf("got %+v, want %s via slow-proxy", got, email)
	}
}

func TestVerifierSessionAdmitsWithoutVerify(t *testing.T) {
	srv, v, _ := sessionFixture(t)

	rec := sessionRequest(srv, "", "ok:alice@example.com", "")
	wantUser(t, rec, "alice@example.com")
	c := issuedSession(rec)
	if c == nil {
		t.Fatalf("full verification set no %s cookie: %v", ui.VerifierSessionCookieName, rec.Header().Values("Set-Cookie"))
	}
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Domain != "" || c.Path != "/" {
		t.Fatalf("session cookie %+v: want HttpOnly, Secure, SameSite=Strict, host-only, Path=/", c)
	}
	if c.MaxAge > int((ui.MaxVerifierSessionTTL+ui.VerifierSessionGrace)/time.Second) {
		t.Fatalf("session cookie MaxAge %d outlives TTL plus grace", c.MaxAge)
	}

	before := v.verifies.Load()
	rec = sessionRequest(srv, "", "ok:alice@example.com", c.Value)
	wantUser(t, rec, "alice@example.com")
	if n := v.verifies.Load() - before; n != 0 {
		t.Fatalf("request with a live session called Verify %d times, want 0", n)
	}
	if issuedSession(rec) != nil {
		t.Fatalf("admitting on the session re-issued it")
	}

	// The cookie without the verifier's header admits nobody.
	rec = sessionRequest(srv, "", "", c.Value)
	if rec.Code == http.StatusOK {
		var got ui.AuthUserResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if got.User != "" {
			t.Fatalf("session cookie alone admitted %+v", got)
		}
	}
}

func TestVerifierSessionIgnoredUnlessItMatches(t *testing.T) {
	srv, v, now := sessionFixture(t)
	exp := now.Add(time.Minute)
	for _, tc := range []struct {
		name, header, cookie, host string
		wantCode                   int
	}{
		{"forged", "ok:alice@example.com", ui.SignVerifierSessionForTest([]byte("not-the-daemon-key-0123456789abc"), "slow-proxy", "alice@example.com", "human", "example.com", exp), "", http.StatusOK},
		{"tampered", "ok:alice@example.com", ui.SignVerifierSessionForTest(nil, "slow-proxy", "alice@example.com", "human", "example.com", exp) + "x", "", http.StatusOK},
		{"expired", "ok:alice@example.com", ui.SignVerifierSessionForTest(nil, "slow-proxy", "alice@example.com", "human", "example.com", now.Add(-time.Second)), "", http.StatusOK},
		{"other host", "ok:alice@example.com", ui.SignVerifierSessionForTest(nil, "slow-proxy", "alice@example.com", "human", "other.example.com", exp), "", http.StatusOK},
		{"other verifier", "ok:alice@example.com", ui.SignVerifierSessionForTest(nil, "another-proxy", "alice@example.com", "human", "example.com", exp), "", http.StatusOK},
		// A session for alice does not make bob's header alice, nor admit bob.
		{"other principal", "ok:bob@example.com", ui.SignVerifierSessionForTest(nil, "slow-proxy", "alice@example.com", "human", "example.com", exp), "", http.StatusForbidden},
		// A session never overrides a refusal.
		{"bad header", "bad:corrupt", ui.SignVerifierSessionForTest(nil, "slow-proxy", "alice@example.com", "human", "example.com", exp), "", http.StatusUnauthorized},
		{"forged header", "forged", ui.SignVerifierSessionForTest(nil, "slow-proxy", "alice@example.com", "human", "example.com", exp), "", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := v.verifies.Load()
			rec := sessionRequest(srv, tc.host, tc.header, tc.cookie)
			if v.verifies.Load() == before {
				t.Fatalf("Verify was not called")
			}
			if rec.Code != tc.wantCode {
				t.Fatalf("got %d, want %d: %s", rec.Code, tc.wantCode, rec.Body)
			}
			if tc.wantCode == http.StatusOK {
				wantUser(t, rec, "alice@example.com")
				if issuedSession(rec) == nil {
					t.Fatalf("full verification did not re-issue the session")
				}
			} else if issuedSession(rec) != nil {
				t.Fatalf("refused request was issued a session")
			}
			if tc.name == "other principal" && !strings.Contains(rec.Body.String(), "bob@example.com") {
				t.Fatalf("other principal: want the refusal to name bob: %s", rec.Body)
			}
		})
	}

	t.Run("live session with the same host on another port", func(t *testing.T) {
		before := v.verifies.Load()
		cookie := ui.SignVerifierSessionForTest(nil, "slow-proxy", "alice@example.com", "human", "example.com", exp)
		sessionRequest(srv, "example.com:8443", "ok:alice@example.com", cookie)
		if v.verifies.Load() == before {
			t.Fatalf("session for example.com admitted example.com:8443 without Verify")
		}
	})
}

func TestVerifierSessionTTLIsCapped(t *testing.T) {
	srv, v, _ := sessionFixture(t)
	v.ttl = time.Hour
	c := issuedSession(sessionRequest(srv, "", "ok:alice@example.com", ""))
	if c == nil {
		t.Fatal("no session issued")
	}
	if max := int((ui.MaxVerifierSessionTTL + ui.VerifierSessionGrace) / time.Second); c.MaxAge > max {
		t.Fatalf("MaxAge %d, want at most %d", c.MaxAge, max)
	}
	v.ttl = 0
	if issuedSession(sessionRequest(srv, "", "ok:alice@example.com", "")) != nil {
		t.Fatal("SessionTTL 0 still issued a session")
	}
}

func TestVerifierSessionGraceDuringOutage(t *testing.T) {
	srv, v, now := sessionFixture(t)
	c := issuedSession(sessionRequest(srv, "", "ok:alice@example.com", ""))
	if c == nil {
		t.Fatal("no session issued")
	}
	v.down.Store(true)

	// Past expiry, within grace: the verifier sees the session and admits
	// on it, and core does not extend it.
	*now = now.Add(time.Minute + 2*time.Minute)
	rec := sessionRequest(srv, "", "ok:alice@example.com", c.Value)
	wantUser(t, rec, "alice@example.com")
	if issuedSession(rec) != nil {
		t.Fatal("admission on grace re-issued the session")
	}

	// Another principal's header gets no grace from alice's session.
	rec = sessionRequest(srv, "", "ok:bob@example.com", c.Value)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("bob on alice's session during outage: got %d, want 503: %s", rec.Code, rec.Body)
	}

	// Past the grace: unavailable, as without a session.
	*now = now.Add(ui.VerifierSessionGrace)
	rec = sessionRequest(srv, "", "ok:alice@example.com", c.Value)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("past grace: got %d, want 503: %s", rec.Code, rec.Body)
	}
}

func TestVerifierSessionGraceNeedsTheSession(t *testing.T) {
	srv, v, _ := sessionFixture(t)
	v.lie.Store(true)
	rec := sessionRequest(srv, "", "ok:alice@example.com", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("SessionGrace without a session: got %d, want 401: %s", rec.Code, rec.Body)
	}
}

// A verifier that binds no sessions cannot admit anybody on one, even a
// request carrying a cookie this daemon signed for that very principal.
func TestVerifierSessionGraceRefusedWithSessionsOff(t *testing.T) {
	srv, v, now := sessionFixture(t)
	cookie := ui.SignVerifierSessionForTest(nil, "slow-proxy", "alice@example.com", "human", "example.com", now.Add(time.Minute))
	v.ttl = 0
	v.lie.Store(true)
	rec := sessionRequest(srv, "", "ok:alice@example.com", cookie)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("SessionGrace with sessions off: got %d, want 401: %s", rec.Code, rec.Body)
	}

	// With sessions on, SessionGrace still has to name the session's
	// principal.
	v.ttl = time.Minute
	*now = now.Add(2 * time.Minute) // past expiry, within grace
	rec = sessionRequest(srv, "", "ok:bob@example.com", cookie)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("SessionGrace for another principal: got %d, want 401: %s", rec.Code, rec.Body)
	}
}
