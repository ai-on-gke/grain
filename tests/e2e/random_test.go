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

// TestRandomizedClusterEndToEnd and TestRandomizedClusterLong are
// bwsalmon/agents#338's randomized counterpart to cli_test.go's one fixed
// scenario: rather than a human, an agent and GitHub each taking one
// scripted turn, every one of the three takes many turns, each one a
// random choice among whatever is actually valid for a task's current
// state -- file a task, approve a proposal, answer a parked question,
// close or reopen a task (the operator, driven through the real grain CLI
// binary, exactly as cli_test.go's own doc comment explains that choice);
// push, fail or ask a question (the agent, scripted the same way
// harness_test.go's pushScript/failScript/askScript already are -- see
// randomPushScript's own doc comment for the two ways this loop's version
// has to differ -- except the choice of which script is made live, once
// dispatch.Cycle -- inside a real orchestrator.RunCycle now, not by hand
// -- reveals which task and branch this turn is for); and merge a clean
// pull request (a real
// github.Client call against a real githubsim.Sim, the same double
// live_test.go and cli_test.go already use instead of the real network).
//
// This is not simulate_test.go over again. That file already randomizes
// several users filing issues across several rounds, but it calls
// dispatch.Cycle and the GitHub-sync stand-in's store.Observe calls
// directly -- it never touches the CLI binary a human actually runs, nor
// a real github.Client/githubsim pair. What only a test built the way
// this one is can catch: a CLI subprocess racing the orchestrator through
// a store that permits one writer at a time (withStore's own doc comment
// explains why opening and closing around every step is the point, not
// ceremony), and a merge that only really lands once githubsim's own
// mergeIntoBase moves real commits -- not just a PR's State field, the
// way e2e's own mergeBranchIntoDefault stands in for a human's click
// but githubsim.Sim.mergeIntoBase's own doc comment explains a fake that
// only flipped State would miss.
//
// The invariant checks inside runRandomizedCluster's own round loop below
// are what this whole exercise is actually checking: after every round, a
// slot RunCycle claimed must be free again (RunDispatch always calls
// FinishRun before RunCycle returns, so nothing here should ever see one
// still occupied -- catching exactly the "cluster blocks" failure mode
// bwsalmon/agents#338 asked to guard against), every branch a scripted
// agent turn actually confirmed pushed must still exist, every branch
// this loop merged must actually be an ancestor of main, and the task
// count the CLI itself reports must never drop -- nothing here ever
// deletes a task, so a lower count next round means one silently
// vanished.
//
// TestRandomizedClusterEndToEnd runs a short, fixed-seed version of this
// loop as part of the ordinary suite (`cd v2 && go test ./...`).
// TestRandomizedClusterLong is the same driver, run for much longer, with
// no seed fixed unless one is given -- see its own doc comment for how to
// run it by hand; it does nothing at all unless asked to, so `go test
// ./...` never pays for it.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

const (
	clusterRepoOwner = "acme"
	clusterRepoName  = "widgets"
	clusterMaxTasks  = 30
)

// TestRandomizedClusterEndToEnd is the short version bwsalmon/agents#338
// asked to add to the integration suite -- a fixed seed so a failure is
// always reproducible, and few enough rounds to stay well within what
// `go test ./...` should cost.
func TestRandomizedClusterEndToEnd(t *testing.T) {
	t.Parallel()
	runRandomizedCluster(t, clusterRunConfig{
		seed:   rand.NewPCG(338, 233),
		rounds: 25,
	})
}

