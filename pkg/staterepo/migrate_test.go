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

package staterepo_test

// The test that has to fail in the pull request that bumps
// model.SchemaVersion, rather than on a deployment (grain/task-59).
//
// An in-place upgrade now carries a deployment's rows across a schema
// change by exporting them and importing them again on the far side
// (pkg/upgrade's schema step, pkg/cli/schemaupgrade.go). That works
// because a dump is not a database: Import skips a column this build does
// not have and treats a table it has no file for as empty, so adding a
// nullable column, dropping one, or adding a table needs nothing written
// anywhere. It stops working the moment somebody makes a change of a
// different shape -- a renamed column, a new one with no usable default,
// a table split in two -- and the failure mode is a deployment that comes
// back missing rows, or that does not come back.
//
// So there is a real export in testdata, checked in at the schema it was
// made at, and this imports it into whatever schema this build knows. The
// day the two numbers differ is the day this test starts being about a
// migration; until then it is about the fixture still loading, which is
// worth having too. What to do when it fails is in the failure message.

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// -update-golden-dump regenerates the fixture from this build, which is
// a deliberate act and not a repair: it throws away the older schema this
// test was carrying rows out of, so the migration it was checking stops
// being checked by anything. Do it when the old dump is genuinely below
// model.BaselineSchemaVersion, and say so in the commit that does it.
var updateGolden = flag.Bool("update-golden-dump", false,
	"rewrite pkg/staterepo/testdata/dump-schema-* from this build's schema")

// goldenDump finds the checked-in export, whatever schema it was made at.
// Found by glob rather than named, so regenerating it at a new version is
// one command and no edit here.
func goldenDump(t *testing.T) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join("testdata", "dump-schema-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("testdata holds %d golden dumps (%v), want exactly one -- the point of it is that there "+
			"is one oldest dump this build is claimed to be able to read", len(matches), matches)
	}
	return matches[0]
}

// TestTheGoldenDumpMigratesIntoThisBuildsSchema is the whole file's
// point: a deployment's rows, exported by the build that was current when
// they were written, landing in a store at whatever schema this build
// has.
//
// Not parallel, because under -update-golden-dump this is the one test
// here that writes outside its own t.TempDir: it deletes testdata's
// golden dump and exports a new one in its place, while the tests below
// are reading that same directory. Serial costs nothing -- `go test`
// runs every non-parallel test to completion before any parallel test's
// body starts -- and it is what keeps regenerating the fixture from
// racing the tests that read it.
func TestTheGoldenDumpMigratesIntoThisBuildsSchema(t *testing.T) {
	ctx := context.Background()
	if *updateGolden {
		writeGoldenDump(t, ctx)
		return
	}
	dir := goldenDump(t)
	found, err := staterepo.ReadSchemaVersion(dir)
	if err != nil {
		t.Fatal(err)
	}
	if found > model.SchemaVersion {
		t.Fatalf("%s is stamped %d and this build knows %d: the fixture is from the future, which means "+
			"it was regenerated on a branch that bumped the schema and this one has not", dir, found,
			model.SchemaVersion)
	}
	if found < model.BaselineSchemaVersion {
		t.Fatalf("%s is stamped %d, which is below the single-tenant baseline (%d) -- so grain refuses to "+
			"migrate the very dump this test exists to prove it can. Regenerate the fixture "+
			"(go test ./pkg/staterepo -run GoldenDump -update-golden-dump)",
			dir, found, model.BaselineSchemaVersion)
	}

	store, db := openRealDB(t)
	report, err := staterepo.CheckMigration(ctx, db, dir, t.TempDir(), model.SchemaVersion)
	if err != nil {
		t.Fatalf("a deployment exported at schema %d does not import into the %d this build knows: %v\n\n"+
			"This is what an in-place upgrade across this schema change would do to a real deployment, so\n"+
			"one of these is now this branch's to do:\n"+
			"  * make the change absorbable -- a new column that is nullable or has a default, a dropped\n"+
			"    column, a new table -- which is what Import already carries;\n"+
			"  * write the step that carries these rows, in pkg/staterepo/migrate.go's dumpFixups, keyed\n"+
			"    by %d;\n"+
			"  * or decide this schema change is one no deployment's rows survive and regenerate this\n"+
			"    fixture past %d -- which makes every upgrade across it destructive, and is what\n"+
			"    scripts/setup.sh already does. Say so in the commit.",
			found, model.SchemaVersion, err, found, found)
	}
	if report.SchemaVersion != found {
		t.Errorf("the report says the dump is at schema %d, want %d", report.SchemaVersion, found)
	}
	assertNoCarriedTableIgnored(t, report.Warnings)

	// Not just "it imported": the rows a deployment would miss.
	task, err := store.GetTask(ctx, "golden-1")
	if err != nil {
		t.Fatalf("the task in the golden dump did not arrive: %v", err)
	}
	if task == nil {
		t.Fatal("the task in the golden dump is not in the store the dump was imported into")
	}
	if task.Title != "Rename the endpoint" {
		t.Errorf("the task arrived as %q", task.Title)
	}
	if task.Target == nil || task.Target.Name != "payments-api" {
		t.Errorf("the task arrived pointing at %v", task.Target)
	}
	templates, err := store.ListTemplates(ctx)
	if err != nil || len(templates) == 0 {
		t.Fatalf("the templates in the golden dump did not arrive: %v (%d)", err, len(templates))
	}
	if templates[0].Name != "nightly" {
		t.Errorf("the template arrived as %q", templates[0].Name)
	}
	cfg, err := store.GetConfig(ctx)
	if err != nil || cfg == nil {
		t.Fatalf("the deployment's own config row did not arrive: %v", err)
	}
	if cfg.MaxWorkers != 7 {
		t.Errorf("the config row arrived with MaxWorkers %d, want 7", cfg.MaxWorkers)
	}
	transcript, hasRun, err := store.RunTranscript(ctx, "golden-1", 1)
	if err != nil || !hasRun {
		t.Fatalf("the run in the golden dump did not arrive: %v (found=%v)", err, hasRun)
	}
	if transcript != "make test\nok\n" {
		t.Errorf("the run arrived with transcript %q", transcript)
	}
	assertGoldenExtrasArrived(t, ctx, store)
}

