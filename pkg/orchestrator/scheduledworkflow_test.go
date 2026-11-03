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

// A workflow's own `on: schedule` cron, from the repository's tree to the
// run it starts (grain/task-344). Two halves:
//
//   - reading it, through a DeclarationSource that is the real
//     pkg/repodecl over a tree a test writes -- which is what makes the
//     `declarations` switch part of the assertion rather than an
//     implementation detail three packages away;
//   - firing it: the run grain starts at the due tick, and the comment
//     that tells the filed task which run is its own.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/repodecl"
)

// treeDeclarations is a declaring repository as a file tree, read by the
// same repodecl.Load a deployment reads a clone with.
type treeDeclarations struct {
	commit string
	files  map[string]string
	reads  int
}

func (d *treeDeclarations) Head(context.Context, model.RepoRef) (string, error) {
	return d.commit, nil
}

func (d *treeDeclarations) Read(_ context.Context, repo model.RepoRef) (model.RepoDeclarations, string, error) {
	d.reads++
	tree := fstest.MapFS{}
	for name, body := range d.files {
		tree[name] = &fstest.MapFile{Data: []byte(body)}
	}
	declared, err := repodecl.Load(tree, repo)
	return declared, d.commit, err
}

const nightlyWorkflowFile = `
name: nightly
on:
  schedule:
    - cron: "11 2 * * *"
jobs:
  go-long:
    steps:
      - run: make test-long
`

func nightlyWorkflowTree() *treeDeclarations {
	return &treeDeclarations{
		commit: "abc123",
		files:  map[string]string{ci.WorkflowDir + "/nightly.yml": nightlyWorkflowFile},
	}
}

// workflowScheduleID is what the translation calls the schedule
// .grain/workflows/nightly.yml declares.
func workflowScheduleID() string {
	return model.RepoDeclaredID(model.DeclaredSchedule, declaringRepo, "workflow.nightly")
}

func TestAWorkflowCronBecomesADailyScheduleInTheDeclaredNamespace(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	declarationsOn(t, store, ctx, true)
	source := nightlyWorkflowTree()

	runDeclarations(t, store, source, baseTime)

	sched, err := store.GetSchedule(ctx, workflowScheduleID())
	if err != nil || sched == nil {
		t.Fatalf("GetSchedule = %v, %v, want the schedule the workflow cron declares", sched, err)
	}
	want := model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 2*60 + 11}
	if sched.Recurrence != want {
		t.Fatalf("recurrence = %+v, want %+v", sched.Recurrence, want)
	}
	if sched.TemplateID == nil || *sched.TemplateID != model.WorkflowReviewTemplateID("nightly") {
		t.Fatalf("template = %v, want grain's own workflow review", sched.TemplateID)
	}
	if !sched.AutoApproveProposals {
		t.Fatalf("autoApproveProposals = %v, want true", sched.AutoApproveProposals)
	}
	if sched.NextRunAt.IsZero() || !sched.NextRunAt.After(baseTime) {
		t.Fatalf("nextRunAt = %v, want the first 02:11 after %v", sched.NextRunAt, baseTime)
	}
	row, err := store.GetRepoDeclaration(ctx, sched.ID)
	if err != nil || row == nil {
		t.Fatalf("provenance = %v, %v", row, err)
	}
	if row.Path != ci.WorkflowDir+"/nightly.yml" {
		t.Fatalf("provenance path = %q, want the workflow file", row.Path)
	}
}

// The switch is not negotiable: a workflow file is as much "a standing
// claim on this deployment's agent capacity, written in a repository whose
// files every run grain dispatches there can rewrite" as a
// `.grain/schedules` file is.
func TestAWorkflowCronDeclaresNothingWhileDeclarationsAreOff(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	declarationsOn(t, store, ctx, false)
	source := nightlyWorkflowTree()

	runDeclarations(t, store, source, baseTime)

	if source.reads != 0 {
		t.Fatalf("read the repository %d times, want none", source.reads)
	}
	if sched, err := store.GetSchedule(ctx, workflowScheduleID()); err != nil || sched != nil {
		t.Fatalf("schedule = %v, %v, want nothing at all", sched, err)
	}
}

// And turning it off afterwards takes the schedule away, the same as it
// does for a hand-written declaration.
func TestTurningDeclarationsOffRemovesAWorkflowCronSchedule(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	declarationsOn(t, store, ctx, true)
	source := nightlyWorkflowTree()
	runDeclarations(t, store, source, baseTime)

	declarationsOn(t, store, ctx, false)
	runDeclarations(t, store, source, baseTime.Add(time.Hour))

	if sched, err := store.GetSchedule(ctx, workflowScheduleID()); err != nil || sched != nil {
		t.Fatalf("schedule after the switch went off = %v, %v, want nothing", sched, err)
	}
}

func TestAWorkflowWhoseCronIsRemovedLosesItsSchedule(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	declarationsOn(t, store, ctx, true)
	source := nightlyWorkflowTree()
	runDeclarations(t, store, source, baseTime)

	source.commit = "def456"
	source.files[ci.WorkflowDir+"/nightly.yml"] = strings.Replace(nightlyWorkflowFile,
		"on:\n  schedule:\n    - cron: \"11 2 * * *\"\n", "on: workflow_dispatch\n", 1)
	runDeclarations(t, store, source, baseTime.Add(time.Hour))

	if sched, err := store.GetSchedule(ctx, workflowScheduleID()); err != nil || sched != nil {
		t.Fatalf("schedule after the cron was removed = %v, %v, want nothing", sched, err)
	}
}

