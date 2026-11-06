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
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/gcpsets"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// grantRows is every (owner, capability) of a grant table, sorted.
func grantRows(t *testing.T, ctx context.Context, db *sql.DB, table, owner string) map[string][]string {
	t.Helper()
	rows, err := db.QueryContext(ctx, "SELECT `"+owner+"`, `capability` FROM `"+table+"`")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var id, capability string
		if err := rows.Scan(&id, &capability); err != nil {
			t.Fatal(err)
		}
		out[id] = append(out[id], capability)
	}
	for _, names := range out {
		sort.Strings(names)
	}
	return out
}

// sameGrantMeaning fails t unless before and after grant the same api
// scopes, on the model's and the sandbox's channel alike, are equally
// dangerous, and hold the same names other than the renamed ones -- the
// fidelity design/design/unified-grants.design.md §3 asks of every migrated row.
func sameGrantMeaning(t *testing.T, what string, before, after []string) {
	t.Helper()
	for _, s := range model.APIScopes() {
		if b, a := model.APIGrantsOf(before).Allows(s.Scope, ""), model.APIGrantsOf(after).Allows(s.Scope, ""); b != a {
			t.Errorf("%s: %s granted %v before the migration and %v after (%v -> %v)", what, s.Scope, b, a, before, after)
		}
		if b, a := model.SandboxAPIGrantsOf(before).Allows(s.Scope, ""), model.SandboxAPIGrantsOf(after).Allows(s.Scope, ""); b != a {
			t.Errorf("%s: %s reached the sandbox %v before the migration and %v after", what, s.Scope, b, a)
		}
	}
	if b, a := slices.ContainsFunc(before, model.IsDangerousGrant), slices.ContainsFunc(after, model.IsDangerousGrant); b != a {
		t.Errorf("%s: dangerous %v before the migration and %v after", what, b, a)
	}
	for _, name := range before {
		to := retiredAs(name)
		retired := len(to) != 1 || to[0] != name
		if !retired && !slices.Contains(after, name) {
			t.Errorf("%s: %q, which is not retired, was lost (%v -> %v)", what, name, before, after)
		}
		if retired && slices.Contains(after, name) {
			t.Errorf("%s: retired %q survived the migration as itself rather than as %v", what, name, to)
		}
	}
}

// retiredGrants are the in-process capabilities a store migration
// rewrote, each with the schema version the migration produces and the
// canonical grant(s) it wrote.
var retiredGrants = []struct {
	version int
	legacy  string
	to      []string
}{
	{67, "team-broadcast", []string{"api:team-broadcast"}},
	{68, "dispatch-control", []string{"api:dispatch-control"}},
	{69, "pipeline-control", []string{"api:pipeline-control"}},
	{71, "memory", []string{"api:memory"}},
	{72, "release-notes", []string{"api:release-notes"}},
	{75, "self-upgrade", []string{"api:self-upgrade"}},
	{76, "test-quarantine", []string{"api:test-quarantine"}},
	{80, "grain-log", []string{model.GrainJournalGrant}},
	{81, "deployment-health", []string{"api:deployment:read", model.GrainHealthGrant}},
	{82, "db-read", []string{model.GrainSQLReadGrant}},
	{82, "db-write", []string{model.GrainSQLWriteGrant}},
	{84, "self-repair", []string{model.GrainHostExecGrant}},
	{85, "git-push-any-ref", []string{model.GitPushAnyRefGrant}},
	// corp-research is the Google build's: core has no alias for it, which
	// that build registers with capability.Register (retiredAs).
	{86, "corp-research", []string{model.CorpResearchGrant}},
	{88, "repo-read:acme/tools", []string{model.RepoReadGrant(model.RepoRef{Owner: "acme", Name: "tools"})}},
	{88, "repo-write:acme/tools", []string{model.RepoWriteGrant(model.RepoRef{Owner: "acme", Name: "tools"})}},
	{96, "grain-ci", []string{model.GrainCIGrant}},
	{97, "gcp-key", []string{model.GCPKeySandboxAgentGrant}},
	{100, "claude-proxy", []string{model.ModelProxyGrant("claude")}},
	{100, "openai-proxy", []string{model.ModelProxyGrant("openai")}},
	{100, "gemini-proxy", []string{model.ModelProxyGrant("gemini")}},
	{101, "image-signing", []string{model.ImageSigningRef + "={}"}},
	{101, "registry-push", []string{model.RegistryPushRef + "={}"}},
	{101, "package-publish", []string{model.PackagePublishRef + "={}"}},
	{103, "gemini-key", []string{model.GCPGeminiKeyGrant}},
	{115, "self-debug", []string{"api:self-debug", model.GrainSourceGrant}},
}

// retiredAs is what a store migration rewrote name to: retiredGrants'
// entry for it, which covers the names only a build outside core makes an
// alias, or else model.CanonicalLegacyGrants.
func retiredAs(name string) []string {
	for _, r := range retiredGrants {
		if r.legacy == name {
			return r.to
		}
	}
	return model.CanonicalLegacyGrants(name)
}

// Each retiring migration rewrites every stored grant of its capability
// -- the three grant tables, the deployment's and a repository's default
// capabilities, and the standing sessions' -- to the grant(s) it
// became, and each row grants exactly what it did before.
func TestMigrationRewritesRetiredGrants(t *testing.T) {
	t.Parallel()
	for _, r := range retiredGrants {
		t.Run(r.legacy, func(t *testing.T) {
			t.Parallel()
			testMigrationRewrites(t, r.version, r.legacy, r.to)
		})
	}
}

