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

// Package metrics measures what a deployment actually delivers: how many
// tasks it finished over a window (throughput), and how long each stage
// of a task's life took to get there (latency).
//
// It exists because grain could not answer either question. The store has
// held every moment needed for both since tasks became rows -- filed,
// approved, dispatched, finished, completed -- and nothing ever read them
// together, so "is this deployment getting slower?" and "where does a
// task's day actually go?" were answerable only by reading a task list
// and doing arithmetic by eye. README's own "What this costs" is the
// sharpest example: a VM boot moved onto the critical path of every
// dispatch, the mitigations for it (a golden image, a warm spare) are
// both unbuilt, and that section says outright that it is "worth
// measuring before reaching for either." This is that measurement, and
// task_run.agent_started_at (schema.go) is the one moment that had to
// start being recorded for it to be possible at all.
//
// Three rules shape everything below, and the one exception to the first
// of them is stated after them.
//
// **Nothing here is stored.** A Report is computed from rows that already
// exist, every time it is asked for, the same way task_state is a view
// rather than a column (docs/data-model.md's "anything derivable is
// derived, never stored"). There is no counter to increment on a hot
// path, nothing to reset, and no way for a metric to disagree with the
// task it describes -- a task edited, retried or closed changes what
// every past report would have said, which is correct, because the record
// changed.
//
// **A window bounds measurements, not rows.** A sample belongs to a
// window when the moment it *ended* falls inside it: a task completed
// this morning contributes its whole lead time even if it was filed last
// month. That makes a report answer "what did this deployment deliver
// during these dates", which is the question, rather than "what happened
// entirely within them", which nothing ever asks.
//
// **A missing moment is skipped, never guessed.** Tasks filed before
// created_at existed, runs recorded before agent_started_at did, a run
// that failed in setup and never reached an agent -- each simply
// contributes to no distribution rather than to a wrong one, which is why
// every Distribution carries its own N. Two stages of the same report
// legitimately have different sample counts.
//
// **One thing measured here is not a row, because it never was.** A
// RunCycle tick -- the daemon's own scheduling pass -- reads the store,
// decides, and returns, leaving nothing behind to derive its duration
// from afterwards. It is also the missing half of Latency.QueueWait: a
// task waiting because the deployment was full and a task waiting
// because the tick was slow to reach dispatch produce the same number.
// So the daemon measures its own ticks into a bounded in-memory ring
// (orchestrator.CycleTimes -- no table, no write per tick, nothing
// stored, and lost on restart, which its own doc comment argues for),
// and hands them here as Input.Cycles. The rules above still hold for it
// as far as they can: it is windowed on the same "the moment it ended"
// rule, it carries its own N, and Report.Cycles says outright how far
// back it actually reaches, which is however long that daemon's ring is
// rather than however long a window was asked for.
//
// This package holds no state and touches no database: pkg/model reads
// the rows (Store.TaskTimings, Store.RunTimings), the daemon reports its
// own ticks, this decides what they mean, and pkg/ui serves the answer.
package metrics

import (
	"math"
	"slices"
	"sort"
	"time"

	"github.com/ai-on-gke/grain/pkg/kubectlgate"
	"github.com/ai-on-gke/grain/pkg/model"
)

// Window is the half-open interval [Since, Until) a Report covers.
type Window struct {
	Since time.Time
	Until time.Time
}

// Duration is how long the window is, or zero for one that is empty or
// inverted -- which is what every rate below divides by, and so what they
// all check before dividing.
func (w Window) Duration() time.Duration {
	if !w.Until.After(w.Since) {
		return 0
	}
	return w.Until.Sub(w.Since)
}

// holds reports whether a moment falls in the window. A nil moment never
// does: "this never happened" and "this happened outside the window" are
// both reasons to leave a sample out, and neither is a reason to invent
// one.
func (w Window) holds(t *time.Time) bool {
	return t != nil && !t.Before(w.Since) && t.Before(w.Until)
}

// DefaultBuckets is how many points a Report's own time series carries
// when the caller does not say -- enough for a sparkline to show a trend
// across the window, few enough that a report stays a small JSON body.
const DefaultBuckets = 12

// maxBuckets caps what a caller can ask for, so a window sliced into a
// bucket per second cannot turn one report into a megabyte of them.
const maxBuckets = 200

