package staterepo_test

// What happens to a commit whose push failed, on a deployment that then
// goes quiet.
//
// The push is the one step of a sync that talks to anybody else, so it
// is the one that fails on its own -- a remote that is briefly
// unreachable, a credential that expired between the commit and the
// network call, a remote that will not take what it is offered. The
// commit is already made by then and stays on the host, and what these
// tests pin down is that the next tick carries it out even when the
// database has stopped changing and there is nothing whatever left to
// commit. That is the state a deployment sits in for hours otherwise,
// and it is the window in which a merged pull request turns an unpushed
// export into a divergence (diverge.go).

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// The remote goes away for one tick, and the database stops changing
// for good. Nothing files a task, nothing changes a setting, the churn
// tier is not due -- so every export after the failure writes the same
// bytes and there is nothing to commit ever again. The commit still has
// to reach the remote on the first tick that can reach it.
func TestAPushThatFailedIsRetriedWithNothingNewToCommit(t *testing.T) {
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
	putTemplate(t, ctx, store, "tpl-1", "Configured before the push failed")
	gone := remote + ".gone"
	if err := os.Rename(remote, gone); err != nil {
		t.Fatalf("taking the remote away: %v", err)
	}
	changed, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion)
	if err == nil {
		t.Fatal("a sync whose push could not reach the remote reported success")
	}
	if !changed {
		t.Fatalf("the export before the failed push committed nothing: %v", err)
	}
	if err := os.Rename(gone, remote); err != nil {
		t.Fatalf("bringing the remote back: %v", err)
	}

	// Not a row is written from here on: this is the idle deployment,
	// and this sync is a tick with nothing of its own to say.
	changed, err = staterepo.Sync(ctx, repo, db, model.SchemaVersion)
	if err != nil {
		t.Fatalf("syncing once the remote answered again: %v", err)
	}
	if changed {
		t.Fatal("a sync with an unchanged database reported something to commit")
	}
	hasTemplateOn(t, remote, "main", "tpl-1", "Configured before the push failed")
}

// A remote that answers and then refuses what it is offered: a
// credential that may read and may not write, a branch protected against
// the deployment's own token, a hook that says no. Every tick tries it
// again and every tick says so, because a sync that fell quiet after the
// first one would leave the pane reporting nothing at all while the
// commits piled up behind it (pkg/cli/statemanager.go reports whatever
// the last cycle returned).
func TestAPushThatKeepsFailingIsReportedByEveryTick(t *testing.T) {
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
	refuse := filepath.Join(remote, "hooks", "pre-receive")
	if err := os.WriteFile(refuse, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatalf("teaching the remote to refuse: %v", err)
	}
	putTemplate(t, ctx, store, "tpl-1", "Configured before the push was refused")
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err == nil {
		t.Fatal("a sync whose push was refused reported success")
	}
	// Three more ticks with nothing to commit, each of which has to
	// report the refusal rather than the silence of having nothing to do.
	for tick := 0; tick < 3; tick++ {
		if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err == nil {
			t.Fatalf("tick %d after a refused push reported success", tick)
		}
	}
	if err := os.Remove(refuse); err != nil {
		t.Fatalf("letting the remote take a push again: %v", err)
	}
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing once the remote took pushes again: %v", err)
	}
	hasTemplateOn(t, remote, "main", "tpl-1", "Configured before the push was refused")
}
