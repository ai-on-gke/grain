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

// A deferral whose suspension row was lost -- a run made before
// Store.HoldResumeOn -- is still resume_later's own when its comment sits
// at the deferral's instant, and ReleaseResumeLater lifts it. Once a
// person defers the task again, the old comment no longer matches and the
// deferral is theirs to lift.
func TestReleaseResumeLaterReadsTheCommentWhenNoSuspensionHoldsIt(t *testing.T) {
	store, ctx := open(t)
	task := model.Task{
		ID: "rl", Intent: model.IntentImplement, Title: "t",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human}, Binding: model.BindingDirective,
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	if released, err := store.ReleaseResumeLater(ctx, "rl", now); err != nil || released {
		t.Fatalf("ReleaseResumeLater on a task nobody deferred = %v, %v; want false", released, err)
	}

	deferredAt := now.Add(time.Minute)
	if err := store.SetDeferred(ctx, "rl", &deferredAt, nil, deferredAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddComment(ctx, model.Comment{TaskID: "rl",
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body: "grain: this task's run stopped with resume_later. It goes back in the queue once " +
			model.ResumeOn{CI: model.ResumeOnMyBranch}.Describe() + "; Undefer releases it sooner.",
		CreatedAt: deferredAt}); err != nil {
		t.Fatal(err)
	}

	// A person defers it again later: the comment is not at this instant.
	again := now.Add(time.Hour)
	if err := store.SetDeferred(ctx, "rl", &again, nil, again); err != nil {
		t.Fatal(err)
	}
	if released, err := store.ReleaseResumeLater(ctx, "rl", again.Add(time.Minute)); err != nil || released {
		t.Fatalf("ReleaseResumeLater on a person's deferral = %v, %v; want false", released, err)
	}
	if st, err := store.State(ctx, "rl"); err != nil || st != model.StateDeferred {
		t.Fatalf("state after a person's deferral was passed over = %q, %v; want deferred", st, err)
	}

	// Put back resume_later's own deferral, at its comment's instant.
	if err := store.SetDeferred(ctx, "rl", &deferredAt, nil, again.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if released, err := store.ReleaseResumeLater(ctx, "rl", again.Add(3*time.Minute)); err != nil || !released {
		t.Fatalf("ReleaseResumeLater on resume_later's own deferral = %v, %v; want true", released, err)
	}
	if st, err := store.State(ctx, "rl"); err != nil || st == model.StateDeferred {
		t.Fatalf("state after the release = %q, %v; want it no longer deferred", st, err)
	}
}
