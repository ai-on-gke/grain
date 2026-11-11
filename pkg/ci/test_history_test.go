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
	"strings"
	"testing"
	"time"
)

func TestQueryTestHistory(t *testing.T) {
	now := time.Now()
	t1 := now.Add(-3 * time.Hour)
	t2 := now.Add(-2 * time.Hour)
	t3 := now.Add(-1 * time.Hour)

	runs := []Run{
		{
			ID:     "run-1",
			Repo:   "bwsalmon/grain",
			Ref:    "main",
			Commit: "111111111111",
			Jobs: []JobRun{
				{
					JobID:      "go",
					Name:       "go test",
					Conclusion: ConclusionSuccess,
					StartedAt:  &t1,
					Steps: []StepRun{
						{
							Output: "=== RUN   TestCalculatorAdd\n--- PASS: TestCalculatorAdd (0.01s)\nPASS\n",
						},
					},
				},
			},
		},
		{
			ID:     "run-2",
			Repo:   "bwsalmon/grain",
			Ref:    "grain/task-1",
			Commit: "222222222222",
			Jobs: []JobRun{
				{
					JobID:      "go",
					Name:       "go test",
					Conclusion: ConclusionFailure,
					StartedAt:  &t2,
					FailureSummary: &FailureSummary{
						Tests: []TestFailure{
							{
								Name:     "TestCalculatorAdd",
								Location: "calc_test.go:42",
								Message:  "expected 4, got 5",
							},
						},
					},
				},
			},
		},
		{
			ID:     "run-3",
			Repo:   "bwsalmon/grain",
			Ref:    "grain/task-2",
			Commit: "333333333333",
			Jobs: []JobRun{
				{
					JobID:      "go",
					Name:       "go test",
					Conclusion: ConclusionSuccess,
					Flake:      true,
					StartedAt:  &t3,
					FailureSummary: &FailureSummary{
						Tests: []TestFailure{
							{
								Name:     "TestCalculatorAdd",
								Location: "calc_test.go:42",
								Message:  "flaked on first attempt",
							},
						},
					},
				},
			},
		},
	}

	report := QueryTestHistory(runs, "TestCalculatorAdd")
	if report.TestName != "TestCalculatorAdd" {
		t.Errorf("expected TestName TestCalculatorAdd, got %s", report.TestName)
	}
	if report.RunsExamined != 3 {
		t.Errorf("expected RunsExamined 3, got %d", report.RunsExamined)
	}
	if report.Executions != 3 {
		t.Errorf("expected Executions 3, got %d", report.Executions)
	}
	if report.Passed != 1 {
		t.Errorf("expected Passed 1, got %d", report.Passed)
	}
	if report.Failed != 1 {
		t.Errorf("expected Failed 1, got %d", report.Failed)
	}
	if report.Flaked != 1 {
		t.Errorf("expected Flaked 1, got %d", report.Flaked)
	}

	rendered := report.Render()
	if !strings.Contains(rendered, "Test History: TestCalculatorAdd") {
		t.Errorf("rendered missing heading: %s", rendered)
	}
	if !strings.Contains(rendered, "Passed:  1 (33.3%)") {
		t.Errorf("rendered missing pass stats: %s", rendered)
	}
	if !strings.Contains(rendered, "Failed:  1 (33.3%)") {
		t.Errorf("rendered missing fail stats: %s", rendered)
	}
	if !strings.Contains(rendered, "Flaked:  1 (33.3%)") {
		t.Errorf("rendered missing flake stats: %s", rendered)
	}
}

// A run recorded before quarantined failures were kept in the summary
// still has them in its step output; they count as quarantined there too.
func TestQueryTestHistoryReadsQuarantineFromStepOutput(t *testing.T) {
	runs := []Run{{
		ID: "run-1", Repo: "bwsalmon/grain", Ref: "main",
		Jobs: []JobRun{{
			JobID: "go", Name: "go test", Conclusion: ConclusionSuccess,
			Quarantined: true, QuarantinedTests: []string{"TestFlaky"},
			Steps: []StepRun{{Output: "--- FAIL: TestFlaky (0.01s)\n--- FAIL: TestOther (0.01s)\nFAIL\n"}},
		}},
	}}
	if r := QueryTestHistory(runs, "TestFlaky"); r.Quarantined != 1 || r.Failed != 0 {
		t.Errorf("TestFlaky: %+v, want one quarantined", r)
	}
	if r := QueryTestHistory(runs, "TestOther"); r.Failed != 1 || r.Quarantined != 0 {
		t.Errorf("TestOther: %+v, want one failure", r)
	}
}

