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
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// slice4Fixture is the fidelity fixture of design/design/unified-grants.design.md
// §3, slice 4: each task's grants in every spelling a store at schema 131
// can hold, legacy api:, non-canonical api references, repo-read: and
// repo-write: included, beside grants the migration must leave alone.
var slice4Fixture = map[string][]string{
	"none":       nil,
	"ci":         {"api:ci:read", "api:ci:write"},
	"ci-limited": {"api:ci:write@acme/widgets"},
	"ci-ref":     {model.APIRef("ci:read") + "={}", model.APIRef("ci:write") + `={"repo":"acme/widgets"}`},
	"admin":      {"api:task-admin", "api:merge:force", model.GrainSourceGrant},
	"dangerous":  {"api:secrets:write", model.GrainSQLWriteGrant, model.GrainHostExecGrant},
	"mixed":      {"api:memory", model.GCPKeyServerGrant("ci"), model.GrainJournalGrant, model.GitHubSandboxRepoGrant, "api:ci-read@acme/widgets"},
	"both":       {"api:tasks:read", model.APIRef("tasks:read")},
	"config":     {"api:settings:write@acme/widgets", "api:deployment-ops"},
	"push-any":   {model.GitPushAnyRefGrant, model.RepoCredentialGrant("ops")},
	"repo":       {"repo-read:acme/docs", "repo-write:acme/site", "release-gate-read"},
	"self":       {"api:self-upgrade", "api:pipeline-control", "api:team-broadcast"},
}

// slice4Predicates is every approval and policy answer for one (proposer,
// child) pair of tasks holding child and parent, as a printable value: the
// rules §2 says carry over unchanged.
func slice4Predicates(child, parent []string) string {
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := func(id string, caps []string) model.Task {
		t := model.Task{ID: id, Target: &widgets}
		// Two spellings of one grant are one grant (they share a
		// CanonicalGrantKey), which is why the migration keeps one row of
		// "both"; a task here holds each once, as the store's tasks do.
		seen := map[string]bool{}
		for _, c := range caps {
			if k := model.CanonicalGrantKey(c); seen[k] {
				continue
			} else {
				seen[k] = true
			}
			t.Grants = append(t.Grants, model.Grant{Capability: c, Via: model.GrantByLabel})
		}
		return t
	}
	c, p := task("child", child), task("parent", parent)
	policy := model.CapabilityPolicy{Rules: []model.CapabilityPolicyRule{
		{Capability: model.APIRef("merge:force"), Deny: true},
		{Capability: model.APIRef("ci:*"), Repos: []string{"acme/widgets"}},
	}}
	var b strings.Builder
	fmt.Fprintf(&b, "subset=%v ", model.GrantsSubsetOf(c.Grants, p.Grants))
	fmt.Fprintf(&b, "escalated=%d ", len(model.EscalatedCapabilities(c, p)))
	fmt.Fprintf(&b, "lands=%v ", model.LandsWhereGranted(c, p))
	kept, dropped := model.ReviewGrants(c.Grants, p)
	fmt.Fprintf(&b, "reviewKept=%d reviewDropped=%d ", len(kept), len(dropped))
	fmt.Fprintf(&b, "tmplWithin=%v ", model.TemplateWithinTask(p, model.Template{ID: "tmpl", Grants: c.Grants}) == nil)
	dangerous, personOnly := 0, 0
	for _, g := range c.Grants {
		if model.IsDangerousGrant(g.Capability) {
			dangerous++
		}
		if model.IsPersonOnlyGrant(g.Capability) {
			personOnly++
		}
		for _, repo := range []model.RepoRef{widgets, {Owner: "acme", Name: "site"}} {
			if err := policy.Permits(g.Capability, model.CapabilitySubject{Kind: model.CapabilityRunTask, Repo: &repo}); err != nil {
				fmt.Fprintf(&b, "refused(%s) ", repo)
			}
		}
	}
	fmt.Fprintf(&b, "dangerous=%d personOnly=%d ", dangerous, personOnly)
	set := model.APIGrantsOf(child)
	for _, sc := range set.Scopes() {
		fmt.Fprintf(&b, "%s:%v ", sc, set.Repos(sc))
	}
	return b.String()
}

