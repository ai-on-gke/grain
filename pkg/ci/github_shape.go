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

package ci

import (
	"context"
	"strings"
	"time"
)

// The GitHub REST shapes below are what `grain ci list --format github`
// and `grain ci checks` write: GitHub's own answers to
// GET /repos/{owner}/{repo}/actions/runs ({workflow_runs}),
// GET .../actions/runs/{id}/jobs?filter=all ({jobs}) and
// GET .../commits/{sha}/check-runs ({check_runs}), with the fields
// scripts/candidate-gate.sh reads, so that a task running the release
// train (.grain/templates/release-train.yml) hands the gate the forge's
// verdicts through grain's own forge connection, unedited, as a GitHub
// token would have.

// GitHubRepository is a workflow run's repository or head_repository.
type GitHubRepository struct {
	FullName string `json:"full_name"`
}

// GitHubWorkflowRun is one of GitHub's workflow_runs.
type GitHubWorkflowRun struct {
	ID             int64             `json:"id"`
	Name           string            `json:"name,omitempty"`
	Path           string            `json:"path"`
	HeadBranch     string            `json:"head_branch"`
	HeadSHA        string            `json:"head_sha"`
	Event          string            `json:"event"`
	Status         string            `json:"status"`
	Conclusion     *string           `json:"conclusion"`
	HTMLURL        string            `json:"html_url,omitempty"`
	RunAttempt     int               `json:"run_attempt,omitempty"`
	RunStartedAt   *time.Time        `json:"run_started_at"`
	UpdatedAt      *time.Time        `json:"updated_at"`
	Repository     GitHubRepository  `json:"repository"`
	HeadRepository *GitHubRepository `json:"head_repository,omitempty"`
}

// GitHubWorkflowRunOf is run as GitHub gave it, or false for a run that is
// not in GitHub's own words: grain CI's, or a GitHub Actions run grain
// put together from its record of the run's jobs (no workflow run
// conclusion of GitHub's, no event), which would be a verdict nobody but
// grain gave.
func GitHubWorkflowRunOf(run Run) (GitHubWorkflowRun, bool) {
	a := run.Actions
	if a == nil || a.RunID <= 0 || a.Status == "" || run.Event == "" {
		return GitHubWorkflowRun{}, false
	}
	if a.Status == "completed" && a.Conclusion == "" {
		return GitHubWorkflowRun{}, false
	}
	out := GitHubWorkflowRun{
		ID: a.RunID, Name: a.WorkflowName, Path: a.Workflow,
		HeadBranch: run.Ref, HeadSHA: run.Commit, Event: string(run.Event),
		Status: a.Status, HTMLURL: a.URL, RunAttempt: a.RunAttempt,
		RunStartedAt: run.StartedAt, UpdatedAt: run.FinishedAt,
		Repository: GitHubRepository{FullName: run.Repo},
	}
	if a.Conclusion != "" {
		c := a.Conclusion
		out.Conclusion = &c
	}
	if a.HeadRepo != "" {
		out.HeadRepository = &GitHubRepository{FullName: a.HeadRepo}
	}
	return out, true
}

// GitHubJob is one of GitHub's jobs: one attempt of one job of a workflow
// run.
type GitHubJob struct {
	ID           int64      `json:"id"`
	RunID        int64      `json:"run_id"`
	RunAttempt   int        `json:"run_attempt"`
	WorkflowName string     `json:"workflow_name"`
	HeadSHA      string     `json:"head_sha"`
	Name         string     `json:"name"`
	Status       string     `json:"status"`
	Conclusion   *string    `json:"conclusion"`
	CreatedAt    *time.Time `json:"created_at"`
	StartedAt    *time.Time `json:"started_at"`
	CompletedAt  *time.Time `json:"completed_at"`
	HTMLURL      string     `json:"html_url,omitempty"`
}

// GitHubApp is the App a check run was posted by.
type GitHubApp struct {
	Slug string `json:"slug"`
}

// GitHubCheckRun is one of GitHub's check_runs.
type GitHubCheckRun struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	HeadSHA     string     `json:"head_sha"`
	ExternalID  string     `json:"external_id,omitempty"`
	Status      string     `json:"status"`
	Conclusion  *string    `json:"conclusion"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	HTMLURL     string     `json:"html_url,omitempty"`
	App         *GitHubApp `json:"app"`
}

// ExternalAttempts is an optional extension of ExternalRuns: the forge's
// answers the release train's gate judges on every attempt of, read
// through grain's own forge connection. Like ExternalRuns it does no
// scoping of its own.
type ExternalAttempts interface {
	// ExternalJobAttempts is every attempt of every job of run id
	// ("gha:<owner>:<name>:<run id>").
	ExternalJobAttempts(ctx context.Context, id string) ([]GitHubJob, error)
	// ExternalCheckRuns is every check run named name ("" for all of
	// them) on commit sha of repo, and the slug of the GitHub App grain's
	// own check runs there are posted as ("" when it cannot tell).
	ExternalCheckRuns(ctx context.Context, repo, sha, name string) (runs []GitHubCheckRun, app string, err error)
}

// GitHubConclusion is a conclusion as GitHub writes it: null for one it
// has not given yet.
func GitHubConclusion(c string) *string {
	if c = strings.TrimSpace(c); c == "" {
		return nil
	}
	return &c
}
