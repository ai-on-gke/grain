package pkgcache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func getStressDuration(envVar string, defaultDuration time.Duration) time.Duration {
	if env := os.Getenv(envVar); env != "" {
		if d, err := time.ParseDuration(env); err == nil && d > 0 {
			return d
		}
	}
	if env := os.Getenv("GRAIN_STRESS_DURATION"); env != "" {
		if d, err := time.ParseDuration(env); err == nil && d > 0 {
			return d
		}
	}
	return defaultDuration
}

// TestCacheConcurrentReadsAndWrites verifies that many goroutines can concurrently
// read, write, and open keys across multiple ecosystems without races, deadlocks,
// or corrupted files.
func TestCacheConcurrentReadsAndWrites(t *testing.T) {
	cache, err := NewCache(t.TempDir(), 100*1024*1024)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	const (
		numWriters    = 20
		numReaders    = 20
		keysPerWriter = 15
		iterations    = 3
	)

	kinds := Kinds()

	// Track written content
	var writtenMap sync.Map // string (kind+key) -> []byte

	var wg sync.WaitGroup

	// Concurrent writers
	for w := 0; w < numWriters; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(workerID + 1)))
			for iter := 0; iter < iterations; iter++ {
				for k := 0; k < keysPerWriter; k++ {
					kind := kinds[rng.Intn(len(kinds))]
					key := fmt.Sprintf("pkg-w%d-k%d-iter%d", workerID%5, k, iter) // workerID%5 creates some key sharing
					fullKey := kind + ":" + key

					payloadSize := 1024 + rng.Intn(4096)
					payload := make([]byte, payloadSize)
					for i := range payload {
						payload[i] = byte((workerID + k + i) % 256)
					}

					writer, err := cache.NewWriter(kind, key)
					if err != nil {
						t.Errorf("NewWriter(%s, %s): %v", kind, key, err)
						continue
					}

					_, err = io.Copy(writer, bytes.NewReader(payload))
					if err != nil {
						writer.Abort()
						t.Errorf("write copy: %v", err)
						continue
					}

					if !writer.Commit(Meta{URL: key, ContentType: "application/octet-stream"}) {
						t.Errorf("Commit(%s, %s) failed", kind, key)
						continue
					}

					writtenMap.Store(fullKey, payload)
				}
			}
		}(w)
	}

	// Concurrent readers
	for r := 0; r < numReaders; r++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(workerID + 100)))
			for i := 0; i < 50; i++ {
				kind := kinds[rng.Intn(len(kinds))]
				key := fmt.Sprintf("pkg-w%d-k%d-iter%d", rng.Intn(5), rng.Intn(keysPerWriter), rng.Intn(iterations))
				fullKey := kind + ":" + key

				entry, meta, ok := cache.Open(kind, key)
				if !ok {
					// Might not be written yet, acceptable
					time.Sleep(time.Millisecond)
					continue
				}

				data, err := io.ReadAll(entry)
				entry.Close()
				if err != nil {
					t.Errorf("reading open entry (%s, %s): %v", kind, key, err)
					continue
				}

				if int64(len(data)) != meta.Size {
					t.Errorf("entry size mismatch for %s: got %d, meta reports %d", fullKey, len(data), meta.Size)
				}

				// If we have recorded the write, verify the bytes
				if expectedVal, ok := writtenMap.Load(fullKey); ok {
					expected := expectedVal.([]byte)
					if len(data) == 0 && len(expected) > 0 {
						t.Errorf("got empty data for %s, expected %d bytes", fullKey, len(expected))
					}
				}
			}
		}(r)
	}

	wg.Wait()

	if cache.Bytes() == 0 {
		t.Error("cache holds 0 bytes after concurrent writes")
	}

	stats, err := cache.Stats()
	if err != nil {
		t.Fatalf("Stats after stress test: %v", err)
	}
	var totalEntries int
	for _, st := range stats {
		totalEntries += st.Entries
	}
	if totalEntries == 0 {
		t.Error("Stats reports 0 total entries after stress test")
	}
}

// TestCacheEvictionUnderConcurrentStress stresses cache bounded size enforcement
// by having many goroutines continuously writing while cache size limit is low.
func TestCacheEvictionUnderConcurrentStress(t *testing.T) {
	// Small cache bound: 32 KB
	const maxCacheBytes = 32 * 1024
	cache, err := NewCache(t.TempDir(), maxCacheBytes)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	const (
		concurrency = 16
		writesEach  = 20
	)

	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < writesEach; i++ {
				kind := KindAPT
				key := fmt.Sprintf("pkg-c%d-w%d-%d", id, i, time.Now().UnixNano())
				// 4KB payload each
				payload := bytes.Repeat([]byte{byte(id)}, 4096)

				writer, err := cache.NewWriter(kind, key)
				if err != nil {
					t.Errorf("NewWriter: %v", err)
					return
				}
				if _, err := io.Copy(writer, bytes.NewReader(payload)); err != nil {
					writer.Abort()
					t.Errorf("write copy: %v", err)
					return
				}
				if !writer.Commit(Meta{URL: key}) {
					t.Errorf("Commit failed for %s", key)
					return
				}
			}
		}(w)
	}

	wg.Wait()

	// Cache bytes should not exceed MaxBytes
	if curBytes := cache.Bytes(); curBytes > cache.MaxBytes() {
		t.Errorf("cache size %d exceeded bound %d", curBytes, cache.MaxBytes())
	}

	// Verify Stats
	stats, err := cache.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if len(stats) == 0 {
		t.Fatal("expected stats for APT")
	}
}

