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

func ecJWK(kid string, pub *ecdsa.PublicKey) map[string]string {
	byteLen := (pub.Curve.Params().BitSize + 7) / 8
	xBytes := pub.X.FillBytes(make([]byte, byteLen))
	yBytes := pub.Y.FillBytes(make([]byte, byteLen))
	return map[string]string{
		"kty": "EC",
		"crv": "P-256",
		"alg": "ES256",
		"use": "sig",
		"kid": kid,
		"x":   base64.RawURLEncoding.EncodeToString(xBytes),
		"y":   base64.RawURLEncoding.EncodeToString(yBytes),
	}
}

func TestRegistryVerifier(t *testing.T) {
	webauth.ResetRegistryConfig()
	t.Cleanup(webauth.ResetRegistryConfig)

	ecKey1, _ := generateECKey(t)
	ecKey2, _ := generateECKey(t)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}

	var jwksFetches atomic.Int32
	var serveKey2 atomic.Bool
	var failJWKS atomic.Bool

	jwksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jwksFetches.Add(1)
		if failJWKS.Load() {
			http.Error(w, "jwks down", http.StatusInternalServerError)
			return
		}
		keys := []map[string]string{
			ecJWK("reg-ec-1", &ecKey1.PublicKey),
			{
				"kty": "RSA",
				"alg": "RS256",
				"use": "sig",
				"kid": "reg-rsa-1",
				"n":   base64.RawURLEncoding.EncodeToString(rsaKey.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString([]byte{0x01, 0x00, 0x01}),
			},
		}
		if serveKey2.Load() {
			keys = append(keys, ecJWK("reg-ec-2", &ecKey2.PublicKey))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys})
	}))
	t.Cleanup(jwksSrv.Close)

	const (
		issuer   = "https://grain.example.com"
		audience = "https://my-grain.grain.example.com"
	)

	verifier := webauth.NewRegistryVerifier(
		webauth.WithRegistryIssuer(issuer),
		webauth.WithRegistryAudience(audience),
		webauth.WithRegistryKeysURL(jwksSrv.URL),
		webauth.WithRegistryVerifierHTTPClient(jwksSrv.Client()),
	)
	if verifier.Name() != "registry" {
		t.Fatalf("Name() = %q, want registry", verifier.Name())
	}
	if headers := verifier.Headers(); len(headers) != 2 || headers[0] != webauth.HubAssertionHeader || headers[1] != webauth.RegistryAssertionHeader {
		t.Fatalf("Headers() = %v, want [%s %s]", headers, webauth.HubAssertionHeader, webauth.RegistryAssertionHeader)
	}
	if verifier.AccessCodeWins() {
		t.Fatal("AccessCodeWins() = true, want false")
	}

	now := time.Now()
	signES256 := func(kid string, key *ecdsa.PrivateKey, claims jwt.Claims) string {
		tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
		if kid != "" {
			tok.Header["kid"] = kid
		}
		s, err := tok.SignedString(key)
		if err != nil {
			t.Fatalf("SignedString: %v", err)
		}
		return s
	}

	// 1. Initial JWKS fetch failure returns an error implementing Unavailable() == true.
	failJWKS.Store(true)
	validClaims := webauth.RegistryClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuer,
			Audience:  jwt.ClaimStrings{audience},
			Subject:   "Alice@Example.com",
			IssuedAt:  jwt.NewNumericDate(now.Add(-5 * time.Second)),
			NotBefore: jwt.NewNumericDate(now.Add(-5 * time.Second)),
			ExpiresAt: jwt.NewNumericDate(now.Add(55 * time.Second)),
			ID:        "jti-1",
		},
		Email:     "Alice@Example.com",
		Kind:      webauth.PrincipalHuman,
		RequestID: "req_1",
	}
	validTok := signES256("reg-ec-1", ecKey1, validClaims)
	req := httptest.NewRequest(http.MethodGet, "https://my-grain.grain.example.com/api/tasks", nil)
	req.Header.Set(webauth.RegistryAssertionHeader, validTok)
	if _, _, err := verifier.VerifyRequest(req); err == nil {
		t.Fatal("expected initial JWKS failure to return error")
	} else {
		var u interface{ Unavailable() bool }
		if !errors.As(err, &u) || !u.Unavailable() {
			t.Fatalf("initial JWKS failure err = %v, want Unavailable() == true", err)
		}
	}
	failJWKS.Store(false)

	// 2. Valid human token verifies and normalizes email.
	id, ok, err := verifier.VerifyRequest(req)
	if err != nil || !ok {
		t.Fatalf("VerifyRequest(valid human): ok=%v, err=%v", ok, err)
	}
	if id.Email != "alice@example.com" || id.Kind != webauth.PrincipalHuman {
		t.Fatalf("id = %+v, want {alice@example.com, human}", id)
	}

	// 3. Valid service account token classifies as PrincipalService.
	saTok := signES256("reg-ec-1", ecKey1, webauth.RegistryClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuer,
			Audience:  jwt.ClaimStrings{audience},
			Subject:   "ci-runner@proj.iam.gserviceaccount.com",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
		},
		Email: "ci-runner@proj.iam.gserviceaccount.com",
	})
	reqSA := httptest.NewRequest(http.MethodGet, "https://my-grain.grain.example.com/api/tasks", nil)
	reqSA.Header.Set(webauth.RegistryAssertionHeader, saTok)
	idSA, ok, err := verifier.VerifyRequest(reqSA)
	if err != nil || !ok || idSA.Kind != webauth.PrincipalService || idSA.Email != "ci-runner@proj.iam.gserviceaccount.com" {
		t.Fatalf("VerifyRequest(valid service): id=%+v, ok=%v, err=%v", idSA, ok, err)
	}

	// 4. Algorithm pinning: reject RS256, HS256, none, and missing kid.
	rsTok := jwt.NewWithClaims(jwt.SigningMethodRS256, validClaims)
	rsTok.Header["kid"] = "reg-rsa-1"
	rsStr, err := rsTok.SignedString(rsaKey)
	if err != nil {
		t.Fatalf("RSA SignedString: %v", err)
	}
	reqRS := httptest.NewRequest(http.MethodGet, "https://my-grain.grain.example.com/api/tasks", nil)
	reqRS.Header.Set(webauth.RegistryAssertionHeader, rsStr)
	if _, _, err := verifier.VerifyRequest(reqRS); err == nil {
		t.Fatal("expected RS256 token to be rejected")
	}

	hsTok := jwt.NewWithClaims(jwt.SigningMethodHS256, validClaims)
	hsTok.Header["kid"] = "reg-ec-1"
	hsStr, err := hsTok.SignedString([]byte("secret"))
	if err != nil {
		t.Fatalf("HS256 SignedString: %v", err)
	}
	reqHS := httptest.NewRequest(http.MethodGet, "https://my-grain.grain.example.com/api/tasks", nil)
	reqHS.Header.Set(webauth.RegistryAssertionHeader, hsStr)
	if _, _, err := verifier.VerifyRequest(reqHS); err == nil {
		t.Fatal("expected HS256 token to be rejected")
	}

	noneTok := jwt.NewWithClaims(jwt.SigningMethodNone, validClaims)
	noneTok.Header["kid"] = "reg-ec-1"
	noneStr, err := noneTok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("none SignedString: %v", err)
	}
	reqNone := httptest.NewRequest(http.MethodGet, "https://my-grain.grain.example.com/api/tasks", nil)
	reqNone.Header.Set(webauth.RegistryAssertionHeader, noneStr)
	if _, _, err := verifier.VerifyRequest(reqNone); err == nil {
		t.Fatal("expected alg=none token to be rejected")
	}

	noKidStr := signES256("", ecKey1, validClaims)
	reqNoKid := httptest.NewRequest(http.MethodGet, "https://my-grain.grain.example.com/api/tasks", nil)
	reqNoKid.Header.Set(webauth.RegistryAssertionHeader, noKidStr)
	if _, _, err := verifier.VerifyRequest(reqNoKid); err == nil {
		t.Fatal("expected token without kid to be rejected")
	}

	// 5. Wrong issuer, wrong audience, and multi-audience token rejected.
	wrongIss := validClaims
	wrongIss.Issuer = "https://other.example.com"
	reqWrongIss := httptest.NewRequest(http.MethodGet, "https://my-grain.grain.example.com/api/tasks", nil)
	reqWrongIss.Header.Set(webauth.RegistryAssertionHeader, signES256("reg-ec-1", ecKey1, wrongIss))
	if _, _, err := verifier.VerifyRequest(reqWrongIss); err == nil {
		t.Fatal("expected wrong issuer to be rejected")
	}

	wrongAud := validClaims
	wrongAud.Audience = jwt.ClaimStrings{"https://other-grain.grain.example.com"}
	reqWrongAud := httptest.NewRequest(http.MethodGet, "https://my-grain.grain.example.com/api/tasks", nil)
	reqWrongAud.Header.Set(webauth.RegistryAssertionHeader, signES256("reg-ec-1", ecKey1, wrongAud))
	if _, _, err := verifier.VerifyRequest(reqWrongAud); err == nil {
		t.Fatal("expected wrong audience to be rejected")
	}

	multiAud := validClaims
	multiAud.Audience = jwt.ClaimStrings{audience, "https://other-grain.grain.example.com"}
	reqMultiAud := httptest.NewRequest(http.MethodGet, "https://my-grain.grain.example.com/api/tasks", nil)
	reqMultiAud.Header.Set(webauth.RegistryAssertionHeader, signES256("reg-ec-1", ecKey1, multiAud))
	if _, _, err := verifier.VerifyRequest(reqMultiAud); err == nil {
		t.Fatal("expected multi-audience token to be rejected")
	}

	// 6. Lifetime and clock-skew checks:
	// - expired beyond 30s skew rejected; expired within 30s skew accepted
	// - nbf in future beyond 30s skew rejected; within 30s skew accepted
	// - iat missing rejected
	// - exp - iat > 5m rejected; exp <= iat rejected
	expiredClaims := validClaims
	expiredClaims.IssuedAt = jwt.NewNumericDate(now.Add(-2 * time.Minute))
	expiredClaims.ExpiresAt = jwt.NewNumericDate(now.Add(-45 * time.Second))
	reqExpired := httptest.NewRequest(http.MethodGet, "https://my-grain.grain.example.com/api/tasks", nil)
	reqExpired.Header.Set(webauth.RegistryAssertionHeader, signES256("reg-ec-1", ecKey1, expiredClaims))
	if _, _, err := verifier.VerifyRequest(reqExpired); err == nil {
		t.Fatal("expected expired token beyond 30s skew to be rejected")
	}

	skewExpClaims := validClaims
	skewExpClaims.IssuedAt = jwt.NewNumericDate(now.Add(-75 * time.Second))
	skewExpClaims.ExpiresAt = jwt.NewNumericDate(now.Add(-15 * time.Second))
	reqSkewExp := httptest.NewRequest(http.MethodGet, "https://my-grain.grain.example.com/api/tasks", nil)
	reqSkewExp.Header.Set(webauth.RegistryAssertionHeader, signES256("reg-ec-1", ecKey1, skewExpClaims))
	if _, ok, err := verifier.VerifyRequest(reqSkewExp); err != nil || !ok {
		t.Fatalf("expected token expired by 15s (within 30s skew) to verify: ok=%v, err=%v", ok, err)
	}

	futureNbfClaims := validClaims
	futureNbfClaims.NotBefore = jwt.NewNumericDate(now.Add(45 * time.Second))
	reqFutureNbf := httptest.NewRequest(http.MethodGet, "https://my-grain.grain.example.com/api/tasks", nil)
	reqFutureNbf.Header.Set(webauth.RegistryAssertionHeader, signES256("reg-ec-1", ecKey1, futureNbfClaims))
	if _, _, err := verifier.VerifyRequest(reqFutureNbf); err == nil {
		t.Fatal("expected future nbf beyond 30s skew to be rejected")
	}

	noIatClaims := validClaims
	noIatClaims.IssuedAt = nil
	reqNoIat := httptest.NewRequest(http.MethodGet, "https://my-grain.grain.example.com/api/tasks", nil)
	reqNoIat.Header.Set(webauth.RegistryAssertionHeader, signES256("reg-ec-1", ecKey1, noIatClaims))
	if _, _, err := verifier.VerifyRequest(reqNoIat); err == nil {
		t.Fatal("expected token missing iat to be rejected")
	}

	longLifetimeClaims := validClaims
	longLifetimeClaims.IssuedAt = jwt.NewNumericDate(now)
	longLifetimeClaims.ExpiresAt = jwt.NewNumericDate(now.Add(5*time.Minute + time.Second))
	reqLong := httptest.NewRequest(http.MethodGet, "https://my-grain.grain.example.com/api/tasks", nil)
	reqLong.Header.Set(webauth.RegistryAssertionHeader, signES256("reg-ec-1", ecKey1, longLifetimeClaims))
	if _, _, err := verifier.VerifyRequest(reqLong); err == nil || !strings.Contains(err.Error(), "lifetime") {
		t.Fatalf("expected token with exp-iat > 5m to be rejected, got %v", err)
	}

	// 7. Reserved act claim rejected (object, string, or null).
	for _, rawAct := range []string{`{"sub":"service@example.com"}`, `"service@example.com"`, `null`} {
		actClaims := validClaims
		actClaims.Act = json.RawMessage(rawAct)
		reqAct := httptest.NewRequest(http.MethodGet, "https://my-grain.grain.example.com/api/tasks", nil)
		reqAct.Header.Set(webauth.RegistryAssertionHeader, signES256("reg-ec-1", ecKey1, actClaims))
		if _, _, err := verifier.VerifyRequest(reqAct); err == nil || !strings.Contains(err.Error(), "act claim") {
			t.Fatalf("expected token with act=%s to be rejected, got %v", rawAct, err)
		}
	}

	// 8. Principal claim validation: mismatched sub/email, email_verified=false, SA claiming human.
	mismatchClaims := validClaims
	mismatchClaims.Subject = "alice@example.com"
	mismatchClaims.Email = "bob@example.com"
	reqMismatch := httptest.NewRequest(http.MethodGet, "https://my-grain.grain.example.com/api/tasks", nil)
	reqMismatch.Header.Set(webauth.RegistryAssertionHeader, signES256("reg-ec-1", ecKey1, mismatchClaims))
	if _, _, err := verifier.VerifyRequest(reqMismatch); err == nil {
		t.Fatal("expected mismatched sub and email to be rejected")
	}

	f := false
	unverifiedClaims := validClaims
	unverifiedClaims.EmailVerified = &f
	reqUnverified := httptest.NewRequest(http.MethodGet, "https://my-grain.grain.example.com/api/tasks", nil)
	reqUnverified.Header.Set(webauth.RegistryAssertionHeader, signES256("reg-ec-1", ecKey1, unverifiedClaims))
	if _, _, err := verifier.VerifyRequest(reqUnverified); err == nil {
		t.Fatal("expected email_verified=false to be rejected")
	}

	saHumanClaims := validClaims
	saHumanClaims.Subject = "bot@proj.iam.gserviceaccount.com"
	saHumanClaims.Email = "bot@proj.iam.gserviceaccount.com"
	saHumanClaims.Kind = webauth.PrincipalHuman
	reqSAHuman := httptest.NewRequest(http.MethodGet, "https://my-grain.grain.example.com/api/tasks", nil)
	reqSAHuman.Header.Set(webauth.RegistryAssertionHeader, signES256("reg-ec-1", ecKey1, saHumanClaims))
	if _, _, err := verifier.VerifyRequest(reqSAHuman); err == nil {
		t.Fatal("expected .gserviceaccount.com claiming human kind to be rejected")
	}

	// 9. Unknown kid refetch floor (1m default): within floor does not refetch; after floor refetches and picks up rotated key.
	fetchesBefore := jwksFetches.Load()
	serveKey2.Store(true)
	tokKey2 := signES256("reg-ec-2", ecKey2, validClaims)
	reqKey2 := httptest.NewRequest(http.MethodGet, "https://my-grain.grain.example.com/api/tasks", nil)
	reqKey2.Header.Set(webauth.RegistryAssertionHeader, tokKey2)
	if _, _, err := verifier.VerifyRequest(reqKey2); err == nil {
		t.Fatal("expected unknown kid within 1m refetch floor to be rejected without refetching")
	}
	if jwksFetches.Load() != fetchesBefore {
		t.Fatalf("JWKS fetches = %d, want %d (rate-limited within floor)", jwksFetches.Load(), fetchesBefore)
	}

	webauth.SetRegistryRefetchFloor(time.Nanosecond)
	if id2, ok, err := verifier.VerifyRequest(reqKey2); err != nil || !ok || id2.Email != "alice@example.com" {
		t.Fatalf("after refetch floor elapsed: id=%+v, ok=%v, err=%v", id2, ok, err)
	}
}

