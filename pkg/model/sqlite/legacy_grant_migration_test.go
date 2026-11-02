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
// fidelity docs/design/unified-grants.md §3 asks of every migrated row.
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
	// corp-research is the Google build's: core has no alias for it, which
	// that build registers with capability.Register (retiredAs).
	{85, "corp-research", []string{model.CorpResearchGrant}},
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
	cfg.DefaultCapabilities = []string{legacy, "gcp-key"}
	cfg.ConsoleSessions = []model.ConsoleSession{
		{ID: "director", Title: "Director", Capabilities: []string{"self-debug", legacy}},
		{ID: "steward", Title: "Steward", Capabilities: append(slices.Clone(replacements), legacy)},
		{ID: "quiet", Title: "Quiet"},
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: widgets, DefaultCapabilities: []string{"self-debug", legacy},
		CICapabilities: []string{"package-publish", legacy}}); err != nil {
		t.Fatal(err)
	}
	fixture := map[string]map[string][]string{
		"task_grant": {
			"1": {legacy, "self-debug"},
			"2": append([]string{legacy}, replacements...),
			"3": {"api:tasks:write"},
		},
		"template_grant": {"t": {legacy}},
		"schedule_grant": {"s": {legacy, "gcp-key"}},
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
	if want := append(slices.Clone(replacements), "gcp-key"); !slices.Equal(after.DefaultCapabilities, want) {
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
	if want := append([]string{"self-debug"}, replacements...); !slices.Equal(repo.DefaultCapabilities, want) {
		t.Errorf("repo default capabilities = %v, want %v", repo.DefaultCapabilities, want)
	}
	// Migrations 83, 84 and 85 rewrite retired names in repo_config.ci_capabilities as well.
	if want := append([]string{"package-publish"}, replacements...); !slices.Equal(repo.CICapabilities, want) {
		t.Errorf("repo CI capabilities = %v, want %v", repo.CICapabilities, want)
	}
	sameGrantMeaning(t, "repo CI capabilities", []string{"package-publish", legacy}, repo.CICapabilities)
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

// Migration 85 rewrites corp-research in every stored location and nothing
// else, leaves a store already holding only the method grant untouched, and
// changes nothing when it runs a second time.
func TestMigration85RewritesCorpResearchIdempotently(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)

	cfg := testConfig()
	cfg.DefaultCapabilities = []string{"self-debug", "corp-research", "gcp-key"}
	cfg.ConsoleSessions = []model.ConsoleSession{
		{ID: "director", Title: "Director", Capabilities: []string{"corp-research", "self-debug"}},
		{ID: "quiet", Title: "Quiet", Capabilities: []string{"self-debug"}},
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: widgets,
		DefaultCapabilities: []string{"corp-research"},
		CICapabilities:      []string{"package-publish", "corp-research", model.CorpResearchGrant}}); err != nil {
		t.Fatal(err)
	}
	other := model.RepoRef{Owner: "acme", Name: "other"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: other,
		DefaultCapabilities: []string{"self-debug"}, CICapabilities: []string{"package-publish"}}); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ table, owner, id, capability string }{
		{"task_grant", "task_id", "1", "corp-research"},
		{"task_grant", "task_id", "1", "self-debug"},
		{"task_grant", "task_id", "2", "corp-research"},
		{"task_grant", "task_id", "2", model.CorpResearchGrant},
		{"task_grant", "task_id", "3", "api:tasks:write"},
		{"template_grant", "template_id", "t", "corp-research"},
		{"schedule_grant", "schedule_id", "s", "corp-research"},
		{"schedule_grant", "schedule_id", "s", "gcp-key"},
	} {
		if _, err := db.ExecContext(ctx, "INSERT INTO `"+row.table+"` (`"+row.owner+"`, `capability`, `via`) VALUES (?, ?, 'label')",
			row.id, row.capability); err != nil {
			t.Fatal(err)
		}
	}
	before := storedGrantState(t, ctx, db)

	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 84 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating a version-84 store: %v", err)
	}
	after := storedGrantState(t, ctx, db)

	corp := model.CorpResearchGrant
	wantTasks := map[string][]string{"1": {corp, "self-debug"}, "2": {corp}, "3": {"api:tasks:write"}}
	if got := after["task_grant"]; !equalRows(got.(map[string][]string), wantTasks) {
		t.Errorf("task grants = %v, want %v", got, wantTasks)
	}
	if got, want := after["template_grant"], map[string][]string{"t": {corp}}; !equalRows(got.(map[string][]string), want) {
		t.Errorf("template grants = %v, want %v", got, want)
	}
	if got, want := after["schedule_grant"], map[string][]string{"s": {"gcp-key", corp}}; !equalRows(got.(map[string][]string), want) {
		t.Errorf("schedule grants = %v, want %v", got, want)
	}
	gotCfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"self-debug", corp, "gcp-key"}; !slices.Equal(gotCfg.DefaultCapabilities, want) {
		t.Errorf("default capabilities = %v, want %v", gotCfg.DefaultCapabilities, want)
	}
	if got, want := gotCfg.ConsoleSessions[0].Capabilities, []string{corp, "self-debug"}; !slices.Equal(got, want) {
		t.Errorf("director capabilities = %v, want %v", got, want)
	}
	gotRepo, err := store.GetRepoConfig(ctx, widgets)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{corp}; !slices.Equal(gotRepo.DefaultCapabilities, want) {
		t.Errorf("repo default capabilities = %v, want %v", gotRepo.DefaultCapabilities, want)
	}
	if want := []string{"package-publish", corp}; !slices.Equal(gotRepo.CICapabilities, want) {
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
	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 84 WHERE `id` = 1"); err != nil {
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
			CICapabilities: []string{"package-publish", r.legacy, "repo-read:acme/tools"},
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.PutRepoConfig(ctx, model.RepoConfig{
			Repo:           dedup,
			CICapabilities: append(append(slices.Clone(r.to), r.legacy), "gcp-key"),
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

		beforeWidgets := []string{"package-publish", r.legacy, "repo-read:acme/tools"}
		gotWidgets, err := store.GetRepoConfig(ctx, widgets)
		if err != nil || gotWidgets == nil {
			t.Fatalf("%s widgets: %v, %+v", r.legacy, err, gotWidgets)
		}
		wantWidgets := append(append([]string{"package-publish"}, r.to...), "repo-read:acme/tools")
		if !slices.Equal(gotWidgets.CICapabilities, wantWidgets) {
			t.Errorf("%s: widgets CI capabilities = %v, want %v", r.legacy, gotWidgets.CICapabilities, wantWidgets)
		}
		sameGrantMeaning(t, r.legacy+" widgets CI capabilities", beforeWidgets, gotWidgets.CICapabilities)

		beforeDedup := append(append(slices.Clone(r.to), r.legacy), "gcp-key")
		gotDedup, err := store.GetRepoConfig(ctx, dedup)
		if err != nil || gotDedup == nil {
			t.Fatalf("%s dedup: %v, %+v", r.legacy, err, gotDedup)
		}
		wantDedup := append(slices.Clone(r.to), "gcp-key")
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
