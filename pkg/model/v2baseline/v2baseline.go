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

// Package v2baseline is grain 2.0.0's schema, frozen, and the tests that
// hold every later build to opening what 2.0.0 wrote.
//
// From 2.0.0 on, a deployment upgrades in place to every later build
// (model.SchemaVersion's own comment has the policy). What this package
// checks in, under frozen/, was written once, by the build that shipped
// model.V2BaselineSchemaVersion, and is never regenerated:
//
//   - schema.sqlite.sql and schema.postgres.sql: the DDL Init created on
//     each engine at 2.0.0 (model.Statements and postgres.Statements);
//   - rows.sql: every row of a small but real 2.0.0 deployment (Seed), as
//     INSERT statements in the store's own SQL dialect, which the
//     Postgres adapter translates as it does every other statement;
//   - state-repo/: the same deployment's state-repository dump
//     (staterepo.Export), stamped 2.0.0's schema.
//
// Run lays the frozen schema and rows down on an empty database of the
// engine under test, opens it with this build's Store.Init, and checks the
// deployment is still there and the schema is the one Init creates fresh.
// It also imports the frozen dump the way `grain state check -migrate`
// does. pkg/model/sqlite and pkg/model/postgres each call it.
package v2baseline

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// frozen is what 2.0.0 wrote. Never edit or regenerate it: the test
// beside this file pins every file's digest.
//
//go:embed all:frozen
var frozen embed.FS

// FrozenDir is frozen's path inside this package's directory, for the
// generator that wrote it.
const FrozenDir = "frozen"

// Separator ends each statement in the frozen .sql files. A line of its
// own, rather than a bare semicolon, because the statements themselves
// hold semicolons inside quoted text.
const Separator = "\n-- grain:end-of-statement\n"

// File returns one frozen file's contents.
func File(t testing.TB, name string) string {
	t.Helper()
	b, err := frozen.ReadFile(FrozenDir + "/" + name)
	if err != nil {
		t.Fatalf("reading the frozen 2.0 baseline's %s: %v", name, err)
	}
	return string(b)
}

// Split is a frozen .sql file's statements, in order.
func Split(text string) []string {
	var out []string
	for _, stmt := range strings.Split(text, Separator) {
		if strings.TrimSpace(stmt) != "" {
			out = append(out, stmt)
		}
	}
	return out
}

// Join is Split's inverse: the text a frozen .sql file holds.
func Join(stmts []string) string {
	var b strings.Builder
	for _, stmt := range stmts {
		b.WriteString(stmt)
		b.WriteString(Separator)
	}
	return b.String()
}

// An Engine is a store driver under test.
type Engine struct {
	// Schema is the frozen DDL file 2.0.0 created on this engine.
	Schema string
	// Empty opens an empty database, with no schema in it.
	Empty func(t *testing.T) *sql.DB
	// Fresh opens a database this build's Init has created.
	Fresh func(t *testing.T) (*model.Store, *sql.DB)
}

