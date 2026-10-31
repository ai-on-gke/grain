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

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/workflows"
)

// DefaultStoredRunHistory is how many finished runs a StoredRunStore
// keeps: the record of one, which is its id, ref, commit and every job's
// and step's verdict, with none of the text those steps printed.
//
// A count rather than an age, because a count is what bounds the cost --
// of the table, and of the state repository the table is exported into
// -- and an age would delete a quiet deployment's history for no
// benefit, since a deployment that runs a run a week is not the one
// whose storage anybody is worried about.
//
// Five thousand rather than the five hundred it was until
// grain/task-269. Five hundred was chosen against "weeks of a busy
// deployment"; the deployment grain runs on itself turned out to start a
// run every few minutes across several branches, some three hundred a
// day, which made it a day and a half -- less than the time its own
// queue takes to dispatch the task that wants to read one. A record is
// the cheap half of a run (measured by staterepo.TestCIRunStorageCost:
// 2.1KiB of dump per run of grain's own two-job suite, against 62KiB
// once its output is in there), so this is the bound that can afford to
// be generous: five thousand is a fortnight of that deployment and years
// of a quiet one, for 10.5MiB of tables/ci_step.json.
//
// It is the default rather than the policy: model.Config.CIRunHistory
// stores a deployment's own answer, and StoredRunStoreOptions.Retention
// is read afresh on every sweep, so raising it is a Settings change
// rather than a restart.
const DefaultStoredRunHistory = 5000

// DefaultStoredRunOutputBytes is how much step output a deployment keeps
// across its whole history -- the bulky half, and the only half that
// makes a red run diagnosable rather than merely countable.
//
// Bytes rather than a count of runs, which is what this was until
// grain/task-269 ("the newest 100 runs"). A run's output is anything
// between nothing and MaxStoredRunOutput depending on what its steps
// printed, so a count of runs bounds disk only if you already know how
// chatty this month's workflows are. The -ci-cache-max-* and
// -ci-artifact-max-* groups next door answer the same question the same
// way, and this now reads like them: a ceiling, an age, a per-entry cap,
// and a sweep of the oldest until what is left fits.
//
// 64MiB, against the 25MiB the count it replaces worked out to.
// Measured by staterepo.TestCIRunStorageCost: a green run of grain's own
// suite stores 57KiB and one with a failing step 118KiB, so the ceiling
// holds 1,150 of the former or 550 of the latter -- a thousand runs at
// this deployment's own mix, which at the 300 runs a day it starts is
// three days and a bit. A failure from Friday afternoon is still
// readable on Monday morning, which is the question this bound is
// actually asked.
//
// The cost of it is disk and the store, and no longer the state
// repository: ci_step used to be exported there on an hourly clock, a
// whole file per commit, which made this ceiling also the JSON that
// commit rewrote 24 times a day (35MiB of it cost 24s in the worst tick
// TestCIStateRepoGrowth measured). Since grain/task-395 the repository
// carries the configuration only and none of this reaches it. A
// deployment that finds this tail expensive lowers it in Settings; one
// that investigates its own failures a week later raises it. Neither
// needs a restart, which is the point.
const DefaultStoredRunOutputBytes int64 = 64 << 20

// DefaultStoredRunOutputAge is how long any run's output may live,
// whatever room there is under the ceiling. A fortnight, which is
// DefaultArtifactMaxAge for the same reason: an artifact and a step's
// log are both evidence about a failure somebody is investigating now,
// and neither goes wrong with time so much as it stops being something
// anybody is going to open. The run's record outlives it either way.
const DefaultStoredRunOutputAge = 14 * 24 * time.Hour

// StoredRunRetention is how long a deployment's CI history lives. Two
// bounds, deliberately independent of each other, because they cost
// different things (grain/task-269):
//
//   - History, the run records. Small, and what "was this job red last
//     week too?" is answered from.
//   - OutputBytes, OutputAge and RunOutputBytes, the step output beside
//     them. Expensive, and what makes a red run diagnosable.
//
// A record therefore outlives its own output, which is the property the
// split is for: a run whose log has been swept still answers "what was
// the verdict", and says so in place of the log (droppedOutputNote and
// declinedOutputNote).
//
// Every field has three readings. A positive number is the bound. Zero
// means "grain's own", which is the Default* constant above -- so a
// deployment that has never chosen, and a column added by a migration
// that reads back zero, both get the default. A negative number means no
// bound at all: keep every run, or every byte of output, forever. That
// last one is an operator's decision about their own disk, and it is
// spelled -1 rather than 0 precisely so that it cannot be arrived at by
// a column nobody has written yet.
type StoredRunRetention struct {
	// History is how many concluded runs keep their record.
	History int
	// OutputBytes is the ceiling on stored step output across every run
	// that has one. The sweep drops whole runs' output, oldest first,
	// until what is left fits.
	OutputBytes int64
	// OutputAge is how old a run's output may be before it is swept
	// whatever room there is under OutputBytes.
	OutputAge time.Duration
	// RunOutputBytes is the per-entry cap: how much output one run may
	// store, spent on the steps a reader would open first
	// (boundRunOutput). Zero means MaxStoredRunOutput.
	//
	// A run whose output would be larger than the whole of OutputBytes
	// is declined rather than stored -- see declineRunOutput, and
	// StoredRunStore.put, which says so once per run. Declining is the
	// alternative to a single run evicting every older run's output on
	// the way in, which is the failure mode a byte ceiling has and a
	// count of runs does not.
	RunOutputBytes int64
}

// DefaultStoredRunRetention is the policy a deployment that has said
// nothing runs with.
func DefaultStoredRunRetention() StoredRunRetention {
	return StoredRunRetention{}.withDefaults()
}

