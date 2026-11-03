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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// docs/test-plans/2026-11-01.md §10 item 2 (PR #218): a task carrying two
// on-target pull requests, both touching a trust boundary, both green and
// mergeable, driven through ten full cycles of the security-hold and merge
// queue steps. The hold is about one of them; it files one review, and
// neither the hold nor the clearance flips from cycle to cycle -- before
// #218 each cycle classified both in turn and every flip could file
// another review.
func TestTwoOnTargetPullRequestsHoldOnceAndStayStableAcrossTenCycles(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	// The second pull request, opened first from another branch the task
	// also pushed, so the task's own (model.TaskPullRequest: the highest
	// number) is the one ProcessResult opens below.
	other := "grain/t1-extra"
	pushBranch(t, sim.BareRepo, other)
	pushFile(t, sim.BareRepo, other, "pkg/gitproxy/proxy.go", "the other branch\n")
	extra, err := client.CreatePullRequest("acme", "widgets", other, "main", "the task's other change", "")
	if err != nil {
		t.Fatal(err)
	}

	task := finishTouching(t, ctx, store, sim, "t1", "pkg/gitproxy/proxy.go", true)
	task.Links = append(task.Links, model.Link{Kind: model.LinkFixes,
		Target: model.PullRequestRef{Repo: repo, Number: extra.Number}.String()})
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	held := reloaded(t, ctx, store, task.ID)
	var fixes []string
	for _, l := range held.Links {
		if l.Kind == model.LinkFixes {
			fixes = append(fixes, l.Target)
		}
	}
	if len(fixes) != 2 {
		t.Fatalf("task links %d pull requests (%v), want both", len(fixes), fixes)
	}
	reviewID := linkOf(held, model.LinkSecurityReviewTask)
	if reviewID == "" {
		t.Fatalf("no security review filed: %+v", held.Links)
	}
	setMergeable(sim, true)
	sim.CheckRuns[model.BranchName(task.ID)] = greenCheck
	sim.CheckRuns[other] = greenCheck

	hold, cleared := linkOf(held, model.LinkSecurityHold), linkOf(held, model.LinkTrustBoundaryCleared)
	for i := 1; i <= 10; i++ {
		now := baseTime.Add(time.Duration(i) * time.Minute)
		if err := orchestrator.SyncSecurityHolds(ctx, store, client, now); err != nil {
			t.Fatalf("cycle %d: SyncSecurityHolds: %v", i, err)
		}
		if err := orchestrator.SyncPullRequests(ctx, store, client, now); err != nil {
			t.Fatalf("cycle %d: SyncPullRequests: %v", i, err)
		}
		got := reloaded(t, ctx, store, task.ID)
		if h, c := linkOf(got, model.LinkSecurityHold), linkOf(got, model.LinkTrustBoundaryCleared); h != hold || c != cleared {
			t.Fatalf("cycle %d: hold/clearance flipped from %q/%q to %q/%q", i, hold, cleared, h, c)
		}
		if r := linkOf(got, model.LinkSecurityReviewTask); r != reviewID {
			t.Fatalf("cycle %d: security review link moved from %s to %s", i, reviewID, r)
		}
		if reviews := securityReviewsOf(t, ctx, store, task.ID); len(reviews) != 1 {
			t.Fatalf("cycle %d: %d security reviews filed, want 1", i, len(reviews))
		}
		if mergedBranch(sim, other) || mergedBranch(sim, model.BranchName(task.ID)) {
			t.Fatalf("cycle %d: a held trust-boundary change merged", i)
		}
	}

	// The review finishes clean: the hold is released for the task's own
	// pull request, which merges on that clearance. The other is not
	// merged on it (fail closed) and files no second review, however many
	// cycles go by.
	completeReview(t, ctx, store, reviewID)
	own := model.BranchName(task.ID)
	for i := 11; i <= 20; i++ {
		now := baseTime.Add(time.Hour + time.Duration(i)*time.Minute)
		if err := orchestrator.SyncSecurityHolds(ctx, store, client, now); err != nil {
			t.Fatalf("cycle %d: SyncSecurityHolds: %v", i, err)
		}
		if err := orchestrator.SyncPullRequests(ctx, store, client, now); err != nil {
			t.Fatalf("cycle %d: SyncPullRequests: %v", i, err)
		}
		if reviews := securityReviewsOf(t, ctx, store, task.ID); len(reviews) != 1 {
			t.Fatalf("cycle %d: %d security reviews filed, want 1", i, len(reviews))
		}
		if mergedBranch(sim, other) {
			t.Fatalf("cycle %d: the other pull request merged on the task's own clearance", i)
		}
	}
	if !mergedBranch(sim, own) {
		got := reloaded(t, ctx, store, task.ID)
		t.Fatalf("the task's own pull request did not merge once its review was clean: automerge=%v links=%+v",
			got.AutoMerge, got.Links)
	}

	// One note for the hold and one for its release, both on the task's
	// own pull request, not one per cycle.
	var bodies []string
	for _, target := range fixes {
		ref, err := model.ParsePullRequestRef(target)
		if err != nil {
			t.Fatal(err)
		}
		comments, err := client.ListComments("acme", "widgets", ref.Number)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range comments {
			if strings.Contains(c.Body, reviewID) {
				bodies = append(bodies, c.Body)
			}
		}
	}
	if len(bodies) != 2 || !strings.Contains(bodies[0], "no longer merging") ||
		!strings.Contains(bodies[1], "finished without findings") {
		t.Fatalf("pull request comments naming the review = %q, want the hold and its release once each", bodies)
	}
}
