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
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/forge"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// What follows, down to queueEntry, is this file's side of the boundary
// docs/three-pieces.md draws: the pure "what is true about this branch"
// half of what used to be here now lives in pkg/forge, which imports no
// pkg/model and so cannot read a task, and this is the adapter onto it.
//
// The adapter exists rather than every call site being rewritten because
// the two packages speak deliberately different vocabularies. forge.Health
// and forge.Ref are pkg/forge's own spellings of model.PrHealth and
// model.PullRequestRef -- same values, same shape, no import of the task
// model -- so translating is a string conversion and a struct literal,
// and doing it once here keeps `model.PrHealth` the only spelling the
// task loop ever sees.
//
// The wrappers keep their old unexported names on purpose: what moved is
// the code, not its behaviour, and this file's own tests still call
// healthFrom, withGrainCI, checkRunsFor and the two clocks by those
// names. Moving those tests to pkg/forge, where the code they exercise
// now is, is the obvious follow-up and deliberately not part of the same
// change -- leaving them here is what makes "the behaviour is identical"
// something the existing suite asserts rather than something this commit
// message claims.

// forgeRef is model.PullRequestRef in pkg/forge's spelling.
func forgeRef(ref model.PullRequestRef) forge.Ref {
	return forge.Ref{Owner: ref.Repo.Owner, Repo: ref.Repo.Name, Number: ref.Number}
}

// grainCIVerdict is pkg/forge's, aliased rather than converted: it is
// pure data with no model type in it, every field is already exported,
// and an alias means the sixteen places this file's tests construct one
// need no edit.
type grainCIVerdict = forge.GrainCI

// grainCICheckName is the name grain's own CI answers under wherever a
// verdict is rendered beside GitHub's checks.
const grainCICheckName = forge.GrainCICheckName

func healthFrom(detail github.PullRequestDetail, checks []github.CheckRun, requiredCheck string,
	checksKnown, checksSettled bool) model.PrHealth {

	return model.PrHealth(forge.HealthFrom(detail, checks, requiredCheck, checksKnown, checksSettled))
}

func healthOf(detail github.PullRequestDetail, checks []github.CheckRun, requiredCheck string,
	checksKnown, checksSettled bool, grain grainCIVerdict) model.PrHealth {

	return model.PrHealth(forge.HealthOf(detail, checks, requiredCheck, checksKnown, checksSettled, grain))
}

func withGrainCI(health model.PrHealth, grain grainCIVerdict) model.PrHealth {
	return model.PrHealth(forge.WithGrainCI(forge.Health(health), grain))
}

func failingChecks(checks []github.CheckRun) []string { return forge.FailingChecks(checks) }

func cancelledChecks(checks []github.CheckRun) []string { return forge.CancelledChecks(checks) }

func unfinishedChecks(checks []github.CheckRun) []string { return forge.UnfinishedChecks(checks) }

func awaitingApprovalChecks(checks []github.CheckRun) []string {
	return forge.AwaitingApprovalChecks(checks)
}

func checkRunsFor(client github.Client, ref model.PullRequestRef, head, headSHA string) (
	[]github.CheckRun, bool, error) {

	// nil: this wrapper is what the tests in this package call, and they
	// are about the forge's own reading of GitHub. The reporting service,
	// where a deployment has one, reaches the forge through
	// queueEntry.observe's forge.Input instead.
	return forge.CheckRuns(client, nil, forgeRef(ref), head, headSHA)
}

func emptyChecksSettled(ref model.PullRequestRef, headSHA string, now time.Time) bool {
	return forge.EmptyChecksSettled(forgeRef(ref), headSHA, now)
}

func forgetEmptyChecks(ref model.PullRequestRef) { forge.ForgetEmptyChecks(forgeRef(ref)) }

func checksStalled(ref model.PullRequestRef, headSHA string, now time.Time) (bool, time.Duration) {
	return forge.ChecksStalled(forgeRef(ref), headSHA, now)
}

func forgetPendingChecks(ref model.PullRequestRef) { forge.ForgetPendingChecks(forgeRef(ref)) }

// ChecksUnavailable, SetCheckRegistrationWindow and SetCheckStallDeadline
// keep their names here because callers outside this package hold them --
// pkg/ui reads the first for the banner it shows when this deployment's
// credential can read no CI at all, and the live tests set the two
// windows.
func ChecksUnavailable() bool { return forge.ChecksUnavailable() }

func SetCheckRegistrationWindow(d time.Duration) func() {
	return forge.SetCheckRegistrationWindow(d)
}

func SetCheckStallDeadline(d time.Duration) func() { return forge.SetCheckStallDeadline(d) }

// grainCIVerdictOf reads the status recorded on a task
// (model.Observation.CI) as an answer about headSHA, or as no answer at
// all.
//
// # The capability is the switch, and the only one
//
// grain-ci is what makes the full suite mandatory, while wait_for_checks
// is what starts it, so the capability decides whether the answer counts.
// is no second setting to turn the gate on for the same reason there was
// no first one: a repository is either checked by grain CI or it is not,
// and "run it but ignore what it says" is a state worth having only while
// nobody trusts the runner -- which is a reason to take the capability
// off that repository, not a mode to keep in this file forever. A task
// that does not hold the grant is invisible here however red its record.
//
// # Only about this commit
//
// A verdict about any other commit is not stale so much as about a
// different tree, and it cannot satisfy the gate. The branch remains
// pending until wait_for_checks explicitly starts and passes the full
// suite against the new head.
//
// # Only "failure" is failure
//
// A run's conclusion is one of success, failure, errored and cancelled
// (ci.concludeRun); "errored" is the section below. "cancelled" is the
// queue's own supersede, a daemon shutting down, or a job timeout -- not
// a tree that was tested and did not pass -- and treating it as one is exactly the mistake failingChecks
// declines to make about GitHub's own "cancelled". "skipped" reaches a
// whole run only through jobs whose needs did not run, and says the same
// nothing. They therefore do not make the head failing, but neither one
// satisfies the required-success gate: the branch remains pending until
// an explicit full-suite run passes.
//
// "cancelled" comes back as GrainCI.Cancelled rather than as the bare
// Required verdict the other non-answers share, and that changes no
// decision here: the head is pending either way. What it buys is the
// row's own sentence. "No run has been recorded for this commit yet" and
// "the run for this commit was cancelled and nothing will report on it
// again" are the same silence to this gate and opposite news to a person
// -- the first ends by itself and the second ends only if somebody asks
// for another run.
//
// # A run grain could not do at all is not a failure
//
// A verdict the engine reached without running anything -- no sandbox
// backend wired, a job whose sandbox could not be built -- used to be
// "failure" like any other, because nothing recorded told the two apart,
// and this comment said a field saying which it was would be a schema
// column to spend once somebody had actually been bitten. Somebody was:
// the first CI runs on a granule-backed deployment died at Acquire on a
// sandbox name too long to be a VM name, and every pull request open at
// the time was told its checks were failing (grain/task-361). No column
// was needed in the end -- ci.Conclusion is passed through to
// model.CIStatus as the word pkg/ci used, so ci.ConclusionErrored
// arrives here on its own -- and it reads as Errored, which withGrainCI
// holds as pending rather than as a broken head to repair. reconcileTaskCI
// retries that verdict after five minutes, at most three times (four attempts
// total), and suppresses comments when the conclusion and detail have not
// changed. Retrying does not alter the merge queue's first-pending sighting,
// so a deployment that stays broken still reaches the ordinary stall deadline.
//
// # gateIsGrainCI
//
// Ordinarily the question "is grain CI required here" is answered by the
// task's own grant: a run that holds grain-ci is judged by grain CI, and
// one that does not is judged by GitHub's checks. A repository that has
// declared GitHub's checks are not a source of verdicts for it
// (model.RepoConfig.IgnoreGitHubCI) is the one place those two come
// apart, because there is no second provider left to fall back to -- so
// the repository's declaration requires grain CI whether or not this
// particular task was granted it.
//
// For a task that does hold the grant this changes nothing at all. For
// one that does not, it is the difference between a pull request that
// waits, visibly, for a verdict that has to be asked for, and one that
// merges because nobody looked. forge.Observe makes the same statement
// for itself (forge.Input.IgnoreGitHubCI); this arm is what lets a task
// that *has* a recorded verdict be judged on it rather than on silence.
func grainCIVerdictOf(task model.Task, obs *model.Observation, headSHA string, gateIsGrainCI bool) grainCIVerdict {
	if (!grainci.Granted(task.Grants) && !gateIsGrainCI) || headSHA == "" {
		return grainCIVerdict{}
	}
	if obs == nil || obs.CI == nil {
		return grainCIVerdict{Required: true}
	}
	status := obs.CI
	if status.Commit != headSHA {
		return grainCIVerdict{Required: true}
	}
	if !status.Done() {
		return grainCIVerdict{Required: true, Running: true}
	}
	// What grain intends to do about a verdict that is not an answer,
	// read off the same status by the same rule reconcileTaskCI acts on,
	// so the row and the reconciler cannot disagree about whether another
	// run is coming (ciReplacementFor).
	replacement := ciReplacementFor(*status)
	attempts := ciAttemptsOf(*status)
	switch status.Conclusion {
	case string(ci.ConclusionFailure):
		return grainCIVerdict{Required: true, Failed: true, Detail: status.Detail}
	case string(ci.ConclusionErrored):
		return grainCIVerdict{Required: true, Errored: true, Detail: status.Detail,
			Replacement: replacement, Attempts: attempts}
	case string(ci.ConclusionSuccess):
		return grainCIVerdict{Required: true, Passed: true}
	case string(ci.ConclusionCancelled):
		return grainCIVerdict{Required: true, Cancelled: true, Detail: status.Detail,
			Replacement: replacement, Attempts: attempts}
	}
	return grainCIVerdict{Required: true}
}

// queueEntry is one still-open tracked pull request, everything syncEntry
// needs about it gathered up front so SyncPullRequests can compute every
// repo's queue head before acting on any single one.
//
// The fields below the store's own are GitHub's answer about this pull
// request, filled in by observe at the top of the entry's own turn.
type queueEntry struct {
	task model.Task
	// paused is an operator's pause on the task (model.TaskPause): its
	// pull request stays open, and it is out of the queue -- and out of
	// any batch -- until it is resumed, when it returns where its
	// backlog position and MergeQueueEnteredAt already put it.
	paused bool
	obs    *model.Observation
	ref    model.PullRequestRef
	// requiredCheck is the check run this pull request may not be called
	// clean without, and it is empty for all but one repository: see
	// healthFrom and StateRepoGate.
	requiredCheck string
	// verdicts is somewhere other than GitHub that may already know how
	// this commit's checks ended (pkg/checkpost), nil on a deployment
	// with no such service. It only ever saves a GitHub read -- see
	// forge.Verdicts on why an incomplete answer is not an answer.
	verdicts forge.Verdicts
	// requireUpToDate is this repository's answer to "may a branch merge
	// while it is behind its base" (model.RepoConfig.RequireBranchUpToDate),
	// read once per repository per cycle rather than per entry. False --
	// the default, and what a repository with no configuration of its own
	// says -- is the cheap behaviour this queue is built around: a branch
	// that merges cleanly merges on the verdict it already holds, however
	// far behind it is. See bringHeadUpToDate for the other direction.
	requireUpToDate bool
	// mergeOnGreen is this repository's answer to "may a green entry
	// merge past one ahead of it whose CI is still running"
	// (model.RepoConfig.MergeOnGreen), read once per repository per cycle
	// beside requireUpToDate above. False -- the default -- is the strict
	// lane: an entry still deciding closes it (passedOver).
	mergeOnGreen bool
	// gateIsGrainCI is this repository's answer to "are GitHub's check
	// runs a source of verdicts here at all"
	// (model.RepoConfig.IgnoreGitHubCI, or model.Config.IgnoreGitHubCI
	// for a whole deployment in that state), read once per repository
	// per cycle beside requireUpToDate above.
	//
	// True and nothing about this pull request is read from GitHub's
	// checks: they are not fetched, not counted in health, not waited
	// on, and not named in any sentence the queue writes about it. What
	// takes their place is grain CI, required by the declaration rather
	// than by the task's grant (grainCIVerdictOf), so the switch swaps
	// this repository's gate rather than removing it.
	//
	// False -- the default, and what every repository with no
	// configuration of its own says -- is every behaviour that was here
	// before the field existed.
	gateIsGrainCI bool
	// ciMode is this repository's configured CI mode (grain, github, none).
	ciMode model.CIMode
	// running says an agent run for this task is in flight right now, so
	// its branch is one somebody is still pushing to.
	//
	// Until run-now (ui.Client.RunNow) the only way that happened to a
	// pull request already on the queue was a repair the queue itself
	// asked for, which it recognised by its own stamp
	// (Observation.RepairInFlight) rather than by there being a run. A
	// person can now hand any task back to an agent, at any point after
	// its work was over, so the queue reads the run instead: the branch
	// being written to is what actually matters to it, and who asked for
	// that is not.
	running bool
	// recheckingHead is the commit SHA of this task's pull request when
	// grain CI is currently running an overlap re-check for it on the
	// merge-queue ref (mergerecheck.go).
	recheckingHead string

	detail        github.PullRequestDetail
	checks        []github.CheckRun
	checksKnown   bool
	checksSettled bool
	// grain is what grain's own CI said about the same commit, read off
	// this task's own record rather than off GitHub (grainCIVerdictOf).
	grain  grainCIVerdict
	health model.PrHealth

	// stalled is health == PrPending having gone on for longer than the
	// queue is willing to wait, with the deadline it was measured
	// against; see checksStalled.
	stalled       bool
	stallDeadline time.Duration

	// declaredUnfixable is this repository's own list of checks nothing
	// pushed to a branch here turns green
	// (model.RepoConfig.UnfixableChecks), read once per repository per
	// cycle beside requireUpToDate above. A CLA bot is the standing
	// example: it works exactly as intended and is never going to pass
	// for the author grain files pull requests as.
	declaredUnfixable []string
	// unfixable is what this cycle concluded about the repository as a
	// whole -- that list, plus the reading of its recent runs
	// repoFinder makes once and every entry shares. Zero for an entry
	// nothing is being decided about, since the question is only asked
	// where the answer could change a move.
	unfixable forge.Unfixable
	// reds is this entry's failing checks split by whether a commit on
	// this branch could clear them (forge.SplitReds). It is what decides
	// whether the queue spends a base merge and a repair on this entry
	// or hands it to a person in seconds, and it is what lets a row tell
	// the two kinds of red apart -- grain/task-219 was one of each at
	// once, and the repairable one went unnoticed among the seven that
	// were not.
	reds forge.Reds
	// rules is the repository's merge rules as its base declares them
	// (mergerules.go), nil where it has none. policyFor classifies the
	// pull request against them for a head commit, and policy is that
	// answer once observe has asked it.
	rules     *model.MergeRules
	policyFor func(headSHA string) *model.EffectiveMergePolicy
	policy    *model.EffectiveMergePolicy
	runner    ci.Runner
	// missingChecks is the GitHub checks the policy requires that are
	// not on the head commit at all.
	missingChecks []string
}

// observe fills in e's GitHub half: the pull request as it stands, the
// checks on its head commit, the health computed from the two, and both
// of the clocks this file runs on a pull request's CI.
//
// It is the read half of syncEntry, named separately because what it
// reads is what the rest of that function -- and, through mergeWalk, the
// entries behind this one -- decides on. It is deliberately still called
// on the entry's own turn rather than for every entry up front: a pull
// request's head branch can move during a cycle (a review or a fix task
// merging back into it is exactly that), and a verdict read before that
// push describes a commit that is gone by the time anything acts on it.
func (e *queueEntry) observe(client github.Client, now time.Time) error {
	// The two facts pkg/forge cannot look up for itself, because both are
	// facts about a *task*: what grain's own CI made of the commit (read
	// off this task's own record by grainCIVerdictOf), and whether the
	// queue has given up on this pull request (blockedEntry, which reads
	// Observation.MergeQueueBlockedAt). Handing them down rather than
	// letting the forge fetch them is the whole of the direction rule --
	// see pkg/forge's package comment.
	//
	// The verdict is a callback rather than a value because it is about
	// the head commit, and the head commit is what the forge's own read
	// is for -- forge.Input.GrainCIFor's own doc comment has the whole
	// of it.
	seen, err := forge.Observe(client, forgeRef(e.ref), forge.Input{
		RequiredCheck:  e.requiredCheck,
		Blocked:        blockedEntry(*e),
		Verdicts:       e.verdicts,
		IgnoreGitHubCI: e.gateIsGrainCI,
		CIMode:         string(e.ciMode),
		PolicyFor: func(headSHA string) *forge.Policy {
			if e.policyFor == nil {
				return nil
			}
			e.policy = e.policyFor(headSHA)
			if e.policy != nil {
				// The rules replace the repository's mode for this pull
				// request, and only ever add to it (model's Effective).
				e.ciMode = e.policy.GateCIMode()
				e.gateIsGrainCI = e.policy.Mode == model.CIGateGrain
			}
			return forgePolicy(e.policy)
		},
		GrainCIFor: func(headSHA string) forge.GrainCI {
			gate := e.gateIsGrainCI || (e.policy != nil && e.policy.Mode.Grain())
			return narrowedGrainCI(grainCIVerdictOf(e.task, e.obs, headSHA, gate), e.policy, e.obs, e.runner)
		},
	}, now)
	if err != nil {
		return err
	}

	e.detail, e.checks = seen.Detail, seen.Checks
	e.checksKnown, e.checksSettled = seen.ChecksKnown, seen.ChecksSettled
	e.grain = seen.GrainCI
	e.grain.GitHubToo = e.policy != nil && e.policy.Mode.GitHub()
	e.health = model.PrHealth(seen.Health)
	e.stalled, e.stallDeadline = seen.Stalled, seen.StallDeadline
	e.missingChecks = seen.MissingChecks
	return nil
}

// splitReds asks, of an entry that is red, which of its red a commit on
// its own branch could clear -- the question that comes before every
// move the queue makes about a broken head, and the one it used not to
// ask at all.
//
// Only for a head that is actually broken, and only where there is
// something named to classify. Everything else answers the zero Reds,
// which reads as "nothing inherited, nothing owned" and leaves every
// decision exactly where it stood before this existed.
//
// The GitHub read behind the repository half (repoFinder.starts) is made
// only when this entry has failing GitHub checks to classify. A
// repository whose gate is grain CI has none -- forge.Observe never read
// them -- and neither does a task holding grain-ci, whose GitHub checks
// were read and not counted (forge.HealthOf): in both, the red that
// holds the merge is grain's own verdict about this exact commit, which
// is the branch's own by construction and needs nobody's opinion about
// the repository.
func splitReds(e queueEntry, find *repoFinder) (forge.Reds, forge.Unfixable) {
	if e.health != model.PrFailing && e.health != model.PrConflicted {
		return forge.Reds{}, forge.Unfixable{}
	}
	failing := failingCheckNames(e)
	if len(failing) == 0 {
		return forge.SplitReds(nil, e.grain.Failed, forge.Unfixable{}), forge.Unfixable{}
	}
	unfixable := find.unfixable(e.ref.Repo, e.declaredUnfixable)
	return forge.SplitReds(failing, e.grain.Failed, unfixable), unfixable
}

// failingCheckNames is the GitHub checks that are holding this entry, and
// it is empty where GitHub's checks are not what holds anything here:
// healthReason's own rule, in one place both it and the split above read
// from. A repository whose gate is grain CI had none of them read, and a
// task holding grain-ci had them read and not counted (forge.HealthOf),
// so naming one would be pointing at a check that is not what is stopping
// the merge.
func failingCheckNames(e queueEntry) []string {
	if e.grain.Required {
		return nil
	}
	if e.grain.Passed {
		return failingChecks(forge.FilterActionsBudgetFailures(e.checks))
	}
	return failingChecks(e.checks)
}

// blockedEntry reports whether the queue has given up on e's pull
// request -- Observation.MergeQueueBlockedAt, set by blockMergeQueue and
// cleared only by a human asking for another attempt
// (ui.Client.RetryMerge). Such an entry is no repo's queue head and gets
// nothing automatic done to it; it still merges the moment it reads
// clean.
// setMergeQueueState writes what the queue is doing about a task, and
// what that is in words, onto the task's own row -- the states are
// queuestatus.go's, which has the vocabulary and the reasoning.
//
// A write only when something actually changed, since this runs for every
// watched pull request every cycle. The timestamp is the state's and not
// the note's: entering a state starts its clock, and a note reworded
// inside one state (a refusal counting up, a different check now the
// outstanding one) is the same wait going on -- restarting the age on it
// would hide exactly the standing still the age is there to show.
//
// The write touches the merge-queue columns and nothing else, read and
// written in one transaction (Store.ObserveField). obs is the copy this
// cycle read before it looked at the pull request, and a run of the same
// task can finish in between: writing that copy back whole (Store.Observe
// REPLACEs the row) put back the RetryRequestedAt a repair's finish had
// just cleared and took away its CompletedAt, so the task read as queued
// again and was dispatched a second time instead of merging. obs is still
// updated, so the rest of the cycle reads what was written.
func setMergeQueueState(ctx context.Context, store *model.Store, obs *model.Observation,
	taskID string, status queueStatus, now time.Time) error {

	if obs == nil {
		obs = &model.Observation{TaskID: taskID}
	}
	if !applyMergeQueueState(obs, status, now) {
		return nil
	}
	return store.ObserveField(ctx, taskID, now, func(o *model.Observation) {
		applyMergeQueueState(o, status, now)
	})
}

// applyMergeQueueState is setMergeQueueState's change to one copy of the
// row, reporting whether there was one to make.
func applyMergeQueueState(obs *model.Observation, status queueStatus, now time.Time) bool {
	var stalledAt *time.Time
	if obs.MergeQueueState == status.state {
		status.note = mergeLane.noteWithStall(status.state, status.note, obs.MergeQueueStateAt, now)
		stalledAt = mergeLane.stalledAt(status.state, obs.MergeQueueStateAt, now)
	}
	status.note = mergeLane.noteWithWayOut(status.state, status.note)
	if status.reason == "" {
		status.reason = mergeReasonOfState(status.state)
	}
	entering := status.state != "" && obs.MergeQueueEnteredAt == nil
	if !entering && obs.MergeQueueState == status.state && obs.MergeQueueNote == status.note &&
		obs.MergeQueueReason == status.reason && sameInstant(obs.MergeQueueStalledAt, stalledAt) {
		return false
	}
	if entering {
		at := now
		obs.MergeQueueEnteredAt = &at
	}
	if obs.MergeQueueState != status.state {
		at := now
		obs.MergeQueueStateAt = &at
		if status.state == "" {
			obs.MergeQueueStateAt = nil
		}
	}
	obs.MergeQueueState, obs.MergeQueueNote = status.state, status.note
	obs.MergeQueueReason = status.reason
	obs.MergeQueueStalledAt = stalledAt
	return true
}

