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

package staterepo

// check.go answers the one question a pull request against a state
// repository cannot answer by being read: will grain be able to load
// this?
//
// Import is already the validator -- it does the whole load in one
// transaction with foreign keys deferred and rolls the lot back on any
// inconsistency -- but until now the only thing that ever ran it was a
// daemon starting up, which is the worst possible place to find out that
// a merged change dropped a NOT NULL column or left a task_link pointing
// at a task that is no longer in the file. By then the change is merged,
// the daemon is the thing that is down, and the person who has to fix it
// is whoever is on call rather than whoever wrote the diff.
//
// So Check runs that same import against a database the caller is
// throwing away afterwards, and reports what broke in terms of the file
// and the row it broke on. It is meant to be a CI step in the state
// repository itself (`grain state check .`), where it fails the pull
// request instead of the deployment.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/trustboundary"
)

// CheckReport is what a check found: enough to print a summary a human
// can read at the bottom of a CI log, and the warnings that are worth
// saying but not worth failing over.
type CheckReport struct {
	// SchemaVersion is the version the dump is stamped with.
	SchemaVersion int
	// Rows is how many rows each table's file loaded, for the tables that
	// had any. A table with no rows is left out rather than recorded as
	// zero: the interesting number in a CI log is what arrived.
	Rows map[string]int
	// Warnings are things a reviewer should know that do not stop the
	// dump loading -- a file naming a table this build does not have, a
	// row carrying a column it does not have, a column holding a word it
	// does not have (vocabulary.go). All three are what a dump from a
	// different build looks like, which is legitimate (Import skips them
	// deliberately), and all three are also what a typo in a hand-edited
	// file looks like, which is not.
	//
	// And one that is about what loading it *does* rather than what it
	// would ignore: a settings row that does not carry a column this
	// build has, which Apply resets to its default on the next tick
	// (settingsResets) -- or, where the column has no default to be reset
	// to, which nothing can load at all, said as the different thing it
	// is rather than as a reset that will never happen.
	Warnings []string
}

// Total is how many rows the whole dump loaded.
func (r CheckReport) Total() int {
	n := 0
	for _, count := range r.Rows {
		n += count
	}
	return n
}

