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
	"slices"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/cifault"
	"github.com/ai-on-gke/grain/pkg/forge"
	"github.com/ai-on-gke/grain/pkg/model"
)

// Deps.CI is grain's own CI as this package takes it: ci.Runner, the
// whole of what the CI service answers (docs/design/grit-split.md#GS-1),
// rather than *ci.Service, so the service can move out of the process
// without this package changing. cmd/grain hands in its own ciGate.
//
// An interface rather than the concrete service for the reason
// Deps.Framework is a factory: a deployment that runs no CI hands in nil
// and every path here becomes a no-op, and a test drives the whole
// trigger/collect loop with a stub (embedding ci.NopRunner for what it
// does not care about) and no sandbox backend in sight.
//
// A run Get answers for may still be one nothing is running:
// ci.Run.Interrupted, which collectCIRun treats as no answer at all.
// Everything this package cancels is grain's own, so every Cancel here
// passes ci.CancelBySystem, and every one of them is therefore a run
// ciReplacementFor is willing to start again.

// liveBranchCIRuns returns every run on repo and branch that is still in
// flight, where the runner can list or find its runs.
func liveBranchCIRuns(runner ci.Runner, repo, branch string) []ci.Run {
	if runner == nil {
		return nil
	}
	runs := runner.Find(ci.RunFilter{Repo: repo, Ref: branch, Descending: true}, 200).Runs
	var out []ci.Run
	for _, r := range runs {
		if !strings.EqualFold(r.Repo, repo) || r.Ref != branch {
			continue
		}
		if r.Conclusion.Done() || r.Interrupted || r.ID == "" {
			continue
		}
		out = append(out, r)
	}
	return out
}

// cancelSupersededBranchRuns cancels live runs on repo and branch other than
// keepRunID, skipping any run IDs already in seen.
func cancelSupersededBranchRuns(runner ci.Runner, repo, branch, headSHA, keepRunID string, seen map[string]bool) {
	if runner == nil {
		return
	}
	if seen == nil {
		seen = map[string]bool{}
	}
	if keepRunID != "" {
		seen[keepRunID] = true
	}
	for _, r := range liveBranchCIRuns(runner, repo, branch) {
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		reason := ci.SupersededByRunReason(keepRunID)
		if headSHA != "" && r.Commit != "" && r.Commit != headSHA {
			reason = fmt.Sprintf("superseded: %s moved to %s while this run was judging %s",
				branch, shortSHA(headSHA), shortSHA(r.Commit))
		}
		runner.Cancel(r.ID, reason, ci.CancelBySystem)
	}
}

// ciSharedFaults is what this deployment's CI is failing the same way on
// more than one branch right now (pkg/ci/sharedfault.go), or nothing at
// all where this deployment runs no CI.
//
// Computed rather than cached, because it is computed once per verdict:
// a run concluding red is a rare event beside the CI pane's five-second
// poll, and the cost is a read of the recent failing runs.
func ciSharedFaults(runner ci.Runner, now time.Time) []ci.SharedFault {
	if runner == nil {
		return nil
	}
	return ci.RecentSharedFaults(runner, now, ci.DefaultFaultWindow, 0)
}

// faultFixLookup answers "which open pull request already fixes this
// fault" for one verdict comment -- ciFaultFixes in a deployment, and
// nil wherever the answer cannot be had at all: no store, no GitHub
// client, or a caller that only wants the wording.
type faultFixLookup func(ci.SharedFault) []cifault.Fix

// of is the lookup made safe to call on a nil one, since a comment is
// worth posting without it: the fault, the other branches and the files
// they all named are grain's own facts, and only the name of the fix
// depends on GitHub answering.
func (f faultFixLookup) of(fault ci.SharedFault) []cifault.Fix {
	if f == nil {
		return nil
	}
	return f(fault)
}

// ciFaultFixes is that lookup over this deployment's own store and
// GitHub client (pkg/cifault, the same judgement the CI pane and
// wait_for_checks report). It is built per verdict and called at most
// once, for the one fault the comment ends up naming, so a red build
// that shares nothing costs nothing.
func ciFaultFixes(ctx context.Context, deps Deps) faultFixLookup {
	if deps.Store == nil {
		return nil
	}
	files := deps.CIFaultFiles
	if files == nil {
		if deps.Client == nil {
			return nil
		}
		files = deps.Client
	}
	return func(fault ci.SharedFault) []cifault.Fix {
		return cifault.Fixes(ctx, deps.Store, files, fault)
	}
}

