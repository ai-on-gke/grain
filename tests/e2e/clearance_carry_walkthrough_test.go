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

// TestClearanceCarryWalkthrough is docs/test-plans/2026-10-12.md §13,
// steps 1 and 2: a security-review clearance carried from a change on
// main to its port onto candidate, and every case in which it must not
// be, through real orchestrator.RunCycle cycles -- dispatch, the
// finish-time classification, SyncSecurityHolds, the merge queue and the
// port filer -- over a real git server and githubsim. The port is made
// both by the mechanical porter (portpick.Porter) and by a scripted
// agent following the port body's recipe. The report is
// docs/test-runs/2026-10-12-clearance-carry-red-base-links.md.
package e2e

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/portpick"
	"github.com/ai-on-gke/grain/pkg/trustboundary"
)

// The trust-boundary file every scenario changes: pkg/mcp is
// exec-sandbox, a category a clean security review releases.
const carryPath = "pkg/mcp/tools.go"

// carryAgent is the scripted model session: a security review finds
// nothing, a port follows its body's recipe (resolving a conflict in the
// picked commit's favour, as port_mechanical_walkthrough_test.go's
// scriptAgent does), and any other task runs the shell its title names.
type carryAgent struct {
	remote string
	store  *model.Store
	mu     sync.Mutex
	// scripts is the shell each non-port task runs, keyed by title.
	scripts map[string]string
	// ran is every task id the agent was run for, in order.
	ran []string
}

func (a *carryAgent) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	a.mu.Lock()
	a.ran = append(a.ran, cfg.TaskID)
	a.mu.Unlock()
	task, err := a.store.GetTask(ctx, cfg.TaskID)
	if err != nil || task == nil {
		return nil, fmt.Errorf("carryAgent: reading %s: %v", cfg.TaskID, err)
	}
	if task.Origin.Reason == model.ReasonReview {
		return antigravity.NewForTest(antigravity.Steps(finalText("no findings"))).Run(ctx, cfg)
	}
	branch := task.BranchName()
	prefix := "git clone -q " + a.remote + " work && cd work && " +
		"git config user.email agent@example.com && git config user.name agent && " +
		"git checkout -q " + task.Base + " && git checkout -q -b " + branch + " && "
	var cmd string
	if m := portTitleRe.FindStringSubmatch(task.Title); m != nil {
		cm := portCherryRe.FindStringSubmatch(task.Body)
		if cm == nil {
			return nil, fmt.Errorf("carryAgent: %s's body names no `git cherry -v` command:\n%s", task.ID, task.Body)
		}
		cherry := strings.ReplaceAll(cm[1], "<merge>", "$M")
		cmd = prefix +
			"M=$(git log --first-parent --merges --format='%H %s' origin/" + sourceBranchOf(task) +
			" | grep -F 'pull request #" + m[1] + " ' | head -1 | cut -d' ' -f1) && test -n \"$M\" && " +
			"for c in $(" + cherry + " | awk '$1 == \"+\" {print $2}'); do " +
			"{ git cherry-pick -x $c >/dev/null 2>&1 || " +
			"{ git checkout -q --theirs -- . && git add -A && GIT_EDITOR=true git cherry-pick --continue >/dev/null; }; } && " +
			"git commit -q --amend -m \"$(git log -1 --format=%B)\n\nBackported-from: $c\"; done && " +
			"git push -q origin " + branch
	} else {
		a.mu.Lock()
		script, ok := a.scripts[task.Title]
		a.mu.Unlock()
		if !ok {
			return nil, fmt.Errorf("carryAgent: no script for %q", task.Title)
		}
		cmd = prefix + script + " && git push -q origin " + branch
	}
	return antigravity.NewForTest(antigravity.Steps(
		toolCall("run_command", map[string]any{"command": cmd}),
		finalText("pushed "+branch),
	)).Run(ctx, cfg)
}

func (a *carryAgent) ranFor(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Contains(a.ran, id)
}

// carryWorld is one paired-branch repository (main and candidate, with
// .grain/settings.yml declaring the pair), one store and one
// orchestrator.
type carryWorld struct {
	t      *testing.T
	ctx    context.Context
	store  *model.Store
	sim    *syncedSim
	bare   string
	remote string
	deps   orchestrator.Deps
	agent  *carryAgent
	repo   model.RepoRef
	now    time.Time
}