// Input is everything a Report is computed from. Tasks and Runs are the
// whole store's worth (see Store.TaskTimings on why they are not
// pre-filtered); States and MaxConcurrent are what the store knows about
// right now rather than about the window, and both are optional -- a
// caller that has neither still gets every throughput and latency number,
// and gets a Backlog and a Utilization of zero.
type Input struct {
	Window Window
	Tasks  []model.TaskTiming
	Runs   []model.RunTiming
	States map[string]model.State
	// TurnsStopped is every open task parked on an operator's Stop
	// (model.Store.TurnsStopped), counted apart in Backlog. Optional like
	// States: a caller with none counts none.
	TurnsStopped  map[string]bool
	MaxConcurrent int
	Buckets       int
	// Cycles is what the daemon's own reconcile loop measured about
	// itself, and the one input here that does not come from a row --
	// see Cycles below for why it cannot. Optional, like States and
	// MaxConcurrent: a caller with no daemon to ask (the CLI over REST
	// against another process, a test) supplies none and gets an empty
	// Cycles rather than a wrong one.
	Cycles CycleHistory
	// ToolUses and CheckWaits are what each run recorded about its own
	// tool use (Store.RunToolUses, Store.RunCheckWaits) -- the whole
	// tables, like Tasks and Runs, and windowed here against the run each
	// row belongs to. Optional in the same way States is: a caller that
	// supplies neither gets an empty Tools and Checks rather than a wrong
	// one, and every other number is unaffected.
	ToolUses   []model.RunToolUse
	CheckWaits []model.RunCheckWait
	// CIHabits is how each run worked a red build (Store.RunCIHabits),
	// windowed and optional in the same way as the two above.
	CIHabits []model.RunCIHabit
	// Prompts is how big each run's own prompt was
	// (Store.RunPrompts) -- the whole table again, windowed here against
	// the run it belongs to, and optional in the same way: a caller that
	// supplies none gets an empty Prompts rather than a deployment that
	// looks as though it hands its runs nothing.
	Prompts []model.RunPrompt
	// Transcripts is how big each run's transcript was (Store.RunTranscripts)
	// -- windowed against the run it belongs to.
	Transcripts []model.RunTranscript
	// CIJobs is every CI job this deployment has stored
	// (Store.CIJobTimings) -- the whole table, like the slices above, and
	// windowed here against each job's own ending rather than against any
	// run of an agent. It is the one input not keyed to a task_run at
	// all: grain's CI runs for base branches and for operators as well as
	// for tasks (see ci.go on what a scope can and cannot do with that).
	// Optional in the same way as the rest: a caller that supplies none
	// gets an empty CIQueueing.
	CIJobs []model.CIJobTiming
	// GitHubCIJobs is every GitHub CI job this deployment recorded
	// (Store.GitHubCIOutcomes) -- CI grain does not run, kept apart from
	// CIJobs and read into GitHubCI alone, so no grain-CI number above
	// depends on it. GitHubBaseBranches is which branches are base
	// branches (Store.ListBaseBranchCI), the ones GitHubCI.BaseBranches
	// measures red and green time on. Both optional: a caller that
	// supplies neither gets an empty GitHubCI and nothing else changes.
	GitHubCIJobs       []model.GitHubCIOutcome
	GitHubBaseBranches []BranchRef
	// MergeBatches is every finished merge batch this deployment still
	// keeps a record of (Store.MergeBatchRecords) -- the whole bounded
	// history, like the slices above, and windowed here against each
	// batch's own ending. MergeBatchSize is what the batch size is
	// configured to now (Config.MergeBatchSizeOr), which is the only thing
	// the count of batches that reached the limit can be measured against
	// and which no row carries; zero for a caller with no settings to
	// read, exactly as MaxConcurrent is. Both optional: a caller that
	// supplies neither gets an empty Merges.Batches rather than a wrong
	// one, and the landing rate and lead times beside it are unaffected
	// (merges.go).
	MergeBatches   []model.MergeBatchRecord
	MergeBatchSize int
	// MergeQueueEvents is the merge queue's record of what it had to do
	// over again -- rebases and retests (Store.MergeQueueContentionEvents)
	// -- a bounded history read entire and windowed here against each
	// event's own moment (Merges.Contention). Optional: a caller that
	// supplies none gets zero rebases and retests and the rest of the
	// section unchanged.
	MergeQueueEvents []model.MergeQueueContentionEvent
	// MergeRechecks is what the merge queue's overlap rule said about each
	// merge (Store.MergeRecheckRecords, grain/task-985) -- a bounded
	// history read entire and windowed here against each decision's own
	// moment (Merges.Rechecks). Optional: a caller that supplies none gets
	// a zeroed Merges.Rechecks.
	MergeRechecks []model.MergeRecheckRecord
	// BaseHolds is the base-branch hold history (Store.BaseHoldPasses):
	// one row per red stretch of a base branch and how many changes it
	// held in base_broken. Bounded, read entire and windowed here against
	// each pass's own ending (Merges.BaseHolds). Optional, like the rest.
	BaseHolds []model.BaseHoldPass
	// APICalls is the per-call agent-API log (Store.RunAPICalls), used by
	// Agents to count re-reads (the same target id read more than twice in a
	// wake). Optional: a caller that supplies none gets 0 re-reads.
	APICalls []model.RunAPICall
	// ReportChecks is the steward report-check history (Store.RunReportChecks).
	// Optional: a caller that supplies none gets zeroed report-check counts.
	ReportChecks []model.RunReportCheck
	// SettingsChanges is when this deployment's own parameters moved,
	// read off the state repository's git history by whoever has one to
	// read (staterepo.SettingsChanges, converted). Like Cycles it is not a
	// row and cannot be derived from one -- a settings row remembers what
	// it is, not when it became that -- and like Cycles it is the caller's
	// to supply.
	//
	// SettingsHistory is whether there was a history to read at all, and
	// is separate from the slice being empty for the reason Cycles is
	// reported with its own "enabled": a deployment with no state
	// repository and a deployment nobody has changed anything on must not
	// read the same (changes.go).
	SettingsChanges []SettingsChange
	SettingsHistory bool
	// Host is what the machine underneath this deployment was doing while
	// it worked, as hourly rollups (sysstat.History, converted). The third
	// input that is not a row and cannot be one: a load average is a fact
	// about a moment that leaves nothing behind, and the host is the thing
	// being measured, so it is sampled cheaply elsewhere and folded before
	// it gets here (host.go).
	//
	// HostSampled is whether there was a sampler to ask, separate from the
	// rollups being empty for SettingsHistory's reason: a deployment with
	// nothing to ask and one whose sampler started five minutes ago are
	// different answers.
	Host        HostHistory
	HostSampled bool
	// Scope is which tasks the report is about, empty for all of them
	// (grain/task-83). It is applied before anything else is measured,
	// so every number below except Cycles is over the tasks it keeps.
	Scope Scope
}

// Scope narrows a report to some of this deployment's tasks: the repos
// their work lands in, and who they are attributed to.
//
// # Why a report is scoped here and not by the caller
//
// A window bounds which measurements a report covers; a scope bounds
// which tasks it is about, and the two are not interchangeable. Filtering
// Input.Tasks before handing them over would drop the runs, the tool
// census and the CI waits belonging to those tasks as well -- five slices
// that have to be kept consistent with each other or a report comes out
// with a throughput over one set of tasks and a tool error rate over
// another. Doing it once, here, is the only place that consistency can be
// stated in one function (scoped below).
//
// # What a scope does not narrow
//
// Cycles. A RunCycle tick is the daemon's own scheduling pass over the
// whole deployment; there is no such thing as acme/widgets' share of one,
// and blanking the section for a scoped report would hide the very thing
// a slow queue_wait is read against. It says what it always says.
//
// Runs.Utilization is the other number worth reading carefully: scoped,
// it is what share of the whole deployment's capacity these tasks took,
// which is a real question -- but it is no longer "was the deployment
// full", because the runs that filled it may be out of scope.
//
// An empty field means "any": a scope naming two repos and no author is
// every task landing in either, whoever filed it. Values are matched
// exactly, in the spelling the store holds and the UI's own filters use
// ("owner/name" for a repo, the actor id for an author), because both
// vocabularies come off a board's stored query, which the UI wrote from
// the same values in the first place.
type Scope struct {
	Repos   []string
	Authors []string
	// ReposBound makes Repos a bound even when it is empty: a reader
	// confined to their teams' repositories (pkg/ui's team read scope)
	// whose teams have none on record is shown none, rather than the
	// whole deployment an empty Repos would otherwise mean.
	ReposBound bool
}

// Empty reports whether this scope narrows nothing, which is the usual
// case and the one that skips the filtering entirely.
func (s Scope) Empty() bool { return !s.byRepo() && len(s.Authors) == 0 }

// byRepo reports whether this scope narrows by repository at all.
func (s Scope) byRepo() bool { return len(s.Repos) > 0 || s.ReposBound }

// holds reports whether one task is in scope. Both conditions have to
// hold, so a scope naming a repo and an author is that author's work in
// that repo rather than either.
//
// A task with no repo to push to carries "" here, so a report scoped to
// a named repo leaves it out, which is what a report about acme/widgets
// wants. Nothing special is made of "" as a *scope* value -- a caller
// asking for it gets exactly the tasks that push nowhere -- because the
// one caller there is drops blanks before asking (ui's own
// metricsScopeValues), and a second rule here would only be a second
// place for the two to disagree.
func (s Scope) holds(t model.TaskTiming) bool {
	if s.byRepo() && !slices.Contains(s.Repos, t.Repo) {
		return false
	}
	return len(s.Authors) == 0 || slices.Contains(s.Authors, t.Author)
}

