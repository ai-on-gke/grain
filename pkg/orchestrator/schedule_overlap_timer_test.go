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

// The overlap policy (model.OverlapPolicy) under the daemon's own timer,
// with a firing that is genuinely still running rather than an open task
// somebody wrote into the store.
//
// schedule_test.go already covers the decision: it files a firing, forces
// the schedule due again, and asserts what the next RunCycle does about a
// task it left open. What it cannot show is the thing the policy is
// actually about -- "may this firing start while the last one is still
// running" is a question about two firings overlapping in time, and the
// state it is answered from (a previous firing's task, still unclosed) is
// there because an agent is in the middle of the work, not because a test
// put it there. The tests here dispatch the firing for real, hold its
// agent inside framework.Run, and let a timer go on calling RunCycle
// around it, which is the shape cmd/grain's own reconcile loop has.
//
// What that turns up, and what the table below pins, is the half of
// "unfinished" nobody had looked at: a firing whose run has *finished* is
// still not a firing that is over. A chore that answers in a comment ends
// completed, not closed (orchestrator.ProcessResult), and
// Store.HasOpenTaskWithTag reads every state but closed as open -- so
// under the default policy such a schedule does not fire again until
// somebody closes the task. Whether that is the intended reading of "the
// previous firing has finished" is a question for a human; this test says
// what today's answer is rather than deciding it.

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// slowFramework is the deliberately slow firing: an agent that records
// the run it was given and then stays inside Run until the test lets it
// out, so a firing is live for exactly as long as the test wants it to
// be. It ends the way a scheduled chore ordinarily does -- a comment and
// nothing else, which is what a nightly report or a dependency check
// leaves behind.
//
// blockingFramework (concurrent_dispatch_test.go) is the same idea with a
// channel of entered runs; this one keeps a slice instead, because a
// schedule set to model.OverlapAllow files a firing every tick and
// nothing here reads the runs back in order.
type slowFramework struct {
	mu       sync.Mutex
	entered  []string
	release  chan struct{}
	released sync.Once
}

func newSlowFramework() *slowFramework {
	return &slowFramework{release: make(chan struct{})}
}

func (f *slowFramework) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	f.mu.Lock()
	f.entered = append(f.entered, filepath.Base(cfg.SandboxRoot))
	f.mu.Unlock()
	select {
	case <-f.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return toolResult(agent.ToolCall{
		Name:      "comment_on_issue",
		Arguments: map[string]any{"comment": "the chore ran"},
	}), nil
}

// runsEntered is how many runs have reached the agent so far.
func (f *slowFramework) runsEntered() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.entered)
}

// releaseAll lets every run still inside Run finish, and every later one
// pass straight through. Idempotent, because the test's own cleanup
// releases whatever the test itself did not.
func (f *slowFramework) releaseAll() { f.released.Do(func() { close(f.release) }) }

// daemonTimer is cmd/grain's reconcile loop, in a test: RunCycle on a
// real ticker, one tick at a time, never overlapped.
//
// Two clocks, exactly as a daemon has them. The ticker is milliseconds --
// the poll interval, how often the deployment looks -- and the `now` each
// cycle is handed moves an hour per tick, which is the cadence being
// polled for (the schedules here recur every hour). Compressing the
// second one is what keeps this a test rather than an hour's wait; the
// concurrency it is testing is not compressed at all, because the run
// held open across those ticks is a real dispatched run.
//
// ticks is unbuffered on purpose: the loop parks on the send after each
// cycle, so a test that has stopped waiting has at most one further cycle
// running underneath its assertions rather than an unbounded number.
type daemonTimer struct {
	ticks chan struct{}
	// stop ends the loop, and is called by this timer's own cleanup as
	// well as by anything that wants the cycles to stop earlier.
	stop func()

	mu   sync.Mutex
	errs []error
}

func startDaemonTimer(t *testing.T, ctx context.Context, deps orchestrator.Deps, start time.Time) *daemonTimer {
	t.Helper()
	return startDaemonTimerStepping(t, ctx, deps, start, time.Hour)
}

// startDaemonTimerStepping is startDaemonTimer with the `now` each cycle
// is handed moving step per tick rather than an hour -- for a schedule
// whose cadence is in minutes, where an hour a tick would find several
// occurrences due at once on every cycle and never one at a time.
func startDaemonTimerStepping(t *testing.T, ctx context.Context, deps orchestrator.Deps,
	start time.Time, step time.Duration) *daemonTimer {

	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	d := &daemonTimer{ticks: make(chan struct{}), stop: sync.OnceFunc(cancel)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for n := 1; ; n++ {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if err := orchestrator.RunCycle(ctx, deps, start.Add(time.Duration(n)*step)); err != nil {
				d.mu.Lock()
				d.errs = append(d.errs, err)
				d.mu.Unlock()
			}
			select {
			case d.ticks <- struct{}{}:
			case <-ctx.Done():
				return
			}
		}
	}()
	t.Cleanup(func() {
		d.stop()
		<-done
	})
	return d
}

