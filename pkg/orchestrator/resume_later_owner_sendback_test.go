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

package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// restOnResumeLaterCI is a run of a task with an open pull request that
// was sent back (CompletedAt cleared, as every send-back does), pushed, and
// called resume_later(ci: my-branch): ui.Client.ResumeLater's deferral,
// held condition and comment, then the run's end.
func restOnResumeLaterCI(t *testing.T, ctx context.Context, store *model.Store, taskID string, at time.Time) {
	t.Helper()
	if err := store.ObserveField(ctx, taskID, at, func(o *model.Observation) { o.CompletedAt = nil }); err != nil {
		t.Fatal(err)
	}
	runID := taskID + "-resume-later"
	if err := store.StartRun(ctx, model.Run{ID: runID, TaskID: taskID, Sandbox: "s", Attempt: 2,
		StartedAt: at.Add(-time.Minute)}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetDeferred(ctx, taskID, &at, nil, at); err != nil {
		t.Fatal(err)
	}
	when := model.ResumeOn{CI: model.ResumeOnMyBranch}
	if err := store.HoldResumeOn(ctx, taskID, when, at); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddComment(ctx, model.Comment{TaskID: taskID,
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body: "grain: this task's run stopped with resume_later. It goes back in the queue once " +
			when.Describe() + "; Undefer releases it sooner.",
		CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, runID, at.Add(10*time.Second), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if s := stateOf(t, ctx, store, taskID); s != model.StateDeferred {
		t.Fatalf("after resume_later the task is %q, want %q", s, model.StateDeferred)
	}
}

// docs/test-runs/2026-11-15-resume-later-release.md (c): a person opened a
// review thread on the pull request of a task resting on resume_later(ci),
// and nothing happened until CI's verdict or the 6h backstop woke it.
// SyncReviewThreads passed over every task not completed or awaiting a
// Submit, so sendRunForReviewThreads -- which already takes the task off its
// resume_later -- was never reached for one. A person's Defer still holds.
func TestANewReviewThreadTakesATaskOffItsResumeLater(t *testing.T) {
	t.Parallel()
	for _, how := range []string{"resume_later", "a person"} {
		how := how
		t.Run(how, func(t *testing.T) {
			t.Parallel()
			store, ctx := openStore(t)
			sim, client := newSim(t, "acme", "widgets", "main")
			repo := model.RepoRef{Owner: "acme", Name: "widgets"}
			task := completedTask(t, ctx, store, sim, client, "t1", repo, true, "")
			number := sim.PullRequests[0].Number
			at := baseTime.Add(time.Minute)
			if how == "resume_later" {
				restOnResumeLaterCI(t, ctx, store, task.ID, at)
			} else {
				if err := store.ObserveField(ctx, task.ID, at, func(o *model.Observation) { o.CompletedAt = nil }); err != nil {
					t.Fatal(err)
				}
				if err := store.SetDeferred(ctx, task.ID, &at, nil, at); err != nil {
					t.Fatal(err)
				}
			}

			line := 3
			sim.ReviewComments = map[int][]github.ReviewComment{number: {
				{ID: 70001, User: "bob", Body: "Please rename this variable.", Path: "walk.txt", Line: &line},
			}}
			if err := orchestrator.SyncReviewThreads(ctx, store, client, at.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			want := model.StateQueued
			if how == "a person" {
				want = model.StateDeferred
			}
			if s := stateOf(t, ctx, store, task.ID); s != want {
				t.Fatalf("after a new review thread the task is %q, want %q", s, want)
			}
		})
	}
}

// docs/test-runs/2026-11-15-resume-later-release.md (e): a change held on
// its review's questions ran again (a repair, say) and that run rests on
// resume_later(ci). Its owner's answer has to send it back now, as it does
// for a change awaiting a Submit, not wait out CI or the backstop.
func TestAnOwnersReviewDecisionTakesATaskOffItsResumeLater(t *testing.T) {
	t.Parallel()
	store, ctx, _, reviewed, _ := heldOnQuestions(t)
	at := baseTime.Add(time.Hour)
	restOnResumeLaterCI(t, ctx, store, reviewed.ID, at)

	say(t, ctx, store, reviewed.ID, owner, "Agreed, keep it core-only.", at.Add(time.Minute))
	if err := orchestrator.SyncReviewQuestions(ctx, store, at.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if s := stateOf(t, ctx, store, reviewed.ID); s != model.StateQueued {
		t.Fatalf("after its owner's decision the task is %q, want %q", s, model.StateQueued)
	}
	if _, held := model.ReviewQuestionsOf(reloaded(t, ctx, store, reviewed.ID)); held {
		t.Error("the answered hold is still recorded")
	}
}
