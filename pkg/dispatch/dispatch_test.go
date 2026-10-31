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

package dispatch_test

// Cycle against a real embedded SQLite database, the same discipline
// model/sqlite/store_test.go holds to: these prove the dispatch decision
// is correct against the actual views it reads, not a fake standing in
// for them. bwsalmon/agents#219 asked for exactly this and nothing
// more — no sandbox, no GitHub, no agent — so every "outcome" below is
// simulated with the store calls a later, side-effecting change would
// make for real: FinishRun, Observe.

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

var (
	now   = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	human = model.Principal{Kind: model.PrincipalHuman, ID: "bwsalmon"}
)

func open(t *testing.T) (*model.Store, context.Context) {
	t.Helper()
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

func task(id string, approved bool, links ...model.Link) model.Task {
	tk := model.Task{
		ID: id, Intent: model.IntentImplement, Title: "task " + id,
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: human},
			Reason:      model.ReasonDirect,
		},
		Binding: model.BindingDirective,
		Links:   links,
	}
	if approved {
		tk.Approval = &model.Attribution{Actor: human}
	}
	return tk
}

// fixTask builds one of the separate tasks the merge queue used to file
// to repair a stuck queue head (Origin.Reason == ReasonFix), including
// the place in the backlog it was filed at: the very head, ahead of the
// zero OrderKey every task() here carries. That position is the whole of
// why it dispatches first, so a fix task built without it would prove
// nothing. A repair is another attempt of the head task itself now
// (orchestrator.requeueForRepair), but such a task is still a merger and
// a database can still hold one of these.
func fixTask(id string) model.Task {
	tk := task(id, true)
	tk.Origin.Reason = model.ReasonFix
	tk.OrderKey = -1
	return tk
}

func putTasks(t *testing.T, store *model.Store, ctx context.Context, tasks ...model.Task) {
	t.Helper()
	for _, tk := range tasks {
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatalf("put task %s: %v", tk.ID, err)
		}
	}
}

func directorTask(id string) model.Task {
	tk := task(id, true)
	tk.Origin.Reason = model.ReasonConsoleSession
	tk.Tags = []string{model.ConsoleSessionTag(model.DirectorSessionID)}
	tk.Interactive = true
	tk.Grants = model.ConsoleSessionGrants(model.ConsoleSession{ID: model.DirectorSessionID})
	return tk
}

// The Director's run is additional to ordinary worker capacity, while
// every other task remains bounded by Workers. Ending that turn removes
// only the session's occupancy and leaves the ordinary accounting exactly
// where it was.
func TestDirectorDispatchBypassesAFullWorkerPool(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("worker-1", true), task("worker-2", true))
	limits := model.Limits{Workers: 1}

	first, err := dispatch.Cycle(ctx, store, limits, now)
	if err != nil || len(first) != 1 || first[0].TaskID != "worker-1" {
		t.Fatalf("filling ordinary capacity: dispatch = %+v, err = %v", first, err)
	}

	putTasks(t, store, ctx, directorTask("director"))
	second, err := dispatch.Cycle(ctx, store, limits, now.Add(time.Second))
	if err != nil || len(second) != 1 || second[0].TaskID != "director" {
		t.Fatalf("dispatch at full worker capacity = %+v, err = %v; want the Director immediately", second, err)
	}
	if live, err := store.LiveRunCounts(ctx); err != nil || live != (model.RunCounts{Workers: 1, Directors: 1}) {
		t.Fatalf("live counts with Director bypass = %+v, err = %v", live, err)
	}

	third, err := dispatch.Cycle(ctx, store, limits, now.Add(2*time.Second))
	if err != nil || len(third) != 0 {
		t.Fatalf("cycle while Director attempt is live = %+v, err = %v; want no duplicate", third, err)
	}
	if runs, err := store.Runs(ctx, "worker-2"); err != nil || len(runs) != 0 {
		t.Fatalf("ordinary task claimed bypass capacity: runs = %+v, err = %v", runs, err)
	}

	if err := store.FinishRun(ctx, second[0].RunID, now.Add(time.Minute), "succeeded", "answered"); err != nil {
		t.Fatal(err)
	}
	if live, err := store.LiveRunCounts(ctx); err != nil || live != (model.RunCounts{Workers: 1}) {
		t.Fatalf("live counts after Director ended = %+v, err = %v; want only the ordinary worker", live, err)
	}
}

// Bypass identity is the complete canonical shape written by the
// console-session reconciler, not a user-facing task field. Neither an
// arbitrary interactive task nor a forged Director title gets it.
func TestInteractiveAndTitledTasksCannotClaimDirectorCapacity(t *testing.T) {
	store, ctx := open(t)
	worker := task("worker", true)
	interactive := task("interactive", true)
	interactive.Interactive = true
	interactive.OrderKey = 1
	titled := task("titled", true)
	titled.Title = "Director"
	titled.OrderKey = 2
	tagged := task("tagged", true)
	tagged.Tags = []string{model.ConsoleSessionTag(model.DirectorSessionID)}
	tagged.Interactive = true
	tagged.OrderKey = 3
	reasonOnly := task("reason-only", true)
	reasonOnly.Origin.Reason = model.ReasonConsoleSession
	reasonOnly.Interactive = true
	reasonOnly.OrderKey = 4
	putTasks(t, store, ctx, worker, interactive, titled, tagged, reasonOnly)

	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now)
	if err != nil || len(got) != 1 || got[0].TaskID != "worker" {
		t.Fatalf("dispatch = %+v, err = %v; want only one ordinary worker", got, err)
	}
	for _, id := range []string{"interactive", "titled", "tagged", "reason-only"} {
		if runs, err := store.Runs(ctx, id); err != nil || len(runs) != 0 {
			t.Errorf("%s claimed Director bypass: runs = %+v, err = %v", id, runs, err)
		}
	}
}

// Creating and dispatching any new console session succeeds even when
// concurrent worker limits and system overcommit/budget limits are saturated.
func TestNewConsoleSessionDispatchesUnderSaturationAndOvercommit(t *testing.T) {
	store, ctx := open(t)
	worker1 := task("w1", true)
	worker1.OrderKey = 1
	worker2 := task("w2", true)
	worker2.OrderKey = 2

	newSession := task("custom-session-1", true)
	newSession.Origin.Reason = model.ReasonConsoleSession
	newSession.Interactive = true
	newSession.Tags = []string{model.ConsoleSessionTag("support-ops")}
	newSession.Grants = model.ConsoleSessionGrants(model.ConsoleSession{ID: "support-ops"})
	newSession.OrderKey = 3

	putTasks(t, store, ctx, worker1, worker2, newSession)

	// Ledger fully saturated with budget: Host 8 CPUs, 16 GiB; Live ledger already at 8 CPUs, 16 GiB.
	ledger := &model.Ledger{}
	budget := model.Budget{
		Host:             model.Commitment{CPUs: 8, MemoryMB: 16 * 1024},
		CPUOvercommit:    1,
		MemoryOvercommit: 1,
	}
	agentShape := model.Commitment{CPUs: 4, MemoryMB: 8 * 1024}
	// Charge 8 CPUs and 16 GiB so SandboxAdmits() is false.
	releaseFull := ledger.Charge(model.PoolAgent, "existing-busy-run", model.Commitment{CPUs: 8, MemoryMB: 16 * 1024})
	defer releaseFull()

	limits := model.Limits{
		Workers:    1,
		Mergers:    0,
		Budget:     budget,
		AgentShape: agentShape,
		Ledger:     ledger,
	}

	if limits.SandboxAdmits() {
		t.Fatal("expected SandboxAdmits to be false when budget is saturated")
	}

	// Dispatch cycle: ordinary workers cannot start (overcommit/budget full and workers=1),
	// but the new console session MUST start immediately via the console session bypass.
	dispatches, err := dispatch.Cycle(ctx, store, limits, now)
	if err != nil {
		t.Fatalf("dispatch.Cycle: %v", err)
	}
	if len(dispatches) != 1 || dispatches[0].TaskID != "custom-session-1" {
		t.Fatalf("dispatches = %+v, want only custom-session-1 to dispatch", dispatches)
	}

	// Verify live run counts show 1 director/console-session run and 0 workers.
	live, err := store.LiveRunCounts(ctx)
	if err != nil {
		t.Fatalf("LiveRunCounts: %v", err)
	}
	if live.Directors != 1 || live.Workers != 0 {
		t.Fatalf("live run counts = %+v, want Directors: 1, Workers: 0", live)
	}
}