// TestRandomizedClusterLong is the same driver run far longer -- the
// "run your random test for 20 minutes" half of bwsalmon/agents#338,
// kept out of the ordinary suite entirely (skipped unless GRAIN_LONG_SIM
// is set) since nothing about CI should pay minutes for a test whose
// whole point is to run far longer than CI ever should. Run it by hand:
//
//	GRAIN_LONG_SIM=1 go test ./tests/e2e/... -run TestRandomizedClusterLong -v -timeout 30m
//
// GRAIN_LONG_SIM_DURATION overrides the default 20 minutes (a Go
// duration string, e.g. "5m"); remember to raise -timeout to match, since
// go test's own default (10m) would kill a 20-minute run partway through
// and report it as a failure rather than a pass. GRAIN_LONG_SIM_SEED
// pins the two PCG seed words (comma-separated) to reproduce a specific
// failing run; left unset, the run picks its own from the clock and logs
// it, so any failure it finds is still reproducible after the fact.
func TestRandomizedClusterLong(t *testing.T) {
	t.Parallel()
	if os.Getenv("GRAIN_LONG_SIM") == "" {
		t.Skip("set GRAIN_LONG_SIM=1 to run the long randomized cluster simulation (~20m by default) -- see this test's own doc comment")
	}
	duration := 20 * time.Minute
	if v := os.Getenv("GRAIN_LONG_SIM_DURATION"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			t.Fatalf("GRAIN_LONG_SIM_DURATION: %v", err)
		}
		duration = d
	}
	seed1, seed2 := uint64(time.Now().UnixNano()), uint64(os.Getpid())
	if v := os.Getenv("GRAIN_LONG_SIM_SEED"); v != "" {
		a, b, ok := strings.Cut(v, ",")
		if !ok {
			t.Fatalf("GRAIN_LONG_SIM_SEED must be two comma-separated numbers, got %q", v)
		}
		s1, err1 := strconv.ParseUint(strings.TrimSpace(a), 10, 64)
		s2, err2 := strconv.ParseUint(strings.TrimSpace(b), 10, 64)
		if err1 != nil || err2 != nil {
			t.Fatalf("GRAIN_LONG_SIM_SEED: %q: not two numbers", v)
		}
		seed1, seed2 = s1, s2
	}
	t.Logf("random cluster simulation: seed=%d,%d duration=%s (reproduce with GRAIN_LONG_SIM_SEED=%d,%d)",
		seed1, seed2, duration, seed1, seed2)

	runRandomizedCluster(t, clusterRunConfig{
		seed:     rand.NewPCG(seed1, seed2),
		rounds:   1 << 30, // effectively unbounded -- the deadline below stops it
		deadline: time.Now().Add(duration),
	})
}

type clusterRunConfig struct {
	seed     *rand.PCG
	rounds   int
	deadline time.Time // zero means "no deadline, run every round"
}

// clusterCoverage records whether this run actually exercised every path
// it set out to -- the same discipline simulate_test.go's own final
// assertions hold to, so a seed that happened to avoid a whole category
// of outcome (say, every dispatch happening to fail) does not let this
// test pass vacuously.
type clusterCoverage struct {
	pushed, failed, asked, replied, closed, reopened, merged bool
}

