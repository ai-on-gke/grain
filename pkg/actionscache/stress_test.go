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

package actionscache

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

// TestActionscacheHighConcurrencyRoundTrip stresses Actions cache HTTP endpoints with
// hundreds of concurrent clients across multiple repos and tokens performing reservations,
// multi-part out-of-order chunk uploads, commits, lookups, and archive downloads.
func TestActionscacheHighConcurrencyRoundTrip(t *testing.T) {
	cache, err := NewCache(t.TempDir(), 200*1024*1024)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	scopes := NewScopes()
	repos := map[string]string{
		"tok-repo1": "org/repo1",
		"tok-repo2": "org/repo2",
		"tok-repo3": "org/repo3",
		"tok-repo4": "org/repo4",
	}
	for tok, repo := range repos {
		scopes.Grant(tok, branch(repo))
	}

	srv := httptest.NewServer(NewServer(Config{Cache: cache, Scopes: scopes, Log: t.Logf}))
	defer srv.Close()

	const (
		numWorkers   = 40
		opsPerWorker = 10
		chunkSize    = 2048
	)

	var wg sync.WaitGroup
	var errorCount uint64

	tokens := []string{"tok-repo1", "tok-repo2", "tok-repo3", "tok-repo4"}

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			client := &http.Client{Timeout: 30 * time.Second}
			tok := tokens[workerID%len(tokens)]

			for op := 0; op < opsPerWorker; op++ {
				key := fmt.Sprintf("cache-key-w%d-op%d-%d", workerID, op, time.Now().UnixNano())
				version := fmt.Sprintf("v-w%d", workerID%4)

				// Generate random payload (4-12 KB)
				numChunks := 2 + (workerID+op)%4
				totalSize := numChunks * chunkSize
				payload := make([]byte, totalSize)
				for i := range payload {
					payload[i] = byte((workerID*13 + op*7 + i) % 256)
				}
				expectedSum := sha256.Sum256(payload)

				// 1. Reserve cache ID
				reserveReqBody := fmt.Sprintf(`{"key":%q,"version":%q,"cacheSize":%d}`, key, version, totalSize)
				reserveURL := SandboxURL(srv.URL, tok) + "_apis/artifactcache/caches"
				resp, err := client.Post(reserveURL, "application/json", strings.NewReader(reserveReqBody))
				if err != nil {
					atomic.AddUint64(&errorCount, 1)
					t.Errorf("worker %d reserve error: %v", workerID, err)
					continue
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					atomic.AddUint64(&errorCount, 1)
					t.Errorf("worker %d reserve status = %d, body: %s", workerID, resp.StatusCode, string(body))
					continue
				}

				var reserved struct {
					CacheID uint64 `json:"cacheId"`
				}
				if err := json.Unmarshal(body, &reserved); err != nil || reserved.CacheID == 0 {
					atomic.AddUint64(&errorCount, 1)
					t.Errorf("worker %d unmarshal reserve: %v, cacheId = %d", workerID, err, reserved.CacheID)
					continue
				}

				// 2. Upload chunks in reverse/scrambled order
				uploadURL := fmt.Sprintf("%s_apis/artifactcache/caches/%d", SandboxURL(srv.URL, tok), reserved.CacheID)
				for c := numChunks - 1; c >= 0; c-- {
					startOffset := int64(c * chunkSize)
					endOffset := startOffset + int64(chunkSize) - 1
					chunkData := payload[startOffset : endOffset+1]

					patchReq, err := http.NewRequest(http.MethodPatch, uploadURL, bytes.NewReader(chunkData))
					if err != nil {
						atomic.AddUint64(&errorCount, 1)
						t.Errorf("worker %d build patch: %v", workerID, err)
						break
					}
					patchReq.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/*", startOffset, endOffset))
					patchReq.Header.Set("Content-Type", "application/octet-stream")

					patchResp, err := client.Do(patchReq)
					if err != nil {
						atomic.AddUint64(&errorCount, 1)
						t.Errorf("worker %d patch chunk %d error: %v", workerID, c, err)
						break
					}
					patchResp.Body.Close()
					if patchResp.StatusCode != http.StatusOK {
						atomic.AddUint64(&errorCount, 1)
						t.Errorf("worker %d patch chunk %d status: %d", workerID, c, patchResp.StatusCode)
						break
					}
				}

				// 3. Commit
				commitReqBody := fmt.Sprintf(`{"size":%d}`, totalSize)
				commitResp, err := client.Post(uploadURL, "application/json", strings.NewReader(commitReqBody))
				if err != nil {
					atomic.AddUint64(&errorCount, 1)
					t.Errorf("worker %d commit error: %v", workerID, err)
					continue
				}
				commitResp.Body.Close()
				if commitResp.StatusCode != http.StatusOK {
					atomic.AddUint64(&errorCount, 1)
					t.Errorf("worker %d commit status: %d", workerID, commitResp.StatusCode)
					continue
				}

				// 4. Find
				findURL := fmt.Sprintf("%s_apis/artifactcache/cache?keys=%s&version=%s", SandboxURL(srv.URL, tok), key, version)
				findResp, err := client.Get(findURL)
				if err != nil {
					atomic.AddUint64(&errorCount, 1)
					t.Errorf("worker %d find error: %v", workerID, err)
					continue
				}
				findBody, _ := io.ReadAll(findResp.Body)
				findResp.Body.Close()
				if findResp.StatusCode != http.StatusOK {
					atomic.AddUint64(&errorCount, 1)
					t.Errorf("worker %d find status = %d", workerID, findResp.StatusCode)
					continue
				}

				var hit struct {
					ArchiveLocation string `json:"archiveLocation"`
					CacheKey        string `json:"cacheKey"`
				}
				if err := json.Unmarshal(findBody, &hit); err != nil || hit.ArchiveLocation == "" {
					atomic.AddUint64(&errorCount, 1)
					t.Errorf("worker %d find json parse: %v, hit: %+v", workerID, err, hit)
					continue
				}

				// 5. Download artifact archive
				getResp, err := client.Get(hit.ArchiveLocation)
				if err != nil {
					atomic.AddUint64(&errorCount, 1)
					t.Errorf("worker %d download error: %v", workerID, err)
					continue
				}
				downloaded, err := io.ReadAll(getResp.Body)
				getResp.Body.Close()
				if err != nil {
					atomic.AddUint64(&errorCount, 1)
					t.Errorf("worker %d reading download: %v", workerID, err)
					continue
				}
				if getResp.StatusCode != http.StatusOK {
					atomic.AddUint64(&errorCount, 1)
					t.Errorf("worker %d download status = %d", workerID, getResp.StatusCode)
					continue
				}

				if len(downloaded) != totalSize {
					atomic.AddUint64(&errorCount, 1)
					t.Errorf("worker %d downloaded %d bytes, want %d", workerID, len(downloaded), totalSize)
					continue
				}

				downloadSum := sha256.Sum256(downloaded)
				if downloadSum != expectedSum {
					atomic.AddUint64(&errorCount, 1)
					t.Errorf("worker %d sha256 mismatch: got %x, want %x", workerID, downloadSum, expectedSum)
				}
			}
		}(w)
	}

	wg.Wait()
	if errorCount > 0 {
		t.Fatalf("encountered %d errors in high concurrency roundtrip test", errorCount)
	}

	stats := cache.Stats()
	if stats.Bytes == 0 || stats.Entries == 0 {
		t.Errorf("cache Stats empty after roundtrip test: %+v", stats)
	}
}

// TestActionscacheEvictionAndDynamicResizingStress stresses actions cache under tight
// memory/disk bounds, constantly triggering LRU evictions while concurrent uploads,
// downloads, dynamic resizing, and flushes are occurring.
func TestActionscacheEvictionAndDynamicResizingStress(t *testing.T) {
	// 64 KB cache bound to force frequent evictions
	const maxCacheBytes = 64 * 1024
	dir := t.TempDir()
	cache, err := NewCache(dir, maxCacheBytes)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	scopes := NewScopes()
	scopes.Grant("tok-evict", branch("org/evict-repo"))
	srv := httptest.NewServer(NewServer(Config{Cache: cache, Scopes: scopes}))
	defer srv.Close()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var uploadErrors uint64
	var readErrors uint64

	const (
		writers = 12
		readers = 8
	)

	// Keep track of committed keys and versions
	type itemInfo struct {
		key     string
		version string
		sha     [32]byte
		size    int
	}
	var committedItems sync.Map // string -> itemInfo

	// Concurrent Writers
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			client := &http.Client{Timeout: 10 * time.Second}
			seq := 0
			tok := "tok-evict"

			for {
				select {
				case <-stop:
					return
				default:
				}
				seq++
				key := fmt.Sprintf("evict-w%d-s%d-%d", workerID, seq, time.Now().UnixNano())
				version := "v1"
				size := 4096 + (workerID%4)*2048 // 4KB to 10KB
				payload := make([]byte, size)
				for i := range payload {
					payload[i] = byte((workerID + seq + i) % 256)
				}
				sum := sha256.Sum256(payload)

				// Reserve
				reserveReqBody := fmt.Sprintf(`{"key":%q,"version":%q,"cacheSize":%d}`, key, version, size)
				reserveURL := SandboxURL(srv.URL, tok) + "_apis/artifactcache/caches"
				resp, err := client.Post(reserveURL, "application/json", strings.NewReader(reserveReqBody))
				if err != nil {
					atomic.AddUint64(&uploadErrors, 1)
					continue
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					continue
				}
				var reserved struct {
					CacheID uint64 `json:"cacheId"`
				}
				if err := json.Unmarshal(body, &reserved); err != nil || reserved.CacheID == 0 {
					continue
				}

				// Upload single range
				uploadURL := fmt.Sprintf("%s_apis/artifactcache/caches/%d", SandboxURL(srv.URL, tok), reserved.CacheID)
				patchReq, _ := http.NewRequest(http.MethodPatch, uploadURL, bytes.NewReader(payload))
				patchReq.Header.Set("Content-Range", fmt.Sprintf("bytes 0-%d/*", size-1))
				patchReq.Header.Set("Content-Type", "application/octet-stream")
				patchResp, err := client.Do(patchReq)
				if err != nil {
					atomic.AddUint64(&uploadErrors, 1)
					continue
				}
				patchResp.Body.Close()
				if patchResp.StatusCode != http.StatusOK {
					continue
				}

				// Commit
				commitResp, err := client.Post(uploadURL, "application/json", strings.NewReader(fmt.Sprintf(`{"size":%d}`, size)))
				if err != nil {
					atomic.AddUint64(&uploadErrors, 1)
					continue
				}
				commitResp.Body.Close()
				if commitResp.StatusCode == http.StatusOK {
					committedItems.Store(key, itemInfo{key: key, version: version, sha: sum, size: size})
				}
			}
		}(w)
	}

	// Concurrent Readers
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			client := &http.Client{Timeout: 10 * time.Second}
			tok := "tok-evict"

			for {
				select {
				case <-stop:
					return
				default:
				}

				var target itemInfo
				var found bool
				committedItems.Range(func(k, v any) bool {
					target = v.(itemInfo)
					found = true
					return false // take one
				})

				if !found {
					time.Sleep(2 * time.Millisecond)
					continue
				}

				findURL := fmt.Sprintf("%s_apis/artifactcache/cache?keys=%s&version=%s", SandboxURL(srv.URL, tok), target.key, target.version)
				findResp, err := client.Get(findURL)
				if err != nil {
					atomic.AddUint64(&readErrors, 1)
					continue
				}
				findBody, _ := io.ReadAll(findResp.Body)
				findResp.Body.Close()

				if findResp.StatusCode == http.StatusNoContent {
					// Evicted - acceptable
					continue
				}
				if findResp.StatusCode != http.StatusOK {
					atomic.AddUint64(&readErrors, 1)
					continue
				}

				var hit struct {
					ArchiveLocation string `json:"archiveLocation"`
				}
				if err := json.Unmarshal(findBody, &hit); err != nil || hit.ArchiveLocation == "" {
					continue
				}

				getResp, err := client.Get(hit.ArchiveLocation)
				if err != nil {
					atomic.AddUint64(&readErrors, 1)
					continue
				}
				dlData, err := io.ReadAll(getResp.Body)
				getResp.Body.Close()
				if err != nil {
					atomic.AddUint64(&readErrors, 1)
					continue
				}

				if getResp.StatusCode == http.StatusNotFound {
					// Evicted between find and download - acceptable
					continue
				}
				if getResp.StatusCode != http.StatusOK {
					atomic.AddUint64(&readErrors, 1)
					continue
				}

				if len(dlData) != target.size {
					atomic.AddUint64(&readErrors, 1)
					t.Errorf("reader %d: downloaded size mismatch: got %d, want %d", workerID, len(dlData), target.size)
					continue
				}

				dlSum := sha256.Sum256(dlData)
				if dlSum != target.sha {
					atomic.AddUint64(&readErrors, 1)
					t.Errorf("reader %d: sha256 mismatch for %s: got %x, want %x", workerID, target.key, dlSum, target.sha)
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
			case <-time.After(15 * time.Millisecond):
				sz := sizes[idx%len(sizes)]
				idx++
				cache.SetMaxBytes(sz)
			}
		}
	}()

	duration := getStressDuration("GRAIN_ACTIONSSCACHE_STRESS_DURATION", 4*time.Second)
	time.Sleep(duration)
	close(stop)
	wg.Wait()

	// Verify byte accounting invariants
	stats := cache.Stats()
	if stats.Bytes > cache.MaxBytes() {
		t.Errorf("cache.Stats().Bytes = %d exceeds MaxBytes = %d", stats.Bytes, cache.MaxBytes())
	}

	// Verify disk integrity: on-disk complete artifact sizes must match stats.Bytes
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var onDiskBytes int64
	var completeCount int
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id, err := strconv.ParseUint(entry.Name(), 10, 64)
		if err != nil {
			continue
		}
		bodyPath := filepath.Join(dir, entry.Name(), "body")
		if info, err := os.Stat(bodyPath); err == nil {
			onDiskBytes += info.Size()
			completeCount++
		}
		_ = id
	}

	if stats.Bytes != onDiskBytes {
		t.Errorf("cache accounting mismatch: stats.Bytes = %d, actual on-disk archive bytes = %d", stats.Bytes, onDiskBytes)
	}
	if stats.Entries != completeCount {
		t.Errorf("cache entries mismatch: stats.Entries = %d, actual on-disk archives = %d", stats.Entries, completeCount)
	}
}

// TestActionscacheClientAbortsAndFaultTolerance tests client aborts mid-stream,
// such as abandoning reservations, leaving uncommitted partial uploads, or trying
// to commit invalid/corrupted uploads.
func TestActionscacheClientAbortsAndFaultTolerance(t *testing.T) {
	cache, err := NewCache(t.TempDir(), 10*1024*1024)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	scopes := NewScopes()
	scopes.Grant("tok-abort", branch("org/abort-repo"))
	srv := httptest.NewServer(NewServer(Config{Cache: cache, Scopes: scopes}))
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	tok := "tok-abort"

	// 1. Reserve and never upload/commit (abandoned reservation)
	for i := 0; i < 20; i++ {
		key := fmt.Sprintf("abandon-%d", i)
		reqBody := fmt.Sprintf(`{"key":%q,"version":"v1","cacheSize":1024}`, key)
		resp, err := client.Post(SandboxURL(srv.URL, tok)+"_apis/artifactcache/caches", "application/json", strings.NewReader(reqBody))
		if err != nil {
			t.Fatalf("reserve error: %v", err)
		}
		resp.Body.Close()
	}

	// 2. Reserve, upload partial bytes, and attempt premature commit with size mismatch
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("mismatch-%d", i)
		reqBody := fmt.Sprintf(`{"key":%q,"version":"v1","cacheSize":4096}`, key)
		resp, err := client.Post(SandboxURL(srv.URL, tok)+"_apis/artifactcache/caches", "application/json", strings.NewReader(reqBody))
		if err != nil {
			t.Fatalf("reserve: %v", err)
		}
		var reserved struct {
			CacheID uint64 `json:"cacheId"`
		}
		json.NewDecoder(resp.Body).Decode(&reserved)
		resp.Body.Close()

		uploadURL := fmt.Sprintf("%s_apis/artifactcache/caches/%d", SandboxURL(srv.URL, tok), reserved.CacheID)
		// Upload only 100 bytes instead of 4096
		patchReq, _ := http.NewRequest(http.MethodPatch, uploadURL, bytes.NewReader(make([]byte, 100)))
		patchReq.Header.Set("Content-Range", "bytes 0-99/*")
		patchResp, err := client.Do(patchReq)
		if err != nil {
			t.Fatalf("patch: %v", err)
		}
		patchResp.Body.Close()

		// Commit should fail because uploaded bytes (100) != reserved size (4096)
		commitResp, err := client.Post(uploadURL, "application/json", strings.NewReader(`{"size":4096}`))
		if err != nil {
			t.Fatalf("commit: %v", err)
		}
		commitResp.Body.Close()
		if commitResp.StatusCode == http.StatusOK {
			t.Errorf("commit with size mismatch should have failed, got 200")
		}

		// Lookup must return miss (204)
		findResp, _ := client.Get(fmt.Sprintf("%s_apis/artifactcache/cache?keys=%s&version=v1", SandboxURL(srv.URL, tok), key))
		findResp.Body.Close()
		if findResp.StatusCode != http.StatusNoContent {
			t.Errorf("find for aborted entry returned status %d, want 204", findResp.StatusCode)
		}
	}

	// 3. Trigger eviction which clears stale reservations
	cache.evict()
}

// TestActionscacheSustainedParallelStress executes a sustained parallel stress test
// covering all actions cache operations simultaneously.
func TestActionscacheSustainedParallelStress(t *testing.T) {
	duration := getStressDuration("GRAIN_ACTIONSSCACHE_STRESS_DURATION", 5*time.Second)
	cache, err := NewCache(t.TempDir(), 2*1024*1024) // 2MB cache
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	scopes := NewScopes()
	scopes.Grant("tok-sustained", branch("org/sustained-repo"))
	srv := httptest.NewServer(NewServer(Config{Cache: cache, Scopes: scopes, Log: t.Logf}))
	defer srv.Close()

	deadline := time.Now().Add(duration)
	var ops atomic.Int64
	var wg sync.WaitGroup
	workers := 16

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			client := &http.Client{Timeout: 10 * time.Second}
			tok := "tok-sustained"
			i := 0

			for time.Now().Before(deadline) {
				i++
				ops.Add(1)
				key := fmt.Sprintf("sustained-w%d-%d", workerID, i%20) // Reuse keys to create hits and overwrites
				version := "v1"
				size := 1024 * (1 + (i % 8)) // 1KB to 8KB
				payload := bytes.Repeat([]byte{byte(workerID*31 + i)}, size)

				// Step 1: Lookup first
				findURL := fmt.Sprintf("%s_apis/artifactcache/cache?keys=%s&version=%s", SandboxURL(srv.URL, tok), key, version)
				if findResp, err := client.Get(findURL); err == nil {
					if findResp.StatusCode == http.StatusOK {
						var hit struct {
							ArchiveLocation string `json:"archiveLocation"`
						}
						if json.NewDecoder(findResp.Body).Decode(&hit) == nil && hit.ArchiveLocation != "" {
							if dlResp, err := client.Get(hit.ArchiveLocation); err == nil {
								io.Copy(io.Discard, dlResp.Body)
								dlResp.Body.Close()
							}
						}
					}
					findResp.Body.Close()
				}

				// Step 2: Reserve
				reserveURL := SandboxURL(srv.URL, tok) + "_apis/artifactcache/caches"
				reserveBody := fmt.Sprintf(`{"key":%q,"version":%q,"cacheSize":%d}`, key, version, size)
				resp, err := client.Post(reserveURL, "application/json", strings.NewReader(reserveBody))
				if err != nil {
					continue
				}
				var reserved struct {
					CacheID uint64 `json:"cacheId"`
				}
				err = json.NewDecoder(resp.Body).Decode(&reserved)
				resp.Body.Close()
				if err != nil || reserved.CacheID == 0 {
					continue
				}

				// Step 3: Upload
				uploadURL := fmt.Sprintf("%s_apis/artifactcache/caches/%d", SandboxURL(srv.URL, tok), reserved.CacheID)
				patchReq, _ := http.NewRequest(http.MethodPatch, uploadURL, bytes.NewReader(payload))
				patchReq.Header.Set("Content-Range", fmt.Sprintf("bytes 0-%d/*", size-1))
				patchReq.Header.Set("Content-Type", "application/octet-stream")
				if pResp, err := client.Do(patchReq); err == nil {
					pResp.Body.Close()
				} else {
					continue
				}

				// Step 4: Commit
				if cResp, err := client.Post(uploadURL, "application/json", strings.NewReader(fmt.Sprintf(`{"size":%d}`, size))); err == nil {
					cResp.Body.Close()
				}
			}
		}(w)
	}

	wg.Wait()
	t.Logf("Completed %d operations during %v sustained actions cache stress test", ops.Load(), duration)
}
