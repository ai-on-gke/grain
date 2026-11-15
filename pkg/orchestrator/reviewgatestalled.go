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
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// # A review gate held behind a blocked review is news for a person
//
// A change's review gate reads "running (task N)" for as long as its
// review task is open, and a review that is queued behind a dependency
// that never clears reads exactly that, for ever. Seven re-reviews filed
// 2026-11-11..13 sat queued for days behind earlier reviews that had
// completed but never closed, each holding its change's merge at "queued",
// and nothing told anybody (task 6236). releaseFromCompletedReview ends that
// particular wait; this is the alarm for any other one.
//
// So a review task that has been queued and blocked for longer than
// ReviewGateStalledAfter raises pipeline.review-gate-stalled:<review>, once,
// naming what blocks it. It clears when the review dispatches, closes, or
// stops being blocked.

const (
	// FamilyReviewGateStalled is a review task queued behind a dependency
	// past ReviewGateStalledAfter, "<review task>".
	FamilyReviewGateStalled = "pipeline.review-gate-stalled"

	// ReviewGateStalledAfter is how long a review may wait, queued and
	// blocked, before somebody is told. Reviews dispatch at the front of
	// the queue (review_priority.go), so a few hours in line is already
	// not a queue.
	ReviewGateStalledAfter = 6 * time.Hour
)

// reconcileReviewGateStalled raises FamilyReviewGateStalled for every
// review that has been queued and blocked past ReviewGateStalledAfter, and
// clears it for every review that no longer is.
func reconcileReviewGateStalled(ctx context.Context, store *model.Store, now time.Time) error {
	ids, err := store.OpenTasksWithLink(ctx, model.LinkDependsOn)
	if err != nil {
		return fmt.Errorf("reading tasks with dependencies: %w", err)
	}
	stalled := map[string]bool{}
	var errs []error
	for _, id := range ids {
		n, err := reviewGateStalledNotification(ctx, store, id, now)
		if err != nil {
			errs = append(errs, err)
			// Leave whatever is open for it: not being able to read the
			// review is no evidence that it moved.
			stalled[id] = true
			continue
		}
		if n == nil {
			continue
		}
		stalled[id] = true
		if err := ensurePipelineNotification(ctx, store, *n, now); err != nil {
			errs = append(errs, err)
		}
	}
	if err := clearFamilyExcept(ctx, store, FamilyReviewGateStalled, stalled); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// reviewGateStalledNotification is the notification id's review stall
// raises, or nil when id is not a review, is not queued, is blocked by
// nothing open, or has not waited long enough.
func reviewGateStalledNotification(ctx context.Context, store *model.Store, id string,
	now time.Time) (*model.Notification, error) {

	review, err := store.GetTask(ctx, id)
	if err != nil || review == nil || !isReviewTask(*review) || review.CreatedAt == nil {
		return nil, err
	}
	if now.Sub(*review.CreatedAt) < ReviewGateStalledAfter {
		return nil, nil
	}
	state, err := store.State(ctx, id)
	if err != nil || state != model.StateQueued {
		return nil, err
	}
	var links, labels []string
	for _, l := range review.Links {
		if !l.Kind.Blocks() {
			continue
		}
		blockerState, err := store.State(ctx, l.Target)
		if err != nil {
			return nil, err
		}
		if blockerState == model.StateClosed {
			continue
		}
		if blockerState == "" {
			blockerState = "missing"
		}
		links = append(links, fmt.Sprintf("[task %s](/tasks/%s) (%s)", l.Target, l.Target, blockerState))
		labels = append(labels, fmt.Sprintf("task %s (%s)", l.Target, blockerState))
	}
	if len(links) == 0 {
		return nil, nil
	}
	what := "the change it reviews"
	repo := ""
	if target := linkTarget(*review, model.LinkReviewsPullRequest); target != "" {
		what = target
		if ref, err := model.ParsePullRequestRef(target); err == nil {
			repo = ref.Repo.String()
		}
	} else if review.Target != nil {
		repo = review.Target.String()
	}
	waited := now.Sub(*review.CreatedAt).Round(time.Hour)
	return &model.Notification{
		Subject:  model.NotificationSubject(FamilyReviewGateStalled, id),
		Family:   FamilyReviewGateStalled,
		Severity: model.NotificationWarning,
		Title: fmt.Sprintf("Review %s of %s has been queued for %s behind %s", id, what, waited,
			strings.Join(labels, ", ")),
		Body: fmt.Sprintf("[Task #%s](/tasks/%s) has been queued for %s and cannot start: it depends on %s, "+
			"which has not closed. Until it runs, the review gate on %s reads running and the change does "+
			"not merge.\n\nWhat unblocks it, whichever applies:\n"+
			"- **The blocker is done:** close it, so this review can run.\n"+
			"- **The blocker is stale:** close it as superseded by this review.\n"+
			"- **This review is not wanted:** close it, and the gate settles on what is left.",
			id, id, waited, strings.Join(links, ", "), what),
		Repo:     repo,
		TaskID:   id,
		RaisedBy: stewardArmsNotifier,
		Actions:  []string{"open-task", "investigate"},
		ClearsWhen: &model.NotificationClear{
			Kind: model.ClearsOnTask, Task: id, Until: "closed",
		},
	}, nil
}
