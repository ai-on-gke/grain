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

// Coalescing concurrent misses on the same object.
//
// The service's purpose is that "a wheel downloaded by one run is not
// downloaded again by the next", and without this that holds serially
// and fails concurrently: ten CI lanes starting the same job within
// seconds of each other all miss, all fetch, and all store, so the one
// case a package cache exists for -- the same packages wanted by
// everything at once -- is the case it does nothing for. Those
// duplicate fetches are also duplicate commits, which is the other half
// of what the byte count had to be taught to survive (cache.go).
//
// What is shared is the *fetch*, not its verdict, and the difference is
// the whole design:
//
//   - A follower waits for the leader and then looks on disk again. If
//     the leader stored the object, that is a hit and no second fetch
//     happened. If the leader failed, refused, got a 404, or streamed a
//     body that was cut short -- anything that leaves nothing stored --
//     the follower fetches upstream itself, exactly as it would have
//     with no coalescing at all. One upstream failure therefore stays
//     one client's failure; it does not become N of them, and a
//     follower is never told "no" on the strength of a request that was
//     not its own.
//   - A follower stops waiting the moment its own client goes away, so
//     a shared fetch can never hold a connection open past the point
//     its requester cares. Beyond that it has no deadline of its own:
//     the leader's fetch is bounded by the upstream client's timeout,
//     which is the same bound the follower's independent fetch would
//     have had, and a deadline shorter than that would spend a real
//     duplicate download to avoid an imaginary hang.
//   - A follower that ends up fetching for itself does not become a
//     leader for the next wave. Coalescing an object nothing can store
//     would serialize a queue of clients behind each other for no gain.
//
// Only cacheable requests take part. A request that may not be served
// from disk is one whose answer may differ per client (a range request,
// an index that must not be stale), and sharing a fetch between two of
// those would be sharing an answer.

import "sync"

// fetchGroup tracks which objects are being fetched right now, so a
// second request for one can wait for the first rather than starting
// its own.
type fetchGroup struct {
	mu       sync.Mutex
	inFlight map[string]chan struct{}
}

// begin registers a fetch of key. The first caller is the leader and
// must call end when its fetch is over, whether it succeeded or not;
// every other caller gets a channel that is closed at that moment.
func (g *fetchGroup) begin(key string) (wait <-chan struct{}, leader bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inFlight == nil {
		g.inFlight = map[string]chan struct{}{}
	}
	if done, ok := g.inFlight[key]; ok {
		return done, false
	}
	done := make(chan struct{})
	g.inFlight[key] = done
	return done, true
}

// end releases key and wakes everyone waiting on it. It must be
// deferred by the leader the moment begin returns: a leader that
// returned without calling it would leave every later request for that
// object waiting on a fetch that is over.
func (g *fetchGroup) end(key string) {
	g.mu.Lock()
	done, ok := g.inFlight[key]
	delete(g.inFlight, key)
	g.mu.Unlock()
	if ok {
		close(done)
	}
}

// inFlightCount is how many fetches are being shared right now. For the
// tests, which is why it is unexported.
func (g *fetchGroup) inFlightCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.inFlight)
}

// flightKey names one object across every ecosystem (Kinds). It is the
// store key rather than the URL, so that the two repositories a
// container layer can be pulled under share a fetch the way they
// already share an entry (docker.go).
func flightKey(obj object) string { return obj.kind + " " + obj.storeKey() }