// Run is the whole driver-level test, against e.
func Run(t *testing.T, e Engine) {
	t.Run("a store 2.0.0 wrote opens in this build with its rows", func(t *testing.T) {
		ctx := context.Background()
		db := e.Empty(t)
		want := lay(ctx, t, db, File(t, e.Schema))
		store := model.New(db)
		if err := store.Init(ctx); err != nil {
			t.Fatalf("this build's Init refuses a store grain 2.0.0 wrote: %v\n\n"+
				"From 2.0.0 on every schema bump ships an in-place migration (pkg/model/migrations.go); "+
				"a store a 2.0 deployment wrote must open here, rows and all", err)
		}
		version, err := store.SchemaVersion(ctx)
		if err != nil || version != model.SchemaVersion {
			t.Fatalf("after Init the store is stamped %d (%v), want %d", version, err, model.SchemaVersion)
		}
		tables := tableSet(ctx, t, db)
		for table, n := range want {
			if !tables[table] {
				continue // a later migration dropped it, which is its to say
			}
			if got := count(ctx, t, db, table); got != n {
				t.Errorf("%s holds %d rows after the migration and held %d at 2.0.0", table, got, n)
			}
		}
		CheckDeployment(ctx, t, store)

		// And migrated to the schema a fresh store has, not merely to the
		// stamp: a migration that forgot a column passes the stamp.
		_, fresh := e.Fresh(t)
		if diff := diffSchema(ctx, t, db, fresh); diff != "" {
			t.Errorf("the 2.0.0 store, migrated, is not the schema this build creates fresh:\n%s", diff)
		}
	})
	t.Run("a state repository 2.0.0 dumped imports into this build", func(t *testing.T) {
		ctx := context.Background()
		dir := t.TempDir()
		sub, err := fs.Sub(frozen, FrozenDir+"/state-repo")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.CopyFS(dir, sub); err != nil {
			t.Fatal(err)
		}
		store, db := e.Fresh(t)
		report, err := staterepo.CheckMigration(ctx, db, dir, t.TempDir(), model.SchemaVersion)
		if err != nil {
			t.Fatalf("`grain state check -migrate` refuses the dump grain 2.0.0 wrote: %v\n\n"+
				"A bump the importer does not absorb on its own needs its step in pkg/staterepo's dumpFixups", err)
		}
		if report.SchemaVersion != model.V2BaselineSchemaVersion {
			t.Errorf("the report says the dump is at schema %d, want %d", report.SchemaVersion, model.V2BaselineSchemaVersion)
		}
		CheckDeployment(ctx, t, store)
	})
}

// lay runs a frozen schema and the frozen rows into db, and returns how
// many rows each table holds.
func lay(ctx context.Context, t *testing.T, db *sql.DB, schema string) map[string]int {
	t.Helper()
	for _, stmt := range Split(schema) {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("laying down the 2.0.0 schema: %v\n%s", err, stmt)
		}
	}
	for _, stmt := range Split(File(t, "rows.sql")) {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("laying down the 2.0.0 rows: %v\n%s", err, stmt)
		}
	}
	want := map[string]int{}
	for table := range tableSet(ctx, t, db) {
		want[table] = count(ctx, t, db, table)
	}
	return want
}

// at is when the fixture deployment did everything it did.
var at = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

// Seed builds the deployment the frozen fixtures hold. It ran once, at
// 2.0.0, to write them; CheckDeployment is what it has to read as since.
func Seed(ctx context.Context, store *model.Store) error {
	if err := store.PutCustomField(ctx, model.CustomField{
		Name: "customer", Label: "Customer", Type: model.CustomFieldEnum,
		Options: []string{"acme", "globex"}, Position: 1, Multiple: true, ShowOnRow: true,
	}); err != nil {
		return fmt.Errorf("declaring a custom field: %w", err)
	}
	if err := store.PutTask(ctx, model.Task{
		ID: "v2-1", Intent: model.IntentImplement, Title: "Rename the endpoint",
		Body: "with a body\nover two lines; and a semicolon",
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "owner"}},
			Reason:      model.ReasonDirect,
		},
		Target:       &model.RepoRef{Owner: "owner", Name: "payments-api"},
		Binding:      model.BindingDirective,
		Writes:       []model.WriteTarget{{Repo: model.RepoRef{Owner: "owner", Name: "docs"}, Base: "main"}},
		CustomFields: map[string][]string{"customer": {"acme", "globex"}},
		CreatedAt:    &at,
	}); err != nil {
		return fmt.Errorf("putting a task: %w", err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "v2-1-1", TaskID: "v2-1", Sandbox: "grain-v2-1-1", Attempt: 1, StartedAt: at,
	}, model.Limits{}); err != nil {
		return fmt.Errorf("starting a run: %w", err)
	}
	if err := store.SetRunTranscript(ctx, "v2-1-1", "make test\nok\n"); err != nil {
		return fmt.Errorf("writing a transcript: %w", err)
	}
	if err := store.PutTemplate(ctx, model.Template{
		ID: "v2-tpl", Name: "nightly", Title: "Nightly sweep", Body: "Look for anything rotting.",
		Reads:     []model.RepoRef{{Owner: "owner", Name: "payments-api"}},
		CreatedAt: at,
	}); err != nil {
		return fmt.Errorf("putting a template: %w", err)
	}
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: "v2-sch", Title: "Weekly sweep", Body: "Look again.",
		Target:     model.RepoRef{Owner: "owner", Name: "payments-api"},
		Recurrence: model.Recurrence{Kind: model.RecurrenceWeekly, TimeOfDay: 540, Weekday: time.Monday},
		Overlap:    model.OverlapAllow,
		Enabled:    true, NextRunAt: at, CreatedAt: at,
	}); err != nil {
		return fmt.Errorf("putting a schedule: %w", err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                model.RepoRef{Owner: "owner", Name: "payments-api"},
		DefaultCapabilities: []string{"githubtoken"},
	}); err != nil {
		return fmt.Errorf("putting repo config: %w", err)
	}
	if err := store.SetPipelinePause(ctx, model.PipelinePause{
		PausedAt: at, Reason: "upgrading the sandbox image",
		By: model.Principal{Kind: model.PrincipalHuman, ID: "owner"},
	}); err != nil {
		return fmt.Errorf("pausing: %w", err)
	}
	cfg := model.NewDeploymentConfig()
	if stored, err := store.GetConfig(ctx); err != nil {
		return fmt.Errorf("reading the config: %w", err)
	} else if stored != nil {
		cfg = *stored
	}
	cfg.MaxWorkers = 7
	if err := store.PutConfig(ctx, cfg); err != nil {
		return fmt.Errorf("writing the config: %w", err)
	}
	return nil
}