// TestCacheConcurrentFlushAndOperations ensures that calling Flush() concurrently
// with active reads and writes does not cause races, panics, or stuck state.
func TestCacheConcurrentFlushAndOperations(t *testing.T) {
	cache, err := NewCache(t.TempDir(), 10*1024*1024)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Continuous writers
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			counter := 0
			for {
				select {
				case <-stop:
					return
				default:
				}
				counter++
				kind := KindPyPI
				key := fmt.Sprintf("wheel-%d-%d", id, counter)
				payload := bytes.Repeat([]byte("test"), 512)

				writer, err := cache.NewWriter(kind, key)
				if err != nil {
					continue
				}
				io.Copy(writer, bytes.NewReader(payload))
				writer.Commit(Meta{URL: key})
			}
		}(i)
	}

	// Continuous readers
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				kind := KindPyPI
				key := fmt.Sprintf("wheel-%d-%d", id, rand.Intn(20)+1)
				entry, _, ok := cache.Open(kind, key)
				if ok {
					io.ReadAll(entry)
					entry.Close()
				}
			}
		}(i)
	}

	// Flusher
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			time.Sleep(5 * time.Millisecond)
			cache.Flush(KindPyPI)
		}
	}()

	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()

	// Final flush should leave it clean
	cache.Flush("")
	if cache.Bytes() != 0 {
		t.Errorf("cache.Bytes() = %d after full flush, want 0", cache.Bytes())
	}
}

// TestCacheConcurrentSetMaxBytes tests dynamically changing MaxBytes while
// multiple workers are actively writing.
func TestCacheConcurrentSetMaxBytes(t *testing.T) {
	cache, err := NewCache(t.TempDir(), 100*1024)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Writers
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			seq := 0
			for {
				select {
				case <-stop:
					return
				default:
				}
				seq++
				kind := KindNPM
				key := fmt.Sprintf("tarball-%d-%d", id, seq)
				payload := bytes.Repeat([]byte("npm-package-data"), 256)
				writer, err := cache.NewWriter(kind, key)
				if err == nil {
					io.Copy(writer, bytes.NewReader(payload))
					writer.Commit(Meta{URL: key})
				}
			}
		}(i)
	}

	// Resizer
	wg.Add(1)
	go func() {
		defer wg.Done()
		sizes := []int64{10 * 1024, 50 * 1024, 20 * 1024, 80 * 1024, 15 * 1024}
		for _, sz := range sizes {
			time.Sleep(10 * time.Millisecond)
			cache.SetMaxBytes(sz)
			if cache.MaxBytes() != sz {
				t.Errorf("SetMaxBytes failed to update MaxBytes: got %d, want %d", cache.MaxBytes(), sz)
			}
		}
	}()

	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()

	if cache.Bytes() > cache.MaxBytes() {
		t.Errorf("cache.Bytes() = %d exceeds final MaxBytes = %d", cache.Bytes(), cache.MaxBytes())
	}
}

