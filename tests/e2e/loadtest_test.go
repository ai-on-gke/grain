// loadtest_test.go is bwsalmon/agents#416's equivalent of v1's own
// Python load-test harness (its roadmap item 6, "actually run, not just
// built"): drive orchestrator.RunCycle under sustained concurrent load --
// many tasks, across many repos, many slots dispatching at once, while
// several independent goroutines write to the very same real, on-disk
// SQLite store the way pkg/ui's own concurrently served HTTP handlers
// really would while RunCycle is, at that same moment, ticking against
// it -- to catch scheduling starvation, sqlite contention
// (pkg/model/sqlite's own doc comment: "the single writer for the whole
// store"), and a capability provider leaking a resource it minted. None
// of the ordinary suite's fixed, small scenarios exercise any of these
// three at real scale: e2e_test.go and simulate_test.go use three slots
// and a couple dozen tasks at most, and even random_test.go's own long
// mode (TestRandomizedClusterLong) drives one store writer at a time,
// taking turns with a CLI subprocess by design (withStore's own doc
// comment) rather than ever letting the store see genuinely concurrent
// callers the way this file's runLoadWriter does.
//
// v1's own version of this test booted real VMs, because the question it
// answered (docs/design.md open question 2) was a host-sizing one: does
// a 4-vCPU host hold a controller plus two fully provisioned sandbox VMs
// under real concurrent `kind create cluster` and build load. v2 has no
// equivalent question to answer by booting anything -- its default
// deployment dispatches onto plain host directories, not VMs
// (terraform/gcp/variables.tf's own machine_type comment: "this VM
// does not run nested virtualization"), and its target host shape is a
// single e2-standard-2 (2 vCPU, 8 GB) instance running one grain daemon
// against a handful of repos. What that deployment actually risks at
// scale is the orchestrator/store layer itself, which is what this file
// is a load test of -- a redesign for what v2 is actually built to run,
// not a port of v1's VM-boot mechanics onto a shape v2 does not have.
//
// This harness never has a dispatch push a real branch. That is a
// deliberate scope cut, not an oversight: the git/pull-request path is
// already exercised at real scale by e2e_test.go, random_test.go and
// pkg/orchestrator/live_test.go, and pushing a branch touches none of
// the three things bwsalmon/agents#416 actually asks this file to catch.
// Every dispatch here ends by closing the task with a comment, failing
// (to exercise dispatch.Cycle's own retry backoff), or asking a question
// (to exercise the awaiting_reply/reply cycle) -- see loadGenerator in
// loadtest_helpers_test.go. All three run through exactly the same
// dispatch.Cycle, RunDispatch, capability materialize/revoke and
// FinishRun code a push would.
//
// What this file reports about the tick is not this file's own
// measurement. RunCycle times itself -- orchestrator.CycleTimes, the
// ring a deployment serves as GET /api/metrics' "cycles" section -- so
// the harness passes one of those into the Deps it ticks and reports
// from it, rather than wrapping each call in a stopwatch of its own:
// one measurement, exercised here and served there, instead of two that
// could quietly drift apart. It is also more than a stopwatch of its
// own could ever see. A tick that has grown under a large store is the
// failure this file is best placed to catch, and only the cycle's own
// numbers say *which part* of it grew -- how far in the dispatch
// decision was reached (CycleTiming.DispatchWait, the tick's own share
// of a queued task's wait) and where the rest of the tick went,
// reconciler by reconciler. reportCycles, below, is what reads them.
//
// Skipped unless GRAIN_LOAD_TEST is set, the same convention
// random_test.go's TestRandomizedClusterLong already established in this
// exact package for "a test worth running by hand on a sized host, never
// in CI" -- go-test.yml's own `go test ./...` step still compiles this
// file on every pull request, so a change that breaks it to build is
// still caught there; only actually running it is opt in. Run it:
//
//	GRAIN_LOAD_TEST=1 go test ./tests/e2e/... -run TestLoadSustainedConcurrency -v -timeout 15m
//
// or `make loadtest` (Makefile). This file's own defaults finish in a
// couple of minutes on a modern laptop; every dimension is sized
// up independently through an env var (loadConfigFromEnv, below) for an
// actual run against v2's own target deployment shape -- see README.md's
// own pointer to this file for suggested sizing on an e2-standard-2.
//
// The harness itself, though, is not opt in any more. What broke it
// last was its setup and not its load -- a capability the shipped
// director had been granted with no provider registered here, which
// parked the director on the first tick and killed the nightly job
// thirteen seconds in -- and a commit could do that and still land
// green, because nothing before the nightly ever started this file.
// TestLoadHarnessSmoke, below, runs the same harness turned all the way
// down on every commit for that reason: same setup, same dispatch path,
// seconds rather than minutes, and none of the measurements, which mean
// nothing at that size. The sustained run keeps everything that needs
// real scale to say anything.
package e2e

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// loadConfig is every dimension v1's own CLI flags (--sandboxes,
// --interval, ...) let a caller size to the host it actually ran
// on -- an env var apiece here instead of flags, since `go test` is how
// this file is invoked either way.
type loadConfig struct {
	slots         int // the concurrency limit; still named for the env var that sets it
	repos         int
	writers       int
	initialTasks  int
	duration      time.Duration
	drainTimeout  time.Duration
	revokeFailPct float64
	seed          uint64
	// dispatchBudget is the p95 dispatch wait reportCycles fails on, or
	// zero to report the number without judging it. Zero is what the
	// smoke run below uses: it drives far too few ticks for a p95 to mean
	// anything, and it runs on a CI machine doing several other packages
	// at once, so a threshold there would fail for load that has nothing
	// to do with the tick. Everything this harness asserts that is not a
	// wall-clock measurement -- a slot that never freed, a task left
	// ready, a leaked capability, a cycle that skipped a reconciler --
	// stays on in both.
	dispatchBudget time.Duration
}

