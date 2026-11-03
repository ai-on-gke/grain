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

package ci

// RunStore with a database behind it: the same five methods, and the one
// property the map could not have -- a run that is still there after the
// process that ran it is not.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

// storedTestStore is a real embedded database in a directory of the
// test's own, and the RunStore over it. The directory is returned so a
// test can open a *second* store over the same file, which is what a
// restart is.
func storedTestStore(t *testing.T, dir string, opts StoredRunStoreOptions) *StoredRunStore {
	t.Helper()
	runs, _ := storedTestStoreDB(t, dir, opts)
	return runs
}

// storedTestStoreDB is storedTestStore with the database itself handed
// back, for the one test that has to look at the rows rather than at the
// run they add up to.
func storedTestStoreDB(t *testing.T, dir string, opts StoredRunStoreOptions) (*StoredRunStore, *sql.DB) {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		t.Fatalf("opening sqlite in %s: %v", dir, err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	if opts.Log == nil {
		opts.Log = func(format string, args ...any) { t.Logf("ci: "+format, args...) }
	}
	return NewStoredRunStore(store, opts), db
}

func storedTestRun(id string, started time.Time) Run {
	return Run{
		ID: id, Key: Key("key-" + id), Repo: "owner/payments-api", Ref: "grain/task-1",
		Commit: "9f8e7d6c", Event: EventPush, RequestedBy: "a1b2", StartedAt: &started,
		Jobs: []JobRun{{
			Workflow: "tests", WorkflowPath: ".grain/workflows/tests.yml", JobID: "unit",
			Name: "unit tests", Key: "job-" + Key(id), StartedAt: &started,
			Steps: []StepRun{{Name: "go test", Output: "the whole log", ExitCode: 0}},
		}},
	}
}

// The point of the exercise: a run written by one process, read back by
// the next one, entire.
func TestAStoredRunOutlivesTheProcessThatRanIt(t *testing.T) {
	dir := t.TempDir()
	run := storedTestRun("3f2a1c", time.Now().UTC().Truncate(time.Millisecond))
	run.Jobs[0].Conclusion = ConclusionSuccess
	run.Conclusion = ConclusionSuccess
	run.FinishedAt = timePtr(run.StartedAt.Add(time.Minute))
	storedTestStore(t, dir, StoredRunStoreOptions{}).Put(run)

	// A whole new store over the same database, which is what the daemon
	// after a restart has.
	restarted := storedTestStore(t, dir, StoredRunStoreOptions{})
	got, ok := restarted.Get("3f2a1c")
	if !ok {
		t.Fatal("the run was gone after a restart, which is the whole thing this store is for")
	}
	if got.Repo != run.Repo || got.Conclusion != ConclusionSuccess || got.Key != run.Key {
		t.Errorf("the run came back as %+v", got)
	}
	if len(got.Jobs) != 1 || len(got.Jobs[0].Steps) != 1 || got.Jobs[0].Steps[0].Output != "the whole log" {
		t.Errorf("its jobs and their output came back as %+v", got.Jobs)
	}
	if listed := restarted.List(); len(listed) != 1 || listed[0].ID != "3f2a1c" {
		t.Errorf("listing after a restart: %+v", listed)
	}
	// A listing carries no output, here as in memory.
	if listed := restarted.List(); listed[0].Jobs[0].Steps[0].Output != "" {
		t.Errorf("a listing carried a step's output: %q", listed[0].Jobs[0].Steps[0].Output)
	}
}

// A recheck is still a recheck when it is read back. It used to come back
// as an ordinary run, so a full-suite wait_for_checks handed the live
// recheck of a task's failed jobs (ui's liveRecheck) never said that what
// it listed was only those jobs (grain/task-2388).
func TestAStoredRecheckIsReadBackAsARecheck(t *testing.T) {
	dir := t.TempDir()
	recheck := storedTestRun("7c1e", time.Now().UTC().Truncate(time.Millisecond))
	recheck.Recheck = true
	storedTestStore(t, dir, StoredRunStoreOptions{}).Put(recheck)
	storedTestStore(t, dir, StoredRunStoreOptions{}).Put(storedTestRun("8d2f", time.Now().UTC()))

	restarted := storedTestStore(t, dir, StoredRunStoreOptions{})
	if got, ok := restarted.Get("7c1e"); !ok || !got.Recheck {
		t.Errorf("the recheck came back as %+v (found %v), want Recheck set", got, ok)
	}
	if got, _ := restarted.Get("8d2f"); got.Recheck {
		t.Error("an ordinary run came back as a recheck")
	}
	page := restarted.Find(RunFilter{Ref: "grain/task-1"}, 10)
	for _, run := range page.Runs {
		if run.Recheck != (run.ID == "7c1e") {
			t.Errorf("listed run %s has Recheck %v", run.ID, run.Recheck)
		}
	}
}

func TestAStoredRunIsUpdatedAsItsJobsFinish(t *testing.T) {
	runs := storedTestStore(t, t.TempDir(), StoredRunStoreOptions{})
	runs.Put(storedTestRun("3f2a1c", time.Now().UTC()))
	runs.Update("3f2a1c", func(r *Run) {
		r.Jobs[0].Conclusion = ConclusionFailure
		r.Jobs[0].Error = "the sandbox stopped answering"
	})
	got, ok := runs.Get("3f2a1c")
	if !ok || len(got.Jobs) != 1 || got.Jobs[0].Conclusion != ConclusionFailure ||
		got.Jobs[0].Error != "the sandbox stopped answering" {
		t.Fatalf("the update did not land: %+v (%v)", got, ok)
	}
	// And an update to a run nobody has is a no-op rather than a write
	// that invents one.
	runs.Update("nothing", func(r *Run) { r.Conclusion = ConclusionSuccess })
	if _, ok := runs.Get("nothing"); ok {
		t.Error("updating a run that is not there created one")
	}
}

func TestAStoredRunIsFoundLiveByItsKeyUntilItConcludes(t *testing.T) {
	runs := storedTestStore(t, t.TempDir(), StoredRunStoreOptions{})
	run := storedTestRun("3f2a1c", time.Now().UTC())
	runs.Put(run)
	if live, ok := runs.Live(run.Key); !ok || live.ID != "3f2a1c" {
		t.Fatalf("a run with no conclusion is live: %+v (%v)", live, ok)
	}
	run.Conclusion = ConclusionSuccess
	runs.Put(run)
	if live, ok := runs.Live(run.Key); ok {
		t.Errorf("a finished run is not live: %+v", live)
	}
}

// What a restart makes of a run that was in flight during the last one:
// readable, concluded, and marked as nobody's verdict.
func TestRecoveringInterruptedRunsMarksThemRatherThanLosingThem(t *testing.T) {
	dir := t.TempDir()
	run := storedTestRun("3f2a1c", time.Now().UTC())
	storedTestStore(t, dir, StoredRunStoreOptions{}).Put(run)

	restarted := storedTestStore(t, dir, StoredRunStoreOptions{})
	stamped, err := restarted.RecoverUnattachable(context.Background(), time.Now().UTC(), nil)
	if err != nil {
		t.Fatalf("recovering: %v", err)
	}
	if stamped != 1 {
		t.Errorf("recovered %d run(s), want the one that was in flight", stamped)
	}
	got, ok := restarted.Get("3f2a1c")
	if !ok {
		t.Fatal("the interrupted run should still be readable -- that is why it is stamped and not deleted")
	}
	if got.Conclusion != ConclusionCancelled || !got.Interrupted {
		t.Errorf("an interrupted run reads %s (interrupted %v)", got.Conclusion, got.Interrupted)
	}
	if !strings.Contains(got.Error, "interrupted") {
		t.Errorf("its error says %q, which does not tell a reader what happened", got.Error)
	}
	if len(got.Jobs) != 1 {
		t.Errorf("recovery lost what the run had already done: %+v", got.Jobs)
	}
	// The reason for all of it: a new request must not be deduplicated
	// against a run nothing is running.
	if live, ok := restarted.Live(run.Key); ok {
		t.Errorf("an interrupted run still looks live: %+v", live)
	}
}

// keeping is one retention policy, held still. Most tests want the
// numbers rather than the live re-reading; the one that wants the live
// re-reading passes a closure over a variable it changes underneath the
// store, which is the whole of what a Settings change looks like from
// here.
func keeping(r StoredRunRetention) func() StoredRunRetention {
	return func() StoredRunRetention { return r }
}

func TestStoredRunsArePrunedPastTheHistoryTheDeploymentKeeps(t *testing.T) {
	runs := storedTestStore(t, t.TempDir(), StoredRunStoreOptions{Retention: keeping(StoredRunRetention{History: 2})})
	start := time.Now().UTC().Add(-time.Hour)
	for i, id := range []string{"oldest", "middle", "newest"} {
		run := storedTestRun(id, start.Add(time.Duration(i)*time.Minute))
		run.Conclusion = ConclusionSuccess
		runs.Put(run)
	}
	if _, ok := runs.Get("oldest"); ok {
		t.Error("the oldest finished run should have been pruned")
	}
	if _, ok := runs.Get("newest"); !ok {
		t.Error("the newest run should be kept")
	}
	// A run still going is not counted and not pruned, whatever the
	// limit says -- otherwise a caller ends up polling an id that has
	// vanished.
	live := storedTestRun("live", start.Add(-time.Hour))
	runs.Put(live)
	runs.Put(storedTestRun("later", time.Now().UTC()))
	if _, ok := runs.Get("live"); !ok {
		t.Error("a live run was pruned")
	}
}

// The property the two bounds exist for: a run answers "what was the
// verdict" long after it has stopped answering "what did it print".
func TestOldStoredRunsKeepTheirRecordButLoseOutput(t *testing.T) {
	// A ceiling with room for one run's output and no more: what a
	// deployment configures in MiB, written here in the bytes a test can
	// count.
	runs := storedTestStore(t, t.TempDir(), StoredRunStoreOptions{
		Retention: keeping(StoredRunRetention{History: 3, OutputBytes: int64(len("diagnostic output") + len("still running"))}),
	})
	start := time.Now().UTC().Add(-time.Hour)
	live := storedTestRun("live", start.Add(-time.Hour))
	live.Jobs[0].Steps[0].Output = "still running"
	runs.Put(live)
	for i, id := range []string{"oldest", "middle", "newest"} {
		run := storedTestRun(id, start.Add(time.Duration(i)*time.Minute))
		run.Jobs[0].Steps[0].Output = "diagnostic output"
		run.Conclusion = ConclusionSuccess
		runs.Put(run)
	}

	for _, id := range []string{"oldest", "middle"} {
		got, ok := runs.Get(id)
		if !ok {
			t.Fatalf("old run %q lost its record", id)
		}
		if got.Jobs[0].Steps[0].Output != "" {
			t.Errorf("old run %q still has output", id)
		}
	}
	got, ok := runs.Get("newest")
	if !ok || got.Jobs[0].Steps[0].Output == "" {
		t.Errorf("newest run should retain output: %+v", got)
	}
	got, ok = runs.Get("live")
	if !ok || got.Jobs[0].Steps[0].Output == "" {
		t.Errorf("a live run is never counted or cleared: %+v", got)
	}
}

// The bound that is not the in-memory one: what a step printed is cut
// again on its way into a database, because that copy is the one that
// ends up in the state repository forever.
func TestStoredStepOutputIsCutToWhatIsWorthKeepingForever(t *testing.T) {
	runs := storedTestStore(t, t.TempDir(), StoredRunStoreOptions{})
	run := storedTestRun("3f2a1c", time.Now().UTC())
	head := strings.Repeat("h", MaxStoredStepOutput)
	tail := strings.Repeat("t", MaxStoredStepOutput)
	run.Jobs[0].Steps[0].Output = head + tail
	runs.Put(run)

	got, ok := runs.Get("3f2a1c")
	if !ok {
		t.Fatal("the run is not there")
	}
	stored := got.Jobs[0].Steps[0].Output
	if len(stored) > MaxStoredStepOutput+512 {
		t.Errorf("a stored step kept %d bytes of output, want no more than %d", len(stored), MaxStoredStepOutput)
	}
	// Both ends survive and the middle says who dropped what: the head
	// is what the step was doing and the tail is where the failure is.
	if !strings.HasPrefix(stored, "hhh") || !strings.HasSuffix(stored, "ttt") {
		t.Error("the truncation kept the wrong end")
	}
	if !strings.Contains(stored, "elided from the middle by grain") {
		t.Errorf("nothing in the stored output says it was cut: %q", stored[:200])
	}
}

// The bound the per-step one does not imply: a run may have as many
// steps as its workflows declare, so "64KB each" is no bound at all on
// what a run costs the state repository it is exported into. A
// repository declaring a dozen chatty jobs stored 4.5MB a run before
// this, which at the history a deployment keeps was a 2.3GiB file git
// would then keep every hourly version of (staterepo.TestCIRunStorageCost).
func TestAStoredRunCannotSpendMoreThanARunIsWorth(t *testing.T) {
	runs := storedTestStore(t, t.TempDir(), StoredRunStoreOptions{})
	run := storedTestRun("7c1d90", time.Now().UTC())
	run.Jobs = nil
	for j := 0; j < 12; j++ {
		job := JobRun{Name: fmt.Sprintf("job %d", j), Conclusion: ConclusionSuccess}
		for s := 0; s < 6; s++ {
			job.Steps = append(job.Steps, StepRun{
				Name:       fmt.Sprintf("step %d", s),
				Conclusion: ConclusionSuccess,
				Output:     strings.Repeat("chatter\n", 1<<17),
			})
		}
		run.Jobs = append(run.Jobs, job)
	}
	// The one step that failed, and the one output a reader of this run
	// is actually after.
	run.Jobs[3].Conclusion = ConclusionFailure
	run.Jobs[3].Steps[2].Conclusion = ConclusionFailure
	run.Jobs[3].Steps[2].ExitCode = 1
	run.Jobs[3].Steps[2].Output = strings.Repeat("f", 8<<10) + "the assertion that failed\n"
	runs.Put(run)

	got, ok := runs.Get("7c1d90")
	if !ok {
		t.Fatal("the run is not there")
	}
	total := 0
	for _, job := range got.Jobs {
		for _, step := range job.Steps {
			total += len(step.Output)
		}
	}
	if total > MaxStoredRunOutput {
		t.Errorf("a stored run kept %d bytes of output across its steps, want no more than %d",
			total, MaxStoredRunOutput)
	}
	// The failing step is paid first, whole: everything else in the run
	// is context for it, and a bound that dropped it would have saved
	// the bytes by throwing away the reason the row exists.
	failed := got.Jobs[3].Steps[2].Output
	if !strings.HasSuffix(failed, "the assertion that failed\n") {
		t.Errorf("the failing step lost its output to the bound: %d bytes, ending %q",
			len(failed), lastOf(failed, 40))
	}
	// And a step that lost most of its output says who took it, rather
	// than looking like a step that printed a few lines and stopped.
	quiet := got.Jobs[0].Steps[0].Output
	if !strings.Contains(quiet, "by grain, not by the step") {
		t.Errorf("a step whose output was cut for the run's budget does not say so: %q", lastOf(quiet, 80))
	}
}

func lastOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// A Service handed one behaves like a Service handed the other: the
// swap RunStore exists to be the seam for.
func TestAServiceRunsAgainstAStoredRunStore(t *testing.T) {
	svc := newTestService(t, &fakeSandboxes{}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  one:\n    steps:\n      - run: true\n",
	})
	svc.Runs = storedTestStore(t, t.TempDir(), StoredRunStoreOptions{})

	started, err := svc.Start(context.Background(), Request{Repo: "o/r", Ref: "main", Event: EventPush})
	if err != nil {
		t.Fatalf("starting: %v", err)
	}
	finished := waitForRun(t, svc, started.ID)
	if finished.Conclusion != ConclusionSuccess {
		t.Fatalf("the run ended %s (%s)", finished.Conclusion, finished.Error)
	}
	if len(finished.Jobs) != 1 || finished.Jobs[0].Conclusion != ConclusionSuccess {
		t.Errorf("its jobs came back as %+v", finished.Jobs)
	}
	if runs := svc.List(); len(runs) != 1 || runs[0].ID != started.ID {
		t.Errorf("the service lists what it stored: %+v", runs)
	}
}

