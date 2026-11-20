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

package model

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gates"
)

func TestGateObservationViewCacheRetainsMultipleGenerationsPerTask(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	cache := &gateObservationViewCache{}
	configs := map[shadowHistoryConfig]bool{}

	raws := make(map[uint64]string, 5)
	for gen := uint64(1); gen <= 5; gen++ {
		v := gates.UnavailableObservation("task-1", now.Add(time.Duration(gen)*time.Second))
		v.SourceRevision = fmt.Sprintf("sha256:gen-%d", gen)
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		raws[gen] = string(b)
		if _, ok := cache.decodeHistory("task-1", gen, raws[gen], configs); !ok {
			t.Fatalf("decodeHistory gen %d failed", gen)
		}
	}

	if got := len(cache.history); got != 5 {
		t.Fatalf("cache.history has %d entries, want all 5 generations retained", got)
	}
	for gen := uint64(1); gen <= 5; gen++ {
		hk := cachedHistoryKey{taskID: "task-1", generation: gen}
		entry, ok := cache.history[hk]
		if !ok || entry.raw != raws[gen] || entry.rawHash == "" {
			t.Fatalf("generation %d missing from cache.history: ok=%t", gen, ok)
		}
		if got := cache.rawHashOf("task-1", gen, raws[gen]); got != entry.rawHash {
			t.Fatalf("rawHashOf gen %d = %q, want %q", gen, got, entry.rawHash)
		}
		// Different raw bytes for the same generation must never return the cached hash.
		if got := cache.rawHashOf("task-1", gen, raws[gen]+" "); got == entry.rawHash {
			t.Fatalf("rawHashOf gen %d matched mutated raw JSON", gen)
		}
	}

	// Verify LRU eviction enforces maxCachedGateHistoryViews.
	for i := 0; i < maxCachedGateHistoryViews+10; i++ {
		tid := fmt.Sprintf("task-lru-%d", i)
		gen := uint64(i + 1)
		if _, ok := cache.decodeHistory(tid, gen, raws[1], configs); !ok {
			t.Fatalf("decodeHistory %s failed", tid)
		}
	}
	if got := len(cache.history); got != maxCachedGateHistoryViews {
		t.Fatalf("cache.history size = %d, want capped at %d", got, maxCachedGateHistoryViews)
	}
}
