package orchestrator_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// heldForBrokenRules finishes a self-merging change touching path while
// the base's triggers do not parse, and checks it was held for that.
func heldForBrokenRules(t *testing.T, ctx context.Context, store *model.Store, sim *githubsim.Sim,
	client github.Client, path string) model.Task {

	t.Helper()
	declareTriggers(t, ctx, store, branchSHA(t, sim.BareRepo, "main"), nil)
	broken := "reading acme/widgets's declarations: .grain/triggers/secrets.yml: yaml: line 3: did not find expected key"
	if err := store.RecordDeclarationFailure(ctx, triggerRepo, broken, baseTime); err != nil {
		t.Fatal(err)
	}
	task := finishTouching(t, ctx, store, sim, "t1", path, true)
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	got := reloaded(t, ctx, store, task.ID)
	head := branchSHA(t, sim.BareRepo, model.BranchName(task.ID))
	if got.AutoMerge || linkOf(got, model.LinkBrokenRulesHold) != head {
		t.Fatalf("held for broken rules: automerge=%v links=%+v, want the hold marked at %s", got.AutoMerge, got.Links, head)
	}
	return got
}

func syncHolds(t *testing.T, ctx context.Context, store *model.Store, client github.Client, at time.Duration) {
	t.Helper()
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(at)); err != nil {
		t.Fatalf("SyncSecurityHolds: %v", err)
	}
}

func TestFixingTheBasesRulesReleasesWhatTheyAloneHeld(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	task := heldForBrokenRules(t, ctx, store, sim, client, "docs/readme.md")
	reviewID := linkOf(task, model.LinkSecurityReviewTask)

	syncHolds(t, ctx, store, client, time.Minute)
	if got := reloaded(t, ctx, store, task.ID); got.AutoMerge {
		t.Fatal("released while the base's rules still do not parse")
	}

	declareTriggers(t, ctx, store, branchSHA(t, sim.BareRepo, "main"), nil)
	syncHolds(t, ctx, store, client, 2*time.Minute)
	got := reloaded(t, ctx, store, task.ID)
	if !got.AutoMerge || len(model.PendingApprovals(got)) != 0 || linkOf(got, model.LinkBrokenRulesHold) != "" {
		t.Fatalf("the base's fix did not release a docs change: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
	if note := strings.Join(prCommentBodies(t, client, got), "\n"); !strings.Contains(note, "rules parse again") {
		t.Errorf("the release is not said on the pull request:\n%s", note)
	}
	if reviewID != "" {
		if o, err := store.GetObservation(ctx, reviewID); err != nil || o == nil || o.ClosedAt == nil {
			t.Errorf("the broken-rules hold's security review %s was left open: %+v %v", reviewID, o, err)
		}
	}
}

func TestFixedRulesThatGuardTheChangeKeepItHeld(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	task := heldForBrokenRules(t, ctx, store, sim, client, "config/secrets/prod.yml")

	declareTriggers(t, ctx, store, branchSHA(t, sim.BareRepo, "main"), map[string]string{"sensitive": "Check"},
		sensitiveTrigger("secrets", model.TriggerHumanRequired, []string{"alice"}, "config/secrets"))
	syncHolds(t, ctx, store, client, time.Minute)
	got := reloaded(t, ctx, store, task.ID)
	rules := map[string]bool{}
	for _, r := range model.PendingApprovals(got) {
		rules[r.Rule] = true
	}
	if got.AutoMerge || !rules["trigger:secrets"] || rules["builtin:unparseable-triggers"] {
		t.Fatalf("a change the fixed rules guard: automerge=%v pending=%v", got.AutoMerge, rules)
	}
}

func TestFixingTheBaseDoesNotReleaseAMovedHead(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	task := heldForBrokenRules(t, ctx, store, sim, client, "docs/readme.md")
	pushFile(t, sim.BareRepo, model.BranchName(task.ID), "docs/other.md", "more\n")

	declareTriggers(t, ctx, store, branchSHA(t, sim.BareRepo, "main"), nil)
	syncHolds(t, ctx, store, client, time.Minute)
	got := reloaded(t, ctx, store, task.ID)
	if got.AutoMerge || linkOf(got, model.LinkBrokenRulesHold) != "" {
		t.Fatalf("a head that moved under the hold: automerge=%v links=%+v, want it left to an owner", got.AutoMerge, got.Links)
	}
}

func TestFixingTheBaseDoesNotSecondGuessAnOwner(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	task := heldForBrokenRules(t, ctx, store, sim, client, "docs/readme.md")
	// An owner released the requirement and then turned auto-merge off
	// again: that is their decision, and the fix does not undo it.
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.Links = append(t.Links, model.Link{Kind: model.LinkApprovedBy, Target: "builtin:unparseable-triggers=owner"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	declareTriggers(t, ctx, store, branchSHA(t, sim.BareRepo, "main"), nil)
	syncHolds(t, ctx, store, client, time.Minute)
	got := reloaded(t, ctx, store, task.ID)
	if got.AutoMerge || linkOf(got, model.LinkBrokenRulesHold) != "" {
		t.Fatalf("a requirement an owner already decided: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
}
