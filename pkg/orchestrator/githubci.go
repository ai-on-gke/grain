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

	"github.com/bwsalmon/grain/pkg/ci"
	"github.com/bwsalmon/grain/pkg/ghwebhook"
	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/model"
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
