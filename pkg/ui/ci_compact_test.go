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
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// ?view=compact is the listing an audit of a week of runs can read in
// pages: each run's identity and verdict, and for each job that did not
// succeed a line saying where it failed -- no steps, and no fault's run
// list (grain/task-975).
func TestListCIRunsCompactViewIsOneLineAJob(t *testing.T) {
	t.Parallel()
	started := time.Now().Add(-time.Hour)
	finished := started.Add(10 * time.Minute)
	red := ci.Run{
		ID: "red", Repo: "acme/widgets", Ref: "grain-merge-queue/main/batch-1", Commit: "abc123",
		Event: ci.EventPush, RequestedBy: "merge-queue", StartedAt: &started, FinishedAt: &finished,
		Conclusion: ci.ConclusionFailure,
		Jobs: []ci.JobRun{
			{JobID: "lint", Name: "lint", Conclusion: ci.ConclusionSuccess, BaseCommit: "base99",
				Steps: []ci.StepRun{{Name: "golangci-lint", Conclusion: ci.ConclusionSuccess, Output: "ok"}}},
			{JobID: "go", Name: "go", Conclusion: ci.ConclusionFailure, BaseCommit: "base99",
				Error: "step \"go test\" exited 1\nmore detail nobody needs here",
				FailureSummary: &ci.FailureSummary{Tests: []ci.TestFailure{
					{Name: "TestQuarantined", Quarantined: true},
					{Name: "TestThing", Message: "boom"},
				}},
				Steps: []ci.StepRun{
					{Name: "build", Conclusion: ci.ConclusionSuccess, Output: strings.Repeat("compiling\n", 500)},
					{Name: "go test", Conclusion: ci.ConclusionFailure, ExitCode: 1, Output: "--- FAIL: TestThing"},
				}},
			{JobID: "smoke", Name: "smoke", Conclusion: ci.ConclusionSkipped},
		},
	}
	green := ci.Run{
		ID: "green", Repo: "acme/widgets", Ref: "main", Commit: "def456", StartedAt: timeAt(started.Add(-time.Hour)),
		Conclusion: ci.ConclusionSuccess,
		Jobs:       []ci.JobRun{{JobID: "go", Name: "go", Conclusion: ci.ConclusionSuccess}},
	}
	srv, _ := ciServer(t, &fakeCIRunner{runs: []ci.Run{red, green}})

	rec := do(t, srv, http.MethodGet, "/api/ci/runs?view=compact", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); strings.Contains(body, "compiling") || strings.Contains(body, `"steps"`) {
		t.Errorf("the compact view carries step detail: %s", body)
	}
	got := decode[ui.CICompactRunsResponse](t, rec)
	if !got.Enabled || got.Matched != 2 || got.Stored != 2 || len(got.Runs) != 2 {
		t.Fatalf("compact listing = %+v", got)
	}
	run := got.Runs[0]
	if run.ID != "red" || run.Ref != red.Ref || run.Commit != "abc123" || run.BaseCommit != "base99" ||
		run.Event != ci.EventPush || run.RequestedBy != "merge-queue" || run.Conclusion != "failure" ||
		run.StartedAt == nil || run.FinishedAt == nil {
		t.Errorf("the run's identity did not survive: %+v", run)
	}
	if len(run.Jobs) != 1 {
		t.Fatalf("only the job that failed is listed -- not the green one, nor the one skipped behind it: %+v", run.Jobs)
	}
	want := ui.CICompactJob{Name: "go", Conclusion: ci.ConclusionFailure, Step: "go test", Test: "TestThing",
		Error: `step "go test" exited 1`}
	if run.Jobs[0] != want {
		t.Errorf("failing job = %+v, want %+v", run.Jobs[0], want)
	}
	if green := got.Runs[1]; green.Conclusion != "success" || len(green.Jobs) != 0 {
		t.Errorf("a green run is its identity and nothing else: %+v", green)
	}

	// Unknown views are refused rather than answered with the full one.
	if rec := do(t, srv, http.MethodGet, "/api/ci/runs?view=tiny", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown view is a 400, got %d: %s", rec.Code, rec.Body)
	}
	// And the full view is still the default, and still answers to its name.
	full := decode[ui.CIRunsResponse](t, do(t, srv, http.MethodGet, "/api/ci/runs?view=full", ""))
	if len(full.Runs) != 2 || len(full.Runs[0].Jobs) != 3 {
		t.Errorf("view=full listing = %+v", full.Runs)
	}
}