func testMigrationRewrites(t *testing.T, version int, legacy string, replacements []string) {
	store, db, ctx := openStore(t)

	cfg := testConfig()
	cfg.DefaultCapabilities = []string{legacy, "read-issues"}
	cfg.ConsoleSessions = []model.ConsoleSession{
		{ID: "director", Title: "Director", Capabilities: []string{"bootstrap-playbooks", legacy}},
		{ID: "steward", Title: "Steward", Capabilities: append(slices.Clone(replacements), legacy)},
		{ID: "quiet", Title: "Quiet"},
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: widgets, DefaultCapabilities: []string{"bootstrap-playbooks", legacy},
		CICapabilities: []string{"github-sandbox", legacy}}); err != nil {
		t.Fatal(err)
	}
	fixture := map[string]map[string][]string{
		"task_grant": {
			"1": {legacy, "bootstrap-playbooks"},
			"2": append([]string{legacy}, replacements...),
			"3": {"api:tasks:write"},
		},
		"template_grant": {"t": {legacy}},
		"schedule_grant": {"s": {legacy, "read-issues"}},
	}
	owners := map[string]string{"task_grant": "task_id", "template_grant": "template_id", "schedule_grant": "schedule_id"}
	for table, byOwner := range fixture {
		for id, names := range byOwner {
			for _, name := range names {
				if _, err := db.ExecContext(ctx, "INSERT INTO `"+table+"` (`"+owners[table]+"`, `capability`, `via`) VALUES (?, ?, 'label')", id, name); err != nil {
					t.Fatal(err)
				}
			}
		}
	}

	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = ? WHERE `id` = 1", version-1); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating a version-%d store: %v", version-1, err)
	}

	for table, byOwner := range fixture {
		got := grantRows(t, ctx, db, table, owners[table])
		for id, before := range byOwner {
			sameGrantMeaning(t, table+" "+id, before, got[id])
		}
	}
	wantSorted := slices.Clone(replacements)
	sort.Strings(wantSorted)
	if got := grantRows(t, ctx, db, "task_grant", "task_id")["2"]; !slices.Equal(got, wantSorted) {
		t.Errorf("task 2, holding both spellings, holds %v after the migration, want %v once", got, wantSorted)
	}

	after, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := append(slices.Clone(replacements), "read-issues"); !slices.Equal(after.DefaultCapabilities, want) {
		t.Errorf("default capabilities = %v, want %v", after.DefaultCapabilities, want)
	}
	if len(after.ConsoleSessions) != len(cfg.ConsoleSessions) {
		t.Fatalf("console sessions = %+v, want the three stored", after.ConsoleSessions)
	}
	for i, s := range after.ConsoleSessions {
		was := cfg.ConsoleSessions[i]
		if s.ID != was.ID || s.Title != was.Title {
			t.Errorf("console session %d = %+v, want only its capabilities changed from %+v", i, s, was)
		}
		sameGrantMeaning(t, "console session "+s.ID, was.Capabilities, s.Capabilities)
	}
	if got, want := after.ConsoleSessions[1].Capabilities, replacements; !slices.Equal(got, want) {
		t.Errorf("steward capabilities = %v, want %v", got, want)
	}

	repo, err := store.GetRepoConfig(ctx, widgets)
	if err != nil {
		t.Fatal(err)
	}
	if want := append([]string{"bootstrap-playbooks"}, replacements...); !slices.Equal(repo.DefaultCapabilities, want) {
		t.Errorf("repo default capabilities = %v, want %v", repo.DefaultCapabilities, want)
	}
	// Migrations 83 to 86 rewrite retired names in repo_config.ci_capabilities as well.
	if want := append([]string{"github-sandbox"}, replacements...); !slices.Equal(repo.CICapabilities, want) {
		t.Errorf("repo CI capabilities = %v, want %v", repo.CICapabilities, want)
	}
	sameGrantMeaning(t, "repo CI capabilities", []string{"github-sandbox", legacy}, repo.CICapabilities)
}

// Migration 83 rewrites every past retired capability name in
// repo_config.ci_capabilities for stores already at schema 82, preserving
// non-retired entries, collapsing duplicates where the canonical grant is
// already held, and handling both CSV and JSON-array column encodings.
// storedGrantState is every place a store migration retiring a capability
// rewrites, read back raw: the three grant tables and the stored text of
// each capability-list column.
func storedGrantState(t *testing.T, ctx context.Context, db *sql.DB) map[string]any {
	t.Helper()
	out := map[string]any{
		"task_grant":     grantRows(t, ctx, db, "task_grant", "task_id"),
		"template_grant": grantRows(t, ctx, db, "template_grant", "template_id"),
		"schedule_grant": grantRows(t, ctx, db, "schedule_grant", "schedule_id"),
	}
	var defaults, sessions string
	if err := db.QueryRowContext(ctx, "SELECT `default_capabilities`, `console_sessions` FROM `grain_config` WHERE `id` = 1").Scan(&defaults, &sessions); err != nil {
		t.Fatal(err)
	}
	out["grain_config.default_capabilities"], out["grain_config.console_sessions"] = defaults, sessions
	rows, err := db.QueryContext(ctx, "SELECT `owner`, `name`, `default_capabilities`, `ci_capabilities` FROM `repo_config`")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var owner, name, def, ci string
		if err := rows.Scan(&owner, &name, &def, &ci); err != nil {
			t.Fatal(err)
		}
		out["repo_config "+owner+"/"+name] = [2]string{def, ci}
	}
	return out
}

