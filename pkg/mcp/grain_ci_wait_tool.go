package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
)

// defaultCILogReadBytes and maxCILogReadBytes bound one get_ci_log read.
//
// The ceiling is maxToolResultBytes' own production value rather than the
// 32 KB it used to be, because a range this tool promises has to be a
// range it can actually deliver: every answer is now capped at
// maxResultTextBytes on the way out (result_size.go), so a caller that
// asked for 32 KB would have been handed 20 KB of it with the middle cut
// out -- the worst of both, since a log read has offset and next_offset
// and could simply have been asked for in two halves. Anything above the
// cap was a promise about somebody else's CLI anyway: past a framework's
// per-result limit the result is truncated or refused on its own terms.
//
// Consts against a var: maxToolResultBytes is a var only so a test can
// shrink it, and these two are part of a schema written once at
// registration, which a later shrink could not reach in any case.
const (
	defaultCILogReadBytes = 16 << 10
	maxCILogReadBytes     = 16 << 10
)

// defaultCIRunListLimit is how many runs list_ci_runs reports when the
// call names no limit of its own. The list is "every run this deployment
// still remembers", which on a busy deployment is hundreds, and the
// question it is asked is nearly always about a recent one -- the run in
// flight, or the same job on another branch an hour ago. Same reasoning,
// and same shape, as defaultTaskListLimit.
const defaultCIRunListLimit = 20

// minListedCIRunBytes is the least one run gets in a list: enough for its
// headline -- id, ref, commit, conclusion -- and a job line or two under
// it, which is what the list is for.
const minListedCIRunBytes = 240

// maxCIRunErrorBytes bounds one error string a CI report quotes: a run's,
// a job's, or a step's.
//
// These are the one part of a status report whose size nothing bounds.
// ci.JobRun.Error is a sentence for most failures and a whole guest dump
// for the one this deployment hits most -- a kontur guest that boots,
// answers and never acquires a default route, whose error carries the
// dump waitForGuestReady collects. One of those, times the jobs of a run,
// times the runs of a list, is an answer that arrives instead of the
// report it was hung off.
//
// Generous enough for the dump's own head and tail, since that is exactly
// what somebody diagnosing the flake wants and it is why the dump was put
// in the error in the first place.
const maxCIRunErrorBytes = 2 << 10

// elisionAdviceCIRun and elisionAdviceCILog are what a cut CI answer
// tells its reader to do next. Both name a call that returns less rather
// than the same call again.
const elisionAdviceCIRun = "read one job's own output with get_ci_log, " +
	"which takes a run id, a job and a byte range"

const elisionAdviceCIRunList = "narrow it -- \"repo\", \"ref\", \"conclusion\", " +
	"\"job\" and \"since\" each ask a smaller question, and \"limit\" says how many " +
	"of the newest runs to report -- or read one run's output with get_ci_log"

type GrainCIRunner interface {
	StartGrainCI(context.Context, []string) (ci.Run, error)
	// RetriggerGrainCI starts an uncached recheck of jobs -- or, naming
	// none, of the jobs whose latest result at the commit did not pass,
	// unless all asks for every job of the workflow.
	RetriggerGrainCI(ctx context.Context, workflow, commit string, jobs []string, all bool) (ci.Run, error)
	GrainCIRun(context.Context, string) (ci.Run, error)
	// FindGrainCIRuns is list_ci_runs' question as the deployment
	// answers it: the filter and the limit go to the daemon, which asks
	// its run store rather than narrowing a page it has already read
	// (ci.RunStore.Find). That is what lets "this job, red, on any
	// branch, this week" reach a fortnight of history rather than the
	// newest few hundred runs (grain/task-289).
	FindGrainCIRuns(context.Context, ci.RunFilter, int) (ci.RunPage, error)
	// GrainCISharedFaults is every failure this deployment's CI is
	// showing on more than one branch at once, as the daemon groups them
	// (pkg/ci/sharedfault.go, served on GET /api/ci/faults). It is what
	// lets a red report say whose red it is.
	//
	// Asked of the daemon rather than worked out here because both
	// halves of the answer need what a sandbox does not have: every
	// recent run's step output, and a GitHub credential to see what an
	// open pull request touches.
	GrainCISharedFaults(context.Context) ([]CIFault, error)
}

