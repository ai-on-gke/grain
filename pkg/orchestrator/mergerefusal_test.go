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
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// mergeCalls counts the merges grain has asked GitHub for -- what "the
// queue stopped retrying" is measured in.
func mergeCalls(sim *githubsim.Sim) int {
	n := 0
	for _, c := range sim.Calls {
		if c.Method == "PUT" && strings.HasSuffix(c.Path, "/merge") {
			n++
		}
	}
	return n
}

func prNumber(t *testing.T, sim *githubsim.Sim, branch string) int {
	t.Helper()
	for _, pr := range sim.PullRequests {
		if pr.Head == branch {
			return pr.Number
		}
	}
	t.Fatalf("no pull request from %s", branch)
	return 0
}

func mergedHead(sim *githubsim.Sim, branch string) bool {
	for _, pr := range sim.PullRequests {
		if pr.Head == branch {
			return pr.Merged
		}
	}
	return false
}

// A merge GitHub refuses for want of an approving review its rules
// require is waited for rather than retried: the row says who must
// approve and where, the change leaves the queue so the one behind it
// merges, and when the approval arrives the queue merges it by itself.
func TestAMergeWaitingOnAGitHubApprovalWaitsOutOfLineAndMergesWhenApproved(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "owned", "main")
	repo := model.RepoRef{Owner: "acme", Name: "owned"}
	first, firstBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	second, secondBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)

	setMergeable(sim, true)
	sim.CheckRuns[firstBranch] = greenChecks()
	sim.CheckRuns[secondBranch] = greenChecks()
	sim.RequireApprovals = 1
	firstPR, secondPR := prNumber(t, sim, firstBranch), prNumber(t, sim, secondBranch)
	sim.Approvals = map[int][]string{secondPR: {"alice"}} // the second change already has its approval
	sim.RequestedReviewers = map[int][]string{firstPR: {"acme/platform", "bob"}}
	sim.BranchRules = map[string][]map[string]any{"main": {{
		"type": "pull_request", "ruleset_id": 7,
		"parameters": map[string]any{"required_approving_review_count": 1, "require_code_owner_review": true},
	}}}
	sim.RulesetNames = map[int]string{7: "protect main"}

	// The head is refused: that is a wait, not the cycle failing.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	obs := mergeQueueStatus(t, ctx, store, first.ID)
	if obs.MergeQueueState != "waiting_github_approval" {
		t.Fatalf("state = %q (%q), want waiting_github_approval", obs.MergeQueueState, obs.MergeQueueNote)
	}
	for _, want := range []string{"@acme/platform", "@bob", "code owners", `"protect main"`, "1 approval still needed",
		fmt.Sprintf("/pull/%d/files", firstPR), "does not count toward GitHub's rule"} {
		if !strings.Contains(obs.MergeQueueNote, want) {
			t.Errorf("note = %q, want %q in it", obs.MergeQueueNote, want)
		}
	}
	if obs.MergeQueueBlockedAt != nil {
		t.Fatal("an approval wait was handed to a person as a failure")
	}
	if bodies := commentBodies(t, ctx, store, first.ID); len(bodies) != 1 || !strings.Contains(bodies[0], "Approvals on the two sides are separate") {
		t.Fatalf("want one comment saying what is being waited for, got %q", bodies)
	}

	// Not head-of-line: the change behind it merges on the next cycles.
	for i := 1; i <= 3 && !mergedHead(sim, secondBranch); i++ {
		if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Duration(i)*10*time.Second)); err != nil {
			t.Fatalf("SyncPullRequests: %v", err)
		}
	}
	if !mergedHead(sim, secondBranch) {
		t.Fatalf("the change behind an approval wait did not merge: %q", mergeQueueStatus(t, ctx, store, second.ID).MergeQueueState)
	}

	// And nothing is retried while the reviews stay as they are.
	calls := mergeCalls(sim)
	for _, at := range []time.Duration{time.Minute, 5 * time.Minute, time.Hour} {
		if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(at)); err != nil {
			t.Fatalf("SyncPullRequests: %v", err)
		}
	}
	if got := mergeCalls(sim); got != calls {
		t.Fatalf("the queue asked GitHub for the merge %d more times while nobody had approved it", got-calls)
	}
	if bodies := commentBodies(t, ctx, store, first.ID); len(bodies) != 1 {
		t.Fatalf("the wait was announced more than once: %q", bodies)
	}

	// The approval arrives, and the queue merges by itself.
	sim.Approvals[firstPR] = []string{"bob"}
	for i := 0; i < 3 && !mergedHead(sim, firstBranch); i++ {
		if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(2*time.Hour+time.Duration(i)*3*time.Minute)); err != nil {
			t.Fatalf("SyncPullRequests after the approval: %v", err)
		}
	}
	if !mergedHead(sim, firstBranch) {
		t.Fatal("the pull request did not merge once GitHub's required approval was given")
	}
}

