package orchestrator_test

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

func TestProcessResultAppliesDefaultsToProposedTasks(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutConfig(ctx, model.Config{
		DefaultCapabilities: []string{"self-debug"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, CIMode: model.CIModeGrain,
	}); err != nil {
		t.Fatal(err)
	}
	task := filedTask(t, ctx, store, "t1", repo)

	result := toolResult(agent.ToolCall{
		Name: "propose_task",
		Arguments: map[string]any{
			"title": "follow-up work", "body": "do more of this",
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposal, ok := proposalsByTitle(t, ctx, store, task.ID)["follow-up work"]
	if !ok {
		t.Fatal("the proposed task was never filed")
	}
	got := map[string]model.GrantSource{}
	for _, grant := range proposal.Grants {
		got[grant.Capability] = grant.Via
	}
	for _, capability := range []string{"self-debug", grainci.CapabilityName} {
		if via, ok := got[capability]; !ok || via != model.GrantByDefault {
			t.Errorf("%s grant = %q, %v; want %q, true", capability, via, ok, model.GrantByDefault)
		}
	}
}

func TestProcessResultAppliesRequiredConfigCIToProposedTasks(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "grain", "config", "main")
	task := filedTask(t, ctx, store, "t1", model.ConfigRepo())

	result := toolResult(agent.ToolCall{
		Name: "propose_task",
		Arguments: map[string]any{
			"title": "follow-up config work", "body": "change another setting",
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposal, ok := proposalsByTitle(t, ctx, store, task.ID)["follow-up config work"]
	if !ok {
		t.Fatal("the proposed task was never filed")
	}
	if !grainci.Granted(proposal.Grants) {
		t.Fatalf("proposal grants = %+v, want required grain-ci", proposal.Grants)
	}
}

func TestRunCycleAutoGrantsImplicitGrainCIForGrainCIRepo(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, CIMode: model.CIModeGrain,
	}); err != nil {
		t.Fatal(err)
	}
	task := filedTask(t, ctx, store, "t1", repo)

	sandboxes := &recordingSandboxes{HostSandboxes: orchestrator.NewHostSandboxes(t.TempDir())}
	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: sandboxes,
		Framework:  completesWithAComment(),
		MaxWorkers: 1,
	}

	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	acquired, _ := sandboxes.calls()
	if len(acquired) != 1 || acquired[0].name != "t1-1" {
		t.Fatalf("acquired = %v, want 1 for t1-1", acquired)
	}
	stored, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !grainci.Granted(stored.Grants) {
		t.Errorf("stored task grants = %v, want implicit grain-ci backfilled", stored.Grants)
	}
	wait, err := store.GrantWaitOf(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wait != nil {
		t.Errorf("grant wait = %+v, want none", wait)
	}
}

func TestRunCycleAutoGrantsImplicitGrainCIForConfigRepo(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "grain", "config", "main")
	task := filedTask(t, ctx, store, "t1", model.ConfigRepo())

	sandboxes := &recordingSandboxes{HostSandboxes: orchestrator.NewHostSandboxes(t.TempDir())}
	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: sandboxes,
		Framework:  completesWithAComment(),
		MaxWorkers: 1,
	}

	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	acquired, _ := sandboxes.calls()
	if len(acquired) != 1 || acquired[0].name != "t1-1" {
		t.Fatalf("acquired = %v, want 1 for t1-1", acquired)
	}
	stored, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !grainci.Granted(stored.Grants) {
		t.Errorf("stored task grants = %v, want implicit grain-ci backfilled", stored.Grants)
	}
	wait, err := store.GrantWaitOf(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wait != nil {
		t.Errorf("grant wait = %+v, want none", wait)
	}
}

func TestCIReconcilerAutoGrantsImplicitGrainCI(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, CIMode: model.CIModeGrain,
	}); err != nil {
		t.Fatal(err)
	}
	task, _ := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	runner := newFakeCI()

	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}
	if runner.started() != 1 {
		t.Fatalf("grain CI started %d runs, want 1", runner.started())
	}
	stored, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !grainci.Granted(stored.Grants) {
		t.Errorf("stored task grants = %v, want implicit grain-ci backfilled", stored.Grants)
	}
	wait, err := store.GrantWaitOf(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wait != nil {
		t.Fatalf("grant wait = %+v, want none", wait)
	}
}

func TestProcessResultAttachesRequestedCapabilitiesToProposedTasks(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutConfig(ctx, model.Config{
		DefaultCapabilities: []string{"self-debug"},
	}); err != nil {
		t.Fatal(err)
	}
	task := filedTask(t, ctx, store, "t1", repo)

	result := toolResult(agent.ToolCall{
		Name: "propose_task",
		Arguments: map[string]any{
			"title":        "follow-up with requested capabilities",
			"body":         "needs gcp key",
			"capabilities": []any{"gcp-key"},
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposal, ok := proposalsByTitle(t, ctx, store, task.ID)["follow-up with requested capabilities"]
	if !ok {
		t.Fatal("the proposed task was never filed")
	}
	got := map[string]model.GrantSource{}
	for _, grant := range proposal.Grants {
		got[grant.Capability] = grant.Via
	}
	if got["self-debug"] != model.GrantByDefault {
		t.Errorf("self-debug via = %q, want GrantByDefault", got["self-debug"])
	}
	if got["gcp-key"] != model.GrantByLabel {
		t.Errorf("gcp-key via = %q, want GrantByLabel", got["gcp-key"])
	}
}

// An agent whose own task holds grain-ci may name it in a proposal.
// Against a repository whose CI Mode does not grant it, that is passed
// over: the proposal holds no grain-ci, and it is not held for a person
// as asking for a capability its proposer lacks.
func TestProcessResultPassesOverARequestedGrainCI(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	result := toolResult(agent.ToolCall{
		Name: "propose_task",
		Arguments: map[string]any{
			"title":        "follow-up naming grain-ci",
			"body":         "carry on",
			"capabilities": []any{"grain-ci"},
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposal, ok := proposalsByTitle(t, ctx, store, task.ID)["follow-up naming grain-ci"]
	if !ok {
		t.Fatal("the proposed task was never filed")
	}
	for _, grant := range proposal.Grants {
		if grant.Capability == "grain-ci" {
			t.Errorf("grants = %+v, want no grain-ci on a repo whose CI Mode does not grant it", proposal.Grants)
		}
	}
	if strings.Contains(proposal.Body, "does not hold itself") {
		t.Errorf("body = %q, want no capability-escalation note for grain-ci", proposal.Body)
	}
}

func TestProcessResultAutoGrantsGrainCIToWorkflowRepos(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "workflow-repo", "main")
	repo := model.RepoRef{Owner: "acme", Name: "workflow-repo"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:   repo,
		CIMode: model.CIModeGrain,
	}); err != nil {
		t.Fatal(err)
	}
	task := filedTask(t, ctx, store, "t1", repo)

	result := toolResult(agent.ToolCall{
		Name: "propose_task",
		Arguments: map[string]any{
			"title": "work on workflow repo",
			"body":  "should automatically get grain-ci",
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposal, ok := proposalsByTitle(t, ctx, store, task.ID)["work on workflow repo"]
	if !ok {
		t.Fatal("the proposed task was never filed")
	}
	if !grainci.Granted(proposal.Grants) {
		t.Fatalf("proposal grants = %+v, want auto-granted grain-ci for workflow repo", proposal.Grants)
	}
}