// runRandomizedCluster is the whole driver, shared by the short and long
// tests above: build the real grain CLI once, wire a real orchestrator
// against a real githubsim.Sim standing in for GitHub, then run cfg.rounds
// rounds (or until cfg.deadline, whichever comes first) of the operator,
// the agent and GitHub each taking a randomly chosen valid turn, checking
// every invariant this package doc comment names after each one.
func runRandomizedCluster(t *testing.T, cfg clusterRunConfig) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	bin := buildGrainCLI(t)

	upstream := t.TempDir()
	bare := initBareRepo(t, filepath.Join(upstream, clusterRepoOwner, clusterRepoName+".git"), "main")
	run(t, upstream, "git", "-C", bare, "config", "http.receivepack", "true")
	seedParent := t.TempDir()
	run(t, seedParent, "git", "clone", bare, "seed")
	seedDir := filepath.Join(seedParent, "seed")
	run(t, seedDir, "git", "config", "user.email", "seed@example.com")
	run(t, seedDir, "git", "config", "user.name", "seed")
	if err := os.WriteFile(filepath.Join(seedDir, "NOTES.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seedDir, "git", "add", "NOTES.md")
	run(t, seedDir, "git", "commit", "-q", "-m", "initial commit")
	run(t, seedDir, "git", "push", "origin", "main")

	sim := &syncedSim{sim: githubsim.New(clusterRepoOwner, clusterRepoName, bare, "main")}
	githubHost := githubHostServer(t, sim, upstream)
	remote := "http://" + githubHost + "/" + clusterRepoOwner + "/" + clusterRepoName + ".git"

	storeDir := t.TempDir()
	sandboxes := credentialed(t, remote)
	const maxWorkers = 3

	client := github.NewClient(sim, nil)
	rng := rand.New(cfg.seed)

	// roundAttempts is every branch a scripted turn *decided* to push this
	// round, cleared at the top of every round; pushedBranches is only
	// ever grown from entries confirmed to have actually landed once the
	// round finishes -- see the confirmation loop below for why a
	// decision alone is not enough to trust forever.
	roundAttempts := map[string]string{}
	pushedBranches := map[string]string{} // taskID -> branch, once a push into roundAttempts was confirmed to have actually landed
	mergedBranches := map[string]string{} // taskID -> branch, once this loop actually merged it
	coverage := &clusterCoverage{}

	// genMu guards rng, roundAttempts and coverage below: orchestrator.
	// RunCycle now runs a tick's dispatches concurrently
	// (bwsalmon/agents#435), so the several randomGenerator instances
	// Framework hands out within one tick -- one per dispatch, all
	// sharing these same three closed-over values -- can genuinely call
	// GenerateContent from different goroutines at once, unlike every
	// scripted generator elsewhere in this package that is only ever
	// used by one run at a time.
	var genMu sync.Mutex
	deps := orchestrator.Deps{
		Client: client, Sandboxes: sandboxes, MaxWorkers: maxWorkers,
		Framework: func(context.Context, orchestrator.AgentChoice) (agent.Framework, error) {
			return antigravity.NewForTest(&randomGenerator{
				mu: &genMu, rng: rng, githubHost: githubHost, pushed: roundAttempts, coverage: coverage,
			}), nil
		},
	}

	clock := baseTime
	createdCount := 0
	lastTaskCount := 0
	round := 0
	for ; round < cfg.rounds; round++ {
		if !cfg.deadline.IsZero() && time.Now().After(cfg.deadline) {
			break
		}
		clock = clock.Add(time.Minute)

		tasksNow := listTasks(t, bin, storeDir)
		if len(tasksNow) < lastTaskCount {
			t.Fatalf("round %d: grain list reports %d tasks, down from %d last round -- a task was lost", round, len(tasksNow), lastTaskCount)
		}
		lastTaskCount = len(tasksNow)

		operatorRound(t, bin, storeDir, rng, tasksNow, &createdCount, coverage)

		clear(roundAttempts)
		withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
			deps.Store = store
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if err := orchestrator.RunCycle(cctx, deps, clock); err != nil {
				t.Fatalf("round %d: RunCycle: %v", round, err)
			}
			occ, err := store.LiveRunCount(ctx)
			if err != nil {
				t.Fatalf("round %d: LiveRunCount: %v", round, err)
			}
			if occ != 0 {
				t.Fatalf("round %d: %d slot(s) still occupied once RunCycle returned -- a dispatched run never finished, blocking its slot: %v", round, occ, occ)
			}
		})

		// A script that decided to push does not always manage to: the
		// local git-http-backend stand-in this loop clones and pushes
		// through (githubHostServer, harness_test.go's own gitHTTPBackend)
		// shells out to `git http-backend` per request rather than
		// holding a real git server's own long-lived state, and an
		// occasional transient failure there is a fact about this test's
		// own rig, not about grain -- the same way a real agent's real
		// push can transiently fail against real GitHub. Only a push
		// confirmed to have actually landed is promoted into
		// pushedBranches, which is the set this loop then holds to
		// forever: a branch grain itself dropped once really pushed
		// would be exactly the "cluster ... loses state" bwsalmon/
		// agents#338 asked this test to catch, and conflating that with
		// an agent's own turn simply not landing would hide it.
		for id, branch := range roundAttempts {
			if branchExistsInBare(t, bare, branch) {
				pushedBranches[id] = branch
			} else {
				t.Logf("round %d: task %s's scripted push did not land (a transient failure in the local git double, not grain) -- left queued for a later retry", round, id)
			}
		}

		githubRound(t, rng, sim, client, mergedBranches, coverage)

		for id, branch := range pushedBranches {
			if !branchExistsInBare(t, bare, branch) {
				t.Fatalf("round %d: task %s's branch %s was pushed and confirmed earlier but is now missing from the upstream repo", round, id, branch)
			}
		}
		for id, branch := range mergedBranches {
			if !branchIsAncestor(t, bare, "main", branch) {
				t.Fatalf("round %d: task %s's branch %s was merged earlier but main does not contain it", round, id, branch)
			}
		}
	}

	t.Logf("random cluster simulation: %d round(s), %d task(s) created", round, createdCount)
	if createdCount < 4 {
		t.Errorf("only %d task(s) got filed -- not enough rounds ran to exercise this scenario meaningfully", createdCount)
	}
	if !coverage.pushed {
		t.Error("scenario never exercised a clean push")
	}
	if !coverage.failed {
		t.Error("scenario never exercised a failed run")
	}
	if !coverage.asked {
		t.Error("scenario never exercised an agent question")
	}
	if !coverage.replied {
		t.Error("scenario never exercised a human reply to a parked question")
	}
	if !coverage.closed {
		t.Error("scenario never exercised an operator closing a task")
	}
	if !coverage.reopened {
		t.Error("scenario never exercised an operator reopening a task")
	}
	if !coverage.merged {
		t.Error("scenario never exercised a merge")
	}
}

