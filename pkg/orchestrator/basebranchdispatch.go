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
	"slices"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// The work that follows a base branch but is not a verdict about it
// (grain/task-409).
//
// basebranchci.go beside this file is the verdict about that branch: it
// judges the tree everything merges into and spends an agent when that
// judgement fails. (It was the merge queue's gate as well until
// grain/task-471; it holds nothing now, and mergepredicate.go is what
// decides a merge.) This file is the other thing a merge used to set off
// and that nothing else does -- work whose output later branches *build
// against*, as against work that says whether the tree is sound.
//
// # The case it was written for
//
// `.grain/workflows/release.yml` fills the content-addressed sandbox
// guest cache on every merge into `main`
// (scripts/release-images.sh's `guest-cache` phase). That cache is not a
// release artifact: it is what makes every CI job resolving a sandbox
// guest a `docker pull` instead of `konturctl guest build` booting a VM
// inside a CI sandbox and pulling ~1GB into it. But it was produced only
// by the release run, so `RepoConfig.ReleaseWorkflowsDisabled` -- a
// switch named for publishing, reached for when a publish job is broken
// -- stopped it being filled, and every branch in flight paid tens of
// minutes for that on the longest job in its suite, an hour or so later,
// with nothing anywhere connecting the two (grain/task-397 measured it;
// docs/merge-queue-behind.md's "Publishing is also a CI input" has the
// day it cost).
//
// So the piece that is a build input is named separately from the
// publishing it happened to live in: RepoConfig.PostMergeDispatchJobs,
// dispatched here whatever the release switch says.
//
// # And named by grain, for the one repository it can name it for
//
// Naming it was still a step, and for the case above it is a step
// nobody should have to take: an operator switching releases off
// because the publisher is broken has not asked for slower CI, and the
// setting that would have saved them is one they have to know exists
// before the bill arrives (grain/task-457 -- the operator's words were
// "I should not have to manually re-enable the cache; it should just
// continue to work").
//
// Grain cannot infer this for an arbitrary repository, which is why the
// setting is a list. It can infer it for exactly one: the repository
// that publishes the sandbox guest *this deployment runs*, read off the
// image its own sandboxes are created from. For that repository, with
// releases off and `package-publish` already granted to its CI, the
// guest cache job is added to whatever the operator named
// (model.EffectivePostMergeDispatchJobs, whose header has the three
// conditions and why each is there). Turning releases back on removes
// it again, because the release run has taken the work back.
//
// # Under `workflow_dispatch`, and why that is the honest event
//
// A job that fills a registry cache pushes to a registry, so it needs
// the package-publish capability, whose material is placed only for a
// release or a dispatch and never for a push
// (pkg/capability/packagepublish.SupportsCIEvent). That gate is what
// keeps a repository-writing credential off an untrusted branch push, so
// it is honoured rather than widened: what grain asks for here is a
// dispatch, which is what an operator naming a job in operator-owned
// repository configuration has asked for. The alternative -- letting the
// post-merge *push* run carry publishing material because grain started
// it -- would make the capability's own gate depend on who asked rather
// than on what the run is, and the answer would then have to be right
// for every repository rather than for this one.
//
// # It cannot hold the merge queue, by construction
//
// Nothing here writes model.BaseBranchCI, and the queue reads nothing
// else. A dispatch that fails is recorded on its own row
// (model.BaseBranchDispatch), logged, and left: what failed is a cache
// that stayed cold, which costs some later branch a slow job, and a
// merge queue stopped by that would be a much larger outage than the one
// it was reporting. Nothing is retried for the same commit either -- the
// branch's next merge asks again, and a 25-minute build re-dispatched
// every cycle would eat a deployment's whole CI capacity.
//
// # What keeps it to one run
//
// The row is the lock, exactly as model.BaseBranchCI's is: a branch with
// a dispatch in flight starts no other, and merges landing in the
// meantime move the tip rather than queueing runs. The cycle after the
// run concludes starts one against the tip as it then stands, so
// intermediate commits are skipped rather than built -- which is right
// for a cache, because the key a consumer misses on is derived from the
// tree they are on, not from one grain merged past.
//
// On top of that, one dispatch is started per cycle across the whole
// deployment. A repository with several watched base branches, or a
// deployment that has just had this named on two repositories at once,
// pays for them one after another rather than all at once on the tick
// after the setting was saved -- seedWatchedBaseBranches is held to the
// same rule and for the same reason, a CI host that has been over its
// sandbox budget twice in a week.

