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

package orchestrator

// githubci.go records GitHub's own CI -- GitHub Actions jobs and check
// runs other providers post -- on the repositories grain works in, beside
// grain CI's own runs (model.GitHubCIJob, docs/github-ci.md).
//
// Three ways in, cheapest first:
//
//   - A check_run or workflow_job webhook delivery, through the GitHub App
//     bot's gateway where one is configured (recordGitHubCIEvent). Free:
//     GitHub sends it, and it covers every ref the App sees -- pull
//     request heads, base branches and merge-queue refs alike.
//   - The check-run listing the pull request sync already reads for every
//     open pull request on a GitHub-gated repository (recordPolledChecks).
//     Also free: it records what that read returned and makes no call of
//     its own, so there is no new per-pull-request poll, and the read
//     keeps its existing adaptive/ETag schedule.
//   - For a failed Actions job only, one read of its log for the failure
//     tail shared-fault correlation digests (reconcileGitHubCITails),
//     bounded per hour and skipped while the hour's budget is low. Those
//     calls are charged to the reconciler's own name in the budget
//     metrics, as every reconciler's are.
//
// What this does not do is let GitHub's CI stand in for grain's: nothing
// here writes ci_run, so no grain-CI verdict, merge gate or metric reads
// a GitHub result as its own.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/ghwebhook"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

const (
	// gitHubCITailsPerHour bounds how many failed-job logs grain reads in
	// any hour. A failure tail is for correlation, which needs a few
	// branches' worth of the same failure, not every one of them; a
	// repository whose whole suite went red should not spend the budget
	// the merge queue is living on reading the same failure forty times.
	gitHubCITailsPerHour = 30
	// gitHubCITailsPerCycle spreads those reads over cycles.
	gitHubCITailsPerCycle = 5
	// gitHubCITailMaxAge is how old a failure may be and still have its
	// log read: past the shared-fault window nobody will correlate it.
	gitHubCITailMaxAge = 6 * time.Hour
	// gitHubCIRetention is how long a recorded job is kept.
	gitHubCIRetention = 14 * 24 * time.Hour
	// gitHubCIPruneEvery is how often retention is applied.
	gitHubCIPruneEvery = time.Hour
)

// gitHubCIBudgetLow is github.BudgetLow, a variable so a test can say the
// budget is low without spending one.
var gitHubCIBudgetLow = github.BudgetLow

// isGrainOwnCheck reports whether a check run is one grain itself posts
// to GitHub -- its review check, or grain CI's own verdict mirrored as a
// check -- which is grain's CI rather than GitHub's and is measured as
// such already.
func isGrainOwnCheck(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return n == "grain" || strings.HasPrefix(n, "grain/") || strings.HasPrefix(n, "grain ci")
}

// gitHubCIJobFromEvent is a concluded check_run or workflow_job delivery
// as a record, or false for one that is not a concluded job worth
// keeping.
func gitHubCIJobFromEvent(repo model.RepoRef, ev ghwebhook.Event, now time.Time) (model.GitHubCIJob, bool) {
	if ev.Action != "completed" || ev.CheckRunID == 0 || ev.CheckConclusion == "" {
		return model.GitHubCIJob{}, false
	}
	if isGrainOwnCheck(ev.CheckName) {
		return model.GitHubCIJob{}, false
	}
	j := model.GitHubCIJob{
		Repo:        repo.String(),
		CheckRunID:  ev.CheckRunID,
		App:         ev.CheckApp,
		Ref:         ev.HeadRef,
		Commit:      ev.HeadSHA,
		Workflow:    ev.WorkflowName,
		RunID:       ev.WorkflowRun,
		RunAttempt:  ev.RunAttempt,
		JobName:     ev.CheckName,
		Conclusion:  ev.CheckConclusion,
		StartedAt:   ev.CheckStartedAt,
		CompletedAt: ev.CheckCompletedAt,
		FailingStep: ev.FailingStep,
		JobURL:      ev.HTMLURL,
		Output:      ev.CheckOutput,
		Via:         "webhook",
		UpdatedAt:   now,
	}
	if ev.WorkflowRun != 0 {
		j.RunURL = runURLOf(ev.HTMLURL)
	}
	attachOutputTail(&j)
	return j, true
}

