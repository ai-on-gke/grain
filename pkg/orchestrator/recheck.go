package orchestrator

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// Flaky failures, and the second automatic repair.
//
// In the merge metrics for 30 Sep - 3 Oct 2026 about 30% of merge batches
// failed on their first run, and of 19 bisections only one named a
// culprit -- six answered nothing at all. That is the pattern of flaky
// tests rather than of bad changes, and each one cost a pull request its
// single automatic repair and then a person's "Retry merge".
//
// So a red grain CI verdict is asked about twice before it is charged to
// anybody. When a pull request's required run, or a merge batch's run,
// fails, the failing jobs are run again once, uncached
// (ci.Request.Recheck -- the mechanism retrigger_ci uses, whose result
// replaces the cached red it re-ran). A job that passes the second time is
// a flake: the verdict is green, the flake is recorded on the task and in
// grain CI's test history (ci.Runner.MarkFlake), and nothing is spent. Only a
// job that fails both times counts as the change's failure. A batch is
// rechecked before it is bisected, so a flaky batch merges rather than
// being split down to an innocent member.
//
// The repair policy moves too, by exactly one: a pull request may have a
// second automatic repair, but only when something has changed since the
// first -- the base branch has new commits, or the set of failing jobs and
// checks is different -- and never a third (maxAutomaticRepairs). A second
// repair asked about the same failure on the same base would be the
// "suggesting a fix for a fix" chain requeueForRepair declines; one asked
// about new facts is a new question.

// maxAutomaticRepairs is the most automatic repairs the merge queue sends
// one pull request on before a person has to look. The second is only
// given when secondRepairReason finds something changed.
const maxAutomaticRepairs = model.MaxAutomaticRepairs

// recheckTargets is what an uncached recheck of a failing run re-runs:
// every job that did not pass (jobsToRecheck's rule and spelling), less
// the ones named in inherited -- red the base branch has too, which a
// second sample cannot clear and which is already off this change's
// account.
func recheckTargets(run ci.Run, inherited []string) []string {
	skip := map[string]bool{}
	for _, name := range inherited {
		skip[name] = true
	}
	var out []string
	for _, job := range run.Jobs {
		if job.Conclusion == ci.ConclusionSuccess || job.JobID == "" || skip[ciJobName(job)] {
			continue
		}
		if job.WorkflowPath == "" {
			out = append(out, job.JobID)
			continue
		}
		out = append(out, job.WorkflowPath+":"+job.JobID)
	}
	return out
}

