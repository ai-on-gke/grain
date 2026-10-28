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

package pkgcache

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestV1StatsAndFlush(t *testing.T) {
	cache, err := NewCache(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	w, err := cache.NewWriter(KindNPM, "https://registry.npmjs.org/left-pad/-/left-pad-1.3.0.tgz")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("tarball-bytes"))
	if !w.Commit(Meta{URL: "https://registry.npmjs.org/left-pad/-/left-pad-1.3.0.tgz"}) {
		t.Fatal("commit failed")
	}

	h := NewV1Handler(cache)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/pkg/v1/stats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /pkg/v1/stats = %d: %s", rec.Code, rec.Body.String())
	}
	var stats V1Stats
	if err := json.Unmarshal(rec.Body.Bytes(), &stats); err != nil {
		t.Fatal(err)
	}
	if !stats.Enabled || stats.Entries != 1 {
		t.Errorf("stats = %+v, want Enabled=true Entries=1", stats)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/pkg/v1/flush?kind=npm", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /pkg/v1/flush?kind=npm = %d: %s", rec.Code, rec.Body.String())
	}
	var flushed V1Flush
	if err := json.Unmarshal(rec.Body.Bytes(), &flushed); err != nil {
		t.Fatal(err)
	}
	if flushed.Kind != "npm" || flushed.Entries != 1 || flushed.Bytes == 0 {
		t.Errorf("flushed = %+v, want Kind=npm Entries=1 Bytes>0", flushed)
	}
	if cache.Entries() != 0 {
		t.Errorf("cache.Entries() after flush = %d, want 0", cache.Entries())
	}

	// Unknown kind answers 400.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/pkg/v1/flush?kind=bogus", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST /pkg/v1/flush?kind=bogus = %d, want 400", rec.Code)
	}
}

func TestJobPodTokenIdentificationAndReadOnly(t *testing.T) {
	cache, err := NewCache(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	tokens := NewJobTokenSet(true)
	tokens.Register("tok-open", JobAccess{Job: "tests/go"})
	tokens.Register("tok-ro", JobAccess{Job: "tests/isolated", ReadOnly: true})

	srv := NewServer(Config{Cache: cache, JobTokens: tokens})

	// Unidentified caller is refused with 401.
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/gocache/action/0123456789abcdef", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated request = %d, want 401", rec.Code)
	}

	// ReadOnly job token can GET, cannot PUT.
	getReq := httptest.NewRequest(http.MethodGet, "/gocache/action/0123456789abcdef", nil)
	getReq.Header.Set("Grit-Job-Token", "tok-ro")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, getReq)
	if rec.Code != http.StatusNotFound {
		t.Errorf("ReadOnly GET = %d, want 404 miss", rec.Code)
	}

	putBody, _ := json.Marshal(GoCacheAction{OutputID: strings.Repeat("a", 64), Size: 4})
	putReq := httptest.NewRequest(http.MethodPut, "/gocache/action/0123456789abcdef", bytes.NewReader(putBody))
	putReq.Header.Set("Grit-Job-Token", "tok-ro")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, putReq)
	if rec.Code != http.StatusForbidden {
		t.Errorf("ReadOnly PUT = %d, want 403", rec.Code)
	}

	// Open job token can PUT.
	putReq = httptest.NewRequest(http.MethodPut, "/gocache/action/0123456789abcdef", bytes.NewReader(putBody))
	putReq.Header.Set("Grit-Job-Token", "tok-open")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, putReq)
	if rec.Code != http.StatusOK {
		t.Errorf("open PUT = %d: %s", rec.Code, rec.Body.String())
	}

	// Revoking the token cuts off further access.
	tokens.Revoke("tok-open")
	getReq = httptest.NewRequest(http.MethodGet, "/gocache/action/0123456789abcdef", nil)
	getReq.Header.Set("Grit-Job-Token", "tok-open")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, getReq)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("revoked token GET = %d, want 401", rec.Code)
	}
}

