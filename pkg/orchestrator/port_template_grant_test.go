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

package orchestrator_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A port of a task filed from a template records that template for
// reporting (SourceTemplateID) and carries no TemplateID, so a grant
// settings/capabilities.yml restricts to the template is not the port's.
// It used to be copied across anyway, and the catalogue then held the
// port at every dispatch ("not granted to an ad-hoc task") so it never
// ran (docs/test-runs/2026-11-13-task-source-template.md, F1). The port
// is now filed without it, saying why, and keeps every other grant.
func TestAPortDropsAGrantRestrictedToItsSourcesTemplate(t *testing.T) {
	store, ctx := openStore(t)
	sim, client, repo := pairedSim(t, ctx, store, "main", "candidate")
	const issues = "https://grain.dev/capabilities/github#read-issues={}"
	if err := store.UpdateConfig(ctx, func(cfg *model.Config) error {
		cfg.CapabilityPolicy.Catalogue = &model.CapabilityCatalogue{InProcess: map[string]model.CatalogueInProcess{
			"read-issues": {Restrictions: &model.CapabilityRestrictions{Templates: model.Patterns{"release-note"}}},
		}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTemplate(ctx, model.Template{ID: "release-note", Name: "release-note", Title: "Release note", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task.Grants = []model.Grant{
		{Capability: issues, Via: ".grain/templates/release-note.yml"},
		{Capability: "gcp-key", Via: model.GrantByLabel},
	}
	task.TemplateID = "release-note"
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	mergeQueued(t, ctx, store, sim, client, task, branch)

	ports := portsOf(t, ctx, store, task.ID)
	if len(ports) != 1 {
		t.Fatalf("filed %d ports, want 1", len(ports))
	}
	port := ports[0]
	if port.TemplateID != "" || port.SourceTemplateID != "release-note" {
		t.Errorf("port template %q, source template %q; want none and release-note", port.TemplateID, port.SourceTemplateID)
	}
	if slices.ContainsFunc(port.Grants, func(g model.Grant) bool { return strings.Contains(g.Capability, "read-issues") }) {
		t.Errorf("port grants = %+v, want the template-restricted read-issues dropped", port.Grants)
	}
	if !slices.ContainsFunc(port.Grants, func(g model.Grant) bool { return g.Capability == "gcp-key" }) {
		t.Errorf("port grants = %+v, want the unrestricted gcp-key kept", port.Grants)
	}
	if !strings.Contains(port.Body, "read-issues") || !strings.Contains(port.Body, "settings/capabilities.yml") {
		t.Errorf("port body does not say which grant it was filed without:\n%s", port.Body)
	}
	if _, err := (*model.CapabilityCatalogue)(nil).ValidateGrants(model.TaskGrantContext(port)); err != nil {
		t.Errorf("nil catalogue refused the port: %v", err)
	}
	cfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.CapabilityPolicy.Catalogue.ValidateGrants(model.TaskGrantContext(port)); err != nil {
		t.Errorf("the catalogue still refuses the port, which would hold it at dispatch: %v", err)
	}
}

// Only a grant the catalogue admits because of the source's template is
// dropped. One it refuses for another reason -- here a repository
// restriction the source's template does not lift -- stays on the port, so
// dispatch holds the port on it exactly as it would hold the source, rather
// than the port quietly running without something a person scoped elsewhere.
func TestAPortKeepsAGrantTheCatalogueRefusesForAnotherReason(t *testing.T) {
	store, ctx := openStore(t)
	sim, client, repo := pairedSim(t, ctx, store, "main", "candidate")
	const issues = "https://grain.dev/capabilities/github#read-issues={}"
	if err := store.UpdateConfig(ctx, func(cfg *model.Config) error {
		cfg.CapabilityPolicy.Catalogue = &model.CapabilityCatalogue{InProcess: map[string]model.CatalogueInProcess{
			"read-issues": {Restrictions: &model.CapabilityRestrictions{Repos: model.Patterns{"acme/elsewhere"}}},
		}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTemplate(ctx, model.Template{ID: "release-note", Name: "release-note", Title: "Release note", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task.Grants = []model.Grant{{Capability: issues, Via: model.GrantByLabel}}
	task.TemplateID = "release-note"
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	mergeQueued(t, ctx, store, sim, client, task, branch)

	ports := portsOf(t, ctx, store, task.ID)
	if len(ports) != 1 {
		t.Fatalf("filed %d ports, want 1", len(ports))
	}
	if !slices.ContainsFunc(ports[0].Grants, func(g model.Grant) bool { return g.Capability == issues }) {
		t.Errorf("port grants = %+v, want read-issues kept: its refusal is not the template's", ports[0].Grants)
	}
	if strings.Contains(ports[0].Body, "filed this port without") {
		t.Errorf("port body says a grant was dropped:\n%s", ports[0].Body)
	}
}
