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

package cli

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
)

// A selective wait naming a job the run never had is not green: a
// mistyped -jobs would otherwise pass once the run finished.
func TestWaitVerdictJobTheRunNeverHadIsNotGreen(t *testing.T) {
	run := ci.Run{ID: "run-1", Conclusion: ci.ConclusionFailure, Jobs: []ci.JobRun{
		{Name: "go unit", JobID: "go-unit", Conclusion: ci.ConclusionSuccess},
		{Name: "lint", JobID: "lint", Conclusion: ci.ConclusionFailure},
	}}

	got := WaitVerdict(run, []string{"go unti"}, 0)
	if got.Status != "failed" || !strings.Contains(got.Error, "go unti") {
		t.Fatalf("wait for a job the run lacks = %q (%q), want failed naming it", got.Status, got.Error)
	}

	got = WaitVerdict(run, []string{"go-unit", "nope"}, 0)
	if got.Status != "failed" {
		t.Fatalf("wait for one real and one missing job = %q, want failed", got.Status)
	}

	got = WaitVerdict(run, []string{"go-unit"}, 0)
	if got.Status != "passed" || got.Error != "" {
		t.Fatalf("wait for a job that passed = %q (%q), want passed", got.Status, got.Error)
	}
}
