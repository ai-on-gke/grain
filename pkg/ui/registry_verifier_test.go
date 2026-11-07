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
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/webauth"
	"github.com/golang-jwt/jwt/v5"
)

func newRegistryJWKSServer(t *testing.T) (*ecdsa.PrivateKey, *rsa.PrivateKey, *atomic.Bool, *httptest.Server) {
	t.Helper()
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey: %v", err)
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	var failJWKS atomic.Bool
	byteLen := (ecKey.PublicKey.Curve.Params().BitSize + 7) / 8
	xBytes := ecKey.PublicKey.X.FillBytes(make([]byte, byteLen))
	yBytes := ecKey.PublicKey.Y.FillBytes(make([]byte, byteLen))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failJWKS.Load() {
			http.Error(w, "jwks unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{
				{
					"kty": "EC",
					"crv": "P-256",
					"alg": "ES256",
					"use": "sig",
					"kid": "reg-key-1",
					"x":   base64.RawURLEncoding.EncodeToString(xBytes),
					"y":   base64.RawURLEncoding.EncodeToString(yBytes),
				},
				{
					"kty": "RSA",
					"alg": "RS256",
					"use": "sig",
					"kid": "reg-rsa-1",
					"n":   base64.RawURLEncoding.EncodeToString(rsaKey.N.Bytes()),
					"e":   base64.RawURLEncoding.EncodeToString([]byte{0x01, 0x00, 0x01}),
				},
			},
		})
	}))
	t.Cleanup(srv.Close)
	return ecKey, rsaKey, &failJWKS, srv
}

func r503Ctx(t *testing.T) context.Context {
	t.Helper()
	return ui.WithPrincipal(context.Background(), model.Principal{
		Kind:   model.PrincipalHuman,
		ID:     "owner@example.com",
		Source: model.SourceLoopback,
	})
}