// Migration 86 rewrites corp-research in every stored location and nothing
// else, leaves a store already holding only the method grant untouched, and
// changes nothing when it runs a second time.
func TestMigration86RewritesCorpResearchIdempotently(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)

	cfg := testConfig()
	cfg.DefaultCapabilities = []string{"bootstrap-playbooks", "corp-research", "read-issues"}
	cfg.ConsoleSessions = []model.ConsoleSession{
		{ID: "director", Title: "Director", Capabilities: []string{"corp-research", "bootstrap-playbooks"}},
		{ID: "quiet", Title: "Quiet", Capabilities: []string{"bootstrap-playbooks"}},
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: widgets,
		DefaultCapabilities: []string{"corp-research"},
		CICapabilities:      []string{"github-sandbox", "corp-research", model.CorpResearchGrant}}); err != nil {
		t.Fatal(err)
	}
	other := model.RepoRef{Owner: "acme", Name: "other"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: other,
		DefaultCapabilities: []string{"bootstrap-playbooks"}, CICapabilities: []string{"github-sandbox"}}); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ table, owner, id, capability string }{
		{"task_grant", "task_id", "1", "corp-research"},
		{"task_grant", "task_id", "1", "bootstrap-playbooks"},
		{"task_grant", "task_id", "2", "corp-research"},
		{"task_grant", "task_id", "2", model.CorpResearchGrant},
		{"task_grant", "task_id", "3", "api:tasks:write"},
		{"template_grant", "template_id", "t", "corp-research"},
		{"schedule_grant", "schedule_id", "s", "corp-research"},
		{"schedule_grant", "schedule_id", "s", "read-issues"},
	} {
		if _, err := db.ExecContext(ctx, "INSERT INTO `"+row.table+"` (`"+row.owner+"`, `capability`, `via`) VALUES (?, ?, 'label')",
			row.id, row.capability); err != nil {
			t.Fatal(err)
		}
	}
	before := storedGrantState(t, ctx, db)

	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 85 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating a version-85 store: %v", err)
	}
	after := storedGrantState(t, ctx, db)

	corp := model.CorpResearchGrant
	wantTasks := map[string][]string{"1": {corp, "bootstrap-playbooks"}, "2": {corp}, "3": {"api:tasks:write"}}
	if got := after["task_grant"]; !equalRows(got.(map[string][]string), wantTasks) {
		t.Errorf("task grants = %v, want %v", got, wantTasks)
	}
	if got, want := after["template_grant"], map[string][]string{"t": {corp}}; !equalRows(got.(map[string][]string), want) {
		t.Errorf("template grants = %v, want %v", got, want)
	}
	if got, want := after["schedule_grant"], map[string][]string{"s": {"read-issues", corp}}; !equalRows(got.(map[string][]string), want) {
		t.Errorf("schedule grants = %v, want %v", got, want)
	}
	gotCfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"bootstrap-playbooks", corp, "read-issues"}; !slices.Equal(gotCfg.DefaultCapabilities, want) {
		t.Errorf("default capabilities = %v, want %v", gotCfg.DefaultCapabilities, want)
	}
	if got, want := gotCfg.ConsoleSessions[0].Capabilities, []string{corp, "bootstrap-playbooks"}; !slices.Equal(got, want) {
		t.Errorf("director capabilities = %v, want %v", got, want)
	}
	gotRepo, err := store.GetRepoConfig(ctx, widgets)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{corp}; !slices.Equal(gotRepo.DefaultCapabilities, want) {
		t.Errorf("repo default capabilities = %v, want %v", gotRepo.DefaultCapabilities, want)
	}
	if want := []string{"github-sandbox", corp}; !slices.Equal(gotRepo.CICapabilities, want) {
		t.Errorf("repo CI capabilities = %v, want %v", gotRepo.CICapabilities, want)
	}

	// Nothing that did not hold corp-research changed at all.
	if key := "repo_config acme/other"; before[key] != after[key] {
		t.Errorf("%s changed from %v to %v, holding no corp-research", key, before[key], after[key])
	}
	if got, want := gotCfg.ConsoleSessions[1], cfg.ConsoleSessions[1]; got.ID != want.ID || got.Title != want.Title ||
		!slices.Equal(got.Capabilities, want.Capabilities) {
		t.Errorf("quiet session = %+v, want it unchanged from %+v", got, want)
	}
	if got := after["task_grant"].(map[string][]string)["3"]; !slices.Equal(got, before["task_grant"].(map[string][]string)["3"]) {
		t.Errorf("task 3, holding no corp-research, holds %v after the migration", got)
	}

	// Running it again is a no-op.
	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 85 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating the store a second time: %v", err)
	}
	again := storedGrantState(t, ctx, db)
	for key, v := range after {
		if fmt.Sprint(again[key]) != fmt.Sprint(v) {
			t.Errorf("a second migration changed %s from %v to %v", key, v, again[key])
		}
	}
}

