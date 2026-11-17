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
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// MaxActionsRunsLimit bounds how many workflow runs one ListActionsRuns call
// pages through (five 100-run GitHub pages).
const MaxActionsRunsLimit = 500

// ActionsRunsQuery narrows ListActionsRuns. Every field is optional.
type ActionsRunsQuery struct {
	// Branch keeps runs whose head branch is this one exactly -- GitHub's
	// own ?branch=, which matches no other way.
	Branch string
	// Workflow narrows to one workflow file (such as "nightly.yml",
	// ".github/workflows/nightly.yml", or "nightly") via GitHub's
	// per-workflow runs endpoint when that workflow file exists on the
	// forge, falling back to the repository-wide runs endpoint on 404.
	Workflow string
	// Since keeps runs created at or after it (?created=>=...).
	Since time.Time
	// Until keeps runs created before it (?created=<..., or with Since
	// the range ?created=since..until, whose inclusive end a caller
	// filtering by Until itself drops again). It is what lets a listing
	// reach a window older than the newest MaxActionsRunsLimit runs:
	// without it the forge pages from now backwards and stops there.
	Until time.Time
	// Limit is the maximum number of runs to return across pages, at most
	// MaxActionsRunsLimit (500); zero is 30.
	Limit int
}

// ActionsRunsPage is the runs ListActionsRunsPage fetched across pages plus
// GitHub's own total_count for the server-side query.
type ActionsRunsPage struct {
	Runs             []WorkflowRun
	TotalCount       int
	WorkflowFiltered bool
	// NotFound is a listing the forge answered with a 404 before any page
	// came back: Actions off, or the path not served by that forge or to
	// this credential. It is still no runs, but a caller can say why.
	NotFound bool
}

// ActionsPageReader is an optional extension of ActionsReader that reports
// GitHub's total_count alongside the paged runs.
type ActionsPageReader interface {
	ListActionsRunsPage(owner, repo string, q ActionsRunsQuery) (ActionsRunsPage, error)
}