// StoredRunRetentionFrom reads the four stored settings as this package
// reads them: the units applied (MiB, days, KiB) and nothing else
// resolved, so a 0 travels through as "grain's own" and a negative as
// "no bound at all".
//
// One definition of that arithmetic, because two consumers now hold it
// against the same number: the sweeper, which applies the ceiling, and
// the Storage section, which shows what is stored underneath it
// (grain/task-290). A pane whose MiB were shifted somewhere else would
// be free to disagree with the bound actually in force.
func StoredRunRetentionFrom(cfg model.Config) StoredRunRetention {
	keep := StoredRunRetention{History: cfg.CIRunHistory}
	if cfg.CIStepOutputMB != 0 {
		keep.OutputBytes = int64(cfg.CIStepOutputMB) << 20
	}
	if cfg.CIStepOutputDays != 0 {
		keep.OutputAge = time.Duration(cfg.CIStepOutputDays) * 24 * time.Hour
	}
	if cfg.CIRunOutputKB != 0 {
		keep.RunOutputBytes = int64(cfg.CIRunOutputKB) << 10
	}
	return keep
}

// Resolved is this policy with every "grain's own default" reading
// replaced by the number in force, which is what a reader is shown and
// what every bound here is applied as. A non-positive field that comes
// back out of it is a bound an operator has switched off.
func (r StoredRunRetention) Resolved() StoredRunRetention { return r.withDefaults() }

// withDefaults resolves the three readings above into numbers this
// package can use: what is left is either a positive bound or a
// non-positive "no bound", which is what every consumer of it tests for.
func (r StoredRunRetention) withDefaults() StoredRunRetention {
	if r.History == 0 {
		r.History = DefaultStoredRunHistory
	}
	if r.OutputBytes == 0 {
		r.OutputBytes = DefaultStoredRunOutputBytes
	}
	if r.OutputAge == 0 {
		r.OutputAge = DefaultStoredRunOutputAge
	}
	if r.RunOutputBytes == 0 {
		r.RunOutputBytes = MaxStoredRunOutput
	}
	return r
}

// Describe is this policy as a sentence, for the line a daemon logs at
// startup and for anything else that has to tell an operator how long
// their evidence lives. Resolved rather than as configured: a zero field
// prints the number actually in force, which is the question being
// asked.
func (r StoredRunRetention) Describe() string {
	keep := r.withDefaults()
	runs := "every run's record"
	if keep.History > 0 {
		runs = strconv.Itoa(keep.History) + " runs' records"
	}
	output := "every byte of their step output"
	if keep.OutputBytes > 0 {
		output = humanBytes(keep.OutputBytes) + " of their step output (up to " +
			humanBytes(keep.RunOutputBytes) + " of it from any one run)"
	}
	age := ""
	if keep.OutputAge > 0 {
		age = ", none of it older than " + strconv.Itoa(int(keep.OutputAge/(24*time.Hour))) + " days"
	}
	return "this deployment keeps " + runs + " and " + output + age
}

// MaxListedRuns bounds what RunStore.List answers with, which is not the
// same question as how much history a deployment keeps.
//
// The listing is a poll: the CI pane asks for it every few seconds and
// draws a table out of it, and list_ci_runs reads the same route. A run
// costs a few kilobytes of JSON there even with every step's output left
// out (ui.ciListSummary), so serving DefaultStoredRunHistory of them
// would be tens of megabytes every five seconds to draw a page of rows.
//
// Five hundred is what this deployment's history was before
// grain/task-269 raised it, so the pane behaves exactly as it did.
// Everything older is still stored and still answers by id -- `grain ci
// show`, get_ci_log, and the task waiting on a run all read one run
// rather than the list.
//
// A *filtered* listing is no longer held to it, which it was until
// grain/task-289: the filter used to be applied in Go to whatever this
// returned, so "was this job red on another branch last week" searched
// the newest five hundred runs whatever the deployment kept. RunStore.
// Find takes the filter instead and answers it over the whole stored
// history, with a limit of its own (MaxFoundRuns) -- this bound is what
// an *unnarrowed* listing gets, which is the poll it was always about.
const MaxListedRuns = 500

// StoredRunStore is RunStore over model.Store: the same five methods,
// backed by the `ci_run`, `ci_job` and `ci_step` tables, so a run
// outlives the process that ran it.
//
// It is what a daemon uses (cmd/grain's buildCIService), and what makes
// `grain ci show` answer for a run from last week, the CI page keep the
// jobs of a run that was in flight when the daemon restarted, and
// orchestrator.collectCIRun find the run a task is waiting on instead of
// starting a second one for the same commit.
//
// # Reads go to the database every time
//
// There is no cache in front of this, on purpose. A run is read by a
// human opening a page, and by Update: once per punctuation mark of the
// run, and once per live publication of every step still printing, which
// is the traffic this store actually has. Against a local SQLite file
// that is nothing -- one indexed read of one run -- and a cache would be
// a second copy to keep honest for no measured gain. If a deployment ever
// finds this hot, the fix is a cache here rather than a different seam.
//
// # Failures degrade rather than propagate
//
// RunStore's methods carry no error (its own doc comment says why), so a
// database that cannot be read answers "no such run" and one that cannot
// be written loses the record. Both are logged. That is the same
// behaviour a caller already had to handle from the in-memory store,
// which lost every run on restart -- so a store that is having a bad day
// is no worse than the one this replaces, and a store that is fine is a
// great deal better.
type StoredRunStore struct {
	store   *model.Store
	keep    func() StoredRunRetention
	now     func() time.Time
	timeout time.Duration
	log     func(format string, args ...any)

	// mu makes Update a read-modify-write rather than two racing
	// statements. One process writes a given run (the daemon running it),
	// so a mutex is the whole of the concurrency control this needs; the
	// database's own transactions cover the rest.
	mu sync.Mutex
}

