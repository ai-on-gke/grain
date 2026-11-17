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

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const maxCommitStatusDescription = 140

// CommitStatus is one commit status on a commit -- the older API beside
// check runs, which a CI system that is not GitHub Actions or a
// deployment whose forge credential is a PAT (rather than a GitHub App)
// posts its verdicts as.
type CommitStatus struct {
	ID          int64
	Context     string
	State       string // "success", "failure", "error" or "pending"
	Description string
	TargetURL   string
	Creator     string // GitHub login of creator (populated by ListCommitStatuses)
	CreatedAt   *time.Time
	UpdatedAt   *time.Time
}

// CommitStatusWrite is the payload for POST /repos/{owner}/{repo}/statuses/{sha}.
// GitHub rejects descriptions longer than 140 characters with HTTP 422, so
// CreateCommitStatus collapses whitespace and truncates Description to fit.
type CommitStatusWrite struct {
	State       string // "pending", "success", "failure" or "error"
	Context     string
	Description string
	TargetURL   string
}

// CommitStatusReader is the optional half of Client that reads a
// commit's combined status: what the release-qualification reconciler
// reads a suite's Stage 1 from when it is declared as a status rather
// than as a workflow run (orchestrator/releasequalification.go).
type CommitStatusReader interface {
	CommitStatuses(owner, repo, ref string) ([]CommitStatus, error)
}

// CommitStatusLister is the optional half of Client that lists every
// commit status posted against ref, newest first, including each entry's
// creator login (GET /repos/{owner}/{repo}/commits/{ref}/statuses).
// Unlike CommitStatusReader's combined status endpoint, this returns the
// creator so a caller can trust only its own statuses even when another
// account posts under the same context.
type CommitStatusLister interface {
	ListCommitStatuses(owner, repo, ref string) ([]CommitStatus, error)
}

// CommitStatusWriter is the optional half of Client that posts a commit
// status on sha (POST /repos/{owner}/{repo}/statuses/{sha}). Unlike the
// Checks API, a personal access token with commit-status write permission
// can post commit statuses.
type CommitStatusWriter interface {
	CreateCommitStatus(owner, repo, sha string, status CommitStatusWrite) error
}

// CommitStatusIdentityReader is the optional half of Client that knows
// which login its own commit statuses on owner/repo are posted as.
type CommitStatusIdentityReader interface {
	StatusLogin(owner, repo string) (string, error)
}

var (
	_ CommitStatusReader         = (*RESTClient)(nil)
	_ CommitStatusLister         = (*RESTClient)(nil)
	_ CommitStatusWriter         = (*RESTClient)(nil)
	_ CommitStatusIdentityReader = (*RESTClient)(nil)
)

// CommitStatuses is GET /repos/{owner}/{repo}/commits/{ref}/status: the
// combined status, which already holds only the newest status of each
// context. A ref GitHub does not know answers no statuses.
func (c *RESTClient) CommitStatuses(owner, repo, ref string) ([]CommitStatus, error) {
	var out []CommitStatus
	path := fmt.Sprintf("/repos/%s/%s/commits/%s/status?per_page=100", owner, repo, url.PathEscape(ref))
	for path != "" {
		resp, err := c.get(owner, repo, path)
		if err != nil {
			return nil, err
		}
		if resp.Status == 404 || resp.Status == 422 {
			return out, nil
		}
		if resp.Status != 200 {
			return nil, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
		}
		var data struct {
			Statuses []struct {
				Context     string `json:"context"`
				State       string `json:"state"`
				Description string `json:"description"`
				TargetURL   string `json:"target_url"`
			} `json:"statuses"`
		}
		if err := json.Unmarshal(resp.Body, &data); err != nil {
			return nil, err
		}
		for _, s := range data.Statuses {
			out = append(out, CommitStatus{Context: s.Context, State: s.State, Description: s.Description, TargetURL: s.TargetURL})
		}
		path = nextPagePath(resp.Headers["Link"])
	}
	return out, nil
}

