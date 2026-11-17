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

package orchestrator_test

// HostSandboxes.Health's per-run CPU reading: the rate the sampler
// derives from sysstat.ProcessTreeCPU's cumulative times, first with a
// scripted reader and clock, then against a real busy process.

import (
	"context"
	"errors"
	"math"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

func healthByName(t *testing.T, h *orchestrator.HostSandboxes) map[string]orchestrator.SandboxHealth {
	t.Helper()
	out := map[string]orchestrator.SandboxHealth{}
	for _, e := range h.Health(context.Background()) {
		out[e.Sandbox] = e
	}
	return out
}

func wantCores(t *testing.T, step string, e orchestrator.SandboxHealth, cores float64, window time.Duration) {
	t.Helper()
	if e.CPUCores == nil {
		t.Fatalf("%s: %s CPUCores = nil, want %.3f", step, e.Sandbox, cores)
	}
	if math.Abs(*e.CPUCores-cores) > 1e-9 || e.CPUWindow != window {
		t.Fatalf("%s: %s CPUCores/CPUWindow = %.3f/%v, want %.3f/%v", step, e.Sandbox, *e.CPUCores, e.CPUWindow, cores, window)
	}
}

func TestHostSandboxesHealthReportsEachRunsCPUAsARate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := orchestrator.NewHostSandboxes(t.TempDir())
	for _, name := range []string{"t1-1", "t2-1"} {
		if _, err := h.Acquire(ctx, name, orchestrator.Shape{}); err != nil {
			t.Fatal(err)
		}
	}
	clock := time.Unix(1_000_000, 0)
	var reads []map[string]time.Duration
	var readErr error
	var slept []time.Duration
	h.SetCPUSamplerForTest(
		func(roots map[string]string) (map[string]time.Duration, error) {
			if len(roots) != 2 || roots["t1-1"] == "" {
				t.Errorf("reader got roots %v, want both live sandboxes' directories", roots)
			}
			if readErr != nil {
				return nil, readErr
			}
			r := reads[0]
			reads = reads[1:]
			return r, nil
		},
		func() time.Time { return clock },
		func(d time.Duration) { slept = append(slept, d); clock = clock.Add(d) },
	)

	// First sight: one settle across both reads. t2-1 has no process.
	reads = []map[string]time.Duration{{"t1-1": 10 * time.Second}, {"t1-1": 10*time.Second + 125*time.Millisecond}}
	got := healthByName(t, h)
	wantCores(t, "first sweep", got["t1-1"], 0.5, 250*time.Millisecond)
	if got["t2-1"].CPUCores != nil {
		t.Fatalf("t2-1 with no process got CPUCores %v, want nil", *got["t2-1"].CPUCores)
	}
	if len(slept) != 1 {
		t.Fatalf("first sweep slept %v, want one settle", slept)
	}

	// Next sweep 10s on: the delta since the previous one, no settle.
	clock = clock.Add(10 * time.Second)
	reads = []map[string]time.Duration{{"t1-1": 30*time.Second + 125*time.Millisecond}}
	wantCores(t, "second sweep", healthByName(t, h)["t1-1"], 2, 10*time.Second)

	// A sweep within cpuMinWindow repeats the last reading without
	// walking /proc again across concurrent browser tabs.
	clock = clock.Add(400 * time.Millisecond)
	wantCores(t, "quick sweep", healthByName(t, h)["t1-1"], 2, 10*time.Second)

	// A total that went backwards (part of the tree reaped outside it)
	// starts over with a settle rather than reporting a negative rate.
	clock = clock.Add(5 * time.Second)
	reads = []map[string]time.Duration{{"t1-1": 20 * time.Second}, {"t1-1": 20*time.Second + 250*time.Millisecond}}
	wantCores(t, "reset sweep", healthByName(t, h)["t1-1"], 1, 250*time.Millisecond)
	if len(slept) != 2 {
		t.Fatalf("slept %v, want a second settle after the reset", slept)
	}
	if len(reads) != 0 {
		t.Fatalf("%d scripted reads unused", len(reads))
	}

	// No /proc: no reading at all, not zeros.
	clock = clock.Add(2 * time.Second)
	readErr = errors.ErrUnsupported
	for name, e := range healthByName(t, h) {
		if e.CPUCores != nil {
			t.Fatalf("%s CPUCores = %v without a reader, want nil", name, *e.CPUCores)
		}
	}
}

// The real thing: a busy shell started in a run's own directory shows up
// as that run's CPU, and an idle run beside it has no reading.
func TestHostSandboxesHealthReadsARealBusyRun(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("process-tree CPU needs /proc")
	}
	t.Parallel()
	ctx := context.Background()
	h := orchestrator.NewHostSandboxes(t.TempDir())
	busy, err := h.Acquire(ctx, "t7-1", orchestrator.Shape{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Acquire(ctx, "t8-1", orchestrator.Shape{}); err != nil {
		t.Fatal(err)
	}
	root, err := busy.(interface{ Root() (string, error) }).Root()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", "while :; do :; done")
	cmd.Dir = root
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	time.Sleep(200 * time.Millisecond)

	got := healthByName(t, h)
	e := got["t7-1"]
	if e.CPUCores == nil {
		t.Fatal("busy run has no CPUCores, want a reading from its process")
	}
	// A spinning shell wants a whole core; a loaded CI runner may give
	// it less, but not nothing.
	if *e.CPUCores < 0.2 || *e.CPUCores > float64(runtime.NumCPU())+0.5 {
		t.Fatalf("busy run CPUCores = %.3f, want roughly one core", *e.CPUCores)
	}
	if e.CPUWindow <= 0 {
		t.Fatalf("busy run CPUWindow = %v, want the settle it was sampled over", e.CPUWindow)
	}
	if got["t8-1"].CPUCores != nil {
		t.Fatalf("idle run CPUCores = %v, want nil", *got["t8-1"].CPUCores)
	}
}