// Director is prioritized ahead of ordinary tasks and mergers and is never
// blocked by at-capacity ordinary candidates in the ready backlog.
func TestDirectorDispatchesEvenWhenPrecededByAtCapacityOrdinaryTasks(t *testing.T) {
	store, ctx := open(t)
	// Put multiple ordinary tasks ahead of director in order key
	w1 := task("w1", true)
	w1.OrderKey = 1
	w2 := task("w2", true)
	w2.OrderKey = 2
	w3 := task("w3", true)
	w3.OrderKey = 3
	dir := directorTask("director")
	dir.OrderKey = 100

	putTasks(t, store, ctx, w1, w2, w3, dir)
	limits := model.Limits{Workers: 1, Mergers: 0}

	// First cycle starts director (bypass) and w1, while w2 and w3 cannot start.
	first, err := dispatch.Cycle(ctx, store, limits, now)
	if err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("dispatch len = %d, want 2 (director and w1): %+v", len(first), first)
	}
	if first[0].TaskID != "director" || first[1].TaskID != "w1" {
		t.Fatalf("dispatch order = %+v, want director first then w1", first)
	}

	// Also verify that when ordinary worker capacity is full (w1 is live),
	// a director task added later is dispatched even when preceded in ready list by w2, w3.
	store2, ctx2 := open(t)
	putTasks(t, store2, ctx2, task("work1", true), task("work2", true))
	cycle1, err := dispatch.Cycle(ctx2, store2, limits, now)
	if err != nil || len(cycle1) != 1 || cycle1[0].TaskID != "work1" {
		t.Fatalf("cycle1 = %+v, err = %v", cycle1, err)
	}

	putTasks(t, store2, ctx2, task("work3", true), directorTask("director2"))
	cycle2, err := dispatch.Cycle(ctx2, store2, limits, now.Add(time.Second))
	if err != nil || len(cycle2) != 1 || cycle2[0].TaskID != "director2" {
		t.Fatalf("cycle2 = %+v, err = %v; want director2 dispatched despite work2 and work3 in backlog", cycle2, err)
	}
}

func TestCycleDispatchesReadyTasksInOrderAndNoFurther(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("t0", true), task("t1", true), task("t2", true))

	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 2}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("dispatched %d tasks, want 2 (the whole concurrency limit): %+v", len(got), got)
	}

	dispatched := map[string]bool{}
	runs := map[string]bool{}
	for _, d := range got {
		dispatched[d.TaskID] = true
		runs[d.RunID] = true
		if d.Attempt != 1 {
			t.Errorf("%s dispatched with attempt %d, want 1", d.TaskID, d.Attempt)
		}
	}
	if !dispatched["t0"] || !dispatched["t1"] {
		t.Errorf("expected t0 and t1 dispatched (task_ready's own order), got %+v", got)
	}
	if len(runs) != 2 {
		t.Errorf("two dispatches produced %d distinct run ids, want 2: %+v", len(runs), got)
	}

	for _, id := range []string{"t0", "t1"} {
		if st, _ := store.State(ctx, id); st != model.StateRunning {
			t.Errorf("%s state = %q, want running", id, st)
		}
	}
	if st, _ := store.State(ctx, "t2"); st != model.StateQueued {
		t.Errorf("t2 state = %q, want queued (no capacity left for it)", st)
	}
}

func TestCycleLeavesAnAlreadyRunningTaskAlone(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("t0", true))

	first, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 2}, now)
	if err != nil || len(first) != 1 {
		t.Fatalf("first cycle: %v, %+v", err, first)
	}

	// Two slots, still only one task in the world, and it is already
	// running — task_ready excludes it, so a second cycle must dispatch
	// nothing even though slot-2 has never been used.
	second, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 2}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("second cycle dispatched %+v, want nothing: a running task must not be redispatched", second)
	}
}

func TestCycleRespectsTheConcurrencyLimit(t *testing.T) {
	store, ctx := open(t)
	ids := []string{"t0", "t1", "t2", "t3", "t4"}
	for _, id := range ids {
		putTasks(t, store, ctx, task(id, true))
	}
	const maxWorkers = 2

	first, err := dispatch.Cycle(ctx, store, model.Limits{Workers: maxWorkers}, now)
	if err != nil || len(first) != 2 {
		t.Fatalf("first cycle: %v, %+v", err, first)
	}
	live, err := store.LiveRunCount(ctx)
	if err != nil || live != 2 {
		t.Fatalf("live runs after first cycle: %v, %d", err, live)
	}

	// No headroom: nothing more may be dispatched no matter how many
	// tasks are ready.
	second, err := dispatch.Cycle(ctx, store, model.Limits{Workers: maxWorkers}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("cycle at the concurrency limit dispatched %+v", second)
	}

	// Finishing one run frees exactly one unit of capacity, so exactly
	// one more task starts.
	if err := store.FinishRun(ctx, first[0].RunID, now.Add(2*time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	third, err := dispatch.Cycle(ctx, store, model.Limits{Workers: maxWorkers}, now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(third) != 1 {
		t.Fatalf("cycle after one finish dispatched %+v, want exactly 1", third)
	}
	if live, err := store.LiveRunCount(ctx); err != nil || live != 2 {
		t.Errorf("live runs after the freed capacity was refilled = %d (%v), want 2", live, err)
	}
}

func TestCycleSkipsBlockedTasksUntilTheirDependencyCloses(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx,
		task("blocker", true),
		task("blocked", true, model.Link{Kind: model.LinkDependsOn, Target: "blocker"}),
	)

	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 2}, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range got {
		if d.TaskID == "blocked" {
			t.Fatalf("dispatched a blocked task: %+v", got)
		}
	}
	if len(got) != 1 || got[0].TaskID != "blocker" {
		t.Fatalf("expected only the blocker dispatched, got %+v", got)
	}

	// Closing the dependency — not merely completing it, see schema.go's
	// task_blocked view — is what should unblock the task on the very
	// next cycle, with nothing re-applied to it.
	if err := store.Observe(ctx, model.Observation{TaskID: "blocker", ClosedAt: &now}); err != nil {
		t.Fatal(err)
	}
	// Still a limit of 2, and the blocker's own run is still live, so
	// exactly one unit of capacity is free for the unblocked task.
	got, err = dispatch.Cycle(ctx, store, model.Limits{Workers: 2}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != "blocked" {
		t.Fatalf("expected the formerly-blocked task dispatched after its dependency closed, got %+v", got)
	}
}

func TestCycleNeverDispatchesTaskWithDanglingDependency(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx,
		task("ready-independent", true),
		task("blocked-on-dangling", true, model.Link{Kind: model.LinkDependsOn, Target: "non-existent-task"}),
		task("blocked-on-prefixed-dangling", true, model.Link{Kind: model.LinkDependsOn, Target: "grain/task-999"}),
	)

	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 5}, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range got {
		if d.TaskID == "blocked-on-dangling" || d.TaskID == "blocked-on-prefixed-dangling" {
			t.Fatalf("dispatched task with dangling/unresolvable blocker: %s", d.TaskID)
		}
	}
	if len(got) != 1 || got[0].TaskID != "ready-independent" {
		t.Fatalf("expected only ready-independent dispatched, got %+v", got)
	}
}

