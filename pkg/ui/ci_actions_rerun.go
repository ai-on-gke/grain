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

// ci_actions_rerun.go is ci:write for a repository whose CI is GitHub
// Actions: re-running a concluded workflow run's failed jobs, the whole
// run, or one job of it, as the run's next attempt -- what grain ci
// retrigger and grain ci run-job do on grain CI. Without it an agent
// whose pull request went red on a runner outage could only push an
// empty commit, which moves the pull request's head, throws away its
// approvals and files a fresh review.
//
// Two doors reach it. The sandbox CLI's (SandboxCLIActions.
// RerunGitHubChecks) acts on the calling task's own branch, at the head
// of its own pull request, and on nothing else: it names no repository
// and no run. POST /api/ci/runs/{id}/rerun names any gha: run, and so is
// ci:write on that run's repository, the scope that already says "start,
// re-run and cancel CI runs".
//
// Both go through the daemon's own forge connection, and both are bound
// per head: no workflow run at a commit is re-run past
// MaxActionsRerunsPerHead attempts beyond its first. The count is
// GitHub's own run_attempt, the same one the merge queue's automatic
// re-runs of an infrastructure failure spend (orchestrator's
// waitOutInfraFailure), so the two share one budget rather than each
// looping on its own, and a new commit -- a new question -- starts again
// at attempt 1.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
)

// MaxActionsRerunsPerHead is how many times any one workflow run at a
// commit may be re-run, by anyone grain acts for: three attempts beyond
// the first. The merge queue's own re-runs of an infrastructure failure
// (two at most) count towards it.
const MaxActionsRerunsPerHead = 3

// actionsRerunListLimit is how many of a branch's newest runs are read to
// find the ones at its head.
const actionsRerunListLimit = 50

// ActionsRerunRequest is what to re-run. With neither Jobs nor All it is
// the failed (and cancelled) jobs of every concluded run at the head.
type ActionsRerunRequest struct {
	// Jobs names jobs, by name or id, to re-run each on its own.
	Jobs []string `json:"jobs,omitempty"`
	// All re-runs every job of each concluded run, not only the failed.
	All bool `json:"all,omitempty"`
	// Workflow narrows to runs of one workflow: its file, its file's
	// stem, or its name.
	Workflow string `json:"workflow,omitempty"`
	// Commit, when set, must be the head the runs are at. The sandbox
	// CLI's door refuses any other, since its runs are its own head's.
	Commit string `json:"commit,omitempty"`
}

// ActionsRerunResult is what was re-run.
type ActionsRerunResult struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch,omitempty"`
	Head   string `json:"head"`
	// Runs is each workflow run asked to start a new attempt.
	Runs []ActionsRerunRun `json:"runs"`
	// Skipped is each run at the head left alone, and why.
	Skipped []string `json:"skipped,omitempty"`
	// Bound is MaxActionsRerunsPerHead.
	Bound int `json:"bound"`
}

// ActionsRerunRun is one workflow run asked to re-run.
type ActionsRerunRun struct {
	ID       string `json:"id"`
	Workflow string `json:"workflow"`
	// Mode is "failed" (rerun-failed-jobs), "all" (rerun) or "jobs".
	Mode string   `json:"mode"`
	Jobs []string `json:"jobs,omitempty"`
	// Attempt is the attempt the re-run starts.
	Attempt int `json:"attempt"`
}

