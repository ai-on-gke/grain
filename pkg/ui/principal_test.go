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
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/golang-jwt/jwt/v5"
)

// A daemon that keeps no accounts names nobody from an identity header,
// whatever it says and however it is spelled: the request is judged by
// its access code alone (singleTenantTrusted).
func TestExtractPrincipalNamesNobodyFromAnIdentityHeader(t *testing.T) {
	values := []string{"accounts.google.com:user@google.com", "https://accounts.google.com:bob@company.com", "charlie@domain.org", "ldapuser"}
	for _, h := range ui.PlaintextIdentityHeaders {
		for _, v := range values {
			req, err := http.NewRequest(http.MethodGet, "/api/tasks", nil)
			if err != nil {
				t.Fatalf("http.NewRequest: %v", err)
			}
			req.Header.Set(h, v)
			if p, ok := ui.ExtractPrincipal(req); ok {
				t.Errorf("%s: %s named %+v, want nobody", h, v, p)
			}
		}
	}
}

func TestContextPrincipal(t *testing.T) {
	ctx := context.Background()

	if _, ok := ui.PrincipalFromContext(ctx); ok {
		t.Fatal("empty context reported principal found")
	}

	p := model.Principal{Kind: model.PrincipalHuman, ID: "tester"}
	withP := ui.WithPrincipal(ctx, p)

	got, ok := ui.PrincipalFromContext(withP)
	if !ok || got != p {
		t.Fatalf("PrincipalFromContext = %+v, %v, want %+v, true", got, ok, p)
	}
}

func generateECDSATestKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey failed: %v", err)
	}
	pemStr := string(pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: der,
	}))
	return priv, pemStr
}

func signIAPToken(t *testing.T, priv *ecdsa.PrivateKey, kid, audience, email, sub, issuer string, exp time.Time) string {
	t.Helper()
	claims := ui.IAPClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuer,
			Audience:  jwt.ClaimStrings{audience},
			Subject:   sub,
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-1 * time.Minute)),
		},
		Email: email,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["kid"] = kid
	signed, err := token.SignedString(priv)
	if err != nil {
		t.Fatalf("SignedString failed: %v", err)
	}
	return signed
}

func TestIAPAssertionVerification(t *testing.T) {
	kid := "test-key-1"
	aud := "/projects/12345/apps/grain-test"
	privKey, _ := generateECDSATestKey(t)

	customValidator := func(ctx context.Context, tokenStr, audience string) (*ui.IAPClaims, error) {
		claims := &ui.IAPClaims{}
		tok, err := jwt.ParseWithClaims(tokenStr, claims, func(token *jwt.Token) (any, error) {
			return &privKey.PublicKey, nil
		})
		if err != nil || !tok.Valid {
			return nil, err
		}
		if audience != "" && !slices.Contains(claims.Audience, audience) {
			return nil, jwt.ErrTokenInvalidAudience
		}
		return claims, nil
	}

	iapCfg := ui.IAPConfig{
		Audience:  aud,
		Validator: customValidator,
	}

	t.Run("valid IAP JWT assertion extracts user", func(t *testing.T) {
		tok := signIAPToken(t, privKey, kid, aud, "user@example.com", "accounts.google.com:1001", ui.GoogleIAPIssuer, time.Now().Add(1*time.Hour))
		req, _ := http.NewRequest(http.MethodGet, "/api/tasks", nil)
		req = req.WithContext(ui.WithIAPConfig(req.Context(), iapCfg))
		req.Header.Set("X-Goog-IAP-JWT-Assertion", tok)

		p, ok := verifiedAs(req)
		if !ok {
			t.Fatal("verifiedAs expected ok=true for valid IAP token")
		}
		if p.Kind != model.PrincipalHuman || p.ID != "user@example.com" {
			t.Fatalf("verifiedAs got %+v, want user@example.com", p)
		}
	})

	t.Run("valid IAP JWT with sub claim fallback", func(t *testing.T) {
		tok := signIAPToken(t, privKey, kid, aud, "", "accounts.google.com:alice_sub", ui.GoogleIAPIssuer, time.Now().Add(1*time.Hour))
		req, _ := http.NewRequest(http.MethodGet, "/api/tasks", nil)
		req = req.WithContext(ui.WithIAPConfig(req.Context(), iapCfg))
		req.Header.Set("X-Goog-IAP-JWT-Assertion", tok)

		p, ok := verifiedAs(req)
		if !ok {
			t.Fatal("verifiedAs expected ok=true for valid IAP token with sub")
		}
		if p.Kind != model.PrincipalHuman || p.ID != "alice_sub" {
			t.Fatalf("verifiedAs got %+v, want alice_sub", p)
		}
	})

	t.Run("IAP JWT with wrong audience is rejected", func(t *testing.T) {
		tok := signIAPToken(t, privKey, kid, "different-aud", "user@example.com", "1001", ui.GoogleIAPIssuer, time.Now().Add(1*time.Hour))
		req, _ := http.NewRequest(http.MethodGet, "/api/tasks", nil)
		req = req.WithContext(ui.WithIAPConfig(req.Context(), iapCfg))
		req.Header.Set("X-Goog-IAP-JWT-Assertion", tok)

		_, ok := verifiedAs(req)
		if ok {
			t.Fatal("verifiedAs expected ok=false for audience mismatch")
		}
	})

	t.Run("expired IAP JWT is rejected", func(t *testing.T) {
		tok := signIAPToken(t, privKey, kid, aud, "user@example.com", "1001", ui.GoogleIAPIssuer, time.Now().Add(-1*time.Hour))
		req, _ := http.NewRequest(http.MethodGet, "/api/tasks", nil)
		req = req.WithContext(ui.WithIAPConfig(req.Context(), iapCfg))
		req.Header.Set("X-Goog-IAP-JWT-Assertion", tok)

		_, ok := verifiedAs(req)
		if ok {
			t.Fatal("verifiedAs expected ok=false for expired token")
		}
	})

	t.Run("forged IAP JWT with invalid signature is rejected", func(t *testing.T) {
		otherKey, _ := generateECDSATestKey(t)
		tok := signIAPToken(t, otherKey, kid, aud, "attacker@example.com", "9999", ui.GoogleIAPIssuer, time.Now().Add(1*time.Hour))
		req, _ := http.NewRequest(http.MethodGet, "/api/tasks", nil)
		req = req.WithContext(ui.WithIAPConfig(req.Context(), iapCfg))
		req.Header.Set("X-Goog-IAP-JWT-Assertion", tok)
		// Attacker also attaches a forged plaintext header
		req.Header.Set("X-Goog-Authenticated-User-Email", "attacker@example.com")

		_, ok := verifiedAs(req)
		if ok {
			t.Fatal("verifiedAs expected ok=false for forged signature")
		}
	})
}

