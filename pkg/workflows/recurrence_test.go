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

package workflows

import "testing"

func TestGitHubWorkflowRunTriggerMatchesBranch(t *testing.T) {
	noBranches := GitHubWorkflowRunTrigger{Workflow: "nightly.yml"}
	if noBranches.MatchesBranch("feature", "") {
		t.Fatal("MatchesBranch(\"feature\", \"\") with empty Branches = true, want false")
	}
	if noBranches.MatchesBranch("main", "") {
		t.Fatal("MatchesBranch(\"main\", \"\") with empty Branches and unresolved fallback = true, want false")
	}
	if !noBranches.MatchesBranch("main", "main") {
		t.Fatal("MatchesBranch(\"main\", \"main\") with empty Branches = false, want true")
	}
	if !noBranches.MatchesBranch("refs/heads/main", "refs/heads/main") {
		t.Fatal("MatchesBranch(\"refs/heads/main\", \"refs/heads/main\") = false, want true")
	}
	if noBranches.MatchesBranch("feature", "main") {
		t.Fatal("MatchesBranch(\"feature\", \"main\") with empty Branches = true, want false")
	}

	explicit := GitHubWorkflowRunTrigger{
		Workflow: "nightly.yml",
		Branches: []string{"main", "release/1.0"},
	}
	if !explicit.MatchesBranch("main", "") || !explicit.MatchesBranch("release/1.0", "") {
		t.Fatal("explicit Branches failed to match listed branch")
	}
	if explicit.MatchesBranch("feature", "main") {
		t.Fatal("explicit Branches matched unlisted branch")
	}
}