// carriedRetiredTables are the tables a dump fixup carries out of and
// this build no longer has. Their rows arrive elsewhere, so a migration
// check -- and `grain state prepare-upgrade`, which prints its warnings
// -- must not report them as rows that would be ignored.
var carriedRetiredTables = []string{"team_repo_config", "team_gcp_key", "team"}

func assertNoCarriedTableIgnored(t *testing.T, warnings []string, tables ...string) {
	t.Helper()
	if len(tables) == 0 {
		tables = carriedRetiredTables
	}
	for _, w := range warnings {
		for _, table := range tables {
			if strings.Contains(w, staterepo.DumpPath(table)) {
				t.Errorf("the migration check warns about %s, whose rows a fixup carried across:\n%s", table, w)
			}
		}
	}
}

// assertGoldenExtrasArrived is what the fixture's newer rows have to
// read as on the far side of an import: the tables 2026-09-06 added,
// each carrying something an upgrade could lose without saying so.
//
// Read through the store rather than in SQL because that is where the
// loss would show. A board whose panels did not arrive is a dashboard
// that renders empty; a task whose second custom field value did not is
// a set silently reduced to one, which is exactly what a build that did
// not have the widened key would do to it.
func assertGoldenExtrasArrived(t *testing.T, ctx context.Context, store *model.Store) {
	t.Helper()
	fields, err := store.ListCustomFields(ctx)
	if err != nil || len(fields) != 1 {
		t.Fatalf("the declared custom field did not arrive: %v (%d fields)", err, len(fields))
	}
	if got := fields[0]; got.Name != "customer" || !got.Multiple ||
		!equalStrings(got.Options, []string{"acme", "globex"}) {
		t.Errorf("the custom field arrived as %+v", got)
	}
	task, err := store.GetTask(ctx, "golden-1")
	if err != nil || task == nil {
		t.Fatalf("the task did not arrive: %v", err)
	}
	if !equalStrings(task.CustomFields["customer"], []string{"acme", "globex"}) {
		t.Errorf("the task's field values arrived as %v, want both of them", task.CustomFields["customer"])
	}
	if len(task.Writes) != 1 || task.Writes[0].Repo.Name != "docs" || task.Writes[0].Base != "main" {
		t.Errorf("the task's extra write target arrived as %+v", task.Writes)
	}
	schedules, err := store.ListSchedules(ctx)
	if err != nil || len(schedules) != 1 {
		t.Fatalf("the schedule did not arrive: %v (%d schedules)", err, len(schedules))
	}
	// The overlap policy is the one column of the four added that day
	// that is not a setting, and the one whose default is not the value
	// the fixture holds.
	if schedules[0].Overlap != model.OverlapAllow {
		t.Errorf("the schedule arrived with overlap %q, want %q", schedules[0].Overlap, model.OverlapAllow)
	}
	if len(schedules[0].Writes) != 1 ||
		!equalStrings(schedules[0].CustomFields["customer"], []string{"acme"}) {
		t.Errorf("the schedule arrived with writes %+v and fields %v",
			schedules[0].Writes, schedules[0].CustomFields)
	}
	boards, err := store.ListBoards(ctx)
	if err != nil || len(boards) != 3 {
		t.Fatalf("the boards did not arrive: %v (%d boards)", err, len(boards))
	}
	var columns, panels, sections int
	for _, b := range boards {
		columns += len(b.Columns)
		panels += len(b.Panels)
		sections += len(b.Sections)
	}
	if columns != 2 || panels != 2 || sections != 2 {
		t.Errorf("the boards arrived with %d columns, %d panels and %d sections, want 2 of each",
			columns, panels, sections)
	}
	pause, err := store.GetPipelinePause(ctx)
	if err != nil || pause == nil {
		t.Fatalf("the pipeline pause did not arrive: %v", err)
	}
	if pause.Reason != "upgrading the sandbox image" {
		t.Errorf("the pause arrived with reason %q", pause.Reason)
	}
}

