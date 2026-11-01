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
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type simFixture struct {
	sim    *githubsim.Sim
	client *github.RESTClient
	repo   model.RepoRef
}

func newBatchFixture(t *testing.T) *simFixture {
	t.Helper()
	sim, client := newSim(t, "acme", "widgets", "main")
	return &simFixture{sim: sim, client: client, repo: model.RepoRef{Owner: "acme", Name: "widgets"}}
}

func shortSHAOf(t *testing.T, bareRepo, branch string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", bareRepo, "rev-parse", "refs/heads/"+branch).CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse %s: %v\n%s", branch, err, out)
	}
	sha := strings.TrimSpace(string(out))
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func mustBatch(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef) *model.MergeBatch {
	t.Helper()
	batch, err := store.GetMergeBatch(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if batch == nil {
		t.Fatal("expected a re-check row in flight on main, found none")
	}
	return batch
}

// The overlap re-check (mergerecheck.go, docs/merge-queue-behind.md "The
// overlap re-check"), end to end through the merge queue: a change green
// on its own head, `main` moved on underneath it, and what each mode does
// about that.

// recheckFixture is one queued change that touches change.go and holds a
// green grain CI verdict on its own head, in a repository set to mode,
// with landed then pushed straight onto main.
func recheckFixture(t *testing.T, mode model.MergeRecheckMode, landed string) (
	context.Context, *model.Store, *simFixture, *fakeCI, string) {

	t.Helper()
	store, ctx := openStore(t)
	fix := newBatchFixture(t)
	if mode != "" {
		if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: fix.repo, MergeRecheck: mode}); err != nil {
			t.Fatal(err)
		}
	}
	runner := newFakeCI()
	task, branch := queuedTaskWithPullRequest(t, ctx, store, fix.sim, fix.client, "t1", fix.repo)
	pushFileToBranch(t, fix.sim.BareRepo, branch, "change.go", "package widgets", "the change")
	task = grantCI(t, ctx, store, task)
	greenGrainCI(t, ctx, store, fix.client, runner, task, fix.repo, branch)
	pushFileToBranch(t, fix.sim.BareRepo, "main", landed, "landed meanwhile", "somebody else's change")
	setMergeable(fix.sim, true)
	return ctx, store, fix, runner, branch
}

func recheckRecords(t *testing.T, ctx context.Context, store *model.Store) []model.MergeRecheckRecord {
	t.Helper()
	records, err := store.MergeRecheckRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return records
}

// Two independent green changes in different packages both merge in the same
// cycle with no lock and no extra CI run.
func TestTwoIndependentGreenChangesBothMergeInSameCycleWithNoLock(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	fix := newBatchFixture(t)
	runner := newFakeCI()

	t1, b1 := queuedTaskWithPullRequest(t, ctx, store, fix.sim, fix.client, "t1", fix.repo)
	pushFileToBranch(t, fix.sim.BareRepo, b1, "pkg/alpha/a.go", "package alpha", "alpha")
	t1 = grantCI(t, ctx, store, t1)
	greenGrainCI(t, ctx, store, fix.client, runner, t1, fix.repo, b1)

	t2, b2 := queuedTaskWithPullRequest(t, ctx, store, fix.sim, fix.client, "t2", fix.repo)
	pushFileToBranch(t, fix.sim.BareRepo, b2, "pkg/beta/b.go", "package beta", "beta")
	t2 = grantCI(t, ctx, store, t2)
	greenGrainCI(t, ctx, store, fix.client, runner, t2, fix.repo, b2)

	setMergeable(fix.sim, true)
	before := runner.started()

	syncWithCI(t, ctx, store, fix.client, runner, baseTime.Add(time.Minute))

	if !mergedBranch(fix.sim, b1) || !mergedBranch(fix.sim, b2) {
		t.Fatalf("merged b1=%v b2=%v, want both independent green changes merged in the same cycle",
			mergedBranch(fix.sim, b1), mergedBranch(fix.sim, b2))
	}
	if got := runner.started() - before; got != 0 {
		t.Fatalf("merging two independent green changes started %d grain CI run(s), want 0", got)
	}
}