// reconcileCI is grain CI's level-triggered controller: for each task with
// an open pull request, make sure its current head has a required full-suite
// run, then collect that run's verdict where the merge queue reads it.
//
// # Why a reconciler and not a line in the finish path
//
// wait_for_checks still starts (or joins) a run immediately when an agent
// asks for feedback, and can choose a small job set while iterating. The
// reconciler is the backstop for the required unselected run: an agent that
// exits without asking, or a merge or push that moves the branch afterwards,
// cannot leave the merge queue waiting forever on a run nobody started.
//
// # What the verdict then holds
//
// The merge. A red grain CI run recorded on a task makes its pull
// request PrFailing and earns it the repair the merge queue gives any
// other broken head, and a run still going holds the merge as PrPending
// -- see withGrainCI, which is where that policy is written down and
// where each of the questions it turns on is answered: a repository that
// declares no workflows passes (an empty plan is a success), while a
// verdict about some other commit or no verdict at all leaves the
// required gate pending rather than calling the tree broken.
//
// "Declares no workflows" is asked of the base as well as the head: the
// required run is started with ci.Request.GateOnBase, so a head that
// would run fewer jobs than its base's workflows require -- deleting
// them all included -- fails instead of planning nothing and passing
// (pkg/ci/basegate.go, bwsalmon/grain#2390).
//
// Nothing here reports it to GitHub, though, and that has not changed:
// no commit status and no check run. The verdict travels on the task's
// own record and is read from there (grainCIVerdictOf), so what grain
// CI holds is decided by this deployment's own rules rather than by
// whatever GitHub happens to require of a branch.
//
// # Which tasks
//
// Those with an open pull request (Store.OpenPullRequestLinks -- the same
// set SyncPullRequests walks) that hold the grain-ci capability. The
// capability is the switch (pkg/capability/grainci), read off the task's
// own resolved grants, which is where a deployment-wide or per-repo
// default has already landed by the time a task exists (ui.Client records
// those as model.GrantByDefault at creation). The pull request is the
// scope: a branch somebody may merge is exactly the branch "does it
// pass?" is asked about, and it keeps a feature that spends sandboxes
// from spending them on every branch grain has ever pushed.
func reconcileCI(ctx context.Context, deps Deps, now time.Time) error {
	if deps.CI == nil {
		return nil // this deployment runs no CI
	}
	links, err := deps.Store.OpenPullRequestLinks(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading open pull request links: %w", err)
	}
	var errs []error
	for _, link := range links {
		task, err := deps.Store.GetTask(ctx, link.TaskID)
		if err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: reading task %s: %w", link.TaskID, err))
			continue
		}
		if task == nil {
			continue
		}
		if err := reconcileTaskCI(ctx, deps, *task, now); err != nil {
			errs = append(errs, err)
		}
	}
	if deps.CI != nil {
		for _, run := range deps.CI.List() {
			if run.Interrupted || run.RequestedBy == "" {
				continue
			}
			running := !run.Conclusion.Done()
			if !running && run.Conclusion != ci.ConclusionCancelled {
				continue
			}
			obs, err := deps.Store.GetObservation(ctx, run.RequestedBy)
			if err != nil {
				errs = append(errs, fmt.Errorf("orchestrator: reading %s's observation: %w", run.RequestedBy, err))
				continue
			}
			if obs == nil || obs.ClosedAt == nil || obs.PrMergedAt != nil {
				continue
			}
			if inPlace, err := requestedByInPlaceReview(ctx, deps.Store, run.RequestedBy); err != nil {
				errs = append(errs, err)
				continue
			} else if inPlace {
				// The reviewed branch's run, whichever task asked first
				// (ci.Service.Start hands an identical live plan to every
				// asker): a review closing says nothing about it.
				continue
			}
			if running {
				deps.CI.Cancel(run.ID, fmt.Sprintf("task %s was closed", run.RequestedBy), ci.CancelBySystem)
			}
			if status := ciStatusOf(obs); status != nil && status.RunID == run.ID && !status.Done() {
				task, err := deps.Store.GetTask(ctx, run.RequestedBy)
				if err != nil {
					errs = append(errs, fmt.Errorf("orchestrator: reading task %s: %w", run.RequestedBy, err))
					continue
				}
				if task == nil {
					continue
				}
				if err := collectCIRun(ctx, deps, *task, *status, now); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	return errors.Join(errs...)
}

// requestedByInPlaceReview reports whether taskID is a review working on
// the reviewed change's own branch (reviewsInPlace), whose runs -- any
// it started before it stopped owning CI, or any it was handed by
// ci.Service.Start's dedupe -- judge that change's branch, not its own.
func requestedByInPlaceReview(ctx context.Context, store *model.Store, taskID string) (bool, error) {
	task, err := store.GetTask(ctx, taskID)
	if err != nil {
		return false, fmt.Errorf("orchestrator: reading task %s: %w", taskID, err)
	}
	if task == nil {
		return false, nil
	}
	_, inPlace := reviewsInPlace(*task)
	return inPlace, nil
}

// ciRetryDelay and ciMaximumAttempts bound reconcileTaskCI's retries of an
// errored or cancelled verdict (ciReplacementFor).
const (
	ciRetryDelay      = 5 * time.Minute
	ciMaximumAttempts = 4
)

// reconcileTaskCI ensures and collects the required full-suite run for this
// task's current branch head. An agent's unselected wait_for_checks call
// records the same status earlier; selected runs are never recorded here and
// therefore never satisfy the merge gate.
//
//	no status
//	    -> start and record the required run
//	a status about an older commit
//	    -> cancel it if still running; start and record one for the new head
//	a status with no conclusion
//	    -> ask the service; record and report a verdict once there is one
//	a status with a conclusion, about this commit
//	    -> nothing, unless it is a verdict grain owes a replacement for
//	       (ciReplacementFor); the answer is already where it belongs
//
// Errored and cancelled conclusions are retried after five minutes, up to
// four total attempts. The durable attempt count and finish time make this
// idempotent: repeated reconciliation neither starts early nor exceeds the
// cap.
func reconcileTaskCI(ctx context.Context, deps Deps, task model.Task, now time.Time) error {
	if deps.CI == nil || task.Target == nil {
		return nil
	}
	// A review working on the reviewed change's own branch owns no CI
	// status: the branch, and the one run judging its head, are the
	// reviewed task's (model.Task.CIOwner). Starting, retrying or
	// collecting a run here too would be a second suite on the same
	// commit -- and each would cancel the other's as superseded.
	if _, inPlace := reviewsInPlace(task); inPlace {
		return nil
	}
	// A paused task starts no CI (reconcileTaskPauses cancels what it had
	// in flight); resuming it lets the next cycle start a fresh run.
	if taskPaused(ctx, deps.Store, task.ID) {
		return nil
	}
	if !grainci.Granted(task.Grants) {
		required, err := taskRequiresGrainCI(ctx, deps.Store, task)
		if err != nil {
			return fmt.Errorf("orchestrator: checking Grain CI policy for task %s: %w", task.ID, err)
		}
		if required {
			if err := deps.Store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
				if !grainci.Granted(t.Grants) {
					t.Grants = append(t.Grants, model.Grant{
						Capability: grainci.CapabilityName,
						Via:        model.GrantByDefault,
					})
				}
				return nil
			}); err != nil {
				return err
			}
			task.Grants = append(task.Grants, model.Grant{
				Capability: grainci.CapabilityName,
				Via:        model.GrantByDefault,
			})
			// Logged rather than returned: the grant has been written, and
			// failing here would cost this task its run over a marker the
			// no-status path below clears again.
			if err := deps.Store.ClearGrantWait(ctx, task.ID); err != nil {
				log.Printf("orchestrator: clearing the grain-ci grant wait on %s: %v", task.ID, err)
			}
		} else {
			return nil
		}
	}
	obs, err := deps.Store.GetObservation(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("orchestrator: reading %s's observation: %w", task.ID, err)
	}
	// A closed task's branch is not worth a sandbox: nobody is going to
	// merge it, and SyncPullRequests has already stopped watching it
	// (noteOrphanedPullRequests). Any run still in flight for an unmerged
	// closed task is cancelled rather than left to spend CI sandboxes on a
	// branch nothing will ever merge.
	if obs != nil && obs.ClosedAt != nil {
		if obs.PrMergedAt == nil {
			if status := ciStatusOf(obs); status != nil && !status.Done() {
				deps.CI.Cancel(status.RunID, fmt.Sprintf("task %s was closed", task.ID), ci.CancelBySystem)
				return collectCIRun(ctx, deps, task, *status, now)
			}
		}
		return nil
	}
	branch := task.BranchName()
	head, err := deps.Client.GetBranchHead(task.Target.Owner, task.Target.Name, branch)
	if err != nil {
		return fmt.Errorf("orchestrator: reading %s's head on %s: %w", branch, task.Target, err)
	}
	if head == nil || head.SHA == "" {
		return nil // no branch (yet, or any more): nothing to test
	}
	var retargeted bool
	if updated, changed, err := retargetTaskBase(ctx, deps.Store, deps.Client, task, ""); err == nil && changed {
		task = updated
		retargeted = true
	}
	status := ciStatusOf(obs)
	if status == nil {
		// A task corrected after being parked for a missing grain-ci grant
		// is no longer waiting on configuration. Unlike agent dispatch, CI
		// does not call StartRun, so it clears that admission marker here.
		if wait, err := deps.Store.GrantWaitOf(ctx, task.ID); err != nil {
			return err
		} else if wait != nil && wait.Capability == grainci.CapabilityName {
			if err := deps.Store.ClearGrantWait(ctx, task.ID); err != nil {
				return err
			}
		}
		return startRequiredCIRun(ctx, deps, task, branch, head.SHA, nil, now)
	}

	switch {
	case status.Commit != head.SHA:
		seen := map[string]bool{}
		if !status.Done() && status.RunID != "" {
			seen[status.RunID] = true
			deps.CI.Cancel(status.RunID, fmt.Sprintf(
				"superseded: %s moved to %s while this run was judging %s",
				branch, shortSHA(head.SHA), shortSHA(status.Commit)), ci.CancelBySystem)
		}
		for _, r := range liveBranchCIRuns(deps.CI, task.Target.String(), branch) {
			if seen[r.ID] || r.Commit == head.SHA {
				continue
			}
			seen[r.ID] = true
			deps.CI.Cancel(r.ID, fmt.Sprintf(
				"superseded: %s moved to %s while this run was judging %s",
				branch, shortSHA(head.SHA), shortSHA(r.Commit)), ci.CancelBySystem)
		}
		return startRequiredCIRun(ctx, deps, task, branch, head.SHA, seen, now)
	case status.Done():
		if retargeted && status.Conclusion == string(ci.ConclusionErrored) {
			return startRequiredCIRun(ctx, deps, task, branch, head.SHA, nil, now)
		}
		// A run a person cancelled while holding the task until a time
		// stays cancelled until then; once the hold has expired the
		// replacement below starts it again (operatorCIHold).
		if operatorCIHold(task, *status, now) {
			return nil
		}
		// A known flaky job failing alone gets one more run per commit
		// (retriggerKnownFlake).
		if started, err := retriggerKnownFlake(ctx, deps, task, *status, now); started || err != nil {
			return err
		}
		replacement := ciReplacementFor(*status)
		if replacement == forge.GrainCIReplacementDue && ciRetryReady(*status, now) {
			return retryRequiredCIRun(ctx, deps, task, branch, head.SHA, *status,
				ciAttemptsOf(*status), now)
		}
		if replacement == forge.GrainCIReplacementSpent && ciRetryReady(*status, now) {
			granted, err := ciRequirementsNowGranted(ctx, deps, task, *status)
			if err != nil {
				return err
			}
			if granted {
				log.Printf("orchestrator: task %s: grain CI at %s errored on configuration %s now provides; "+
					"starting it again past its %d attempts", task.ID, shortSHA(head.SHA), task.Target, ciAttemptsOf(*status))
				return retryRequiredCIRun(ctx, deps, task, branch, head.SHA, *status,
					ciAttemptsOf(*status), now)
			}
		}
		return nil // the verdict on this tree is already recorded
	default:
		return collectCIRun(ctx, deps, task, *status, now)
	}
}

