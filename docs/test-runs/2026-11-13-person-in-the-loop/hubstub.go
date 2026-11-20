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

// hubstub stands in for Grain Hub as an identity provider -- the one
// boundary a sandbox has no verified-person source for. It publishes an
// ES256 JWK set at /keys and mints X-Grain-Hub-Assertion tokens at
// /mint?email=<addr>, so a real daemon started with -ui-hub-issuer,
// -ui-hub-audience and -ui-hub-keys-url sees requests carrying them as a
// verified ("registry") person.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:4286", "listen address")
	issuer := flag.String("issuer", "https://hub.example.com", "issuer claim")
	audience := flag.String("audience", "http://127.0.0.1:4280", "audience claim")
	flag.Parse()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	n := (key.Curve.Params().BitSize + 7) / 8
	jwks := map[string]any{"keys": []map[string]string{{
		"kty": "EC", "crv": "P-256", "alg": "ES256", "use": "sig", "kid": "walk-1",
		"x": base64.RawURLEncoding.EncodeToString(key.X.FillBytes(make([]byte, n))),
		"y": base64.RawURLEncoding.EncodeToString(key.Y.FillBytes(make([]byte, n))),
	}}}
	http.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwks)
	})
	http.HandleFunc("/mint", func(w http.ResponseWriter, r *http.Request) {
		email := r.URL.Query().Get("email")
		now := time.Now()
		tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
			"iss": *issuer, "aud": []string{*audience}, "sub": email, "email": email,
			"email_verified": true, "kind": "human",
			"iat": now.Unix(), "exp": now.Add(4 * time.Minute).Unix(),
		})
		tok.Header["kid"] = "walk-1"
		s, err := tok.SignedString(key)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_, _ = w.Write([]byte(s))
	})
	log.Fatal(http.ListenAndServe(*addr, nil))
}
