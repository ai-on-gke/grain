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

package github

// conditional.go is how a poll that reads the same bytes over and over
// stops paying for them.
//
// grain is a poller. Every thirty seconds a reconcile cycle reads each
// open pull request, the check runs on its head commit and the head of
// each branch it is testing; another loop reads the open pull requests
// on the state repository; a run waiting on CI re-reads its own checks
// every fifteen seconds for as long as its build takes. Almost every one
// of those reads returns exactly the bytes the last one did -- a pull
// request nobody has touched, a check list that has not moved -- and
// each one used to cost a request against a budget of 5,000 an hour.
// The arithmetic is in docs/github-api-rate-limits.md; the short of it
// is that a deployment with a dozen open pull requests and its workers
// busy spends most of that budget re-reading unchanged JSON.
//
// GitHub's answer to this is conditional requests, and it is the reason
// every response here carries an `ETag`. Send it back as `If-None-Match`
// and an unchanged resource answers `304 Not Modified` with no body --
// and, the part that matters, a 304 does not count against the primary
// rate limit at all. GitHub documents that explicitly. So a poll of
// something that has not changed becomes free, and the budget is spent
// only on reads that actually said something new.
//
// What this must not do is change a single answer. The cache is keyed by
// the request as sent -- method, path, the Accept it asked for and the
// credential it asked as -- and it is only ever consulted to fill in a
// 304, which is GitHub itself asserting the entity is byte-identical to
// the one this cache is holding. There is no expiry heuristic here and
// no "probably still fresh": nothing is served from the cache that
// GitHub has not just been asked about.
//
// GETs only, and only responses GitHub gave an ETag. A write has nothing
// to revalidate, and the redirect-and-download shape a job log arrives
// by (jobLog) carries no validator to send back.
//
// It is off in the zero value and on in NewRealTransport, deliberately:
// the struct-literal RealTransport is the mocked-GitHub test seam (see
// UseTLS's own doc comment), and a seam whose whole job is to observe
// the requests a client makes should observe all of them.

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"strings"
	"sync"
)

// The cache's two bounds. Both are about a daemon that runs for weeks:
// the entry count is what stops a deployment with a long history of
// pull requests accumulating one entry per number it has ever read, and
// the byte ceiling is what stops a handful of very large listings (a
// hundred check runs with their HTML urls, a pull request's file list)
// standing for the whole of it.
//
// A single response larger than maxCachedEntryBytes is not cached at
// all rather than evicting everything else to fit: the reads worth
// caching here are small and frequent, and one big one is neither.
//
// The working set these have to hold is what one reconcile cycle polls:
// about thirty open pull requests, each with its pull request, its head's
// check runs, its review comments and its branch head, plus the base
// branches of three repositories -- some 150 entries per credential, a
// few KiB each. 512 held that with no room to spare once single-use reads
// (a compare, a pull request's file list, the check runs of a superseded
// head) were mixed in, and each of those pushed out an entry a poll was
// about to revalidate. Evictions are counted (Stats.Cache) so whether
// these still hold the working set is something a deployment can see.
const (
	maxCachedEntries    = 2048
	maxCachedBytes      = 32 << 20
	maxCachedEntryBytes = 1 << 20
)

// responseCache is the ETag store: least-recently-used, bounded twice
// over, and safe for the concurrent callers a reconcile cycle's
// dispatch goroutines and a UI request make of one transport.
type responseCache struct {
	mu    sync.Mutex
	order *list.List               // front is most recently used
	items map[string]*list.Element // key -> element holding a *cachedResponse
	bytes int
	// evicted is entries dropped to make room, which is the number
	// that says whether the bounds above hold the working set.
	evicted int64
}

// CacheStats is the ETag cache's occupancy.
type CacheStats struct {
	Entries    int   `json:"entries"`
	Bytes      int   `json:"bytes"`
	MaxEntries int   `json:"maxEntries"`
	MaxBytes   int   `json:"maxBytes"`
	Evicted    int64 `json:"evicted"`
}

func (c *responseCache) stats() CacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return CacheStats{Entries: c.order.Len(), Bytes: c.bytes, MaxEntries: maxCachedEntries,
		MaxBytes: maxCachedBytes, Evicted: c.evicted}
}