// ciReplacementFor says whether grain owes this commit another run, for a
// verdict that has concluded against the commit still at the head of the
// branch. It is the whole of the retry policy, in one place because two
// callers have to agree about it: reconcileTaskCI acts on it, and
// grainCIVerdictOf puts it on the merge queue's row, where a sentence
// promising a run nothing was going to start is worse than no sentence.
//
// # Errored
//
// A run that never got a machine to run a job in has said nothing about
// the tree, so trying again is the only way to find anything out.
//
// # Cancelled
//
// A cancellation is nearly always grain's own doing rather than anything
// the branch did -- a job that ran past ci.DefaultJobTimeout, a daemon
// stopping under the run, a supersede -- which makes it *more* deserving
// of another run than an errored one, not less. Until this existed it got
// none: `cancelled` is Done() and is not `errored`, so it fell through to
// "the verdict is already recorded" while withGrainCI held the merge
// pending on a verdict that could never arrive. grain/task-149 sat there
// for days, and grain/task-264 sat there again after grain/task-170 had
// made the row *say* so -- a legible dead end is still a dead end.
//
// A supersede is not the awkward case it sounds like. By the time control
// reaches here reconcileTaskCI has already dealt with a head that moved
// (status.Commit != head.SHA cancels the stale run and starts one for the
// new commit), so a cancellation still being judged on this arm is one
// about the commit that is *currently* at the head -- and a superseded
// run for the current commit is exactly the run that ought to be replaced.
//
// A person pressing cancel is replaced too, and on purpose. CancelledBy
// still records whose hand was on it (ci.CancelSource), but it no longer
// decides anything here: for a while it made the replacement
// GrainCIReplacementDeclined, and that was a dead end -- a run cancelled
// because it was hung, or because a second run was overlapping it ("new
// run running"), left the task in waiting_ci with no verdict and nothing
// that would ever produce one (task 285, 2026-10-23). A person cancelling
// a run is stopping that run; a person who means to stop the task from
// merging withdraws auto-merge or closes the task, and both of those are
// read elsewhere. The replacement still waits ciRetryDelay like any other,
// so a cancel is not answered with a new run in the same second.
//
// Bounded either way by ciMaximumAttempts: a cancellation that keeps
// happening -- a job that cannot fit inside the timeout at all -- stops
// being retried and becomes a state a person owns, which the merge
// queue's own stall deadline then hands over (escalateStalledChecks).
func ciReplacementFor(status model.CIStatus) forge.GrainCIReplacement {
	switch status.Conclusion {
	case string(ci.ConclusionErrored), string(ci.ConclusionCancelled):
	default:
		return forge.GrainCIReplacementNone
	}
	if ciAttemptsOf(status) >= ciMaximumAttempts {
		return forge.GrainCIReplacementSpent
	}
	return forge.GrainCIReplacementDue
}

