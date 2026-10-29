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

package demo

import (
	"context"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// -stalled-tasks is what puts the Inbox's "Look at a stuck task" group
// in front of a browser: each seeded task reads back a stall note and
// the control its sub-case names, and the ordinary demo, with no stall
// deadlines, reads back none.
func TestStalledTasksDemoSeedsAStuckQueuedTaskAndRun(t *testing.T) {
	t.Parallel()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	plain := ui.Config{
		Actor:         ui.DefaultActor("demo-operator"),
		DefaultTarget: &repo,
		Capabilities:  ui.OfferedCapabilities(),
		Reasons:       daemon.ReasonClassifier{},
	}
	cfg := stalledTasksConfig(plain)
	if err := seedStalledTasksDemo(ctx, store, cfg); err != nil {
		t.Fatalf("seedStalledTasksDemo: %v", err)
	}

	tasks, err := ui.NewClient(cfg, store).ListTasks(ctx)
	if err != nil {
		t.Fatalf("listing tasks: %v", err)
	}
	want := map[string]struct{ state, control string }{
		demoStalledQueuedTitle:  {"queued", "Run now"},
		demoStalledRunningTitle: {"running", "Cancel"},
	}
	for _, task := range tasks {
		w, ok := want[task.Title]
		if !ok {
			continue
		}
		delete(want, task.Title)
		if string(task.State) != w.state {
			t.Errorf("%q is %s, want %s", task.Title, task.State, w.state)
		}
		if task.Stall == "" {
			t.Errorf("%q reads back no stall note", task.Title)
		}
		if task.Reason == nil || task.Reason.Control != w.control {
			t.Errorf("%q offers %+v, want the %q control", task.Title, task.Reason, w.control)
		}
	}
	for title := range want {
		t.Errorf("%q was not seeded", title)
	}

	unstalled, err := ui.NewClient(plain, store).ListTasks(ctx)
	if err != nil {
		t.Fatalf("listing tasks: %v", err)
	}
	for _, task := range unstalled {
		if task.Stall != "" {
			t.Errorf("without -stalled-tasks, %q still reads back a stall note: %q", task.Title, task.Stall)
		}
	}
}
