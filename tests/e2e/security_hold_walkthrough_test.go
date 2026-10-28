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
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/trustboundary"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// The security-hold walkthrough: docs/test-plans/2026-10-01.md §1, run
// against the real orchestrator (RunCycle: dispatch, finishWithPullRequest's
// classification, SyncSecurityHolds, the merge queue in SyncPullRequests)
// over a real git server and githubsim. Only GitHub (githubsim) and the
// model vendor (a scripted antigravity run) are stood in for. The report is
// docs/test-runs/2026-10-01-security-hold-escalated-proposals.md.

// holdWorld is one repository, one store and one orchestrator, with a
// scripted agent whose run is chosen per task by script.
type holdWorld struct {
	t        *testing.T
	ctx      context.Context
	store    *model.Store
	ui       *ui.Client
	sim      *syncedSim
	upstream string
	bare     string
	remote   string
	deps     orchestrator.Deps
	now      time.Time
	dbDir    string

	mu     sync.Mutex
	script func(task model.Task) []antigravity.Step
}

var runningAsTask = regexp.MustCompile(`You are running as task (\S+?)\.`)

// failingFilesSim is GitHub with a pull-request file listing that errors.
type failingFilesSim struct{ inner github.Transport }

var pullFilesPath = regexp.MustCompile(`^/repos/[^/]+/[^/]+/pulls/\d+/files`)

func (f failingFilesSim) Request(method, path string, headers map[string]string, body []byte) (github.ApiResponse, error) {
	if pullFilesPath.MatchString(path) {
		return github.ApiResponse{Status: http.StatusBadGateway, Body: []byte(`{"message":"upstream timeout"}`)}, nil
	}
	return f.inner.Request(method, path, headers, body)
}

func newHoldWorld(t *testing.T, failFiles bool) *holdWorld {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	const owner, repoName = "acme", "widgets"
	w := &holdWorld{t: t, ctx: context.Background(), now: baseTime}
	w.upstream = t.TempDir()
	w.bare = initBareRepo(t, filepath.Join(w.upstream, owner, repoName+".git"), "main")
	run(t, w.upstream, "git", "-C", w.bare, "config", "http.receivepack", "true")
	seedParent := t.TempDir()
	run(t, seedParent, "git", "clone", w.bare, "seed")
	seed := filepath.Join(seedParent, "seed")
	run(t, seed, "git", "config", "user.email", "seed@example.com")
	run(t, seed, "git", "config", "user.name", "seed")
	for path, body := range map[string]string{
		"NOTES.md":                   "seed\n",
		"docs/guide.md":              "# guide\n",
		"pkg/relay/relay.go":         "package relay\n\n// Relay forwards a sandbox's traffic.\nfunc Relay() {}\n",
		"pkg/gitproxy/proxy.go":      "package gitproxy\n",
		".grain/baselines/lint.json": "{}\n",
	} {
		mustWriteFile(t, filepath.Join(seed, path), body)
	}
	run(t, seed, "git", "add", "-A")
	run(t, seed, "git", "commit", "-q", "-m", "initial commit")
	run(t, seed, "git", "push", "origin", "main")

	w.sim = &syncedSim{sim: githubsim.New(owner, repoName, w.bare, "main")}
	var transport github.Transport = w.sim
	if failFiles {
		transport = failingFilesSim{inner: w.sim}
	}
	host := githubHostServer(t, transport, w.upstream)
	w.remote = "http://" + host + "/" + owner + "/" + repoName + ".git"

	w.dbDir = t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(w.dbDir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	w.store = model.New(db)
	if err := w.store.Init(w.ctx); err != nil {
		t.Fatal(err)
	}
	w.ui = ui.NewClient(ui.Config{Actor: ui.DefaultActor("operator"), Capabilities: ui.OfferedCapabilities()}, w.store)
	w.deps = orchestrator.Deps{
		Store: w.store, Client: github.NewClient(transport, nil),
		Sandboxes: credentialed(t, w.remote), MaxWorkers: 2,
		Framework: func(context.Context, orchestrator.AgentChoice) (agent.Framework, error) {
			var steps []antigravity.Step
			return antigravity.NewForTest(antigravity.ScriptFunc(func(prompt string) (antigravity.Step, bool) {
				if steps == nil {
					steps = []antigravity.Step{finalText("nothing scripted")}
					if m := runningAsTask.FindStringSubmatch(prompt); m != nil {
						if task, err := w.store.GetTask(context.Background(), m[1]); err == nil && task != nil {
							w.mu.Lock()
							steps = w.script(*task)
							w.mu.Unlock()
						}
					}
				}
				if len(steps) == 0 {
					return antigravity.Step{}, false
				}
				s := steps[0]
				steps = steps[1:]
				return s, true
			})), nil
		},
	}
	w.script = func(model.Task) []antigravity.Step { return []antigravity.Step{finalText("no findings")} }
	return w
}

func (w *holdWorld) setScript(fn func(task model.Task) []antigravity.Step) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.script = fn
}

