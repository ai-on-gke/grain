package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres"
	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/version"
)

// schemaUpgradeDir is where the outgoing build writes the deployment's
// rows for the incoming one. Under -data-dir, beside the store and the
// working tree, because it has to outlive the restart between them and
// -data-dir is what a redeploy keeps.
func SchemaUpgradeDir(dataDir string) string { return filepath.Join(dataDir, "schema-upgrade") }

// schemaUpgradeManifest names the file inside it that turns a directory
// of table files into an instruction. Everything else in there is an
// ordinary dump, which is what lets `grain state check` be pointed
// straight at it.
const SchemaUpgradeManifest = "upgrade.json"

// schemaUpgradeMaxAge is how long a note stands for this deployment's
// state before grain stops believing it.
//
// The window it closes: an upgrade that cut over and never restarted
// leaves a note armed for whenever the new build does start, which is
// the behaviour we want when that is seconds later and not the behaviour
// we want when it is a fortnight later. The old build goes on serving
// and writing rows the whole time, and importing a fortnight-old dump
// over the store holding them would be a silent rollback of everything
// since -- the one outcome worse than not starting. Past this, grain
// stops and says which of the two ways out to take.
const schemaUpgradeMaxAge = 7 * 24 * time.Hour

// schemaUpgrade is upgrade.json: what the outgoing build knew, for the
// incoming one to check against what it knows.
type schemaUpgrade struct {
	// From is the schema of the store this deployment was running on --
	// and of the dump written beside this file.
	From int `json:"from"`
	// To is the schema of the build this deployment was upgrading to. A
	// build whose own SchemaVersion is not this is not the build the note
	// was written for, and does nothing with it.
	To int `json:"to"`
	// Branch is what the upgrade was onto, and Build the stamp of the
	// process that wrote this. Neither is read by anything: they are for
	// whoever finds the directory and wants to know which upgrade left
	// it.
	Branch    string    `json:"branch,omitempty"`
	Build     string    `json:"build,omitempty"`
	WrittenAt time.Time `json:"writtenAt"`
}

// writeSchemaUpgrade exports db into a fresh schema-upgrade directory and
// records what it is for. It is pkg/upgrade's Prepare hook: the last
// thing the outgoing build does before the ref file is repointed.
//
// It returns the directory, which is what the caller hands the new image
// to rehearse against.
//
// Any earlier directory is removed rather than written over: a dump is a
// set of files, and a table that has since been dropped would otherwise
// leave its rows lying there for the import to pick up.
//
// It cleans up after itself when it fails, and does so here rather than
// leaving it to pkg/upgrade's Discard hook, because only this function
// knows how far it got (grain/task-255). Past the RemoveAll below there
// is nothing under the directory but this call's own half-written work,
// so taking it away costs nothing and saves the data directory a whole
// unusable copy of the deployment's rows. But if that very RemoveAll is
// what failed -- a permissions problem, a busy mount -- then whatever
// survived it is an *earlier* upgrade's note, possibly still pending the
// restart that spends it, and this call has no business making a second
// attempt on it under the name of cleaning up after itself. So the
// cleanup below is armed only once the ground is known to be clear.
func WriteSchemaUpgrade(ctx context.Context, dataDir string, db *sql.DB, branch string, from, to int) (_ string, err error) {
	dir := SchemaUpgradeDir(dataDir)
	if err := os.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("clearing %s: %w", dir, err)
	}
	// Everything from here on is this call's doing, so an error from here
	// on takes it back with it: a failed export is not a migration, and
	// leaving a whole copy of the deployment's rows under the data dir
	// for nothing would sit there until some later upgrade happened to
	// clear it, which could be never.
	defer func() {
		if err != nil {
			if rmErr := os.RemoveAll(dir); rmErr != nil {
				log.Printf("grain: %s was left behind by a schema export that failed, and removing it "+
					"failed too (%v) -- it holds no upgrade note and nothing will act on it, but it is "+
					"a partial copy of this deployment's rows and is safe to delete by hand", dir, rmErr)
			}
		}
	}()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("preparing %s: %w", dir, err)
	}
	// The whole database, both tiers, in one read snapshot -- the same
	// export staterepo.Reformat makes and for the same reason: this is
	// not a tick of the sync loop, it is the only copy of these rows that
	// the next build will ever see.
	if err := staterepo.Export(ctx, db, dir); err != nil {
		return "", fmt.Errorf("exporting this deployment's rows to %s: %w", dir, err)
	}
	if err := staterepo.WriteSchemaVersion(dir, from); err != nil {
		return "", err
	}
	note := schemaUpgrade{From: from, To: to, Branch: branch, WrittenAt: time.Now().UTC(),
		Build: version.Get().Revision}
	data, err := json.MarshalIndent(note, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, SchemaUpgradeManifest), append(data, '\n'), 0o600); err != nil {
		return "", fmt.Errorf("writing %s: %w", filepath.Join(dir, SchemaUpgradeManifest), err)
	}
	return dir, nil
}

