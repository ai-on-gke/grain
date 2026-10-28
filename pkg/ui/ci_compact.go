package ui

import (
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bwsalmon/grain/pkg/ci"
)

// ciCompactErrorLen bounds the one line of a job's error the compact view
// carries: enough to say what went wrong, short enough that a page of
// fifty runs still fits an agent's tool-result budget.
const ciCompactErrorLen = 200

// CICompactRunsResponse is what GET /api/ci/runs?view=compact answers
// with: the listing with everything an audit does not read taken out.
//
// The full listing carries every job's steps (without their output, but
// with their names, timings and exit codes) and the whole of every
// shared fault, run list and all. Over a week of merge-queue runs that is
// about 10 KB of steps a run and 6 KB of faults a page, and a reader
// whose answer is capped -- an agent's tool result -- was cut off before
// it reached the one thing it came for: which jobs failed (grain/task-975).
// So each run here is its identity, its verdict and, for the jobs that
// did not succeed, a line each saying where they failed.
type CICompactRunsResponse struct {
	// Enabled, Matched and Stored mean what they do in CIRunsResponse.
	Enabled bool           `json:"enabled"`
	Runs    []CICompactRun `json:"runs"`
	Matched int            `json:"matched"`
	Stored  int            `json:"stored"`
	// Faults are the shared faults some job on this page is part of,
	// each once and without its run list: a job names the one it belongs
	// to by Digest (CICompactJob.Fault), and GET /api/ci/faults has the
	// rest of one.
	Faults []CICompactFault `json:"faults,omitempty"`
}