// ListCommitStatuses is GET /repos/{owner}/{repo}/commits/{ref}/statuses:
// every commit status posted against ref, newest first, with each entry's
// creator login. Like ListCheckRuns, non-200 responses (including 404 when
// the ref is unknown or the credential has lost access to the repository)
// return *Error so callers such as grain/rc-hold fail closed rather than
// reading a missing permission as an empty status list.
func (c *RESTClient) ListCommitStatuses(owner, repo, ref string) ([]CommitStatus, error) {
	var out []CommitStatus
	path := fmt.Sprintf("/repos/%s/%s/commits/%s/statuses?per_page=100", owner, repo, url.PathEscape(ref))
	for path != "" {
		resp, err := c.get(owner, repo, path)
		if err != nil {
			return nil, err
		}
		if resp.Status != 200 {
			return nil, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
		}
		var data []struct {
			ID          int64      `json:"id"`
			Context     string     `json:"context"`
			State       string     `json:"state"`
			Description string     `json:"description"`
			TargetURL   string     `json:"target_url"`
			CreatedAt   *time.Time `json:"created_at"`
			UpdatedAt   *time.Time `json:"updated_at"`
			Creator     *struct {
				Login string `json:"login"`
			} `json:"creator"`
		}
		if err := json.Unmarshal(resp.Body, &data); err != nil {
			return nil, err
		}
		for _, s := range data {
			var creator string
			if s.Creator != nil {
				creator = s.Creator.Login
			}
			out = append(out, CommitStatus{
				ID:          s.ID,
				Context:     s.Context,
				State:       s.State,
				Description: s.Description,
				TargetURL:   s.TargetURL,
				Creator:     creator,
				CreatedAt:   s.CreatedAt,
				UpdatedAt:   s.UpdatedAt,
			})
		}
		path = nextPagePath(resp.Headers["Link"])
	}
	return out, nil
}

// CreateCommitStatus posts a commit status on sha via
// POST /repos/{owner}/{repo}/statuses/{sha}.
func (c *RESTClient) CreateCommitStatus(owner, repo, sha string, status CommitStatusWrite) error {
	m := map[string]string{
		"state": status.State,
	}
	if status.Context != "" {
		m["context"] = status.Context
	}
	if desc := TruncateCommitStatusDescription(status.Description); desc != "" {
		m["description"] = desc
	}
	if status.TargetURL != "" {
		m["target_url"] = status.TargetURL
	}
	payload, err := json.Marshal(m)
	if err != nil {
		return err
	}
	resp, err := c.request(
		"POST", fmt.Sprintf("/repos/%s/%s/statuses/%s", owner, repo, url.PathEscape(sha)),
		c.headers(owner, repo, true), payload,
	)
	if err != nil {
		return err
	}
	if resp.Status != http.StatusCreated && resp.Status != http.StatusOK {
		return &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	return nil
}

// TruncateCommitStatusDescription normalizes whitespace and truncates s to
// GitHub's 140-character commit-status description limit.
func TruncateCommitStatusDescription(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= maxCommitStatusDescription {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxCommitStatusDescription-3]) + "..."
}

// PathCommitLister is the optional half of Client that lists the commits
// on a ref that changed one path: what the release-qualification
// reconciler finds a downstream repository's go.mod syncs with.
type PathCommitLister interface {
	ListPathCommits(owner, repo, ref, filePath string) ([]Commit, error)
}

var _ PathCommitLister = (*RESTClient)(nil)

// ListPathCommits is GET /repos/{owner}/{repo}/commits?sha={ref}&path={path}:
// the newest commits reachable from ref that changed filePath, newest
// first. One page, deliberately, as ListTags is: the caller only ever
// wants the newest few, and runs on a timer.
func (c *RESTClient) ListPathCommits(owner, repo, ref, filePath string) ([]Commit, error) {
	path := fmt.Sprintf("/repos/%s/%s/commits?sha=%s&path=%s&per_page=100",
		owner, repo, url.QueryEscape(ref), url.QueryEscape(filePath))
	resp, err := c.get(owner, repo, path)
	if err != nil {
		return nil, err
	}
	if resp.Status == 404 || resp.Status == 409 {
		// 409 is an empty repository.
		return nil, nil
	}
	if resp.Status != 200 {
		return nil, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var data []struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message string `json:"message"`
		} `json:"commit"`
		Parents []struct {
			SHA string `json:"sha"`
		} `json:"parents"`
	}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return nil, err
	}
	out := make([]Commit, 0, len(data))
	for _, d := range data {
		commit := Commit{SHA: d.SHA, Message: d.Commit.Message, Merge: len(d.Parents) > 1}
		for _, p := range d.Parents {
			commit.Parents = append(commit.Parents, p.SHA)
		}
		out = append(out, commit)
	}
	return out, nil
}
