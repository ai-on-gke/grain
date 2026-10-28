package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// repoRoot is the checkout this test file was compiled from, found by
// walking up from its own path rather than from the working directory:
// `go test` runs each package in its own directory, and these assertions
// are all about files relative to the repository root.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file, so cannot locate the repository")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("%s does not look like the repository root: %v", root, err)
	}
	return root
}
