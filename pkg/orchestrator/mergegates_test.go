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
	"slices"
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

// A force requested for a head that had not been classified yet could see no
// security-review gate to name. The cycle that merges it classifies that head
// first, and a trust-boundary push held there stays held unless the override
// for that commit names the security review too.
func TestForcedMergeDoesNotSkipAReviewGateItsOverrideDidNotName(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		gates []string
		merge bool
	}{
		{gates: []string{model.GateCI}, merge: false},
		{gates: []string{model.GateCI, model.GateSecurityReview}, merge: true},
	} {
		t.Run(strings.Join(tc.gates, "+"), func(t *testing.T) {
			t.Parallel()
			store, ctx := openStore(t)
			sim, client := newSim(t, "acme", "widgets", "main")
			task := finishTouching(t, ctx, store, sim, "t1", "pkg/thing/thing.go", true)
			result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
			if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
				t.Fatalf("ProcessResult: %v", err)
			}
			got := reloaded(t, ctx, store, task.ID)
			if !got.AutoMerge || linkOf(got, model.LinkTrustBoundaryCleared) == "" {
				t.Fatalf("the change was not cleared: automerge=%v links=%+v", got.AutoMerge, got.Links)
			}
			branch := model.BranchName(task.ID)
			setMergeable(sim, true)

			// A trust-boundary commit lands, and the operator forces that head
			// before any cycle has classified it.
			pushFile(t, sim.BareRepo, branch, "pkg/gitproxy/proxy.go", "unreviewed\n")
			sim.CheckRuns[branch] = greenCheck
			head := prHeadOf(t, client, got)
			forceAt := baseTime.Add(5 * time.Minute)
			if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
				tk.Links = append(tk.Links, model.GateOverrideRecord{
					Gates: tc.gates, Reason: "outage", By: "alice@example.com", HeadSHA: head, At: forceAt,
				}.Link())
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := store.ObserveField(ctx, task.ID, forceAt, func(o *model.Observation) {
				o.ForceMergeAt = &forceAt
				o.ForceMergeReason, o.ForceMergeBy, o.ForceMergeHeadSHA = "outage", "alice@example.com", head
			}); err != nil {
				t.Fatal(err)
			}

			if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(6*time.Minute)); err != nil {
				t.Fatalf("SyncPullRequests: %v", err)
			}
			if merged := mergedBranch(sim, branch); merged != tc.merge {
				t.Fatalf("override of %v: merged = %v, want %v; links=%+v",
					tc.gates, merged, tc.merge, reloaded(t, ctx, store, task.ID).Links)
			}
			if !tc.merge && linkOf(reloaded(t, ctx, store, task.ID), model.LinkSecurityReviewTask) == "" {
				t.Fatal("the unclassified trust-boundary head was not held for a security review")
			}
		})
	}
}

