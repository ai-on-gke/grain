package staterepo

// replaced.go says what a settings import overwrote.
//
// resets.go is the half next to this one and is not it. That one reports
// what a merged *file* leaves out -- a column the proposal did not carry,
// which the import lands on the schema's default -- and it can only see a
// loss the file itself is shaped like. The other way a value goes is a
// column the file does carry: the dump is whatever the last export wrote,
// so a file merged from it holds this deployment's settings as they were
// at that moment, and importing it replaces whatever the database has
// done since. On a tick the only visible trace of that in the journal was
// the setting somebody changed on purpose. Everything else the import
// happened to undo went unsaid.
//
// That is a real thing to be undone by rather than a theoretical one. A
// deployment is ahead of its own dump between exports by construction,
// and `grain repo-case -apply` is the case that was driven end to end
// (docs/hand-runs/2026-09-07-repo-case.md, finding 2): a reconciliation of
// every divergent repo-name spelling in the database, applied while an
// ordinary settings pull request had merged in between, was half undone on
// the next start -- the merged repo_config split in two again, the
// qualification plan with it, the templates back under the old spelling,
// and the schedule that fires one of them filing fresh tasks under that
// spelling within the minute. `repo-case` had exited successfully some
// minutes earlier and nothing said any of it had happened.
//
// So every import says what it replaced: one journal line naming the
// settings tables it touched and, row by row, what this database held and
// what arrived instead. `#1094` made `repo-case -apply` export before it
// exits, which closes that one route; this is the general fact, since
// `repo-case` is unlikely to be the only way a deployment gets ahead of
// its own dump.
//
// A notice, and deliberately not a refusal. The repository is
// authoritative for settings and the database is authoritative for what
// grain itself did (bind.go's SettingsTables and Load), and this is that
// line working as designed: the file is what this repository says the
// settings are, and a host that refused an import because one of its own
// rows had moved would sit at a commit it has not taken up and stop every
// later settings change from arriving until somebody went to the host --
// the same reason resets.go gives for reporting rather than refusing. What
// was missing was not a stronger rule but anybody being told.
//
// Journal only, unlike a reset. A reset is something lost that nobody
// asked to lose, so it is worth waking Config.AnnounceResets and the pull
// request the merge came in on; this line fires on every settings import
// that changes anything at all, which is every ordinary merge, and a
// comment on each of them would be a surface nobody reads. The journal is
// where an operator looks when a setting is not what they left it as, and
// that is the question this answers.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
)

// replacedValue is one column of one row whose stored value the arriving
// file replaced with a different one.
//
// Both sides are rendered the way resets.go renders its own (literal),
// so the two notices about one import read in one notation.
type replacedValue struct {
	column string
	from   string
	to     string
}

// replacedRow is what a settings import did to one row, in the three
// shapes that are all a stored value going.
//
// A row both sides hold, whose columns differ, is the plain case. A row
// the file does not hold at all is the same event written larger:
// ImportTables clears the table and refills it, so the row is simply
// gone. And a row the database does not hold is on this list too, because
// a row *coming back* is what an undone change looks like from here --
// the repo_config the reconciliation merged away arrives again as an
// addition, and a line that named only the changes would tell an operator
// half of what happened to their settings.
type replacedRow struct {
	// path is the file that carried it, relative to the repository root,
	// which names the table as well -- the same field Reset.Path is.
	path string
	// row names it by its primary key ("owner = 'bwsalmon', repo =
	// 'grain'"), since a settings table has many rows and this line is
	// about which of them moved.
	row string
	// values is the columns that differ, for a row both sides hold. Empty
	// for the two below.
	values []replacedValue
	// moreValues counts the columns past replacedColumnLimit that this
	// line does not have room to name.
	moreValues int
	added      bool
	removed    bool
}

func (r replacedRow) String() string {
	switch {
	case r.added:
		return fmt.Sprintf("%s (%s): the file holds this row and this database did not, so it is here now", r.path, r.row)
	case r.removed:
		return fmt.Sprintf("%s (%s): the file does not hold this row, so it is gone", r.path, r.row)
	}
	parts := make([]string, 0, len(r.values)+1)
	for _, v := range r.values {
		parts = append(parts, fmt.Sprintf("%s was %s and is now %s", v.column, v.from, v.to))
	}
	if r.moreValues > 0 {
		parts = append(parts, fmt.Sprintf("and %d other %s of this row",
			r.moreValues, plural(r.moreValues, "column", "columns")))
	}
	return fmt.Sprintf("%s (%s): %s", r.path, r.row, strings.Join(parts, ", "))
}