func loadConfigFromEnv(t *testing.T) loadConfig {
	t.Helper()
	cfg := loadConfig{
		slots: 12, repos: 6, writers: 4, initialTasks: 48,
		duration: 30 * time.Second, drainTimeout: 120 * time.Second,
		revokeFailPct: 0.1, seed: uint64(time.Now().UnixNano()),
		dispatchBudget: loadDispatchBudget,
	}
	envInt(t, "GRAIN_LOAD_TEST_SLOTS", &cfg.slots)
	envInt(t, "GRAIN_LOAD_TEST_REPOS", &cfg.repos)
	envInt(t, "GRAIN_LOAD_TEST_WRITERS", &cfg.writers)
	envInt(t, "GRAIN_LOAD_TEST_INITIAL_TASKS", &cfg.initialTasks)
	envDuration(t, "GRAIN_LOAD_TEST_DURATION", &cfg.duration)
	envDuration(t, "GRAIN_LOAD_TEST_DRAIN", &cfg.drainTimeout)
	if v := os.Getenv("GRAIN_LOAD_TEST_SEED"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			t.Fatalf("GRAIN_LOAD_TEST_SEED: %v", err)
		}
		cfg.seed = n
	}
	return cfg
}

func envInt(t *testing.T, name string, dst *int) {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		return
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	*dst = n
}

func envDuration(t *testing.T, name string, dst *time.Duration) {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		return
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	*dst = d
}

func TestLoadSustainedConcurrency(t *testing.T) {
	t.Parallel()
	if os.Getenv("GRAIN_LOAD_TEST") == "" {
		t.Skip("set GRAIN_LOAD_TEST=1 to run the sustained concurrent-load harness (see this file's own doc comment)")
	}
	runLoadHarness(t, loadConfigFromEnv(t))
}

// TestLoadHarnessSmoke is the same harness, turned all the way down: a
// couple of repos, a handful of tasks, one tick's worth of sustained
// phase. It is not gated on GRAIN_LOAD_TEST, so `make test` and the go
// job run it on every commit, and it costs seconds rather than the
// minutes TestLoadSustainedConcurrency does.
//
// It is here because of what actually broke this harness: not the load,
// the setup. A capability the shipped director had been granted had no
// provider registered here, so the first tick that reached the director
// parked it and the whole nightly job died thirteen seconds in -- and
// since nothing gates a commit on a job only the nightly runs, it stayed
// broken for a week. A run this short would have caught it on the
// commit that caused it, and would catch the next thing of that shape
// that no assertion about a list can anticipate: a Deps field RunCycle
// grows, a reconciler that needs something this harness does not
// construct, a dispatch path that stops working against the fake
// framework.
//
// What it does not do is measure anything. At this size the numbers
// reportCycles and reportMetrics print are noise (cfg.dispatchBudget is
// zero for exactly that reason), and it shares a CI machine with the
// rest of the suite. Scheduling starvation, sqlite contention under
// genuinely concurrent writers and a capability leak across thousands of
// dispatches are still the sustained run's to catch, nightly, at a size
// worth measuring.
func TestLoadHarnessSmoke(t *testing.T) {
	t.Parallel()
	runLoadHarness(t, loadConfig{
		// Small enough that the whole thing -- seed backlog, one tick of
		// sustained phase, drain -- is a handful of ticks: a dispatch
		// here costs a few seconds of real subprocess, and the drain
		// runs until the queue is empty, so every task filed is tick
		// time this pays for on every commit.
		slots: 3, repos: 2, writers: 1, initialTasks: 3,
		duration: time.Second,
		// Generous, and it costs nothing when the run is healthy: the
		// drain loop stops the moment the queue is empty, which is a
		// few seconds here. What it buys is a CI machine running four
		// packages at once under -race, where the same handful of
		// dispatches takes several times as long -- a drain that timed
		// out there would fail assertDrainedCleanly for load rather
		// than for anything this test is about.
		drainTimeout: 4 * time.Minute,
		// A revoke failure in every second dispatch, rather than the
		// sustained run's one in ten: this run makes too few of them for
		// a tenth to reliably exercise Reap's leak-recovery path at all,
		// and reportCapability's own check that an injected failure was
		// reclaimed is one of the few things worth having here.
		revokeFailPct: 0.5,
		// Fixed, unlike the sustained run's clock-derived seed: a
		// per-commit test that fails should fail the same way twice.
		seed: 0x5eed10ad,
	})
}

