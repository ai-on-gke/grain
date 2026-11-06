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

package demo

import (
	"context"
	"fmt"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// seedHumanReviewDemo is `grain demo -human-review`: a finished change
// whose review found the code fine but asked a person to decide something
// about it (request_human_review), so it waits on a human-review hold
// (model.LinkHumanReviewHold) -- in the inbox, with a banner on its page.
// Opt-in, like the other hold demos, so the default demo's inbox stays as
// it is.
func seedHumanReviewDemo(ctx context.Context, store *model.Store, cfg ui.Config, repo model.RepoRef) error {
	now := time.Now().UTC()
	client := ui.NewClient(cfg, store)
	held, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       "Default new workspaces to the paid tier",
		Description: "New workspaces start on the paid tier instead of the free one.",
		Repo:        repo.String(),
		Approved:    true,
		AutoMerge:   true,
	})
	if err != nil {
		return fmt.Errorf("seeding the held change: %w", err)
	}
	review, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       "Review: Default new workspaces to the paid tier",
		Description: "Review " + model.BranchName(held.ID) + " and fix what you find.",
		Repo:        repo.String(),
		Base:        model.BranchName(held.ID),
		Approved:    true,
	})
	if err != nil {
		return fmt.Errorf("seeding the held change's review: %w", err)
	}
	if err := store.UpdateTask(ctx, review.ID, func(t *model.Task) error {
		t.Origin.Reason = model.ReasonReview
		t.Links = append(t.Links, model.Link{Kind: model.LinkProposedBy, Target: held.ID})
		return nil
	}); err != nil {
		return fmt.Errorf("marking the review: %w", err)
	}
	if err := store.UpdateTask(ctx, held.ID, func(t *model.Task) error {
		t.Links = append(t.Links,
			model.Link{Kind: model.LinkFixes, Target: repo.String() + "#107"},
			model.Link{Kind: model.LinkReviewTask, Target: review.ID})
		return nil
	}); err != nil {
		return fmt.Errorf("linking the held change: %w", err)
	}
	finished := now.Add(-10 * time.Minute)
	if err := store.ObserveField(ctx, held.ID, finished, func(o *model.Observation) {
		o.CompletedAt = &finished
	}); err != nil {
		return fmt.Errorf("finishing the held change's run: %w", err)
	}
	_, err = store.RaiseHumanReviewHold(ctx, held.ID, model.HumanReviewRequest{
		Reason: model.HumanReviewReason{Source: model.HumanReviewRequested, Category: model.HumanReviewProductDecision,
			Reason:     "Changing the default tier changes what new customers are billed",
			ReviewTask: review.ID, Run: review.ID + "-1", At: now.Add(-5 * time.Minute)},
		Details: "The code is correct and tested. Whether new workspaces should start on the paid tier " +
			"is a product decision: I recommend confirming with billing before this merges.",
		By: model.Principal{Kind: model.PrincipalAgent, ID: review.ID},
	})
	return err
}