// writeGoldenDump builds a small but real deployment and exports it,
// which is what -update-golden-dump does. Small on purpose: what this
// fixture is for is the shape of the rows, not their number, and every
// row in it has to be worth reading in a diff six schema versions from
// now.
//
// Small is not the same as narrow, though, and the difference is what
// the tests reading this fixture can prove. A table with no file here,
// or a file with no rows in it, makes "a build that has this table
// imports a dump written before it" a claim about an empty table --
// which every build passes. So a feature that changes what a state
// repository holds belongs here the week it lands: this deployment
// declares a custom field and carries values for it, files work in more
// than one repository, keeps a dashboard and a release train, and is
// paused.
func writeGoldenDump(t *testing.T, ctx context.Context) {
	t.Helper()
	store, db := openRealDB(t)
	at := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	repo := model.RepoRef{Owner: "owner", Name: "payments-api"}
	// Declared first, since the task below carries values for it. Two
	// values on one field, which is the shape that needs the value in
	// task_custom_field's primary key (model.CustomField.Multiple).
	if err := store.PutCustomField(ctx, model.CustomField{
		Name: "customer", Label: "Customer", Type: model.CustomFieldEnum,
		Options: []string{"acme", "globex"}, Description: "Who asked for this",
		Position: 1, Multiple: true, ShowOnRow: true,
	}); err != nil {
		t.Fatalf("declaring a custom field: %v", err)
	}
	if err := store.PutTask(ctx, model.Task{
		ID: "golden-1", Intent: model.IntentImplement, Title: "Rename the endpoint",
		Body: "with a body\nover two lines",
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "bwsalmon"}},
			Reason:      model.ReasonDirect,
		},
		Target:  &model.RepoRef{Owner: "owner", Name: "payments-api"},
		Binding: model.BindingDirective,
		// A second repository this task may land work in (grain/task-86),
		// and this deployment's own field, carrying both its values.
		Writes:       []model.WriteTarget{{Repo: model.RepoRef{Owner: "owner", Name: "docs"}, Base: "main"}},
		CustomFields: map[string][]string{"customer": {"acme", "globex"}},
		CreatedAt:    &at,
	}); err != nil {
		t.Fatalf("putting a task: %v", err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "golden-1-1", TaskID: "golden-1", Sandbox: "grain-golden-1-1", Attempt: 1, StartedAt: at,
	}, model.Limits{}); err != nil {
		t.Fatalf("starting a run: %v", err)
	}
	if err := store.SetRunTranscript(ctx, "golden-1-1", "make test\nok\n"); err != nil {
		t.Fatalf("writing a transcript: %v", err)
	}
	if err := store.PutTemplate(ctx, model.Template{
		ID: "golden-tpl", Name: "nightly", Title: "Nightly sweep", Body: "Look for anything rotting.",
		Reads:        []model.RepoRef{{Owner: "owner", Name: "payments-api"}},
		Writes:       []model.WriteTarget{{Repo: model.RepoRef{Owner: "owner", Name: "docs"}}},
		CustomFields: map[string][]string{"customer": {"acme"}},
		CreatedAt:    at,
	}); err != nil {
		t.Fatalf("putting a template: %v", err)
	}
	// A schedule, carrying the same two things a template does and an
	// overlap policy that is not the column's default -- so a dump
	// written before that column existed has something to be missing.
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: "golden-sch", Title: "Weekly sweep", Body: "Look again.", Target: repo,
		Recurrence:   model.Recurrence{Kind: model.RecurrenceWeekly, TimeOfDay: 540, Weekday: time.Monday},
		Overlap:      model.OverlapAllow,
		Writes:       []model.WriteTarget{{Repo: model.RepoRef{Owner: "owner", Name: "docs"}, Base: "main"}},
		CustomFields: map[string][]string{"customer": {"acme"}},
		Enabled:      true, NextRunAt: at, CreatedAt: at,
	}); err != nil {
		t.Fatalf("putting a schedule: %v", err)
	}
	// A saved dashboard of each kind that has child rows: a Kanban
	// board's columns, a panels board's panels and a report board's
	// sections (grain/task-48, grain/task-88, grain/task-83). The ids
	// come from the store's own allocator, which is what a board made
	// through the UI or by an agent gets.
	kanbanID, err := store.NewBoardID(ctx)
	if err != nil {
		t.Fatalf("allocating a board id: %v", err)
	}
	if err := store.PutBoard(ctx, model.Board{
		ID: kanbanID, Name: "Payments", Folder: "Reports", Kind: model.BoardKanban,
		Description: "What the payments team has in flight",
		Query:       "f=repo:any:owner/payments-api",
		Columns: []model.BoardColumn{
			{Name: "Queued", States: []model.State{model.StateQueued}},
			{Name: "In flight", States: []model.State{model.StateRunning, model.StateAwaitingReply}},
		},
		OrderKey: 1,
	}); err != nil {
		t.Fatalf("putting a board: %v", err)
	}
	panelsID, err := store.NewBoardID(ctx)
	if err != nil {
		t.Fatalf("allocating a board id: %v", err)
	}
	if err := store.PutBoard(ctx, model.Board{
		ID: panelsID, Name: "Monday morning", Kind: model.BoardPanels,
		Panels: []model.BoardPanel{
			{Kind: model.PanelList, Title: "Running now", Query: "f=state:any:running", Width: model.PanelHalf},
			{Kind: model.PanelGantt, Range: "7d", Width: model.PanelFull},
		},
		OrderKey: 2,
	}); err != nil {
		t.Fatalf("putting a board: %v", err)
	}
	reportID, err := store.NewBoardID(ctx)
	if err != nil {
		t.Fatalf("allocating a board id: %v", err)
	}
	if err := store.PutBoard(ctx, model.Board{
		ID: reportID, Name: "How payments is going", Folder: "Reports",
		Kind:        model.BoardReport,
		Description: "Throughput and latency for the payments team",
		// The query a report board is scoped by is the same string a
		// task board carries, read as a scope (model.Board's own note).
		Query: "f=repo:any:owner/payments-api&f=created:within:30",
		Sections: []model.BoardSection{
			{Kind: model.SectionThroughput, Title: "Last thirty days"},
			{Kind: model.SectionLatency},
		},
		OrderKey: 3,
	}); err != nil {
		t.Fatalf("putting a board: %v", err)
	}
	// And the deployment is paused, which is a row rather than a flag
	// (model.PipelinePause) and so is a row a restore has to bring back:
	// a deployment that came back up unpaused would start picking work
	// up that somebody had stopped.
	if err := store.SetPipelinePause(ctx, model.PipelinePause{
		PausedAt: at, Reason: "upgrading the sandbox image",
		By: model.Principal{Kind: model.PrincipalHuman, ID: "bwsalmon"},
	}); err != nil {
		t.Fatalf("pausing: %v", err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                model.RepoRef{Owner: "owner", Name: "payments-api"},
		DefaultCapabilities: []string{"githubtoken"},
		CICapabilities:      []string{"gcp-key"},
	}); err != nil {
		t.Fatalf("putting repo config: %v", err)
	}
	cfg := model.Config{}
	if stored, err := store.GetConfig(ctx); err != nil {
		t.Fatalf("reading the config: %v", err)
	} else if stored != nil {
		cfg = *stored
	}
	cfg.MaxWorkers = 7
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("writing the config: %v", err)
	}

	for _, old := range must(filepath.Glob(filepath.Join("testdata", "dump-schema-*")))(t) {
		if err := os.RemoveAll(old); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join("testdata", "dump-schema-"+strconv.Itoa(model.SchemaVersion))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	if err := staterepo.WriteSchemaVersion(dir, model.SchemaVersion); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s from this build's schema", dir)
}

