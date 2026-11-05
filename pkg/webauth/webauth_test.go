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

package webauth_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/webauth"
	"github.com/golang-jwt/jwt/v5"
)

func generateECKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	block := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	return priv, string(block)
}

func signEC(t *testing.T, priv *ecdsa.PrivateKey, kid, aud, email, sub, iss string, exp time.Time) string {
	t.Helper()
	claims := webauth.IAPClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    iss,
			Audience:  jwt.ClaimStrings{aud},
			Subject:   sub,
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-time.Minute)),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
		Email: email,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	tok.Header["kid"] = kid
	signed, err := tok.SignedString(priv)
	if err != nil {
		t.Fatalf("SignedString: %v", err)
	}
	return signed
}

func TestIAPVerificationPEMAndJWKS(t *testing.T) {
	webauth.ResetIAPConfig()
	t.Cleanup(webauth.ResetIAPConfig)

	ecPriv, ecPEM := generateECKey(t)
	rsaPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}

	pemSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"ec-1": ecPEM})
	}))
	t.Cleanup(pemSrv.Close)

	jwkSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expBytes := []byte{0x01, 0x00, 0x01}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{
				{
					"kty": "RSA",
					"kid": "rsa-1",
					"n":   base64.RawURLEncoding.EncodeToString(rsaPriv.N.Bytes()),
					"e":   base64.RawURLEncoding.EncodeToString(expBytes),
				},
			},
		})
	}))
	t.Cleanup(jwkSrv.Close)

	const aud = "/projects/100/global/backendServices/200"
	webauth.SetIAPAudience("/projects/999/global/backendServices/1," + aud)
	webauth.SetIAPCertURL(pemSrv.URL)

	good := signEC(t, ecPriv, "ec-1", aud, "accounts.google.com:Alice@Example.com", "sub-1", webauth.GoogleIAPIssuer, time.Now().Add(5*time.Minute))
	email, err := webauth.VerifyIAPToken(context.Background(), good)
	if err != nil {
		t.Fatalf("VerifyIAPToken: %v", err)
	}
	if email != "alice@example.com" {
		t.Fatalf("email = %q, want alice@example.com", email)
	}

	verifier := webauth.NewIAPVerifier()
	req := httptest.NewRequest(http.MethodGet, "https://grain-user.example/api/me", nil)
	if _, ok, err := verifier.VerifyRequest(req); err != nil || ok {
		t.Fatalf("VerifyRequest without header = (%v, %v), want (false, nil)", ok, err)
	}
	req.Header.Set(webauth.IAPAssertionHeader, good)
	id, err := verifier.Verify(req)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.Email != "alice@example.com" || id.Kind != webauth.PrincipalHuman {
		t.Fatalf("id = %+v, want human alice@example.com", id)
	}

	// Service account classification.
	saTok := signEC(t, ecPriv, "ec-1", aud, "ci-bot@my-proj.iam.gserviceaccount.com", "sub-sa", webauth.GoogleIAPIssuer, time.Now().Add(5*time.Minute))
	reqSA := httptest.NewRequest(http.MethodGet, "https://grain-user.example/api/me", nil)
	reqSA.Header.Set(webauth.IAPAssertionHeader, saTok)
	saID, err := verifier.Verify(reqSA)
	if err != nil {
		t.Fatalf("Verify SA: %v", err)
	}
	if saID.Kind != webauth.PrincipalService {
		t.Fatalf("SA Kind = %q, want %q", saID.Kind, webauth.PrincipalService)
	}

	// Reject wrong audience, wrong issuer, expired token, unknown kid.
	wrongAud := signEC(t, ecPriv, "ec-1", "/projects/other/global/backendServices/9", "alice@example.com", "s", webauth.GoogleIAPIssuer, time.Now().Add(5*time.Minute))
	if _, err := webauth.VerifyIAPToken(context.Background(), wrongAud); err == nil || !strings.Contains(err.Error(), "audience mismatch") {
		t.Fatalf("wrong audience err = %v, want audience mismatch", err)
	}
	wrongIss := signEC(t, ecPriv, "ec-1", aud, "alice@example.com", "s", "https://evil.example", time.Now().Add(5*time.Minute))
	if _, err := webauth.VerifyIAPToken(context.Background(), wrongIss); err == nil {
		t.Fatal("expected error for wrong issuer")
	}
	expired := signEC(t, ecPriv, "ec-1", aud, "alice@example.com", "s", webauth.GoogleIAPIssuer, time.Now().Add(-5*time.Minute))
	if _, err := webauth.VerifyIAPToken(context.Background(), expired); err == nil {
		t.Fatal("expected error for expired token")
	}
	unknownKid := signEC(t, ecPriv, "missing-kid", aud, "alice@example.com", "s", webauth.GoogleIAPIssuer, time.Now().Add(5*time.Minute))
	if _, err := webauth.VerifyIAPToken(context.Background(), unknownKid); err == nil || !strings.Contains(err.Error(), `unknown IAP key ID "missing-kid"`) {
		t.Fatalf("unknown kid err = %v", err)
	}

	// RSA JWK verification via pinned verifier options.
	rsaClaims := webauth.IAPClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "https://proxy.example",
			Audience:  jwt.ClaimStrings{"custom-aud"},
			Subject:   "https://accounts.google.com:Bob@Example.com",
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-time.Minute)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
		},
	}
	rsaTok := jwt.NewWithClaims(jwt.SigningMethodRS256, rsaClaims)
	rsaTok.Header["kid"] = "rsa-1"
	rsaSigned, err := rsaTok.SignedString(rsaPriv)
	if err != nil {
		t.Fatalf("SignedString RSA: %v", err)
	}
	pinned := webauth.NewIAPVerifier(
		webauth.WithIAPAudience("custom-aud"),
		webauth.WithIAPIssuer("https://proxy.example"),
		webauth.WithIAPKeysURL(jwkSrv.URL),
	)
	reqRSA := httptest.NewRequest(http.MethodGet, "https://grain-user.example/api/me", nil)
	reqRSA.Header.Set(webauth.IAPAssertionHeader, rsaSigned)
	rsaID, err := pinned.Verify(reqRSA)
	if err != nil {
		t.Fatalf("Verify RSA JWK: %v", err)
	}
	if rsaID.Email != "bob@example.com" || rsaID.Kind != webauth.PrincipalHuman {
		t.Fatalf("rsaID = %+v, want human bob@example.com", rsaID)
	}
}

