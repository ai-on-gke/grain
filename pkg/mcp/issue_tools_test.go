package mcp_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/mcp"
)

type mockIssueReader struct {
	issues   func(owner, repo, label string) ([]github.Issue, error)
	issue    func(owner, repo string, number int) (github.Issue, error)
	comments func(owner, repo string, number int) ([]github.Comment, error)
	review   func(owner, repo string, number int) ([]github.ReviewComment, error)
}

func (m *mockIssueReader) ListReviewComments(owner, repo string, number int) ([]github.ReviewComment, error) {
	if m.review != nil {
		return m.review(owner, repo, number)
	}
	return nil, nil
}

func (m *mockIssueReader) ListIssues(owner, repo, label string) ([]github.Issue, error) {
	if m.issues != nil {
		return m.issues(owner, repo, label)
	}
	return nil, nil
}

func (m *mockIssueReader) GetIssue(owner, repo string, number int) (github.Issue, error) {
	if m.issue != nil {
		return m.issue(owner, repo, number)
	}
	return github.Issue{}, nil
}

func (m *mockIssueReader) ListComments(owner, repo string, number int) ([]github.Comment, error) {
	if m.comments != nil {
		return m.comments(owner, repo, number)
	}
	return nil, nil
}

func TestIssueToolsNilReaderReturnsGracefulError(t *testing.T) {
	tools := mcp.NewIssueTools(nil, mcp.IssueScope{DefaultOwner: "owner", DefaultRepo: "repo"})
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}

	for _, tool := range tools {
		res := tool.Handler(context.Background(), map[string]any{"issue_number": 1})
		if !res.IsError {
			t.Errorf("tool %s expected IsError=true with nil reader", tool.Name)
		}
		if !strings.Contains(res.Text, "GitHub issue reading is unavailable") {
			t.Errorf("tool %s unexpected text: %s", tool.Name, res.Text)
		}
	}
}

func TestListIssuesTool(t *testing.T) {
	reader := &mockIssueReader{
		issues: func(owner, repo, label string) ([]github.Issue, error) {
			if owner != "acme" || repo != "widgets" {
				t.Fatalf("unexpected repo: %s/%s", owner, repo)
			}
			if label == "nonexistent" {
				return nil, nil
			}
			return []github.Issue{
				{
					Number:  101,
					Title:   "Crash on startup",
					State:   "open",
					Author:  "alice",
					HTMLURL: "https://github.com/acme/widgets/issues/101",
					Labels:  map[string]struct{}{"bug": {}, "p1": {}},
				},
				{
					Number:  102,
					Title:   "Documentation fix",
					State:   "open",
					Author:  "bob",
					HTMLURL: "https://github.com/acme/widgets/issues/102",
				},
			}, nil
		},
	}

	tools := mcp.NewIssueTools(reader, mcp.IssueScope{DefaultOwner: "acme", DefaultRepo: "widgets"})
	var listTool mcp.Tool
	for _, tool := range tools {
		if tool.Name == "list_issues" {
			listTool = tool
			break
		}
	}
	if listTool.Name == "" {
		t.Fatal("list_issues tool not found")
	}

	// 1. Default repo scope
	res := listTool.Handler(context.Background(), nil)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Text)
	}
	if !strings.Contains(res.Text, "Open issues for acme/widgets (2):") ||
		!strings.Contains(res.Text, "#101 [open] Crash on startup (by @alice) -- labels: bug, p1") ||
		!strings.Contains(res.Text, "#102 [open] Documentation fix (by @bob)") {
		t.Errorf("unexpected output: %s", res.Text)
	}

	// 2. Empty results with label
	resEmpty := listTool.Handler(context.Background(), map[string]any{"label": "nonexistent"})
	if resEmpty.IsError {
		t.Fatalf("unexpected error: %s", resEmpty.Text)
	}
	if !strings.Contains(resEmpty.Text, "No open issues found for acme/widgets with label \"nonexistent\".") {
		t.Errorf("unexpected empty output: %s", resEmpty.Text)
	}

	// 3. Error from GitHub
	readerErr := &mockIssueReader{
		issues: func(owner, repo, label string) ([]github.Issue, error) {
			return nil, errors.New("rate limited")
		},
	}
	toolsErr := mcp.NewIssueTools(readerErr, mcp.IssueScope{DefaultOwner: "acme", DefaultRepo: "widgets"})
	resErr := toolsErr[0].Handler(context.Background(), nil)
	if !resErr.IsError || !strings.Contains(resErr.Text, "rate limited") {
		t.Errorf("expected error containing rate limited, got: %+v", resErr)
	}

	// 4. Missing repo without default scope
	toolsNoScope := mcp.NewIssueTools(reader, mcp.IssueScope{})
	resNoScope := toolsNoScope[0].Handler(context.Background(), nil)
	if !resNoScope.IsError || !strings.Contains(resNoScope.Text, "a repository must be specified") {
		t.Errorf("expected repo required error, got: %+v", resNoScope)
	}

	// 5. Invalid repo format
	resInvalid := toolsNoScope[0].Handler(context.Background(), map[string]any{"repo": "not-a-valid-repo"})
	if !resInvalid.IsError || !strings.Contains(resInvalid.Text, "invalid repository") {
		t.Errorf("expected invalid repo error, got: %+v", resInvalid)
	}
}

