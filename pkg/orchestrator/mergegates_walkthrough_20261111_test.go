// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package orchestrator_test

// The 2026-11-11 §4 walkthrough (docs/test-runs/2026-11-11-merge-gates.md)
// as one deterministic story: a change whose pull request touches a
// trust-boundary path and two gating triggers' paths (each naming its own
// review template) goes through every gate on the GitHub simulator, with
// Submit and the gate overrides made through the real ui.Client, and the
// review runs played by a scripted agent through ProcessResult.

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func walkUIClient(store *model.Store, gh github.Client, now time.Time) *ui.Client {
	c := ui.NewClient(ui.Config{Actor: ui.DefaultActor("alice@example.com"), GitHubPullRequests: gh}, store)
	c.Now = func() time.Time { return now }
	return c
}

func TestMergeGatesWalkthrough20261111(t *testing.T) {
	t.Parallel()
	secrets := gateTrigger("secrets", model.TriggerHumanOnFindings, "config/secrets")
	plans := gateTrigger("plans", model.TriggerHumanOnFindings, "docs")
	plans.Template = templateID("planner")
	ctx, store, sim, client, task := finishGated(t, []model.PathTrigger{secrets, plans},
		"pkg/gitproxy/proxy.go", "config/secrets/prod.yml", "docs/plan.md")
	branch := model.BranchName(task.ID)
	setMergeable(sim, true)
	sim.CheckRuns[branch] = greenCheck

	// 1. Exactly one review per kind: one security review, one review per
	// trigger template.
	got := reloaded(t, ctx, store, task.ID)
	secID := linkOf(got, model.LinkSecurityReviewTask)
	if secID == "" {
		t.Fatalf("no security review for a trust-boundary change: links %+v", got.Links)
	}
	trig := distinctGatingIDs(got)
	if len(trig) != 2 {
		t.Fatalf("trigger reviews = %v, want one per template (2)", trig)
	}
	t.Logf("security review %s, trigger reviews %v", secID, trig)
	for _, id := range trig {
		r := reloaded(t, ctx, store, id)
		if linkOf(r, model.LinkReviewCommentsOnly) != task.ID {
			t.Errorf("trigger review %s may open a pull request: links %+v", id, r.Links)
		}
		if _, err := orchestrator.OpenPullRequestForTask(ctx, store, client, r); err == nil {
			t.Errorf("trigger review %s opened a pull request", id)
		}
	}
	sec := reloaded(t, ctx, store, secID)
	t.Logf("security review links %+v branch %q base %q", sec.Links, sec.Branch, sec.Base)

	// Re-judging the same head files nothing.
	judgeHead(t, ctx, store, client, sim.BareRepo, task, baseTime.Add(2*time.Minute))
	if again := distinctGatingIDs(reloaded(t, ctx, store, task.ID)); !slices.Equal(again, trig) {
		t.Fatalf("re-judging the same head changed the reviews: %v -> %v", trig, again)
	}
	if n := len(openReviews(t, ctx, store, task.ID)); n != 2 {
		t.Fatalf("open trigger reviews after re-judging = %d, want 2", n)
	}

	// 2. Submit before the reviews finish is recorded and merges nothing.
	ref := model.TaskPullRequest(got)
	c := walkUIClient(store, client, baseTime.Add(3*time.Minute))
	res, err := c.SubmitPullRequest(ctx, ui.SubmitPullRequestRequest{PR: ref.String()})
	if err != nil {
		t.Fatalf("SubmitPullRequest: %v", err)
	}
	t.Logf("submit: %+v", res)
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if mergedBranch(sim, branch) {
		t.Fatal("merged after Submit with three reviews still running")
	}

	// 3. The security review and the planner review pass; the secrets
	// review leaves a finding. The finding keeps the PR open after Submit.
	var secretsID, plannerID string
	for _, id := range trig {
		if strings.Contains(strings.Join(reloaded(t, ctx, store, id).Tags, ","), "sensitive") {
			secretsID = id
		} else {
			plannerID = id
		}
	}
	if secretsID == "" || plannerID == "" {
		for _, id := range trig {
			t.Logf("review %s tags %v", id, reloaded(t, ctx, store, id).Tags)
		}
		t.Fatal("cannot tell the two trigger reviews apart")
	}
	completeReview(t, ctx, store, secID)
	completeReview(t, ctx, store, plannerID)
	secrets3 := reloaded(t, ctx, store, secretsID)
	if err := orchestrator.ProcessResult(ctx, store, client, secrets3,
		toolResult(reviewFinding("plaintext token", "config/secrets/prod.yml", 1)),
		secretsID+"-1", baseTime.Add(10*time.Minute)); err != nil {
		t.Fatalf("ProcessResult(secrets review): %v", err)
	}
	completeReview(t, ctx, store, secretsID)
	walkSync(t, ctx, store, client, baseTime.Add(20*time.Minute))
	if mergedBranch(sim, branch) {
		t.Fatal("merged with a findings-blocked trigger review after Submit")
	}
	logGates(t, ctx, store, task.ID, "after findings")
	for _, l := range reloaded(t, ctx, store, task.ID).Links {
		t.Logf("link %s = %s", l.Kind, l.Target)
	}
	t.Logf("secrets review tags %v", reloaded(t, ctx, store, secretsID).Tags)
	if g := gateOf(t, ctx, store, task.ID, model.GateReview); g.State != model.GateStateFindings {
		t.Fatalf("review gate = %+v, want findings", g)
	}
	if g := gateOf(t, ctx, store, task.ID, model.GateSecurityReview); g.State != model.GateStatePassed {
		t.Fatalf("security-review gate = %+v, want passed", g)
	}

	// 4. A new head files an incremental re-review only after the debounce.
	pushFile(t, sim.BareRepo, branch, "config/secrets/prod.yml", "rotated\n")
	sim.CheckRuns[branch] = greenCheck
	at := baseTime.Add(30 * time.Minute)
	head2 := judgeHead(t, ctx, store, client, sim.BareRepo, task, at)
	reheld := reloaded(t, ctx, store, task.ID)
	ids2 := distinctGatingIDs(reheld)
	t.Logf("after push %s: gating reviews %v", head2[:12], ids2)
	var rereview model.Task
	for _, id := range ids2 {
		if id != secretsID && id != plannerID {
			rereview = reloaded(t, ctx, store, id)
		}
	}
	if rereview.ID == "" {
		t.Fatalf("no re-review filed for the new head: %v", ids2)
	}
	if rereview.DeferredUntil == nil || !rereview.DeferredUntil.After(at) {
		t.Fatalf("re-review %s not debounced: deferred until %v", rereview.ID, rereview.DeferredUntil)
	}
	if !strings.Contains(rereview.Body, "This is an incremental review.") {
		t.Errorf("re-review %s is not incremental:\n%s", rereview.ID, rereview.Body)
	}
	// Re-judging the new head again files nothing more.
	judgeHead(t, ctx, store, client, sim.BareRepo, task, at.Add(time.Minute))
	if again := distinctGatingIDs(reloaded(t, ctx, store, task.ID)); !slices.Equal(again, ids2) {
		t.Fatalf("re-judging head %s refiled: %v -> %v", head2[:12], ids2, again)
	}
	logGates(t, ctx, store, task.ID, "after new head")

	// 5. Every review of the new head passes; with Submit given earlier
	// the PR merges by itself -- or says why it waits.
	newSec := linkOf(reloaded(t, ctx, store, task.ID), model.LinkSecurityReviewTask)
	pending := append([]string{}, ids2...)
	if newSec != secID {
		pending = append(pending, newSec)
	}
	for i, id := range pending {
		r := reloaded(t, ctx, store, id)
		t.Logf("review %s of the new head: tags %v deferred %v", id, r.Tags, r.DeferredUntil)
		if _, after, ok := strings.Cut(r.Body, "This is an incremental review."); ok {
			t.Logf("review %s incremental part: %.300q", id, after)
		}
		if err := orchestrator.ProcessResult(ctx, store, client, r,
			toolResult(agent.ToolCall{Name: "run_command", Text: "reviewed"}),
			id+"-1", baseTime.Add(time.Duration(40+i)*time.Minute)); err != nil {
			t.Fatalf("ProcessResult(review %s): %v", id, err)
		}
		completeReview2(t, ctx, store, id, baseTime.Add(time.Duration(40+i)*time.Minute))
	}
	walkSync(t, ctx, store, client, baseTime.Add(50*time.Minute))
	walkSync(t, ctx, store, client, baseTime.Add(55*time.Minute))
	logGates(t, ctx, store, task.ID, "after clean re-reviews")
	if !mergedBranch(sim, branch) {
		for _, l := range reloaded(t, ctx, store, task.ID).Links {
			t.Logf("link %s = %s", l.Kind, l.Target)
		}
		t.Fatalf("not merged after every review of the new head passed")
	}
}

