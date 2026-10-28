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

// The state repository against real git and a real embedded SQLite
// database. Nothing here is mocked: the remote is a bare repository in a
// temporary directory, which is what a push and a fetch actually talk
// to, and the database is the model's own schema, which is what an
// export has to be able to carry.

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

var now = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

func openDB(t *testing.T) (*model.Store, *sql.DB) {
	t.Helper()
	return sqlitetest.Open(t)
}

// openRealDB is reserved for tests whose subject is schema initialization or
// migration. Ordinary store users should use the per-binary schema template.
func openRealDB(t *testing.T) (*model.Store, *sql.DB) {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	return store, db
}

func task(id string) model.Task {
	return model.Task{
		ID: id, Intent: model.IntentImplement, Title: "Rename the endpoint",
		Body: "with a body\nover two lines",
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "bwsalmon"}},
			Reason:      model.ReasonDirect,
		},
		Target:    &model.RepoRef{Owner: "owner", Name: "payments-api"},
		Binding:   model.BindingDirective,
		CreatedAt: &now,
	}
}

func bareRemote(t *testing.T) string {
	t.Helper()
	return bareRemoteOn(t, "main")
}

// bareRemoteOn is the remote every test here pushes to: a real bare
// repository in a temporary directory, on the branch named.
//
// The two config settings are what keep a push from outliving the test
// that made it. git runs its own auto-maintenance on the receiving side
// of a push, and since v2.47 it does that by detaching: receive-pack
// spawns `git maintenance run --auto --detach`, which forks and lets the
// push return while it goes on repacking the repository in the
// background. That background process is still writing into remote.git
// when the test returns and t.TempDir deletes the tree underneath it,
// and the deletion fails on files that land after RemoveAll has walked
// past them:
//
//	TempDir RemoveAll cleanup: unlinkat
//	/tmp/TestTheReformatCommitNamesBothSchemas.../002/remote.git:
//	directory not empty
//
// Saying autoDetach=false does not turn the maintenance off, it runs it
// in the foreground, so the push that triggered it is what waits for it
// -- which is the same choice Repo.configure makes for grain's own
// working tree, and for the same reason. Both keys are set because
// gc.autoDetach is only honoured here as a fallback for a repository
// that says nothing about maintenance.autoDetach, which git's own source
// calls "somewhat weird" and keeps for compatibility.
//
// Nothing on a runner with git 2.39 can see any of this: detaching
// auto-maintenance is newer than that, so the flake only ever appeared
// on CI.
func bareRemoteOn(t *testing.T, branch string) string {
	t.Helper()
	// Nearly every test here starts with a remote, so this is where they
	// claim the log -- first, before the cleanups below and whatever the
	// caller registers after us, because the release has to run last of
	// all. See log_test.go.
	staterepo.LogToTest(t)
	dir := filepath.Join(t.TempDir(), "remote.git")
	if out, err := exec.Command("git", "init", "--bare", "--initial-branch="+branch, dir).CombinedOutput(); err != nil {
		t.Fatalf("creating a bare remote: %v: %s", err, out)
	}
	for _, kv := range [][2]string{
		{"maintenance.autoDetach", "false"},
		{"gc.autoDetach", "false"},
	} {
		cmd := exec.Command("git", "config", kv[0], kv[1])
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setting %s on the bare remote: %v: %s", kv[0], err, out)
		}
	}
	return dir
}

func TestExportRoundTripsThroughFiles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	want := task("a1b2")
	if err := store.PutTask(ctx, want); err != nil {
		t.Fatalf("putting a task: %v", err)
	}
	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}

	// A second, empty database imports the files and has to end up with
	// the same task -- which is the whole claim this package makes: the
	// files, not the database file, are the state.
	other, otherDB := openDB(t)
	if err := staterepo.Import(ctx, otherDB, dir); err != nil {
		t.Fatalf("importing: %v", err)
	}
	got, err := other.GetTask(ctx, "a1b2")
	if err != nil {
		t.Fatalf("reading the imported task: %v", err)
	}
	if got == nil {
		t.Fatal("the task did not survive the round trip")
	}
	if got.Title != want.Title || got.Body != want.Body {
		t.Fatalf("round trip changed the task: got %q/%q", got.Title, got.Body)
	}
	if got.Target == nil || got.Target.Name != "payments-api" {
		t.Fatalf("round trip lost the target: %+v", got.Target)
	}
	if got.CreatedAt == nil || !got.CreatedAt.Equal(now) {
		t.Fatalf("round trip changed CreatedAt: %v, want %v", got.CreatedAt, now)
	}
}