// Migration 132 is slice 4's "migrate once": every stored grant becomes
// its canonical reference, reading each one back gives the string the
// store held before byte for byte (old -> CanonicalGrant -> old), every
// approval predicate answers each (proposer, child) pair of the fixture
// the same before and after, and running it again changes nothing.
func TestMigration132StoresCanonicalReferencesWithFidelity(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)

	cfg := testConfig()
	cfg.DefaultCapabilities = []string{"api:tasks:read", "release-gate-read", model.APIRef("ci:read") + "={}"}
	cfg.ConsoleSessions = []model.ConsoleSession{
		{ID: "director", Title: "Director", Capabilities: []string{"api:task-admin", "api:memory", model.APIRef("memory")},
			DeclinedDefaults: []string{"api:tasks:read"}},
		{ID: "quiet", Title: "Quiet", Capabilities: []string{model.GrainSourceGrant}},
	}
	cfg.CapabilityPolicy = model.CapabilityPolicy{
		Catalogue: &model.CapabilityCatalogue{
			Defaults: model.CatalogueDefaults{
				Task: []string{"api:tasks:read", "release-gate-read"},
				CI:   []string{"api:ci:read", "repo-read:acme/docs", model.APIRef("ci:read")},
				Repos: map[string]model.CatalogueRepoDefaults{
					"acme/widgets": {
						Task: []string{"api:ci-read@acme/widgets"},
						CI:   []string{"release-gate-read", "repo-write:acme/widgets"},
					},
				},
			},
		},
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: widgets,
		DefaultCapabilities: []string{"api:ci-read@acme/widgets"},
		CICapabilities:      []string{"release-gate-read", "repo-write:acme/widgets", "api:ci:read"}}); err != nil {
		t.Fatal(err)
	}
	owners := map[string]string{"task_grant": "task_id", "template_grant": "template_id", "schedule_grant": "schedule_id"}
	for table, owner := range owners {
		for id, names := range slice4Fixture {
			for _, name := range names {
				if _, err := db.ExecContext(ctx, "INSERT INTO `"+table+"` (`"+owner+"`, `capability`, `via`) VALUES (?, ?, 'label')", id, name); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO `task_grant_wait` (`task_id`, `capability`, `resource`, `waiting_at`) VALUES ('ci', 'api:ci:read', '', ?)", now); err != nil {
		t.Fatal(err)
	}
	before := grantRows(t, ctx, db, "task_grant", "task_id")

	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 131 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating a version-131 store: %v", err)
	}

	for table, owner := range owners {
		after := grantRows(t, ctx, db, table, owner)
		for id, names := range slice4Fixture {
			want := canonicals(names)
			sort.Strings(want)
			if !slices.Equal(after[id], want) {
				t.Errorf("%s %s = %v, want %v", table, id, after[id], want)
			}
			for _, name := range after[id] {
				if strings.HasPrefix(name, model.APIGrantPrefix) || strings.HasPrefix(name, model.RepoReadCapabilityPrefix) ||
					strings.HasPrefix(name, model.RepoWriteCapabilityPrefix) {
					t.Errorf("%s %s still holds the legacy spelling %q", table, id, name)
				}
			}
			sameGrantMeaning(t, table+" "+id, names, after[id])
		}
	}

	// old -> CanonicalGrant -> old is byte-identical for every fixture row
	// the migration rewrote from api:<what>[@repo] (repo-read:/repo-write:
	// read back as the reference migration 88 already stores).
	for _, names := range slice4Fixture {
		for _, old := range names {
			if !strings.HasPrefix(old, model.APIGrantPrefix) {
				continue
			}
			stored, _ := model.CanonicalStoredGrant(old)
			if back, ok := model.LegacyGrantString(model.CanonicalGrant(stored)); !ok || back != old {
				t.Errorf("LegacyGrantString(CanonicalGrant(%q)) = %q, %v; want %q back", stored, back, ok, old)
			}
		}
	}

	// The approval predicates (§2) answer every (proposer, child) pair the
	// same on the store's rows before and after.
	after := grantRows(t, ctx, db, "task_grant", "task_id")
	var answers strings.Builder
	for parent := range slice4Fixture {
		for child := range slice4Fixture {
			want := slice4Predicates(before[child], before[parent])
			answers.WriteString(want + "\n")
			if got := slice4Predicates(after[child], after[parent]); got != want {
				t.Errorf("(%s proposes %s) after migration 132:\n got %s\nwant %s", parent, child, got, want)
			}
		}
	}
	for _, answer := range []string{"subset=true", "subset=false", "lands=true", "dangerous=0", "dangerous=1", "refused(acme/site)", "tmplWithin=false"} {
		if !strings.Contains(answers.String(), answer) {
			t.Errorf("no pair in the fixture answers %s", answer)
		}
	}

	var wait string
	if err := db.QueryRowContext(ctx, "SELECT `capability` FROM `task_grant_wait` WHERE `task_id` = 'ci'").Scan(&wait); err != nil {
		t.Fatal(err)
	}
	if wait != model.APIRef("ci:read") {
		t.Errorf("task_grant_wait = %q, want %q", wait, model.APIRef("ci:read"))
	}
	gotCfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{model.APIRef("tasks:read"), "release-gate-read", model.APIRef("ci:read")}; !slices.Equal(gotCfg.DefaultCapabilities, want) {
		t.Errorf("default capabilities = %v, want %v", gotCfg.DefaultCapabilities, want)
	}
	if got, want := gotCfg.ConsoleSessions[0].Capabilities, []string{model.APIRef("task-admin"), model.APIRef("memory")}; !slices.Equal(got, want) {
		t.Errorf("director capabilities = %v, want %v", got, want)
	}
	if got, want := gotCfg.ConsoleSessions[0].DeclinedDefaults, []string{model.APIRef("tasks:read")}; !slices.Equal(got, want) {
		t.Errorf("director declined defaults = %v, want %v", got, want)
	}
	if got, want := gotCfg.ConsoleSessions[1].Capabilities, []string{model.GrainSourceGrant}; !slices.Equal(got, want) {
		t.Errorf("quiet capabilities = %v, want them unchanged", got)
	}
	cat := gotCfg.CapabilityPolicy.Catalogue
	if cat == nil {
		t.Fatal("capability policy catalogue was dropped")
	}
	if want := []string{model.APIRef("tasks:read"), "release-gate-read"}; !slices.Equal(cat.Defaults.Task, want) {
		t.Errorf("catalogue task defaults = %v, want %v", cat.Defaults.Task, want)
	}
	if want := []string{model.APIRef("ci:read"), model.RepoReadGrant(model.RepoRef{Owner: "acme", Name: "docs"})}; !slices.Equal(cat.Defaults.CI, want) {
		t.Errorf("catalogue CI defaults = %v, want %v", cat.Defaults.CI, want)
	}
	if want := []string{model.APIGrantString("ci-read", "acme/widgets")}; !slices.Equal(cat.Defaults.Repos["acme/widgets"].Task, want) {
		t.Errorf("catalogue repo task defaults = %v, want %v", cat.Defaults.Repos["acme/widgets"].Task, want)
	}
	if want := []string{"release-gate-read", model.RepoWriteGrant(widgets)}; !slices.Equal(cat.Defaults.Repos["acme/widgets"].CI, want) {
		t.Errorf("catalogue repo CI defaults = %v, want %v", cat.Defaults.Repos["acme/widgets"].CI, want)
	}
	repo, err := store.GetRepoConfig(ctx, widgets)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{model.APIGrantString("ci-read", "acme/widgets")}; !slices.Equal(repo.DefaultCapabilities, want) {
		t.Errorf("repo default capabilities = %v, want %v", repo.DefaultCapabilities, want)
	}
	if want := []string{"release-gate-read", model.RepoWriteGrant(widgets), model.APIRef("ci:read")}; !slices.Equal(repo.CICapabilities, want) {
		t.Errorf("repo CI capabilities = %v, want %v", repo.CICapabilities, want)
	}

	// Running it again is a no-op.
	state := storedGrantState(t, ctx, db)
	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 131 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating the store a second time: %v", err)
	}
	if again := storedGrantState(t, ctx, db); fmt.Sprint(again) != fmt.Sprint(state) {
		t.Errorf("a second run changed the store:\n got %v\nwant %v", again, state)
	}
}
