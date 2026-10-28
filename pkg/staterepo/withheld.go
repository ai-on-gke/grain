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

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// withheldColumns names the settings-table columns that hold secret
// material and so never go into a state repository, keyed by table.
//
// A state repository is a git repository that agents are dispatched to
// work in and humans review pull requests against, so every byte
// ExportConfig writes is readable by all of them, and stays in the
// history after it is overwritten. A column holding a credential cannot
// live there. Such a column stays in the database and in the full,
// host-local Export (the schema-upgrade handover, which has to carry it
// across or an upgrade would lose it), and is handled three ways here:
//
//   - ExportConfig leaves it out of the table's file entirely, so not
//     even its emptiness is written down.
//   - ImportTables, which applies a merged settings change to a running
//     daemon, keeps the value the database already holds rather than
//     resetting it to the column's default as an omitted column would
//     otherwise be (SettingsTables' own doc comment). A file that names
//     the column anyway -- one branched before the column was withheld,
//     or one somebody wrote it into -- has the value ignored rather than
//     imported, so the repository is never a way to set it.
//   - The check, the reset notices and the replacement notices pass the
//     column over, so none of them ever prints its value or warns that
//     leaving it out costs anything.
var withheldColumns = map[string][]string{
	// The Google OAuth client secret Google sign-in used. Nothing sets
	// or reads it any more (docs/single-tenant-reset.md, stage 3d), but
	// an upgraded installation's row can still hold one until stage 6
	// drops the column, so it is still kept out of the repository.
	"grain_config": {"google_oauth_client_secret"},
}

// isWithheld reports whether column of table is one withheldColumns
// names.
func isWithheld(table, column string) bool {
	for _, c := range withheldColumns[table] {
		if c == column {
			return true
		}
	}
	return false
}

// withoutWithheld is cols minus the columns of table withheldColumns
// names, in the same order.
func withoutWithheld(table string, cols []column) []column {
	if len(withheldColumns[table]) == 0 {
		return cols
	}
	out := make([]column, 0, len(cols))
	for _, c := range cols {
		if !isWithheld(table, c.name) {
			out = append(out, c)
		}
	}
	return out
}

// heldValues is what a withheld column held in every row of one table,
// read before a settings import clears it so the rows can be given it
// back afterwards.
type heldValues struct {
	table string
	key   []string
	cols  []string
	rows  [][]any // key values, then cols values, per row
}

// readHeld reads the withheld columns of table along with each row's
// primary key. A table with nothing withheld, or with no primary key to
// match the refilled rows against, reports nil.
func readHeld(ctx context.Context, db querier, table string) (*heldValues, error) {
	cols, err := columns(ctx, db, table)
	if err != nil {
		return nil, err
	}
	var key []column
	var held []string
	for _, c := range cols {
		if c.pk > 0 {
			key = append(key, c)
		}
		if isWithheld(table, c.name) {
			held = append(held, c.name)
		}
	}
	if len(held) == 0 || len(key) == 0 {
		return nil, nil
	}
	sort.Slice(key, func(i, j int) bool { return key[i].pk < key[j].pk })
	h := &heldValues{table: table, cols: held}
	for _, c := range key {
		h.key = append(h.key, c.name)
	}
	q := "SELECT " + quoteList(append(append([]string{}, h.key...), h.cols...)) + " FROM " + quote(table)
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("staterepo: reading %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		vals := make([]any, len(h.key)+len(h.cols))
		ptrs := make([]any, len(vals))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("staterepo: reading %s: %w", table, err)
		}
		h.rows = append(h.rows, vals)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("staterepo: reading %s: %w", table, err)
	}
	return h, nil
}

// restore writes the held values back onto whichever of their rows the
// import refilled. A row the file no longer holds is simply not there to
// update, which is right: the row is gone, and its secret with it.
func (h *heldValues) restore(ctx context.Context, tx *sql.Tx) error {
	sets := make([]string, len(h.cols))
	for i, c := range h.cols {
		sets[i] = quote(c) + " = ?"
	}
	// IS NOT DISTINCT FROM so a NULL key matches itself, on Postgres as
	// well as SQLite (rowKey).
	wheres := make([]string, len(h.key))
	for i, k := range h.key {
		wheres[i] = quote(k) + " IS NOT DISTINCT FROM ?"
	}
	stmt := "UPDATE " + quote(h.table) + " SET " + strings.Join(sets, ", ") + " WHERE " + strings.Join(wheres, " AND ")
	for _, row := range h.rows {
		args := make([]any, 0, len(row))
		args = append(args, row[len(h.key):]...)
		args = append(args, row[:len(h.key)]...)
		if _, err := tx.ExecContext(ctx, stmt, args...); err != nil {
			return fmt.Errorf("staterepo: keeping %s's withheld columns: %w", h.table, err)
		}
	}
	return nil
}