func runLoadHarness(t *testing.T, cfg loadConfig) {
	t.Logf("load config: %+v", cfg)

	dataDir := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(dataDir))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	defer db.Close()
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}

	sandboxes := orchestrator.NewHostSandboxes(t.TempDir())
	repos := make([]model.RepoRef, cfg.repos)
	for i := range repos {
		repos[i] = model.RepoRef{Owner: "acme", Name: fmt.Sprintf("repo-%d", i)}
	}

	gh := newLoadGitHub()
	capability := newLoadCapability(cfg.revokeFailPct, 2*time.Second)
	// RunCycle also maintains grain's shipped director, so the registry
	// carries the grant-only providers a real daemon always gives that
	// session as well as this harness's own double -- a load writer talks
	// to the director through the same dispatch path as everything else,
	// and an absent provider there would make a missing test double the
	// thing under test. loadtest_capabilities_test.go builds it and says
	// why it is derived rather than listed; this store never configures
	// console sessions, so what a tick maintains here is the shipped
	// default. Checked before the first tick rather than left to the
	// dispatch that would hit it: a parked director fails this harness in
	// seconds, and naming the capability here is the difference between a
	// diagnosis and a puzzle.
	registry := loadHarnessRegistry(capability)
	if missing := missingSessionCapabilities(registry, model.DefaultConsoleSessions()); len(missing) != 0 {
		t.Fatalf("load harness: no capability provider for %v, which model.DefaultConsoleSessions grants -- "+
			"every tick below would park the director instead of dispatching it (see loadHarnessRegistry)", missing)
	}
	metrics := newLoadMetrics()

	tickerRNG := rand.New(rand.NewPCG(cfg.seed, cfg.seed^0x9e3779b97f4a7c15))
	var tickerRNGMu sync.Mutex
	var taskSeq atomic.Int64

	// Every tick RunCycle runs below measures itself into this ring, the
	// way cmd/grain's own reconcile loop does -- see reportCycles.
	cycles := orchestrator.NewCycleTimes(loadCycleHistory)
	deps := orchestrator.Deps{
		Store: store, Client: gh, Sandboxes: sandboxes,
		Framework: func(context.Context, orchestrator.AgentChoice) (agent.Framework, error) {
			return antigravity.NewForTest(newLoadGenerator(&tickerRNGMu, tickerRNG, metrics)), nil
		},
		Config:     orchestrator.Config{Capabilities: registry},
		MaxWorkers: cfg.slots,
		CycleTimes: cycles,
	}

	// Seed a backlog before the sustained phase starts, so every slot has
	// something to do from the very first tick rather than ramping up
	// from zero.
	for i := 0; i < cfg.initialTasks; i++ {
		createLoadTask(t, ctx, store, &taskSeq, repos, tickerRNG, metrics)
	}

	var creating atomic.Bool
	creating.Store(true)
	stopWriters := make(chan struct{})
	var writers sync.WaitGroup
	for i := 0; i < cfg.writers; i++ {
		writers.Add(1)
		go func(id int) {
			defer writers.Done()
			runLoadWriter(t, id, store, repos, &taskSeq, metrics, &creating, stopWriters)
		}(i)
	}

	// The sustained-load phase: tick RunCycle back to back (no
	// poll-interval delay -- this is a stress test, not a realistic
	// cadence) for cfg.duration while cfg.writers goroutines create and
	// mutate tasks concurrently through their own store connections.
	deadline := time.Now().Add(cfg.duration)
	for time.Now().Before(deadline) {
		runOneTick(t, ctx, store, deps, metrics)
	}

	// Drain: stop creating new tasks, but let writers keep approving,
	// replying and closing so a parked or proposed task still has a way
	// to resolve, and keep ticking until the queue empties or
	// cfg.drainTimeout runs out.
	creating.Store(false)
	drainDeadline := time.Now().Add(cfg.drainTimeout)
	for time.Now().Before(drainDeadline) {
		if storeIsQuiet(t, ctx, store) {
			break
		}
		runOneTick(t, ctx, store, deps, metrics)
	}

	// Only now do writers actually stop -- closing and waiting on them
	// before this loop's own last check would leave a race exactly like
	// the one this harness caught while this file was still being
	// written: a writer approving or reopening a task in the gap between
	// that check and stopWriters closing leaves a freshly ready task no
	// further tick would ever see. A short, writer-free tail below mops
	// up anything created in that last window instead.
	close(stopWriters)
	writers.Wait()
	for i := 0; i < 5 && !storeIsQuiet(t, ctx, store); i++ {
		runOneTick(t, ctx, store, deps, metrics)
	}

	assertDrainedCleanly(t, ctx, store)
	assertConsoleSessionsDispatched(t, ctx, store)
	reportConcurrency(t, metrics)
	reportCapability(t, ctx, capability)
	reportMetrics(t, metrics)
	reportCycles(t, cycles, metrics.tickCount(), cfg.dispatchBudget)
}

