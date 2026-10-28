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

package forge

import (
	"fmt"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
)

// Observation is everything the forge has to say about one pull request
// at one instant: the pull request as it stands, the checks on its head
// commit, the health computed from the two, and where both of the clocks
// this package runs on a pull request's CI had got to.
//
// It is a value rather than a set of fields filled in on somebody else's
// struct, which is the change that makes this package possible at all:
// what used to be here was `queueEntry.observe`, a method that reached
// into an entry carrying a model.Task and a model.Observation and wrote
// six fields back onto it. Returning instead of mutating is what leaves
// the task out.
type Observation struct {
	// Detail is the pull request as GitHub answered for it.
	Detail github.PullRequestDetail
	// Checks is what CI reported against Detail.HeadSHA, empty for a
	// pull request that is closed (see Observe on why it is not read).
	Checks []github.CheckRun
	// ChecksKnown is false when this deployment's credential could read
	// neither the Checks API nor Actions, which is not the same as
	// nothing being reported -- see CheckRuns, and HealthFrom's own
	// "checks we could not read are not checks that passed".
	ChecksKnown bool
	// ChecksSettled is whether an empty check list has been empty for
	// long enough to mean this repository has no CI rather than that its
	// CI has not registered yet (EmptyChecksSettled).
	ChecksSettled bool
	// Health is the answer from the selected CI provider, together with
	// GitHub's pull-request state and mergeability.
	Health Health
	// GrainCI is what Input.GrainCIFor answered for Detail.HeadSHA,
	// returned so the caller need not resolve the same verdict twice
	// against a head commit it did not know when it called.
	GrainCI GrainCI
	// Stalled is Health == Pending having gone on for longer than the
	// queue is willing to wait, with the deadline it was measured
	// against. Always false when Input.Blocked, since the clock measures
	// how long the queue has been waiting and a pull request it has
	// stopped waiting on is not being waited on.
	Stalled       bool
	StallDeadline time.Duration
	// MissingChecks is the GitHub checks the pull request's merge rules
	// require (Policy.RequiredChecks) that are not on the commit at all.
	MissingChecks []string
}

// Policy is a pull request's own merge rules as far as CI goes
// (model.EffectiveMergePolicy, grain/task-1659): which CI system gates
// it, which GitHub checks must be there, and how long to wait.
type Policy struct {
	// Mode is "grain", "github", "both" or "none", and replaces
	// Input.CIMode and Input.IgnoreGitHubCI for this pull request.
	Mode string
	// RequiredChecks must each be present and green. With AllChecks every
	// other check on the commit gates too, as without a policy; without
	// it only these do.
	RequiredChecks []string
	AllChecks      bool
	// StallTimeout, when set, is this pull request's own CI stall
	// deadline. It can only shorten the deployment's.
	StallTimeout time.Duration
}

// emptyChecksSettler is implemented by a forge adapter that knows an empty
// check list is final. GitHub needs the registration window below because a
// workflow can appear after its branch does; a controller-native forge with no
// check publisher has no such race and should not make every merge wait for an
// event that cannot happen.
type emptyChecksSettler interface {
	EmptyChecksAreSettled(owner, repo string) bool
}

