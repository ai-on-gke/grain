package staterepo_test

// What happens to a reset notice that reached nobody (resets.go's
// announceResets, Repo.UndeliveredResets).
//
// The journal keeps every reset, and the surface that reaches a person
// -- a comment on the pull request the merge arrived in -- reaches most
// deployments. These tests are about the rest: local-only, not on
// GitHub, no credential, no pull request, or an outright failure. For
// those the deployment keeps the notice on the host, so that the State
// pane and `grain state status` can hand it to whoever turns up, and so
// that a person who has read it can say so and be rid of it.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/staterepo"
)

// A notice the deployment could not deliver is kept, with the values in
// it and the reason it was kept -- which is the sentence the surface
// that gave up wrote, so that the pane and the journal say one thing.
func TestAnUndeliveredResetIsKeptForThePane(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	// The deployment this is for: one with nowhere to say it. The
	// sentinel is what says so, and it is not a failure.
	announce := func(context.Context, string, []staterepo.Reset) error {
		return fmt.Errorf("this deployment's state repository is local only, so %w",
			staterepo.ErrNowhereToAnnounce)
	}
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote,
		AnnounceResets: func(ctx context.Context, commit string, resets []staterepo.Reset) error {
			return announce(ctx, commit, resets)
		}})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	configWith(t, store, func(c *model.Config) { c.UpgradeCheckMode = model.UpgradeCheckOff })
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}
	if _, ok := repo.UndeliveredResets(ctx); ok {
		t.Fatal("a deployment that has taken nothing up is holding a notice")
	}

	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	mergeDropping(t, work, "upgrade_check_mode")
	if applied, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); err != nil || !applied {
		t.Fatalf("applying the reset: applied=%v err=%v", applied, err)
	}

	kept, ok := repo.UndeliveredResets(ctx)
	if !ok || len(kept.Notices) != 1 {
		t.Fatalf("a notice nobody was told about was not kept: %+v %v", kept, ok)
	}
	notice := kept.Notices[0]
	head, err := repo.Head(ctx)
	if err != nil {
		t.Fatalf("reading the head: %v", err)
	}
	if notice.Commit != head {
		t.Errorf("the notice names %q, want the merge that caused it %q", notice.Commit, head)
	}
	if notice.At.IsZero() {
		t.Error("the notice does not say when it happened, so the pane cannot either")
	}
	// The reason, verbatim: the pane shows it as its own explanation of
	// why it is showing anything, which is the whole of what makes the
	// narrow rule readable (docs/state-pane-reset-notice.md).
	if notice.Reason != "this deployment's state repository is local only, so there is nowhere to say it" {
		t.Errorf("the notice's reason is %q, want the sentence the surface wrote", notice.Reason)
	}
	// And not a fault: this deployment is running exactly as it was set
	// up to.
	if notice.Failed {
		t.Error("having nowhere to say something was recorded as a failure")
	}
	if len(notice.Values) != 1 {
		t.Fatalf("the notice carries %d values, want the one that was lost: %+v", len(notice.Values), notice.Values)
	}
	want := staterepo.Reset{
		Path:   filepath.Join(staterepo.TablesDir, "grain_config.json"),
		Column: "upgrade_check_mode",
		Row:    "id = 1",
		From:   "'off'",
		To:     "'auto'",
	}
	if notice.Values[0] != want {
		t.Errorf("kept %+v, want %+v", notice.Values[0], want)
	}

	// A second merge that resets something before anybody has read the
	// first one appends. Replacing would throw the first notice away,
	// which is the one failure this whole surface exists to prevent.
	announce = func(context.Context, string, []staterepo.Reset) error {
		return errors.New("grain could not post this on acme/grain-state#7")
	}
	configWith(t, store, func(c *model.Config) { c.MaxWorkers = 7 })
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("exporting the value the next merge takes away: %v", err)
	}
	git(t, work, "pull", "--quiet", "--ff-only")
	mergeDropping(t, work, "max_workers")
	if applied, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); err != nil || !applied {
		t.Fatalf("applying the second reset: applied=%v err=%v", applied, err)
	}
	kept, _ = repo.UndeliveredResets(ctx)
	if len(kept.Notices) != 2 {
		t.Fatalf("the second notice replaced the first instead of joining it: %+v", kept.Notices)
	}
	if kept.Notices[0].Values[0].Column != "upgrade_check_mode" || kept.Notices[1].Values[0].Column != "max_workers" {
		t.Errorf("the notices are not in the order they happened: %+v", kept.Notices)
	}
	// This one *is* a fault: grain had somewhere to say it and could not.
	if !kept.Notices[1].Failed {
		t.Error("a notice grain failed to deliver was recorded as there being nowhere to deliver it")
	}

	// And a person saying they have read them takes both away, for the
	// whole deployment. There is nothing to un-dismiss: the values are
	// in the journal, and the marker is not the record.
	if err := repo.ClearUndeliveredResets(ctx); err != nil {
		t.Fatalf("dismissing: %v", err)
	}
	if kept, ok := repo.UndeliveredResets(ctx); ok {
		t.Fatalf("dismissed notices came back: %+v", kept)
	}
	// Twice is not an error: two browsers, or a browser and a retry.
	if err := repo.ClearUndeliveredResets(ctx); err != nil {
		t.Fatalf("dismissing an empty marker: %v", err)
	}
}

