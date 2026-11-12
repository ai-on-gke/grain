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
	"path"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A red default branch on GitHub Actions always has an owner, the way a
// red Stage 1 does (stage1repair.go).
//
// basebranchci.go's base-green repairs judge a default branch only where
// grain CI runs it: reconcileBaseBranchCI stands down any repository whose
// CI mode is github. So on a ci_mode: github repository nothing filed a
// task when the workflow that gates its merges went red on the default
// branch -- and a release train that cuts only from a commit whose
// tests.yml passed (scripts/candidate-gate.sh select-lkg) sat at "no new
// LKG" until a person noticed (ai-on-gke/grain, 2026-11-11, #4669).
//
// When a run of a gating workflow concludes failure on the default branch
// of a ci_mode: github repository, grain files one repair task at
// GitHubBaseRepairPriority with the failing jobs, their log tails, the
// run's link and its commit, and a nightly-green goal (on_met close) on
// that workflow: the next fully green run on the default branch closes
// it. While it is open every further red run is appended to it as a
// comment, never filed twice, and its priority kept.
// reconcileGitHubBaseRepairs raises pipeline.base-red-unattended when
// nobody is working on it, and pipeline.base-no-verdict when the gating
// workflows have run on the default branch but every run was cancelled
// before a verdict for GitHubBaseNoVerdictAfter.
//
// A cancelled run is never red: with cancel-in-progress most default-branch
// runs are cancelled by the next push (32 of 40 on ai-on-gke/grain's main on
// 2026-11-11). Only a concluded failure files or updates a repair.
//
// It is on by default for every ci_mode: github repository rather than
// opted in, because it is that mode's counterpart of the base-green repair
// grain CI repositories get without asking, and because a declaration a
// daemon cannot parse opts nothing in and says nothing (#4392).

// GitHubBaseRepairPriority is a default-branch repair's priority: the
// release train's own, which the red branch stalls.
const GitHubBaseRepairPriority = Stage1RepairPriority

// GitHubBaseNoVerdictAfter is how long a default branch's gating workflows
// may run without one finished verdict before pipeline.base-no-verdict.
const GitHubBaseNoVerdictAfter = 6 * time.Hour

// githubBaseRepairer is who files, updates and watches default-branch
// repairs.
var githubBaseRepairer = model.Principal{Kind: model.PrincipalAutomation, ID: "github-base-repair"}

// isGitHubGatingWorkflow reports whether a workflow is one that gates a
// repository's merges, and so decides whether its default branch is
// green: tests.yml, test.yml or ci.yml by file or by name. nightly.yml is
// Stage 1's (stage1repair.go) and release.yml release triage's
// (basebranchrelease.go), so neither is here.
func isGitHubGatingWorkflow(name, wfPath string) bool {
	for _, cand := range []string{path.Base(strings.TrimSpace(wfPath)), name} {
		s := strings.ToLower(strings.TrimSpace(cand))
		s = strings.TrimSuffix(strings.TrimSuffix(s, ".yml"), ".yaml")
		switch s {
		case "tests", "test", "ci":
			return true
		}
	}
	return false
}

// gitHubBaseRepairSubject is repo's pipeline.base-red-unattended subject.
func gitHubBaseRepairSubject(repo model.RepoRef) string {
	return model.NotificationSubject(model.FamilyBaseRedUnattended, strings.ToLower(repo.String()))
}

// gitHubBaseNoVerdictSubject is repo's pipeline.base-no-verdict subject.
func gitHubBaseNoVerdictSubject(repo model.RepoRef) string {
	return model.NotificationSubject(model.FamilyBaseNoVerdict, strings.ToLower(repo.String()))
}

// gitHubActionsJudges reports whether repo's CI is GitHub Actions
// (ci_mode: github), where grain's own base-branch repair stands down.
func gitHubActionsJudges(ctx context.Context, store *model.Store, repo model.RepoRef) (bool, error) {
	off, mode, err := repoExplicitlyDisablesGrainCI(ctx, store, repo)
	return err == nil && off && mode == model.CIModeGitHub, err
}

