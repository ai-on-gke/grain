package mcp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/bwsalmon/grain/pkg/github"
)

// IssueReader is the read-only GitHub operations needed for reading issues
// and their comments. github.Client implements this directly.
type IssueReader interface {
	ListIssues(owner, repo, label string) ([]github.Issue, error)
	GetIssue(owner, repo string, number int) (github.Issue, error)
	ListComments(owner, repo string, number int) ([]github.Comment, error)
	// ListReviewComments is a pull request's inline review comments,
	// which ListComments' conversation endpoint does not return. A plain
	// issue answers it with a 404.
	ListReviewComments(owner, repo string, number int) ([]github.ReviewComment, error)
}

// IssueScope holds the default repository for issue tools when a call
// does not specify one.
type IssueScope struct {
	DefaultOwner string
	DefaultRepo  string
}

// NewIssueTools returns the tools for reading GitHub issues: list_issues
// and read_issue.
func NewIssueTools(reader IssueReader, scope IssueScope) []Tool {
	return []Tool{
		listIssuesTool(reader, scope),
		readIssueTool(reader, scope),
	}
}

const noIssueReader = "GitHub issue reading is unavailable: no GitHub credentials are configured on this deployment."

func resolveRepo(scope IssueScope, repoArg string) (string, string, error) {
	repoArg = strings.TrimSpace(repoArg)
	if repoArg != "" {
		owner, repo, found := strings.Cut(repoArg, "/")
		if !found || owner == "" || repo == "" || strings.Contains(repo, "/") {
			return "", "", fmt.Errorf("invalid repository %q: expected \"owner/repo\" format", repoArg)
		}
		return owner, repo, nil
	}
	if scope.DefaultOwner == "" || scope.DefaultRepo == "" {
		return "", "", errors.New("a repository must be specified in \"owner/repo\" format (no default repository is attached to this task)")
	}
	return scope.DefaultOwner, scope.DefaultRepo, nil
}

var listIssuesInputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties": map[string]any{
		"repo": map[string]any{
			"type":        "string",
			"description": "Repository in 'owner/repo' format (e.g. 'octocat/Hello-World'). Defaults to the repository this task targets if omitted.",
		},
		"label": map[string]any{
			"type":        "string",
			"description": "Optional label to filter issues by (e.g. 'bug').",
		},
		"limit": map[string]any{
			"type":        "integer",
			"description": "Maximum number of issues to return (default 30, max 100).",
		},
	},
}

func listIssuesTool(reader IssueReader, scope IssueScope) Tool {
	return Tool{
		Name:        "list_issues",
		Description: "List open issues for a GitHub repository, optionally filtered by label.",
		InputSchema: listIssuesInputSchema,
		Handler: func(_ context.Context, args map[string]any) Result {
			if reader == nil {
				return Result{Text: noIssueReader, IsError: true}
			}
			repoArg, _ := argString(args, "repo")
			owner, repo, err := resolveRepo(scope, repoArg)
			if err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			label, _ := argString(args, "label")
			limit, ok := argInt(args, "limit")
			if !ok || limit <= 0 {
				limit = 30
			} else if limit > 100 {
				limit = 100
			}

			issues, err := reader.ListIssues(owner, repo, label)
			if err != nil {
				return Result{
					Text:    fmt.Sprintf("Error listing issues for %s/%s: %v", owner, repo, github.Readable(err)),
					IsError: true,
				}
			}

			if len(issues) == 0 {
				if label != "" {
					return Result{Text: fmt.Sprintf("No open issues found for %s/%s with label %q.", owner, repo, label)}
				}
				return Result{Text: fmt.Sprintf("No open issues found for %s/%s.", owner, repo)}
			}

			total := len(issues)
			if len(issues) > limit {
				issues = issues[:limit]
			}

			var b strings.Builder
			if total > len(issues) {
				fmt.Fprintf(&b, "Open issues for %s/%s (showing %d of %d):\n", owner, repo, len(issues), total)
			} else {
				fmt.Fprintf(&b, "Open issues for %s/%s (%d):\n", owner, repo, len(issues))
			}

			for _, issue := range issues {
				var labels []string
				for l := range issue.Labels {
					labels = append(labels, l)
				}
				sort.Strings(labels)
				labelsStr := ""
				if len(labels) > 0 {
					labelsStr = " -- labels: " + strings.Join(labels, ", ")
				}
				author := issue.Author
				if author == "" {
					author = "unknown"
				}
				fmt.Fprintf(&b, "  #%d [%s] %s (by @%s)%s\n", issue.Number, issue.State, issue.Title, author, labelsStr)
				if issue.HTMLURL != "" {
					fmt.Fprintf(&b, "    %s\n", issue.HTMLURL)
				}
			}

			return Result{Text: strings.TrimSpace(b.String())}
		},
	}
}

var readIssueInputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties": map[string]any{
		"issue_number": map[string]any{
			"type":        "integer",
			"description": "The GitHub issue number to read.",
		},
		"repo": map[string]any{
			"type":        "string",
			"description": "Repository in 'owner/repo' format (e.g. 'octocat/Hello-World'). Defaults to the repository this task targets if omitted.",
		},
		"include_comments": map[string]any{
			"type":        "boolean",
			"description": "Whether to include discussion comments on the issue, and a pull request's inline review comments (default true).",
		},
	},
	"required": []string{"issue_number"},
}

func readIssueTool(reader IssueReader, scope IssueScope) Tool {
	return Tool{
		Name:        "read_issue",
		Description: "Read a specific GitHub issue by number, including its title, body, author, state, labels, and discussion comments.",
		InputSchema: readIssueInputSchema,
		Handler: func(_ context.Context, args map[string]any) Result {
			if reader == nil {
				return Result{Text: noIssueReader, IsError: true}
			}
			number, ok := argInt(args, "issue_number")
			if !ok || number <= 0 {
				number, ok = argInt(args, "number")
			}
			if !ok || number <= 0 {
				number, ok = argInt(args, "issue")
			}
			if !ok || number <= 0 {
				return Result{Text: "issue_number is required and must be a positive integer", IsError: true}
			}

			repoArg, _ := argString(args, "repo")
			owner, repo, err := resolveRepo(scope, repoArg)
			if err != nil {
				return Result{Text: err.Error(), IsError: true}
			}

			includeComments := true
			if inc, ok := argBool(args, "include_comments"); ok {
				includeComments = inc
			}

			issue, err := reader.GetIssue(owner, repo, number)
			if err != nil {
				return Result{
					Text:    fmt.Sprintf("Error reading issue #%d in %s/%s: %v", number, owner, repo, github.Readable(err)),
					IsError: true,
				}
			}

			var b strings.Builder
			fmt.Fprintf(&b, "Issue #%d: %s\n", issue.Number, issue.Title)
			author := issue.Author
			if author == "" {
				author = "unknown"
			}
			fmt.Fprintf(&b, "State: %s | Author: @%s\n", issue.State, author)
			if issue.HTMLURL != "" {
				fmt.Fprintf(&b, "URL: %s\n", issue.HTMLURL)
			}

			var labels []string
			for l := range issue.Labels {
				labels = append(labels, l)
			}
			sort.Strings(labels)
			if len(labels) > 0 {
				fmt.Fprintf(&b, "Labels: %s\n", strings.Join(labels, ", "))
			}

			b.WriteString("\nDescription:\n")
			if strings.TrimSpace(issue.Body) != "" {
				b.WriteString(strings.TrimSpace(issue.Body))
				b.WriteString("\n")
			} else {
				b.WriteString("(No description provided)\n")
			}

			if includeComments {
				comments, err := reader.ListComments(owner, repo, number)
				if err != nil {
					fmt.Fprintf(&b, "\nComments: (error fetching comments: %v)\n", github.Readable(err))
				} else if len(comments) == 0 {
					b.WriteString("\nComments:\n(No comments on this issue)\n")
				} else {
					fmt.Fprintf(&b, "\nComments (%d):\n", len(comments))
					for i, c := range comments {
						assoc := c.AuthorAssociation
						if assoc == "" {
							assoc = "NONE"
						}
						user := c.User
						if user == "" {
							user = "unknown"
						}
						fmt.Fprintf(&b, "\n--- Comment %d by @%s (%s) ---\n%s\n", i+1, user, assoc, strings.TrimSpace(c.Body))
					}
				}
				// Only a pull request has inline review comments; the
				// endpoint answers a plain issue's number with a 404,
				// which is not worth a line.
				review, err := reader.ListReviewComments(owner, repo, number)
				switch {
				case err != nil && !github.IsNotFound(err):
					fmt.Fprintf(&b, "\nInline review comments: (error fetching them: %v)\n", github.Readable(err))
				case len(review) > 0:
					b.WriteString("\n" + renderReviewComments(review, nil))
				}
			}

			return Result{Text: strings.TrimSpace(b.String())}
		},
	}
}
