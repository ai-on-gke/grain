package github

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ReviewEvent is the submission action passed to GitHub's pull request
// review creation API when posting a visible review (rather than omitting
// event for a PENDING draft). A GitHub App (<slug>[bot]) has no web login to
// open and submit a PENDING draft review, so bot-backed reviews must submit
// directly using one of these events.
type ReviewEvent string

const (
	ReviewEventComment        ReviewEvent = "COMMENT"
	ReviewEventApprove        ReviewEvent = "APPROVE"
	ReviewEventRequestChanges ReviewEvent = "REQUEST_CHANGES"
)

// ReviewSubmitter is the optional half of Client that posts a submitted
// review (carrying an explicit event) on a pull request. Kept separate from
// Client.CreateReview so existing fakes and tests for draft reviews remain
// unchanged.
type ReviewSubmitter interface {
	SubmitReview(owner, repo string, number int, event ReviewEvent, body string, comments []NewReviewComment) (int, error)
}

// CollaboratorReader is the optional half of Client that checks a GitHub
// user's repository permission level ("admin", "maintain", "write",
// "triage", "read", or "none") so inbound webhook events can enforce the
// trust boundary before queueing sandboxed agent work.
type CollaboratorReader interface {
	CollaboratorPermission(owner, repo, username string) (string, error)
}

// ReactionPoster is the optional half of Client that adds a reaction (such
// as "eyes" or "rocket") to an issue or pull-request review comment to
// acknowledge an @bot command.
type ReactionPoster interface {
	CreateIssueCommentReaction(owner, repo string, commentID int64, content string) error
	CreateReviewCommentReaction(owner, repo string, commentID int64, content string) error
}

// CheckRunOutput is the rich title/summary/details payload attached to a
// GitHub Check Run created or updated by a GitHub App installation token.
type CheckRunOutput struct {
	Title   string
	Summary string
	Text    string
}

// CheckRunWrite describes a GitHub Check Run creation or update request.
type CheckRunWrite struct {
	Name        string
	HeadSHA     string
	Status      string // "queued", "in_progress", "completed"
	Conclusion  string // required when Status == "completed"
	DetailsURL  string
	ExternalID  string
	StartedAt   *time.Time
	CompletedAt *time.Time
	Output      *CheckRunOutput
}

// CheckRunWriter is the optional half of Client that creates and updates
// native GitHub Check Runs using an App installation token with checks:write.
type CheckRunWriter interface {
	CreateCheckRun(owner, repo string, run CheckRunWrite) (int64, error)
	UpdateCheckRun(owner, repo string, checkRunID int64, run CheckRunWrite) error
}

var (
	_ ReviewSubmitter    = (*RESTClient)(nil)
	_ CollaboratorReader = (*RESTClient)(nil)
	_ ReactionPoster     = (*RESTClient)(nil)
	_ CheckRunWriter     = (*RESTClient)(nil)
)

// IsTrustedCollaboratorPermission reports whether a GitHub repository
// permission level carries write access to the repository ("admin",
// "maintain", or "write").
func IsTrustedCollaboratorPermission(perm string) bool {
	switch strings.ToLower(strings.TrimSpace(perm)) {
	case "admin", "maintain", "write":
		return true
	default:
		return false
	}
}

// SubmitReview posts a submitted review with event ("COMMENT", "APPROVE",
// or "REQUEST_CHANGES"). If GitHub rejects APPROVE or REQUEST_CHANGES with
// 422 because the credential also authored the pull request, SubmitReview
// automatically retries once with ReviewEventComment so the findings still
// land visibly on the PR.
func (c *RESTClient) SubmitReview(owner, repo string, number int, event ReviewEvent, body string, comments []NewReviewComment) (int, error) {
	if event == "" {
		event = ReviewEventComment
	}
	id, err := c.postReview(owner, repo, number, string(event), body, comments)
	if err != nil && event != ReviewEventComment && isOwnPullRequestReviewError(err) {
		return c.postReview(owner, repo, number, string(ReviewEventComment), body, comments)
	}
	return id, err
}

func isOwnPullRequestReviewError(err error) bool {
	apiErr, ok := err.(*Error)
	if !ok || apiErr.Status != http.StatusUnprocessableEntity {
		return false
	}
	lower := strings.ToLower(string(apiErr.Body))
	return strings.Contains(lower, "your own pull request")
}

func (c *RESTClient) postReview(owner, repo string, number int, event, body string, comments []NewReviewComment) (int, error) {
	payloadComments := make([]map[string]any, len(comments))
	for i, cm := range comments {
		payloadComments[i] = map[string]any{"path": cm.Path, "line": cm.Line, "body": cm.Body}
	}
	reqBody := map[string]any{
		"body":     body,
		"comments": payloadComments,
	}
	if event != "" {
		reqBody["event"] = event
	}
	payload, _ := json.Marshal(reqBody)
	resp, err := c.request(
		"POST", fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", owner, repo, number),
		c.headers(owner, repo, true), payload,
	)
	if err != nil {
		return 0, err
	}
	if resp.Status != http.StatusOK {
		return 0, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var data struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return 0, err
	}
	return data.ID, nil
}