func TestAChangedWorkflowCronMovesTheScheduleRatherThanDuplicatingIt(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	declarationsOn(t, store, ctx, true)
	source := nightlyWorkflowTree()
	runDeclarations(t, store, source, baseTime)

	source.commit = "def456"
	source.files[ci.WorkflowDir+"/nightly.yml"] = strings.Replace(nightlyWorkflowFile,
		"11 2 * * *", "30 4 * * *", 1)
	runDeclarations(t, store, source, baseTime.Add(time.Hour))

	all, err := store.ListSchedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("%d schedules, want the one moved rather than a second one", len(all))
	}
	if all[0].ID != workflowScheduleID() {
		t.Fatalf("id = %q", all[0].ID)
	}
	want := model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 4*60 + 30}
	if all[0].Recurrence != want {
		t.Fatalf("recurrence = %+v, want %+v", all[0].Recurrence, want)
	}
}

// defaultBranchClient answers the one question starting a scheduled run
// asks of GitHub: which branch a cron means.
type defaultBranchClient struct {
	github.Client
	branch string
	err    error
}

func (c defaultBranchClient) DefaultBranch(string, string) (string, error) {
	return c.branch, c.err
}

// workflowSchedule is the row the translation produces, filed directly and
// already due -- the reading half is asserted above, and this half is about
// what firing one does.
func workflowSchedule(t *testing.T, store *model.Store, nextRunAt time.Time) model.Schedule {
	t.Helper()
	templateID := model.WorkflowReviewTemplateID("nightly")
	sched := model.Schedule{
		ID:                   workflowScheduleID(),
		Title:                "Review last night's \"nightly\" workflow run",
		Target:               declaringRepo,
		TemplateID:           &templateID,
		Recurrence:           model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 2*60 + 11},
		Overlap:              model.OverlapSkip,
		Enabled:              true,
		AutoApproveProposals: true,
		NextRunAt:            nextRunAt,
		CreatedAt:            baseTime,
	}
	if err := store.PutSchedule(t.Context(), sched); err != nil {
		t.Fatalf("filing schedule: %v", err)
	}
	if _, err := store.SyncRepoDeclarations(t.Context(), declaringRepo, "abc123",
		model.RepoDeclarations{Schedules: []model.DeclaredScheduleFile{{
			Path: ci.WorkflowDir + "/nightly.yml", Schedule: sched,
		}}}, baseTime, time.UTC); err != nil {
		t.Fatalf("recording provenance: %v", err)
	}
	return sched
}

// workflowDeps is the deployment these tests fire into: a store, a fake
// CI engine and a GitHub client that answers the one question starting a
// scheduled run asks.
func workflowDeps(store *model.Store, runner ci.Runner) orchestrator.Deps {
	return orchestrator.Deps{Store: store, CI: runner,
		Client: defaultBranchClient{branch: "main"}}
}