// sameInstant compares two optional stamps, nil included, so that
// setMergeQueueState can tell "nothing about this row has changed" from
// "this row has just been flagged" without writing on every cycle. Two
// nils are the same answer -- neither row is flagged -- which is the
// comparison == would get wrong for two pointers to equal times.
func sameInstant(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func blockedEntry(e queueEntry) bool {
	return e.obs != nil && e.obs.MergeQueueBlockedAt != nil
}

// isQueueMember reports whether e's task is a member of its target repo's
// merge queue at all -- see mergeWalk. A task never asked to be merged
// automatically (no /auto-merge) is not in any queue: the merge queue is
// opt in, the same "submit" step the issue this implements
// (bwsalmon/agents#283) describes, reusing the directive that already
// meant "merge me once I'm clean" rather than adding a second field that
// would mean the same thing. A fix task the queue itself filed
// (Origin.Reason == ReasonFix) is deliberately excluded too: it merges
// into the task it repairs, not into the repo's own base branch, so it is
// not a queue entry in its own right -- see syncEntry's own eligibility
// check for why it still merges unconditionally. A review task
// (isReviewTask, grain/task-284) is excluded on exactly the same
// grounds: it is stacked on the branch of the task it reviews and merges
// back into it, not into the repo's base. A task the queue has
// already given up on (obs.MergeQueueBlockedAt set) is excluded as well,
// which is what lets the queue move on to the next task rather than
// waiting forever on one that needs a human.
func isQueueMember(e queueEntry) bool {
	if !e.task.AutoMerge || e.task.Origin.Reason == model.ReasonFix || isReviewTask(e.task) || e.paused {
		return false
	}
	// Nor one waiting on an approval GitHub requires: it waits out of
	// line, so the changes behind it go on merging (githubrefusal.go).
	return !blockedEntry(e) && !awaitingGitHubApproval(e.ref)
}

// queueOrder is every entry isQueueMember accepts, across all repos at
// once, in the merge queue's own order: the backlog's, ascending
// Task.OrderKey with the task ID as the tiebreak, which is the order
// Store.ListTasks shows a human and Store.Ready dispatches in.
//
// The queue used to have an order of its own -- earliest submitted, by
// Task.CreatedAt -- computed here, inside one cycle, and written down
// nowhere. Reading position off the backlog instead means there is one
// ordering rather than two, and it is the one already on screen:
// showQueueAtFrontOfBacklog keeps these entries at the front of that
// backlog, in this order, so what a list shows and what merges next
// cannot drift apart, and a human who disagrees with the order can drag a
// row rather than discover there was nothing to drag. For a deployment
// that has never reordered anything this is the same answer CreatedAt
// gave: Store.OrderKeyForNewTask files each new task behind the last.
//
// # Newcomers join at the back
//
// One exception to reading the backlog's order: a member the queue has
// not yet taken on (Observation.MergeQueueEnteredAt nil -- this is its
// first cycle in the queue) sorts behind every member it already has,
// whatever its OrderKey says (grain/task-545). A task filed at the front
// of the backlog (Config.NewestFirst, at_front, and every port
// newPortTask files) takes an OrderKey below everything in the table,
// the merge queue's block included, which is right while it waits to be
// dispatched and wrong the moment it opens a pull request: a mechanical
// port is Completed the cycle it is filed, so a cold pull request used to
// land at position one, ahead of changes that were already green and
// waiting. Placing it behind on its first cycle is enough, because
// showQueueAtFrontOfBacklog writes that order down and the cycles after
// read it back like any other. An operator's drag (POST
// /api/tasks/reorder) still decides the order of everything the queue
// has taken on.
func queueOrder(entries []queueEntry) []queueEntry {
	members := make([]queueEntry, 0, len(entries))
	for _, e := range entries {
		if isQueueMember(e) {
			members = append(members, e)
		}
	}
	sort.SliceStable(members, func(i, j int) bool {
		pi, pj := members[i].task.Priority, members[j].task.Priority
		if pi == 0 {
			pi = model.DefaultTaskPriority
		}
		if pj == 0 {
			pj = model.DefaultTaskPriority
		}
		if pi != pj {
			return pi > pj
		}
		if fi, fj := model.FixesBaseBranch(members[i].task), model.FixesBaseBranch(members[j].task); fi != fj {
			return fi
		}
		if ni, nj := newToQueue(members[i]), newToQueue(members[j]); ni != nj {
			return nj
		}
		if members[i].task.OrderKey != members[j].task.OrderKey {
			return members[i].task.OrderKey < members[j].task.OrderKey
		}
		return members[i].task.ID < members[j].task.ID
	})
	return members
}

// newToQueue reports whether e is a member the merge queue has not yet
// taken on: no cycle has placed it yet, so this is its first
// (Observation.MergeQueueEnteredAt, stampQueueEntry).
func newToQueue(e queueEntry) bool {
	return e.obs == nil || e.obs.MergeQueueEnteredAt == nil
}

// stampQueueEntry records, for each member the queue has not taken on
// before (newToQueue), that it has now: its place was decided this cycle
// and written to the backlog, so from the next cycle on it is ordered by
// that place like every other member (queueOrder).
//
// It is stamped here, as membership is decided, rather than left to the
// first merge-queue state syncEntry writes, because that write needs the
// pull request read. An entry nobody could read would otherwise stay
// "new" for as long as the outage lasted and sink behind every change
// filed after it -- the one entry the queue must not be reordered
// around.
func stampQueueEntry(ctx context.Context, store *model.Store, members []queueEntry, now time.Time) error {
	var errs []error
	for _, e := range members {
		if !newToQueue(e) {
			continue
		}
		err := store.ObserveField(ctx, e.task.ID, now, func(o *model.Observation) {
			if o.MergeQueueEnteredAt == nil {
				at := now
				o.MergeQueueEnteredAt = &at
			}
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: recording when %s joined the merge queue: %w", e.task.ID, err))
			continue
		}
		if e.obs != nil && e.obs.MergeQueueEnteredAt == nil {
			at := now
			e.obs.MergeQueueEnteredAt = &at
		}
	}
	return errors.Join(errs...)
}

// mergeWalk is who merges this cycle, decided one lane at a time as
// SyncPullRequests works down each queue (actOrder) rather than up front.
// It has to be decided that way round: whether the
// entry behind may merge depends on what is wrong with the one in front,
// and that is only known once the one in front has been read.
//
// A lane is a repository and one base branch in it (mergelane.go), and
// everything below is about one lane: a `main` change still deciding
// holds the `main` changes behind it and nothing queued for `candidate`.
// The lane is the pull request's base as this cycle read it (laneOf).
//
// A lane's slot starts open. The first member acted on takes it if it can
// merge; an entry that cannot merge either closes the slot -- everything
// behind it waits, which is the ordinary queue -- or is *passed over*,
// leaving the slot open for the next entry along (passedOver).
//
// Passing over is the whole of what stops one conflict holding up every
// merge behind it. A conflicted or failing head is repaired on its own
// branch by an agent (requeueForRepair): a sandbox, a clone, a
// resolution, a push, and another round of CI. Every green pull request
// queued behind it used to wait out all of that and only then merge, in
// order. Now they merge while it is being resolved, and it merges when
// its resolution lands.
//
// What is *not* passed over matters as much. An entry still deciding --
// PrPending while CI runs, PrUnknown, or one this cycle could not read --
// closes the slot, so nothing behind it merges. Those resolve on their
// own, usually within a cycle or two, and merging past one would put a
// later change ahead of an earlier one on nothing better than being
// quicker to answer, which is the ordering a queue exists to provide.
// Only an entry whose next move is a whole agent run, or a person, is
// worth reordering around.
//
// Each entry that merges consumes one of its lane's configured slots
// (model.Config.MaxMergesPerRepo, which despite its name is per lane).
// Once the limit is reached, everything behind it is judged again next
// cycle against the base the earlier merges moved.
//
// # Heads
//
// The walk also says who is at the front of each lane: the first queue
// member in queueOrder whose lane it is. Only a lane's head is ever
// refreshed or escalated on a given cycle -- everything behind it waits,
// which is the whole property that makes this a queue rather than "merge
// every clean PR in whatever order GitHub happens to return them in": a
// fix filed for the second task while the first is still being repaired
// would very likely need refiling the moment the first merges and changes
// what the second is based against.
//
// That reasoning stops at the branch, which is why heads are per lane.
// And it is why they are claimed here, as each member is read, rather than
// computed up front: a task that named no base is only placed in its lane
// by reading its pull request (laneOf).
type mergeWalk struct {
	used  map[string]int
	limit int
	// heads is each lane's head task (claimHead), keyed by laneKey -- and,
	// under the bare repository name, the task that took every lane of
	// its repository because nobody could say which lane it was in.
	heads map[string]string
	// closedRepos is every repository closed as a whole: an entry whose
	// lane nothing could name failed, and a member nobody could read or
	// place is one nobody in its repository may be reordered around.
	closedRepos map[string]bool
}

func (w *mergeWalk) limitOrDefault() int {
	if w.limit < 1 {
		return model.DefaultMaxMergesPerRepo
	}
	return w.limit
}

// mayMerge reports whether lane (laneKey) has merge capacity left in this
// cycle.
func (w *mergeWalk) mayMerge(lane string) bool {
	if w.closedRepos[laneRepo(lane)] {
		return false
	}
	return w.used[lane] < w.limitOrDefault()
}

// merged spends one of lane's merges.
func (w *mergeWalk) merged(lane string) {
	if w.used == nil {
		w.used = map[string]int{}
	}
	w.used[lane]++
}

// close takes lane out of the walk for the rest of this cycle. It is used
// when an entry is not safe to pass over, and when acting on a member fails:
// a member nobody could read is a member nobody may reorder around.
func (w *mergeWalk) close(lane string) {
	if w.used == nil {
		w.used = map[string]int{}
	}
	w.used[lane] = w.limitOrDefault()
}

// claimHead makes taskID the head of lane, unless the lane already has
// one. Called for each queue member as it is read, in queue order, so the
// first member of each lane is its head.
func (w *mergeWalk) claimHead(lane, taskID string) {
	if w.heads == nil {
		w.heads = map[string]string{}
	}
	if _, taken := w.heads[laneRepo(lane)]; taken {
		return
	}
	if _, taken := w.heads[lane]; !taken {
		w.heads[lane] = taskID
	}
}

// releaseHead clears taskID from being the head of lane once its pull
// request has merged, so the next unmerged member in lane claims the head.
func (w *mergeWalk) releaseHead(lane, taskID string) {
	if w.heads != nil && w.heads[lane] == taskID {
		delete(w.heads, lane)
	}
}

// claimHeadOf is claimHead for a member this cycle could not read. Its
// declared lane is claimed where it has one; where it has none, which lane
// it heads is not known, so it heads every lane of its repository not yet
// headed -- nobody behind it is promoted past a change nobody could read.
func (w *mergeWalk) claimHeadOf(e queueEntry) {
	if laneBase(e) != "" {
		w.claimHead(laneOf(e), e.task.ID)
		return
	}
	if w.heads == nil {
		w.heads = map[string]string{}
	}
	repo := e.ref.Repo.String()
	if _, taken := w.heads[repo]; !taken {
		w.heads[repo] = e.task.ID
	}
}

// headOf is the head of lane, "" where nothing has claimed it yet.
func (w *mergeWalk) headOf(lane string) string { return headIn(w.heads, lane) }

// headIn reads lane's head out of a mergeWalk's heads.
func headIn(heads map[string]string, lane string) string {
	if head, ok := heads[lane]; ok {
		return head
	}
	return heads[laneRepo(lane)]
}

// closeEntry closes e's lane after acting on it failed. Where the lane has
// no branch to it -- a task that named no base, whose pull request nothing
// has read -- which lane it belongs to is not known, and the whole
// repository is closed instead: that is the conservative direction, and
// the per-repository behaviour this queue had before it had lanes.
func (w *mergeWalk) closeEntry(e queueEntry) {
	if laneBase(e) != "" {
		w.close(laneOf(e))
		return
	}
	if w.closedRepos == nil {
		w.closedRepos = map[string]bool{}
	}
	w.closedRepos[e.ref.Repo.String()] = true
}

// passedOver reports whether e is a queue member the queue can see is not
// merging as it stands, and which the entries behind it may therefore
// merge ahead of -- see mergeWalk.
//
// The first two cases are one situation at two moments. A conflicted or
// failing entry is one the queue is about to send back to an agent
// (advanceMergeQueueHead, this same cycle, if it is the head); an entry
// with a repair in flight is one it already has. Either way the next
// thing that can change its verdict is a push from a whole agent run, so
// there is nothing to be gained by making the changes behind it wait.
//
// The third is the exception mergeWalk's own doc comment reserves. An
// entry still deciding closes the slot, on the reasoning that it resolves
// on its own within a cycle or two and merging past it would reorder two
// changes on nothing better than which was quicker to answer. A cancelled
// verdict is the read that reasoning does not cover: it is PENDING
// (forge.HealthFrom's "cancelled is neither"), it is *finished*, and no
// cycle will ever change it -- only a re-run somebody asks for, or the
// stall deadline two hours away. Everything behind it used to wait out
// both. That is what grain/task-149 did to the whole of this
// repository's queue for a day, which is how the silence was noticed at
// all: not by the row that said nothing, but by every task behind it
// standing still.
//
// The fourth is a repository's own choice to give up the ordering itself
// (model.RepoConfig.MergeOnGreen, grain/task-545): there an entry whose CI
// is still running is passed over too, so a green, clean change behind it
// takes the slot and merges this cycle. The pending entry keeps its place
// and its waiting_ci row, and merges on a later cycle once its own
// verdict is green. Only PrPending: an entry this cycle could not read,
// or one whose state is unknown, still closes the lane.
func passedOver(e queueEntry) bool {
	if e.obs.RepairInFlight() {
		return true
	}
	if e.health == model.PrPending && e.mergeOnGreen {
		return true
	}
	if e.health == model.PrPending && verdictWithdrawn(e) {
		return true
	}
	return e.health == model.PrConflicted || e.health == model.PrFailing
}

// verdictWithdrawn reports whether the only thing holding e PENDING is a
// verdict that was cancelled rather than one that has not arrived --
// GitHub's own cancelled check runs, or a cancelled grain CI run.
//
// The "only" is what the second half is for. A commit with one cancelled
// check and one still running has an answer coming, and an answer coming
// is exactly the wait the queue keeps its position through: it may yet
// read clean on this very cycle's successor, and reordering around it
// would be reordering around a fast reader. So this holds solely when
// nothing outstanding remains -- at which point PENDING is not a wait but
// a gap, and the entries behind it are waiting on a re-run nobody has
// asked for.
func verdictWithdrawn(e queueEntry) bool {
	// Where grain CI is the gate, only grain CI's own state can tell the
	// two apart. GitHub's checks are either never read at all (a
	// repository whose gate is grain CI, queueEntry.gateIsGrainCI) or
	// read and not counted towards health (a task holding grain-ci), and
	// in neither case is an unfinished one "an answer coming": nothing
	// it eventually says will move this pull request. Waiting on it
	// would hold the entry's queue position for a verdict the gate has
	// already stopped listening to.
	if e.grain.Required {
		if e.grain.Running {
			return false
		}
		return e.grain.Cancelled
	}
	if e.grain.Running || len(unfinishedChecks(e.checks)) > 0 {
		return false
	}
	// A workflow run waiting on approval is the same gap: finished, and
	// no cycle will change it until a person approves the run.
	return e.grain.Cancelled || len(cancelledChecks(e.checks)) > 0 ||
		len(awaitingApprovalChecks(e.checks)) > 0
}

// actOrder is the order SyncPullRequests acts on this cycle's entries:
// everything that is not a queue member first, then the queue itself, in
// its own order (queueOrder).
//
// The queue half is what mergeWalk needs -- it decides who merges by
// working down a repo's queue from the front, which it can only do if the
// front is acted on first.
//
// Non-members going first is a smaller point, and an older one. A stacked
// change -- a review, or one of the separate fix tasks the queue used to
// file -- merges back into the branch of the task it was filed against,
// which is a push to a queue member's own pull request. Acting on it
// first means that member is read after its stack has landed rather than
// before, so it is judged on the commit that is actually there and can
// merge on the same cycle its review does.
func actOrder(entries []queueEntry) []queueEntry {
	members := queueOrder(entries)
	inQueue := make(map[string]bool, len(members))
	for _, e := range members {
		inQueue[e.task.ID] = true
	}
	ordered := make([]queueEntry, 0, len(entries))
	for _, e := range entries {
		if !inQueue[e.task.ID] {
			ordered = append(ordered, e)
		}
	}
	return append(ordered, members...)
}

// showQueueAtFrontOfBacklog moves every task waiting on the merge queue to
// the front of the backlog, in the order the queue will act on them
// (queueOrder), so grain's internal ordering is something a person can
// read off the task list rather than something only a cycle knows.
//
// Reconciled every cycle rather than written once when a task joins the
// queue: membership is a derived fact (isQueueMember, over auto-merge,
// fix-task and blocked-at state that all move on their own), so the only
// way for the front of the backlog to keep agreeing with it is to
// recompute both together. Store.MoveToFrontOfBacklog writes nothing when
// the order already holds, which is almost every cycle.
//
// Failing is not fatal to the sync it runs inside: what is lost is the
// backlog showing the right order for one cycle, and every merge decision
// here is made from queueOrder directly rather than from what was
// written.
func showQueueAtFrontOfBacklog(ctx context.Context, store *model.Store, members []queueEntry) error {
	if len(members) == 0 {
		return nil
	}
	ids := make([]string, len(members))
	for i, e := range members {
		ids[i] = e.task.ID
	}
	if err := store.MoveToFrontOfBacklog(ctx, ids); err != nil {
		return fmt.Errorf("orchestrator: moving the merge queue to the front of the backlog: %w", err)
	}
	return nil
}

// SyncPullRequests refreshes every completed task's tracked pull request,
// advances each merge queue lane (a repo and base branch) by one step, and closes out the
// tasks whose PR finished -- the other half of core.py's _close_finished_prs
// this package owns, plus the merge queue bwsalmon/agents#283 asked for in
// place of core.py's own _suggest_fix (a suggestion a human had to
// approve before the agent set would attempt it). See queueOrder,
// mergeWalk and syncEntry for the queue itself, and
// showQueueAtFrontOfBacklog for where it puts itself so a person can see
// it; this function only gathers the state every entry's decision needs
// before any of them act, so a decision about task N never depends on an
// in-progress change queueOrder has not seen yet.
func SyncPullRequests(ctx context.Context, store *model.Store, client github.Client, now time.Time) error {
	return SyncPullRequestsGuarding(ctx, store, client, nil, StateRepoGate{}, model.DefaultMaxMergesPerRepo, now)
}

// StateRepoGate is what this reconciler has to be told about the one
// repository a deployment keeps its own settings in, and it is empty for
// every caller that has none -- a test, `grain demo`, anything embedding
// this package, and every deployment whose state repository is checked
// by GitHub Actions like anybody else's.
//
// Both fields exist because a deployment that checks its own settings
// changes itself (pkg/staterepo/postcheck.go) makes its state repository
// the one repository in the queue whose rules are not GitHub's:
//
//   - Check is the check run it posts, and naming it here is what makes
//     that check's *absence* block rather than pass. See healthFrom.
//   - Merged is called after this reconciler merges into that
//     repository, so the deployment can take its own settings up now
//     rather than on whatever tick comes next -- the merge changed what
//     grain runs on, and until it is applied this host is still running,
//     and still exporting, the settings it had.
type StateRepoGate struct {
	Repo   model.RepoRef
	Check  string
	Merged func()
	// CI receives the release event caused by every merged pull request. The
	// run is against the destination branch's new head, so an `on: release`
	// workflow is repository-agnostic and fires for both Grain-managed and
	// externally merged pull requests when Grain next observes them.
	CI ci.Runner
	// Porter makes the ports a merge onto a paired branch calls for
	// mechanically where it can (port.go); nil files every one as an
	// agent task.
	Porter Porter
}

// requiredCheckFor is the gate's answer for one pull request: the check
// name for a pull request against the state repository, and nothing for
// anywhere else. A gate with a repository and no check name says
// nothing, which is the deployment that has a state repository and does
// not post its own check -- almost all of them.
func (g StateRepoGate) requiredCheckFor(ref model.PullRequestRef) string {
	if g.Check == "" || g.Repo == (model.RepoRef{}) || ref.Repo != g.Repo {
		return ""
	}
	return g.Check
}

// merged tells the gate a pull request has just been merged, if it was
// one of the state repository's and if anybody is listening.
func (g StateRepoGate) merged(ref model.PullRequestRef) {
	if g.Merged == nil || g.Repo == (model.RepoRef{}) || ref.Repo != g.Repo {
		return
	}
	g.Merged()
}

// SyncPullRequestsGuarding is SyncPullRequests told which repository is
// this deployment's own state repository and what that means for it --
// see StateRepoGate. It is the entry point the daemon uses
// (reconcileSync); the plain SyncPullRequests is the same reconciler
// with no such repository, which is what every other caller has.
func SyncPullRequestsGuarding(ctx context.Context, store *model.Store, client github.Client,
	verdicts forge.Verdicts, gate StateRepoGate, maxMergesPerRepo int, now time.Time) error {

	if err := advanceMergeRechecks(ctx, Deps{Store: store, Client: client, CI: gate.CI}, now); err != nil {
		return err
	}

	// A task moved to another repository after a run had opened a pull
	// request in the old one carries a second LinkFixes link it no
	// longer owns. OpenPullRequestLinks already leaves it out; this takes
	// it off the task for good, so the task's page, its Submit and every
	// other reader of its links agree on the one pull request it has.
	pruned, err := store.PruneOffTargetPullRequestLinks(ctx)
	for _, l := range pruned {
		log.Printf("orchestrator: dropped %s from task %s: it is in a repository the task no longer targets",
			l.PullRequest, l.TaskID)
	}
	if err != nil {
		log.Printf("orchestrator: dropping off-target pull request links: %v", err)
	}

	links, err := store.OpenPullRequestLinks(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading open pull request links: %w", err)
	}

	// The gather loop is not isolated per entry the way the act loop
	// below is, and deliberately so: queueOrder and the walk decide who is
	// at the front of each lane by comparing entries against each other,
	// so acting on a set with an entry silently missing from it could
	// promote the task behind the real head and merge two changes in the
	// wrong order. A store read failing here is systemic anyway (the same
	// database answers every one of these), so the whole reconciler backs
	// off to the next tick rather than advancing a queue it cannot see all
	// of.
	//
	// A link whose ref will not parse is the one exception, because it is
	// not a transient failure: nothing can ever be done with a pull
	// request whose repo and number are unreadable, so it can never be
	// anyone's head, and letting one malformed row wedge every other
	// repo's queue forever is strictly worse than skipping it and
	// reporting it every cycle.
	// Which of these tasks has an agent on it right now, read once for
	// the whole set rather than per entry: one row per live run is a
	// handful on the busiest deployment, where a query per open pull
	// request is the size of the queue. See queueEntry.running.
	live, err := store.LiveRuns(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading live runs: %w", err)
	}
	running := make(map[string]bool, len(live))
	for _, r := range live {
		running[r.TaskID] = true
	}

	entries := make([]queueEntry, 0, len(links))
	var errs []error
	// The deployment-wide half of "are GitHub's checks a source of
	// verdicts here", read once for the cycle: a whole deployment can be
	// in that state at once -- an Actions bill that has stopped being
	// paid stops every repository under the account, not one -- and a
	// per-repository row that has to be written for each of them is a
	// row somebody forgets. A repository's own setting can only turn it
	// on, never off again, which is the same union rule
	// DefaultCapabilities is under and for the same reason: a repo layer
	// that could subtract would let a stale row re-enable a provider the
	// deployment has already said is dead.
	deploymentGateIsGrainCI, err := deploymentIgnoresGitHubCI(ctx, store)
	if err != nil {
		return err
	}
	// Who is being re-checked on top of the base branch right now
	// (mergerecheck.go), read once for the cycle.
	rechecking, err := recheckingTaskHeads(ctx, store)
	if err != nil {
		return err
	}

	// Every task this cycle is watching a pull request for, whether or
	// not an entry gets built for it below -- see clearStaleQueueState,
	// which takes the queue's own account off the rows that are not in
	// here.
	watched := make(map[string]bool, len(links))
	for _, link := range links {
		watched[link.TaskID] = true
	}
	// One read per repository rather than one per entry: whether this
	// repository wants its branches up to date is a fact about the
	// repository, and a queue of thirty entries against one repo would
	// otherwise ask the same question thirty times a cycle.
	upToDate := map[model.RepoRef]bool{}
	onGreen := map[model.RepoRef]bool{}
	grainCIGate := map[model.RepoRef]bool{}
	ciModes := map[model.RepoRef]model.CIMode{}
	unfixableChecks := map[model.RepoRef][]string{}
	mergeRules := map[model.RepoRef]*model.MergeRules{}
	for _, link := range links {
		task, err := store.GetTask(ctx, link.TaskID)
		if err != nil {
			return fmt.Errorf("orchestrator: reading task %s: %w", link.TaskID, err)
		}
		if task == nil {
			continue
		}
		obs, err := store.GetObservation(ctx, link.TaskID)
		if err != nil {
			return fmt.Errorf("orchestrator: reading observation for %s: %w", link.TaskID, err)
		}
		ref, err := model.ParsePullRequestRef(link.PullRequest)
		if err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: task %s: %w", link.TaskID, err))
			continue
		}
		if _, read := upToDate[ref.Repo]; !read {
			strict, err := repoRequiresBranchUpToDate(ctx, store, ref.Repo)
			if err != nil {
				return err
			}
			upToDate[ref.Repo] = strict
			eager, err := repoMergesOnGreen(ctx, store, ref.Repo)
			if err != nil {
				return err
			}
			onGreen[ref.Repo] = eager
		}
		if _, read := grainCIGate[ref.Repo]; !read {
			mode, err := repoCIMode(ctx, store, ref.Repo, deploymentGateIsGrainCI)
			if err != nil {
				return err
			}
			ciModes[ref.Repo] = mode
			grainCIGate[ref.Repo] = (mode == model.CIModeGrain)
		}
		if _, read := unfixableChecks[ref.Repo]; !read {
			declared, err := repoUnfixableChecks(ctx, store, ref.Repo)
			if err != nil {
				return err
			}
			unfixableChecks[ref.Repo] = declared
		}
		if _, read := mergeRules[ref.Repo]; !read {
			rules, err := store.MergeRulesOf(ctx, ref.Repo)
			if err != nil {
				return err
			}
			mergeRules[ref.Repo] = rules
		}
		entries = append(entries, queueEntry{
			rules:  mergeRules[ref.Repo],
			runner: gate.CI,
			task:   *task, obs: obs, ref: ref, requiredCheck: gate.requiredCheckFor(ref),
			verdicts: verdicts, running: running[link.TaskID],
			requireUpToDate:   upToDate[ref.Repo],
			mergeOnGreen:      onGreen[ref.Repo],
			gateIsGrainCI:     grainCIGate[ref.Repo],
			ciMode:            ciModes[ref.Repo],
			declaredUnfixable: unfixableChecks[ref.Repo],
			recheckingHead:    rechecking[link.TaskID],
			paused:            taskPaused(ctx, store, link.TaskID),
		})
	}

	// The queue, in the order it will act: computed once against the whole
	// set, and put where a person can see it before anything acts on it.
	members := queueOrder(entries)
	if err := showQueueAtFrontOfBacklog(ctx, store, members); err != nil {
		errs = append(errs, err)
	}
	// The order everything acts in is fixed before the newcomers are
	// stamped: once stamped they read as established, and their in-memory
	// OrderKeys predate the write above.
	acting := actOrder(entries)
	if err := stampQueueEntry(ctx, store, members, now); err != nil {
		errs = append(errs, err)
	}

	// Acting on one entry is isolated, because head-of-queue was already
	// decided above against the complete set: an entry that fails here
	// cannot make another entry merge that would not have merged anyway
	// (syncEntry merges an ordinary queue member only while its repo's
	// merge slot is still open, and a failure closes that slot rather than
	// opening it), so the worst a failure costs the others is a cycle of
	// latency -- which is what returning early used to cost all of them.
	//
	// The order is actOrder's, and the walk is what carries one entry's
	// answer to the entries behind it: who merges cannot be settled up
	// front the way heads can, because it turns on health nobody has read
	// yet.
	if maxMergesPerRepo < 1 {
		maxMergesPerRepo = model.DefaultMaxMergesPerRepo
	}
	walk := &mergeWalk{limit: maxMergesPerRepo}
	// One cycle's memory of what has been concluded about each
	// repository, so that the entries behind a broken head do not each
	// rediscover -- at the price of a base merge, an agent run and a full
	// CI suite apiece -- what the one in front of them already proved
	// (checkstartups.go).
	find := newRepoFinder(client)
	for _, e := range acting {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		// An entry nothing is happening to is read less and less often
		// (syncpoll.go); the queue's head never is.
		if !syncDue(store, e, walk, now) {
			syncSkipped(e, walk)
			continue
		}
		if err := syncEntry(ctx, store, client, e, gate, walk, find, now); err != nil {
			errs = append(errs, err)
			if isQueueMember(e) {
				walk.closeEntry(e)
			}
		}
	}
	// What the queue said about the tasks it is no longer watching, taken
	// back: every state above is written from this loop and corrected by
	// the next pass through it, so a task that has left the watched set
	// is one whose row nothing will ever correct again.
	if err := clearStaleQueueState(ctx, store, watched, now); err != nil {
		errs = append(errs, err)
	}
	// How long each base branch was red, recorded as a hold pass that
	// held nothing (basehold.go) -- only for a walk that got to the end,
	// as it was when the walk counted what it held.
	if ctx.Err() == nil {
		if err := recordBaseHolds(ctx, store, nil, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// syncEntry is SyncPullRequests' per-task decision: refresh e's PR health,
// act on it, and close the task out if the PR itself is done. It also
// hands its repo's merge slot on (mergeWalk) -- either taking it, or
// closing it behind itself, or leaving it open for the entry behind.
func syncEntry(ctx context.Context, store *model.Store, client github.Client,
	e queueEntry, gate StateRepoGate, walk *mergeWalk,
	find *repoFinder, now time.Time) error {

	task, ref := e.task, e.ref
	e.policyFor = func(headSHA string) *model.EffectiveMergePolicy {
		return mergePolicyFor(ctx, store, client, &e, headSHA, now)
	}
	if err := e.observe(client, now); err != nil {
		if isQueueMember(e) {
			walk.claimHeadOf(e)
		}
		// Say on the row that this cycle could not read the pull request,
		// rather than returning under whatever the row last said and
		// leaving that standing for as long as the reads keep failing
		// (noteUnreadableEntry). The read error is what the cycle
		// reports either way.
		return errors.Join(noteUnreadableEntry(ctx, store, e, err, now), err)
	}
	// What GitHub's CI concluded, recorded from the read just made rather
	// than by a read of its own (githubci.go).
	recordPolledChecks(ctx, store, ref.Repo, e.detail.HeadRef, e.detail.HeadSHA, e.checks, now)
	syncRead(store, e, now)
	if e.detail.State == "open" && !e.detail.Merged {
		if updated, retargeted, err := retargetTaskBase(ctx, store, client, task, e.detail.BaseRef); err == nil && retargeted {
			task = updated
			e.task = updated
		}
	}
	// Its place at the front of its lane, if it is first there, taken now
	// that the read has said which lane that is, and before anything below
	// can return: a member the queue is holding for some other reason is
	// still the one in front (mergeWalk's heads).
	if isQueueMember(e) {
		walk.claimHead(laneOf(e), task.ID)
	}
	// detail and health are the only two of e's observations that change
	// after that read: merging settles the pull request, and the merge arm
	// re-reads both. The rest are used as observe left them.
	detail, health := e.detail, e.health
	checks, checksKnown, checksSettled := e.checks, e.checksKnown, e.checksSettled
	grain := e.grain
	stalled, stallDeadline := e.stalled, e.stallDeadline
	var err error

	// The security hold's classification is about one commit
	// (securityhold.go). A head that moved since -- a stacked task merged
	// back into this branch, a push nobody dispatched -- is classified
	// again before anything below can merge it, and if that takes the
	// change's auto-merge away, this cycle is over for it: e still says
	// auto-merge is on.
	//
	// Not while this task's own run is still in flight, though: that run
	// is still pushing (a merge of its base first, then the fixes it was
	// sent for), its finish classifies whatever head it leaves
	// (finishWithPullRequest), and nothing below merges a branch somebody
	// is working on. Classifying here filed a review of every
	// intermediate head (task 447: a merge commit classified nine seconds
	// before the run pushed its fixes on top). A forced merge is the one
	// thing that does merge a running task's branch, so it is still
	// classified first.
	midRun := e.running && forcedMergeOf(e.obs) == nil
	if detail.State == "open" && !detail.Merged && !midRun {
		// A human's Submit is pinned to the head it shipped first, so the
		// base-branch merges below carry it rather than re-hold it
		// (securityhold_submit.go).
		pinned, ok, err := pinHumanSubmit(ctx, store, client, task, ref, detail.HeadSHA, now)
		if err != nil || !ok {
			return err
		}
		task = pinned
		cleared, err := clearedForHead(ctx, store, client, task, ref, detail.HeadSHA, now)
		if err != nil {
			return err
		}
		if !cleared {
			return nil
		}
		// And the repository's own pull-request triggers, judged by the
		// base's rules against a head they have not seen yet
		// (pathtriggerhold.go).
		current, err := pathTriggersCurrentFor(ctx, store, client, task, ref, detail.HeadSHA, now)
		if err != nil {
			return err
		}
		if !current {
			return nil
		}
		// And whoever a sensitive path or grain's own guard named: a
		// change still owed an approver's Submit does not merge, however
		// its auto-merge came to be on (model's sensitive_path.go).
		if held, err := withdrawForPendingApprovals(ctx, store, client, task, ref, now); err != nil || held {
			return err
		}
		// And the repository's merge rules, where they say a person has
		// to press Submit (`auto_merge: never`, mergerules.go).
		if held, err := withdrawForMergePolicy(ctx, store, client, e.task, ref, e.policy, now); err != nil || held {
			return err
		}
	}

	// A repair or a base-merge refresh that left nothing to merge: the
	// work already landed some other way (a squash merge done by hand,
	// gke-labs/granule task 147), and what the queue would merge now is
	// an empty merge commit. Closed instead, with where the work landed
	// said on it, and the task marked done by the closing write below
	// (closeIfLanded). Asked only of an entry the queue has written to
	// or sent back to an agent, and not while that agent is still on the
	// branch; the merge arm below asks it of every other entry at the
	// moment it would merge.
	if !e.running && !e.obs.RepairInFlight() &&
		((e.obs != nil && (e.obs.MergeQueueRepairAt != nil ||
			e.obs.MergeQueueRefreshedAt != nil ||
			e.obs.MergeQueueBlockedAt != nil ||
			e.obs.MergeQueueState == mergeStateFailed)) ||
			health == model.PrConflicted ||
			isPortTask(task)) {
		closed, err := closeIfLanded(ctx, store, client, task, ref, detail, now)
		if err != nil {
			return err
		}
		if closed {
			return closeOutLanded(ctx, store, task, e.obs, ref, detail, now)
		}
	}

	isFixTask := task.Origin.Reason == model.ReasonFix
	// The lane this entry merges in, as this cycle read it: the slot it
	// takes or closes is its own branch's and no other's (mergeWalk).
	lane := laneOf(e)
	isHead := walk.headOf(lane) == task.ID
	// isMergeHead is the narrower question: is this the entry that takes
	// its repo's one merge this cycle? Usually the head -- but a head that
	// is conflicted, failing or being repaired is passed over instead, so
	// that a green change behind it does not wait out somebody else's
	// conflict. See mergeWalk.
	member := isQueueMember(e)
	isMergeHead := member && walk.mayMerge(lane)
	if member && !passedOver(e) && health != model.PrClean {
		walk.close(lane)
	}
	blocked := blockedEntry(e)
	rechecking := e.recheckingHead != "" && e.recheckingHead == detail.HeadSHA
	// A repair the queue asked for is still being worked on this very
	// branch (requeueForRepair), so a task in that state does not merge:
	// its checks are green about a tree an agent is still pushing to, and
	// waiting one cycle costs nothing next to landing half a resolution.
	// The walk already passes such an entry over, so this is belt and
	// braces for the merge arm below; what it is load-bearing for is the
	// stall escalation, which is asked of the queue head rather than of
	// whoever is merging.
	// Neither stops a *blocked* task merging, below -- there the queue
	// has already given up on the repair and a clean pull request is
	// somebody having fixed it by hand.
	repairing := e.obs.RepairInFlight()
	// The same sentence about a run nobody here asked for: somebody has
	// put an agent back on this task themselves (ui.Client.RunNow), so
	// its branch is being pushed to whatever its checks currently say.
	//
	// Mostly such a task is not read by this reconciler at all, and that
	// is the first line of the same defence: asking for a run clears the
	// task's completion, which takes it out of
	// Store.OpenPullRequestLinks. What still reaches here is the task
	// that has been through the queue's own repair, since the stamp from
	// that repair keeps it in the list (that query's third arm) -- and
	// that is exactly the task somebody is most likely to run now, the
	// one the queue tried, gave up on, and left for a person.
	//
	// It stops a *blocked* task merging, unlike repairing above: the
	// queue having given up says nothing about a run in flight now, and
	// what a merge would cut short is the same either way.
	working := e.running

	// stacked: a task filed onto another task's branch and merged back
	// into it rather than into the repo's own base -- a fix task, or a
	// review (grain/task-284). isQueueMember excludes both, so neither
	// ever waits for a head position, and neither is a head that could be
	// repaired or escalated.
	stacked := isFixTask || isReviewTask(task)

	// The review this task declared, if it has one and it has not landed
	// yet (SyncReviews is what files it). A change waiting on its own
	// review does not merge, clean or not and head or not: attaching a
	// review means it happens before the merge rather than after.
	//
	// Only for an ordinary queue member whose pull request is still
	// open. A stacked task is never itself reviewed; one the queue has
	// already given up on is held by nothing -- blocked means exactly
	// that this task merges the moment it reads clean and waits on
	// nobody; and a pull request that has already merged or closed is
	// past the point a review could hold anything back, so asking would
	// only risk announcing that a review is overdue on a task this same
	// cycle is about to close out.
	settled := health == model.PrMerged || health == model.PrClosed
	// Which of this entry's red is its own and which is the
	// repository's, decided before anything is said about it or done to
	// it: the row says which kind it is, the head's own arm spends
	// nothing on red no push can reach, and a repair that is dispatched
	// anyway tells its agent which of the two it is being sent for.
	//
	// Asked only where the answer could change something, since it can
	// cost a GitHub read: a task the queue is not driving (no
	// /auto-merge), a stacked one that merges into another task's branch
	// rather than into the repository's own, a pull request that has
	// already settled and one the queue has already given up on are each
	// entries nothing below decides anything about.
	if e.task.AutoMerge && !stacked && !blocked && !settled {
		e.reds, e.unfixable = splitReds(e, find)
	}
	var review reviewState
	if !stacked && !blocked && !settled {
		review, err = reviewStateOf(ctx, store, task, e.obs, now)
		if err != nil {
			return err
		}
	}

	// Keep the row honest while grain, rather than an agent, owns the
	// work: say what this task is waiting for, in the words a person
	// would use, before anything is done about it (statusOf, which has
	// the whole vocabulary). Everything it reads has been decided by
	// here and nothing here calls GitHub, so the row is written before
	// the one call in this function that can block long enough for
	// somebody to be looking at that row while it does.
	status := statusOf(e, isMergeHead, blocked, review, walk.heads)
	// A change undergoing an overlap re-check has its row written by
	// setRecheckState when the re-check started.
	if !rechecking || settled || health != model.PrClean || !task.AutoMerge || working {
		status.reason = mergeReasonOf(e, status.state, gate.CI)
		if err := setMergeQueueState(ctx, store, e.obs, task.ID, status, now); err != nil {
			return err
		}
	}
	// The refusal record is about one clean pull request grain is trying
	// to merge and cannot; a pull request that is not clean has something
	// else wrong with it and other machinery for it, so the run of
	// refusals ends here and the next clean read starts a fresh one --
	// the same discipline ForgetPendingChecks keeps for the stall clock.
	if health != model.PrClean {
		forgetMergeRefusals(ref)
	}

	// A stacked task -- a fix task, which is the separate repair branch
	// the queue used to file and which nothing creates any more though a
	// database may still carry one of in flight, or a review -- always
	// merges into the branch it was filed against the moment it reads
	// clean, unconditionally, the same as every AutoMerge task did before
	// this package had a queue at all: it is not itself a queue entry
	// (isQueueMember excludes it), so it is never "blocking" a repo the
	// way a stuck top-level task would. A task the queue already gave
	// up on (blocked) gets the same unconditional treatment: it stopped
	// being anyone's queue head so it cannot hold the queue back, but it
	// still lands the moment a human's own push makes it clean. Anything
	// else -- an ordinary queue member -- only merges once it is its
	// repo's merge head: merging task 2 while task 1 is still ahead of it
	// and still deciding would defeat the reason to queue at all. Task 1
	// having *stopped* deciding -- conflicted, failing, or away being
	// repaired, none of which resolve without an agent's own run -- is
	// the exception mergeWalk makes, and the reason a conflict at the
	// front of a repo's queue no longer holds up everything behind it.
	//
	// PrPending matches none of these arms until the last one, and
	// PrUnknown matches none of them at all: CI is still running, or has
	// not reported for the first time yet, or its verdict is unreadable
	// -- so in every case there is nothing to merge yet and nothing to
	// file a fix for. The entry is simply left where it is -- still the
	// head of its queue, holding the position -- and the next cycle asks
	// GitHub again. That is what makes a merge wait for the tests rather
	// than race them, and it is deliberately the same "do nothing, look
	// again" the asynchronous Mergeable read has always taken.
	//
	// "Look again" has to stop somewhere, though, and the last arm is
	// where: a head whose checks have been unfinished for longer than
	// defaultCheckStallDeadline is one nothing is going to resolve on its
	// own, so the queue says so and moves on rather than holding its own
	// head position forever with nothing said to anyone. PrUnknown has no
	// such arm on purpose -- the thing that produces it for cycle after
	// cycle is a credential that cannot read checks, which is one fact
	// about the whole deployment (ChecksUnavailable, and the one log line
	// that reports it) rather than something wrong with any one of the
	// pull requests it would otherwise comment on individually.
	//
	// An operator's own override outranks the first four arms outright,
	// which is what `forced` is doing in each of their conditions
	// (forcedmerge.go, and mergepredicate.go for the rule). Every one of
	// them is grain deciding to wait -- for a review, for a batch it is
	// testing this change in, for the gather window -- and a person who
	// has said "merge this anyway, here is why" is not asking grain to
	// weigh those again. The predicate below still gets the last word,
	// and it still refuses a pull request that does not merge cleanly.
	//
	// A forced merge of a pull request that has already merged or closed
	// is not a merge at all, so `settled` holds the override the way it
	// holds everything else: there is nothing left to land.
	//
	// A conflicted pull request is left to the ordinary machinery too,
	// and that is the sharp end of "a conflict is still a refusal". The
	// predicate would turn the override down anyway (mayMerge's own first
	// arm), and taking this arm to be turned down would cost the change
	// the repair that is the thing most likely to resolve the conflict --
	// an override that quietly stopped the conflict being fixed would be
	// the worst of both. The stamp stays outstanding and lands the moment
	// the branch merges cleanly with the head the operator reviewed --
	// the base moving back out of the conflict, say.
	//
	// What it never does is outlive that head (bwsalmon/grain#2399). A
	// push after the override is code nobody reviewed, a repair resolving
	// the conflict included, so a branch that has moved has its override
	// withdrawn here and said so on the task, and the change goes through
	// the ordinary arms below like any other. An empty head is GitHub not
	// having filled the sha in yet: nothing can be pinned to it, so the
	// override waits for a cycle that can tell rather than being spent on
	// one that cannot.
	forced := forcedMergeOf(e.obs)
	if forced != nil && !settled && detail.HeadSHA == "" {
		forced = nil
	} else if forced != nil && !settled && forced.movedSince(detail.HeadSHA) {
		if err := dropMovedForcedMerge(ctx, store, task.ID, *forced, detail.HeadSHA, now); err != nil {
			return err
		}
		forced = nil
	}
	forcing := forced != nil && !settled && health != model.PrConflicted
	switch {
	case !forcing && review.overdue:
		// The review is not arriving. Say so, give up the wait, and leave
		// the merge itself to the next cycle -- which reads a task the
		// store now says is blocked, and merges it the moment it reads
		// clean the way it does any other blocked task.
		if err := escalateUnfinishedReview(ctx, store, task, ref, review.taskID, now); err != nil {
			return err
		}

	case !forcing && review.pending:
		// Nothing to do but wait: the review is still running, or its own
		// pull request has not merged back into this branch yet. Holding
		// the head position is the point -- this change is next in its
		// repo, and it is not ready. No fix is filed and no stall
		// escalated either, deliberately: the agent already reading this
		// branch was sent to fix what is wrong with it, so a second one
		// filed onto the same branch at the same time would be two
		// stacked repairs of one change.

	case !forcing && health == model.PrClean && task.AutoMerge && !working && rechecking:
		// Being re-checked on top of the base branch (mergerecheck.go):
		// this change merges when that run reports green. The lane closes
		// behind it so later changes do not jump ahead while it is tested.
		if member {
			walk.close(lane)
		}

	case forcing || (health == model.PrClean && task.AutoMerge && !working &&
		(stacked || blocked || awaitingGitHubApproval(ref) || (isMergeHead && !repairing))):
		// A merge GitHub has refused this head before is asked for again
		// only when what it was refused for may have changed -- an
		// approval arriving, required checks passing, a person's Retry
		// merge, or a bounded backoff for a refusal nobody could place
		// (holdRefusedMerge) -- never once a cycle. A head held for a
		// retry keeps its place; the slot closes behind it.
		if !forcing && holdRefusedMerge(client, e, detail, blocked, now) {
			if member {
				walk.close(lane)
			}
			break
		}
		// "Clean" is GitHub's word for a pull request that merges without
		// a conflict, and on this deployment that is all it has to be: a
		// branch behind its base merges as it stands, on the full-suite
		// verdict it already holds, because bringing it up to date would
		// make a new head and throw that verdict away (grain/task-198).
		// Only a repository that has asked for the strict rule
		// (model.RepoConfig.RequireBranchUpToDate) has its branch caught
		// up first, and then this cycle merges nothing -- see
		// bringHeadUpToDate, which is where both halves are reasoned
		// about.
		// Not for a forced merge: catching the branch up makes a new head,
		// which throws away whatever verdict it had and starts another
		// suite -- the opposite of what the person who asked for this
		// merge to happen now asked for, and a delay they cannot see the
		// reason for.
		if !stacked && !forcing && e.requireUpToDate {
			held, err := bringHeadUpToDate(ctx, store, client, task, e.obs, ref, detail, now)
			if err != nil {
				return err
			}
			if held {
				// Nothing merged, and nothing behind this entry may merge
				// past it either: it is still this repository's head, it
				// is still first in line, and what is happening to it is
				// a write the queue itself made a moment ago.
				if member {
					walk.close(lane)
				}
				break
			}
		}
		// The predicate, asked once, immediately before the merge
		// (mergepredicate.go). Everything above this line decides which
		// entry is being *offered* a merge -- the queue's order, the
		// repository's slot, whose turn it is -- and this decides whether
		// it may take it.
		//
		// What it is load-bearing for here is the lock: while a batch is in
		// flight for this branch, the only change that may land is the head
		// of that batch's frontier. The batch's own reconciler holds its
		// members out of this path already (e.batched, and it runs first on
		// every cycle), but a change that is no member of the batch at all
		// -- one the queue has given up on and a person has since made
		// clean, a stacked task filed onto this branch -- reaches here
		// past that hold, and landing it would leave the batch's members
		// completing a tree nobody tested.
		//
		// No branch tip is read for it: that would be a GitHub call per
		// entry per cycle, and the only decision it could change is
		// whether an already-green batch's frontier head may merge -- a
		// question the batch's own reconciler answers with the tip in
		// hand, on the cycle the verdict arrives, before this one runs.
		decision, err := mergeDecisionFor(ctx, store, e, detail, health, forced)
		if err != nil {
			return err
		}
		if !decision.ok {
			if err := setMergeQueueState(ctx, store, e.obs, task.ID,
				queueStatus{state: mergeStateWaitingQueue, note: decision.why}, now); err != nil {
				return err
			}
			// Nothing behind it merges past it either: what is holding
			// this entry is a statement about the branch rather than about
			// the change.
			if member {
				walk.close(lane)
			}
			break
		}
		// The overlap re-check (mergerecheck.go): asked of every change
		// merging on its own green into a queue base -- not of a forced
		// merge, which is an operator's override of exactly this kind of
		// wait, and not of a stacked task, which merges into another pull
		// request's branch rather than into a base anything else lands on.
		// In observe mode, the default, it only records; in enforce mode
		// a change whose diff overlaps what has landed since its tested
		// tree is not merged here but re-checked on top of the branch as
		// a merge batch of one, which holds the branch like any batch and
		// lands it on the frontier arm if it passes.
		var recheck singleRecheck
		if !stacked && !forcing && decision.arm == mergeArmOwnHead {
			recheck, err = singleMergeRecheck(ctx, store, client, gate.CI, e, detail, now)
			if err != nil {
				return err
			}
			if recheck.held != "" {
				// A batch just started has written its own row
				// (setBatchState); anything else is said here.
				if recheck.held != "started" {
					if err := setMergeQueueState(ctx, store, e.obs, task.ID,
						queueStatus{state: mergeStateWaitingQueue, note: recheck.held}, now); err != nil {
						return err
					}
				}
				if member {
					walk.close(lane)
				}
				break
			}
		}
		// Pinned to the commit the verdict above was computed for, not
		// left to land on whatever the head branch points at by the time
		// GitHub processes this. The two are the same commit right up
		// until they are not: a push arriving between the reads above and
		// this call -- a fix task merging into this branch, a human's own
		// "push a fix by hand" that escalateToUser asked for, a
		// redispatched task pushing again -- would otherwise merge code
		// whose CI this cycle never read, which is the one thing waiting
		// for CI at all exists to prevent.
		//
		// GitHub refuses that with 409 (MergePullRequest's own doc
		// comment), and refusing is the whole point: the error goes back
		// to SyncPullRequests, which is already built to have one entry
		// fail without disturbing the others, and the next cycle reads
		// the commit that is there now and judges it on its own CI.
		//
		// An empty HeadSHA merges unpinned, as before. There is nothing
		// to pin to, and it is very nearly unreachable anyway: a PR
		// GitHub has not filled the sha in for reads PENDING on an empty
		// check list (emptyChecksSettled) rather than clean.
		// Said before the call because the call can block long enough for
		// the UI poller to observe it -- but not over a refusal already
		// on the row, because this call is then the next of those
		// refusals rather than a fresh attempt, and "merging" written
		// over it once a cycle would restart the age on a wait that has
		// not restarted. That flicker is the whole shape of the
		// complaint: a row that says "Merging" forever and means
		// "refused, again, since this morning".
		// An empty diff is never merged, whatever brought it here: see
		// closeIfLanded.
		if closed, err := closeIfLanded(ctx, store, client, task, ref, detail, now); err != nil {
			return err
		} else if closed {
			if member {
				walk.close(lane)
			}
			return closeOutLanded(ctx, store, task, e.obs, ref, detail, now)
		}
		// A head already handed to GitHub's merge queue is not merged
		// again: GitHub has said it will not, and asking once a cycle is
		// the refusal loop githubqueue.go exists to replace.
		_, onGitHubQueue := lastGitHubQueued(ref, detail.HeadSHA)
		if _, refused := lastMergeRefusal(ref, detail.HeadSHA); !refused && !onGitHubQueue {
			if err := setMergeQueueState(ctx, store, e.obs, task.ID,
				queueStatus{state: mergeStateMerging}, now); err != nil {
				return err
			}
		}
		var mergeErr error
		if !onGitHubQueue {
			mergeErr = mergeTaskPullRequest(ctx, store, client, ref.Repo, ref.Number, detail.HeadSHA, task.ID, detail.Title)
			if mergeErr != nil && (github.ClassifyMergeRefusal(mergeErr) == github.RefusalUnresolvedThreads || github.IsMergeRuleStatus(mergeErr)) {
				if resolveOrClassifyUnresolvedThreadsOnMerge(ctx, store, client, task, ref) {
					mergeErr = mergeTaskPullRequest(ctx, store, client, ref.Repo, ref.Number, detail.HeadSHA, task.ID, detail.Title)
				}
			}
		}
		// A base branch that only takes changes through GitHub's merge
		// queue: the pull request is put in that queue instead, and lands
		// when GitHub merges it -- this entry keeps its repository's slot
		// until then (githubqueue.go).
		if handled, err := landThroughGitHubQueue(ctx, store, client, e, ref, detail, mergeErr, now); handled && err == nil {
			if member {
				walk.close(lane)
			}
			break
		} else if err != nil {
			// A merge GitHub will not do is the one dead end the queue
			// used to retry forever in silence, with the row left saying
			// "Merging" -- refusedMerge is what gives it words and an end.
			return refusedMerge(ctx, store, client, e, ref, detail, err, now)
		}
		// This run of refusals, if there was one, is over: the next one
		// is a fresh story with its own clock.
		forgetMergeRefusals(ref)
		// What the overlap rule said about the merge that just happened,
		// for the rollout's counters (model.MergeRecheckRecord).
		if recheck.verdict != nil {
			recordRecheck(ctx, store, ref.Repo, detail.BaseRef, ref.Number, task.ID,
				recheck.mode, *recheck.verdict, recheck.outcome, now)
		}
		// A merge that happened because a person said so is recorded as
		// such: the override spent, the reason and what it skipped said on
		// the task and on the pull request, and one more row in the count
		// that makes "forcing should be rare" checkable (forcedmerge.go).
		//
		// Its failures are logged rather than returned, unlike everything
		// else in this arm. The merge has already happened; returning here
		// would leave the task un-closed-out over a comment that could not
		// be posted, which is grain's state going stale over the very
		// machinery that exists to stop it going stale.
		if forcing {
			if err := recordForcedMerge(ctx, store, client, task, e.obs, ref,
				detail.HeadSHA, detail.BaseRef, *forced, now); err != nil {
				log.Printf("orchestrator: %v", err)
			}
		}
		// The merge above may have already settled the PR; re-read rather
		// than assume, since GitHub applies it asynchronously the same way
		// it computes Mergeable asynchronously (detail.Mergeable's own doc
		// comment).
		detail, err = client.GetPullRequest(ref.Repo.Owner, ref.Repo.Name, ref.Number)
		if err != nil {
			return fmt.Errorf("orchestrator: re-reading %s after merge: %w", ref, err)
		}
		health = healthOf(detail, checks, e.requiredCheck, checksKnown, checksSettled, grain)
		if member {
			// Under MergeRecheckEnforce, each entry in the lane is checked
			// against the live branch tip before merging, so a landed merge
			// does not spend the un-rechecked per-cycle limit.
			if !detail.Merged || recheck.verdict == nil || !recheck.verdict.hasFiles || recheck.mode != model.MergeRecheckEnforce {
				walk.merged(lane)
			}
			if detail.Merged && walk.mayMerge(lane) {
				walk.releaseHead(lane, task.ID)
			}
		}
		gate.merged(ref)

	case !stacked && !blocked && (health == model.PrFailing || health == model.PrConflicted):
		// Both standalone CI failure and base branch merge conflicts trigger
		// repairs immediately for queued pull requests without waiting for them to reach
		// the head of the merge queue, allowing repair agents to resolve conflicts in parallel.
		if err := advanceMergeQueueHead(ctx, store, client, e, isHead, find, now); err != nil {
			return err
		}

	case isHead && !stacked && !blocked && !repairing && health == model.PrPending && grainCIGivenUp(grain):
		// grain has stopped starting runs for this commit, so nothing is
		// going to end this wait: said now rather than after the stall
		// deadline, which would leave the row reading "Waiting for CI"
		// for two hours with no CI in flight (grain/task-480).
		if err := escalateGivenUpGrainCI(ctx, store, task, ref, checks, grain, now); err != nil {
			return err
		}

	case isHead && !stacked && !blocked && !repairing && stalled:
		// A grain CI stall is handed to a person (merge_failed, the
		// Inbox's "Unblock a merge"). The daemon no longer retries it or
		// extends its deadline on its own: system:pipeline-steward sees the
		// same stall (Observation.MergeQueueStalledAt) and decides.
		starvedSince, _ := ciStarvation(gate.CI, now)
		if err := escalateStalledChecks(ctx, store, task, ref, checks, grain, stallDeadline,
			"", starvedSince, now); err != nil {
			return err
		}
	}

	// Closing out is one write now. It used to be two -- close the task's
	// GitHub issue, then record the closure -- with the issue closed first
	// and the store told second, so a crash in between left a closed issue
	// that grain still believed was open. recordPullRequestEvents widens
	// that one write rather than adding a second: PrOpenedAt can also need
	// recording on a cycle that closes nothing (an open PR seen for the
	// first time), and PrMergedAt/PrClosedAt only ever get set alongside
	// ClosedAt, so there is never a cycle needing both writes at once.
	if health == model.PrMerged || health == model.PrClosed {
		forgetEmptyChecks(ref)
		forgetMergeRefusals(ref)
		forgetGitHubQueued(ref)
	}
	// Before the closing write, not after it: recordPullRequestEvents is
	// what takes this task out of OpenPullRequestLinks, so this is the
	// last cycle syncEntry ever runs for it.
	if health == model.PrMerged {
		if err := recordPostMergeCI(ctx, store, client, gate.CI, task, stacked, ref, detail, now); err != nil {
			return err
		}
		// A merge onto one of a repository's paired branches is filed as
		// a port onto the others (port.go). Logged rather than returned:
		// the merge has landed, and a missed port is the nightly
		// triage's to catch rather than a reason to leave this task open.
		if err := filePorts(ctx, store, client, gate.Porter, task, ref, detail, now); err != nil {
			log.Printf("orchestrator: %v", err)
		}
	}
	if err := closeSecurityReviewOnSettle(ctx, store, client, task, ref, detail, health, now); err != nil {
		return err
	}
	return recordPullRequestEvents(ctx, store, task.ID, e.obs, detail, health, now)
}

// closeOutLanded is the closing write for a pull request closeIfLanded
// has just closed: the same forgetting and the same observation any
// closed pull request gets, which is what takes the task out of the
// queue and marks it done.
func closeOutLanded(ctx context.Context, store *model.Store, task model.Task, obs *model.Observation,
	ref model.PullRequestRef, detail github.PullRequestDetail, now time.Time) error {

	forgetEmptyChecks(ref)
	forgetMergeRefusals(ref)
	if err := setMergeQueueState(ctx, store, obs, task.ID, queueStatus{}, now); err != nil {
		return err
	}
	if obs != nil && (obs.MergeQueueBlockedAt != nil || obs.MergeQueueBlockedBase != "") {
		if err := observeField(ctx, store, task.ID, now, func(o *model.Observation) {
			o.MergeQueueBlockedAt = nil
			o.MergeQueueBlockedBase = ""
		}); err != nil {
			return err
		}
	}
	detail.State = "closed"
	return recordPullRequestEvents(ctx, store, task.ID, obs, detail, model.PrClosed, now)
}

// recordPostMergeCI writes down that a merge has moved a base branch, so
// that the branch's own release run is started against it. Grain has no
// repository webhook, so the pull-request reconciler is the authoritative
// observer of its own merges: before it closes the task, it resolves the
// base branch's new tip and records it.
//
// Only repositories already opted into Grain CI get a row. A retry before
// the closing observation is written is harmless: the write is a
// statement about the branch rather than an action, and repeating it
// changes nothing.
//
// # It records, and no longer starts (grain/task-334)
//
// This used to call runner.Start itself, once per merge, and that is what
// stacked release runs: a full run on bwsalmon/grain is 22 minutes to an
// hour and a half, merges land far faster, and several concurrent image
// builds are what made the rest of CI slow and the release's own
// pre-publish gate flake. Starting one is now reconcileBaseBranchCI's
// alone, which is already level-triggered on the right condition -- "the
// tip is not the commit the recorded verdict is about" -- and which now
// also holds the branch to one run at a time. A merge that lands while a
// run is going leaves PendingTaskID on the row and moves the tip; the
// cycle after that run finishes starts one against the tip as it then
// stands.
//
// The verdict is not weakened by that. The run reconcileBaseBranchCI
// collects is the one that judges the tree -- a `push` of the
// repository's own plan since grain/task-461, rather than the trusted
// release run this function used to start -- it still holds the merge
// queue when it fails and still earns its
// repair (grain/task-198), and a commit skipped by coalescing is judged by
// the later run whose tree contains it -- see reconcileBaseBranchCI's own
// "One run in flight".
//
// # Which switch decides it (grain/task-320)
//
// The repository's, not the merged task's. This used to return on
// `!grainci.Granted(task.Grants)` alone, which made whether `main` gets
// tested after a merge depend on a capability ticked on whichever task
// happened to merge -- so an old task filed before the repo was opted in,
// or one somebody unticked it on, merged and left the branch untested with
// nothing said anywhere. The task's own grant still counts (a repository
// nobody has opted in, with one task trying grain CI, gets its post-merge
// run as it always did), but the repo's own settings are enough on their
// own: baseBranchCIWanted.
//
// # Publishing is not judging at all any more (grain/task-330, 458, 461)
//
// `ReleaseWorkflowsDisabled` used to suppress this run entirely, which
// meant an operator turning off *publishing* for a repository -- because
// a release job is broken, say -- also silently turned off the
// base-branch check, which at the time was what stopped the queue
// merging onto a red `main` (it holds nothing since grain/task-471).
// grain/task-330 made a repository with releases off judged under `push`
// instead, grain/task-458 stopped an empty BaseBranchCIJobs suppressing
// even that, and grain/task-461 finished the job: the run
// reconcileBaseBranchCI starts is a `push` for *every* repository,
// untrusted and with no release secrets, with BaseBranchCIJobs narrowing
// it for cost. The trusted release a merge still emits is a second run
// started by reconcileBaseBranchRelease, on a row the queue does not
// read, so a publisher that fails no longer holds a repository's merges.
// baseBranchCIWanted has the whole decision and why it changed; this
// function reads only its refusal, since the run itself is started
// elsewhere.
//
// Either way it is no longer silent: every merge that records no row says
// so and says why, which is what the gke-labs/granule investigation had to
// reconstruct from an absence.
func recordPostMergeCI(ctx context.Context, store *model.Store, client github.Client, runner ci.Runner, task model.Task,
	stacked bool, ref model.PullRequestRef, detail github.PullRequestDetail, now time.Time) error {

	if runner == nil || store == nil {
		// No CI engine to judge the branch, or nowhere to write down that
		// it moved -- and the reconciler that would start the run reads
		// both, so a row recorded here without them is a row nothing ever
		// acts on.
		return nil
	}
	policy, err := baseBranchCIWanted(ctx, store, ref.Repo)
	if err != nil {
		return err
	}
	if policy.Refusal == baseCINotEnabled && grainci.Granted(task.Grants) {
		// Not enabled for the repository, but this task holds the grant:
		// the one branch somebody is trying grain CI on gets its
		// post-merge run, exactly as it did when the task's grant was the
		// only thing consulted.
		policy.Refusal = ""
	}
	if policy.Refusal != "" {
		log.Printf("orchestrator: %s merged into %s on %s, and grain CI is not judging the result: %s",
			task.ID, detail.BaseRef, ref.Repo, policy.Refusal)
		return nil
	}
	branch := detail.BaseRef
	if branch == "" {
		return fmt.Errorf("orchestrator: starting post-merge grain CI for %s: GitHub reported no base branch", ref)
	}
	// A stacked merge -- a review or fix task's pull request into the
	// reviewed task's own branch -- moved no base at all, and must not
	// make one of that branch (grain/task-949). Enrolling it here is what
	// used to hand an agent-authored grain/task-* branch everything a
	// watched base gets: push runs, a repair task when it went red, a
	// place among the branches a release may run from, and a trusted
	// release run of its own workflows with the repository's release
	// secrets mounted. The reviewed task's own merge, later, is what
	// moves the real base. The branch name is checked as well as the
	// task, so a merge into a task branch by anything else is refused
	// the same way.
	if stacked {
		log.Printf("orchestrator: %s merged into %s on %s, and grain CI is not judging the result: "+
			"it is a stacked review or fix, which lands on another task's branch rather than on a base branch",
			task.ID, branch, ref.Repo)
		return nil
	}
	if isTask, err := store.IsTaskBranch(ctx, branch); err != nil {
		return err
	} else if isTask {
		log.Printf("orchestrator: %s merged into %s on %s, and grain CI is not judging the result: "+
			"%s is a task's branch, which grain never watches as a base branch",
			task.ID, branch, ref.Repo, branch)
		return nil
	}
	head, err := client.GetBranchHead(ref.Repo.Owner, ref.Repo.Name, branch)
	if err != nil {
		return fmt.Errorf("orchestrator: reading %s's post-merge head on %s: %w", branch, ref.Repo, err)
	}
	if head == nil || head.SHA == "" {
		return fmt.Errorf("orchestrator: recording the post-merge tip of %s for %s: %s has no head",
			branch, ref, branch)
	}
	// Written down where the merge queue reads it, which is the whole of
	// what makes this branch's run mean anything: until grain/task-198 the
	// verdict was started and nobody ever collected it, so a `main` this
	// very merge had broken was found out about by the next task to fail
	// mysteriously. reconcileBaseBranchCI starts the run and collects it;
	// a failure earns a repair and is said out loud on every change
	// queued for this branch. It stopped the queue merging into the
	// branch as well until grain/task-471, and does not now.
	return noteBaseBranchMerge(ctx, store, ref.Repo, branch, head.SHA, task.ID, now)
}

// recordPullRequestEvents stamps whichever of a task's own pull-request
// timeline moments (bwsalmon/agents#493: "PR opened", "PR merged", "PR
// closed unmerged") this cycle can newly answer, and closes the task out
// once the pull request itself is done.
//
// PrOpenedAt, off detail's own CreatedAt, is written the first cycle this
// runs for a given task -- the moment a human (or EnsurePullRequest)
// actually opened the PR, which can predate this task completing by
// however long a found (rather than freshly opened) PR had already been
// open before grain started tracking it. Written once: unlike Health, an
// opening moment never changes, so re-observing it every cycle a PR
// happens to stay open would be a write with nothing new to say.
//
// PrMergedAt or PrClosedAt -- mutually exclusive, per Observation's own
// doc comment -- are set the one cycle health first reads PrMerged or
// PrClosed, alongside ClosedAt: once written, this task drops out of
// OpenPullRequestLinks (task_state reads 'closed', rather than either of
// the two post-run states that query names, once closed_at is set) and
// syncEntry never runs for it again, so there is no
// second cycle for either field to be overwritten on.
//
// The merged cycle also records how big the diff was -- additions,
// deletions and files changed, off the same response
// (github.PullRequestDetail.Additions) -- which is where a week's
// release notes get their lines-changed figure from
// (model.ClusterStats). On the merge and nowhere else, for the reason
// the paragraph above gives: that is the one cycle per task this runs
// on last, so the number is written exactly once and cannot be revised
// by a later read of a pull request whose head has since been deleted.
//
// A detail carrying all three zero is left unrecorded rather than
// written as a measured zero. GitHub does not merge a pull request that
// changed nothing, so all-zero means the response did not carry the
// figures -- a client standing in for GitHub, an older cached read --
// and NULL is the honest record of that (Observation.PrAdditions).
func recordPullRequestEvents(ctx context.Context, store *model.Store, taskID string,
	obs *model.Observation, detail github.PullRequestDetail, health model.PrHealth, now time.Time) error {

	needsOpenedAt := (obs == nil || obs.PrOpenedAt == nil) && !detail.CreatedAt.IsZero()
	closing := health == model.PrClosed || health == model.PrMerged
	if !needsOpenedAt && !closing {
		return nil
	}

	err := observeField(ctx, store, taskID, now, func(o *model.Observation) {
		if needsOpenedAt {
			openedAt := detail.CreatedAt
			o.PrOpenedAt = &openedAt
		}
		if !closing {
			return
		}
		o.ClosedAt = &now
		if health != model.PrMerged {
			o.PrClosedAt = &now
			return
		}
		mergedAt := now
		if detail.MergedAt != nil {
			mergedAt = *detail.MergedAt
		}
		o.PrMergedAt = &mergedAt
		if detail.Additions != 0 || detail.Deletions != 0 || detail.ChangedFiles != 0 {
			additions, deletions, files := detail.Additions, detail.Deletions, detail.ChangedFiles
			o.PrAdditions, o.PrDeletions, o.PrChangedFiles = &additions, &deletions, &files
		}
	})
	if err == nil && closing && health == model.PrMerged {
		if t, tErr := store.GetTask(ctx, taskID); tErr == nil && t != nil {
			_ = CheckAndMarkTuningAdoption(ctx, store, *t)
		}
	}
	return err
}

// advanceMergeQueueHead is what makes task -- the head of its repo's
// merge queue, its PR conflicted or failing checks -- progress: bring a
// stale branch up to date with its base and re-ask next cycle, send the
// task back to an agent the first time a genuine failure happens, or
// notice the repair it asked for has finished and decide whether that
// resolved things, or give up on a repair that has not finished and is
// not going to (defaultRepairDeadline).
//
// "Genuine" now excludes one failure an agent cannot be genuine about:
// a build whose every failing run GitHub concluded before a job of it
// started (github.OnlyStartupFailures) goes straight to a person, since
// the one automatic repair this pull request gets would be spent proving
// that no commit clears it. See escalateStartupFailure.
//
// With one exception, and it is the one startup failure a branch really
// did cause: a workflow file this very pull request made unparseable.
// GitHub fails the run identically either way -- a check-run name and a
// run conclusion carry no cause -- so the queue asks the one question
// that separates them, which files this pull request touches
// (touchedWorkflowFiles), and repairs the case whose cause is in the
// tree rather than in the repository's settings.
//
// e.grain is what grain's own CI made of the same commit, and a head that
// is here only because of it (withGrainCI) arrives with an empty
// buildEvidence -- there is no failing GitHub run to read logs from, so
// the startup-failure arm cannot fire and the ordinary repair is asked
// for. What that repair is told about it is grainCINote's.
//
// # Nothing is spent before there is a path to green
//
// The whole of this function is moves that cost something -- a base
// merge written to somebody's branch, an agent run, the CI suite that
// answers it -- and every one of them is spent to reach a green pull
// request. So the first question is whether green is reachable at all
// (e.reds, forge.Reds): a head whose red is the repository's rather than
// its own does not get the base merge, does not get the repair, and is
// handed to a person in the seconds it takes to read the checks.
//
// This is not the same as giving up early. The red that is the branch's
// own still earns exactly the repair it earned before -- grain/task-219
// had seven checks an Actions budget had stopped and one grain CI
// failure its own base merge had caused, and that one is real work an
// agent should do. What changes for it is that the repair says which of
// the two it is for, and that no base merge is made first to reach a
// green this pull request cannot have until somebody pays a bill.
func advanceMergeQueueHead(ctx context.Context, store *model.Store, client github.Client,
	e queueEntry, isHead bool, find *repoFinder, now time.Time) error {

	task, obs, ref := e.task, e.obs, e.ref
	detail, health, checks, grain := e.detail, e.health, e.checks, e.grain
	running, requireUpToDate, gateIsGrainCI := e.running, e.requireUpToDate, e.gateIsGrainCI

	baseBroken, err := brokenBaseBranch(ctx, store, ref.Repo, detail.BaseRef)
	if err != nil {
		return err
	}
	// A repair of the base itself -- grain's, or a task a person marked as
	// fixing it -- is not held back by the red it is there to fix.
	baseIsBroken := baseBroken != nil && !isBaseRepair(task, ref.Repo, detail.BaseRef, baseBroken)

	if obs == nil || obs.MergeQueueRepairAt == nil || (obs.RetryRequestedAt != nil && obs.RetryRequestedAt.After(*obs.MergeQueueRepairAt)) {
		// Not while an agent is on this branch already (queueEntry.
		// running): every move this arm makes is a move on the branch --
		// a base merge pushed to it, or a repair run asked for it -- and
		// both are being made by somebody already. The entry is passed
		// over rather than held (passedOver), so the queue behind it
		// keeps moving, and whatever that run leaves behind is what the
		// next cycle judges.
		//
		// Only this arm. The one below is the deadline on a repair that
		// has not finished, which exists precisely for a run that is
		// still in flight and never going to end, and gating it on the
		// same fact would be a wait with nothing left to end it.
		if running {
			return nil
		}
		// The cheap half first -- unless there is nothing cheap to try.
		// Two things rule it out, and they rule it out for the same
		// reason: the merge this would write is spent reaching a green
		// it cannot reach. A head grain's own CI failed was judged on
		// its exact commit, so merging the base in cannot change the
		// verdict and would only spend a second full suite arriving at
		// it (refreshWorthTrying). And a head whose red is the
		// repository's own -- checks that failed before a job of them
		// ran, a check that never passes for this author -- stays red
		// whatever commit is on the branch, so the base merge buys
		// nothing and can cost plenty: the conflict grain/task-219 spent
		// a repair resolving was one its own base merge created, on a
		// pull request the queue had already proved could not go green.
		//
		// That second one overrides a repository's standing wish for
		// up-to-date branches (RequireBranchUpToDate), which the first
		// deliberately does not. There the branch is going to merge, so
		// carrying its base is work that has to happen either way; here
		// it is not going to merge, and whoever clears the repository's
		// red gets the branch brought up to date then, once, rather than
		// now and again later.
		outcome := refreshNotOffered
		if isHead && e.reds.PathToGreen() && refreshWorthTrying(health, grain, requireUpToDate) && !baseIsBroken {
			var err error
			outcome, err = refreshStaleHead(ctx, store, client, task, obs, ref, detail, now)
			if err != nil {
				return err
			}
		}
		if outcome == refreshUpdated {
			// CI is running again against a tree somebody would merge.
			// Decide next cycle, on that answer rather than this one.
			return nil
		}
		// Read once, here, rather than inside requeueForRepair as this
		// used to: what CI printed is no longer only an annotation on the
		// repair, it is what decides whether a repair is the right move
		// at all. A conflicted head has no failing build to read, so it
		// is not asked for.
		//
		// ev.logs is the whole of what that decision is made on, exactly
		// as it was when this read one slice: the rest of the evidence
		// (what Actions says when there is no log) is prose for the
		// comment and is read only where it would change no branch here.
		//
		// Not read at all where GitHub's check runs are not a source of
		// verdicts for this repository (gateIsGrainCI,
		// model.RepoConfig.IgnoreGitHubCI). Nothing here is about them:
		// such a head is red because grain CI failed against its exact
		// commit (forge.Observe never made the Checks read, so PrFailing
		// can only have come from grain's own verdict), and every branch
		// below that reads ev is a question about GitHub Actions --
		// which runs failed, whether they died before a step, what they
		// printed. Asking it anyway is what put two screens of somebody
		// else's Actions output, and an emphatic "act on that sentence
		// rather than on the list above", into a bounce message whose
		// own first paragraph had just told the run those checks are not
		// read here (grain/task-355). The read costs money and a
		// round-trip on top, so it is skipped rather than made and
		// dropped -- the same discipline forge.Observe keeps about the
		// Checks API itself.
		//
		// Nor for the configuration repository (model.IsConfigRepo): grain
		// serves it itself, it never reaches GitHub, and the native
		// client's empty Actions answer was rendered as "GitHub Actions
		// has no workflow run at all ... Buildkite, CircleCI" under the
		// grain CI note of a grain/config repair.
		var ev buildEvidence
		if health == model.PrFailing && !gateIsGrainCI && !model.IsConfigRepo(ref.Repo) {
			ev = readBuildEvidence(client, ref, detail.HeadSHA)
		}
		// Startup failures, from either direction: this pull request's
		// own runs all died before a step (github.OnlyStartupFailures),
		// or the repository's do and this cycle already concluded so
		// (e.unfixable, repoFinder). The two are the same news and the
		// second is the cheaper to come by, which is the point of it --
		// the entries behind this one share the conclusion rather than
		// each buying their own diagnosis.
		reds, note := e.reds, ""
		if e.unfixable.ChecksNeverStart || github.OnlyStartupFailures(ev.logs) {
			// The one question a run conclusion cannot answer: is the
			// cause in this tree? A workflow file this very pull request
			// made unparseable fails the run identically to an account
			// with no minutes left, and is the one startup failure a
			// commit clears -- so that head keeps its repair, and its
			// checks go back to being its own.
			workflows, read := touchedWorkflowFiles(client, ref)
			startup := e.unfixable
			startup.ChecksNeverStart = len(workflows) == 0
			if startup.ChecksNeverStart && startup.Why == "" {
				startup.Why = "every failing run against this commit was concluded before a " +
					"job of it ran a step"
			}
			reds = forge.SplitReds(failingCheckNames(e), grain.Failed, startup)
			switch {
			case len(workflows) > 0:
				note = unparseableWorkflowNote(ref, detail, ev.logs, workflows)
			case !reds.BranchOwns():
				if !isHead {
					return nil
				}
				// Nothing here is the branch's, so the repair would be
				// spent proving that no commit clears it. The
				// repository's own recent runs say which of the causes
				// it is, and this cycle has already read them
				// (repoFinder.starts) if any entry needed the answer.
				starts := find.starts(ref.Repo)
				noteRepositoryChecksNeverStarted(ref.Repo, starts)
				return escalateStartupFailure(ctx, store, task, ref, detail, checks, grain, ev.logs,
					read, starts, now)
			}
		}
		// Everything red belongs to somebody else and no commit here
		// reaches it: a CLA bot that will not pass for this author, a
		// check the repository has declared unfixable. Said once, in
		// seconds, rather than discovered by an agent in hours.
		if !reds.BranchOwns() && !reds.PathToGreen() {
			if !isHead {
				return nil
			}
			return escalateUnfixableChecks(ctx, store, task, ref, reds, e.unfixable, false, now)
		}
		// A repository whose merge rules say `repairs: 0` for these paths
		// wants a person on every failure (grain/task-1660).
		rc := repairCapOf(entryPolicy(e))
		if rc.max == 0 {
			return escalateNoRepair(ctx, store, task, ref, health, rc, now)
		}
		attempt := repairAttempt{n: obs.RepairsSpent() + 1, max: rc.max,
			baseTip: baseTipOf(client, ref, detail.BaseRef), failures: failureSignature(e)}
		return requeueForRepair(ctx, store, task, ref, detail, health, checks, grain, ev, outcome,
			gateIsGrainCI, reds, note, attempt, now)
	}

	if obs.RepairInFlight() {
		// The repair is queued, running, or waiting on an answer --
		// nothing to do until the run that is fixing this very branch
		// finishes one way or the other, unless it has been
		// not-finishing for longer than any repair is waited on.
		if !repairOverdue(obs, now) {
			return nil
		}
		return escalateUnfinishedRepair(ctx, store, task, ref, health, now)
	}
	// The repair ran to completion -- another attempt of this task,
	// pushed onto this very branch -- and yet ref still reads broken.
	//
	// Not on the cycle it finished on, though. Dispatch runs before sync
	// within one tick (Reconcilers), so a repair that completed moments
	// ago is judged here on a `detail` describing the branch as it was
	// before its push: GitHub recomputes a pull request's mergeability
	// asynchronously and its checks have not re-run at all, so acting now
	// means giving up on a resolution that has just landed. One cycle's
	// wait is the whole guard -- by the next one the verdict is about the
	// commit the repair actually pushed, whatever that verdict is.
	if obs.CompletedAt != nil && !now.After(*obs.CompletedAt) {
		return nil
	}
	// It did not stick -- but "it" is read here against a base that has
	// moved since the repair was asked for, and moved *because* of this
	// queue: a head being repaired is passed over (mergeWalk), so every
	// green pull request behind it merges while the repair runs. A
	// resolution that was correct when it was pushed can therefore be
	// merely behind by the time it is judged, and giving up on it here
	// hands a person a branch whose whole problem is one merge commit.
	//
	// So the refresh is offered once more, on the same terms as the first
	// one: ask GitHub to merge the base in and read the answer off what it
	// does (refreshStaleHead). 201 means the resolution was fine and only
	// stale, and the next cycle judges CI that ran against a tree somebody
	// would actually merge. 204 means nothing was behind, so the repair
	// really did fail on its own merits. 409 means it conflicts for real,
	// which is the second conflict in a row and squarely a person's.
	//
	// This does not buy a second repair, and that is the whole reason it
	// is safe: one refresh per repair round, one repair per pull request,
	// so a pull request makes at most two of these merges in its life and
	// then a person is asked. Not counting the repair as spent instead --
	// re-repairing whenever the base moved under it -- is the unbounded
	// chain requeueForRepair's own doc comment declines, and other
	// people's merges are exactly what would drive it.
	// Unless what it is still red with is red nobody reaches from this
	// branch. Then the repair did what it could -- it may even have
	// fixed the half that was this branch's -- and the second base merge
	// would be one more write spent on a merge that is not this queue's
	// to make. The person who is told gets the finding rather than "the
	// repair didn't take", which is the sentence that would send them
	// looking at the branch.
	if !e.reds.PathToGreen() {
		return escalateUnfixableChecks(ctx, store, task, ref, e.reds, e.unfixable, true, now)
	}
	outcome := refreshNotOffered
	if refreshWorthTrying(health, grain, requireUpToDate) && !baseIsBroken {
		var err error
		outcome, err = refreshStaleHead(ctx, store, client, task, obs, ref, detail, now)
		if err != nil {
			return err
		}
	}
	if outcome == refreshUpdated {
		// CI is running again against a tree somebody would merge. Decide
		// next cycle, on that answer rather than this one -- and if it is
		// still red then, the refresh is spent too and this arm escalates.
		return nil
	}
	// A second repair, where something has changed since the first was
	// asked for -- the base has new commits, or what is failing is
	// different -- and never a third (secondRepairReason). The same
	// failure on the same base is not retried: requeueForRepair's own doc
	// comment on the unbounded chain.
	rc := repairCapOf(entryPolicy(e))
	attempt := repairAttempt{n: obs.RepairsSpent() + 1, max: rc.max, failures: failureSignature(e)}
	if obs.RepairsSpent() < rc.max {
		attempt.baseTip = baseTipOf(client, ref, detail.BaseRef)
		if attempt.why = secondRepairReason(obs, detail.BaseRef, attempt.baseTip, attempt.failures); attempt.why != "" {
			var ev buildEvidence
			if health == model.PrFailing && !gateIsGrainCI && !model.IsConfigRepo(ref.Repo) {
				ev = readBuildEvidence(client, ref, detail.HeadSHA)
			}
			return requeueForRepair(ctx, store, task, ref, detail, health, checks, grain, ev, outcome,
				gateIsGrainCI, e.reds, "", attempt, now)
		}
	}
	return escalateToUser(ctx, store, task, ref, health, outcome, obs.RepairsSpent(), rc, now)
}

// repoMergesOnGreen reads one repository's answer to "may a green entry
// merge past one ahead of it whose CI is still running"
// (model.RepoConfig.MergeOnGreen). A repository with no configuration row
// says no: the strict lane.
func repoMergesOnGreen(ctx context.Context, store *model.Store, repo model.RepoRef) (bool, error) {
	if store == nil {
		return false, nil
	}
	cfg, err := store.GetRepoConfig(ctx, repo)
	if err != nil {
		return false, fmt.Errorf("orchestrator: reading merge settings for %s: %w", repo, err)
	}
	return cfg != nil && cfg.MergeOnGreen, nil
}

// repoRequiresBranchUpToDate reads one repository's answer to "may a
// branch merge while it is behind its base"
// (model.RepoConfig.RequireBranchUpToDate). A repository with no
// configuration row at all says no, which is the deployment-wide default
// and the cheap behaviour.
func repoRequiresBranchUpToDate(ctx context.Context, store *model.Store, repo model.RepoRef) (bool, error) {
	if store == nil {
		return false, nil
	}
	cfg, err := store.GetRepoConfig(ctx, repo)
	if err != nil {
		return false, fmt.Errorf("orchestrator: reading merge settings for %s: %w", repo, err)
	}
	return cfg != nil && cfg.RequireBranchUpToDate, nil
}

// repoUnfixableChecks reads one repository's list of checks nothing
// pushed to a branch there ever turns green
// (model.RepoConfig.UnfixableChecks) -- a CLA bot and its kind. A
// repository with no configuration row at all names none, which is every
// repository until an operator says otherwise.
//
// Read per repository per cycle beside repoRequiresBranchUpToDate, and
// deliberately not composed with anything deployment-wide: unlike
// "GitHub is not a verdict source here", which an unpaid Actions bill
// makes true of a whole account at once, a check that cannot pass is
// named per repository because that is where checks live.
func repoUnfixableChecks(ctx context.Context, store *model.Store, repo model.RepoRef) ([]string, error) {
	if store == nil {
		return nil, nil
	}
	cfg, err := store.GetRepoConfig(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: reading merge settings for %s: %w", repo, err)
	}
	if cfg == nil {
		return nil, nil
	}
	return cfg.UnfixableChecks, nil
}

// deploymentIgnoresGitHubCI is the whole deployment's answer to "are
// GitHub's check runs a source of verdicts here"
// (model.Config.IgnoreGitHubCI). A store that cannot be read is an error
// rather than a false: the question decides which CI provider gates
// every merge this cycle, and guessing "GitHub" at it would put the
// queue back to waiting on checks a deployment has said are dead.
func deploymentIgnoresGitHubCI(ctx context.Context, store *model.Store) (bool, error) {
	return false, nil
}

// repoIgnoresGitHubCI is one repository's answer to the same question,
// composed with the deployment's the only way it can be composed: a
// deployment that has said GitHub is not a verdict source has said it
// for every repository under it, and a repository may add itself to that
// set but never take itself out of it.
func repoIgnoresGitHubCI(ctx context.Context, store *model.Store, repo model.RepoRef, deploymentDefault bool) (bool, error) {
	mode, err := repoCIMode(ctx, store, repo, deploymentDefault)
	if err != nil {
		return false, err
	}
	return mode == model.CIModeGrain, nil
}

func repoCIMode(ctx context.Context, store *model.Store, repo model.RepoRef, deploymentDefault bool) (model.CIMode, error) {
	if deploymentDefault {
		return model.CIModeGrain, nil
	}
	if store == nil {
		return model.CIModeGitHub, nil
	}
	cfg, err := store.GetRepoConfig(ctx, repo)
	if err != nil {
		return "", fmt.Errorf("orchestrator: reading ci-mode settings for %s: %w", repo, err)
	}
	if cfg == nil {
		return model.CIModeGitHub, nil
	}
	return cfg.EffectiveCIMode(), nil
}

// bringHeadUpToDate is the strict half of grain/task-198, and it runs for
// no repository that has not asked for it
// (model.RepoConfig.RequireBranchUpToDate): merge the pull request's base
// branch into its head branch before merging the pull request, so that
// what lands has been tested against the tree it lands on.
//
// It reports whether the queue is *holding* this merge -- true when it
// wrote to the branch (so the verdict this cycle read is about a commit
// that is no longer the head, and the next cycle decides on the new one),
// and true when GitHub refused the merge as a conflict (so what the next
// cycle reads is PrConflicted, and the ordinary repair path takes it from
// there). False is "there was nothing to bring in, or nothing that could
// be": merge the pull request now, exactly as before.
//
// # Why this is off by default
//
// A grain CI verdict is about one exact commit (grainCIVerdictOf), so any
// write to the head branch throws the verdict away and starts another
// full suite. With the queue doing that to every branch each time the
// base moved, N open branches and N merges cost on the order of N² full
// suites: on 2026-09-17 one task on this repository burned four
// twenty-minute suites in ninety minutes without a line of its own
// changing, while the CI host had run out of sandboxes. That is the whole
// cost of the property this buys, and the property itself -- two changes
// that are each green alone and broken together -- is bought more cheaply
// by testing the base branch after the merge and stopping the queue when
// that goes red (reconcileBaseBranchCI). So the default is off, and this
// is for a repository that would rather pay.
//
// # No attempt bookkeeping
//
// Unlike refreshStaleHead, which is a repair and is rationed to one
// attempt per round, this is a *precondition*: while the rule is on, a
// branch that is behind is brought up to date however many times the base
// moves under it, because merging it without doing so is exactly what the
// rule forbids. It is bounded instead by the same thing the strict rule is
// bounded by on GitHub -- the queue's own throughput -- and by the queue
// closing the repository's slot for the cycle, so a repository set this
// way merges at most one change per base movement rather than racing
// itself.
func bringHeadUpToDate(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, obs *model.Observation, ref model.PullRequestRef,
	detail github.PullRequestDetail, now time.Time) (bool, error) {

	if detail.HeadRef == "" || detail.BaseRef == "" {
		// Nothing to name in the call. A pull request grain cannot see the
		// branches of is one it merges as it stands rather than one it
		// holds forever on a write it cannot make.
		return false, nil
	}

	// Said before the call for the reason the merge state below it is:
	// this is a write to somebody's branch and it can block long enough
	// for a person to be looking at the row while it does.
	if err := setMergeQueueState(ctx, store, obs, task.ID, queueStatus{
		state: mergeStateUpdatingBranch,
		note: fmt.Sprintf("bringing this branch up to date with %s: this repository requires "+
			"branches to contain their base before they merge", detail.BaseRef),
	}, now); err != nil {
		return false, err
	}

	result, err := client.MergeBranch(ref.Repo.Owner, ref.Repo.Name,
		detail.HeadRef, detail.BaseRef, refreshCommitMessage(detail))
	switch {
	case err == nil:
	case github.IsMergeConflict(err):
		// GitHub said clean a moment ago and refuses the merge now: the
		// base has moved since the mergeability this cycle read was
		// computed. Hold, and let the next cycle read the conflict off
		// the pull request and send it to an agent like any other
		// (advanceMergeQueueHead) -- which is the one case where a branch
		// really does have to be updated, and the one this change leaves
		// exactly as it was.
		return true, nil
	case notFound(err):
		// A head branch in another repository (a fork's), or a branch
		// that has gone. There is no merge to make, so there is nothing
		// to hold for: the pull request merges as it stands, which is
		// what every repository does by default anyway.
		return false, nil
	default:
		return false, fmt.Errorf("orchestrator: merging %q into %q for %s: %w",
			detail.BaseRef, detail.HeadRef, ref, err)
	}

	// GitHub's 204: the head already contains its base. Nothing was
	// written, the verdict this cycle read is about the tree that will
	// land, and the merge goes ahead now.
	if !result.Merged {
		return false, nil
	}
	noteRebase(ctx, store, ref, task.ID, model.MergeQueueUpToDate, now)

	// Not optional, for refreshStaleHead's own reason: somebody working
	// in this branch has a merge commit on it that they did not make, and
	// this is the only thing that says who did.
	if err := queueComment(ctx, store, task.ID, upToDateComment(detail), now); err != nil {
		return true, err
	}
	return true, nil
}

// upToDateComment is what the queue says on the task when it has merged a
// base branch into a head branch to satisfy the strict rule -- which is a
// different sentence from refreshedComment's, because nothing is wrong
// with this branch. It was simply behind, and this repository has asked
// that a branch not merge while it is.
func upToDateComment(detail github.PullRequestDetail) string {
	return fmt.Sprintf(
		"The merge queue merged `%s` into `%s`: this repository requires a branch to contain "+
			"its base before it merges (`require_branch_up_to_date`), and this one was "+
			"behind. Nothing is wrong with the branch -- CI runs again against the new "+
			"commit, and the queue merges it once that is green.",
		detail.BaseRef, detail.HeadRef)
}

// refreshOutcome is what refreshStaleHead did about a broken queue head.
// It is also the queue's whole classification of "was this head simply
// behind its base": rather than guess from a signal that cannot answer it
// (see refreshStaleHead), the queue asks GitHub to make the merge and
// reads the answer off what GitHub does.
type refreshOutcome int

const (
	// refreshUpdated: the head really was behind, the merge landed, and CI
	// is re-running against a tree somebody would actually merge. Nothing
	// is filed this cycle -- the next one decides on the fresh answer.
	refreshUpdated refreshOutcome = iota
	// refreshUpToDate: GitHub's own 204. The head branch already contained
	// its base, so whatever CI reported, it reported about the tree that
	// would merge. Nothing was stale and the failure is genuine.
	refreshUpToDate
	// refreshConflicted: GitHub's own 409. Behind *and* genuinely
	// conflicting, which is exactly the case a fix task is for -- and now
	// one the queue has watched fail rather than inferred.
	refreshConflicted
	// refreshUnreachable: there was no merge to attempt. A pull request
	// from a fork has its head branch in another repository, which POST
	// /merges answers 404 for, and a detail with no branch names on it
	// (very nearly unreachable) has nothing to name in the call.
	refreshUnreachable
	// refreshAlreadyTried: this pull request has had the one attempt it
	// gets. See Observation.MergeQueueRefreshedAt.
	refreshAlreadyTried
	// refreshNotOffered: no merge was attempted because no merge could
	// have changed the answer. See refreshWorthTrying.
	refreshNotOffered
)

// refreshWorthTrying reports whether merging the base branch into a
// broken head could change what CI said about it -- which is the whole
// premise refreshStaleHead rests on, and which is not always true.
//
// It is true wherever GitHub's own checks are the gate. Those run against
// `refs/pull/N/merge`, a merge of the head into a base that has since
// moved, so a red verdict really can be about a tree nobody would merge
// and a base merge really can clear it (docs/merge-queue-staleness.md is
// the whole story).
//
// It is false for a head that grain's own CI failed. A grain CI verdict
// is about one exact commit -- the branch's head as it stands, never a
// merge ref (grainCIVerdictOf) -- so staleness cannot be what made it
// red, and merging the base in would only throw a true answer away and
// spend another full suite arriving at the same one. What that head needs
// is the repair, and it needs it a suite sooner.
//
// A repository that has asked for branches to be up to date
// (model.RepoConfig.RequireBranchUpToDate) gets the merge either way: it
// has said it wants its branches carrying their base, and a broken branch
// is not an exception to that.
func refreshWorthTrying(health model.PrHealth, grain grainCIVerdict, requireUpToDate bool) bool {
	if requireUpToDate || health != model.PrFailing {
		return true
	}
	return !grain.Failed
}

// refreshStaleHead is the cheap half of repairing a broken queue head:
// before a fix task is filed and an agent dispatched, merge the pull
// request's base branch into its head branch and let the next cycle
// decide on CI that ran against a tree somebody would actually merge.
//
// Nearly every automatic fix this deployment has filed was resolved by
// exactly this commit -- a plain merge of main into the task's branch,
// with nothing to arbitrate (docs/merge-queue-staleness.md lists them).
// The shape is always the same: a branch whose checks last ran against a
// base that has since moved, so either GitHub can no longer compute a
// merge ref and every `on: pull_request` job dies at checkout (which
// healthFrom reads as PrFailing -- the jobs exist, they completed, they
// concluded `failure`), or the verdict that did report is about a tree
// nobody would ever merge. Either way the fix task's body would name
// failures that are not what is actually wrong with the branch, so the
// agent starts from a false description of its own job.
//
// The queue deliberately does not classify "behind and stale" against
// "genuinely failing" before acting, because neither available signal
// answers it. detail.MergeableState is one value where two independent
// facts are wanted, and `dirty` and `blocked` outrank `behind` -- so a
// pull request that is both behind and failing a required check reports
// `blocked` and never says "behind" at all, which is precisely this case.
// A check run carries no sha of the base it was merged onto, either; the
// staleness lives in the merge ref, which the Checks API does not name.
// So the queue asks the question it can get an authoritative answer to,
// at the moment it matters, with the write it wants to make anyway: 201
// means it was behind and is not now, 204 means nothing was stale, 409
// means it conflicts for real. The 204 is the whole answer to "is this
// failure genuine".
//
// One attempt per repair round, persisted
// (Observation.MergeQueueRefreshedAt, compared against
// MergeQueueRepairAt by refreshSpent): once before the repair is asked
// for, and once after it has run and left the branch broken. Not once per
// cycle -- a refresh that lands and still leaves the branch red has told
// us the failure is real, and re-merging every time the base moves again
// would spend writes re-asking a question that has been answered. The
// attempt is recorded whether or not the merge landed -- what must not
// repeat is the *attempt*, not just its success.
//
// The second round exists because the base moves under a branch that is
// being repaired, by this queue's own doing: the head is passed over while
// its repair runs (mergeWalk), so the changes behind it merge past it, and
// a resolution can be correct when pushed and behind by the time it is
// read. See advanceMergeQueueHead's last arm, which is what asks for it.
// Repairs stay capped at one, so the rounds are capped at two: refresh,
// repair, refresh, a person.
//
// A merge, not a rebase, and made by GitHub rather than by a clone here:
// a rebase would need a force push to a branch an agent may still hold a
// clone of, would move the base out from under any stacked fix branch
// (a stacked fix task's Base *is* this head branch), and would discard review
// history. The merge is also exactly what the agents were doing by hand.
func refreshStaleHead(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, obs *model.Observation, ref model.PullRequestRef,
	detail github.PullRequestDetail, now time.Time) (refreshOutcome, error) {

	if refreshSpent(obs) {
		return refreshAlreadyTried, nil
	}
	if detail.HeadRef == "" || detail.BaseRef == "" {
		return refreshUnreachable, nil
	}

	result, err := client.MergeBranch(ref.Repo.Owner, ref.Repo.Name,
		detail.HeadRef, detail.BaseRef, refreshCommitMessage(detail))
	switch {
	case err == nil:
	case github.IsMergeConflict(err):
		if err := recordRefreshAttempt(ctx, store, task.ID, now); err != nil {
			return refreshConflicted, err
		}
		return refreshConflicted, nil
	case notFound(err):
		// The head branch is not in this repository -- a pull request from
		// a fork, which grain never opens itself (finishWithPullRequest
		// pushes into the target repo) but may well have found -- or it is
		// gone. Nothing to refresh, and the attempt is recorded so the
		// next cycle does not re-ask.
		if err := recordRefreshAttempt(ctx, store, task.ID, now); err != nil {
			return refreshUnreachable, err
		}
		return refreshUnreachable, nil
	default:
		// An ordinary failure, including a permission this credential does
		// not hold: not latched the way checksUnavailable is, since this
		// is a write on the same permission MergePullRequest already
		// needs, so a deployment that cannot make it has a broken merge
		// queue anyway and should say so loudly.
		return refreshUnreachable, fmt.Errorf(
			"orchestrator: merging %q into %q for %s: %w", detail.BaseRef, detail.HeadRef, ref, err)
	}

	// GitHub's 204: nothing was written because nothing was behind. No
	// attempt is recorded -- there was no merge to not repeat, and the fix
	// task filed this cycle is what stops the queue coming back here
	// anyway (advanceMergeQueueHead's own hasFix check).
	if !result.Merged {
		return refreshUpToDate, nil
	}
	noteRebase(ctx, store, ref, task.ID, model.MergeQueueStaleHead, now)

	if err := recordRefreshAttempt(ctx, store, task.ID, now); err != nil {
		return refreshUpdated, err
	}
	// Not optional: a human working in this branch who did not want a
	// merge commit has one now, and the only thing that tells them who
	// made it is this.
	if err := queueComment(ctx, store, task.ID,
		refreshedComment(ref, detail, obs != nil && obs.MergeQueueRepairAt != nil), now); err != nil {
		return refreshUpdated, err
	}
	return refreshUpdated, nil
}

// refreshSpent reports whether this pull request has already had the one
// refresh it gets for the round it is in -- see refreshStaleHead.
//
// A round is bounded by the repair: a refresh recorded before the repair
// was asked for was spent on the branch as the agent found it, and says
// nothing about the tree the agent pushed. So a stamp that predates
// MergeQueueRepairAt leaves the refresh available again, exactly once,
// for judging the repair's own push. A stamp with no repair behind it at
// all is the first round's, and is spent.
//
// Equal timestamps count as "before": requeueForRepair stamps the repair
// with the same `now` the refresh that preceded it in that very cycle was
// recorded with (a 409 or a 404 records the attempt and then asks for the
// repair), and that refresh is the first round's by construction.
func refreshSpent(obs *model.Observation) bool {
	if obs == nil || obs.MergeQueueRefreshedAt == nil {
		return false
	}
	if obs.MergeQueueRepairAt == nil {
		return true
	}
	return obs.MergeQueueRefreshedAt.After(*obs.MergeQueueRepairAt)
}

// refreshedComment is what the queue says on the task when it has just
// merged a base branch into a head branch, and it has to say two
// different things about what happens next, because by then the head is
// in one of the two rounds refreshSpent distinguishes.
//
// Before the repair, a red verdict next cycle costs an agent run and the
// person nothing. After it, the repair is already spent, so the same red
// verdict is what hands the branch to a human -- and somebody reading
// this comment then needs to have been told that, rather than waiting on
// a second automatic attempt that is never coming.
func refreshedComment(ref model.PullRequestRef, detail github.PullRequestDetail, afterRepair bool) string {
	next := "if it is still broken next cycle, a fix task is filed then."
	if afterRepair {
		next = "if it is still broken next cycle, this needs a person: the one automatic " +
			"repair this pull request gets has already been spent."
	}
	why := "rather than filing a fix for a failure that may not be real"
	if afterRepair {
		why = "rather than giving up on a resolution that may only be behind -- the changes " +
			"queued behind this one merged while it was being repaired, which is what moved " +
			"the base under it"
	}
	return fmt.Sprintf(
		"%s last had its checks run against a `%s` that has moved since, so the merge queue "+
			"merged `%s` into `%s` %s. CI is re-running against a tree that could actually "+
			"merge; %s",
		ref, detail.BaseRef, detail.BaseRef, detail.HeadRef, why, next,
	)
}

// refreshCommitMessage is what the merge queue's own merge commit says:
// who made it and why, since the first person to find it will be reading
// `git log` on a branch they thought only an agent had touched.
func refreshCommitMessage(detail github.PullRequestDetail) string {
	return fmt.Sprintf(
		"Merge %s into %s (grain merge queue)\n\n"+
			"Its checks last ran against a base that has moved since; re-running\n"+
			"them against a tree that could actually merge.",
		detail.BaseRef, detail.HeadRef)
}

// recordRefreshAttempt writes down that this pull request has had its one
// refresh -- see Observation.MergeQueueRefreshedAt for why this is
// persisted where the CI clocks above are not.
func recordRefreshAttempt(ctx context.Context, store *model.Store, taskID string, now time.Time) error {
	return observeField(ctx, store, taskID, now, func(o *model.Observation) { o.MergeQueueRefreshedAt = &now })
}

// notFound reports whether err is GitHub answering 404 -- for the merges
// endpoint, a branch that is not in this repository at all.
func notFound(err error) bool {
	var e *github.Error
	return errors.As(err, &e) && e.Status == 404
}

// defaultRepairDeadline is the third clock the merge queue runs, and the
// one that bounds advanceMergeQueueHead's wait: how long a queue head is
// left waiting on the repair run it asked for before the queue gives up
// and asks a person.
//
// Without it the wait is unbounded, and by a route neither of the two CI
// clocks above reaches. A head being repaired reads PrConflicted or
// PrFailing, not PrPending, so defaultCheckStallDeadline never times it.
// Anything that leaves the repair unfinished -- a dispatch that never
// happens because the deployment is saturated, an agent run that never
// comes back, a run that ends without pushing and leaves the task queued
// for another attempt -- would otherwise hold the head of the repo's
// queue indefinitely, with everything behind it waiting: the same stall
// defaultCheckStallDeadline closes, reached from the other side.
//
// Six hours is chosen against what a repair honestly needs: it has to
// wait its turn to be dispatched (briefly -- the queue's own members sit
// at the front of the backlog, which is the order Store.Ready dispatches
// in, and a repair draws on the capacity Limits.Mergers keeps back), run
// an agent (capped at DefaultMaxRunRuntime, two hours), and push. That is
// comfortably inside six hours even when every stage takes longer than
// usual, and the cost of erring long is one that is paid once per stuck
// head rather than per cycle. Erring short is worse here than it is for
// CI: giving up throws away a repair that might have been minutes from
// landing, and the queue never asks for a second one.
//
// Measured from Observation.MergeQueueRepairAt, which requeueForRepair
// stamps in the same write that requeues the task, rather than from an
// in-memory sighting like the CI clocks: this one is naturally persisted
// already, so a restart does not hand a stuck head another six hours.
const defaultRepairDeadline = 6 * time.Hour

// repairOverdue reports whether the repair asked for on this head has
// been unfinished for longer than defaultRepairDeadline. Call it only
// while repairInFlight holds.
//
// Compared against the deadline rather than as `now.Sub(asked) >=
// deadline`, so a clock that jumped backwards reads as "not overdue yet"
// -- the same direction the CI clocks err in.
func repairOverdue(obs *model.Observation, now time.Time) bool {
	if obs == nil || obs.MergeQueueRepairAt == nil {
		return false
	}
	return !now.Before(obs.MergeQueueRepairAt.Add(defaultRepairDeadline))
}

// repairReason renders why a PR is not mergeable, for a human or an
// agent reading the comment that asks for a repair. A failing PR names
// the checks that failed (failingChecks) rather than saying only that
// something did: the agent sent to repair it starts from that list, and
// "one or more required checks are failing" left it to go and find out
// which.
//
// A conflict the queue has just watched GitHub refuse (refreshConflicted)
// is described as that rather than as the inference healthReason would
// otherwise make from detail.Mergeable: the queue tried the merge, so it
// can tell the agent what its job actually is -- a real resolution rather
// than the plain merge that fixes a merely stale branch.
func repairReason(outcome refreshOutcome, health model.PrHealth,
	detail github.PullRequestDetail, checks []github.CheckRun, grain grainCIVerdict) string {

	if outcome == refreshConflicted {
		return fmt.Sprintf(
			"the merge queue tried to merge `%s` into `%s` and it conflicted, so this "+
				"needs a real resolution rather than a merge", detail.BaseRef, detail.HeadRef)
	}
	return healthReason(health, detail, checks, grain)
}

// healthReason names what is wrong, and names grain's own CI among the
// failing checks when it is one of them (grainCICheckName) -- it is a
// check as far as the merge queue is now concerned (withGrainCI), so a
// repair that did not mention it would be sending an agent to fix "the
// checks" while the only red thing is one this deployment ran itself.
// Which of its jobs failed is grainCINote's, below: this sentence has
// room for names and the paragraph after it has room for the verdict.
func healthReason(health model.PrHealth, detail github.PullRequestDetail,
	checks []github.CheckRun, grain grainCIVerdict) string {

	switch health {
	case model.PrConflicted:
		return fmt.Sprintf("it has conflicts with `%s`", detail.BaseRef)
	case model.PrFailing:
		// GitHub's checks are named here only where they are what made
		// this pull request PrFailing. Where grain CI is the gate they
		// are not: for a repository whose gate is grain CI there are
		// none to name -- forge.Observe never read them -- and for a
		// task holding grain-ci they were read and not counted, so
		// listing one would send a repair at a check that is not what
		// is stopping the merge.
		var failing []string
		if !grain.Required {
			if grain.Passed {
				failing = failingChecks(forge.FilterActionsBudgetFailures(checks))
			} else {
				failing = failingChecks(checks)
			}
		}
		if grain.Failed {
			failing = append(failing, grainCICheckName)
		}
		if len(failing) == 0 {
			return "one or more required checks are failing"
		}
		return fmt.Sprintf("its checks are failing (`%s`)", strings.Join(failing, "`, `"))
	default:
		return "it is not mergeable"
	}
}

// grainCINote is the paragraph a repair carries when grain's own CI is
// what went red: the verdict in the words the task's record already
// carries, and where the rest of it is.
//
// The rest of it is in the run, which is why this is three sentences
// rather than a log excerpt: the step output is kept with the run and
// read on demand (wait_for_checks, get_ci_log), and the comment
// announceCIRun leaves on the task is the verdict alone, so as not to be
// replayed into every later prompt (ciTaskCommentBody). This used to say
// that comment quoted the failing step's output; it has not since the
// comments were made terse, and a repair that went looking for the quote
// found one line. What the run needs from here is to know that grain CI
// is the thing holding the merge, where its output is, and that fixing
// it is the job.
func grainCINote(grain grainCIVerdict) string {
	if !grain.Failed {
		return ""
	}
	detail := grain.Detail
	if detail == "" {
		detail = "it did not pass"
	}
	return fmt.Sprintf(
		"grain's own CI (`.grain/workflows`) is what is red here: %s. It is not a GitHub "+
			"check and will not appear on the pull request. The failing step's output is "+
			"in that grain CI run -- `grain ci log` reads it, and `grain ci wait` runs it "+
			"again on a push -- and this merges once a push makes that run pass.", detail)
}

// failingJobLogs reads what CI has to say about a broken head: the
// failing jobs of the runs against headSHA, each with the tail of its own
// log, or -- for a run that has no job log to give -- the run itself.
//
// Best effort, deliberately: this is three extra GitHub reads
// (github.FailedJobLogs) against a credential that may not be allowed to
// make them, for a CI provider that may not be Actions at all, at the
// one moment the queue has finally decided to act on a broken head.
// Failing the whole cycle over what is an *annotation* on a repair would
// cost the repair itself, so an error is logged and nothing comes back.
//
// Nothing comes back is also how "this is not Actions" and "this
// credential may not read Actions" arrive, which is why an empty result
// has to leave every decision below exactly where it was before logs
// were ever fetched: it is the absence of an answer, not an answer.
//
// read reports whether GitHub answered at all, the same way
// touchedWorkflowFiles' does and for the same reason: a refused read and
// a build with no log are both "no log here" to every decision, and
// neither one is a fact anything may go on to explain (readBuildEvidence).
func failingJobLogs(client github.Client, ref model.PullRequestRef, headSHA string) (logs []github.JobLog, read bool) {
	logs, err := client.FailedJobLogs(ref.Repo.Owner, ref.Repo.Name, headSHA)
	if err != nil {
		log.Printf("orchestrator: reading the failing job logs for %s: %v -- "+
			"filing the fix task with the check names alone", ref, err)
		return nil, false
	}
	return logs, true
}

// buildEvidence is everything the queue managed to read about why a head
// is red: the failing jobs' logs, and -- only when those came back with
// nothing printable in them -- what GitHub Actions itself has against the
// commit.
//
// The second read exists because "no log" used to be reported as
// nothing at all. An empty github.FailedJobLogs is three completely
// different situations that look identical from an empty slice: no
// Actions run against the commit (the failing check belongs to some
// other provider), a run still in progress (there is no *completed
// failed run* for FailedJobLogs to walk yet, which is by far the
// commonest -- see mcp's failing_job_logs.go for the measurement), or a
// finished run whose logs are not being served. github.ActionsState
// tells them apart, and carries the failed jobs and the steps of them
// that broke, which arrive with the job itself and need no log at all.
//
// That matters more here than anywhere else. What this annotates is the
// body of a fix task, read minutes later in a cold sandbox by a run that
// cannot go and look for itself, and a repair that opens with a job name
// and no output is the exact failure the logs were added to this path to
// prevent.
//
// logs is what every decision is still made on. Nothing in this struct
// but logs is read by github.OnlyStartupFailures or by anything else
// that chooses between a repair and an escalation: the Actions state is
// read after that choice's inputs are fixed, and only ever reaches the
// prose (failingJobLogsSection).
type buildEvidence struct {
	logs    []github.JobLog
	actions github.ActionsState
	// actionsRead is whether that second read happened and answered.
	// False is both "there was nothing worth asking" and "the ask
	// failed", which are the same thing to a renderer: say what the logs
	// said and no more, exactly as this path did before the read existed.
	actionsRead bool
}

// readBuildEvidence is failingJobLogs plus, when GitHub answered and the
// answer has no log in it, the one further read that says why.
//
// Only when it answered: a log read GitHub refused is a fact about this
// deployment's credential rather than about the build, and the same
// credential is what the second read would go through. A repair whose
// logs could not be fetched therefore carries exactly the comment it
// carried before any of this existed, rather than an explanation
// composed after the read that would have justified it failed.
//
// Deliberately not the retry mcp's readJobLogs does. That one waits up
// to fifty seconds for a log that is usually seconds away, which is the
// right trade for a tool call a run is already blocked on; it is the
// wrong one here. This runs inside syncPullRequests, on a tick whose
// default period is thirty seconds (model.Config.PollInterval), and a
// cycle is not a per-pull-request goroutine: sleeping a minute per red
// head would hold up every other repo's queue, every merge ready this
// cycle, and -- since reconcilers share the tick -- the next dispatch,
// to buy prose on one comment. Waiting a cycle instead is not free
// either, and it is not what it looks like: the queue asks for exactly
// one repair per pull request (requeueForRepair stamps
// MergeQueueRepairAt and nothing clears it), so the comment is written
// once and there is no second cycle that would pick the log up. Holding
// the repair back until a log turns up would mean delaying the repair
// itself, on a clock, and letting a run that stays in progress for an
// hour delay it that long.
//
// So the queue takes the answer it has and says what it is. The failed
// job and the step of it that broke are in ActionsState whether or not a
// log is being served yet, and the repair run can fetch the log itself
// when it starts: pull_request_status reads the same commit's CI, with
// mcp's retry behind it, from a sandbox that by then is minutes newer
// than this read (noJobLogSection tells it so).
func readBuildEvidence(client github.Client, ref model.PullRequestRef, headSHA string) buildEvidence {
	logs, read := failingJobLogs(client, ref, headSHA)
	ev := buildEvidence{logs: logs}
	if !read || !actionsStateWorthReading(logs) {
		return ev
	}
	state, err := client.ActionsState(ref.Repo.Owner, ref.Repo.Name, headSHA)
	if err != nil {
		log.Printf("orchestrator: reading what Actions has against %s at %s: %v -- "+
			"filing the fix task without saying why there is no log", ref, shortSHA(headSHA), err)
		return ev
	}
	ev.actions, ev.actionsRead = state, true
	return ev
}

// actionsStateWorthReading reports whether asking GitHub what Actions has
// against this head would add anything the logs have not already said.
//
// A build with a real job log in it needs nothing: that is the answer
// this whole path is for. A build that is only startup failures needs
// nothing either -- no job of it ran a step, so there is no failed step
// to name -- and it is the one shape that does not end in a repair
// comment at all (github.OnlyStartupFailures, escalateStartupFailure),
// so the read would be spent on a message that never renders it.
//
// What is left is the two shapes with nothing to print: no failing
// Actions job found at all, and a run standing in for jobs whose logs
// were not served. Both are ones ActionsState can name a job and a step
// for.
func actionsStateWorthReading(logs []github.JobLog) bool {
	if github.AnyJobLogRead(logs) {
		return false
	}
	return !github.OnlyStartupFailures(logs)
}

// workflowDir is the one directory GitHub reads a repository's Actions
// workflows from. A file anywhere else cannot be the workflow that
// failed to parse, and a file here is the only thing in a branch's tree
// that can fail a run before a job of it starts.
const workflowDir = ".github/workflows/"

// touchedWorkflowFiles is the question that tells the two startup
// failures apart: of the files this pull request touches, which are
// workflow files? A non-empty answer means the branch itself changed
// what GitHub was asked to parse at this commit, and that a repair has
// something real to fix; an empty one leaves the failure belonging to
// the repository, where no commit reaches it (escalateStartupFailure).
//
// read reports whether GitHub answered at all, and the caller passes it
// on so the escalation it writes can be honest about which it is: "this
// pull request touches no workflow file" and "nobody could find out
// whether it does" are the same decision -- escalate, since a repair
// asked for on a guess is the run this whole path exists not to spend --
// but they are not the same sentence to read.
//
// One extra GitHub read, and only ever on a path where a queue head is
// already broken and the queue is about to either dispatch a run or wake
// a person: it costs a request per startup failure to recover the
// automatic repair in the one case it would have worked.
func touchedWorkflowFiles(client github.Client, ref model.PullRequestRef) (workflows []string, read bool) {
	files, err := client.ListPullRequestFiles(ref.Repo.Owner, ref.Repo.Name, ref.Number)
	if err != nil {
		log.Printf("orchestrator: reading which files %s touches: %v -- "+
			"escalating its startup failure rather than guessing a workflow edit "+
			"caused it", ref, err)
		return nil, false
	}
	for _, f := range files {
		if strings.HasPrefix(f, workflowDir) {
			workflows = append(workflows, f)
		}
	}
	return workflows, true
}

// unparseableWorkflowNote is what the repair comment says beyond the
// ordinary "your checks are red": that no job ran at all, that the
// likeliest reason is the workflow file this pull request itself edited,
// and -- the part that keeps the run from becoming the wasted one this
// whole path is about -- what to do if the run's page says otherwise.
//
// A workflow edit is evidence, not proof. A pull request can touch
// .github/workflows/ in the same week its repository's Actions minutes
// run out, and then the agent dispatched here is in exactly the position
// bwsalmon/grain-home#1 put one in: pushing commits at a check that
// could never pass. So the note tells it how to recognise that from the
// run's own page and to say so on the task instead, which ends the
// attempt in a sentence rather than a run.
func unparseableWorkflowNote(ref model.PullRequestRef, detail github.PullRequestDetail,
	logs []github.JobLog, workflows []string) string {

	return fmt.Sprintf(
		"GitHub concluded every one of those runs %s: each failed before a job of it ran a "+
			"step, so there is no failing test to read. This pull request edits `%s`, and a "+
			"workflow file GitHub cannot parse is the one cause of that which lives in this "+
			"branch's tree rather than in %s's settings -- so it is very likely what your "+
			"change broke, and a fix pushed to `%s` clears it.%s\n\n"+
			"Read GitHub's own reason on the run's page (linked below) first: it names the "+
			"file and line that would not parse. If it says something else -- Actions "+
			"disabled or restricted, no runner this repository may use, no Actions minutes "+
			"left -- nothing you push will fix it. Say that on this task and stop: only a "+
			"person can change that setting.",
		runConclusions(logs), strings.Join(workflows, "`, `"), ref.Repo, detail.HeadRef,
		startupFailureReason(logs),
	)
}

// failingJobLogsSection is the rest of what a repair needs and
// healthReason cannot give it: not just which jobs are red, but what they
// printed.
//
// healthReason already names the failing checks, which is what a person
// reading the task needs to know where to look. An agent dispatched
// into a sandbox needs more than that, because the sandbox is not a CI
// runner -- it may not be able to run the failing suite at all -- so
// "which test is red" has to travel with the task rather than be
// rediscovered inside it. A job name plus the tail of its log is the
// difference between "one or more required checks are failing" and a
// stack trace.
//
// A build with no log at all is no longer silence. It used to render
// nothing here, which left the repair opening with a check name and an
// invitation to go and find out what it said -- and "no log" is usually
// not "no output" but "not published yet at the moment the queue looked"
// (buildEvidence). noJobLogSection says which it was, and names the job
// and the step whatever the answer.
func failingJobLogsSection(ev buildEvidence) string {
	logs := ev.logs
	if len(logs) == 0 {
		return noJobLogSection(ev)
	}

	var b strings.Builder
	if github.AnyJobLogRead(logs) {
		b.WriteString("\n\n## What CI printed\n\n" +
			"The end of each failing job's own log:")
	} else {
		b.WriteString("\n\n## What CI says\n\n" +
			"No failing job of this build has a log to read, so this is the workflow " +
			"run's own verdict rather than anything it printed:")
	}
	for _, l := range logs {
		if l.IsRunFailure() {
			fmt.Fprintf(&b, "\n\n### %s (the workflow run, not a job of it)\n", l.Name)
			if l.URL != "" {
				fmt.Fprintf(&b, "\n%s\n", l.URL)
			}
			fmt.Fprintf(&b, "\n%s\n", github.RunFailureNote(l))
			continue
		}
		fmt.Fprintf(&b, "\n\n### %s\n", l.Name)
		if l.URL != "" {
			fmt.Fprintf(&b, "\n%s\n", l.URL)
		}
		// Four backticks, so a log that itself contains a fenced block --
		// any Go test printing three backticks does -- cannot close this
		// one early.
		fmt.Fprintf(&b, "\n````\n%s\n````\n", github.JobLogExcerpt(l.Log))
		if l.Truncated {
			b.WriteString("\n(the tail of the log, not all of it)\n")
		}
	}
	// A run reported as itself names its workflow and nothing under it,
	// which leaves the repair without even a job name. The failed jobs
	// and their steps come off the jobs endpoint rather than the logs
	// one, so they are there even when no log is (mcp's failedStepsNote
	// appends the same thing to the same shape of answer).
	if !github.AnyJobLogRead(logs) && ev.actionsRead && len(ev.actions.Jobs) > 0 {
		b.WriteString("\n\nWhich jobs those runs failed, and at which step:\n" +
			github.FailedJobsNote(ev.actions.Jobs))
	}
	return b.String()
}

// noJobLogSection is what the repair comment says where the logs would be
// when github.FailedJobLogs found none: which of the three reasons for
// that it actually was, the failing job and step if GitHub will name
// them, and what the run reading this can do about it.
//
// That last part is what makes this different from mcp's noJobLogNote,
// which says the same facts to a different reader. A tool result is read
// by a run that can simply call the tool again in a minute; this is a
// task body, read in a sandbox dispatched some minutes after the read it
// describes -- so where the answer is "the log was not published yet",
// the useful instruction is not "wait" but "the wait is already over,
// call pull_request_status and it will hand you the log".
//
// Silent when the second read did not happen or did not answer
// (buildEvidence.actionsRead): the repair then carries exactly what it
// carried before any of this existed. Nothing here is worth spending a
// queue head's one repair on a guess for.
func noJobLogSection(ev buildEvidence) string {
	if !ev.actionsRead {
		return ""
	}
	state := ev.actions

	const heading = "\n\n## What CI says\n\n"

	if state.Runs == 0 {
		return heading + "GitHub Actions has no workflow run at all against this commit, so " +
			"there is no job log to hand you: the failing check named above comes through " +
			"the Checks API from something else -- Buildkite, CircleCI, a review bot -- " +
			"which keeps its own output. Your sandbox cannot reach it and neither can " +
			"`pull_request_status`, so reproduce the failure in your checkout; if its name " +
			"does not say what to reproduce, say so on this task rather than pushing at " +
			"it blind."
	}

	if len(state.Jobs) == 0 {
		if state.Unfinished > 0 {
			return heading + "When the queue looked, " + stillRunningPhrase(state) +
				", and no job of " + runsPronoun(state.Runs) + " had failed yet: the check " +
				"above concluded ahead of the Actions API, which is ordinary. That read is " +
				"older than your sandbox, so call `pull_request_status` first -- it " +
				"fetches the failing jobs' logs for this pull request -- and read what it " +
				"says before going through the code."
		}
		return heading + allFinishedPhrase(state.Runs) +
			" -- so the failing check named above is not from Actions and has no job log " +
			"behind it. Its output is on its own page, which your sandbox cannot reach: " +
			"reproduce the failure in your checkout, and say so on this task if the " +
			"check's name is not enough to tell you what to reproduce."
	}

	var b strings.Builder
	b.WriteString(heading + "GitHub Actions served no log for this build when the queue read " +
		"it, but it names the job that broke and the step that did it:\n")
	b.WriteString(github.FailedJobsNote(state.Jobs))
	if state.Unfinished > 0 {
		b.WriteString("\nAt that moment " + stillRunningPhrase(state) + ", which is why there " +
			"is no log here: Actions only serves a job's log once its run has finished. " +
			"That read is older than your sandbox, so call `pull_request_status` -- it " +
			"will most likely fetch the log now.")
	} else {
		b.WriteString("\nThose runs had all finished and their logs were still not served: a " +
			"log past Actions' 90-day retention, or a credential that may not fetch one. " +
			"`pull_request_status` reads the same commit and will find no more, so work " +
			"from the failed steps above and reproduce that step in your checkout.")
	}
	return b.String()
}

// stillRunningPhrase and allFinishedPhrase keep the sentences above
// reading like English for the one-run case, which is the commonest one
// there is: a repository with a single workflow, whose one run against
// the commit is the whole of its CI.
func stillRunningPhrase(state github.ActionsState) string {
	if state.Runs == 1 {
		return "the one workflow run GitHub Actions has against this commit was still running"
	}
	verb := "were"
	if state.Unfinished == 1 {
		verb = "was"
	}
	return fmt.Sprintf("%d of the %d workflow runs GitHub Actions has against this commit "+
		"%s still running", state.Unfinished, state.Runs, verb)
}

// runsPronoun is what the sentence after one of those says "them" about,
// which is "it" when there is only the one run.
func runsPronoun(runs int) string {
	if runs == 1 {
		return "it"
	}
	return "any of them"
}

func allFinishedPhrase(runs int) string {
	if runs == 1 {
		return "GitHub Actions has one workflow run against this commit, it has finished, " +
			"and no job of it failed"
	}
	return fmt.Sprintf("GitHub Actions has %d workflow runs against this commit, all of them "+
		"finished, and not one failed job among them", runs)
}

// requeueForRepair is what the merge queue does about a head that is
// genuinely broken: it sends the task itself back to an agent, on the
// very branch its pull request is already open from.
//
// It replaces filing a separate fix task (bwsalmon/agents#283's own
// replacement for core.py's _suggest_fix, which filed an issue a human
// had to approve). That version worked -- the fix branch was stacked on
// the head branch and merged straight back into it -- but it bought the
// resolution at the price of a second pull request and a second full run
// of CI: once on the fix's own branch before it could merge, and again on
// the head branch afterward, with the queue waiting out both. A conflict
// resolved on the branch that has the conflict needs neither. The commits
// land on the branch the pull request is open from, GitHub re-runs that
// pull request's checks once, and the queue reads the answer on its next
// cycle.
//
// The requeue is three observations written together, and each is doing
// something:
//
//   - CompletedAt cleared is the requeue itself. StateOf reads a task
//     with no CompletedAt and no ClosedAt as 'queued' again, so
//     task_ready offers it and the next dispatch.Cycle picks it up --
//     the ordinary redispatch path every task already has, not a second
//     way of starting work. The task goes visibly back to working, which
//     is what a person watching the queue should see: the change is not
//     finished, and the thing that is not finished is this task.
//   - MergeQueueRepairAt is the record that this is a repair and not
//     somebody's second thoughts. advanceMergeQueueHead waits on it,
//     syncEntry will not merge underneath it, the store spends
//     Limits.Mergers' reserved capacity on the run because of it
//     (model.mergerTaskSQL), and it never being cleared -- with
//     MergeQueueRepairs beside it counting -- is what holds the
//     deployment to at most two automatic repairs per pull request. A
//     second is not simply retried, for core.py's own _suggest_fix
//     reason ("suggesting a fix for a fix risks an unbounded chain"): it
//     is given only when the base has moved or the failures have changed
//     since the first (secondRepairReason), and a third never is -- see
//     escalateToUser.
//   - RetryRequestedAt is what stops a task whose earlier attempts failed
//     from being unable to have this one. A run salvaged into a pull
//     request keeps its "failed" outcome forever (orchestrator.RunCycle's
//     "only the ending failed"), so a task can sit on a full failure
//     streak with a perfectly real pull request open, and StateOf would
//     read the requeued task 'failed' rather than 'queued'. Asking for
//     the repair is asking for the attempt.
//
// The comment goes in first, and it is not decoration: it is the whole of
// what the dispatched run is told. A redispatched task's prompt carries
// its conversation (BuildPrompt, commentThreadSection), so this comment
// is where the agent learns which branch is broken, how, and that it must
// push to that branch rather than start a new one -- repairReason names
// the failure and, for a failing pull request, failingJobLogsSection
// appends what the red jobs actually printed -- or, when there was no
// log to be had, which of the reasons for that it was and whichever job
// and failed step GitHub will name without one (ev, read by the caller,
// which needs its logs to decide whether to come here at all --
// advanceMergeQueueHead). Writing it before the requeue also
// means the ordering cannot leave a dispatched run with nothing to read:
// a crash in between leaves the task completed and the next cycle does
// both again.
//
// It also says what a repair's scope is, because the default reading is
// the wrong one here: every run is otherwise told that a check failing on
// the base too is not its to fix (orchestrator.BuildPrompt), and a repair
// that applies that rule reports the failure and hands back the very pull
// request this queue could not merge -- which the next cycle gives up on
// (escalateToUser). The prompt itself says the same thing to a repair run
// (repairCISection), and so does base_branch_status when it looks
// (mcp.PullRequestScope.Repair); this is the first place it reads it, and
// the three have to agree.
//
// gateIsGrainCI is read twice for that reason. Once for the scope
// sentence, which says which verdict is holding the merge, and once for
// the evidence below it, which must be about the same verdict: a
// message that opens "GitHub's own check runs are not read here" and
// then spends its longest section quoting them contradicts itself, and
// points the wrong way while doing it.
//
// note is the caller's own paragraph about this particular failure,
// dropped in between that and the logs, and empty for every repair but
// one: a build whose runs all died before starting, on a branch that
// edited a workflow file, needs to be told something the check names and
// the run's verdict cannot say by themselves (unparseableWorkflowNote).
func requeueForRepair(ctx context.Context, store *model.Store,
	task model.Task, ref model.PullRequestRef, detail github.PullRequestDetail,
	health model.PrHealth, checks []github.CheckRun, grain grainCIVerdict,
	ev buildEvidence, outcome refreshOutcome, gateIsGrainCI bool, reds forge.Reds,
	note string, attempt repairAttempt, now time.Time) error {

	reason := repairReason(outcome, health, detail, checks, grain)
	body := fmt.Sprintf(
		"%s %s, so the merge queue has sent this task back to an agent. Work on `%s`, the "+
			"branch this pull request is already open from -- no separate task, no "+
			"approval -- and %s merges as soon as it reads clean. %s",
		ref, reason, detail.HeadRef, ref, repairScopeSentence(gateIsGrainCI, reds),
	)
	if attempt.n > 0 {
		body += " " + repairAttemptSentence(attempt.n, attempt.max, attempt.why)
	}
	if note != "" {
		body += "\n\n" + note
	}
	// grain CI's own half, when it is the thing that went red. Before the
	// GitHub logs rather than after: those are what a repair usually opens
	// with, and a reader who does not know grain CI ran at all needs to be
	// told that before being shown a log section that does not contain it.
	// Where GitHub's checks are not read here there is no such section at
	// all, and this is the whole of what the repair is told about CI.
	if n := grainCINote(grain); n != "" {
		body += "\n\n" + n
	}
	// GitHub's half, and only where GitHub is a source of verdicts here.
	// The whole of this section is about Actions -- failing runs, the
	// jobs and steps under them, the explainer for a run concluded
	// before a job of it started -- and in a repository that has
	// declared those checks are not read (gateIsGrainCI,
	// model.RepoConfig.IgnoreGitHubCI) that is evidence for a verdict
	// nobody consulted, printed under a heading ("What CI says") that
	// claims to be the answer, three paragraphs beneath the sentence
	// saying it is not. The caller already skips the reads that would
	// fill it in (advanceMergeQueueHead), so this is belt as well as
	// braces: a future caller that composes a repair comment from
	// evidence it read for some other reason still must not send a
	// repair run after checks this repository does not read.
	//
	// The section itself is kept, not deleted: it is exactly what a
	// repair needs in the repositories that do read GitHub's checks,
	// which is most of them. This is a routing decision, one setting
	// wide, and it is the same setting the scope sentence above reads.
	if !gateIsGrainCI {
		body += failingJobLogsSection(ev)
		if sec := gitHubRepairFaultSection(ctx, store, task, detail.HeadRef, now); sec != "" {
			body += sec
		}
	}
	if err := queueComment(ctx, store, task.ID, body, now); err != nil {
		return err
	}

	return observeField(ctx, store, task.ID, now, func(o *model.Observation) {
		markRepairAsked(o, now, attempt.baseTip, attempt.failures, attempt.why)
	})
}

// repairAttempt is which automatic repair requeueForRepair is asking for
// (n, of maxAutomaticRepairs), what it is asked about -- the base
// branch's tip and the failure signature (failureSignature), which a
// second repair is measured against -- and, for a second, why it was
// allowed (secondRepairReason).
type repairAttempt struct {
	// n is which repair this is, of max (repairCap).
	n, max                 int
	baseTip, failures, why string
}

// baseTipOf is the base branch's tip, or "" where GitHub would not say --
// which a second repair reads as "not known to have moved".
func baseTipOf(client github.Client, ref model.PullRequestRef, base string) string {
	if client == nil || base == "" {
		return ""
	}
	head, err := client.GetBranchHead(ref.Repo.Owner, ref.Repo.Name, base)
	if err != nil {
		log.Printf("orchestrator: reading %s's head on %s: %v", base, ref.Repo, err)
		return ""
	}
	if head == nil {
		return ""
	}
	return head.SHA
}

// repairScopeSentence is the last sentence of the repair comment: what
// this run is on the hook for.
//
// The ordinary one puts inherited breakage in scope, and has to: the
// queue does not care whose commit turned a required check red, only
// that it is red, and a repair that reports "already failing on the
// base, not mine" hands back exactly the pull request the queue could
// not merge (requeueForRepair's own doc comment, repairCISection, and
// mcp.PullRequestScope.Repair, which have to agree with this).
//
// It becomes a false instruction in a repository that has declared
// GitHub's check runs are not a source of verdicts for it
// (model.RepoConfig.IgnoreGitHubCI). There, a red GitHub check is not
// "already red on the base still stopping this pull request merging" --
// it is not stopping anything, nobody read it, and the queue is not
// waiting on it. Telling a run to fix it anyway spends a whole repair on
// a check the deployment has said it does not listen to, which is how
// this deployment's agents ended up arguing with a merge queue comment
// in their closing notes for a week (grain/task-116, -188, -191, -202).
//
// It becomes a false instruction for a second reason too, and this one
// is per pull request rather than per repository: a check that is red
// for something no commit reaches -- a run that never started, a CLA bot
// that will not pass for this author -- is not "already red on the base
// and still stopping the merge" in any sense an agent can act on. It is
// stopping the merge, and a person is the only one who can clear it. So
// where this repair is being sent for red the branch genuinely owns
// *beside* red it does not (forge.Reds), the sentence names both and is
// explicit about which half the run is on the hook for. That is the
// state grain/task-219 was in, and telling its repair that all eight
// checks were its to fix is how a real regression in one of them spent a
// run being argued about rather than fixed.
func repairScopeSentence(gateIsGrainCI bool, reds forge.Reds) string {
	if !gateIsGrainCI && !reds.PathToGreen() && reds.BranchOwns() {
		return fmt.Sprintf("Not all of it is yours, and this is the one repair where that "+
			"matters: `%s` is red for a reason nothing pushed to this branch reaches, and "+
			"a person has to clear it before this can merge at all. What is in scope is "+
			"`%s` -- fix that, push it, and leave the rest to whoever owns the setting "+
			"behind it rather than spending the run proving it cannot be fixed here.",
			strings.Join(reds.Inherited, "`, `"), strings.Join(reds.Own, "`, `"))
	}
	if gateIsGrainCI {
		return "This repository's gate is grain CI (`.grain/workflows`); GitHub's own check " +
			"runs are not read here, so a red one on this branch is not what is holding " +
			"the merge and is not yours to chase. What is in scope is everything grain CI " +
			"says about the commit you push -- get a full-suite pass for that exact " +
			"commit, and fix a failing job rather than weakening it."
	}
	return "Everything failing on it is in scope: a check already red on the base still " +
		"stops this pull request merging, so fix it here rather than reporting it as " +
		"somebody else's."
}

// queueComment records something the merge queue said about a task,
// attributed to it rather than to grain generally -- the merge queue is
// its own automation principal, and a human reading the conversation can
// tell its remarks from a relayed agent's.
func queueComment(ctx context.Context, store *model.Store, taskID, body string, now time.Time) error {
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    taskID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "merge-queue"}},
		Body:      body,
		CreatedAt: now,
	}); err != nil {
		return fmt.Errorf("orchestrator: commenting on %s: %w", taskID, err)
	}
	return nil
}

