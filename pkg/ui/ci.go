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

package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/apierror"
	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// Config.CI is grain CI as these routes take it: ci.Runner, the whole of
// what the CI service answers (docs/design/grit-split.md#GS-1) --
// cmd/grain's ciGate in a real deployment.
//
// An interface so a test can drive these routes with no sandbox
// backend, and so a deployment with no CI wired hands in nil. It is
// stated in pkg/ci's own types rather than in types of this package's,
// unlike SandboxHealth and CycleTimes, because a CI run is not a shape
// this package renders into something else -- it is served as it stands,
// and repeating a dozen result fields here would be a second definition
// to keep in step for no reader's benefit. pkg/ci imports nothing this
// package would have to avoid (no orchestrator, no dispatch), which is
// what makes that safe.
//
// A runner that keeps no artifacts or no job cache answers ok=false for
// them (ci.NopRunner's answers), and the routes below say "this
// deployment keeps none" rather than failing.

// CIRunArtifactsResponse is what GET /api/ci/runs/{id}/artifacts answers
// with.
//
// Three states again, the shape CICacheResponse settled on: a deployment
// with no CI at all, one that keeps no artifacts, and one that does and
// has these. A reader who is told "none" wants to know which of the
// three they are looking at before they go hunting for a workflow bug.
type CIRunArtifactsResponse struct {
	// Enabled is whether this deployment has grain CI wired at all.
	Enabled bool `json:"enabled"`
	// Available is whether it keeps artifacts. False with Enabled true is
	// a deployment whose artifact directory could not be opened; the
	// daemon log says why (pkg/cli/ci.go).
	Available bool `json:"available"`
	// Artifacts is what this run still has, oldest run sweeps and all.
	Artifacts []ci.Artifact `json:"artifacts"`
}

// CIRunRequest is the body of POST /api/ci/runs: a repository, a
// branch, and optionally which event to run as and which commit the
// branch was at.
//
// Repo and Ref are the whole of what a caller has to know, which is the
// shape this was asked for in: "the api should take a repo and branch".
type CIRunRequest struct {
	// TaskID asks for the task-scoped form used by an agent. The server
	// derives Repo and Ref from that task and verifies its grain-ci grant.
	TaskID string `json:"taskId,omitempty"`
	Repo   string `json:"repo"`
	// Ref is the branch. "branch" is accepted as a spelling of the same
	// field, since half of everything else in this API calls it that.
	Ref    string `json:"ref"`
	Branch string `json:"branch,omitempty"`
	// Commit is the SHA the branch was at when the caller asked. Worth
	// sending where it is known: it is what makes a result mean
	// something afterwards, and what a cache is keyed by.
	Commit string `json:"commit,omitempty"`
	// Event is "push", "pull_request" or "workflow_dispatch"; empty is a
	// manual dispatch, which is what an API call with no event of its
	// own is.
	Event string `json:"event,omitempty"`
	// Workflow is the path selected for an operator-started manual run.
	// Empty preserves the existing event-driven complete-suite behaviour.
	Workflow string `json:"workflow,omitempty"`
	// Jobs optionally selects job ids for a diagnostic run. Empty is the
	// complete suite. A selective task run is deliberately not recorded as
	// the task's required CI verdict.
	Jobs []string `json:"jobs,omitempty"`
	// Recheck bypasses the CI result cache and re-runs all matching jobs
	// (or the ones Jobs names).
	Recheck bool `json:"recheck,omitempty"`
	// RecheckFailed narrows a task-scoped recheck that names no Jobs to
	// the jobs whose latest result at the commit is not a pass
	// (Client.failedTaskJobs) -- retrigger_ci's default. Without it a
	// recheck naming no jobs re-runs every one.
	RecheckFailed bool `json:"recheckFailed,omitempty"`
}

// CICancelRequest is the body of DELETE /api/ci/runs/{id}: a note about
// why this run is being stopped, and nothing else.
//
// Optional, in the strongest sense -- the route has always worked with no
// body at all and still does. A cancellation without one is what it has
// always been: the run stops and the record says "cancelled". A
// cancellation with one carries the reason to the two readers who arrive
// afterwards and cannot ask -- the person reading the run, and the agent
// whose branch it was judging, which is told the note rather than left to
// guess at a bare conclusion (ci.Run.CancelReason).
type CICancelRequest struct {
	Reason string `json:"reason,omitempty"`
}

// CIWorkflowsResponse is what GET /api/ci/workflows answers with: the
// workflows a manual run on the asked-for repo and ref can select.
type CIWorkflowsResponse struct {
	Workflows []ci.WorkflowOption `json:"workflows"`
}

// TaskCIRunOptions narrows a task-scoped run (Client.StartTaskCIRunOpt).
// The zero value is the complete suite, which is the only run recorded as
// the task's required CI verdict.
type TaskCIRunOptions struct {
	Jobs     []string
	Workflow string
	Commit   string
	Recheck  bool
	// FailedOnly, with Recheck and no Jobs, re-runs only the jobs whose
	// latest result at the commit did not pass (failedTaskJobs).
	FailedOnly bool
}