// discardSchemaUpgrade removes the note. It is pkg/upgrade's Discard
// hook -- an upgrade that prepared a migration and then did not cut over
// must leave nothing armed behind it -- and it is also how a completed
// migration is spent.
func discardSchemaUpgrade(dataDir string) {
	dir := SchemaUpgradeDir(dataDir)
	if err := os.RemoveAll(dir); err != nil {
		log.Printf("grain: removing %s: %v -- remove it by hand, or a later start will migrate this "+
			"deployment onto rows that were current when it was written", dir, err)
	}
}

// readSchemaUpgrade reads the note, if there is one this build should act
// on at all.
//
// Three things disqualify one, and each is reported rather than passed
// over in silence, because in every case the deployment is about to fail
// to start and the note is the reason a reader is looking here:
//
//   - it names a schema this build does not have. Then it was written for
//     some other build, and this one has no business acting on a dump
//     shaped for it. Left in place: the build it names may yet be the one
//     that starts next.
//   - it is older than schemaUpgradeMaxAge (see that constant).
//   - it is unreadable, which is a note that says nothing.
func readSchemaUpgrade(dataDir string) (schemaUpgrade, bool) {
	path := filepath.Join(SchemaUpgradeDir(dataDir), SchemaUpgradeManifest)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return schemaUpgrade{}, false
	}
	if err != nil {
		log.Printf("grain: %s cannot be read (%v), so grain will not migrate this deployment's schema "+
			"from it", path, err)
		return schemaUpgrade{}, false
	}
	var note schemaUpgrade
	if err := json.Unmarshal(data, &note); err != nil {
		log.Printf("grain: %s is not readable as an upgrade note (%v), so grain will not migrate this "+
			"deployment's schema from it", path, err)
		return schemaUpgrade{}, false
	}
	if note.To != model.SchemaVersion {
		log.Printf("grain: %s was written for a build at schema %d and this build knows %d -- leaving it "+
			"alone, since it says nothing about what this build should do", path, note.To, model.SchemaVersion)
		return schemaUpgrade{}, false
	}
	if age := time.Since(note.WrittenAt); age > schemaUpgradeMaxAge {
		log.Printf("grain: %s was written %s ago, from a deployment that has been running and writing rows "+
			"ever since -- too old to stand for this deployment's state, so grain will not import it. "+
			"Run the upgrade again from the build that is running, or move the store aside by hand and "+
			"run `grain state reformat` to start fresh", path, age.Round(time.Hour))
		return schemaUpgrade{}, false
	}
	return note, true
}