func equalRows(got, want map[string][]string) bool {
	if len(got) != len(want) {
		return false
	}
	for id, names := range want {
		w := slices.Clone(names)
		sort.Strings(w)
		if !slices.Equal(got[id], w) {
			return false
		}
	}
	return true
}

func TestMigration83RewritesRetiredCICapabilities(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)

	for _, r := range retiredGrants {
		widgets := model.RepoRef{Owner: "widgets", Name: r.legacy}
		dedup := model.RepoRef{Owner: "dedup", Name: r.legacy}
		jsonEncoded := model.RepoRef{Owner: "json", Name: r.legacy}

		if err := store.PutRepoConfig(ctx, model.RepoConfig{
			Repo:           widgets,
			CICapabilities: []string{"github-sandbox", r.legacy, "release-gate-read"},
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.PutRepoConfig(ctx, model.RepoConfig{
			Repo:           dedup,
			CICapabilities: append(append(slices.Clone(r.to), r.legacy), "read-issues"),
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: jsonEncoded, SetupCommand: "make test"}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx,
			"UPDATE `repo_config` SET `ci_capabilities` = ? WHERE `owner` = ? AND `name` = ?",
			`["`+r.legacy+`"]`, jsonEncoded.Owner, jsonEncoded.Name); err != nil {
			t.Fatal(err)
		}
	}
	empty := model.RepoRef{Owner: "acme", Name: "empty"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: empty, SetupCommand: "make test"}); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 82 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating a version-82 store: %v", err)
	}

	for _, r := range retiredGrants {
		widgets := model.RepoRef{Owner: "widgets", Name: r.legacy}
		dedup := model.RepoRef{Owner: "dedup", Name: r.legacy}
		jsonEncoded := model.RepoRef{Owner: "json", Name: r.legacy}

		beforeWidgets := []string{"github-sandbox", r.legacy, "release-gate-read"}
		gotWidgets, err := store.GetRepoConfig(ctx, widgets)
		if err != nil || gotWidgets == nil {
			t.Fatalf("%s widgets: %v, %+v", r.legacy, err, gotWidgets)
		}
		wantWidgets := append(append([]string{"github-sandbox"}, r.to...), "release-gate-read")
		if !slices.Equal(gotWidgets.CICapabilities, wantWidgets) {
			t.Errorf("%s: widgets CI capabilities = %v, want %v", r.legacy, gotWidgets.CICapabilities, wantWidgets)
		}
		sameGrantMeaning(t, r.legacy+" widgets CI capabilities", beforeWidgets, gotWidgets.CICapabilities)

		beforeDedup := append(append(slices.Clone(r.to), r.legacy), "read-issues")
		gotDedup, err := store.GetRepoConfig(ctx, dedup)
		if err != nil || gotDedup == nil {
			t.Fatalf("%s dedup: %v, %+v", r.legacy, err, gotDedup)
		}
		wantDedup := append(slices.Clone(r.to), "read-issues")
		if !slices.Equal(gotDedup.CICapabilities, wantDedup) {
			t.Errorf("%s: dedup CI capabilities = %v, want %v", r.legacy, gotDedup.CICapabilities, wantDedup)
		}
		sameGrantMeaning(t, r.legacy+" dedup CI capabilities", beforeDedup, gotDedup.CICapabilities)

		gotJSON, err := store.GetRepoConfig(ctx, jsonEncoded)
		if err != nil || gotJSON == nil {
			t.Fatalf("%s json: %v, %+v", r.legacy, err, gotJSON)
		}
		if !slices.Equal(gotJSON.CICapabilities, r.to) {
			t.Errorf("%s: json-encoded CI capabilities = %v, want %v", r.legacy, gotJSON.CICapabilities, r.to)
		}
	}

	gotEmpty, err := store.GetRepoConfig(ctx, empty)
	if err != nil || gotEmpty == nil {
		t.Fatalf("empty: %v, %+v", err, gotEmpty)
	}
	if len(gotEmpty.CICapabilities) != 0 {
		t.Errorf("empty CI capabilities = %v, want empty", gotEmpty.CICapabilities)
	}
}

