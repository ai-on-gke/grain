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

// iapstub is the walkthrough's stand-in for the identity-aware proxy that
// sits in front of grain-registry's main listener in production. It
//
//   - publishes an ES256 JWK set at GET /jwks (on -jwks-addr), which the
//     registry's `auth.iap_certs_url` points at; and
//   - reverse-proxies every request on -addr to -upstream (preserving the
//     Host header so subdomain routing still sees <entry>.<instance_domain>,
//     and passing WebSocket upgrades through), replacing any caller-supplied
//     X-Goog-IAP-JWT-Assertion with one it signs itself for the identity named
//     by `X-Test-Email: <email>` or by the `email` claim of an unsigned
//     `Authorization: Bearer <hdr>.<claims>.<sig>` token. Requests with no
//     identity are forwarded with no assertion, so the registry rejects them.
//
// Run with: go run ./docs/test-runs/2026-11-07-grain-registry/iapstub.go \
//
//	-key /tmp/grwalk/iap.pem -addr 127.0.0.1:4251 -jwks-addr 127.0.0.1:4250 \
//	-upstream http://127.0.0.1:4252 -audience /projects/walk/backendServices/registry
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"flag"
	"log"
	"math/big"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"
)

func loadOrCreateKey(path string) *ecdsa.PrivateKey {
	if b, err := os.ReadFile(path); err == nil {
		blk, _ := pem.Decode(b)
		k, err := x509.ParseECPrivateKey(blk.Bytes)
		if err != nil {
			log.Fatalf("parse %s: %v", path, err)
		}
		return k
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	der, _ := x509.MarshalECPrivateKey(k)
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		log.Fatal(err)
	}
	return k
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func pad32(n *big.Int) []byte {
	out := make([]byte, 32)
	n.FillBytes(out)
	return out
}

func main() {
	keyPath := flag.String("key", "", "PEM EC key path (created if missing)")
	addr := flag.String("addr", "", "front proxy listen address (empty: JWKS only)")
	jwksAddr := flag.String("jwks-addr", "", "JWKS listen address (empty: none)")
	upstream := flag.String("upstream", "", "registry main listener URL")
	audience := flag.String("audience", "", "IAP audience to sign")
	issuer := flag.String("issuer", "https://cloud.google.com/iap", "IAP issuer to sign")
	forceHost := flag.String("force-host", "", "rewrite every request's Host (lets a plain `grain --server` client reach <entry>.<instance_domain>)")
	as := flag.String("as", "", "identity to assert when the request names none")
	flag.Parse()

	key := loadOrCreateKey(*keyPath)
	pubDER, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	sum := sha256.Sum256(pubDER)
	kid := b64(sum[:8])

	sign := func(email string) string {
		hdr, _ := json.Marshal(map[string]string{"alg": "ES256", "typ": "JWT", "kid": kid})
		now := time.Now()
		hd := ""
		if i := strings.LastIndexByte(email, '@'); i >= 0 {
			hd = email[i+1:]
		}
		claims, _ := json.Marshal(map[string]any{
			"iss": *issuer, "aud": *audience, "sub": "accounts.google.com:" + email,
			"email": email, "hd": hd, "iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(),
		})
		signing := b64(hdr) + "." + b64(claims)
		h := sha256.Sum256([]byte(signing))
		r, s, err := ecdsa.Sign(rand.Reader, key, h[:])
		if err != nil {
			log.Fatal(err)
		}
		return signing + "." + b64(append(pad32(r), pad32(s)...))
	}

	if *jwksAddr != "" {
		mux := http.NewServeMux()
		mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
				"kty": "EC", "crv": "P-256", "alg": "ES256", "use": "sig", "kid": kid,
				"x": b64(pad32(key.PublicKey.X)), "y": b64(pad32(key.PublicKey.Y)),
			}}})
		})
		go func() { log.Fatal(http.ListenAndServe(*jwksAddr, mux)) }()
		log.Printf("iapstub: JWKS on http://%s/jwks (kid %s)", *jwksAddr, kid)
	}
	if *addr == "" {
		select {}
	}
	up, err := url.Parse(*upstream)
	if err != nil {
		log.Fatal(err)
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(up)
			pr.Out.Host = pr.In.Host // keep <entry>.<instance_domain> for subdomain routing
			if *forceHost != "" {
				pr.Out.Host = *forceHost
			}
			pr.Out.Header.Del("X-Goog-IAP-JWT-Assertion")
			email := strings.TrimSpace(pr.In.Header.Get("X-Test-Email"))
			if email == "" {
				if a := pr.In.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
					parts := strings.Split(strings.TrimPrefix(a, "Bearer "), ".")
					if len(parts) == 3 {
						if raw, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
							var c struct {
								Email string `json:"email"`
							}
							if json.Unmarshal(raw, &c) == nil {
								email = c.Email
							}
						}
					}
				}
			}
			if email == "" {
				email = *as
			}
			pr.Out.Header.Del("X-Test-Email")
			if email != "" {
				pr.Out.Header.Set("X-Goog-IAP-JWT-Assertion", sign(email))
			}
		},
		FlushInterval: -1,
	}
	log.Printf("iapstub: proxy %s -> %s", *addr, *upstream)
	log.Fatal(http.ListenAndServe(*addr, rp))
}