// NewGrainCIWaitTools is the three tools that start, list and read grain
// CI, as one run gets them.
//
// scope is which repositories this run may read CI for, and it is fixed
// here rather than taken as an argument for the reason PullRequestScope
// is: what a run may see is grain's to decide, never the agent's to ask
// for (pkg/mcp's ci_repo_scope.go). branch is this run's own branch,
// used to default run_id in get_ci_log to the latest run on the branch
// before falling back to the whole repository. wait_for_checks needs none of it --
// it starts a run for this run's own branch and can reach no other.
func NewGrainCIWaitTools(runner GrainCIRunner, scope CIRepoScope, branch ...string) []Tool {
	var taskBranch string
	if len(branch) > 0 {
		taskBranch = strings.TrimSpace(branch[0])
	}
	return []Tool{
		{
			Name:          "wait_for_checks",
			Description:   fmt.Sprintf("Start grain CI for your task's pushed branch (or reuse the identical run already in progress), then wait for its verdict and report status only. A failing job is reported with whether the same job is failing the same way on other branches right now -- how many, which, and any open pull request that touches the file the failure names -- so an inherited outage is distinguishable from your own red build without going looking for it. It never returns test logs; use get_ci_log with the returned run id for output you actually need -- including while the run is still going, which is how you watch a long job rather than waiting it out blind. It does not read GitHub checks. Optional jobs selects job ids for fast feedback and includes their dependencies; omit jobs for the complete required suite. A selective run never satisfies the task's full-suite gate. Job results are cached by content fingerprint, failures as well as passes, but a cached red that a retrigger_ci run has since cleared is no longer replayed: the recheck's verdict replaced it, so there is no need to make cosmetic edits to change the fingerprint. The full-suite verdict, and the merge gate that reads it, combine each job's latest result: after retrigger_ci re-runs just the failed jobs and they pass, the gate is green without the passed jobs running again, and while that recheck is still going this waits for it rather than starting a run that would read the old red. Optional timeout_seconds defaults to %d and is capped at %d.", int(DefaultWaitForChecksTimeout.Seconds()), int(MaxWaitForChecksTimeout.Seconds())),
			InputSchema:   map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"timeout_seconds": map[string]any{"type": "number"}, "jobs": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1}}},
			ElisionAdvice: elisionAdviceCIRun,
			Handler: func(ctx context.Context, args map[string]any) Result {
				if runner == nil {
					return Result{Text: "Grain CI is unavailable for this run.", IsError: true}
				}
				budget := waitForChecksBudget(ctx, args)
				if budget.tooLate {
					return Result{Text: noTimeToWait(budget.remaining, "grain CI")}
				}
				jobs := argStringSlice(args, "jobs")
				text, err := waitForGrainCI(ctx, runner, jobs, budget.timeout)
				if err != nil {
					return Result{Text: err.Error(), IsError: true}
				}
				return Result{Text: budget.note + text}
			},
		},
		{
			Name: "list_ci_runs",
			Description: fmt.Sprintf("List the status of the grain CI runs this deployment still remembers %s, including the ones still going -- the %d most recent by default, or as many as \"limit\" asks for, and the answer says how many it left out. Narrow it rather than reading it all: \"repo\" is one of the repositories this run may see, \"ref\" matches a branch by any part of its name, \"conclusion\" is one or more of %s (the last two being a run that has not finished), \"job\" is one job by name or id -- which also puts that job's own result and duration on every line, so a flaky job is distinguishable from a broken tree -- and \"since\" is how far back (24h, 7d, or a date). \"sort\" orders by %s and \"order\" points it. Use it to find similar failures on other branches, or to find the id of a run in flight; use get_ci_log with a returned run id to inspect its output, or to tail a run that has not finished. A failing job grain has already found on several branches at once says so on its own line, with the branches it is shared with.",
				scope.listedRepos(), defaultCIRunListLimit, strings.Join(ci.RunStates, ", "), ciRunSortWords()),
			InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"repo": map[string]any{"type": "string", "description": scope.repoArgument()},
				"ref":  map[string]any{"type": "string", "description": "a branch, matched on any part of its name (\"task-231\" finds \"grain/task-231\")"},
				"conclusion": map[string]any{
					"description": fmt.Sprintf("one of %s, or several of them", strings.Join(ci.RunStates, ", ")),
					"anyOf": []any{
						map[string]any{"type": "string"},
						map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1},
					},
				},
				"job":   map[string]any{"type": "string", "description": "one job, by name or id -- runs that have it, with that job's own result and duration"},
				"since": map[string]any{"type": "string", "description": "how far back: a span (24h, 7d, 90m) or a date (2026-09-17, or an RFC 3339 timestamp)"},
				"sort":  map[string]any{"type": "string", "description": fmt.Sprintf("order by %s (default %s)", ciRunSortWords(), ci.SortRunsByStarted)},
				"order": map[string]any{"type": "string", "enum": []any{"asc", "desc"}, "description": "desc, the default, is newest or longest first"},
				"limit": map[string]any{"type": "number", "description": fmt.Sprintf(
					"how many of the matching runs to report (default %d)", defaultCIRunListLimit)},
				"view": map[string]any{"type": "string", "enum": []any{ciRunViewSummary, ciRunViewFull},
					"description": "summary (the default: one line per run -- id, ref, verdict, failing jobs) or full (each run's jobs, errors and shared faults)"},
				"after": map[string]any{"type": "string",
					"description": "a run id the last page ended with: list the runs that come after it in this order (started-time order only)"},
			}},
			ElisionAdvice: elisionAdviceCIRunList,
			Handler: func(ctx context.Context, args map[string]any) Result {
				if runner == nil {
					return Result{Text: "Grain CI is unavailable for this run.", IsError: true}
				}
				now := time.Now()
				filter, err := ciRunFilter(args, now)
				if err != nil {
					return Result{Text: err.Error(), IsError: true}
				}
				// What this run may see, applied to what it asked for:
				// a repository outside the scope is refused here rather
				// than answered with an empty list (ci_repo_scope.go).
				filter, err = scope.narrow(filter)
				if err != nil {
					return Result{Text: err.Error(), IsError: true}
				}
				limit := defaultCIRunListLimit
				if f, ok := argFloat(args, "limit"); ok && int(f) > 0 {
					limit = int(f)
				}
				view, _ := argString(args, "view")
				view = strings.ToLower(strings.TrimSpace(view))
				if view == "" {
					view = ciRunViewSummary
				}
				if view != ciRunViewSummary && view != ciRunViewFull {
					return Result{Text: fmt.Sprintf("view %q is not summary or full", view), IsError: true}
				}
				if after, ok := argString(args, "after"); ok && strings.TrimSpace(after) != "" {
					if filter, err = ciRunsAfter(ctx, runner, scope, filter, strings.TrimSpace(after)); err != nil {
						return Result{Text: err.Error(), IsError: true}
					}
				}
				page, err := runner.FindGrainCIRuns(ctx, filter, limit)
				if err != nil {
					return Result{Text: fmt.Sprintf("listing grain CI runs: %v", err), IsError: true}
				}
				if page.Stored == 0 {
					return Result{Text: "No grain CI runs are currently remembered."}
				}
				// The boundary again, on the answer rather than on the
				// question: the filter went to another process, and a
				// scope only that process enforces is not one
				// (CIRepoScope.keepInScope).
				page = scope.keepInScope(page)
				if view == ciRunViewSummary {
					return Result{Text: renderGrainCIRunSummary(page, filter, scope)}
				}
				return Result{Text: renderGrainCIRunList(page, filter, scope, now,
					sharedFaults(ctx, runner, page.Runs...))}
			},
		},
		{
			Name: "get_ci_log",
			Description: fmt.Sprintf(
				"Read one byte range from a job or step of a grain CI run -- including a run that is still going, whose log this follows as it is printed. "+
					"Supply run_id (optional, defaults to latest on branch; wait_for_checks and list_ci_runs both report it); "+
					"job (or job_id) and step (or step_name) are optional and default to the job that is running, or the one that failed.%s "+
					"offset is a zero-based byte offset into that selected log and defaults to 0; "+
					"tail reads that many bytes from the end instead, which is what you want for a running job (or set tail to true). "+
					"size or limit defaults to %d bytes and is capped at %d. "+
					"The result reports total_size, next_offset and whether the log is still growing, so call it again from next_offset to keep watching. "+
					"A read of a job that failed says above the bytes whether the same job is failing the same way on other branches right now.",
				scope.logScopeSentence(), defaultCILogReadBytes, maxCILogReadBytes),
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"run_id":    map[string]any{"type": "string", "description": "Specific CI run ID (defaults to latest on branch)."},
					"job":       map[string]any{"type": "string", "description": "Specific job name or ID."},
					"job_id":    map[string]any{"type": "string", "description": "Specific job name or ID."},
					"step":      map[string]any{"type": "string", "description": "Specific step name."},
					"step_name": map[string]any{"type": "string", "description": "Specific step name."},
					"attempt":   map[string]any{"type": "integer", "minimum": 1, "description": "Specific 1-indexed job attempt number (e.g. 1 for first attempt before retry). Omit for the latest attempt (or the failing attempt if a retry was cancelled)."},
					"offset":    map[string]any{"type": "integer", "minimum": 0, "description": "Offset in bytes to read from."},
					"tail": map[string]any{
						"description": "Read from end of log (boolean true or integer byte count).",
						"anyOf": []any{
							map[string]any{"type": "boolean"},
							map[string]any{"type": "integer", "minimum": 1, "maximum": maxCILogReadBytes},
						},
					},
					"size":  map[string]any{"type": "integer", "minimum": 1, "maximum": maxCILogReadBytes, "description": "Maximum number of bytes to read."},
					"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": maxCILogReadBytes, "description": "Maximum number of bytes/lines to read."},
				},
			},
			ElisionAdvice: "ask for a smaller \"size\", or read on from the \"next_offset\" this answer reports",
			Handler: func(ctx context.Context, args map[string]any) Result {
				if runner == nil {
					return Result{Text: "Grain CI is unavailable for this run.", IsError: true}
				}
				if !scope.mayReadAny() {
					// Refused before the lookup: a run this one may not
					// be told about is one it may not be told the
					// existence of either (ci_repo_scope.go).
					return Result{Text: scope.refuseAll().Error(), IsError: true}
				}
				runID, _ := argString(args, "run_id")
				runID = strings.TrimSpace(runID)
				if runID == "" {
					var page ci.RunPage
					var err error
					if taskBranch != "" {
						page, err = runner.FindGrainCIRuns(ctx, ci.RunFilter{Repo: scope.Repo, Ref: taskBranch, Descending: true}, 1)
					}
					if taskBranch == "" || err != nil || len(page.Runs) == 0 {
						page, err = runner.FindGrainCIRuns(ctx, ci.RunFilter{Repo: scope.Repo, Descending: true}, 1)
					}
					if err != nil || len(page.Runs) == 0 {
						return Result{Text: "No grain CI runs are currently remembered for this repository.", IsError: true}
					}
					runID = page.Runs[0].ID
				}
				jobName, _ := argString(args, "job_id")
				if jobName == "" {
					jobName, _ = argString(args, "job")
				}
				stepName, _ := argString(args, "step_name")
				if stepName == "" {
					stepName, _ = argString(args, "step")
				}
				attemptArg, err := ciLogRangeArg(args, "attempt", 0, 1, 100)
				if err != nil {
					return Result{Text: err.Error(), IsError: true}
				}
				offset, err := ciLogRangeArg(args, "offset", 0, 0, -1)
				if err != nil {
					return Result{Text: err.Error(), IsError: true}
				}
				sizeArg := "size"
				if _, hasLimit := args["limit"]; hasLimit {
					sizeArg = "limit"
				}
				size, err := ciLogRangeArg(args, sizeArg, defaultCILogReadBytes, 1, maxCILogReadBytes)
				if err != nil {
					return Result{Text: err.Error(), IsError: true}
				}
				var tail int
				if b, ok := argBool(args, "tail"); ok && b {
					tail = size
					if tail <= 0 {
						tail = defaultCILogReadBytes
					}
				} else {
					t, err := ciLogRangeArg(args, "tail", 0, 1, maxCILogReadBytes)
					if err != nil {
						return Result{Text: err.Error(), IsError: true}
					}
					tail = t
				}
				if tail > 0 {
					if _, given := argFloat(args, "offset"); given {
						return Result{Text: "tail and offset are two ways of saying where to read from; give one or the other", IsError: true}
					}
					size = tail
				}
				run, err := runner.GrainCIRun(ctx, runID)
				if err != nil {
					return Result{Text: fmt.Sprintf("reading grain CI run %s: %v", runID, err), IsError: true}
				}
				// Whose run this is can only be known once it has been
				// read, so the scope is checked here -- and a run
				// belonging to another repository is refused in a
				// sentence rather than reported as missing.
				if !scope.permits(run.Repo) {
					return Result{Text: scope.refuseRun(run).Error(), IsError: true}
				}
				job, err := grainCIJob(run, strings.TrimSpace(jobName))
				if err != nil {
					return Result{Text: err.Error(), IsError: true}
				}
				selectedJob, attemptNum, totalAttempts, err := selectCIJobAttempt(run.ID, *job, attemptArg)
				if err != nil {
					return Result{Text: err.Error(), IsError: true}
				}
				logText, running, err := grainCILog(run, selectedJob, strings.TrimSpace(stepName))
				if err != nil {
					return Result{Text: err.Error(), IsError: true}
				}
				if tail > 0 {
					offset = len(logText) - tail
					if offset < 0 {
						offset = 0
					}
				}
				return Result{Text: ciLogHeading(run, selectedJob, strings.TrimSpace(stepName), running, attemptNum, totalAttempts) +
					ciLogFaultNote(ctx, runner, run, *job) +
					renderCILogRange(logText, offset, size, running)}
			},
		},
		{
			Name: "retrigger_ci",
			Description: "Rerun just the failed jobs of your branch's grain CI, fresh and uncached, on the same commit -- " +
				"to show a red was a flake, or to sample a job again. By default it reruns only the jobs whose latest result " +
				"at the commit did not pass; jobs names the ones to rerun instead, and jobs: [\"*\"] reruns every job of the " +
				"workflow. Jobs that passed are not rerun, and a named job's dependencies reuse their cached pass rather than " +
				"running again. Each rerun job's fresh success or failure is saved as the cached verdict for its own fingerprint, " +
				"replacing the one it re-ran, and the passed jobs' verdicts stay as they are: the full-suite verdict that " +
				"wait_for_checks and the merge-queue gate read combines every job's latest result, so once the rerun jobs pass " +
				"the gate is green, with no need to run the whole suite again or to change the tree.",
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"workflow": map[string]any{
						"type":        "string",
						"description": "Specific workflow file path (e.g. \".grain/workflows/ci.yml\") to run. Leave empty to run all matching workflows.",
					},
					"commit": map[string]any{
						"type":        "string",
						"description": "Specific commit SHA to test (defaults to the commit of your branch's latest grain CI run).",
					},
					"jobs": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"minItems":    1,
						"description": "Jobs to rerun, by id or name as a run reports them (\"go\", \"ui e2e\"). Leave it out to rerun only the jobs that failed; [\"*\"] reruns every job. A name the workflows do not have is refused with the names they do.",
					},
				},
			},
			ElisionAdvice: elisionAdviceCIRun,
			Handler: func(ctx context.Context, args map[string]any) Result {
				if runner == nil {
					return Result{Text: "Grain CI is unavailable for this run.", IsError: true}
				}
				workflow, _ := argString(args, "workflow")
				commit, _ := argString(args, "commit")
				var jobs []string
				all := false
				for _, job := range argStringSlice(args, "jobs") {
					if job = strings.TrimSpace(job); job == "*" {
						all = true
					} else if job != "" {
						jobs = append(jobs, job)
					}
				}
				if all {
					jobs = nil
				}
				run, err := runner.RetriggerGrainCI(ctx, strings.TrimSpace(workflow), strings.TrimSpace(commit), jobs, all)
				if err != nil {
					return Result{Text: fmt.Sprintf("retriggering grain CI: %v", err), IsError: true}
				}
				var jobNames []string
				for _, j := range run.Jobs {
					if j.Name != "" {
						jobNames = append(jobNames, j.Name)
					} else if j.JobID != "" {
						jobNames = append(jobNames, j.JobID)
					}
				}
				jobsClause := ""
				if len(jobNames) > 0 {
					jobsClause = fmt.Sprintf("\nJobs queued: %s", strings.Join(jobNames, ", "))
				}
				return Result{Text: fmt.Sprintf("Triggered fresh uncached Grain CI run %s against commit %s.%s\nEach rerun job's result replaces the cached verdict for its fingerprint and the other jobs keep theirs, so the full-suite verdict wait_for_checks and the merge-queue gate read combines this run's answer with the jobs that had already passed.\nUse wait_for_checks or get_ci_log (run_id: %s) to monitor progress.", run.ID, run.Commit, jobsClause, run.ID)}
			},
		},
		{
			Name:        "query_test_history",
			Description: "Query recent execution records across Grain CI runs for a specific test name or suite to report historical pass, failure, and flake rates.",
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"test_name"},
				"properties": map[string]any{
					"test_name": map[string]any{
						"type":        "string",
						"description": "The name of the test or test suite to inspect (e.g. \"TestFoo\", \"pkg/ci.TestBar\").",
					},
					"repo": map[string]any{
						"type":        "string",
						"description": scope.repoArgument(),
					},
					"limit_runs": map[string]any{
						"type":        "integer",
						"description": "Maximum number of recent runs to inspect (default 20, max 100).",
						"minimum":     1,
						"maximum":     100,
					},
				},
			},
			Handler: func(ctx context.Context, args map[string]any) Result {
				if runner == nil {
					return Result{Text: "Grain CI is unavailable for this run.", IsError: true}
				}
				testName, ok := argString(args, "test_name")
				testName = strings.TrimSpace(testName)
				if !ok || testName == "" {
					return Result{Text: "test_name is required", IsError: true}
				}
				repo, _ := argString(args, "repo")
				repo = strings.TrimSpace(repo)
				if repo == "" {
					repo = scope.Repo
				}
				if !scope.permits(repo) {
					if !scope.mayReadAny() {
						return Result{Text: scope.refuseAll().Error(), IsError: true}
					}
					return Result{Text: scope.refuseRepo(repo).Error(), IsError: true}
				}
				limit := 20
				if f, ok := argFloat(args, "limit_runs"); ok && int(f) > 0 {
					limit = int(f)
					if limit > 100 {
						limit = 100
					}
				}
				page, err := runner.FindGrainCIRuns(ctx, ci.RunFilter{Repo: repo, Descending: true}, limit)
				if err != nil {
					return Result{Text: fmt.Sprintf("querying grain CI runs: %v", err), IsError: true}
				}
				report := ci.QueryTestHistory(page.Runs, testName)
				report.Repo = repo
				return Result{Text: report.Render()}
			},
		},
	}
}

