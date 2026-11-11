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

package githubsim

import (
	"context"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/reviewedmerge"
)

// TestReviewedMergeCheckReadsTheSim runs the upgrade's reviewed-merge
// check (pkg/reviewedmerge's REST forge, unmodified) against the sim over
// HTTP. Before the sim answered GET .../git/commits/{sha}, carried
// merged_by, merge_commit_sha and base on a commit's pull requests, and
// listed reviews with commit_id and author_association, every chain was
// refused against it -- reviewed or not -- so the check could not be
// walked against the simulator at all (docs/test-runs/2026-11-11-upgrades.md).
func TestReviewedMergeCheckReadsTheSim(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	bare := initBareRepo(t, filepath.Join(dir, "repo.git"), "main")
	wd := filepath.Join(dir, "work")
	run(t, dir, "git", "clone", "-q", bare, wd)
	run(t, wd, "git", "config", "user.email", "alice@example.com")
	run(t, wd, "git", "config", "user.name", "alice")
	revParse := func(rev string) string {
		t.Helper()
		out, err := exec.Command("git", "-C", wd, "rev-parse", rev).Output()
		if err != nil {
			t.Fatalf("rev-parse %s: %v", rev, err)
		}
		return strings.TrimSpace(string(out))
	}
	run(t, wd, "git", "commit", "-q", "--allow-empty", "-m", "running build")
	base := revParse("HEAD")
	run(t, wd, "git", "checkout", "-q", "-b", "feature")
	run(t, wd, "git", "commit", "-q", "--allow-empty", "-m", "the change")
	head := revParse("HEAD")
	run(t, wd, "git", "checkout", "-q", "main")
	run(t, wd, "git", "merge", "-q", "--no-ff", "-m", "Merge pull request #7", "feature")
	merge := revParse("HEAD")
	run(t, wd, "git", "push", "-q", "origin", "main", "feature")

	sim := New("acme", "widgets", bare, "main")
	sim.PullRequests = []PullRequest{{
		Number: 7, Head: "feature", Base: "main", State: "closed", Merged: true,
		Author: "alice", MergedBy: "alice", MergeCommitSHA: merge,
	}}
	sim.PullRequests[0].MergedAt = sim.now()
	srv := httptest.NewServer(sim.Handler())
	defer srv.Close()

	forge := reviewedmerge.REST{BaseURL: srv.URL}
	policy := reviewedmerge.Policy{Repo: "acme/widgets", Branches: []string{"main"}, Mergers: []string{"grain-bot"}}
	ctx := context.Background()

	parent, err := forge.FirstParent(ctx, "acme/widgets", merge)
	if err != nil || parent != base {
		t.Fatalf("FirstParent(merge) = %q, %v; want %s", parent, err, base)
	}
	cmp, err := forge.Compare(ctx, "acme/widgets", base, merge)
	if err != nil || cmp.Status != "ahead" {
		t.Fatalf("Compare(base, merge) = %+v, %v; want ahead", cmp, err)
	}

	// Self-merged, nobody's approval: refused, and saying who merged.
	if _, err := policy.CheckChain(ctx, forge, merge, base, 10); err == nil || !strings.Contains(err.Error(), "merged by alice") {
		t.Fatalf("self-merged chain: err = %v, want a refusal naming alice", err)
	}

	// A collaborator's approval of the merged head: allowed.
	sim.PullReviews = map[int][]PullReview{7: {{Login: "bob", State: "APPROVED", CommitID: head, Association: "COLLABORATOR"}}}
	rec, err := policy.CheckChain(ctx, forge, merge, base, 10)
	if err != nil {
		t.Fatalf("reviewed chain refused: %v", err)
	}
	if len(rec.Merges) != 1 || rec.Merges[0].Via != "review" || rec.Merges[0].By != "bob" {
		t.Fatalf("reviewed chain = %+v, want one merge reviewed by bob", rec.Merges)
	}

	// An approval of an earlier head is stale.
	sim.PullReviews = map[int][]PullReview{7: {{Login: "bob", State: "APPROVED", CommitID: base, Association: "COLLABORATOR"}}}
	if _, err := policy.CheckChain(ctx, forge, merge, base, 10); err == nil || !strings.Contains(err.Error(), "earlier head") {
		t.Fatalf("stale approval: err = %v, want a refusal naming the earlier head", err)
	}

	// Merged by grain's own login: allowed with no review.
	sim.PullReviews = nil
	sim.PullRequests[0].MergedBy = "grain-bot"
	if rec, err := policy.CheckChain(ctx, forge, merge, base, 10); err != nil || rec.Merges[0].Via != "grain" {
		t.Fatalf("grain's merge: rec = %+v, err = %v; want Via grain", rec, err)
	}
}
