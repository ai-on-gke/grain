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

// The security hold on the wire (orchestrator's securityhold.go): a held
// task's pane has to be able to say why it is not merging itself and link
// to the review that decides it, and before these fields it could do
// neither (docs/test-runs/2026-10-01-security-hold-escalated-proposals.md,
// findings 3 and 4).

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestTaskDetailCarriesTheSecurityHoldAndItsReview(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	held := create(t, c, ctx)
	plain := create(t, c, ctx)

	if err := store.UpdateTask(ctx, held.ID, func(task *model.Task) error {
		task.Links = append(task.Links,
			model.Link{Kind: model.LinkSecurityHold, Target: "trust-boundary/v4"},
			model.Link{Kind: model.LinkSecurityReviewTask, Target: "1514"},
			model.Link{Kind: model.LinkTrustBoundaryCleared, Target: "trust-boundary/v4@abc"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	detail, err := c.GetTask(ctx, held.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.SecurityHold != "trust-boundary/v4" || detail.SecurityReviewTask != "1514" {
		t.Fatalf("securityHold = %q, securityReviewTask = %q; want trust-boundary/v4 and 1514",
			detail.SecurityHold, detail.SecurityReviewTask)
	}

	// The list shape carries it too, so a row can say the same thing the
	// pane does.
	list, err := c.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range list {
		switch task.ID {
		case held.ID:
			if task.SecurityHold != "trust-boundary/v4" || task.SecurityReviewTask != "1514" {
				t.Fatalf("listed held task = %+v, want its hold and review on it", task)
			}
		case plain.ID:
			if task.SecurityHold != "" || task.SecurityReviewTask != "" {
				t.Fatalf("listed plain task = %+v, want no hold or review on it", task)
			}
		}
	}

	// A hold that has ended -- on findings, or one only a human could
	// ever release -- leaves the review behind and nothing else.
	if err := store.UpdateTask(ctx, held.ID, func(task *model.Task) error {
		var kept []model.Link
		for _, l := range task.Links {
			if l.Kind != model.LinkSecurityHold {
				kept = append(kept, l)
			}
		}
		task.Links = kept
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	detail, err = c.GetTask(ctx, held.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.SecurityHold != "" || detail.SecurityReviewTask != "1514" {
		t.Fatalf("after the hold ended: securityHold = %q, securityReviewTask = %q; want \"\" and 1514",
			detail.SecurityHold, detail.SecurityReviewTask)
	}
}