// newCarryWorld seeds the repository. candidateTools, when not empty, is
// a version of carryPath that only candidate has, so a port's patch of
// it reads differently there (or does not apply at all).
func newCarryWorld(t *testing.T, mechanical bool, candidateTools string) *carryWorld {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	const owner, repoName = "acme", "widgets"
	w := &carryWorld{t: t, ctx: context.Background(), now: baseTime, repo: model.RepoRef{Owner: owner, Name: repoName}}
	upstream := t.TempDir()
	w.bare = initBareRepo(t, filepath.Join(upstream, owner, repoName+".git"), "main")
	run(t, upstream, "git", "-C", w.bare, "config", "http.receivepack", "true")
	seedParent := t.TempDir()
	run(t, seedParent, "git", "clone", "-q", w.bare, "seed")
	seed := filepath.Join(seedParent, "seed")
	run(t, seed, "git", "config", "user.email", "seed@example.com")
	run(t, seed, "git", "config", "user.name", "seed")
	mustWriteFile(t, filepath.Join(seed, "NOTES.md"), "seed\n")
	mustWriteFile(t, filepath.Join(seed, "docs", "guide.md"), "# guide\n")
	mustWriteFile(t, filepath.Join(seed, carryPath), "package mcp\n\n// Tools lists the sandbox tools.\nfunc Tools() []string {\n\treturn nil\n}\n")
	mustWriteFile(t, filepath.Join(seed, ".grain", "settings.yml"), "port-branches: [main, candidate]\n")
	run(t, seed, "git", "add", "-A")
	run(t, seed, "git", "commit", "-q", "-m", "initial commit")
	run(t, seed, "git", "push", "-q", "origin", "main", "main:candidate")
	if candidateTools != "" {
		run(t, seed, "git", "checkout", "-q", "-b", "candidate", "origin/candidate")
		mustWriteFile(t, filepath.Join(seed, carryPath), candidateTools)
		run(t, seed, "git", "commit", "-q", "-am", "fix(mcp): candidate only\n\nPort: no")
		run(t, seed, "git", "push", "-q", "origin", "candidate")
	}

	w.sim = &syncedSim{sim: githubsim.New(owner, repoName, w.bare, "main")}
	host := githubHostServer(t, w.sim, upstream)
	w.remote = "http://" + host + "/" + owner + "/" + repoName + ".git"
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	w.store = model.New(db)
	if err := w.store.Init(w.ctx); err != nil {
		t.Fatal(err)
	}
	if err := w.store.PutRepoConfig(w.ctx, model.RepoConfig{Repo: w.repo, Declarations: true}); err != nil {
		t.Fatal(err)
	}
	w.agent = &carryAgent{remote: w.remote, store: w.store, scripts: map[string]string{}}
	w.deps = orchestrator.Deps{
		Store: w.store, Client: github.NewClient(w.sim, nil), Sandboxes: credentialed(t, w.remote), MaxWorkers: 1,
		Declarations: bareDeclarations{t: t, bare: w.bare},
		CI:           &greenCI{runs: map[string]ci.Run{}},
		Framework: func(context.Context, orchestrator.AgentChoice) (agent.Framework, error) {
			return w.agent, nil
		},
	}
	if mechanical {
		w.deps.Config.Porter = &portpick.Porter{
			Dir: t.TempDir(),
			Remote: func(context.Context, model.RepoRef, string) (portpick.Remote, error) {
				return portpick.Remote{URL: w.bare}, nil
			},
		}
	}
	w.cycle() // the declaration sync reads port-branches off main
	return w
}

func (w *carryWorld) cycle() {
	w.t.Helper()
	w.now = w.now.Add(10 * time.Minute)
	if err := orchestrator.RunCycle(w.ctx, w.deps, w.now); err != nil {
		w.t.Fatalf("RunCycle at %s: %v", w.now, err)
	}
}

func (w *carryWorld) markOpenClean() {
	for _, pr := range w.sim.openPullRequests() {
		w.sim.markPullRequestClean(pr.Number)
	}
}

func (w *carryWorld) task(id string) model.Task {
	w.t.Helper()
	got, err := w.store.GetTask(w.ctx, id)
	if err != nil || got == nil {
		w.t.Fatalf("reading %s: %v", id, err)
	}
	return *got
}

func (w *carryWorld) state(id string) model.State {
	w.t.Helper()
	st, err := w.store.State(w.ctx, id)
	if err != nil {
		w.t.Fatal(err)
	}
	return st
}