// A run in flight publishes one step's output over and over, and that is
// the one update this store does not rewrite the whole run for.
//
// How the test can tell: a row planted under a job index the run does not
// have. The reader skips it (a step whose job is not there is not a step)
// and a one-column UPDATE cannot reach it, but the whole-run write
// deletes every job and step under the run on its way past -- so the
// sentinel surviving means the narrow write happened and nothing else
// did.
func TestALiveStepsOutputIsWrittenWithoutRewritingTheWholeRun(t *testing.T) {
	ctx := context.Background()
	runs, db := storedTestStoreDB(t, t.TempDir(), StoredRunStoreOptions{})
	run := storedTestRun("3f2a1c", time.Now().UTC())
	run.Jobs[0].Steps[0].Output = "=== RUN TestOne\n"
	runs.Put(run)
	plantOrphanStep(ctx, t, db, "3f2a1c")

	// The publication itself: the engine's snapshot of a job whose last
	// step has printed another line.
	runs.Update("3f2a1c", func(r *Run) {
		r.Jobs[0].Steps[0].Output = "=== RUN TestOne\n=== RUN TestTwo\n"
	})
	if !orphanStepIsStored(ctx, t, db, "3f2a1c") {
		t.Error("a live publication rewrote the whole run, which is the cost this write exists to avoid")
	}
	got, ok := runs.Get("3f2a1c")
	if !ok || got.Jobs[0].Steps[0].Output != "=== RUN TestOne\n=== RUN TestTwo\n" {
		t.Fatalf("the published output did not land: %+v (%v)", got.Jobs, ok)
	}
	if got.Jobs[0].Steps[0].Name != "go test" || got.Jobs[0].JobID != "unit" || got.Repo != run.Repo {
		t.Errorf("the narrow write changed something else: %+v", got)
	}

	// And the punctuation of a run is written as it always was.
	runs.Update("3f2a1c", func(r *Run) {
		r.Jobs[0].Steps[0].Conclusion = ConclusionSuccess
		r.Jobs[0].Conclusion = ConclusionSuccess
	})
	if orphanStepIsStored(ctx, t, db, "3f2a1c") {
		t.Error("a job finishing was written narrowly, which would leave the run's rows disagreeing")
	}
	if got, _ := runs.Get("3f2a1c"); got.Jobs[0].Conclusion != ConclusionSuccess {
		t.Errorf("the job's conclusion did not land: %+v", got.Jobs)
	}
}

