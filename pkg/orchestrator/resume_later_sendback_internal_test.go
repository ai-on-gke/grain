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
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/forge"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// deferOnResumeLaterCI does what ui.Client.ResumeLater does for a run of
// task that waits on ci: defers it, holds the condition on the live run,
// leaves its comment -- and then finishes the run.
func deferOnResumeLaterCI(t *testing.T, ctx context.Context, store *model.Store, taskID string, at time.Time) {
	t.Helper()
	if err := store.StartRun(ctx, model.Run{ID: taskID + "-1", TaskID: taskID, Sandbox: "s", Attempt: 1,
		StartedAt: at.Add(-time.Hour)}, model.Limits{}); err != nil {
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
	if err := store.FinishRun(ctx, taskID+"-1", at.Add(10*time.Second), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
}

// Tasks 4523 and 4526: the merge queue sent a task resting on
// resume_later(ci) back to an agent for a repair, but the deferral
// outranked the repair's RetryRequestedAt, so the repair never ran and the
// queue gave up on it as never finished. The send-back has to take the
// task off its resume_later; a deferral a person made is theirs to lift.
func TestAMergeQueueRepairTakesATaskOffItsResumeLater(t *testing.T) {
	t.Parallel()
	for _, how := range []string{"resume_later", "a person"} {
		how := how
		t.Run(how, func(t *testing.T) {
			t.Parallel()
			store, ctx := openTestStore(t)
			repo := model.RepoRef{Owner: "acme", Name: "widgets"}
			human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
			task := model.Task{ID: "q", Intent: model.IntentImplement, Title: "t", Body: "b",
				Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
				Approval: &model.Attribution{Actor: human}, Target: &repo, Binding: model.BindingDirective}
			if err := store.PutTask(ctx, task); err != nil {
				t.Fatal(err)
			}
			at := time.Date(2026, 11, 11, 23, 17, 43, 0, time.UTC)
			if how == "resume_later" {
				deferOnResumeLaterCI(t, ctx, store, task.ID, at)
			} else if err := store.SetDeferred(ctx, task.ID, &at, nil, at); err != nil {
				t.Fatal(err)
			}

			now := at.Add(time.Hour)
			ref := model.PullRequestRef{Repo: repo, Number: 1265}
			if err := requeueForRepair(ctx, store, task, ref, github.PullRequestDetail{HeadRef: task.BranchName()},
				model.PrConflicted, nil, grainCIVerdict{}, buildEvidence{}, refreshOutcome(0), false, forge.Reds{}, "",
				repairAttempt{n: 1, max: 2}, now); err != nil {
				t.Fatal(err)
			}
			state, err := store.State(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := model.StateQueued
			if how == "a person" {
				want = model.StateDeferred
			}
			if state != want {
				t.Fatalf("after the repair's send-back the task is %q, want %q", state, want)
			}
		})
	}
}