// Text is the result as grain ci retrigger prints it.
func (r ActionsRerunResult) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Re-ran GitHub Actions on %s at %s:\n", r.Repo, shortHead(r.Head))
	for _, run := range r.Runs {
		what := "its failed jobs"
		switch run.Mode {
		case "all":
			what = "every job"
		case "jobs":
			what = strings.Join(run.Jobs, ", ")
		}
		fmt.Fprintf(&b, "  %s (%s): %s -- attempt %d of at most %d at this head\n",
			run.ID, run.Workflow, what, run.Attempt, r.Bound+1)
	}
	for _, s := range r.Skipped {
		fmt.Fprintf(&b, "  skipped %s\n", s)
	}
	fmt.Fprintf(&b, "No commit was pushed: the pull request's head, approvals and reviews are unchanged.\n")
	fmt.Fprintf(&b, "Each run at a commit is re-run at most %d times, grain's own automatic re-runs of a runner fault included; "+
		"past that, the failure is the change's to fix.\n", r.Bound)
	fmt.Fprintf(&b, "Wait for it with: grain ci wait\n")
	return b.String()
}

// RerunGitHubChecks is grain ci retrigger (and run-job) for taskID on a
// repository gated by GitHub Actions: req re-run at the head of the
// task's own pull request -- or its branch's newest commit with runs,
// before there is one -- and nowhere else.
func (a SandboxCLIActions) RerunGitHubChecks(ctx context.Context, taskID string, req gitproxy.CIStartRequest) (gitproxy.CLIActionResult, error) {
	res, err := a.Client.RerunTaskActions(ctx, taskID, ActionsRerunRequest{
		Jobs:     req.Jobs,
		All:      len(req.Jobs) == 0 && !req.FailedOnly,
		Workflow: req.Workflow,
		Commit:   req.Commit,
	})
	if err != nil {
		return gitproxy.CLIActionResult{}, cliActionError(err)
	}
	return gitproxy.CLIActionResult{Text: res.Text()}, nil
}

// RerunTaskActions re-runs taskID's own Actions runs (RerunGitHubChecks).
func (c *Client) RerunTaskActions(ctx context.Context, taskID string, req ActionsRerunRequest) (ActionsRerunResult, error) {
	task, err := SandboxCLIActions{Client: c}.resolveCITask(ctx, taskID)
	if err != nil {
		return ActionsRerunResult{}, err
	}
	if task == nil || task.Target == nil {
		return ActionsRerunResult{}, validationErrorf("task %s has no target repository", taskID)
	}
	branch := task.BranchName()
	if branch == "" {
		return ActionsRerunResult{}, validationErrorf("task %s has no branch of its own for CI to run on", taskID)
	}
	repo := *task.Target
	head := ""
	if pr := model.TaskPullRequest(*task); pr != nil && (pr.Repo.Same(*task.Target) || model.IsUpstreamRepoFor(*task.Target, pr.Repo)) {
		// A pull request from a fork runs its checks in the repository
		// it is opened against.
		repo = pr.Repo
		if c.Config.GitHubPullRequests != nil {
			if detail, err := c.Config.GitHubPullRequests.GetPullRequest(pr.Repo.StoredOwner(), pr.Repo.Name, pr.Number); err == nil {
				head = detail.HeadSHA
			}
		}
		if head == "" {
			// Without the pull request's head, the newest run on a branch
			// of this name could be another fork's: refuse rather than
			// guess.
			return ActionsRerunResult{}, fmt.Errorf("the head of %s#%d could not be read from the forge, so its checks were not re-run: try again shortly", pr.Repo, pr.Number)
		}
	}
	if want := strings.TrimSpace(req.Commit); want != "" && head != "" && !sameSHA(want, head) {
		return ActionsRerunResult{}, forbiddenErrorf("%s is not the head of task %s's pull request (%s): "+
			"a run re-runs only its own pull request's checks, at its head", want, taskID, shortHead(head))
	}
	resetPullRequestPolls(*task)
	return c.rerunActions(ctx, repo, branch, head, 0, req)
}

// RerunActionsRun re-runs req of the Actions run id names (a gha: id):
// POST /api/ci/runs/{id}/rerun, under ci:write on its repository.
func (c *Client) RerunActionsRun(ctx context.Context, id string, req ActionsRerunRequest) (ActionsRerunResult, error) {
	repoText, runID, ok := ParseActionsRunID(id)
	if !ok {
		return ActionsRerunResult{}, validationErrorf("%q is not a GitHub Actions run id: they are gha:<owner>:<name>:<run id>; "+
			"re-run a grain CI run with POST /api/ci/runs and recheck", id)
	}
	repo, err := c.parseRepo(repoText)
	if err != nil {
		return ActionsRerunResult{}, err
	}
	return c.rerunActions(ctx, repo, "", "", runID, req)
}

