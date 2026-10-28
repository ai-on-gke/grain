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

// This file is two more of bwsalmon/agents#337's ten: both are
// ProcessResult/SyncPullRequests endings orchestrator's own unit tests
// prove in isolation, but that nothing in this package had ever driven
// through a real dispatch, a real githubsim and a real bare repo before.
//
//   - TestAgentCompletesByCommentingWithNoPushOpensNoPullRequest is
//     ProcessResult's third ending (finish.go): no push, no question, just
//     a comment_on_issue call -- "the run produced nothing to act on
//     [besides that]" is a completion, not a failure, and it must open no
//     pull request at all. Every other test in this package reaches a
//     post-run state through a real push; this is the one path that never
//     touches git, which is why it is also the one that ends in
//     model.StateAwaitingAck -- the state for a run whose whole product is
//     words somebody has to read.
//   - TestSyncPullRequestsClosesATaskWhosePullRequestWasClosedWithoutMerging
//     is healthFrom's own doc comment made concrete: a pull request GitHub
//     reports "closed" folds into the same PrClosed outcome whether or not
//     it was ever merged, so a task whose PR a human declined must close
//     out cleanly too -- with no MergePullRequest call, and nothing of the
//     agent's branch ever landing on main.
//
// Both reuse mergequeue_conflict_test.go's own worldSandboxes (a real
// gitproxy-fronted world's already-credentialed slots, adapted to the
// orchestrator.Sandboxes interface RunCycle wants) and cli_test.go's own
// scriptedFramework, rather than building either again.
package e2e

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// commentOnlyScript is a scripted turn that never touches git at all:
// one comment_on_issue call standing in for an agent that investigated
// and decided nothing needs changing, the mcp contract's own third
// escape hatch alongside ask_question and propose_task.
func commentOnlyScript(comment string) []antigravity.Step {
	return []antigravity.Step{
		toolCall("comment_on_issue", map[string]any{"comment": comment}),
		finalText("nothing to change: " + comment),
	}
}

func TestAgentCompletesByCommentingWithNoPushOpensNoPullRequest(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	const owner, repoName = "acme", "gadgets"
	w.newRepo(owner, repoName)
	repo := model.RepoRef{Owner: owner, Name: repoName}

	bare := filepath.Join(w.upstreamDir, owner, repoName+".git")
	sim := githubsim.New(owner, repoName, bare, "main")
	client := github.NewClient(sim, nil)

	const comment = "looked into it -- the behavior described is already correct, no change needed"
	deps := orchestrator.Deps{
		Store: w.store, Client: client, Sandboxes: worldSandboxes{w}, MaxWorkers: 1,
		Framework: scriptedFramework(commentOnlyScript(comment)),
	}

	fileIssue(w, "t-comment-only", human("erin"), repo)
	assertState(w, "t-comment-only", model.StateQueued, false)

	clock := baseTime
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	// 'awaiting_ack', not 'completed': this ending is the whole of what
	// model.StateAwaitingAck names -- a run that answered on the task and
	// opened nothing, waiting on somebody to read it and close it. The
	// task is finished either way (Observation.CompletedAt is set), and
	// the state is what says nothing is going to land.
	assertState(w, "t-comment-only", model.StateAwaitingReply, false)

	if len(sim.PullRequests) != 0 {
		t.Fatalf("expected no pull request from a comment-only completion, got %+v", sim.PullRequests)
	}
	if w.branchExists(owner, repoName, model.BranchName("t-comment-only")) {
		t.Fatal("expected no branch to have been pushed at all")
	}

	comments, err := w.store.Comments(w.ctx, "t-comment-only")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range comments {
		if !strings.Contains(c.Body, comment) {
			continue
		}
		found = true
		if c.Author.Actor.Kind != model.PrincipalAutomation || c.Author.Actor.ID != "grain" {
			t.Fatalf("relayed comment author = %+v, want automation:grain", c.Author.Actor)
		}
		if c.Author.OnBehalfOf == nil || c.Author.OnBehalfOf.Kind != model.PrincipalAgent {
			t.Fatalf("relayed comment OnBehalfOf = %+v, want the agent principal", c.Author.OnBehalfOf)
		}
	}
	if !found {
		t.Fatalf("expected the agent's own comment among %+v", comments)
	}

	// A later cycle finds nothing new to do: no pull request exists to
	// sync, and a completed task is not ready for another dispatch.
	if err := orchestrator.RunCycle(w.ctx, deps, clock.Add(time.Minute)); err != nil {
		t.Fatalf("RunCycle (second, idempotent): %v", err)
	}
	assertState(w, "t-comment-only", model.StateAwaitingReply, false)
}

func TestSyncPullRequestsClosesATaskWhosePullRequestWasClosedWithoutMerging(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	const owner, repoName = "acme", "gizmos"
	w.newRepo(owner, repoName)
	repo := model.RepoRef{Owner: owner, Name: repoName}

	bare := filepath.Join(w.upstreamDir, owner, repoName+".git")
	sim := githubsim.New(owner, repoName, bare, "main")
	client := github.NewClient(sim, nil)

	branch := model.BranchName("t-declined")
	deps := orchestrator.Deps{
		Store: w.store, Client: client, Sandboxes: worldSandboxes{w}, MaxWorkers: 1,
		Framework: scriptedFramework(pushScript(w.remote(owner, repoName), branch, "t-declined")),
	}

	fileIssue(w, "t-declined", human("frank"), repo)
	clock := baseTime
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (push): %v", err)
	}
	assertState(w, "t-declined", model.StateAwaitingSubmit, false)
	if len(sim.PullRequests) != 1 {
		t.Fatalf("expected exactly one pull request, got %+v", sim.PullRequests)
	}

	// GitHub -- played by the test, the same way every other file in this
	// package stands in for it -- declines the pull request: closed, not
	// merged, the way clicking "Close pull request" (not "Merge pull
	// request") leaves it. Nothing here ever calls MergePullRequest.
	for i := range sim.PullRequests {
		sim.PullRequests[i].State = "closed"
	}

	clock = clock.Add(time.Minute)
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (sync notices it closed): %v", err)
	}
	assertState(w, "t-declined", model.StateClosed, false)

	if w.branchContains(owner, repoName, "main", branch) {
		t.Fatal("main must never have absorbed a pull request that was closed without merging")
	}

	// And it stays closed -- this is a real close-out, not a state a
	// later cycle un-does or tries to merge after the fact.
	clock = clock.Add(time.Minute)
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (later tick): %v", err)
	}
	assertState(w, "t-declined", model.StateClosed, false)
}