// attachOutputTail makes a failed check that is not an Actions job carry
// its own output as its failure tail: it has no log, and its summary is
// what it said went wrong.
func attachOutputTail(j *model.GitHubCIJob) {
	if j.Failed() && !j.IsActions() && j.Output != "" {
		j.FailureTail, j.TailSource = j.Output, "output"
	}
}

// runURLOf is a job's workflow-run page, from the job's own page
// (".../actions/runs/7/job/42" -> ".../actions/runs/7").
func runURLOf(jobURL string) string {
	if i := strings.Index(jobURL, "/job/"); i > 0 {
		return jobURL[:i]
	}
	return ""
}

// recordGitHubCIEvent is processGitHubEvent's handler for check_run and
// workflow_job deliveries.
func recordGitHubCIEvent(ctx context.Context, deps Deps, repo model.RepoRef, ev ghwebhook.Event, now time.Time) error {
	j, ok := gitHubCIJobFromEvent(repo, ev, now)
	if !ok {
		return nil
	}
	return deps.Store.PutGitHubCIJob(ctx, j)
}

// polledChecksSeen remembers which concluded check runs the sync has
// already recorded, so a pull request read every cycle writes a row once
// rather than every thirty seconds. Bounded by forgetting everything at
// a size no deployment's open pull requests reach; forgetting costs one
// idempotent rewrite per check.
var polledChecksSeen = struct {
	sync.Mutex
	m map[string]bool
}{m: map[string]bool{}}

const polledChecksSeenMax = 50000

// recordPolledChecks records the concluded check runs a pull request read
// returned. It makes no GitHub call of its own.
func recordPolledChecks(ctx context.Context, store *model.Store, repo model.RepoRef, ref, sha string,
	checks []github.CheckRun, now time.Time) {
	if store == nil {
		return
	}
	for _, c := range checks {
		if c.ID == 0 || c.Status != "completed" || c.Conclusion == nil || isGrainOwnCheck(c.Name) {
			continue
		}
		commit := c.HeadSHA
		if commit == "" {
			commit = sha
		}
		key := fmt.Sprintf("%p|%s|%d|%s|%s", store, repo, c.ID, *c.Conclusion, commit)
		polledChecksSeen.Lock()
		seen := polledChecksSeen.m[key]
		polledChecksSeen.Unlock()
		if seen {
			continue
		}
		j := model.GitHubCIJob{
			Repo: repo.String(), CheckRunID: c.ID, App: c.App, Ref: ref, Commit: commit,
			JobName: c.Name, Conclusion: *c.Conclusion, StartedAt: c.StartedAt,
			CompletedAt: c.CompletedAt, JobURL: c.HTMLURL, Output: c.Output, Via: "poll",
			UpdatedAt: now,
		}
		attachOutputTail(&j)
		if err := store.PutGitHubCIJob(ctx, j); err != nil {
			log.Printf("orchestrator: recording GitHub check %q on %s: %v", c.Name, repo, err)
			continue
		}
		polledChecksSeen.Lock()
		if len(polledChecksSeen.m) >= polledChecksSeenMax {
			polledChecksSeen.m = map[string]bool{}
		}
		polledChecksSeen.m[key] = true
		polledChecksSeen.Unlock()
	}
}

// tailLedger is one store's record of the log reads it has spent this
// hour, and when it last pruned.
type tailLedger struct {
	mu        sync.Mutex
	fetches   []time.Time
	lastPrune time.Time
}

var tailLedgers sync.Map // *model.Store -> *tailLedger

func tailLedgerFor(store *model.Store) *tailLedger {
	l, _ := tailLedgers.LoadOrStore(store, &tailLedger{})
	return l.(*tailLedger)
}

// allowance is how many more log reads this hour has room for.
func (l *tailLedger) allowance(now time.Time) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.fetches[:0]
	for _, at := range l.fetches {
		if now.Sub(at) < time.Hour {
			kept = append(kept, at)
		}
	}
	l.fetches = kept
	return gitHubCITailsPerHour - len(kept)
}