// StartTaskCIRunOpt starts grain CI on task id's own target and branch --
// never on a repo or ref the caller names -- and, for a complete-suite
// run, records it as the task's CI verdict and cancels the run it
// replaces.
func (c *Client) StartTaskCIRunOpt(ctx context.Context, id string, opt TaskCIRunOptions) (ci.Run, error) {
	if c.Config.CI == nil {
		return ci.Run{}, fmt.Errorf("this deployment has no grain CI wired")
	}
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return ci.Run{}, err
	}
	if task == nil {
		return ci.Run{}, &NotFoundError{ID: id}
	}
	if task.Target == nil {
		return ci.Run{}, validationErrorf("task %s has no target repository", id)
	}
	if !grainci.Granted(task.Grants) {
		return ci.Run{}, forbiddenErrorf("task %s does not hold the %q capability", id, grainci.CapabilityName)
	}
	// A review working on the reviewed change's own branch owns no CI of
	// its own (model.Task.CIOwner): what it asks for is asked as the
	// reviewed task, on that task's branch, and recorded as that task's
	// verdict -- the one the orchestrator keeps for the branch and the
	// merge gate reads. Asking as itself would start a second suite on
	// the same commit, each superseding the other. The caller's own grant
	// above is what lets it ask at all.
	if ownerID, inPlace := task.CIOwner(); inPlace {
		owner, err := c.Store.GetTask(ctx, ownerID)
		if err != nil {
			return ci.Run{}, err
		}
		if owner == nil || owner.Target == nil || !owner.Target.Same(*task.Target) ||
			owner.BranchName() != task.BranchName() {
			return ci.Run{}, validationErrorf("task %s reviews task %s in place, whose branch is not %s on %s",
				id, ownerID, task.BranchName(), task.Target)
		}
		id, task = ownerID, owner
	}
	resetPullRequestPolls(*task)
	full := len(opt.Jobs) == 0 && opt.Workflow == "" && !opt.Recheck
	previous, err := c.Store.GetObservation(ctx, id)
	if err != nil {
		return ci.Run{}, err
	}
	if full {
		// The task's verdict is a recheck still in flight -- retrigger_ci's,
		// or the merge queue's own (orchestrator.startCIRecheck). It is
		// waited for rather than replaced: a new complete run started now
		// would read the very cached red the recheck is re-sampling, and
		// superseding it below would cancel the answer. Once it concludes
		// the orchestrator combines it with the run it re-checked, and the
		// next complete run reads every job's newest verdict from the cache.
		if live, ok := liveRecheck(c.Config.CI, task, previous, opt.Commit); ok {
			c.cancelSupersededTaskRuns(task, previous, live, true)
			return live, nil
		}
	}
	if opt.Recheck && opt.FailedOnly && len(opt.Jobs) == 0 {
		jobs, commit, err := failedTaskJobs(c.Config.CI, task, opt)
		if err != nil {
			return ci.Run{}, err
		}
		opt.Jobs = jobs
		if opt.Commit == "" {
			opt.Commit = commit
		}
	}
	c.reconcileTaskBase(ctx, task)
	baseCommit := ""
	if task.Target != nil {
		if baseCI, err := c.Store.GetBaseBranchCI(ctx, *task.Target, task.Base); err == nil && baseCI != nil {
			baseCommit = baseCI.Commit
		}
	}
	run, err := c.Config.CI.Start(ctx, ci.Request{
		Repo:        task.Target.String(),
		Ref:         task.BranchName(),
		Commit:      strings.TrimSpace(opt.Commit),
		Event:       ci.EventPush,
		RequestedBy: id,
		Workflow:    strings.TrimSpace(opt.Workflow),
		Jobs:        opt.Jobs,
		Recheck:     opt.Recheck,
		// The full suite is recorded below as the task's required
		// verdict, so it is held to its base's gate too (ci.basegate.go);
		// a narrowed run never is, and Start ignores this for one.
		GateOnBase: true,
		BaseRef:    task.Base,
		BaseCommit: baseCommit,
	})
	if err != nil {
		// A daemon still starting is not a fact about the request: left
		// as the ErrNotUp it is, so it is served as 503 and a waiting
		// caller asks again rather than giving up on a 400.
		// Nor is a start queued for a grit server that cannot be reached
		// (ci.ErrStartQueued): it runs once grit is back, so it is served
		// as 503 too, and asking again joins it.
		if errors.Is(err, ci.ErrNotUp) || errors.Is(err, ci.ErrStartQueued) {
			return ci.Run{}, err
		}
		// A fact about the request, as handleStartCIRun says of the same
		// refusal: a 400 that carries Start's reason, rather than a 500
		// whose reason apierror.Body keeps to the daemon's log.
		return ci.Run{}, &ValidationError{err: err}
	}
	if opt.Recheck {
		if run.Commit == "" && opt.Commit != "" {
			run.Commit = strings.TrimSpace(opt.Commit)
		}
		recorded, err := c.recordTaskRecheck(ctx, task, previous, opt, run)
		c.cancelSupersededTaskRuns(task, previous, run, recorded)
		return run, err
	}
	if !full {
		c.cancelSupersededTaskRuns(task, previous, run, false)
		return run, nil
	}
	c.cancelSupersededTaskRuns(task, previous, run, true)
	if err := c.Store.ObserveField(ctx, id, c.now(), func(o *model.Observation) {
		o.CI = &model.CIStatus{RunID: run.ID, Commit: run.Commit, StartedAt: run.StartedAt}
	}); err != nil {
		return ci.Run{}, fmt.Errorf("recording grain CI run on task %s: %w", id, err)
	}
	return run, nil
}

// reconcileTaskBase updates task.Base when task is stacked on an upstream
// task branch that has since merged (or when GitHub has retargeted task's
// open pull request after the upstream branch merged and was deleted), so
// full-suite CI runs gate against the live target base branch rather than
// cloning a deleted feature branch.
func (c *Client) reconcileTaskBase(ctx context.Context, task *model.Task) {
	if c == nil || c.Store == nil || task == nil || task.Target == nil || task.Base == "" {
		return
	}
	oldBase := task.Base
	targetBase, _, merged, _ := c.Store.MergedUpstreamTaskBase(ctx, *task.Target, oldBase)
	if pr := model.TaskPullRequest(*task); pr != nil && pr.Repo.Same(*task.Target) && c.Config.GitHubPullRequests != nil {
		if detail, err := c.Config.GitHubPullRequests.GetPullRequest(pr.Repo.Owner, pr.Repo.Name, pr.Number); err == nil && detail.BaseRef != "" && detail.BaseRef != oldBase {
			if merged {
				targetBase = detail.BaseRef
			} else {
				if checker, ok := c.Config.GitHubPullRequests.(interface {
					BranchExists(owner, repo, branch string) (bool, error)
				}); ok {
					if exists, err := checker.BranchExists(task.Target.Owner, task.Target.Name, oldBase); err == nil && !exists {
						merged = true
						targetBase = detail.BaseRef
					}
				}
			}
		}
	}
	if !merged || targetBase == oldBase {
		return
	}
	// A review or fix merges back into the branch it was stacked on and is
	// never classified on its own account (orchestrator's
	// holdForTrustBoundary), so one moved onto that branch's base loses its
	// AutoMerge and waits for a person, as the orchestrator's retarget does.
	unattended := task.Origin.Reason == model.ReasonReview || task.Origin.Reason == model.ReasonFix
	if err := c.Store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		if t.Base == oldBase {
			t.Base = targetBase
			if unattended {
				t.AutoMerge = false
			}
		}
		return nil
	}); err != nil {
		return
	}
	task.Base = targetBase
	if unattended {
		task.AutoMerge = false
	}
}

// cancelSupersededTaskRuns cancels still-running CI runs on task's branch
// that run replaces. When cancelAllOnBranch is true (a full-suite run or a
// retrigger_ci recheck), every other live run on the branch -- including
// selective runs and earlier rechecks -- is superseded. When false (a
// selective run), earlier selective runs on the branch and any live runs for
// an older commit are superseded, while an in-flight required run at the
// same commit is left to finish.
func (c *Client) cancelSupersededTaskRuns(task *model.Task, previous *model.Observation, run ci.Run, cancelAllOnBranch bool) {
	if c.Config.CI == nil || task == nil || task.Target == nil {
		return
	}
	reason := ci.SupersededByRunReason(run.ID)
	seen := map[string]bool{}
	if run.ID != "" {
		seen[run.ID] = true
	}
	var activeRequiredID string
	if previous != nil && previous.CI != nil && !previous.CI.Done() && previous.CI.RunID != "" {
		r, ok := c.Config.CI.Get(previous.CI.RunID)
		if !ok || (!r.Conclusion.Done() && !r.Interrupted) {
			activeRequiredID = previous.CI.RunID
			olderCommit := run.Commit != "" && previous.CI.Commit != "" && previous.CI.Commit != run.Commit
			if (cancelAllOnBranch || olderCommit) && !seen[activeRequiredID] {
				seen[activeRequiredID] = true
				// grain's own supersede, not the asker's cancellation: they asked
				// for a run, and this is the older one getting out of its way.
				c.Config.CI.Cancel(activeRequiredID, reason, ci.CancelBySystem)
			}
		}
	}
	repo, ref := task.Target.String(), task.BranchName()
	page := c.Config.CI.Find(ci.RunFilter{Repo: repo, Ref: ref, Descending: true}, 200)
	for _, r := range page.Runs {
		if r.ID == "" || seen[r.ID] {
			continue
		}
		if !strings.EqualFold(r.Repo, repo) || r.Ref != ref {
			continue
		}
		if r.Conclusion.Done() || r.Interrupted {
			continue
		}
		if !cancelAllOnBranch && r.ID == activeRequiredID {
			if run.Commit == "" || r.Commit == "" || r.Commit == run.Commit {
				continue
			}
		}
		seen[r.ID] = true
		c.Config.CI.Cancel(r.ID, reason, ci.CancelBySystem)
	}
}