// A store written before migration 90 whose capability_policy carries a
// catalogue with retired in-process entries (memory, release-notes,
// test-quarantine, ...) folds them into api_scopes.presets.<name>, merges
// restrictions conservatively when both exist, and rewrites retired task
// defaults inside the catalogue.
func TestMigrationFoldsRetiredCatalogueInProcess(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	off := false
	on := true

	cfg := testConfig()
	cfg.CapabilityPolicy = model.CapabilityPolicy{
		Catalogue: &model.CapabilityCatalogue{
			Defaults: model.CatalogueDefaults{
				Task: []string{"pipeline-control", "bootstrap-playbooks"},
				Repos: map[string]model.CatalogueRepoDefaults{
					"acme/widgets": {
						Task: []string{"release-notes", "memory"},
						CI:   []string{"gcp-key"},
					},
				},
			},
			InProcess: map[string]model.CatalogueInProcess{
				"memory": {Enabled: &off},
				"release-notes": {Restrictions: &model.CapabilityRestrictions{
					Repos:          model.Patterns{"acme/*"},
					RequiresPerson: true,
				}},
				"test-quarantine": {Restrictions: &model.CapabilityRestrictions{
					Repos: model.Patterns{"acme/*"},
					Kinds: model.Patterns{"task", "session"},
				}},
				"bootstrap-playbooks": {Restrictions: &model.CapabilityRestrictions{
					Repos: model.Patterns{"ai-on-gke/grain"},
				}},
			},
			APIScopes: model.CatalogueAPIScopes{
				Presets: map[string]model.CataloguePreset{
					"test-quarantine": {
						Enabled: &on,
						Restrictions: &model.CapabilityRestrictions{
							Repos: model.Patterns{"acme/widgets"},
							Kinds: model.Patterns{"task"},
						},
					},
				},
			},
		},
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 65 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating a version-65 store: %v", err)
	}

	after, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cat := after.CapabilityPolicy.Catalogue
	if cat == nil {
		t.Fatal("catalogue lost after migration")
	}
	for _, retired := range []string{"memory", "release-notes", "test-quarantine"} {
		if _, exists := cat.InProcess[retired]; exists {
			t.Errorf("inProcess[%q] survived migration: %+v", retired, cat.InProcess)
		}
	}
	if _, exists := cat.InProcess["bootstrap-playbooks"]; !exists {
		t.Error("inProcess[bootstrap-playbooks] was dropped")
	}
	mem, ok := cat.APIScopes.Presets["memory"]
	if !ok || mem.Enabled == nil || *mem.Enabled {
		t.Fatalf("apiScopes.presets[memory] = %+v, want disabled", mem)
	}
	rn, ok := cat.APIScopes.Presets["release-notes"]
	if !ok || rn.Restrictions == nil || !rn.Restrictions.RequiresPerson || !slices.Equal([]string(rn.Restrictions.Repos), []string{"acme/*"}) {
		t.Fatalf("apiScopes.presets[release-notes] = %+v", rn)
	}
	tq, ok := cat.APIScopes.Presets["test-quarantine"]
	if !ok || tq.Restrictions == nil || !slices.Equal([]string(tq.Restrictions.Repos), []string{"acme/widgets"}) ||
		!slices.Equal([]string(tq.Restrictions.Kinds), []string{"task"}) {
		t.Fatalf("apiScopes.presets[test-quarantine] = %+v, want conservative intersection", tq)
	}
	if want := []string{"api:pipeline-control", "bootstrap-playbooks"}; !slices.Equal(cat.Defaults.Task, want) {
		t.Errorf("catalogue.defaults.task = %v, want %v", cat.Defaults.Task, want)
	}
	if want := []string{"api:release-notes", "api:memory"}; !slices.Equal(cat.Defaults.Repos["acme/widgets"].Task, want) {
		t.Errorf("catalogue.defaults.repos[acme/widgets].task = %v, want %v", cat.Defaults.Repos["acme/widgets"].Task, want)
	}
	if _, err := cat.ValidateGrants(model.GrantContext{Kind: "task", Grants: []string{"api:memory"}}); err == nil {
		t.Error("migrated catalogue admitted api:memory, want disabled refusal")
	}
}

func TestSchema65StoreAndStateRepoImportRewriteCICapabilities(t *testing.T) {
	t.Parallel()
	t.Run("in_place_from_65", func(t *testing.T) {
		t.Parallel()
		store, db, ctx := openStore(t)
		widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
		if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: widgets, SetupCommand: "make test"}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx,
			"UPDATE `repo_config` SET `ci_capabilities` = ? WHERE `owner` = ? AND `name` = ?",
			`["release-notes"]`, widgets.Owner, widgets.Name); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 65 WHERE `id` = 1"); err != nil {
			t.Fatal(err)
		}
		if err := model.New(db).Init(ctx); err != nil {
			t.Fatalf("migrating a version-65 store: %v", err)
		}
		got, err := store.GetRepoConfig(ctx, widgets)
		if err != nil || got == nil {
			t.Fatalf("GetRepoConfig: %v, %+v", err, got)
		}
		if !slices.Equal(got.CICapabilities, []string{"api:release-notes"}) {
			t.Errorf("repo_config.ci_capabilities = %v, want [api:release-notes]", got.CICapabilities)
		}
	})

	t.Run("staterepo_import_from_64", func(t *testing.T) {
		t.Parallel()
		store, db, ctx := openStore(t)
		dumpDir := filepath.Join("..", "..", "staterepo", "testdata", "upgrade-schema-64")
		if _, err := staterepo.CheckMigration(ctx, db, dumpDir, t.TempDir(), model.SchemaVersion); err != nil {
			t.Fatalf("importing schema-64 state-repo dump: %v", err)
		}
		got, err := store.GetRepoConfig(ctx, model.RepoRef{Owner: "acme", Name: "widgets"})
		if err != nil || got == nil {
			t.Fatalf("GetRepoConfig: %v, %+v", err, got)
		}
		if !slices.Equal(got.CICapabilities, []string{"api:release-notes"}) {
			t.Errorf("repo_config.ci_capabilities after state-repo import = %v, want [api:release-notes]", got.CICapabilities)
		}
	})
}