// rerunActions is both doors' work. With onlyRun it re-runs that run
// alone, at its own head; otherwise every run on branch at head (the
// branch's newest commit with runs when head is "").
func (c *Client) rerunActions(ctx context.Context, repo model.RepoRef, branch, head string, onlyRun int64,
	req ActionsRerunRequest) (ActionsRerunResult, error) {

	if refused := c.actionsRefusal(ctx, repo); refused != nil {
		return ActionsRerunResult{}, validationErrorf("%s", refused.reason)
	}
	reader, ok := forgeReader[github.ActionsReader](c)
	if !ok {
		return ActionsRerunResult{}, errNoActionsReader
	}
	rerunner, ok := forgeReader[github.ActionsRerunner](c)
	if !ok {
		return ActionsRerunResult{}, errors.New("this deployment's forge connection cannot re-run GitHub Actions runs")
	}
	owner := repo.StoredOwner()

	var runs []github.WorkflowRun
	if onlyRun > 0 {
		run, err := reader.GetActionsRun(owner, repo.Name, onlyRun)
		if err != nil {
			return ActionsRerunResult{}, fmt.Errorf("reading run %d of %s from the forge: %w", onlyRun, repo, err)
		}
		if run == nil {
			return ActionsRerunResult{}, &NotFoundError{ID: ActionsRunID(repo.String(), onlyRun)}
		}
		runs, head, branch = []github.WorkflowRun{*run}, run.HeadSHA, run.HeadBranch
	} else {
		listed, err := reader.ListActionsRuns(owner, repo.Name, github.ActionsRunsQuery{Branch: branch, Limit: actionsRerunListLimit})
		if err != nil {
			return ActionsRerunResult{}, fmt.Errorf("listing %s's GitHub Actions runs on %s: %w", repo, branch, err)
		}
		if head == "" {
			// No pull request pins the head: keep the runs of the branch
			// in repo itself, not a fork's branch of the same name.
			listed = slices.DeleteFunc(listed, func(run github.WorkflowRun) bool {
				return run.HeadRepo != "" && !strings.EqualFold(run.HeadRepo, repo.StoredOwner()+"/"+repo.Name)
			})
		}
		if head == "" && len(listed) > 0 {
			// Newest first: the branch's newest commit with runs.
			head = listed[0].HeadSHA
		}
		for _, run := range listed {
			if sameSHA(run.HeadSHA, head) {
				runs = append(runs, run)
			}
		}
	}
	if want := strings.TrimSpace(req.Commit); want != "" && head != "" && !sameSHA(want, head) {
		return ActionsRerunResult{}, validationErrorf("the runs asked about are at %s, not %s", shortHead(head), want)
	}
	if wf := strings.TrimSpace(req.Workflow); wf != "" {
		runs = slices.DeleteFunc(runs, func(run github.WorkflowRun) bool { return !workflowMatches(run, wf) })
	}
	if len(runs) == 0 {
		where := shortHead(head)
		if where == "" {
			where = branch
		}
		return ActionsRerunResult{}, &NotFoundError{ID: repo.String(),
			message: fmt.Sprintf("no GitHub Actions run of %s at %s to re-run", repo, where)}
	}

	res := ActionsRerunResult{Repo: repo.String(), Branch: branch, Head: head, Bound: MaxActionsRerunsPerHead}
	jobs := rerunJobNames(req.Jobs)
	if len(jobs) > 0 {
		return c.rerunActionsJobs(reader, rerunner, repo, runs, jobs, res)
	}
	// Every run to re-run is picked, and held to the bound, before any
	// is re-run: a run at the head the request leaves alone does not
	// refuse it, and one past the bound refuses it whole, not half-done.
	type target struct {
		run  github.WorkflowRun
		mode string
	}
	var targets []target
	for _, run := range runs {
		id := ActionsRunID(repo.String(), run.ID)
		if !strings.EqualFold(run.Status, "completed") {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s (%s): still %s", id, runWorkflow(run), orUnknown(run.Status)))
			continue
		}
		mode := "failed"
		switch {
		case req.All:
			mode = "all"
		case !actionsRunFailed(run):
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s (%s): concluded %s, nothing failed", id, runWorkflow(run), orUnknown(run.Conclusion)))
			continue
		}
		targets = append(targets, target{run: run, mode: mode})
	}
	if len(targets) == 0 {
		return res, validationErrorf("nothing to re-run at %s on %s: %s", shortHead(head), repo, strings.Join(res.Skipped, "; "))
	}
	for _, t := range targets {
		if err := rerunBound(repo, t.run, head); err != nil {
			return ActionsRerunResult{}, err
		}
	}
	for _, t := range targets {
		id := ActionsRunID(repo.String(), t.run.ID)
		var err error
		if t.mode == "all" {
			err = rerunner.RerunWorkflowRun(owner, repo.Name, t.run.ID)
		} else {
			err = rerunner.RerunFailedJobs(owner, repo.Name, t.run.ID)
		}
		if err != nil {
			return res, rerunRefusal(id, err)
		}
		res.Runs = append(res.Runs, ActionsRerunRun{ID: id, Workflow: runWorkflow(t.run), Mode: t.mode, Attempt: max(t.run.RunAttempt, 1) + 1})
	}
	return res, nil
}

