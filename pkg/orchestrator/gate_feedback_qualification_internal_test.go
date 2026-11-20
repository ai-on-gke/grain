// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package orchestrator

import (
	"reflect"
	"testing"
	"time"
)

// Qualify the Task 58 writer -> shadow reader -> real dispatch seam. Reads
// happen before and after each legacy event, so an extra dispatch or consumed
// message cannot be hidden by testing the two mechanisms independently.
func TestGateShadowPRFeedbackDispatchNoninterference(t *testing.T) {
	for _, hold := range []string{"ci", "question", "secret"} {
		t.Run(hold, func(t *testing.T) {
			f := newFeedbackFixture(t)
			ctx := t.Context()
			if hold == "ci" {
				f.ciWait(true)
			} else {
				f.question(hold == "secret")
			}
			observe := func() {
				t.Helper()
				before, err := f.store.GetObservation(ctx, "feedback")
				if err != nil {
					t.Fatal(err)
				}
				task, err := f.store.GetTask(ctx, "feedback")
				if err != nil {
					t.Fatal(err)
				}
				wake, commits := f.store.Changed(), f.store.CommitCount()
				view, _, err := f.store.ObserveTaskGates(ctx, "feedback", f.now.Add(20*time.Minute))
				if err != nil {
					t.Fatal(err)
				}
				after, err := f.store.GetObservation(ctx, "feedback")
				if err != nil {
					t.Fatal(err)
				}
				afterTask, err := f.store.GetTask(ctx, "feedback")
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(task, afterTask) || commits != f.store.CommitCount() {
					t.Fatal("shadow consumed feedback or changed a legacy hold")
				}
				if view.Mode != "observation_only" || view.Authority != "legacy" || view.ActionEffects || view.LifecycleEffects || view.DispatchEffects || view.AutomaticCutover {
					t.Fatal("shadow gained authority")
				}
				for _, b := range view.Bindings {
					if b.Required || b.Action != nil {
						t.Fatal("operational binding")
					}
				}
				for _, r := range view.Results {
					if len(r.Result.Work) > 0 || len(r.Result.Actions) > 0 {
						t.Fatal("operational prediction")
					}
				}
				select {
				case <-wake:
					t.Fatal("shadow woke dispatch")
				default:
				}
			}
			observe()
			f.cycle(0)
			f.event("issue_comment", "created", 701, "alice")
			observe()
			if hold != "ci" {
				if hold == "question" {
					d := f.cycle(1)[0]
					cs := f.comments()
					f.finish(d, cs[len(cs)-1].ID)
					observe()
				}
				f.cycle(0)
				f.event("issue_comment", "created", 701, "alice")
				observe()
				f.cycle(0)
				return
			}
			first := f.cycle(1)[0]
			observe()
			f.cycle(0)
			// Another comment while the first run is live stays unseen, even though
			// the observations and duplicate webhook have both been processed.
			f.event("issue_comment", "created", 702, "alice")
			f.event("issue_comment", "created", 702, "alice")
			observe()
			f.cycle(0)
			comments := f.comments()
			if len(comments) != 2 {
				t.Fatalf("feedback deduplication: %+v", comments)
			}
			f.finish(first, comments[0].ID)
			observe()
			second := f.cycle(1)[0]
			observe()
			f.cycle(0)
			f.finish(second, comments[1].ID)
			observe()
			f.cycle(0)
			holds, err := f.store.HeldCIResumes(ctx)
			if err != nil || len(holds) != 1 || holds[0].Head != "still-pending-head" {
				t.Fatalf("real CI wait changed: %+v %v", holds, err)
			}
		})
	}
}
