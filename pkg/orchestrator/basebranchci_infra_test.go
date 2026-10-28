package orchestrator_test

import (
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// Trunk's post-merge suite is a release gate (docs/design/release-from-trunk.md,
// RFT-2): it is ranked as one at the job gate, and a run of it that said
// nothing about the tree is re-run -- the jobs it had not passed, uncached
// -- rather than replayed in full from the cache.

// judgingRun puts main's row in flight on a run that has just ended the
// way a test says, at main's tip.
func judgingRun(t *testing.T, store *model.Store, runner *fakeCI, repo model.RepoRef, tip string,
	conclusion ci.Conclusion, jobs ...ci.JobRun) {

	t.Helper()
	ended := baseTime
	runner.remember(ci.Run{
		ID: "judge-1", Repo: repo.String(), Ref: "main", Commit: tip, Event: ci.EventPush,
		Conclusion: conclusion, Jobs: jobs, FinishedAt: &ended,
	})
	if err := store.PutBaseBranchCI(t.Context(), model.BaseBranchCI{
		Repo: repo, Branch: "main", RunID: "judge-1", Commit: tip,
		StartedAt: baseTime.Add(-time.Hour), MergedTaskID: "t0", Attempts: 1,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTrunksBaseBranchRunAsksForReleaseGatePriority(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{Repo: repo, Branch: "main", StartedAt: baseTime}); err != nil {
		t.Fatal(err)
	}
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime); err != nil {
		t.Fatal(err)
	}
	if got := runner.lastRequest(t).Priority; got != ci.PriorityReleaseGate {
		t.Fatalf("main's own run asked for priority %d, want PriorityReleaseGate (%d)", got, ci.PriorityReleaseGate)
	}
}

func TestACancelledBaseBranchRunReRunsOnlyWhatItHadNotPassedUncached(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	tip := headSHA(t, client, repo, "main")
	judgingRun(t, store, runner, repo, tip, ci.ConclusionCancelled,
		ci.JobRun{WorkflowPath: ".grain/workflows/tests.yml", JobID: "go", Conclusion: ci.ConclusionSuccess},
		ci.JobRun{WorkflowPath: ".grain/workflows/tests.yml", JobID: "ui-e2e", Conclusion: ci.ConclusionCancelled})

	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	req := runner.lastRequest(t)
	if want := []string{".grain/workflows/tests.yml:ui-e2e"}; !slices.Equal(req.Jobs, want) || !req.Recheck ||
		req.Commit != tip || req.Priority != ci.PriorityReleaseGate {
		t.Fatalf("the re-run asked for %+v, want %v uncached at main's tip as a release gate", req, want)
	}
	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || row == nil || row.Attempts != 2 || row.Broken() {
		t.Fatalf("main's row is %+v (%v), want attempt 2 in flight and nothing held", row, err)
	}
}

// A failure whose only failing job failed on a quota is an infra verdict:
// it holds no merge and files nothing while its re-runs last, and once they
// are spent it is the failure it is -- held, and sent to be confirmed.
func TestAnInfraFailureOnTrunkIsReRunBeforeItHoldsTheQueue(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	tip := headSHA(t, client, repo, "main")
	quota := ci.JobRun{
		WorkflowPath: ".grain/workflows/tests.yml", JobID: "gcp-smoke", Conclusion: ci.ConclusionFailure,
		Steps: []ci.StepRun{{Name: "deploy", Conclusion: ci.ConclusionFailure, ExitCode: 1,
			Output: "ERROR: (gcloud.iam.service-accounts.keys.create) RESOURCE_EXHAUSTED: Quota exceeded"}},
	}
	judgingRun(t, store, runner, repo, tip, ci.ConclusionFailure, quota)

	at := baseTime
	for attempt := 1; attempt < orchestrator.BaseCIMaximumAttempts; attempt++ {
		at = at.Add(time.Minute)
		if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
			t.Fatal(err)
		}
		row, err := store.GetBaseBranchCI(ctx, repo, "main")
		if err != nil || row == nil {
			t.Fatalf("main's row went missing: %v", err)
		}
		if row.Broken() || row.Conclusion != string(ci.ConclusionErrored) || row.Confirmation != "" {
			t.Fatalf("attempt %d: main's row reads %q (%q), confirmation %q, want an infra failure "+
				"holding nothing", attempt, row.Conclusion, row.Detail, row.Confirmation)
		}
		at = at.Add(10 * time.Minute)
		if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
			t.Fatal(err)
		}
		req := runner.lastRequest(t)
		if want := []string{".grain/workflows/tests.yml:gcp-smoke"}; !slices.Equal(req.Jobs, want) || !req.Recheck {
			t.Fatalf("attempt %d: the re-run asked for %+v, want %v uncached", attempt, req, want)
		}
		row, err = store.GetBaseBranchCI(ctx, repo, "main")
		if err != nil || row == nil {
			t.Fatalf("main's row went missing: %v", err)
		}
		runner.finish(row.RunID, ci.ConclusionFailure, quota)
	}

	// The last attempt fails the same way, and that one stands.
	at = at.Add(time.Minute)
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
		t.Fatal(err)
	}
	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || row == nil {
		t.Fatalf("main's row went missing: %v", err)
	}
	if !row.Broken() || row.Confirmation != model.BaseCIConfirming {
		t.Fatalf("main's row reads %q, confirmation %q, after its re-runs were spent: want it held "+
			"and being confirmed", row.Conclusion, row.Confirmation)
	}
}

// A failure a test produced is the tree's, and goes straight to the
// ordinary road with no infra re-run in front of it.
func TestATestFailureOnTrunkIsNotAnInfraVerdict(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	tip := headSHA(t, client, repo, "main")
	judgingRun(t, store, runner, repo, tip, ci.ConclusionFailure,
		ci.JobRun{WorkflowPath: ".grain/workflows/tests.yml", JobID: "go", Conclusion: ci.ConclusionFailure,
			Steps: []ci.StepRun{{Name: "test", Conclusion: ci.ConclusionFailure, ExitCode: 1,
				Output: "--- FAIL: TestSomething\n    want 1, got 2"}}})
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || row == nil {
		t.Fatalf("main's row went missing: %v", err)
	}
	if !row.Broken() || row.Confirmation != model.BaseCIConfirming {
		t.Fatalf("main's row reads %q, confirmation %q, want a test failure held and being confirmed",
			row.Conclusion, row.Confirmation)
	}
	if req := runner.lastRequest(t); req.Priority != ci.PriorityReleaseGate {
		t.Fatalf("the confirmation asked for priority %d, want trunk's release-gate priority", req.Priority)
	}
}
