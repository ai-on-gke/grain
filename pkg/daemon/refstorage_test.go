package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// reftableRepo makes dir a repository whose config says its refs are in
// reftable. With a git that can, it is a real one (--ref-format=reftable);
// with one that cannot -- Debian 12's, the image's own -- the config is
// written by hand, which is all an old git ever reads of such a
// repository before it refuses it.
func reftableRepo(t *testing.T, dir string, real bool) {
	t.Helper()
	if real {
		if out, err := exec.Command("git", "init", "--quiet", "--ref-format=reftable", dir).CombinedOutput(); err != nil {
			t.Skipf("this git cannot make a reftable repository: %v: %s", err, out)
		}
		return
	}
	if out, err := exec.Command("git", "init", "--quiet", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	config := filepath.Join(dir, ".git", "config")
	b, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(b), "repositoryformatversion = 0", "repositoryformatversion = 1", 1)
	text += "[extensions]\n\trefStorage = reftable\n"
	if err := os.WriteFile(config, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOpenStateRepoRefusesAReftableRepositoryActionably(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for name, real := range map[string]bool{"real": true, "as an old git sees it": false} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dataDir := t.TempDir()
			dir := StateRepoDir(dataDir)
			reftableRepo(t, dir, real)
			_, err := OpenStateRepo(context.Background(), dataDir)
			if err == nil {
				t.Fatal("OpenStateRepo opened a reftable repository; want it refused")
			}
			msg := err.Error()
			for _, want := range []string{
				dir,
				"extensions.refstorage = reftable",
				"git -C " + dir + " refs migrate --ref-format=files",
				"scripts/setup.sh",
				"git version",
			} {
				if !strings.Contains(msg, want) {
					t.Errorf("error does not say %q:\n%s", want, msg)
				}
			}
			if strings.Contains(msg, "not in a git directory") {
				t.Errorf("error is git's own rather than one that says what to do:\n%s", msg)
			}
		})
	}
}

func TestCheckRefStorageAcceptsFilesAndNonRepositories(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if err := checkRefStorage(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Errorf("a missing directory: %v", err)
	}
	plain := t.TempDir()
	if out, err := exec.Command("git", "init", "--quiet", plain).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if err := checkRefStorage(plain); err != nil {
		t.Errorf("a plain repository: %v", err)
	}
	files := t.TempDir()
	if err := os.MkdirAll(filepath.Join(files, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(files, ".git", "config"),
		[]byte("[core]\n\trepositoryformatversion = 1\n[extensions]\n\trefstorage = files\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkRefStorage(files); err != nil {
		t.Errorf("an explicit files repository: %v", err)
	}
}

// A linked worktree's .git is a file, and the extension is in the common
// directory's config.
func TestCheckRefStorageFollowsAGitdirFile(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	main := filepath.Join(root, "main")
	reftableRepo(t, main, false)
	wt := filepath.Join(root, "wt")
	gitDir := filepath.Join(main, ".git", "worktrees", "wt")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkRefStorage(wt); err == nil {
		t.Error("a worktree of a reftable repository was not refused")
	}
}

// A state repository this build creates is a files one even under a git
// whose default is reftable. Only a git new enough to have the setting
// can tell the difference; an older one passes trivially.
func TestOpenStateRepoCreatesAFilesRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_DEFAULT_REF_FORMAT", "reftable")
	dataDir := t.TempDir()
	if _, err := OpenStateRepo(context.Background(), dataDir); err != nil {
		t.Fatalf("OpenStateRepo: %v", err)
	}
	if got := refStorage(StateRepoDir(dataDir)); got != "" && got != "files" {
		t.Errorf("a fresh state repository has refstorage %q, want files", got)
	}
}
