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
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// A person's edit to a gated task's priority is carried onto its open
// gating review; a priority a person set on the review itself is raised
// by it but never lowered.
func TestUpdateTaskPriorityCarriesToGatingReview(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	gated, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "the change"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	now := time.Now()
	reviewer := model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "reviewer"}}
	review := model.Task{
		ID:        "review-of-" + gated.ID,
		Intent:    model.IntentImplement,
		Title:     "Review",
		Priority:  model.ReviewPriorityFloor,
		Origin:    model.Origin{Attribution: reviewer, Reason: model.ReasonReview},
		Approval:  &reviewer,
		Links:     []model.Link{{Kind: model.LinkProposedBy, Target: gated.ID}},
		CreatedAt: &now,
	}
	if err := store.PutTask(ctx, review); err != nil {
		t.Fatalf("PutTask(review): %v", err)
	}
	// A follow-up the change proposed is linked the same way, and is not
	// a review: its priority is its own.
	followUp := model.Task{
		ID:        "follow-up-of-" + gated.ID,
		Intent:    model.IntentImplement,
		Title:     "Follow-up",
		Priority:  4,
		Origin:    model.Origin{Attribution: reviewer, Reason: model.ReasonProposal},
		Approval:  &reviewer,
		Links:     []model.Link{{Kind: model.LinkProposedBy, Target: gated.ID}},
		CreatedAt: &now,
	}
	if err := store.PutTask(ctx, followUp); err != nil {
		t.Fatalf("PutTask(follow-up): %v", err)
	}
	reviewPriority := func() *model.Task {
		t.Helper()
		got, err := store.GetTask(ctx, review.ID)
		if err != nil || got == nil {
			t.Fatalf("GetTask(%s): %v", review.ID, err)
		}
		return got
	}
	setPriority := func(id string, p int) {
		t.Helper()
		if _, err := c.UpdateTask(ctx, id, ui.UpdateTaskRequest{Priority: &p}); err != nil {
			t.Fatalf("UpdateTask(%s, priority=%d): %v", id, p, err)
		}
	}

	setPriority(gated.ID, 9)
	if got := reviewPriority().Priority; got != 9 {
		t.Errorf("after raising the change to 9, review Priority = %d, want 9", got)
	}
	if got, err := store.GetTask(ctx, followUp.ID); err != nil || got == nil || got.Priority != 4 {
		t.Errorf("after raising the change to 9, follow-up = %+v, %v; want its own Priority 4", got, err)
	}
	setPriority(gated.ID, 3)
	if got := reviewPriority().Priority; got != model.ReviewPriorityFloor {
		t.Errorf("after lowering the change to 3, review Priority = %d, want the floor %d", got, model.ReviewPriorityFloor)
	}
	if slices.Contains(reviewPriority().Tags, model.ReviewPriorityHandSetTag) {
		t.Fatalf("review carries %q before anybody set its priority", model.ReviewPriorityHandSetTag)
	}

	// A person sets the review's own priority.
	setPriority(review.ID, 8)
	if !slices.Contains(reviewPriority().Tags, model.ReviewPriorityHandSetTag) {
		t.Fatalf("review Tags = %v, want %q once a person set its priority", reviewPriority().Tags, model.ReviewPriorityHandSetTag)
	}
	setPriority(gated.ID, 6)
	if got := reviewPriority().Priority; got != 8 {
		t.Errorf("after raising the change to 6 below the hand-set 8, review Priority = %d, want 8", got)
	}
	setPriority(gated.ID, 2)
	if got := reviewPriority().Priority; got != 8 {
		t.Errorf("after lowering the change to 2, hand-set review Priority = %d, want 8", got)
	}
	setPriority(gated.ID, 9)
	if got := reviewPriority().Priority; got != 9 {
		t.Errorf("after raising the change to 9, hand-set review Priority = %d, want 9", got)
	}
}
