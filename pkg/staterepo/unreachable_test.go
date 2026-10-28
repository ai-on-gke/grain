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

// What a load does when the remote is not there: the difference between
// "could not reach the remote" and "the repository says something this
// build must not overwrite", which is the difference between a
// deployment that goes on running and one that must stop.
//
// The remote here is a bare repository in a temporary directory, and
// "unreachable" is that directory moved out of the way -- which is what
// git reports for a network that is down, a credential that has expired
// and a repository renamed on GitHub alike: a fetch that did not
// complete, saying nothing at all about what the repository holds.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// A deployment that has loaded this repository before starts again on
// what it has. The database is complete, the working tree is where the
// last fetch left it, and the only thing missing is the news -- so the
// load goes ahead, reports that it is out of touch, and the caller
// carries on.
func TestALoadCarriesOnWhenTheRemoteHasGoneAway(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	putTemplate(t, ctx, store, "tpl-before", "Configured before the remote went")
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("first load: %v", err)
	}

	// The remote stops answering, and the daemon is restarted -- a
	// redeploy landing in the middle of an outage, which is when a
	// deployment can least afford not to come back.
	gone := remote + ".gone"
	if err := os.Rename(remote, gone); err != nil {
		t.Fatalf("taking the remote away: %v", err)
	}
	// A settings change made after the last export, so it lives in the
	// database and nowhere else: a load that decided to import its own
	// stale dump because it could not fetch would lose it.
	putTemplate(t, ctx, store, "tpl-after", "Configured after the remote went")
	// And a task, which the repository never held and must not be asked
	// to put back either.
	if err := store.PutTask(ctx, task("filed-after-the-remote-went")); err != nil {
		t.Fatalf("putting: %v", err)
	}
	reopened, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("reopening with the remote gone: %v", err)
	}
	err = staterepo.Load(ctx, reopened, db, model.SchemaVersion)
	if !errors.Is(err, staterepo.ErrUnreachable) {
		t.Fatalf("loading with the remote gone: got %v, want an ErrUnreachable", err)
	}
	// Not the other one: this host has a copy, which is the whole reason
	// it may carry on.
	if errors.Is(err, staterepo.ErrNoLocalCopy) {
		t.Fatalf("a repository this host had already loaded was reported as no local copy: %v", err)
	}
	for _, id := range []string{"tpl-before", "tpl-after"} {
		if got, err := store.GetTemplate(ctx, id); err != nil || got == nil {
			t.Fatalf("the load lost %s: %v %v", id, got, err)
		}
	}
	if got, err := store.GetTask(ctx, "filed-after-the-remote-went"); err != nil || got == nil {
		t.Fatalf("the load lost the task the database is the only copy of: %v %v", got, err)
	}

	// And the deployment goes on working: the export commits locally,
	// the push is what fails, and everything that accumulated goes out
	// with the first push that works.
	changed, err := staterepo.Sync(ctx, reopened, db, model.SchemaVersion)
	if !changed {
		t.Fatalf("a sync with the remote gone committed nothing: %v", err)
	}
	if err == nil {
		t.Fatal("a sync with the remote gone reported no failure to push")
	}
	hasTemplate(t, dir, "tpl-after", "Configured after the remote went")
	if err := os.Rename(gone, remote); err != nil {
		t.Fatalf("bringing the remote back: %v", err)
	}
	// The commits made while it was gone go out with the next export that
	// has something to say -- a push carries the branch, not the tick --
	// so the outage costs the remote nothing but the delay.
	putTemplate(t, ctx, store, "tpl-once-back", "Configured once the remote answered")
	if _, err := staterepo.Sync(ctx, reopened, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing once the remote answered again: %v", err)
	}
	got := templatesOn(t, remote, "main")
	for id, title := range map[string]string{
		"tpl-after":     "Configured after the remote went",
		"tpl-once-back": "Configured once the remote answered",
	} {
		if got[id] != title {
			t.Fatalf("%s never reached the remote: it holds %v", id, got)
		}
	}
	// The outage is no way back in for grain's own record of itself.
	noOperationalTablesOn(t, remote, "main")
}

// The other end of it: a host that has never held a byte of this
// repository cannot carry on with what it has, because it has nothing.
// Seeding here would commit a root commit sharing no history with the
// remote's branch -- a repository that can never be pushed and that
// every later pull calls diverged -- so the load refuses, and refuses in
// a way that is not an ErrUnreachable, because a caller keying off that
// would start anyway.
func TestALoadRefusesWhenItHasNeverReachedTheRemote(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	if err := store.PutTask(ctx, task("a1b2")); err != nil {
		t.Fatalf("putting: %v", err)
	}
	// A remote that is not there at all, and a working tree with a file
	// in it already -- which is what a deployed host looks like before
	// the daemon has ever started, since scripts/setup.sh writes into
	// this directory first. Open inits rather than clones there, so the
	// repository exists with no commits in it and the first fetch is what
	// would have brought the remote's content down.
	remote := filepath.Join(t.TempDir(), "never-created.git")
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "left-here-by-the-installer"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("opening against a remote that is not there: %v", err)
	}
	err = staterepo.Load(ctx, repo, db, model.SchemaVersion)
	if !errors.Is(err, staterepo.ErrNoLocalCopy) {
		t.Fatalf("loading with nothing fetched yet: got %v, want an ErrNoLocalCopy", err)
	}
	if errors.Is(err, staterepo.ErrUnreachable) {
		t.Fatalf("a load with nothing to fall back on was reported as merely unreachable: %v", err)
	}
	// And nothing was written: the repository the remote holds, whatever
	// it holds, is still the only history there is.
	if staterepo.HasDump(dir) {
		t.Fatal("the load seeded a repository it had never fetched")
	}
}

// An unreachable remote excuses nothing about the working tree itself. A
// dump this build cannot read is the case that must stop whatever else
// is going on, because exporting today's schema over it is how the rows
// in it are lost -- so it comes back as itself, unmarked, and a caller
// that starts on an ErrUnreachable does not start on this.
func TestAnUnreachableRemoteDoesNotExcuseADumpThisBuildCannotRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, db := openDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("first load: %v", err)
	}
	// A newer build of grain wrote this repository, and then the remote
	// stopped answering. Both are true at once, and only one of them
	// decides whether this deployment may run.
	if err := staterepo.WriteSchemaVersion(dir, model.SchemaVersion+1); err != nil {
		t.Fatalf("stamping a later schema: %v", err)
	}
	if err := os.Rename(remote, remote+".gone"); err != nil {
		t.Fatalf("taking the remote away: %v", err)
	}
	err = staterepo.Load(ctx, repo, db, model.SchemaVersion)
	if !errors.Is(err, staterepo.ErrSchemaTooNew) {
		t.Fatalf("loading a dump from a newer build: got %v, want an ErrSchemaTooNew", err)
	}
	if errors.Is(err, staterepo.ErrUnreachable) {
		t.Fatalf("a repository this build cannot read was reported as merely unreachable: %v", err)
	}
}
