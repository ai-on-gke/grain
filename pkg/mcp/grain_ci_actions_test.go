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

package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
)

// actionsGrainCIRunner is a daemon whose acme/widgets has one GitHub
// Actions run beside a grain CI run, and serves its jobs' logs.
type actionsGrainCIRunner struct {
	fakeGrainCIRunner
	logJob string
}

func (a *actionsGrainCIRunner) GrainCIJobLog(_ context.Context, runID, job string) (CIJobLog, error) {
	a.logJob = job
	return CIJobLog{Job: "publish", JobID: 701, Bytes: 40, Log: "login ok\nerror: GHCR_TOKEN is not set\n"}, nil
}

func TestCIToolsReadGitHubActionsRuns(t *testing.T) {
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	gha := ci.Run{ID: "gha:acme:widgets:42", Provider: ci.ProviderGitHubActions, Repo: testCIRepo, Ref: "main",
		Conclusion: ci.ConclusionFailure, StartedAt: &at,
		Actions: &ci.ActionsRun{RunID: 42, WorkflowName: "Release", FailedJobs: []string{"publish"}},
		Jobs:    []ci.JobRun{{JobID: "701", Name: "publish", Conclusion: ci.ConclusionFailure}}}
	runner := &actionsGrainCIRunner{fakeGrainCIRunner: fakeGrainCIRunner{
		run:  &gha,
		runs: []ci.Run{gha, {ID: "ci-1", Repo: testCIRepo, Ref: "main", Conclusion: ci.ConclusionSuccess}},
	}}
	tools := NewGrainCIReadTools(runner, CIRepoScope{Repo: testCIRepo})

	list := toolNamed(t, tools, "list_ci_runs")
	res := list.Handler(context.Background(), map[string]any{"repo": testCIRepo, "provider": "github-actions"})
	if res.IsError || !strings.Contains(res.Text, `gha:acme:widgets:42 acme/widgets main failure [github-actions "Release"]`) ||
		!strings.Contains(res.Text, "publish") || strings.Contains(res.Text, "ci-1") {
		t.Errorf("list_ci_runs provider=github-actions = %s", res.Text)
	}
	if res := list.Handler(context.Background(), map[string]any{"provider": "jenkins"}); !res.IsError {
		t.Errorf("an unknown provider was taken: %s", res.Text)
	}

	logs := toolNamed(t, tools, "get_ci_log")
	res = logs.Handler(context.Background(), map[string]any{"run_id": gha.ID, "job": "publish", "tail": float64(30)})
	if res.IsError || !strings.Contains(res.Text, `job "publish" (id 701)`) || !strings.Contains(res.Text, "GHCR_TOKEN is not set") ||
		strings.Contains(res.Text, "login ok") {
		t.Errorf("get_ci_log of an Actions job = %s", res.Text)
	}
	if runner.logJob != "publish" {
		t.Errorf("the daemon was asked for job %q, want publish", runner.logJob)
	}
}
