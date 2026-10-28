package deploy

// scripts/export-source.sh, run rather than read.
//
// It decides what a deployment carries as "the source this binary was
// built from" -- the tree read_grain_source and list_grain_source hand an
// agent, staged into the image by the Dockerfile and onto a host by
// setup.sh. It exists because the rule used to be a denylist: copy the
// whole build context, then delete .git, bin/ and ui/node_modules. A
// release host's checkout still held the tree from before the Go module
// was promoted out of its old subdirectory, untracked and so untouched by
// every checkout since, and none of the three names matched a single
// thing in it -- so every published image carried 3.1GB of that
// directory's module and build caches, which every deployment pulled and
// which host mode copied back out of the image on every daemon start.
//
// What the script does about that can only be asserted by running it: the
// question is what a dirty directory produces, and reading the file
// cannot tell you.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// exportSource runs the script against src, staging into a fresh
// directory it returns along with what the script said and whether it
// succeeded.
func exportSource(t *testing.T, src string, env ...string) (string, string, error) {
	t.Helper()
	dest := filepath.Join(t.TempDir(), "export")
	cmd := exec.Command(filepath.Join(repoRoot(t), "scripts", "export-source.sh"), src, dest)
	cmd.Env = append(os.Environ(), env...)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return dest, out.String(), err
}