// listTasks reads every task back through the real CLI, in a fresh
// subprocess -- how this whole test decides what a valid next move is,
// the same way a human operator deciding what to do next would run
// `grain list` first rather than remember every task's state by hand.
//
// -closed and -deferred because "every task" is what this loop means: a
// plain listing is what is in play (pkg/cli/list.go's hideable), and
// this one has to see the closed tasks in order to reopen one, and to
// hold its own "the count never drops" invariant across a round that
// closed something.
func listTasks(t *testing.T, bin, storeDir string) []ui.Task {
	t.Helper()
	out := runCLIStore(t, bin, storeDir, "-json", "list", "-closed", "-deferred")
	var tasks []ui.Task
	if err := json.Unmarshal([]byte(out), &tasks); err != nil {
		t.Fatalf("parsing grain list -json output: %v\n%s", err, out)
	}
	return tasks
}

// pickByState returns a random task ID among tasks whose state is state.
func pickByState(rng *rand.Rand, tasks []ui.Task, state model.State) (string, bool) {
	return pickWhere(rng, tasks, func(tk ui.Task) bool { return tk.State == state })
}

// pickNotState returns a random task ID among tasks whose state is not
// state.
func pickNotState(rng *rand.Rand, tasks []ui.Task, state model.State) (string, bool) {
	return pickWhere(rng, tasks, func(tk ui.Task) bool { return tk.State != state })
}

func pickWhere(rng *rand.Rand, tasks []ui.Task, ok func(ui.Task) bool) (string, bool) {
	var candidates []string
	for _, tk := range tasks {
		if ok(tk) {
			candidates = append(candidates, tk.ID)
		}
	}
	if len(candidates) == 0 {
		return "", false
	}
	return candidates[rng.IntN(len(candidates))], true
}

// operatorRound is the "operator" component's own turn: whatever a human
// at a shell running the real grain CLI could validly do to the tasks
// listed in tasks, chosen at random and gated on the state that makes
// each one valid in the first place (approving something not proposed,
// say, would not be wrong so much as meaningless -- Client.Approve's own
// doc comment: "approving an already-approved task is a no-op" -- so
// restricting to a task actually in that state is what makes this a
// choice among valid operations rather than a fuzz of arbitrary ones).
func operatorRound(t *testing.T, bin, storeDir string, rng *rand.Rand, tasks []ui.Task, createdCount *int, coverage *clusterCoverage) {
	t.Helper()

	if *createdCount < clusterMaxTasks && rng.Float64() < 0.5 {
		args := []string{
			"-json", "create",
			"-title", fmt.Sprintf("random cluster task %d", *createdCount),
			"-body", "exercise the randomized end-to-end cluster",
			"-repo", clusterRepoOwner + "/" + clusterRepoName,
		}
		if rng.Float64() < 0.7 {
			args = append(args, "-approve")
		}
		runCLIStore(t, bin, storeDir, args...)
		*createdCount++
	}

	if id, ok := pickByState(rng, tasks, model.StateProposed); ok && rng.Float64() < 0.6 {
		runCLIStore(t, bin, storeDir, "-json", "approve", id)
	}

	if id, ok := pickByState(rng, tasks, model.StateAwaitingReply); ok && rng.Float64() < 0.7 {
		runCLIStore(t, bin, storeDir, "-json", "comment", id, "here is the direction you asked for")
		coverage.replied = true
	}

	if id, ok := pickNotState(rng, tasks, model.StateClosed); ok && rng.Float64() < 0.15 {
		runCLIStore(t, bin, storeDir, "-json", "close", id)
		coverage.closed = true
	}

	if id, ok := pickByState(rng, tasks, model.StateClosed); ok && rng.Float64() < 0.15 {
		runCLIStore(t, bin, storeDir, "-json", "reopen", id)
		coverage.reopened = true
	}
}