func TestExportIsByteStable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	for _, id := range []string{"c3", "a1", "b2"} {
		if err := store.PutTask(ctx, task(id)); err != nil {
			t.Fatalf("putting %s: %v", id, err)
		}
	}
	first, second := t.TempDir(), t.TempDir()
	if err := staterepo.Export(ctx, db, first); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	if err := staterepo.Export(ctx, db, second); err != nil {
		t.Fatalf("exporting again: %v", err)
	}
	// Byte-identical, not merely equivalent: an export that reordered
	// rows would commit and push on every sync cycle forever.
	a := read(t, filepath.Join(first, staterepo.TablesDir, "task.json"))
	b := read(t, filepath.Join(second, staterepo.TablesDir, "task.json"))
	if a != b {
		t.Fatalf("two exports of one database differ:\n%s\n---\n%s", a, b)
	}
	if !strings.Contains(a, `"id": "a1"`) {
		t.Fatalf("the dump does not name its rows readably:\n%s", a)
	}
	// Sorted by primary key, so a reviewer reads a stable file.
	if strings.Index(a, `"a1"`) > strings.Index(a, `"b2"`) {
		t.Fatalf("rows are not sorted by primary key:\n%s", a)
	}
}

func TestImportReplacesWhatIsThere(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	if err := store.PutTask(ctx, task("keep")); err != nil {
		t.Fatalf("putting: %v", err)
	}
	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	// A row added after the export is one the dump does not have, and an
	// import is a replacement, not a merge -- that is how a merged pull
	// request deleting a template actually deletes it.
	if err := store.PutTask(ctx, task("added-later")); err != nil {
		t.Fatalf("putting: %v", err)
	}
	if err := staterepo.Import(ctx, db, dir); err != nil {
		t.Fatalf("importing: %v", err)
	}
	if got, err := store.GetTask(ctx, "added-later"); err != nil {
		t.Fatalf("reading: %v", err)
	} else if got != nil {
		t.Fatal("import left behind a row the dump does not have")
	}
	if got, err := store.GetTask(ctx, "keep"); err != nil || got == nil {
		t.Fatalf("import lost a row the dump does have: %v %v", got, err)
	}
}

func TestLocalOnlyRepositoryNeedsNoRemote(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	putTemplate(t, ctx, store, "tpl-1", "Configured on a host with no remote")
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir})
	if err != nil {
		t.Fatalf("opening a local-only repository: %v", err)
	}
	if repo.Remote() != "" {
		t.Fatalf("a local-only repository grew a remote: %q", repo.Remote())
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}
	if !staterepo.HasDump(dir) {
		t.Fatal("nothing was written to the repository")
	}
	log := git(t, dir, "log", "--oneline")
	if strings.TrimSpace(log) == "" {
		t.Fatal("nothing was committed")
	}
	// Sync with nothing changed must not produce a commit, or a deployment
	// pushes noise forever.
	changed, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion)
	if err != nil {
		t.Fatalf("syncing an unchanged database: %v", err)
	}
	if changed {
		t.Fatal("an unchanged database produced a commit")
	}
}

func TestSyncPushesToTheRemote(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("opening against a remote: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}
	putTemplate(t, ctx, store, "tpl-1", "Run the nightly sweep")
	changed, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion)
	if err != nil {
		t.Fatalf("syncing: %v", err)
	}
	if !changed {
		t.Fatal("a new setting produced no commit")
	}
	// Read it back out of the remote itself rather than trusting the push
	// exited zero.
	hasTemplateOn(t, remote, "main", "tpl-1", "Run the nightly sweep")

	// The other half of what a push means now. A task filed in the same
	// breath is grain's own record of what it did, and the next sync must
	// carry it nowhere: the repository is configuration, and a deployment
	// that published its own task list to a repository agents are
	// dispatched to work in would be a narrowing undone by accident.
	if err := store.PutTask(ctx, task("a1b2")); err != nil {
		t.Fatalf("putting: %v", err)
	}
	if changed, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil || changed {
		t.Fatalf("filing a task moved the repository: changed=%v err=%v", changed, err)
	}
	noOperationalTablesOn(t, remote, "main")
}

