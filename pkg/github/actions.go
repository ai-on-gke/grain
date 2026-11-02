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

package github

// actions.go is GitHub Actions read the way an agent asks about it: a
// repository's workflow runs, one run, its jobs and their steps, one
// job's log and a run's artifacts. pkg/ui serves them under ci:read beside
// grain's own CI runs (GET /api/ci/runs and below it), so an agent can
// say whether the latest release.yml on main passed without a credential
// of its own: every call here goes through the daemon's own forge
// connection, the App or token for github.com and the host's own tool
// (ForgeViaCommand) for a GitHub Enterprise host.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ActionsRunsQuery narrows ListActionsRuns. Every field is optional.
type ActionsRunsQuery struct {
	// Branch keeps runs whose head branch is this one exactly -- GitHub's
	// own ?branch=, which matches no other way.
	Branch string
	// Since keeps runs created at or after it (?created=>=...).
	Since time.Time
	// Limit is the page size, at most 100 (GitHub's own bound); zero is 30.
	Limit int
}

// ActionsRunJob is one job of a workflow run with its steps, as
// GET /repos/{owner}/{repo}/actions/runs/{run_id}/jobs gives it.
type ActionsRunJob struct {
	ID          int64            `json:"id"`
	RunID       int64            `json:"runId"`
	RunAttempt  int              `json:"runAttempt,omitempty"`
	Name        string           `json:"name"`
	Status      string           `json:"status"`
	Conclusion  string           `json:"conclusion,omitempty"`
	StartedAt   *time.Time       `json:"startedAt,omitempty"`
	CompletedAt *time.Time       `json:"completedAt,omitempty"`
	HTMLURL     string           `json:"htmlUrl,omitempty"`
	Steps       []ActionsRunStep `json:"steps,omitempty"`
}

