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
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTruncatedUpstreamResponseNotCached ensures that when upstream abruptly terminates
// the connection before sending the full Content-Length, the truncated body is never
// committed to the cache. Subsequent requests must re-fetch the complete body.
func TestTruncatedUpstreamResponseNotCached(t *testing.T) {
	const pkg = "/debian/pool/main/h/hello/hello_2.10-3_amd64.deb"
	const fullPayload = "complete full package payload bytes"

	var serveTruncated = true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != pkg {
			http.NotFound(w, r)
			return
		}
		if serveTruncated {
			// Advertise a large Content-Length but close connection early
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(fullPayload)*2))
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("truncated"))
			// Force flush and close connection by hijacking or just returning
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, _ := hj.Hijack()
				conn.Close()
			}
			return
		}

		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(fullPayload)))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fullPayload))
	}))
	t.Cleanup(srv.Close)

	parsedURL, _ := url.Parse(srv.URL)
	client := &http.Client{Transport: roundTripTo{base: parsedURL}}
	s, cache := newTestServer(t, Config{Client: client})

	// First request gets truncated upstream
	rec1 := get(t, s, "http://deb.debian.org"+pkg)
	// Server either returns partial or error
	_ = rec1

	// Verify nothing was committed to cache
	if _, _, ok := cache.Open(KindAPT, "http://deb.debian.org"+pkg); ok {
		t.Fatal("cache committed truncated entry!")
	}
	if cache.Bytes() > 0 {
		t.Fatalf("cache.Bytes() = %d after truncated response, want 0", cache.Bytes())
	}

	// Now make upstream healthy
	serveTruncated = false

	// Second request should fetch cleanly from upstream and commit
	rec2 := get(t, s, "http://deb.debian.org"+pkg)
	if rec2.Code != http.StatusOK || rec2.Body.String() != fullPayload {
		t.Fatalf("second fetch: code=%d, body=%q, want %q", rec2.Code, rec2.Body.String(), fullPayload)
	}
	if got := rec2.Header().Get(cacheStatusHeader); got != "miss" {
		t.Errorf("second fetch cache status = %q, want miss", got)
	}

	// Third request must be a hit from disk
	rec3 := get(t, s, "http://deb.debian.org"+pkg)
	if rec3.Code != http.StatusOK || rec3.Body.String() != fullPayload {
		t.Fatalf("third fetch: code=%d, body=%q, want %q", rec3.Code, rec3.Body.String(), fullPayload)
	}
	if got := rec3.Header().Get(cacheStatusHeader); got != "hit" {
		t.Errorf("third fetch cache status = %q, want hit", got)
	}
}

