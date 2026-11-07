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

package postgres_test

import (
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Migration 125 on PostgreSQL: a capability policy whose rules and
// catalogue defaults name the retired grain-ci -- which migration 96 left
// alone -- names grain#ci afterwards, once.
func TestMigration125RewritesGrainCIInThePolicy(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	cfg := model.DefaultConfig()
	cfg.CapabilityPolicy = model.CapabilityPolicy{
		Rules: []model.CapabilityPolicyRule{{Capability: "grain-ci", Repos: []string{"acme/*"}, Deny: true}},
		Catalogue: &model.CapabilityCatalogue{Defaults: model.CatalogueDefaults{
			Task: []string{"read-issues", "grain-ci"},
			CI:   []string{"grain-ci"},
			Repos: map[string]model.CatalogueRepoDefaults{
				"acme/widgets": {CI: []string{"grain-ci", model.GrainCIGrant}},
			},
		}},
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	rewindSchemaTo(t, ctx, db, 124)
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating a version-124 store: %v", err)
	}
	got, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pol := got.CapabilityPolicy
	if r := pol.Rules; len(r) != 1 || r[0].Capability != model.GrainCIRef || !r[0].Deny {
		t.Errorf("rules = %+v, want grain-ci's deny on %s", r, model.GrainCIRef)
	}
	if want := []string{"read-issues", model.GrainCIGrant}; !slices.Equal(pol.Catalogue.Defaults.Task, want) {
		t.Errorf("catalogue task defaults = %v, want %v", pol.Catalogue.Defaults.Task, want)
	}
	if want := []string{model.GrainCIGrant}; !slices.Equal(pol.Catalogue.Defaults.CI, want) {
		t.Errorf("catalogue CI defaults = %v, want %v", pol.Catalogue.Defaults.CI, want)
	}
	if want := []string{model.GrainCIGrant}; !slices.Equal(pol.Catalogue.Defaults.Repos["acme/widgets"].CI, want) {
		t.Errorf("acme/widgets CI defaults = %v, want %v once", pol.Catalogue.Defaults.Repos["acme/widgets"].CI, want)
	}
}
