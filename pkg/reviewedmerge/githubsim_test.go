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

package reviewedmerge_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/reviewedmerge"
)

// simRepo is acme/widgets in a real bare repository behind the GitHub
// simulator, served over HTTP so the REST forge reads it the way
// cmd/reviewed-merge reads GitHub.
type simRepo struct {
	t     *testing.T
	sim   *githubsim.Sim
	bare  string
	work  string
	forge reviewedmerge.REST
	n     int
}

func newSimRepo(t *testing.T) *simRepo {
	t.Helper()
	dir := t.TempDir()
	r := &simRepo{t: t, bare: filepath.Join(dir, "widgets.git"), work: filepath.Join(dir, "work")}
	r.git(dir, "init", "-q", "--bare", "-b", "main", r.bare)
	r.git(dir, "clone", "-q", r.bare, r.work)
	r.git(r.work, "checkout", "-q", "-b", "main")
	r.commit("seed")
	r.git(r.work, "push", "-q", "origin", "main")
	r.sim = githubsim.New("acme", "widgets", r.bare, "main")
	r.sim.RecordMergeCommits = true
	srv := httptest.NewServer(r.sim.Handler())
	t.Cleanup(srv.Close)
	r.forge = reviewedmerge.REST{BaseURL: srv.URL}
	return r
}

func (r *simRepo) git(dir string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *simRepo) commit(msg string) string {
	r.n++
	if err := os.WriteFile(filepath.Join(r.work, "f.txt"), []byte(fmt.Sprintf("%d %s\n", r.n, msg)), 0o644); err != nil {
		r.t.Fatal(err)
	}
	r.git(r.work, "add", "f.txt")
	r.git(r.work, "commit", "-q", "-m", msg)
	return r.git(r.work, "rev-parse", "HEAD")
}

func (r *simRepo) main() string { return r.git(r.work, "--git-dir", r.bare, "rev-parse", "main") }

// pull opens a pull request from a new branch carrying one commit, by
// author, and returns its number.
func (r *simRepo) pull(branch, author string) int {
	r.t.Helper()
	r.git(r.work, "fetch", "-q", "origin")
	r.git(r.work, "checkout", "-q", "-B", branch, "origin/main")
	r.commit(branch)
	r.git(r.work, "push", "-q", "-f", "origin", branch)
	body, _ := json.Marshal(map[string]string{"title": branch, "head": branch, "base": "main"})
	resp, err := r.sim.Request("POST", "/repos/acme/widgets/pulls", nil, body)
	if err != nil || resp.Status >= 300 {
		r.t.Fatalf("open pull: %v %d %s", err, resp.Status, resp.Body)
	}
	var pr struct{ Number int }
	_ = json.Unmarshal(resp.Body, &pr)
	for i := range r.sim.PullRequests {
		if r.sim.PullRequests[i].Number == pr.Number {
			r.sim.PullRequests[i].Author = author
		}
	}
	return pr.Number
}

// merge merges pull request n through the API as login, and returns the
// merge commit main now points at.
func (r *simRepo) merge(n int, login string) string {
	r.t.Helper()
	r.sim.MergeAs = login
	resp, err := r.sim.Request("PUT", fmt.Sprintf("/repos/acme/widgets/pulls/%d/merge", n), nil, []byte(`{"merge_method":"merge"}`))
	if err != nil || resp.Status >= 300 {
		r.t.Fatalf("merge #%d: %v %d %s", n, err, resp.Status, resp.Body)
	}
	return r.main()
}

func (r *simRepo) check(sha, since string, mergers ...string) (reviewedmerge.Record, error) {
	p := reviewedmerge.Policy{Repo: "acme/widgets", Branches: []string{"main", "release/v*"}, Mergers: mergers}
	return p.CheckChain(context.Background(), r.forge, sha, since, 100)
}

// The reviewed-merge cases of docs/test-plans/2026-11-11.md §7 item 6,
// against the GitHub simulator over HTTP rather than a Go fake forge:
// the simulator now answers merge_commit_sha, merged_by, review commit_id
// and author_association, and git/commits parents, which the release
// gate reads.
func TestReviewedMergeAgainstTheGitHubSimulator(t *testing.T) {
	r := newSimRepo(t)
	base := r.main()

	self := r.pull("self", "alice")
	selfMerge := r.merge(self, "alice")
	if _, err := r.check(selfMerge, base, "grain-bot"); err == nil || !strings.Contains(err.Error(), "merged by alice") {
		t.Fatalf("self-merged pull request: err = %v, want refused as merged by alice", err)
	}

	approved := r.pull("approved", "alice")
	r.sim.Approvals = map[int][]string{approved: {"bob"}}
	approvedMerge := r.merge(approved, "alice")
	rec, err := r.check(approvedMerge, selfMerge, "grain-bot")
	if err != nil || len(rec.Merges) != 1 || rec.Merges[0].Via != "review" || rec.Merges[0].By != "bob" {
		t.Fatalf("approved by another: rec = %+v, err = %v; want one merge via review by bob", rec, err)
	}
	if _, err := r.check(approvedMerge, base, "grain-bot"); err == nil || !strings.Contains(err.Error(), "merged by alice") {
		t.Fatalf("reviewed merge on top of a self-merged one: err = %v, want the self-merge refused", err)
	}

	byGrain := r.pull("by-grain", "alice")
	grainMerge := r.merge(byGrain, "grain-bot")
	rec, err = r.check(grainMerge, approvedMerge, "grain-bot")
	if err != nil || len(rec.Merges) != 1 || rec.Merges[0].Via != "grain" {
		t.Fatalf("grain-merged: rec = %+v, err = %v; want one merge via grain", rec, err)
	}
	if _, err := r.check(grainMerge, approvedMerge); err == nil {
		t.Fatalf("grain-merged with no -mergers: want refused, got pass")
	}

	stale := r.pull("stale", "alice")
	r.sim.SubmittedReviews = map[int][]githubsim.SubmittedReview{stale: {{Login: "bob", State: "APPROVED", CommitID: base, AuthorAssociation: "MEMBER"}}}
	staleMerge := r.merge(stale, "alice")
	if _, err := r.check(staleMerge, grainMerge, "grain-bot"); err == nil || !strings.Contains(err.Error(), "earlier head") {
		t.Fatalf("approval of an earlier head: err = %v, want refused", err)
	}

	outsider := r.pull("outsider", "alice")
	r.sim.SubmittedReviews = map[int][]githubsim.SubmittedReview{outsider: {{Login: "mallory", State: "APPROVED", CommitID: "", AuthorAssociation: "NONE"}}}
	outsiderMerge := r.merge(outsider, "alice")
	if _, err := r.check(outsiderMerge, staleMerge, "grain-bot"); err == nil {
		t.Fatalf("approval by someone with no standing: want refused, got pass")
	}

	r.git(r.work, "fetch", "-q", "origin")
	r.git(r.work, "checkout", "-q", "-B", "main", "origin/main")
	pushed := r.commit("pushed straight onto main")
	r.git(r.work, "push", "-q", "origin", "main")
	if _, err := r.check(pushed, outsiderMerge, "grain-bot"); err == nil || !strings.Contains(err.Error(), "not the merge commit of any pull request") {
		t.Fatalf("direct push: err = %v, want refused as not merged through a pull request", err)
	}
}
