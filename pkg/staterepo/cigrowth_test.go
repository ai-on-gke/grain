package staterepo_test

// What does grain's own CI cost the state repository?
//
// growth_test.go asks that question of task_run and task_observation.
// This file asks it of ci_run, ci_job and ci_step (grain/task-303),
// which are the same shape of thing -- grain's own record of what it did
// -- with one difference that matters: a step's output is a column, a
// workflow decides how many steps a run has, and the deployment does not
// decide either. task_run's cost per row is bounded by grain (a
// transcript and a prompt); ci_step's is bounded by two numbers chosen
// from first principles when the tables were added, and this file is
// what puts figures on them.
//
// Two measurements, because the question has two halves:
//
//   - TestCIRunStorageCost is cheap and needs no git. It writes runs of
//     several workflow shapes through the real StoredRunStore and
//     reports what one run costs the database and the dump, so "what is
//     in tables/ci_step.json at steady state" is one multiplication away
//     rather than an hour of simulation.
//   - TestCIStateRepoGrowth is the expensive one: a real database, a
//     real git repository, a real day of a deployment that runs CI on
//     every push, and what that day cost .git. It is gated on
//     GRAIN_CI_GROWTH_TICKS for growth_test.go's own reason -- a real
//     measurement belongs in a reviewer's hands rather than in CI.
//
// Both drive pkg/ci's production path (ci.NewStoredRunStore), not a
// reimplementation of it, so the bounds they measure are the bounds a
// deployment actually has: MaxStoredStepOutput per step,
// MaxStoredRunOutput per run, and DefaultStoredRunHistory runs kept.
//
// What neither of them asks about is the writes a run makes while it is
// still going -- a step publishing its output every few seconds. Those
// cost this repository nothing at all (the churn tier is written out on
// its own clock, whole file at a time, whatever has happened to the table
// since), and they cost SQLite something: pkg/ci's TestLiveOutputWriteCost
// is where that half is measured.

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/ci"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/staterepo"
)

// A workflow shape, as the bytes each of its steps prints: one slice per
// job, one entry per step, in the order they run. Written this way
// rather than as (jobs, steps, bytes) because the shape that matters is
// lopsided -- one step of one job prints a hundred times what the others
// do -- and an average would hide exactly the thing being measured.
type shape [][]int

// grainsOwnSuite is .grain/workflows/tests.yml as it actually runs on
// this repository, measured on 2026-09-08 by running each step's own
// script over this checkout and counting the bytes it printed:
//
//	make fmt                    0 B    (silent when there is nothing to reformat)
//	make frontend           1,483 B
//	go build ./...              0 B
//	go vet ./...                0 B
//	go test ./...           2,975 B    (an "ok <pkg>" line for each of 51 packages)
//	make fmt-frontend         201 B
//	npm test (in ui/)      52,208 B    (vitest names every file and every test it ran)
//
// Only one of the eight prints anything worth measuring, and it is the
// frontend's test runner rather than any of the Go steps. A green run of
// this suite stores 57KB across its eight steps -- under the 64KB *one*
// step is allowed. Grain running CI on itself is not what these bounds
// are for.
func grainsOwnSuite() shape {
	return shape{
		{0, 1483, 0, 0, 2975}, // go: fmt, frontend, build, vet, test
		{1483, 201, 52208},    // frontend: frontend, fmt-frontend, npm test
	}
}

// grainsOwnSuiteFailing is the same suite with the Go test step failing.
// A failing `go test -race ./...` prints its "ok" lines, the failing
// test's own output and a panic trace or a diff, which is what runs past
// the per-step bound and is the one output anybody ever goes looking
// for.
func grainsOwnSuiteFailing() shape {
	return shape{
		{0, 1483, 0, 0, 380 << 10},
		{1483, 201, 52208},
	}
}

