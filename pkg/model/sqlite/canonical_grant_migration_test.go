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
	"time"

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

func TestSyncRepoDeclarationsStoresCanonicalGrantReferences(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	declaring := model.RepoRef{Owner: "acme", Name: "widgets"}
	tmplID := model.RepoDeclaredID(model.DeclaredTemplate, declaring, "nightly")
	schedID := model.RepoDeclaredID(model.DeclaredSchedule, declaring, "nightly")

	declared := model.RepoDeclarations{
		Templates: []model.DeclaredTemplateFile{{
			Path: ".grain/templates/nightly.yml",
			Template: model.Template{
				ID:     tmplID,
				Name:   "nightly",
				Title:  "Nightly check",
				Target: &declaring,
				Grants: []model.Grant{
					{Capability: "api:ci-read@acme/widgets", Via: ".grain/templates/nightly.yml"},
					{Capability: model.GrainSourceGrant, Via: ".grain/templates/nightly.yml"},
					{Capability: "api:self-debug", Via: ".grain/templates/nightly.yml"},
				},
			},
		}},
		Schedules: []model.DeclaredScheduleFile{{
			Path: ".grain/schedules/nightly.yml",
			Schedule: model.Schedule{
				ID:         schedID,
				TemplateID: &tmplID,
				Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 3 * 60},
				Enabled:    true,
				Grants: []model.Grant{
					{Capability: "api:tasks:read", Via: ".grain/schedules/nightly.yml"},
					{Capability: model.APIRef("tasks:read") + "={}", Via: ".grain/schedules/nightly.yml"},
				},
			},
		}},
	}
	if _, err := store.SyncRepoDeclarations(ctx, declaring, "abc123", declared, now, time.UTC); err != nil {
		t.Fatalf("SyncRepoDeclarations: %v", err)
	}

	gotTmpl := grantRows(t, ctx, db, "template_grant", "template_id")[tmplID]
	wantTmpl := []string{model.APIGrantString("ci-read", "acme/widgets"), model.APIRef("self-debug"), model.GrainSourceGrant}
	sort.Strings(wantTmpl)
	if !slices.Equal(gotTmpl, wantTmpl) {
		t.Errorf("template_grant = %v, want %v", gotTmpl, wantTmpl)
	}

	gotSched := grantRows(t, ctx, db, "schedule_grant", "schedule_id")[schedID]
	wantSched := []string{model.APIRef("tasks:read")}
	if !slices.Equal(gotSched, wantSched) {
		t.Errorf("schedule_grant = %v, want %v", gotSched, wantSched)
	}
}

// repoAccessFixture is each fixture task's repository access beside its
// target, acme/widgets on main: reads, writes on another repo and on the
// target's own repo on another branch, and the mix of both.
var repoAccessFixture = map[string]struct {
	reads  []model.RepoRef
	writes []model.WriteTarget
}{
	"none":     {},
	"docs":     {reads: []model.RepoRef{{Owner: "acme", Name: "docs"}}},
	"two":      {reads: []model.RepoRef{{Owner: "acme", Name: "docs"}, {Owner: "acme", Name: "site"}}},
	"site":     {writes: []model.WriteTarget{{Repo: model.RepoRef{Owner: "acme", Name: "site"}, Base: "main"}}},
	"publish":  {writes: []model.WriteTarget{{Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, Base: "publish"}}},
	"mixed":    {reads: []model.RepoRef{{Owner: "acme", Name: "docs"}}, writes: []model.WriteTarget{{Repo: model.RepoRef{Owner: "acme", Name: "site"}, Base: "main"}}},
	"readSite": {reads: []model.RepoRef{{Owner: "acme", Name: "site"}}},
}

// repoAccessPredicates is every approval and policy answer that reads a
// task's repository access, for one (proposer, child) pair: the rules
// unified-grants §3 says must not move when Reads and Writes are read back
// from …/repo#read and #write grant rows rather than their own tables.
func repoAccessPredicates(child, parent model.Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "ungrantedWrites=%v ", model.UngrantedWrites(child, parent))
	fmt.Fprintf(&b, "ungrantedReads=%v ", model.UngrantedReads(child, parent))
	fmt.Fprintf(&b, "lands=%v ", model.LandsWhereGranted(child, parent))
	fmt.Fprintf(&b, "sameBranch=%v ", model.SameBranch(child, parent))
	// TemplateWithinTask's mayRead and WriteTargetFor, over a template
	// carrying the child's access.
	err := model.TemplateWithinTask(parent, model.Template{ID: "tmpl", Reads: child.Reads, Writes: child.Writes})
	fmt.Fprintf(&b, "tmplWithin=%v ", err == nil)
	policy := model.CapabilityPolicy{Rules: []model.CapabilityPolicyRule{
		{Capability: model.RepoWriteRef, Deny: true, Repos: []string{"acme/widgets"}},
		{Capability: model.RepoReadRef, RunKinds: []string{model.CapabilityRunCI}},
	}}
	for _, g := range model.TaskRepoGrants(child) {
		if g.Capability == model.RepoTargetRef {
			continue
		}
		subject := model.CapabilitySubject{Kind: model.CapabilityRunTask, Repo: child.Target}
		fmt.Fprintf(&b, "[%s policy=%v]", g.Key(), policy.PermitsTaskRepoGrant(g, subject) != nil)
	}
	return b.String()
}