// blockMergeQueue is the merge queue's other exit besides merging, and
// bwsalmon/agents#283's "mark the task as needing user input and move
// onto the next task in the queue." Marking is obs.MergeQueueBlockedAt --
// which isQueueMember reads to exclude task from ever being a queue head
// again, freeing whatever is behind it to become head next cycle instead
// of waiting on a task nothing further will fix by itself -- plus a
// comment on the task's own issue, since a label a human would need to
// know to look for is easy to miss and a comment lands wherever they're
// already watching.
//
// Blocked is not abandoned: syncEntry still merges such a task the moment
// it reads clean, so whatever the comment asked a person to do, doing it
// is enough. What is given up is the queue position and the automatic
// fix, not the merge. And giving those up is not final either -- a person
// who has dealt with what the comment named can ask for the whole round
// again (ui.Client.RetryMerge, the UI's "Retry merge" button), which
// clears this stamp along with the repair and the refresh. Nothing here
// clears it: the queue asks for a person exactly once per pull request,
// and only a person takes that back.
//
// The four callers below are the four reasons the queue gives up, and
// they say different things to the person now holding it: escalateToUser
// for a repair that ran and did not take, escalateUnfinishedRepair for
// one that never finished running at all, escalateStalledChecks for CI
// that never reported, and escalateStartupFailure for CI that failed in
// a way no repair could have addressed -- the only one of the four that
// gives up before asking for a repair rather than after. (Two more join
// them from outside this file: escalateRefusedMerge and
// escalateUnfinishedReview.)
//
// reason is the same news in a row's worth of words, and it is a separate
// argument from the comment because the two are read in different places
// and at different lengths: the comment is a paragraph on the task's own
// thread, and this is the line beside the task's title that says which
// phase of the merge cycle it died in (mergeStateFailed, queuestatus.go).
// Without it a blocked task's row said only "Merge blocked", and finding
// out what of took opening the thread -- which is the shape of complaint
// this whole file keeps answering.
func blockMergeQueue(ctx context.Context, store *model.Store, taskID, comment, reason string, now time.Time) error {
	return blockMergeQueueAgainst(ctx, store, taskID, comment, reason, "", now)
}

