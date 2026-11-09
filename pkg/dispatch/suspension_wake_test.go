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

// A resume_later that names a time and another condition is woken by
// whichever comes first: once the condition releases the task's deferral,
// the time it named holds nothing (model.Store.SuspensionResumeAt).

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
)

// resumeLaterAtOrMessage starts id's run and ends it the way a
// resume_later with when {at: now+19h, message: true} does: the run
// defers its own task with that wake time (ui.Client.ResumeLater), then
// is recorded as a requested suspension naming the same time
// (orchestrator.requestedSuspension).
func resumeLaterAtOrMessage(t *testing.T, ctx context.Context, store *model.Store, id string) time.Time {
	t.Helper()
	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now)
	if err != nil || len(got) != 1 || got[0].TaskID != id {
		t.Fatalf("starting %s: %+v, %v", id, got, err)
	}
	at := now.Add(19 * time.Hour)
	called := now.Add(time.Minute)
	if err := store.SetDeferred(ctx, id, &called, &at, called); err != nil {
		t.Fatal(err)
	}
	when := model.ResumeOn{At: &at, Message: true}
	if err := store.Suspend(ctx, model.Suspension{RunID: got[0].RunID, Kind: model.SuspendRequested,
		Detail: model.RequestedDetail("waiting on the operator", when), ResumeOn: when.JSON(),
		ResumeAt: &at, CreatedAt: now.Add(2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	return at
}

func TestAMessageWakesAResumeLaterBeforeTheTimeItNamed(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("w", true))
	resumeLaterAtOrMessage(t, ctx, store, "w")

	if got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now.Add(time.Hour)); err != nil || len(got) != 0 {
		t.Fatalf("before anybody posted dispatched %+v, %v; want it still deferred", got, err)
	}
	woke, err := store.WakeOnMessage(ctx, "w", now.Add(12*time.Hour))
	if err != nil || !woke {
		t.Fatalf("WakeOnMessage = %v, %v; want the post to release it", woke, err)
	}
	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now.Add(12*time.Hour))
	if err != nil || len(got) != 1 || got[0].TaskID != "w" {
		t.Fatalf("the cycle after the post dispatched %+v, %v; want w, not held to its resume time", got, err)
	}
}

func TestUndeferWakesAResumeLaterBeforeTheTimeItNamed(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("w", true))
	resumeLaterAtOrMessage(t, ctx, store, "w")

	// Undefer (ui.Client.Undefer), a CI verdict and a task closing all
	// release the deferral the same way.
	if err := store.SetDeferred(ctx, "w", nil, nil, now.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now.Add(3*time.Hour))
	if err != nil || len(got) != 1 || got[0].TaskID != "w" {
		t.Fatalf("the cycle after Undefer dispatched %+v, %v; want w, not held to its resume time", got, err)
	}
	susps, err := store.Suspensions(ctx, "w")
	if err != nil || len(susps) != 1 || susps[0].ResumedAt == nil || susps[0].ResumedRunID != got[0].RunID {
		t.Fatalf("Suspensions = %+v, %v; want the suspension resumed by the run Undefer let start", susps, err)
	}
}

func TestASetupSuspensionHoldIsRecordedAsItsOwnWait(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, durableTask("d"))
	got, err := dispatch.Cycle(ctx, store, model.Limits{Durable: 4}, now)
	if err != nil || len(got) != 1 {
		t.Fatalf("starting the durable task: %+v, %v", got, err)
	}
	resume := now.Add(dispatch.DurableSetupBackoff)
	if err := store.Suspend(ctx, model.Suspension{RunID: got[0].RunID, Kind: model.SuspendSetup,
		Detail: "sandbox could not be prepared", ResumeAt: &resume, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if got, err := dispatch.Cycle(ctx, store, model.Limits{Durable: 4}, now.Add(time.Minute)); err != nil || len(got) != 0 {
		t.Fatalf("a minute after a setup suspension dispatched %+v, %v; want it held", got, err)
	}
	wait, err := store.GrantWaitOf(ctx, "d")
	if err != nil || wait == nil || wait.Capability != model.SuspensionWaitCapability ||
		!strings.Contains(wait.Resource, "held until "+resume.Format(time.RFC3339)+" by its suspension") {
		t.Fatalf("d's wait = %+v, %v; want it held by its suspension until %s", wait, err, resume.Format(time.RFC3339))
	}

	// A Run now made since the run that suspended is a person asking
	// again, and is started at once (dispatch.runNowEligible).
	requestRunNow(t, ctx, store, "d", now.Add(90*time.Second))
	got, err = dispatch.Cycle(ctx, store, model.Limits{Durable: 4}, now.Add(90*time.Second))
	if err != nil || len(got) != 1 || got[0].TaskID != "d" {
		t.Fatalf("a Run now made after the suspension dispatched %+v, %v; want d at once", got, err)
	}
	if wait, err := store.GrantWaitOf(ctx, "d"); err != nil || wait != nil {
		t.Fatalf("d's wait once started = %+v, %v; want none", wait, err)
	}
}
