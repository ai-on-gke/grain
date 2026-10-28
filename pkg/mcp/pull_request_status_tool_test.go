package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/github"
)

func strPtr(s string) *string {
	return &s
}

func TestPullRequestStatusAnnotatesRegression(t *testing.T) {
	client := &fakePullRequests{
		head: &github.BranchHead{SHA: "2222222333444", Message: "feat: something"},
		heads: map[string]*github.BranchHead{
			"grain/task-654": {SHA: "2222222333444", Message: "feat: something"},
			"main":           {SHA: "1111111222333", Message: "fix: base"},
		},
		defaultBranch: "main",
		checksByRef: map[string][]github.CheckRun{
			"2222222333444": {
				{
					Name:       "test-suite",
					Status:     "completed",
					Conclusion: strPtr("failure"),
				},
			},
			"1111111222333": {
				{
					Name:       "test-suite",
					Status:     "completed",
					Conclusion: strPtr("success"),
				},
			},
		},
		jobLogs: []github.JobLog{
			{
				Name: "test-suite",
				Log:  "FAIL: TestFeature (0.01s)\nexit 1",
			},
		},
	}

	scope := PullRequestScope{
		Owner:  "acme",
		Repo:   "widgets",
		Branch: "grain/task-654",
	}

	text, err := pullRequestStatus(context.Background(), client, nil, scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(text, "[REGRESSION]") {
		t.Errorf("expected [REGRESSION] in output, got:\n%s", text)
	}
	if !strings.Contains(text, "The check passed on the base branch at commit 1111111 but failed on this commit.") {
		t.Errorf("expected base pass reason in output, got:\n%s", text)
	}
	if !strings.Contains(text, "This failure was introduced by your changes.") {
		t.Errorf("expected regression guidance in output, got:\n%s", text)
	}
}

func TestPullRequestStatusAnnotatesSharedBaseFault(t *testing.T) {
	client := &fakePullRequests{
		head: &github.BranchHead{SHA: "2222222333444", Message: "feat: something"},
		heads: map[string]*github.BranchHead{
			"grain/task-654": {SHA: "2222222333444", Message: "feat: something"},
			"main":           {SHA: "1111111222333", Message: "fix: base"},
		},
		defaultBranch: "main",
		checksByRef: map[string][]github.CheckRun{
			"2222222333444": {
				{
					Name:       "build-suite",
					Status:     "completed",
					Conclusion: strPtr("failure"),
				},
			},
			"1111111222333": {
				{
					Name:       "build-suite",
					Status:     "completed",
					Conclusion: strPtr("failure"),
				},
			},
		},
		jobLogs: []github.JobLog{
			{
				Name: "build-suite",
				Log:  "FATAL: docker daemon down\nexit 1",
			},
		},
	}

	scope := PullRequestScope{
		Owner:  "acme",
		Repo:   "widgets",
		Branch: "grain/task-654",
	}

	text, err := pullRequestStatus(context.Background(), client, nil, scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(text, "[SHARED / BASE FAULT]") {
		t.Errorf("expected [SHARED / BASE FAULT] in output, got:\n%s", text)
	}
	if !strings.Contains(text, "The check failed on main at commit 1111111.") {
		t.Errorf("expected base failure reason in output, got:\n%s", text)
	}
	if !strings.Contains(text, "This is a shared upstream issue on the base branch, not caused by your changes. Do not attempt to fix this locally.") {
		t.Errorf("expected shared fault guidance in output, got:\n%s", text)
	}
}
