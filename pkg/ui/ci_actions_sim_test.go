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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// docs/test-plans/2026-11-03.md §7 item 5 against githubsim rather than a
// fake reader: more than 100 workflow runs, newest first, the nightly ones
// buried behind PR runs. `grain ci list --provider github-actions
// --workflow nightly.yml --since 26h` (this endpoint) lists exactly the
// nightly runs inside the window, and the repository-wide listing pages
// past the first hundred through the sim's Link header.
func TestCIRunsListOverGitHubSimFindsNightlyRunsPastTheFirstHundred(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	sim := githubsim.New("acme", "widgets", filepath.Join(t.TempDir(), "none.git"), "main")
	for i := 0; i < 140; i++ {
		sim.AddWorkflowRun(githubsim.WorkflowRun{ID: int64(5000 - i), Name: "Tests", Path: ".github/workflows/tests.yml",
			HeadBranch: "grain/pr", HeadSHA: "abc", Event: "pull_request", CreatedAt: now.Add(-time.Duration(i+1) * 4 * time.Minute)})
	}
	for i, age := range []time.Duration{10 * time.Hour, 25 * time.Hour, 34 * time.Hour} {
		sim.AddWorkflowRun(githubsim.WorkflowRun{ID: int64(9000 + i), Name: "nightly", Path: ".github/workflows/nightly.yml",
			HeadBranch: "main", HeadSHA: "def", Event: "schedule", CreatedAt: now.Add(-age)})
	}
	srv, _, _ := actionsServerOver(t, github.NewClient(sim, nil), now)

	code, body := asAgent(t, srv, "reader", http.MethodGet,
		"/api/ci/runs?repo=acme/widgets&provider=github-actions&workflow=nightly.yml&since=26h", "", "")
	if code != http.StatusOK {
		t.Fatalf("workflow=nightly.yml since=26h: %d %s", code, body)
	}
	got := decodeString[ui.CIRunsResponse](t, body)
	if len(got.Runs) != 2 || got.Runs[0].ID != "gha:acme:widgets:9000" || got.Runs[1].ID != "gha:acme:widgets:9001" {
		t.Fatalf("nightly runs in the last 26h = %+v, want 9000 and 9001", got.Runs)
	}

	code, body = asAgent(t, srv, "reader", http.MethodGet,
		"/api/ci/runs?repo=acme/widgets&provider=github-actions&limit=200", "", "")
	if code != http.StatusOK {
		t.Fatalf("limit=200: %d %s", code, body)
	}
	all := decodeString[ui.CIRunsResponse](t, body)
	if len(all.Runs) != 143 {
		t.Fatalf("limit=200 listed %d runs, want all 143 across two sim pages", len(all.Runs))
	}
	pages := 0
	for _, c := range sim.Calls {
		if c.Method == "GET" && strings.HasPrefix(c.Path, "/repos/acme/widgets/actions/runs?") {
			pages++
		}
	}
	if pages < 2 {
		t.Fatalf("the repository-wide listing was read in %d page(s), want it to follow the Link header", pages)
	}
}
