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
// finished run is neither. A run in setup has its framework already --
// RunDispatch records it before building the sandbox -- so Started, not
// Framework, is what says it is in setup (task 5665).
func TestLiveRunSubstratesSaysWhichRunsARestartLoses(t *testing.T) {
	store, ctx := open(t)
	at := time.Date(2026, 10, 27, 6, 0, 0, 0, time.UTC)
	for i, r := range []struct {
		id, framework string
		started       bool
	}{
		{"in-run-host", "claude", true},
		{"in-hostd", "antigravity", true},
		{"local", "codex", true},
		{"setup", "antigravity", false},
		{"no-framework", "", false},
		{"finished", "claude", true},
	} {
		task := model.Task{
			ID: r.id, Intent: model.IntentImplement, Title: r.id,
			Filing:   model.Filing{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
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
		if r.started {
			if err := store.SetRunAgentStarted(ctx, run.ID, run.StartedAt.Add(time.Minute)); err != nil {
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
		{Run: "in-run-host-1", Task: "in-run-host", Framework: "claude", Started: true, Detached: true},
		{Run: "in-hostd-1", Task: "in-hostd", Framework: "antigravity", Started: true, Detached: true},
		{Run: "local-1", Task: "local", Framework: "codex", Started: true},
		{Run: "setup-1", Task: "setup", Framework: "antigravity"},
		{Run: "no-framework-1", Task: "no-framework"},
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
