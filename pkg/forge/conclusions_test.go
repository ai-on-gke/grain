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

package forge

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
)

// Every conclusion a completed check run can carry, and what HealthFrom
// makes of it standing alone on an otherwise mergeable pull request. The
// whole vocabulary is here so that a conclusion falling through to Clean
// is a deliberate row in this table rather than an accident of a switch
// that never named it.
func TestHealthFromClassifiesEveryConclusion(t *testing.T) {
	t.Parallel()
	conclusion := func(s string) *string { return &s }
	for _, tc := range []struct {
		name       string
		conclusion *string
		want       Health
	}{
		{"success", conclusion("success"), Clean},
		{"neutral", conclusion("neutral"), Clean},
		{"skipped", conclusion("skipped"), Clean},
		{"failure", conclusion("failure"), Failing},
		{"timed_out", conclusion("timed_out"), Failing},
		{"startup_failure", conclusion("startup_failure"), Failing},
		{"cancelled", conclusion("cancelled"), Pending},
		{"stale", conclusion("stale"), Pending},
		{"action_required", conclusion("action_required"), Pending},
		{"no conclusion", nil, Pending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			yes := true
			got := HealthFrom(github.PullRequestDetail{State: "open", Mergeable: &yes}, []github.CheckRun{
				{Name: "go", Status: "completed", Conclusion: tc.conclusion},
			}, "", true, true)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// The approval wait is named apart from the cancelled ones, so the person
// eventually told about it is told to approve the run rather than re-run it.
func TestAwaitingApprovalIsNamedApartFromCancelled(t *testing.T) {
	t.Parallel()
	conclusion := func(s string) *string { return &s }
	checks := []github.CheckRun{
		{Name: "deploy", Status: "completed", Conclusion: conclusion("action_required")},
		{Name: "go", Status: "completed", Conclusion: conclusion("cancelled")},
		{Name: "old", Status: "completed", Conclusion: conclusion("stale")},
		{Name: "odd", Status: "completed"},
		{Name: "lint", Status: "completed", Conclusion: conclusion("success")},
		{Name: "frontend", Status: "in_progress"},
	}
	if got := AwaitingApprovalChecks(checks); len(got) != 1 || got[0] != "deploy" {
		t.Errorf("AwaitingApprovalChecks = %q, want [deploy]", got)
	}
	got := CancelledChecks(checks)
	if len(got) != 3 || got[0] != "go" || got[1] != "old" || got[2] != "odd" {
		t.Errorf("CancelledChecks = %q, want [go old odd]", got)
	}
}