// chattySuite is the shape today's bounds have to survive rather than
// the shape grain has: a repository whose workflows declare a dozen jobs
// of half a dozen steps, every one of them printing more than a step is
// allowed to store. It is not a straw man -- .github/workflows/tests.yml
// declares nine jobs on its own, a `make -x` or a verbose test runner
// prints megabytes, and nothing in grain stops a repository doing both.
func chattySuite(jobs, steps int) shape {
	s := make(shape, 0, jobs)
	for j := 0; j < jobs; j++ {
		job := make([]int, steps)
		for i := range job {
			job[i] = 2 << 20
		}
		s = append(s, job)
	}
	return s
}

// TestCIRunStorageCost reports what one run costs, per workflow shape:
// the bytes ci_step holds for it, and the bytes tables/ci_step.json
// grows by. Multiply by the history a deployment keeps and that is the
// file, which is the number the whole question turns on.
//
// Cheap enough to run on any build, and it asserts nothing -- it is a
// measurement, and pkg/ci's own tests are where the bounds are pinned.
// Run it with -v.
func TestCIRunStorageCost(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("measures storage cost; nothing to assert")
	}
	for _, c := range []struct {
		name  string
		shape shape
	}{
		{"grain's own suite, green", grainsOwnSuite()},
		{"grain's own suite, one failing step", grainsOwnSuiteFailing()},
		{"twelve jobs of six chatty steps", chattySuite(12, 6)},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			store, db := openDB(t)
			clock := &simClock{t: now}
			runs := ci.NewStoredRunStore(store, ci.StoredRunStoreOptions{Now: clock.now})
			sim := newCIRuns(t, runs)
			sim.clock = clock
			sim.put(sim.run(0, c.shape, true))

			dir := t.TempDir()
			if err := staterepo.Export(ctx, db, dir); err != nil {
				t.Fatalf("exporting: %v", err)
			}
			stored := ciStepBytes(t, ctx, db)
			file := fileSize(t, filepath.Join(dir, staterepo.TablesDir, "ci_step.json"))
			printed := 0
			steps := 0
			for _, job := range c.shape {
				for _, n := range job {
					printed += n
					steps++
				}
			}
			// And the same run with its output swept, which is what the
			// records on their own cost: the two bounds are independent
			// (grain/task-269), so the two numbers are what each of them
			// is actually spending.
			if _, err := db.ExecContext(ctx, "UPDATE `ci_step` SET `output` = ''"); err != nil {
				t.Fatalf("clearing output: %v", err)
			}
			recordsDir := t.TempDir()
			if err := staterepo.Export(ctx, db, recordsDir); err != nil {
				t.Fatalf("exporting the records alone: %v", err)
			}
			records := fileSize(t, filepath.Join(recordsDir, staterepo.TablesDir, "ci_step.json"))

			t.Logf("%d jobs, %d steps, %s printed", len(c.shape), steps, bytesHuman(int64(printed)))
			t.Logf("  stored in ci_step:     %s", bytesHuman(stored))
			t.Logf("  in ci_step.json:       %s (%s of it once the output is swept)",
				bytesHuman(file), bytesHuman(records))
			t.Logf("  the record bound:      %d runs × %s = %s of ci_step.json, rewritten whole on every churn commit",
				ci.DefaultStoredRunHistory, bytesHuman(records),
				bytesHuman(records*int64(ci.DefaultStoredRunHistory)))
			if stored > 0 {
				t.Logf("  the output ceiling:    %s holds %d runs of this shape",
					bytesHuman(ci.DefaultStoredRunOutputBytes), ci.DefaultStoredRunOutputBytes/stored)
			}
		})
	}
}

// ciStepBytes is what the `output` column holds across the whole table:
// the cost of the runs stored, before the dump's own JSON escaping.
func ciStepBytes(t *testing.T, ctx context.Context, db *sql.DB) int64 {
	t.Helper()
	var n sql.NullInt64
	if err := db.QueryRowContext(ctx, "SELECT SUM(LENGTH(`output`)) FROM `ci_step`").Scan(&n); err != nil {
		t.Fatalf("measuring ci_step: %v", err)
	}
	return n.Int64
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("measuring %s: %v", path, err)
	}
	return info.Size()
}