func completeReview2(t *testing.T, ctx context.Context, store *model.Store, id string, at time.Time) {
	t.Helper()
	if err := store.Observe(ctx, model.Observation{TaskID: id, CompletedAt: &at}); err != nil {
		t.Fatal(err)
	}
}

func walkSync(t *testing.T, ctx context.Context, store *model.Store, client github.Client, at time.Time) {
	t.Helper()
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, at); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.SyncPathTriggerHolds(ctx, store, client, at); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, at); err != nil {
		t.Fatal(err)
	}
}

func gatesOf(t *testing.T, ctx context.Context, store *model.Store, id string) []model.MergeGate {
	t.Helper()
	tk := reloaded(t, ctx, store, id)
	obs, err := store.GetObservation(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	states, err := store.States(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return model.ComputeMergeGates(tk, obs, states)
}

func gateOf(t *testing.T, ctx context.Context, store *model.Store, id, gate string) model.MergeGate {
	t.Helper()
	for _, g := range gatesOf(t, ctx, store, id) {
		if g.ID == gate {
			return g
		}
	}
	return model.MergeGate{}
}

func logGates(t *testing.T, ctx context.Context, store *model.Store, id, when string) {
	t.Helper()
	var parts []string
	for _, g := range gatesOf(t, ctx, store, id) {
		parts = append(parts, g.ID+"="+string(g.State)+" ("+g.Detail+")")
	}
	t.Logf("gates %s: %s", when, strings.Join(parts, "; "))
}

// §4 items 2 and 4: every override refusal, then a valid partial override
// that skips only the gate it names, and the trigger review it skipped
// still running post-merge and filing its findings as a follow-up.
func TestMergeGatesWalkthrough20261111Overrides(t *testing.T) {
	t.Parallel()
	ctx, store, sim, client, task := finishGated(t,
		[]model.PathTrigger{gateTrigger("secrets", model.TriggerHumanOnFindings, "config/secrets")},
		"pkg/gitproxy/proxy.go", "config/secrets/prod.yml")
	branch := model.BranchName(task.ID)
	setMergeable(sim, true)
	sim.CheckRuns[branch] = greenCheck
	got := reloaded(t, ctx, store, task.ID)
	secID := linkOf(got, model.LinkSecurityReviewTask)
	trigID := distinctGatingIDs(got)[0]
	ref := model.TaskPullRequest(got).String()
	head := prHeadOf(t, client, got)
	c := walkUIClient(store, client, baseTime.Add(3*time.Minute))

	// Missing a gate: security-review named, the trigger's review gate not.
	err := c.ForceMergeWithGates(ctx, task.ID, "outage", head, []string{"security-review"})
	t.Logf("override missing review gate: %v", err)
	if err == nil || !strings.Contains(err.Error(), "review") {
		t.Fatalf("override missing the review gate = %v, want refused", err)
	}
	// Empty reason, through both doors.
	err = c.ForceMergeWithGates(ctx, task.ID, "   ", head, []string{"security-review", "review"})
	t.Logf("override with blank reason: %v", err)
	if err == nil {
		t.Fatal("override with a blank reason accepted")
	}
	_, err = c.SubmitPullRequest(ctx, ui.SubmitPullRequestRequest{PR: ref, OverrideGates: []string{"security-review", "review"}})
	t.Logf("pr submit override without reason: %v", err)
	if err == nil {
		t.Fatal("pr submit override without a reason accepted")
	}
	// An unknown gate and the approval gate are not overridable.
	for _, g := range []string{"approval", "everything"} {
		err = c.ForceMergeWithGates(ctx, task.ID, "outage", head, []string{"security-review", "review", g})
		t.Logf("override naming %q: %v", g, err)
		if err == nil {
			t.Fatalf("override naming %q accepted", g)
		}
	}
	// An agent: neither Submit nor an override.
	agentClient := ui.NewClient(ui.Config{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "t9"}, GitHubPullRequests: client}, store)
	for _, req := range []ui.SubmitPullRequestRequest{
		{PR: ref},
		{PR: ref, OverrideGates: []string{"security-review", "review"}, Reason: "outage"},
	} {
		_, err = agentClient.SubmitPullRequest(ctx, req)
		t.Logf("agent pr submit %+v: %v", req.OverrideGates, err)
		if err == nil {
			t.Fatalf("an agent's pr submit %+v was accepted", req)
		}
	}
	if ov := model.GateOverrides(reloaded(t, ctx, store, task.ID)); len(ov) != 0 {
		t.Fatalf("a refused override left a record: %+v", ov)
	}

	// The security review passes; the trigger review is still running.
	completeReview(t, ctx, store, secID)
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	logGates(t, ctx, store, task.ID, "before partial override")

	// A valid partial override names only the review gate.
	if _, err := c.SubmitPullRequest(ctx, ui.SubmitPullRequestRequest{
		PR: ref, OverrideGates: []string{"review"}, Reason: "release blocker, trigger review continues", Head: head,
	}); err != nil {
		t.Fatalf("valid partial override: %v", err)
	}
	logGates(t, ctx, store, task.ID, "after partial override")
	if g := gateOf(t, ctx, store, task.ID, model.GateSecurityReview); g.State != model.GateStatePassed {
		t.Fatalf("security-review gate = %+v, want passed on its own (not overridden)", g)
	}
	ov, ok := model.LatestGateOverride(reloaded(t, ctx, store, task.ID))
	if !ok || !slices.Equal(ov.Gates, []string{"review"}) || ov.By != "alice@example.com" || ov.Reason == "" {
		t.Fatalf("override record = %+v (%v), want review only, by alice, with the reason", ov, ok)
	}
	walkSync(t, ctx, store, client, baseTime.Add(6*time.Minute))
	if !mergedBranch(sim, branch) {
		t.Logf("comments: %q", commentBodies(t, ctx, store, task.ID))
		t.Fatal("the partial override did not merge")
	}
	if recs, err := store.ForceMergeRecords(ctx); err != nil || len(recs) != 1 || recs[0].By != "alice@example.com" {
		t.Fatalf("force-merge audit = %+v (%v)", recs, err)
	}

	// Item 4: the trigger review keeps running post-merge and files its
	// findings as a follow-up.
	if err := orchestrator.SyncReviews(ctx, store, baseTime.Add(7*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.State(ctx, trigID); st == model.StateClosed {
		t.Fatalf("trigger review %s closed unfinished at merge", trigID)
	}
	review := reloaded(t, ctx, store, trigID)
	t.Logf("post-merge trigger review: base %q branch %q links %+v", review.Base, review.Branch, review.Links)
	if err := orchestrator.ProcessResult(ctx, store, client, review,
		toolResult(reviewFinding("token logged in plain text", "config/secrets/prod.yml", 1)),
		trigID+"-1", baseTime.Add(10*time.Minute)); err != nil {
		t.Fatalf("ProcessResult(post-merge trigger review): %v", err)
	}
	all, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var followUp *model.Task
	for i := range all {
		if all[i].ID != secID && all[i].ID != trigID && all[i].ID != task.ID && linkOf(all[i], model.LinkProposedBy) == task.ID {
			followUp = &all[i]
		}
	}
	if followUp == nil {
		t.Fatal("no follow-up filed for the post-merge trigger review's findings")
	}
	t.Logf("follow-up %s %q base %q", followUp.ID, followUp.Title, followUp.Base)
	if !strings.Contains(followUp.Body, "token logged in plain text") || followUp.Base != "main" {
		t.Fatalf("follow-up = %+v", followUp)
	}
}

// §4 item 3: a findings-only trigger review that pushes anyway opens no
// pull request; grain's security review works on the change's own branch.
func TestMergeGatesWalkthrough20261111ReviewsOpenNoPullRequests(t *testing.T) {
	t.Parallel()
	ctx, store, sim, client, task := finishGated(t,
		[]model.PathTrigger{gateTrigger("secrets", model.TriggerHumanOnFindings, "config/secrets")},
		"pkg/gitproxy/proxy.go", "config/secrets/prod.yml")
	got := reloaded(t, ctx, store, task.ID)
	sec := reloaded(t, ctx, store, linkOf(got, model.LinkSecurityReviewTask))
	if linkOf(sec, model.LinkPushesReviewedBranch) != task.ID || sec.Branch != model.BranchName(task.ID) {
		t.Fatalf("security review does not fix in place: %+v", sec)
	}
	trigID := distinctGatingIDs(got)[0]
	review := reloaded(t, ctx, store, trigID)
	prsBefore := len(sim.PullRequests)
	pushFileCommit(t, sim.BareRepo, model.BranchName(review.ID), model.BranchName(task.ID),
		"config/secrets/prod.yml", "fixed anyway\n", "fix it anyway")
	if err := orchestrator.ProcessResult(ctx, store, client, review,
		toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"}), trigID+"-1", baseTime.Add(10*time.Minute)); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if linkOf(reloaded(t, ctx, store, trigID), model.LinkFixes) != "" || len(sim.PullRequests) != prsBefore {
		t.Fatalf("a findings-only review opened a pull request (%d -> %d)", prsBefore, len(sim.PullRequests))
	}
	completeReview(t, ctx, store, trigID)
	walkSync(t, ctx, store, client, baseTime.Add(20*time.Minute))
	logGates(t, ctx, store, task.ID, "after a findings-only review pushed and finished")
	for _, b := range commentBodies(t, ctx, store, trigID) {
		t.Logf("review thread: %.200q", b)
	}
}
