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
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Reading a shared fault out of a run store, and saying it in a sentence.
//
// sharedfault.go decides what one fault is and refuses to guess; this
// file is everything around that decision which more than one reader
// needs and none of them should write twice: which runs are worth
// reading back to prove a fault, how a reader finds its own job in the
// answer, and the one sentence a fault is said in.
//
// Three readers now. The CI pane and a task's own pane (pkg/ui), the
// verdict grain comments on a pull request (pkg/orchestrator), and the
// tools an agent reads a red build through (pkg/mcp, over the daemon's
// API). They were told about the same afternoon at different times and
// in different words before this file existed, which is how the agent
// half came to be told nothing at all (grain/task-299).

// DefaultFaultRunLimit bounds how many failing runs one grouping reads
// back whole. A deployment that has been red all day has hundreds of
// them; a fault is provable from the recent ones, and the bound is what
// keeps "is this red mine?" from turning into a table scan on the path
// of an ordinary poll.
const DefaultFaultRunLimit = 60

// FaultRunSource is what grouping needs of a run store: the listing, to
// find which runs failed recently, and one run in full, because a
// listing carries no step output (RunStore.List drops it) and output is
// the whole of the evidence a fault is claimed from.
//
// *Service satisfies it, and so does cmd/grain's ciGate -- both halves
// of a method set every caller here already holds.
type FaultRunSource interface {
	List() []Run
	Get(id string) (Run, bool)
}

// IsConcludedRun reports whether run and all of its jobs have concluded,
// meaning its step outputs and failure signatures will not change.
func IsConcludedRun(run Run) bool {
	if !run.Conclusion.Done() && run.FinishedAt == nil {
		return false
	}
	for _, j := range run.Jobs {
		if !j.Conclusion.Done() && j.FinishedAt == nil {
			return false
		}
	}
	return true
}

// ClassifyFaultRun precomputes FailureSignature for each job on run and
// drops step outputs, artifacts, and failure summaries so the classified
// Run can be cached in memory with minimal footprint.
func ClassifyFaultRun(run Run) Run {
	out := run
	if len(run.Jobs) > 0 {
		out.Jobs = make([]JobRun, len(run.Jobs))
		for i, job := range run.Jobs {
			sig, ok := FailureSignature(run, job)
			slim := job
			slim.Steps = nil
			slim.Artifacts = nil
			slim.FailureSummary = nil
			var cached *Signature
			if ok {
				s := sig
				cached = &s
			}
			slim.SetFaultSignatureCache(cached)
			out.Jobs[i] = slim
		}
	}
	return out
}

var (
	faultSourceCacheMu sync.Mutex
	faultSourceCaches  = map[any]map[string]Run{}
)

func canKeyFaultSource(src FaultRunSource) bool {
	if src == nil {
		return false
	}
	v := reflect.ValueOf(src)
	return v.Kind() == reflect.Ptr && !v.IsNil()
}

// RecentSharedFaults is SharedFaults fed from a run store: the faults
// this deployment's CI is showing on more than one branch inside window,
// newest first.
//
// Only runs that failed inside the window are read back in full, newest
// first and at most limit of them (zero meaning DefaultFaultRunLimit). A
// nil source, or one that has nothing recent, is no faults -- absence
// here is silence, never an error, because every caller is annotating a
// report that is worth giving without it.
func RecentSharedFaults(src FaultRunSource, now time.Time, window time.Duration, limit int) []SharedFault {
	if src == nil {
		return nil
	}
	runs := src.List()
	keyable := canKeyFaultSource(src)
	var active map[string]bool
	if keyable {
		active = make(map[string]bool, len(runs))
		for _, r := range runs {
			active[r.ID] = true
		}
		faultSourceCacheMu.Lock()
		if cache := faultSourceCaches[src]; cache != nil {
			for id := range cache {
				if !active[id] {
					delete(cache, id)
				}
			}
		}
		faultSourceCacheMu.Unlock()
	}
	return RecentSharedFaultsFromRuns(runs, func(id string) (Run, bool) {
		if keyable {
			faultSourceCacheMu.Lock()
			cache := faultSourceCaches[src]
			if cache != nil {
				if cached, ok := cache[id]; ok {
					faultSourceCacheMu.Unlock()
					return cached, true
				}
			}
			faultSourceCacheMu.Unlock()
		}
		run, ok := src.Get(id)
		if !ok {
			return Run{}, false
		}
		if keyable && IsConcludedRun(run) {
			slim := ClassifyFaultRun(run)
			faultSourceCacheMu.Lock()
			if len(faultSourceCaches) > 64 {
				faultSourceCaches = map[any]map[string]Run{}
			}
			cache := faultSourceCaches[src]
			if cache == nil {
				cache = map[string]Run{}
				faultSourceCaches[src] = cache
			}
			cache[id] = slim
			faultSourceCacheMu.Unlock()
			return slim, true
		}
		return run, true
	}, now, window, limit)
}

