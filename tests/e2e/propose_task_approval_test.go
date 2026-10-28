// TestProposedTaskWaitsForApprovalThenRunsThroughTheCLI is
// bwsalmon/agents#331's own scenario, the whole-pipeline sibling of
// pkg/orchestrator/finish_test.go's
// TestProcessResultFilesAProposedTaskIntoTheStore, which proves only the
// filing half in isolation. Here, an agent completing its own task (a
// real push, over a real local git server) also calls propose_task once
// (mcp's own contract, pkg/mcp/mock_tools.go's proposeTaskTool) in the
// same turn -- relayProposedTasks runs unconditionally regardless of how
// else a run ended (finish.go's own doc comment), so both effects land
// from one ProcessResult call. The resulting task must read as
// model.StateProposed (Approval nil) and never be dispatchable, even with
// a free slot sitting right there, until a human approves it -- which
// here means the real `grain approve` CLI verb (cmd/grain/main.go's
// cmdApprove), not a direct store write. Once approved, a following
// dispatch.Cycle must pick it up, and it runs a normal push/PR/merge/close
// cycle of its own, exactly like any other task.
//
// TestProposedChainStaysBlockedUntilTheTasksItNamedClose is the same
// pipeline carrying the other half of what propose_task offers: a
// depends_on. Approval and readiness are two different questions, and
// only the whole pipeline can ask the second -- pkg/orchestrator's own
// finish_test.go proves relayProposedTasks writes the
// model.LinkDependsOn links, but nothing there can say whether
// dispatch.Cycle then agrees with them.
//
// The CLI subprocess and this test's own dispatch/ProcessResult calls
// take turns owning storeDir rather than holding it open for the whole
// test, the same discipline TestCLICreatesTaskAgentOpensPRAndUserMergeClosesIt
// already holds to (cli_test.go's own withStore doc comment) -- not
// because the embedded SQLite store requires it, but because taking
// turns is what proves the handoff between the two writers goes through
// the store rather than through anything held in memory.
// Both tests reuse that file's rig (syncedSim, githubHostServer, a plain
// HostSandboxes root credentialed directly against the git/REST stand-in)
// rather than harness_test.go's gitproxy-fronted world, since gitproxy
// authorizes off one fixed *model.Store captured at build time and has no
// supported way to be repointed at a fresh store once the old one closes.
package e2e

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent/antigravity"
	"github.com/bwsalmon/grain/pkg/dispatch"
	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/github/githubsim"
	"github.com/bwsalmon/grain/pkg/mcp"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/ui"
)

// proposalRig is the outside world both tests in this file stand up: one
// bare upstream repo seeded with a commit, served over a single httptest
// server that answers REST calls from a githubsim and everything else
// from git's own http-backend (cli_test.go's githubHostServer).
//
// It is one struct rather than two copies of the same forty lines of git
// plumbing. Each test builds its own, though, rather than sharing one:
// every run in both pushes to the rig's repo and merges into its main,
// so two tests over one rig would be stepping on each other's branches.
type proposalRig struct {
	t          *testing.T
	owner      string
	repoName   string
	target     model.RepoRef
	remote     string
	githubHost string
	client     *github.RESTClient
}