// Enforce (and the default "") when nothing the change touches has been
// touched since: it merges on the green it already holds, with no second suite.
func TestEnforceMergesANonOverlappingChangeWithNoRecheck(t *testing.T) {
	t.Parallel()
	for _, mode := range []model.MergeRecheckMode{"", model.MergeRecheckEnforce} {
		ctx, store, fix, runner, branch := recheckFixture(t, mode, "NOTES.md")
		before := runner.started()

		syncWithCI(t, ctx, store, fix.client, runner, baseTime.Add(time.Minute))

		if !mergedBranch(fix.sim, branch) {
			t.Fatalf("mode %q: a change that overlaps nothing landed since its run did not merge on its own green", mode)
		}
		if got := runner.started() - before; got != 0 {
			t.Fatalf("mode %q: merging a non-overlapping change started %d grain CI run(s), want none", mode, got)
		}
		records := recheckRecords(t, ctx, store)
		if len(records) != 1 {
			t.Fatalf("mode %q: %d re-check records, want 1: %+v", mode, len(records), records)
		}
		r := records[0]
		if r.Triggered || r.Rechecked || r.Mode != model.MergeRecheckEnforce || r.Fork == r.Tip {
			t.Fatalf("mode %q: record = %+v, want an untriggered, unrechecked enforce merge with main ahead of its fork", mode, r)
		}
	}
}

// Enforce, and main has moved in the same Go package: the change is not
// merged on its own green but re-checked on top of main, one change at a
// time, and lands when that passes.
func TestEnforceRechecksAnOverlappingChangeBeforeItMerges(t *testing.T) {
	t.Parallel()
	ctx, store, fix, runner, branch := recheckFixture(t, "", "landed.go")
	before := runner.started()

	syncWithCI(t, ctx, store, fix.client, runner, baseTime.Add(time.Minute))

	if mergedBranch(fix.sim, branch) {
		t.Fatal("a change overlapping what landed since its run merged without a re-check")
	}
	batch := mustBatch(t, ctx, store, fix.repo)
	if len(batch.Members) != 1 || batch.Members[0].TaskID != "t1" || batch.Splits != 0 {
		t.Fatalf("the re-check is %+v, want a single-change re-check of t1", batch)
	}
	if got := runner.started() - before; got != 1 {
		t.Fatalf("the re-check started %d grain CI run(s), want 1", got)
	}
	if state, _ := mergeQueueStateOf(t, ctx, store, "t1"); state != "waiting_ci" {
		t.Fatalf("t1's row reads %q while it is re-checked, want waiting_ci", state)
	}

	// Nothing else happens while it runs: another cycle starts nothing
	// more and merges nothing.
	syncWithCI(t, ctx, store, fix.client, runner, baseTime.Add(2*time.Minute))
	if mergedBranch(fix.sim, branch) || runner.started()-before != 1 {
		t.Fatal("the change merged, or another run started, while its re-check was still going")
	}

	runner.finish(batch.RunID, ci.ConclusionSuccess)
	syncWithCI(t, ctx, store, fix.client, runner, baseTime.Add(3*time.Minute))
	if !mergedBranch(fix.sim, branch) {
		t.Fatal("the change did not merge once its re-check passed")
	}
	records := recheckRecords(t, ctx, store)
	if len(records) != 1 {
		t.Fatalf("%d re-check records, want 1: %+v", len(records), records)
	}
	r := records[0]
	if !r.Triggered || !r.Rechecked || r.Caught || !r.HasClause(model.MergeRecheckClauseSamePackage) {
		t.Fatalf("record = %+v, want a triggered same-package merge that was re-checked", r)
	}
}

