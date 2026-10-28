package treeid

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func run(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

const longDir = "a-directory-name-long-enough-that-the-whole-path-does-not-fit-in-ustar-s-one-hundred-byte-name-field"

// repo makes a repository holding one of everything the archive maps,
// and returns it with its commit.
func repo(t *testing.T, format string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, nil, "init", "-q", "--object-format="+format)
	write := func(p, s string, mode os.FileMode) {
		p = filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", "hello\n", 0o644)
	write("scripts/run.sh", "#!/bin/sh\necho hi\n", 0o755)
	write("a-b", "sorts after a/ in git's order, before it in bytes\n", 0o644)
	write("a/z.txt", "z\n", 0o644)
	write(filepath.Join(longDir, longDir, "deep.txt"), "deep\n", 0o644)
	if err := os.Symlink("README.md", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	run(t, dir, nil, "add", "-A")
	gitlink := strings.Repeat("1", len(run(t, dir, nil, "hash-object", "README.md")))
	run(t, dir, nil, "update-index", "--add", "--cacheinfo", "160000,"+gitlink+",vendor/sub")
	run(t, dir, []string{"GIT_AUTHOR_DATE=2001-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2001-01-01T00:00:00Z"}, "commit", "-q", "-m", "one")
	return dir, run(t, dir, nil, "rev-parse", "HEAD")
}

func TestTheIdentityDependsOnTheTreeAloneNotTheCommitOrTheCheckout(t *testing.T) {
	ctx := context.Background()
	dir, commit := repo(t, "sha1")
	first, err := Compute(ctx, dir, commit, 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := run(t, dir, nil, "rev-parse", "HEAD^{tree}"); first.GitTree != want || len(first.SHA256) != 64 {
		t.Fatalf("Compute = %+v, want git tree %s", first, want)
	}
	// Another commit of the same tree, at another time with another
	// message, and a clone whose files all have fresh mtimes.
	run(t, dir, []string{"GIT_COMMITTER_DATE=2020-02-02T00:00:00Z"}, "commit", "-q", "--allow-empty", "-m", "two")
	clone := filepath.Join(t.TempDir(), "clone")
	run(t, dir, nil, "clone", "-q", dir, clone)
	for _, c := range []struct{ dir, treeish string }{{dir, "HEAD"}, {clone, "HEAD"}, {clone, first.GitTree}} {
		got, err := Compute(ctx, c.dir, c.treeish, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got != first {
			t.Errorf("Compute(%s, %s) = %+v, want %+v", c.dir, c.treeish, got, first)
		}
	}

	// Content, an executable bit and a symlink target each change it.
	if err := os.Chmod(filepath.Join(dir, "README.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, dir, nil, "commit", "-q", "-am", "chmod")
	changed, err := Compute(ctx, dir, "HEAD", 0)
	if err != nil {
		t.Fatal(err)
	}
	if changed.SHA256 == first.SHA256 || changed.GitTree == first.GitTree {
		t.Errorf("a mode change kept the identity %+v", changed)
	}
}

func TestTheCanonicalArchive(t *testing.T) {
	dir, commit := repo(t, "sha1")
	var buf bytes.Buffer
	if err := Archive(context.Background(), dir, commit, &buf); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(&buf)
	var names []string
	got := map[string]*tar.Header{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, strings.TrimSuffix(h.Name, "/"))
		got[strings.TrimSuffix(h.Name, "/")] = h
		if h.ModTime.Unix() != 0 || h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" {
			t.Errorf("%s: mtime %v uid %d gid %d user %q group %q, want all zero", h.Name, h.ModTime, h.Uid, h.Gid, h.Uname, h.Gname)
		}
		if len(h.PAXRecords) > 0 && len(h.Name) <= 100 {
			t.Errorf("%s: pax records %v on a path ustar holds", h.Name, h.PAXRecords)
		}
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("entries are not in path-byte order: %v", names)
	}
	check := func(name string, typ byte, mode int64, link string) {
		t.Helper()
		h := got[name]
		if h == nil {
			t.Errorf("no entry %s in %v", name, names)
			return
		}
		if h.Typeflag != typ || h.Mode != mode || h.Linkname != link {
			t.Errorf("%s: type %c mode %o link %q, want %c %o %q", name, h.Typeflag, h.Mode, h.Linkname, typ, mode, link)
		}
	}
	check("README.md", tar.TypeReg, 0o644, "")
	check("scripts/run.sh", tar.TypeReg, 0o755, "")
	check("scripts", tar.TypeDir, 0o755, "")
	check("link", tar.TypeSymlink, 0o777, "README.md")
	check("vendor/sub", tar.TypeReg, 0o644, strings.Repeat("1", 40))
	check(longDir+"/"+longDir+"/deep.txt", tar.TypeReg, 0o644, "")
	if h := got["vendor/sub"]; h != nil && h.Size != 0 {
		t.Errorf("submodule entry has size %d, want 0", h.Size)
	}
}

func TestALimitAnswersErrTooLarge(t *testing.T) {
	dir, commit := repo(t, "sha1")
	if _, err := Compute(context.Background(), dir, commit, 1024); !errors.Is(err, ErrTooLarge) {
		t.Errorf("Compute with a 1 KiB limit = %v, want ErrTooLarge", err)
	}
	if _, err := Compute(context.Background(), dir, commit, 1<<20); err != nil {
		t.Errorf("Compute with a 1 MiB limit = %v", err)
	}
}

func TestASHA256RepositoryKeepsItsOwnObjectFormat(t *testing.T) {
	dir, commit := repo(t, "sha256")
	id, err := Compute(context.Background(), dir, commit, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(id.GitTree) != 64 {
		t.Errorf("git tree %q in a sha256 repository", id.GitTree)
	}
}

func TestAnOptionLikeTreeishIsRefused(t *testing.T) {
	dir, _ := repo(t, "sha1")
	if _, err := Compute(context.Background(), dir, "--output=/tmp/x", 0); err == nil {
		t.Error("Compute(--output=...) = nil error")
	}
}