// The deployment that gets its comment posted keeps nothing. The whole
// rule is "only when it could not be delivered", and a pane that carried
// a notice for every reset would fire on deployments that were told
// properly, where it is noise on a thing that already worked.
func TestADeliveredResetIsNotKept(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote,
		AnnounceResets: func(context.Context, string, []staterepo.Reset) error { return nil }})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	configWith(t, store, func(c *model.Config) { c.UpgradeCheckMode = model.UpgradeCheckOff })
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}
	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	mergeDropping(t, work, "upgrade_check_mode")
	if applied, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); err != nil || !applied {
		t.Fatalf("applying the reset: applied=%v err=%v", applied, err)
	}
	if kept, ok := repo.UndeliveredResets(ctx); ok {
		t.Fatalf("a notice that was delivered was kept for the pane as well: %+v", kept)
	}
}

// The marker is a fact about this host and its credential, not about the
// repository: another clone of the same state, whose deployment can
// comment, has nothing undelivered. So it must never be committed, and
// an export that carried it into the dump would push one deployment's
// unread notices to everybody else's.
func TestUndeliveredResetsAreNotCommitted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote,
		AnnounceResets: func(context.Context, string, []staterepo.Reset) error {
			return errors.New("grain could not post this anywhere")
		}})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	configWith(t, store, func(c *model.Config) { c.UpgradeCheckMode = model.UpgradeCheckOff })
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}
	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	mergeDropping(t, work, "upgrade_check_mode")
	if applied, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); err != nil || !applied {
		t.Fatalf("applying the reset: applied=%v err=%v", applied, err)
	}
	if _, ok := repo.UndeliveredResets(ctx); !ok {
		t.Fatal("nothing was kept, so this test is checking nothing")
	}
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	if out := git(t, repo.Dir(), "status", "--porcelain"); out != "" {
		t.Errorf("the working tree is not clean after an export, so something about the notice is "+
			"in the tree:\n%s", out)
	}
	if out := git(t, repo.Dir(), "log", "--name-only", "--pretty=format:"); strings.Contains(out, "undelivered") {
		t.Errorf("a notice this host could not deliver was committed to the repository:\n%s", out)
	}
}

// mergeDropping is a merge that arrives from a branch taken before a
// column existed: the file simply does not mention it, so applying it
// puts that column back on the schema's default.
func mergeDropping(t *testing.T, work, column string) {
	t.Helper()
	dropColumns(t, filepath.Join(work, staterepo.TablesDir, "grain_config.json"), []string{column}, nil)
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-am",
		"A settings file branched before "+column)
	git(t, work, "push", "--quiet", "origin", "main")
}