// StoredRunStoreOptions is what a deployment may say about how its runs
// are kept. Every field has a working default, so the zero value is the
// answer for anything that has no opinion.
type StoredRunStoreOptions struct {
	// Retention is this deployment's CI history policy
	// (StoredRunRetention). nil, and a zero StoredRunRetention, both mean
	// grain's own defaults.
	//
	// A function rather than a value because the policy is a stored
	// setting a daemon re-reads (model.Config.CIRunHistory and its three
	// neighbours, cmd/grain's liveConfig): this is called on every sweep
	// and every listing, so a retention widened in the Settings pane
	// binds the next run that concludes rather than the next process.
	// Narrowing one is the same operation and needs no more care -- the
	// next sweep is simply a larger one.
	Retention func() StoredRunRetention
	// Now is the clock the output sweep's age bound is measured against;
	// nil means time.Now. A daemon has no use for it, and anything
	// simulating a deployment does: a run stamped with a simulated clock
	// is months old by the wall clock, and a sweep reading the wall clock
	// would clear every log the simulation had just written
	// (staterepo's own TestCIStateRepoGrowth, which is where that was
	// noticed).
	Now func() time.Time
	// Timeout bounds one storage call, so a database that has stopped
	// answering cannot wedge an HTTP handler or a reconcile cycle. Zero
	// means DefaultStoredRunTimeout.
	Timeout time.Duration
	// Log is where a failed read or write is reported; nil discards,
	// which is what a test wants and no daemon does.
	Log func(format string, args ...any)
}

// DefaultStoredRunTimeout bounds one call into the store. Generous for
// a local database and short enough that nothing waits on a broken one:
// the caller behind it is an HTTP handler or a reconcile cycle, and both
// would rather be told "no such run" than hang.
const DefaultStoredRunTimeout = 10 * time.Second

// NewStoredRunStore returns a RunStore backed by store.
//
// It does not recover interrupted runs by itself -- see
// RecoverUnattachable, which a daemon calls once at startup and which is
// separate precisely because it is a decision about a *previous*
// process's runs, not something a constructor should be doing behind a
// caller's back.
func NewStoredRunStore(store *model.Store, opts StoredRunStoreOptions) *StoredRunStore {
	s := &StoredRunStore{
		store:   store,
		keep:    opts.Retention,
		now:     opts.Now,
		timeout: opts.Timeout,
		log:     opts.Log,
	}
	if s.now == nil {
		s.now = func() time.Time { return time.Now().UTC() }
	}
	if s.keep == nil {
		s.keep = DefaultStoredRunRetention
	}
	if s.timeout <= 0 {
		s.timeout = DefaultStoredRunTimeout
	}
	return s
}

// retention is the policy as it stands now, with every unset field
// resolved. Read per call rather than held, which is the whole of what
// makes a change in the Settings pane reach a running daemon.
func (s *StoredRunStore) retention() StoredRunRetention {
	return s.keep().withDefaults()
}

func (s *StoredRunStore) ctx() (context.Context, context.CancelFunc) {
	// Background rather than a caller's: a write recording that a job
	// finished must not be cancelled because whoever asked for the run
	// has since closed their browser.
	return context.WithTimeout(context.Background(), s.timeout)
}

// bound is ctx, cut to the store's own timeout.
func (s *StoredRunStore) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.timeout)
}

func (s *StoredRunStore) logf(format string, args ...any) {
	if s.log != nil {
		s.log(format, args...)
	}
}

// Put records a run, replacing whatever was stored under the same id,
// and prunes the history once a run has concluded.
//
// Pruning here rather than on a timer: a run finishing is the only thing
// that can push the history over its bounds, so it is the only moment
// worth checking, and it keeps the policy where the numbers are.
func (s *StoredRunStore) Put(run Run) {
	keep := s.retention()
	stored, declined := storedRun(run, keep)
	s.put(stored, keep, declined)
}

// put is Put with the conversion already done, so that Update can decide
// what kind of write it is making from the rows rather than from the Run
// -- and, having converted, does not convert again. keep is passed in
// rather than read again for the same reason: the run was bounded
// against one policy, and the sweep that follows has to be the same one.
func (s *StoredRunStore) put(run model.CIRun, keep StoredRunRetention, declined bool) {
	ctx, cancel := s.ctx()
	defer cancel()
	if err := s.store.PutCIRun(ctx, run); err != nil {
		s.logf("ci: recording run %s: %v -- this run will not survive a restart", run.ID, err)
		return
	}
	if !run.Done() {
		return
	}
	if declined {
		s.logf("ci: run %s printed more than the %s this deployment keeps output within, so none of its output was "+
			"stored -- the steps say so in place of their logs, and the runs behind it kept theirs "+
			"(raise the CI step output ceiling in Settings, or have the run's steps print less)",
			run.ID, humanBytes(keep.OutputBytes))
	}
	if keep.History > 0 {
		if deleted, err := s.store.PruneCIRuns(ctx, keep.History); err != nil {
			s.logf("ci: pruning stored runs past %d: %v", keep.History, err)
		} else if deleted > 0 {
			s.logf("ci: forgot %d run(s) past the %d this deployment keeps", deleted, keep.History)
		}
	}
	s.sweepOutput(ctx, keep)
}

// sweepOutput drops whole runs' step output, oldest first, until what is
// left is within the ceiling and none of it is past the age -- the runs,
// their jobs, their steps and every verdict on them stay exactly where
// they are.
//
// Whole runs rather than whole steps, because half a run's log answers
// nothing: a reader opening a failure wants the step that failed and the
// step before it, and a sweep that took one and left the other would
// leave a run that looks diagnosable and is not.
func (s *StoredRunStore) sweepOutput(ctx context.Context, keep StoredRunRetention) {
	bounds := model.CIStepOutputBounds{MaxBytes: keep.OutputBytes}
	if keep.OutputAge > 0 {
		bounds.Before = s.now().Add(-keep.OutputAge)
	}
	if bounds.MaxBytes <= 0 && bounds.Before.IsZero() {
		return // an operator who has asked for no bound at all
	}
	swept, err := s.store.PruneCIStepOutput(ctx, bounds)
	if err != nil {
		s.logf("ci: sweeping stored step output: %v", err)
		return
	}
	if swept.Runs == 0 {
		return
	}
	s.logf("ci: cleared %s of output from %d step(s) of %d run(s) -- their records stand, and %s of output is kept "+
		"across the runs that still have it (the ceiling is %s, the age %s)",
		humanBytes(swept.Bytes), swept.Steps, swept.Runs, humanBytes(swept.Kept),
		humanBytes(keep.OutputBytes), keep.OutputAge)
}

