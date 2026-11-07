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

// Package perf provides operation latency, error rate, cache hit/miss, and SLO
// budget tracking for PLC Studio, the PLC Chat Gateway, and grainclient.
package perf

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"sync"
	"time"
)

// DefaultMaxSamples is the default sliding window size per operation.
const DefaultMaxSamples = 512

// SLO defines latency and error-rate objectives for a named operation.
type SLO struct {
	Operation    string  `json:"operation"`
	P95LimitMs   float64 `json:"p95_limit_ms,omitempty"`
	P99LimitMs   float64 `json:"p99_limit_ms,omitempty"`
	MaxErrorRate float64 `json:"max_error_rate,omitempty"`
}

// OpStats summarizes latency and error metrics for a single operation.
type OpStats struct {
	Operation   string  `json:"operation"`
	Count       int64   `json:"count"`
	Errors      int64   `json:"errors"`
	ErrorRate   float64 `json:"error_rate"`
	MinMs       float64 `json:"min_ms"`
	MaxMs       float64 `json:"max_ms"`
	MeanMs      float64 `json:"mean_ms"`
	P50Ms       float64 `json:"p50_ms"`
	P95Ms       float64 `json:"p95_ms"`
	P99Ms       float64 `json:"p99_ms"`
	LastMs      float64 `json:"last_ms"`
	SLOBreached bool    `json:"slo_breached,omitempty"`
}

// CacheStats summarizes hit/miss/eviction metrics for a named cache.
type CacheStats struct {
	Cache     string  `json:"cache"`
	Hits      int64   `json:"hits"`
	Misses    int64   `json:"misses"`
	Evictions int64   `json:"evictions"`
	HitRate   float64 `json:"hit_rate"`
}

// Snapshot is a point-in-time view of all recorded operations and caches.
type Snapshot struct {
	GeneratedAt   time.Time             `json:"generated_at"`
	Operations    map[string]OpStats    `json:"operations"`
	Caches        map[string]CacheStats `json:"caches"`
	SLOViolations []string              `json:"slo_violations,omitempty"`
}

type opBucket struct {
	count   int64
	errors  int64
	sumMs   float64
	minMs   float64
	maxMs   float64
	lastMs  float64
	samples []float64
	cursor  int
}

type cacheBucket struct {
	hits      int64
	misses    int64
	evictions int64
}

// Recorder tracks bounded latency samples, error counts, cache counters, and SLOs.
type Recorder struct {
	mu         sync.RWMutex
	maxSamples int
	ops        map[string]*opBucket
	caches     map[string]*cacheBucket
	slos       map[string]SLO
	now        func() time.Time
}

var defaultRecorder = NewRecorder(DefaultMaxSamples)

// Default returns the process-wide default Recorder.
func Default() *Recorder {
	return defaultRecorder
}

// NewRecorder creates a new Recorder retaining up to maxSamples durations per operation.
func NewRecorder(maxSamples int, slos ...SLO) *Recorder {
	if maxSamples <= 0 {
		maxSamples = DefaultMaxSamples
	}
	r := &Recorder{
		maxSamples: maxSamples,
		ops:        make(map[string]*opBucket),
		caches:     make(map[string]*cacheBucket),
		slos:       make(map[string]SLO),
		now:        time.Now,
	}
	for _, s := range slos {
		if s.Operation != "" {
			r.slos[s.Operation] = s
		}
	}
	return r
}

// RegisterSLO registers or updates an SLO budget for an operation.
func (r *Recorder) RegisterSLO(slo SLO) {
	if slo.Operation == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.slos[slo.Operation] = slo
}

// Start begins timing an operation and returns a completion callback.
func (r *Recorder) Start(operation string) func(err error) {
	start := r.now()
	return func(err error) {
		r.Record(operation, r.now().Sub(start), err)
	}
}

// Record records a completed operation's duration and error outcome.
func (r *Recorder) Record(operation string, duration time.Duration, err error) {
	ms := float64(duration) / float64(time.Millisecond)
	r.RecordDurationMs(operation, ms, err != nil)
}