// assertConsoleSessionsDispatched is the check that keeps this harness's
// own capability wiring honest at run time, where
// TestLoadHarnessCoversDefaultConsoleSessions only reads the list: at
// least one of the tasks RunCycle filed for a shipped console session
// has to have actually been dispatched by the end of the run.
//
// Without it, a run in which the director never once reached a dispatch
// -- because nothing filed it, or because every candidate was skipped --
// passes while proving nothing about the path that broke, which is the
// one a load writer shares with it. A director that is filed and then
// refused does not get this far: RunCycle returns the refusal and
// runOneTick fails on it, which is how the original breakage showed up.
func assertConsoleSessionsDispatched(t *testing.T, ctx context.Context, store *model.Store) {
	t.Helper()
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	var sessionTasks, dispatched int
	for _, task := range tasks {
		if _, ok := model.ConsoleSessionIDOf(task); !ok {
			continue
		}
		sessionTasks++
		runs, err := store.Runs(ctx, task.ID)
		if err != nil {
			t.Fatalf("Runs(%s): %v", task.ID, err)
		}
		if len(runs) > 0 {
			dispatched++
		}
	}
	if sessionTasks == 0 {
		t.Errorf("console sessions: RunCycle filed no task for any shipped session across this run -- "+
			"the dispatch path this harness shares with grain's own director went untested (%d task(s) in the store)", len(tasks))
		return
	}
	if dispatched == 0 {
		t.Errorf("console sessions: %d session task(s) filed, none of them ever dispatched -- "+
			"every one was skipped, so the capabilities those sessions hold were never resolved", sessionTasks)
	}
}

// storeIsQuiet reports whether nothing is ready and no slot is occupied
// -- draining's own stopping condition, used both while writers may still
// be creating work and, briefly, once they cannot be anymore.
func storeIsQuiet(t *testing.T, ctx context.Context, store *model.Store) bool {
	t.Helper()
	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	occupied, err := store.LiveRunCount(ctx)
	if err != nil {
		t.Fatalf("LiveRunCount: %v", err)
	}
	return len(ready) == 0 && occupied == 0
}

// loadTickBudget is how long one RunCycle call gets before this harness
// gives up on it. A tick that blows it comes back as a context deadline
// -- an error runOneTick does not tolerate -- so it is already the hard
// ceiling on a tick here, which is what makes it the natural thing for
// reportCycles' own, tighter dispatch budget to be a fraction of rather
// than a number picked on its own.
const loadTickBudget = 30 * time.Second

// loadDispatchBudget is how much of a tick may go by, at p95, before
// RunCycle reaches the dispatch decision. A dispatch wait above it means
// a queued task here is waiting on the tick itself rather than on a busy
// deployment -- the two causes of a queue wait pkg/metrics' own Cycles
// section exists to tell apart, seen from the side that can fail a test
// about it. A third of the tick budget, so it trips well before the
// deadline above does and says something sharper when it dies.
const loadDispatchBudget = loadTickBudget / 3

// loadCycleHistory is how many ticks the CycleTimes ring keeps. A
// deployment's own DefaultCycleHistory (720) is six hours of a 30s
// -poll-interval; this harness ticks back to back, so 720 would be the
// last few seconds of a run lasting minutes, and the shape across the
// whole run is the point. Sized instead for a default run to fit whole,
// at a few megabytes; a longer one truncates the oldest cycles, exactly
// as a deployment's ring does, and reportCycles says so when it has.
const loadCycleHistory = 20000

