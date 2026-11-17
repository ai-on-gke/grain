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

//go:build ignore

// jwks_signer.go serves a local ES256 JWK set for -ui-iap-keys-url and writes
// signed X-Goog-IAP-JWT-Assertion tokens for admin@example.com,
// limited@example.com and ci-only@example.com so curl can authenticate to a
// live grain daemon as verified principals with distinct RBAC roles.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"log"
	"math/big"
	"net/http"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:4291", "listen address")
	audience := flag.String("audience", "/projects/424242/global/backendServices/1", "IAP audience")
	issuer := flag.String("issuer", "https://iap.example.com", "IAP issuer")
	out := flag.String("out", "", "path to write JSON map of email -> signed JWT")
	flag.Parse()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatalf("generate key: %v", err)
	}
	const kid = "walkthrough-iap-key-1"

	b := func(n *big.Int) string {
		return base64.RawURLEncoding.EncodeToString(n.FillBytes(make([]byte, 32)))
	}
	jwksBody, err := json.Marshal(map[string]any{
		"keys": []map[string]string{{
			"kty": "EC",
			"crv": "P-256",
			"kid": kid,
			"alg": "ES256",
			"x":   b(priv.PublicKey.X),
			"y":   b(priv.PublicKey.Y),
		}},
	})
	if err != nil {
		log.Fatalf("marshal jwks: %v", err)
	}

	sign := func(email string) string {
		now := time.Now()
		tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
			"iss":   *issuer,
			"aud":   *audience,
			"sub":   "accounts.example.com:" + email,
			"email": email,
			"iat":   now.Add(-time.Minute).Unix(),
			"exp":   now.Add(2 * time.Hour).Unix(),
		})
		tok.Header["kid"] = kid
		s, err := tok.SignedString(priv)
		if err != nil {
			log.Fatalf("sign %s: %v", email, err)
		}
		return s
	}

	if *out != "" {
		tokens := map[string]string{
			"admin@example.com":   sign("admin@example.com"),
			"limited@example.com": sign("limited@example.com"),
			"ci-only@example.com": sign("ci-only@example.com"),
		}
		data, err := json.MarshalIndent(tokens, "", "  ")
		if err != nil {
			log.Fatalf("marshal tokens: %v", err)
		}
		if err := os.WriteFile(*out, data, 0o600); err != nil {
			log.Fatalf("write %s: %v", *out, err)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwksBody)
	})
	log.Printf("jwks_signer listening on %s", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