// blockMergeQueueAgainst is blockMergeQueue for a caller that knows which
// base the verdict it is blocking on was actually tested against
// (blockedBase), and so can seed Observation.MergeQueueBlockedBase with
// it rather than leaving rearmBlockedOnBaseMove to snapshot the base as
// it is on its next sighting. That snapshot is wrong whenever the base
// moved while the red CI run was still in flight: the movement would be
// taken as the base the change was blocked against, and the one re-arm
// it should have earned would never come (grain/task-189). "" keeps
// blockMergeQueue's own behaviour.
func blockMergeQueueAgainst(ctx context.Context, store *model.Store, taskID, comment, reason,
	blockedBase string, now time.Time) error {
	if err := queueComment(ctx, store, taskID, comment, now); err != nil {
		return err
	}
	return observeField(ctx, store, taskID, now, func(o *model.Observation) {
		o.MergeQueueBlockedAt = &now
		// A fresh block is measured against the base its red verdict was
		// tested on where the caller knows it, and otherwise against the
		// base as it is now (rearmBlockedOnBaseMove records it on its
		// next sighting). Either way it is no longer waiting on the base
		// for anything.
		o.MergeQueueBlockedBase = blockedBase
		o.MergeQueueInherited = ""
		// The same discipline setMergeQueueState keeps, for the same
		// reason: entering the state starts its clock, and a task
		// already sitting in merge_failed (a refusal the queue has been
		// retrying, which is now out of patience) has been failing to
		// merge since it first said so.
		if o.MergeQueueState != mergeStateFailed {
			at := now
			o.MergeQueueStateAt = &at
		}
		// Whatever wait was flagged as stuck is over: this is a state
		// with no patience at all, because it is the one state that is
		// meant to stand still (liveness.go). Leaving the flag on would
		// put "stalled" beside a row whose whole message is that a
		// person now owns it, which is two different alarms for one
		// fact.
		o.MergeQueueStalledAt = nil
		// The reason each escalation writes says what this died of and not
		// what to do about it; the way out comes from the classification
		// that calls this state a person's (liveness.go).
		o.MergeQueueState = mergeStateFailed
		o.MergeQueueNote = mergeLane.noteWithWayOut(mergeStateFailed, reason)
		o.MergeQueueReason = mergeReasonGaveUp
	})
}

