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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// distinctGatingIDs is gatingIDs without the repeats several triggers
// sharing one review leave.
func distinctGatingIDs(task model.Task) []string {
	var out []string
	for _, id := range gatingIDs(task) {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// judgeHead judges task's pull request at its branch's current head, as
// the merge queue does when it finds the head somewhere new.
func judgeHead(t *testing.T, ctx context.Context, store *model.Store, client github.Client,
	bare string, task model.Task, at time.Time) string {
	t.Helper()
	got := reloaded(t, ctx, store, task.ID)
	head := branchSHA(t, bare, model.BranchName(task.ID))
	ref, err := model.ParsePullRequestRef(linkOf(got, model.LinkFixes))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := orchestrator.JudgePullRequestTriggers(ctx, store, client, got, ref, head, at); err != nil {
		t.Fatalf("judging %s: %v", head, err)
	}
	return head
}

func openReviews(t *testing.T, ctx context.Context, store *model.Store, reviewedID string) []model.Task {
	t.Helper()
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var out []model.Task
	for _, task := range tasks {
		if task.Origin.Reason != model.ReasonReview || task.Parent() != reviewedID ||
			!slices.Contains(task.Tags, model.PathTriggerMarkerTag) {
			continue
		}
		st, err := store.State(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if st != model.StateClosed {
			out = append(out, task)
		}
	}
	return out
}

// Two gating triggers naming the same review and matching one head file
// one review between them, holding the change under the stricter.
func TestTwoTriggersMatchingOneHeadFileOneReview(t *testing.T) {
	t.Parallel()
	ctx, store, _, client, task := finishGated(t, []model.PathTrigger{
		gateTrigger("release-machinery", model.TriggerHumanOnFindings, "release"),
		gateTrigger("release-promotion", model.TriggerHumanRequired, "release/promote.yml"),
	}, "release/promote.yml", "release/build.sh")

	got := reloaded(t, ctx, store, task.ID)
	ids := distinctGatingIDs(got)
	if len(ids) != 1 || len(gatingIDs(got)) != 2 {
		t.Fatalf("want one review recorded for both triggers, got links %v", gatingIDs(got))
	}
	if v := linkOf(got, model.LinkPathTriggerHold); v != string(model.TriggerHumanRequired) {
		t.Errorf("hold = %q, want the strictest (required)", v)
	}
	if open := openReviews(t, ctx, store, task.ID); len(open) != 1 {
		t.Fatalf("want one open review, got %d", len(open))
	}
	review := reloaded(t, ctx, store, ids[0])
	for _, want := range []string{"trigger `release-machinery`", "trigger `release-promotion`",
		"`release/build.sh`", "`release/promote.yml`", "human: `required`"} {
		if !strings.Contains(review.Body, want) {
			t.Errorf("the review's body lacks %q:\n%s", want, review.Body)
		}
	}

	completeReview(t, ctx, store, review.ID)
	syncTriggerHolds(t, ctx, store, client, baseTime.Add(time.Hour))
	if reloaded(t, ctx, store, task.ID).AutoMerge {
		t.Fatal("human: required was released by the shared review alone")
	}
}

// Triggers naming different review templates file one review each.
func TestDistinctReviewTemplatesFileOneReviewEach(t *testing.T) {
	t.Parallel()
	planner := gateTrigger("plans", model.TriggerHumanOnFindings, "docs")
	planner.Template = templateID("planner")
	ctx, store, _, _, task := finishGated(t, []model.PathTrigger{
		gateTrigger("secrets", model.TriggerHumanOnFindings, "config/secrets"),
		gateTrigger("more-secrets", model.TriggerHumanOnFindings, "config"),
		planner,
	}, "config/secrets/prod.yml", "docs/a.md")

	if ids := distinctGatingIDs(reloaded(t, ctx, store, task.ID)); len(ids) != 2 {
		t.Fatalf("want one review per template, got %v", ids)
	}
	if open := openReviews(t, ctx, store, task.ID); len(open) != 2 {
		t.Fatalf("want two open reviews, got %d", len(open))
	}
}

// Judging a head that already has its review files nothing more.
func TestReJudgingTheSameHeadDoesNotRefileItsReview(t *testing.T) {
	t.Parallel()
	ctx, store, sim, client, task := finishGated(t,
		[]model.PathTrigger{gateTrigger("secrets", model.TriggerHumanOnFindings, "config/secrets")},
		"config/secrets/prod.yml")
	first := distinctGatingIDs(reloaded(t, ctx, store, task.ID))

	// Forget that this head was judged, as a restart between the filing
	// and the record would.
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		var kept []model.Link
		for _, l := range t.Links {
			if l.Kind != model.LinkPathTriggersRead {
				kept = append(kept, l)
			}
		}
		t.Links = kept
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	judgeHead(t, ctx, store, client, sim.BareRepo, task, baseTime.Add(time.Minute))

	if again := distinctGatingIDs(reloaded(t, ctx, store, task.ID)); !slices.Equal(first, again) {
		t.Fatalf("re-judging the same head changed its review: %v then %v", first, again)
	}
	if open := openReviews(t, ctx, store, task.ID); len(open) != 1 {
		t.Fatalf("want the one review, got %d", len(open))
	}
}

// A push after a review finished is reviewed as the delta since the head
// that review read, deferred until the head has been still a while.
func TestANewPushFilesAnIncrementalReviewOfTheDelta(t *testing.T) {
	t.Parallel()
	ctx, store, sim, client, task := finishGated(t,
		[]model.PathTrigger{gateTrigger("secrets", model.TriggerHumanOnFindings, "config/secrets")},
		"config/secrets/prod.yml")
	firstHead := branchSHA(t, sim.BareRepo, model.BranchName(task.ID))
	first := distinctGatingIDs(reloaded(t, ctx, store, task.ID))[0]
	completeReview(t, ctx, store, first)

	pushFile(t, sim.BareRepo, model.BranchName(task.ID), "config/secrets/staging.yml", "new\n")
	at := baseTime.Add(time.Hour)
	head := judgeHead(t, ctx, store, client, sim.BareRepo, task, at)

	ids := distinctGatingIDs(reloaded(t, ctx, store, task.ID))
	if len(ids) != 1 || ids[0] == first {
		t.Fatalf("want a new review of the new head, got %v", ids)
	}
	review := reloaded(t, ctx, store, ids[0])
	_, delta, _ := strings.Cut(review.Body, "This is an incremental review.")
	if delta == "" || !strings.Contains(delta, firstHead) || !strings.Contains(delta, head) {
		t.Fatalf("the review is not an incremental one from %s:\n%s", firstHead, review.Body)
	}
	if !strings.Contains(delta, "`config/secrets/staging.yml`") || strings.Contains(delta, "`config/secrets/prod.yml`") {
		t.Errorf("the incremental review lists the wrong files:\n%s", delta)
	}
	if !strings.Contains(delta, "Do not re-review the rest of the change") ||
		!strings.Contains(delta, "check the callers of anything those commits changed") {
		t.Errorf("the incremental review does not scope caller tracing to the new commits:\n%s", delta)
	}
	if review.DeferredUntil == nil || !review.DeferredUntil.After(at) {
		t.Errorf("the re-review was not debounced: deferred until %v", review.DeferredUntil)
	}
}

// A push while the last head's review is still queued supersedes that
// review rather than letting both run.
func TestAQueuedOlderHeadReviewIsSuperseded(t *testing.T) {
	t.Parallel()
	ctx, store, sim, client, task := finishGated(t,
		[]model.PathTrigger{gateTrigger("secrets", model.TriggerHumanOnFindings, "config/secrets")},
		"config/secrets/prod.yml")
	first := distinctGatingIDs(reloaded(t, ctx, store, task.ID))[0]

	pushFile(t, sim.BareRepo, model.BranchName(task.ID), "config/secrets/staging.yml", "new\n")
	judgeHead(t, ctx, store, client, sim.BareRepo, task, baseTime.Add(time.Minute))
	pushFile(t, sim.BareRepo, model.BranchName(task.ID), "config/secrets/dev.yml", "new\n")
	judgeHead(t, ctx, store, client, sim.BareRepo, task, baseTime.Add(2*time.Minute))

	open := openReviews(t, ctx, store, task.ID)
	if len(open) != 1 || open[0].ID == first {
		t.Fatalf("want only the newest head's review open, got %+v", open)
	}
	if st, err := store.State(ctx, first); err != nil || st != model.StateClosed {
		t.Errorf("the first head's review is %s (%v), want closed as superseded", st, err)
	}
	// The first review never ran, so the newest reads the change in full.
	if strings.Contains(open[0].Body, "This is an incremental review.") {
		t.Errorf("a review superseding one that never ran is incremental:\n%s", open[0].Body)
	}
}

// A trigger-filed review posts its findings and opens no pull request,
// unless its triggers ask for fixes; grain's security review fixes on the
// change's own branch, so it never opens one either.
func TestATriggerReviewOpensNoPullRequestUnlessAskedForFixes(t *testing.T) {
	t.Parallel()
	ctx, store, _, client, task := finishGated(t,
		[]model.PathTrigger{gateTrigger("secrets", model.TriggerHumanOnFindings, "config/secrets")},
		"config/secrets/prod.yml")
	review := reloaded(t, ctx, store, distinctGatingIDs(reloaded(t, ctx, store, task.ID))[0])
	if linkOf(review, model.LinkReviewCommentsOnly) != task.ID {
		t.Fatalf("the review may open a pull request: links %+v", review.Links)
	}
	if !strings.Contains(review.Body, "Do not commit, push or open a pull request") {
		t.Errorf("the review is not told to post findings only:\n%s", review.Body)
	}
	if strings.Contains(review.Body, "Commit and push those fixes") {
		t.Errorf("findings-only review still carries the stacked push paragraph:\n%s", review.Body)
	}
	if _, err := orchestrator.OpenPullRequestForTask(ctx, store, client, review); err == nil ||
		!strings.Contains(err.Error(), "post findings") {
		t.Errorf("opening a pull request for a findings-only review: err = %v", err)
	}

	fixing := gateTrigger("secrets", model.TriggerHumanOnFindings, "config/secrets")
	fixing.Fixes = true
	ctx, store, _, _, task = finishGated(t, []model.PathTrigger{fixing}, "config/secrets/prod.yml")
	review = reloaded(t, ctx, store, distinctGatingIDs(reloaded(t, ctx, store, task.ID))[0])
	if linkOf(review, model.LinkReviewCommentsOnly) != "" || review.Base != model.BranchName(task.ID) {
		t.Errorf("a trigger asking for fixes did not get the stacked fixing review: %+v", review)
	}

	security := gateTrigger("payments", model.TriggerHumanOnFindings, "config/payments")
	security.Template, security.Sensitive = model.SecurityReviewTemplateID, true
	ctx, store, _, _, task = finishGated(t, []model.PathTrigger{security}, "config/payments/keys.yml")
	review = reloaded(t, ctx, store, distinctGatingIDs(reloaded(t, ctx, store, task.ID))[0])
	if linkOf(review, model.LinkPushesReviewedBranch) != task.ID || review.Branch != model.BranchName(task.ID) {
		t.Errorf("the trigger's security review does not work on the change's own branch: %+v", review)
	}
}

// A catch-all gating trigger (`paths: ["**"]`, the shape that reviews every
// pull request) never files a review of a review: even one whose triggers
// ask for fixes, so that it opens a pull request of its own and pushes
// files the trigger watches, is not judged against the triggers.
func TestACatchAllTriggerNeverReviewsItsOwnReview(t *testing.T) {
	t.Parallel()
	every := gateTrigger("every-pr", model.TriggerHumanOnFindings, "**")
	every.Fixes = true
	ctx, store, sim, client, task := finishGated(t, []model.PathTrigger{every}, "pkg/widget/widget.go")
	ids := distinctGatingIDs(reloaded(t, ctx, store, task.ID))
	if len(ids) != 1 {
		t.Fatalf("a catch-all trigger filed %v reviews of one head, want one", ids)
	}
	review := reloaded(t, ctx, store, ids[0])

	pushFileCommit(t, sim.BareRepo, model.BranchName(review.ID), model.BranchName(task.ID),
		"widget.go", "fixed\n", "fix the widget")
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, review, result, review.ID+"-1", baseTime.Add(10*time.Minute)); err != nil {
		t.Fatalf("ProcessResult for the review: %v", err)
	}
	got := reloaded(t, ctx, store, review.ID)
	if linkOf(got, model.LinkFixes) == "" {
		t.Fatalf("the fixing review opened no pull request, so this test proves nothing: links %+v", got.Links)
	}
	if linkOf(got, model.LinkPathTriggersRead) != "" || len(gatingIDs(got)) != 0 {
		t.Errorf("the review's own pull request was judged against the triggers: links %+v", got.Links)
	}
	all, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, other := range all {
		if other.ID != review.ID && other.Parent() == review.ID {
			t.Errorf("task %s (%q) was filed against the review %s", other.ID, other.Title, review.ID)
		}
	}
}

// A push addressing findings from an earlier gating review (sensitive or not)
// files an incremental re-review, keeps the review gate pending until that
// re-review finishes, and restores auto-merge once it passes clean.
func TestAPushAfterFindingsFilesAnIncrementalReReviewAndClearsTheGateWhenClean(t *testing.T) {
	t.Parallel()
	for _, sensitive := range []bool{false, true} {
		tr := gateTrigger("secrets", model.TriggerHumanOnFindings, "config/secrets")
		tr.Sensitive = sensitive
		ctx, store, sim, client, task := finishGated(t, []model.PathTrigger{tr}, "config/secrets/prod.yml")

		firstID := distinctGatingIDs(reloaded(t, ctx, store, task.ID))[0]
		first := reloaded(t, ctx, store, firstID)
		res := toolResult(reviewFinding("plaintext token", "config/secrets/prod.yml", 1))
		if err := orchestrator.ProcessResult(ctx, store, client, first, res, firstID+"-1", baseTime.Add(10*time.Minute)); err != nil {
			t.Fatalf("ProcessResult(sensitive=%v): %v", sensitive, err)
		}
		completeReview(t, ctx, store, firstID)
		syncTriggerHolds(t, ctx, store, client, baseTime.Add(20*time.Minute))

		afterFindings := reloaded(t, ctx, store, task.ID)
		if afterFindings.AutoMerge || !model.HasPendingReviewHold(afterFindings) {
			t.Fatalf("sensitive=%v: findings did not hold the review gate: automerge=%v links=%+v",
				sensitive, afterFindings.AutoMerge, afterFindings.Links)
		}

		pushFile(t, sim.BareRepo, model.BranchName(task.ID), "config/secrets/prod.yml", "rotated\n")
		judgeHead(t, ctx, store, client, sim.BareRepo, task, baseTime.Add(30*time.Minute))

		reheld := reloaded(t, ctx, store, task.ID)
		ids := distinctGatingIDs(reheld)
		if len(ids) != 1 || ids[0] == firstID {
			t.Fatalf("sensitive=%v: want a new re-review after findings, got %v (first %s)", sensitive, ids, firstID)
		}
		if reheld.AutoMerge || !model.HasPendingReviewHold(reheld) {
			t.Fatalf("sensitive=%v: review gate cleared before the re-review finished: automerge=%v links=%+v",
				sensitive, reheld.AutoMerge, reheld.Links)
		}
		second := reloaded(t, ctx, store, ids[0])
		if !strings.Contains(second.Body, "This is an incremental review.") ||
			!strings.Contains(second.Body, "record it again with add_review_comment") {
			t.Errorf("sensitive=%v: re-review lacks incremental findings guidance:\n%s", sensitive, second.Body)
		}

		completeReview(t, ctx, store, second.ID)
		syncTriggerHolds(t, ctx, store, client, baseTime.Add(time.Hour))
		cleared := reloaded(t, ctx, store, task.ID)
		if !cleared.AutoMerge || model.HasPendingReviewHold(cleared) || model.HasReviewFindings(cleared) {
			t.Fatalf("sensitive=%v: clean re-review did not clear findings and restore auto-merge: automerge=%v links=%+v",
				sensitive, cleared.AutoMerge, cleared.Links)
		}
	}
}

// A push after a human: required trigger's first review settled still files a
// re-review for the new head rather than letting the old review cover unseen
// commits.
func TestAHumanRequiredTriggerReReviewsAPushAfterItsFirstReviewSettled(t *testing.T) {
	t.Parallel()
	ctx, store, sim, client, task := finishGated(t,
		[]model.PathTrigger{gateTrigger("secrets", model.TriggerHumanRequired, "config/secrets")},
		"config/secrets/prod.yml")

	firstID := distinctGatingIDs(reloaded(t, ctx, store, task.ID))[0]
	completeReview(t, ctx, store, firstID)
	syncTriggerHolds(t, ctx, store, client, baseTime.Add(20*time.Minute))

	pushFile(t, sim.BareRepo, model.BranchName(task.ID), "config/secrets/prod.yml", "updated\n")
	judgeHead(t, ctx, store, client, sim.BareRepo, task, baseTime.Add(30*time.Minute))

	got := reloaded(t, ctx, store, task.ID)
	ids := distinctGatingIDs(got)
	if len(ids) != 1 || ids[0] == firstID || !model.HasPendingReviewHold(got) {
		t.Fatalf("human: required trigger did not re-review the new head: ids=%v links=%+v", ids, got.Links)
	}
}

// Reverting one of the change's own files back to the base branch's state
// still appears in the incremental review's delta rather than reading as a
// base-only move.
func TestIncrementalReviewIncludesAFileRevertedToTheBase(t *testing.T) {
	t.Parallel()
	ctx, store, sim, client, task := finishGated(t,
		[]model.PathTrigger{gateTrigger("secrets", model.TriggerHumanOnFindings, "config/secrets")},
		"config/secrets/prod.yml", "pkg/auth/guard.go")

	firstID := distinctGatingIDs(reloaded(t, ctx, store, task.ID))[0]
	completeReview(t, ctx, store, firstID)
	syncTriggerHolds(t, ctx, store, client, baseTime.Add(20*time.Minute))

	removeFile(t, sim.BareRepo, model.BranchName(task.ID), "pkg/auth/guard.go")
	judgeHead(t, ctx, store, client, sim.BareRepo, task, baseTime.Add(30*time.Minute))

	secondID := distinctGatingIDs(reloaded(t, ctx, store, task.ID))[0]
	second := reloaded(t, ctx, store, secondID)
	if !strings.Contains(second.Body, "`pkg/auth/guard.go`") ||
		strings.Contains(second.Body, "none of the change's own files moved") {
		t.Fatalf("reverted file missing from incremental delta:\n%s", second.Body)
	}
}

// When a running review closes without finishing, a queued incremental review
// waiting on it is converted back to a full review before it runs.
func TestAnAbandonedRunningReviewForcesTheQueuedDependentToReviewInFull(t *testing.T) {
	t.Parallel()
	ctx, store, sim, client, task := finishGated(t,
		[]model.PathTrigger{gateTrigger("secrets", model.TriggerHumanOnFindings, "config/secrets")},
		"config/secrets/prod.yml")

	firstID := distinctGatingIDs(reloaded(t, ctx, store, task.ID))[0]
	if err := store.StartRun(ctx, model.Run{
		ID: firstID + "-1", TaskID: firstID, Sandbox: "sb-1", Attempt: 1, StartedAt: baseTime.Add(time.Minute),
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	pushFile(t, sim.BareRepo, model.BranchName(task.ID), "README.md", "docs\n")
	judgeHead(t, ctx, store, client, sim.BareRepo, task, baseTime.Add(2*time.Minute))

	secondID := distinctGatingIDs(reloaded(t, ctx, store, task.ID))[0]
	if secondID == firstID {
		t.Fatalf("expected a second review queued behind running %s", firstID)
	}
	if !strings.Contains(reloaded(t, ctx, store, secondID).Body, "This is an incremental review.") {
		t.Fatalf("expected %s to start as incremental while %s is running", secondID, firstID)
	}

	closedAt := baseTime.Add(5 * time.Minute)
	if err := store.FinishRun(ctx, firstID+"-1", closedAt, "cancelled", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{TaskID: firstID, ClosedAt: &closedAt}); err != nil {
		t.Fatal(err)
	}

	syncTriggerHolds(t, ctx, store, client, baseTime.Add(6*time.Minute))
	second := reloaded(t, ctx, store, secondID)
	if strings.Contains(second.Body, "This is an incremental review.") {
		t.Fatalf("abandoned base review left dependent %s incremental:\n%s", secondID, second.Body)
	}
	if reloaded(t, ctx, store, task.ID).AutoMerge {
		t.Fatal("released before the full replacement review finished")
	}
}

func TestUnapprovedTriggerReviewLandsUnapproved(t *testing.T) {
	t.Parallel()
	tr := gateTrigger("secrets", model.TriggerHumanOnFindings, "config/secrets")
	tr.AutoApprove = false
	ctx, store, _, _, task := finishGated(t, []model.PathTrigger{tr}, "config/secrets/prod.yml")
	reviewID := distinctGatingIDs(reloaded(t, ctx, store, task.ID))[0]
	review := reloaded(t, ctx, store, reviewID)
	st, err := store.State(ctx, reviewID)
	if err != nil {
		t.Fatal(err)
	}
	if review.Approval != nil || st != model.StateProposed {
		t.Fatalf("auto-approve: false review state = %s, approval = %+v; want proposed and unapproved", st, review.Approval)
	}
}

func TestPathTriggerReviewCarriesTestingGuidanceAndRepoScopedCIRead(t *testing.T) {
	t.Parallel()
	for _, fixes := range []bool{false, true} {
		tr := gateTrigger("secrets", model.TriggerHumanOnFindings, "config/secrets")
		tr.Fixes = fixes
		ctx, store, _, _, task := finishGated(t, []model.PathTrigger{tr}, "config/secrets/prod.yml")
		reviewID := distinctGatingIDs(reloaded(t, ctx, store, task.ID))[0]
		review := reloaded(t, ctx, store, reviewID)
		wantGrant := model.APIGrantString("ci-read", "acme/widgets")
		if len(review.Grants) != 1 || review.Grants[0].Capability != wantGrant || review.Grants[0].Via != model.GrantByGrain {
			t.Errorf("fixes=%v: review grants = %+v, want [{Capability:%s Via:grain}]", fixes, review.Grants, wantGrant)
		}
		for _, want := range []string{
			"Testing and CI",
			"Do not run `go test ./...`",
			"grain ci wait",
			"narrowly targeted test (`go test ./pkg/x -run TestName`)",
		} {
			if !strings.Contains(review.Body, want) {
				t.Errorf("fixes=%v: review body missing %q:\n%s", fixes, want, review.Body)
			}
		}
	}
}