func runOneTick(t *testing.T, ctx context.Context, store *model.Store, deps orchestrator.Deps, metrics *loadMetrics) {
	t.Helper()
	tickCtx, cancel := context.WithTimeout(ctx, loadTickBudget)
	defer cancel()
	start := time.Now()
	if err := orchestrator.RunCycle(tickCtx, deps, start.UTC()); err != nil {
		// A writer's own "close" action deliberately avoids a task it
		// last saw running (doLoadWriterAction's own comment), but the
		// read-then-act gap between that check and the close landing is
		// real, so this race still happens occasionally: RunDispatch's
		// own cancel-on-close (run.go's errTaskClosed) firing exactly
		// when a task is closed out from under a run genuinely still
		// live. That is expected, already-tested behavior here, not a
		// failure this file exists to catch -- see reconcileDispatch's
		// own doc comment on why one dispatch's error never fails the
		// cycle either.
		if !strings.Contains(err.Error(), "task closed while its run was still live") {
			t.Fatalf("RunCycle: %v", err)
		}
		t.Logf("RunCycle (tolerated -- close/cancel race): %v", err)
	}
	// How long that took is deps.CycleTimes' to say, not this loop's --
	// all this counts is that a tick happened, which is what lets
	// reportCycles check the ring saw every one of them.
	metrics.tickRan()
	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	metrics.observeReadyTasks(ready, time.Now())
}

// createLoadTask files one task directly (bypassing the slower ui.Client
// round trip -- fine for the initial backlog and for a writer's own
// "create" action, since ui.Client.CreateTask's extra bookkeeping,
// dependsOnLinks/grantsFor, has nothing to resolve for a task with no
// dependencies and a capability granted directly). 80% land already
// approved (ready at once); the rest land proposed, giving writers'
// "approve" action something to do. 30% carry the fake capability grant,
// enough to make an outstanding-resource leak or a successful reap
// visible without every single dispatch paying for one.
func createLoadTask(t *testing.T, ctx context.Context, store *model.Store, seq *atomic.Int64,
	repos []model.RepoRef, rng *rand.Rand, metrics *loadMetrics) {
	t.Helper()
	id := fmt.Sprintf("load-%07d", seq.Add(1))
	repo := repos[rng.IntN(len(repos))]
	approved := rng.Float64() < 0.8
	withCapability := rng.Float64() < 0.3
	now := time.Now().UTC()
	task := newLoadTask(id, repo, approved, withCapability, now)
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("filing %s: %v", id, err)
	}
	metrics.taskFiled()
	if approved {
		metrics.taskBecameReady(id, now)
	}
}

func newLoadTask(id string, repo model.RepoRef, approved, withCapability bool, now time.Time) model.Task {
	var actor model.Principal
	var reason model.OriginReason
	if approved {
		// model.LandsQueued: a human actor lands queued outright.
		actor = model.Principal{Kind: model.PrincipalHuman, ID: "load-operator"}
		reason = model.ReasonDirect
	} else {
		actor = model.Principal{Kind: model.PrincipalAutomation, ID: "load-generator"}
		reason = model.ReasonProposal
	}
	task := model.Task{
		ID: id, Intent: model.IntentImplement, Title: "load task " + id,
		Origin:    model.Origin{Attribution: model.Attribution{Actor: actor}, Reason: reason},
		Binding:   model.BindingDirective,
		Target:    &repo,
		CreatedAt: &now,
	}
	if approved {
		task.Approval = &model.Attribution{Actor: actor}
	}
	if withCapability {
		task.Grants = []model.Grant{{Capability: loadCapabilityName, Via: model.GrantByGrain}}
	}
	return task
}

// runLoadWriter is this harness's stand-in for pkg/ui's own concurrently
// served HTTP handlers (pkg/ui's own doc comment: "that process still
// serves concurrent requests ... safe to run concurrently") -- several
// goroutines calling into the very same *model.Store the RunCycle loop
// above is also, at that same moment, ticking against. That is v2's
// actual concurrency shape today: one process, one *model.Store, the UI's
// own concurrent request goroutines and the reconcile loop all sharing
// it (pkg/cli/daemon.go's own doc comment -- the CLI is a REST client
// of that same UI now, not a second direct writer of the file, so this
// harness does not open a second *sql.DB to stand in for it). What still
// makes this a real stress on pkg/model/sqlite's "single writer" design
// is that database/sql opens more than one underlying connection to the
// same file when called from more than one goroutine at once, so
// SQLite's own file lock (_txlock=immediate, busy_timeout) is fully in
// play regardless of every caller sharing one *model.Store Go value --
// nothing in the ordinary suite ever drives that store concurrently with
// a live RunCycle tick the way this file does (withStore's own doc
// comment: existing tests take turns by design).
func runLoadWriter(t *testing.T, id int, store *model.Store, repos []model.RepoRef, seq *atomic.Int64,
	metrics *loadMetrics, creating *atomic.Bool, stop <-chan struct{}) {
	t.Helper()
	seed := uint64(id)*0x9e3779b97f4a7c15 + 1
	rng := rand.New(rand.NewPCG(seed, seed^0xff51afd7ed558ccd))

	for {
		select {
		case <-stop:
			return
		default:
		}
		doLoadWriterAction(context.Background(), store, rng, repos, seq, metrics, creating.Load())
		time.Sleep(time.Duration(50+rng.IntN(100)) * time.Millisecond)
	}
}

