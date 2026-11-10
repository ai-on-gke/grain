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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// Requirement 6.1: Submit before review — no merge until the review passes,
// then an automatic merge (both for normal trust-boundary paths and for
// human-required trust-boundary paths).
func TestSubmitBeforeSecurityReviewDoesNotMergeUntilReviewPassesThenMergesAutomatically(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"pkg/gitproxy/proxy.go", ".grain/quarantine.yml"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			store, ctx := openStore(t)
			sim, client, task, branch := heldAt(t, ctx, store, path)
			setMergeable(sim, true)
			sim.CheckRuns[branch] = greenCheck

			got := reloaded(t, ctx, store, task.ID)
			reviewID := linkOf(got, model.LinkSecurityReviewTask)
			if reviewID == "" {
				t.Fatal("expected a security review task")
			}
			// Automation comment must never tell the human that Submit skips the review.
			for _, body := range commentBodies(t, ctx, store, task.ID) {
				if strings.Contains(body, "merge it without waiting") {
					t.Errorf("automation comment still claims Submit skips the review:\n%s", body)
				}
			}

			// Person presses Submit while the security review is still queued/running.
			head := prHeadOf(t, client, got)
			humanSubmit(t, ctx, store, task.ID, head)

			if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(5*time.Minute)); err != nil {
				t.Fatalf("SyncPullRequests before review finishes: %v", err)
			}
			if mergedBranch(sim, branch) {
				t.Fatalf("PR merged after Submit while security review %s had not passed", reviewID)
			}
			if st, err := store.State(ctx, reviewID); err != nil || st == model.StateClosed {
				t.Fatalf("security review %s state = %q (%v), must not be closed unfinished", reviewID, st, err)
			}

			// Now the security review finishes cleanly: the PR merges automatically
			// without asking the person to press Submit a second time.
			completeReview(t, ctx, store, reviewID)
			if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(35*time.Minute)); err != nil {
				t.Fatalf("SyncSecurityHolds: %v", err)
			}
			if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(40*time.Minute)); err != nil {
				t.Fatalf("SyncPullRequests after clean review: %v", err)
			}
			if !mergedBranch(sim, branch) {
				t.Fatalf("PR did not merge automatically once security review %s passed: links=%+v",
					reviewID, reloaded(t, ctx, store, task.ID).Links)
			}
		})
	}
}

// Requirement 6.2: Submit plus a review with findings — no merge until a
// re-review passes after fixes.
func TestSubmitPlusSecurityReviewWithFindingsDoesNotMergeUntilCleanRereview(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, branch := heldAt(t, ctx, store, "pkg/gitproxy/proxy.go")
	setMergeable(sim, true)
	sim.CheckRuns[branch] = greenCheck

	got := reloaded(t, ctx, store, task.ID)
	reviewID := linkOf(got, model.LinkSecurityReviewTask)
	review, err := store.GetTask(ctx, reviewID)
	if err != nil || review == nil {
		t.Fatalf("reading review %s: %v", reviewID, err)
	}

	// Person presses Submit before the review finishes.
	humanSubmit(t, ctx, store, task.ID, prHeadOf(t, client, got))

	// Security review finishes WITH findings.
	findingRes := toolResult(
		reviewFinding("Token header forwarded to untrusted upstream.", "pkg/gitproxy/proxy.go", 1),
	)
	if err := orchestrator.ProcessResult(ctx, store, client, *review, findingRes, review.ID+"-1", baseTime.Add(10*time.Minute)); err != nil {
		t.Fatalf("ProcessResult (security review with findings): %v", err)
	}
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(11*time.Minute)); err != nil {
		t.Fatalf("SyncSecurityHolds: %v", err)
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(12*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if mergedBranch(sim, branch) {
		t.Fatal("PR merged despite security review leaving findings")
	}

	// Pressing Submit again AFTER findings still does NOT merge the PR.
	humanSubmit(t, ctx, store, task.ID, prHeadOf(t, client, reloaded(t, ctx, store, task.ID)))
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(15*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests after second Submit: %v", err)
	}
	if mergedBranch(sim, branch) {
		t.Fatal("second Submit merged PR over unresolved security review findings")
	}

	// Fixing the code and passing a fresh security re-review clears the gate and merges.
	pushFile(t, sim.BareRepo, branch, "pkg/gitproxy/proxy.go", "fixed upstream check\n")
	fixRes := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed fix"})
	if err := orchestrator.ProcessResult(ctx, store, client, reloaded(t, ctx, store, task.ID), fixRes, task.ID+"-2", baseTime.Add(20*time.Minute)); err != nil {
		t.Fatalf("ProcessResult (fix run): %v", err)
	}
	rereviewID := linkOf(reloaded(t, ctx, store, task.ID), model.LinkSecurityReviewTask)
	if rereviewID == "" || rereviewID == reviewID {
		t.Fatalf("expected a new security re-review task, got %q (first was %q)", rereviewID, reviewID)
	}
	done := baseTime.Add(25 * time.Minute)
	if err := store.Observe(ctx, model.Observation{TaskID: rereviewID, CompletedAt: &done}); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(26*time.Minute)); err != nil {
		t.Fatalf("SyncSecurityHolds (re-review): %v", err)
	}
	sim.CheckRuns[branch] = greenCheck
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(27*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests after clean re-review: %v", err)
	}
	if !mergedBranch(sim, branch) {
		t.Fatalf("PR did not merge after clean security re-review: links=%+v", reloaded(t, ctx, store, task.ID).Links)
	}
}

