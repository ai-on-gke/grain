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

package staterepo_test

// What a schema change does to a state repository, in both of the shapes
// a deployment can be in: local-only, where moving the working tree aside
// is the whole answer, and with a remote, where it is not an answer at
// all until somebody says the schema changed.
//
// The first two tests here are the trace this file was written to settle
// (grain/task-44). The README said scripts/setup.sh's archive of the
// working tree was enough for both, and it is enough for exactly one of
// them: a repository with a remote comes straight back down, dump, schema
// stamp and all, and the load fails on it. The rest are what Reformat
// does about that.
//
// "This build" is model.SchemaVersion+1 throughout: the repository is
// seeded by a build at the current version, and everything after the bump
// asks the next one, which is the same pair of numbers a real schema bump
// puts a deployment between.

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// seededBy is a repository an earlier build wrote: one deployment's
// configuration and one task, exported, committed and pushed at
// model.SchemaVersion. It returns the remote, which is all a host on the
// far side of a schema bump has left of it -- its store and its working
// tree having been moved aside.
//
// The task is there to be left out. It is in the database the old build
// ran on and in no commit this function makes, and every test below that
// reads the remote is entitled to find no trace of it.
func seededBy(t *testing.T, ctx context.Context) string {
	t.Helper()
	store, db := openDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := store.PutTask(ctx, task("a1b2")); err != nil {
		t.Fatalf("putting a task: %v", err)
	}
	putTemplate(t, ctx, store, "tpl-old", "Configured by the build before the bump")
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	return remote
}

// afterTheBump is the deployment the schema bump leaves behind: a fresh,
// empty database at the new schema, and a working tree directory that
// does not exist yet because the bump archived the one that did.
func afterTheBump(t *testing.T, ctx context.Context, remote string) (*sql.DB, *staterepo.Repo) {
	t.Helper()
	_, db := openDB(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("opening after the bump: %v", err)
	}
	return db, repo
}

// The failure this whole file is about: a schema bump on a deployment
// with a remote leaves nothing for the daemon to re-seed, because the
// first thing it does is clone the old dump back down.
func TestASchemaBumpCannotReseedARepositoryThatHasARemote(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	remote := seededBy(t, ctx)
	db, repo := afterTheBump(t, ctx, remote)

	err := staterepo.Load(ctx, repo, db, model.SchemaVersion+1)
	if !errors.Is(err, staterepo.ErrSchemaTooOld) {
		t.Fatalf("loading after a schema bump = %v, want ErrSchemaTooOld", err)
	}
	// And it is not a state the next attempt gets out of: cmd/grain
	// treats this as a deployment that must not start, so what a real
	// host does here is exit and be restarted into the identical
	// failure. Asserted rather than assumed, because "the daemon
	// re-seeds it" was the documented behaviour of this exact case.
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion+1); !errors.Is(err, staterepo.ErrSchemaTooOld) {
		t.Fatalf("a second load = %v, want the same ErrSchemaTooOld", err)
	}
}

