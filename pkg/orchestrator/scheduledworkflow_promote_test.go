package orchestrator_test

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// promote.yml is what publishes the release the git `latest` tag names
// (moved by a person's approval, never by release-train.yml itself), and it holds package-publish and
// image-signing only as a trusted release
// (docs/design/release-from-trunk.md §6, RFT-3).

// releaseTrainSchedule files the schedule .grain/workflows/release-train.yml's
// cron declares, due at nextRunAt.
func releaseTrainSchedule(t *testing.T, store *model.Store, nextRunAt time.Time) model.Schedule {
	t.Helper()
	templateID := model.WorkflowReviewTemplateID("release-train")
	sched := model.Schedule{
		ID:                   model.RepoDeclaredID(model.DeclaredSchedule, declaringRepo, "workflow.release-train"),
		Title:                "Review the \"release-train\" workflow run",
		Target:               declaringRepo,
		TemplateID:           &templateID,
		Recurrence:           model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 4*60 + 15},
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
			Path: ci.WorkflowDir + "/release-train.yml", Schedule: sched,
		}}}, baseTime, time.UTC); err != nil {
		t.Fatalf("recording provenance: %v", err)
	}
	return sched
}

func TestAFinishedReleaseTrainOnTrunkStartsPromoteAsATrustedRelease(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := releaseTrainSchedule(t, store, baseTime.Add(-time.Minute))
	runner := newFakeCI()
	deps := workflowDeps(store, runner)
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	train := runner.lastRequest(t)
	if train.Event != ci.EventSchedule || !train.TrustedSchedule || train.Priority != ci.PriorityReleaseGate {
		t.Fatalf("release-train asked for %+v, want a trusted schedule run at release-gate priority", train)
	}

	runner.mu.Lock()
	run := runner.runs["run1"]
	run.Commit = "abc123"
	runner.runs["run1"] = run
	runner.mu.Unlock()
	runner.finish("run1", ci.ConclusionSuccess, ci.JobRun{JobID: "cut-or-promote", Conclusion: ci.ConclusionSuccess})
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if n := runner.started(); n != 2 {
		t.Fatalf("%d runs started, want release-train and then promote", n)
	}
	req := runner.lastRequest(t)
	if req.Workflow != ci.WorkflowDir+"/promote.yml" || req.Event != ci.EventRelease || !req.TrustedRelease ||
		req.Ref != "main" || req.Commit != "abc123" || req.Repo != declaringRepo.String() {
		t.Fatalf("promotion asked for %+v, want promote.yml as a trusted release of main at abc123", req)
	}
	if req.TrustedSchedule {
		t.Errorf("promotion is a release, not a schedule: %+v", req)
	}
	row, err := store.GetScheduledWorkflowRun(ctx, sched.ID)
	if err != nil || row == nil || !row.ReviewNotNeeded {
		t.Fatalf("row = %+v, %v, want the green night settled with no review", row, err)
	}

	// Settled: later cycles start nothing more.
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(2*time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if n := runner.started(); n != 2 {
		t.Fatalf("%d runs started after the night settled, want promote started once", n)
	}
}

// A run can push its tags and fail a later job; promote.yml publishes
// whatever `latest` names, so it is started for a red night too.
func TestAFailedReleaseTrainStillStartsPromote(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	releaseTrainSchedule(t, store, baseTime.Add(-time.Minute))
	runner := newFakeCI()
	deps := workflowDeps(store, runner)
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	runner.finish("run1", ci.ConclusionFailure, ci.JobRun{JobID: "promote-images", Conclusion: ci.ConclusionFailure})
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if req := runner.lastRequest(t); req.Workflow != ci.WorkflowDir+"/promote.yml" || req.Event != ci.EventRelease {
		t.Fatalf("last request %+v, want promote.yml started after the red night", req)
	}
}

// Only release-train promotes: a nightly that passes starts nothing.
func TestAGreenNightlyStartsNoPromotion(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	workflowSchedule(t, store, baseTime.Add(-time.Minute))
	runner := newFakeCI()
	deps := workflowDeps(store, runner)
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	runner.finish("run1", ci.ConclusionSuccess, ci.JobRun{JobID: "go-long", Conclusion: ci.ConclusionSuccess})
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if n := runner.started(); n != 1 {
		t.Fatalf("%d runs started, want only the nightly", n)
	}
}

// Off trunk a release-train run promotes nothing: `latest` is trunk's.
func TestAReleaseTrainOffTrunkStartsNoPromotion(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := releaseTrainSchedule(t, store, baseTime.Add(-time.Minute))
	sched.Base = "candidate"
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatal(err)
	}
	runner := newFakeCI()
	deps := workflowDeps(store, runner)
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	runner.finish("run1", ci.ConclusionSuccess, ci.JobRun{JobID: "cut-or-promote", Conclusion: ci.ConclusionSuccess})
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if n := runner.started(); n != 1 {
		t.Fatalf("%d runs started, want no promotion off trunk", n)
	}
}