func doLoadWriterAction(ctx context.Context, store *model.Store, rng *rand.Rand, repos []model.RepoRef,
	seq *atomic.Int64, metrics *loadMetrics, creating bool) {

	states, err := store.States(ctx)
	if err != nil {
		return // a transient read failure costs this one action, nothing more
	}
	client := ui.NewClient(ui.Config{Actor: ui.DefaultActor("load-writer")}, store)

	roll := rng.Float64()
	switch {
	case creating && roll < 0.2:
		id := fmt.Sprintf("load-%07d", seq.Add(1))
		repo := repos[rng.IntN(len(repos))]
		approved := rng.Float64() < 0.8
		withCapability := rng.Float64() < 0.3
		now := time.Now().UTC()
		start := time.Now()
		err := store.PutTask(ctx, newLoadTask(id, repo, approved, withCapability, now))
		metrics.recordWrite(time.Since(start), err)
		if err == nil {
			metrics.taskFiled()
			if approved {
				metrics.taskBecameReady(id, now)
			}
		}
	case roll < 0.45:
		if id, ok := loadPickState(states, model.StateProposed, rng); ok {
			start := time.Now()
			err := client.Approve(ctx, id)
			metrics.recordWrite(time.Since(start), benignOrErr(err))
			if err == nil {
				metrics.taskBecameReady(id, time.Now())
			}
		}
	case roll < 0.65:
		if id, ok := loadPickState(states, model.StateAwaitingReply, rng); ok {
			start := time.Now()
			err := client.AddComment(ctx, id, "here is some direction", nil)
			metrics.recordWrite(time.Since(start), benignOrErr(err))
			if err == nil {
				metrics.taskBecameReady(id, time.Now())
			}
		}
	case roll < 0.85:
		// Never a currently running task: cancel-on-close
		// (RunDispatch's own watchForTaskClosed) is a real, already
		// separately tested code path, not one of the three things this
		// file exists to catch, and racing it in here would only turn
		// RunCycle's own reported error into noise this test would
		// otherwise have to specifically parse out again.
		if id, ok := loadPickWhere(states, rng, func(s model.State) bool {
			return s != model.StateClosed && s != model.StateRunning
		}); ok {
			start := time.Now()
			err := client.Close(ctx, id, ui.CloseOptions{})
			metrics.recordWrite(time.Since(start), benignOrErr(err))
		}
	case creating:
		if id, ok := loadPickState(states, model.StateClosed, rng); ok {
			start := time.Now()
			err := client.Reopen(ctx, id)
			metrics.recordWrite(time.Since(start), benignOrErr(err))
			if err == nil {
				metrics.taskBecameReady(id, time.Now())
			}
		}
	}
}

// benignOrErr drops a *ui.NotFoundError or *ui.ValidationError to nil --
// both are an ordinary, expected outcome of several goroutines picking
// among the same tasks at random (one writer closes a task between
// another reading states and acting on it), not a sign of sqlite
// contention, which is what metrics.recordWrite's own error count exists
// to catch.
func benignOrErr(err error) error {
	switch err.(type) {
	case *ui.NotFoundError, *ui.ValidationError:
		return nil
	default:
		return err
	}
}

func loadPickState(states map[string]model.State, want model.State, rng *rand.Rand) (string, bool) {
	return loadPickWhere(states, rng, func(s model.State) bool { return s == want })
}

func loadPickWhere(states map[string]model.State, rng *rand.Rand, ok func(model.State) bool) (string, bool) {
	var candidates []string
	for id, s := range states {
		if ok(s) {
			candidates = append(candidates, id)
		}
	}
	if len(candidates) == 0 {
		return "", false
	}
	return candidates[rng.IntN(len(candidates))], true
}

// loadRetryBackoff mirrors pkg/dispatch's own unexported retryBackoff
// (baseRetryBackoff 30s, doubling, capped at 30m) -- duplicated rather
// than exported, the same "small helpers are duplicated per test" choice
// harness_test.go's own package doc comment already makes for this
// package, so assertDrainedCleanly can tell a task legitimately still
// backing off after a failed run apart from one dispatch.Cycle should
// have picked up again by now but did not.
func loadRetryBackoff(streak int) time.Duration {
	if streak <= 0 {
		return 0
	}
	backoff := 30 * time.Second * time.Duration(uint64(1)<<uint(streak-1))
	if backoff > 30*time.Minute || backoff <= 0 {
		return 30 * time.Minute
	}
	return backoff
}

