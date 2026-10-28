package e2e

import (
	"os"
	"path/filepath"
	"testing"
)

// initBareRepo creates the bare repository at path -- the upstream every
// test in this package that pushes for real pushes to -- on branch, with
// its parent directories, and returns the path so it reads as one line
// at a call site.
//
// The two config settings are what keep a push from outliving the test
// that made it: git runs auto-maintenance on the receiving side of a
// push, and since v2.47 it detaches, so the repacking goes on in the
// background while t.TempDir deletes the tree underneath it, and the
// deletion fails on files that land after RemoveAll has walked past them
// ("TempDir RemoveAll cleanup: unlinkat ...: directory not empty").
// Saying autoDetach=false does not turn the maintenance off, it runs it
// in the foreground, so the push that triggered it is what waits for it.
// pkg/cli/bare_remote_test.go's initBareRepo has the whole
// explanation, including why both keys are set and why nothing on a
// runner with git 2.39 can see any of this; this is that helper
// restated, because this codebase duplicates a small test helper across
// packages rather than sharing one (harness_test.go's own comment on
// why).
func initBareRepo(t *testing.T, path, branch string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating the bare repository's parent directory: %v", err)
	}
	run(t, filepath.Dir(path), "git", "init", "--bare", "-q", "-b", branch, path)
	run(t, path, "git", "config", "maintenance.autoDetach", "false")
	run(t, path, "git", "config", "gc.autoDetach", "false")
	return path
}
