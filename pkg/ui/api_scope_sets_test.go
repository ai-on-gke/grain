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

import (
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// grain/task-442: Settings lists every scope set, built-in and declared,
// with what it resolves to; a declared one is offered to the capability
// pickers with its scopes, so the resolved list is in front of whoever
// grants it; and declaring one grants it to nobody.
func TestScopeSetsAreListedAndOfferedButNotGranted(t *testing.T) {
	client, store, ctx := testClient(t)
	t.Cleanup(func() { _ = model.InstallAPIScopeSets(nil) })
	triage := model.APIScopeSet{Name: "ops/triage", Includes: []string{"grain/ci-read", "grain/task-admin"},
		Deny: []string{"tasks:admin"}, Repos: []string{"bwsalmon/grain"}}
	if err := client.PutAPIScopeSet(ctx, triage); err != nil {
		t.Fatal(err)
	}
	if err := client.PutAPIScopeSet(ctx, model.APIScopeSet{Name: "grain/task-admin", Scopes: []string{"ci:read"}}); err == nil {
		t.Error("a built-in was edited through the API")
	}
	if err := client.DeleteAPIScopeSet(ctx, "grain/task-admin"); err == nil {
		t.Error("a built-in was removed through the API")
	}

	list := client.APIScopeSets(ctx)
	var got *ui.APIScopeSetRow
	for i := range list.Sets {
		if list.Sets[i].Name == "ops/triage" {
			got = &list.Sets[i]
		}
	}
	if got == nil || got.Source != "config" || got.Path != "tables/api_scope_set.json" || got.Person {
		t.Fatalf("ops/triage listed as %+v", got)
	}
	if !list.Sets[0].Builtin || list.Sets[0].Source != "builtin" {
		t.Errorf("built-ins are not listed first: %+v", list.Sets[0])
	}
	if !slices.Contains(got.Resolved, "ci:write") || slices.Contains(got.Resolved, "tasks:admin") {
		t.Errorf("resolved = %v", got.Resolved)
	}

	var offered *ui.Capability
	for _, row := range ui.APICapabilities() {
		if row.ID == "api:ops/triage" {
			offered = &row
		}
	}
	if offered == nil || !offered.Preset || !slices.Contains(offered.Scopes, "merge:write") ||
		!strings.Contains(offered.Name, "ops/triage") {
		t.Fatalf("api:ops/triage offered as %+v", offered)
	}

	created, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "Triage the queue"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.GetTask(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, g := range task.Grants {
		names = append(names, g.Capability)
	}
	if s := model.APIGrantsOf(names); s.Has("tasks:write") {
		t.Errorf("a task nobody granted ops/triage to holds its scopes: %v", s.Scopes())
	}

	if err := client.DeleteAPIScopeSet(ctx, "ops/triage"); err != nil {
		t.Fatal(err)
	}
	if _, ok := model.APIScopeSetNamed("ops/triage"); ok {
		t.Error("a removed set is still in effect")
	}
}

// Settings warns before removing a scope set by naming what holds it,
// so GET /api/api-scope-sets/usage lists every grant of a set, @repo or
// not (docs/test-runs/2026-10-28-unified-grants-scope-sets.md, O3). And
// an @repo outside a repo-limited set's own repositories grants nothing,
// so it is refused when granted rather than recorded silently (O1).
func TestScopeSetUsageAndGrantsOutsideItsRepos(t *testing.T) {
	client, _, ctx := testClient(t)
	t.Cleanup(func() { _ = model.InstallAPIScopeSets(nil) })
	if err := client.PutAPIScopeSet(ctx, model.APIScopeSet{Name: "walk-readers",
		Scopes: []string{"tasks:read", "ci:read"}, Repos: []string{"acme/widgets"}}); err != nil {
		t.Fatal(err)
	}
	created, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "Read the widgets"})
	if err != nil {
		t.Fatal(err)
	}

	err = client.SetCapability(ctx, created.ID, "api:walk-readers@acme/gadgets", true)
	if err == nil || !strings.Contains(err.Error(), "grants nothing") || !strings.Contains(err.Error(), "acme/widgets") {
		t.Fatalf("granting walk-readers outside its repositories = %v, want it refused naming them", err)
	}
	for _, g := range []string{"api:walk-readers", "api:walk-readers@acme/widgets"} {
		if err := client.SetCapability(ctx, created.ID, g, true); err != nil {
			t.Fatalf("granting %s: %v", g, err)
		}
	}

	usage, err := client.APIScopeSetUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var grants []string
	for _, u := range usage {
		switch u.Name {
		case "walk-readers":
			for _, h := range u.Holders {
				if h.Kind != "task" || h.ID != created.ID {
					t.Errorf("walk-readers held by %+v", h)
				}
				grants = append(grants, h.Grant)
			}
		case "grain/ci-read":
			for _, h := range u.Holders {
				if h.ID == created.ID {
					t.Errorf("grain/ci-read counted the task's walk-readers grant %s", h.Grant)
				}
			}
		}
	}
	slices.Sort(grants)
	if want := []string{"api:walk-readers", "api:walk-readers@acme/widgets"}; !slices.Equal(grants, want) {
		t.Errorf("walk-readers holders' grants = %v, want %v", grants, want)
	}
}

func TestAPIScopeSetsIncludeCataloguePresetAndRetiredInProcessEntries(t *testing.T) {
	client, store, ctx := testClient(t)
	off := false
	setCatalogue(t, ctx, store, &model.CapabilityCatalogue{
		InProcess: map[string]model.CatalogueInProcess{
			"memory": {Enabled: &off},
		},
		APIScopes: model.CatalogueAPIScopes{
			Presets: map[string]model.CataloguePreset{
				"release-notes": {
					Restrictions: &model.CapabilityRestrictions{
						Repos:          model.Patterns{"acme/*"},
						RequiresPerson: true,
					},
				},
			},
		},
	})
	resp := client.APIScopeSets(ctx)
	byName := map[string]ui.APIScopeSetRow{}
	for _, row := range resp.Sets {
		byName[row.Name] = row
	}
	mem := byName["grain/memory"]
	if mem.Catalogue == nil || !mem.Catalogue.Disabled || mem.Catalogue.Preset != "memory" ||
		!slices.Equal(mem.Catalogue.LegacyInProcess, []string{"memory"}) {
		t.Fatalf("grain/memory = %+v (catalogue %+v), want disabled from legacy in_process.memory", mem, mem.Catalogue)
	}
	rn := byName["grain/release-notes"]
	if rn.Catalogue == nil || rn.Catalogue.Disabled || !rn.Person || rn.Catalogue.Restrictions == nil ||
		!slices.Equal([]string(rn.Catalogue.Restrictions.Repos), []string{"acme/*"}) {
		t.Fatalf("grain/release-notes = %+v (catalogue %+v), want repo restriction and person", rn, rn.Catalogue)
	}
	if ciRead := byName["grain/ci-read"]; ciRead.Catalogue != nil {
		t.Errorf("unrestricted grain/ci-read has Catalogue = %+v, want nil", ciRead.Catalogue)
	}
}
