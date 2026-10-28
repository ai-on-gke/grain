package staterepo

// merge.go keeps a stale export from rewinding the database.
//
// An import used to be a replacement: every settings table cleared and
// refilled from the commit that arrived. That is right only while the
// database still holds what this host last exported, and nothing makes
// that so. An export that cannot run -- a sync failing for a day and a
// half on a query Postgres could not parse, say -- leaves the repository
// at the last export while operators go on changing settings in the UI,
// and the next import (a merge arriving, a restart into one) then puts
// every one of those rows back to the old export's version. Seventy rows
// across seven files went that way on grain-eu-host on 2026-10-07: admin
// accounts, console sessions, a team's name, schedules' run times.
//
// So an import is a three-way merge instead. The base is the commit this
// host last agreed with the repository at (loadedHead: the last export it
// wrote, or the last commit it imported), or, when the repository was
// reset onto a branch that does not contain that commit (RecoverDiverged),
// where the two histories part. Per row, by primary key:
//
//   - a row the arriving commit changed relative to the base -- edited,
//     added, deleted -- takes the arriving version, which is the merge
//     doing what it was merged for;
//   - a row the arriving commit did not change keeps whatever the
//     database holds, including a row the database added or deleted
//     since the base and never exported.
//
// When the database has not moved since the base, the merge and the old
// replacement are the same import, row for row. When it has, the rows it
// moved survive and the next export writes them out.
//
// A base that cannot be read is a refusal rather than a fallback to the
// replacement: applying blind is exactly what rewound grain-eu-host.

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrNoMergeBase is an import refused because there is no base to merge
// against: the commit this host last agreed with the repository at is not
// in the repository any more, or shares no history with what arrived.
// Taking the arriving commit wholesale there could rewind any setting
// changed since, so the database is kept and nothing is imported until an
// operator decides which side wins.
var ErrNoMergeBase = errors.New("staterepo: cannot work out what the arriving settings changed, so they are not applied")

// mergePlan is what a three-way import keeps from the database: per
// table, the rows the arriving commit left alone.
type mergePlan struct {
	base   string
	tables map[string]*tableKeep
	// unexported counts the kept rows whose database version differs from
	// the base -- changes made since the last export that a replacement
	// would have undone.
	unexported []string
	// conflicts names rows both sides changed. The arriving version wins
	// those, and they are said so nobody has to find out from the diff.
	conflicts []string
	// keptDescs names the kept rows the way the import's notices name
	// them, per file, so those notices can leave them out; keptCols does
	// the same for the columns kept of a row the merge did change.
	keptDescs map[string]map[string]bool
	keptCols  map[string]map[string]map[string]bool
}

// tableKeep is one table's half of a mergePlan.
type tableKeep struct {
	key  []column
	cols []column
	// rows maps a row's identity (rowIdentity) to what is kept of it.
	rows map[string]keptRow
	// cols maps the identity of a row the arriving commit did change to
	// the database's values of the columns it did not, which are put back
	// over the imported row.
	colsKept map[string]keptRow
}

// keptRow is one row whose database version stands: key holds its key
// columns, and row the database's whole row -- nil when the database does
// not hold it, a row it deleted that the arriving commit still carries
// unchanged.
type keptRow struct {
	key map[string]any
	row map[string]any
}

// mergeBase is the commit an import of head merges against: the marker
// when head descends from it, and the two histories' common ancestor
// otherwise. "" with ErrNoMergeBase when there is neither.
func (r *Repo) mergeBase(ctx context.Context, marker, head string) (string, error) {
	if marker == "" {
		return "", fmt.Errorf("%w: this host has no record of the commit it last exported", ErrNoMergeBase)
	}
	if _, err := r.git(ctx, "cat-file", "-e", "--end-of-options", marker+"^{commit}"); err != nil {
		return "", fmt.Errorf("%w: the commit this host last exported, %s, is not in the repository", ErrNoMergeBase, short(marker))
	}
	out, err := r.git(ctx, "merge-base", "--end-of-options", marker, head)
	if err != nil {
		return "", fmt.Errorf("%w: %s and %s share no history", ErrNoMergeBase, short(marker), short(head))
	}
	return strings.TrimSpace(out), nil
}

