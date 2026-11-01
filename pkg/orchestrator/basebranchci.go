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
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// The merge queue's answer to the one thing it gives up by not requiring
// a branch to be up to date before it merges
// (model.RepoConfig.RequireBranchUpToDate, grain/task-198).
//
// Two pull requests can each be green on their own head and broken
// together: one renames a function, the other adds a caller of it, and no
// verdict about either branch alone says so. Requiring every branch to
// carry its base is what buys protection from that, and it costs a full
// suite per open branch per merge -- N² suites for N branches, which on
// this deployment was four twenty-minute suites for one task in ninety
// minutes while the CI host had no sandboxes left.
//
// So the property is bought the other way round, after the fact rather
// than before it. grain runs its own CI on a base branch whenever that
// branch's tip is a commit its recorded verdict is not about -- however
// the branch got there, a merge of grain's own included (grain/task-320),
// and one run at a time (grain/task-334). This is what it does about the
// answer:
//
//   - a base branch whose post-merge run failed says so, on its own row
//     and on the row of every change queued for it (mergeStateBaseBroken),
//     rather than sitting silently;
//   - and that failure earns a repair, naming the merge that broke it, the
//     way a red branch has always earned one.
//
// Without both halves this would trade a slow queue for a broken `main`
// nobody is driving, which is the same hole grain/task-170 closes
// elsewhere.
//
// The first half used to be a *hold*: a red row stopped the queue merging
// anything further into that branch. It is not, since grain/task-471 --
// see mergepredicate.go, and brokenBaseBranch further down this file for
// what the verdict is read for now. Comments below that still weighed
// "does this failure deserve the hold?" have been restated against what
// they are really weighing, which is whether it deserves an agent
// (grain/task-424).
//
// # What counts as red (grain/task-202, restated by 424)
//
// The two halves above are not the same price. Saying a branch is red is
// cheap and undone by one write; filing a repair spends an agent run and
// puts a task in front of a person. A deployment with a flaky job has
// reds that are not worth the second of those, and the control would
// spend an agent on every one of them.
//
// So they are decided separately:
//
//   - what the rows *say* changes on the first failure, immediately. A
//     branch that may be broken is worth saying so about, and the cost of
//     being wrong is a sentence that is retracted a few minutes later;
//   - the *repair* waits for a second, independently-run sample of the
//     jobs that failed (startBaseCIConfirmation). Agreement confirms the
//     branch is red; a pass files nothing and the sentence goes away.
//
// That second run has to be asked for rather than simply started: grain
// CI content-addresses a job's verdict per commit, so re-running the
// same commit replays the cached failure in seconds and confirms
// anything. ci.Request.Recheck exists for this caller.
//
// The alternative considered and not taken was a per-repository list of
// jobs whose failure is not acted on. It is cheaper -- no second run at
// all -- but it is a standing decision to ignore a job's verdict, made
// once and then forgotten, and a real regression in the listed job
// reaches `main` unremarked. A confirmation costs one partial suite and
// only when a branch is red, and it distinguishes *this* failure of a
// flaky job from the next one, which is the distinction that matters.
//
// grain/task-424 was filed to ask that same question about one particular
// kind of job -- a publish, whose failure means "this commit did not
// reach the registry" and not "the tree is broken" -- and found the
// question had been dissolved rather than answered. Two things had to be
// true for a broken publish to cost a repository its queue: the publish
// had to be *in* the judging run, and the judging run had to hold. Since
// grain/task-461 the publish is a run of its own on a row the queue does
// not read (basebranchrelease.go), and since grain/task-471 nothing
// holds. So no list of exempt jobs is needed, and grain never has to do
// the thing both this comment and model.RepoConfig.BaseBranchCIJobs
// refused to do -- tell a publishing job from a judging one by reading a
// workflow file it does not control. The separation is by run, where the
// event already draws it. docs/merge-queue-behind.md, "Should a failing
// publish hold the queue?", has the shapes that were weighed.

// # A branch grain has never merged into (grain/task-331)
//
// The tip-watching above watches the branches there are rows for, and
// until this a row was only ever created by grain performing a merge.
// So a repository whose pull requests are all merged by hand -- or one
// grain has not merged into yet -- was watched by nothing at all, and
// its `main` could be red with the queue merging onto it exactly as
// before the control existed.
//
// seedWatchedBaseBranches closes that from the repository's end rather
// than from a merge's: it resolves the default branch of each repository
// an operator has ticked model.RepoConfig.WatchDefaultBranch on and
// enrols it, writing the same runless row a first merge writes
// (noteBaseBranchMerge). The reconciler starts the run from there, so a
// seeded branch is an ordinary tracked row from the moment it exists and
// everything above applies to it -- including the one-run-per-branch
// bound grain/task-334 put on these.
//
// Two things about it are deliberately conservative, both because what
// this spends is a full suite:
//
//   - it is off until a repository is ticked. Seeding every repository
//     grain CI is enabled for would start a suite for each of them on
//     the first cycle after an upgrade, and file a repair for every one
//     that turned out to be red -- a bill, and a pile of tasks, arriving
//     with a deployment upgrade rather than with anybody's decision. The
//     set of repositories with a config row is
//     also not the set whose `main` anybody wants suites spent on;
//   - and it enrols one branch per cycle, and none at all while any base
//     branch grain watches has a run in flight anywhere
//     (baseBranchCIBusy). An operator who ticks six repositories at once
//     therefore pays for them one suite after another rather than six at
//     once, which is the difference between this control and an outage on
//     a CI host that has been over its sandbox budget twice in a week.
//     The per-branch bound grain/task-334 added does not cover this: six
//     branches judged once each are six runs it is perfectly happy with.
//
// The cost of that second rule is latency: six repositories are watched
// after six suites rather than after one cycle. Nothing merges onto an
// unwatched branch any *less* safely in the meantime than it did before
// this existed, so latency is the right thing to trade here.

// baseCIRepairer is the automation principal a base-branch repair is
// attributed to, beside "backport", "schedule" and "qualification": the
// task's origin says which of grain's mechanisms filed it.
var baseCIRepairer = model.Principal{Kind: model.PrincipalAutomation, ID: "merge-queue"}

// baseCIRepairTag is the idempotency marker a base-branch repair carries.
// Scoped to the exact commit that went red: a second failure of the same
// branch at a later commit is a different breakage and deserves its own
// task, while the same commit read again on the next cycle must not.
func baseCIRepairTag(repo model.RepoRef, branch, commit string) string {
	return model.GrainBaseRepairCommitTag(repo, branch, commit)
}

// BaseCIRepairBranchTag is the branch-scoped tag every base-branch repair carries: what
// work waiting on the branch finds its repair task by (baserepairlink.go).
// Scoped to the branch so at most one active repair task exists per base branch.
// A task a person marked as fixing the branch carries it too
// (model.BaseRepairBranchTag, grain/task-3451).
func BaseCIRepairBranchTag(repo model.RepoRef, branch string) string {
	return model.BaseRepairBranchTag(repo, branch)
}

// noteBaseBranchMerge writes down that a merge has moved a base branch,
// which is all a merge does now (recordPostMergeCI, grain/task-334).
// Starting the run is reconcileBaseBranchCI's, and this is what tells it
// there is something to start: a branch it has never seen gets a row, and
// a branch it has gets the merging task recorded as the one whose merge
// the next run there is the answer to.
//
// Nothing else on the row is touched, and that is the point. A run in
// flight keeps its own Commit, RunID and MergedTaskID -- it is judging a
// tree that merge is not in, and a failure of it must go on naming the
// merge it *is* about -- while the branch's tip moves out from under it.
// The reconciler compares the two and starts one run when that run ends.
func noteBaseBranchMerge(ctx context.Context, store *model.Store, repo model.RepoRef,
	branch, commit, taskID string, now time.Time) error {

	row, err := store.GetBaseBranchCI(ctx, repo, branch)
	if err != nil {
		return fmt.Errorf("orchestrator: reading base branch CI for %s %s: %w", repo, branch, err)
	}
	if row == nil {
		// The first merge grain has watched into this branch. The row
		// names no run and holds no verdict, which reads as "nothing in
		// flight, nothing judged": the next cycle starts the first run
		// against whatever the tip is then. Its existence is also the
		// branch's enrolment -- reconcileBaseBranchCI walks the rows, so
		// until there is one nothing looks at this branch at all.
		return store.PutBaseBranchCI(ctx, model.BaseBranchCI{
			Repo: repo, Branch: branch, StartedAt: now, PendingTaskID: taskID,
			PendingMerges: 1,
		})
	}
	if row.Commit == commit {
		// The tip this merge produced is already the one the row is
		// about: a merge observed twice, or a run the reconciler started
		// after it. Nothing to record, and overwriting PendingTaskID here
		// would hand this task's name to the *next* run as well --
		// PendingMerges below would count the same merge twice for the
		// same reason.
		return nil
	}
	row.PendingTaskID = taskID
	// And one more merge for whichever run judges this tip. It is counted
	// here rather than inferred from the commits between two runs because
	// this is the only place that knows a merge was grain's: how many
	// merges a post-merge run covers is what the merge queue's batch
	// arithmetic turns on (model.BaseBranchCI.Covers).
	row.PendingMerges++
	return store.PutBaseBranchCI(ctx, *row)
}

