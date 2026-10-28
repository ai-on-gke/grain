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

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

func setupBaseRepairBypassEnv(t *testing.T, optIn bool, baseRed bool, isRepair bool) (
	context.Context, *model.Store, *githubsim.Sim, *github.RESTClient, model.Task, string) {

	t.Helper()
	return setupBaseRepairBypassEnvWith(t, optIn, baseRed, isRepair, nil)
}

// setupBaseRepairBypassEnvWith is setupBaseRepairBypassEnv with shape
// applied to the task before it is filed. Only isRepair registers the task
// as the red branch's repair (BaseBranchCI.RepairTaskID).
func setupBaseRepairBypassEnvWith(t *testing.T, optIn bool, baseRed bool, isRepair bool, shape func(*model.Task)) (
	context.Context, *model.Store, *githubsim.Sim, *github.RESTClient, model.Task, string) {

	t.Helper()
	return setupBaseRepairBypassEnvTouching(t, optIn, baseRed, isRepair, shape, [][2]string{
		{".grain/workflows/gcp-smoke.yml", "name: smoke\n"},
		{"pkg/ci/basegate.go", "package ci\n"},
	})
}

// setupBaseRepairBypassEnvTouching is setupBaseRepairBypassEnvWith with the
// repair's pull request touching files (path, content) rather than both a
// gated workflow declaration and a trust-boundary package.
func setupBaseRepairBypassEnvTouching(t *testing.T, optIn bool, baseRed bool, isRepair bool, shape func(*model.Task), files [][2]string) (
	context.Context, *model.Store, *githubsim.Sim, *github.RESTClient, model.Task, string) {

	t.Helper()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	if optIn {
		if err := store.PutRepoConfig(ctx, model.RepoConfig{
			Repo:                         repo,
			BaseRepairBypassesHumanGates: true,
		}); err != nil {
			t.Fatalf("PutRepoConfig: %v", err)
		}
	}

	sensitiveTrigger := model.PathTrigger{
		Name:        "grain-declarations",
		On:          model.TriggerOnPullRequest,
		Paths:       []string{".grain/workflows"},
		Template:    templateID("sensitive"),
		Gate:        true,
		Human:       model.TriggerHumanRequired,
		Sensitive:   true,
		Approvers:   []string{model.ApproverOwner},
		AutoApprove: true,
	}
	declareTriggers(t, ctx, store, branchSHA(t, sim.BareRepo, "main"),
		map[string]string{"sensitive": "Check {{trigger}} on {{pr}}"}, sensitiveTrigger)

	task := filedTask(t, ctx, store, "repair1", repo)
	task.Base = "main"
	task.AutoMerge = true
	if isRepair {
		task.Title = "Fix grain CI on acme/widgets main"
		task.Tags = append(task.Tags, model.MarkedBaseRepairTag, orchestrator.BaseCIRepairBranchTag(repo, "main"))
	} else {
		task.Title = "Add a new feature"
	}
	if shape != nil {
		shape(&task)
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("PutTask: %v", err)
	}

	if baseRed {
		fin := baseTime.Add(-10 * time.Minute)
		repairID := "other-repair"
		if isRepair {
			repairID = task.ID
		}
		if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
			Repo:         repo,
			Branch:       "main",
			Commit:       branchSHA(t, sim.BareRepo, "main"),
			RunID:        "ci-base-red",
			StartedAt:    baseTime.Add(-15 * time.Minute),
			FinishedAt:   &fin,
			Conclusion:   model.BaseBranchCIFailure,
			Confirmation: model.BaseCIConfirmed,
			Detail:       "duplicate capabilities key in .grain/workflows/gcp-smoke.yml",
			RepairTaskID: repairID,
		}); err != nil {
			t.Fatalf("PutBaseBranchCI: %v", err)
		}
	}

	branch := model.BranchName(task.ID)
	pushBranch(t, sim.BareRepo, branch)
	for _, f := range files {
		pushFile(t, sim.BareRepo, branch, f[0], f[1])
	}
	sim.CheckRuns[branch] = greenCheck

	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "repair1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	setMergeable(sim, true)
	sim.CheckRuns[branch] = greenCheck
	return ctx, store, sim, client, task, branch
}

