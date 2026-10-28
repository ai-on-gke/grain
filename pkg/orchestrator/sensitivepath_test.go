package orchestrator_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/trustboundary"
)

// Sensitive paths (grain/task-1714): repository-declared, built on gated
// pull-request triggers, with the declarations guarding themselves.

func sensitiveTrigger(name string, human model.TriggerHuman, approvers []string, paths ...string) model.PathTrigger {
	tr := gateTrigger(name, human, paths...)
	tr.Sensitive, tr.Approvers = true, approvers
	tr.Template = model.SecurityReviewTemplateID
	return tr
}

func TestASensitiveTriggerHoldsAMatchingChangeAndRunsItsTemplate(t *testing.T) {
	t.Parallel()
	ctx, store, _, client, task := finishGated(t,
		[]model.PathTrigger{sensitiveTrigger("payments", model.TriggerHumanOnFindings, nil, "payments")},
		"payments/charge.go")
	got := reloaded(t, ctx, store, task.ID)
	if got.AutoMerge || linkOf(got, model.LinkPathTriggerHold) == "" {
		t.Fatalf("a sensitive path was not held: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
	ids := gatingIDs(got)
	if len(ids) != 1 {
		t.Fatalf("want the trigger's review filed, got %v", ids)
	}
	gate := reloaded(t, ctx, store, ids[0])
	if gate.Origin.Reason != model.ReasonReview || !strings.Contains(gate.Body, "`payments/charge.go`") {
		t.Fatalf("the sensitive path's review is not a stacked review of the change: %+v", gate)
	}
	pr := strings.Join(prCommentBodies(t, client, got), "\n")
	if !strings.Contains(pr, "`payments` (repository file `.grain/triggers/payments.yml`, sensitive") ||
		!strings.Contains(pr, "grain's security review") {
		t.Errorf("the pull request does not say which rule, from where, filed what:\n%s", pr)
	}
	if len(model.PendingApprovals(got)) != 0 {
		t.Errorf("on-findings asks for no named approver, got %+v", model.PendingApprovals(got))
	}
	completeReview(t, ctx, store, gate.ID)
	syncTriggerHolds(t, ctx, store, client, baseTime.Add(time.Hour))
	if !reloaded(t, ctx, store, task.ID).AutoMerge {
		t.Fatal("a clean review did not release an on-findings sensitive hold")
	}
}

func TestASensitivePathRequiringAHumanNamesWhoAfterACleanReview(t *testing.T) {
	t.Parallel()
	ctx, store, _, client, task := finishGated(t, []model.PathTrigger{
		sensitiveTrigger("payments", model.TriggerHumanRequired, []string{"team:payments", "alice"}, "payments"),
		sensitiveTrigger("keys", model.TriggerHumanRequired, nil, "keys"),
	}, "payments/charge.go", "keys/prod.pem")
	got := reloaded(t, ctx, store, task.ID)
	pending := model.PendingApprovals(got)
	// Triggers are judged in name order.
	if len(pending) != 2 || pending[0].Rule != "trigger:keys" || pending[1].Rule != "trigger:payments" {
		t.Fatalf("pending approvals = %+v, want one per sensitive trigger", pending)
	}
	if strings.Join(pending[1].Approvers, ",") != "team:payments,alice" {
		t.Errorf("payments approvers = %v", pending[1].Approvers)
	}
	for _, id := range gatingIDs(got) {
		completeReview(t, ctx, store, id)
	}
	syncTriggerHolds(t, ctx, store, client, baseTime.Add(time.Hour))
	if reloaded(t, ctx, store, task.ID).AutoMerge {
		t.Fatal("human: required was released by a clean review")
	}
	pr := strings.Join(prCommentBodies(t, client, got), "\n")
	if !strings.Contains(pr, "released only by a person holding the access code") {
		t.Errorf("the pull request does not say who can release it:\n%s", pr)
	}
}

func TestASensitivePathWithNoTemplateFilesNothingAndWaits(t *testing.T) {
	t.Parallel()
	tr := sensitiveTrigger("keys", model.TriggerHumanRequired, []string{"alice"}, "keys")
	tr.Template = model.TemplateNone
	ctx, store, _, client, task := finishGated(t, []model.PathTrigger{tr}, "keys/prod.pem")
	got := reloaded(t, ctx, store, task.ID)
	if got.AutoMerge || len(gatingIDs(got)) != 0 || len(model.PendingApprovals(got)) != 1 {
		t.Fatalf("template: none: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
	syncTriggerHolds(t, ctx, store, client, baseTime.Add(time.Hour))
	if reloaded(t, ctx, store, task.ID).AutoMerge {
		t.Fatal("released with nobody having looked")
	}
}

func TestAGateWhoseTemplateIsGoneFilesTheSecurityReview(t *testing.T) {
	t.Parallel()
	tr := sensitiveTrigger("payments", model.TriggerHumanOnFindings, nil, "payments")
	tr.Template = templateID("deleted-since")
	ctx, store, _, _, task := finishGated(t, []model.PathTrigger{tr}, "payments/charge.go")
	got := reloaded(t, ctx, store, task.ID)
	ids := gatingIDs(got)
	if got.AutoMerge || len(ids) != 1 {
		t.Fatalf("a gate with a missing template: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
	security := model.SystemTemplate(model.SecurityReviewTemplateID, model.RepoRef{})
	if gate := reloaded(t, ctx, store, ids[0]); !strings.Contains(gate.Body, strings.TrimSpace(security.Body)[:60]) {
		t.Errorf("the stand-in is not grain's security review:\n%s", gate.Body)
	}
}

func TestEditingATemplateAGatedTriggerFilesNeedsTheRepositorysOwners(t *testing.T) {
	t.Parallel()
	ctx, store, _, client, task := finishGated(t,
		[]model.PathTrigger{gateTrigger("secrets", model.TriggerHumanOnFindings, "config/secrets")},
		model.TemplateDeclarationDir+"/sensitive.yml")
	got := reloaded(t, ctx, store, task.ID)
	pending := model.PendingApprovals(got)
	if got.AutoMerge || len(pending) != 1 || pending[0].Rule != "builtin:trigger-templates" {
		t.Fatalf("editing a gated trigger's template: automerge=%v pending=%+v", got.AutoMerge, pending)
	}
	if strings.Join(pending[0].Approvers, ",") != model.ApproverTeamAdmins(model.DefaultTeamID)+","+model.ApproverOwner {
		t.Errorf("approvers = %v, want the owning team's admins and system admins", pending[0].Approvers)
	}
	note := strings.Join(prCommentBodies(t, client, got), "\n")
	for _, want := range []string{"**trigger-templates** (built in, from this repository's `.grain/triggers`)",
		"Who can release it", "repo-rules@"} {
		if !strings.Contains(note, want) {
			t.Errorf("the hold note lacks %q:\n%s", want, note)
		}
	}
}

func TestDeletingATriggerInThePullRequestIsStillJudgedByTheBase(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	pushFile(t, sim.BareRepo, "main", ".grain/triggers/secrets.yml", "on: pull_request\n")
	declareTriggers(t, ctx, store, branchSHA(t, sim.BareRepo, "main"), map[string]string{"sensitive": "Check"},
		sensitiveTrigger("secrets", model.TriggerHumanRequired, []string{"alice"}, "config/secrets"))
	task := finishTouching(t, ctx, store, sim, "t1", "config/secrets/prod.yml", true)
	removeFile(t, sim.BareRepo, model.BranchName(task.ID), ".grain/triggers/secrets.yml")
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	got := reloaded(t, ctx, store, task.ID)
	rules := map[string]bool{}
	for _, r := range model.PendingApprovals(got) {
		rules[r.Rule] = true
	}
	if got.AutoMerge || !rules["trigger:secrets"] || !rules["builtin:path-triggers"] {
		t.Fatalf("a change deleting the trigger that judges it escaped it: automerge=%v pending=%v", got.AutoMerge, rules)
	}
}

func TestUnparseableTriggersOnTheBaseHoldEverySelfMergingChange(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	declareTriggers(t, ctx, store, branchSHA(t, sim.BareRepo, "main"), nil)
	broken := "reading acme/widgets's declarations: .grain/triggers/secrets.yml: yaml: line 3: did not find expected key"
	if err := store.RecordDeclarationFailure(ctx, triggerRepo, broken, baseTime); err != nil {
		t.Fatal(err)
	}
	task := finishTouching(t, ctx, store, sim, "t1", "docs/readme.md", true)
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	got := reloaded(t, ctx, store, task.ID)
	pending := model.PendingApprovals(got)
	if got.AutoMerge || len(pending) != 1 || pending[0].Rule != "builtin:unparseable-triggers" {
		t.Fatalf("a docs change under broken triggers: automerge=%v pending=%+v", got.AutoMerge, pending)
	}
	if note := strings.Join(prCommentBodies(t, client, got), "\n"); !strings.Contains(note, "did not find expected key") {
		t.Errorf("the hold does not say why:\n%s", note)
	}
}

func TestARepositoryWithNoTriggersIsUnchanged(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	task := finishTouching(t, ctx, store, sim, "t1", "docs/readme.md", true)
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	got := reloaded(t, ctx, store, task.ID)
	head := branchSHA(t, sim.BareRepo, model.BranchName(task.ID))
	if !got.AutoMerge || linkOf(got, model.LinkTrustBoundaryCleared) != trustboundary.Version+"@"+head {
		t.Fatalf("a repository with no triggers was not cleared as before: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
	if linkOf(got, model.LinkRepoRules) != "" || len(model.ApprovalRequirements(got)) != 0 {
		t.Errorf("a repository with no triggers grew repository-rule links: %+v", got.Links)
	}
}

// removeFile deletes path on branch, as a pull request deleting it would.
func removeFile(t *testing.T, bare, branch, path string) {
	t.Helper()
	dir := t.TempDir()
	wd := filepath.Join(dir, "work")
	run(t, dir, "git", "clone", "-q", "--branch", branch, bare, wd)
	run(t, wd, "git", "config", "user.email", "agent@example.com")
	run(t, wd, "git", "config", "user.name", "agent")
	run(t, wd, "git", "rm", "-q", path)
	run(t, wd, "git", "commit", "-q", "-m", "remove "+path)
	run(t, wd, "git", "push", "-q", "origin", branch)
}

func TestTheDeclarationSyncRefusesApproversThisDeploymentDoesNotKnow(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	// Any user name is accepted -- the deployment has no accounts to
	// check one against -- but a team must be one it has.
	cfg := model.DefaultConfig()
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	known := sensitiveTrigger("ok", model.TriggerHumanRequired, []string{"team:default", "team:Admin", "carol"}, "a")
	known.Path = ".grain/triggers/ok.yml"
	if err := orchestrator.CheckTriggerApprovers(ctx, store, []model.PathTrigger{known}); err != nil {
		t.Fatalf("known approvers refused: %v", err)
	}
	unknown := sensitiveTrigger("bad", model.TriggerHumanRequired, []string{"team:paymnets", "nobody-at-all"}, "a")
	unknown.Path = ".grain/triggers/bad.yml"
	err := orchestrator.CheckTriggerApprovers(ctx, store, []model.PathTrigger{unknown})
	if err == nil || !strings.Contains(err.Error(), `"team:paymnets" names a team`) || strings.Contains(err.Error(), "nobody-at-all") {
		t.Fatalf("unknown approvers: %v", err)
	}
	// And the error is one that holds the repository (the sync records it).
	if !model.TriggerDeclarationsBroken(err.Error()) {
		t.Error("an approver error would not be read as broken triggers")
	}
}

func TestSensitiveGrainDeclarationsTriggerReusesTrustBoundarySecurityReview(t *testing.T) {
	t.Parallel()
	tr := sensitiveTrigger("grain-declarations", model.TriggerHumanRequired, nil, ".grain/**")
	ctx, store, _, client, task := finishGated(t, []model.PathTrigger{tr}, ".grain/workflows/tests.yml")
	got := reloaded(t, ctx, store, task.ID)
	if got.AutoMerge {
		t.Fatalf("change touching .grain/workflows/tests.yml kept auto-merge: %+v", got.Links)
	}
	reviewID := linkOf(got, model.LinkSecurityReviewTask)
	if reviewID == "" {
		t.Fatalf("expected trust-boundary security review link: %+v", got.Links)
	}
	ids := gatingIDs(got)
	if len(ids) != 1 || ids[0] != reviewID {
		t.Fatalf("gating trigger IDs = %v, want [%s] (reusing the trust-boundary security review)", ids, reviewID)
	}
	if reviews := securityReviewsOf(t, ctx, store, task.ID); len(reviews) != 1 {
		t.Fatalf("filed %d security reviews for .grain/workflows/tests.yml, want 1", len(reviews))
	}
	if linkOf(got, model.LinkPathTriggerHold) != string(model.TriggerHumanRequired) {
		t.Fatalf("path-trigger hold = %q, want %q", linkOf(got, model.LinkPathTriggerHold), model.TriggerHumanRequired)
	}
	pending := model.PendingApprovals(got)
	foundTriggerApproval := false
	for _, p := range pending {
		if p.Rule == "trigger:grain-declarations" {
			foundTriggerApproval = true
		}
	}
	if !foundTriggerApproval {
		t.Fatalf("pending approvals = %+v, want trigger:grain-declarations", pending)
	}

	completeReview(t, ctx, store, reviewID)
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	syncTriggerHolds(t, ctx, store, client, baseTime.Add(time.Hour))
	if reloaded(t, ctx, store, task.ID).AutoMerge {
		t.Fatal("a clean review released a RequiresHuman + human:required change to .grain/workflows/tests.yml")
	}
}