// reconcileBaseBranchDispatch collects whatever dispatch is in flight,
// then starts at most one for a base branch whose tip has moved past the
// commit its last dispatch was for -- or whose repository now names jobs
// that row was not dispatched with, which is what makes turning the
// setting on take effect without waiting for a merge.
func reconcileBaseBranchDispatch(ctx context.Context, deps Deps, now time.Time) error {
	if deps.CI == nil || deps.Store == nil {
		return nil // this deployment runs no CI of its own
	}
	rows, err := deps.Store.ListBaseBranchDispatch(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading base branch dispatches: %w", err)
	}
	var errs []error
	busy := map[string]bool{}
	for _, row := range rows {
		if !row.InFlight() {
			continue
		}
		// In flight at the start of this cycle, so nothing new is started
		// for the branch on it -- whatever the conclusion recorded here
		// turns out to be. The cycle after is soon enough, and it reads
		// the tip as it stands then.
		busy[dispatchKey(row.Repo, row.Branch)] = true
		if err := collectBaseBranchDispatch(ctx, deps, row, now); err != nil {
			errs = append(errs, err)
		}
	}
	if err := startDueBaseBranchDispatch(ctx, deps, busy, now); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// dispatchKey identifies one watched branch in this cycle's bookkeeping.
func dispatchKey(repo model.RepoRef, branch string) string {
	return repo.String() + "\x00" + branch
}

// startDueBaseBranchDispatch walks the repositories that name post-merge
// dispatch jobs and starts one dispatch, for the first branch that is
// owed one. Everything it skips it will find again on the next cycle,
// which is what makes "one per cycle" a delay rather than a loss.
func startDueBaseBranchDispatch(ctx context.Context, deps Deps, busy map[string]bool, now time.Time) error {
	configs, err := deps.Store.ListRepoConfigs(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading repo configs: %w", err)
	}
	ciRows, err := deps.Store.ListBaseBranchCI(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading base branch CI: %w", err)
	}
	var errs []error
	for _, cfg := range configs {
		// What this repository has asked for, plus what grain keeps
		// running for it unasked: the guest cache job, for the one
		// repository publishing the guest this deployment runs, while
		// its releases are off (model.EffectivePostMergeDispatchJobs and
		// this file's own header).
		jobs := model.EffectivePostMergeDispatchJobs(cfg, deps.Config.SandboxGuestImage)
		if len(jobs) == 0 {
			continue
		}
		if allowed, err := repoAllowedForAnyTeam(ctx, deps.Store, cfg.Repo); err != nil || !allowed {
			continue
		}
		// Post-merge jobs are standing work, and the guest cache job grain
		// adds unasked is one of them: none run for a repository this
		// deployment does not run them for (model.PostMergeItem,
		// grain/task-3332).
		if !model.RunsWorkItem(&cfg, model.PostMergeItem) {
			sayOnce("base-dispatch-off:"+cfg.Repo.String(),
				"orchestrator: not dispatching %s's post-merge jobs (%v): %s",
				cfg.Repo, jobs, model.StandingWorkOffReason(cfg.Repo))
			continue
		}
		enabled, err := grainCIEnabledForRepo(ctx, deps.Store, cfg.Repo)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !enabled {
			// A repository naming jobs that grain has no CI on is a
			// setting that quietly does nothing, which is the silence
			// grain/task-320 was filed about -- said once per daemon
			// rather than once per cycle.
			sayOnce("base-dispatch:"+cfg.Repo.String(),
				"orchestrator: %s names jobs to dispatch after a merge (%v), and grain CI is not "+
					"enabled for it: nothing is dispatched", cfg.Repo, jobs)
			continue
		}
		branches, err := baseBranchesToDispatchOn(ctx, deps, cfg.Repo, ciRows)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, branch := range branches {
			if busy[dispatchKey(cfg.Repo, branch)] {
				continue
			}
			started, err := startBaseBranchDispatch(ctx, deps, cfg, jobs, branch, now)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if started {
				return errors.Join(errs...) // one per cycle
			}
		}
	}
	return errors.Join(errs...)
}

// baseBranchesToDispatchOn is which of a repository's branches this is
// about: the base branches grain is already watching
// (model.BaseBranchCI, written by a merge or by an operator ticking
// WatchDefaultBranch), and the repository's default branch when it
// watches none.
//
// The fallback is not a nicety. A repository with releases off and no
// judging jobs named has no base-branch row at all -- recordPostMergeCI
// records nothing for a branch grain is not judging -- and that is
// exactly the repository this whole file exists for. Reading the watched
// rows alone would mean the setting did nothing precisely where it was
// needed.
func baseBranchesToDispatchOn(ctx context.Context, deps Deps, repo model.RepoRef,
	ciRows []model.BaseBranchCI) ([]string, error) {

	var out []string
	for _, row := range ciRows {
		if row.Repo.Same(repo) && row.Branch != "" {
			out = append(out, row.Branch)
		}
	}
	if len(out) > 0 {
		return out, nil
	}
	if deps.Client == nil {
		return nil, nil // no way to ask what the default branch is
	}
	branch, err := deps.Client.DefaultBranch(repo.Owner, repo.Name)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: reading %s's default branch: %w", repo, err)
	}
	if branch == "" {
		return nil, nil
	}
	return []string{branch}, nil
}

