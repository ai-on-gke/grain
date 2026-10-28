package e2e

// The randomized counterpart bwsalmon/agents#233 asked for alongside
// e2e_test.go's fixed scenarios: several users filing issues against
// several repos over many rounds, dispatch.Cycle deciding what runs when,
// real gemini agent turns actually pushing branches through a real
// gitproxy, and a GitHub-sync stand-in observing completion, questions,
// and merges -- all against a real embedded SQLite store and real (local) git, the
// same discipline model/simulate_test.go and dispatch/dispatch_test.go already
// hold their own random tests to one layer down.
//
// What this test checks that neither of those two can: model/
// simulate_test.go and dispatch/dispatch_test.go never touch git, so nothing
// there can catch a task whose state says "done" with no branch behind
// it, or a push that landed on the wrong repo because two concurrently
// live sandboxes' scopes got crossed. checkSimInvariants' last section
// is that check -- every round, a task the sim believes pushed cleanly
// must have exactly that branch, with exactly that commit, in exactly
// its own target repo, and a task the sim never pushed must have no
// branch at all.

import (
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent/antigravity"
	"github.com/bwsalmon/grain/pkg/dispatch"
	"github.com/bwsalmon/grain/pkg/model"
)

// simTask is what the simulation's own components know about one issue,
// kept independently of the store for the same reason model/
// simulate_test.go's taskState is: the checks below compare two
// independent records of the same history, rather than reading the
// store back to check itself.
type simTask struct {
	repo   model.RepoRef
	branch string

	liveRunID    string
	liveDispatch dispatch.Dispatch
	attempts     int

	pushed        bool
	awaitingReply bool
	completed     bool
	closed        bool
}