// A publication that moves what the *other* steps keep is written whole.
//
// A run past MaxStoredRunOutput divides the budget between its steps
// smallest first (boundRunOutput), so a step that was small and is now
// large takes its new share out of theirs: several columns move, and one
// UPDATE is no longer the same write. The narrow write is an
// optimisation, not a second policy -- what lands is what the whole-run
// write would have left behind, or it is not written narrowly.
func TestAPublicationThatMovesWhatOtherStepsKeepIsWrittenWhole(t *testing.T) {
	ctx := context.Background()
	runs, db := storedTestStoreDB(t, t.TempDir(), StoredRunStoreOptions{})
	run := storedTestRun("3f2a1c", time.Now().UTC())
	run.Jobs[0].Steps = []StepRun{{Name: "quiet", Output: "one line\n"}}
	for i := 0; i < 5; i++ {
		run.Jobs[0].Steps = append(run.Jobs[0].Steps, StepRun{
			Name:   fmt.Sprintf("chatty %d", i),
			Output: strings.Repeat("x", 2*MaxStoredStepOutput),
		})
	}
	runs.Put(run)
	plantOrphanStep(ctx, t, db, "3f2a1c")

	// The quiet step stops being quiet, and the share every other step
	// was living on shrinks.
	runs.Update("3f2a1c", func(r *Run) {
		r.Jobs[0].Steps[0].Output = strings.Repeat("y", 2*MaxStoredStepOutput)
	})
	if orphanStepIsStored(ctx, t, db, "3f2a1c") {
		t.Error("a publication that moves every step's share was written as one column")
	}
	got, ok := runs.Get("3f2a1c")
	if !ok {
		t.Fatal("the run is gone")
	}
	stored := 0
	for _, step := range got.Jobs[0].Steps {
		stored += len(step.Output)
	}
	if stored > MaxStoredRunOutput {
		t.Errorf("the run kept %d bytes of output, past the %d it is allowed", stored, MaxStoredRunOutput)
	}
}

