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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The disk half of the gate (grain/task-196). The filesystem figures are
// stubbed rather than read, the same way budgetGate above stubs the
// host: these prove what the gate does with a reading, and pkg/model
// proves what the floor makes of one.

// diskGate is the deployment's gate with a volume behind it: a count, a
// memory budget, and a free-space reading the test moves as a run would.
// The reading is taken per attempt, exactly as cmd/grain takes it, which
// is what lets a waiting job be admitted once space is freed without
// anything having been released.
func diskGate(limit int, free func() int) *SharedGate {
	return &SharedGate{
		Limit: func() int { return limit },
		Budget: func() model.Budget {
			return model.Budget{
				Host:             model.Commitment{CPUs: 28, MemoryMB: 80 * 1024},
				ReserveMemoryMB:  16 * 1024,
				CPUOvercommit:    4,
				MemoryOvercommit: 1,
				DiskFloor:        model.DefaultDiskFloor(),
				Disk: model.DiskSpace{
					Path:    "/var/lib/grain/data",
					TotalMB: 443 * 1024,
					FreeMB:  free(),
				},
			}
		},
		Poll: time.Millisecond,
	}
}

// freeSpace is a volume the test writes to and the gate reads, without
// the two racing over it.
type freeSpace struct {
	mu sync.Mutex
	mb int
}

func (f *freeSpace) get() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mb
}

func (f *freeSpace) set(mb int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mb = mb
}

// The failure this exists for: /var/lib/grain/data went from 70 GB to 181 GB
// of 443 GB in an hour, and nothing in a count or a memory budget had
// anything to say about it.
func TestSharedGateHoldsAJobWhileTheVolumeIsUnderItsFloor(t *testing.T) {
	disk := &freeSpace{mb: 20 * 1024} // 20 GiB of 443, against a 44 GiB floor
	gate := diskGate(15, disk.get)

	// It is held even though nothing at all holds the gate: the count is
	// empty and the memory budget is empty, and the disk is the whole
	// reason. The gate saying so to the job is the refusal -- see
	// acquiring.heldBy -- and nothing but the reading below can change it.
	waiter := queue(t, gate, GateRequest{Want: smallJob()})
	if why := waiter.heldBy(t, "disk"); !strings.Contains(why, "/var/lib/grain/data") {
		t.Errorf("the reason does not name the volume: %q", why)
	}

	// And it is admitted once space is freed, without anything having
	// been released -- the reading is taken again on the next attempt.
	disk.set(200 * 1024)
	waiter.admitted(t, "the job, once the volume was cleared")()
}

// The floor is where admission stops, not the last byte: a volume with
// room to spare admits the job it would have held a moment ago.
func TestSharedGateAdmitsAJobOnAVolumeClearOfItsFloor(t *testing.T) {
	disk := &freeSpace{mb: 200 * 1024}
	gate := diskGate(15, disk.get)
	release, err := gate.Acquire(context.Background(), GateRequest{Want: smallJob()})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	release()

	// Exactly on the floor is still admitted -- a floor is a floor.
	disk.set(model.DefaultDiskFloor().FloorMB(443 * 1024))
	release, err = gate.Acquire(context.Background(), GateRequest{Want: smallJob()})
	if err != nil {
		t.Fatalf("Acquire on exactly the floor: %v", err)
	}
	release()
}

// And what an operator actually sees: a job held for disk reads as held
// for disk in the run pane, the same way one held for memory does
// (JobRun.Activity, grain/task-173's own reporting).
func TestEngineReportsAJobWaitingForDiskAsWaitingForDisk(t *testing.T) {
	sandboxes := &fakeSandboxes{}
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  one:\n    steps:\n      - run: a\n",
	})
	disk := &freeSpace{mb: 20 * 1024}
	engine := &Engine{Sandboxes: sandboxes, Concurrency: 1, Jobs: diskGate(8, disk.get)}

	// The observer keeps the disk waits and drops everything else, and
	// never blocks the engine doing it: a run whose observations stop
	// being read stops.
	diskWaits := make(chan JobRun, 4)
	done := make(chan Run, 1)
	go func() {
		done <- engine.Run(context.Background(), "r1", plan, func(j JobRun) {
			if strings.Contains(j.Activity, "disk") && j.Sandbox == "" {
				signal(diskWaits, j)
			}
		})
	}()

	waiting := awaitSignal(t, diskWaits, "the job was never reported as waiting for disk")
	if waiting.ActivityAt == nil {
		t.Error("a job reported as waiting says since when")
	}

	disk.set(300 * 1024)
	if run := awaitSignal(t, done, "the run never finished once the volume was cleared"); run.Conclusion != ConclusionSuccess {
		t.Fatalf("the job ran once there was room: %q (%s)", run.Conclusion, run.Error)
	}
}

// Disk is a third bound rather than a replacement for either of the two
// that were already there.
func TestSharedGateStillCountsAndStillWeighsWithADiskFloor(t *testing.T) {
	disk := &freeSpace{mb: 300 * 1024}

	// The count, with the volume and the memory budget both roomy.
	countedGate := diskGate(1, disk.get)
	first, err := countedGate.Acquire(context.Background(), GateRequest{Want: smallJob()})
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	second := queue(t, countedGate, GateRequest{Want: smallJob()})
	if why := second.heldBy(t, "sandbox"); strings.Contains(why, "disk") {
		t.Errorf("a job held by the count reports disk: %q", why)
	}
	first()
	second.admitted(t, "the second job, once the first gave its permit back")()

	// And memory, on the same roomy volume: four 16 GiB jobs is the
	// whole 64 GiB budget, and the fifth waits for memory rather than
	// for disk.
	weighed := diskGate(15, disk.get)
	for i := 0; i < 4; i++ {
		if _, err := weighed.Acquire(context.Background(), GateRequest{Want: bigJob()}); err != nil {
			t.Fatalf("job %d of 4: %v", i+1, err)
		}
	}
	fifth := queue(t, weighed, GateRequest{Want: bigJob()})
	if why := fifth.heldBy(t, "memory"); strings.Contains(why, "disk") {
		t.Errorf("the fifth 16 GiB job is waiting for %q, want memory", why)
	}
}
