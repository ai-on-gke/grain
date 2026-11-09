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

package model_test

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestRunNowSurvivesOnlyTheDeploymentsEndings(t *testing.T) {
	for _, outcome := range []string{"setup-failed", model.PausedOutcome, model.CapacityOutcome,
		model.InterruptedOutcome, model.OOMOutcome} {
		if _, ok := model.RunNowSurvives(outcome); !ok {
			t.Errorf("RunNowSurvives(%q) = false, want the request kept", outcome)
		}
	}
	for _, outcome := range []string{"succeeded", "failed", "cancelled", "no_action", "finish-failed",
		model.NothingNeededOutcome, model.ParkedOutcome, model.EnvironmentOutcome, ""} {
		if _, ok := model.RunNowSurvives(outcome); ok {
			t.Errorf("RunNowSurvives(%q) = true, want the request spent", outcome)
		}
	}
}

// The request a run gives back is the one it spent, timestamp and all,
// written in the transaction that records the run's outcome; and an
// outcome re-recorded on a run that is no longer its task's latest gives
// nothing back, since the request is the later run's to settle.
func TestFinishRunPutsBackTheRequestItsRunSpent(t *testing.T) {
	store, ctx := open(t)
	task := model.Task{
		ID: "t", Intent: model.IntentImplement, Title: "t",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Binding:  model.BindingDirective,
		Approval: &model.Attribution{Actor: human},
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	asked := now.Add(-time.Hour)
	if err := store.ObserveField(ctx, "t", asked, func(o *model.Observation) { o.RunNowAt = &asked }); err != nil {
		t.Fatal(err)
	}
	start := func(attempt int, at time.Time) string {
		t.Helper()
		id := "t-" + string(rune('0'+attempt))
		if err := store.StartRun(ctx, model.Run{ID: id, TaskID: "t", Attempt: attempt, StartedAt: at}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	requests := func() []model.RunNowRequest {
		t.Helper()
		reqs, err := store.RunNow(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return reqs
	}

	first := start(1, now)
	if reqs := requests(); len(reqs) != 0 {
		t.Fatalf("RunNow = %+v while the run is live, want none", reqs)
	}
	if err := store.FinishRun(ctx, first, now.Add(time.Second), "setup-failed", ""); err != nil {
		t.Fatal(err)
	}
	reqs := requests()
	if len(reqs) != 1 || reqs[0].TaskID != "t" || !reqs[0].At.Equal(asked) {
		t.Fatalf("RunNow = %+v, want t's request put back at %v", reqs, asked)
	}

	second := start(2, now.Add(time.Minute))
	if err := store.FinishRun(ctx, second, now.Add(2*time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRunOutcome(ctx, first, model.PausedOutcome, "re-recorded"); err != nil {
		t.Fatal(err)
	}
	if reqs := requests(); len(reqs) != 0 {
		t.Fatalf("RunNow = %+v, want the request spent by the successful second run and not revived by the first", reqs)
	}

	// A run nobody asked for gives nothing back, however it ends.
	third := start(3, now.Add(3*time.Minute))
	if err := store.FinishRun(ctx, third, now.Add(4*time.Minute), model.InterruptedOutcome, ""); err != nil {
		t.Fatal(err)
	}
	if reqs := requests(); len(reqs) != 0 {
		t.Fatalf("RunNow = %+v, want none: the interrupted run spent no request", reqs)
	}
}

// A request is kept only by the suspensions that stopped the run before it
// could do the work; the agent's own resume_later, and the other kinds in
// which the agent ran its turn, spent it (task 3557).
func TestSuspensionKindKeepsRunNowOnlyForInterruptions(t *testing.T) {
	for _, kind := range []model.SuspensionKind{
		model.SuspendSetup, model.SuspendRestart, model.SuspendOperatorPause,
		model.SuspendOOM, model.SuspendUsageLimit, model.SuspendCapacity,
		model.SuspendModel, model.SuspendCapability,
	} {
		if _, ok := kind.KeepsRunNow(); !ok {
			t.Errorf("%q.KeepsRunNow() = false, want the request kept", kind)
		}
	}
	for _, kind := range []model.SuspensionKind{
		model.SuspendRequested, model.SuspendDeadline, model.SuspendIdle, model.SuspendContext,
	} {
		if _, ok := kind.KeepsRunNow(); ok {
			t.Errorf("%q.KeepsRunNow() = true, want the request spent", kind)
		}
	}
	if got, want := len(model.SuspensionKinds()), 12; got != want {
		t.Errorf("%d suspension kinds, this test classifies %d: add the new one above", got, want)
	}
}

// The stamps older builds left standing after a run spent them -- a parked
// run's, and one on a closed task -- are withdrawn when the store is next
// opened. A request a run genuinely gave back, and one made after the
// last run, stand.
func TestInitClearsSpentRunNowStamps(t *testing.T) {
	store, ctx := open(t)
	for _, id := range []string{"parked", "closed", "kept", "fresh"} {
		if err := store.PutTask(ctx, model.Task{
			ID: id, Intent: model.IntentImplement, Title: id,
			Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
			Binding:  model.BindingDirective,
			Approval: &model.Attribution{Actor: human},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ id, outcome string }{
		{"parked", model.ParkedOutcome}, {"kept", "setup-failed"}, {"fresh", model.ParkedOutcome},
	} {
		if err := store.StartRun(ctx, model.Run{ID: tc.id + "-1", TaskID: tc.id, Attempt: 1, StartedAt: now}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, tc.id+"-1", now.Add(time.Minute), tc.outcome, ""); err != nil {
			t.Fatal(err)
		}
	}
	asked := now.Add(-time.Hour)
	later := now.Add(10 * time.Minute)
	closedAt := now.Add(2 * time.Minute)
	stamp := func(id string, at time.Time, closed *time.Time) {
		t.Helper()
		if err := store.ObserveField(ctx, id, at, func(o *model.Observation) {
			o.RunNowAt = &at
			if closed != nil {
				o.ClosedAt = closed
			}
		}); err != nil {
			t.Fatal(err)
		}
	}
	stamp("parked", asked, nil)
	stamp("kept", asked, nil)
	stamp("fresh", later, nil)
	stamp("closed", asked, &closedAt)

	if err := store.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	for id, want := range map[string]*time.Time{"parked": nil, "closed": nil, "kept": &asked, "fresh": &later} {
		obs, err := store.GetObservation(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		var got *time.Time
		if obs != nil {
			got = obs.RunNowAt
		}
		if (got == nil) != (want == nil) || got != nil && !got.Equal(*want) {
			t.Errorf("%s: RunNowAt = %v after Init, want %v", id, got, want)
		}
	}
}
