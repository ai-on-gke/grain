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
	"strings"
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

// UnappliedCommit describes a commit in the state repository past the host's
// loaded-head marker that the database had not yet applied before a schema
// migration.
type UnappliedCommit struct {
	Hash    string
	Short   string
	Subject string
	Author  string
}

func (c UnappliedCommit) String() string {
	s := c.Short
	if s == "" {
		s = short(c.Hash)
	}
	if c.Subject == "" {
		return s
	}
	return s + " " + c.Subject
}

// MigratedReport describes what MigrateRepo did to the state repository.
type MigratedReport struct {
	// Committed reports whether exporting the migrated database produced a
	// new commit in the state repository.
	Committed bool
	// LoadedHead is the commit marker recorded on the host before MigrateRepo
	// ran, when one existed.
	LoadedHead string
	// PriorHead is the state repository's HEAD commit before MigrateRepo
	// exported the migrated database over it.
	PriorHead string
	// Head is the state repository's HEAD commit after MigrateRepo finished.
	Head string
	// Applied lists commits between LoadedHead and PriorHead that the
	// database had not previously applied and that MigrateRepo merged into db
	// before exporting at the new schema.
	Applied []UnappliedCommit
	// Dropped lists commits between LoadedHead and PriorHead that the
	// database had not applied and that MigrateRepo could not apply before
	// rewriting the dump at the new schema.
	Dropped []UnappliedCommit
	// ApplyErr is the error from attempting to apply Dropped when
	// len(Dropped) > 0.
	ApplyErr error
}

// Migrated is Reformat's counterpart for a deployment whose rows came
// across: this database, exported at this build's schema, over a dump
// written at another one, in one commit that says the rows are the same
// rows.
//
// Mechanically it shares Reformat's export-and-push shape, plus one step
// Reformat never wants: when the repository holds commits past this host's
// loaded-head marker that the old daemon never applied, MigrateRepo attempts
// to three-way merge those settings changes into db before exporting at the
// new schema, and reports any commits that still could not be applied.
func Migrated(ctx context.Context, r *Repo, db *sql.DB, version int) (bool, error) {
	report, err := MigrateRepo(ctx, r, db, version)
	return report.Committed, err
}

// MigrateRepo brings the state repository onto version from db, first
// attempting to merge any commits in the state repository past this host's
// loaded-head marker that the database had not yet applied.
//
// When those unapplied commits merge cleanly into db (for example, commits an
// older build could not import because of a bug fixed in this build, or
// commits merged right as the daemon restarted), their settings changes are
// preserved in both db and the rewritten dump, and report.Applied names them.
//
// When they still cannot be applied against db at version (for example, a
// malformed settings file or broken history), MigrateRepo leaves db intact,
// exports db at version so the daemon can start and state-repository CI can
// run on repair branches, and records every overwritten commit in
// report.Dropped, report.ApplyErr, and the migration commit message so the
// caller can log a warning and raise an operator notification.
func MigrateRepo(ctx context.Context, r *Repo, db *sql.DB, version int) (MigratedReport, error) {
	var report MigratedReport
	var unreachable error
	if _, err := r.Pull(ctx); err != nil {
		if errors.Is(err, ErrDiverged) {
			if recovered, rerr := r.RecoverDiverged(ctx); rerr == nil && recovered {
				_, err = r.Pull(ctx)
			}
		}
		if err != nil {
			if !errors.Is(err, ErrUnreachable) {
				return report, err
			}
			unreachable = err
		}
	}

	found := 0
	if HasDump(r.Dir()) {
		found, _ = ReadSchemaVersion(r.Dir())
	}
	head, err := r.Head(ctx)
	if err != nil {
		return report, err
	}
	marker, err := r.loadedHead(ctx)
	if err != nil {
		return report, err
	}
	report.PriorHead = head
	report.LoadedHead = marker

	if HasDump(r.Dir()) && head != "" && marker != "" && marker != head {
		pending, perr := r.unappliedCommits(ctx, marker, head)
		if perr != nil {
			return report, perr
		}
		if len(pending) > 0 {
			if applyErr := applyPendingOnMigrate(ctx, r, db, version, head, marker); applyErr != nil {
				report.Dropped = pending
				report.ApplyErr = applyErr
			} else {
				report.Applied = pending
			}
		}
	}

	if err := writeReadme(r.Dir(), deploymentName(ctx, db)); err != nil {
		return report, err
	}
	if err := ExportConfig(ctx, db, r.Dir()); err != nil {
		return report, err
	}
	if err := WriteSchemaVersion(r.Dir(), version); err != nil {
		return report, err
	}
	now := r.now()
	msg := migratedCommitMessageWithReport(found, version, r.cfg.Build, now, report)
	committed, err := r.Commit(ctx, msg)
	if err != nil {
		return report, err
	}
	report.Committed = committed
	if err := r.recordLoadedHead(ctx); err != nil {
		return report, err
	}
	if err := r.recordExportedBuild(ctx); err != nil {
		return report, err
	}
	report.Head, _ = r.Head(ctx)
	if err := r.Push(ctx); err != nil {
		if unreachable != nil {
			return report, fmt.Errorf("%w (the new dump is committed in %s and will go out "+
				"with the next push that works: %v)", unreachable, r.Dir(), err)
		}
		return report, err
	}
	return report, nil
}

