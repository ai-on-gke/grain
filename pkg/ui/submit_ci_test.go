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

package ui_test

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// Pressing Submit on a task whose grain CI run concluded without passing
// -- an operator cancelled a hung or duplicate run, or the run errored --
// drops that verdict, so the merge queue starts a fresh required run on
// its next cycle instead of waiting on one nothing will replace (task
// 285). A pass, and a run still going, are kept.
func TestSubmitClearsAGrainCIVerdictThatDidNotPass(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		conclusion string
		cleared    bool
	}{
		{"cancelled", true},
		{"errored", true},
		{"failure", true},
		{"success", false},
		{"", false},
	} {
		// "" would name the subtest #00, and t.TempDir's path with it: the
		// sqlite DSN reads # as a fragment, putting the store outside the
		// temp dir, where it outlives the test.
		name := c.conclusion
		if name == "" {
			name = "none"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client, store, ctx := testClient(t)
			task := create(t, client, ctx)
			if err := store.ObserveField(ctx, task.ID, time.Now(), func(o *model.Observation) {
				o.CI = &model.CIStatus{RunID: "run-1", Commit: "abc", Conclusion: c.conclusion,
					CancelledBy: "operator"}
			}); err != nil {
				t.Fatal(err)
			}
			if err := client.Submit(ctx, task.ID); err != nil {
				t.Fatal(err)
			}
			obs, err := store.GetObservation(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got := obs.CI == nil; got != c.cleared {
				t.Fatalf("CI = %+v after Submit, want cleared=%v", obs.CI, c.cleared)
			}
		})
	}
}

// The same holds for a Submit that releases a change held for approvers:
// once the last approval lands, the cancelled verdict goes too.
func TestSubmitWithApprovalsClearsACancelledGrainCIVerdict(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
		tk.AutoMerge = false
		tk.Links = append(tk.Links, model.ApprovalRequiredLink("trigger:payments", []string{"alice"}))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(ctx, task.ID, time.Now(), func(o *model.Observation) {
		o.CI = &model.CIStatus{RunID: "run-1", Commit: "abc", Conclusion: "cancelled", CancelledBy: "operator"}
	}); err != nil {
		t.Fatal(err)
	}
	person := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "alice"})
	if err := client.Submit(person, task.ID); err != nil {
		t.Fatal(err)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.CI != nil {
		t.Fatalf("CI = %+v after a releasing Submit, want it cleared", obs.CI)
	}
}
