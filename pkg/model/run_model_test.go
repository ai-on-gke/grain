package model_test

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The model and framework a run ran on are recorded on its own row, so
// Runs reads back which of a task's rotation each attempt was given, and
// a run nobody recorded one for reads back empty rather than failing.
func TestRunsReadBackTheModelEachRunRanOn(t *testing.T) {
	store, ctx := open(t)
	at := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	task := model.Task{
		ID: "t", Intent: model.IntentImplement, Title: "t",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Binding:  model.BindingDirective,
		Approval: &model.Attribution{Actor: human},
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"t-1", "t-2"} {
		run := model.Run{ID: id, TaskID: "t", Sandbox: id, Attempt: i + 1, StartedAt: at.Add(time.Duration(i) * time.Hour)}
		if err := store.StartRun(ctx, run, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if id == "t-1" {
			if err := store.SetRunModel(ctx, id, "gemini-3.1-flash", "antigravity"); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.FinishRun(ctx, id, at.Add(time.Duration(i)*time.Hour+time.Minute), "failed", "x"); err != nil {
			t.Fatal(err)
		}
	}

	runs, err := store.Runs(ctx, "t")
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %+v (%v), want two", runs, err)
	}
	if runs[0].Model != "gemini-3.1-flash" || runs[0].AgentFramework != "antigravity" {
		t.Errorf("runs[0] ran on %q/%q, want gemini-3.1-flash/antigravity", runs[0].Model, runs[0].AgentFramework)
	}
	if runs[1].Model != "" || runs[1].AgentFramework != "" {
		t.Errorf("runs[1] ran on %q/%q, want nothing recorded", runs[1].Model, runs[1].AgentFramework)
	}
}