// orphanStepIndex is a job index storedTestRun's run does not have, so a
// ci_step row under it belongs to no job: invisible to a reader, and
// deleted by the whole-run write along with the rows that are real.
const orphanStepIndex = 9

func plantOrphanStep(ctx context.Context, t *testing.T, db *sql.DB, runID string) {
	t.Helper()
	if _, err := db.ExecContext(ctx,
		"INSERT INTO `ci_step` (`run_id`,`job_index`,`step_index`,`name`) VALUES (?,?,0,'sentinel')",
		runID, orphanStepIndex); err != nil {
		t.Fatalf("planting the sentinel row: %v", err)
	}
}

func orphanStepIsStored(ctx context.Context, t *testing.T, db *sql.DB, runID string) bool {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `ci_step` WHERE `run_id` = ? AND `job_index` = ?",
		runID, orphanStepIndex).Scan(&n); err != nil {
		t.Fatalf("looking for the sentinel row: %v", err)
	}
	return n > 0
}

// --- what the history is bounded by (grain/task-269) ---------------------

// storedTestRunPrinting is a concluded run whose one step printed
// exactly this many bytes: the unit the byte ceiling below is counted
// in.
func storedTestRunPrinting(id string, started time.Time, bytes int) Run {
	run := storedTestRun(id, started)
	run.Jobs[0].Steps[0].Output = strings.Repeat("x", bytes)
	run.Conclusion = ConclusionSuccess
	return run
}