// drainAfter stops the timer and lets every run it started finish before
// the test returns, so the store outlives the goroutines writing to it: a
// run still in flight when openStore's own cleanup closes the database
// logs a pile of failures about a database that is closed, none of which
// is what the test was measuring.
func drainAfter(t *testing.T, timer *daemonTimer, fw *slowFramework, runs *orchestrator.InFlight) {
	t.Helper()
	t.Cleanup(func() {
		timer.stop()
		fw.releaseAll()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := runs.Wait(ctx); err != nil {
			t.Errorf("waiting for the runs the timer started: %v", err)
		}
	})
}

// wait lets n more cycles complete, failing rather than hanging if the
// loop has stopped producing them.
func (d *daemonTimer) wait(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-d.ticks:
		case <-time.After(60 * time.Second):
			t.Fatalf("the daemon timer stopped after %d of %d ticks", i, n)
		}
	}
}

// waitUntil lets up to n more cycles complete, stopping as soon as cond
// holds and failing with why if it never does. Anything a further *tick*
// has to bring about is waited for this way rather than with waitFor
// below: the loop parks on its own send between cycles, so a wait that
// takes no ticks is a wait during which no cycle runs.
func (d *daemonTimer) waitUntil(t *testing.T, why string, n int, cond func() bool) {
	t.Helper()
	for i := 0; i < n; i++ {
		if cond() {
			return
		}
		d.wait(t, 1)
	}
	if !cond() {
		t.Fatalf("%s did not happen within %d cycles", why, n)
	}
}

// failed reports whatever RunCycle returned across the whole run, which
// is otherwise swallowed by a loop that (like the real one) logs and
// carries on.
func (d *daemonTimer) failed(t *testing.T) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, err := range d.errs {
		t.Errorf("RunCycle: %v", err)
	}
}

// hourlySchedule is filedSchedule's cadence set to the shortest one a
// deployment can ask for, so that every tick of the timer above finds it
// due again -- the pile-up the policy exists to decide about.
func hourlySchedule(t *testing.T, store *model.Store, ctx context.Context,
	id string, overlap model.OverlapPolicy, start time.Time) model.Schedule {

	t.Helper()
	sched := filedSchedule(t, store, id, start)
	sched.Recurrence = model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 1}
	sched.Overlap = overlap
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatal(err)
	}
	return sched
}

// waitFor polls cond until it holds, failing with why rather than
// hanging: every wait here is on something another goroutine (the timer,
// or a dispatched run finishing) is in the middle of doing.
func waitFor(t *testing.T, why string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", why)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestOverlapPolicyDecidesASecondFiringWhileTheFirstIsStillRunning(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		// policy is what the schedule says. The empty one is a schedule
		// written before the field existed, or by any caller that never
		// sets it, and has to read as skipping (OverlapPolicy.Allows).
		policy model.OverlapPolicy
		// overlaps is whether a firing may start while the previous one
		// is still inside its agent.
		overlaps bool
	}{
		{"skip", model.OverlapSkip, false},
		{"unset, which is skipping", "", false},
		{"allow", model.OverlapAllow, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, ctx := openStore(t)
			sched := hourlySchedule(t, store, ctx, "sched-1", tc.policy, baseTime)
			tag := "schedule:" + sched.ID

			fw := newSlowFramework()
			// Room for several firings at once, so what decides whether a
			// second one runs is the policy and never the worker count.
			deps, runs := asyncDeps(t, store, fw, 4)
			timer := startDaemonTimer(t, ctx, deps, baseTime)
			drainAfter(t, timer, fw, runs)

			// The first firing, filed and dispatched by the timer's own
			// cycles, and now sitting inside the agent.
			waitFor(t, "the first firing to reach the agent", func() bool {
				return fw.runsEntered() >= 1
			})
			firings := scheduledTasksTagged(t, store, tag)
			if len(firings) != 1 {
				t.Fatalf("filed %d tasks up to the first dispatch, want 1", len(firings))
			}
			first := firings[0]

			// Three more cycles, every one of them finding the schedule
			// due again with that run still live. This is the overlap the
			// policy is about.
			timer.wait(t, 3)
			switch filed := scheduledTasksTagged(t, store, tag); {
			case tc.overlaps && len(filed) < 2:
				t.Fatalf("filed %d tasks while the first firing was still running, want a second one", len(filed))
			case !tc.overlaps && len(filed) != 1:
				t.Fatalf("filed %d tasks while the first firing was still running, want still 1", len(filed))
			}
			if tc.overlaps {
				// Not merely filed: actually running alongside the first,
				// which is what "allow" is for.
				waitFor(t, "a second firing to be running alongside the first", func() bool {
					n, err := store.LiveRunCount(ctx)
					return err == nil && n >= 2
				})
			}

			// The slow firing's agent returns. Its run ends and its task
			// is completed -- and, since a comment is not a close, that
			// task is still open by HasOpenTaskWithTag's reading, so a
			// skipping schedule goes on waiting for it.
			//
			// 'awaiting_ack' is what that completion reads as: this
			// agent's whole turn is a comment_on_issue call, so the task
			// finishes with no pull request behind it (model.AwaitsAck).
			// Which state of the post-run three it lands in decides
			// nothing here -- the point is the run being over and the task
			// not being closed -- but the wait has to name the one it
			// really reaches or it waits forever.
			fw.releaseAll()
			waitFor(t, "the first firing's run to finish", func() bool {
				st, err := store.State(ctx, first.ID)
				return err == nil && st == model.StateAwaitingReply
			})
			timer.wait(t, 3)
			if !tc.overlaps {
				if filed := scheduledTasksTagged(t, store, tag); len(filed) != 1 {
					t.Fatalf("filed %d tasks while the first firing was completed but not closed, want still 1", len(filed))
				}
			}

			// Closed -- what a merged pull request, or a human clicking
			// Close, does to it -- and the schedule picks straight up: the
			// firings it did not make were delayed, not lost.
			closedAt := baseTime.Add(24 * time.Hour)
			if err := store.Observe(ctx, model.Observation{TaskID: first.ID, ClosedAt: &closedAt}); err != nil {
				t.Fatal(err)
			}
			before := len(scheduledTasksTagged(t, store, tag))
			timer.waitUntil(t, "the schedule firing again once its previous firing closed", 5, func() bool {
				return len(scheduledTasksTagged(t, store, tag)) > before
			})
			timer.failed(t)
		})
	}
}