func newProposalRig(t *testing.T) *proposalRig {
	t.Helper()
	const owner, repoName = "acme", "widgets"
	upstream := t.TempDir()
	bare := initBareRepo(t, filepath.Join(upstream, owner, repoName+".git"), "main")
	run(t, upstream, "git", "-C", bare, "config", "http.receivepack", "true")
	seedParent := t.TempDir()
	run(t, seedParent, "git", "clone", bare, "seed")
	seed := filepath.Join(seedParent, "seed")
	run(t, seed, "git", "config", "user.email", "seed@example.com")
	run(t, seed, "git", "config", "user.name", "seed")
	if err := os.WriteFile(filepath.Join(seed, "NOTES.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "add", "NOTES.md")
	run(t, seed, "git", "commit", "-q", "-m", "initial commit")
	run(t, seed, "git", "push", "origin", "main")

	sim := &syncedSim{sim: githubsim.New(owner, repoName, bare, "main")}
	githubHost := githubHostServer(t, sim, upstream)
	return &proposalRig{
		t:          t,
		owner:      owner,
		repoName:   repoName,
		target:     model.RepoRef{Owner: owner, Name: repoName},
		remote:     "http://" + githubHost + "/" + owner + "/" + repoName + ".git",
		githubHost: githubHost,
		client:     github.NewClient(sim, nil),
	}
}

// credentialedRoot is one run's own sandbox-stand-in directory. These
// tests drive runDispatch directly rather than through a Sandboxes
// backend, so they seed world.roots themselves; a real dispatch builds
// each of these as it goes.
//
// One per run, not one per test: a slot's root used to be the same
// directory every time, so the leftover "work" clone from the parent's
// run collided with the proposal's.
func (r *proposalRig) credentialedRoot() string {
	r.t.Helper()
	root := r.t.TempDir()
	if err := mcp.ConfigureGitCredentials(root, r.remote, "unused", mcp.GitIdentity{}); err != nil {
		r.t.Fatal(err)
	}
	return root
}

// mergeOnGitHub is a human clicking "Merge pull request": a real git
// merge of branch into main over the same local git server, plus a real
// HTTP PUT to the merge endpoint from a second, independent
// github.Client, exactly
// TestCLICreatesTaskAgentOpensPRAndUserMergeClosesIt's own step 3. It is
// what makes a task close here -- SyncPullRequests only ever reports
// what GitHub already did.
func (r *proposalRig) mergeOnGitHub(branch string, prNumber int) {
	r.t.Helper()
	mergeParent := r.t.TempDir()
	run(r.t, mergeParent, "git", "clone", r.remote, "merge")
	mergeWd := filepath.Join(mergeParent, "merge")
	run(r.t, mergeWd, "git", "config", "user.email", "github@example.com")
	run(r.t, mergeWd, "git", "config", "user.name", "github (simulated merge)")
	run(r.t, mergeWd, "git", "fetch", "origin", branch)
	run(r.t, mergeWd, "git", "checkout", "main")
	run(r.t, mergeWd, "git", "merge", "--no-ff", "origin/"+branch, "-m", "Merge "+branch)
	run(r.t, mergeWd, "git", "push", "origin", "main")

	userTransport := github.NewRealTransport(r.githubHost)
	userTransport.UseTLS = false
	userClient := github.NewClient(userTransport, nil)
	if err := userClient.MergePullRequest(r.owner, r.repoName, prNumber, ""); err != nil {
		r.t.Fatalf("submitting (merging) pull request %d: %v", prNumber, err)
	}
}

// pullRequestNumber is the pull request ProcessResult opened for taskID,
// read back off the task's own model.LinkFixes link -- the only place
// the number is recorded, since EnsurePullRequest allocates it.
func pullRequestNumber(w *world, taskID string) int {
	w.t.Helper()
	task, err := w.store.GetTask(w.ctx, taskID)
	if err != nil || task == nil {
		w.t.Fatalf("GetTask(%s): %v (nil=%v)", taskID, err, task == nil)
	}
	for _, l := range task.Links {
		if l.Kind == model.LinkFixes {
			ref, err := model.ParsePullRequestRef(l.Target)
			if err != nil {
				w.t.Fatalf("parsing pull request link %q: %v", l.Target, err)
			}
			return ref.Number
		}
	}
	w.t.Fatalf("%s has no pull request link: %+v", taskID, task.Links)
	return 0
}

// proposedBy is every task relayProposedTasks filed on parentID's
// behalf, keyed by title. Title is the handle because nothing else about
// a proposal is knowable ahead of time: its id comes from
// Store.NewTaskID at filing time, and the agent script never sees it.
func proposedBy(w *world, parentID string) map[string]model.Task {
	w.t.Helper()
	tasks, err := w.store.ListTasks(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	out := map[string]model.Task{}
	for _, tk := range tasks {
		if tk.ID == parentID {
			continue
		}
		for _, l := range tk.Links {
			if l.Kind == model.LinkProposedBy && l.Target == parentID {
				out[tk.Title] = tk
			}
		}
	}
	return out
}

// dependsOn is the targets of task's model.LinkDependsOn links, in the
// order they were filed -- which is the order the agent listed them in,
// since relayProposedTasks resolves depends_on entry by entry.
func dependsOn(task model.Task) []string {
	var out []string
	for _, l := range task.Links {
		if l.Kind == model.LinkDependsOn {
			out = append(out, l.Target)
		}
	}
	return out
}

// closedTasks is the map model.IsBlocked wants: every task that has
// closed. Built by asking the store about each task rather than kept as
// the test goes, so it says what the store believes rather than what the
// test assumed.
func closedTasks(w *world) map[string]bool {
	w.t.Helper()
	tasks, err := w.store.ListTasks(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	closed := map[string]bool{}
	for _, tk := range tasks {
		obs, err := w.store.GetObservation(w.ctx, tk.ID)
		if err != nil {
			w.t.Fatalf("GetObservation(%s): %v", tk.ID, err)
		}
		if obs != nil && obs.ClosedAt != nil {
			closed[tk.ID] = true
		}
	}
	return closed
}

// assertBlocked checks the two answers to "is anything still in front of
// this task" agree: the SQL views dispatch actually reads (task_ready,
// which drops whatever task_blocked counts an open blocker for) and the
// pure Go derivation (model.IsBlocked), the same two-ways-at-once
// discipline assertState holds to for state itself.
//
// It asserts the task is queued first because that is the only state in
// which the two are comparable at all: a proposed or running task is out
// of task_ready for reasons that have nothing to do with its links,
// which would make "not ready" agree with "blocked" for the wrong
// reason.
func assertBlocked(w *world, id string, want bool) {
	w.t.Helper()
	assertState(w, id, model.StateQueued, false)

	task, err := w.store.GetTask(w.ctx, id)
	if err != nil || task == nil {
		w.t.Fatalf("GetTask(%s): %v (nil=%v)", id, err, task == nil)
	}
	closed := closedTasks(w)
	if got := model.IsBlocked(*task, closed); got != want {
		w.t.Fatalf("model.IsBlocked(%s) = %v, want %v (links %+v, closed %v)", id, got, want, task.Links, closed)
	}

	ready, err := w.store.Ready(w.ctx)
	if err != nil {
		w.t.Fatalf("Ready: %v", err)
	}
	if inReady := slices.Contains(ready, id); inReady == want {
		w.t.Fatalf("%s in task_ready = %v, disagrees with model.IsBlocked's blocked=%v (ready = %v)",
			id, inReady, want, ready)
	}
}

// pushAndProposeScript is pushScript's own clone/commit/push, plus one
// propose_task call in the same turn -- what "completing its own task
// while also proposing a follow-up" looks like scripted through the real
// mocked tool, rather than the hand-built agent.ToolCall
// finish_test.go's own unit test uses.
func pushAndProposeScript(remote, branch, taskID, title, body string) []antigravity.Step {
	cmd := "git clone " + remote + " work && cd work && " +
		"git checkout -b " + branch + " && " +
		"echo 'change for " + taskID + "' >> NOTES.md && " +
		"git add NOTES.md && git commit -q -m 'agent commit for " + taskID + "' && " +
		"git push origin " + branch
	return []antigravity.Step{
		toolCall("run_command", map[string]any{"command": cmd}),
		toolCall("propose_task", map[string]any{"title": title, "body": body}),
		finalText("pushed " + branch + " and proposed a follow-up"),
	}
}

// The two proposals pushAndProposeChainScript makes, named here because
// the test matches them back by title -- see proposedBy.
const (
	formatProposalTitle = "follow-up: document NOTES.md's format"
	lintProposalTitle   = "follow-up: check every NOTES.md entry against that format"
)

// pushAndProposeChainScript is pushAndProposeScript with two proposals
// instead of one, chained the way propose_task's own schema asks for:
// the first waits on the task the run is doing now, and the second waits
// on both that task and the first proposal, named by the local `id` the
// tool hands out for exactly this.
//
// Two entries on the second call, not one, because they are resolved by
// different halves of relayProposedTasks: taskID names a task already in
// the store, "notes-format" names nothing but an earlier propose_task
// call in this same turn. A test that used only the local id would never
// notice the other path breaking.
func pushAndProposeChainScript(remote, branch, taskID string) []antigravity.Step {
	cmd := "git clone " + remote + " work && cd work && " +
		"git checkout -b " + branch + " && " +
		"echo 'change for " + taskID + "' >> NOTES.md && " +
		"git add NOTES.md && git commit -q -m 'agent commit for " + taskID + "' && " +
		"git push origin " + branch
	return []antigravity.Step{
		toolCall("run_command", map[string]any{"command": cmd}),
		toolCall("propose_task", map[string]any{
			"id":         "notes-format",
			"title":      formatProposalTitle,
			"body":       "a human should describe the format before more agents touch it",
			"depends_on": []any{taskID},
		}),
		toolCall("propose_task", map[string]any{
			"id":         "notes-lint",
			"title":      lintProposalTitle,
			"body":       "nothing to check entries against until the format is written down",
			"depends_on": []any{"notes-format", taskID},
		}),
		finalText("pushed " + branch + " and proposed two follow-ups"),
	}
}

func TestProposedTaskWaitsForApprovalThenRunsThroughTheCLI(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	bin := buildGrainCLI(t)
	rig := newProposalRig(t)

	storeDir := t.TempDir()
	const parentID = "task-parent-1"
	parentBranch := model.BranchName(parentID)

	clock := baseTime
	var proposalID string

	// Phase 1: file the parent task the way a human would, dispatch it,
	// and have its own run both push a branch and call propose_task.
	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		w := &world{t: t, store: store, ctx: ctx, roots: map[string]string{parentID + "-1": rig.credentialedRoot()}}
		fileIssue(w, parentID, human("alice"), rig.target)
		assertState(w, parentID, model.StateQueued, false)

		dispatches, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, clock)
		if err != nil {
			t.Fatal(err)
		}
		if len(dispatches) != 1 || dispatches[0].TaskID != parentID {
			t.Fatalf("Cycle dispatched %+v, want exactly %s", dispatches, parentID)
		}
		assertState(w, parentID, model.StateRunning, true)

		clock = clock.Add(time.Minute)
		script := pushAndProposeScript(rig.remote, parentBranch, parentID,
			"follow-up: document NOTES.md's format",
			"a human should describe the format before more agents touch it")
		result := w.runDispatch(dispatches[0], script, clock)
		if !pushedOK(result) {
			t.Fatalf("parent run did not push cleanly: %+v", result.ToolCalls)
		}
		if occ, _ := store.LiveRunCount(ctx); occ != 0 {
			t.Fatalf("occupied slots after finish = %v, want none", occ)
		}

		parentTask, err := store.GetTask(ctx, parentID)
		if err != nil || parentTask == nil {
			t.Fatalf("GetTask(%s): %v", parentID, err)
		}
		clock = clock.Add(time.Minute)
		if err := orchestrator.ProcessResult(ctx, store, rig.client, *parentTask, result, dispatches[0].RunID, clock); err != nil {
			t.Fatalf("ProcessResult: %v", err)
		}

		// The parent's own push still finishes normally -- relayProposedTasks
		// runs unconditionally before anything else, it does not replace the
		// rest of ProcessResult.
		assertState(w, parentID, model.StateAwaitingSubmit, false)

		proposals := proposedBy(w, parentID)
		if len(proposals) != 1 {
			t.Fatalf("tasks filed as proposed-by %s = %+v, want exactly one", parentID, proposals)
		}
		for _, p := range proposals {
			proposalID = p.ID
			if p.Approval != nil {
				t.Fatalf("proposal %s has Approval = %+v, want nil", p.ID, p.Approval)
			}
			if len(p.Links) != 1 || p.Links[0].Kind != model.LinkProposedBy || p.Links[0].Target != parentID {
				t.Fatalf("proposal %s links = %+v, want proposed-by %s", p.ID, p.Links, parentID)
			}
		}
		assertState(w, proposalID, model.StateProposed, false)

		// Not dispatchable, even with the slot the parent's own run just
		// freed up.
		clock = clock.Add(time.Minute)
		stillProposed, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, clock)
		if err != nil {
			t.Fatal(err)
		}
		if len(stillProposed) != 0 {
			t.Fatalf("Cycle dispatched a proposed, unapproved task: %+v", stillProposed)
		}
	})

	// Phase 2: approve it through the real CLI binary, not a direct store
	// write -- storeDir is untouched by anything else while this runs.
	approved := runCLIStore(t, bin, storeDir, "-json", "approve", proposalID)
	var approvedTask ui.Task
	if err := json.Unmarshal([]byte(approved), &approvedTask); err != nil {
		t.Fatalf("parsing grain approve -json output: %v\n%s", err, approved)
	}
	if approvedTask.ID != proposalID {
		t.Fatalf("grain approve responded about %q, want %q", approvedTask.ID, proposalID)
	}
	if approvedTask.State != model.StateQueued {
		t.Fatalf("state after grain approve = %q, want queued", approvedTask.State)
	}

	// Phase 3: now it dispatches, and runs a normal push/PR/merge/close
	// cycle of its own, exactly like any other task.
	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		w := &world{t: t, store: store, ctx: ctx, roots: map[string]string{proposalID + "-1": rig.credentialedRoot()}}

		clock = clock.Add(time.Minute)
		dispatches, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, clock)
		if err != nil {
			t.Fatal(err)
		}
		if len(dispatches) != 1 || dispatches[0].TaskID != proposalID {
			t.Fatalf("Cycle after approval dispatched %+v, want exactly %s", dispatches, proposalID)
		}

		proposalBranch := model.BranchName(proposalID)
		clock = clock.Add(time.Minute)
		result := w.runDispatch(dispatches[0], pushScript(rig.remote, proposalBranch, proposalID), clock)
		if !pushedOK(result) {
			t.Fatalf("approved task's run did not push cleanly: %+v", result.ToolCalls)
		}

		proposal, err := store.GetTask(ctx, proposalID)
		if err != nil || proposal == nil {
			t.Fatalf("GetTask(%s): %v", proposalID, err)
		}
		clock = clock.Add(time.Minute)
		if err := orchestrator.ProcessResult(ctx, store, rig.client, *proposal, result, dispatches[0].RunID, clock); err != nil {
			t.Fatalf("ProcessResult after approval: %v", err)
		}
		assertState(w, proposalID, model.StateAwaitingSubmit, false)

		rig.mergeOnGitHub(proposalBranch, pullRequestNumber(w, proposalID))

		clock = clock.Add(time.Minute)
		if err := orchestrator.SyncPullRequests(ctx, store, rig.client, clock); err != nil {
			t.Fatalf("SyncPullRequests: %v", err)
		}
		assertState(w, proposalID, model.StateClosed, false)
	})
}

