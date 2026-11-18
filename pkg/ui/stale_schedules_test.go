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
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// The CI pane reads the schedules that stopped running on their own
// (?section=staleSchedules), without the sandbox sweep; System → Health
// reads them as part of the whole report. Both say the same thing.
func TestSandboxHealthStaleSchedulesSection(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	now := time.Now()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := client.Store.PutSchedule(ctx, model.Schedule{
		ID: "nightly", Title: "nightly", Target: repo, Enabled: true,
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 7 * 60},
		CreatedAt:  now.Add(-10 * 24 * time.Hour), NextRunAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	sectionOnly := decode[map[string]any](t, do(t, srv, http.MethodGet, "/api/sandboxes?section=staleSchedules", ""))
	if _, ok := sectionOnly["enabled"]; ok {
		t.Errorf("?section=staleSchedules = %v, want only the stale schedules", sectionOnly)
	}
	for _, path := range []string{"/api/sandboxes?section=staleSchedules", "/api/sandboxes"} {
		got := decode[ui.SandboxHealthResponse](t, do(t, srv, http.MethodGet, path, ""))
		if len(got.StaleSchedules) != 1 || got.StaleSchedules[0].ID != "nightly" || got.StaleSchedules[0].LastRunAt != nil {
			t.Errorf("%s: staleSchedules = %+v, want nightly, never run", path, got.StaleSchedules)
		}
	}
}

type staticDefaultBranches map[string]string

func (m staticDefaultBranches) DefaultBranch(owner, repo string) (string, error) {
	return m[owner+"/"+repo], nil
}

// A `.grain/schedules/` entry that sets `grain-ci: true` and names the
// repository's default branch explicitly (`base: trunk` on a repository
// whose default branch is trunk) clears the
// workflow's dropped cron (`Base: ""`) from the stale-schedules endpoint.
func TestStaleSchedulesAlignsExplicitDefaultBranchWithWorkflowCron(t *testing.T) {
	srv, client := testServer(t)
	client.Config.GitHubDefaultBranches = staticDefaultBranches{"acme/widgets": "trunk"}
	ctx := context.Background()
	now := time.Now()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := client.Store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGitHub}); err != nil {
		t.Fatal(err)
	}
	daily := model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 5*60 + 53}
	live := model.WorkflowReviewTemplateID("live-secrets-repo")
	if err := client.Store.PutSchedule(ctx, model.Schedule{
		ID: model.RepoDeclaredID(model.DeclaredSchedule, repo, "workflow.live-secrets-repo"), Title: "live-secrets-repo", Target: repo,
		TemplateID: &live, Recurrence: daily, Enabled: true,
		CreatedAt: now.Add(-10 * 24 * time.Hour), NextRunAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	ran := now.Add(-time.Hour)
	if err := client.Store.PutSchedule(ctx, model.Schedule{
		ID: "live-secrets-repo", Title: "Live secrets repository", Target: repo,
		TemplateID: &live, Base: "trunk", GrainCI: true, Recurrence: daily, Enabled: true,
		CreatedAt: now.Add(-10 * 24 * time.Hour), LastRunAt: &ran, NextRunAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	got := decode[ui.SandboxHealthResponse](t, do(t, srv, http.MethodGet, "/api/sandboxes?section=staleSchedules", ""))
	if len(got.StaleSchedules) != 0 {
		t.Fatalf("staleSchedules = %+v, want dropped cron cleared by explicit base: trunk schedule", got.StaleSchedules)
	}
}

func TestRepoReleaseEndpointBranchFilterAndHeldStaleSchedule(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	now := time.Now()
	client.Now = func() time.Time { return now }
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	// Two failing branches: release/v2 has failed longer (not gated), while
	// main is gated on triage task 4256.
	oldSince := now.Add(-24 * time.Hour)
	mainSince := now.Add(-6 * time.Hour)
	finished := now.Add(-time.Hour)
	if err := client.Store.PutBaseBranchRelease(ctx, model.BaseBranchRelease{
		Repo: repo, Branch: "release/v2", Commit: "aaa1111", RunID: "rel-v2",
		StartedAt: now.Add(-2 * time.Hour), FinishedAt: &finished, Conclusion: "failure",
		Failures: 8, FailingSince: &oldSince,
	}); err != nil {
		t.Fatal(err)
	}
	if err := client.Store.PutBaseBranchRelease(ctx, model.BaseBranchRelease{
		Repo: repo, Branch: "main", Commit: "bbb2222", RunID: "rel-main",
		StartedAt: now.Add(-2 * time.Hour), FinishedAt: &finished, Conclusion: "failure",
		Failures: 3, FailingSince: &mainSince,
	}); err != nil {
		t.Fatal(err)
	}
	if err := client.Store.PutTask(ctx, model.Task{
		ID: "4256", Title: "Triage main's failing releases", Target: &repo, Base: "main",
		Tags: []string{model.BaseReleaseTriageBranchTag(repo, "main")},
	}); err != nil {
		t.Fatal(err)
	}

	unfiltered := decode[ui.RepoReleaseSettings](t, do(t, srv, http.MethodGet, "/api/repos/acme/widgets/release", ""))
	if unfiltered.BaseBranchPublishing == nil || unfiltered.BaseBranchPublishing.Branch != "release/v2" {
		t.Fatalf("unfiltered baseBranchPublishing = %+v, want longest-failing branch release/v2", unfiltered.BaseBranchPublishing)
	}
	mainOnly := decode[ui.RepoReleaseSettings](t, do(t, srv, http.MethodGet, "/api/repos/acme/widgets/release?branch=main", ""))
	if mainOnly.BaseBranchPublishing == nil || mainOnly.BaseBranchPublishing.Branch != "main" ||
		!mainOnly.BaseBranchPublishing.Gated || mainOnly.BaseBranchPublishing.TriageTaskID != "4256" {
		t.Fatalf("?branch=main baseBranchPublishing = %+v, want main gated on triage task 4256", mainOnly.BaseBranchPublishing)
	}

	lastRun := now.Add(-3 * time.Hour)
	if err := client.Store.PutSchedule(ctx, model.Schedule{
		ID: "release-train", Title: "Release train", Target: repo, Base: "main",
		Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNMinutes, EveryNMinutes: 20},
		Overlap:    model.OverlapSkip, SkipWhen: model.SkipWhenPublishingHeld,
		Enabled: true, LastRunAt: &lastRun, CreatedAt: now.Add(-48 * time.Hour), NextRunAt: now.Add(-2 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	health := decode[ui.SandboxHealthResponse](t, do(t, srv, http.MethodGet, "/api/sandboxes?section=staleSchedules", ""))
	if len(health.StaleSchedules) != 1 || !health.StaleSchedules[0].Held ||
		health.StaleSchedules[0].HeldByTaskID != "4256" ||
		!strings.Contains(health.StaleSchedules[0].Reason, "publishing held by task 4256") {
		t.Fatalf("staleSchedules = %+v, want held release-train naming task 4256", health.StaleSchedules)
	}
}