// The sweep is about disk, not about a count of runs: one run's output
// is anything between nothing and the per-run cap, so what has to hold
// is "the oldest go until what is left fits", whatever number of runs
// that turns out to be.
func TestTheOutputSweepDropsTheOldestUntilTheByteCeilingIsMet(t *testing.T) {
	const printed = 4 << 10
	runs := storedTestStore(t, t.TempDir(), StoredRunStoreOptions{
		// Room for two runs of this size and not a byte more.
		Retention: keeping(StoredRunRetention{OutputBytes: 2 * printed}),
	})
	start := time.Now().UTC().Add(-time.Hour)
	// The oldest prints a great deal more than the two after it, which is
	// the case a count of runs gets wrong: by run count it is one of
	// three and well inside a history of three, and by bytes it is the
	// whole ceiling on its own.
	runs.Put(storedTestRunPrinting("oldest", start, 8*printed))
	runs.Put(storedTestRunPrinting("middle", start.Add(time.Minute), printed))
	runs.Put(storedTestRunPrinting("newest", start.Add(2*time.Minute), printed))

	for _, id := range []string{"middle", "newest"} {
		got, ok := runs.Get(id)
		if !ok {
			t.Fatalf("run %q lost its record", id)
		}
		if len(got.Jobs[0].Steps[0].Output) != printed {
			t.Errorf("run %q kept %d bytes of output, want the %d it printed -- it is inside the ceiling",
				id, len(got.Jobs[0].Steps[0].Output), printed)
		}
	}
	// And the one that did not fit: swept of its output, still a run.
	got, ok := runs.Get("oldest")
	if !ok {
		t.Fatal("the oldest run lost its record, and a record outlives its own output")
	}
	if got.Jobs[0].Steps[0].Output != "" {
		t.Errorf("the oldest run kept %d bytes of output past the ceiling", len(got.Jobs[0].Steps[0].Output))
	}
	if got.Conclusion != ConclusionSuccess || got.Jobs[0].JobID != "unit" {
		t.Errorf("the oldest run's verdict did not survive its log: %+v", got)
	}
}

// The age half of the same bound: output old enough to be of no use to
// anybody goes whatever room there is under the ceiling.
func TestOutputPastTheAgeIsSweptWithRoomToSpare(t *testing.T) {
	runs := storedTestStore(t, t.TempDir(), StoredRunStoreOptions{
		Retention: keeping(StoredRunRetention{OutputBytes: 1 << 30, OutputAge: 24 * time.Hour}),
	})
	now := time.Now().UTC()
	runs.Put(storedTestRunPrinting("last-month", now.Add(-30*24*time.Hour), 1<<10))
	runs.Put(storedTestRunPrinting("this-morning", now.Add(-time.Hour), 1<<10))

	old, ok := runs.Get("last-month")
	if !ok {
		t.Fatal("a month-old run lost its record, and the age bounds output rather than history")
	}
	if old.Jobs[0].Steps[0].Output != "" {
		t.Errorf("a month-old run kept its output under a one-day age bound")
	}
	fresh, ok := runs.Get("this-morning")
	if !ok || fresh.Jobs[0].Steps[0].Output == "" {
		t.Errorf("this morning's run should still have its output: %+v", fresh)
	}
}

// A run larger than the whole ceiling is the case a byte bound has and a
// count of runs does not: swept for room, it would evict every older
// run's output and still not fit. It is declined on the way in instead,
// and says so where a reader will look.
func TestAnOversizedRunsOutputIsDeclinedRatherThanEvictingTheRunsBehindIt(t *testing.T) {
	const ceiling = 8 << 10
	runs := storedTestStore(t, t.TempDir(), StoredRunStoreOptions{
		Retention: keeping(StoredRunRetention{OutputBytes: ceiling, RunOutputBytes: 64 << 10}),
	})
	start := time.Now().UTC().Add(-time.Hour)
	runs.Put(storedTestRunPrinting("modest", start, 1<<10))
	runs.Put(storedTestRunPrinting("firehose", start.Add(time.Minute), 32<<10))

	loud, ok := runs.Get("firehose")
	if !ok {
		t.Fatal("the oversized run lost its record")
	}
	note := loud.Jobs[0].Steps[0].Output
	if !strings.Contains(note, "declined") {
		t.Errorf("the declined step reads %q, which does not tell a reader its output was declined rather than never printed", note)
	}
	if len(note) >= ceiling {
		t.Errorf("the note in place of the output is %d bytes, which is no smaller than the ceiling it explains", len(note))
	}
	// The whole point: the runs behind it kept theirs.
	quiet, ok := runs.Get("modest")
	if !ok {
		t.Fatal("the earlier run lost its record")
	}
	if len(quiet.Jobs[0].Steps[0].Output) != 1<<10 {
		t.Errorf("the earlier run kept %d bytes, want its whole log -- one oversized run must not evict the history behind it",
			len(quiet.Jobs[0].Steps[0].Output))
	}
}

// Retention is a stored setting, so what has to hold is that a change to
// it binds the runs this daemon is already keeping: no restart, no new
// store, the next run that concludes swept under the new numbers.
func TestRetentionIsReReadOnEverySweepRatherThanHeldFromStartup(t *testing.T) {
	keep := StoredRunRetention{History: 10, OutputBytes: 1 << 30}
	runs := storedTestStore(t, t.TempDir(), StoredRunStoreOptions{
		Retention: func() StoredRunRetention { return keep },
	})
	start := time.Now().UTC().Add(-time.Hour)
	for i, id := range []string{"first", "second", "third"} {
		runs.Put(storedTestRunPrinting(id, start.Add(time.Duration(i)*time.Minute), 1<<10))
	}
	if _, ok := runs.Get("first"); !ok {
		t.Fatal("a history of ten should have kept all three")
	}

	// An operator narrows both bounds in the Settings pane. The daemon is
	// not restarted; the store is not rebuilt.
	keep = StoredRunRetention{History: 2, OutputBytes: 1 << 10}
	runs.Put(storedTestRunPrinting("fourth", start.Add(3*time.Minute), 1<<10))

	if _, ok := runs.Get("first"); ok {
		t.Error("the first run survived a history lowered to two -- retention was read once, at startup")
	}
	if _, ok := runs.Get("second"); ok {
		t.Error("the second run survived a history lowered to two")
	}
	third, ok := runs.Get("third")
	if !ok {
		t.Fatal("the third run should be one of the two kept")
	}
	if third.Jobs[0].Steps[0].Output != "" {
		t.Error("the third run kept its output under a ceiling with room for one run's")
	}
	newest, ok := runs.Get("fourth")
	if !ok || newest.Jobs[0].Steps[0].Output == "" {
		t.Errorf("the newest run should have its output: %+v", newest)
	}
}

