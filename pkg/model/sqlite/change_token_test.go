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

package sqlite_test

// Store.ChangeToken against a real embedded SQLite database. These are
// here rather than in pkg/model because the whole of what the token is
// worth is a property of the engine underneath -- what PRAGMA
// data_version does and does not move for -- and pkg/model's own tests
// have no engine.

import (
	"context"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

func changeToken(t *testing.T, store *model.Store, ctx context.Context) string {
	t.Helper()
	token, err := store.ChangeToken(ctx)
	if err != nil {
		t.Fatalf("ChangeToken: %v", err)
	}
	if token == "" {
		t.Fatal("ChangeToken returned an empty token with no error")
	}
	return token
}

// TestChangeTokenHoldsStillWhenNothingIsCommitted is the half of the
// contract a poller leans on: two readings with nothing in between are
// equal, and so are two readings with a *read* in between. Without this
// the token would be worth nothing -- a value that changed on its own
// would send every poll back to the store anyway.
func TestChangeTokenHoldsStillWhenNothingIsCommitted(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	first := changeToken(t, store, ctx)
	if second := changeToken(t, store, ctx); second != first {
		t.Errorf("ChangeToken moved with nothing in between: %s then %s", first, second)
	}
	if _, err := store.ListTasks(ctx); err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if after := changeToken(t, store, ctx); after != first {
		t.Errorf("ChangeToken moved across a read: %s then %s", first, after)
	}
}

// TestChangeTokenMovesForThisStoresOwnWrite is the trap the dedicated
// connection exists for, and the one that would make a stale answer
// possible if it were ever lost: the daemon writing and the UI reading
// are one process over one *sql.DB pool, so a write through this very
// store must move the token this very store reports. PRAGMA
// data_version does not report a connection its *own* writes, so this
// passes only for as long as ChangeToken keeps a connection that never
// writes.
func TestChangeTokenMovesForThisStoresOwnWrite(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	before := changeToken(t, store, ctx)
	commitsBefore := store.CommitCount()
	if err := store.PutTask(ctx, task("1", false)); err != nil {
		t.Fatalf("PutTask: %v", err)
	}
	after := changeToken(t, store, ctx)
	if after == before {
		t.Fatalf("ChangeToken = %s across a write through the same store, want it moved", after)
	}
	if got := store.CommitCount() - commitsBefore; got != 1 {
		t.Errorf("CommitCount moved by %d across one write, want 1", got)
	}

	// And it holds still again afterwards, so a writing deployment does
	// not simply read as "always changed".
	if again := changeToken(t, store, ctx); again != after {
		t.Errorf("ChangeToken moved again with nothing in between: %s then %s", after, again)
	}
}

// TestChangeTokenMovesForAnotherProcessesWrite: a daemon, a UI and a CLI
// open the same file rather than dialing a server (pkg/model/sqlite's
// own doc comment), so a token blind to another process's commit would
// be a UI that never noticed a task the CLI filed. A second *sql.DB over
// the same file is what that other process looks like from here.
func TestChangeTokenMovesForAnotherProcessesWrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.Config{Dir: dir, Name: "grain"})
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}

	other, err := sqlite.Open(sqlite.Config{Dir: dir, Name: "grain"})
	if err != nil {
		t.Fatalf("opening the same database again: %v", err)
	}
	t.Cleanup(func() { other.Close() })

	before := changeToken(t, store, ctx)
	if err := model.New(other).PutTask(ctx, task("1", false)); err != nil {
		t.Fatalf("PutTask through the second connection: %v", err)
	}
	if after := changeToken(t, store, ctx); after == before {
		t.Fatalf("ChangeToken = %s across another connection's write, want it moved", after)
	}
}

