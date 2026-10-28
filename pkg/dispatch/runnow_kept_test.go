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

package dispatch_test

// A run-now request is spent by a run of the task, and only by one: a
// run that ended without doing the work -- it never reached its agent,
// or the deployment stopped it (model.RunNowSurvives) -- gives it back,
// and the task is started ahead of the backlog again.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
)

// startRequested puts t0, t1 and t2 on the backlog in that order, asks
// for t2 by name and runs the cycle that starts it, returning its run.
func startRequested(t *testing.T, ctx context.Context, store *model.Store) dispatch.Dispatch {
	t.Helper()
	putTasks(t, store, ctx, task("t0", true), task("t1", true), task("t2", true))
	requestRunNow(t, ctx, store, "t2", now)
	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now)
	if err != nil || len(got) != 1 || got[0].TaskID != "t2" {
		t.Fatalf("first cycle dispatched %+v, err = %v; want the requested t2", got, err)
	}
	return got[0]
}

func runNowAt(t *testing.T, ctx context.Context, store *model.Store, taskID string) *time.Time {
	t.Helper()
	obs, err := store.GetObservation(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil {
		return nil
	}
	return obs.RunNowAt
}

func keptNotes(t *testing.T, ctx context.Context, store *model.Store, taskID string) []string {
	t.Helper()
	comments, err := store.Comments(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range comments {
		if strings.HasPrefix(c.Body, "Run now kept:") {
			out = append(out, c.Body)
		}
	}
	return out
}

func TestRunNowSurvivesARunThatDidNotDoTheWork(t *testing.T) {
	for _, tc := range []struct {
		outcome, says string
	}{
		{"setup-failed", "ended setup-failed before reaching its agent"},
		{model.PausedOutcome, "paused on a usage limit"},
		{model.CapacityOutcome, "out of capacity"},
		{model.InterruptedOutcome, "interrupted"},
		{model.OOMOutcome, "OOM-killed"},
		// A park is the agent's own suspension (requested), which keeps
		// the request as every suspension does.
		{model.ParkedOutcome, "was suspended (requested)"},
	} {
		t.Run(tc.outcome, func(t *testing.T) {
			store, ctx := open(t)
			d := startRequested(t, ctx, store)
			if at := runNowAt(t, ctx, store, "t2"); at != nil {
				t.Fatalf("RunNowAt = %v while its run is live, want it spent", at)
			}
			if err := store.FinishRun(ctx, d.RunID, now.Add(time.Second), tc.outcome, "detail"); err != nil {
				t.Fatal(err)
			}
			if at := runNowAt(t, ctx, store, "t2"); at == nil || !at.Equal(now) {
				t.Fatalf("RunNowAt = %v, want the original request (%v) put back", at, now)
			}
			notes := keptNotes(t, ctx, store, "t2")
			if len(notes) != 1 || !strings.Contains(notes[0], "run "+d.RunID+" ") || !strings.Contains(notes[0], tc.says) {
				t.Fatalf("notes on t2 = %q, want one naming %s and saying it %s", notes, d.RunID, tc.says)
			}

			// Well past any backoff: t2 is started again ahead of
			// t0 and t1, which have been on the backlog all along.
			got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now.Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].TaskID != "t2" {
				t.Fatalf("dispatched %+v, want t2 again ahead of the backlog", got)
			}
			if at := runNowAt(t, ctx, store, "t2"); at != nil {
				t.Fatalf("RunNowAt = %v after the second run started, want it spent again", at)
			}
		})
	}
}

func TestRunNowIsSpentByARunOfTheTask(t *testing.T) {
	for _, outcome := range []string{"succeeded", "failed", model.NothingNeededOutcome,
		model.EnvironmentOutcome, "no_action"} {
		t.Run(outcome, func(t *testing.T) {
			store, ctx := open(t)
			d := startRequested(t, ctx, store)
			if err := store.FinishRun(ctx, d.RunID, now.Add(time.Second), outcome, ""); err != nil {
				t.Fatal(err)
			}
			if at := runNowAt(t, ctx, store, "t2"); at != nil {
				t.Fatalf("RunNowAt = %v, want the request spent by a run ending %s", at, outcome)
			}
			if notes := keptNotes(t, ctx, store, "t2"); len(notes) != 0 {
				t.Fatalf("notes = %q, want none", notes)
			}
			got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now.Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].TaskID != "t0" {
				t.Fatalf("dispatched %+v, want t0: the backlog's own order once the request is spent", got)
			}
		})
	}
}

