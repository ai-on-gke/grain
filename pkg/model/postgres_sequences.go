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

package model

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// PostgresSequenceSync records one PostgreSQL serial or identity sequence that
// was behind its column's maximum value and was advanced to match it.
type PostgresSequenceSync struct {
	Table    string
	Column   string
	Sequence string
	NextWas  int64
	MaxID    int64
}

// PostgresQueryExecer is the subset of *sql.DB, *sql.Tx and *sql.Conn needed
// to inspect and advance PostgreSQL serial sequences.
type PostgresQueryExecer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// IsPostgresDB reports whether db uses Grain's PostgreSQL driver adapter
// (pkg/model/postgres). SQLite AUTOINCREMENT follows explicit rowids
// automatically, so callers that take a generic *sql.DB (such as
// pkg/staterepo) only need to resynchronize sequences when this returns true.
func IsPostgresDB(db *sql.DB) bool {
	if db == nil {
		return false
	}
	d, ok := db.Driver().(interface{ IsPostgresDriver() bool })
	return ok && d.IsPostgresDriver()
}

// SyncPostgresSequences inspects every serial or identity sequence in q's
// current PostgreSQL schema against MAX(column), advances any sequence whose
// next allocation would be <= MAX(column), and returns the sequences it
// advanced.
//
// Sequences already ahead of MAX(column) (for example after top rows were
// deleted or after a high-water floor was seeded) and sequences on empty
// tables are left untouched so IDs are never rewound.
func SyncPostgresSequences(ctx context.Context, q PostgresQueryExecer) ([]PostgresSequenceSync, error) {
	type serialColumn struct {
		tableSchema string
		tableName   string
		columnName  string
		seqName     string
		seqSchema   string
		seqRel      string
	}

	const listSerialsSQL = `
SELECT
  c.table_schema::text,
  c.table_name::text,
  c.column_name::text,
  seq.seq_name::text,
  sn.nspname::text,
  sc.relname::text
FROM information_schema.columns c
CROSS JOIN LATERAL (
  SELECT pg_get_serial_sequence(format('%I.%I', c.table_schema, c.table_name), c.column_name) AS seq_name
) seq
JOIN pg_class sc ON sc.oid = seq.seq_name::regclass
JOIN pg_namespace sn ON sn.oid = sc.relnamespace
WHERE c.table_schema = CURRENT_SCHEMA()
  AND seq.seq_name IS NOT NULL
ORDER BY c.table_name, c.column_name`

	rows, err := q.QueryContext(ctx, listSerialsSQL)
	if err != nil {
		return nil, fmt.Errorf("synchronizing postgres sequences: listing serial columns: %w", err)
	}
	var cols []serialColumn
	for rows.Next() {
		var sc serialColumn
		if err := rows.Scan(&sc.tableSchema, &sc.tableName, &sc.columnName, &sc.seqName, &sc.seqSchema, &sc.seqRel); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("synchronizing postgres sequences: scanning serial column: %w", err)
		}
		cols = append(cols, sc)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("synchronizing postgres sequences: reading serial columns: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("synchronizing postgres sequences: closing serial column rows: %w", err)
	}
	if len(cols) == 0 {
		return nil, nil
	}

	var checkSQL strings.Builder
	checkSQL.WriteString("SELECT idx, last_val, is_called, max_id FROM (\n")
	for i, sc := range cols {
		if i > 0 {
			checkSQL.WriteString("\nUNION ALL\n")
		}
		fmt.Fprintf(&checkSQL,
			"SELECT %d::int AS idx, s.last_value::bigint AS last_val, s.is_called AS is_called, (SELECT MAX(%s)::bigint FROM %s.%s) AS max_id FROM %s.%s s",
			i,
			quotePGIdent(sc.columnName),
			quotePGIdent(sc.tableSchema),
			quotePGIdent(sc.tableName),
			quotePGIdent(sc.seqSchema),
			quotePGIdent(sc.seqRel),
		)
	}
	checkSQL.WriteString("\n) checks\nWHERE max_id IS NOT NULL AND max_id >= 1 AND ((is_called AND last_val < max_id) OR (NOT is_called AND last_val <= max_id))\nORDER BY idx")

	crows, err := q.QueryContext(ctx, checkSQL.String())
	if err != nil {
		return nil, fmt.Errorf("synchronizing postgres sequences: checking sequence state: %w", err)
	}
	var behind []PostgresSequenceSync
	for crows.Next() {
		var (
			idx      int
			lastVal  int64
			isCalled bool
			maxID    int64
		)
		if err := crows.Scan(&idx, &lastVal, &isCalled, &maxID); err != nil {
			_ = crows.Close()
			return nil, fmt.Errorf("synchronizing postgres sequences: scanning sequence check: %w", err)
		}
		if idx < 0 || idx >= len(cols) {
			continue
		}
		nextWas := lastVal
		if isCalled {
			nextWas = lastVal + 1
		}
		sc := cols[idx]
		behind = append(behind, PostgresSequenceSync{
			Table:    sc.tableName,
			Column:   sc.columnName,
			Sequence: sc.seqName,
			NextWas:  nextWas,
			MaxID:    maxID,
		})
	}
	if err := crows.Err(); err != nil {
		_ = crows.Close()
		return nil, fmt.Errorf("synchronizing postgres sequences: reading sequence checks: %w", err)
	}
	if err := crows.Close(); err != nil {
		return nil, fmt.Errorf("synchronizing postgres sequences: closing sequence check rows: %w", err)
	}
	if len(behind) == 0 {
		return nil, nil
	}

	var setSQL strings.Builder
	setSQL.WriteString("SELECT ")
	for i, b := range behind {
		if i > 0 {
			setSQL.WriteString(", ")
		}
		setSQL.WriteString("setval(")
		setSQL.WriteString(quotePGLiteral(b.Sequence))
		setSQL.WriteString(", ")
		setSQL.WriteString(strconv.FormatInt(b.MaxID, 10))
		setSQL.WriteString(", true)")
	}
	if _, err := q.ExecContext(ctx, setSQL.String()); err != nil {
		return nil, fmt.Errorf("synchronizing postgres sequences: %w", err)
	}
	return behind, nil
}

func quotePGIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func quotePGLiteral(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `''`) + `'`
}
