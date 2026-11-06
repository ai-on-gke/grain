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

package perf_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/grain-plc/pkg/perf"
)

func TestRecorderPercentilesCachesAndSLO(t *testing.T) {
	r := perf.NewRecorder(100, perf.SLO{
		Operation:    "grain.create_task",
		P95LimitMs:   50,
		MaxErrorRate: 0.05,
	})

	for i := 1; i <= 20; i++ {
		var err error
		if i == 20 {
			err = errors.New("upstream timeout")
		}
		r.Record("grain.create_task", time.Duration(i*5)*time.Millisecond, err)
	}

	r.RecordCacheHit("directory")
	r.RecordCacheHit("directory")
	r.RecordCacheMiss("directory")
	r.RecordCacheEviction("directory")

	snap := r.Snapshot()
	op, ok := snap.Operations["grain.create_task"]
	if !ok {
		t.Fatalf("missing grain.create_task stats in %+v", snap.Operations)
	}
	if op.Count != 20 || op.Errors != 1 {
		t.Fatalf("count=%d errors=%d, want 20 and 1", op.Count, op.Errors)
	}
	if op.MinMs != 5 || op.MaxMs != 100 {
		t.Fatalf("min=%v max=%v, want 5 and 100", op.MinMs, op.MaxMs)
	}
	if op.P50Ms != 50 || op.P95Ms != 95 {
		t.Fatalf("p50=%v p95=%v, want 50 and 95", op.P50Ms, op.P95Ms)
	}
	if !op.SLOBreached || len(snap.SLOViolations) == 0 {
		t.Fatalf("expected SLO breach for p95=95ms > 50ms, got op=%+v violations=%v", op, snap.SLOViolations)
	}

	cs, ok := snap.Caches["directory"]
	if !ok {
		t.Fatalf("missing directory cache stats in %+v", snap.Caches)
	}
	if cs.Hits != 2 || cs.Misses != 1 || cs.Evictions != 1 {
		t.Fatalf("cache stats = %+v, want hits=2 misses=1 evictions=1", cs)
	}
	if cs.HitRate < 0.66 || cs.HitRate > 0.67 {
		t.Fatalf("hit rate = %v, want ~0.6667", cs.HitRate)
	}

	r.Reset()
	afterReset := r.Snapshot()
	if len(afterReset.Operations) != 0 || len(afterReset.Caches) != 0 {
		t.Fatalf("expected empty snapshot after reset, got %+v", afterReset)
	}
}

func TestMiddlewareAndHandler(t *testing.T) {
	r := perf.NewRecorder(16)
	mux := http.NewServeMux()
	mux.Handle("/work", r.Middleware("http.work", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Query().Get("fail") == "1" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})))
	mux.Handle("/perf", r.Handler())

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp1, err := http.Get(srv.URL + "/work")
	if err != nil {
		t.Fatalf("GET /work: %v", err)
	}
	resp1.Body.Close()

	resp2, err := http.Get(srv.URL + "/work?fail=1")
	if err != nil {
		t.Fatalf("GET /work?fail=1: %v", err)
	}
	resp2.Body.Close()

	perfResp, err := http.Get(srv.URL + "/perf")
	if err != nil {
		t.Fatalf("GET /perf: %v", err)
	}
	defer perfResp.Body.Close()
	if perfResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /perf status = %d, want 200", perfResp.StatusCode)
	}
	var snap perf.Snapshot
	if err := json.NewDecoder(perfResp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode perf snapshot: %v", err)
	}
	stats, ok := snap.Operations["http.work"]
	if !ok || stats.Count != 2 || stats.Errors != 1 {
		t.Fatalf("http.work stats = %+v, want count=2 errors=1", stats)
	}
}
