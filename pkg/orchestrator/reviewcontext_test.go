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
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// A first review on a pull request with no sibling review renders neither the
// earlier-reviews section nor the parallel-reviews section.
func TestFirstReviewRendersNeitherEarlierNorParallelSection(t *testing.T) {
	t.Parallel()

	t.Run("security-hold", func(t *testing.T) {
		t.Parallel()
		store, ctx := openStore(t)
		_, _, _, reviewID := heldTaskWithSim(t, ctx, store)
		body := reloaded(t, ctx, store, reviewID).Body
		for _, unwanted := range []string{
			"Earlier reviews of this pull request",
			"Parallel reviews of this head",
			"This is an incremental review.",
		} {
			if strings.Contains(body, unwanted) {
				t.Errorf("first security review unexpectedly contains %q:\n%s", unwanted, body)
			}
		}
	})

	t.Run("path-trigger", func(t *testing.T) {
		t.Parallel()
		ctx, store, _, _, task := finishGated(t,
			[]model.PathTrigger{gateTrigger("every-pr", model.TriggerHumanOnFindings, "**")},
			"pkg/widget/widget.go")
		reviewID := distinctGatingIDs(reloaded(t, ctx, store, task.ID))[0]
		body := reloaded(t, ctx, store, reviewID).Body
		for _, unwanted := range []string{
			"Earlier reviews of this pull request",
			"Parallel reviews of this head",
			"This is an incremental review.",
		} {
			if strings.Contains(body, unwanted) {
				t.Errorf("first path-trigger review unexpectedly contains %q:\n%s", unwanted, body)
			}
		}
	})
}