// file puts an approved, auto-merging task on main whose run is script.
func (w *carryWorld) file(title, script string) string {
	w.t.Helper()
	id, err := w.store.NewTaskID(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	orderKey, err := w.store.OrderKeyForNewTask(w.ctx, false)
	if err != nil {
		w.t.Fatal(err)
	}
	now := w.now
	if err := w.store.PutTask(w.ctx, model.Task{
		ID: id, Title: title, Body: "body of " + title, Intent: model.IntentImplement,
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human("alice")}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human("alice")}, ApprovedAt: &now,
		Target: &w.repo, Base: "main", AutoMerge: true,
		OrderKey: orderKey, CreatedAt: &now,
	}); err != nil {
		w.t.Fatal(err)
	}
	w.agent.mu.Lock()
	w.agent.scripts[title] = script
	w.agent.mu.Unlock()
	return id
}

// until runs up to n cycles, settling every open pull request's
// mergeability before each, and stops once done says so.
func (w *carryWorld) until(n int, done func() bool) bool {
	w.t.Helper()
	for i := 0; i < n; i++ {
		w.markOpenClean()
		w.cycle()
		if done() {
			return true
		}
	}
	return false
}

func (w *carryWorld) closed(id string) func() bool {
	return func() bool { return w.state(id) == model.StateClosed }
}

