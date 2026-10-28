package orchestrator_test

import (
	"context"
	"sync"
	"testing"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/ci"
	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

type capturingFramework struct {
	mu         sync.Mutex
	dispatched []string
}

func (f *capturingFramework) Run(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	f.mu.Lock()
	f.dispatched = append(f.dispatched, cfg.TaskID)
	f.mu.Unlock()
	return toolResult(agent.ToolCall{Name: "comment_on_issue", Arguments: map[string]any{"comment": "done"}}), nil
}

func (f *capturingFramework) count(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, d := range f.dispatched {
		if d == id {
			n++
		}
	}
	return n
}

// confirmedRedBase records main as confirmed red with repairID as its
// repair, and files that repair -- running, so it is not itself dispatched
// out from under the test.
func confirmedRedBase(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef,
	repairID string) {

	t.Helper()
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: "badc0ffee123", RunID: "base-run",
		Conclusion: string(ci.ConclusionFailure), Detail: "failure: go, lint",
		Confirmation: model.BaseCIConfirmed, RepairTaskID: repairID,
	}); err != nil {
		t.Fatalf("PutBaseBranchCI: %v", err)
	}
	repair := filedTask(t, ctx, store, repairID, repo)
	repair.Base = "main"
	repair.Tags = []string{orchestrator.BaseCIRepairBranchTag(repo, "main")}
	if err := store.PutTask(ctx, repair); err != nil {
		t.Fatalf("PutTask %s: %v", repairID, err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "run-" + repairID, TaskID: repairID, Sandbox: "s-" + repairID, Attempt: 1, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatalf("StartRun %s: %v", repairID, err)
	}
}

func linkedTo(t *testing.T, ctx context.Context, store *model.Store, id, target string) bool {
	t.Helper()
	task, err := store.GetTask(ctx, id)
	if err != nil || task == nil {
		t.Fatalf("GetTask %s: %v", id, err)
	}
	for _, l := range task.Links {
		if l.Kind == model.LinkDependsOn && l.Target == target {
			return true
		}
	}
	return false
}

// A task targeting a red branch is not held back by it: dispatch links it
// to nothing and starts it like any other. Coordinating a red base branch
// is system:pipeline-steward's job, not a dispatch-time wait.
func TestATaskOnARedBranchIsNotHeldOnItsRepair(t *testing.T) {
	ctx := t.Context()
	store, _ := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	confirmedRedBase(t, ctx, store, repo, "repair-1")

	featureTask := filedTask(t, ctx, store, "t-feature", repo)
	featureTask.Base = "main"
	if err := store.PutTask(ctx, featureTask); err != nil {
		t.Fatalf("PutTask: %v", err)
	}

	framework := &capturingFramework{}
	deps := orchestrator.Deps{
		Store:      store,
		Client:     client,
		Sandboxes:  orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  orchestrator.StaticFramework(framework),
		MaxWorkers: 2,
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if linkedTo(t, ctx, store, "t-feature", "repair-1") {
		t.Fatal("t-feature was linked to repair-1 on a red main")
	}
	if framework.count("t-feature") != 1 {
		t.Errorf("t-feature dispatched %d times on a red main, want 1", framework.count("t-feature"))
	}
}

// A red the policy has not yet decided deserves an agent -- the first
// failure, still being confirmed -- has no repair to point at, and so
// holds nothing.
func TestAnUnconfirmedRedHoldsNothing(t *testing.T) {
	ctx := t.Context()
	store, _ := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: "badc0ffee123",
		Conclusion: string(ci.ConclusionFailure), Detail: "failure: go",
		Confirmation: model.BaseCIConfirming,
	}); err != nil {
		t.Fatal(err)
	}
	featureTask := filedTask(t, ctx, store, "t-feature", repo)
	featureTask.Base = "main"
	if err := store.PutTask(ctx, featureTask); err != nil {
		t.Fatal(err)
	}
	framework := &capturingFramework{}
	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework: orchestrator.StaticFramework(framework), MaxWorkers: 1,
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatal(err)
	}
	if framework.count("t-feature") != 1 {
		t.Fatalf("t-feature dispatched %d times on an unconfirmed red, want 1", framework.count("t-feature"))
	}
}

func TestMergeQueuePausesBaseRebaseOnBrokenBaseBranch(t *testing.T) {
	ctx := t.Context()
	store, _ := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	_, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	pushFileToBranch(t, sim.BareRepo, "main", "MAIN.md", "unrelated work", "unrelated change on main")
	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "go", Status: "completed", Conclusion: strPtr("failure")},
	}

	brokenBase := model.BaseBranchCI{
		Repo:       repo,
		Branch:     "main",
		Commit:     "badc0ffee123",
		Conclusion: string(ci.ConclusionFailure),
		Detail:     "failure: go, lint",
	}
	if err := store.PutBaseBranchCI(ctx, brokenBase); err != nil {
		t.Fatalf("PutBaseBranchCI: %v", err)
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	if n := mergeWrites(sim, repo); n != 0 {
		t.Fatalf("expected 0 branch merges while base branch is broken, got %d", n)
	}
}

// An open repair task is not an incident once the base is green again: a
// repair left open -- parked awaiting acknowledgement, or still queued --
// after somebody else fixed the branch must not keep holding every other
// task on it (docs/test-runs/2026-10-12-circuit-breaker-cache-telemetry.md).
func TestSharedFaultCircuitBreaker_OpenRepairDoesNotHoldOnceBaseIsGreen(t *testing.T) {
	ctx := t.Context()
	store, _ := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: "goodc0ffee456",
		Conclusion: string(ci.ConclusionSuccess), Detail: "success: 1 job(s)",
	}); err != nil {
		t.Fatalf("PutBaseBranchCI: %v", err)
	}
	repair := filedTask(t, ctx, store, "repair-1", repo)
	repair.Base = "main"
	repair.Tags = []string{orchestrator.BaseCIRepairBranchTag(repo, "main")}
	if err := store.PutTask(ctx, repair); err != nil {
		t.Fatalf("PutTask repair-1: %v", err)
	}
	feature := filedTask(t, ctx, store, "t-feature", repo)
	feature.Base = "main"
	if err := store.PutTask(ctx, feature); err != nil {
		t.Fatalf("PutTask: %v", err)
	}

	framework := &capturingFramework{}
	deps := orchestrator.Deps{
		Store:      store,
		Client:     client,
		Sandboxes:  orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  orchestrator.StaticFramework(framework),
		MaxWorkers: 2,
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if framework.count("t-feature") != 1 {
		wait, _ := store.GrantWaitOf(ctx, "t-feature")
		t.Fatalf("t-feature dispatched %d time(s) on a green base with a repair task open, want 1 (wait: %+v)",
			framework.count("t-feature"), wait)
	}
}
