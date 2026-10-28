package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// initBareRepo creates the bare repository at path -- the remote every
// test in this package that pushes for real pushes to -- on main, with
// its parent directories, and returns the path so it reads as one line
// at a call site.
//
// The two config settings are what keep a push from outliving the test
// that made it. git runs its own auto-maintenance on the receiving side
// of a push, and since v2.47 it does that by detaching: receive-pack
// spawns `git maintenance run --auto --detach`, which forks and lets the
// push return while it goes on repacking the repository in the
// background. That background process is still writing into the bare
// repository when the test returns and t.TempDir deletes the tree
// underneath it, and the deletion fails on files that land after
// RemoveAll has walked past them:
//
//	TempDir RemoveAll cleanup: unlinkat
//	/tmp/TestDaemonPushesItsDatabase.../002/state.git: directory not empty
//
// Saying autoDetach=false does not turn the maintenance off, it runs it
// in the foreground, so the push that triggered it is what waits for it
// -- the same choice staterepo.Repo.configure makes for grain's own
// working tree, and pkg/staterepo's tests make for their own fixture.
// Both keys are set because gc.autoDetach is only honoured here as a
// fallback for a repository that says nothing about
// maintenance.autoDetach, which git's own source calls "somewhat weird"
// and keeps for compatibility.
//
// The tests in this package have more to lose by it than most: they push
// from a real daemon, several times over in a single test. Nothing on a
// runner with git 2.39 can see any of this -- detaching auto-maintenance
// is newer than that, so the flake only ever appeared on CI.
func initBareRepo(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating the remote's parent directory: %v", err)
	}
	if out, err := exec.Command("git", "init", "--bare", "-q", "--initial-branch=main", path).CombinedOutput(); err != nil {
		t.Fatalf("creating the bare remote: %v: %s", err, out)
	}
	for _, kv := range [][2]string{
		{"maintenance.autoDetach", "false"},
		{"gc.autoDetach", "false"},
	} {
		cmd := exec.Command("git", "config", kv[0], kv[1])
		cmd.Dir = path
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setting %s on the bare remote: %v: %s", kv[0], err, out)
		}
	}
	return path
}