// recheckJobsOf reads the job names back out of a failing, timed-out
// (cancelled), or errored CI detail line ("failure: <workflow>/<job>, ...",
// "cancelled: <workflow>/<job>, ...", "errored: <workflow>/<job>, ...", or
// "grain CI could not run N job(s) (<workflow>/<job>, ...)").
func recheckJobsOf(detail string) []string {
	bestIdx := -1
	bestMarker := ""
	for _, marker := range []string{"failure: ", "cancelled: ", "errored: "} {
		if i := strings.LastIndex(detail, marker); i > bestIdx {
			bestIdx = i
			bestMarker = marker
		}
	}
	raw := ""
	if bestIdx >= 0 {
		raw = detail[bestIdx+len(bestMarker):]
		if bestMarker == "cancelled: " && strings.HasPrefix(strings.TrimSpace(raw), "superseded") {
			return nil
		}
	} else if strings.Contains(detail, "grain CI could not run ") {
		const open = "job(s) ("
		if i := strings.Index(detail, open); i >= 0 {
			rest := detail[i+len(open):]
			if j := strings.IndexByte(rest, ')'); j >= 0 {
				raw = rest[:j]
			}
		}
	}
	if raw == "" {
		return nil
	}
	var out []string
	for _, name := range strings.Split(raw, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func firstConclusionOf(detail string) ci.Conclusion {
	switch {
	case strings.HasPrefix(detail, "cancelled: "):
		return ci.ConclusionCancelled
	case strings.HasPrefix(detail, "errored: "), strings.HasPrefix(detail, "grain CI could not run"):
		return ci.ConclusionErrored
	default:
		return ci.ConclusionFailure
	}
}

// recheckVerdict combines a failing run's verdict (firstDetail, ciDetail's
// "failure: <job>, <job>") with the recheck of some of its jobs: a job
// re-run and passed is a flake, a job re-run and failed again is a
// failure, and a job that was not re-run (an inherited one) still fails.
// It returns the combined conclusion and detail, and the jobs that
// flaked, by ciJobName.
//
// A recheck that neither passed nor failed -- errored, cancelled -- is no
// second sample, and the first verdict stands as it was.
func recheckVerdict(firstDetail string, recheck ci.Run) (ci.Conclusion, string, []string) {
	if recheck.Conclusion != ci.ConclusionSuccess && recheck.Conclusion != ci.ConclusionFailure {
		return firstConclusionOf(firstDetail), firstDetail, nil
	}
	rechecked := map[string]bool{}
	for _, job := range recheck.Jobs {
		rechecked[ciJobName(job)] = true
	}
	again := map[string]bool{}
	for _, name := range failedCIJobs(recheck) {
		again[name] = true
	}
	// A first verdict that names no job -- a run that could not plan, or
	// one cancelled with a reason rather than job names -- has nothing a
	// recheck could have re-run, so no recheck turns it green.
	firstJobs := recheckJobsOf(firstDetail)
	if len(firstJobs) == 0 {
		return firstConclusionOf(firstDetail), firstDetail, nil
	}
	var failing, flaked []string
	for _, name := range firstJobs {
		switch {
		case !rechecked[name]:
			failing = append(failing, name)
		case again[name]:
			failing = append(failing, name)
			delete(again, name)
		default:
			flaked = append(flaked, name)
		}
	}
	// A job failing now that the first run did not name: one that had
	// been skipped behind a failure the first time and has now run.
	for _, name := range failedCIJobs(recheck) {
		if again[name] {
			failing = append(failing, name)
		}
	}
	if len(failing) == 0 {
		return ci.ConclusionSuccess, fmt.Sprintf("success: %s failed once and passed an uncached recheck",
			strings.Join(flaked, ", ")), flaked
	}
	conclusion := ci.ConclusionFailure
	if recheck.Conclusion != ci.ConclusionFailure {
		conclusion = firstConclusionOf(firstDetail)
	}
	return conclusion, fmt.Sprintf("%s: %s", conclusion, strings.Join(failing, ", ")), flaked
}

// flakedTests names the failed tests the flaked jobs of run reported
// (ci.JobRun.FailureSummary), and planIDs their plan-wide ids.
func flakedTests(run ci.Run, flaked []string) (tests, planIDs []string) {
	want := map[string]bool{}
	for _, name := range flaked {
		want[name] = true
	}
	seen := map[string]bool{}
	for _, job := range run.Jobs {
		if !want[ciJobName(job)] {
			continue
		}
		if job.WorkflowPath != "" {
			planIDs = append(planIDs, job.WorkflowPath+":"+job.JobID)
		} else {
			planIDs = append(planIDs, job.JobID)
		}
		if job.FailureSummary == nil {
			continue
		}
		for _, tf := range job.FailureSummary.Tests {
			if tf.Name != "" && !seen[tf.Name] {
				seen[tf.Name] = true
				tests = append(tests, tf.Name)
			}
		}
	}
	return tests, planIDs
}

// recordFlake marks the flaked jobs of the first run as flakes in grain
// CI's own record (ci.Runner.MarkFlake), which is what query_test_history and
// the quarantine decisions read, and returns the tests they named. A
// first run this daemon no longer holds has nothing to mark; the comment
// the caller writes still names the jobs.
func recordFlake(deps Deps, firstRunID, recheckRunID string, flaked []string) []string {
	if len(flaked) == 0 || deps.CI == nil {
		return nil
	}
	first, ok := deps.CI.Get(firstRunID)
	if !ok {
		return nil
	}
	tests, ids := flakedTests(first, flaked)
	if len(ids) > 0 {
		deps.CI.MarkFlake(firstRunID, ids, recheckRunID)
	}
	return tests
}

// flakeComment is the queue comment a flake leaves on a task: which job,
// which test, and that nothing was spent on it.
func flakeComment(what, commit string, flaked, tests []string, outcome string) string {
	body := fmt.Sprintf("grain CI failed %s on %s at %s, and an uncached recheck of the same "+
		"commit passed, so the merge queue is treating it as a flake rather than as this change's "+
		"failure: %s No automatic repair was spent on it.",
		"`"+strings.Join(flaked, "`, `")+"`", what, shortSHA(commit), outcome)
	if len(tests) > 0 {
		body += fmt.Sprintf(" The failing test(s): `%s`.", strings.Join(tests, "`, `"))
	}
	return body + " It is recorded as a flake in grain CI's test history (query_test_history)."
}

// startCIRecheck re-runs, uncached, the jobs of a task's required run
// that failed for reasons of its own, before that failure is recorded as
// the verdict -- see the top of this file. It reports whether it started
// one; a commit is rechecked once (model.CIStatus.RecheckOf), and a
// failure with nothing of its own to re-run is recorded as it stands.
func startCIRecheck(ctx context.Context, deps Deps, task model.Task, status model.CIStatus,
	run ci.Run, now time.Time) (bool, error) {

	if status.RecheckOf != "" || task.Target == nil {
		return false, nil
	}
	detail := ciDetail(run)
	if len(failedJobsOf(detail)) == 0 {
		return false, nil
	}
	// Not where the change's merge rules say a flaky-looking failure is
	// to be taken as real (`flake_recheck: false`, grain/task-1660).
	if why := flakeRecheckRefusal(task); why != "" {
		log.Printf("orchestrator: task %s: grain CI run %s failed at %s (%s); not rechecking it: %s",
			task.ID, run.ID, shortSHA(status.Commit), detail, why)
		return false, nil
	}
	started, rechecking, recheck, err := askCIRecheck(ctx, deps, task, status, run, detail, now)
	if err != nil || !started {
		return started, err
	}
	// A recheck that is already over by the time it is started -- one the
	// service refused to plan, and answered with a concluded run -- is
	// collected now rather than a cycle later.
	if recheck.Conclusion.Done() {
		return true, collectCIRecheck(ctx, deps, task, rechecking, recheck, false, now)
	}
	return true, nil
}

func askCIRecheck(ctx context.Context, deps Deps, task model.Task, status model.CIStatus,
	run ci.Run, detail string, now time.Time) (bool, model.CIStatus, ci.Run, error) {

	jobs := recheckTargets(run, nil)
	if len(jobs) == 0 {
		return false, status, ci.Run{}, nil
	}
	recheck, err := deps.CI.Start(ctx, ci.Request{
		Repo: task.Target.String(), Ref: task.BranchName(), Commit: status.Commit,
		Event: ci.EventPush, RequestedBy: task.ID,
		Jobs: jobs, Recheck: true,
	})
	if err != nil {
		// Not a reason to hold the verdict: the failure is recorded as it
		// stands, which is what happened before rechecks existed.
		log.Printf("orchestrator: task %s: rechecking %s at %s: %v", task.ID,
			strings.Join(jobs, ", "), shortSHA(status.Commit), err)
		return false, status, ci.Run{}, nil
	}
	cancelSupersededBranchRuns(deps.CI, task.Target.String(), task.BranchName(), status.Commit, recheck.ID, nil)
	rechecking := model.CIStatus{
		RunID: recheck.ID, Commit: status.Commit, StartedAt: status.StartedAt,
		Attempts: status.Attempts, PreviousConclusion: status.PreviousConclusion,
		PreviousDetail: status.PreviousDetail, RecheckOf: run.ID, RecheckDetail: detail,
	}
	started := false
	if err := observeField(ctx, deps.Store, task.ID, now, func(o *model.Observation) {
		if o.CI == nil || o.CI.RunID != status.RunID || o.CI.Done() {
			return
		}
		next := rechecking
		o.CI = &next
		started = true
	}); err != nil {
		return false, status, ci.Run{}, err
	}
	// Counted as merge queue contention only for a change the queue is
	// driving: a recheck of a branch nobody asked to merge automatically
	// is a fact about CI, not about the queue.
	if started && task.AutoMerge {
		noteContention(ctx, deps.Store, *task.Target, task.ID, model.MergeQueueRetest, model.MergeQueueFlakeRecheck, now)
	}
	if started {
		log.Printf("orchestrator: task %s: grain CI run %s failed at %s (%s); re-running %s uncached "+
			"(run %s) before charging it to the change", task.ID, run.ID, shortSHA(status.Commit),
			detail, strings.Join(jobs, ", "), recheck.ID)
	}
	return started, rechecking, recheck, nil
}

// collectCIRecheck records the verdict of a task's recheck run, combined
// with the run it re-checked (recheckVerdict). A flake is said on the
// task and marked in the test history; a failure that reproduced is
// announced the way any red verdict is. lost is a recheck run nobody is
// going to conclude, which leaves the first failure standing rather than
// starting the whole suite again.
func collectCIRecheck(ctx context.Context, deps Deps, task model.Task, status model.CIStatus,
	run ci.Run, lost bool, now time.Time) error {

	conclusion, detail, flaked := firstConclusionOf(status.RecheckDetail), status.RecheckDetail, []string(nil)
	if !lost {
		conclusion, detail, flaked = recheckVerdict(status.RecheckDetail, run)
	}
	finished := run.FinishedAt
	if finished == nil || lost {
		finished = &now
	}
	recorded := false
	if err := observeField(ctx, deps.Store, task.ID, now, func(o *model.Observation) {
		if o.CI == nil || o.CI.RunID != status.RunID || o.CI.Done() {
			return
		}
		o.CI.Conclusion = string(conclusion)
		o.CI.FinishedAt = finished
		o.CI.Detail = detail
		recorded = true
	}); err != nil {
		return err
	}
	if !recorded {
		return nil
	}
	log.Printf("orchestrator: task %s: recheck %s of grain CI run %s: %s at %s -- %s",
		task.ID, status.RunID, status.RecheckOf, conclusion, shortSHA(status.Commit), detail)
	if len(flaked) > 0 {
		tests := recordFlake(deps, status.RecheckOf, status.RunID, flaked)
		outcome := "this commit's grain CI reads green."
		if conclusion != ci.ConclusionSuccess {
			outcome = "what is still red is " + detail + "."
		}
		if err := queueComment(ctx, deps.Store, task.ID,
			flakeComment("this branch", status.Commit, flaked, tests, outcome), now); err != nil {
			return err
		}
	}
	if conclusion == ci.ConclusionSuccess {
		return nil
	}
	// Red, announced as any red verdict is -- with the run whose failing
	// steps say most: the recheck where it ran, the first run otherwise.
	announce := run
	if lost || run.Conclusion != ci.ConclusionFailure {
		first, ok := deps.CI.Get(status.RecheckOf)
		if !ok {
			return nil
		}
		announce = first
	}
	return announceCIRun(ctx, deps, task, announce, status.PreviousConclusion, status.PreviousDetail, now)
}

// --- the second automatic repair ---------------------------------------

// failureSignature is what a pull request is failing on, as one sorted,
// comma-separated string that two repairs' worth of it can be compared
// by: the grain CI jobs, the GitHub checks where those are read, and a
// conflict where there is one.
func failureSignature(e queueEntry) string {
	var parts []string
	if e.health == model.PrConflicted {
		parts = append(parts, "merge conflict")
	}
	if e.grain.Failed {
		parts = append(parts, failedJobsOf(e.grain.Detail)...)
	}
	parts = append(parts, failingCheckNames(e)...)
	return signatureOf(parts)
}

func signatureOf(parts []string) string {
	seen := map[string]bool{}
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// secondRepairReason says why this pull request may have a second
// automatic repair, or "" where it may not: it has had exactly one, and
// since that one was asked for the base branch has new commits (baseTip
// differs from the base it was asked against) or the set of failing jobs
// and checks is different. A third is never given, and a repair recorded
// before what it was asked about was kept has nothing to compare against
// and gets no second.
func secondRepairReason(obs *model.Observation, base, baseTip, failures string) string {
	if obs.RepairsSpent() != 1 {
		return ""
	}
	var why []string
	if baseTip != "" && obs.MergeQueueRepairBase != "" && baseTip != obs.MergeQueueRepairBase {
		why = append(why, fmt.Sprintf("`%s` has new commits since the first repair (%s, was %s)",
			base, shortSHA(baseTip), shortSHA(obs.MergeQueueRepairBase)))
	}
	if failures != "" && obs.MergeQueueRepairFailures != "" && failures != obs.MergeQueueRepairFailures {
		why = append(why, fmt.Sprintf("what is failing has changed since the first repair (now %s; "+
			"then %s)", failures, obs.MergeQueueRepairFailures))
	}
	return strings.Join(why, ", and ")
}

// repairAttemptSentence is what a repair's comment says about which
// attempt it is out of the max the pull request may have (repairCap),
// and -- for the second -- why it was allowed.
func repairAttemptSentence(n, max int, why string) string {
	s := fmt.Sprintf("This is automatic repair %d of %d.", n, max)
	if n > 1 && why != "" {
		s = fmt.Sprintf("This is automatic repair %d of %d, allowed because %s.", n, max, why)
	}
	return s
}

// repairAttemptNote is the same for the row (query_merge_queue's note):
// "automatic repair 2 of 2: <why>".
func repairAttemptNote(obs *model.Observation, max int) string {
	n := obs.RepairsSpent()
	if n == 0 {
		return ""
	}
	note := fmt.Sprintf("automatic repair %d of %d", n, max)
	if n > 1 && obs.MergeQueueRepairWhy != "" {
		note += ", allowed because " + obs.MergeQueueRepairWhy
	}
	return note
}

// repairsSpentSentence is what a block says about the repairs a pull
// request has already had: all it may have, or one with nothing changed
// since that a second could be asked about.
func repairsSpentSentence(spent int, base string, rc repairCap) string {
	switch {
	case rc.max == 0:
		return fmt.Sprintf("The repository's merge rules say `repairs: 0` for the paths this pull "+
			"request touches (from %s): a failure goes straight to a person, with no automatic repair.",
			describeRuleSources(rc.from))
	case spent >= rc.max && len(rc.from) > 0:
		return fmt.Sprintf("This pull request has already had automatic repair %d of %d, which is the "+
			"most its merge rules allow (`repairs: %d`, from %s).", spent, rc.max, rc.max,
			describeRuleSources(rc.from))
	case spent >= rc.max:
		return fmt.Sprintf("This pull request has already had automatic repair %d of %d, which is the "+
			"most the merge queue gives.", spent, rc.max)
	}
	return fmt.Sprintf("This pull request has already had an automatic repair, and a second is only "+
		"given when something has changed since -- new commits on %s, or a different set of "+
		"failing jobs -- and neither has.", base)
}

// repairsSpentReason is the same for the row, after "and ".
func repairsSpentReason(spent int, rc repairCap) string {
	switch {
	case rc.max == 0:
		return "its merge rules allow no automatic repair (`repairs: 0`, from " + describeRuleSources(rc.from) + ")"
	case spent >= rc.max && len(rc.from) > 0:
		return fmt.Sprintf("its merge rules allow %d automatic repair(s) (`repairs: %d`, from %s)",
			rc.max, rc.max, describeRuleSources(rc.from))
	case spent >= rc.max:
		return fmt.Sprintf("%d automatic repairs are the most a pull request gets", rc.max)
	}
	return "nothing has changed since its automatic repair that a second one could be asked about"
}

// markRepairAsked is the bookkeeping every automatic repair writes beside
// MergeQueueRepairAt: one more spent, and what it was asked about.
func markRepairAsked(o *model.Observation, now time.Time, baseTip, failures, why string) {
	o.MergeQueueRepairs = o.RepairsSpent() + 1
	o.MergeQueueRepairAt = &now
	o.MergeQueueRepairBase = baseTip
	o.MergeQueueRepairFailures = failures
	o.MergeQueueRepairWhy = why
	o.CompletedAt = nil
	o.RetryRequestedAt = &now
}

// failedJobsOf reads the job names back out of ciDetail's sentence. The
// base branch's own row may carry a prefix of grain's in front of it
// ("grain stopped this run itself: failure: ..."), so the last "failure: "
// is the one that starts the list.
func failedJobsOf(detail string) []string {
	const marker = "failure: "
	i := strings.LastIndex(detail, marker)
	if i < 0 {
		return nil
	}
	var jobs []string
	for _, job := range strings.Split(detail[i+len(marker):], ",") {
		if job = strings.TrimSpace(job); job != "" {
			jobs = append(jobs, job)
		}
	}
	return jobs
}