// CICompactRun is one run of the compact listing.
type CICompactRun struct {
	ID     string   `json:"id"`
	Repo   string   `json:"repo"`
	Ref    string   `json:"ref"`
	Commit string   `json:"commit,omitempty"`
	Event  ci.Event `json:"event"`
	// BaseCommit is the base branch's commit the run's jobs were tested
	// against, where they say (ci.JobRun.BaseCommit) -- the first job
	// that names one, since a run's jobs share their base.
	BaseCommit  string     `json:"baseCommit,omitempty"`
	RequestedBy string     `json:"requestedBy,omitempty"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
	// Conclusion is the run's state in list_ci_runs' words
	// (ci.RunStateOf), so a run still going reads "running" or "pending"
	// rather than as a missing field.
	Conclusion string `json:"conclusion"`
	// Error is the first line of the run's own error, if it has one --
	// a run that never got as far as a job has nothing in Jobs to say why.
	Error string `json:"error,omitempty"`
	// Jobs are the jobs that did not succeed: failed, errored, cancelled
	// or still unfinished. A job skipped because something it needed
	// failed is left out -- the job it needed is here already.
	Jobs []CICompactJob `json:"jobs,omitempty"`
}

// CICompactJob is one job that did not succeed, in a line.
type CICompactJob struct {
	Name       string        `json:"name"`
	Conclusion ci.Conclusion `json:"conclusion,omitempty"`
	// Step is the first step that failed, if one did.
	Step string `json:"step,omitempty"`
	// Test is the first failing test the job's output named, where the
	// output could be read for one (ci.FailureSummary).
	Test string `json:"test,omitempty"`
	// Error is the first line of the job's own error, or of the failing
	// step's, bounded at ciCompactErrorLen.
	Error string `json:"error,omitempty"`
	Flake bool   `json:"flake,omitempty"`
	// Fault is the Digest of the shared fault this job's failure is part
	// of, one of the response's Faults.
	Fault string `json:"fault,omitempty"`
}

// CICompactFault is a shared fault named once for the page: what it is
// and how widely it has been seen, without the runs it was seen in.
type CICompactFault struct {
	Digest      string        `json:"digest"`
	Repo        string        `json:"repo"`
	JobName     string        `json:"jobName"`
	Step        string        `json:"step,omitempty"`
	Kind        ci.Conclusion `json:"kind"`
	Refs        int           `json:"refs"`
	Runs        int           `json:"runs"`
	OwnerTaskID string        `json:"ownerTaskId,omitempty"`
}

// ciRunViewFrom reads ?view=: "full" (or nothing) for the listing as it
// has always been, "compact" for CICompactRunsResponse. Anything else is
// refused, for the reason an unknown filter word is.
func ciRunViewFrom(query url.Values) (compact bool, err error) {
	switch strings.ToLower(strings.TrimSpace(query.Get("view"))) {
	case "", "full":
		return false, nil
	case "compact":
		return true, nil
	default:
		return false, fmt.Errorf("%q is not a view: use full (the default) or compact", query.Get("view"))
	}
}

// ciCompactRuns is a page of runs, and the faults shared across the
// deployment, as the compact listing.
func ciCompactRuns(page ci.RunPage, faults []CIFault) CICompactRunsResponse {
	type faultKey struct{ run, job string }
	faultOf := map[faultKey]int{}
	for i, fault := range faults {
		for _, run := range fault.Runs {
			faultOf[faultKey{run.RunID, fault.JobID}] = i
		}
	}
	resp := CICompactRunsResponse{Enabled: true, Runs: make([]CICompactRun, 0, len(page.Runs)),
		Matched: page.Matched, Stored: page.Stored}
	used := map[int]bool{}
	var order []int
	for _, run := range page.Runs {
		out := CICompactRun{
			ID: run.ID, Repo: run.Repo, Ref: run.Ref, Commit: run.Commit, Event: run.Event,
			RequestedBy: run.RequestedBy, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt,
			Conclusion: ci.RunStateOf(run), Error: compactLine(run.Error),
		}
		for _, job := range run.Jobs {
			if out.BaseCommit == "" {
				out.BaseCommit = job.BaseCommit
			}
			if job.Conclusion == ci.ConclusionSuccess || job.Conclusion == ci.ConclusionSkipped {
				continue
			}
			line := compactJob(job)
			if i, ok := faultOf[faultKey{run.ID, job.JobID}]; ok {
				line.Fault = faults[i].Digest
				if !used[i] {
					used[i] = true
					order = append(order, i)
				}
			}
			out.Jobs = append(out.Jobs, line)
		}
		resp.Runs = append(resp.Runs, out)
	}
	for _, i := range order {
		f := faults[i]
		resp.Faults = append(resp.Faults, CICompactFault{
			Digest: f.Digest, Repo: f.Repo, JobName: f.JobName, Step: f.Step, Kind: f.Kind,
			Refs: len(f.Refs), Runs: len(f.Runs), OwnerTaskID: f.OwnerTaskID,
		})
	}
	return resp
}

// compactJob is one job that did not succeed as its line.
func compactJob(job ci.JobRun) CICompactJob {
	out := CICompactJob{Name: job.Name, Conclusion: job.Conclusion, Flake: job.Flake}
	if out.Name == "" {
		out.Name = job.JobID
	}
	stepErr := ""
	for _, step := range job.Steps {
		if step.Conclusion == ci.ConclusionFailure || step.Conclusion == ci.ConclusionErrored {
			out.Step = step.Name
			stepErr = step.Error
			break
		}
	}
	if job.FailureSummary != nil {
		for _, test := range job.FailureSummary.Tests {
			if !test.Quarantined {
				out.Test = test.Name
				break
			}
		}
	}
	out.Error = compactLine(job.Error)
	if out.Error == "" {
		out.Error = compactLine(stepErr)
	}
	return out
}

// compactLine is the first non-blank line of s, bounded at
// ciCompactErrorLen.
func compactLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > ciCompactErrorLen {
			cut := ciCompactErrorLen
			for cut > 0 && !utf8.RuneStart(line[cut]) {
				cut--
			}
			line = line[:cut] + "…"
		}
		return line
	}
	return ""
}
