package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/daemon"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/staterepo"
	"github.com/bwsalmon/grain/pkg/ui"
)

func TestConfigPullRequestStaysInsideTheController(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDir := t.TempDir()
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: daemon.StateRepoDir(dataDir)})
	if err != nil {
		t.Fatalf("opening config repository: %v", err)
	}
	dir := repo.Dir()
	runLive(t, dir, "git", "commit", "--allow-empty", "-m", "Initial config")

	store := testStore(t)
	seedReconcileTask(t, store, "6", "grain", "config")
	task, err := store.GetTask(ctx, "6")
	if err != nil || task == nil {
		t.Fatalf("reading task: %+v, %v", task, err)
	}

	runLive(t, dir, "git", "checkout", "-b", task.BranchName())
	if err := os.WriteFile(filepath.Join(dir, "setting.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runLive(t, dir, "git", "add", "setting.txt")
	runLive(t, dir, "git", "commit", "-m", "Change the config", "-m", "Keep the change on the controller-owned repository.")
	runLive(t, dir, "git", "checkout", repo.Branch())

	manager := daemon.NewStateManager(dataDir, nil, repo, daemon.OpenSecrets(dataDir), nil, nil)
	external := github.NewClient(github.NewFakeTransport(), nil)
	client := &daemon.StateRepoPullRequestClient{Client: external, Manager: manager, Store: store}

	commits, err := client.CompareCommits("grain", "config", repo.Branch(), task.BranchName())
	if err != nil || len(commits) != 1 || commits[0].Message == "" {
		t.Fatalf("CompareCommits = %+v, %v", commits, err)
	}
	now := time.Now().UTC()
	if err := orchestrator.ProcessResult(ctx, store, client, *task, &agent.Result{}, "unused", now); err != nil {
		t.Fatalf("finishing config task: %v", err)
	}
	finished, err := store.GetTask(ctx, task.ID)
	if err != nil || finished == nil {
		t.Fatalf("reading finished task: %+v, %v", finished, err)
	}
	wantLink := model.PullRequestRef{Repo: model.ConfigRepo(), Number: 6}.String()
	foundLink := false
	for _, link := range finished.Links {
		foundLink = foundLink || link.Kind == model.LinkFixes && link.Target == wantLink
	}
	if !foundLink {
		t.Fatalf("finished task links = %+v, want %s", finished.Links, wantLink)
	}
	detail, err := client.GetPullRequest("grain", "config", 6)
	if err != nil {
		t.Fatalf("GetPullRequest before merge: %v", err)
	}
	if detail.State != "open" || detail.Mergeable == nil || !*detail.Mergeable {
		t.Fatalf("open detail = %+v, want a clean internal pull request", detail)
	}

	// Submit through the same model operation the browser calls, then let the
	// real merge queue observe and land the native pull request. Calling the
	// adapter's MergePullRequest directly would leave the user-facing half of
	// the lifecycle untested.
	if err := ui.NewClient(ui.Config{}, store).Submit(ctx, task.ID); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, now.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	detail, err = client.GetPullRequest("grain", "config", 6)
	if err != nil {
		t.Fatalf("GetPullRequest after merge: %v", err)
	}
	if detail.State != "closed" || !detail.Merged {
		t.Fatalf("merged detail = %+v, want closed and merged", detail)
	}
}
