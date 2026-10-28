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

// What the cache believes it is holding, against what is actually on
// disk. The two used to be allowed to diverge permanently -- a commit
// that replaced an existing entry added its whole size to the count, and
// nothing anywhere ever measured the store again while the process
// lived -- so these tests are written as "walk the directory and compare
// with Bytes()" rather than as "check the arithmetic".

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// onDisk is what the cache directory really holds, counted the way the
// cache counts it.
func onDisk(t *testing.T, c *Cache) int64 {
	t.Helper()
	entries, err := c.walk()
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	var total int64
	for _, e := range entries {
		total += e.size
	}
	return total
}

// entriesOnDisk is how many objects the cache directory really holds.
func entriesOnDisk(t *testing.T, c *Cache) int {
	t.Helper()
	entries, err := c.walk()
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return len(entries)
}

// wantAccounted holds both running counts to the same standard: what is
// on disk. Entries is the rail's Proxy cache number (grain/task-333) and
// is kept exactly the way the byte count is, so every case below that
// used to drift one of them would have drifted the other.
func wantAccounted(t *testing.T, c *Cache, when string) {
	t.Helper()
	if got, want := c.Bytes(), onDisk(t, c); got != want {
		t.Errorf("%s: the cache believes it holds %d bytes, the disk holds %d", when, got, want)
	}
	if got, want := c.Entries(), entriesOnDisk(t, c); got != want {
		t.Errorf("%s: the cache believes it holds %d entries, the disk holds %d", when, got, want)
	}
}

func store(t *testing.T, c *Cache, kind, url string, size int) {
	t.Helper()
	w, err := c.NewWriter(kind, url)
	if err != nil {
		t.Fatalf("NewWriter(%s): %v", url, err)
	}
	if _, err := w.Write(make([]byte, size)); err != nil {
		t.Fatalf("Write(%s): %v", url, err)
	}
	if !w.Commit(Meta{}) {
		t.Fatalf("Commit(%s) stored nothing", url)
	}
}

// Two runs fetching the same package at once is the documented, expected
// end of a race -- one rename wins -- and it used to add two copies'
// worth of bytes to a store holding one.
func TestBytesMatchesDiskAfterADuplicateCommit(t *testing.T) {
	c, err := NewCache(t.TempDir(), 1<<30)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	const url = "http://deb.debian.org/debian/pool/main/c/cpio/cpio_2.13_amd64.deb"
	for i := 0; i < 3; i++ {
		store(t, c, KindAPT, url, 1000)
		wantAccounted(t, c, fmt.Sprintf("after commit %d of the same object", i+1))
	}

	// And a commit that replaces a larger entry with a smaller one has
	// to take the count down, not up.
	const other = "http://deb.debian.org/debian/pool/main/c/cpio/cpio_2.14_amd64.deb"
	store(t, c, KindAPT, other, 9000)
	store(t, c, KindAPT, other, 10)
	wantAccounted(t, c, "after an entry was replaced by a smaller one")
}

// The same thing through the whole service, which is where it actually
// happened: several sandboxes asking for one package at the same moment.
func TestBytesMatchesDiskAfterConcurrentMisses(t *testing.T) {
	const pkg = "/debian/pool/main/h/hello/hello_2.10-3_amd64.deb"
	up := newUpstream(t, map[string]string{pkg: strings.Repeat("x", 4096)})
	s, cache := newTestServer(t, Config{Client: up.client()})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://deb.debian.org"+pkg, nil))
			if rec.Code != http.StatusOK {
				t.Errorf("GET %s = %d, want 200", pkg, rec.Code)
			}
		}()
	}
	wg.Wait()
	wantAccounted(t, cache, "after eight concurrent requests for one package")
}

func TestBytesMatchesDiskAfterEviction(t *testing.T) {
	c, err := NewCache(t.TempDir(), 40*1024)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	for i := 0; i < 20; i++ {
		store(t, c, KindNPM, fmt.Sprintf("http://registry.npmjs.org/pkg-%d.tgz", i), 4096)
	}
	if c.Bytes() > c.MaxBytes() {
		t.Errorf("after eviction the cache holds %d bytes, past its %d-byte bound", c.Bytes(), c.MaxBytes())
	}
	wantAccounted(t, c, "after eviction")
}

func TestBytesMatchesDiskAfterAFlush(t *testing.T) {
	c, err := NewCache(t.TempDir(), 1<<30)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	for i := 0; i < 4; i++ {
		store(t, c, KindNPM, fmt.Sprintf("http://registry.npmjs.org/pkg-%d.tgz", i), 1000)
		store(t, c, KindGo, fmt.Sprintf("http://proxy.golang.org/mod-%d.zip", i), 1000)
	}
	// A duplicate, so that the count being flushed is one that would
	// have drifted before this change.
	store(t, c, KindNPM, "http://registry.npmjs.org/pkg-0.tgz", 1000)

	if _, _, err := c.Flush(KindNPM); err != nil {
		t.Fatalf("Flush(npm): %v", err)
	}
	wantAccounted(t, c, "after flushing one ecosystem")

	if _, _, err := c.Flush(""); err != nil {
		t.Fatalf("Flush(all): %v", err)
	}
	wantAccounted(t, c, "after flushing everything")
	if c.Bytes() != 0 {
		t.Errorf("a flushed cache reports a phantom balance of %d bytes", c.Bytes())
	}
}

