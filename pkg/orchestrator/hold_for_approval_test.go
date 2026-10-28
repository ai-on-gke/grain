package orchestrator_test

import (
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// The end-of-run relay honours hold_for_approval exactly as the live call
// does (ui.CreateTask): an auto-merge job's proposal it would have queued
// is filed proposed, tagged and noted as held on purpose, beside one the
// same run let queue; and a held proposal's dependencies and not_before
// are filed as asked.
func TestProcessResultHoldsAProposalTheRunAskedToHold(t *testing.T) {
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	task.AutoMerge = true
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	result := toolResult(
		agent.ToolCall{Name: "propose_task", Arguments: map[string]any{
			"title": "runs on its own", "body": "b",
		}},
		agent.ToolCall{Name: "propose_task", Arguments: map[string]any{
			"title": "waits for the operator", "body": "b", "hold_for_approval": true,
			"depends_on": []any{"t1"}, "not_before": "2099-01-01",
		}},
	)
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	filed := proposalsByTitle(t, ctx, store, "t1")

	queued := filed["runs on its own"]
	if queued.Approval == nil {
		t.Error("the unheld proposal was not approved -- its proposer's permission covers it")
	}
	if model.HeldForApproval(queued) {
		t.Error("the unheld proposal is tagged held")
	}

	held, ok := filed["waits for the operator"]
	if !ok {
		t.Fatal("the held proposal was never filed")
	}
	if held.Approval != nil {
		t.Errorf("held proposal approved by %+v, want nobody", held.Approval)
	}
	if !model.HeldForApproval(held) {
		t.Error("held proposal is not tagged held -- its row could not say it was deliberate")
	}
	if !strings.Contains(held.Body, "hold_for_approval") {
		t.Errorf("held proposal body does not say why it waits:\n%s", held.Body)
	}
	if held.DeferredUntil == nil {
		t.Error("held proposal lost its not_before")
	}
	dependsOnParent := false
	for _, l := range held.Links {
		if l.Kind == model.LinkDependsOn && l.Target == "t1" {
			dependsOnParent = true
		}
	}
	if !dependsOnParent {
		t.Errorf("held proposal links = %+v, want a depends-on t1", held.Links)
	}
}

// A run whose permission covers nothing gains nothing by holding, and is
// not credited with a choice: the proposal waits as it would have, and
// carries no held tag.
func TestProcessResultHoldWithoutPermissionIsAnOrdinaryProposal(t *testing.T) {
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	result := toolResult(agent.ToolCall{Name: "propose_task", Arguments: map[string]any{
		"title": "held anyway", "body": "b", "hold_for_approval": true,
	}})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	p := proposalsByTitle(t, ctx, store, "t1")["held anyway"]
	if p.Approval != nil {
		t.Errorf("approved by %+v, want nobody", p.Approval)
	}
	if model.HeldForApproval(p) {
		t.Error("tagged held, but nothing was held back")
	}
}