// handleGitHubBaseVerdict is what a concluded run of a gating workflow on
// a ci_mode: github repository's default branch does to its repair: a
// failure files it or appends to the open one, a fully green run checks
// its nightly-green goal now, and a cancelled one does nothing.
func handleGitHubBaseVerdict(ctx context.Context, deps Deps, run ConcludedGitHubWorkflowRun, jobs []model.GitHubCIJob,
	now time.Time) error {

	if deps.Store == nil || !isGitHubGatingWorkflow(run.WorkflowName, run.WorkflowPath) {
		return nil
	}
	// A fork's run, or a pull request's whose head branch is called main,
	// is not the default branch: its log tails would be an outsider's words
	// in an approved priority-9 directive. handleConcludedGitHubWorkflowRun
	// drops both already; this handler does not lean on that.
	if isGitHubWorkflowRunForeignHead(run.Repo, run.HeadRepo) || !isGitHubBranchWorkflowEvent(run.Event) {
		return nil
	}
	if !isGitHubBaseBranch(deps, run.Repo, run.Ref) {
		return nil
	}
	if ok, err := gitHubActionsJudges(ctx, deps.Store, run.Repo); err != nil || !ok {
		return err
	}
	if stage1JobsGreen(run.Conclusion, jobs) {
		return errors.Join(
			checkNightlyGreenGoals(ctx, deps, run.Repo, run.WorkflowName, gitHubBaseRepairSubject(run.Repo), githubBaseRepairer, now),
			clearPipelineNotification(ctx, deps.Store, gitHubBaseNoVerdictSubject(run.Repo), model.PrincipalString(githubBaseRepairer)))
	}
	if !model.GitHubCIFailed(run.Conclusion) {
		// Cancelled -- cancel-in-progress, by the next push -- or skipped:
		// no verdict about the tree, and nobody's repair.
		return nil
	}
	_, err := fileOrUpdateGitHubBaseRepair(ctx, deps.Store, run, jobs, now)
	return err
}

// fileOrUpdateGitHubBaseRepair files run's repository a default-branch
// repair, or appends run to the open one. It returns the repair's id.
func fileOrUpdateGitHubBaseRepair(ctx context.Context, store *model.Store, run ConcludedGitHubWorkflowRun,
	jobs []model.GitHubCIJob, now time.Time) (string, error) {

	branch := strings.TrimPrefix(run.Ref, "refs/heads/")
	wf := path.Base(run.WorkflowPath)
	if wf == "." || wf == "/" || wf == "" {
		wf = run.WorkflowName
	}
	return fileOrUpdateRepair(ctx, store, repairKind{
		what: "`" + branch + "`", tag: model.GitHubBaseRepairTag(run.Repo), by: githubBaseRepairer,
		priority: GitHubBaseRepairPriority,
		title:    fmt.Sprintf("Fix the red %s on %s at %s", wf, branch, shortSHA(run.HeadSHA)),
		intro: "`" + wf + "` is red on `" + branch + "`, the default branch of " + run.Repo.String() + ". It is the " +
			"workflow that gates merges, and a release train cuts only from a commit it passed, so nothing " +
			"releases until it is green again. Find why and fix it on `" + branch + "`: a regression two merges " +
			"made together, a flaky test, or the workflow itself. Leave anything you cannot fix as a precise " +
			"report.\n\n" +
			"This task closes itself when the next `" + run.WorkflowName + "` run on `" + branch + "` is fully " +
			"green (goal `nightly-green`); a cancelled run counts neither way. Further red runs are appended to " +
			"it as comments while it is open.",
	}, run, jobs, now)
}

