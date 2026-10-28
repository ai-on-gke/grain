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

package portpick

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// fixture is a remote with a main/candidate pair and a working clone of
// it to make history in.
type fixture struct {
	t      *testing.T
	remote string
	work   string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	f := &fixture{t: t, remote: filepath.Join(root, "remote.git"), work: filepath.Join(root, "work")}
	f.git(root, "init", "--bare", "--quiet", "-b", "main", f.remote)
	f.git(root, "clone", "--quiet", f.remote, f.work)
	f.write("README", "one\ntwo\nthree\n")
	f.commit("initial")
	f.git(f.work, "push", "--quiet", "origin", "HEAD:refs/heads/main", "HEAD:refs/heads/candidate")
	return f
}

func (f *fixture) git(dir string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@example.com",
		"GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@example.com", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *fixture) write(name, content string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.work, name), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) commit(msg string) string {
	f.git(f.work, "add", "-A")
	f.git(f.work, "commit", "--quiet", "-m", msg)
	return f.git(f.work, "rev-parse", "HEAD")
}

// change makes a pull request's worth of commits on a branch off main and
// merges it into main the way the merge queue does, returning the PR's
// head and its own commits.
func (f *fixture) change(files map[string]string, msgs ...string) (string, []string) {
	f.git(f.work, "checkout", "--quiet", "-B", "feature", "origin/main")
	var commits []string
	for i, msg := range msgs {
		for name, content := range files {
			f.write(name, content+strings.Repeat("x\n", i))
		}
		commits = append(commits, f.commit(msg))
	}
	head := commits[len(commits)-1]
	f.git(f.work, "checkout", "--quiet", "-B", "main", "origin/main")
	f.git(f.work, "merge", "--quiet", "--no-ff", "-m", "Merge pull request #7", "feature")
	f.git(f.work, "push", "--quiet", "origin", "main")
	return head, commits
}

// onCandidate commits files straight onto candidate.
func (f *fixture) onCandidate(files map[string]string, msg string) string {
	f.git(f.work, "fetch", "--quiet", "origin")
	f.git(f.work, "checkout", "--quiet", "-B", "candidate", "origin/candidate")
	for name, content := range files {
		f.write(name, content)
	}
	sha := f.commit(msg)
	f.git(f.work, "push", "--quiet", "origin", "candidate")
	return sha
}

func (f *fixture) porter() *Porter {
	return &Porter{
		Dir: f.t.TempDir(),
		Remote: func(context.Context, model.RepoRef, string) (Remote, error) {
			return Remote{URL: f.remote}, nil
		},
	}
}

func request(head string, commits []string) Request {
	return Request{
		Repo:   model.RepoRef{Owner: "o", Name: "r"},
		Source: "main", Target: "candidate", PullRequest: 7,
		Head: head, Commits: commits, Branch: "grain/task-9",
	}
}

func TestPortCleanPushesTheBranchWithFooters(t *testing.T) {
	f := newFixture(t)
	head, commits := f.change(map[string]string{"new.txt": "hello\n"}, "add new", "grow new")

	res, err := f.porter().Port(context.Background(), request(head, commits))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != Picked || len(res.Picked) != 2 {
		t.Fatalf("result = %+v, want two picks", res)
	}
	tip := f.git(f.remote, "rev-parse", "refs/heads/grain/task-9")
	if tip != res.Head {
		t.Fatalf("pushed tip %s, result says %s", tip, res.Head)
	}
	// On top of candidate, oldest first, each saying where it came from.
	if parent := f.git(f.remote, "rev-parse", tip+"~2"); parent != f.git(f.remote, "rev-parse", "candidate") {
		t.Fatalf("port is not two commits on candidate: %s", parent)
	}
	for i, p := range res.Picked {
		if p.Source != commits[i] {
			t.Fatalf("pick %d is %s, want %s", i, p.Source, commits[i])
		}
		msg := f.git(f.remote, "log", "-1", "--format=%B", p.Ported)
		if !strings.Contains(msg, "(cherry picked from commit "+p.Source+")") ||
			!strings.Contains(msg, "Backported-from: "+p.Source) {
			t.Fatalf("pick message lacks its footers:\n%s", msg)
		}
		if author := f.git(f.remote, "log", "-1", "--format=%ae", p.Ported); author != "a@example.com" {
			t.Fatalf("author = %s, want the original's", author)
		}
	}
	// Nothing else moved.
	if got := f.git(f.remote, "for-each-ref", "--format=%(refname)"); got != "refs/heads/candidate\nrefs/heads/grain/task-9\nrefs/heads/main" {
		t.Fatalf("remote refs = %q", got)
	}
}

