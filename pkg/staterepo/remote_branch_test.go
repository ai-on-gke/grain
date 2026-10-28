package staterepo_test

// What another branch on the remote is allowed to do to a sync, which is
// nothing.
//
// A state repository is a repository like any other: a pull request
// against it is a branch pushed to the same remote, and a deployment
// that dispatches agents at its own settings has those branches sitting
// beside its own for as long as the pull requests are open. None of them
// is grain's, and none of them may change what grain makes of where its
// own branch is.
//
// That is not free, because of how `git ls-remote` reads the pattern it
// is given: a plain branch name matches any ref whose *tail* is that
// name, so asking for "main" also answers with `refs/heads/x/main`, and
// the answer that comes back first is whichever sorts first. Reading the
// wrong line there is not a cosmetic mistake -- the commit belongs to a
// branch this working tree has never fetched, so the remote looks ahead,
// and sync refuses to export at all for as long as the branch exists
// (ErrRemoteAhead).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// A branch whose name ends in the state branch's own, pushed by somebody
// opening a pull request against grain's settings. The deployment goes
// on exporting.
func TestABranchWhoseNameEndsInTheStateBranchDoesNotStopTheSync(t *testing.T) {
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

	// Somebody's proposed change to a template, on a branch of its own.
	// It sorts before refs/heads/main, so it is the line ls-remote
	// answers with first, and its commit is one this working tree has
	// never seen.
	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	git(t, work, "checkout", "--quiet", "-b", "agent/main")
	if err := os.WriteFile(filepath.Join(work, "NOTES.md"), []byte("proposed\n"), 0o644); err != nil {
		t.Fatalf("writing a file on the branch: %v", err)
	}
	git(t, work, "add", "--all", ".")
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-m", "A proposed change")
	git(t, work, "push", "--quiet", "origin", "agent/main")

	putTemplate(t, ctx, store, "tpl-1", "Configured while a branch was open")
	changed, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion)
	if err != nil {
		t.Fatalf("syncing with another branch open on the remote: %v", err)
	}
	if !changed {
		t.Fatal("the sync committed nothing")
	}
	hasTemplateOn(t, remote, "main", "tpl-1", "Configured while a branch was open")
}

// The same reading, asked the other way round: Pull treats a fetch that
// failed as "nothing to pull" when the branch is not on the remote yet,
// and a branch that merely ends in its name is not it.
func TestABranchWhoseNameEndsInTheStateBranchIsNotTheStateBranch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, db := openDB(t)
	remote := bareRemote(t)

	// Only the other branch exists on the remote: a repository somebody
	// pushed a proposal to before any deployment had seeded it.
	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	if err := os.WriteFile(filepath.Join(work, "NOTES.md"), []byte("proposed\n"), 0o644); err != nil {
		t.Fatalf("writing a file on the branch: %v", err)
	}
	git(t, work, "checkout", "--quiet", "-b", "agent/main")
	git(t, work, "add", "--all", ".")
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-m", "A proposed change")
	git(t, work, "push", "--quiet", "origin", "agent/main")

	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	// The state branch is not there, so this is a seed and not a failure
	// to fetch one.
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading against a remote holding only another branch: %v", err)
	}
	if !staterepo.HasDump(dir) {
		t.Fatal("the repository was never seeded")
	}
	out := git(t, remote, "show", "main:"+staterepo.SchemaVersionFile)
	if strings.TrimSpace(out) == "" {
		t.Fatal("the seed never reached the remote")
	}
}