func TestPullBringsBackAMergedChange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := store.PutTemplate(ctx, model.Template{
		ID: "tpl-1", Name: "nightly", Title: "Run the nightly sweep", CreatedAt: now,
	}); err != nil {
		t.Fatalf("putting a template: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}

	// Somebody else -- an agent's merged pull request, in the real thing --
	// edits the dump and pushes it.
	mergeIntoRemote(t, remote, staterepo.DumpPath("template"), "Run the nightly sweep", "Retitled by a pull request")

	// grain pulls, and the merged file is what its database now says.
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading after the merge: %v", err)
	}
	got, err := store.GetTemplate(ctx, "tpl-1")
	if err != nil || got == nil {
		t.Fatalf("reading the template: %v %v", got, err)
	}
	if got.Title != "Retitled by a pull request" {
		t.Fatalf("the merged change did not reach the database: %q", got.Title)
	}
}

// The window this closed. A merged pull request that grain has not taken
// up yet leaves HEAD ahead of the commit this host recorded, and a Load
// that read that as "replace grain's own record from the dump" replaced
// it with a dump written at the last export -- up to an export interval
// ago -- so every task, comment and branch written in between went.
//
// Half a minute is not much of a window until you notice how an operator
// gets into it: merge a settings pull request, restart grain to make it
// take effect. That is a habit from before Apply existed, and it lands
// the restart squarely inside it.
func TestARestartWithAMergePendingKeepsWhatTheDatabaseHas(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	putTemplate(t, ctx, store, "tpl-1", "Run the nightly sweep")
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}

	// A settings pull request, merged, and no tick of the sync loop has
	// come round to pull it down yet.
	mergeIntoRemote(t, remote, staterepo.DumpPath("template"), "Run the nightly sweep", "Merged")

	// grain files a task and the process stops before its next export.
	//
	// A task rather than a setting, and the difference is the whole of
	// what the restart has to get right. A merged change to the settings
	// is the repository's, and the import replaces whole rows out of it
	// -- an edit somebody made here and has not exported is replaced and
	// announced as a reset (resets.go), which is the deal the settings
	// half is on. Everything grain wrote for itself is the database's,
	// arrives at no dump to be replaced from, and has to still be here.
	if err := store.PutTask(ctx, task("filed-just-before-the-stop")); err != nil {
		t.Fatalf("putting: %v", err)
	}

	reopened, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	if err := staterepo.Load(ctx, reopened, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading after the restart: %v", err)
	}

	got, err := store.GetTask(ctx, "filed-just-before-the-stop")
	if err != nil {
		t.Fatalf("reading the task: %v", err)
	}
	if got == nil {
		t.Fatal("a restart with a merge pending rolled the task back")
	}
	// And the merge it restarted for is live all the same.
	tpl, err := store.GetTemplate(ctx, "tpl-1")
	if err != nil || tpl == nil {
		t.Fatalf("reading the template: %v %v", tpl, err)
	}
	if tpl.Title != "Merged" {
		t.Fatalf("the merged settings change did not take effect: %q", tpl.Title)
	}
	// And the export that follows goes out on top of the merge rather
	// than over it: what somebody configures after the restart reaches
	// the remote, and the merged title is still the merged title.
	putTemplate(t, ctx, store, "tpl-2", "Configured after the restart")
	if _, err := staterepo.Sync(ctx, reopened, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing after the restart: %v", err)
	}
	pushed := templatesOn(t, remote, "main")
	if pushed["tpl-1"] != "Merged" {
		t.Fatalf("the export reverted the merge: the remote holds %v", pushed)
	}
	if pushed["tpl-2"] != "Configured after the restart" {
		t.Fatalf("the setting made after the restart never reached the remote: it holds %v", pushed)
	}
	// And the task it kept stayed where it was kept: in the database.
	noOperationalTablesOn(t, remote, "main")
}