// create files an approved task through the same ui.Client the UI's
// create form calls.
func (w *holdWorld) create(title string, autoMerge bool, caps []string) model.Task {
	w.t.Helper()
	req := ui.CreateTaskRequest{Title: title, Description: title, Repo: "acme/widgets", AutoMerge: autoMerge}
	if caps != nil {
		req.Capabilities = &caps
	}
	created, err := w.ui.CreateTask(w.ctx, req)
	if err != nil {
		w.t.Fatalf("CreateTask: %v", err)
	}
	if created.State == model.StateProposed {
		if err := w.ui.Approve(w.ctx, created.ID); err != nil {
			w.t.Fatal(err)
		}
	}
	return w.task(created.ID)
}

func (w *holdWorld) task(id string) model.Task {
	w.t.Helper()
	got, err := w.store.GetTask(w.ctx, id)
	if err != nil || got == nil {
		w.t.Fatalf("reading %s: %v", id, err)
	}
	return *got
}

func (w *holdWorld) state(id string) model.State {
	w.t.Helper()
	st, err := w.store.State(w.ctx, id)
	if err != nil {
		w.t.Fatal(err)
	}
	return st
}

func (w *holdWorld) cycle() {
	w.t.Helper()
	w.now = w.now.Add(time.Minute)
	if err := orchestrator.RunCycle(w.ctx, w.deps, w.now); err != nil {
		w.t.Fatalf("RunCycle: %v", err)
	}
}

// cycles runs up to n cycles, stopping as soon as done reports true.
func (w *holdWorld) cycles(n int, done func() bool) bool {
	w.t.Helper()
	for i := 0; i < n; i++ {
		w.cycle()
		if done() {
			return true
		}
	}
	return false
}

func (w *holdWorld) markAllClean() {
	w.sim.mu.Lock()
	defer w.sim.mu.Unlock()
	clean := true
	for i := range w.sim.sim.PullRequests {
		w.sim.sim.PullRequests[i].Mergeable = &clean
	}
}

func (w *holdWorld) merged(branch string) bool {
	cmd := exec.Command("git", "--git-dir", w.bare, "merge-base", "--is-ancestor", "refs/heads/"+branch, "main")
	return w.branchExists(branch) && cmd.Run() == nil
}

func (w *holdWorld) branchExists(branch string) bool {
	return exec.Command("git", "--git-dir", w.bare, "rev-parse", "--verify", "-q", "refs/heads/"+branch).Run() == nil
}

func (w *holdWorld) comments(id string) string {
	w.t.Helper()
	cs, err := w.store.Comments(w.ctx, id)
	if err != nil {
		w.t.Fatal(err)
	}
	var b strings.Builder
	for _, c := range cs {
		b.WriteString(c.Body)
		b.WriteString("\n")
	}
	return b.String()
}

