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
	"reflect"
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// Migration 100 rewrites a capability policy's rules and catalogue
// defaults naming a retired <vendor>-proxy, leaves everything else in it
// as it was, and changes nothing when it runs again.
func TestMigration100RewritesModelProxyPolicy(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)

	cfg := testConfig()
	cfg.CapabilityPolicy = model.CapabilityPolicy{
		Rules: []model.CapabilityPolicyRule{
			{Capability: "claude-proxy", Repos: []string{"acme/*"}, Deny: true},
			{Capability: "https://github.com/acme/caps#build", Repos: []string{"acme/widgets"}},
		},
		Catalogue: &model.CapabilityCatalogue{
			Defaults: model.CatalogueDefaults{
				Task: []string{"release-gate-read", "openai-proxy"},
				CI:   []string{"gemini-proxy"},
				Repos: map[string]model.CatalogueRepoDefaults{
					"acme/widgets": {Task: []string{"claude-proxy", model.ModelProxyGrant("claude")}},
					"acme/tools":   {Task: []string{"release-gate-read"}},
				},
			},
		},
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 99 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating a version-99 store: %v", err)
	}
	got, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pol := got.CapabilityPolicy
	if len(pol.Rules) != 2 || pol.Rules[0].Capability != model.ModelProxyRef || !pol.Rules[0].Deny ||
		!slices.Equal(pol.Rules[0].Repos, []string{"acme/*"}) || pol.Rules[1].Capability != "https://github.com/acme/caps#build" {
		t.Errorf("rules = %+v, want claude-proxy's deny on %s and the other rule untouched", pol.Rules, model.ModelProxyRef)
	}
	cat := pol.Catalogue
	if cat == nil {
		t.Fatal("catalogue lost")
	}
	if want := []string{"release-gate-read", model.ModelProxyGrant("openai")}; !slices.Equal(cat.Defaults.Task, want) {
		t.Errorf("defaults.task = %v, want %v", cat.Defaults.Task, want)
	}
	if want := []string{model.ModelProxyGrant("gemini")}; !slices.Equal(cat.Defaults.CI, want) {
		t.Errorf("defaults.ci = %v, want %v", cat.Defaults.CI, want)
	}
	if want := []string{model.ModelProxyGrant("claude")}; !slices.Equal(cat.Defaults.Repos["acme/widgets"].Task, want) {
		t.Errorf("acme/widgets task defaults = %v, want %v once", cat.Defaults.Repos["acme/widgets"].Task, want)
	}
	if want := []string{"release-gate-read"}; !slices.Equal(cat.Defaults.Repos["acme/tools"].Task, want) {
		t.Errorf("acme/tools task defaults = %v, want %v", cat.Defaults.Repos["acme/tools"].Task, want)
	}

	before := storedGrantState(t, ctx, db)
	var policyBefore string
	if err := db.QueryRowContext(ctx, "SELECT `capability_policy` FROM `grain_config` WHERE `id` = 1").Scan(&policyBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 99 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating again: %v", err)
	}
	var policyAfter string
	if err := db.QueryRowContext(ctx, "SELECT `capability_policy` FROM `grain_config` WHERE `id` = 1").Scan(&policyAfter); err != nil {
		t.Fatal(err)
	}
	if policyAfter != policyBefore {
		t.Errorf("a second run changed the policy:\n%s\n->\n%s", policyBefore, policyAfter)
	}
	if after := storedGrantState(t, ctx, db); !reflect.DeepEqual(before, after) {
		t.Errorf("a second run changed stored grants: %v -> %v", before, after)
	}
}

// A state-repo export written before schema 100 is rewritten on import:
// the retired <vendor>-proxy grants it carries arrive as model-proxy
// {vendor}.
func TestStateRepoImportRewritesModelProxyGrants(t *testing.T) {
	t.Parallel()
	src, srcDB, ctx := openStore(t)
	cfg := testConfig()
	cfg.DefaultCapabilities = []string{"claude-proxy", "release-gate-read"}
	if err := src.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := src.PutRepoConfig(ctx, model.RepoConfig{Repo: widgets,
		DefaultCapabilities: []string{"openai-proxy"}, CICapabilities: []string{"gemini-proxy"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := srcDB.ExecContext(ctx, "INSERT INTO `template_grant` (`template_id`, `capability`, `via`) VALUES ('t', 'claude-proxy', 'label')"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := staterepo.Export(ctx, srcDB, dir); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.WriteSchemaVersion(dir, 99); err != nil {
		t.Fatal(err)
	}

	dst, dstDB, _ := openStore(t)
	if err := staterepo.Import(ctx, dstDB, dir); err != nil {
		t.Fatalf("importing a schema-99 export: %v", err)
	}
	got, err := dst.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{model.ModelProxyGrant("claude"), "release-gate-read"}; !slices.Equal(got.DefaultCapabilities, want) {
		t.Errorf("default capabilities = %v, want %v", got.DefaultCapabilities, want)
	}
	repo, err := dst.GetRepoConfig(ctx, widgets)
	if err != nil || repo == nil {
		t.Fatalf("GetRepoConfig: %v, %+v", err, repo)
	}
	if want := []string{model.ModelProxyGrant("openai")}; !slices.Equal(repo.DefaultCapabilities, want) {
		t.Errorf("repo default capabilities = %v, want %v", repo.DefaultCapabilities, want)
	}
	if want := []string{model.ModelProxyGrant("gemini")}; !slices.Equal(repo.CICapabilities, want) {
		t.Errorf("repo CI capabilities = %v, want %v", repo.CICapabilities, want)
	}
	if want := []string{model.ModelProxyGrant("claude")}; !slices.Equal(grantRows(t, ctx, dstDB, "template_grant", "template_id")["t"], want) {
		t.Errorf("template grants = %v, want %v", grantRows(t, ctx, dstDB, "template_grant", "template_id")["t"], want)
	}
}
