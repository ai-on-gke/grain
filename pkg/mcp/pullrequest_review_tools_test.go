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

package mcp

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
)

// fakeCommenter is fakePullRequests that can also write: every reply,
// comment and resolution it is asked for is recorded, so a test can
// assert both what was posted and that nothing was posted at all.
type fakeCommenter struct {
	*fakePullRequests

	threads    []github.ReviewThread
	threadsErr error

	created  []string // "#<number>: <body>"
	replies  []string // "#<number>/<comment>: <body>"
	resolved []string
	writeErr error
}

func (f *fakeCommenter) CreateComment(owner, repo string, number int, body string) (int, error) {
	f.note(owner, repo)
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	f.created = append(f.created, "#"+strconv.Itoa(number)+": "+body)
	return 501, nil
}

func (f *fakeCommenter) ReplyToReviewComment(owner, repo string, pullNumber, commentID int, body string) (int, error) {
	f.note(owner, repo)
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	f.replies = append(f.replies, "#"+strconv.Itoa(pullNumber)+"/"+strconv.Itoa(commentID)+": "+body)
	return 502, nil
}

func (f *fakeCommenter) ListReviewThreads(owner, repo string, number int) ([]github.ReviewThread, error) {
	f.note(owner, repo)
	return f.threads, f.threadsErr
}

func (f *fakeCommenter) ResolveReviewThread(owner, repo, threadID string) error {
	f.note(owner, repo)
	if f.writeErr != nil {
		return f.writeErr
	}
	f.resolved = append(f.resolved, threadID)
	return nil
}

func intPtr(n int) *int { return &n }

// reviewedPullRequest is an open pull request for testScope's branch with
// a bot's review on it: one thread with a reply in it, one resolved, and
// one on a line the diff has since moved past.
func reviewedPullRequest() *fakeCommenter {
	success := "success"
	return &fakeCommenter{
		fakePullRequests: &fakePullRequests{
			head:   &github.BranchHead{SHA: "abcdef1234567", Message: "do the thing"},
			pr:     &github.PullRequest{Number: 42, HTMLURL: "https://github.example/acme/widgets/pull/42"},
			detail: github.PullRequestDetail{State: "open", Mergeable: boolPtr(true), BaseRef: "main"},
			checks: []github.CheckRun{{Name: "unit", Status: "completed", Conclusion: &success}},
			reviewComments: []github.ReviewComment{
				{ID: 11, User: "gemini-code-assist", Body: "Prefer errors.Is here.\nIt survives wrapping.", Path: "pkg/a.go", Line: intPtr(7)},
				{ID: 12, User: "gemini-code-assist", Body: "Typo in the comment.", Path: "pkg/b.go", Line: intPtr(3)},
				{ID: 13, User: "grain-agent", Body: "Switched to errors.Is.", Path: "pkg/a.go", Line: intPtr(7), InReplyTo: 11},
				{ID: 14, User: "alice", Body: "Why this file?", Path: "pkg/c.go"},
			},
		},
		threads: []github.ReviewThread{
			{ID: "PRRT_a", CommentIDs: []int{11, 13}},
			{ID: "PRRT_b", Resolved: true, CommentIDs: []int{12}},
			{ID: "PRRT_c", Outdated: true, CommentIDs: []int{14}},
		},
	}
}

func TestPullRequestStatusListsInlineReviewCommentsByThread(t *testing.T) {
	client := reviewedPullRequest()
	res := call(t, client, testScope)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Text)
	}
	for _, want := range []string{
		"Inline review comments (3 thread(s), 2 unresolved):",
		"  comment 11 by @gemini-code-assist on pkg/a.go:7, thread PRRT_a, unresolved\n" +
			"    Prefer errors.Is here.\n    It survives wrapping.\n" +
			"    reply 13 by @grain-agent:\n      Switched to errors.Is.\n",
		// A resolved thread is one line: there is nothing left to do.
		"  comment 12 by @gemini-code-assist on pkg/b.go:3, thread PRRT_b, resolved\n    \"Typo in the comment.\"\n",
		"  comment 14 by @alice on pkg/c.go (outdated), thread PRRT_c, unresolved\n    Why this file?\n",
		"reply_to_pull_request_comment",
		"resolve_review_thread",
	} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("answer is missing %q; got:\n%s", want, res.Text)
		}
	}
	// Before the checks, right under the pull request it belongs to.
	if strings.Index(res.Text, "Inline review comments") > strings.Index(res.Text, "Checks against") {
		t.Errorf("review comments come after the checks; got:\n%s", res.Text)
	}
}