// openStoreForSchemaUpgrade is run's own store open: openStore, plus the
// one recovery that is this file's whole point.
//
// A store at a schema this build cannot use stops the process, and that
// is still the answer whenever nothing has said what happened to it. When
// the note beside it *has* said so -- and says this build is the one it
// was written for, and that the store's own stamp is the schema it was
// written from -- the store is moved aside under a timestamped name and a
// fresh one takes its place, filled from the dump the note carries.
//
// The archived store is never removed. It is the only copy of anything
// the migration could not carry (a column the new schema does not have)
// or did not see (a row written in the seconds between the export and the
// restart), so it is what an operator goes to when they want to know what
// a schema change cost them.
//
// It reports whether it migrated, because the state repository holds the
// same rows at the same old schema and the load that follows has to know
// that the answer there is "rewrite it" rather than "refuse to start".
func openStoreForSchemaUpgrade(ctx context.Context, driver, dsn, dataDir string) (*model.Store, *sql.DB, bool, error) {
	store, db, err := OpenStore(driver, dsn, dataDir)
	if err == nil {
		// A store that opens perfectly well, with the note still beside
		// it, is the middle of a migration this process did not start:
		// an earlier start rebuilt the store and died -- or was killed --
		// before the state repository's dump was rewritten from it. The
		// note is deliberately not removed until both halves are done
		// (run's own comment), and this is the half it is still standing
		// for. Without this the next start would meet a dump it cannot
		// read with nothing authorising a rewrite, and stop: the same
		// restart loop this file exists to end, one step further along.
		if note, ok := readSchemaUpgrade(dataDir); ok {
			log.Printf("grain: the store is already at schema %d and %s is still here, so the state "+
				"repository has not been brought across yet -- doing that now", note.To,
				SchemaUpgradeDir(dataDir))
			return store, db, true, nil
		}
		return store, db, false, nil
	}
	if !errors.Is(err, model.ErrSchemaTooOld) && !errors.Is(err, model.ErrSchemaTooNew) {
		return nil, nil, false, err
	}
	note, ok := readSchemaUpgrade(dataDir)
	if !ok {
		return nil, nil, false, err
	}
	// Opened again, without Init this time, purely to ask the store what
	// it is stamped with: the note only authorises a migration away from
	// the schema it was written from, and a store at any other version is
	// one this note does not describe.
	found, verr := storeSchemaVersion(ctx, driver, dsn, dataDir)
	if verr != nil {
		return nil, nil, false, fmt.Errorf("%w (and the store's own schema could not be read to check the "+
			"upgrade note against it: %v)", err, verr)
	}
	if found != note.From {
		return nil, nil, false, fmt.Errorf("%w -- %s says this deployment was upgrading from schema %d, "+
			"and the store is at %d, so the rows in it are not the rows that note describes",
			err, filepath.Join(SchemaUpgradeDir(dataDir), SchemaUpgradeManifest), note.From, found)
	}

	archived, err := archiveStore(ctx, driver, dsn, dataDir, found)
	if err != nil {
		return nil, nil, false, err
	}
	log.Printf("grain: this deployment's schema changed from %d to %d. The store written at %d is now %s "+
		"-- it is not deleted, and it is where anything this migration cannot carry stays -- and grain is "+
		"building a fresh one at %d from the rows the upgrade exported to %s",
		found, model.SchemaVersion, found, archived, model.SchemaVersion, SchemaUpgradeDir(dataDir))

	store, db, err = OpenStore(driver, dsn, dataDir)
	if err != nil {
		return nil, nil, false, fmt.Errorf("creating a store at schema %d after moving the old one to %s: %w",
			model.SchemaVersion, archived, err)
	}
	if err := importSchemaUpgrade(ctx, dataDir, db, note); err != nil {
		db.Close()
		return nil, nil, false, fmt.Errorf("%w (the store it came from is still here, at %s)", err, archived)
	}
	return store, db, true, nil
}