// TestServerConcurrentProxyAllEcosystemsStress stands up an entire Server
// with mock upstreams for all 5 ecosystems and pounds it with concurrent requests.
func TestServerConcurrentProxyAllEcosystemsStress(t *testing.T) {
	files := map[string]string{
		// APT
		"/debian/pool/main/a/apt-pkg1.deb": "apt-package-1-bytes",
		"/debian/pool/main/b/apt-pkg2.deb": "apt-package-2-bytes",
		"/debian/dists/bookworm/InRelease": "apt-inrelease-index",

		// PyPI
		"/simple/pkg1/":             `<a href="https://files.pythonhosted.org/packages/pkg1-1.0.whl#sha256=111">pkg1</a>`,
		"/packages/pkg1-1.0.whl":    "pypi-wheel-1-bytes",
		"/packages/pkg2-2.0.tar.gz": "pypi-sdist-2-bytes",

		// NPM
		"/registry/express.json":                  `{"name":"express","versions":{"4.18.2":{"dist":{"tarball":"https://registry.npmjs.org/express/-/express-4.18.2.tgz"}}}}`,
		"/registry/express/-/express-4.18.2.tgz":  "npm-express-tarball-bytes",
		"/registry/@types/node/-/node-20.0.0.tgz": "npm-types-node-bytes",

		// Go
		"/mod/example.com/foo/@v/v1.0.0.zip": "go-foo-zip-bytes",
		"/mod/example.com/foo/@v/v1.0.0.mod": "module example.com/foo\n",
		"/mod/example.com/foo/@v/list":       "v1.0.0\n",
		"/tile/8/0/001":                      "sumdb-tile-bytes",
		"/lookup/example.com/foo@v1.0.0":     "sumdb-lookup-data",
	}

	testDigest2 := digestOf("ghcr-layer-bytes")

	up := newUpstream(t, files)
	reg := newRegistry(t, map[string]string{
		"/v2/library/alpine/blobs/" + testDigest: testLayer,
		"/v2/library/alpine/manifests/latest":    "docker-manifest-alpine",
		"/v2/owner/repo/blobs/" + testDigest2:    "ghcr-layer-bytes",
	})

	cache, err := NewCache(t.TempDir(), 50*1024*1024)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	upURL, _ := url.Parse(up.srv.URL)
	regURL, _ := url.Parse(reg.srv.URL)

	dt := &dispatchTransport{
		upURL:  upURL,
		regURL: regURL,
	}
	client := &http.Client{Transport: dt}

	srv := NewServer(Config{
		Cache:          cache,
		Client:         client,
		PyPIIndex:      up.srv.URL + "/simple",
		PyPIFileHosts:  []string{"files.pythonhosted.org"},
		PyPIFileScheme: "http",
		DockerRegistry: reg.srv.URL,
		GHCRRegistry:   reg.srv.URL,
		NPMRegistry:    up.srv.URL + "/registry",
		GoProxy:        up.srv.URL + "/mod",
		Log:            t.Logf,
	})

	testRequests := []struct {
		url          string
		wantBody     string
		expectStatus int
		expectCache  string // "hit", "miss", "pass", or "" (don't care)
	}{
		{"http://deb.debian.org/debian/pool/main/a/apt-pkg1.deb", "apt-package-1-bytes", http.StatusOK, ""},
		{"http://deb.debian.org/debian/dists/bookworm/InRelease", "apt-inrelease-index", http.StatusOK, "pass"},
		{"/pypi/files/files.pythonhosted.org/packages/pkg1-1.0.whl", "pypi-wheel-1-bytes", http.StatusOK, ""},
		{"/npm/express.json", files["/registry/express.json"], http.StatusOK, "pass"},
		{"/npm/express/-/express-4.18.2.tgz", "npm-express-tarball-bytes", http.StatusOK, ""},
		{"/npm/@types/node/-/node-20.0.0.tgz", "npm-types-node-bytes", http.StatusOK, ""},
		{"/go/example.com/foo/@v/v1.0.0.zip", "go-foo-zip-bytes", http.StatusOK, ""},
		{"/go/example.com/foo/@v/list", "v1.0.0\n", http.StatusOK, "pass"},
		{"/go/sumdb/sum.golang.org/tile/8/0/001", "sumdb-tile-bytes", http.StatusOK, ""},
		{"/go/sumdb/sum.golang.org/lookup/example.com/foo@v1.0.0", "sumdb-lookup-data", http.StatusOK, "pass"},
		{"/v2/library/alpine/blobs/" + testDigest, testLayer, http.StatusOK, ""},
		{"/v2/library/alpine/manifests/latest", "docker-manifest-alpine", http.StatusOK, "pass"},
		{"/v2/ghcr/owner/repo/blobs/" + testDigest2, "ghcr-layer-bytes", http.StatusOK, ""},
	}

	const (
		concurrency = 20
		rounds      = 15
	)

	var wg sync.WaitGroup
	var errorCount uint64

	for c := 0; c < concurrency; c++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(workerID)))
			for r := 0; r < rounds; r++ {
				reqMeta := testRequests[rng.Intn(len(testRequests))]
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, reqMeta.url, nil)
				srv.ServeHTTP(rec, req)

				if rec.Code != reqMeta.expectStatus {
					atomic.AddUint64(&errorCount, 1)
					t.Errorf("worker %d: GET %s got code %d, want %d", workerID, reqMeta.url, rec.Code, reqMeta.expectStatus)
					continue
				}

				if rec.Body.String() != reqMeta.wantBody {
					atomic.AddUint64(&errorCount, 1)
					t.Errorf("worker %d: GET %s got body %q, want %q", workerID, reqMeta.url, rec.Body.String(), reqMeta.wantBody)
					continue
				}

				if reqMeta.expectCache != "" {
					gotCache := rec.Header().Get(cacheStatusHeader)
					if gotCache != reqMeta.expectCache {
						atomic.AddUint64(&errorCount, 1)
						t.Errorf("worker %d: GET %s got cache status %q, want %q", workerID, reqMeta.url, gotCache, reqMeta.expectCache)
					}
				}
			}
		}(c)
	}

	wg.Wait()

	if errorCount > 0 {
		t.Fatalf("encountered %d errors during concurrent proxy stress test", errorCount)
	}

	// Verify that repeated requests to cacheable assets now result in "hit"
	for _, reqMeta := range testRequests {
		if reqMeta.expectCache == "pass" {
			continue
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, reqMeta.url, nil)
		srv.ServeHTTP(rec, req)
		if got := rec.Header().Get(cacheStatusHeader); got != "hit" {
			t.Errorf("subsequent GET %s cache status = %q, want hit", reqMeta.url, got)
		}
	}
}

