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

// iap_keys serves an ES256 IAP public-key map at /keys and mints signed
// X-Goog-IAP-JWT-Assertion tokens at /mint?email=<addr> for live RBAC tests:
//
//	go build -o /var/tmp/iap_keys docs/test-runs/2026-11-11-merge-gates/iap_keys.go
//	/var/tmp/iap_keys -addr 127.0.0.1:4283 -aud walk-aud -iss https://cloud.google.com/iap
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/ai-on-gke/grain/pkg/webauth"
	"github.com/golang-jwt/jwt/v5"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:4283", "listen address")
	aud := flag.String("aud", "walk-aud", "JWT audience")
	iss := flag.String("iss", "https://cloud.google.com/iap", "JWT issuer")
	flag.Parse()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		log.Fatal(err)
	}
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))

	mux := http.NewServeMux()
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"ec-1": pubPEM})
	})
	mux.HandleFunc("/mint", func(w http.ResponseWriter, r *http.Request) {
		email := r.URL.Query().Get("email")
		claims := webauth.IAPClaims{
			RegisteredClaims: jwt.RegisteredClaims{
				Issuer:    *iss,
				Audience:  jwt.ClaimStrings{*aud},
				Subject:   "accounts.google.com:" + email,
				IssuedAt:  jwt.NewNumericDate(time.Now().Add(-time.Minute)),
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			},
			Email: email,
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
		tok.Header["kid"] = "ec-1"
		signed, err := tok.SignedString(priv)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, signed)
	})
	log.Fatal(http.ListenAndServe(*addr, mux))
}
