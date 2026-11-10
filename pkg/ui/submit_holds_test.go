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

package ui_test

// Submit is a human's answer to every hold a change waits under (task
// 441): it drops them there and then rather than leaving them for a later
// cycle, puts a change the merge queue had given up on back on it, and
// marks the decision for the orchestrator to pin to the head it reads next
// (model.LinkHumanSubmitted, orchestrator's securityhold_submit.go).

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func heldAndBlocked(t *testing.T, ctx context.Context, store *model.Store, id string) {
	t.Helper()
	if err := store.UpdateTask(ctx, id, func(task *model.Task) error {
		task.AutoMerge = false
		task.Links = append(task.Links,
			model.Link{Kind: model.LinkSecurityHold, Target: "trust-boundary/v5"},
			model.Link{Kind: model.LinkHumanOnlyHold, Target: "trust-boundary/v5"},
			model.Link{Kind: model.LinkSecurityReviewTask, Target: "551"},
			model.Link{Kind: model.LinkTrustBoundaryCleared, Target: "trust-boundary/v5@stale"},
			model.Link{Kind: model.LinkBrokenRulesHold, Target: "stale"},
			model.Link{Kind: model.LinkReviewQuestions, Target: "551:3"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	blockedAt := time.Date(2026, 10, 20, 6, 0, 0, 0, time.UTC)
	if err := store.ObserveField(ctx, id, blockedAt, func(o *model.Observation) {
		o.MergeQueueBlockedAt = &blockedAt
		o.MergeQueueBlockedBase, o.MergeQueueRearms = "0560ddf5", 2
		o.MergeQueueState, o.MergeQueueNote = "blocked", "merge failed"
	}); err != nil {
		t.Fatal(err)
	}
}

func requireSubmitted(t *testing.T, ctx context.Context, store *model.Store, id string) {
	t.Helper()
	got, err := store.GetTask(ctx, id)
	if err != nil || got == nil {
		t.Fatalf("reading %s: %v", id, err)
	}
	// Because LinkSecurityHold and LinkReviewQuestions remain active review
	// gates, Submit records approval (LinkHumanSubmitted) and drops human-only
	// holds while keeping AutoMerge off until the review gates pass.
	if got.AutoMerge {
		t.Fatal("auto-merge turned on despite pending security review and review questions")
	}
	links := map[model.LinkKind]string{}
	for _, l := range got.Links {
		links[l.Kind] = l.Target
	}
	for _, kind := range []model.LinkKind{model.LinkHumanOnlyHold, model.LinkBrokenRulesHold} {
		if target, ok := links[kind]; ok {
			t.Errorf("%s = %q survived Submit", kind, target)
		}
	}
	for _, kind := range []model.LinkKind{model.LinkSecurityHold, model.LinkReviewQuestions} {
		if links[kind] == "" {
			t.Errorf("review gate %s was cleared by Submit: links=%+v", kind, got.Links)
		}
	}
	if links[model.LinkHumanSubmitted] == "" {
		t.Errorf("Submit left no %s marker for the orchestrator to pin: links=%+v", model.LinkHumanSubmitted, got.Links)
	}
	if links[model.LinkSecurityReviewTask] != "551" {
		t.Errorf("the security review link went with the hold: links=%+v", got.Links)
	}
	obs, err := store.GetObservation(ctx, id)
	if err != nil || obs == nil {
		t.Fatalf("reading %s's observation: %v", id, err)
	}
	if obs.MergeQueueBlockedAt != nil || obs.MergeQueueBlockedBase != "" || obs.MergeQueueRearms != 0 ||
		obs.MergeQueueState != "" || obs.MergeQueueNote != "" {
		t.Errorf("the merge queue block survived Submit: %+v", obs)
	}
}

func TestSubmitClearsHoldsAndTheMergeQueueBlock(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	heldAndBlocked(t, ctx, store, task.ID)

	if err := c.Submit(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	requireSubmitted(t, ctx, store, task.ID)
}

// Turning auto-merge on by editing the task is Submit by another name.
func TestTurningAutoMergeOnClearsHoldsAndTheMergeQueueBlock(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	heldAndBlocked(t, ctx, store, task.ID)

	on := true
	if _, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{AutoMerge: &on}); err != nil {
		t.Fatal(err)
	}
	requireSubmitted(t, ctx, store, task.ID)
}

// A Submit over a change already merging itself -- pressed to retry its
// CI -- is no decision about a head the human has looked at, so it marks
// nothing for the orchestrator to pin: a head that moved since its
// clearance is classified again rather than cleared by the press.
func TestSubmittingAnAutoMergingChangePinsNothing(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	if err := store.UpdateTask(ctx, task.ID, func(task *model.Task) error {
		task.AutoMerge = true
		task.Links = append(task.Links,
			model.Link{Kind: model.LinkTrustBoundaryCleared, Target: "trust-boundary/v5@older"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := c.Submit(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetTask(ctx, task.ID)
	if err != nil || got == nil {
		t.Fatalf("reading %s: %v", task.ID, err)
	}
	cleared := ""
	for _, l := range got.Links {
		if l.Kind == model.LinkHumanSubmitted {
			t.Fatalf("a Submit over an auto-merging change was marked to pin: links=%+v", got.Links)
		}
		if l.Kind == model.LinkTrustBoundaryCleared {
			cleared = l.Target
		}
	}
	if cleared != "trust-boundary/v5@older" {
		t.Fatalf("the recorded clearance changed to %q; it is the orchestrator's to judge", cleared)
	}
}

// An edit that leaves auto-merge where it was says nothing about holds.
func TestAnEditLeavingAutoMergeOffKeepsTheHolds(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	heldAndBlocked(t, ctx, store, task.ID)

	off := false
	if _, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{AutoMerge: &off}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	held := false
	for _, l := range got.Links {
		if l.Kind == model.LinkHumanSubmitted {
			t.Fatalf("an edit leaving auto-merge off was read as a Submit: links=%+v", got.Links)
		}
		held = held || l.Kind == model.LinkSecurityHold
	}
	if !held {
		t.Fatalf("the security hold went with an edit that did not submit: links=%+v", got.Links)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil || obs == nil || obs.MergeQueueBlockedAt == nil {
		t.Fatalf("the merge queue block went with an edit that did not submit: %+v, %v", obs, err)
	}
}

// Submit records the head the human was shown -- here the commit grain CI
// last judged, this client having no GitHub to ask -- for the
// orchestrator to pin the decision to that head and no later one (task
// 979: a push between the Submit and the next cycle took the clearance).
func TestSubmitRecordsTheHeadTheHumanWasShown(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	heldAndBlocked(t, ctx, store, task.ID)
	if err := store.UpdateTask(ctx, task.ID, func(task *model.Task) error {
		task.Links = append(task.Links, model.Link{Kind: model.LinkFixes, Target: "acme/widgets#7"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(ctx, task.ID, time.Now(), func(o *model.Observation) {
		o.CI = &model.CIStatus{RunID: "run-full", Commit: "abcdef0123"}
	}); err != nil {
		t.Fatal(err)
	}

	if err := c.Submit(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetTask(ctx, task.ID)
	if err != nil || got == nil {
		t.Fatalf("reading %s: %v", task.ID, err)
	}
	for _, l := range got.Links {
		if l.Kind == model.LinkHumanSubmitted {
			if head := model.HumanSubmittedHead(l.Target); head != "abcdef0123" {
				t.Fatalf("Submit recorded head %q (%q), want abcdef0123", head, l.Target)
			}
			return
		}
	}
	t.Fatalf("Submit left no %s marker: links=%+v", model.LinkHumanSubmitted, got.Links)
}

func TestForceMergeRequiresExplicitOverrideGatesForReviews(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
		tk.AutoMerge = false
		tk.Links = append(tk.Links,
			model.Link{Kind: model.LinkFixes, Target: "acme/widgets#88"},
			model.Link{Kind: model.LinkSecurityHold, Target: "trust-boundary/v5"},
			model.Link{Kind: model.LinkSecurityReviewTask, Target: "551"},
		)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	done := time.Date(2026, 11, 10, 1, 0, 0, 0, time.UTC)
	if err := store.Observe(ctx, model.Observation{
		TaskID: task.ID, CompletedAt: &done,
		CI: &model.CIStatus{RunID: "run-1", Commit: "0123456789abcdef0123456789abcdef01234567", Conclusion: "success", FinishedAt: &done},
	}); err != nil {
		t.Fatal(err)
	}

	// Plain ForceMerge without naming security-review is refused!
	if err := c.ForceMerge(ctx, task.ID, "hotfix", ""); err == nil {
		t.Fatal("ForceMerge without explicit overrideGates must refuse an unsatisfied security-review gate")
	}

	// Naming security-review explicitly with a reason succeeds and records the override.
	if err := c.ForceMergeWithGates(ctx, task.ID, "hotfix", "", []string{"security-review"}); err != nil {
		t.Fatalf("ForceMergeWithGates: %v", err)
	}
	got, err := store.GetTask(ctx, task.ID)
	if err != nil || got == nil {
		t.Fatalf("GetTask: %v", err)
	}
	ov, ok := model.LatestGateOverride(*got)
	if !ok || ov.Reason != "hotfix" || len(ov.Gates) != 1 || ov.Gates[0] != "security-review" {
		t.Fatalf("LatestGateOverride = %+v (%v), want security-review override", ov, ok)
	}
}

func TestSubmitPullRequestSeparatesApprovalAndGateOverride(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
		tk.AutoMerge = false
		tk.Links = append(tk.Links,
			model.Link{Kind: model.LinkFixes, Target: "acme/widgets#99"},
			model.Link{Kind: model.LinkSecurityHold, Target: "trust-boundary/v5"},
			model.Link{Kind: model.LinkSecurityReviewTask, Target: "601"},
		)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	done := time.Date(2026, 11, 10, 1, 0, 0, 0, time.UTC)
	if err := store.Observe(ctx, model.Observation{
		TaskID: task.ID, CompletedAt: &done,
		CI: &model.CIStatus{RunID: "run-1", Commit: "0123456789abcdef0123456789abcdef01234567", Conclusion: "success", FinishedAt: &done},
	}); err != nil {
		t.Fatal(err)
	}

	// Normal SubmitPullRequest records approval without clearing security-review.
	res, err := c.SubmitPullRequest(ctx, ui.SubmitPullRequestRequest{PR: "acme/widgets#99"})
	if err != nil {
		t.Fatalf("SubmitPullRequest: %v", err)
	}
	if !res.Approved || len(res.Overridden) != 0 {
		t.Fatalf("res = %+v, want Approved=true and Overridden empty", res)
	}
	got, _ := store.GetTask(ctx, task.ID)
	if got.AutoMerge {
		t.Fatal("SubmitPullRequest turned AutoMerge on while security-review was still running")
	}

	// Override without reason is refused.
	if _, err := c.SubmitPullRequest(ctx, ui.SubmitPullRequestRequest{
		PR: "acme/widgets#99", OverrideGates: []string{"security-review"},
	}); err == nil {
		t.Fatal("SubmitPullRequest override without reason must fail")
	}

	// Override with explicit gate and reason succeeds.
	ovRes, err := c.SubmitPullRequest(ctx, ui.SubmitPullRequestRequest{
		PR: "acme/widgets#99", OverrideGates: []string{"security-review"}, Reason: "emergency rollback",
	})
	if err != nil {
		t.Fatalf("SubmitPullRequest override: %v", err)
	}
	if len(ovRes.Overridden) != 1 || ovRes.Overridden[0] != "security-review" {
		t.Fatalf("ovRes = %+v, want security-review overridden", ovRes)
	}
}
