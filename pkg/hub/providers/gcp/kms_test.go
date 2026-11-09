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

package gcp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/hub"
	"github.com/ai-on-gke/grain/pkg/jwks"
)

func TestKMSSignerSignAndJWKSRoundTrip(t *testing.T) {
	ctx := context.Background()
	priv1, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey v1: %v", err)
	}
	priv2, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey v2: %v", err)
	}

	pubPEM := func(pub *ecdsa.PublicKey) string {
		der, err := x509.MarshalPKIXPublicKey(pub)
		if err != nil {
			t.Fatalf("MarshalPKIXPublicKey: %v", err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	}

	const keyResource = "projects/test-proj/locations/us-central1/keyRings/grain-reg/cryptoKeys/assertion-key"
	const ver1 = keyResource + "/cryptoKeyVersions/1"
	const ver2 = keyResource + "/cryptoKeyVersions/2"

	kmsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/cryptoKeyVersions"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"cryptoKeyVersions": []map[string]any{
					{"name": ver1, "state": "ENABLED"},
					{"name": ver2, "state": "ENABLED"},
				},
			})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/cryptoKeyVersions/1/publicKey"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"pem":       pubPEM(&priv1.PublicKey),
				"algorithm": "EC_SIGN_P256_SHA256",
			})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/cryptoKeyVersions/2/publicKey"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"pem":       pubPEM(&priv2.PublicKey),
				"algorithm": "EC_SIGN_P256_SHA256",
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, ":asymmetricSign"):
			var body struct {
				Digest struct {
					SHA256 string `json:"sha256"`
				} `json:"digest"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			digest, err := base64.StdEncoding.DecodeString(body.Digest.SHA256)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			key := priv2
			if strings.Contains(r.URL.Path, "/cryptoKeyVersions/1:") {
				key = priv1
			}
			rInt, sInt, err := ecdsa.Sign(rand.Reader, key, digest)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			derSig, err := asn1.Marshal(struct{ R, S *big.Int }{R: rInt, S: sInt})
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"signature": base64.StdEncoding.EncodeToString(derSig),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer kmsSrv.Close()

	now := time.Date(2026, 11, 5, 21, 0, 0, 0, time.UTC)
	signer, err := NewKMSSigner(ctx, KMSSignerOptions{
		KeyName:    keyResource,
		JWTTTL:     60 * time.Second,
		BaseURL:    kmsSrv.URL,
		HTTPClient: kmsSrv.Client(),
		Now:        func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewKMSSigner: %v", err)
	}

	jwksDoc, err := signer.JWKS(ctx)
	if err != nil {
		t.Fatalf("JWKS: %v", err)
	}
	if len(jwksDoc.Keys) != 2 {
		t.Fatalf("expected 2 keys in JWKS (v1 and v2), got %d", len(jwksDoc.Keys))
	}

	token, err := signer.SignAssertion(ctx, hub.RegistryClaims{
		Audience:  "grain-instance:alice-dev",
		Subject:   "sub-alice",
		Email:     "alice@example.com",
		Kind:      "human",
		RequestID: "req-123",
	})
	if err != nil {
		t.Fatalf("SignAssertion: %v", err)
	}

	claims, err := hub.VerifyRegistryAssertion(token, jwksDoc, "grain-instance:alice-dev", now)
	if err != nil {
		t.Fatalf("VerifyRegistryAssertion: %v", err)
	}
	if claims.Email != "alice@example.com" || claims.Subject != "sub-alice" || claims.RequestID != "req-123" {
		t.Fatalf("unexpected verified claims: %+v", claims)
	}
	if claims.ExpiresAt-claims.IssuedAt != 60 {
		t.Fatalf("expected 60s TTL, got %ds", claims.ExpiresAt-claims.IssuedAt)
	}
	if claims.IssuedAt-claims.NotBefore != 30 {
		t.Fatalf("expected 30s nbf skew, got %ds", claims.IssuedAt-claims.NotBefore)
	}

	// Also verify compatibility with pkg/jwks.Fetch (used by instance-side JWT verification).
	jwksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwksDoc)
	}))
	defer jwksSrv.Close()

	fetchedKeys, err := jwks.Fetch(ctx, jwksSrv.Client(), jwksSrv.URL, "registry")
	if err != nil {
		t.Fatalf("jwks.Fetch: %v", err)
	}
	if len(fetchedKeys) != 2 {
		t.Fatalf("expected 2 keys from jwks.Fetch, got %d", len(fetchedKeys))
	}
}