func TestReadIssueTool(t *testing.T) {
	reader := &mockIssueReader{
		issue: func(owner, repo string, number int) (github.Issue, error) {
			if owner != "acme" || repo != "widgets" || number != 42 {
				return github.Issue{}, errors.New("issue not found")
			}
			return github.Issue{
				Number:  42,
				Title:   "Important Feature Request",
				State:   "open",
				Author:  "carol",
				Body:    "Please add support for reading issues.",
				HTMLURL: "https://github.com/acme/widgets/issues/42",
				Labels:  map[string]struct{}{"enhancement": {}},
			}, nil
		},
		comments: func(owner, repo string, number int) ([]github.Comment, error) {
			if number == 42 {
				return []github.Comment{
					{
						ID:                1,
						User:              "dave",
						Body:              "I agree with this request!",
						AuthorAssociation: "CONTRIBUTOR",
					},
				}, nil
			}
			return nil, nil
		},
	}

	tools := mcp.NewIssueTools(reader, mcp.IssueScope{DefaultOwner: "acme", DefaultRepo: "widgets"})
	var readTool mcp.Tool
	for _, tool := range tools {
		if tool.Name == "read_issue" {
			readTool = tool
			break
		}
	}
	if readTool.Name == "" {
		t.Fatal("read_issue tool not found")
	}

	// 1. Full read with comments
	res := readTool.Handler(context.Background(), map[string]any{"issue_number": 42})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Text)
	}
	if !strings.Contains(res.Text, "Issue #42: Important Feature Request") ||
		!strings.Contains(res.Text, "State: open | Author: @carol") ||
		!strings.Contains(res.Text, "Labels: enhancement") ||
		!strings.Contains(res.Text, "Please add support for reading issues.") ||
		!strings.Contains(res.Text, "Comments (1):") ||
		!strings.Contains(res.Text, "--- Comment 1 by @dave (CONTRIBUTOR) ---") ||
		!strings.Contains(res.Text, "I agree with this request!") {
		t.Errorf("unexpected read output:\n%s", res.Text)
	}

	// 2. Read without comments
	resNoComments := readTool.Handler(context.Background(), map[string]any{"issue_number": 42, "include_comments": false})
	if resNoComments.IsError {
		t.Fatalf("unexpected error: %s", resNoComments.Text)
	}
	if strings.Contains(resNoComments.Text, "Comments (1):") || strings.Contains(resNoComments.Text, "I agree with this request!") {
		t.Errorf("expected no comments in output, got:\n%s", resNoComments.Text)
	}

	// 3. Missing issue number
	resMissing := readTool.Handler(context.Background(), nil)
	if !resMissing.IsError || !strings.Contains(resMissing.Text, "issue_number is required") {
		t.Errorf("expected missing issue_number error, got: %+v", resMissing)
	}

	// 4. Issue not found
	resNotFound := readTool.Handler(context.Background(), map[string]any{"issue_number": 999})
	if !resNotFound.IsError || !strings.Contains(resNotFound.Text, "issue not found") {
		t.Errorf("expected issue not found error, got: %+v", resNotFound)
	}
}