// Enforce, and the re-check fails: the change is green on its own head
// and red on top of main, which is the catch the rule is for. It is sent
// straight to repair with no flake recheck, and when its repair budget is
// spent it goes to merge_failed in the Inbox.
func TestEnforceRecheckCatchesAChangeRedOnTopOfItsBranch(t *testing.T) {
	t.Parallel()
	ctx, store, fix, runner, branch := recheckFixture(t, model.MergeRecheckEnforce, "landed.go")
	before := runner.started()

	syncWithCI(t, ctx, store, fix.client, runner, baseTime.Add(time.Minute))
	batch := mustBatch(t, ctx, store, fix.repo)
	failing := ci.JobRun{WorkflowPath: ".grain/workflows/tests.yml", JobID: "go", Name: "go",
		Conclusion: ci.ConclusionFailure}
	runner.finish(batch.RunID, ci.ConclusionFailure, failing)
	syncWithCI(t, ctx, store, fix.client, runner, baseTime.Add(2*time.Minute))

	if got := runner.started() - before; got != 1 {
		t.Fatalf("overlap re-check ran %d grain CI run(s), want 1 (no batch-flake recheck)", got)
	}
	if mergedBranch(fix.sim, branch) {
		t.Fatal("a change whose re-check failed merged")
	}
	obs, err := store.GetObservation(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if obs.MergeQueueRepairAt == nil {
		t.Fatal("the change its re-check caught was not sent back for repair")
	}
	records := recheckRecords(t, ctx, store)
	if len(records) != 1 || !records[0].Triggered || !records[0].Rechecked || !records[0].Caught {
		t.Fatalf("records = %+v, want one triggered merge re-checked and caught", records)
	}

	// If repairs are already spent when the re-check fails, it escalates to merge_failed (Inbox).
	ctx2, store2, fix2, runner2, _ := recheckFixture(t, model.MergeRecheckEnforce, "landed.go")
	repairAt := baseTime.Add(-time.Hour)
	if err := store2.ObserveField(ctx2, "t1", repairAt, func(o *model.Observation) {
		o.MergeQueueRepairAt = &repairAt
		o.MergeQueueRepairs = 2
	}); err != nil {
		t.Fatal(err)
	}
	syncWithCI(t, ctx2, store2, fix2.client, runner2, baseTime.Add(time.Minute))
	batch2 := mustBatch(t, ctx2, store2, fix2.repo)
	runner2.finish(batch2.RunID, ci.ConclusionFailure, failing)
	syncWithCI(t, ctx2, store2, fix2.client, runner2, baseTime.Add(2*time.Minute))

	obs2, err := store2.GetObservation(ctx2, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if obs2.MergeQueueBlockedAt == nil || obs2.MergeQueueState != "merge_failed" {
		t.Fatalf("after repairs are spent, obs = %+v, want MergeQueueBlockedAt set and state merge_failed", obs2)
	}
}

// Observe changes nothing: the overlapping change merges on its own green
// with no second suite, and the record says the rule would have fired.
func TestObserveModeChangesNoMergeAndRecordsTheRule(t *testing.T) {
	t.Parallel()
	ctx, store, fix, runner, branch := recheckFixture(t, model.MergeRecheckObserve, "landed.go")
	before := runner.started()

	syncWithCI(t, ctx, store, fix.client, runner, baseTime.Add(time.Minute))

	if !mergedBranch(fix.sim, branch) {
		t.Fatal("observe held back a change the queue would have merged")
	}
	if got := runner.started() - before; got != 0 {
		t.Fatalf("observe started %d grain CI run(s), want none", got)
	}
	if row, _ := store.GetMergeBatch(ctx, fix.repo, "main"); row != nil {
		t.Fatalf("observe formed a re-check: %+v", row)
	}
	records := recheckRecords(t, ctx, store)
	if len(records) != 1 {
		t.Fatalf("%d re-check records, want 1", len(records))
	}
	r := records[0]
	if !r.Triggered || r.Rechecked || r.Mode != model.MergeRecheckObserve ||
		!r.HasClause(model.MergeRecheckClauseSamePackage) {
		t.Fatalf("record = %+v, want an observed, unrechecked same-package trigger", r)
	}
}

// Off is the queue without overlap re-checking, and records nothing.
func TestOffModeRecordsNothing(t *testing.T) {
	t.Parallel()
	ctx, store, fix, runner, branch := recheckFixture(t, model.MergeRecheckOff, "landed.go")

	syncWithCI(t, ctx, store, fix.client, runner, baseTime.Add(time.Minute))

	if !mergedBranch(fix.sim, branch) {
		t.Fatal("off held back a change the queue would have merged")
	}
	if records := recheckRecords(t, ctx, store); len(records) != 0 {
		t.Fatalf("off recorded %d re-check decision(s), want none", len(records))
	}
}

// Each of the three overlap clauses (same-file, same-package, global-input)
// triggers a re-check under enforce and records the clause under observe.
func TestAllOverlapClausesUnderObserveAndEnforce(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		prFile     string
		landedFile string
		wantClause string
	}{
		{"same-file", "pkg/alpha/a.go", "pkg/alpha/a.go", model.MergeRecheckClauseSameFile},
		{"same-package", "pkg/alpha/a.go", "pkg/alpha/b.go", model.MergeRecheckClauseSamePackage},
		{"global-input-gomod", "pkg/alpha/a.go", "go.mod", model.MergeRecheckClauseGlobalInput},
		{"global-input-workflow", "pkg/alpha/a.go", ".grain/workflows/ci.yml", model.MergeRecheckClauseGlobalInput},
		{"global-input-makefile", "pkg/alpha/a.go", "Makefile", model.MergeRecheckClauseGlobalInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, mode := range []model.MergeRecheckMode{model.MergeRecheckObserve, model.MergeRecheckEnforce} {
				store, ctx := openStore(t)
				fix := newBatchFixture(t)
				if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: fix.repo, MergeRecheck: mode}); err != nil {
					t.Fatal(err)
				}
				runner := newFakeCI()
				if tc.prFile == tc.landedFile {
					pushFileToBranch(t, fix.sim.BareRepo, "main", tc.prFile,
						"package alpha\n\nfunc One() int { return 1 }\n\nfunc Mid() int { return 2 }\n\nfunc End() int { return 3 }",
						"seed shared file")
				}
				task, branch := queuedTaskWithPullRequest(t, ctx, store, fix.sim, fix.client, "t1", fix.repo)
				prContent := "package alpha\n\nfunc One() int { return 10 }\n\nfunc Mid() int { return 2 }\n\nfunc End() int { return 3 }"
				landedContent := "package alpha\n\nfunc One() int { return 1 }\n\nfunc Mid() int { return 2 }\n\nfunc End() int { return 30 }"
				pushFileToBranch(t, fix.sim.BareRepo, branch, tc.prFile, prContent, "pr change")
				task = grantCI(t, ctx, store, task)
				greenGrainCI(t, ctx, store, fix.client, runner, task, fix.repo, branch)
				pushFileToBranch(t, fix.sim.BareRepo, "main", tc.landedFile, landedContent, "landed on main")
				setMergeable(fix.sim, true)

				syncWithCI(t, ctx, store, fix.client, runner, baseTime.Add(time.Minute))
				if mode == model.MergeRecheckObserve {
					if !mergedBranch(fix.sim, branch) {
						t.Fatalf("observe (%s): did not merge directly", tc.name)
					}
				} else {
					if mergedBranch(fix.sim, branch) {
						t.Fatalf("enforce (%s): merged without recheck", tc.name)
					}
					batch := mustBatch(t, ctx, store, fix.repo)
					runner.finish(batch.RunID, ci.ConclusionSuccess)
					syncWithCI(t, ctx, store, fix.client, runner, baseTime.Add(2*time.Minute))
					if !mergedBranch(fix.sim, branch) {
						t.Fatalf("enforce (%s): did not merge after recheck passed", tc.name)
					}
				}
				records := recheckRecords(t, ctx, store)
				if len(records) != 1 || !records[0].Triggered || !records[0].HasClause(tc.wantClause) {
					t.Fatalf("mode %s (%s): records = %+v, want triggered with clause %q", mode, tc.name, records, tc.wantClause)
				}
				if gotRechecked := (mode == model.MergeRecheckEnforce); records[0].Rechecked != gotRechecked {
					t.Fatalf("mode %s (%s): rechecked = %v, want %v", mode, tc.name, records[0].Rechecked, gotRechecked)
				}
			}
		})
	}
}