// CheckDeployment fails t unless store holds what Seed wrote, read
// through the store, which is where a lost row would show.
func CheckDeployment(ctx context.Context, t *testing.T, store *model.Store) {
	t.Helper()
	task, err := store.GetTask(ctx, "v2-1")
	if err != nil || task == nil {
		t.Fatalf("the 2.0.0 deployment's task did not survive: %v", err)
	}
	if task.Title != "Rename the endpoint" || task.Target == nil || task.Target.Name != "payments-api" {
		t.Errorf("the task survived as %q on %v", task.Title, task.Target)
	}
	if got := task.CustomFields["customer"]; !slices.Equal(got, []string{"acme", "globex"}) {
		t.Errorf("the task's field values survived as %v", got)
	}
	if len(task.Writes) != 1 || task.Writes[0].Repo.Name != "docs" {
		t.Errorf("the task's write targets survived as %+v", task.Writes)
	}
	transcript, hasRun, err := store.RunTranscript(ctx, "v2-1", 1)
	if err != nil || !hasRun || transcript != "make test\nok\n" {
		t.Errorf("the run survived as %q (found=%v, %v)", transcript, hasRun, err)
	}
	templates, err := store.ListTemplates(ctx)
	if err != nil || len(templates) != 1 || templates[0].Name != "nightly" {
		t.Errorf("the templates survived as %+v (%v)", templates, err)
	}
	schedules, err := store.ListSchedules(ctx)
	if err != nil || len(schedules) != 1 || schedules[0].Overlap != model.OverlapAllow {
		t.Errorf("the schedules survived as %+v (%v)", schedules, err)
	}
	pause, err := store.GetPipelinePause(ctx)
	if err != nil || pause == nil || pause.Reason != "upgrading the sandbox image" {
		t.Errorf("the pipeline pause survived as %+v (%v)", pause, err)
	}
	cfg, err := store.GetConfig(ctx)
	if err != nil || cfg == nil || cfg.MaxWorkers != 7 {
		t.Errorf("the deployment's config survived as %+v (%v)", cfg, err)
	}
}