// A superseded security review -- one whose task's security-review-task link
// has already moved on to a newer security review -- still belongs to the
// security-review gate, never the code-review gate. Its findings stay on
// security-review, are acceptable and clearable only there, and never block or
// offer Accept findings on the code-review gate.
func TestSupersededSecurityReviewFindingsBelongToSecurityReviewGate(t *testing.T) {
	t.Parallel()
	for _, stripTags := range []bool{false, true} {
		name := "tagged"
		if stripTags {
			name = "pre-tag"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store, ctx := openStore(t)
			sim, client, task, branch := heldAt(t, ctx, store, "pkg/gitproxy/proxy.go")

			reviewAID := linkOf(reloaded(t, ctx, store, task.ID), model.LinkSecurityReviewTask)
			if reviewAID == "" {
				t.Fatal("expected initial security review A")
			}

			// Push a second trust-boundary head so security-review-task moves from A to B.
			pushFile(t, sim.BareRepo, branch, "pkg/gitproxy/proxy.go", "second revision\n")
			run2 := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed second head"})
			if err := orchestrator.ProcessResult(ctx, store, client, reloaded(t, ctx, store, task.ID), run2, "t1-2", baseTime.Add(10*time.Minute)); err != nil {
				t.Fatalf("ProcessResult (head 2): %v", err)
			}
			reviewBID := linkOf(reloaded(t, ctx, store, task.ID), model.LinkSecurityReviewTask)
			if reviewBID == "" || reviewBID == reviewAID {
				t.Fatalf("security-review-task did not move from A (%s) to B: got %q", reviewAID, reviewBID)
			}

			// Let B pass so only A's findings remain after A reports them.
			completeReview(t, ctx, store, reviewBID)
			if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(20*time.Minute)); err != nil {
				t.Fatalf("SyncSecurityHolds (B clean): %v", err)
			}

			reviewA := reloaded(t, ctx, store, reviewAID)
			if stripTags {
				// Simulate a security review filed before dedicated gate tags existed.
				reviewA.Tags = nil
			}
			findingRes := toolResult(
				reviewFinding("Token header forwarded to untrusted upstream.", "pkg/gitproxy/proxy.go", 1),
			)
			if err := orchestrator.ProcessResult(ctx, store, client, reviewA, findingRes, reviewAID+"-1", baseTime.Add(30*time.Minute)); err != nil {
				t.Fatalf("ProcessResult (superseded security review A): %v", err)
			}

			got := reloaded(t, ctx, store, task.ID)
			if cur := linkOf(got, model.LinkSecurityReviewTask); cur != reviewBID {
				t.Fatalf("security-review-task = %q, want %q", cur, reviewBID)
			}

			findings := model.ReviewFindings(got)
			if len(findings) != 1 || findings[0].Gate != model.GateSecurityReview || findings[0].ReviewTask != reviewAID {
				t.Fatalf("ReviewFindings = %+v, want [{Gate:%s ReviewTask:%s}]", findings, model.GateSecurityReview, reviewAID)
			}

			gates := map[string]model.MergeGate{}
			for _, g := range model.ComputeMergeGates(got, nil, nil) {
				gates[g.ID] = g
			}
			secGate, hasSec := gates[model.GateSecurityReview]
			if !hasSec || secGate.State != model.GateStateFindings || secGate.ReviewTask != reviewAID || !secGate.Acceptable {
				t.Errorf("security-review gate = %+v (present %v), want findings on %s with acceptable=true", secGate, hasSec, reviewAID)
			}
			if codeGate, hasCode := gates[model.GateReview]; hasCode {
				t.Errorf("code-review gate appeared for superseded security review A: %+v", codeGate)
			}

			// Accept findings refuses code-review and succeeds on security-review.
			acceptCopy := got
			acceptCopy.Links = slices.Clone(got.Links)
			if model.AcceptReviewFindings(&acceptCopy, model.ReviewFindingsAcceptance{
				Gate: model.GateReview, ReviewTask: reviewAID, Reason: "wrong gate", By: "alice", At: baseTime.Add(40 * time.Minute),
			}) {
				t.Error("AcceptReviewFindings accepted superseded security review A on the code-review gate")
			}
			if !model.AcceptReviewFindings(&acceptCopy, model.ReviewFindingsAcceptance{
				Gate: model.GateSecurityReview, ReviewTask: reviewAID, Reason: "already fixed in head 2", By: "alice", At: baseTime.Add(40 * time.Minute),
			}) {
				t.Fatal("AcceptReviewFindings did not accept superseded security review A on security-review")
			}
			if model.HasReviewFindings(acceptCopy) {
				t.Errorf("findings or questions remain after accepting on security-review: %+v", acceptCopy.Links)
			}
			for _, g := range model.ComputeMergeGates(acceptCopy, nil, nil) {
				if g.ID == model.GateReview {
					t.Errorf("unexpected code-review gate after accepting A on security-review: %+v", g)
				}
				if g.ID == model.GateSecurityReview && g.State != model.GateStatePassed {
					t.Errorf("security-review gate after accepting A = %+v, want passed", g)
				}
			}

			// ClearReviewFindingsForGate(review) leaves A's findings alone;
			// ClearReviewFindingsForGate(security-review) clears them and their questions.
			clearCopy := got
			clearCopy.Links = slices.Clone(got.Links)
			model.ClearReviewFindingsForGate(&clearCopy, model.GateReview)
			if len(model.ReviewFindingsForGate(clearCopy, model.GateSecurityReview)) != 1 {
				t.Errorf("ClearReviewFindingsForGate(review) cleared security review A's findings: %+v", clearCopy.Links)
			}
			model.ClearReviewFindingsForGate(&clearCopy, model.GateSecurityReview)
			if model.HasReviewFindings(clearCopy) {
				t.Errorf("ClearReviewFindingsForGate(security-review) left findings or questions: %+v", clearCopy.Links)
			}
		})
	}
}

