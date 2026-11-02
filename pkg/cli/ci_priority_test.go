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

package cli

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// The deployment's own answer to "which of these runs matters most": a
// branch in a repository's merge queue that merges on its verdict
// (grain/task-316). Members of a lane merge independently of each other
// (grain/task-2346), so every one of them ranks the same, and every other
// run is ranked the same as before the gate had an order at all.
func TestCIMergeQueuePriorityRanksEveryQueuedChangeAboveEverythingElse(t *testing.T) {
	t.Parallel()
	store := testStore(t)
	ctx := context.Background()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	first := queuedTaskLinkedToAPullRequest(t, ctx, store, "t1", repo, 1)
	second := queuedTaskLinkedToAPullRequest(t, ctx, store, "t2", repo, 2)

	rank := daemon.CiMergeQueuePriority(store)
	if got := rank(ci.Request{Repo: repo.String(), Ref: first.BranchName()}); got != ci.PriorityMergeHead {
		t.Errorf("the change queued first ranks %d, want %d", got, ci.PriorityMergeHead)
	}
	if got := rank(ci.Request{Repo: repo.String(), Ref: second.BranchName()}); got != ci.PriorityMergeHead {
		t.Errorf("the change queued second ranks %d, want %d: it merges on its own green", got, ci.PriorityMergeHead)
	}
	// A branch nobody has queued, and a request with nothing to go on:
	// both ordinary work rather than a lookup failure.
	if got := rank(ci.Request{Repo: repo.String(), Ref: "somebody-elses-branch"}); got != ci.PriorityNormal {
		t.Errorf("an unqueued branch ranks %d, want %d", got, ci.PriorityNormal)
	}
	if got := rank(ci.Request{}); got != ci.PriorityNormal {
		t.Errorf("a request with no repo or ref ranks %d, want %d", got, ci.PriorityNormal)
	}
	// refs/heads/ or not, it is the same branch -- a caller that has not
	// been through Request.normalize must not silently lose its rank.
	if got := rank(ci.Request{Repo: repo.String(), Ref: "refs/heads/" + first.BranchName()}); got != ci.PriorityMergeHead {
		t.Errorf("a queued change spelled as a ref ranks %d, want %d", got, ci.PriorityMergeHead)
	}

	// A change the queue has given up on merges nothing on its verdict,
	// and loses its rank at once rather than after a stale cache TTL.
	now := time.Date(2026, 9, 19, 12, 1, 0, 0, time.UTC)
	if err := store.Observe(ctx, model.Observation{
		TaskID: first.ID, CompletedAt: &now, MergeQueueBlockedAt: &now,
	}); err != nil {
		t.Fatalf("blocking %s: %v", first.ID, err)
	}
	if got := rank(ci.Request{Repo: repo.String(), Ref: first.BranchName()}); got != ci.PriorityNormal {
		t.Errorf("a blocked change ranks %d, want %d", got, ci.PriorityNormal)
	}
	if got := rank(ci.Request{Repo: repo.String(), Ref: second.BranchName()}); got != ci.PriorityMergeHead {
		t.Errorf("the change still queued ranks %d, want %d", got, ci.PriorityMergeHead)
	}
}

// queuedTaskLinkedToAPullRequest is a task in its repo's merge queue as
// the store records one: asked to be merged, its run over, and linked to
// an open pull request. No GitHub here -- MergeQueueHeads reads the queue
// out of the store alone.
func queuedTaskLinkedToAPullRequest(t *testing.T, ctx context.Context, store *model.Store,
	id string, repo model.RepoRef, pr int) model.Task {

	t.Helper()
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	task := model.Task{
		ID: id, Intent: model.IntentImplement, Title: "do the thing", Body: "please",
		Origin:    model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval:  &model.Attribution{Actor: human},
		Target:    &repo,
		Binding:   model.BindingDirective,
		AutoMerge: true,
		Links: []model.Link{{
			Kind: model.LinkFixes, Target: model.PullRequestRef{Repo: repo, Number: pr}.String(),
		}},
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("filing task %s: %v", id, err)
	}
	if err := store.Observe(ctx, model.Observation{TaskID: id, CompletedAt: &now}); err != nil {
		t.Fatalf("recording task %s as finished: %v", id, err)
	}
	return task
}
