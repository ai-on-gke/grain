package mcp

// pullrequest_review_tools.go is the review half of a run's own pull
// request: reading the inline comments reviewers left on it, answering
// them, and resolving the conversations they opened.
//
// A repository whose ruleset requires conversation resolution holds a
// pull request until every review thread on it is resolved, and a bot
// reviewer (gemini-code-assist and the like) opens one per remark. Before
// this a run could neither see those threads -- pull_request_status read
// checks, and read_issue reads the issue conversation, where inline
// comments do not appear -- nor answer or resolve them, so its pull
// request sat held until a person clicked through each one.
//
// These are the only tools in this package that write to GitHub, and
// they are held to exactly the scope the read-only ones are: the pull
// request open for PullRequestScope's own branch, found by the same
// lookup pull_request_status makes. No argument names a repository, a
// branch or a pull request number. A comment or thread ID the agent
// passes is checked against the threads of that one pull request before
// anything is posted, so an ID copied from somewhere else is refused
// rather than acted on.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/bwsalmon/grain/pkg/github"
)

// PullRequestCommenter is what reply_to_pull_request_comment and
// resolve_review_thread need on top of PullRequestReader: posting a
// comment on the pull request, replying in an inline thread, and reading
// and resolving review threads. github.RESTClient implements it. It is
// kept apart from PullRequestReader so that the read-only interface
// stays read-only; NewPullRequestTools finds it on the client it was
// given, and a client without it registers both tools as ones that say
// so.
type PullRequestCommenter interface {
	CreateComment(owner, repo string, number int, body string) (int, error)
	ReplyToReviewComment(owner, repo string, pullNumber, commentID int, body string) (int, error)
	ListReviewThreads(owner, repo string, number int) ([]github.ReviewThread, error)
	ResolveReviewThread(owner, repo, threadID string) error
}

// readableCommenter is PullRequestCommenter with github.Readable over
// each answer, for the reason readable_github.go gives.
type readableCommenter struct {
	client PullRequestCommenter
}

func (r readableCommenter) CreateComment(owner, repo string, number int, body string) (int, error) {
	id, err := r.client.CreateComment(owner, repo, number, body)
	return id, github.Readable(err)
}

func (r readableCommenter) ReplyToReviewComment(owner, repo string, pullNumber, commentID int, body string) (int, error) {
	id, err := r.client.ReplyToReviewComment(owner, repo, pullNumber, commentID, body)
	return id, github.Readable(err)
}

func (r readableCommenter) ListReviewThreads(owner, repo string, number int) ([]github.ReviewThread, error) {
	threads, err := r.client.ListReviewThreads(owner, repo, number)
	return threads, github.Readable(err)
}

func (r readableCommenter) ResolveReviewThread(owner, repo, threadID string) error {
	return github.Readable(r.client.ResolveReviewThread(owner, repo, threadID))
}

// commenterOf is the PullRequestCommenter client also is, if it is one.
func commenterOf(client PullRequestReader) PullRequestCommenter {
	switch c := client.(type) {
	case nil:
		return nil
	case readableReader:
		return commenterOf(c.client)
	case PullRequestCommenter:
		return readableCommenter{client: c}
	}
	return nil
}

// reviewCommentsSection is the inline review comments on pull request
// number, rendered for pull_request_status: "" when there are none, so a
// pull request nobody has reviewed reads exactly as it did before these
// were shown. threads, when the commenter could read them, adds each
// comment's thread and whether it is resolved; a resolved thread is
// listed by its first line only, since there is nothing left to do
// about it.
func reviewCommentsSection(client PullRequestReader, commenter PullRequestCommenter, scope PullRequestScope, number int) string {
	comments, err := client.ListReviewComments(scope.Owner, scope.Repo, number)
	if err != nil {
		return fmt.Sprintf("\nInline review comments could not be read: %v\n", err)
	}
	if len(comments) == 0 {
		return ""
	}
	var threads []github.ReviewThread
	if commenter != nil {
		// Best effort: the thread view is GraphQL, which a credential or
		// a host may not offer, and the comments are worth showing
		// without it.
		read, err := commenter.ListReviewThreads(scope.Owner, scope.Repo, number)
		if err == nil {
			threads = read
		}
	}
	return "\n" + renderReviewComments(comments, threads) +
		"Answer one with reply_to_pull_request_comment (comment_id and body), and mark its " +
		"conversation resolved with resolve_review_thread once it is dealt with -- a " +
		"repository that requires conversation resolution will not merge this pull " +
		"request while any thread is open.\n"
}

