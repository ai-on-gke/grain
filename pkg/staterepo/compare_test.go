package staterepo_test

// Comparing two databases row by row, column by column, storage class by
// storage class.
//
// The claim this package makes is that the repository is the database:
// export it, clone it somewhere else, import it, and what you have is
// what you had. Every test that checks a field or two checks a corner of
// that; this checks the whole of it, which is what makes it worth the
// reflection. It is also what a soak run needs at the end of a few
// hundred cycles, when naming the rows to look at by hand is no longer
// possible.
//
// typeof() is asked for alongside every value on purpose. SQLite stores
// a type per value rather than per column, so a round trip that turned
// the text "42" into the integer 42, or a TEXT column's contents into a
// blob, would compare equal on the value alone and be a different
// database.

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// sameDatabase fails the test unless a and b hold exactly the same rows
// in exactly the same types, naming the first table and row that differ.
func sameDatabase(t *testing.T, ctx context.Context, a, b *sql.DB, what string) {
	t.Helper()
	tables := realTables(t, ctx, a)
	if other := realTables(t, ctx, b); !equalStrings(tables, other) {
		t.Fatalf("%s: the two databases do not have the same tables:\n%v\n%v", what, tables, other)
	}
	sameTables(t, ctx, a, b, tables, what)
}

// sameTables is sameDatabase over a named subset -- what a caller wants
// when the two are expected to agree about the state tier and not about
// grain's own churn, which is exported on a slower clock and so is
// legitimately behind.
func sameTables(t *testing.T, ctx context.Context, a, b *sql.DB, tables []string, what string) {
	t.Helper()
	for _, table := range tables {
		left := dumpRows(t, ctx, a, table)
		right := dumpRows(t, ctx, b, table)
		if len(left) != len(right) {
			t.Fatalf("%s: %s has %d rows on one side and %d on the other", what, table, len(left), len(right))
		}
		for i := range left {
			if left[i] != right[i] {
				t.Fatalf("%s: %s row %d differs:\n  %s\n  %s", what, table, i, left[i], right[i])
			}
		}
	}
}

// dumpRows renders every row of one table as a comparable string, sorted
// by primary key so the two sides line up -- the same ordering the export
// itself uses, and for the same reason.
func dumpRows(t *testing.T, ctx context.Context, db *sql.DB, table string) []string {
	t.Helper()
	cols, pk := tableColumns(t, ctx, db, table)
	if len(cols) == 0 {
		return nil
	}
	var sel []string
	for _, c := range cols {
		sel = append(sel, fmt.Sprintf("typeof(`%s`), `%s`", c, c))
	}
	order := pk
	if len(order) == 0 {
		order = cols
	}
	var by []string
	for _, c := range order {
		by = append(by, "`"+c+"`")
	}
	query := "SELECT " + strings.Join(sel, ", ") + " FROM `" + table + "` ORDER BY " + strings.Join(by, ", ")
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		t.Fatalf("reading %s: %v", table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		vals := make([]any, 2*len(cols))
		ptrs := make([]any, len(vals))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scanning %s: %v", table, err)
		}
		var b strings.Builder
		for i, c := range cols {
			fmt.Fprintf(&b, "%s=%v:%#v ", c, vals[2*i], vals[2*i+1])
		}
		out = append(out, b.String())
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading %s: %v", table, err)
	}
	return out
}

func tableColumns(t *testing.T, ctx context.Context, db *sql.DB, table string) (cols, pk []string) {
	t.Helper()
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(`"+table+"`)")
	if err != nil {
		t.Fatalf("describing %s: %v", table, err)
	}
	defer rows.Close()
	type keyed struct {
		name string
		at   int
	}
	var keys []keyed
	for rows.Next() {
		var (
			cid        int
			name       string
			declType   sql.NullString
			notNull    int
			defaultVal any
			pkAt       int
		)
		if err := rows.Scan(&cid, &name, &declType, &notNull, &defaultVal, &pkAt); err != nil {
			t.Fatalf("describing %s: %v", table, err)
		}
		cols = append(cols, name)
		if pkAt > 0 {
			keys = append(keys, keyed{name, pkAt})
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].at < keys[j].at })
	for _, k := range keys {
		pk = append(pk, k.name)
	}
	return cols, pk
}

func realTables(t *testing.T, ctx context.Context, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx,
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatalf("listing tables: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if staterepo.LocalTables[name] {
			continue
		}
		out = append(out, name)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