func waitForGrainCI(ctx context.Context, runner GrainCIRunner, jobs []string, timeout time.Duration) (string, error) {
	run, err := runner.StartGrainCI(ctx, jobs)
	if err != nil {
		return "", fmt.Errorf("starting grain CI: %w", err)
	}
	started := time.Now()
	credit := startWaitCredit(ctx)
	defer func() { _, _ = credit(ctx, time.Since(started)) }()
	deadline := started.Add(timeout)
	// The verdict, plus -- for a run with something red in it -- whose red
	// it is. The faults are fetched per answer rather than once, because
	// the answer is given at most once and only a failing one asks.
	report := func(run ci.Run, timedOut bool) string {
		faults := sharedFaults(ctx, runner, run)
		var attributions map[string]ci.JobAttribution
		if runner != nil && hasFailedJob(run) {
			if page, err := runner.FindGrainCIRuns(ctx, ci.RunFilter{Repo: run.Repo, Descending: true}, 50); err == nil {
				attributions = ci.AttributeRun(run, "main", append([]ci.Run{run}, page.Runs...))
			}
		}
		if len(faults) > 0 && attributions != nil {
			for _, job := range run.Jobs {
				if fault, ok := faultForJob(faults, run, job); ok && fault.OwnerTaskID != "" {
					if attr, exists := attributions[job.JobID]; exists {
						attributions[job.JobID] = attr.WithOwner(run.Ref, fault.OwnerTaskID)
					}
					if job.Name != "" {
						if attr, exists := attributions[job.Name]; exists {
							attributions[job.Name] = attr.WithOwner(run.Ref, fault.OwnerTaskID)
						}
					}
				}
			}
		}
		text := renderGrainCIRun(run, time.Since(started), timedOut, faults, attributions)
		if faultsSaid(faults, run) {
			text += faultAdviceForRun(faults, run)
		}
		return text + selectiveRunReminder(jobs) + recheckRunNote(jobs, run)
	}
	followed := map[string]bool{}
	for {
		latest, err := runner.GrainCIRun(ctx, run.ID)
		if err != nil {
			return "", fmt.Errorf("reading grain CI run %s: %w", run.ID, err)
		}
		run = latest
		if next, ok := followSupersede(ctx, runner, jobs, run); ok && !followed[next.ID] {
			followed[run.ID] = true
			run = next
			continue
		}
		if run.Conclusion.Done() || (hasFailedJob(run) && !onlyExternallyOwnedFaultsInProgress(ctx, runner, run)) {
			return report(run, false), nil
		}
		if time.Now().After(deadline) {
			return report(run, true), nil
		}
		if left, ok := credit(ctx, time.Since(started)); ok && left <= waitForChecksDeadlineSlack {
			return report(run, true), nil
		}
		nap := checkPollInterval
		if remaining := time.Until(deadline); remaining < nap {
			nap = remaining
		}
		if err := sleepUntilCancelled(ctx, nap); err != nil {
			return "", fmt.Errorf("waiting for grain CI run %s: %w", run.ID, err)
		}
	}
}

