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

package ui_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// GET /api/tasks/{id} reads the CI gate of a pull request GitHub Actions
// gates off the checks grain recorded on its branch, rather than leaving
// it "running, waiting for checks" for as long as the pull request is open.
func TestTaskDetailCIGateReadsRecordedGitHubChecks(t *testing.T) {
	t.Parallel()
	head := "abcdef0123456789abcdef0123456789abcdef01"
	for _, tc := range []struct {
		name       string
		conclusion []string
		state      model.MergeGateState
		detail     string
	}{
		{"green", []string{"success", "success"}, model.GateStatePassed, "passed (2 checks at abcdef012345)"},
		{"red", []string{"success", "failure"}, model.GateStateFailing, "failing: Tests / job1 at abcdef012345"},
		{"no data", nil, model.GateStateRunning, "grain has no record of this pull request's checks"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client, store, ctx := testClient(t)
			repo := model.RepoRef{Owner: "acme", Name: "widgets"}
			task := model.Task{ID: "g1", Title: "held", Intent: model.IntentImplement, Target: &repo,
				Branch: "grain/task-g1", Links: []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#7"}}}
			if err := store.PutTask(ctx, task); err != nil {
				t.Fatal(err)
			}
			at := baseTime.Add(-time.Hour)
			for i, c := range tc.conclusion {
				if err := store.PutGitHubCIJob(ctx, model.GitHubCIJob{Repo: repo.String(), CheckRunID: int64(i + 1),
					Ref: "grain/task-g1", Commit: head, Workflow: "Tests", JobName: "job" + string(rune('0'+i)),
					Conclusion: c, CompletedAt: &at}); err != nil {
					t.Fatal(err)
				}
			}
			detail, err := client.GetTask(ctx, "g1")
			if err != nil {
				t.Fatal(err)
			}
			var ci *model.MergeGate
			for i := range detail.MergeGates {
				if detail.MergeGates[i].ID == model.GateCI {
					ci = &detail.MergeGates[i]
				}
			}
			if ci == nil || ci.State != tc.state || ci.Detail != tc.detail {
				t.Fatalf("CI gate = %+v, want %s %q", ci, tc.state, tc.detail)
			}
			if strings.Contains(ci.Detail, "waiting for checks") {
				t.Errorf("CI gate still says %q", ci.Detail)
			}
		})
	}
}

// A pass grain recorded minutes ago, at a head whose previous commit ran
// more checks, is not CI passing: the task page says it is still running.
func TestTaskDetailCIGateDoesNotPassWhileRecordedChecksMayStillRun(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	head := "abcdef0123456789abcdef0123456789abcdef01"
	prev := "1111111111111111111111111111111111111111"
	task := model.Task{ID: "g2", Title: "held", Intent: model.IntentImplement, Target: &repo,
		Branch: "grain/task-g2", Links: []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#8"},
			{Kind: model.LinkTrustBoundaryCleared, Target: "v28@" + head}}}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	older, recent := baseTime.Add(-3*time.Hour), baseTime.Add(-5*time.Minute)
	for i, j := range []model.GitHubCIJob{
		{Commit: prev, JobName: "go", Conclusion: "success", CompletedAt: &older},
		{Commit: prev, JobName: "ui e2e", Conclusion: "success", CompletedAt: &older},
		{Commit: head, JobName: "go", Conclusion: "success", CompletedAt: &recent},
	} {
		j.Repo, j.CheckRunID, j.Ref, j.Workflow = repo.String(), int64(i+1), "grain/task-g2", "Tests"
		if err := store.PutGitHubCIJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	detail, err := client.GetTask(ctx, "g2")
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range detail.MergeGates {
		if g.ID == model.GateCI {
			if g.State != model.GateStateRunning || !strings.Contains(g.Detail, "others may still be running") {
				t.Fatalf("CI gate = %+v, want running while others may still run", g)
			}
			return
		}
	}
	t.Fatal("no CI gate")
}