func (l *tailLedger) spend(now time.Time) {
	l.mu.Lock()
	l.fetches = append(l.fetches, now)
	l.mu.Unlock()
}

// reconcileGitHubCITails reads the log tail of recently failed GitHub
// Actions jobs, within the hourly cap and only while the budget is not
// low, and applies retention to the recorded jobs.
func reconcileGitHubCITails(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil {
		return nil
	}
	ledger := tailLedgerFor(deps.Store)
	var errs []error
	ledger.mu.Lock()
	prune := now.Sub(ledger.lastPrune) >= gitHubCIPruneEvery
	if prune {
		ledger.lastPrune = now
	}
	ledger.mu.Unlock()
	if prune {
		if _, err := deps.Store.PruneGitHubCIJobs(ctx, now.Add(-gitHubCIRetention)); err != nil {
			errs = append(errs, err)
		}
	}

	tailer, ok := deps.Client.(github.JobLogTailer)
	if ok && deps.Client != nil && !gitHubCIBudgetLow(now) {
		if room := min(ledger.allowance(now), gitHubCITailsPerCycle); room > 0 {
			jobs, err := deps.Store.ListGitHubCIJobs(ctx, model.GitHubCIFilter{
				NeedsTail: true, Since: now.Add(-gitHubCITailMaxAge), Limit: room,
			})
			if err != nil {
				return errors.Join(append(errs, err)...)
			}
			for _, j := range jobs {
				if err := ctx.Err(); err != nil {
					errs = append(errs, err)
					break
				}
				owner, name, ok := splitStoredRepo(j.Repo)
				if !ok {
					continue
				}
				ledger.spend(now)
				tail, err := tailer.JobLogTail(owner, name, j.CheckRunID)
				if err != nil {
					// A transport failure: leave it unfetched for the next cycle,
					// which the hourly cap still bounds.
					errs = append(errs, fmt.Errorf("reading the log of GitHub job %s/%d: %w", j.Repo, j.CheckRunID, err))
					continue
				}
				source := ""
				if tail != "" {
					source = "log"
				}
				if err := deps.Store.SetGitHubCIFailureTail(ctx, j.Repo, j.CheckRunID, tail, source, now); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	if err := annotateGitHubSharedFaults(ctx, deps, now); err != nil {
		errs = append(errs, err)
	}
	if err := pollConcludedGitHubWorkflowRuns(ctx, deps, now); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// annotateGitHubSharedFaults correlates recent failed GitHub CI jobs that
// have a failure tail and annotates affected tasks and pull requests with
// the same shared-fault sentence and PR section grain-CI faults post.
func annotateGitHubSharedFaults(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil {
		return nil
	}
	links, err := deps.Store.OpenPullRequestLinks(ctx)
	if err != nil || len(links) == 0 {
		return err
	}
	jobs, err := deps.Store.ListGitHubCIJobs(ctx, model.GitHubCIFilter{
		Since: now.Add(-ci.DefaultFaultWindow),
	})
	if err != nil {
		return err
	}
	runs := ci.GitHubFaultRuns(jobs)
	faults := ci.SharedFaults(runs, now, ci.DefaultFaultWindow)
	if len(faults) == 0 {
		return nil
	}
	type branchKey struct{ repo, ref string }
	runsByBranch := make(map[branchKey]ci.Run, len(runs))
	for _, r := range runs {
		k := branchKey{repo: strings.ToLower(r.Repo), ref: r.Ref}
		cur, ok := runsByBranch[k]
		if !ok {
			runsByBranch[k] = r
			continue
		}
		if cur.Commit == "" && r.Commit != "" {
			cur.Commit = r.Commit
		}
		cur.Jobs = append(cur.Jobs, r.Jobs...)
		runsByBranch[k] = cur
	}

	fixes := ciFaultFixes(ctx, deps)
	var errs []error
	for _, link := range links {
		task, err := deps.Store.GetTask(ctx, link.TaskID)
		if err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: reading task %s: %w", link.TaskID, err))
			continue
		}
		if task == nil || task.Target == nil {
			continue
		}
		run, ok := runsByBranch[branchKey{repo: strings.ToLower(task.Target.String()), ref: task.BranchName()}]
		if !ok {
			continue
		}
		shared := ciSharedFaultSentence(run, faults)
		if shared == "" {
			continue
		}
		comments, err := deps.Store.Comments(ctx, task.ID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		alreadyNoted := false
		for _, c := range comments {
			if strings.Contains(c.Body, shared) {
				alreadyNoted = true
				break
			}
		}
		if alreadyNoted {
			continue
		}
		commit := shortSHA(run.Commit)
		taskBody := fmt.Sprintf("GitHub CI failure at `%s`.\n\n%s", commit, shared)
		if _, err := deps.Store.AddComment(ctx, model.Comment{
			TaskID:    task.ID,
			Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
			Body:      taskBody,
			CreatedAt: now,
		}); err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: recording GitHub CI shared fault on %s: %w", task.ID, err))
			continue
		}
		ref, ok := trackedPullRequest(*task)
		if !ok || deps.Client == nil {
			continue
		}
		prBody := strings.TrimLeft(ciCommentFaultSection(run, faults, fixes), "\n")
		if prBody == "" {
			continue
		}
		if _, err := deps.Client.CreateComment(ref.Repo.Owner, ref.Repo.Name, ref.Number, prBody); err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: commenting GitHub CI shared fault on %s: %w", ref, err))
		}
	}
	return errors.Join(errs...)
}

// gitHubRepairFaultSection returns the shared-fault paragraph for a repair
// comment when a GitHub-gated branch is part of a GitHub CI shared fault.
func gitHubRepairFaultSection(ctx context.Context, store *model.Store, task model.Task, headRef string, now time.Time) string {
	if store == nil || task.Target == nil {
		return ""
	}
	branch := strings.TrimSpace(headRef)
	if branch == "" {
		branch = task.BranchName()
	}
	jobs, err := store.ListGitHubCIJobs(ctx, model.GitHubCIFilter{
		Repo:  task.Target.String(),
		Since: now.Add(-ci.DefaultFaultWindow),
	})
	if err != nil || len(jobs) == 0 {
		return ""
	}
	runs := ci.GitHubFaultRuns(jobs)
	faults := ci.SharedFaults(runs, now, ci.DefaultFaultWindow)
	if len(faults) == 0 {
		return ""
	}
	var combined ci.Run
	found := false
	for _, r := range runs {
		if !strings.EqualFold(r.Repo, task.Target.String()) || r.Ref != branch {
			continue
		}
		if !found {
			combined = r
			found = true
			continue
		}
		combined.Jobs = append(combined.Jobs, r.Jobs...)
	}
	if !found {
		return ""
	}
	return ciCommentFaultSection(combined, faults, nil)
}

// splitStoredRepo is a GitHubCIJob.Repo ("owner/name", or
// "host/owner/name" off the default host) as the owner and name the
// GitHub client is called with.
func splitStoredRepo(repo string) (owner, name string, ok bool) {
	parts := strings.Split(repo, "/")
	if len(parts) < 2 {
		return "", "", false
	}
	owner, name = parts[len(parts)-2], parts[len(parts)-1]
	return owner, name, owner != "" && name != ""
}

// ConcludedGitHubWorkflowRun is grain's normalized "GitHub workflow run
// concluded" event, emitted from either a workflow_run webhook delivery or
// the completed-runs polling fallback.
type ConcludedGitHubWorkflowRun struct {
	Repo         model.RepoRef
	WorkflowName string
	WorkflowPath string
	RunID        int64
	Attempt      int
	Conclusion   string
	HeadSHA      string
	HeadRepo     string
	Ref          string
	Event        string
	HTMLURL      string
	StartedAt    *time.Time
	CompletedAt  *time.Time
	Via          string
}

// concludedGitHubRunsSeen deduplicates concluded workflow run signals per
// (Store, Repo, RunID, Attempt) so a webhook delivery and a poll (or
// redelivered webhook) for the same run attempt emit once.
var concludedGitHubRunsSeen = struct {
	sync.Mutex
	m map[string]bool
}{m: map[string]bool{}}

const concludedGitHubRunsSeenMax = 50000

func markConcludedGitHubRunSeen(store *model.Store, repo model.RepoRef, runID int64, attempt int) bool {
	if attempt <= 0 {
		attempt = 1
	}
	key := fmt.Sprintf("%p|%s|%d|%d", store, strings.ToLower(repo.String()), runID, attempt)
	concludedGitHubRunsSeen.Lock()
	defer concludedGitHubRunsSeen.Unlock()
	if concludedGitHubRunsSeen.m[key] {
		return false
	}
	if len(concludedGitHubRunsSeen.m) >= concludedGitHubRunsSeenMax {
		concludedGitHubRunsSeen.m = map[string]bool{}
	}
	concludedGitHubRunsSeen.m[key] = true
	return true
}

// recordGitHubWorkflowRunEvent handles an inbound workflow_run webhook delivery.
func recordGitHubWorkflowRunEvent(ctx context.Context, deps Deps, repo model.RepoRef, ev ghwebhook.Event, now time.Time) error {
	if ev.Action != "completed" || ev.WorkflowRun == 0 || ev.CheckConclusion == "" {
		return nil
	}
	attempt := ev.RunAttempt
	if attempt <= 0 {
		attempt = 1
	}
	run := ConcludedGitHubWorkflowRun{
		Repo:         repo,
		WorkflowName: ev.WorkflowName,
		WorkflowPath: ev.WorkflowPath,
		RunID:        ev.WorkflowRun,
		Attempt:      attempt,
		Conclusion:   strings.ToLower(strings.TrimSpace(ev.CheckConclusion)),
		HeadSHA:      ev.HeadSHA,
		HeadRepo:     ev.HeadRepo,
		Ref:          strings.TrimPrefix(strings.TrimSpace(ev.HeadRef), "refs/heads/"),
		Event:        ev.WorkflowEvent,
		HTMLURL:      ev.HTMLURL,
		StartedAt:    ev.CheckStartedAt,
		CompletedAt:  ev.CheckCompletedAt,
		Via:          "webhook",
	}
	return handleConcludedGitHubWorkflowRun(ctx, deps, run, now)
}

const gitHubCompletedRunsPollLimit = 20

// pollConcludedGitHubWorkflowRuns is the polling fallback for deployments
// whose webhook gateway GitHub cannot reach: it polls
// GET /repos/{owner}/{repo}/actions/runs?status=completed (ETag-cached and
// budget-aware) for target repositories that have a github_workflow_run
// schedule or a tracked base branch.
func pollConcludedGitHubWorkflowRuns(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil || deps.Client == nil || gitHubCIBudgetLow(now) {
		return nil
	}
	reader, ok := deps.Client.(github.WorkflowRunsReader)
	if !ok {
		return nil
	}
	repos, err := reposWantingGitHubWorkflowRunPoll(ctx, deps.Store)
	if err != nil || len(repos) == 0 {
		return err
	}
	var errs []error
	for _, repo := range repos {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		if gitHubCIBudgetLow(now) {
			break
		}
		runs, err := reader.CompletedWorkflowRuns(repo.Owner, repo.Name, gitHubCompletedRunsPollLimit)
		if err != nil {
			errs = append(errs, fmt.Errorf("polling completed GitHub workflow runs on %s: %w", repo, err))
			continue
		}
		// Process oldest first so consecutive runs land in order.
		for i := len(runs) - 1; i >= 0; i-- {
			r := runs[i]
			if r.ID == 0 || r.Status != "completed" || r.Conclusion == "" {
				continue
			}
			attempt := r.RunAttempt
			if attempt <= 0 {
				attempt = 1
			}
			concluded := ConcludedGitHubWorkflowRun{
				Repo:         repo,
				WorkflowName: r.Name,
				WorkflowPath: r.Path,
				RunID:        r.ID,
				Attempt:      attempt,
				Conclusion:   strings.ToLower(strings.TrimSpace(r.Conclusion)),
				HeadSHA:      r.HeadSHA,
				HeadRepo:     r.HeadRepo,
				Ref:          strings.TrimPrefix(strings.TrimSpace(r.HeadBranch), "refs/heads/"),
				Event:        r.Event,
				HTMLURL:      r.HTMLURL,
				StartedAt:    r.StartedAt,
				CompletedAt:  r.UpdatedAt,
				Via:          "poll",
			}
			if err := handleConcludedGitHubWorkflowRun(ctx, deps, concluded, now); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func reposWantingGitHubWorkflowRunPoll(ctx context.Context, store *model.Store) ([]model.RepoRef, error) {
	allowed, err := store.TargetRepos(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []model.RepoRef
	add := func(r model.RepoRef) {
		if r.Owner == "" || r.Name == "" || model.IsConfigRepo(r) {
			return
		}
		if len(allowed) > 0 && !model.RepoAllowed(allowed, r) {
			return
		}
		key := strings.ToLower(r.String())
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, r)
	}
	schedules, err := store.ListSchedules(ctx)
	if err != nil {
		return nil, err
	}
	for _, s := range schedules {
		if s.Enabled && s.Recurrence.Kind == model.RecurrenceGitHubWorkflowRun {
			add(s.Target)
		}
	}
	if bci, err := store.ListBaseBranchCI(ctx); err == nil {
		for _, row := range bci {
			add(row.Repo)
		}
	}
	if brel, err := store.ListBaseBranchRelease(ctx); err == nil {
		for _, row := range brel {
			add(row.Repo)
		}
	}
	return out, nil
}

// isGitHubWorkflowRunForeignHead reports whether a workflow run's head
// repository is explicitly a repository other than repo (such as a pull
// request opened from a fork).
func isGitHubWorkflowRunForeignHead(repo model.RepoRef, headRepo string) bool {
	hr := strings.TrimSpace(headRepo)
	if hr == "" {
		return false
	}
	fullName := repo.Owner + "/" + repo.Name
	return !strings.EqualFold(hr, fullName) && !strings.EqualFold(hr, repo.String())
}

// isGitHubBranchWorkflowEvent reports whether a GitHub Actions workflow_run's
// triggering event names a branch of the repository itself -- never
// pull_request or pull_request_target, whose head_branch is the pull request's
// head ref rather than a branch of the base repository.
func isGitHubBranchWorkflowEvent(event string) bool {
	switch strings.ToLower(strings.TrimSpace(event)) {
	case "push", "schedule", "workflow_dispatch", "release", "merge_group":
		return true
	default:
		return false
	}
}

// handleConcludedGitHubWorkflowRun deduplicates a concluded workflow run by
// (repo, run_id, attempt), ensures its jobs and failure tails are recorded in
// github_ci_job, fires any matching github_workflow_run schedules, and checks
// release triage parity on the base branch.
func handleConcludedGitHubWorkflowRun(ctx context.Context, deps Deps, run ConcludedGitHubWorkflowRun, now time.Time) error {
	if deps.Store == nil || run.RunID == 0 || run.Conclusion == "" {
		return nil
	}
	if isGitHubWorkflowRunForeignHead(run.Repo, run.HeadRepo) {
		sayOnce("github-workflow-run-fork:"+strings.ToLower(run.Repo.String())+":"+strings.ToLower(strings.TrimSpace(run.HeadRepo)),
			"orchestrator: ignoring concluded GitHub workflow run %d on %s from head repository %s",
			run.RunID, run.Repo, strings.TrimSpace(run.HeadRepo))
		return nil
	}
	if !isGitHubBranchWorkflowEvent(run.Event) {
		return nil
	}
	if !markConcludedGitHubRunSeen(deps.Store, run.Repo, run.RunID, run.Attempt) {
		return nil
	}
	jobs, err := ensureGitHubWorkflowRunJobs(ctx, deps, run, now)
	if err != nil {
		log.Printf("orchestrator: reading jobs of GitHub workflow run %d on %s: %v", run.RunID, run.Repo, err)
	}
	var errs []error
	if err := fireGitHubWorkflowRunSchedules(ctx, deps, run, jobs, now); err != nil {
		errs = append(errs, err)
	}
	if err := recordGitHubBaseBranchRelease(ctx, deps, run, jobs, now); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// ensureGitHubWorkflowRunJobs makes sure github_ci_job has the concluded run's
// jobs and failure tails so review and release-triage tasks have the failing
// jobs, steps, and log tails immediately in their prompt and via grain ci log.
func ensureGitHubWorkflowRunJobs(ctx context.Context, deps Deps, run ConcludedGitHubWorkflowRun, now time.Time) ([]model.GitHubCIJob, error) {
	if isGitHubWorkflowRunForeignHead(run.Repo, run.HeadRepo) || !isGitHubBranchWorkflowEvent(run.Event) {
		return nil, nil
	}
	if reader, ok := deps.Client.(github.WorkflowRunsReader); ok && deps.Client != nil {
		ghJobs, err := reader.ListWorkflowRunJobs(run.Repo.Owner, run.Repo.Name, run.RunID)
		if err != nil {
			return nil, err
		}
		tailer, hasTailer := deps.Client.(github.JobLogTailer)
		for _, gj := range ghJobs {
			if gj.ID == 0 {
				continue
			}
			conclusion := strings.ToLower(strings.TrimSpace(gj.Conclusion))
			if conclusion == "" {
				conclusion = run.Conclusion
			}
			j := model.GitHubCIJob{
				Repo:        run.Repo.String(),
				CheckRunID:  gj.ID,
				Source:      model.GitHubCISource,
				App:         model.GitHubActionsApp,
				Ref:         run.Ref,
				Commit:      run.HeadSHA,
				Workflow:    run.WorkflowName,
				RunID:       run.RunID,
				RunAttempt:  run.Attempt,
				JobName:     gj.Name,
				Conclusion:  conclusion,
				StartedAt:   gj.StartedAt,
				CompletedAt: gj.CompletedAt,
				FailingStep: gj.FailingStep,
				RunURL:      run.HTMLURL,
				JobURL:      gj.HTMLURL,
				Via:         run.Via,
				UpdatedAt:   now,
			}
			if j.RunURL == "" && j.JobURL != "" {
				j.RunURL = runURLOf(j.JobURL)
			}
			if err := deps.Store.PutGitHubCIJob(ctx, j); err != nil {
				return nil, err
			}
			if j.Failed() && hasTailer && !gitHubCIBudgetLow(now) {
				existing, _ := deps.Store.GetGitHubCIJob(ctx, j.Repo, j.CheckRunID)
				if existing == nil || (!existing.TailFetched && existing.FailureTail == "") {
					if tail, err := tailer.JobLogTail(run.Repo.Owner, run.Repo.Name, gj.ID); err == nil {
						source := ""
						if tail != "" {
							source = "log"
						}
						_ = deps.Store.SetGitHubCIFailureTail(ctx, j.Repo, j.CheckRunID, tail, source, now)
					}
				}
			}
		}
	}
	jobs, err := deps.Store.ListGitHubCIJobs(ctx, model.GitHubCIFilter{
		Repo:  run.Repo.String(),
		RunID: run.RunID,
	})
	if err != nil {
		return nil, err
	}
	if len(jobs) == 0 {
		// Synthesize a run-level job record when no per-job records were
		// delivered or fetched (e.g. startup_failure or webhook-only without
		// a job listing) so grain ci log <run_id> and the review prompt still
		// have the run's record.
		synth := model.GitHubCIJob{
			Repo:        run.Repo.String(),
			CheckRunID:  run.RunID,
			Source:      model.GitHubCISource,
			App:         model.GitHubActionsApp,
			Ref:         run.Ref,
			Commit:      run.HeadSHA,
			Workflow:    run.WorkflowName,
			RunID:       run.RunID,
			RunAttempt:  run.Attempt,
			JobName:     firstNonEmpty(run.WorkflowName, "workflow"),
			Conclusion:  run.Conclusion,
			StartedAt:   run.StartedAt,
			CompletedAt: run.CompletedAt,
			RunURL:      run.HTMLURL,
			JobURL:      run.HTMLURL,
			Via:         run.Via,
			UpdatedAt:   now,
		}
		_ = deps.Store.PutGitHubCIJob(ctx, synth)
		jobs = []model.GitHubCIJob{synth}
	} else {
		tailer, hasTailer := deps.Client.(github.JobLogTailer)
		if hasTailer && deps.Client != nil && !gitHubCIBudgetLow(now) {
			for i := range jobs {
				if jobs[i].Failed() && !jobs[i].TailFetched && jobs[i].FailureTail == "" {
					if tail, err := tailer.JobLogTail(run.Repo.Owner, run.Repo.Name, jobs[i].CheckRunID); err == nil {
						source := ""
						if tail != "" {
							source = "log"
						}
						_ = deps.Store.SetGitHubCIFailureTail(ctx, jobs[i].Repo, jobs[i].CheckRunID, tail, source, now)
						jobs[i].FailureTail = tail
						jobs[i].TailSource = source
						jobs[i].TailFetched = true
					}
				}
			}
		}
	}
	return jobs, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// gitHubWorkflowRunDetail summarizes a concluded GitHub workflow run and its
// failing jobs/steps in one line for ScheduledWorkflowRun.Detail and
// BaseBranchRelease.Detail.
func gitHubWorkflowRunDetail(run ConcludedGitHubWorkflowRun, jobs []model.GitHubCIJob) string {
	var failed []string
	for _, j := range jobs {
		if !j.Failed() {
			continue
		}
		if j.FailingStep != "" {
			failed = append(failed, fmt.Sprintf("%s (step %q: %s)", j.JobName, j.FailingStep, j.Conclusion))
		} else {
			failed = append(failed, fmt.Sprintf("%s (%s)", j.JobName, j.Conclusion))
		}
	}
	if len(failed) > 0 {
		return fmt.Sprintf("GitHub Actions run %d concluded %s: failed jobs: %s",
			run.RunID, run.Conclusion, strings.Join(failed, ", "))
	}
	return fmt.Sprintf("GitHub Actions run %d concluded %s", run.RunID, run.Conclusion)
}

// gitHubWorkflowRunSummarySection formats the run URL, conclusion, failing
// jobs/steps, and failure tails from github_ci_job for inclusion in the filed
// review or triage task's prompt and opening comment.
func gitHubWorkflowRunSummarySection(run ConcludedGitHubWorkflowRun, jobs []model.GitHubCIJob) string {
	var b strings.Builder
	runIDStr := fmt.Sprintf("%d", run.RunID)
	wfLabel := firstNonEmpty(run.WorkflowPath, run.WorkflowName, "workflow")
	fmt.Fprintf(&b, "GitHub Actions workflow run `%s` (attempt %d) of `%s` on `%s` (branch `%s`, commit `%s`) concluded **%s**.\n",
		runIDStr, run.Attempt, wfLabel, run.Repo, run.Ref, shortSHA(run.HeadSHA), run.Conclusion)
	if run.HTMLURL != "" {
		fmt.Fprintf(&b, "- Run URL: %s\n", run.HTMLURL)
	}
	fmt.Fprintf(&b, "- Conclusion: `%s`\n", run.Conclusion)
	var failedJobs []model.GitHubCIJob
	for _, j := range jobs {
		if j.Failed() {
			failedJobs = append(failedJobs, j)
		}
	}
	if len(failedJobs) > 0 {
		b.WriteString("- Failing jobs:\n")
		for _, j := range failedJobs {
			line := fmt.Sprintf("  - `%s`: `%s`", j.JobName, j.Conclusion)
			if j.FailingStep != "" {
				line += fmt.Sprintf(" at step `%s`", j.FailingStep)
			}
			if j.JobURL != "" {
				line += fmt.Sprintf(" (%s)", j.JobURL)
			}
			b.WriteString(line + "\n")
		}
		for _, j := range failedJobs {
			if strings.TrimSpace(j.FailureTail) == "" {
				continue
			}
			fmt.Fprintf(&b, "\n### Failure tail: `%s`", j.JobName)
			if j.FailingStep != "" {
				fmt.Fprintf(&b, " (step `%s`)", j.FailingStep)
			}
			b.WriteString("\n```\n")
			b.WriteString(strings.TrimRight(j.FailureTail, "\n"))
			b.WriteString("\n```\n")
		}
	}
	fmt.Fprintf(&b, "\nInspect this run's jobs and logs with `grain ci show %s` and `grain ci log %s`.\n",
		runIDStr, runIDStr)
	return b.String()
}