// mergeIntoRemote is somebody's pull request against the state
// repository, merged: a clone of the remote with one string in one dump
// file replaced, committed and pushed by a hand that is not grain's.
func mergeIntoRemote(t *testing.T, remote, file, from, to string) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	path := filepath.Join(work, filepath.FromSlash(file))
	before := read(t, path)
	edited := strings.Replace(before, from, to, 1)
	if edited == before {
		t.Fatalf("%s does not hold %q to edit:\n%s", file, from, before)
	}
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatalf("editing the dump: %v", err)
	}
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-am", "Retitle")
	git(t, work, "push", "--quiet", "origin", "main")
}

func TestAdoptingAnEmptyRemoteSeedsIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	if err := store.PutTask(ctx, task("a1b2")); err != nil {
		t.Fatalf("putting: %v", err)
	}
	// The bootstrap's "start from scratch" path: a repository that exists
	// on GitHub and has nothing in it at all.
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("cloning an empty remote: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}
	out := git(t, remote, "show", "main:"+staterepo.SchemaVersionFile)
	if strings.TrimSpace(out) == "" {
		t.Fatalf("the empty remote was not seeded")
	}
}

func TestSetRemoteConvertsALocalRepository(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	putTemplate(t, ctx, store, "tpl-1", "Configured before there was a remote")
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}
	// The bootstrap's "I ran locally first and now want a remote" path.
	remote := bareRemote(t)
	if err := repo.SetRemote(ctx, remote); err != nil {
		t.Fatalf("setting a remote: %v", err)
	}
	if err := repo.Push(ctx); err != nil {
		t.Fatalf("pushing: %v", err)
	}
	hasTemplateOn(t, remote, "main", "tpl-1", "Configured before there was a remote")
}

// A restart must not undo what the process before it did in the seconds
// after its last export. The export runs on a timer, so the database is
// routinely ahead of the repository, and a Load that imported
// unconditionally would roll those writes back -- which is exactly what a
// container stopped shortly after a task was filed looks like, and what
// tests/container's own "a task survives the container that created it"
// caught.
func TestARestartDoesNotImportOverANewerDatabase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	// First start: an empty database, exported as an empty dump.
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("first load: %v", err)
	}
	// A task is filed and a setting changed, and the process dies before
	// the next sync tick. The task is the database's own and nothing but
	// the database will ever have it; the setting is the repository's and
	// has not got there yet. A load that imported unconditionally would
	// roll both of them back to the empty dump the first start wrote.
	if err := store.PutTask(ctx, task("filed-just-before-the-stop")); err != nil {
		t.Fatalf("putting: %v", err)
	}
	putTemplate(t, ctx, store, "tpl-1", "Configured just before the stop")
	// Second start, same data directory, same repository.
	reopened, err := staterepo.Open(ctx, staterepo.Config{Dir: dir})
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	if err := staterepo.Load(ctx, reopened, db, model.SchemaVersion); err != nil {
		t.Fatalf("second load: %v", err)
	}
	got, err := store.GetTask(ctx, "filed-just-before-the-stop")
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if got == nil {
		t.Fatal("restarting imported a stale dump over the database and lost the task")
	}
	if tpl, err := store.GetTemplate(ctx, "tpl-1"); err != nil || tpl == nil {
		t.Fatalf("restarting imported a stale dump over the database and lost the setting: %v %v", tpl, err)
	}
	// And the next sync writes the setting out, so the repository catches
	// up rather than staying behind forever -- while the task stays where
	// it has always been.
	if _, err := staterepo.Sync(ctx, reopened, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing: %v", err)
	}
	hasTemplate(t, dir, "tpl-1", "Configured just before the stop")
	noOperationalTables(t, dir)
}