// applyPendingOnMigrate three-way merges settings changes between marker and
// head into db when db has already migrated to version.
func applyPendingOnMigrate(ctx context.Context, r *Repo, db *sql.DB, version int, head, marker string) error {
	base, err := r.mergeBase(ctx, marker, head)
	if err != nil {
		return refuseApply(err)
	}
	baseDir, err := r.checkoutTree(ctx, base)
	if err != nil {
		return refuseApply(err)
	}
	defer os.RemoveAll(baseDir)

	scratch, err := os.MkdirTemp(r.DataDir(), ".grain-migrate-apply-*")
	if err != nil {
		return fmt.Errorf("staterepo: preparing scratch directory for pending commits: %w", err)
	}
	defer os.RemoveAll(scratch)

	incomingDir, err := prepareDumpForApply(r.Dir(), filepath.Join(scratch, "incoming"), version)
	if err != nil {
		return refuseApply(err)
	}
	mergedBaseDir := baseDir
	if HasDump(baseDir) {
		migratedBase, berr := prepareDumpForApply(baseDir, filepath.Join(scratch, "base"), version)
		if berr != nil {
			return refuseApply(berr)
		}
		mergedBaseDir = migratedBase
	}
	return applySettingsFromDirs(ctx, r, db, head, base, mergedBaseDir, incomingDir)
}

// prepareDumpForApply runs any pre-import structural dumpFixups needed to read
// dir into a store at version, while preserving a pre-version schema stamp so
// importInto still runs any subsequent data migrations.
func prepareDumpForApply(dir, scratch string, version int) (string, error) {
	found, err := ReadSchemaVersion(dir)
	if err != nil {
		return "", err
	}
	if found == version {
		return dir, nil
	}
	work, err := MigrateDump(dir, scratch, found, version)
	if err != nil {
		return "", err
	}
	if work != dir {
		steps := fixupsBetween(found, version)
		if len(steps) > 0 {
			if afterFixups := steps[len(steps)-1] + 1; afterFixups < version {
				if err := WriteSchemaVersion(work, afterFixups); err != nil {
					return "", err
				}
			}
		}
	}
	return work, nil
}

