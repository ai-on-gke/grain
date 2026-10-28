// Package sqlite opens an embedded SQLite database for the task store.
//
// This is the only package that imports a SQLite driver. Everything
// above it takes a *sql.DB, so the model and the store are testable
// against anything that speaks SQL and carry none of this dependency
// tree.
//
// The driver is modernc.org/sqlite, a pure-Go transpilation of SQLite
// with no cgo involved: the binary that embeds it needs no C toolchain,
// no shared library the target host has to already carry (the ICU/glibc
// coupling embedded Dolt required -- see the Makefile's git history for
// what that used to cost), and no server process anywhere.
//
// Multiple processes -- a daemon, a UI, a CLI, all writing -- open the
// same database file directly rather than dialing a server: SQLite's own
// file locking is what serialises them, in WAL mode so readers never
// block on a writer. Open sets that up. There is no second constructor
// here the way pkg/model/dolt had Connect/OpenOrConnect for a SQL
// server, because SQLite has no wire protocol to dial in the first
// place -- every writer just opens the file.
package sqlite

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Config names the database file to open.
type Config struct {
	Dir  string // the directory holding the database file
	Name string // the database's file name, without its ".db" extension
}

// DefaultConfig is what a deployment gets without saying otherwise.
func DefaultConfig(dir string) Config {
	return Config{Dir: dir, Name: "grain"}
}

// Open connects to an embedded SQLite database, creating the directory
// and the database file if either is absent.
//
// busy_timeout and _txlock=immediate together are what make Store.write
// correct (store.go's own doc comment on write): every transaction takes
// SQLite's write lock up front, at BEGIN rather than at the first write
// statement, so two overlapping writers fail (or block, until
// busy_timeout elapses) at the same point every time instead of one
// succeeding partway through and losing to the other at COMMIT the way a
// deferred transaction would. A blocked writer is given five seconds to
// simply wait its turn before Store.write's own retry loop ever sees an
// error -- worth having on top of the retry loop, not instead of it,
// since a request that outlasts busy_timeout still needs somewhere to
// go.
//
// synchronous=NORMAL is a durability trade made deliberately, and it is
// the one setting here worth arguing with rather than skimming. SQLite's
// default, FULL, fsyncs the WAL on every single commit. NORMAL fsyncs at
// a checkpoint instead: it is what SQLite's own WAL documentation
// recommends for WAL mode, it cannot corrupt the database under any
// circumstance, and it weakens nothing above it -- the write lock, the
// serialisation and the isolation _txlock=immediate buys are untouched,
// and a crash of the *process* still loses nothing, because a committed
// frame is in the OS page cache and whoever opens the file next reads
// it. What is given up is durability against the machine itself going.
// An OS panic or a power cut can lose transactions the kernel had not
// yet written back -- seconds of them at the default writeback interval,
// not the whole WAL, but seconds of commits grain had already told
// somebody had landed.
//
// The reason that is the right trade here is that grain's database is a
// record of work whose durable artifacts live somewhere else. Branches,
// pull requests, check runs and review comments are on GitHub, and every
// reconciler re-reads GitHub and the store each tick rather than acting
// on a change it was handed (README's "Reconcilers, not a pipeline"), so
// a lost tail is largely re-derived rather than mourned. The same event
// destroys every sandbox in flight anyway, so a run row that is a few
// seconds stale is not what makes that restart hard. And pkg/staterepo
// exports the whole database to a git repository on a timer and pushes
// it, so these rows are not the only copy -- that export trails the
// database and so is no way to recover the tail, but it is why losing
// the tail is not losing the record.
//
// One hazard this genuinely widens, and it should be named rather than
// waved at: the persistence-before-irreversible-effect ordering intake
// relies on. Intake writes the task, and only then takes the trigger
// label off the issue, so a failure anywhere leaves the issue labelled
// and retried next tick. Under FULL the commit is on disk before the
// label comes off; under NORMAL it is only in the page cache, so a power
// cut in the seconds after can leave an issue whose label is gone and
// whose task never existed -- and nothing re-derives that one, because
// the label was the trigger. It needs a power cut inside a few seconds
// of a filing, the loss is one issue rather than the store, and a human
// who never saw their issue picked up can re-label it. That is the
// concrete thing being bought with the fsyncs below, and it is judged
// too small to buy.
//
// What it costs to keep FULL, measured on this repository's own
// store-backed tests over three alternating runs of each: pkg/model/sqlite
// took 126-132s at FULL against 54-65s at NORMAL, and pkg/staterepo --
// noisier, because most of its time goes on shelling out to git rather
// than on committing -- 174-220s against 107-178s. That is an fsync per
// commit on a container filesystem, where one measures 30-45ms; a
// deployment on a real disk pays far less per commit, so the win outside
// a container is proportionately smaller -- but grain runs in containers
// often enough (CI, `grain state check`, a daemon in a sandbox) that it
// is not only a test-suite number. It is set here rather than offered as
// a knob on Config because it is a judgement about grain's data, not a
// preference: a deployment that could turn it off is a deployment that
// can get it wrong, and a test suite running at a durability setting no
// deployment runs would be measuring a store nobody has.
func Open(cfg Config) (*sql.DB, error) {
	if cfg.Dir == "" {
		return nil, fmt.Errorf("sqlite: no directory configured")
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("sqlite: preparing %s: %w", cfg.Dir, err)
	}
	name := cfg.Name
	if name == "" {
		name = "grain"
	}
	path := filepath.Join(cfg.Dir, name+".db")

	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("sqlite: opening %s: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: connecting to %s: %w", path, err)
	}
	// WAL is what lets a reader run concurrently with the one writer that
	// holds the write lock, rather than every access serialising through
	// SQLite's older rollback-journal locking -- the property that makes
	// "a daemon, a UI and a CLI all pointed at the same file" work at all.
	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: enabling WAL on %s: %w", path, err)
	}
	return db, nil
}

// dsn builds the driver's connection string. The query is assembled
// with net/url so each pragma's parentheses are escaped; the path itself
// goes in verbatim, which the driver reads up to the first '?'.
func dsn(path string) string {
	q := url.Values{}
	// One _pragma per setting -- modernc.org/sqlite applies each on every
	// new connection the pool opens, which a bare PRAGMA statement run
	// once after Open would not: database/sql may open more connections
	// later, and each needs its own busy_timeout, foreign_keys and txlock
	// mode. (journal_mode is the exception: WAL is recorded in the
	// database file itself, so Open sets it once.)
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	// synchronous(1) is NORMAL, spelled numerically because that is the
	// value PRAGMA synchronous reads back as -- see Open's doc comment
	// for what this trades away and why.
	q.Add("_pragma", "synchronous(1)")
	q.Set("_txlock", "immediate")
	return "file:" + path + "?" + q.Encode()
}
