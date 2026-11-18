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

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// A repository's merge rules at the merge gate (grain/task-1659): read
// from the base as the declaration sync stored them, applied per pull
// request by the files it touches.

func declareMergeRules(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef,
	rules *model.MergeRules) {

	t.Helper()
	if _, err := store.SyncRepoDeclarations(ctx, repo, "base1", model.RepoDeclarations{MergeRules: rules},
		baseTime, time.UTC); err != nil {
		t.Fatal(err)
	}
}

func TestAutoMergeNeverWaitsForAPersonsSubmit(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	declareMergeRules(t, ctx, store, repo, &model.MergeRules{Path: model.MergeRulesPath,
		Rules: []model.MergeRule{{ID: "deploy", Paths: []string{"deploy/**"},
			MergePolicy: model.MergePolicy{AutoMerge: model.AutoMergeNever}}}})

	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	pushFile(t, sim.BareRepo, branch, "deploy/main.tf", "resource {}\n")
	setMergeable(sim, true)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AutoMerge || sim.PullRequests[0].Merged {
		t.Fatalf("auto-merge = %v, merged = %v: auto_merge: never must wait for a person",
			got.AutoMerge, sim.PullRequests[0].Merged)
	}
	policy, ok := model.MergePolicyOf(*got)
	if !ok || !policy.AutoMergeNever || len(policy.Matched) != 1 || policy.Matched[0] != "deploy" {
		t.Fatalf("recorded policy = %+v (%v), want auto_merge never from deploy", policy, ok)
	}
	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	said := false
	for _, c := range comments {
		if strings.Contains(c.Body, "`auto_merge: never` -- from rule `deploy`") {
			said = true
		}
	}
	if !said {
		t.Fatal("the task was not told which rule withdrew its auto-merge")
	}

	// A person's Submit is what turns it on again, and that merges it.
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.AutoMerge = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if !sim.PullRequests[0].Merged {
		t.Fatal("a person submitted the change and it did not merge")
	}
}

func TestAChangeNoRuleWithholdsStillMergesItself(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	declareMergeRules(t, ctx, store, repo, &model.MergeRules{Path: model.MergeRulesPath,
		Rules: []model.MergeRule{{ID: "deploy", Paths: []string{"deploy/**"},
			MergePolicy: model.MergePolicy{AutoMerge: model.AutoMergeNever}}}})

	_, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	pushFile(t, sim.BareRepo, branch, "docs/readme.md", "hello\n")
	setMergeable(sim, true)
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatal(err)
	}
	if !sim.PullRequests[0].Merged {
		t.Fatal("a docs change no rule withholds did not merge")
	}
}

func TestAMissingRequiredGitHubCheckHoldsTheMergeAndIsNamed(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	declareMergeRules(t, ctx, store, repo, &model.MergeRules{Path: model.MergeRulesPath,
		Rules: []model.MergeRule{{ID: "deploy", Paths: []string{"deploy/**"},
			MergePolicy: model.MergePolicy{CI: &model.CIRule{GitHub: []string{"cla/google"}}}}}})

	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	pushFile(t, sim.BareRepo, branch, "deploy/main.tf", "resource {}\n")
	setMergeable(sim, true)
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatal(err)
	}
	if sim.PullRequests[0].Merged {
		t.Fatal("merged without the cla/google check its merge rules require")
	}
	obs := mergeQueueStatus(t, ctx, store, task.ID)
	if !strings.Contains(obs.MergeQueueNote, "`cla/google` (required by `deploy`)") {
		t.Fatalf("queue note = %q, want it to name the missing check and its rule", obs.MergeQueueNote)
	}
}

// A grain suite narrowed to named jobs passes on those jobs alone -- and
// only when every one of them was in the run and passed.
func TestANarrowedGrainSuitePassesOnlyOnItsNamedJobs(t *testing.T) {
	run := ci.Run{Jobs: []ci.JobRun{
		{Workflow: "checks", WorkflowPath: ".grain/workflows/checks.yml", JobID: "lint", Name: "lint",
			Conclusion: ci.ConclusionSuccess},
		{Workflow: "checks", WorkflowPath: ".grain/workflows/checks.yml", JobID: "go", Name: "go",
			Conclusion: ci.ConclusionFailure},
	}}
	if !orchestrator.NarrowedJobsPassed(run, []string{"lint"}) {
		t.Fatal("lint passed and is all that was asked for")
	}
	if orchestrator.NarrowedJobsPassed(run, []string{"lint", "go"}) {
		t.Fatal("go failed and was asked for")
	}
	if orchestrator.NarrowedJobsPassed(run, []string{"ui e2e"}) {
		t.Fatal("a job the run does not have is not a pass")
	}
	if orchestrator.NarrowedJobsPassed(run, []string{"checks"}) {
		t.Fatal("a workflow is every job in it, and one of them failed")
	}
}

