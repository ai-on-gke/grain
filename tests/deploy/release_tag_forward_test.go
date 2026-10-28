// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// release-images.sh's tag_may_move (its `may-move` phase) decides whether a release moves its
// branch's moving tag: forward only, failing closed (grain/task-4432).
// The rule it replaced moved the tag only from the branch tip, which in a
// run of merges meant no release ever moved it, and grain:main sat on
// f09c277 through four merges.
//
// These run the real script against a real git history and a stub
// `docker` that answers `imagetools inspect` with whatever revision label
// the case says the published tag carries.

// forwardHistory is a repository with a line of three commits on main --
// older, released, newer -- and one commit off to the side that main
// never had. It is its own remote, so `git ls-remote origin` and `git
// fetch origin` work against it.
type forwardHistory struct {
	dir                            string
	older, released, newer, beside string
}

func newForwardHistory(t *testing.T) forwardHistory {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	git("remote", "add", "origin", dir)
	commit := func(msg string) string {
		git("commit", "-q", "--allow-empty", "-m", msg)
		return git("rev-parse", "HEAD")
	}
	h := forwardHistory{dir: dir}
	h.older = commit("older")
	git("checkout", "-q", "-b", "side")
	h.beside = commit("beside")
	git("checkout", "-q", "main")
	h.released = commit("released")
	h.newer = commit("newer")
	// The checkout a release runs in is at the commit it released.
	git("checkout", "-q", "--detach", h.released)
	return h
}

// forwardRun runs the script for h.released (or commit, when given)
// with the registry answering `answer` -- a revision label, or, with
// failing set, an error -- and reports whether it said to move and what
// it said.
func forwardRun(t *testing.T, h forwardHistory, commit, answer string, failing, buildx bool) (bool, string) {
	t.Helper()
	bin := t.TempDir()
	stub := "#!/bin/sh\n"
	if !buildx {
		stub += "if [ \"$1\" = buildx ]; then exit 1; fi\n"
	}
	stub += "if [ \"$1 $2\" = 'buildx version' ]; then echo 'github.com/docker/buildx v0.17.1'; exit 0; fi\n"
	if failing {
		stub += "echo \"$STUB_ANSWER\" >&2; exit 1\n"
	} else {
		stub += "printf '%s\\n' \"$STUB_ANSWER\"; exit 0\n"
	}
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git here")
	}
	if commit == "" {
		commit = h.released
	}
	cmd := exec.Command("bash", filepath.Join(repoRoot(t), "scripts", "release-images.sh"),
		"may-move", "ghcr.io/bwsalmon/grain/grain:main", commit, "main")
	cmd.Dir = h.dir
	cmd.Env = []string{
		"PATH=" + bin + ":" + filepath.Dir(gitPath) + ":/usr/bin:/bin",
		"HOME=" + t.TempDir(),
		"STUB_ANSWER=" + answer,
	}
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, string(out)
	}
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
		return false, string(out)
	}
	t.Fatalf("release-images.sh may-move: %v\n%s", err, out)
	return false, ""
}

func TestAMovingTagMovesForwardOntoADescendant(t *testing.T) {
	h := newForwardHistory(t)
	moved, out := forwardRun(t, h, "", h.older, false, true)
	if !moved {
		t.Fatalf("the tag holds %s, an ancestor of the release, and did not move:\n%s", h.older[:7], out)
	}
	containsRun(t, out, "an ancestor of "+h.released[:7])
}

// The tip rule's whole reason for existing, kept: a release that
// finishes after a newer one must not take the tag back. And the case
// the tip rule got wrong: the released commit is not the tip any more,
// and the tag still moves (above) because what it holds is older.
func TestAMovingTagNeverMovesBackwards(t *testing.T) {
	h := newForwardHistory(t)
	moved, out := forwardRun(t, h, "", h.newer, false, true)
	if moved {
		t.Fatalf("the tag holds %s, newer than the release, and was moved back onto %s:\n%s",
			h.newer[:7], h.released[:7], out)
	}
	containsRun(t, out, "newer than "+h.released[:7])
}

