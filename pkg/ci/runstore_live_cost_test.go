package ci

// What does a running step's live output cost the database?
//
// staterepo's cigrowth_test.go asks what a run costs the state
// repository. This asks the question grain/task-164 was opened on, which
// is a different one and now the only one: the ci_* tables are not in
// the state repository at all (staterepo.ExportConfig), so a hundred
// publications cost that repository exactly what none of them does.
// What they cost is
// paid in SQLite, once every ci.LiveOutputInterval, for as long as the
// step prints -- and until grain/task-164 each one was a transaction that
// replaced the run row, deleted every job and step under it and
// re-inserted them all in order to change one cell.
//
// So both writes are measured here, on the same workload, through the
// real StoredRunStore:
//
//   - "narrow" is what Update does now: read the run, notice that one
//     step's output is the whole of the difference, UPDATE that cell.
//   - "whole run" is what Update did before, spelled out at the bottom of
//     this file so it can go on being measured rather than remembered.
//   - "read only" is the read both of them do first, on its own.
//
// It asserts nothing. It is a measurement, and the tests either side of
// it are where the behaviour is pinned; run it with -v.
//
// Measured on 2026-09-17, on this build (three cores, an embedded SQLite
// file, 48 publications of one step):
//
//	grain's own suite, one step printing -- 2 jobs, 8 steps, 64 KiB stored
//	  whole run  4.29ms   11 rows,    64.0 KiB of output
//	  narrow     1.67ms    1 row,      8.0 KiB
//	  read only  1.47ms
//	a chatty run, one step printing -- 12 jobs, 72 steps, 256 KiB stored
//	  whole run 10.09ms   85 rows,   256.0 KiB of output
//	  narrow     1.92ms    1 row,      3.6 KiB
//	  read only  1.58ms
//
// The finding is the third line of each: what a publication costs now is
// very nearly the read it has to do anyway -- the write itself went from
// 2.8ms to 0.2ms on grain's own shape, and from 8.5ms to 0.35ms on the
// chatty one -- and the rows and bytes are why. The whole-run write
// carries the run; the narrow one carries the line the step just printed.
// If a deployment ever finds this hot again, the read is what is left,
// and StoredRunStore's own doc comment says what to do about it.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

// livePublications is how many times one step publishes: forty minutes
// of a `go test` at LiveOutputInterval is 480, which is also roughly the
// most any single step of a grain CI job manages. A tenth of that is
// plenty to time and keeps this cheap enough to leave in every build.
const livePublications = 48

func TestLiveOutputWriteCost(t *testing.T) {
	if testing.Short() {
		t.Skip("measures what a live publication costs the database; nothing to assert")
	}
	for _, c := range []struct {
		name        string
		jobs, steps int
		printed     int // bytes each step other than the live one has printed
	}{
		{"grain's own suite, one step printing", 2, 4, 8 << 10},
		{"a chatty run, one step printing", 12, 6, 2 * MaxStoredStepOutput},
	} {
		t.Run(c.name, func(t *testing.T) {
			run := liveCostRun(c.jobs, c.steps, c.printed)
			rows := 1
			for _, job := range run.Jobs {
				rows += 1 + len(job.Steps)
			}
			t.Logf("%d jobs, %d steps, %s of output stored",
				len(run.Jobs), c.jobs*c.steps, liveCostBytes(int64(storedOutputOf(run))))
			runLiveCost(t, "whole run", run, rows, wholeRunUpdate)
			runLiveCost(t, "narrow", run, 1, func(s *StoredRunStore, id string, fn func(*Run)) {
				s.Update(id, fn)
			})
			// The read Update does before either write, on its own: what
			// is left of a publication once the write is narrow, and so
			// the next thing to look at if this is ever hot again.
			runLiveCost(t, "read only", run, 0, func(s *StoredRunStore, id string, fn func(*Run)) {
				s.Get(id)
			})
		})
	}
}

