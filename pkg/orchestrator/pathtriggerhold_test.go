package orchestrator_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/github/githubsim"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

func gateTrigger(name string, human model.TriggerHuman, paths ...string) model.PathTrigger {
	return model.PathTrigger{
		Name: name, On: model.TriggerOnPullRequest, Paths: paths, Template: templateID("sensitive"),
		Gate: true, Human: human, AutoApprove: true,
	}
}

// finishGated declares triggers on triggerRepo's base, then finishes an
// auto-merging change that touches each of paths, through ProcessResult.
func finishGated(t *testing.T, triggers []model.PathTrigger, paths ...string) (
	context.Context, *model.Store, *githubsim.Sim, *github.RESTClient, model.Task) {
	t.Helper()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	declareTriggers(t, ctx, store, branchSHA(t, sim.BareRepo, "main"),
		map[string]string{"sensitive": "Check {{trigger}} on {{pr}}", "planner": "Plan"}, triggers...)
	task := finishTouching(t, ctx, store, sim, "t1", paths[0], true)
	for _, p := range paths[1:] {
		pushFile(t, sim.BareRepo, model.BranchName(task.ID), p, "changed\n")
	}
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	return ctx, store, sim, client, task
}

func gatingIDs(task model.Task) []string {
	var ids []string
	for _, l := range task.Links {
		if l.Kind == model.LinkPathTriggerTask {
			_, id, _ := strings.Cut(l.Target, "=")
			ids = append(ids, id)
		}
	}
	return ids
}

func syncTriggerHolds(t *testing.T, ctx context.Context, store *model.Store, client github.Client, at time.Time) {
	t.Helper()
	if err := orchestrator.SyncPathTriggerHolds(ctx, store, client, at); err != nil {
		t.Fatal(err)
	}
}