func TestBaseRepairBypassOptionOffRequiresHuman(t *testing.T) {
	t.Parallel()
	ctx, store, sim, client, task, branch := setupBaseRepairBypassEnv(t, false, true, true)

	got := reloaded(t, ctx, store, task.ID)
	if got.AutoMerge {
		t.Fatal("with option off, base-repair touching sensitive trigger and trust boundary kept AutoMerge")
	}
	if linkOf(got, model.LinkBaseRepairBypass) != "" {
		t.Fatalf("with option off, task unexpectedly carries LinkBaseRepairBypass: %+v", got.Links)
	}
	if len(model.PendingApprovals(got)) == 0 {
		t.Fatalf("with option off, expected pending human approvals: %+v", got.Links)
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if mergedBranch(sim, branch) {
		t.Fatal("with option off, base-repair PR merged without a human")
	}
}

func TestBaseRepairBypassOptionOnMergesOnGreenCIDespiteSensitiveTriggerAndTrustBoundary(t *testing.T) {
	t.Parallel()
	ctx, store, sim, client, task, branch := setupBaseRepairBypassEnv(t, true, true, true)

	got := reloaded(t, ctx, store, task.ID)
	if !got.AutoMerge {
		t.Fatalf("with option on + base-repair + red base, AutoMerge was withdrawn: links=%+v", got.Links)
	}
	if linkOf(got, model.LinkBaseRepairBypass) == "" {
		t.Fatalf("expected LinkBaseRepairBypass on task, got links=%+v", got.Links)
	}
	if linkOf(got, model.LinkSecurityHold) != "" || linkOf(got, model.LinkPathTriggerHold) != "" {
		t.Fatalf("expected no security or path-trigger hold link, got links=%+v", got.Links)
	}
	if pending := model.PendingApprovals(got); len(pending) != 0 {
		t.Fatalf("expected no pending human approvals on bypassed repair, got %+v", pending)
	}

	// Both the security review and the trigger review task must still be filed and recorded.
	secReviewID := linkOf(got, model.LinkSecurityReviewTask)
	if secReviewID == "" {
		t.Fatalf("expected security review task to still be filed, got links=%+v", got.Links)
	}
	gateIDs := gatingIDs(got)
	if len(gateIDs) != 1 {
		t.Fatalf("expected 1 path-trigger review task to still be filed, got %v", gateIDs)
	}

	// Both the task and PR must clearly record that the human gate was bypassed under base_repair_bypasses_human_gates
	// and which triggers/paths were bypassed.
	taskNotes := strings.Join(commentBodies(t, ctx, store, task.ID), "\n")
	for _, want := range []string{
		"base_repair_bypasses_human_gates",
		"grain-declarations",
		".grain/workflows/gcp-smoke.yml",
		"pkg/ci/basegate.go",
		secReviewID,
		gateIDs[0],
	} {
		if !strings.Contains(taskNotes, want) {
			t.Errorf("task comments missing %q:\n%s", want, taskNotes)
		}
	}
	prNotes := strings.Join(prCommentBodies(t, client, got), "\n")
	for _, want := range []string{
		"base_repair_bypasses_human_gates",
		"grain-declarations",
		".grain/workflows/gcp-smoke.yml",
		"pkg/ci/basegate.go",
		secReviewID,
		gateIDs[0],
	} {
		if !strings.Contains(prNotes, want) {
			t.Errorf("PR comments missing %q:\n%s", want, prNotes)
		}
	}

	// On green CI, the PR merges without waiting for a human or the reviews.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if !mergedBranch(sim, branch) {
		t.Fatalf("expected bypassed base-repair PR to merge on green CI; links=%+v", reloaded(t, ctx, store, task.ID).Links)
	}

	// After the PR merges, the filed security review and trigger review stay open (not closed as obsolete)
	// so they can finish and report findings if any.
	if err := orchestrator.SyncReviews(ctx, store, baseTime.Add(2*time.Minute)); err != nil {
		t.Fatalf("SyncReviews: %v", err)
	}
	for _, revID := range []string{secReviewID, gateIDs[0]} {
		st, err := store.State(ctx, revID)
		if err != nil {
			t.Fatalf("State(%s): %v", revID, err)
		}
		if st == model.StateClosed {
			t.Errorf("review task %s was closed when bypassed base-repair PR merged; want it to remain open to finish", revID)
		}
	}
}

