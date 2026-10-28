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
