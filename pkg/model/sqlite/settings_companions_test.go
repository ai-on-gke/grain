package sqlite_test

// What happens to a template's and a schedule's companion rows when the
// template or the schedule goes (grain/task-380), and the sweep that
// clears up after the builds where the answer was "nothing".

import (
	"context"
	"database/sql"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

// fullTemplate and fullSchedule are one of each with a row in every one
// of its companion tables, which is what makes "every companion table is
// empty afterwards" a check with something to check.
func fullTemplate(id string) model.Template {
	t := template(id)
	t.Reads = []model.RepoRef{{Owner: "owner", Name: "shared-lib"}}
	t.Writes = []model.WriteTarget{
		{Repo: model.RepoRef{Owner: "owner", Name: "deploy-config"}, Base: "deploy"},
	}
	t.Grants = []model.Grant{{Capability: "web-search", Via: model.GrantByLabel}}
	t.Fields = []model.TemplateField{{Name: "package", Label: "Package"}}
	t.CustomFields = map[string][]string{"team": {"infra"}}
	return t
}

func fullSchedule(id string) model.Schedule {
	s := schedule(id, now)
	s.Reads = []model.RepoRef{{Owner: "owner", Name: "shared-lib"}}
	s.Writes = []model.WriteTarget{
		{Repo: model.RepoRef{Owner: "owner", Name: "deploy-config"}, Base: "deploy"},
	}
	s.Grants = []model.Grant{{Capability: "web-search", Via: model.GrantByLabel}}
	s.CustomFields = map[string][]string{"team": {"infra"}}
	return s
}

// companionTables asks the database itself which tables are companions
// of prefix -- every table named `prefix_something` that has an idColumn
// -- rather than being told. A companion table added later is then
// covered by these tests on the day it is added, which is the whole
// failure mode here: the rule is kept by hand (nothing in this schema
// carries a foreign key), so it is a rule a new table silently escapes.
//
// GLOB rather than LIKE because LIKE's `_` is a wildcard: `schedule_*`
// matches schedule_read and not scheduled_upgrade. The id column is what
// separates a companion from a sequence table (schedule_sequence is an
// id allocator and belongs to no schedule).
func companionTables(t *testing.T, ctx context.Context, db *sql.DB, prefix, idColumn string) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx,
		"SELECT `name` FROM `sqlite_master` WHERE `type` = 'table' AND `name` GLOB ? ORDER BY `name`",
		prefix+"_*")
	if err != nil {
		t.Fatalf("listing %s tables: %v", prefix, err)
	}
	var named []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatalf("scanning a table name: %v", err)
		}
		named = append(named, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("listing %s tables: %v", prefix, err)
	}

	var out []string
	for _, name := range named {
		columns, err := db.QueryContext(ctx, "PRAGMA table_info(`"+name+"`)")
		if err != nil {
			t.Fatalf("reading the columns of %s: %v", name, err)
		}
		has := false
		for columns.Next() {
			var cid int
			var colName, colType string
			var notNull int
			var dflt sql.NullString
			var pk int
			if err := columns.Scan(&cid, &colName, &colType, &notNull, &dflt, &pk); err != nil {
				columns.Close()
				t.Fatalf("scanning the columns of %s: %v", name, err)
			}
			if colName == idColumn {
				has = true
			}
		}
		columns.Close()
		if has {
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		t.Fatalf("found no companion tables of %s keyed by %s, which cannot be right", prefix, idColumn)
	}
	return out
}

// rowsLeft is how many rows of table name id.
func rowsLeft(t *testing.T, ctx context.Context, db *sql.DB, table, idColumn, id string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `"+table+"` WHERE `"+idColumn+"` = ?", id).Scan(&n); err != nil {
		t.Fatalf("counting %s rows of %s: %v", table, id, err)
	}
	return n
}

func TestDeleteTemplateRemovesItsCompanionRowsToo(t *testing.T) {
	t.Parallel()
	// Until grain/task-380 it removed the `template` row alone, so every
	// read, write, grant, field and custom-field value of every template
	// anybody ever deleted stayed in the database for good: nothing reads
	// them (every read joins from the parent) and nothing else would ever
	// delete them.
	store, db, ctx := openStore(t)
	if err := store.PutTemplate(ctx, fullTemplate("template-1")); err != nil {
		t.Fatalf("put: %v", err)
	}
	// A second template, to pin that the delete is keyed on the id: the
	// bug this replaces would have been just as well hidden by a delete
	// that took everything.
	if err := store.PutTemplate(ctx, fullTemplate("template-2")); err != nil {
		t.Fatalf("putting the template that stays: %v", err)
	}
	if err := store.DeleteTemplate(ctx, "template-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	for _, table := range companionTables(t, ctx, db, "template", "template_id") {
		if n := rowsLeft(t, ctx, db, table, "template_id", "template-1"); n != 0 {
			t.Errorf("%s: %d rows left behind by deleting template-1, want 0", table, n)
		}
		if n := rowsLeft(t, ctx, db, table, "template_id", "template-2"); n != 1 {
			t.Errorf("%s: %d rows of template-2, want the 1 it was saved with", table, n)
		}
	}
}