func signRegistryES256(t *testing.T, key *ecdsa.PrivateKey, kid string, claims ui.RegistryClaims) string {
	t.Helper()
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

func TestRegistryIdentityVerifierEndToEnd(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)

	ecKey, rsaKey, failJWKS, jwksSrv := newRegistryJWKSServer(t)

	const (
		issuer     = "https://grain.example.com"
		audience   = "https://my-grain.grain.example.com"
		accessCode = "test-access-code-secret-123456"
	)

	srv, client := testServer(t)
	client.Config.Actor = ui.DefaultActor("owner@example.com")
	client.Config.RegistryIssuer = issuer
	client.Config.RegistryAudience = audience
	client.Config.RegistryKeysURL = jwksSrv.URL
	client.Config.RequireVerifier = "registry"
	client.Config.AllowedPrincipals = []string{"user:alice@example.com"}
	client.Config.ProvisionedServiceAccounts = []string{"ci-bot@proj.iam.gserviceaccount.com"}
	ui.SetAccessCodeForTest(srv, accessCode)
	if _, err := client.AllowPrincipal(r503Ctx(t), ui.AllowPrincipalRequest{
		Entry:  "serviceAccount:ci-bot@proj.iam.gserviceaccount.com",
		Role:   "grain/viewer",
		Reason: "ci bot",
	}); err != nil {
		t.Fatalf("AllowPrincipal: %v", err)
	}

	now := time.Now()
	validClaims := func(email string, kind model.PrincipalKind) ui.RegistryClaims {
		return ui.RegistryClaims{
			RegisteredClaims: jwt.RegisteredClaims{
				Issuer:    issuer,
				Audience:  jwt.ClaimStrings{audience},
				Subject:   email,
				IssuedAt:  jwt.NewNumericDate(now.Add(-5 * time.Second)),
				NotBefore: jwt.NewNumericDate(now.Add(-5 * time.Second)),
				ExpiresAt: jwt.NewNumericDate(now.Add(55 * time.Second)),
				ID:        "jti-reg-1",
			},
			Email:     email,
			Kind:      webauth.PrincipalKind(kind),
			RequestID: "req_01J",
		}
	}

	// 1. Initial JWKS fetch failure returns 503 Service Unavailable with Retry-After: 5.
	failJWKS.Store(true)
	tokAlice := signRegistryES256(t, ecKey, "reg-key-1", validClaims("Alice@Example.com", model.PrincipalHuman))
	r503 := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	r503.RemoteAddr = "203.0.113.10:4242"
	r503.Header.Set(ui.RegistryAssertionHeader, tokAlice)
	w503 := httptest.NewRecorder()
	srv.ServeHTTP(w503, r503)
	if w503.Code != http.StatusServiceUnavailable {
		t.Fatalf("initial JWKS outage status = %d, want 503 (%s)", w503.Code, w503.Body.String())
	}
	if got := w503.Header().Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After = %q, want 5", got)
	}
	failJWKS.Store(false)

	// 2. Valid human registry JWT authenticates as Principal{Kind: human, ID: alice@example.com, Source: "registry"}
	//    and GET /api/auth/user reports authMethod: "registry" and user: "alice@example.com".
	rUser := httptest.NewRequest(http.MethodGet, "/api/auth/user", nil)
	rUser.RemoteAddr = "203.0.113.10:4242"
	rUser.Header.Set(ui.RegistryAssertionHeader, tokAlice)
	wUser := httptest.NewRecorder()
	srv.ServeHTTP(wUser, rUser)
	if wUser.Code != http.StatusOK {
		t.Fatalf("GET /api/auth/user status = %d: %s", wUser.Code, wUser.Body.String())
	}
	var userResp ui.AuthUserResponse
	if err := json.Unmarshal(wUser.Body.Bytes(), &userResp); err != nil {
		t.Fatalf("unmarshal /api/auth/user: %v", err)
	}
	if !userResp.Authenticated || userResp.User != "alice@example.com" || userResp.AuthMethod != ui.AuthMethodRegistry {
		t.Fatalf("/api/auth/user = %+v, want authenticated alice@example.com via registry", userResp)
	}

	// Verify ExtractPrincipal and PrincipalLabel on the registry assertion request.
	rExtract := rUser.WithContext(ui.WithIAPConfig(rUser.Context(), ui.IAPConfig{
		RegistryIssuer:   issuer,
		RegistryAudience: audience,
		RegistryKeysURL:  jwksSrv.URL,
	}))
	pAlice, ok := ui.ExtractPrincipal(rExtract)
	if !ok || pAlice != (model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com", Source: model.SourceRegistry}) || !pAlice.Verified() {
		t.Fatalf("ExtractPrincipal = (%+v, %v), want verified registry principal alice@example.com", pAlice, ok)
	}
	if got := ui.PrincipalLabel(pAlice, nil); got != "alice@example.com" {
		t.Fatalf("PrincipalLabel = %q, want %q", got, "alice@example.com")
	}

	// 3. Valid service account registry JWT authenticates as PrincipalService with Source="registry".
	tokSA := signRegistryES256(t, ecKey, "reg-key-1", validClaims("ci-bot@proj.iam.gserviceaccount.com", ""))
	rSA := httptest.NewRequest(http.MethodGet, "/api/auth/user", nil)
	rSA.RemoteAddr = "203.0.113.10:4242"
	rSA.Header.Set(ui.RegistryAssertionHeader, tokSA)
	wSA := httptest.NewRecorder()
	srv.ServeHTTP(wSA, rSA)
	if wSA.Code != http.StatusOK {
		t.Fatalf("service account GET /api/auth/user status = %d: %s", wSA.Code, wSA.Body.String())
	}
	var saResp ui.AuthUserResponse
	if err := json.Unmarshal(wSA.Body.Bytes(), &saResp); err != nil {
		t.Fatalf("unmarshal service account /api/auth/user: %v", err)
	}
	if !saResp.Authenticated || saResp.User != "ci-bot@proj.iam.gserviceaccount.com" || saResp.AuthMethod != ui.AuthMethodRegistry {
		t.Fatalf("service account response = %+v", saResp)
	}
	rSAExtract := rSA.WithContext(ui.WithIAPConfig(rSA.Context(), ui.IAPConfig{
		RegistryIssuer:   issuer,
		RegistryAudience: audience,
		RegistryKeysURL:  jwksSrv.URL,
	}))
	pSA, ok := ui.ExtractPrincipal(rSAExtract)
	if !ok || pSA != (model.Principal{Kind: model.PrincipalService, ID: "ci-bot@proj.iam.gserviceaccount.com", Source: model.SourceRegistry}) || !pSA.Verified() {
		t.Fatalf("ExtractPrincipal(SA) = (%+v, %v)", pSA, ok)
	}

	// 4. AccessCodeWins() == false: when both X-Grain-Registry-Assertion and an access code are sent,
	//    the registry identity wins (and an unallowlisted registry principal gets 403 even with a valid access code).
	tokOutsider := signRegistryES256(t, ecKey, "reg-key-1", validClaims("mallory@example.com", model.PrincipalHuman))
	rOutsider := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	rOutsider.RemoteAddr = "127.0.0.1:4242"
	rOutsider.Header.Set(ui.RegistryAssertionHeader, tokOutsider)
	rOutsider.Header.Set("Authorization", "Bearer "+accessCode)
	wOutsider := httptest.NewRecorder()
	srv.ServeHTTP(wOutsider, rOutsider)
	if wOutsider.Code != http.StatusForbidden {
		t.Fatalf("unallowlisted registry principal + valid access code = %d, want 403 (%s)", wOutsider.Code, wOutsider.Body.String())
	}

	// 5. Invalid assertions (bad signature, RS256, wrong audience, wrong issuer, expired, lifetime > 5m, act claim present)
	//    all return 401 and never fall back to a valid access code on the same request.
	otherEC, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey: %v", err)
	}
	rsTok := jwt.NewWithClaims(jwt.SigningMethodRS256, validClaims("alice@example.com", model.PrincipalHuman))
	rsTok.Header["kid"] = "reg-rsa-1"
	rsSigned, err := rsTok.SignedString(rsaKey)
	if err != nil {
		t.Fatalf("rsa SignedString: %v", err)
	}

	wrongAud := validClaims("alice@example.com", model.PrincipalHuman)
	wrongAud.Audience = jwt.ClaimStrings{"https://other.grain.example.com"}

	multiAud := validClaims("alice@example.com", model.PrincipalHuman)
	multiAud.Audience = jwt.ClaimStrings{audience, "https://other.grain.example.com"}

	wrongIss := validClaims("alice@example.com", model.PrincipalHuman)
	wrongIss.Issuer = "https://evil.example.com"

	expired := validClaims("alice@example.com", model.PrincipalHuman)
	expired.IssuedAt = jwt.NewNumericDate(now.Add(-3 * time.Minute))
	expired.ExpiresAt = jwt.NewNumericDate(now.Add(-2 * time.Minute))

	tooLong := validClaims("alice@example.com", model.PrincipalHuman)
	tooLong.IssuedAt = jwt.NewNumericDate(now)
	tooLong.ExpiresAt = jwt.NewNumericDate(now.Add(6 * time.Minute))

	withAct := validClaims("alice@example.com", model.PrincipalHuman)
	withAct.Act = json.RawMessage(`{"sub":"service@example.com"}`)

	badTokens := map[string]string{
		"wrong-key-signature": signRegistryES256(t, otherEC, "reg-key-1", validClaims("alice@example.com", model.PrincipalHuman)),
		"rs256-alg":           rsSigned,
		"wrong-audience":      signRegistryES256(t, ecKey, "reg-key-1", wrongAud),
		"multi-audience":      signRegistryES256(t, ecKey, "reg-key-1", multiAud),
		"wrong-issuer":        signRegistryES256(t, ecKey, "reg-key-1", wrongIss),
		"expired":             signRegistryES256(t, ecKey, "reg-key-1", expired),
		"lifetime-over-5m":    signRegistryES256(t, ecKey, "reg-key-1", tooLong),
		"reserved-act-claim":  signRegistryES256(t, ecKey, "reg-key-1", withAct),
	}
	for name, badTok := range badTokens {
		rBad := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
		rBad.RemoteAddr = "127.0.0.1:4242"
		rBad.Header.Set(ui.RegistryAssertionHeader, badTok)
		rBad.Header.Set("Authorization", "Bearer "+accessCode)
		wBad := httptest.NewRecorder()
		srv.ServeHTTP(wBad, rBad)
		if wBad.Code != http.StatusUnauthorized {
			t.Fatalf("case %s: status = %d, want 401 (%s)", name, wBad.Code, wBad.Body.String())
		}
		// Ensure raw token is not echoed in error response (#AU-19.2).
		if strings.Contains(wBad.Body.String(), badTok) {
			t.Fatalf("case %s: response body leaked raw token", name)
		}
	}

	// 6. With RequireVerifier: "registry", a network caller presenting only the access code is refused (401),
	//    while an unforwarded loopback caller presenting the access code is admitted (200).
	rNetCode := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	rNetCode.RemoteAddr = "203.0.113.10:4242"
	rNetCode.Header.Set("Authorization", "Bearer "+accessCode)
	wNetCode := httptest.NewRecorder()
	srv.ServeHTTP(wNetCode, rNetCode)
	if wNetCode.Code != http.StatusUnauthorized {
		t.Fatalf("network access code under RequireVerifier=registry: status = %d, want 401", wNetCode.Code)
	}

	rLoopCode := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	rLoopCode.RemoteAddr = "127.0.0.1:4242"
	rLoopCode.Header.Set("Authorization", "Bearer "+accessCode)
	wLoopCode := httptest.NewRecorder()
	srv.ServeHTTP(wLoopCode, rLoopCode)
	if wLoopCode.Code != http.StatusOK {
		t.Fatalf("unforwarded loopback access code under RequireVerifier=registry: status = %d, want 200 (%s)", wLoopCode.Code, wLoopCode.Body.String())
	}
}