// checkoutTree writes commit's tree into a fresh temporary directory,
// which the caller removes. git archive rather than a worktree, so
// nothing is left registered in the repository if this process dies
// half way.
func (r *Repo) checkoutTree(ctx context.Context, commit string) (string, error) {
	out, err := r.git(ctx, "archive", "--format=tar", "--end-of-options", commit)
	if err != nil {
		return "", fmt.Errorf("%w: reading %s: %w", ErrNoMergeBase, short(commit), err)
	}
	dir, err := os.MkdirTemp("", "grain-staterepo-base-")
	if err != nil {
		return "", err
	}
	tr := tar.NewReader(bytes.NewReader([]byte(out)))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			os.RemoveAll(dir)
			return "", fmt.Errorf("%w: reading %s: %w", ErrNoMergeBase, short(commit), err)
		}
		name := filepath.Clean(filepath.FromSlash(h.Name))
		if name == "." || strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			continue
		}
		path := filepath.Join(dir, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o700); err != nil {
				os.RemoveAll(dir)
				return "", err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				os.RemoveAll(dir)
				return "", err
			}
			data, err := io.ReadAll(tr)
			if err != nil {
				os.RemoveAll(dir)
				return "", err
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				os.RemoveAll(dir)
				return "", err
			}
		}
	}
	return dir, nil
}

// planMerge works out, for tables, which rows importing dir over db
// leaves at the database's values, against the dump in baseDir.
func planMerge(ctx context.Context, db querier, base, baseDir, dir string, tables []string) (*mergePlan, error) {
	present, err := tableNames(ctx, db)
	if err != nil {
		return nil, err
	}
	has := map[string]bool{}
	for _, t := range present {
		has[t] = true
	}
	plan := &mergePlan{base: base, tables: map[string]*tableKeep{}, keptDescs: map[string]map[string]bool{},
		keptCols: map[string]map[string]map[string]bool{}}
	for _, table := range tables {
		if !has[table] {
			continue
		}
		cols, err := columns(ctx, db, table)
		if err != nil {
			return nil, err
		}
		var key []column
		for _, c := range cols {
			if c.pk > 0 {
				key = append(key, c)
			}
		}
		if len(key) == 0 {
			// Nothing to match rows by, so nothing can be merged: the
			// arriving file replaces the table, as it always has. None of
			// today's settings tables is keyless.
			continue
		}
		sort.Slice(key, func(i, j int) bool { return key[i].pk < key[j].pk })
		byName := map[string]column{}
		for _, c := range cols {
			byName[c.name] = c
		}
		baseRows, err := decodedRows(baseDir, table, key, byName)
		if err != nil {
			return nil, fmt.Errorf("staterepo: reading %s as of %s: %w", DumpPath(table), short(base), err)
		}
		incoming, err := decodedRows(dir, table, key, byName)
		if err != nil {
			return nil, err
		}
		stored, err := storedRows(ctx, db, table, cols)
		if err != nil {
			return nil, err
		}
		inDB := map[string]map[string]any{}
		for _, row := range stored {
			if id, ok := rowIdentity(key, row); ok {
				inDB[id] = row
			}
		}
		ids := map[string]bool{}
		for id := range baseRows {
			ids[id] = true
		}
		for id := range incoming {
			ids[id] = true
		}
		for id := range inDB {
			ids[id] = true
		}
		keep := &tableKeep{key: key, cols: cols, rows: map[string]keptRow{}, colsKept: map[string]keptRow{}}
		comparable := withoutWithheld(table, cols)
		for id := range ids {
			b, inBase := baseRows[id]
			in, inIncoming := incoming[id]
			d, inStore := inDB[id]
			desc := DumpPath(table) + " (" + describeRow(key, firstOf(d, in, b)) + ")"
			if inBase == inIncoming && (!inBase || sameRow(b, in)) {
				// The arriving commit left this row alone: the database's
				// version stands, whatever it is.
				if inStore != inBase || (inStore && !sameStored(comparable, d, b)) {
					plan.unexported = append(plan.unexported, desc)
				}
				if inStore || inIncoming {
					keep.rows[id] = keptRow{key: firstOf(d, in), row: d}
					plan.noteKept(table, key, firstOf(d, in))
				}
				continue
			}
			if !(inBase && inIncoming && inStore) {
				// Added or deleted by the arriving commit: it wins whole.
				if inStore != inBase || (inStore && !sameStored(comparable, d, b)) {
					plan.conflicts = append(plan.conflicts, desc)
				}
				continue
			}
			// A row all three hold and the arriving commit edited: merged
			// column by column. A column it changed -- a new value, or left
			// out so that it lands on its default -- takes the import's
			// value; every other column keeps the database's.
			kept := map[string]any{}
			for _, c := range comparable {
				if c.pk > 0 {
					continue
				}
				bv, bHas := b[c.name]
				iv, iHas := in[c.name]
				changed := bHas != iHas || (bHas && canonical(bv) != canonical(iv))
				dbMoved := !bHas || canonical(d[c.name]) != canonical(bv)
				if changed {
					if dbMoved && canonical(d[c.name]) != canonical(iv) {
						plan.conflicts = append(plan.conflicts, desc+": "+c.name)
					}
					continue
				}
				if iHas && canonical(d[c.name]) == canonical(iv) {
					continue
				}
				kept[c.name] = d[c.name]
				plan.unexported = append(plan.unexported, desc+": "+c.name)
			}
			if len(kept) > 0 {
				keep.colsKept[id] = keptRow{key: d, row: kept}
				plan.noteKeptCols(table, key, d, kept)
			}
		}
		if len(keep.rows) > 0 || len(keep.colsKept) > 0 {
			plan.tables[table] = keep
		}
	}
	sort.Strings(plan.unexported)
	sort.Strings(plan.conflicts)
	return plan, nil
}