// followSupersede is the run a full-suite wait should be waiting on
// instead of run, when grain cancelled run to make way for a newer one
// started for the same task at the same commit (ci.SupersedingRun): a
// second retrigger_ci replacing a recheck this wait had joined, or a
// complete run replacing it. That cancellation is not this branch's
// verdict -- the newer run is -- and reporting it as one sends the agent
// back to wait_for_checks for the answer it was already waiting for.
//
// A selective wait does not follow: the run replacing it may sample
// other jobs than the ones it asked about. Nor does a wait follow to
// another commit, which is a branch that moved under it and a different
// question.
func followSupersede(ctx context.Context, runner GrainCIRunner, jobs []string, run ci.Run) (ci.Run, bool) {
	if len(jobs) > 0 {
		return ci.Run{}, false
	}
	id, ok := ci.SupersedingRun(run)
	if !ok {
		return ci.Run{}, false
	}
	next, err := runner.GrainCIRun(ctx, id)
	if err != nil || next.ID != id || next.Commit != run.Commit {
		return ci.Run{}, false
	}
	return next, true
}

func hasFailedJob(run ci.Run) bool {
	for _, job := range run.Jobs {
		if job.Conclusion == ci.ConclusionFailure || job.Conclusion == ci.ConclusionErrored {
			return true
		}
	}
	return false
}

func ciJobOutcomes(run ci.Run) (failed []ci.JobRun, unfinished int) {
	for _, job := range run.Jobs {
		if job.Conclusion == ci.ConclusionFailure || job.Conclusion == ci.ConclusionErrored {
			failed = append(failed, job)
		} else if !job.Conclusion.Done() {
			unfinished++
		}
	}
	return failed, unfinished
}

func selectiveRunReminder(jobs []string) string {
	if len(jobs) == 0 {
		return ""
	}
	return "\n\nThis was a selective run: before finishing, call wait_for_checks without jobs for a full-suite verdict on this commit."
}

