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

package daemon

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A review that posts findings on the pull request it reviews, and has no
// pull request of its own (a path trigger's pr-review), reads that pull
// request's threads with `grain pr threads` -- where the parallel review of
// the same head posted what it is told not to repeat. Before, it was told it
// had no pull request (docs/test-runs/2026-11-14-review-context.md).
func TestGrainPRThreadsInACommentsOnlyReviewListsTheReviewedPullRequest(t *testing.T) {
	ctx := context.Background()
	r := newDiffTestRepo(t)
	r.commit(t, "grain/task-2", "pkg/gitproxy/proxy.go", "package gitproxy\n")
	client := github.NewClient(r.sim, nil)
	pr, err := client.CreatePullRequest("acme", "widgets", "grain/task-2", "main", "Change", "")
	if err != nil {
		t.Fatal(err)
	}
	line := 1
	r.sim.ReviewComments = map[int][]github.ReviewComment{pr.Number: {{
		ID: 701, User: "grain-agent", Path: "pkg/gitproxy/proxy.go", Line: &line, Body: "Q1: reject empty owners?",
	}}}

	store, closeStore := openTestStore(t, t.TempDir())
	t.Cleanup(closeStore)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	ref := model.PullRequestRef{Repo: repo, Number: pr.Number}
	for _, task := range []model.Task{
		{ID: "4", Title: "Review acme/widgets", Target: &repo,
			Links: []model.Link{{Kind: model.LinkReviewsPullRequest, Target: ref.String()}}},
		{ID: "5", Title: "Unrelated", Target: &repo},
	} {
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	opener := &PullRequestOpener{Store: store, Client: client}

	text, refused, err := opener.ThreadsForTask(ctx, "4", false)
	if err != nil || refused {
		t.Fatalf("ThreadsForTask(review) = %q, refused=%v, %v", text, refused, err)
	}
	for _, want := range []string{fmt.Sprintf("Review threads on #%d", pr.Number), "Q1: reject empty owners?"} {
		if !strings.Contains(text, want) {
			t.Errorf("ThreadsForTask(review) = %q, want %q in it", text, want)
		}
	}

	// A task that names no reviewed pull request still has none.
	if text, refused, err := opener.ThreadsForTask(ctx, "5", false); err != nil || !refused ||
		!strings.Contains(text, "No pull request is open") {
		t.Errorf("ThreadsForTask(unrelated) = %q, refused=%v, %v; want the no-pull-request refusal", text, refused, err)
	}
}

// In a fork-to-upstream deployment (RepoConfig.UpstreamRepo), the reviewed
// pull request lives on the upstream repository, so LinkReviewsPullRequest
// names the upstream rather than task.Target. ThreadsForTask reads that
// upstream pull request's threads, refuses a same-target fork link rather
// than looking its number up against the upstream repository, and keeps
// ReplyForTask and ResolveForTask scoped to the task's own pull request.
func TestGrainPRThreadsInAForkToUpstreamCommentsOnlyReview(t *testing.T) {
	ctx := context.Background()
	upstream := newDiffTestRepo(t)
	fork := newDiffTestRepo(t)
	fork.sim.Owner = "acme-fork"
	upstream.sim.Forks = map[string]*githubsim.Sim{"acme-fork/widgets": fork.sim}

	fork.commit(t, "grain/task-2", "pkg/gitproxy/proxy.go", "package gitproxy\n")
	fork.commit(t, "grain/task-3", "pkg/gitproxy/other.go", "package gitproxy\n")

	client := github.NewClient(upstream.sim, nil)
	reviewedPR, err := client.CreatePullRequest("acme", "widgets", "acme-fork:grain/task-2", "main", "Reviewed change", "")
	if err != nil {
		t.Fatal(err)
	}
	otherPR, err := client.CreatePullRequest("acme", "widgets", "acme-fork:grain/task-3", "main", "Other change", "")
	if err != nil {
		t.Fatal(err)
	}
	line := 1
	upstream.sim.ReviewComments = map[int][]github.ReviewComment{
		reviewedPR.Number: {{
			ID: 701, User: "grain-agent", Path: "pkg/gitproxy/proxy.go", Line: &line, Body: "Q1: reject empty owners?",
		}},
		otherPR.Number: {{
			ID: 702, User: "grain-agent", Path: "pkg/gitproxy/other.go", Line: &line, Body: "unrelated upstream thread",
		}},
	}

	store, closeStore := openTestStore(t, t.TempDir())
	t.Cleanup(closeStore)
	forkRepo := model.RepoRef{Owner: "acme-fork", Name: "widgets"}
	upstreamRepo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:         forkRepo,
		UpstreamRepo: upstreamRepo.String(),
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = store.PutRepoConfig(ctx, model.RepoConfig{Repo: forkRepo})
	})

	upstreamRef := model.PullRequestRef{Repo: upstreamRepo, Number: reviewedPR.Number}
	wrongRepoRef := model.PullRequestRef{Repo: forkRepo, Number: otherPR.Number}
	for _, task := range []model.Task{
		{ID: "4", Title: "Review acme/widgets from fork", Target: &forkRepo,
			Links: []model.Link{{Kind: model.LinkReviewsPullRequest, Target: upstreamRef.String()}}},
		{ID: "5", Title: "Fork link with upstream configured", Target: &forkRepo,
			Links: []model.Link{{Kind: model.LinkReviewsPullRequest, Target: wrongRepoRef.String()}}},
	} {
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	opener := &PullRequestOpener{Store: store, Client: client}

	text, refused, err := opener.ThreadsForTask(ctx, "4", false)
	if err != nil || refused {
		t.Fatalf("ThreadsForTask(upstream review) = %q, refused=%v, %v", text, refused, err)
	}
	for _, want := range []string{fmt.Sprintf("Review threads on #%d", reviewedPR.Number), "Q1: reject empty owners?"} {
		if !strings.Contains(text, want) {
			t.Errorf("ThreadsForTask(upstream review) = %q, want %q in it", text, want)
		}
	}

	// Replying and resolving stay on the task's own pull request, not the reviewed one.
	if replyText, replyRefused, err := opener.ReplyForTask(ctx, "4", 701, "ack"); err != nil || !replyRefused ||
		!strings.Contains(replyText, "No pull request is open") {
		t.Errorf("ReplyForTask(upstream review) = %q, refused=%v, %v; want the no-pull-request refusal",
			replyText, replyRefused, err)
	}
	if resolveText, resolveRefused, err := opener.ResolveForTask(ctx, "4", "", 701); err != nil || !resolveRefused ||
		!strings.Contains(resolveText, "No pull request is open") {
		t.Errorf("ResolveForTask(upstream review) = %q, refused=%v, %v; want the no-pull-request refusal",
			resolveText, resolveRefused, err)
	}

	// When UpstreamRepo is set, scope reads pull requests from the upstream
	// repository, so a Same(target) fork link must not be read against upstream.
	wrongText, wrongRefused, err := opener.ThreadsForTask(ctx, "5", false)
	if err != nil || !wrongRefused || !strings.Contains(wrongText, "No pull request is open") {
		t.Errorf("ThreadsForTask(fork link) = %q, refused=%v, %v; want the no-pull-request refusal",
			wrongText, wrongRefused, err)
	}
	if strings.Contains(wrongText, "unrelated upstream thread") {
		t.Errorf("ThreadsForTask(fork link) read upstream pull request #%d: %q", otherPR.Number, wrongText)
	}
}
