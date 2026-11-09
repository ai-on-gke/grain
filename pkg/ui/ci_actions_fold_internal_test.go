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

// foldRecordedTails puts the log tail grain recorded of a failed job on
// the forge's account of that job's failing step, and leaves a job that
// passed, or one whose steps already carry output, alone.
func TestFoldRecordedTails(t *testing.T) {
	run := ci.Run{Jobs: []ci.JobRun{
		{Name: "go-long", Conclusion: ci.ConclusionFailure, Steps: []ci.StepRun{
			{Name: "checkout", Conclusion: ci.ConclusionSuccess},
			{Name: "test", Conclusion: ci.ConclusionFailure},
		}},
		{Name: "lint", Conclusion: ci.ConclusionFailure},
		{Name: "go unit", Conclusion: ci.ConclusionSuccess, Steps: []ci.StepRun{{Name: "test"}}},
	}}
	recorded := ci.Run{Jobs: []ci.JobRun{
		{Name: "go-long", Conclusion: ci.ConclusionFailure, Steps: []ci.StepRun{{Name: "test", Output: "--- FAIL: TestThing\n"}}},
		{Name: "lint", Conclusion: ci.ConclusionFailure, Steps: []ci.StepRun{{Name: "lint", Output: "vet: oops\n"}}},
		{Name: "go unit", Conclusion: ci.ConclusionFailure, Steps: []ci.StepRun{{Name: "test", Output: "stale\n"}}},
	}}
	foldRecordedTails(&run, recorded)
	if got := run.Jobs[0].Steps; got[0].Output != "" || got[1].Output != "--- FAIL: TestThing\n" {
		t.Errorf("go-long's steps = %+v", got)
	}
	if got := run.Jobs[1].Steps; len(got) != 1 || got[0].Output != "vet: oops\n" {
		t.Errorf("a failed job with no steps should take the tail as one: %+v", got)
	}
	if got := run.Jobs[2].Steps; got[0].Output != "" {
		t.Errorf("a job the forge says passed took a recorded tail: %+v", got)
	}
}
