//go:build ignore

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

// regsim is a loopback stand-in for grain-registry's assertion signer: it
// publishes an ES256 JWKS and mints X-Grain-Registry-Assertion JWTs, with an
// optional act claim, so the walkthrough can present delegated
// (person via delegate:<app>) sessions to a real daemon.
//
//	go run regsim.go -addr 127.0.0.1:18380 -iss https://registry.test.local -aud https://grain.test.local
//	curl '127.0.0.1:18380/sign?email=alice@example.com&act=bot@my-proj.iam.gserviceaccount.com&access=write'
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
	addr := flag.String("addr", "", "listener (JWKS and signer), host:port; required")
	iss := flag.String("iss", "https://registry.test.local", "issuer the assertions carry")
	aud := flag.String("aud", "https://grain.test.local", "audience the assertions carry")
	flag.Parse()
	if *addr == "" {
		log.Fatal("regsim: -addr host:port is required (walkthrough.sh passes it)")
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	const kid = "regsim-1"
	b64 := base64.RawURLEncoding.EncodeToString
	pad := func(b []byte) []byte {
		out := make([]byte, 32)
		copy(out[32-len(b):], b)
		return out
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "EC", "crv": "P-256", "alg": "ES256", "use": "sig", "kid": kid,
			"x": b64(pad(key.PublicKey.X.Bytes())), "y": b64(pad(key.PublicKey.Y.Bytes())),
		}}})
	})
	mux.HandleFunc("GET /sign", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		now := time.Now()
		claims := jwt.MapClaims{
			"iss":   *iss,
			"aud":   *aud,
			"sub":   q.Get("email"),
			"email": q.Get("email"),
			"kind":  "human",
			"iat":   now.Unix(),
			"exp":   now.Add(4 * time.Minute).Unix(),
			"jti":   "regsim-" + now.Format("150405.000000000"),
		}
		if k := q.Get("kind"); k != "" {
			claims["kind"] = k
		}
		if act := q.Get("act"); act != "" {
			access := q.Get("access")
			if access == "" {
				access = "write"
			}
			claims["act"] = map[string]string{"sub": act, "kind": "service", "app": q.Get("app"), "access": access}
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
		tok.Header["kid"] = kid
		s, err := tok.SignedString(key)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(s))
	})
	log.Printf("regsim on %s (iss %s, aud %s)", *addr, *iss, *aud)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
