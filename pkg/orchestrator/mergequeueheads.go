package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// MergeQueueHeads reports the branches at the front of each repository's
// merge queues, keyed by "owner/name" -- one per lane (mergelane.go: each
// base branch of a repository is a queue of its own), and the same queues,
// in the same order, SyncPullRequests itself acts on (queueOrder,
// mergeWalk). A repository merging into `main` and `candidate` at once
// has two heads, and both verdicts unblock somebody.
//
// It reads the store and nothing else: membership and position are both
// derived facts (isQueueMember over auto-merge, fix-task and blocked-at
// state, and Task.OrderKey, which is the backlog's own order), so
// answering this needs no GitHub round trip and no cycle to have run
// recently. That is what makes it something another part of the daemon can
// ask on its own schedule.
//
// grain CI is the caller (cmd/grain's ciMergeQueuePriority). The change at
// the front of a queue is the one whose verdict unblocks everything behind
// it, so when the deployment's sandboxes are short, its jobs are the ones
// worth spending them on first -- CI time converted into merges rather
// than into progress on four branches at once (ci.PriorityMergeHead,
// grain/task-316).
//
// A repository with nothing queued is absent rather than empty. A link
// whose ref will not parse is skipped and reported, exactly as the
// reconciler treats one: nothing can ever be done with a pull request
// whose repo and number are unreadable, and it can never be anybody's
// head, so one malformed row must not cost every other repository its
// answer.
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
	for lane, e := range laneHeads(queueOrder(entries)) {
		// The repo the change is queued against, and the branch it is on:
		// the two halves of what identifies a CI run (ci.Request's Repo
		// and Ref).
		addHead(laneRepo(lane), e.task.BranchName())
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

// laneHeads is the first entry of each lane in members, by the lane each
// task declares (laneOf with nothing read). It is reconcileSync's own
// heads (mergeWalk.claimHead) for every task that names its base; a task
// that does not is laned as its repository's default branch.
func laneHeads(members []queueEntry) map[string]queueEntry {
	heads := map[string]queueEntry{}
	for _, e := range members {
		lane := laneOf(e)
		if _, ok := heads[lane]; !ok {
			heads[lane] = e
		}
	}
	return heads
}
