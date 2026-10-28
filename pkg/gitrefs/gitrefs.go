// Package gitrefs keeps the repositories grain makes in git's classic
// "files" ref format, and explains the ones it finds in any other.
//
// A new enough git can be told -- by init.defaultRefFormat in a user's
// global config, or by a feature preset -- to create repositories with
// the reftable backend (extensions.refstorage = reftable). The git in
// grain's release image is Debian bookworm's 2.39, which cannot open such
// a repository at all: every command in one fails with "unknown repository
// extension found: refstorage". A host-mode install's state repository was
// made by the host's git, so a Cloudtop moved to container mode came up
// with a daemon that could not read it (task 5195).
//
// So grain pins the format itself rather than inheriting one. Pin sets
// GIT_DEFAULT_REF_FORMAT=files for the whole process, which every git it
// runs inherits: git 2.45 and newer let that variable override any
// init.defaultRefFormat a user or system config sets (only an explicit
// --ref-format beats it), and an older git, which only ever writes files,
// ignores it. Check is the other half: a repository that is already in
// another format, opened by a git that cannot read it, fails with a
// message saying how to convert it instead of git's own fatal.
package gitrefs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// EnvVar is the variable git reads its default ref format for new
// repositories from.
const EnvVar = "GIT_DEFAULT_REF_FORMAT"

// Files is the classic ref format, the only one every git reads.
const Files = "files"

// EnvEntry is Pin's setting as a NAME=value pair, for a git environment
// that is built from scratch rather than inherited from this process.
func EnvEntry() string { return EnvVar + "=" + Files }

// Pin makes every repository a git started by this process creates use
// the files format, whatever the user's or the system's git config says.
func Pin() {
	_ = os.Setenv(EnvVar, Files)
}

// UnreadableError is a repository in a ref format the git on PATH cannot
// open.
type UnreadableError struct {
	Dir    string // the repository, as it was named to Check
	Format string // its extensions.refstorage
	Git    string // `git --version` of the git that could not read it
	Detail string // what that git said
}

func (e *UnreadableError) Error() string {
	git := e.Git
	if git == "" {
		git = "this git"
	}
	return fmt.Sprintf("the git repository at %s uses the %q ref format (extensions.refstorage = %s), which %s cannot read. "+
		"Stop the daemon and convert it with git 2.46 or newer, as the user that owns it:\n\n"+
		"    git -C %s refs migrate --ref-format=files\n\n"+
		"then start the daemon again. (git 2.46 and 2.47 refuse a repository with reflogs; re-running scripts/setup.sh converts every repository under the data and sandbox directories, those included.) git said: %s",
		e.Dir, e.Format, e.Format, git, shellArg(e.Dir), e.Detail)
}

// Format reports the ref format of the repository at dir -- a work tree,
// a linked worktree or a bare repository -- as its config names it: ""
// when it names none, which is the files format. It reads the config file
// directly (through `git config -f`, which parses a file without opening
// the repository it belongs to), so it answers for a repository the git
// on PATH could not open. A dir that holds no repository is "", nil.
func Format(ctx context.Context, dir string) (string, error) {
	config, err := configFile(dir)
	if err != nil || config == "" {
		return "", err
	}
	out, err := git(ctx, "", "config", "--file="+config, "--get", "extensions.refstorage").Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return "", nil // not set
		}
		return "", fmt.Errorf("gitrefs: reading %s: %w", config, err)
	}
	return strings.ToLower(strings.TrimSpace(string(out))), nil
}

// Check returns an *UnreadableError when the repository at dir is in a
// ref format other than files and the git on PATH cannot open it, and nil
// for every repository it can -- a reftable one included, under a git
// that reads reftable -- and for a dir that holds no repository.
func Check(ctx context.Context, dir string) error {
	format, err := Format(ctx, dir)
	if err != nil || format == "" || format == Files {
		return err
	}
	out, err := git(ctx, dir, "rev-parse", "--git-dir").CombinedOutput()
	if err == nil {
		return nil
	}
	version, _ := git(ctx, "", "--version").Output()
	return &UnreadableError{
		Dir:    dir,
		Format: format,
		Git:    strings.TrimSpace(string(version)),
		Detail: strings.Join(strings.Fields(string(out)), " "),
	}
}

// git is a git command in dir with this process's environment, which is
// the one the daemon's own git calls run with: what Check probes is
// whether they could open the repository.
func git(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	return cmd
}

// configFile finds the config file of the repository at dir: its .git
// directory's, the common directory's for a linked worktree, or dir's own
// for a bare repository. "" means dir holds no repository.
func configFile(dir string) (string, error) {
	gitDir := filepath.Join(dir, ".git")
	info, err := os.Stat(gitDir)
	switch {
	case err == nil && info.IsDir():
	case err == nil:
		// A linked worktree or a submodule: ".git" is a "gitdir: <path>"
		// file, and that directory's "commondir" names the repository
		// whose config it shares.
		body, err := os.ReadFile(gitDir)
		if err != nil {
			return "", fmt.Errorf("gitrefs: reading %s: %w", gitDir, err)
		}
		target, ok := strings.CutPrefix(strings.TrimSpace(string(body)), "gitdir:")
		if !ok {
			return "", nil
		}
		gitDir = resolve(dir, strings.TrimSpace(target))
		if common, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
			gitDir = resolve(gitDir, strings.TrimSpace(string(common)))
		}
	case errors.Is(err, os.ErrNotExist):
		// Bare: HEAD and config in dir itself.
		if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
			return "", nil
		}
		gitDir = dir
	default:
		return "", fmt.Errorf("gitrefs: inspecting %s: %w", gitDir, err)
	}
	config := filepath.Join(gitDir, "config")
	if _, err := os.Stat(config); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("gitrefs: inspecting %s: %w", config, err)
	}
	return config, nil
}

func resolve(base, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(base, path)
}

// shellArg quotes a path for the command the error message offers, when
// it has anything a shell would split or expand.
func shellArg(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-+=:@") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