// And the same policy widened rather than narrowed: nothing is deleted
// to make a larger number true, and the runs stored from then on live
// under it.
func TestRaisingRetentionKeepsWhatWouldHaveBeenSwept(t *testing.T) {
	keep := StoredRunRetention{History: 1, OutputBytes: 1 << 10}
	runs := storedTestStore(t, t.TempDir(), StoredRunStoreOptions{
		Retention: func() StoredRunRetention { return keep },
	})
	start := time.Now().UTC().Add(-time.Hour)
	runs.Put(storedTestRunPrinting("first", start, 512))

	keep = StoredRunRetention{History: 100, OutputBytes: 1 << 20}
	runs.Put(storedTestRunPrinting("second", start.Add(time.Minute), 512))
	for _, id := range []string{"first", "second"} {
		got, ok := runs.Get(id)
		if !ok {
			t.Fatalf("run %q was swept under a retention that had already been raised", id)
		}
		if got.Jobs[0].Steps[0].Output == "" {
			t.Errorf("run %q lost its output under a raised ceiling", id)
		}
	}
}

// The listing is a poll rather than the history: what it answers with is
// bounded whatever a deployment keeps, so raising the history does not
// turn the CI pane's five-second refresh into tens of megabytes.
func TestTheListingIsBoundedIndependentlyOfTheHistory(t *testing.T) {
	runs := storedTestStore(t, t.TempDir(), StoredRunStoreOptions{
		Retention: keeping(StoredRunRetention{History: 100000}),
	})
	if got := runs.listed(); got != MaxListedRuns {
		t.Errorf("a deployment keeping 100000 runs lists %d of them, want %d", got, MaxListedRuns)
	}
	// A deployment that keeps fewer than the listing bound still sees all
	// of them.
	short := storedTestStore(t, t.TempDir(), StoredRunStoreOptions{
		Retention: keeping(StoredRunRetention{History: 7}),
	})
	if got := short.listed(); got != 7 {
		t.Errorf("a deployment keeping 7 runs lists %d of them", got)
	}
}

// --- the filtered read (grain/task-289) ------------------------------

// keepingEverything is the retention a filtering test wants: no history
// bound and no output sweep, so that what a filter answers with is about
// the filter rather than about what a Put in the middle of the test
// happened to prune.
func keepingEverything() func() StoredRunRetention {
	return keeping(StoredRunRetention{History: -1, OutputBytes: -1, OutputAge: -1})
}

// filterTestRuns is a deployment with enough shape to ask a narrow
// question of: two repositories, three branches, a job that fails on one
// branch and passes on another, a run still going and one queued, and a
// run old enough to fall outside a window.
func filterTestRuns(now time.Time) []Run {
	at := func(m int) *time.Time { t := now.Add(-time.Duration(m) * time.Minute); return &t }
	job := func(name string, conclusion Conclusion, started *time.Time, finished *time.Time) JobRun {
		return JobRun{
			Workflow: "tests", WorkflowPath: ".grain/workflows/tests.yml", JobID: name, Name: name,
			Conclusion: conclusion, StartedAt: started, FinishedAt: finished,
		}
	}
	return []Run{
		// Queued: no job has begun, so nothing about it has started.
		{ID: "queued", Key: "key-queued", Repo: "acme/widgets", Ref: "grain/task-289",
			Jobs: []JobRun{job("ui e2e", ConclusionPending, nil, nil)}},
		// Running: a job on a machine, the run itself unconcluded.
		{ID: "running", Key: "key-running", Repo: "acme/widgets", Ref: "main", StartedAt: at(5),
			Jobs: []JobRun{job("ui e2e", ConclusionPending, at(5), nil)}},
		{ID: "red-main", Key: "key-red-main", Repo: "acme/widgets", Ref: "main",
			StartedAt: at(60), FinishedAt: at(18), Conclusion: ConclusionFailure,
			Jobs: []JobRun{
				job("go", ConclusionSuccess, at(60), at(57)),
				job("ui e2e", ConclusionFailure, at(60), at(18)),
			}},
		{ID: "green-task", Key: "key-green-task", Repo: "acme/widgets", Ref: "grain/task-289",
			StartedAt: at(200), FinishedAt: at(197), Conclusion: ConclusionSuccess,
			Jobs: []JobRun{job("ui e2e", ConclusionSuccess, at(200), at(197))}},
		{ID: "old-errored", Key: "key-old-errored", Repo: "acme/gadgets", Ref: "main",
			StartedAt: at(5000), FinishedAt: at(4999), Conclusion: ConclusionErrored,
			Jobs: []JobRun{job("go", ConclusionErrored, at(5000), at(4999))}},
	}
}

func foundIDs(page RunPage) string {
	ids := make([]string, 0, len(page.Runs))
	for _, run := range page.Runs {
		ids = append(ids, run.ID)
	}
	return strings.Join(ids, " ")
}