// renderReviewComments lays comments out thread by thread, each reply
// indented under the comment that opened its thread, in the order GitHub
// listed the threads' first comments.
func renderReviewComments(comments []github.ReviewComment, threads []github.ReviewThread) string {
	threadOf := map[int]github.ReviewThread{}
	for _, t := range threads {
		for _, id := range t.CommentIDs {
			threadOf[id] = t
		}
	}
	var roots []int
	replies := map[int][]github.ReviewComment{}
	byID := map[int]github.ReviewComment{}
	for _, c := range comments {
		byID[c.ID] = c
	}
	for _, c := range comments {
		root := c.InReplyTo
		if _, ok := byID[root]; root == 0 || !ok {
			roots = append(roots, c.ID)
			continue
		}
		replies[root] = append(replies[root], c)
	}

	unresolved := 0
	var b strings.Builder
	for _, id := range roots {
		root := byID[id]
		thread, known := threadOf[id]
		if !known || !thread.Resolved {
			unresolved++
		}
		where := root.Path
		if root.Line != nil {
			where = fmt.Sprintf("%s:%d", root.Path, *root.Line)
		} else {
			where += " (outdated)"
		}
		state := ""
		if known {
			state = ", thread " + thread.ID + ", unresolved"
			if thread.Resolved {
				state = ", thread " + thread.ID + ", resolved"
			}
		}
		fmt.Fprintf(&b, "  comment %d by @%s on %s%s\n", root.ID, userOrUnknown(root.User), where, state)
		if known && thread.Resolved {
			fmt.Fprintf(&b, "    %s\n", firstLine(root.Body))
			continue
		}
		writeIndented(&b, root.Body, "    ")
		for _, reply := range replies[id] {
			fmt.Fprintf(&b, "    reply %d by @%s:\n", reply.ID, userOrUnknown(reply.User))
			writeIndented(&b, reply.Body, "      ")
		}
	}

	header := fmt.Sprintf("Inline review comments (%d thread(s)", len(roots))
	if len(threads) > 0 {
		header += fmt.Sprintf(", %d unresolved", unresolved)
	}
	return header + "):\n" + b.String()
}

func userOrUnknown(user string) string {
	if user == "" {
		return "unknown"
	}
	return user
}

func writeIndented(b *strings.Builder, text, indent string) {
	text = strings.TrimSpace(text)
	if text == "" {
		text = "(empty comment)"
	}
	for _, line := range strings.Split(text, "\n") {
		b.WriteString(strings.TrimRight(indent+line, " \t"))
		b.WriteString("\n")
	}
}

// argID reads an ID argument an agent may pass as a number or as the
// digits of one.
func argID(args map[string]any, key string) (int, bool) {
	if n, ok := argInt(args, key); ok {
		return n, n > 0
	}
	if s, ok := argString(args, key); ok {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		return n, err == nil && n > 0
	}
	return 0, false
}

const noPullRequestCommenter = "There is no pull request to write to: this run has no " +
	"GitHub repository configured for it that grain can comment through."

// ownOpenPullRequest is the pull request open for the run's own branch
// (or the daemon-pinned external PullRequestNumber when the run has no
// open pull request on its own branch), or a sentence for the agent saying
// why there is none.
func ownOpenPullRequest(client PullRequestReader, scope PullRequestScope) (*github.PullRequest, *Result) {
	if scope.Branch != "" {
		pr, err := client.FindOpenPullRequestForBranch(scope.Owner, scope.Repo, scope.Branch)
		if err != nil {
			return nil, &Result{Text: fmt.Sprintf("looking for an open pull request for %s: %v", scope.Branch, err), IsError: true}
		}
		if pr != nil {
			return pr, nil
		}
	}
	if scope.PullRequestNumber > 0 {
		detail, err := client.GetPullRequest(scope.Owner, scope.Repo, scope.PullRequestNumber)
		if err != nil {
			return nil, &Result{Text: fmt.Sprintf("reading pull request #%d: %v", scope.PullRequestNumber, err), IsError: true}
		}
		if detail.State != "open" {
			return nil, &Result{Text: fmt.Sprintf(
				"Pull request #%d is %s, so there is nothing open to comment on.",
				detail.Number, detail.State), IsError: true}
		}
		return &github.PullRequest{
			Number:  detail.Number,
			HTMLURL: detail.HTMLURL,
		}, nil
	}
	return nil, &Result{Text: fmt.Sprintf(
		"No pull request is open for %s, so there is nothing to comment on. Push the branch "+
			"and open its pull request first.", scope.Branch), IsError: true}
}

