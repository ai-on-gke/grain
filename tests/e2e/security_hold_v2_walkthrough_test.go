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

package e2e

import (
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/trustboundary"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// The security hold v2 walkthrough: docs/test-plans/2026-10-02.md §3, on
// the same harness as security_hold_walkthrough_test.go (real orchestrator,
// real git server, githubsim, a scripted agent). It covers what v1's
// walkthrough could not: a change grain cleared at one commit and then
// pushed to outside a run, a change to the list itself, the cost of a
// cleared change merging, and proposals that widen reads or writes. The
// report is docs/test-runs/2026-10-02-security-hold-v2.md.

// countingFilesSim counts GitHub's pull-request file listings.
type countingFilesSim struct {
	inner github.Transport
	files atomic.Int32
}

func (c *countingFilesSim) Request(method, path string, headers map[string]string, body []byte) (github.ApiResponse, error) {
	if pullFilesPath.MatchString(path) {
		c.files.Add(1)
	}
	return c.inner.Request(method, path, headers, body)
}

// countFileListings points the orchestrator's GitHub client at a counter
// in front of the sim, and returns it.
func (w *holdWorld) countFileListings() *countingFilesSim {
	c := &countingFilesSim{inner: w.sim}
	w.deps.Client = github.NewClient(c, nil)
	return c
}

// pushOutsideARun is a commit reaching branch the way the plan's commit B
// does: pushed over the git server by somebody other than the task's own
// run, which therefore never classifies it.
func (w *holdWorld) pushOutsideARun(branch, path string) string {
	w.t.Helper()
	dir := filepath.Join(w.t.TempDir(), "b")
	run(w.t, filepath.Dir(dir), "git", "clone", "-q", "-b", branch, w.remote, dir)
	run(w.t, dir, "git", "config", "user.email", "b@example.com")
	run(w.t, dir, "git", "config", "user.name", "b")
	mustWriteFile(w.t, filepath.Join(dir, path), "package x\n\n// commit B\n")
	run(w.t, dir, "git", "add", "-A")
	run(w.t, dir, "git", "commit", "-q", "-m", "commit B: "+path)
	run(w.t, dir, "git", "push", "-q", "origin", branch)
	return w.head(branch)
}

func (w *holdWorld) head(branch string) string {
	w.t.Helper()
	out, err := exec.Command("git", "--git-dir", w.bare, "rev-parse", "refs/heads/"+branch).Output()
	if err != nil {
		w.t.Fatalf("reading %s's head: %v", branch, err)
	}
	return strings.TrimSpace(string(out))
}

// holdComments is the pull request's comments the security hold posted.
func (w *holdWorld) holdComments() []string {
	w.sim.mu.Lock()
	defer w.sim.mu.Unlock()
	var out []string
	for _, pr := range w.sim.sim.PullRequests {
		for _, c := range w.sim.sim.Comments[pr.Number] {
			if strings.Contains(c.Body, "grain security hold") {
				out = append(out, c.Body)
			}
		}
	}
	return out
}

// clearedAtA runs task, whose own commit edits only docs/, to its finish,
// and checks it was cleared at exactly that commit under v2.
func (w *holdWorld) clearedAtA(task model.Task) string {
	w.t.Helper()
	w.cycle()
	branch := model.BranchName(task.ID)
	a := w.head(branch)
	got := w.task(task.ID)
	if !got.AutoMerge || hasSecurityHoldLink(got) {
		w.t.Fatalf("a docs-only change was held at A: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
	if want := trustboundary.Version + "@" + a; linkTarget(got, model.LinkTrustBoundaryCleared) != want {
		w.t.Fatalf("trust-boundary-cleared = %q, want %q", linkTarget(got, model.LinkTrustBoundaryCleared), want)
	}
	// Written against v2's categories, every one of which v3 keeps (it
	// only adds path-triggers, grain/task-1714; v4 adds merge-rules; v5
	// only drops the removed tunnel gateway's binary, grain/task-2099; v6
	// only adds paths and state-repo, grain/task-947; v7 only adds
	// state-privilege, which judges the state repository alone,
	// grain/task-1025; v8 only adds release-promotion, a handful of
	// release files, grain/task-1236).
	if trustboundary.Version != "trust-boundary/v8" {
		w.t.Fatalf("this walkthrough is about trust-boundary/v8, the build has %s", trustboundary.Version)
	}
	if w.merged(branch) {
		w.t.Fatal("merged before the pull request read mergeable")
	}
	return a
}

func hasSecurityHoldLink(t model.Task) bool { return linkTarget(t, model.LinkSecurityHold) != "" }

func TestSecurityHoldV2Walkthrough(t *testing.T) {
	t.Parallel()

	t.Run("a pkg/mcp commit pushed after A is reclassified and held before any merge", func(t *testing.T) {
		t.Parallel()
		w := newHoldWorld(t, false)
		task := w.create("fix a typo in the guide", true, nil)
		w.workThenReview(task.ID, "echo 'typo fixed' >> docs/guide.md",
			func(model.Task) []antigravity.Step { return askScript("why does this touch the MCP tools?") })
		w.clearedAtA(task)
		branch := model.BranchName(task.ID)
		b := w.pushOutsideARun(branch, "pkg/mcp/sandbox_tools.go")

		w.notMergedWhileHeld(task, 4)
		got := w.task(task.ID)
		if got.AutoMerge {
			t.Fatalf("auto-merge survived a push onto the trust boundary: links=%+v", got.Links)
		}
		if hold := linkTarget(got, model.LinkSecurityHold); hold != trustboundary.Version {
			t.Fatalf("security-hold = %q, want %q; links %+v", hold, trustboundary.Version, got.Links)
		}
		if want := trustboundary.Version + "@" + b; linkTarget(got, model.LinkTrustBoundaryCleared) != want {
			t.Errorf("trust-boundary-cleared = %q, want %q (the head B it classified)",
				linkTarget(got, model.LinkTrustBoundaryCleared), want)
		}
		reviewID := linkTarget(got, model.LinkSecurityReviewTask)
		if reviewID == "" {
			t.Fatalf("no security review filed: %+v", got.Links)
		}
		review := w.task(reviewID)
		if review.Base != branch || !strings.Contains(review.Body, "`pkg/mcp/sandbox_tools.go`") ||
			!strings.Contains(review.Body, "exec-sandbox") {
			t.Errorf("review %s is not about B's path on %s: base=%s body=%s", reviewID, branch, review.Base, review.Body)
		}
		if cs := w.holdComments(); len(cs) != 1 || !strings.Contains(cs[0], "`pkg/mcp/sandbox_tools.go`") {
			t.Errorf("want exactly one PR comment naming the path over 4 cycles, got %d: %q", len(cs), cs)
		}
	})

	t.Run("a pkg/trustboundary commit pushed after A is held for a human, a clean review notwithstanding", func(t *testing.T) {
		t.Parallel()
		w := newHoldWorld(t, false)
		task := w.create("fix a typo in the guide", true, nil)
		w.workThenReview(task.ID, "echo 'typo fixed' >> docs/guide.md", nil) // the review finishes clean
		w.clearedAtA(task)
		w.pushOutsideARun(model.BranchName(task.ID), "pkg/trustboundary/trustboundary.go")

		w.notMergedWhileHeld(task, 6)
		got := w.task(task.ID)
		reviewID := linkTarget(got, model.LinkSecurityReviewTask)
		if reviewID == "" {
			t.Fatalf("no security review filed: %+v", got.Links)
		}
		if st := w.state(reviewID); st != model.StateCompleted && st != model.StateClosed {
			t.Fatalf("the review did not finish (state %s), so this proved nothing", st)
		}
		if got.AutoMerge || hasSecurityHoldLink(got) {
			t.Fatalf("a clean review released a hold-policy change: automerge=%v links=%+v", got.AutoMerge, got.Links)
		}
		cs := w.holdComments()
		if len(cs) != 1 || !strings.Contains(cs[0], "**hold-policy**") || !strings.Contains(cs[0], "never merge automatically") {
			t.Errorf("want one PR comment saying hold-policy never merges itself, got %d: %q", len(cs), cs)
		}
	})

	t.Run("a cleared change whose head has not moved merges on one file listing", func(t *testing.T) {
		t.Parallel()
		w := newHoldWorld(t, false)
		listings := w.countFileListings()
		task := w.create("fix a typo in the guide", true, nil)
		w.workThenReview(task.ID, "echo 'typo fixed' >> docs/guide.md", nil)
		w.clearedAtA(task)
		if n := listings.files.Load(); n != 1 {
			t.Fatalf("finishing the run listed the PR's files %d times, want 1", n)
		}
		if !w.cycles(4, func() bool { w.markAllClean(); return w.merged(model.BranchName(task.ID)) }) {
			t.Fatal("the cleared change never merged")
		}
		if n := listings.files.Load(); n != 1 {
			t.Errorf("merging an unmoved, cleared head listed the PR's files again: %d listings in all, want 1", n)
		}
		if cs := w.holdComments(); len(cs) != 0 {
			t.Errorf("a cleared change got hold comments: %q", cs)
		}
	})
}

// TestWidenedProposalsWalkthrough is §3's proposal half: from an
// auto-merge parent, a proposal that reads or lands somewhere the parent
// could not is filed for a person, whether the run's propose_task call is
// relayed at the end of the run or filed live through ui.CreateTask.
func TestWidenedProposalsWalkthrough(t *testing.T) {
	t.Parallel()

	const readsTitle, writesTitle = "follow-up reading acme/secret-sauce", "follow-up landing in acme/gadgets"
	check := func(t *testing.T, w *holdWorld, parent string) {
		t.Helper()
		proposals := w.proposalsOf(parent)
		if len(proposals) != 2 {
			t.Fatalf("%s filed %d proposals, want 2", parent, len(proposals))
		}
		for _, p := range proposals {
			st := w.state(p.ID)
			if st != model.StateProposed || p.Approval != nil {
				t.Errorf("%q from auto-merge parent %s: state=%s approval=%+v, want proposed with no approval",
					p.Title, parent, st, p.Approval)
			}
			switch p.Title {
			case readsTitle:
				// Reads gate the queueing only (model.AutoSubmits): what a
				// task reads says nothing about where it lands, so a
				// same-branch proposal keeps the auto-merge it inherits.
				if !strings.Contains(p.Body, "asks to read `acme/secret-sauce`") {
					t.Errorf("reads proposal does not say why it waits: %q", p.Body)
				}
			case writesTitle:
				if p.AutoMerge {
					t.Errorf("writes proposal inherited auto-merge: %+v", p)
				}
				if !strings.Contains(p.Body, "`acme/gadgets`") || !strings.Contains(p.Body, "was never given itself") {
					t.Errorf("writes proposal does not say why it waits: %q", p.Body)
				}
			default:
				t.Errorf("unexpected proposal %q", p.Title)
			}
		}
	}

	t.Run("relayed at the end of the parent's run", func(t *testing.T) {
		t.Parallel()
		w := newHoldWorld(t, false)
		parent := w.create("auto-merging parent", true, []string{})
		w.setScript(func(task model.Task) []antigravity.Step {
			if task.ID != parent.ID {
				return []antigravity.Step{finalText("nothing to do")}
			}
			steps := w.changeScript(task, "echo 'typo' >> docs/guide.md")
			return append([]antigravity.Step{
				toolCall("propose_task", map[string]any{"title": readsTitle, "body": "split out",
					"reads": []any{"acme/secret-sauce"}}),
				toolCall("propose_task", map[string]any{"title": writesTitle, "body": "split out",
					"writes": []any{map[string]any{"repo": "acme/gadgets", "base": "main"}}}),
			}, steps...)
		})
		w.cycle()
		check(t, w, parent.ID)
	})

	t.Run("filed live while the parent is running", func(t *testing.T) {
		t.Parallel()
		w := newHoldWorld(t, false)
		parent := w.create("auto-merging parent", true, []string{})
		for _, req := range []ui.CreateTaskRequest{
			{Title: readsTitle, Reads: []string{"acme/secret-sauce"}, AutoMerge: true},
			{Title: writesTitle, Writes: []ui.WriteTarget{{Repo: "acme/gadgets", Base: "main"}}, AutoMerge: true},
		} {
			req.Description, req.Repo, req.ProposedBy = "split out", "acme/widgets", parent.ID
			if _, err := w.ui.CreateTask(w.ctx, req); err != nil {
				t.Fatalf("CreateTask(%q): %v", req.Title, err)
			}
		}
		check(t, w, parent.ID)
	})
}