func TestExtractPrincipal_IAPMode_ForgedPlaintextHeaders(t *testing.T) {
	iapCfg := ui.IAPConfig{
		Mode: true,
	}

	t.Run("plaintext headers ignored when IAP mode is active without JWT assertion", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "/api/tasks", nil)
		req = req.WithContext(ui.WithIAPConfig(req.Context(), iapCfg))
		req.Header.Set("X-Goog-Authenticated-User-Email", "accounts.google.com:admin@example.com")
		req.Header.Set("X-User-LDAP", "admin")
		req.Header.Set("X-Forwarded-User", "admin")
		req.Header.Set("X-Grain-User", "admin")

		_, ok := ui.ExtractPrincipal(req)
		if ok {
			t.Fatal("ExtractPrincipal expected ok=false when IAP mode is active and no JWT assertion is provided")
		}
	})

	t.Run("plaintext headers ignored even if request is from loopback when IAP mode is active", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "/api/tasks", nil)
		req = req.WithContext(ui.WithIAPConfig(req.Context(), iapCfg))
		req.RemoteAddr = "127.0.0.1:54321"
		req.Header.Set("X-Goog-Authenticated-User-Email", "accounts.google.com:admin@example.com")

		_, ok := ui.ExtractPrincipal(req)
		if ok {
			t.Fatal("ExtractPrincipal expected ok=false in IAP mode even on loopback")
		}
	})
}

func TestIAPAssertionKeysAreFetchedFromTheEndpoint(t *testing.T) {
	kid := "test-key-live"
	aud := "/projects/55555/apps/grain-proxy"
	privKey, pubKeyPEM := generateECDSATestKey(t)

	// Mock server that returns the key map
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys := map[string]string{
			kid: pubKeyPEM,
		}
		_ = json.NewEncoder(w).Encode(keys)
	}))
	defer mockServer.Close()

	ui.SetIAPCertURL(mockServer.URL)
	ui.SetIAPHTTPClient(mockServer.Client())
	ui.SetIAPAudience(aud)
	defer ui.ResetIAPConfig()

	tok := signIAPToken(t, privKey, kid, aud, "endpoint-user@example.com", "accounts.google.com:555", ui.GoogleIAPIssuer, time.Now().Add(30*time.Minute))
	req, _ := http.NewRequest(http.MethodGet, "/api/tasks", nil)
	req.Header.Set("X-Goog-IAP-JWT-Assertion", tok)

	p, ok := verifiedAs(req)
	if !ok {
		t.Fatal("verifiedAs expected ok=true when fetching keys from endpoint")
	}
	if p.Kind != model.PrincipalHuman || p.ID != "endpoint-user@example.com" {
		t.Fatalf("verifiedAs got %+v, want endpoint-user@example.com", p)
	}
}
