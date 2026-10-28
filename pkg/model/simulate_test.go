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

package model_test

// bwsalmon/agents#220 asked for tests that simulate the components that
// sit outside the model but drive it: a human or automation filing and
// approving tasks, a dispatcher handing a task to a sandbox, an agent
// session working inside one, and GitHub sync observing what happened.
// Nothing here is a fake standing in for the store -- these run against a
// real embedded SQLite database, the discipline model/sqlite/store_test.go
// and dispatch/dispatch_test.go already hold to. What is mocked is the *caller*:
// every function below makes the same store calls a real GitHub poller,
// sandbox manager or approval handler would make, so the sequences they
// produce are only ever ones the model permits, not ones invented for
// convenience.
//
// TestModelInvariantsHoldUnderRandomComponentActions runs many rounds of
// these components in random combination and, after every round, checks
// the property dispatch/dispatch_test.go's random test does not: that
// model.StateOf -- the pure Go derivation a caller with no database can
// use -- agrees with task_state, the SQL view the store actually reads,
// for every task, under whatever sequence of raw facts the components
// happened to produce. state_test.go already holds the two to agreeing
// on six fixed cases; this is the same check, but against sequences
// nobody wrote by hand.

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

var (
	now   = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	human = model.Principal{Kind: model.PrincipalHuman, ID: "bwsalmon"}
	bot   = model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}
)

func open(t *testing.T) (*model.Store, context.Context) {
	t.Helper()
	if postgres.IsAvailable() || os.Getenv("GRAIN_POSTGRES_TEST_URL") != "" {
		store, _ := postgrestest.Open(t)
		return store, context.Background()
	}
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	return store, ctx
}

// --- fixed scenarios, one per component ------------------------------

func TestUserFilingLandsAccordingToActor(t *testing.T) {
	store, ctx := open(t)
	filedByHuman := model.Task{
		ID: "human-filed", Intent: model.IntentImplement, Title: "t",
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect,
		},
		Binding:  model.BindingDirective,
		Approval: &model.Attribution{Actor: human},
	}
	filedByAutomation := model.Task{
		ID: "automation-filed", Intent: model.IntentImplement, Title: "t",
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: bot}, Reason: model.ReasonProposal,
		},
		Binding: model.BindingDirective,
	}
	if err := store.PutTask(ctx, filedByHuman); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTask(ctx, filedByAutomation); err != nil {
		t.Fatal(err)
	}

	if st, _ := store.State(ctx, "human-filed"); st != model.StateQueued {
		t.Errorf("human-filed state = %q, want queued", st)
	}
	if st, _ := store.State(ctx, "automation-filed"); st != model.StateProposed {
		t.Errorf("automation-filed state = %q, want proposed: automation can never queue its own proposal", st)
	}

	// A human's approval -- even relayed by automation -- is what moves it
	// to queued; the reason it was proposed is never consulted.
	if err := store.Approve(ctx, "automation-filed",
		model.Attribution{Actor: bot, OnBehalfOf: &human}, now); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.State(ctx, "automation-filed"); st != model.StateQueued {
		t.Errorf("automation-filed after relayed approval = %q, want queued", st)
	}
}

func TestDispatcherNeverStartsAnUnapprovedOrBlockedTask(t *testing.T) {
	store, ctx := open(t)
	mk := func(id string, approved bool, links ...model.Link) model.Task {
		tk := model.Task{
			ID: id, Intent: model.IntentImplement, Title: "t",
			Origin: model.Origin{
				Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect,
			},
			Binding: model.BindingDirective,
			Links:   links,
		}
		if approved {
			tk.Approval = &model.Attribution{Actor: human}
		}
		return tk
	}
	for _, tk := range []model.Task{
		mk("blocker", true),
		mk("blocked", true, model.Link{Kind: model.LinkDependsOn, Target: "blocker"}),
		mk("proposed", false),
		mk("ready-task", true),
	} {
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatalf("put %s: %v", tk.ID, err)
		}
	}

	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, id := range ready {
		got[id] = true
	}
	if !got["blocker"] || !got["ready-task"] {
		t.Errorf("ready = %v, want blocker and ready-task", ready)
	}
	if got["blocked"] {
		t.Error("a task with an open depends-on must never be dispatchable")
	}
	if got["proposed"] {
		t.Error("automation must never start work a human did not approve")
	}
	if len(ready) != 2 {
		t.Errorf("ready = %v, want exactly [blocker, ready-task]", ready)
	}
}