func TestGoCacheTrustSplitPreventsPoisoningTrustedBuilds(t *testing.T) {
	cache, err := NewCache(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	tokens := NewJobTokenSet(true)
	tokens.Register("tok-trusted", JobAccess{Job: "release/build", Trusted: true})
	tokens.Register("tok-untrusted", JobAccess{Job: "pr/test", Trusted: false})

	srv := NewServer(Config{Cache: cache, JobTokens: tokens})

	poisonAction := "aaaa111122223333"
	poisonOut := strings.Repeat("b", 64)
	body, _ := json.Marshal(GoCacheAction{OutputID: poisonOut, Size: 10})

	// Untrusted job writes an action entry, even attempting to use the
	// /gocache/trusted/ path and Grit-Cache-Trust header to escalate.
	putReq := httptest.NewRequest(http.MethodPut, "/gocache/trusted/action/"+poisonAction, bytes.NewReader(body))
	putReq.Header.Set("Grit-Job-Token", "tok-untrusted")
	putReq.Header.Set("Grit-Cache-Trust", "trusted")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, putReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("untrusted PUT = %d: %s", rec.Code, rec.Body.String())
	}

	// Untrusted job reads its own entry back.
	getUntrusted := httptest.NewRequest(http.MethodGet, "/gocache/action/"+poisonAction, nil)
	getUntrusted.Header.Set("Grit-Job-Token", "tok-untrusted")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, getUntrusted)
	if rec.Code != http.StatusOK {
		t.Fatalf("untrusted GET = %d, want 200", rec.Code)
	}

	// Trusted job NEVER sees the untrusted action entry.
	getTrusted := httptest.NewRequest(http.MethodGet, "/gocache/action/"+poisonAction, nil)
	getTrusted.Header.Set("Grit-Job-Token", "tok-trusted")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, getTrusted)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("trusted GET of untrusted entry = %d, want 404", rec.Code)
	}

	// Trusted job writes a trusted action entry; both trusted and untrusted
	// jobs read the trusted entry.
	cleanAction := "cccc444455556666"
	cleanBytes := []byte("verified-compiled-output")
	sum := sha256.Sum256(cleanBytes)
	cleanOut := hex.EncodeToString(sum[:])
	cleanBody, _ := json.Marshal(GoCacheAction{OutputID: cleanOut, Size: int64(len(cleanBytes))})

	putTrusted := httptest.NewRequest(http.MethodPut, "/gocache/action/"+cleanAction, bytes.NewReader(cleanBody))
	putTrusted.Header.Set("Grit-Job-Token", "tok-trusted")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, putTrusted)
	if rec.Code != http.StatusOK {
		t.Fatalf("trusted PUT = %d: %s", rec.Code, rec.Body.String())
	}

	for _, tok := range []string{"tok-trusted", "tok-untrusted"} {
		req := httptest.NewRequest(http.MethodGet, "/gocache/action/"+cleanAction, nil)
		req.Header.Set("Grit-Job-Token", tok)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET cleanAction with %s = %d, want 200", tok, rec.Code)
		}
	}

	// Outputs are shared and content-verified across both namespaces.
	putOut := httptest.NewRequest(http.MethodPut, "/gocache/output/"+cleanOut, bytes.NewReader(cleanBytes))
	putOut.Header.Set("Grit-Job-Token", "tok-untrusted")
	putOut.ContentLength = int64(len(cleanBytes))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, putOut)
	if rec.Code != http.StatusOK {
		t.Fatalf("output PUT = %d: %s", rec.Code, rec.Body.String())
	}

	getOut := httptest.NewRequest(http.MethodGet, "/gocache/output/"+cleanOut, nil)
	getOut.Header.Set("Grit-Job-Token", "tok-trusted")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, getOut)
	if rec.Code != http.StatusOK {
		t.Fatalf("trusted output GET = %d", rec.Code)
	}
	if got, _ := io.ReadAll(rec.Body); !bytes.Equal(got, cleanBytes) {
		t.Errorf("output bytes = %q, want %q", got, cleanBytes)
	}
}