// recheckRunNote is what a full-suite wait says when what it was handed
// is the uncached recheck the task's verdict is waiting on (ui's
// liveRecheck) rather than a complete run: its jobs are only the ones
// being sampled again, and the verdict is them combined with the run
// they re-checked.
func recheckRunNote(jobs []string, run ci.Run) string {
	if len(jobs) > 0 || !run.Recheck {
		return ""
	}
	return "\n\nThis is the uncached recheck of this commit's failed jobs that the task's verdict is waiting on, so it lists only those jobs. " +
		"The full-suite verdict, and the merge-queue gate, combine it with the jobs that had already passed. " +
		"Call wait_for_checks again once it has finished for the combined full-suite verdict."
}

// ciRunSortWords is the sort vocabulary as a tool description says it.
func ciRunSortWords() string {
	words := make([]string, 0, len(ci.RunSortKeys))
	for _, key := range ci.RunSortKeys {
		words = append(words, string(key))
	}
	return strings.Join(words, ", ")
}

// ciRunFilter reads list_ci_runs' arguments into the filter both this
// tool and the CI pane narrow by (ci.RunFilter).
//
// Every word it does not recognise is refused rather than ignored. A
// filter that quietly dropped "failed" would answer with every run in
// the deployment, or with none of them, and either is an answer that
// looks like a fact about CI rather than a typo -- which is the failure
// mode this whole tool exists to end.
func ciRunFilter(args map[string]any, now time.Time) (ci.RunFilter, error) {
	filter := ci.RunFilter{Descending: true}
	repo, _ := argString(args, "repo")
	filter.Repo = strings.TrimSpace(repo)
	ref, _ := argString(args, "ref")
	filter.Ref = strings.TrimSpace(ref)
	job, _ := argString(args, "job")
	filter.Job = strings.TrimSpace(job)
	for _, word := range argStrings(args, "conclusion") {
		state, err := ci.ParseRunState(word)
		if err != nil {
			return ci.RunFilter{}, err
		}
		filter.States = append(filter.States, state)
	}
	if since, ok := argString(args, "since"); ok {
		at, err := ci.ParseSince(since, now)
		if err != nil {
			return ci.RunFilter{}, err
		}
		filter.Since = at
	}
	if word, ok := argString(args, "sort"); ok && strings.TrimSpace(word) != "" {
		key, err := ci.ParseRunSortKey(strings.TrimSpace(word))
		if err != nil {
			return ci.RunFilter{}, err
		}
		filter.Sort = key
	}
	if word, ok := argString(args, "order"); ok && strings.TrimSpace(word) != "" {
		switch strings.ToLower(strings.TrimSpace(word)) {
		case "asc":
			filter.Descending = false
		case "desc":
			filter.Descending = true
		default:
			return ci.RunFilter{}, fmt.Errorf("%q is not an order: use asc or desc", word)
		}
	}
	return filter, nil
}

// ciRunOrderPhrase is the order in the words a reader uses for it, for
// the line above the list. "Newest first" and "longest first" are the
// same direction on two different keys, and a heading that said "desc"
// would be describing the argument rather than the answer.
func ciRunOrderPhrase(f ci.RunFilter) string {
	switch f.Sort {
	case ci.SortRunsByFinished:
		if f.Descending {
			return "latest to finish first"
		}
		return "earliest to finish first"
	case ci.SortRunsByDuration:
		if f.Descending {
			return "longest first"
		}
		return "shortest first"
	default:
		if f.Descending {
			return "newest first"
		}
		return "oldest first"
	}
}

// renderGrainCIRunList is the list answer: the runs that matched, in the
// order asked for, each one as much of its own report as a list can
// afford -- and, above them, what was asked and what was left out.
//
// Saying what was left out is the whole difference between a bounded
// answer and a truncated one, and it is what get_ci_log already does
// with total_size and next_offset. Three things can be missing from this
// list and a reader has to be able to tell which: runs this deployment
// no longer remembers (nothing here can help), runs that did not match
// (widen the filter), and matching runs past the limit (raise it). The
// heading names all three.
//
// Two bounds rather than one, because they are answers to two different
// questions. The limit is how many runs are worth reading about, and it
// is applied by the store now rather than here -- a run that wanted the
// twenty-first says so from page.Matched; the per-run budget is what
// stops one run with a lot to say -- a dozen jobs, an error carrying a
// guest dump -- from being the whole list. A run cut here is one to read
// on its own with get_ci_log, which is what this list exists to point
// at.
func renderGrainCIRunList(page ci.RunPage, f ci.RunFilter, scope CIRepoScope, now time.Time, faults []CIFault) string {
	runs, remembered := page.Runs, page.Stored
	if len(runs) == 0 {
		// An empty answer that says it is empty, and why. "No runs" from
		// a filtered call used to be indistinguishable from a deployment
		// that has never run anything, or from a tool that is broken.
		//
		// The filter is described through the scope, because the "repo"
		// in it may be one this call never asked for and the count
		// beside it covers every repository this deployment remembers
		// (CIRepoScope.describeFilter).
		return fmt.Sprintf("No grain CI run matches %s.\n\nThis deployment remembers %d %s in all, "+
			"so this is an empty answer to that question rather than an empty deployment -- "+
			"leave a filter out, or widen \"since\".",
			scope.describeFilter(f), remembered, plural(remembered, "run", "runs"))
	}
	shown := runs
	dropped := page.Matched - len(shown)
	// Three quarters of one answer's worth, shared out -- the rest is for
	// the heading, the elision notices and the room between the cap and
	// the backstop. Never below a floor, since an entry cut to less than
	// its own headline is worse than no entry: a caller that asked for
	// more runs than that divides into is one the backstop then cuts,
	// with "narrow it with limit" on the cut, which is the right advice.
	perRun := max(int(maxToolResultBytes.Load())*3/4/len(shown), minListedCIRunBytes)

	var b strings.Builder
	order := ciRunOrderPhrase(f)
	switch {
	case f.Narrowed() && dropped > 0:
		fmt.Fprintf(&b, "Filter: %s.\n%d of %d remembered grain CI runs match; showing %d (%s):",
			scope.describeFilter(f), page.Matched, remembered, len(shown), order)
	case f.Narrowed():
		fmt.Fprintf(&b, "Filter: %s.\n%d of %d remembered grain CI runs match (%s):",
			scope.describeFilter(f), page.Matched, remembered, order)
	case dropped > 0:
		fmt.Fprintf(&b, "%d of %d remembered grain CI runs (%s):", len(shown), remembered, order)
	default:
		fmt.Fprintf(&b, "%d grain CI runs (%s):", len(runs), order)
	}
	listed := make([]ci.Run, 0, len(shown))
	for _, run := range shown {
		listed = append(listed, ciListedRun(run, f))
	}
	for _, run := range listed {
		b.WriteString("\n\n")
		b.WriteString(elideMiddle(renderGrainCIRun(run, -1, false, faults), perRun, elisionAdviceCIRun))
	}
	// Once for the list rather than under every run in it: a list is
	// where a deployment-wide fault shows up several times over, and
	// saying the same paragraph beside each of them would bury the runs
	// it is about. Asked of the runs as drawn, so that a list narrowed to
	// one job does not advise about a fault it has just filtered away.
	if faultsSaid(faults, listed...) {
		b.WriteString(faultAdvice)
	}
	if dropped > 0 {
		// "Older" only where the order makes the unshown ones older. A
		// list sorted by duration has no older end, and telling a reader
		// that the runs past the limit are the old ones would be a plain
		// falsehood about which runs they are.
		what := "runs"
		if f.Narrowed() {
			what = "matching runs"
		}
		adjective := "further"
		if f.Sort == "" || f.Sort == ci.SortRunsByStarted {
			adjective = "newer"
			if f.Descending {
				adjective = "older"
			}
		}
		fmt.Fprintf(&b, "\n\n%d %s %s are not shown -- raise \"limit\" to see them.", dropped, adjective, what)
	}
	return b.String()
}