// TestCycleDispatchesFixTasksBeforeOrdinaryReadyTasks is bwsalmon/
// agents#389: a fix task the merge queue filed for a stuck repo head
// must take a free slot ahead of ordinary new work, even one that has
// been ready longer and would otherwise win task_ready's own tiebreak
// (task ID) -- otherwise the repair sits behind unrelated tasks while
// the branch it targets keeps moving, and has to be refiled once it
// finally runs.
//
// It wins that slot by sitting at the head of the backlog, not by any
// rule of Store.Ready's own: the priority is a position now, which is
// what makes it visible in a task list and movable by a human who
// disagrees with it.
func TestCycleDispatchesFixTasksBeforeOrdinaryReadyTasks(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("a-new-work", true), fixTask("z-fix"))

	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != "z-fix" {
		t.Fatalf("dispatched %+v, want the fix task despite sorting after the other task by ID", got)
	}
}

// When a merge queue repair task sits behind ordinary work in the backlog,
// dispatch.Cycle prefers the repair task so available workers fix broken PRs
// rather than taking new work.
func TestCyclePrefersRepairsOverNewWorkEvenWhenNewWorkIsEarlierInBacklog(t *testing.T) {
	store, ctx := open(t)
	tNew := task("a-new-work", true)
	tNew.OrderKey = 1

	tRepair := task("z-repair", true)
	tRepair.OrderKey = 100
	putTasks(t, store, ctx, tNew, tRepair)

	repairAt := now.Add(-5 * time.Minute)
	if err := store.Observe(ctx, model.Observation{
		TaskID:             tRepair.ID,
		MergeQueueRepairAt: &repairAt,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != "z-repair" {
		t.Fatalf("dispatched %+v, want the repair task z-repair ahead of a-new-work", got)
	}
}

// The three tests below are grain/task-63's whole rule (model.Limits):
// workers are capped at Limits.Workers, everything together is capped at
// Workers+Mergers, and the asymmetry between those two is what lets a
// merge-queue fix task use a worker's free slot while a worker can never
// use a merger's.

// A merge-queue fix task may take capacity ordinary work cannot: with
// three workers and two mergers configured, five fix tasks run at once.
func TestCycleLetsMergersUseWorkerCapacityAsWellAsTheirOwn(t *testing.T) {
	store, ctx := open(t)
	for i := 0; i < 6; i++ {
		putTasks(t, store, ctx, fixTask(fmt.Sprintf("fix-%d", i)))
	}

	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 3, Mergers: 2}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("dispatched %d fix tasks, want 5 (3 worker slots plus 2 merger slots): %+v", len(got), got)
	}
	live, err := store.LiveRunCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if live.Mergers != 5 || live.Workers != 0 {
		t.Fatalf("live runs = %+v, want five mergers and no workers", live)
	}
}

// A worker refused because mergers hold the whole shared pool, not
// because of the worker count, is told so: "0 worker runs are live of
// the 3" was true and explained nothing (grain/task-1328).
func TestCycleNamesTheSharedPoolWhenMergersFillIt(t *testing.T) {
	store, ctx := open(t)
	for i := 0; i < 4; i++ {
		putTasks(t, store, ctx, fixTask(fmt.Sprintf("fix-%d", i)))
	}
	putTasks(t, store, ctx, task("t1", true))

	limits := model.Limits{Workers: 3, Mergers: 1}
	got, err := dispatch.Cycle(ctx, store, limits, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("dispatched %d, want the 4 fix tasks: %+v", len(got), got)
	}
	live, err := store.LiveRunCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if live.Mergers != 4 || live.Workers != 0 {
		t.Fatalf("live runs = %+v, want four mergers and no workers", live)
	}
	if _, err := dispatch.Cycle(ctx, store, limits, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	wait, err := store.GrantWaitOf(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	want := "0 worker and 4 merger runs are live of the 4 this cycle allows (mergers fill the shared pool)"
	if wait == nil || wait.Capability != model.QuotaWaitCapability || wait.Resource != want {
		t.Fatalf("GrantWaitOf(t1) = %+v, want resource %q", wait, want)
	}
}

// Ordinary work is capped at Limits.Workers even when every merger slot
// is idle: the capacity kept back for the merge queue is not a general
// pool with a name on it.
func TestCycleNeverLetsWorkersTakeMergerCapacity(t *testing.T) {
	store, ctx := open(t)
	for i := 0; i < 6; i++ {
		putTasks(t, store, ctx, task(fmt.Sprintf("t%d", i), true))
	}

	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 3, Mergers: 2}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("dispatched %d ordinary tasks, want 3 (the worker limit, with the two merger slots left alone): %+v",
			len(got), got)
	}
}

// CI is independently gated and must not silently shrink the agent
// limits. That made raising max_ci_jobs ineffective once it reached the
// agent pool's size (grain/task-296).
func TestCycleAgentCapacityIsIndependentOfCIJobs(t *testing.T) {
	dispatched := func(t *testing.T, limits model.Limits) int {
		t.Helper()
		store, ctx := open(t)
		for i := 0; i < 6; i++ {
			putTasks(t, store, ctx, fixTask(fmt.Sprintf("fix-%d", i)))
		}
		got, err := dispatch.Cycle(ctx, store, limits, now)
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}

	if got := dispatched(t, model.Limits{Workers: 3, Mergers: 2, CIJobs: 8}); got != 5 {
		t.Errorf("dispatched %d mergers with CI enabled, want all 5 agent slots", got)
	}
	if got := dispatched(t, model.Limits{Workers: 3, Mergers: 2}); got != 5 {
		t.Errorf("dispatched %d mergers with CI off, want all 5 agent slots", got)
	}
	// Ordinary work likewise keeps its configured worker capacity.
	store, ctx := open(t)
	for i := 0; i < 6; i++ {
		putTasks(t, store, ctx, task(fmt.Sprintf("t%d", i), true))
	}
	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 5, Mergers: 1, CIJobs: 2}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("dispatched %d ordinary tasks with CI enabled, want all 5 worker slots: %+v", len(got), got)
	}
	live, err := store.LiveRunCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if live.Total() != 5 {
		t.Fatalf("live runs = %+v, want five in all", live)
	}
}

// A ready task whose own half of the limits is full is passed over, not
// treated as the end of the cycle: with the single worker slot taken,
// the second ordinary task waits while a fix task further down the ready
// order still reaches the capacity kept for it. That skip is the same
// one a task still backing off gets, and it is what makes reserved
// merger capacity reachable at all on a saturated deployment -- the case
// it exists for.
func TestCycleSkipsPastAFullWorkerHalfToReachAMerger(t *testing.T) {
	store, ctx := open(t)
	// Not fixTask's own head-of-backlog position: this one has to sit
	// behind the ordinary work for the skip to be what dispatches it.
	behind := fixTask("z-fix")
	behind.OrderKey = 1
	putTasks(t, store, ctx, task("t0", true), task("t1", true), behind)

	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1, Mergers: 1}, now)
	if err != nil {
		t.Fatal(err)
	}
	dispatched := map[string]bool{}
	for _, d := range got {
		dispatched[d.TaskID] = true
	}
	if len(got) != 2 || !dispatched["t0"] || !dispatched["z-fix"] {
		t.Fatalf("dispatched %+v, want t0 (the one worker slot) and z-fix (the merger slot), with t1 left queued", got)
	}
	live, err := store.LiveRunCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if live.Workers != 1 || live.Mergers != 1 {
		t.Fatalf("live runs = %+v, want one of each", live)
	}
	// And nothing more: the sum of the two limits is the ceiling on
	// everything, so t1 stays queued until one of them ends.
	if again, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1, Mergers: 1}, now.Add(time.Minute)); err != nil || len(again) != 0 {
		t.Fatalf("second cycle dispatched %+v (%v), want nothing past the sum of the two limits", again, err)
	}
}