// write creates a file under dir, with its parents, of the given size in
// bytes (its content beyond the first byte is uninteresting).
func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// staged lists every file under dir, relative to it, sorted.
func staged(t *testing.T, dir string) []string {
	t.Helper()
	var found []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		found = append(found, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	sort.Strings(found)
	return found
}

func contain(list []string, want string) bool {
	for _, got := range list {
		if got == want {
			return true
		}
	}
	return false
}

// A checkout with the repository's own source in it and a build host's
// leavings beside it exports the first and none of the second.
//
// The untracked paths here are the shape of the ones that actually
// shipped: a cache and a built binary under a directory from a layout
// this repository does not have any more, which is exactly what no
// hand-written exclusion list was ever going to name.
func TestTheExportCarriesTrackedSourceAndNothingElse(t *testing.T) {
	src := t.TempDir()
	gitIn(t, src, "init", "-q", ".")
	write(t, src, "go.mod", "module github.com/bwsalmon/grain\n")
	write(t, src, "cmd/grain/main.go", "package main\n")
	gitIn(t, src, "add", ".")
	gitIn(t, src, "commit", "-q", "-m", "source")

	// None of these is tracked, and none of them is called .git, bin or
	// ui/node_modules at the root -- which is the whole of what the
	// previous rule knew how to remove.
	write(t, src, "stale-layout/.container-cache/mod/cache/download/blob", "a module cache")
	write(t, src, "stale-layout/bin/grain", "a binary from a previous life")
	write(t, src, "stale-layout/pkg/ui/frontend/node_modules/esbuild/bin", "a package manager's tree")
	write(t, src, "scripts/granule/output/disk.img", "a guest disk")

	dest, out, err := exportSource(t, src)
	if err != nil {
		t.Fatalf("export-source.sh: %v\n%s", err, out)
	}

	got := staged(t, dest)
	for _, want := range []string{"go.mod", filepath.Join("cmd", "grain", "main.go")} {
		if !contain(got, want) {
			t.Errorf("%s was not exported: %v", want, got)
		}
	}
	for _, unwanted := range got {
		if strings.HasPrefix(unwanted, "stale-layout") || strings.Contains(unwanted, "output") {
			t.Errorf("%s is untracked and was exported anyway", unwanted)
		}
	}
	// .git is the one the old rule did remove, and the one whose return
	// would be invisible except in the image's size: with no history
	// asked for, none goes.
	nohist, out, err := exportSource(t, src, "GRAIN_SOURCE_HISTORY_DEPTH=0")
	if err != nil {
		t.Fatalf("export-source.sh: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(nohist, ".git")); err == nil {
		t.Error("the repository's own history was exported with GRAIN_SOURCE_HISTORY_DEPTH=0")
	}
}

// The history baked beside the tree is a shallow repository of its own,
// not the checkout's .git: HEAD detached at the commit built, the number
// of commits asked for, no refs, and a config written from scratch -- so
// a credential in the building checkout's remote cannot ship.
func TestTheExportBakesAShallowHistoryOfItsOwn(t *testing.T) {
	src := t.TempDir()
	gitIn(t, src, "init", "-q", ".")
	for i := 0; i < 5; i++ {
		write(t, src, "go.mod", fmt.Sprintf("module github.com/bwsalmon/grain // %d\n", i))
		gitIn(t, src, "add", ".")
		date := fmt.Sprintf("@%d +0000", 1700000000+i)
		cmd := exec.Command("git", "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", fmt.Sprint("commit ", i))
		cmd.Dir, cmd.Env = src, append(os.Environ(), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git commit: %v\n%s", err, out)
		}
	}
	gitIn(t, src, "remote", "add", "origin", "https://x-access-token:ghs_secret@github.com/bwsalmon/grain.git")
	gitIn(t, src, "branch", "other")
	gitIn(t, src, "tag", "v1")

	dest, out, err := exportSource(t, src, "GRAIN_SOURCE_HISTORY_DEPTH=3")
	if err != nil {
		t.Fatalf("export-source.sh: %v\n%s", err, out)
	}
	head := strings.TrimSpace(gitOut(t, src, "rev-parse", "HEAD"))
	if got := strings.TrimSpace(gitOut(t, dest, "log", "-1", "--format=%H")); got != head {
		t.Errorf("baked HEAD = %s, want the checkout's %s", got, head)
	}
	if got := strings.TrimSpace(gitOut(t, dest, "rev-list", "--count", "HEAD")); got != "3" {
		t.Errorf("baked %s commits, want 3", got)
	}
	if got := strings.TrimSpace(gitOut(t, dest, "for-each-ref")); got != "" {
		t.Errorf("baked refs %q, want none", got)
	}
	config, err := os.ReadFile(filepath.Join(dest, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), "ghs_secret") {
		t.Errorf("the checkout's credential was baked into .git/config:\n%s", config)
	}
	if marker, err := os.ReadFile(filepath.Join(dest, ".git", "grain-baked-history")); err != nil || strings.TrimSpace(string(marker)) != head+" 3" {
		t.Errorf("grain-baked-history = %q (%v), want %q", marker, err, head+" 3")
	}
	if got := strings.TrimSpace(gitOut(t, dest, "status", "--porcelain")); got != "" {
		t.Errorf("git status in the export = %q, want a clean tree", got)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return string(out)
}

// The working tree, not HEAD.
//
// The tree has to be the source the binary beside it was built from, and
// what `make build` compiled is what was on disk -- so an uncommitted
// edit belongs in the export, and a file staged for deletion does not
// stop the build over an index entry with nothing behind it.
func TestTheExportIsTheWorkingTreeRatherThanTheCommit(t *testing.T) {
	src := t.TempDir()
	gitIn(t, src, "init", "-q", ".")
	write(t, src, "go.mod", "module github.com/bwsalmon/grain\n")
	write(t, src, "pkg/version/version.go", "package version // committed\n")
	write(t, src, "docs/gone.md", "removed since\n")
	gitIn(t, src, "add", ".")
	gitIn(t, src, "commit", "-q", "-m", "source")

	write(t, src, "pkg/version/version.go", "package version // edited, not committed\n")
	gitIn(t, src, "rm", "-q", "--cached", "docs/gone.md")
	if err := os.Remove(filepath.Join(src, "docs", "gone.md")); err != nil {
		t.Fatalf("removing the staged deletion's file: %v", err)
	}

	dest, out, err := exportSource(t, src)
	if err != nil {
		t.Fatalf("export-source.sh: %v\n%s", err, out)
	}

	edited, err := os.ReadFile(filepath.Join(dest, "pkg", "version", "version.go"))
	if err != nil {
		t.Fatalf("reading the exported file: %v", err)
	}
	if !strings.Contains(string(edited), "edited, not committed") {
		t.Errorf("the export took the commit's content, not the tree's: %q", edited)
	}
}

// A directory git will not talk about still builds.
//
// An export, a tarball fed to `docker build`, a linked worktree whose
// gitdir is outside the context: scripts/buildvcs.sh's own list. None of
// them is a reason to refuse to build grain, so the copy that was here
// before is what happens -- loudly, because what it stages is no longer a
// list anybody chose.
func TestADirectoryWithNoRepositoryFallsBackToACopyAndSaysSo(t *testing.T) {
	src := t.TempDir()
	write(t, src, "go.mod", "module github.com/bwsalmon/grain\n")
	write(t, src, "bin/grain", "a build output")
	write(t, src, "ui/node_modules/vite/index.js", "a package manager's tree")

	dest, out, err := exportSource(t, src)
	if err != nil {
		t.Fatalf("export-source.sh: %v\n%s", err, out)
	}
	if !strings.Contains(out, "not a checkout git can enumerate") {
		t.Errorf("the fallback was taken silently: %s", out)
	}

	got := staged(t, dest)
	if !contain(got, "go.mod") {
		t.Errorf("the source was not exported at all: %v", got)
	}
	for _, unwanted := range got {
		if strings.HasPrefix(unwanted, "bin") || strings.Contains(unwanted, "node_modules") {
			t.Errorf("%s survived the fallback's own exclusions", unwanted)
		}
	}
}

// The ceiling, which is the backstop under that fallback.
//
// tests/container asks the same question of the finished image, but the
// release path (scripts/release-images.sh, from a Grain CI release job)
// builds and pushes without running it -- and that is the path that
// published the 5.8GB image. Failing the build is what makes the answer
// arrive before a deployment's start timeout does.
func TestAnOversizedExportFailsTheBuildAndNamesWhatIsInIt(t *testing.T) {
	src := t.TempDir()
	write(t, src, "go.mod", "module github.com/bwsalmon/grain\n")
	write(t, src, "cache/blob", strings.Repeat("x", 3<<20))

	_, out, err := exportSource(t, src, "GRAIN_SOURCE_EXPORT_MAX_MB=1")
	if err == nil {
		t.Fatalf("a 3MB export passed a 1MB ceiling:\n%s", out)
	}
	if !strings.Contains(out, "over the 1MB ceiling") {
		t.Errorf("the failure does not say what the ceiling was: %s", out)
	}
	// An operator staring at a failed release needs the name of the thing
	// that is too big, not just the total.
	if !strings.Contains(out, "cache") {
		t.Errorf("the failure does not name the largest thing in the export: %s", out)
	}
}

// unreadableByOthers walks a staged tree and returns every path an
// account other than its owner could not use: a directory without o+x
// (which denies everything below it, not just itself) or a file without
// o+r. Symlinks carry no useful mode of their own and are skipped.
func unreadableByOthers(t *testing.T, dir string) []string {
	t.Helper()
	var denied []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		mode := info.Mode()
		switch {
		case mode&os.ModeSymlink != 0:
			return nil
		case info.IsDir() && mode.Perm()&0o005 != 0o005:
			denied = append(denied, path+" (dir, "+mode.Perm().String()+")")
		case !info.IsDir() && mode.Perm()&0o004 == 0:
			denied = append(denied, path+" (file, "+mode.Perm().String()+")")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	return denied
}

// The staged tree is readable by an account that is not the one that
// staged it -- which is the only account that ever reads it.
//
// This is grain/task-160 and grain/task-250, and it is the assertion those
// two needed and nobody had written. A deployment's daemon opens this tree
// as somebody else: in container mode it is a `docker run --user <host
// uid>:<gid>` against an image whose files belong to root, and in host mode
// it is the unprivileged service account reading a tree root installed. So
// a mode that is merely fine for the builder is a self-debug capability
// that answers "permission denied" for every path an agent asks for.
//
// What made it possible for months is that every mode in the staged copy
// came from somewhere else: the tar copies each file's mode out of the
// build host's checkout verbatim, the fallback `cp -a`s the directories'
// modes too, and only the root is created here with a mode of its own. A
// checkout like the one below -- private files, private directories, which
// is what a clone made under a umask of 0077 produces -- therefore exported
// a tree whose root listed and whose every path denied, and the symptom
// reached an agent as a per-path error that reads like a bad path.
//
// Asserted as mode bits rather than by dropping privileges: this test
// process cannot become another account, and the bits are what the kernel
// will decide on anyway.
func TestTheExportIsReadableByAnAccountThatDidNotBuildIt(t *testing.T) {
	src := t.TempDir()
	gitIn(t, src, "init", "-q", ".")
	write(t, src, "go.mod", "module github.com/bwsalmon/grain\n")
	write(t, src, "pkg/capability/selfdebug/selfdebug.go", "package selfdebug\n")
	write(t, src, "docs/agent-ergonomics.md", "# ergonomics\n")
	write(t, src, "scripts/setup.sh", "#!/usr/bin/env bash\n")
	if err := os.Chmod(filepath.Join(src, "scripts", "setup.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, src, "add", ".")
	gitIn(t, src, "commit", "-q", "-m", "source")

	// Private after the commit, so the index still holds the file list
	// while the working tree is what a tight umask would have left.
	private := []string{"go.mod", "docs/agent-ergonomics.md", "pkg/capability/selfdebug/selfdebug.go"}
	for _, rel := range private {
		if err := os.Chmod(filepath.Join(src, filepath.FromSlash(rel)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, rel := range []string{"pkg", "pkg/capability", "pkg/capability/selfdebug", "docs", "scripts"} {
		if err := os.Chmod(filepath.Join(src, filepath.FromSlash(rel)), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	dest, out, err := exportSource(t, src)
	if err != nil {
		t.Fatalf("export-source.sh: %v\n%s", err, out)
	}
	if denied := unreadableByOthers(t, dest); len(denied) > 0 {
		t.Errorf("the staged source has %d path(s) no other account can read, so read_grain_source "+
			"would answer \"permission denied\" for them:\n%s", len(denied), strings.Join(denied, "\n"))
	}
	// And the one mode bit that is worth keeping still is: git records the
	// exec bit, and this tree's scripts are read by people who run them.
	script, err := os.Stat(filepath.Join(dest, "scripts", "setup.sh"))
	if err != nil {
		t.Fatalf("stat on the staged script: %v", err)
	}
	if script.Mode().Perm()&0o111 != 0o111 {
		t.Errorf("scripts/setup.sh was staged as %v; the export flattened the exec bit git records",
			script.Mode().Perm())
	}
}

// ...and so is the copy the fallback makes, which is where a host
// checkout's own modes travel most directly.
//
// setup.sh's GRAIN_HOST_SOURCE_DIR install stages a named checkout through
// this same script, and a directory git will not talk about takes the
// `cp -a` branch -- which preserves every mode in it, including the ones
// that make the tree unreadable to the account the daemon runs as.
func TestTheFallbackCopyIsReadableToo(t *testing.T) {
	src := t.TempDir()
	write(t, src, "go.mod", "module github.com/bwsalmon/grain\n")
	write(t, src, "pkg/orchestrator/run.go", "package orchestrator\n")
	if err := os.Chmod(filepath.Join(src, "pkg", "orchestrator"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(src, "pkg", "orchestrator", "run.go"), 0o600); err != nil {
		t.Fatal(err)
	}

	dest, out, err := exportSource(t, src)
	if err != nil {
		t.Fatalf("export-source.sh: %v\n%s", err, out)
	}
	if !strings.Contains(out, "not a checkout git can enumerate") {
		t.Fatalf("this was meant to exercise the fallback and did not:\n%s", out)
	}
	if denied := unreadableByOthers(t, dest); len(denied) > 0 {
		t.Errorf("the fallback copy carried the checkout's own private modes:\n%s", strings.Join(denied, "\n"))
	}
}

// Both callers run it by path, out of a checkout git cloned.
func TestTheExportScriptIsExecutable(t *testing.T) {
	executable(t, "scripts", "export-source.sh")
}