// Checks GitHub's rules require, in a repository where grain does not run
// them, are said once, named, with the two ways out -- and the merge is
// not retried until GitHub reports them passing.
func TestARequiredGitHubCheckRefusalBlocksOnceAndSaysWhichChecks(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "required", "main")
	repo := model.RepoRef{Owner: "acme", Name: "required"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = greenChecks()
	sim.RefuseMerges = "Repository rule violations found\n\n4 of 4 required status checks are failing."
	sim.BranchRules = map[string][]map[string]any{"main": {{
		"type": "required_status_checks",
		"parameters": map[string]any{"required_status_checks": []map[string]any{
			{"context": "nightly"}, {"context": "e2e"},
		}},
	}}}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err == nil {
		t.Fatal("expected the refused merge to be reported")
	}
	obs := mergeQueueStatus(t, ctx, store, task.ID)
	if obs.MergeQueueState != "merge_failed" || obs.MergeQueueBlockedAt == nil {
		t.Fatalf("state = %q blocked %v, want blocked at once", obs.MergeQueueState, obs.MergeQueueBlockedAt)
	}
	if !strings.Contains(obs.MergeQueueNote, "checks e2e, nightly that grain does not run here") {
		t.Fatalf("note = %q, want the checks named", obs.MergeQueueNote)
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 || !strings.Contains(bodies[0], "remove them from the branch's ruleset") {
		t.Fatalf("want one comment with the way out, got %q", bodies)
	}

	calls := mergeCalls(sim)
	for _, at := range []time.Duration{time.Minute, 10 * time.Minute, time.Hour} {
		if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(at)); err != nil {
			t.Fatalf("SyncPullRequests: %v", err)
		}
	}
	if got := mergeCalls(sim); got != calls {
		t.Fatalf("a required-checks refusal was retried %d times", got-calls)
	}
	if len(commentBodies(t, ctx, store, task.ID)) != 1 {
		t.Fatal("the block was announced more than once")
	}

	// GitHub reports the checks passing and stops refusing: merged.
	sim.CheckRuns[branch] = append(greenChecks(),
		github.CheckRun{Name: "nightly", Status: "completed", Conclusion: strPtr("success")},
		github.CheckRun{Name: "e2e", Status: "completed", Conclusion: strPtr("success")})
	sim.RefuseMerges = ""
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(2*time.Hour)); err != nil {
		t.Fatalf("SyncPullRequests once the checks passed: %v", err)
	}
	if !mergedHead(sim, branch) {
		t.Fatal("the pull request did not merge once GitHub's required checks passed")
	}
}

// Any other rule is named and handed to a person once, and not retried
// until the person says so.
func TestAGitHubRuleRefusalBlocksOnceWithTheRuleNamed(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "signed", "main")
	repo := model.RepoRef{Owner: "acme", Name: "signed"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	_, behindBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = greenChecks()
	sim.RefuseMerges = "Repository rule violations found\n\nCommits must have verified signatures."
	sim.BranchRules = map[string][]map[string]any{"main": {{"type": "required_signatures"}}}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err == nil {
		t.Fatal("expected the refused merge to be reported")
	}
	obs := mergeQueueStatus(t, ctx, store, task.ID)
	if obs.MergeQueueBlockedAt == nil {
		t.Fatal("a rule refusal was not handed to a person")
	}
	if !strings.Contains(obs.MergeQueueNote, "required_signatures") || !strings.Contains(obs.MergeQueueNote, "verified signatures") {
		t.Fatalf("note = %q, want the rule named", obs.MergeQueueNote)
	}

	// The change behind it is not held up, and the refused one is not
	// asked about again: with the rule lifted, only a retry could merge
	// it, and none comes until a person asks for one.
	sim.CheckRuns[behindBranch] = greenChecks()
	sim.RefuseMerges = ""
	for _, at := range []time.Duration{time.Minute, time.Hour} {
		if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(at)); err != nil {
			t.Fatalf("SyncPullRequests: %v", err)
		}
	}
	if !mergedHead(sim, behindBranch) {
		t.Fatal("the change behind a rule refusal did not merge")
	}
	if mergedHead(sim, branch) {
		t.Fatal("a rule refusal was retried without anybody asking")
	}
	if bodies := commentBodies(t, ctx, store, task.ID); len(bodies) != 1 || !strings.Contains(bodies[0], "Retry merge") {
		t.Fatalf("want one comment with the way out, got %q", bodies)
	}
}