func TestPortConflictPushesNothingAndNamesThePaths(t *testing.T) {
	f := newFixture(t)
	f.onCandidate(map[string]string{"README": "candidate's own\n"}, "diverge on candidate")
	head, commits := f.change(map[string]string{"README": "main's own\n"}, "rewrite readme")

	res, err := f.porter().Port(context.Background(), request(head, commits))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != Conflicted || res.Conflict == nil {
		t.Fatalf("result = %+v, want a conflict", res)
	}
	if res.Conflict.Commit != commits[0] || strings.Join(res.Conflict.Paths, ",") != "README" ||
		res.Conflict.Subject != "rewrite readme" {
		t.Fatalf("conflict = %+v", res.Conflict)
	}
	if out := f.git(f.remote, "branch", "--list", "grain/task-9"); out != "" {
		t.Fatalf("a conflicted port pushed %q", out)
	}
}

func TestPortAlreadyPortedByFooterIsNothing(t *testing.T) {
	f := newFixture(t)
	head, commits := f.change(map[string]string{"new.txt": "hello\n"}, "add new")
	// Candidate already has a port of it, made differently (another
	// file), saying so in its footer.
	f.onCandidate(map[string]string{"other.txt": "ported by hand\n"}, "port it\n\nBackported-from: "+commits[0][:10])

	res, err := f.porter().Port(context.Background(), request(head, commits))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != NothingToPort || len(res.Dropped) != 1 {
		t.Fatalf("result = %+v, want nothing to port", res)
	}
	if out := f.git(f.remote, "branch", "--list", "grain/task-9"); out != "" {
		t.Fatalf("pushed %q for nothing", out)
	}
}

func TestPortAlreadyPortedByContentIsNothing(t *testing.T) {
	f := newFixture(t)
	head, commits := f.change(map[string]string{"new.txt": "hello\n"}, "add new")
	f.onCandidate(map[string]string{"new.txt": "hello\n"}, "same change, other commit")

	res, err := f.porter().Port(context.Background(), request(head, commits))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != NothingToPort {
		t.Fatalf("result = %+v, want nothing to port", res)
	}
}

func TestPortRefusesAnExistingBranch(t *testing.T) {
	f := newFixture(t)
	head, commits := f.change(map[string]string{"new.txt": "hello\n"}, "add new")
	f.git(f.work, "push", "--quiet", "origin", "origin/main:refs/heads/grain/task-9")
	before := f.git(f.remote, "rev-parse", "grain/task-9")

	if _, err := f.porter().Port(context.Background(), request(head, commits)); err == nil {
		t.Fatal("ported onto a branch that already existed")
	}
	if after := f.git(f.remote, "rev-parse", "grain/task-9"); after != before {
		t.Fatalf("existing branch moved from %s to %s", before, after)
	}
}

func TestPortRefusesToPushToThePair(t *testing.T) {
	for _, branch := range []string{"main", "candidate", "-f", "x:y", "+x", "a..b"} {
		req := request(strings.Repeat("a", 40), []string{strings.Repeat("a", 40)})
		req.Branch = branch
		if err := validate(req); err == nil {
			t.Errorf("validate accepted branch %q", branch)
		}
	}
	req := request("HEAD", []string{strings.Repeat("a", 40)})
	if err := validate(req); err == nil {
		t.Error("validate accepted a symbolic head")
	}
}