// A minutes cadence (model.RecurrenceEveryNMinutes) under the same timer,
// polled every five minutes of `now`: the cadence a release manager that
// wakes every half hour asks for, and the ten-minute floor. However many
// occurrences come and go while the first firing is still inside its
// agent, the schedule does not stack a second one on top of it -- the
// held occurrences collapse into one skip and, once the first firing
// closes, into one late firing.
func TestOverlapSkipHoldsAMinutesCadenceWhileItsFiringIsStillRunning(t *testing.T) {
	t.Parallel()
	for _, minutes := range []int{model.MinEveryNMinutes, 30} {
		t.Run(fmt.Sprintf("every %dm", minutes), func(t *testing.T) {
			t.Parallel()
			store, ctx := openStore(t)
			sched := filedSchedule(t, store, "sched-1", baseTime)
			sched.Recurrence = model.Recurrence{Kind: model.RecurrenceEveryNMinutes, EveryNMinutes: minutes}
			sched.Overlap = model.OverlapSkip
			if err := store.PutSchedule(ctx, sched); err != nil {
				t.Fatal(err)
			}
			tag := "schedule:" + sched.ID

			fw := newSlowFramework()
			deps, runs := asyncDeps(t, store, fw, 4)
			timer := startDaemonTimerStepping(t, ctx, deps, baseTime, 5*time.Minute)
			drainAfter(t, timer, fw, runs)

			waitFor(t, "the first firing to reach the agent", func() bool {
				return fw.runsEntered() >= 1
			})
			first := scheduledTasksTagged(t, store, tag)
			if len(first) != 1 {
				t.Fatalf("filed %d tasks up to the first dispatch, want 1", len(first))
			}

			// Two hours of polls with that run still live: four to
			// twelve occurrences, none of which may start a second run.
			timer.wait(t, 24)
			if filed := scheduledTasksTagged(t, store, tag); len(filed) != 1 {
				t.Fatalf("filed %d tasks while the first firing was still running, want still 1", len(filed))
			}
			if n := fw.runsEntered(); n != 1 {
				t.Fatalf("%d runs reached the agent while the first was still running, want 1", n)
			}
			skips := skipsOf(t, store, sched.ID)
			if len(skips) != 1 || skips[0].Outcome != model.ScheduleSkipHeld ||
				skips[0].TaskID != first[0].ID {
				t.Fatalf("skips = %+v, want the held occurrences collapsed into one, held on %s",
					skips, first[0].ID)
			}

			// Closed: the schedule fires once more -- one late firing for
			// every occurrence it held, not one per occurrence.
			fw.releaseAll()
			closedAt := baseTime.Add(24 * time.Hour)
			if err := store.Observe(ctx, model.Observation{TaskID: first[0].ID, ClosedAt: &closedAt}); err != nil {
				t.Fatal(err)
			}
			timer.waitUntil(t, "the schedule firing again once its previous firing closed", 5, func() bool {
				return len(scheduledTasksTagged(t, store, tag)) > 1
			})
			// The late firing's own task is open in turn, so the polls
			// after it hold again rather than catching up.
			timer.wait(t, 6)
			if filed := scheduledTasksTagged(t, store, tag); len(filed) != 2 {
				t.Fatalf("filed %d tasks after the first firing closed, want 2: one late firing, then held again",
					len(filed))
			}
			skips = skipsOf(t, store, sched.ID) // newest first
			if skip := skips[len(skips)-1]; skip.Outcome != model.ScheduleSkipFired || skip.Folded < 1 {
				t.Errorf("first skip = %+v, want fired with the later held occurrences folded in", skip)
			}
			timer.failed(t)
		})
	}
}