type funcChecker struct {
	fn      func(ctx context.Context, groupEmail, memberEmail string) (bool, error)
	handles func(groupEmail string) bool
}

func (f funcChecker) IsMember(ctx context.Context, groupEmail, memberEmail string) (bool, error) {
	return f.fn(ctx, groupEmail, memberEmail)
}

type scopedFuncChecker struct {
	funcChecker
}

func (s scopedFuncChecker) HandlesGroup(groupEmail string) bool {
	return s.handles(groupEmail)
}

func TestCloudIdentityAndRegisterGroupMemberChecker(t *testing.T) {
	var cloudCalls atomic.Int32
	ciSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cloudCalls.Add(1)
		switch r.URL.Path {
		case "/v1/groups:lookup":
			if r.URL.Query().Get("groupKey.id") == "cloud-team@example.com" {
				_ = json.NewEncoder(w).Encode(map[string]string{"name": "groups/12345"})
				return
			}
			http.Error(w, "not found", http.StatusNotFound)
		case "/v1/groups/12345/memberships:checkTransitiveMembership":
			has := strings.Contains(r.URL.Query().Get("query"), "carol@example.com")
			_ = json.NewEncoder(w).Encode(map[string]bool{"hasMembership": has})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ciSrv.Close)

	now := time.Now()
	ci := webauth.NewCloudIdentityGroupChecker(
		webauth.WithCloudIdentityEndpoint(ciSrv.URL),
		webauth.WithCloudIdentityHTTPClient(ciSrv.Client()),
		webauth.WithCloudIdentityClock(func() time.Time { return now }),
	)
	composite := webauth.NewDefaultGroupMemberChecker(ci)

	// 1. With no registered checkers, Cloud Identity resolves directly and caches.
	ok, err := composite.IsMember(context.Background(), "cloud-team@example.com", "carol@example.com")
	if err != nil || !ok {
		t.Fatalf("Cloud Identity member = (%v, %v), want (true, nil)", ok, err)
	}
	callsAfterFirst := cloudCalls.Load()
	ok, err = composite.IsMember(context.Background(), "cloud-team@example.com", "carol@example.com")
	if err != nil || !ok {
		t.Fatalf("cached Cloud Identity member = (%v, %v), want (true, nil)", ok, err)
	}
	if cloudCalls.Load() != callsAfterFirst {
		t.Fatalf("expected cached answer without extra HTTP calls: got %d, want %d", cloudCalls.Load(), callsAfterFirst)
	}

	// 2. Register an unscoped directory checker beside Cloud Identity:
	//    - A positive match short-circuits without touching Cloud Identity.
	//    - A negative match falls through to Cloud Identity.
	unregCorp := webauth.RegisterGroupMemberChecker(funcChecker{
		fn: func(ctx context.Context, groupEmail, memberEmail string) (bool, error) {
			if groupEmail == "eng-roster@example.com" && memberEmail == "alice@example.com" {
				return true, nil
			}
			return false, nil
		},
	})
	t.Cleanup(unregCorp)

	beforeShortCircuit := cloudCalls.Load()
	ok, err = composite.IsMember(context.Background(), "eng-roster@example.com", "alice@example.com")
	if err != nil || !ok {
		t.Fatalf("registered checker positive match = (%v, %v), want (true, nil)", ok, err)
	}
	if cloudCalls.Load() != beforeShortCircuit {
		t.Fatalf("positive registered match should not call Cloud Identity: got %d, want %d", cloudCalls.Load(), beforeShortCircuit)
	}

	ok, err = composite.IsMember(context.Background(), "cloud-team@example.com", "dave@example.com")
	if err != nil || ok {
		t.Fatalf("non-member in both = (%v, %v), want (false, nil)", ok, err)
	}

	// 3. Register a scoped directory checker authoritative for *@internal.example:
	//    neither positive nor negative lookups for @internal.example touch Cloud Identity.
	unregScoped := webauth.RegisterGroupMemberChecker(scopedFuncChecker{
		funcChecker: funcChecker{
			handles: func(groupEmail string) bool {
				return strings.HasSuffix(groupEmail, "@internal.example")
			},
			fn: func(ctx context.Context, groupEmail, memberEmail string) (bool, error) {
				return groupEmail == "sre@internal.example" && memberEmail == "erin@example.com", nil
			},
		},
	})
	t.Cleanup(unregScoped)

	beforeScoped := cloudCalls.Load()
	if ok, err := composite.IsMember(context.Background(), "sre@internal.example", "erin@example.com"); err != nil || !ok {
		t.Fatalf("scoped member = (%v, %v), want (true, nil)", ok, err)
	}
	if ok, err := composite.IsMember(context.Background(), "sre@internal.example", "mallory@example.com"); err != nil || ok {
		t.Fatalf("scoped non-member = (%v, %v), want (false, nil)", ok, err)
	}
	if cloudCalls.Load() != beforeScoped {
		t.Fatalf("scoped checker should be authoritative without calling Cloud Identity: got %d, want %d", cloudCalls.Load(), beforeScoped)
	}

	// 4. Error fail-closed behavior: if a registered checker errors and Cloud Identity does not confirm membership, return error.
	unregErr := webauth.RegisterGroupMemberChecker(funcChecker{
		fn: func(ctx context.Context, groupEmail, memberEmail string) (bool, error) {
			if groupEmail == "broken-dir@example.com" {
				return false, errors.New("directory backend unreachable")
			}
			return false, webauth.ErrGroupCheckerSkip
		},
	})
	if _, err := composite.IsMember(context.Background(), "broken-dir@example.com", "alice@example.com"); err == nil || !strings.Contains(err.Error(), "directory backend unreachable") {
		t.Fatalf("broken checker err = %v, want directory backend unreachable", err)
	}
	unregErr()
	unregErr() // idempotent
}

