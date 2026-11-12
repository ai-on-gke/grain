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

package qa

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime/metrics"
	"strings"
	"sync"
	"time"
)

const (
	metricHeapAllocsBytes   = "/gc/heap/allocs:bytes"
	metricHeapAllocsObjects = "/gc/heap/allocs:objects"
)

// PerfSample captures the wall-clock duration and Go heap allocation delta of one QA stage/check.
type PerfSample struct {
	Timestamp  time.Time `json:"ts"`
	TaskID     string    `json:"taskId,omitempty"`
	RunID      string    `json:"runId,omitempty"`
	Stage      string    `json:"stage"`
	CheckID    string    `json:"checkId"`
	Arm        string    `json:"arm,omitempty"`
	Shadow     bool      `json:"shadow,omitempty"`
	Verdict    string    `json:"verdict,omitempty"`
	DurationMS int64     `json:"durationMs"`
	DurationUS int64     `json:"durationUs"`
	AllocBytes int64     `json:"allocBytes"`
	Mallocs    uint64    `json:"mallocs"`
}

// Profiler records execution timings and memory allocation deltas for QA pipeline stages
// to an append-only local JSONL file so latency/resource regressions are caught early.
type Profiler struct {
	mu   sync.Mutex
	path string
}

// NewProfiler returns a Profiler writing JSONL samples to logPath. An empty logPath disables
// file writes while still returning measured PerfSample values in memory.
func NewProfiler(logPath string) *Profiler {
	return &Profiler{path: strings.TrimSpace(logPath)}
}

// DefaultPerfLogPath returns <dataDir>/perf/qa-checks.jsonl when dataDir is non-empty,
// falling back to ~/.perf/grain-qa-checks.jsonl.
func DefaultPerfLogPath(dataDir string) string {
	if d := strings.TrimSpace(dataDir); d != "" {
		return filepath.Join(d, "perf", "qa-checks.jsonl")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".perf", "grain-qa-checks.jsonl")
	}
	return ""
}

// Path returns the JSONL file path configured on p.
func (p *Profiler) Path() string {
	if p == nil {
		return ""
	}
	return p.path
}

func readHeapCounters() (allocBytes uint64, allocObjects uint64) {
	samples := [2]metrics.Sample{
		{Name: metricHeapAllocsBytes},
		{Name: metricHeapAllocsObjects},
	}
	metrics.Read(samples[:])
	if samples[0].Value.Kind() == metrics.KindUint64 {
		allocBytes = samples[0].Value.Uint64()
	}
	if samples[1].Value.Kind() == metrics.KindUint64 {
		allocObjects = samples[1].Value.Uint64()
	}
	return allocBytes, allocObjects
}

// Measure executes fn, captures wall-clock duration and lock-free runtime/metrics heap deltas
// (never triggering a Stop-The-World runtime.ReadMemStats pause), and appends the resulting
// PerfSample to p's JSONL log (best-effort, non-fatal).
func (p *Profiler) Measure(meta PerfSample, fn func() (verdict string, err error)) (PerfSample, error) {
	beforeBytes, beforeObjs := readHeapCounters()
	start := time.Now()

	verdict, fnErr := fn()

	elapsed := time.Since(start)
	afterBytes, afterObjs := readHeapCounters()

	var allocDelta int64
	if afterBytes >= beforeBytes {
		allocDelta = int64(afterBytes - beforeBytes)
	}
	var mallocDelta uint64
	if afterObjs >= beforeObjs {
		mallocDelta = afterObjs - beforeObjs
	}

	sample := meta
	sample.Timestamp = start.UTC()
	if verdict != "" {
		sample.Verdict = verdict
	} else if fnErr != nil && sample.Verdict == "" {
		sample.Verdict = "error"
	}
	sample.DurationUS = elapsed.Microseconds()
	sample.DurationMS = elapsed.Milliseconds()
	sample.AllocBytes = allocDelta
	sample.Mallocs = mallocDelta

	if p != nil && p.path != "" {
		_ = p.appendSample(sample)
	}
	return sample, fnErr
}

func (p *Profiler) appendSample(s PerfSample) error {
	line, err := json.Marshal(s)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	p.mu.Lock()
	defer p.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(p.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(line)
	return err
}
