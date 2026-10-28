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

package staterepo_test

// Is what reaches the repository one state of the database, or several?
//
// Every other test here writes rows, exports, and reads them back with
// nothing else happening. A deployment is not like that: the export runs
// on a timer inside the daemon that is also filing tasks, starting runs
// and stamping observations, and it reads one table per statement. These
// tests are about the seam that opens between two of those statements.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// A dump has to be a snapshot of one database, not a series of reads
// taken while it moved.
//
// The tables are read in name order, so `task` is read before
// `task_run`: a task filed and dispatched in the milliseconds between
// the two used to produce a dump whose task_run.json named a task
// task.json did not have. Nothing rejected it -- this schema declares no
// foreign keys today -- so it arrived as a restore with runs belonging
// to tasks that were not there.
func TestAnExportIsASnapshotOfOneDatabase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("seed-%03d", i)
		if err := store.PutTask(ctx, task(id)); err != nil {
			t.Fatalf("putting: %v", err)
		}
		if err := store.StartRun(ctx, model.Run{
			ID: id + "-1", TaskID: id, Sandbox: "grain-" + id, Attempt: 1, StartedAt: now,
		}, model.Limits{}); err != nil {
			t.Fatalf("starting a run: %v", err)
		}
	}

	// A writer doing what the daemon does: filing a task and dispatching
	// it, as fast as it can, for as long as the exports take.
	stop := make(chan struct{})
	failed := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			id := fmt.Sprintf("live-%05d", i)
			if err := store.PutTask(ctx, task(id)); err != nil {
				select {
				case failed <- err:
				default:
				}
				return
			}
			if err := store.StartRun(ctx, model.Run{
				ID: id + "-1", TaskID: id, Sandbox: "grain-" + id, Attempt: 1, StartedAt: now,
			}, model.Limits{}); err != nil {
				select {
				case failed <- err:
				default:
				}
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	for attempt := 0; attempt < 10; attempt++ {
		dir := t.TempDir()
		if err := staterepo.Export(ctx, db, dir); err != nil {
			t.Fatalf("exporting: %v", err)
		}
		tasks := columnValues(t, filepath.Join(dir, staterepo.TablesDir, "task.json"), "id")
		runs := columnValues(t, filepath.Join(dir, staterepo.TablesDir, "task_run.json"), "task_id")
		for taskID := range runs {
			if !tasks[taskID] {
				t.Fatalf("export %d is not a snapshot: task_run.json names task %q, "+
					"which task.json does not have", attempt, taskID)
			}
		}
	}
	close(stop)
	wg.Wait()
	select {
	case err := <-failed:
		t.Fatalf("the concurrent writer failed, so the exports above raced nothing: %v", err)
	default:
	}
}

// And the export must not do that by locking the database: the daemon
// goes on writing while it runs. pkg/model/sqlite opens the store with
// _txlock=immediate, so a transaction begun without saying it is
// read-only takes SQLite's write lock -- and an export of every
// transcript grain has stored would hold it for as long as that takes.
//
// From the daemon's side that shows up as its writes stopping for the
// length of the export, so what this measures is a writer's rate while
// exports run against the same writer's rate a moment earlier with
// nothing in its way. The test below holds the write lock across an
// export on purpose and requires the same comparison to catch it, which
// is what keeps the threshold here honest: it is a number shown to sit
// between a blocking export and a well-behaved one, not one nothing can
// fail.
func TestAnExportDoesNotBlockTheDaemonFromWriting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	seedTranscripts(t, store)

	dir := t.TempDir()
	idle, busy := writerRates(t, store, func() {
		if err := staterepo.Export(ctx, db, dir); err != nil {
			t.Fatalf("exporting: %v", err)
		}
	})
	t.Logf("the writer managed %.0f writes/s while exports ran, against %.0f/s with nothing "+
		"in its way (wanted at least %.0f/s)", busy, idle, idle/allowedSlowdown)
	if busy < idle/allowedSlowdown {
		t.Fatalf("the writer managed %.0f writes/s while exports ran and %.0f/s with nothing "+
			"in its way: the export is holding SQLite's write lock and the daemon is "+
			"waiting on it", busy, idle)
	}
}