// The other direction of the same decision: a working tree cloned onto a
// host that has never loaded it is a restore, and must import.
func TestACloneOntoANewHostImports(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	putTemplate(t, ctx, store, "tpl-1", "Run the nightly sweep")
	if err := store.PutTask(ctx, task("a1b2")); err != nil {
		t.Fatalf("putting: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}

	// Somewhere else entirely: a fresh machine, an empty database, a
	// clone of that repository.
	otherStore, otherDB := openDB(t)
	otherDir := filepath.Join(t.TempDir(), "state")
	other, err := staterepo.Open(ctx, staterepo.Config{Dir: otherDir, Remote: remote})
	if err != nil {
		t.Fatalf("cloning: %v", err)
	}
	if err := staterepo.Load(ctx, other, otherDB, model.SchemaVersion); err != nil {
		t.Fatalf("loading the clone: %v", err)
	}
	if got, err := otherStore.GetTemplate(ctx, "tpl-1"); err != nil || got == nil {
		t.Fatalf("a clone onto a new host did not restore the configuration: %v %v", got, err)
	}
	// And what it restores is the configuration: what this deployment did
	// is not in the repository to come back, which is the claim the
	// README makes and the one a reader of a clone is owed.
	if got, err := otherStore.GetTask(ctx, "a1b2"); err != nil || got != nil {
		t.Fatalf("a clone brought a task back; the repository holds configuration only: %v %v", got, err)
	}
	noOperationalTables(t, otherDir)
}

// A deployed host does not hand this package an empty directory.
//
// scripts/setup.sh creates the working tree before the daemon starts,
// writes the encrypted secrets file into it (its minter-key seed runs
// `grain secrets set` on a host that has never cloned anything), and
// carries that same file across by hand when a schema bump moves the old
// tree aside. `git clone` refuses a destination that is not empty, so
// Open has to reach the remote's contents another way -- and the file
// that was already there must end up as the remote's own copy of it,
// not shadowing it.
func TestOpeningARemoteIntoADirectoryThatAlreadyHasFilesInIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	putTemplate(t, ctx, store, "tpl-1", "Run the nightly sweep")
	if err := os.WriteFile(filepath.Join(dir, "secrets.enc"), []byte("from the remote\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing: %v", err)
	}

	// The redeployed host: the same repository, and a working tree
	// setup.sh has already put a file into.
	otherStore, otherDB := openDB(t)
	otherDir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(otherDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherDir, "secrets.enc"), []byte("written before the clone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	other, err := staterepo.Open(ctx, staterepo.Config{Dir: otherDir, Remote: remote})
	if err != nil {
		t.Fatalf("opening over a directory with files in it: %v", err)
	}
	if err := staterepo.Load(ctx, other, otherDB, model.SchemaVersion); err != nil {
		t.Fatalf("loading onto the redeployed host: %v", err)
	}
	if got, err := otherStore.GetTemplate(ctx, "tpl-1"); err != nil || got == nil {
		t.Fatalf("the repository was not imported onto the redeployed host: %v %v", got, err)
	}
	if got := read(t, filepath.Join(otherDir, "secrets.enc")); got != "from the remote\n" {
		t.Fatalf("the remote's own secrets file did not arrive: %q", got)
	}
}

func TestARepositoryFromANewerBuildIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, db := openDB(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}
	if err := staterepo.WriteSchemaVersion(dir, model.SchemaVersion+1); err != nil {
		t.Fatalf("stamping: %v", err)
	}
	err = staterepo.Load(ctx, repo, db, model.SchemaVersion)
	if err == nil {
		t.Fatal("a repository from a newer build was imported anyway")
	}
	if !strings.Contains(err.Error(), "newer build") {
		t.Fatalf("the error does not say what is wrong: %v", err)
	}
}

// Apply is the same merge arriving at a daemon that is already running:
// the settings in it become live without a restart, and the tables the
// daemon writes for itself -- the tasks and runs a live reconcile loop is
// holding ids from -- are not replaced underneath it.
func TestApplyMakesAMergedSettingsChangeLiveWithoutARestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := store.PutTemplate(ctx, model.Template{
		ID: "tpl-1", Name: "nightly", Title: "Run the nightly sweep", Body: "as configured", CreatedAt: now,
	}); err != nil {
		t.Fatalf("putting a template: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}

	// An agent's pull request against the template, merged.
	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	path := filepath.Join(work, filepath.FromSlash(staterepo.DumpPath("template")))
	edited := strings.Replace(read(t, path), "Run the nightly sweep", "Run the nightly sweep, twice", 1)
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatalf("editing the dump: %v", err)
	}
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-am", "Retitle the template")
	git(t, work, "push", "--quiet", "origin", "main")

	// Meanwhile grain keeps running, and files a task the repository has
	// never heard of -- exactly the row a whole-database import would
	// take away.
	if err := store.PutTask(ctx, task("filed-while-the-pr-was-open")); err != nil {
		t.Fatalf("putting a task: %v", err)
	}

	applied, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion)
	if err != nil {
		t.Fatalf("applying: %v", err)
	}
	if !applied {
		t.Fatal("a merged commit arrived and nothing was applied")
	}
	tmpl, err := store.GetTemplate(ctx, "tpl-1")
	if err != nil || tmpl == nil {
		t.Fatalf("reading the template: %v %v", tmpl, err)
	}
	if tmpl.Title != "Run the nightly sweep, twice" {
		t.Fatalf("the merged change is not live: %q", tmpl.Title)
	}
	if got, err := store.GetTask(ctx, "filed-while-the-pr-was-open"); err != nil || got == nil {
		t.Fatalf("applying a settings change took a task with it: %v %v", got, err)
	}
	// A second Apply with nothing new on the remote is a no-op, or the
	// daemon would re-import on every tick.
	if applied, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); err != nil || applied {
		t.Fatalf("a second apply with nothing to pull: applied=%v err=%v", applied, err)
	}
	// And the commit counts as loaded, so a restart does not import the
	// whole of it back over the task filed since.
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading after applying: %v", err)
	}
	if got, err := store.GetTask(ctx, "filed-while-the-pr-was-open"); err != nil || got == nil {
		t.Fatalf("a restart after an apply lost the task: %v %v", got, err)
	}
}