// CollaboratorPermission returns username's repository permission level on
// owner/repo ("admin", "maintain", "write", "triage", "read", or "none").
func (c *RESTClient) CollaboratorPermission(owner, repo, username string) (string, error) {
	if strings.TrimSpace(username) == "" {
		return "none", nil
	}
	resp, err := c.request(
		"GET",
		fmt.Sprintf("/repos/%s/%s/collaborators/%s/permission",
			url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(username)),
		c.headers(owner, repo, false), nil,
	)
	if err != nil {
		return "", err
	}
	if resp.Status == http.StatusNotFound {
		return "none", nil
	}
	if resp.Status != http.StatusOK {
		return "", &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var data struct {
		Permission string `json:"permission"`
	}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return "", err
	}
	if data.Permission == "" {
		return "none", nil
	}
	return strings.ToLower(data.Permission), nil
}

// CreateIssueCommentReaction adds a reaction (e.g. "eyes", "rocket", "+1")
// to an issue or PR conversation comment.
func (c *RESTClient) CreateIssueCommentReaction(owner, repo string, commentID int64, content string) error {
	path := fmt.Sprintf("/repos/%s/%s/issues/comments/%d/reactions", owner, repo, commentID)
	return c.postReaction(owner, repo, path, content)
}

// CreateReviewCommentReaction adds a reaction (e.g. "eyes", "rocket", "+1")
// to an inline pull request review comment.
func (c *RESTClient) CreateReviewCommentReaction(owner, repo string, commentID int64, content string) error {
	path := fmt.Sprintf("/repos/%s/%s/pulls/comments/%d/reactions", owner, repo, commentID)
	return c.postReaction(owner, repo, path, content)
}

func (c *RESTClient) postReaction(owner, repo, path, content string) error {
	payload, _ := json.Marshal(map[string]string{"content": content})
	resp, err := c.request("POST", path, c.headers(owner, repo, true), payload)
	if err != nil {
		return err
	}
	if resp.Status != http.StatusOK && resp.Status != http.StatusCreated {
		return &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	return nil
}

// CreateCheckRun creates a new check run on owner/repo using the Checks API.
func (c *RESTClient) CreateCheckRun(owner, repo string, run CheckRunWrite) (int64, error) {
	payload, err := marshalCheckRunWrite(run, true)
	if err != nil {
		return 0, err
	}
	resp, err := c.request(
		"POST", fmt.Sprintf("/repos/%s/%s/check-runs", owner, repo),
		c.headers(owner, repo, true), payload,
	)
	if err != nil {
		return 0, err
	}
	if resp.Status != http.StatusCreated && resp.Status != http.StatusOK {
		return 0, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var data struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return 0, err
	}
	return data.ID, nil
}

// UpdateCheckRun updates an existing check run on owner/repo.
func (c *RESTClient) UpdateCheckRun(owner, repo string, checkRunID int64, run CheckRunWrite) error {
	payload, err := marshalCheckRunWrite(run, false)
	if err != nil {
		return err
	}
	resp, err := c.request(
		"PATCH", fmt.Sprintf("/repos/%s/%s/check-runs/%d", owner, repo, checkRunID),
		c.headers(owner, repo, true), payload,
	)
	if err != nil {
		return err
	}
	if resp.Status != http.StatusOK {
		return &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	return nil
}

func marshalCheckRunWrite(run CheckRunWrite, includeHead bool) ([]byte, error) {
	m := map[string]any{}
	if run.Name != "" {
		m["name"] = run.Name
	}
	if includeHead && run.HeadSHA != "" {
		m["head_sha"] = run.HeadSHA
	}
	if run.Status != "" {
		m["status"] = run.Status
	}
	if run.Conclusion != "" {
		m["conclusion"] = run.Conclusion
	}
	if run.DetailsURL != "" {
		m["details_url"] = run.DetailsURL
	}
	if run.ExternalID != "" {
		m["external_id"] = run.ExternalID
	}
	if run.StartedAt != nil {
		m["started_at"] = run.StartedAt.UTC().Format(time.RFC3339)
	}
	if run.CompletedAt != nil {
		m["completed_at"] = run.CompletedAt.UTC().Format(time.RFC3339)
	}
	if run.Output != nil {
		out := map[string]string{
			"title":   run.Output.Title,
			"summary": run.Output.Summary,
		}
		if run.Output.Text != "" {
			out["text"] = run.Output.Text
		}
		m["output"] = out
	}
	return json.Marshal(m)
}
