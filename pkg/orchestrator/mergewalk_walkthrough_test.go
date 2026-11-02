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

package orchestrator_test

// mergewalk_walkthrough_test.go is §4 of docs/test-plans/2026-11-02.md
// ("What is NOT proved" 1-3) walked cycle by cycle over githubsim, with
// real git content behind every pull request: GitHub's mergeability is
// worked out from the bare repo on every read (git merge-tree), not set by
// hand, so a merge that moves the base really does make a sibling
// conflict, and every request grain makes is logged. The report is
// docs/test-runs/2026-11-02-merge-walk-retry.md.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// liveGitHub routes each request to its repository's sim, works out every
// pull request's mergeability from git before it is read (unless a test
// pinned it), and keeps a log of the requests that matter to the walk:
// pull request reads and merges.
type liveGitHub struct {
	sims map[string]*githubsim.Sim

	mu     sync.Mutex
	pinned map[string]bool // "owner/name#branch" whose Mergeable a test set
	log    []string
}

func newLiveGitHub(sims ...*githubsim.Sim) *liveGitHub {
	g := &liveGitHub{sims: map[string]*githubsim.Sim{}, pinned: map[string]bool{}}
	for _, s := range sims {
		g.sims[s.Owner+"/"+s.Repo] = s
	}
	return g
}

var pullPath = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/pulls/(\d+)(/merge)?$`)

func (g *liveGitHub) Request(method, path string, headers map[string]string, body []byte) (github.ApiResponse, error) {
	parts := strings.SplitN(strings.TrimPrefix(path, "/repos/"), "/", 3)
	sim := g.sims[parts[0]+"/"+parts[1]]
	if sim == nil {
		return github.ApiResponse{Status: http.StatusNotFound, Body: []byte(`{"message":"Not Found"}`)}, nil
	}
	if m := pullPath.FindStringSubmatch(strings.SplitN(path, "?", 2)[0]); m != nil {
		g.mu.Lock()
		g.log = append(g.log, method+" "+m[1]+"/"+m[2]+"#"+m[3]+m[4])
		g.mu.Unlock()
		if method == http.MethodGet && m[4] == "" {
			g.recompute(sim, m[3])
		}
	}
	return sim.Request(method, path, headers, body)
}

// recompute is GitHub's background mergeability job, finished: the pull
// request numbered number merges cleanly into its base as the base stands
// now, or it does not.
func (g *liveGitHub) recompute(sim *githubsim.Sim, number string) {
	for i := range sim.PullRequests {
		pr := &sim.PullRequests[i]
		if fmt.Sprint(pr.Number) != number || pr.Merged {
			continue
		}
		g.mu.Lock()
		pinned := g.pinned[sim.Owner+"/"+sim.Repo+"#"+pr.Head]
		g.mu.Unlock()
		if pinned {
			return
		}
		clean := exec.Command("git", "--git-dir", sim.BareRepo, "merge-tree", "--write-tree",
			"refs/heads/"+pr.Base, "refs/heads/"+pr.Head).Run() == nil
		pr.Mergeable = &clean
	}
}

// pinUnknown is GitHub still working out one branch's mergeability.
func (g *liveGitHub) pinUnknown(sim *githubsim.Sim, branch string) {
	g.mu.Lock()
	g.pinned[sim.Owner+"/"+sim.Repo+"#"+branch] = true
	g.mu.Unlock()
	setBranchUnknown(sim, branch)
}

// takeLog returns the requests since the last call.
func (g *liveGitHub) takeLog() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := g.log
	g.log = nil
	return out
}

// commitFile puts content at file on branch in sim's repo, as the task's
// run would have pushed it.
func commitFile(t *testing.T, sim *githubsim.Sim, branch, file, content string) {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "git", "clone", "-q", "-b", branch, sim.BareRepo, "work")
	wd := filepath.Join(dir, "work")
	if err := os.WriteFile(filepath.Join(wd, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, wd, "git", "add", "-A")
	run(t, wd, "git", "-c", "user.email=agent@example.com", "-c", "user.name=agent", "commit", "-q", "-m", "edit "+file)
	run(t, wd, "git", "push", "-q", "origin", branch)
}

// walkCycle is one merge-queue pass at at with cap cap; it returns which
// of ids merged on it, each id's row note, and the requests it made.
type walkCycle struct {
	merged []string
	notes  map[string]string
	log    []string
}

func runWalkCycle(t *testing.T, ctx context.Context, store *model.Store, g *liveGitHub,
	client github.Client, cap int, at time.Time, branches map[string]string) walkCycle {

	t.Helper()
	before := map[string]bool{}
	for id, b := range branches {
		before[id] = g.merged(b)
	}
	if err := orchestrator.SyncPullRequestsGuarding(ctx, store, client, nil, orchestrator.StateRepoGate{}, cap, at); err != nil {
		t.Fatalf("SyncPullRequestsGuarding: %v", err)
	}
	c := walkCycle{notes: map[string]string{}, log: g.takeLog()}
	for _, id := range sortedKeys(branches) {
		if !before[id] && g.merged(branches[id]) {
			c.merged = append(c.merged, id)
		}
		_, c.notes[id] = mergeQueueStateOf(t, ctx, store, id)
	}
	return c
}

func (g *liveGitHub) merged(branch string) bool {
	for _, s := range g.sims {
		if mergedBranch(s, branch) {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func logCycle(t *testing.T, n int, c walkCycle) {
	t.Helper()
	t.Logf("cycle %d: merged %v", n, c.merged)
	for _, id := range sortedKeys(c.notes) {
		t.Logf("  %s: %q", id, c.notes[id])
	}
	t.Logf("  requests: %s", strings.Join(c.log, ", "))
}

// walkQueue is item 1's queue: an UNKNOWN pull request, one whose check
// never reports and one that conflicts with main, queued in front of four
// green ones that each touch a file of their own.
func walkQueue(t *testing.T) (context.Context, *model.Store, *githubsim.Sim, *liveGitHub, github.Client, map[string]string) {
	t.Helper()
	store, ctx := openStore(t)
	sim, rest := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	branches := map[string]string{}
	for _, id := range []string{"u1", "p2", "c3", "g4", "g5", "g6", "g7"} {
		_, b := queuedTaskWithPullRequest(t, ctx, store, sim, rest, id, repo)
		branches[id] = b
		commitFile(t, sim, b, id+".txt", id+"\n")
		sim.CheckRuns[b] = greenCheck
	}
	// c3 changes README.md one way on its branch, main another.
	commitFile(t, sim, branches["c3"], "README.md", "c3's README\n")
	commitFile(t, sim, "main", "README.md", "main's README\n")
	sim.CheckRuns[branches["p2"]] = []github.CheckRun{{Name: "cla/google", Status: "queued"}}
	g := newLiveGitHub(sim)
	g.pinUnknown(sim, branches["u1"])
	return ctx, store, sim, g, github.NewClient(g, nil), branches
}

// mergeNote matches "merges next cycle: this cycle's N merge(s) into
// acme/widgets main is used (task X merged)".
var capNote = regexp.MustCompile(`^merges next cycle: this cycle's (\d) merges? into acme/widgets main (is|are) used \(tasks? (.+) merged\)$`)

