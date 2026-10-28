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
