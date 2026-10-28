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
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
)

func TestExternalPullRequestScopeAllowsStatusReplyAndResolveWithoutOwnBranchPR(t *testing.T) {
	success := "success"
	client := &fakeCommenter{
		fakePullRequests: &fakePullRequests{
			head: nil, // Task's own branch has not been pushed at all.
			pr:   nil, // No PR open for grain/task-77.
			detail: github.PullRequestDetail{
				Number:    88,
				Title:     "External human PR",
				HTMLURL:   "https://github.example/acme/widgets/pull/88",
				HeadRef:   "feat/human",
				HeadSHA:   "9988776655443",
				BaseRef:   "main",
				State:     "open",
				Mergeable: boolPtr(true),
			},
			checks: []github.CheckRun{{Name: "test", Status: "completed", Conclusion: &success}},
			reviewComments: []github.ReviewComment{
				{ID: 301, User: "bob", Body: "Check nil slice here.", Path: "pkg/s.go", Line: intPtr(19)},
			},
		},
		threads: []github.ReviewThread{
			{ID: "PRRT_ext", CommentIDs: []int{301}},
		},
	}

	scope := PullRequestScope{
		Owner:             "acme",
		Repo:              "widgets",
		Branch:            "grain/task-77",
		Base:              "feat/human",
		PullRequestNumber: 88,
	}

	tools := NewPullRequestTools(client, nil, scope)

	statusRes := namedTool(t, tools, "pull_request_status").Handler(context.Background(), nil)
	if statusRes.IsError {
		t.Fatalf("pull_request_status error: %s", statusRes.Text)
	}
	for _, want := range []string{
		"Pull request #88 (https://github.example/acme/widgets/pull/88) is open",
		"comment 301 by @bob on pkg/s.go:19",
		"Checks against 9988776:",
	} {
		if !strings.Contains(statusRes.Text, want) {
			t.Errorf("pull_request_status missing %q; got:\n%s", want, statusRes.Text)
		}
	}

	// Reply to an inline comment on #88 succeeds; foreign comment ID is refused.
	foreignReply := namedTool(t, tools, "reply_to_pull_request_comment").Handler(context.Background(), map[string]any{
		"comment_id": 999,
		"body":       "Trying foreign comment",
	})
	if !foreignReply.IsError {
		t.Fatalf("expected foreign comment_id to be refused, got: %s", foreignReply.Text)
	}

	okReply := namedTool(t, tools, "reply_to_pull_request_comment").Handler(context.Background(), map[string]any{
		"comment_id": 301,
		"body":       "Handled in companion commit.",
	})
	if okReply.IsError {
		t.Fatalf("reply_to_pull_request_comment failed: %s", okReply.Text)
	}
	if !slices.Contains(client.replies, "#88/301: Handled in companion commit.") {
		t.Errorf("replies = %v, want reply on #88/301", client.replies)
	}

	resolveRes := namedTool(t, tools, "resolve_review_thread").Handler(context.Background(), map[string]any{
		"comment_id": 301,
	})
	if resolveRes.IsError {
		t.Fatalf("resolve_review_thread failed: %s", resolveRes.Text)
	}
	if !slices.Contains(client.resolved, "PRRT_ext") {
		t.Errorf("resolved = %v, want PRRT_ext", client.resolved)
	}
}