func TestCSRFHelpers(t *testing.T) {
	// Safe GET passes CheckCrossOrigin even cross-site.
	getReq := httptest.NewRequest(http.MethodGet, "https://alice.grain-user.example/api/entries", nil)
	getReq.Header.Set("Sec-Fetch-Site", "cross-site")
	if err := webauth.CheckCrossOrigin(getReq); err != nil {
		t.Fatalf("GET CheckCrossOrigin: %v", err)
	}

	// Cross-site POST fails CheckCrossOrigin.
	badPost := httptest.NewRequest(http.MethodPost, "https://alice.grain-user.example/api/entries", nil)
	badPost.Header.Set("Sec-Fetch-Site", "cross-site")
	if err := webauth.CheckCrossOrigin(badPost); err == nil {
		t.Fatal("expected CheckCrossOrigin to reject cross-site POST")
	}

	// Sibling-subdomain Origin must NOT match on proxy-authenticated mutation.
	sibling := httptest.NewRequest(http.MethodPost, "https://alice.grain-user.example/api/tasks", nil)
	sibling.Host = "alice.grain-user.example"
	sibling.Header.Set("Origin", "https://bob.grain-user.example")
	if err := webauth.VerifySameOriginForProxyAuth(sibling); err == nil {
		t.Fatal("expected sibling subdomain Origin to be rejected")
	}

	// Matching Origin via X-Forwarded-Host passes.
	xfh := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/tasks", nil)
	xfh.Host = "127.0.0.1:8080"
	xfh.Header.Set("Origin", "https://alice.grain-user.example")
	xfh.Header.Set("X-Forwarded-Host", "alice.grain-user.example")
	if err := webauth.VerifySameOriginForProxyAuth(xfh); err != nil {
		t.Fatalf("X-Forwarded-Host match failed: %v", err)
	}

	// Cookie without Sec-Fetch-Site or Origin is rejected; cookie-less CLI passes.
	cookieReq := httptest.NewRequest(http.MethodPost, "https://alice.grain-user.example/api/tasks", nil)
	cookieReq.Header.Set("Cookie", "GCP_IAAP_AUTH_TOKEN=abc")
	if err := webauth.VerifySameOriginForProxyAuth(cookieReq); err == nil {
		t.Fatal("expected cookie-authenticated POST without Sec-Fetch-Site/Origin to be rejected")
	}
	cliReq := httptest.NewRequest(http.MethodPost, "https://alice.grain-user.example/api/tasks", nil)
	if err := webauth.VerifySameOriginForProxyAuth(cliReq); err != nil {
		t.Fatalf("cookie-less CLI POST failed: %v", err)
	}
}