// The paired case, which does work, and is what the archive was written
// for: with no remote there is nothing to clone back, so the daemon finds
// an empty directory and seeds it.
func TestASchemaBumpReseedsALocalOnlyRepository(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := store.PutTask(ctx, task("a1b2")); err != nil {
		t.Fatalf("putting a task: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	// The bump: the store and the working tree are both moved aside, and
	// the daemon comes up on a fresh database at the new schema.
	_, fresh := openDB(t)
	next := filepath.Join(t.TempDir(), "state")
	repo2, err := staterepo.Open(ctx, staterepo.Config{Dir: next})
	if err != nil {
		t.Fatalf("opening after the bump: %v", err)
	}
	if err := staterepo.Load(ctx, repo2, fresh, model.SchemaVersion+1); err != nil {
		t.Fatalf("a local-only repository did not re-seed after a schema bump: %v", err)
	}
	found, err := staterepo.ReadSchemaVersion(repo2.Dir())
	if err != nil {
		t.Fatalf("reading the stamp of the re-seeded repository: %v", err)
	}
	if found != model.SchemaVersion+1 {
		t.Fatalf("re-seeded at schema %d, want %d", found, model.SchemaVersion+1)
	}
}

// Reformat is what the remote case has instead, and what it costs: the
// dump becomes this deployment's database at this build's schema, and the
// rows it held are the commit before it.
func TestReformatReplacesADumpThisBuildCannotRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	remote := seededBy(t, ctx)
	db, repo := afterTheBump(t, ctx, remote)
	store := model.New(db)
	if err := store.PutTask(ctx, task("c3d4")); err != nil {
		t.Fatalf("putting a task into the fresh database: %v", err)
	}
	putTemplate(t, ctx, store, "tpl-new", "Configured on the build after the bump")

	committed, err := staterepo.Reformat(ctx, repo, db, model.SchemaVersion+1)
	if err != nil {
		t.Fatalf("reformatting: %v", err)
	}
	if !committed {
		t.Fatal("Reformat committed nothing over a dump at another schema")
	}
	// The load that could not happen before this one now can, and it is
	// an ordinary one: the repository is where this host left it.
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion+1); err != nil {
		t.Fatalf("loading a reformatted repository: %v", err)
	}
	if got, err := store.GetTask(ctx, "c3d4"); err != nil || got == nil {
		t.Fatalf("the load after a reformat lost the deployment's own task: %v %v", got, err)
	}

	// On the remote, at the tip: this build's schema and this
	// deployment's configuration, pushed by Reformat itself rather than
	// left for a sync that a daemon refusing to start would never run.
	if got := strings.TrimSpace(git(t, remote, "show", "main:"+staterepo.SchemaVersionFile)); got != strconv.Itoa(model.SchemaVersion+1) {
		t.Fatalf("the remote is stamped %q, want %d", got, model.SchemaVersion+1)
	}
	tip := templatesOn(t, remote, "main")
	if tip["tpl-new"] != "Configured on the build after the bump" {
		t.Fatalf("the remote does not hold this deployment's own settings: %v", tip)
	}
	if _, ok := tip["tpl-old"]; ok {
		t.Fatalf("the remote still holds the old build's settings: %v", tip)
	}
	// And they are still there to be read, which is the whole of what
	// makes this recoverable: the history is the backup.
	before := git(t, remote, "show", "main~1:"+staterepo.DumpPath("template"))
	if !strings.Contains(before, "Configured by the build before the bump") {
		t.Fatalf("the commit before the reformat does not hold what was there:\n%s", before)
	}

	// And a reformat is an export like any other: it writes the
	// configuration, not the database. It used to call the whole-database
	// Export -- this is the same working tree the sync loop pushes from,
	// so a schema change would have committed every task, run and CI step
	// this deployment had into a repository agents are dispatched to
	// clone, and the next ordinary sync would have deleted them again
	// (flagged on #1712, fixed with this test).
	noOperationalTables(t, repo.Dir())
	noOperationalTablesOn(t, remote, "main")
}

// The commit says which schema it left and which it arrived at, because
// its diff is every row in the deployment at once and cannot say either.
func TestTheReformatCommitNamesBothSchemas(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	remote := seededBy(t, ctx)
	db, repo := afterTheBump(t, ctx, remote)
	if _, err := staterepo.Reformat(ctx, repo, db, model.SchemaVersion+1); err != nil {
		t.Fatalf("reformatting: %v", err)
	}
	got := message(t, repo.Dir())
	for _, want := range []string{
		"Reformat grain state",
		"schema " + strconv.Itoa(model.SchemaVersion),
		"schema " + strconv.Itoa(model.SchemaVersion+1),
		"the commit before this one",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("the reformat commit does not say %q:\n%s", want, got)
		}
	}
}

// A remote that is out of reach does not stop the reformat, for the same
// reason it does not stop a load: the working tree is the deployment's
// own and the commit belongs on it either way. The push is what waits.
func TestReformatCommitsEvenWhenTheRemoteIsUnreachable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	remote := seededBy(t, ctx)
	db, repo := afterTheBump(t, ctx, remote)
	// Cloned first (afterTheBump), then taken away: a host with no copy
	// at all has nothing to reformat, and Load says so on its own
	// (ErrNoLocalCopy) long before this.
	if err := os.Rename(remote, remote+".gone"); err != nil {
		t.Fatalf("taking the remote away: %v", err)
	}

	committed, err := staterepo.Reformat(ctx, repo, db, model.SchemaVersion+1)
	if !committed {
		t.Fatalf("Reformat committed nothing with the remote away: %v", err)
	}
	if !errors.Is(err, staterepo.ErrUnreachable) {
		t.Fatalf("Reformat with the remote away = %v, want an ErrUnreachable", err)
	}
	found, err := staterepo.ReadSchemaVersion(repo.Dir())
	if err != nil {
		t.Fatalf("reading the stamp: %v", err)
	}
	if found != model.SchemaVersion+1 {
		t.Fatalf("the working tree is stamped %d, want %d", found, model.SchemaVersion+1)
	}
	// The remote comes back and an ordinary sync carries the commit out:
	// nothing had to be reformatted twice.
	if err := os.Rename(remote+".gone", remote); err != nil {
		t.Fatalf("bringing the remote back: %v", err)
	}
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion+1); err != nil {
		t.Fatalf("syncing after the remote came back: %v", err)
	}
	if got := strings.TrimSpace(git(t, remote, "show", "main:"+staterepo.SchemaVersionFile)); got != strconv.Itoa(model.SchemaVersion+1) {
		t.Fatalf("the remote is stamped %q, want %d", got, model.SchemaVersion+1)
	}
}
