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
	"sort"
	"sync"
	"time"
)

// DefaultRunHistory is how many finished runs a MemoryRunStore keeps.
// Enough that the run somebody is asking about is still there when they
// get round to asking, small enough that a daemon holding all of it in
// memory never notices.
//
// A store with a database behind it is not held to it: see
// DefaultStoredRunHistory, which is an order of magnitude larger for
// exactly that reason -- and which a deployment can move for itself
// (StoredRunRetention), where this one is a constant because what it
// bounds is one process's memory.
const DefaultRunHistory = 100

// RunStore is where runs are kept between being started and being read
// back.
//
// Two implementations, and the interface is the seam between them that
// this type always existed to be: MemoryRunStore, which is bounded and
// forgets everything a restart takes with it, and StoredRunStore, which
// is the same thing over model.Store's `ci_run` tables and survives one.
// A deployment with a database uses the second (cmd/grain's
// buildCIService); a test, and anything with nowhere to write, uses the
// first.
//
// # What the signatures do not have
//
// A context, and an error. That is deliberate and it is the reason the
// swap cost no caller anything: Service.Get and Service.List are read by
// an HTTP handler and by a reconciler that both already handle "there is
// no such run" (it is what an evicted run always looked like), and
// threading an error out of them would have turned a storage change into
// a change to ci.Runner and every test that implements it. So a durable store answers a failed read the way an
// in-memory one answers a forgotten run -- not found -- and says so in
// the daemon log, where a failing database is an operator's problem
// rather than a caller's.
type RunStore interface {
	// Put records a run, replacing whatever was there under the same id.
	Put(run Run)
	// Update applies fn to the run with this id, if it is still held. It
	// is how a run in flight records a job finishing without a read, a
	// mutate and a write racing another one.
	Update(id string, fn func(*Run))
	// Get returns one run entire, output and all.
	Get(id string) (Run, bool)
	// List returns the runs held, newest first, each with its jobs and
	// steps but without any step's output -- see MemoryRunStore.List on
	// why nothing that lists runs wants it.
	List() []Run
	// Find is List's narrowed form: the runs matching f, in the order f
	// asks for, at most limit of them and without any step's output
	// either.
	//
	// It is a method rather than FilterRuns over List because of what
	// List is bounded by. A listing is a poll and answers with at most
	// MaxListedRuns, so a filter applied to one searches the newest few
	// hundred runs however many a deployment keeps -- which made "has
	// this job been red on another branch this week?" a question about
	// the last day and a half of this deployment, and that question is
	// what a fortnight of retention is for (grain/task-289). A store
	// with a database behind it answers this in SQL, over all of it.
	//
	// limit of zero or less is MaxListedRuns, and any limit is bounded
	// by MaxFoundRuns.
	Find(f RunFilter, limit int) RunPage
	// Live returns a run with this key that has not finished, if there is
	// one -- what Service.Start uses to answer a second request for work
	// already in flight with the run doing it, rather than by doing it
	// twice.
	Live(key Key) (Run, bool)
}

// RunPage is one filtered read's answer: the runs themselves, and the
// two counts that say what is not among them.
//
// The counts are the difference between a bounded answer and a truncated
// one. Three things can be missing from a page and a reader has to be
// able to tell which: runs this deployment no longer keeps (nothing can
// help), runs that did not match (widen the filter), and matching runs
// past the limit (raise it). Matched and Stored are what name the last
// two, and they are counted over the whole history rather than over the
// page -- which is the only reason "42 of 4,812 runs match" can be said
// honestly.
type RunPage struct {
	// Runs is the page, in the filter's own order, each without any
	// step's output.
	Runs []Run
	// Matched is how many runs answered the filter in all, the limit
	// aside -- so len(Runs) below it is runs the limit dropped.
	Matched int
	// Stored is how many runs this deployment holds at all, whether or
	// not they matched. It is what tells an empty answer from an empty
	// deployment.
	Stored int
}

// MaxFoundRuns bounds one filtered read however large a limit asks for.
//
// A ceiling rather than a bound anybody is expected to hit: a filtered
// read is a deliberate question ("this job, on any branch, this week"),
// not the five-second poll MaxListedRuns is about, so it may reach past
// the listing's window and into the whole stored history -- but a
// limit typed with an extra zero should cost a large answer rather than
// a daemon's memory. It is DefaultStoredRunHistory, which is to say "at
// most everything a deployment running grain's own numbers keeps".
const MaxFoundRuns = DefaultStoredRunHistory

// foundRunLimit resolves what a caller asked for into the number of runs
// a page actually carries.
func foundRunLimit(limit int) int {
	if limit <= 0 {
		return MaxListedRuns
	}
	if limit > MaxFoundRuns {
		return MaxFoundRuns
	}
	return limit
}

// boundRuns is the first limit of these runs.
func boundRuns(runs []Run, limit int) []Run {
	if n := foundRunLimit(limit); len(runs) > n {
		return runs[:n]
	}
	return runs
}

// MemoryRunStore is RunStore in memory: bounded, oldest finished runs
// evicted first, and empty again the moment the process restarts.
//
// Live runs are never evicted, whatever the limit says: dropping the
// record of something that is still going would leave a caller polling
// an id that has vanished, and a deployment cannot have more runs in
// flight than it has sandboxes anyway.
type MemoryRunStore struct {
	mu    sync.Mutex
	limit int
	order []string // insertion order, oldest first
	runs  map[string]Run
}

// NewRunStore returns an in-memory store keeping at most limit finished
// runs; zero means DefaultRunHistory.
func NewRunStore(limit int) *MemoryRunStore {
	if limit <= 0 {
		limit = DefaultRunHistory
	}
	return &MemoryRunStore{limit: limit, runs: map[string]Run{}}
}