func TestBaseRepairBypassOptionOnLeavesNonRepairTaskHeldForHuman(t *testing.T) {
	t.Parallel()
	ctx, store, sim, client, task, branch := setupBaseRepairBypassEnv(t, true, true, false)

	got := reloaded(t, ctx, store, task.ID)
	if got.AutoMerge {
		t.Fatal("non-repair task bypassed human gates when option was on")
	}
	if linkOf(got, model.LinkBaseRepairBypass) != "" {
		t.Fatalf("non-repair task unexpectedly carries LinkBaseRepairBypass: %+v", got.Links)
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if mergedBranch(sim, branch) {
		t.Fatal("non-repair task merged without human approval")
	}
}

func TestBaseRepairBypassBaseAlreadyGreenOrTurnsGreenBeforeMerge(t *testing.T) {
	t.Parallel()

	// Subcase 1: Base is already green when ProcessResult runs.
	t.Run("AlreadyGreen", func(t *testing.T) {
		t.Parallel()
		ctx, store, sim, client, task, branch := setupBaseRepairBypassEnv(t, true, false, true)
		got := reloaded(t, ctx, store, task.ID)
		if got.AutoMerge {
			t.Fatal("base-repair task bypassed human gates even though base branch was already green")
		}
		if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
			t.Fatalf("SyncPullRequests: %v", err)
		}
		if mergedBranch(sim, branch) {
			t.Fatal("base-repair task merged without human when base was already green")
		}
	})

	// Subcase 2: Base is red when ProcessResult runs, then turns green before the repair PR merges.
	t.Run("TurnsGreenBeforeMerge", func(t *testing.T) {
		t.Parallel()
		ctx, store, sim, client, task, branch := setupBaseRepairBypassEnv(t, true, true, true)
		got := reloaded(t, ctx, store, task.ID)
		if !got.AutoMerge || linkOf(got, model.LinkBaseRepairBypass) == "" {
			t.Fatalf("expected initial bypass while base is red: automerge=%v links=%+v", got.AutoMerge, got.Links)
		}

		// Now mark the base branch green before SyncPullRequests runs.
		repo := model.RepoRef{Owner: "acme", Name: "widgets"}
		fin := baseTime.Add(30 * time.Second)
		if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
			Repo:       repo,
			Branch:     "main",
			Commit:     branchSHA(t, sim.BareRepo, "main"),
			RunID:      "ci-base-green",
			StartedAt:  baseTime,
			FinishedAt: &fin,
			Conclusion: string(ci.ConclusionSuccess),
		}); err != nil {
			t.Fatalf("PutBaseBranchCI green: %v", err)
		}

		if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
			t.Fatalf("SyncPullRequests: %v", err)
		}
		if mergedBranch(sim, branch) {
			t.Fatal("repair PR still merged without a human after the base branch turned green")
		}
		after := reloaded(t, ctx, store, task.ID)
		if after.AutoMerge {
			t.Fatalf("expected AutoMerge to be withdrawn once base turned green: links=%+v", after.Links)
		}
		if linkOf(after, model.LinkBaseRepairBypass) != "" {
			t.Fatalf("expected LinkBaseRepairBypass to be removed once base turned green: links=%+v", after.Links)
		}
	})
}