// TestClientContextCancellationAbortsWriter verifies that if the client cancels
// its HTTP context midway through receiving the response, the cache writer is aborted
// and no orphaned files or corrupt entries remain.
func TestClientContextCancellationAbortsWriter(t *testing.T) {
	const pkg = "/packages/large-pkg-1.0.whl"
	payload := strings.Repeat("wheel-data-block-", 1000)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		// Send in small chunks with delays
		for i := 0; i < len(payload); i += 100 {
			end := i + 100
			if end > len(payload) {
				end = len(payload)
			}
			w.Write([]byte(payload[i:end]))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(5 * time.Millisecond)
		}
	}))
	t.Cleanup(srv.Close)

	parsedURL, _ := url.Parse(srv.URL)
	client := &http.Client{Transport: roundTripTo{base: parsedURL}}
	s, cache := newTestServer(t, Config{
		Client:         client,
		PyPIIndex:      srv.URL + "/simple",
		PyPIFileHosts:  []string{"files.pythonhosted.org"},
		PyPIFileScheme: "http",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/pypi/files/files.pythonhosted.org"+pkg, nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	// Entry must NOT be committed to cache
	if _, _, ok := cache.Open(KindPyPI, "http://files.pythonhosted.org"+pkg); ok {
		t.Error("cache committed entry despite cancelled client context")
	}
}

// TestCorruptedMetaOnDiskRepaired verifies that if a .meta file on disk contains corrupted
// JSON, Open returns false, the server treats it as a cache miss, fetches fresh data,
// and rewrites a valid entry.
func TestCorruptedMetaOnDiskRepaired(t *testing.T) {
	const pkg = "/debian/pool/main/h/hello/hello_2.10-3_amd64.deb"
	const expected = "valid package payload"
	up := newUpstream(t, map[string]string{pkg: expected})
	s, cache := newTestServer(t, Config{Client: up.client()})

	// 1. First fetch to populate cache
	rec1 := get(t, s, "http://deb.debian.org"+pkg)
	if rec1.Code != http.StatusOK || rec1.Body.String() != expected {
		t.Fatalf("first fetch failed: %d %q", rec1.Code, rec1.Body.String())
	}

	// 2. Corrupt the .meta file on disk
	path := cache.key(KindAPT, "http://deb.debian.org"+pkg)
	metaPath := path + ".meta"
	if err := os.WriteFile(metaPath, []byte("NOT_VALID_JSON{{{"), 0o644); err != nil {
		t.Fatalf("corrupting meta file: %v", err)
	}

	// Open should now return false
	if _, _, ok := cache.Open(KindAPT, "http://deb.debian.org"+pkg); ok {
		t.Fatal("Open succeeded on corrupted .meta file")
	}

	// 3. Second fetch should treat it as miss, re-fetch from upstream, and repair .meta
	rec2 := get(t, s, "http://deb.debian.org"+pkg)
	if rec2.Code != http.StatusOK || rec2.Body.String() != expected {
		t.Fatalf("second fetch failed: %d %q", rec2.Code, rec2.Body.String())
	}
	if got := rec2.Header().Get(cacheStatusHeader); got != "miss" {
		t.Errorf("second fetch cache status = %q, want miss", got)
	}

	// 4. Third fetch should now be a healthy hit
	rec3 := get(t, s, "http://deb.debian.org"+pkg)
	if got := rec3.Header().Get(cacheStatusHeader); got != "hit" {
		t.Errorf("third fetch cache status = %q, want hit", got)
	}
}

// TestCorruptedPayloadSizeMismatchRepaired verifies that if payload file size on disk
// differs from meta.Size, Open detects the mismatch, rejects the entry, and the proxy
// re-fetches and repairs it.
func TestCorruptedPayloadSizeMismatchRepaired(t *testing.T) {
	const pkg = "/debian/pool/main/h/hello/hello_2.10-3_amd64.deb"
	const expected = "valid package payload of exact length"
	up := newUpstream(t, map[string]string{pkg: expected})
	s, cache := newTestServer(t, Config{Client: up.client()})

	// Initial fetch
	rec1 := get(t, s, "http://deb.debian.org"+pkg)
	if rec1.Code != http.StatusOK {
		t.Fatalf("initial fetch failed: %d", rec1.Code)
	}

	// Truncate payload file on disk (leaving .meta intact)
	path := cache.key(KindAPT, "http://deb.debian.org"+pkg)
	if err := os.Truncate(path, 5); err != nil {
		t.Fatalf("truncating payload: %v", err)
	}

	// Open must detect size mismatch and return false
	if _, _, ok := cache.Open(KindAPT, "http://deb.debian.org"+pkg); ok {
		t.Fatal("Open succeeded despite payload size mismatch")
	}

	// Next fetch must repair and serve full body
	rec2 := get(t, s, "http://deb.debian.org"+pkg)
	if rec2.Code != http.StatusOK || rec2.Body.String() != expected {
		t.Fatalf("repair fetch failed: code=%d body=%q", rec2.Code, rec2.Body.String())
	}
	if got := rec2.Header().Get(cacheStatusHeader); got != "miss" {
		t.Errorf("repair fetch cache status = %q, want miss", got)
	}

	// Subsequent fetch is hit
	rec3 := get(t, s, "http://deb.debian.org"+pkg)
	if got := rec3.Header().Get(cacheStatusHeader); got != "hit" {
		t.Errorf("subsequent fetch cache status = %q, want hit", got)
	}
}

// TestMissingMetaOrPayloadHandled tests cases where either .meta or payload is deleted.
func TestMissingMetaOrPayloadHandled(t *testing.T) {
	const pkg = "/debian/pool/main/h/hello/hello_2.10-3_amd64.deb"
	const expected = "valid package payload"
	up := newUpstream(t, map[string]string{pkg: expected})
	s, cache := newTestServer(t, Config{Client: up.client()})

	get(t, s, "http://deb.debian.org"+pkg)

	path := cache.key(KindAPT, "http://deb.debian.org"+pkg)

	// Case 1: Delete .meta
	os.Remove(path + ".meta")
	if _, _, ok := cache.Open(KindAPT, "http://deb.debian.org"+pkg); ok {
		t.Error("Open succeeded with missing .meta")
	}
	rec := get(t, s, "http://deb.debian.org"+pkg)
	if rec.Code != http.StatusOK || rec.Body.String() != expected {
		t.Errorf("fetch with missing .meta failed: %d %q", rec.Code, rec.Body.String())
	}

	// Case 2: Delete payload
	os.Remove(path)
	if _, _, ok := cache.Open(KindAPT, "http://deb.debian.org"+pkg); ok {
		t.Error("Open succeeded with missing payload")
	}
	rec = get(t, s, "http://deb.debian.org"+pkg)
	if rec.Code != http.StatusOK || rec.Body.String() != expected {
		t.Errorf("fetch with missing payload failed: %d %q", rec.Code, rec.Body.String())
	}
}

// TestUnwritableCacheDirectoryFallback verifies graceful degradation: if the cache
// directory cannot be written (e.g. read-only filesystem or unwritable permissions),
// the proxy still streams the upstream response cleanly to the client with 200 OK.
func TestUnwritableCacheDirectoryFallback(t *testing.T) {
	// Root ignores the 0555 below, so the write it is meant to fail
	// succeeds and the fallback this asks about is never reached.
	if os.Geteuid() == 0 {
		t.Skip("running as root: a read-only directory does not stop root writing to it")
	}
	const pkg = "/debian/pool/main/h/hello/hello_2.10-3_amd64.deb"
	const expected = "valid package payload"
	up := newUpstream(t, map[string]string{pkg: expected})

	dir := t.TempDir()
	cache, err := NewCache(dir, 0)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	// Make the apt kind directory read-only so NewWriter will fail
	aptDir := filepath.Join(dir, KindAPT)
	if err := os.Chmod(aptDir, 0o555); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer os.Chmod(aptDir, 0o755)

	s := NewServer(Config{
		Cache:  cache,
		Client: up.client(),
		Log:    t.Logf,
	})

	rec := get(t, s, "http://deb.debian.org"+pkg)
	if rec.Code != http.StatusOK {
		t.Fatalf("proxy failed with unwritable cache dir: status %d", rec.Code)
	}
	if rec.Body.String() != expected {
		t.Fatalf("proxy body mismatch with unwritable cache dir: got %q, want %q", rec.Body.String(), expected)
	}
	if got := rec.Header().Get(cacheStatusHeader); got != "pass" {
		t.Errorf("cacheStatusHeader = %q, want pass", got)
	}
}

// TestUpstreamErrorsNotCached verifies that upstream 404, 500, 502, 503, 401
// responses are passed to the client without being stored in the cache.
func TestUpstreamErrorsNotCached(t *testing.T) {
	statusCodes := []int{
		http.StatusNotFound,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusUnauthorized,
	}

	for _, code := range statusCodes {
		t.Run(fmt.Sprintf("Status_%d", code), func(t *testing.T) {
			path := fmt.Sprintf("/debian/pool/main/e/err_%d.deb", code)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, fmt.Sprintf("upstream error %d", code), code)
			}))
			defer srv.Close()

			parsedURL, _ := url.Parse(srv.URL)
			client := &http.Client{Transport: roundTripTo{base: parsedURL}}
			s, cache := newTestServer(t, Config{Client: client})

			rec := get(t, s, "http://deb.debian.org"+path)
			if rec.Code != code {
				t.Fatalf("proxy status = %d, want %d", rec.Code, code)
			}
			if got := rec.Header().Get(cacheStatusHeader); got != "pass" {
				t.Errorf("cacheStatusHeader = %q, want pass", got)
			}

			// Verify nothing is in cache
			if cache.Bytes() != 0 {
				t.Errorf("cache holds %d bytes after upstream error %d, want 0", cache.Bytes(), code)
			}
		})
	}
}

