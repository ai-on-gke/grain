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

// Package ghwebhook receives, verifies, normalizes, stores, and relays
// inbound GitHub App webhook deliveries.
//
// Like pkg/checkpost, it is an edge forge package with no dependency on
// pkg/model or grain's internal stores (docs/three-pieces.md): the public
// receiver verifies GitHub's X-Hub-Signature-256 with the webhook secret,
// deduplicates deliveries by X-GitHub-Delivery, persists compact normalized
// envelopes in a bounded JSONL log, and serves them over a timestamp-signed
// internal endpoint to grain daemon. It holds no GitHub credentials, cannot
// talk to GitHub, and never approves or dispatches tasks on its own.
package ghwebhook

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// MaxWebhookBody matches GitHub's 25 MiB webhook payload limit.
	MaxWebhookBody = 25 << 20
	// maxStoredTextField bounds free-form markdown bodies retained in the
	// normalized JSONL envelope so a multi-megabyte webhook payload cannot
	// bloat the event log.
	maxStoredTextField = 32 << 10
	// maxShortField bounds identifier-like fields (login, ref, repo, state).
	maxShortField = 512
)

// Event is one verified, normalized GitHub webhook delivery.
type Event struct {
	Seq        uint64    `json:"seq"`
	DeliveryID string    `json:"delivery_id"`
	Event      string    `json:"event"`
	Action     string    `json:"action,omitempty"`
	ReceivedAt time.Time `json:"received_at"`

	Repo           string `json:"repo,omitempty"`
	InstallationID int64  `json:"installation_id,omitempty"`

	SenderLogin string `json:"sender_login,omitempty"`
	SenderType  string `json:"sender_type,omitempty"`
	SenderID    int64  `json:"sender_id,omitempty"`

	Number         int    `json:"number,omitempty"`
	IsPullRequest  bool   `json:"is_pull_request,omitempty"`
	Title          string `json:"title,omitempty"`
	Body           string `json:"body,omitempty"`
	State          string `json:"state,omitempty"`
	Draft          bool   `json:"draft,omitempty"`
	Merged         bool   `json:"merged,omitempty"`
	MergeCommitSHA string `json:"merge_commit_sha,omitempty"`

	HeadRef  string `json:"head_ref,omitempty"`
	HeadSHA  string `json:"head_sha,omitempty"`
	HeadRepo string `json:"head_repo,omitempty"`
	BaseRef  string `json:"base_ref,omitempty"`

	AuthorLogin            string `json:"author_login,omitempty"`
	RequestedReviewerLogin string `json:"requested_reviewer_login,omitempty"`
	// Label is the label a pull_request or issues "labeled" event added.
	Label string `json:"label,omitempty"`

	CommentID   int64  `json:"comment_id,omitempty"`
	CommentBody string `json:"comment_body,omitempty"`
	CommentPath string `json:"comment_path,omitempty"`
	CommentLine int    `json:"comment_line,omitempty"`
	InReplyToID int64  `json:"in_reply_to_id,omitempty"`
	HTMLURL     string `json:"html_url,omitempty"`

	ReviewID    int64  `json:"review_id,omitempty"`
	ReviewState string `json:"review_state,omitempty"`
	ReviewBody  string `json:"review_body,omitempty"`

	CheckName       string `json:"check_name,omitempty"`
	CheckStatus     string `json:"check_status,omitempty"`
	CheckConclusion string `json:"check_conclusion,omitempty"`

	// The rest of a concluded CI job, from a check_run or workflow_job
	// delivery -- what grain records of GitHub's own CI
	// (orchestrator.recordGitHubCIEvent, model.GitHubCIJob).
	//
	// CheckRunID is the check run's id; for a workflow_job it is the
	// job's id, which GitHub uses for the job's check run too.
	CheckRunID       int64      `json:"check_run_id,omitempty"`
	CheckApp         string     `json:"check_app,omitempty"`
	CheckStartedAt   *time.Time `json:"check_started_at,omitempty"`
	CheckCompletedAt *time.Time `json:"check_completed_at,omitempty"`
	// CheckOutput is the check run's output title, summary and text,
	// bounded: what a check that is not an Actions job says instead of
	// a log.
	CheckOutput   string `json:"check_output,omitempty"`
	WorkflowName  string `json:"workflow_name,omitempty"`
	WorkflowPath  string `json:"workflow_path,omitempty"`
	WorkflowEvent string `json:"workflow_event,omitempty"`
	WorkflowRun   int64  `json:"workflow_run_id,omitempty"`
	RunAttempt    int    `json:"run_attempt,omitempty"`
	FailingStep   string `json:"failing_step,omitempty"`
}