// ciRetryReady is the other half of the decision, and the half the row
// does not render: a replacement that is owed is still not started until
// ciRetryDelay has passed, so a deployment that is broken for a minute is
// not answered with four runs in four cycles.
//
// A verdict with no finish time is ready now. That cannot happen through
// collectCIRun, which stamps one on every verdict it records, but reading
// it as "never retry" -- which is what the errored path did before this
// was written down -- would be the same silence this whole change is
// about.
func ciRetryReady(status model.CIStatus, now time.Time) bool {
	if status.FinishedAt == nil {
		return true
	}
	return !now.Before(status.FinishedAt.Add(ciRetryDelay))
}

// ciUnmetRequirement matches the sentence ci.unmetRequirements writes
// for a capability or release secret the repository's configuration does
// not grant -- the one way a run errors that a person fixes in Settings
// rather than on a machine. Its other sentences (an untrusted commit, a
// push run that never carries release secrets) are not configuration and
// deliberately do not match.
var ciUnmetRequirement = regexp.MustCompile(
	`requires the (capability|release secret) "([^"]+)" and this deployment does not provide it for`)

// ciRequirementsNowGranted is the one way past ciMaximumAttempts: an
// errored verdict whose runs could not start because the repository
// lacked a capability or release secret, where the repository's
// configuration now grants every one of them. Retrying a run the
// deployment could not do is spent after three tries because nothing
// was expected to change; an operator adding the capability is exactly
// that change, and without this the commit stayed errored until somebody
// pushed to it or forced the merge (grain/task-480).
//
// It cannot loop. The run it starts reads the same configuration this
// does (daemon.CiRepoCapabilities), so it no longer errors with these
// sentences; if it errors for any other reason, nothing here matches and
// the commit is spent again. ciMaximumAttempts more runs is the backstop
// for a deployment where the two reads somehow disagree.
//
// The sentences come from the run's own jobs where the run is still
// remembered, and from the verdict's detail -- which keeps the first of
// them -- where it has been pruned.
func ciRequirementsNowGranted(ctx context.Context, deps Deps, task model.Task, status model.CIStatus) (bool, error) {
	if status.Conclusion != string(ci.ConclusionErrored) || task.Target == nil ||
		ciAttemptsOf(status) >= 2*ciMaximumAttempts {
		return false, nil
	}
	texts := []string{status.Detail}
	if deps.CI != nil {
		if run, ok := deps.CI.Get(status.RunID); ok {
			texts = append(texts, run.Error)
			for _, j := range run.Jobs {
				texts = append(texts, j.Error)
			}
		}
	}
	var capabilities, secrets []string
	for _, text := range texts {
		for _, m := range ciUnmetRequirement.FindAllStringSubmatch(text, -1) {
			if m[1] == "capability" {
				capabilities = append(capabilities, m[2])
			} else {
				secrets = append(secrets, m[2])
			}
		}
	}
	if len(capabilities) == 0 && len(secrets) == 0 {
		return false, nil
	}
	eff, err := deps.Store.EffectiveRepoConfig(ctx, *task.Target)
	if err != nil {
		return false, err
	}
	for _, id := range capabilities {
		if !slices.Contains(eff.CICapabilities, id) {
			return false, nil
		}
	}
	for _, name := range secrets {
		if !slices.Contains(eff.ReleaseSecrets, name) {
			return false, nil
		}
	}
	return true, nil
}

// ciAttemptsOf is how many runs this commit has had, counting the one the
// status holds. Zero is a status written before retry bookkeeping existed,
// which is one run.
func ciAttemptsOf(status model.CIStatus) int {
	if status.Attempts == 0 {
		return 1
	}
	return status.Attempts
}

// startRequiredCIRun starts the complete push suite for sha and records the
// pending verdict before the merge reconciler runs. ci.Service.Start returns
// an identical in-flight plan when wait_for_checks won the race, so both
// callers converge on one run rather than spending two sets of sandboxes.
func startRequiredCIRun(ctx context.Context, deps Deps, task model.Task,
	branch, sha string, alreadyCancelled map[string]bool, now time.Time) error {

	baseCommit := ""
	if task.Target != nil {
		if baseCI, err := deps.Store.GetBaseBranchCI(ctx, *task.Target, task.Base); err == nil && baseCI != nil {
			baseCommit = baseCI.Commit
		}
	}
	run, err := deps.CI.Start(ctx, ci.Request{
		Repo: task.Target.String(), Ref: branch, Commit: sha,
		Event: ci.EventPush, RequestedBy: task.ID,
		GateOnBase: true, BaseRef: task.Base, BaseCommit: baseCommit,
	})
	if err != nil {
		return fmt.Errorf("orchestrator: starting grain CI for task %s at %s: %w",
			task.ID, shortSHA(sha), err)
	}
	cancelSupersededBranchRuns(deps.CI, task.Target.String(), branch, sha, run.ID, alreadyCancelled)
	started := run.StartedAt
	if started == nil {
		started = &now
	}
	return observeField(ctx, deps.Store, task.ID, now, func(o *model.Observation) {
		o.CI = &model.CIStatus{RunID: run.ID, Commit: run.Commit, StartedAt: started, Attempts: 1}
	})
}

