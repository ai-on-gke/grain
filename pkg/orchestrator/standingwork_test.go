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

// A repository's standing work, switched off (grain/task-3332): the
// repository default off stops every piece of automated work grain does
// for it, a per-item override turns one piece back on, and nothing a
// person filed by hand is touched.
package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// standingWorkOff writes the switch: grain CI on for the repository, its
// standing work off, and whatever per-item overrides the test names.
func standingWorkOff(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef,
	edit func(*model.RepoConfig)) {

	t.Helper()
	cfg := model.RepoConfig{Repo: repo, CIMode: model.CIModeGrain,
		Work: model.WorkOwnership{Default: model.OwnershipOff}}
	if edit != nil {
		edit(&cfg)
	}
	if err := store.PutRepoConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
}

// The gke-labs/bouncer case: a row grain's own retries would re-ask
// ("never judged … asking grain CI again"), and a red one waiting on a
// repair. Off, neither starts anything, and neither is a verdict any more.
func TestStandingWorkOffStartsNoBaseBranchRunAndRetriesNothing(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	standingWorkOff(t, ctx, store, repo, nil)
	finished := baseTime
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: headSHA(t, client, repo, "main"),
		StartedAt: baseTime, FinishedAt: &finished, Attempts: 1,
		Conclusion: string(ci.ConclusionErrored), Detail: "a team refusal", PendingMerges: 2,
	}); err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 6; i++ {
		if err := runBaseBranchCIReconciler(t, ctx, store, client, runner,
			baseTime.Add(time.Duration(i)*10*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if n := runner.started(); n != 0 {
		t.Fatalf("%d base-branch runs were started for a repository whose standing work is off", n)
	}
	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || row == nil {
		t.Fatalf("main's row went missing: %+v (%v)", row, err)
	}
	if row.Conclusion != "" || row.RunID != "" || row.PendingMerges != 0 || row.Broken() {
		t.Fatalf("main's row reads %+v, want one standing down to no verdict and nothing owed", row)
	}
}

// A run already in flight when the switch goes off is cancelled, and the
// red row it replaced holds nobody: the merge queue merges a hand-filed
// change -- whose own CI still runs -- and does not call the base broken.
func TestStandingWorkOffHoldsNoMergeAndLeavesHandFiledCIRunning(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	standingWorkOff(t, ctx, store, repo, nil)

	first, firstBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	first = grantCI(t, ctx, store, first)
	second, _ := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)
	second = grantCI(t, ctx, store, second)
	// Hand-filed work's own CI is not standing work: it runs and goes green.
	greenGrainCI(t, ctx, store, client, runner, first, repo, firstBranch)
	greenGrainCI(t, ctx, store, client, runner, second, repo, "grain/task-t2")
	setMergeable(sim, true)

	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", RunID: "old", Commit: headSHA(t, client, repo, "main"),
		StartedAt: baseTime, Conclusion: model.BaseBranchCIFailure, Detail: "the `go` job failed",
		Confirmation: model.BaseCIConfirmed, MergedTaskID: "t0", RepairTaskID: "t9",
	}); err != nil {
		t.Fatal(err)
	}

	syncWithCI(t, ctx, store, client, runner, baseTime)
	if !mergedBranch(sim, firstBranch) {
		t.Fatal("a green, hand-filed change did not merge on a repository whose standing work is off")
	}
	if state, note := mergeQueueStateOf(t, ctx, store, second.ID); state == "base_broken" {
		t.Fatalf("the waiting change is told main is broken (%q), but nothing is judging main", note)
	}

	before := runner.started()
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if runner.started() != before {
		t.Fatal("the merge started a base-branch run on a repository whose standing work is off")
	}
	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || row == nil {
		t.Fatal(err)
	}
	if row.Broken() || row.RepairTaskID != "" || row.Confirmation != "" {
		t.Fatalf("main's row still reads as a red verdict: %+v", row)
	}
}

// An item explicitly on under a default off runs: the base-branch check
// judges the branch, and the triage it would file on a red stays off
// with the default.
func TestABaseCheckSwitchedBackOnRunsButFilesNoRepair(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	standingWorkOff(t, ctx, store, repo, func(cfg *model.RepoConfig) {
		cfg.Work = cfg.Work.With(model.BaseBranchCIItem, model.OwnershipOn)
	})
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", StartedAt: baseTime,
	}); err != nil {
		t.Fatal(err)
	}
	row := baseBranchRunAfterMerge(t, ctx, store, client, runner, repo, "main", baseTime.Add(time.Second))

	row.Conclusion, row.Confirmation, row.Detail = model.BaseBranchCIFailure, model.BaseCIConfirmed, "the `go` job failed"
	if err := store.PutBaseBranchCI(ctx, *row); err != nil {
		t.Fatal(err)
	}
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if got.RepairTaskID != "" {
		t.Fatalf("repair %s was filed, but grain's triage follows the repository default, which is off",
			got.RepairTaskID)
	}
	if !got.Broken() {
		t.Fatal("the judged branch's red verdict was dropped although its check is switched on")
	}
}