// maxCheckOutput bounds Event.CheckOutput.
const maxCheckOutput = 16 << 10

// ParsePayload extracts a compact normalized Event from a raw GitHub webhook
// JSON payload.
func ParsePayload(eventName, deliveryID string, raw []byte) (Event, error) {
	eventName = strings.TrimSpace(eventName)
	deliveryID = strings.TrimSpace(deliveryID)
	if eventName == "" {
		return Event{}, fmt.Errorf("ghwebhook: missing X-GitHub-Event header")
	}
	if deliveryID == "" || len(deliveryID) > maxShortField {
		return Event{}, fmt.Errorf("ghwebhook: missing or oversized X-GitHub-Delivery header")
	}
	if len(eventName) > maxShortField {
		return Event{}, fmt.Errorf("ghwebhook: oversized X-GitHub-Event header")
	}

	var p rawWebhookPayload
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return Event{}, fmt.Errorf("ghwebhook: invalid JSON payload: %w", err)
		}
	}

	ev := Event{
		DeliveryID:     deliveryID,
		Event:          eventName,
		Action:         clampShort(p.Action),
		Repo:           clampShort(p.Repository.FullName),
		InstallationID: p.Installation.ID,
		SenderLogin:    clampShort(p.Sender.Login),
		SenderType:     clampShort(p.Sender.Type),
		SenderID:       p.Sender.ID,
	}

	if p.Number > 0 {
		ev.Number = p.Number
	}

	if p.PullRequest != nil {
		ev.IsPullRequest = true
		if p.PullRequest.Number > 0 {
			ev.Number = p.PullRequest.Number
		}
		ev.Title = clampText(p.PullRequest.Title, maxShortField)
		ev.Body = clampText(p.PullRequest.Body, maxStoredTextField)
		ev.State = clampShort(p.PullRequest.State)
		ev.Draft = p.PullRequest.Draft
		ev.Merged = p.PullRequest.Merged
		ev.MergeCommitSHA = clampShort(p.PullRequest.MergeCommitSHA)
		ev.HeadRef = clampShort(p.PullRequest.Head.Ref)
		ev.HeadSHA = clampShort(p.PullRequest.Head.SHA)
		ev.HeadRepo = clampShort(p.PullRequest.Head.Repo.FullName)
		ev.BaseRef = clampShort(p.PullRequest.Base.Ref)
		ev.AuthorLogin = clampShort(p.PullRequest.User.Login)
		ev.HTMLURL = clampShort(p.PullRequest.HTMLURL)
	} else if p.Issue != nil {
		ev.Number = p.Issue.Number
		ev.IsPullRequest = p.Issue.PullRequest != nil
		ev.Title = clampText(p.Issue.Title, maxShortField)
		ev.Body = clampText(p.Issue.Body, maxStoredTextField)
		ev.State = clampShort(p.Issue.State)
		ev.AuthorLogin = clampShort(p.Issue.User.Login)
		ev.HTMLURL = clampShort(p.Issue.HTMLURL)
	}

	if p.RequestedReviewer != nil && p.RequestedReviewer.Login != "" {
		ev.RequestedReviewerLogin = clampShort(p.RequestedReviewer.Login)
	}
	if p.Label != nil {
		ev.Label = clampShort(p.Label.Name)
	}

	if p.Comment != nil {
		ev.CommentID = p.Comment.ID
		ev.CommentBody = clampText(p.Comment.Body, maxStoredTextField)
		ev.CommentPath = clampShort(p.Comment.Path)
		ev.CommentLine = p.Comment.Line
		if ev.CommentLine == 0 && p.Comment.OriginalLine > 0 {
			ev.CommentLine = p.Comment.OriginalLine
		}
		ev.InReplyToID = p.Comment.InReplyToID
		if p.Comment.HTMLURL != "" {
			ev.HTMLURL = clampShort(p.Comment.HTMLURL)
		}
	}

	if p.Review != nil {
		ev.ReviewID = p.Review.ID
		ev.ReviewState = strings.ToLower(clampShort(p.Review.State))
		ev.ReviewBody = clampText(p.Review.Body, maxStoredTextField)
		if p.Review.CommitID != "" && ev.HeadSHA == "" {
			ev.HeadSHA = clampShort(p.Review.CommitID)
		}
		if p.Review.HTMLURL != "" {
			ev.HTMLURL = clampShort(p.Review.HTMLURL)
		}
	}

	if p.CheckRun != nil {
		ev.CheckName = clampShort(p.CheckRun.Name)
		ev.CheckStatus = clampShort(p.CheckRun.Status)
		ev.CheckConclusion = clampShort(p.CheckRun.Conclusion)
		if p.CheckRun.HeadSHA != "" {
			ev.HeadSHA = clampShort(p.CheckRun.HeadSHA)
		}
		if p.CheckRun.HTMLURL != "" {
			ev.HTMLURL = clampShort(p.CheckRun.HTMLURL)
		}
		ev.CheckRunID = p.CheckRun.ID
		ev.CheckApp = clampShort(p.CheckRun.App.Slug)
		ev.CheckStartedAt = p.CheckRun.StartedAt
		ev.CheckCompletedAt = p.CheckRun.CompletedAt
		ev.CheckOutput = checkOutputText(p.CheckRun.Output.Title, p.CheckRun.Output.Summary, p.CheckRun.Output.Text)
		if p.CheckRun.CheckSuite.HeadBranch != "" && ev.HeadRef == "" {
			ev.HeadRef = clampShort(p.CheckRun.CheckSuite.HeadBranch)
		}
	} else if p.WorkflowJob != nil {
		j := p.WorkflowJob
		ev.CheckRunID = j.ID
		if id := checkRunIDFromURL(j.CheckRunURL); id != 0 {
			ev.CheckRunID = id
		}
		ev.CheckName = clampShort(j.Name)
		ev.CheckStatus = clampShort(j.Status)
		ev.CheckConclusion = clampShort(j.Conclusion)
		ev.CheckApp = "github-actions"
		ev.CheckStartedAt = j.StartedAt
		ev.CheckCompletedAt = j.CompletedAt
		ev.WorkflowName = clampShort(j.WorkflowName)
		ev.WorkflowRun = j.RunID
		ev.RunAttempt = j.RunAttempt
		ev.HeadSHA = clampShort(j.HeadSHA)
		ev.HeadRef = clampShort(j.HeadBranch)
		ev.HTMLURL = clampShort(j.HTMLURL)
		for _, step := range j.Steps {
			if step.Status == "completed" && failedStepConclusion(step.Conclusion) {
				ev.FailingStep = clampShort(step.Name)
				break
			}
		}
	} else if p.WorkflowRun != nil {
		r := p.WorkflowRun
		ev.WorkflowRun = r.ID
		ev.RunAttempt = r.RunAttempt
		if ev.RunAttempt <= 0 {
			ev.RunAttempt = 1
		}
		ev.WorkflowName = clampShort(r.Name)
		ev.WorkflowPath = clampShort(r.Path)
		ev.WorkflowEvent = clampShort(r.Event)
		ev.CheckStatus = clampShort(r.Status)
		ev.CheckConclusion = clampShort(r.Conclusion)
		ev.CheckApp = "github-actions"
		ev.CheckStartedAt = r.RunStartedAt
		ev.CheckCompletedAt = r.UpdatedAt
		ev.HeadSHA = clampShort(r.HeadSHA)
		ev.HeadRef = clampShort(r.HeadBranch)
		if r.HeadRepository != nil {
			ev.HeadRepo = clampShort(r.HeadRepository.FullName)
		}
		ev.HTMLURL = clampShort(r.HTMLURL)
	} else if p.CheckSuite != nil {
		ev.CheckStatus = clampShort(p.CheckSuite.Status)
		ev.CheckConclusion = clampShort(p.CheckSuite.Conclusion)
		if p.CheckSuite.HeadBranch != "" && ev.HeadRef == "" {
			ev.HeadRef = clampShort(p.CheckSuite.HeadBranch)
		}
		if p.CheckSuite.HeadSHA != "" && ev.HeadSHA == "" {
			ev.HeadSHA = clampShort(p.CheckSuite.HeadSHA)
		}
	}

	switch eventName {
	case "push":
		if p.After != "" && ev.HeadSHA == "" {
			ev.HeadSHA = clampShort(p.After)
		}
		if branch, ok := strings.CutPrefix(p.Ref, "refs/heads/"); ok && ev.HeadRef == "" {
			ev.HeadRef = clampShort(branch)
		}
	case "status":
		if p.SHA != "" && ev.HeadSHA == "" {
			ev.HeadSHA = clampShort(p.SHA)
		}
		if p.Context != "" && ev.CheckName == "" {
			ev.CheckName = clampShort(p.Context)
		}
		if p.State != "" && ev.CheckConclusion == "" {
			ev.CheckConclusion = clampShort(p.State)
		}
	}

	return ev, nil
}

