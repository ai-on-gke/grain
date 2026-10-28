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

package orchestrator

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
)

// A change whose merge rules make GitHub's checks a gate beside grain CI
// (`ci.mode: both`) names GitHub's unfinished checks when it waits or
// stalls. It used to read "waiting on no check reported against it at
// all" with `cla/google` still queued, because a Required grain verdict
// set GitHub's checks aside (docs/test-runs/2026-10-05-merge-rules.md, F2).
func TestABothModeWaitNamesGitHubsUnfinishedChecks(t *testing.T) {
	done := "success"
	checks := []github.CheckRun{
		{Name: "build", Status: "completed", Conclusion: &done},
		{Name: "cla/google", Status: "queued"},
	}
	both := grainCIVerdict{Required: true, Passed: true, GitHubToo: true}
	if got := stalledChecksPhrase(checks, both); !strings.Contains(got, "`cla/google`") {
		t.Fatalf("both-mode wait = %q, want it to name cla/google", got)
	}
	grainOnly := grainCIVerdict{Required: true, Passed: true}
	if got := stalledChecksPhrase(checks, grainOnly); strings.Contains(got, "cla/google") {
		t.Fatalf("a grain-only gate named a GitHub check it does not read: %q", got)
	}
}
