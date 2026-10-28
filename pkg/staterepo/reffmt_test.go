package staterepo_test

// The state repository's ref format (pkg/gitrefs, task 5195): a reftable
// default in the user's git config does not reach a repository grain
// creates, and a reftable repository the daemon's git cannot open is
// refused with a message that says how to convert it.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/gitrefs"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// reftableByDefault points HOME at a global config asking for reftable,
// the way a Cloudtop's newer git is set up, and keeps the system config
// out of it.
func reftableByDefault(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[init]\n\tdefaultRefFormat = reftable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	// What gitrefs.Pin sets, unset here so each test decides.
	t.Setenv(gitrefs.EnvVar, "")
	os.Unsetenv(gitrefs.EnvVar)
}

// gitMakesReftable reports whether the git on PATH honours that config,
// which is what makes the first test below a test rather than a formality:
// bookworm's 2.39 knows no reftable and makes files whatever it is told.
func gitMakesReftable(t *testing.T) bool {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	format, err := gitrefs.Format(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return format == "reftable"
}

func TestTheStateRepositoryIsFilesFormatEvenWithAReftableUserDefault(t *testing.T) {
	reftableByDefault(t)
	if gitMakesReftable(t) {
		t.Log("this git makes reftable repositories from the user's default; the pin has to override it")
	} else {
		v, _ := exec.Command("git", "--version").Output()
		t.Logf("%s makes files repositories whatever it is told; checking the pin is harmless to it", strings.TrimSpace(string(v)))
	}
	gitrefs.Pin()
	if got := os.Getenv(gitrefs.EnvVar); got != gitrefs.Files {
		t.Fatalf("after Pin, %s=%q", gitrefs.EnvVar, got)
	}

	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "state-repo")
	if _, err := staterepo.Open(ctx, staterepo.Config{Dir: dir}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if format, err := gitrefs.Format(ctx, dir); err != nil || format != "" {
		t.Fatalf("the state repository's extensions.refstorage is %q (%v), want none (files)", format, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "reftable")); err == nil {
		t.Fatal("the state repository has a reftable directory")
	}

	// A clone takes the same default, and is what a remote-backed state
	// repository starts as.
	remote := filepath.Join(t.TempDir(), "remote.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}
	cloned := filepath.Join(t.TempDir(), "cloned")
	if _, err := staterepo.Open(ctx, staterepo.Config{Dir: cloned, Remote: remote}); err != nil {
		t.Fatalf("Open with a remote: %v", err)
	}
	if format, err := gitrefs.Format(ctx, cloned); err != nil || format != "" {
		t.Fatalf("the cloned state repository's extensions.refstorage is %q (%v), want none (files)", format, err)
	}
}

func TestOpeningAStateRepositoryThisGitCannotReadSaysHowToConvertIt(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "state-repo")
	if _, err := staterepo.Open(ctx, staterepo.Config{Dir: dir}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	// What a newer host git leaves behind, as far as an older git can
	// tell: a format-1 repository naming the reftable backend.
	for _, kv := range [][2]string{{"core.repositoryformatversion", "1"}, {"extensions.refstorage", "reftable"}} {
		if out, err := exec.Command("git", "-C", dir, "config", kv[0], kv[1]).CombinedOutput(); err != nil {
			t.Fatalf("git config %s: %v: %s", kv[0], err, out)
		}
	}
	probe := exec.Command("git", "rev-parse", "--git-dir")
	probe.Dir = dir
	if probe.Run() == nil {
		t.Skip("this git opens the fixture (it reads reftable); the message is for a git that cannot")
	}

	_, err := staterepo.Open(ctx, staterepo.Config{Dir: dir})
	if err == nil {
		t.Fatal("Open accepted a repository its git cannot read")
	}
	var unreadable *gitrefs.UnreadableError
	if !errors.As(err, &unreadable) {
		t.Fatalf("Open's error is not a gitrefs.UnreadableError: %v", err)
	}
	msg := err.Error()
	for _, want := range []string{
		dir,
		`"reftable" ref format`,
		"git -C " + dir + " refs migrate --ref-format=files",
		"git 2.46 or newer",
		"Stop the daemon",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the error does not say %q:\n%s", want, msg)
		}
	}
	if strings.HasPrefix(msg, "git config") || strings.Contains(msg, "user.name") {
		t.Errorf("the error is git's own, not the explanation:\n%s", msg)
	}
}