// The filter's whole vocabulary, asked of SQL and of the map, and
// answered the same way by both.
//
// Written as a comparison rather than as expected ids alone because the
// two stores are one contract with two implementations: "repo" means a
// whole name without regard to case in both, "ref" means a substring in
// both, and a run that has not started is inside every window in both.
// A SQL clause that quietly disagreed would be a deployment whose CI
// pane and whose `grain ci` answered differently about the same runs.
func TestAStoredFilterAnswersAsTheInMemoryOneDoes(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	stored := storedTestStore(t, t.TempDir(), StoredRunStoreOptions{
		Retention: keepingEverything(),
		Now:       func() time.Time { return now },
	})
	memory := NewRunStore(0)
	for _, run := range filterTestRuns(now) {
		stored.Put(run)
		memory.Put(run)
	}

	for _, tc := range []struct {
		name   string
		filter RunFilter
		want   string
	}{
		{"everything", RunFilter{Descending: true}, "running red-main green-task old-errored queued"},
		{"one repo", RunFilter{Repo: "acme/gadgets", Descending: true}, "old-errored"},
		{"a repo however it is typed", RunFilter{Repo: "ACME/Widgets", Descending: true},
			"running red-main green-task queued"},
		{"a branch by its tail", RunFilter{Ref: "task-289", Descending: true}, "green-task queued"},
		{"what is red right now", RunFilter{
			States: []string{string(ConclusionFailure), string(ConclusionErrored)}, Descending: true},
			"red-main old-errored"},
		{"still going", RunFilter{States: []string{StateRunning}, Descending: true}, "running"},
		{"waiting for a machine", RunFilter{States: []string{StatePending}, Descending: true}, "queued"},
		{"one job, whatever it did", RunFilter{Job: "ui e2e", Descending: true},
			"running red-main green-task queued"},
		{"a job by its id and case", RunFilter{Job: "UI E2E", Descending: true},
			"running red-main green-task queued"},
		{"a window, which a queued run is always inside", RunFilter{
			Since: now.Add(-2 * time.Hour), Descending: true}, "running red-main queued"},
		{"a window with an end, which a queued run is outside", RunFilter{
			Since: now.Add(-300 * time.Minute), Until: now.Add(-30 * time.Minute), Descending: true},
			"red-main green-task"},
		{"this job, red, on any branch", RunFilter{
			Job: "ui e2e", States: []string{string(ConclusionFailure)}, Descending: true}, "red-main"},
		{"oldest first", RunFilter{Sort: SortRunsByStarted}, "old-errored green-task red-main running queued"},
		{"longest first", RunFilter{Sort: SortRunsByDuration, Descending: true},
			"red-main running green-task old-errored queued"},
		{"latest to finish first", RunFilter{Sort: SortRunsByFinished, Descending: true},
			"red-main green-task old-errored running queued"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := foundIDs(stored.Find(tc.filter, 0))
			if got != tc.want {
				t.Errorf("the stored store found %q, want %q", got, tc.want)
			}
			if inMemory := foundIDs(memory.Find(tc.filter, 0)); inMemory != got {
				t.Errorf("the same filter found %q in memory and %q in SQL", inMemory, got)
			}
		})
	}
}

// The point of the exercise: a narrowed question reaches a run the
// listing's own window cannot, which is what a deployment keeping a
// fortnight of history raised that history for (grain/task-269 raised
// it; this is the half it left).
func TestAFilteredReadReachesPastTheListingsWindow(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	runs := storedTestStore(t, t.TempDir(), StoredRunStoreOptions{
		Retention: keepingEverything(),
		Now:       func() time.Time { return now },
	})
	// One run from last week, and then more than a listing answers with
	// on top of it.
	old := storedTestRun("last-week", now.Add(-7*24*time.Hour))
	old.Repo = "acme/gadgets"
	old.Ref = "grain/task-231"
	old.Conclusion = ConclusionFailure
	old.Jobs[0].Name = "ui e2e"
	old.Jobs[0].Conclusion = ConclusionFailure
	old.FinishedAt = timePtr(old.StartedAt.Add(time.Minute))
	runs.Put(old)
	for i := 0; i < MaxListedRuns+5; i++ {
		run := storedTestRun(fmt.Sprintf("recent-%d", i), now.Add(-time.Duration(i)*time.Minute))
		run.Conclusion = ConclusionSuccess
		run.FinishedAt = timePtr(run.StartedAt.Add(time.Minute))
		runs.Put(run)
	}

	listed := runs.List()
	if len(listed) != MaxListedRuns {
		t.Fatalf("the listing answered with %d runs, want the %d it is bounded to", len(listed), MaxListedRuns)
	}
	for _, run := range listed {
		if run.ID == "last-week" {
			t.Fatal("last week's run is inside the listing's window, so this test proves nothing")
		}
	}

	page := runs.Find(RunFilter{Job: "ui e2e", States: []string{string(ConclusionFailure)}, Descending: true}, 0)
	if got := foundIDs(page); got != "last-week" {
		t.Errorf("a narrowed read found %q, want the run from last week the listing cannot reach", got)
	}
	if page.Stored != MaxListedRuns+6 {
		t.Errorf("the page says this deployment holds %d runs, want %d", page.Stored, MaxListedRuns+6)
	}
	if page.Matched != 1 {
		t.Errorf("the page says %d runs matched, want 1", page.Matched)
	}
}

