package sqlite_test

// What Open puts on a connection, checked on connections the pool opened
// rather than on the first one. Each of these is a decision with a
// paragraph of reasoning behind it in sqlite.go -- WAL so a reader never
// blocks on the writer, synchronous=NORMAL trading durability against a
// power cut for the fsync every commit would otherwise pay -- and each
// is a one-line change away from silently not being set, since a DSN
// pragma the driver never applies fails no query and shows up only as
// behaviour nobody was watching.

import (
	"context"
	"database/sql"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

func TestOpenSetsPragmasOnEveryConnection(t *testing.T) {
	t.Parallel()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	// Held open together, so the pool has to open a second and a third
	// connection rather than handing the same one back each time: the
	// _pragma settings ride on the DSN precisely so a connection opened
	// later carries them too, and checking one connection would not
	// notice if they did not.
	const conns = 3
	var held []*sql.Conn
	for i := 0; i < conns; i++ {
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("taking connection %d from the pool: %v", i, err)
		}
		defer c.Close()
		held = append(held, c)
	}

	for i, c := range held {
		var synchronous int
		if err := c.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
			t.Fatalf("reading synchronous on connection %d: %v", i, err)
		}
		// 1 is NORMAL. 2, SQLite's default, is FULL -- an fsync on every
		// commit, which is what this deployment decided not to pay.
		if synchronous != 1 {
			t.Errorf("connection %d: synchronous = %d, want 1 (NORMAL)", i, synchronous)
		}

		var busy int
		if err := c.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil {
			t.Fatalf("reading busy_timeout on connection %d: %v", i, err)
		}
		if busy != 5000 {
			t.Errorf("connection %d: busy_timeout = %d, want 5000", i, busy)
		}

		var journal string
		if err := c.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
			t.Fatalf("reading journal_mode on connection %d: %v", i, err)
		}
		if journal != "wal" {
			t.Errorf("connection %d: journal_mode = %q, want %q", i, journal, "wal")
		}

		// foreign_keys is per connection; a PRAGMA run once after Open
		// reached only whichever connection the pool handed it.
		var foreignKeys int
		if err := c.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
			t.Fatalf("reading foreign_keys on connection %d: %v", i, err)
		}
		if foreignKeys != 1 {
			t.Errorf("connection %d: foreign_keys = %d, want 1", i, foreignKeys)
		}
	}
}
