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
	"fmt"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// MergeQueueHeads reports the branches whose CI verdict is the last thing
// standing between them and a merge, keyed by "owner/name" and then by
// branch: every member of every lane (mergelane.go) that is otherwise
// ready to merge, plus the base repairs and fault owners the queue is
// waiting on.
//
// It used to name only the front of each lane. That was the queue's own
// order when a lane merged one change at a time and every member behind
// the front waited on it. Members no longer wait on each other
// (mergeWalk, grain/task-2346): each merges the moment the predicate
// admits it, so any one of them going green unblocks a merge, and none
// unblocks anyone else's. Naming one of them would rank its CI above
// changes that merge just as soon and help nobody behind it. So every
// member that is otherwise ready (readyButForCI) ranks the same. A
// member something else is still holding does not: its verdict merges
// nothing until a person approves it or its repair finishes.
//
// It reads the store and nothing else: membership is a derived fact
// (isQueueMember over auto-merge, fix-task and blocked-at state), so
// answering this needs no GitHub round trip and no cycle to have run
// recently. That is what makes it something another part of the daemon can
// ask on its own schedule.
//
// grain CI is the caller (cmd/grain's ciMergeQueuePriority). When the
// deployment's sandboxes are short, the jobs of a change that merges on
// its verdict are the ones worth spending them on first -- CI time
// converted into merges rather than into progress on branches that cannot
// merge yet (ci.PriorityMergeHead, grain/task-316).
//
// A repository with nothing queued is absent rather than empty. A link
// whose ref will not parse is skipped and reported, exactly as the
// reconciler treats one: nothing can ever be done with a pull request
// whose repo and number are unreadable, so one malformed row must not
// cost every other repository its answer.
func MergeQueueHeads(ctx context.Context, store *model.Store) (map[string]map[string]bool, error) {
	links, err := store.OpenPullRequestLinks(ctx)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: reading open pull request links: %w", err)
	}
	entries := make([]queueEntry, 0, len(links))
	for _, link := range links {
		task, err := store.GetTask(ctx, link.TaskID)
		if err != nil {
			return nil, fmt.Errorf("orchestrator: reading task %s: %w", link.TaskID, err)
		}
		if task == nil {
			continue
		}
		obs, err := store.GetObservation(ctx, link.TaskID)
		if err != nil {
			return nil, fmt.Errorf("orchestrator: reading observation for %s: %w", link.TaskID, err)
		}
		ref, err := model.ParsePullRequestRef(link.PullRequest)
		if err != nil {
			continue
		}
		entries = append(entries, queueEntry{task: *task, obs: obs, ref: ref, paused: taskPaused(ctx, store, link.TaskID)})
	}
	heads := map[string]map[string]bool{}
	addHead := func(repo, branch string) {
		if repo == "" || branch == "" {
			return
		}
		if heads[repo] == nil {
			heads[repo] = map[string]bool{}
		}
		heads[repo][branch] = true
	}
	for _, e := range queueOrder(entries) {
		if !readyButForCI(e) {
			continue
		}
		// The repo the change is queued against, and the branch it is on:
		// the two halves of what identifies a CI run (ci.Request's Repo
		// and Ref).
		addHead(laneRepo(laneOf(e)), e.task.BranchName())
	}
	// Grant merge-head CI priority to active base-repair / FixesBaseBranch
	// tasks and to any fault-owner task that another entry in the queue is
	// waiting on via BaseRepairWaitTag.
	if markedIDs, err := store.OpenTasksWithTag(ctx, model.MarkedBaseRepairTag); err == nil {
		for _, id := range markedIDs {
			task, err := store.GetTask(ctx, id)
			if err != nil || task == nil || !countsAsBaseRepair(*task) {
				continue
			}
			if obs, err := store.GetObservation(ctx, id); err == nil && obs != nil && obs.MergeQueueBlockedAt != nil {
				continue
			}
			repo, _ := model.TaskBaseBranch(*task)
			if repo.Owner != "" {
				addHead(repo.String(), task.BranchName())
			}
		}
	}
	seenOwner := map[string]bool{}
	for _, e := range entries {
		for _, tag := range e.task.Tags {
			ownerID, ok := strings.CutPrefix(tag, "base-ci-wait:")
			if !ok || ownerID == "" || seenOwner[ownerID] {
				continue
			}
			seenOwner[ownerID] = true
			st, err := store.State(ctx, ownerID)
			if err != nil || st == "" || st == model.StateClosed || st == model.StateFailed {
				continue
			}
			if obs, err := store.GetObservation(ctx, ownerID); err == nil && obs != nil && obs.MergeQueueBlockedAt != nil {
				continue
			}
			owner, err := store.GetTask(ctx, ownerID)
			if err != nil || owner == nil || (!countsAsBaseRepair(*owner) && owner.Approval == nil) {
				continue
			}
			repo, _ := model.TaskBaseBranch(*owner)
			if repo.Owner != "" {
				addHead(repo.String(), owner.BranchName())
			}
		}
	}
	return heads, nil
}

// readyButForCI is whether a queue member's own CI verdict is what it is
// waiting on, as far as the store can say: no approval is outstanding and
// no repair the queue asked for is still being worked on its branch. A
// member held by either merges nothing when its CI goes green, so its jobs
// are not worth putting ahead of one that does.
func readyButForCI(e queueEntry) bool {
	if len(model.PendingApprovals(e.task)) > 0 {
		return false
	}
	return e.obs == nil || !e.obs.RepairInFlight()
}