// ciListedRun is one run as the list draws it: itself, or -- when the
// list was narrowed to one job -- itself with only that job on it.
//
// The whole point of asking about one job across twenty runs is to read
// down a column of that job's results and durations. A row that answered
// with the run's other eleven jobs as well would bury the one thing that
// was asked for, and would spend the list's per-run budget on it.
func ciListedRun(run ci.Run, f ci.RunFilter) ci.Run {
	if f.Job == "" {
		return run
	}
	run.Jobs = ci.MatchingJobs(run, f.Job)
	return run
}

// scheduledWorkflowRequester is the RequestedBy prefix grain's own
// scheduled workflow runs carry ("workflow-schedule:<schedule id>",
// orchestrator.scheduledWorkflowStarter).
const scheduledWorkflowRequester = "workflow-schedule:"

// renderGrainCIRun is one run as a status report says it, with faults
// being what the deployment knows about failures shared with other
// branches -- nil where nothing asked, or where there was nothing to
// ask about, and never the difference between a report and no report.
func renderGrainCIRun(run ci.Run, waited time.Duration, timedOut bool, faults []CIFault, attributions ...map[string]ci.JobAttribution) string {
	now := time.Now()
	status := string(run.Conclusion)
	if status == "" {
		status = "still running"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Grain CI run %s for %s at %s is %s", run.ID, run.Ref, shortSHA(run.Commit), status)
	if run.Repo != "" {
		fmt.Fprintf(&b, " (%s)", run.Repo)
	}
	// How long the caller waited, for a wait; how long the run itself
	// took, for a list -- which is a different number and the one that
	// tells a forty-minute timeout from a two-minute assertion failure.
	if waited >= 0 {
		fmt.Fprintf(&b, " after waiting %s", roundDuration(waited))
	} else if took := run.Duration(now); took > 0 {
		fmt.Fprintf(&b, " after %s", roundDuration(took))
	}
	b.WriteString(".")
	if run.Build != nil {
		// Which build of grain produced this run, on the headline rather
		// than buried: a run that behaves unlike the source an agent is
		// reading is a deployment running older code at least as often as
		// it is a bug, and this is the only thing in the report that can
		// say which (ci.Build). Said in one clause, because it is said on
		// every run of a list of twenty.
		fmt.Fprintf(&b, " Produced by grain build %s.", run.Build.Describe())
	}
	if schedule, ok := strings.CutPrefix(run.RequestedBy, scheduledWorkflowRequester); ok && schedule != "" {
		// A scheduled workflow's run says whose it is: a green night
		// files no review task (grain/task-3160), so this line and the
		// schedule's own page are where it is seen.
		fmt.Fprintf(&b, " Started by schedule %s.", schedule)
	}
	if timedOut {
		// Two moves rather than one. Waiting again is the cheap one and is
		// usually right; reading what the run is printing right now is the
		// one worth knowing about when a job has been going for twenty
		// minutes and the question is whether it is working or wedged.
		fmt.Fprintf(&b, " The wait timed out: call wait_for_checks again to keep waiting, or tail "+
			"what it is printing with get_ci_log (run_id: %q, tail: 4000).", run.ID)
	} else if failed, unfinished := ciJobOutcomes(run); waited >= 0 && len(failed) > 0 && unfinished > 0 {
		names := make([]string, 0, len(failed))
		for _, j := range failed {
			names = append(names, fmt.Sprintf("%q", j.Name))
		}
		jobWord := "job"
		if len(failed) > 1 {
			jobWord = "jobs"
		}
		fmt.Fprintf(&b, " CI has failed on %s %s, so I stopped waiting (%d unfinished %s still running). "+
			"Read failure logs with get_ci_log (run_id: %q, job: %q).",
			jobWord, strings.Join(names, ", "),
			unfinished, plural(unfinished, "job", "jobs"),
			run.ID, failed[0].Name)
	}
	if run.Error != "" {
		fmt.Fprintf(&b, "\n\nRun error: %s", ciErrorText(run.Error))
	}
	// Somebody stopped this run on purpose and left a note saying why.
	// It is the one thing in this report written by a person to whoever
	// is reading it, and without it a cancellation is indistinguishable
	// from a supersede or a daemon restart -- so it is said plainly, and
	// said before the jobs, which have nothing to add about it.
	// A reason grain wrote itself -- a supersede, a closed task -- is said
	// as grain's, so the agent does not take it for a person's decision.
	if reason := strings.TrimSpace(run.CancelReason); reason != "" {
		if run.CancelledBy == ci.CancelByOperator {
			fmt.Fprintf(&b, "\n\nThis run was cancelled by hand. The reason given: %s", reason)
		} else {
			fmt.Fprintf(&b, "\n\nGrain cancelled this run itself: %s", reason)
		}
	}
	for _, job := range ci.SortJobRuns(run.Jobs) {
		conclusion := string(job.Conclusion)
		if conclusion == "" {
			conclusion = "running"
		}
		fmt.Fprintf(&b, "\n\nJob %s: %s", job.Name, conclusion)
		// What it cost, which is half of "is this job flaky or is my
		// change broken": the same red chip means two different things
		// at two minutes and at forty.
		if took := job.Duration(now); took > 0 {
			if job.Conclusion.Done() {
				fmt.Fprintf(&b, " in %s", roundDuration(took))
			} else {
				fmt.Fprintf(&b, " for %s so far", roundDuration(took))
			}
		}
		// And what it spent waiting for a machine before any of that,
		// which the duration above deliberately excludes: a job's own
		// clock starts when it gets one. It is the other half of the same
		// question -- a job cancelled at its timeout was either starved or
		// slow, and an agent told only "cancelled" reads it as its own
		// change (ci.JobRun.Queued, grain/task-326).
		// A job grain is running a second time after its first attempt
		// failed. Said while it runs, so a wait that is taking twice as
		// long reads as a retry rather than a hang, and kept afterwards,
		// so a pass that needed one is not mistaken for a clean pass.
		if job.Retried {
			fmt.Fprintf(&b, "\nRetry %d/1 in a fresh sandbox", job.RetryCount)
			if job.RetryReason != "" {
				fmt.Fprintf(&b, " -- first attempt failed: %s", job.RetryReason)
			}
			if run.Repo != "" && run.ID != "" {
				jk := job.JobID
				if jk == "" {
					jk = job.Name
				}
				fmt.Fprintf(&b, " (attempt 1 log: /fs/ci/%s/%s/%s/attempts/1/)", run.Repo, run.ID, jk)
			}
		}
		if job.Queued > 0 {
			fmt.Fprintf(&b, "\nQueued for %s", roundDuration(job.Queued))
			if job.QueuedReason != "" {
				fmt.Fprintf(&b, " -- %s", job.QueuedReason)
			}
		}
		if job.Error != "" {
			fmt.Fprintf(&b, "\nError: %s", ciErrorText(job.Error))
		}
		summary := job.FailureSummary
		// A job that passed only because its failing tests are
		// quarantined keeps them in its FailureSummary, for test
		// history; printed under a green job as "Failed tests" it
		// would read as the job having failed.
		if job.Conclusion == ci.ConclusionSuccess {
			summary = nil
		} else if summary == nil || summary.Empty() {
			for _, step := range job.Steps {
				if step.Conclusion == ci.ConclusionFailure || step.Conclusion == ci.ConclusionErrored {
					if s := ci.ExtractFailureSummary(step.Output); s != nil && !s.Empty() {
						summary = s
						break
					}
				}
			}
			if (summary == nil || summary.Empty()) && len(job.Attempts) > 0 {
				if failing, ok := job.FailingAttempt(); ok {
					if failing.FailureSummary != nil && !failing.FailureSummary.Empty() {
						summary = failing.FailureSummary
					} else {
						for _, step := range failing.Steps {
							if step.Conclusion == ci.ConclusionFailure || step.Conclusion == ci.ConclusionErrored {
								if s := ci.ExtractFailureSummary(step.Output); s != nil && !s.Empty() {
									summary = s
									break
								}
							}
						}
					}
				}
			}
		}
		if summary != nil && !summary.Empty() {
			fmt.Fprintf(&b, "\n%s", summary.Render())
		}
		// What the job left behind, named rather than fetched. A sandbox
		// cannot reach this deployment's API, so an agent reading this
		// cannot download the file -- but "the Playwright trace of this
		// failure was kept" is the single most useful thing to be able to
		// put in a note to whoever can, and a failure investigated
		// without it is what grain/task-182 cost.
		for _, artifact := range job.Artifacts {
			if !artifact.Stored {
				fmt.Fprintf(&b, "\nArtifact %s: not kept (%s)", artifact.Name, artifact.Note)
				continue
			}
			fmt.Fprintf(&b, "\nArtifact %s: %d bytes, kept with this run -- a person can fetch it "+
				"with \"grain ci artifact %s %s %s\"",
				artifact.Name, artifact.Bytes, artifact.Run, artifact.Job, artifact.Name)
		}
		for _, step := range job.Steps {
			if step.Conclusion == ci.ConclusionSuccess || step.Conclusion == ci.ConclusionSkipped {
				continue
			}
			sc := string(step.Conclusion)
			if sc == "" {
				sc = "running"
			}
			fmt.Fprintf(&b, "\nStep %s: %s", step.Name, sc)
			if step.Error != "" {
				fmt.Fprintf(&b, " (%s)", ciErrorText(step.Error))
			}
		}
		if job.Retried && job.Conclusion != ci.ConclusionSuccess {
			for _, att := range job.Attempts {
				if att.Attempt == len(job.Attempts) && len(job.Attempts) > 1 {
					continue
				}
				for _, step := range att.Steps {
					if step.Conclusion != ci.ConclusionFailure && step.Conclusion != ci.ConclusionErrored {
						continue
					}
					fmt.Fprintf(&b, "\nAttempt %d step %s: %s", att.Attempt, step.Name, step.Conclusion)
					if step.Error != "" {
						fmt.Fprintf(&b, " (%s)", ciErrorText(step.Error))
					}
				}
			}
		}
		if len(attributions) > 0 && attributions[0] != nil {
			var attr ci.JobAttribution
			var ok bool
			if attr, ok = attributions[0][job.JobID]; !ok && job.Name != "" {
				attr, ok = attributions[0][job.Name]
			}
			if ok && attr.Summary() != "" {
				fmt.Fprintf(&b, "\nAttribution: %s", attr.Summary())
			}
		}
		// Last under the job, after the step that failed and what it
		// said, because it is a claim about that failure: this one, on
		// five other branches, right now (ci_faults.go).
		if fault, ok := faultForJob(faults, run, job); ok {
			b.WriteString(ciFaultReport(fault, run.Ref))
		}
	}
	return b.String()
}

// ciErrorText is one error as a status report quotes it: the whole of it
// when it is the sentence these usually are, and its head and tail when
// it is not (maxCIRunErrorBytes). A status report is a report -- the
// place to read a failure in full is the job's own log, which the notice
// names.
func ciErrorText(err string) string {
	return elideMiddle(err, maxCIRunErrorBytes, elisionAdviceCIRun)
}

// grainCIJob is which job a read is about: the one the caller named, or
// -- where it named none -- the one worth reading.
//
// A run has several jobs and a caller tailing one of them usually has no
// idea which, because the interesting one is whichever is still going: it
// is a read that would otherwise have to be preceded by a list, a choice
// and a second call, and the whole point of tailing is to be able to ask
// again cheaply. So an unnamed job means the job that is running, and on a
// run that has finished it means the job that failed -- the same two
// things wait_for_checks' own report is about.
//
// Ambiguity is refused rather than guessed at, with the jobs named: two
// jobs running at once is the ordinary shape of a run, and reading the
// wrong one's log is a mistake that looks exactly like a passing build.
func grainCIJob(run ci.Run, jobName string) (*ci.JobRun, error) {
	if jobName != "" {
		for i := range run.Jobs {
			if run.Jobs[i].Name == jobName || run.Jobs[i].JobID == jobName {
				return &run.Jobs[i], nil
			}
		}
		return nil, fmt.Errorf("grain CI run %s has no job %q. It has: %s", run.ID, jobName, grainCIJobList(run))
	}
	if len(run.Jobs) == 0 {
		// Why it has none, not just that it has none. A run of no jobs is
		// the one shape a reader cannot investigate -- there is no log to
		// read and nothing to name -- so the answer to "why is this run
		// empty" has to travel with the refusal, and the run already
		// carries it: a run whose plan matched nothing says so in its
		// error (ci.Plan.Unmatched), and a run with neither jobs nor an
		// error is a repository that declares no workflow at all, which
		// is a success on purpose (ci.PlanRun).
		if run.Error != "" {
			return nil, fmt.Errorf("grain CI run %s has no jobs to read: %s", run.ID, ciErrorText(run.Error))
		}
		return nil, fmt.Errorf("grain CI run %s has no jobs to read: this repository declares no "+
			"workflow, so the run had nothing to run", run.ID)
	}
	if only := grainCIOnlyJob(run, func(job ci.JobRun) bool {
		return job.Conclusion == "" && job.StartedAt != nil
	}); only != nil {
		return only, nil
	}
	if only := grainCIOnlyJob(run, func(job ci.JobRun) bool {
		return job.Conclusion == ci.ConclusionFailure || job.Conclusion == ci.ConclusionErrored
	}); only != nil {
		return only, nil
	}
	if only := grainCIOnlyJob(run, func(job ci.JobRun) bool {
		_, ok := job.FailingAttempt()
		return ok
	}); only != nil {
		return only, nil
	}
	if len(run.Jobs) == 1 {
		return &run.Jobs[0], nil
	}
	return nil, fmt.Errorf("grain CI run %s has several jobs and none of them is the obvious one to read, "+
		"so name one with job: %s", run.ID, grainCIJobList(run))
}

func selectCIJobAttempt(runID string, job ci.JobRun, attemptArg int) (ci.JobRun, int, int, error) {
	attempts := job.EffectiveAttempts()
	total := len(attempts)
	if total == 0 {
		total = 1
	}
	if attemptArg > 0 {
		att, ok := job.GetAttempt(attemptArg)
		if !ok {
			return ci.JobRun{}, 0, total, fmt.Errorf("grain CI run %s job %q has no attempt %d (it has %d attempt(s))", runID, job.Name, attemptArg, total)
		}
		return jobRunFromAttempt(job, att), att.Attempt, total, nil
	}
	if total > 1 && job.Conclusion != ci.ConclusionSuccess {
		latestHasFailedStep := false
		for _, s := range job.Steps {
			if s.Conclusion == ci.ConclusionFailure || s.Conclusion == ci.ConclusionErrored {
				latestHasFailedStep = true
				break
			}
		}
		if !latestHasFailedStep {
			if failing, ok := job.FailingAttempt(); ok {
				return jobRunFromAttempt(job, failing), failing.Attempt, total, nil
			}
		}
	}
	return job, total, total, nil
}

func jobRunFromAttempt(job ci.JobRun, att ci.JobAttempt) ci.JobRun {
	selected := job
	selected.Steps = att.Steps
	selected.Conclusion = att.Conclusion
	selected.Error = att.Error
	selected.StartedAt = att.StartedAt
	selected.FinishedAt = att.FinishedAt
	selected.FailureSummary = att.FailureSummary
	selected.GoTestLog = att.GoTestLog
	return selected
}

// grainCIOnlyJob returns the one job matching want, or nil where none or
// several do -- "several" being the case that has to stay a question for
// the caller rather than a pick.
func grainCIOnlyJob(run ci.Run, want func(ci.JobRun) bool) *ci.JobRun {
	var found *ci.JobRun
	for i := range run.Jobs {
		if !want(run.Jobs[i]) {
			continue
		}
		if found != nil {
			return nil
		}
		found = &run.Jobs[i]
	}
	return found
}

func grainCIJobList(run ci.Run) string {
	names := make([]string, 0, len(run.Jobs))
	for _, job := range ci.SortJobRuns(run.Jobs) {
		conclusion := string(job.Conclusion)
		if conclusion == "" {
			conclusion = "running"
			if job.StartedAt == nil {
				conclusion = "pending"
			}
		}
		names = append(names, fmt.Sprintf("%s (%s)", job.Name, conclusion))
	}
	return strings.Join(names, ", ")
}

// grainCILog is the text a read is served from, and whether more of it is
// still being written -- which is what tells a caller whether reaching the
// end means "that is all there is" or "ask again in a moment".
func grainCILog(run ci.Run, selected ci.JobRun, stepName string) (string, bool, error) {
	var b strings.Builder
	if stepName != "" {
		var step *ci.StepRun
		for i := range selected.Steps {
			if selected.Steps[i].Name == stepName {
				step = &selected.Steps[i]
				break
			}
		}
		if step == nil {
			return "", false, fmt.Errorf("grain CI run %s job %q has no step %q", run.ID, selected.Name, stepName)
		}
		return stepLogText(*step), step.Conclusion == "", nil
	}
	for _, step := range selected.Steps {
		if strings.TrimSpace(step.Output) == "" && step.Error == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "Step %s:\n%s", step.Name, stepLogText(step))
	}
	if selected.Error != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "Job error: %s", selected.Error)
	}
	running := selected.Conclusion == ""
	if b.Len() == 0 {
		// Two different emptinesses, and they call for different next
		// moves: a job that has printed nothing *yet* is one to ask about
		// again, and a job that is over and printed nothing never will.
		if running {
			return "(nothing printed yet)", true, nil
		}
		return "(no output recorded)", false, nil
	}
	return b.String(), running, nil
}