// reviewsOf is every security review filed for task's branch.
func (w *carryWorld) reviewsOf(id string) []model.Task {
	w.t.Helper()
	all, err := w.store.ListTasks(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	var out []model.Task
	for _, task := range all {
		if task.Origin.Reason == model.ReasonReview && task.Base == string(model.BranchName(id)) &&
			strings.HasPrefix(task.Title, "Security review: ") {
			out = append(out, task)
		}
	}
	return out
}

func (w *carryWorld) portOf(source string) model.Task {
	w.t.Helper()
	all, err := w.store.ListTasks(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	var out []model.Task
	for _, task := range all {
		if slices.Contains(task.Links, model.Link{Kind: model.LinkPortOf, Target: source}) {
			out = append(out, task)
		}
	}
	if len(out) != 1 {
		w.t.Fatalf("ports of %s = %d, want 1: %+v", source, len(out), out)
	}
	return out[0]
}

// prComments is the bodies of the comments on task's pull request that
// contain substr.
func (w *carryWorld) prComments(id, substr string) []string {
	w.t.Helper()
	ref := model.TaskPullRequest(w.task(id))
	if ref == nil {
		w.t.Fatalf("%s has no pull request", id)
	}
	w.sim.mu.Lock()
	defer w.sim.mu.Unlock()
	var out []string
	for _, c := range w.sim.sim.Comments[ref.Number] {
		if strings.Contains(c.Body, substr) {
			out = append(out, c.Body)
		}
	}
	return out
}

func (w *carryWorld) taskComments(id, substr string) int {
	w.t.Helper()
	cs, err := w.store.Comments(w.ctx, id)
	if err != nil {
		w.t.Fatal(err)
	}
	n := 0
	for _, c := range cs {
		if strings.Contains(c.Body, substr) {
			n++
		}
	}
	return n
}

func (w *carryWorld) onCandidate(path string) string {
	w.t.Helper()
	out, err := exec.Command("git", "--git-dir", w.bare, "show", "candidate:"+path).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// landCleared drives a change to carryPath on main through its hold, its
// clean security review and its merge, and checks it was cleared at the
// head that merged. extra is shell run after the change's own commit.
func (w *carryWorld) landCleared(extra string) string {
	w.t.Helper()
	script := commitFile(carryPath, "package mcp\n\n// Tools lists the sandbox tools.\nfunc Tools() []string {\n\treturn []string{\"exec\"}\n}", "feat(mcp): list exec")
	if extra != "" {
		script += " && " + extra
	}
	source := w.file("list the exec tool", script)
	held := false
	if !w.until(12, func() bool {
		if linkTarget(w.task(source), model.LinkSecurityHold) != "" {
			held = true
		}
		return w.state(source) == model.StateClosed
	}) {
		w.t.Fatalf("the source %s never merged: state %s links %+v", source, w.state(source), w.task(source).Links)
	}
	got := w.task(source)
	if !held || linkTarget(got, model.LinkSecurityReviewTask) == "" {
		w.t.Fatalf("the source was never held for review (held=%v links %+v)", held, got.Links)
	}
	if n := len(w.reviewsOf(source)); n != 1 {
		w.t.Fatalf("the source has %d security reviews, want 1", n)
	}
	if !strings.HasPrefix(linkTarget(got, model.LinkTrustBoundaryCleared), trustboundary.Version+"@") {
		w.t.Fatalf("the source was not cleared under %s: %+v", trustboundary.Version, got.Links)
	}
	return source
}

// carried checks port merged onto candidate on its source's clearance,
// with no review of its own and the notice posted once.
func (w *carryWorld) carried(source, port string) {
	w.t.Helper()
	if !w.until(10, w.closed(port)) {
		got := w.task(port)
		w.t.Fatalf("the port %s never merged: state %s automerge %v links %+v comments %d", port,
			w.state(port), got.AutoMerge, got.Links, w.taskComments(port, ""))
	}
	got := w.task(port)
	if !got.AutoMerge {
		w.t.Errorf("the port lost auto-merge: %+v", got.Links)
	}
	if from := linkTarget(got, model.LinkClearanceInheritedFrom); from != source {
		w.t.Errorf("clearance-inherited-from = %q, want %q (links %+v)", from, source, got.Links)
	}
	if hold, review := linkTarget(got, model.LinkSecurityHold), linkTarget(got, model.LinkSecurityReviewTask); hold != "" || review != "" {
		w.t.Errorf("the port carries a hold %q or review %q", hold, review)
	}
	if rs := w.reviewsOf(port); len(rs) != 0 {
		w.t.Errorf("the port was reviewed again: %d review(s)", len(rs))
	}
	if cs := w.prComments(port, "is a port of task "+source); len(cs) != 1 {
		w.t.Errorf("want the inherited-clearance notice once on the port's pull request, got %d: %q", len(cs), cs)
	} else if !strings.Contains(cs[0], "`"+carryPath+"`") {
		w.t.Errorf("the notice does not name %s: %s", carryPath, cs[0])
	}
	if n := w.taskComments(port, "is a port of task "+source); n != 1 {
		w.t.Errorf("want the notice once on the port task, got %d", n)
	}
	if !strings.Contains(w.onCandidate(carryPath), "\"exec\"") {
		w.t.Errorf("candidate does not carry the ported change:\n%s", w.onCandidate(carryPath))
	}
}

// heldForItsOwnReview checks port was held, not given its source's
// clearance, and returns its review.
func (w *carryWorld) heldForItsOwnReview(port string) model.Task {
	w.t.Helper()
	if !w.until(6, func() bool { return linkTarget(w.task(port), model.LinkSecurityReviewTask) != "" }) {
		got := w.task(port)
		w.t.Fatalf("the port %s was never held for a review: state %s automerge %v links %+v",
			port, w.state(port), got.AutoMerge, got.Links)
	}
	got := w.task(port)
	if from := linkTarget(got, model.LinkClearanceInheritedFrom); from != "" {
		w.t.Errorf("the port inherited %s's clearance anyway: %+v", from, got.Links)
	}
	if linkTarget(got, model.LinkSecurityHold) == "" || got.AutoMerge {
		w.t.Errorf("the port is not on hold: automerge %v links %+v", got.AutoMerge, got.Links)
	}
	rs := w.reviewsOf(port)
	if len(rs) != 1 {
		w.t.Fatalf("the port has %d security reviews, want 1", len(rs))
	}
	return rs[0]
}

// pushTo commits a docs-only change onto branch from outside any run:
// a CI repair that touches nothing on the trust boundary.
func (w *carryWorld) pushTo(branch string) {
	w.t.Helper()
	dir := filepath.Join(w.t.TempDir(), "r")
	run(w.t, filepath.Dir(dir), "git", "clone", "-q", "-b", branch, w.bare, dir)
	run(w.t, dir, "git", "config", "user.email", "ci@example.com")
	run(w.t, dir, "git", "config", "user.name", "ci")
	mustWriteFile(w.t, filepath.Join(dir, "docs", "guide.md"), "# guide\n\nfixed the CI\n")
	run(w.t, dir, "git", "commit", "-q", "-am", "fix(ci): repair the build")
	run(w.t, dir, "git", "push", "-q", "origin", branch)
}

func TestClearanceCarryWalkthrough(t *testing.T) {
	t.Parallel()

	for _, mechanical := range []bool{false, true} {
		name := "agent-made port"
		if mechanical {
			name = "mechanical port"
		}
		t.Run(name+" inherits its source's clearance", func(t *testing.T) {
			t.Parallel()
			w := newCarryWorld(t, mechanical, "")
			source := w.landCleared("")
			port := w.portOf(source)
			w.carried(source, port.ID)
			if made := w.agent.ranFor(port.ID); made == mechanical {
				t.Errorf("port %s: agent ran = %v, want %v for a %s", port.ID, made, !mechanical, name)
			}
		})
	}

	t.Run("a port whose conflict resolution changed the patch is held, and a CI repair files no second review", func(t *testing.T) {
		t.Parallel()
		w := newCarryWorld(t, false,
			"package mcp\n\n// Tools lists the sandbox tools on candidate.\nfunc Tools() []string {\n\treturn nil\n}\n")
		source := w.landCleared("")
		port := w.portOf(source)
		review := w.heldForItsOwnReview(port.ID)

		// A CI repair onto the held port touches no trust-boundary patch:
		// it keeps waiting on the review it has.
		w.pushTo(port.BranchName())
		for i := 0; i < 2; i++ {
			w.cycle()
		}
		if rs := w.reviewsOf(port.ID); len(rs) != 1 || rs[0].ID != review.ID {
			t.Fatalf("a docs-only push filed another review: %d review(s), want only %s", len(rs), review.ID)
		}
		if !w.until(10, w.closed(port.ID)) {
			got := w.task(port.ID)
			t.Fatalf("the reviewed port never merged: state %s links %+v", w.state(port.ID), got.Links)
		}
		if rs := w.reviewsOf(port.ID); len(rs) != 1 {
			t.Errorf("reviews of the port by its merge = %d, want 1", len(rs))
		}
	})

	// The two below edit the source's recorded clearance after it merged,
	// standing in for a source cleared at a head it did not merge at and
	// for a build whose trust-boundary list has changed since.
	rewrite := func(w *carryWorld, source string, fn func(version, sha string) string) {
		t.Helper()
		if err := w.store.UpdateTask(w.ctx, source, func(task *model.Task) error {
			for i, l := range task.Links {
				if l.Kind == model.LinkTrustBoundaryCleared {
					version, sha, _ := strings.Cut(l.Target, "@")
					task.Links[i].Target = fn(version, sha)
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("a port of a source cleared at an earlier head is held", func(t *testing.T) {
		t.Parallel()
		w := newCarryWorld(t, false, "")
		source := w.landCleared("")
		port := w.portOf(source)
		if w.agent.ranFor(port.ID) {
			t.Fatal("the port ran in the cycle that filed it; the rewrite below would come too late")
		}
		earlier := strings.TrimSpace(runOutput(t, "", "git", "--git-dir", w.bare, "rev-parse", "main~1"))
		rewrite(w, source, func(version, _ string) string { return version + "@" + earlier })
		w.heldForItsOwnReview(port.ID)
	})
	t.Run("a port whose source was cleared under another trust-boundary list is held", func(t *testing.T) {
		t.Parallel()
		w := newCarryWorld(t, false, "")
		source := w.landCleared("")
		port := w.portOf(source)
		rewrite(w, source, func(_, sha string) string { return "trust-boundary/v4@" + sha })
		w.heldForItsOwnReview(port.ID)
	})

	t.Run("a cleared change moved by a CI repair keeps its clearance", func(t *testing.T) {
		t.Parallel()
		w := newCarryWorld(t, false, "")
		source := w.file("list the exec tool", commitFile(carryPath,
			"package mcp\n\nfunc Tools() []string { return []string{\"exec\"} }", "feat(mcp): list exec"))
		// Held, reviewed and released -- but not merged: nothing marks
		// its pull request mergeable yet.
		released := func() bool {
			got := w.task(source)
			return linkTarget(got, model.LinkSecurityReviewTask) != "" && linkTarget(got, model.LinkSecurityHold) == "" &&
				got.AutoMerge
		}
		for i := 0; i < 8 && !released(); i++ {
			w.cycle()
		}
		if !released() || w.state(source) == model.StateClosed {
			t.Fatalf("the change was not released unmerged: state %s links %+v", w.state(source), w.task(source).Links)
		}
		w.pushTo(string(model.BranchName(source)))
		moved := strings.TrimSpace(runOutput(t, "", "git", "--git-dir", w.bare, "rev-parse",
			"refs/heads/"+string(model.BranchName(source))))
		if !w.until(10, w.closed(source)) {
			t.Fatalf("the moved change never merged: state %s links %+v", w.state(source), w.task(source).Links)
		}
		got := w.task(source)
		if n := len(w.reviewsOf(source)); n != 1 {
			t.Errorf("a docs-only CI repair after the clearance filed more reviews: %d, want 1", n)
		}
		if want := trustboundary.Version + "@" + moved; linkTarget(got, model.LinkTrustBoundaryCleared) != want {
			t.Errorf("trust-boundary-cleared = %q, want the clearance carried to %q",
				linkTarget(got, model.LinkTrustBoundaryCleared), want)
		}
	})
}