// liveRecheck is the recheck a task's recorded verdict is waiting on,
// when there is one still running.
func liveRecheck(runner ci.Runner, _ *model.Task, obs *model.Observation, wantCommit string) (ci.Run, bool) {
	wantCommit = strings.TrimSpace(wantCommit)
	if obs != nil && obs.CI != nil && obs.CI.RecheckOf != "" && !obs.CI.Done() {
		if run, ok := runner.Get(obs.CI.RunID); ok && !run.Interrupted && !run.Conclusion.Done() {
			if wantCommit == "" || run.Commit == "" || strings.HasPrefix(run.Commit, wantCommit) {
				return run, true
			}
		}
	}
	return ci.Run{}, false
}

// failedTaskJobs is what retrigger_ci re-runs when it is not told which
// jobs: every job whose newest result at the commit did not pass, and
// the commit it read them at.
//
// Newest result per job, across every run of the task's branch at that
// commit, rather than the jobs of the newest run alone: a selective run
// of one job that passed is the newest run often enough (wait_for_checks
// with jobs), and it says nothing about the job that failed in the full
// suite before it. It is the same combination the task's own verdict
// makes of a run and its rechecks. The commit is opt.Commit (a prefix is
// enough) or, unnamed, the one the branch's newest run was against.
func failedTaskJobs(runner ci.Runner, task *model.Task, opt TaskCIRunOptions) ([]string, string, error) {
	ref := task.BranchName()
	page := runner.Find(ci.RunFilter{Repo: task.Target.String(), Ref: ref, Descending: true}, 200)
	commit := strings.TrimSpace(opt.Commit)
	workflow := strings.TrimSpace(opt.Workflow)
	latest := map[string]ci.Conclusion{}
	var order []string
	seen := 0
	for _, run := range page.Runs {
		if run.Ref != ref || run.Commit == "" {
			continue
		}
		if commit == "" {
			commit = run.Commit
		}
		if !strings.HasPrefix(run.Commit, commit) {
			continue
		}
		commit = run.Commit
		seen++
		for _, job := range run.Jobs {
			if job.JobID == "" || !job.Conclusion.Done() || (workflow != "" && job.WorkflowPath != workflow) {
				continue
			}
			id := job.JobID
			if job.WorkflowPath != "" {
				id = job.WorkflowPath + ":" + job.JobID
			}
			if _, ok := latest[id]; ok {
				continue
			}
			conclusion := job.Conclusion
			// A job a passing run skipped was skipped by its own `if:`,
			// not behind a failure, and is nothing to re-run.
			if conclusion == ci.ConclusionSkipped && run.Conclusion == ci.ConclusionSuccess {
				conclusion = ci.ConclusionSuccess
			}
			latest[id] = conclusion
			order = append(order, id)
		}
	}
	if seen == 0 {
		at := ""
		if commit != "" {
			at = " at " + commit
		}
		return nil, "", validationErrorf("there is no grain CI run on %s%s to re-run the failed jobs of; "+
			"name the jobs to re-run, or ask for all of them", ref, at)
	}
	var failed []string
	for _, id := range order {
		if latest[id] != ci.ConclusionSuccess {
			failed = append(failed, id)
		}
	}
	if len(failed) == 0 {
		return nil, "", validationErrorf("no job failed in grain CI's latest results for %s at %s, so there is "+
			"nothing to re-run by default; name the jobs to re-run, or ask for all of them", ref, shortCommit(commit))
	}
	sort.Strings(failed)
	return failed, commit, nil
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

func taskCIJobName(job ci.JobRun) string {
	name := job.Name
	if name == "" {
		name = job.JobID
	}
	if job.Workflow != "" && name != "" {
		return job.Workflow + "/" + name
	}
	return name
}

func taskCIDetail(run ci.Run) string {
	if strings.TrimSpace(run.Error) != "" || strings.TrimSpace(run.CancelReason) != "" || run.CancelledBy == ci.CancelByOperator {
		return ""
	}
	var failed []string
	for _, job := range run.Jobs {
		switch job.Conclusion {
		case ci.ConclusionFailure, ci.ConclusionCancelled, ci.ConclusionErrored:
			if name := taskCIJobName(job); name != "" {
				failed = append(failed, name)
			}
		}
	}
	if len(failed) == 0 {
		return ""
	}
	conclusion := run.Conclusion
	if conclusion == "" {
		conclusion = ci.ConclusionCancelled
	}
	return fmt.Sprintf("%s: %s", conclusion, strings.Join(failed, ", "))
}

func firstTaskConclusionOf(detail string) ci.Conclusion {
	switch {
	case strings.HasPrefix(detail, "cancelled: "):
		return ci.ConclusionCancelled
	case strings.HasPrefix(detail, "errored: "), strings.HasPrefix(detail, "grain CI could not run"):
		return ci.ConclusionErrored
	default:
		return ci.ConclusionFailure
	}
}

func taskRecheckVerdict(firstDetail string, recheck ci.Run) (ci.Conclusion, string) {
	if recheck.Conclusion != ci.ConclusionSuccess && recheck.Conclusion != ci.ConclusionFailure {
		return firstTaskConclusionOf(firstDetail), firstDetail
	}
	rechecked := map[string]bool{}
	again := map[string]bool{}
	for _, job := range recheck.Jobs {
		name := taskCIJobName(job)
		rechecked[name] = true
		switch job.Conclusion {
		case ci.ConclusionFailure, ci.ConclusionCancelled, ci.ConclusionErrored:
			again[name] = true
		}
	}
	// A first verdict naming no job has nothing a recheck re-ran, so it
	// stands (orchestrator.recheckVerdict).
	firstJobs := recheckJobsOf(firstDetail)
	if len(firstJobs) == 0 {
		return firstTaskConclusionOf(firstDetail), firstDetail
	}
	var failing []string
	for _, name := range firstJobs {
		switch {
		case !rechecked[name]:
			failing = append(failing, name)
		case again[name]:
			failing = append(failing, name)
			delete(again, name)
		}
	}
	for _, job := range recheck.Jobs {
		name := taskCIJobName(job)
		if again[name] {
			failing = append(failing, name)
		}
	}
	if len(failing) == 0 {
		return ci.ConclusionSuccess, ""
	}
	conclusion := ci.ConclusionFailure
	if recheck.Conclusion != ci.ConclusionFailure {
		conclusion = firstTaskConclusionOf(firstDetail)
	}
	return conclusion, fmt.Sprintf("%s: %s", conclusion, strings.Join(failing, ", "))
}

func isRecheckableConclusion(conclusion string) bool {
	switch ci.Conclusion(conclusion) {
	case ci.ConclusionFailure, ci.ConclusionCancelled, ci.ConclusionErrored:
		return true
	default:
		return false
	}
}

// rechecksAnyOf reports whether run re-runs any of the jobs a non-passing
// verdict's detail names. A recheck of only jobs that had passed is a
// diagnostic: it has nothing to say about what is red, and is not made the
// verdict's second sample.
func rechecksAnyOf(run ci.Run, requestedJobs []string, detail string) bool {
	names := recheckJobsOf(detail)
	if len(names) == 0 {
		return false
	}
	failed := map[string]bool{}
	failedBare := map[string]bool{}
	for _, name := range names {
		failed[name] = true
		if i := strings.LastIndexByte(name, '/'); i >= 0 {
			failedBare[name[i+1:]] = true
		} else {
			failedBare[name] = true
		}
	}
	if len(run.Jobs) > 0 {
		for _, job := range run.Jobs {
			if failed[taskCIJobName(job)] {
				return true
			}
			if job.Workflow != "" && job.JobID != "" && failed[job.Workflow+"/"+job.JobID] {
				return true
			}
			if job.Workflow == "" && (failedBare[job.Name] || failedBare[job.JobID]) {
				return true
			}
		}
		return false
	}
	if len(requestedJobs) == 0 {
		return true
	}
	for _, req := range requestedJobs {
		req = strings.TrimSpace(req)
		if i := strings.LastIndexByte(req, ':'); i >= 0 {
			req = req[i+1:]
		}
		if failed[req] || failedBare[req] {
			return true
		}
	}
	return false
}

// recordTaskRecheck makes a recheck of the task's failed, timed-out, or
// errored verdict the verdict's second sample, exactly as the merge queue's
// own recheck is (orchestrator.askCIRecheck): the status names the recheck's
// run, with RecheckOf the run it re-checks, and the orchestrator combines the
// two when it concludes (orchestrator.recheckVerdict) -- a job re-run and
// passed is green, one that was not re-run keeps the result it had. So
// re-running only the failed jobs, and having them pass, is what lifts
// the merge gate; the jobs that had passed are not run again.
//
// Only a recorded required verdict at the same commit is replaced. A
// selective run is never recorded in Observation.CI, so scanning unrecorded
// runs would let a selective run and its recheck satisfy the full-suite gate.
func (c *Client) recordTaskRecheck(ctx context.Context, task *model.Task, previous *model.Observation, opt TaskCIRunOptions, run ci.Run) (bool, error) {
	first, ok := c.resolveRecheckTarget(task, previous, opt, run)
	if !ok {
		return false, nil
	}
	prevRunID := previous.CI.RunID
	prevConclusion := previous.CI.Conclusion
	recheckOf := first.RunID
	if first.RecheckOf != "" {
		recheckOf = first.RecheckOf
	}
	startedAt := first.StartedAt
	if startedAt == nil {
		startedAt = run.StartedAt
	}
	recorded := false
	if err := c.Store.ObserveField(ctx, task.ID, c.now(), func(o *model.Observation) {
		if o.CI == nil || o.CI.RunID != prevRunID || o.CI.Conclusion != prevConclusion {
			return
		}
		o.CI = &model.CIStatus{
			RunID: run.ID, Commit: run.Commit, StartedAt: startedAt,
			Attempts: first.Attempts, PreviousConclusion: first.PreviousConclusion,
			PreviousDetail: first.PreviousDetail, RecheckOf: recheckOf, RecheckDetail: first.Detail,
		}
		recorded = true
	}); err != nil {
		return false, fmt.Errorf("recording grain CI recheck %s on task %s: %w", run.ID, task.ID, err)
	}
	return recorded, nil
}

func (c *Client) resolveRecheckTarget(task *model.Task, previous *model.Observation, opt TaskCIRunOptions, run ci.Run) (model.CIStatus, bool) {
	if run.Commit == "" || task == nil || task.Target == nil || previous == nil || previous.CI == nil {
		return model.CIStatus{}, false
	}
	first := c.hydrateRecheckTarget(*previous.CI)
	if first.Commit != run.Commit || first.RunID == "" || first.RunID == run.ID {
		return model.CIStatus{}, false
	}
	if first.CancelledBy == string(ci.CancelByOperator) || !isRecheckableConclusion(first.Conclusion) {
		return model.CIStatus{}, false
	}
	if !rechecksAnyOf(run, opt.Jobs, first.Detail) {
		return model.CIStatus{}, false
	}
	first.Detail = c.withUnrecheckedSkippedJobs(first, run, opt.Jobs)
	return first, true
}

func (c *Client) hydrateRecheckTarget(st model.CIStatus) model.CIStatus {
	if st.RunID == "" || c.Config.CI == nil {
		return st
	}
	if prior, ok := c.Config.CI.Get(st.RunID); ok {
		if prior.Interrupted || strings.TrimSpace(prior.CancelReason) != "" || prior.CancelledBy == ci.CancelByOperator {
			st.CancelledBy = string(ci.CancelByOperator)
			return st
		}
		if st.Commit == "" {
			st.Commit = prior.Commit
		}
		if st.StartedAt == nil {
			st.StartedAt = prior.StartedAt
		}
		if st.Conclusion == "" {
			if st.RecheckOf != "" {
				if prior.Conclusion.Done() {
					c, d := taskRecheckVerdict(st.RecheckDetail, prior)
					st.Conclusion = string(c)
					st.Detail = d
				} else {
					st.Conclusion = string(firstTaskConclusionOf(st.RecheckDetail))
					st.Detail = st.RecheckDetail
				}
			} else if prior.Conclusion.Done() {
				st.Conclusion = string(prior.Conclusion)
				if len(recheckJobsOf(st.Detail)) == 0 {
					st.Detail = taskCIDetail(prior)
				}
			}
		} else if len(recheckJobsOf(st.Detail)) == 0 && st.RecheckOf == "" && prior.Conclusion.Done() {
			st.Detail = taskCIDetail(prior)
		}
	}
	return st
}

func (c *Client) withUnrecheckedSkippedJobs(first model.CIStatus, run ci.Run, requestedJobs []string) string {
	if c.Config.CI == nil {
		return first.Detail
	}
	baseID := first.RunID
	if first.RecheckOf != "" {
		baseID = first.RecheckOf
	}
	baseRun, ok := c.Config.CI.Get(baseID)
	if !ok || baseRun.Conclusion == ci.ConclusionSuccess {
		return first.Detail
	}
	existing := map[string]bool{}
	for _, name := range recheckJobsOf(first.Detail) {
		existing[name] = true
	}
	detail := first.Detail
	for _, job := range baseRun.Jobs {
		if job.Conclusion != ci.ConclusionSkipped {
			continue
		}
		name := taskCIJobName(job)
		if name == "" || existing[name] {
			continue
		}
		if rechecksAnyOf(run, requestedJobs, "failure: "+name) {
			continue
		}
		existing[name] = true
		detail += ", " + name
	}
	return detail
}

// CIRunsResponse is what GET /api/ci/runs answers with.
type CIRunsResponse struct {
	// Enabled is whether this deployment has CI wired at all -- false
	// means every other field here is empty because there is nothing
	// behind them, not because nothing has run.
	Enabled bool `json:"enabled"`
	// Runs are the runs this deployment still remembers, newest first,
	// each without its steps' own output -- see ciListSummary. The whole
	// of a run, output and all, is GET /api/ci/runs/{id}.
	//
	// Narrowed by the query's own filter (ciRunFilterFrom) and bounded
	// by its ?limit=, which is what Matched and Stored below are here to
	// say: this is a page of an answer, not the answer.
	Runs []ci.Run `json:"runs"`
	// Matched is how many runs answered the filter in all, and Stored
	// how many this deployment holds whether they matched or not
	// (ci.RunPage). Together they are what tells a reader which of the
	// three reasons a run is not on this page: it was never kept, it did
	// not match, or the limit stopped short of it.
	Matched int `json:"matched"`
	Stored  int `json:"stored"`
	// Faults are the failures showing on more than one branch at once
	// (cifault.go): one entry per fault, naming every run it has been
	// seen in, so a row can say that its red is not its own.
	//
	// Computed over every recent run rather than over the narrowed list
	// above, because that is the question it answers: "is anybody else
	// failing this way" cannot be asked of one branch's runs alone, and a
	// pane narrowed to one branch is exactly where it is worth asking.
	Faults []CIFault `json:"faults,omitempty"`
}

// CIFaultsResponse is what GET /api/ci/faults answers with: the same
// faults the listing carries, asked for on their own.
//
// A route of their own because the listing's other half is expensive to
// the reader that only wants this one. An agent's tools ask "is this red
// shared?" about a run they already hold (pkg/mcp's wait_for_checks),
// and fetching a page of runs to reach the annotation hung off it would
// spend an answer's budget on runs nobody asked about.
type CIFaultsResponse struct {
	// Enabled is whether this deployment has CI wired at all -- false
	// meaning there is nothing behind Faults rather than nothing failing,
	// the same distinction CIRunsResponse draws.
	Enabled bool      `json:"enabled"`
	Faults  []CIFault `json:"faults,omitempty"`
}

// CICacheResponse is what GET /api/ci/cache answers with, and what
// DELETE /api/ci/cache answers with once it has emptied it.
//
// Three states rather than two, because "off", "on with no cache" and
// "on with a cache" send a reader to three different places: a
// deployment started with -ci=false, a deployment whose cache directory
// could not be opened (which is logged once at startup and then only
// visible as every job running, every time), and the ordinary case.
type CICacheResponse struct {
	// Enabled is whether this deployment has grain CI wired at all --
	// the same flag CIRunsResponse carries, and for the same reason.
	Enabled bool `json:"enabled"`
	// Available is whether there is a cache behind it that can be read.
	// False with Enabled true is a deployment running every job every
	// time; the daemon log says why (pkg/cli/ci.go).
	Available bool `json:"available"`
	// Cache is what it is holding, absent unless Available.
	Cache *CICacheStats `json:"cache,omitempty"`
	// Removed is how many entries the DELETE removed, absent on a read.
	// A pointer so that "cleared, and there was nothing in it" is
	// distinguishable from a read, rather than both being a missing 0.
	Removed *int `json:"removed,omitempty"`
}

// CICacheStats is ci.CacheStats over the wire.
//
// Restated here rather than served as it stands -- unlike ci.Run, which
// this package does serve as it stands -- because of the durations: a
// time.Duration is nanoseconds in JSON, and a pane that has to know to
// divide by a billion is a pane that will one day forget. Seconds is
// what every other measured thing in this API sends (ui.MetricsReport).
type CICacheStats struct {
	// Dir is the directory on the host, for an operator being told where
	// to look.
	Dir string `json:"dir,omitempty"`
	// Entries and Bytes are what it holds now, including anything past
	// MaxAge that the next sweep has not taken yet -- it is still disk.
	Entries int   `json:"entries"`
	Bytes   int64 `json:"bytes"`
	// Oldest and Newest are the age span of what is held; Oldest is what
	// goes first when the bounds bite.
	Oldest time.Time `json:"oldest,omitempty"`
	Newest time.Time `json:"newest,omitempty"`
	// LastSweep is when the bounds were last enforced, zero for a cache
	// that has not swept since this daemon started.
	LastSweep time.Time `json:"lastSweep,omitempty"`
	// Hits and Misses are Lookups since this daemon started -- not since
	// the cache was filled, which is the older number and one nothing
	// keeps: a restart leaves a full cache and empty counters.
	Hits   int64 `json:"hits"`
	Misses int64 `json:"misses"`
	// The bounds, so that a size means something.
	MaxAgeSeconds float64 `json:"maxAgeSeconds"`
	MaxBytes      int64   `json:"maxBytes"`
	MaxEntries    int     `json:"maxEntries"`
	MaxEntryBytes int64   `json:"maxEntryBytes"`
}

func ciCacheStats(s ci.CacheStats) CICacheStats {
	return CICacheStats{
		Dir:           s.Dir,
		Entries:       s.Entries,
		Bytes:         s.Bytes,
		Oldest:        s.Oldest,
		Newest:        s.Newest,
		LastSweep:     s.LastSweep,
		Hits:          s.Hits,
		Misses:        s.Misses,
		MaxAgeSeconds: s.MaxAge.Seconds(),
		MaxBytes:      s.MaxBytes,
		MaxEntries:    s.MaxEntries,
		MaxEntryBytes: s.MaxEntryBytes,
	}
}

// ciListSummary is one run as the listing serves it: the run, its jobs
// and its steps entire, with each step's own output left out.
//
// A step keeps up to 256KB of what it printed (ci.MaxStepOutput; 64KB of
// it once stored, and 256KB across a whole stored run), a run has as
// many steps as its workflows declare, and a deployment remembers
// thousands of runs (ci.DefaultStoredRunHistory) of which this listing
// serves the newest ci.MaxListedRuns -- so a listing that carried every
// step's output could be tens of megabytes of log, sent again every time
// a pane polls, to draw a table with no output column in it. The one reader
// that wants a step's output is the one looking at that step, and it
// opens the run to get there.
//
// The store now drops it before this ever sees it (ci.RunStore.List does
// not read output off disk at all), so this is belt and braces rather
// than the only guard -- and it stays, because a RunStore that changed
// its mind must not quietly turn this route into a log firehose.
//
// Everything else stays: which jobs a run has and how each of them
// ended is what the listing is *for* while a run is still going.
func ciListSummary(run ci.Run) ci.Run {
	if len(run.Jobs) == 0 {
		return run
	}
	jobs := make([]ci.JobRun, len(run.Jobs))
	for i, job := range run.Jobs {
		if len(job.Steps) > 0 {
			steps := make([]ci.StepRun, len(job.Steps))
			for j, step := range job.Steps {
				step.Output = ""
				steps[j] = step
			}
			job.Steps = steps
		}
		jobs[i] = job
	}
	run.Jobs = jobs
	return run
}

// ciEnabledFor reports whether grain CI is turned on for this repo: the
// controller-local config repository always, otherwise the repo's own CI
// mode (model.RepoUsesGrainCI -- "Use Grain CI", or the legacy
// IgnoreGitHubCI), or workflows recorded for it. The grain-ci capability
// is not read here: since grain-ci became implicit (57939261f) a repo's CI
// mode decides it and grants grain-ci to that repo's tasks, so the
// capability in a repo's or the deployment's default set turns nothing on.
//
// A repo grain CI is not enabled for is refused rather than run, and
// the refusal names what to do about it (ciNotEnabledMessage) -- an
// operator's next move is two clicks away in Settings, and a 403 that
// does not say which two is a support question.
func (c *Client) ciEnabledFor(ctx context.Context, repo model.RepoRef) (bool, error) {
	if grainci.RequiredFor(repo) {
		return true, nil
	}
	repoCfg, err := c.Store.GetRepoConfig(ctx, repo)
	if err != nil {
		return false, err
	}
	if grainci.RepoUsesGrainCI(repoCfg, repo) {
		return true, nil
	}
	wfs, err := c.Store.GetRepoWorkflows(ctx, repo)
	if err != nil {
		return false, err
	}
	return len(wfs) > 0, nil
}

// ciNotEnabledMessage is the refusal for a repo ciEnabledFor says no
// to. It names the switch that does turn grain CI on -- the repo's CI
// mode -- and says outright that the grain-ci capability does not: this
// message once told operators to attach it, and somebody remembering
// that advice should not be sent round the same loop again. Every
// picker refuses the capability itself with grainci.Refusal.
func ciNotEnabledMessage(repo model.RepoRef) string {
	return fmt.Sprintf("grain CI is not enabled for %s: set its CI Mode to \"Use Grain CI\" on the repo's page (Settings, Repos). "+
		"Attaching the %q capability does not turn it on by itself: the CI mode decides that, and grants %q to the repo's tasks",
		repo, grainci.CapabilityName, grainci.CapabilityName)
}

// ciEnabledAnywhere is the same question ciEnabledFor asks, for an
// action that is about no repository in particular: the job cache is one
// directory for the whole deployment, so "may this caller clear it" has
// no repo in it to read a capability off.
//
// The answer is the union of this deployment's own default capabilities
// and every repo config's -- grain CI being on for anything is what puts
// entries in this cache in the first place, and a deployment that has
// turned it on for one repository has an operator who can be trusted
// with the cache that repository fills. A deployment where nobody has
// grain-ci has an empty cache and no way to fill one, so refusing there
// costs nothing and keeps the gate honest: no action here that a run
// could not have caused.
func (c *Client) ciEnabledAnywhere(ctx context.Context) (bool, error) {
	// Every deployment has the controller-local config repository, whose
	// pre-merge validation is always on.
	if grainci.RequiredFor(model.ConfigRepo()) {
		return true, nil
	}
	repos, err := c.Store.ListRepoConfigs(ctx)
	if err != nil {
		return false, err
	}
	for _, repo := range repos {
		if grainci.RepoUsesGrainCI(&repo, repo.Repo) {
			return true, nil
		}
	}
	return false, nil
}

// --- handlers ------------------------------------------------------------

func (s *Server) handleStartCIRun(w http.ResponseWriter, r *http.Request) {
	runner := s.tasks.Config.CI
	if runner == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("this deployment has no grain CI wired"))
		return
	}
	var req CIRunRequest
	if !readJSON(w, r, &req) {
		return
	}
	if id := strings.TrimSpace(req.TaskID); id != "" {
		run, err := s.tasks.StartTaskCIRunOpt(r.Context(), id, TaskCIRunOptions{
			Jobs:       req.Jobs,
			Workflow:   strings.TrimSpace(req.Workflow),
			Commit:     strings.TrimSpace(req.Commit),
			Recheck:    req.Recheck,
			FailedOnly: req.RecheckFailed,
		})
		if err != nil {
			writeClientError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, run)
		return
	}
	ref := req.Ref
	if ref == "" {
		ref = req.Branch
	}
	repo, err := s.tasks.parseRepo(req.Repo)
	if err != nil {
		writeClientError(w, err)
		return
	}
	enabled, err := s.tasks.ciEnabledFor(r.Context(), repo)
	if err != nil {
		writeClientError(w, err)
		return
	}
	if !enabled {
		writeError(w, http.StatusForbidden, errors.New(ciNotEnabledMessage(repo)))
		return
	}

	event := ci.Event(strings.TrimSpace(req.Event))
	withhold, err := s.tasks.withholdReleaseSecrets(r.Context(), repo, event)
	if err != nil {
		writeClientError(w, err)
		return
	}
	withholdCapabilities := withhold
	if withholdCapabilities == "" && event == "" {
		withholdCapabilities, err = s.tasks.withholdReleaseSecrets(r.Context(), repo, ci.EventManual)
		if err != nil {
			writeClientError(w, err)
			return
		}
	}
	withholdReason := withhold
	if withholdReason == "" {
		withholdReason = withholdCapabilities
	}
	run, err := runner.Start(r.Context(), ci.Request{
		Repo:                   repo.String(),
		Ref:                    strings.TrimSpace(ref),
		Commit:                 strings.TrimSpace(req.Commit),
		Event:                  event,
		RequestedBy:            s.tasks.actor(r.Context()).ID,
		Workflow:               strings.TrimSpace(req.Workflow),
		Jobs:                   req.Jobs,
		Recheck:                req.Recheck,
		WithholdEventGated:     withholdCapabilities != "",
		WithholdReleaseSecrets: withhold != "",
		WithholdReason:         withholdReason,
	})
	if errors.Is(err, ci.ErrNotUp) {
		writeCINotUp(w)
		return
	}
	if errors.Is(err, ci.ErrStartQueued) {
		writeCIStartQueued(w, err)
		return
	}
	if err != nil {
		// Everything else Start refuses is a fact about the request -- a
		// branch that does not resolve, a workflow file that does not
		// parse, a repo with no name -- and the caller is who can fix
		// it.
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// 202: the run has been accepted and is going on without this
	// request, which is what the id in the body is for.
	writeJSON(w, http.StatusAccepted, run)
}