// scoped is in with everything out of scope removed: the tasks the scope
// does not hold, the runs belonging to them, the census rows belonging to
// those runs, and the states of tasks that are gone. States is a map of
// every task in the store, so a Backlog computed against the unfiltered
// one would be the whole deployment's backlog under a scoped report's
// heading -- which is the mistake this function exists to make
// impossible.
//
// Cycles and MaxConcurrent are left exactly as they arrived: both
// describe the deployment rather than any set of tasks (see Scope).
func (in Input) scoped() Input {
	if in.Scope.Empty() {
		return in
	}
	tasks := make([]model.TaskTiming, 0, len(in.Tasks))
	keptTask := make(map[string]bool, len(in.Tasks))
	for _, t := range in.Tasks {
		if in.Scope.holds(t) {
			tasks = append(tasks, t)
			keptTask[t.TaskID] = true
		}
	}
	runs := make([]model.RunTiming, 0, len(in.Runs))
	keptRun := make(map[string]bool, len(in.Runs))
	for _, r := range in.Runs {
		if keptTask[r.TaskID] {
			runs = append(runs, r)
			keptRun[r.RunID] = true
		}
	}
	states := make(map[string]model.State, len(keptTask))
	for id, st := range in.States {
		if keptTask[id] {
			states[id] = st
		}
	}
	uses := make([]model.RunToolUse, 0, len(in.ToolUses))
	for _, u := range in.ToolUses {
		if keptRun[u.RunID] {
			uses = append(uses, u)
		}
	}
	waits := make([]model.RunCheckWait, 0, len(in.CheckWaits))
	for _, wait := range in.CheckWaits {
		if keptRun[wait.RunID] {
			waits = append(waits, wait)
		}
	}
	habits := make([]model.RunCIHabit, 0, len(in.CIHabits))
	for _, h := range in.CIHabits {
		if keptRun[h.RunID] {
			habits = append(habits, h)
		}
	}
	apiCalls := make([]model.RunAPICall, 0, len(in.APICalls))
	for _, c := range in.APICalls {
		if keptRun[c.RunID] {
			apiCalls = append(apiCalls, c)
		}
	}
	reportChecks := make([]model.RunReportCheck, 0, len(in.ReportChecks))
	for _, rc := range in.ReportChecks {
		if keptRun[rc.RunID] {
			reportChecks = append(reportChecks, rc)
		}
	}
	prompts := make([]model.RunPrompt, 0, len(in.Prompts))
	for _, p := range in.Prompts {
		if keptRun[p.RunID] {
			prompts = append(prompts, p)
		}
	}
	transcripts := make([]model.RunTranscript, 0, len(in.Transcripts))
	for _, tr := range in.Transcripts {
		if keptRun[tr.RunID] {
			transcripts = append(transcripts, tr)
		}
	}
	// CI jobs are narrowed by the repository they ran for and by nothing
	// else. They are not a task's rows: a job belongs to a repository and
	// a branch, and joining a branch back to whoever filed a task would
	// be a guess about base-branch and operator runs as well as an
	// unnecessary one -- ci.go's own note, and Cycles' precedent for a
	// section a scope does not narrow in full.
	ciJobs := in.CIJobs
	if in.Scope.byRepo() {
		ciJobs = make([]model.CIJobTiming, 0, len(in.CIJobs))
		for _, j := range in.CIJobs {
			if !slices.Contains(in.Scope.Repos, j.Repo) {
				continue
			}
			ciJobs = append(ciJobs, j)
		}
	}
	// GitHub CI jobs are narrowed the same way and for the same reason.
	gitHubJobs := in.GitHubCIJobs
	if in.Scope.byRepo() {
		gitHubJobs = make([]model.GitHubCIOutcome, 0, len(in.GitHubCIJobs))
		for _, j := range in.GitHubCIJobs {
			if slices.Contains(in.Scope.Repos, j.Repo) {
				gitHubJobs = append(gitHubJobs, j)
			}
		}
	}
	in.GitHubCIJobs = gitHubJobs
	// Merge batches are narrowed the same way and for the same reason: a
	// batch belongs to a repository and a base branch, and it records no
	// member list at all (model.MergeBatchRecord), so there is nothing to
	// join back to whoever filed one of the changes in it -- merges.go's
	// own note on what an author-scoped report says about the queue.
	batches := in.MergeBatches
	if in.Scope.byRepo() {
		batches = make([]model.MergeBatchRecord, 0, len(in.MergeBatches))
		for _, b := range in.MergeBatches {
			if slices.Contains(in.Scope.Repos, b.Repo.String()) {
				batches = append(batches, b)
			}
		}
	}
	// Contention events are narrowed by repository like the batches, and
	// beyond that by the task they were about: one about a change is kept
	// with that change, and one about a whole batch -- a batch's recheck
	// or split, which names no member (model.MergeQueueContentionEvent) --
	// is the batches' to count, so a report narrowed to authors
	// counts none of those rather than other people's.
	events := in.MergeQueueEvents
	byTask := len(in.Scope.Authors) > 0
	if in.Scope.byRepo() || byTask {
		events = make([]model.MergeQueueContentionEvent, 0, len(in.MergeQueueEvents))
		for _, e := range in.MergeQueueEvents {
			if in.Scope.byRepo() && !slices.Contains(in.Scope.Repos, e.Repo.String()) {
				continue
			}
			if byTask && !keptTask[e.TaskID] {
				continue
			}
			events = append(events, e)
		}
	}
	// Re-check records are narrowed like the contention events: by the
	// repository merged into, and by the task merged.
	rechecks := in.MergeRechecks
	if in.Scope.byRepo() || byTask {
		rechecks = make([]model.MergeRecheckRecord, 0, len(in.MergeRechecks))
		for _, r := range in.MergeRechecks {
			if in.Scope.byRepo() && !slices.Contains(in.Scope.Repos, r.Repo.String()) {
				continue
			}
			if byTask && !keptTask[r.TaskID] {
				continue
			}
			rechecks = append(rechecks, r)
		}
	}
	in.MergeRechecks = rechecks
	in.Tasks, in.Runs, in.States = tasks, runs, states
	in.ToolUses, in.CheckWaits, in.Prompts, in.Transcripts = uses, waits, prompts, transcripts
	in.CIHabits = habits
	in.APICalls, in.ReportChecks = apiCalls, reportChecks
	// Holds are a branch's, narrowed by repository like the batches. A
	// report narrowed to authors keeps them all: what a red branch held is
	// the wait those authors' changes sat in, and there is no share of it
	// to divide out.
	holds := in.BaseHolds
	if in.Scope.byRepo() {
		holds = make([]model.BaseHoldPass, 0, len(in.BaseHolds))
		for _, h := range in.BaseHolds {
			if slices.Contains(in.Scope.Repos, h.Repo.String()) {
				holds = append(holds, h)
			}
		}
	}
	in.CIJobs, in.MergeBatches, in.MergeQueueEvents, in.BaseHolds = ciJobs, batches, events, holds
	return in
}

// CycleHistory is a daemon's own record of its recent RunCycle ticks --
// orchestrator.CycleTimes' contents, converted at the one boundary where
// both types are in scope (cmd/grain).
//
// Samples are oldest first, and are the most recent ones that daemon
// still remembers rather than all of them: Observed is how many it has
// ever run, so a caller can tell a full history from a truncated tail.
type CycleHistory struct {
	Observed int
	Samples  []CycleSample
}

// CycleSample is one RunCycle tick -- orchestrator.CycleTiming, in this
// package's own terms.
type CycleSample struct {
	Start        time.Time
	Duration     time.Duration
	DispatchWait time.Duration
	Commits      uint64
	Reconcilers  []ReconcilerSample
}

// ReconcilerSample is one reconciler's share of one tick. Wait is how far
// into the cycle it started; Duration is how long it then took.
type ReconcilerSample struct {
	Name     string
	Wait     time.Duration
	Duration time.Duration
	Failed   bool
}

// Distribution summarises one latency series. Percentiles are
// nearest-rank over the samples themselves -- no interpolation, so every
// value reported is one that really happened -- and N is how many samples
// there were, which is the first thing to read: a P99 over four samples
// is the maximum wearing a percentile's name.
type Distribution struct {
	N     int
	Min   time.Duration
	P50   time.Duration
	P90   time.Duration
	P99   time.Duration
	Max   time.Duration
	Mean  time.Duration
	Total time.Duration
}

// Throughput is how much work crossed each line during the window, and
// the same counts as a daily rate so two windows of different lengths
// compare.
//
// TasksClosed overlaps TasksCompleted deliberately: a task that completed
// and was closed later counts once in each, because the two lines are
// different events and a report that netted them off could not show the
// second happening at all.
type Throughput struct {
	TasksFiled     int
	TasksCompleted int
	TasksClosed    int
	RunsStarted    int
	RunsFinished   int

	FiledPerDay        float64
	CompletedPerDay    float64
	RunsFinishedPerDay float64

	// Buckets is the same throughput sliced into equal spans across the
	// window, oldest first -- what a trend line is drawn from. The last
	// bucket is the only one that can be partially elapsed.
	Buckets []Bucket
}