// runsByID is a ci.Runner that only remembers runs.
type runsByID struct {
	ci.NopRunner
	runs map[string]ci.Run
}

func (r runsByID) Start(context.Context, ci.Request) (ci.Run, error) { return ci.Run{}, nil }
func (r runsByID) Get(id string) (ci.Run, bool)                      { run, ok := r.runs[id]; return run, ok }

// A verdict a flake recheck reached is read job by job off the run it
// rechecked with the recheck laid over it. The recheck re-runs only the
// failing jobs, so read alone it has no `lint` at all, and a docs-only
// change narrowed to `lint` was sent to repair over a `go` failure its
// rules say nothing about (docs/test-runs/2026-10-05-merge-rules.md, F1).
func TestANarrowedSuiteIsReadThroughAFlakeRecheck(t *testing.T) {
	job := func(id string, c ci.Conclusion) ci.JobRun {
		return ci.JobRun{Workflow: "checks", WorkflowPath: ".grain/workflows/checks.yml", JobID: id, Name: id,
			Conclusion: c}
	}
	runner := runsByID{runs: map[string]ci.Run{
		"first":   {ID: "first", Jobs: []ci.JobRun{job("lint", ci.ConclusionSuccess), job("go", ci.ConclusionFailure)}},
		"recheck": {ID: "recheck", Jobs: []ci.JobRun{job("go", ci.ConclusionFailure)}},
	}}
	run, ok := orchestrator.VerdictRunOf(runner, &model.CIStatus{RunID: "recheck", RecheckOf: "first"})
	if !ok {
		t.Fatal("both runs are remembered")
	}
	if !orchestrator.NarrowedJobsPassed(run, []string{"lint"}) {
		t.Fatalf("lint passed in the run the recheck rechecked: %+v", run.Jobs)
	}
	if orchestrator.NarrowedJobsPassed(run, []string{"go"}) {
		t.Fatal("go failed both times")
	}
	if _, ok := orchestrator.VerdictRunOf(runner, &model.CIStatus{RunID: "recheck", RecheckOf: "gone"}); ok {
		t.Fatal("a recheck whose first run is forgotten cannot be read job by job")
	}
}