// Reproduces the 2026-11-18 walkthrough finding (D1): a change held on both a
// trust-boundary security review and a gating path trigger (e.g. api/**) has
// its security review post findings while the path-trigger hold is still open,
// then moves to a newer head whose security review passes. The earlier
// security review's findings must stay on security-review (cleared by the new
// head's hold) and never leak onto the code-review gate.
func TestSecurityReviewFindingsWithActivePathTriggerHoldStayOnSecurityGate(t *testing.T) {
	t.Parallel()
	ctx, store, sim, client, task := finishGated(t,
		[]model.PathTrigger{gateTrigger("api", model.TriggerHumanOnFindings, "api/**")},
		"pkg/gitproxy/proxy.go", "api/handler.go")

	got := reloaded(t, ctx, store, task.ID)
	sec1ID := linkOf(got, model.LinkSecurityReviewTask)
	if sec1ID == "" {
		t.Fatalf("expected security review task on %+v", got.Links)
	}
	if linkOf(got, model.LinkPathTriggerHold) == "" {
		t.Fatalf("expected active path-trigger hold on %+v", got.Links)
	}

	// Security review 1 posts findings while the api path-trigger hold is still active.
	sec1 := reloaded(t, ctx, store, sec1ID)
	findingRes := toolResult(reviewFinding("reject empty owners too?", "pkg/gitproxy/proxy.go", 1))
	if err := orchestrator.ProcessResult(ctx, store, client, sec1, findingRes, sec1ID+"-1", baseTime.Add(10*time.Minute)); err != nil {
		t.Fatalf("ProcessResult (sec1 findings): %v", err)
	}

	afterSec1 := reloaded(t, ctx, store, task.ID)
	for _, rf := range model.ReviewFindings(afterSec1) {
		if rf.ReviewTask == sec1ID && rf.Gate != model.GateSecurityReview {
			t.Fatalf("security review %s findings recorded under gate %q, want %q: %+v",
				sec1ID, rf.Gate, model.GateSecurityReview, model.ReviewFindings(afterSec1))
		}
	}
	for _, g := range model.ComputeMergeGates(afterSec1, nil, nil) {
		if g.ID == model.GateReview && (g.State == model.GateStateFindings || g.ReviewTask == sec1ID || g.Acceptable) {
			t.Errorf("code-review gate polluted by security review %s: %+v", sec1ID, g)
		}
	}

	// Head 2: author pushes fixes; security review 2 is filed and passes clean.
	pushFile(t, sim.BareRepo, model.BranchName(task.ID), "pkg/gitproxy/proxy.go", "reject empty owners\n")
	run2 := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed head 2"})
	if err := orchestrator.ProcessResult(ctx, store, client, reloaded(t, ctx, store, task.ID), run2, "t1-2", baseTime.Add(20*time.Minute)); err != nil {
		t.Fatalf("ProcessResult (head 2): %v", err)
	}
	head2 := reloaded(t, ctx, store, task.ID)
	sec2ID := linkOf(head2, model.LinkSecurityReviewTask)
	if sec2ID == "" || sec2ID == sec1ID {
		t.Fatalf("expected new security review on head 2, got %q (sec1=%q)", sec2ID, sec1ID)
	}
	completeReview(t, ctx, store, sec2ID)
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(30*time.Minute)); err != nil {
		t.Fatalf("SyncSecurityHolds (sec2 clean): %v", err)
	}

	final := reloaded(t, ctx, store, task.ID)
	if rfs := model.ReviewFindings(final); len(rfs) != 0 {
		t.Fatalf("ReviewFindings after clean security re-review = %+v, want empty", rfs)
	}
	gates := map[string]model.MergeGate{}
	for _, g := range model.ComputeMergeGates(final, nil, nil) {
		gates[g.ID] = g
	}
	if g := gates[model.GateSecurityReview]; g.State != model.GateStatePassed {
		t.Errorf("security-review gate = %+v, want passed", g)
	}
	if g, ok := gates[model.GateReview]; ok && (g.State == model.GateStateFindings || g.ReviewTask == sec1ID || g.Acceptable) {
		t.Errorf("code-review gate after clean security re-review = %+v, must not name security review %s or offer findings", g, sec1ID)
	}
}