func TestAMovingTagThatHasDivergedOrIsUnknownStays(t *testing.T) {
	h := newForwardHistory(t)
	for name, c := range map[string]struct {
		commit  string
		answer  string
		failing bool
		buildx  bool
		want    string
	}{
		"diverged":           {answer: h.beside, buildx: true, want: "neither behind nor ahead"},
		"a commit git lacks": {answer: strings.Repeat("ab", 20), buildx: true, want: "neither this checkout nor"},
		"no label":           {answer: "<no value>", buildx: true, want: "names no commit"},
		"empty label":        {answer: "", buildx: true, want: "names no commit"},
		"registry error":     {answer: "unexpected status: 500 Internal Server Error", failing: true, buildx: true, want: "could not read"},
		"no buildx, not tip": {answer: h.older, buildx: false, want: "no docker buildx"},
		// A release of a commit that was never on the branch, ahead of
		// what the tag holds or not, is not a build of the branch.
		"off the branch": {commit: h.beside, answer: h.older, buildx: true, want: "is not on main"},
	} {
		t.Run(name, func(t *testing.T) {
			moved, out := forwardRun(t, h, c.commit, c.answer, c.failing, c.buildx)
			if moved {
				t.Fatalf("moved the tag on %q, which proves nothing about order:\n%s", c.answer, out)
			}
			containsRun(t, out, c.want)
			containsRun(t, out, "leaving ghcr.io/bwsalmon/grain/grain:main where it is")
		})
	}
}

// Where there is nothing to go backwards from -- the tag does not exist
// yet -- or no way to read what it holds, or the tag holds a pre-cutover
// commit from an older repository history that git lacks, the tip rule
// still answers, and a release of the tip creates or moves it.
func TestAMissingTagIsCreatedFromTheTipOnly(t *testing.T) {
	h := newForwardHistory(t)
	notFound := "ERROR: ghcr.io/bwsalmon/grain/grain:main: not found"
	if moved, out := forwardRun(t, h, h.newer, notFound, true, true); !moved {
		t.Fatalf("a missing tag was not created from the branch tip:\n%s", out)
	}
	if moved, out := forwardRun(t, h, "", notFound, true, true); moved {
		t.Fatalf("a missing tag was created from %s, which is not the tip:\n%s", h.released[:7], out)
	}
	if moved, out := forwardRun(t, h, h.newer, h.older, false, false); !moved {
		t.Fatalf("with no buildx, a release of the tip did not move the tag:\n%s", out)
	}
	preCutover := strings.Repeat("ab", 20)
	if moved, out := forwardRun(t, h, h.newer, preCutover, false, true); !moved {
		t.Fatalf("a tag holding a pre-cutover commit git lacks did not move onto the branch tip:\n%s", out)
	} else {
		containsRun(t, out, "which neither this checkout nor origin's main has, and "+h.newer[:7]+" is still main's tip")
	}
}

func TestReleasingTheSameCommitAgainRepublishesIt(t *testing.T) {
	h := newForwardHistory(t)
	if moved, out := forwardRun(t, h, "", h.released+"\n", false, true); !moved {
		t.Fatalf("a re-release of the commit the tag already holds did not move it:\n%s", out)
	}
}

// release-images.sh's push asks the forward rule, and no longer reads the
// tip itself.
func TestReleaseImagesMovesTagsThroughTheForwardRule(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "release-images.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	if !strings.Contains(script, `if ! tag_may_move "$root/grain:$branch" "$commit" "$GRAIN_CI_REF"; then`) {
		t.Error("release-images.sh does not ask tag_may_move before moving the branch tags")
	}
	if strings.Contains(script, `git ls-remote origin "refs/heads/$GRAIN_CI_REF"`) {
		t.Error("release-images.sh still moves moving tags only from the branch tip")
	}
}