// importSchemaUpgrade fills a freshly created store from the note's dump.
//
// staterepo.MigrateDump is what stands between "these files" and "this
// schema": for most bumps it hands the directory straight back, because
// Import already skips a column this build does not have and treats a
// table it has no file for as empty, and that tolerance *is* the
// migration. For the bumps it does not cover, that is where the
// transformation lives (pkg/staterepo/migrate.go).
func importSchemaUpgrade(ctx context.Context, dataDir string, db *sql.DB, note schemaUpgrade) error {
	dir := SchemaUpgradeDir(dataDir)
	from, err := staterepo.MigrateDump(dir, dataDir, note.From, model.SchemaVersion)
	if err != nil {
		return fmt.Errorf("migrating the dump in %s from schema %d to %d: %w",
			dir, note.From, model.SchemaVersion, err)
	}
	if err := staterepo.ImportMigrated(ctx, db, from); err != nil {
		return fmt.Errorf("importing %s into a fresh store at schema %d: %w", from, model.SchemaVersion, err)
	}
	store := model.New(db)
	// Init ran on the empty store before these rows arrived, so the
	// startup guard asks again of what did (model.Store.CheckSingleTenant),
	// before anything below adds to them.
	// The rehearsal before the cutover asked the same question of the
	// same dump; this is the answer for an upgrade that skipped it.
	if err := store.CheckSingleTenant(ctx); err != nil {
		return err
	}
	log.Printf("grain: imported this deployment's rows from %s into a fresh store at schema %d",
		dir, model.SchemaVersion)
	return nil
}

// archiveStore moves the store at schema found out of the way, so the
// next OpenStore creates a fresh one, and says where it went. Nothing is
// deleted on either driver.
//
// SQLite's store is a directory, renamed beside itself. PostgreSQL's is
// the tables in the connection's current schema, which are moved into a
// schema of their own in the same database (postgres.ArchiveStore) --
// the one way to set a database's rows aside that needs no second
// database, no superuser and no copy.
func archiveStore(ctx context.Context, driver, dsn, dataDir string, found int) (string, error) {
	suffix := fmt.Sprintf("schema%d", found) + "-" + timestampSuffix()
	if driver != "postgres" {
		storeDir := dsn
		if storeDir == "" {
			storeDir = filepath.Join(dataDir, "store")
		}
		archived := filepath.Join(filepath.Dir(storeDir), filepath.Base(storeDir)+"."+suffix)
		if err := os.Rename(storeDir, archived); err != nil {
			return "", fmt.Errorf("moving the store at schema %d aside to %s: %w", found, archived, err)
		}
		return archived, nil
	}
	db, err := openRawDB(driver, dsn, dataDir)
	if err != nil {
		return "", fmt.Errorf("opening the postgres store at schema %d to move it aside: %w", found, err)
	}
	defer db.Close()
	schema, err := postgres.ArchiveStore(ctx, db, "_"+strings.ReplaceAll(suffix, "-", "_"))
	if err != nil {
		return "", fmt.Errorf("moving the postgres store at schema %d aside: %w", found, err)
	}
	return fmt.Sprintf("postgres schema %q", schema), nil
}

// storeSchemaVersion reads the version stamped in the store without
// creating or altering anything -- Store.SchemaVersion over a connection
// this opens and closes, since the one run holds has been closed by the
// failed open that got us here.
func storeSchemaVersion(ctx context.Context, driver, dsn, dataDir string) (int, error) {
	if driver == "" {
		driver = "sqlite"
	}
	if driver != "sqlite" && driver != "postgres" {
		return 0, fmt.Errorf("unsupported driver %q for reading schema version", driver)
	}
	db, err := openRawDB(driver, dsn, dataDir)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	return model.New(db).SchemaVersion(ctx)
}

// ErrNoStore is PrepareStoreSchemaUpgrade's answer for a data directory
// whose database holds no store grain has stamped -- a brand new host,
// or a Postgres database nothing has initialised yet -- where there are
// no rows for a schema change to carry or to lose.
var ErrNoStore = errors.New("no store in this data directory's database")