// Update applies fn to the run with this id, if it is still stored.
//
// # The live log's write
//
// Most of what reaches this is a run's punctuation -- a job got a
// machine, a step started, a job finished -- and each of those is a
// whole-run write, which is what keeps a run's rows agreeing with each
// other (model.PutCIRun says why). One thing that reaches it is not
// punctuation at all: a step that is still running publishing what it has
// printed so far, every ci.LiveOutputInterval, for as long as it prints.
// That one changes a single `output` cell and costs a transaction
// carrying every step's output in the run.
//
// So this looks at what the write would actually change. Both versions of
// the run are converted to rows -- which is also where each step's output
// is cut to what a stored run keeps, so the comparison is of the columns
// and not of what the engine happened to hand over -- and if a single
// step's output is the whole of the difference, one UPDATE goes out
// instead (model.CIStepOutputChange, model.Store.PutCIStepOutput).
// Anything else is the write it always was.
//
// The narrow write is taken only where it leaves behind exactly the rows
// the whole-run write would have, so nothing downstream can tell which
// one happened. Two things follow from that, and both are deliberate:
// only a run still in flight is eligible, since concluding is also the
// moment history is pruned and that belongs with the write that concludes
// it; and a publication that moves what the *other* steps keep is written
// whole, which is how a run past MaxStoredRunOutput takes care of itself
// -- there a step's share of the budget is taken out of theirs, several
// columns move, and one UPDATE stops being the same write.
func (s *StoredRunStore) Update(id string, fn func(*Run)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.Get(id)
	if !ok {
		return
	}
	updated := cloneRun(run)
	fn(&updated)
	keep := s.retention()
	before, _ := storedRun(run, keep)
	after, declined := storedRun(updated, keep)
	if !before.Done() && !after.Done() {
		if job, step, ok := model.CIStepOutputChange(before, after); ok {
			s.putStepOutput(after, job, step)
			return
		}
	}
	s.put(after, keep, declined)
}

// putStepOutput writes the one column a live publication changes.
//
// A failure is logged and left there rather than retried as a whole-run
// write. What is lost is one tick of a log that is about to be published
// again five seconds later, and rewritten in full by the step's own
// result when it finishes; a store that is failing has an operator's
// problem, and the run's record does not depend on this write landing.
func (s *StoredRunStore) putStepOutput(run model.CIRun, job, step int) {
	ctx, cancel := s.ctx()
	defer cancel()
	if err := s.store.PutCIStepOutput(ctx, run.ID, job, step, run.Jobs[job].Steps[step].Output); err != nil {
		s.logf("ci: recording live output of run %s: %v -- this step's log will catch up at the next publication", run.ID, err)
	}
}

// Get returns one run entire, output and all.
func (s *StoredRunStore) Get(id string) (Run, bool) {
	ctx, cancel := s.ctx()
	defer cancel()
	stored, err := s.store.GetCIRun(ctx, id)
	if err != nil {
		s.logf("ci: reading run %s: %v", id, err)
		return Run{}, false
	}
	if stored == nil {
		if runID, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64); err == nil && runID > 0 {
			ghJobs, err := s.store.ListGitHubCIJobs(ctx, model.GitHubCIFilter{RunID: runID})
			if err == nil && len(ghJobs) > 0 {
				return GitHubCIRun(ghJobs)
			}
		}
		return Run{}, false
	}
	return runOf(*stored), true
}

// List returns the runs stored, newest first, without any step's output
// -- RunStore.List's own contract, and here it is not merely dropped on
// the way out but never read off disk.
func (s *StoredRunStore) List() []Run {
	ctx, cancel := s.ctx()
	defer cancel()
	stored, err := s.store.ListCIRuns(ctx, s.listed())
	if err != nil {
		s.logf("ci: listing runs: %v", err)
		return nil
	}
	out := make([]Run, 0, len(stored))
	for _, r := range stored {
		out = append(out, runOf(r))
	}
	return out
}

// listed is how many runs one listing answers with: MaxListedRuns, or
// the whole history where a deployment keeps fewer than that, so a
// deployment that has chosen a short history still sees all of it.
func (s *StoredRunStore) listed() int {
	keep := s.retention()
	if keep.History > 0 && keep.History < MaxListedRuns {
		return keep.History
	}
	return MaxListedRuns
}

