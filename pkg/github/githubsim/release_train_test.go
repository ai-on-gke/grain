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

package githubsim

// The GitHub behaviour the release train reads and writes, which the sim
// answered unlike GitHub until the 2026-11-04 release-train walkthrough
// (docs/test-runs/2026-11-04-release-train.md, findings 3-5): check runs
// that record their App and commit and are updated one at a time, the
// tags listing, workflow dispatch and cancel, and the fields of a
// workflow run and its jobs that scripts/candidate-gate.sh reads.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
)

type appTokens struct{}

func (appTokens) TokenFor(string, string) *string           { t := "t"; return &t }
func (appTokens) AppSlugFor(string, string) (string, error) { return "grain-app", nil }

func releaseSim(t *testing.T) (*Sim, string) {
	t.Helper()
	bare := setupBareRepoWithBranch(t, "main")
	sim := New("acme", "widgets", bare, "main")
	sim.CheckRunApp = "grain-app"
	return sim, strings.TrimSpace(runOut(t, bare, "git", "rev-parse", "main"))
}

func runOut(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %v: %v", name, args, err)
	}
	return string(out)
}

func TestCheckRunsRecordTheirAppAndCommitAndUpdateOneAtATime(t *testing.T) {
	sim, sha := releaseSim(t)
	other := strings.Repeat("b", 40)
	client := github.NewClient(sim, appTokens{})
	start := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	first, err := client.CreateCheckRun("acme", "widgets", github.CheckRunWrite{
		Name: "grain/release-qualification", HeadSHA: sha, Status: "in_progress", ExternalID: "q:rc.1", StartedAt: &start,
		Output: &github.CheckRunOutput{Title: "Qualifying", Summary: "Stage 2 started"},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.CreateCheckRun("acme", "widgets", github.CheckRunWrite{
		Name: "grain/release-qualification", HeadSHA: other, Status: "in_progress", ExternalID: "q:rc.2", StartedAt: &start,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := start.Add(time.Hour)
	if err := client.UpdateCheckRun("acme", "widgets", first, github.CheckRunWrite{
		Name: "grain/release-qualification", Conclusion: "failure", CompletedAt: &done,
		Output: &github.CheckRunOutput{Title: "failure", Summary: "confirmed red"},
	}); err != nil {
		t.Fatal(err)
	}
	runs, err := client.ListCheckRuns("acme", "widgets", sha)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].App != "grain-app" || runs[0].HeadSHA != sha || runs[0].Status != "completed" ||
		runs[0].Conclusion == nil || *runs[0].Conclusion != "failure" || runs[0].CompletedAt == nil || !runs[0].CompletedAt.Equal(done) ||
		runs[0].StartedAt == nil || !runs[0].StartedAt.Equal(start) || runs[0].ExternalID != "q:rc.1" {
		t.Fatalf("check run on %s is %+v, want grain-app's, completed failure at %s", sha, runs, done)
	}
	// The same-named check run on the other commit is untouched.
	if got := sim.CheckRuns[other]; len(got) != 1 || got[0].ID != second || got[0].Status != "in_progress" || got[0].Conclusion != nil {
		t.Fatalf("updating %d changed %d too: %+v", first, second, got)
	}
	// Only the App that created a check run may update it.
	sim.CheckRunApp = "intruder"
	if err := client.UpdateCheckRun("acme", "widgets", second, github.CheckRunWrite{Conclusion: "success"}); err == nil {
		t.Fatal("another App updated grain-app's check run")
	}
	if err := client.UpdateCheckRun("acme", "widgets", 424242, github.CheckRunWrite{Conclusion: "success"}); err == nil {
		t.Fatal("updating a check run that does not exist succeeded")
	}
}

func TestCheckRunsListingHonoursCheckName(t *testing.T) {
	sim, sha := releaseSim(t)
	sim.CheckRuns = map[string][]github.CheckRun{sha: {
		{ID: 1, Name: "grain/release-qualification", Status: "completed"},
		{ID: 2, Name: "grain/rc-hold", Status: "in_progress"},
	}}
	resp, err := sim.Request("GET", "/repos/acme/widgets/commits/"+sha+"/check-runs?check_name=grain%2Frc-hold&filter=all", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		CheckRuns []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"check_runs"`
	}
	if err := json.Unmarshal(resp.Body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.CheckRuns) != 1 || got.CheckRuns[0].Name != "grain/rc-hold" {
		t.Fatalf("check_name=grain/rc-hold answered %+v", got.CheckRuns)
	}
}

func TestTagsAreListedPeeledAndPagedAndTheClientReadsEveryPage(t *testing.T) {
	sim, sha := releaseSim(t)
	for i := 0; i < 120; i++ {
		runOut(t, sim.BareRepo, "git", "-c", "user.name=t", "-c", "user.email=t@example.com",
			"tag", "-a", "-m", "m", fmt.Sprintf("v3.%d.0-rc.1", i), sha)
	}
	resp, err := sim.Request("GET", "/repos/acme/widgets/tags?per_page=100", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var page []struct {
		Name   string `json:"name"`
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := json.Unmarshal(resp.Body, &page); err != nil {
		t.Fatal(err)
	}
	if len(page) != 100 || page[0].Commit.SHA != sha || !strings.Contains(resp.Headers["Link"], `rel="next"`) {
		t.Fatalf("first page: %d tags, first %+v, Link %q; want 100 peeled to %s and a next page", len(page), page[0], resp.Headers["Link"], sha)
	}
	tags, err := github.NewClient(sim, nil).ListTags("acme", "widgets")
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 120 {
		t.Fatalf("ListTags read %d tags, want all 120", len(tags))
	}
	found := false
	for _, tg := range tags {
		found = found || tg.Name == "v3.0.0-rc.1"
	}
	if !found {
		t.Fatal("ListTags missed v3.0.0-rc.1, which is on the second page")
	}
}

func TestWorkflowDispatchQueuesARunAndCancelStopsOnlyThatRun(t *testing.T) {
	sim, sha := releaseSim(t)
	runOut(t, sim.BareRepo, "git", "tag", "v1.2.0-rc.1", sha)
	sim.WorkflowRuns = []WorkflowRun{
		{Name: "nightly", Path: ".github/workflows/nightly.yml", HeadBranch: "main", HeadSHA: sha, Event: "schedule", Status: "in_progress"},
		{Name: "nightly", Path: ".github/workflows/nightly.yml", HeadBranch: "main", HeadSHA: sha, Event: "schedule"},
	}
	mainID := int64(runIDBase + 500)
	resp, err := sim.Request("POST", "/repos/acme/widgets/actions/workflows/nightly.yml/dispatches", nil,
		[]byte(`{"ref":"v1.2.0-rc.1","inputs":{"jobs":"loadtest,vulns"}}`))
	if err != nil || resp.Status != 204 {
		t.Fatalf("dispatch: %d %s %v", resp.Status, resp.Body, err)
	}
	if len(sim.Dispatches) != 1 || sim.Dispatches[0].Ref != "v1.2.0-rc.1" || sim.Dispatches[0].Inputs["jobs"] != "loadtest,vulns" {
		t.Fatalf("dispatches: %+v", sim.Dispatches)
	}
	queued := sim.WorkflowRuns[0]
	if queued.ID != sim.Dispatches[0].RunID || queued.HeadBranch != "v1.2.0-rc.1" || queued.HeadSHA != sha ||
		queued.Status != "queued" || queued.Event != "workflow_dispatch" || queued.Name != "nightly" {
		t.Fatalf("the dispatch queued %+v", queued)
	}
	if sim.WorkflowRuns[1].ID != mainID {
		t.Fatalf("the earlier runs were renumbered: %+v", sim.WorkflowRuns[1])
	}
	// The queued run is listed newest first, with no conclusion yet.
	resp, _ = sim.Request("GET", "/repos/acme/widgets/actions/workflows/nightly.yml/runs?head_sha="+sha+"&per_page=100", nil, nil)
	var listed struct {
		WorkflowRuns []map[string]any `json:"workflow_runs"`
	}
	if err := json.Unmarshal(resp.Body, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.WorkflowRuns) != 3 || listed.WorkflowRuns[0]["head_branch"] != "v1.2.0-rc.1" ||
		listed.WorkflowRuns[0]["conclusion"] != nil || listed.WorkflowRuns[1]["conclusion"] != nil {
		t.Fatalf("listing: %+v", listed.WorkflowRuns)
	}
	if repo, _ := listed.WorkflowRuns[0]["repository"].(map[string]any); repo["full_name"] != "acme/widgets" {
		t.Fatalf("a workflow run carries no repository: %+v", listed.WorkflowRuns[0])
	}
	// Cancelling the rc's run cancels it, and nothing else.
	resp, _ = sim.Request("POST", fmt.Sprintf("/repos/acme/widgets/actions/runs/%d/cancel", queued.ID), nil, nil)
	if resp.Status != 202 || sim.WorkflowRuns[0].Conclusion != "cancelled" || sim.WorkflowRuns[1].Status != "in_progress" {
		t.Fatalf("cancel: %d, runs %+v", resp.Status, sim.WorkflowRuns)
	}
	resp, _ = sim.Request("POST", fmt.Sprintf("/repos/acme/widgets/actions/runs/%d/cancel", queued.ID), nil, nil)
	if resp.Status != 409 || len(sim.CancelledRuns) != 1 {
		t.Fatalf("cancelling a completed run: %d, cancelled %v", resp.Status, sim.CancelledRuns)
	}
	resp, _ = sim.Request("POST", "/repos/acme/widgets/actions/workflows/nightly.yml/dispatches", nil, []byte(`{"ref":"no-such-ref"}`))
	if resp.Status != 422 {
		t.Fatalf("a dispatch on a missing ref: %d", resp.Status)
	}
}

func TestRunJobsCarryTheirRunAndAttempt(t *testing.T) {
	sim, sha := releaseSim(t)
	sim.WorkflowRuns = []WorkflowRun{{
		ID: 77, Name: "nightly", Path: ".github/workflows/nightly.yml", HeadBranch: "v1.2.0-rc.1", HeadSHA: sha,
		Event: "push", RunAttempt: 2,
		Jobs: []WorkflowJob{{Name: "loadtest", Conclusion: "failure", RunAttempt: 1}, {Name: "vulns", RunAttempt: 1}, {Name: "loadtest"}},
	}}
	jobs := func(query string) []map[string]any {
		resp, err := sim.Request("GET", "/repos/acme/widgets/actions/runs/77/jobs?per_page=100"+query, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Jobs []map[string]any `json:"jobs"`
		}
		if err := json.Unmarshal(resp.Body, &got); err != nil {
			t.Fatal(err)
		}
		return got.Jobs
	}
	latest := jobs("")
	if len(latest) != 1 || latest[0]["name"] != "loadtest" || latest[0]["run_attempt"] != float64(2) {
		t.Fatalf("latest attempt's jobs: %+v", latest)
	}
	all := jobs("&filter=all")
	if len(all) != 3 || all[0]["run_attempt"] != float64(1) || all[0]["conclusion"] != "failure" ||
		all[0]["run_id"] != float64(77) || all[0]["head_sha"] != sha || all[0]["workflow_name"] != "nightly" {
		t.Fatalf("every attempt's jobs: %+v", all)
	}
	// The run concludes on its latest attempt: the retry passed.
	resp, _ := sim.Request("GET", "/repos/acme/widgets/actions/runs/77", nil, nil)
	var run map[string]any
	_ = json.Unmarshal(resp.Body, &run)
	if run["conclusion"] != "success" {
		t.Fatalf("a run whose retry passed concludes %v", run["conclusion"])
	}
}

func TestHandlerServesTheSimOverHTTP(t *testing.T) {
	sim, sha := releaseSim(t)
	sim.CheckRuns = map[string][]github.CheckRun{sha: {{ID: 1, Name: "grain/rc-hold", Status: "in_progress", App: "grain-app"}}}
	srv := httptest.NewServer(sim.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/repos/acme/widgets/commits/" + sha + "/check-runs?check_name=grain%2Frc-hold")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"slug":"grain-app"`) {
		t.Fatalf("GET over HTTP: %d %s", resp.StatusCode, body)
	}
	resp, err = http.Get(srv.URL + "/no/such/endpoint")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("an endpoint the sim has no answer for: %d, want 501", resp.StatusCode)
	}
}