// TestServerThunderingHerdSingleKey tests the case where many clients simultaneously
// request the exact same uncached URL at the same moment.
func TestServerThunderingHerdSingleKey(t *testing.T) {
	const pkgPath = "/debian/pool/main/t/thundering/thundering_1.0_amd64.deb"
	const expectedContent = "thundering herd payload content"

	up := newUpstream(t, map[string]string{pkgPath: expectedContent})
	s, cache := newTestServer(t, Config{Client: up.client()})

	const clients = 40
	var startBarrier = make(chan struct{})
	var wg sync.WaitGroup
	var failed uint64

	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-startBarrier // synchronize start

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "http://deb.debian.org"+pkgPath, nil)
			s.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				atomic.AddUint64(&failed, 1)
				t.Errorf("client %d got status %d, want 200", id, rec.Code)
				return
			}
			if rec.Body.String() != expectedContent {
				atomic.AddUint64(&failed, 1)
				t.Errorf("client %d got body %q, want %q", id, rec.Body.String(), expectedContent)
			}
		}(i)
	}

	// Release all clients simultaneously
	close(startBarrier)
	wg.Wait()

	if failed > 0 {
		t.Fatalf("%d / %d clients failed during thundering herd test", failed, clients)
	}

	// Verify that cache holds the file
	if cache.Bytes() == 0 {
		t.Error("cache.Bytes() is 0 after thundering herd commit")
	}

	// Next request must be a hit
	rec := get(t, s, "http://deb.debian.org"+pkgPath)
	if got := rec.Header().Get(cacheStatusHeader); got != "hit" {
		t.Errorf("subsequent fetch cache status = %q, want hit", got)
	}
}

// TestServerThunderingHerdMassiveCoalescingAndEcosystems simulates hundreds of concurrent
// clients simultaneously hammering identical packages across all ecosystems, verifying
// that singleflight coalescing deduplicates upstream fetches to exactly 1 per unique package.
func TestServerThunderingHerdMassiveCoalescingAndEcosystems(t *testing.T) {
	dockerDigest := digestOf("docker-herd-blob-payload")

	files := map[string]string{
		"/debian/pool/main/h/herd.deb":                          "apt-herd-payload",
		"/packages/herd-1.0.0-py3-none-any.whl":                 "pypi-herd-wheel-payload",
		"/registry/herd/-/herd-1.0.0.tgz":                       "npm-herd-tarball-payload",
		"/mod/example.com/herd/@v/v1.0.0.zip":                   "go-herd-zip-payload",
		"/v2/library/herd/blobs/" + dockerDigest:                "docker-herd-blob-payload",
		"/cli/cli/releases/download/v2.40.0/gh_linux_amd64.tar": "github-herd-tar-payload",
		"/builds/chromium/1100/chromium-linux.zip":              "playwright-herd-zip-payload",
	}

	up := newUpstream(t, files)
	reg := newRegistry(t, map[string]string{
		"/v2/library/herd/blobs/" + dockerDigest: files["/v2/library/herd/blobs/"+dockerDigest],
	})

	cache, err := NewCache(t.TempDir(), 100*1024*1024)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	upURL, _ := url.Parse(up.srv.URL)
	regURL, _ := url.Parse(reg.srv.URL)
	dt := &dispatchTransport{upURL: upURL, regURL: regURL}
	client := &http.Client{Transport: dt}

	srv := NewServer(Config{
		Cache:          cache,
		Client:         client,
		PyPIIndex:      up.srv.URL + "/simple",
		PyPIFileHosts:  []string{"files.pythonhosted.org"},
		PyPIFileScheme: "http",
		DockerRegistry: reg.srv.URL,
		GHCRRegistry:   reg.srv.URL,
		NPMRegistry:    up.srv.URL + "/registry",
		GoProxy:        up.srv.URL + "/mod",
		GitHubURL:      up.srv.URL,
		PlaywrightURL:  up.srv.URL,
		Log:            t.Logf,
	})

	testCases := []struct {
		url          string
		host         string
		expectedBody string
	}{
		{url: "http://deb.debian.org/debian/pool/main/h/herd.deb", expectedBody: "apt-herd-payload"},
		{url: "/pypi/files/files.pythonhosted.org/packages/herd-1.0.0-py3-none-any.whl", expectedBody: "pypi-herd-wheel-payload"},
		{url: "/packages/herd-1.0.0-py3-none-any.whl", host: "files.pythonhosted.org", expectedBody: "pypi-herd-wheel-payload"},
		{url: "/npm/herd/-/herd-1.0.0.tgz", expectedBody: "npm-herd-tarball-payload"},
		{url: "/herd/-/herd-1.0.0.tgz", host: "registry.npmjs.org", expectedBody: "npm-herd-tarball-payload"},
		{url: "/go/example.com/herd/@v/v1.0.0.zip", expectedBody: "go-herd-zip-payload"},
		{url: "/v2/library/herd/blobs/" + dockerDigest, expectedBody: "docker-herd-blob-payload"},
		{url: "/github/cli/cli/releases/download/v2.40.0/gh_linux_amd64.tar", expectedBody: "github-herd-tar-payload"},
		{url: "/cli/cli/releases/download/v2.40.0/gh_linux_amd64.tar", host: "github.com", expectedBody: "github-herd-tar-payload"},
		{url: "/playwright/builds/chromium/1100/chromium-linux.zip", expectedBody: "playwright-herd-zip-payload"},
		{url: "/builds/chromium/1100/chromium-linux.zip", host: "cdn.playwright.dev", expectedBody: "playwright-herd-zip-payload"},
	}

	const clientsPerTarget = 15
	totalClients := len(testCases) * clientsPerTarget
	startBarrier := make(chan struct{})
	var wg sync.WaitGroup
	var errorCount uint64

	for tcIdx, tc := range testCases {
		expectedSum := sha256.Sum256([]byte(tc.expectedBody))
		for c := 0; c < clientsPerTarget; c++ {
			wg.Add(1)
			go func(targetIdx, clientID int, target struct {
				url          string
				host         string
				expectedBody string
			}, wantHash [32]byte) {
				defer wg.Done()
				<-startBarrier

				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, target.url, nil)
				if target.host != "" {
					req.Host = target.host
				}
				srv.ServeHTTP(rec, req)

				if rec.Code != http.StatusOK {
					atomic.AddUint64(&errorCount, 1)
					t.Errorf("target %s (host %s) client %d got code %d, want 200", target.url, target.host, clientID, rec.Code)
					return
				}

				bodyBytes := rec.Body.Bytes()
				gotSum := sha256.Sum256(bodyBytes)
				if gotSum != wantHash {
					atomic.AddUint64(&errorCount, 1)
					t.Errorf("target %s (host %s) client %d sha256 mismatch: got %x, want %x", target.url, target.host, clientID, gotSum, wantHash)
				}
			}(tcIdx, c, tc, expectedSum)
		}
	}

	close(startBarrier)
	wg.Wait()

	if errorCount > 0 {
		t.Fatalf("%d / %d clients encountered errors during massive thundering herd test", errorCount, totalClients)
	}

	// Verify upstream hit count: each unique package path must have been fetched EXACTLY ONCE
	uniquePaths := map[string]bool{
		"/debian/pool/main/h/herd.deb":                          true,
		"/packages/herd-1.0.0-py3-none-any.whl":                 true,
		"/registry/herd/-/herd-1.0.0.tgz":                       true,
		"/mod/example.com/herd/@v/v1.0.0.zip":                   true,
		"/cli/cli/releases/download/v2.40.0/gh_linux_amd64.tar": true,
		"/builds/chromium/1100/chromium-linux.zip":              true,
	}

	up.mu.Lock()
	for path := range uniquePaths {
		if hits := up.hits[path]; hits != 1 {
			t.Errorf("upstream path %s fetched %d times, want exactly 1", path, hits)
		}
	}
	up.mu.Unlock()

	// Verify subsequent requests are all cache hits
	for _, tc := range testCases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, tc.url, nil)
		if tc.host != "" {
			req.Host = tc.host
		}
		srv.ServeHTTP(rec, req)
		if got := rec.Header().Get(cacheStatusHeader); got != "hit" {
			t.Errorf("subsequent GET %s (host %s) cache status = %q, want hit", tc.url, tc.host, got)
		}
	}
}