// Bucket is one slice of the window's own time series.
type Bucket struct {
	Since        time.Time
	Until        time.Time
	Filed        int
	Completed    int
	RunsFinished int
}

// Latency is where a task's wall-clock time goes, stage by stage. Each
// distribution covers the samples whose own stage *ended* inside the
// window (this package's own doc comment), and each is measured
// independently -- they do not sum to LeadTime, because a task can sit in
// awaiting_reply between them, be retried after a backoff, or wait on a
// dependency, and none of those is a stage anything records the start of.
type Latency struct {
	// ApprovalWait is filed -> approved: how long a proposal sat before a
	// human accepted it. Tasks approved in the same instant they were
	// filed (anything a human files directly, LandsQueued) are left out
	// rather than counted as zero -- nobody waited, and a pile of zeroes
	// would drag the percentiles of the tasks that did.
	ApprovalWait Distribution
	// QueueWait is approved -> the first attempt starting: the backlog
	// wait, and the one stage that is grain's own scheduling rather than
	// anyone's work. It is what a bigger max_concurrent, a faster tick or
	// a warm sandbox would shrink.
	QueueWait Distribution
	// SandboxSetup is an attempt starting -> its agent's first turn: a
	// sandbox built, a repo cloned, capabilities minted and placed. This
	// is the number README's "a VM boot moves onto the critical path of
	// every task" is about, and the one that says whether a golden image
	// or a warm spare would be worth building.
	SandboxSetup Distribution
	// AgentWork is the agent's first turn -> the attempt finishing: what
	// the agent framework actually spent. Grain does not control it, and
	// separating it out is what keeps it from being read as if grain did.
	AgentWork Distribution
	// Attempt is one whole attempt, start to finish -- SandboxSetup plus
	// AgentWork plus the finish path, and reported alongside them because
	// it is the only one of the three every run has (a run that never
	// reached its agent has no setup or agent sample, but it took time
	// all the same).
	Attempt Distribution
	// RetryWait is one attempt finishing -> the next attempt on the same
	// task starting: dispatch's own failure backoff plus another turn
	// through the queue.
	RetryWait Distribution
	// TimeToFinish is the first attempt starting -> the task completing:
	// how long the work itself took once it began, across however many
	// attempts, questions and replies it needed.
	TimeToFinish Distribution
	// LeadTime is filed -> completed, the whole of what somebody who
	// filed a task actually waited. Every other stage above is an answer
	// to "why is this number what it is".
	LeadTime Distribution
	// InteractiveStartup and BatchStartup break an attempt's startup down by
	// phase (queued -> dispatched, sandbox acquire, prepare sandbox, agent
	// launch -> first turn, and attempt started -> agent's first turn) for
	// interactive sessions separately from batch tasks.
	InteractiveStartup StartupBreakdown
	BatchStartup       StartupBreakdown
}

// StartupBreakdown is the per-phase startup breakdown of attempts in the window
// (model.RunStartup), computed separately for interactive and batch tasks.
type StartupBreakdown struct {
	QueuedToDispatched     Distribution
	SandboxAcquire         Distribution
	PrepareSandbox         Distribution
	AgentLaunchToFirstTurn Distribution
	StartedToFirstTurn     Distribution
}

// Runs is how the window's attempts turned out, and how much of the
// deployment's own capacity they used.
type Runs struct {
	// Outcomes counts task_run.outcome over the runs that finished in the
	// window. The vocabulary is open (schema.go keeps enums out of the
	// DDL): "succeeded", "failed", "cancelled", "setup-failed" today, and
	// whatever a later ending calls itself. A run finished with no
	// outcome recorded counts under "unrecorded".
	Outcomes map[string]int
	// Endings counts the same runs by how they actually ended
	// (model.RunEnding), which is what Outcomes above cannot say. Two of
	// its words each cover two endings with different fixes: "cancelled"
	// is both a human closing the task and the run hitting the
	// wall-clock cap, and "failed" is both a framework that broke and a
	// run that exhausted MaxAgentTurns. Each of those is its own series
	// here -- alongside no_action, a run that had tools, used them and
	// produced nothing, which is the purest measure of the agent-facing
	// surface failing and was previously one key in a map beside
	// "succeeded"; and alongside the runs a provider's usage limit
	// stopped, which are not a fault of the deployment's at all.
	Endings map[model.RunEnding]int
	// ExecTunnelFailures counts the same runs that never reached their
	// agent because the path to their sandbox's container failed
	// (kubectlgate.IsTransientTransport on the run's detail: the
	// cluster's exec tunnel to the kubelet, an API server that could not
	// be reached) -- whether they were suspended for grain's automatic
	// setup retry or ended setup-failed after it. It is the cluster's
	// health, not the tasks': a number that climbs is the control plane
	// or its konnectivity tunnel, and no change to any task will fix it.
	ExecTunnelFailures int
	// ExecTunnelToolCalls is the same tunnel failing later, under runs
	// that had reached their agent: how many of their sandbox tool calls
	// still failed on the exec transport after the exec runner's own
	// retries (model.RunTiming.ExecTransportFailures), summed over the
	// runs that finished in the window.
	ExecTunnelToolCalls int
	// AttemptsPerCompletion is how many attempts, on average, each task
	// completed in this window took -- every attempt it ever made, not
	// only those inside the window, since that is what the completion
	// actually cost. 1.0 is a deployment that gets it right first time;
	// the distance above 1.0 is rework.
	AttemptsPerCompletion float64
	// MeanConcurrent is how many runs were live at once on average across
	// the window: every run's overlap with it, summed, over the window's
	// own length. A run still live at Until counts up to Until.
	MeanConcurrent float64
	// MaxConcurrent is the deployment's own configured limit, echoed here
	// so MeanConcurrent has something to be a fraction of, and zero when
	// the caller did not supply one.
	MaxConcurrent int
	// Utilization is MeanConcurrent / MaxConcurrent: the share of the
	// deployment's dispatch capacity that was actually in use. A low
	// number next to a deep Backlog is the signature of a scheduling
	// problem rather than a capacity one -- work waiting while slots sat
	// idle -- and that pair is the reason both numbers are in one report.
	Utilization float64
	// Live is how many runs were still in flight at Until.
	Live int
}