// Find answers a filter over every run this deployment has stored,
// rather than over the newest page of them -- which is the whole point
// of keeping a fortnight of history (grain/task-289).
//
// # Two passes, and why the order is not in the SQL
//
// The narrowing is SQL (model.CIRunQuery): repo, ref, conclusion, job
// and since are columns and rows of `ci_job`, so the database decides
// which runs match, however many are stored. What comes back from that
// pass is an id and two timestamps per matching run -- small enough to
// hold every match -- and the *order* is then pkg/ci's own comparator
// (SortRuns) over those stamps, after which only the page asked for is
// read back in full, jobs and all.
//
// The order is here rather than in an ORDER BY for two reasons. One is
// that it would be a second implementation of it: three sort keys, each
// with a rule about a missing timestamp and a stability guarantee that
// a reader polling every five seconds can feel (SortRuns' own comment),
// and the in-memory store, the browser and this one all have to agree
// about them. The other is that one of the three cannot be written in
// SQL here at all -- a duration is the difference between two DATETIME
// columns whose stored text SQLite's own date functions do not parse
// (they are written by the driver as Go renders a time), so julianday()
// over them is NULL. Doing all three the same way beats doing two in
// SQL and one somewhere else.
//
// A failed read answers with an empty page and says so in the log, like
// every other method here (the type's own doc comment on why).
func (s *StoredRunStore) Find(f RunFilter, limit int) RunPage {
	ctx, cancel := s.ctx()
	defer cancel()
	stamps, err := s.store.MatchCIRuns(ctx, ciRunQueryOf(f))
	if err != nil {
		s.logf("ci: matching runs (%s): %v", f.Describe(), err)
		return RunPage{}
	}
	stored, err := s.store.CountCIRuns(ctx)
	if err != nil {
		// The count is the sentence above the answer rather than the
		// answer, so a failure here costs that sentence its precision
		// and nothing else: what matched is at least as much as is
		// stored, which is true and is the honest floor.
		s.logf("ci: counting stored runs: %v", err)
		stored = len(stamps)
	}
	if f.Visible != nil {
		// What the reader may not see is neither matched nor stored as
		// far as they are told. Stored is narrowed the same way so that
		// "N of M" does not count another team's runs in M; that costs a
		// second pass over every stamp, which is the price of a bound
		// SQL cannot be asked.
		visible := stamps[:0]
		for _, stamp := range stamps {
			if f.SeesRepo(stamp.Repo) {
				visible = append(visible, stamp)
			}
		}
		stamps = visible
		if f.Narrowed() {
			all, err := s.store.MatchCIRuns(ctx, model.CIRunQuery{})
			if err != nil {
				s.logf("ci: counting visible stored runs: %v", err)
				stored = len(stamps)
			} else {
				stored = 0
				for _, stamp := range all {
					if f.SeesRepo(stamp.Repo) {
						stored++
					}
				}
			}
		} else {
			stored = len(stamps)
		}
	}
	page := RunPage{Matched: len(stamps), Stored: stored}
	ordered := boundRuns(SortRuns(runsOfStamps(stamps), f, s.now()), limit)
	if len(ordered) == 0 {
		return page
	}
	ids := make([]string, 0, len(ordered))
	for _, run := range ordered {
		ids = append(ids, run.ID)
	}
	rows, err := s.store.ListCIRunsByID(ctx, ids)
	if err != nil {
		s.logf("ci: reading the %d run(s) matching %s: %v", len(ids), f.Describe(), err)
		return RunPage{}
	}
	page.Runs = make([]Run, 0, len(rows))
	for _, row := range rows {
		page.Runs = append(page.Runs, runOf(row))
	}
	return page
}

// ciRunQueryOf is a reader's filter as the store's own question. The
// state vocabulary is the one thing that has to be translated rather
// than copied: "running" and "pending" are not conclusions but facts
// about a run's jobs, and model does not know pkg/ci's words for them
// (RunStateOf, model.CIRunQuery).
//
// A word that is neither a conclusion nor one of those two is passed on
// as a conclusion, where it matches nothing -- exactly what it does in
// memory, and one fewer way for the two stores to disagree. Nothing
// should send one: both readers parse the vocabulary first
// (ParseRunState).
func ciRunQueryOf(f RunFilter) model.CIRunQuery {
	q := model.CIRunQuery{Repo: f.Repo, Ref: f.Ref, Job: f.Job, Since: f.Since, Until: f.Until}
	for _, state := range f.States {
		switch {
		case strings.EqualFold(state, StateRunning):
			q.Running = true
		case strings.EqualFold(state, StatePending):
			q.Pending = true
		default:
			q.Conclusions = append(q.Conclusions, state)
		}
	}
	return q
}

// runsOfStamps is the narrowing pass's answer as the runs SortRuns
// orders: an id and the two instants every one of the three orders is a
// function of, and nothing else, since nothing else has been read yet.
func runsOfStamps(stamps []model.CIRunStamp) []Run {
	out := make([]Run, 0, len(stamps))
	for _, stamp := range stamps {
		out = append(out, Run{
			ID:         stamp.ID,
			StartedAt:  timePtr(stamp.StartedAt),
			FinishedAt: timePtr(stamp.FinishedAt),
		})
	}
	return out
}

// Live returns a run with this key that has not concluded, if there is
// one.
func (s *StoredRunStore) Live(key Key) (Run, bool) {
	ctx, cancel := s.ctx()
	defer cancel()
	stored, err := s.store.LiveCIRun(ctx, string(key))
	if err != nil {
		s.logf("ci: looking for a live run for %s: %v", key, err)
		return Run{}, false
	}
	if stored == nil {
		return Run{}, false
	}
	return runOf(*stored), true
}

// InterruptedRunError is what a run gets for an Error when the daemon
// running it stopped before it concluded. It is prose rather than a
// sentinel because the thing that reads it is a person looking at the
// run; the fact a *caller* acts on is Run.Interrupted.
const InterruptedRunError = workflows.InterruptedRunError

// LiveRuns returns every stored run that has not concluded yet, with its jobs
// and steps.
func (s *StoredRunStore) LiveRuns(ctx context.Context) ([]Run, error) {
	stored, err := s.store.LiveCIRuns(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Run, 0, len(stored))
	for _, r := range stored {
		out = append(out, runOf(r))
	}
	return out, nil
}