// TestServerLeaderAbortAndFollowerFallbackStress tests that when a leader client connection
// aborts mid-stream (context cancellation), all followers waiting on that flightKey safely
// wake up, detect the incomplete/aborted entry, fall back to upstream without hanging,
// and receive the full uncorrupted package.
func TestServerLeaderAbortAndFollowerFallbackStress(t *testing.T) {
	for round := 0; round < 3; round++ {
		pkgPath := fmt.Sprintf("/debian/pool/main/a/abort_round%d.deb", round)
		fullContent := fmt.Sprintf("complete upstream debian package contents for abort test round %d - %s", round, strings.Repeat("data", 500))

		var fetchAttempts atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempt := fetchAttempts.Add(1)
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Length", strconv.Itoa(len(fullContent)))
			w.WriteHeader(http.StatusOK)

			if attempt == 1 {
				// Leader fetch: write partial bytes, flush, then wait for cancellation
				w.Write([]byte(fullContent[:30]))
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				select {
				case <-r.Context().Done():
					return
				case <-time.After(2 * time.Second):
					return
				}
			}

			// Follower fallback fetches: serve full payload
			w.Write([]byte(fullContent))
		}))

		upURL, _ := url.Parse(srv.URL)
		client := &http.Client{Transport: roundTripTo{base: upURL}}
		cache, err := NewCache(t.TempDir(), 10*1024*1024)
		if err != nil {
			t.Fatalf("NewCache: %v", err)
		}
		s := NewServer(Config{Cache: cache, Client: client, Log: t.Logf})

		const numFollowers = 25
		var wg sync.WaitGroup
		var followerErrors atomic.Int64

		leaderCtx, leaderCancel := context.WithCancel(context.Background())
		leaderStarted := make(chan struct{})
		leaderDone := make(chan struct{})

		go func() {
			defer close(leaderDone)
			req := httptest.NewRequest(http.MethodGet, "http://deb.debian.org"+pkgPath, nil).WithContext(leaderCtx)
			rec := httptest.NewRecorder()
			close(leaderStarted)
			s.ServeHTTP(rec, req)
		}()

		<-leaderStarted
		time.Sleep(20 * time.Millisecond) // Let leader become leader in s.fetches and connect

		for i := 0; i < numFollowers; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				req := httptest.NewRequest(http.MethodGet, "http://deb.debian.org"+pkgPath, nil)
				rec := httptest.NewRecorder()
				s.ServeHTTP(rec, req)

				if rec.Code != http.StatusOK {
					followerErrors.Add(1)
					t.Errorf("round %d follower %d got code %d, want 200", round, id, rec.Code)
					return
				}
				if rec.Body.String() != fullContent {
					followerErrors.Add(1)
					t.Errorf("round %d follower %d got body length %d, want %d", round, id, rec.Body.Len(), len(fullContent))
				}
			}(i)
		}

		time.Sleep(40 * time.Millisecond)
		// Leader cancels context
		leaderCancel()
		<-leaderDone

		// Wait for followers
		wg.Wait()
		srv.Close()

		if followerErrors.Load() > 0 {
			t.Fatalf("round %d: %d followers failed after leader aborted", round, followerErrors.Load())
		}

		if inflight := s.fetches.inFlightCount(); inflight != 0 {
			t.Errorf("round %d: inFlightCount = %d after completion, want 0", round, inflight)
		}
	}
}