// replacedByApplying reports what importing dir's settings tables into db
// would overwrite: the rows db holds that the files do not, the rows the
// files hold that db does not, and the columns the two disagree about.
//
// Read before ImportTables, for resetsFromApplying's reason: afterwards
// the database holds the file's version and nothing can say what was
// there.
//
// Columns the file does not mention are not compared here at all. Those
// are resets.go's -- it says which of them cost something and which were
// already sitting on the default the import will land them on, which is a
// question this comparison has no way to ask -- and reporting them twice
// about one import would put the same value in one journal line in two
// different sentences.
func replacedByApplying(ctx context.Context, db querier, dir string) ([]replacedRow, error) {
	present, err := tableNames(ctx, db)
	if err != nil {
		return nil, err
	}
	has := map[string]bool{}
	for _, t := range present {
		has[t] = true
	}
	var out []replacedRow
	for _, table := range SettingsTables {
		// A table this build does not have is one ImportTables skips, so
		// there is nothing for it to overwrite either.
		if !has[table] {
			continue
		}
		rows, _, err := dumpRowsOf(dir, table)
		if err != nil {
			return nil, err
		}
		// A file the dump does not have imports as empty (importTable), so
		// rows stays nil and every row of the table below comes out as
		// removed -- which is what that import does to it, and is worth
		// saying rather than passing over as a diff somebody will read.
		cols, err := columns(ctx, db, table)
		if err != nil {
			return nil, err
		}
		// A withheld column is never replaced, and its value is never
		// one to put in a journal line (withheld.go).
		replaced, err := tableReplaced(ctx, db, dir, table, withoutWithheld(table, cols), rows)
		if err != nil {
			return nil, err
		}
		out = append(out, replaced...)
	}
	return out, nil
}

// tableReplaced is replacedByApplying for one table.
//
// The primary key is what matches a file row to a database row, exactly
// as it is in tableResets and for the same reason: it is the key the
// export orders by (orderBy), so two rows that share it are the same row
// in every sense this package has. A table with no primary key is passed
// over rather than guessed at -- there is no way to say which database
// row a file row was, so no way to say what it replaced. None of today's
// settings tables are in that position.
func tableReplaced(ctx context.Context, db querier, dir, table string, cols []column, rows []map[string]any) ([]replacedRow, error) {
	var key []column
	for _, c := range cols {
		if c.pk > 0 {
			key = append(key, c)
		}
	}
	if len(key) == 0 {
		return nil, nil
	}
	sort.Slice(key, func(i, j int) bool { return key[i].pk < key[j].pk })
	byName := map[string]column{}
	for _, c := range cols {
		byName[c.name] = c
	}
	stored, err := storedRows(ctx, db, table, cols)
	if err != nil {
		return nil, err
	}

	// The file's rows in the values ImportTables would actually bind, so
	// that both sides of every comparison below are Go values of the same
	// kinds and the comparison is of what would be stored rather than of
	// how it was written down.
	arriving := map[string]map[string]any{}
	var order []string
	for _, row := range rows {
		decoded, ok := decodeRow(dir, byName, row)
		if !ok {
			continue
		}
		k, ok := rowIdentity(key, decoded)
		if !ok {
			// A row that does not carry its own key cannot be matched to
			// anything, and is a row importTable is about to refuse or
			// insert as a new one. Either way there is nothing to say
			// about what it replaced.
			continue
		}
		if _, seen := arriving[k]; seen {
			// Two file rows with one key: the second insert is about to
			// fail on the primary key, the import is one transaction, and
			// nothing here will be reported at all.
			continue
		}
		arriving[k] = decoded
		order = append(order, k)
	}

	path := filepath.FromSlash(DumpPath(table))
	var out []replacedRow
	held := map[string]bool{}
	for _, row := range stored {
		k, ok := rowIdentity(key, row)
		if !ok {
			continue
		}
		held[k] = true
		desc := describeRow(key, row)
		file, ok := arriving[k]
		if !ok {
			out = append(out, replacedRow{path: path, row: desc, removed: true})
			continue
		}
		if values := rowReplaced(cols, row, file); len(values) > 0 {
			out = append(out, replacedRow{path: path, row: desc, values: values})
		}
	}
	for _, k := range order {
		if !held[k] {
			out = append(out, replacedRow{path: path, row: describeRow(key, arriving[k]), added: true})
		}
	}
	return out, nil
}

