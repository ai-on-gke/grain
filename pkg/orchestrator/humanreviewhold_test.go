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

// The security hold's endings that hand a change to a person go through
// the human-review hold (humanreviewhold.go), and nothing automatic --
// a clean re-review, a later push -- releases a hold a reviewer raised.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

func humanReviewSources(task model.Task) map[model.HumanReviewSource]model.HumanReviewReason {
	out := map[model.HumanReviewSource]model.HumanReviewReason{}
	for _, r := range model.HumanReviewReasons(task) {
		out[r.Source] = r
	}
	return out
}

// A security review leaving findings ends the security hold the way it
// always has, and the change now carries the human-review hold: the
// findings' summary as its reason, attributed to the review. A clean
// review afterwards releases nothing.
func TestSecurityFindingsRaiseTheHumanReviewHold(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	client, task, reviewID := heldTask(t, ctx, store)

	review := reloaded(t, ctx, store, reviewID)
	result := toolResult(reviewFinding("this route skips auth", "pkg/ui/server.go", 1))
	if err := orchestrator.ProcessResult(ctx, store, client, review, result, reviewID+"-1", baseTime.Add(20*time.Minute)); err != nil {
		t.Fatalf("ProcessResult for the review: %v", err)
	}
	completeReview(t, ctx, store, reviewID)
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got := reloaded(t, ctx, store, task.ID)
	r, ok := humanReviewSources(got)[model.HumanReviewSecurityFindings]
	if !ok {
		t.Fatalf("findings left no human-review hold: links %+v", got.Links)
	}
	if r.ReviewTask != reviewID || r.Category != model.HumanReviewSecurity || !strings.Contains(r.Reason, "this route skips auth") {
		t.Fatalf("reason = %+v, want review %s's findings", r, reviewID)
	}
	if got.AutoMerge {
		t.Fatal("findings left auto-merge on")
	}
	// The backstop reconcile adds nothing more.
	if err := orchestrator.SyncHumanReviewHolds(ctx, store, baseTime.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := len(model.HumanReviewReasons(reloaded(t, ctx, store, task.ID))); n != 1 {
		t.Fatalf("%d reasons after the reconcile, want 1", n)
	}
}

// A change touching a path only a human may release carries the
// human-review hold beside its human-only marker, naming the path's
// category, and a clean security review does not release it.
func TestHumanOnlyPathRaisesTheHumanReviewHold(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	task := finishTouching(t, ctx, store, sim, "t1", ".grain/quarantine.yml", true)
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	got := reloaded(t, ctx, store, task.ID)
	r, ok := humanReviewSources(got)[model.HumanReviewHumanOnly]
	if !ok || linkOf(got, model.LinkHumanOnlyHold) == "" {
		t.Fatalf("a human-only path left no human-review hold: links %+v", got.Links)
	}
	if !strings.Contains(r.Reason, "requires a human") {
		t.Errorf("reason = %q, want it to say the path requires a human", r.Reason)
	}
	completeReview(t, ctx, store, linkOf(got, model.LinkSecurityReviewTask))
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got = reloaded(t, ctx, store, task.ID)
	if got.AutoMerge || !model.HumanReviewHeld(got) {
		t.Fatalf("a clean review released a human-only change: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
}

// A hold a reviewer raised outlives the security review finishing clean,
// and a later run's push re-classified clean.
func TestRequestedHumanReviewSurvivesCleanReviewAndPush(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	client, task, reviewID := heldTask(t, ctx, store)
	if _, err := store.RaiseHumanReviewHold(ctx, task.ID, model.HumanReviewRequest{
		Reason: model.HumanReviewReason{Source: model.HumanReviewRequested, Category: model.HumanReviewProductDecision,
			Reason: "the new default changes billing", ReviewTask: reviewID, At: baseTime.Add(10 * time.Minute)},
		By: model.Principal{Kind: model.PrincipalAgent, ID: reviewID},
	}); err != nil {
		t.Fatal(err)
	}
	completeReview(t, ctx, store, reviewID)
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got := reloaded(t, ctx, store, task.ID)
	if linkOf(got, model.LinkSecurityHold) != "" {
		t.Fatalf("the clean review did not settle the security hold: links %+v", got.Links)
	}
	if got.AutoMerge || !model.HumanReviewHeld(got) {
		t.Fatalf("a clean review released a human-review hold: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}

	// A later run pushes again and finishes.
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed again"})
	if err := orchestrator.ProcessResult(ctx, store, client, got, result, "t1-2", baseTime.Add(2*time.Hour)); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got = reloaded(t, ctx, store, task.ID)
	if got.AutoMerge || !model.HumanReviewHeld(got) {
		t.Fatalf("a later push released a human-review hold: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
}

// Changes held the old way before the human-review hold existed -- a
// human-only marker, a security hold a review's findings ended -- are
// carried onto it by the reconcile, once.
func TestSyncHumanReviewHoldsMigratesExistingHolds(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	humanOnly := filedTask(t, ctx, store, "m1", repo)
	findings := filedTask(t, ctx, store, "m2", repo)
	for id, links := range map[string][]model.Link{
		humanOnly.ID: {{Kind: model.LinkFixes, Target: "acme/widgets#7"},
			{Kind: model.LinkHumanOnlyHold, Target: "trust-boundary/v1"}},
		findings.ID: {{Kind: model.LinkFixes, Target: "acme/widgets#8"},
			{Kind: model.LinkSecurityReviewTask, Target: "r9"},
			{Kind: model.LinkAutoMergeAwaitsPerson, Target: "review r9 left findings"}},
	} {
		if err := store.UpdateTask(ctx, id, func(t *model.Task) error {
			t.AutoMerge = false
			t.Links = append(t.Links, links...)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		completeReview(t, ctx, store, id) // a finished run with its pull request open
	}
	for i := 0; i < 2; i++ {
		if err := orchestrator.SyncHumanReviewHolds(ctx, store, baseTime.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if rs := model.HumanReviewReasons(reloaded(t, ctx, store, humanOnly.ID)); len(rs) != 1 || rs[0].Source != model.HumanReviewHumanOnly {
		t.Errorf("human-only task reasons = %+v, want one human-only", rs)
	}
	rs := model.HumanReviewReasons(reloaded(t, ctx, store, findings.ID))
	if len(rs) != 1 || rs[0].Source != model.HumanReviewSecurityFindings || rs[0].ReviewTask != "r9" {
		t.Errorf("findings task reasons = %+v, want one from review r9", rs)
	}
}

// A person who accepted a review's security findings is not overruled by
// the reconcile: while the same review's code-review findings still hold
// the change, its "left findings" mark stays, and that mark must not
// bring back the security-findings hold the acceptance cleared.
func TestSyncHumanReviewHoldsLeavesAcceptedSecurityFindings(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "a1", repo)
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.AutoMerge = false
		t.Links = append(t.Links, model.Link{Kind: model.LinkFixes, Target: "acme/widgets#9"},
			model.Link{Kind: model.LinkSecurityReviewTask, Target: "r9"},
			model.Link{Kind: model.LinkAutoMergeAwaitsPerson, Target: "review r9 left findings"})
		model.AddReviewFindings(t, model.ReviewFindingsRecord{Gate: model.GateSecurityReview, ReviewTask: "r9",
			Summary: "security review left findings", At: baseTime})
		model.AddReviewFindings(t, model.ReviewFindingsRecord{Gate: "trigger:every-pr", ReviewTask: "r9",
			Summary: "every-pr review left findings", At: baseTime})
		if !model.AcceptReviewFindings(t, model.ReviewFindingsAcceptance{Gate: model.GateSecurityReview,
			ReviewTask: "r9", Reason: "out of scope", By: "alice", At: baseTime}) {
			return fmt.Errorf("nothing accepted")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	completeReview(t, ctx, store, task.ID)
	if err := orchestrator.SyncHumanReviewHolds(ctx, store, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got := reloaded(t, ctx, store, task.ID)
	if rs := model.HumanReviewReasons(got); len(rs) != 0 {
		t.Errorf("the reconcile brought back an accepted security-findings hold: %+v", rs)
	}
	for _, g := range model.ComputeMergeGates(got, nil, nil) {
		if g.ID == model.GateSecurityReview && g.State == model.GateStateFindings {
			t.Errorf("security-review gate = findings after its findings were accepted: %+v", g)
		}
	}

	// The same review, re-run, leaves findings again: the backstop raises.
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		model.AddReviewFindings(t, model.ReviewFindingsRecord{Gate: model.GateSecurityReview, ReviewTask: "r9",
			Summary: "still unsafe", At: baseTime.Add(2 * time.Hour)})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.SyncHumanReviewHolds(ctx, store, baseTime.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if rs := model.HumanReviewReasons(reloaded(t, ctx, store, task.ID)); len(rs) != 1 || rs[0].ReviewTask != "r9" {
		t.Errorf("reasons after the re-run's findings = %+v, want one from review r9", rs)
	}
}
