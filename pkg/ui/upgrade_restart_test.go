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

package ui_test

// What the upgrade pane says about the runs an upgrade's restart carries
// across (task 1389): before one, which of the runs in flight it would
// lose, by framework; after one, what the last restart did to them -- the
// same report the post-upgrade check is told.

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type upgradeRestartResponse struct {
	InFlightRuns int `json:"inFlightRuns"`
	AtRisk       *struct {
		Lost            int                 `json:"lost"`
		LostByFramework []ui.FrameworkCount `json:"lostByFramework"`
		Survive         int                 `json:"survive"`
	} `json:"atRisk"`
	LastRestart       *ui.RestartAdoption `json:"lastRestart"`
	UpgradeInterrupts []ui.CLIUpgradeNote `json:"upgradeInterrupts"`
}

func TestUpgradeStatusSaysWhichRunsARestartWouldLoseAndWhatTheLastOneLost(t *testing.T) {
	t.Parallel()
	_, store, ctx := testClient(t)
	_ = store.PutConfig(ctx, model.DefaultConfig())
	at := time.Date(2026, 10, 27, 6, 0, 0, 0, time.UTC)
	alice := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	for i, r := range []struct{ id, framework string }{
		{"claude-a", "claude"},
		{"claude-b", "claude"},
		{"agy", "antigravity"},
		{"setup", ""},
	} {
		if err := store.PutTask(ctx, model.Task{
			ID: r.id, Intent: model.IntentImplement, Title: r.id,
			Origin:   model.Origin{Attribution: model.Attribution{Actor: alice}, Reason: model.ReasonDirect},
			Binding:  model.BindingDirective,
			Approval: &model.Attribution{Actor: alice},
		}); err != nil {
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
	// The agy run's CLI started detached in grain hostd.
	if err := store.SetHostdCursor(ctx, "agy-1", 0, at); err != nil {
		t.Fatal(err)
	}
	report := &ui.RestartAdoption{
		At:      at,
		Adopted: []ui.RestartRun{{Run: "r-1", Task: "t-1", Framework: "antigravity", Reason: "its spool went 12s unread"}},
		Lost: []ui.RestartRun{
			{Run: "r-2", Task: "t-2", Framework: "claude", Reason: "stopped"},
			{Run: "r-3", Task: "t-3", Framework: "claude", Reason: "stopped"},
			{Run: "r-4", Task: "t-4", Setup: true, Reason: "setting up"},
		},
	}
	srv := ui.NewServer(ui.Config{
		Actor: ui.DefaultActor("alice"), Capabilities: ui.OfferedCapabilities(), Upgrader: &fakeUpgrader{},
		LastRestart: func(context.Context) *ui.RestartAdoption { return report },
	}, store)

	rec := do(t, srv, http.MethodGet, "/api/upgrade", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rec.Code, rec.Body)
	}
	got := decode[upgradeRestartResponse](t, rec)
	if got.InFlightRuns != 4 {
		t.Errorf("inFlightRuns = %d, want 4", got.InFlightRuns)
	}
	if got.AtRisk == nil {
		t.Fatal("no atRisk with four runs in flight")
	}
	want := []ui.FrameworkCount{{Framework: "claude", Count: 2}, {Framework: "setup", Count: 1}}
	if got.AtRisk.Lost != 3 || got.AtRisk.Survive != 1 || !reflect.DeepEqual(got.AtRisk.LostByFramework, want) {
		t.Errorf("atRisk = %+v, want 3 lost (%v) and 1 surviving", *got.AtRisk, want)
	}
	if got.LastRestart == nil {
		t.Fatal("no lastRestart")
	}
	if len(got.LastRestart.Adopted) != 1 || len(got.LastRestart.Lost) != 3 {
		t.Errorf("lastRestart = %+v", *got.LastRestart)
	}
	if !reflect.DeepEqual(got.LastRestart.LostByFramework, want) {
		t.Errorf("lastRestart lost by framework = %v, want %v", got.LastRestart.LostByFramework, want)
	}
}

// With nothing running and no report there is nothing to say, and the
// response says nothing rather than an empty section.
func TestUpgradeStatusOmitsAnIdleRestart(t *testing.T) {
	t.Parallel()
	srv, _ := serverAndStoreWithUpgrader(t, &fakeUpgrader{})
	rec := do(t, srv, http.MethodGet, "/api/upgrade", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rec.Code, rec.Body)
	}
	got := decode[upgradeRestartResponse](t, rec)
	if got.AtRisk != nil || got.LastRestart != nil {
		t.Errorf("idle deployment reported atRisk %+v, lastRestart %+v", got.AtRisk, got.LastRestart)
	}
}

// The pane says which CLIs an upgrade interrupts wherever they are set to
// run now, from the daemon's own resolution (Config.CLIRuns), whether or
// not a run is in flight: codex here, and not the claude hostd carries.
func TestUpgradeStatusNamesTheCLIsAnUpgradeInterrupts(t *testing.T) {
	t.Parallel()
	_, store, ctx := testClient(t)
	_ = store.PutConfig(ctx, model.DefaultConfig())
	srv := ui.NewServer(ui.Config{
		Actor: ui.DefaultActor("alice"), Capabilities: ui.OfferedCapabilities(), Upgrader: &fakeUpgrader{},
		CLIRuns: func(context.Context, map[string]model.CLIRunSetting) any {
			return []map[string]string{
				{"framework": "claude", "location": "detached"},
				{"framework": "codex", "location": "container", "upgradeInterrupts": "A daemon upgrade interrupts codex runs."},
			}
		},
	}, store)
	rec := do(t, srv, http.MethodGet, "/api/upgrade", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rec.Code, rec.Body)
	}
	got := decode[upgradeRestartResponse](t, rec)
	want := []ui.CLIUpgradeNote{{Framework: "codex", Note: "A daemon upgrade interrupts codex runs."}}
	if !reflect.DeepEqual(got.UpgradeInterrupts, want) {
		t.Errorf("upgradeInterrupts = %+v, want %+v", got.UpgradeInterrupts, want)
	}
}