func retryRequiredCIRun(ctx context.Context, deps Deps, task model.Task,
	branch, sha string, previous model.CIStatus, attempts int, now time.Time) error {

	baseCommit := ""
	if task.Target != nil {
		if baseCI, err := deps.Store.GetBaseBranchCI(ctx, *task.Target, task.Base); err == nil && baseCI != nil {
			baseCommit = baseCI.Commit
		}
	}
	run, err := deps.CI.Start(ctx, ci.Request{
		Repo: task.Target.String(), Ref: branch, Commit: sha,
		Event: ci.EventPush, RequestedBy: task.ID,
		GateOnBase: true, BaseRef: task.Base, BaseCommit: baseCommit,
	})
	if err != nil {
		return fmt.Errorf("orchestrator: retrying grain CI for task %s at %s: %w",
			task.ID, shortSHA(sha), err)
	}
	cancelSupersededBranchRuns(deps.CI, task.Target.String(), branch, sha, run.ID, nil)
	started := run.StartedAt
	if started == nil {
		started = &now
	}
	return observeField(ctx, deps.Store, task.ID, now, func(o *model.Observation) {
		if o.CI == nil || o.CI.RunID != previous.RunID || o.CI.Conclusion != previous.Conclusion {
			return
		}
		o.CI = &model.CIStatus{
			RunID: run.ID, Commit: run.Commit, StartedAt: started, Attempts: attempts + 1,
			PreviousConclusion: previous.Conclusion, PreviousDetail: previous.Detail,
		}
	})
}

// ciStatusOf is Observation.CI off a possibly-nil observation, which is
// what a task nothing has ever observed reads as.
func ciStatusOf(obs *model.Observation) *model.CIStatus {
	if obs == nil {
		return nil
	}
	return obs.CI
}

// collectCIRun reads back a run this task is already waiting on and, once
// it has ended, writes the verdict onto the task and says so in the
// places a person reads.
//
// A run nobody is going to conclude is not a verdict, and there are two
// ways to be one. The service may not have the run at all -- it was
// pruned past ci.DefaultStoredRunHistory, or this deployment keeps its
// runs in memory and has restarted -- or it may have it stamped
// interrupted, which is what a stored run left in flight by a daemon
// that stopped is turned into at the next startup
// (ci.StoredRunStore.RecoverInterrupted). Either way the task is waiting
// on an answer that is never coming.
//
// Forgetting the status leaves the task pending until the next reconciliation
// starts its replacement. It is also why "interrupted" is a fact of its own
// rather than just a cancelled conclusion: a cancellation somebody asked for
// is a verdict and stands, and this is the one kind that is not.
//
// Now that the verdict holds the merge (withGrainCI), forgetting one is
// worth being deliberate about: for the cycle between forgetting and
// starting again the pull request has no grain CI answer at all, which
// that policy reads as nothing known rather than as a pass or a
// failure. Required grain CI makes that absence pending, rather than
// allowing a merge on an answer that was never given.
func collectCIRun(ctx context.Context, deps Deps, task model.Task,
	status model.CIStatus, now time.Time) error {

	run, ok := deps.CI.Get(status.RunID)
	if status.RecheckOf != "" {
		// An uncached recheck of a run that failed (startCIRecheck). Lost,
		// it leaves the first failure standing rather than forgetting the
		// status: forgetting would start the suite again, read the cached
		// red, and recheck it again.
		lost := !ok || run.Interrupted
		if !lost && !run.Conclusion.Done() {
			return nil
		}
		return collectCIRecheck(ctx, deps, task, status, run, lost, now)
	}
	if !ok || run.Interrupted {
		why := "is no longer known to this daemon"
		if ok {
			why = "was interrupted by the daemon that was running it stopping"
		}
		log.Printf("orchestrator: task %s: grain CI run %s %s; "+
			"forgetting it so the required run for %s can be started again", task.ID, status.RunID, why, shortSHA(status.Commit))
		return observeField(ctx, deps.Store, task.ID, now, func(o *model.Observation) { o.CI = nil })
	}
	if !run.Conclusion.Done() {
		return nil // still going
	}
	// Red is asked about twice before it is this branch's: the jobs that
	// failed for reasons of their own are re-run once, uncached, and the
	// verdict waits for that answer (recheck.go). The status stays
	// unconcluded meanwhile, so the merge queue reads it as still running
	// rather than spending a repair on a flake.
	if run.Conclusion == ci.ConclusionFailure {
		if started, err := startCIRecheck(ctx, deps, task, status, run, now); started || err != nil {
			return err
		}
	}
	// An errored run that errored because a sandbox's channel was lost is
	// re-run the same way, at once and uncached (infraverdict.go).
	if run.Conclusion == ci.ConclusionErrored {
		if started, err := startChannelLostRecheck(ctx, deps, task, status, run, now); started || err != nil {
			return err
		}
	}

	finished := run.FinishedAt
	if finished == nil {
		finished = &now
	}
	detail := ciDetail(run)

	// Recorded before it is announced, and announced only if this call is
	// the one that recorded it -- noteBaseRetarget's own discipline, for
	// the same reason: a cycle that fails between the two would otherwise
	// come back next tick and say the same thing a second time. Losing a
	// comment to a GitHub failure is the better of the two, since the
	// verdict itself is on the task either way.
	recorded := false
	if err := observeField(ctx, deps.Store, task.ID, now, func(o *model.Observation) {
		if o.CI == nil || o.CI.RunID != status.RunID || o.CI.Done() {
			return
		}
		o.CI.Conclusion = string(run.Conclusion)
		o.CI.FinishedAt = finished
		o.CI.Detail = detail
		// Who stopped it, where somebody did, kept beside the verdict so
		// the retry decision outlives the run itself (ciReplacementFor).
		o.CI.CancelledBy = string(run.CancelledBy)
		recorded = true
	}); err != nil {
		return err
	}
	if !recorded {
		return nil
	}
	log.Printf("orchestrator: task %s: grain CI run %s: %s at %s -- %s",
		task.ID, run.ID, run.Conclusion, shortSHA(status.Commit), detail)

	return announceCIRun(ctx, deps, task, run, status.PreviousConclusion, status.PreviousDetail, now)
}