func TestBaseRepairBypassReviewFindingsFileFollowUpTaskWithoutBlockingMerge(t *testing.T) {
	t.Parallel()
	ctx, store, sim, client, task, branch := setupBaseRepairBypassEnv(t, true, true, true)
	got := reloaded(t, ctx, store, task.ID)
	secReviewID := linkOf(got, model.LinkSecurityReviewTask)
	secReview := reloaded(t, ctx, store, secReviewID)

	// Security review finishes with findings BEFORE the repair PR merges.
	result := toolResult(reviewFinding("check capability parsing edge case", "pkg/ci/basegate.go", 1))
	if err := orchestrator.ProcessResult(ctx, store, client, secReview, result, secReviewID+"-1", baseTime.Add(30*time.Second)); err != nil {
		t.Fatalf("ProcessResult for security review: %v", err)
	}
	completeReview(t, ctx, store, secReviewID)

	// The repair task must STILL have AutoMerge = true (findings do NOT block the merge).
	afterReview := reloaded(t, ctx, store, task.ID)
	if !afterReview.AutoMerge {
		t.Fatalf("security review findings withdrew AutoMerge on bypassed base-repair task: links=%+v", afterReview.Links)
	}
	if linkOf(afterReview, model.LinkReviewQuestions) != "" {
		t.Fatalf("security review findings parked bypassed base-repair task on LinkReviewQuestions: links=%+v", afterReview.Links)
	}

	// A follow-up task must have been filed on main, and a loud alert posted on the task and PR.
	allTasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	var followUp *model.Task
	for i := range allTasks {
		tk := allTasks[i]
		if tk.Parent() == task.ID && tk.Origin.Reason == model.ReasonProposal {
			followUp = &tk
			break
		}
	}
	if followUp == nil {
		t.Fatalf("expected a follow-up task to be filed for the review findings; tasks=%+v", allTasks)
	}
	if followUp.Base != "main" || !strings.Contains(followUp.Body, "check capability parsing edge case") {
		t.Errorf("unexpected follow-up task: base=%q body=%s", followUp.Base, followUp.Body)
	}

	notes := strings.Join(commentBodies(t, ctx, store, task.ID), "\n")
	if !strings.Contains(notes, "ALERT: Review findings on bypassed base-branch repair") ||
		!strings.Contains(notes, followUp.ID) {
		t.Errorf("expected loud alert on repair task naming follow-up task %s:\n%s", followUp.ID, notes)
	}
	prNotes := strings.Join(prCommentBodies(t, client, afterReview), "\n")
	if !strings.Contains(prNotes, followUp.ID) {
		t.Errorf("expected loud alert on PR naming follow-up task %s:\n%s", followUp.ID, prNotes)
	}

	// And the repair PR still merges on green CI!
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if !mergedBranch(sim, branch) {
		t.Fatal("bypassed base-repair PR did not merge after review reported findings")
	}

	// Now test the trigger review finishing with findings AFTER the repair PR has already merged and closed.
	gateID := gatingIDs(afterReview)[0]
	if err := orchestrator.SyncReviews(ctx, store, baseTime.Add(90*time.Second)); err != nil {
		t.Fatalf("SyncReviews: %v", err)
	}
	gateReview := reloaded(t, ctx, store, gateID)
	// Still open once the repair has merged, it is moved onto the base
	// branch -- where nothing classifies a review's own pull request, so it
	// must not merge one there by itself.
	if gateReview.Base != "main" || gateReview.AutoMerge {
		t.Fatalf("review left open after the bypassed repair merged: base=%q autoMerge=%v, want base main and no auto-merge",
			gateReview.Base, gateReview.AutoMerge)
	}
	postMergeResult := toolResult(reviewFinding("remove duplicate capabilities key in workflow", ".grain/workflows/gcp-smoke.yml", 1))
	if err := orchestrator.ProcessResult(ctx, store, client, gateReview, postMergeResult, gateID+"-1", baseTime.Add(2*time.Minute)); err != nil {
		t.Fatalf("ProcessResult for post-merge trigger review: %v", err)
	}
	completeReview(t, ctx, store, gateID)

	allTasks, err = store.ListTasks(ctx)
	if err != nil {
		t.Fatalf("ListTasks after post-merge review: %v", err)
	}
	var postMergeFollowUp *model.Task
	for i := range allTasks {
		tk := allTasks[i]
		if tk.Parent() == task.ID && tk.Origin.Reason == model.ReasonProposal && tk.ID != followUp.ID {
			postMergeFollowUp = &tk
			break
		}
	}
	if postMergeFollowUp == nil {
		t.Fatalf("expected a follow-up task to be filed when review reported findings after merge; tasks=%+v", allTasks)
	}
	if !strings.Contains(postMergeFollowUp.Body, "remove duplicate capabilities key in workflow") {
		t.Errorf("unexpected post-merge follow-up task body: %s", postMergeFollowUp.Body)
	}
}

// A task does not become a repair by what it is called, by a repair tag for
// another branch, or by marking itself one under an agent's own approval:
// each of those is something an agent can arrange, and none may let its
// change past a human gate while the base branch is red.
func TestBaseRepairBypassIgnoresClaimsAnAgentCanMake(t *testing.T) {
	t.Parallel()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	for name, shape := range map[string]func(*model.Task){
		"title only": func(task *model.Task) {
			task.Title = "Fix grain CI on acme/widgets main"
		},
		"tag for another branch": func(task *model.Task) {
			task.Tags = append(task.Tags, orchestrator.BaseCIRepairBranchTag(repo, "candidate"))
		},
		"tag for another repository": func(task *model.Task) {
			task.Tags = append(task.Tags, orchestrator.BaseCIRepairBranchTag(model.RepoRef{Owner: "acme", Name: "other"}, "main"))
		},
		"marked by a self-approved proposal": func(task *model.Task) {
			task.Tags = append(task.Tags, model.MarkedBaseRepairTag, orchestrator.BaseCIRepairBranchTag(repo, "main"))
			task.Approval = &model.Attribution{Actor: model.AutoSubmitPrincipal}
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, store, sim, client, task, branch := setupBaseRepairBypassEnvWith(t, true, true, false, shape)
			got := reloaded(t, ctx, store, task.ID)
			if got.AutoMerge || linkOf(got, model.LinkBaseRepairBypass) != "" {
				t.Fatalf("bypass granted: autoMerge=%v links=%+v", got.AutoMerge, got.Links)
			}
			if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
				t.Fatalf("SyncPullRequests: %v", err)
			}
			if mergedBranch(sim, branch) {
				t.Fatal("merged without a human")
			}
		})
	}
}