// escalateToUser gives up on task because the repair run the queue asked
// for ran and finished and its PR is still broken. (A repair that has not
// finished at all is escalateUnfinishedRepair's, and says so
// differently.)
//
// This never asks for another automatic repair itself. core.py's own
// _suggest_fix reasoning ("suggesting a fix for a fix risks an unbounded
// chain") applies just as much to redispatching a task whose repair
// already failed without anything about the PR having changed in between
// -- which is why the one second repair there is (secondRepairReason) is
// given only where the base moved or the failures changed, by the caller,
// and why a third is never given at all. spent is how many it has had.
//
// outcome is what the caller's own second refresh made of the branch
// first (advanceMergeQueueHead), and it is worth a sentence rather than
// dropping: the first thing a person picking this up would try is
// merging the base in, and the queue has just tried it for them. 204
// says the branch was not behind at all, 409 says it conflicts for real,
// and either way saying so saves them the attempt.
func escalateToUser(ctx context.Context, store *model.Store,
	task model.Task, ref model.PullRequestRef, health model.PrHealth,
	outcome refreshOutcome, spent int, rc repairCap, now time.Time) error {

	comment := fmt.Sprintf(
		"The automatic repair of %s didn't take -- %s -- so this needs a person. Push a fix "+
			"by hand (or resolve it on GitHub) and %s merges as soon as it reads clean. The "+
			"queue has moved on to the next task in %s.",
		ref, healthReasonSuffix(health), ref, ref.Repo,
	)
	switch outcome {
	case refreshUpToDate:
		comment += " The queue tried merging the base in first and it was already up to " +
			"date: this is not a branch that has simply fallen behind."
	case refreshConflicted:
		comment += " The queue tried merging the base in first and git refused it: what is " +
			"left is a real resolution, not a stale branch."
	}
	comment += "\n\n" + repairsSpentSentence(spent, "the base branch", rc)
	reason := "the repair ran and did not take -- " + healthReasonSuffix(health) +
		", and " + repairsSpentReason(spent, rc)
	// The red being escalated was tested against the base the last repair
	// or re-arm merged in (Observation.MergeQueueRepairBase), not
	// necessarily the base as it is now: the base may have moved while
	// that CI run was in flight. Unless the queue has just found the
	// branch up to date with the current base, block against the base it
	// was actually tested on, so rearmBlockedOnBaseMove sees any movement
	// since as the movement it is.
	blockedBase := ""
	if outcome != refreshUpToDate {
		if obs, err := store.GetObservation(ctx, task.ID); err == nil && obs != nil {
			blockedBase = obs.MergeQueueRepairBase
		}
	}
	return blockMergeQueueAgainst(ctx, store, task.ID, comment, reason, blockedBase, now)
}