// A page says what it is a page of: how many matched, and how many the
// deployment holds. Without those two an answer cut by a limit is
// indistinguishable from an answer that is all there is.
func TestAFilteredPageSaysWhatItLeftOut(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	runs := storedTestStore(t, t.TempDir(), StoredRunStoreOptions{
		Retention: keepingEverything(),
		Now:       func() time.Time { return now },
	})
	for _, run := range filterTestRuns(now) {
		runs.Put(run)
	}

	page := runs.Find(RunFilter{Repo: "acme/widgets", Descending: true}, 2)
	if got := foundIDs(page); got != "running red-main" {
		t.Errorf("a page of 2 held %q", got)
	}
	if page.Matched != 4 || page.Stored != 5 {
		t.Errorf("page said %d of %d matched, want 4 of 5", page.Matched, page.Stored)
	}
	// A page carries a run's jobs, since a row is drawn from them, and
	// not a step's output, which is what reading one run is for.
	if len(page.Runs[1].Jobs) != 2 {
		t.Errorf("the page dropped a run's jobs: %+v", page.Runs[1].Jobs)
	}
	// Nothing matched is a different answer from nothing stored, and
	// both are said in the same two numbers.
	empty := runs.Find(RunFilter{Repo: "acme/nothing"}, 0)
	if len(empty.Runs) != 0 || empty.Matched != 0 || empty.Stored != 5 {
		t.Errorf("an empty answer said %d runs, %d matched, %d stored", len(empty.Runs), empty.Matched, empty.Stored)
	}
}

// The limit a caller asks for is what the page carries, and a limit
// nobody asks for is the listing's own bound -- with a ceiling over both
// so that a typo costs a large answer rather than a daemon.
func TestAFilteredReadIsBoundedWhateverIsAskedFor(t *testing.T) {
	if got := foundRunLimit(0); got != MaxListedRuns {
		t.Errorf("no limit at all answered with %d runs, want %d", got, MaxListedRuns)
	}
	if got := foundRunLimit(7); got != 7 {
		t.Errorf("a limit of 7 answered with %d", got)
	}
	if got := foundRunLimit(MaxFoundRuns * 10); got != MaxFoundRuns {
		t.Errorf("a limit of %d answered with %d, want the %d ceiling", MaxFoundRuns*10, got, MaxFoundRuns)
	}
}

// A store that cannot be read answers with an empty page and says so in
// the log, which is what every other method here does -- RunStore's
// signatures carry no error on purpose.
func TestAFilteredReadOnABrokenStoreIsEmptyRatherThanFatal(t *testing.T) {
	var logged []string
	runs, db := storedTestStoreDB(t, t.TempDir(), StoredRunStoreOptions{
		Log: func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	})
	runs.Put(storedTestRun("3f2a1c", time.Now().UTC()))
	db.Close()

	page := runs.Find(RunFilter{Repo: "owner/payments-api"}, 0)
	if len(page.Runs) != 0 || page.Matched != 0 {
		t.Errorf("a broken store answered with %+v", page)
	}
	if len(logged) == 0 {
		t.Error("a failed read said nothing in the log")
	}
}

// A reader bounded by RunFilter.Visible is told neither of a run it may
// not see nor of its being counted: Matched and Stored are both over
// what it may see, so "N of M" does not leak another team's M. The
// in-memory store says the same.
func TestFindLeavesOutWhatTheReaderMayNotSee(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	stored := storedTestStore(t, t.TempDir(), StoredRunStoreOptions{})
	memory := NewRunStore(10)
	for i, spec := range []struct{ id, repo string }{
		{"a1", "acme/alpha"},
		{"a2", "acme/alpha"},
		{"b1", "acme/beta"},
		{"b2", "acme/beta"},
	} {
		run := storedTestRun(spec.id, now.Add(time.Duration(i)*time.Second))
		run.Repo = spec.repo
		stored.Put(run)
		memory.Put(run)
	}
	visible := func(repo string) bool { return repo == "acme/alpha" }
	for name, store := range map[string]RunStore{"stored": stored, "memory": memory} {
		page := store.Find(RunFilter{Visible: visible, Descending: true}, 0)
		if page.Matched != 2 || page.Stored != 2 || len(page.Runs) != 2 {
			t.Errorf("%s: page = %d runs, %d matched of %d stored, want 2 of 2", name, len(page.Runs), page.Matched, page.Stored)
		}
		page = store.Find(RunFilter{Visible: visible, Ref: "task", Descending: true}, 1)
		if page.Matched != 2 || page.Stored != 2 || len(page.Runs) != 1 || page.Runs[0].Repo != "acme/alpha" {
			t.Errorf("%s: narrowed page = %+v, want one of alpha's two runs, of 2 stored", name, page)
		}
	}
}

// A Put that lands while an Update is between its read and its write is
// not undone by that write. Service.Cancel notes its reason with Update;
// a second Cancel of a run that was already stopping read the run still
// in flight, the run's own goroutine concluded it with Put, and the note
// then wrote the in-flight copy back over the conclusion -- leaving a run
// nothing would ever conclude (ci_mode grain -> github cancels a task's
// run twice: docs/test-runs/2026-11-01-github-actions-ci-workflow-run.md).
func TestAPutDuringAnUpdateIsNotOverwrittenByIt(t *testing.T) {
	runs := storedTestStore(t, t.TempDir(), StoredRunStoreOptions{})
	started := time.Now().UTC().Truncate(time.Millisecond)
	live := storedTestRun("c4nc31", started)
	runs.Put(live)

	concluded := cloneRun(live)
	concluded.Conclusion, concluded.FinishedAt = ConclusionCancelled, timePtr(started.Add(time.Second))
	concluded.Jobs[0].Conclusion = ConclusionCancelled
	put := make(chan struct{})
	runs.Update(live.ID, func(r *Run) {
		go func() {
			runs.Put(concluded)
			close(put)
		}()
		// Long enough for an unserialized Put to have finished.
		select {
		case <-put:
		case <-time.After(300 * time.Millisecond):
		}
		r.CancelReason = "repository switched to GitHub CI"
	})
	<-put
	got, ok := runs.Get(live.ID)
	if !ok || got.Conclusion != ConclusionCancelled || got.FinishedAt == nil {
		t.Fatalf("after a Put raced an Update the run is %s (finished %v), want the Put's conclusion", got.Conclusion, got.FinishedAt)
	}
}