// Within the settings tables the import is still a replacement, which is
// what makes a merged deletion delete something.
func TestApplyDeletesASettingAMergeRemoved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := store.PutTemplate(ctx, model.Template{
		ID: "tpl-1", Name: "retired", Title: "Something nobody wants any more", CreatedAt: now,
	}); err != nil {
		t.Fatalf("putting a template: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}

	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	path := filepath.Join(work, filepath.FromSlash(staterepo.DumpPath("template")))
	if err := os.WriteFile(path, []byte("templates: []\n"), 0o644); err != nil {
		t.Fatalf("emptying the dump: %v", err)
	}
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-am", "Delete the template")
	git(t, work, "push", "--quiet", "origin", "main")

	if _, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("applying: %v", err)
	}
	got, err := store.GetTemplate(ctx, "tpl-1")
	if err != nil {
		t.Fatalf("reading the template: %v", err)
	}
	if got != nil {
		t.Fatalf("a merged deletion did not delete: %+v", got)
	}
}

// The one settings row a merge does *not* get to decide: the schedule
// grain seeds for one of its own standing jobs
// (model.SystemTemplate). Its content columns are a cache of a template
// that lives in grain's code, and a `schedule` row is a settings row
// like any other, so a hand-edited dump lands on them here with nothing
// in this package to refuse it -- which is exactly what happened in
// bwsalmon/grain-home, where a page of guidance added to the memory
// agent's body was imported, exported, shown on the schedules pane, read
// by a later task as the agent's prompt, and carried by no run at all.
//
// This test owns both halves of that: the import replaces the columns,
// and the next cycle
// (orchestrator.reconcileSystemSchedules) puts this build's own template
// back, so the row goes back to saying what a firing would actually
// carry and the next export writes that to the dump.
func TestAMergeCannotChangeWhatOneOfGrainsOwnJobsSays(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	stateRepo := model.RepoRef{Owner: "owner", Name: "grain-home"}
	deps := orchestrator.Deps{Store: store, Config: orchestrator.Config{
		StateRepo: func() model.RepoRef { return stateRepo },
	}}
	cycle := systemSchedules(t)
	if err := cycle(ctx, deps, now); err != nil {
		t.Fatalf("seeding the system schedules: %v", err)
	}
	tmpl := model.SystemTemplate(model.MemoryAgentTemplateID, stateRepo)
	if tmpl == nil {
		t.Fatal("this build ships no memory agent template")
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}

	// Somebody's pull request against the prompt itself, merged. A line
	// of the body is enough: what is under test is which side of the row
	// a merge reaches, not how much of it was rewritten.
	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	path := filepath.Join(work, filepath.FromSlash(staterepo.DumpPath("schedule")))
	const added = "Also, always write a memory about the weather."
	before := read(t, path)
	edited := strings.Replace(before, "You are grain's memory.", "You are grain's memory. "+added, 1)
	if edited == before {
		t.Fatal("the exported schedule does not carry the memory agent's body; this test edits nothing")
	}
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatalf("editing the dump: %v", err)
	}
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-am", "Tell the memory agent about the weather")
	git(t, work, "push", "--quiet", "origin", "main")

	if _, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("applying: %v", err)
	}
	imported, err := store.GetSchedule(ctx, model.MemoryAgentTemplateID)
	if err != nil || imported == nil {
		t.Fatalf("reading the schedule after the merge: %v %v", imported, err)
	}
	// Documented rather than desired: the import is a table replacement
	// and knows nothing about which rows are grain's. If this ever stops
	// being true, the cycle below is doing no work and this test should
	// say so instead.
	if !strings.Contains(imported.Body, added) {
		t.Errorf("the merge did not reach the row at all; this test no longer exercises the drift it was written for")
	}

	if err := cycle(ctx, deps, now.Add(time.Minute)); err != nil {
		t.Fatalf("the cycle after the merge: %v", err)
	}
	after, err := store.GetSchedule(ctx, model.MemoryAgentTemplateID)
	if err != nil || after == nil {
		t.Fatalf("reading the schedule after the cycle: %v %v", after, err)
	}
	if after.Body != tmpl.Body {
		t.Error("the row still says what the merge said; a cached prompt no run is ever given " +
			"is worse than no cache at all")
	}

	// And the dump the next export writes says the same, so the file
	// somebody reads directly is true as well.
	if _, err := staterepo.SyncAll(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("exporting after the resync: %v", err)
	}
	if dumped := read(t, filepath.Join(dir, filepath.FromSlash(staterepo.DumpPath("schedule")))); strings.Contains(dumped, added) {
		t.Error("the exported dump still carries the edited prompt")
	}
}