// RecordDurationMs records a completed operation's duration in milliseconds.
func (r *Recorder) RecordDurationMs(operation string, durationMs float64, failed bool) {
	if operation == "" {
		return
	}
	if durationMs < 0 {
		durationMs = 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	b, ok := r.ops[operation]
	if !ok {
		b = &opBucket{
			minMs:   durationMs,
			maxMs:   durationMs,
			samples: make([]float64, 0, r.maxSamples),
		}
		r.ops[operation] = b
	}
	b.count++
	if failed {
		b.errors++
	}
	b.sumMs += durationMs
	if durationMs < b.minMs {
		b.minMs = durationMs
	}
	if durationMs > b.maxMs {
		b.maxMs = durationMs
	}
	b.lastMs = durationMs
	if len(b.samples) < r.maxSamples {
		b.samples = append(b.samples, durationMs)
	} else {
		b.samples[b.cursor] = durationMs
		b.cursor = (b.cursor + 1) % r.maxSamples
	}
}

// RecordCacheHit increments the hit counter for the named cache.
func (r *Recorder) RecordCacheHit(cache string) {
	if cache == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.ensureCache(cache)
	c.hits++
}

// RecordCacheMiss increments the miss counter for the named cache.
func (r *Recorder) RecordCacheMiss(cache string) {
	if cache == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.ensureCache(cache)
	c.misses++
}

// RecordCacheEviction increments the eviction counter for the named cache.
func (r *Recorder) RecordCacheEviction(cache string) {
	if cache == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.ensureCache(cache)
	c.evictions++
}

func (r *Recorder) ensureCache(cache string) *cacheBucket {
	c, ok := r.caches[cache]
	if !ok {
		c = &cacheBucket{}
		r.caches[cache] = c
	}
	return c
}

// Reset clears all recorded operation and cache metrics while preserving registered SLOs.
func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ops = make(map[string]*opBucket)
	r.caches = make(map[string]*cacheBucket)
}

// Snapshot computes a point-in-time summary of all recorded metrics and SLO checks.
func (r *Recorder) Snapshot() Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()

	snap := Snapshot{
		GeneratedAt: r.now().UTC(),
		Operations:  make(map[string]OpStats, len(r.ops)),
		Caches:      make(map[string]CacheStats, len(r.caches)),
	}

	var violations []string
	for name, b := range r.ops {
		if b.count == 0 {
			continue
		}
		sorted := make([]float64, len(b.samples))
		copy(sorted, b.samples)
		sort.Float64s(sorted)

		errRate := float64(b.errors) / float64(b.count)
		stats := OpStats{
			Operation: name,
			Count:     b.count,
			Errors:    b.errors,
			ErrorRate: round4(errRate),
			MinMs:     round3(b.minMs),
			MaxMs:     round3(b.maxMs),
			MeanMs:    round3(b.sumMs / float64(b.count)),
			P50Ms:     round3(percentile(sorted, 0.50)),
			P95Ms:     round3(percentile(sorted, 0.95)),
			P99Ms:     round3(percentile(sorted, 0.99)),
			LastMs:    round3(b.lastMs),
		}

		if slo, ok := r.slos[name]; ok {
			if slo.P95LimitMs > 0 && stats.P95Ms > slo.P95LimitMs {
				stats.SLOBreached = true
				violations = append(violations, fmt.Sprintf("%s: p95 %.2fms > limit %.2fms", name, stats.P95Ms, slo.P95LimitMs))
			}
			if slo.P99LimitMs > 0 && stats.P99Ms > slo.P99LimitMs {
				stats.SLOBreached = true
				violations = append(violations, fmt.Sprintf("%s: p99 %.2fms > limit %.2fms", name, stats.P99Ms, slo.P99LimitMs))
			}
			if slo.MaxErrorRate > 0 && stats.ErrorRate > slo.MaxErrorRate {
				stats.SLOBreached = true
				violations = append(violations, fmt.Sprintf("%s: error_rate %.4f > limit %.4f", name, stats.ErrorRate, slo.MaxErrorRate))
			}
		}

		snap.Operations[name] = stats
	}

	sort.Strings(violations)
	snap.SLOViolations = violations

	for name, c := range r.caches {
		total := c.hits + c.misses
		var hitRate float64
		if total > 0 {
			hitRate = round4(float64(c.hits) / float64(total))
		}
		snap.Caches[name] = CacheStats{
			Cache:     name,
			Hits:      c.hits,
			Misses:    c.misses,
			Evictions: c.evictions,
			HitRate:   hitRate,
		}
	}

	return snap
}

func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if q <= 0 {
		return sorted[0]
	}
	if q >= 1 {
		return sorted[len(sorted)-1]
	}
	idx := int(math.Ceil(q*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func round3(v float64) float64 {
	return math.Round(v*1000) / 1000
}

func round4(v float64) float64 {
	return math.Round(v*10000) / 10000
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Flush() {
	if f, ok := sr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Middleware wraps an http.Handler and records request latency and HTTP 5xx errors.
func (r *Recorder) Middleware(operation string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		start := r.now()
		sr := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sr, req)
		var err error
		if sr.status >= http.StatusInternalServerError {
			err = fmt.Errorf("http status %d", sr.status)
		}
		op := operation
		if op == "" {
			op = req.Method + " " + req.URL.Path
		}
		r.Record(op, r.now().Sub(start), err)
	})
}

// Handler returns an http.Handler serving the JSON Snapshot of the Recorder.
func (r *Recorder) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(r.Snapshot())
	})
}
