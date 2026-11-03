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

// iapstub stands in for Google's IAP in the 2026-11-02 verified-identity
// walkthrough: it publishes a JWK set and signs ES256 assertions with the
// matching private key, so a real `grain daemon -ui-iap-keys-url=...` can
// be sent X-Goog-IAP-JWT-Assertion headers it has to verify.
//
//	go run iapstub.go keygen -dir D            # writes D/k1.pem (published) and D/k2.pem (never published)
//	go run iapstub.go serve -dir D -addr A     # GET /jwks serves k1's public key as a JWK set
//	go run iapstub.go sign -dir D -email E -aud AUD -iss ISS [-kid k1] [-key k1] [-exp 1h] [-iat 0] [-badsig]
//
// sign prints the compact JWT. -kid is the header's kid, -key the file it is
// signed with, so -kid k1 -key k2 is a token that names a published key but
// was signed by another one; -badsig flips a byte of the signature instead.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: iapstub keygen|serve|sign ...")
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	dir := fs.String("dir", ".", "key directory")
	addr := fs.String("addr", "127.0.0.1:0", "serve: listen address")
	email := fs.String("email", "", "sign: email claim")
	aud := fs.String("aud", "", "sign: audience claim")
	iss := fs.String("iss", "https://cloud.google.com/iap", "sign: issuer claim")
	kid := fs.String("kid", "k1", "sign: kid header")
	key := fs.String("key", "", "sign: key file to sign with (defaults to -kid)")
	exp := fs.Duration("exp", time.Hour, "sign: expiry relative to now (negative for an expired token)")
	iat := fs.Duration("iat", 0, "sign: issued-at relative to now")
	badsig := fs.Bool("badsig", false, "sign: corrupt the signature")
	_ = fs.Parse(os.Args[2:])

	switch os.Args[1] {
	case "keygen":
		for _, k := range []string{"k1", "k2"} {
			priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			check(err)
			der, err := x509.MarshalECPrivateKey(priv)
			check(err)
			check(os.WriteFile(filepath.Join(*dir, k+".pem"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600))
		}
	case "serve":
		pub := load(*dir, "k1").PublicKey
		b64 := base64.RawURLEncoding.EncodeToString
		jwks, _ := json.Marshal(map[string]any{"keys": []map[string]string{{
			"kty": "EC", "crv": "P-256", "alg": "ES256", "use": "sig", "kid": "k1",
			"x": b64(pub.X.FillBytes(make([]byte, 32))), "y": b64(pub.Y.FillBytes(make([]byte, 32))),
		}}})
		http.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
			log.Printf("GET %s", r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(jwks)
		})
		log.Printf("serving JWKS on http://%s/jwks", *addr)
		log.Fatal(http.ListenAndServe(*addr, nil))
	case "sign":
		if *key == "" {
			*key = *kid
		}
		now := time.Now()
		claims := jwt.MapClaims{
			"email": *email, "sub": "accounts.google.com:" + *email,
			"aud": *aud, "iss": *iss,
			"iat": now.Add(*iat).Unix(), "exp": now.Add(*exp).Unix(),
		}
		t := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
		t.Header["kid"] = *kid
		s, err := t.SignedString(load(*dir, *key))
		check(err)
		if *badsig {
			i := strings.LastIndexByte(s, '.') + 5
			c := byte('A')
			if s[i] == 'A' {
				c = 'B'
			}
			s = s[:i] + string(c) + s[i+1:]
		}
		fmt.Println(s)
	default:
		log.Fatalf("unknown command %q", os.Args[1])
	}
}

func load(dir, name string) *ecdsa.PrivateKey {
	b, err := os.ReadFile(filepath.Join(dir, name+".pem"))
	check(err)
	blk, _ := pem.Decode(b)
	if blk == nil {
		log.Fatalf("%s: no PEM block", name)
	}
	k, err := x509.ParseECPrivateKey(blk.Bytes)
	check(err)
	return k
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