// Backlog is a gauge, not a window: how much work was sitting in the
// system when the report was taken. Throughput without it says how fast
// the deployment goes and not whether that is fast enough -- a completion
// rate matching the filing rate means something entirely different
// depending on whether the queue behind it is empty or a hundred deep.
//
// It is only populated when Input.States is supplied, because the honest
// version of these counts is task_state's own vocabulary: a task that is
// neither completed nor closed may be waiting for a human to approve it,
// queued, awaiting a reply, or capped out at MaxConsecutiveFailures, and
// those are four different problems that the timings alone cannot tell
// apart.
//
// One thing that vocabulary cannot say is which queued tasks are actually
// dispatchable. Waiting on a dependency is an annotation on 'queued'
// rather than a state of its own (schema.go's task_blocked, and task_ready
// which is what joins the two), and Input carries no blocker counts -- so
// a task nothing can dispatch until its blocker lands is counted here, and
// in Queued below, exactly like one that is only waiting its turn.
type Backlog struct {
	// ByState counts the unfinished states only. Completed, awaiting
	// submit and closed tasks are left out on purpose: they are a census
	// of everything the deployment has ever done, which grows forever and
	// is not work sitting in the system -- how many of them landed *in the
	// window* is what Throughput already answers.
	//
	// StateAwaitingSubmit is on that side of the line because it is a
	// post-run state: the run is over and its lead time is already
	// recorded, and a task nobody ever submits and nobody ever closes
	// accumulates for exactly as long as a completed one does. It is
	// still a queue of a kind -- of human clicks rather than of dispatch
	// -- but not the one Utilization is read against, and counting it
	// here would make a deployment that simply does not use auto-merge
	// look permanently backed up. StateAwaitingAck is out for the same
	// reason and would be worse: an answer nobody has read is a run
	// already paid for, and a deployment that files investigations would
	// otherwise look like one that cannot keep up with its own queue.
	//
	// StateDeferred is left out for the same last reason, from the other
	// end of a task's life: work somebody has explicitly put aside is not
	// asking for capacity, so counting it would make a deployment that
	// parks ideas rather than closing them look permanently backed up.
	// It comes back into this count the moment it is picked back up,
	// which is exactly when it starts wanting a dispatch again.
	ByState map[model.State]int
	// Queued is ByState[StateQueued] -- the depth of the dispatch queue
	// itself, lifted out because it is the one this report's own
	// Utilization is read against. Blocked tasks are in it, for the reason
	// ByState gives, so read a deep queue beside an idle deployment as a
	// question rather than as a verdict.
	Queued int
	// StoppedByOperator is how many of the tasks counted above are parked
	// on an operator's Stop (Input.TurnsStopped): waiting on that person
	// to reply or press Retry, and on no worker. Counted apart from
	// Queued so a backlog that is really a handful of stopped turns does
	// not read as work grain is failing to dispatch.
	StoppedByOperator int
	// OldestQueuedWait is how long the longest-queued task has been
	// waiting at Until, and OldestQueuedTaskID is which one it is. A
	// percentile over what finished cannot see a task that has never
	// started; this is what does.
	OldestQueuedWait   time.Duration
	OldestQueuedTaskID string
}

// Cycles is how the daemon's own reconcile loop has been behaving: how
// long a RunCycle tick takes, how often one actually happens, and where
// inside a tick the time goes.
//
// It is here because Latency.QueueWait cannot be attributed without it.
// A queued task waits for two entirely different reasons that produce
// the same number -- the deployment was at max_concurrent and there was
// genuinely no room (which Runs.Utilization near 1.0 shows), or there
// was room the whole time and the task was waiting on the tick. Nothing
// showed the second. A DispatchWait that has quietly grown to minutes
// under a large store looks exactly like a busy deployment from
// QueueWait alone, and the fix for each is the opposite of the fix for
// the other: more concurrency for one, a faster or better-ordered cycle
// for the second.
//
// It is also the one part of a Report not derived from rows, because a
// tick leaves none -- see orchestrator.CycleTimes on why the record is a
// ring in the daemon rather than a table. What that costs is stated
// rather than hidden: this section describes the process serving the
// report, is empty right after a restart, and reaches back only as far
// as that daemon's ring does, however long a window the caller asked
// for.
type Cycles struct {
	// N is how many ticks the window holds, and Observed is how many
	// this daemon has run since it started -- so N below Observed is
	// either a window narrower than the ring or a ring that has
	// forgotten the rest, and Truncated is which.
	N        int
	Observed int
	// Truncated says the daemon has run more ticks than it still
	// remembers: First below is where its ring begins rather than where
	// this deployment's history does. It is a fact about the history
	// supplied, not about the window, which is why it is not simply
	// Observed > N.
	Truncated bool
	// First and Last are the oldest and newest tick this section
	// actually covers -- the real span behind N, which is not the
	// requested window when the ring is shorter than it (the usual
	// case).
	First time.Time
	Last  time.Time
	// Tick is the whole of a RunCycle call: the stored-configuration
	// refresh plus every reconciler.
	Tick Distribution
	// Interval is one tick's start to the next one's -- the loop's real
	// period, which is the -poll-interval only while a tick is fast
	// compared to it. Ticks do not overlap (cmd/grain's reconcile waits
	// for one to return), so this is what the wait for the *next*
	// dispatch decision is really drawn from.
	Interval Distribution
	// DispatchWait is how far into a tick the dispatch decision was
	// reached. Interval plus this is the queue wait a task pays purely
	// for grain's own scheduling, with no contention involved at all:
	// the floor under Latency.QueueWait, and the number to compare it
	// against before concluding a deployment needs more concurrency.
	DispatchWait Distribution
	// Commits is the number of successful Store write transactions that
	// landed during each tick. CommitFree is the count of ticks whose
	// task-list cache could survive the cycle itself unchanged.
	Commits    Counts
	CommitFree int
	// Reconcilers is where a tick's time went, in the order the cycle
	// ran them. A single tick duration cannot say which reconciler grew;
	// this can, and the ordering matters because they run in sequence --
	// a pull-request sync that has grown to a minute delays every
	// decision behind it by a minute.
	Reconcilers []ReconcilerCycles
}

// ReconcilerCycles is one reconciler's numbers across the window.
type ReconcilerCycles struct {
	Name string
	// Wait is how far into a cycle this reconciler started -- what
	// everything before it cost it. For the dispatch reconciler this is
	// the same series as Cycles.DispatchWait.
	Wait Distribution
	// Duration is how long it took, and Failures is how many of those
	// ticks it ended in an error. The two are worth reading together: a
	// reconciler that is fast because it fails immediately is not a
	// reconciler that is fast.
	Duration Distribution
	Failures int
}

// Report is one measurement of a deployment. Its gauges (Runs.Live,
// Backlog) describe Window.Until rather than the window as a whole.
//
// For the report a caller usually asks for -- a length ending now -- that
// moment is the moment the report was taken, and the two readings are the
// same. They come apart for the absolute span a saved report board can
// carry (ui.MetricsRequest), whose Until may be months back, and the two
// gauges part company differently there. Runs.Live really is computed as
// of Until, because a run's own row says when it started and when it
// stopped. Backlog cannot be: Input.States is what task_state says right
// now, so a report over a past span still counts today's backlog, and
// only Backlog.OldestQueuedWait is measured to Until at all.
type Report struct {
	Window Window
	// Scope is the narrowing this report was computed under, echoed back
	// so that a reader of the report -- a page, a printout -- can say
	// which tasks it is about without being told separately.
	Scope      Scope
	Throughput Throughput
	Latency    Latency
	Runs       Runs
	Backlog    Backlog
	Cycles     Cycles
	// Tools and Checks are the inside of a run: what its tools cost it,
	// and how the CI loop it was told to go round actually went. Both are
	// empty for a caller that supplied no census (Input.ToolUses,
	// Input.CheckWaits) and for a window whose runs all predate it -- see
	// tools.go.
	Tools  Tools
	Checks Checks
	// Prompts is the other side of Tools: not what a run spent its
	// context on, but what grain spent of it before the run's first turn.
	// Empty for a caller that supplied no prompt sizes (Input.Prompts)
	// and for a window whose runs recorded none -- see prompts.go, which
	// also says why the distribution is censored by the bound and what to
	// read instead.
	Prompts Prompts
	// PullRequests is the last stretch of that same loop: whether runs
	// take the pull request grain offers them mid-flight, and whether
	// taking it changes how often a red build outlives the run that
	// pushed it. Read off the same census as Tools, and empty on the same
	// input -- see pullrequests.go.
	PullRequests PullRequests
	// CIQueueing is the other side of Checks: not how long a run waited
	// for its checks, but how much of the time those checks took was
	// spent waiting for a machine rather than running the tree, what held
	// them and which repositories wore it. Empty for a caller that
	// supplied no CI jobs (Input.CIJobs) -- see ci.go, which also says
	// why the author half of a scope does not narrow it.
	CIQueueing CIQueueing
	// GitHubCI is the CI beside grain's own: GitHub Actions jobs and
	// other providers' check runs, their pass, fail and cancel rates per
	// job, how long a commit took to go green, which jobs flaked, and how
	// long base branches were red by GitHub's results -- every number
	// labelled Source "github". Computed from Input.GitHubCIJobs alone,
	// so grain CI's sections are the same with or without it (githubci.go).
	GitHubCI GitHubCI
	// Merges is the last stretch of the same loop: what this deployment
	// landed over the window, how long a change took to go from filed to
	// landed, and what the merge queue's batching cost and saved getting
	// it there. Throughput and Latency both stop at the agent finishing;
	// this is what happens after that, and it is the half somebody waiting
	// on a change is actually waiting through -- see merges.go.
	Merges Merges
	// Changes is what was done to this deployment while everything above
	// was happening: when one of its own parameters moved, and which. It is
	// the section that makes the rest answer a tuning question rather than
	// only a diagnostic one -- see changes.go. Available is false for a
	// caller with no settings history to read, which is not the same as a
	// deployment nobody has changed.
	Changes Changes
	// Host is the machine underneath all of it: how hard it was working
	// while the numbers above were produced, on the same slices, so a job
	// that took twice as long can be read against the host that gave it
	// half a machine (host.go). Available is false for a caller with no
	// sampler to ask.
	Host Host
	// Agents groups wake telemetry and steward report checks by template
	// (agents.go, docs/design/durable-tasks.md#DT-14).
	Agents Agents
}