// RecentSharedFaultsFromRuns computes RecentSharedFaults from an already-listed
// slice of run summaries without calling List() again, invoking get(id) only
// for runs that failed within window (up to limit).
func RecentSharedFaultsFromRuns(runs []Run, get func(string) (Run, bool), now time.Time, window time.Duration, limit int) []SharedFault {
	if len(runs) == 0 || get == nil {
		return nil
	}
	if window <= 0 {
		window = DefaultFaultWindow
	}
	if limit <= 0 {
		limit = DefaultFaultRunLimit
	}
	full := make([]Run, 0, limit)
	for _, summary := range runs {
		if len(full) >= limit {
			break
		}
		if !RecentlyFailed(summary, now, window) {
			continue
		}
		run, ok := get(summary.ID)
		if !ok {
			continue
		}
		full = append(full, run)
	}
	return SharedFaults(full, now, window)
}

// RecentlyFailed is the cheap test a listing can answer: did any job of
// this run fail inside the window?
//
// Deliberately looser than FailureSignature, which is the test that
// decides anything -- this one only picks which runs are worth the read
// that makes the real test possible.
func RecentlyFailed(run Run, now time.Time, window time.Duration) bool {
	if run.Ref == "" {
		return false
	}
	if window <= 0 {
		window = DefaultFaultWindow
	}
	for _, job := range run.Jobs {
		if job.Conclusion != ConclusionFailure && job.Conclusion != ConclusionErrored {
			continue
		}
		at := job.FinishedAt
		if at == nil {
			at = run.FinishedAt
		}
		if at != nil && now.Sub(*at) <= window {
			return true
		}
	}
	return false
}

// FaultForJob is the fault one job of one run belongs to: FaultFor where
// the fault list already knows the run, and the job's own signature
// where it does not.
//
// The second half is for the reader whose run is newer than the list.
// An agent calling wait_for_checks is told about a run that concluded
// seconds ago, while the faults it is matched against are computed
// behind a cache and over a bounded page of the store -- so the run
// which prompted the question is exactly the one most likely to be
// missing from the answer. Matching on the digest costs nothing in
// precision: it is the same digest, taken by the same code over the same
// normalized tail (FailureSignature), and a fault only exists at all
// once two *other* refs have proved it.
//
// The run must carry its steps' output for the second half to fire; a
// listing's run simply falls back to no fault, which is silence.
func FaultForJob(faults []SharedFault, run Run, job JobRun) (SharedFault, bool) {
	if fault, ok := FaultFor(faults, run.ID, job.JobID); ok && faultSourceOf(fault.Source) == faultSourceOf(run.Source) {
		return fault, true
	}
	sig, ok := FailureSignature(run, job)
	if !ok {
		return SharedFault{}, false
	}
	for _, fault := range faults {
		if fault.Digest == sig.Digest && faultSourceOf(fault.Source) == faultSourceOf(run.Source) {
			return fault, true
		}
	}
	return SharedFault{}, false
}

// GitHubFaultRun converts one failed model.GitHubCIJob row that has a
// FailureTail into the Run input SharedFaults reads: JobID = j.JobKey()
// (workflow / job), Step = j.FailingStep, and j.FailureTail as the failing
// step's output.
func GitHubFaultRun(j model.GitHubCIJob) (Run, bool) {
	if !j.Failed() {
		return Run{}, false
	}
	tail := strings.TrimSpace(j.FailureTail)
	repo := strings.TrimSpace(j.Repo)
	ref := strings.TrimPrefix(strings.TrimSpace(j.Ref), "refs/heads/")
	key := j.JobKey()
	if tail == "" || repo == "" || ref == "" || key == "" {
		return Run{}, false
	}
	finished := githubJobTime(j)
	var finishedPtr *time.Time
	if !finished.IsZero() {
		t := finished
		finishedPtr = &t
	}
	return Run{
		ID:         strconv.FormatInt(j.CheckRunID, 10),
		Source:     FaultSourceGitHub,
		Repo:       repo,
		Ref:        ref,
		Commit:     strings.TrimSpace(j.Commit),
		StartedAt:  j.StartedAt,
		FinishedAt: finishedPtr,
		Conclusion: ConclusionFailure,
		Jobs: []JobRun{{
			JobID:      key,
			Name:       key,
			StartedAt:  j.StartedAt,
			FinishedAt: finishedPtr,
			Conclusion: ConclusionFailure,
			Steps: []StepRun{{
				Name:       strings.TrimSpace(j.FailingStep),
				Conclusion: ConclusionFailure,
				ExitCode:   1,
				Output:     j.FailureTail,
			}},
		}},
	}, true
}

