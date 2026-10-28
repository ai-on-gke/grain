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

package engine

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/workflows"
)

// The clock itself, at the granularity the engine cannot show: an
// infrastructure step spends its own time and gives it back, so the steps
// after it are where they would have been had the cache been warm.
func TestTheJobsClockDoesNotChargeItForFetchingItsEnvironment(t *testing.T) {
	job := workflows.Job{TimeoutMinutes: 30}
	clock := &jobClock{deadline: time.Now().Add(30 * time.Minute)}
	before := clock.deadline

	ctx, spent := clock.bound(context.Background(), job, workflows.Step{Infrastructure: true, TimeoutMinutes: 120})
	deadline, ok := ctx.Deadline()
	if !ok || deadline.Sub(before) < 80*time.Minute {
		t.Fatalf("an infrastructure step's deadline is %v (job's is %v), want its own 2h", deadline, before)
	}
	time.Sleep(2 * time.Millisecond)
	spent()
	if !clock.deadline.After(before) {
		t.Errorf("the job's deadline is still %v: the time spent fetching its environment was charged to it", clock.deadline)
	}

	// An ordinary step is bounded by the job's own deadline and spends it,
	// which is what the key changes nothing about.
	ordinary := clock.deadline
	ctx, spent = clock.bound(context.Background(), job, workflows.Step{})
	deadline, ok = ctx.Deadline()
	if !ok || !deadline.Equal(ordinary) {
		t.Errorf("an ordinary step's deadline is %v, want the job's own %v", deadline, ordinary)
	}
	spent()
	if !clock.deadline.Equal(ordinary) {
		t.Errorf("the job's deadline moved to %v: only an infrastructure step gives time back", clock.deadline)
	}
}

// The clock underneath all of that, on its own: time nobody attributed
// to anything is still part of the wait, but it is never what the wait
// is blamed on -- a duration with no reason is an honest answer and an
// invented reason is not.
func TestQueueClockCountsUnattributedTimeWithoutBlamingItOnAnything(t *testing.T) {
	start := time.Date(2026, 9, 18, 14, 0, 0, 0, time.UTC)
	q := newQueueClock(start)
	// Ten minutes nothing reported, then two of the count.
	q.waiting(start.Add(10*time.Minute), workflows.Wait{Cause: workflows.WaitForSlot, Reason: "for one of the 3 sandboxes"})
	queued, cause, reason := q.stop(start.Add(12 * time.Minute))

	if queued != 12*time.Minute {
		t.Errorf("queued = %s, want the whole 12m including the time nobody accounted for", queued)
	}
	if cause != workflows.WaitForSlot {
		t.Errorf("cause = %q, want the only cause that ever held it", cause)
	}
	if reason != "for one of the 3 sandboxes" {
		t.Errorf("reason = %q, want the only thing that ever reported holding it", reason)
	}

	silent := newQueueClock(start)
	queued, cause, reason = silent.stop(start.Add(3 * time.Minute))
	if queued != 3*time.Minute || cause != "" || reason != "" {
		t.Errorf("a wait nothing reported = %s %q %q, want the duration and no cause or reason at all", queued, cause, reason)
	}
}

func TestRunGateBoundsOneRunInArrivalOrder(t *testing.T) {
	gate := &runGate{limit: 1}
	ctx := context.Background()

	rel1, err := gate.Acquire(ctx, GateRequest{})
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	var waits []workflows.Wait
	reported := make(chan workflows.Wait, 4)
	done2 := make(chan func())
	go func() {
		rel, err := gate.Acquire(ctx, GateRequest{
			Wait: func(w workflows.Wait) {
				reported <- w
			},
		})
		if err != nil {
			t.Errorf("second Acquire: %v", err)
		}
		done2 <- rel
	}()

	firstWait := <-reported
	waits = append(waits, firstWait)
	if firstWait.Cause != workflows.WaitForRun || firstWait.Reason != "for one of the 1 sandbox this run may hold at once; 1 job of it holds one right now" {
		t.Fatalf("first wait = %+v", firstWait)
	}

	rel1()
	rel2 := <-done2
	cleared := <-reported
	waits = append(waits, cleared)
	if cleared != (workflows.Wait{}) {
		t.Fatalf("cleared wait = %+v, want zero Wait", cleared)
	}
	rel2()
}