// Requirement 6.3 & Requirement 5: Override with a reason — merged and audited,
// the security review continues post-merge against the base branch rather than
// closing unfinished, and any findings it reports are filed as follow-up fix
// tasks on the base branch.
func TestOverrideWithReasonMergesIsAuditedAndContinuesSecurityReviewPostMerge(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, branch := heldAt(t, ctx, store, "pkg/gitproxy/proxy.go")
	setMergeable(sim, true)
	sim.CheckRuns[branch] = greenCheck

	got := reloaded(t, ctx, store, task.ID)
	reviewID := linkOf(got, model.LinkSecurityReviewTask)
	head := prHeadOf(t, client, got)

	// Operator overrides the security-review gate with a reason.
	forceAt := baseTime.Add(5 * time.Minute)
	if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
		tk.Links = append(tk.Links, model.GateOverrideRecord{
			Gates:   []string{model.GateSecurityReview},
			Skipped: "security-review (task " + reviewID + " running)",
			Reason:  "production outage mitigation",
			By:      "alice@example.com",
			HeadSHA: head,
			At:      forceAt,
		}.Link())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(ctx, task.ID, forceAt, func(o *model.Observation) {
		o.ForceMergeAt = &forceAt
		o.ForceMergeReason = "production outage mitigation"
		o.ForceMergeBy = "alice@example.com"
		o.ForceMergeSkipped = "security-review (task " + reviewID + " running)"
		o.ForceMergeHeadSHA = head
	}); err != nil {
		t.Fatal(err)
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(6*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests (override): %v", err)
	}
	if !mergedBranch(sim, branch) {
		t.Fatal("override with reason did not merge the PR")
	}

	// Audit trail check: ForceMergeRecords has the record with reason & actor.
	records, err := store.ForceMergeRecords(ctx)
	if err != nil || len(records) != 1 || records[0].By != "alice@example.com" || records[0].Reason != "production outage mitigation" {
		t.Fatalf("ForceMergeRecords = %+v (%v), want 1 record for alice@example.com", records, err)
	}

	// SyncReviews runs next cycle: security review must NOT be closed unfinished!
	if err := orchestrator.SyncReviews(ctx, store, baseTime.Add(7*time.Minute)); err != nil {
		t.Fatalf("SyncReviews post-merge: %v", err)
	}
	st, err := store.State(ctx, reviewID)
	if err != nil || st == model.StateClosed {
		t.Fatalf("post-merge security review %s state = %q (%v), want still open/queued", reviewID, st, err)
	}
	reviewTask := reloaded(t, ctx, store, reviewID)
	if reviewTask.Base != "main" {
		t.Errorf("post-merge security review Base = %q, want main", reviewTask.Base)
	}
	for _, body := range commentBodies(t, ctx, store, reviewID) {
		if strings.Contains(body, "Closing this review unfinished") {
			t.Errorf("review was closed unfinished:\n%s", body)
		}
	}

	// Now the continuing post-merge security review finishes WITH findings:
	// grain files an approved follow-up fix task targeting main!
	findingRes := toolResult(
		reviewFinding("Missing header sanitisation on fallback path.", "pkg/gitproxy/proxy.go", 1),
	)
	if err := orchestrator.ProcessResult(ctx, store, client, reviewTask, findingRes, reviewTask.ID+"-1", baseTime.Add(10*time.Minute)); err != nil {
		t.Fatalf("ProcessResult (post-merge security review): %v", err)
	}

	allTasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var followUp *model.Task
	for i := range allTasks {
		tk := allTasks[i]
		if tk.ID != task.ID && tk.ID != reviewID && linkOf(tk, model.LinkProposedBy) == task.ID {
			followUp = &tk
			break
		}
	}
	if followUp == nil {
		t.Fatalf("expected a follow-up fix task proposed by %s after post-merge review findings, got tasks=%+v", task.ID, allTasks)
	}
	if followUp.Base != "main" || followUp.Approval == nil {
		t.Errorf("follow-up task Base=%q Approval=%+v, want Base=main and approved", followUp.Base, followUp.Approval)
	}
	if !strings.Contains(followUp.Body, "Missing header sanitisation") {
		t.Errorf("follow-up task body lacks finding details:\n%s", followUp.Body)
	}
}

// Requirement 6.4: Non-trust-boundary PR with auto-merge — unchanged behaviour.
func TestNonTrustBoundaryAutoMergePRUnchanged(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, branch := clearedOrdinaryChange(t, ctx, store)
	if reviews := securityReviewsOf(t, ctx, store, task.ID); len(reviews) != 0 {
		t.Fatalf("unexpected security review filed for ordinary change: %+v", reviews)
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(5*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if !mergedBranch(sim, branch) {
		t.Fatal("non-trust-boundary PR with auto-merge did not merge automatically on green CI")
	}
}
