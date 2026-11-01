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
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// The publishing a merge sets off, which is no longer the verdict about
// the tree it publishes (grain/task-461).
//
// # What this file took out of the gate
//
// Until this, a base branch's post-merge run *was* the trusted release
// event: `baseBranchCIWanted` returned `ci.EventRelease`, the run it
// planned published the images, and its conclusion was written to
// model.BaseBranchCI -- the row model.BaseBranchCI.Broken reads and the
// merge queue gates on. One run answered two questions, so "did the
// image publish" held every merge in the repository exactly as "do the
// tests pass" did.
//
// On this deployment that was the dominant failure mode for weeks, in
// both directions. With releases on, `release` matched
// `.grain/workflows/release.yml` and nothing else, so the gate was
// asking whether an image still published and running no test at all
// (grain/task-355). With releases off there was no release event, so
// nothing judged `main` -- 33 hours and ~100 merges with no gate
// (grain/task-458). And underneath both, the publisher is flaky
// (grain/task-456), so the gate was red or absent for reasons that had
// nothing to do with the code being merged. An operator ruled on it:
// *"Release should not be the gate. That is the CI's job. If we wanted
// release to be the gate then we would do it as part of the ci jobs"* --
// and, on a failed release nobody notices once it stops holding merges,
// *"Not noticing a failed release is a 3rd order problem. Our releases
// have literally never worked from grain in the recent past so gating
// the whole build on it is ludicrous."*
//
// So a merged tip now gets two runs. The judging one is a `push` of the
// repository's own plan, started and collected by basebranchci.go beside
// this file, and it is the whole of what the queue reads. The publishing
// one is started here, at the same tip, and recorded on a row of its own.
//
// # It cannot hold the merge queue, by construction
//
// Nothing here writes model.BaseBranchCI, and the queue reads nothing
// else -- basebranchdispatch.go's own rule, and this file is the second
// thing to be held to it. A release that fails is recorded on
// model.BaseBranchRelease, logged, and retried once if the branch is
// still on that commit -- no hold and no repair, and never more than
// model.MaxReleaseAttempts releases of one commit, because re-publishing
// a failing 25-minute image build every cycle would eat a deployment's
// whole CI capacity. The branch's next merge asks again either way:
// whatever the last release ended as, a tip it did not publish is
// released (grain/task-4432).
//
// Noticing a release that keeps failing is now built, and it is a
// reading rather than a repair: ui.BaseBranchPublishing says "nothing
// has published from `main` since Tuesday, and every release started
// since has published nothing" on the deployment health response and on
// the repository's own release pane (grain/task-511,
// pkg/ui/basebranchrelease.go). The only thing it needed from this file
// is bookkeeping -- the run of failures on model.BaseBranchRelease,
// counted by noteBaseBranchReleaseOutcome below and carried across the
// row's replace -- and it deliberately remains the case that nothing
// here holds the merge queue. A repository that cannot
// publish goes on merging; somebody is simply told.
//
// # What keeps it to one run, and off the judging run's toes
//
// The row is the lock, exactly as model.BaseBranchCI's and
// model.BaseBranchDispatch's are: a branch with a release in flight
// starts no other, and merges landing in the meantime move the tip
// rather than queueing publishes. The cycle after the run concludes
// starts one against the tip as it then stands.
//
// The two runs per tip do not contend. They are separate rows, so
// neither reconciler's one-in-flight bound (grain/task-334) sees the
// other's run; they are separate ci.Request events, so neither's plan
// contains the other's jobs on a native workflow; and nothing cancels a
// run for sharing a ref -- `collectBaseBranchCI` reads
// `model.BaseBranchCI.RunID` and therefore only ever collects the
// judging one. What they do share is the sandbox budget, which is why
// one release is started per cycle across the whole deployment,
// `startDueBaseBranchDispatch`'s rule and for its reason.