func TestMultipleUsersFilingIssuesSimulationEndToEnd(t *testing.T) {
	t.Parallel()
	const maxWorkers = 3
	w := newWorld(t)

	repos := []model.RepoRef{
		{Owner: "acme", Name: "widgets"},
		{Owner: "acme", Name: "gadgets"},
		{Owner: "acme", Name: "tools"},
	}
	for _, r := range repos {
		w.newRepo(r.Owner, r.Name)
	}
	users := []model.Principal{human("alice"), human("bob"), human("carol"), human("dave")}

	rng := rand.New(rand.NewPCG(1, 233))
	const maxTasks = 14
	tasks := map[string]*simTask{}
	var order []string
	clock := baseTime

	for round := 0; round < 25; round++ {
		clock = clock.Add(time.Minute)

		// A user files an issue -- sometimes depending on one filed
		// earlier, never later, so no cycle is constructible (the same
		// discipline model/simulate_test.go's fileTask and its own
		// TestModelInvariantsHoldUnderRandomComponentActions both use).
		if len(order) < maxTasks && rng.Float64() < 0.6 {
			id := fmt.Sprintf("iss%02d", len(order))
			repo := repos[rng.IntN(len(repos))]
			var links []model.Link
			if len(order) > 0 && rng.Float64() < 0.3 {
				parent := order[rng.IntN(len(order))]
				links = []model.Link{{Kind: model.LinkDependsOn, Target: parent}}
			}
			actor := users[rng.IntN(len(users))]
			fileIssue(w, id, actor, repo, links...)
			tasks[id] = &simTask{repo: repo, branch: model.BranchName(id)}
			order = append(order, id)
		}

		// Dispatcher: hand every ready task a free slot. task_ready
		// itself excludes anything already running, so a task with an
		// unresolved live run from an earlier round is never redispatched
		// out from under itself.
		dispatches, err := dispatch.Cycle(w.ctx, w.store, model.Limits{Workers: maxWorkers}, clock)
		if err != nil {
			t.Fatalf("round %d: Cycle: %v", round, err)
		}
		for _, d := range dispatches {
			st := tasks[d.TaskID]
			if st == nil {
				t.Fatalf("round %d: Cycle dispatched unknown task %s", round, d.TaskID)
			}
			if st.liveRunID != "" {
				t.Fatalf("round %d: %s dispatched while already running", round, d.TaskID)
			}
			st.liveRunID, st.liveDispatch = d.RunID, d
			st.attempts++
		}

		// Agent sessions: for every currently live run -- this round's
		// new dispatches and any still unresolved from an earlier one --
		// resolve most of them now and leave the rest live, so slots
		// really do stay occupied across a round boundary sometimes,
		// exactly the case model/simulate_test.go's
		// TestModelInvariantsHoldUnderRandomComponentActions exercises
		// against the pure model.
		for _, id := range order {
			st := tasks[id]
			if st.liveRunID == "" || rng.Float64() >= 0.7 {
				continue
			}
			resolveLiveRun(t, w, round, id, st, rng, &clock)
		}

		// A human replies to a random parked task.
		if rng.Float64() < 0.5 {
			if id, ok := pickRandom(rng, order, func(id string) bool { return tasks[id].awaitingReply }); ok {
				clock = clock.Add(time.Second)
				if err := w.store.Observe(w.ctx, model.Observation{TaskID: id, ObservedAt: &clock}); err != nil {
					t.Fatalf("round %d: Observe reply %s: %v", round, id, err)
				}
				tasks[id].awaitingReply = false
			}
		}

		// GitHub merges a random completed-but-unmerged PR -- standing in
		// for GitHub's own merge button (harness_test.go's package doc),
		// since nothing in v2 owns that action yet.
		if rng.Float64() < 0.3 {
			if id, ok := pickRandom(rng, order, func(id string) bool { return tasks[id].completed && !tasks[id].closed }); ok {
				st := tasks[id]
				w.mergeBranchIntoDefault(st.repo.Owner, st.repo.Name, st.branch, "main")
				clock = clock.Add(time.Second)
				if err := w.store.Observe(w.ctx, model.Observation{TaskID: id, CompletedAt: &clock, ClosedAt: &clock}); err != nil {
					t.Fatalf("round %d: Observe merge %s: %v", round, id, err)
				}
				st.closed = true
			}
		}

		checkSimInvariants(t, w, round, tasks, order, maxWorkers)
	}

	// The run must actually have exercised every path it set out to --
	// a seed that happened to avoid pushes, questions or merges entirely
	// would let every check above pass vacuously.
	var pushed, asked, closed int
	for _, id := range order {
		st := tasks[id]
		if st.pushed {
			pushed++
		}
		if st.awaitingReply {
			asked++
		}
		if st.closed {
			closed++
		}
	}
	if len(order) < maxTasks {
		t.Errorf("only %d/%d tasks got filed before the rounds ran out", len(order), maxTasks)
	}
	if pushed == 0 {
		t.Error("no task ever pushed a branch -- the scenario never exercised the happy path")
	}
	if closed == 0 {
		t.Error("no task ever got merged and closed -- the scenario never exercised a merge")
	}
}

// resolveLiveRun picks a random outcome for st's live run, drives it
// through a real (scripted) agent turn, and applies the same GitHub-sync
// stand-in logic e2e_test.go's fixed tests apply by hand.
func resolveLiveRun(t *testing.T, w *world, round int, id string, st *simTask, rng *rand.Rand, clock *time.Time) {
	t.Helper()
	*clock = clock.Add(time.Second)

	var script []antigravity.Step
	switch r := rng.Float64(); {
	case r < 0.55:
		script = pushScriptOwnFile(w.remote(st.repo.Owner, st.repo.Name), st.branch, id)
	case r < 0.8:
		script = failScript("simulated failure")
	default:
		script = askScript("need direction on " + id)
	}

	result := w.runDispatch(st.liveDispatch, script, *clock)
	st.liveRunID = ""

	switch {
	case pushedOK(result):
		st.pushed = true
		*clock = clock.Add(time.Second)
		if err := w.store.Observe(w.ctx, model.Observation{TaskID: id, CompletedAt: clock}); err != nil {
			t.Fatalf("round %d: Observe completed %s: %v", round, id, err)
		}
		st.completed = true
	default:
		if _, asked := askedQuestion(result); asked {
			commentID := int64(round*1000 + st.attempts)
			*clock = clock.Add(time.Second)
			if err := w.store.Observe(w.ctx, model.Observation{
				TaskID: id, PendingQuestionCommentID: &commentID, ObservedAt: clock,
			}); err != nil {
				t.Fatalf("round %d: Observe question %s: %v", round, id, err)
			}
			st.awaitingReply = true
		}
		// A plain failure observes nothing: task_ready offers the task
		// again on its own, with no requeue call needed.
	}
}

