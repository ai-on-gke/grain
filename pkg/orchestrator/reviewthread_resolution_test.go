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
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// When a repository enforces "require conversation resolution", inline
// findings posted by grain's review task open review threads on the PR.
// Once the owner answers the review questions and the owner's decision run
// handles them (pushing a fix or recording a "keep as is" decision), grain
// replies to each relayed thread with a one-line summary, resolves the
// thread via ResolveReviewThread, and merges the PR cleanly.
func TestReviewTaskThreadsAreResolvedAfterOwnerRunHandlesThemAndPRMerges(t *testing.T) {
	t.Parallel()
	store, ctx, sim, client, reviewed, review := reviewedAndReviewing(t)
	sim.RequireConversationResolution = true

	// 1. The review task posts an inline finding via add_review_comment.
	findingResult := toolResult(
		reviewFinding("Should the core build also publish the google image? Recommend keeping it core-only.", "Makefile", 3),
	)
	if err := orchestrator.ProcessResult(ctx, store, client, review, findingResult, review.ID+"-1", baseTime); err != nil {
		t.Fatalf("ProcessResult (review): %v", err)
	}

	got := reloaded(t, ctx, store, reviewed.ID)
	relayed := model.RelayedReviewThreadsOf(got)
	if len(relayed) != 1 {
		t.Fatalf("RelayedReviewThreadsOf = %+v, want 1 tracked thread", relayed)
	}
	if relayed[0].ThreadID == "" || relayed[0].CommentID == 0 || relayed[0].Decided {
		t.Fatalf("relayed thread = %+v, want populated CommentID/ThreadID and Decided=false", relayed[0])
	}
	threadID := relayed[0].ThreadID

	// 2. The owner replies with their decision and SyncReviewQuestions queues the decision run.
	say(t, ctx, store, reviewed.ID, owner, "Agreed, fix Makefile to keep it core-only.", baseTime.Add(time.Minute))
	if err := orchestrator.SyncReviewQuestions(ctx, store, baseTime.Add(2*time.Minute)); err != nil {
		t.Fatalf("SyncReviewQuestions: %v", err)
	}
	got = reloaded(t, ctx, store, reviewed.ID)
	relayed = model.RelayedReviewThreadsOf(got)
	if len(relayed) != 1 || !relayed[0].Decided {
		t.Fatalf("RelayedReviewThreadsOf after owner reply = %+v, want Decided=true", relayed)
	}

	// Verify the owner run's prompt tells it to resolve each relayed thread once handled.
	prompt := orchestrator.BuildPrompt(got, "/work", "/home/debian", false, 0, orchestrator.History{}, nil)
	for _, want := range []string{"resolve_review_thread", threadID, fmt.Sprintf("comment %d", relayed[0].CommentID)} {
		if !strings.Contains(prompt, want) {
			t.Errorf("owner run prompt lacks %q:\n%s", want, prompt)
		}
	}

	// 3. The owner's decision run pushes a fix (without calling resolve_review_thread itself).
	pushBranch(t, sim.BareRepo, model.BranchName(reviewed.ID))
	ownerRunResult := toolResult(agent.ToolCall{
		Name:      "comment_on_issue",
		Arguments: map[string]any{"comment": "Kept the core build core-only in Makefile."},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, got, ownerRunResult, reviewed.ID+"-2", baseTime.Add(3*time.Minute)); err != nil {
		t.Fatalf("ProcessResult (owner decision run): %v", err)
	}

	// The relayed thread must be replied to with one line and resolved via ResolveReviewThread.
	if !sim.ResolvedThreads[threadID] {
		t.Fatalf("expected relayed review thread %s to be resolved after owner run, got ResolvedThreads=%+v", threadID, sim.ResolvedThreads)
	}
	prNum := sim.PullRequests[0].Number
	comments := sim.ReviewComments[prNum]
	if len(comments) < 2 || comments[len(comments)-1].InReplyTo != relayed[0].CommentID {
		t.Fatalf("expected a reply on review comment %d, got comments=%+v", relayed[0].CommentID, comments)
	}
	if !strings.Contains(comments[len(comments)-1].Body, "Kept the core build core-only") {
		t.Errorf("reply body = %q, want one-line summary of what was done", comments[len(comments)-1].Body)
	}

	// 4. The merge queue merges the PR cleanly even with RequireConversationResolution enabled.
	markMergeable(sim)
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(4*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if !sim.PullRequests[0].Merged {
		t.Fatalf("expected PR #%d to merge after review task thread was resolved", prNum)
	}
}

// Belt-and-braces in the merge queue: if a PR is blocked only by unresolved
// threads and every unresolved thread was authored by grain's review relay
// and its finding has a recorded decision, the merge queue resolves them
// and retries the merge in the same cycle.
func TestMergeQueueBeltAndBracesResolvesDecidedGrainReviewThreadsAndRetriesMerge(t *testing.T) {
	t.Parallel()
	store, ctx, sim, client, reviewed, review := reviewedAndReviewing(t)
	sim.RequireConversationResolution = true

	findingResult := toolResult(
		reviewFinding("Consider renaming this helper.", "pkg/thing/thing.go", 12),
	)
	if err := orchestrator.ProcessResult(ctx, store, client, review, findingResult, review.ID+"-1", baseTime); err != nil {
		t.Fatalf("ProcessResult (review): %v", err)
	}

	got := reloaded(t, ctx, store, reviewed.ID)
	relayed := model.RelayedReviewThreadsOf(got)
	if len(relayed) != 1 {
		t.Fatalf("RelayedReviewThreadsOf = %+v, want 1", relayed)
	}
	threadID := relayed[0].ThreadID

	// Owner decides "Ship as is" by re-enabling AutoMerge and commenting.
	if err := store.UpdateTask(ctx, reviewed.ID, func(tk *model.Task) error {
		tk.AutoMerge = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	say(t, ctx, store, reviewed.ID, owner, "Keep as is.", baseTime.Add(time.Minute))
	if err := orchestrator.SyncReviewQuestions(ctx, store, baseTime.Add(2*time.Minute)); err != nil {
		t.Fatalf("SyncReviewQuestions: %v", err)
	}

	// Thread is still unresolved before SyncPullRequests runs.
	if sim.ResolvedThreads[threadID] {
		t.Fatalf("expected thread %s to still be unresolved before SyncPullRequests", threadID)
	}

	// SyncPullRequests hits the conversation-resolution refusal, resolves the
	// decided grain-relayed thread, and retries the merge in the same cycle.
	markMergeable(sim)
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(3*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if !sim.ResolvedThreads[threadID] {
		t.Fatalf("expected merge queue belt-and-braces to resolve thread %s", threadID)
	}
	if !sim.PullRequests[0].Merged {
		t.Fatalf("expected PR to merge after belt-and-braces resolved the relayed review thread")
	}
}

// Threads written by people are never auto-resolved. When human threads
// remain unresolved under "require conversation resolution", the merge
// queue leaves them alone and surfaces
// "blocked: N unresolved review conversations by <people>" on the task and
// in the merge-queue view.
func TestHumanReviewThreadIsLeftAloneAndReportedClearly(t *testing.T) {
	t.Parallel()
	store, ctx, sim, client, reviewed, _ := reviewedAndReviewing(t)
	sim.RequireConversationResolution = true

	// Complete the review cleanly so the PR is ready for the merge queue.
	prNum := sim.PullRequests[0].Number
	line := 10
	sim.ReviewComments[prNum] = []github.ReviewComment{
		{
			ID:   901,
			Path: "main.go",
			Line: &line,
			Body: "Please handle the nil receiver case here.",
			User: "alice",
		},
	}
	humanThreadID := "thread-901"

	// Mark the review task completed with nothing_needed so it does not hold the merge.
	review := reviewTaskOf(t, ctx, store, reviewed.ID)
	cleanReview := toolResult(agent.ToolCall{
		Name:      "nothing_needed",
		Arguments: map[string]any{"reason": "LGTM"},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, review, cleanReview, review.ID+"-1", baseTime); err != nil {
		t.Fatalf("ProcessResult (clean review): %v", err)
	}

	markMergeable(sim)
	err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute))
	if err == nil {
		t.Fatal("expected SyncPullRequests to report the merge refusal for unresolved human thread")
	}

	// Human thread must NOT be auto-resolved, and PR must NOT be merged.
	if sim.ResolvedThreads[humanThreadID] {
		t.Fatalf("human review thread %s must never be auto-resolved", humanThreadID)
	}
	if sim.PullRequests[0].Merged {
		t.Fatal("PR must not merge while a human review thread is unresolved")
	}

	wantPhrase := "blocked: 1 unresolved review conversations by alice"
	obs, err := store.GetObservation(ctx, reviewed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(obs.MergeQueueNote, wantPhrase) {
		t.Errorf("MergeQueueNote = %q, want %q", obs.MergeQueueNote, wantPhrase)
	}
	bodies := strings.Join(commentBodies(t, ctx, store, reviewed.ID), "\n")
	if !strings.Contains(bodies, wantPhrase) {
		t.Errorf("task comments lack %q:\n%s", wantPhrase, bodies)
	}
}

// When the owner replies "won't fix / keep as is" and the owner's decision
// run finishes with nothing_needed (without pushing a commit), the relayed
// review thread is still replied to and resolved, and the PR merges.
func TestReviewTaskThreadsAreResolvedWhenOwnerRunKeepsAsIsWithoutPushing(t *testing.T) {
	t.Parallel()
	store, ctx, sim, client, reviewed, review := reviewedAndReviewing(t)
	sim.RequireConversationResolution = true

	findingResult := toolResult(
		reviewFinding("Should we split this package?", "pkg/thing/thing.go", 5),
	)
	if err := orchestrator.ProcessResult(ctx, store, client, review, findingResult, review.ID+"-1", baseTime); err != nil {
		t.Fatalf("ProcessResult (review): %v", err)
	}

	got := reloaded(t, ctx, store, reviewed.ID)
	relayed := model.RelayedReviewThreadsOf(got)
	if len(relayed) != 1 {
		t.Fatalf("RelayedReviewThreadsOf = %+v, want 1", relayed)
	}
	threadID := relayed[0].ThreadID

	say(t, ctx, store, reviewed.ID, owner, "Won't fix: keep it in one package.", baseTime.Add(time.Minute))
	if err := orchestrator.SyncReviewQuestions(ctx, store, baseTime.Add(2*time.Minute)); err != nil {
		t.Fatalf("SyncReviewQuestions: %v", err)
	}

	got = reloaded(t, ctx, store, reviewed.ID)
	ownerRunResult := toolResult(agent.ToolCall{
		Name:      "nothing_needed",
		Arguments: map[string]any{"reason": "Owner decided to keep the single package as is."},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, got, ownerRunResult, reviewed.ID+"-2", baseTime.Add(3*time.Minute)); err != nil {
		t.Fatalf("ProcessResult (owner decision run): %v", err)
	}

	if !sim.ResolvedThreads[threadID] {
		t.Fatalf("expected relayed review thread %s to be resolved after keep-as-is run", threadID)
	}
	markMergeable(sim)
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(4*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if !sim.PullRequests[0].Merged {
		t.Fatal("expected PR to merge after keep-as-is run resolved the relayed review thread")
	}
}
