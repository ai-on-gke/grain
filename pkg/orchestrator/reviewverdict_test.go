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

// Every kind of review records the head it read (model.LinkReviewedHead)
// and, as each of its runs finishes, the verdict it reached
// (model.LinkReviewVerdict): the record a report over many reviews reads
// instead of each review's closing note (reviewverdict.go).

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/trustboundary"
)

// categorized is reviewFinding filed under category.
func categorized(category, body, path string, line int) agent.ToolCall {
	call := reviewFinding(body, path, line)
	call.Arguments["category"] = category
	return call
}

func verdictOf(t *testing.T, ctx context.Context, store *model.Store, reviewID string) model.ReviewVerdictRecord {
	t.Helper()
	v, ok := model.ReviewVerdictOf(reloaded(t, ctx, store, reviewID))
	if !ok {
		t.Fatalf("review %s recorded no verdict", reviewID)
	}
	return v
}

// A declared review is stacked on the change's branch and filed by
// SyncReviews, which reads no GitHub: its head is the reviewed branch's
// as its first run is dispatched, and a later dispatch keeps it.
func TestAStackedReviewRecordsTheHeadItWasDispatchedAt(t *testing.T) {
	t.Parallel()
	store, ctx, sim, client, reviewed, review := reviewedAndReviewing(t)
	if got := model.ReviewedHead(review); got != "" {
		t.Fatalf("a stacked review had a head before dispatch: %s", got)
	}
	head := branchSHA(t, sim.BareRepo, model.BranchName(reviewed.ID))
	orchestrator.RecordDispatchedReviewHead(ctx, store, client, review)
	if got := model.ReviewedHead(reloaded(t, ctx, store, review.ID)); got != head {
		t.Fatalf("reviewed head = %q, want %s, the reviewed branch's head at dispatch", got, head)
	}

	pushFile(t, sim.BareRepo, model.BranchName(reviewed.ID), "later.go", "package later\n")
	orchestrator.RecordDispatchedReviewHead(ctx, store, client, reloaded(t, ctx, store, review.ID))
	if got := model.ReviewedHead(reloaded(t, ctx, store, review.ID)); got != head {
		t.Fatalf("reviewed head = %q after a second dispatch, want the first run's %s kept", got, head)
	}

	// And a task that is not a review records none.
	orchestrator.RecordDispatchedReviewHead(ctx, store, client, reviewed)
	if got := model.ReviewedHead(reloaded(t, ctx, store, reviewed.ID)); got != "" {
		t.Fatalf("the reviewed task itself recorded a reviewed head: %s", got)
	}
}

func TestAStackedReviewRecordsItsVerdict(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		push       bool
		calls      []agent.ToolCall
		want       model.ReviewVerdict
		categories []string
		pushed     bool
	}{{
		name:  "clean",
		calls: []agent.ToolCall{{Name: "comment_on_issue", Arguments: map[string]any{"comment": "nothing to fix"}}},
		want:  model.VerdictClean,
	}, {
		name: "findings by category",
		calls: []agent.ToolCall{
			categorized("tests", "nothing covers the retry", "retry.go", 3),
			categorized("Security", "the token is logged", "", 0),
			reviewFinding("rename this", "", 0),
			categorized("style", "not a category grain knows", "", 0),
		},
		want:       model.VerdictFindings,
		categories: []string{"tests", "security", "other", "other"},
	}, {
		name:   "fixes pushed",
		push:   true,
		calls:  []agent.ToolCall{{Name: "run_command", Text: "pushed the fixes"}},
		want:   model.VerdictFindings,
		pushed: true,
	}, {
		name: "question",
		calls: []agent.ToolCall{
			categorized("compatibility", "this drops the v1 field", "", 0),
			{Name: "ask_question", Arguments: map[string]any{"question": "Keep the v1 field?", "summary": "Keep v1?"}},
		},
		want:       model.VerdictQuestions,
		categories: []string{"compatibility"},
	}, {
		// A review that holds the change for a person has not passed it.
		name: "held for a person's review",
		calls: []agent.ToolCall{
			{Name: "request_human_review", Arguments: map[string]any{"reason": "the brief's intent is unclear"}},
			{Name: "comment_on_issue", Arguments: map[string]any{"comment": "code is fine; a person must decide"}},
		},
		want: model.VerdictQuestions,
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, ctx, sim, client, _, review := reviewedAndReviewing(t)
			if tc.push {
				pushBranch(t, sim.BareRepo, model.BranchName(review.ID))
			}
			at := baseTime.Add(10 * time.Minute)
			if err := orchestrator.ProcessResult(ctx, store, client, review, toolResult(tc.calls...), review.ID+"-1", at); err != nil {
				t.Fatalf("ProcessResult: %v", err)
			}
			v := verdictOf(t, ctx, store, review.ID)
			if v.Verdict != tc.want || v.FindingCount != len(tc.categories) || !slices.Equal(v.Categories, tc.categories) ||
				v.FixesPushed != tc.pushed || v.RunID != review.ID+"-1" || !v.At.Equal(at) {
				t.Fatalf("verdict = %+v, want %s with categories %v, fixes pushed %v", v, tc.want, tc.categories, tc.pushed)
			}
		})
	}
}