func TestAttemptNumberIncrementsOnEachRedispatch(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("t0", true))
	const maxWorkers = 1

	first, err := dispatch.Cycle(ctx, store, model.Limits{Workers: maxWorkers}, now)
	if err != nil || len(first) != 1 || first[0].Attempt != 1 {
		t.Fatalf("first dispatch: %v, %+v", err, first)
	}

	// Finishing with no Observation is a requeue, not a completion — the
	// task must come back through task_ready exactly as before.
	if err := store.FinishRun(ctx, first[0].RunID, now.Add(time.Minute), "requeued", ""); err != nil {
		t.Fatal(err)
	}
	second, err := dispatch.Cycle(ctx, store, model.Limits{Workers: maxWorkers}, now.Add(2*time.Minute))
	if err != nil || len(second) != 1 || second[0].Attempt != 2 {
		t.Fatalf("second dispatch: %v, %+v", err, second)
	}
	if second[0].RunID == first[0].RunID {
		t.Errorf("retry reused run id %q", second[0].RunID)
	}
	if n, err := store.Attempts(ctx, "t0"); err != nil || n != 2 {
		t.Fatalf("attempts = %d (%v), want 2", n, err)
	}
}

// TestARecentlyFailedTaskBacksOffWithoutBlockingOthers is bwsalmon/
// agents#403: a task whose last run failed a moment ago must not take a
// free slot again immediately (that is the whole "retried forever with no
// backoff" bug), but it also must not hold up a free slot going to some
// other, unrelated ready task that is not backing off at all.
func TestARecentlyFailedTaskBacksOffWithoutBlockingOthers(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("t0", true), task("t1", true))
	const maxWorkers = 1

	first, err := dispatch.Cycle(ctx, store, model.Limits{Workers: maxWorkers}, now)
	if err != nil || len(first) != 1 || first[0].TaskID != "t0" {
		t.Fatalf("first dispatch: %v, %+v", err, first)
	}
	if err := store.FinishRun(ctx, first[0].RunID, now.Add(time.Second), "failed", "simulated failure"); err != nil {
		t.Fatal(err)
	}

	// t0 just failed and has not backed off yet, but t1 has never run at
	// all -- it must be offered the only free slot instead of the cycle
	// giving up because task_ready's first entry (t0) is not eligible.
	second, err := dispatch.Cycle(ctx, store, model.Limits{Workers: maxWorkers}, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].TaskID != "t1" {
		t.Fatalf("second dispatch = %+v, want t1 dispatched in t0's place", second)
	}
	finished := now.Add(3 * time.Second)
	if err := store.FinishRun(ctx, second[0].RunID, finished, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	// Mark t1 completed so it does not itself come back through
	// task_ready and mask what this test is actually about: whether t0
	// backs off correctly.
	if err := store.Observe(ctx, model.Observation{TaskID: "t1", CompletedAt: &finished}); err != nil {
		t.Fatal(err)
	}

	// Still too soon after t0's own failure: nothing to dispatch even
	// though the slot is free again.
	third, err := dispatch.Cycle(ctx, store, model.Limits{Workers: maxWorkers}, now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(third) != 0 {
		t.Fatalf("third dispatch = %+v, want nothing: t0 has not backed off yet", third)
	}

	// Comfortably past baseRetryBackoff (30s): t0 is eligible again.
	fourth, err := dispatch.Cycle(ctx, store, model.Limits{Workers: maxWorkers}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(fourth) != 1 || fourth[0].TaskID != "t0" || fourth[0].Attempt != 2 {
		t.Fatalf("fourth dispatch = %+v, want t0's second attempt", fourth)
	}
}

// TestATaskCappedAtMaxConsecutiveFailuresStopsBeingReady is bwsalmon/
// agents#403's own cap: once a task has failed model.MaxConsecutiveFailures
// times in a row with nothing succeeding in between, task_state reads it
// as 'failed' rather than 'queued', so it drops out of Ready and Cycle
// never offers it a slot again on its own.
func TestATaskCappedAtMaxConsecutiveFailuresStopsBeingReady(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("t0", true))
	const maxWorkers = 1

	clock := now
	for i := 0; i < model.MaxConsecutiveFailures; i++ {
		// Advance well past any backoff window so the cap itself, not a
		// still-pending backoff, is what this test exercises.
		clock = clock.Add(time.Hour)
		got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: maxWorkers}, clock)
		if err != nil {
			t.Fatalf("attempt %d: Cycle: %v", i+1, err)
		}
		if len(got) != 1 {
			t.Fatalf("attempt %d: dispatched %+v, want exactly one run", i+1, got)
		}
		clock = clock.Add(time.Second)
		if err := store.FinishRun(ctx, got[0].RunID, clock, "failed", "simulated failure"); err != nil {
			t.Fatalf("attempt %d: FinishRun: %v", i+1, err)
		}
	}

	st, err := store.State(ctx, "t0")
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateFailed {
		t.Fatalf("state after %d consecutive failures = %q, want failed", model.MaxConsecutiveFailures, st)
	}

	clock = clock.Add(24 * time.Hour)
	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: maxWorkers}, clock)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Cycle dispatched a capped task: %+v", got)
	}

	// A human's own retry request is the only way out: it clears the
	// streak, and the task is dispatchable again despite no run ever
	// having succeeded.
	if err := store.ObserveField(ctx, "t0", clock, func(o *model.Observation) {
		o.RetryRequestedAt = &clock
	}); err != nil {
		t.Fatal(err)
	}
	if st, err := store.State(ctx, "t0"); err != nil || st != model.StateQueued {
		t.Fatalf("state after a retry request = %q (%v), want queued", st, err)
	}
	clock = clock.Add(time.Minute)
	got, err = dispatch.Cycle(ctx, store, model.Limits{Workers: maxWorkers}, clock)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != "t0" {
		t.Fatalf("Cycle after a retry request = %+v, want t0 dispatched once more", got)
	}
}