// Input is what Observe needs that it cannot read from the forge itself.
//
// Both fields are facts about a *task*, and their being arguments rather
// than reads is the whole of this package's direction rule: the caller
// looks them up (orchestrator's grainCIVerdictOf and blockedEntry) and
// hands them down, so that nothing here has to know a task exists.
type Input struct {
	// RequiredCheck is the check run this pull request may not be called
	// clean without, and it is empty for all but one repository -- see
	// HealthFrom, and orchestrator's StateRepoGate.
	RequiredCheck string
	// GrainCIFor answers what grain's own CI made of one commit, read off
	// the task's own record by the caller.
	//
	// A callback rather than a value because of an ordering the two
	// halves cannot avoid: the verdict is about the head commit, and the
	// head commit is what the read this function performs is for. A
	// caller that wanted to pass a value would have to read the pull
	// request itself first, which is the one call this whole path exists
	// to make once.
	//
	// So the forge asks, at the moment it has a commit to ask about. It
	// is the same shape gitproxy.Authorizer is, and for the same reason:
	// a question whose answer lives on the other side of the boundary is
	// asked across it rather than fetched through it.
	//
	// nil is a deployment with no grain CI, and reads as the zero
	// verdict -- which WithGrainCI treats as silence, never as failure.
	GrainCIFor func(headSHA string) GrainCI
	// Blocked is whether the queue has given up on this pull request
	// (orchestrator's Observation.MergeQueueBlockedAt). It stops the
	// stall clock, and only that.
	Blocked bool
	// Verdicts, when set, is somewhere other than the forge that may
	// already know how this commit's checks ended -- and is consulted
	// before the forge is asked (verdicts.go). nil is every deployment
	// that has no such service, and reads exactly as this package did
	// before the seam existed.
	Verdicts Verdicts
	// IgnoreGitHubCI says this repository has declared that GitHub's
	// check runs are not a source of verdicts for it: grain CI
	// (.grain/workflows) is its gate, and GitHub's checks are neither
	// read nor waited on (model.RepoConfig.IgnoreGitHubCI, and
	// model.Config.IgnoreGitHubCI for a whole deployment in that state).
	IgnoreGitHubCI bool
	// CIMode is the single-select CI mode configured for this repository:
	// "grain", "github", or "none".
	CIMode string
	// PolicyFor, when set, answers the pull request's own merge rules for
	// its head commit, or nil where it has none -- asked the moment the
	// head is known, for GrainCIFor's reason.
	PolicyFor func(headSHA string) *Policy
}

