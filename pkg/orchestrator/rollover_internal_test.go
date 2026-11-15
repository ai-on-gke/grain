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

package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A durable task open for more than a year rolls itself over to a
// successor, and a running one waits until its run has ended.
func TestReconcileRolloverRollsOverADurableTaskPastItsAge(t *testing.T) {
	store, ctx := openTestStore(t)
	filed := standingNow.Add(-model.RolloverAgeLimit - time.Hour)
	tk := durableTask("8001")
	tk.CreatedAt = &filed
	young := durableTask("8002")
	young.CreatedAt = &standingNow
	running := durableTask("8003")
	running.CreatedAt = &filed
	for _, task := range []model.Task{tk, young, running} {
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.StartRun(ctx, model.Run{ID: "8003-1", TaskID: running.ID, Attempt: 1, StartedAt: standingNow},
		model.Limits{}); err != nil {
		t.Fatal(err)
	}

	if err := reconcileRollover(ctx, Deps{Store: store}, standingNow); err != nil {
		t.Fatal(err)
	}

	closed, err := store.GetTask(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	succ := model.RolledOverTo(*closed)
	if succ == "" {
		t.Fatalf("task %s past its age was not rolled over: links %+v", tk.ID, closed.Links)
	}
	if state, _ := store.State(ctx, tk.ID); state != model.StateClosed {
		t.Errorf("predecessor state = %s, want closed", state)
	}
	comments, err := store.Comments(ctx, tk.ID)
	if err != nil || len(comments) == 0 {
		t.Fatalf("predecessor comments = %v, %v", comments, err)
	}
	if last := comments[len(comments)-1]; !strings.Contains(last.Body, "Rolled over to [task "+succ+"]") ||
		!strings.Contains(last.Body, "been open since") {
		t.Errorf("predecessor's closing comment = %q", last.Body)
	}
	successor, err := store.GetTask(ctx, succ)
	if err != nil || successor == nil || !successor.IsDurable() {
		t.Fatalf("successor = %+v, %v; want a durable task", successor, err)
	}
	for _, id := range []string{young.ID, running.ID} {
		task, _ := store.GetTask(ctx, id)
		if model.RolledOverTo(*task) != "" {
			t.Errorf("task %s was rolled over, want it left alone", id)
		}
	}

	// Run again: the successor is new, so nothing more rolls over.
	if err := reconcileRollover(ctx, Deps{Store: store}, standingNow); err != nil {
		t.Fatal(err)
	}
	if s, _ := store.GetTask(ctx, succ); model.RolledOverTo(*s) != "" {
		t.Errorf("the successor was rolled over again")
	}
}
