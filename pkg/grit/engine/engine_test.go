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
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	gritsteps "github.com/ai-on-gke/grain/pkg/grit/steps"
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

type fakeSandbox struct {
	name     string
	execFn   func(ctx context.Context, cmd gritsteps.Command) (gritsteps.CommandResult, error)
	released *atomic.Int32
}

func (s *fakeSandbox) Name() string { return s.name }
func (s *fakeSandbox) Exec(ctx context.Context, cmd gritsteps.Command) (gritsteps.CommandResult, error) {
	if s.execFn != nil {
		return s.execFn(ctx, cmd)
	}
	return gritsteps.CommandResult{ExitCode: 0}, nil
}
func (s *fakeSandbox) Release(context.Context) error {
	if s.released != nil {
		s.released.Add(1)
	}
	return nil
}

type fakeSandboxes struct {
	acquireFn func(ctx context.Context, req gritsteps.SandboxRequest) (gritsteps.Sandbox, error)
}

func (f *fakeSandboxes) Acquire(ctx context.Context, req gritsteps.SandboxRequest) (gritsteps.Sandbox, error) {
	return f.acquireFn(ctx, req)
}

func TestStoppingSkipsNewJobsAndAutomaticRetriesAndRecordsInterrupted(t *testing.T) {
	stopping := make(chan struct{})
	var acquired atomic.Int32
	var released atomic.Int32
	var secondStarted atomic.Bool

	sb := &fakeSandboxes{
		acquireFn: func(ctx context.Context, req gritsteps.SandboxRequest) (gritsteps.Sandbox, error) {
			n := acquired.Add(1)
			return &fakeSandbox{
				name:     fmt.Sprintf("sb-%d", n),
				released: &released,
				execFn: func(ctx context.Context, cmd gritsteps.Command) (gritsteps.CommandResult, error) {
					if cmd.Script == "run-second" {
						secondStarted.Store(true)
						return gritsteps.CommandResult{ExitCode: 0}, nil
					}
					close(stopping)
					return gritsteps.CommandResult{
						ExitCode: 128,
						Output: "fatal: unable to access 'http://127.0.0.1:9728/ai-on-gke/grain.git/': Recv failure: Connection reset by peer\n" +
							"Actions cache http://127.0.0.1:9758/_apis/artifactcache/ is not reachable",
					}, nil
				},
			}, nil
		},
	}

	eng := &Engine{
		Sandboxes:        sb,
		Stopping:         stopping,
		ConcurrencyLimit: func() int { return 1 },
	}
	plan := workflows.Plan{
		Key: "plan-1",
		Request: workflows.Request{
			Repo:   "ai-on-gke/grain",
			Ref:    "main",
			Commit: "abc123",
			Event:  workflows.EventPush,
		},
		Jobs: []workflows.PlannedJob{
			{
				ID:           "first",
				Workflow:     "ci",
				WorkflowPath: ".grain/workflows/ci.yml",
				Key:          "key-first",
				Job: workflows.Job{
					ID:    "first",
					Name:  "first",
					Retry: true,
					Steps: []workflows.Step{
						{Name: "checkout", Run: "run-first", Infrastructure: true},
						{Name: "later-step", Run: "run-second"},
					},
				},
			},
			{
				ID:           "second",
				Workflow:     "ci",
				WorkflowPath: ".grain/workflows/ci.yml",
				Key:          "key-second",
				Needs:        []string{"first"},
				Job: workflows.Job{
					ID:    "second",
					Name:  "second",
					Needs: []string{"first"},
					Steps: []workflows.Step{
						{Name: "test", Run: "run-second"},
					},
				},
			},
		},
	}

	run := eng.Run(context.Background(), "run-1", plan, nil)
	if got := acquired.Load(); got != 1 {
		t.Fatalf("Sandboxes.Acquire count = %d, want 1 (no automatic retry or second job)", got)
	}
	if secondStarted.Load() {
		t.Fatal("second step or job started after Stopping was closed")
	}
	if got := released.Load(); got != 1 {
		t.Fatalf("sandbox release count = %d, want 1", got)
	}
	if !run.Interrupted || run.Conclusion != workflows.ConclusionCancelled {
		t.Fatalf("run = (Interrupted=%v, Conclusion=%q, Error=%q), want Interrupted=true Conclusion=cancelled",
			run.Interrupted, run.Conclusion, run.Error)
	}
	if run.Error != workflows.InterruptedRunError {
		t.Fatalf("run.Error = %q, want %q", run.Error, workflows.InterruptedRunError)
	}
	for _, j := range run.Jobs {
		if j.Conclusion != workflows.ConclusionCancelled || j.Error != workflows.InterruptedRunError {
			t.Fatalf("job %s = (Conclusion=%q, Error=%q), want cancelled with InterruptedRunError",
				j.JobID, j.Conclusion, j.Error)
		}
	}
}

func TestStoppingDuringRetrySandboxAcquireRecordsInterruptedInsteadOfErrored(t *testing.T) {
	stopping := make(chan struct{})
	var acquired atomic.Int32
	var released atomic.Int32

	sb := &fakeSandboxes{
		acquireFn: func(ctx context.Context, req gritsteps.SandboxRequest) (gritsteps.Sandbox, error) {
			n := acquired.Add(1)
			if n == 2 {
				// Simulate base run 55d1a2c6dc5fc22f: retry starts and daemon begins
				// shutting down while retry is cloning from the torn-down git proxy.
				close(stopping)
				return nil, fmt.Errorf("checking out repository: git clone http://127.0.0.1:9728/ai-on-gke/grain.git: Connection reset by peer; Actions cache not reachable")
			}
			return &fakeSandbox{
				name:     fmt.Sprintf("sb-%d", n),
				released: &released,
				execFn: func(ctx context.Context, cmd gritsteps.Command) (gritsteps.CommandResult, error) {
					return gritsteps.CommandResult{
						ExitCode: 1,
						Output:   "transient connection reset by peer",
					}, nil
				},
			}, nil
		},
	}

	eng := &Engine{
		Sandboxes: sb,
		Stopping:  stopping,
	}
	plan := workflows.Plan{
		Key: "plan-retry-stop",
		Request: workflows.Request{
			Repo:   "ai-on-gke/grain",
			Ref:    "main",
			Commit: "def456",
			Event:  workflows.EventPush,
		},
		Jobs: []workflows.PlannedJob{
			{
				ID:           "build",
				Workflow:     "ci",
				WorkflowPath: ".grain/workflows/ci.yml",
				Key:          "key-build",
				Job: workflows.Job{
					ID:   "build",
					Name: "build",
					Steps: []workflows.Step{
						{Name: "test", Run: "go test ./..."},
					},
				},
			},
		},
	}

	run := eng.Run(context.Background(), "run-retry-stop", plan, nil)
	if !run.Interrupted || run.Conclusion != workflows.ConclusionCancelled {
		t.Fatalf("run = (Interrupted=%v, Conclusion=%q, Error=%q), want Interrupted=true Conclusion=cancelled",
			run.Interrupted, run.Conclusion, run.Error)
	}
	if len(run.Jobs) != 1 || run.Jobs[0].Conclusion != workflows.ConclusionCancelled {
		t.Fatalf("run.Jobs = %+v, want 1 cancelled job", run.Jobs)
	}
}