func TestMergeWalkWalkthroughCapOneMergesOneGreenPerCycle(t *testing.T) {
	t.Parallel()
	ctx, store, _, g, client, branches := walkQueue(t)
	at := baseTime
	var order []string
	for n := 1; n <= 5; n++ {
		c := runWalkCycle(t, ctx, store, g, client, 1, at, branches)
		logCycle(t, n, c)
		if n <= 4 && len(c.merged) != 1 {
			t.Fatalf("cycle %d merged %v, want exactly one green change on a cap of 1", n, c.merged)
		}
		order = append(order, c.merged...)
		for _, id := range []string{"g4", "g5", "g6", "g7"} {
			if g.merged(branches[id]) {
				continue
			}
			if m := capNote.FindStringSubmatch(c.notes[id]); m == nil || m[1] != "1" || m[3] != c.merged[0] {
				t.Errorf("cycle %d: %s says %q, want the cap of 1 named with this cycle's merge (%v)", n, id, c.notes[id], c.merged)
			}
		}
		for _, id := range []string{"u1", "p2", "c3"} {
			if g.merged(branches[id]) {
				t.Fatalf("cycle %d: %s merged without passing the predicate", n, id)
			}
			if strings.Contains(c.notes[id], "behind task") || strings.Contains(c.notes[id], "merges next cycle") {
				t.Errorf("cycle %d: %s says %q, which is not its own blocker", n, id, c.notes[id])
			}
		}
		at = at.Add(4 * time.Minute)
	}
	if strings.Join(order, ",") != "g4,g5,g6,g7" {
		t.Errorf("merge order %v, want g4..g7 one per cycle", order)
	}
}