func readIssueWithReview(t *testing.T, review func(owner, repo string, number int) ([]github.ReviewComment, error), args map[string]any) mcp.Result {
	t.Helper()
	reader := &mockIssueReader{
		issue: func(owner, repo string, number int) (github.Issue, error) {
			return github.Issue{Number: number, Title: "Fold the widget", State: "open", Author: "carol"}, nil
		},
		review: review,
	}
	for _, tool := range mcp.NewIssueTools(reader, mcp.IssueScope{DefaultOwner: "acme", DefaultRepo: "widgets"}) {
		if tool.Name == "read_issue" {
			return tool.Handler(context.Background(), args)
		}
	}
	t.Fatal("read_issue tool not found")
	return mcp.Result{}
}

func TestReadIssueListsAPullRequestsInlineReviewComments(t *testing.T) {
	line := 7
	var asked string
	res := readIssueWithReview(t, func(owner, repo string, number int) ([]github.ReviewComment, error) {
		asked = owner + "/" + repo + "#" + strconv.Itoa(number)
		return []github.ReviewComment{
			{ID: 11, User: "gemini-code-assist", Body: "Prefer errors.Is here.", Path: "pkg/a.go", Line: &line},
			{ID: 13, User: "carol", Body: "Agreed.", Path: "pkg/a.go", Line: &line, InReplyTo: 11},
			{ID: 14, User: "dave", Body: "Old remark.", Path: "pkg/b.go"},
		}, nil
	}, map[string]any{"issue_number": 42})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Text)
	}
	if asked != "acme/widgets#42" {
		t.Errorf("review comments read for %s, want acme/widgets#42", asked)
	}
	for _, want := range []string{
		"Inline review comments (2 thread(s)):",
		"comment 11 by @gemini-code-assist on pkg/a.go:7\n    Prefer errors.Is here.\n    reply 13 by @carol:\n      Agreed.",
		"comment 14 by @dave on pkg/b.go (outdated)",
	} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("answer is missing %q; got:\n%s", want, res.Text)
		}
	}
}

func TestReadIssueSaysNothingOfReviewCommentsForAPlainIssue(t *testing.T) {
	res := readIssueWithReview(t, func(string, string, int) ([]github.ReviewComment, error) {
		return nil, &github.Error{Status: 404, Body: []byte(`{"message":"Not Found"}`)}
	}, map[string]any{"issue_number": 42})
	if res.IsError || strings.Contains(res.Text, "review") || strings.Contains(res.Text, "Not Found") {
		t.Errorf("got %+v", res)
	}
}

func TestReadIssueReportsAnUnreadableReviewCommentList(t *testing.T) {
	res := readIssueWithReview(t, func(string, string, int) ([]github.ReviewComment, error) {
		return nil, &github.Error{Status: 403, Body: []byte(`{"message":"Resource not accessible"}`)}
	}, map[string]any{"issue_number": 42})
	if res.IsError || !strings.Contains(res.Text, "Inline review comments: (error fetching them: ") ||
		!strings.Contains(res.Text, "Resource not accessible") {
		t.Errorf("got %+v", res)
	}
}

func TestReadIssueWithoutCommentsReadsNoReviewComments(t *testing.T) {
	res := readIssueWithReview(t, func(string, string, int) ([]github.ReviewComment, error) {
		t.Error("review comments read with include_comments false")
		return nil, nil
	}, map[string]any{"issue_number": 42, "include_comments": false})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Text)
	}
}
