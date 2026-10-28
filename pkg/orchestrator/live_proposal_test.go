package orchestrator_test

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

func TestProcessResultRecognizesLiveProposalAndResolvesLaterDependency(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	parent := filedTask(t, ctx, store, "t1", repo)
	firstArgs := map[string]any{"id": "first", "title": "first", "body": "body"}
	live := filedTask(t, ctx, store, "t2", repo)
	live.Title = "first"
	live.Approval = nil
	live.Tags = []string{mcp.ProposalKeyArgs(parent.ID, firstArgs)}
	live.Links = []model.Link{{Kind: model.LinkProposedBy, Target: parent.ID}}
	if err := store.PutTask(ctx, live); err != nil {
		t.Fatal(err)
	}
	result := toolResult(
		agent.ToolCall{Name: "propose_task", Arguments: firstArgs},
		agent.ToolCall{Name: "propose_task", Arguments: map[string]any{
			"id": "second", "title": "second", "body": "body", "depends_on": []any{"first"},
		}},
	)
	if err := orchestrator.ProcessResult(ctx, store, client, parent, result, "t1-1", baseTime); err != nil {
		t.Fatal(err)
	}
	proposals := proposalsByTitle(t, ctx, store, parent.ID)
	if len(proposals) != 2 {
		t.Fatalf("proposals = %v", proposals)
	}
	second := proposals["second"]
	found := false
	for _, link := range second.Links {
		if link.Kind == model.LinkDependsOn && link.Target == live.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("second links = %+v", second.Links)
	}
}