// rowReplaced is the column-by-column comparison of one stored row
// against the row that is about to replace it, in the table's own column
// order so that a line reads the way the file does.
//
// Only columns the file carries and this build has. A column the file
// leaves out is resets.go's to report; a column this build does not have
// is one importTable skips, so nothing of it is being replaced.
func rowReplaced(cols []column, stored, arriving map[string]any) []replacedValue {
	var out []replacedValue
	for _, c := range cols {
		was, held := stored[c.name]
		now, carried := arriving[c.name]
		if !held || !carried {
			continue
		}
		if canonical(was) == canonical(now) {
			continue
		}
		out = append(out, replacedValue{
			column: c.name,
			from:   abbreviate(settingValue(was)),
			to:     abbreviate(settingValue(now)),
		})
	}
	return out
}

// storedRows reads a whole settings table as the values the driver hands
// back, in the export's own row order.
//
// Whole, and in memory: these are the settings tables, which are the
// small end of the dump -- a deployment's config row, its templates, its
// schedules and their reads -- and this runs on an import, which happens
// when the repository has moved and not on every tick.
func storedRows(ctx context.Context, db querier, table string, cols []column) ([]map[string]any, error) {
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.name
	}
	q := "SELECT " + quoteList(names) + " FROM " + quote(table) +
		" ORDER BY " + quoteList(orderBy(cols))
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("staterepo: reading what applying %s would replace: %w", table, err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(names))
		ptrs := make([]any, len(names))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("staterepo: reading what applying %s would replace: %w", table, err)
		}
		row := make(map[string]any, len(names))
		for i, name := range names {
			row[name] = vals[i]
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("staterepo: reading what applying %s would replace: %w", table, err)
	}
	return out, nil
}

// decodeRow turns one file row into the values ImportTables would bind
// for it.
//
// A column this build does not have is dropped, exactly as importTable
// drops it. A column that cannot be decoded gives up the whole row rather
// than a column of it: that file is one the import is about to fail on,
// and an import that fails reports nothing at all
// (applySettings' ordering), so the only thing a partial row here could
// do is be wrong.
func decodeRow(dir string, byName map[string]column, row map[string]any) (map[string]any, bool) {
	out := make(map[string]any, len(row))
	for name, raw := range row {
		col, ok := byName[name]
		if !ok {
			continue
		}
		raw, err := resolveFileValue(dir, raw)
		if err != nil {
			return nil, false
		}
		v, err := decodeValue(raw, col)
		if err != nil {
			return nil, false
		}
		out[name] = v
	}
	return out, true
}

// rowIdentity is the key of a row as something two maps can be compared
// on: every key column in the schema's own key order, in the notation an
// export would write it, joined by a byte no JSON document can hold.
//
// Canonical rather than displayed, because the displayed form is a
// sentence and two different keys can be written as sentences that look
// alike. Reports false when a row does not carry every key column, which
// on the file side is a row importTable will insert or refuse rather than
// replace anything with.
func rowIdentity(key []column, row map[string]any) (string, bool) {
	parts := make([]string, 0, len(key))
	for _, c := range key {
		v, ok := row[c.name]
		if !ok {
			return "", false
		}
		parts = append(parts, canonical(v))
	}
	return strings.Join(parts, "\x00"), true
}

// describeRow is the same key written for a person ("id = 1", "owner =
// 'grain', name = 'grain'"), which is how the journal names the row --
// rowKey's desc, built from values that are already decoded.
func describeRow(key []column, row map[string]any) string {
	parts := make([]string, 0, len(key))
	for _, c := range key {
		parts = append(parts, c.name+" = "+settingValue(row[c.name]))
	}
	return strings.Join(parts, ", ")
}

