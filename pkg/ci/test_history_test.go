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
