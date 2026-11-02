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

package workflows

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// This file is the one vocabulary for asking a narrower question of a
// list of runs: which repository, which ref, which result, which job,
// how far back, and in what order (grain/task-231).
//
// It lives in pkg/ci rather than in either of its readers because there
// are two of them and they answer to different people. The MCP tool
// (mcp.list_ci_runs) is what an agent asks, and GET /api/ci/runs is what
// the CI pane asks; an operator and an agent both looking at "failed
// runs on main" have to be looking at the same set and calling it by the
// same name, or the answer one of them reports is not one the other can
// check. The browser has its own copy of the same words in JavaScript
// (ui/src/ciFilters.js), which says so and names this file.
//
// Nothing here is about *keeping* runs. What a deployment remembers is
// RunStore's business and is bounded by its own history setting; this is
// only about which of the remembered ones a reader is asking after.
//
// Nor is anything here about *who may ask*. That is each reader's own
// question and each reader answers it before the filter is built: an
// agent's list_ci_runs and get_ci_log are scoped to the repository its
// task works in, which pkg/mcp's CIRepoScope enforces by pinning Repo
// and by refusing a call that names another repository (grain/task-172);
// the CI pane is an operator's view of the deployment and is scoped to
// nothing. So a filter that asks for one repository means "show me this
// one", and that a reader is *only* shown one is a fact about the reader
// rather than about these words -- which is why Matches has no notion of
// a caller at all, and must not grow one.

// RunState is the one word for how a run or a job stands right now: its
// conclusion, if it has reached one, and otherwise which kind of
// unfinished it is.
//
// Conclusion cannot say this on its own. Its zero value is "not finished"
// and covers two situations a reader has to be able to tell apart, and
// which the pane has always drawn as two different chips (CIPage.jsx's
// progressChip): a job on a machine and working, and a job that is still
// waiting for one. "What is running now, and what is waiting for a
// sandbox" is a question about this deployment's capacity rather than
// about any commit, and answering it used to mean watching the daemon
// log go past.
//
// So the filter vocabulary is Conclusion's five words plus these two.
// They are strings rather than a type of their own because that is what
// they are on the wire, in a query string and in a tool argument.
const (
	StateRunning = "running"
	StatePending = "pending"
)

// RunStates is the whole vocabulary, worst first -- the order a menu
// offers it in and the order a reader scans it in, which is the same
// order the pane already sorts jobs by (SortJobRuns).
var RunStates = []string{
	string(ConclusionFailure),
	string(ConclusionErrored),
	string(ConclusionCancelled),
	string(ConclusionSkipped),
	string(ConclusionSuccess),
	StateRunning,
	StatePending,
}

// RunStateOf is which of those words describes a run.
//
// A run enters the store when it is queued, so its own StartedAt is not
// the question -- it is set on a run that has not been given a machine
// yet. What separates running from pending is whether any of its jobs
// has actually begun.
func RunStateOf(run Run) string {
	if run.Conclusion.Done() {
		return string(run.Conclusion)
	}
	if run.Actions != nil && run.Actions.Status != "" {
		// A forge's run says itself whether it has started.
		if run.Actions.Status == "in_progress" {
			return StateRunning
		}
		return StatePending
	}
	for _, job := range run.Jobs {
		if job.Conclusion.Done() || job.StartedAt != nil {
			return StateRunning
		}
	}
	return StatePending
}

// JobStateOf is the same for one job, where StartedAt *is* the question:
// a job acquires one when it gets a machine, so a job with no start is
// one queueing for a CI slot or one whose `needs` have not finished.
func JobStateOf(job JobRun) string {
	if job.Conclusion.Done() {
		return string(job.Conclusion)
	}
	if job.StartedAt != nil {
		return StateRunning
	}
	return StatePending
}