// Compute derives a Report. It reads its input and nothing else -- no
// clock beyond Input.Window.Until, no database -- so the same input
// always gives the same report.
func Compute(in Input) Report {
	// Narrowed before a single number is taken, so that every section
	// below is over the same set of tasks (Scope, and scoped's own note
	// on the two inputs it deliberately leaves alone).
	in = in.scoped()
	rep := Report{Window: in.Window, Scope: in.Scope}
	w := in.Window

	byTask := runsByTask(in.Runs)

	// --- throughput, and the same counts bucketed ---------------------
	buckets := newBuckets(w, in.Buckets)
	for _, t := range in.Tasks {
		if w.holds(t.CreatedAt) {
			rep.Throughput.TasksFiled++
			addTo(buckets, w, t.CreatedAt, func(b *Bucket) { b.Filed++ })
		}
		if w.holds(t.CompletedAt) {
			rep.Throughput.TasksCompleted++
			addTo(buckets, w, t.CompletedAt, func(b *Bucket) { b.Completed++ })
		}
		if w.holds(t.ClosedAt) {
			rep.Throughput.TasksClosed++
		}
	}
	rep.Runs.Outcomes = map[string]int{}
	rep.Runs.Endings = map[model.RunEnding]int{}
	for _, r := range in.Runs {
		started := r.StartedAt
		if w.holds(&started) {
			rep.Throughput.RunsStarted++
		}
		if w.holds(r.FinishedAt) {
			rep.Throughput.RunsFinished++
			rep.Runs.Outcomes[outcomeOf(r)]++
			rep.Runs.Endings[model.EndingOf(r.Outcome, r.Detail)]++
			if isExecTunnelFailure(r) {
				rep.Runs.ExecTunnelFailures++
			}
			rep.Runs.ExecTunnelToolCalls += r.ExecTransportFailures
			addTo(buckets, w, r.FinishedAt, func(b *Bucket) { b.RunsFinished++ })
		}
	}
	rep.Throughput.Buckets = buckets
	if days := w.Duration().Hours() / 24; days > 0 {
		rep.Throughput.FiledPerDay = float64(rep.Throughput.TasksFiled) / days
		rep.Throughput.CompletedPerDay = float64(rep.Throughput.TasksCompleted) / days
		rep.Throughput.RunsFinishedPerDay = float64(rep.Throughput.RunsFinished) / days
	}

	// --- latency ------------------------------------------------------
	var approval, queue, setup, agent, attempt, retry, toFinish, lead []time.Duration
	interactiveTask := make(map[string]bool, len(in.Tasks))
	for _, t := range in.Tasks {
		if t.Interactive {
			interactiveTask[t.TaskID] = true
		}
		// Approved strictly after being filed: a task a human filed
		// directly is approved in the same instant, and nobody waited.
		if t.CreatedAt != nil && w.holds(t.ApprovedAt) && t.ApprovedAt.After(*t.CreatedAt) {
			approval = append(approval, t.ApprovedAt.Sub(*t.CreatedAt))
		}
		runs := byTask[t.TaskID]
		if len(runs) > 0 {
			first := runs[0].StartedAt
			if t.ApprovedAt != nil && w.holds(&first) {
				queue = appendIfPositive(queue, first.Sub(*t.ApprovedAt))
			}
			if w.holds(t.CompletedAt) {
				toFinish = appendIfPositive(toFinish, t.CompletedAt.Sub(first))
			}
		}
		if t.CreatedAt != nil && w.holds(t.CompletedAt) {
			lead = appendIfPositive(lead, t.CompletedAt.Sub(*t.CreatedAt))
		}
	}
	type startupSamples struct {
		queuedToDispatched, sandboxAcquire, prepareSandbox, agentLaunchToFirstTurn, startedToFirstTurn []time.Duration
	}
	var interactiveStartup, batchStartup startupSamples
	for _, runs := range byTask {
		for i, r := range runs {
			if w.holds(r.AgentStartedAt) {
				setup = appendIfPositive(setup, r.AgentStartedAt.Sub(r.StartedAt))
			}
			if w.holds(r.FinishedAt) {
				attempt = appendIfPositive(attempt, r.FinishedAt.Sub(r.StartedAt))
				if r.AgentStartedAt != nil {
					agent = appendIfPositive(agent, r.FinishedAt.Sub(*r.AgentStartedAt))
				}
			}
			dst := &batchStartup
			if interactiveTask[r.TaskID] {
				dst = &interactiveStartup
			}
			startedAt := r.StartedAt
			if r.QueuedAt != nil && w.holds(&startedAt) {
				dst.queuedToDispatched = appendIfNonNegative(dst.queuedToDispatched, r.StartedAt.Sub(*r.QueuedAt))
			}
			if r.SandboxAcquiredAt != nil && w.holds(r.SandboxAcquiredAt) {
				dst.sandboxAcquire = appendIfNonNegative(dst.sandboxAcquire, r.SandboxAcquiredAt.Sub(r.StartedAt))
			}
			prepEnd := r.SandboxPreparedAt
			if prepEnd == nil {
				prepEnd = r.AgentStartedAt
			}
			if w.holds(prepEnd) {
				prepStart := r.StartedAt
				if r.SandboxAcquiredAt != nil {
					prepStart = *r.SandboxAcquiredAt
				}
				dst.prepareSandbox = appendIfNonNegative(dst.prepareSandbox, prepEnd.Sub(prepStart))
			}
			if w.holds(r.FirstTurnAt) {
				launchStart := r.AgentStartedAt
				if launchStart == nil {
					launchStart = r.SandboxPreparedAt
				}
				if launchStart != nil {
					dst.agentLaunchToFirstTurn = appendIfNonNegative(dst.agentLaunchToFirstTurn, r.FirstTurnAt.Sub(*launchStart))
				}
				dst.startedToFirstTurn = appendIfNonNegative(dst.startedToFirstTurn, r.FirstTurnAt.Sub(r.StartedAt))
			} else if r.FirstTurnAt == nil && w.holds(r.AgentStartedAt) {
				dst.startedToFirstTurn = appendIfNonNegative(dst.startedToFirstTurn, r.AgentStartedAt.Sub(r.StartedAt))
			}
			if i == 0 {
				continue
			}
			if prev := runs[i-1].FinishedAt; prev != nil {
				next := r.StartedAt
				if w.holds(&next) {
					retry = appendIfPositive(retry, next.Sub(*prev))
				}
			}
		}
	}
	summarizeStartup := func(s startupSamples) StartupBreakdown {
		return StartupBreakdown{
			QueuedToDispatched:     summarize(s.queuedToDispatched),
			SandboxAcquire:         summarize(s.sandboxAcquire),
			PrepareSandbox:         summarize(s.prepareSandbox),
			AgentLaunchToFirstTurn: summarize(s.agentLaunchToFirstTurn),
			StartedToFirstTurn:     summarize(s.startedToFirstTurn),
		}
	}
	rep.Latency = Latency{
		ApprovalWait:       summarize(approval),
		QueueWait:          summarize(queue),
		SandboxSetup:       summarize(setup),
		AgentWork:          summarize(agent),
		Attempt:            summarize(attempt),
		RetryWait:          summarize(retry),
		TimeToFinish:       summarize(toFinish),
		LeadTime:           summarize(lead),
		InteractiveStartup: summarizeStartup(interactiveStartup),
		BatchStartup:       summarizeStartup(batchStartup),
	}

	// --- capacity -----------------------------------------------------
	var occupied time.Duration
	for _, r := range in.Runs {
		occupied += overlap(w, r)
		// Live at Until, not unfinished now: a run that started before
		// Until and outlived it was live then, whatever it has done
		// since. The two only differ for a window ending in the past
		// (Report), where "still unfinished" would report a live count
		// of zero for every span the deployment has since worked
		// through.
		if r.StartedAt.Before(w.Until) && (r.FinishedAt == nil || r.FinishedAt.After(w.Until)) {
			rep.Runs.Live++
		}
	}
	if d := w.Duration(); d > 0 {
		rep.Runs.MeanConcurrent = float64(occupied) / float64(d)
	}
	rep.Runs.MaxConcurrent = in.MaxConcurrent
	if in.MaxConcurrent > 0 {
		rep.Runs.Utilization = rep.Runs.MeanConcurrent / float64(in.MaxConcurrent)
	}
	var completedTasks, completedAttempts int
	for _, t := range in.Tasks {
		if !w.holds(t.CompletedAt) {
			continue
		}
		completedTasks++
		completedAttempts += len(byTask[t.TaskID])
	}
	if completedTasks > 0 {
		rep.Runs.AttemptsPerCompletion = float64(completedAttempts) / float64(completedTasks)
	}

	// --- backlog ------------------------------------------------------
	if in.States != nil {
		rep.Backlog = backlogOf(in.Tasks, in.States, in.TurnsStopped, w.Until)
	}

	// --- the inside of a run ------------------------------------------
	rep.Tools = toolsOf(w, in.Runs, in.ToolUses)
	rep.Prompts = promptsOf(w, in.Runs, in.Prompts)
	rep.Checks = checksOf(w, in.Runs, in.CheckWaits)
	rep.PullRequests = pullRequestsOf(w, in.Tasks, in.Runs, in.ToolUses)
	rep.Agents = agentsOf(w, in.Tasks, in.Runs, in.ToolUses, in.APICalls, in.ReportChecks)

	// --- and the CI those runs waited on ------------------------------
	// Sliced by the same buckets Throughput is, so the two trend lines
	// are points over the same spans.
	rep.CIQueueing = ciQueueingOf(w, buckets, in.CIJobs)
	// And the CI beside it that grain does not run, from its own rows
	// only: nothing above reads them (githubci.go).
	rep.GitHubCI = gitHubCIOf(w, in.GitHubCIJobs, in.GitHubBaseBranches)

	// --- and what came out the far end --------------------------------
	// On the same buckets again: how much landed per slice belongs beside
	// how long the jobs above took, since the batches those jobs were run
	// for are what landed it (merges.go).
	rep.Merges = mergesOf(w, buckets, in.Tasks, in.MergeBatches, in.MergeBatchSize, in.Runs, in.ToolUses, in.Transcripts, in.MergeQueueEvents)
	rep.Merges.Rechecks = mergeRechecksOf(w, in.MergeRechecks)
	rep.Merges.BaseHolds = baseHoldsOf(w, in.BaseHolds)
	rep.Checks.Repairs = rep.Merges.Repairs
	rep.Checks.RedCI = redCIOf(w, in.Tasks, in.Runs, in.CIHabits)

	// --- and what was changed while all of that was happening ----------
	// On the same buckets again, which is the entire point of this
	// section: a series of numbers and a series of the changes made to the
	// deployment producing them, point for point (changes.go).
	rep.Changes = changesOf(w, buckets, in.SettingsHistory, in.SettingsChanges)

	// --- and the machine all of it ran on -----------------------------
	// On the same buckets again, which is what makes "the jobs got slower
	// because the host was full" a thing a reader can see rather than
	// infer (host.go).
	rep.Host = hostOf(w, buckets, in.HostSampled, in.Host)

	// --- the daemon's own tick ----------------------------------------
	rep.Cycles = cyclesOf(w, in.Cycles)
	return rep
}

