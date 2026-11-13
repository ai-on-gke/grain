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
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// ?until= asks the forge for runs created before it, rather than paging
// the newest MaxActionsRunsLimit back from now and narrowing those -- which
// answered nothing for any window older than them (grain/task-5274).
func TestCIRunsListAsksTheForgeForTheUntilWindow(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	forge := newFakeActions(now)
	old := now.Add(-21 * 24 * time.Hour)
	oldDone := old.Add(20 * time.Minute)
	forge.runs = append(forge.runs, github.WorkflowRun{ID: 7, Name: "Tests", Path: ".github/workflows/tests.yml",
		HeadBranch: "main", HeadSHA: "0ld", Event: "push", Status: "completed", Conclusion: "failure", RunAttempt: 1,
		StartedAt: &old, UpdatedAt: &oldDone})
	srv, _, _ := actionsServer(t, forge, now)

	until := now.Add(-14 * 24 * time.Hour).Format(time.RFC3339)
	code, body := asAgent(t, srv, "reader", http.MethodGet,
		"/api/ci/runs?repo=acme/widgets&provider=github-actions&ref=main&until="+until, "", "")
	if code != http.StatusOK {
		t.Fatalf("list: %d %s", code, body)
	}
	got := decodeString[ui.CIRunsResponse](t, body)
	if len(got.Runs) != 1 || got.Runs[0].ID != "gha:acme:widgets:7" || got.Runs[0].Conclusion != ci.ConclusionFailure {
		t.Fatalf("until=%s = %+v, want the three-week-old main run alone", until, got.Runs)
	}
	forge.mu.Lock()
	defer forge.mu.Unlock()
	if len(forge.queries) == 0 || forge.queries[0].Until.Format(time.RFC3339) != until {
		t.Errorf("the forge was asked %+v, want ?until= passed on", forge.queries)
	}
}

// The record behind the listing is read with the filter's window and ref,
// so a default branch's runs from weeks ago still come back however many
// newer jobs other branches recorded since.
func TestCIRunsListReadsGrainsRecordOfAnOlderWindow(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	srv, _, store := actionsServer(t, nil, now)
	ctx := context.Background()
	put := func(j model.GitHubCIJob) {
		t.Helper()
		j.Repo, j.App, j.RunAttempt = "acme/widgets", model.GitHubActionsApp, 1
		if err := store.PutGitHubCIJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	// Three weeks ago: a red main and its repair.
	for i, conclusion := range []string{"failure", "success"} {
		at := now.Add(-21*24*time.Hour + time.Duration(i)*time.Hour)
		put(model.GitHubCIJob{CheckRunID: int64(1 + i), Ref: "main", Commit: fmt.Sprintf("m%d", i), Workflow: "Tests",
			RunID: int64(10 + i), JobName: "go", Conclusion: conclusion, StartedAt: &at, CompletedAt: &at, UpdatedAt: at})
	}
	// Since then, more task-branch jobs than one read of the record holds.
	for i := 0; i < 5001; i++ {
		at := now.Add(-time.Duration(i) * time.Minute)
		put(model.GitHubCIJob{CheckRunID: int64(1000 + i), Ref: fmt.Sprintf("grain/task-%d", i%40), Workflow: "Tests",
			RunID: int64(1000 + i/10), JobName: fmt.Sprintf("job-%d", i%10), Conclusion: "success",
			StartedAt: &at, CompletedAt: &at, UpdatedAt: at})
	}

	until := now.Add(-14 * 24 * time.Hour).Format(time.RFC3339)
	code, body := asAgent(t, srv, "reader", http.MethodGet,
		"/api/ci/runs?repo=acme/widgets&provider=github-actions&ref=main&until="+until, "", "")
	if code != http.StatusOK {
		t.Fatalf("list: %d %s", code, body)
	}
	var ids []string
	for _, run := range decodeString[ui.CIRunsResponse](t, body).Runs {
		ids = append(ids, run.ID+"="+string(run.Conclusion))
	}
	if want := "gha:acme:widgets:11=success,gha:acme:widgets:10=failure"; strings.Join(ids, ",") != want {
		t.Fatalf("main before %s = %v, want %s", until, ids, want)
	}
}

// The record a test's history is counted over is the failed jobs naming
// it, for as long as they are kept -- including the ones compacted to
// their verdict and test names, whose log tail is gone.
func TestActionsTestFailuresReadTheCompactedRecord(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	_, client, store := actionsServer(t, nil, now)
	ctx := context.Background()
	monthAgo := now.Add(-30 * 24 * time.Hour)
	for _, j := range []model.GitHubCIJob{
		{CheckRunID: 1, RunID: 10, Ref: "main", JobName: "go", Conclusion: "failure",
			FailureTail: "--- FAIL: TestThing (0.1s)\n"},
		{CheckRunID: 2, RunID: 10, Ref: "main", JobName: "lint", Conclusion: "success"},
		{CheckRunID: 3, RunID: 11, Ref: "grain/task-4", JobName: "go", Conclusion: "failure",
			FailureTail: "--- FAIL: TestOther (0.1s)\n"},
	} {
		j.Repo, j.App, j.Workflow, j.RunAttempt = "acme/widgets", model.GitHubActionsApp, "Tests", 1
		j.StartedAt, j.CompletedAt, j.UpdatedAt = &monthAgo, &monthAgo, monthAgo
		if err := store.PutGitHubCIJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.PruneGitHubCIJobs(ctx, model.GitHubCIPrune{
		Before: now.Add(-56 * 24 * time.Hour), Compact: now.Add(-14 * 24 * time.Hour), KeepRefs: []string{"main"},
	}); err != nil {
		t.Fatal(err)
	}
	failures, ok := client.ExternalCIRuns().(ci.ExternalTestFailures)
	if !ok {
		t.Fatal("the Actions provider keeps no record of failed tests")
	}
	runs, note := failures.FindExternalTestFailures(ctx, ci.RunFilter{Repo: "acme/widgets"}, "TestThing")
	if note != "" || len(runs) != 1 || runs[0].ID != "gha:acme:widgets:10" {
		t.Fatalf("failures of TestThing = %+v (%q), want run 10 alone", runs, note)
	}
	report := ci.QueryTestHistory(runs, "TestThing")
	if report.Failed != 1 || len(report.RecentRuns) != 1 || report.RecentRuns[0].JobName != "go" {
		t.Fatalf("history over the compacted record = %+v", report)
	}
}