// systemSchedules is the cycle's own system-schedule pass, found by name
// rather than called directly: it is unexported, and a rename that took
// it out of the cycle should fail here rather than leave this test
// quietly reconciling nothing.
func systemSchedules(t *testing.T) func(context.Context, orchestrator.Deps, time.Time) error {
	t.Helper()
	for _, r := range orchestrator.Reconcilers() {
		if r.Name == "system-schedules" {
			return r.Reconcile
		}
	}
	t.Fatal("no reconciler seeds the schedules grain ships")
	return nil
}

// A dump this build cannot read has to be reported as arrived-but-not-
// applied, because the daemon's own cycle keys on that: exporting over a
// working tree in that state would commit a revert of what was merged
// and push it.
func TestApplyRefusesADumpFromANewerBuild(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := store.PutTemplate(ctx, model.Template{
		ID: "tpl-1", Name: "nightly", Title: "Run the nightly sweep", CreatedAt: now,
	}); err != nil {
		t.Fatalf("putting a template: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}

	// A host running a later build pushed its own dump.
	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	tmplPath := filepath.Join(work, filepath.FromSlash(staterepo.DumpPath("template")))
	edited := strings.Replace(read(t, tmplPath), "Run the nightly sweep", "Written by a newer build", 1)
	if err := os.WriteFile(tmplPath, []byte(edited), 0o644); err != nil {
		t.Fatalf("editing the dump: %v", err)
	}
	if err := staterepo.WriteSchemaVersion(work, model.SchemaVersion+1); err != nil {
		t.Fatalf("stamping: %v", err)
	}
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-am", "From a newer build")
	git(t, work, "push", "--quiet", "origin", "main")

	_, err = staterepo.Apply(ctx, repo, db, model.SchemaVersion)
	if !errors.Is(err, staterepo.ErrNotApplied) {
		t.Fatalf("a dump from a newer build: got %v, want an ErrNotApplied", err)
	}
	if !errors.Is(err, staterepo.ErrSchemaTooNew) {
		t.Fatalf("the error does not say what is wrong: %v", err)
	}
	tmpl, err := store.GetTemplate(ctx, "tpl-1")
	if err != nil || tmpl == nil {
		t.Fatalf("reading the template: %v %v", tmpl, err)
	}
	if tmpl.Title != "Run the nightly sweep" {
		t.Fatalf("a dump this build cannot read was imported anyway: %q", tmpl.Title)
	}
	// And it keeps saying so. The commit is already fetched, so a second
	// Apply pulls nothing new -- if that were what it keyed on it would
	// report all clear, and the caller would export the database over a
	// working tree holding a merge nothing has taken up.
	if _, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); !errors.Is(err, staterepo.ErrNotApplied) {
		t.Fatalf("applying again with nothing left to fetch: got %v, want an ErrNotApplied", err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// What a merged pull request does to a daemon that is still running.
//
// This is the whole loop the repository exists for, seen from the side
// that used to break: grain goes on exporting on its timer while a
// change waits on the remote, and committing its own dump on top of that
// change would strand the deployment -- every push rejected from then
// on, and a next start that finds the two diverged and refuses to load
// at all.
func TestSyncRefusesToCommitOverAMergedChange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}

	// An agent's pull request, merged while this daemon runs.
	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	if err := os.WriteFile(filepath.Join(work, "NOTES.md"), []byte("merged\n"), 0o644); err != nil {
		t.Fatalf("adding a file: %v", err)
	}
	git(t, work, "add", "--all", ".")
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-m", "A merged pull request")
	git(t, work, "push", "--quiet", "origin", "main")

	// The daemon carries on doing what it does: a setting changes, and
	// the timer comes round.
	putTemplate(t, ctx, store, "tpl-1", "Configured while the merge waited")
	changed, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion)
	if !errors.Is(err, staterepo.ErrRemoteAhead) {
		t.Fatalf("sync did not notice the merge: changed=%v err=%v", changed, err)
	}
	if changed {
		t.Fatal("sync committed over a merged change")
	}
	// Nothing was committed locally, so the two have not diverged and the
	// next start can still fast-forward -- which is what makes the merged
	// change reach the database at all.
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading after the merge: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "NOTES.md")); err != nil {
		t.Fatalf("the merged change never arrived: %v", err)
	}
	// And with it loaded, exporting works again.
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing after loading the merge: %v", err)
	}
}