func TestDeleteScheduleRemovesItsCompanionRowsToo(t *testing.T) {
	t.Parallel()
	// DeleteTemplate's own reasoning, one kind over.
	store, db, ctx := openStore(t)
	if err := store.PutSchedule(ctx, fullSchedule("sched-1")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := store.PutSchedule(ctx, fullSchedule("sched-2")); err != nil {
		t.Fatalf("putting the schedule that stays: %v", err)
	}
	if err := store.DeleteSchedule(ctx, "sched-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	for _, table := range companionTables(t, ctx, db, "schedule", "schedule_id") {
		if n := rowsLeft(t, ctx, db, table, "schedule_id", "sched-1"); n != 0 {
			t.Errorf("%s: %d rows left behind by deleting sched-1, want 0", table, n)
		}
		if n := rowsLeft(t, ctx, db, table, "schedule_id", "sched-2"); n != 1 {
			t.Errorf("%s: %d rows of sched-2, want the 1 it was saved with", table, n)
		}
	}
}

func TestDeleteOrphanedSettingsRowsSweepsWhatOlderBuildsLeft(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	// The orphans are written straight into the tables rather than made
	// by deleting something, since making them by deleting something is
	// exactly what no longer works. This is the database an older build
	// hands over.
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO `template_grant` (`template_id`,`capability`,`via`,`folder`) VALUES (?,?,?,?)",
			[]any{"template-gone", "web-search", "label", nil}},
		{"INSERT INTO `template_read` (`template_id`,`owner`,`name`) VALUES (?,?,?)",
			[]any{"template-gone", "owner", "shared-lib"}},
		{"INSERT INTO `schedule_read` (`schedule_id`,`owner`,`name`) VALUES (?,?,?)",
			[]any{"sched-gone", "owner", "shared-lib"}},
		{"INSERT INTO `schedule_custom_field` (`schedule_id`,`name`,`value`) VALUES (?,?,?)",
			[]any{"sched-gone", "team", "infra"}},
		{"INSERT INTO `suite_item` (`suite_id`,`template_id`,`order_key`) VALUES (?,?,?)",
			[]any{"suite-gone", "template-1", 1.0}},
	} {
		if _, err := db.ExecContext(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("writing an orphaned row: %v", err)
		}
	}
	// Alongside a template that still exists, whose rows the sweep must
	// not touch: the whole risk in running a delete nobody asked for is
	// that it is keyed on something other than "the parent is gone".
	if err := store.PutTemplate(ctx, fullTemplate("template-1")); err != nil {
		t.Fatalf("putting the template that stays: %v", err)
	}

	swept, err := store.DeleteOrphanedSettingsRows(ctx)
	if err != nil {
		t.Fatalf("sweeping: %v", err)
	}
	want := model.OrphanedSettingsRows{
		"template_grant": 1, "template_read": 1,
		"schedule_read": 1, "schedule_custom_field": 1,
		"suite_item": 1,
	}
	if len(swept) != len(want) {
		t.Fatalf("swept %v, want %v", swept, want)
	}
	for table, n := range want {
		if swept[table] != n {
			t.Errorf("swept[%s] = %d, want %d (all of it: %v)", table, swept[table], n, swept)
		}
	}
	if swept.Total() != 5 {
		t.Errorf("Total() = %d, want 5", swept.Total())
	}
	if got := swept.String(); got != "schedule_custom_field 1, schedule_read 1, suite_item 1, "+
		"template_grant 1, template_read 1" {
		t.Errorf("String() = %q, want the tables and their counts in table order", got)
	}

	for _, table := range companionTables(t, ctx, db, "template", "template_id") {
		if n := rowsLeft(t, ctx, db, table, "template_id", "template-gone"); n != 0 {
			t.Errorf("%s: %d orphaned rows survived the sweep, want 0", table, n)
		}
		if n := rowsLeft(t, ctx, db, table, "template_id", "template-1"); n != 1 {
			t.Errorf("%s: %d rows of the template that still exists, want the 1 it was saved with", table, n)
		}
	}
	got, err := store.GetTemplate(ctx, "template-1")
	if err != nil || got == nil {
		t.Fatalf("getting the template that stays: (%+v, %v)", got, err)
	}
	if len(got.Grants) != 1 || len(got.Reads) != 1 || len(got.Fields) != 1 {
		t.Errorf("the surviving template came back as %+v, want its companion rows intact", *got)
	}

	// Idempotent, and silent on a database with nothing to sweep: the
	// sweep runs on every store open, and an operator reads a line from
	// it only when it did something.
	again, err := store.DeleteOrphanedSettingsRows(ctx)
	if err != nil {
		t.Fatalf("sweeping a second time: %v", err)
	}
	if again.Total() != 0 || len(again) != 0 {
		t.Errorf("a second sweep found %v, want nothing left", again)
	}
	if again.String() != "" {
		t.Errorf("String() of an empty sweep = %q, want %q", again.String(), "")
	}
}

func TestDeleteOrphanedSettingsRowsLeavesAHealthyStoreAlone(t *testing.T) {
	t.Parallel()
	// The case that matters most, since this runs unattended at every
	// store open: a database where every companion row has its parent
	// comes out of the sweep untouched.
	store, _, ctx := openStore(t)
	if err := store.PutTemplate(ctx, fullTemplate("template-1")); err != nil {
		t.Fatalf("putting a template: %v", err)
	}
	if err := store.PutSchedule(ctx, fullSchedule("sched-1")); err != nil {
		t.Fatalf("putting a schedule: %v", err)
	}
	swept, err := store.DeleteOrphanedSettingsRows(ctx)
	if err != nil {
		t.Fatalf("sweeping: %v", err)
	}
	if swept.Total() != 0 {
		t.Fatalf("swept %v from a store with nothing orphaned, want nothing", swept)
	}
	tpl, err := store.GetTemplate(ctx, "template-1")
	if err != nil || tpl == nil || len(tpl.Grants) != 1 || len(tpl.Fields) != 1 {
		t.Fatalf("template after the sweep: (%+v, %v)", tpl, err)
	}
	sched, err := store.GetSchedule(ctx, "sched-1")
	if err != nil || sched == nil || len(sched.Grants) != 1 || len(sched.Reads) != 1 {
		t.Fatalf("schedule after the sweep: (%+v, %v)", sched, err)
	}
}