// TestUpstreamConnectionRefused tests behavior when upstream is completely unreachable.
func TestUpstreamConnectionRefused(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := l.Addr().String()
	l.Close() // Immediately close to guarantee connection refused

	client := &http.Client{
		Transport: &http.Transport{},
		Timeout:   2 * time.Second,
	}

	host, _, _ := net.SplitHostPort(addr)
	s, _ := newTestServer(t, Config{
		Client:   client,
		AptHosts: []string{host},
		Log:      t.Logf,
	})

	rec := get(t, s, fmt.Sprintf("http://%s/debian/pool/main/p/pkg.deb", addr))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("got status %d, want 502 Bad Gateway", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "could not reach") {
		t.Errorf("body %q does not mention could not reach", rec.Body.String())
	}
}

// TestWriterAbortRemovesPartialFile directly tests that Writer.Abort unlinks
// the .partial-* temp file from disk.
func TestWriterAbortRemovesPartialFile(t *testing.T) {
	cache, err := NewCache(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	w, err := cache.NewWriter(KindAPT, "http://example.com/test.deb")
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	tmpPath := w.(*Writer).tmp.Name()
	if _, err := os.Stat(tmpPath); err != nil {
		t.Fatalf("temp file does not exist before abort: %v", err)
	}

	w.Write([]byte("some data"))
	w.Abort()

	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Errorf("temp file still exists after Abort(): %v", err)
	}
}