// noteKept records a kept row under both of the names the notices give a
// row: describeRow's (replaced.go) and rowKey's (resets.go).
func (p *mergePlan) noteKept(table string, key []column, row map[string]any) {
	path := filepath.FromSlash(DumpPath(table))
	if p.keptDescs[path] == nil {
		p.keptDescs[path] = map[string]bool{}
	}
	p.keptDescs[path][describeRow(key, row)] = true
	parts := make([]string, len(key))
	for i, k := range key {
		parts[i] = k.name + " = " + literal(row[k.name])
	}
	p.keptDescs[path][strings.Join(parts, ", ")] = true
}

// noteKeptCols is noteKept for the columns kept of an edited row.
func (p *mergePlan) noteKeptCols(table string, key []column, row, kept map[string]any) {
	path := filepath.FromSlash(DumpPath(table))
	if p.keptCols[path] == nil {
		p.keptCols[path] = map[string]map[string]bool{}
	}
	parts := make([]string, len(key))
	for i, k := range key {
		parts[i] = k.name + " = " + literal(row[k.name])
	}
	set := map[string]bool{}
	for c := range kept {
		set[c] = true
	}
	p.keptCols[path][describeRow(key, row)] = set
	p.keptCols[path][strings.Join(parts, ", ")] = set
}

// keptResets drops the resets the merge does not make: a row or a column
// it keeps at the database's value loses nothing.
func (p *mergePlan) keptResets(in []Reset) []Reset {
	var out []Reset
	for _, r := range in {
		if p.keptDescs[r.Path][r.Row] || p.keptCols[r.Path][r.Row][r.Column] {
			continue
		}
		out = append(out, r)
	}
	return out
}

// keptReplaced is keptResets for replaced.go's rows.
func (p *mergePlan) keptReplaced(in []replacedRow) []replacedRow {
	var out []replacedRow
	for _, r := range in {
		if p.keptDescs[r.path][r.row] {
			continue
		}
		if cols := p.keptCols[r.path][r.row]; len(cols) > 0 && len(r.values) > 0 {
			var values []replacedValue
			for _, v := range r.values {
				if !cols[v.column] {
					values = append(values, v)
				}
			}
			if len(values) == 0 {
				continue
			}
			r.values = values
		}
		out = append(out, r)
	}
	return out
}

func firstOf(rows ...map[string]any) map[string]any {
	for _, r := range rows {
		if r != nil {
			return r
		}
	}
	return nil
}

// decodedRows reads one table's rows out of a dump, decoded the way the
// import would bind them and keyed by identity. A table the dump has no
// file for is empty, as importTable treats it.
func decodedRows(dir, table string, key []column, byName map[string]column) (map[string]map[string]any, error) {
	rows, _, err := dumpRowsOf(dir, table)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]any{}
	for _, row := range rows {
		decoded, ok := decodeRow(dir, byName, row)
		if !ok {
			return nil, fmt.Errorf("staterepo: %s holds a row this build cannot read", DumpPath(table))
		}
		id, ok := rowIdentity(key, decoded)
		if !ok {
			continue
		}
		out[id] = decoded
	}
	return out, nil
}

// sameRow compares two file rows: every column either carries, in the
// notation an export would write it. A column one carries and the other
// does not is a difference -- the import would land it on its default.
func sameRow(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for name, v := range a {
		w, ok := b[name]
		if !ok || canonical(v) != canonical(w) {
			return false
		}
	}
	return true
}

// sameStored compares a database row with a file row over the columns
// the file carries; a withheld column is never in a file and never
// compared.
func sameStored(cols []column, stored, file map[string]any) bool {
	for _, c := range cols {
		f, carried := file[c.name]
		if !carried {
			continue
		}
		if canonical(stored[c.name]) != canonical(f) {
			return false
		}
	}
	return true
}