func TestRegistryVerifierExposesPersonAndDelegatingApp(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)

	ecKey, _, _, jwksSrv := newRegistryJWKSServer(t)
	const (
		issuer   = "https://grain.example.com"
		audience = "https://my-grain.grain.example.com"
	)

	now := time.Now().Truncate(time.Second)
	claims := ui.RegistryClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuer,
			Subject:   "alice@example.com",
			Audience:  jwt.ClaimStrings{audience},
			IssuedAt:  jwt.NewNumericDate(now.Add(-5 * time.Second)),
			NotBefore: jwt.NewNumericDate(now.Add(-5 * time.Second)),
			ExpiresAt: jwt.NewNumericDate(now.Add(55 * time.Second)),
			ID:        "reg-jti-42",
		},
		Kind:      webauth.PrincipalHuman,
		RequestID: "r_01hj9x",
		Act: json.RawMessage(`{
			"sub": "slack-bot@proj.iam.gserviceaccount.com",
			"kind": "service",
			"app": "slack-bot",
			"grant": "dg_01hj",
			"access": "write"
		}`),
	}
	tok := signRegistryES256(t, ecKey, "reg-key-1", claims)

	verifier := webauth.NewRegistryVerifier(
		webauth.WithRegistryIssuer(issuer),
		webauth.WithRegistryAudience(audience),
		webauth.WithRegistryKeysURL(jwksSrv.URL),
	)

	r := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	r.Header.Set(ui.RegistryAssertionHeader, tok)
	id, err := verifier.Verify(r)
	if err != nil {
		t.Fatalf("verifier.Verify: %v", err)
	}
	if id.Email != "alice@example.com" || id.Sub != "alice@example.com" || id.Kind != webauth.PrincipalHuman {
		t.Fatalf("VerifiedIdentity = %+v, want human alice@example.com", id)
	}
	if id.Via == nil {
		t.Fatal("id.Via == nil, want DelegatingApp")
	}
	wantVia := webauth.DelegatingApp{
		Email:     "slack-bot@proj.iam.gserviceaccount.com",
		Name:      "slack-bot",
		Grant:     "dg_01hj",
		Access:    "write",
		JTI:       "reg-jti-42",
		RequestID: "r_01hj9x",
	}
	if *id.Via != wantVia {
		t.Fatalf("id.Via = %+v, want %+v", *id.Via, wantVia)
	}

	// Default access when omitted is "write"; explicit "read" is preserved.
	claimsRead := claims
	claimsRead.Act = json.RawMessage(`{"sub":"slack-bot@proj.iam.gserviceaccount.com","kind":"service","access":"read"}`)
	rRead := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	rRead.Header.Set(ui.RegistryAssertionHeader, signRegistryES256(t, ecKey, "reg-key-1", claimsRead))
	idRead, err := verifier.Verify(rRead)
	if err != nil || idRead.Via == nil || idRead.Via.Access != "read" {
		t.Fatalf("verifier.Verify(read) = %+v, %v; want Via.Access=read", idRead, err)
	}
}

