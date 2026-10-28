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

// What the stored CI history occupies, on the pane that draws the disk
// bars it is filling (grain/task-290).

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// storeCIRun writes one finished run holding bytes of step output.
func storeCIRun(t *testing.T, store *model.Store, id string, started time.Time, bytes int) {
	t.Helper()
	run := model.CIRun{
		ID: id, Key: "key-" + id, Repo: "acme/widgets", Ref: "grain/task-1",
		Commit: "9f8e7d6c", Event: "push", StartedAt: started,
		FinishedAt: started.Add(time.Minute), Conclusion: "failure",
		Jobs: []model.CIJob{{
			Workflow: "tests", WorkflowPath: ".grain/workflows/tests.yml", JobID: "go",
			Name: "go", Key: "job-" + id, StartedAt: started, Conclusion: "failure",
			Steps: []model.CIStep{{Name: "go test", Conclusion: "failure", ExitCode: 1,
				Output: strings.Repeat("x", bytes), StartedAt: started}},
		}},
	}
	if err := store.PutCIRun(context.Background(), run); err != nil {
		t.Fatalf("putting CI run %s: %v", id, err)
	}
}

// The reading an operator needs before touching either bound: how much
// output is stored, across how many runs, how far back, and the ceiling
// it is all held against.
func TestSandboxHealthReportsWhatTheCIHistoryOccupies(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	client.Config.Sandboxes = fakeSandboxHealth{}
	oldest := time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC)
	storeCIRun(t, client.Store, "older", oldest, 2048)
	storeCIRun(t, client.Store, "newer", oldest.Add(time.Hour), 1024)

	rec := do(t, srv, http.MethodGet, "/api/sandboxes", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[ui.SandboxHealthResponse](t, rec)
	if got.CIHistory == nil {
		t.Fatal("no CI history figures at all")
	}
	if got.CIHistory.Bytes != 3072 || got.CIHistory.Runs != 2 || got.CIHistory.Steps != 2 {
		t.Errorf("CI history = %+v, want both runs' output", got.CIHistory)
	}
	// A deployment that has never saved its settings is bounded by
	// grain's own defaults, and those are the numbers in force -- so they
	// are the ones shown, rather than a blank where the ceiling goes.
	if got.CIHistory.Ceiling <= 0 || got.CIHistory.AgeDays <= 0 || got.CIHistory.RunsKept <= 0 {
		t.Errorf("bounds = %+v, want grain's own defaults resolved", got.CIHistory)
	}
	if got.CIHistory.OldestAt == nil || !got.CIHistory.OldestAt.Equal(oldest) {
		t.Errorf("oldest run with output = %v, want %v", got.CIHistory.OldestAt, oldest)
	}
}

// The ceiling shown is the ceiling the sweep applies, units and all: a
// pane reporting MiB against bytes is worse than one reporting nothing.
func TestCIHistoryCeilingFollowsTheSetting(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	client.Config.Sandboxes = fakeSandboxHealth{}
	ctx := context.Background()
	cfg, err := client.Store.GetConfig(ctx)
	if err != nil {
		t.Fatalf("reading settings: %v", err)
	}
	if cfg == nil {
		cfg = &model.Config{}
	}
	// 8 MiB of output, kept for a week, and no bound at all on how many
	// run records stand behind it.
	cfg.CIStepOutputMB, cfg.CIStepOutputDays, cfg.CIRunHistory = 8, 7, -1
	if err := client.Store.PutConfig(ctx, *cfg); err != nil {
		t.Fatalf("saving settings: %v", err)
	}

	got := decode[ui.SandboxHealthResponse](t, do(t, srv, http.MethodGet, "/api/sandboxes", ""))
	if got.CIHistory == nil {
		t.Fatal("no CI history figures at all")
	}
	if got.CIHistory.Ceiling != 8<<20 || got.CIHistory.AgeDays != 7 {
		t.Errorf("bounds = %+v, want 8 MiB and 7 days", got.CIHistory)
	}
	// A bound switched off reads back as nothing rather than as a very
	// large number: "-1 runs kept" is not a sentence a pane can say.
	if got.CIHistory.RunsKept != 0 {
		t.Errorf("runs kept = %d, want none where an operator has turned the bound off",
			got.CIHistory.RunsKept)
	}
	// Nothing has run, so there is nothing stored -- which is a reading
	// of zero, not a missing section.
	if got.CIHistory.Bytes != 0 || got.CIHistory.OldestAt != nil {
		t.Errorf("an empty history holds nothing: %+v", got.CIHistory)
	}
}