// A success the finish path later re-records -- the shape ProcessResult
// gives no_action and nothing_needed -- never had a request to give back.
// And one re-recorded the other way, as a refusal burst re-records its
// earlier runs as paused, gives it back as long as nothing has started
// since.
func TestRunNowFollowsARecordedOutcome(t *testing.T) {
	store, ctx := open(t)
	d := startRequested(t, ctx, store)
	if err := store.FinishRun(ctx, d.RunID, now.Add(time.Second), "failed", "refused"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRunOutcome(ctx, d.RunID, model.PausedOutcome, "paused by a refusal burst"); err != nil {
		t.Fatal(err)
	}
	if at := runNowAt(t, ctx, store, "t2"); at == nil || !at.Equal(now) {
		t.Fatalf("RunNowAt = %v, want it put back once the run reads paused", at)
	}
	if err := store.SetRunOutcome(ctx, d.RunID, "failed", "refused after all"); err != nil {
		t.Fatal(err)
	}
	if at := runNowAt(t, ctx, store, "t2"); at != nil {
		t.Fatalf("RunNowAt = %v, want it withdrawn once the run reads failed again", at)
	}
}

func TestRunNowIsNotKeptForAClosedTask(t *testing.T) {
	for _, tc := range []struct{ outcome, detail string }{
		// A person closing the task cancels its run.
		{"cancelled", model.TaskClosedDetail},
		// And a closure that lands while the run is ending some
		// other way is still a closure.
		{model.InterruptedOutcome, model.InterruptedDetail},
	} {
		t.Run(tc.outcome, func(t *testing.T) {
			store, ctx := open(t)
			d := startRequested(t, ctx, store)
			closed := now.Add(time.Second)
			if err := store.ObserveField(ctx, "t2", closed, func(o *model.Observation) { o.ClosedAt = &closed }); err != nil {
				t.Fatal(err)
			}
			if err := store.FinishRun(ctx, d.RunID, closed, tc.outcome, tc.detail); err != nil {
				t.Fatal(err)
			}
			if at := runNowAt(t, ctx, store, "t2"); at != nil {
				t.Fatalf("RunNowAt = %v, want no request on a closed task", at)
			}
			if notes := keptNotes(t, ctx, store, "t2"); len(notes) != 0 {
				t.Fatalf("notes = %q, want none", notes)
			}
		})
	}
}

// A request somebody makes again while the run is live is theirs, and a
// run that ends without doing the work does not overwrite it.
func TestRunNowKeepsARequestMadeSince(t *testing.T) {
	store, ctx := open(t)
	d := startRequested(t, ctx, store)
	again := now.Add(30 * time.Second)
	requestRunNow(t, ctx, store, "t2", again)
	if err := store.FinishRun(ctx, d.RunID, now.Add(time.Minute), "setup-failed", ""); err != nil {
		t.Fatal(err)
	}
	if at := runNowAt(t, ctx, store, "t2"); at == nil || !at.Equal(again) {
		t.Fatalf("RunNowAt = %v, want the newer request (%v) left standing", at, again)
	}
}

// Order among requests is the order they were made in, and a request
// given back keeps its original place in it.
func TestRunNowKeptRequestKeepsItsPlace(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("t0", true), task("t1", true), task("t2", true))
	requestRunNow(t, ctx, store, "t1", now)
	requestRunNow(t, ctx, store, "t2", now.Add(10*time.Second))
	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now.Add(20*time.Second))
	if err != nil || len(got) != 1 || got[0].TaskID != "t1" {
		t.Fatalf("first cycle dispatched %+v, err = %v; want t1, asked for first", got, err)
	}
	if err := store.FinishRun(ctx, got[0].RunID, now.Add(21*time.Second), model.InterruptedOutcome, ""); err != nil {
		t.Fatal(err)
	}
	got, err = dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now.Add(22*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != "t1" {
		t.Fatalf("dispatched %+v, want t1 again ahead of t2's later request", got)
	}
}

// The restart loop startRun's own doc comment warns of: a sandbox that
// cannot be built, with a request that keeps being given back. The
// request waits out the retry backoff each time -- and keeps its place
// in front of the backlog while it does -- and stops being given back
// once the setup failures reach the cap task_state retires a task at.
func TestRunNowKeptRequestRespectsBackoff(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("t0", true), task("t2", true))
	requestRunNow(t, ctx, store, "t2", now)
	limits := model.Limits{Workers: 2}

	clock := now
	runs := 0
	for {
		got, err := dispatch.Cycle(ctx, store, limits, clock)
		if err != nil {
			t.Fatal(err)
		}
		var run string
		for _, d := range got {
			if d.TaskID == "t2" {
				run = d.RunID
			}
		}
		if run == "" {
			t.Fatalf("cycle at %v dispatched %+v, want t2 (run-now, past its backoff)", clock, got)
		}
		runs++
		clock = clock.Add(time.Second)
		if err := store.FinishRun(ctx, run, clock, "setup-failed", "dial tcp: i/o timeout"); err != nil {
			t.Fatal(err)
		}
		if runNowAt(t, ctx, store, "t2") == nil {
			break
		}
		if runs > model.MaxConsecutiveFailures {
			t.Fatalf("request still kept after %d setup failures", runs)
		}
		// The very next poll is inside the backoff: t2 is not started.
		got, err = dispatch.Cycle(ctx, store, limits, clock.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range got {
			if d.TaskID == "t2" {
				t.Fatalf("t2 restarted %v after setup failure %d, inside its retry backoff", time.Second, runs)
			}
		}
		clock = clock.Add(time.Hour)
	}
	if runs != model.MaxConsecutiveFailures {
		t.Fatalf("request given back after %d setup failures, want %d", runs-1, model.MaxConsecutiveFailures-1)
	}
}

// A person pressing Run now again after a failure is asking again, and is
// not made to wait out the backoff the failure earned.
func TestRunNowFreshRequestSkipsBackoff(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("t0", true), task("t2", true))
	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now)
	if err != nil || len(got) != 1 || got[0].TaskID != "t0" {
		t.Fatalf("first cycle dispatched %+v, err = %v", got, err)
	}
	if err := store.FinishRun(ctx, got[0].RunID, now.Add(time.Second), "setup-failed", ""); err != nil {
		t.Fatal(err)
	}
	requestRunNow(t, ctx, store, "t0", now.Add(2*time.Second))
	got, err = dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != "t0" {
		t.Fatalf("dispatched %+v, want t0 at once on a fresh request", got)
	}
}