// githubRound is "GitHub"'s own turn: submit (merge) a random pull
// request that is still open, standing in for a reviewer clicking
// "Merge pull request" -- real githubsim.Sim.mergeIntoBase performs the
// git side for real, the same as a human's own click would, rather than
// only flipping a State field (Sim.PullRequest's own doc comment).
func githubRound(t *testing.T, rng *rand.Rand, sim *syncedSim, client github.Client, merged map[string]string, coverage *clusterCoverage) {
	t.Helper()
	if rng.Float64() >= 0.35 {
		return
	}
	open := sim.openPullRequests()
	if len(open) == 0 {
		return
	}
	pr := open[rng.IntN(len(open))]
	if err := client.MergePullRequest(clusterRepoOwner, clusterRepoName, pr.Number, ""); err != nil {
		t.Fatalf("merging pull request #%d (%s): %v", pr.Number, pr.Head, err)
	}
	taskID := strings.TrimPrefix(pr.Head, "grain/task-")
	merged[taskID] = pr.Head
	coverage.merged = true
}

// branchIsAncestor reports whether ancestor's history is already
// contained in ref -- harness_test.go's own world.branchContains,
// restated as a free function since this file builds no world (see that
// file's package doc comment on why small helpers are duplicated per
// file here rather than shared).
func branchIsAncestor(t *testing.T, bare, ref, ancestor string) bool {
	t.Helper()
	cmd := exec.Command("git", "--git-dir", bare, "merge-base", "--is-ancestor", ancestor, ref)
	return cmd.Run() == nil
}

// promptRe pulls the repo and branch BuildPrompt's own fixed phrasing
// names back out of the prompt an agent turn receives -- the one channel
// randomGenerator has to learn which task and branch this dispatch is
// for, since Deps.Framework is handed only the framework a task names,
// never the task itself (pkg/orchestrator/cycle.go's own Deps doc
// comment: "a factory, not a shared instance").
var promptRe = regexp.MustCompile(`Work in (\S+)\. Push your change to a new branch named "([^"]+)"`)

// randomGenerator implements antigravity.Script by deciding, the first
// time it is asked, which script this dispatch's turn plays out --
// randomPushScript below, or harness_test.go's own failScript/askScript
// -- then playing that same script out one step per call, since one
// Framework (and so one randomGenerator) is only ever used for one run.
type randomGenerator struct {
	// mu guards every access below to rng, pushed and coverage: they are
	// shared across every randomGenerator a tick's Framework factory
	// hands out, one per concurrently dispatched task, unlike script and
	// calls below, which are this randomGenerator's own and never touched
	// by any other goroutine (one Framework, and so one randomGenerator,
	// is only ever used for one run -- see this type's own doc comment).
	mu         *sync.Mutex
	rng        *rand.Rand
	githubHost string
	pushed     map[string]string // taskID -> branch, written on a push decision (an attempt, not yet a confirmed one -- see runRandomizedCluster's roundAttempts)
	coverage   *clusterCoverage

	script []antigravity.Step
	calls  int
}

