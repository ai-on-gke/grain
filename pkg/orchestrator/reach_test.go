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

// The filing-time reach check (gitproxy.Reach.Refusal) on the three
// paths this package files tasks through itself, none of which reach
// ui.Client.CreateTask (grain/task-1857): the end-of-run replay of
// propose_task, a schedule's firing, and a suite run's passes. Each
// files the task and parks it rather than refusing it -- they are
// automation, and refusing would lose the work.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

var (
	reachableRepo   = model.RepoRef{Owner: "gke-labs", Name: "bouncer"}
	unreachableRepo = model.RepoRef{Owner: "acme", Name: "widgets"}
)

// teamReach files team "bouncer", whose credential GitHub (the
// simulator) says reaches gke-labs/bouncer with push and nothing else,
// and returns the Reach that asks it.
func teamReach(t *testing.T, ctx context.Context, store *model.Store) *gitproxy.Reach {
	t.Helper()
	credentials, err := gitproxy.LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := credentials.SetToken("bouncer-cred", "tok-bouncer"); err != nil {
		t.Fatal(err)
	}
	sim := githubsim.New("acme", "widgets", "", "main")
	sim.Reachable = map[string][]githubsim.ReachableRepo{
		"tok-bouncer": {{FullName: "gke-labs/bouncer", Push: true}},
	}
	// The credential is the deployment's git connection, which team
	// bouncer's tasks reach with like every other team's
	// (docs/single-tenant-reset.md, stage 1).
	if err := store.UpdateConfig(ctx, func(tm *model.Config) error {
		tm.GitCredential = "bouncer-cred"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return &gitproxy.Reach{Credentials: credentials, API: sim}
}

// assertParkedOnReach fails unless taskID is parked on the reach
// comment naming repo.
func assertParkedOnReach(t *testing.T, ctx context.Context, store *model.Store, taskID string, repo model.RepoRef) {
	t.Helper()
	obs, err := store.GetObservation(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.PendingQuestionCommentID == nil {
		t.Fatalf("task %s is not parked; want it parked on its team's reach", taskID)
	}
	comments, err := store.Comments(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	var body string
	for _, c := range comments {
		if c.ID == *obs.PendingQuestionCommentID {
			body = c.Body
		}
	}
	for _, want := range []string{"cannot be dispatched yet", "the deployment's", repo.String(), "Settings → Git"} {
		if !strings.Contains(body, want) {
			t.Errorf("task %s parked on %q; want it to mention %q", taskID, body, want)
		}
	}
}

func assertNotParked(t *testing.T, ctx context.Context, store *model.Store, taskID string) {
	t.Helper()
	obs, err := store.GetObservation(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if obs != nil && obs.PendingQuestionCommentID != nil {
		t.Errorf("task %s is parked; want a reachable task left alone", taskID)
	}
}

func TestProposedTaskTheTeamCannotReachIsFiledAndParked(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	reach := teamReach(t, ctx, store)
	_, client := newSim(t, "acme", "widgets", "main")
	task := filedTask(t, ctx, store, "t1", unreachableRepo)
	// A repository the task declares is one its proposals may name.
	writes := []model.WriteTarget{{Repo: reachableRepo, Base: "main"}}
	if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
		tk.Writes = writes
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	task.Writes = writes

	result := toolResult(
		agent.ToolCall{Name: "propose_task", Arguments: map[string]any{
			"title": "unreachable", "body": "more work in acme/widgets",
		}},
		agent.ToolCall{Name: "propose_task", Arguments: map[string]any{
			"title": "reachable", "body": "work in gke-labs/bouncer", "repo": reachableRepo.String(),
		}},
	)
	if err := orchestrator.ProcessResultWithReach(ctx, store, reach, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]model.Task{}
	for _, tk := range tasks {
		byTitle[tk.Title] = tk
	}
	unreached, ok := byTitle["unreachable"]
	if !ok {
		t.Fatal("the unreachable proposal was not filed; it must be parked, not dropped")
	}
	if unreached.Target == nil || *unreached.Target != unreachableRepo {
		t.Fatalf("unreachable proposal targets %v, want %s", unreached.Target, unreachableRepo)
	}
	assertParkedOnReach(t, ctx, store, unreached.ID, unreachableRepo)
	reached, ok := byTitle["reachable"]
	if !ok {
		t.Fatal("the reachable proposal was not filed")
	}
	if reached.Target == nil || *reached.Target != reachableRepo {
		t.Fatalf("reachable proposal targets %v, want %s", reached.Target, reachableRepo)
	}
	assertNotParked(t, ctx, store, reached.ID)
}

func TestScheduleFiringTheTeamCannotReachIsFiledAndParked(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	reach := teamReach(t, ctx, store)
	for id, repo := range map[string]model.RepoRef{"sched-unreached": unreachableRepo, "sched-reached": reachableRepo} {
		sched := filedSchedule(t, store, id, baseTime.Add(-time.Minute))
		sched.Target = repo
		if err := store.PutSchedule(ctx, sched); err != nil {
			t.Fatal(err)
		}
	}

	if err := orchestrator.RunCycle(ctx, orchestrator.Deps{Store: store, GitReach: reach}, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	unreached := scheduledTasksTagged(t, store, "schedule:sched-unreached")
	if len(unreached) != 1 {
		t.Fatalf("schedule into an unreachable repo filed %d tasks, want 1 (parked, not dropped)", len(unreached))
	}
	assertParkedOnReach(t, ctx, store, unreached[0].ID, unreachableRepo)
	reached := scheduledTasksTagged(t, store, "schedule:sched-reached")
	if len(reached) != 1 {
		t.Fatalf("schedule into a reachable repo filed %d tasks, want 1", len(reached))
	}
	assertNotParked(t, ctx, store, reached[0].ID)
}

func TestSuitePassTheTeamCannotReachIsFiledAndParked(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	reach := teamReach(t, ctx, store)
	if err := store.PutTemplate(ctx, suiteSmokeTemplate()); err != nil {
		t.Fatal(err)
	}
	suite := model.Suite{
		Name: "smoke", Items: []model.SuiteItem{{TemplateID: "template-suite-smoke"}},
		Mode: model.SuiteCount, Count: 2,
	}
	runs := map[model.RepoRef]model.SuiteRun{}
	for _, repo := range []model.RepoRef{unreachableRepo, reachableRepo} {
		run, err := store.CreateSuiteRun(ctx, suite, repo, "main", baseTime)
		if err != nil {
			t.Fatal(err)
		}
		completeTask(t, ctx, store, onlyTask(t, run, 1), baseTime)
		runs[repo] = run
	}

	if err := orchestrator.RunCycle(ctx, orchestrator.Deps{Store: store, GitReach: reach}, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	for repo, run := range runs {
		updated, err := store.GetSuiteRun(ctx, run.ID)
		if err != nil || updated == nil {
			t.Fatalf("suite run %d: (%+v, %v)", run.ID, updated, err)
		}
		if updated.CurrentPass() != 2 {
			t.Fatalf("suite run against %s is on pass %d, want its second pass filed", repo, updated.CurrentPass())
		}
		next := onlyTask(t, *updated, 2)
		if repo == unreachableRepo {
			assertParkedOnReach(t, ctx, store, next, repo)
		} else {
			assertNotParked(t, ctx, store, next)
		}
	}
}
