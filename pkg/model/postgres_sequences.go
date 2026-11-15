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

type postgresTxBeginner interface {
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

type postgresSerialColumn struct {
	tableSchema string
	tableName   string
	columnName  string
	seqName     string
	seqSchema   string
	seqRel      string
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
// tables are left untouched so IDs are never rewound. When q can begin a
// transaction (*sql.DB or *sql.Conn) and a behind sequence is detected, the
// re-check and setval run inside a read-write transaction so Grain's
// PostgreSQL single-writer fence and advisory writer lock are enforced.
func SyncPostgresSequences(ctx context.Context, q PostgresQueryExecer) ([]PostgresSequenceSync, error) {
	cols, err := listPostgresSerialColumns(ctx, q)
	if err != nil {
		return nil, err
	}
	behind, err := findBehindPostgresSequences(ctx, q, cols)
	if err != nil || len(behind) == 0 {
		return nil, err
	}

	if tb, ok := q.(postgresTxBeginner); ok {
		tx, err := tb.BeginTx(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("synchronizing postgres sequences: beginning transaction: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

		cols, err = listPostgresSerialColumns(ctx, tx)
		if err != nil {
			return nil, err
		}
		behind, err = findBehindPostgresSequences(ctx, tx, cols)
		if err != nil {
			return nil, err
		}
		if len(behind) == 0 {
			if err := tx.Commit(); err != nil {
				return nil, fmt.Errorf("synchronizing postgres sequences: committing transaction: %w", err)
			}
			return nil, nil
		}
		if err := advancePostgresSequences(ctx, tx, behind); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("synchronizing postgres sequences: committing transaction: %w", err)
		}
		return behind, nil
	}

	if err := advancePostgresSequences(ctx, q, behind); err != nil {
		return nil, err
	}
	return behind, nil
}

func listPostgresSerialColumns(ctx context.Context, q PostgresQueryExecer) ([]postgresSerialColumn, error) {
	const listSerialsSQL = `
SELECT
  tn.nspname::text,
  tc.relname::text,
  a.attname::text,
  seq.seq_name::text,
  sn.nspname::text,
  sc.relname::text
FROM pg_class tc
JOIN pg_namespace tn ON tn.oid = tc.relnamespace
JOIN pg_attribute a ON a.attrelid = tc.oid
  AND a.attnum > 0
  AND NOT a.attisdropped
CROSS JOIN LATERAL (
  SELECT pg_get_serial_sequence(format('%I.%I', tn.nspname, tc.relname), a.attname) AS seq_name
) seq
JOIN pg_class sc ON sc.oid = seq.seq_name::regclass AND sc.relkind = 'S'
JOIN pg_namespace sn ON sn.oid = sc.relnamespace
WHERE tn.nspname = CURRENT_SCHEMA()
  AND tc.relkind IN ('r', 'p')
  AND NOT tc.relispartition
  AND seq.seq_name IS NOT NULL
ORDER BY tc.relname, a.attname`

	rows, err := q.QueryContext(ctx, listSerialsSQL)
	if err != nil {
		return nil, fmt.Errorf("synchronizing postgres sequences: listing serial columns: %w", err)
	}
	var cols []postgresSerialColumn
	for rows.Next() {
		var sc postgresSerialColumn
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
	return cols, nil
}

func findBehindPostgresSequences(ctx context.Context, q PostgresQueryExecer, cols []postgresSerialColumn) ([]PostgresSequenceSync, error) {
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
	return behind, nil
}

func advancePostgresSequences(ctx context.Context, q PostgresQueryExecer, behind []PostgresSequenceSync) error {
	var (
		setSQL strings.Builder
		args   = make([]any, 0, len(behind)*2)
	)
	setSQL.WriteString("SELECT ")
	for i, b := range behind {
		if i > 0 {
			setSQL.WriteString(", ")
		}
		fmt.Fprintf(&setSQL, "setval($%d::regclass, $%d::bigint, true)", i*2+1, i*2+2)
		args = append(args, b.Sequence, b.MaxID)
	}
	if _, err := q.ExecContext(ctx, setSQL.String(), args...); err != nil {
		return fmt.Errorf("synchronizing postgres sequences: %w", err)
	}
	return nil
}

func quotePGIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