// The entry count is what the sidebar draws beside its Proxy cache
// entry, and it is answered from memory rather than from a walk -- so
// what it says has to survive the things that change the store without
// anybody walking it: a duplicate commit, a restart, and a flush of one
// ecosystem while another is left alone.
func TestEntriesCountsWhatTheStoreHolds(t *testing.T) {
	dir := t.TempDir()
	c, err := NewCache(dir, 1<<30)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	if c.Entries() != 0 {
		t.Errorf("a new cache holds %d entries, want none", c.Entries())
	}
	for i := 0; i < 3; i++ {
		store(t, c, KindNPM, fmt.Sprintf("http://registry.npmjs.org/pkg-%d.tgz", i), 1000)
	}
	store(t, c, KindGo, "http://proxy.golang.org/mod.zip", 1000)
	if c.Entries() != 4 {
		t.Errorf("Entries() = %d after storing four objects, want 4", c.Entries())
	}

	// Storing the same object again replaces it: the store holds what it
	// held.
	store(t, c, KindNPM, "http://registry.npmjs.org/pkg-0.tgz", 2000)
	if c.Entries() != 4 {
		t.Errorf("Entries() = %d after re-storing an object, want 4", c.Entries())
	}
	wantAccounted(t, c, "after a duplicate commit")

	// Stats walks the store, and what it counts is what Entries says.
	stats, err := c.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	var walked int
	for _, s := range stats {
		walked += s.Entries
	}
	if walked != c.Entries() {
		t.Errorf("Stats counted %d entries, Entries() says %d", walked, c.Entries())
	}

	if _, _, err := c.Flush(KindNPM); err != nil {
		t.Fatalf("Flush(npm): %v", err)
	}
	if c.Entries() != 1 {
		t.Errorf("Entries() = %d after flushing npm, want the one Go module left", c.Entries())
	}
	wantAccounted(t, c, "after flushing one ecosystem")

	// A restart learns the count from the directory, the way it learns
	// the size -- otherwise the rail would read 0 until something was
	// cached again.
	reopened, err := NewCache(dir, 1<<30)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	if reopened.Entries() != 1 {
		t.Errorf("a reopened cache holds %d entries, want 1", reopened.Entries())
	}
}

// The reason the drift mattered rather than merely being untidy: an
// inflated count made eviction throw away entries that were not over any
// bound, which caused more misses, which caused more duplicate commits.
func TestEvictionBelievesTheDiskOverADriftedCount(t *testing.T) {
	c, err := NewCache(t.TempDir(), 100*1024)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	for i := 0; i < 4; i++ {
		store(t, c, KindAPT, fmt.Sprintf("http://deb.debian.org/pkg-%d.deb", i), 1024)
	}
	before := onDisk(t, c)

	// Whatever the count says, these four entries are well inside the
	// bound and none of them may be thrown away. grew is what a commit
	// calls, so this is the shape the drift really had.
	c.grew(200*1024, 0)

	if got := onDisk(t, c); got != before {
		t.Errorf("eviction threw away live entries on the strength of a wrong count: %d bytes on disk, was %d", got, before)
	}
	wantAccounted(t, c, "after an eviction triggered by a count that had drifted")
}

// blockingUpstream serves one file, counts fetches of it, and holds
// every request inside the handler until the test lets it go -- which is
// how a test can have several requests in flight for one object at once.
type blockingUpstream struct {
	srv     *httptest.Server
	arrived chan struct{}
	release chan struct{}

	mu      sync.Mutex
	fetches int
}

