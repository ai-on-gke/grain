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
	"errors"
	"fmt"
	"log"
	"strings"

	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// # The merge queue giving up is news for a person's Inbox
//
// blockMergeQueue is the queue saying "this needs a person", and until
// this file it said so in exactly two places: a comment on the task's own
// thread and the row's merge_failed badge. Neither reaches anybody who is
// not already looking at that task. Task 4525 (2026-11-12) sat for
// seventeen hours like that: its owner had approved it, so the Inbox's
// Submit item no longer listed it, and nothing else did -- its repairs were
// spent, CI was red on a fault grain itself called somebody else's, and
// the only alert open was a stall warning about a different task.
//
// So the block raises pipeline.merge-needs-person:<task> as well, whether
// the change has been approved or not, naming what unblocks it. It is
// cleared once the queue is driving the change again (a person pressed
// Retry merge, which clears MergeQueueBlockedAt) or the change merged or
// closed (reconcileMergeNeedsPersonClears, and the ClearsOnTask condition
// beside it).

// raiseMergeNeedsPerson is blockMergeQueueAgainst's notification. reason
// is the row's own words for what the queue died of, note the merge
// queue note with its way out already appended.
func raiseMergeNeedsPerson(ctx context.Context, store *model.Store, taskID, reason, note string,
	now time.Time) error {

	task, err := store.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return nil
	}
	what := "its pull request"
	repo := ""
	if ref, ok := trackedPullRequest(*task); ok {
		what = ref.String()
		repo = ref.Repo.String()
	} else if task.Target != nil {
		repo = task.Target.String()
	}
	n := model.Notification{
		Subject:  model.NotificationSubject(model.FamilyMergeNeedsPerson, taskID),
		Family:   model.FamilyMergeNeedsPerson,
		Severity: model.NotificationWarning,
		Title:    fmt.Sprintf("The merge queue gave up on %s (task %s): it needs a person", what, taskID),
		Body:     mergeNeedsPersonBody(taskID, what, cmpOrString(note, reason)),
		Repo:     repo,
		TaskID:   taskID,
		RaisedBy: mergeQueueNotifier,
		Actions:  []string{"open-task", "investigate"},
		ClearsWhen: &model.NotificationClear{
			Kind: model.ClearsOnTask, Task: taskID, Until: "closed",
		},
	}
	return ensurePipelineNotification(ctx, store, n, now)
}

// mergeNeedsPersonBody says what stopped the queue and the three ways out
// of it, in the order a person should try them.
func mergeNeedsPersonBody(taskID, what, why string) string {
	return fmt.Sprintf("[Task #%s](/tasks/%s): the merge queue stopped driving %s -- %s.\n\n"+
		"What unblocks it, whichever applies:\n"+
		"- **A fix:** push one to the branch (or resolve it on GitHub); it merges as soon as it "+
		"reads clean.\n"+
		"- **Retry merge** on the task, once what was red has been dealt with -- including a red "+
		"that was not this branch's own and has since gone green on the base.\n"+
		"- **A stale gate:** if a review or hold for an older head is what is still red, close "+
		"it on its own task so the newer one decides.",
		taskID, taskID, what, strings.TrimSuffix(why, "."))
}

func cmpOrString(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// reconcileMergeNeedsPersonClears clears every pipeline.merge-needs-person
// whose task the queue is driving again (no MergeQueueBlockedAt) or whose
// pull request has settled.
func reconcileMergeNeedsPersonClears(ctx context.Context, store *model.Store) error {
	open, err := store.ListNotifications(ctx, model.NotificationFilter{
		Open: true, Family: model.FamilyMergeNeedsPerson, Limit: 1000,
	})
	if err != nil {
		return err
	}
	var errs []error
	for _, n := range open {
		taskID := n.TaskID
		if taskID == "" {
			_, taskID, _ = strings.Cut(n.Subject, ":")
		}
		obs, err := store.GetObservation(ctx, taskID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if obs != nil && obs.MergeQueueBlockedAt != nil && obs.PrMergedAt == nil && obs.PrClosedAt == nil {
			continue
		}
		if _, err := store.ClearNotification(ctx, n.Subject, mergeQueueNotifier); err != nil {
			errs = append(errs, err)
			continue
		}
		log.Printf("orchestrator: cleared notification %s: the merge queue is no longer blocked on it", n.Subject)
	}
	return errors.Join(errs...)
}
