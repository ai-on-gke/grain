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

package sqlite_test

import (
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Migration 115 rewrites the capability policy's catalogue defaults too,
// and leaves a catalogue's in_process.self-debug entry where it is: it
// keeps binding both grants self-debug became (judgeRetiredInProcess).
func TestMigration115RewritesSelfDebugInTheCatalogue(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	cfg := testConfig()
	cfg.CapabilityPolicy = model.CapabilityPolicy{
		Catalogue: &model.CapabilityCatalogue{
			Defaults: model.CatalogueDefaults{
				Task: []string{"release-gate-read", "self-debug"},
				CI:   []string{"self-debug"},
				Repos: map[string]model.CatalogueRepoDefaults{
					"acme/tools": {Task: []string{"self-debug", "api:self-debug"}},
				},
			},
			InProcess: map[string]model.CatalogueInProcess{
				"self-debug": {Restrictions: &model.CapabilityRestrictions{Repos: model.Patterns{"acme/*"}}},
			},
		},
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 114 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating a version-114 store: %v", err)
	}
	got, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cat := got.CapabilityPolicy.Catalogue
	if cat == nil {
		t.Fatal("the catalogue was dropped")
	}
	became := model.SelfDebugGrants()
	if want := append([]string{"release-gate-read"}, became...); !slices.Equal(cat.Defaults.Task, want) {
		t.Errorf("defaults.task = %v, want %v", cat.Defaults.Task, want)
	}
	if !slices.Equal(cat.Defaults.CI, became) {
		t.Errorf("defaults.ci = %v, want %v", cat.Defaults.CI, became)
	}
	if got := cat.Defaults.Repos["acme/tools"].Task; !slices.Equal(got, became) {
		t.Errorf("acme/tools task defaults = %v, want %v once each", got, became)
	}
	if _, kept := cat.InProcess["self-debug"]; !kept {
		t.Fatal("in_process.self-debug was dropped")
	}
	otherRepo := &model.RepoRef{Owner: "other", Name: "repo"}
	for _, g := range became {
		if _, err := cat.ValidateGrants(model.GrantContext{Kind: "task", Repo: otherRepo, Grants: []string{g}}); err == nil {
			t.Errorf("in_process.self-debug restriction did not bind %s", g)
		}
	}
}

// Migration 118 rewrites capability_policy deny rules and catalogue defaults
// for bootstrap-playbooks, while never rewriting a non-deny rule (such as
// AllowUntrusted) onto the shared config-map#config-map method where it would
// admit secret maps on untrusted runs.
func TestMigration118RewritesBootstrapPlaybooksDenyWithoutWideningAllowUntrusted(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	cfg := testConfig()
	cfg.CapabilityPolicy = model.CapabilityPolicy{
		Rules: []model.CapabilityPolicyRule{
			{Capability: "bootstrap-playbooks", Repos: []string{"acme/*"}, Deny: true},
			{Capability: "bootstrap-playbooks", AllowUntrusted: true},
		},
		Catalogue: &model.CapabilityCatalogue{
			Defaults: model.CatalogueDefaults{
				Task: []string{"release-gate-read", "bootstrap-playbooks"},
				CI:   []string{"bootstrap-playbooks"},
			},
			InProcess: map[string]model.CatalogueInProcess{
				"bootstrap-playbooks": {Restrictions: &model.CapabilityRestrictions{Repos: model.Patterns{"acme/*"}}},
			},
		},
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 117 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating a version-117 store: %v", err)
	}
	got, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pol := got.CapabilityPolicy
	if len(pol.Rules) != 2 || pol.Rules[0].Capability != model.ConfigMapRef || !pol.Rules[0].Deny {
		t.Fatalf("rules[0] = %+v, want deny rewritten to %s", pol.Rules, model.ConfigMapRef)
	}
	if pol.Rules[1].Capability == model.ConfigMapRef {
		t.Fatalf("non-deny AllowUntrusted rule was rewritten onto shared %s: %+v", model.ConfigMapRef, pol.Rules[1])
	}
	untrusted := model.CapabilitySubject{
		Repo:      &model.RepoRef{Owner: "other", Name: "repo"},
		Kind:      model.CapabilityRunCI,
		Event:     "pull_request",
		Untrusted: true,
	}
	if err := pol.Permits(model.ConfigMapRef+`={"secrets":["prod"]}`, untrusted, nil); err == nil {
		t.Fatal("Permits admitted config-map secrets on an untrusted run after migration 118")
	}
	cat := pol.Catalogue
	if cat == nil || !slices.Equal(cat.Defaults.Task, []string{"release-gate-read", model.BootstrapPlaybooksGrant}) ||
		!slices.Equal(cat.Defaults.CI, []string{model.BootstrapPlaybooksGrant}) {
		t.Errorf("catalogue defaults after migration 118 = %+v", cat)
	}
}