// canonical renders a value as the JSON an export would write for it,
// which is the one form both sides of this comparison can be put in.
//
// Comparing here rather than in SQLite, which is the other way round from
// rowResets, and for a reason that is specific to comparing two values
// rather than a value with a default: the two sides arrive as different
// Go types for the same stored value -- a DATETIME comes back from the
// driver as a time.Time and out of the file as an RFC3339 string, an
// INTEGER as an int64 and as a json.Number -- and encodeValue is the
// function this package already trusts to say when two of those are the
// same thing, because it is what writes the dump both sides came from. A
// value that has been through it and back is equal exactly when an export
// would write the same bytes for both, which is also what a reader
// comparing the pull request's diff with the deployment would call equal.
func canonical(v any) string {
	data, err := json.Marshal(encodeValue(v))
	if err != nil {
		// Nothing the driver returns or decodeValue produces gets here.
		// One that did would rather be reported as a difference than
		// compared wrongly, which is what a type-tagged rendering gives.
		return fmt.Sprintf("%T:%v", v, v)
	}
	return string(data)
}

// settingValue renders a value for the journal in the notation resets.go
// uses (literal): a quoted string, a bare number, NULL. A blob is named
// rather than printed, since a settings column holding one holds nothing
// a line of a journal can show.
func settingValue(v any) string {
	switch t := encodeValue(v).(type) {
	case map[string]string:
		return "<blob>"
	default:
		return literal(t)
	}
}

// replacedRowLimit is how many rows one journal line names, and
// replacedColumnLimit how many columns of one of those rows.
//
// Bounded for the reason the undelivered notice is: the case worth
// reporting at all is the one where a deployment had moved a long way
// from its dump, and that is exactly the case that can produce hundreds
// of rows -- a reconciliation of every repo name in the database moved
// 521 of them. What is past the bound is counted rather than dropped
// silently, so the line says how much more there was.
//
// Generous rather than tight, unlike the pane's: this is a line in a
// journal that is read once, by somebody who has already noticed a
// setting is not what they left it as, and the row they are looking for
// is as likely to be the fortieth as the first.
const (
	replacedRowLimit    = 40
	replacedColumnLimit = 12
)

// reportReplaced writes the line: what the import replaced, table by
// table and row by row.
//
// One line and not one per row, for reportResets' reason -- these arrive
// together, as one import of one commit, and a journal is read by the
// line.
func reportReplaced(replaced []replacedRow) {
	if len(replaced) == 0 {
		return
	}
	var tables []string
	seen := map[string]bool{}
	for _, r := range replaced {
		if !seen[r.path] {
			seen[r.path] = true
			tables = append(tables, r.path)
		}
	}
	shown := replaced
	var tail string
	if len(shown) > replacedRowLimit {
		tail = fmt.Sprintf(" (and %d more, which this line does not have room for)",
			len(shown)-replacedRowLimit)
		shown = shown[:replacedRowLimit]
	}
	lines := make([]string, len(shown))
	for i, r := range shown {
		lines[i] = trimValues(r).String()
	}
	log.Printf("staterepo: the settings that just arrived did not match this database on %d %s of its "+
		"settings tables, across %d %s (%s), and a settings file holds whole rows: the repository is "+
		"what says what a setting is, so the import replaced every one of them with the file's version "+
		"-- including rows the change was not about, which the last export wrote and this deployment "+
		"has moved on from since. What differed: %s%s. If one of those was not meant, what this "+
		"database held is above: put it back in the repository and merge that, because the database "+
		"now holds what arrived and the next export writes that back out.",
		len(replaced),
		plural(len(replaced), "row", "rows"),
		len(tables),
		plural(len(tables), "file", "files"),
		strings.Join(tables, ", "),
		strings.Join(lines, "; "),
		tail)
}

// trimValues cuts one row's columns to replacedColumnLimit, counting the
// rest rather than dropping them, so the row still says how much it lost.
// A file branched before an upgrade differs from the database in every
// column that upgrade touched, in every row, and one row is not allowed
// to fill the line.
func trimValues(r replacedRow) replacedRow {
	if len(r.values) <= replacedColumnLimit {
		return r
	}
	r.moreValues = len(r.values) - replacedColumnLimit
	r.values = r.values[:replacedColumnLimit]
	return r
}

// replacedForNotice is replacedByApplying with a cancelled context
// treated as nothing to say, for resetsForNotice's reason: the import
// below is about to fail on the same context, and a line about the
// notice being unavailable would be noise on top of a shutdown.
func replacedForNotice(ctx context.Context, db querier, dir string) ([]replacedRow, error) {
	replaced, err := replacedByApplying(ctx, db, dir)
	if err != nil && ctx.Err() != nil {
		return nil, nil
	}
	return replaced, err
}
