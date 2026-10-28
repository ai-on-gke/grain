package staterepo

// Migrating: carrying a deployment's rows across a schema change instead
// of leaving them behind.
//
// reformat.go is the other answer to the same question, and it was the
// only one for as long as a schema change only ever arrived through
// scripts/setup.sh: that script moves the store aside before the new
// build ever opens it, so by the time anything here runs the rows are
// already gone and all that is left to decide is what the repository
// should say about it. The UI's Upgrade button changes nothing on disk,
// which is why it needed an answer of its own (grain/task-59), and the
// answer it wanted was not "lose the state faster".
//
// What makes a migration possible at all is that a dump is not a
// database. Import already skips a column this build does not have and
// treats a table it has no file for as empty (dump.go), because that is
// what a dump written by a *different* build looks like -- so the shape
// of a schema change that adds a nullable column, drops a column, or
// adds a table is one the importer already absorbs. That is the whole
// trick: the store cannot be migrated in place (Init's CREATE TABLE IF
// NOT EXISTS never alters a table that is already there, which is what
// model.SchemaVersion exists to say), but the rows *can* be re-imported
// into a fresh database at the new schema, and the dump is the form they
// take that survives the trip.
//
// It is not unconditional, and the two things that bound it are here:
//
//   - model.BaselineSchemaVersion, a floor. A dump from before the
//     single-tenant baseline was written by a multi-tenant build, and
//     nothing here reads one forward: it is refused in the startup
//     guard's words (model.CheckSchemaBaseline).
//   - dumpFixups, the escape hatch for a bump the importer's tolerance
//     does not cover on its own -- a renamed column, a new NOT NULL one
//     with no default, a table split in two, a column moving between
//     tables (schema 22's, default_team_settings.go), rows moving
//     between tables (schema 23's, team_repo_settings.go). The test
//     that keeps it honest is pkg/staterepo's own golden dump: a real
//     export, checked in at the schema it was made at, imported into
//     whatever schema this build knows. A bump that breaks the import
//     fails that test, in the pull request that makes the bump, rather
//     than on a deployment.
//
// From grain 2.0.0 (model.V2BaselineSchemaVersion) on, this is no longer
// how a deployment crosses a schema bump: every bump ships an in-place
// migration (pkg/model/migrations.go), and the store never leaves the
// database it is in. A dump must still import, because it is the
// backup a restore and `grain state check -migrate` read, so a bump
// the importer does not absorb still brings its dumpFixups step;
// pkg/model/v2baseline imports a frozen dump 2.0.0 wrote to hold every
// later build to that.
//
// A migration that is refused here is not the end of the deployment: it
// is the point at which the destructive path (reformat.go) is the honest
// answer again, and cmd/grain says so in as many words.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// ErrNotMigratable is returned when a dump cannot be carried into this
// build's schema -- it is older than model.BaselineSchemaVersion, or it is
// newer than this build and a fixup stands between the two. It is not a
// failure of the dump: it is this build saying it will not guess.
var ErrNotMigratable = errors.New("state repository holds a dump grain cannot migrate into this build's schema")

// dumpFixups transforms a dump written at a schema version into one the
// next version can import, keyed by the version being left behind.
//
// A key that is absent means the plain import is enough, which is the
// ordinary case: adding a nullable column, dropping one, adding a table
// and adding an index all come out right on their own (see this file's
// own header). Add an entry when the bump is one of the others -- a
// renamed column, a new column with no usable default, rows that have to
// be split across two tables -- and the entry gets the dump directory to
// rewrite in place. It is handed a scratch copy, never the deployment's
// own working tree, so a fixup is free to be destructive about it.
//
// Ordering is by version: migrating from 19 to 23 runs 21's, then 22's,
// each one leaving the dump as the next version expects to find it.
var dumpFixups = map[int]func(dir string) error{
	// 22 moves the deployment's git connections, pinned connection,
	// per-model settings and default model off the default team's row
	// onto grain_config (default_team_settings.go).
	21: moveDefaultTeamSettings,
	// 23 folds the default team's team_repo_config rows into repo_config
	// and moves its team_gcp_key rows to gcp_key (team_repo_settings.go).
	22: foldTeamRepoSettings,
	// 24 drops the team table, carrying what the default team's row
	// still held onto grain_config (default_team_row.go).
	23: foldDefaultTeamRow,
}

// Migratable reports whether a dump stamped `found` can be imported into
// a database at `version`, and says why not when it cannot.
//
// Both directions are allowed. A rollback -- an operator putting
// yesterday's image back after a bad release -- meets exactly the same
// wall from the other side, and a deployment that cannot come back up on
// the build it was running an hour ago is a worse failure than the one
// this exists to fix. What a rollback does not get is the fixups: those
// are written one way, so a version range that has one in it is refused
// rather than half-applied backwards.
func Migratable(found, version int) error {
	if found == version {
		return nil
	}
	if found < model.BaselineSchemaVersion {
		if err := model.CheckSchemaBaseline(found, model.StateRepository); err != nil {
			return fmt.Errorf("%w: %w", ErrNotMigratable, err)
		}
		return fmt.Errorf("%w: the dump is not stamped with a schema, and grain migrates from schema %d onwards",
			ErrNotMigratable, model.BaselineSchemaVersion)
	}
	if found > version {
		if steps := fixupsBetween(version, found); len(steps) > 0 {
			return fmt.Errorf("%w: rolling back from schema %d to %d would have to undo the change at "+
				"schema %d, and grain's dump migrations only run forwards",
				ErrNotMigratable, found, version, steps[0])
		}
		return nil
	}
	return nil
}