// A Cloud Identity endpoint override (-ui-cloud-identity-endpoint,
// $GRAIN_CLOUD_IDENTITY_ENDPOINT) must not carry the daemon's ADC bearer
// token in cleartext off this host: anything but https or loopback http is
// refused before the HTTP client is used, and the check fails closed.
func TestCloudIdentityCheckerRefusesNonHTTPSEndpointOffLoopback(t *testing.T) {
	var calls atomic.Int32
	spy := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("unexpected request to " + r.URL.String())
	})}
	for _, ep := range []string{
		"http://cloudidentity.example.com",
		"http://198.51.100.7:8080",
		"ftp://127.0.0.1",
		"127.0.0.1:18280",
	} {
		ci := webauth.NewCloudIdentityGroupChecker(
			webauth.WithCloudIdentityEndpoint(ep),
			webauth.WithCloudIdentityHTTPClient(spy),
		)
		ok, err := ci.IsMember(context.Background(), "ops@example.com", "carol@example.com")
		if ok || err == nil {
			t.Errorf("endpoint %q: IsMember = (%v, %v), want a refusal", ep, ok, err)
		}
	}
	t.Setenv("GRAIN_CLOUD_IDENTITY_ENDPOINT", "http://cloudidentity.example.com")
	if ok, err := webauth.NewCloudIdentityGroupChecker(webauth.WithCloudIdentityHTTPClient(spy)).IsMember(
		context.Background(), "ops@example.com", "carol@example.com"); ok || err == nil {
		t.Errorf("$GRAIN_CLOUD_IDENTITY_ENDPOINT over plain http: IsMember = (%v, %v), want a refusal", ok, err)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("refused endpoints still made %d HTTP request(s)", n)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