func replyToPullRequestCommentTool(client PullRequestReader, commenter PullRequestCommenter, scope PullRequestScope) Tool {
	return Tool{
		Name: "reply_to_pull_request_comment",
		Description: "Post a comment on GitHub on the pull request open for the branch you are " +
			"pushing to, and only that one. With comment_id, the reply goes into that inline " +
			"review comment's thread (the IDs are in pull_request_status); without it, it is " +
			"a top-level comment on the pull request. This publishes to GitHub where reviewers " +
			"read it -- use it to answer a reviewer, not to report to grain (that is " +
			"comment_on_issue).",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"body": map[string]any{
					"type":        "string",
					"description": "The comment text, in GitHub-flavored markdown.",
				},
				"comment_id": map[string]any{
					"type":        "integer",
					"description": "The inline review comment to reply to. Omit for a top-level comment on the pull request.",
				},
			},
			"required": []string{"body"},
		},
		Handler: func(_ context.Context, args map[string]any) Result {
			if client == nil || commenter == nil || !scope.complete() {
				return Result{Text: noPullRequestCommenter, IsError: true}
			}
			body, _ := argString(args, "body")
			if strings.TrimSpace(body) == "" {
				return Result{Text: "body is required and must not be empty", IsError: true}
			}
			_, hasCommentArg := args["comment_id"]
			commentID, ok := argID(args, "comment_id")
			if hasCommentArg && !ok {
				return Result{Text: "comment_id must be a positive integer", IsError: true}
			}
			pr, refusal := ownOpenPullRequest(client, scope)
			if refusal != nil {
				return *refusal
			}
			if !hasCommentArg {
				id, err := commenter.CreateComment(scope.Owner, scope.Repo, pr.Number, body)
				if err != nil {
					return Result{Text: fmt.Sprintf("commenting on pull request #%d: %v", pr.Number, err), IsError: true}
				}
				return Result{Text: fmt.Sprintf("Posted comment %d on pull request #%d.", id, pr.Number)}
			}
			comments, err := client.ListReviewComments(scope.Owner, scope.Repo, pr.Number)
			if err != nil {
				return Result{Text: fmt.Sprintf("reading pull request #%d's review comments: %v", pr.Number, err), IsError: true}
			}
			if !hasReviewComment(comments, commentID) {
				return Result{Text: fmt.Sprintf(
					"Comment %d is not an inline review comment on pull request #%d, the one open "+
						"for %s -- nothing was posted. pull_request_status lists the ones there are.",
					commentID, pr.Number, scope.Branch), IsError: true}
			}
			id, err := commenter.ReplyToReviewComment(scope.Owner, scope.Repo, pr.Number, commentID, body)
			if err != nil {
				return Result{Text: fmt.Sprintf("replying to comment %d on pull request #%d: %v", commentID, pr.Number, err), IsError: true}
			}
			return Result{Text: fmt.Sprintf("Replied to comment %d on pull request #%d (reply %d).", commentID, pr.Number, id)}
		},
	}
}

func hasReviewComment(comments []github.ReviewComment, id int) bool {
	for _, c := range comments {
		if c.ID == id {
			return true
		}
	}
	return false
}

func resolveReviewThreadTool(client PullRequestReader, commenter PullRequestCommenter, scope PullRequestScope) Tool {
	return Tool{
		Name: "resolve_review_thread",
		Description: "Mark an inline review conversation resolved on GitHub, on the pull request " +
			"open for the branch you are pushing to and no other. Name it by thread_id, or by " +
			"the comment_id of any comment in it (both are in pull_request_status). Resolve " +
			"a thread once its point is dealt with -- a repository that requires conversation " +
			"resolution will not merge while one is open -- and reply first if the reviewer " +
			"should know what you did.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"thread_id": map[string]any{
					"type":        "string",
					"description": "The review thread's ID, as pull_request_status shows it.",
				},
				"comment_id": map[string]any{
					"type":        "integer",
					"description": "The ID of any inline review comment in the thread.",
				},
			},
		},
		Handler: func(_ context.Context, args map[string]any) Result {
			if client == nil || commenter == nil || !scope.complete() {
				return Result{Text: noPullRequestCommenter, IsError: true}
			}
			threadID, _ := argString(args, "thread_id")
			threadID = strings.TrimSpace(threadID)
			commentID, hasComment := argID(args, "comment_id")
			if threadID == "" && !hasComment {
				return Result{Text: "name the thread: thread_id, or the comment_id of a comment in it", IsError: true}
			}
			pr, refusal := ownOpenPullRequest(client, scope)
			if refusal != nil {
				return *refusal
			}
			threads, err := commenter.ListReviewThreads(scope.Owner, scope.Repo, pr.Number)
			if err != nil {
				return Result{Text: fmt.Sprintf("reading pull request #%d's review threads: %v", pr.Number, err), IsError: true}
			}
			thread, found := findReviewThread(threads, threadID, commentID)
			if !found {
				named := "thread " + threadID
				if threadID == "" {
					named = fmt.Sprintf("a thread holding comment %d", commentID)
				}
				return Result{Text: fmt.Sprintf(
					"There is no %s on pull request #%d, the one open for %s -- nothing was resolved.",
					named, pr.Number, scope.Branch), IsError: true}
			}
			if thread.Resolved {
				return Result{Text: fmt.Sprintf("Thread %s on pull request #%d is already resolved.", thread.ID, pr.Number)}
			}
			if err := commenter.ResolveReviewThread(scope.Owner, scope.Repo, thread.ID); err != nil {
				return Result{Text: fmt.Sprintf("resolving thread %s on pull request #%d: %v", thread.ID, pr.Number, err), IsError: true}
			}
			return Result{Text: fmt.Sprintf("Resolved thread %s on pull request #%d.", thread.ID, pr.Number)}
		},
	}
}

// findReviewThread is the thread named by threadID, or else the one
// holding commentID. When both are given they must name the same thread.
func findReviewThread(threads []github.ReviewThread, threadID string, commentID int) (github.ReviewThread, bool) {
	for _, t := range threads {
		if threadID != "" && t.ID != threadID {
			continue
		}
		if commentID == 0 {
			return t, true
		}
		for _, id := range t.CommentIDs {
			if id == commentID {
				return t, true
			}
		}
	}
	return github.ReviewThread{}, false
}
