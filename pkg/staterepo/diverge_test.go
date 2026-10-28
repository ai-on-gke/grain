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

// Divergence, against a real bare remote and a real second clone: a push
// that failed, a pull request merged before it could be retried, and what
// the deployment does about it on its next sync.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// diverge produces the real thing rather than a hand-built history: an
// export grain committed and could not push -- the remote is pointed at a
// directory that is not there for exactly as long as the push takes --
// followed by somebody else's commit on the remote, which is what a
// merged pull request against the state repository leaves behind.
//
// It returns the two sides so a test can name them: nothing here asserts,
// so a test that wants a divergence somebody else made can build the
// local half itself.
func diverge(t *testing.T, ctx context.Context, repo *staterepo.Repo, remote, dir string, local func()) {
	t.Helper()
	unreachable := filepath.Join(t.TempDir(), "gone.git")
	git(t, dir, "remote", "set-url", "origin", unreachable)
	local()
	git(t, dir, "remote", "set-url", "origin", remote)

	// And the merge that lands in the meantime, pushed from a clone that
	// was made before any of it.
	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	if err := os.WriteFile(filepath.Join(work, "NOTES.md"), []byte("merged by somebody\n"), 0o644); err != nil {
		t.Fatalf("writing the merged file: %v", err)
	}
	git(t, work, "add", "--all", ".")
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-m", "A merged pull request")
	git(t, work, "push", "--quiet", "origin", "main")
}

// The failure this exists for, end to end: grain's own export is stranded
// on this host, a merge is stranded on the remote, and neither moves
// again until the divergence is cleared. Both directions have to recover
// in the one cycle -- what was merged arrives here, and what only this
// host had reaches the remote.
func TestADivergenceMadeOfGrainsOwnExportsRecovers(t *testing.T) {
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

	diverge(t, ctx, repo, remote, dir, func() {
		// A setting changes and the timer comes round, exactly as it does
		// on any other tick. The commit is made; the push is what fails.
		putTemplate(t, ctx, store, "tpl-1", "Configured while the push was failing")
		if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err == nil {
			t.Fatal("the push against an unreachable remote reported success")
		}
	})

	// Where the deployment is now, and where it stayed forever before
	// this: a pull that refuses, tick after tick.
	if _, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); !errors.Is(err, staterepo.ErrDiverged) {
		t.Fatalf("applying over a divergence: got %v, want an ErrDiverged", err)
	}

	recovered, err := repo.RecoverDiverged(ctx)
	if err != nil || !recovered {
		t.Fatalf("recovering a divergence made only of grain's own exports: %v %v", recovered, err)
	}

	// Inbound: the merged commit is in the working tree, and the history
	// is the remote's own rather than a merge of the two.
	if _, err := os.Stat(filepath.Join(dir, "NOTES.md")); err != nil {
		t.Fatalf("the merged change never arrived: %v", err)
	}
	if _, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("applying after the recovery: %v", err)
	}

	// Outbound: the deployment can export and push again, which is the
	// half of the recovery that had stopped. A setting changed after it
	// reaches the remote on the very next sync.
	//
	// And the setting that was stranded is still there. The reset threw
	// away the commit that held it, but the import after it is a
	// three-way merge against the commit this host last exported
	// (merge.go): the merge did not touch tpl-1, so the database's
	// version stands and the next export writes it out. This used to be
	// a replacement out of the older dump, which rewound it.
	if got, err := store.GetTemplate(ctx, "tpl-1"); err != nil || got == nil ||
		got.Title != "Configured while the push was failing" {
		t.Fatalf("the import after the recovery rewound a setting the merge did not touch: %v %v",
			got, err)
	}
	putTemplate(t, ctx, store, "tpl-2", "Configured once the divergence was cleared")
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing after the recovery: %v", err)
	}
	hasTemplateOn(t, remote, "main", "tpl-2", "Configured once the divergence was cleared")
	// And what was merged is still there, rather than reverted by the
	// export that followed it.
	if got := git(t, remote, "show", "main:NOTES.md"); !strings.Contains(got, "merged by somebody") {
		t.Fatalf("the export committed over the merge:\n%s", got)
	}
	noOperationalTablesOn(t, remote, "main")
}