func TestMergeWalkWalkthroughCapTwoReReadsBeforeTheSecondMerge(t *testing.T) {
	t.Parallel()
	ctx, store, _, g, client, branches := walkQueue(t)
	c := runWalkCycle(t, ctx, store, g, client, 2, baseTime, branches)
	logCycle(t, 1, c)
	if strings.Join(c.merged, ",") != "g4,g5" {
		t.Fatalf("cycle 1 merged %v, want g4 and g5 on a cap of 2", c.merged)
	}
	// Between the first merge and the second, the second's pull request
	// is read again.
	first, second := -1, -1
	for i, r := range c.log {
		if strings.HasSuffix(r, "/merge") && strings.HasPrefix(r, "PUT ") {
			if first < 0 {
				first = i
			} else if second < 0 {
				second = i
			}
		}
	}
	if first < 0 || second < 0 {
		t.Fatalf("two merges not in the request log: %v", c.log)
	}
	secondPR := strings.TrimSuffix(strings.TrimPrefix(c.log[second], "PUT "), "/merge")
	reread := false
	for _, r := range c.log[first+1 : second] {
		if r == "GET "+secondPR {
			reread = true
		}
	}
	if !reread {
		t.Errorf("%s was not read again between the first merge and its own: %v", secondPR, c.log[first:second+1])
	}
	for _, id := range []string{"g6", "g7"} {
		if m := capNote.FindStringSubmatch(c.notes[id]); m == nil || m[1] != "2" {
			t.Errorf("%s says %q, want the cap of 2 named", id, c.notes[id])
		}
	}
	c = runWalkCycle(t, ctx, store, g, client, 2, baseTime.Add(4*time.Minute), branches)
	logCycle(t, 2, c)
	if strings.Join(c.merged, ",") != "g6,g7" {
		t.Fatalf("cycle 2 merged %v, want g6 and g7", c.merged)
	}
	c = runWalkCycle(t, ctx, store, g, client, 2, baseTime.Add(20*time.Minute), branches)
	logCycle(t, 3, c)
	for id, want := range map[string]string{
		"u1": "waiting for GitHub to work out whether this branch merges cleanly for 20m",
	} {
		if c.notes[id] != want {
			t.Errorf("%s says %q, want %q", id, c.notes[id], want)
		}
	}
	if n := c.notes["p2"]; !strings.Contains(n, "`cla/google`") || !strings.HasSuffix(n, " for 20m") {
		t.Errorf("p2 says %q, want its check named and how long", n)
	}
}

// Two green pull requests that each change README.md the same way the
// other does not: the first merges, and the second, read again against
// the moved base, does not merge in the same cycle and says why.
func TestMergeWalkWalkthroughTwoMergesThatConflictWithEachOther(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, rest := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	branches := map[string]string{}
	for _, id := range []string{"x1", "x2", "x3"} {
		_, b := queuedTaskWithPullRequest(t, ctx, store, sim, rest, id, repo)
		branches[id] = b
		sim.CheckRuns[b] = greenCheck
	}
	commitFile(t, sim, branches["x1"], "README.md", "x1's README\n")
	commitFile(t, sim, branches["x2"], "README.md", "x2's README\n")
	commitFile(t, sim, branches["x3"], "x3.txt", "x3\n")
	g := newLiveGitHub(sim)
	client := github.NewClient(g, nil)

	c := runWalkCycle(t, ctx, store, g, client, 3, baseTime, branches)
	logCycle(t, 1, c)
	if strings.Join(c.merged, ",") != "x1,x3" {
		t.Fatalf("cycle 1 merged %v, want x1 and x3 (x2 conflicts with x1 once x1 lands)", c.merged)
	}
	state, note := mergeQueueStateOf(t, ctx, store, "x2")
	t.Logf("x2: %s %q", state, note)
	if state != "repair_needed" || !strings.Contains(note, "conflict") {
		t.Errorf("x2 says %s %q, want repair_needed naming the conflict", state, note)
	}
	for _, r := range c.log {
		if r == fmt.Sprintf("PUT acme/widgets#%d/merge", prNumber(t, sim, branches["x2"])) {
			t.Errorf("x2's merge was attempted on a stale mergeability: %v", c.log)
		}
	}
}


// Two repositories in one cycle, each with three green pull requests and
// a cap of 2: each lane merges two, and each held row names its own
// repository's merges.
func TestMergeWalkWalkthroughTwoRepositoriesMergeToTheirOwnCaps(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	widgets, wrest := newSim(t, "acme", "widgets", "main")
	gadgets, grest := newSim(t, "acme", "gadgets", "main")
	branches := map[string]string{}
	for _, id := range []string{"w1", "w2", "w3"} {
		_, b := queuedTaskWithPullRequest(t, ctx, store, widgets, wrest, id, model.RepoRef{Owner: "acme", Name: "widgets"})
		branches[id] = b
		commitFile(t, widgets, b, id+".txt", id+"\n")
		widgets.CheckRuns[b] = greenCheck
	}
	for _, id := range []string{"k1", "k2", "k3"} {
		_, b := queuedTaskWithPullRequest(t, ctx, store, gadgets, grest, id, model.RepoRef{Owner: "acme", Name: "gadgets"})
		branches[id] = b
		commitFile(t, gadgets, b, id+".txt", id+"\n")
		gadgets.CheckRuns[b] = greenCheck
	}
	g := newLiveGitHub(widgets, gadgets)
	client := github.NewClient(g, nil)
	c := runWalkCycle(t, ctx, store, g, client, 2, baseTime, branches)
	logCycle(t, 1, c)
	if strings.Join(c.merged, ",") != "k1,k2,w1,w2" {
		t.Fatalf("cycle 1 merged %v, want two in each repository", c.merged)
	}
	for id, repo := range map[string]string{"w3": "acme/widgets", "k3": "acme/gadgets"} {
		if !strings.Contains(c.notes[id], "into "+repo+" main") {
			t.Errorf("%s says %q, want its own lane (%s main) named", id, c.notes[id], repo)
		}
	}
}
