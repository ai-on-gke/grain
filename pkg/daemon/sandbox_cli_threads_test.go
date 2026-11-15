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