// TestChangeTokenIgnoresAWriteThatWroteNothing: the counter is bumped by
// a commit that actually wrote a page, not by a transaction being
// opened. A rolled-back write and an UPDATE that matched no rows both
// left the database exactly as they found it, and a poller rebuilding
// for either would be paying for a change nobody made.
func TestChangeTokenIgnoresAWriteThatWroteNothing(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)

	before := changeToken(t, store, ctx)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("beginning a transaction: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO `task_tag` (`task_id`,`tag`) VALUES ('1','x')"); err != nil {
		t.Fatalf("writing inside it: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rolling it back: %v", err)
	}
	if after := changeToken(t, store, ctx); after != before {
		t.Errorf("ChangeToken moved across a rolled-back write: %s then %s", before, after)
	}

	if _, err := db.ExecContext(ctx, "UPDATE `task` SET `title` = 'x' WHERE `id` = 'nobody'"); err != nil {
		t.Fatalf("running an update that matches nothing: %v", err)
	}
	if after := changeToken(t, store, ctx); after != before {
		t.Errorf("ChangeToken moved across an update that matched no rows: %s then %s", before, after)
	}
}

// TestChangeTokenErrorsWhenItCannotAsk: the other half of the contract
// is what a caller does when the store cannot answer, so the error has
// to actually arrive rather than an empty token passing for a real one.
// A closed database is the reachable version of that; a store behind an
// engine with no PRAGMA data_version is the other, and both leave a
// caller with the same "assume it changed".
func TestChangeTokenErrorsWhenItCannotAsk(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.Config{Dir: dir, Name: "grain"})
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	if err := model.New(db).Init(context.Background()); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("closing the database: %v", err)
	}
	if token, err := model.New(db).ChangeToken(context.Background()); err == nil {
		t.Errorf("ChangeToken = %q, nil on a closed database, want an error", token)
	}
}

// TestChangeTokenDeclinesAPoolOfOne: the connection this holds is only
// free because the pool is unbounded. On a pool capped at one -- what
// pkg/staterepo's migration check sets on its throwaway database -- the
// same connection is the only connection, and holding it forever would
// hang the next query rather than merely slow it down. Declining is what
// keeps that cap meaning what whoever set it meant, and the caller's own
// "an error means assume it changed" covers the rest.
func TestChangeTokenDeclinesAPoolOfOne(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	db.SetMaxOpenConns(1)

	if token, err := store.ChangeToken(ctx); err == nil {
		t.Fatalf("ChangeToken = %q, nil on a pool of one connection, want an error", token)
	}
	// And the store still works, which is the whole point of declining:
	// the connection it would have held is the one this read needs.
	if _, err := store.ListTasks(ctx); err != nil {
		t.Fatalf("ListTasks after ChangeToken declined: %v", err)
	}
}

// TestChangeTokenNeverReusesATokenAcrossAReconnect: a call that fails
// drops the connection it failed on, and the counter the replacement
// reads is a different connection's counter -- unrelated to the old
// one's, and free to land on the same number. The generation in the
// token is what stops that collision reading as "nothing has changed",
// and this is what pins it: nothing at all was committed here, and the
// token still has to come back different.
func TestChangeTokenNeverReusesATokenAcrossAReconnect(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	before := changeToken(t, store, ctx)

	// A cancelled context fails the pragma on the connection ChangeToken
	// holds, which is what makes it drop it -- the reachable stand-in
	// for the driver-level failure this guards against.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.ChangeToken(cancelled); err == nil {
		t.Fatal("ChangeToken on a cancelled context returned no error")
	}

	if after := changeToken(t, store, ctx); after == before {
		t.Errorf("ChangeToken = %s on both sides of a reconnect, want a token that cannot be mistaken for the old one", after)
	}
}

// TestStoreCloseGivesBackTheChangeTokenConnection: the connection
// ChangeToken holds is checked out of the pool, so sql.DB.Close alone
// leaves it open. Store.Close is what returns it, and a ChangeToken
// after it still answers, from a fresh connection.
func TestStoreCloseGivesBackTheChangeTokenConnection(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)

	first := changeToken(t, store, ctx)
	if inUse := db.Stats().InUse; inUse != 1 {
		t.Fatalf("after ChangeToken: %d connection(s) in use, want the 1 it holds", inUse)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if inUse := db.Stats().InUse; inUse != 0 {
		t.Errorf("after Close: %d connection(s) still in use, want 0", inUse)
	}
	if err := store.Close(); err != nil {
		t.Errorf("a second Close: %v", err)
	}
	if again := changeToken(t, store, ctx); again == first {
		t.Errorf("ChangeToken after Close reused the token %s from the connection it gave back", first)
	}
}