// TestInvariantsHoldAcrossManyRandomCycles is the property this package
// exists to guarantee, checked the way a property survives rather than
// the way one example does: build a small DAG of tasks, drive Cycle for
// many cycles against a real store, and after every single one confirm
// three things no cycle may ever violate — no slot holds two runs, no
// task holds two runs, and no blocked task ever runs — while a runs of
// outcomes (retried, completed, closed) is fed back in exactly the way a
// side-effecting layer will once #219 grows one. The seed is fixed so a
// failure is reproducible rather than a flake to chase.
func TestInvariantsHoldAcrossManyRandomCycles(t *testing.T) {
	store, ctx := open(t)
	rng := rand.New(rand.NewPCG(1, 219))

	const numTasks = 10
	ids := make([]string, numTasks)
	dependsOn := map[string]string{}
	for i := 0; i < numTasks; i++ {
		ids[i] = fmt.Sprintf("tk%02d", i)
		var links []model.Link
		if i > 0 && rng.Float64() < 0.5 {
			parent := ids[rng.IntN(i)]
			dependsOn[ids[i]] = parent
			links = []model.Link{{Kind: model.LinkDependsOn, Target: parent}}
		}
		putTasks(t, store, ctx, task(ids[i], true, links...))
	}

	const maxWorkers = 3
	closed := map[string]bool{}
	liveRunID := map[string]string{}   // task -> its current run, while live
	liveSandbox := map[string]string{} // task -> the sandbox its live run was given
	sandboxOf := map[string]string{}   // sandbox -> task, while that run is live
	attemptsSoFar := map[string]int{}
	clock := now

	unblocked := func(id string) bool {
		parent, ok := dependsOn[id]
		return !ok || closed[parent]
	}

	for cycle := 0; cycle < 60; cycle++ {
		clock = clock.Add(time.Minute)
		dispatches, err := dispatch.Cycle(ctx, store, model.Limits{Workers: maxWorkers}, clock)
		if err != nil {
			t.Fatalf("cycle %d: %v", cycle, err)
		}

		for _, d := range dispatches {
			if _, already := liveRunID[d.TaskID]; already {
				t.Fatalf("cycle %d: %s dispatched while already running", cycle, d.TaskID)
			}
			if holder, taken := sandboxOf[d.RunID]; taken {
				t.Fatalf("cycle %d: run id %s handed to %s while still held by %s",
					cycle, d.RunID, d.TaskID, holder)
			}
			if closed[d.TaskID] {
				t.Fatalf("cycle %d: closed task %s was dispatched", cycle, d.TaskID)
			}
			if !unblocked(d.TaskID) {
				t.Fatalf("cycle %d: blocked task %s was dispatched", cycle, d.TaskID)
			}
			if want := attemptsSoFar[d.TaskID] + 1; d.Attempt != want {
				t.Fatalf("cycle %d: %s dispatched with attempt %d, want %d",
					cycle, d.TaskID, d.Attempt, want)
			}
			liveRunID[d.TaskID] = d.RunID
			// A run's sandbox is named after the run itself, which is
			// what orchestrator.runOne records; dispatch never invents
			// one, so the mirror derives it the same way.
			liveSandbox[d.TaskID] = d.RunID
			sandboxOf[d.RunID] = d.TaskID
			attemptsSoFar[d.TaskID]++
		}

		if len(sandboxOf) > maxWorkers {
			t.Fatalf("cycle %d: %d runs live, limit is %d", cycle, len(sandboxOf), maxWorkers)
		}
		if occ, err := store.LiveRunCount(ctx); err != nil {
			t.Fatalf("cycle %d: LiveRunCount: %v", cycle, err)
		} else if occ != len(sandboxOf) {
			t.Fatalf("cycle %d: store reports %d live runs, mirror expects %d",
				cycle, occ, len(sandboxOf))
		}

		// Feed back outcomes for a random subset of live runs — exactly
		// the store calls a real dispatcher will make once side effects
		// exist, simulated here since #219 explicitly defers them.
		for _, id := range ids {
			runID, isLive := liveRunID[id]
			if !isLive || rng.Float64() >= 0.4 {
				continue
			}
			clock = clock.Add(time.Minute)
			if err := store.FinishRun(ctx, runID, clock, "outcome", ""); err != nil {
				t.Fatalf("cycle %d: finishing %s: %v", cycle, runID, err)
			}
			delete(sandboxOf, liveSandbox[id])
			delete(liveRunID, id)
			delete(liveSandbox, id)

			switch r := rng.Float64(); {
			case r < 0.5:
				// A plain requeue: no observation, so it is ready again
				// next cycle unless something else blocks it now.
			case r < 0.8:
				if err := store.Observe(ctx, model.Observation{TaskID: id, CompletedAt: &clock}); err != nil {
					t.Fatal(err)
				}
			default:
				if err := store.Observe(ctx, model.Observation{
					TaskID: id, CompletedAt: &clock, ClosedAt: &clock,
				}); err != nil {
					t.Fatal(err)
				}
				closed[id] = true
			}
		}
	}
}

// The window dispatch.Busy exists for, simulated with the store calls
// the orchestrator makes for real: a run whose row is already finished
// but whose result has not yet been turned into an observation. The
// store says the task is queued again, and it is the caller -- still
// holding that result -- that knows better.
func TestCycleSkipsATaskItsCallerIsStillFinishingWith(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("t0", true), task("t1", true))

	first, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now)
	if err != nil || len(first) != 1 || first[0].TaskID != "t0" {
		t.Fatalf("first cycle: %v, %+v", err, first)
	}
	// The run is over as far as the store is concerned; what it means for
	// the task has not been recorded yet.
	if err := store.FinishRun(ctx, first[0].RunID, now.Add(time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.State(ctx, "t0"); st != model.StateQueued {
		t.Fatalf("t0 state = %q, want queued -- the premise of this test", st)
	}

	busy := func(taskID string) bool { return taskID == "t0" }
	second, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now.Add(2*time.Minute), dispatch.Busy(busy))
	if err != nil {
		t.Fatal(err)
	}
	// t1, not t0: being passed over must not cost t0's capacity, the same
	// way a task still backing off does not cost the one behind it.
	if len(second) != 1 || second[0].TaskID != "t1" {
		t.Fatalf("cycle with t0 busy = %+v, want t1 dispatched into the free capacity instead", second)
	}
	if runs, err := store.Runs(ctx, "t0"); err != nil || len(runs) != 1 {
		t.Fatalf("t0 has %d run(s) (%v), want only the one it was already finishing", len(runs), err)
	}

	// Once the caller is done with it, nothing about the task stops it
	// being dispatched again on whatever the store says then.
	if err := store.FinishRun(ctx, second[0].RunID, now.Add(3*time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	third, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(third) != 1 || third[0].TaskID != "t0" {
		t.Fatalf("cycle with nothing busy = %+v, want t0 dispatched again", third)
	}
}

// The other half of that window, and the half Busy cannot cover on its
// own: a caller that finishes with the task *during* the cycle. Cycle
// reads task_ready once, up front, and reaches a given candidate some
// time later, so the two questions it asks about that candidate --
// "queued?" and "busy?" -- are answered at different moments. A run
// ending in between makes both answers yes when they are read and
// neither true together: the ready list was taken while the result was
// still being applied (queued, because the run's row was already
// finished), and busy is asked once it has been (not busy, because the
// caller is done -- and the task is completed, which the snapshot cannot
// know). Dispatching on that pair starts a second run of a task that is
// finished, which is the whole thing this path exists to prevent.
//
// The interleaving is forced here rather than waited for: busy is the
// caller saying it has finished applying the run's result, so recording
// that result inside busy is exactly where a real orchestrator's own
// completion lands relative to this cycle's two reads. cmd/grain's
// TestReconcileDispatchesWhileAnEarlierRunIsStillLive is where it was
// found, as a flake -- a live reconcile loop hitting this window every
// few hundred ticks.
func TestCycleDoesNotDispatchATaskFinishedSinceTheReadyListWasRead(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("t0", true))

	first, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now)
	if err != nil || len(first) != 1 || first[0].TaskID != "t0" {
		t.Fatalf("first cycle: %v, %+v", err, first)
	}
	// The run's row is finished, its result not yet recorded: the state
	// this cycle's ready list is about to be read at.
	if err := store.FinishRun(ctx, first[0].RunID, now.Add(time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.State(ctx, "t0"); st != model.StateQueued {
		t.Fatalf("t0 state = %q, want queued -- the premise of this test", st)
	}

	done := now.Add(2 * time.Minute)
	busy := func(taskID string) bool {
		if taskID != "t0" {
			return false
		}
		// The moment the caller stops being busy with the run is the
		// moment its result is in the store -- both, here, in the order
		// the orchestrator does them (ProcessResult, then the goroutine
		// InFlight tracks returning).
		if err := store.Observe(ctx, model.Observation{TaskID: "t0", CompletedAt: &done}); err != nil {
			t.Fatal(err)
		}
		return false
	}
	second, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, done, dispatch.Busy(busy))
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Errorf("cycle dispatched %+v, want nothing: t0 completed while the cycle was deciding", second)
	}
	if runs, err := store.Runs(ctx, "t0"); err != nil || len(runs) != 1 {
		t.Errorf("t0 has %d run(s) (%v), want only the one that completed it", len(runs), err)
	}
}

