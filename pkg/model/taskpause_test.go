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
	"errors"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// StartRun is the one place every dispatch path records its run, so it is
// where a pause has to hold whatever asked: the ready list, a run-now
// request, a schedule's wake or a console session. A paused task's run is
// refused, and none of the reads dispatch works from offers it; resuming
// it gives all of that back.
func TestAPausedTaskCannotStartARun(t *testing.T) {
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
	if paused, err := store.Pause(ctx, model.TaskPause{TaskID: "t", PausedAt: now, PausedBy: human,
		FromState: model.StateQueued}); err != nil || !paused {
		t.Fatalf("Pause = %v, %v; want true, nil", paused, err)
	}

	err := store.StartRun(ctx, model.Run{ID: "t-1", TaskID: "t", Attempt: 1, StartedAt: now}, model.Limits{})
	if !errors.Is(err, model.ErrTaskPaused) {
		t.Fatalf("StartRun of a paused task = %v, want ErrTaskPaused", err)
	}
	if runs, err := store.Runs(ctx, "t"); err != nil || len(runs) != 0 {
		t.Fatalf("runs of a paused task = %v, %v; want none recorded", runs, err)
	}
	if ready, err := store.Ready(ctx); err != nil || len(ready) != 0 {
		t.Errorf("Ready while paused = %v, %v; want the task left out", ready, err)
	}
	if reqs, err := store.RunNow(ctx); err != nil || len(reqs) != 0 {
		t.Errorf("RunNow while paused = %v, %v; want the request held back", reqs, err)
	}

	if lifted, err := store.Resume(ctx, "t", now); err != nil || lifted == nil {
		t.Fatalf("Resume = %v, %v; want the pause lifted", lifted, err)
	}
	if reqs, err := store.RunNow(ctx); err != nil || len(reqs) != 1 {
		t.Errorf("RunNow after resume = %v, %v; want the request back", reqs, err)
	}
	if err := store.StartRun(ctx, model.Run{ID: "t-1", TaskID: "t", Attempt: 1, StartedAt: now}, model.Limits{}); err != nil {
		t.Fatalf("StartRun after resume: %v", err)
	}
}