// escalateUnfixableChecks gives up on task -- without spending its
// repair, and without the base merge that would have come first --
// because every check between this pull request and a merge is one
// nothing pushed to its branch reaches.
//
// It is escalateStartupFailure's sibling, and the two are separate
// because what a person is being asked to do differs. There, every run
// in the repository died before a step and the fix is a setting on the
// repository or the account behind it; here the checks ran and reported,
// and one of them is simply never going to pass for a pull request this
// deployment's agents author. `cla/google` is the case that forced it:
// restoring an Actions budget clears every never-started run in a
// repository and leaves that one red on every agent-authored pull
// request, permanently, which is a standing fact somebody declares
// (model.RepoConfig.UnfixableChecks) rather than an outage that ends.
//
// afterRepair says whether this pull request's one automatic repair has
// already been spent, which changes a sentence and nothing else. Reached
// before the repair, the news is that grain is not spending it; reached
// after, the news is that the repair -- whatever it fixed -- was never
// going to be enough on its own.
//
// Blocked, not abandoned, like every escalation here: syncEntry merges
// this task the moment its pull request reads clean, so whoever clears
// the check gets the merge with no push. And where the finding is about
// the repository rather than about this pull request, clearing it clears
// every entry grain has open against that repository at once -- which is
// the sentence the queue used to leave on one task's thread and nowhere
// else (grain/task-291).
func escalateUnfixableChecks(ctx context.Context, store *model.Store, task model.Task,
	ref model.PullRequestRef, reds forge.Reds, unfixable forge.Unfixable,
	afterRepair bool, now time.Time) error {

	spent := "the merge queue has not spent this pull request's one automatic repair on it, " +
		"and has not merged the base branch in either: neither could change that answer"
	if afterRepair {
		spent = "the repair this pull request already had could not have been enough on its " +
			"own, and the merge queue has not merged the base branch in again: neither " +
			"reaches what is red"
	}
	comment := fmt.Sprintf(
		"%s cannot go green from this branch. What is failing is %s, and no commit pushed to "+
			"`%s` clears it%s -- so %s, and this now needs a person.\n\n"+
			"%s merges as soon as it reads clean, with no push at all: whoever clears this "+
			"gets the merge for free.%s The queue has moved on to the next task in %s.",
		ref, unfixableChecksPhrase(reds), refHeadBranch(ref, task), unfixableWhy(unfixable), spent,
		ref, unfixableBreadth(unfixable, ref.Repo), ref.Repo,
	)
	return blockMergeQueue(ctx, store, task.ID, comment, unfixableReason(reds), now)
}