// RunID's own doc comment makes two promises the rest of the tree spends:
// that a run id is exactly "<task>-<attempt>", and that its *last* "-"
// still splits it back into those two halves now that the separator
// carries no letter of its own. orchestrator's VM-name budget is
// measured against the first (kontur_sandboxes_test.go's
// TestVMNameBudgetCoversRealisticRunIDs counts the bytes this format
// costs), and the second holds only for as long as a task id never
// contains a "-" itself -- Store.NewTaskID hands out decimal counters,
// which is a fact about a different package. Neither was pinned in the
// package that makes the name, so a change to the format would have been
// caught, if at all, by a byte-counting test somewhere else.
func TestRunIDIsTaskAndAttemptSplitByItsLastSeparator(t *testing.T) {
	for _, tc := range []struct {
		taskID  string
		attempt int
		want    string
	}{
		{"1", 1, "1-1"},
		{"42", 7, "42-7"},
		{"999999", 99, "999999-99"},
	} {
		got := dispatch.RunID(tc.taskID, tc.attempt)
		if got != tc.want {
			t.Errorf("RunID(%q, %d) = %q, want %q", tc.taskID, tc.attempt, got, tc.want)
			continue
		}
		i := strings.LastIndex(got, "-")
		if i < 0 {
			t.Errorf("RunID(%q, %d) = %q, which has no separator to split back on", tc.taskID, tc.attempt, got)
			continue
		}
		taskID, attempt := got[:i], got[i+1:]
		if taskID != tc.taskID || attempt != strconv.Itoa(tc.attempt) {
			t.Errorf("%q split on its last %q = (%q, %q), want (%q, %d)",
				got, "-", taskID, attempt, tc.taskID, tc.attempt)
		}
	}
}

// The deployment-wide pause (grain/task-38): a paused cycle starts
// nothing, an exempted task still goes, and lifting the pause dispatches
// the rest -- with nothing spent in between. The tasks passed over here
// keep their attempt numbers and their capacity, which is what makes a
// pause cost a deployment time and nothing else.
func TestCycleStartsNothingWhilePausedExceptWhatThePauseExempts(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("t0", true), task("t1", true), task("t2", true))

	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 3}, now, dispatch.Paused())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a paused cycle dispatched %+v, want nothing", got)
	}

	// The one exemption: grain's own pause around a check of an upgrade
	// it just installed names the check, and the check has to be
	// dispatched by the very cycle that is dispatching nothing else.
	got, err = dispatch.Cycle(ctx, store, model.Limits{Workers: 3}, now.Add(time.Minute), dispatch.Paused("t1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != "t1" {
		t.Fatalf("a pause exempting t1 dispatched %+v, want t1 alone", got)
	}

	// And nothing was spent on the two that were passed over: they are
	// dispatched on their first attempt as soon as the pause is gone.
	got, err = dispatch.Cycle(ctx, store, model.Limits{Workers: 3}, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("the cycle after the pause dispatched %+v, want t0 and t2", got)
	}
	for _, d := range got {
		if d.Attempt != 1 {
			t.Errorf("%s dispatched with attempt %d, want 1: a pause spends no attempt", d.TaskID, d.Attempt)
		}
	}
}

// When worker capacity is exhausted and no merger or director candidates remain,
// Cycle must break early from scanning the backlog rather than evaluating every task.
func TestReadyLoopBreaksEarlyWhenOnlyWorkersRemainAndWorkerCapacityExhausted(t *testing.T) {
	store, ctx := open(t)
	for i := 0; i < 50; i++ {
		putTasks(t, store, ctx, task(fmt.Sprintf("w-%02d", i), true))
	}

	limits := model.Limits{Workers: 2, Mergers: 2}
	first, err := dispatch.Cycle(ctx, store, limits, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 {
		t.Fatalf("first cycle dispatched %d tasks, want 2", len(first))
	}

	// Second cycle: worker pool is full (2/2), merger capacity (2) is nominally available,
	// but there are 0 merger candidates. Must return 0 without scanning through the remaining 48 tasks.
	second, err := dispatch.Cycle(ctx, store, limits, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("second cycle dispatched %d tasks, want 0", len(second))
	}
}

// When worker capacity is full but merger capacity is available and merger candidates exist,
// Cycle scans past workers to reach the merger and then terminates early once no more eligible tasks remain.
func TestReadyLoopScansPastWorkersToReachMergerAndBreaksAfterwards(t *testing.T) {
	store, ctx := open(t)
	var tasks []model.Task
	for i := 0; i < 5; i++ {
		tk := task(fmt.Sprintf("w-before-%d", i), true)
		tk.OrderKey = 0
		tasks = append(tasks, tk)
	}
	m := fixTask("merger-0")
	m.OrderKey = 1
	tasks = append(tasks, m)
	for i := 0; i < 5; i++ {
		tk := task(fmt.Sprintf("w-after-%d", i), true)
		tk.OrderKey = 2
		tasks = append(tasks, tk)
	}
	putTasks(t, store, ctx, tasks...)

	limits := model.Limits{Workers: 0, Mergers: 1}
	got, err := dispatch.Cycle(ctx, store, limits, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != "merger-0" {
		t.Fatalf("dispatched %+v, want only merger-0", got)
	}

	live, err := store.LiveRunCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if live.Mergers != 1 || live.Workers != 0 {
		t.Fatalf("live runs = %+v, want 1 merger and 0 workers", live)
	}
}

// grain/task-1592: a team with no GitHub credential has nothing its
// sandboxes could clone with -- there is no deployment credential to fall
// back to -- so the orchestrator holds its tasks (dispatch.Hold over
// model.Store.TaskGitCredential) and they are dispatched, on their first
// attempt, by the first cycle after the team has one.
func TestCycleHoldsATeamsTasksUntilTheTeamHasAGitCredential(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("t0", true), task("t1", true))
	noCredential := dispatch.Hold(func(taskID string) string {
		_, err := store.TaskGitCredential(ctx, taskID, "acme", "widgets")
		if err != nil && !errors.Is(err, model.ErrNoTeamGitCredential) {
			t.Errorf("TaskGitCredential(%s): %v", taskID, err)
		}
		if err != nil {
			return "no team GitHub credential"
		}
		return ""
	})

	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 3}, now, noCredential)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a team with no GitHub credential had %+v dispatched, want nothing", got)
	}

	if err := store.UpdateConfig(ctx, func(tm *model.Config) error {
		tm.GitCredential = "grain-team-ZGVmYXVsdA"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, err = dispatch.Cycle(ctx, store, model.Limits{Workers: 3}, now.Add(time.Minute), noCredential)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("once the team had a credential the cycle dispatched %+v, want t0 and t1", got)
	}
	for _, d := range got {
		if d.Attempt != 1 {
			t.Errorf("%s dispatched on attempt %d, want 1 -- holding it should have cost nothing", d.TaskID, d.Attempt)
		}
	}
}