// RecoverUnattachable resolves the runs a previous process left in
// flight that nothing could reattach, and reports how many there were.
//
// Called once at startup, after the adoption pass (daemon.AdoptCIRuns) has
// reattached what it could and before anything reads a run back. A run
// with no conclusion means "still going", which was self-evidently true
// of an in-memory store and is not true of a stored one: nothing is
// running those jobs any more, and left alone such a row would
// deduplicate a fresh request against a dead run (Live) and leave the
// task waiting on it waiting forever (orchestrator.collectCIRun).
//
// attached names the runs the adoption pass took back -- every run with
// a step to reattach by exec id, or a readopted sandbox whose step runner
// is still there (docs/design/grain-on-gke.md#GKE-4) -- and those are
// still going. What is left has nothing to attach: a run that was queued
// and had no sandbox yet, or one on a backend that cannot give a sandbox
// back. Those are stamped cancelled-and-interrupted rather than deleted.
// What a daemon was in the middle of when it stopped is worth being able
// to look at, and Run.Interrupted is what tells the reconciler to run the
// branch again rather than to treat the cancellation as a verdict.
func (s *StoredRunStore) RecoverUnattachable(ctx context.Context, now time.Time, attached []string) (int, error) {
	return s.store.RecoverInterruptedCIRunsExcept(ctx, string(ConclusionCancelled), InterruptedRunError, now, attached)
}

// PutStepAttachment implements StepAttachments: it records where a running
// step's exec stands, so a restart can reattach it.
func (s *StoredRunStore) PutStepAttachment(ctx context.Context, a StepAttachment) error {
	ctx, cancel := s.bound(ctx)
	defer cancel()
	return s.store.PutCIStepAttachment(ctx, model.CIStepAttachment{
		RunID: a.RunID, JobID: a.JobID, StepID: a.StepID, Sandbox: a.Sandbox,
		ExecID: a.ExecID, Cursor: a.Cursor, UpdatedAt: s.now(),
	})
}

// ClearStepAttachments implements StepAttachments: a job done with its
// sandbox has nothing left to reattach.
func (s *StoredRunStore) ClearStepAttachments(ctx context.Context, runID, jobID string) error {
	ctx, cancel := s.bound(ctx)
	defer cancel()
	return s.store.DeleteCIStepAttachments(ctx, runID, jobID)
}

// StepAttachments returns what run's steps were last attached to, for the
// adoption pass after a restart.
func (s *StoredRunStore) StepAttachments(ctx context.Context, runID string) ([]StepAttachment, error) {
	ctx, cancel := s.bound(ctx)
	defer cancel()
	stored, err := s.store.CIStepAttachments(ctx, runID)
	if err != nil {
		return nil, err
	}
	out := make([]StepAttachment, 0, len(stored))
	for _, a := range stored {
		out = append(out, StepAttachment{
			RunID: a.RunID, JobID: a.JobID, StepID: a.StepID, Sandbox: a.Sandbox,
			ExecID: a.ExecID, Cursor: a.Cursor,
		})
	}
	return out, nil
}

// storedRun is a Run as the store keeps it: the same facts, with pkg/ci's
// vocabularies flattened to strings (model does not import this package,
// and should not -- see model.CIRun) and every step's output cut to what
// is worth keeping forever.
//
// A result's timestamps are optional and the columns behind them are
// not, so a time nobody has goes down as the zero time (timeValue) and
// comes back as nil again (runOf). The schema is untouched by the
// change of shape, and a row written before it round-trips the same as
// one written after.
// It reports whether the run's output was declined entire -- see
// declineRunOutput, which is the rule that stops one monstrous run
// evicting every older run's log on the way in.
func storedRun(run Run, keep StoredRunRetention) (model.CIRun, bool) {
	keep = keep.withDefaults()
	out := model.CIRun{
		ID:           run.ID,
		Key:          string(run.Key),
		Repo:         run.Repo,
		Ref:          run.Ref,
		Commit:       run.Commit,
		Event:        string(run.Event),
		RequestedBy:  run.RequestedBy,
		StartedAt:    timeValue(run.StartedAt),
		FinishedAt:   timeValue(run.FinishedAt),
		Conclusion:   string(run.Conclusion),
		Interrupted:  run.Interrupted,
		Error:        run.Error,
		CancelReason: run.CancelReason,
		CancelledBy:  string(run.CancelledBy),
		Recheck:      run.Recheck,
	}
	// A run no build stamped goes down as empty columns and comes back as
	// no Build at all (runOf) -- the same absent-is-absent round trip the
	// optional instants above take.
	if run.Build != nil {
		out.Build = run.Build.Revision
		out.BuildModified = run.Build.Modified
		out.BuildTime = timeValue(run.Build.CommittedAt)
	}
	tempAttempts := make(map[int][]JobAttempt, len(run.Jobs))
	for j, job := range run.Jobs {
		stored := model.CIJob{
			Workflow:     job.Workflow,
			WorkflowPath: job.WorkflowPath,
			JobID:        job.JobID,
			Name:         job.Name,
			Key:          string(job.Key),
			Cached:       job.Cached,
			Sandbox:      job.Sandbox,
			StartedAt:    timeValue(job.StartedAt),
			FinishedAt:   timeValue(job.FinishedAt),
			Conclusion:   string(job.Conclusion),
			Error:        job.Error,
			Activity:     job.Activity,
			ActivityAt:   timeValue(job.ActivityAt),
			Queued:       job.Queued,
			QueuedCause:  string(job.QueuedCause),
			QueuedReason: job.QueuedReason,
			RetryCount:   job.RetryCount,
			RetryReason:  job.RetryReason,

			Flake:            job.Flake,
			Quarantined:      job.Quarantined,
			QuarantinedTests: job.QuarantinedTests,
			Status:           job.Status,
			Warning:          job.Warning,
			FailureSummary:   storedFailureSummary(job.FailureSummary),
			BaseCommit:       job.BaseCommit,
		}
		for _, step := range job.Steps {
			stored.Steps = append(stored.Steps, model.CIStep{
				Name:       step.Name,
				Conclusion: string(step.Conclusion),
				ExitCode:   step.ExitCode,
				Output:     TruncateOutputTo(step.Output, MaxStoredStepOutput),
				Error:      step.Error,
				StartedAt:  timeValue(step.StartedAt),
				FinishedAt: timeValue(step.FinishedAt),
			})
		}
		if attempts := prepareJobAttemptsForStore(job); len(attempts) > 0 {
			tempAttempts[j] = attempts
		}
		out.Jobs = append(out.Jobs, stored)
	}
	boundRunOutput(out.Jobs, tempAttempts, int(keep.RunOutputBytes))
	declined := declineRunOutput(out.Jobs, tempAttempts, keep.OutputBytes)
	for j := range out.Jobs {
		if attempts := tempAttempts[j]; len(attempts) > 0 {
			out.Jobs[j].Attempts = encodeStoredAttempts(attempts)
		}
	}
	return out, declined
}