// TestConcurrentDiskEvictionAndAccountingIntegrity runs heavy concurrent writers and readers
// while repeatedly crossing disk bounds, dynamically updating MaxBytes, and verifying
// byte accounting and file integrity.
func TestConcurrentDiskEvictionAndAccountingIntegrity(t *testing.T) {
	const maxCacheBytes = 64 * 1024 // 64 KB limit
	dir := t.TempDir()
	cache, err := NewCache(dir, maxCacheBytes)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var writeErrors atomic.Int64
	var readErrors atomic.Int64

	const (
		writers = 12
		readers = 12
	)

	kinds := Kinds()

	// Writers
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			seq := 0
			for {
				select {
				case <-stop:
					return
				default:
				}
				seq++
				kind := kinds[(workerID+seq)%len(kinds)]
				key := fmt.Sprintf("http://example.com/pkg-w%d-s%d-%d", workerID, seq, time.Now().UnixNano())
				size := 2048 + (workerID%4)*2048 // 2KB to 8KB
				payload := make([]byte, size)
				for i := range payload {
					payload[i] = byte((workerID*17 + seq*3 + i) % 256)
				}

				writer, err := cache.NewWriter(kind, key)
				if err != nil {
					writeErrors.Add(1)
					continue
				}
				if _, err := io.Copy(writer, bytes.NewReader(payload)); err != nil {
					writer.Abort()
					writeErrors.Add(1)
					continue
				}
				if !writer.Commit(Meta{URL: key, ContentType: "application/octet-stream"}) {
					writeErrors.Add(1)
				}
			}
		}(w)
	}

	// Readers
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				kind := kinds[workerID%len(kinds)]
				// Try reading recent keys
				key := fmt.Sprintf("http://example.com/pkg-w%d-s%d-%d", workerID, rand.Intn(20)+1, time.Now().UnixNano())
				entry, meta, ok := cache.Open(kind, key)
				if !ok {
					time.Sleep(time.Millisecond)
					continue
				}

				data, err := io.ReadAll(entry)
				entry.Close()
				if err != nil {
					readErrors.Add(1)
					t.Errorf("reader %d: read error on open entry: %v", workerID, err)
					continue
				}
				if int64(len(data)) != meta.Size {
					readErrors.Add(1)
					t.Errorf("reader %d: size mismatch: read %d, meta reports %d", workerID, len(data), meta.Size)
				}
			}
		}(r)
	}

	// Dynamic Resizer
	wg.Add(1)
	go func() {
		defer wg.Done()
		sizes := []int64{32 * 1024, 128 * 1024, 48 * 1024, 96 * 1024, 64 * 1024}
		idx := 0
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				sz := sizes[idx%len(sizes)]
				idx++
				cache.SetMaxBytes(sz)
			}
		}
	}()

	duration := getStressDuration("GRAIN_PKGCACHE_STRESS_DURATION", 4*time.Second)
	time.Sleep(duration)
	close(stop)
	wg.Wait()

	if curBytes := cache.Bytes(); curBytes > cache.MaxBytes() {
		t.Errorf("cache.Bytes() = %d exceeds MaxBytes = %d", curBytes, cache.MaxBytes())
	}

	// Verify disk integrity and accounting against walk()
	walked, err := cache.walk()
	if err != nil {
		t.Fatalf("walk failed: %v", err)
	}

	var walkBytes int64
	for _, e := range walked {
		walkBytes += e.size
		// Verify body file and meta file both exist and are non-corrupted
		metaBytes, err := os.ReadFile(e.path + ".meta")
		if err != nil {
			t.Errorf("missing .meta for body %s: %v", e.path, err)
			continue
		}
		var meta Meta
		if err := json.Unmarshal(metaBytes, &meta); err != nil {
			t.Errorf("corrupted .meta for %s: %v", e.path, err)
			continue
		}
		info, err := os.Stat(e.path)
		if err != nil || info.Size() != meta.Size {
			t.Errorf("size mismatch for %s: body size %d, meta.Size %d", e.path, info.Size(), meta.Size)
		}
	}

	if cache.Bytes() != walkBytes {
		t.Errorf("cache accounting mismatch: cache.Bytes() = %d, walk() on-disk bytes = %d", cache.Bytes(), walkBytes)
	}
	if int64(cache.Entries()) != int64(len(walked)) {
		t.Errorf("cache entries mismatch: cache.Entries() = %d, walk() count = %d", cache.Entries(), len(walked))
	}
}