func TestMigration96SeedsGCPPermissionSetsAndRewritesGrants(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)

	cfg := testConfig()
	cfg.GCPProject = "acme-pool"
	cfg.GCPServiceAccountEmail = "sandbox-agent@acme-pool.iam.gserviceaccount.com"
	cfg.GCPServiceAccountCount = 3
	cfg.DefaultCapabilities = []string{"bootstrap-playbooks", "gcp-key", "gcp-key:deploy"}
	cfg.ConsoleSessions = []model.ConsoleSession{
		{ID: "director", Title: "Director", Capabilities: []string{"gcp-key", "gcp-key:staging", "bootstrap-playbooks"}},
	}
	cfg.CapabilityPolicy = model.CapabilityPolicy{
		Rules: []model.CapabilityPolicyRule{
			{Capability: "gcp-key:deploy", Repos: []string{"acme/widgets"}, AllowUntrusted: true},
			{Capability: "gcp-key", Repos: []string{"acme/public"}, AllowUntrusted: true},
		},
		Catalogue: &model.CapabilityCatalogue{
			Defaults: model.CatalogueDefaults{
				Task: []string{"bootstrap-playbooks", "gcp-key", "gcp-key:deploy"},
				CI:   []string{"gcp-key:staging"},
				Repos: map[string]model.CatalogueRepoDefaults{
					"acme/widgets": {
						Task: []string{"gcp-key", "gcp-key:deploy", "bootstrap-playbooks"},
						CI:   []string{"github-sandbox", "gcp-key", "gcp-key:staging"},
					},
				},
			},
		},
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	if err := store.PutGCPKey(ctx, model.GCPKey{
		Name:                "deploy",
		ProjectID:           "acme-deploy",
		ServiceAccountEmail: "deployer@acme-deploy.iam.gserviceaccount.com",
		ServiceAccountCount: 2,
		MinterCredential:    "custom-deploy-minter",
		MaxKeyAge:           2 * time.Hour,
		Description:         "Deploy pool",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutGCPKey(ctx, model.GCPKey{
		Name:                "staging",
		ProjectID:           "acme-staging",
		ServiceAccountEmail: "staging-bot",
		ServiceAccountCount: 1,
		Description:         "Staging bot",
	}); err != nil {
		t.Fatal(err)
	}

	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                widgets,
		DefaultCapabilities: []string{"gcp-key", "gcp-key:deploy", "bootstrap-playbooks"},
		CICapabilities:      []string{"github-sandbox", "gcp-key", "gcp-key:staging"},
	}); err != nil {
		t.Fatal(err)
	}

	for _, row := range []struct{ table, owner, id, capability string }{
		{"task_grant", "task_id", "t1", "gcp-key"},
		{"task_grant", "task_id", "t1", "gcp-key:deploy"},
		{"task_grant", "task_id", "t1", "bootstrap-playbooks"},
		{"template_grant", "template_id", "suite-tmpl", "gcp-key"},
		{"template_grant", "template_id", "suite-tmpl", "gcp-key:staging"},
		{"schedule_grant", "schedule_id", "sched-1", "gcp-key:deploy"},
	} {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO `"+row.table+"` (`"+row.owner+"`, `capability`, `via`) VALUES (?, ?, 'label')",
			row.id, row.capability); err != nil {
			t.Fatal(err)
		}
	}

	// Rewind to schema 96 and export a pre-migration state-repo dump before migrating in place.
	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 96 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	exportDir := t.TempDir()
	if err := staterepo.Export(ctx, db, exportDir); err != nil {
		t.Fatalf("exporting version-96 store: %v", err)
	}
	if err := staterepo.WriteSchemaVersion(exportDir, 96); err != nil {
		t.Fatalf("stamping version-96 export: %v", err)
	}

	// Migrate in place to 97.
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating version-96 store to 97: %v", err)
	}

	assertMigrated := func(t *testing.T, s *model.Store, sqlDB *sql.DB) {
		t.Helper()
		gotCfg, err := s.GetConfig(ctx)
		if err != nil {
			t.Fatal(err)
		}
		gcpServer, ok := gotCfg.MCPServers[model.GCPCapabilityServerID]
		if !ok {
			t.Fatalf("MCPServers missing %q: %+v", model.GCPCapabilityServerID, gotCfg.MCPServers)
		}
		if gcpServer.Notice == "" {
			t.Errorf("expected deprecation notice on %s server entry", model.GCPCapabilityServerID)
		}
		resolvedEnv := map[string]string{}
		for k, v := range gcpServer.Env {
			resolvedEnv[k] = `{"type": "service_account", "client_email": "minter@acme.iam.gserviceaccount.com", "secret": "` + v + `"}`
		}
		parsedSets, err := gcpsets.ParseWithEnv(gcpServer.Config, func(k string) string { return resolvedEnv[k] })
		if err != nil {
			t.Fatalf("gcpsets.ParseWithEnv on seeded server config: %v (config=%#v, env=%#v)", err, gcpServer.Config, gcpServer.Env)
		}
		if len(parsedSets.Sets) != 3 {
			t.Fatalf("seeded %d permission sets, want 3: %+v", len(parsedSets.Sets), parsedSets.Sets)
		}
		sandboxSet := parsedSets.Set(model.GCPKeyDefaultPermissionSet)
		if sandboxSet == nil {
			t.Fatalf("missing %s set", model.GCPKeyDefaultPermissionSet)
		}
		wantSandboxAccounts := []string{
			"sandbox-agent-0@acme-pool.iam.gserviceaccount.com",
			"sandbox-agent-1@acme-pool.iam.gserviceaccount.com",
			"sandbox-agent-2@acme-pool.iam.gserviceaccount.com",
		}
		if sandboxSet.Project != "acme-pool" || !slices.Equal(sandboxSet.Accounts, wantSandboxAccounts) || sandboxSet.KeyPath != "" || !sandboxSet.AdoptLegacy {
			t.Errorf("sandbox-agent set = %+v", sandboxSet)
		}
		deploySet := parsedSets.Set("deploy")
		if deploySet == nil {
			t.Fatalf("missing deploy set")
		}
		wantDeployAccounts := []string{
			"deployer-0@acme-deploy.iam.gserviceaccount.com",
			"deployer-1@acme-deploy.iam.gserviceaccount.com",
		}
		if deploySet.Project != "acme-deploy" || !slices.Equal(deploySet.Accounts, wantDeployAccounts) ||
			deploySet.MaxLease != 2*time.Hour || deploySet.KeyPath != "/home/debian/.gcp-service-account-deploy.json" {
			t.Errorf("deploy set = %+v", deploySet)
		}
		stagingSet := parsedSets.Set("staging")
		if stagingSet == nil {
			t.Fatalf("missing staging set")
		}
		if stagingSet.Project != "acme-staging" || !slices.Equal(stagingSet.Accounts, []string{"staging-bot@acme-staging.iam.gserviceaccount.com"}) ||
			stagingSet.MaxLease != 24*time.Hour || stagingSet.KeyPath != "/home/debian/.gcp-service-account-staging.json" {
			t.Errorf("staging set = %+v", stagingSet)
		}

		sandboxGrant := model.GCPKeySandboxAgentGrant
		deployGrant := model.GCPKeyServerGrant("deploy")
		stagingGrant := model.GCPKeyServerGrant("staging")

		if want := []string{"bootstrap-playbooks", sandboxGrant, deployGrant}; !slices.Equal(gotCfg.DefaultCapabilities, want) {
			t.Errorf("DefaultCapabilities = %v, want %v", gotCfg.DefaultCapabilities, want)
		}
		if want := []string{sandboxGrant, stagingGrant, "bootstrap-playbooks"}; !slices.Equal(gotCfg.ConsoleSessions[0].Capabilities, want) {
			t.Errorf("ConsoleSessions[0].Capabilities = %v, want %v", gotCfg.ConsoleSessions[0].Capabilities, want)
		}
		if len(gotCfg.CapabilityPolicy.Rules) != 2 ||
			gotCfg.CapabilityPolicy.Rules[0].Capability != model.GCPKeyRef || gotCfg.CapabilityPolicy.Rules[0].AllowUntrusted ||
			gotCfg.CapabilityPolicy.Rules[1].Capability != model.GCPKeyRef || !gotCfg.CapabilityPolicy.Rules[1].AllowUntrusted {
			t.Errorf("CapabilityPolicy.Rules = %+v, want named rule AllowUntrusted=false and bare rule AllowUntrusted=true on %s", gotCfg.CapabilityPolicy.Rules, model.GCPKeyRef)
		}
		if gotCfg.CapabilityPolicy.Catalogue == nil {
			t.Fatalf("CapabilityPolicy.Catalogue nil")
		}
		if want := []string{"bootstrap-playbooks", sandboxGrant, deployGrant}; !slices.Equal(gotCfg.CapabilityPolicy.Catalogue.Defaults.Task, want) {
			t.Errorf("Catalogue.Defaults.Task = %v, want %v", gotCfg.CapabilityPolicy.Catalogue.Defaults.Task, want)
		}
		if want := []string{stagingGrant}; !slices.Equal(gotCfg.CapabilityPolicy.Catalogue.Defaults.CI, want) {
			t.Errorf("Catalogue.Defaults.CI = %v, want %v", gotCfg.CapabilityPolicy.Catalogue.Defaults.CI, want)
		}
		if want := []string{sandboxGrant, deployGrant, "bootstrap-playbooks"}; !slices.Equal(gotCfg.CapabilityPolicy.Catalogue.Defaults.Repos["acme/widgets"].Task, want) {
			t.Errorf("Catalogue.Defaults.Repos[acme/widgets].Task = %v, want %v", gotCfg.CapabilityPolicy.Catalogue.Defaults.Repos["acme/widgets"].Task, want)
		}
		if want := []string{"github-sandbox", sandboxGrant, stagingGrant}; !slices.Equal(gotCfg.CapabilityPolicy.Catalogue.Defaults.Repos["acme/widgets"].CI, want) {
			t.Errorf("Catalogue.Defaults.Repos[acme/widgets].CI = %v, want %v", gotCfg.CapabilityPolicy.Catalogue.Defaults.Repos["acme/widgets"].CI, want)
		}

		gotRepo, err := s.GetRepoConfig(ctx, widgets)
		if err != nil || gotRepo == nil {
			t.Fatalf("GetRepoConfig: %v, %+v", err, gotRepo)
		}
		if want := []string{sandboxGrant, deployGrant, "bootstrap-playbooks"}; !slices.Equal(gotRepo.DefaultCapabilities, want) {
			t.Errorf("repo DefaultCapabilities = %v, want %v", gotRepo.DefaultCapabilities, want)
		}
		if want := []string{"github-sandbox", sandboxGrant, stagingGrant}; !slices.Equal(gotRepo.CICapabilities, want) {
			t.Errorf("repo CICapabilities = %v, want %v", gotRepo.CICapabilities, want)
		}

		wantTasks := map[string][]string{"t1": {deployGrant, sandboxGrant, "bootstrap-playbooks"}}
		if got := grantRows(t, ctx, sqlDB, "task_grant", "task_id"); !equalRows(got, wantTasks) {
			t.Errorf("task_grant = %v, want %v", got, wantTasks)
		}
		wantTemplates := map[string][]string{"suite-tmpl": {sandboxGrant, stagingGrant}}
		if got := grantRows(t, ctx, sqlDB, "template_grant", "template_id"); !equalRows(got, wantTemplates) {
			t.Errorf("template_grant = %v, want %v", got, wantTemplates)
		}
		wantSchedules := map[string][]string{"sched-1": {deployGrant}}
		if got := grantRows(t, ctx, sqlDB, "schedule_grant", "schedule_id"); !equalRows(got, wantSchedules) {
			t.Errorf("schedule_grant = %v, want %v", got, wantSchedules)
		}
	}

	assertMigrated(t, store, db)

	// Re-running migration 97 is idempotent.
	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 96 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("re-running migration 97: %v", err)
	}
	assertMigrated(t, store, db)

	// Importing the pre-migration schema-96 state-repo export into a fresh store rewrites grants and seeds permission sets.
	importedStore, importedDB, importCtx := openStore(t)
	_ = importCtx
	if err := staterepo.Import(ctx, importedDB, exportDir); err != nil {
		t.Fatalf("staterepo.Import of schema-96 export: %v", err)
	}
	assertMigrated(t, importedStore, importedDB)
}

