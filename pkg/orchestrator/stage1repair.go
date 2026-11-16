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

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A red Stage 1 always has a high-priority owner
// (design/playbook/releases.playbook.md, section 15).
//
// The release train qualifies every release candidate with nightly.yml on
// the rc tag (Stage 1), and the same workflow may still run on the default
// branch. A red Stage 1 drops the rc, and the train's next rc is only as
// good as main, so a red Stage 1 left alone stalls every release. When a
// Stage 1 run concludes red, grain files one repair task of the repository
// at Stage1RepairPriority, carrying the failing jobs, their log tails, the
// run's link and its commit, with a nightly-green goal (on_met close): the
// next fully green Stage 1 closes it. While it is open, every further red
// run is appended to it as a comment -- it is never filed twice -- and its
// priority kept. stage1Unattended raises pipeline.nightly-red-unattended
// when nobody is working on it.
//
// A repository opts in by declaring the train's own trigger: an enabled
// github_workflow_run schedule watching the workflow on its release
// candidates (.grain/schedules/release-train-on-stage1.yml). Nothing else
// a nightly.yml does files anything here.

// Stage1RepairPriority is a Stage 1 repair's task priority: the release
// train's own (#4086), so a repair is never queued behind ordinary work.
const Stage1RepairPriority = 9

const (
	// stage1QueuedPatience is how long a repair may sit queued, never
	// started, before nobody is taken to be on it.
	stage1QueuedPatience = time.Hour
	// stage1WaitingPatience is how long a repair may sit deferred, paused
	// or awaiting a reply.
	stage1WaitingPatience = 3 * time.Hour
)

// stage1Repairer is who files, updates and watches Stage 1 repairs.
var stage1Repairer = model.Principal{Kind: model.PrincipalAutomation, ID: "stage1-repair"}