// A refusal nothing can place is retried a bounded number of times with
// backoff, then handed to a person with GitHub's words -- never counted
// up to thirty-five.
func TestAnUnknownMergeRefusalIsRetriedABoundedNumberOfTimes(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "odd", "main")
	repo := model.RepoRef{Owner: "acme", Name: "odd"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = greenChecks()
	sim.RefuseMerges = "Something went sideways."

	for i := 0; i < 60; i++ {
		orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Duration(i)*20*time.Second))
	}
	if got := mergeCalls(sim); got != 3 {
		t.Fatalf("an unplaceable refusal was asked %d times over 20 minutes, want 3", got)
	}
	obs := mergeQueueStatus(t, ctx, store, task.ID)
	if obs.MergeQueueBlockedAt == nil || !strings.Contains(obs.MergeQueueNote, "3 times") ||
		!strings.Contains(obs.MergeQueueNote, "Something went sideways") {
		t.Fatalf("state %q (%q) blocked %v, want the raw reason handed to a person after 3 tries",
			obs.MergeQueueState, obs.MergeQueueNote, obs.MergeQueueBlockedAt)
	}
	if len(commentBodies(t, ctx, store, task.ID)) != 1 {
		t.Fatal("want exactly one comment when the queue gives up")
	}

	// A person pressing Retry merge is a fresh start.
	sim.RefuseMerges = ""
	obs.MergeQueueBlockedAt = nil
	if err := store.Observe(ctx, obs); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatalf("SyncPullRequests after Retry merge: %v", err)
	}
	if !mergedHead(sim, branch) {
		t.Fatal("Retry merge did not merge a pull request GitHub had stopped refusing")
	}
}

func TestClassifyMergeRefusal(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   github.RefusalKind
	}{
		{405, `{"message":"At least 1 approving review is required by reviewers with write access."}`, github.RefusalApproval},
		{405, `{"message":"Waiting on code owner review from acme/platform."}`, github.RefusalApproval},
		{405, `{"message":"Repository rule violations found\n\nReview required."}`, github.RefusalApproval},
		{405, `{"message":"Repository rule violations found\n\n4 of 4 required status checks are failing."}`, github.RefusalRequiredChecks},
		{405, `{"message":"Required status check \"deploy\" is expected."}`, github.RefusalRequiredChecks},
		{405, `{"message":"Repository rule violations found\n\nThis branch must not contain merge commits."}`, github.RefusalRule},
		{405, `{"message":"Merge commits are not allowed on this repository."}`, github.RefusalRule},
		{403, `{"message":"Resource not accessible by integration"}`, github.RefusalRule},
		{409, `{"message":"Head branch was modified. Review and try the merge again."}`, github.RefusalTransient},
		{405, `{"message":"Base branch was modified. Review and try the merge again."}`, github.RefusalTransient},
		{502, `bad gateway`, github.RefusalTransient},
		{405, `{"message":"Something went sideways."}`, github.RefusalTransient},
		{405, `{"message":"Repository rule violations found\n\nAll comments must be resolved."}`, github.RefusalUnresolvedThreads},
		// The words GitHub refused task 3016's pull request with four times:
		// they name a rule violation, and are still a conversation to resolve.
		{405, `{"message":"Repository rule violations found\n\nA conversation must be resolved before this pull request can be merged."}`, github.RefusalUnresolvedThreads},
	} {
		got := github.ClassifyMergeRefusal(&github.Error{Status: tc.status, Body: []byte(tc.body)})
		if got != tc.want {
			t.Errorf("%d %s: got %q, want %q", tc.status, tc.body, got, tc.want)
		}
	}
}

// conversationRefusal is GitHub's ruleset wording for a merge refused over
// unresolved review conversations, as it refused task 3016's.
const conversationRefusal = "Repository rule violations found\n\n" +
	"A conversation must be resolved before this pull request can be merged."