func must[T any](v T, err error) func(*testing.T) T {
	return func(t *testing.T) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

// A dump stamped from before the single-tenant baseline is refused in
// the startup guard's words, whatever rows it holds: grain has no
// migration from a schema a multi-tenant build wrote
// (model.CheckSchemaBaseline), and the refusal says what to do instead.
func TestADumpFromBeforeTheBaselineIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "dump")
	if err := os.CopyFS(dir, os.DirFS(goldenDump(t))); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.WriteSchemaVersion(dir, model.BaselineSchemaVersion-1); err != nil {
		t.Fatal(err)
	}
	store, db := openRealDB(t)
	_, err := staterepo.CheckMigration(ctx, db, dir, t.TempDir(), model.SchemaVersion)
	if !errors.Is(err, model.ErrBeforeBaseline) || !errors.Is(err, staterepo.ErrNotMigratable) {
		t.Fatalf("CheckMigration of a dump from before the baseline: %v, want ErrBeforeBaseline and ErrNotMigratable", err)
	}
	for _, w := range []string{"this state repository", "before the single-tenant baseline", "fresh data dir",
		"grain connections import", "Nothing in this state repository has been changed"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("the refusal does not say %q:\n%v", w, err)
		}
	}
	// The golden dump is a single-tenant one, so the refusal must not say
	// it holds multi-tenant data, nor call it a data dir (task 4800).
	for _, w := range []string{"multi-tenant", "this data dir"} {
		if strings.Contains(err.Error(), w) {
			t.Errorf("the refusal of a single-tenant dump says %q:\n%v", w, err)
		}
	}
	if got, err := store.GetTask(ctx, "golden-1"); err != nil || got != nil {
		t.Errorf("the refused dump's task arrived anyway: %v, %v", got, err)
	}
	// And Import refuses it the same way, whichever path a dump takes.
	if err := staterepo.Import(ctx, db, dir); !errors.Is(err, model.ErrBeforeBaseline) {
		t.Errorf("Import of a dump from before the baseline: %v, want ErrBeforeBaseline", err)
	}
}