// PrepareStoreSchemaUpgrade is WriteSchemaUpgrade for the other way a
// deployment changes build: scripts/setup.sh, re-run on the host, which
// has already installed the new build by the time it notices the schema
// moved and so has no old build running to ask for the export
// (grain/task-4357). Before this, that path's only answer was to move the
// store aside and start empty -- every user and task gone -- while the
// Upgrade button carried the very same change across with its rows.
//
// The new build can write the note itself because the dump does not
// depend on which build reads it: staterepo.Export walks the tables the
// database actually has, at whatever schema it is stamped with, and the
// note records that stamp as From. So this opens the store without
// Init, reads its stamp, and when that is not this build's schema writes
// the same note the button writes -- after which the daemon's next start
// migrates exactly as it does after the button (openStoreForSchemaUpgrade,
// then migrateStateRepo for the state repository).
//
// It returns the store's schema, and the note's directory when it wrote
// one. A store already at this build's schema writes nothing and returns
// an empty directory: whatever the caller's own marker said, there is
// nothing to migrate, and a note still standing from an interrupted
// migration (the store rebuilt, the state repository not yet) is left
// for the daemon to finish rather than overwritten.
//
// The store is the one the daemon on dataDir runs on (ResolveDatabase):
// the embedded SQLite store, or the Postgres database the daemon
// recorded in db-config.json or the environment names. Both are carried
// the same way; only where the next start keeps the old store differs
// (archiveStore).
//
// The caller is expected to have stopped the daemon: rows written after
// the export are in the archived store and nowhere else.
func PrepareStoreSchemaUpgrade(ctx context.Context, dataDir, branch string) (int, string, error) {
	driver, dsn, err := ResolveDatabase(dataDir)
	if err != nil {
		return 0, "", err
	}
	where := "the postgres database"
	if driver != "postgres" {
		storeDir := dsn
		if storeDir == "" {
			storeDir = filepath.Join(dataDir, "store")
		}
		if _, err := os.Stat(storeDir); errors.Is(err, os.ErrNotExist) {
			return 0, "", ErrNoStore
		} else if err != nil {
			return 0, "", err
		}
		where = storeDir
	}
	db, err := openRawDB(driver, dsn, dataDir)
	if err != nil {
		return 0, "", fmt.Errorf("opening the store in %s: %w", where, err)
	}
	defer db.Close()
	found, err := model.New(db).SchemaVersion(ctx)
	if err != nil {
		return 0, "", fmt.Errorf("reading the schema the store in %s is stamped with: %w", where, err)
	}
	if found == 0 {
		// No stamp is a store no build has ever initialised -- the
		// directory or database, with nothing grain wrote in it.
		return 0, "", ErrNoStore
	}
	if found == model.SchemaVersion {
		return found, "", nil
	}
	if err := staterepo.Migratable(found, model.SchemaVersion); err != nil {
		return found, "", err
	}
	dir, err := WriteSchemaUpgrade(ctx, dataDir, db, branch, found, model.SchemaVersion)
	if err != nil {
		return found, "", err
	}
	return found, dir, nil
}

// DiscardSchemaUpgrade removes a note PrepareStoreSchemaUpgrade wrote,
// for a caller whose rehearsal of it failed.
func DiscardSchemaUpgrade(dataDir string) { discardSchemaUpgrade(dataDir) }

// PostgresStoreSchema reads the schema the Postgres database a daemon on
// dataDir would run on (dsn, or the included server when dsn is empty)
// is stamped with, without initialising it the way OpenStore would: 0 is
// a database no build has stamped. It never falls back to the embedded
// SQLite store, so a caller checking a Postgres data directory does not
// leave one behind in it.
func PostgresStoreSchema(ctx context.Context, dsn, dataDir string) (int, error) {
	if dsn == "" && !postgres.IsAvailable() {
		return 0, errors.New("postgres DSN is required")
	}
	db, err := openRawDB("postgres", dsn, dataDir)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	return model.New(db).SchemaVersion(ctx)
}
