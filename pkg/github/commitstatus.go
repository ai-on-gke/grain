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
	"net/url"
)

// CommitStatus is one context's newest commit status on a commit -- the
// older API beside check runs, which a CI system that is not GitHub
// Actions posts its verdicts as.
type CommitStatus struct {
	Context     string
	State       string // "success", "failure", "error" or "pending"
	Description string
	TargetURL   string
}

// CommitStatusReader is the optional half of Client that reads a
// commit's statuses: what the release-qualification reconciler reads a
// suite's Stage 1 from when it is declared as a status rather than as a
// workflow run (orchestrator/releasequalification.go).
type CommitStatusReader interface {
	CommitStatuses(owner, repo, ref string) ([]CommitStatus, error)
}

var _ CommitStatusReader = (*RESTClient)(nil)

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
