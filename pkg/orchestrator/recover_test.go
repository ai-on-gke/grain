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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// TestRecoverOrphanedRunsReturnsATaskToQueued is bwsalmon/agents#425's own
// scenario: a task_run row with no `finished_at` -- what a crashed
// daemon leaves behind -- and no branch pushed for it. Recovery must
// finish that run and hand the task back to task_ready, exactly what
// dispatch.Cycle could never do on its own (dispatch_test.go's own
// TestCycleLeavesAnAlreadyRunningTaskAlone; crash_test.go's own
// TestRestartAfterACrashMidRunDoesNotDoubleDispatchOrLoseTheRun proves
// the same run is neither lost nor redispatched before recovery ever
// runs).
func TestRecoverOrphanedRunsReturnsATaskToQueued(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	task := filedTask(t, ctx, store, "t1", model.RepoRef{Owner: "acme", Name: "widgets"})

	d := dispatch.Dispatch{TaskID: task.ID, RunID: "t1-1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)

	if st, err := store.State(ctx, task.ID); err != nil || st != model.StateRunning {
		t.Fatalf("state before recovery = %q (%v), want running", st, err)
	}

	if err := orchestrator.RecoverOrphanedRuns(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("RecoverOrphanedRuns: %v", err)
	}

	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateQueued {
		t.Fatalf("state after recovery = %q, want queued: an orphaned run must free the task", st)
	}

	occupied, err := store.LiveRunCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if occupied != 0 {
		t.Fatalf("occupied slots after recovery = %v, want none: the orphaned run must be finished", occupied)
	}
}

// A row whose run died holding an activity line is the same lie the
// stale line was, in a worse form: the run is not slow, it is gone, and
// the phrase describes work nothing is still doing. Every tool call a run
// makes now writes that line (mcp.Registry.AnnounceActivity), so every
// run has one to leave behind -- and recovery must take it with the run,
// the way an ordinary ending does (tests/e2e's own
// mcpserver_update_status_test.go covers that half).
//
// It comes free of finishing the run, and that is the point of asserting
// it here: Store.TaskActivity reads live runs only, so there is nothing
// to clear and nothing that can be forgotten, whichever way a run ends.
func TestRecoverOrphanedRunsTakesTheActivityLineWithIt(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	task := filedTask(t, ctx, store, "t1", model.RepoRef{Owner: "acme", Name: "widgets"})

	d := dispatch.Dispatch{TaskID: task.ID, RunID: "t1-1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	if _, err := store.SetTaskActivity(ctx, task.ID, "looking into slow typing in the UI", baseTime); err != nil {
		t.Fatalf("SetTaskActivity: %v", err)
	}
	if live, err := store.TaskActivityOf(ctx, task.ID); err != nil || live == nil {
		t.Fatalf("activity while the run is live = %+v (%v), want the phrase the run wrote", live, err)
	}

	if err := orchestrator.RecoverOrphanedRuns(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("RecoverOrphanedRuns: %v", err)
	}

	stale, err := store.TaskActivityOf(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stale != nil {
		t.Errorf("activity after recovery = %q, want nothing: the run that said it is gone", stale.Note)
	}
	all, err := store.TaskActivity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := all[task.ID]; ok {
		t.Errorf("the task list still shows %q for an orphaned run", all[task.ID].Note)
	}
}

// TestRecoverOrphanedRunsOpensAPullRequestForAnAlreadyPushedBranch covers
// the issue's own other half: a crashed run may have pushed its branch
// before dying, before ProcessResult ever ran to turn it into a pull
// request. Recovery must not strand that branch -- it mirrors
// finish_test.go's own TestProcessResultOpensAPullRequestForAPushedBranch,
// through RecoverOrphanedRuns instead of ProcessResult.
func TestRecoverOrphanedRunsOpensAPullRequestForAnAlreadyPushedBranch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	task := filedTask(t, ctx, store, "t1", model.RepoRef{Owner: "acme", Name: "widgets"})
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))

	d := dispatch.Dispatch{TaskID: task.ID, RunID: "t1-1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)

	if err := orchestrator.RecoverOrphanedRuns(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("RecoverOrphanedRuns: %v", err)
	}

	if len(sim.PullRequests) != 1 {
		t.Fatalf("expected one pull request, got %+v", sim.PullRequests)
	}
	if sim.PullRequests[0].Head != model.BranchName(task.ID) || sim.PullRequests[0].Base != "main" {
		t.Fatalf("got %+v", sim.PullRequests[0])
	}

	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateAwaitingSubmit {
		t.Fatalf("state = %q, want awaiting_submit: recovery opened its pull request and nobody has submitted it", st)
	}
}

// TestRecoverOrphanedRunsLeavesAClosedTasksBranchUnopened is
// ProcessResult's own race -- a close that lands while a run is still
// live -- restated for a run that never got the chance to notice because
// the process driving it crashed instead of finishing normally. Recovery
// must still free the task (it is finished either way), but must not
// open a pull request nobody wants merged.
func TestRecoverOrphanedRunsLeavesAClosedTasksBranchUnopened(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	task := filedTask(t, ctx, store, "t1", model.RepoRef{Owner: "acme", Name: "widgets"})
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))

	d := dispatch.Dispatch{TaskID: task.ID, RunID: "t1-1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)

	if err := store.Observe(ctx, model.Observation{TaskID: task.ID, ClosedAt: &baseTime}); err != nil {
		t.Fatalf("closing task: %v", err)
	}

	if err := orchestrator.RecoverOrphanedRuns(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("RecoverOrphanedRuns: %v", err)
	}

	if len(sim.PullRequests) != 0 {
		t.Fatalf("expected no pull request for a closed task, got %+v", sim.PullRequests)
	}

	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateClosed {
		t.Fatalf("state = %q, want closed", st)
	}
}

func TestRecoverOrphanedRunsLeavesARepairTaskQueued(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	task.AutoMerge = true
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("PutTask: %v", err)
	}
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))
	_ = openedMidRun(t, ctx, store, client, task)

	repairedAt := baseTime.Add(-time.Minute)
	if err := store.Observe(ctx, model.Observation{
		TaskID:             task.ID,
		MergeQueueRepairAt: &repairedAt,
		MergeQueueRepairs:  1,
	}); err != nil {
		t.Fatalf("recording repair: %v", err)
	}

	d := dispatch.Dispatch{TaskID: task.ID, RunID: "t1-2", Attempt: 2}
	startRun(t, ctx, store, d, baseTime)

	if err := orchestrator.RecoverOrphanedRuns(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("RecoverOrphanedRuns: %v", err)
	}

	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if !obs.RepairInFlight() || obs.CompletedAt != nil {
		t.Fatalf("RepairInFlight = %v, CompletedAt = %v; want interrupted repair to remain in flight",
			obs.RepairInFlight(), obs.CompletedAt)
	}
	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateQueued {
		t.Fatalf("state = %q, want queued so the interrupted repair is redispatched", st)
	}
}