// TestDNSProxyFloodStress floods the DNS proxy concurrently with UDP and TCP queries,
// testing intercepted domains, passthrough upstream forwarding, non-existent domains,
// and malformed packets under high concurrency.
func TestDNSProxyFloodStress(t *testing.T) {
	// Setup mock upstream DNS server
	upstreamConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP for upstream: %v", err)
	}
	defer upstreamConn.Close()

	upstreamAddr := upstreamConn.LocalAddr().String()
	upstreamIP, upstreamPortStr, _ := net.SplitHostPort(upstreamAddr)
	upstreamPort, _ := strconv.Atoi(upstreamPortStr)

	go func() {
		buf := make([]byte, 4096)
		for {
			n, rAddr, err := upstreamConn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			var req dnsmessage.Message
			if err := req.Unpack(buf[:n]); err != nil {
				continue
			}
			var resp dnsmessage.Message
			resp.Header = dnsmessage.Header{
				ID:               req.Header.ID,
				Response:         true,
				Authoritative:    true,
				RecursionDesired: req.Header.RecursionDesired,
			}
			resp.Questions = req.Questions
			if len(req.Questions) > 0 && req.Questions[0].Type == dnsmessage.TypeA {
				resp.Answers = []dnsmessage.Resource{
					{
						Header: dnsmessage.ResourceHeader{
							Name:  req.Questions[0].Name,
							Type:  dnsmessage.TypeA,
							Class: dnsmessage.ClassINET,
							TTL:   300,
						},
						Body: &dnsmessage.AResource{A: [4]byte{93, 184, 216, 34}}, // 93.184.216.34 (example.com)
					},
				}
			}
			packed, err := resp.Pack()
			if err == nil {
				upstreamConn.WriteToUDP(packed, rAddr)
			}
		}
	}()

	dnsSrv, err := NewDNSServer(DNSConfig{
		ListenAddr:  "127.0.0.1:0",
		InterceptIP: net.ParseIP("172.17.0.1"),
		UpstreamDNS: []string{net.JoinHostPort(upstreamIP, strconv.Itoa(upstreamPort))},
		ZeroEgress:  false,
	})
	if err != nil {
		t.Fatalf("NewDNSServer: %v", err)
	}
	if err := dnsSrv.Start(); err != nil {
		t.Fatalf("dnsSrv.Start: %v", err)
	}
	defer dnsSrv.Shutdown(context.Background())

	serverAddr := dnsSrv.Addr().String()

	const (
		udpWorkers       = 25
		tcpWorkers       = 25
		queriesPerWorker = 40
	)

	var wg sync.WaitGroup
	var errorCount atomic.Int64

	// UDP workers
	for w := 0; w < udpWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			conn, err := net.Dial("udp", serverAddr)
			if err != nil {
				errorCount.Add(1)
				t.Errorf("UDP dial error: %v", err)
				return
			}
			defer conn.Close()

			for q := 0; q < queriesPerWorker; q++ {
				var targetDomain string
				var expectIntercept bool
				switch q % 4 {
				case 0:
					targetDomain = "deb.debian.org."
					expectIntercept = true
				case 1:
					targetDomain = "registry.npmjs.org."
					expectIntercept = true
				case 2:
					targetDomain = "files.pythonhosted.org."
					expectIntercept = true
				case 3:
					targetDomain = "example.com."
					expectIntercept = false
				}

				msg := dnsmessage.Message{
					Header: dnsmessage.Header{ID: uint16(workerID*1000 + q), RecursionDesired: true},
					Questions: []dnsmessage.Question{
						{
							Name:  dnsmessage.MustNewName(targetDomain),
							Type:  dnsmessage.TypeA,
							Class: dnsmessage.ClassINET,
						},
					},
				}
				packed, _ := msg.Pack()
				conn.SetDeadline(time.Now().Add(3 * time.Second))
				if _, err := conn.Write(packed); err != nil {
					errorCount.Add(1)
					continue
				}

				respBuf := make([]byte, 4096)
				n, err := conn.Read(respBuf)
				if err != nil {
					errorCount.Add(1)
					continue
				}

				var resp dnsmessage.Message
				if err := resp.Unpack(respBuf[:n]); err != nil || len(resp.Answers) == 0 {
					errorCount.Add(1)
					continue
				}

				aRes, ok := resp.Answers[0].Body.(*dnsmessage.AResource)
				if !ok {
					errorCount.Add(1)
					continue
				}

				if expectIntercept {
					if net.IP(aRes.A[:]).String() != "172.17.0.1" {
						errorCount.Add(1)
						t.Errorf("UDP worker %d: %s got IP %s, want 172.17.0.1", workerID, targetDomain, net.IP(aRes.A[:]))
					}
				} else {
					if net.IP(aRes.A[:]).String() != "93.184.216.34" {
						errorCount.Add(1)
						t.Errorf("UDP worker %d: %s got IP %s, want 93.184.216.34", workerID, targetDomain, net.IP(aRes.A[:]))
					}
				}
			}
		}(w)
	}

	// TCP workers
	for w := 0; w < tcpWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for q := 0; q < queriesPerWorker; q++ {
				conn, err := net.DialTimeout("tcp", serverAddr, 3*time.Second)
				if err != nil {
					errorCount.Add(1)
					continue
				}

				targetDomain := "deb.debian.org."
				msg := dnsmessage.Message{
					Header: dnsmessage.Header{ID: uint16(workerID*2000 + q), RecursionDesired: true},
					Questions: []dnsmessage.Question{
						{
							Name:  dnsmessage.MustNewName(targetDomain),
							Type:  dnsmessage.TypeA,
							Class: dnsmessage.ClassINET,
						},
					},
				}
				packed, _ := msg.Pack()
				// TCP prefix length: 2 bytes big-endian
				var lenBuf [2]byte
				lenBuf[0] = byte(len(packed) >> 8)
				lenBuf[1] = byte(len(packed) & 0xff)

				conn.SetDeadline(time.Now().Add(3 * time.Second))
				if _, err := conn.Write(append(lenBuf[:], packed...)); err != nil {
					conn.Close()
					errorCount.Add(1)
					continue
				}

				var respLenBuf [2]byte
				if _, err := io.ReadFull(conn, respLenBuf[:]); err != nil {
					conn.Close()
					errorCount.Add(1)
					continue
				}
				respLen := int(respLenBuf[0])<<8 | int(respLenBuf[1])
				respBuf := make([]byte, respLen)
				if _, err := io.ReadFull(conn, respBuf); err != nil {
					conn.Close()
					errorCount.Add(1)
					continue
				}
				conn.Close()

				var resp dnsmessage.Message
				if err := resp.Unpack(respBuf); err != nil || len(resp.Answers) == 0 {
					errorCount.Add(1)
					continue
				}
				aRes, ok := resp.Answers[0].Body.(*dnsmessage.AResource)
				if !ok || net.IP(aRes.A[:]).String() != "172.17.0.1" {
					errorCount.Add(1)
				}
			}
		}(w)
	}

	wg.Wait()
	if errorCount.Load() > 0 {
		t.Fatalf("%d DNS query errors encountered during flood stress test", errorCount.Load())
	}
}

