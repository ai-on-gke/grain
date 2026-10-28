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

package metrics_test

// Host: how hard the machine underneath the deployment was working, on the
// same slices as everything else in the report (grain/task-495).

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/metrics"
)

// rollup is one hour of host pressure, d into the window.
func rollup(d time.Duration, samples int, load, cpu float64, memUsed int) metrics.HostRollup {
	return metrics.HostRollup{
		Since:              since.Add(d).Truncate(time.Hour),
		Samples:            samples,
		Load1Mean:          load,
		Load1Max:           load * 1.5,
		CPUUtilizationMean: cpu,
		CPUUtilizationMax:  cpu,
		MemUsedMeanMB:      float64(memUsed),
		MemUsedMaxMB:       memUsed,
		CPUs:               28,
		MemTotalMB:         64000,
	}
}

// The join the section exists for: a quiet first half and a saturated
// second, on the same buckets the CI series is sliced into.
func TestHostPressureIsReportedOnTheWindowsOwnSlices(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window:      win,
		Buckets:     2,
		HostSampled: true,
		Host: metrics.HostHistory{
			Hours: 14 * 24,
			Rollups: []metrics.HostRollup{
				rollup(time.Hour, 60, 6, 0.2, 8000),
				rollup(6*24*time.Hour, 60, 47.4, 0.994, 48000),
			},
		},
	})
	h := rep.Host
	if !h.Available {
		t.Fatal("Available is false with a sampler supplied")
	}
	if h.Hours != 2 || h.Samples != 120 {
		t.Errorf("got %d hour(s) of %d sample(s), want 2 and 120", h.Hours, h.Samples)
	}
	if got, want := h.Load1Mean, 26.7; got != want {
		t.Errorf("Load1Mean = %v, want %v -- the mean over samples, not over hours", got, want)
	}
	// Compared loosely: this is one float division of another, and the
	// last bit of it is not what the measurement is about.
	if got, want := h.LoadPerCPUMean, 26.7/28; got < want-0.0001 || got > want+0.0001 {
		t.Errorf("LoadPerCPUMean = %v, want %v -- load means nothing without the core count", got, want)
	}
	if h.Load1Max != 47.4*1.5 || h.CPUUtilizationMax != 0.994 {
		t.Errorf("worst of the window is load %v / cpu %v, want the busy hour's own worst", h.Load1Max, h.CPUUtilizationMax)
	}
	if h.CPUs != 28 || h.MemTotalMB != 64000 {
		t.Errorf("the machine is %d cores / %d MiB, want 28 and 64000", h.CPUs, h.MemTotalMB)
	}
	if len(h.Buckets) != 2 {
		t.Fatalf("Buckets = %+v, want one per slice", h.Buckets)
	}
	if h.Buckets[0].Load1Mean != 6 || h.Buckets[1].Load1Mean != 47.4 {
		t.Errorf("per-slice load is %v then %v, want 6 then 47.4 -- the series the CI series is read against",
			h.Buckets[0].Load1Mean, h.Buckets[1].Load1Mean)
	}
}

// A sampler that has only just started, and a deployment with no sampler
// at all, are different answers -- and neither of them is an idle machine.
func TestHostSaysWhenThereIsNothingToAskAndWhenItHasNotBeenRunningLong(t *testing.T) {
	none := metrics.Compute(metrics.Input{Window: win, Buckets: 2}).Host
	if none.Available {
		t.Error("Available is true with no sampler supplied")
	}

	// A sampler with an hour of history, asked for a week of it: the
	// window holds nothing, and Reaches is what says why.
	young := metrics.Compute(metrics.Input{
		Window:      win,
		Buckets:     2,
		HostSampled: true,
		Host: metrics.HostHistory{
			Hours:   14 * 24,
			Rollups: []metrics.HostRollup{rollup(-2*time.Hour, 60, 3, 0.1, 8000)},
		},
	}).Host
	if !young.Available || young.Hours != 0 {
		t.Errorf("Host = %+v, want an available sampler with nothing in the window", young)
	}
	if young.Reaches == nil {
		t.Fatal("Reaches is nil -- nothing says whether the window was quiet or the history is short")
	}
	if young.RingHours != 14*24 {
		t.Errorf("RingHours = %d, want the bound the sampler is working towards", young.RingHours)
	}
}