// A security-hold re-review of a new head carries the earlier review's verdict
// and open or answered add_review_comment questions, and receives the
// incremental diff note for the commits since the reviewed head.
func TestSecurityHoldReReviewNamesEarlierReviewFindingsAndIncrementalDelta(t *testing.T) {
	t.Parallel()

	t.Run("open-question", func(t *testing.T) {
		t.Parallel()
		store, ctx := openStore(t)
		sim, client, task, firstID := heldTaskWithSim(t, ctx, store)
		branch := model.BranchName(task.ID)
		firstHead := branchSHA(t, sim.BareRepo, branch)

		first := reloaded(t, ctx, store, firstID)
		res := toolResult(
			reviewFinding("should this route require admin\nscope before writing?", "pkg/ui/server.go", 12),
			agent.ToolCall{Name: "comment_on_issue", Arguments: map[string]any{"comment": "Left one question."}},
		)
		if err := orchestrator.ProcessResult(ctx, store, client, first, res, firstID+"-1", baseTime.Add(10*time.Minute)); err != nil {
			t.Fatalf("ProcessResult (first review): %v", err)
		}

		// Author pushes a new trust-boundary file without answering in grain yet.
		pushFile(t, sim.BareRepo, branch, "pkg/gitproxy/proxy.go", "new proxy check\n")
		secondHead := branchSHA(t, sim.BareRepo, branch)
		run2 := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
		if err := orchestrator.ProcessResult(ctx, store, client, reloaded(t, ctx, store, task.ID), run2, "t1-2", baseTime.Add(30*time.Minute)); err != nil {
			t.Fatalf("ProcessResult (second push): %v", err)
		}

		secondID := linkOf(reloaded(t, ctx, store, task.ID), model.LinkSecurityReviewTask)
		if secondID == "" || secondID == firstID {
			t.Fatalf("expected new security review, got %q (first %q)", secondID, firstID)
		}
		second := reloaded(t, ctx, store, secondID)

		for _, want := range []string{
			"## Earlier reviews of this pull request",
			fmt.Sprintf("Task %s (`security-review` at `%s`): verdict `findings`; commits pushed: none; questions:", firstID, firstHead[:12]),
			"- [open] `pkg/ui/server.go:12`: should this route require admin scope before writing?",
			"**This is an incremental review.**",
			fmt.Sprintf("`git diff %s %s`", firstHead, secondHead),
			"`pkg/gitproxy/proxy.go`",
		} {
			if !strings.Contains(second.Body, want) {
				t.Errorf("second security review body missing %q:\n%s", want, second.Body)
			}
		}
		_, delta, _ := strings.Cut(second.Body, "This is an incremental review.")
		if strings.Contains(delta, "`pkg/ui/server.go`") {
			t.Errorf("incremental delta listed untouched file from first head:\n%s", delta)
		}
	})

	t.Run("answered-question", func(t *testing.T) {
		t.Parallel()
		store, ctx := openStore(t)
		sim, client, task, firstID := heldTaskWithSim(t, ctx, store)
		branch := model.BranchName(task.ID)

		first := reloaded(t, ctx, store, firstID)
		res := toolResult(
			reviewFinding("keep legacy endpoint or remove it?", "pkg/ui/server.go", 7),
			agent.ToolCall{Name: "comment_on_issue", Arguments: map[string]any{"comment": "Needs owner decision."}},
		)
		if err := orchestrator.ProcessResult(ctx, store, client, first, res, firstID+"-1", baseTime.Add(10*time.Minute)); err != nil {
			t.Fatalf("ProcessResult (first review): %v", err)
		}

		// Owner answers the open question on the held task; SyncReviewQuestions
		// settles the hold, and the redispatched run pushes the fix.
		if _, err := store.AddComment(ctx, model.Comment{
			TaskID:    task.ID,
			Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
			Body:      "Remove the legacy endpoint.",
			CreatedAt: baseTime.Add(15 * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
		if err := orchestrator.SyncReviewQuestions(ctx, store, baseTime.Add(16*time.Minute)); err != nil {
			t.Fatalf("SyncReviewQuestions: %v", err)
		}

		pushFile(t, sim.BareRepo, branch, "pkg/ui/server.go", "legacy endpoint removed\n")
		run2 := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
		if err := orchestrator.ProcessResult(ctx, store, client, reloaded(t, ctx, store, task.ID), run2, "t1-2", baseTime.Add(30*time.Minute)); err != nil {
			t.Fatalf("ProcessResult (fix run): %v", err)
		}

		secondID := linkOf(reloaded(t, ctx, store, task.ID), model.LinkSecurityReviewTask)
		if secondID == "" || secondID == firstID {
			t.Fatalf("expected new security review, got %q (first %q)", secondID, firstID)
		}
		second := reloaded(t, ctx, store, secondID)
		want := "- [answered] `pkg/ui/server.go:7`: keep legacy endpoint or remove it?"
		if !strings.Contains(second.Body, want) {
			t.Errorf("second security review body missing answered question %q:\n%s", want, second.Body)
		}
	})
}

// When an earlier security review pushed fixes onto the branch that touched the
// trust boundary, the follow-up security review names the earlier review, its
// clean (pushed fixes) verdict, the commit SHA it pushed, and reviews the delta.
func TestSecurityHoldReReviewNamesCommitsPushedByEarlierReview(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, firstID := heldTaskWithSim(t, ctx, store)
	branch := model.BranchName(task.ID)
	firstHead := branchSHA(t, sim.BareRepo, branch)

	finishInPlaceReview(t, ctx, store, sim, client, task, firstID, "pkg/ui/server.go")
	fixHead := branchSHA(t, sim.BareRepo, branch)

	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatalf("SyncSecurityHolds: %v", err)
	}
	secondID := linkOf(reloaded(t, ctx, store, task.ID), model.LinkSecurityReviewTask)
	if secondID == "" || secondID == firstID {
		t.Fatalf("expected follow-up security review for reviewer's trust-boundary fix, got %q", secondID)
	}
	second := reloaded(t, ctx, store, secondID)
	for _, want := range []string{
		"## Earlier reviews of this pull request",
		fmt.Sprintf("Task %s (`security-review` at `%s`): verdict `clean (pushed fixes)`; commits pushed: `%s`", firstID, firstHead[:12], fixHead[:12]),
		"**This is an incremental review.**",
		fmt.Sprintf("`git diff %s %s`", firstHead, fixHead),
	} {
		if !strings.Contains(second.Body, want) {
			t.Errorf("follow-up security review body missing %q:\n%s", want, second.Body)
		}
	}
}

// A security-hold re-review superseding an uncompleted review reviews the whole
// change in full rather than restricting itself to a delta from an unread head.
func TestSecurityHoldReReviewSupersedingUnfinishedReviewReadsInFull(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, firstID := heldTaskWithSim(t, ctx, store)

	secondID := rerunHeld(t, ctx, store, sim, client, task)
	if secondID == "" || secondID == firstID {
		t.Fatalf("expected second security review, got %q", secondID)
	}
	second := reloaded(t, ctx, store, secondID)
	if strings.Contains(second.Body, "This is an incremental review.") {
		t.Errorf("security review superseding an unstarted review should not be incremental:\n%s", second.Body)
	}
	if strings.Contains(second.Body, "Earlier reviews of this pull request") {
		t.Errorf("unstarted superseded review with no runs or findings should not appear as an earlier review:\n%s", second.Body)
	}
}

// When two review kinds are filed for the same head -- grain's security review
// beside pr-review, or two distinct gating triggers -- each review's body names
// the other review task and instructs the reviewer to check existing PR review
// threads with `grain pr open` rather than re-raising duplicate questions.
func TestParallelReviewsOfSameHeadNameEachOtherInEachBody(t *testing.T) {
	t.Parallel()

	t.Run("security-plus-pr-review", func(t *testing.T) {
		t.Parallel()
		store, ctx := openStore(t)
		sim, client := newSim(t, "acme", "widgets", "main")
		everyPR := model.PathTrigger{
			Name: "every-pr", On: model.TriggerOnPullRequest, Paths: []string{"**"},
			Template: templateID("pr-review"), Gate: true, Human: model.TriggerHumanOnFindings, AutoApprove: true,
		}
		declareTriggers(t, ctx, store, branchSHA(t, sim.BareRepo, "main"),
			map[string]string{"pr-review": "Review {{pr}} for correctness."}, everyPR)

		task := finishTouching(t, ctx, store, sim, "t1", "pkg/gitproxy/proxy.go", true)
		res := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
		if err := orchestrator.ProcessResult(ctx, store, client, task, res, "t1-1", baseTime); err != nil {
			t.Fatalf("ProcessResult: %v", err)
		}

		got := reloaded(t, ctx, store, task.ID)
		secID := linkOf(got, model.LinkSecurityReviewTask)
		trigIDs := distinctGatingIDs(got)
		if secID == "" || len(trigIDs) != 1 || trigIDs[0] == secID {
			t.Fatalf("expected distinct security (%q) and pr-review (%v) tasks", secID, trigIDs)
		}
		prReviewID := trigIDs[0]

		secReview := reloaded(t, ctx, store, secID)
		prReview := reloaded(t, ctx, store, prReviewID)

		for _, want := range []string{
			"## Parallel reviews of this head",
			fmt.Sprintf("Task %s (`pr-review`)", prReviewID),
			fmt.Sprintf("Do not re-raise a question or finding that task %s has already posted", prReviewID),
			"`grain pr open`",
		} {
			if !strings.Contains(secReview.Body, want) {
				t.Errorf("security review body missing %q:\n%s", want, secReview.Body)
			}
		}
		for _, want := range []string{
			"## Parallel reviews of this head",
			fmt.Sprintf("Task %s (`security-review`)", secID),
			fmt.Sprintf("Do not re-raise a question or finding that task %s has already posted", secID),
			"`grain pr open`",
		} {
			if !strings.Contains(prReview.Body, want) {
				t.Errorf("pr-review body missing %q:\n%s", want, prReview.Body)
			}
		}
		if strings.Contains(secReview.Body, "Earlier reviews of this pull request") ||
			strings.Contains(prReview.Body, "Earlier reviews of this pull request") {
			t.Errorf("first-head parallel reviews should not render earlier-reviews section")
		}
	})

	t.Run("two-triggers", func(t *testing.T) {
		t.Parallel()
		store, ctx := openStore(t)
		sim, client := newSim(t, "acme", "widgets", "main")
		trA := model.PathTrigger{
			Name: "every-pr", On: model.TriggerOnPullRequest, Paths: []string{"**"},
			Template: templateID("pr-review"), Gate: true, Human: model.TriggerHumanOnFindings, AutoApprove: true,
		}
		trB := model.PathTrigger{
			Name: "release", On: model.TriggerOnPullRequest, Paths: []string{"release/**"},
			Template: templateID("release-review"), Gate: true, Human: model.TriggerHumanOnFindings, AutoApprove: true,
		}
		declareTriggers(t, ctx, store, branchSHA(t, sim.BareRepo, "main"),
			map[string]string{
				"pr-review":      "General review of {{pr}}.",
				"release-review": "Release review of {{pr}}.",
			}, trA, trB)

		task := finishTouching(t, ctx, store, sim, "t1", "release/cut.sh", true)
		res := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
		if err := orchestrator.ProcessResult(ctx, store, client, task, res, "t1-1", baseTime); err != nil {
			t.Fatalf("ProcessResult: %v", err)
		}

		ids := distinctGatingIDs(reloaded(t, ctx, store, task.ID))
		if len(ids) != 2 {
			t.Fatalf("expected 2 parallel trigger reviews, got %v", ids)
		}
		r0 := reloaded(t, ctx, store, ids[0])
		r1 := reloaded(t, ctx, store, ids[1])
		if !strings.Contains(r0.Body, fmt.Sprintf("Task %s", r1.ID)) || !strings.Contains(r0.Body, "`grain pr open`") {
			t.Errorf("first trigger review body does not name sibling %s:\n%s", r1.ID, r0.Body)
		}
		if !strings.Contains(r1.Body, fmt.Sprintf("Task %s", r0.ID)) || !strings.Contains(r1.Body, "`grain pr open`") {
			t.Errorf("second trigger review body does not name sibling %s:\n%s", r0.ID, r1.Body)
		}
	})
}

// Earlier reviews of any kind on the same PR are listed on head N+1, and total
// question text is capped at roughly 2 KB with an omitted-count notice.
func TestEarlierReviewsIncludeAllKindsAndCapQuestionsAtAboutTwoKB(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	everyPR := model.PathTrigger{
		Name: "every-pr", On: model.TriggerOnPullRequest, Paths: []string{"**"},
		Template: templateID("pr-review"), Gate: true, Human: model.TriggerHumanOnFindings, AutoApprove: true,
	}
	declareTriggers(t, ctx, store, branchSHA(t, sim.BareRepo, "main"),
		map[string]string{"pr-review": "Review {{pr}} for correctness."}, everyPR)

	task := finishTouching(t, ctx, store, sim, "t1", "pkg/gitproxy/proxy.go", true)
	res := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, res, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	got := reloaded(t, ctx, store, task.ID)
	sec1ID := linkOf(got, model.LinkSecurityReviewTask)
	pr1ID := distinctGatingIDs(got)[0]

	// Security review finishes clean; pr-review posts 20 lengthy questions (~4 KB raw).
	completeReview(t, ctx, store, sec1ID)
	var calls []agent.ToolCall
	for i := 1; i <= 20; i++ {
		body := fmt.Sprintf("question %02d: %s", i, strings.Repeat("verify edge case handling on nil input ", 5))
		calls = append(calls, reviewFinding(body, "pkg/gitproxy/proxy.go", i))
	}
	calls = append(calls, agent.ToolCall{Name: "comment_on_issue", Arguments: map[string]any{"comment": "20 findings."}})
	if err := orchestrator.ProcessResult(ctx, store, client, reloaded(t, ctx, store, pr1ID),
		toolResult(calls...), pr1ID+"-1", baseTime.Add(10*time.Minute)); err != nil {
		t.Fatalf("ProcessResult (pr-review): %v", err)
	}
	completeReview(t, ctx, store, pr1ID)

	// Push head 2 touching the trust boundary again: both sec2 and pr2 are filed.
	pushFile(t, sim.BareRepo, model.BranchName(task.ID), "pkg/gitproxy/proxy.go", "updated proxy\n")
	run2 := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, reloaded(t, ctx, store, task.ID), run2, "t1-2", baseTime.Add(40*time.Minute)); err != nil {
		t.Fatalf("ProcessResult (head 2): %v", err)
	}

	got2 := reloaded(t, ctx, store, task.ID)
	sec2 := reloaded(t, ctx, store, linkOf(got2, model.LinkSecurityReviewTask))
	pr2 := reloaded(t, ctx, store, distinctGatingIDs(got2)[0])

	for label, review := range map[string]model.Task{"sec2": sec2, "pr2": pr2} {
		if !strings.Contains(review.Body, fmt.Sprintf("Task %s (`security-review`", sec1ID)) {
			t.Errorf("%s missing earlier security review %s:\n%s", label, sec1ID, review.Body)
		}
		if !strings.Contains(review.Body, fmt.Sprintf("Task %s (`pr-review`", pr1ID)) {
			t.Errorf("%s missing earlier pr-review %s:\n%s", label, pr1ID, review.Body)
		}
		if !strings.Contains(review.Body, "omitted; read full threads with `grain pr open`") {
			t.Errorf("%s did not cap 20 verbose questions with omitted notice:\n%s", label, review.Body)
		}
		_, afterEarlier, ok := strings.Cut(review.Body, "## Earlier reviews of this pull request\n\n")
		if !ok {
			t.Fatalf("%s missing earlier-reviews header", label)
		}
		section, _, _ := strings.Cut(afterEarlier, "\n**This is an incremental review.**")
		var questionBytes int
		for _, line := range strings.Split(section, "\n") {
			if strings.HasPrefix(line, "  - ") {
				questionBytes += len(line) + 1
			}
		}
		if questionBytes > 2150 {
			t.Errorf("%s question lines = %d bytes, want capped around 2 KB:\n%s", label, questionBytes, section)
		}
	}
}

// When a running review of head 1 finishes with findings while head 2's review
// is queued behind it, head 2's body is refreshed with those findings before
// dispatch; and if head 1's review instead closes without finishing, stripping
// the incremental restriction preserves both the earlier-reviews and
// parallel-reviews sections.
func TestQueuedDependentReviewRefreshesOnFinishAndPreservesContextWhenStripped(t *testing.T) {
	t.Parallel()

	t.Run("refreshes-on-finish", func(t *testing.T) {
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

		pushFile(t, sim.BareRepo, model.BranchName(task.ID), "config/secrets/staging.yml", "next\n")
		judgeHead(t, ctx, store, client, sim.BareRepo, task, baseTime.Add(2*time.Minute))
		secondID := distinctGatingIDs(reloaded(t, ctx, store, task.ID))[0]
		if !strings.Contains(reloaded(t, ctx, store, secondID).Body, "verdict `running`") {
			t.Fatalf("expected queued dependent review to show running verdict initially:\n%s",
				reloaded(t, ctx, store, secondID).Body)
		}

		if err := store.FinishRun(ctx, firstID+"-1", baseTime.Add(3*time.Minute), "succeeded", ""); err != nil {
			t.Fatal(err)
		}
		res := toolResult(
			reviewFinding("rotate key before deploy", "config/secrets/prod.yml", 1),
			agent.ToolCall{Name: "comment_on_issue", Arguments: map[string]any{"comment": "One finding."}},
		)
		if err := orchestrator.ProcessResult(ctx, store, client, reloaded(t, ctx, store, firstID),
			res, firstID+"-1", baseTime.Add(3*time.Minute)); err != nil {
			t.Fatalf("ProcessResult (first review): %v", err)
		}

		refreshed := reloaded(t, ctx, store, secondID)
		for _, want := range []string{
			"verdict `findings`",
			"- [open] `config/secrets/prod.yml:1`: rotate key before deploy",
		} {
			if !strings.Contains(refreshed.Body, want) {
				t.Errorf("queued review body was not refreshed with %q:\n%s", want, refreshed.Body)
			}
		}
	})

	t.Run("strip-incremental-preserves-context", func(t *testing.T) {
		t.Parallel()
		store, ctx := openStore(t)
		sim, client := newSim(t, "acme", "widgets", "main")
		everyPR := model.PathTrigger{
			Name: "every-pr", On: model.TriggerOnPullRequest, Paths: []string{"**"},
			Template: templateID("pr-review"), Gate: true, Human: model.TriggerHumanOnFindings, AutoApprove: true,
		}
		declareTriggers(t, ctx, store, branchSHA(t, sim.BareRepo, "main"),
			map[string]string{"pr-review": "Review {{pr}}."}, everyPR)

		task := finishTouching(t, ctx, store, sim, "t1", "pkg/ui/server.go", true)
		res := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
		if err := orchestrator.ProcessResult(ctx, store, client, task, res, "t1-1", baseTime); err != nil {
			t.Fatalf("ProcessResult: %v", err)
		}

		got := reloaded(t, ctx, store, task.ID)
		sec1ID := linkOf(got, model.LinkSecurityReviewTask)
		pr1ID := distinctGatingIDs(got)[0]
		completeReview(t, ctx, store, sec1ID)
		if err := store.StartRun(ctx, model.Run{
			ID: pr1ID + "-1", TaskID: pr1ID, Sandbox: "sb-pr1", Attempt: 1, StartedAt: baseTime.Add(time.Minute),
		}, model.Limits{}); err != nil {
			t.Fatal(err)
		}

		pushFile(t, sim.BareRepo, model.BranchName(task.ID), "pkg/ui/server.go", "next\n")
		run2 := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
		if err := orchestrator.ProcessResult(ctx, store, client, reloaded(t, ctx, store, task.ID), run2, "t1-2", baseTime.Add(5*time.Minute)); err != nil {
			t.Fatalf("ProcessResult (head 2): %v", err)
		}

		got2 := reloaded(t, ctx, store, task.ID)
		sec2ID := linkOf(got2, model.LinkSecurityReviewTask)
		pr2ID := distinctGatingIDs(got2)[0]

		closedAt := baseTime.Add(6 * time.Minute)
		if err := store.FinishRun(ctx, pr1ID+"-1", closedAt, "cancelled", ""); err != nil {
			t.Fatal(err)
		}
		if err := store.Observe(ctx, model.Observation{TaskID: pr1ID, ClosedAt: &closedAt}); err != nil {
			t.Fatal(err)
		}
		syncTriggerHolds(t, ctx, store, client, baseTime.Add(7*time.Minute))

		pr2 := reloaded(t, ctx, store, pr2ID)
		if strings.Contains(pr2.Body, "This is an incremental review.") {
			t.Errorf("expected incremental note stripped from %s:\n%s", pr2ID, pr2.Body)
		}
		for _, want := range []string{
			"## Earlier reviews of this pull request",
			fmt.Sprintf("Task %s (`security-review`", sec1ID),
			"## Parallel reviews of this head",
			fmt.Sprintf("Task %s (`security-review`)", sec2ID),
		} {
			if !strings.Contains(pr2.Body, want) {
				t.Errorf("stripping incremental review lost %q:\n%s", want, pr2.Body)
			}
		}
	})
}

// When a merge of the base branch brings in a trust-boundary file alongside new
// branch commits, the security re-review falls back to a full review while
// still listing the earlier review.
func TestSecurityHoldReReviewFallsBackToFullWhenBaseMergeBringsTrustBoundaryFile(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, firstID := heldTaskWithSim(t, ctx, store)
	branch := model.BranchName(task.ID)
	completeReview(t, ctx, store, firstID)
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(20*time.Minute)); err != nil {
		t.Fatal(err)
	}

	pushFileToBranch(t, sim.BareRepo, "main", "pkg/gitproxy/proxy.go", "base proxy change\n", "update proxy on main")
	mergeBaseResolving(t, sim.BareRepo, branch, "main", nil)
	pushFile(t, sim.BareRepo, branch, "pkg/ui/server.go", "new server edit\n")
	run2 := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, reloaded(t, ctx, store, task.ID), run2, "t1-2", baseTime.Add(40*time.Minute)); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	secondID := linkOf(reloaded(t, ctx, store, task.ID), model.LinkSecurityReviewTask)
	second := reloaded(t, ctx, store, secondID)
	if strings.Contains(second.Body, "This is an incremental review.") {
		t.Errorf("expected full security review when base merge brought in trust-boundary file:\n%s", second.Body)
	}
	if !strings.Contains(second.Body, "## Earlier reviews of this pull request") ||
		!strings.Contains(second.Body, fmt.Sprintf("Task %s (`security-review`", firstID)) {
		t.Errorf("full security re-review should still list earlier review %s:\n%s", firstID, second.Body)
	}
}
