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