// TestProposedChainStaysBlockedUntilTheTasksItNamedClose is the
// depends_on half of propose_task, end to end: one run proposes two
// follow-ups, the second of which names both the first (by its local id
// in the same turn) and the run's own task.
//
// pkg/orchestrator/finish_test.go already proves relayProposedTasks
// writes those two model.LinkDependsOn links, which is a different claim
// from the one that matters -- that dispatch then honours them. Only the
// whole pipeline can make it: task_blocked and task_ready are the views
// dispatch.Cycle actually reads, and nothing about a link's existence
// says they agree with model.IsBlocked about it.
//
// So the assertion here is about when each proposal runs, not what it
// links to. Approving both is deliberately not enough: a human saying
// yes answers a different question from "is the work this waits on
// done", and the whole point of filing a depends_on rather than leaving
// it in the body is that the second question keeps being asked after the
// first is settled. Each proposal becomes dispatchable exactly when
// everything it named has closed -- for real, by its pull request
// merging on GitHub -- and not one cycle earlier, even with a free slot
// sitting right there.
func TestProposedChainStaysBlockedUntilTheTasksItNamedClose(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	bin := buildGrainCLI(t)
	rig := newProposalRig(t)

	storeDir := t.TempDir()
	const parentID = "task-parent-1"
	parentBranch := model.BranchName(parentID)

	clock := baseTime
	var formatID, lintID string

	// Phase 1: the parent runs, pushes, and proposes the chain.
	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		w := &world{t: t, store: store, ctx: ctx, roots: map[string]string{parentID + "-1": rig.credentialedRoot()}}
		fileIssue(w, parentID, human("alice"), rig.target)

		dispatches, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, clock)
		if err != nil {
			t.Fatal(err)
		}
		if len(dispatches) != 1 || dispatches[0].TaskID != parentID {
			t.Fatalf("Cycle dispatched %+v, want exactly %s", dispatches, parentID)
		}

		clock = clock.Add(time.Minute)
		result := w.runDispatch(dispatches[0], pushAndProposeChainScript(rig.remote, parentBranch, parentID), clock)
		if !pushedOK(result) {
			t.Fatalf("parent run did not push cleanly: %+v", result.ToolCalls)
		}

		parentTask, err := store.GetTask(ctx, parentID)
		if err != nil || parentTask == nil {
			t.Fatalf("GetTask(%s): %v", parentID, err)
		}
		clock = clock.Add(time.Minute)
		if err := orchestrator.ProcessResult(ctx, store, rig.client, *parentTask, result, dispatches[0].RunID, clock); err != nil {
			t.Fatalf("ProcessResult: %v", err)
		}
		assertState(w, parentID, model.StateAwaitingSubmit, false)

		proposals := proposedBy(w, parentID)
		format, ok := proposals[formatProposalTitle]
		if !ok {
			t.Fatalf("no proposal titled %q was filed; got %v", formatProposalTitle, slices.Sorted(maps.Keys(proposals)))
		}
		lint, ok := proposals[lintProposalTitle]
		if !ok {
			t.Fatalf("no proposal titled %q was filed; got %v", lintProposalTitle, slices.Sorted(maps.Keys(proposals)))
		}
		formatID, lintID = format.ID, lint.ID

		// Both depends_on entries resolved, each by a different half of
		// relayProposedTasks: parentID named a task already in the store,
		// "notes-format" named nothing but the propose_task call before this
		// one. Order follows the order the agent listed them in.
		if got, want := dependsOn(format), []string{parentID}; !slices.Equal(got, want) {
			t.Fatalf("%s (%q) depends on %v, want %v", formatID, formatProposalTitle, got, want)
		}
		if got, want := dependsOn(lint), []string{formatID, parentID}; !slices.Equal(got, want) {
			t.Fatalf("%s (%q) depends on %v, want %v", lintID, lintProposalTitle, got, want)
		}
		assertState(w, formatID, model.StateProposed, false)
		assertState(w, lintID, model.StateProposed, false)
	})

	// Phase 2: a human approves both, through the real CLI binary. The CLI
	// answers with each task queued and blocked at once, which is the
	// distinction this whole test is about: approval is not readiness.
	for _, id := range []string{formatID, lintID} {
		out := runCLIStore(t, bin, storeDir, "-json", "approve", id)
		var approved ui.Task
		if err := json.Unmarshal([]byte(out), &approved); err != nil {
			t.Fatalf("parsing grain approve -json output: %v\n%s", err, out)
		}
		if approved.State != model.StateQueued {
			t.Fatalf("state after grain approve %s = %q, want queued", id, approved.State)
		}
		if !approved.Blocked {
			t.Fatalf("grain approve %s reported blocked = false, blockedBy = %v; "+
				"the parent it depends on has not closed", id, approved.BlockedBy)
		}
	}

	// Phase 3: approved, queued -- and still nothing dispatches, because
	// the parent has completed but not closed and both proposals named it.
	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		w := &world{t: t, store: store, ctx: ctx, roots: map[string]string{
			formatID + "-1": rig.credentialedRoot(),
			lintID + "-1":   rig.credentialedRoot(),
		}}

		assertBlocked(w, formatID, true)
		assertBlocked(w, lintID, true)

		clock = clock.Add(time.Minute)
		// Room for both, deliberately: "nothing dispatched" has to mean the
		// links held it back rather than the concurrency limit.
		none, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 2}, clock)
		if err != nil {
			t.Fatal(err)
		}
		if len(none) != 0 {
			t.Fatalf("Cycle dispatched %+v; both tasks depend on %s, which has not closed", none, parentID)
		}

		// The parent closes the only way anything closes here: its pull
		// request merges on GitHub, and SyncPullRequests notices.
		rig.mergeOnGitHub(parentBranch, pullRequestNumber(w, parentID))
		clock = clock.Add(time.Minute)
		if err := orchestrator.SyncPullRequests(ctx, store, rig.client, clock); err != nil {
			t.Fatalf("SyncPullRequests: %v", err)
		}
		assertState(w, parentID, model.StateClosed, false)

		// The first proposal's only blocker is gone, so it dispatches. The
		// second still does not, with the other slot free: it named the
		// first as well, and the first has not closed.
		assertBlocked(w, formatID, false)
		assertBlocked(w, lintID, true)

		clock = clock.Add(time.Minute)
		dispatches, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 2}, clock)
		if err != nil {
			t.Fatal(err)
		}
		if len(dispatches) != 1 || dispatches[0].TaskID != formatID {
			t.Fatalf("Cycle dispatched %+v, want exactly %s -- %s still waits on it", dispatches, formatID, lintID)
		}

		formatBranch := model.BranchName(formatID)
		clock = clock.Add(time.Minute)
		result := w.runDispatch(dispatches[0], pushScript(rig.remote, formatBranch, formatID), clock)
		if !pushedOK(result) {
			t.Fatalf("%s's run did not push cleanly: %+v", formatID, result.ToolCalls)
		}
		format, err := store.GetTask(ctx, formatID)
		if err != nil || format == nil {
			t.Fatalf("GetTask(%s): %v", formatID, err)
		}
		clock = clock.Add(time.Minute)
		if err := orchestrator.ProcessResult(ctx, store, rig.client, *format, result, dispatches[0].RunID, clock); err != nil {
			t.Fatalf("ProcessResult for %s: %v", formatID, err)
		}
		assertState(w, formatID, model.StateAwaitingSubmit, false)

		// A finished run is not a closed task, and a depends_on waits for
		// closed --
		// which is the whole reason IsBlocked is re-read every cycle rather
		// than settled when the link was written.
		assertBlocked(w, lintID, true)
		clock = clock.Add(time.Minute)
		stillBlocked, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 2}, clock)
		if err != nil {
			t.Fatal(err)
		}
		if len(stillBlocked) != 0 {
			t.Fatalf("Cycle dispatched %+v; %s depends on %s, which has completed but not closed",
				stillBlocked, lintID, formatID)
		}

		rig.mergeOnGitHub(formatBranch, pullRequestNumber(w, formatID))
		clock = clock.Add(time.Minute)
		if err := orchestrator.SyncPullRequests(ctx, store, rig.client, clock); err != nil {
			t.Fatalf("SyncPullRequests: %v", err)
		}
		assertState(w, formatID, model.StateClosed, false)

		// Everything the second proposal named has closed, so it runs, and
		// runs like any other task from there.
		assertBlocked(w, lintID, false)
		clock = clock.Add(time.Minute)
		last, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 2}, clock)
		if err != nil {
			t.Fatal(err)
		}
		if len(last) != 1 || last[0].TaskID != lintID {
			t.Fatalf("Cycle dispatched %+v, want exactly %s", last, lintID)
		}

		clock = clock.Add(time.Minute)
		lintResult := w.runDispatch(last[0], pushScript(rig.remote, model.BranchName(lintID), lintID), clock)
		if !pushedOK(lintResult) {
			t.Fatalf("%s's run did not push cleanly: %+v", lintID, lintResult.ToolCalls)
		}
		lint, err := store.GetTask(ctx, lintID)
		if err != nil || lint == nil {
			t.Fatalf("GetTask(%s): %v", lintID, err)
		}
		clock = clock.Add(time.Minute)
		if err := orchestrator.ProcessResult(ctx, store, rig.client, *lint, lintResult, last[0].RunID, clock); err != nil {
			t.Fatalf("ProcessResult for %s: %v", lintID, err)
		}
		assertState(w, lintID, model.StateAwaitingSubmit, false)
	})
}
