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
// A retired name in before is read as what it granted when it was stored,
// the grant(s) it became (retiredAs): grain no longer reads it as an alias.
func sameGrantMeaning(t *testing.T, what string, before, after []string) {
	t.Helper()
	var meant []string
	for _, name := range before {
		meant = append(meant, retiredAs(name)...)
	}
	for _, s := range model.APIScopes() {
		if b, a := model.APIGrantsOf(meant).Allows(s.Scope, ""), model.APIGrantsOf(after).Allows(s.Scope, ""); b != a {
			t.Errorf("%s: %s granted %v before the migration and %v after (%v -> %v)", what, s.Scope, b, a, before, after)
		}
		if b, a := model.SandboxAPIGrantsOf(meant).Allows(s.Scope, ""), model.SandboxAPIGrantsOf(after).Allows(s.Scope, ""); b != a {
			t.Errorf("%s: %s reached the sandbox %v before the migration and %v after", what, s.Scope, b, a)
		}
	}
	if b, a := slices.ContainsFunc(meant, model.IsDangerousGrant), slices.ContainsFunc(after, model.IsDangerousGrant); b != a {
		t.Errorf("%s: dangerous %v before the migration and %v after", what, b, a)
	}
	for _, name := range before {
		to := retiredAs(name)
		retired := len(to) != 1 || to[0] != name
		if !retired && !slices.Contains(after, canonical(name)) {
			t.Errorf("%s: %q, which is not retired, was lost (%v -> %v)", what, name, before, after)
		}
		if retired && slices.Contains(after, name) {
			t.Errorf("%s: retired %q survived the migration as itself rather than as %v", what, name, to)
		}
	}
}

// canonical is name as migration 132 stores it (model.CanonicalStoredGrant):
// every migration test here runs the store up to model.SchemaVersion, so an
// api:<what> a retirement wrote is read back as its reference.
func canonical(name string) string {
	out, _ := model.CanonicalStoredGrant(name)
	return out
}

// canonicals is canonical over names, each kept once.
func canonicals(names []string) []string {
	out, _ := model.CanonicalStoredGrants(names)
	return out
}

// retiredGrants are the in-process capabilities a store migration
// rewrote, each with the schema version the migration produces and the
// canonical grant(s) it wrote.
var retiredGrants = []struct {
	version int
	legacy  string
	to      []string
}{
	{67, "team-broadcast", canonicals([]string{"api:team-broadcast"})},
	{68, "dispatch-control", canonicals([]string{"api:dispatch-control"})},
	{69, "pipeline-control", canonicals([]string{"api:pipeline-control"})},
	{71, "memory", canonicals([]string{"api:memory"})},
	{72, "release-notes", canonicals([]string{"api:release-notes"})},
	{75, "self-upgrade", canonicals([]string{"api:self-upgrade"})},
	{76, "test-quarantine", canonicals([]string{"api:test-quarantine"})},
	{80, "grain-log", []string{model.GrainJournalGrant}},
	{81, "deployment-health", canonicals([]string{"api:deployment:read", model.GrainHealthGrant})},
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
	{115, "self-debug", canonicals([]string{"api:self-debug", model.GrainSourceGrant})},
	{118, "bootstrap-playbooks", []string{model.BootstrapPlaybooksGrant}},
	{120, "github-credential:release-bot", []string{model.RepoCredentialGrant("release-bot")}},
	{121, "read-issues", []string{model.GitHubReadIssuesGrant}},
	{124, "github-sandbox", []string{model.GitHubSandboxRepoGrant}},
}