// rcTagPattern is a release candidate tag, the ref an rc's Stage 1 runs on.
var rcTagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$`)

// Stage1RepairTag is the tag every Stage 1 repair of repo carries.
func Stage1RepairTag(repo model.RepoRef) string {
	return "stage1-repair:" + strings.ToLower(repo.String())
}

// stage1Subject is repo's pipeline.nightly-red-unattended subject.
func stage1Subject(repo model.RepoRef) string {
	return model.NotificationSubject(model.FamilyNightlyRedUnattended, strings.ToLower(repo.String()))
}

// stage1Ref reports whether ref is one a Stage 1 verdict is read on: a
// release candidate tag, or the default branch.
func stage1Ref(ref, defaultBranch string) bool {
	ref = strings.TrimPrefix(strings.TrimPrefix(ref, "refs/heads/"), "refs/tags/")
	return rcTagPattern.MatchString(ref) || (defaultBranch != "" && ref == defaultBranch)
}

// stage1Watched reports whether repo has opted in for run's workflow: an
// enabled github_workflow_run schedule of repo watches it on a release
// candidate. It returns that schedule's base, the branch whose own runs
// count beside the rc tags.
func stage1Watched(ctx context.Context, store *model.Store, run ConcludedGitHubWorkflowRun) (string, bool, error) {
	schedules, err := store.ListSchedules(ctx)
	if err != nil {
		return "", false, err
	}
	for _, s := range schedules {
		trig := s.Recurrence.GitHubWorkflowRun
		if !s.Enabled || !s.Target.Same(run.Repo) || s.Recurrence.Kind != model.RecurrenceGitHubWorkflowRun || trig == nil {
			continue
		}
		if trig.MatchesWorkflow(run.WorkflowName, run.WorkflowPath) && trig.MatchesBranch("v1.0.0-rc.1", "") {
			base := strings.TrimSpace(s.Base)
			if base == "" {
				base = "main"
			}
			return base, true, nil
		}
	}
	return "", false, nil
}

// stage1RunSkipped reports whether a concluded run is one nightly.yml's
// idle-gate turned off: the scheduled run on the default branch while an
// rc's Stage 1 ran in the last 24h, where idle-gate passed and every other
// job, the stage-1 verdict included, was skipped. It carries no Stage 1
// verdict, so it neither files a repair nor meets a nightly-green goal.
func stage1RunSkipped(jobs []model.GitHubCIJob) bool {
	skipped := false
	for _, j := range jobs {
		if j.Failed() {
			return false
		}
		if strings.EqualFold(strings.TrimSpace(j.JobName), "idle-gate") {
			continue
		}
		if !strings.EqualFold(j.Conclusion, "skipped") {
			return false
		}
		skipped = true
	}
	return skipped
}

// stage1JobsGreen reports whether every job of a concluded run passed or
// was skipped: a cancelled job is not a verdict.
func stage1JobsGreen(conclusion string, jobs []model.GitHubCIJob) bool {
	if conclusion != "success" {
		return false
	}
	for _, j := range jobs {
		switch strings.ToLower(j.Conclusion) {
		case "success", "skipped", "neutral", "":
		default:
			return false
		}
	}
	return true
}

// handleStage1Verdict is what a concluded Stage 1 run does to its
// repository's repair: a red run files it, or appends to the open one; a
// green one is left to the nightly-green goal (nightlyGreenReading), which
// reconcileTaskGoals and this call both check.
func handleStage1Verdict(ctx context.Context, deps Deps, run ConcludedGitHubWorkflowRun, jobs []model.GitHubCIJob,
	now time.Time) error {

	if deps.Store == nil {
		return nil
	}
	base, ok, err := stage1Watched(ctx, deps.Store, run)
	if err != nil || !ok || !stage1Ref(run.Ref, base) {
		return err
	}
	if !model.GitHubCIFailed(run.Conclusion) && stage1RunSkipped(jobs) {
		// idle-gate turned this run off: no verdict either way.
		return nil
	}
	if stage1JobsGreen(run.Conclusion, jobs) {
		return checkNightlyGreenGoals(ctx, deps, run.Repo, run.WorkflowName, stage1Subject(run.Repo), stage1Repairer, now)
	}
	if !model.GitHubCIFailed(run.Conclusion) {
		// Cancelled, or a job neither red nor green: the train re-runs it
		// (candidate-gate.sh's INFRA), and it is nobody's repair yet.
		return nil
	}
	_, err = fileOrUpdateStage1Repair(ctx, deps.Store, run, jobs, now)
	return err
}

// openStage1Repair is repo's open Stage 1 repair, the oldest if a person
// has tagged more than one, or nil.
func openStage1Repair(ctx context.Context, store *model.Store, repo model.RepoRef) (*model.Task, error) {
	return openTaggedRepair(ctx, store, Stage1RepairTag(repo))
}

// openTaggedRepair is the oldest open task carrying tag, or nil.
func openTaggedRepair(ctx context.Context, store *model.Store, tag string) (*model.Task, error) {
	ids, err := store.OpenTasksWithTag(ctx, tag)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	sort.Strings(ids)
	return store.GetTask(ctx, ids[0])
}

// stage1Failures is a red run's failing jobs in words: each job, its
// failing step and the end of its log.
func stage1Failures(run ConcludedGitHubWorkflowRun, jobs []model.GitHubCIJob) string {
	return runFailures("Stage 1", run, jobs)
}

// runFailures is stage1Failures for a run the reader knows as label.
func runFailures(label string, run ConcludedGitHubWorkflowRun, jobs []model.GitHubCIJob) string {
	var b strings.Builder
	what := run.Ref
	if rcTagPattern.MatchString(what) {
		what = "release candidate `" + what + "`"
	} else {
		what = "`" + what + "`"
	}
	fmt.Fprintf(&b, "%s (`%s`, run %d attempt %d, %s) concluded **%s** on %s at commit `%s`.\n\nRun: %s\n",
		label, run.WorkflowName, run.RunID, max(1, run.Attempt), run.Event, run.Conclusion, what, run.HeadSHA, run.HTMLURL)
	failed := 0
	for _, j := range jobs {
		if !j.Failed() {
			continue
		}
		failed++
		fmt.Fprintf(&b, "\n- **%s** (%s)", j.JobName, j.Conclusion)
		if j.FailingStep != "" {
			fmt.Fprintf(&b, ", step `%s`", j.FailingStep)
		}
		if j.JobURL != "" {
			fmt.Fprintf(&b, ": %s", j.JobURL)
		}
		if tail := strings.TrimSpace(j.FailureTail); tail != "" {
			if len(tail) > 3000 {
				tail = "..." + tail[len(tail)-3000:]
			}
			fmt.Fprintf(&b, "\n\n  ```\n%s\n  ```\n", tail)
		}
	}
	if failed == 0 {
		b.WriteString("\nGrain has no failing job recorded for it yet; `grain ci log` reads the run.\n")
	}
	return b.String()
}

// fileOrUpdateStage1Repair files run's repository a Stage 1 repair, or,
// while one is open, appends run's failures to it and keeps its priority.
// It returns the repair's id.
func fileOrUpdateStage1Repair(ctx context.Context, store *model.Store, run ConcludedGitHubWorkflowRun,
	jobs []model.GitHubCIJob, now time.Time) (string, error) {

	short := run.HeadSHA
	if len(short) > 12 {
		short = short[:12]
	}
	return fileOrUpdateRepair(ctx, store, repairKind{
		what: "Stage 1", tag: Stage1RepairTag(run.Repo), by: stage1Repairer, priority: Stage1RepairPriority,
		title: fmt.Sprintf("Fix the red Stage 1 (%s) at %s", run.WorkflowName, short),
		intro: "Stage 1 of the release train (`" + run.WorkflowName + "`) is red, and every release waits on it " +
			"(design/playbook/releases.playbook.md, section 15). Find why and fix it on the default branch: a " +
			"regression, a flaky test, or the workflow itself. Leave anything you cannot fix as a precise report.\n\n" +
			"This task closes itself when the next Stage 1 run (an rc's or the default branch's) is fully green " +
			"(goal `nightly-green`). Further red runs are appended to it as comments while it is open.",
	}, run, jobs, now)
}

// repairKind is what one kind of red-run repair is filed as.
type repairKind struct {
	// what names the red thing in comments and logs ("Stage 1").
	what     string
	tag      string
	by       model.Principal
	priority int
	title    string
	// intro is the body's opening paragraphs, before the run's failures.
	intro string
}

// fileOrUpdateRepair files kind's repair of run's repository, or, while
// one carrying kind.tag is open, appends run's failures to it and keeps
// its priority at kind.priority. It returns the repair's id.
func fileOrUpdateRepair(ctx context.Context, store *model.Store, kind repairKind, run ConcludedGitHubWorkflowRun,
	jobs []model.GitHubCIJob, now time.Time) (string, error) {

	failures := runFailures(kind.what, run, jobs)
	open, err := openTaggedRepair(ctx, store, kind.tag)
	if err != nil {
		return "", err
	}
	if open != nil {
		if err := store.UpdateTask(ctx, open.ID, func(t *model.Task) error {
			if t.Priority < kind.priority {
				t.Priority = kind.priority
			}
			return nil
		}); err != nil {
			return "", fmt.Errorf("orchestrator: keeping %s repair %s's priority: %w", kind.what, open.ID, err)
		}
		if _, err := store.AddComment(ctx, model.Comment{
			TaskID: open.ID, Author: model.Attribution{Actor: kind.by}, CreatedAt: now,
			Body: kind.what + " is still red. Another failing run:\n\n" + failures,
		}); err != nil {
			return "", fmt.Errorf("orchestrator: appending run %d to %s repair %s: %w", run.RunID, kind.what, open.ID, err)
		}
		log.Printf("orchestrator: %s run %d on %s is red; appended it to the open repair %s", kind.what, run.RunID, run.Repo, open.ID)
		return open.ID, nil
	}
	id, err := store.NewTaskID(ctx)
	if err != nil {
		return "", fmt.Errorf("orchestrator: allocating a %s repair task ID: %w", kind.what, err)
	}
	orderKey, err := store.OrderKeyForNewTask(ctx, true)
	if err != nil {
		return "", fmt.Errorf("orchestrator: ordering the %s repair: %w", kind.what, err)
	}
	repo := run.Repo
	// The repair gets the CI grant every other filing path gives a task on
	// repo (defaultRepoGrants): grain#ci where grain CI gates it, and the
	// default api:ci-read@<repo> where GitHub Actions does and the
	// capability policy permits it. Without it the repair cannot run the
	// `grain ci log` its own body points it at, and parks asking a person
	// for a grant every other task already holds.
	grants, err := defaultRepoGrants(ctx, store, repo)
	if err != nil {
		return "", fmt.Errorf("orchestrator: granting the %s repair of %s: %w", kind.what, repo, err)
	}
	task := model.Task{
		ID:     id,
		Intent: model.IntentImplement,
		Title:  kind.title,
		Body:   kind.intro + "\n\n" + failures,
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: kind.by},
			Reason:      model.ReasonProposal,
		},
		Approval:   &model.Attribution{Actor: kind.by},
		ApprovedAt: &now,
		Target:     &repo,
		Binding:    model.BindingDirective,
		Priority:   kind.priority,
		Tags:       []string{kind.tag},
		Grants:     grants,
		CreatedAt:  &now,
		OrderKey:   orderKey,
	}
	if rcTagPattern.MatchString(run.Ref) {
		// Red on a release candidate: the repair is a defect that
		// candidate shipped (model.LinkFoundIn). A red default branch is
		// against no release yet.
		model.SetFoundIn(&task, run.Ref)
	}
	if err := store.PutTask(ctx, task); err != nil {
		return "", fmt.Errorf("orchestrator: filing the %s repair of %s: %w", kind.what, repo, err)
	}
	if err := store.SetTaskGoal(ctx, id, model.GoalDecl{
		Kind: model.GoalNightlyGreen, Spec: model.GoalSpec{Repo: repo.String(), Workflow: run.WorkflowName},
		OnMet: model.GoalCloseOnMet,
	}, model.PrincipalString(kind.by), now); err != nil {
		return "", fmt.Errorf("orchestrator: declaring %s repair %s's goal: %w", kind.what, id, err)
	}
	log.Printf("orchestrator: %s run %d on %s is red; filed repair %s at priority %d", kind.what, run.RunID, repo, id, kind.priority)
	return id, nil
}

func isGitHubBaseRepairGoal(g model.TaskGoal) bool {
	return g.DeclaredBy == model.PrincipalString(githubBaseRepairer) || isGitHubGatingWorkflow(g.Spec.Workflow, "")
}

// nightlyGreenReading is a nightly-green goal's check: the newest Stage 1
// run of the workflow recorded since the goal was declared, met when every
// job of it passed.
func nightlyGreenReading(ctx context.Context, store *model.Store, g model.TaskGoal) (goalReading, error) {
	if isGitHubBaseRepairGoal(g) {
		// A gating workflow runs on pull requests too, and a fork's pull
		// request opened from its own main records job rows on "main" that
		// github_ci_job cannot tell from the default branch's. Only a run
		// handleGitHubBaseVerdict saw pushed to the default branch moves or
		// meets this goal (meetGitHubBaseGoals / noteRedGitHubBaseGoals):
		// unverified job rows never reset g.Measure's progress limit and never
		// clear g.MetAt while a running repair finishes its pause grace.
		if g.MetAt != nil {
			return goalReading{met: true, measure: "green", evidence: g.Evidence}, nil
		}
		measure := g.Measure
		if measure == "" {
			measure = "awaiting-push"
		}
		evidence := g.Evidence
		if evidence == "" {
			evidence = fmt.Sprintf("awaiting a green `%s` push on %s's default branch", g.Spec.Workflow, g.Spec.Repo)
		}
		return goalReading{measure: measure, evidence: evidence}, nil
	}
	jobs, err := store.ListGitHubCIJobs(ctx, model.GitHubCIFilter{Repo: g.Spec.Repo, Since: g.DeclaredAt, Limit: 500})
	if err != nil {
		return goalReading{}, err
	}
	// Jobs come newest first; the first matching run that idle-gate did not
	// turn off, and that was not cancelled before its verdict, is the
	// newest verdict.
	var order []int64
	byRun := map[int64][]model.GitHubCIJob{}
	for _, j := range jobs {
		if !strings.EqualFold(j.Workflow, g.Spec.Workflow) || j.RunID == 0 || !stage1Ref(j.Ref, "") && !isDefaultLike(j.Ref) {
			continue
		}
		if _, seen := byRun[j.RunID]; !seen {
			order = append(order, j.RunID)
		}
		byRun[j.RunID] = append(byRun[j.RunID], j)
	}
	var runID int64
	var run []model.GitHubCIJob
	for _, id := range order {
		if !stage1RunSkipped(byRun[id]) && !runCancelledUnjudged(byRun[id]) {
			runID, run = id, byRun[id]
			break
		}
	}
	if runID == 0 {
		return goalReading{measure: "none",
			evidence: fmt.Sprintf("no `%s` run on %s has concluded since the repair was filed", g.Spec.Workflow, g.Spec.Repo)}, nil
	}
	var red []string
	for _, j := range run {
		switch strings.ToLower(j.Conclusion) {
		case "success", "skipped", "neutral":
		default:
			red = append(red, j.JobName)
		}
	}
	sort.Strings(red)
	what := fmt.Sprintf("`%s` run %d on `%s` (%s)", g.Spec.Workflow, runID, run[0].Ref, shortSHA(run[0].Commit))
	if len(red) > 0 {
		return goalReading{measure: strings.Join(red, ","),
			evidence: fmt.Sprintf("%s has %d job(s) not green: %s", what, len(red), strings.Join(red, ", "))}, nil
	}
	return goalReading{met: true, measure: "green", evidence: what + " is green"}, nil
}

// runCancelledUnjudged reports whether a run's jobs reached no verdict: none
// failed, and at least one was cancelled -- a run cancel-in-progress
// stopped when the next push arrived.
func runCancelledUnjudged(jobs []model.GitHubCIJob) bool {
	cancelled := false
	for _, j := range jobs {
		if j.Failed() {
			return false
		}
		cancelled = cancelled || strings.EqualFold(j.Conclusion, "cancelled")
	}
	return cancelled
}

// isDefaultLike is a branch a nightly-green goal counts beside rc tags:
// the default branch, which a goal row does not name, so "main" or
// "master".
func isDefaultLike(ref string) bool {
	ref = strings.TrimPrefix(ref, "refs/heads/")
	return ref == "main" || ref == "master"
}

// checkNightlyGreenGoals checks every open nightly-green goal of repo's
// workflow now, on the green verdict that has just landed, rather than on
// the next reconcile, and clears the notification subject raised about it.
func checkNightlyGreenGoals(ctx context.Context, deps Deps, repo model.RepoRef, workflow, subject string,
	by model.Principal, now time.Time) error {
	goals, err := deps.Store.OpenTaskGoals(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, g := range goals {
		if g.Kind != model.GoalNightlyGreen || !strings.EqualFold(g.Spec.Repo, repo.String()) ||
			!strings.EqualFold(g.Spec.Workflow, workflow) {
			continue
		}
		errs = append(errs, reconcileTaskGoal(ctx, deps, g, now))
	}
	errs = append(errs, clearPipelineNotification(ctx, deps.Store, subject, model.PrincipalString(by)))
	return errors.Join(errs...)
}

// stage1Unattended is why repair is unattended as of now, or "": queued and
// never started past stage1QueuedPatience, or deferred, paused or awaiting
// a reply past stage1WaitingPatience -- and, for a question, what it asks.
func stage1Unattended(ctx context.Context, store *model.Store, repair model.Task, now time.Time) (string, error) {
	state, err := store.State(ctx, repair.ID)
	if err != nil {
		return "", err
	}
	runs, err := store.Runs(ctx, repair.ID)
	if err != nil {
		return "", err
	}
	since := now
	if repair.CreatedAt != nil {
		since = *repair.CreatedAt
	}
	var lastEnd *time.Time
	for _, r := range runs {
		if r.FinishedAt != nil && (lastEnd == nil || r.FinishedAt.After(*lastEnd)) {
			lastEnd = r.FinishedAt
		}
	}
	switch state {
	case model.StateQueued:
		if len(runs) == 0 && now.Sub(since) >= stage1QueuedPatience {
			return fmt.Sprintf("it has been queued for %s and has never started", now.Sub(since).Round(time.Minute)), nil
		}
	case model.StateDeferred:
		at := since
		if repair.DeferredAt != nil {
			at = *repair.DeferredAt
		}
		if now.Sub(at) >= stage1WaitingPatience {
			why := "deferred"
			if repair.DeferredUntil != nil {
				why = "deferred (paused) until " + repair.DeferredUntil.UTC().Format(time.RFC3339)
			}
			return fmt.Sprintf("it has been %s for %s", why, now.Sub(at).Round(time.Minute)), nil
		}
	case model.StateAwaitingReply:
		at := since
		if lastEnd != nil {
			at = *lastEnd
		}
		if now.Sub(at) < stage1WaitingPatience {
			return "", nil
		}
		why := fmt.Sprintf("it has been awaiting a reply for %s", now.Sub(at).Round(time.Minute))
		if obs, err := store.GetObservation(ctx, repair.ID); err == nil && obs != nil && obs.PendingQuestionCommentID != nil {
			comments, err := store.Comments(ctx, repair.ID)
			if err == nil {
				for _, c := range comments {
					if c.ID == *obs.PendingQuestionCommentID {
						q := strings.TrimSpace(c.Body)
						if len(q) > 500 {
							q = q[:500] + "..."
						}
						why += ", asking: " + q
					}
				}
			}
		}
		return why, nil
	}
	return "", nil
}

// reconcileStage1Repairs raises pipeline.nightly-red-unattended for each
// repository whose open Stage 1 repair nobody is working on, and clears it
// once somebody is (the repair running, or closed by a green Stage 1).
func reconcileStage1Repairs(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil {
		return nil
	}
	repos, err := deps.Store.TargetRepos(ctx)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	var errs []error
	check := func(repo model.RepoRef) {
		key := strings.ToLower(repo.String())
		if seen[key] {
			return
		}
		seen[key] = true
		repair, err := openStage1Repair(ctx, deps.Store, repo)
		if err != nil {
			errs = append(errs, err)
			return
		}
		why := ""
		if repair != nil {
			if why, err = stage1Unattended(ctx, deps.Store, *repair, now); err != nil {
				errs = append(errs, err)
				return
			}
		}
		if why == "" {
			errs = append(errs, clearPipelineNotification(ctx, deps.Store, stage1Subject(repo),
				model.PrincipalString(stage1Repairer)))
			return
		}
		errs = append(errs, ensurePipelineNotification(ctx, deps.Store, model.Notification{
			Subject:  stage1Subject(repo),
			Family:   model.FamilyNightlyRedUnattended,
			Severity: model.NotificationCritical,
			Title:    fmt.Sprintf("Stage 1 of %s is red and its repair %s is unattended", repo, repair.ID),
			Body: fmt.Sprintf("Stage 1 (nightly.yml) of %s is red, which stalls every release, and its repair "+
				"[task %s](/tasks/%s) (%q) is not being worked on: %s.", repo, repair.ID, repair.ID, repair.Title, why),
			TaskID:   repair.ID,
			Repo:     repo.String(),
			RaisedBy: model.PrincipalString(stage1Repairer),
		}, now))
	}
	for _, r := range repos {
		if repo, err := model.ParseRepo(r); err == nil {
			check(repo)
		}
	}
	// A repair of a repository off the target list (an empty list is
	// every repository) still has its notification kept.
	schedules, err := deps.Store.ListSchedules(ctx)
	if err == nil {
		for _, s := range schedules {
			if s.Recurrence.Kind == model.RecurrenceGitHubWorkflowRun && s.Target.Owner != "" {
				check(s.Target)
			}
		}
	}
	return errors.Join(append(errs, err)...)
}
