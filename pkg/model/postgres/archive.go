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

package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// maxIdentifierLength is PostgreSQL's NAMEDATALEN-1: a longer name is
// truncated by the server, with only a NOTICE to say so.
const maxIdentifierLength = 63

// ArchiveSchemaName is the schema ArchiveStore moves a store's relations
// into: the schema they are in now, with suffix after it, cut short at
// the front rather than letting the server cut off the suffix that says
// what it is.
func ArchiveSchemaName(current, suffix string) string {
	if room := maxIdentifierLength - len(suffix); len(current) > room {
		current = current[:max(room, 0)]
	}
	return current + suffix
}

// ArchiveStore is the PostgreSQL counterpart of moving a SQLite store's
// directory aside: every table, view and sequence in the connection's
// current schema is moved into a new schema named by ArchiveSchemaName,
// in one transaction, so the current schema is left empty for Init to
// build a store at this build's schema in, and the old rows are kept --
// not dropped -- under the name it returns.
//
// Indexes, constraints and the sequences a column owns go with their
// tables. Nothing is copied, so this is as quick on a large store as on
// an empty one, and a failure part way leaves everything where it was.
func ArchiveStore(ctx context.Context, db *sql.DB, suffix string) (string, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	var current string
	if err := tx.QueryRowContext(ctx, "SELECT current_schema()").Scan(&current); err != nil {
		return "", fmt.Errorf("reading the current schema: %w", err)
	}
	if current == "" {
		return "", fmt.Errorf("the connection has no current schema (search_path names none that exists)")
	}
	archive := ArchiveSchemaName(current, suffix)

	// Sequences a column owns (serial, identity) are left out: they are
	// moved with their table, and PostgreSQL refuses to move one alone.
	rows, err := tx.QueryContext(ctx, `
SELECT c.relname::text, c.relkind::text
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = current_schema()
  AND c.relkind IN ('r', 'p', 'v', 'm', 'S', 'f')
  AND NOT c.relispartition
  AND NOT (c.relkind = 'S' AND EXISTS (
    SELECT 1 FROM pg_depend d
    WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype IN ('a', 'i')))
ORDER BY c.relname`)
	if err != nil {
		return "", fmt.Errorf("listing the store's relations in schema %s: %w", current, err)
	}
	type relation struct{ name, kind string }
	var relations []relation
	for rows.Next() {
		var r relation
		if err := rows.Scan(&r.name, &r.kind); err != nil {
			rows.Close()
			return "", err
		}
		relations = append(relations, r)
	}
	if err := rows.Close(); err != nil {
		return "", err
	}
	if err := rows.Err(); err != nil {
		return "", err
	}

	if _, err := tx.ExecContext(ctx, "CREATE SCHEMA "+quoteIdentifier(archive)); err != nil {
		return "", fmt.Errorf("creating schema %s to keep the old store in: %w", archive, err)
	}
	for _, r := range relations {
		kind := "TABLE"
		switch r.kind {
		case "v":
			kind = "VIEW"
		case "m":
			kind = "MATERIALIZED VIEW"
		case "S":
			kind = "SEQUENCE"
		case "f":
			kind = "FOREIGN TABLE"
		}
		stmt := fmt.Sprintf("ALTER %s %s.%s SET SCHEMA %s", kind,
			quoteIdentifier(current), quoteIdentifier(r.name), quoteIdentifier(archive))
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return "", fmt.Errorf("moving %s %s into %s: %w", strings.ToLower(kind), r.name, archive, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return archive, nil
}

func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
