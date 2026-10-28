package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func declareGoal(t *testing.T, ctx context.Context, store *model.Store, id string, d model.GoalDecl, at time.Time) {
	t.Helper()
	if err := store.SetTaskGoal(ctx, id, d, "human:alice", at); err != nil {
		t.Fatal(err)
	}
}

func finishedRun(t *testing.T, ctx context.Context, store *model.Store, taskID string, n int, at time.Time) string {
	t.Helper()
	id := fmt.Sprintf("%s-run-%d", taskID, n)
	if err := store.StartRun(ctx, model.Run{ID: id, TaskID: taskID, Attempt: n, StartedAt: at}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, id, at.Add(time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	return id
}

// An agent ending a goal task's run does not complete it: the task rests,
// suspended on its goal and a message, and the goal check is what closes
// it -- with outcome goal-met and a note naming the evidence.
func TestAGoalTaskRestsOnItsAgentsWordAndClosesWhenTheGoalIsMet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	fileTaggedTask(t, ctx, store, "watcher", repo)
	fileTaggedTask(t, ctx, store, "dep", repo)
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	declareGoal(t, ctx, store, "watcher", model.GoalDecl{Kind: model.GoalTasksClosed,
		Spec: model.GoalSpec{Tasks: []string{"dep"}}}, now)

	runID := fmt.Sprintf("watcher-run-%d", 1)
	if err := store.StartRun(ctx, model.Run{ID: runID, TaskID: "watcher", Attempt: 1, StartedAt: now}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, runID, now.Add(time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	task, _ := store.GetTask(ctx, "watcher")
	rested, err := restOnGoal(ctx, store, *task, runID, now.Add(time.Minute))
	if err != nil || !rested {
		t.Fatalf("restOnGoal = %v, %v; want a goal task rested", rested, err)
	}
	if st, _ := store.State(ctx, "watcher"); st != model.StateDeferred {
		t.Fatalf("watcher is %s after its agent ended it, want deferred (resting on its goal)", st)
	}
	if ok, err := restingOnGoal(ctx, store, "watcher"); err != nil || !ok {
		t.Fatalf("restingOnGoal = %v, %v; want the requested suspension on the goal", ok, err)
	}

	later := now.Add(time.Hour)
	if err := reconcileTaskGoals(ctx, Deps{Store: store}, later); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.State(ctx, "watcher"); st == model.StateClosed {
		t.Fatal("watcher closed while dep is still open")
	}
	if err := store.ObserveField(ctx, "dep", later, func(o *model.Observation) { o.ClosedAt = &later }); err != nil {
		t.Fatal(err)
	}
	if err := reconcileTaskGoals(ctx, Deps{Store: store}, later.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.State(ctx, "watcher"); st != model.StateClosed {
		t.Fatalf("watcher is %s once dep closed, want closed", st)
	}
	g, _ := store.TaskGoal(ctx, "watcher")
	if g == nil || g.Outcome != model.GoalMetOutcome || g.MetAt == nil {
		t.Fatalf("watcher's goal = %+v, want met with outcome goal-met", g)
	}
	comments, _ := store.Comments(ctx, "watcher")
	if len(comments) == 0 || !strings.Contains(comments[len(comments)-1].Body, model.GoalMetOutcome) ||
		!strings.Contains(comments[len(comments)-1].Body, "dep") {
		t.Fatalf("closing note does not name the outcome and the evidence: %+v", comments)
	}
}

// A goal met while a run is live tells the run first, and closes the task
// only once the run has finished: the grace is the run's to wrap up in.
func TestAGoalMetUnderALiveRunTellsItBeforeClosing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	fileTaggedTask(t, ctx, store, "watcher", repo)
	fileTaggedTask(t, ctx, store, "dep", repo)
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	declareGoal(t, ctx, store, "watcher", model.GoalDecl{Kind: model.GoalTasksClosed,
		Spec: model.GoalSpec{Tasks: []string{"dep"}}}, now)
	if err := store.ObserveField(ctx, "dep", now, func(o *model.Observation) { o.ClosedAt = &now }); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{ID: "live", TaskID: "watcher", Attempt: 1, StartedAt: now}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := reconcileTaskGoals(ctx, Deps{Store: store}, now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if st, _ := store.State(ctx, "watcher"); st != model.StateRunning {
		t.Fatalf("watcher is %s with its run live, want still running", st)
	}
	comments, _ := store.Comments(ctx, "watcher")
	if len(comments) != 1 || !strings.Contains(comments[0].Body, "will be stopped") {
		t.Fatalf("want one addendum telling the live run, got %+v", comments)
	}
	if err := store.FinishRun(ctx, "live", now.Add(2*time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if err := reconcileTaskGoals(ctx, Deps{Store: store}, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.State(ctx, "watcher"); st != model.StateClosed {
		t.Fatalf("watcher is %s once its run finished, want closed", st)
	}
}

// Resumes without progress count up to the goal's limit, which raises
// task.no-progress and holds the task; a person acting on it (here,
// posting to it) is progress and resets the count.
func TestAGoalThatStopsProgressingRaisesNoProgressAndHolds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	fileTaggedTask(t, ctx, store, "watcher", repo)
	fileTaggedTask(t, ctx, store, "dep", repo)
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	declareGoal(t, ctx, store, "watcher", model.GoalDecl{Kind: model.GoalTasksClosed,
		Spec: model.GoalSpec{Tasks: []string{"dep"}}, MaxResumes: 2}, now)
	deps := Deps{Store: store}
	if err := reconcileTaskGoals(ctx, deps, now); err != nil {
		t.Fatal(err)
	}
	subject := model.NotificationSubject(model.FamilyNoProgress, "watcher")
	for i := 1; i <= 2; i++ {
		at := now.Add(time.Duration(i) * time.Hour)
		finishedRun(t, ctx, store, "watcher", i, at)
		if err := reconcileTaskGoals(ctx, deps, at.Add(2*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	g, _ := store.TaskGoal(ctx, "watcher")
	if g == nil || g.ResumesSinceProgress != 2 || g.LimitNotifiedAt == nil {
		t.Fatalf("goal = %+v, want 2 resumes without progress and the limit notified", g)
	}
	n, err := store.OpenNotification(ctx, subject)
	if err != nil || n == nil || n.TaskID != "watcher" || !strings.Contains(n.Body, "watcher-run-2") {
		t.Fatalf("no-progress notification = %+v (%v), want one about watcher naming its last run", n, err)
	}
	task, _ := store.GetTask(ctx, "watcher")
	if task.DeferredAt == nil || task.DeferredUntil != nil {
		t.Fatalf("watcher deferred %v until %v, want held with no wake time", task.DeferredAt, task.DeferredUntil)
	}

	// A person replies: progress.
	reply := now.Add(4 * time.Hour)
	if _, err := store.AddComment(ctx, model.Comment{TaskID: "watcher",
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
		Body:   "try the other flake", CreatedAt: reply}); err != nil {
		t.Fatal(err)
	}
	if err := reconcileTaskGoals(ctx, deps, reply.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	g, _ = store.TaskGoal(ctx, "watcher")
	if g.ResumesSinceProgress != 0 || g.LimitNotifiedAt != nil {
		t.Fatalf("goal = %+v after a person posted, want the count reset", g)
	}
	if n, _ := store.OpenNotification(ctx, subject); n != nil {
		t.Fatal("no-progress still open after a person acted on it")
	}
}

// on_limit notify keeps going past the limit, backing off: the task is
// deferred with a wake time rather than held.
func TestANotifyGoalBacksOffPastItsLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	fileTaggedTask(t, ctx, store, "watcher", repo)
	fileTaggedTask(t, ctx, store, "dep", repo)
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	declareGoal(t, ctx, store, "watcher", model.GoalDecl{Kind: model.GoalTasksClosed,
		Spec: model.GoalSpec{Tasks: []string{"dep"}}, MaxResumes: 1, OnLimit: model.GoalNotifyOnLimit}, now)
	deps := Deps{Store: store}
	if err := reconcileTaskGoals(ctx, deps, now); err != nil {
		t.Fatal(err)
	}
	finishedRun(t, ctx, store, "watcher", 1, now.Add(time.Hour))
	at := now.Add(time.Hour + 2*time.Minute)
	if err := reconcileTaskGoals(ctx, deps, at); err != nil {
		t.Fatal(err)
	}
	task, _ := store.GetTask(ctx, "watcher")
	if task.DeferredUntil == nil || !task.DeferredUntil.After(at) {
		t.Fatalf("watcher deferred until %v, want backed off past %v", task.DeferredUntil, at)
	}
	if n, _ := store.OpenNotification(ctx, model.NotificationSubject(model.FamilyNoProgress, "watcher")); n == nil {
		t.Fatal("no no-progress notification for a notify goal past its limit")
	}
}