// announceCIRun puts the verdict in front of the two audiences a task
// has, on different terms for each.
//
// The task's own conversation gets one terse comment per distinct verdict,
// green or red. A task dispatched again reads that thread back in its prompt,
// so copying job lists and logs into it makes every later turn pay for
// diagnostics that are already available from wait_for_checks and the run
// named by model.CIStatus. An automatic retry with the same conclusion and
// detail adds no information, so it is recorded without another comment;
// recovery or a changed error remains useful history.
//
// The pull request gets one only when the run did not pass. A reviewer
// wants to be told about red, and a green comment on every push is the
// noise GitHub's own checks UI exists to avoid -- it reports a passing
// build in a badge and says nothing in the conversation. Nothing here
// posts a commit status or a check run, still deliberately: the merge
// queue reads this verdict off the task (grainCIVerdictOf) rather than
// out of GitHub, so reporting it as a check would add a second, slower
// path to the same decision -- and one that a repository's own required
// checks would then also be judging.
//
// Neither comment can fail the reconcile of any other task: the error is
// returned for the cycle to log against this task and nothing downstream
// reads it, because the verdict is already recorded.
func announceCIRun(ctx context.Context, deps Deps, task model.Task, run ci.Run,
	previousConclusion, previousDetail string, now time.Time) error {

	if previousConclusion == string(run.Conclusion) && previousDetail == ciDetail(run) {
		return nil
	}
	// Whether this red is this branch's own, asked once and said to both
	// audiences. Only for a run that did not pass: a green run has no
	// failure to share, and asking would cost a read of the store per
	// passing verdict.
	var faults []ci.SharedFault
	if run.Conclusion != ci.ConclusionSuccess {
		faults = ciSharedFaults(deps.CI, now)
	}
	if _, err := deps.Store.AddComment(ctx, model.Comment{
		TaskID:    task.ID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body:      ciTaskCommentBody(run, faults),
		CreatedAt: now,
	}); err != nil {
		return fmt.Errorf("orchestrator: recording grain CI's verdict on %s: %w", task.ID, err)
	}
	if run.Conclusion == ci.ConclusionSuccess {
		return nil
	}
	ref, ok := trackedPullRequest(task)
	if !ok || deps.Client == nil {
		return nil
	}
	body := ciCommentBody(run, faults, ciFaultFixes(ctx, deps))
	if _, err := deps.Client.CreateComment(ref.Repo.Owner, ref.Repo.Name, ref.Number, body); err != nil {
		return fmt.Errorf("orchestrator: commenting grain CI's verdict on %s: %w", ref, err)
	}
	return nil
}

// ciTaskCommentBody is deliberately only a verdict. Task conversations
// are replayed into later agent sessions, while the CI status and run log
// already carry the job names, errors, and output for readers who need
// them.
//
// The one exception is a shared fault. Everything else kept out of here
// is available to whoever reads this later -- a job list, a step's
// output, an error -- from the run the status names; "your `image` job
// is failing the same way on five other branches right now" is not,
// because it is a fact about runs that are not this task's and that a
// later agent has no reason to go looking for. It is also the fact that
// decides what that agent should do next, and it is one sentence
// (grain/task-299).
func ciTaskCommentBody(run ci.Run, faults []ci.SharedFault) string {
	commit := shortSHA(run.Commit)
	if shared := ciSharedFaultSentence(run, faults); shared != "" {
		return ciVerdictSentence(run, commit) + "\n\n" + shared
	}
	return ciVerdictSentence(run, commit)
}

// ciSharedFaultSentence is what a verdict says about the other branches
// failing the same way, in the two lines a conversation has room for --
// empty for the ordinary red build that is this branch's own, and for
// every green one.
//
// The rule about when there is anything to say is pkg/ci's, not this
// package's: a fault needs the same job failing identically on at least
// two distinct refs inside a window, and SharedFault.Summary is empty
// for a reader who is the only branch in it. Nothing here can widen
// that, which is the point -- a verdict comment that blamed the
// deployment for a branch's own failure would teach every reader to
// ignore the line.
func ciSharedFaultSentence(run ci.Run, faults []ci.SharedFault) string {
	if len(faults) == 0 {
		return ""
	}
	for _, job := range run.Jobs {
		fault, ok := ci.FaultForJob(faults, run, job)
		if !ok {
			continue
		}
		summary := fault.Summary(run.Ref)
		if summary == "" {
			continue
		}
		if refs := fault.OtherRefsPhrase(run.Ref); refs != "" {
			return summary + " Also failing on: " + refs + "."
		}
		return summary
	}
	return ""
}

// ciVerdictSentence is the verdict itself, in the one line a task's
// conversation gets.
func ciVerdictSentence(run ci.Run, commit string) string {
	switch {
	case run.Error != "", run.Conclusion == ci.ConclusionErrored:
		return fmt.Sprintf("grain CI could not run at `%s`.", commit)
	case run.Conclusion == ci.ConclusionSuccess && len(run.Jobs) == 0:
		// Which empty run this is, in the line itself: a repository
		// declaring nothing is the only one that passes, and a reader who
		// is not told that reads a green run of no jobs as a suite that
		// somehow skipped (ciDetail says the same thing for the same
		// reason).
		return fmt.Sprintf("grain CI had nothing to run at `%s`: this repository declares no workflow.", commit)
	case strings.TrimSpace(run.CancelReason) != "":
		// The note whoever stopped it left, carried into the one place a
		// later run of this task reads: its own conversation. Saying
		// "cancelled" and keeping the reason to ourselves would leave the
		// next agent to work out for itself whether to start another run.
		return fmt.Sprintf("grain CI %s at `%s`: %s",
			run.Conclusion, commit, truncate(strings.TrimSpace(run.CancelReason), 200))
	default:
		return fmt.Sprintf("grain CI %s at `%s`.", run.Conclusion, commit)
	}
}

// trackedPullRequest is the pull request this task's branch is open as,
// off its own LinkFixes link -- the same link finishWithPullRequest wrote
// and Store.OpenPullRequestLinks selects on. A task with several (a
// branch reopened by hand) yields the first, which is the one grain
// itself opened.
func trackedPullRequest(task model.Task) (model.PullRequestRef, bool) {
	for _, l := range task.Links {
		if l.Kind != model.LinkFixes {
			continue
		}
		if ref, err := model.ParsePullRequestRef(l.Target); err == nil {
			return ref, true
		}
	}
	return model.PullRequestRef{}, false
}