func newBlockingUpstream(t *testing.T, body string, status func(fetch int) int) *blockingUpstream {
	t.Helper()
	u := &blockingUpstream{arrived: make(chan struct{}, 64), release: make(chan struct{})}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.fetches++
		n := u.fetches
		u.mu.Unlock()
		u.arrived <- struct{}{}
		<-u.release
		if code := status(n); code != http.StatusOK {
			http.Error(w, "upstream said no", code)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *blockingUpstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.fetches
}

// client sends every request to this upstream whatever host it names,
// the way the ordinary test upstream's does.
func (u *blockingUpstream) client(t *testing.T) *http.Client {
	t.Helper()
	base, err := url.Parse(u.srv.URL)
	if err != nil {
		t.Fatalf("parsing %s: %v", u.srv.URL, err)
	}
	return &http.Client{Transport: roundTripTo{base: base}}
}

// Ten CI lanes starting the same job is the normal path in this
// deployment, and it used to mean ten downloads of one package.
func TestConcurrentMissesFetchUpstreamOnce(t *testing.T) {
	const pkg = "/debian/pool/main/h/hello/hello_2.10-3_amd64.deb"
	const body = "the package bytes"
	up := newBlockingUpstream(t, body, func(int) int { return http.StatusOK })
	s, _ := newTestServer(t, Config{Client: up.client(t)})

	const clients = 6
	codes := make(chan string, clients)
	for i := 0; i < clients; i++ {
		go func() {
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://deb.debian.org"+pkg, nil))
			codes <- fmt.Sprintf("%d %s", rec.Code, rec.Body.String())
		}()
	}

	// Wait until one request is inside the upstream handler, then give
	// the rest time to pile up behind it. Without coalescing they pile
	// up in the handler, and the count below sees it.
	<-up.arrived
	time.Sleep(100 * time.Millisecond)
	close(up.release)

	for i := 0; i < clients; i++ {
		if got, want := <-codes, "200 "+body; got != want {
			t.Errorf("client %d got %q, want %q", i, got, want)
		}
	}
	if got := up.count(); got != 1 {
		t.Errorf("%d concurrent requests for one package made %d upstream fetches, want 1", clients, got)
	}
}

// Share the fetch, not the verdict: a follower whose leader came back
// with nothing storable fetches for itself rather than being handed the
// leader's failure.
func TestASharedFetchThatFailsDoesNotFailTheOthers(t *testing.T) {
	const pkg = "/debian/pool/main/h/hello/hello_2.10-3_amd64.deb"
	const body = "the package bytes"
	up := newBlockingUpstream(t, body, func(fetch int) int {
		if fetch == 1 {
			return http.StatusInternalServerError
		}
		return http.StatusOK
	})
	s, _ := newTestServer(t, Config{Client: up.client(t)})

	const followers = 3
	results := make(chan int, followers+1)
	go func() {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://deb.debian.org"+pkg, nil))
		results <- rec.Code
	}()
	<-up.arrived
	for i := 0; i < followers; i++ {
		go func() {
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://deb.debian.org"+pkg, nil))
			if rec.Code == http.StatusOK && rec.Body.String() != body {
				t.Errorf("a follower got %q, want %q", rec.Body.String(), body)
			}
			results <- rec.Code
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(up.release)

	var ok, failed int
	for i := 0; i < followers+1; i++ {
		if code := <-results; code == http.StatusOK {
			ok++
		} else {
			failed++
		}
	}
	if failed != 1 {
		t.Errorf("one upstream failure became %d client failures", failed)
	}
	if ok != followers {
		t.Errorf("%d of %d followers were served, want all of them", ok, followers)
	}
}

// An object too big to store is fetched from upstream for ever, and the
// only thing that distinguishes that from a broken cache is somebody
// having said so.
func TestAnObjectOverThePerEntryLimitSaysSo(t *testing.T) {
	const pkg = "/debian/pool/main/b/big/big_1.0_amd64.deb"
	up := newUpstream(t, map[string]string{pkg: strings.Repeat("x", 4096)})
	cache, err := NewCache(t.TempDir(), 1<<30)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	cache.maxEntry = 1024

	var logged []string
	var mu sync.Mutex
	s := NewServer(Config{Cache: cache, Client: up.client(), Log: func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logged = append(logged, fmt.Sprintf(format, args...))
	}})

	for i := 0; i < 2; i++ {
		rec := get(t, s, "http://deb.debian.org"+pkg)
		if rec.Code != http.StatusOK || rec.Body.Len() != 4096 {
			t.Fatalf("an object too big to cache must still be served whole: %d, %d bytes", rec.Code, rec.Body.Len())
		}
	}
	if up.hits[pkg] != 2 {
		t.Errorf("the object was fetched %d times, want 2 -- nothing should have been stored", up.hits[pkg])
	}
	if cache.Bytes() != 0 {
		t.Errorf("the cache accounted for %d bytes it did not store", cache.Bytes())
	}

	mu.Lock()
	defer mu.Unlock()
	var said int
	for _, line := range logged {
		if strings.Contains(line, "larger than") && strings.Contains(line, pkg) {
			said++
		}
	}
	if said != 2 {
		t.Errorf("the cache said %d times that it would not store this; want once per fetch, so 2.\nlogged: %v", said, logged)
	}
}

func TestFetchGroupReleasesEveryWaiter(t *testing.T) {
	var g fetchGroup
	wait, leader := g.begin("apt http://example/x")
	if !leader {
		t.Fatal("the first caller must be the leader")
	}
	if _, leader := g.begin("apt http://example/x"); leader {
		t.Fatal("the second caller for one key must not also be a leader")
	}
	if _, leader := g.begin("apt http://example/y"); !leader {
		t.Fatal("a different object is a different fetch")
	}
	g.end("apt http://example/x")
	select {
	case <-wait:
	case <-time.After(time.Second):
		t.Fatal("ending a fetch must wake everything waiting on it")
	}
	if got := g.inFlightCount(); got != 1 {
		t.Errorf("%d fetches still registered, want only the one that has not ended", got)
	}
	// Ending something that was never begun, or twice, is not a panic:
	// the leader's end is deferred and the map is emptied by a flush of
	// nothing.
	g.end("apt http://example/x")
}