// withholdReleaseSecrets is why a by-hand run of event on repo is to be
// handed none of the repository's capabilities or release secrets, ""
// when it may be.
//
// A workflow_dispatch -- and a release, which this route can be asked
// for by name -- is the event capabilities and release secrets are
// placed into, on whatever branch the caller names, including a task
// branch nobody has reviewed. GitHub lets only somebody with write
// access fire one; grain's equivalent is the line StartRepoRelease
// already draws: a person, the deployment's owner, rather than an agent.
// An agent's run still runs, and a job that needs a capability or a
// secret says why it did not get one.
func (c *Client) withholdReleaseSecrets(ctx context.Context, repo model.RepoRef, event ci.Event) (string, error) {
	if event != ci.EventManual && event != ci.EventRelease {
		return "", nil
	}
	if c.actor(ctx).Kind == model.PrincipalHuman {
		return "", nil
	}
	return fmt.Sprintf("a %s run of %s started by a caller that is not a person places no capabilities or release secrets",
		event, repo), nil
}

func (s *Server) handleListCIWorkflows(w http.ResponseWriter, r *http.Request) {
	runner := s.tasks.Config.CI
	if runner == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("this deployment has no grain CI wired"))
		return
	}
	repo, err := s.tasks.parseRepo(r.URL.Query().Get("repo"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	enabled, err := s.tasks.ciEnabledFor(r.Context(), repo)
	if err != nil {
		writeClientError(w, err)
		return
	}
	if !enabled {
		writeError(w, http.StatusForbidden, errors.New(ciNotEnabledMessage(repo)))
		return
	}
	options, err := runner.WorkflowOptions(r.Context(), ci.Request{
		Repo: repo.String(), Ref: strings.TrimSpace(r.URL.Query().Get("ref")), Event: ci.EventManual,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if options == nil {
		options = []ci.WorkflowOption{}
	}
	writeJSON(w, http.StatusOK, CIWorkflowsResponse{Workflows: options})
}

func (s *Server) handleListCIRuns(w http.ResponseWriter, r *http.Request) {
	runner := s.tasks.Config.CI
	if runner == nil {
		writeJSON(w, http.StatusOK, CIRunsResponse{
			Enabled: false,
			Faults:  s.tasks.CISharedFaults(r.Context()),
		})
		return
	}
	filter, err := ciRunFilterFrom(r.URL.Query(), time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	limit, err := ciRunLimitFrom(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	compact, err := ciRunViewFrom(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// The filter goes to the store rather than to what the store
	// answered with: a narrowed question is asked of the whole history
	// this deployment keeps, not of the newest ci.MaxListedRuns of it
	// (ci.RunStore.Find, grain/task-289).
	page := runner.Find(filter, limit)
	var faults []CIFault
	if canShareFaultRuns(filter, limit) {
		faults = s.tasks.ciSharedFaultsForRuns(r.Context(), page.Runs)
	} else {
		faults = s.tasks.CISharedFaults(r.Context())
	}
	if compact {
		writeJSON(w, http.StatusOK, ciCompactRuns(page, faults))
		return
	}
	listed := make([]ci.Run, 0, len(page.Runs))
	for _, run := range page.Runs {
		listed = append(listed, ciListSummary(run))
	}
	writeJSON(w, http.StatusOK, CIRunsResponse{
		Enabled: true,
		Runs:    listed,
		Matched: page.Matched,
		Stored:  page.Stored,
		Faults:  faults,
	})
}

// canShareFaultRuns reports whether the page a runs request was answered
// with is the same newest-first listing CISharedFaults would read, so
// the fault list can be computed from it instead of listing again. A
// page bounded by a reader's own visibility (filter.Visible, which
// Narrowed leaves out) never qualifies: the fault cache is shared by
// every reader, and must not hold a list computed from one reader's view.
func canShareFaultRuns(filter ci.RunFilter, limit int) bool {
	if filter.Narrowed() || filter.Visible != nil {
		return false
	}
	if !filter.Descending || (filter.Sort != "" && filter.Sort != ci.SortRunsByStarted) {
		return false
	}
	return limit == 0 || limit >= ciFaultRunLimit
}

// handleListCIFaults serves the fault list on its own, for a reader that
// holds the run already and only wants to know whether its red is shared
// (CIFaultsResponse).
func (s *Server) handleListCIFaults(w http.ResponseWriter, r *http.Request) {
	runner := s.tasks.Config.CI
	faults := s.tasks.CISharedFaults(r.Context())
	if runner == nil && len(faults) == 0 {
		writeJSON(w, http.StatusOK, CIFaultsResponse{Enabled: false})
		return
	}
	writeJSON(w, http.StatusOK, CIFaultsResponse{Enabled: true, Faults: faults})
}

// ciRunLimitFrom reads ?limit=, which is how many runs the answer
// carries -- absent (or zero) meaning ci.MaxListedRuns, the bound the
// unnarrowed poll has always had.
//
// It is a separate argument from the filter rather than a field of it
// because it is not a question about a run: every other word here says
// which runs are being asked about, and this one says how many of the
// answer to send. ci.RunStore.Find bounds it in turn (ci.MaxFoundRuns),
// so a limit with an extra zero in it costs a large answer rather than
// the daemon's memory.
//
// A limit that is not a number, or is negative, is refused rather than
// ignored, for the reason an unknown filter word is: an argument that is
// silently dropped answers with something else and says nothing about
// it.
func ciRunLimitFrom(query url.Values) (int, error) {
	text := strings.TrimSpace(query.Get("limit"))
	if text == "" {
		return 0, nil
	}
	limit, err := strconv.Atoi(text)
	if err != nil || limit < 0 {
		return 0, fmt.Errorf("%q is not a limit: it is how many runs to answer with, a whole number "+
			"(0, or no limit at all, means the newest %d)", query.Get("limit"), ci.MaxListedRuns)
	}
	return limit, nil
}

// ciRunFilterFrom reads GET /api/ci/runs' query into the same filter
// list_ci_runs takes, spelled with the same words (ci.RunFilter, and
// mcp.ciRunFilter beside it): ?repo=, ?ref=, ?conclusion= (repeated, or
// comma-separated, for several), ?job=, ?since=, ?until=, ?sort= and
// ?order=. ?until= is spelled the way ?since= is -- a span back from now
// or an instant -- so a window in the past is ?since=…&until=….
//
// One vocabulary on purpose. An operator narrowing the CI pane and an
// agent calling list_ci_runs are asking the same question of the same
// runs, and "failed runs on main" has to mean one set of runs and be
// spelled one way, or neither of them can check what the other reports
// (grain/task-231).
//
// The pane does most of its narrowing in the browser, over the runs it
// already polls for, so that a filter answers as it is typed
// (ui/src/ciFilters.js). What it sends here is the repo, because that is
// the one that decides how much goes over the wire every five seconds.
// The rest are served all the same: a query is a link somebody can keep,
// and a route that understood only half the vocabulary would be a second
// vocabulary again.
//
// An unknown word is a 400 rather than an empty table, for the reason
// the tool refuses one: a silently-ignored filter answers with
// everything, which reads as a fact about CI rather than as a typo.
func ciRunFilterFrom(query url.Values, now time.Time) (ci.RunFilter, error) {
	filter := ci.RunFilter{
		Repo:       strings.TrimSpace(query.Get("repo")),
		Ref:        strings.TrimSpace(query.Get("ref")),
		Job:        strings.TrimSpace(query.Get("job")),
		Descending: true,
	}
	for _, value := range query["conclusion"] {
		for _, word := range strings.Split(value, ",") {
			if strings.TrimSpace(word) == "" {
				continue
			}
			state, err := ci.ParseRunState(strings.TrimSpace(word))
			if err != nil {
				return ci.RunFilter{}, err
			}
			filter.States = append(filter.States, state)
		}
	}
	if since := strings.TrimSpace(query.Get("since")); since != "" {
		at, err := ci.ParseSince(since, now)
		if err != nil {
			return ci.RunFilter{}, err
		}
		filter.Since = at
	}
	if until := strings.TrimSpace(query.Get("until")); until != "" {
		at, err := ci.ParseSince(until, now)
		if err != nil {
			return ci.RunFilter{}, err
		}
		filter.Until = at
	}
	if word := strings.TrimSpace(query.Get("sort")); word != "" {
		key, err := ci.ParseRunSortKey(word)
		if err != nil {
			return ci.RunFilter{}, err
		}
		filter.Sort = key
	}
	switch strings.TrimSpace(query.Get("order")) {
	case "", "desc":
	case "asc":
		filter.Descending = false
	default:
		return ci.RunFilter{}, fmt.Errorf("%q is not an order: use asc or desc", query.Get("order"))
	}
	return filter, nil
}

// writeCINotUp answers a CI request this daemon cannot answer yet
// (ci.ErrNotUp): 503, carrying the reason, which is a fixed sentence
// and safe to show anyone.
func writeCINotUp(w http.ResponseWriter) {
	writeError(w, http.StatusServiceUnavailable, apierror.Public(ci.ErrNotUp.Error()))
}

// writeCIStartQueued answers a start the remote grit server could not
// take yet (ci.ErrStartQueued) with 503, not the 400 a refused request
// gets: the run is in the durable outbox and is submitted once grit is
// back, and asking for the same run again joins it rather than queueing
// another. What the attempt met -- a transport error naming grit's
// address -- stays in the daemon's log.
func writeCIStartQueued(w http.ResponseWriter, err error) {
	msg := ci.ErrStartQueued.Error()
	var queued *ci.StartQueuedError
	if errors.As(err, &queued) {
		log.Printf("ci: start %s queued for grit: %v", queued.RequestID, queued.Err)
		msg = fmt.Sprintf("%s (request %s); asking for the same run again joins it rather than starting a second", msg, queued.RequestID)
	}
	writeError(w, http.StatusServiceUnavailable, apierror.Public(msg))
}

func (s *Server) handleGetCIRun(w http.ResponseWriter, r *http.Request) {
	runner := s.tasks.Config.CI
	if runner == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("this deployment has no grain CI wired"))
		return
	}
	run, ok := runner.Get(r.PathValue("id"))
	if !ok {
		// A daemon whose CI service is not up yet remembers nothing, so
		// its false is not "forgotten": saying so would end every wait
		// that was polling this run across a restart (grain/task-1863).
		if !ci.IsUp(runner) {
			writeCINotUp(w)
			return
		}
		writeError(w, http.StatusNotFound, fmt.Errorf("no CI run %q -- a run is remembered for a while and then forgotten", r.PathValue("id")))
		return
	}
	// What a job kept is not a stored column of the run (see
	// ci.JobRun.Artifacts): it is a directory the daemon sweeps, so the
	// answer is read at the moment the run is served rather than
	// remembered from when it finished.
	run = runner.WithArtifacts(r.Context(), run)
	writeJSON(w, http.StatusOK, run)
}

// handleListCIRunArtifacts answers "what did this run leave behind" --
// GET /api/ci/runs/{id}/artifacts.
//
// Served exactly as the run it belongs to is: whoever
// can read the run can see the list of what it kept, and the *bytes*,
// the route below, are gated the same way -- an artifact is the run's
// own build output, as much the run's team's as its step output is.
func (s *Server) handleListCIRunArtifacts(w http.ResponseWriter, r *http.Request) {
	runner := s.tasks.Config.CI
	if runner == nil {
		writeJSON(w, http.StatusOK, CIRunArtifactsResponse{Artifacts: []ci.Artifact{}})
		return
	}
	artifacts, available, err := runner.RunArtifacts(r.Context(), r.PathValue("id"))
	if err != nil {
		// A store that cannot be read is this deployment's problem rather
		// than the caller's, and worth reporting rather than drawing as a
		// run that kept nothing -- which is what a broken one looks like
		// from the outside.
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if artifacts == nil {
		artifacts = []ci.Artifact{}
	}
	writeJSON(w, http.StatusOK, CIRunArtifactsResponse{Enabled: true, Available: available, Artifacts: artifacts})
}

// handleGetCIArtifact serves one artifact's bytes -- GET
// /api/ci/runs/{id}/artifacts/{job}/{name}.
//
// Content-Disposition is "attachment" rather than the "inline" an
// attachment gets: every artifact is a gzipped tar, which no browser
// renders and every browser would otherwise offer to save under the
// URL's last segment. The filename says which run and which job it came
// out of, because the thing people do with these is download three of
// them and then work out which is which.
func (s *Server) handleGetCIArtifact(w http.ResponseWriter, r *http.Request) {
	runner := s.tasks.Config.CI
	if runner == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("this deployment has no grain CI wired"))
		return
	}
	ref := ci.ArtifactRef{Run: r.PathValue("id"), Job: r.PathValue("job"), Name: r.PathValue("name")}
	if err := ref.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	body, artifact, err := runner.OpenArtifact(r.Context(), ref)
	if err != nil {
		// Not there is the ordinary case and the only one worth
		// distinguishing: an artifact is kept for a while and then swept,
		// exactly as a run is remembered for a while and then forgotten.
		if errors.Is(err, fs.ErrNotExist) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", ci.ArtifactContentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", ref.Filename()))
	w.Header().Set("Content-Length", strconv.FormatInt(artifact.Bytes, 10))
	w.WriteHeader(http.StatusOK)
	// Nothing to do about a copy that fails half way: the status and the
	// length have gone by, and the client -- which knows what it asked
	// for and what arrived -- is the end that can tell. This package
	// logs nowhere else either.
	_, _ = io.Copy(w, body)
}

// handleGetCICache answers "what is this cache holding, and is it
// working" -- GET /api/ci/cache.
//
// Ungated, like the listing beside it: it is a count, a size and a hit
// rate, it names no repository and no commit, and an operator who can
// see that CI ran at all can see how much of it did not have to.
func (s *Server) handleGetCICache(w http.ResponseWriter, r *http.Request) {
	runner := s.tasks.Config.CI
	if runner == nil {
		writeJSON(w, http.StatusOK, CICacheResponse{Enabled: false})
		return
	}
	stats, ok, err := runner.CacheStats(r.Context())
	if err != nil {
		// A cache that cannot be read is a directory that has stopped
		// answering, which is this deployment's problem rather than the
		// caller's -- and worth reporting rather than drawing as an empty
		// cache, since "nothing is cached" is what a broken one looks
		// like from the outside.
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	resp := CICacheResponse{Enabled: true, Available: ok}
	if ok {
		view := ciCacheStats(stats)
		resp.Cache = &view
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleClearCICache empties the job cache -- DELETE /api/ci/cache.
//
// Behind the grain-ci capability, which is the gate on starting a run,
// because those are the two things here that cost a deployment work: a
// run spends sandboxes, and clearing the cache is every job of the next
// few runs doing itself over. Nothing else is at stake -- an entry is a
// promise that identical work was already done, never a fact only the
// cache holds (ci.InspectableCache) -- so this is not a destructive
// action that needs guarding, it is an expensive one.
func (s *Server) handleClearCICache(w http.ResponseWriter, r *http.Request) {
	runner := s.tasks.Config.CI
	if runner == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("this deployment has no grain CI wired"))
		return
	}
	enabled, err := s.tasks.ciEnabledAnywhere(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	if !enabled {
		writeError(w, http.StatusForbidden, fmt.Errorf(
			"grain CI is not enabled anywhere on this deployment, so there is no job cache to clear: attach the %q capability to a repo (Settings, Repos) or to this deployment's default capabilities",
			grainci.CapabilityName))
		return
	}
	removed, ok, err := runner.ClearCache(r.Context())
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf(
			"this deployment's grain CI has no job cache to clear -- every job runs, every time"))
		return
	}
	resp := CICacheResponse{Enabled: true, Available: true, Removed: &removed}
	if err != nil {
		// Partly cleared: the count is what it did manage, and saying
		// "cleared" over a directory that still holds entries would be
		// the wrong thing to tell somebody acting on a suspicion.
		writeError(w, http.StatusInternalServerError, fmt.Errorf("cleared %d entries: %w", removed, err))
		return
	}
	// Read back rather than assumed empty: a Save that landed between
	// the clear and now is a real thing, and this is the same shape the
	// GET answers with so a pane can simply take it.
	if stats, ok, err := runner.CacheStats(r.Context()); err == nil && ok {
		view := ciCacheStats(stats)
		resp.Cache = &view
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleCancelCIRun ends a run early, with the optional note whoever
// asked left about why.
//
// The note is read out of the request body rather than a query
// parameter, and an absent body is a cancellation with nothing to say --
// which is what every caller that predates it sends (the CLI without its
// flag, `curl -X DELETE`), and what handleClose does with its own
// late-arriving field, for the same reason.
func (s *Server) handleCancelCIRun(w http.ResponseWriter, r *http.Request) {
	runner := s.tasks.Config.CI
	if runner == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("this deployment has no grain CI wired"))
		return
	}
	var req CICancelRequest
	if !readOptionalJSON(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	// The one door a person cancels through, and therefore the one place
	// ci.CancelByOperator is said, so the run records whose hand was on
	// it. It stops this run, not the task: the merge queue's reconciler
	// replaces it with another run for the same commit after
	// ciRetryDelay, as it does any cancellation (orchestrator's
	// ciReplacementFor). Stopping the task is withdrawing auto-merge or
	// closing it.
	if !runner.Cancel(id, strings.TrimSpace(req.Reason), ci.CancelByOperator) {
		// Either it finished or it never existed; both are "there is
		// nothing here to cancel", and telling them apart would mean
		// reporting a 200 for a run that ignored the request.
		writeError(w, http.StatusNotFound, fmt.Errorf("no CI run %q is still going", id))
		return
	}
	run, _ := runner.Get(id)
	writeJSON(w, http.StatusAccepted, run)
}

// resetPullRequestPolls puts task's pull requests back on every sync
// cycle (github.PollSchedule): a run asking for CI on its branch --
// wait_for_checks, run_ci_job, retrigger_ci -- has just pushed or is about
// to be told a verdict, and the merge queue should read the pull request
// fresh rather than on an idle backoff (orchestrator's syncpoll.go).
func resetPullRequestPolls(task model.Task) {
	for _, l := range task.Links {
		if l.Kind != model.LinkFixes {
			continue
		}
		if ref, err := model.ParsePullRequestRef(l.Target); err == nil {
			github.Polls().Reset(github.PullRequestSubject(ref.Repo.Owner, ref.Repo.Name, ref.Number))
		}
	}
}