// The same recovery, reached from a start rather than from a tick, which
// is the path pkg/cli/daemon.go takes: Load, and if that is a
// divergence grain made itself, recover and Load again.
//
// The rows written while the pushes were failing exist only in the
// database -- the commits that held them are exactly what the recovery
// threw away, on the argument that the database still has them -- so
// this is the one place that argument can be falsified. It was:
// Load read "HEAD is not the commit we recorded" as "a merge arrived"
// and replaced the whole state tier from a dump older than the database
// by however long the remote had been unreachable, deleting every task
// filed in that window.
//
// The window is the same one and the rows in it are now grain's own
// alone, since the repository carries no more than the configuration:
// the tasks and the runs of that window are in the database and in
// nothing else at all, and no recovery, reset or re-import can be asked
// to put one back. The settings half of the same window is the other
// deal, stated in the test below it: the repository is what says what a
// setting is, and an edit stranded here loses to what the merge brought
// and is announced as a reset rather than kept.
func TestAStartThatRecoversADivergenceKeepsWhatTheDatabaseHas(t *testing.T) {
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
	if err := store.PutTemplate(ctx, model.Template{
		ID: "tpl-1", Name: "nightly", Title: "Run the nightly sweep", CreatedAt: now,
	}); err != nil {
		t.Fatalf("putting a template: %v", err)
	}
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing: %v", err)
	}

	diverge(t, ctx, repo, remote, dir, func() {
		// Two ticks' worth of work that got as far as a commit and no
		// further, which is what a remote that goes away for a few minutes
		// leaves behind. Each tick files a task -- which nothing but this
		// database will ever hold -- and changes a setting, which is what
		// gives the tick an export to strand in the first place.
		for _, id := range []string{"filed-first", "filed-second"} {
			if err := store.PutTask(ctx, task(id)); err != nil {
				t.Fatalf("putting: %v", err)
			}
			putTemplate(t, ctx, store, "tpl-1", "Configured at "+id)
			if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err == nil {
				t.Fatal("the push against an unreachable remote reported success")
			}
		}
	})

	// The process restarts, and takes the daemon's own path.
	reopened, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	if err := staterepo.Load(ctx, reopened, db, model.SchemaVersion); err != nil {
		if !errors.Is(err, staterepo.ErrDiverged) {
			t.Fatalf("loading over a divergence: got %v, want an ErrDiverged", err)
		}
		recovered, rerr := reopened.RecoverDiverged(ctx)
		if rerr != nil || !recovered {
			t.Fatalf("recovering a divergence made only of grain's own exports: %v %v", recovered, rerr)
		}
		if err := staterepo.Load(ctx, reopened, db, model.SchemaVersion); err != nil {
			t.Fatalf("loading after the recovery: %v", err)
		}
	}

	for _, id := range []string{"filed-first", "filed-second"} {
		got, err := store.GetTask(ctx, id)
		if err != nil {
			t.Fatalf("reading %s: %v", id, err)
		}
		if got == nil {
			t.Fatalf("%s was written while the push was failing and the start that "+
				"recovered the divergence rolled it back", id)
		}
	}
	// What was merged is live all the same -- the point of recovering at
	// all is that both directions move again.
	if _, err := os.Stat(filepath.Join(dir, "NOTES.md")); err != nil {
		t.Fatalf("the merged change never arrived: %v", err)
	}
	// The settings are kept too: the two stranded exports were reset
	// away, but the merge did not touch tpl-1, so the three-way import
	// (merge.go) leaves the database's version in place rather than
	// rewinding it to the dump the merge left behind.
	tpl, err := store.GetTemplate(ctx, "tpl-1")
	if err != nil || tpl == nil {
		t.Fatalf("reading the template after the recovery: %v %v", tpl, err)
	}
	if tpl.Title != "Configured at filed-second" {
		t.Fatalf("the settings after a recovery were rewound to the old export: %q", tpl.Title)
	}
	// And the export that follows puts the database back on the remote,
	// on top of the merge: the deployment is exporting and pushing again,
	// which is what the divergence had stopped.
	putTemplate(t, ctx, store, "tpl-2", "Configured once the divergence was cleared")
	if _, err := staterepo.Sync(ctx, reopened, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing after the recovery: %v", err)
	}
	hasTemplateOn(t, remote, "main", "tpl-2", "Configured once the divergence was cleared")
	noOperationalTablesOn(t, remote, "main")
}

