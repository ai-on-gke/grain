// Which repositories RunDispatch tells a run it may file a proposal
// against -- the fact a subprocess Framework turns into its forked
// mcpserver's -propose-repo, and that mcpserver folds into propose_task's
// own `repo` argument (agent.ProposeRepoArgs, mcp.NewMockTools).
//
// It is worth a test of its own for the reason run_grants_test.go's
// subject is: the failure it prevents is silent. A run that does not know
// a repository is nameable files the work against its own repo instead,
// and nobody finds out until a human retargets the proposal by hand.
package orchestrator_test

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// proposeRepoCapturingFramework records what one run was told it may
// propose against, then finishes the way grantCapturingFramework does.
type proposeRepoCapturingFramework struct {
	mu    sync.Mutex
	repos []string
}

func (f *proposeRepoCapturingFramework) Run(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	f.mu.Lock()
	f.repos = cfg.ProposeRepos
	f.mu.Unlock()
	return toolResult(agent.ToolCall{Name: "comment_on_issue", Arguments: map[string]any{"comment": "done"}}), nil
}

func (f *proposeRepoCapturingFramework) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.repos
}

func TestRunCycleTellsARunWhichReposAProposalMayName(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedTask(t, ctx, store, "t1", repo)

	// A second repository this deployment works in but has no task for:
	// exactly the one a run cannot discover from its own sandbox, and the
	// case the whole list exists for.
	elsewhere := model.RepoRef{Owner: "acme", Name: "docs"}
	// With defaults of its own, since a RepoConfig carrying nothing is
	// deleted rather than stored (Store.PutRepoConfig).
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: elsewhere, SetupCommand: "make deps",
	}); err != nil {
		t.Fatal(err)
	}

	framework := &proposeRepoCapturingFramework{}
	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  orchestrator.StaticFramework(framework),
		MaxWorkers: 1,
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	told := framework.seen()
	for _, want := range []string{repo.String(), elsewhere.String()} {
		if !slices.Contains(told, want) {
			t.Errorf("RunConfig.ProposeRepos = %v, want %s among them", told, want)
		}
	}
	// Every repository offered has to be one proposedTarget will actually
	// accept, or the list would send runs after proposals grain refiles
	// with a note (model.Store.WorksInRepo draws that line).
	for _, named := range told {
		parsed, err := model.ParseRepo(named)
		if err != nil {
			t.Fatalf("ProposeRepos named %q, which is not owner/name: %v", named, err)
		}
		known, err := store.WorksInRepo(ctx, parsed)
		if err != nil {
			t.Fatal(err)
		}
		if !known {
			t.Errorf("ProposeRepos offered %s, which propose_task's own gate refuses", named)
		}
	}
}