// assertDrainedCleanly is this file's scheduling-starvation check: no
// slot may still be occupied (a dispatched run that never finished blocks
// its slot forever, exactly the "cluster ... blocks" failure mode
// random_test.go's own doc comment names), and no ready task may still be
// sitting in the queue unless it is legitimately still backing off after
// a recent failure -- dispatch.Cycle's own retryEligible check, mirrored
// here since task_ready itself (Store.Ready) has no notion of time
// (dispatch.go's own doc comment) and so lists a backing-off task too.
func assertDrainedCleanly(t *testing.T, ctx context.Context, store *model.Store) {
	t.Helper()
	occupied, err := store.LiveRunCount(ctx)
	if err != nil {
		t.Fatalf("LiveRunCount: %v", err)
	}
	if occupied != 0 {
		t.Errorf("scheduling: %d slot(s) still occupied after draining -- a dispatched run never finished: %v", occupied, occupied)
	}

	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	var stuck []string
	for _, id := range ready {
		streak, err := store.FailureStreak(ctx, id)
		if err != nil {
			t.Fatalf("FailureStreak(%s): %v", id, err)
		}
		if streak == nil || streak.Count == 0 {
			stuck = append(stuck, id)
			continue
		}
		if time.Since(streak.LastFinishedAt) >= loadRetryBackoff(streak.Count) {
			stuck = append(stuck, id)
		}
	}
	if len(stuck) != 0 {
		t.Errorf("scheduling starvation: %d task(s) still ready with no active retry backoff after draining -- dispatch.Cycle should have picked them up: %v", len(stuck), stuck)
	} else if len(ready) != 0 {
		t.Logf("%d task(s) still ready after draining, all legitimately backing off after a recent failure", len(ready))
	}
}

// reportConcurrency logs whether dispatched runs actually overlapped in
// wall-clock time -- see loadMetrics.concurrencyRatio's own doc comment.
// A ratio at or near 1.0x despite cfg.slots > 1 free slots and several
// tasks ready means every dispatch this run made actually executed one
// at a time, regardless of how many slots were configured.
func reportConcurrency(t *testing.T, metrics *loadMetrics) {
	t.Helper()
	ratio, n := metrics.concurrencyRatio()
	t.Logf("concurrency: %d dispatch(es) measured, overlap ratio %.2fx "+
		"(1.0x means dispatches never actually overlapped, regardless of how many slots were free)", n, ratio)
}