// A merge that arrives the ordinary way -- fast-forwarded, no divergence
// -- is imported exactly as the recovery above is: the settings out of
// it, and nothing else. The two cases differ only in how far behind the
// database the dump is, and neither is a reason to let a dump say what
// grain itself did.
//
// What "nothing else" means is now stronger than it was, and this is
// where it is pinned. It used to mean that a hand edit to task.json was
// read and not applied -- the edit sat in the repository, ignored, and
// the next export wrote the database's version back over it. The export
// carries no such file any more, so somebody proposing one is proposing
// a file the repository has no place for: it is not imported, it does
// not survive the next export, and the deployment's own record of what
// it did is untouched by the whole exercise.
func TestAnOrdinaryMergeImportsOnlyTheSettingsAtAStart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := store.PutTask(ctx, task("a1b2")); err != nil {
		t.Fatalf("putting: %v", err)
	}
	putTemplate(t, ctx, store, "tpl-1", "Run the nightly sweep")
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}
	// Somebody edits the repository and merges it, with nothing stranded
	// on this side: a settings change, and -- in the same breath, by a
	// hand that has read a README from before the narrowing -- a file
	// claiming to say what grain did.
	mergeIntoRemote(t, remote, staterepo.DumpPath("template"), "Run the nightly sweep", "Retitled by a pull request")
	mergeAFile(t, remote, staterepo.TablesDir+"/task.json",
		`[{"id":"a1b2","title":"Retitled by a pull request"}]`+"\n")

	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading after the merge: %v", err)
	}
	tpl, err := store.GetTemplate(ctx, "tpl-1")
	if err != nil || tpl == nil {
		t.Fatalf("reading the template: %v %v", tpl, err)
	}
	if tpl.Title != "Retitled by a pull request" {
		t.Fatalf("the merged settings change did not reach the database: %q", tpl.Title)
	}
	got, err := store.GetTask(ctx, "a1b2")
	if err != nil || got == nil {
		t.Fatalf("reading the task: %v %v", got, err)
	}
	if got.Title != "Rename the endpoint" {
		t.Fatalf("a merged edit to grain's own record was imported: %q", got.Title)
	}
	// And the export that follows takes the file back out rather than
	// leaving it there to be read as this deployment's task list by
	// whoever clones the repository next.
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing after the merge: %v", err)
	}
	noOperationalTables(t, dir)
	noOperationalTablesOn(t, remote, "main")
}

// mergeAFile is a pull request that adds or replaces one file outright,
// for the merges that are not an edit to something grain wrote.
func mergeAFile(t *testing.T, remote, name, content string) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "pull-request")
	git(t, "", "clone", "--quiet", remote, work)
	path := filepath.Join(work, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("preparing %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	git(t, work, "add", "--all", ".")
	git(t, work, "-c", "user.email=someone@example.com", "-c", "user.name=someone",
		"commit", "-m", "A merged pull request: "+name)
	git(t, work, "push", "--quiet", "origin", "main")
}

// The other half of the rule: a commit that is not grain's own export is
// never reset over, however inconvenient the divergence is. A hand edit
// in the working tree exists nowhere else -- no export will write it back
// -- so this stays a refusal with the commit named, and the deployment
// waits for a human.
func TestADivergenceSomebodyElseMadeIsRefused(t *testing.T) {
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
	if err := store.PutTask(ctx, task("a1b2")); err != nil {
		t.Fatalf("putting: %v", err)
	}

	diverge(t, ctx, repo, remote, dir, func() {
		if err := os.WriteFile(filepath.Join(dir, "HAND-EDIT.md"), []byte("by a human\n"), 0o644); err != nil {
			t.Fatalf("writing: %v", err)
		}
		git(t, dir, "add", "--all", ".")
		git(t, dir, "-c", "user.email=someone@example.com", "-c", "user.name=someone",
			"commit", "-m", "Something a person did on the host")
	})

	before := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))
	recovered, err := repo.RecoverDiverged(ctx)
	if recovered {
		t.Fatal("grain reset over a commit it did not write")
	}
	if !errors.Is(err, staterepo.ErrDiverged) {
		t.Fatalf("refusing somebody else's commit: got %v, want an ErrDiverged", err)
	}
	if !strings.Contains(err.Error(), "someone") {
		t.Fatalf("the refusal does not say who wrote the commit in the way: %v", err)
	}
	if after := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD")); after != before {
		t.Fatalf("the working tree moved anyway: %s -> %s", before, after)
	}
	if _, err := os.Stat(filepath.Join(dir, "HAND-EDIT.md")); err != nil {
		t.Fatalf("the commit grain refused to reset over was reset over anyway: %v", err)
	}
}