// declineRunOutput drops a run's output entire when it is larger than
// the whole ceiling the deployment keeps output within, and reports that
// it did.
//
// This is the case a byte ceiling has and a count of runs does not. The
// sweep behind it drops whole runs, oldest first, until what is left
// fits; a single run larger than the ceiling would therefore sweep every
// other run's output away and still not fit, so one badly configured
// ceiling -- or one run allowed to store more than it -- would cost a
// deployment its whole diagnostic history rather than its own.
//
// Declining is the same choice DirArtifacts makes for a body over its
// per-artifact bound: what the run printed is recorded as *declined*,
// step by step, rather than silently absent, so a reader who goes
// looking is told which bound took it and what to do about it.
//
// It cannot fire on a deployment running grain's own numbers
// (MaxStoredRunOutput is 256KB against a 64MB ceiling). It fires on one
// that has set the ceiling below what a single run may store, which is a
// combination Settings lets an operator choose deliberately.
func declineRunOutput(jobs []model.CIJob, tempAttempts map[int][]JobAttempt, ceiling int64) bool {
	if ceiling <= 0 {
		return false // no ceiling: nothing to be larger than
	}
	total := int64(0)
	for j := range jobs {
		for s := range jobs[j].Steps {
			total += int64(len(jobs[j].Steps[s].Output))
		}
		for a := range tempAttempts[j] {
			att := &tempAttempts[j][a]
			total += int64(len(att.GoTestLog))
			for s := range att.Steps {
				total += int64(len(att.Steps[s].Output))
			}
		}
	}
	if total <= ceiling {
		return false
	}
	for j := range jobs {
		for s := range jobs[j].Steps {
			step := &jobs[j].Steps[s]
			if step.Output == "" {
				continue
			}
			step.Output = declinedOutputNote(len(step.Output), ceiling)
		}
		for a := range tempAttempts[j] {
			att := &tempAttempts[j][a]
			if att.GoTestLog != "" {
				att.GoTestLog = declinedOutputNote(len(att.GoTestLog), ceiling)
			}
			for s := range att.Steps {
				step := &att.Steps[s]
				if step.Output == "" {
					continue
				}
				step.Output = declinedOutputNote(len(step.Output), ceiling)
			}
		}
	}
	return true
}

// declinedOutputNote is what a step keeps when the whole run's output
// was declined, as against droppedOutputNote's "there was no room for
// yours in particular".
func declinedOutputNote(dropped int, ceiling int64) string {
	return "[grain ci] " + humanBytes(int64(dropped)) + " of this step's output was declined by grain, not dropped by " +
		"the step: this run printed more than the " + humanBytes(ceiling) + " of step output this deployment keeps in " +
		"total, so none of it was stored rather than sweeping every older run's output to make room. Raise the CI " +
		"step output ceiling in Settings, or have the step write what matters to a file and print that instead."
}

// boundRunOutput cuts a run's stored output down to budget, spending it
// where a reader would look first.
//
// A step that failed keeps what it printed, up to the per-step bound,
// because that output is the whole reason anybody opens a finished run.
// What is left over is divided among the rest smallest first, so a step
// that printed one line keeps its line and the steps that printed
// megabytes share the remainder; a step whose share is too small to read
// keeps a note saying where its output went instead of a fragment of it.
//
// It runs on every write rather than once at the end, because a run is
// written again as each of its jobs finishes (Update reads the stored
// run back, so earlier jobs arrive here already cut). A step's stored
// output can therefore shrink as the run goes on -- the later jobs are
// competing for the same budget -- and what a reader sees on a finished
// run is the last of those passes, which is the one that had every job
// in front of it.
func boundRunOutput(jobs []model.CIJob, tempAttempts map[int][]JobAttempt, budget int) {
	var failed, rest []*string
	total := 0
	for j := range jobs {
		for s := range jobs[j].Steps {
			step := &jobs[j].Steps[s]
			total += len(step.Output)
			if step.Conclusion == string(ConclusionFailure) || step.Error != "" {
				failed = append(failed, &step.Output)
				continue
			}
			rest = append(rest, &step.Output)
		}
		for a := range tempAttempts[j] {
			att := &tempAttempts[j][a]
			if att.GoTestLog != "" {
				total += len(att.GoTestLog)
				if att.Conclusion == ConclusionFailure || att.Conclusion == ConclusionErrored || att.Error != "" {
					failed = append(failed, &att.GoTestLog)
				} else {
					rest = append(rest, &att.GoTestLog)
				}
			}
			for s := range att.Steps {
				step := &att.Steps[s]
				total += len(step.Output)
				if step.Conclusion == ConclusionFailure || step.Conclusion == ConclusionErrored || step.Error != "" {
					failed = append(failed, &step.Output)
					continue
				}
				rest = append(rest, &step.Output)
			}
		}
	}
	if total <= budget {
		return
	}

	spent := 0
	for _, out := range failed {
		spent += len(*out)
	}
	if spent >= budget {
		// The failures alone are past the budget, which is a run where
		// everything went wrong at once. They divide the whole of it, and
		// the steps that succeeded keep a note rather than a share: a
		// green step's output is the first thing worth giving up.
		shareOutput(budget, failed)
		for _, out := range rest {
			*out = droppedOutputNote(len(*out))
		}
		return
	}
	shareOutput(budget-spent, rest)
}

