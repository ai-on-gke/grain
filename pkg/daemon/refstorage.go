package daemon

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// checkRefStorage refuses a git repository at dir whose refs are not in
// the files backend (extensions.refstorage, task 410), with a message an
// operator can act on rather than git's own.
//
// A repository a newer git made with reftable refs -- a host-mode
// install's host git, say, before the deployment moved into the release
// container -- is one the image's git (Debian 12's 2.39) cannot open at
// all: every command dies on "unknown repository extension found:
// refstorage ... fatal: not in a git directory", which says nothing
// about where that repository came from or what to do. It is refused
// rather than repaired here: the git that could read its refs is the
// one this process does not have, so it cannot tell which commits are
// pushed, and a state repository is local-only unless an operator
// mirrors it, so there is often no origin to clone again from either.
// scripts/setup.sh converts these on the host before the cutover
// (convert_reftable_repos); this is what a deployment that skipped it
// is told.
//
// The config file is read directly rather than through git, since git
// is exactly what cannot read it. Anything that is not (yet) a
// repository, or whose config cannot be read, is left to the caller's
// own git to judge.
func checkRefStorage(dir string) error {
	format := refStorage(dir)
	if format == "" || format == "files" {
		return nil
	}
	return fmt.Errorf("%s is a git repository with %s refs (extensions.refstorage = %s), which the git in "+
		"this image (%s) cannot read -- it was most likely created by a newer git on the host, before this "+
		"deployment moved into the release container. Convert it to the files backend on the host, as the "+
		"account that owns it, with git 2.46 or later:\n\n"+
		"    git -C %s refs migrate --ref-format=files\n\n"+
		"(or re-run scripts/setup.sh, which converts every repository under the data directory), then start "+
		"grain again", dir, format, format, gitVersion(), dir)
}

// refStorage is dir's extensions.refstorage, lowercased, or "" when it
// sets none or is not a repository this can read.
func refStorage(dir string) string {
	config := gitConfigPath(dir)
	if config == "" {
		return ""
	}
	b, err := os.ReadFile(config)
	if err != nil {
		return ""
	}
	var section, format string
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			end := strings.IndexByte(line, ']')
			if end < 0 {
				continue
			}
			section = strings.ToLower(strings.TrimSpace(line[1:end]))
			line = strings.TrimSpace(line[end+1:])
			if line == "" {
				continue
			}
		}
		if section != "extensions" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "refstorage") {
			continue
		}
		if i := strings.IndexAny(value, "#;"); i >= 0 {
			value = value[:i]
		}
		format = strings.ToLower(strings.Trim(strings.TrimSpace(value), `"`))
	}
	return format
}

// gitConfigPath is the config file of the repository whose working tree
// is dir: .git/config, or the common directory's config when .git is a
// "gitdir:" file (a linked worktree or a separated git dir). "" when dir
// has no .git.
func gitConfigPath(dir string) string {
	dotGit := filepath.Join(dir, ".git")
	info, err := os.Stat(dotGit)
	if err != nil {
		return ""
	}
	gitDir := dotGit
	if !info.IsDir() {
		b, err := os.ReadFile(dotGit)
		if err != nil {
			return ""
		}
		target, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
		if !ok {
			return ""
		}
		gitDir = strings.TrimSpace(target)
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(dir, gitDir)
		}
		if common, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
			c := strings.TrimSpace(string(common))
			if !filepath.IsAbs(c) {
				c = filepath.Join(gitDir, c)
			}
			gitDir = c
		}
	}
	return filepath.Join(gitDir, "config")
}

// gitVersion is `git version` for the message above, or a description
// of why there is none.
func gitVersion() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "version")
	// Nothing of the daemon's own: `git version` needs only to be found.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	out, err := cmd.Output()
	if err != nil {
		return "git version unknown"
	}
	return strings.TrimSpace(string(out))
}