// Tables names the tables that loaded rows, sorted.
func (r CheckReport) Tables() []string {
	names := make([]string, 0, len(r.Rows))
	for name := range r.Rows {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Check loads dir into db and reports whether it worked.
//
// db must be an empty, throwaway database at this build's own schema:
// Import replaces every row in whatever it is given, so handing it the
// database a deployment is running on would be a check that costs the
// deployment its state. The caller opens it because opening a database is
// pkg/model/sqlite's business and this package deliberately depends on no
// driver (see that package's own doc comment).
//
// version is the schema this build knows -- model.SchemaVersion at every
// real call site. A dump stamped with anything else is refused here for
// the same reason Load refuses it: the rows are shaped for a schema this
// binary does not have, so "it imported" would not mean what a reader of
// a green check would take it to mean.
func Check(ctx context.Context, db *sql.DB, dir string, version int) (CheckReport, error) {
	return check(ctx, db, dir, "", version, false)
}

// CheckMigration is Check for a dump at a schema this build does not
// share: it rehearses the migration (migrate.go) rather than refusing
// the stamp, and reports what would arrive.
//
// It is what the Upgrade button runs, inside the image it is about to
// cut over to, before it cuts over to anything (pkg/upgrade's own
// schema step). That ordering is the whole point. A new image is
// health-checked today by running `grain schema-version` in it, which
// proves the build works and proves nothing about this deployment's
// data -- so a release that changed the schema passed every check and
// then failed against the one database that mattered, in a restart loop
// systemd repeated forever. This asks the question the health check
// cannot: not "does this build run", but "does this build run on these
// rows".
//
// scratch is somewhere to write a transformed copy of the dump if the
// migration needs one (MigrateDump); it may be empty when no fixup lies
// between the two schemas, and a caller that cannot know that should
// pass a temporary directory it removes afterwards.
func CheckMigration(ctx context.Context, db *sql.DB, dir, scratch string, version int) (CheckReport, error) {
	return check(ctx, db, dir, scratch, version, true)
}

func check(ctx context.Context, db *sql.DB, dir, scratch string, version int, migrating bool) (CheckReport, error) {
	report := CheckReport{Rows: map[string]int{}}
	// One connection, with foreign keys on it, so that the import runs
	// under the constraints a daemon's would. pkg/model/sqlite enables
	// the pragma with a single Exec, which reaches one pooled connection
	// rather than every connection database/sql may later open -- so a
	// check that happened to import on another one would be quietly
	// laxer than the load it stands in for. The schema declares no
	// foreign keys today (Import defers them anyway, for the day it
	// does), which is precisely why this is worth pinning here rather
	// than left to be noticed when the first one is added.
	//
	// Mutating the caller's *sql.DB is fair here for the reason the doc
	// comment above gives: it is a database the caller is throwing away.
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return report, fmt.Errorf("staterepo: enabling foreign keys for the check: %w", err)
	}
	if !HasDump(dir) {
		return report, fmt.Errorf("staterepo: %s holds no dump: there is no %s/ directory with table files in it",
			dir, TablesDir)
	}
	found, err := ReadSchemaVersion(dir)
	if err != nil {
		return report, err
	}
	report.SchemaVersion = found
	switch {
	case found == version:
	case !migrating && found > version:
		return report, fmt.Errorf("%w: repository is at schema %d, this build knows %d", ErrSchemaTooNew, found, version)
	case !migrating:
		return report, fmt.Errorf("%w: repository is at schema %d, this build knows %d", ErrSchemaTooOld, found, version)
	default:
		migrated, err := MigrateDump(dir, scratch, found, version)
		if err != nil {
			return report, err
		}
		// Whatever MigrateDump handed back is what Import would be given
		// for real, which is the thing being rehearsed: dir itself when
		// the importer's own tolerance is the whole migration, and the
		// transformed copy when it is not.
		dir = migrated
	}
	tables, err := tableNames(ctx, db)
	if err != nil {
		return report, err
	}
	if err := checkConfigPolicy(dir); err != nil {
		return report, err
	}
	warnings, err := describeDump(ctx, db, dir, tables)
	if err != nil {
		return report, err
	}
	report.Warnings = warnings

	if err := Import(ctx, db, dir); err != nil {
		return report, err
	}
	// A migration is an upgrade being rehearsed, and the build being
	// upgraded to refuses a multi-tenant deployment's rows at its first
	// start (model.Store.CheckSingleTenant). Refusing them here instead is
	// what leaves that deployment on the build it runs.
	if migrating {
		if err := model.New(db).CheckSingleTenantIn(ctx, model.StateRepository); err != nil {
			return report, err
		}
	}
	for _, t := range tables {
		var n int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quote(t)).Scan(&n); err != nil {
			return report, fmt.Errorf("staterepo: counting %s: %w", t, err)
		}
		if n > 0 {
			report.Rows[t] = n
		}
	}
	return report, nil
}

// describeDump reads the dump the way Import will and says what Import
// would silently pass over. Import is right to skip a file or a column
// this build does not have -- that is a dump written by a build one
// version along, and refusing it would make every upgrade a flag day --
// but a check that said nothing about it would let a misspelt table name
// through as a change that "loads fine" and does nothing at all.
//
// The same argument one level in, for a column whose value is a word out
// of a closed vocabulary rather than free text, is vocabulary.go.
//
// It also says the two things a *complete* load costs, in the one place
// they cost anything: a column a settings file leaves out, which Apply
// resets rather than leaves alone (settingsResets, below), and a
// settings file that is not there at all, which Apply empties the table
// for (settingsMissing).
func describeDump(ctx context.Context, db *sql.DB, dir string, tables []string) ([]string, error) {
	known := map[string]bool{}
	for _, t := range tables {
		known[t] = true
	}
	settings := map[string]bool{}
	for _, t := range SettingsTables {
		settings[t] = true
	}
	entries, err := os.ReadDir(filepath.Join(dir, TablesDir))
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("staterepo: reading %s: %w", filepath.Join(dir, TablesDir), err)
	}
	var warnings []string
	present := map[string]bool{}
	// The three settings kinds are not files of rows: they are nested
	// documents, one per kind, carrying the kind's own table and its
	// companions (decl.go). Read first, so that a legacy file left beside
	// one is reported as the ignored file it is rather than checked as
	// if it were what would be imported.
	nested := map[string]bool{}
	for _, k := range declKinds() {
		if _, err := os.Stat(filepath.Join(dir, SettingsDir, k.file)); err != nil {
			continue
		}
		for _, table := range k.Tables() {
			nested[table] = true
			present[table] = true
			if !known[table] {
				continue
			}
			rows, _, err := dumpRowsOf(dir, table)
			if err != nil {
				return nil, err
			}
			said, err := describeTable(ctx, db, k.Path(), table, rows, settings[table])
			if err != nil {
				return nil, err
			}
			warnings = append(warnings, said...)
		}
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		table := strings.TrimSuffix(e.Name(), ".json")
		path := filepath.Join(TablesDir, e.Name())
		if nested[table] {
			warnings = append(warnings, fmt.Sprintf(
				"%s is left over from before %s carried %s; every row in it would be ignored, "+
					"and the next export removes it", path, DumpPath(table), table))
			continue
		}
		present[table] = true
		if !known[table] {
			warnings = append(warnings, fmt.Sprintf(
				"%s names no table this build has; every row in it would be ignored", path))
			continue
		}
		rows, err := readRows(filepath.Join(dir, TablesDir, e.Name()))
		if err != nil {
			return nil, err
		}
		said, err := describeTable(ctx, db, path, table, rows, settings[table])
		if err != nil {
			return nil, err
		}
		warnings = append(warnings, said...)
	}
	warnings = append(warnings, settingsMissing(tables, settings, present, nested)...)
	sort.Strings(warnings)
	return warnings, nil
}

