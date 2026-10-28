package orchestrator_test

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// A run part way down a tree with no origin at its root names its own
// task in propose_task's origin: the proposal is filed at the tree's
// root, not in a new tree at the caller. One naming a pull request starts
// that pull request's tree, since the caller's tree has no GitHub origin
// to keep it in.
func TestProposeTaskNamingItsOwnTaskFilesAtTheTreesRoot(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	// The caller is filed first and linked to the root after, so it is
	// stored the way a task from before origins is: filed by the root,
	// with no origin of its own.
	root := filedTask(t, ctx, store, "root", repo)
	caller := filedTask(t, ctx, store, "caller", repo)
	caller.Links = []model.Link{{Kind: model.LinkProposedBy, Target: root.ID}}
	if err := store.PutTask(ctx, caller); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetTask(ctx, caller.ID)
	if err != nil || stored.WorkOrigin != "" || stored.Parent() != root.ID {
		t.Fatalf("caller = origin %q parent %q (%v), want no origin and parent %s",
			stored.WorkOrigin, stored.Parent(), err, root.ID)
	}

	result := toolResult(
		agent.ToolCall{Name: "propose_task", Arguments: map[string]any{
			"title": "named itself", "body": "b", "origin": "task " + caller.ID,
		}},
		agent.ToolCall{Name: "propose_task", Arguments: map[string]any{
			"title": "named a pull request", "body": "b", "origin": "#7",
		}},
	)
	if err := orchestrator.ProcessResult(ctx, store, client, *stored, result, "caller-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	proposals := proposalsByTitle(t, ctx, store, caller.ID)
	for title, want := range map[string]model.WorkOrigin{
		"named itself":         model.TaskOrigin(root.ID),
		"named a pull request": model.GitHubOrigin(repo, 7),
	} {
		got, ok := proposals[title]
		if !ok {
			t.Fatalf("proposal %q was not filed", title)
		}
		if got.WorkOrigin != want {
			t.Errorf("%q origin = %q, want %q", title, got.WorkOrigin, want)
		}
		if got.Parent() != caller.ID {
			t.Errorf("%q parent = %q, want %s", title, got.Parent(), caller.ID)
		}
	}
}