func TestPullRequestStatusIsUnchangedWithoutReviewComments(t *testing.T) {
	plain := reviewedPullRequest()
	plain.reviewComments = nil
	reviewed := reviewedPullRequest()
	reviewed.reviewComments = nil
	reviewed.threads = nil
	before := call(t, plain.fakePullRequests, testScope).Text
	after := call(t, reviewed, testScope).Text
	if before != after || strings.Contains(after, "review") {
		t.Errorf("a pull request with no review comments reads differently:\n%s\n---\n%s", before, after)
	}
}

func TestPullRequestStatusListsReviewCommentsWithoutThreads(t *testing.T) {
	// A reader that cannot see threads (no GraphQL) still lists the
	// comments, with no resolution state rather than a guessed one.
	client := reviewedPullRequest()
	client.threadsErr = errors.New("graphql unavailable")
	res := call(t, client, testScope)
	if !strings.Contains(res.Text, "Inline review comments (3 thread(s)):") ||
		!strings.Contains(res.Text, "comment 11 by @gemini-code-assist on pkg/a.go:7\n") {
		t.Errorf("got:\n%s", res.Text)
	}
	res = call(t, client.fakePullRequests, testScope)
	if !strings.Contains(res.Text, "comment 12 by @gemini-code-assist on pkg/b.go:3\n    Typo in the comment.") {
		t.Errorf("got:\n%s", res.Text)
	}
}

func TestPullRequestStatusSaysWhenReviewCommentsAreUnreadable(t *testing.T) {
	client := reviewedPullRequest()
	client.reviewCommentsErr = &github.Error{Status: 403, Body: []byte(`{"message":"Resource not accessible"}`)}
	res := call(t, client, testScope)
	if res.IsError || !strings.Contains(res.Text, "Inline review comments could not be read: ") ||
		!strings.Contains(res.Text, "Resource not accessible") {
		t.Errorf("got:\n%s", res.Text)
	}
}

func reviewTool(t *testing.T, client PullRequestReader, name string, args map[string]any) Result {
	t.Helper()
	return namedTool(t, NewPullRequestTools(client, nil, testScope), name).Handler(context.Background(), args)
}

func TestReplyToPullRequestCommentRepliesInTheThread(t *testing.T) {
	client := reviewedPullRequest()
	res := reviewTool(t, client, "reply_to_pull_request_comment", map[string]any{"comment_id": float64(11), "body": "Done."})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Text)
	}
	if len(client.replies) != 1 || client.replies[0] != "#42/11: Done." || len(client.created) != 0 {
		t.Errorf("replies = %v, created = %v", client.replies, client.created)
	}
	if !strings.Contains(res.Text, "pull request #42") {
		t.Errorf("got %q", res.Text)
	}
	for _, repo := range client.repos {
		if repo != "acme/widgets" {
			t.Errorf("a call left the run's own repository: %s", repo)
		}
	}
}

func TestReplyToPullRequestCommentAcceptsAnIDAsDigits(t *testing.T) {
	client := reviewedPullRequest()
	res := reviewTool(t, client, "reply_to_pull_request_comment", map[string]any{"comment_id": "13", "body": "Also."})
	if res.IsError || len(client.replies) != 1 || client.replies[0] != "#42/13: Also." {
		t.Errorf("res = %+v, replies = %v", res, client.replies)
	}
}

func TestReplyToPullRequestCommentWithoutACommentIsTopLevel(t *testing.T) {
	client := reviewedPullRequest()
	res := reviewTool(t, client, "reply_to_pull_request_comment", map[string]any{"body": "All addressed."})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Text)
	}
	if len(client.created) != 1 || client.created[0] != "#42: All addressed." || len(client.replies) != 0 {
		t.Errorf("created = %v, replies = %v", client.created, client.replies)
	}
}

func TestReplyToPullRequestCommentRefusesACommentNotOnItsPullRequest(t *testing.T) {
	client := reviewedPullRequest()
	res := reviewTool(t, client, "reply_to_pull_request_comment", map[string]any{"comment_id": 999, "body": "Hi."})
	if !res.IsError || !strings.Contains(res.Text, "not an inline review comment on pull request #42") {
		t.Errorf("got %+v", res)
	}
	if len(client.replies) != 0 || len(client.created) != 0 {
		t.Errorf("something was posted: replies = %v, created = %v", client.replies, client.created)
	}
}

