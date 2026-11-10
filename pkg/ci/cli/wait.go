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

package cli

// wait.go is what the external `grain ci wait` and `grain ci retrigger`
// decide (pkg/cli's cicmd.go is their flags and printing,
// design/spec/external-cli/external-cli.spec.md #XC-26): which run a
// branch's wait attaches to, when a wait has its verdict and what it is,
// which of a run's failures are shared faults, and which jobs a
// retrigger reruns.

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// WaitResult is `grain ci wait`'s answer. It is the sandbox proxy's own
// answer to the in-sandbox `grain ci wait` (gitproxy.CLIWaitResponse,
// design/spec/cli/sandbox.spec.md), so a script reading -json reads the
// same fields on either side of the sandbox wall -- with the repository
// and branch the run was on, and each failing job's details and shared
// fault, beside it.
//
// The wait-credit fields are always zero: credit is what a sandbox
// run's deadline is extended by while it blocks (RunWaitCredits), and a
// terminal has no attempt deadline to extend.
type WaitResult struct {
	gitproxy.CLIWaitResponse
	Repo    string        `json:"repo,omitempty"`
	Ref     string        `json:"ref,omitempty"`
	Failing []WaitFailure `json:"failing,omitempty"`
}

// WaitFailure is one job that did not pass, and why.
type WaitFailure struct {
	Name       string `json:"name"`
	JobID      string `json:"jobId,omitempty"`
	Conclusion string `json:"conclusion"`
	// Step is the first step that failed, where one did.
	Step  string `json:"step,omitempty"`
	Error string `json:"error,omitempty"`
	// Tests are the failing tests the job's failure summary names.
	Tests []string `json:"tests,omitempty"`
	// SharedFault is set where this failure is one other branches have
	// too (GET /api/ci/faults): then it is not the branch's own to fix.
	SharedFault *ui.CIFault `json:"sharedFault,omitempty"`
}

// ParsePullRequest reads a pull request written as its GitHub URL
// (https://github.com/owner/name/pull/n) or as owner/name#n.
func ParsePullRequest(s string) (model.PullRequestRef, error) {
	s = strings.TrimSpace(s)
	if u, err := url.Parse(s); err == nil && u.Scheme != "" {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 4 || parts[2] != "pull" {
			return model.PullRequestRef{}, fmt.Errorf("%q is not a pull request URL (.../<owner>/<name>/pull/<n>)", s)
		}
		s = parts[0] + "/" + parts[1] + "#" + parts[3]
	}
	return model.ParsePullRequestRef(s)
}

// TaskBranchFor is the branch of the grain task that opened pr or is
// tracking it, which is the branch its CI runs on.
func TaskBranchFor(tasks []ui.Task, pr model.PullRequestRef) (string, bool) {
	for _, t := range tasks {
		if t.Branch == "" {
			continue
		}
		for _, s := range append([]string{t.PullRequest}, t.ExternalPullRequests...) {
			if got, err := model.ParsePullRequestRef(s); err == nil && model.SamePullRequest(got, pr) {
				return t.Branch, true
			}
		}
	}
	return "", false
}

// NewestRunOn is the run a wait on repo's branch ref attaches to, out of
// runs newest first: the newest still in flight, else the newest at all.
// A listing's ref filter matches part of a name, so the branch is
// matched here exactly.
func NewestRunOn(runs []ci.Run, repo, ref string) (string, bool) {
	newest := ""
	for _, run := range runs {
		if run.Ref != ref || !strings.EqualFold(run.Repo, repo) {
			continue
		}
		if !run.Conclusion.Done() {
			return run.ID, true
		}
		if newest == "" {
			newest = run.ID
		}
	}
	return newest, newest != ""
}

// PollWait asks src after run id every interval until the wait for jobs
// is settled (WaitSettled) or ctx ends, and returns the last snapshot it
// had -- a run still going, when it was ctx that ended it.
func PollWait(ctx context.Context, src RunSource, id string, jobs []string, interval time.Duration) (ci.Run, error) {
	var last ci.Run
	for {
		run, err := src.CIRun(ctx, id)
		if err != nil {
			if ctx.Err() != nil && last.ID != "" {
				return last, nil
			}
			return ci.Run{}, err
		}
		last = run
		if WaitSettled(run, jobs) {
			return run, nil
		}
		select {
		case <-ctx.Done():
			return last, nil
		case <-time.After(interval):
		}
	}
}