// Put records a run, replacing whatever was there under the same id.
func (s *MemoryRunStore) Put(run Run) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.runs[run.ID]; !ok {
		s.order = append(s.order, run.ID)
	}
	s.runs[run.ID] = cloneRun(run)
	s.evict()
}

// Update applies fn to the run with this id, if it is still held.
func (s *MemoryRunStore) Update(id string, fn func(*Run)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[id]
	if !ok {
		return
	}
	run = cloneRun(run)
	fn(&run)
	s.runs[id] = run
}

// Get returns one run.
func (s *MemoryRunStore) Get(id string) (Run, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[id]
	return cloneRun(run), ok
}

// List returns every run held, newest first -- which is the order
// anything showing them wants, and the reverse of the order they are
// evicted in -- with each step's output left out.
//
// Left out because nothing that lists runs reads it. A step keeps up to
// MaxStepOutput of what it printed and a run has as many steps as its
// workflows declare, so a listing that carried the lot is megabytes of
// log to draw a table with no output column in it; ui.ciListSummary
// threw it away again at the far end for exactly that reason, and
// StoredRunStore does not even read it off disk. The reader that wants a
// step's output is looking at that step, and Get is how it gets there.
func (s *MemoryRunStore) List() []Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Run, 0, len(s.runs))
	for _, id := range s.order {
		out = append(out, runWithoutOutput(s.runs[id]))
	}
	// Newest first, and a run with no start of its own sorts to the
	// bottom in the order it was inserted -- nothing to compare is not a
	// reason to reorder it.
	sort.SliceStable(out, func(i, j int) bool {
		return timeValue(out[i].StartedAt).After(timeValue(out[j].StartedAt))
	})
	return out
}

// Find narrows the runs held, in Go: this store keeps everything it has
// in one process's memory and is bounded by DefaultRunHistory, so there
// is nothing here for SQL to reach that a range over the same slice does
// not (StoredRunStore.Find is where the seam earns its keep).
//
// The clock is this machine's, which matters to one of the three orders:
// a run that has not finished has been going for however long it is now,
// so "longest first" moves as it runs. Nothing else here reads it.
func (s *MemoryRunStore) Find(f RunFilter, limit int) RunPage {
	runs := s.List()
	if f.Visible != nil {
		runs = FilterRuns(runs, RunFilter{Visible: f.Visible})
	}
	matched := SortRuns(FilterRuns(runs, f), f, time.Now())
	return RunPage{Runs: boundRuns(matched, limit), Matched: len(matched), Stored: len(runs)}
}

// Live returns a run with this key that has not finished, if there is
// one.
func (s *MemoryRunStore) Live(key Key) (Run, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.order {
		run := s.runs[id]
		if run.Key == key && !run.Conclusion.Done() {
			return cloneRun(run), true
		}
	}
	return Run{}, false
}

// evict drops the oldest finished runs until no more than limit of them
// are held. Live runs are neither counted nor dropped -- see the type's
// own doc comment. Called with the lock held.
func (s *MemoryRunStore) evict() {
	finished := 0
	for _, run := range s.runs {
		if run.Conclusion.Done() {
			finished++
		}
	}
	for finished > s.limit {
		dropped := false
		for i, id := range s.order {
			if run, ok := s.runs[id]; !ok || !run.Conclusion.Done() {
				continue // a live run is never evicted
			}
			delete(s.runs, id)
			s.order = append(s.order[:i], s.order[i+1:]...)
			finished--
			dropped = true
			break
		}
		if !dropped {
			return // everything held is live
		}
	}
}

// clone deep-copies the parts of a run that a caller could hold on to
// while the run itself carries on being written -- its jobs, and each
// job's steps.
//
// Every read and every write here goes through it. A Run is a value, so
// handing one out looks safe, but its slices are not: a reader ranging
// over Jobs while a job finishing rewrites one of them in place is a
// data race, and it is exactly the shape of every caller this has (an
// API handler rendering a run that is still going).
func cloneRun(r Run) Run {
	if r.Jobs == nil {
		return r
	}
	jobs := make([]JobRun, len(r.Jobs))
	copy(jobs, r.Jobs)
	for i := range jobs {
		if jobs[i].Steps != nil {
			steps := make([]StepRun, len(jobs[i].Steps))
			copy(steps, jobs[i].Steps)
			jobs[i].Steps = steps
		}
		if jobs[i].Attempts != nil {
			attempts := make([]JobAttempt, len(jobs[i].Attempts))
			for j, a := range jobs[i].Attempts {
				if a.Steps != nil {
					aSteps := make([]StepRun, len(a.Steps))
					copy(aSteps, a.Steps)
					a.Steps = aSteps
				}
				if a.Artifacts != nil {
					aArts := make([]Artifact, len(a.Artifacts))
					copy(aArts, a.Artifacts)
					a.Artifacts = aArts
				}
				attempts[j] = a
			}
			jobs[i].Attempts = attempts
		}
	}
	r.Jobs = jobs
	return r
}

// withoutOutput is a clone with every step's output dropped -- what a
// listing serves. See MemoryRunStore.List.
func runWithoutOutput(r Run) Run {
	out := cloneRun(r)
	for i := range out.Jobs {
		out.Jobs[i].GoTestLog = ""
		for j := range out.Jobs[i].Steps {
			out.Jobs[i].Steps[j].Output = ""
		}
		for j := range out.Jobs[i].Attempts {
			out.Jobs[i].Attempts[j].GoTestLog = ""
			for k := range out.Jobs[i].Attempts[j].Steps {
				out.Jobs[i].Attempts[j].Steps[k].Output = ""
			}
		}
	}
	return out
}
