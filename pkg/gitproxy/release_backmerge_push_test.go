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

package gitproxy

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The retired nightly release cut (scripts/release-candidate-cut.sh, now
// scripts/release-from-trunk.sh) pushed main as a back-merge of
// candidate, then a release-notes commit on top of it, then candidate and
// its rc tag -- all refs its template declared, main without
// `force: true`. Any push of main through the proxy still has to prove
// itself a fast-forward the same way. These are that push's shapes, through a real
// git client, so the fast-forward proof is tested against the packs git
// actually sends rather than ones built by hand (grain/task-3561).

// releaseCutFixture is a repository served through the proxy, with main
// and candidate diverged the way they are on the night of a cut, and a
// client clone of it holding the cut's declared push set.
type releaseCutFixture struct {
	t             *testing.T
	state, client string
}

func newReleaseCutFixture(t *testing.T) *releaseCutFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	f := &releaseCutFixture{t: t, state: filepath.Join(root, "state"), client: filepath.Join(root, "client")}
	runLocalGit(t, "", "init", "-q", "-b", "main", f.state)
	runLocalGit(t, f.state, "config", "receive.denyCurrentBranch", "ignore")
	gitAs := []string{"-c", "user.name=grain", "-c", "user.email=grain@example.invalid"}
	// Files big enough, and edited on both sides, that git deltifies the
	// trees and blobs the back-merge carries against the remote's own --
	// a thin pack, as the real cut's is.
	write := func(name string, edit func(i int) string) {
		var b strings.Builder
		for i := 0; i < 400; i++ {
			fmt.Fprintf(&b, "%s line %d %s\n", name, i, edit(i))
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(f.state, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.state, name), []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		runLocalGit(t, f.state, "add", name)
	}
	same := func(int) string { return "" }
	write("pkg/a.go", same)
	write("pkg/b.go", same)
	write("docs/c.md", same)
	runLocalGit(t, f.state, append(gitAs, "commit", "-q", "-m", "Initial")...)
	runLocalGit(t, f.state, append(gitAs, "tag", "-a", "-m", "v1.8.0", "v1.8.0")...)
	runLocalGit(t, f.state, "branch", "candidate")
	for i := 0; i < 3; i++ {
		i := i
		write("pkg/a.go", func(n int) string {
			if n%50 == i {
				return "main fix " + fmt.Sprint(i)
			}
			return ""
		})
		runLocalGit(t, f.state, append(gitAs, "commit", "-q", "-m", fmt.Sprintf("feat: main %d", i))...)
	}
	runLocalGit(t, f.state, "checkout", "-q", "candidate")
	for i := 0; i < 3; i++ {
		i := i
		write("pkg/b.go", func(n int) string {
			if n%40 == i {
				return "candidate fix " + fmt.Sprint(i)
			}
			return ""
		})
		runLocalGit(t, f.state, append(gitAs, "commit", "-q", "-m", fmt.Sprintf("fix: candidate %d", i))...)
	}
	runLocalGit(t, f.state, append(gitAs, "tag", "-a", "-m", "v1.9.0-rc.1", "v1.9.0-rc.1", "candidate~2")...)
	runLocalGit(t, f.state, "checkout", "-q", "--detach", "main")

	tokens, err := LoadSandboxTokens(writeTokensFile(t, t.TempDir(), map[string]string{"sandbox-0": "tok0"}))
	if err != nil {
		t.Fatal(err)
	}
	proxy := &GitProxy{
		Authorizer: &ModelAuthorizer{
			Store: stubScopeLookup{target: &model.RepoRef{Owner: "acme", Name: "widgets"}},
			Branches: stubDeclaredPush{
				stubBranchLookup: stubBranchLookup{branch: "grain/eu/task-3201", live: true},
				// The release-candidate template's push: list, as
				// .grain/templates/release-candidate.yml declares it.
				declared: releaseDeclaration(
					model.RefPush{Pattern: "refs/heads/candidate", Force: true},
					model.RefPush{Pattern: "refs/heads/main"},
					model.RefPush{Pattern: "refs/heads/reconcile-candidate-to-main", Force: true},
					model.RefPush{Pattern: "refs/tags/v*"},
					model.RefPush{Pattern: "refs/tags/latest", Force: true},
				),
			},
		},
		Tokens:    tokens,
		Forwarder: &FakeForwarder{},
		Audit:     &RecordingAuditLog{},
		Local:     &LocalRepository{Owner: "acme", Name: "widgets", Dir: f.state},
	}
	srv := httptest.NewServer(NewHandler(proxy))
	t.Cleanup(srv.Close)
	url := strings.Replace(srv.URL, "http://", "http://x:tok0@", 1) + "/acme/widgets.git"
	runLocalGit(t, "", "clone", "-q", url, f.client)
	runLocalGit(t, f.client, "config", "user.name", "grain-ci[bot]")
	runLocalGit(t, f.client, "config", "user.email", "grain-ci[bot]@users.noreply.github.com")
	runLocalGit(t, f.client, "fetch", "-q", "--tags", "origin")
	return f
}

// backMerge does what the cut's steps 1 and 3 do on the client: main
// checked out from origin/main, candidate merged into it --no-ff, and the
// promoted release's notes committed on top. It returns the merge and the
// notes commit.
func (f *releaseCutFixture) backMerge() (merge, notes string) {
	f.t.Helper()
	runLocalGit(f.t, f.client, "checkout", "-q", "-B", "main", "origin/main")
	runLocalGit(f.t, f.client, "merge", "-q", "--no-ff", "-m", "feat(release): back-merge qualified candidate fixes into main", "origin/candidate")
	merge = f.rev("HEAD")
	if err := os.MkdirAll(filepath.Join(f.client, "docs", "releases"), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.client, "docs", "releases", "v1.9.0.md"), []byte("- notes\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
	runLocalGit(f.t, f.client, "add", "docs/releases/v1.9.0.md")
	runLocalGit(f.t, f.client, "commit", "-q", "-m", "docs(release): add release notes for v1.9.0")
	return merge, f.rev("HEAD")
}

func (f *releaseCutFixture) rev(ref string) string {
	f.t.Helper()
	out, err := localGitOutput(f.t, f.client, "", "rev-parse", ref)
	if err != nil {
		f.t.Fatalf("rev-parse %s: %v\n%s", ref, err, out)
	}
	return out
}

func (f *releaseCutFixture) remote(ref string) string {
	f.t.Helper()
	out, _ := localGitOutput(f.t, f.state, "", "rev-parse", "--verify", "-q", ref)
	return out
}

func (f *releaseCutFixture) push(args ...string) (string, error) {
	f.t.Helper()
	return localGitOutput(f.t, f.client, "", append([]string{"push", "origin"}, args...)...)
}

func TestTheReleaseCutsBackMergeFastForwardsMainThroughTheProxy(t *testing.T) {
	for name, tc := range map[string]struct {
		// steps are the pushes, in order, after the back-merge and the
		// notes commit; every one of them must be taken.
		steps [][]string
	}{
		// Step 1 of the cut: the merge on its own, then step 3: the notes.
		"the merge, then the notes": {steps: [][]string{
			{"MERGE:refs/heads/main"},
			{"main"},
		}},
		// Step 1's push refused or skipped: step 3 carries both.
		"merge and notes in one push": {steps: [][]string{{"main"}}},
		// Step 4 after step 3: candidate force-moved to main, and the tag.
		"main, then candidate and the rc tag": {steps: [][]string{
			{"main"},
			{"--force", "candidate"},
			{"v1.10.0-rc.1"},
		}},
		// One command for every ref the cut moves: the pack is the union.
		"main with candidate and tags together": {steps: [][]string{
			{"main", "+candidate", "v1.10.0-rc.1", "v1.9.0"},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newReleaseCutFixture(t)
			oldMain := f.remote("refs/heads/main")
			merge, notes := f.backMerge()
			if parents := strings.Fields(f.rev(merge + "^@")); len(parents) == 0 || parents[0] != oldMain {
				t.Fatalf("the back-merge's first parent is %v, want main's tip %s", parents, oldMain)
			}
			runLocalGit(t, f.client, "branch", "-f", "candidate", "main")
			runLocalGit(t, f.client, "tag", "-a", "-m", "rc", "v1.10.0-rc.1", "main")
			runLocalGit(t, f.client, "tag", "-a", "-m", "v1.9.0", "v1.9.0", "origin/candidate")
			for _, step := range tc.steps {
				for i, arg := range step {
					step[i] = strings.Replace(arg, "MERGE", merge, 1)
				}
				if out, err := f.push(step...); err != nil {
					t.Fatalf("git push %v was refused: %v\n%s", step, err, out)
				}
			}
			if got := f.remote("refs/heads/main"); got != notes {
				t.Errorf("main = %s, want the notes commit %s", got, notes)
			}
		})
	}
}

// Task 3201's own sequence: the back-merge pushed somewhere else first --
// the run's own branch, to be tested in CI, or candidate, cut from it
// before main took it -- so git sends main's update with none of the
// commits between its old and new tips. The walk runs out at once and
// carries on in the remote's own commits (ancestry.go), so the merge, and
// the notes commit on top of it, reach main as they are, by a plain git
// push.
func TestABackMergeAlreadyOnTheRemoteFastForwardsMain(t *testing.T) {
	for name, first := range map[string]string{
		"on the run's own branch": "HEAD:refs/heads/grain/eu/task-3201",
		"on candidate":            "+HEAD:refs/heads/candidate",
	} {
		t.Run(name, func(t *testing.T) {
			f := newReleaseCutFixture(t)
			oldMain := f.remote("refs/heads/main")
			merge, notes := f.backMerge()
			if out, err := f.push(first); err != nil {
				t.Fatalf("pushing %s: %v\n%s", first, err, out)
			}
			for _, ref := range []string{merge + ":refs/heads/main", "main"} {
				if out, err := f.push(ref); err != nil {
					t.Fatalf("git push %s with its commits already on the remote was refused: %v\n%s", ref, err, out)
				}
			}
			if got := f.remote("refs/heads/main"); got != notes {
				t.Errorf("main = %s, want the tested notes commit %s itself (was %s)", got, notes, oldMain)
			}
		})
	}
}

// A rewrite of main is refused whatever the push carries.
func TestAForcePushOfMainIsRefused(t *testing.T) {
	f := newReleaseCutFixture(t)
	oldMain := f.remote("refs/heads/main")
	runLocalGit(t, f.client, "checkout", "-q", "-B", "main", oldMain+"~1")
	runLocalGit(t, f.client, "commit", "-q", "--allow-empty", "-m", "rewritten")
	if out, err := f.push("--force", "main"); err == nil || !strings.Contains(out, "could not show") {
		t.Errorf("a force-push of main was let through: err %v\n%s", err, out)
	}
	if got := f.remote("refs/heads/main"); got != oldMain {
		t.Errorf("main moved to %s", got)
	}
}