func TestAgentSessionLeasesAreRevokedWhenARunFinishes(t *testing.T) {
	store, ctx := open(t)
	if err := store.PutTask(ctx, model.Task{
		ID: "a1b2", Intent: model.IntentImplement, Title: "t",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Binding:  model.BindingDirective,
		Approval: &model.Attribution{Actor: human},
	}); err != nil {
		t.Fatal(err)
	}
	expires := now.Add(24 * time.Hour)
	leases := []model.Lease{
		{Capability: "gemini-key", Resource: "projects/p/keys/g1",
			MintedBy: model.CredentialRef{Name: "gcp-host-service-account"}, IssuedAt: now, ExpiresAt: &expires},
		{Capability: "gcp-key", Resource: "projects/p/keys/g2",
			MintedBy: model.CredentialRef{Name: "gcp-host-service-account"}, IssuedAt: now},
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: "a1b2", Sandbox: "s1", Attempt: 1, StartedAt: now, Leases: leases,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if live, err := store.LiveLeases(ctx, ""); err != nil || len(live) != 2 {
		t.Fatalf("live leases while running = %v (%v), want 2", live, err)
	}

	// The view masks a finished run's leases immediately, before the
	// session that held them ever calls DropLease -- the row is not gone,
	// only no longer live.
	if err := store.FinishRun(ctx, "r1", now.Add(time.Hour), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if live, err := store.LiveLeases(ctx, ""); err != nil || len(live) != 0 {
		t.Fatalf("live leases right after finish = %v (%v), want none", live, err)
	}

	// Revocation must still happen on this path -- and happen twice
	// without error, since a release and an expiry reaper can both reach
	// the same lease.
	for i := 0; i < 2; i++ {
		for _, l := range leases {
			if err := store.DropLease(ctx, "r1", l.Capability, l.Resource); err != nil {
				t.Fatalf("DropLease pass %d, %s: %v", i, l.Capability, err)
			}
		}
	}
}

func TestGitHubSyncObservationsReplaceTheWholeRowNotJustTheChangedField(t *testing.T) {
	// Observe REPLACEs every column from what it is given -- there is no
	// partial update. A GitHub-sync component that builds a fresh
	// Observation each poll and forgets a fact it still believes true
	// silently erases it. This is the behavior any such component must
	// respect, and every helper in this file's random test carries the
	// previous observation forward for exactly this reason.
	store, ctx := open(t)
	id := int64(42)
	if err := store.Observe(ctx, model.Observation{TaskID: "a1b2", PendingQuestionCommentID: &id}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetObservation(ctx, "a1b2")
	if err != nil || got == nil || got.PendingQuestionCommentID == nil {
		t.Fatalf("after first observe: %+v (%v)", got, err)
	}

	// A second Observe that only names CompletedAt, without repeating the
	// pending question, must silently drop it.
	if err := store.Observe(ctx, model.Observation{TaskID: "a1b2", CompletedAt: &now}); err != nil {
		t.Fatal(err)
	}
	got, err = store.GetObservation(ctx, "a1b2")
	if err != nil || got == nil {
		t.Fatalf("after second observe: %+v (%v)", got, err)
	}
	if got.PendingQuestionCommentID != nil {
		t.Fatalf("pending question survived a replace that did not repeat it: %+v", got)
	}
	if got.CompletedAt == nil {
		t.Fatalf("completed_at did not take: %+v", got)
	}
}

func TestSandboxTransitionReusesFreedSlotForTheNextRun(t *testing.T) {
	store, ctx := open(t)
	for _, id := range []string{"t0", "t1"} {
		if err := store.PutTask(ctx, model.Task{
			ID: id, Intent: model.IntentImplement, Title: "t",
			Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
			Binding:  model.BindingDirective,
			Approval: &model.Attribution{Actor: human},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "t0-1", TaskID: "t0", Sandbox: "sandbox-1", Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if occ, _ := store.LiveRunCount(ctx); occ != 1 {
		t.Fatalf("live runs = %d, want 1", occ)
	}

	// A sandbox is long-lived and recreated on demand, not per task: what
	// frees it is the run finishing, not a reset the store knows about.
	if err := store.FinishRun(ctx, "t0-1", now.Add(time.Hour), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if occ, _ := store.LiveRunCount(ctx); occ != 0 {
		t.Fatalf("occupied after finish = %v, want none", occ)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "t1-1", TaskID: "t1", Sandbox: "sandbox-1", Attempt: 1, StartedAt: now.Add(time.Hour),
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if occ, _ := store.LiveRunCount(ctx); occ != 1 {
		t.Fatalf("live runs after reassignment = %d, want 1 again", occ)
	}
}

func TestClosingATaskWhileItsRunIsStillLiveOutranksRunning(t *testing.T) {
	// task.go's StateOf docstring says this precedence is deliberate: "a
	// completed task whose issue was then closed reads closed", and that
	// generalises to running -- GitHub can close an issue out from under
	// an agent still working it, and the derived state must say so
	// immediately, even though the slot the run occupies is not freed
	// until something actually calls FinishRun.
	store, ctx := open(t)
	if err := store.PutTask(ctx, model.Task{
		ID: "a1b2", Intent: model.IntentImplement, Title: "t",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Binding:  model.BindingDirective,
		Approval: &model.Attribution{Actor: human},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: "a1b2", Sandbox: "s1", Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.State(ctx, "a1b2"); st != model.StateRunning {
		t.Fatalf("state before closing = %q, want running", st)
	}
	if err := store.Observe(ctx, model.Observation{TaskID: "a1b2", ClosedAt: &now}); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.State(ctx, "a1b2"); st != model.StateClosed {
		t.Errorf("state after closing = %q, want closed even though the run never finished", st)
	}
	if occ, _ := store.LiveRunCount(ctx); occ != 1 {
		t.Errorf("live runs = %d, want the run still live: closing does not itself finish it", occ)
	}
}

// --- the random-component property test -------------------------------

// taskState is what the components themselves know without asking the
// store -- the same facts a real dispatcher, agent session and lease
// reaper would carry -- kept separately from the store so the checks
// below compare two independent records of the same history rather than
// reading the store back to check itself.
type taskState struct {
	liveRunID   string
	liveSandbox string
	leases      []leaseInfo
	attempts    int
}

type leaseInfo struct {
	runID, capability, resource string
}

func TestModelInvariantsHoldUnderRandomComponentActions(t *testing.T) {
	store, ctx := open(t)
	rng := rand.New(rand.NewPCG(1, 220))
	const maxWorkers = 3
	leaseCaps := []string{"gemini-key", "gcp-key", "github-app-token"}
	const maxTasks = 14

	world := map[string]*taskState{}
	var order []string
	clock := now
	nextCommentID := int64(1)

	for round := 0; round < 60; round++ {
		clock = clock.Add(time.Minute)

		// User update: a human or automation files a new task, sometimes
		// depending on one filed earlier -- never later, so no cycle is
		// constructible (the same discipline tests/e2e/simulate_test.go's
		// own random DAG generation uses).
		if len(order) < maxTasks && rng.Float64() < 0.5 {
			id := fmt.Sprintf("tk%02d", len(order))
			fileTask(t, store, ctx, rng, world, &order, id)
		}

		// User update: a human approves something still proposed.
		if rng.Float64() < 0.5 {
			approveARandomProposedTask(t, store, ctx, rng, order, clock)
		}

		// Dispatcher / sandbox: hand every ready task a free slot.
		dispatchReadyTasks(t, store, ctx, rng, world, maxWorkers, clock, leaseCaps)

		// Agent session: for a live run, ask a question or finish.
		for _, id := range order {
			ts := world[id]
			if ts.liveRunID == "" || rng.Float64() >= 0.5 {
				continue
			}
			if rng.Float64() < 0.3 {
				askQuestion(t, store, ctx, id, &nextCommentID, clock)
			} else {
				outcome := []string{"succeeded", "failed", "requeued"}[rng.IntN(3)]
				finishRun(t, store, ctx, ts, outcome, clock)
			}
		}

		// GitHub sync: observe completion (only once something actually
		// ran and finished) and closure (a human can close an issue at
		// any time, run or no run).
		for _, id := range order {
			ts := world[id]
			if ts.liveRunID == "" && ts.attempts > 0 && rng.Float64() < 0.15 {
				observeCompleted(t, store, ctx, id, clock)
			}
			if rng.Float64() < 0.08 {
				observeClosed(t, store, ctx, id, clock)
			}
		}

		// Lease reaper: an expired lease can be revoked before the run
		// that holds it ever finishes, and doing so twice must not error.
		if rng.Float64() < 0.2 {
			reapARandomLease(t, store, ctx, rng, world, order)
		}

		checkModelInvariants(t, store, ctx, round, world, order)
	}
}

func fileTask(t *testing.T, store *model.Store, ctx context.Context, rng *rand.Rand,
	world map[string]*taskState, order *[]string, id string) {
	t.Helper()
	var links []model.Link
	if len(*order) > 0 && rng.Float64() < 0.4 {
		parent := (*order)[rng.IntN(len(*order))]
		links = []model.Link{{Kind: model.LinkDependsOn, Target: parent}}
	}
	actor, reason := human, model.ReasonDirect
	if rng.Float64() < 0.5 {
		actor, reason = bot, model.ReasonProposal
	}
	tk := model.Task{
		ID: id, Intent: model.IntentImplement, Title: "task " + id,
		Origin:  model.Origin{Attribution: model.Attribution{Actor: actor}, Reason: reason},
		Binding: model.BindingDirective,
		Links:   links,
	}
	if model.LandsQueued(tk.Origin) {
		tk.Approval = &model.Attribution{Actor: human}
	}
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatalf("filing %s: %v", id, err)
	}
	world[id] = &taskState{}
	*order = append(*order, id)
}

func approveARandomProposedTask(t *testing.T, store *model.Store, ctx context.Context, rng *rand.Rand, order []string, clock time.Time) {
	t.Helper()
	var candidates []string
	for _, id := range order {
		tk, err := store.GetTask(ctx, id)
		if err != nil {
			t.Fatalf("GetTask(%s): %v", id, err)
		}
		if tk != nil && tk.Approval == nil {
			candidates = append(candidates, id)
		}
	}
	if len(candidates) == 0 {
		return
	}
	id := candidates[rng.IntN(len(candidates))]
	a := model.Attribution{Actor: human}
	if rng.Float64() < 0.5 {
		a = model.Attribution{Actor: bot, OnBehalfOf: &human}
	}
	if err := store.Approve(ctx, id, a, clock); err != nil {
		t.Fatalf("Approve(%s): %v", id, err)
	}
}

func dispatchReadyTasks(t *testing.T, store *model.Store, ctx context.Context, rng *rand.Rand,
	world map[string]*taskState, maxWorkers int, clock time.Time, leaseCaps []string) {
	t.Helper()
	live, err := store.LiveRunCount(ctx)
	if err != nil {
		t.Fatalf("LiveRunCount: %v", err)
	}
	free := maxWorkers - live
	if free <= 0 {
		return
	}

	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	for _, id := range ready {
		tk, err := store.GetTask(ctx, id)
		if err != nil || tk == nil {
			t.Fatalf("GetTask(%s) for a ready task: %v", id, err)
		}
		if tk.Approval == nil {
			t.Fatalf("Ready() returned unapproved task %s", id)
		}
		if n, err := store.OpenBlockers(ctx, id); err != nil {
			t.Fatalf("OpenBlockers(%s): %v", id, err)
		} else if n > 0 {
			t.Fatalf("Ready() returned blocked task %s with %d open blocker(s)", id, n)
		}
	}

	for i := 0; i < free; i++ {
		if i >= len(ready) {
			break
		}
		id := ready[i]
		ts := world[id]
		if ts == nil {
			t.Fatalf("Ready() returned %s, unknown to any component", id)
		}
		if ts.liveRunID != "" {
			t.Fatalf("Ready() returned %s while it already has a live run", id)
		}

		attempt := ts.attempts + 1
		runID := fmt.Sprintf("%s-%d", id, attempt)
		// The sandbox is the run's own, named after it -- what
		// orchestrator.runOne records via SetRunSandbox once it has
		// acquired one.
		run := model.Run{ID: runID, TaskID: id, Sandbox: runID, Attempt: attempt, StartedAt: clock}
		var leases []leaseInfo
		for n := rng.IntN(3); n > 0; n-- {
			capability := leaseCaps[rng.IntN(len(leaseCaps))]
			resource := fmt.Sprintf("projects/p/keys/%s-%d", capability, rng.IntN(1000))
			l := model.Lease{
				Capability: capability, Resource: resource,
				MintedBy: model.CredentialRef{Name: "gcp-host-service-account"}, IssuedAt: clock,
			}
			if rng.Float64() < 0.5 {
				exp := clock.Add(24 * time.Hour)
				l.ExpiresAt = &exp
			}
			run.Leases = append(run.Leases, l)
			leases = append(leases, leaseInfo{runID: runID, capability: capability, resource: resource})
		}
		if err := store.StartRun(ctx, run, model.Limits{}); err != nil {
			t.Fatalf("StartRun(%s): %v", id, err)
		}
		ts.liveRunID, ts.liveSandbox, ts.leases, ts.attempts = runID, runID, leases, attempt
	}
}

func currentOrEmptyObservation(t *testing.T, store *model.Store, ctx context.Context, id string) model.Observation {
	t.Helper()
	got, err := store.GetObservation(ctx, id)
	if err != nil {
		t.Fatalf("GetObservation(%s): %v", id, err)
	}
	if got == nil {
		return model.Observation{TaskID: id}
	}
	return *got
}

func askQuestion(t *testing.T, store *model.Store, ctx context.Context, id string, nextCommentID *int64, clock time.Time) {
	t.Helper()
	obs := currentOrEmptyObservation(t, store, ctx, id)
	commentID := *nextCommentID
	*nextCommentID++
	obs.PendingQuestionCommentID = &commentID
	obs.ObservedAt = &clock
	if err := store.Observe(ctx, obs); err != nil {
		t.Fatalf("Observe (question) %s: %v", id, err)
	}
}

func finishRun(t *testing.T, store *model.Store, ctx context.Context, ts *taskState, outcome string, clock time.Time) {
	t.Helper()
	if err := store.FinishRun(ctx, ts.liveRunID, clock, outcome, ""); err != nil {
		t.Fatalf("FinishRun(%s): %v", ts.liveRunID, err)
	}
	// Revocation happens on every path that frees a slot -- the session
	// ending is exactly such a path.
	for _, l := range ts.leases {
		if err := store.DropLease(ctx, l.runID, l.capability, l.resource); err != nil {
			t.Fatalf("DropLease(%s,%s,%s): %v", l.runID, l.capability, l.resource, err)
		}
	}
	ts.liveRunID, ts.liveSandbox, ts.leases = "", "", nil
}

func observeCompleted(t *testing.T, store *model.Store, ctx context.Context, id string, clock time.Time) {
	t.Helper()
	obs := currentOrEmptyObservation(t, store, ctx, id)
	obs.CompletedAt = &clock
	obs.PendingQuestionCommentID = nil
	obs.ObservedAt = &clock
	if err := store.Observe(ctx, obs); err != nil {
		t.Fatalf("Observe (completed) %s: %v", id, err)
	}
}

func observeClosed(t *testing.T, store *model.Store, ctx context.Context, id string, clock time.Time) {
	t.Helper()
	obs := currentOrEmptyObservation(t, store, ctx, id)
	obs.ClosedAt = &clock
	obs.ObservedAt = &clock
	if err := store.Observe(ctx, obs); err != nil {
		t.Fatalf("Observe (closed) %s: %v", id, err)
	}
}

func reapARandomLease(t *testing.T, store *model.Store, ctx context.Context, rng *rand.Rand,
	world map[string]*taskState, order []string) {
	t.Helper()
	var all []leaseInfo
	for _, id := range order {
		all = append(all, world[id].leases...)
	}
	if len(all) == 0 {
		return
	}
	picked := all[rng.IntN(len(all))]
	for i := 0; i < 2; i++ {
		if err := store.DropLease(ctx, picked.runID, picked.capability, picked.resource); err != nil {
			t.Fatalf("DropLease pass %d (%s,%s,%s): %v", i, picked.runID, picked.capability, picked.resource, err)
		}
	}
	for _, id := range order {
		ts := world[id]
		for i, l := range ts.leases {
			if l == picked {
				ts.leases = append(ts.leases[:i], ts.leases[i+1:]...)
				break
			}
		}
	}
}

// checkModelInvariants is the property this test exists to guarantee,
// checked after every round rather than once at the end so a failure
// points at the round that broke it. Four things are asserted, none of
// them duplicating tests/e2e/simulate_test.go's dispatch-ordering
// checks: the SQL view
// and the pure Go derivation agree on every task's state; a task's
// attempt count is exactly how many times it has been dispatched; every
// live lease the store reports is one a component believes it still
// holds and no more; and no sandbox is ever shared by two live runs.
func checkModelInvariants(t *testing.T, store *model.Store, ctx context.Context, round int,
	world map[string]*taskState, order []string) {
	t.Helper()

	sandboxes := map[string]bool{}
	for _, id := range order {
		ts := world[id]
		if ts.liveRunID == "" {
			continue
		}
		if sandboxes[ts.liveSandbox] {
			t.Fatalf("round %d: sandbox %s held by two live runs in the oracle itself", round, ts.liveSandbox)
		}
		sandboxes[ts.liveSandbox] = true
	}
	occ, err := store.LiveRunCount(ctx)
	if err != nil {
		t.Fatalf("round %d: LiveRunCount: %v", round, err)
	}
	if occ != len(sandboxes) {
		t.Fatalf("round %d: store reports %d live runs, components expect %d",
			round, occ, len(sandboxes))
	}

	wantLive := map[string]bool{}
	for _, id := range order {
		ts := world[id]

		gotTask, err := store.GetTask(ctx, id)
		if err != nil || gotTask == nil {
			t.Fatalf("round %d: GetTask(%s): %v (nil=%v)", round, id, err, gotTask == nil)
		}
		obs, err := store.GetObservation(ctx, id)
		if err != nil {
			t.Fatalf("round %d: GetObservation(%s): %v", round, id, err)
		}
		active := ts.liveRunID != ""

		streak, err := store.FailureStreak(ctx, id)
		if err != nil {
			t.Fatalf("round %d: FailureStreak(%s): %v", round, id, err)
		}
		failureStreak := 0
		if streak != nil {
			failureStreak = streak.Count
		}

		want := model.StateOf(*gotTask, obs, active, failureStreak)
		got, err := store.State(ctx, id)
		if err != nil {
			t.Fatalf("round %d: State(%s): %v", round, id, err)
		}
		if got != want {
			t.Fatalf("round %d: task %s: task_state view = %q, model.StateOf = %q (obs=%+v active=%v approval=%+v)",
				round, id, got, want, obs, active, gotTask.Approval)
		}

		attempts, err := store.Attempts(ctx, id)
		if err != nil {
			t.Fatalf("round %d: Attempts(%s): %v", round, id, err)
		}
		if attempts != ts.attempts {
			t.Fatalf("round %d: task %s: store attempts = %d, components dispatched it %d time(s)",
				round, id, attempts, ts.attempts)
		}

		for _, l := range ts.leases {
			wantLive[l.runID+"/"+l.capability+"/"+l.resource] = true
		}
	}

	live, err := store.LiveLeases(ctx, "")
	if err != nil {
		t.Fatalf("round %d: LiveLeases: %v", round, err)
	}
	if len(live) != len(wantLive) {
		t.Fatalf("round %d: LiveLeases returned %d, components expect %d: %+v", round, len(live), len(wantLive), live)
	}
	for _, l := range live {
		key := l.RunID + "/" + l.Capability + "/" + l.Resource
		if !wantLive[key] {
			t.Fatalf("round %d: LiveLeases returned %s, which no live component believes it holds", round, key)
		}
	}
}