// A person's mark counts: a task a human approved and marked as fixing its
// red base branch bypasses the gates like grain's own registered repair.
func TestBaseRepairBypassHonoursHumanMarkedRepair(t *testing.T) {
	t.Parallel()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	ctx, store, _, _, task, _ := setupBaseRepairBypassEnvWith(t, true, true, false, func(task *model.Task) {
		task.Tags = append(task.Tags, model.MarkedBaseRepairTag, orchestrator.BaseCIRepairBranchTag(repo, "main"))
	})
	got := reloaded(t, ctx, store, task.ID)
	if !got.AutoMerge || linkOf(got, model.LinkBaseRepairBypass) == "" {
		t.Fatalf("human-marked repair not bypassed: autoMerge=%v links=%+v", got.AutoMerge, got.Links)
	}
}

// docs/test-plans/2026-10-25.md section 3 (task 929): a red-base repair
// whose diff touches only ordinary files has no human gate to bypass. It
// merges on green CI whether or not the repository opted in, and no
// security review is filed and no bypass is recorded.
func TestBaseRepairOrdinaryFilesMergeOnGreenWithNothingToBypass(t *testing.T) {
	t.Parallel()
	for _, optIn := range []bool{false, true} {
		t.Run(map[bool]string{false: "option off", true: "option on"}[optIn], func(t *testing.T) {
			t.Parallel()
			ctx, store, sim, client, task, branch := setupBaseRepairBypassEnvTouching(t, optIn, true, true, nil,
				[][2]string{{"README.md", "fixed\n"}, {"internal/widgets/widgets.go", "package widgets\n"}})
			got := reloaded(t, ctx, store, task.ID)
			if !got.AutoMerge || linkOf(got, model.LinkBaseRepairBypass) != "" || linkOf(got, model.LinkSecurityReviewTask) != "" {
				t.Fatalf("autoMerge=%v links=%+v, want auto-merge on, no bypass and no review", got.AutoMerge, got.Links)
			}
			if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
				t.Fatalf("SyncPullRequests: %v", err)
			}
			if !mergedBranch(sim, branch) {
				t.Fatalf("an ordinary repair did not merge on green: links=%+v", reloaded(t, ctx, store, task.ID).Links)
			}
			if reviews := securityReviewsOf(t, ctx, store, task.ID); len(reviews) != 0 {
				t.Fatalf("filed %d security reviews for ordinary files", len(reviews))
			}
		})
	}
}

// The same repair touching only a path that is human-only even after a
// clean review (.grain/quarantine.yml, trustboundary's ci-suppressions):
// with the option off it is held for a human and does not merge. With it
// on and the base red, the bypass covers that path too, as the settings
// page warns. A security review is still filed and the bypass is recorded,
// and the change merges on green.
func TestBaseRepairHumanOnlyTrustBoundaryPath(t *testing.T) {
	t.Parallel()
	files := [][2]string{{".grain/quarantine.yml", "tests: []\n"}}
	t.Run("option off", func(t *testing.T) {
		t.Parallel()
		ctx, store, sim, client, task, branch := setupBaseRepairBypassEnvTouching(t, false, true, true, nil, files)
		got := reloaded(t, ctx, store, task.ID)
		if got.AutoMerge || linkOf(got, model.LinkSecurityReviewTask) == "" {
			t.Fatalf("not held: autoMerge=%v links=%+v", got.AutoMerge, got.Links)
		}
		if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
			t.Fatalf("SyncPullRequests: %v", err)
		}
		if mergedBranch(sim, branch) {
			t.Fatal("a human-only path merged without a human")
		}
	})
	t.Run("option on", func(t *testing.T) {
		t.Parallel()
		ctx, store, sim, client, task, branch := setupBaseRepairBypassEnvTouching(t, true, true, true, nil, files)
		got := reloaded(t, ctx, store, task.ID)
		if !got.AutoMerge || linkOf(got, model.LinkBaseRepairBypass) == "" || linkOf(got, model.LinkSecurityReviewTask) == "" {
			t.Fatalf("autoMerge=%v links=%+v, want auto-merge on, the bypass recorded and a review filed", got.AutoMerge, got.Links)
		}
		if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
			t.Fatalf("SyncPullRequests: %v", err)
		}
		if !mergedBranch(sim, branch) {
			t.Fatalf("the bypassed repair did not merge on green: links=%+v", reloaded(t, ctx, store, task.ID).Links)
		}
	})
}