// ActionsRunStep is one step of an ActionsRunJob.
type ActionsRunStep struct {
	Number      int        `json:"number"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion,omitempty"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
}

// ActionsArtifact is one artifact a workflow run uploaded.
type ActionsArtifact struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	Bytes     int64      `json:"bytes"`
	Expired   bool       `json:"expired,omitempty"`
	CreatedAt *time.Time `json:"createdAt,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

// ActionsReader is the optional Client capability ci:read's GitHub
// Actions routes are served by. A client without it -- a test's fake, a
// forge that is not GitHub -- leaves those routes answering from grain's
// own polled record (model.GitHubCIJob) alone.
type ActionsReader interface {
	ListActionsRuns(owner, repo string, q ActionsRunsQuery) ([]WorkflowRun, error)
	// GetActionsRun is one run, or nil with no error for a run GitHub
	// does not have (404).
	GetActionsRun(owner, repo string, runID int64) (*WorkflowRun, error)
	ListActionsJobs(owner, repo string, runID int64) ([]ActionsRunJob, error)
	// ActionsJobLog is one job's whole log, plain text. A log GitHub will
	// not serve (past its retention window, or a credential without
	// actions:read) is an *Error carrying GitHub's status.
	ActionsJobLog(owner, repo string, jobID int64) (string, error)
	ListActionsArtifacts(owner, repo string, runID int64) ([]ActionsArtifact, error)
}

var _ ActionsReader = (*RESTClient)(nil)

// workflowRunJSON is a workflow run as the REST API spells it.
type workflowRunJSON struct {
	WorkflowRun
	HeadRepository *struct {
		FullName string `json:"full_name"`
	} `json:"head_repository"`
}

func (r workflowRunJSON) run() WorkflowRun {
	out := r.WorkflowRun
	if out.RunAttempt <= 0 {
		out.RunAttempt = 1
	}
	if r.HeadRepository != nil && out.HeadRepo == "" {
		out.HeadRepo = strings.TrimSpace(r.HeadRepository.FullName)
	}
	return out
}

// ListActionsRuns is one page of the repository's workflow runs, newest
// first. A 404 (Actions off, or not readable with this credential) is no
// runs rather than an error, as it is for CompletedWorkflowRuns.
func (c *RESTClient) ListActionsRuns(owner, repo string, q ActionsRunsQuery) ([]WorkflowRun, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	query := url.Values{"per_page": {strconv.Itoa(limit)}}
	if b := strings.TrimSpace(q.Branch); b != "" {
		query.Set("branch", b)
	}
	if !q.Since.IsZero() {
		query.Set("created", ">="+q.Since.UTC().Format(time.RFC3339))
	}
	resp, err := c.get(owner, repo, fmt.Sprintf("/repos/%s/%s/actions/runs?%s", owner, repo, query.Encode()))
	if err != nil {
		return nil, err
	}
	switch resp.Status {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, nil
	default:
		return nil, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var data struct {
		WorkflowRuns []workflowRunJSON `json:"workflow_runs"`
	}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return nil, err
	}
	runs := make([]WorkflowRun, 0, len(data.WorkflowRuns))
	for _, r := range data.WorkflowRuns {
		runs = append(runs, r.run())
	}
	return runs, nil
}

// GetActionsRun is GET /repos/{owner}/{repo}/actions/runs/{run_id}.
func (c *RESTClient) GetActionsRun(owner, repo string, runID int64) (*WorkflowRun, error) {
	if runID <= 0 {
		return nil, nil
	}
	resp, err := c.get(owner, repo, fmt.Sprintf("/repos/%s/%s/actions/runs/%d", owner, repo, runID))
	if err != nil {
		return nil, err
	}
	switch resp.Status {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, nil
	default:
		return nil, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var data workflowRunJSON
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return nil, err
	}
	run := data.run()
	return &run, nil
}

// ListActionsJobs is every job of the run's latest attempt, steps and
// all, following GitHub's pagination.
func (c *RESTClient) ListActionsJobs(owner, repo string, runID int64) ([]ActionsRunJob, error) {
	if runID <= 0 {
		return nil, nil
	}
	var out []ActionsRunJob
	path := fmt.Sprintf("/repos/%s/%s/actions/runs/%d/jobs?per_page=100", owner, repo, runID)
	for path != "" {
		resp, err := c.get(owner, repo, path)
		if err != nil {
			return nil, err
		}
		if resp.Status == http.StatusNotFound {
			return out, nil
		}
		if resp.Status != http.StatusOK {
			return nil, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
		}
		var data struct {
			Jobs []struct {
				ID          int64      `json:"id"`
				RunID       int64      `json:"run_id"`
				RunAttempt  int        `json:"run_attempt"`
				Name        string     `json:"name"`
				Status      string     `json:"status"`
				Conclusion  string     `json:"conclusion"`
				StartedAt   *time.Time `json:"started_at"`
				CompletedAt *time.Time `json:"completed_at"`
				HTMLURL     string     `json:"html_url"`
				Steps       []struct {
					Number      int        `json:"number"`
					Name        string     `json:"name"`
					Status      string     `json:"status"`
					Conclusion  string     `json:"conclusion"`
					StartedAt   *time.Time `json:"started_at"`
					CompletedAt *time.Time `json:"completed_at"`
				} `json:"steps"`
			} `json:"jobs"`
		}
		if err := json.Unmarshal(resp.Body, &data); err != nil {
			return nil, err
		}
		for _, job := range data.Jobs {
			j := ActionsRunJob{
				ID: job.ID, RunID: job.RunID, RunAttempt: job.RunAttempt, Name: job.Name,
				Status: job.Status, Conclusion: job.Conclusion,
				StartedAt: job.StartedAt, CompletedAt: job.CompletedAt, HTMLURL: job.HTMLURL,
			}
			if j.RunID == 0 {
				j.RunID = runID
			}
			for _, step := range job.Steps {
				j.Steps = append(j.Steps, ActionsRunStep{
					Number: step.Number, Name: step.Name, Status: step.Status, Conclusion: step.Conclusion,
					StartedAt: step.StartedAt, CompletedAt: step.CompletedAt,
				})
			}
			out = append(out, j)
		}
		path = nextPagePath(resp.Headers["Link"])
	}
	return out, nil
}

// ActionsJobLog is GET /repos/{owner}/{repo}/actions/jobs/{job_id}/logs.
//
// GitHub answers it with a redirect to a short-lived blob URL on another
// host. RealTransport's http.Client follows it, dropping the
// Authorization header on the way as net/http does for a different host,
// and so does a ForgeViaCommand host's tool (`<tool> api` follows
// redirects itself; a redirect it did hand back would be re-sent through
// the tool as a whole URL, CommandForgeTransport's rule for a URL outside
// the API base). Either way what comes back here is the log.
func (c *RESTClient) ActionsJobLog(owner, repo string, jobID int64) (string, error) {
	resp, err := c.get(owner, repo, fmt.Sprintf("/repos/%s/%s/actions/jobs/%d/logs", owner, repo, jobID))
	if err != nil {
		return "", err
	}
	if resp.Status != http.StatusOK {
		return "", &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	return strings.ToValidUTF8(string(resp.Body), ""), nil
}

// ListActionsArtifacts is GET /repos/{owner}/{repo}/actions/runs/{run_id}/artifacts.
func (c *RESTClient) ListActionsArtifacts(owner, repo string, runID int64) ([]ActionsArtifact, error) {
	if runID <= 0 {
		return nil, nil
	}
	var out []ActionsArtifact
	path := fmt.Sprintf("/repos/%s/%s/actions/runs/%d/artifacts?per_page=100", owner, repo, runID)
	for path != "" {
		resp, err := c.get(owner, repo, path)
		if err != nil {
			return nil, err
		}
		if resp.Status == http.StatusNotFound {
			return out, nil
		}
		if resp.Status != http.StatusOK {
			return nil, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
		}
		var data struct {
			Artifacts []struct {
				ID          int64      `json:"id"`
				Name        string     `json:"name"`
				SizeInBytes int64      `json:"size_in_bytes"`
				Expired     bool       `json:"expired"`
				CreatedAt   *time.Time `json:"created_at"`
				ExpiresAt   *time.Time `json:"expires_at"`
			} `json:"artifacts"`
		}
		if err := json.Unmarshal(resp.Body, &data); err != nil {
			return nil, err
		}
		for _, a := range data.Artifacts {
			out = append(out, ActionsArtifact{ID: a.ID, Name: a.Name, Bytes: a.SizeInBytes, Expired: a.Expired,
				CreatedAt: a.CreatedAt, ExpiresAt: a.ExpiresAt})
		}
		path = nextPagePath(resp.Headers["Link"])
	}
	return out, nil
}
