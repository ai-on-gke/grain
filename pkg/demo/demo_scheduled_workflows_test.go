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

package demo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// pushDemoDeclarations commits files onto the simulated repo's main, as a
// walkthrough pushing its `.grain` declarations does.
func pushDemoDeclarations(t *testing.T, bare string, files map[string]string) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "work")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=walk", "GIT_AUTHOR_EMAIL=walk@grain.invalid",
			"GIT_COMMITTER_NAME=walk", "GIT_COMMITTER_EMAIL=walk@grain.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	run("", "clone", "-q", "--branch", "main", "file://"+bare, work)
	for name, body := range files {
		path := filepath.Join(work, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run(work, "add", "-A")
	run(work, "commit", "-qm", "declare")
	run(work, "push", "-q", "origin", "main")
}

const demoTestWorkflow = `name: x
on:
  schedule:
    - cron: "0 3 * * *"
jobs:
  smoke:
    runs-on: default
    steps:
      - run: echo ok
`

const demoTestCandidateSchedule = `workflow: x
base: candidate
daily:
  at: "15:00"
overlap: skip
`

// TestDemoScheduledWorkflowsSettleAGreenRunAndReviewARedOne is
// -scheduled-workflows end to end against the demo's own store: the
// simulated repo's `workflow:` schedule is read, Run now's firing is
// started on candidate by the reconciler, and finishing the -fake-ci run
// over POST /api/e2e/fake-ci/runs/{id}/finish settles a green one with no
// review and files exactly one for a red one, titled with the branch.
func TestDemoScheduledWorkflowsSettleAGreenRunAndReviewARedOne(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("the simulated GitHub needs git")
	}
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg, err := configureFakeCIDemo(ctx, store, ui.Config{}, repo)
	if err != nil {
		t.Fatalf("configureFakeCIDemo: %v", err)
	}
	cfg, bare, err := configureGitHubSimDemo(dir, cfg, repo)
	if err != nil {
		t.Fatalf("configureGitHubSimDemo: %v", err)
	}
	fake := cfg.CI.(*demoFakeCI)
	client, _ := cfg.GitHubBranches.(github.Client)
	deps, err := configureScheduledWorkflowsDemo(store, repo, bare, fake, client)
	if err != nil {
		t.Fatalf("configureScheduledWorkflowsDemo: %v", err)
	}
	if _, err := demoGit(bare, "rev-parse", "--verify", "refs/heads/candidate"); err != nil {
		t.Fatalf("the simulated repo has no candidate branch: %v", err)
	}
	pushDemoDeclarations(t, bare, map[string]string{
		".grain/workflows/x.yml":           demoTestWorkflow,
		".grain/schedules/x-candidate.yml": demoTestCandidateSchedule,
	})
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, Declarations: true}); err != nil {
		t.Fatalf("PutRepoConfig: %v", err)
	}
	now := time.Now().UTC()
	cycle := func() {
		t.Helper()
		if err := demoScheduledWorkflowCycle(ctx, deps, now); err != nil {
			t.Fatalf("cycle: %v", err)
		}
	}
	cycle()
	const id = "repo:schedule:acme:widgets:x-candidate"
	sched, err := store.GetSchedule(ctx, id)
	if err != nil || sched == nil {
		t.Fatalf("schedule %s not declared (err %v)", id, err)
	}

	finish := httptest.NewServer(scheduledWorkflowsDemoHandler(http.NotFoundHandler(), fake, bare))
	t.Cleanup(finish.Close)
	fireAndFinish := func(conclusion string) model.ScheduledWorkflowRun {
		t.Helper()
		if _, err := store.FireScheduleNow(ctx, *sched, now, model.RepoRef{}); err != nil {
			t.Fatalf("FireScheduleNow: %v", err)
		}
		cycle()
		row, err := store.GetScheduledWorkflowRun(ctx, id)
		if err != nil || row == nil || row.RunID == "" {
			t.Fatalf("firing not started: %+v (err %v)", row, err)
		}
		if row.Branch != "candidate" {
			t.Errorf("run started on %q, want candidate", row.Branch)
		}
		res, err := http.Post(finish.URL+"/api/e2e/fake-ci/runs/"+row.RunID+"/finish", "application/json",
			strings.NewReader(`{"conclusion":"`+conclusion+`","job":"smoke"}`))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("finishing %s: %s", row.RunID, res.Status)
		}
		cycle()
		cycle() // a second cycle files nothing more
		row, err = store.GetScheduledWorkflowRun(ctx, id)
		if err != nil || row == nil {
			t.Fatalf("firing gone: %v", err)
		}
		return *row
	}
	reviews := func() []model.Task {
		t.Helper()
		tasks, err := store.ListTasks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []model.Task
		for _, task := range tasks {
			if strings.HasPrefix(task.Title, "Review last night's") {
				out = append(out, task)
			}
		}
		return out
	}

	green := fireAndFinish(string(ci.ConclusionSuccess))
	if !green.ReviewNotNeeded || green.ReviewTaskID != "" {
		t.Errorf("green firing = %+v, want settled with no review", green)
	}
	if got := reviews(); len(got) != 0 {
		t.Errorf("a green run filed %d review(s)", len(got))
	}

	red := fireAndFinish(string(ci.ConclusionFailure))
	got := reviews()
	if len(got) != 1 || red.ReviewTaskID != got[0].ID {
		t.Fatalf("red firing %+v filed %d review(s), want exactly one it names", red, len(got))
	}
	if want := `Review last night's "x" workflow run on candidate`; got[0].Title != want || got[0].Base != "candidate" {
		t.Errorf("review = %q on %q, want %q on candidate", got[0].Title, got[0].Base, want)
	}

	// A run can be finished once, and only as a verdict.
	if _, err := fake.Finish(red.RunID, ci.ConclusionSuccess, ""); err == nil {
		t.Error("finishing a run that has already ended was accepted")
	}
	if _, err := fake.Finish("fake-ci-404", ci.ConclusionSuccess, ""); err == nil {
		t.Error("finishing a run that does not exist was accepted")
	}
	if _, err := fake.Finish(red.RunID, ci.ConclusionCancelled, ""); err == nil {
		t.Error("finishing a run as cancelled was accepted; Cancel is for that")
	}
}

func TestDemoFakeCIFinishWithOutputPopulatesSharedFaultSignature(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := &demoFakeCI{}
	finish := httptest.NewServer(scheduledWorkflowsDemoHandler(http.NotFoundHandler(), fake, ""))
	t.Cleanup(finish.Close)

	r1, err := fake.Start(ctx, ci.Request{Repo: "acme/widgets", Ref: "grain/task-101"})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := fake.Start(ctx, ci.Request{Repo: "acme/widgets", Ref: "grain/task-102"})
	if err != nil {
		t.Fatal(err)
	}

	const body = `{"conclusion":"failure","job":"test","step":"go test","output":"--- FAIL: TestWidget (0.01s)\n    widget_test.go:42: expected HTTP 200 OK from upstream widget service, got 502 Bad Gateway"}`
	for _, id := range []string{r1.ID, r2.ID} {
		res, err := http.Post(finish.URL+"/api/e2e/fake-ci/runs/"+id+"/finish", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("finishing %s: %s", id, res.Status)
		}
	}

	faults := ci.SharedFaults(fake.List(), time.Now().UTC(), 0)
	if len(faults) != 1 || len(faults[0].Refs) != 2 {
		t.Fatalf("SharedFaults = %+v, want 1 fault across 2 refs", faults)
	}
}
