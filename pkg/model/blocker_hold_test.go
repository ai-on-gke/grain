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
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// TestBlockerHoldOfIsThePersonsStates pins which blockers a dependent
// waits on a person for: the states that never close by themselves, and
// not the ones grain is driving.
func TestBlockerHoldOfIsThePersonsStates(t *testing.T) {
	wake := now.Add(time.Hour)
	for _, c := range []struct {
		name                     string
		task                     model.Task
		state                    model.State
		paused, goal, resumeHeld bool
		want                     string
	}{
		{name: "failed", state: model.StateFailed, want: "failed"},
		{name: "proposed", state: model.StateProposed, want: "Approve"},
		{name: "asking", state: model.StateAwaitingReply, want: "Reply"},
		{name: "unsubmitted", state: model.StateAwaitingSubmit, want: "Submit"},
		{name: "parked", state: model.StateDeferred, want: "Undefer"},
		{name: "paused", state: model.StateQueued, paused: true, want: "Resume"},
		{name: "queued", state: model.StateQueued},
		{name: "running", state: model.StateRunning},
		{name: "completed", state: model.StateCompleted},
		{name: "closed", state: model.StateClosed},
		{name: "wake time", task: model.Task{DeferredUntil: &wake}, state: model.StateDeferred},
		{name: "goal", state: model.StateDeferred, goal: true},
		{name: "resume_later", state: model.StateDeferred, resumeHeld: true},
	} {
		c.task.ID = "9"
		hold, ok := model.BlockerHoldOf(c.task, c.state, c.paused, c.goal, c.resumeHeld)
		if ok != (c.want != "") {
			t.Errorf("%s: BlockerHoldOf(%s) held = %v, want %v", c.name, c.state, ok, c.want != "")
			continue
		}
		if ok && (hold.ID != "9" || hold.State != c.state || !strings.Contains(hold.Why, c.want)) {
			t.Errorf("%s: BlockerHoldOf(%s) = %+v, want task 9 in %s with %q in its words", c.name, c.state, hold, c.state, c.want)
		}
	}
}

// TestBlockerHoldsReadsTheStore is task 4856 against a real store: a
// task that failed MaxConsecutiveFailures runs in a row, one never
// approved and one deferred indefinitely hold their dependents for a
// person; one deferred until a time, one a resume_later still holds and
// one queued do not.
func TestBlockerHoldsReadsTheStore(t *testing.T) {
	store, ctx := open(t)
	put := func(id string, approved bool) {
		t.Helper()
		task := model.Task{
			ID: id, Intent: model.IntentImplement, Title: "t",
			Origin:  model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
			Binding: model.BindingDirective,
		}
		if approved {
			task.Approval = &model.Attribution{Actor: human}
		}
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	put("failed", true)
	for i := 1; i <= model.MaxConsecutiveFailures; i++ {
		run := fmt.Sprintf("failed-%d", i)
		at := now.Add(time.Duration(i) * time.Minute)
		if err := store.StartRun(ctx, model.Run{ID: run, TaskID: "failed", Attempt: i, StartedAt: at}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, run, at.Add(time.Second), "failed", "boom"); err != nil {
			t.Fatal(err)
		}
	}
	put("proposed", false)
	put("parked", true)
	if err := store.SetDeferred(ctx, "parked", &now, nil, now); err != nil {
		t.Fatal(err)
	}
	put("waking", true)
	wake := now.Add(time.Hour)
	if err := store.SetDeferred(ctx, "waking", &now, &wake, now); err != nil {
		t.Fatal(err)
	}
	stoppedOnMessage(t, store, "resuming")
	if err := store.FinishRun(ctx, "resuming-1", now.Add(2*time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	put("queued", true)

	holds, err := store.BlockerHolds(ctx, []string{"failed", "proposed", "parked", "waking", "resuming", "queued", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	for id, state := range map[string]model.State{
		"failed": model.StateFailed, "proposed": model.StateProposed, "parked": model.StateDeferred,
	} {
		if h, ok := holds[id]; !ok || h.State != state {
			t.Errorf("BlockerHolds[%s] = %+v, %v; want a hold in %s", id, h, ok, state)
		}
	}
	for _, id := range []string{"waking", "resuming", "queued", "missing"} {
		if h, ok := holds[id]; ok {
			t.Errorf("BlockerHolds[%s] = %+v; want no hold, grain is still driving it", id, h)
		}
	}
	held, err := store.ResumeHeldTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !held["resuming"] || held["parked"] {
		t.Errorf("ResumeHeldTasks = %v, want resuming and not parked", held)
	}
	if got := model.HoldsOf([]string{"queued", "proposed", "failed"}, holds); len(got) != 2 ||
		got[0].ID != "proposed" || got[1].ID != "failed" {
		t.Errorf("HoldsOf = %+v, want proposed then failed", got)
	}
}