// reconcileBaseBranchCI collects the verdict of each base branch's own
// post-merge run and acts on it: nothing at all while it is still going
// or once it has passed, and a repair the first time one fails.
//
// Level-triggered like every reconciler here, and idempotent on both
// sides of the write: the verdict is recorded on the row before the
// repair is filed, and the repair carries a tag naming the exact commit,
// so a daemon restarted between the two files one task rather than two or
// none.
//
// # The tip, not the merge (grain/task-320)
//
// What this asks every cycle is whether the recorded verdict is about the
// commit the branch is *on*, and it asks that whatever the verdict says.
// A row about a commit the branch has moved past is an answer about a
// tree that has gone, so a fresh run is started against the tip and
// replaces the row -- for a failure somebody may have pushed a fix over,
// and equally for a green, which is the case that was missing.
//
// Missing, and it mattered: until this, a base branch was only ever
// tested by the merge path, on the one cycle the pull-request
// reconciler observed grain's own merge of a task's pull request. Every
// other way a base branch moves -- a pull request merged by hand to
// unblock the queue, a push straight to `main`, a pull request nobody
// filed as a grain task, a merge that landed after grain had stopped
// watching the task -- moved it silently, and the row went on describing
// a commit several merges back. gke-labs/kontur took five commits on
// `main` that way with no run against any of them, which is exactly the
// interval in which the control was believed to be working.
//
// So the trigger is the branch's own tip rather than an event grain
// happened to be the author of. Who moved the branch decides nothing
// here; that the tree under it is untested decides everything, because an
// untested `main` is what the queue merges onto either way.
//
// A run that said nothing about the tree at all -- errored, or lost with
// the daemon that was running it -- is re-asked on the same principle and
// bounded by BaseBranchCI.Attempts (baseCIRetryDue), since a branch whose
// only run never got onto a machine is as untested as one nothing was
// started for.
//
// # Asking for one by hand (grain/task-480)
//
// The third trigger, and the only one that is not grain's own reading of
// the row: an operator asks for a run on a branch, recorded on the row
// (model.BaseBranchCI.RunRequestedAt) and honoured here on the next
// cycle. The request is a row, not a run, so that the bound below and the
// line that says what a run is made of stay in one place -- the API
// handler starts nothing (ui.Client.RequestBaseBranchRun).
//
// It is for a commit grain will not re-ask about by itself: one whose
// retries are spent (baseCIMaximumAttempts), or one a person wants judged
// again now rather than after ciRetryDelay. A run an operator cancelled
// used to be the main case, and is no longer one at all: it is recorded
// as errored and retried like any other (collectBaseBranchCI, task 285).
//
// # One run in flight (grain/task-334)
//
// And exactly one. A base-branch run is a release run: it builds and
// publishes this deployment's own images, which takes 22 minutes to an
// hour and a half, while merges into `main` land every few minutes. Until
// this, every merge started one of its own (startPostMergeCI, now
// recordPostMergeCI), so they stacked -- two full image builds at once was
// ordinary -- and several concurrent builds competing for the sandbox
// budget are what made the rest of CI slow and what made the release's own
// pre-publish gate fail tests that should take a second on being killed
// for running out of time.
//
// So starting one is this reconciler's alone, and it will not start one
// for a branch that already has one going (baseCIInFlight). A merge that
// lands meanwhile only moves the tip; the cycle that records the running
// run's conclusion is followed by one that sees a tip nothing has judged
// and starts a run against the tip *as it then stands*. Intermediate
// commits are skipped rather than queued, which is the saving: only the
// newest image is worth building.
//
// # Why coalescing does not gate fewer merges
//
// This is the one place where "publish fewer images" could quietly become
// "check fewer merges", so it is worth being explicit that it does not.
// The release run and the base-branch verdict are the same run -- it is
// started as a trusted release and collected here, and a failure holds the
// queue and earns a repair (grain/task-198). Skipping a commit's run is
// therefore skipping a verdict, and the question is whose.
//
// It is nobody's. model.BaseBranchCI is one row per (repo, branch): it has
// only ever held one answer, about the branch rather than about a commit,
// and each merge's run replaced the last one's answer as it started. What
// grain/task-320 settled on top of that is that a verdict about a commit
// the branch has moved past is an answer about a tree that has gone --
// which is why a row behind the tip is re-run rather than believed. A
// coalesced commit is exactly that case: the branch is already past it, so
// its run's verdict would have been discarded on the cycle it arrived.
// The run that does happen judges the tip, whose tree *contains* the
// skipped merges, so anything they broke together is caught by it -- which
// is the only thing this control was ever buying (two changes green alone
// and broken together). What is lost is the ability to say which of
// several coalesced merges broke the branch, and that was never certain
// anyway: the repair body has always named the last merge as "the first
// place to look, though not necessarily the whole cause".
//
// # Not a retry loop
//
// The trigger is "the tip is not the commit the row is about", so a run
// that *fails* must not leave that condition true, or a red `main` would
// burn a 90-minute release run every cycle forever. It does not: a
// failure's row names the tip it failed at, so the tip is judged and
// nothing restarts until a new commit arrives. The exception is the one
// baseCIRetryDue already drew -- a run that said nothing about the tree at
// all is re-asked, bounded by Attempts -- and that is also what makes a
// run killed by grain's own upgrade self-healing: the row concludes as
// errored, and a run gets started again rather than waiting for whatever
// merge happens next.
func reconcileBaseBranchCI(ctx context.Context, deps Deps, now time.Time) error {
	if deps.CI == nil || deps.Store == nil {
		return nil // this deployment runs no CI of its own
	}
	// A task's branch is never a base (grain/task-949). Rows for one were
	// written by stacked review and fix merges before that, and each was
	// being judged, repaired and released as though it were `main`; they
	// are cleared here, every cycle, before anything reads them.
	removed, err := deps.Store.PurgeTaskBranchBaseRows(ctx)
	for _, r := range removed {
		log.Printf("orchestrator: no longer watching %s: it is a task's branch, not a base branch", r)
	}
	if len(removed) > 0 {
		log.Printf("orchestrator: pruned %d base-branch rows for task branches", len(removed))
	}
	if err != nil {
		return fmt.Errorf("orchestrator: clearing task branches from base branch CI: %w", err)
	}
	rows, err := deps.Store.ListBaseBranchCI(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading base branch CI: %w", err)
	}
	var errs []error
	for _, row := range rows {
		if allowed, err := repoAllowedForAnyTeam(ctx, deps.Store, row.Repo); err != nil || !allowed {
			continue
		}
		// A repository whose base-branch check this deployment does not
		// run starts nothing, retries nothing and confirms nothing, and
		// its row stops being a verdict anybody waits on
		// (standDownBaseBranchCI, grain/task-3332).
		if off, err := baseBranchCIOff(ctx, deps.Store, row.Repo); err != nil {
			errs = append(errs, err)
			continue
		} else if off {
			if err := standDownBaseBranchCI(ctx, deps, row, now); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if disabled, mode, err := repoExplicitlyDisablesGrainCI(ctx, deps.Store, row.Repo); err != nil {
			errs = append(errs, err)
			continue
		} else if disabled {
			if err := standDownBaseBranchCIWithReason(ctx, deps, row, cancelReasonForCIMode(mode), now); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if err := reconcileOneBaseBranchCI(ctx, deps, row, now); err != nil {
			errs = append(errs, err)
		}
	}
	// Last, and reading the rows again, so that a run this very cycle
	// started counts against the one-at-a-time rule seeding is held to.
	if err := seedWatchedBaseBranches(ctx, deps, now); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// seedWatchedBaseBranches gives a repository nobody has merged into
// under grain's eye the row everything above works from: the default
// branch of each repository model.RepoConfig.WatchDefaultBranch is set
// on, judged by grain CI for the first time. See the file's own "A
// branch grain has never merged into" for why this is a tick rather than
// something every repository gets, and why it is one at a time.
func seedWatchedBaseBranches(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Client == nil {
		return nil // no way to ask what a repository's default branch is
	}
	rows, err := deps.Store.ListBaseBranchCI(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading base branch CI: %w", err)
	}
	if baseBranchCIBusy(rows) {
		// Some base branch is already being judged. A seed is the one
		// base-branch run that is never urgent -- the branch it is about
		// has gone untested since before this deployment was upgraded --
		// so it waits for the machine rather than competing for it.
		return nil
	}
	configs, err := deps.Store.ListRepoConfigs(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading repo configs: %w", err)
	}
	var errs []error
	for _, cfg := range configs {
		if !cfg.WatchDefaultBranch {
			continue
		}
		if allowed, err := repoAllowedForAnyTeam(ctx, deps.Store, cfg.Repo); err != nil || !allowed {
			continue
		}
		seeded, err := seedWatchedBaseBranch(ctx, deps, cfg.Repo, rows, now)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if seeded {
			break // one per cycle: the next is this one's suite away
		}
	}
	return errors.Join(errs...)
}

// seedWatchedBaseBranch enrols one repository's default branch, reporting
// whether it enrolled anything: false for a branch that already has a row
// (the overwhelmingly common case, every cycle after the first), and for
// a repository grain CI is not judging.
//
// What it writes is a row with no run and no verdict -- exactly what
// noteBaseBranchMerge writes for the first merge grain watches into a
// branch, and for the same reason (grain/task-334): starting the run is
// reconcileBaseBranchCI's job, and the row is the whole of what tells it
// there is a branch to start one for. So this costs one store write, and
// the run it leads to is an ordinary first run with the ordinary
// one-run-per-branch bound on it.
func seedWatchedBaseBranch(ctx context.Context, deps Deps, repo model.RepoRef,
	rows []model.BaseBranchCI, now time.Time) (bool, error) {

	policy, err := baseBranchCIWanted(ctx, deps.Store, repo)
	if err != nil {
		return false, err
	}
	if policy.Refusal != "" {
		// A repository ticked for watching that grain CI is not on for is
		// a setting that quietly does nothing, which is the silence
		// grain/task-320 was filed about -- so it is said, once per
		// daemon rather than once per cycle (sayOnce).
		sayOnce("base-seed:"+repo.String()+":"+string(policy.Refusal),
			"orchestrator: %s is set to have its default branch watched, but grain CI is not judging it: %s",
			repo, policy.Refusal)
		return false, nil
	}
	branch, err := deps.Client.DefaultBranch(repo.Owner, repo.Name)
	if err != nil {
		return false, fmt.Errorf("orchestrator: reading %s's default branch: %w", repo, err)
	}
	if branch == "" {
		return false, nil
	}
	for _, row := range rows {
		if row.Repo.Same(repo) && row.Branch == branch {
			return false, nil // already watched, by this or by a merge
		}
	}
	if err := deps.Store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: branch, StartedAt: now,
	}); err != nil {
		return false, fmt.Errorf("orchestrator: watching %s %s: %w", repo, branch, err)
	}
	log.Printf("orchestrator: watching %s %s, which no merge of grain's has ever asked grain CI "+
		"about: the next cycle judges whatever the queue is merging onto", repo, branch)
	return true, nil
}

// baseBranchCIBusy reports whether grain already has a base-branch run of
// its own in flight anywhere -- a first run, a re-run after the tip
// moved, or a confirmation of a failure. It is what holds seeding to one
// repository at a time: those runs are release runs, twenty minutes to an
// hour and a half apiece on this deployment (baseCIInFlight), and a
// deployment that has just had six repositories ticked should pay for
// them one after another.
//
// It is deliberately deployment-wide where baseCIInFlight is per branch:
// that bound is about not judging one branch twice at once, this one is
// about how fast grain takes on new standing obligations. Neither is an
// admission controller -- pkg/ci has one, and it is the thing that knows
// what the host can take.
func baseBranchCIBusy(rows []model.BaseBranchCI) bool {
	for _, row := range rows {
		if baseCIInFlight(row) {
			return true
		}
		if row.Confirmation == model.BaseCIConfirming && row.ConfirmRunID != "" {
			return true
		}
	}
	return false
}

// sayOnce logs a line the first time this daemon has anything to say
// under key, and never again.
//
// For the standing kind of fact a reconciler rediscovers every cycle: a
// repository configured to be watched that grain CI is not on for is
// worth one line -- a setting that does nothing is exactly what
// grain/task-320 was filed about -- and is worth nothing at all repeated
// every poll interval for as long as the misconfiguration lasts. A
// restart says it again, which is the right cadence for something an
// operator fixes by editing a row.
func sayOnce(key, format string, args ...any) {
	if _, said := saidOnce.LoadOrStore(key, true); said {
		return
	}
	log.Printf(format, args...)
}

var saidOnce sync.Map

func reconcileOneBaseBranchCI(ctx context.Context, deps Deps, row model.BaseBranchCI, now time.Time) error {
	// A branch that is not red has no repair task left to wait on: any
	// still open is closed, which releases everything depending on it
	// (baserepairlink.go). Before the tip is read, so a read that fails
	// cannot keep a green branch's dependents waiting.
	if err := closeBaseRepairsOnGreen(ctx, deps, row, now); err != nil {
		return err
	}
	if err := reconcileRepairFailing(ctx, deps, row, now); err != nil {
		log.Printf("orchestrator: %s %s: checking its repair's progress: %v", row.Repo, row.Branch, err)
	}
	// First, and for every row rather than only for a failure: is this
	// verdict even about the tree the branch is on? See the file's own
	// "The tip, not the merge".
	tip, err := baseBranchTip(ctx, deps, row)
	if err != nil {
		return err
	}
	moved := tip != "" && tip != row.Commit
	if baseCIInFlight(row) {
		// One run per branch at a time (grain/task-334). Whatever the
		// branch has done since this one started, the only thing to do
		// with it is read it: the cycle that records its conclusion is
		// the cycle after which a fresh one may start.
		return collectBaseBranchCI(ctx, deps, row, moved, now)
	}
	if row.RunRequestedAt != nil && (tip != "" || row.Commit != "") {
		// An operator asked for a run on this branch without waiting for
		// a merge to move the tip (model.BaseBranchCI.RunRequestedAt).
		// It is honoured here, below the in-flight check, so that the
		// button cannot do anything the reconciler would not: one run in
		// flight per branch still, and a branch whose tip has moved gets
		// the run it was already owed rather than a second one.
		//
		// Whatever the row says is re-asked -- a run the operator
		// themselves cancelled above all, which is the one verdict grain
		// will not re-ask about by itself and the case this exists for --
		// and a failure being confirmed is superseded by the full run,
		// since a person asking for one has overtaken the question the
		// confirmation was answering.
		policy, err := baseBranchCIWanted(ctx, deps.Store, row.Repo)
		if err != nil {
			return err
		}
		targetSHA := tip
		if targetSHA == "" {
			targetSHA = row.Commit
		}
		return startBaseBranchCI(ctx, deps, policy, row, targetSHA, now)
	}

	// Active Base Branch CI Reconciler Rule:
	// 1. Uncovered Merges: Merges landed into main without completing post-merge verification run on tip.
	// 2. Pending Merge Queue Demand: Outstanding tasks in backlog or merge queue attempting to merge into main.
	pendingTasks, err := pendingMergeTasks(ctx, deps.Store, row.Repo, row.Branch)
	if err != nil {
		return err
	}

	if moved || row.PendingMerges > 0 || (pendingTasks > 0 && row.Conclusion == "") {
		policy, err := baseBranchCIWanted(ctx, deps.Store, row.Repo)
		if err != nil {
			return err
		}
		targetSHA := tip
		if targetSHA == "" {
			targetSHA = row.Commit
		}
		if targetSHA != "" {
			return startBaseBranchCI(ctx, deps, policy, row, targetSHA, now)
		}
	}

	if row.Conclusion == "" {
		// A row with no run and no verdict: the seed noteBaseBranchMerge
		// writes for a branch grain has never judged, once the tip has
		// been read as the commit it already names -- or as nothing at
		// all, on a deployment with no GitHub client. Neither is a run to
		// collect and neither holds anything.
		return nil
	}
	if !row.Broken() {
		// Green, cancelled, or a failure a second run cleared: nothing is
		// being held. An errored run is the exception -- it is a verdict
		// about nothing, and this tip has still never been judged.
		if baseCIRetryDue(row, now) {
			policy, err := baseBranchCIWanted(ctx, deps.Store, row.Repo)
			if err != nil {
				return err
			}
			targetSHA := row.Commit
			if tip != "" {
				targetSHA = tip
			}
			return startBaseBranchCI(ctx, deps, policy, row, targetSHA, now)
		}
		return nil
	}
	// Still failing, still at the commit that failed. What is left is
	// confirming it: once confirmed, what to do about it -- a repair, a
	// revert, nothing -- is system:pipeline-steward's, woken when the
	// confirmation lands (confirmBaseBranchCI), not the daemon's.
	switch row.Confirmation {
	case model.BaseCIConfirmed:
		return nil
	case model.BaseCIConfirming:
		return collectBaseCIConfirmation(ctx, deps, row, now)
	default:
		return startBaseCIConfirmation(ctx, deps, row, now)
	}
}

// pendingMergeTasks counts outstanding tasks in backlog or merge queue
// attempting to merge into this repository and base branch.
func pendingMergeTasks(ctx context.Context, store *model.Store, repo model.RepoRef, branch string) (int, error) {
	if store == nil {
		return 0, nil
	}
	links, err := store.OpenPullRequestLinks(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	seen := make(map[string]bool)
	for _, link := range links {
		ref, err := model.ParsePullRequestRef(link.PullRequest)
		if err != nil {
			continue
		}
		if ref.Repo.Same(repo) {
			seen[link.TaskID] = true
			count++
		}
	}
	unsettled, err := store.UnsettledTasksInRepo(ctx, repo)
	if err != nil {
		return count, nil
	}
	for _, taskID := range unsettled {
		if seen[taskID] {
			continue
		}
		t, err := store.GetTask(ctx, taskID)
		if err != nil || t == nil {
			continue
		}
		targetBase := t.Base
		if targetBase == "" || targetBase == branch {
			count++
		}
	}
	return count, nil
}

// baseCIInFlight reports whether this branch already has a run of its
// own going: a row naming a run that has not recorded a conclusion yet.
// Usually a release run, and a push run where the repository's releases
// are off (baseBranchCIWanted) -- the bound is the same either way.
//
// This is the bound (grain/task-334). A base-branch run is a release run,
// 22 minutes to an hour and a half of image building on this deployment,
// and while one is in flight nothing else is started for the branch --
// not the tip it has moved to since, not a retry of the commit it is
// judging. The row is the whole of the state that needs: RunID with no
// Conclusion *is* "one is going".
//
// A row with no RunID is not in flight but has no verdict either: it is
// the seed noteBaseBranchMerge writes for a branch nothing has judged, and
// what it wants is the first run rather than a collection.
//
// Waiting on this has no timeout of its own, deliberately: there are only
// two ways a run ends up never concluding, and neither of them needs one.
// A run whose jobs hang is bounded by each job's own timeout
// (ci.DefaultJobTimeout, which also covers a job waiting for room in the
// sandbox budget), and a run lost with the daemon is concluded as errored
// by the very next collectBaseBranchCI. A wall-clock bound on top of
// those could only cancel a release that is merely slow -- and slow is
// what a release run is.
func baseCIInFlight(row model.BaseBranchCI) bool {
	return row.RunID != "" && row.Conclusion == ""
}

// collectBaseBranchCI reads back a base-branch run that had not finished
// and records how it ended.
//
// A run the service no longer has is not a verdict -- pruned past
// ci.DefaultStoredRunHistory, or lost with the daemon that was keeping it
// in memory (collectCIRun makes the same distinction for a task's own
// run). It is recorded as errored rather than forgotten: errored holds
// nothing, and leaving the row pending forever would leave a branch grain
// believes it is still testing. It is also what makes a run killed by an
// upgrade self-healing now: the row concludes, the branch's tip is still
// a commit nothing has judged, and the next cycle starts one.
//
// moved says the branch has left the commit this run is about, which
// decides only one thing here: whether a failure is worth confirming. It
// is not -- the next cycle starts a run against the tip, and a second
// opinion about a tree nobody is on would spend a run to answer a
// question that has stopped being asked.
func collectBaseBranchCI(ctx context.Context, deps Deps, row model.BaseBranchCI,
	moved bool, now time.Time) error {

	run, ok := deps.CI.Get(row.RunID)
	if !ok || run.Interrupted {
		row.Conclusion, row.Detail = string(ci.ConclusionErrored), baseCILostDetail(ok)
		row.FinishedAt = &now
		// What the re-run has to ask about (startBaseBranchCI's infra
		// re-run): the jobs an interrupted run had not passed, where the
		// run is still known. A run nothing knows any more is asked about
		// in full.
		row.ConfirmJobs = nil
		if ok {
			row.ConfirmJobs = jobsToRecheck(run)
		}
		return deps.Store.PutBaseBranchCI(ctx, row)
	}
	if !run.Conclusion.Done() {
		return nil // still going
	}
	row.Conclusion, row.Detail = string(run.Conclusion), ciDetail(run)
	if run.Conclusion == ci.ConclusionCancelled {
		// A cancelled run is a verdict about nothing at all, which is what
		// errored means here, and recording it as that is what gets this
		// tip judged rather than leaving the branch blind until somebody
		// merges again (baseCIRetryDue). Two successive restarts killed two
		// runs on this deployment's own `main` on 20 Sep 2026 and that tip
		// was never judged at all.
		//
		// A person's own cancel is no different: whoever stops a wedged or
		// duplicate run is stopping that run, and leaving the commit
		// unjudged for ever because of it is the same dead end (task 285).
		// It is retried after the same ciRetryDelay as grain's own.
		row.Conclusion = string(ci.ConclusionErrored)
		if run.CancelledBy == ci.CancelByOperator {
			row.Detail = "a person stopped this run: " + ciDetail(run)
		} else {
			row.Detail = "grain stopped this run itself: " + ciDetail(run)
		}
	}
	if run.Conclusion == ci.ConclusionFailure && baseCIAttemptsOf(row) < baseCIMaximumAttempts {
		// A failure whose every failing job failed on the deployment --
		// a capability or credential grain could not hand over, a quota,
		// a registry rate limit -- is an `infra` verdict, not a red tree
		// (infraVerdict, design/design/release-from-trunk.design.md RFT-2). While
		// it has re-runs left it is recorded as errored, which holds no
		// merge and earns no repair, and re-run like any other verdict
		// about nothing (baseCIRetryDue). Once they are spent it stands
		// as the failure it is and takes the ordinary road: held,
		// confirmed, repaired.
		if why, infra := infraVerdict(run); infra {
			row.Conclusion = string(ci.ConclusionErrored)
			row.Detail = "an infra failure (" + why + "), re-run before anything is held: " + ciDetail(run)
		}
	}
	if row.Conclusion == string(ci.ConclusionSuccess) && len(run.Jobs) == 0 {
		// A run made of no job at all judged nothing, and on a base
		// branch that is not a pass (grain/task-508).
		//
		// ci.PlanRun keeps one empty plan as a success on purpose: a
		// repository that declares no workflow at all has nothing red
		// about it, and a pull request there should not be held pending
		// on a suite nobody wrote. That rule is right for a task branch
		// and wrong for this row. Here the green is read as "the tree
		// this queue is merging onto has been tested", by the merge
		// queue (Broken), by the release pane and by whoever goes
		// looking after an incident -- and it was the one state in which
		// a base branch went unjudged and *nothing at all* said so, not
		// even the sentence pkg/ui's basebranchgate.go exists to write.
		// A repository grain CI is enabled for, whose default branch it
		// has been told to watch, and which declares no `.grain/workflows`
		// gets a gate that passes over any tree whatever, for ever.
		//
		// Errored is the right word for the same reason it is right
		// above: it is grain's word for a verdict about nothing at all.
		// It holds no merge -- only a failure does, and a repository
		// with no suite has done nothing wrong -- but it is re-asked
		// while a suite might appear (baseCIRetryDue), it is not
		// mistakable for a tested tree, and it is what the gate reading
		// reports on.
		//
		// Only this case. A repository that declares workflows and
		// matched none of them already arrives errored with run.Error
		// set (ci.Plan.Unmatched), and a cancelled run is already
		// errored by the branch above.
		row.Conclusion = string(ci.ConclusionErrored)
		row.Detail = "grain CI ran no job at all, so nothing judged this branch: " + ciDetail(run)
	}
	finished := run.FinishedAt
	if finished == nil {
		finished = &now
	}
	row.FinishedAt = finished
	// Which jobs a confirmation would have to re-run, decided here
	// because this is where the run itself is in hand: by the time the
	// confirmation is started the run may have been pruned, and a
	// confirmation that could not name its jobs would re-run the whole
	// suite or nothing.
	row.ConfirmJobs = jobsToRecheck(run)
	// And the verdict goes into the branch's streak, before anything
	// below can drop it for being about a tip the branch has left
	// (basecistreak.go).
	noteBaseCIStreak(&row, run)
	if err := deps.Store.PutBaseBranchCI(ctx, row); err != nil {
		return err
	}
	// Every merge into this branch that nothing had judged yet, up to the
	// moment this run started, is in the tree it judged: the overlap
	// rule's record of each learns what became of it, which is how a
	// skipped re-check followed by a red branch is counted
	// (model.MergeRecheckStats.SkippedBaseRed). Errored is no verdict and
	// is not recorded.
	if v := baseVerdictOf(row.Conclusion); v != "" {
		if err := deps.Store.JudgeMergeRechecks(ctx, row.Repo, row.Branch, row.StartedAt, v); err != nil {
			log.Printf("orchestrator: recording %s %s's verdict on the merges into it: %v", row.Repo, row.Branch, err)
		}
	}
	if row.Conclusion == string(ci.ConclusionSuccess) {
		// Only the failures recorded against this commit go: failures
		// against the bases before it were evicted as the branch moved
		// off them (reconcileBaseBranchCI), and the engine discards any
		// that slipped past at lookup. The passing entries stay -- the
		// whole cache used to be cleared here, which threw away every
		// pass on every green verdict for nothing a pass could be
		// wrong about.
		if deps.CI != nil {
			if n, ok, err := deps.CI.EvictBaseTransitionFailures(ctx, row.Commit); err == nil && ok && n > 0 {
				log.Printf("orchestrator: base branch %s recovered to green: evicted %d negative CI cache entries", row.Branch, n)
			}
		}
	}
	if row.Broken() {
		if moved {
			// Too late for a confirmation re-run of this tip to mean
			// anything, but not too late for the verdict to count: it is
			// on the streak, and a streak long enough confirms the branch
			// at this tip on its own (grain/task-1034).
			if confirmed, err := confirmBaseCIStreak(ctx, deps, row, now); err != nil || confirmed {
				return err
			}
			streak := ""
			if s := baseCIStreakSummary(row); s != "" {
				streak = "; " + s
			}
			log.Printf("orchestrator: %s %s was red at %s (%s%s), and has already moved past it: "+
				"the next cycle judges the tip instead",
				row.Repo, row.Branch, shortSHA(row.Commit), row.Detail, streak)
			return nil
		}
		// "after the merge of task X" only where there was one: a branch
		// grain watches rather than merges into (seedWatchedBaseBranches)
		// goes red at a commit nobody here authored, and naming an empty
		// task for it would read as a bug in the queue rather than as a
		// commit somebody pushed.
		after := " after the merge of task " + row.MergedTaskID
		if row.MergedTaskID == "" {
			after = ", which no merge of grain's produced,"
		}
		streak := ""
		if s := baseCIStreakSummary(row); s != "" {
			streak = "; " + s
		}
		log.Printf("orchestrator: %s %s is red at %s%s (%s%s): "+
			"the merge queue is holding %s while grain checks whether that reproduces",
			row.Repo, row.Branch, shortSHA(row.Commit), after, row.Detail, streak, row.Repo)
		return startBaseCIConfirmation(ctx, deps, row, now)
	}
	return nil
}

// jobsToRecheck is every job of a failing run that did not pass, as the
// plan-wide ids ci.Request.Jobs selects by ("<workflow path>:<job id>",
// ci.planID).
//
// Everything that is not a success, rather than only the failures: a job
// skipped because the job it `needs` failed never judged this tree
// either, and a confirmation that re-ran the failure without it could
// report a green that nothing had actually finished. Selecting a job
// brings its own dependencies with it (ci.selectPlanJobs), so naming the
// far end of a chain is enough to have the chain run.
//
// The jobs that *did* pass are deliberately left out, and are the whole
// saving: a confirmation costs the failing jobs again, not the suite.
func jobsToRecheck(run ci.Run) []string {
	var out []string
	for _, job := range run.Jobs {
		if job.Conclusion == ci.ConclusionSuccess || job.JobID == "" {
			continue
		}
		// The plan-wide spelling where the run knows which file the job
		// came from, and the bare id otherwise -- which ci.selectPlanJobs
		// accepts as long as it is unique, and rejects loudly rather than
		// guessing when it is not.
		if job.WorkflowPath == "" {
			out = append(out, job.JobID)
			continue
		}
		out = append(out, job.WorkflowPath+":"+job.JobID)
	}
	return out
}

func baseCILostDetail(known bool) string {
	if known {
		return "the run was interrupted by the daemon that was running it stopping"
	}
	return "the run is no longer known to this daemon"
}

// baseBranchTip is the commit the branch is actually on, or "" where that
// cannot be read: a deployment with no GitHub client wired, and a branch
// that has been deleted. Both are "nothing to compare against" rather
// than "the branch has moved", which is what keeps a row from being
// replaced on the strength of a question nobody could answer.
func baseBranchTip(ctx context.Context, deps Deps, row model.BaseBranchCI) (string, error) {
	return branchTip(ctx, deps, row.Repo, row.Branch)
}

// branchTip is the same read addressed by repository and branch rather
// than by a row, which is what basebranchdispatch.go needs: it watches
// the same branches from a row of its own.
func branchTip(ctx context.Context, deps Deps, repo model.RepoRef, branch string) (string, error) {
	if deps.Client == nil {
		return "", nil
	}
	head, err := deps.Client.GetBranchHead(repo.Owner, repo.Name, branch)
	if err != nil {
		return "", fmt.Errorf("orchestrator: reading %s's head on %s: %w", branch, repo, err)
	}
	if head == nil {
		return "", nil
	}
	return head.SHA, nil
}

// baseBranchCIRefusal is why grain is not putting a repository's base
// branches through its own CI -- empty when it is. A value rather than a
// bare bool because both callers log it, and "no post-merge run, no
// reason" is the silence grain/task-320 was filed about.
type baseBranchCIRefusal string

const (
	// baseCINotEnabled: the grain-ci capability is not on for this repo.
	// It is the only refusal left. Releases being off used to be a second
	// one; grain/task-458 reduced it to changing the event the run was
	// asked for, and grain/task-461 took even that away -- every judging
	// run is a push now, and the release switch decides only whether
	// there is a second, publishing run beside it
	// (baseBranchReleaseWanted).
	baseCINotEnabled baseBranchCIRefusal = "grain CI is not enabled for it"
	// baseCIWorkOff: this deployment has said it does not run the
	// repository's base-branch check -- the repository's standing work is
	// off, or model.BaseBranchCIItem is (grain/task-3332). Unlike
	// baseCINotEnabled a merged task's own grant does not overrule it
	// (recordPostMergeCI): it is an operator's decision about this
	// repository, not a default nobody chose.
	baseCIWorkOff baseBranchCIRefusal = "this deployment doesn't run its standing work"
)

// baseBranchCIEvent is what every run that judges a base branch is asked
// for, without exception: an untrusted push of the repository's own push
// plan (grain/task-461). It is a constant rather than a field on the
// policy below because "which run is the verdict" is not a per-repository
// decision any more -- see baseBranchCIWanted's "The gate is always the
// push suite".
const baseBranchCIEvent = ci.EventPush

// baseBranchCIPolicy is how grain judges one repository's base branches:
// with which jobs, or -- Refusal -- not at all.
//
// It is one value rather than two answers because the two are one
// decision, taken in one place and read by everything that has to know
// it: the two that start a run -- the tip that has moved
// (startBaseBranchCI) and the second opinion on a failure
// (startBaseCIConfirmation) -- and the three that only need to know
// whether there will be one (recordPostMergeCI, seedWatchedBaseBranch,
// recordFinishedBatch), which read Refusal and nothing else.
//
// It used to carry the event too, and that is the field grain/task-461
// took out: a repository grain released was judged under ci.EventRelease,
// which made the publishing and the verdict one run and let a flaky
// publisher hold every merge in the repository.
type baseBranchCIPolicy struct {
	// Refusal is why grain is not judging this repository's base
	// branches, empty when it is.
	Refusal baseBranchCIRefusal
	// Jobs narrows the run to the jobs that judge the branch. Empty is
	// the whole push plan, which is what a repository gets until it names
	// something narrower (model.RepoConfig.BaseBranchCIJobs).
	Jobs []string
}

// request is the ci.Request this policy judges one commit of one branch
// with: a push, never a trusted release, so no release secrets are
// placed and nothing is published (ci.Service.Start). That is not a
// choice this function makes per repository -- there is no longer any
// way to ask for a judging run that is a release, which is what stops a
// publisher's conclusion ever reaching model.BaseBranchCI.Broken. The
// publishing happens in a run of its own (basebranchrelease.go).
func (p baseBranchCIPolicy) request(repo model.RepoRef, branch, commit, requestedBy string) ci.Request {
	return ci.Request{
		Repo: repo.String(), Ref: branch, Commit: commit,
		Event: baseBranchCIEvent, TrustedRelease: false,
		RequestedBy: requestedBy, Jobs: p.Jobs,
	}
}

// baseBranchCIWanted decides how -- or whether -- grain puts this
// repository's base branches through its own CI.
//
// The switches are the repository's rather than any one task's, which is
// the point of asking it here (grain/task-320): whether a base branch is
// worth testing is a property of the repository, and reading it off
// whichever task happened to merge last made the answer depend on a tick
// in a task form.
//
//   - grain CI enabled for the repo at all -- its CI mode, which
//     defaultTaskGrants turns into the grain-ci grant a task filed there
//     would hold (pkg/capability/grainci on why the mode, not the
//     capability, is the switch), or grainci.RequiredFor, the one
//     repository grain checks whether or not anybody opted it in;
//   - RepoConfig.BaseBranchCIJobs, which narrows the push plan the branch
//     is judged by, for cost, and can narrow it but never stop it;
//   - whether this deployment runs the check at all: model.BaseBranchCIItem,
//     resolved through model.RunsWorkItem, so the repository's standing
//     work default decides unless the item has an override of its own
//     (grain/task-3332). That is the one switch that stops judging, and
//     its refusal (baseCIWorkOff) is one a merged task's grant does not
//     overrule. The release item is not consulted here, and says nothing
//     about judging (baseBranchReleaseWanted is the whole of what it
//     decides).
//
// # Publishing and judging are not the same switch (grain/task-330, 461)
//
// They used to be, in both directions, and both of them cost this
// deployment weeks.
//
// A base-branch run *was* the trusted release event. So
// `ReleaseWorkflowsDisabled` switched off the compensating control that
// stops the merge queue merging onto a red `main`
// (docs/merge-queue-behind.md) along with the publishing it is named for
// -- and left the last red row on such a branch unrefreshable, since
// nothing would ever start another run for it. That was grain/task-330's
// half, answered by asking under `push` when releases were off.
//
// The other half is that with releases *on* the verdict was the
// publisher's. `release` matched `.grain/workflows/release.yml` and
// nothing else, so the gate ran no test (grain/task-355); the patch for
// that moved the tests into the release run, which made the whole suite
// run inside a trusted run carrying release secrets and still left a
// flaky publish job holding every merge in the repository
// (grain/task-456 on the flake). An operator ruled on it: *"Release
// should not be the gate. That is the CI's job. If we wanted release to
// be the gate then we would do it as part of the ci jobs."*
//
// # The gate is always the push suite (grain/task-461)
//
// So this returns ci.EventPush for every repository it judges, whatever
// its releases are doing -- there is no event field left to return
// anything else (baseBranchCIEvent). Which repositories it judges is the
// standing work switch above: every one grain CI is enabled for, unless
// this deployment has said it does not run that repository's check. Untrusted, no release secrets, nothing
// published, and BaseBranchCIJobs narrows it where an operator has named
// jobs, whether releases are on or off.
//
// A merge still publishes: reconcileBaseBranchRelease starts the trusted
// release run at the same tip when `ReleaseWorkflowsDisabled` is unset,
// and records it on model.BaseBranchRelease, a row the merge queue does
// not read. So there are two runs per merged tip, and only one of them
// is a verdict. The secrets boundary did not move with any of this: the
// judging run is a push run because it always was one for a repository
// with releases off, and the release run keeps its secrets because it is
// still the release.
//
// # Naming no jobs is not a refusal any more (grain/task-458)
//
// It was until this: releases off with nothing named started no run at
// all, on the argument that grain cannot tell a publishing job from a
// judging one by looking, so "everything a push would run" is not a safe
// reading of a repository that has asked not to publish. The argument
// still holds for one shape of repository -- an Actions-backed workflow
// triggered `on: push` to `main` carries its publish job in the very plan
// a push run would run, and that job fails for want of secrets rather
// than being skipped -- but it cannot be the default, because the default
// is what every repository gets by accident.
//
// What it cost on this deployment: releases were switched off on
// bwsalmon/grain because a publish job was flaky, nothing was named here,
// and `main` went unjudged for 33 hours across ~100 merges with the queue
// merging onto it the whole time and nothing on any surface saying so. A
// gate that may be wrong for an unusual repository is a better default
// than no gate at all for every repository, and the repository in the
// unusual shape says so by naming its judging jobs -- one field, which is
// exactly what that field is for.
//
// Skipping a job that would need release material it is not being given,
// rather than guessing either way, was weighed and not built: grain reads
// capabilities per repository rather than per job (ci.Request.Capabilities
// is filled from repo settings), so there is nothing in a plan to read
// that answer off without inferring it from a job's steps.
//
// # What judges a repository grain does release (grain/task-355, 461)
//
// The same thing that judges one it does not: its push plan. This is the
// rule grain/task-461 reversed, and it is worth saying what the old one
// was, because it was argued carefully and is still written into a
// workflow file somewhere in every deployment that followed it.
//
// The old rule was "the jobs which judge a branch answer the event the
// judging run is asked for" -- with releases on that event was `release`,
// so a repository said so in its workflow's `on:` block and its test jobs
// were planned into the release run. It was sound about the mechanism:
// which jobs a plan holds is decided by the event, and a native grain
// workflow gets no Actions translation to widen it (ci.workflowEvent), so
// a repository whose tests answer `push` and whose publisher answers
// `release` really was judged by the publisher alone. It was wrong about
// which run should be the verdict. It put the whole suite inside a
// trusted run carrying release secrets, it ran those tests twice per
// merge wherever they also answered `push`, and it left the publisher's
// conclusion holding the queue -- which on this deployment is the failure
// that actually happens, over and over.
//
// So the answer is no longer a line a repository writes: grain asks for
// the push plan, which is where a repository's tests already are.
// startBaseBranchCI logs the jobs a base-branch run is made of, which is
// how a repository whose push plan is empty or surprising says so on the
// cycle it happens. docs/merge-queue-behind.md has the alternatives that
// were weighed on the way here.
//
// # What is still bundled with the publishing (grain/task-397)
//
// Everything else the release run did. Judging came out of it here;
// anything that run produces which is neither a release artifact nor a
// verdict is still produced only when releases are on -- in this
// repository, the sandbox guest cache `.grain/workflows/release.yml`
// fills on every merge, which is a build input every grain CI job
// resolving a guest reads rather than anything anyone releases. Under
// either branch of the decision below it stops being filled, silently,
// and every branch that merges the base afterwards pays tens of minutes
// for it on the longest job in the suite
// (docs/merge-queue-behind.md, "Publishing is also a CI input").
//
// Nothing is inferred about that here, because nothing here can be: what
// a repository's release workflow produces is not something grain reads
// off its settings. The coupling is reported where it *can* be
// established -- the sandbox guest this deployment resolves names the
// repository that publishes it, so pkg/ui/sandbox_guest.go puts it in
// front of the operator at the switch and in the sandbox health reading.
//
// # The configuration repository is judged, never released
//
// grainci.RequiredFor's repository is the one whose capability is not
// read at all: grain checks it whether or not anybody opted it in,
// because merging a dump grain cannot import can stop the deployment on
// its next start. The event it is asked under is no longer special --
// everything is a push now -- but it was the first repository to need
// that, and the reason is worth keeping: grain publishes nothing out of
// its own configuration, the workflow grain supplies for it answers
// `push` and `workflow_dispatch` and nothing else (cmd/grain's
// configRepoCISource), so asking under `release` matched no workflow and
// concluded errored once per merge while the one check this exception
// exists for never ran (grain/task-396). baseBranchReleaseWanted keeps
// the other half of that: nothing is published from it either.
func baseBranchCIWanted(ctx context.Context, store *model.Store,
	repo model.RepoRef) (baseBranchCIPolicy, error) {

	// The whole push plan unless the repository narrows it below. The
	// event is not in here because it is not a decision (baseBranchCIEvent).
	var policy baseBranchCIPolicy
	if store == nil {
		// Nothing to read the repository's own answer out of, so there is
		// no repository-level opt-in -- which leaves recordPostMergeCI's
		// per-task grant as the only thing that can turn this on, the way
		// it was the only thing before this function existed.
		policy.Refusal = baseCINotEnabled
		return policy, nil
	}
	cfg, err := store.GetRepoConfig(ctx, repo)
	if err != nil {
		return baseBranchCIPolicy{}, fmt.Errorf("orchestrator: reading CI settings for %s: %w", repo, err)
	}
	if cfg != nil {
		// Narrowed to the jobs the repository named, if it named any, and
		// now whether or not its releases are on -- BaseBranchCIJobs is a
		// cost control over the push plan, and the push plan is what
		// judges every repository since grain/task-461. Naming none is
		// not a refusal: it is the whole push plan (grain/task-458).
		policy.Jobs = cfg.BaseBranchCIJobs
	}
	if grainci.RequiredFor(repo) {
		// The deployment's own state repository: judged without anybody
		// opting it in, so the capability below is not consulted for it
		// (pkg/capability/grainci on why this is not an ordinary per-repo
		// setting) -- and nor is the work switch below, which cannot turn
		// this one off.
		return policy, nil
	}
	if !model.RunsWorkItem(cfg, model.BaseBranchCIItem) {
		// The operator's switch (grain/task-3332), asked before the
		// capability because it is the stronger answer: a repository
		// grain CI is enabled for can still be one this deployment has
		// said it does not judge.
		policy.Refusal = baseCIWorkOff
		return policy, nil
	}
	grants, err := defaultTaskGrants(ctx, store, &repo, "")
	if err != nil {
		return baseBranchCIPolicy{}, fmt.Errorf("orchestrator: reading grain CI settings for %s: %w", repo, err)
	}
	if !grainci.Granted(grants) {
		// The refusal rides on the policy rather than replacing it: the
		// merged task's own grant can still turn this one on
		// (recordPostMergeCI), and what it then records is a branch
		// judged the way this repository's settings describe.
		policy.Refusal = baseCINotEnabled
	}
	return policy, nil
}

// baseCIRetryDue reports whether a concluded run owes this commit another
// one: an errored run, which is a verdict about nothing at all, within
// baseCIMaximumAttempts and no sooner than ciRetryDelay after the last one.
// The task-branch policy this mirrors is ciReplacementFor.
//
// A cancelled run is retried too, and that is why the word this reads is
// only "errored": collectBaseBranchCI records every cancelled run as
// errored, since a daemon restart or an upgrade killing the run that was
// judging `main` is a verdict about nothing and used to leave the branch
// unjudged until somebody happened to merge again (grain/task-471). That
// includes a run an operator cancelled (ci.CancelByOperator): for a while
// it was left alone, which left a branch whose wedged run somebody stopped
// unjudged for good (task 285).
//
// A run grain could not ask for at all is recorded as errored too
// (refuseBaseBranchCI, grain/task-509), which is what puts it under this
// bound: a refusal that was a hiccup is re-asked and heals, and one that
// is a setting stops costing a request every cycle and stands where
// somebody can read it.
func baseCIRetryDue(row model.BaseBranchCI, now time.Time) bool {
	if row.Conclusion != string(ci.ConclusionErrored) {
		return false
	}
	if baseCIAttemptsOf(row) >= baseCIMaximumAttempts {
		return false
	}
	if row.FinishedAt == nil {
		return true
	}
	return !now.Before(row.FinishedAt.Add(ciRetryDelay))
}

// baseCIMaximumAttempts bounds how many runs one commit of a base branch
// gets while each ends as a verdict about nothing: the first, and
// maxInfraRetries automatic re-runs of it (RFT-2). A person asking for a
// run starts the count over (startBaseBranchCI).
const baseCIMaximumAttempts = 1 + maxInfraRetries

// baseCIAttemptsOf is how many runs this commit has had, counting the one
// the row names. Zero is a row written before they were counted, which is
// one run.
func baseCIAttemptsOf(row model.BaseBranchCI) int {
	if row.Attempts == 0 {
		return 1
	}
	return row.Attempts
}

// startBaseBranchCI asks grain CI about a base branch and makes that run
// the branch's row. Two callers, both in reconcileOneBaseBranchCI: the
// branch has moved to a commit nothing has judged, or the run it does have
// judged nothing (baseCIRetryDue).
//
// Never called while one is in flight for this branch (baseCIInFlight is
// checked first, grain/task-334): a run judging an older commit is waited
// for rather than superseded, because cancelling and restarting on every
// merge is how a branch that merges faster than it builds never finishes
// a release at all.
//
// policy is what the run is asked for: a `push` of the whole push plan,
// narrowed to the jobs the repository named if it named any, whatever
// its releases are doing (baseBranchCIWanted). It is never called for a
// repository whose check is switched off: reconcileBaseBranchCI stands
// those rows down before anything here is reached (standDownBaseBranchCI). The publishing is a
// separate run at the same tip and no part of this verdict
// (basebranchrelease.go).
func startBaseBranchCI(ctx context.Context, deps Deps, policy baseBranchCIPolicy,
	row model.BaseBranchCI, sha string, now time.Time) error {

	retry := sha == row.Commit
	// Whether a person asked for this one (model.BaseBranchCI.RunRequestedAt).
	// It is read off the row rather than passed in because the row is
	// where the request lives and this is the only thing that consumes
	// it: the fresh row below replaces it, which is what clears it.
	requested := row.RunRequestedAt != nil
	if row.Confirmation == model.BaseCIConfirming && row.ConfirmRunID != "" {
		reason := fmt.Sprintf(
			"superseded: %s moved to %s, so the failure at %s is no longer what is holding it",
			row.Branch, shortSHA(sha), shortSHA(row.Commit))
		if retry {
			// The same commit, asked about in full. The confirmation was
			// a narrowed re-run of the jobs that failed there, and the
			// run replacing it answers everything it was asking.
			reason = fmt.Sprintf(
				"superseded: a full run of %s at %s was asked for, which answers this",
				row.Branch, shortSHA(sha))
		}
		deps.CI.Cancel(row.ConfirmRunID, reason, ci.CancelBySystem)
	}
	req := policy.request(row.Repo, row.Branch, sha, "base:"+row.Repo.String()+":"+row.Branch)
	// Trunk's post-merge suite is a release gate (releaseGatePriority).
	req.Priority = releaseGatePriority(deps, row.Repo, row.Branch)
	// A person asking again about the commit the row has already judged
	// is asking for work, not for the answer grain has: content-addressed,
	// the run would replay the cached verdict in milliseconds -- a red
	// branch whose failure was a flake, or was fixed outside its tree,
	// could never be turned green from the button (the 2026-10-13 §2
	// walkthrough). Recheck runs every job uncached and writes what it
	// learns back over the entry it skipped.
	req.Recheck = retry && requested
	// grain's own re-run of a verdict about nothing -- cancelled, errored,
	// interrupted, an infra failure (baseCIRetryDue) -- is the jobs that
	// did not pass, uncached: the passes the run did reach still stand,
	// and the rest is asked for as work rather than replayed. A run that
	// never had jobs to name is asked about in full, as it always was.
	infraRetry := retry && !requested && row.Conclusion == string(ci.ConclusionErrored) && len(row.ConfirmJobs) > 0
	if infraRetry {
		req.Jobs, req.Recheck = slices.Clone(row.ConfirmJobs), true
	}
	run, err := deps.CI.Start(ctx, req)
	if err != nil {
		return refuseBaseBranchCI(ctx, deps, row, sha, retry, requested, err, now)
	}
	started := run.StartedAt
	if started == nil {
		started = &now
	}
	fresh := model.BaseBranchCI{
		Repo: row.Repo, Branch: row.Branch, RunID: run.ID, Commit: sha,
		StartedAt: *started, Attempts: 1,
		// Whose merge this run is the answer to, if grain merged anything
		// into this branch since the last run was started: PendingTaskID
		// is where noteBaseBranchMerge left it, and this is the run that
		// judges it (grain/task-334). It empties as it is taken up, so a
		// later run started for a commit nobody's merge produced -- a
		// hand-merge, a push straight to the branch -- names nobody, which
		// is the property dropping MergedTaskID used to buy outright.
		MergedTaskID: row.PendingTaskID,
		// RepairTaskID is dropped -- a repair filed for the old
		// commit is about a tree that is no longer there, and if this run
		// is red too the new failure files its own. So does the whole
		// confirmation: it was a second opinion about a commit nobody is
		// being held on any more, and this run is the first opinion about
		// the one they are.
		//
		// Covers takes up the merges that were waiting for a run, beside
		// PendingTaskID and for the same reason: this is the run that
		// answers for all of them. It empties as it is taken, so the
		// merges that arrive while this run is going are counted for the
		// next one.
		Covers: row.PendingMerges,
	}
	carryBaseCIStreak(&fresh, row)
	switch {
	case retry:
		// The same tree, asked again. Everything the row knows about this
		// commit still holds -- who merged it above all, since that is
		// who a failure will name -- so only the run and the count move,
		// and a merge waiting for its own run goes on waiting.
		fresh.MergedTaskID = row.MergedTaskID
		fresh.PendingTaskID = row.PendingTaskID
		fresh.PendingMerges = row.PendingMerges
		fresh.Covers = row.Covers
		fresh.Attempts = baseCIAttemptsOf(row) + 1
		switch {
		case requested:
			// A person asked, so the count starts over: Attempts bounds
			// grain re-asking a question nothing answered (baseCIRetryDue),
			// and an operator who has looked at the branch and pressed the
			// button has asked a fresh one. It is also what keeps the
			// button working on a commit whose automatic budget is spent,
			// which is exactly the commit somebody is standing in front of
			// when they press it.
			fresh.Attempts = 1
			log.Printf("orchestrator: %s asked for grain CI on %s %s at %s by hand (the run it has: %s)",
				baseCIRequester(row), row.Repo, row.Branch, shortSHA(sha), baseCILastVerdict(row))
		case infraRetry:
			log.Printf("orchestrator: %s %s at %s was never judged (%s): re-running %s uncached (attempt %d of %d)",
				row.Repo, row.Branch, shortSHA(sha), row.Detail, strings.Join(req.Jobs, ", "),
				fresh.Attempts, baseCIMaximumAttempts)
		default:
			log.Printf("orchestrator: %s %s at %s was never judged (%s): asking grain CI again (attempt %d)",
				row.Repo, row.Branch, shortSHA(sha), row.Detail, fresh.Attempts)
		}
	case row.Commit == "":
		// A branch grain has never judged: the row a first merge wrote
		// (noteBaseBranchMerge) or one seedWatchedBaseBranch enrolled.
		// Either way there is no previous commit to have moved on from.
		log.Printf("orchestrator: %s %s is at %s and grain CI has never judged it: asking",
			row.Repo, row.Branch, shortSHA(sha))
	default:
		log.Printf("orchestrator: %s %s has moved to %s since grain CI last judged it at %s: asking again",
			row.Repo, row.Branch, shortSHA(sha), shortSHA(row.Commit))
		if deps.CI != nil {
			if n, ok, err := deps.CI.EvictBaseTransitionFailures(ctx, row.Commit); err == nil && ok && n > 0 {
				log.Printf("orchestrator: base branch %s moved from %s to %s: evicted %d negative CI cache entries",
					row.Branch, shortSHA(row.Commit), shortSHA(sha), n)
			}
		}
	}
	if requested && !retry {
		// The branch moved before the request was taken up, so the run
		// above is the one the branch was owed anyway -- said out loud
		// because the person who pressed the button is owed an account of
		// what became of it, and it is not a run at the commit they were
		// looking at.
		log.Printf("orchestrator: that run is also what %s asked for by hand on %s %s",
			baseCIRequester(row), row.Repo, row.Branch)
	}
	log.Printf("orchestrator: run %s is what judges %s %s at %s, %s, asked for under %s, and it is made of: %s",
		run.ID, row.Repo, row.Branch, shortSHA(sha), mergesCovered(fresh.Covers),
		baseBranchCIEvent, baseCIJobsOf(run))
	return deps.Store.PutBaseBranchCI(ctx, fresh)
}

// mergesCovered says how many of grain's own merges one post-merge run is
// the answer to, for the line above.
//
// It is a log line rather than a decision, and it is here for the reason
// baseCIJobsOf below is: the number decides something nothing was saying
// out loud. A batch is credited with saving the post-merge suites its
// members would each have spent, but one run at a time
// (model.BaseBranchCI) means merges arriving faster than a suite finishes
// already share a run, so on a busy branch most of those suites were
// never going to be spent -- measured here, 2.7 merges to the run, which
// takes the saving at the default batch size below zero
// (docs/merge-queue-batching.md). Now the run that does the coalescing
// says how much it coalesced, so the next reading of that is one grep
// rather than a reconstruction from git log and run history.
func mergesCovered(n int) string {
	switch n {
	case 0:
		// A tip no merge of grain's produced: a hand-merge, a push
		// straight to the branch, a branch seeded for watching, or a run
		// started before these were counted.
		return "covering no merge grain made"
	case 1:
		return "covering 1 merge"
	default:
		return fmt.Sprintf("covering %d merges", n)
	}
}

// refuseBaseBranchCI records that grain could not even ask for a
// base-branch run: the request itself was refused, so there is no run,
// no verdict, and nothing about this tip has been judged
// (grain/task-509).
//
// Until this, that was an error returned to the reconciler and nothing
// else. The row was left exactly as it was, so the next cycle asked the
// same question, was refused the same way, and logged the same line --
// for ever, with no attempt counted, nothing on any surface, and the
// merge queue merging onto the branch throughout. It is the last shape
// of silently-disabled gate the reading in pkg/ui/basebranchgate.go
// could not see: grain/task-508 covers the run that starts and judges
// nothing, and this is the run that never starts at all.
//
// The way in that actually happens is a job list: model.RepoConfig.
// BaseBranchCIJobs names jobs by id, ci.selectPlanJobs rejects a
// selector matching nothing rather than guessing, and whether a name
// matches is a property of a commit that no settings pane can check
// (ui.SetRepoBaseBranchCIJobs' own note). So a list that has fallen
// behind the workflows it names turns the gate off, and the operator who
// presses "run the base-branch check" gets a button that does nothing
// visible however often they press it -- their request is consumed only
// by a run being started (RunRequestedAt, grain/task-480).
//
// Recorded as errored, which is the same word collectBaseBranchCI gives
// a run that concluded having judged nothing, and for the same reason:
// it is a verdict about nothing at all. That buys the two things the
// error return did not. baseCIRetryDue re-asks a bounded few times, so a
// refusal that was transient heals and one that is not stops costing a
// request every cycle; and the row carries the refusal in words, which
// is what ui.baseGateJudgedNothing reports and the whole diagnosis when
// it does -- "selected job %q does not exist in the matching workflows"
// names the setting to fix.
//
// It is emphatically not a failure. Nothing about the tree was asked, so
// nothing about it is wrong, and a refusal that held the queue would be
// the deadlock this file's neighbours keep being rewritten to avoid.
func refuseBaseBranchCI(ctx context.Context, deps Deps, row model.BaseBranchCI, sha string,
	retry, requested bool, cause error, now time.Time) error {

	if ctx.Err() != nil {
		// The daemon is stopping under this cycle, so what failed is the
		// cycle rather than the request: grain CI has refused nothing.
		// Written down it would be a verdict about nothing that spends
		// one of this commit's attempts and reads on the gate as a
		// repository whose settings are wrong, which every restart would
		// then leave behind. The next cycle asks again.
		return fmt.Errorf("orchestrator: asking grain CI about %s %s at %s: %w",
			row.Repo, row.Branch, shortSHA(sha), cause)
	}
	// The same attribution the run that did start would have carried
	// (startBaseBranchCI above): who to name if this tip turns out to be
	// broken, and how many attempts it has had.
	fresh := model.BaseBranchCI{
		Repo: row.Repo, Branch: row.Branch, Commit: sha,
		StartedAt: now, FinishedAt: &now, Attempts: 1,
		Conclusion:   string(ci.ConclusionErrored),
		Detail:       "grain CI could not be asked for a run: " + truncate(cause.Error(), 200),
		MergedTaskID: row.PendingTaskID,
	}
	carryBaseCIStreak(&fresh, row)
	if retry {
		fresh.MergedTaskID, fresh.PendingTaskID = row.MergedTaskID, row.PendingTaskID
		fresh.Attempts = baseCIAttemptsOf(row) + 1
		if requested {
			// A person asking starts the count over, for the reason it does
			// on a run that started: the bound is on grain re-asking a
			// question nothing answered, not on somebody who has looked at
			// the branch and asked a fresh one.
			fresh.Attempts = 1
		}
	}
	log.Printf("orchestrator: grain CI would not judge %s %s at %s at all (attempt %d): %v",
		row.Repo, row.Branch, shortSHA(sha), fresh.Attempts, cause)
	return deps.Store.PutBaseBranchCI(ctx, fresh)
}

// baseCIRequester is who asked for a run by hand, for the line that says
// one was. "an operator" for a request with no name on it, which is what
// a deployment with no identified actor writes.
func baseCIRequester(row model.BaseBranchCI) string {
	if row.RunRequestedBy == "" {
		return "an operator"
	}
	return row.RunRequestedBy
}

// baseCILastVerdict is what the row being replaced said, for the same
// line: the word and the detail, or a note that there was no verdict at
// all -- which is the state a branch is in while nothing has judged its
// tip, and one of the reasons somebody presses the button.
func baseCILastVerdict(row model.BaseBranchCI) string {
	switch {
	case row.Conclusion == "":
		return "no verdict yet"
	case row.Detail == "":
		return row.Conclusion
	default:
		return row.Conclusion + ", " + row.Detail
	}
}

// baseCIJobsOf names the jobs a base-branch run was planned with, for the
// line startBaseBranchCI writes as it starts one.
//
// It is a log line rather than a decision, and it is here because of what
// the absence of it cost (grain/task-355). Which jobs judge a branch is
// decided by the event and by nothing this package can see: this
// deployment's own `main` was judged for weeks by one job that builds and
// publishes an image and by no test at all -- the repository's tests
// answered `push`, its publisher answered `release`, and a base-branch
// run is a release -- while the gate went on holding merges and filing
// repairs as though it had asked about the tree. Nothing said which jobs
// the gate was made of, so nothing about the run itself made that
// visible. Now the first line about a base-branch run says what it is
// asking, and a gate that judges nothing is one line rather than an
// archaeology.
func baseCIJobsOf(run ci.Run) string {
	if len(run.Jobs) == 0 {
		return "no job at all, so it judges nothing -- no workflow of this repository answers that event"
	}
	names := make([]string, 0, len(run.Jobs))
	for _, job := range run.Jobs {
		if job.WorkflowPath == "" {
			names = append(names, job.JobID)
			continue
		}
		names = append(names, job.WorkflowPath+":"+job.JobID)
	}
	return strings.Join(names, ", ")
}

// maxBaseCIConfirmations bounds how many times grain asks for the second
// opinion before it stops asking. A confirmation that never reports is
// not a reason to keep starting runs forever -- a deployment whose CI
// cannot run at all would spend its whole capacity on a question it
// cannot answer -- and it is not a reason to quietly reopen the queue
// either. Two attempts, and then the failure stands: see
// confirmBaseBranchCI's own note on which way this errs.
const maxBaseCIConfirmations = 2

// startBaseCIConfirmation asks grain CI the same question a second time,
// which is the whole of this file's answer to "is that red real".
//
// Why it has to be asked rather than simply re-read: a grain CI job's
// verdict is content-addressed per commit (ci.Fingerprint), so an
// ordinary re-run of the same commit replays the cached failure in
// seconds and agrees with itself every time. ci.Request.Recheck is the
// opt-out, and this is its caller: the jobs that did not pass, run again
// for real, on the same commit.
//
// It costs the failing jobs and only them (row.ConfirmJobs), and only
// when a base branch is red -- which on a healthy deployment is rarely,
// and on a deployment with one flaky job is exactly when it is worth
// paying. What it buys is that a flake no longer spends an agent on a
// branch that is not broken.
func startBaseCIConfirmation(ctx context.Context, deps Deps, row model.BaseBranchCI, now time.Time) error {
	// A streak long enough is a second opinion already in hand: the same
	// job red on the tips before this one. Confirming on it here rather
	// than as well as re-running is what keeps the two from both
	// confirming, and both waking the steward, about one tip.
	if confirmed, err := confirmBaseCIStreak(ctx, deps, row, now); err != nil || confirmed {
		return err
	}
	if len(row.ConfirmJobs) == 0 {
		// Nothing to re-run: a workflow that would not parse, a run that
		// failed before it had jobs, or a row written by a build older
		// than this one. None of those are flaky in the way a job is, and
		// re-running "no jobs" would be a green that means nothing -- so
		// the failure stands as it is.
		return confirmBaseBranchCI(ctx, deps, row,
			"there were no failing jobs to re-run, so this failure is taken as it stands", now)
	}
	if row.ConfirmAttempts >= maxBaseCIConfirmations {
		return confirmBaseBranchCI(ctx, deps, row, fmt.Sprintf(
			"grain CI could not be asked a second time (%d attempt(s)): %s",
			row.ConfirmAttempts, row.ConfirmDetail), now)
	}
	// Under the same event the failing run was asked for, because a
	// confirmation planned under another one is a different question: for
	// a repository whose releases are off, the jobs that failed exist in
	// a push plan and may not exist in a release one at all. Read again
	// rather than remembered on the row -- the repository's settings are
	// where the answer lives, and a confirmation that outlives a change
	// to them should follow the change.
	policy, err := baseBranchCIWanted(ctx, deps.Store, row.Repo)
	if err != nil {
		return err
	}
	row.ConfirmAttempts++
	request := policy.request(row.Repo, row.Branch, row.Commit,
		"base-confirm:"+row.Repo.String()+":"+row.Branch)
	request.Jobs, request.Recheck = row.ConfirmJobs, true
	request.Priority = releaseGatePriority(deps, row.Repo, row.Branch)
	run, err := deps.CI.Start(ctx, request)
	if err != nil {
		// Recorded rather than returned: the attempt counts, so a request
		// that cannot be made at all -- a job that has since been renamed
		// out of the workflow, say -- ends in a confirmed failure within
		// maxBaseCIConfirmations cycles instead of erroring here forever
		// with the queue held and nobody filed to fix it.
		row.ConfirmDetail = truncate(err.Error(), 200)
		log.Printf("orchestrator: asking grain CI again about %s %s at %s: %v",
			row.Repo, row.Branch, shortSHA(row.Commit), err)
		return deps.Store.PutBaseBranchCI(ctx, row)
	}
	row.Confirmation, row.ConfirmRunID = model.BaseCIConfirming, run.ID
	row.ConfirmDetail = ""
	log.Printf("orchestrator: %s %s is red at %s: re-running %s to see whether that reproduces "+
		"before anything is merged or anybody is sent to fix it",
		row.Repo, row.Branch, shortSHA(row.Commit), strings.Join(row.ConfirmJobs, ", "))
	return deps.Store.PutBaseBranchCI(ctx, row)
}

// collectBaseCIConfirmation reads back the second run and decides what
// the branch's failure was.
//
//   - It failed too: the branch is red, and the repair is filed.
//   - It passed: the first failure did not reproduce. The block lifts
//     and nobody is sent anywhere. That is a judgement, not a proof --
//     a genuine break that the re-run happens not to hit would be let
//     through -- and it is the right one here because the very next
//     merge starts another post-merge run, so a real break is caught
//     again within one suite while a flake is not.
//   - It neither failed nor passed (errored, cancelled, or lost with the
//     daemon that was running it): no sample. The row goes back to
//     unconfirmed so the next cycle asks again, bounded by
//     maxBaseCIConfirmations.
func collectBaseCIConfirmation(ctx context.Context, deps Deps, row model.BaseBranchCI, now time.Time) error {
	run, ok := deps.CI.Get(row.ConfirmRunID)
	if !ok || run.Interrupted {
		return unconfirmedBaseBranchCI(ctx, deps, row, baseCILostDetail(ok))
	}
	if !run.Conclusion.Done() {
		return nil // still going, and the queue is still held
	}
	switch run.Conclusion {
	case ci.ConclusionFailure:
		return confirmBaseBranchCI(ctx, deps, row,
			"a second run of the failing jobs failed too: "+ciDetail(run), now)
	case ci.ConclusionSuccess:
		// The failing jobs passed on this tip after all, which ends any
		// streak they were on: the next red starts counting again.
		clearBaseCIStreak(&row)
		row.LastGreenCommit = row.Commit
		row.Confirmation = model.BaseCICleared
		row.ConfirmDetail = fmt.Sprintf("a second run of %s passed, so the failure above did not "+
			"reproduce and is being read as a flake", strings.Join(row.ConfirmJobs, ", "))
		log.Printf("orchestrator: %s %s failed at %s (%s) but a re-run of %s passed: "+
			"the merge queue is reopening and no repair is being filed",
			row.Repo, row.Branch, shortSHA(row.Commit), row.Detail, strings.Join(row.ConfirmJobs, ", "))
		return deps.Store.PutBaseBranchCI(ctx, row)
	default:
		return unconfirmedBaseBranchCI(ctx, deps, row, ciDetail(run))
	}
}

// confirmBaseBranchCI records that this failure is the branch's own and
// wakes system:pipeline-steward to decide what to do about it. The daemon
// files no repair of its own any more: filing one, reverting the merge
// that broke the branch, or leaving it to a person is the steward's call.
//
// It is also where every way of *not* getting a second opinion ends up,
// which is the one place this design has to choose a direction: a
// failure grain could not challenge is treated as real. The cost of
// being wrong that way is one steward wake on a branch that turns out to
// be fine; the cost of the other way is a red branch nobody is told
// about.
func confirmBaseBranchCI(ctx context.Context, deps Deps, row model.BaseBranchCI,
	detail string, now time.Time) error {

	row.Confirmation, row.ConfirmRunID = model.BaseCIConfirmed, ""
	row.ConfirmDetail = detail
	// One wake per red stretch: a streak that was already confirmed at an
	// earlier tip -- by a re-run there, or by the streak itself -- has had
	// the steward woken for it, and the same jobs still failing on the
	// next tip is not news to it (grain/task-1034). A red with no streak
	// to its name wakes it as it always has.
	wake := row.RedStreak == 0 || row.RedStreakWoken == ""
	if row.RedStreak > 0 && wake {
		row.RedStreakWoken = row.Commit
	}
	if err := deps.Store.PutBaseBranchCI(ctx, row); err != nil {
		return err
	}
	log.Printf("orchestrator: %s %s is confirmed red at %s (%s; %s)",
		row.Repo, row.Branch, shortSHA(row.Commit), row.Detail, detail)
	reason := fmt.Sprintf("%s %s is confirmed red at %s (%s)",
		row.Repo, row.Branch, shortSHA(row.Commit), truncate(row.Detail, 200))
	if streak := baseCIStreakSummary(row); streak != "" {
		reason += "; " + streak
	}
	if bisect := baseCIBisectRange(row); bisect != "" {
		reason += " (" + bisect + ")"
	}
	// Every confirmation raises it: a later tip confirmed red is a bump of
	// the same open notification, not a second one (DT-5).
	if nerr := raiseBaseRed(ctx, deps, row, fmt.Sprintf("at %s (%s)", shortSHA(row.Commit),
		truncate(row.Detail, 200)), now); nerr != nil {
		log.Printf("orchestrator: %v", nerr)
	}
	if !wake {
		log.Printf("orchestrator: not waking the pipeline steward again for %s %s at %s: "+
			"it was woken for this streak at %s", row.Repo, row.Branch, shortSHA(row.Commit),
			shortSHA(row.RedStreakWoken))
		return nil
	}
	wakePipelineSteward(ctx, deps, reason, now)
	return nil
}

// confirmBaseCIStreak confirms row red on the strength of its streak
// alone, where the streak has run for the deployment's
// model.Config.BaseCIRedStreakOr() tips and ends at the tip row is about
// (basecistreak.go). confirmed reports whether it did; when it did not,
// nothing was written.
func confirmBaseCIStreak(ctx context.Context, deps Deps, row model.BaseBranchCI,
	now time.Time) (confirmed bool, err error) {

	if row.RedStreak < 2 {
		return false, nil // no streak, and no need to read the setting
	}
	cfg, err := deps.Store.GetConfig(ctx)
	if err != nil {
		return false, fmt.Errorf("orchestrator: reading the base-branch red streak setting: %w", err)
	}
	if !baseCIStreakConfirms(row, cfg.BaseCIRedStreakOr()) {
		return false, nil
	}
	detail := fmt.Sprintf("%s (%s): the same job failing on consecutive tips is taken as the second run",
		baseCIStreakSummary(row), baseCIBisectRange(row))
	return true, confirmBaseBranchCI(ctx, deps, row, detail, now)
}

// unconfirmedBaseBranchCI puts a confirmation that produced no sample
// back where it started, so the next cycle asks again. The attempt is
// already counted, so this cannot loop.
func unconfirmedBaseBranchCI(ctx context.Context, deps Deps, row model.BaseBranchCI, detail string) error {
	row.Confirmation, row.ConfirmRunID = "", ""
	row.ConfirmDetail = detail
	return deps.Store.PutBaseBranchCI(ctx, row)
}

// ResolveRepairTemplate resolves the repair template to use for a repair or triage task.
// Precedence order:
//  1. Target repository's RepoConfig.RepairTemplateID (which holds what the
//     default team's repository config set until schema 23)
//  2. Target repository's declared default repair template (repo:template:<owner>:<name>:repair)
//  3. Team's Team.RepairTemplateID (if teamID is non-empty)
//  4. Deployment-wide Config.RepairTemplateID
//  5. System BaseRepairTemplateID (system:base-repair)
func ResolveRepairTemplate(ctx context.Context, store *model.Store, stateRepo model.RepoRef, repo model.RepoRef, teamID string) (*model.Template, error) {
	if store == nil {
		return nil, nil
	}
	// A level that names no template, or names one that does not exist,
	// falls through to the next; a store read that fails stops here. Every
	// getter below answers nil, nil for "not there", so an error is only
	// ever a read that did not happen -- and resolving past it would hand
	// the caller a template from further down the list (in the end the
	// system one, with its own AutoMerge, grants and network access) for
	// a task that is filed once and never again (grain/task-1456).
	resolve := func(id string) (*model.Template, error) {
		if id == "" {
			return nil, nil
		}
		tmpl, err := store.ResolveTemplate(ctx, id, stateRepo)
		if err != nil {
			return nil, fmt.Errorf("resolving template %s: %w", id, err)
		}
		return tmpl, nil
	}
	// 1. RepoConfig.RepairTemplateID
	repoCfg, err := store.GetRepoConfig(ctx, repo)
	if err != nil {
		return nil, err
	}
	if repoCfg != nil {
		if tmpl, err := resolve(repoCfg.RepairTemplateID); err != nil || tmpl != nil {
			return tmpl, err
		}
	}
	// 2. Default declared repair template on repo: DefaultRepairTemplateID(repo)
	if tmpl, err := resolve(model.DefaultRepairTemplateID(repo)); err != nil || tmpl != nil {
		return tmpl, err
	}
	// 3. Config.RepairTemplateID, which took the default team's own over
	// at schema 24.
	cfg, err := store.GetConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	if cfg != nil {
		if tmpl, err := resolve(cfg.RepairTemplateID); err != nil || tmpl != nil {
			return tmpl, err
		}
	}
	return nil, nil
}

func resolveRepairTemplate(ctx context.Context, deps Deps, repo model.RepoRef, teamID string) (*model.Template, error) {
	return ResolveRepairTemplate(ctx, deps.Store, deps.Config.settingsRepo(), repo, teamID)
}

// fileBaseBranchRepair files the one task that fixes a base branch grain
// CI has found red, and records it on the row so that exactly one is
// filed (model.BaseBranchCI.RepairTaskID). The row's id is also what
// exempts the repair from the pause syncEntry puts on automatic repairs
// of red pull requests while their base branch is broken. Merges are no
// longer held behind a red base branch (grain/task-471).
//
// It is the same bargain a red *branch* gets and has always got: an agent
// on the branch, one automatic attempt, and then a person. What is new is
// only that the branch in question is the one everything merges into --
// which is why the task is filed rather than a run requeued. The task
// whose merge broke it has merged and closed; there is nothing to send
// back.
//
// The daemon no longer calls this on its own when a branch is confirmed
// red: system:pipeline-steward owns that decision (confirmBaseBranchCI).
//
// Grants resolve repository and deployment defaults, plus any reads, writes
// and grants from the merged task that broke the branch (backport's reasoning
// exactly: this is work on the same repository that somebody already agreed a
// task could do), and always ensures the grain-ci grant is attached so the
// repair can run and prove its fix.
func fileBaseBranchRepair(ctx context.Context, deps Deps, row model.BaseBranchCI, now time.Time) error {
	if row.RepairTaskID != "" {
		return nil // already filed for this commit
	}
	// Grain files no repair for a repository whose triage it does not run
	// (model.TriageItem), nor for a branch it does not judge -- the
	// standing work switch, grain/task-3332. A person can still file one,
	// or mark their own task as fixing the branch.
	if off, err := baseBranchCIOff(ctx, deps.Store, row.Repo); err != nil || off {
		return err
	}
	if runs, err := runsStandingWork(ctx, deps.Store, row.Repo, model.TriageItem); err != nil || !runs {
		if err == nil {
			sayOnce("base-repair-off:"+row.Repo.String(), "orchestrator: not filing a repair of %s %s: %s",
				row.Repo, row.Branch, model.StandingWorkOffReason(row.Repo))
		}
		return err
	}
	tag := baseCIRepairTag(row.Repo, row.Branch, row.Commit)
	filed, err := deps.Store.HasTaskWithTag(ctx, tag)
	if err != nil {
		return fmt.Errorf("orchestrator: checking for an existing repair of %s %s: %w",
			row.Repo, row.Branch, err)
	}
	if filed {
		return nil
	}

	branchTag := BaseCIRepairBranchTag(row.Repo, row.Branch)
	// An open repair already there -- grain's own from an earlier commit,
	// or a task a person marked as fixing the branch -- is the one, and
	// is told about this commit instead (single flight, baserepairlink.go).
	existing, err := openBaseRepair(ctx, deps.Store, row.Repo, row.Branch, nil)
	if err != nil {
		return fmt.Errorf("orchestrator: checking for open branch repair of %s %s: %w", row.Repo, row.Branch, err)
	}
	if existing != nil {
		row.RepairTaskID = existing.ID
		commentBody := fmt.Sprintf("A subsequent commit on `%s` (`%s`) has also failed grain CI:\n\n"+
			"- Commit: `%s`\n"+
			"- Detail: %s\n",
			row.Branch, row.Repo, shortSHA(row.Commit), row.Detail)
		if row.ConfirmDetail != "" {
			commentBody += fmt.Sprintf("- Confirmation: %s\n", row.ConfirmDetail)
		}
		if _, err := deps.Store.AddComment(ctx, model.Comment{
			TaskID:    existing.ID,
			Author:    model.Attribution{Actor: baseCIRepairer},
			Body:      commentBody,
			CreatedAt: now,
		}); err != nil {
			return fmt.Errorf("orchestrator: updating existing repair task %s: %w", existing.ID, err)
		}
		// Red again: the next green is news to a person's task again.
		if err := forgetGreenNoted(ctx, deps.Store, existing.ID); err != nil {
			return err
		}
		return deps.Store.PutBaseBranchCI(ctx, row)
	}
	var merged *model.Task
	if row.MergedTaskID != "" {
		merged, err = deps.Store.GetTask(ctx, row.MergedTaskID)
		if err != nil {
			return fmt.Errorf("orchestrator: reading task %s: %w", row.MergedTaskID, err)
		}
	}
	teamID := ""
	tmpl, err := resolveRepairTemplate(ctx, deps, row.Repo, teamID)
	if err != nil {
		// Nothing is written yet, and the tag check above makes the next
		// cycle try again from the top.
		return fmt.Errorf("orchestrator: resolving the repair template for %s %s: %w",
			row.Repo, row.Branch, err)
	}
	if tmpl == nil {
		tmpl = model.SystemTemplate(model.BaseRepairTemplateID, deps.Config.settingsRepo())
	}

	id, err := deps.Store.NewTaskID(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: allocating an id for the repair of %s %s: %w",
			row.Repo, row.Branch, err)
	}
	repo := row.Repo
	grants, err := defaultTaskGrants(ctx, deps.Store, &repo, teamID)
	if err != nil {
		return fmt.Errorf("orchestrator: resolving default capabilities for repair of %s %s: %w", repo, row.Branch, err)
	}

	mergedTaskTitle := ""
	if merged != nil {
		mergedTaskTitle = merged.Title
	}
	values := map[string]string{
		"team":              teamID,
		"repo":              repo.String(),
		"branch":            row.Branch,
		"commit":            shortSHA(row.Commit),
		"detail":            row.Detail,
		"confirm_detail":    row.ConfirmDetail,
		"merged_task_id":    row.MergedTaskID,
		"merged_task_title": mergedTaskTitle,
	}

	title := fmt.Sprintf("Fix grain CI on %s %s", repo, row.Branch)
	body := baseBranchRepairBody(row, merged)
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

	if merged != nil {
		for _, r := range merged.Reads {
			if !slices.Contains(reads, r) {
				reads = append(reads, r)
			}
		}
		for _, w := range model.WritesBesides(merged.Writes, repo, row.Branch) {
			if !slices.ContainsFunc(writes, func(existing model.WriteTarget) bool {
				return existing.Repo == w.Repo && existing.Base == w.Base
			}) {
				writes = append(writes, w)
			}
		}
		for _, g := range merged.Grants {
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
		return fmt.Errorf("orchestrator: allocating order key for repair of %s %s: %w", repo, row.Branch, err)
	}

	repair := model.Task{
		ID:     id,
		Intent: model.IntentImplement,
		Title:  title,
		Body:   body,
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: baseCIRepairer},
			Reason:      model.ReasonDirect,
		},
		Approval:      &model.Attribution{Actor: baseCIRepairer},
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
	// A repair of the change that turned the branch red carries that
	// change's requester, as it carries its grants above: grain filed it,
	// but it is that task's work being fixed with that task's access.
	if merged != nil {
		model.InheritRequester(&repair, *merged)
	}
	if err := deps.Store.PutTask(ctx, repair); err != nil {
		return fmt.Errorf("orchestrator: filing the repair of %s %s: %w", repo, row.Branch, err)
	}
	// Recorded second on purpose: a crash between the two leaves a task
	// the tag check above finds, rather than a row naming a task that was
	// never filed -- and a row naming no repair is one the queue would
	// otherwise hold with nobody working on it.
	row.RepairTaskID = repair.ID
	if err := deps.Store.PutBaseBranchCI(ctx, row); err != nil {
		return err
	}
	log.Printf("orchestrator: filed %s to fix %s %s, red at %s", repair.ID, repo, row.Branch,
		shortSHA(row.Commit))
	return nil
}

// baseBranchRepairBody is what the agent fixing a broken base branch is
// told: which branch, which commit, what grain CI said, which merge
// brought it about, and why it is urgent.
//
// The last of those used to be "everything else is waiting behind you",
// and that stopped being true in grain/task-471 without this sentence
// being rewritten -- so the repair agent was told a hold that no longer
// exists, while baseBrokenStatus told every queued change the opposite
// (grain/task-424). The urgency is real and it is a different one: the
// queue goes on merging onto this branch, so every change that lands
// while it is red lands on a broken tree, and the next base-branch run
// says nothing new about whose fault that is.
func baseBranchRepairBody(row model.BaseBranchCI, merged *model.Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "grain CI is failing on `%s`, the branch changes merge into on %s, "+
		"at commit `%s`.\n\n", row.Branch, row.Repo, shortSHA(row.Commit))
	if row.Detail != "" {
		fmt.Fprintf(&b, "The run reported: %s\n\n", row.Detail)
	}
	if row.ConfirmDetail != "" {
		// What makes this a red branch rather than a red run: grain asks
		// again, with the cache bypassed, before it spends an agent. The
		// answer belongs in the task that answer filed -- both because it
		// says the failure is reproducible, and because when it says the
		// opposite (grain could not get a second opinion) whoever picks
		// this up should know that before they start bisecting.
		fmt.Fprintf(&b, "Asked a second time: %s\n\n", row.ConfirmDetail)
	}
	if merged != nil {
		fmt.Fprintf(&b, "It went red immediately after grain task %s, %q, merged -- the first "+
			"place to look, though not necessarily the whole cause: two changes each green on "+
			"their own branch can be broken together, which is what this check catches.\n\n",
			merged.ID, merged.Title)
	}
	fmt.Fprintf(&b, "Fix it on a branch of your own and open a pull request against `%s`.\n\n", row.Branch)
	fmt.Fprintf(&b, "You are repairing the root base branch itself. Take a holistic approach: "+
		"inspect and resolve all failing jobs in the suite, rather than stopping after the first fix. "+
		"Disregard hints about \"shared base faults\" or failures occurring on other branches—this repair "+
		"task is the source of truth for `%s`, and no other repair will fix them for you. Revert the "+
		"merge above if that is the right answer, or repair the broken suites directly. Iterate and "+
		"re-run CI until all required jobs in the suite pass.\n\n", row.Branch)
	b.WriteString(model.RedCIProcedureReminder + "\n\n")
	fmt.Fprintf(&b, "The merge queue is still merging green changes into `%s` -- a red base "+
		"branch is not a gate on merging, because a gate whose own failure blocks the only route "+
		"to fixing it is a deadlock rather than a control. What does wait is work that would only "+
		"inherit this red: new tasks targeting `%s`, merge-queue repairs on it, and pull requests "+
		"whose only failures are the ones `%s` has too all depend on this task, and are released "+
		"when it closes. grain closes it by itself as soon as `%s`'s own grain CI is green again, "+
		"whoever fixed it. So every change waiting for %s is waiting on you. That is the hurry.\n",
		row.Branch, row.Branch, row.Branch, row.Branch, row.Repo)
	return b.String()
}