// cachedResponse is one revalidatable answer: the validator to send back
// and the whole response to return when GitHub says it still stands.
//
// The response is kept entire, headers included, because the headers are
// part of the answer -- `Link` is what every paginated read here walks
// (nextPagePath), and a 304 carries no `Link` of its own.
type cachedResponse struct {
	key  string
	etag string
	resp ApiResponse
	size int
}

func newResponseCache() *responseCache {
	return &responseCache{order: list.New(), items: map[string]*list.Element{}}
}

// cacheKey is the request as sent, reduced to a string.
//
// The credential is in it because GitHub's answer depends on who asked:
// the same path can be a 200 for one token and a 404 for another (a
// private repository the second cannot see), and a deployment whose
// ladder holds several tokens routes different repositories through
// different ones. It is hashed rather than kept, so a cache dumped in a
// panic or a heap profile carries no secret.
//
// Accept is in it because it selects the representation, and this
// package sends more than one (the raw diff media type among them).
func cacheKey(method, path string, headers map[string]string) string {
	sum := sha256.Sum256([]byte(headerValue(headers, "Authorization")))
	return method + "\x00" + path + "\x00" + headerValue(headers, "Accept") + "\x00" +
		hex.EncodeToString(sum[:8])
}

// lookup returns the validator to send and the answer to return if
// GitHub says it still holds.
func (c *responseCache) lookup(key string) (*cachedResponse, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*cachedResponse), true
}

// touch marks an entry used, which is what keeps a resource that is
// polled constantly and answers 304 every time from ageing out from
// under the poll that is the whole reason to hold it.
func (c *responseCache) touch(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.order.MoveToFront(el)
	}
}

// store records a fresh answer, replacing whatever this key held.
//
// An answer with no ETag drops the key rather than leaving the old entry
// in place: the resource has stopped offering a validator, and holding a
// stale one would mean sending an If-None-Match GitHub could only ever
// answer 200 to.
func (c *responseCache) store(key, etag string, resp ApiResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.bytes -= el.Value.(*cachedResponse).size
		c.order.Remove(el)
		delete(c.items, key)
	}
	if etag == "" {
		return
	}
	size := len(resp.Body)
	if size > maxCachedEntryBytes {
		return
	}
	entry := &cachedResponse{key: key, etag: etag, resp: copyResponse(resp), size: size}
	c.items[key] = c.order.PushFront(entry)
	c.bytes += size
	for c.order.Len() > maxCachedEntries || c.bytes > maxCachedBytes {
		oldest := c.order.Back()
		if oldest == nil {
			return
		}
		old := oldest.Value.(*cachedResponse)
		c.order.Remove(oldest)
		delete(c.items, old.key)
		c.bytes -= old.size
		c.evicted++
	}
}

// copyResponse is a deep enough copy that nothing handed to a caller
// shares a map or a slice with the cache.
//
// Headers are copied because a caller is handed the map itself and could
// write to it; the body is copied because a caller that appends to a
// returned slice would otherwise be writing into the cached entry. Both
// are cheap next to the request they replace.
func copyResponse(resp ApiResponse) ApiResponse {
	out := ApiResponse{Status: resp.Status}
	if resp.Headers != nil {
		out.Headers = maps.Clone(resp.Headers)
	}
	if resp.Body != nil {
		out.Body = append([]byte(nil), resp.Body...)
	}
	return out
}

// InvalidatePrefix drops every cached conditional GET whose API path begins
// with pathPrefix (for example "/repos/acme/widgets"), forcing the next read
// of those resources to ask GitHub unconditionally when an inbound webhook
// has already reported a state change.
func (t *RealTransport) InvalidatePrefix(pathPrefix string) {
	if t == nil || pathPrefix == "" {
		return
	}
	t.init()
	t.cache.invalidatePrefix(pathPrefix)
}

func (c *responseCache) invalidatePrefix(pathPrefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, el := range c.items {
		_, rest, ok := strings.Cut(key, "\x00")
		if !ok {
			continue
		}
		path, _, ok := strings.Cut(rest, "\x00")
		if !ok {
			continue
		}
		if strings.HasPrefix(path, pathPrefix) {
			c.bytes -= el.Value.(*cachedResponse).size
			c.order.Remove(el)
			delete(c.items, key)
		}
	}
}