// When a review hold already turned auto-merge off before SyncPullRequests
// reached withdrawForMergePolicy, no LinkMergePolicyWithdrawn is written yet.
// Even so:
//   - a merge:force override of the review gate must not bypass the
//     non-overridable Approval gate that `auto_merge: never` raises;
//   - a clean review finishing in the same cycle still leaves auto-merge off;
//   - a person's Submit (either after or before accepting review findings)
//     merges on the first Submit rather than being withdrawn by
//     withdrawForMergePolicy.
func TestAutoMergeNeverUnderReviewHoldBlocksForceOverrideAndHonorsSubmit(t *testing.T) {
	declareBoth := func(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef, baseCommit string) {
		t.Helper()
		decl := model.RepoDeclarations{
			MergeRules: &model.MergeRules{
				Path: model.MergeRulesPath,
				Rules: []model.MergeRule{{
					ID:          "frozen",
					Paths:       []string{"frozen/**"},
					MergePolicy: model.MergePolicy{AutoMerge: model.AutoMergeNever},
				}},
			},
			Triggers: []model.PathTrigger{{
				Name:        "payments",
				Path:        model.TriggerDeclarationDir + "/payments.yml",
				On:          model.TriggerOnPullRequest,
				Per:         model.TriggerPerChange,
				Paths:       []string{"payments/**"},
				Template:    model.SecurityReviewTemplateID,
				Gate:        true,
				Sensitive:   true,
				Human:       model.TriggerHumanOnFindings,
				AutoApprove: true,
			}},
		}
		if _, err := store.SyncRepoDeclarations(ctx, repo, baseCommit, decl, baseTime, time.UTC); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("force override cannot bypass auto_merge never and Submit after AcceptReviewFindings merges", func(t *testing.T) {
		store, ctx := openStore(t)
		sim, client := newSim(t, "acme", "widgets", "main")
		repo := model.RepoRef{Owner: "acme", Name: "widgets"}
		declareBoth(t, ctx, store, repo, branchSHA(t, sim.BareRepo, "main"))

		task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
		pushFile(t, sim.BareRepo, branch, "frozen/f.go", "package frozen\n")
		pushFile(t, sim.BareRepo, branch, "payments/pay.go", "package payments\n")
		head := branchSHA(t, sim.BareRepo, branch)
		setMergeable(sim, true)

		if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
			t.Fatal(err)
		}
		got := reloaded(t, ctx, store, task.ID)
		if got.AutoMerge || sim.PullRequests[0].Merged {
			t.Fatalf("autoMerge=%v merged=%v, want held", got.AutoMerge, sim.PullRequests[0].Merged)
		}
		if w := linkOf(got, model.LinkMergePolicyWithdrawn); w != "" {
			t.Fatalf("unexpected LinkMergePolicyWithdrawn=%q while review hold already had auto-merge off", w)
		}

		// Overriding targeted-review and ci under merge:force without a human's
		// Submit must still be blocked by GateApproval under auto_merge: never.
		forceAt := baseTime.Add(time.Minute)
		if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
			tk.Links = append(tk.Links, (model.GateOverrideRecord{
				Gates:   []string{model.GateTargetedReview, model.GateCI},
				HeadSHA: head,
				By:      "agent:runner",
				Reason:  "try to bypass auto_merge: never",
				At:      forceAt,
			}).Link())
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.ObserveField(ctx, task.ID, forceAt, func(o *model.Observation) {
			o.ForceMergeAt = &forceAt
			o.ForceMergeHeadSHA = head
			o.ForceMergeBy = "agent:runner"
			o.ForceMergeReason = "try to bypass auto_merge: never"
		}); err != nil {
			t.Fatal(err)
		}
		if err := orchestrator.SyncPullRequests(ctx, store, client, forceAt); err != nil {
			t.Fatal(err)
		}
		if sim.PullRequests[0].Merged {
			t.Fatal("merge:force override of targeted-review merged an auto_merge: never change without a person's Submit")
		}

		// Clear the override, simulate the targeted review leaving findings and
		// the owner accepting them, then pressing Submit once.
		ids := gatingIDs(reloaded(t, ctx, store, task.ID))
		if len(ids) != 1 {
			t.Fatalf("gating review ids = %v, want 1", ids)
		}
		reviewID := ids[0]
		completeReview(t, ctx, store, reviewID)
		if err := store.ObserveField(ctx, task.ID, forceAt, func(o *model.Observation) {
			o.ForceMergeAt = nil
			o.ForceMergeHeadSHA = ""
		}); err != nil {
			t.Fatal(err)
		}
		acceptAt := baseTime.Add(2 * time.Minute)
		if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
			var filtered []model.Link
			for _, l := range tk.Links {
				if l.Kind != model.LinkGateOverride && l.Kind != model.LinkPathTriggerHold {
					filtered = append(filtered, l)
				}
			}
			tk.Links = filtered
			model.AddReviewFindings(tk, model.ReviewFindingsRecord{
				Gate:       "trigger:payments",
				ReviewTask: reviewID,
				Summary:    "check negative amount",
				HeadSHA:    head,
				At:         acceptAt,
			})
			if !model.AcceptReviewFindings(tk, model.ReviewFindingsAcceptance{
				Gate:       model.GateTargetedReview,
				ReviewTask: reviewID,
				Reason:     "validated upstream",
				By:         "alice@example.com",
				HeadSHA:    head,
				At:         acceptAt,
			}) {
				t.Fatal("AcceptReviewFindings returned false")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if reloaded(t, ctx, store, task.ID).AutoMerge {
			t.Fatal("AcceptReviewFindings restored auto-merge under auto_merge: never")
		}

		submitAt := baseTime.Add(3 * time.Minute)
		if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
			tk.Links = append(tk.Links, model.Link{
				Kind:   model.LinkHumanSubmitted,
				Target: model.HumanSubmittedByTarget("alice@example.com", submitAt, head),
			})
			tk.AutoMerge = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := orchestrator.SyncPullRequests(ctx, store, client, submitAt); err != nil {
			t.Fatal(err)
		}
		if !sim.PullRequests[0].Merged {
			t.Fatal("a person's first Submit after accepting findings was withdrawn instead of merging")
		}
	})

	t.Run("Submit before AcceptReviewFindings merges once findings are accepted", func(t *testing.T) {
		store, ctx := openStore(t)
		sim, client := newSim(t, "acme", "widgets", "main")
		repo := model.RepoRef{Owner: "acme", Name: "widgets"}
		declareBoth(t, ctx, store, repo, branchSHA(t, sim.BareRepo, "main"))

		task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)
		pushFile(t, sim.BareRepo, branch, "frozen/f.go", "package frozen\n")
		pushFile(t, sim.BareRepo, branch, "payments/pay.go", "package payments\n")
		head := branchSHA(t, sim.BareRepo, branch)
		setMergeable(sim, true)

		if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
			t.Fatal(err)
		}
		ids := gatingIDs(reloaded(t, ctx, store, task.ID))
		reviewID := ids[0]
		completeReview(t, ctx, store, reviewID)

		findingsAt := baseTime.Add(time.Minute)
		if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
			var filtered []model.Link
			for _, l := range tk.Links {
				if l.Kind != model.LinkPathTriggerHold {
					filtered = append(filtered, l)
				}
			}
			tk.Links = filtered
			model.AddReviewFindings(tk, model.ReviewFindingsRecord{
				Gate:       "trigger:payments",
				ReviewTask: reviewID,
				Summary:    "check negative amount",
				HeadSHA:    head,
				At:         findingsAt,
			})
			// Human presses Submit while findings still stand: LinkHumanSubmitted
			// is recorded with AutoMerge still false.
			tk.Links = append(tk.Links, model.Link{
				Kind:   model.LinkHumanSubmitted,
				Target: model.HumanSubmittedByTarget("alice@example.com", findingsAt, head),
			})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := orchestrator.SyncPullRequests(ctx, store, client, findingsAt); err != nil {
			t.Fatal(err)
		}
		if sim.PullRequests[0].Merged {
			t.Fatal("merged while targeted-review findings still stood")
		}

		acceptAt := baseTime.Add(2 * time.Minute)
		if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
			if !model.AcceptReviewFindings(tk, model.ReviewFindingsAcceptance{
				Gate:       model.GateTargetedReview,
				ReviewTask: reviewID,
				Reason:     "validated upstream",
				By:         "alice@example.com",
				HeadSHA:    head,
				At:         acceptAt,
			}) {
				t.Fatal("AcceptReviewFindings returned false")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := orchestrator.SyncPullRequests(ctx, store, client, acceptAt); err != nil {
			t.Fatal(err)
		}
		if !sim.PullRequests[0].Merged {
			t.Fatal("Submit pressed before accepting findings was withdrawn instead of merging once findings were accepted")
		}
	})

	t.Run("force override on later cycle still requires Submit and merges once Submitted", func(t *testing.T) {
		store, ctx := openStore(t)
		sim, client := newSim(t, "acme", "widgets", "main")
		repo := model.RepoRef{Owner: "acme", Name: "widgets"}
		declareMergeRules(t, ctx, store, repo, &model.MergeRules{
			Path: model.MergeRulesPath,
			Rules: []model.MergeRule{{
				ID:          "frozen",
				Paths:       []string{"frozen/**"},
				MergePolicy: model.MergePolicy{AutoMerge: model.AutoMergeNever},
			}},
		})

		task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t3", repo)
		pushFile(t, sim.BareRepo, branch, "frozen/f.go", "package frozen\n")
		head := branchSHA(t, sim.BareRepo, branch)
		setMergeable(sim, true)

		// Cycle 1 withdraws auto-merge and records LinkMergePolicyWithdrawn.
		if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
			t.Fatal(err)
		}
		if sim.PullRequests[0].Merged {
			t.Fatal("merged without Submit on cycle 1")
		}

		// Cycle 2 with a merge:force override of CI still must not bypass the
		// unsubmitted Approval gate once LinkMergePolicyWithdrawn is already set.
		forceAt := baseTime.Add(time.Minute)
		if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
			tk.Links = append(tk.Links, (model.GateOverrideRecord{
				Gates:   []string{model.GateCI},
				HeadSHA: head,
				By:      "agent:runner",
				Reason:  "force CI without human Submit",
				At:      forceAt,
			}).Link())
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.ObserveField(ctx, task.ID, forceAt, func(o *model.Observation) {
			o.ForceMergeAt = &forceAt
			o.ForceMergeHeadSHA = head
			o.ForceMergeBy = "agent:runner"
			o.ForceMergeReason = "force CI without human Submit"
		}); err != nil {
			t.Fatal(err)
		}
		if err := orchestrator.SyncPullRequests(ctx, store, client, forceAt); err != nil {
			t.Fatal(err)
		}
		if sim.PullRequests[0].Merged {
			t.Fatal("cycle 2 force-merge bypassed auto_merge: never without Submit")
		}

		// Once a person submits that head, the forced merge proceeds.
		submitAt := baseTime.Add(2 * time.Minute)
		if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
			tk.Links = append(tk.Links, model.Link{
				Kind:   model.LinkHumanSubmitted,
				Target: model.HumanSubmittedByTarget("alice@example.com", submitAt, head),
			})
			tk.AutoMerge = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := orchestrator.SyncPullRequests(ctx, store, client, submitAt); err != nil {
			t.Fatal(err)
		}
		if !sim.PullRequests[0].Merged {
			t.Fatal("forced merge did not proceed after a person submitted the head")
		}
	})
}