// brokenBaseBranch is a branch's own recorded verdict where it is a
// failure, and nil where there is none -- a branch nothing has merged into
// under grain's eye, a run still going, or any conclusion but failure
// (model.BaseBranchCI.Broken).
//
// It is read for what a row should *say*, and for nothing else
// (grain/task-471). It used to be the gate syncEntry consulted before
// merging, which is strategy wired into the merge predicate: a branch
// being red is a fact about the branch, not about whether any one change
// was eligible, and as a gate its own failure blocked the only route to
// fixing it -- "a broken release means we can't officially push out new
// changes; if that also means we cannot update the code then there is
// literally no way to fix the release". The exemption the design gave the
// repair task's own pull request was the admission, one lane wide and
// dependent on grain having filed a repair and matched its id.
//
// What the run is instead is information, which is what it is good at: it
// tells "this batch broke `main`" from "`main` was already broken", and it
// is what a repair task is filed from. See mergepredicate.go for what does
// decide a merge now, and docs/merge-queue-behind.md for the reasoning
// this replaced.
func brokenBaseBranch(ctx context.Context, store *model.Store, repo model.RepoRef,
	branch string) (*model.BaseBranchCI, error) {

	if store == nil || branch == "" {
		return nil, nil
	}
	row, err := store.GetBaseBranchCI(ctx, repo, branch)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: reading base branch CI for %s %s: %w", repo, branch, err)
	}
	if !row.Broken() {
		return nil, nil
	}
	// A branch whose check is switched off is not red to anybody, even in
	// the cycle between the switch and the reconciler standing its row
	// down (standDownBaseBranchCI): no verdict is expected of it.
	if off, err := baseBranchCIOff(ctx, store, repo); err != nil || off {
		return nil, err
	}
	return row, nil
}
