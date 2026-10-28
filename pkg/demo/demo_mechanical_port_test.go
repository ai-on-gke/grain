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
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// -mechanical-port seeds what the orchestrator leaves after a clean
// mechanical port: a merged change ported by a completed task that owns
// its pull request, is tagged port-mechanical, and has never had a run.
func TestMechanicalPortDemoSeedsAPortWithNoRun(t *testing.T) {
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
	cfg := ui.Config{Actor: ui.DefaultActor("demo-operator"), DefaultTarget: &repo}
	if err := seedMechanicalPortDemo(ctx, store, cfg, repo); err != nil {
		t.Fatalf("seedMechanicalPortDemo: %v", err)
	}

	all, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var source, port *model.Task
	for i := range all {
		switch {
		case all[i].Title == demoMechanicalPortSourceTitle:
			source = &all[i]
		case slices.Contains(all[i].Tags, model.TagMechanicalPort):
			port = &all[i]
		}
	}
	if source == nil || port == nil {
		t.Fatalf("seeded tasks %+v lack the merged change or its mechanical port", all)
	}
	if !slices.Contains(port.Links, model.Link{Kind: model.LinkPortOf, Target: source.ID}) ||
		!slices.Contains(source.Links, model.Link{Kind: model.LinkPortedBy, Target: port.ID}) {
		t.Errorf("port and source are not linked: %+v / %+v", port.Links, source.Links)
	}
	if pr := model.TaskPullRequest(*port); pr == nil || port.Base != "candidate" {
		t.Errorf("port pull request %v, base %q", pr, port.Base)
	}
	if st, _ := store.State(ctx, port.ID); st != model.StateCompleted {
		t.Errorf("port state = %q, want completed", st)
	}
	if st, _ := store.State(ctx, source.ID); st != model.StateClosed {
		t.Errorf("source state = %q, want closed", st)
	}
	if runs, _ := store.Runs(ctx, port.ID); len(runs) != 0 {
		t.Errorf("mechanical port has %d runs, want none", len(runs))
	}

	// The port configuration the repository page shows: the operator's
	// pair, a declared list that widens its filter, and the two declared
	// entries that cannot take effect.
	rc, err := store.GetRepoConfig(ctx, repo)
	if err != nil || rc == nil {
		t.Fatalf("GetRepoConfig = %v, %v", rc, err)
	}
	if got := rc.PortTypeFilter("main", "candidate"); !slices.Equal(got, []string{"fix", "revert"}) {
		t.Errorf("main->candidate filter = %v, want [fix revert]", got)
	}
	warnings := rc.PortTypesWarnings()
	if len(warnings) != 2 {
		t.Fatalf("port-types warnings = %q, want the typo and the list on an operator pair", warnings)
	}
	for i, want := range []string{"candidate->main is ignored", "main->candidtae names candidtae"} {
		if !strings.Contains(warnings[i], want) {
			t.Errorf("warning %d = %q, want it to say %q", i, warnings[i], want)
		}
	}
}