// ciWorkload is a deployment that runs CI, as the numbers a simulated
// day is built out of.
type ciWorkload struct {
	// runsPerDay is how many runs start: one per push of one branch, and
	// a deployment with agents on it pushes a great deal more often than
	// a human does.
	runsPerDay int
	// failureRate is the share of those runs with a failing step in them
	// -- the runs whose output is both larger and worth keeping.
	failureRate float64
	// green and red are the workflow shapes a run takes.
	green, red shape
	// history is how many finished runs the store keeps; zero means
	// ci.DefaultStoredRunHistory.
	history int
	// outputBytes is the ceiling on the step output kept across them;
	// zero means ci.DefaultStoredRunOutputBytes. Bytes rather than a
	// count of runs since grain/task-269, which is why the arms below
	// say "MiB of output" where they used to say "with output".
	outputBytes int64
}

// TestCIStateRepoGrowth runs a day of a deployment that runs CI on every
// push and reports what the day cost the repository.
//
// The deployment it runs on top of is a seeded one -- a few hundred
// tasks and their runs, so the dump is the size a real one is -- and
// nothing but CI moves during the day, so every byte the measurement
// reports is CI's. growth_test.go is the other half of the picture.
//
// What it said on 2026-09-08, on this build (three cores, and with
// grain/task-360's per-run bound in place):
//
//	grain's own CI, 300 runs a day, 500 kept and 500 with output
//	  24 commits, ci_step.json 35.4 MiB, task_run.json 10.7 MiB
//	  .git +31.2 MiB over the day, +1.6 MiB once packed (~49 MiB a month)
//	  Sync: 109ms per tick, 24.1s at worst -- the churn commit
//
//	grain's own CI, 300 runs a day, 500 kept and 100 with output
//	  24 commits, ci_step.json 8.4 MiB, task_run.json 10.7 MiB
//	  .git +7.5 MiB over the day, +965 KiB once packed (~28 MiB a month)
//	  Sync: 94ms per tick, 10.7s at worst
//
//	a chatty suite, 60 runs a day, 50 kept
//	  24 commits, ci_step.json 14.1 MiB (289KB a run: ×500 is 141 MiB)
//	  .git +4.2 MiB over the day, +1.3 MiB once packed
//	  Sync: 59ms per tick, 14.3s at worst
//
// The same chatty arm before the bound stored 4.5MB a run and did not
// finish a simulated day in thirty-five minutes: a churn commit was
// rewriting a quarter of a gigabyte of JSON, twenty-four times a day,
// forever. That is the finding, and the tail of Sync is the part of it
// that costs a deployment something other than disk.
func TestCIStateRepoGrowth(t *testing.T) {
	t.Parallel()
	v := os.Getenv("GRAIN_CI_GROWTH_TICKS")
	if v == "" {
		t.Skip("set GRAIN_CI_GROWTH_TICKS (2880 is a day) to measure what CI costs the state repository")
	}
	ticks, err := strconv.Atoi(v)
	if err != nil {
		t.Fatalf("GRAIN_CI_GROWTH_TICKS=%q is not a number: %v", v, err)
	}

	for _, c := range []struct {
		name string
		w    ciWorkload
	}{
		// What this deployment does now: grain's own suite, run on every
		// push of every branch an agent is working on.
		{"grain's own CI, 300 runs a day", ciWorkload{
			runsPerDay: 300, failureRate: 0.15, outputBytes: 512 << 20,
			green: grainsOwnSuite(), red: grainsOwnSuiteFailing(),
		}},
		// The split lifetime, which is what a deployment actually runs:
		// every run record remains, while the replaceable diagnostic
		// output beside them is held to a byte ceiling and swept oldest
		// first. 8MiB is what the old "newest 100 runs" came to for this
		// suite, so this arm is comparable with the measurement that
		// chose it (grain/task-360).
		{"grain's own CI, 300 runs a day, 8MiB of output", ciWorkload{
			runsPerDay: 300, failureRate: 0.15, outputBytes: 8 << 20,
			green: grainsOwnSuite(), red: grainsOwnSuiteFailing(),
		}},
		// The same day in a repository whose workflows print. Its history
		// is short because the point is the marginal cost of a run and not
		// how long a 2GB export takes: multiply by the history a
		// deployment keeps to get the file.
		{"a chatty suite, 60 runs a day, 50 kept", ciWorkload{
			runsPerDay: 60, failureRate: 0.15, history: 50,
			green: chattySuite(12, 6), red: chattySuite(12, 6),
		}},
	} {
		t.Run(c.name, func(t *testing.T) { measureCI(t, ticks, c.w) })
	}
}