// reconcileBaseBranchRelease collects whatever release is in flight,
// then starts at most one for a base branch whose tip has moved past the
// commit its last release was started for.
func reconcileBaseBranchRelease(ctx context.Context, deps Deps, now time.Time) error {
	if deps.CI == nil || deps.Store == nil {
		return nil // this deployment runs no CI of its own
	}
	rows, err := deps.Store.ListBaseBranchRelease(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading base branch releases: %w", err)
	}
	var errs []error
	busy := map[string]bool{}
	for _, row := range rows {
		if !row.InFlight() {
			continue
		}
		if disabled, mode, err := repoExplicitlyDisablesGrainCI(ctx, deps.Store, row.Repo); err != nil {
			errs = append(errs, err)
			continue
		} else if disabled {
			reason := cancelReasonForCIMode(mode)
			deps.CI.Cancel(row.RunID, reason, ci.CancelBySystem)
			row.Conclusion = string(ci.ConclusionCancelled)
			row.Detail = "cancelled: " + reason
			row.FinishedAt = &now
			if err := deps.Store.PutBaseBranchRelease(ctx, row); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		// In flight at the start of this cycle, so nothing new is started
		// for the branch on it -- whatever the conclusion recorded here
		// turns out to be. The cycle after is soon enough, and it reads
		// the tip as it stands then.
		busy[dispatchKey(row.Repo, row.Branch)] = true
		if err := collectBaseBranchRelease(ctx, deps, row, now); err != nil {
			errs = append(errs, err)
		}
	}
	if err := startDueBaseBranchRelease(ctx, deps, busy, now); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// startDueBaseBranchRelease walks the base branches grain watches and
// starts one release, for the first branch that is owed one. Everything
// it skips it will find again on the next cycle, which is what makes
// "one per cycle" a delay rather than a loss.
//
// The branches are model.BaseBranchCI's rather than a repository's
// configuration, because those are the branches grain has merged into or
// been told to watch -- which is exactly the set a release used to
// follow when the release *was* that row's run. A repository grain does
// not judge publishes nothing from here, as it published nothing before.
//
// # Trunk only (docs/design/release-from-trunk.md, RFT-2)
//
// Of those, only the repository's default branch is released. A release
// is cut from trunk now: `candidate` is a pointer the release pipeline
// force-moves to a commit `main` already published, not a branch grain
// merges into and publishes from on its own, and a base-branch release of
// it would rebuild images the trunk run already built under a moving tag
// the pipeline owns. Rows for any other branch -- `candidate`'s above all,
// left behind by the stabilization-branch workflow -- are no longer
// followed; a release already in flight for one is still collected.
func startDueBaseBranchRelease(ctx context.Context, deps Deps, busy map[string]bool, now time.Time) error {
	rows, err := deps.Store.ListBaseBranchCI(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading base branch CI: %w", err)
	}
	var errs []error
	for _, row := range rows {
		if row.Branch == "" || busy[dispatchKey(row.Repo, row.Branch)] {
			continue
		}
		if !isTrunk(deps, row.Repo, row.Branch) {
			continue // see "Trunk only" above
		}
		if allowed, err := repoAllowedForAnyTeam(ctx, deps.Store, row.Repo); err != nil || !allowed {
			continue
		}
		wanted, err := baseBranchReleaseWanted(ctx, deps.Store, row.Repo)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !wanted {
			continue
		}
		started, err := startBaseBranchRelease(ctx, deps, row.Repo, row.Branch, now)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if started {
			return errors.Join(errs...) // one per cycle
		}
	}
	return errors.Join(errs...)
}

// baseBranchReleaseWanted reports whether a merge into this repository
// still publishes.
//
// The deployment's half of it is one question asked of one function:
// does this deployment run model.ReleaseItem for this repository
// (model.RunsWorkItem)? Release is an ordinary work item there rather
// than a switch of its own, so a deployment that hands this repository's
// releases to another deployment says so in the same place, and the same
// way, as it would for any of its schedules (grain/task-486).
//
// Since grain/task-461 that answer suppresses the trusted release event
// and nothing else. It does not touch the judging run any more
// (baseBranchCIWanted), which is what the old name always promised.
//
// Two other conditions ride with it, both unchanged from when the
// release and the verdict were one run: grain CI has to be enabled for
// the repository at all, and the deployment's own configuration
// repository is judged and never released -- grain publishes nothing out
// of it, and the workflow grain supplies for it answers `push` and
// `workflow_dispatch`, so asking under `release` would match no workflow
// and error once per merge (grain/task-396). Any other repository that
// declares no `release:` workflow is asked anyway -- what it declares is
// the tip's to say, and can change with any merge -- and its run's
// "no workflow answers" is recorded as nothing to publish rather than as
// a failed release (noteNothingToPublish, grain/task-4575).
func baseBranchReleaseWanted(ctx context.Context, store *model.Store, repo model.RepoRef) (bool, error) {
	if store == nil {
		return false, nil
	}
	if grainci.RequiredFor(repo) {
		return false, nil
	}
	cfg, err := store.GetRepoConfig(ctx, repo)
	if err != nil {
		return false, fmt.Errorf("orchestrator: reading release settings for %s: %w", repo, err)
	}
	if !model.RunsWorkItem(cfg, model.ReleaseItem) {
		return false, nil
	}
	return grainCIEnabledForRepo(ctx, store, repo)
}

// startBaseBranchRelease starts one branch's release if it is owed one,
// reporting whether it started anything.
//
// Owed is level-triggered and says nothing about merges: the branch's
// tip is not the commit the row was started for. A merge is the ordinary
// way that becomes true; a push straight to the branch is the other, and
// both want the same thing done about them.
func startBaseBranchRelease(ctx context.Context, deps Deps, repo model.RepoRef,
	branch string, now time.Time) (bool, error) {

	refusal, err := ReleaseBranchRefusal(ctx, deps.Store, deps.Client, repo, branch)
	if err != nil {
		return false, err
	}
	if refusal != "" {
		sayOnce("release-branch-refused:"+repo.String()+":"+branch,
			"orchestrator: not releasing %s %s, though grain has a base-branch row for it: %s", repo, branch, refusal)
		return false, nil
	}
	tip, err := branchTip(ctx, deps, repo, branch)
	if err != nil {
		return false, err
	}
	if tip == "" {
		// Nothing to compare against -- no GitHub client, or a branch
		// that has gone. Not "the branch has moved".
		return false, nil
	}
	row, err := deps.Store.GetBaseBranchRelease(ctx, repo, branch)
	if err != nil {
		return false, err
	}
	attempts := 1
	if row != nil && row.Commit == tip {
		// The tip has been released before. A release that published it
		// is the end of it; one that did not is retried once, and then
		// the tip waits for a newer merge or a hand-started release run
		// (grain/task-4432). The retry is what keeps one flake in a
		// twenty-minute release from leaving the branch's moving tag on
		// an older commit until somebody happens to merge again.
		if row.Conclusion == string(ci.ConclusionSuccess) {
			return false, nil // already published from this tree
		}
		if row.Conclusion == string(ci.ConclusionSkipped) {
			return false, nil // nothing to publish from this tree (noteNothingToPublish)
		}
		made := row.AttemptsMade()
		if made >= model.MaxReleaseAttempts {
			sayOnce("release-retries-spent:"+repo.String()+":"+branch+":"+tip,
				"orchestrator: %s %s is at %s, whose %d releases all ended without publishing (the last %s, run %s): "+
					"not retrying it again -- the next merge releases the branch, or a release run started by hand does",
				repo, branch, shortSHA(tip), made, row.Conclusion, row.RunID)
			return false, nil
		}
		attempts = made + 1
		log.Printf("orchestrator: %s %s is still at %s, whose release run %s ended %s: retrying it (attempt %d of %d)",
			repo, branch, shortSHA(tip), row.RunID, row.Conclusion, attempts, model.MaxReleaseAttempts)
	}
	// An open release triage task (fileBaseBranchReleaseTriage) holds
	// nothing here any more. It used to hold every release of the branch
	// while it was queued or running, and since one is filed and approved
	// the moment a release fails, that meant one failed release left the
	// branch's moving tag behind every merge after it until an agent
	// finished -- which is how main's image sat on f09c277 through four
	// merges on 2026-10-16 (grain/task-4432). The tip is released instead,
	// one run at a time per branch and at most MaxReleaseAttempts per
	// commit, which is the same bound a working branch runs under; and a
	// release that publishes still closes the triage.
	// The run of failures is about the branch rather than about the
	// attempt, so it survives the replace below: a streak that started
	// four merges ago is still four merges long while the fifth release
	// is in flight. Only a success clears it (collectBaseBranchRelease).
	var failures int
	var failingSince *time.Time
	if row != nil {
		failures, failingSince = row.Failures, row.FailingSince
	}
	run, err := deps.CI.Start(ctx, baseBranchReleaseRequest(repo, branch, tip))
	if err != nil {
		// Logged and swallowed rather than returned as this cycle's
		// error, startBaseBranchDispatch's rule: a repository whose
		// release cannot be planned -- no workflow answering `release`
		// is the likely one -- must not make the reconciler that has
		// just collected every other branch's run report a failure. The
		// row is left alone, so the next cycle asks again.
		log.Printf("orchestrator: releasing %s %s at %s: %v", repo, branch, shortSHA(tip), err)
		return false, nil
	}
	started := run.StartedAt
	if started == nil {
		started = &now
	}
	if err := deps.Store.PutBaseBranchRelease(ctx, model.BaseBranchRelease{
		Repo: repo, Branch: branch, Commit: tip,
		RunID: run.ID, StartedAt: *started,
		Failures: failures, FailingSince: failingSince,
		Attempts: attempts,
	}); err != nil {
		return false, err
	}
	log.Printf("orchestrator: %s %s is at %s: run %s is publishing it, and nothing is gated on "+
		"that -- what judges this tree is the push run on the branch's own row",
		repo, branch, shortSHA(tip), run.ID)
	return true, nil
}

// ReleaseBranchRefusal is why a trusted release of repo may not run from
// branch -- empty when it may (grain/task-949).
//
// A release run reads its workflows at the branch and is handed the
// repository's release secrets and publishing capabilities, so it may
// run only from a branch whose every commit has been reviewed in: the
// repository's default branch, one of its paired branches, or a branch
// of one of its open releases (model.Store.ReleaseBranches). Never a
// task's branch (model.IsTaskBranch), whatever row or configuration
// names it -- that is code its author controls. Having a BaseBranchCI
// row is not enough on its own: a stacked review merging into the
// reviewed task's branch used to write one.
//
// The default branch is read from client only when the branch is not
// already one of the others, so a cycle walking a paired repository's
// rows asks GitHub nothing new. A nil client leaves the default branch
// out, which refuses rather than trusts.
func ReleaseBranchRefusal(ctx context.Context, store *model.Store, client github.Client,
	repo model.RepoRef, branch string) (string, error) {

	if store == nil {
		return "", nil
	}
	if isTask, err := store.IsTaskBranch(ctx, branch); err != nil {
		return "", err
	} else if isTask {
		return branch + " is a task's branch, whose workflows its author controls", nil
	}
	allowed, err := store.ReleaseBranches(ctx, repo, "")
	if err != nil {
		return "", err
	}
	if slices.Contains(allowed, branch) {
		return "", nil
	}
	if client != nil {
		def, err := client.DefaultBranch(repo.Owner, repo.Name)
		if err != nil {
			return "", fmt.Errorf("orchestrator: reading %s's default branch: %w", repo, err)
		}
		if def != "" && def == branch {
			return "", nil
		}
		if def != "" {
			allowed = append([]string{def}, allowed...)
		}
	}
	if len(allowed) == 0 {
		return branch + " is not the default branch, a paired branch or an open release's branch", nil
	}
	return fmt.Sprintf("%s is not the default branch, a paired branch or an open release's branch (%s)",
		branch, strings.Join(allowed, ", ")), nil
}

// baseBranchReleaseRequest is the release run of repo's branch at commit.
//
// The trusted release event is the whole point of this run: it is what
// places the repository's release secrets and its publishing capabilities
// (ci.Service.Start), and the one thing that changed in grain/task-461 is
// that its conclusion is no longer a verdict about the tree. It is a trunk
// release gate (startDueBaseBranchRelease releases nothing else), so it
// asks for ci.PriorityReleaseGate.
func baseBranchReleaseRequest(repo model.RepoRef, branch, commit string) ci.Request {
	return ci.Request{
		Repo: repo.String(), Ref: branch, Commit: commit,
		Event: ci.EventRelease, TrustedRelease: true,
		RequestedBy: baseBranchReleaseRequester(repo, branch),
		Priority:    ci.PriorityReleaseGate,
	}
}

func baseBranchReleaseRequester(repo model.RepoRef, branch string) string {
	return "release:" + repo.String() + ":" + branch
}

// BaseReleaseTriager is the automation principal written to Origin and Approval of a
// base branch release triage task.
var BaseReleaseTriager = model.Principal{Kind: model.PrincipalAutomation, ID: "release-triage"}

// BaseReleaseTriageTag is the idempotency marker a base-branch release triage carries.
func BaseReleaseTriageTag(repo model.RepoRef, branch, commit string) string {
	return fmt.Sprintf("base-release-triage:%s:%s:%s", repo, branch, commit)
}

// BaseReleaseTriageBranchTag is the branch-scoped tag every base-branch release triage carries.
// Scoped to the branch so at most one active release triage task exists per base branch.
func BaseReleaseTriageBranchTag(repo model.RepoRef, branch string) string {
	return fmt.Sprintf("base-release-triage:%s:%s", repo, branch)
}

// collectBaseBranchRelease reads back a release that had not finished
// and records how it ended.
//
// A run the service no longer has -- pruned, or lost with the daemon
// that was running it -- is forgotten (forgetLostBaseBranchRelease)
// rather than left in flight forever, which would leave a branch grain
// believes it is still publishing and nothing would ever be published
// there again.
//
// Recording is all that happens to a failure with respect to the merge queue:
// the merge queue does not read this row, and a release that failed is
// images that did not ship rather than a broken tree. When a release run
// concludes with a non-success verdict, an automated triage task is filed
// to investigate CI logs and repair the release pipeline.
func collectBaseBranchRelease(ctx context.Context, deps Deps, row model.BaseBranchRelease,
	now time.Time) error {

	run, ok := deps.CI.Get(row.RunID)
	if !ok || run.Interrupted {
		return forgetLostBaseBranchRelease(ctx, deps, row, baseCILostDetail(ok))
	}
	if !run.Conclusion.Done() {
		return nil // still going
	}
	finished := run.FinishedAt
	if finished == nil {
		finished = &now
	}
	row.FinishedAt = finished
	if ci.NoWorkflowAnswered(run) {
		return noteNothingToPublish(ctx, deps, row)
	}
	if retried, err := retryInfraBaseBranchRelease(ctx, deps, &row, run, now); err != nil || retried {
		return err
	}
	row.Conclusion, row.Detail = string(run.Conclusion), ciDetail(run)
	noteBaseBranchReleaseOutcome(&row, run.Conclusion)
	if err := deps.Store.PutBaseBranchRelease(ctx, row); err != nil {
		return err
	}
	if run.Conclusion != ci.ConclusionSuccess {
		log.Printf("orchestrator: the release of %s %s at %s ended %s (%s): nothing is held by "+
			"that -- the merge queue is gated on the branch's own CI run and never on this one, "+
			"and the next merge asks again. %s",
			row.Repo, row.Branch, shortSHA(row.Commit), run.Conclusion, row.Detail,
			baseBranchReleaseStreak(row))
		return fileBaseBranchReleaseTriage(ctx, deps, row, now)
	}
	// A release that published is the end of every triage of this
	// branch's releases, however it came about: close them as superseded,
	// as a green base-branch run closes its repairs (baserepairlink.go).
	ids, err := deps.Store.OpenTasksWithTag(ctx, BaseReleaseTriageBranchTag(row.Repo, row.Branch))
	if err != nil {
		return err
	}
	return closeSupersededTasks(ctx, deps.Store, ids, BaseReleaseTriager, fmt.Sprintf(
		"Closed as superseded: release run %s published `%s` on %s at %s, so its releases are "+
			"working again and there is nothing left for this task to triage.",
		row.RunID, row.Branch, row.Repo, shortSHA(row.Commit)), now)
}

// retryInfraBaseBranchRelease is what happens to a release run that ended
// with an `infra` verdict (infraVerdict) -- cancelled, errored, or failed
// only on a credential, quota or registry limit -- before it is counted
// as a failed release or files triage: it is re-run, the jobs that did not
// pass and only them, uncached, up to maxInfraRetries times and
// infraRetryDelay apart (docs/design/release-from-trunk.md, RFT-2). The
// count is on the run (infraRetriesOf), so the row needs no column for it.
//
// It reports whether it has dealt with the run: true while the re-run is
// waiting out its delay, once one is started (the row names it and is in
// flight again), and for an infra verdict about a commit the branch has
// moved past, which is recorded with no triage and no failure counted --
// the newer tip's release is the answer now, and the next cycle starts it.
// false hands the run back to be recorded as the verdict it is: not an
// infra one, its re-runs spent, or a re-run grain could not start.
func retryInfraBaseBranchRelease(ctx context.Context, deps Deps, row *model.BaseBranchRelease,
	run ci.Run, now time.Time) (bool, error) {

	why, infra := infraVerdict(run)
	if !infra {
		return false, nil
	}
	n := infraRetriesOf(run.RequestedBy)
	if n >= maxInfraRetries {
		return false, nil
	}
	tip, err := branchTip(ctx, deps, row.Repo, row.Branch)
	if err != nil {
		return false, err
	}
	if tip != "" && tip != row.Commit {
		row.Conclusion, row.Detail = string(run.Conclusion), ciDetail(run)
		log.Printf("orchestrator: the release of %s %s at %s ended without a verdict (%s), and the branch "+
			"has moved to %s since: not re-running it, and not counting it as a failed release",
			row.Repo, row.Branch, shortSHA(row.Commit), why, shortSHA(tip))
		return true, deps.Store.PutBaseBranchRelease(ctx, *row)
	}
	// Measured from the run's own end, never from the row's FinishedAt,
	// which collection fills with "now" for a run that recorded none and
	// would then never come due.
	ended := row.StartedAt
	switch {
	case run.FinishedAt != nil:
		ended = *run.FinishedAt
	case run.StartedAt != nil:
		ended = *run.StartedAt
	}
	if now.Before(ended.Add(infraRetryDelay)) {
		return true, nil // waiting out the delay; the row stays in flight
	}
	retry, err := startInfraRetry(ctx, deps, baseBranchReleaseRequest(row.Repo, row.Branch, row.Commit),
		run, baseBranchReleaseRequester(row.Repo, row.Branch), n+1, why)
	if err != nil {
		log.Printf("orchestrator: %v -- recording the release as it ended", err)
		return false, nil
	}
	// StartedAt stays the first run's: it is when grain set out to publish
	// this commit, which is what a run of failures is measured from.
	row.RunID, row.FinishedAt = retry.ID, nil
	return true, deps.Store.PutBaseBranchRelease(ctx, *row)
}

// noteNothingToPublish records a release whose run no workflow of the
// repository answered: a repository that publishes nothing, which is
// every repository on grain CI until it declares a `release:` trigger
// (grain/task-4575). grain CI concludes a push that no workflow answers
// errored -- it tested nothing -- but for a release it is the whole
// answer: there was nothing to publish, and nothing failed. grain CI
// now concludes such a release run skipped itself
// (ci.Plan.NothingToPublish); one stored before that is errored, and
// ci.NoWorkflowAnswered recognises both.
//
// So it is recorded as skipped rather than as a failure: no triage task,
// no retry of the tip (startBaseBranchRelease reads a skipped tip as
// done, as it does a published one), and no failure counted toward the
// streak ui.BaseBranchPublishing warns about -- the streak is cleared
// instead, because a repository that has stopped declaring a release has
// not stopped publishing by accident. The branch's next merge asks again,
// which is what picks up a `release:` workflow the day one is added.
func noteNothingToPublish(ctx context.Context, deps Deps, row model.BaseBranchRelease) error {
	row.Conclusion = string(ci.ConclusionSkipped)
	row.Detail = "no workflow of the repository answers `release` at this commit, so there was nothing to publish"
	row.Failures, row.FailingSince = 0, nil
	if err := deps.Store.PutBaseBranchRelease(ctx, row); err != nil {
		return err
	}
	sayOnce("release-unanswered:"+row.Repo.String()+":"+row.Branch,
		"orchestrator: %s declares no workflow answering `release`, so merges into %s publish nothing "+
			"and no release is owed: not counting that as a failed release",
		row.Repo, row.Branch)
	return nil
}

// forgetLostBaseBranchRelease clears a release whose run never concluded
// on its own -- interrupted by a daemon restart, or no longer known to
// this daemon -- so that the next cycle starts one again against the tip
// as it then stands.
//
// It is not a failure of the release pipeline: nothing about the
// workflow or the tree went wrong, the process running it went away. So
// it files no triage task (which would send an agent to repair a
// pipeline that never failed, and hold the branch's releases until it
// closed), and it does not move the run of failures on: the streak
// carried on the row is kept exactly as it was. The judging run is
// treated the same way when it is lost (ci.go: "forgetting it so the
// required run ... can be started again").
//
// The row stays, so the streak survives, but with no RunID and no
// Commit: not in flight, so the branch is not busy, and not the tip, so
// startBaseBranchRelease reads the branch as owed a release even when
// nothing has merged since.
func forgetLostBaseBranchRelease(ctx context.Context, deps Deps, row model.BaseBranchRelease,
	detail string) error {

	log.Printf("orchestrator: the release of %s %s at %s (run %s) never concluded: %s; "+
		"forgetting it so the next cycle releases the branch again, and counting it as no failure",
		row.Repo, row.Branch, shortSHA(row.Commit), row.RunID, detail)
	row.Commit, row.RunID = "", ""
	row.Conclusion, row.Detail, row.FinishedAt = "", "", nil
	return deps.Store.PutBaseBranchRelease(ctx, row)
}

// noteBaseBranchReleaseOutcome moves the branch's run of failures on by
// this release (model.BaseBranchRelease.Failures): a success ends it, and
// anything else is another merge that published nothing.
//
// It is bookkeeping for a reader and nothing else. What it writes reaches
// ui.BaseBranchPublishing, which says "nothing has published from `main`
// since Tuesday" on the deployment health reading and on the repository's
// own release pane (grain/task-511); it reaches no gate, starts no run
// and files no task, because the row it is on is one the merge queue has
// never heard of and this file's whole point is that it stays that way.
func noteBaseBranchReleaseOutcome(row *model.BaseBranchRelease, conclusion ci.Conclusion) {
	if conclusion == ci.ConclusionSuccess {
		row.Failures, row.FailingSince = 0, nil
		return
	}
	row.Failures++
	if row.FailingSince == nil {
		// The start of the run that failed, rather than the moment it gave
		// up: what a reader is asking is how long this branch has been
		// shipping nothing, and that is measured from the merge grain last
		// tried to publish and could not.
		at := row.StartedAt
		row.FailingSince = &at
	}
}

// baseBranchReleaseStreak is the one clause the log line about a failed
// release could not say before: whether this is the first one or the
// twentieth. A single failure is noise and a fortnight of them is a
// repository that has quietly stopped shipping, and the difference was
// invisible in a log of identical lines.
func baseBranchReleaseStreak(row model.BaseBranchRelease) string {
	if row.Failures <= 1 || row.FailingSince == nil {
		return "That is the first release to fail here since the last one that published."
	}
	return fmt.Sprintf("%d releases in a row have published nothing from this branch, "+
		"the first of them started %s.", row.Failures, row.FailingSince.UTC().Format(time.RFC3339))
}

// fileBaseBranchReleaseTriage files a triage task when a base branch release fails.
func fileBaseBranchReleaseTriage(ctx context.Context, deps Deps, row model.BaseBranchRelease,
	now time.Time) error {

	// No triage for a repository whose triage this deployment does not
	// run (model.TriageItem, grain/task-3332). A release that failed
	// under a release override still says so in the log and on its row.
	if runs, err := runsStandingWork(ctx, deps.Store, row.Repo, model.TriageItem); err != nil {
		return err
	} else if !runs {
		sayOnce("release-triage-off:"+row.Repo.String(), "orchestrator: not filing release triage for %s %s: %s",
			row.Repo, row.Branch, model.StandingWorkOffReason(row.Repo))
		return nil
	}
	tag := BaseReleaseTriageTag(row.Repo, row.Branch, row.Commit)
	filed, err := deps.Store.HasTaskWithTag(ctx, tag)
	if err != nil {
		return fmt.Errorf("orchestrator: checking for an existing release triage of %s %s: %w",
			row.Repo, row.Branch, err)
	}
	if filed {
		return nil
	}

	branchTag := BaseReleaseTriageBranchTag(row.Repo, row.Branch)
	existing, err := deps.Store.OpenTaskWithTag(ctx, branchTag)
	if err != nil {
		return fmt.Errorf("orchestrator: checking for open release triage of %s %s: %w", row.Repo, row.Branch, err)
	}
	if existing != nil {
		commentBody := fmt.Sprintf("A subsequent commit on `%s` (`%s`) has also failed the base branch release workflow:\n\n"+
			"- Commit: `%s`\n"+
			"- Run ID: `%s`\n"+
			"- Conclusion: `%s`\n",
			row.Branch, row.Repo, shortSHA(row.Commit), row.RunID, row.Conclusion)
		if row.Detail != "" {
			commentBody += fmt.Sprintf("- Detail: %s\n", row.Detail)
		}
		if _, err := deps.Store.AddComment(ctx, model.Comment{
			TaskID:    existing.ID,
			Author:    model.Attribution{Actor: BaseReleaseTriager},
			Body:      commentBody,
			CreatedAt: now,
		}); err != nil {
			return fmt.Errorf("orchestrator: updating existing release triage task %s: %w", existing.ID, err)
		}
		log.Printf("orchestrator: appended comment to active release triage task %s for failed release on %s %s at %s (%s)",
			existing.ID, row.Repo, row.Branch, shortSHA(row.Commit), row.Conclusion)
		return nil
	}
	id, err := deps.Store.NewTaskID(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: allocating an id for the release triage of %s %s: %w",
			row.Repo, row.Branch, err)
	}
	repo := row.Repo
	grants, err := defaultTaskGrants(ctx, deps.Store, &repo, "")
	if err != nil {
		return fmt.Errorf("orchestrator: resolving default capabilities for release triage of %s %s: %w", repo, row.Branch, err)
	}

	tmpl, err := resolveRepairTemplate(ctx, deps, repo, "")
	if err != nil {
		// Nothing is written yet (the id allocated above is only a number),
		// and the tag check above makes the next cycle try again.
		return fmt.Errorf("orchestrator: resolving the repair template for %s %s: %w",
			repo, row.Branch, err)
	}
	values := map[string]string{
		"team":              "",
		"repo":              repo.String(),
		"branch":            row.Branch,
		"commit":            shortSHA(row.Commit),
		"detail":            row.Detail,
		"confirm_detail":    "",
		"merged_task_id":    "",
		"merged_task_title": "",
	}

	title := fmt.Sprintf("Fix base branch release on %s %s", repo, row.Branch)
	body := baseBranchReleaseTriageBody(row)
	var customFields map[string][]string
	var networkAccess model.NetworkAccess
	var chosenModel string
	autoMerge := true
	var reads []model.RepoRef
	var writes []model.WriteTarget

	if tmpl != nil {
		filled := tmpl.FieldValues(values)
		if filled == nil {
			filled = make(map[string]string)
		}
		for k, v := range values {
			if _, ok := filled[k]; !ok {
				filled[k] = v
			}
		}
		if t := strings.TrimSpace(model.SubstituteFields(tmpl.Title, filled)); t != "" {
			title = t
		}
		if b := strings.TrimSpace(model.SubstituteFields(tmpl.Body, filled)); b != "" {
			body = b
		}
		customFields, err = deps.Store.FiringCustomFields(ctx, tmpl.CustomFields)
		if err != nil {
			return fmt.Errorf("orchestrator: resolving the repair template's custom fields for %s %s: %w",
				repo, row.Branch, err)
		}
		networkAccess = tmpl.NetworkAccess
		chosenModel = tmpl.Model
		autoMerge = tmpl.AutoMerge
		reads = slices.Clone(tmpl.Reads)
		writes = model.WritesBesides(tmpl.Writes, repo, row.Branch)
		for _, g := range tmpl.Grants {
			if !slices.ContainsFunc(grants, func(existing model.Grant) bool {
				return existing.Capability == g.Capability
			}) {
				grants = append(grants, g)
			}
		}
	}

	if !grainci.Granted(grants) {
		grants = append(grants, model.Grant{
			Capability: grainci.CapabilityName,
			Via:        model.GrantByGrain,
		})
	}
	orderKey, err := deps.Store.OrderKeyForNewTask(ctx, true)
	if err != nil {
		return fmt.Errorf("orchestrator: allocating order key for release triage of %s %s: %w", repo, row.Branch, err)
	}
	triage := model.Task{
		ID:     id,
		Intent: model.IntentImplement,
		Title:  title,
		Body:   body,
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: BaseReleaseTriager},
			Reason:      model.ReasonDirect,
		},
		Approval:      &model.Attribution{Actor: BaseReleaseTriager},
		ApprovedAt:    &now,
		Target:        &repo,
		Binding:       model.BindingDirective,
		Base:          row.Branch,
		AutoMerge:     autoMerge,
		Tags:          []string{tag, branchTag},
		OrderKey:      orderKey,
		Reads:         reads,
		Writes:        writes,
		Grants:        grants,
		CustomFields:  customFields,
		NetworkAccess: networkAccess,
		Model:         chosenModel,
		ModelSource:   model.ModelSourceOf(chosenModel, model.LayerTemplate),
		CreatedAt:     &now,
	}
	if err := deps.Store.PutTask(ctx, triage); err != nil {
		return fmt.Errorf("orchestrator: filing the release triage of %s %s: %w", repo, row.Branch, err)
	}
	log.Printf("orchestrator: filed %s to triage failed release on %s %s at %s (%s)",
		triage.ID, repo, row.Branch, shortSHA(row.Commit), row.Conclusion)
	return nil
}