func TestCycleEnforcesDefaultMaxWorkersAlongsideSandboxBudget(t *testing.T) {
	store, ctx := open(t)
	var tasks []model.Task
	for i := 0; i < 15; i++ {
		tasks = append(tasks, task(fmt.Sprintf("t%02d", i), true))
	}
	putTasks(t, store, ctx, tasks...)

	cfg := model.DefaultConfig()
	if cfg.MaxWorkers != 10 {
		t.Fatalf("DefaultConfig().MaxWorkers = %d, want 10", cfg.MaxWorkers)
	}
	// Even on a large host whose sandbox budget could fit 20 agent sandboxes,
	// Limits.Workers (10 by default) caps ordinary worker concurrency at 10.
	limits := model.Limits{
		Workers:    cfg.MaxWorkers,
		Mergers:    cfg.MaxMergers,
		Budget:     model.Budget{Host: model.Commitment{CPUs: 96, MemoryMB: 256 * 1024}, CPUOvercommit: 1, MemoryOvercommit: 1},
		AgentShape: model.Commitment{CPUs: 2, MemoryMB: 4096},
		Ledger:     &model.Ledger{},
	}
	got, err := dispatch.Cycle(ctx, store, limits, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 10 {
		t.Fatalf("dispatched %d workers, want %d (DefaultMaxWorkers)", len(got), model.DefaultMaxWorkers)
	}
}

func TestCycleRecordsAndClearsQuotaWaitForReadyAndRunNowTasks(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("t1", true), task("t2", true), task("t3", false))
	requestRunNow(t, ctx, store, "t3", now)

	// 1. Worker capacity limit holds back t2 (ready) and t3 (RunNow after t1 starts, or if t1 already running).
	// Start t1 first so worker capacity (1) is full when t2 and t3 are evaluated.
	if err := store.StartRun(ctx, model.Run{
		ID: "t1-1", TaskID: "t1", Attempt: 1, StartedAt: now,
	}, model.Limits{Workers: 1}); err != nil {
		t.Fatal(err)
	}

	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("dispatched %+v at capacity, want none", got)
	}

	for _, id := range []string{"t2", "t3"} {
		wait, err := store.GrantWaitOf(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		want := "1 worker runs are live of the 1 this cycle allows"
		if wait == nil || wait.Capability != model.QuotaWaitCapability || wait.Resource != want {
			t.Fatalf("GrantWaitOf(%s) = %+v, want capability %q and resource %q", id, wait, model.QuotaWaitCapability, want)
		}
	}

	// Repeated tick with unchanged reason does not write-churn the store.
	commitsBefore := store.CommitCount()
	if _, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if commitsAfter := store.CommitCount(); commitsAfter != commitsBefore {
		t.Fatalf("store commits changed from %d to %d when quota wait reason was unchanged", commitsBefore, commitsAfter)
	}

	// 2. Sandbox budget limit holds back t2 and t3 with the budget's reason.
	ledger := &model.Ledger{}
	releaseOther := ledger.Charge(model.PoolAgent, "other-run", model.Commitment{CPUs: 8, MemoryMB: 8192})
	sandboxLimits := model.Limits{
		Workers:    5,
		Budget:     model.Budget{Host: model.Commitment{CPUs: 8, MemoryMB: 8192}, CPUOvercommit: 1, MemoryOvercommit: 1},
		AgentShape: model.Commitment{CPUs: 2, MemoryMB: 2048},
		Ledger:     ledger,
	}
	if _, err := dispatch.Cycle(ctx, store, sandboxLimits, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	wantSandboxReason := sandboxLimits.SandboxVerdict().Reason
	for _, id := range []string{"t2", "t3"} {
		wait, err := store.GrantWaitOf(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if wait == nil || wait.Capability != model.QuotaWaitCapability || wait.Resource != wantSandboxReason {
			t.Fatalf("GrantWaitOf(%s) under sandbox budget = %+v, want %q", id, wait, wantSandboxReason)
		}
	}

	// 3. When capacity frees up, starting t3 (RunNow) and t2 (ready) clears their GrantWait.
	releaseOther()
	got, err = dispatch.Cycle(ctx, store, sandboxLimits, now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("dispatched %+v after capacity freed, want t3 and t2", got)
	}
	for _, id := range []string{"t2", "t3"} {
		if wait, err := store.GrantWaitOf(ctx, id); err != nil || wait != nil {
			t.Fatalf("GrantWaitOf(%s) after start = %+v (%v), want nil", id, wait, err)
		}
	}
}

func TestCyclePreservesNonQuotaGrantWaitAndClearsStaleQuotaWait(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("t1", true), task("t2", true), task("t3", true))
	requestRunNow(t, ctx, store, "t1", now)

	// 1. t1 (RunNow) is declined by Admit, which sets a ModelWaitCapability GrantWait.
	// t2 starts and fills the single worker slot (1/1).
	// When the ready loop then encounters t1 at full worker capacity, setQuotaWait must NOT
	// overwrite t1's ModelWaitCapability wait; t3 has no wait so it gets QuotaWaitCapability.
	admit := dispatch.Admit(func(ctx context.Context, c dispatch.Candidate) (dispatch.Admission, error) {
		if c.TaskID == "t1" {
			if err := store.SetGrantWait(ctx, model.GrantWait{
				TaskID: c.TaskID, Capability: model.ModelWaitCapability, Resource: "no connection serves model-x", WaitingAt: now,
			}); err != nil {
				return dispatch.Admission{}, err
			}
			return dispatch.Admission{}, nil
		}
		return dispatch.Admission{Admitted: true}, nil
	})
	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now, admit)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != "t2" {
		t.Fatalf("dispatched %+v, want only t2", got)
	}
	if wait, err := store.GrantWaitOf(ctx, "t1"); err != nil || wait == nil || wait.Capability != model.ModelWaitCapability {
		t.Fatalf("GrantWaitOf(t1) = %+v (%v), want ModelWaitCapability preserved", wait, err)
	}
	if wait, err := store.GrantWaitOf(ctx, "t3"); err != nil || wait == nil || wait.Capability != model.QuotaWaitCapability {
		t.Fatalf("GrantWaitOf(t3) = %+v (%v), want QuotaWaitCapability", wait, err)
	}

	// 2. Finish t2 as failed so t2 enters retry backoff. While a usage-limit pause (PausedFor) is active,
	// t2 in retry backoff must NOT get a QuotaWaitCapability recorded on it.
	if err := store.FinishRun(ctx, got[0].RunID, now.Add(time.Second), "failed", "boom"); err != nil {
		t.Fatal(err)
	}
	pausedForAll := dispatch.PausedFor(func(string) bool { return true })
	if _, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now.Add(2*time.Second), pausedForAll); err != nil {
		t.Fatal(err)
	}
	if wait, err := store.GrantWaitOf(ctx, "t2"); err != nil || wait != nil {
		t.Fatalf("GrantWaitOf(t2) during retry backoff under PausedFor = %+v (%v), want nil", wait, err)
	}

	// 3. If t2 somehow had a stale QuotaWaitCapability while still in retry backoff, the next cycle clears it.
	if err := store.SetGrantWait(ctx, model.GrantWait{
		TaskID: "t2", Capability: model.QuotaWaitCapability, Resource: "stale pause", WaitingAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if wait, err := store.GrantWaitOf(ctx, "t2"); err != nil || wait != nil {
		t.Fatalf("GrantWaitOf(t2) after retry backoff skip = %+v (%v), want stale QuotaWait cleared", wait, err)
	}

	// 4. Stale QuotaWaitCapability on t3 is cleared when skipped by dispatch.Paused or dispatch.Busy,
	// or when declined by Admit without setting a replacement GrantWait.
	if err := store.SetGrantWait(ctx, model.GrantWait{
		TaskID: "t3", Capability: model.QuotaWaitCapability, Resource: "stale quota", WaitingAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now.Add(4*time.Second), dispatch.Paused()); err != nil {
		t.Fatal(err)
	}
	if wait, err := store.GrantWaitOf(ctx, "t3"); err != nil || wait != nil {
		t.Fatalf("GrantWaitOf(t3) under dispatch.Paused = %+v (%v), want stale QuotaWait cleared", wait, err)
	}

	if err := store.SetGrantWait(ctx, model.GrantWait{
		TaskID: "t3", Capability: model.QuotaWaitCapability, Resource: "stale quota", WaitingAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	declineSilent := dispatch.Admit(func(context.Context, dispatch.Candidate) (dispatch.Admission, error) {
		return dispatch.Admission{Admitted: false}, nil
	})
	if _, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 2}, now.Add(5*time.Second), declineSilent); err != nil {
		t.Fatal(err)
	}
	if wait, err := store.GrantWaitOf(ctx, "t3"); err != nil || wait != nil {
		t.Fatalf("GrantWaitOf(t3) after silent Admit decline = %+v (%v), want stale QuotaWait cleared", wait, err)
	}
}

func TestDirectorDispatchAdmitsAtMostOneSandboxOverBudgetAndHoldsSecond(t *testing.T) {
	store, ctx := open(t)
	d1 := directorTask("dir-1")
	d2 := directorTask("dir-2")
	d2.Tags = []string{model.ConsoleSessionTag("dir-2")}
	putTasks(t, store, ctx, d1, d2)

	ledger := &model.Ledger{}
	shape := model.Commitment{CPUs: 4, MemoryMB: 8 * 1024}
	// Budget is 4 vCPU / 8 GiB, and one worker run already fills it to cap.
	budget := model.Budget{
		Host:             model.Commitment{CPUs: 4, MemoryMB: 8 * 1024},
		CPUOvercommit:    1,
		MemoryOvercommit: 1,
	}
	releaseWorker := ledger.Charge(model.PoolAgent, "worker-1", shape)
	defer releaseWorker()

	limits := model.Limits{
		Workers:    2,
		Budget:     budget,
		AgentShape: shape,
		Ledger:     ledger,
	}

	// Cycle dispatches at most one director as the single budget exception,
	// and holds the second director with a quota wait until committed is back
	// under the cap.
	got, err := dispatch.Cycle(ctx, store, limits, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != "dir-1" {
		t.Fatalf("dispatched %+v, want only dir-1 admitted over full budget", got)
	}
	if wait, err := store.GrantWaitOf(ctx, "dir-2"); err != nil || wait == nil || wait.Capability != model.QuotaWaitCapability {
		t.Fatalf("GrantWaitOf(dir-2) = %+v (%v), want QuotaWaitCapability while over cap", wait, err)
	}

	// Even an explicit RunNow for dir-2 must wait while committed exceeds the cap.
	requestRunNow(t, ctx, store, "dir-2", now.Add(time.Second))
	gotNow, err := dispatch.Cycle(ctx, store, limits, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(gotNow) != 0 {
		t.Fatalf("RunNow for dir-2 while over cap dispatched %+v, want none", gotNow)
	}

	// Once worker-1 releases its sandbox, live commitment drops back to the
	// 4 vCPU / 8 GiB cap, so dir-2 is admitted.
	releaseWorker()

	got2, err := dispatch.Cycle(ctx, store, limits, now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(got2) != 1 || got2[0].TaskID != "dir-2" {
		t.Fatalf("dispatched %+v after cap cleared, want dir-2", got2)
	}
}

func TestDispatchChargesPerTaskSandboxShapeOverride(t *testing.T) {
	store, ctx := open(t)

	// Task t-big asks for 8 vCPU / 16 GiB, overriding the 2 vCPU / 4 GiB default.
	big := task("t-big", true)
	big.SandboxCPUs = 8
	big.SandboxMemoryMB = 16 * 1024
	small := task("t-small", true)
	putTasks(t, store, ctx, big, small)

	ledger := &model.Ledger{}
	limits := model.Limits{
		Workers:    4,
		Budget:     model.Budget{Host: model.Commitment{CPUs: 8, MemoryMB: 16 * 1024}, CPUOvercommit: 1, MemoryOvercommit: 1},
		AgentShape: model.Commitment{CPUs: 2, MemoryMB: 4 * 1024},
		Ledger:     ledger,
	}

	got, err := dispatch.Cycle(ctx, store, limits, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != "t-big" {
		t.Fatalf("dispatched %+v, want only t-big (which fills the 8 vCPU / 16 GiB budget)", got)
	}
	if live := ledger.Live(); live != (model.Commitment{CPUs: 8, MemoryMB: 16 * 1024}) {
		t.Fatalf("ledger.Live() = %s, want 8 vCPU / 16 GiB from t-big's override", live)
	}
}

func TestDispatchPrefersHigherPriorityAndPreservesBandOrder(t *testing.T) {
	store, ctx := open(t)
	repo := model.RepoRef{Owner: "owner", Name: "payments-api"}

	// File tasks in mixed order across priorities 2, 5 (default), and 8.
	low := task("prio-2", true)
	low.Priority = 2
	low.Target = &repo
	low.OrderKey = 10
	t0 := now
	low.CreatedAt = &t0

	midFIFO1 := task("prio-5-fifo-1", true)
	midFIFO1.Target = &repo // Priority 0 -> defaults to 5
	midFIFO1.OrderKey = 20
	t1 := now.Add(time.Minute)
	midFIFO1.CreatedAt = &t1

	midFIFO2 := task("prio-5-fifo-2", true)
	midFIFO2.Priority = 5
	midFIFO2.Target = &repo
	midFIFO2.OrderKey = 20 // tie on OrderKey -> broken by CreatedAt FIFO
	t2 := now.Add(2 * time.Minute)
	midFIFO2.CreatedAt = &t2

	midAtFront := task("prio-5-at-front", true)
	midAtFront.Priority = 5
	midAtFront.Target = &repo
	midAtFront.OrderKey = 30
	t3 := now.Add(3 * time.Minute)
	midAtFront.CreatedAt = &t3

	midFixesBase := task("prio-5-fixes-base", true)
	midFixesBase.Priority = 5
	midFixesBase.Target = &repo
	midFixesBase.OrderKey = 40
	t4 := now.Add(4 * time.Minute)
	midFixesBase.CreatedAt = &t4
	model.SetFixesBaseBranch(&midFixesBase, true)

	high := task("prio-8", true)
	high.Priority = 8
	high.Target = &repo
	high.OrderKey = 50
	t5 := now.Add(5 * time.Minute)
	high.CreatedAt = &t5

	putTasks(t, store, ctx, low, midFIFO1, midFIFO2, midAtFront, midFixesBase, high)

	// Move prio-5-at-front to the front of the backlog; it moves to the
	// front of its priority=5 band (behind prio-5-fixes-base) without
	// jumping ahead of prio-8.
	if err := store.MoveToFrontOfBacklog(ctx, []string{"prio-5-at-front"}); err != nil {
		t.Fatalf("MoveToFrontOfBacklog: %v", err)
	}

	limits := model.Limits{Workers: 1}
	wantOrder := []string{
		"prio-8",
		"prio-5-fixes-base",
		"prio-5-at-front",
		"prio-5-fifo-1",
		"prio-5-fifo-2",
		"prio-2",
	}

	for i, wantID := range wantOrder {
		stepTime := now.Add(time.Duration(i+1) * time.Hour)
		got, err := dispatch.Cycle(ctx, store, limits, stepTime)
		if err != nil {
			t.Fatalf("step %d Cycle: %v", i, err)
		}
		if len(got) != 1 || got[0].TaskID != wantID {
			t.Fatalf("step %d dispatched %+v, want [%s]", i, got, wantID)
		}
		if err := store.FinishRun(ctx, got[0].RunID, stepTime.Add(time.Minute), "succeeded", "done"); err != nil {
			t.Fatalf("step %d FinishRun: %v", i, err)
		}
		doneAt := stepTime.Add(time.Minute)
		if err := store.Observe(ctx, model.Observation{TaskID: wantID, CompletedAt: &doneAt, ClosedAt: &doneAt}); err != nil {
			t.Fatalf("step %d Observe: %v", i, err)
		}
	}
}