func measureCI(t *testing.T, ticks int, w ciWorkload) {
	ctx := context.Background()
	store, db := openDB(t)
	// A deployment with history behind it, so the dump is the size a real
	// one is. It is seeded and then stands still: what moves during the
	// measured day is CI and nothing else.
	newSimulation(t, store, db, ciBaseDeployment())

	keepPolicy := ci.StoredRunRetention{History: w.history, OutputBytes: w.outputBytes}
	// The simulated clock, not the wall clock: these runs are stamped
	// months ago, and the sweep's age bound would otherwise clear every
	// log the simulation has just written and measure an empty table.
	clock := &simClock{t: now}
	runs := ci.NewStoredRunStore(store, ci.StoredRunStoreOptions{
		Retention: func() ci.StoredRunRetention { return keepPolicy },
		Now:       clock.now,
	})
	sim := newCIRuns(t, runs)
	sim.clock = clock
	sim.w = w
	// Fill the history before the clock starts, so the day is measured
	// from the steady state a deployment reaches in its first week rather
	// than from an empty table.
	keep := w.history
	if keep <= 0 {
		keep = ci.DefaultStoredRunHistory
	}
	for i := 0; i < keep; i++ {
		sim.put(sim.run(sim.next(), sim.shape(), true))
	}

	dir := t.TempDir()
	repo, err := staterepo.Open(ctx, staterepo.Config{
		Dir: dir, Now: sim.clock.now,
	})
	if err != nil {
		t.Fatalf("opening the repository: %v", err)
	}
	if err := staterepo.Seed(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	git(t, dir, "gc", "--quiet", "--prune=now")
	seeded := gitSize(t, dir)

	commits, spent, worst := 0, time.Duration(0), time.Duration(0)
	for tick := 0; tick < ticks; tick++ {
		sim.tick(tick)
		start := time.Now()
		changed, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion)
		took := time.Since(start)
		spent += took
		if took > worst {
			worst = took
		}
		if err != nil {
			t.Fatalf("syncing at tick %d: %v", tick, err)
		}
		if changed {
			commits++
		}
	}

	loose := gitSize(t, dir)
	git(t, dir, "gc", "--quiet", "--prune=now")
	packed := gitSize(t, dir)
	days := float64(ticks) / ticksPerDay

	outputKeep := w.outputBytes
	if outputKeep <= 0 {
		outputKeep = ci.DefaultStoredRunOutputBytes
	}
	t.Logf("%d ticks (%.2f days), %d runs a day, %d runs kept, %s of output kept",
		ticks, days, w.runsPerDay, keep, bytesHuman(outputKeep))
	t.Logf("  runs started:     %d", sim.started)
	t.Logf("  commits:          %d", commits)
	t.Logf("  .git seeded:      %s (packed)", bytesHuman(seeded))
	t.Logf("  .git grew by:     %s as the day ran, %s once packed",
		bytesHuman(loose-seeded), bytesHuman(packed-seeded))
	if days > 0 {
		t.Logf("  a month of that:  %s packed", bytesHuman(int64(float64(packed-seeded)*30/days)))
	}
	t.Logf("  time in Sync:     %s total, %s per tick, %s at worst",
		spent.Round(time.Second), (spent / time.Duration(ticks)).Round(time.Millisecond),
		worst.Round(time.Millisecond))
	for _, l := range tableSizes(t, dir) {
		t.Logf("  %s", l)
	}
	for _, l := range churnReport(t, dir) {
		t.Logf("  %s", l)
	}
}