func baseBranchReleaseTriageBody(row model.BaseBranchRelease) string {
	var b strings.Builder
	b.WriteString("The base branch release workflow (`.grain/workflows/release.yml`) failed on ")
	fmt.Fprintf(&b, "`%s` (branch `%s`) at commit `%s`.\n\n", row.Repo, row.Branch, shortSHA(row.Commit))
	b.WriteString("Release run details:\n")
	fmt.Fprintf(&b, "- Run ID: `%s`\n", row.RunID)
	fmt.Fprintf(&b, "- Conclusion: `%s`\n", row.Conclusion)
	if row.Detail != "" {
		fmt.Fprintf(&b, "- Detail: %s\n", row.Detail)
	}
	b.WriteString("\nWhat to do:\n\n")
	b.WriteString("- Inspect the failed CI run logs using `get_ci_log` and `list_ci_runs`.\n")
	b.WriteString("- Identify the cause of the failure in the release workflow or codebase.\n")
	b.WriteString("- Repair the release pipeline or repository files so releases succeed.\n")
	b.WriteString("\n" + model.RedCIProcedureReminder + "\n")
	return b.String()
}

func isGitHubReleaseWorkflow(name, wfPath, event string) bool {
	if strings.EqualFold(strings.TrimSpace(event), "release") {
		return true
	}
	for _, cand := range []string{name, path.Base(wfPath)} {
		s := strings.ToLower(strings.TrimSpace(cand))
		s = strings.TrimSuffix(strings.TrimSuffix(s, ".yml"), ".yaml")
		if s == "release" || s == "publish" ||
			strings.HasPrefix(s, "release-") || strings.HasSuffix(s, "-release") ||
			strings.HasPrefix(s, "publish-") || strings.HasSuffix(s, "-publish") {
			return true
		}
	}
	return false
}