// GitHubFaultRuns converts model.GitHubCIJob rows into the Run slice
// SharedFaults reads. For each (repo, ref, jobKey) only the newest row is
// considered, so a branch whose later run of the same job passed is no longer
// counted in the fault its earlier commit hit.
func GitHubFaultRuns(jobs []model.GitHubCIJob) []Run {
	type jobSlot struct {
		repo string
		ref  string
		key  string
	}
	latest := make(map[jobSlot]model.GitHubCIJob, len(jobs))
	order := make([]jobSlot, 0, len(jobs))
	for _, j := range jobs {
		repo := strings.TrimSpace(j.Repo)
		ref := strings.TrimPrefix(strings.TrimSpace(j.Ref), "refs/heads/")
		key := j.JobKey()
		if repo == "" || ref == "" || key == "" {
			continue
		}
		slot := jobSlot{repo: repo, ref: ref, key: key}
		prev, exists := latest[slot]
		if !exists {
			latest[slot] = j
			order = append(order, slot)
			continue
		}
		if githubJobNewer(j, prev) {
			latest[slot] = j
		}
	}
	out := make([]Run, 0, len(order))
	for _, slot := range order {
		if run, ok := GitHubFaultRun(latest[slot]); ok {
			out = append(out, run)
		}
	}
	return out
}

// GitHubSharedFaults runs SharedFaults over recent failed GitHubCIJob rows.
func GitHubSharedFaults(jobs []model.GitHubCIJob, now time.Time, window time.Duration) []SharedFault {
	runs := GitHubFaultRuns(jobs)
	if len(runs) == 0 {
		return nil
	}
	return SharedFaults(runs, now, window)
}

func githubJobTime(j model.GitHubCIJob) time.Time {
	if j.CompletedAt != nil && !j.CompletedAt.IsZero() {
		return *j.CompletedAt
	}
	if !j.RecordedAt.IsZero() {
		return j.RecordedAt
	}
	return j.UpdatedAt
}

func githubJobNewer(a, b model.GitHubCIJob) bool {
	ta, tb := githubJobTime(a), githubJobTime(b)
	if !ta.Equal(tb) {
		return ta.After(tb)
	}
	return a.CheckRunID > b.CheckRunID
}

// Summary is the sentence a fault is said in, from the point of view of
// one branch: the same one the CI pane and a task's pane show
// (ui/src/ciFaults.js faultSummary), so that an operator reading a row
// and an agent reading wait_for_checks are quoting one claim rather than
// two descriptions of it.
//
// It names the job, because a branch whose `image` job is inherited-red
// and whose `go` job is genuinely red must not be told its build is
// fine. It counts *other* branches, so the sentence reads as a claim
// about elsewhere -- and it is empty where there are none, which is the
// rule the whole feature rests on: one branch failing is a branch with a
// problem, and saying anything else about it would be the annotation
// lying in the one direction it cannot afford.
func (f SharedFault) Summary(ref string) string {
	others := f.Others(ref)
	if len(others) == 0 {
		return ""
	}
	job := f.JobName
	if job == "" {
		job = f.JobID
	}
	branches := fmt.Sprintf("%d other branches", len(others))
	if len(others) == 1 {
		branches = "1 other branch"
	}
	return fmt.Sprintf("%s is failing the same way on %s right now -- one fault, not this branch's.", job, branches)
}

// maxNamedFaultRefs is how many of the other branches a report names
// before it counts the rest. The names are evidence a reader can go and
// check; past a handful they are a wall of task ids, and the number was
// always the part that mattered.
const maxNamedFaultRefs = 5

// OtherRefsPhrase is those branches as a report lists them, bounded --
// "grain/task-246, grain/task-251 and 4 more". Empty where the reader's
// own branch is the only one, for Summary's reason.
func (f SharedFault) OtherRefsPhrase(ref string) string {
	others := f.Others(ref)
	if len(others) == 0 {
		return ""
	}
	if len(others) <= maxNamedFaultRefs {
		return strings.Join(others, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(others[:maxNamedFaultRefs], ", "), len(others)-maxNamedFaultRefs)
}