// checkOutputText joins a check run's output fields into one bounded
// text, title first.
func checkOutputText(parts ...*string) string {
	var kept []string
	for _, p := range parts {
		if p != nil && strings.TrimSpace(*p) != "" {
			kept = append(kept, strings.TrimSpace(*p))
		}
	}
	out := strings.Join(kept, "\n\n")
	if len(out) > maxCheckOutput {
		out = out[len(out)-maxCheckOutput:]
		for len(out) > 0 && !utf8.ValidString(out) {
			out = out[1:]
		}
	}
	return out
}

// checkRunIDFromURL is the id at the end of a check_run_url
// (".../check-runs/123"), or 0.
func checkRunIDFromURL(u string) int64 {
	i := strings.LastIndex(u, "/check-runs/")
	if i < 0 {
		return 0
	}
	var id int64
	for _, c := range u[i+len("/check-runs/"):] {
		if c < '0' || c > '9' {
			return 0
		}
		id = id*10 + int64(c-'0')
	}
	return id
}

// failedStepConclusion is a step conclusion that broke its job.
func failedStepConclusion(c string) bool {
	return c == "failure" || c == "timed_out"
}

// RepoSplit splits an "owner/name" repository identifier into its two parts.
func RepoSplit(full string) (owner, name string, ok bool) {
	owner, name, ok = strings.Cut(strings.TrimSpace(full), "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", false
	}
	return owner, name, true
}