func isGitHubBaseBranch(deps Deps, repo model.RepoRef, branch string) bool {
	branch = strings.TrimSpace(strings.TrimPrefix(branch, "refs/heads/"))
	if branch == "" || deps.Client == nil {
		return false
	}
	return isTrunk(deps, repo, branch)
}

// recordGitHubBaseBranchRelease updates BaseBranchRelease state when a
// GitHub Actions release/publish workflow concludes on a base branch, raising
// pipeline.not-publishing notifications and filing/closing release-triage
// tasks on parity with grain CI release workflows.
func recordGitHubBaseBranchRelease(ctx context.Context, deps Deps, run ConcludedGitHubWorkflowRun, jobs []model.GitHubCIJob, now time.Time) error {
	if deps.Store == nil || run.RunID == 0 {
		return nil
	}
	if isGitHubWorkflowRunForeignHead(run.Repo, run.HeadRepo) || !isGitHubBranchWorkflowEvent(run.Event) {
		return nil
	}
	if !isGitHubReleaseWorkflow(run.WorkflowName, run.WorkflowPath, run.Event) {
		return nil
	}
	branch := strings.TrimPrefix(run.Ref, "refs/heads/")
	if !isGitHubBaseBranch(deps, run.Repo, branch) {
		return nil
	}

	existing, err := deps.Store.GetBaseBranchRelease(ctx, run.Repo, branch)
	if err != nil {
		return err
	}
	row := model.BaseBranchRelease{
		Repo:   run.Repo,
		Branch: branch,
	}
	if existing != nil {
		row = *existing
	}

	conclusion := gitHubConclusionToCI(run.Conclusion)
	started := now
	if run.StartedAt != nil && !run.StartedAt.IsZero() {
		started = *run.StartedAt
	}
	finished := now
	if run.CompletedAt != nil && !run.CompletedAt.IsZero() {
		finished = *run.CompletedAt
	}

	row.Commit = run.HeadSHA
	row.RunID = strconv.FormatInt(run.RunID, 10)
	row.Attempts = max(1, run.Attempt)
	row.StartedAt = started
	row.FinishedAt = &finished
	row.Conclusion = string(conclusion)
	row.Detail = gitHubWorkflowRunDetail(run, jobs)
	noteBaseBranchReleaseOutcome(&row, conclusion)

	if err := deps.Store.PutBaseBranchRelease(ctx, row); err != nil {
		return err
	}

	switch conclusion {
	case ci.ConclusionSuccess:
		ids, err := deps.Store.OpenTasksWithTag(ctx, BaseReleaseTriageBranchTag(run.Repo, branch))
		if err != nil {
			return err
		}
		return closeSupersededTasks(ctx, deps.Store, ids, BaseReleaseTriager, fmt.Sprintf(
			"Closed as superseded: GitHub Actions release workflow `%s` published `%s` on %s at %s (run `%s`), so its releases are working again and there is nothing left for this task to triage.",
			run.WorkflowName, branch, run.Repo, shortSHA(row.Commit), row.RunID), now)
	case ci.ConclusionFailure:
		if err := fileBaseBranchReleaseTriage(ctx, deps, row, now); err != nil {
			return err
		}
		// Attach the GitHub workflow run summary and failure tails to the open
		// release-triage task so the triage agent has the run URL, failing
		// job/step, and failure tail immediately.
		summarySection := gitHubWorkflowRunSummarySection(run, jobs)
		commitTag := BaseReleaseTriageTag(run.Repo, branch, row.Commit)
		if t, err := deps.Store.OpenTaskWithTag(ctx, commitTag); err == nil && t != nil {
			if !strings.Contains(t.Body, summarySection) {
				t.Body = strings.TrimRight(t.Body, "\n") + "\n\n" + summarySection
				_ = deps.Store.PutTask(ctx, *t)
			}
		}
	}
	return nil
}