// What the deployment does while that refusal stands, and what it does
// when a human clears it.
//
// A divergence grain will not resolve is a deployment that has stopped
// syncing until somebody looks at it. What it must not become is a
// deployment that has stopped: the database is still the live state, so
// tasks go on being filed and runs go on being recorded, and the state
// that piles up in the meantime has to reach the remote once the way is
// clear rather than having been thrown away in the interim.
func TestADeploymentKeepsRunningThroughADivergenceItCannotResolve(t *testing.T) {
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
	putTemplate(t, ctx, store, "tpl-1", "Run the nightly sweep")
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing: %v", err)
	}

	diverge(t, ctx, repo, remote, dir, func() {
		if err := os.WriteFile(filepath.Join(dir, "HAND-EDIT.md"), []byte("by a human\n"), 0o644); err != nil {
			t.Fatalf("writing: %v", err)
		}
		git(t, dir, "add", "--all", ".")
		git(t, dir, "-c", "user.email=someone@example.com", "-c", "user.name=someone",
			"commit", "-m", "Something a person did on the host")
	})

	// Several cycles of the daemon's loop over the stuck repository. Each
	// one refuses in the same way, and none of them touches the database.
	for tick := 0; tick < 3; tick++ {
		if err := store.PutTask(ctx, task(fmt.Sprintf("filed-while-stuck-%d", tick))); err != nil {
			t.Fatalf("putting: %v", err)
		}
		if _, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); !errors.Is(err, staterepo.ErrDiverged) {
			t.Fatalf("tick %d: got %v, want an ErrDiverged", tick, err)
		}
		if recovered, err := repo.RecoverDiverged(ctx); recovered || !errors.Is(err, staterepo.ErrDiverged) {
			t.Fatalf("tick %d: grain resolved a divergence it should not: %v %v", tick, recovered, err)
		}
		// The export refuses too, rather than committing this deployment's
		// dump on top of a history the remote has moved past.
		if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); !errors.Is(err, staterepo.ErrRemoteAhead) {
			t.Fatalf("tick %d: exporting over a divergence: got %v, want an ErrRemoteAhead", tick, err)
		}
		// And the settings the deployment is running on are untouched: a
		// stuck repository is not a reason to lose what is live.
		tmpl, err := store.GetTemplate(ctx, "tpl-1")
		if err != nil || tmpl == nil {
			t.Fatalf("tick %d: reading the template: %v %v", tick, tmpl, err)
		}
		if tmpl.Title != "Run the nightly sweep" {
			t.Fatalf("tick %d: the template changed while the repository was stuck: %q", tick, tmpl.Title)
		}
	}

	// The operator does what the error told them to: keeps their own
	// commit, on top of what was merged.
	git(t, dir, "fetch", "--quiet", "origin", "main")
	git(t, dir, "rebase", "--quiet", "FETCH_HEAD")
	git(t, dir, "push", "--quiet", "origin", "main")

	// And the deployment picks up where it left off. Everything it wrote
	// while it was stuck is in the database, where it always was and
	// where it now stays -- the repository never held it and the hours of
	// refusals cost it nothing.
	if _, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("applying once the divergence was resolved: %v", err)
	}
	for tick := 0; tick < 3; tick++ {
		id := fmt.Sprintf("filed-while-stuck-%d", tick)
		if got, err := store.GetTask(ctx, id); err != nil || got == nil {
			t.Fatalf("%s was filed while the repository was stuck and is gone: %v %v", id, got, err)
		}
	}
	// And the settings start reaching the remote again, which is the
	// thing that had stopped.
	putTemplate(t, ctx, store, "tpl-1", "Configured once the divergence was cleared")
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing once the divergence was resolved: %v", err)
	}
	hasTemplateOn(t, remote, "main", "tpl-1", "Configured once the divergence was cleared")
	noOperationalTablesOn(t, remote, "main")
	if got := git(t, remote, "show", "main:NOTES.md"); !strings.Contains(got, "merged by somebody") {
		t.Fatalf("the merged change was lost in the resolution:\n%s", got)
	}
	if got := git(t, remote, "show", "main:HAND-EDIT.md"); !strings.Contains(got, "by a human") {
		t.Fatalf("the commit grain refused to reset over was lost in the resolution:\n%s", got)
	}
}

// A deployment that is merely ahead of its remote -- which is every
// deployment between its commit and its push -- has not diverged, and
// must not have its own unpushed export thrown away by anything that
// mistakes the two.
func TestBeingAheadOfTheRemoteIsNotADivergence(t *testing.T) {
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
	unreachable := filepath.Join(t.TempDir(), "gone.git")
	git(t, dir, "remote", "set-url", "origin", unreachable)
	putTemplate(t, ctx, store, "tpl-1", "Configured before the push failed")
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err == nil {
		t.Fatal("the push against an unreachable remote reported success")
	}
	git(t, dir, "remote", "set-url", "origin", remote)

	recovered, err := repo.RecoverDiverged(ctx)
	if recovered || err != nil {
		t.Fatalf("an unpushed export was treated as a divergence: %v %v", recovered, err)
	}
	// The commit is still here to be pushed, and the next sync carries it
	// out -- not a row has changed in the database since, so this is a
	// tick with nothing whatever of its own to commit (push_test.go).
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing after the remote came back: %v", err)
	}
	hasTemplateOn(t, remote, "main", "tpl-1", "Configured before the push failed")
}
