package model

import (
	"context"
	"fmt"
)

// "Has anything at all changed?", asked without reading anything.
//
// A poller is the caller this exists for. The UI re-reads GET /api/tasks
// every three seconds in every open tab, and on a store with real
// history that answer is 1.7 MB built out of a scan of most of the
// database -- about a second of it -- to say, nearly every time, exactly
// what it said three seconds ago
// (docs/hand-runs/2026-09-07-panel-dashboard.md, section 5). What that
// poll needs first is a question whose answer is cheap and whose "no"
// can be trusted: nothing has been committed since you last asked, so
// the answer you already have is still the answer.
//
// # What SQLite can say, and what it costs
//
// PRAGMA data_version is exactly that question. SQLite keeps a change
// counter in the database header and bumps it on every commit that
// writes a page; the pragma reports the value this connection last saw,
// so two readings that differ mean somebody committed in between. It
// reads the header and nothing else -- 11-19us against this repository's
// own driver, measured on the sandbox this was written in, against the
// ~1s the answer it guards costs to build.
//
// Three of its properties are what make it usable here, and all three
// are pinned by change_token_test.go rather than taken on trust:
//
//   - it sees another *process*. A daemon, a UI and a CLI all open the
//     same file directly (pkg/model/sqlite's own doc comment), so a
//     counter this package kept in memory would be blind to two of the
//     three. This is read out of the file.
//   - it does not move on a rollback, or on an UPDATE that matched no
//     rows. Nothing was committed, so nothing changed, and a poll that
//     rebuilt for those would be rebuilding for a write that never
//     happened.
//   - it does move for a commit made through the *same* *sql.DB pool,
//     on some other connection of it -- which is the case that matters
//     most, because in a real deployment the daemon writing and the UI
//     reading are one process sharing one pool
//     (bwsalmon/agents#363).
//
// # Why a connection of its own
//
// The one thing data_version will not do is report a connection's own
// writes back to it: SQLite defines it as "changes committed by any
// *other* connection". Read through database/sql's pool that is a trap
// with teeth, because the pool hands out whichever connection is free
// and the pool grain writes through is the pool it reads through -- so
// the very connection that just wrote the task would be the one
// reporting that nothing had changed. That is a stale answer, which is
// the one failure mode a poller must not have.
//
// So this holds a connection of its own, acquired lazily and used for
// nothing but this pragma. It never writes, so "any other connection"
// is every connection that ever writes, and the exception cannot bite.
//
// # Why the answer is an opaque token rather than the number
//
// Because data_version's values are only meaningful compared against
// *the same connection's* earlier readings, and this one is not
// guaranteed to live forever: a driver error, or a closed database,
// drops it and the next call acquires a fresh one whose counter starts
// somewhere unrelated. Two unrelated counters could collide on the same
// number, and a caller comparing bare integers would read that collision
// as "nothing changed". The generation number is in the token so they
// cannot: a reconnect changes the token, the caller rebuilds once, and
// the trade is a wasted rebuild rather than a stale answer.
//
// The rule for a caller is therefore the whole contract: **equal tokens
// mean nothing was committed in between; anything else, including an
// error, means assume it did.** Never that a changed token means the
// answer changed -- a commit anywhere in the database moves it, so a
// telemetry row written while nobody touched a task will cost a poller
// one rebuild. That is the right direction to be wrong in.
//
// # The one pool it will not do this on
//
// Holding a connection is only free because database/sql's default pool
// is unbounded, so the one held here is one nobody else was waiting for.
// A pool capped at a single connection is the exception, and taking that
// connection and never giving it back would not be a slow store -- it
// would be a store where the next query blocks forever. So a capped-at-1
// pool is declined rather than served: the caller gets an error, reads
// it as "assume it changed", and the cap keeps meaning what whoever set
// it meant. pkg/staterepo's own migration check is the one place in this
// repository that sets it (on a throwaway database, so that its import
// runs under the same pragmas a daemon's would), and it never asks this
// question -- but a future caller that does should not have to discover
// this by hanging.
//
// # Other engines
//
// PRAGMA is SQLite's, and this package deliberately imports no driver
// and speaks to "anything that speaks SQL" (Store's own doc comment). A
// database that does not understand the pragma returns an error here, on
// every call, and every caller's rule for an error is "assume it
// changed" -- so the feature degrades to exactly the behaviour that
// existed before it, on a store that cannot answer, without anything
// above having to know which kind of store it has.
func (s *Store) ChangeToken(ctx context.Context) (string, error) {
	s.changeMu.Lock()
	defer s.changeMu.Unlock()
	if s.changeConn == nil {
		// Checked here rather than on every call: it is a property of the
		// pool, and this is the only moment that takes anything from it.
		if s.db.Stats().MaxOpenConnections == 1 {
			return "", fmt.Errorf("change token: this store's pool allows one connection, which cannot be the one held open for this")
		}
		conn, err := s.db.Conn(ctx)
		if err != nil {
			return "", fmt.Errorf("change token: taking a connection: %w", err)
		}
		s.changeConn, s.changeGen = conn, s.changeGen+1
	}
	var version int64
	if err := s.changeConn.QueryRowContext(ctx, "PRAGMA data_version").Scan(&version); err != nil {
		// Dropped rather than kept: a connection that has failed once may
		// be finished (a cancelled query poisons it, and sql.ErrConnDone
		// is what a closed database gives), and a caller retrying against
		// a dead connection would get an error forever. The generation
		// number is what keeps the replacement's own counter from being
		// mistaken for this one's.
		_ = s.changeConn.Close()
		s.changeConn = nil
		return "", fmt.Errorf("change token: %w", err)
	}
	return fmt.Sprintf("%d:%d", s.changeGen, version), nil
}

// Close gives back the connection ChangeToken holds, if it took one. It
// does not close the database: New was handed that already open, and
// whoever opened it still closes it -- after this, because sql.DB.Close
// only closes the connections in its pool, and the one held here is
// checked out of it, so it would stay open behind a closed database
// with all of SQLite's own memory for it. That is nothing to a daemon,
// which opens one store for its whole life, and a great deal to anything
// that opens a store per step: tests/e2e's withStore does it several
// times a round, and TestRandomizedClusterLong ran out of memory at
// about a megabyte a round before this. A later ChangeToken takes a
// fresh connection, under a new generation, as it would after an error.
func (s *Store) Close() error {
	s.changeMu.Lock()
	defer s.changeMu.Unlock()
	if s.changeConn == nil {
		return nil
	}
	err := s.changeConn.Close()
	s.changeConn = nil
	return err
}