// unappliedCommits lists the commits in the state repository that arrived
// after marker on the path to head, oldest first.
//
// When commits were merged with --no-ff (for example via grain/config pull
// requests), --no-merges names the underlying change commits rather than
// duplicate merge wrappers; if a range contains only merge commits, the merge
// commits themselves are returned. When marker is missing or shares no merge
// base with head, head itself is returned so callers still report what could
// not be applied.
func (r *Repo) unappliedCommits(ctx context.Context, marker, head string) ([]UnappliedCommit, error) {
	if head == "" || marker == "" || marker == head {
		return nil, nil
	}
	base, err := r.mergeBase(ctx, marker, head)
	if err != nil {
		info, ierr := r.commitInfo(ctx, head)
		if ierr != nil {
			return []UnappliedCommit{{Hash: head, Short: short(head)}}, nil
		}
		return []UnappliedCommit{info}, nil
	}
	if base == head {
		// head is already an ancestor of marker: nothing in head is unapplied.
		return nil, nil
	}
	spec := base + ".." + head
	commits, err := r.logCommits(ctx, spec, true)
	if err != nil {
		return nil, err
	}
	if len(commits) == 0 {
		commits, err = r.logCommits(ctx, spec, false)
		if err != nil {
			return nil, err
		}
	}
	return commits, nil
}

func (r *Repo) logCommits(ctx context.Context, spec string, noMerges bool) ([]UnappliedCommit, error) {
	args := []string{"log", "--topo-order", "--reverse"}
	if noMerges {
		args = append(args, "--no-merges")
	}
	args = append(args, "--format=%H%x1f%h%x1f%s%x1f%an", "--end-of-options", spec)
	out, err := r.git(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("staterepo: listing commits in %s: %w", spec, err)
	}
	var commits []UnappliedCommit
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		commits = append(commits, parseUnappliedCommitLine(line))
	}
	return commits, nil
}

func (r *Repo) commitInfo(ctx context.Context, rev string) (UnappliedCommit, error) {
	out, err := r.git(ctx, "show", "--quiet", "--format=%H%x1f%h%x1f%s%x1f%an", "--end-of-options", rev)
	if err != nil {
		return UnappliedCommit{}, err
	}
	line := strings.TrimSpace(out)
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = strings.TrimSpace(line[:idx])
	}
	return parseUnappliedCommitLine(line), nil
}

func parseUnappliedCommitLine(line string) UnappliedCommit {
	parts := strings.SplitN(line, "\x1f", 4)
	var c UnappliedCommit
	if len(parts) > 0 {
		c.Hash = parts[0]
	}
	if len(parts) > 1 {
		c.Short = parts[1]
	}
	if c.Short == "" {
		c.Short = short(c.Hash)
	}
	if len(parts) > 2 {
		c.Subject = parts[2]
	}
	if len(parts) > 3 {
		c.Author = parts[3]
	}
	return c
}

func formatUnappliedInline(commits []UnappliedCommit) string {
	parts := make([]string, 0, len(commits))
	for _, c := range commits {
		parts = append(parts, fmt.Sprintf("%s (%q)", c.Short, c.Subject))
	}
	return strings.Join(parts, ", ")
}

// migratedCommitMessageWithReport says what a migration did, in the terms
// reformatCommitMessage says what a reformat did -- and against it,
// since the two diffs are indistinguishable.
func migratedCommitMessageWithReport(found, version int, build string, now time.Time, report MigratedReport) string {
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
		"before this one."
	if len(report.Applied) > 0 {
		var b strings.Builder
		b.WriteString("\n\nApplied previously unapplied configuration commit(s) before exporting:\n")
		for _, c := range report.Applied {
			b.WriteString("  - ")
			b.WriteString(c.String())
			b.WriteByte('\n')
		}
		msg += strings.TrimRight(b.String(), "\n")
	}
	if len(report.Dropped) > 0 {
		var b strings.Builder
		b.WriteString("\n\nWARNING: the following commit(s) in the state repository had never been\n" +
			"applied to the database and still failed to apply during migration, so\n" +
			"this export overwrote them (re-apply or cherry-pick them onto the new schema):\n")
		for _, c := range report.Dropped {
			b.WriteString("  - ")
			b.WriteString(c.String())
			b.WriteByte('\n')
		}
		if report.ApplyErr != nil {
			b.WriteString("Apply error: ")
			b.WriteString(report.ApplyErr.Error())
		}
		msg += strings.TrimRight(b.String(), "\n")
	}
	msg += "\n\nWritten by grain at " + now.UTC().Format(time.RFC3339) + "."
	if build != "" {
		msg += "\n\nThe build that wrote it: " + build + "."
	}
	return msg
}