// describeTable is describeDump for the rows of one table, wherever in
// the repository they were written down.
func describeTable(ctx context.Context, db *sql.DB, path, table string, rows []map[string]any, isSettings bool) ([]string, error) {
	cols, err := columns(ctx, db, table)
	if err != nil {
		return nil, err
	}
	hasColumn := map[string]bool{}
	for _, c := range cols {
		hasColumn[c.name] = true
	}
	var warnings []string
	// Reported once per column rather than once per row: a column
	// name that is wrong is wrong in every row of the file, and a
	// warning per row would bury the one line that matters.
	seen := map[string]bool{}
	for i, row := range rows {
		for name := range row {
			if hasColumn[name] || seen[name] {
				continue
			}
			seen[name] = true
			warnings = append(warnings, fmt.Sprintf(
				"%s row %d has a column %q that %s does not; it would be ignored", path, i, name, table))
		}
	}
	if isSettings {
		warnings = append(warnings, settingsResets(path, table, withoutWithheld(table, cols), rows)...)
		warnings = append(warnings, withheldCarried(path, table, rows)...)
	}
	// And the values, for the tables whose columns hold a closed
	// vocabulary: a word this build does not have is the same silent
	// pass-over as a column it does not have, one level in
	// (vocabulary.go).
	return append(warnings, vocabularyWarnings(path, table, rows)...), nil
}

// withheldCarried warns about a settings file that names a column
// withheldColumns keeps out of the repository. Applying it ignores the
// value (withheld.go), so the warning says that -- and, since the value
// is now in this repository's history, that it should be treated as
// leaked. The value itself is not repeated.
func withheldCarried(path, table string, rows []map[string]any) []string {
	var out []string
	for _, name := range withheldColumns[table] {
		for i, row := range rows {
			if _, ok := row[name]; !ok {
				continue
			}
			out = append(out, fmt.Sprintf(
				"%s row %d carries %q, which holds secret material and is never imported from this "+
					"repository; applying this keeps the value the running database holds. Remove the "+
					"column from the file, set the value through the settings API instead, and rotate "+
					"it if it was ever committed here",
				path, i, name))
			break
		}
	}
	return out
}