// RunSortKey is what an ordered list of runs is ordered by.
//
// Three keys rather than one, because a list of runs is read for three
// different reasons. Started is the default and the obvious one: what
// happened, most recent first. Finished is not the same order whenever
// runs overlap, which on a deployment running several at once is most of
// the time -- "what landed a verdict last" is a different question from
// "what was set going last". Duration is the one that answers whether a
// job is flaky or a tree is broken: a forty-minute timeout and a
// two-minute assertion failure are the same red chip and very different
// problems, and sorting by how long it took is what separates them at a
// glance.
type RunSortKey string

const (
	SortRunsByStarted  RunSortKey = "started"
	SortRunsByFinished RunSortKey = "finished"
	SortRunsByDuration RunSortKey = "duration"
)

// RunSortKeys is the vocabulary, in the order a menu offers it.
var RunSortKeys = []RunSortKey{SortRunsByStarted, SortRunsByFinished, SortRunsByDuration}

// RunFilter is one question asked of a list of runs. Every field is
// optional and an empty one narrows nothing, so the zero value is "all
// of them, newest first" -- which is what the unfiltered call has always
// meant and goes on meaning.
//
// The fields compose: a run has to satisfy every one that is set. That
// is the whole of the rule, deliberately, because the alternative --
// a condition whose meaning depends on which other conditions are
// present -- is one neither a tool description nor a row of menus can
// state honestly.
type RunFilter struct {
	// Repo is a whole repository name ("acme/widgets"), matched without
	// regard to case. Not a prefix and not a substring: the name is what
	// every other part of grain calls the repository, and a reader who
	// half-remembers it is better served by leaving this out than by
	// getting an answer about two repositories at once.
	//
	// For a reader that is scoped to one repository this field is also
	// where that scope arrives: pkg/mcp's CIRepoScope sets it whether
	// the caller named a repository or not, so that the answer's own
	// heading says which repository it is about. Setting it is still all
	// it does -- the enforcing is the scope's, above this.
	Repo string
	// Ref is the branch, matched without regard to case and as a
	// substring, so "task-231" finds "grain/task-231". Substring because
	// a branch name here is long, is typed rather than picked, and is
	// most often remembered by its tail.
	Ref string
	// States are the words from RunStates a run may be in; a run in any
	// one of them matches. Several rather than one because the first
	// question anybody asks -- "what is red right now" -- is two of them,
	// failure and errored, and they are two words on purpose
	// (Conclusion's own doc comment on why).
	States []string
	// Job is a job name or job id, matched exactly and without regard to
	// case: a run matches when it has a job by that name at all,
	// whatever that job did. It is deliberately about the run and not
	// about the job's result -- "did this job fail here" is States on
	// top of it, and MatchingJobs is how a reader gets the job's own
	// result and duration onto the row.
	Job string
	// Since drops runs that started before it. A run that has not
	// started at all is kept: it is queued, so it cannot be older than
	// any window a reader is asking about, and dropping it would hide
	// exactly the run "what is waiting for a sandbox" is about.
	Since time.Time
	// Until drops runs that started at or after it, the other end of the
	// window Since opens. A run that has not started is dropped too: it
	// is queued now, so it is newer than any instant an audit of a past
	// window names.
	Until time.Time
	// Sort and Descending are the order: by started unless Sort says
	// otherwise, and oldest first unless Descending is set. Newest first
	// -- what a reader asking for "the latest run" means -- is
	// Descending: true. The zero value is *oldest* first, which is what
	// SortRuns has always done with it; this comment used to say the
	// opposite, and the callers that believed it read the oldest run of a
	// repository as its latest (grain/task-2388).
	Sort       RunSortKey
	Descending bool
	// Provider keeps the runs of one CI provider (ProviderGrain,
	// ProviderGitHubActions); "" keeps every provider's.
	Provider string
	// Visible, when set, is whether the reader asking may see a run on
	// this repository at all. It is not a question the reader asked but a
	// bound on who is asking -- pkg/ui's team read scope -- so it is
	// left out of Narrowed and Describe, and a run it refuses is not
	// counted in a page's Matched or Stored either: a hidden run is
	// absent, not filtered out.
	Visible func(repo string) bool
}