// A superseded security review finishing cleanly must not be mistaken for an
// ordinary code review and clear standing code-review findings.
func TestSupersededSecurityReviewCleanCompletionDoesNotClearCodeReviewFindings(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, branch := heldAt(t, ctx, store, "pkg/gitproxy/proxy.go")
	reviewAID := linkOf(reloaded(t, ctx, store, task.ID), model.LinkSecurityReviewTask)

	pushFile(t, sim.BareRepo, branch, "pkg/gitproxy/proxy.go", "second revision\n")
	run2 := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed second head"})
	if err := orchestrator.ProcessResult(ctx, store, client, reloaded(t, ctx, store, task.ID), run2, "t1-2", baseTime.Add(10*time.Minute)); err != nil {
		t.Fatalf("ProcessResult (head 2): %v", err)
	}
	// Standing code-review finding on task (with no LinkReviewTask).
	if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
		model.AddReviewFindings(tk, model.ReviewFindingsRecord{
			Gate:       model.GateReview,
			ReviewTask: "code-rev-1",
			Summary:    "missing error check",
			At:         baseTime.Add(15 * time.Minute),
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Superseded security review A completes cleanly and is closed by SyncReviews.
	done := baseTime.Add(20 * time.Minute)
	if err := store.Observe(ctx, model.Observation{TaskID: reviewAID, CompletedAt: &done}); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.SyncReviews(ctx, store, baseTime.Add(25*time.Minute)); err != nil {
		t.Fatalf("SyncReviews: %v", err)
	}

	got := reloaded(t, ctx, store, task.ID)
	if rfs := model.ReviewFindingsForGate(got, model.GateReview); len(rfs) != 1 || rfs[0].ReviewTask != "code-rev-1" {
		t.Fatalf("code-review findings after superseded security review finished clean = %+v, want code-rev-1 intact", rfs)
	}
}

// A superseded security review posting findings while the newer head's security
// review is still running must keep the newer head's security hold and
// clearance intact, attribute the findings to the superseded review's head on
// security-review, and keep security-review running after those findings are
// accepted until the current security review finishes clean.
func TestSupersededSecurityReviewFindingsDoNotDropCurrentSecurityHold(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, branch := heldAt(t, ctx, store, "pkg/gitproxy/proxy.go")

	head1 := prHeadOf(t, client, reloaded(t, ctx, store, task.ID))
	reviewAID := linkOf(reloaded(t, ctx, store, task.ID), model.LinkSecurityReviewTask)

	// Push head 2 so security-review-task moves to B with an active hold on head 2.
	pushFile(t, sim.BareRepo, branch, "pkg/gitproxy/proxy.go", "second revision\n")
	run2 := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed second head"})
	if err := orchestrator.ProcessResult(ctx, store, client, reloaded(t, ctx, store, task.ID), run2, "t1-2", baseTime.Add(10*time.Minute)); err != nil {
		t.Fatalf("ProcessResult (head 2): %v", err)
	}
	head2Task := reloaded(t, ctx, store, task.ID)
	head2 := prHeadOf(t, client, head2Task)
	reviewBID := linkOf(head2Task, model.LinkSecurityReviewTask)
	if reviewBID == "" || reviewBID == reviewAID {
		t.Fatalf("security-review-task did not move from A (%s) to B: got %q", reviewAID, reviewBID)
	}

	// Superseded security review A finishes with findings while B is still running.
	reviewA := reloaded(t, ctx, store, reviewAID)
	findingRes := toolResult(reviewFinding("old head issue", "pkg/gitproxy/proxy.go", 1))
	if err := orchestrator.ProcessResult(ctx, store, client, reviewA, findingRes, reviewAID+"-1", baseTime.Add(20*time.Minute)); err != nil {
		t.Fatalf("ProcessResult (superseded review A): %v", err)
	}

	got := reloaded(t, ctx, store, task.ID)
	if linkOf(got, model.LinkSecurityHold) == "" {
		t.Fatalf("superseded review A dropped B's active LinkSecurityHold: %+v", got.Links)
	}
	if cleared := linkOf(got, model.LinkTrustBoundaryCleared); !strings.HasSuffix(strings.ToLower(cleared), "@"+strings.ToLower(head2)) {
		t.Fatalf("LinkTrustBoundaryCleared = %q, want head 2 (%s) preserved", cleared, head2)
	}
	rfs := model.ReviewFindings(got)
	if len(rfs) != 1 || rfs[0].Gate != model.GateSecurityReview || rfs[0].ReviewTask != reviewAID || !strings.EqualFold(rfs[0].HeadSHA, head1) {
		t.Fatalf("ReviewFindings = %+v, want [{Gate:%s ReviewTask:%s HeadSHA:%s}]", rfs, model.GateSecurityReview, reviewAID, head1)
	}

	// Accepting A's findings on security-review must leave B's hold running.
	acceptCopy := got
	acceptCopy.Links = slices.Clone(got.Links)
	if !model.AcceptReviewFindings(&acceptCopy, model.ReviewFindingsAcceptance{
		Gate: model.GateSecurityReview, ReviewTask: reviewAID, Reason: "fixed in head 2", By: "alice", At: baseTime.Add(25 * time.Minute),
	}) {
		t.Fatal("AcceptReviewFindings refused superseded security review A on security-review")
	}
	if !model.HasPendingReviewHold(acceptCopy) {
		t.Fatalf("accepting A cleared B's pending security hold: %+v", acceptCopy.Links)
	}
	for _, g := range model.ComputeMergeGates(acceptCopy, nil, nil) {
		if g.ID == model.GateSecurityReview && (g.State != model.GateStateRunning || g.ReviewTask != reviewBID) {
			t.Errorf("security-review gate after accepting A while B runs = %+v, want running on %s", g, reviewBID)
		}
	}

	// Once B finishes clean, SyncSecurityHolds clears A's findings and restores auto-merge.
	completeReview(t, ctx, store, reviewBID)
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(30*time.Minute)); err != nil {
		t.Fatalf("SyncSecurityHolds (B clean): %v", err)
	}
	afterB := reloaded(t, ctx, store, task.ID)
	if !afterB.AutoMerge || linkOf(afterB, model.LinkSecurityHold) != "" || model.HasReviewFindings(afterB) {
		t.Fatalf("clean B did not settle hold and clear A's findings: automerge=%v links=%+v", afterB.AutoMerge, afterB.Links)
	}
}

// A non-security review (path-trigger or code review) posting findings while a
// trust-boundary security review is running must not drop the security hold or
// attribute its findings to security-review; and when the security review later
// finishes clean, it must keep the other review's findings and open questions
// intact.
func TestNonSecurityReviewFindingsDoNotDropSecurityHoldOrPolluteSecurityGate(t *testing.T) {
	t.Parallel()
	ctx, store, _, client, task := finishGated(t,
		[]model.PathTrigger{gateTrigger("api", model.TriggerHumanOnFindings, "api/**")},
		"pkg/gitproxy/proxy.go", "api/handler.go")

	got := reloaded(t, ctx, store, task.ID)
	secID := linkOf(got, model.LinkSecurityReviewTask)
	trigIDs := gatingIDs(got)
	if secID == "" || len(trigIDs) != 1 {
		t.Fatalf("expected security review and 1 gating trigger review, got sec=%q trig=%v", secID, trigIDs)
	}
	trigID := trigIDs[0]

	// Path-trigger review posts findings while security review is still running.
	trig := reloaded(t, ctx, store, trigID)
	findingRes := toolResult(reviewFinding("document error codes", "api/handler.go", 1))
	if err := orchestrator.ProcessResult(ctx, store, client, trig, findingRes, trigID+"-1", baseTime.Add(10*time.Minute)); err != nil {
		t.Fatalf("ProcessResult (trigger findings): %v", err)
	}

	afterTrig := reloaded(t, ctx, store, task.ID)
	if linkOf(afterTrig, model.LinkSecurityHold) == "" || linkOf(afterTrig, model.LinkTrustBoundaryCleared) == "" {
		t.Fatalf("trigger review findings dropped security hold or clearance: %+v", afterTrig.Links)
	}
	if rfs := model.ReviewFindingsForGate(afterTrig, model.GateSecurityReview); len(rfs) != 0 {
		t.Fatalf("trigger review polluted security-review findings: %+v", rfs)
	}
	for _, hr := range model.HumanReviewReasons(afterTrig) {
		if hr.Source == model.HumanReviewSecurityFindings {
			t.Fatalf("trigger review raised HumanReviewSecurityFindings: %+v", hr)
		}
	}
	gates := map[string]model.MergeGate{}
	for _, g := range model.ComputeMergeGates(afterTrig, nil, nil) {
		gates[g.ID] = g
	}
	if sec := gates[model.GateSecurityReview]; sec.State != model.GateStateRunning || sec.ReviewTask != secID {
		t.Errorf("security-review gate = %+v, want running on %s", sec, secID)
	}
	if code := gates[model.GateReview]; code.State != model.GateStateFindings || code.ReviewTask != trigID || !code.Acceptable {
		t.Errorf("code-review gate = %+v, want acceptable findings on %s", code, trigID)
	}

	// Security review finishes clean: security-review passes, while trigger
	// findings and open questions stay intact and auto-merge remains off.
	completeReview(t, ctx, store, secID)
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(20*time.Minute)); err != nil {
		t.Fatalf("SyncSecurityHolds: %v", err)
	}
	afterSec := reloaded(t, ctx, store, task.ID)
	if afterSec.AutoMerge {
		t.Fatal("auto-merge restored despite unresolved trigger review findings")
	}
	if q, ok := model.ReviewQuestionsOf(afterSec); !ok || q.ReviewTaskID != trigID {
		t.Errorf("settleSecurityHold clobbered trigger review questions: got %+v (ok=%v)", q, ok)
	}
	if rfs := model.ReviewFindings(afterSec); len(rfs) != 1 || rfs[0].ReviewTask != trigID {
		t.Errorf("trigger review findings after clean security review = %+v, want %s intact", rfs, trigID)
	}
}

