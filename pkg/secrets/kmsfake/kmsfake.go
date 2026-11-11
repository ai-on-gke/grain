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

// Package kmsfake is a stand-in for Cloud KMS's encrypt and decrypt
// REST methods, for tests of the KMS-wrapped secrets key. Start points
// GRAIN_KMS_ENDPOINT and GRAIN_KMS_TOKEN at it for the test's duration,
// so a store, a daemon or a CLI subprocess reaches it unconfigured.
package kmsfake

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Server is the fake. Ciphertexts are opaque handles into its memory,
// each remembering the plaintext, the AAD and the version that made it.
type Server struct {
	srv *httptest.Server

	mu       sync.Mutex
	primary  int
	blobs    map[string]blob
	deny     bool
	corrupt  bool
	decrypts int
	encrypts int
}

type blob struct {
	key, plaintext, aad string
	version             int
}

// Start runs a fake and points this process's KMS client at it.
func Start(t testing.TB) *Server {
	t.Helper()
	f := &Server{primary: 1, blobs: map[string]blob{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	t.Setenv("GRAIN_KMS_ENDPOINT", f.URL())
	t.Setenv("GRAIN_KMS_TOKEN", "fake-kms-token")
	return f
}

// URL is the fake's REST root, what GRAIN_KMS_ENDPOINT is set to.
func (f *Server) URL() string { return f.srv.URL + "/v1/" }

// Close takes the fake down, so that calls fail as unreachable.
func (f *Server) Close() { f.srv.Close() }

// Rotate makes a new primary version: blobs made before it decrypt with
// usedPrimary false.
func (f *Server) Rotate() { f.mu.Lock(); f.primary++; f.mu.Unlock() }

// Deny makes every call answer 403, as KMS does once IAM is revoked.
func (f *Server) Deny(on bool) { f.mu.Lock(); f.deny = on; f.mu.Unlock() }

// CorruptDecrypts makes decrypt answer a plaintext other than the one
// encrypted.
func (f *Server) CorruptDecrypts(on bool) { f.mu.Lock(); f.corrupt = on; f.mu.Unlock() }

// Decrypts and Encrypts count the calls that reached the fake.
func (f *Server) Decrypts() int { f.mu.Lock(); defer f.mu.Unlock(); return f.decrypts }
func (f *Server) Encrypts() int { f.mu.Lock(); defer f.mu.Unlock(); return f.encrypts }

func (f *Server) serve(w http.ResponseWriter, r *http.Request) {
	name, method, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v1/"), ":")
	if !ok || r.Method != http.MethodPost {
		apiError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Header.Get("Authorization") != "Bearer fake-kms-token" {
		apiError(w, http.StatusUnauthorized, "no credential")
		return
	}
	var in map[string]string
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		apiError(w, http.StatusBadRequest, "bad body")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch method {
	case "encrypt":
		f.encrypts++
	case "decrypt":
		f.decrypts++
	}
	if f.deny {
		apiError(w, http.StatusForbidden, "Permission 'cloudkms.cryptoKeyVersions.useToDecrypt' denied on resource '"+name+"'")
		return
	}
	switch method {
	case "encrypt":
		var id [16]byte
		_, _ = rand.Read(id[:])
		handle := hex.EncodeToString(id[:])
		f.blobs[handle] = blob{key: name, plaintext: in["plaintext"], aad: in["additionalAuthenticatedData"], version: f.primary}
		writeJSON(w, map[string]any{"name": name + "/cryptoKeyVersions/1", "ciphertext": base64.StdEncoding.EncodeToString([]byte(handle))})
	case "decrypt":
		raw, _ := base64.StdEncoding.DecodeString(in["ciphertext"])
		b, ok := f.blobs[string(raw)]
		if !ok || b.key != name || b.aad != in["additionalAuthenticatedData"] {
			apiError(w, http.StatusBadRequest, "Decryption failed: the ciphertext is invalid.")
			return
		}
		pt := b.plaintext
		if f.corrupt {
			var other [32]byte
			_, _ = rand.Read(other[:])
			pt = base64.StdEncoding.EncodeToString(other[:])
		}
		out := map[string]any{"plaintext": pt}
		if b.version == f.primary {
			out["usedPrimary"] = true
		}
		writeJSON(w, out)
	default:
		apiError(w, http.StatusNotFound, "no method "+method)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func apiError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": status, "message": msg}})
}
