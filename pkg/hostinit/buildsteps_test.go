package hostinit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- granuleCLICurrent -------------------------------------------------
//
// No real build is needed here: the check is Stat-based, comparing the
// binary's own mtime against every file under third_party/granule.

func writeAt(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestGranuleCLICurrentWhenBinaryIsNewerThanEverySourceFile(t *testing.T) {
	t.Parallel()
	repoRoot := t.TempDir()
	older := time.Now().Add(-time.Hour)
	newer := time.Now()
	writeAt(t, filepath.Join(repoRoot, "third_party", "granule", "cmd", "granule", "main.go"), older)
	writeAt(t, filepath.Join(repoRoot, "third_party", "granule", "go.mod"), older)
	binPath := filepath.Join(t.TempDir(), "granule")
	writeAt(t, binPath, newer)

	if !granuleCLICurrent(repoRoot, binPath) {
		t.Error("granuleCLICurrent = false, want true: the binary is newer than every file it was built from")
	}
}

func TestGranuleCLICurrentFalseWhenASourceFileIsNewer(t *testing.T) {
	t.Parallel()
	repoRoot := t.TempDir()
	older := time.Now().Add(-time.Hour)
	newer := time.Now()
	binPath := filepath.Join(t.TempDir(), "granule")
	writeAt(t, binPath, older)
	writeAt(t, filepath.Join(repoRoot, "third_party", "granule", "cmd", "granule", "main.go"), older)
	// A file touched after the binary was built -- the binary can no
	// longer be the build of this tree.
	writeAt(t, filepath.Join(repoRoot, "third_party", "granule", "pkg", "newer.go"), newer)

	if granuleCLICurrent(repoRoot, binPath) {
		t.Error("granuleCLICurrent = true, want false: a source file is newer than the binary")
	}
}

func TestGranuleCLICurrentFalseWhenBinaryDoesNotExist(t *testing.T) {
	t.Parallel()
	repoRoot := t.TempDir()
	writeAt(t, filepath.Join(repoRoot, "third_party", "granule", "cmd", "granule", "main.go"), time.Now())
	if granuleCLICurrent(repoRoot, filepath.Join(t.TempDir(), "granule")) {
		t.Error("granuleCLICurrent = true for a path with nothing at it")
	}
}

// --- grainBinaryCurrent --------------------------------------------------
//
// Unlike granule, the grain binary gets a real `go build -buildvcs=auto`
// VCS stamp (it is built directly from the main module, not through a
// replace directive into a directory with no VCS of its own), so these
// tests build one for real, against a throwaway one-commit git repo, and
// drive the comparison with a fakeSystem standing in for that repo's
// current `git rev-parse HEAD`/`git status --porcelain`.

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// buildStampedRepo lays out a trivial one-file module in its own git
// repo, commits it, and go-builds it to binPath -- returning the real
// HEAD commit the build stamped in. If dirty, a further uncommitted edit
// is made (and left uncommitted) before the build, so the binary's own
// stamp carries vcs.modified=true.
func buildStampedRepo(t *testing.T, binPath string, dirty bool) (repoRoot, head string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repoRoot = t.TempDir()
	if err := os.WriteFile(filepath.Join(repoRoot, "go.mod"), []byte("module example.com/x\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoRoot, "init", "-q")
	runGit(t, repoRoot, "add", "-A")
	runGit(t, repoRoot, "commit", "-q", "-m", "initial")
	head = runGit(t, repoRoot, "rev-parse", "HEAD")

	if dirty {
		if err := os.WriteFile(filepath.Join(repoRoot, "main.go"), []byte("package main\n\nfunc main() { _ = 1 }\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cmd := exec.Command("go", "build", "-o", binPath, ".")
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return repoRoot, head
}

func TestGrainBinaryCurrentWhenStampMatchesCleanHead(t *testing.T) {
	t.Parallel()
	binPath := filepath.Join(t.TempDir(), "grain")
	repoRoot, head := buildStampedRepo(t, binPath, false)

	sys := newFakeSystem()
	sys.on(head, nil, "git", "rev-parse", "HEAD")
	sys.on("", nil, "git", "status", "--porcelain")

	if !grainBinaryCurrent(context.Background(), sys, repoRoot, binPath) {
		t.Error("grainBinaryCurrent = false, want true: the binary's stamp matches a clean checkout at the same HEAD")
	}
}

func TestGrainBinaryCurrentFalseWhenHeadMoved(t *testing.T) {
	t.Parallel()
	binPath := filepath.Join(t.TempDir(), "grain")
	repoRoot, _ := buildStampedRepo(t, binPath, false)

	sys := newFakeSystem()
	sys.on("0000000000000000000000000000000000000000", nil, "git", "rev-parse", "HEAD")
	sys.on("", nil, "git", "status", "--porcelain")

	if grainBinaryCurrent(context.Background(), sys, repoRoot, binPath) {
		t.Error("grainBinaryCurrent = true, want false: the checkout has moved to a commit the binary was not built from")
	}
}

func TestGrainBinaryCurrentFalseWhenCheckoutIsDirty(t *testing.T) {
	t.Parallel()
	binPath := filepath.Join(t.TempDir(), "grain")
	repoRoot, head := buildStampedRepo(t, binPath, false)

	sys := newFakeSystem()
	sys.on(head, nil, "git", "rev-parse", "HEAD")
	sys.on(" M main.go", nil, "git", "status", "--porcelain")

	if grainBinaryCurrent(context.Background(), sys, repoRoot, binPath) {
		t.Error("grainBinaryCurrent = true, want false: the checkout has uncommitted changes right now")
	}
}

func TestGrainBinaryCurrentFalseWhenStampIsItselfFromADirtyBuild(t *testing.T) {
	t.Parallel()
	binPath := filepath.Join(t.TempDir(), "grain")
	repoRoot, head := buildStampedRepo(t, binPath, true)

	// Even reporting the checkout as clean at the exact stamped
	// revision must not be enough: the stamp itself says the tree that
	// produced it had edits beyond that revision's own content.
	sys := newFakeSystem()
	sys.on(head, nil, "git", "rev-parse", "HEAD")
	sys.on("", nil, "git", "status", "--porcelain")

	if grainBinaryCurrent(context.Background(), sys, repoRoot, binPath) {
		t.Error("grainBinaryCurrent = true, want false: the binary's own stamp says it was built from a dirty tree")
	}
}

func TestGrainBinaryCurrentFalseWhenPathHasNoStamp(t *testing.T) {
	t.Parallel()
	repoRoot := t.TempDir()
	notABinary := filepath.Join(t.TempDir(), "grain")
	if err := os.WriteFile(notABinary, []byte("not a binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	sys := newFakeSystem()
	if grainBinaryCurrent(context.Background(), sys, repoRoot, notABinary) {
		t.Error("grainBinaryCurrent = true for a path that is not even a Go binary")
	}
}

// --- ensureGranuleCLI / ensureGrainBinary: the get-or-create behavior ---
//
// The property that matters (see the task that added these tests): a
// second Init against a host that already has a current binary must not
// touch that file at all -- not merely report success -- because any
// rewrite of it, changed content or not, is what would drop
// CAP_NET_BIND_SERVICE out from under a daemon already running from it.

func TestEnsureGranuleCLISkipsAndDoesNotTouchAnUpToDateBinary(t *testing.T) {
	t.Parallel()
	opts := baseOptions(t).withDefaults()
	older := time.Now().Add(-time.Hour)
	writeAt(t, filepath.Join(opts.RepoRoot, "third_party", "granule", "cmd", "granule", "main.go"), older)
	writeAt(t, opts.GranuleBinaryPath, time.Now())
	before, err := os.ReadFile(opts.GranuleBinaryPath)
	if err != nil {
		t.Fatal(err)
	}

	sys := newFakeSystem()
	sys.root = true
	var r Result
	ok := ensureGranuleCLI(context.Background(), sys, opts, &r, nil)
	if !ok {
		t.Fatal("ensureGranuleCLI reported failure for an already-current binary")
	}
	step, _ := stepStatus(r, "build the granule CLI")
	if step.Status != StepSkipped {
		t.Errorf("build the granule CLI: got %s, want %s (detail: %s)", step.Status, StepSkipped, step.Detail)
	}
	for _, c := range sys.calls {
		if strings.HasPrefix(c, "go build") {
			t.Errorf("ensureGranuleCLI ran %q against an already-current binary", c)
		}
	}
	after, err := os.ReadFile(opts.GranuleBinaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("ensureGranuleCLI rewrote an already-current binary")
	}
}

func TestEnsureGranuleCLIBuildsWhenSourceIsNewer(t *testing.T) {
	t.Parallel()
	opts := baseOptions(t).withDefaults()
	writeAt(t, opts.GranuleBinaryPath, time.Now().Add(-time.Hour))
	writeAt(t, filepath.Join(opts.RepoRoot, "third_party", "granule", "cmd", "granule", "main.go"), time.Now())

	sys := newFakeSystem()
	sys.root = true
	sys.on("", nil, "go", "build", "-o", opts.GranuleBinaryPath, "github.com/bwsalmon/granule/cmd/granule")
	var r Result
	ok := ensureGranuleCLI(context.Background(), sys, opts, &r, nil)
	if !ok {
		t.Fatal("ensureGranuleCLI reported failure")
	}
	step, _ := stepStatus(r, "build the granule CLI")
	if step.Status != StepDone {
		t.Errorf("build the granule CLI: got %s, want %s", step.Status, StepDone)
	}
}

func TestEnsureGrainBinarySkipsAndDoesNotTouchAnUpToDateBinary(t *testing.T) {
	t.Parallel()
	opts := baseOptions(t).withDefaults()
	opts.GrainBinaryPath = filepath.Join(t.TempDir(), "grain")
	repoRoot, head := buildStampedRepo(t, opts.GrainBinaryPath, false)
	opts.RepoRoot = repoRoot
	before, err := os.ReadFile(opts.GrainBinaryPath)
	if err != nil {
		t.Fatal(err)
	}

	sys := newFakeSystem()
	sys.root = true
	sys.on(head, nil, "git", "rev-parse", "HEAD")
	sys.on("", nil, "git", "status", "--porcelain")

	var r Result
	ok := ensureGrainBinary(context.Background(), sys, opts, &r, nil)
	if !ok {
		t.Fatal("ensureGrainBinary reported failure for an already-current binary")
	}
	step, _ := stepStatus(r, "build grain (frontend + binary)")
	if step.Status != StepSkipped {
		t.Errorf("build grain (frontend + binary): got %s, want %s (detail: %s)", step.Status, StepSkipped, step.Detail)
	}
	for _, c := range sys.calls {
		if strings.HasPrefix(c, "make build") {
			t.Errorf("ensureGrainBinary ran %q against an already-current binary", c)
		}
	}
	after, err := os.ReadFile(opts.GrainBinaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("ensureGrainBinary rewrote an already-current binary")
	}
}

func TestEnsureGrainBinaryRebuildsWhenCheckoutMoved(t *testing.T) {
	t.Parallel()
	opts := baseOptions(t).withDefaults()
	// GrainBinaryPath left at its default (<RepoRoot>/bin/grain), so a
	// successful "make build" needs no copy step.
	sys := newFakeSystem()
	sys.root = true
	// No binary on disk at all yet -- grainBinaryCurrent must say so
	// without needing any git call.
	sys.on("", nil, "make", "build")

	var r Result
	ok := ensureGrainBinary(context.Background(), sys, opts, &r, nil)
	if !ok {
		t.Fatal("ensureGrainBinary reported failure")
	}
	step, _ := stepStatus(r, "build grain (frontend + binary)")
	if step.Status != StepDone {
		t.Errorf("build grain (frontend + binary): got %s, want %s", step.Status, StepDone)
	}
}