func TestRegistryVerifierRefusesMalformedOrNestedAct(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)

	ecKey, _, _, jwksSrv := newRegistryJWKSServer(t)
	const (
		issuer     = "https://grain.example.com"
		audience   = "https://my-grain.grain.example.com"
		accessCode = "test-access-code-secret-123456"
	)

	srv, client := testServer(t)
	client.Config.Actor = ui.DefaultActor("owner@example.com")
	client.Config.RegistryIssuer = issuer
	client.Config.RegistryAudience = audience
	client.Config.RegistryKeysURL = jwksSrv.URL
	client.Config.AllowedPrincipals = []string{"user:alice@example.com"}
	ui.SetAccessCodeForTest(srv, accessCode)
	if _, err := client.AllowPrincipal(r503Ctx(t), ui.AllowPrincipalRequest{
		Entry:  "delegate:slack-bot@proj.iam.gserviceaccount.com",
		Role:   "grain/delegated",
		Reason: "slack bot",
	}); err != nil {
		t.Fatalf("AllowPrincipal: %v", err)
	}

	now := time.Now().Truncate(time.Second)
	mkClaims := func(sub string, kind model.PrincipalKind, act string) ui.RegistryClaims {
		return ui.RegistryClaims{
			RegisteredClaims: jwt.RegisteredClaims{
				Issuer:    issuer,
				Subject:   sub,
				Audience:  jwt.ClaimStrings{audience},
				IssuedAt:  jwt.NewNumericDate(now.Add(-5 * time.Second)),
				NotBefore: jwt.NewNumericDate(now.Add(-5 * time.Second)),
				ExpiresAt: jwt.NewNumericDate(now.Add(55 * time.Second)),
				ID:        "reg-jti-bad",
			},
			Kind: webauth.PrincipalKind(kind),
			Act:  json.RawMessage(act),
		}
	}

	cases := map[string]ui.RegistryClaims{
		"non-object-string":    mkClaims("alice@example.com", model.PrincipalHuman, `"slack-bot@proj.iam.gserviceaccount.com"`),
		"non-object-array":     mkClaims("alice@example.com", model.PrincipalHuman, `["slack-bot@proj.iam.gserviceaccount.com"]`),
		"null-act":             mkClaims("alice@example.com", model.PrincipalHuman, `null`),
		"missing-act-sub":      mkClaims("alice@example.com", model.PrincipalHuman, `{"kind":"service"}`),
		"act-sub-not-email":    mkClaims("alice@example.com", model.PrincipalHuman, `{"sub":"slack-bot","kind":"service"}`),
		"act-kind-human":       mkClaims("alice@example.com", model.PrincipalHuman, `{"sub":"bob@example.com","kind":"human"}`),
		"outer-kind-service":   mkClaims("ci@proj.iam.gserviceaccount.com", model.PrincipalService, `{"sub":"slack-bot@proj.iam.gserviceaccount.com","kind":"service"}`),
		"nested-act-in-act":    mkClaims("alice@example.com", model.PrincipalHuman, `{"sub":"slack-bot@proj.iam.gserviceaccount.com","kind":"service","act":{"sub":"other@proj.iam.gserviceaccount.com","kind":"service"}}`),
		"self-delegation":      mkClaims("slack-bot@proj.iam.gserviceaccount.com", model.PrincipalHuman, `{"sub":"Slack-Bot@proj.iam.gserviceaccount.com","kind":"service"}`),
		"unknown-access-value": mkClaims("alice@example.com", model.PrincipalHuman, `{"sub":"slack-bot@proj.iam.gserviceaccount.com","kind":"service","access":"admin"}`),
	}

	for name, c := range cases {
		tok := signRegistryES256(t, ecKey, "reg-key-1", c)
		r := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
		r.RemoteAddr = "127.0.0.1:4242"
		r.Header.Set(ui.RegistryAssertionHeader, tok)
		r.Header.Set("Authorization", "Bearer "+accessCode)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("case %s: status = %d, want 401 (%s)", name, w.Code, w.Body.String())
		}
	}
}