func TestGatedPathIsHeldAndReleasedByACleanTask(t *testing.T) {
	t.Parallel()
	ctx, store, _, client, task := finishGated(t,
		[]model.PathTrigger{gateTrigger("secrets", model.TriggerHumanOnFindings, "config/secrets")},
		"config/secrets/prod.yml")

	got := reloaded(t, ctx, store, task.ID)
	if got.AutoMerge {
		t.Fatal("a change touching a gated path kept auto-merge")
	}
	if v := linkOf(got, model.LinkPathTriggerHold); v != string(model.TriggerHumanOnFindings) {
		t.Fatalf("hold link = %q, links %+v", v, got.Links)
	}
	ids := gatingIDs(got)
	if len(ids) != 1 {
		t.Fatalf("want one gating task, got %v", ids)
	}
	gate := reloaded(t, ctx, store, ids[0])
	if gate.Origin.Reason != model.ReasonReview || gate.Base != model.BranchName(task.ID) {
		t.Fatalf("the gating task is not a review stacked on the change: %+v", gate)
	}
	if !strings.HasPrefix(gate.Title, "Check secrets on acme/widgets#") {
		t.Errorf("gating task title = %q", gate.Title)
	}
	if !strings.Contains(gate.Body, "`config/secrets/prod.yml`") || !strings.Contains(gate.Body, "- head_sha: `") {
		t.Errorf("gating task body lacks the facts:\n%s", gate.Body)
	}
	pr := strings.Join(prCommentBodies(t, client, got), "\n")
	if !strings.Contains(pr, "`secrets` (repository file `.grain/triggers/secrets.yml`, human: `on-findings`) filed task "+gate.ID) {
		t.Errorf("the pull request does not say which trigger demanded what: %s", pr)
	}

	syncTriggerHolds(t, ctx, store, client, baseTime.Add(time.Minute))
	if reloaded(t, ctx, store, task.ID).AutoMerge {
		t.Fatal("released before the gating task finished")
	}
	completeReview(t, ctx, store, gate.ID)
	syncTriggerHolds(t, ctx, store, client, baseTime.Add(time.Hour))
	got = reloaded(t, ctx, store, task.ID)
	if !got.AutoMerge || linkOf(got, model.LinkPathTriggerHold) != "" {
		t.Fatalf("a clean gating task did not release the hold: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
}

func TestGatedPathWithFindingsWaitsForAHuman(t *testing.T) {
	t.Parallel()
	ctx, store, _, client, task := finishGated(t,
		[]model.PathTrigger{gateTrigger("secrets", model.TriggerHumanOnFindings, "config/secrets")},
		"config/secrets/prod.yml")
	gateID := gatingIDs(reloaded(t, ctx, store, task.ID))[0]
	gate := reloaded(t, ctx, store, gateID)
	result := toolResult(reviewFinding("this rotates nothing", "config/secrets/prod.yml", 1))
	if err := orchestrator.ProcessResult(ctx, store, client, gate, result, gateID+"-1", baseTime.Add(20*time.Minute)); err != nil {
		t.Fatalf("ProcessResult for the gating task: %v", err)
	}
	completeReview(t, ctx, store, gateID)
	syncTriggerHolds(t, ctx, store, client, baseTime.Add(time.Hour))
	got := reloaded(t, ctx, store, task.ID)
	if got.AutoMerge || linkOf(got, model.LinkPathTriggerHold) != "" {
		t.Fatalf("findings did not leave the change for a human: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
}

func TestGatedPathRequiringAHumanIsNeverReleased(t *testing.T) {
	t.Parallel()
	ctx, store, _, client, task := finishGated(t,
		[]model.PathTrigger{gateTrigger("secrets", model.TriggerHumanRequired, "config/secrets")},
		"config/secrets/prod.yml")
	gateID := gatingIDs(reloaded(t, ctx, store, task.ID))[0]
	completeReview(t, ctx, store, gateID)
	syncTriggerHolds(t, ctx, store, client, baseTime.Add(time.Hour))
	got := reloaded(t, ctx, store, task.ID)
	if got.AutoMerge {
		t.Fatal("human: required was released without a human")
	}
	if pr := strings.Join(prCommentBodies(t, client, got), "\n"); !strings.Contains(pr, "requires a human") {
		t.Errorf("the pull request does not say a human is needed: %s", pr)
	}
}

func TestGatedPathsUnderSeveralTriggersWaitForAllUnderTheStrictest(t *testing.T) {
	t.Parallel()
	ctx, store, _, client, task := finishGated(t, []model.PathTrigger{
		gateTrigger("docs", model.TriggerHumanNo, "docs"),
		gateTrigger("secrets", model.TriggerHumanOnFindings, "config/secrets"),
	}, "docs/a.md", "config/secrets/prod.yml")

	got := reloaded(t, ctx, store, task.ID)
	if v := linkOf(got, model.LinkPathTriggerHold); v != string(model.TriggerHumanOnFindings) {
		t.Fatalf("hold = %q, want the strictest (on-findings)", v)
	}
	ids := gatingIDs(got)
	if len(ids) != 2 {
		t.Fatalf("want a gating task per trigger, got %v", ids)
	}
	completeReview(t, ctx, store, ids[0])
	syncTriggerHolds(t, ctx, store, client, baseTime.Add(time.Hour))
	if reloaded(t, ctx, store, task.ID).AutoMerge {
		t.Fatal("released with one gating task still to finish")
	}
	completeReview(t, ctx, store, ids[1])
	syncTriggerHolds(t, ctx, store, client, baseTime.Add(2*time.Hour))
	if !reloaded(t, ctx, store, task.ID).AutoMerge {
		t.Fatal("not released once every gating task finished clean")
	}
}

func TestAPullRequestEditingItsTriggerIsStillJudgedByTheBasesCopy(t *testing.T) {
	t.Parallel()
	// The change rewrites the trigger so it watches nothing, and deletes
	// nothing else -- but the rule that judges it is the base's.
	ctx, store, _, client, task := finishGated(t,
		[]model.PathTrigger{gateTrigger("secrets", model.TriggerHumanOnFindings, "config/secrets")},
		"config/secrets/prod.yml", ".grain/triggers/secrets.yml")
	got := reloaded(t, ctx, store, task.ID)
	if got.AutoMerge || len(gatingIDs(got)) != 1 {
		t.Fatalf("a change editing its own trigger escaped it: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}

	// Editing `.grain/triggers` is also grain's own path-triggers
	// category (grain/task-1714): only the repository's owners may let it
	// through, so neither the security review nor the trigger's own task
	// finishing clean hands auto-merge back.
	pending := model.PendingApprovals(got)
	if len(pending) != 1 || pending[0].Rule != "builtin:path-triggers" {
		t.Fatalf("pending approvals = %+v, want builtin:path-triggers", pending)
	}
	if linkOf(got, model.LinkRepoRules) == "" {
		t.Error("the hold does not record which repository rules it was decided under")
	}
	// The path-triggers hold withdrew auto-merge first, with no hold link
	// of its own; the gate still holds the change rather than saying it
	// "was not merging automatically anyway" (grain/task-1829).
	if linkOf(got, model.LinkPathTriggerHold) == "" {
		t.Errorf("the gate did not record its hold under a human-only trust-boundary hold: %+v", got.Links)
	}
	for _, c := range commentBodies(t, ctx, store, task.ID) {
		if strings.Contains(c, "not merging automatically anyway") {
			t.Errorf("the gate's note says the change was not merging itself:\n%s", c)
		}
	}
	completeReview(t, ctx, store, linkOf(got, model.LinkSecurityReviewTask))
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	completeReview(t, ctx, store, gatingIDs(got)[0])
	syncTriggerHolds(t, ctx, store, client, baseTime.Add(2*time.Hour))
	if reloaded(t, ctx, store, task.ID).AutoMerge {
		t.Fatal("a change to the repository's triggers was released without its owners")
	}
}

func TestAPullRequestWhoseHeadMovesSupersedesItsUndispatchedTask(t *testing.T) {
	t.Parallel()
	ctx, store, sim, client, task := finishGated(t, []model.PathTrigger{{
		Name: "docs", On: model.TriggerOnPullRequest, Paths: []string{"docs"}, Template: templateID("planner"),
	}}, "docs/a.md")
	got := reloaded(t, ctx, store, task.ID)
	if !got.AutoMerge {
		t.Fatal("an ungated trigger held the change")
	}
	first := openTriggered(t, ctx, store)
	if len(first) != 1 {
		t.Fatalf("want one triggered task, got %d", len(first))
	}

	pushFile(t, sim.BareRepo, model.BranchName(task.ID), "docs/b.md", "b\n")
	head := branchSHA(t, sim.BareRepo, model.BranchName(task.ID))
	ref, err := model.ParsePullRequestRef(linkOf(got, model.LinkFixes))
	if err != nil {
		t.Fatal(err)
	}
	ok, err := orchestrator.JudgePullRequestTriggers(ctx, store, client, got, ref, head, baseTime.Add(time.Hour))
	if err != nil || !ok {
		t.Fatalf("judging the new head: ok=%v err=%v", ok, err)
	}
	open := openTriggered(t, ctx, store)
	if len(open) != 1 || open[0].ID == first[0].ID {
		t.Fatalf("want the first task superseded, open = %+v", open)
	}
	if !strings.Contains(open[0].Body, "docs/b.md") || !strings.Contains(open[0].Body, head) {
		t.Errorf("the new task is not about the new head:\n%s", open[0].Body)
	}
	if linkOf(reloaded(t, ctx, store, task.ID), model.LinkPathTriggersRead) != head {
		t.Error("the new head was not recorded as judged")
	}
}

// The tasks a pull-request trigger files for an external request's
// change -- a gating review stacked on it, or an ordinary task -- are
// that request's work, and local policy judges them so (GSA1-02).
func TestPullRequestTriggerTasksInheritTheChangesRequester(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	declareTriggers(t, ctx, store, branchSHA(t, sim.BareRepo, "main"),
		map[string]string{"sensitive": "Check {{trigger}} on {{pr}}", "planner": "Plan"},
		gateTrigger("secrets", model.TriggerHumanOnFindings, "config/secrets"),
		model.PathTrigger{Name: "plan", On: model.TriggerOnPullRequest, Paths: []string{"docs"},
			Template: templateID("planner"), AutoApprove: true})
	task := finishTouching(t, ctx, store, sim, "t1", "config/secrets/prod.yml", true)
	pushFile(t, sim.BareRepo, model.BranchName(task.ID), "docs/design.md", "changed\n")
	task.Creator = "friend@example.com"
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	ids := gatingIDs(reloaded(t, ctx, store, task.ID))
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, other := range tasks {
		if other.Parent() == task.ID && !strings.Contains(strings.Join(ids, ","), other.ID) {
			ids = append(ids, other.ID)
		}
	}
	if len(ids) != 2 {
		t.Fatalf("want a gating review and a planner task, got %v", ids)
	}
	for _, id := range ids {
		got := reloaded(t, ctx, store, id)
		if got.Creator != "friend@example.com" {
			t.Errorf("trigger task %s (%q): creator %q; want the change's requester",
				id, got.Title, got.Creator)
		}
	}
}