// A dump holding a sign-in account in the `user` table schema 20 dropped
// is refused in the startup guard's words, rather than imported with the
// account left on the floor: once it is gone from the store, nothing
// else would ever see it (retired.go).
func TestADumpWithASignInAccountIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "dump")
	if err := os.CopyFS(dir, os.DirFS(goldenDump(t))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tables", "user.json"),
		[]byte(`[{"id":"alice","username":"alice","time_zone":"","password_hash":""}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	store, db := openRealDB(t)
	_, err := staterepo.CheckMigration(ctx, db, dir, t.TempDir(), model.SchemaVersion)
	if !errors.Is(err, model.ErrMultiTenantData) {
		t.Fatalf("CheckMigration of a dump holding a sign-in account: %v, want ErrMultiTenantData", err)
	}
	for _, w := range []string{"this state repository", "1 sign-in accounts (user)", "fresh data dir"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("the refusal does not say %q:\n%v", w, err)
		}
	}
	if got, err := store.GetTask(ctx, "golden-1"); err != nil || got != nil {
		t.Errorf("the refused dump's task arrived anyway: %v, %v", got, err)
	}
	if err := staterepo.Import(ctx, db, dir); !errors.Is(err, model.ErrMultiTenantData) {
		t.Errorf("Import of a dump holding a sign-in account: %v, want ErrMultiTenantData", err)
	}
}

// A dump holding a member of a team besides the default one, in the
// `team_user` table schema 21 dropped, is refused in the startup guard's
// words. The default team's own members are not: every build before
// 3e-iv-b2c-iii seeded them on a single-tenant daemon, so a dump that
// holds only those imports, with the rows passed over (retired.go).
func TestADumpWithAnotherTeamsMemberIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dump := func(t *testing.T, rows string) string {
		t.Helper()
		dir := filepath.Join(t.TempDir(), "dump")
		if err := os.CopyFS(dir, os.DirFS(goldenDump(t))); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "tables", "team_user.json"), []byte(rows), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	defaultMembers := dump(t, `[{"team_id":"default","user":"grain","role":"admin","admin":1}]`)
	store, db := openRealDB(t)
	if _, err := staterepo.CheckMigration(ctx, db, defaultMembers, t.TempDir(), model.SchemaVersion); err != nil {
		t.Fatalf("CheckMigration of a dump holding only the default team's members: %v", err)
	}

	other := dump(t, `[{"team_id":"default","user":"grain","role":"admin","admin":1},`+
		`{"team_id":"beta","user":"alice","role":"member","admin":0}]`)
	store, db = openRealDB(t)
	_, err := staterepo.CheckMigration(ctx, db, other, t.TempDir(), model.SchemaVersion)
	if !errors.Is(err, model.ErrMultiTenantData) {
		t.Fatalf("CheckMigration of a dump holding another team's member: %v, want ErrMultiTenantData", err)
	}
	for _, w := range []string{"this state repository", "1 members of teams besides the default one (team_user)", "fresh data dir"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("the refusal does not say %q:\n%v", w, err)
		}
	}
	if got, err := store.GetTask(ctx, "golden-1"); err != nil || got != nil {
		t.Errorf("the refused dump's task arrived anyway: %v, %v", got, err)
	}
	if err := staterepo.Import(ctx, db, other); !errors.Is(err, model.ErrMultiTenantData) {
		t.Errorf("Import of a dump holding another team's member: %v, want ErrMultiTenantData", err)
	}
}

// A dump at schema 21 keeps the deployment's git connections, pinned
// connection, per-model settings and default model on the default team's
// row. Schema 22 moved them onto grain_config, which Import cannot do by
// itself -- it would pass over the team columns as ones this build lacks
// -- so dumpFixups' step past 21 carries them across
// (moveDefaultTeamSettings). The team's default model wins over the one
// grain_config held, since it was the one dispatch ran on.
func TestADumpAt21CarriesTheDefaultTeamsSettingsOntoGrainConfig(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "dump")
	if err := os.CopyFS(dir, os.DirFS(goldenDump(t))); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.WriteSchemaVersion(dir, 21); err != nil {
		t.Fatal(err)
	}
	writeTable(t, dir, "team", []map[string]any{{
		"id": model.DefaultTeamID, "name": "admin", "description": "", "agent_framework": "",
		"agent_backend_id": "claude-main",
		"default_model":    "claude-opus-5-5",
		"model_settings":   `[{"model":"claude-opus-5-5","effort":"high"}]`,
		"git_credential":   "legacy-token",
		"git_connections":  `[{"credential":"grain-token"},{"credential":"acme-token","scope":"acme"}]`,
		"time_zone":        "Europe/London",
		"merge_batch_size": 4,
	}})
	configs := readTable(t, dir, "grain_config")
	configs[0]["default_model"] = "gemini-3.1-pro"
	writeTable(t, dir, "grain_config", configs)

	store, db := openRealDB(t)
	if _, err := staterepo.CheckMigration(ctx, db, dir, t.TempDir(), model.SchemaVersion); err != nil {
		t.Fatalf("CheckMigration of a dump at 21 holding the default team's settings: %v", err)
	}
	cfg, err := store.GetConfig(ctx)
	if err != nil || cfg == nil {
		t.Fatalf("the config row did not arrive: %v", err)
	}
	if cfg.MaxWorkers != 7 {
		t.Errorf("the config row arrived with MaxWorkers %d, want the dump's 7", cfg.MaxWorkers)
	}
	wantConns := []model.TeamGitConnection{{Credential: "grain-token"}, {Credential: "acme-token", Scope: "acme"}}
	if !slices.Equal(cfg.GitConnections, wantConns) {
		t.Errorf("GitConnections arrived as %+v, want %+v", cfg.GitConnections, wantConns)
	}
	if cfg.GitCredential != "legacy-token" {
		t.Errorf("GitCredential arrived as %q, want legacy-token", cfg.GitCredential)
	}
	if cfg.DefaultModel != "claude-opus-5-5" {
		t.Errorf("DefaultModel arrived as %q, want the team's claude-opus-5-5 over grain_config's own", cfg.DefaultModel)
	}
	if cfg.AgentBackendID != "claude-main" {
		t.Errorf("AgentBackendID arrived as %q, want claude-main", cfg.AgentBackendID)
	}
	if s, ok := cfg.LookupModelSetting("claude-opus-5-5"); !ok || s.Effort != "high" {
		t.Errorf("the model setting arrived as %+v (found=%v), want effort high", s, ok)
	}
	if cred, err := store.GitConnectionOn(ctx, "", "acme", "widgets"); err != nil || cred != "acme-token" {
		t.Errorf("acme/widgets resolves to %q, %v; want the owner-scoped acme-token", cred, err)
	}
	// The step past 23 carries the row's merge batch size on in its
	// turn, and the time zone, which nothing acted on, stays behind.
	if cfg.MergeBatchSize != 4 {
		t.Errorf("MergeBatchSize arrived as %d, want the team's 4", cfg.MergeBatchSize)
	}
	if cfg.TimeZone == "Europe/London" {
		t.Errorf("the team's time zone arrived on grain_config, which has its own")
	}
}

// A dump at schema 23 still holds the default team's row, with what
// dispatch, commits, the repo allowlist, base-branch repair and the merge
// queue read off it. Schema 24 dropped the table, so dumpFixups' step
// past 23 carries each onto grain_config (foldDefaultTeamRow): the
// framework, commit identity and target repos as they were, the team's
// repair template and merge batch size over grain_config's own, and the
// legacy per-framework models folded into the default model and the
// per-model settings, as dispatch folded them.
func TestADumpAt23FoldsTheDefaultTeamRowOntoGrainConfig(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "dump")
	if err := os.CopyFS(dir, os.DirFS(goldenDump(t))); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.WriteSchemaVersion(dir, 23); err != nil {
		t.Fatal(err)
	}
	writeTable(t, dir, "team", []map[string]any{{
		"id": model.DefaultTeamID, "name": "admin", "description": "Built-in admin team",
		"agent_framework":    model.AgentFrameworkCodex,
		"codex_model":        "gpt-5.5-codex",
		"codex_effort":       "high",
		"agent_git_name":     "acme bot",
		"agent_git_email":    "bot@acme.example",
		"target_repos":       "acme/widgets,acme/site",
		"repair_template_id": "tmpl-team",
		"merge_batch_size":   2,
		"time_zone":          "Europe/London",
	}})
	configs := readTable(t, dir, "grain_config")
	configs[0]["default_model"] = ""
	configs[0]["model_settings"] = ""
	configs[0]["repair_template_id"] = "tmpl-deployment"
	configs[0]["merge_batch_size"] = 5
	writeTable(t, dir, "grain_config", configs)

	store, db := openRealDB(t)
	report, err := staterepo.CheckMigration(ctx, db, dir, t.TempDir(), model.SchemaVersion)
	if err != nil {
		t.Fatalf("CheckMigration of a dump at 23 holding the default team's row: %v", err)
	}
	// Only team: the golden dump's team_repo_config and team_gcp_key are
	// still in it, which no dump really written at 23 would be.
	assertNoCarriedTableIgnored(t, report.Warnings, "team")
	cfg, err := store.GetConfig(ctx)
	if err != nil || cfg == nil {
		t.Fatalf("the config row did not arrive: %v", err)
	}
	if cfg.AgentFramework != model.AgentFrameworkCodex {
		t.Errorf("AgentFramework arrived as %q, want codex", cfg.AgentFramework)
	}
	if cfg.AgentGitName != "acme bot" || cfg.AgentGitEmail != "bot@acme.example" {
		t.Errorf("the commit identity arrived as %q <%q>", cfg.AgentGitName, cfg.AgentGitEmail)
	}
	if want := []string{"acme/widgets", "acme/site"}; !slices.Equal(cfg.TargetRepos, want) {
		t.Errorf("TargetRepos arrived as %v, want %v", cfg.TargetRepos, want)
	}
	if cfg.RepairTemplateID != "tmpl-team" || cfg.MergeBatchSize != 2 {
		t.Errorf("RepairTemplateID %q and MergeBatchSize %d, want the team's tmpl-team and 2",
			cfg.RepairTemplateID, cfg.MergeBatchSize)
	}
	if cfg.DefaultModel != "gpt-5.5-codex" {
		t.Errorf("DefaultModel arrived as %q, want the team's legacy codex model", cfg.DefaultModel)
	}
	if s, ok := cfg.LookupModelSetting("gpt-5.5-codex"); !ok || s.Effort != "high" {
		t.Errorf("the legacy codex effort arrived as %+v (found=%v), want high", s, ok)
	}
	if cfg.TimeZone == "Europe/London" {
		t.Errorf("the team's time zone arrived on grain_config, which has its own")
	}
}

// A dump at 23 holding a team besides the default one is a multi-tenant
// deployment's, and is refused in the startup guard's words rather than
// imported with the team left behind (retired.go).
func TestADumpWithASecondTeamIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "dump")
	if err := os.CopyFS(dir, os.DirFS(goldenDump(t))); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.WriteSchemaVersion(dir, 23); err != nil {
		t.Fatal(err)
	}
	writeTable(t, dir, "team", []map[string]any{
		{"id": model.DefaultTeamID, "name": "admin"},
		{"id": "beta", "name": "beta"},
	})
	_, db := openRealDB(t)
	_, err := staterepo.CheckMigration(ctx, db, dir, t.TempDir(), model.SchemaVersion)
	if !errors.Is(err, model.ErrMultiTenantData) || !strings.Contains(err.Error(), "1 teams besides the default one") {
		t.Fatalf("CheckMigration of a dump with a second team: %v, want ErrMultiTenantData naming it", err)
	}
}

// A dump at 21 whose default team holds no settings imports with
// grain_config's own default model left as it was.
func TestADumpAt21WithAnEmptyDefaultTeamKeepsGrainConfigsDefaultModel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "dump")
	if err := os.CopyFS(dir, os.DirFS(goldenDump(t))); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.WriteSchemaVersion(dir, 21); err != nil {
		t.Fatal(err)
	}
	writeTable(t, dir, "team", []map[string]any{{
		"id": model.DefaultTeamID, "name": "admin", "default_model": "", "git_connections": "",
	}})
	configs := readTable(t, dir, "grain_config")
	configs[0]["default_model"] = "gemini-3.1-pro"
	writeTable(t, dir, "grain_config", configs)

	store, db := openRealDB(t)
	if _, err := staterepo.CheckMigration(ctx, db, dir, t.TempDir(), model.SchemaVersion); err != nil {
		t.Fatalf("CheckMigration: %v", err)
	}
	cfg, err := store.GetConfig(ctx)
	if err != nil || cfg == nil {
		t.Fatalf("the config row did not arrive: %v", err)
	}
	if cfg.DefaultModel != "gemini-3.1-pro" || len(cfg.GitConnections) != 0 {
		t.Errorf("the config row arrived with DefaultModel %q and connections %+v", cfg.DefaultModel, cfg.GitConnections)
	}
}

// The two migrations grain will not do, and the difference between them.
// A dump older than the baseline is one a multi-tenant build wrote; a rollback
// across a fixup is one grain would have to run backwards, which the
// fixups are not written to do. Everything else is allowed, rollbacks
// included -- a deployment that cannot come back up on the build it was
// running an hour ago is a worse failure than the one this whole
// mechanism exists to fix.
func TestMigratableSaysWhatItWillNotCarry(t *testing.T) {
	t.Parallel()
	if err := staterepo.Migratable(model.BaselineSchemaVersion-1, model.SchemaVersion); err == nil {
		t.Error("a dump older than the baseline is migratable")
	} else if !errors.Is(err, staterepo.ErrNotMigratable) || !errors.Is(err, model.ErrBeforeBaseline) {
		t.Errorf("refused with %v, want an ErrNotMigratable in the startup guard's words", err)
	}
	if err := staterepo.Migratable(model.SchemaVersion, model.SchemaVersion); err != nil {
		t.Errorf("a dump at this build's own schema is not migratable: %v", err)
	}
	if err := staterepo.Migratable(model.BaselineSchemaVersion, model.SchemaVersion+2); err != nil {
		t.Errorf("a dump from the baseline into a later schema is not migratable: %v", err)
	}
	if err := staterepo.Migratable(model.SchemaVersion+1, model.SchemaVersion); err != nil {
		t.Errorf("a rollback with no fixup in the way is not migratable: %v", err)
	}
	// Schema 22 moved the default team's settings onto grain_config
	// (moveDefaultTeamSettings), which nothing puts back.
	if err := staterepo.Migratable(22, 21); !errors.Is(err, staterepo.ErrNotMigratable) {
		t.Errorf("a rollback across schema 22's fixup: %v, want ErrNotMigratable", err)
	}
}

// MigrateDump hands back the directory it was given when nothing has to
// be done to the files, which is every migration with no fixup in its
// range. Worth pinning: the alternative is copying a whole deployment's
// rows around to change nothing about them, on a host that is in the
// middle of a restart.
func TestMigrateDumpDoesNotCopyADumpItHasNothingToDoTo(t *testing.T) {
	t.Parallel()
	dir := goldenDump(t)
	// From this build's schema to one two along: no fixup is keyed in
	// that range, whereas the baseline's range now holds 21's.
	got, err := staterepo.MigrateDump(dir, t.TempDir(), model.SchemaVersion, model.SchemaVersion+2)
	if err != nil {
		t.Fatalf("MigrateDump: %v", err)
	}
	if got != dir {
		t.Errorf("MigrateDump returned %q, want the directory it was given (%q)", got, dir)
	}
}

func readGolden(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Schema 23 folded the default team's team_repo_config into repo_config
// and moved its team_gcp_key rows to gcp_key. A dump at 22 holding both
// comes across with the team's per-repository settings on the
// repository's own row -- over the row's where both said something, and
// unioned for default capabilities -- a row made for a repository that
// had none, and the deployment's GCP keys where the store reads them.
func TestADumpAt22FoldsTheDefaultTeamsRepoSettingsAndGCPKeys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "dump")
	if err := os.CopyFS(dir, os.DirFS(goldenDump(t))); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.WriteSchemaVersion(dir, 22); err != nil {
		t.Fatal(err)
	}
	// The golden dump's repo_config row for owner/payments-api says
	// githubtoken and ci gcp-key, and its setup command is a file.
	writeTable(t, dir, "team_repo_config", []map[string]any{
		{
			"team_id": model.DefaultTeamID, "owner": "owner", "name": "payments-api",
			"clone_url": "", "default_capabilities": "memory,githubtoken", "ci_capabilities": "",
			"prompt_extension": "", "setup_command": "make team-setup", "ci_mode": "grain",
			"git_credential": "payments-token", "repair_template_id": "",
			"require_branch_up_to_date": nil, "watch_default_branch": 1, "release_secrets": "",
		},
		{
			"team_id": model.DefaultTeamID, "owner": "acme", "name": "widgets",
			"clone_url": "https://git.example.com/acme/widgets.git", "default_capabilities": "",
			"ci_capabilities": "repo-read:acme/docs", "prompt_extension": "", "setup_command": "",
			"ci_mode": "", "git_credential": "", "repair_template_id": "widgets-repair",
			"require_branch_up_to_date": 1, "watch_default_branch": nil, "release_secrets": "NPM_TOKEN",
		},
	})
	writeTable(t, dir, "team_gcp_key", []map[string]any{{
		"team_id": model.DefaultTeamID, "name": "deploy", "project_id": "proj",
		"service_account_email": "deployer@proj.iam.gserviceaccount.com", "service_account_count": 3,
		"minter_credential": "gcp-key-minter-deploy", "max_key_age_seconds": 3600, "description": "deploys",
		"created_at": "2026-10-01T00:00:00Z", "updated_at": "2026-10-02T00:00:00Z",
	}})

	store, db := openRealDB(t)
	report, err := staterepo.CheckMigration(ctx, db, dir, t.TempDir(), model.SchemaVersion)
	if err != nil {
		t.Fatalf("CheckMigration of a dump at 22 holding the default team's repo settings: %v", err)
	}
	// These warnings are what `grain state prepare-upgrade` prints.
	assertNoCarriedTableIgnored(t, report.Warnings)

	payments, err := store.GetRepoConfig(ctx, model.RepoRef{Owner: "owner", Name: "payments-api"})
	if err != nil || payments == nil {
		t.Fatalf("owner/payments-api's row did not arrive: %+v, %v", payments, err)
	}
	if !slices.Equal(payments.DefaultCapabilities, []string{"githubtoken", "memory"}) {
		t.Errorf("DefaultCapabilities = %v, want the row's and the team's, unioned", payments.DefaultCapabilities)
	}
	if !slices.Equal(payments.CICapabilities, []string{"gcp-key"}) {
		t.Errorf("CICapabilities = %v, want the row's own, which the team did not override", payments.CICapabilities)
	}
	if payments.SetupCommand != "make team-setup" || payments.CIMode != model.CIModeGrain ||
		payments.GitCredential != "payments-token" || !payments.WatchDefaultBranch || payments.RequireBranchUpToDate {
		t.Errorf("owner/payments-api arrived as %+v", payments)
	}

	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	got, err := store.GetRepoConfig(ctx, widgets)
	if err != nil || got == nil {
		t.Fatalf("acme/widgets, which had no repo_config row, did not arrive: %+v, %v", got, err)
	}
	if got.CloneURL != "https://git.example.com/acme/widgets.git" || got.RepairTemplateID != "widgets-repair" ||
		!got.RequireBranchUpToDate || !slices.Equal(got.CICapabilities, []string{"repo-read:acme/docs"}) ||
		!slices.Equal(got.ReleaseSecrets, []string{"NPM_TOKEN"}) {
		t.Errorf("acme/widgets arrived as %+v", got)
	}
	if cred, err := store.GitConnectionOn(ctx, "", "owner", "payments-api"); err != nil || cred != "payments-token" {
		t.Errorf("owner/payments-api resolves to %q, %v; want its own payments-token", cred, err)
	}

	key, err := store.GetGCPKey(ctx, "deploy")
	if err != nil || key == nil {
		t.Fatalf("the deploy GCP key did not arrive: %v", err)
	}
	if key.ProjectID != "proj" || key.ServiceAccountCount != 3 || key.MaxKeyAge != time.Hour || key.Description != "deploys" {
		t.Errorf("the deploy GCP key arrived as %+v", key)
	}
}

// Another team's rows in either table schema 23 dropped are a
// multi-tenant deployment's, and refuse the dump rather than being left
// behind.
func TestADumpWithAnotherTeamsRepoSettingsOrGCPKeyIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		table, what string
		row         map[string]any
	}{
		{"team_repo_config", "1 repository settings of teams besides the default one (team_repo_config)", map[string]any{
			"team_id": "beta", "owner": "acme", "name": "widgets", "default_capabilities": "memory",
		}},
		{"team_gcp_key", "1 GCP keys of teams besides the default one (team_gcp_key)", map[string]any{
			"team_id": "beta", "name": "deploy", "project_id": "proj", "service_account_email": "sa@proj.iam.gserviceaccount.com",
			"created_at": "2026-10-01T00:00:00Z", "updated_at": "2026-10-01T00:00:00Z",
		}},
	} {
		t.Run(tc.table, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(t.TempDir(), "dump")
			if err := os.CopyFS(dir, os.DirFS(goldenDump(t))); err != nil {
				t.Fatal(err)
			}
			writeTable(t, dir, tc.table, []map[string]any{tc.row})
			_, db := openRealDB(t)
			_, err := staterepo.CheckMigration(ctx, db, dir, t.TempDir(), model.SchemaVersion)
			if !errors.Is(err, model.ErrMultiTenantData) {
				t.Fatalf("CheckMigration of a dump holding another team's %s row: %v, want ErrMultiTenantData", tc.table, err)
			}
			if !strings.Contains(err.Error(), tc.what) {
				t.Errorf("the refusal does not say %q:\n%v", tc.what, err)
			}
			_, db = openRealDB(t)
			if err := staterepo.Import(ctx, db, dir); !errors.Is(err, model.ErrMultiTenantData) {
				t.Errorf("Import of a dump holding another team's %s row: %v, want ErrMultiTenantData", tc.table, err)
			}
		})
	}
}