// ciLogHeading says which log the bytes below it came from, since a read
// that named no job picked one for itself, and what state it is in.
func ciLogHeading(run ci.Run, job ci.JobRun, stepName string, running bool, attemptNum, totalAttempts int) string {
	what := "job " + job.Name
	if totalAttempts > 1 && attemptNum > 0 {
		what += fmt.Sprintf(", attempt %d of %d", attemptNum, totalAttempts)
	}
	if stepName != "" {
		what += ", step " + stepName
	}
	state := "still running"
	switch {
	case running:
	case stepName != "":
		state = "finished"
	default:
		state = string(job.Conclusion)
	}
	heading := fmt.Sprintf("grain CI run %s, %s (%s)\n", run.ID, what, state)
	if totalAttempts > 1 && attemptNum > 0 && run.Repo != "" && run.ID != "" {
		jk := job.JobID
		if jk == "" {
			jk = job.Name
		}
		heading += fmt.Sprintf("attempt %d logs: http://127.0.0.1:9728/fs/ci/%s/%s/%s/attempts/%d/\n", attemptNum, run.Repo, run.ID, jk, attemptNum)
	}
	return heading
}
func stepLogText(step ci.StepRun) string {
	var b strings.Builder
	if step.Error != "" {
		fmt.Fprintf(&b, "Error: %s", step.Error)
	}
	if output := strings.TrimSpace(step.Output); output != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(output)
	}
	if b.Len() == 0 {
		return "(no output recorded)"
	}
	return b.String()
}
func ciLogRangeArg(args map[string]any, name string, def, min, max int) (int, error) {
	value, ok := argFloat(args, name)
	if !ok {
		return def, nil
	}
	if value != float64(int(value)) {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	result := int(value)
	if result < min {
		return 0, fmt.Errorf("%s must be at least %d", name, min)
	}
	if max >= 0 && result > max {
		return 0, fmt.Errorf("%s must be at most %d", name, max)
	}
	return result, nil
}

// renderCILogRange is one read of a log: where it came from, how much
// more there is, and the bytes themselves.
//
// running is whether the thing that wrote this text is still writing it,
// and it is what "complete" means. A read that has reached the end of a
// *finished* job's log has the whole log; one that has reached the end of
// a running job's has everything printed so far, which is a different
// answer and a different next move -- so the end of a growing log is
// reported as incomplete, with the offset to carry on from.
func renderCILogRange(logText string, offset, size int, running bool) string {
	total := len(logText)
	if offset > total {
		offset = total
	}
	end := offset + size
	if end > total {
		end = total
	}
	next := end
	var b strings.Builder
	fmt.Fprintf(&b, "offset: %d\nreturned: %d\ntotal_size: %d\nnext_offset: %d\n", offset, end-offset, total, next)
	if next >= total && !running {
		b.WriteString("complete: true\n")
	} else {
		b.WriteString("complete: false\n")
	}
	if running {
		b.WriteString("still_running: true\n")
		fmt.Fprintf(&b, "To follow it, call get_ci_log again with offset: %d, or with tail for the "+
			"newest bytes. Output is published every few seconds, so an immediate second call "+
			"may return nothing new.\n", next)
	}
	b.WriteString("\n")
	b.WriteString(logText[offset:end])
	return b.String()
}