// rerunBound is the per-head bound: run, at head, is not re-run once it
// has been re-run MaxActionsRerunsPerHead times, whoever asked for them.
func rerunBound(repo model.RepoRef, run github.WorkflowRun, head string) error {
	if spent := max(run.RunAttempt, 1) - 1; spent >= MaxActionsRerunsPerHead {
		return validationErrorf("run %s (%s) at %s has been re-run %d times already, the most grain re-runs any run at one commit "+
			"(grain's own automatic re-runs of a runner fault count too). A failure that survives that many attempts is not a flake: "+
			"fix it, or ask a person to re-run it by hand", ActionsRunID(repo.String(), run.ID), runWorkflow(run), shortHead(head), spent)
	}
	return nil
}

// rerunActionsJobs re-runs each job jobs names, wherever it is among
// runs: a job still running, or one no run has, is refused.
func (c *Client) rerunActionsJobs(reader github.ActionsReader, rerunner github.ActionsRerunner, repo model.RepoRef,
	runs []github.WorkflowRun, jobs []string, res ActionsRerunResult) (ActionsRerunResult, error) {

	owner := repo.StoredOwner()
	type pick struct {
		run  github.WorkflowRun
		jobs []github.ActionsRunJob
	}
	var picks []*pick
	found := map[string]bool{}
	var names []string
	for _, run := range runs {
		listed, err := reader.ListActionsJobs(owner, repo.Name, run.ID)
		if err != nil {
			return res, fmt.Errorf("listing the jobs of %s: %w", ActionsRunID(repo.String(), run.ID), err)
		}
		var p *pick
		for _, job := range listed {
			names = append(names, job.Name)
			for _, want := range jobs {
				if !strings.EqualFold(job.Name, want) && strconv.FormatInt(job.ID, 10) != want {
					continue
				}
				if !strings.EqualFold(job.Status, "completed") {
					return res, validationErrorf("job %q of %s is still %s: wait for it (grain ci wait) before re-running it",
						job.Name, ActionsRunID(repo.String(), run.ID), orUnknown(job.Status))
				}
				found[want] = true
				if p == nil {
					p = &pick{run: run}
					picks = append(picks, p)
				}
				p.jobs = append(p.jobs, job)
			}
		}
	}
	for _, want := range jobs {
		if !found[want] {
			return res, validationErrorf("no job %q in the GitHub Actions runs at %s (their jobs: %s)",
				want, shortHead(res.Head), strings.Join(names, ", "))
		}
	}
	for _, p := range picks {
		if err := rerunBound(repo, p.run, res.Head); err != nil {
			return res, err
		}
	}
	for _, p := range picks {
		id := ActionsRunID(repo.String(), p.run.ID)
		run := ActionsRerunRun{ID: id, Workflow: runWorkflow(p.run), Mode: "jobs", Attempt: max(p.run.RunAttempt, 1) + 1}
		for _, job := range p.jobs {
			if err := rerunner.RerunWorkflowJob(owner, repo.Name, job.ID); err != nil {
				return res, rerunRefusal(id, err)
			}
			run.Jobs = append(run.Jobs, job.Name)
		}
		res.Runs = append(res.Runs, run)
	}
	return res, nil
}