// The test above can fail. An export that takes SQLite's write lock is
// not a thing this suite can arrange -- ExportTier asks for a read-only
// transaction and gets a read snapshot -- so this arranges the same
// consequence a regression there would have: an export with a write
// transaction held open across it, which is exactly what would happen if
// that ReadOnly were dropped or if the driver stopped honouring it.
//
// It exists because the check above is a comparison of two measured
// rates, and a comparison like that can rot into one that passes
// whatever happens -- a threshold nudged down after a flake, a writer
// that stopped writing, a window too short to hold a write. If the
// writer keeps its rate up while the write lock is held, the failure is
// in the measurement rather than in the database, and this says so.
//
// The lock is taken by writerRatesHolding, between the two windows, and
// held across the whole of the busy one -- so what the busy rate is
// taken over is the export alone, with the writer locked out for all of
// it. Taking it inside the timed work instead is what made this test
// itself load-dependent; holdTheWriteLock's comment has the measurement.
func TestABlockingExportFailsTheTestAbove(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	seedTranscripts(t, store)

	dir := t.TempDir()
	idle, busy := writerRatesHolding(t, store, holdTheWriteLock(t, db), func() {
		if err := staterepo.Export(ctx, db, dir); err != nil {
			t.Fatalf("exporting: %v", err)
		}
	})
	t.Logf("with the write lock held across each export the writer managed %.0f writes/s, "+
		"against %.0f/s with nothing in its way (wanted under %.0f/s)",
		busy, idle, idle/allowedSlowdown)
	if busy >= idle/allowedSlowdown {
		t.Fatalf("the writer managed %.0f writes/s with SQLite's write lock held across every "+
			"export and %.0f/s with nothing in its way: a blocked daemon is not far enough "+
			"below an unblocked one for the test above to tell them apart", busy, idle)
	}
}

// How far the writer is allowed to fall behind its own unobstructed rate
// before the export is held to be blocking it.
//
// Both ends of this are measured, and the two tests above hold it to
// both of them. An export that blocks nobody still competes for the same
// disk and the same cores, so some slowdown is honest: over sixty runs
// the writer kept ~100% of its rate through an export on an idle
// four-core box, and never less than 36% of it in any loaded arrangement
// tried -- three busy loops per core, `go test -race`, GOMAXPROCS pinned
// to two and to one. An export holding the write lock left it 0-5%
// across the same set. An eighth sits between the two with about three
// times the margin on either side.
const allowedSlowdown = 8

// How long the writer waits between one task and the next.
//
// Ten milliseconds is far faster than grain's daemon files anything and
// slow enough to leave the write lock free most of the time, which is
// what makes the two rates comparable at all. At one millisecond the
// writer holds the lock for most of every millisecond, and whatever else
// wants it waits on SQLite's busy handler -- which backs off in sleeps
// of up to 100ms, and the writer writes through every one of them.
// Measured: a blocking export spent so long getting the lock that the
// writer kept a quarter of its rate through it, which is what an
// unblocked writer looks like, and the test above that exists to catch
// exactly that missed it once in twenty runs. Backing the writer off to
// ten milliseconds drops the same measurement to 0-5%.
const writerPause = 10 * time.Millisecond

// writerRates runs a writer doing what the daemon does -- filing a task,
// pausing, filing the next -- and reports the rate it manages with
// nothing in its way and the rate it manages while work runs, in writes
// per second.
//
// The two are measured over windows of the same length, on the same
// machine, seconds apart, and work is called over and over for the whole
// of the second one. That is what makes the comparison mean something a
// wall-clock threshold could not. The old form of this test asserted one
// write per ten milliseconds of export, on the reasoning that a writer
// sleeping a millisecond between tasks manages far more than that. It
// does on an idle machine; on a loaded hosted runner a single PutTask
// can take ten milliseconds by itself, and the test failed reporting a
// write lock nobody was holding. Judging the writer against itself
// removes the machine's speed from the answer -- and measuring over a
// fixed window rather than over one export's duration removes the other
// half of it, that a 25ms export leaves too little room for the
// difference between one write and none to mean anything.
func writerRates(t *testing.T, store *model.Store, work func()) (idle, busy float64) {
	t.Helper()
	return writerRatesHolding(t, store, nil, work)
}

