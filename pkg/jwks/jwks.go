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

// Package jwks fetches and caches the public keys an issuer signs its
// JWTs with: a JWK set ({"keys": [...]}, RFC 7517) or Google's own map
// of key ID to PEM public key (IAP's https://www.gstatic.com/iap/verify/
// public_key). EC P-256 and RSA keys are kept; anything else is skipped.
//
// The relay verifies IAP assertions with it.
package jwks

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"time"
)

// refusingDowngrades is client, refusing to follow a redirect off https:
// keys fetched over https and then served over plain http are keys
// anybody on the path could have swapped (GSA1-19). Whatever client
// already refused, it still refuses.
func refusingDowngrades(client *http.Client) *http.Client {
	c := *client
	next := c.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		for _, prev := range via {
			if prev.URL.Scheme == "https" && req.URL.Scheme != "https" {
				return fmt.Errorf("refusing a redirect from %s to %s: it is not https", prev.URL.Redacted(), req.URL.Redacted())
			}
		}
		if next != nil {
			return next(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &c
}

// minRSABits is the smallest RSA modulus a published key may have.
const minRSABits = 2048

// Fetch reads the keys published at url. name says whose keys they are
// in an error.
func Fetch(ctx context.Context, client *http.Client, url, name string) (map[string]crypto.PublicKey, error) {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	client = refusingDowngrades(client)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s public keys: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching %s public keys: unexpected status %d", name, resp.StatusCode)
	}
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decoding %s public keys: %w", name, err)
	}

	keys := map[string]crypto.PublicKey{}
	if set, ok := raw["keys"]; ok {
		var jwks []jsonWebKey
		if err := json.Unmarshal(set, &jwks); err != nil {
			return nil, fmt.Errorf("decoding JWK set: %w", err)
		}
		for _, k := range jwks {
			if pub, err := k.publicKey(); err == nil && k.Kid != "" {
				keys[k.Kid] = pub
			}
		}
	} else {
		for kid, msg := range raw {
			var pemStr string
			if json.Unmarshal(msg, &pemStr) != nil {
				continue
			}
			block, _ := pem.Decode([]byte(pemStr))
			if block == nil {
				continue
			}
			pub, err := x509.ParsePKIXPublicKey(block.Bytes)
			if err != nil {
				continue
			}
			switch pub := pub.(type) {
			case *ecdsa.PublicKey:
				keys[kid] = pub
			case *rsa.PublicKey:
				if pub.N.BitLen() >= minRSABits {
					keys[kid] = pub
				}
			}
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no usable public keys found at the %s key endpoint", name)
	}
	return keys, nil
}

// jsonWebKey is the part of an RFC 7517 key Fetch reads.
type jsonWebKey struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func (k jsonWebKey) publicKey() (crypto.PublicKey, error) {
	dec := base64.RawURLEncoding.DecodeString
	switch k.Kty {
	case "EC":
		if k.Crv != "P-256" {
			return nil, fmt.Errorf("unsupported curve %q", k.Crv)
		}
		x, err := dec(k.X)
		if err != nil {
			return nil, err
		}
		y, err := dec(k.Y)
		if err != nil {
			return nil, err
		}
		pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		if !pub.Curve.IsOnCurve(pub.X, pub.Y) {
			return nil, errors.New("EC key is not on its curve")
		}
		return pub, nil
	case "RSA":
		n, err := dec(k.N)
		if err != nil {
			return nil, err
		}
		e, err := dec(k.E)
		if err != nil {
			return nil, err
		}
		exp := new(big.Int).SetBytes(e)
		if !exp.IsInt64() || exp.Int64() < 3 || exp.Int64() > 1<<31 {
			return nil, errors.New("RSA key has an unusable exponent")
		}
		mod := new(big.Int).SetBytes(n)
		if mod.BitLen() < minRSABits {
			return nil, fmt.Errorf("RSA key has a %d-bit modulus, under %d", mod.BitLen(), minRSABits)
		}
		return &rsa.PublicKey{N: mod, E: int(exp.Int64())}, nil
	}
	return nil, fmt.Errorf("unsupported key type %q", k.Kty)
}