// settingsMissing is settingsResets one level up: a settings table this
// build has that the dump holds no file for at all.
//
// Import treats such a table as empty on purpose (importTable), and for
// a whole-database load that is the right answer -- it is what a
// repository written before this build gained the table looks like, and
// an empty table is what that deployment had. Apply is not a
// whole-database load. It clears the tables it is given and refills them
// from the dump (importInto), so a settings table with no file is not
// left alone: it is emptied, on the next tick, with nothing in the diff
// to say so -- the loss is the file that is not there.
//
// The window that produces one is narrow and real, and it is exactly the
// window settingsResets' own doc comment describes, widened from a
// column to a table: a pull request branched before an upgrade that
// added the table, merged after it, on a deployment that has since
// created a board (or declared a field, or given a template a second
// write target) through the UI. The dump those rows would have been
// exported into is not in the branch, and merging deletes them.
//
// A warning and not a failure, for settingsResets' own three reasons:
// this runs against the caller's throwaway database and so cannot know
// whether the running one holds anything to lose, a pre-upgrade branch
// is a legitimate thing to open, and the same code runs inside a new
// image before pkg/upgrade cuts over to it, where failing would block
// the upgrade that adds the table on the ground that the table is about
// to be added.
// nested names the tables a settings document in this dump carries. It
// is what decides which file the warning is about: a repository this
// build wrote holds the document, and one written before it holds a file
// per table, and naming the wrong one sends whoever reads the warning to
// a file that is not in their pull request.
func settingsMissing(tables []string, settings, present, nested map[string]bool) []string {
	var out []string
	for _, table := range tables {
		if !settings[table] || present[table] {
			continue
		}
		path := filepath.Join(TablesDir, table+".json")
		if nested[table] {
			path = filepath.FromSlash(DumpPath(table))
		}
		out = append(out, fmt.Sprintf(
			"%s is not in this dump, and %s is a settings table this build has; applying this empties "+
				"that table rather than leaving it alone, so every row the running database holds in it "+
				"would be deleted",
			path, table))
	}
	return out
}

// settingsResets is the other direction, for the tables where it costs
// something: a column the *file* does not mention, in a table Apply
// imports into a running daemon.
//
// Everywhere else that is not news and is deliberately not warned about.
// A file missing a column is what every dump written before that column
// existed looks like -- a column added with a default does not move
// model.SchemaVersion -- so warning about all of them would put a
// warning on every deployment's first pull request after an upgrade, for
// a load where the database supplying the default is the right answer
// and nothing is lost.
//
// In a settings table it is not the database supplying a default, it is
// the database being overwritten with one. Apply replaces whole rows
// (SettingsTables' own doc comment, and
// TestApplyingASettingsFileWithoutAColumnResetsItToItsDefault), so a
// merged grain_config.json branched before time_zone existed does not
// leave time_zone alone: it resets it on the next tick, moves every
// timestamp in the UI and the firing time of every daily, weekly and
// monthly schedule with it, and the export after that writes the revert
// back out. Nobody reading the diff would see that, because the loss is
// in the lines the diff does not have. So the check says it.
//
// What it cannot say is what would actually be lost. This runs against
// the throwaway database the caller opened, not the deployment's, so it
// knows the value the column would land on and not the one it would land
// on *instead of* -- hence "rather than on whatever the running database
// holds" rather than a before-and-after a reviewer would rightly expect
// to be real. The deployment says that half itself, once the merge
// reaches it: applying a settings file says what it replaced and what it
// replaced it with (resets.go), and says it back on this same pull
// request when it can reach it, under the warning this left. Before the
// fact from here, after the fact from there, and neither can do the
// other's half.
//
// A warning and not a failure, including for the columns whose default
// means something. Three reasons, in order of how much they decide it:
// the same not-knowing above, so a failure would red-tick a change that
// loses nothing at all -- a pull request against a deployment whose
// time_zone *is* the default, or a row the file is adding, which has no
// database side to lose; the pre-upgrade branch is a legitimate thing to
// open, and its author often cannot know the running value to write in;
// and this same code is what `grain state check -migrate` runs inside a
// new image before pkg/upgrade cuts over to it, where failing would
// block the upgrade that adds the column on the ground that the column
// is about to be added. The reviewer decides, which is what a warning is
// for.
//
// All of which is true of a column that has a default to land on, and
// none of it is true of one that has not. A column declared NOT NULL
// with no default is not reset by an omission -- the insert has nothing
// to write and SQLite refuses the row, so the dump does not load and no
// deployment ever reaches the question. Those get a sentence of their
// own (mustBeGiven), because the two are opposite advice: one says weigh
// what the merge would cost, the other says this will not merge.
func settingsResets(path, table string, cols []column, rows []map[string]any) []string {
	var out []string
	// One warning per column, naming the first row that is missing it,
	// for the reason the loop above reports per column: a settings file
	// written before a column existed is missing it in every row, and the
	// news is the column.
	for _, c := range cols {
		for i, row := range rows {
			if _, ok := row[c.name]; ok {
				continue
			}
			if mustBeGiven(c, cols) {
				out = append(out, fmt.Sprintf(
					"%s row %d does not carry %q, which %s declares NOT NULL with no default; there is "+
						"no value for it to land on, so this row cannot be imported at all -- the import "+
						"below refuses it rather than resetting anything, and the fix is to name the "+
						"column in the file",
					path, i, c.name, table))
				break
			}
			out = append(out, fmt.Sprintf(
				"%s row %d does not carry %q, which %s has; %s is a settings table, so applying this "+
					"replaces the whole row and that column lands on its default %s rather than on "+
					"whatever the running database holds",
				path, i, c.name, table, table, defaultLiteral(c)))
			break
		}
	}
	return out
}