// DumpRows renders every row db holds as INSERT statements in the
// store's dialect, joined for a frozen .sql file. The generator's half.
func DumpRows(ctx context.Context, db *sql.DB) (string, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT `name` FROM `sqlite_master` WHERE `type` = 'table' AND `name` NOT LIKE 'sqlite_%' ORDER BY `name`")
	if err != nil {
		return "", err
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return "", err
		}
		tables = append(tables, name)
	}
	rows.Close()
	var stmts []string
	for _, table := range tables {
		got, err := dumpTable(ctx, db, table)
		if err != nil {
			return "", fmt.Errorf("%s: %w", table, err)
		}
		stmts = append(stmts, got...)
	}
	for _, stmt := range stmts {
		if strings.Contains(stmt, strings.TrimSpace(Separator)) {
			return "", fmt.Errorf("a row holds the frozen files' separator: %s", stmt)
		}
	}
	return Join(stmts), nil
}

func dumpTable(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT * FROM `"+table+"`")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = "`" + c + "`"
	}
	var out []string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		lits := make([]string, len(vals))
		for i, v := range vals {
			lit, err := literal(v)
			if err != nil {
				return nil, fmt.Errorf("column %s: %w", cols[i], err)
			}
			lits[i] = lit
		}
		out = append(out, "INSERT INTO `"+table+"` ("+strings.Join(quoted, ", ")+") VALUES ("+strings.Join(lits, ", ")+")")
	}
	return out, rows.Err()
}

func literal(v any) (string, error) {
	switch v := v.(type) {
	case nil:
		return "NULL", nil
	case int64:
		return fmt.Sprint(v), nil
	case float64:
		return fmt.Sprint(v), nil
	case bool:
		if v {
			return "1", nil
		}
		return "0", nil
	case string:
		return "'" + strings.ReplaceAll(v, "'", "''") + "'", nil
	case []byte:
		return "'" + strings.ReplaceAll(string(v), "'", "''") + "'", nil
	case time.Time:
		return "'" + v.UTC().Format("2006-01-02 15:04:05.999999999-07:00") + "'", nil
	}
	return "", fmt.Errorf("no literal for %T", v)
}

// tableSet names the tables db holds, the compatibility view aside.
func tableSet(ctx context.Context, t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	return names(ctx, t, db, "table")
}

func names(ctx context.Context, t *testing.T, db *sql.DB, kind string) map[string]bool {
	t.Helper()
	rows, err := db.QueryContext(ctx, "SELECT `name` FROM `sqlite_master` WHERE `type` = ?", kind)
	if err != nil {
		t.Fatalf("listing %ss: %v", kind, err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(name, "sqlite_") {
			continue
		}
		out[name] = true
	}
	return out
}

func count(ctx context.Context, t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+table+"`").Scan(&n); err != nil {
		t.Fatalf("counting %s: %v", table, err)
	}
	return n
}

// columns names table's columns, in order.
func columns(ctx context.Context, t *testing.T, db *sql.DB, table string) string {
	t.Helper()
	rows, err := db.QueryContext(ctx, "SELECT * FROM `"+table+"` WHERE 1 = 0")
	if err != nil {
		t.Fatalf("reading %s's columns: %v", table, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(cols)
	return strings.Join(cols, ", ")
}

// diffSchema says how got's tables, columns, indexes and views differ
// from want's, or "" when they agree.
func diffSchema(ctx context.Context, t *testing.T, got, want *sql.DB) string {
	t.Helper()
	var b strings.Builder
	for _, kind := range []string{"table", "index", "view"} {
		g, w := names(ctx, t, got, kind), names(ctx, t, want, kind)
		for name := range w {
			if !g[name] {
				fmt.Fprintf(&b, "  missing %s %s\n", kind, name)
			}
		}
		for name := range g {
			if !w[name] {
				fmt.Fprintf(&b, "  extra %s %s\n", kind, name)
			}
		}
		if kind != "table" {
			continue
		}
		for name := range w {
			if !g[name] {
				continue
			}
			if gc, wc := columns(ctx, t, got, name), columns(ctx, t, want, name); gc != wc {
				fmt.Fprintf(&b, "  table %s has columns (%s), want (%s)\n", name, gc, wc)
			}
		}
	}
	return b.String()
}
