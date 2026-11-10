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
	"sort"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// TestSchema78StoreMigratesEveryRetiredGrantAtOnce is the upgrade
// docs/test-runs/2026-11-02-grain-capability-server-upgrade.md walked: a
// schema-78 store (the last before the in-process grain server) holding
// grain-log, deployment-health, db-read, db-write, self-repair and
// git-push-any-ref together on one task, template, schedule, standing
// session and capability list, opened once by a build that runs 79-86 in
// a row. testMigrationRewrites starts each migration from the version
// before it with one name; this one checks the chain: two #sql grants
// side by side, deployment-health folding into an api:deployment:read
// already held, and a CSV list column that 85's repo#write grant (which
// has commas in it) turns into a JSON array before 86 reads it again.
func TestSchema78StoreMigratesEveryRetiredGrantAtOnce(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	retired := []string{"grain-log", "deployment-health", "db-read", "db-write", "self-repair", "git-push-any-ref"}
	became := []string{
		model.GrainJournalGrant,
		canonical("api:deployment:read"), model.GrainHealthGrant,
		model.GrainSQLReadGrant, model.GrainSQLWriteGrant,
		model.GrainHostExecGrant,
		model.GitPushAnyRefGrant,
	}

	cfg := schema78Config()
	cfg.DefaultCapabilities = []string{"deployment-health", "self-repair"}
	cfg.ConsoleSessions = []model.ConsoleSession{{ID: "director", Title: "Director", Capabilities: slices.Clone(retired)}}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: widgets,
		DefaultCapabilities: []string{"grain-log", "db-read", "git-push-any-ref"},
		CICapabilities:      slices.Clone(retired)}); err != nil {
		t.Fatal(err)
	}
	fixture := map[string]map[string][]string{
		"task_grant": {
			"all": retired,
			"dup": {"deployment-health", "api:deployment:read"},
		},
		"template_grant": {"t": retired},
		"schedule_grant": {"s": retired},
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
	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 78 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating a schema-78 store: %v", err)
	}

	wantAll := slices.Clone(became)
	sort.Strings(wantAll)
	for table, byOwner := range fixture {
		got := grantRows(t, ctx, db, table, owners[table])
		for id, before := range byOwner {
			sameGrantMeaning(t, table+" "+id, before, got[id])
		}
		if id := map[string]string{"task_grant": "all", "template_grant": "t", "schedule_grant": "s"}[table]; !slices.Equal(got[id], wantAll) {
			t.Errorf("%s %s = %v, want %v", table, id, got[id], wantAll)
		}
	}
	if got, want := grantRows(t, ctx, db, "task_grant", "task_id")["dup"], []string{canonical("api:deployment:read"), model.GrainHealthGrant}; !slices.Equal(got, want) {
		t.Errorf("task holding deployment-health and api:deployment:read = %v, want %v once each", got, want)
	}

	after, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{canonical("api:deployment:read"), model.GrainHealthGrant, model.GrainHostExecGrant}; !slices.Equal(after.DefaultCapabilities, want) {
		t.Errorf("deployment default capabilities = %v, want %v", after.DefaultCapabilities, want)
	}
	if len(after.ConsoleSessions) != 1 || !slices.Equal(after.ConsoleSessions[0].Capabilities, became) {
		t.Errorf("director capabilities = %+v, want %v", after.ConsoleSessions, became)
	}
	repo, err := store.GetRepoConfig(ctx, widgets)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{model.GrainJournalGrant, model.GrainSQLReadGrant, model.GitPushAnyRefGrant}; !slices.Equal(repo.DefaultCapabilities, want) {
		t.Errorf("repo default capabilities = %v, want %v", repo.DefaultCapabilities, want)
	}
	if !slices.Equal(repo.CICapabilities, became) {
		t.Errorf("repo CI capabilities = %v, want %v", repo.CICapabilities, became)
	}
}

func schema78Config() model.Config { return testConfig() }