// rerunRefusal is GitHub's refusal to re-run id, said so a caller can
// tell a credential short of `actions: write` from anything else.
func rerunRefusal(id string, err error) error {
	var gh *github.Error
	if errors.As(err, &gh) && (gh.Status == http.StatusForbidden || gh.Status == http.StatusUnauthorized) {
		return forbiddenErrorf("GitHub refused to re-run %s: this deployment's forge connection needs `actions: write` on the repository (%v)", id, err)
	}
	return fmt.Errorf("re-running %s on the forge: %w", id, err)
}

func actionsRunFailed(run github.WorkflowRun) bool {
	switch strings.ToLower(run.Conclusion) {
	case "failure", "cancelled", "timed_out", "startup_failure":
		return true
	}
	return false
}

func runWorkflow(run github.WorkflowRun) string {
	if run.Name != "" {
		return run.Name
	}
	if run.Path != "" {
		return run.Path
	}
	return "workflow"
}

// workflowMatches is whether run is of the workflow wf names: its file
// (with or without .github/workflows/), the file's stem, or its name.
func workflowMatches(run github.WorkflowRun, wf string) bool {
	wf = strings.TrimPrefix(strings.TrimSpace(wf), ".github/workflows/")
	file := strings.TrimPrefix(run.Path, ".github/workflows/")
	if i := strings.IndexByte(file, '@'); i >= 0 {
		file = file[:i]
	}
	stem := strings.TrimSuffix(strings.TrimSuffix(file, ".yml"), ".yaml")
	return strings.EqualFold(wf, file) || strings.EqualFold(wf, stem) || strings.EqualFold(wf, run.Name)
}

// rerunJobNames is the jobs a request names, a comma-separated entry
// split, the blanks and a lone "*" (every job) left out.
func rerunJobNames(jobs []string) []string {
	var out []string
	for _, j := range jobs {
		for _, part := range strings.Split(j, ",") {
			if part = strings.TrimSpace(part); part != "" && part != "*" {
				out = append(out, part)
			}
		}
	}
	return out
}

func sameSHA(a, b string) bool {
	a, b = strings.ToLower(strings.TrimSpace(a)), strings.ToLower(strings.TrimSpace(b))
	if a == "" || b == "" {
		return false
	}
	return strings.HasPrefix(b, a) || strings.HasPrefix(a, b)
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// handleRerunCIRun is POST /api/ci/runs/{id}/rerun.
func (s *Server) handleRerunCIRun(w http.ResponseWriter, r *http.Request) {
	var req ActionsRerunRequest
	if !readOptionalJSON(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	res, err := s.tasks.RerunActionsRun(r.Context(), id, req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	log.Printf("ui: %s re-ran GitHub Actions run %s at %s (%d run(s))",
		s.tasks.requestActor(r.Context()).Actor.ID, id, shortHead(res.Head), len(res.Runs))
	writeJSON(w, http.StatusAccepted, res)
}