// Next implements antigravity.Script.
func (g *randomGenerator) Next(prompt string) (antigravity.Step, bool) {
	if g.script == nil {
		m := promptRe.FindStringSubmatch(prompt)
		if m == nil {
			// A dispatch with no repo and no branch in its prompt is the
			// deployment's own standing session -- the director, which
			// every fresh store gets (orchestrator.reconcileConsoleSessions)
			// and which this loop's operator round talks to like any
			// other task, since it is one. It answers in words: there is
			// nothing to clone and no branch to push, so a push script
			// would be scripting an impossibility.
			//
			// Not counted in coverage, and deliberately not randomized:
			// the sessions are not what this loop is measuring, and an
			// exchange that ends cleanly is the one shape that keeps the
			// session out of the way of everything that is.
			if !strings.Contains(prompt, "This task has no repo attached") {
				panic("random cluster: could not find the target repo/branch in the prompt: " + prompt)
			}
			g.script = commentOnlyScript("nothing to report about this deployment")
			return g.Next(prompt)
		}
		repo, branch := m[1], m[2]
		taskID := strings.TrimPrefix(branch, "grain/task-")
		remote := "http://" + g.githubHost + "/" + repo + ".git"

		g.mu.Lock()
		// clonePrefix folds in a fresh random token, not just taskID: a
		// task whose scripted push script gets run more than once (a
		// retry after an earlier attempt's clone itself failed, leaving a
		// same-named directory behind half-populated) must still get a
		// directory git is willing to clone into.
		clonePrefix := fmt.Sprintf("work-%s-%x", taskID, g.rng.Uint64())
		r := g.rng.Float64()
		switch {
		case r < 0.55:
			g.pushed[taskID] = branch
			g.coverage.pushed = true
		case r < 0.8:
			g.coverage.failed = true
		default:
			g.coverage.asked = true
		}
		g.mu.Unlock()

		switch {
		case r < 0.55:
			g.script = randomPushScript(remote, branch, taskID, clonePrefix)
		case r < 0.8:
			g.script = failScript("simulated failure")
		default:
			g.script = askScript("need direction on " + taskID)
		}
	}
	if g.calls >= len(g.script) {
		g.calls++
		return antigravity.Step{}, false
	}
	step := g.script[g.calls]
	g.calls++
	return step, true
}

// randomPushScript is harness_test.go's own pushScript, with two changes
// pushScript's own callers never needed. Every task writes its own
// uniquely named file instead of all of them appending to the same
// NOTES.md: two branches independently appending to one shared file can
// produce a real git merge conflict once both reach main (fine for
// pushScript's own callers, which each merge at most one branch), and a
// merge conflict here would only be testing git's own merge algorithm,
// not anything grain does -- every task touching its own path keeps
// every merge this loop performs conflict-free, so a merge failure stays
// a genuine invariant violation worth failing over. And every attempt
// clones into dir, a caller-chosen, freshly unique directory, rather than
// a fixed "work": orchestrator.HostSandboxes hands out one directory per
// *slot*, reused across dispatches rather than a fresh one per run, so a
// second attempt reusing a fixed name would otherwise find an earlier
// attempt's own clone (complete or, after a failed one, half-written)
// still sitting there and fail to clone into it.
func randomPushScript(remote, branch, taskID, dir string) []antigravity.Step {
	file := "task-" + taskID + ".md"
	cmd := "git clone " + remote + " " + dir + " && cd " + dir + " && " +
		"git checkout -b " + branch + " && " +
		"echo 'change for " + taskID + "' > " + file + " && " +
		"git add " + file + " && git commit -q -m 'agent commit for " + taskID + "' && " +
		"git push origin " + branch
	return []antigravity.Step{
		toolCall("run_command", map[string]any{"command": cmd}),
		finalText("pushed " + branch),
	}
}

// openPullRequests returns every pull request sim currently reads as
// still open, safe to call from this file's own goroutine even though
// sim is also being driven by the CLI subprocess's and the orchestrator's
// own HTTP round trips -- syncedSim's own doc comment explains why a bare
// *githubsim.Sim is not safe to read directly here.
func (s *syncedSim) openPullRequests() []githubsim.PullRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []githubsim.PullRequest
	for _, pr := range s.sim.PullRequests {
		if pr.State == "open" {
			out = append(out, pr)
		}
	}
	return out
}