// WaitSettled is whether a wait for jobs (every job, when empty) has its
// verdict in run: the run is over, or every job asked about is.
func WaitSettled(run ci.Run, jobs []string) bool {
	if run.Conclusion.Done() {
		return true
	}
	if len(jobs) == 0 || len(run.Jobs) == 0 {
		return false
	}
	for _, name := range jobs {
		i := slices.IndexFunc(run.Jobs, func(job ci.JobRun) bool { return JobNamed(job, name) })
		if i < 0 || !run.Jobs[i].Conclusion.Done() {
			return false
		}
	}
	return true
}

// WaitVerdict is what run says to a wait for jobs, waited after waiting
// for it: the same verdict the sandbox proxy gives (gitproxy's
// serveCIWait), so "passed" is green and anything else finished is red.
func WaitVerdict(run ci.Run, jobs []string, waited time.Duration) WaitResult {
	res := WaitResult{
		CLIWaitResponse: gitproxy.CLIWaitResponse{
			RunID:      run.ID,
			Commit:     run.Commit,
			Conclusion: string(run.Conclusion),
			Error:      run.Error,
			Selective:  len(jobs) > 0,
			WaitedMS:   waited.Milliseconds(),
			Jobs:       []gitproxy.CLIJobVerdict{},
		},
		Repo: run.Repo,
		Ref:  run.Ref,
	}
	var considered []ci.JobRun
	for _, job := range ci.SortJobRuns(run.Jobs) {
		if len(jobs) > 0 && !slices.ContainsFunc(jobs, func(name string) bool { return JobNamed(job, name) }) {
			continue
		}
		considered = append(considered, job)
		verdict := string(job.Conclusion)
		if verdict == "" {
			verdict = "running"
		}
		res.Jobs = append(res.Jobs, gitproxy.CLIJobVerdict{Name: job.Name, Conclusion: verdict})
	}
	switch {
	case !WaitSettled(run, jobs):
		res.Status, res.ResumeRunID = "running", run.ID
		return res
	case len(jobs) > 0:
		// Every job asked about has finished, and they alone are the
		// answer, whatever the rest of the run did or is still doing.
		res.Status, res.Conclusion, res.Error = "passed", string(ci.ConclusionSuccess), ""
		for _, job := range considered {
			if !JobPassed(job) {
				res.Status, res.Conclusion = "failed", string(ci.ConclusionFailure)
			}
		}
		// A job asked about that the run never had is no verdict at
		// all: a mistyped -jobs must not read as green.
		var missing []string
		for _, name := range jobs {
			if !slices.ContainsFunc(run.Jobs, func(job ci.JobRun) bool { return JobNamed(job, name) }) {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			res.Status, res.Conclusion = "failed", string(ci.ConclusionFailure)
			res.Error = "run " + run.ID + " has no job named " + strings.Join(missing, ", ")
		}
	case run.Conclusion == ci.ConclusionSuccess:
		res.Status = "passed"
	default:
		res.Status = "failed"
		if run.Conclusion == ci.ConclusionCancelled {
			res.CancelReason = strings.TrimSpace(run.CancelReason)
			res.CancelledByHand = run.CancelledBy.ByHand()
		}
	}
	if res.Status != "failed" {
		return res
	}
	for _, job := range considered {
		if !job.Conclusion.Done() || JobPassed(job) {
			continue
		}
		f := WaitFailure{Name: job.Name, JobID: job.JobID, Conclusion: string(job.Conclusion), Error: job.Error}
		for _, step := range job.Steps {
			if step.Conclusion == ci.ConclusionFailure || step.Conclusion == ci.ConclusionErrored {
				f.Step = step.Name
				break
			}
		}
		if s := job.FailureSummary; s != nil {
			for _, test := range s.Tests {
				if !test.Quarantined {
					f.Tests = append(f.Tests, test.Name)
				}
			}
		}
		res.Failing = append(res.Failing, f)
	}
	return res
}

// JobPassed is whether a finished job counts as green.
func JobPassed(job ci.JobRun) bool {
	return job.Conclusion == ci.ConclusionSuccess || job.Conclusion == ci.ConclusionSkipped
}

// JobNamed is whether job is the one name means: its display name or
// its id, as the sandbox's --jobs reads them.
func JobNamed(job ci.JobRun, name string) bool {
	return job.Name == name || (job.JobID != "" && job.JobID == name)
}

// MayHaveSharedFault is whether any failure here could be part of a
// shared fault, so that the faults are worth asking for: the two
// conclusions ci.FailureSignature looks at, and nothing else.
func (r WaitResult) MayHaveSharedFault() bool {
	return slices.ContainsFunc(r.Failing, func(f WaitFailure) bool {
		return f.Conclusion == string(ci.ConclusionFailure) || f.Conclusion == string(ci.ConclusionErrored)
	})
}

// AnnotateFaults marks each failure that is part of one of faults --
// the same job failing in this run and on another branch (ci.FaultFor,
// over the daemon's own grouping).
func (r *WaitResult) AnnotateFaults(faults []ui.CIFault) {
	for i, f := range r.Failing {
		for _, fault := range faults {
			if fault.JobID != f.JobID || fault.Summary(r.Ref) == "" {
				continue
			}
			if slices.ContainsFunc(fault.Runs, func(run ci.FaultRun) bool { return run.RunID == r.RunID }) {
				r.Failing[i].SharedFault = &fault
				break
			}
		}
	}
}

// SplitJobs reads a comma-separated -jobs.
func SplitJobs(s string) []string {
	var jobs []string
	for _, job := range strings.Split(s, ",") {
		if job = strings.TrimSpace(job); job != "" && !slices.Contains(jobs, job) {
			jobs = append(jobs, job)
		}
	}
	return jobs
}

// FailedJobIDs is the ids of run's jobs that finished without passing --
// what a retrigger reruns -- spelled workflow-path:job-id where one id
// names jobs in two workflows.
func FailedJobIDs(run ci.Run) []string {
	seen := map[string]int{}
	for _, job := range run.Jobs {
		seen[job.JobID]++
	}
	var ids []string
	for _, job := range ci.SortJobRuns(run.Jobs) {
		if !job.Conclusion.Done() || JobPassed(job) || job.JobID == "" {
			continue
		}
		id := job.JobID
		if seen[id] > 1 && job.WorkflowPath != "" {
			id = job.WorkflowPath + ":" + id
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// Report is the verdict as a terminal shows it: one line saying how it
// stands, then -- for a red one -- each failing job with the step that
// failed, the tests it named, and whether other branches fail the same
// way.
func (r WaitResult) Report() string {
	var b strings.Builder
	commit := r.Commit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	where := r.Repo
	if r.Ref != "" {
		where += " " + r.Ref
	}
	if commit != "" {
		where += " at " + commit
	}
	waited := (time.Duration(r.WaitedMS) * time.Millisecond).Round(time.Second).String()
	var running, passed []string
	for _, job := range r.Jobs {
		switch c := ci.Conclusion(job.Conclusion); {
		case c == ci.ConclusionSuccess || c == ci.ConclusionSkipped:
			passed = append(passed, job.Name)
		case job.Conclusion == "running" || !c.Done():
			running = append(running, job.Name)
		}
	}
	switch r.Status {
	case "running":
		fmt.Fprintf(&b, "grain CI run %s on %s is still running after %s (jobs running: %s; passed: %s)\n",
			r.RunID, where, waited, listOrNone(running), listOrNone(passed))
		fmt.Fprintf(&b, "keep waiting with: grain ci wait %s\n", r.RunID)
		return b.String()
	case "passed":
		fmt.Fprintf(&b, "grain CI passed: run %s on %s (waited %s): %s\n", r.RunID, where, waited, listOrNone(passed))
		if r.Selective {
			b.WriteString("only the jobs named with -jobs were judged; the rest of the run is not part of this verdict\n")
		}
		return b.String()
	}
	fmt.Fprintf(&b, "grain CI failed: run %s on %s, %s (waited %s)\n", r.RunID, where, r.Conclusion, waited)
	if r.Error != "" {
		fmt.Fprintf(&b, "  %s\n", r.Error)
	}
	switch {
	case r.CancelReason != "" && r.CancelledByHand:
		fmt.Fprintf(&b, "  cancelled by hand: %s\n", r.CancelReason)
	case r.CancelReason != "":
		fmt.Fprintf(&b, "  grain cancelled it itself: %s\n", r.CancelReason)
	}
	shared := false
	for _, f := range r.Failing {
		step := ""
		if f.Step != "" {
			step = ", step " + f.Step
		}
		fmt.Fprintf(&b, "  ✗ %s (%s%s)\n", f.Name, f.Conclusion, step)
		if f.Error != "" {
			fmt.Fprintf(&b, "      %s\n", f.Error)
		}
		if len(f.Tests) > 0 {
			fmt.Fprintf(&b, "      failing tests: %s\n", strings.Join(f.Tests, ", "))
		}
		if fault := f.SharedFault; fault != nil {
			shared = true
			fmt.Fprintf(&b, "      shared fault: %s\n", fault.Summary(r.Ref))
			if refs := fault.OtherRefsPhrase(r.Ref); refs != "" {
				fmt.Fprintf(&b, "      also failing on: %s\n", refs)
			}
			if fault.OwnerTaskID != "" && !fault.OwnedBy(r.Ref) {
				fmt.Fprintf(&b, "      being fixed by: task #%s\n", fault.OwnerTaskID)
			}
			for _, fix := range fault.Fixes {
				fmt.Fprintf(&b, "      open fix: %s", fix.PullRequest)
				if fix.Title != "" {
					fmt.Fprintf(&b, " (%s)", fix.Title)
				}
				b.WriteString("\n")
			}
		}
	}
	if len(passed) > 0 {
		fmt.Fprintf(&b, "  passed: %s\n", strings.Join(passed, ", "))
	}
	if shared {
		b.WriteString("at least one failure is shared with other branches: it is not this branch's own (\"grain ci faults\" lists them)\n")
	}
	fmt.Fprintf(&b, "read the failing steps' output with: grain ci show %s\n", r.RunID)
	return b.String()
}

func listOrNone(xs []string) string {
	if len(xs) == 0 {
		return "none"
	}
	return strings.Join(xs, ", ")
}

// ProviderTestHistory is test's history across the newest limit runs
// matching f, read from c (TestHistory). A repository whose CI is GitHub
// Actions (ci_mode: github), named by f.Repo with no f.Provider, has its
// history read from its Actions runs alone: its grain CI runs are a
// retired mode's, and the report says how many it left out, as it says
// what the forge's half of the listing was short of.
func ProviderTestHistory(ctx context.Context, c *ui.HTTPClient, f ci.RunFilter, limit int, test string) (ci.TestHistoryReport, error) {
	defaulted := false
	leftOut := -1
	if f.Provider == "" && f.Repo != "" {
		if defaults, err := c.RepoDefaults(ctx, f.Repo); err == nil {
			if defaults.CIMode == string(model.CIModeGitHub) {
				f.Provider, defaulted = ci.ProviderGitHubActions, true
			}
		} else {
			grain := f
			grain.Provider = ci.ProviderGrain
			if probe, perr := c.FindCIRuns(ctx, grain, 1); perr != nil {
				return ci.TestHistoryReport{}, perr
			} else if probe.RepoProvider == ci.ProviderGitHubActions {
				f.Provider, defaulted, leftOut = ci.ProviderGitHubActions, true, probe.Matched
			}
		}
	}
	page, err := c.FindCIRuns(ctx, f, limit)
	if err != nil {
		return ci.TestHistoryReport{}, err
	}
	report := TestHistory(ctx, c, page.Runs, test, f.Repo)
	if page.ActionsNote != "" {
		report.Notes = append(report.Notes, "GitHub Actions: "+page.ActionsNote)
	}
	if defaulted {
		if leftOut < 0 {
			grain := f
			grain.Provider = ci.ProviderGrain
			if left, err := c.FindCIRuns(ctx, grain, 1); err == nil {
				leftOut = left.Matched
			}
		}
		if leftOut > 0 {
			report.Notes = append(report.Notes, fmt.Sprintf(
				"%s's CI is GitHub Actions: its %d Grain CI run(s), from before, are left out (-provider grain reads them)", f.Repo, leftOut))
		}
	}
	return report, nil
}

// TestHistory is test's history across runs, a listing newest first,
// as the in-sandbox `grain ci test-history` reports it
// (ci.QueryTestHistory). Each run is read from src in full, because a
// listing carries no step output and a pass is only ever found there; a
// run that cannot be read in full is counted from its listing, which
// still carries its failures. repo is the repository the listing was
// narrowed to, or "" for none.
func TestHistory(ctx context.Context, src RunSource, runs []ci.Run, test, repo string) ci.TestHistoryReport {
	full := make([]ci.Run, 0, len(runs))
	repos := map[string]bool{}
	for _, run := range runs {
		if got, err := src.CIRun(ctx, run.ID); err == nil {
			run = got
		}
		repos[strings.ToLower(run.Repo)] = true
		full = append(full, run)
	}
	report := ci.QueryTestHistory(full, test)
	switch {
	case repo != "":
		report.Repo = repo
	case len(repos) > 1:
		report.Repo = ""
	}
	return report
}
