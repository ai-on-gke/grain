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

package sqlite_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// TestARunAPersonStoppedIsLeftOutOfTheStreak is model.StoppedByPerson's
// rule, read through both walks: a run cancelled because its task was
// closed under it, or because somebody stopped its turn, is a decision
// about the task and no evidence that it fails. It neither adds to the
// streak nor clears the failures before it, and the failure the UI
// names is the last one counted, not the cancellation after it. The
// "real failure first" history is the one that matters for the
// regression on 2026-10-30: closing and reopening a failing task must not
// wash its failure away either.
func TestARunAPersonStoppedIsLeftOutOfTheStreak(t *testing.T) {
	t.Parallel()
	for _, detail := range []string{model.TaskClosedDetail, model.TurnStoppedDetail} {
		for _, priorFailures := range []int{0, 1} {
			t.Run(fmt.Sprintf("%s/after %d failures", detail, priorFailures), func(t *testing.T) {
				t.Parallel()
				store, db, ctx := openStore(t)
				if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
					t.Fatal(err)
				}
				n := 0
				finish := func(outcome, detail string) {
					t.Helper()
					n++
					id := fmt.Sprintf("r%d", n)
					at := now.Add(time.Duration(n) * time.Hour)
					if err := store.StartRun(ctx, model.Run{
						ID: id, TaskID: "a1b2", Sandbox: "s1", Attempt: n, StartedAt: at,
					}, model.Limits{}); err != nil {
						t.Fatal(err)
					}
					if err := store.FinishRun(ctx, id, at.Add(time.Minute), outcome, detail); err != nil {
						t.Fatal(err)
					}
				}
				for i := 0; i < priorFailures; i++ {
					finish("failed", "the agent gave up")
				}
				// More than the cap, so a wall that counted would show
				// as a failed task and not merely a larger number.
				for i := 0; i < model.MaxConsecutiveFailures+1; i++ {
					finish("cancelled", detail)
				}

				if got := taskStreakView(t, ctx, db, "a1b2"); got != priorFailures {
					t.Errorf("task_streak = %d, want %d", got, priorFailures)
				}
				streak, err := store.FailureStreak(ctx, "a1b2")
				if err != nil {
					t.Fatal(err)
				}
				if streak == nil || streak.Count != priorFailures || streak.BackoffCount != priorFailures {
					t.Fatalf("FailureStreak = %+v, want Count and BackoffCount %d", streak, priorFailures)
				}
				if priorFailures > 0 && streak.LastFailureDetail != "the agent gave up" {
					t.Errorf("LastFailureDetail = %q, want the failure's own detail, not the cancellation's",
						streak.LastFailureDetail)
				}
				if state, err := store.State(ctx, "a1b2"); err != nil {
					t.Fatal(err)
				} else if state != model.StateQueued {
					t.Errorf("state = %q, want %q", state, model.StateQueued)
				}
			})
		}
	}
}