// Narrowed reports whether this filter asks anything at all -- the sort
// aside, which is an order rather than a question. It is what tells an
// empty answer whether to say "nothing matched" or "nothing has run".
func (f RunFilter) Narrowed() bool {
	return f.Repo != "" || f.Ref != "" || len(f.States) > 0 || f.Job != "" || !f.Since.IsZero() || !f.Until.IsZero() || f.Provider != ""
}

// Describe is the filter as a reader's own sentence, for the line an
// answer puts above itself: "repo acme/widgets, ref main, result failure
// or errored, job \"ui e2e\", started since 2026-09-17T08:00:00Z". Empty
// for a filter that asks nothing.
func (f RunFilter) Describe() string {
	var parts []string
	if f.Repo != "" {
		parts = append(parts, "repo "+f.Repo)
	}
	if f.Ref != "" {
		parts = append(parts, "ref "+f.Ref)
	}
	if len(f.States) > 0 {
		parts = append(parts, "result "+strings.Join(f.States, " or "))
	}
	if f.Job != "" {
		parts = append(parts, fmt.Sprintf("job %q", f.Job))
	}
	if f.Provider != "" {
		parts = append(parts, "provider "+f.Provider)
	}
	if !f.Since.IsZero() {
		parts = append(parts, "started since "+f.Since.UTC().Format(time.RFC3339))
	}
	if !f.Until.IsZero() {
		parts = append(parts, "started before "+f.Until.UTC().Format(time.RFC3339))
	}
	return strings.Join(parts, ", ")
}