// grainCIEnabledForRepo is whether grain CI judges this repository at
// all, by the repository's own settings rather than by any task's tick:
// the grain-ci capability in the deployment's defaults or the repo's, or
// the one repository grain checks whether or not anybody opted it in.
//
// baseBranchCIWanted asks the same question on its way to a larger one.
// It is asked separately here because the answers it folds in are about
// the release switch -- and this file's whole point is to be independent
// of that switch.
func grainCIEnabledForRepo(ctx context.Context, store *model.Store, repo model.RepoRef) (bool, error) {
	if store == nil {
		return false, nil
	}
	if grainci.RequiredFor(repo) {
		return true, nil
	}
	grants, err := defaultTaskGrants(ctx, store, &repo, "")
	if err != nil {
		return false, fmt.Errorf("orchestrator: reading grain CI settings for %s: %w", repo, err)
	}
	return grainci.Granted(grants), nil
}

// startBaseBranchDispatch starts one branch's dispatch if it is owed
// one, reporting whether it started anything.
//
// Owed is level-triggered and deliberately says nothing about merges:
// the branch's tip is not the commit the row was dispatched for, or the
// jobs it should be dispatched with are not the jobs it was. A merge is
// the ordinary way the first becomes true; a push straight to the
// branch, an operator saving the setting, and an operator switching
// releases off -- which is what adds the guest cache job to `jobs`
// without anybody naming it -- are the others, and all of them want the
// same thing done about them.
func startBaseBranchDispatch(ctx context.Context, deps Deps, cfg model.RepoConfig,
	jobs []string, branch string, now time.Time) (bool, error) {

	tip, err := branchTip(ctx, deps, cfg.Repo, branch)
	if err != nil {
		return false, err
	}
	if tip == "" {
		// Nothing to compare against -- no GitHub client, or a branch
		// that has gone. Not "the branch has moved".
		return false, nil
	}
	row, err := deps.Store.GetBaseBranchDispatch(ctx, cfg.Repo, branch)
	if err != nil {
		return false, err
	}
	if row != nil && row.Commit == tip && slices.Equal(row.Jobs, jobs) {
		return false, nil // already asked for, at this tree, with these jobs
	}
	run, err := deps.CI.Start(ctx, ci.Request{
		Repo: cfg.Repo.String(), Ref: branch, Commit: tip,
		// `workflow_dispatch`: the event this repository's operator asked
		// for by naming the jobs, and the only one besides a release that
		// a publishing capability's material may enter
		// (this file's own header, and RepoConfig.PostMergeDispatchJobs).
		// Untrusted: TrustedRelease is a release run's property, and this
		// is not one.
		Event: ci.EventManual, Jobs: jobs,
		RequestedBy: "post-merge:" + cfg.Repo.String() + ":" + branch,
	})
	if err != nil {
		// Logged and swallowed rather than returned as this cycle's
		// error: a repository whose dispatch cannot be planned -- a job
		// name that matches nothing in the dispatch plan is the likely
		// one -- must not make the reconciler that has just collected
		// every other branch's run report a failure. The row is left
		// alone, so the next cycle asks again and the operator's fix to
		// the name takes effect without anything else being done.
		log.Printf("orchestrator: dispatching %v on %s %s at %s: %v",
			jobs, cfg.Repo, branch, shortSHA(tip), err)
		return false, nil
	}
	started := run.StartedAt
	if started == nil {
		started = &now
	}
	if err := deps.Store.PutBaseBranchDispatch(ctx, model.BaseBranchDispatch{
		Repo: cfg.Repo, Branch: branch, Commit: tip, Jobs: jobs,
		RunID: run.ID, StartedAt: *started,
	}); err != nil {
		return false, err
	}
	log.Printf("orchestrator: %s %s is at %s: dispatching %v, which follows this branch whatever "+
		"its releases are doing", cfg.Repo, branch, shortSHA(tip), jobs)
	return true, nil
}