// ciDetail is the verdict in one line: what goes in the task's record,
// where it has to still mean something after ci.RunStore has forgotten
// the run it came from (model.CIStatus' own doc comment).
//
// Names, not output. This is a stored column read in a task listing, the
// same discipline model.Run.Detail holds to; the failing step's own
// output goes in the comment below, bounded, where there is room for it.
func ciDetail(run ci.Run) string {
	if run.Error != "" {
		return "grain CI could not run: " + truncate(run.Error, 200)
	}
	// Somebody stopped this run and said why. That note is the whole of
	// what distinguishes one cancellation from another -- a supersede, a
	// person who knows this tree is not worth testing -- and it is the
	// only thing here written by a human for the reader of this line, so
	// it is what the line says rather than the job names underneath it.
	if reason := strings.TrimSpace(run.CancelReason); reason != "" {
		return fmt.Sprintf("%s: %s", run.Conclusion, truncate(reason, 200))
	}
	failed := failedCIJobs(run)
	switch {
	case run.Conclusion == ci.ConclusionErrored && len(failed) > 0:
		// Nothing in this run judged the tree: every job that did not
		// pass failed to get a machine to run in. The sentence has to
		// say so, because this is what the merge queue holds the pull
		// request pending on (withGrainCI) and what it eventually tells
		// a person -- "failure: tests/go" would send them to the branch
		// instead of to this deployment. The job's own error comes with
		// it, which is the one exception this line makes to carrying
		// names and nothing else: it is a sentence rather than a log,
		// and it is the whole of what the person it reaches has to go on.
		detail := fmt.Sprintf("grain CI could not run %d job(s) (%s)", len(failed), strings.Join(failed, ", "))
		if why := firstCIJobError(run); why != "" {
			detail += ": " + truncate(why, 200)
		}
		return detail
	case len(failed) > 0:
		return fmt.Sprintf("%s: %s", run.Conclusion, strings.Join(failed, ", "))
	case len(run.Jobs) == 0:
		// PlanRun's own rule: a repository that declares no workflows at
		// all is an empty plan, and an empty plan is a success. Saying so
		// is what keeps a green tick from being read as "the tests
		// passed" on a repository that declares none.
		//
		// This is only that repository. One that declares workflows and
		// matched none of them arrives with run.Error set and is answered
		// at the top of this function, because it is not a success at all
		// (ci.Plan.Unmatched).
		return "this repository declares no workflow, so grain CI ran nothing"
	default:
		return fmt.Sprintf("%s: %d job(s)", run.Conclusion, len(run.Jobs))
	}
}

// failedCIJobs names the jobs of run that did not pass -- including the
// ones grain could not run at all, which did not pass either -- in the
// order the
// run reports them (plan order -- ci.SortJobRuns is for a reader looking
// at a whole run, and everything here is already a failure), each as
// "<workflow>/<job>" -- unique across a plan the way ci.PlannedJob.ID
// is, and readable the way a bare job id is not when two workflows each
// declare a "test". `grain ci` spells the same job with the workflow in
// a column of its own (pkg/cli/cicmd_print.go); one sentence has nowhere to
// put a column.
func failedCIJobs(run ci.Run) []string {
	var out []string
	for _, j := range run.Jobs {
		switch j.Conclusion {
		case ci.ConclusionFailure, ci.ConclusionCancelled, ci.ConclusionErrored:
			out = append(out, ciJobName(j))
		}
	}
	return out
}

// firstCIJobError is the reason the first job that could not run gives
// for it (ci.JobRun.Error) -- "building a sandbox for this job: ...".
// Every job of an errored run usually carries the same one, since what
// stops one sandbox being built stops all of them, so the first is the
// whole story and the rest would be the same sentence again.
func firstCIJobError(run ci.Run) string {
	for _, j := range run.Jobs {
		if j.Conclusion == ci.ConclusionErrored && j.Error != "" {
			return j.Error
		}
	}
	return ""
}

func ciJobName(j ci.JobRun) string {
	name := j.Name
	if name == "" {
		name = j.JobID
	}
	if j.Workflow == "" {
		return name
	}
	return j.Workflow + "/" + name
}

// ciCommentBody is the verdict as words: what happened, which jobs said
// so, and enough of the first failing step's output to act on without
// going and looking.
//
// The output is here and not in the stored detail because this is the
// one place with room for it, and because a link is not a substitute:
// GET /api/ci/runs/{id} serves the whole log for as long as the run is
// kept (grain/task-303 made that weeks rather than until the next
// restart), and it serves it to somebody who is logged into grain, which
// a reviewer reading a pull request on GitHub need not be. Failing that,
// the few lines that matter belong where the verdict is.
// Bounded hard for the reason ci.MaxStepOutput exists
// at all -- a step is free to print a hundred megabytes, and a comment is
// a row in the store and a body posted to GitHub.
func ciCommentBody(run ci.Run, faults []ci.SharedFault, fixes faultFixLookup) string {
	var b strings.Builder
	commit := shortSHA(run.Commit)
	switch {
	case run.Error != "":
		fmt.Fprintf(&b, "**grain CI could not run** against `%s` at `%s`.\n\n```\n%s\n```\n",
			run.Ref, commit, truncate(run.Error, 1000))
	case run.Conclusion == ci.ConclusionSuccess && len(run.Jobs) == 0:
		// The only empty run that still reaches here: a repository that
		// declares no workflow anywhere, which is a success on purpose
		// (ci.PlanRun). It used to say "no workflow answers a push to
		// this branch", which is the *other* empty run's sentence -- and
		// that one is errored now and answered above, so saying it here
		// sent a reader looking for the trigger they had got wrong in a
		// repository that had no triggers to get wrong.
		fmt.Fprintf(&b, "**grain CI: nothing to run** for `%s` at `%s` -- this repository declares "+
			"no workflow at all, in `.grain/workflows` or in `.github/workflows`.\n", run.Ref, commit)
	case run.Conclusion == ci.ConclusionSuccess:
		fmt.Fprintf(&b, "**grain CI passed** for `%s` at `%s`: %d job(s), all green.\n",
			run.Ref, commit, len(run.Jobs))
	case run.Conclusion == ci.ConclusionErrored:
		// The one verdict that is not about the branch at all, and the
		// sentence has to open by saying so: whoever reads this on a
		// pull request is entitled to know that nothing here is theirs
		// to fix (ci.ConclusionErrored).
		fmt.Fprintf(&b, "**grain CI could not run** against `%s` at `%s`. This says nothing "+
			"about the commit: nothing here got as far as testing the tree -- a job never got "+
			"a machine, or a step that fetches what the job needs to run in could not "+
			"(`infrastructure:`, which is how a cold cache costs time and not a verdict) -- "+
			"so what needs looking at is this deployment, not this branch.\n",
			run.Ref, commit)
	default:
		fmt.Fprintf(&b, "**grain CI: %s** for `%s` at `%s`.\n", run.Conclusion, run.Ref, commit)
	}

	if failed := failedCIJobs(run); len(failed) > 0 {
		if run.Conclusion == ci.ConclusionErrored {
			b.WriteString("\nJobs grain could not run:\n\n")
		} else {
			b.WriteString("\nJobs that did not pass:\n\n")
		}
		for _, name := range failed {
			fmt.Fprintf(&b, "- `%s`\n", name)
		}
		if why := firstCIJobError(run); run.Conclusion == ci.ConclusionErrored && why != "" {
			fmt.Fprintf(&b, "\nWhat stopped them:\n\n```\n%s\n```\n", truncate(why, ciCommentOutput))
		}
		if step, job, ok := firstFailingStep(run); ok {
			what := "The first failing step"
			if run.Conclusion == ci.ConclusionErrored {
				// Not a failing step: a step that was fetching this job's
				// environment and did not manage it, which is why the run
				// says nothing about the commit.
				what = "The step that could not get its job an environment"
			}
			fmt.Fprintf(&b, "\n%s, `%s` in `%s` (exit %d):\n\n```\n%s\n```\n",
				what, step.Name, job, step.ExitCode, truncate(tail(step.Output, ciCommentOutput), ciCommentOutput))
		}
	}
	b.WriteString(ciCommentFaultSection(run, faults, fixes))

	fmt.Fprintf(&b, "\nThis is grain's own CI (`.grain/workflows`), not a GitHub check, so it "+
		"appears on this task rather than among the pull request's checks. A failed run "+
		"holds the merge queue exactly as a red check does until the exact head passes the "+
		"full suite; a cancelled one stays pending; a run grain could not do holds it like "+
		"an unfinished check -- no repair is filed against a branch that has done nothing "+
		"wrong, and the queue hands the pull request to a person if this deployment still "+
		"cannot run it by the time it gives up. `GET /api/ci/runs/%s` serves run `%s` for "+
		"as long as this deployment keeps it.\n",
		run.ID, run.ID)
	return b.String()
}

