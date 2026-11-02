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

// One of grain's own closes is written with who made it and why, at the
// stamp itself, so the timeline's closed step is attributed rather than
// left bare; a call that moves nothing -- the task already closed at that
// moment, or a guard that leaves it open -- records nothing.
func TestObserveCloseRecordsTheCloseItStamps(t *testing.T) {
	store, ctx := open(t)
	task := queuedTask(t, store, ctx, "80", &widgets)

	if err := store.ObserveClose(ctx, task.ID, now, model.GrainPrincipal, "goal met",
		func(o *model.Observation) {}); err != nil {
		t.Fatal(err)
	}
	if events, err := store.TaskEvents(ctx, task.ID); err != nil || len(events) != 0 {
		t.Fatalf("events after a close that stamped nothing = %+v (%v), want none", events, err)
	}

	stamp := func(o *model.Observation) {
		if o.ClosedAt == nil {
			o.ClosedAt = &now
		}
	}
	if err := store.ObserveClose(ctx, task.ID, now, model.GrainPrincipal, "goal met", stamp); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Minute)
	if err := store.ObserveClose(ctx, task.ID, later, model.GrainPrincipal, "goal met", stamp); err != nil {
		t.Fatal(err)
	}

	state, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateClosed {
		t.Errorf("state = %s, want closed", state)
	}
	events, err := store.TaskEvents(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v, want the one close", events)
	}
	e := events[0]
	if e.Kind != model.TaskEventClosed || e.By != model.GrainPrincipal || e.Reason != "goal met" ||
		!e.At.Equal(now) {
		t.Errorf("event = %+v, want grain's close at %s for the goal", e, now)
	}
}
