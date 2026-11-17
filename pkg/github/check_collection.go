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
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

// CheckCollectionReader is the bounded ordinary internal read used by shadow CI.
// It reads all attempts and Actions run verdicts (including startup failures
// which create no check run). Actions-only evidence never claims Checks coverage.
type CheckCollectionReader interface {
	ReadCheckCollection(ctx context.Context, owner, repo, head string, limit int) (CheckCollection, error)
}
type CheckCollection struct {
	Checks          []CheckRun
	Workflows       []WorkflowRun
	ChecksComplete  bool
	ActionsComplete bool
	Complete        bool
	Gap             string
}

func (c *RESTClient) ReadCheckCollection(ctx context.Context, owner, repo, head string, limit int) (CheckCollection, error) {
	out := CheckCollection{}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	var checksErr error
	if head == "" || limit < 1 || limit > 512 {
		return out, fmt.Errorf("invalid check collection scope or limit")
	}
	path := fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs?per_page=100&filter=all", owner, repo, url.PathEscape(head))
	total := -1
	seen := map[int64]bool{}
	for page := 0; path != ""; page++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if page >= 6 {
			out.Gap = "Checks page bound exceeded."
			return out, nil
		}
		resp, err := c.get(owner, repo, path)
		if err != nil {
			return out, err
		}
		if resp.Status != 200 {
			checksErr = &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
			if IsPermissionDenied(checksErr) {
				out.Gap = "Checks access denied; Actions-only evidence cannot prove complete CI coverage."
				break
			}
			return out, checksErr
		}
		var data struct {
			Total *int `json:"total_count"`
			Runs  []struct {
				Output struct {
					Title   string `json:"title"`
					Summary string `json:"summary"`
					Text    string `json:"text"`
				} `json:"output"`
				Steps       []json.RawMessage `json:"steps"`
				ID          int64             `json:"id"`
				Name        string            `json:"name"`
				Head        string            `json:"head_sha"`
				Status      string            `json:"status"`
				Conclusion  *string           `json:"conclusion"`
				StartedAt   *time.Time        `json:"started_at"`
				CompletedAt *time.Time        `json:"completed_at"`
				App         struct {
					Slug string `json:"slug"`
				} `json:"app"`
				Suite struct {
					ID int64 `json:"id"`
				} `json:"check_suite"`
			} `json:"check_runs"`
		}
		if err = json.Unmarshal(resp.Body, &data); err != nil {
			return out, err
		}
		if data.Total == nil || *data.Total > limit || *data.Total < 0 || (total >= 0 && *data.Total != total) {
			out.Gap = "Checks total is missing, changed or exceeds the bound."
			return out, nil
		}
		total = *data.Total
		for _, r := range data.Runs {
			if len(out.Checks) >= limit || r.ID <= 0 || seen[r.ID] || r.Head != head || r.App.Slug == "" {
				out.Gap = "Check identities or head coverage are incomplete."
				return out, nil
			}
			seen[r.ID] = true
			reason := r.Output.Title
			if reason == "" {
				reason = r.Output.Summary
			}
			if reason == "" {
				reason = r.Output.Text
			}
			out.Checks = append(out.Checks, CheckRun{FailureReason: reason, StepsExecuted: len(r.Steps), ID: r.ID, Name: r.Name, HeadSHA: r.Head, Status: r.Status, Conclusion: r.Conclusion, StartedAt: r.StartedAt, CompletedAt: r.CompletedAt, App: r.App.Slug, CheckSuiteID: r.Suite.ID})
		}
		path = nextPagePath(resp.Headers["Link"])
	}
	if checksErr == nil {
		if len(out.Checks) != total {
			out.Gap = "Checks count does not cover the reported collection."
			return out, nil
		}
		out.ChecksComplete = true
	}
	path = fmt.Sprintf("/repos/%s/%s/actions/runs?head_sha=%s&per_page=100", owner, repo, url.QueryEscape(head))
	total = -1
	seen = map[int64]bool{}
	for page := 0; path != ""; page++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if page >= 6 {
			out.Gap = "Actions page bound exceeded."
			return out, nil
		}
		resp, err := c.get(owner, repo, path)
		if err != nil {
			return out, err
		}
		if resp.Status != 200 {
			return out, &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
		}
		var data struct {
			Total *int              `json:"total_count"`
			Runs  []workflowRunJSON `json:"workflow_runs"`
		}
		if err = json.Unmarshal(resp.Body, &data); err != nil {
			return out, err
		}
		if data.Total == nil || *data.Total > limit || *data.Total < 0 || (total >= 0 && *data.Total != total) {
			out.Gap = "Actions total is missing, changed or exceeds the bound."
			return out, nil
		}
		total = *data.Total
		for _, raw := range data.Runs {
			r := raw.run()
			if len(out.Workflows) >= limit || r.ID <= 0 || seen[r.ID] || r.HeadSHA != head || r.Path == "" || r.RunAttempt <= 0 {
				out.Gap = "Actions identities or head coverage are incomplete."
				return out, nil
			}
			seen[r.ID] = true
			out.Workflows = append(out.Workflows, r)
		}
		path = nextPagePath(resp.Headers["Link"])
	}
	out.ActionsComplete = len(out.Workflows) == total
	out.Complete = out.ChecksComplete && out.ActionsComplete
	if !out.ActionsComplete {
		out.Gap = "Actions count does not cover the reported collection."
	}
	return out, checksErr
}
