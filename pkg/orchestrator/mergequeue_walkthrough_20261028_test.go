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

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// docs/test-plans/2026-10-28.md §8, steps 1 and 2, through the real merge
// queue (SyncPullRequestsGuarding) over githubsim and a fake grain CI. The
// report is docs/test-runs/2026-10-28-merge-queue-security-review-submit.md.

// Two queued changes in the same Go package, both green on their own
// heads: no batch forms. The first merges on its own green; the second
// is re-checked on top of the first, alone, and merges once that passes.
func TestWalkthroughOverlappingChangesMergeOneAtATimeWithNoBatch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	fix := newBatchFixture(t)
	runner := newFakeCI()

	t1, b1 := queuedTaskWithPullRequest(t, ctx, store, fix.sim, fix.client, "t1", fix.repo)
	pushFileToBranch(t, fix.sim.BareRepo, b1, "pkg/alpha/a.go", "package alpha", "alpha a")
	t1 = grantCI(t, ctx, store, t1)
	greenGrainCI(t, ctx, store, fix.client, runner, t1, fix.repo, b1)

	t2, b2 := queuedTaskWithPullRequest(t, ctx, store, fix.sim, fix.client, "t2", fix.repo)
	pushFileToBranch(t, fix.sim.BareRepo, b2, "pkg/alpha/b.go", "package alpha", "alpha b")
	t2 = grantCI(t, ctx, store, t2)
	greenGrainCI(t, ctx, store, fix.client, runner, t2, fix.repo, b2)
	setMergeable(fix.sim, true)
	before := runner.started()

	syncWithCI(t, ctx, store, fix.client, runner, baseTime.Add(time.Minute))
	if !mergedBranch(fix.sim, b1) {
		t.Fatal("t1, green and overlapping nothing that landed since its run, did not merge")
	}
	if mergedBranch(fix.sim, b2) {
		t.Fatal("t2 merged on its own green although t1 had just landed in its package")
	}
	row := mustBatch(t, ctx, store, fix.repo)
	if len(row.Members) != 1 || row.Members[0].TaskID != "t2" {
		t.Fatalf("re-check row = %+v, want t2 alone: no batch of changes", row)
	}
	if got := runner.started() - before; got != 1 {
		t.Fatalf("%d grain CI run(s) started, want one re-check", got)
	}

	runner.finish(row.RunID, ci.ConclusionSuccess)
	syncWithCI(t, ctx, store, fix.client, runner, baseTime.Add(2*time.Minute))
	if !mergedBranch(fix.sim, b2) {
		t.Fatal("t2 did not merge once its re-check on top of t1 passed")
	}
	byTask := map[string]model.MergeRecheckRecord{}
	for _, r := range recheckRecords(t, ctx, store) {
		byTask[r.TaskID] = r
	}
	if r := byTask["t1"]; r.Triggered || r.Rechecked {
		t.Fatalf("t1's record = %+v, want untriggered", r)
	}
	if r := byTask["t2"]; !r.Triggered || !r.Rechecked || r.Caught || !r.HasClause(model.MergeRecheckClauseSamePackage) {
		t.Fatalf("t2's record = %+v, want a same-package trigger that was re-checked", r)
	}
}

// A push to a change while its re-check runs: the run judged the head the
// queue cleared, not the one pushed after, so its pass merges nothing.
func TestWalkthroughPushDuringRecheckIsNotMergedOnThatRecheck(t *testing.T) {
	t.Parallel()
	ctx, store, fix, runner, branch := recheckFixture(t, "", "landed.go")

	syncWithCI(t, ctx, store, fix.client, runner, baseTime.Add(time.Minute))
	row := mustBatch(t, ctx, store, fix.repo)
	cleared := row.Members[0].HeadSHA
	pushFileToBranch(t, fix.sim.BareRepo, branch, "change.go", "package widgets // pushed later", "an uncleared push")
	setMergeable(fix.sim, true)

	runner.finish(row.RunID, ci.ConclusionSuccess)
	syncWithCI(t, ctx, store, fix.client, runner, baseTime.Add(2*time.Minute))
	if mergedBranch(fix.sim, branch) {
		t.Fatalf("the head pushed after the queue cleared %s merged on the re-check of %s", branch, cleared)
	}
	if again, err := store.GetMergeBatch(ctx, fix.repo, "main"); err != nil {
		t.Fatal(err)
	} else if again != nil && again.Members[0].HeadSHA != cleared {
		t.Fatalf("the queue re-checked the uncleared head %s", again.Members[0].HeadSHA)
	}
}

// Step 2: a human presses Submit on a change held for it (auto-merge
// off), through ui.Client.Submit as the browser's button does, and a
// commit lands on the branch before the next merge cycle. What merges,
// if anything, is the head the human saw -- the pushed head has no green
// verdict of its own, so it does not merge on the one its parent held.
func TestWalkthroughSubmitThenPushDoesNotMergeTheUnseenHead(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	fix := newBatchFixture(t)
	runner := newFakeCI()
	task, branch := queuedTaskWithPullRequest(t, ctx, store, fix.sim, fix.client, "t1", fix.repo)
	pushFileToBranch(t, fix.sim.BareRepo, branch, "docs/a.md", "a", "the change")
	task = grantCI(t, ctx, store, task)
	greenGrainCI(t, ctx, store, fix.client, runner, task, fix.repo, branch)
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error { t.AutoMerge = false; return nil }); err != nil {
		t.Fatal(err)
	}
	setMergeable(fix.sim, true)
	syncWithCI(t, ctx, store, fix.client, runner, baseTime.Add(time.Minute))
	if mergedBranch(fix.sim, branch) {
		t.Fatal("a change held for Submit merged before anybody pressed it")
	}

	seen := prHeadOf(t, fix.client, reloaded(t, ctx, store, task.ID))
	client := ui.NewClient(ui.Config{Actor: ui.DefaultActor("operator"), GitHubPullRequests: fix.client}, store)
	if err := client.Submit(ctx, task.ID); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if got := model.HumanSubmittedHead(linkOf(reloaded(t, ctx, store, task.ID), model.LinkHumanSubmitted)); got != seen {
		t.Fatalf("Submit recorded head %q, want the one shown, %q", got, seen)
	}
	pushFileToBranch(t, fix.sim.BareRepo, branch, "docs/b.md", "b", "pushed after Submit")
	setMergeable(fix.sim, true)

	syncWithCI(t, ctx, store, fix.client, runner, baseTime.Add(2*time.Minute))
	if mergedBranch(fix.sim, branch) {
		t.Fatalf("the head pushed after Submit merged on the verdict of %s, the head the human saw", seen)
	}
}
