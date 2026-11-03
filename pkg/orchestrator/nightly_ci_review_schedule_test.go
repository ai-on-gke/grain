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

package orchestrator_test

import (
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/repodecl"
)

// This repository's own .grain/schedules/nightly-ci-review.yml, as it is
// checked in: it loads as a github_workflow_run trigger on nightly.yml,
// and a red nightly run on main files a workflow review while a red run
// on a release candidate's tag (nightly.yml as Stage 1 of qualification,
// which the release train judges) or a green one on main files nothing.
func TestTheRepositorysNightlyCIReviewScheduleFilesAReviewForARedNightlyOnMain(t *testing.T) {
	data, err := os.ReadFile("../../.grain/schedules/nightly-ci-review.yml")
	if err != nil {
		t.Fatal(err)
	}
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	defer orchestrator.SetGitHubCIBudgetLow(false)()
	decls, err := repodecl.Load(fstest.MapFS{".grain/schedules/nightly-ci-review.yml": {Data: data}},
		model.RepoRef{Owner: "acme", Name: "widgets"})
	if err != nil {
		t.Fatalf("repodecl.Load: %v", err)
	}
	if len(decls.Schedules) != 1 {
		t.Fatalf("loaded %d schedules, want 1", len(decls.Schedules))
	}
	sched := decls.Schedules[0].Schedule
	trig := sched.Recurrence.GitHubWorkflowRun
	if sched.Recurrence.Kind != model.RecurrenceGitHubWorkflowRun || trig == nil || trig.Workflow != "nightly.yml" {
		t.Fatalf("recurrence = %+v, want a github_workflow_run trigger on nightly.yml", sched.Recurrence)
	}
	if got := strings.Join(trig.Conclusions, ","); got != "failure,timed_out,startup_failure" {
		t.Errorf("conclusions = %s, want failure,timed_out,startup_failure", got)
	}
	if got := strings.Join(trig.Branches, ","); got != "main" {
		t.Errorf("branches = %s, want main", got)
	}
	if sched.TemplateID == nil || *sched.TemplateID != model.WorkflowReviewTemplateID("nightly") {
		t.Errorf("template = %v, want %q", sched.TemplateID, model.WorkflowReviewTemplateID("nightly"))
	}
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatalf("PutSchedule: %v", err)
	}

	run := func(id int64, branch, conclusion string) githubsim.WorkflowRun {
		return githubsim.WorkflowRun{
			ID: id, Name: "nightly", Path: ".github/workflows/nightly.yml", HeadBranch: branch,
			HeadSHA: strings.Repeat(string(rune('a'+id%6)), 16), Event: "schedule", Status: "completed",
			Conclusion: conclusion, RunAttempt: 1,
			Jobs: []githubsim.WorkflowJob{{Name: "upgrade-test", Conclusion: conclusion, FailingStep: "go test ./tests/upgrade/...",
				Log: "2026-11-03T02:40:00.0000000Z --- FAIL: TestUpgradeFromReleasedVersions (61.0s)\n"}},
		}
	}
	sim.WorkflowRuns = []githubsim.WorkflowRun{
		run(9101, "v9.1.0-rc.1", "failure"), // Stage 1 on an rc: the train's to judge
		run(9102, "main", "success"),
		run(9103, "main", "failure"),
	}
	deps := orchestrator.Deps{Store: store, Client: client}
	runGitHubCITails(t, ctx, deps, baseTime)
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("filed %d tasks, want 1 review of the red nightly on main: %+v", len(tasks), tasks)
	}
	for _, want := range []string{"actions/runs/9103", "upgrade-test", "TestUpgradeFromReleasedVersions"} {
		if !strings.Contains(tasks[0].Body, want) {
			t.Errorf("review task body lacks %q:\n%s", want, tasks[0].Body)
		}
	}
	if strings.Contains(tasks[0].Body, "9101") {
		t.Errorf("review task names the rc's run 9101:\n%s", tasks[0].Body)
	}

	// overlap: skip -- a second red night while the first review is open
	// files nothing more.
	sim.WorkflowRuns = append(sim.WorkflowRuns, run(9104, "main", "timed_out"))
	runGitHubCITails(t, ctx, deps, baseTime.Add(time.Hour))
	if tasks, _ = store.ListTasks(ctx); len(tasks) != 1 {
		t.Errorf("filed %d tasks after a second red night with the first review open, want 1 (overlap: skip)", len(tasks))
	}
}
