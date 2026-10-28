package orchestrator_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// A run that parked through wait_for_task leaves its task queued and held
// by the links the park wrote, records itself as parked, and the next
// attempt's prompt -- once the target has closed and the task is ready
// again -- says it resumed and how the target ended.
func TestParkedRunResumesWithItsTargetsOutcome(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	target := filedTask(t, ctx, store, "b1", repo)
	task := filedTask(t, ctx, store, "a1", repo)

	runID := "a1-1"
	if err := store.StartRun(ctx, model.Run{ID: runID, TaskID: task.ID, Sandbox: "s1", Attempt: 1, StartedAt: baseTime}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	// What ui.Client.WaitOn does while the run is still going.
	parked := task
	parked.Links = append(slices.Clone(parked.Links), model.Link{Kind: model.LinkDependsOn, Target: target.ID})
	if err := store.PutTask(ctx, parked); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, runID, baseTime, "succeeded", ""); err != nil {
		t.Fatal(err)
	}

	result := toolResult(agent.ToolCall{Name: "wait_for_task",
		Arguments: map[string]any{"task_id": target.ID, "park": true},
		Text:      "Parked. This task now depends on #" + target.ID})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, runID, baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if st := taskState(t, ctx, store, task.ID); st != model.StateQueued {
		t.Fatalf("state = %q, want queued", st)
	}
	if e := runEnding(t, ctx, store, task.ID); e != model.EndingParked {
		t.Fatalf("ending = %q, want parked", e)
	}
	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(ready, task.ID) {
		t.Fatal("a parked task was ready before its target closed")
	}

	closedAt := baseTime
	if err := store.ObserveField(ctx, target.ID, baseTime, func(o *model.Observation) {
		o.CompletedAt, o.PrMergedAt, o.ClosedAt = &closedAt, &closedAt, &closedAt
	}); err != nil {
		t.Fatal(err)
	}
	if ready, err = store.Ready(ctx); err != nil {
		t.Fatal(err)
	} else if !slices.Contains(ready, task.ID) {
		t.Fatalf("ready = %v, want the parked task once its target closed", ready)
	}

	runs, err := store.Runs(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	history := orchestrator.History{Attempt: 2, Attempts: runs,
		Resumed: orchestrator.ResumedFrom(ctx, store, runs)}
	prompt := orchestrator.BuildPrompt(task, "work", "", true, 0, history, nil)
	for _, want := range []string{"resumes from a park", "Task " + target.ID, "outcome merged"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, prompt)
		}
	}
}

// A wait_for_task call that only looked -- or one whose park wrote no
// link -- is not a park, and the run ends the way it otherwise would.
func TestWaitForTaskThatDidNotParkIsNotAPark(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	target := filedTask(t, ctx, store, "b1", repo)
	task := filedTask(t, ctx, store, "a1", repo)
	runID := "a1-1"
	if err := store.StartRun(ctx, model.Run{ID: runID, TaskID: task.ID, Sandbox: "s1", Attempt: 1, StartedAt: baseTime}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, runID, baseTime, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	result := toolResult(agent.ToolCall{Name: "wait_for_task",
		Arguments: map[string]any{"task_id": target.ID, "park": true}, Text: "Parked."})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, runID, baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if e := runEnding(t, ctx, store, task.ID); e == model.EndingParked {
		t.Fatal("a park with no link behind it was recorded as parked")
	}
}

// A run that parked until a time through wait_for_task's resume_at leaves
// its task deferred with that wake time (what ui.Client.ParkUntil wrote
// while it ran) and records itself as parked, with the time. Nothing
// dispatches the task before then; the cycle at the wake time brings it
// back to the queue and dispatches it, and that attempt's prompt says it
// resumed from a timed park.
func TestTimedParkDefersTheTaskAndWakesItBackIntoTheQueue(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "a1", repo)
	wake := baseTime.Add(time.Hour)

	runID := "a1-1"
	if err := store.StartRun(ctx, model.Run{ID: runID, TaskID: task.ID, Sandbox: "s1", Attempt: 1, StartedAt: baseTime}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	// What ui.Client.ParkUntil does while the run is still going.
	deferUntil(t, store, task.ID, baseTime, wake)
	if err := store.FinishRun(ctx, runID, baseTime, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	result := toolResult(agent.ToolCall{Name: "wait_for_task",
		Arguments: map[string]any{"resume_at": "1h"},
		Text: "Parked. This task is deferred until " + wake.Format(time.RFC3339) +
			" and is queued again once that time has passed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, runID, baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	stateIs(t, store, task.ID, model.StateDeferred)
	parked, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if parked.DeferredUntil == nil || !parked.DeferredUntil.Equal(wake) {
		t.Fatalf("deferred until %v, want %v", parked.DeferredUntil, wake)
	}
	if e := runEnding(t, ctx, store, task.ID); e != model.EndingParked {
		t.Fatalf("ending = %q, want parked", e)
	}

	fw := newBlockingFramework()
	deps, runs := asyncDeps(t, store, fw, 2)
	cycleAt(t, ctx, deps, wake.Add(-time.Minute))
	stateIs(t, store, task.ID, model.StateDeferred)
	if got, err := store.Runs(ctx, task.ID); err != nil {
		t.Fatal(err)
	} else if len(got) != 1 {
		t.Fatalf("runs = %d before the wake time, want only the parked one", len(got))
	}
	cycleAt(t, ctx, deps, wake)
	enteredRun(t, fw, "a1-2")
	fw.releaseAll()
	drainRuns(t, ctx, runs)

	previous, err := store.Runs(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	previous = previous[:1]
	history := orchestrator.History{Attempt: 2, Attempts: previous,
		ResumedUntil: orchestrator.ResumedUntil(previous)}
	prompt := orchestrator.BuildPrompt(task, "work", "", true, 0, history, nil)
	for _, want := range []string{"resumes from a park", "resume_at", wake.Format(time.RFC3339), "retrigger_ci"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, prompt)
		}
	}
}