// When the post-merge base-branch CI run finishes, collectBaseBranchCI calls
// JudgeMergeRechecks to stamp base_verdict ('green' or 'red') on every
// unjudged merge_recheck_history row whose merge landed at or before the
// base CI run started.
func TestPostMergeBaseBranchCIJudgesMergeRecheckRecords(t *testing.T) {
	t.Parallel()
	ctx, store, fix, runner, branch := recheckFixture(t, model.MergeRecheckObserve, "NOTES.md")
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: fix.repo, CIMode: model.CIModeGrain, MergeRecheck: model.MergeRecheckObserve,
	}); err != nil {
		t.Fatal(err)
	}

	// Merge t1 under observe (unrechecked).
	mergeAt := baseTime.Add(time.Minute)
	syncWithCI(t, ctx, store, fix.client, runner, mergeAt)
	if !mergedBranch(fix.sim, branch) {
		t.Fatal("t1 did not merge")
	}
	records := recheckRecords(t, ctx, store)
	if len(records) != 1 || records[0].BaseVerdict != "" {
		t.Fatalf("before post-merge CI finishes, records = %+v, want empty BaseVerdict", records)
	}

	// Start and fail the post-merge base branch CI run on main.
	runStartAt := mergeAt.Add(time.Minute)
	if err := runBaseBranchCIReconciler(t, ctx, store, fix.client, runner, runStartAt); err != nil {
		t.Fatal(err)
	}
	baseRow, err := store.GetBaseBranchCI(ctx, fix.repo, "main")
	if err != nil || baseRow == nil || baseRow.RunID == "" {
		t.Fatalf("base branch CI row = %+v (%v), want run in flight", baseRow, err)
	}
	runner.finish(baseRow.RunID, ci.ConclusionFailure, ci.JobRun{
		WorkflowPath: ".grain/workflows/tests.yml", JobID: "go", Name: "go", Conclusion: ci.ConclusionFailure,
	})
	if err := runBaseBranchCIReconciler(t, ctx, store, fix.client, runner, runStartAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	records = recheckRecords(t, ctx, store)
	if len(records) != 1 || records[0].BaseVerdict != model.MergeRecheckBaseRed {
		t.Fatalf("after red post-merge CI, records = %+v, want BaseVerdict=%q", records, model.MergeRecheckBaseRed)
	}
	stats := model.SummarizeMergeRechecks(records)
	if stats.SkippedBaseRed != 1 {
		t.Fatalf("SkippedBaseRed = %d, want 1", stats.SkippedBaseRed)
	}
}

