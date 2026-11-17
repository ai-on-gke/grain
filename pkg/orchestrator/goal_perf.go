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

package orchestrator

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
	metricHeapAllocBytes   = "/gc/heap/allocs:bytes"
	metricHeapAllocObjects = "/gc/heap/allocs:objects"
)

// GoalPerfSample is one append-only JSONL record capturing runtime/metrics heap
// allocation deltas and wall-clock duration for a single Goal Mode pass.
type GoalPerfSample struct {
	TaskID           string    `json:"taskId"`
	RunID            string    `json:"runId"`
	Pass             int       `json:"pass"`
	Phase            string    `json:"phase,omitempty"`
	DurationMs       int64     `json:"durationMs"`
	DurationUs       int64     `json:"durationUs"`
	HeapAllocBytes   uint64    `json:"heapAllocBytes"`
	HeapAllocObjects uint64    `json:"heapAllocObjects"`
	Verified         bool      `json:"verified"`
	RecordedAt       time.Time `json:"recordedAt"`
}

var goalPerfFileMu sync.Mutex

// GoalPerfProfiler writes per-pass GoalPerfSample records to an append-only
// JSONL file (<dataDir>/perf/goal-mode.jsonl, or ~/.perf/grain-goal-mode.jsonl
// when dataDir is empty). All I/O errors fail open so profiling never disrupts
// task execution.
type GoalPerfProfiler struct {
	mu      sync.Mutex
	dataDir string
}

// NewGoalPerfProfiler creates a GoalPerfProfiler writing under dataDir.
func NewGoalPerfProfiler(dataDir string) *GoalPerfProfiler {
	return &GoalPerfProfiler{dataDir: strings.TrimSpace(dataDir)}
}

// Path returns the JSONL output file path for this profiler.
func (p *GoalPerfProfiler) Path() string {
	if p != nil && p.dataDir != "" {
		return filepath.Join(p.dataDir, "perf", "goal-mode.jsonl")
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		home = os.TempDir()
	}
	return filepath.Join(home, ".perf", "grain-goal-mode.jsonl")
}

// GoalPerfSpan tracks an in-flight Goal Mode pass measurement.
type GoalPerfSpan struct {
	profiler     *GoalPerfProfiler
	taskID       string
	runID        string
	pass         int
	phase        string
	startedAt    time.Time
	startBytes   uint64
	startObjects uint64
}

// Start begins measuring a Goal Mode pass.
func (p *GoalPerfProfiler) Start(taskID, runID string, pass int, phase string) *GoalPerfSpan {
	allocBytes, allocObjects := readHeapAllocMetrics()
	return &GoalPerfSpan{
		profiler:     p,
		taskID:       taskID,
		runID:        runID,
		pass:         pass,
		phase:        phase,
		startedAt:    time.Now(),
		startBytes:   allocBytes,
		startObjects: allocObjects,
	}
}

// End completes the span, writes the JSONL sample fail-open, and returns the sample.
func (s *GoalPerfSpan) End(verified bool) GoalPerfSample {
	if s == nil {
		return GoalPerfSample{Verified: verified, RecordedAt: time.Now().UTC()}
	}
	endBytes, endObjects := readHeapAllocMetrics()
	now := time.Now().UTC()
	elapsed := now.Sub(s.startedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	var deltaBytes, deltaObjects uint64
	if endBytes >= s.startBytes {
		deltaBytes = endBytes - s.startBytes
	}
	if endObjects >= s.startObjects {
		deltaObjects = endObjects - s.startObjects
	}
	sample := GoalPerfSample{
		TaskID:           s.taskID,
		RunID:            s.runID,
		Pass:             s.pass,
		Phase:            s.phase,
		DurationMs:       elapsed.Milliseconds(),
		DurationUs:       elapsed.Microseconds(),
		HeapAllocBytes:   deltaBytes,
		HeapAllocObjects: deltaObjects,
		Verified:         verified,
		RecordedAt:       now,
	}
	if s.profiler != nil {
		s.profiler.Record(sample)
	}
	return sample
}

// Record appends sample as a JSON line to p.Path(), failing open on any error.
func (p *GoalPerfProfiler) Record(sample GoalPerfSample) {
	if p == nil {
		return
	}
	if sample.RecordedAt.IsZero() {
		sample.RecordedAt = time.Now().UTC()
	}
	line, err := json.Marshal(sample)
	if err != nil {
		return
	}
	line = append(line, '\n')

	p.mu.Lock()
	defer p.mu.Unlock()
	goalPerfFileMu.Lock()
	defer goalPerfFileMu.Unlock()

	outPath := p.Path()
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(line)
}

func readHeapAllocMetrics() (allocBytes, allocObjects uint64) {
	samples := []metrics.Sample{
		{Name: metricHeapAllocBytes},
		{Name: metricHeapAllocObjects},
	}
	metrics.Read(samples)
	if samples[0].Value.Kind() == metrics.KindUint64 {
		allocBytes = samples[0].Value.Uint64()
	}
	if samples[1].Value.Kind() == metrics.KindUint64 {
		allocObjects = samples[1].Value.Uint64()
	}
	return allocBytes, allocObjects
}