// ciBaseDeployment is the deployment CI runs on top of: enough tasks and
// finished runs that the dump is a real one, and no more, because every
// tick of this measurement exports all of it and none of it changes.
func ciBaseDeployment() workload {
	w := busyDeployment()
	w.seedTasks = 150
	return w
}

// tableSizes reports the dump file by file, largest first. It is how the
// question "is ci_step the largest thing in the repository?" is answered
// with a number rather than with an intuition.
func tableSizes(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, staterepo.TablesDir))
	if err != nil {
		t.Fatalf("reading the dump: %v", err)
	}
	type f struct {
		name string
		size int64
	}
	var files []f
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatalf("measuring %s: %v", e.Name(), err)
		}
		files = append(files, f{e.Name(), info.Size()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].size > files[j].size })
	var out []string
	for i, file := range files {
		if i == 5 {
			break
		}
		out = append(out, fmt.Sprintf("%-40s %10s", "tables/"+file.name, bytesHuman(file.size)))
	}
	return out
}

// ciRuns drives a StoredRunStore the way a daemon running CI does: a run
// is written when it starts, again as each of its jobs finishes, and
// once more when it concludes.
type ciRuns struct {
	t     *testing.T
	store *ci.StoredRunStore
	w     ciWorkload

	rnd     *rand.Rand
	clock   *simClock
	live    []liveCIRun
	seq     int
	started int
}

type liveCIRun struct {
	run ci.Run
	// done is the tick each job finishes on, in order.
	at []int
}

func newCIRuns(t *testing.T, store *ci.StoredRunStore) *ciRuns {
	return &ciRuns{t: t, store: store, rnd: rand.New(rand.NewSource(2)), clock: &simClock{t: now}}
}

func (c *ciRuns) next() int { c.seq++; return c.seq }

// shape picks the workflow shape this run takes: a failing run at the
// workload's failure rate, a green one otherwise.
func (c *ciRuns) shape() shape {
	if c.rnd.Float64() < c.w.failureRate {
		return c.w.red
	}
	return c.w.green
}

// instant is a timestamp as a ci.Run carries one: they are pointers, so
// that a run or a job with no such time has no field rather than an
// instant in the year 1.
func instant(t time.Time) *time.Time { return &t }

// run builds one complete run of this shape. done says whether it has
// concluded -- a seeded run has, a run just started has not.
func (c *ciRuns) run(seq int, s shape, done bool) ci.Run {
	id := fmt.Sprintf("%016x", seq*2654435761)
	at := c.clock.t
	run := ci.Run{
		ID: id, Key: ci.Key(id + "-key"),
		Repo: "bwsalmon/grain", Ref: fmt.Sprintf("refs/heads/grain/task-%d", 300+seq%200),
		Commit: fmt.Sprintf("%040x", seq*2246822519), Event: "push",
		RequestedBy: "grain", StartedAt: &at,
	}
	for j, job := range s {
		result := ci.JobRun{
			Workflow: "tests", WorkflowPath: ".grain/workflows/tests.yml",
			JobID: fmt.Sprintf("job-%d", j), Name: fmt.Sprintf("job %d", j),
			Key: ci.Key(fmt.Sprintf("%s-%d", id, j)), Sandbox: "grain-ci-" + id,
			StartedAt: &at, FinishedAt: instant(at.Add(9 * time.Minute)),
		}
		for i, n := range job {
			result.Steps = append(result.Steps, ci.StepRun{
				Name: fmt.Sprintf("step %d", i), ExitCode: 0,
				Output:    ciText(c.rnd, n),
				StartedAt: &at, FinishedAt: instant(at.Add(time.Minute)),
			})
		}
		if done {
			concludeJob(&result)
		}
		run.Jobs = append(run.Jobs, result)
	}
	if done {
		run.FinishedAt = instant(at.Add(11 * time.Minute))
		run.Conclusion = ci.ConclusionSuccess
		for _, job := range run.Jobs {
			if job.Conclusion == ci.ConclusionFailure {
				run.Conclusion = ci.ConclusionFailure
			}
		}
	}
	return run
}

