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

package jwks

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func rsaJWK(t *testing.T, kid string, bits int) map[string]string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return map[string]string{
		"kty": "RSA", "kid": kid,
		"n": enc(key.N.Bytes()), "e": enc(big.NewInt(int64(key.E)).Bytes()),
	}
}

func rsaPEM(t *testing.T, bits int) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func serve(t *testing.T, body any) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// An RSA key too small to trust is dropped, from a JWK set and from a
// map of PEM keys alike (GSA1-19).
func TestFetchDropsSmallRSAKeys(t *testing.T) {
	ctx := context.Background()
	set := serve(t, map[string]any{"keys": []map[string]string{rsaJWK(t, "small", 1024), rsaJWK(t, "big", 2048)}})
	keys, err := Fetch(ctx, nil, set.URL, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["small"]; ok {
		t.Error("kept a 1024-bit RSA key from a JWK set")
	}
	if _, ok := keys["big"]; !ok {
		t.Error("dropped a 2048-bit RSA key from a JWK set")
	}

	pems := serve(t, map[string]string{"small": rsaPEM(t, 1024), "big": rsaPEM(t, 2048)})
	if keys, err = Fetch(ctx, nil, pems.URL, "test"); err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["small"]; ok {
		t.Error("kept a 1024-bit PEM RSA key")
	}
	if _, ok := keys["big"]; !ok {
		t.Error("dropped a 2048-bit PEM RSA key")
	}
}

// A key set fetched over https is never followed onto plain http, where
// anybody on the path could serve keys of their own (GSA1-19); a
// redirect that stays on https is followed.
func TestFetchRefusesARedirectOffHTTPS(t *testing.T) {
	ctx := context.Background()
	plain := serve(t, map[string]any{"keys": []map[string]string{rsaJWK(t, "k", 2048)}})
	var secure *httptest.Server
	secure = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/downgrade":
			http.Redirect(w, r, plain.URL, http.StatusFound)
		case "/moved":
			http.Redirect(w, r, secure.URL+"/keys", http.StatusFound)
		default:
			json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{rsaJWK(t, "k", 2048)}})
		}
	}))
	t.Cleanup(secure.Close)

	if _, err := Fetch(ctx, secure.Client(), secure.URL+"/downgrade", "test"); err == nil || !strings.Contains(err.Error(), "not https") {
		t.Errorf("Fetch through an https->http redirect: %v, want it refused", err)
	}
	if keys, err := Fetch(ctx, secure.Client(), secure.URL+"/moved", "test"); err != nil || keys["k"] == nil {
		t.Errorf("Fetch through an https->https redirect: %v, %v", keys, err)
	}
}

type unavailableErr interface {
	error
	Unavailable() bool
}

func isUnavailable(err error) bool {
	var u unavailableErr
	return errors.As(err, &u) && u.Unavailable()
}

// Transport errors (except caller-cancelled contexts and HTTPS downgrade
// redirects) and upstream 5xx/429 responses mark the error as Unavailable so
// the UI/API returns 503 Service Unavailable while the key server is down,
// whereas 4xx responses, bad JSON and empty key sets stay plain errors (401).
func TestFetchMarksTransportAnd5xxErrorsAsUnavailable(t *testing.T) {
	ctx := context.Background()

	// Closed local TCP port: dial connection refused -> Unavailable.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedURL := "http://" + ln.Addr().String() + "/jwks"
	_ = ln.Close()
	if _, err := Fetch(ctx, nil, closedURL, "IAP"); !isUnavailable(err) {
		t.Errorf("closed port: err = %v, want Unavailable", err)
	}

	// Caller cancelled its own context -> not a key-server outage.
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Fetch(canceledCtx, nil, closedURL, "IAP"); err == nil || isUnavailable(err) {
		t.Errorf("canceled caller context: err = %v, want non-Unavailable error", err)
	}

	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusTooManyRequests} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		_, err := Fetch(ctx, srv.Client(), srv.URL, "IAP")
		srv.Close()
		if !isUnavailable(err) {
			t.Errorf("status %d: err = %v, want Unavailable", status, err)
		}
	}

	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"404", http.StatusNotFound, "not found"},
		{"403", http.StatusForbidden, "forbidden"},
		{"malformed-json", http.StatusOK, "{not-json"},
		{"empty-keys", http.StatusOK, `{"keys":[]}`},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		_, err := Fetch(ctx, srv.Client(), srv.URL, "IAP")
		srv.Close()
		if err == nil || isUnavailable(err) {
			t.Errorf("%s: err = %v, want non-Unavailable error", tc.name, err)
		}
	}

	// HTTPS -> HTTP downgrade redirect stays a 401 refusal, never 503.
	plain := serve(t, map[string]any{"keys": []map[string]string{rsaJWK(t, "k", 2048)}})
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL, http.StatusFound)
	}))
	defer secure.Close()
	if _, err := Fetch(ctx, secure.Client(), secure.URL, "IAP"); err == nil || isUnavailable(err) {
		t.Errorf("https->http downgrade: err = %v, want non-Unavailable error", err)
	}
}