// writerRatesHolding is writerRates with something held across the busy
// window and only that window: hold is called once the idle rate has
// been taken, and whatever it returns is called once the busy rate has.
//
// The point of it is where the hold is *not*. SQLite's write lock is the
// only thing any caller holds here, and the test that holds it used to
// take it at the top of its own work function, inside the stretch
// writerRates times as "the export running". On a loaded machine that
// measured the wrong thing: the writer's own PutTask is slow enough
// there to hold the write lock most of the time, so BEGIN IMMEDIATE
// starves against it behind SQLite's busy handler, and the seconds spent
// waiting for the lock were counted as seconds an export was running --
// with the writer writing freely through every one of them. Measured on
// three cores under `go test -race`: a single 2.6s "export" over which
// the writer kept 8 writes/s of its 21, and a test whose whole job is to
// prove the threshold catches a blocked daemon reporting that it does
// not. Held from out here, work() is the export alone and the writer is
// genuinely locked out for the whole of what is measured.
//
// Being locked out for that long is not itself a way for the writer to
// fail. It waits the busy window (measureAtMost at most, plus the one
// export that overruns it) against the five seconds of busy_timeout that
// pkg/model/sqlite opens the store with -- and Store.write gives a write
// five attempts at that before it gives up.
func writerRatesHolding(t *testing.T, store *model.Store, hold func() (release func()), work func()) (idle, busy float64) {
	t.Helper()
	ctx := context.Background()
	var written atomic.Int64
	stop := make(chan struct{})
	done := make(chan struct{})
	failed := make(chan error, 1)
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if err := store.PutTask(ctx, task(fmt.Sprintf("during-%05d", i))); err != nil {
				select {
				case failed <- err:
				default:
				}
				return
			}
			written.Add(1)
			time.Sleep(writerPause)
		}
	}()

	// Let the writer get going, so that a rate of nothing below means it
	// was held up rather than that it had not started.
	time.Sleep(20 * time.Millisecond)
	from, start := written.Load(), time.Now()
	window := time.Duration(0)
	for {
		time.Sleep(10 * time.Millisecond)
		window = time.Since(start)
		// Long enough to have watched the writer for a while, and to have
		// counted enough of its writes that the same count taken again can
		// differ by something other than one. A machine too slow to manage
		// that many in measureFor gets a longer window rather than a
		// verdict off two writes -- up to a cap, past which its rate is
		// what it is.
		if window >= measureFor && written.Load()-from >= enoughWrites {
			break
		}
		if window >= measureAtMost {
			break
		}
	}
	idle = rate(written.Load()-from, window)

	var release func()
	if hold != nil {
		release = hold()
	}

	// The same stretch of the same machine, with work running for the
	// whole of it -- and the writer counted only over the parts of it work
	// was actually running.
	//
	// That last part matters, and it was measured rather than reasoned
	// about. A writer held off by the write lock does not stay held off:
	// the moment the lock is dropped, between one call and the next, every
	// write that was queued behind it lands at once. Counting a whole
	// window including those gaps credited a blocked writer with a quarter
	// of its unobstructed rate, enough to pass a check meant to catch
	// exactly that. Counted while the export is in flight, it manages a
	// few percent, which is what being blocked looks like.
	start = time.Now()
	var during, running = int64(0), time.Duration(0)
	calls := 0
	for ; time.Since(start) < window; calls++ {
		before, began := written.Load(), time.Now()
		work()
		running += time.Since(began)
		during += written.Load() - before
	}
	busy = rate(during, running)
	t.Logf("%d calls running %s of a %s window, against a %s window with nothing running",
		calls, running.Round(time.Millisecond), time.Since(start).Round(time.Millisecond),
		window.Round(time.Millisecond))

	// Before the writer is asked to stop, so that the write it is blocked
	// on when it is can land rather than waiting out busy_timeout and
	// reporting a failure this arranged.
	if release != nil {
		release()
	}
	close(stop)
	<-done
	select {
	case err := <-failed:
		t.Fatalf("the writer failed, so these rates measure nothing: %v", err)
	default:
	}
	if idle == 0 {
		t.Fatalf("no write landed in %s with nothing in the writer's way, so this machine "+
			"cannot say anything about what an export does to it", window)
	}
	return idle, busy
}

// holdTheWriteLock is the hold writerRatesHolding takes for
// TestABlockingExportFailsTheTestAbove: a transaction begun without
// TxOptions.ReadOnly, so pkg/model/sqlite's _txlock=immediate takes
// SQLite's write lock at BEGIN, and a release that rolls it back.
//
// Retried, because taking it is a race the writer beside it can win --
// BeginTx gives up after busy_timeout, and a PutTask that got there
// first holds the lock for as long as that one write takes. Each attempt
// is another five seconds of waiting the writer cannot spend on
// anything, so the loop is short; what it is insuring against is one
// unlucky handoff, not a machine on which the lock is never free.
func holdTheWriteLock(t *testing.T, db *sql.DB) func() (release func()) {
	return func() (release func()) {
		t.Helper()
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			var tx *sql.Tx
			tx, err = db.BeginTx(context.Background(), nil)
			if err == nil {
				return func() { _ = tx.Rollback() }
			}
		}
		t.Fatalf("taking the write lock: %v", err)
		return func() {}
	}
}

const (
	// How long each of writerRates' two windows is, at least. A rate is
	// only as good as the number of writes it is taken over: measuring
	// across one export's own duration -- 25ms on the runner this test
	// used to flake on -- is measuring whether one write landed or none,
	// and the difference between those two is scheduling luck rather than
	// anything about the export. This is short enough that two of them
	// plus the seeding keep the test to a couple of seconds.
	measureFor = 300 * time.Millisecond
	// Writes that have to land in that window before its rate is taken as
	// saying anything, and how far writerRates will stretch the window
	// waiting for them.
	enoughWrites  = 20
	measureAtMost = 3 * time.Second
)

