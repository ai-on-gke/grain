package demo

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestDemoReconcileProvisionsAReleaseOnTheSimulatedGitHub is -reconcile
// with -github-sim: a release created through the store leaves
// Provisioning on the loop's own, with its latest branch on the
// simulated GitHub, and nothing calls SyncReleases by hand.
func TestDemoReconcileProvisionsAReleaseOnTheSimulatedGitHub(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("the simulated GitHub needs git")
	}
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg, bare, err := configureGitHubSimDemo(dir, ui.Config{}, repo)
	if err != nil {
		t.Fatalf("configureGitHubSimDemo: %v", err)
	}
	releases, ok := cfg.GitHubBranches.(github.Client)
	if !ok {
		t.Fatalf("-github-sim wired a %T, which cannot sync releases", cfg.GitHubBranches)
	}
	r, err := store.CreateRelease(ctx, repo, "walk", time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		runDemoReconcile(ctx, store, releases, 10*time.Millisecond)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, err := store.GetRelease(ctx, repo, r.Name)
		if err != nil {
			t.Fatalf("GetRelease: %v", err)
		}
		if got.Status != model.ReleaseProvisioning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("release still provisioning after 10s of -reconcile (last error %q)", got.LastError)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := demoGit(bare, "rev-parse", "--verify", "refs/heads/"+r.LatestBranch()); err != nil {
		t.Errorf("the simulated GitHub has no %s: %v", r.LatestBranch(), err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("runDemoReconcile did not return once its context was done")
	}
}

// TestDemoReconcileCycleWithoutGitHubSim is -reconcile alone: suites
// still sync, and a release is left where it is rather than synced
// against a GitHub the demo does not have.
func TestDemoReconcileCycleWithoutGitHubSim(t *testing.T) {
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if _, err := store.CreateRelease(ctx, repo, "walk", time.Now().UTC()); err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	if err := demoReconcileCycle(ctx, store, nil, time.Now().UTC()); err != nil {
		t.Fatalf("demoReconcileCycle: %v", err)
	}
	got, err := store.GetRelease(ctx, repo, "walk")
	if err != nil {
		t.Fatalf("GetRelease: %v", err)
	}
	if got.Status != model.ReleaseProvisioning || got.LastError != "" {
		t.Errorf("release is %s (error %q), want provisioning with no error", got.Status, got.LastError)
	}
}