// cyclesOf summarises the daemon's own ticks over the window.
//
// A tick belongs to the window on the same rule everything else here
// follows -- the moment it *ended* falls inside -- so a cycle that began
// before Since and was still running at it contributes in full, which is
// the case a report of slow ticks most wants to see.
//
// Interval is measured between consecutive ticks in the history rather
// than between consecutive ticks in the window, and attributed to the
// later of the two: the gap a tick waited is a fact about that tick, and
// dropping the pair whose earlier half fell outside the window would
// silently understate the very first interval in every report.
func cyclesOf(w Window, h CycleHistory) Cycles {
	out := Cycles{Observed: h.Observed, Truncated: h.Observed > len(h.Samples)}
	var tick, interval, dispatch []time.Duration
	var commits []int64
	// Reconcilers keep the order the cycle ran them in, which is the
	// order that explains them -- so they are collected in a slice, with
	// a map only to find the entry a name already has.
	type series struct {
		wait, duration []time.Duration
		failures       int
	}
	var names []string
	byName := map[string]*series{}

	for i, s := range h.Samples {
		end := s.Start.Add(s.Duration)
		if !w.holds(&end) {
			continue
		}
		out.N++
		if out.First.IsZero() || s.Start.Before(out.First) {
			out.First = s.Start
		}
		if s.Start.After(out.Last) {
			out.Last = s.Start
		}
		tick = appendIfPositive(tick, s.Duration)
		dispatch = appendIfPositive(dispatch, s.DispatchWait)
		commits = append(commits, int64(s.Commits))
		if s.Commits == 0 {
			out.CommitFree++
		}
		if i > 0 {
			interval = appendIfPositive(interval, s.Start.Sub(h.Samples[i-1].Start))
		}
		for _, r := range s.Reconcilers {
			entry, ok := byName[r.Name]
			if !ok {
				entry = &series{}
				byName[r.Name] = entry
				names = append(names, r.Name)
			}
			entry.wait = appendIfPositive(entry.wait, r.Wait)
			entry.duration = appendIfPositive(entry.duration, r.Duration)
			if r.Failed {
				entry.failures++
			}
		}
	}

	out.Tick = summarize(tick)
	out.Interval = summarize(interval)
	out.DispatchWait = summarize(dispatch)
	out.Commits = summarizeCounts(commits)
	for _, name := range names {
		entry := byName[name]
		out.Reconcilers = append(out.Reconcilers, ReconcilerCycles{
			Name:     name,
			Wait:     summarize(entry.wait),
			Duration: summarize(entry.duration),
			Failures: entry.failures,
		})
	}
	return out
}