// The deployment's own configuration repository is judged whatever its
// standing work says: merging a dump grain cannot load stops grain.
func TestTheConfigRepositoryIsJudgedWithItsStandingWorkOff(t *testing.T) {
	store, ctx := openStore(t)
	repo := model.ConfigRepo()
	_, client := newSim(t, repo.Owner, repo.Name, "main")
	runner := newFakeCI()
	standingWorkOff(t, ctx, store, repo, nil)
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", StartedAt: baseTime,
	}); err != nil {
		t.Fatal(err)
	}
	baseBranchRunAfterMerge(t, ctx, store, client, runner, repo, "main", baseTime.Add(30*time.Second))
}

// Post-merge jobs, the guest cache job grain adds unasked among them, are
// standing work: off, none is dispatched; switched back on by their own
// item, they are.
func TestStandingWorkOffDispatchesNoPostMergeJobs(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	standingWorkOff(t, ctx, store, repo, func(cfg *model.RepoConfig) {
		cfg.PostMergeDispatchJobs = []string{guestCacheJob}
	})

	runBaseBranchDispatchReconciler(t, ctx, store, client, runner, baseTime)
	if got := runner.dispatches(); len(got) != 0 {
		t.Fatalf("dispatched %v for a repository whose standing work is off", got)
	}

	standingWorkOff(t, ctx, store, repo, func(cfg *model.RepoConfig) {
		cfg.PostMergeDispatchJobs = []string{guestCacheJob}
		cfg.Work = cfg.Work.With(model.PostMergeItem, model.OwnershipOn)
	})
	runBaseBranchDispatchReconciler(t, ctx, store, client, runner, baseTime.Add(time.Minute))
	if got := runner.dispatches(); len(got) != 1 {
		t.Fatalf("dispatched %v with post-merge switched back on, want the job it names", got)
	}
}

// A hand-filed change still merges, and no port is filed for it; with
// ports switched back on, one is.
func TestStandingWorkOffFilesNoPort(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ports model.Ownership
		want  int
	}{
		{name: "default off", ports: model.OwnershipUnset, want: 0},
		{name: "ports on", ports: model.OwnershipOn, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, ctx := openStore(t)
			sim, client, repo := pairedSim(t, ctx, store, "main", "candidate")
			if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, PortBranches: []string{"main", "candidate"},
				Work: model.WorkOwnership{Default: model.OwnershipOff}.With(model.PortsItem, tc.ports)}); err != nil {
				t.Fatal(err)
			}
			task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
			mergeQueued(t, ctx, store, sim, client, task, branch)
			if got := len(portsOf(t, ctx, store, task.ID)); got != tc.want {
				t.Fatalf("filed %d ports, want %d", got, tc.want)
			}
		})
	}
}

// A release switched back on by its own item still publishes, and still
// files no triage when it fails: triage follows the default.
func TestStandingWorkOffFilesNoReleaseTriage(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	standingWorkOff(t, ctx, store, repo, func(cfg *model.RepoConfig) {
		cfg.Work = cfg.Work.With(model.ReleaseItem, model.OwnershipOn).
			With(model.BaseBranchCIItem, model.OwnershipOn)
	})

	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	greenGrainCI(t, ctx, store, client, runner, task, repo, branch)
	setMergeable(sim, true)
	syncWithCI(t, ctx, store, client, runner, baseTime)
	if !mergedBranch(sim, branch) {
		t.Fatal("the change did not merge")
	}
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	runBaseBranchReleaseReconciler(t, ctx, store, client, runner, baseTime.Add(2*time.Second))
	release, err := store.GetBaseBranchRelease(ctx, repo, "main")
	if err != nil || release == nil {
		t.Fatalf("a release switched back on published nothing: %+v (%v)", release, err)
	}
	runner.finish(release.RunID, ci.ConclusionFailure, ci.JobRun{
		WorkflowPath: ".grain/workflows/release.yml", JobID: "docker", Conclusion: ci.ConclusionFailure,
	})
	runBaseBranchReleaseReconciler(t, ctx, store, client, runner, baseTime.Add(time.Minute))
	id, err := store.TaskIDWithTag(ctx, orchestrator.BaseReleaseTriageTag(repo, "main", release.Commit))
	if err != nil {
		t.Fatal(err)
	}
	if id != "" {
		t.Fatalf("release triage %s was filed for a repository whose triage is off", id)
	}
}