// unfixableChecksPhrase names the red this is about, backticked, in the
// order the checks were reported.
func unfixableChecksPhrase(reds forge.Reds) string {
	if len(reds.Inherited) == 0 {
		return "one or more checks this branch cannot reach"
	}
	return "`" + strings.Join(reds.Inherited, "`, `") + "`"
}

// unfixableWhy is grain's own evidence for the claim, where it has any --
// the reading of the repository's recent runs that repoFinder made. A
// declaration needs none: an operator has said so, and saying it back to
// them is not evidence.
func unfixableWhy(unfixable forge.Unfixable) string {
	if unfixable.Why == "" {
		return ""
	}
	return " (" + unfixable.Why + ")"
}

// unfixableBreadth is the sentence that makes this a fact about the
// repository rather than about one pull request -- the one the merge
// queue used to write on whichever task happened to reach the front of
// the queue, and nowhere else.
func unfixableBreadth(unfixable forge.Unfixable, repo model.RepoRef) string {
	if !unfixable.ChecksNeverStart {
		return ""
	}
	return fmt.Sprintf(" This is not a fact about this pull request: every pull request open "+
		"against %s is failing this way and none of them can be repaired from a branch, so "+
		"whoever changes the setting behind it clears all of them at once.", repo)
}

// unfixableReason is the same news in a row's worth of words
// (blockMergeQueue's own `reason`).
func unfixableReason(reds forge.Reds) string {
	return "nothing pushed to this branch turns " + unfixableChecksPhrase(reds) +
		" green, so grain handed it to a person rather than spend a repair that could not have worked"
}

