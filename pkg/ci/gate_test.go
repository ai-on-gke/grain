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

package ci

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestSharedGateHandsOutNoMorePermitsThanItsLimit(t *testing.T) {
	gate := &SharedGate{Limit: func() int { return 2 }, Poll: time.Millisecond}

	first, err := gate.Acquire(context.Background(), GateRequest{})
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	second, err := gate.Acquire(context.Background(), GateRequest{})
	if err != nil {
		t.Fatalf("second Acquire: %v", err)
	}

	// The third has to wait, and the only thing that can free it is one
	// of the two above being given back. The gate saying so is the
	// refusal -- see acquiring.heldBy -- and with both permits held and
	// nothing released, no later attempt of this job can be answered any
	// differently.
	third := queue(t, gate, GateRequest{})
	if why := third.heldBy(t, "this deployment keeps for CI"); !strings.Contains(why, "2 sandboxes") {
		t.Errorf("a job held by the count names the limit holding it: got %q", why)
	}

	first()
	third.admitted(t, "the job waiting for the permit the first one gave back")()
	second()

	if got := gate.inUse(); got != 0 {
		t.Errorf("every permit given back: %d still held", got)
	}
}

func TestSharedGateReleaseIsIdempotent(t *testing.T) {
	gate := &SharedGate{Limit: func() int { return 1 }, Poll: time.Millisecond}
	release, err := gate.Acquire(context.Background(), GateRequest{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	release()
	release()
	if got := gate.inUse(); got != 0 {
		// A release counted twice would read as -1 here, and would have
		// handed the deployment a sandbox nobody gave back.
		t.Fatalf("a release called twice gave back two permits: %d held", got)
	}
	if _, err := gate.Acquire(context.Background(), GateRequest{}); err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	if got := gate.inUse(); got != 1 {
		t.Errorf("one permit held after re-acquiring: got %d", got)
	}
}

func TestSharedGateStopsWaitingWhenItsRunIsCancelled(t *testing.T) {
	gate := &SharedGate{Limit: func() int { return 1 }, Poll: time.Millisecond}
	held, err := gate.Acquire(context.Background(), GateRequest{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer held()

	ctx, cancel := context.WithCancel(context.Background())
	waiting := make(chan error, 1)
	reported := make(chan Wait, 4)
	go func() {
		_, err := gate.Acquire(ctx, GateRequest{Wait: func(w Wait) { signal(reported, w) }})
		waiting <- err
	}()
	// Cancelled while it is waiting, which is the case this is about. The
	// gate's own report of what is holding the job is what says it has got
	// that far, rather than a sleep long enough that it probably has.
	awaitSignal(t, reported, "the second job never reported being held at the gate")
	cancel()

	if err := awaitSignal(t, waiting, "cancelling the run never freed the job waiting at the gate"); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled wait reports its context: got %v", err)
	}
	if got := gate.inUse(); got != 1 {
		t.Errorf("a wait that gave up holds nothing: %d held", got)
	}
}

// A pool resized under a running daemon is the case Limit exists to be a
// func for: a job already waiting must see the new number without being
// told about it, since nothing tells this gate that Settings was edited.
func TestSharedGateNoticesItsLimitGrowingWhileAJobWaits(t *testing.T) {
	var limit atomic.Int64
	limit.Store(1)
	gate := &SharedGate{Limit: func() int { return int(limit.Load()) }, Poll: time.Millisecond}

	held, err := gate.Acquire(context.Background(), GateRequest{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer held()

	// Held by the count of one, which the gate says in as many words: the
	// only permit is held above and nothing gives it back, so nothing but
	// the limit itself moving can change that answer.
	waiter := queue(t, gate, GateRequest{})
	if why := waiter.heldBy(t, "this deployment keeps for CI"); !strings.Contains(why, "1 sandbox") {
		t.Errorf("a job held by a gate of 1 says so: got %q", why)
	}

	limit.Store(2)
	waiter.admitted(t, "a job waiting at a gate the deployment grew under it")()
}

func TestSharedGateSaysWhoIsWaitingAndWhyOnce(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	gate := &SharedGate{
		Limit: func() int { return 1 },
		Poll:  time.Millisecond,
		Log: func(format string, args ...any) {
			mu.Lock()
			defer mu.Unlock()
			lines = append(lines, fmt.Sprintf(format, args...))
		},
	}
	held, err := gate.Acquire(context.Background(), GateRequest{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	// Several attempts rather than several milliseconds: a line logged per
	// attempt would show up as several rather than as one, and the
	// attempts themselves are what this is about -- see counted. Sleeping
	// for long enough that a few polls have probably happened proves the
	// same thing on an idle machine and nothing at all on a busy one.
	attempts := make(chan int, 8)
	waiting := queue(t, gate, counted(GateRequest{}, attempts))
	for i := 0; i < 3; i++ {
		awaitSignal(t, attempts, "the job held at the gate stopped asking again")
	}
	held()
	waiting.admitted(t, "the waiting job")()

	mu.Lock()
	defer mu.Unlock()
	var waits int
	for _, line := range lines {
		if strings.Contains(line, "this deployment keeps for CI") {
			waits++
		}
	}
	if waits != 1 {
		t.Errorf("one line per job that has to wait, not one per attempt: got %d of %v", waits, lines)
	}
}

// The bound that matters to a deployment is across runs, not within one:
// ci.Service gives every request its own Engine.Run, so Concurrency
// alone let three branches pushed at once hold three times a
// deployment's share of sandboxes (grain/task-296).
func TestEngineHoldsNoMoreSandboxesThanTheGateAllowsAcrossRuns(t *testing.T) {
	sandboxes := &fakeSandboxes{block: make(chan struct{}), acquires: make(chan SandboxRequest, 8)}
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n" +
			"  a:\n    steps:\n      - run: a\n" +
			"  b:\n    steps:\n      - run: b\n",
	})

	engine := &Engine{
		Sandboxes:   sandboxes,
		Concurrency: 2,
		Jobs:        &SharedGate{Limit: func() int { return 2 }, Poll: time.Millisecond},
	}

	// Six jobs across three runs against a gate of two, so four of them
	// are held there: the gate telling one of them so is what says the
	// allowance is being enforced, where holding still for 20ms only ever
	// said the other four had not got in yet.
	gateWaits := make(chan JobRun, 8)
	const runs = 3
	done := make(chan Run, runs)
	for i := 0; i < runs; i++ {
		go func(i int) {
			done <- engine.Run(context.Background(), string(rune('a'+i)), plan, func(job JobRun) {
				if strings.Contains(job.Activity, "this deployment keeps for CI") {
					signal(gateWaits, job)
				}
			})
		}(i)
	}

	// Nothing gives a sandbox back until block is closed, so two
	// acquisitions are the gate's whole allowance out at once.
	awaitSignal(t, sandboxes.acquires, "no run ever got a job going")
	awaitSignal(t, sandboxes.acquires, "the gate's whole allowance was never out")
	awaitSignal(t, gateWaits, "no job was ever held by the deployment's gate")
	close(sandboxes.block)

	for i := 0; i < runs; i++ {
		if run := <-done; run.Conclusion != ConclusionSuccess {
			t.Fatalf("conclusion: got %q (%s)", run.Conclusion, run.Error)
		}
	}
	if sandboxes.peakLive > 2 {
		t.Errorf("three runs against a gate of 2 held %d sandboxes at once", sandboxes.peakLive)
	}
	if sandboxes.sandboxes != runs*2 {
		t.Errorf("every job still ran, one sandbox each: got %d", sandboxes.sandboxes)
	}
}

// A run cancelled while its jobs are queueing is reported as cancelled,
// not as a mysterious failure and not as a job that ran: the deployment
// was busy, which is a fact about the deployment.
func TestEngineReportsAJobCancelledWhileItWaitedForASandbox(t *testing.T) {
	sandboxes := &fakeSandboxes{}
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  one:\n    steps:\n      - run: true\n",
	})

	// A gate nothing can ever satisfy, so the job is still waiting when
	// its run is cancelled.
	engine := &Engine{
		Sandboxes: sandboxes,
		Jobs:      &SharedGate{Limit: func() int { return 0 }, Poll: time.Millisecond},
	}
	ctx, cancel := context.WithCancel(context.Background())
	waits := make(chan JobRun, 4)
	done := make(chan Run, 1)
	go func() {
		done <- engine.Run(ctx, "r1", plan, func(job JobRun) {
			if job.Activity != "" {
				signal(waits, job)
			}
		})
	}()
	// The job is queueing when the run is cancelled, which the engine
	// reporting the wait is what establishes -- a sleep first would be
	// asserting that this machine gets a goroutine to a gate in 10ms.
	awaitSignal(t, waits, "the job never reported waiting at the gate")
	cancel()

	run := awaitSignal(t, done, "cancelling the run never ended it")
	job := jobByID(t, run, "one")
	if job.Conclusion != ConclusionCancelled {
		t.Errorf("a job cancelled at the gate is cancelled: got %q (%s)", job.Conclusion, job.Error)
	}
	if !strings.Contains(job.Error, "CI sandboxes") {
		t.Errorf("the error says what it was waiting for: got %q", job.Error)
	}
	if len(sandboxes.acquired) != 0 {
		t.Errorf("a job that never got a permit never asked for a machine: %v", sandboxes.acquired)
	}
}

// budgetGate is the deployment's gate as cmd/grain wires it: a count,
// and a share of a host that is stubbed here rather than read
// (grain/task-173). 64 GiB of budget, which is four of this repository's
// own 16 GiB CI jobs and no more.
func budgetGate(limit int, log func(string, ...any)) *SharedGate {
	return &SharedGate{
		Limit: func() int { return limit },
		Budget: func() model.Budget {
			return model.Budget{
				Host:             model.Commitment{CPUs: 28, MemoryMB: 80 * 1024},
				ReserveMemoryMB:  16 * 1024,
				CPUOvercommit:    4,
				MemoryOvercommit: 1,
			}
		},
		Poll: time.Millisecond,
		Log:  log,
	}
}

func bigJob() model.Commitment   { return model.Commitment{CPUs: 4, MemoryMB: 16 * 1024} }
func smallJob() model.Commitment { return model.Commitment{CPUs: 2, MemoryMB: 4 * 1024} }

// The failure this exists for: fifteen jobs is a defensible count, and
// fifteen 16 GiB jobs is 240 GiB on a host with 128. The count admits
// the fifth job here; the budget is what holds it.
func TestSharedGateHoldsAJobThatWouldTakeMemoryPastTheBudget(t *testing.T) {
	gate := budgetGate(15, nil)
	var held []func()
	for i := 0; i < 4; i++ {
		release, err := gate.Acquire(context.Background(), GateRequest{Want: bigJob()})
		if err != nil {
			t.Fatalf("job %d of 4: %v", i+1, err)
		}
		held = append(held, release)
	}

	// The fifth is refused for the budget, which the gate says to the job
	// itself: nothing gives memory back until the release below, so that
	// answer stands for as long as the test leaves it standing.
	fifth := queue(t, gate, GateRequest{Want: bigJob()})
	if why := fifth.heldBy(t, "sandbox budget"); !strings.Contains(why, "memory") {
		t.Errorf("a fifth 16 GiB job in a 64 GiB budget is held for memory: got %q", why)
	}

	// And it is the memory it was waiting for, not the count: giving one
	// job's memory back is what lets it through.
	held[0]()
	fifth.admitted(t, "the job waiting for the memory the first one gave back")()
	for _, release := range held[1:] {
		release()
	}
	if got := gate.inUse(); got != 0 {
		t.Errorf("every permit given back: %d still held", got)
	}
}

// A job held for memory must not hold up one that fits: the budget is a
// bound on the machine, not a queue.
func TestSharedGateAdmitsASmallerJobBehindOneThatDoesNotFit(t *testing.T) {
	gate := budgetGate(15, nil)
	// Reservation off, so this is about the budget's own arithmetic and
	// not about whether the test out-runs a one-minute window: a gate
	// holding capacity back for the job that does not fit deliberately
	// does keep the smaller one out, which is
	// TestSharedGateHoldsCapacityForAJobNothingSmallFitsAround's subject
	// rather than this one's.
	gate.ReserveAfter = window(-1)
	// 52 GiB of a 64 GiB budget: room for another 4 GiB job and not for
	// another 16 GiB one.
	for i := 0; i < 3; i++ {
		if _, err := gate.Acquire(context.Background(), GateRequest{Want: bigJob()}); err != nil {
			t.Fatalf("job %d of 3: %v", i+1, err)
		}
	}
	if _, err := gate.Acquire(context.Background(), GateRequest{Want: smallJob()}); err != nil {
		t.Fatalf("the small job that fits: %v", err)
	}
	// The 16 GiB job is in line and refused for room, which is what puts
	// it ahead of the smaller one below in arrival order -- the thing this
	// test needs established before the small job asks, and which the
	// gate's own report of the refusal establishes.
	waiting := queue(t, gate, GateRequest{Want: bigJob()})
	waiting.heldBy(t, "sandbox budget")

	gate.mu.Lock()
	committed := gate.committed
	gate.mu.Unlock()
	if committed.MemoryMB != 52*1024 {
		t.Fatalf("three 16 GiB jobs and a 4 GiB one commit 52 GiB: got %d MiB", committed.MemoryMB)
	}
	// Nothing here is full by the count, and the budget's own arithmetic
	// is per shape, so the job behind the one that does not fit is
	// admitted on its own merits rather than on its place in the queue.
	small := queue(t, gate, GateRequest{Want: smallJob()})
	small.admitted(t, "the 4 GiB job that fits the 12 GiB still free")()
	// And the one that does not fit is still where it was. Complete, not a
	// sample: the small job's own release is the only thing that has given
	// anything back, which leaves the same 12 GiB free that was too little
	// for this job a moment ago.
	waiting.holdsNothing(t, "the job that does not fit was admitted anyway")
}

// Both bounds apply, and the count is still the one an operator set: a
// budget with room to spare does not raise max_ci_jobs.
func TestSharedGateStillCountsWhenTheBudgetHasRoom(t *testing.T) {
	gate := budgetGate(2, nil)
	for i := 0; i < 2; i++ {
		if _, err := gate.Acquire(context.Background(), GateRequest{Want: smallJob()}); err != nil {
			t.Fatalf("job %d of 2: %v", i+1, err)
		}
	}
	third := queue(t, gate, GateRequest{Want: smallJob()})
	// Held, and held by the count: a gate of 2 hands out no third permit
	// however much room the budget has, and it says which of its two
	// bounds is the one refusing.
	if why := third.heldBy(t, "this deployment keeps for CI"); strings.Contains(why, "budget") {
		t.Errorf("a job held by the count blames the budget: got %q", why)
	}
}

// The reserve is respected rather than being the last byte of RAM: 80
// GiB of host with 16 held back is 64 to commit, so the fifth 16 GiB job
// is refused even though the machine itself has room for it.
func TestSharedGateRespectsTheHostReserve(t *testing.T) {
	gate := budgetGate(15, nil)
	for i := 0; i < 4; i++ {
		if _, err := gate.Acquire(context.Background(), GateRequest{Want: bigJob()}); err != nil {
			t.Fatalf("job %d of 4: %v", i+1, err)
		}
	}
	// The fifth is refused, and refused for the budget rather than for the
	// host: waiting for the gate to say so proves what a 30ms context
	// could not, since an Acquire whose context expired before it ever
	// reached the gate returns an error too.
	fifth := queue(t, gate, GateRequest{Want: bigJob()})
	if why := fifth.heldBy(t, "sandbox budget"); !strings.Contains(why, "memory") {
		t.Errorf("the fifth job fits the host and not the budget's memory: got %q", why)
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.committed.MemoryMB > 64*1024 {
		t.Errorf("never more than the budget committed: %d MiB", gate.committed.MemoryMB)
	}
}

// A job waiting for memory is visible as waiting for memory, in the same
// place a job waiting for a capability grant already is
// (ci.JobRun.Activity).
func TestSharedGateSaysWhenItIsMemoryAJobIsWaitingFor(t *testing.T) {
	var mu sync.Mutex
	var logged []string
	gate := budgetGate(15, func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logged = append(logged, fmt.Sprintf(format, args...))
	})
	// Reservation off: a waiter the budget has been refusing for the whole
	// window is told a second thing -- that capacity is being held for it
	// -- and that second reason is a second line, which would make the
	// count below a race against a one-minute clock. What a reservation
	// says is gate_priority_test.go's subject.
	gate.ReserveAfter = window(-1)
	for i := 0; i < 4; i++ {
		if _, err := gate.Acquire(context.Background(), GateRequest{Want: bigJob()}); err != nil {
			t.Fatalf("job %d of 4: %v", i+1, err)
		}
	}
	fifth := queue(t, gate, GateRequest{Want: bigJob()})
	what := fifth.heldBy(t, "memory")
	if !strings.Contains(what, "sandbox budget") {
		t.Errorf("and says what it is being held by: got %q", what)
	}
	mu.Lock()
	defer mu.Unlock()
	var memory int
	for _, line := range logged {
		if strings.Contains(line, "memory:") {
			memory++
		}
	}
	if memory != 1 {
		t.Errorf("one line per reason a job waits, not one per attempt: got %d of %v", memory, logged)
	}
}

// A job that named no shape is charged the deployment's default, since
// the backend will give it one whatever the workflow file left out.
func TestSharedGateChargesTheDefaultShapeForAJobThatNamedNone(t *testing.T) {
	gate := budgetGate(15, nil)
	gate.Default = func() model.Commitment { return model.Commitment{CPUs: 4, MemoryMB: 8 * 1024} }
	release, err := gate.Acquire(context.Background(), GateRequest{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	gate.mu.Lock()
	committed := gate.committed
	gate.mu.Unlock()
	if committed.MemoryMB != 8*1024 {
		t.Fatalf("a job with no shape of its own is charged the default: got %d MiB", committed.MemoryMB)
	}
	release()
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if !gate.committed.IsZero() {
		t.Errorf("a release gives back exactly what its acquire took: %+v", gate.committed)
	}
}

// A job's `sandbox:` block is a per-dimension floor ("at least") against the
// deployment's default shape rather than a ceiling that pins the job down
// when the deployment default is larger.
func TestSharedGateTreatsJobShapeAsFloorAgainstDefault(t *testing.T) {
	t.Parallel()
	def := model.Commitment{CPUs: 12, MemoryMB: 16384}
	gate := &SharedGate{
		Budget: func() model.Budget {
			return model.Budget{
				Host:             model.Commitment{CPUs: 64, MemoryMB: 128 * 1024},
				CPUOvercommit:    1,
				MemoryOvercommit: 1,
			}
		},
		Default: func() model.Commitment { return def },
		Poll:    time.Millisecond,
	}

	for _, tc := range []struct {
		name string
		def  model.Commitment
		want model.Commitment
		exp  model.Commitment
	}{
		{
			name: "job smaller than deployment default scales up to default",
			def:  model.Commitment{CPUs: 12, MemoryMB: 16384},
			want: model.Commitment{CPUs: 2, MemoryMB: 8192},
			exp:  model.Commitment{CPUs: 12, MemoryMB: 16384},
		},
		{
			name: "job larger than deployment default keeps its larger dimensions",
			def:  model.Commitment{CPUs: 12, MemoryMB: 16384},
			want: model.Commitment{CPUs: 16, MemoryMB: 32768},
			exp:  model.Commitment{CPUs: 16, MemoryMB: 32768},
		},
		{
			name: "partial job shape with CPUs smaller than default fills both from max",
			def:  model.Commitment{CPUs: 12, MemoryMB: 16384},
			want: model.Commitment{CPUs: 8},
			exp:  model.Commitment{CPUs: 12, MemoryMB: 16384},
		},
		{
			name: "partial job shape with CPUs larger than default keeps CPUs and takes default memory",
			def:  model.Commitment{CPUs: 4, MemoryMB: 16384},
			want: model.Commitment{CPUs: 8},
			exp:  model.Commitment{CPUs: 8, MemoryMB: 16384},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def = tc.def
			release, err := gate.Acquire(context.Background(), GateRequest{Want: tc.want})
			if err != nil {
				t.Fatalf("Acquire(%+v): %v", tc.want, err)
			}
			gate.mu.Lock()
			committed := gate.committed
			gate.mu.Unlock()
			if committed != tc.exp {
				t.Errorf("committed = %+v, want %+v", committed, tc.exp)
			}
			release()
			gate.mu.Lock()
			after := gate.committed
			gate.mu.Unlock()
			if !after.IsZero() {
				t.Errorf("after release, committed = %+v, want zero", after)
			}
		})
	}
}

// A job held for memory is held visibly: the same JobRun.Activity a job
// waiting for a capability grant reports, so "why is this check slow"
// has an answer in the pane rather than only in the journal
// (grain/task-173, and the merge lane's own reason strings in
// grain/task-170).
func TestEngineReportsAJobWaitingForMemoryAsWaitingForMemory(t *testing.T) {
	sandboxes := &fakeSandboxes{block: make(chan struct{})}
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n" +
			"  big:\n    sandbox:\n      cpus: 4\n      memory-mb: 16384\n    steps:\n      - run: a\n" +
			"  bigger:\n    sandbox:\n      cpus: 4\n      memory-mb: 16384\n    steps:\n      - run: b\n",
	})
	// Room for one of those two jobs at a time, with a count that would
	// happily run both.
	engine := &Engine{
		Sandboxes:   sandboxes,
		Concurrency: 2,
		Jobs: &SharedGate{
			Limit: func() int { return 8 },
			Budget: func() model.Budget {
				return model.Budget{
					Host:             model.Commitment{CPUs: 16, MemoryMB: 24 * 1024},
					ReserveMemoryMB:  4 * 1024,
					CPUOvercommit:    2,
					MemoryOvercommit: 1,
				}
			},
			Poll: time.Millisecond,
		},
	}

	// The observer keeps the memory waits and nothing else, and keeps them
	// without ever blocking the engine: an engine whose observations stop
	// being drained stops running.
	memoryWaits := make(chan JobRun, 4)
	done := make(chan Run, 1)
	go func() {
		done <- engine.Run(context.Background(), "r1", plan, func(j JobRun) {
			if strings.Contains(j.Activity, "memory") && j.Sandbox == "" {
				signal(memoryWaits, j)
			}
		})
	}()

	waiting := awaitSignal(t, memoryWaits, "neither job was ever reported as waiting for memory")
	if waiting.ActivityAt == nil {
		t.Error("a job reported as waiting says since when")
	}

	close(sandboxes.block)
	run := awaitSignal(t, done, "the run never finished")
	if run.Conclusion != ConclusionSuccess {
		t.Fatalf("both jobs still ran, one after the other: %q (%s)", run.Conclusion, run.Error)
	}
	for _, job := range run.Jobs {
		if job.Activity != "" {
			t.Errorf("job %s finished still claiming to be waiting: %q", job.JobID, job.Activity)
		}
	}
	if sandboxes.peakLive > 1 {
		t.Errorf("two 16 GiB jobs held a 20 GiB budget at once: peak %d", sandboxes.peakLive)
	}
}

// A refusal names what is holding the capacity, and says when waiting for
// it cannot help.
//
// Both halves cost real operator time without them, which is how this was
// found: a `go` job wanting 16 GiB sat "running" for an hour and three
// quarters on a deployment whose agent run held 8 GiB of a 23.4 GiB
// budget, and the reason it reported -- "is committed by this
// deployment's live sandboxes" -- was true of both an agent run and a
// pile of other CI jobs. Only the first is worth acting on, and only the
// journal plus `docker ps` could tell them apart. Budget.memoryRefusal
// cannot say it (a budget knows nothing about pools, as its own doc
// notes); the gate that holds the ledger can.
func TestSharedGateRefusalNamesTheHolderAndWhetherWaitingCanHelp(t *testing.T) {
	// 80 GiB host less 16 reserved at 1.00x: a 64 GiB budget, with an
	// agent run already holding 8 of it.
	ledger := &model.Ledger{}
	t.Cleanup(ledger.Charge(model.PoolAgent, "task-34", model.Commitment{CPUs: 4, MemoryMB: 8 * 1024}))

	gate := budgetGate(15, nil)
	gate.Ledger = func() *model.Ledger { return ledger }
	// Reservation off for the reason TestSharedGateSaysWhenItIsMemoryAJobIsWaitingFor
	// turns it off: a held-for-capacity waiter is told a second thing, and
	// this test is about the first.
	gate.ReserveAfter = window(-1)

	// Fits the budget on an empty host (60 of 64) and not beside the
	// agent run, so it is refused and waiting could genuinely help.
	fits := queue(t, gate, GateRequest{Want: model.Commitment{CPUs: 4, MemoryMB: 60 * 1024}})
	why := fits.heldBy(t, "memory")
	if !strings.Contains(why, "agent sandbox") || !strings.Contains(why, "agent run task-34") {
		t.Errorf("a refusal says what is holding the capacity, including the holder id: got %q", why)
	}
	if strings.Contains(why, "otherwise empty") {
		t.Errorf("this job does fit an empty host, so it must not be told it can only ever run alone: got %q", why)
	}

	// Larger than the whole budget: the Sole rule will admit it when the
	// host is empty and never beside anything, so a wait for capacity is
	// a wait for something that is not coming.
	never := queue(t, gate, GateRequest{Want: model.Commitment{CPUs: 4, MemoryMB: 80 * 1024}})
	why = never.heldBy(t, "memory")
	if !strings.Contains(why, "otherwise empty") {
		t.Errorf("a job larger than the whole budget is told waiting cannot make room beside anything: got %q", why)
	}
}

// Sole's own reasoning -- "no amount of waiting will make room that the
// budget was never going to have" -- applies just as well to room held
// by a waiter that will not move until this job runs. Stuck is how a
// caller that knows about runs and tasks tells the gate that is the case
// for one specific refusal, and the gate takes its word for it: this
// job's own shape is charged anyway, past a budget that has genuinely
// refused it.
func TestSharedGateStuckAdmitsAJobCapacityWillNeverRelease(t *testing.T) {
	ledger := &model.Ledger{}
	// 64 GiB budget (80 host less 16 reserved), with a task's own agent
	// run already holding 20 of it.
	t.Cleanup(ledger.Charge(model.PoolAgent, "task-34", model.Commitment{CPUs: 4, MemoryMB: 20 * 1024}))

	gate := budgetGate(15, nil)
	gate.Ledger = func() *model.Ledger { return ledger }
	gate.ReserveAfter = window(-1)
	var asked atomic.Int64
	gate.Stuck = func(jobID string) (bool, string) {
		asked.Add(1)
		return true, "every sandbox is a task's own run waiting for this job"
	}

	// 20 (the agent run) + 60 is past the 64 GiB budget, so this is
	// refused about its own shape and Stuck gets a say.
	release, err := gate.Acquire(context.Background(),
		GateRequest{Want: model.Commitment{CPUs: 2, MemoryMB: 60 * 1024}})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer release()
	if asked.Load() == 0 {
		t.Fatal("Stuck was never asked about a refusal that was about this job's own shape")
	}
	if got := ledger.LiveIn(model.PoolCI).MemoryMB; got != 60*1024 {
		t.Errorf("a job Stuck admits is charged its own shape in the ledger: %d MiB in PoolCI, want %d", got, 60*1024)
	}
}

// Stuck is asked about a refusal that is about this job's own shape and
// about nothing else: a job held by the plain count has nothing to do
// with capacity that might or might not ever be released, and asking
// about it would be asking the wrong question of whatever answers it.
func TestSharedGateStuckIsNotAskedOnACountRefusal(t *testing.T) {
	var asked atomic.Int64
	gate := &SharedGate{
		Limit: func() int { return 1 },
		Poll:  time.Millisecond,
		Stuck: func(string) (bool, string) { asked.Add(1); return true, "x" },
	}
	held, err := gate.Acquire(context.Background(), GateRequest{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer held()

	waiter := queue(t, gate, GateRequest{})
	waiter.heldBy(t, "this deployment keeps for CI")
	if asked.Load() != 0 {
		t.Error("Stuck was asked about a refusal that was only ever about the count")
	}
}

// When an agent sandbox (e.g. 16 vCPU) is waiting for room in the shared
// budget alongside queued 4-vCPU CI jobs, releasing 4-vCPU CI sandboxes holds
// the freed slivers back for the agent sandbox and wakes dispatch rather than
// letting queued CI jobs consume each sliver as it appears.
func TestSharedGateHoldsBackCIJobsAndWakesDispatchForAWaitingAgentSandbox(t *testing.T) {
	ledger := &model.Ledger{}
	budget := budgetGateBudget() // 16 vCPU / 32 GiB
	agentShape := model.Commitment{CPUs: 16, MemoryMB: 32 * 1024}
	ciShape := budgetJobShape() // 4 vCPU / 8 GiB

	wakes := make(chan struct{}, 16)
	gate := &SharedGate{
		Budget: func() model.Budget { return budget },
		Ledger: func() *model.Ledger { return ledger },
		Wake:   func() { signal(wakes, struct{}{}) },
		Poll:   time.Millisecond,
	}

	// Four 4-vCPU CI jobs fill the 16-vCPU budget.
	var held [4]func()
	for i := range held {
		release, err := gate.Acquire(context.Background(), GateRequest{Want: ciShape})
		if err != nil {
			t.Fatalf("CI job %d of 4: %v", i+1, err)
		}
		held[i] = release
	}

	// A 16-vCPU agent task is ready and held back by the sandbox budget.
	limits := model.Limits{Budget: budget, AgentShape: agentShape, Ledger: ledger}
	if limits.SandboxAdmits() {
		t.Fatal("16-vCPU agent sandbox should not fit while four 4-vCPU CI jobs hold the budget")
	}
	limits.SetAgentWaiting(true)

	// Another 4-vCPU CI job queues behind the running four.
	queuedCI := queue(t, gate, GateRequest{Want: ciShape})
	queuedCI.heldBy(t, "sandbox budget")

	// Release three of the four 4-vCPU CI sandboxes one by one. Each release
	// frees 4 vCPUs (enough for queuedCI, not yet enough for the 16-vCPU
	// agent sandbox): the gate must hold the freed capacity back behind the
	// waiting agent sandbox and wake dispatch on each release.
	for i := 0; i < 3; i++ {
		held[i]()
		awaitSignal(t, wakes, "releasing a CI sandbox while an agent is waiting must wake dispatch")
	}
	why := queuedCI.heldBy(t, "waiting behind a queued agent sandbox")
	if got := ledger.LiveIn(model.PoolCI).CPUs; got != 4 {
		t.Fatalf("queued 4-vCPU CI job must not consume freed slivers while agent waits: PoolCI has %d vCPUs, want 4 (reason: %s)", got, why)
	}

	// Releasing the fourth 4-vCPU CI job frees the full 16 vCPUs and wakes
	// dispatch; the 16-vCPU agent sandbox now fits and claims the budget.
	held[3]()
	awaitSignal(t, wakes, "releasing the fourth CI sandbox must wake dispatch")

	verdict, releaseAgent := limits.ChargeSandbox("agent-run-1")
	if !verdict.OK {
		t.Fatalf("16-vCPU agent sandbox should fit once all four 4-vCPU CI sandboxes released: %s", verdict.Reason)
	}
	limits.SetAgentWaiting(false)

	// Once the agent sandbox finishes and no agent tasks are waiting, the
	// queued 4-vCPU CI job resumes admission normally.
	releaseAgent()
	queuedCI.admitted(t, "the queued 4-vCPU CI job once the agent sandbox is done")()
}

// Clearing the waiting-agent flag (for instance when the waiting task is
// cancelled or admitted with room to spare) immediately wakes CI jobs that
// were held back only by agent precedence.
func TestSharedGateResumesCIJobsWhenAgentStopsWaiting(t *testing.T) {
	ledger := &model.Ledger{}
	budget := budgetGateBudget()
	ciShape := budgetJobShape()

	gate := &SharedGate{
		Budget: func() model.Budget { return budget },
		Ledger: func() *model.Ledger { return ledger },
		Poll:   time.Hour, // rely on ledger change notification, not polling
	}
	ledger.SetAgentDemand(true, model.Commitment{CPUs: 16, MemoryMB: 32 * 1024})

	waiter := queue(t, gate, GateRequest{Want: ciShape})
	waiter.heldBy(t, "waiting behind a queued agent sandbox")

	ledger.SetAgentWaiting(false)
	waiter.admitted(t, "the CI job after the agent is no longer waiting")()
}

// Deadlock safety: if live agent runs holding the budget are blocked in
// wait_for_checks waiting on a CI job (Stuck), that CI job must still be
// admitted even while another agent task is queued and waiting on sandbox
// budget -- whether the CI job fits the remaining sliver of budget or not.
func TestSharedGateStuckAdmitsAJobEvenWhileAnAgentSandboxIsWaiting(t *testing.T) {
	ledger := &model.Ledger{}
	budget := budgetGateBudget() // 16 vCPU / 32 GiB
	agentShape := model.Commitment{CPUs: 16, MemoryMB: 32 * 1024}

	// A live agent run holds 12 vCPUs / 24 GiB and is waiting on "stuck-job".
	// Only 4 vCPUs / 8 GiB remain free, so a queued 16-vCPU agent task cannot
	// fit until the live agent run finishes its CI checks.
	t.Cleanup(ledger.Charge(model.PoolAgent, "live-agent", model.Commitment{CPUs: 12, MemoryMB: 24 * 1024}))
	ledger.SetAgentDemand(true, agentShape)

	var stuckID atomic.Value
	gate := &SharedGate{
		Budget: func() model.Budget { return budget },
		Ledger: func() *model.Ledger { return ledger },
		Poll:   time.Millisecond,
		Stuck: func(jobID string) (bool, string) {
			if id, _ := stuckID.Load().(string); id != "" && jobID == id {
				return true, "live-agent is waiting for stuck-job"
			}
			return false, ""
		},
	}

	// An unrelated 4-vCPU CI job fits the remaining 4 vCPUs, but Stuck is
	// false for it, so it must wait behind the queued agent sandbox.
	unrelated := queue(t, gate, GateRequest{Want: budgetJobShape()})
	unrelated.heldBy(t, "waiting behind a queued agent sandbox")

	// The 4-vCPU CI job that live-agent is waiting on also fits the remaining
	// 4 vCPUs, and Stuck is true for it: it must be admitted so the deployment
	// cannot deadlock.
	releaseStuck, err := gate.Acquire(context.Background(), GateRequest{
		Want:     budgetJobShape(),
		Assigned: func(id string) { stuckID.Store(id) },
	})
	if err != nil {
		t.Fatalf("Acquire for stuck-job: %v", err)
	}
	releaseStuck()
}

// A deadlock-breaking CI job (Stuck) may exceed the budget cap by at most one
// sandbox while existing commitments are within the cap; a second Stuck job
// waits until committed drops back under the cap.
func TestSharedGateStuckExceptionAdmitsAtMostOneJobOverBudget(t *testing.T) {
	ledger := &model.Ledger{}
	budget := budgetGateBudget() // 16 vCPU / 32 GiB
	// Fill the budget to 100% cap with a live agent run waiting on CI.
	t.Cleanup(ledger.Charge(model.PoolAgent, "live-agent", model.Commitment{CPUs: 16, MemoryMB: 32 * 1024}))

	gate := &SharedGate{
		Budget: func() model.Budget { return budget },
		Ledger: func() *model.Ledger { return ledger },
		Poll:   time.Millisecond,
		Stuck: func(jobID string) (bool, string) {
			return true, "live-agent is waiting for " + jobID
		},
	}

	// First stuck job is admitted as the one allowed exception over cap.
	releaseFirst, err := gate.Acquire(context.Background(), GateRequest{
		Want: budgetJobShape(),
	})
	if err != nil {
		t.Fatalf("Acquire first stuck job: %v", err)
	}
	if budget.WithinCap(ledger.Live()) {
		t.Fatalf("expected live commitment (%s) to exceed cap after first stuck job", ledger.Live())
	}

	// Second stuck job must wait while committed already exceeds the cap.
	second := queue(t, gate, GateRequest{Want: budgetJobShape()})
	second.heldBy(t, "memory")

	// Once the first stuck job finishes, committed is back at cap and the
	// second stuck job is admitted.
	releaseFirst()
	second.admitted(t, "second stuck job after first releases")()
}

// Unrelated sandboxes starting and finishing in the shared ledger wake every
// waiting CI job and change both live commitments and Ledger.Describe's
// "held by ..." breakdown. The UI callback (GateRequest.Wait) receives each
// fresh snapshot, while the daemon log emits one line per underlying reason
// and names which job is waiting.
func TestSharedGateDeduplicatesWaitingLogsAcrossLedgerChanges(t *testing.T) {
	ledger := &model.Ledger{}
	clock := newTestClock()
	budget := model.Budget{
		Host:             model.Commitment{CPUs: 32, MemoryMB: 64 * 1024, DiskGB: 200},
		CPUOvercommit:    1,
		MemoryOvercommit: 1,
		DiskOvercommit:   1,
	}

	var mu sync.Mutex
	var logged []string
	gate := &SharedGate{
		Budget: func() model.Budget { return budget },
		Ledger: func() *model.Ledger { return ledger },
		Now:    clock.Now,
		Poll:   time.Hour, // rely strictly on ledgerWake notifications
		Log: func(format string, args ...any) {
			mu.Lock()
			defer mu.Unlock()
			logged = append(logged, fmt.Sprintf(format, args...))
		},
	}

	// Hold 170 GiB of the 200 GiB disk budget across two agent sandboxes so a
	// 40 GiB CI job waits on disk while CPU and memory have plenty of room.
	relA1 := ledger.Charge(model.PoolAgent, "agent-1", model.Commitment{CPUs: 4, MemoryMB: 8 * 1024, DiskGB: 150})
	defer relA1()
	relA2 := ledger.Charge(model.PoolAgent, "agent-2", model.Commitment{CPUs: 4, MemoryMB: 8 * 1024, DiskGB: 20})
	defer relA2()

	jobWant := model.Commitment{CPUs: 4, MemoryMB: 16 * 1024, DiskGB: 40}
	waiter := queue(t, gate, GateRequest{
		Job:  "ai-on-gke/grain/r-42/go",
		Want: jobWant,
	})
	if why := waiter.heldBy(t, "disk:"); !strings.Contains(why, "2 agent sandboxes") {
		t.Fatalf("initial wait should include live ledger breakdown, got %q", why)
	}

	// Start and finish unrelated sandboxes in the ledger that alter both live
	// disk/CPU/memory totals and the "held by ..." pool breakdown, without
	// freeing enough disk for jobWant.
	relA3 := ledger.Charge(model.PoolAgent, "agent-3", model.Commitment{CPUs: 2, MemoryMB: 4 * 1024, DiskGB: 10})
	waiter.heldBy(t, "3 agent sandboxes")
	relA3()
	waiter.heldBy(t, "2 agent sandboxes")

	relPersist := ledger.Charge(model.PoolPersistent, "persist-1", model.Commitment{CPUs: 2, MemoryMB: 4 * 1024, DiskGB: 15})
	waiter.heldBy(t, "persistent sandbox")

	// Cross ReserveAfter so the waiter transitions from roomWait to holeWait on
	// the same disk dimension; the UI banner updates, while daemon log stays quiet.
	clock.advance(2 * DefaultReserveAfter)
	relPersist()
	waiter.heldBy(t, "hole big enough")

	// Now shift the bottleneck to a genuinely different dimension (memory):
	// occupy memory with another agent run and release agent-2's 20 GiB disk.
	relMem := ledger.Charge(model.PoolAgent, "agent-mem", model.Commitment{CPUs: 4, MemoryMB: 48 * 1024})
	relA2()
	waiter.heldBy(t, "memory:")

	// Release memory blocker so the job is admitted.
	relMem()
	waiter.admitted(t, "CI job once both disk and memory clear")()

	mu.Lock()
	defer mu.Unlock()
	var waitingLines []string
	for _, line := range logged {
		if strings.Contains(line, "is waiting --") {
			waitingLines = append(waitingLines, line)
		}
	}
	if len(waitingLines) != 2 {
		t.Fatalf("expected 2 waiting log lines (once for disk, once for memory), got %d:\n%s",
			len(waitingLines), strings.Join(logged, "\n"))
	}
	const wantPrefix = "ci: job ai-on-gke/grain/r-42/go is waiting -- "
	for i, line := range waitingLines {
		if !strings.HasPrefix(line, wantPrefix) {
			t.Errorf("waiting line %d = %q, want prefix %q", i, line, wantPrefix)
		}
	}
	if !strings.Contains(waitingLines[0], "disk:") {
		t.Errorf("first waiting line should report disk shortage, got %q", waitingLines[0])
	}
	if !strings.Contains(waitingLines[1], "memory:") {
		t.Errorf("second waiting line should report memory shortage, got %q", waitingLines[1])
	}
}

func TestEngineIncludesRepoRunAndJobInGateWaitingLogs(t *testing.T) {
	var limit atomic.Int64
	var mu sync.Mutex
	var logged []string
	gate := &SharedGate{
		Limit: func() int { return int(limit.Load()) },
		Poll:  time.Millisecond,
		Log: func(format string, args ...any) {
			mu.Lock()
			defer mu.Unlock()
			logged = append(logged, fmt.Sprintf(format, args...))
		},
	}
	plan := planFor(t, Request{Repo: "ai-on-gke/grain", Ref: "main", Event: EventPush}, map[string]string{
		".grain/workflows/tests.yml": "on: push\njobs:\n  unit:\n    steps:\n      - run: true\n",
	})
	engine := &Engine{
		Sandboxes: &fakeSandboxes{},
		Jobs:      gate,
	}

	waits := make(chan JobRun, 4)
	done := make(chan Run, 1)
	go func() {
		done <- engine.Run(context.Background(), "run-99", plan, func(job JobRun) {
			if job.Activity != "" {
				signal(waits, job)
			}
		})
	}()
	awaitSignal(t, waits, "the job never reported waiting at the gate")
	limit.Store(1)
	if run := awaitSignal(t, done, "the run never completed"); run.Conclusion != ConclusionSuccess {
		t.Fatalf("run conclusion = %q (%s), want success", run.Conclusion, run.Error)
	}

	mu.Lock()
	defer mu.Unlock()
	const wantPrefix = "ci: job ai-on-gke/grain/run-99/unit is waiting -- "
	found := false
	for _, line := range logged {
		if strings.HasPrefix(line, wantPrefix) {
			found = true
		}
	}
	if !found {
		t.Errorf("expected log line starting with %q, got:\n%s", wantPrefix, strings.Join(logged, "\n"))
	}
}
