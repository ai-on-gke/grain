package model_test

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// What the upgrade pane says an upgrade would cost (task 1389): a live
// run whose CLI started in a run host or detached in grain hostd -- each
// marked by its spool cursor -- survives the restart, and every other
// live run, a child of the daemon or one still in setup, is lost. A
// finished run is neither.
func TestLiveRunSubstratesSaysWhichRunsARestartLoses(t *testing.T) {
	store, ctx := open(t)
	at := time.Date(2026, 10, 27, 6, 0, 0, 0, time.UTC)
	for i, r := range []struct{ id, framework string }{
		{"in-run-host", "claude"},
		{"in-hostd", "antigravity"},
		{"local", "codex"},
		{"setup", ""},
		{"finished", "claude"},
	} {
		task := model.Task{
			ID: r.id, Intent: model.IntentImplement, Title: r.id,
			Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
			Binding:  model.BindingDirective,
			Approval: &model.Attribution{Actor: human},
		}
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		run := model.Run{ID: r.id + "-1", TaskID: r.id, Sandbox: r.id + "-1", Attempt: 1,
			StartedAt: at.Add(time.Duration(i) * time.Minute)}
		if err := store.StartRun(ctx, run, model.Limits{Workers: 10}); err != nil {
			t.Fatal(err)
		}
		if r.framework != "" {
			if err := store.SetRunModel(ctx, run.ID, "m", r.framework); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := store.SetSpoolCursor(ctx, "in-run-host-1", 0, at); err != nil {
		t.Fatal(err)
	}
	if err := store.SetHostdCursor(ctx, "in-hostd-1", 0, at); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "finished-1", at.Add(time.Hour), model.InterruptedOutcome, "stopped"); err != nil {
		t.Fatal(err)
	}

	got, err := store.LiveRunSubstrates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []model.LiveRunSubstrate{
		{Run: "in-run-host-1", Task: "in-run-host", Framework: "claude", Detached: true},
		{Run: "in-hostd-1", Task: "in-hostd", Framework: "antigravity", Detached: true},
		{Run: "local-1", Task: "local", Framework: "codex"},
		{Run: "setup-1", Task: "setup"},
	}
	if len(got) != len(want) {
		t.Fatalf("live runs = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("live run %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