// Observe reads one pull request and answers with everything the forge
// knows about it.
//
// It is the read half of what was orchestrator's syncEntry, and it is
// deliberately still called on the entry's own turn rather than for
// every entry up front: a pull request's head branch can move during a
// cycle (a review or a fix task merging back into it is exactly that),
// and a verdict read before that push describes a commit that is gone by
// the time anything acts on it.
//
// It writes nothing anywhere. The two clocks it advances -- the
// registration window and the stall deadline -- are in-process maps
// belonging to this package (sightings), not rows, which is what lets
// this be called without a store.
func Observe(client github.Client, ref Ref, in Input, now time.Time) (Observation, error) {
	detail, err := client.GetPullRequest(ref.Owner, ref.Repo, ref.Number)
	if err != nil {
		return Observation{}, recordFailedRead(ref, err, now,
			fmt.Errorf("forge: reading %s: %w", ref, err))
	}

	// A closed PR's head branch is routinely gone by the time this runs --
	// GitHub deletes it on merge when a repo has that setting on, and
	// nothing stops a human deleting it after closing without merging
	// either. HealthFrom's first line already answers Closed off
	// detail.State alone with no need for checks, so skip CheckRuns
	// entirely here rather than have a 404 on a ref that no longer
	// resolves turn a task that is plainly done into one the caller can
	// never close: this same task keeps recurring in the caller's list
	// every cycle, so an error here would repeat forever, not just once.
	var policy *Policy
	if in.PolicyFor != nil {
		policy = in.PolicyFor(detail.HeadSHA)
	}
	if policy != nil {
		in.CIMode, in.IgnoreGitHubCI = policy.Mode, false
	}
	var checks []github.CheckRun
	checksKnown := true
	checksSettled := true
	switch {
	case in.IgnoreGitHubCI || in.CIMode == "grain" || in.CIMode == "none":
		// Not read, rather than read and then disregarded. Three things
		// follow from making it an absent read instead of an ignored
		// answer, and all three are the point of the setting:
		//
		//   - a Checks API that cannot answer -- a credential without
		//     the scope, an Actions installation that is off -- no
		//     longer makes this pull request unreadable, when its
		//     verdict never depended on that read;
		//   - Observation.Checks is empty, so every sentence built from
		//     it downstream (healthReason, stalledChecksPhrase, the
		//     queue row) has nothing to list and says why instead;
		//   - neither of this package's two clocks on GitHub's CI can
		//     start, because neither has anything to time.
		//
		// The empty-checks sighting is dropped rather than left lying
		// around, on the same discipline the rest of this function
		// keeps: a repository that turns this on mid-flight must not
		// keep a window open on a read nobody is making any more.
		ForgetEmptyChecks(ref)
	case detail.State != "closed":
		checks, checksKnown, err = CheckRuns(client, in.Verdicts, ref, detail.HeadRef, detail.HeadSHA)
		if err != nil {
			return Observation{}, recordFailedRead(ref, err, now, err)
		}
		// Only an empty list needs the window, and asking only for one
		// is what keeps the sighting map to pull requests that are
		// actually in that state (EmptyChecksSettled starts a commit's
		// window on the first call for it).
		//
		// A list that is *not* empty drops the sighting, the same way
		// ForgetPendingChecks below drops the other clock the moment its
		// own state stops holding: checks have registered, so there is
		// nothing left to time, and a sighting left lying around is one
		// that could outlive the state it was timing.
		if checksKnown {
			if len(checks) == 0 {
				settler, definitive := client.(emptyChecksSettler)
				if definitive && settler.EmptyChecksAreSettled(ref.Owner, ref.Repo) {
					checksSettled = true
					ForgetEmptyChecks(ref)
				} else {
					checksSettled = EmptyChecksSettled(ref, detail.HeadSHA, now)
				}
			} else {
				ForgetEmptyChecks(ref)
			}
		}
	}
	// grain CI's half of the same question, about the same commit. It
	// needs no read of the forge: the verdict was copied onto the task
	// when the run concluded, precisely so that reading a task is reading
	// the answer -- and reading a task is the caller's job, not this
	// package's.
	var grain GrainCI
	if in.GrainCIFor != nil {
		grain = in.GrainCIFor(detail.HeadSHA)
	}
	// A repository whose gate is grain CI is gated on grain CI, which is
	// not the same thing as being gated on nothing. Required here is the
	// whole of what keeps the setting from being a way to merge with no
	// verdict at all: with GitHub's checks unread, HealthFrom would
	// otherwise see an empty settled list and read Clean.
	//
	// Required by the repository's declaration rather than by the task's
	// grant, which is the one case where those two come apart. A task
	// that does not hold grain-ci in a repository that has declared grain
	// CI its gate has no verdict for this commit and never will, so its
	// pull request sits Pending until the stall deadline hands it to a
	// person -- loudly, and closed, rather than merging quietly.
	if in.CIMode == "none" {
		grain.Required = false
	} else if in.IgnoreGitHubCI || in.CIMode == "grain" || in.CIMode == "both" {
		grain.Required = true
	}
	var health Health
	var missing []string
	if policy == nil {
		health = HealthOf(detail, checks, in.RequiredCheck, checksKnown, checksSettled, grain)
	} else {
		health, missing = policyHealth(detail, checks, in.RequiredCheck, checksKnown, checksSettled, grain, *policy)
	}

	// The clock on PENDING, kept for every entry rather than only for the
	// one at the head of its queue: a pull request whose checks hang has
	// been hanging for as long as it has been hanging, whether or not
	// something ahead of it happened to be occupying the queue in the
	// meantime. Starting it only on promotion would make a repo with
	// three stalled pull requests take three deadlines to clear rather
	// than one.
	//
	// Not for one the queue has given up on, though (Input.Blocked): the
	// clock measures how long the queue has been *waiting* on CI, and a
	// pull request it has stopped waiting on is not being waited on. It
	// is dropped rather than merely ignored so that a human's own "try
	// again" (ui.Client.RetryMerge) gets a whole deadline to be answered
	// in: an entry unblocked after a stall would otherwise come back with
	// a clock still reading hours, and the very next cycle would give up
	// on it again before anyone could see it try.
	stalled, stallDeadline := false, time.Duration(0)
	if health == Pending && !in.Blocked {
		var timeout time.Duration
		if policy != nil {
			timeout = policy.StallTimeout
		}
		stalled, stallDeadline = ChecksStalledWithin(ref, detail.HeadSHA, now, timeout)
	} else {
		// Any other answer -- clean, failing, conflicted, unknown, closed
		// -- is CI having reported, or having stopped being the question,
		// so whatever pending run was being timed is over. A later
		// PENDING on this same commit (a re-run of a check, hours after
		// the first round finished) is timed from itself.
		ForgetPendingChecks(ref)
	}

	// Both reads answered, so whatever run of failed reads was being
	// timed is over -- the same discipline the two clocks above keep, for
	// the same reason: a run that is not forgotten the moment its own
	// state stops holding is one that outlives the state it was timing,
	// and a flake a day either side of a working week would otherwise
	// add up to a fault nobody has.
	ForgetReadFailures(ref)

	return Observation{
		Detail:        detail,
		Checks:        checks,
		ChecksKnown:   checksKnown,
		ChecksSettled: checksSettled,
		Health:        health,
		GrainCI:       grain,
		Stalled:       stalled,
		StallDeadline: stallDeadline,
		MissingChecks: missing,
	}, nil
}