// unresolvedConversationQueue is a clean, green pull request on a branch
// requiring conversation resolution, with two unresolved threads on it --
// one of them outdated -- and the task that owns it.
func unresolvedConversationQueue(t *testing.T, repoName string) (*githubsim.Sim, *github.RESTClient, *model.Store, model.Task, string, func(time.Time) error) {
	t.Helper()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", repoName, "main")
	repo := model.RepoRef{Owner: "acme", Name: repoName}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	setMergeable(sim, true)
	sim.CheckRuns[branch] = greenChecks()
	sim.RequireConversationResolution = true
	sim.ConversationRefusal = conversationRefusal
	line := 14
	number := prNumber(t, sim, branch)
	sim.ReviewComments = map[int][]github.ReviewComment{number: {
		{ID: 234726, User: "code-review[bot]", Body: "This runs in a subshell, so the exit is lost.", Path: "scripts/run.sh"},
		{ID: 234727, User: "code-review[bot]", Body: "Quote this expansion.", Path: "scripts/run.sh", Line: &line},
	}}
	sync := func(at time.Time) error { return orchestrator.SyncPullRequests(ctx, store, client, at) }
	return sim, client, store, task, branch, sync
}

// A merge GitHub refuses because a conversation must be resolved is not a
// rule for a person to change: the task is sent back with every open
// thread listed (thread ID, comment ID, path:line, body), and once its run
// has resolved them the queue merges on its own -- no Retry merge, no
// redispatch by hand.
func TestAMergeRefusedForAnUnresolvedConversationSendsTheTaskBackWithTheThreads(t *testing.T) {
	sim, _, store, task, branch, sync := unresolvedConversationQueue(t, "convo")
	ctx := t.Context()

	if err := sync(baseTime); err == nil {
		t.Fatal("expected the refused merge to be reported")
	}
	obs := mergeQueueStatus(t, ctx, store, task.ID)
	if obs.MergeQueueBlockedAt != nil {
		t.Fatalf("an unresolved conversation was handed to a person: %q", obs.MergeQueueNote)
	}
	if s := stateOf(t, ctx, store, task.ID); s != model.StateQueued {
		t.Fatalf("state = %q, want %q -- the task goes back to deal with the threads", s, model.StateQueued)
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 {
		t.Fatalf("want one comment sending the task back, got %q", bodies)
	}
	note := bodies[0]
	for _, want := range []string{
		"2 threads are still open",
		"thread `thread-234726`, comment 234726 by code-review[bot] on `scripts/run.sh (outdated)` -- This runs in a subshell",
		"thread `thread-234727`, comment 234727 by code-review[bot] on `scripts/run.sh:14` -- Quote this expansion.",
		"grain pr resolve --thread-id",
		"A conversation must be resolved",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("the send-back comment lacks %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, "Nothing in the branch is wrong") {
		t.Errorf("the refusal was still described as a rule:\n%s", note)
	}
	calls := mergeCalls(sim)

	// While the threads are open the merge is not asked for again.
	if err := sync(baseTime.Add(5 * time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if got := mergeCalls(sim); got != calls {
		t.Fatalf("merge asked for %d more time(s) while threads were still open", got-calls)
	}

	// The run resolves both and finishes without pushing; the queue merges.
	sim.ResolvedThreads["thread-234726"] = true
	sim.ResolvedThreads["thread-234727"] = true
	done := baseTime.Add(20 * time.Minute)
	if err := store.ObserveField(ctx, task.ID, done, func(o *model.Observation) { o.CompletedAt = &done }); err != nil {
		t.Fatal(err)
	}
	if err := sync(done.Add(5 * time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if !mergedHead(sim, branch) {
		t.Fatal("the pull request did not merge once its threads were resolved")
	}
}

// A run sent back for unresolved threads that finishes with some still
// open is not sent back again: the next refusal is handed to a person.
func TestAnUnresolvedConversationTheRunLeftOpenIsHandedToAPerson(t *testing.T) {
	sim, _, store, task, branch, sync := unresolvedConversationQueue(t, "convo-left")
	ctx := t.Context()

	if err := sync(baseTime); err == nil {
		t.Fatal("expected the refused merge to be reported")
	}
	if s := stateOf(t, ctx, store, task.ID); s != model.StateQueued {
		t.Fatalf("state = %q, want %q", s, model.StateQueued)
	}
	sim.ResolvedThreads["thread-234727"] = true
	done := baseTime.Add(20 * time.Minute)
	if err := store.ObserveField(ctx, task.ID, done, func(o *model.Observation) { o.CompletedAt = &done }); err != nil {
		t.Fatal(err)
	}
	if err := sync(done.Add(5 * time.Minute)); err == nil {
		t.Fatal("expected the second refusal to be reported")
	}
	obs := mergeQueueStatus(t, ctx, store, task.ID)
	if obs.MergeQueueBlockedAt == nil {
		t.Fatal("a thread the sent-back run left open was not handed to a person")
	}
	if s := stateOf(t, ctx, store, task.ID); s == model.StateQueued {
		t.Fatal("the task was sent back a second time")
	}
	if mergedHead(sim, branch) {
		t.Fatal("merged with a thread unresolved")
	}
	if !strings.Contains(obs.MergeQueueNote, "1 unresolved review conversations by code-review[bot]") {
		t.Errorf("note = %q, want the open thread counted", obs.MergeQueueNote)
	}
}

// withDraftFinding adds a thread from grain's own draft review (a review
// task's question for the owner) to the pull request unresolvedConversationQueue
// built, and links that review to the task.
func withDraftFinding(t *testing.T, sim *githubsim.Sim, store *model.Store, task model.Task, branch string) {
	t.Helper()
	line := 20
	number := prNumber(t, sim, branch)
	sim.ReviewComments[number] = append(sim.ReviewComments[number],
		github.ReviewComment{ID: 234728, User: "grain", Body: "owner question: keep this?", Path: "scripts/run.sh", Line: &line, ReviewID: 77})
	if err := store.UpdateTask(t.Context(), task.ID, func(tk *model.Task) error {
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkDraftReview, Target: "77"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A thread from grain's own draft review is the owner's question, not
// something the sent-back run is told to fix and resolve.
func TestAnUnresolvedConversationSendBackLeavesOutGrainsDraftReview(t *testing.T) {
	sim, _, store, task, branch, sync := unresolvedConversationQueue(t, "convo-draft")
	withDraftFinding(t, sim, store, task, branch)
	ctx := t.Context()

	if err := sync(baseTime); err == nil {
		t.Fatal("expected the refused merge to be reported")
	}
	if s := stateOf(t, ctx, store, task.ID); s != model.StateQueued {
		t.Fatalf("state = %q, want %q", s, model.StateQueued)
	}
	for _, body := range commentBodies(t, ctx, store, task.ID) {
		if strings.Contains(body, "owner question") || strings.Contains(body, "thread-234728") {
			t.Errorf("grain's own draft review was sent to the run to resolve:\n%s", body)
		}
		if strings.Contains(body, "2 threads are still open") {
			return
		}
	}
	t.Error("the send-back comment did not list the two other threads")
}

// failingReviewComments is a client whose REST review-comment read fails
// while its thread view still answers.
type failingReviewComments struct{ *github.RESTClient }

func (failingReviewComments) ListReviewComments(string, string, int) ([]github.ReviewComment, error) {
	return nil, errors.New("github: 502 bad gateway")
}

// Where the review comments cannot be read, nothing tells grain's own
// draft-review threads from anybody else's: the queue sends nothing back
// and hands the refusal to a person, rather than telling a run to resolve
// threads that may be the owner's questions.
func TestAnUnresolvedConversationIsNotSentBackBlindWhenCommentsCannotBeRead(t *testing.T) {
	sim, client, store, task, branch, _ := unresolvedConversationQueue(t, "convo-blind")
	withDraftFinding(t, sim, store, task, branch)
	ctx := t.Context()

	if err := orchestrator.SyncPullRequests(ctx, store, failingReviewComments{client}, baseTime); err == nil {
		t.Fatal("expected the refused merge to be reported")
	}
	if s := stateOf(t, ctx, store, task.ID); s == model.StateQueued {
		t.Fatalf("the task was sent back with threads it could not tell apart: %q", commentBodies(t, ctx, store, task.ID))
	}
	if obs := mergeQueueStatus(t, ctx, store, task.ID); obs.MergeQueueBlockedAt == nil {
		t.Fatalf("the refusal was not handed to a person: %q", obs.MergeQueueNote)
	}
	if mergedHead(sim, branch) {
		t.Fatal("merged with threads unresolved")
	}
}