// rate is writes per second. Zero for a window in which nothing landed,
// and for one of no duration -- neither says anything about a rate, and
// writerRates checks for it rather than dividing by zero here.
func rate(writes int64, over time.Duration) float64 {
	if writes <= 0 || over <= 0 {
		return 0
	}
	return float64(writes) / over.Seconds()
}

// seedTranscripts fills the store with enough rows, with transcripts on
// them, that an export of it is doing real work rather than finishing
// before the writer beside it has been scheduled.
func seedTranscripts(t *testing.T, store *model.Store) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 150; i++ {
		id := fmt.Sprintf("seed-%03d", i)
		if err := store.PutTask(ctx, task(id)); err != nil {
			t.Fatalf("putting: %v", err)
		}
		if err := store.StartRun(ctx, model.Run{
			ID: id + "-1", TaskID: id, Sandbox: "grain-" + id, Attempt: 1, StartedAt: now,
		}, model.Limits{}); err != nil {
			t.Fatalf("starting a run: %v", err)
		}
		if err := store.SetRunTranscript(ctx, id+"-1", randomText(32*1024)); err != nil {
			t.Fatalf("writing a transcript: %v", err)
		}
	}
}

// A template and everything it is made of are written in one
// transaction and reach the repository together.
//
// This began as the same question about a task: task_read sat in the
// churn tier on the strength of its name, so an ordinary sync exported
// the task within thirty seconds and its read scope up to an hour later,
// and a restore taken in between gave the task back with no read scope
// at all. Tasks are not in the repository any more, and that failure
// cannot be had that way -- but the shape of it survives exactly, one
// object over. A template is six tables (decl.go), a settings document
// carries all six, and a restore that half-remembered one would hand a
// deployment a template whose firings may read nothing, grant nothing
// and ask for none of the inputs it was written to ask for.
func TestATemplateReachesTheRepositoryWithEverythingItIsMadeOf(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}
	// A template configured after the seed, with one of everything, and
	// one ordinary sync.
	if err := store.PutTemplate(ctx, model.Template{
		ID: "tpl-1", Name: "nightly", Title: "Run the nightly sweep", Body: "as configured",
		Reads:     []model.RepoRef{{Owner: "owner", Name: "docs"}, {Owner: "owner", Name: "protos"}},
		Writes:    []model.WriteTarget{{Repo: model.RepoRef{Owner: "owner", Name: "payments-api"}}},
		Grants:    []model.Grant{{Capability: "githubtoken"}},
		Fields:    []model.TemplateField{{Name: "environment", Label: "Environment"}},
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("putting a template: %v", err)
	}
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing: %v", err)
	}

	// The restore: an empty database, and only what the sync wrote.
	other, otherDB := openDB(t)
	if err := staterepo.ImportTables(ctx, otherDB, dir, configTables()); err != nil {
		t.Fatalf("importing: %v", err)
	}
	got, err := other.GetTemplate(ctx, "tpl-1")
	if err != nil || got == nil {
		t.Fatalf("reading the restored template: %v %v", got, err)
	}
	if len(got.Reads) != 2 {
		t.Fatalf("the restored template has %d read repos, want 2: %+v", len(got.Reads), got.Reads)
	}
	if len(got.Writes) != 1 {
		t.Fatalf("the restored template has %d write targets, want 1: %+v", len(got.Writes), got.Writes)
	}
	if len(got.Grants) != 1 {
		t.Fatalf("the restored template has %d grants, want 1: %+v", len(got.Grants), got.Grants)
	}
	if len(got.Fields) != 1 {
		t.Fatalf("the restored template asks for %d inputs, want 1: %+v", len(got.Fields), got.Fields)
	}
}

// randomText is filler of about the right size and the right
// compressibility for a transcript: English-shaped rather than random
// bytes, since what it is standing in for is somebody's terminal.
func randomText(n int) string {
	words := []string{"the", "agent", "ran", "make", "test", "and", "it", "failed", "with",
		"an", "error", "in", "pkg", "model", "store", "so", "I", "changed", "the", "query"}
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		b.WriteString(words[i%len(words)])
		b.WriteByte(' ')
	}
	return b.String()
}

// columnValues reads one column out of a dumped table file, as a set.
func columnValues(t *testing.T, path, column string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	out := map[string]bool{}
	for _, row := range rows {
		if s, ok := row[column].(string); ok {
			out[s] = true
		}
	}
	return out
}