// retiredAs is what a store migration rewrote name to: retiredGrants'
// entry for it, which covers the names only a build outside core makes an
// alias, or else model.RetiredGrantsBecame.
func retiredAs(name string) []string {
	for _, r := range retiredGrants {
		if r.legacy == name {
			return r.to
		}
	}
	return model.RetiredGrantsBecame(name)
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
	cfg.DefaultCapabilities = []string{legacy, "release-gate-read"}
	cfg.ConsoleSessions = []model.ConsoleSession{
		{ID: "director", Title: "Director", Capabilities: []string{"release-gate-read", legacy}},
		{ID: "steward", Title: "Steward", Capabilities: append(slices.Clone(replacements), legacy)},
		{ID: "quiet", Title: "Quiet"},
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: widgets, DefaultCapabilities: []string{"release-gate-read", legacy},
		CICapabilities: []string{"release-gate-read", legacy}}); err != nil {
		t.Fatal(err)
	}
	fixture := map[string]map[string][]string{
		"task_grant": {
			"1": {legacy, "release-gate-read"},
			"2": append([]string{legacy}, replacements...),
			"3": {"api:tasks:write"},
		},
		"template_grant": {"t": {legacy}},
		"schedule_grant": {"s": {legacy, "release-gate-read"}},
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
	wantSorted := canonicals(replacements)
	sort.Strings(wantSorted)
	if got := grantRows(t, ctx, db, "task_grant", "task_id")["2"]; !slices.Equal(got, wantSorted) {
		t.Errorf("task 2, holding both spellings, holds %v after the migration, want %v once", got, wantSorted)
	}

	after, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := canonicals(append(slices.Clone(replacements), "release-gate-read")); !slices.Equal(after.DefaultCapabilities, want) {
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
	if got, want := after.ConsoleSessions[1].Capabilities, canonicals(replacements); !slices.Equal(got, want) {
		t.Errorf("steward capabilities = %v, want %v", got, want)
	}

	repo, err := store.GetRepoConfig(ctx, widgets)
	if err != nil {
		t.Fatal(err)
	}
	if want := canonicals(append([]string{"release-gate-read"}, replacements...)); !slices.Equal(repo.DefaultCapabilities, want) {
		t.Errorf("repo default capabilities = %v, want %v", repo.DefaultCapabilities, want)
	}
	// Migrations 83 to 86 rewrite retired names in repo_config.ci_capabilities as well.
	if want := canonicals(append([]string{"release-gate-read"}, replacements...)); !slices.Equal(repo.CICapabilities, want) {
		t.Errorf("repo CI capabilities = %v, want %v", repo.CICapabilities, want)
	}
	sameGrantMeaning(t, "repo CI capabilities", []string{"release-gate-read", legacy}, repo.CICapabilities)
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
	cfg.DefaultCapabilities = []string{"release-gate-read", "corp-research", "api:tasks:read"}
	cfg.ConsoleSessions = []model.ConsoleSession{
		{ID: "director", Title: "Director", Capabilities: []string{"corp-research", "release-gate-read"}},
		{ID: "quiet", Title: "Quiet", Capabilities: []string{"release-gate-read"}},
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: widgets,
		DefaultCapabilities: []string{"corp-research"},
		CICapabilities:      []string{"release-gate-read", "corp-research", model.CorpResearchGrant}}); err != nil {
		t.Fatal(err)
	}
	other := model.RepoRef{Owner: "acme", Name: "other"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: other,
		DefaultCapabilities: []string{"release-gate-read"}, CICapabilities: []string{"release-gate-read"}}); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ table, owner, id, capability string }{
		{"task_grant", "task_id", "1", "corp-research"},
		{"task_grant", "task_id", "1", "release-gate-read"},
		{"task_grant", "task_id", "2", "corp-research"},
		{"task_grant", "task_id", "2", model.CorpResearchGrant},
		{"task_grant", "task_id", "3", "api:tasks:write"},
		{"template_grant", "template_id", "t", "corp-research"},
		{"schedule_grant", "schedule_id", "s", "corp-research"},
		{"schedule_grant", "schedule_id", "s", "release-gate-read"},
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
	wantTasks := map[string][]string{"1": {corp, "release-gate-read"}, "2": {corp}, "3": {canonical("api:tasks:write")}}
	if got := after["task_grant"]; !equalRows(got.(map[string][]string), wantTasks) {
		t.Errorf("task grants = %v, want %v", got, wantTasks)
	}
	if got, want := after["template_grant"], map[string][]string{"t": {corp}}; !equalRows(got.(map[string][]string), want) {
		t.Errorf("template grants = %v, want %v", got, want)
	}
	if got, want := after["schedule_grant"], map[string][]string{"s": {"release-gate-read", corp}}; !equalRows(got.(map[string][]string), want) {
		t.Errorf("schedule grants = %v, want %v", got, want)
	}
	gotCfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"release-gate-read", corp, canonical("api:tasks:read")}; !slices.Equal(gotCfg.DefaultCapabilities, want) {
		t.Errorf("default capabilities = %v, want %v", gotCfg.DefaultCapabilities, want)
	}
	if got, want := gotCfg.ConsoleSessions[0].Capabilities, []string{corp, "release-gate-read"}; !slices.Equal(got, want) {
		t.Errorf("director capabilities = %v, want %v", got, want)
	}
	gotRepo, err := store.GetRepoConfig(ctx, widgets)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{corp}; !slices.Equal(gotRepo.DefaultCapabilities, want) {
		t.Errorf("repo default capabilities = %v, want %v", gotRepo.DefaultCapabilities, want)
	}
	if want := []string{"release-gate-read", corp}; !slices.Equal(gotRepo.CICapabilities, want) {
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
	if got := after["task_grant"].(map[string][]string)["3"]; !slices.Equal(got, canonicals(before["task_grant"].(map[string][]string)["3"])) {
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
			CICapabilities: []string{"release-gate-read", r.legacy, "legacy-extra-cap"},
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.PutRepoConfig(ctx, model.RepoConfig{
			Repo:           dedup,
			CICapabilities: append(append(slices.Clone(r.to), r.legacy), "legacy-extra-cap"),
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

		beforeWidgets := []string{"release-gate-read", r.legacy, "legacy-extra-cap"}
		gotWidgets, err := store.GetRepoConfig(ctx, widgets)
		if err != nil || gotWidgets == nil {
			t.Fatalf("%s widgets: %v, %+v", r.legacy, err, gotWidgets)
		}
		wantWidgets := append(append([]string{"release-gate-read"}, r.to...), "legacy-extra-cap")
		if !slices.Equal(gotWidgets.CICapabilities, wantWidgets) {
			t.Errorf("%s: widgets CI capabilities = %v, want %v", r.legacy, gotWidgets.CICapabilities, wantWidgets)
		}
		sameGrantMeaning(t, r.legacy+" widgets CI capabilities", beforeWidgets, gotWidgets.CICapabilities)

		beforeDedup := append(append(slices.Clone(r.to), r.legacy), "legacy-extra-cap")
		gotDedup, err := store.GetRepoConfig(ctx, dedup)
		if err != nil || gotDedup == nil {
			t.Fatalf("%s dedup: %v, %+v", r.legacy, err, gotDedup)
		}
		wantDedup := append(slices.Clone(r.to), "legacy-extra-cap")
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
				Task: []string{"pipeline-control", "release-gate-read"},
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
				"release-gate-read": {Restrictions: &model.CapabilityRestrictions{
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
	if _, exists := cat.InProcess["release-gate-read"]; !exists {
		t.Error("inProcess[read-issues] was dropped")
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
	if want := []string{canonical("api:pipeline-control"), "release-gate-read"}; !slices.Equal(cat.Defaults.Task, want) {
		t.Errorf("catalogue.defaults.task = %v, want %v", cat.Defaults.Task, want)
	}
	// Migration 90 rewrites release-notes and memory to api:release-notes and
	// api:memory; migration 128 then drops disabled api:memory from defaults,
	// and migration 132 stores api:release-notes as its canonical reference.
	if want := []string{canonical("api:release-notes")}; !slices.Equal(cat.Defaults.Repos["acme/widgets"].Task, want) {
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
		if !slices.Equal(got.CICapabilities, []string{canonical("api:release-notes")}) {
			t.Errorf("repo_config.ci_capabilities = %v, want [%s]", got.CICapabilities, canonical("api:release-notes"))
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
		if !slices.Equal(got.CICapabilities, []string{canonical("api:release-notes")}) {
			t.Errorf("repo_config.ci_capabilities after state-repo import = %v, want [%s]", got.CICapabilities, canonical("api:release-notes"))
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
	cfg.DefaultCapabilities = []string{"release-gate-read", "gcp-key", "gcp-key:deploy"}
	cfg.ConsoleSessions = []model.ConsoleSession{
		{ID: "director", Title: "Director", Capabilities: []string{"gcp-key", "gcp-key:staging", "release-gate-read"}},
	}
	cfg.CapabilityPolicy = model.CapabilityPolicy{
		Rules: []model.CapabilityPolicyRule{
			{Capability: "gcp-key:deploy", Repos: []string{"acme/widgets"}, AllowUntrusted: true},
			{Capability: "gcp-key", Repos: []string{"acme/public"}, AllowUntrusted: true},
		},
		Catalogue: &model.CapabilityCatalogue{
			Defaults: model.CatalogueDefaults{
				Task: []string{"release-gate-read", "gcp-key", "gcp-key:deploy"},
				CI:   []string{"gcp-key:staging"},
				Repos: map[string]model.CatalogueRepoDefaults{
					"acme/widgets": {
						Task: []string{"gcp-key", "gcp-key:deploy", "release-gate-read"},
						CI:   []string{"release-gate-read", "gcp-key", "gcp-key:staging"},
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
		DefaultCapabilities: []string{"gcp-key", "gcp-key:deploy", "release-gate-read"},
		CICapabilities:      []string{"release-gate-read", "gcp-key", "gcp-key:staging"},
	}); err != nil {
		t.Fatal(err)
	}

	for _, row := range []struct{ table, owner, id, capability string }{
		{"task_grant", "task_id", "t1", "gcp-key"},
		{"task_grant", "task_id", "t1", "gcp-key:deploy"},
		{"task_grant", "task_id", "t1", "release-gate-read"},
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

		if want := []string{"release-gate-read", sandboxGrant, deployGrant}; !slices.Equal(gotCfg.DefaultCapabilities, want) {
			t.Errorf("DefaultCapabilities = %v, want %v", gotCfg.DefaultCapabilities, want)
		}
		if want := []string{sandboxGrant, stagingGrant, "release-gate-read"}; !slices.Equal(gotCfg.ConsoleSessions[0].Capabilities, want) {
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
		if want := []string{"release-gate-read", sandboxGrant, deployGrant}; !slices.Equal(gotCfg.CapabilityPolicy.Catalogue.Defaults.Task, want) {
			t.Errorf("Catalogue.Defaults.Task = %v, want %v", gotCfg.CapabilityPolicy.Catalogue.Defaults.Task, want)
		}
		if want := []string{stagingGrant}; !slices.Equal(gotCfg.CapabilityPolicy.Catalogue.Defaults.CI, want) {
			t.Errorf("Catalogue.Defaults.CI = %v, want %v", gotCfg.CapabilityPolicy.Catalogue.Defaults.CI, want)
		}
		if want := []string{sandboxGrant, deployGrant, "release-gate-read"}; !slices.Equal(gotCfg.CapabilityPolicy.Catalogue.Defaults.Repos["acme/widgets"].Task, want) {
			t.Errorf("Catalogue.Defaults.Repos[acme/widgets].Task = %v, want %v", gotCfg.CapabilityPolicy.Catalogue.Defaults.Repos["acme/widgets"].Task, want)
		}
		if want := []string{"release-gate-read", sandboxGrant, stagingGrant}; !slices.Equal(gotCfg.CapabilityPolicy.Catalogue.Defaults.Repos["acme/widgets"].CI, want) {
			t.Errorf("Catalogue.Defaults.Repos[acme/widgets].CI = %v, want %v", gotCfg.CapabilityPolicy.Catalogue.Defaults.Repos["acme/widgets"].CI, want)
		}

		gotRepo, err := s.GetRepoConfig(ctx, widgets)
		if err != nil || gotRepo == nil {
			t.Fatalf("GetRepoConfig: %v, %+v", err, gotRepo)
		}
		if want := []string{sandboxGrant, deployGrant, "release-gate-read"}; !slices.Equal(gotRepo.DefaultCapabilities, want) {
			t.Errorf("repo DefaultCapabilities = %v, want %v", gotRepo.DefaultCapabilities, want)
		}
		if want := []string{"release-gate-read", sandboxGrant, stagingGrant}; !slices.Equal(gotRepo.CICapabilities, want) {
			t.Errorf("repo CICapabilities = %v, want %v", gotRepo.CICapabilities, want)
		}

		wantTasks := map[string][]string{"t1": {deployGrant, sandboxGrant, "release-gate-read"}}
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

func TestMigration121RewritesReadIssuesPolicy(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)

	cfg := testConfig()
	cfg.DefaultCapabilities = []string{"release-gate-read", "read-issues"}
	cfg.CapabilityPolicy = model.CapabilityPolicy{
		Rules: []model.CapabilityPolicyRule{
			{Capability: "read-issues", Repos: []string{"acme/widgets"}, AllowUntrusted: true},
		},
		Catalogue: &model.CapabilityCatalogue{
			Defaults: model.CatalogueDefaults{
				Task: []string{"release-gate-read", "read-issues"},
				CI:   []string{"read-issues"},
				Repos: map[string]model.CatalogueRepoDefaults{
					"acme/widgets": {
						Task: []string{"read-issues", "release-gate-read"},
						CI:   []string{"release-gate-read", "read-issues"},
					},
				},
			},
			InProcess: map[string]model.CatalogueInProcess{
				"read-issues": {Restrictions: &model.CapabilityRestrictions{
					Repos: model.Patterns{"acme/*"},
				}},
			},
		},
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                widgets,
		DefaultCapabilities: []string{"read-issues", "release-gate-read"},
		CICapabilities:      []string{"release-gate-read", "read-issues"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 120 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	exportDir := t.TempDir()
	if err := staterepo.Export(ctx, db, exportDir); err != nil {
		t.Fatalf("staterepo.Export at schema 120: %v", err)
	}
	if err := staterepo.WriteSchemaVersion(exportDir, 120); err != nil {
		t.Fatal(err)
	}

	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating 120 -> 120: %v", err)
	}

	assertPolicy := func(t *testing.T, s *model.Store) {
		t.Helper()
		got, err := s.GetConfig(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.CapabilityPolicy.Rules) != 1 || got.CapabilityPolicy.Rules[0].Capability != model.GitHubReadIssuesRef || !got.CapabilityPolicy.Rules[0].AllowUntrusted {
			t.Errorf("CapabilityPolicy.Rules = %+v, want %s AllowUntrusted=true", got.CapabilityPolicy.Rules, model.GitHubReadIssuesRef)
		}
		cat := got.CapabilityPolicy.Catalogue
		if cat == nil {
			t.Fatal("CapabilityPolicy.Catalogue nil")
		}
		if want := []string{"release-gate-read", model.GitHubReadIssuesGrant}; !slices.Equal(cat.Defaults.Task, want) {
			t.Errorf("Catalogue.Defaults.Task = %v, want %v", cat.Defaults.Task, want)
		}
		if want := []string{model.GitHubReadIssuesGrant}; !slices.Equal(cat.Defaults.CI, want) {
			t.Errorf("Catalogue.Defaults.CI = %v, want %v", cat.Defaults.CI, want)
		}
		if want := []string{model.GitHubReadIssuesGrant, "release-gate-read"}; !slices.Equal(cat.Defaults.Repos["acme/widgets"].Task, want) {
			t.Errorf("Catalogue.Defaults.Repos[acme/widgets].Task = %v, want %v", cat.Defaults.Repos["acme/widgets"].Task, want)
		}
		if want := []string{"release-gate-read", model.GitHubReadIssuesGrant}; !slices.Equal(cat.Defaults.Repos["acme/widgets"].CI, want) {
			t.Errorf("Catalogue.Defaults.Repos[acme/widgets].CI = %v, want %v", cat.Defaults.Repos["acme/widgets"].CI, want)
		}
		// Legacy in_process.read-issues restriction continues to bind the migrated grant.
		if _, err := cat.ValidateGrants(model.GrantContext{Kind: "task", Repo: &model.RepoRef{Owner: "other", Name: "repo"}, Grants: []string{model.GitHubReadIssuesGrant}}); err == nil {
			t.Error("in_process.read-issues failed to bind migrated github#read-issues grant on disallowed repo")
		}
		if _, err := cat.ValidateGrants(model.GrantContext{Kind: "task", Repo: &model.RepoRef{Owner: "acme", Name: "widgets"}, Grants: []string{model.GitHubReadIssuesGrant}}); err != nil {
			t.Errorf("ValidateGrants on allowed repo failed: %v", err)
		}
	}

	assertPolicy(t, store)

	// Re-running migration 121 is idempotent.
	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 120 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("re-running migration 121: %v", err)
	}
	assertPolicy(t, store)

	importedStore, importedDB, _ := openStore(t)
	if err := staterepo.Import(ctx, importedDB, exportDir); err != nil {
		t.Fatalf("staterepo.Import of schema-120 export: %v", err)
	}
	assertPolicy(t, importedStore)
}

func TestMigrate120GitHubCredentialToRepoServer(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)

	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg := model.Config{
		DefaultCapabilities: []string{"github-credential:release-bot", "release-gate-read"},
		ConsoleSessions: []model.ConsoleSession{
			{
				ID:           "director",
				Title:        "Director",
				Capabilities: []string{"github-credential:ops-bot", "release-gate-read"},
			},
		},
		CapabilityPolicy: model.CapabilityPolicy{
			Rules: []model.CapabilityPolicyRule{
				{Capability: "github-credential:release-bot", Deny: false, AllowUntrusted: true},
				{Capability: "github-credential:*", Deny: true, AllowUntrusted: true},
			},
			Catalogue: &model.CapabilityCatalogue{
				Defaults: model.CatalogueDefaults{
					Task: []string{"github-credential:release-bot"},
					CI:   []string{"github-credential:ci-bot"},
					Repos: map[string]model.CatalogueRepoDefaults{
						"acme/widgets": {
							Task: []string{"github-credential:widgets-bot"},
							CI:   []string{"github-credential:ci-bot"},
						},
					},
				},
			},
		},
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                widgets,
		DefaultCapabilities: []string{"github-credential:widgets-bot", "release-gate-read"},
		CICapabilities:      []string{"github-credential:ci-bot"},
	}); err != nil {
		t.Fatal(err)
	}

	for _, ins := range []struct{ table, col, id, cap string }{
		{"task_grant", "task_id", "t1", "github-credential:release-bot"},
		{"task_grant", "task_id", "t1", "release-gate-read"},
		{"template_grant", "template_id", "rel-tmpl", "github-credential:release-bot"},
		{"schedule_grant", "schedule_id", "rel-sched", "github-credential:ops-bot"},
	} {
		if _, err := db.ExecContext(ctx, "INSERT INTO `"+ins.table+"` (`"+ins.col+"`, `capability`, `via`) VALUES (?, ?, 'label')", ins.id, ins.cap); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 119 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating 119 -> 120: %v", err)
	}

	releaseGrant := model.RepoCredentialGrant("release-bot")
	opsGrant := model.RepoCredentialGrant("ops-bot")
	widgetsGrant := model.RepoCredentialGrant("widgets-bot")
	ciGrant := model.RepoCredentialGrant("ci-bot")

	gotCfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{releaseGrant, "release-gate-read"}; !slices.Equal(gotCfg.DefaultCapabilities, want) {
		t.Errorf("DefaultCapabilities = %v, want %v", gotCfg.DefaultCapabilities, want)
	}
	if want := []string{opsGrant, "release-gate-read"}; !slices.Equal(gotCfg.ConsoleSessions[0].Capabilities, want) {
		t.Errorf("ConsoleSessions[0].Capabilities = %v, want %v", gotCfg.ConsoleSessions[0].Capabilities, want)
	}
	if len(gotCfg.CapabilityPolicy.Rules) != 2 ||
		gotCfg.CapabilityPolicy.Rules[0].Capability != model.RepoCredentialRef ||
		gotCfg.CapabilityPolicy.Rules[0].AllowUntrusted ||
		gotCfg.CapabilityPolicy.Rules[1].Capability != model.RepoCredentialRef ||
		!gotCfg.CapabilityPolicy.Rules[1].Deny {
		t.Errorf("CapabilityPolicy.Rules = %+v", gotCfg.CapabilityPolicy.Rules)
	}
	if want := []string{releaseGrant}; !slices.Equal(gotCfg.CapabilityPolicy.Catalogue.Defaults.Task, want) {
		t.Errorf("Catalogue.Defaults.Task = %v, want %v", gotCfg.CapabilityPolicy.Catalogue.Defaults.Task, want)
	}
	if want := []string{ciGrant}; !slices.Equal(gotCfg.CapabilityPolicy.Catalogue.Defaults.CI, want) {
		t.Errorf("Catalogue.Defaults.CI = %v, want %v", gotCfg.CapabilityPolicy.Catalogue.Defaults.CI, want)
	}
	if want := []string{widgetsGrant}; !slices.Equal(gotCfg.CapabilityPolicy.Catalogue.Defaults.Repos["acme/widgets"].Task, want) {
		t.Errorf("Catalogue.Defaults.Repos.Task = %v, want %v", gotCfg.CapabilityPolicy.Catalogue.Defaults.Repos["acme/widgets"].Task, want)
	}

	gotRepo, err := store.GetRepoConfig(ctx, widgets)
	if err != nil || gotRepo == nil {
		t.Fatalf("GetRepoConfig: %v, %+v", err, gotRepo)
	}
	if want := []string{widgetsGrant, "release-gate-read"}; !slices.Equal(gotRepo.DefaultCapabilities, want) {
		t.Errorf("repo DefaultCapabilities = %v, want %v", gotRepo.DefaultCapabilities, want)
	}
	if want := []string{ciGrant}; !slices.Equal(gotRepo.CICapabilities, want) {
		t.Errorf("repo CICapabilities = %v, want %v", gotRepo.CICapabilities, want)
	}

	wantTask := []string{releaseGrant, "release-gate-read"}
	sort.Strings(wantTask)
	if got := grantRows(t, ctx, db, "task_grant", "task_id")["t1"]; !slices.Equal(got, wantTask) {
		t.Errorf("task_grant = %v, want %v", got, wantTask)
	}
	if got := grantRows(t, ctx, db, "template_grant", "template_id")["rel-tmpl"]; !slices.Equal(got, []string{releaseGrant}) {
		t.Errorf("template_grant = %v, want [%s]", got, releaseGrant)
	}
	if got := grantRows(t, ctx, db, "schedule_grant", "schedule_id")["rel-sched"]; !slices.Equal(got, []string{opsGrant}) {
		t.Errorf("schedule_grant = %v, want [%s]", got, opsGrant)
	}
}

func TestMigration124RewritesGitHubSandboxPolicy(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)

	cfg := testConfig()
	cfg.DefaultCapabilities = []string{"api:tasks:read", "github-sandbox"}
	cfg.CapabilityPolicy = model.CapabilityPolicy{
		Rules: []model.CapabilityPolicyRule{
			{Capability: "github-sandbox", Repos: []string{"acme/widgets"}, AllowUntrusted: true},
		},
		Catalogue: &model.CapabilityCatalogue{
			Defaults: model.CatalogueDefaults{
				Task: []string{"api:tasks:read", "github-sandbox"},
				CI:   []string{"github-sandbox"},
				Repos: map[string]model.CatalogueRepoDefaults{
					"acme/widgets": {
						Task: []string{"github-sandbox", "api:tasks:read"},
						CI:   []string{"release-gate-read", "github-sandbox"},
					},
				},
			},
			InProcess: map[string]model.CatalogueInProcess{
				"github-sandbox": {Restrictions: &model.CapabilityRestrictions{
					Repos: model.Patterns{"acme/*"},
				}},
			},
		},
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                widgets,
		DefaultCapabilities: []string{"github-sandbox", "api:tasks:read"},
		CICapabilities:      []string{"release-gate-read", "github-sandbox"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 123 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	exportDir := t.TempDir()
	if err := staterepo.Export(ctx, db, exportDir); err != nil {
		t.Fatalf("staterepo.Export at schema 123: %v", err)
	}
	if err := staterepo.WriteSchemaVersion(exportDir, 123); err != nil {
		t.Fatal(err)
	}

	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating 123 -> 124: %v", err)
	}

	assertPolicy := func(t *testing.T, s *model.Store) {
		t.Helper()
		got, err := s.GetConfig(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.CapabilityPolicy.Rules) != 1 || got.CapabilityPolicy.Rules[0].Capability != model.GitHubSandboxRepoRef || !got.CapabilityPolicy.Rules[0].AllowUntrusted {
			t.Errorf("CapabilityPolicy.Rules = %+v, want %s AllowUntrusted=true", got.CapabilityPolicy.Rules, model.GitHubSandboxRepoRef)
		}
		cat := got.CapabilityPolicy.Catalogue
		if cat == nil {
			t.Fatal("CapabilityPolicy.Catalogue nil")
		}
		if want := []string{canonical("api:tasks:read"), model.GitHubSandboxRepoGrant}; !slices.Equal(cat.Defaults.Task, want) {
			t.Errorf("Catalogue.Defaults.Task = %v, want %v", cat.Defaults.Task, want)
		}
		if want := []string{model.GitHubSandboxRepoGrant}; !slices.Equal(cat.Defaults.CI, want) {
			t.Errorf("Catalogue.Defaults.CI = %v, want %v", cat.Defaults.CI, want)
		}
		if want := []string{model.GitHubSandboxRepoGrant, canonical("api:tasks:read")}; !slices.Equal(cat.Defaults.Repos["acme/widgets"].Task, want) {
			t.Errorf("Catalogue.Defaults.Repos[acme/widgets].Task = %v, want %v", cat.Defaults.Repos["acme/widgets"].Task, want)
		}
		if want := []string{"release-gate-read", model.GitHubSandboxRepoGrant}; !slices.Equal(cat.Defaults.Repos["acme/widgets"].CI, want) {
			t.Errorf("Catalogue.Defaults.Repos[acme/widgets].CI = %v, want %v", cat.Defaults.Repos["acme/widgets"].CI, want)
		}
		// Legacy in_process.github-sandbox restriction continues to bind the migrated grant.
		if _, err := cat.ValidateGrants(model.GrantContext{Kind: "task", Repo: &model.RepoRef{Owner: "other", Name: "repo"}, Grants: []string{model.GitHubSandboxRepoGrant}}); err == nil {
			t.Error("in_process.github-sandbox failed to bind migrated github#sandbox-repo grant on disallowed repo")
		}
		if _, err := cat.ValidateGrants(model.GrantContext{Kind: "task", Repo: &model.RepoRef{Owner: "acme", Name: "widgets"}, Grants: []string{model.GitHubSandboxRepoGrant}}); err != nil {
			t.Errorf("ValidateGrants on allowed repo failed: %v", err)
		}
	}

	assertPolicy(t, store)

	// Re-running migration 124 is idempotent.
	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 123 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("re-running migration 124: %v", err)
	}
	assertPolicy(t, store)

	importedStore, importedDB, _ := openStore(t)
	if err := staterepo.Import(ctx, importedDB, exportDir); err != nil {
		t.Fatalf("staterepo.Import of schema-123 export: %v", err)
	}
	assertPolicy(t, importedStore)
}

// A store from before the first conversion (schema 66), holding every
// retired grant form at once -- in tasks, templates, schedules, the
// deployment's default capabilities and standing sessions, and a
// repository's default and CI capabilities -- upgrades to the current
// schema in one pass with every one of them in its new form, meaning
// what it meant, and nothing retired left over. Task 2876 removed the
// in-process provider machinery those names were served by; the
// migrations that rewrite them are what keeps such a store upgradeable.
func TestAPreConversionStoreUpgradesEveryRetiredGrantAtOnce(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	first := retiredGrants[0].version
	var legacy, replacements []string
	for _, r := range retiredGrants {
		first = min(first, r.version)
		legacy = append(legacy, r.legacy)
		for _, to := range r.to {
			if !slices.Contains(replacements, to) {
				replacements = append(replacements, to)
			}
		}
	}
	sortedReplacements := slices.Clone(replacements)
	sort.Strings(sortedReplacements)

	cfg := testConfig()
	cfg.DefaultCapabilities = slices.Clone(legacy)
	cfg.ConsoleSessions = []model.ConsoleSession{{ID: "director", Title: "Director", Capabilities: slices.Clone(legacy)}}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: widgets, DefaultCapabilities: slices.Clone(legacy),
		CICapabilities: slices.Clone(legacy)}); err != nil {
		t.Fatal(err)
	}
	owners := map[string]string{"task_grant": "task_id", "template_grant": "template_id", "schedule_grant": "schedule_id"}
	for table, owner := range owners {
		for _, name := range legacy {
			if _, err := db.ExecContext(ctx, "INSERT INTO `"+table+"` (`"+owner+"`, `capability`, `via`) VALUES ('old', ?, 'label')", name); err != nil {
				t.Fatal(err)
			}
		}
	}

	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = ? WHERE `id` = 1", first-1); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating a version-%d store: %v", first-1, err)
	}

	noneRetired := func(what string, got []string) {
		t.Helper()
		for _, name := range legacy {
			if slices.Contains(got, name) {
				t.Errorf("%s still holds retired %q after the upgrade: %v", what, name, got)
			}
		}
	}
	for table, owner := range owners {
		got := grantRows(t, ctx, db, table, owner)["old"]
		sameGrantMeaning(t, table, legacy, got)
		noneRetired(table, got)
		if !slices.Equal(got, sortedReplacements) {
			t.Errorf("%s holds %v after the upgrade, want every new form once: %v", table, got, sortedReplacements)
		}
	}
	after, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for what, got := range map[string][]string{
		"default capabilities": after.DefaultCapabilities,
		"director session":     after.ConsoleSessions[0].Capabilities,
	} {
		sameGrantMeaning(t, what, legacy, got)
		noneRetired(what, got)
	}
	repo, err := store.GetRepoConfig(ctx, widgets)
	if err != nil {
		t.Fatal(err)
	}
	for what, got := range map[string][]string{
		"repo default capabilities": repo.DefaultCapabilities,
		"repo CI capabilities":      repo.CICapabilities,
	} {
		sameGrantMeaning(t, what, legacy, got)
		noneRetired(what, got)
	}
}

func TestMigrationRewritesRetiredGrantWaits(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		from int
	}{
		{"from_123_rewriteLegacyGrantsMulti", 123},
		{"from_128_migrateRetiredGrantWaits", 128},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, db, ctx := openStore(t)
			now := time.Now().UTC().Format(time.RFC3339Nano)
			if _, err := db.ExecContext(ctx,
				"INSERT INTO `task_grant_wait` (`task_id`, `capability`, `resource`, `waiting_at`) VALUES ('w1', 'github-sandbox', 'GitHub App rate limit', ?), ('w2', 'console', 'slot', ?)",
				now, now); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = ? WHERE `id` = 1", tc.from); err != nil {
				t.Fatal(err)
			}
			if err := model.New(db).Init(ctx); err != nil {
				t.Fatalf("Init from %d: %v", tc.from, err)
			}
			var gotCap string
			if err := db.QueryRowContext(ctx, "SELECT `capability` FROM `task_grant_wait` WHERE `task_id` = 'w1'").Scan(&gotCap); err != nil {
				t.Fatalf("reading w1 grant wait: %v", err)
			}
			if gotCap != model.GitHubSandboxRepoGrant {
				t.Errorf("w1 capability = %q, want %q", gotCap, model.GitHubSandboxRepoGrant)
			}
			var count int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `task_grant_wait` WHERE `task_id` = 'w2'").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if tc.from == 128 && count != 0 {
				t.Errorf("w2 console wait survived migration 129: count = %d", count)
			}
		})
	}
}
