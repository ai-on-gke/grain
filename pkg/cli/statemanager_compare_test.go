package cli

// The config repository's own pull-request view (stateManager.Compare),
// which is the only pull request a grain deployment owns end to end: a
// change to its own settings never reaches GitHub, so this diff is the
// whole of what a reviewer -- or anybody reading back what a finished
// task did -- has to look at.
//
// What these two tests are about is that the second of those readers
// exists. A comparison between two branch tips goes empty the moment the
// branch lands, because the merge base of a base that contains head is
// head itself, and grain/task-375 is that going-empty: the page somebody
// opens from a completed task's own link said nothing at all.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// configRepoWithBranch is a config repository holding one commit on its
// default branch and one task branch carrying one change, left checked
// out on the default branch the way the controller keeps it.
func configRepoWithBranch(t *testing.T, branch string) (*daemon.StateManager, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dataDir := t.TempDir()
	repo, err := staterepo.Open(context.Background(), staterepo.Config{Dir: daemon.StateRepoDir(dataDir)})
	if err != nil {
		t.Fatalf("opening the config repository: %v", err)
	}
	dir := repo.Dir()
	runLive(t, dir, "git", "commit", "--allow-empty", "-m", "Initial config")
	runLive(t, dir, "git", "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(dir, "setting.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runLive(t, dir, "git", "add", "setting.txt")
	runLive(t, dir, "git", "commit", "-m", "Change the setting")
	runLive(t, dir, "git", "checkout", repo.Branch())
	return daemon.NewStateManager(dataDir, nil, repo, daemon.OpenSecrets(dataDir), nil, nil), dir
}

func TestConfigComparisonShowsAnOpenBranchAgainstItsMergeBase(t *testing.T) {
	t.Parallel()
	manager, dir := configRepoWithBranch(t, "grain/task-9")
	// Base moves on under the branch, as it does while a task is in
	// flight: what the branch changed is still only its own commit.
	runLive(t, dir, "git", "commit", "--allow-empty", "-m", "Somebody else's change")

	got, err := manager.Compare(context.Background(), model.ConfigRepo(), "", "grain/task-9")
	if err != nil {
		t.Fatalf("comparing an open branch: %v", err)
	}
	if got.Merged {
		t.Errorf("comparison reports a merged branch: %+v", got)
	}
	if got.MergeBase == "" {
		t.Errorf("comparison names no merge base: %+v", got)
	}
	if !strings.Contains(got.Diff, "setting.txt") || !strings.Contains(got.Diff, "+changed") {
		t.Errorf("diff = %q, want the branch's own change", got.Diff)
	}
}

func TestConfigComparisonStillShowsTheChangeAfterItHasMerged(t *testing.T) {
	t.Parallel()
	manager, dir := configRepoWithBranch(t, "grain/task-10")
	// The merge queue's own merge (stateRepoPullRequestClient.
	// MergePullRequest), and the base moving on afterwards.
	runLive(t, dir, "git", "merge", "--no-ff", "--no-edit", "grain/task-10")
	runLive(t, dir, "git", "commit", "--allow-empty", "-m", "A later change")

	got, err := manager.Compare(context.Background(), model.ConfigRepo(), "", "grain/task-10")
	if err != nil {
		t.Fatalf("comparing a merged branch: %v", err)
	}
	if !got.Merged {
		t.Errorf("comparison does not report the branch as merged: %+v", got)
	}
	if !strings.Contains(got.Diff, "setting.txt") || !strings.Contains(got.Diff, "+changed") {
		t.Fatalf("diff = %q, want the change the merged branch made", got.Diff)
	}
	// The fork point, not head's own tip: the latter is what git answers
	// for the merge base of a branch its base contains, and diffing from
	// it is what made this view blank.
	head, err := exec.Command("git", "-C", dir, "rev-parse", "refs/heads/grain/task-10").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got.MergeBase == strings.TrimSpace(string(head)) {
		t.Errorf("merge base = %q, which is head's own tip", got.MergeBase)
	}
}