// A clean code review finishing must not wipe open review questions belonging
// to the security-review gate.
func TestCleanCodeReviewDoesNotClearSecurityReviewQuestions(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client, task, _ := heldAt(t, ctx, store, "pkg/gitproxy/proxy.go")

	secID := linkOf(reloaded(t, ctx, store, task.ID), model.LinkSecurityReviewTask)
	sec := reloaded(t, ctx, store, secID)
	findingRes := toolResult(reviewFinding("restrict proxy scope", "pkg/gitproxy/proxy.go", 1))
	if err := orchestrator.ProcessResult(ctx, store, client, sec, findingRes, secID+"-1", baseTime.Add(10*time.Minute)); err != nil {
		t.Fatalf("ProcessResult (sec findings): %v", err)
	}

	// Add a standing code-review finding and a clean code review completing after it.
	codeRev := model.Task{
		ID:        "code-rev-clean",
		Title:     "Code review",
		Filing:    model.Filing{Reason: model.ReasonReview},
		Target:    task.Target,
		Links:     []model.Link{{Kind: model.LinkProposedBy, Target: task.ID}},
		CreatedAt: &baseTime,
	}
	if err := store.PutTask(ctx, codeRev); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkReviewTask, Target: codeRev.ID})
		model.AddReviewFindings(tk, model.ReviewFindingsRecord{
			Gate:       model.GateReview,
			ReviewTask: "code-rev-old",
			Summary:    "rename helper",
			At:         baseTime.Add(12 * time.Minute),
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	done := baseTime.Add(20 * time.Minute)
	if err := store.Observe(ctx, model.Observation{TaskID: codeRev.ID, CompletedAt: &done}); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.SyncReviews(ctx, store, baseTime.Add(25*time.Minute)); err != nil {
		t.Fatalf("SyncReviews: %v", err)
	}

	got := reloaded(t, ctx, store, task.ID)
	if got.AutoMerge {
		t.Fatal("clean code review restored auto-merge over security findings")
	}
	if rfs := model.ReviewFindingsForGate(got, model.GateReview); len(rfs) != 0 {
		t.Errorf("code-review findings not cleared: %+v", rfs)
	}
	if rfs := model.ReviewFindingsForGate(got, model.GateSecurityReview); len(rfs) != 1 || rfs[0].ReviewTask != secID {
		t.Errorf("security-review findings altered: %+v", rfs)
	}
	if q, ok := model.ReviewQuestionsOf(got); !ok || q.ReviewTaskID != secID {
		t.Errorf("clean code review cleared security review questions: got %+v (ok=%v)", q, ok)
	}
}