// TestRepoAccessReadBackFromGrantRowsKeepsItsAnswers: a task's, template's
// and schedule's Reads and Writes, stored instead as the …/repo#read
// {repo} and …/repo#write {repo, base} rows TaskRepoGrants projects them
// to and read back through TaskRepoAccessOf, are the access they started
// as, and every predicate over repository access answers each (proposer,
// child) pair of the fixture the same on both.
func TestRepoAccessReadBackFromGrantRowsKeepsItsAnswers(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	asGrants := func(reads []model.RepoRef, writes []model.WriteTarget) []model.Grant {
		var out []model.Grant
		for _, g := range model.TaskRepoGrants(model.Task{Reads: reads, Writes: writes}) {
			// The row's own spelling: its Key, as task_grant holds it.
			out = append(out, model.Grant{Capability: g.Key(), Via: model.GrantByLabel})
		}
		return out
	}
	fromRows := func(what, id string, grants []model.Grant) ([]model.RepoRef, []model.WriteTarget) {
		reads, writes, rest := model.TaskRepoAccessOf(grants)
		if len(rest) != 0 {
			t.Errorf("%s %s: grant rows %v left %v that are not repository access", what, id, grants, rest)
		}
		return reads, writes
	}
	same := func(what, id string, gotR, wantR []model.RepoRef, gotW, wantW []model.WriteTarget) {
		if !slices.EqualFunc(gotR, wantR, model.RepoRef.Same) || fmt.Sprint(gotW) != fmt.Sprint(wantW) {
			t.Errorf("%s %s read back from grant rows as reads=%v writes=%v, want %v %v", what, id, gotR, gotW, wantR, wantW)
		}
	}

	fromTables, fromGrantRows := map[string]model.Task{}, map[string]model.Task{}
	for id, access := range repoAccessFixture {
		tables := task(id, true)
		tables.Target, tables.Base, tables.Reads, tables.Writes = &widgets, "main", access.reads, access.writes
		rows := task(id+"-rows", true)
		rows.Target, rows.Base, rows.Grants = &widgets, "main", asGrants(access.reads, access.writes)
		for _, tk := range []model.Task{tables, rows} {
			if err := store.PutTask(ctx, tk); err != nil {
				t.Fatal(err)
			}
		}
		gotTables, err := store.GetTask(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		gotRows, err := store.GetTask(ctx, id+"-rows")
		if err != nil {
			t.Fatal(err)
		}
		reads, writes := fromRows("task", id, gotRows.Grants)
		same("task", id, reads, gotTables.Reads, writes, gotTables.Writes)
		gotRows.Reads, gotRows.Writes, gotRows.Grants = reads, writes, nil
		fromTables[id], fromGrantRows[id] = *gotTables, *gotRows

		if err := store.PutTemplate(ctx, model.Template{ID: "tmpl-" + id, Name: "tmpl-" + id, Title: id, Body: "seed",
			Target: &widgets, CreatedAt: now, Grants: asGrants(access.reads, access.writes)}); err != nil {
			t.Fatal(err)
		}
		tmpl, err := store.GetTemplate(ctx, "tmpl-"+id)
		if err != nil {
			t.Fatal(err)
		}
		reads, writes = fromRows("template", id, tmpl.Grants)
		same("template", id, reads, access.reads, writes, access.writes)

		if err := store.PutSchedule(ctx, model.Schedule{ID: "sched-" + id, Title: id, Body: "seed", Target: widgets,
			Recurrence: model.Recurrence{Kind: model.RecurrenceDaily}, Overlap: model.OverlapSkip, NextRunAt: now,
			CreatedAt: now, Grants: asGrants(access.reads, access.writes)}); err != nil {
			t.Fatal(err)
		}
		sched, err := store.GetSchedule(ctx, "sched-"+id)
		if err != nil {
			t.Fatal(err)
		}
		reads, writes = fromRows("schedule", id, sched.Grants)
		same("schedule", id, reads, access.reads, writes, access.writes)
	}

	// The rows really are grant rows, and a task_grant #read stays a CI
	// job's grant to Permits: only PermitsTaskRepoGrant reads it as a
	// task's own access.
	rows := grantRows(t, ctx, db, "task_grant", "task_id")
	if want := model.TaskRepoReadGrant(model.RepoRef{Owner: "acme", Name: "docs"}).Key(); !slices.Contains(rows["docs-rows"], want) {
		t.Errorf("task_grant docs-rows = %v, want it to hold %s", rows["docs-rows"], want)
	}
	if err := (model.CapabilityPolicy{}).Permits(rows["docs-rows"][0], model.CapabilitySubject{Kind: model.CapabilityRunTask, Repo: &widgets}); err == nil {
		t.Errorf("Permits(%s) on a task = nil, want the CI-only refusal", rows["docs-rows"][0])
	}

	var answers strings.Builder
	for parent := range repoAccessFixture {
		for child := range repoAccessFixture {
			want := repoAccessPredicates(fromTables[child], fromTables[parent])
			answers.WriteString(want + "\n")
			if got := repoAccessPredicates(fromGrantRows[child], fromGrantRows[parent]); got != want {
				t.Errorf("(%s proposes %s) read back from grant rows:\n got %s\nwant %s", parent, child, got, want)
			}
		}
	}
	for _, answer := range []string{"ungrantedWrites=[]", "ungrantedWrites=[{", "ungrantedReads=[]", "ungrantedReads=[acme/",
		"lands=true", "lands=false", "tmplWithin=true", "tmplWithin=false", "policy=true", "policy=false"} {
		if !strings.Contains(answers.String(), answer) {
			t.Errorf("no pair in the fixture answers %s", answer)
		}
	}
}