// A run that stops to wait is not a verdict; the run that finishes is.
func TestAReviewThatSuspendedRecordsNoVerdictUntilItFinishes(t *testing.T) {
	t.Parallel()
	store, ctx, _, client, _, review := reviewedAndReviewing(t)
	res := toolResult(agent.ToolCall{Name: "resume_later", Arguments: map[string]any{"reason": "waiting for CI"}})
	if err := orchestrator.ProcessResult(ctx, store, client, review, res, review.ID+"-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if v, ok := model.ReviewVerdictOf(reloaded(t, ctx, store, review.ID)); ok {
		t.Fatalf("a suspended run recorded verdict %+v", v)
	}
}

// A gating trigger's review (the pr-review template here) records the head
// it was filed for, and each re-review of a later head records its own --
// which is what tells two reviews of one head from a review per push.
func TestAPathTriggerReviewRecordsItsHeadAndVerdict(t *testing.T) {
	t.Parallel()
	ctx, store, sim, client, task := finishGated(t,
		[]model.PathTrigger{gateTrigger("secrets", model.TriggerHumanOnFindings, "config/secrets")},
		"config/secrets/prod.yml")
	head := branchSHA(t, sim.BareRepo, model.BranchName(task.ID))

	firstID := distinctGatingIDs(reloaded(t, ctx, store, task.ID))[0]
	first := reloaded(t, ctx, store, firstID)
	if got := model.ReviewedHead(first); got != head || linkOf(first, model.LinkReviewedHead) != head {
		t.Fatalf("reviewed head = %q (link %q), want %s", got, linkOf(first, model.LinkReviewedHead), head)
	}
	res := toolResult(categorized("security", "plaintext token", "config/secrets/prod.yml", 1))
	if err := orchestrator.ProcessResult(ctx, store, client, first, res, firstID+"-1", baseTime.Add(10*time.Minute)); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if v := verdictOf(t, ctx, store, firstID); v.Verdict != model.VerdictFindings || v.FindingCount != 1 ||
		!slices.Equal(v.Categories, []string{"security"}) || v.FixesPushed || v.Head != head {
		t.Fatalf("verdict = %+v, want one security finding at %s, no fixes", v, head)
	}
	completeReview(t, ctx, store, firstID)
	syncTriggerHolds(t, ctx, store, client, baseTime.Add(20*time.Minute))

	pushFile(t, sim.BareRepo, model.BranchName(task.ID), "config/secrets/prod.yml", "rotated\n")
	next := judgeHead(t, ctx, store, client, sim.BareRepo, task, baseTime.Add(30*time.Minute))
	ids := distinctGatingIDs(reloaded(t, ctx, store, task.ID))
	if len(ids) != 1 || ids[0] == firstID {
		t.Fatalf("want a re-review of the new head, got %v", ids)
	}
	if got := model.ReviewedHead(reloaded(t, ctx, store, ids[0])); got != next || got == head {
		t.Fatalf("re-review's reviewed head = %q, want the new head %s", got, next)
	}
}

// The security hold's review records the head the hold classified, and a
// clean in-place review's verdict says so; one that pushed fixes says that.
func TestASecurityReviewRecordsTheHeadItWasFiledForAndItsVerdict(t *testing.T) {
	t.Parallel()
	for _, fix := range []string{"", "pkg/widgets/validate.go"} {
		store, ctx := openStore(t)
		sim, client, task, reviewID := heldTaskWithSim(t, ctx, store)
		classified := linkOf(reloaded(t, ctx, store, task.ID), model.LinkTrustBoundaryCleared)
		head := branchSHA(t, sim.BareRepo, model.BranchName(task.ID))
		if classified != trustboundary.Version+"@"+head {
			t.Fatalf("classified %q, want the branch head %s", classified, head)
		}
		if got := model.ReviewedHead(reloaded(t, ctx, store, reviewID)); got != head {
			t.Fatalf("fix=%q: security review's reviewed head = %q, want %s", fix, got, head)
		}

		finishInPlaceReview(t, ctx, store, sim, client, task, reviewID, fix)
		v := verdictOf(t, ctx, store, reviewID)
		want := model.VerdictClean
		if fix != "" {
			want = model.VerdictFindings
		}
		if v.Verdict != want || v.FixesPushed != (fix != "") || v.FindingCount != 0 || v.Head != head {
			t.Fatalf("fix=%q: verdict = %+v, want %s at %s", fix, v, want, head)
		}
		// The head it read stays the one it was filed for; the head it
		// left is LinkReviewerHead's.
		if got := model.ReviewedHead(reloaded(t, ctx, store, reviewID)); got != head {
			t.Fatalf("fix=%q: reviewed head moved to %q after the review pushed", fix, got)
		}
	}
}