func TestReplyToPullRequestCommentRefusesBadArguments(t *testing.T) {
	for name, args := range map[string]map[string]any{
		"no body":        {"comment_id": 11},
		"blank body":     {"body": "  "},
		"bad comment id": {"body": "x", "comment_id": "eleven"},
		"zero id":        {"body": "x", "comment_id": 0},
	} {
		t.Run(name, func(t *testing.T) {
			client := reviewedPullRequest()
			res := reviewTool(t, client, "reply_to_pull_request_comment", args)
			if !res.IsError {
				t.Errorf("accepted %v: %q", args, res.Text)
			}
			if len(client.replies) != 0 || len(client.created) != 0 {
				t.Errorf("something was posted for %v", args)
			}
		})
	}
}

func TestReviewToolsRefuseWithoutAnOpenPullRequest(t *testing.T) {
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"reply_to_pull_request_comment", map[string]any{"body": "x"}},
		{"resolve_review_thread", map[string]any{"comment_id": 11}},
	} {
		client := reviewedPullRequest()
		client.pr = nil
		res := reviewTool(t, client, tc.tool, tc.args)
		if !res.IsError || !strings.Contains(res.Text, "No pull request is open for grain/task-9") {
			t.Errorf("%s: got %+v", tc.tool, res)
		}
	}
}

func TestReviewToolsRefuseWithoutAWriter(t *testing.T) {
	// A reader that cannot write -- or no client at all -- registers both
	// tools anyway, and each says why it cannot act.
	for _, client := range []PullRequestReader{nil, reviewedPullRequest().fakePullRequests} {
		for _, name := range []string{"reply_to_pull_request_comment", "resolve_review_thread"} {
			res := reviewTool(t, client, name, map[string]any{"body": "x", "comment_id": 11})
			if !res.IsError || res.Text != noPullRequestCommenter {
				t.Errorf("%s with %T: got %+v", name, client, res)
			}
		}
	}
}

func TestResolveReviewThreadByCommentOrThread(t *testing.T) {
	for name, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"by the comment that opened it": {map[string]any{"comment_id": 11}, "PRRT_a"},
		"by a reply in it":              {map[string]any{"comment_id": "13"}, "PRRT_a"},
		"by thread":                     {map[string]any{"thread_id": "PRRT_c"}, "PRRT_c"},
		"by both, agreeing":             {map[string]any{"thread_id": "PRRT_a", "comment_id": 13}, "PRRT_a"},
	} {
		t.Run(name, func(t *testing.T) {
			client := reviewedPullRequest()
			res := reviewTool(t, client, "resolve_review_thread", tc.args)
			if res.IsError {
				t.Fatalf("unexpected error: %s", res.Text)
			}
			if len(client.resolved) != 1 || client.resolved[0] != tc.want {
				t.Errorf("resolved = %v, want [%s]", client.resolved, tc.want)
			}
		})
	}
}

func TestResolveReviewThreadRefusesAThreadNotOnItsPullRequest(t *testing.T) {
	for name, args := range map[string]map[string]any{
		"unknown thread":      {"thread_id": "PRRT_elsewhere"},
		"unknown comment":     {"comment_id": 999},
		"disagreeing pair":    {"thread_id": "PRRT_c", "comment_id": 11},
		"neither named":       {},
		"blank thread, no id": {"thread_id": " "},
	} {
		t.Run(name, func(t *testing.T) {
			client := reviewedPullRequest()
			res := reviewTool(t, client, "resolve_review_thread", args)
			if !res.IsError {
				t.Errorf("accepted %v: %q", args, res.Text)
			}
			if len(client.resolved) != 0 {
				t.Errorf("resolved %v for %v", client.resolved, args)
			}
		})
	}
}

func TestResolveReviewThreadLeavesAResolvedThreadAlone(t *testing.T) {
	client := reviewedPullRequest()
	res := reviewTool(t, client, "resolve_review_thread", map[string]any{"comment_id": 12})
	if res.IsError || !strings.Contains(res.Text, "already resolved") || len(client.resolved) != 0 {
		t.Errorf("res = %+v, resolved = %v", res, client.resolved)
	}
}

func TestResolveReviewThreadReportsGitHubsRefusal(t *testing.T) {
	client := reviewedPullRequest()
	client.writeErr = &github.Error{Status: 403, Body: []byte(`{"message":"Resource not accessible by integration"}`)}
	res := reviewTool(t, client, "resolve_review_thread", map[string]any{"thread_id": "PRRT_a"})
	if !res.IsError || !strings.Contains(res.Text, "Resource not accessible by integration") ||
		strings.Contains(res.Text, `\"`) {
		t.Errorf("got %+v", res)
	}
}