// runsByTask groups attempts by their task, oldest first. The store
// already returns them in that order; sorting here anyway is what lets
// Compute's own "the run before this one" reasoning (RetryWait) hold for
// any caller, including a test that builds its input by hand.
func runsByTask(runs []model.RunTiming) map[string][]model.RunTiming {
	out := make(map[string][]model.RunTiming)
	for _, r := range runs {
		out[r.TaskID] = append(out[r.TaskID], r)
	}
	for _, list := range out {
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].StartedAt.Equal(list[j].StartedAt) {
				return list[i].Attempt < list[j].Attempt
			}
			return list[i].StartedAt.Before(list[j].StartedAt)
		})
	}
	return out
}

// backlogOf counts what was still in the system at until, by task_state's
// own vocabulary, and finds the queued task that has been waiting longest.
func backlogOf(tasks []model.TaskTiming, states map[string]model.State, stopped map[string]bool, until time.Time) Backlog {
	b := Backlog{ByState: map[model.State]int{}}
	for _, t := range tasks {
		state, ok := states[t.TaskID]
		if !ok || state == model.StateCompleted || state == model.StateAwaitingSubmit ||
			(state == model.StateAwaitingReply && t.CompletedAt != nil) || state == model.StateDeferred ||
			state == model.StateClosed {
			continue
		}
		b.ByState[state]++
		if stopped[t.TaskID] {
			b.StoppedByOperator++
		}
		if state != model.StateQueued {
			continue
		}
		b.Queued++
		// Queued since it was approved -- the same moment QueueWait
		// measures from, so "the oldest thing still waiting" and "how long
		// the things that started waited" are the same clock.
		//
		// Unless it was put aside and picked back up, in which case the
		// wait began when it came back (model.Task.UndeferredAt): a task
		// deferred for three months has not been waiting for three
		// months, and measuring it from its approval would hand
		// OldestQueuedTaskID to whichever task had been parked longest,
		// permanently, and say the backlog's oldest wait was that. This
		// is the same moment model.StateTimes.Since measures a queued
		// task from, so the row a stall flag lands on and the task named
		// here are the same task.
		since := t.ApprovedAt
		if t.UndeferredAt != nil && (since == nil || t.UndeferredAt.After(*since)) {
			since = t.UndeferredAt
		}
		if since == nil {
			since = t.CreatedAt
		}
		if since == nil {
			continue
		}
		if wait := until.Sub(*since); wait > b.OldestQueuedWait {
			b.OldestQueuedWait, b.OldestQueuedTaskID = wait, t.TaskID
		}
	}
	return b
}

// overlap is how much of one run's life fell inside the window. A run
// still live counts up to the window's end: it really was occupying
// capacity for all of that, and waiting for it to finish before counting
// any of it would make a deployment look idlest exactly when it is
// busiest.
func overlap(w Window, r model.RunTiming) time.Duration {
	start := r.StartedAt
	if start.Before(w.Since) {
		start = w.Since
	}
	end := w.Until
	if r.FinishedAt != nil && r.FinishedAt.Before(end) {
		end = *r.FinishedAt
	}
	if !end.After(start) {
		return 0
	}
	return end.Sub(start)
}

// outcomeOf names a finished run's outcome, giving the empty one a word
// of its own rather than letting it count under "".
func outcomeOf(r model.RunTiming) string {
	if r.Outcome == "" {
		return "unrecorded"
	}
	return r.Outcome
}

// appendIfPositive drops a negative or zero sample. Both mean the same
// thing -- two timestamps that cannot really be that far apart in that
// order, from a clock adjustment, a hand-edited row, or a moment recorded
// by two different writers -- and neither is a latency worth reporting.
func appendIfPositive(samples []time.Duration, d time.Duration) []time.Duration {
	if d <= 0 {
		return samples
	}
	return append(samples, d)
}

func appendIfNonNegative(samples []time.Duration, d time.Duration) []time.Duration {
	if d < 0 {
		return samples
	}
	return append(samples, d)
}

// newBuckets slices the window into n equal spans, oldest first.
func newBuckets(w Window, n int) []Bucket {
	if w.Duration() <= 0 {
		return nil
	}
	if n <= 0 {
		n = DefaultBuckets
	}
	if n > maxBuckets {
		n = maxBuckets
	}
	span := w.Duration() / time.Duration(n)
	if span <= 0 {
		return nil
	}
	out := make([]Bucket, 0, n)
	for i := 0; i < n; i++ {
		since := w.Since.Add(span * time.Duration(i))
		until := since.Add(span)
		if i == n-1 {
			// The last bucket ends where the window does, whatever the
			// integer division above left over.
			until = w.Until
		}
		out = append(out, Bucket{Since: since, Until: until})
	}
	return out
}

// addTo applies add to whichever bucket holds t. The caller has already
// established that t is inside the window, so a moment landing outside
// every bucket is arithmetic drift rather than a real case, and is
// dropped rather than forced into an end bucket.
func addTo(buckets []Bucket, w Window, t *time.Time, add func(*Bucket)) {
	if i := bucketOf(buckets, w, t); i >= 0 {
		add(&buckets[i])
	}
}

// bucketOf is which bucket holds t, or -1 for a moment that is in none of
// them -- see addTo, which is this with the assignment attached. It is
// separate so that a second series sliced the same way (CIQueueing.Buckets)
// lines up with Throughput's point for point rather than by writing the
// same arithmetic twice.
func bucketOf(buckets []Bucket, w Window, t *time.Time) int {
	if t == nil || len(buckets) == 0 {
		return -1
	}
	span := buckets[0].Until.Sub(buckets[0].Since)
	if span <= 0 {
		return -1
	}
	i := int(t.Sub(w.Since) / span)
	if i < 0 || i >= len(buckets) {
		return -1
	}
	return i
}

// summarize turns a sample set into a Distribution. It sorts a copy: the
// caller's slice is its own, and Compute reuses none of them anyway.
func summarize(samples []time.Duration) Distribution {
	d := Distribution{N: len(samples)}
	if d.N == 0 {
		return d
	}
	sorted := make([]time.Duration, len(samples))
	copy(sorted, samples)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	for _, s := range sorted {
		d.Total += s
	}
	d.Min, d.Max = sorted[0], sorted[len(sorted)-1]
	d.P50, d.P90, d.P99 = percentile(sorted, 0.50), percentile(sorted, 0.90), percentile(sorted, 0.99)
	d.Mean = d.Total / time.Duration(d.N)
	return d
}

// percentile is nearest-rank over an ascending slice: the smallest sample
// at or above the p'th of them. No interpolation, so every number a
// report shows is a duration something really took.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Ceil(p*float64(len(sorted)))) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}

// isExecTunnelFailure reports whether r is a run that never reached its
// agent because the cluster's exec transport failed (Runs.ExecTunnelFailures).
// A setup retry is recorded as a suspension (model.SuspendedOutcome),
// whose kind RunTiming does not carry; only a setup suspension's detail
// is a setup error, so the detail is what tells it from the rest.
func isExecTunnelFailure(r model.RunTiming) bool {
	setup := r.Outcome == model.SetupFailedOutcome || r.Outcome == model.SuspendedOutcome
	return setup && kubectlgate.IsTransientTransport(r.Detail)
}