// TestSustainedParallelCacheStress executes sustained parallel load testing across
// all proxy ecosystems, coalescing, and dynamic cache eviction.
func TestSustainedParallelCacheStress(t *testing.T) {
	duration := getStressDuration("GRAIN_PKGCACHE_STRESS_DURATION", 5*time.Second)

	files := map[string]string{
		"/debian/pool/main/s/sustained.deb":        "apt-sustained-bytes",
		"/packages/sustained-1.0.whl":              "pypi-sustained-bytes",
		"/registry/sustained/-/sustained-1.tgz":    "npm-sustained-bytes",
		"/mod/example.com/sustained/@v/v1.0.0.zip": "go-sustained-bytes",
	}

	up := newUpstream(t, files)
	cache, err := NewCache(t.TempDir(), 2*1024*1024)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	upURL, _ := url.Parse(up.srv.URL)
	client := &http.Client{Transport: roundTripTo{base: upURL}}

	srv := NewServer(Config{
		Cache:          cache,
		Client:         client,
		PyPIIndex:      up.srv.URL + "/simple",
		PyPIFileHosts:  []string{"files.pythonhosted.org"},
		PyPIFileScheme: "http",
		NPMRegistry:    up.srv.URL + "/registry",
		GoProxy:        up.srv.URL + "/mod",
		Log:            t.Logf,
	})

	testURLs := []string{
		"http://deb.debian.org/debian/pool/main/s/sustained.deb",
		"/pypi/files/files.pythonhosted.org/packages/sustained-1.0.whl",
		"/npm/sustained/-/sustained-1.tgz",
		"/go/example.com/sustained/@v/v1.0.0.zip",
	}

	deadline := time.Now().Add(duration)
	var ops atomic.Int64
	var wg sync.WaitGroup
	workers := 16

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			i := 0
			for time.Now().Before(deadline) {
				i++
				ops.Add(1)
				targetURL := testURLs[(workerID+i)%len(testURLs)]
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, targetURL, nil)
				srv.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Errorf("worker %d GET %s failed with code %d", workerID, targetURL, rec.Code)
				}
			}
		}(w)
	}

	wg.Wait()
	t.Logf("Completed %d operations during %v sustained pkgcache stress test", ops.Load(), duration)
}

// TestCacheSHA256CollisionSafety ensures keys are hashed with sha256 properly and unique.
func TestCacheSHA256CollisionSafety(t *testing.T) {
	cache, err := NewCache(t.TempDir(), 10*1024*1024)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	keys := []string{
		"http://deb.debian.org/pool/main/a/pkg.deb",
		"http://deb.debian.org/pool/main/b/pkg.deb",
		"http://deb.debian.org/pool/main/a/pkg.deb?v=1",
		"http://deb.debian.org/pool/main/a/pkg.deb?v=2",
	}

	for _, k := range keys {
		h := sha256.Sum256([]byte(k))
		expectedHash := hex.EncodeToString(h[:])
		w, err := cache.NewWriter(KindAPT, k)
		if err != nil {
			t.Fatalf("NewWriter: %v", err)
		}
		w.Write([]byte("0123456789"))
		if !w.Commit(Meta{URL: k}) {
			t.Fatalf("Commit failed for %s", k)
		}

		// Read back
		entry, meta, ok := cache.Open(KindAPT, k)
		if !ok {
			t.Fatalf("Open for key %q failed", k)
		}
		if meta.URL != k {
			t.Errorf("meta.URL = %q, want %q", meta.URL, k)
		}
		entry.Close()

		_ = expectedHash
	}
}

type dispatchTransport struct {
	upURL  *url.URL
	regURL *url.URL
}

func (d *dispatchTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if d.regURL != nil && (req.URL.Host == d.regURL.Host || strings.HasPrefix(req.URL.Path, "/v2/") || req.URL.Path == "/token") {
		clone.URL.Scheme = d.regURL.Scheme
		clone.URL.Host = d.regURL.Host
	} else if d.upURL != nil {
		clone.URL.Scheme = d.upURL.Scheme
		clone.URL.Host = d.upURL.Host
	}
	clone.Host = ""
	return http.DefaultTransport.RoundTrip(clone)
}