// concludeJob finishes a job the way the engine would: every step
// succeeded unless it printed past what a step is allowed to store, and
// a step that printed that much is the one that failed. That is what a
// red run looks like -- the failure is at the end of the longest output
// in the run -- and it is what decides which output is worth room when
// there is not room for all of it.
func concludeJob(job *ci.JobRun) {
	job.Conclusion = ci.ConclusionSuccess
	for i := range job.Steps {
		job.Steps[i].Conclusion = ci.ConclusionSuccess
		if len(job.Steps[i].Output) > ci.MaxStoredStepOutput {
			job.Steps[i].Conclusion = ci.ConclusionFailure
			job.Steps[i].ExitCode = 1
			job.Conclusion = ci.ConclusionFailure
		}
	}
}

func (c *ciRuns) put(run ci.Run) { c.store.Put(run) }

// tick advances the simulated deployment by one 30s cycle: runs start on
// pushes, their jobs finish on their own schedule, and each of those is
// a write the daemon really makes.
func (c *ciRuns) tick(tick int) {
	c.clock.t = c.clock.t.Add(tickInterval)

	var still []liveCIRun
	for _, live := range c.live {
		finished := 0
		for i, at := range live.at {
			if at > tick {
				break
			}
			finished = i + 1
		}
		if finished == 0 {
			still = append(still, live)
			continue
		}
		// Each job that finished is written as it finishes: the run's row
		// and every one of its steps' rows, rewritten whole (PutCIRun).
		for i := 0; i < finished; i++ {
			if live.run.Jobs[i].Conclusion != "" {
				continue
			}
			concludeJob(&live.run.Jobs[i])
			c.put(live.run)
		}
		if finished < len(live.at) {
			still = append(still, live)
			continue
		}
		live.run.FinishedAt = instant(c.clock.t)
		live.run.Conclusion = ci.ConclusionSuccess
		for _, job := range live.run.Jobs {
			if job.Conclusion == ci.ConclusionFailure {
				live.run.Conclusion = ci.ConclusionFailure
			}
		}
		c.put(live.run)
	}
	c.live = still

	if due(tick, c.w.runsPerDay) {
		run := c.run(c.next(), c.shape(), false)
		c.put(run)
		c.started++
		// Jobs run in parallel and finish minutes apart: 18 ticks is nine
		// minutes, which is what a green run of grain's own suite takes.
		at := make([]int, len(run.Jobs))
		for i := range at {
			at[i] = tick + 6 + i*2
		}
		c.live = append(c.live, liveCIRun{run: run, at: at})
	}
}

// ciText is filler of roughly the right size and roughly the right
// compressibility: log-shaped lines rather than random bytes, because
// git's own packing is what a pile of random blobs would misrepresent.
func ciText(rnd *rand.Rand, n int) string {
	if n == 0 {
		return ""
	}
	lines := []string{
		"=== RUN   TestExportWritesAFileForEveryTableTheSchemaHas",
		"--- PASS: TestSyncCommitsOnlyWhatChanged (0.04s)",
		"ok  \tgithub.com/bwsalmon/grain/pkg/staterepo\t2.418s",
		"go: downloading github.com/mattn/go-sqlite3 v1.14.22",
		"  vite v5.4.2 building for production...",
		"    at Object.<anonymous> (/home/debian/work/ui/src/app.test.js:41:19)",
	}
	var b strings.Builder
	for b.Len() < n {
		b.WriteString(lines[rnd.Intn(len(lines))])
		b.WriteByte('\n')
	}
	return b.String()[:n]
}
