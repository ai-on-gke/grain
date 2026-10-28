package staterepo_test

// What the commit messages in a state repository say when the
// deployment writing it is upgraded.
//
// Every other commit here is the deployment doing something and its diff
// says what: a task filed, a setting changed. The first export by a new
// build is the one commit whose diff can be bigger than the activity
// behind it, because a build writes the dump the way its own code does,
// and it is the one thing a reviewer cannot recover from the repository
// afterwards -- "the image this deployment runs" was true of one build
// on one day. So it is named in the message: which build wrote the
// export before this one, which build wrote this one, and the schema
// they agree on.
//
// The same question for the CI step, which is the other commit an
// upgrade makes here: the one-line diff that repoints it at the new
// image says which image it left and which it arrived at.

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// message is the full commit message at HEAD.
func message(t *testing.T, dir string) string {
	t.Helper()
	return git(t, dir, "log", "-1", "--format=%B")
}

const (
	buildBefore = "ghcr.io/bwsalmon/grain/grain:sha-0000000 (built from 000000000000)"
	buildAfter  = "ghcr.io/bwsalmon/grain/grain:sha-abc1234 (built from abc123456789)"
	// The image the check runs is held still across these tests unless a
	// test is about it, so that the export is the only commit a sync
	// makes and HEAD is the commit being read.
	pinnedImage = "ghcr.io/bwsalmon/grain/grain:sha-0000000"
)

func TestTheFirstExportByAnUpgradedBuildNamesBothBuilds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openRealDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{
		Dir: dir, Remote: remote, CheckImage: pinnedImage, Build: buildBefore,
	})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}

	// The upgrade: the same working tree, the same repository, a grain
	// that says it is a different build.
	upgraded, err := staterepo.Open(ctx, staterepo.Config{
		Dir: dir, Remote: remote, CheckImage: pinnedImage, Build: buildAfter,
	})
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	putTemplate(t, ctx, store, "tpl-1", "Configured after the upgrade")
	if _, err := staterepo.Sync(ctx, upgraded, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing after the upgrade: %v", err)
	}
	got := message(t, dir)
	for _, want := range []string{
		"upgraded",
		buildBefore,
		buildAfter,
		"schema " + strconv.Itoa(model.SchemaVersion),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the first export after the upgrade does not mention %q:\n%s", want, got)
		}
	}

	// And once, not on every commit afterwards: a message that says
	// "upgraded" every thirty seconds tells a reader nothing on the
	// commit where it is true.
	putTemplate(t, ctx, store, "tpl-2", "And a second setting, a tick later")
	if _, err := staterepo.Sync(ctx, upgraded, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing again: %v", err)
	}
	next := message(t, dir)
	if next == got {
		t.Fatal("the second sync committed nothing; the test is not testing anything")
	}
	if strings.Contains(next, "upgraded") || strings.Contains(next, buildBefore) {
		t.Errorf("every export after the upgrade still announces it:\n%s", next)
	}
}

// A deployment that has not been upgraded says nothing at all about
// builds, and neither does one whose caller does not know which build it
// is -- an unstamped binary, or any test that does not set Config.Build.
// Both would otherwise be an upgrade announced where none happened.
func TestAnOrdinaryExportSaysNothingAboutBuilds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openRealDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{
		Dir: dir, Remote: remote, CheckImage: pinnedImage, Build: buildBefore,
	})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}
	putTemplate(t, ctx, store, "tpl-1", "Configured by the build that seeded it")
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing: %v", err)
	}
	if got := message(t, dir); strings.Contains(got, "upgraded") || strings.Contains(got, "built from") {
		t.Errorf("an export by the build that seeded the repository claims an upgrade:\n%s", got)
	}
}

// The upgrade rides on the commit that carries the new build's dump,
// whenever that turns out to be. A deployment upgraded while nothing is
// happening syncs with nothing to commit -- there is no commit to say it
// in -- and the fact must not be spent there: the next commit, an hour
// later or a minute later, is the first one a new build wrote and is the
// one a reviewer wants the wording on.
func TestAnUpgradeWithNothingToCommitIsSaidOnTheNextCommit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openRealDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{
		Dir: dir, Remote: remote, CheckImage: pinnedImage, Build: buildBefore,
	})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}
	upgraded, err := staterepo.Open(ctx, staterepo.Config{
		Dir: dir, Remote: remote, CheckImage: pinnedImage, Build: buildAfter,
	})
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	// An idle tick: the database has not moved since the seed, so the
	// export is byte-identical and there is nothing to commit.
	before := head(t, dir)
	if committed, err := staterepo.Sync(ctx, upgraded, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing an idle deployment: %v", err)
	} else if committed {
		t.Fatalf("an idle sync committed something; the test is not testing anything")
	}
	if head(t, dir) != before {
		t.Fatal("an idle sync moved HEAD")
	}
	// And then the deployment does something.
	putTemplate(t, ctx, store, "tpl-1", "The first setting the new build exported")
	if _, err := staterepo.Sync(ctx, upgraded, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing: %v", err)
	}
	got := message(t, dir)
	if !strings.Contains(got, buildBefore) || !strings.Contains(got, buildAfter) {
		t.Errorf("the first commit written by the upgraded build does not name the upgrade:\n%s", got)
	}
}

// The commit that repoints the CI step is the other one an upgrade makes
// here, and the image is the version a reader of this repository cares
// about: it is what `grain state check` runs, and a check running a
// build that knows another schema fails every pull request. The diff is
// one line and the message says which line it was.
func TestTheCheckRepointCommitNamesTheImageItLeftAndTheOneItRuns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openRealDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	const was = "ghcr.io/bwsalmon/grain/grain:sha-0000000"
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote, CheckImage: was})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}
	const runs = "ghcr.io/bwsalmon/grain/grain:sha-abc1234"
	upgraded, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote, CheckImage: runs})
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	putTemplate(t, ctx, store, "tpl-1", "Configured after the upgrade")
	if _, err := staterepo.Sync(ctx, upgraded, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing after the upgrade: %v", err)
	}
	at := strings.TrimSpace(git(t, dir, "log", "-1", "--format=%H", "--", staterepo.WorkflowFile))
	got := git(t, dir, "show", "-s", "--format=%B", at)
	if !strings.Contains(got, "from "+was) {
		t.Errorf("the repointing commit does not say which image the check was running:\n%s", got)
	}
	if !strings.Contains(got, "to   "+runs) {
		t.Errorf("the repointing commit does not say which image the check runs now:\n%s", got)
	}
}