// mustBeGiven reports whether leaving c out of a row is refused rather
// than defaulted: NOT NULL, with no DEFAULT for the insert to fall back
// on. importTable leaves an absent column out of the INSERT's column
// list entirely, so such a column is not written as NULL -- SQLite has
// nothing to write at all, and the whole import fails on the NOT NULL
// constraint.
//
// Most of grain_config's original columns are in this position today --
// poll_interval_ms, max_agent_turns, github_host,
// target_repos and the rest of the ones the first schema declared, none
// of which was ever given a default -- and for them the reset warning
// was not merely imprecise but false: it promised a default the schema
// then refused, one line above the error refusing it. Nothing was ever
// lost by that, since the check fails, in CI, before any deployment
// takes the file up. But the check is where somebody looks first, and a
// warning that describes a load that cannot happen sends them looking
// for the wrong thing.
//
// The INTEGER PRIMARY KEY is the exception and has to be excluded by
// hand: it is NOT NULL with no default and SQLite still fills it in,
// because it is an alias for the rowid and an omitted rowid is
// generated. Warning that such a row "cannot be imported at all" would
// be the same false promise in the other direction. It falls through to
// the reset warning above, which is not exactly right about it either --
// see the note there -- but is at least not a claim about a failure that
// will not happen.
func mustBeGiven(c column, cols []column) bool {
	if !c.notNull || c.dflt.Valid {
		return false
	}
	return !rowIDAlias(c, cols)
}

// rowIDAlias reports whether c is its table's INTEGER PRIMARY KEY, in
// the strict sense SQLite means by that: the whole of a single-column
// primary key, declared with the type INTEGER and no other spelling of
// it, on a table with a rowid. A composite key is not one, and neither
// is a key column declared BIGINT or INT.
//
// The schema declares no WITHOUT ROWID table (pkg/model/schema.go), and
// PRAGMA table_info does not say whether a table has a rowid, so that
// half of the rule is taken on the schema's word rather than asked. A
// WITHOUT ROWID settings table arriving here would make this report an
// alias where there is none, and turn a correct refusal warning back
// into today's misleading reset one.
func rowIDAlias(c column, cols []column) bool {
	if c.pk != 1 || !strings.EqualFold(strings.TrimSpace(c.declType), "INTEGER") {
		return false
	}
	for _, other := range cols {
		if other.name != c.name && other.pk > 0 {
			return false
		}
	}
	return true
}

// defaultLiteral renders a column's default as the schema spells it: a
// quoted 'America/Los_Angeles', a bare 0, an empty pair of quotes -- and
// NULL for a column that declares none, since NULL is what such a column
// lands on.
//
// The literal rather than the value it evaluates to. This package runs
// none of the schema's expressions, and the text SQLite hands back is
// the text of the CREATE TABLE, so a reviewer who goes looking for where
// the warning's value comes from is comparing the same characters.
func defaultLiteral(c column) string {
	if !c.dflt.Valid {
		return "NULL"
	}
	return c.dflt.String
}

// readRows parses one table file, with the same decoder Import uses so
// that a file this rejects is exactly a file Import would reject.
func readRows(path string) ([]map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("staterepo: reading %s: %w", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var rows []map[string]any
	if err := dec.Decode(&rows); err != nil {
		return nil, fmt.Errorf("staterepo: parsing %s: %w", path, err)
	}
	return rows, nil
}

// checkConfigPolicy validates settings/config-policy.yml when present in the
// dump directory, failing on invalid YAML syntax, unknown fields, malformed
// globs, or any auto_merge_paths entry that matches or overlaps
// trustboundary.NeverAutoMergePaths.
func checkConfigPolicy(dir string) error {
	p := filepath.Join(dir, filepath.FromSlash(trustboundary.ConfigPolicyPath))
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("staterepo: reading %s: %w", trustboundary.ConfigPolicyPath, err)
	}
	if _, err := trustboundary.CheckConfigPolicy(data); err != nil {
		return fmt.Errorf("staterepo: %w", err)
	}
	return nil
}
