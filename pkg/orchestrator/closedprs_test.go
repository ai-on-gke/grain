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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// Task 2350's sequence: the queue blocks a pull request on a rule it
// cannot satisfy, the task is closed, and a person then merges the pull
// request by hand. The merge has to reach the record -- the merged
// stamp, the "merged" outcome a wait until "merged" reads, and no
// "Blocked ... Retry merge" left standing over a change that landed --
// within the cap an idle pull request is read at.
func TestAPullRequestMergedByHandAfterItsTaskClosedIsRecordedMerged(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "late", "main")
	repo := model.RepoRef{Owner: "acme", Name: "late"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = greenChecks()
	sim.RefuseMerges = "Repository rule violations found\n\nAll comments must be resolved."
	sim.BranchRules = map[string][]map[string]any{"main": {{"type": "required_review_thread_resolution"}}}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err == nil {
		t.Fatal("expected the refused merge to be reported")
	}
	if obs := mergeQueueStatus(t, ctx, store, task.ID); obs.MergeQueueBlockedAt == nil {
		t.Fatalf("the refusal was not handed to a person: state %q", obs.MergeQueueState)
	}

	closedAt := baseTime.Add(3 * time.Minute)
	closeTask(t, ctx, store, task.ID, closedAt)
	if err := orchestrator.SyncPullRequests(ctx, store, client, closedAt.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests after the close: %v", err)
	}
	if obs := mergeQueueStatus(t, ctx, store, task.ID); obs.PrMergedAt != nil || obs.PrClosedAt != nil {
		t.Fatal("a pull request still open was recorded as settled")
	}

	// Merged by hand, outside grain.
	mergedAt := closedAt.Add(16 * time.Hour)
	for i := range sim.PullRequests {
		if sim.PullRequests[i].Head == branch {
			sim.PullRequests[i].State = "closed"
			sim.PullRequests[i].Merged = true
			sim.PullRequests[i].MergedAt = mergedAt
		}
	}
	// One idle-read cap is enough for the merge to be seen.
	if err := orchestrator.SyncPullRequests(ctx, store, client, mergedAt.Add(github.PollCap)); err != nil {
		t.Fatalf("SyncPullRequests after the hand merge: %v", err)
	}
	obs := mergeQueueStatus(t, ctx, store, task.ID)
	if obs.PrMergedAt == nil || !obs.PrMergedAt.Equal(mergedAt) {
		t.Fatalf("PrMergedAt = %v, want the forge's own merge time %v", obs.PrMergedAt, mergedAt)
	}
	if obs.MergeQueueBlockedAt != nil || obs.MergeQueueState != "" {
		t.Fatalf("the row still reads blocked %v in state %q over a merged change",
			obs.MergeQueueBlockedAt, obs.MergeQueueState)
	}
	if obs.ClosedAt == nil || !obs.ClosedAt.Equal(closedAt) {
		t.Fatalf("ClosedAt = %v, want the task's own close %v kept", obs.ClosedAt, closedAt)
	}
	target, err := store.WaitTargetOf(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if settled, met := target.Settled(true); !settled || !met || target.Outcome != model.WaitOutcomeMerged {
		t.Fatalf("a wait until merged reads settled=%v met=%v outcome %q, want it met as merged",
			settled, met, target.Outcome)
	}

	// Settled for good: the pull request is not read again.
	before := len(sim.Calls)
	for _, at := range []time.Duration{time.Hour, 2 * time.Hour} {
		if err := orchestrator.SyncPullRequests(ctx, store, client, mergedAt.Add(at)); err != nil {
			t.Fatalf("SyncPullRequests: %v", err)
		}
	}
	if got := len(sim.Calls) - before; got != 0 {
		t.Fatalf("a settled closed task's pull request was read %d more times", got)
	}
}

// The other way such a pull request settles: closed without merging. It
// is recorded as that -- not as a merge -- and is not read again.
func TestAPullRequestClosedAfterItsTaskClosedIsRecordedAndForgotten(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "abandoned", "main")
	repo := model.RepoRef{Owner: "acme", Name: "abandoned"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	closeTask(t, ctx, store, task.ID, baseTime)

	for i := range sim.PullRequests {
		if sim.PullRequests[i].Head == branch {
			sim.PullRequests[i].State = "closed"
		}
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	obs := mergeQueueStatus(t, ctx, store, task.ID)
	if obs.PrClosedAt == nil || obs.PrMergedAt != nil {
		t.Fatalf("closed %v merged %v, want the pull request recorded closed unmerged", obs.PrClosedAt, obs.PrMergedAt)
	}
	links, err := store.ClosedUnsettledPullRequestLinks(ctx, baseTime.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 {
		t.Fatalf("still watching %v after the pull request closed", links)
	}
}

// A task closed longer ago than the watch window is not read at all,
// which is what keeps a deployment's old closures from costing a read
// each forever.
func TestALongClosedTasksPullRequestIsNotRead(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "old", "main")
	repo := model.RepoRef{Owner: "acme", Name: "old"}
	task, _ := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	closeTask(t, ctx, store, task.ID, baseTime)

	before := len(sim.Calls)
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(31*24*time.Hour)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if got := len(sim.Calls) - before; got != 0 {
		t.Fatalf("a task closed a month ago had its pull request read %d times", got)
	}
}