// ActionsRunJob is one job of a workflow run with its steps, as
// GET /repos/{owner}/{repo}/actions/runs/{run_id}/jobs gives it.
type ActionsRunJob struct {
	ID         int64  `json:"id"`
	RunID      int64  `json:"runId"`
	RunAttempt int    `json:"runAttempt,omitempty"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
	// WorkflowName, HeadSHA and CreatedAt are the run's, as GitHub repeats
	// them on each of its jobs.
	WorkflowName string           `json:"workflowName,omitempty"`
	HeadSHA      string           `json:"headSha,omitempty"`
	CreatedAt    *time.Time       `json:"createdAt,omitempty"`
	StartedAt    *time.Time       `json:"startedAt,omitempty"`
	CompletedAt  *time.Time       `json:"completedAt,omitempty"`
	HTMLURL      string           `json:"htmlUrl,omitempty"`
	Steps        []ActionsRunStep `json:"steps,omitempty"`
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

var (
	_ ActionsReader     = (*RESTClient)(nil)
	_ ActionsPageReader = (*RESTClient)(nil)
)

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

// ListActionsRuns returns up to q.Limit of the repository's workflow runs,
// newest first, following GitHub's pagination up to MaxActionsRunsLimit. A 404
// (Actions off, or not readable with this credential) is no runs rather than
// an error, as it is for CompletedWorkflowRuns.
func (c *RESTClient) ListActionsRuns(owner, repo string, q ActionsRunsQuery) ([]WorkflowRun, error) {
	page, err := c.ListActionsRunsPage(owner, repo, q)
	return page.Runs, err
}

// ListActionsRunsPage is ListActionsRuns plus GitHub's total_count for the
// server-side query. When q.Workflow names a workflow file (or stem), it
// queries /repos/{owner}/{repo}/actions/workflows/{workflow_id}/runs first so
// low-frequency scheduled workflows are not buried behind hundreds of PR runs.
func (c *RESTClient) ListActionsRunsPage(owner, repo string, q ActionsRunsQuery) (ActionsRunsPage, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 30
	}
	if limit > MaxActionsRunsLimit {
		limit = MaxActionsRunsLimit
	}
	perPage := min(limit, 100)
	query := url.Values{"per_page": {strconv.Itoa(perPage)}}
	if b := strings.TrimSpace(q.Branch); b != "" {
		query.Set("branch", b)
	}
	switch {
	case !q.Since.IsZero() && !q.Until.IsZero():
		query.Set("created", q.Since.UTC().Format(time.RFC3339)+".."+q.Until.UTC().Format(time.RFC3339))
	case !q.Since.IsZero():
		query.Set("created", ">="+q.Since.UTC().Format(time.RFC3339))
	case !q.Until.IsZero():
		query.Set("created", "<"+q.Until.UTC().Format(time.RFC3339))
	}
	for _, wf := range actionsWorkflowFileCandidates(q.Workflow) {
		p := fmt.Sprintf("/repos/%s/%s/actions/workflows/%s/runs?%s", owner, repo, url.PathEscape(wf), query.Encode())
		page, found, err := c.fetchActionsRunsPages(owner, repo, p, limit)
		if err != nil {
			return ActionsRunsPage{}, err
		}
		if found {
			page.WorkflowFiltered = true
			return page, nil
		}
	}
	page, found, err := c.fetchActionsRunsPages(owner, repo, fmt.Sprintf("/repos/%s/%s/actions/runs?%s", owner, repo, query.Encode()), limit)
	if err == nil && !found {
		page.NotFound = true
	}
	return page, err
}

func (c *RESTClient) fetchActionsRunsPages(owner, repo, startPath string, limit int) (ActionsRunsPage, bool, error) {
	var runs []WorkflowRun
	var totalCount int
	var gotTotal bool
	for p := startPath; p != "" && len(runs) < limit; {
		resp, err := c.get(owner, repo, p)
		if err != nil {
			return ActionsRunsPage{}, false, err
		}
		switch resp.Status {
		case http.StatusOK:
		case http.StatusNotFound:
			if !gotTotal {
				return ActionsRunsPage{}, false, nil
			}
			return ActionsRunsPage{Runs: runs, TotalCount: max(totalCount, len(runs))}, true, nil
		default:
			return ActionsRunsPage{}, false, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
		}
		var data struct {
			TotalCount   int               `json:"total_count"`
			WorkflowRuns []workflowRunJSON `json:"workflow_runs"`
		}
		if err := json.Unmarshal(resp.Body, &data); err != nil {
			return ActionsRunsPage{}, false, err
		}
		if !gotTotal {
			totalCount = data.TotalCount
			gotTotal = true
		}
		for _, r := range data.WorkflowRuns {
			runs = append(runs, r.run())
			if len(runs) >= limit {
				break
			}
		}
		if len(runs) >= limit || len(data.WorkflowRuns) == 0 {
			break
		}
		p = nextPagePath(resp.Headers["Link"])
	}
	return ActionsRunsPage{Runs: runs, TotalCount: max(totalCount, len(runs))}, gotTotal, nil
}

// actionsWorkflowFileCandidates turns a workflow selector ("nightly.yml",
// ".github/workflows/nightly.yml", "nightly", or a numeric workflow id) into
// the workflow_id path segment(s) GitHub's per-workflow runs endpoint accepts.
// Display names with spaces or non-Actions paths return nil so the caller
// falls back to /actions/runs.
func actionsWorkflowFileCandidates(workflow string) []string {
	w := strings.TrimSpace(workflow)
	if w == "" || strings.ContainsAny(w, " \t\r\n") {
		return nil
	}
	clean := path.Clean(w)
	if strings.HasPrefix(clean, ".grain/") {
		return nil
	}
	clean = strings.TrimPrefix(clean, ".github/workflows/")
	if clean == "" || clean == "." || strings.Contains(clean, "/") {
		return nil
	}
	lower := strings.ToLower(clean)
	if strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".yaml") || isDigitsOnly(clean) {
		return []string{clean}
	}
	return []string{clean + ".yml", clean + ".yaml"}
}

func isDigitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
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
	return c.listActionsJobs(owner, repo, runID, "")
}

// ActionsJobAttemptsReader is the optional Client capability for every
// attempt of every job of a run (GitHub's filter=all), where
// ListActionsJobs is each job's latest attempt alone. It is what the
// release train's qualification gate reads: a job that failed and
// passed on its retry is a flake only when both attempts are in hand.
type ActionsJobAttemptsReader interface {
	ListActionsJobAttempts(owner, repo string, runID int64) ([]ActionsRunJob, error)
}

var _ ActionsJobAttemptsReader = (*RESTClient)(nil)

// ListActionsJobAttempts is GET
// /repos/{owner}/{repo}/actions/runs/{run_id}/jobs?filter=all.
func (c *RESTClient) ListActionsJobAttempts(owner, repo string, runID int64) ([]ActionsRunJob, error) {
	return c.listActionsJobs(owner, repo, runID, "&filter=all")
}

func (c *RESTClient) listActionsJobs(owner, repo string, runID int64, query string) ([]ActionsRunJob, error) {
	if runID <= 0 {
		return nil, nil
	}
	var out []ActionsRunJob
	path := fmt.Sprintf("/repos/%s/%s/actions/runs/%d/jobs?per_page=100%s", owner, repo, runID, query)
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
				ID           int64      `json:"id"`
				RunID        int64      `json:"run_id"`
				RunAttempt   int        `json:"run_attempt"`
				WorkflowName string     `json:"workflow_name"`
				HeadSHA      string     `json:"head_sha"`
				Name         string     `json:"name"`
				Status       string     `json:"status"`
				Conclusion   string     `json:"conclusion"`
				CreatedAt    *time.Time `json:"created_at"`
				StartedAt    *time.Time `json:"started_at"`
				CompletedAt  *time.Time `json:"completed_at"`
				HTMLURL      string     `json:"html_url"`
				Steps        []struct {
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
				WorkflowName: job.WorkflowName, HeadSHA: job.HeadSHA,
				Status: job.Status, Conclusion: job.Conclusion, CreatedAt: job.CreatedAt,
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
// GitHub -- github.com and an Enterprise Server alike -- answers it with
// a redirect to a short-lived blob URL, usually on another host.
// RealTransport's http.Client follows it, dropping the Authorization
// header on the way as net/http does for a different host, and so does a
// ForgeViaCommand host's tool (`<tool> api` follows redirects itself; a
// redirect it did hand back would be re-sent through the tool as a whole
// URL, CommandForgeTransport's rule for a URL outside the API base).
// A transport that hands the redirect back instead (a Client whose
// CheckRedirect stops it, a fake) has it followed here, once, without
// the credential: the blob URL is signed and wants none.
func (c *RESTClient) ActionsJobLog(owner, repo string, jobID int64) (string, error) {
	resp, err := c.get(owner, repo, fmt.Sprintf("/repos/%s/%s/actions/jobs/%d/logs", owner, repo, jobID))
	if err != nil {
		return "", redactURLError(err)
	}
	if loc := headerValue(resp.Headers, "Location"); resp.Status >= 300 && resp.Status < 400 && loc != "" {
		blob, err := c.Transport.Request(http.MethodGet, loc, map[string]string{"User-Agent": "grain-automation"}, nil)
		if err != nil {
			return "", fmt.Errorf("following the job log's redirect to %s: %w", hostOf(loc), redactURLError(err))
		}
		// Named as the transport names a redirect it followed itself: the
		// API request, then the blob host -- never the signed URL, whose
		// query is the credential.
		if resp.URL != "" {
			blob.URL = resp.URL + " -> " + hostOf(loc)
		} else {
			blob.URL = "GET " + hostOf(loc)
		}
		resp = blob
	}
	if resp.Status != http.StatusOK {
		return "", &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	if len(resp.Body) == 0 {
		// A 200 with nothing in it is not a log, and saying only that it
		// was empty leaves nobody able to tell which hop lost it: the
		// forge, the blob store it redirected to, or the transport in
		// between. So it is an error carrying the status, the hosts, and
		// the headers that say who answered.
		return "", fmt.Errorf("%w: %w", ErrEmptyJobLog,
			&Error{Status: resp.Status, URL: resp.URL, Note: answeredBy(resp.Headers)})
	}
	return strings.ToValidUTF8(string(resp.Body), ""), nil
}

// ErrEmptyJobLog is ActionsJobLog's error for a job log answered 200 with
// no body. It wraps an *Error naming the status, the host or hosts the
// answer came from, and what the answer's headers say about who sent it.
var ErrEmptyJobLog = errors.New("the forge answered the job log with no bytes at all")

// answeredBy is what an answer's headers say about who sent it -- its
// content type, server and any proxy it passed -- for an error about an
// answer whose body says nothing ("content-type text/plain, server
// blobstore"). Empty when the headers name none of them.
func answeredBy(headers map[string]string) string {
	var parts []string
	for _, name := range []string{"Content-Type", "Server", "Via"} {
		if v := strings.TrimSpace(headerValue(headers, name)); v != "" && !strings.ContainsAny(v, "\r\n") {
			if len(v) > 80 {
				v = v[:80]
			}
			parts = append(parts, strings.ToLower(name)+" "+v)
		}
	}
	return strings.Join(parts, ", ")
}

// redactURLError cuts a *url.Error's URL down to its scheme and host. A
// transport failure on the way to a job log -- net/http following the
// forge's redirect, or this package following it itself -- names the
// signed blob URL it was fetching, and that error reaches the agent
// asking for the log.
func redactURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		ue.URL = hostOf(ue.URL)
	}
	return err
}

// hostOf is the scheme and host of a URL, leaving out a signed URL's
// path and query.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "the URL it named"
	}
	return u.Scheme + "://" + u.Host
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