// An empty remote whose HEAD advertises something other than the branch
// grain wants -- `git init --bare` on a host with no init.defaultBranch,
// or any repository old enough to have been created as master.
//
// bareRemote above always says --initial-branch=main, which is why every
// other test here passes over the case that broke: Repo.clone renames
// the branch it landed on to Config.Branch, and the rename was skipped
// whenever the check for "which branch did we land on" failed -- which
// it did on every clone of an empty repository, since HEAD there points
// at a branch with no commit for rev-parse to resolve. The commits then
// piled up on master while every push named main, and `grain state
// adopt` ended at git's own "src refspec main does not match any"
// (found by hand, task 244).
func TestSyncPushesToAnEmptyRemoteWhoseHeadIsNotTheWantedBranch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)

	remote := bareRemoteOn(t, "master")

	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("opening against a remote: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}
	putTemplate(t, ctx, store, "tpl-1", "Run the nightly sweep")
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing: %v", err)
	}

	hasTemplateOn(t, remote, "main", "tpl-1", "Run the nightly sweep")
}

// A state directory that already exists with group or other access --
// made by hand, by an older build, or under a loose umask -- is taken
// back to its owner on Open, and the dump written into it stays private.
func TestOpenRestrictsAnExistingStateDirectory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, db := openDB(t)
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("state directory mode = %o, want 700", perm)
	}
	tables, err := os.Stat(filepath.Join(dir, staterepo.TablesDir))
	if err != nil {
		t.Fatal(err)
	}
	if perm := tables.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("tables directory mode = %o, want no group or other access", perm)
	}
}