// Matches is whether one run answers this filter.
func (f RunFilter) Matches(run Run) bool {
	if !f.SeesRepo(run.Repo) {
		return false
	}
	if f.Repo != "" && !strings.EqualFold(run.Repo, f.Repo) {
		return false
	}
	if f.Ref != "" && !strings.Contains(strings.ToLower(run.Ref), strings.ToLower(f.Ref)) {
		return false
	}
	if f.Provider != "" && !strings.EqualFold(ProviderOf(run), f.Provider) {
		return false
	}
	if len(f.States) > 0 {
		state := RunStateOf(run)
		found := false
		for _, want := range f.States {
			if strings.EqualFold(state, want) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if f.Job != "" && len(MatchingJobs(run, f.Job)) == 0 {
		return false
	}
	if !f.Since.IsZero() && run.StartedAt != nil && run.StartedAt.Before(f.Since) {
		return false
	}
	if !f.Until.IsZero() && (run.StartedAt == nil || !run.StartedAt.Before(f.Until)) {
		return false
	}
	return true
}

// SeesRepo is Visible, with a nil one seeing everything.
func (f RunFilter) SeesRepo(repo string) bool {
	if f.Visible == nil {
		return true
	}
	return f.Visible(repo)
}

// MatchingJobs is the jobs of one run a job filter names -- normally one,
// since a job id is unique within a workflow, but two workflows may each
// declare a job called "tests" and a reader asking about "tests" means
// both.
//
// It is exported because the filter is only half of what a reader wants:
// having narrowed to one job across twenty runs, the thing worth putting
// on each row is that job's own result and how long it took, not the
// run's.
func MatchingJobs(run Run, name string) []JobRun {
	if name == "" {
		return nil
	}
	var out []JobRun
	for _, job := range run.Jobs {
		if strings.EqualFold(job.Name, name) || strings.EqualFold(job.JobID, name) {
			out = append(out, job)
		}
	}
	return out
}

// FilterRuns is Matches over a list, in the order they arrived.
func FilterRuns(runs []Run, f RunFilter) []Run {
	out := make([]Run, 0, len(runs))
	for _, run := range runs {
		if f.Matches(run) {
			out = append(out, run)
		}
	}
	return out
}

// SortRuns puts runs in the order a filter asks for, leaving the input
// alone.
//
// Stable, and that is load-bearing rather than incidental. Runs started
// in the same second -- a merge queue setting three going at once, or a
// store whose timestamps are whole seconds -- compare equal, and a sort
// that reordered them would shuffle the list under a reader between two
// five-second polls of the same unchanged data. Equal keys keep the
// order the store handed them over in, which is the order everything
// else about this list already means.
//
// A run with nothing to compare -- no start, or no finish when the order
// is by finish -- sorts last whichever way the order points, the same
// rule the browser's own sort follows (taskFilters.js's comparatorFor).
// It is the honest place for it: a run that has not finished is not the
// longest-ago finisher, and burying it at the top of "oldest finish
// first" would put the thing nobody asked about above the thing they
// did.
func SortRuns(runs []Run, f RunFilter, now time.Time) []Run {
	out := append([]Run(nil), runs...)
	key := func(run Run) (float64, bool) {
		switch f.Sort {
		case SortRunsByFinished:
			if run.FinishedAt == nil {
				return 0, false
			}
			return float64(run.FinishedAt.UnixNano()), true
		case SortRunsByDuration:
			if run.StartedAt == nil {
				return 0, false
			}
			return float64(run.Duration(now)), true
		default:
			if run.StartedAt == nil {
				return 0, false
			}
			return float64(run.StartedAt.UnixNano()), true
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		x, xok := key(out[i])
		y, yok := key(out[j])
		if !xok || !yok {
			// Missing sorts last both ways, so it is compared before the
			// direction is applied rather than after it.
			return xok && !yok
		}
		if x == y {
			return false
		}
		if f.Descending {
			return x > y
		}
		return x < y
	})
	return out
}

// ParseRunState checks one word of the state vocabulary, so that a
// reader who asks for "failed" is told the word is "failure" rather than
// handed an empty list to puzzle over. An unknown word is the single
// easiest way to get an answer that looks like "nothing is wrong".
func ParseRunState(word string) (string, error) {
	for _, known := range RunStates {
		if strings.EqualFold(word, known) {
			return known, nil
		}
	}
	return "", fmt.Errorf("%q is not a result: use one of %s", word, strings.Join(RunStates, ", "))
}

// ParseRunSortKey is the same for the order.
func ParseRunSortKey(word string) (RunSortKey, error) {
	for _, known := range RunSortKeys {
		if strings.EqualFold(word, string(known)) {
			return known, nil
		}
	}
	if strings.EqualFold(word, "completed") {
		return SortRunsByFinished, nil
	}
	words := make([]string, 0, len(RunSortKeys))
	for _, known := range RunSortKeys {
		words = append(words, string(known))
	}
	return "", fmt.Errorf("%q is not an order: use one of %s", word, strings.Join(words, ", "))
}

// ParseSince reads the "how far back" argument, which is two spellings
// of one thing: a span ending now ("24h", "7d", "90m"), and an instant
// (2026-09-17, or a whole RFC 3339 timestamp).
//
// Both, because the two readers ask it differently. A person narrowing a
// pane means "the last day"; something reporting on a window it already
// knows the edges of means the edge. Go's own ParseDuration does not
// know "d" -- a day is not a fixed span to it -- but a reader asking for
// seven days of CI history means seven twenty-four-hour days and would
// otherwise have to write 168h, so it is spelled out here.
func ParseSince(text string, now time.Time) (time.Time, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return time.Time{}, nil
	}
	if rest, ok := strings.CutSuffix(text, "d"); ok {
		days, err := time.ParseDuration(rest + "h")
		if err == nil {
			return now.Add(-days * 24), nil
		}
	}
	if span, err := time.ParseDuration(text); err == nil {
		if span < 0 {
			span = -span
		}
		return now.Add(-span), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if at, err := time.Parse(layout, text); err == nil {
			return at, nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is neither a span back from now (24h, 7d, 90m) nor a date (2026-09-17, or an RFC 3339 timestamp)", text)
}
