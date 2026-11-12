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

package qa_test

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/qa"
)

func TestProfilerMeasuresDurationAllocationsAndWritesJSONL(t *testing.T) {
	t.Parallel()
	logPath := filepath.Join(t.TempDir(), "perf", "qa-checks.jsonl")
	prof := qa.NewProfiler(logPath)

	const workers = 8
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(idx int) {
			defer wg.Done()
			sample, err := prof.Measure(qa.PerfSample{
				TaskID:  "42",
				Stage:   "S3",
				CheckID: "hollow-test-ast",
				Arm:     "treatment",
			}, func() (string, error) {
				buf := make([]byte, 32*1024)
				buf[0] = byte(idx)
				time.Sleep(2 * time.Millisecond)
				if buf[0] == 255 {
					return "fail", nil
				}
				return "pass", nil
			})
			if err != nil {
				t.Errorf("Measure worker %d: %v", idx, err)
			}
			if sample.DurationUS < 1000 {
				t.Errorf("Measure DurationUS = %d, expected >= 1000us", sample.DurationUS)
			}
			if sample.Verdict != "pass" {
				t.Errorf("Measure Verdict = %q, want pass", sample.Verdict)
			}
		}(i)
	}
	wg.Wait()

	f, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("opening JSONL perf log: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	count := 0
	for scanner.Scan() {
		var row qa.PerfSample
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatalf("invalid JSONL line %d: %v", count, err)
		}
		if row.Stage != "S3" || row.CheckID != "hollow-test-ast" || row.Verdict != "pass" {
			t.Errorf("unexpected JSONL row: %+v", row)
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanning JSONL log: %v", err)
	}
	if count != workers {
		t.Fatalf("wrote %d JSONL perf records, want %d", count, workers)
	}
}

func TestDefaultPerfLogPathUsesDataDirOrHome(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	gotHome := qa.DefaultPerfLogPath("")
	wantHome := filepath.Join(fakeHome, ".perf", "grain-qa-checks.jsonl")
	if gotHome != wantHome {
		t.Fatalf("DefaultPerfLogPath(\"\") = %q, want %q", gotHome, wantHome)
	}

	dataDir := t.TempDir()
	gotData := qa.DefaultPerfLogPath(dataDir)
	wantData := filepath.Join(dataDir, "perf", "qa-checks.jsonl")
	if gotData != wantData {
		t.Fatalf("DefaultPerfLogPath(dataDir) = %q, want %q", gotData, wantData)
	}
}