// A shared fault is named once for the page, by digest, and each job
// that is part of it points at it -- rather than the whole fault, run
// list and all, riding along with the page.
func TestListCIRunsCompactViewNamesFaultsOnce(t *testing.T) {
	t.Parallel()
	runner := &fakeCIRunner{runs: []ci.Run{
		faultRun("run-1", "grain/task-238", baseTime.Add(-10*time.Minute), mirror405),
		faultRun("run-2", "grain/task-246", baseTime.Add(-40*time.Minute), mirror405),
		faultRun("run-3", "grain/task-251", baseTime.Add(-90*time.Minute), mirror405),
	}}
	srv, _ := ciServer(t, runner)

	rec := do(t, srv, http.MethodGet, "/api/ci/runs?view=compact", "")
	if body := rec.Body.String(); !strings.Contains(body, `"refs":3`) || strings.Contains(body, `"runId"`) {
		t.Errorf("a fault's refs and runs are counts in the compact view, not lists: %s", body)
	}
	got := decode[ui.CICompactRunsResponse](t, rec)
	if len(got.Faults) != 1 {
		t.Fatalf("want one fault named once, got %+v", got.Faults)
	}
	fault := got.Faults[0]
	if fault.Digest == "" || fault.JobName != "image" || fault.Runs != 3 || fault.Refs != 3 {
		t.Errorf("fault = %+v", fault)
	}
	for _, run := range got.Runs {
		if len(run.Jobs) != 1 || run.Jobs[0].Fault != fault.Digest || run.Jobs[0].Step != "build the image" {
			t.Errorf("run %s does not point at the fault: %+v", run.ID, run.Jobs)
		}
	}
}

// ?until= closes the window ?since= opens, so an audit can ask about a
// week that is over -- in both views, being a filter rather than a view.
func TestListCIRunsUntilClosesTheWindow(t *testing.T) {
	t.Parallel()
	now := time.Now()
	runner := &fakeCIRunner{runs: []ci.Run{
		{ID: "today", Repo: "acme/widgets", Ref: "grain-merge-queue/main/b3", StartedAt: timeAt(now.Add(-time.Hour)),
			Conclusion: ci.ConclusionFailure},
		{ID: "last-week", Repo: "acme/widgets", Ref: "grain-merge-queue/main/b2", StartedAt: timeAt(now.Add(-8 * 24 * time.Hour)),
			Conclusion: ci.ConclusionFailure},
		{ID: "long-ago", Repo: "acme/widgets", Ref: "grain-merge-queue/main/b1", StartedAt: timeAt(now.Add(-30 * 24 * time.Hour)),
			Conclusion: ci.ConclusionFailure},
		{ID: "branch", Repo: "acme/widgets", Ref: "grain/task-1", StartedAt: timeAt(now.Add(-8 * 24 * time.Hour)),
			Conclusion: ci.ConclusionFailure},
	}}
	srv, _ := ciServer(t, runner)

	compactIDs := func(query string) string {
		t.Helper()
		rec := do(t, srv, http.MethodGet, "/api/ci/runs?view=compact&"+query, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", query, rec.Code, rec.Body)
		}
		var out []string
		for _, run := range decode[ui.CICompactRunsResponse](t, rec).Runs {
			out = append(out, run.ID)
		}
		return strings.Join(out, ",")
	}
	for _, tc := range []struct{ query, want string }{
		{"until=7d", "last-week,branch,long-ago"},
		{"since=14d&until=7d", "last-week,branch"},
		{"since=14d&until=7d&ref=grain-merge-queue/&conclusion=failure", "last-week"},
	} {
		if got := compactIDs(tc.query); got != tc.want {
			t.Errorf("?%s listed %q, want %q", tc.query, got, tc.want)
		}
	}
	if rec := do(t, srv, http.MethodGet, "/api/ci/runs?until=whenever", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("an until that is not a time is a 400, got %d: %s", rec.Code, rec.Body)
	}
}