func clampShort(s string) string {
	return clampText(strings.TrimSpace(s), maxShortField)
}

func clampText(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	b := s[:maxBytes]
	for len(b) > 0 && !utf8.ValidString(b) {
		b = b[:len(b)-1]
	}
	return b
}

type rawWebhookPayload struct {
	Action     string `json:"action"`
	Number     int    `json:"number"`
	Ref        string `json:"ref"`
	After      string `json:"after"`
	SHA        string `json:"sha"`
	State      string `json:"state"`
	Context    string `json:"context"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Sender struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
		Type  string `json:"type"`
	} `json:"sender"`
	RequestedReviewer *struct {
		Login string `json:"login"`
	} `json:"requested_reviewer"`
	Label *struct {
		Name string `json:"name"`
	} `json:"label"`
	PullRequest *struct {
		Number         int    `json:"number"`
		Title          string `json:"title"`
		Body           string `json:"body"`
		State          string `json:"state"`
		Draft          bool   `json:"draft"`
		Merged         bool   `json:"merged"`
		MergeCommitSHA string `json:"merge_commit_sha"`
		HTMLURL        string `json:"html_url"`
		User           struct {
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
			Ref  string `json:"ref"`
			SHA  string `json:"sha"`
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	} `json:"pull_request"`
	Issue *struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		State   string `json:"state"`
		HTMLURL string `json:"html_url"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
		PullRequest json.RawMessage `json:"pull_request"`
	} `json:"issue"`
	Comment *struct {
		ID           int64  `json:"id"`
		Body         string `json:"body"`
		Path         string `json:"path"`
		Line         int    `json:"line"`
		OriginalLine int    `json:"original_line"`
		InReplyToID  int64  `json:"in_reply_to_id"`
		HTMLURL      string `json:"html_url"`
	} `json:"comment"`
	Review *struct {
		ID       int64  `json:"id"`
		State    string `json:"state"`
		Body     string `json:"body"`
		CommitID string `json:"commit_id"`
		HTMLURL  string `json:"html_url"`
	} `json:"review"`
	CheckRun *struct {
		ID          int64      `json:"id"`
		Name        string     `json:"name"`
		HeadSHA     string     `json:"head_sha"`
		Status      string     `json:"status"`
		Conclusion  string     `json:"conclusion"`
		HTMLURL     string     `json:"html_url"`
		StartedAt   *time.Time `json:"started_at"`
		CompletedAt *time.Time `json:"completed_at"`
		App         struct {
			Slug string `json:"slug"`
		} `json:"app"`
		Output struct {
			Title   *string `json:"title"`
			Summary *string `json:"summary"`
			Text    *string `json:"text"`
		} `json:"output"`
		CheckSuite struct {
			HeadBranch string `json:"head_branch"`
		} `json:"check_suite"`
	} `json:"check_run"`
	WorkflowJob *struct {
		ID           int64      `json:"id"`
		RunID        int64      `json:"run_id"`
		RunAttempt   int        `json:"run_attempt"`
		WorkflowName string     `json:"workflow_name"`
		HeadBranch   string     `json:"head_branch"`
		HeadSHA      string     `json:"head_sha"`
		Name         string     `json:"name"`
		Status       string     `json:"status"`
		Conclusion   string     `json:"conclusion"`
		StartedAt    *time.Time `json:"started_at"`
		CompletedAt  *time.Time `json:"completed_at"`
		HTMLURL      string     `json:"html_url"`
		CheckRunURL  string     `json:"check_run_url"`
		Steps        []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"steps"`
	} `json:"workflow_job"`
	WorkflowRun *struct {
		ID             int64      `json:"id"`
		Name           string     `json:"name"`
		Path           string     `json:"path"`
		Event          string     `json:"event"`
		RunAttempt     int        `json:"run_attempt"`
		HeadBranch     string     `json:"head_branch"`
		HeadSHA        string     `json:"head_sha"`
		Status         string     `json:"status"`
		Conclusion     string     `json:"conclusion"`
		HTMLURL        string     `json:"html_url"`
		RunStartedAt   *time.Time `json:"run_started_at"`
		UpdatedAt      *time.Time `json:"updated_at"`
		HeadRepository *struct {
			FullName string `json:"full_name"`
		} `json:"head_repository"`
	} `json:"workflow_run"`
	CheckSuite *struct {
		HeadBranch string `json:"head_branch"`
		HeadSHA    string `json:"head_sha"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	} `json:"check_suite"`
}