// runLiveCost drives one step through livePublications publications and
// reports what they cost: the wall time per publication, and what one
// transaction carried to change one cell.
//
// rows is what the write touches by construction rather than by
// measurement -- SQLite has no counter for "rows this statement wrote"
// that survives the transaction, and the number is not in doubt: the
// whole-run write deletes and re-inserts every row under the run, the
// narrow one updates one.
func runLiveCost(t *testing.T, what string, run Run, rows int, update func(*StoredRunStore, string, func(*Run))) {
	t.Helper()
	ctx := context.Background()
	runs, db := storedTestStoreDB(t, t.TempDir(), StoredRunStoreOptions{
		Log: func(format string, args ...any) { t.Errorf("ci: "+format, args...) },
	})
	runs.Put(run)

	job, step := len(run.Jobs)-1, len(run.Jobs[0].Steps)-1
	commits := runs.store.CommitCount()
	start := time.Now()
	for i := 0; i < livePublications; i++ {
		text := livePublishedText(i, run.Jobs[job].Steps[step].Output)
		update(runs, run.ID, func(r *Run) { r.Jobs[job].Steps[step].Output = text })
	}
	elapsed := time.Since(start)

	t.Logf("  %-9s %s per publication (%d in %s), %d commit(s)",
		what, (elapsed / livePublications).Round(time.Microsecond), livePublications,
		elapsed.Round(time.Millisecond), runs.store.CommitCount()-commits)
	if rows == 0 {
		return // nothing was written; the line above is the whole of it
	}
	// What one transaction carried: for the whole-run write, every step's
	// output in the run, which is the whole of this database's ci_step;
	// for the narrow one, the cell it changed.
	written := storedOutputBytes(ctx, t, db, "")
	if rows == 1 {
		written = storedOutputBytes(ctx, t, db,
			fmt.Sprintf("WHERE `job_index` = %d AND `step_index` = %d", job, step))
	}
	t.Logf("            one transaction: %d row(s), %s of output", rows, liveCostBytes(written))
}

// wholeRunUpdate is StoredRunStore.Update as it was before task-164: read
// the run, apply the change, write the whole thing back. Kept so the
// comparison above is with the real thing rather than with a memory of
// it, and so that a deployment that ever wants the number again can have
// it.
func wholeRunUpdate(s *StoredRunStore, id string, fn func(*Run)) {
	run, ok := s.Get(id)
	if !ok {
		return
	}
	fn(&run)
	s.Put(run)
}

// liveCostRun is a run of the given shape, every step of it finished and
// carrying what it printed, except the last step of the last job -- the
// one still running, and the one the publications are about.
func liveCostRun(jobs, steps, printed int) Run {
	started := time.Now().UTC()
	run := Run{
		ID: "cost", Key: "key-cost", Repo: "owner/payments-api", Ref: "grain/task-164",
		Commit: "9f8e7d6c", Event: EventPush, StartedAt: &started,
	}
	for j := 0; j < jobs; j++ {
		job := JobRun{
			Workflow: "tests", WorkflowPath: ".grain/workflows/tests.yml",
			JobID: fmt.Sprintf("job-%d", j), Name: fmt.Sprintf("job %d", j),
			Key: Key(fmt.Sprintf("key-%d", j)), Sandbox: "grain-ci-cost", StartedAt: &started,
		}
		for s := 0; s < steps; s++ {
			job.Steps = append(job.Steps, StepRun{
				Name: fmt.Sprintf("step %d", s), Conclusion: ConclusionSuccess,
				Output: strings.Repeat("x", printed), StartedAt: &started,
			})
		}
		run.Jobs = append(run.Jobs, job)
	}
	live := &run.Jobs[len(run.Jobs)-1].Steps[steps-1]
	live.Conclusion = ConclusionPending
	live.FinishedAt = nil
	return run
}

// livePublishedText is what the running step has printed by publication
// i: the same log with another line on the end of it, which is what a
// step that is still going actually sends.
func livePublishedText(i int, printed string) string {
	return printed + fmt.Sprintf("=== RUN   TestNumber%d\n", i)
}

func storedOutputOf(run Run) int {
	total := 0
	stored, _ := storedRun(run, DefaultStoredRunRetention())
	for _, job := range stored.Jobs {
		for _, step := range job.Steps {
			total += len(step.Output)
		}
	}
	return total
}

// storedOutputBytes is what the `output` column holds across ci_step, or
// across the rows a where clause names. This database has one run in it,
// so with no clause it is that run's whole output.
func storedOutputBytes(ctx context.Context, t *testing.T, db *sql.DB, where string) int64 {
	t.Helper()
	var n sql.NullInt64
	if err := db.QueryRowContext(ctx, "SELECT SUM(LENGTH(`output`)) FROM `ci_step` "+where).Scan(&n); err != nil {
		t.Fatalf("measuring ci_step: %v", err)
	}
	return n.Int64
}

func liveCostBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
