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

// The merge-queue states demo is the fixture behind
// ui/e2e/merge-queue-waiting-states.spec.js: a submitted task with an open
// pull request, parked by the merge queue in a state it waits in without
// blocking anything -- waiting_github_approval (orchestrator's
// githubrefusal.go). Opt-in, so the ordinary demo's task list, which
// other specs count, is unchanged.
//
// The row is written through the store exactly as the queue writes it
// (Observation.MergeQueueState, MergeQueueNote, MergeQueueStateAt), and
// the note is the sentence approvalNote produces, so what the browser
// renders is the real UI reading a real row.

import (
	"context"
	"fmt"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

const demoWaitingApprovalTitle = "Move the billing exporter onto the new schema"

// demoMergeReasons is the sub-case the merge queue records with each
// state these seeds write (orchestrator's livenessreasons.go).
var demoMergeReasons = map[string]string{
	"waiting_github_approval": "waiting_github_approval/approval",
}

func seedMergeQueueStatesDemo(ctx context.Context, store *model.Store, cfg ui.Config) error {
	client := ui.NewClient(cfg, store)
	now := time.Now().UTC()
	seed := func(title, pr, state, note string) error {
		at := now.Add(-20 * time.Minute)
		client.Now = func() time.Time { return now.Add(-3 * time.Hour) }
		created, err := client.CreateTask(ctx, ui.CreateTaskRequest{
			Title:       title,
			Description: "Seeded by grain demo -merge-queue-states.",
			Approved:    true,
		})
		if err != nil {
			return fmt.Errorf("seeding %q: %w", title, err)
		}
		task, err := store.GetTask(ctx, created.ID)
		if err != nil {
			return err
		}
		task.Links = append(task.Links, model.Link{Kind: model.LinkFixes, Target: pr})
		task.AutoMerge = true
		if err := store.PutTask(ctx, *task); err != nil {
			return fmt.Errorf("seeding %q: %w", title, err)
		}
		return store.ObserveField(ctx, task.ID, at, func(o *model.Observation) {
			o.CompletedAt = &at
			o.MergeQueueState, o.MergeQueueNote, o.MergeQueueStateAt = state, note, &at
			o.MergeQueueReason = demoMergeReasons[state]
		})
	}
	return seed(demoWaitingApprovalTitle, "acme/widgets#121", "waiting_github_approval",
		"GitHub requires an approving review before this merges (code owners @acme/platform, @bob; "+
			"1 approval still needed (0 of 1 given); ruleset \"protect main\"): At least 1 approving review is required "+
			"by reviewers with write access. Approve it at https://github.com/acme/widgets/pull/121/files "+
			"and grain merges it on its own; it has left the queue so nothing behind it waits. A Submit "+
			"or approval in grain does not count toward GitHub's rule, nor GitHub's toward grain's.")
}
