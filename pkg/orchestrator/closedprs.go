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

// closedprs.go watches the pull requests of tasks that closed before their
// pull request did.
//
// SyncPullRequests reads a task's pull request only while the task is
// open (Store.OpenPullRequestLinks): its closing write is what takes the
// task out of that list, and on every ordinary path that write is the
// one recording the merge. A task a person closes first leaves no such
// write behind. Task 2350 is the case: the merge queue gave up on PR #47
// (a review conversation it could not resolve), the task was closed three
// minutes later, and the operator then resolved the threads and merged
// the pull request by hand. Nothing read #47 again, so the task went on
// saying "Blocked ... until somebody presses Retry merge" over a change
// that had landed, its outcome read "cancelled", and anything waiting on
// it until "merged" waited on a merge grain would never see.
//
// This pass is that read, and nothing else: it never merges, repairs or
// queues anything. Each cycle it lists the closed tasks whose pull request
// grain has no settled word on (Store.ClosedUnsettledPullRequestLinks),
// reads each one that is due, and records what it finds -- a merge as
// PrMergedAt (with the "Merge blocked" stamp and the queue's last words
// taken off the row), a close as PrClosedAt -- and either write takes the
// task off the list for good.
//
// Kept cheap two ways. Reads go through the shared github.PollSchedule as
// PollNormal with nothing active, so a pull request that reads the same
// each time backs off to github.PollCap (eight minutes) -- the same bound
// on noticing a hand-merge that an idle open pull request has -- and is
// held there while the budget is low. And a task is only watched for
// closedPullRequestWindow after it closed: past that, a pull request left
// open under a closed task is one nobody is going to merge.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// closedPullRequestWindow is how long after a task closes its unsettled
// pull request is still read -- model's, since a wait until "merged" on
// the task waits exactly as long (model.WaitTarget.PullRequestOpen).
const closedPullRequestWindow = model.ClosedPullRequestWindow

// closedPollAspect is the PollKey aspect for syncClosedPullRequests' read,
// apart from SyncPullRequests' own so the two backoffs do not share a
// clock across the close.
const closedPollAspect = "closed task's pull request"

// syncClosedPullRequests records the merge or close of every closed task's
// pull request that settled after the task did. See the file comment.
func syncClosedPullRequests(ctx context.Context, store *model.Store, client github.Client, now time.Time) error {
	links, err := store.ClosedUnsettledPullRequestLinks(ctx, now.Add(-closedPullRequestWindow))
	if err != nil {
		return fmt.Errorf("orchestrator: reading closed tasks' pull requests: %w", err)
	}
	var errs []error
	for _, link := range links {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		ref, err := model.ParsePullRequestRef(link.PullRequest)
		if err != nil {
			// Already reported for the open task it was once; a closed
			// task's unreadable link is not worth a line every cycle.
			continue
		}
		key := github.PollKey{Scope: storeScope(store),
			Subject: github.PullRequestSubject(ref.Repo.StoredOwner(), ref.Repo.Name, ref.Number), Aspect: closedPollAspect}
		if !github.Polls().Due(key, github.PollNormal, false, now) {
			continue
		}
		detail, err := client.GetPullRequest(ref.Repo.StoredOwner(), ref.Repo.Name, ref.Number)
		if err != nil {
			// Backed off like any other read that saw nothing new, so a
			// forge refusing every read is asked every PollCap rather than
			// every cycle -- and reported each time, not swallowed.
			github.Polls().Observe(key, "unreadable", now)
			errs = append(errs, fmt.Errorf("orchestrator: reading %s for closed task %s: %w", ref, link.TaskID, err))
			continue
		}
		github.Polls().Observe(key, detail.State+"/"+strconv.FormatBool(detail.Merged), now)
		if err := recordClosedTaskPullRequest(ctx, store, link.TaskID, ref, detail, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// recordClosedTaskPullRequest writes down what one read of a closed task's
// pull request found, and nothing for one still open.
func recordClosedTaskPullRequest(ctx context.Context, store *model.Store, taskID string,
	ref model.PullRequestRef, detail github.PullRequestDetail, now time.Time) error {

	if !detail.Merged && detail.State != "closed" {
		return nil
	}
	obs, err := store.GetObservation(ctx, taskID)
	if err != nil {
		return fmt.Errorf("orchestrator: reading observation for %s: %w", taskID, err)
	}
	if obs == nil || obs.ClosedAt == nil || obs.PrMergedAt != nil || obs.PrClosedAt != nil {
		return nil
	}
	// The queue's last words about it are no longer true either way: a
	// "waiting for a person" state (outlivesTheQueuesAttention) survives
	// the close on purpose, and this is the read that ends it.
	if err := setMergeQueueState(ctx, store, obs, taskID, queueStatus{}, now); err != nil {
		return err
	}
	forgetEmptyChecks(ref)
	forgetMergeRefusals(ref)
	if err := observeField(ctx, store, taskID, now, func(o *model.Observation) {
		if o.PrMergedAt != nil || o.PrClosedAt != nil {
			return
		}
		o.MergeQueueBlockedAt = nil
		o.MergeQueueBlockedBase = ""
		if !detail.Merged {
			o.PrClosedAt = &now
			return
		}
		mergedAt := now
		if detail.MergedAt != nil {
			mergedAt = *detail.MergedAt
		}
		o.PrMergedAt = &mergedAt
		if detail.Additions != 0 || detail.Deletions != 0 || detail.ChangedFiles != 0 {
			additions, deletions, files := detail.Additions, detail.Deletions, detail.ChangedFiles
			o.PrAdditions, o.PrDeletions, o.PrChangedFiles = &additions, &deletions, &files
		}
	}); err != nil {
		return err
	}
	if detail.Merged {
		if t, err := store.GetTask(ctx, taskID); err == nil && t != nil {
			_ = CheckAndMarkTuningAdoption(ctx, store, *t)
		}
	}
	return nil
}
