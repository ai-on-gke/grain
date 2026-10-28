package dbaccess_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/dbaccess"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
	_ "modernc.org/sqlite"
)

func TestMain(m *testing.M) { postgrestest.Main(m) }

// The queries the MCP security audit (task 2801) found the old
// substring validator passing, and their near relations.
var readOnlyBypasses = []string{
	"WITH x AS (SELECT 1) DELETE\tFROM tasks WHERE 1=0",
	"WITH x AS (SELECT 1) UPDATE\ntasks SET title='x' WHERE 1=0",
	"WITH x AS (SELECT 1) INSERT\rINTO tasks(id) VALUES ('x')",
	"WITH x AS (SELECT 1) DELETE/**/FROM tasks",
	"with x as (select 1) delete from tasks",
	"SELECT 1; DELETE\nFROM tasks",
	"SELECT 1;DELETE FROM tasks",
	"SELECT 1; PRAGMA query_only = OFF",
	"SELECT 1 -- x\r; DELETE FROM tasks",
	"SELECT 1 /* /* */ ' */ ; DELETE FROM tasks; -- '",
	"SELECT $$'$$; DELETE FROM tasks; --'",
	"SELECT $a$'$a$; DELETE FROM tasks; --'",
	"SELECT E'\\''; DELETE FROM tasks; --'",
	"SELECT [ ' ] FROM t; DELETE FROM tasks; --'",
	"SELECT * INTO copy FROM tasks",
	"EXPLAIN ANALYZE DELETE FROM tasks",
	"PRAGMA writable_schema=ON",
	"PRAGMA writable_schema = 1",
	"PRAGMA journal_mode=OFF",
	"PRAGMA journal_mode(OFF)",
	"PRAGMA main.journal_mode = DELETE",
	"PRAGMA user_version(7)",
	"PRAGMA query_only=0",
	"PRAGMA wal_checkpoint",
	"PRAGMA incremental_vacuum",
	"SELECT load_extension('x')",
	"UPDATE tasks SET title = 'x'",
}

func TestValidateReadOnlyQueryRefusesBypasses(t *testing.T) {
	t.Parallel()
	for _, q := range readOnlyBypasses {
		if err := dbaccess.ValidateReadOnlyQuery(q); err == nil {
			t.Errorf("ValidateReadOnlyQuery(%q) = nil, want a refusal", q)
		}
	}
}

func TestValidateReadOnlyQueryAllowsReads(t *testing.T) {
	t.Parallel()
	for _, q := range []string{
		"SELECT id, title FROM tasks LIMIT 10",
		"SELECT 1;",
		"  -- a comment\nSELECT 1 /* another */",
		"SELECT\tid\nFROM tasks WHERE title = 'please delete; or update'",
		`SELECT "update" FROM t`,
		"SELECT replace(title, 'a', 'b') FROM tasks",
		"SELECT * FROM tasks WHERE id = ? AND state = $2",
		"WITH recent AS (SELECT id FROM tasks) SELECT count(*) FROM recent",
		"EXPLAIN QUERY PLAN SELECT * FROM tasks",
		"PRAGMA table_info(tasks)",
		"PRAGMA main.table_info('tasks')",
		"PRAGMA user_version",
		"PRAGMA journal_mode",
		"PRAGMA foreign_key_list(tasks);",
	} {
		if err := dbaccess.ValidateReadOnlyQuery(q); err != nil {
			t.Errorf("ValidateReadOnlyQuery(%q) = %v, want nil", q, err)
		}
	}
}

// writesPastTheValidator are statements a db-read query must not be able
// to make stick even if ValidateReadOnlyQuery let them through.
var writesPastTheValidator = []string{
	"DELETE FROM tasks",
	"UPDATE tasks SET title = 'changed'",
	"WITH x AS (SELECT 1) DELETE FROM tasks",
	"WITH x AS (SELECT 1) UPDATE tasks SET title = 'changed'",
	"INSERT INTO tasks (id, title) VALUES ('t2', 'new') RETURNING id",
}

// sqliteOnlyWrites go further than a single statement, which only
// SQLite's driver runs: they end the read-only transaction, or switch
// query_only off, and then write.
var sqliteOnlyWrites = []string{
	"SELECT 1; DELETE FROM tasks",
	"SELECT 1; PRAGMA query_only = OFF; DELETE FROM tasks",
	"SELECT 1; PRAGMA query_only = OFF; COMMIT; DELETE FROM tasks",
	"SELECT 1; ROLLBACK; PRAGMA query_only = OFF; UPDATE tasks SET title = 'changed'",
}

func TestQueryReadOnlyCannotWriteSQLite(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "grain.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// One connection, so a connection the query left read-only (or
	// otherwise changed) would be the very one the next caller gets.
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode = WAL"); err != nil {
		t.Fatal(err)
	}
	queryReadOnlyCannotWrite(t, ctx, db, append(append([]string{}, writesPastTheValidator...), sqliteOnlyWrites...))
}

func TestQueryReadOnlyCannotWritePostgres(t *testing.T) {
	t.Parallel()
	postgrestest.SkipUnlessAvailable(t)
	_, db := postgrestest.Open(t)
	queryReadOnlyCannotWrite(t, context.Background(), db, writesPastTheValidator)
}

func queryReadOnlyCannotWrite(t *testing.T, ctx context.Context, db *sql.DB, writes []string) {
	t.Helper()
	for _, stmt := range []string{
		"CREATE TABLE IF NOT EXISTS tasks (id TEXT PRIMARY KEY, title TEXT)",
		"DELETE FROM tasks",
		"INSERT INTO tasks (id, title) VALUES ('t1', 'original')",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	var got []string
	if err := dbaccess.QueryReadOnly(ctx, db, "SELECT title FROM tasks WHERE id = ?", []any{"t1"}, func(rows *sql.Rows) error {
		for rows.Next() {
			var title string
			if err := rows.Scan(&title); err != nil {
				return err
			}
			got = append(got, title)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("reading through QueryReadOnly: %v", err)
	}
	if len(got) != 1 || got[0] != "original" {
		t.Fatalf("QueryReadOnly read %q, want [original]", got)
	}

	for _, q := range writes {
		// The error, if any, is not the point: some of these fail and
		// some "succeed" into a rolled-back transaction. What matters is
		// the table afterwards.
		_ = dbaccess.QueryReadOnly(ctx, db, q, nil, func(rows *sql.Rows) error {
			for rows.Next() {
			}
			return rows.Err()
		})
		var n int
		var title sql.NullString
		if err := db.QueryRowContext(ctx, "SELECT count(*), max(title) FROM tasks").Scan(&n, &title); err != nil {
			t.Fatalf("after %q: %v", q, err)
		}
		if n != 1 || title.String != "original" {
			t.Fatalf("after %q through QueryReadOnly: tasks has %d row(s), title %q; want 1 row titled original", q, n, title.String)
		}
	}

	// Nothing the read-only query did is left on the pool: an ordinary
	// write still works.
	if _, err := db.ExecContext(ctx, "UPDATE tasks SET title = 'written' WHERE id = 't1'"); err != nil {
		t.Fatalf("writing after QueryReadOnly: %v", err)
	}
}