// If a change's auto-merge is withdrawn while its overlap re-check is in flight,
// the completed re-check discards the row rather than merging the pull request.
func TestEnforceRecheckDiscardsWhenAutoMergeWithdrawnMidRun(t *testing.T) {
	t.Parallel()
	ctx, store, fix, runner, branch := recheckFixture(t, "", "landed.go")

	syncWithCI(t, ctx, store, fix.client, runner, baseTime.Add(time.Minute))
	batch := mustBatch(t, ctx, store, fix.repo)

	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("GetTask(t1): %+v, %v", task, err)
	}
	task.AutoMerge = false
	if err := store.PutTask(ctx, *task); err != nil {
		t.Fatal(err)
	}

	runner.finish(batch.RunID, ci.ConclusionSuccess)
	syncWithCI(t, ctx, store, fix.client, runner, baseTime.Add(2*time.Minute))

	if mergedBranch(fix.sim, branch) {
		t.Fatal("the change merged after its auto-merge was withdrawn during an overlap re-check")
	}
	if row, err := store.GetMergeBatch(ctx, fix.repo, "main"); err != nil || row != nil {
		t.Fatalf("re-check row after discard = %+v (%v), want nil", row, err)
	}
}

// A push landing between the cycle that cleared a change for merging and
// the re-check it starts is not adopted: what passed the security hold,
// the approvals and its own grain CI was the earlier commit, and a
// passing re-check merges on its own word, so re-checking the new head
// would merge a commit nothing cleared.
func TestEnforceRecheckRefusesAHeadPushedAfterItWasCleared(t *testing.T) {
	t.Parallel()
	ctx, store, fix, runner, branch := recheckFixture(t, "", "landed.go")
	if len(fix.sim.PullRequests) != 1 {
		t.Fatalf("%d pull requests in the sim, want 1", len(fix.sim.PullRequests))
	}
	number := fix.sim.PullRequests[0].Number
	cleared, err := fix.client.GetPullRequest(fix.repo.Owner, fix.repo.Name, number)
	if err != nil {
		t.Fatal(err)
	}
	pushFileToBranch(t, fix.sim.BareRepo, branch, "change.go", "package widgets // later", "an uncleared push")
	before := runner.started()

	started, err := orchestrator.StartMergeRecheckForTest(ctx, store, fix.client, runner,
		fix.repo, "main", "t1", number, cleared.HeadSHA, baseTime.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if started || runner.started() != before {
		t.Fatal("a re-check started for a head pushed after the queue cleared the change")
	}
	if row, err := store.GetMergeBatch(ctx, fix.repo, "main"); err != nil || row != nil {
		t.Fatalf("re-check row = %+v (%v), want none", row, err)
	}

	moved, err := fix.client.GetPullRequest(fix.repo.Owner, fix.repo.Name, number)
	if err != nil {
		t.Fatal(err)
	}
	started, err = orchestrator.StartMergeRecheckForTest(ctx, store, fix.client, runner,
		fix.repo, "main", "t1", number, moved.HeadSHA, baseTime.Add(2*time.Minute))
	if err != nil || !started {
		t.Fatalf("re-check of the head that was cleared: started=%v err=%v, want started", started, err)
	}
	if got := mustBatch(t, ctx, store, fix.repo).Members[0].HeadSHA; got != moved.HeadSHA {
		t.Fatalf("re-check pinned to %s, want %s", got, moved.HeadSHA)
	}
}

// When an overlap re-check CI run concludes errored (for instance when no
// workflow answers pull_request on the merge-queue ref, or when a job cannot
// acquire a sandbox), SyncPullRequests hands the task to a person with the CI
// error surfaced, clears the queue for later changes, and does not spin
// creating a fresh merge batch on the same or subsequent sync ticks.
func TestMergeRecheckErroredDoesNotSpinInAnInfiniteLoop(t *testing.T) {
	t.Parallel()

	t.Run("unmatched-workflow-error-and-ci-reconciler", func(t *testing.T) {
		t.Parallel()
		ctx, store, fix, runner, branch := recheckFixture(t, model.MergeRecheckEnforce, "landed.go")

		// Queue a second, non-overlapping green change behind t1 to verify it
		// is no longer held back once t1's errored re-check hands t1 to a person.
		t2, b2 := queuedTaskWithPullRequest(t, ctx, store, fix.sim, fix.client, "t2", fix.repo)
		pushFileToBranch(t, fix.sim.BareRepo, b2, "docs/notes.md", "notes", "unrelated docs")
		t2 = grantCI(t, ctx, store, t2)
		greenGrainCI(t, ctx, store, fix.client, runner, t2, fix.repo, b2)
		setMergeable(fix.sim, true)

		before := runner.started()
		t0 := baseTime.Add(time.Minute)
		syncWithCI(t, ctx, store, fix.client, runner, t0)

		batch := mustBatch(t, ctx, store, fix.repo)
		if got := runner.started() - before; got != 1 {
			t.Fatalf("started %d grain CI run(s), want 1", got)
		}

		erroredRun, ok := runner.Get(batch.RunID)
		if !ok {
			t.Fatalf("missing merge-recheck run %s", batch.RunID)
		}
		finishedAt := t0.Add(5 * time.Second)
		erroredRun.Conclusion = ci.ConclusionErrored
		erroredRun.FinishedAt = &finishedAt
		erroredRun.Error = "ci: no workflow of acme/widgets answers pull_request of branch " + batch.Ref
		runner.remember(erroredRun)

		// Advance across several 20s sync ticks and past ciRetryDelay with the
		// task CI reconciler running alongside SyncPullRequests.
		for i, dt := range []time.Duration{
			20 * time.Second,
			40 * time.Second,
			60 * time.Second,
			10 * time.Minute,
			20 * time.Minute,
		} {
			tick := t0.Add(dt)
			if err := runCIReconcilerAt(t, ctx, store, fix.client, runner, tick); err != nil {
				t.Fatalf("tick %d: runCIReconcilerAt: %v", i, err)
			}
			syncWithCI(t, ctx, store, fix.client, runner, tick)
			if got := runner.started() - before; got != 1 {
				t.Fatalf("tick %d (%s): started %d merge-recheck/CI run(s), want 1 (infinite re-check loop)",
					i, dt, got)
			}
			if row, err := store.GetMergeBatch(ctx, fix.repo, "main"); err != nil || row != nil {
				t.Fatalf("tick %d: GetMergeBatch = %+v (%v), want nil", i, row, err)
			}
		}

		if mergedBranch(fix.sim, branch) {
			t.Fatal("t1 merged after its overlap re-check errored")
		}
		if !mergedBranch(fix.sim, b2) {
			t.Fatal("t2 stayed blocked behind t1 after t1's overlap re-check errored")
		}
		if repairInFlightNow(t, ctx, store, "t1") {
			t.Fatal("t1 was sent for an automatic repair on an errored overlap re-check")
		}
		obs, err := store.GetObservation(ctx, "t1")
		if err != nil {
			t.Fatal(err)
		}
		if obs.RepairsSpent() != 0 {
			t.Fatalf("RepairsSpent = %d, want 0", obs.RepairsSpent())
		}
		if obs.MergeQueueBlockedAt == nil || obs.MergeQueueState != "merge_failed" {
			t.Fatalf("obs = %+v, want MergeQueueBlockedAt set and state merge_failed", obs)
		}
		if !strings.Contains(obs.MergeQueueNote, "no workflow of acme/widgets answers pull_request") {
			t.Fatalf("MergeQueueNote = %q, want errored run detail surfaced", obs.MergeQueueNote)
		}
		bodies := commentBodies(t, ctx, store, "t1")
		if len(bodies) == 0 || !strings.Contains(bodies[len(bodies)-1], "no workflow of acme/widgets answers pull_request") {
			t.Fatalf("comments = %q, want comment surfacing why the overlap re-check errored", bodies)
		}
		for _, r := range recheckRecords(t, ctx, store) {
			if r.TaskID == "t1" {
				t.Fatalf("unexpected merge re-check history record for errored run on t1: %+v", r)
			}
		}

		// Once the operator fixes the workflow and presses "Retry merge", t1
		// re-enters the queue cleanly and merges when the fresh re-check passes.
		uiClient := ui.NewClient(ui.Config{Actor: ui.DefaultActor("operator"), GitHubPullRequests: fix.client}, store)
		if err := uiClient.RetryMerge(ctx, "t1"); err != nil {
			t.Fatalf("RetryMerge(t1): %v", err)
		}
		retryTick := t0.Add(25 * time.Minute)
		if err := runCIReconcilerAt(t, ctx, store, fix.client, runner, retryTick); err != nil {
			t.Fatal(err)
		}
		headStatus := ciStatusOf(t, ctx, store, "t1")
		if headStatus == nil || headStatus.RunID == "" {
			t.Fatalf("after RetryMerge, head CI status = %+v, want fresh run", headStatus)
		}
		runner.finish(headStatus.RunID, ci.ConclusionSuccess)
		if err := runCIReconcilerAt(t, ctx, store, fix.client, runner, retryTick.Add(10*time.Second)); err != nil {
			t.Fatal(err)
		}
		syncWithCI(t, ctx, store, fix.client, runner, retryTick.Add(20*time.Second))
		retryBatch := mustBatch(t, ctx, store, fix.repo)
		// The retry costs exactly one more re-check (plus the head run
		// RetryMerge asks for), however many cycles it takes to merge.
		if got := runner.started() - before; got != 3 {
			t.Fatalf("after RetryMerge started %d grain CI run(s) in all, want 3: the errored re-check, "+
				"the retried head and exactly one more re-check", got)
		}
		runner.finish(retryBatch.RunID, ci.ConclusionSuccess)
		syncWithCI(t, ctx, store, fix.client, runner, retryTick.Add(40*time.Second))
		if !mergedBranch(fix.sim, branch) {
			t.Fatal("t1 did not merge after RetryMerge and a green overlap re-check")
		}
		if got := runner.started() - before; got != 3 {
			t.Fatalf("merging after RetryMerge started %d grain CI run(s) in all, want 3", got)
		}
	})

	t.Run("explicit-enforce-without-task-grain-ci", func(t *testing.T) {
		t.Parallel()
		store, ctx := openStore(t)
		fix := newBatchFixture(t)
		if err := store.PutRepoConfig(ctx, model.RepoConfig{
			Repo:         fix.repo,
			CIMode:       model.CIModeGitHub,
			MergeRecheck: model.MergeRecheckEnforce,
		}); err != nil {
			t.Fatal(err)
		}
		runner := newFakeCI()
		_, branch := queuedTaskWithPullRequest(t, ctx, store, fix.sim, fix.client, "t1", fix.repo)
		pushFileToBranch(t, fix.sim.BareRepo, branch, "change.go", "package widgets", "the change")
		greenOnGitHub(fix.sim, branch)
		pushFileToBranch(t, fix.sim.BareRepo, "main", "landed.go", "package widgets", "landed meanwhile")
		setMergeable(fix.sim, true)

		before := runner.started()
		syncWithCI(t, ctx, store, fix.client, runner, baseTime.Add(time.Minute))
		batch := mustBatch(t, ctx, store, fix.repo)
		runner.finish(batch.RunID, ci.ConclusionErrored, ci.JobRun{
			WorkflowPath: ".grain/workflows/tests.yml",
			JobID:        "go",
			Name:         "go",
			Conclusion:   ci.ConclusionErrored,
			Error:        "no sandbox capacity",
		})

		for i := 2; i <= 5; i++ {
			syncWithCI(t, ctx, store, fix.client, runner, baseTime.Add(time.Duration(i)*time.Minute))
			if got := runner.started() - before; got != 1 {
				t.Fatalf("sync %d: started %d grain CI run(s), want 1", i, got)
			}
		}
		obs, err := store.GetObservation(ctx, "t1")
		if err != nil {
			t.Fatal(err)
		}
		if obs.MergeQueueBlockedAt == nil || obs.MergeQueueState != "merge_failed" {
			t.Fatalf("obs = %+v, want MergeQueueBlockedAt set and state merge_failed", obs)
		}
	})
}
