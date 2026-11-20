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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// TestSyncReviewsShortCircuitsAndHandlesSplitActivity verifies that
// SyncReviews short-circuits cleanly when no task declares a review template
// and no review task exists, still reports missing templates when a task
// declares one without any review task yet, and still closes finished reviews
// (such as security reviews) when no task declares a review template.
func TestSyncReviewsShortCircuitsAndHandlesSplitActivity(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	actor := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	approval := model.Attribution{Actor: actor}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	// 1. Ordinary tasks with open PRs and dependencies, no review declarations
	// and no review tasks: SyncReviews short-circuits cleanly.
	for _, id := range []string{"t-plain", "t-blocked"} {
		tk := model.Task{
			ID: id, Intent: model.IntentImplement, Title: id,
			Filing:     model.Filing{Attribution: model.Attribution{Actor: actor}, Reason: model.ReasonDirect},
			Binding:    model.BindingDirective,
			Target:     &repo,
			Approval:   &approval,
			ApprovedAt: &baseTime,
			AutoMerge:  true,
			CreatedAt:  &baseTime,
		}
		if id == "t-plain" {
			tk.Links = []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#1"}}
		} else {
			tk.Links = []model.Link{{Kind: model.LinkDependsOn, Target: "t-plain"}}
		}
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ObserveField(ctx, "t-plain", baseTime, func(o *model.Observation) { o.CompletedAt = &baseTime }); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.SyncReviews(ctx, store, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("SyncReviews on store with no review activity: %v", err)
	}

	// 2. A security-style review task with no ReviewTemplateID anywhere in the
	// store still gets closed once finished without a PR.
	if err := store.PutTask(ctx, model.Task{
		ID: "sec-rev", Intent: model.IntentImplement, Title: "Security review",
		Filing:     model.Filing{Attribution: model.Attribution{Actor: actor}, Reason: model.ReasonReview},
		Binding:    model.BindingDirective,
		Target:     &repo,
		Approval:   &approval,
		ApprovedAt: &baseTime,
		CreatedAt:  &baseTime,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(ctx, "sec-rev", baseTime, func(o *model.Observation) { o.CompletedAt = &baseTime }); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.SyncReviews(ctx, store, baseTime.Add(2*time.Minute)); err != nil {
		t.Fatalf("SyncReviews closing standalone review: %v", err)
	}
	if st, err := store.State(ctx, "sec-rev"); err != nil || st != model.StateClosed {
		t.Fatalf("sec-rev state = %q (%v), want closed", st, err)
	}

	// 3. A task naming a missing review template still surfaces the error
	// rather than being hidden by a template-table short-circuit.
	if err := store.PutTask(ctx, model.Task{
		ID: "t-missing-tmpl", Intent: model.IntentImplement, Title: "Missing template",
		Filing:           model.Filing{Attribution: model.Attribution{Actor: actor}, Reason: model.ReasonDirect},
		Binding:          model.BindingDirective,
		Target:           &repo,
		Approval:         &approval,
		ApprovedAt:       &baseTime,
		AutoMerge:        true,
		ReviewTemplateID: "deleted-template",
		Links:            []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#2"}},
		CreatedAt:        &baseTime,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(ctx, "t-missing-tmpl", baseTime, func(o *model.Observation) { o.CompletedAt = &baseTime }); err != nil {
		t.Fatal(err)
	}
	err := orchestrator.SyncReviews(ctx, store, baseTime.Add(3*time.Minute))
	if err == nil || !strings.Contains(err.Error(), "deleted-template") {
		t.Fatalf("SyncReviews with missing review template = %v, want error naming deleted-template", err)
	}
}
