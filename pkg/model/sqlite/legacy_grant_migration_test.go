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
	"slices"
	"sort"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
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
		if model.CanonicalLegacyGrant(name) == name && !slices.Contains(after, name) {
			t.Errorf("%s: %q, which is not retired, was lost (%v -> %v)", what, name, before, after)
		}
		if to := model.CanonicalLegacyGrant(name); to != name && slices.Contains(after, name) {
			t.Errorf("%s: retired %q survived the migration as itself rather than as %q", what, name, to)
		}
	}
}

// retiredGrants are the in-process capabilities a store migration
// rewrote, each with the schema version the migration produces and the
// api grant it wrote.
var retiredGrants = []struct {
	version int
	legacy  string
	api     string
}{
	{67, "team-broadcast", "api:team-broadcast"},
	{68, "dispatch-control", "api:dispatch-control"},
	{69, "pipeline-control", "api:pipeline-control"},
	{71, "memory", "api:memory"},
	{72, "release-notes", "api:release-notes"},
}

// Each retiring migration rewrites every stored grant of its capability
// -- the three grant tables, the deployment's and a repository's default
// capabilities, and the standing sessions' -- to the api grant it
// became, and each row grants exactly what it did before.
func TestMigrationRewritesRetiredGrants(t *testing.T) {
	t.Parallel()
	for _, r := range retiredGrants {
		t.Run(r.legacy, func(t *testing.T) {
			t.Parallel()
			testMigrationRewrites(t, r.version, r.legacy, r.api)
		})
	}
}

func testMigrationRewrites(t *testing.T, version int, legacy, api string) {
	store, db, ctx := openStore(t)

	cfg := testConfig()
	cfg.DefaultCapabilities = []string{legacy, "gcp-key"}
	cfg.ConsoleSessions = []model.ConsoleSession{
		{ID: "director", Title: "Director", Capabilities: []string{"self-debug", legacy}},
		{ID: "steward", Title: "Steward", Capabilities: []string{api, legacy}},
		{ID: "quiet", Title: "Quiet"},
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: widgets, DefaultCapabilities: []string{"self-debug", legacy},
		CICapabilities: []string{legacy}}); err != nil {
		t.Fatal(err)
	}
	fixture := map[string]map[string][]string{
		"task_grant": {
			"1": {legacy, "self-debug"},
			"2": {legacy, api},
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
	if got := grantRows(t, ctx, db, "task_grant", "task_id")["2"]; !slices.Equal(got, []string{api}) {
		t.Errorf("task 2, holding both spellings, holds %v after the migration, want the api grant once", got)
	}

	after, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{api, "gcp-key"}; !slices.Equal(after.DefaultCapabilities, want) {
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
	if got, want := after.ConsoleSessions[1].Capabilities, []string{api}; !slices.Equal(got, want) {
		t.Errorf("steward capabilities = %v, want %v", got, want)
	}

	repo, err := store.GetRepoConfig(ctx, widgets)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"self-debug", api}; !slices.Equal(repo.DefaultCapabilities, want) {
		t.Errorf("repo default capabilities = %v, want %v", repo.DefaultCapabilities, want)
	}
	// A CI job's api grant is a CIAPI token, which none of these ever
	// was, so ci_capabilities is not a list the migration speaks for.
	if want := []string{legacy}; !slices.Equal(repo.CICapabilities, want) {
		t.Errorf("repo CI capabilities = %v, want %v left alone", repo.CICapabilities, want)
	}
}