// policyHealth is HealthOf under a pull request's own merge rules.
//
// GitHub's half is its checks -- all of them, or only the ones the rules
// name -- with a named check that is not on the commit at all read as
// Pending: a check that never registers is a stall for a person, never a
// pass. grain's half is WithGrainCI's, and under mode both (or github,
// for a change whose grain CI is required by its grant) the two are
// combined strictest-first: a fact about the pull request itself, then
// failing, then pending, and clean only where both are.
func policyHealth(detail github.PullRequestDetail, checks []github.CheckRun, requiredCheck string,
	checksKnown, checksSettled bool, grain GrainCI, p Policy) (Health, []string) {

	if p.Mode != "github" && p.Mode != "both" {
		return HealthOf(detail, checks, requiredCheck, checksKnown, checksSettled, grain), nil
	}
	gated := checks
	if !p.AllChecks {
		gated = nil
		for _, c := range checks {
			for _, n := range p.RequiredChecks {
				if c.Name == n {
					gated = append(gated, c)
					break
				}
			}
		}
	}
	if grain.Passed {
		gated = FilterActionsBudgetFailures(gated)
	}
	var missing []string
	for _, n := range p.RequiredChecks {
		if !hasCheck(checks, n) {
			missing = append(missing, n)
		}
	}
	gh := HealthFrom(detail, gated, requiredCheck, checksKnown, checksSettled || len(p.RequiredChecks) > 0)
	if gh == Clean && len(missing) > 0 {
		gh = Pending
	}
	if p.Mode == "github" && !grain.Required {
		return gh, missing
	}
	// Under mode both, and under mode github where grain CI is required
	// anyway -- the task holds grain-ci, which without rules makes its
	// verdict the gate (HealthOf) -- grain's half gates too: a rule adds
	// a CI system, it never takes away the one the change already had.
	g := WithGrainCI(HealthFrom(detail, nil, "", true, true), grain)
	switch {
	case gh == Conflicted || gh == Unknown || gh == Merged || gh == Closed:
		return gh, missing
	case g == Conflicted || g == Unknown || g == Merged || g == Closed:
		return g, missing
	case gh == Failing || g == Failing:
		return Failing, missing
	case gh == Pending || g == Pending:
		return Pending, missing
	}
	return gh, missing
}

// recordFailedRead notes that one of Observe's reads did not answer and
// hands back the error the caller was going to return anyway, so that a
// failing read is one statement at the call site rather than three.
//
// It is here rather than at Observe's caller because this is where the
// reads are, and because the record has to be kept for *every* way the
// read can fail to be a run of consecutive failures at all: the pull
// request endpoint and the check-runs endpoint are one question as far
// as "can grain read this pull request" goes, and this deployment's log
// has 5xx from both.
func recordFailedRead(ref Ref, cause error, now time.Time, out error) error {
	RecordReadFailure(ref, cause, now)
	return out
}
