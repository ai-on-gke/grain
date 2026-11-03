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

package security

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// identityHeaders is every plaintext header ExtractPrincipal is willing
// to read an identity from, when it is willing to at all.
var identityHeaders = []string{
	"X-Goog-Authenticated-User-Email",
	"X-User-LDAP",
	"X-Forwarded-User",
	"X-Goog-Authenticated-User-Id",
	"X-Grain-User",
	"Grain-User",
}

func requestFrom(remoteAddr string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	r.RemoteAddr = remoteAddr
	return r
}

// A plaintext identity header is a claim, and it is only believed from
// somewhere a proxy that set it could be: never from the open internet.
func TestPlaintextIdentityHeadersIgnoredFromUntrustedSource(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	for _, h := range identityHeaders {
		r := requestFrom("203.0.113.9:4242") // TEST-NET-3: public, not configured as trusted
		r.Header.Set(h, "admin@example.com")
		if p, ok := ui.ExtractPrincipal(r); ok {
			t.Errorf("%s from a public address was believed: principal %+v", h, p)
		}
	}
}

// Even from loopback, a plaintext identity header is believed only where
// the operator opted in (-ui-trust-identity-headers): a BeyondCorp port
// tunnel, or any other process on the host, delivers from loopback too
// (grain/task-1847).
func TestPlaintextIdentityHeadersIgnoredFromLoopbackByDefault(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	for _, h := range identityHeaders {
		r := requestFrom("127.0.0.1:4242")
		r.Header.Set(h, "accounts.google.com:admin@example.com")
		if p, ok := ui.ExtractPrincipal(r); ok {
			t.Errorf("loopback %s believed without the opt-in: %+v", h, p)
		}
	}
}

// In IAP mode the only identity that counts is a verified assertion: a
// header saying who the caller is proves nothing,
// even from inside the network, because IAP is what was meant to say so.
func TestIAPModeIgnoresPlaintextIdentityHeaders(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	for _, h := range identityHeaders {
		r := requestFrom("127.0.0.1:4242")
		r = r.WithContext(ui.WithIAPConfig(r.Context(), ui.IAPConfig{RequireIAP: true, Audience: "/projects/1/global/backendServices/2"}))
		r.Header.Set(h, "admin@example.com")
		if p, ok := ui.ExtractPrincipal(r); ok {
			t.Errorf("%s believed in IAP mode: principal %+v", h, p)
		}
	}
}

// A forged IAP assertion must not fall through to the plaintext headers
// beside it -- that fallthrough would make "send any garbage as the
// assertion" a way past IAP.
func TestForgedIAPAssertionDoesNotFallBackToHeaders(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	reject := func(context.Context, string, string) (*ui.IAPClaims, error) {
		return nil, errors.New("signature does not verify")
	}
	unreg := ui.RegisterIdentityVerifier(ui.NewIAPVerifier(ui.WithIAPValidator(reject)))
	t.Cleanup(unreg)
	r := requestFrom("127.0.0.1:4242")
	r.Header.Set(ui.IAPAssertionHeader, "forged.jwt.value")
	r.Header.Set("X-Grain-User", "admin@example.com")
	if p, ok := ui.ExtractPrincipal(r); ok {
		t.Fatalf("forged IAP assertion fell back to X-Grain-User: %+v", p)
	}
}

func signSession(t *testing.T, method jwt.SigningMethod, key any, subject string, expires time.Time) string {
	t.Helper()
	claims := jwt.RegisteredClaims{
		Subject:   subject,
		Issuer:    "grain",
		IssuedAt:  jwt.NewNumericDate(time.Now().Add(-time.Minute)),
		ExpiresAt: jwt.NewNumericDate(expires),
	}
	s, err := jwt.NewWithClaims(method, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// grain keeps no sign-in sessions any more (docs/single-tenant-reset.md,
// stage 3d), so no token in the old session cookie or a bearer header
// names anybody -- not one shaped like the session JWTs grain used to
// sign, however it is signed.
func TestSessionTokensNameNobody(t *testing.T) {
	future := time.Now().Add(time.Hour)
	cases := map[string]string{
		"HS256":               signSession(t, jwt.SigningMethodHS256, []byte("security-suite-session-secret-0123456789"), "admin", future),
		"unsigned (alg none)": signSession(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, "admin", future),
		"garbage":             "not-a-token",
	}
	for name, token := range cases {
		r := requestFrom("203.0.113.9:4242")
		r.Header.Set("Authorization", "Bearer "+token)
		r.AddCookie(&http.Cookie{Name: "grain_session", Value: token})
		if p, ok := ui.ExtractPrincipal(r); ok {
			t.Errorf("%s token authenticated a request as %+v", name, p)
		}
	}
}