// restore puts the kept rows back after the import has refilled the
// tables from the arriving commit, inside the import's transaction.
//
// Values go through the export's own encoding and back (encodeValue,
// decodeValue), so what is written is exactly what an export and a
// re-import of the row would write -- a DATETIME in the notation the
// import stores, not whatever the driver's time.Time renders as.
func (p *mergePlan) restore(ctx context.Context, tx *sql.Tx) error {
	if p == nil {
		return nil
	}
	names := make([]string, 0, len(p.tables))
	for t := range p.tables {
		names = append(names, t)
	}
	sort.Strings(names)
	for _, table := range names {
		t := p.tables[table]
		wheres := make([]string, len(t.key))
		for i, k := range t.key {
			wheres[i] = quote(k.name) + " IS NOT DISTINCT FROM ?"
		}
		del := "DELETE FROM " + quote(table) + " WHERE " + strings.Join(wheres, " AND ")
		ids := make([]string, 0, len(t.rows))
		for id := range t.rows {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			row, keyRow := t.rows[id].row, t.rows[id].key
			args := make([]any, len(t.key))
			for i, k := range t.key {
				v, err := roundTrip(keyRow[k.name], k)
				if err != nil {
					return fmt.Errorf("staterepo: keeping %s's row %s: %w", table, describeRow(t.key, keyRow), err)
				}
				args[i] = v
			}
			if _, err := tx.ExecContext(ctx, del, args...); err != nil {
				return fmt.Errorf("staterepo: keeping %s's row %s: %w", table, describeRow(t.key, keyRow), err)
			}
			if row == nil {
				continue
			}
			cols := make([]string, 0, len(t.cols))
			vals := make([]any, 0, len(t.cols))
			for _, c := range t.cols {
				v, err := roundTrip(row[c.name], c)
				if err != nil {
					return fmt.Errorf("staterepo: keeping %s's row %s: %w", table, describeRow(t.key, row), err)
				}
				cols = append(cols, c.name)
				vals = append(vals, v)
			}
			ins := "INSERT INTO " + quote(table) + " (" + quoteList(cols) + ") VALUES (" +
				strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",") + ")"
			if _, err := tx.ExecContext(ctx, ins, vals...); err != nil {
				return fmt.Errorf("staterepo: keeping %s's row %s: %w", table, describeRow(t.key, row), err)
			}
		}
		ids = ids[:0]
		for id := range t.colsKept {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		byName := map[string]column{}
		for _, c := range t.cols {
			byName[c.name] = c
		}
		for _, id := range ids {
			k := t.colsKept[id]
			names := make([]string, 0, len(k.row))
			for name := range k.row {
				names = append(names, name)
			}
			sort.Strings(names)
			sets := make([]string, len(names))
			args := make([]any, 0, len(names)+len(t.key))
			for i, name := range names {
				sets[i] = quote(name) + " = ?"
				v, err := roundTrip(k.row[name], byName[name])
				if err != nil {
					return fmt.Errorf("staterepo: keeping %s's %s for %s: %w", table, name, describeRow(t.key, k.key), err)
				}
				args = append(args, v)
			}
			for _, kc := range t.key {
				v, err := roundTrip(k.key[kc.name], kc)
				if err != nil {
					return fmt.Errorf("staterepo: keeping %s's row %s: %w", table, describeRow(t.key, k.key), err)
				}
				args = append(args, v)
			}
			upd := "UPDATE " + quote(table) + " SET " + strings.Join(sets, ", ") + " WHERE " + strings.Join(wheres, " AND ")
			if _, err := tx.ExecContext(ctx, upd, args...); err != nil {
				return fmt.Errorf("staterepo: keeping %s's row %s: %w", table, describeRow(t.key, k.key), err)
			}
		}
	}
	return nil
}

// roundTrip is one value through an export and back into an import.
func roundTrip(v any, c column) (any, error) {
	data, err := json.Marshal(encodeValue(v))
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var raw any
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	return decodeValue(raw, c)
}

// report says what the merge kept, once the import has committed.
func (p *mergePlan) report() {
	if p == nil {
		return
	}
	if len(p.unexported) > 0 {
		log.Printf("staterepo: this deployment held %d settings %s that had never been exported to the state "+
			"repository (it last agreed with it at %s); the arriving change did not touch %s, so %s kept "+
			"the database's %s rather than rewinding to the old export, and the next export writes %s out: %s",
			len(p.unexported), plural(len(p.unexported), "row", "rows"), short(p.base),
			plural(len(p.unexported), "it", "them"), plural(len(p.unexported), "it is", "they are"),
			plural(len(p.unexported), "version", "versions"), plural(len(p.unexported), "it", "them"),
			strings.Join(p.unexported, "; "))
	}
	if len(p.conflicts) > 0 {
		log.Printf("staterepo: the arriving settings change and this deployment's own unexported changes both "+
			"touched %d %s; the arriving version was taken, so the database's own change to %s is gone: %s",
			len(p.conflicts), plural(len(p.conflicts), "row", "rows"), plural(len(p.conflicts), "it", "them"),
			strings.Join(p.conflicts, "; "))
	}
}