// reportCapability calls Reap the same way pkg/cli/daemon.go's own
// reapCapabilities does on its hourly timer -- here, once, now that
// assertDrainedCleanly has already confirmed no run is still live, so
// every capability this run ever materialized has already had its own
// chance at a normal Revoke by the time this looks for what that missed.
func reportCapability(t *testing.T, ctx context.Context, cap *loadCapability) {
	t.Helper()
	if _, err := cap.Reap(ctx, nil, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	stats := cap.Stats()
	t.Logf("capability: minted=%d revoked=%d revoke_failures=%d reaped=%d still_outstanding=%d",
		stats.Minted, stats.Revoked, stats.RevokeFails, stats.Reaped, stats.StillOutstanding)
	if stats.StillOutstanding != 0 {
		t.Errorf("capability leak: %d resource(s) still outstanding after Reap", stats.StillOutstanding)
	}
	if stats.RevokeFails > 0 && stats.Reaped == 0 {
		t.Errorf("capability: %d revoke failure(s) were injected but Reap never reclaimed anything -- the leak-recovery path was never actually exercised", stats.RevokeFails)
	}
}

func reportMetrics(t *testing.T, metrics *loadMetrics) {
	t.Helper()
	s := metrics.snapshot()
	t.Logf("tasks: filed=%d max_ready_depth=%d unresolved_at_end=%d outcomes=%v",
		s.filed, s.maxReady, s.stillUnresolved, s.outcomes)
	t.Logf("ready-to-resolved latency: n=%d min=%v p50=%v p95=%v max=%v",
		s.readyWait.N, s.readyWait.Min, s.readyWait.P50, s.readyWait.P95, s.readyWait.Max)
	t.Logf("sqlite writes: n=%d errors=%d min=%v p50=%v p95=%v max=%v",
		s.writeOps, s.writeErrs, s.write.Min, s.write.P50, s.write.P95, s.write.Max)

	if s.writeOps > 0 && s.writeErrs*20 > s.writeOps { // more than 5% failed outright
		t.Errorf("sqlite contention: %d/%d concurrent store write(s) failed even after Store.write's own retry loop", s.writeErrs, s.writeOps)
	}
}

// reportCycles reports what this run's ticks measured about themselves,
// and is the one place this file looks at the tick at all -- the numbers
// come out of the CycleTimes ring RunCycle wrote them into, which is the
// same ring, filled by the same code, that a deployment serves as GET
// /api/metrics' "cycles" section (see this file's own doc comment).
//
// ticks is how many times the loops above called RunCycle, and is not a
// second measurement of anything: it is the cross-check that the ring
// really saw every pass this harness drove, so a Deps that stopped
// carrying a CycleTimes -- or a cycle quietly declining to record itself
// -- fails here rather than silently reporting a tick nobody measured.
// dispatchBudget is the p95 dispatch wait this run fails on, or zero to
// print the figure and judge nothing -- see loadConfig.dispatchBudget
// for which run passes which and why.
func reportCycles(t *testing.T, cycles *orchestrator.CycleTimes, ticks int, dispatchBudget time.Duration) {
	t.Helper()
	history, observed := cycles.History()
	if observed == 0 {
		t.Errorf("cycles: nothing measured across %d tick(s) -- RunCycle is not writing into the ring this harness handed its Deps", ticks)
		return
	}
	if observed != ticks {
		t.Errorf("cycles: %d tick(s) driven, %d measured -- a cycle declines to record itself only when its context is already done, so either this is not the ring RunCycle was handed or a tick spent its whole %v budget getting back",
			ticks, observed, loadTickBudget)
	}
	if len(history) < observed {
		t.Logf("cycles: reporting the most recent %d of %d tick(s) -- the rest have aged out of a %d-cycle ring",
			len(history), observed, loadCycleHistory)
	}

	// Per reconciler, in the order the cycle ran them, which is the order
	// that explains them: they run in sequence, so one that has grown is
	// time every decision behind it did not get to spend.
	reconcilers := orchestrator.Reconcilers()
	order := make([]string, 0, len(reconcilers))
	for _, r := range reconcilers {
		order = append(order, r.Name)
	}
	waits := map[string][]time.Duration{}
	durations := map[string][]time.Duration{}
	failures := map[string]int{}

	var tickDur, dispatchWait []time.Duration
	incomplete := 0
	var firstIncomplete []string
	for _, c := range history {
		tickDur = append(tickDur, c.Duration)
		dispatchWait = append(dispatchWait, c.DispatchWait)
		var ran []string
		for _, r := range c.Reconcilers {
			ran = append(ran, r.Name)
			waits[r.Name] = append(waits[r.Name], r.Wait)
			durations[r.Name] = append(durations[r.Name], r.Duration)
			if r.Failed {
				failures[r.Name]++
			}
		}
		if !slices.Equal(ran, order) {
			if incomplete == 0 {
				firstIncomplete = ran
			}
			incomplete++
		}
	}

	tick := statsOf(tickDur)
	dispatch := statsOf(dispatchWait)
	t.Logf("RunCycle tick duration: n=%d min=%v p50=%v p95=%v max=%v",
		tick.N, tick.Min, tick.P50, tick.P95, tick.Max)
	t.Logf("RunCycle dispatch wait: n=%d min=%v p50=%v p95=%v max=%v (how far into a tick the dispatch decision was reached)",
		dispatch.N, dispatch.Min, dispatch.P50, dispatch.P95, dispatch.Max)
	for _, name := range order {
		d, w := statsOf(durations[name]), statsOf(waits[name])
		t.Logf("  %-14s duration p50=%v p95=%v max=%v; started p50=%v p95=%v into the tick; failed %d/%d cycle(s)",
			name, d.P50, d.P95, d.Max, w.P50, w.P95, failures[name], d.N)
	}
	// A tick growing under a store that is filling up, which is the
	// failure this harness is best placed to catch: the same p50 over the
	// first half of the cycles still in the ring and over the second.
	// Logged rather than asserted on -- a tick that grows somewhat as
	// tasks accumulate is the expected shape, and any threshold on
	// "somewhat" would be a number invented here rather than one this
	// harness knows -- but it is the line to read first when the dispatch
	// budget below does trip.
	if half := len(tickDur) / 2; half > 0 {
		early, late := statsOf(tickDur[:half]), statsOf(tickDur[half:])
		t.Logf("tick growth: p50 %v over the first %d cycle(s) -> %v over the last %d",
			early.P50, half, late.P50, len(tickDur)-half)
	}

	// A cycle that did not run every reconciler is a cycle cut short, and
	// one that never reached "dispatch" decided nothing at all while
	// tasks sat ready -- the starvation this file exists to catch, seen
	// from inside the tick rather than from the queue.
	if incomplete > 0 {
		t.Errorf("cycles: %d/%d tick(s) did not run every reconciler -- first one ran [%s], where the cycle runs [%s]",
			incomplete, len(history), strings.Join(firstIncomplete, ","), strings.Join(order, ","))
	}
	if dispatchBudget > 0 && dispatch.P95 > dispatchBudget {
		t.Errorf("scheduling: p95 dispatch wait %v exceeds %v -- that much of a tick went by before RunCycle reached the dispatch decision, so a queued task is waiting on the tick itself, not on a full deployment (the per-reconciler lines above say which part of the tick grew)",
			dispatch.P95, dispatchBudget)
	}
}
