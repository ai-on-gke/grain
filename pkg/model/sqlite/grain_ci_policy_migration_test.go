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
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// grainCIPolicy is a schema-93 capability policy whose catalogue names the
// retired grain-ci in its task, CI and per-repository defaults -- what the
// schema-93 API saved (docs/test-runs/2026-11-04-capabilities.md, F1).
func grainCIPolicy() model.CapabilityPolicy {
	return model.CapabilityPolicy{
		Catalogue: &model.CapabilityCatalogue{
			Defaults: model.CatalogueDefaults{
				Task: []string{"read-issues", "grain-ci"},
				CI:   []string{"package-publish", "grain-ci"},
				Repos: map[string]model.CatalogueRepoDefaults{
					"acme/widgets": {CI: []string{"grain-ci", model.GrainCIGrant}},
					"acme/tools":   {Task: []string{"read-issues"}},
				},
			},
		},
	}
}

func checkGrainCIPolicyRewritten(t *testing.T, pol model.CapabilityPolicy) {
	t.Helper()
	cat := pol.Catalogue
	if cat == nil {
		t.Fatal("catalogue lost")
	}
	// read-issues rides along to show the rewrite leaves other entries in
	// place; migration 121 retired it onto the github server on the way.
	if want := []string{model.GitHubReadIssuesGrant, model.GrainCIGrant}; !slices.Equal(cat.Defaults.Task, want) {
		t.Errorf("defaults.task = %v, want %v", cat.Defaults.Task, want)
	}
	if want := []string{model.PackagePublishRef + "={}", model.GrainCIGrant}; !slices.Equal(cat.Defaults.CI, want) {
		t.Errorf("defaults.ci = %v, want %v", cat.Defaults.CI, want)
	}
	if want := []string{model.GrainCIGrant}; !slices.Equal(cat.Defaults.Repos["acme/widgets"].CI, want) {
		t.Errorf("acme/widgets CI defaults = %v, want %v once", cat.Defaults.Repos["acme/widgets"].CI, want)
	}
	if want := []string{model.GitHubReadIssuesGrant}; !slices.Equal(cat.Defaults.Repos["acme/tools"].Task, want) {
		t.Errorf("acme/tools task defaults = %v, want %v", cat.Defaults.Repos["acme/tools"].Task, want)
	}
}

// Migration 96 rewrote stored grain-ci grants everywhere but the
// capability policy, so a catalogue naming grain-ci kept the bare alias
// through every upgrade. Migration 125 finishes the rewrite there, and a
// second run changes nothing.
func TestMigration125RewritesGrainCIInThePolicy(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	cfg := testConfig()
	cfg.CapabilityPolicy = grainCIPolicy()
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 93 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating a version-93 store: %v", err)
	}
	got, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	checkGrainCIPolicyRewritten(t, got.CapabilityPolicy)

	var before, after string
	if err := db.QueryRowContext(ctx, "SELECT `capability_policy` FROM `grain_config` WHERE `id` = 1").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 124 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating again: %v", err)
	}
	if err := db.QueryRowContext(ctx, "SELECT `capability_policy` FROM `grain_config` WHERE `id` = 1").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("a second run changed the policy:\n%s\n->\n%s", before, after)
	}
}

// A state-repo export written at schema 93 gets the same rewrite on
// import (staterepo replays the data migrations past the dump's schema).
func TestStateRepoImportRewritesGrainCIInThePolicy(t *testing.T) {
	t.Parallel()
	src, srcDB, ctx := openStore(t)
	cfg := testConfig()
	cfg.CapabilityPolicy = grainCIPolicy()
	if err := src.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := staterepo.Export(ctx, srcDB, dir); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.WriteSchemaVersion(dir, 93); err != nil {
		t.Fatal(err)
	}
	dst, dstDB, _ := openStore(t)
	if err := staterepo.Import(ctx, dstDB, dir); err != nil {
		t.Fatalf("importing a schema-93 export: %v", err)
	}
	got, err := dst.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cat := got.CapabilityPolicy.Catalogue
	if cat == nil {
		t.Fatal("catalogue lost")
	}
	for _, list := range [][]string{cat.Defaults.Task, cat.Defaults.CI, cat.Defaults.Repos["acme/widgets"].CI} {
		if slices.Contains(list, model.GrainCICapability) {
			t.Errorf("catalogue defaults still name the bare %s after import: %+v", model.GrainCICapability, cat.Defaults)
		}
	}
}