// ciCommentFaultSection is what this comment says about the other
// branches failing the same way -- the same sentence the CI pane and the
// task's own pane show (ui/src/ciFaults.js), with the evidence under it.
//
// Under the failing step's output rather than above the verdict,
// because it is a claim about that output: the reader has to be able to
// see what failed before being told who else it is failing for. It says
// nothing at all for a red build nobody else shares, which is most of
// them.
//
// It names the fix as well, where grain can honestly say there is one:
// an open pull request touching a file every branch in the fault
// printed (pkg/cifault, which is where that judgement lives and which
// the CI pane and wait_for_checks read the same answer out of). That
// sentence is the whole point of the section for a reviewer -- "this is
// shared, and #1591 already fixes it" is what stops an approval of a
// repair nobody needed -- so it goes under the evidence rather than
// being left to whoever thinks to open the pane.
//
// fixes is the lookup, passed in rather than made here because it wants
// a store and a GitHub client; nil, or a nil answer, leaves the section
// exactly as it was.
func ciCommentFaultSection(run ci.Run, faults []ci.SharedFault, fixes faultFixLookup) string {
	if len(faults) == 0 {
		return ""
	}
	var b strings.Builder
	for _, job := range run.Jobs {
		fault, ok := ci.FaultForJob(faults, run, job)
		if !ok {
			continue
		}
		summary := fault.Summary(run.Ref)
		if summary == "" {
			continue
		}
		fmt.Fprintf(&b, "\n**This red is shared.** %s\n\n", summary)
		if refs := fault.OtherRefsPhrase(run.Ref); refs != "" {
			fmt.Fprintf(&b, "- Also failing on: %s\n", refs)
		}
		if len(fault.Paths) > 0 {
			fmt.Fprintf(&b, "- Every one of those runs names: %s\n", strings.Join(fault.Paths, ", "))
		}
		b.WriteString("- Fixing it on this branch will not make it green: a shared fault is fixed " +
			"where it comes from.\n")
		for _, fix := range fixes.of(fault) {
			// Bold, because this is the line that decides what the
			// reader does next: there is already a change open against
			// the cause, and merging it is cheaper than approving a
			// repair of the symptom here.
			fmt.Fprintf(&b, "- **Open already, touching %s: %s**", fix.Where(), fix.PullRequest)
			if fix.Title != "" {
				fmt.Fprintf(&b, " (%s)", fix.Title)
			}
			if fix.Task != "" {
				fmt.Fprintf(&b, ", from task %s", fix.Task)
			}
			b.WriteString("\n")
		}
		// One fault per comment. A run with two jobs in two separate
		// deployment-wide faults is rare enough, and a second block of
		// this would push the failure it is about off the top of what
		// anybody reads.
		break
	}
	return b.String()
}

// ciCommentOutput is how much of a failing step's output a comment
// carries: enough to see the assertion that failed and the few lines
// around it, and nowhere near enough for one bad workflow to fill a
// task's conversation or a pull request thread.
const ciCommentOutput = 2000

// firstFailingStep finds the step worth quoting: the first non-zero exit
// of the first job that did not pass, which is where a reader would have
// started looking anyway.
//
// A job that errored counts, and so does a step that did: a step which
// could not fetch the environment its job runs in is errored rather than
// failed (ci.Step.Infrastructure), and its output -- the registry that
// refused, the build that ran out of time -- is the whole of what the
// person reading "grain CI could not run" has to work from. A job that
// errored before it ran a step has nothing but skipped steps and matches
// nothing here, which is what leaves ci.JobRun.Error as its account.
func firstFailingStep(run ci.Run) (ci.StepRun, string, bool) {
	for _, j := range run.Jobs {
		if j.Conclusion != ci.ConclusionFailure && j.Conclusion != ci.ConclusionErrored {
			continue
		}
		for _, s := range j.Steps {
			if s.Conclusion == ci.ConclusionFailure || s.Conclusion == ci.ConclusionErrored {
				return s, ciJobName(j), true
			}
		}
	}
	return ci.StepRun{}, "", false
}

// tail returns the last max bytes of s, which is where a failure is --
// the head of a step's output says what it was doing and the tail says
// what went wrong, and only one of them fits in a comment.
func tail(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[len(s)-max:]
	if i := strings.IndexByte(cut, '\n'); i >= 0 && i < len(cut)-1 {
		cut = cut[i+1:] // start at a line boundary rather than mid-line
	}
	return "[...earlier output elided by grain...]\n" + cut
}

// shortSHA is a commit as it is written in a sentence. An empty sha --
// a Source with no notion of one (ci.FSSource) -- reads as "the branch
// head" rather than as an empty pair of backticks.
func shortSHA(sha string) string {
	if sha == "" {
		return "the branch head"
	}
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