// shareOutput divides budget among these steps smallest first: a step
// whose output fits in an equal share keeps all of it, and what it does
// not use is divided again among the steps that do not fit. That is
// max-min fairness, and it is the rule that makes "one step printed a
// gigabyte" cost that step rather than every other step in the run.
//
// The elision note each truncation leaves behind is charged to the
// budget like anything else, so a run lands within it rather than within
// it plus a note per step.
func shareOutput(budget int, steps []*string) {
	sort.SliceStable(steps, func(i, j int) bool { return len(*steps[i]) < len(*steps[j]) })
	for i, out := range steps {
		share := budget / (len(steps) - i)
		switch {
		case len(*out) <= share:
		case share < MinStoredStepOutput:
			*out = droppedOutputNote(len(*out))
		default:
			*out = truncateWithin(*out, share)
		}
		budget -= len(*out)
	}
}

// truncateWithin is TruncateOutputTo with the note it leaves behind
// counted inside max rather than added to it. TruncateOutputTo answers
// "keep this much of the output"; a caller spending a fixed budget is
// asking "cost me no more than this", and the difference is the note --
// small, but a bound that is exceeded by a note per step is not a bound.
func truncateWithin(s string, max int) string {
	cut := TruncateOutputTo(s, max)
	if over := len(cut) - max; over > 0 {
		cut = TruncateOutputTo(s, max-over)
	}
	if len(cut) > max {
		// The note's own length moved when the elision did (a digit
		// fewer in "27KB"). Nothing here is worth a third pass.
		return cut[:max]
	}
	return cut
}

// droppedOutputNote is what a step keeps in place of output there was no
// room for. A note rather than an empty column: a reader looking at a
// step that printed nothing and a step whose output grain did not keep
// is asking two different questions, and only one of them is about the
// step.
func droppedOutputNote(dropped int) string {
	return "[grain ci] " + strconv.Itoa(dropped/1024) + "KB of this step's output was dropped by grain, not by " +
		"the step: this run printed more than a stored run keeps (ci.MaxStoredRunOutput), and what room there " +
		"was went to the steps that failed. Have the step write what matters to a file and print that instead."
}

// runOf is storedRun the other way round.
func runOf(stored model.CIRun) Run {
	run := Run{
		ID:           stored.ID,
		Key:          Key(stored.Key),
		Repo:         stored.Repo,
		Ref:          stored.Ref,
		Commit:       stored.Commit,
		Event:        Event(stored.Event),
		RequestedBy:  stored.RequestedBy,
		StartedAt:    timePtr(stored.StartedAt),
		FinishedAt:   timePtr(stored.FinishedAt),
		Conclusion:   Conclusion(stored.Conclusion),
		Interrupted:  stored.Interrupted,
		Error:        stored.Error,
		CancelReason: stored.CancelReason,
		CancelledBy:  CancelSource(stored.CancelledBy),
		Recheck:      stored.Recheck,
	}
	if stored.Build != "" {
		run.Build = &Build{
			Revision:    stored.Build,
			CommittedAt: timePtr(stored.BuildTime),
			Modified:    stored.BuildModified,
		}
	}
	for _, job := range stored.Jobs {
		result := JobRun{
			Workflow:     job.Workflow,
			WorkflowPath: job.WorkflowPath,
			JobID:        job.JobID,
			Name:         job.Name,
			Key:          Key(job.Key),
			Cached:       job.Cached,
			Sandbox:      job.Sandbox,
			StartedAt:    timePtr(job.StartedAt),
			FinishedAt:   timePtr(job.FinishedAt),
			Conclusion:   Conclusion(job.Conclusion),
			Error:        job.Error,
			Activity:     job.Activity,
			ActivityAt:   timePtr(job.ActivityAt),
			Queued:       job.Queued,
			QueuedCause:  WaitCause(job.QueuedCause),
			QueuedReason: job.QueuedReason,
			Retried:      job.RetryCount > 0,
			RetryCount:   job.RetryCount,
			RetryReason:  job.RetryReason,

			Flake:            job.Flake,
			Quarantined:      job.Quarantined,
			QuarantinedTests: job.QuarantinedTests,
			Status:           job.Status,
			Warning:          job.Warning,
			FailureSummary:   failureSummaryOf(job.FailureSummary),
			BaseCommit:       job.BaseCommit,
		}
		// Run.Quarantined is the engine's "any job quarantined" and has no
		// column of its own: it is read back from the jobs it was made of.
		if job.Quarantined {
			run.Quarantined = true
		}
		for _, step := range job.Steps {
			result.Steps = append(result.Steps, StepRun{
				Name:       step.Name,
				Conclusion: Conclusion(step.Conclusion),
				ExitCode:   step.ExitCode,
				Output:     step.Output,
				Error:      step.Error,
				StartedAt:  timePtr(step.StartedAt),
				FinishedAt: timePtr(step.FinishedAt),
			})
		}
		result.Attempts = jobAttemptsOf(job.Attempts)
		if len(result.Attempts) > 0 {
			expectedFinal := 1 + job.RetryCount
			if result.Retried && expectedFinal < 2 {
				expectedFinal = 2
			}
			last := &result.Attempts[len(result.Attempts)-1]
			if last.Attempt == expectedFinal {
				if len(last.Steps) == 0 && len(result.Steps) > 0 {
					last.Steps = append([]StepRun(nil), result.Steps...)
				}
				result.GoTestLog = last.GoTestLog
			}
		}
		run.Jobs = append(run.Jobs, result)
	}
	return run
}

// timePtr is an instant as a result carries one: nil for Go's zero
// time, which is what a store with a plain, non-nullable time.Time
// column has in it for a timestamp nobody ever set.
func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// timeValue is timePtr the other way round, for writing an optional
// instant into such a column.
func timeValue(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