// fixupsBetween names the fixup versions that lie in [from, to), in
// order -- the steps a migration forwards across that range has to run.
func fixupsBetween(from, to int) []int {
	var steps []int
	for v := range dumpFixups {
		if v >= from && v < to {
			steps = append(steps, v)
		}
	}
	sort.Ints(steps)
	return steps
}

// MigrateDump prepares the dump in dir for import into a database at
// `version`, and returns the directory to import from.
//
// That is dir itself whenever nothing has to be done to the files, which
// is every migration with no fixup in its range -- the importer's own
// tolerance is the migration, and copying a whole deployment's rows
// around to change nothing about them would be pure cost. When there is
// a fixup to run, the dump is copied under scratch first and the copy is
// what is rewritten: dir may be a git working tree, or a directory this
// build is about to be asked to leave exactly as it found it if the
// migration fails.
func MigrateDump(dir, scratch string, found, version int) (string, error) {
	if err := Migratable(found, version); err != nil {
		return "", err
	}
	steps := fixupsBetween(found, version)
	if len(steps) == 0 {
		return dir, nil
	}
	work := filepath.Join(scratch, "dump-migration")
	if err := os.RemoveAll(work); err != nil {
		return "", fmt.Errorf("staterepo: clearing %s: %w", work, err)
	}
	if err := os.MkdirAll(filepath.Dir(work), 0o700); err != nil {
		return "", fmt.Errorf("staterepo: preparing %s: %w", work, err)
	}
	if err := os.CopyFS(work, os.DirFS(dir)); err != nil {
		return "", fmt.Errorf("staterepo: copying the dump in %s to migrate it: %w", dir, err)
	}
	for _, v := range steps {
		if err := dumpFixups[v](work); err != nil {
			return "", fmt.Errorf("staterepo: migrating the dump past schema %d: %w", v, err)
		}
	}
	if err := WriteSchemaVersion(work, version); err != nil {
		return "", err
	}
	return work, nil
}

// ImportMigrated fills a database at this build's schema from a dump
// written at another one: Import, minus the schema stamp.
//
// That one exclusion is the whole difference, and leaving it out would
// undo the migration at the last step. grain_schema is a table like any
// other in a dump -- Export writes it, Import replaces it -- and what it
// holds in a dump from before a schema change is the schema from before
// the change. Importing it would stamp the freshly created store with
// the version it was migrating *away* from, so this build would open its
// own new store once, and every start after that would refuse it.
//
// Every other table is replaced exactly as Import replaces it, which is
// right: the database being filled was created seconds ago and has
// nothing in it to lose.
func ImportMigrated(ctx context.Context, db *sql.DB, dir string) error {
	tables, err := tableNames(ctx, db)
	if err != nil {
		return err
	}
	var wanted []string
	for _, t := range tables {
		if !schemaStampTables[t] {
			wanted = append(wanted, t)
		}
	}
	return importInto(ctx, db, dir, wanted, false, nil)
}

// Migrated is Reformat's counterpart for a deployment whose rows came
// across: this database, exported at this build's schema, over a dump
// written at another one, in one commit that says the rows are the same
// rows.
//
// Mechanically it is the identical operation -- see rewrite, which both
// call -- and the difference is the whole point of having two. Reformat
// commits the sentence "the rows this replaces are not coming back",
// because when setup.sh moves a store aside that is true. Here it would
// be false: the diff looks much the same, every table rewritten at once,
// and a reader of this repository six months from now has nothing but
// the message to tell the two apart.
func Migrated(ctx context.Context, r *Repo, db *sql.DB, version int) (bool, error) {
	return rewrite(ctx, r, db, version, migratedCommitMessage)
}

// migratedCommitMessage says what a migration did, in the terms
// reformatCommitMessage says what a reformat did -- and against it,
// since the two diffs are indistinguishable.
func migratedCommitMessage(found, version int, build string, now time.Time) string {
	was := "an earlier schema"
	if found > 0 {
		was = "schema " + strconv.Itoa(found)
	}
	msg := "Migrate grain state to schema " + strconv.Itoa(version) + "\n\n" +
		"This deployment's schema changed, from " + was + " to schema " +
		strconv.Itoa(version) + ".\n" +
		"These are the same rows: the database this deployment runs on was rebuilt\n" +
		"at the new schema by importing the dump this replaces, and this commit is\n" +
		"that database written back out. Nothing was left behind that the import\n" +
		"could carry -- a column the new schema does not have is gone, which is what\n" +
		"the schema change means.\n\n" +
		"The store the rows came out of is still on the deployment, moved aside\n" +
		"under a timestamped name, and the dump they came out of is the commit\n" +
		"before this one.\n\n" +
		"Written by grain at " + now.UTC().Format(time.RFC3339) + "."
	if build != "" {
		msg += "\n\nThe build that wrote it: " + build + "."
	}
	return msg
}