// pickRandom returns a random element of order matching ok, and false if
// none does.
func pickRandom(rng *rand.Rand, order []string, ok func(string) bool) (string, bool) {
	var candidates []string
	for _, id := range order {
		if ok(id) {
			candidates = append(candidates, id)
		}
	}
	if len(candidates) == 0 {
		return "", false
	}
	return candidates[rng.IntN(len(candidates))], true
}

// checkSimInvariants is the property this test exists to guarantee,
// checked after every round rather than once at the end so a failure
// points at the round that broke it.
func checkSimInvariants(t *testing.T, w *world, round int, tasks map[string]*simTask, order []string, maxWorkers int) {
	t.Helper()

	wantOccupied := 0
	for _, id := range order {
		st := tasks[id]
		active := st.liveRunID != ""
		if active {
			wantOccupied++
		}

		task, err := w.store.GetTask(w.ctx, id)
		if err != nil || task == nil {
			t.Fatalf("round %d: GetTask(%s): %v (nil=%v)", round, id, err, task == nil)
		}
		obs, err := w.store.GetObservation(w.ctx, id)
		if err != nil {
			t.Fatalf("round %d: GetObservation(%s): %v", round, id, err)
		}
		streak, err := w.store.FailureStreak(w.ctx, id)
		if err != nil {
			t.Fatalf("round %d: FailureStreak(%s): %v", round, id, err)
		}
		failureStreak := 0
		if streak != nil {
			failureStreak = streak.Count
		}
		want := model.StateOf(*task, obs, active, failureStreak)
		got, err := w.store.State(w.ctx, id)
		if err != nil {
			t.Fatalf("round %d: State(%s): %v", round, id, err)
		}
		if got != want {
			t.Fatalf("round %d: task %s: view state = %q, model.StateOf = %q (active=%v)",
				round, id, got, want, active)
		}

		attempts, err := w.store.Attempts(w.ctx, id)
		if err != nil {
			t.Fatalf("round %d: Attempts(%s): %v", round, id, err)
		}
		if attempts != st.attempts {
			t.Fatalf("round %d: task %s: store attempts = %d, sim dispatched it %d time(s)",
				round, id, attempts, st.attempts)
		}

		// The check neither model/simulate_test.go nor dispatch/dispatch_test.go
		// can run, since neither touches git: model state and real git
		// state must agree on whether this task's branch exists.
		exists := w.branchExists(st.repo.Owner, st.repo.Name, st.branch)
		if st.pushed && !exists {
			t.Fatalf("round %d: task %s: sim recorded a clean push, but branch %s is missing from %s/%s",
				round, id, st.branch, st.repo.Owner, st.repo.Name)
		}
		if !st.pushed && exists {
			t.Fatalf("round %d: task %s: sim never pushed, but branch %s exists in %s/%s -- wrong-scope leak?",
				round, id, st.branch, st.repo.Owner, st.repo.Name)
		}
		if st.pushed {
			wantMsg := "agent commit for " + id
			if got := w.log1(st.repo.Owner, st.repo.Name, st.branch, "%s"); got != wantMsg {
				t.Fatalf("round %d: task %s: branch tip = %q, want %q", round, id, got, wantMsg)
			}
		}
		if st.closed && !w.branchContains(st.repo.Owner, st.repo.Name, "main", st.branch) {
			t.Fatalf("round %d: task %s: sim recorded a merge, but main does not contain %s", round, id, st.branch)
		}
	}

	occ, err := w.store.LiveRunCount(w.ctx)
	if err != nil {
		t.Fatalf("round %d: LiveRunCount: %v", round, err)
	}
	if occ != wantOccupied {
		t.Fatalf("round %d: store reports %d live runs, sim expects %d", round, occ, wantOccupied)
	}
	if occ > maxWorkers {
		t.Fatalf("round %d: %d runs live, over the limit of %d", round, occ, maxWorkers)
	}
}