// collectBaseBranchDispatch reads back a dispatch that had not finished
// and records how it ended.
//
// A run the service no longer has -- pruned, or lost with the daemon
// that was running it -- is recorded as errored, for collectBaseBranchCI's
// reason: leaving the row in flight forever would leave a branch grain
// believes it is still dispatching work for, and nothing would ever be
// dispatched there again.
//
// Recording is all that happens to a failure. See this file's header:
// what failed is a cache that stayed cold, and the log line is the whole
// of what grain does about it -- no hold, no repair, no retry of this
// commit.
func collectBaseBranchDispatch(ctx context.Context, deps Deps, row model.BaseBranchDispatch,
	now time.Time) error {

	run, ok := deps.CI.Get(row.RunID)
	if !ok || run.Interrupted {
		row.Conclusion, row.Detail = string(ci.ConclusionErrored), baseCILostDetail(ok)
		row.FinishedAt = &now
		log.Printf("orchestrator: the dispatch of %v on %s %s at %s said nothing: %s",
			row.Jobs, row.Repo, row.Branch, shortSHA(row.Commit), row.Detail)
		return deps.Store.PutBaseBranchDispatch(ctx, row)
	}
	if !run.Conclusion.Done() {
		return nil // still going
	}
	row.Conclusion, row.Detail = string(run.Conclusion), ciDetail(run)
	finished := run.FinishedAt
	if finished == nil {
		finished = &now
	}
	row.FinishedAt = finished
	if err := deps.Store.PutBaseBranchDispatch(ctx, row); err != nil {
		return err
	}
	if run.Conclusion != ci.ConclusionSuccess {
		log.Printf("orchestrator: the dispatch of %v on %s %s at %s ended %s (%s): nothing is held "+
			"by that -- what it produces is a build input, so the cost is a later job doing the "+
			"work itself, and the next merge asks again",
			row.Jobs, row.Repo, row.Branch, shortSHA(row.Commit), run.Conclusion, row.Detail)
	}
	return nil
}