// refHeadBranch is the branch a repair would have been asked to push to,
// for a sentence written where the pull request's own detail is no longer
// in hand. The task's own branch is what the queue would have sent an
// agent to, and a task with none named falls back to saying so plainly.
func refHeadBranch(ref model.PullRequestRef, task model.Task) string {
	if task.Branch != "" {
		return task.Branch
	}
	return ref.String()
}

// escalateStartupFailure gives up on task without ever asking for the
// repair: every failing workflow run against its head is one GitHub
// concluded before a job of it started, so there is no failing job, no
// log, and nothing in the tree that a commit could change to make the
// check pass.
//
// This is the one place the queue declines to spend the automatic repair
// on a head it can see is broken, and the reason is that spending it
// here is spending it on nothing. bwsalmon/grain-home#1 is the worked
// example: the queue asked for a repair, an agent ran a full attempt and
// concluded -- correctly -- that the check could not go green from that
// repository, and the queue escalated to a person anyway one run later.
// The run was not wasted work that happened to fail; it was work that
// could not have succeeded, because Actions was restricted for the
// repository and every commit's run died at startup.
//
// Only three of the four causes get here, though, and that is the point
// of workflowsRead: a workflow file this very pull request made
// unparseable is a startup failure whose cause *is* in the tree, and an
// agent could have fixed it. A check-run name and a run conclusion carry
// nothing that separates it from the other three, so the caller asks
// GitHub which files the pull request touches and repairs that one
// instead of escalating it (advanceMergeQueueHead). What arrives here is
// a startup failure with no workflow edit behind it -- or, when
// workflowsRead is false, one whose branch could not be read at all,
// which escalates for the same reason it always did: a repair asked for
// on a guess is exactly the run this path exists not to spend.
//
// workflowsRead only changes the sentence, not the decision. A comment
// that tells a person the first of the four causes is ruled out when
// nobody managed to check would send them looking at the other three on
// grain's word alone.
//
// starts is the same kind of evidence about the other three, read from
// the repository's own recent runs rather than from this pull request's
// (checkstartups.go): it says whether anything here gets a job started
// at all, which is the difference between "your repository's checks have
// never run" and "this one workflow cannot start". It changes the
// sentence and not the decision either -- and an empty RunStarts, which
// is what a credential that cannot read Actions leaves, adds nothing to
// the comment at all.
//
// Blocked, not abandoned, like every other escalation here: whoever
// fixes the workflow file -- or turns Actions back on, or buys minutes
// -- gets the merge for free, since syncEntry still merges this task the
// moment its pull request reads clean.
func escalateStartupFailure(ctx context.Context, store *model.Store,
	task model.Task, ref model.PullRequestRef, detail github.PullRequestDetail,
	checks []github.CheckRun, grain grainCIVerdict, logs []github.JobLog,
	workflowsRead bool, starts github.RunStarts, now time.Time) error {

	firstCause := fmt.Sprintf("Only the first can be fixed from this branch, and the merge "+
		"queue could not read which files this pull request touches, so it did not rule "+
		"that out: if this pull request edited a file under `%s`, that is where to look, "+
		"and a commit on `%s` would clear it.", workflowDir, detail.HeadRef)
	if workflowsRead {
		firstCause = fmt.Sprintf("Only the first can be fixed from this branch, and it is "+
			"ruled out already: this pull request touches no file under `%s`, so nothing "+
			"in this branch's tree is what failed and no commit pushed to `%s` would "+
			"clear it.", workflowDir, detail.HeadRef)
	}
	comment := fmt.Sprintf(
		"%s %s, but GitHub concluded every one of those runs %s: each failed before a single "+
			"job of it ran a step, so the queue has gone straight to a person rather than "+
			"spend this pull request's one automatic repair on it. Nothing ran, so there is "+
			"no failing job and no log -- and the run's page draws its jobs with no steps "+
			"under them, which is what a run that never started looks like.%s\n\n"+
			"The causes are a handful: a workflow file GitHub cannot parse at this commit, "+
			"Actions disabled or restricted for the repository, no runner it may use, or an "+
			"account with no Actions minutes left (a spending limit, or a failed payment). "+
			"%s The others are settings on %s and the account behind it, which no run of "+
			"this task can reach.%s%s\n\n"+
			"Push a fix by hand (or change the setting) and %s merges as soon as it reads "+
			"clean. The queue has moved on to the next task in %s.",
		ref, healthReason(model.PrFailing, detail, checks, grain), runConclusions(logs),
		startupFailureReason(logs),
		firstCause, ref.Repo, startupFailureRunPages(logs),
		repositoryStartupNote(ref.Repo, starts), ref, ref.Repo,
	)
	reason := "its checks never started -- every run against this commit died before a job of " +
		"it ran, which no repair could fix, so grain has handed it to a person"
	return blockMergeQueue(ctx, store, task.ID, comment, reason, now)
}

// runConclusions is how GitHub labelled the runs behind these entries,
// backticked and without repeats -- for a comment that used to say
// `startup_failure` outright and would now be lying half the time it is
// written: a job GitHub creates and then refuses to start (no minutes, no
// runner) leaves a run concluded plain `failure`, which is the same
// class of failure under a different word (see github.JobLog.NoJobStarted).
func runConclusions(logs []github.JobLog) string {
	var seen []string
	for _, l := range logs {
		if l.RunConclusion != "" && !slices.Contains(seen, l.RunConclusion) {
			seen = append(seen, l.RunConclusion)
		}
	}
	if len(seen) == 0 {
		return "a failure"
	}
	return "`" + strings.Join(seen, "` / `") + "`"
}

// startupFailureReason is GitHub's own sentence about why those runs
// failed, when one of them carried one -- the difference between a person
// reading a list of four things it might be and reading the one it is.
//
// It is the whole point of the read behind github.JobLog.RunFailureReason:
// "The job was not started because recent account payments have failed or
// your spending limit needs to be increased" is not a thing anybody
// derives from a run conclusion, and grain had it in hand for three
// escalations of bwsalmon/grain-home#1 before it started passing it on.
func startupFailureReason(logs []github.JobLog) string {
	for _, l := range logs {
		if l.RunFailureReason == "" {
			continue
		}
		return "\n\nGitHub has said why, and this is its own wording:\n\n" +
			github.QuoteReason(l.RunFailureReason)
	}
	return ""
}

// startupFailureRunPages points the person escalateStartupFailure just
// woke at GitHub's own reason for the failure, which is on the run's page
// and nowhere else: the API says only `startup_failure`, while the page
// says which line of which workflow file would not parse, or that Actions
// is disabled for the repository. Without the link the comment can only
// list the four causes; with it, the reader can tell which one it is.
func startupFailureRunPages(logs []github.JobLog) string {
	var b strings.Builder
	for _, l := range logs {
		if l.URL == "" {
			continue
		}
		if b.Len() == 0 {
			b.WriteString("\n\nGitHub's own reason for it is on the run's page:\n")
		}
		fmt.Fprintf(&b, "\n- %s: %s", l.Name, l.URL)
	}
	return b.String()
}

// escalateUnfinishedRepair gives up on task because the repair asked of
// it has been neither finished nor abandoned for longer than
// defaultRepairDeadline, so the head has been holding its queue position
// on a repair that is not arriving.
//
// The task is left dispatchable exactly as it is -- queued, running, or
// waiting on an answer. Nothing here can tell which of those it is stuck
// in, and cancelling the run would throw away work for no gain: if it
// does finish and the branch goes clean, ref itself, now blocked rather
// than abandoned, still merges the moment it reads clean. What the queue
// gives up is the position and the waiting, not the merge.
func escalateUnfinishedRepair(ctx context.Context, store *model.Store,
	task model.Task, ref model.PullRequestRef, health model.PrHealth, now time.Time) error {

	comment := fmt.Sprintf(
		"The automatic repair of %s never finished -- sent back to an agent more than %s ago "+
			"and still not run to completion, and %s -- so this needs a person. Its latest "+
			"attempt may be stuck; either way a fix pushed by hand is enough, and %s merges "+
			"as soon as it reads clean. The queue has moved on to the next task in %s.",
		ref, humanDuration(defaultRepairDeadline), healthReasonSuffix(health),
		ref, ref.Repo,
	)
	reason := fmt.Sprintf(
		"the repair never finished -- this task was sent back to an agent more than %s ago "+
			"and has not run to completion", humanDuration(defaultRepairDeadline))
	return blockMergeQueue(ctx, store, task.ID, comment, reason, now)
}

// escalateStalledChecks gives up on task for the third reason: its checks
// have been unfinished for longer than deadline (checksStalled), so the
// queue has been holding its own head position on a signal that is not
// coming.
//
// No fix task is filed first, which is the one way this differs in shape
// from every other way the queue gives up. A fix task exists to repair a
// pull request, and CI that never reports has said nothing about the pull
// request being broken -- the usual causes sit entirely outside it, and
// dispatching an agent to "fix" a workflow waiting on an approval would
// spend a run to produce, at best, nothing. So this goes straight to the
// person, naming what it was waiting on.
//
// The clock goes with the wait. Giving up is the queue saying it is no
// longer waiting on these checks, so the sighting that was timing them
// has nothing left to time -- and leaving it lying around is what would
// make a human's "try again" (ui.Client.RetryMerge) worthless: the retry
// clears the block, and the very next cycle would read a clock still
// standing at the whole deadline and give up again before anyone saw it
// try. queueEntry.observe declines to time a blocked entry for the same
// reason; this is the moment that rule starts applying to this one.
//
// retried is what the queue's one automatic retry of this stall did
// (retryStalledCI), empty where it gave none, and starvedSince is when
// grain CI became short of machines deployment-wide (ciStarvation), zero
// where it is not. Both are said after the comment above rather than
// instead of it, so a person reads that the retry is spent and that the
// wait is the deployment's rather than the pull request's.
func escalateStalledChecks(ctx context.Context, store *model.Store,
	task model.Task, ref model.PullRequestRef, checks []github.CheckRun,
	grain grainCIVerdict, deadline time.Duration, retried string, starvedSince time.Time,
	now time.Time) error {

	forgetPendingChecks(ref)
	comment := fmt.Sprintf(
		"%s has had %s for more than %s, so the merge queue has stopped waiting and moved on "+
			"to the next task in %s. Nothing has failed, so there is no automatic fix to "+
			"file -- %s Have a look (or merge %s by hand); if the checks do report clean it "+
			"still merges on its own.",
		ref, stalledChecksPhrase(checks, grain), humanDuration(deadline), ref.Repo,
		stalledChecksCause(checks, grain), ref,
	)
	reason := fmt.Sprintf("gave up waiting on CI after %s: %s",
		humanDuration(deadline), stalledChecksPhrase(checks, grain))
	if retried != "" {
		comment += fmt.Sprintf("\n\nThe queue had already retried once on its own (%s), and that "+
			"wait stalled too.", retried)
		reason += ", after one automatic retry"
	}
	if !starvedSince.IsZero() {
		starved := ciStarvationSentence(starvedSince, now)
		comment += "\n\n" + capitalize(starved) + ", so this is the deployment's CI capacity rather than anything about this pull request."
		reason += " -- " + starved + ", not this pull request's doing"
	}
	return blockMergeQueue(ctx, store, task.ID, comment, reason, now)
}

// grainCIGivenUp is a required grain CI verdict that is over without
// being an answer -- errored, or cancelled by grain or by a person -- at a commit
// grain has finished retrying (ciReplacementFor's Spent). Nothing is in
// flight and nothing will be started, so it is a wait only a person, or
// a change to this deployment's configuration (ciRequirementsNowGranted),
// can end.
func grainCIGivenUp(grain grainCIVerdict) bool {
	return grain.Required && !grain.Passed && !grain.Failed && !grain.Running &&
		(grain.Errored || grain.Cancelled) && grain.Replacement == forge.GrainCIReplacementSpent
}

// escalateGivenUpGrainCI is escalateStalledChecks for the wait it has no
// clock for: grain CI at this commit is spent (grainCIGivenUp), so the
// queue hands the pull request to a person at once instead of after the
// stall deadline. It says the same things -- stalledChecksPhrase names
// the run and that grain has stopped starting them -- and, like that
// one, the pull request still merges on its own if a later run passes.
func escalateGivenUpGrainCI(ctx context.Context, store *model.Store,
	task model.Task, ref model.PullRequestRef, checks []github.CheckRun,
	grain grainCIVerdict, now time.Time) error {

	forgetPendingChecks(ref)
	phrase := stalledChecksPhrase(checks, grain)
	comment := fmt.Sprintf(
		"%s has %s, so the merge queue has stopped waiting and moved on to the next task in %s "+
			"rather than holding its place for a run nobody is going to start. Re-run grain CI "+
			"from this task (or merge %s by hand); if the check does report clean it still "+
			"merges on its own.",
		ref, phrase, ref.Repo, ref)
	reason := "grain CI stopped retrying: " + phrase
	return blockMergeQueue(ctx, store, task.ID, comment, reason, now)
}

// stalledChecksPhrase names what the queue has been waiting on, for the
// comment above. The no-checks-at-all case is reachable despite the
// registration window: that window only settles a commit GitHub has named
// a head sha for (emptyChecksSettled), so a pull request GitHub never
// finishes thinking about reads PENDING with nothing to name.
//
// A grain CI run that never concluded is named alongside GitHub's, since
// it is one of the things that can hold a pull request PENDING now
// (withGrainCI) -- and it is the one whose usual causes are this
// deployment's own rather than GitHub's, which is worth a person knowing
// before they go looking at the pull request for it. A run that
// concluded having never got a machine to run in (grainCIVerdict.Errored)
// is the same wait with its cause already known, and it says so.
func stalledChecksPhrase(checks []github.CheckRun, grain grainCIVerdict) string {
	var unfinished []string
	if readsGitHubChecks(grain) {
		unfinished = unfinishedChecks(checks)
	}
	if grain.Running {
		unfinished = append(unfinished, grainCICheckName)
	}
	var parts []string
	if len(unfinished) > 0 {
		parts = append(parts, fmt.Sprintf(
			"checks that have not finished (`%s`)", strings.Join(unfinished, "`, `")))
	}
	// An errored grain CI run is the one wait here that is over rather
	// than outstanding, and the one whose cause is certainly this
	// deployment's: it says so in its own words, since nothing about the
	// pull request explains it and stalledChecksCause is about GitHub's.
	if grain.Errored {
		detail := grain.Detail
		if detail == "" {
			detail = "it could not run"
		}
		parts = append(parts, fmt.Sprintf(
			"a `%s` run this deployment could not do at all (%s), which is a machine to "+
				"fix here rather than anything wrong with this branch%s",
			grainCICheckName, detail, grainReplacementNote(grain)))
	}
	if readsGitHubChecks(grain) {
		if cancelled := cancelledChecks(checks); len(cancelled) > 0 {
			parts = append(parts, fmt.Sprintf(
				"checks GitHub cancelled without a verdict (`%s`)", strings.Join(cancelled, "`, `")))
		}
		if awaiting := awaitingApprovalChecks(checks); len(awaiting) > 0 {
			parts = append(parts, fmt.Sprintf(
				"workflow runs waiting for somebody to approve them (`%s`)", strings.Join(awaiting, "`, `")))
		}
	}
	// grain CI's own silence, and the reason this function stopped being
	// only about GitHub's checks. A repository on grain CI has that
	// verdict as its whole CI gate (forge.HealthOf), so a commit whose
	// run was cancelled -- or whose run has not been recorded at all --
	// is PENDING with nothing above naming why, and the row said
	// "waiting on no check reported against it at all" about a
	// repository whose checks are all grain's own. That is what left
	// grain/task-149 with no sentence anybody could act on.
	if grain.Required && !grain.Passed && !grain.Running && !grain.Failed && !grain.Errored {
		if grain.Cancelled {
			parts = append(parts, fmt.Sprintf(
				"a `%s` run that was cancelled without a verdict%s%s", grainCICheckName,
				grainCancellationNote(grain.Detail), grainReplacementNote(grain)))
		} else {
			parts = append(parts, fmt.Sprintf(
				"a full-suite `%s` pass for this commit, which has not been recorded yet",
				grainCICheckName))
		}
	}
	if len(parts) == 0 {
		return noChecksReportedPhrase
	}
	return strings.Join(parts, " and ")
}

// noChecksReportedPhrase is stalledChecksPhrase with nothing to name.
const noChecksReportedPhrase = "no check reported against it at all"

// readsGitHubChecks is whether GitHub's checks are a source of verdicts
// for a commit with this grain CI verdict: where grain CI is not
// required, and where merge rules require GitHub's checks beside it
// (grain/task-1993: a `ci.mode: both` change waiting on a queued
// `cla/google` read "waiting on no check reported against it at all").
func readsGitHubChecks(grain grainCIVerdict) bool { return !grain.Required || grain.GitHubToo }

// grainCancellationNote is what a cancelled grain CI run's own record
// says about itself (model.CIStatus.Detail), rendered as the
// parenthetical the sentence above hangs off "cancelled without a
// verdict" -- and, through it, the same parenthetical in the comment
// escalateStalledChecks leaves on the pull request and in the reason
// blockMergeQueue records.
//
// The reason is the whole point of it. A cancellation now carries the
// note whoever stopped the run left (ci.Run.CancelReason -- a person
// pressing cancel, the daemon shutting down under the run, the queue
// superseding it when the branch moved), and ciDetail writes that note
// into the status as the whole of the detail. So the row can say
// "cancelled when the daemon restarted" rather than "cancelled", which
// is the difference between a person going to look for a stuck runner
// and a person not going at all.
//
// The conclusion word comes off the front because the sentence has
// already said it: ciDetail spells every cancellation "cancelled: ...",
// and interpolating that whole made a row read "cancelled without a
// verdict: cancelled: ...". What is left is the reason where somebody
// left one, and the jobs the cancellation stopped where nobody did --
// both worth the reader's eye, neither worth a second "cancelled".
func grainCancellationNote(detail string) string {
	detail = cancelReasonOf(detail)
	if detail == "" {
		return ""
	}
	return " (" + detail + ")"
}

// cancelReasonOf is the note whoever cancelled the run left, off the
// status's detail (ciDetail writes "cancelled: <note>"), or "" where
// nobody left one. A detail that is only the jobs the cancellation
// stopped is not a reason, but it is not distinguishable from one here,
// and quoting it costs the reader nothing.
func cancelReasonOf(detail string) string {
	detail = strings.TrimSpace(detail)
	if rest, ok := strings.CutPrefix(detail, string(ci.ConclusionCancelled)); ok {
		// The word only comes off where it is the whole of the detail or
		// the label on the rest of it, so a reason that merely begins
		// "cancelled by ..." keeps every word it was written with.
		if rest = strings.TrimSpace(rest); rest == "" || strings.HasPrefix(rest, ":") {
			detail = strings.TrimSpace(strings.TrimPrefix(rest, ":"))
		}
	}
	return detail
}

// grainReplacementNote finishes the sentence above: whether another run
// is coming for a commit whose own run was cancelled.
//
// It exists because the clause it replaces -- "which nothing but another
// run will replace" -- was a true sentence written when nothing replaced
// one, and grain now does (ciReplacementFor). A row that still said it
// while a retry was pending would send somebody to push an empty commit
// at a branch grain was about to run again; a row that said it about a
// commit grain has finished retrying would leave them thinking the wait
// still ends by itself. Each of the endings says who is next. A
// cancellation a person asked for is replaced like any other
// (ciReplacementFor), so there is no ending of its own for it.
//
// The errored arm above wears it too (grain/task-480). "A machine to fix
// here" is as true of a verdict grain is about to retry as of one it has
// given up on, which is exactly why it needed saying which: a row that
// read the same either way left a commit grain had stopped running
// looking like one it was still working on.
func grainReplacementNote(grain grainCIVerdict) string {
	switch grain.Replacement {
	case forge.GrainCIReplacementDue:
		return fmt.Sprintf(", which grain is replacing with another run for the same commit "+
			"(attempt %d of %d)", grain.Attempts+1, ciMaximumAttempts)
	case forge.GrainCIReplacementSpent:
		return fmt.Sprintf(", which nothing but another run will replace -- and grain has "+
			"stopped starting them after %d attempts at this commit, so this one needs a person",
			grain.Attempts)
	default:
		return ", which nothing but another run will replace"
	}
}

// stalledChecksCause is the sentence in the same comment that says what
// this kind of wait usually means, and it is two sentences because the
// two waits have nothing in common but the clock. A check that never
// finishes is stuck on something outside the pull request; a cancelled
// one is finished, and a re-run is the only thing that will get a
// verdict out of it (healthFrom's "cancelled is neither"). Telling
// somebody woken at two hours to go looking for a runner that never
// picked the job up, when what actually happened is that a person
// pressed cancel, is the whole of what this avoids.
// grain CI's own cancellation is counted as a cancelled check here, since
// it is the same news: a run that is over, a verdict that was never
// delivered, and nothing that will deliver one without another run. The
// cause differs only in who cancelled it -- a person in this deployment's
// own UI, or its daemon shutting down under the run -- which the run's own
// record says, and which stalledChecksPhrase now quotes off that record
// (grainCancellationNote), so this general sentence still does not need to.
func stalledChecksCause(checks []github.CheckRun, grain grainCIVerdict) string {
	const neverFinished = "a check that never finishes is usually waiting on something " +
		"outside the pull request: a workflow approval nobody gave, a runner that never " +
		"picked the job up, a provider that reported \"queued\" and went away."
	const cancelled = "a cancelled check is one whose verdict was never delivered, and " +
		"nothing but a re-run will deliver it -- somebody pressed cancel on the run, or a " +
		"job that failed took its siblings down with it."
	const awaitingApproval = "a check concluded \"action_required\" is a workflow run GitHub is " +
		"holding until a maintainer approves it -- a first-time contributor's run, or an " +
		"environment with required reviewers -- and approving it on the pull request's Checks " +
		"tab is what delivers its verdict."
	anyCancelled := len(cancelledChecks(checks)) > 0 || grain.Cancelled
	anyUnfinished := len(unfinishedChecks(checks)) > 0 || grain.Running
	var cause string
	switch {
	case !anyCancelled:
		cause = neverFinished
	case !anyUnfinished:
		cause = cancelled
	default:
		cause = neverFinished + " And " + cancelled
	}
	if len(awaitingApprovalChecks(checks)) == 0 {
		return cause
	}
	if !anyCancelled && !anyUnfinished {
		return awaitingApproval
	}
	return cause + " And " + awaitingApproval
}

// humanDuration renders d for a person reading a comment. Duration's own
// String is exact and unpleasant about round hours ("2h0m0s"), so the
// trailing zero units come off -- carefully, since "1h30m0s" ends in a
// "0m" that is part of the thirty.
func humanDuration(d time.Duration) string {
	s := d.Round(time.Second).String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// healthReasonSuffix restates health without a PullRequestDetail in hand
// -- escalateToUser has already lost the one syncEntry read this cycle by
// the time it knows a fix task closed without success, and re-reading it
// only to name a base branch again is not worth another GitHub call.
func healthReasonSuffix(health model.PrHealth) string {
	if health == model.PrFailing {
		return "checks are still failing"
	}
	return "it's still conflicted"
}
