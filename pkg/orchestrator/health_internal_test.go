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
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestParseProcStatsReadsLoadAverageAndMemory(t *testing.T) {
	t.Parallel()
	output := "0.10 0.05 0.01 1/234 5678\n" +
		"MemTotal:        1024000 kB\n" +
		"MemFree:          200000 kB\n" +
		"MemAvailable:     400000 kB\n" +
		"SwapTotal:         512000 kB\n" +
		"SwapFree:          307200 kB\n"

	loadAverage, usedMB, totalMB, swapUsedMB, swapTotalMB := parseProcStats(output)

	if loadAverage != "0.10 0.05 0.01" {
		t.Errorf("loadAverage = %q, want %q", loadAverage, "0.10 0.05 0.01")
	}
	if totalMB != 1000 {
		t.Errorf("totalMB = %d, want 1000", totalMB)
	}
	// usedMB is MemTotal minus MemAvailable, not minus MemFree -- see
	// parseProcStats' own doc comment on why.
	if usedMB != 609 {
		t.Errorf("usedMB = %d, want 609", usedMB)
	}
	if swapUsedMB != 200 || swapTotalMB != 500 {
		t.Errorf("swapUsedMB/swapTotalMB = %d/%d, want 200/500", swapUsedMB, swapTotalMB)
	}
}

func TestParseProcStatsToleratesMissingMeminfo(t *testing.T) {
	t.Parallel()
	loadAverage, usedMB, totalMB, swapUsedMB, swapTotalMB := parseProcStats("0.50 0.40 0.30 2/10 99\n")

	if loadAverage != "0.50 0.40 0.30" {
		t.Errorf("loadAverage = %q, want %q", loadAverage, "0.50 0.40 0.30")
	}
	if usedMB != 0 || totalMB != 0 {
		t.Errorf("usedMB/totalMB = %d/%d, want 0/0 with no MemTotal line", usedMB, totalMB)
	}
	if swapUsedMB != 0 || swapTotalMB != 0 {
		t.Errorf("swapUsedMB/swapTotalMB = %d/%d, want 0/0 with no swap lines", swapUsedMB, swapTotalMB)
	}
}

// The breaker's policy on its own, branch by branch: what one sweep
// decides to spend on one guest, given what the last probe of it found.
// The end-to-end cost of each of these is measured in health_test.go
// against a real fake docker; this is the decision table behind them.
func TestHealthPlanSpendsOnAGuestAccordingToWhatTheLastProbeFound(t *testing.T) {
	t.Parallel()
	base := time.Now()
	k := NewGranuleSandboxes(GranuleConfig{})

	plan := k.healthPlan("t1-1", base)
	if plan.remembered != nil || plan.budget != healthTimeout || plan.attempts != 0 {
		t.Errorf("a guest nothing is known about = %+v, want the full budget with retries", plan)
	}

	// A probe that failed: the next sweeps are answered from it, and
	// then one cheap attempt.
	k.recordHealthProbe("t1-1", plan, base, 20_000, errors.New("connection refused"))

	plan = k.healthPlan("t1-1", base.Add(healthBreakerCooldown-time.Millisecond))
	if plan.remembered == nil {
		t.Fatalf("a guest that failed a moment ago = %+v, want its own error back rather than another probe", plan)
	}
	if plan.remembered.failedWith != "connection refused" || plan.remembered.failedAfterMS != 20_000 {
		t.Errorf("remembered = %+v, want the failure and the duration the probe actually took", plan.remembered)
	}

	plan = k.healthPlan("t1-1", base.Add(healthBreakerCooldown))
	if plan.remembered != nil || plan.attempts != 1 || plan.budget != healthBreakerBudget {
		t.Errorf("a guest past its cooldown = %+v, want a single-attempt probe", plan)
	}

	// Still failing much later: asked properly again, in case it was
	// slow rather than gone.
	plan = k.healthPlan("t1-1", base.Add(healthBreakerFullInterval))
	if plan.remembered != nil || plan.budget != healthTimeout || plan.attempts != 0 {
		t.Errorf("a guest that has been failing for %v = %+v, want the full budget back", healthBreakerFullInterval, plan)
	}

	// And one answer closes it: the sweep after a successful probe is a
	// full one again, whenever it arrives.
	k.recordHealthProbe("t1-1", plan, base.Add(healthBreakerFullInterval), 40, nil)
	plan = k.healthPlan("t1-1", base.Add(healthBreakerFullInterval))
	if plan.remembered != nil || plan.budget != healthTimeout || plan.attempts != 0 {
		t.Errorf("a guest that has just answered = %+v, want the full budget with retries", plan)
	}
}

// A single-attempt probe that fails must not postpone the full one
// (healthBreakerFullInterval is measured from the last full budget
// spent, not from the last probe of any kind), or a guest re-probed
// cheaply every fifteen seconds would never be asked properly again.
func TestHealthPlanMeasuresItsFullProbeFromTheLastFullProbe(t *testing.T) {
	t.Parallel()
	base := time.Now()
	k := NewGranuleSandboxes(GranuleConfig{})

	full := k.healthPlan("t1-1", base)
	k.recordHealthProbe("t1-1", full, base, 20_000, errors.New("connection refused"))

	for at := base.Add(healthBreakerCooldown); at.Sub(base) < healthBreakerFullInterval; at = at.Add(healthBreakerCooldown) {
		plan := k.healthPlan("t1-1", at)
		if plan.remembered != nil {
			t.Fatalf("at %v the sweep was answered from memory, want a cheap probe", at.Sub(base))
		}
		if plan.attempts != 1 {
			t.Fatalf("at %v the plan was %+v, want a single-attempt probe", at.Sub(base), plan)
		}
		k.recordHealthProbe("t1-1", plan, at, 10, errors.New("connection refused"))
	}
	due := base.Add(healthBreakerFullInterval)
	if plan := k.healthPlan("t1-1", due); plan.attempts != 0 || plan.budget != healthTimeout {
		t.Errorf("after %v of cheap probes the plan was %+v, want the full budget back", healthBreakerFullInterval, plan)
	}
}

// A slow or unreachable guest gets its own healthTimeout budget. Those
// budgets must overlap: serial probes make the sandboxes page's request grow
// with the number of guests until an upstream HTTP server drops it, which the
// browser can report only as "Failed to fetch".
func TestCollectSandboxHealthProbesConcurrentlyAndKeepsOrder(t *testing.T) {
	t.Parallel()
	names := []string{"t3-r1", "t7-r1", "t9-r1"}
	started := make(chan string, len(names))
	release := make(chan struct{})
	done := make(chan []SandboxHealth, 1)

	go func() {
		done <- collectSandboxHealth(names, func(name string) SandboxHealth {
			started <- name
			<-release
			return SandboxHealth{Sandbox: name}
		})
	}()

	for range names {
		select {
		case <-started:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("sandbox probes did not all start before the first completed")
		}
	}
	close(release)

	got := <-done
	gotNames := make([]string, len(got))
	for i := range got {
		gotNames[i] = got[i].Sandbox
	}
	if !reflect.DeepEqual(gotNames, names) {
		t.Fatalf("sandbox order = %v, want %v", gotNames, names)
	}
}