func TestMigration96HandlesPreexistingAdminConfigShapes(t *testing.T) {
	t.Run("ambient admin_credential_source with no existing sets omits unused default admin", func(t *testing.T) {
		store, db, ctx := openStore(t)
		cfg := model.Config{
			GCPProject:             "acme-pool",
			GCPServiceAccountEmail: "sandbox-agent@acme-pool.iam.gserviceaccount.com",
			GCPServiceAccountCount: 1,
			MCPServers: map[string]protocol.ServerConfig{
				model.GCPCapabilityServerID: {
					Command: "grain",
					Args:    []string{"capability", "gcp"},
					Builtin: "gcp",
					Config: map[string]any{
						"admin_credential_source": "ambient",
					},
				},
			},
		}
		if err := store.PutConfig(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 96 WHERE `id` = 1"); err != nil {
			t.Fatal(err)
		}
		if err := model.New(db).Init(ctx); err != nil {
			t.Fatalf("migrating 96 -> 97: %v", err)
		}
		gotCfg, err := store.GetConfig(ctx)
		if err != nil {
			t.Fatal(err)
		}
		srv := gotCfg.MCPServers[model.GCPCapabilityServerID]
		resolvedEnv := map[string]string{}
		for k := range srv.Env {
			resolvedEnv[k] = `{"type": "service_account", "client_email": "minter@acme.iam.gserviceaccount.com"}`
		}
		if _, err := gcpsets.ParseWithEnv(srv.Config, func(k string) string { return resolvedEnv[k] }); err != nil {
			t.Fatalf("gcpsets.ParseWithEnv after migration: %v (config=%#v)", err, srv.Config)
		}
	})

	t.Run("existing sets with ambient source and admin_credential preserve configured default admin", func(t *testing.T) {
		store, db, ctx := openStore(t)
		cfg := model.Config{
			GCPProject:             "acme-pool",
			GCPServiceAccountEmail: "sandbox-agent@acme-pool.iam.gserviceaccount.com",
			GCPServiceAccountCount: 1,
			MCPServers: map[string]protocol.ServerConfig{
				model.GCPCapabilityServerID: {
					Command: "grain",
					Args:    []string{"capability", "gcp"},
					Builtin: "gcp",
					Config: map[string]any{
						protocol.DeploymentIDConfig: "049f1e09",
						"admin_credential_source":   "ambient",
						"admin_credential":          "$secret:existing-admin",
						"permission_sets": []any{
							map[string]any{
								"name":    "ci-readers",
								"project": "acme-ci",
								"roles":   []any{"roles/storage.objectViewer"},
							},
						},
					},
				},
			},
		}
		if err := store.PutConfig(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 96 WHERE `id` = 1"); err != nil {
			t.Fatal(err)
		}
		if err := model.New(db).Init(ctx); err != nil {
			t.Fatalf("migrating 96 -> 97: %v", err)
		}
		gotCfg, err := store.GetConfig(ctx)
		if err != nil {
			t.Fatal(err)
		}
		srv := gotCfg.MCPServers[model.GCPCapabilityServerID]
		// Resolve top-level $secret: reference as protocol.Attach does.
		cfgCopy := map[string]any{}
		for k, v := range srv.Config {
			cfgCopy[k] = v
		}
		cfgCopy["admin_credential"] = `{"type": "service_account", "client_email": "existing@acme.iam.gserviceaccount.com"}`
		resolvedEnv := map[string]string{}
		for k := range srv.Env {
			resolvedEnv[k] = `{"type": "service_account", "client_email": "minter@acme.iam.gserviceaccount.com"}`
		}
		parsed, err := gcpsets.ParseWithEnv(cfgCopy, func(k string) string { return resolvedEnv[k] })
		if err != nil {
			t.Fatalf("gcpsets.ParseWithEnv after migration: %v (config=%#v)", err, srv.Config)
		}
		defAdmin := parsed.Admin("default")
		if defAdmin == nil || defAdmin.CredentialSource != gcpsets.SourceConfigured {
			t.Fatalf("default admin = %+v, want configured credential_source", defAdmin)
		}
	})
}