func (w *holdWorld) proposalsOf(parent string) []model.Task {
	w.t.Helper()
	all, err := w.store.ListTasks(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	var out []model.Task
	for _, t := range all {
		if o := t.Origin.Attribution.OnBehalfOf; (o != nil && o.ID == parent) || linkTarget(t, model.LinkProposedBy) == parent {
			out = append(out, t)
		}
	}
	return out
}

func holds(task model.Task, capability string) bool {
	for _, g := range task.Grants {
		if g.Capability == capability {
			return true
		}
	}
	return false
}

func linkTarget(task model.Task, kind model.LinkKind) string {
	for _, l := range task.Links {
		if l.Kind == kind {
			return l.Target
		}
	}
	return ""
}

// changeScript clones the repository, runs edit (a shell fragment) on a
// fresh task branch, and pushes it.
func (w *holdWorld) changeScript(task model.Task, edit string) []antigravity.Step {
	branch := model.BranchName(task.ID)
	cmd := "git clone " + w.remote + " work && cd work && git checkout -b " + branch + " && " +
		edit + " && git add -A && git commit -q -m 'agent commit for " + task.ID + "' && git push origin " + branch
	return []antigravity.Step{toolCall("run_command", map[string]any{"command": cmd}), finalText("pushed " + branch)}
}

// workThenReview is the script for a scenario: the task under test makes
// edit, and its security review does review.
func (w *holdWorld) workThenReview(subject string, edit string, review func(model.Task) []antigravity.Step) {
	w.setScript(func(task model.Task) []antigravity.Step {
		if task.ID == subject {
			return w.changeScript(task, edit)
		}
		if task.Origin.Reason == model.ReasonReview && review != nil {
			return review(task)
		}
		return []antigravity.Step{finalText("no findings")}
	})
}

// heldOn dispatches subject and returns its security review's id, having
// checked the hold the plan describes.
func (w *holdWorld) heldOn(subject model.Task, path string, releasable bool) string {
	w.t.Helper()
	w.cycle()
	if st := w.state(subject.ID); st != model.StateAwaitingSubmit {
		w.t.Fatalf("%s state after its run = %q, want awaiting_submit (comments: %s)", subject.ID, st, w.comments(subject.ID))
	}
	got := w.task(subject.ID)
	if got.AutoMerge {
		w.t.Fatalf("%s kept auto-merge with a diff on %s", subject.ID, path)
	}
	hold := linkTarget(got, model.LinkSecurityHold)
	if releasable && hold != trustboundary.Version {
		w.t.Fatalf("security-hold link = %q, want %q; links %+v", hold, trustboundary.Version, got.Links)
	}
	if !releasable && hold != "" {
		w.t.Fatalf("a hold only a human may release carries a releasable hold link: %+v", got.Links)
	}
	reviewID := linkTarget(got, model.LinkSecurityReviewTask)
	if reviewID == "" {
		w.t.Fatalf("no security-review-task link: %+v", got.Links)
	}
	review := w.task(reviewID)
	if review.Origin.Reason != model.ReasonReview || review.Base != model.BranchName(subject.ID) ||
		!strings.HasPrefix(review.Title, "Security review: ") {
		w.t.Fatalf("security review is not stacked on the branch: %+v", review)
	}
	if path != "" && !strings.Contains(review.Body, "`"+path+"`") {
		w.t.Errorf("security review body does not name %s:\n%s", path, review.Body)
	}
	if c := w.comments(subject.ID); !strings.Contains(c, "trust boundary") || (path != "" && !strings.Contains(c, "`"+path+"`")) {
		w.t.Errorf("no comment naming the matched paths on %s: %s", subject.ID, c)
	}
	return reviewID
}

// notMergedWhileHeld checks that a clean pull request, held, is not
// batched or merged by the merge queue.
func (w *holdWorld) notMergedWhileHeld(subject model.Task, n int) {
	w.t.Helper()
	w.markAllClean()
	for i := 0; i < n; i++ {
		w.cycle()
		w.markAllClean()
		if w.merged(model.BranchName(subject.ID)) {
			w.t.Fatalf("%s merged while on a security hold", subject.ID)
		}
	}
}

func TestSecurityHoldWalkthrough(t *testing.T) {
	t.Parallel()

	t.Run("credential proxy change is held, then released by a clean review and merges", func(t *testing.T) {
		t.Parallel()
		w := newHoldWorld(t, false)
		task := w.create("touch the git proxy", true, nil)
		w.workThenReview(task.ID, "echo '// tweak' >> pkg/gitproxy/proxy.go", nil)
		// The review is dispatched in the cycle after the hold is filed, so
		// the second cycle here runs it; hold it off by keeping the PR
		// unmergeable until the hold has been seen in full.
		reviewID := w.heldOn(task, "pkg/gitproxy/proxy.go", true)
		w.markAllClean()
		if !w.cycles(6, func() bool { w.markAllClean(); return w.merged(model.BranchName(task.ID)) }) {
			t.Fatalf("a clean review did not release the hold: automerge=%v links=%+v review state=%s comments=%s",
				w.task(task.ID).AutoMerge, w.task(task.ID).Links, w.state(reviewID), w.comments(task.ID))
		}
		if !strings.Contains(w.comments(task.ID), "finished without findings") {
			t.Errorf("no release comment: %s", w.comments(task.ID))
		}
	})

	t.Run("held change is not batched by the merge queue while its review is pending", func(t *testing.T) {
		t.Parallel()
		w := newHoldWorld(t, false)
		task := w.create("touch the git proxy", true, nil)
		// A review that never finishes: it asks a question and parks.
		w.workThenReview(task.ID, "echo '// tweak' >> pkg/gitproxy/proxy.go",
			func(model.Task) []antigravity.Step { return askScript("is this route meant to be public?") })
		w.heldOn(task, "pkg/gitproxy/proxy.go", true)
		w.notMergedWhileHeld(task, 4)
		if w.task(task.ID).AutoMerge {
			t.Fatal("auto-merge came back while the review was parked")
		}
	})

	t.Run("review findings drop the hold and wait for a human", func(t *testing.T) {
		t.Parallel()
		w := newHoldWorld(t, false)
		task := w.create("touch the git proxy", true, nil)
		w.workThenReview(task.ID, "echo '// tweak' >> pkg/gitproxy/proxy.go", func(model.Task) []antigravity.Step {
			return []antigravity.Step{
				toolCall("add_review_comment", map[string]any{
					"body": "this widens what the proxy forwards", "path": "pkg/gitproxy/proxy.go", "line": float64(2)}),
				finalText("one finding"),
			}
		})
		reviewID := w.heldOn(task, "pkg/gitproxy/proxy.go", true)
		w.notMergedWhileHeld(task, 4)
		got := w.task(task.ID)
		if got.AutoMerge || linkTarget(got, model.LinkSecurityHold) != "" {
			t.Fatalf("findings did not leave the change for a human: automerge=%v links=%+v review=%s",
				got.AutoMerge, got.Links, w.state(reviewID))
		}
	})

	// The reviewer fixes what it finds and the change goes through
	// (task 5008): it pushes its fix to the held change's own branch
	// (task 984) and opens no pull request of its own. The fix is on the
	// trust boundary, so a second review reads it before the hold is
	// released -- and still no Submit is needed.
	t.Run("a review that fixes what it found on the change's branch releases the hold", func(t *testing.T) {
		t.Parallel()
		w := newHoldWorld(t, false)
		task := w.create("touch the git proxy", true, nil)
		var mu sync.Mutex
		fixer := ""
		w.workThenReview(task.ID, "echo '// tweak' >> pkg/gitproxy/proxy.go", func(review model.Task) []antigravity.Step {
			mu.Lock()
			defer mu.Unlock()
			if fixer != "" && fixer != review.ID {
				return []antigravity.Step{finalText("read the reviewer's fix; nothing more to change")}
			}
			fixer = review.ID
			branch := review.BranchName()
			cmd := "git clone " + w.remote + " work && cd work && git checkout " + branch +
				" && echo '// reviewer fix' >> pkg/gitproxy/proxy.go && git commit -qam 'review fix' && git push origin " + branch
			return []antigravity.Step{toolCall("run_command", map[string]any{"command": cmd}), finalText("fixed it")}
		})
		reviewID := w.heldOn(task, "pkg/gitproxy/proxy.go", true)
		if got := w.task(reviewID).BranchName(); got != model.BranchName(task.ID) {
			t.Fatalf("the security review works on %q, want the held change's own branch %q", got, model.BranchName(task.ID))
		}
		w.markAllClean()
		if !w.cycles(16, func() bool { w.markAllClean(); return w.merged(model.BranchName(task.ID)) }) {
			t.Fatalf("a review that fixed what it found did not let the change through: automerge=%v links=%+v review=%s comments=%s",
				w.task(task.ID).AutoMerge, w.task(task.ID).Links, w.state(reviewID), w.comments(task.ID))
		}
		landed, err := exec.Command("git", "--git-dir", w.bare, "show", "main:pkg/gitproxy/proxy.go").Output()
		if err != nil || !strings.Contains(string(landed), "// reviewer fix") {
			t.Errorf("the change merged without the review's fix (%v):\n%s", err, landed)
		}
		if w.branchExists(model.BranchName(reviewID)) {
			t.Error("the review pushed a branch of its own")
		}
		if linkTarget(w.task(reviewID), model.LinkFixes) != "" {
			t.Error("the review opened a pull request of its own")
		}
		// The fix is on the trust boundary itself, so it is not cleared on
		// the reviewer's say-so: a second review reads it first.
		if !strings.Contains(w.comments(task.ID), "touch grain's trust boundary themselves") {
			t.Errorf("no comment saying the reviewer's own fix is reviewed again: %s", w.comments(task.ID))
		}
	})

	t.Run("a human Submit overrides a pending hold", func(t *testing.T) {
		t.Parallel()
		w := newHoldWorld(t, false)
		task := w.create("touch the git proxy", true, nil)
		w.workThenReview(task.ID, "echo '// tweak' >> pkg/gitproxy/proxy.go",
			func(model.Task) []antigravity.Step { return askScript("still reading") })
		w.heldOn(task, "pkg/gitproxy/proxy.go", true)
		if err := w.ui.Submit(w.ctx, task.ID); err != nil {
			t.Fatalf("Submit: %v", err)
		}
		if !w.cycles(4, func() bool { w.markAllClean(); return w.merged(model.BranchName(task.ID)) }) {
			t.Fatalf("Submit did not merge a held change: automerge=%v links=%+v", w.task(task.ID).AutoMerge, w.task(task.ID).Links)
		}
	})

	t.Run("baselines never auto-merge even after a clean review", func(t *testing.T) {
		t.Parallel()
		w := newHoldWorld(t, false)
		task := w.create("refresh the lint baseline", true, nil)
		w.workThenReview(task.ID, "echo '{\"x\":1}' > .grain/baselines/lint.json", nil)
		reviewID := w.heldOn(task, ".grain/baselines/lint.json", false)
		w.notMergedWhileHeld(task, 5)
		if st := w.state(reviewID); st != model.StateCompleted && st != model.StateClosed {
			t.Fatalf("the baseline's review did not finish (state %s), so this proved nothing", st)
		}
		if w.task(task.ID).AutoMerge {
			t.Fatal("a clean review gave a .grain/baselines/ change auto-merge back")
		}
	})

	t.Run("docs-only control change merges with no review", func(t *testing.T) {
		t.Parallel()
		w := newHoldWorld(t, false)
		task := w.create("fix a typo in the guide", true, nil)
		w.workThenReview(task.ID, "echo 'typo fixed' >> docs/guide.md", nil)
		w.cycle()
		if got := w.task(task.ID); !got.AutoMerge || linkTarget(got, model.LinkSecurityReviewTask) != "" {
			t.Fatalf("a docs-only change was held: automerge=%v links=%+v", got.AutoMerge, got.Links)
		}
		if !w.cycles(3, func() bool { w.markAllClean(); return w.merged(model.BranchName(task.ID)) }) {
			t.Fatal("the docs-only control change never merged")
		}
	})

	t.Run("a rename out of pkg/relay is held", func(t *testing.T) {
		t.Parallel()
		w := newHoldWorld(t, false)
		task := w.create("move the relay", true, nil)
		w.workThenReview(task.ID, "mkdir -p internal && git mv pkg/relay/relay.go internal/relay.go",
			func(model.Task) []antigravity.Step { return askScript("still reading") })
		w.heldOn(task, "pkg/relay/relay.go", true)
		w.notMergedWhileHeld(task, 2)
	})

	t.Run("a pull request whose files cannot be listed is held", func(t *testing.T) {
		t.Parallel()
		w := newHoldWorld(t, true)
		task := w.create("fix a typo in the guide", true, nil)
		w.workThenReview(task.ID, "echo 'typo fixed' >> docs/guide.md", nil)
		reviewID := w.heldOn(task, "", false)
		w.notMergedWhileHeld(task, 4)
		if !strings.Contains(w.task(reviewID).Body, "could not read the full list") {
			t.Errorf("the review is not told the listing failed:\n%s", w.task(reviewID).Body)
		}
	})
}

// TestCapabilityEscalatedProposalsWalkthrough is §1's proposal half:
// propose_task naming a capability the proposer does not hold is filed for
// a person, from every path that would otherwise approve it.
func TestCapabilityEscalatedProposalsWalkthrough(t *testing.T) {
	t.Parallel()

	propose := func(caps ...string) antigravity.Step {
		args := map[string]any{"title": fmt.Sprintf("follow-up needing %v", caps), "body": "split out"}
		if len(caps) > 0 {
			list := make([]any, len(caps))
			for i, c := range caps {
				list[i] = c
			}
			args["capabilities"] = list
		}
		return toolCall("propose_task", args)
	}
	check := func(t *testing.T, w *holdWorld, parent string) {
		t.Helper()
		proposals := w.proposalsOf(parent)
		if len(proposals) != 2 {
			t.Fatalf("%s filed %d proposals, want 2", parent, len(proposals))
		}
		for _, p := range proposals {
			escalated := strings.Contains(p.Title, "gcp-key")
			st := w.state(p.ID)
			noted := strings.Contains(p.Body, model.CapabilityEscalationNote(parent, []string{"gcp-key"}))
			switch {
			case escalated && (st != model.StateProposed || !noted):
				t.Errorf("escalated proposal %q: state=%s noted=%v body=%q", p.Title, st, noted, p.Body)
			case escalated && !holds(p, "gcp-key"):
				t.Errorf("escalated proposal %q was filed without the capability it asked for: %+v", p.Title, p.Grants)
			case !escalated && (st != model.StateQueued || strings.Contains(p.Body, "grain: this proposal asked for")):
				t.Errorf("plain proposal %q: state=%s body=%q", p.Title, st, p.Body)
			}
		}
	}

	t.Run("from an auto-submitting parent at the end of its run", func(t *testing.T) {
		t.Parallel()
		w := newHoldWorld(t, false)
		parent := w.create("auto-merging parent", true, []string{})
		if holds(parent, "gcp-key") {
			t.Fatalf("the parent holds gcp-key already: %+v", parent.Grants)
		}
		w.setScript(func(task model.Task) []antigravity.Step {
			if task.ID != parent.ID {
				return []antigravity.Step{finalText("nothing to do")}
			}
			steps := w.changeScript(task, "echo 'typo' >> docs/guide.md")
			return append([]antigravity.Step{propose("gcp-key"), propose()}, steps...)
		})
		w.cycle()
		check(t, w, parent.ID)
	})

	t.Run("from a console session trusted to queue its own work, filed live", func(t *testing.T) {
		t.Parallel()
		w := newHoldWorld(t, false)
		cfg := model.DefaultConfig()
		sessions := model.DefaultConsoleSessions()
		sessions[0].TasksApproved = true
		cfg.ConsoleSessions = sessions
		if err := w.store.PutConfig(w.ctx, cfg); err != nil {
			t.Fatal(err)
		}
		session := w.create("Director", false, []string{})
		if err := w.store.UpdateTask(w.ctx, session.ID, func(t *model.Task) error {
			t.Tags = append(t.Tags, model.ConsoleSessionTag(sessions[0].ID))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		for _, caps := range [][]string{{"gcp-key"}, {}} {
			if _, err := w.ui.CreateTask(w.ctx, ui.CreateTaskRequest{
				Title: fmt.Sprintf("follow-up needing %v", caps), Description: "split out", Repo: "acme/widgets",
				ProposedBy: session.ID, Capabilities: &caps,
			}); err != nil {
				t.Fatal(err)
			}
		}
		check(t, w, session.ID)
	})

	t.Run("from a console session trusted to queue its own work, relayed at the end of its run", func(t *testing.T) {
		t.Parallel()
		w := newHoldWorld(t, false)
		cfg := model.DefaultConfig()
		sessions := model.DefaultConsoleSessions()
		sessions[0].TasksApproved = true
		cfg.ConsoleSessions = sessions
		if err := w.store.PutConfig(w.ctx, cfg); err != nil {
			t.Fatal(err)
		}
		session := w.create("Director (batch)", false, []string{})
		if err := w.store.UpdateTask(w.ctx, session.ID, func(t *model.Task) error {
			t.Tags = append(t.Tags, model.ConsoleSessionTag(sessions[0].ID))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		w.setScript(func(task model.Task) []antigravity.Step {
			if task.ID != session.ID {
				return []antigravity.Step{finalText("nothing to do")}
			}
			return []antigravity.Step{propose("gcp-key"), propose(), finalText("filed two follow-ups")}
		})
		// The session reconciler adopts the tagged task and parks it
		// awaiting a message, as a standing chat is; a person's message is
		// what puts it back in the queue.
		w.cycle()
		if err := w.ui.AddComment(w.ctx, session.ID, "split the two follow-ups out", nil); err != nil {
			t.Fatal(err)
		}
		w.cycles(3, func() bool { return len(w.proposalsOf(session.ID)) >= 2 })
		check(t, w, session.ID)
	})
}