// reconcileGitHubBaseRepairs raises pipeline.base-red-unattended for each
// ci_mode: github repository whose open default-branch repair nobody is
// working on (stage1Unattended's patience), and pipeline.base-no-verdict
// for each whose gating workflows have reached no verdict on the default
// branch for GitHubBaseNoVerdictAfter; each clears once it no longer
// holds.
func reconcileGitHubBaseRepairs(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil {
		return nil
	}
	configs, err := deps.Store.ListRepoConfigs(ctx)
	if err != nil {
		return err
	}
	repos := map[string]model.RepoRef{}
	for _, c := range configs {
		if c.CIMode == model.CIModeGitHub {
			repos[strings.ToLower(c.Repo.String())] = c.Repo
		}
	}
	// A repair of a repository no longer on Actions keeps its
	// notification reconciled until it closes.
	ids, err := deps.Store.OpenTasksWithTagPrefix(ctx, "github-base-repair:")
	if err != nil {
		return err
	}
	for _, id := range ids {
		if t, err := deps.Store.GetTask(ctx, id); err == nil && t != nil && t.Target != nil {
			repos[strings.ToLower(t.Target.String())] = *t.Target
		}
	}
	keys := make([]string, 0, len(repos))
	for k := range repos {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var errs []error
	for _, k := range keys {
		repo := repos[k]
		errs = append(errs, reconcileGitHubBaseRepair(ctx, deps.Store, repo, now),
			reconcileGitHubBaseNoVerdict(ctx, deps.Store, repo, now))
	}
	return errors.Join(errs...)
}

func reconcileGitHubBaseRepair(ctx context.Context, store *model.Store, repo model.RepoRef, now time.Time) error {
	repair, err := openTaggedRepair(ctx, store, model.GitHubBaseRepairTag(repo))
	if err != nil {
		return err
	}
	why := ""
	if repair != nil {
		if why, err = stage1Unattended(ctx, store, *repair, now); err != nil {
			return err
		}
	}
	if why == "" {
		return clearPipelineNotification(ctx, store, gitHubBaseRepairSubject(repo), model.PrincipalString(githubBaseRepairer))
	}
	return ensurePipelineNotification(ctx, store, model.Notification{
		Subject:  gitHubBaseRepairSubject(repo),
		Family:   model.FamilyBaseRedUnattended,
		Severity: model.NotificationCritical,
		Title:    fmt.Sprintf("The default branch of %s is red and its repair %s is unattended", repo, repair.ID),
		Body: fmt.Sprintf("The workflow that gates %s's merges is red on its default branch, which holds every "+
			"release, and its repair [task %s](/tasks/%s) (%q) is not being worked on: %s.", repo, repair.ID, repair.ID,
			repair.Title, why),
		TaskID:   repair.ID,
		Repo:     repo.String(),
		RaisedBy: model.PrincipalString(githubBaseRepairer),
	}, now)
}

// gitHubBaseVerdicts reads repo's recorded gating-workflow runs on its
// default branch (main or master: a reconcile does not ask the forge)
// since since: when the newest of them finished, when the newest that
// reached a verdict did, and when the oldest did.
func gitHubBaseVerdicts(ctx context.Context, store *model.Store, repo model.RepoRef, since time.Time) (
	newest, newestVerdict, oldest time.Time, err error) {

	jobs, err := store.ListGitHubCIJobs(ctx, model.GitHubCIFilter{
		Repo: repo.String(), Since: since, Refs: []string{"main", "master"}, Limit: 5000,
	})
	if err != nil {
		return
	}
	byRun := map[int64][]model.GitHubCIJob{}
	for _, j := range jobs {
		if j.RunID != 0 && isGitHubGatingWorkflow(j.Workflow, "") {
			byRun[j.RunID] = append(byRun[j.RunID], j)
		}
	}
	for _, run := range byRun {
		var at time.Time
		for _, j := range run {
			if j.CompletedAt != nil && j.CompletedAt.After(at) {
				at = *j.CompletedAt
			} else if j.CompletedAt == nil && j.UpdatedAt.After(at) {
				at = j.UpdatedAt
			}
		}
		if at.After(newest) {
			newest = at
		}
		if oldest.IsZero() || at.Before(oldest) {
			oldest = at
		}
		if !runCancelledUnjudged(run) && at.After(newestVerdict) {
			newestVerdict = at
		}
	}
	return
}

func reconcileGitHubBaseNoVerdict(ctx context.Context, store *model.Store, repo model.RepoRef, now time.Time) error {
	clear := func() error {
		return clearPipelineNotification(ctx, store, gitHubBaseNoVerdictSubject(repo), model.PrincipalString(githubBaseRepairer))
	}
	if ok, err := gitHubActionsJudges(ctx, store, repo); err != nil || !ok {
		if err != nil {
			return err
		}
		return clear()
	}
	newest, verdict, oldest, err := gitHubBaseVerdicts(ctx, store, repo, now.Add(-4*GitHubBaseNoVerdictAfter))
	if err != nil {
		return err
	}
	since := verdict
	if since.IsZero() {
		since = oldest
	}
	if newest.IsZero() || !newest.After(verdict) || now.Sub(since) < GitHubBaseNoVerdictAfter {
		return clear()
	}
	last := "none in the last " + (4 * GitHubBaseNoVerdictAfter).String()
	if !verdict.IsZero() {
		last = "the last was " + now.Sub(verdict).Round(time.Minute).String() + " ago, at " + verdict.UTC().Format(time.RFC3339)
	}
	return ensurePipelineNotification(ctx, store, model.Notification{
		Subject:  gitHubBaseNoVerdictSubject(repo),
		Family:   model.FamilyBaseNoVerdict,
		Severity: model.NotificationWarning,
		Title:    fmt.Sprintf("The default branch of %s has had no finished CI verdict for %s", repo, now.Sub(since).Round(time.Minute)),
		Body: fmt.Sprintf("The workflow that gates %s's merges keeps running on its default branch, but every run "+
			"since %s was cancelled before it finished (cancel-in-progress, by the next push), so no commit is known "+
			"good and a release train that cuts only from a green commit cuts nothing. Finished verdicts: %s. A quiet "+
			"spell between merges lets one finish; so does re-running the newest run.",
			repo, since.UTC().Format(time.RFC3339), last),
		Repo:     repo.String(),
		RaisedBy: model.PrincipalString(githubBaseRepairer),
	}, now)
}
