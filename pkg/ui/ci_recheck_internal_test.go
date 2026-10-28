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

package ui

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
)

// A first verdict that names no job had nothing re-run, so a passing
// recheck does not turn it green (orchestrator.recheckVerdict).
func TestTaskRecheckVerdictKeepsAVerdictThatNamesNoJob(t *testing.T) {
	pass := ci.Run{Conclusion: ci.ConclusionSuccess, Jobs: []ci.JobRun{
		{Workflow: "tests", JobID: "go", Name: "go", Conclusion: ci.ConclusionSuccess},
	}}
	if conclusion, detail := taskRecheckVerdict("failure: tests/go", pass); conclusion != ci.ConclusionSuccess || detail != "" {
		t.Fatalf("a recheck of the failed job read %s %q, want success", conclusion, detail)
	}
	for _, first := range []string{
		"cancelled: superseded by run r2, started for the same task",
		"grain CI could not run: no workflow matched",
		"failure: ",
	} {
		if conclusion, detail := taskRecheckVerdict(first, pass); conclusion == ci.ConclusionSuccess || detail != first {
			t.Errorf("taskRecheckVerdict(%q, pass) = %s %q, want the first verdict unchanged", first, conclusion, detail)
		}
	}
}