// The firing half's central claim (grain/task-351): the run is started at
// the due tick and the review is filed when that run *finishes*, with its
// verdict already on the task.
func TestAWorkflowScheduleFilesItsReviewOnlyOnceTheRunHasFinished(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := workflowSchedule(t, store, baseTime.Add(-time.Minute))
	runner := newFakeCI()
	deps := workflowDeps(store, runner)

	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	if len(runner.requests) != 1 {
		t.Fatalf("started %d runs, want 1", len(runner.requests))
	}
	req := runner.requests[0]
	if req.Repo != declaringRepo.String() || req.Ref != "main" {
		t.Errorf("run is against %s %s, want the declaring repo's default branch", req.Repo, req.Ref)
	}
	if req.Event != ci.EventSchedule {
		t.Errorf("event = %q, want %q", req.Event, ci.EventSchedule)
	}
	if req.Workflow != ci.WorkflowDir+"/nightly.yml" {
		t.Errorf("workflow = %q, want the file the schedule was declared in", req.Workflow)
	}
	if len(req.Jobs) != 0 {
		t.Errorf("jobs = %v, want the whole workflow", req.Jobs)
	}

	// Nobody is dispatched to watch a suite that has just begun.
	if filed := scheduledTasksTagged(t, store, "schedule:"+sched.ID); len(filed) != 0 {
		t.Fatalf("filed %d tasks while the run was still going, want none", len(filed))
	}
	// Several cycles while it runs change nothing and start nothing.
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if runner.started() != 1 {
		t.Fatalf("started %d runs, want the one", runner.started())
	}

	runner.finish("run1", ci.ConclusionFailure,
		ci.JobRun{JobID: "go-long", Conclusion: ci.ConclusionFailure})
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(2*time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	filed := scheduledTasksTagged(t, store, "schedule:"+sched.ID)
	if len(filed) != 1 {
		t.Fatalf("filed %d tasks once the run finished, want 1", len(filed))
	}
	if !strings.Contains(filed[0].Body, ".grain/workflows/") {
		t.Errorf("the prompt does not send the agent to the workflow file: %q", filed[0].Body)
	}
	if !filed[0].AutoApproveProposals {
		t.Errorf("review task AutoApproveProposals = %v, want true", filed[0].AutoApproveProposals)
	}
	bodies := commentBodies(t, ctx, store, filed[0].ID)
	if len(bodies) != 1 {
		t.Fatalf("comments = %q, want one naming the run and its verdict", bodies)
	}
	for _, want := range []string{"run1", ci.WorkflowDir + "/nightly.yml", "main", "failure", "go-long"} {
		if !strings.Contains(bodies[0], want) {
			t.Errorf("the comment does not name %q: %s", want, bodies[0])
		}
	}
	// And the firing is recorded as done, naming the task that reads it.
	row, err := store.GetScheduledWorkflowRun(ctx, sched.ID)
	if err != nil || row == nil {
		t.Fatalf("GetScheduledWorkflowRun = %v, %v", row, err)
	}
	if row.ReviewTaskID != filed[0].ID || row.Conclusion != string(ci.ConclusionFailure) {
		t.Fatalf("row = %+v, want it to name the review and the verdict", row)
	}
}

// The review is filed before the row is marked as reviewed, so a daemon
// that stops between the two comes back to a firing that looks unreviewed
// and a task that already exists. It must finish the job rather than file
// a second review of the same night.
func TestAReviewFiledButNotRecordedIsNotFiledTwice(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := workflowSchedule(t, store, baseTime.Add(-time.Minute))
	runner := newFakeCI()
	deps := workflowDeps(store, runner)

	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	runner.finish("run1", ci.ConclusionFailure,
		ci.JobRun{JobID: "go-long", Conclusion: ci.ConclusionFailure})
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	filed := scheduledTasksTagged(t, store, "schedule:"+sched.ID)
	if len(filed) != 1 {
		t.Fatalf("filed %d tasks, want 1", len(filed))
	}

	// The write that never landed.
	row, err := store.GetScheduledWorkflowRun(ctx, sched.ID)
	if err != nil || row == nil {
		t.Fatalf("GetScheduledWorkflowRun = %v, %v", row, err)
	}
	row.ReviewTaskID = ""
	if err := store.PutScheduledWorkflowRun(ctx, *row); err != nil {
		t.Fatalf("rewinding the row: %v", err)
	}

	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(2*time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	again := scheduledTasksTagged(t, store, "schedule:"+sched.ID)
	if len(again) != 1 {
		t.Fatalf("filed %d tasks after the restart, want the one", len(again))
	}
	if bodies := commentBodies(t, ctx, store, again[0].ID); len(bodies) != 1 {
		t.Fatalf("comments = %q, want the one -- the review was not told twice", bodies)
	}
	// And the row is picked up where it was left, naming that task.
	recovered, err := store.GetScheduledWorkflowRun(ctx, sched.ID)
	if err != nil || recovered == nil || recovered.ReviewTaskID != again[0].ID {
		t.Fatalf("row = %v, %v, want it to name the review that was already filed", recovered, err)
	}
}

// Overlap covers both halves of a firing: while the run is going, and
// while the review of it is still being read. A nightly must not stack on
// a morning nobody has finished with.
func TestAWorkflowScheduleDoesNotFireOverItsOwnUnfinishedFiring(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := workflowSchedule(t, store, baseTime.Add(-time.Minute))
	runner := newFakeCI()
	deps := workflowDeps(store, runner)

	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	// A day later, with last night's run still going: no second run.
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(25*time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if runner.started() != 1 {
		t.Fatalf("started %d runs while the first was still going, want 1", runner.started())
	}

	// It finishes, the review is filed, and the cadence is still held --
	// this time by the review nobody has read.
	runner.finish("run1", ci.ConclusionFailure,
		ci.JobRun{JobID: "go-long", Conclusion: ci.ConclusionFailure})
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(26*time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	filed := scheduledTasksTagged(t, store, "schedule:"+sched.ID)
	if len(filed) != 1 {
		t.Fatalf("filed %d tasks, want 1", len(filed))
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(27*time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if runner.started() != 1 {
		t.Fatalf("started %d runs while the review was open, want 1", runner.started())
	}

	// Once somebody has read it, the next night runs.
	read := baseTime.Add(28 * time.Hour)
	if err := store.Observe(ctx, model.Observation{TaskID: filed[0].ID, ClosedAt: &read}); err != nil {
		t.Fatalf("closing the review: %v", err)
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(29*time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if runner.started() != 2 {
		t.Fatalf("started %d runs once the review closed, want 2", runner.started())
	}
	if len(scheduledTasksTagged(t, store, "schedule:"+sched.ID)) != 1 {
		t.Fatal("the new night filed its review before its run had finished")
	}
}

// A run that could not be started is a report, not a silence: an agent
// hunting for a run nobody asked for spends its whole budget on it, and a
// nightly that has stopped running is invisible from everywhere else.
func TestAWorkflowScheduleWhoseRunCannotStartFilesAReviewSayingSo(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := workflowSchedule(t, store, baseTime.Add(-time.Minute))
	runner := newFakeCI()
	deps := orchestrator.Deps{Store: store, CI: runner,
		Client: defaultBranchClient{err: errors.New("404 Not Found")}}

	// The failure is reported to the cycle as well as recorded, so
	// RunCycle is expected to return it -- every attempt, until grain
	// gives up on the night and files the review.
	now := baseTime
	for attempt := 0; attempt < orchestrator.ScheduledWorkflowStartAttempts; attempt++ {
		if err := orchestrator.RunCycle(ctx, deps, now); err == nil {
			t.Fatalf("attempt %d: RunCycle reported nothing about a run it could not start", attempt+1)
		}
		now = now.Add(orchestrator.ScheduledWorkflowStartDelay)
	}
	if len(runner.requests) != 0 {
		t.Fatalf("started %d runs with no branch to run against", len(runner.requests))
	}
	filed := scheduledTasksTagged(t, store, "schedule:"+sched.ID)
	if len(filed) != 1 {
		t.Fatalf("filed %d tasks, want 1 -- the review is filed either way", len(filed))
	}
	bodies := commentBodies(t, ctx, store, filed[0].ID)
	if len(bodies) != 1 || !strings.Contains(bodies[0], "could not start") {
		t.Fatalf("comments = %q, want one saying the run could not be started", bodies)
	}
	if !strings.Contains(bodies[0], "404 Not Found") {
		t.Errorf("the comment does not say why: %s", bodies[0])
	}
}

// A daemon that stopped over the night comes back to a run nothing
// concluded. That is still a report -- and it has to be, because nothing
// fires again until this firing is reviewed.
func TestAWorkflowRunLostWithTheDaemonIsStillReviewed(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := workflowSchedule(t, store, baseTime.Add(-time.Minute))
	runner := newFakeCI()
	deps := workflowDeps(store, runner)

	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	// A nightly on trunk is a release gate, so an interrupted run is
	// re-run before it is reviewed; the review is of the last of them.
	at, _ := spendInfraRetries(t, ctx, deps, runner, baseTime, runner.interrupt)
	if err := orchestrator.RunCycle(ctx, deps, at.Add(time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	filed := scheduledTasksTagged(t, store, "schedule:"+sched.ID)
	if len(filed) != 1 {
		t.Fatalf("filed %d tasks for an interrupted run, want 1", len(filed))
	}
	bodies := commentBodies(t, ctx, store, filed[0].ID)
	if len(bodies) != 1 || !strings.Contains(bodies[0], "interrupted") {
		t.Fatalf("comments = %q, want one saying the run was interrupted", bodies)
	}
}

// And a run that never ends at all does not hold the cadence for ever:
// grain stops waiting, says how long it waited, and lets tomorrow fire.
//
// That review is not the "there was no run" report the other two are,
// and this is the half of it that matters (grain/task-386): the run is
// still there and still going, most of its jobs have logs and the one
// that hung is still writing one, and where a nightly hangs is visible
// from nowhere else at all. So the comment names the run, says it has
// not finished, and sends the agent to read it -- rather than telling it
// to write one sentence and stop.
func TestAWorkflowRunThatNeverFinishesIsGivenUpOnAndReviewed(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := workflowSchedule(t, store, baseTime.Add(-time.Minute))
	runner := newFakeCI()
	deps := workflowDeps(store, runner)

	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(orchestrator.ScheduledWorkflowDeadline)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	filed := scheduledTasksTagged(t, store, "schedule:"+sched.ID)
	if len(filed) != 1 {
		t.Fatalf("filed %d tasks for a run that never finished, want 1", len(filed))
	}
	bodies := commentBodies(t, ctx, store, filed[0].ID)
	if len(bodies) != 1 || !strings.Contains(bodies[0], "stopped waiting") {
		t.Fatalf("comments = %q, want one saying grain stopped waiting", bodies)
	}
	// Which run to read, that it is unfinished, and the tool that reads
	// a log while it is still being written.
	for _, want := range []string{"run1", "has not finished", "still going", "get_ci_log"} {
		if !strings.Contains(bodies[0], want) {
			t.Errorf("the comment does not say %q: %s", want, bodies[0])
		}
	}
	if strings.Contains(bodies[0], "There is no run to review") {
		t.Errorf("the comment tells the reviewer there is nothing to read: %s", bodies[0])
	}
	// The run is left alone rather than cancelled: whatever it is stuck
	// on is the evidence.
	if len(runner.cancelled) != 0 {
		t.Errorf("cancelled %v, want the stuck run left where it is", runner.cancelled)
	}
	// And it is the row that remembers which kind of night this was, not
	// the wording of Detail: the review is often filed by a later cycle
	// than the one that gave up, and a daemon restarted in between has
	// nothing else to go on.
	row, err := store.GetScheduledWorkflowRun(ctx, sched.ID)
	if err != nil || row == nil {
		t.Fatalf("GetScheduledWorkflowRun = %v, %v", row, err)
	}
	if !row.Unfinished {
		t.Errorf("row = %+v, want it to record that the run had not finished", row)
	}
}

// The restart in the middle of that: grain gives up on the run, records
// it, and stops before the review is filed. The cycle that comes back to
// the row has only the row, and must still file the review that sends
// somebody to read a run which is still going.
func TestAReviewOfAnUnfinishedRunSurvivesARestartBeforeItIsFiled(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := workflowSchedule(t, store, baseTime.Add(-time.Minute))
	runner := newFakeCI()
	deps := workflowDeps(store, runner)

	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	// The state a daemon that stopped between the two writes comes back
	// to: the conclusion is on the row, and no review has been filed.
	row, err := store.GetScheduledWorkflowRun(ctx, sched.ID)
	if err != nil || row == nil {
		t.Fatalf("GetScheduledWorkflowRun = %v, %v", row, err)
	}
	gaveUp := baseTime.Add(orchestrator.ScheduledWorkflowDeadline)
	row.Conclusion, row.Unfinished, row.FinishedAt = string(ci.ConclusionErrored), true, &gaveUp
	row.Detail = "the run had still not finished 12h0m0s after the schedule came due, so grain " +
		"stopped waiting for it"
	if err := store.PutScheduledWorkflowRun(ctx, *row); err != nil {
		t.Fatalf("recording the conclusion the restart interrupted: %v", err)
	}

	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(13*time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	refiled := scheduledTasksTagged(t, store, "schedule:"+sched.ID)
	if len(refiled) != 1 {
		t.Fatalf("filed %d reviews after the restart, want 1", len(refiled))
	}
	bodies := commentBodies(t, ctx, store, refiled[0].ID)
	if len(bodies) != 1 || !strings.Contains(bodies[0], "has not finished") {
		t.Fatalf("comments = %q, want one that still says the run had not finished", bodies)
	}
}

// A workflow whose cron was removed this morning still owes a report on
// the run it started last night.
func TestAFiringIsReviewedEvenAfterItsScheduleHasGone(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := workflowSchedule(t, store, baseTime.Add(-time.Minute))
	runner := newFakeCI()
	deps := workflowDeps(store, runner)

	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	runner.finish("run1", ci.ConclusionFailure,
		ci.JobRun{JobID: "go-long", Conclusion: ci.ConclusionFailure})
	if err := store.DeleteSchedule(ctx, sched.ID); err != nil {
		t.Fatalf("deleting the schedule: %v", err)
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	filed := scheduledTasksTagged(t, store, "schedule:"+sched.ID)
	if len(filed) != 1 {
		t.Fatalf("filed %d tasks for a schedule that has gone, want 1", len(filed))
	}
	if filed[0].Target == nil || *filed[0].Target != declaringRepo {
		t.Errorf("review target = %v, want the repo the run was against", filed[0].Target)
	}
	// And with nothing left to replace the row, it is not kept for ever.
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(2*time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	row, err := store.GetScheduledWorkflowRun(ctx, sched.ID)
	if err != nil || row != nil {
		t.Fatalf("row = %v, %v, want it forgotten with its schedule", row, err)
	}
}

// A cron asks for the work, not for the answer (grain/task-347).
//
// grain CI serves the same commit's second run out of its cache in
// seconds, which on a day the branch has not moved would turn a firing
// into a replay of yesterday's verdict -- reviewed, honestly and
// wrongly, as a night that passed. A nightly's jobs are the ones whose
// repetition is the finding, so the request says so with
// ci.Request.Recheck and the suite runs again.
func TestAScheduledWorkflowRunIsNeverServedFromTheCache(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	workflowSchedule(t, store, baseTime.Add(-time.Minute))
	runner := newFakeCI()

	if err := orchestrator.RunCycle(ctx, workflowDeps(store, runner), baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	if len(runner.requests) != 1 {
		t.Fatalf("started %d runs, want 1", len(runner.requests))
	}
	if !runner.requests[0].Recheck {
		t.Error("the firing asked for an answer rather than for the work: an unmoved commit " +
			"would be reviewed on yesterday's cached verdict")
	}
}

// The run grain's own cron starts is the one schedule run a caller cannot
// ask for, and says so: ci.Request.TrustedSchedule is what lets a job in
// it hold a capability that opted in to trusted schedules (repo-write).
func TestAScheduledWorkflowRunIsATrustedSchedule(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	workflowSchedule(t, store, baseTime.Add(-time.Minute))
	runner := newFakeCI()

	if err := orchestrator.RunCycle(ctx, workflowDeps(store, runner), baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	if len(runner.requests) != 1 {
		t.Fatalf("started %d runs, want 1", len(runner.requests))
	}
	if !runner.requests[0].TrustedSchedule || runner.requests[0].Event != ci.EventSchedule {
		t.Errorf("request = event %q, trusted %v; want a trusted %s run",
			runner.requests[0].Event, runner.requests[0].TrustedSchedule, ci.EventSchedule)
	}
}

func TestWorkflowReviewAutoApprovesProposedTasks(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := workflowSchedule(t, store, baseTime.Add(-time.Minute))
	runner := newFakeCI()
	deps := workflowDeps(store, runner)

	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	runner.finish("run1", ci.ConclusionFailure,
		ci.JobRun{JobID: "go-long", Conclusion: ci.ConclusionFailure})
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(2*time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	filed := scheduledTasksTagged(t, store, "schedule:"+sched.ID)
	if len(filed) != 1 {
		t.Fatalf("filed %d tasks, want 1", len(filed))
	}
	reviewTask := filed[0]
	if !reviewTask.AutoApproveProposals {
		t.Fatalf("review task AutoApproveProposals = %v, want true", reviewTask.AutoApproveProposals)
	}

	_, client := newSim(t, "acme", "widgets", "main")
	result := toolResult(agent.ToolCall{
		Name: "propose_task",
		Arguments: map[string]any{
			"title": "fix go-long flake",
			"body":  "diagnosed failure in go-long step",
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, reviewTask, result, reviewTask.ID+"-1", baseTime.Add(3*time.Hour)); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposals := proposalsByTitle(t, ctx, store, reviewTask.ID)
	proposal, ok := proposals["fix go-long flake"]
	if !ok {
		t.Fatal("the proposed task was never filed")
	}
	state, err := store.State(ctx, proposal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateQueued {
		t.Fatalf("state = %q, want queued -- workflow review task has auto-approve-proposals", state)
	}
	if proposal.Approval == nil || proposal.Approval.Actor != model.AutoSubmitPrincipal {
		t.Errorf("approval = %+v, want AutoSubmitPrincipal", proposal.Approval)
	}
}

// When a workflow fails due to capability unavailability (e.g. missing github-sandbox
// or missing CLAUDE_CODE_OAUTH_TOKEN), the review leaves a comment explaining the
// missing operator configuration and files no code modification task.
func TestWorkflowReviewCapabilityFailureProducesOperatorCommentNotCodeTask(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := workflowSchedule(t, store, baseTime.Add(-time.Minute))
	runner := newFakeCI()
	deps := workflowDeps(store, runner)

	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	at, _ := spendInfraRetries(t, ctx, deps, runner, baseTime, func(id string) {
		runner.finish(id, ci.ConclusionErrored,
			ci.JobRun{
				JobID:      "github-smoke",
				Conclusion: ci.ConclusionErrored,
			})
	})
	if err := orchestrator.RunCycle(ctx, deps, at.Add(time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	filed := scheduledTasksTagged(t, store, "schedule:"+sched.ID)
	if len(filed) != 1 {
		t.Fatalf("filed %d tasks, want 1", len(filed))
	}
	reviewTask := filed[0]

	_, client := newSim(t, "acme", "widgets", "main")
	commentText := "Job github-smoke failed before running steps: missing deployment capability " +
		"\"github-sandbox\" and missing secret CLAUDE_CODE_OAUTH_TOKEN. This is an operator configuration " +
		"issue, not a code defect. No code task proposed."
	result := toolResult(agent.ToolCall{
		Name: "comment_on_issue",
		Arguments: map[string]any{
			"comment": commentText,
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, reviewTask, result, reviewTask.ID+"-1", at.Add(2*time.Hour)); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	// 1. Verify no code modification task was proposed.
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		for _, l := range task.Links {
			if l.Kind == model.LinkProposedBy && l.Target == reviewTask.ID {
				t.Fatalf("found task %s (%q) proposed by review task, want none for capability/infra error", task.ID, task.Title)
			}
		}
	}

	// 2. Verify comment was recorded on the review task.
	bodies := commentBodies(t, ctx, store, reviewTask.ID)
	found := false
	for _, b := range bodies {
		if strings.Contains(b, "missing deployment capability") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("comments = %q, want one explaining missing operator capability/secrets", bodies)
	}

	// 3. Verify task is in StateAwaitingAck (finished run with closing note for operator, no PR).
	state, err := store.State(ctx, reviewTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateAwaitingReply {
		t.Fatalf("state = %q, want %q", state, model.StateAwaitingReply)
	}
}

// Every other schedule is untouched by any of this.
func TestAnOrdinaryScheduleStartsNoCIRun(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	filedSchedule(t, store, "sched-1", baseTime.Add(-time.Minute))
	runner := newFakeCI()
	deps := orchestrator.Deps{Store: store, CI: runner}

	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if len(runner.requests) != 0 {
		t.Fatalf("an ordinary schedule started %d CI runs", len(runner.requests))
	}
}

// A `.grain/schedules` file naming a workflow (grain/task-3159), from the
// tree to the review: it runs the very workflow the cron runs, as the
// same trusted schedule run, on the branch the file names rather than the
// default one -- and its review is filed on that branch and says which
// branch it is about. The provenance of this schedule is the schedule file
// rather than the workflow, so this is also what pins the run being
// narrowed to the workflow and not to the file that scheduled it.
func TestAScheduleFileRunsItsWorkflowOnItsOwnBase(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	declarationsOn(t, store, ctx, true)
	source := nightlyWorkflowTree()
	source.files[model.ScheduleDeclarationDir+"/nightly-candidate.yml"] = `
workflow: nightly
base: candidate
daily:
  at: "14:11"
`
	runDeclarations(t, store, source, baseTime)

	id := model.RepoDeclaredID(model.DeclaredSchedule, declaringRepo, "nightly-candidate")
	sched, err := store.GetSchedule(ctx, id)
	if err != nil || sched == nil {
		t.Fatalf("GetSchedule(%s) = %v, %v", id, sched, err)
	}
	runner := newFakeCI()
	deps := workflowDeps(store, runner)
	due := sched.NextRunAt
	if err := orchestrator.RunCycle(ctx, deps, due); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("started %d runs, want the candidate's one", len(runner.requests))
	}
	req := runner.requests[0]
	if req.Ref != "candidate" || req.Repo != declaringRepo.String() {
		t.Errorf("run is against %s %s, want %s candidate", req.Repo, req.Ref, declaringRepo)
	}
	if req.Workflow != ci.WorkflowDir+"/nightly.yml" {
		t.Errorf("workflow = %q, want the workflow the file names", req.Workflow)
	}
	if req.Event != ci.EventSchedule || !req.TrustedSchedule || !req.Recheck {
		t.Errorf("request = event %q, trusted %v, recheck %v; want what a cron's run is",
			req.Event, req.TrustedSchedule, req.Recheck)
	}

	runner.finish("run1", ci.ConclusionFailure,
		ci.JobRun{JobID: "go-long", Conclusion: ci.ConclusionFailure})
	if err := orchestrator.RunCycle(ctx, deps, due.Add(2*time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	filed := scheduledTasksTagged(t, store, "schedule:"+id)
	if len(filed) != 1 {
		t.Fatalf("filed %d reviews, want 1", len(filed))
	}
	if want := `Review last night's "nightly" workflow run on candidate`; filed[0].Title != want {
		t.Errorf("review title %q, want %q", filed[0].Title, want)
	}
	if filed[0].Base != "candidate" {
		t.Errorf("review base %q, want candidate", filed[0].Base)
	}
	bodies := commentBodies(t, ctx, store, filed[0].ID)
	if len(bodies) != 1 || !strings.Contains(bodies[0], "`candidate`") || !strings.Contains(bodies[0], id) {
		t.Errorf("comments = %q, want one naming the branch and the schedule", bodies)
	}
}

// greenNight finishes a firing's run the way a night that passed ends:
// success, with the job in it passing.
func greenNight(runner *fakeCI, id string) {
	runner.finish(id, ci.ConclusionSuccess, ci.JobRun{JobID: "go-long", Conclusion: ci.ConclusionSuccess})
}

// The run is the check (grain/task-3160): a night whose run passed files
// no task, is settled so the overlap rule does not wait on a review that
// will never exist, and lets the next night fire on its own cadence.
func TestAGreenWorkflowRunFilesNoReviewAndDoesNotHoldTheCadence(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := workflowSchedule(t, store, baseTime.Add(-time.Minute))
	runner := newFakeCI()
	deps := workflowDeps(store, runner)

	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	greenNight(runner, "run1")
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	if filed := scheduledTasksTagged(t, store, "schedule:"+sched.ID); len(filed) != 0 {
		t.Fatalf("filed %d tasks for a night that passed, want none", len(filed))
	}
	row, err := store.GetScheduledWorkflowRun(ctx, sched.ID)
	if err != nil || row == nil {
		t.Fatalf("GetScheduledWorkflowRun = %v, %v", row, err)
	}
	if !row.Settled() || !row.ReviewNotNeeded || row.ReviewTaskID != "" ||
		row.Conclusion != string(ci.ConclusionSuccess) || row.RunID != "run1" {
		t.Fatalf("row = %+v, want it settled as green with no review", row)
	}

	// A restarted daemon reading the settled row files nothing either.
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(2*time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if filed := scheduledTasksTagged(t, store, "schedule:"+sched.ID); len(filed) != 0 {
		t.Fatalf("filed %d tasks on a later cycle, want none", len(filed))
	}

	// And the next night runs.
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(25*time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if runner.started() != 2 {
		t.Fatalf("started %d runs by the next night, want 2 -- a green night holds nothing", runner.started())
	}
}

// A daemon that stopped between recording a green verdict and settling
// the firing comes back and settles it, rather than filing a review the
// policy says nobody needs.
func TestAGreenWorkflowRunRecordedButNotSettledIsSettledWithoutAReview(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := workflowSchedule(t, store, baseTime.Add(-time.Minute))
	runner := newFakeCI()
	deps := workflowDeps(store, runner)

	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	greenNight(runner, "run1")
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	row, err := store.GetScheduledWorkflowRun(ctx, sched.ID)
	if err != nil || row == nil {
		t.Fatalf("GetScheduledWorkflowRun = %v, %v", row, err)
	}
	row.ReviewNotNeeded = false // the write that never landed
	if err := store.PutScheduledWorkflowRun(ctx, *row); err != nil {
		t.Fatalf("rewinding the row: %v", err)
	}

	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(2*time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if filed := scheduledTasksTagged(t, store, "schedule:"+sched.ID); len(filed) != 0 {
		t.Fatalf("filed %d tasks after the restart, want none", len(filed))
	}
	if again, err := store.GetScheduledWorkflowRun(ctx, sched.ID); err != nil || again == nil || !again.ReviewNotNeeded {
		t.Fatalf("row = %+v, %v, want it settled again", again, err)
	}
}

// Every ending that is not clearly green is reviewed as it always was,
// and survives a restart between filing the review and recording it with
// exactly one task: a failure, which is a finding; an errored or
// cancelled run, in which nothing was tested; and a "success" with no
// job in it, which tested nothing either.
func TestAWorkflowRunThatDidNotPassIsReviewedOnce(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		conclusion ci.Conclusion
		jobs       []ci.JobRun
		mention    string
		// infra is a verdict about nothing, which a nightly on trunk
		// re-runs before it is reviewed (spendInfraRetries).
		infra bool
	}{
		{"failure", ci.ConclusionFailure,
			[]ci.JobRun{{JobID: "go-long", Conclusion: ci.ConclusionFailure}}, "go-long", false},
		{"errored", ci.ConclusionErrored,
			[]ci.JobRun{{JobID: "go-long", Conclusion: ci.ConclusionErrored, Error: "no sandbox"}}, "errored", true},
		{"cancelled", ci.ConclusionCancelled,
			[]ci.JobRun{{JobID: "go-long", Conclusion: ci.ConclusionCancelled}}, "cancelled", true},
		{"nothing ran", ci.ConclusionSuccess, nil, "success", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, ctx := openStore(t)
			sched := workflowSchedule(t, store, baseTime.Add(-time.Minute))
			runner := newFakeCI()
			deps := workflowDeps(store, runner)

			if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
				t.Fatalf("RunCycle: %v", err)
			}
			end := func(id string) { runner.finish(id, tc.conclusion, tc.jobs...) }
			at, last := baseTime, "run1"
			if tc.infra {
				at, last = spendInfraRetries(t, ctx, deps, runner, at, end)
			} else {
				end(last)
			}
			if err := orchestrator.RunCycle(ctx, deps, at.Add(time.Hour)); err != nil {
				t.Fatalf("RunCycle: %v", err)
			}
			filed := scheduledTasksTagged(t, store, "schedule:"+sched.ID)
			if len(filed) != 1 {
				t.Fatalf("filed %d tasks, want 1", len(filed))
			}
			bodies := commentBodies(t, ctx, store, filed[0].ID)
			if len(bodies) != 1 || !strings.Contains(bodies[0], tc.mention) || !strings.Contains(bodies[0], last) {
				t.Fatalf("comments = %q, want one naming %s and %q", bodies, last, tc.mention)
			}

			// Restarted between filing and recording.
			row, err := store.GetScheduledWorkflowRun(ctx, sched.ID)
			if err != nil || row == nil || row.ReviewTaskID != filed[0].ID || row.ReviewNotNeeded {
				t.Fatalf("row = %+v, %v, want it to name the review", row, err)
			}
			row.ReviewTaskID = ""
			if err := store.PutScheduledWorkflowRun(ctx, *row); err != nil {
				t.Fatalf("rewinding the row: %v", err)
			}
			if err := orchestrator.RunCycle(ctx, deps, at.Add(2*time.Hour)); err != nil {
				t.Fatalf("RunCycle: %v", err)
			}
			if again := scheduledTasksTagged(t, store, "schedule:"+sched.ID); len(again) != 1 {
				t.Fatalf("filed %d tasks after the restart, want the one", len(again))
			}
			if recovered, err := store.GetScheduledWorkflowRun(ctx, sched.ID); err != nil || recovered == nil ||
				recovered.ReviewTaskID != filed[0].ID {
				t.Fatalf("row = %+v, %v, want it to name the review already filed", recovered, err)
			}
		})
	}
}

// `review: always` puts back what every workflow schedule did before:
// a green night is reviewed too.
func TestAWorkflowScheduleThatAsksForEveryNightReviewsAGreenOne(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := workflowSchedule(t, store, baseTime.Add(-time.Minute))
	sched.WorkflowReview = model.WorkflowReviewAlways
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatalf("PutSchedule: %v", err)
	}
	runner := newFakeCI()
	deps := workflowDeps(store, runner)

	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	greenNight(runner, "run1")
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	filed := scheduledTasksTagged(t, store, "schedule:"+sched.ID)
	if len(filed) != 1 {
		t.Fatalf("filed %d tasks, want the review `review: always` asks for", len(filed))
	}
	if bodies := commentBodies(t, ctx, store, filed[0].ID); len(bodies) != 1 ||
		!strings.Contains(bodies[0], "success") {
		t.Fatalf("comments = %q, want one saying the run passed", bodies)
	}

	row, err := store.GetScheduledWorkflowRun(ctx, sched.ID)
	if err != nil || row == nil || row.ReviewTaskID != filed[0].ID || row.ReviewNotNeeded {
		t.Fatalf("row = %+v, %v, want it to name the review", row, err)
	}
	row.ReviewTaskID = ""
	if err := store.PutScheduledWorkflowRun(ctx, *row); err != nil {
		t.Fatalf("rewinding the row: %v", err)
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(2*time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if again := scheduledTasksTagged(t, store, "schedule:"+sched.ID); len(again) != 1 {
		t.Fatalf("filed %d tasks after the restart, want the one", len(again))
	}
}

// The setting is read off the declarations that write it: a workflow's own
// cron entry, and a `.grain/schedules` file that runs a workflow on
// another branch (grain/task-3159).
func TestTheReviewPolicyIsReadFromBothKindsOfWorkflowSchedule(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	declarationsOn(t, store, ctx, true)
	source := nightlyWorkflowTree()
	source.files[ci.WorkflowDir+"/nightly.yml"] = strings.Replace(nightlyWorkflowFile,
		"- cron: \"11 2 * * *\"", "- cron: \"11 2 * * *\"\n      review: always", 1)
	source.files[model.ScheduleDeclarationDir+"/nightly-candidate.yml"] = `
workflow: nightly
base: candidate
review: on-failure
daily:
  at: "14:11"
`
	runDeclarations(t, store, source, baseTime)

	cron, err := store.GetSchedule(ctx, workflowScheduleID())
	if err != nil || cron == nil || cron.WorkflowReview != model.WorkflowReviewAlways {
		t.Fatalf("the cron's schedule = %+v, %v, want review: always", cron, err)
	}
	id := model.RepoDeclaredID(model.DeclaredSchedule, declaringRepo, "nightly-candidate")
	file, err := store.GetSchedule(ctx, id)
	if err != nil || file == nil || file.WorkflowReview != model.WorkflowReviewOnFailure {
		t.Fatalf("the declared schedule = %+v, %v, want review: on-failure", file, err)
	}
}

func TestScheduledWorkflowRunsAreSkippedAndCancelledWhenRepoUsesGitHubCIMode(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := workflowSchedule(t, store, baseTime.Add(-time.Minute))
	runner := newFakeCI()
	deps := workflowDeps(store, runner)

	// Start a scheduled workflow run while the repo uses Grain CI.
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("started %d runs, want 1", len(runner.requests))
	}

	// Switch the repo to CIModeGitHub while run1 is in flight.
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:   declaringRepo,
		CIMode: model.CIModeGitHub,
	}); err != nil {
		t.Fatal(err)
	}

	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("RunCycle after switching to GitHub CI: %v", err)
	}
	if got := runner.reasons["run1"]; got != "repository switched to GitHub CI" {
		t.Fatalf("run1 cancel reason = %q, want %q", got, "repository switched to GitHub CI")
	}
	if filed := scheduledTasksTagged(t, store, "schedule:"+sched.ID); len(filed) != 0 {
		t.Fatalf("filed %d review tasks for a cancelled scheduled workflow run on CIModeGitHub, want 0", len(filed))
	}

	// Advance to the next day: the workflow schedule must not start a new Grain CI run while CIMode is GitHub.
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(25*time.Hour)); err != nil {
		t.Fatalf("RunCycle next day: %v", err)
	}
	if got := runner.started(); got != 1 {
		t.Fatalf("started %d runs after switching to CIModeGitHub, want 1 (no new runs)", got)
	}
}