// No schedule fires into a repository whose standing work is off: each
// occurrence is skipped, the skip is the schedule's status, and the
// cadence moves on -- so turning the work back on fires the next
// occurrence once rather than a backlog. A schedule switched back on by
// its own item fires throughout.
func TestStandingWorkOffFiresNoScheduleAndOwesNoCatchUp(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	standingWorkOff(t, ctx, store, repo, func(cfg *model.RepoConfig) {
		cfg.Work = cfg.Work.With(model.ScheduleItem("kept"), model.OwnershipOn)
	})
	filedSchedule(t, store, "skipped", baseTime.Add(-time.Minute))
	filedSchedule(t, store, "kept", baseTime.Add(-time.Minute))

	for i := range 3 {
		if err := runScheduleOnly(t, store, baseTime.Add(time.Duration(i)*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if filed := scheduledTasksTagged(t, store, "schedule:skipped"); len(filed) != 0 {
		t.Fatalf("filed %d tasks into a repository whose standing work is off", len(filed))
	}
	if filed := scheduledTasksTagged(t, store, "schedule:kept"); len(filed) == 0 {
		t.Fatal("a schedule switched back on by its own item filed nothing")
	}
	after, err := store.GetSchedule(ctx, "skipped")
	if err != nil {
		t.Fatal(err)
	}
	if after.LastRunAt != nil {
		t.Errorf("lastRunAt = %v, want nil: nothing ran", after.LastRunAt)
	}
	if !after.NextRunAt.After(baseTime.Add(48 * time.Hour)) {
		t.Errorf("nextRunAt = %v, want it past every skipped occurrence", after.NextRunAt)
	}
	why, err := store.SuppressedFiring(ctx, *after, baseTime.Add(49*time.Hour), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if why == nil || why.Reason != model.SuppressedByStandingWork ||
		why.Detail != "skipped: this deployment doesn't run acme/widgets's standing work" {
		t.Fatalf("the schedule's status reads %+v, want the standing-work skip said on it", why)
	}

	// Back on: the next occurrence fires once, and only once.
	standingWorkOff(t, ctx, store, repo, func(cfg *model.RepoConfig) {
		cfg.Work.Default = model.OwnershipUnset
	})
	if err := runScheduleOnly(t, store, baseTime.Add(72*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if filed := scheduledTasksTagged(t, store, "schedule:skipped"); len(filed) != 1 {
		t.Fatalf("filed %d tasks once the work was back on, want the one occurrence that came due", len(filed))
	}
	if why, err := store.SuppressedFiring(ctx, *after, baseTime.Add(72*time.Hour), time.UTC); err != nil ||
		(why != nil && why.Reason == model.SuppressedByStandingWork) {
		t.Fatalf("the schedule still reads skipped (%+v, %v) with its work back on", why, err)
	}
}

// The switch beats the overlap check: a schedule whose last firing's
// task is still open, with its work then switched off, skips the next
// occurrence for the switch and moves on, rather than holding it for the
// task -- which would owe that occurrence as a catch-up the moment the
// task closed, and record a skip naming the task while the schedule's
// status names the switch. (The demo's sched-1, firing itself at
// midnight in the middle of ui/e2e/standing-work-switch-walkthrough.spec.js.)
func TestStandingWorkOffSkipsForTheSwitchOverAnOpenTask(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedSchedule(t, store, "held", baseTime.Add(-time.Minute))
	if err := runScheduleOnly(t, store, baseTime); err != nil {
		t.Fatal(err)
	}
	if filed := scheduledTasksTagged(t, store, "schedule:held"); len(filed) != 1 {
		t.Fatalf("filed %d tasks with the work on, want 1 left open", len(filed))
	}

	standingWorkOff(t, ctx, store, repo, nil)
	due := baseTime.Add(24 * time.Hour)
	if err := runScheduleOnly(t, store, due); err != nil {
		t.Fatal(err)
	}
	skips, err := store.ScheduleSkips(ctx, "held")
	if err != nil {
		t.Fatal(err)
	}
	if len(skips) != 1 || skips[0].Reason != model.SuppressedByStandingWork {
		t.Fatalf("skips = %+v, want the one occurrence skipped for the switch", skips)
	}
	after, err := store.GetSchedule(ctx, "held")
	if err != nil {
		t.Fatal(err)
	}
	if !after.NextRunAt.After(due) {
		t.Errorf("nextRunAt = %v, want the cadence moved past the skipped occurrence", after.NextRunAt)
	}
	if filed := scheduledTasksTagged(t, store, "schedule:held"); len(filed) != 1 {
		t.Fatalf("filed %d tasks, want only the one from before the switch", len(filed))
	}
}

// A workflow cron fires no CI run into a repository whose standing work
// is off, and a firing recorded before the switch is dropped rather than
// started.
func TestStandingWorkOffStartsNoScheduledWorkflowRun(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := workflowSchedule(t, store, baseTime.Add(-time.Minute))
	runner := newFakeCI()
	deps := workflowDeps(store, runner)

	// Recorded while the work was on, not yet started.
	if err := store.PutScheduledWorkflowRun(ctx, model.ScheduledWorkflowRun{
		ScheduleID: sched.ID, Stem: "nightly", Repo: declaringRepo, FiredAt: baseTime.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	standingWorkOff(t, ctx, store, declaringRepo, nil)
	for i := range 2 {
		if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Duration(i)*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if n := runner.started(); n != 0 {
		t.Fatalf("%d scheduled workflow runs were started in a repository whose standing work is off", n)
	}
	if row, err := store.GetScheduledWorkflowRun(ctx, sched.ID); err != nil || row != nil {
		t.Fatalf("a firing is still recorded (%+v, %v), want none", row, err)
	}
}
