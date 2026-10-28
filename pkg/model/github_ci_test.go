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

package model_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestGitHubCIJobMergesReportsOfOneCheckRun(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	t0 := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	done := t0.Add(5 * time.Minute)

	// The poll sees the check run first: name and conclusion, nothing else.
	if err := st.PutGitHubCIJob(ctx, model.GitHubCIJob{
		Repo: "o/r", CheckRunID: 42, JobName: "test", Conclusion: "failure",
		Commit: "abc", Via: "poll", UpdatedAt: t0,
	}); err != nil {
		t.Fatal(err)
	}
	// Then the workflow_job delivery, which knows the workflow and step.
	wj := model.GitHubCIJob{
		Repo: "o/r", CheckRunID: 42, JobName: "test", Conclusion: "failure",
		Workflow: "ci", RunID: 7, RunAttempt: 1, FailingStep: "go test", Ref: "feature",
		StartedAt: &t0, CompletedAt: &done, Via: "webhook", UpdatedAt: t0.Add(time.Minute),
	}
	if err := st.PutGitHubCIJob(ctx, wj); err != nil {
		t.Fatal(err)
	}
	// A redelivery of the same event changes nothing and adds no row.
	if err := st.PutGitHubCIJob(ctx, wj); err != nil {
		t.Fatal(err)
	}
	jobs, err := st.ListGitHubCIJobs(ctx, model.GitHubCIFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("got %d rows, want 1: %+v", len(jobs), jobs)
	}
	j := jobs[0]
	if j.Source != model.GitHubCISource || j.Commit != "abc" || j.Workflow != "ci" ||
		j.FailingStep != "go test" || j.JobKey() != "ci / test" || !j.IsActions() {
		t.Fatalf("merged row = %+v", j)
	}
	if j.RecordedAt != t0 {
		t.Errorf("recorded_at = %v, want the first sighting %v", j.RecordedAt, t0)
	}

	// The tail fetcher's work list holds it until its log is fetched.
	need, err := st.ListGitHubCIJobs(ctx, model.GitHubCIFilter{NeedsTail: true})
	if err != nil || len(need) != 1 {
		t.Fatalf("needs tail = %v, %v; want one", need, err)
	}
	if err := st.SetGitHubCIFailureTail(ctx, "o/r", 42, "--- FAIL: TestX\n", "log", done); err != nil {
		t.Fatal(err)
	}
	need, _ = st.ListGitHubCIJobs(ctx, model.GitHubCIFilter{NeedsTail: true})
	if len(need) != 0 {
		t.Fatalf("needs tail after fetch = %v", need)
	}
	// A later report without a tail keeps it.
	if err := st.PutGitHubCIJob(ctx, model.GitHubCIJob{Repo: "o/r", CheckRunID: 42, Conclusion: "failure", RunAttempt: 1}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetGitHubCIJob(ctx, "o/r", 42)
	if got.FailureTail != "--- FAIL: TestX\n" || !got.TailFetched {
		t.Fatalf("tail lost: %+v", got)
	}
	// A re-run replaces the attempt's verdict and forgets its tail.
	if err := st.PutGitHubCIJob(ctx, model.GitHubCIJob{Repo: "o/r", CheckRunID: 42, Conclusion: "success", RunAttempt: 2}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetGitHubCIJob(ctx, "o/r", 42)
	if got.Conclusion != "success" || got.FailureTail != "" || got.FailingStep != "" || got.TailFetched {
		t.Fatalf("re-run = %+v", got)
	}
}

func TestGitHubCIJobBoundsTextAndPrunes(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	old := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	big := strings.Repeat("noise line\n", model.MaxGitHubCIOutput/5) + "the error\n"
	if err := st.PutGitHubCIJob(ctx, model.GitHubCIJob{
		Repo: "o/r", CheckRunID: 1, JobName: "lint", Conclusion: "failure", Output: big, UpdatedAt: old,
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetGitHubCIJob(ctx, "o/r", 1)
	if len(got.Output) > model.MaxGitHubCIOutput || !strings.HasSuffix(got.Output, "the error\n") {
		t.Fatalf("output kept %d bytes, ending %q", len(got.Output), got.Output[len(got.Output)-20:])
	}
	n, err := st.PruneGitHubCIJobs(ctx, old.Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("prune = %d, %v", n, err)
	}
}

// GitHubCIOutcomes is what the metrics read: identity and conclusion,
// oldest first by the moment each concluded, with none of the text.
func TestGitHubCIOutcomesReadsConclusionsOldestFirst(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	t0 := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	later := t0.Add(time.Hour)
	for _, j := range []model.GitHubCIJob{
		{Repo: "o/r", CheckRunID: 2, Workflow: "ci", JobName: "test", RunAttempt: 2, Conclusion: "success",
			Ref: "main", Commit: "abc", CompletedAt: &later, Output: "big", UpdatedAt: t0},
		// No completion time: placed by when it was recorded.
		{Repo: "o/r", CheckRunID: 1, JobName: "codecov", Conclusion: "failure", Commit: "abc", UpdatedAt: t0},
	} {
		if err := st.PutGitHubCIJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.GitHubCIOutcomes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].CheckRunID != 1 || got[1].CheckRunID != 2 {
		t.Fatalf("outcomes = %+v, want check runs 1 then 2", got)
	}
	if o := got[1]; o.JobKey() != "ci / test" || o.Commit != "abc" || o.Ref != "main" || o.RunAttempt != 2 ||
		o.Conclusion != "success" || !o.At.Equal(later) {
		t.Errorf("outcome = %+v", o)
	}
	if !got[0].At.Equal(t0) {
		t.Errorf("a row with no completion time is at %s, want its recorded time %s", got[0].At, t0)
	}
}