// A GitHub Actions run's jobs that passed are not passes of the test --
// grain keeps no log of them -- and the report names each source's runs
// and the window they span.
func TestQueryTestHistoryGitHubActionsCountsFailuresOnly(t *testing.T) {
	grainAt := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	first := time.Date(2026, 11, 8, 6, 0, 0, 0, time.UTC)
	second := time.Date(2026, 11, 9, 6, 0, 0, 0, time.UTC)
	gha := func(id string, at time.Time, failed bool) Run {
		run := Run{ID: id, Provider: ProviderGitHubActions, Repo: "acme/widgets", Ref: "main", StartedAt: &at,
			Actions: &ActionsRun{Workflow: ".github/workflows/nightly.yml"},
			Jobs:    []JobRun{{Name: "go unit", Conclusion: ConclusionSuccess, StartedAt: &at}}}
		if failed {
			run.Jobs = append(run.Jobs, JobRun{Name: "go-long", Conclusion: ConclusionFailure, StartedAt: &at,
				Steps: []StepRun{{Name: "test", Output: "--- FAIL: TestThing (0.10s)\n"}}})
		}
		return run
	}
	runs := []Run{
		gha("gha:acme:widgets:2", second, true),
		gha("gha:acme:widgets:1", first, false),
		{ID: "run-1", Repo: "acme/widgets", Ref: "main", StartedAt: &grainAt,
			Jobs: []JobRun{{Name: "go test", Conclusion: ConclusionSuccess, StartedAt: &grainAt}}},
	}
	report := QueryTestHistory(runs, "TestThing")
	if report.Failed != 1 || report.Passed != 1 || !report.PassesUncounted {
		t.Fatalf("report = %+v", report)
	}
	if got := report.RecentRuns[0]; got.Provider != ProviderGitHubActions || got.Workflow != "nightly.yml" || got.Status != TestStatusFail {
		t.Errorf("the Actions failure = %+v", got)
	}
	if len(report.Sources) != 2 || report.Sources[0].Provider != ProviderGitHubActions || report.Sources[0].Runs != 2 ||
		!report.Sources[0].Oldest.Equal(first) || !report.Sources[0].Newest.Equal(second) || report.Sources[1].Runs != 1 {
		t.Errorf("sources = %+v", report.Sources)
	}
	rendered := report.Render()
	for _, want := range []string{
		"GitHub Actions: 2 run(s) (2026-11-08 06:00 UTC .. 2026-11-09 06:00 UTC)",
		"Grain CI: 1 run(s) (2026-10-31 09:00 UTC)",
		"GitHub Actions runs count only this test's failures",
		`(nightly.yml, main) | job "go-long": FAIL`,
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the report does not say %q:\n%s", want, rendered)
		}
	}

	none := QueryTestHistory(runs[1:2], "TestThing").Render()
	if !strings.Contains(none, "No recorded failures of this test") {
		t.Errorf("an Actions history with no failure should say it saw none, not that the test never ran:\n%s", none)
	}
}

func TestQueryTestHistoryFallsBackToRunStartedAt(t *testing.T) {
	at := time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC)
	runs := []Run{{
		ID: "gha:acme:widgets:9505", Provider: ProviderGitHubActions, Repo: "acme/widgets", Ref: "main",
		StartedAt: &at, Conclusion: ConclusionFailure,
		Actions: &ActionsRun{Workflow: ".github/workflows/nightly.yml"},
		Jobs: []JobRun{{
			Name: "go-long", Conclusion: ConclusionFailure,
			Steps: []StepRun{{Name: "test", Output: "--- FAIL: TestNightly (1.00s)\n"}},
		}},
	}}
	rendered := QueryTestHistory(runs, "TestNightly").Render()
	if strings.Contains(rendered, "unknown time") || !strings.Contains(rendered, "2026-11-10 03:00 UTC | run gha:acme:widgets:9505") {
		t.Fatalf("expected execution to inherit run StartedAt rather than unknown time:\n%s", rendered)
	}
}
