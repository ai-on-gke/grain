package model

// How a run ended, as a vocabulary rather than as a sentence.
//
// task_run.outcome is deliberately open (schema.go keeps enums out of the
// DDL) and deliberately coarse, and the coarseness hides exactly the
// distinctions worth acting on. "cancelled" is both "a human closed the
// task while its run was live" -- routine, nobody's fault, nothing to fix
// -- and "the run hit its wall-clock cap", which is a run whose work was
// cut off mid-flight. "failed" is both "the agent CLI died" and "the run
// exhausted its turn budget without ever producing a final answer". Each
// of those has a different fix, and counting task_run.outcome strings
// cannot tell any of them apart.
//
// The distinction is not stored, because it does not have to be: it is
// already in task_run.detail, which the paths below write. What was
// missing was a way to read that detail back as a fact rather than as
// prose, which is what EndingOf is -- the reader half of the sentences
// this file also owns the writing of.
//
// Owning both halves here is the point. The phrases orchestrator records
// and the phrases a report matches on used to be able to drift apart
// silently, at the cost of a metric that quietly reads zero forever; now
// there is one definition of each, in the package that owns the column
// they land in, with ending_test.go asserting the round trip.

import (
	"fmt"
	"strings"
	"time"
)

// RunEnding is how a finished run actually ended -- what a human would
// say happened, rather than which of a handful of outcome strings the
// finish path reached for.
type RunEnding string

const (
	// EndingSucceeded is a run that got as far as its tools and produced
	// something (ProcessResult has already downgraded the ones that did
	// not to EndingNoAction).
	EndingSucceeded RunEnding = "succeeded"
	// EndingNoAction is a run that had tools, used them, and produced
	// nothing: no branch pushed, no question asked, no closing comment.
	// It is the purest measure of the agent-facing surface failing, which
	// is why it is its own series rather than a key beside "succeeded".
	EndingNoAction RunEnding = "no_action"
	// EndingNothingNeeded is a run that looked and said, through the
	// nothing_needed tool, that the task needs no change and no report
	// anybody has to act on. A success of its own kind -- the run did
	// what it was dispatched to -- kept apart from EndingSucceeded so a
	// deployment can see how much of its queue turned out to be no-ops,
	// and from EndingNoAction because, unlike that one, it said so.
	EndingNothingNeeded RunEnding = "nothing_needed"
	// EndingParked is a run that parked its task on other tasks through
	// wait_for_task (ParkedOutcome): it did what it meant to, and the
	// task resumes once they close.
	EndingParked RunEnding = "parked"
	// EndingRuntimeCap is a run cancelled by Config.MaxRunRuntime -- the
	// wall-clock wall, not a human and not a fault of the run's.
	EndingRuntimeCap RunEnding = "runtime_cap"
	// EndingTurnsExhausted is a run that used every turn MaxAgentTurns
	// allowed it without producing a final answer.
	EndingTurnsExhausted RunEnding = "turns_exhausted"
	// EndingUsageLimit is a run stopped by the agent provider's own usage
	// limit -- either the run that found the wall or one cancelled by the
	// pause it began. See PausedOutcome.
	EndingUsageLimit RunEnding = "usage_limit"
	// EndingCapacity is a run the agent's provider refused for want of
	// capacity -- overloaded, unavailable, a gateway that gave up. Its
	// own word rather than a shade of EndingFailed because it is the one
	// failure an operator should read as weather rather than as a fault:
	// see CapacityOutcome, which no failure streak counts.
	EndingCapacity RunEnding = "capacity"
	// EndingInterrupted is a run cut off because the daemon driving it
	// was stopped -- an upgrade or a restart -- whether it was finished
	// by that daemon's own drain or recovered by the next one at
	// startup. Its own word for EndingCapacity's reason: it is the
	// deployment's doing rather than the task's, which is why no failure
	// streak counts InterruptedOutcome either.
	EndingInterrupted RunEnding = "interrupted"
	// EndingOOM is a run whose sandbox the kernel OOM-killed under memory
	// overcommit. The deployment's doing rather than the task's; see
	// OOMOutcome.
	EndingOOM RunEnding = "oom"
	// EndingEnvironment is a run that could not start a tool it needed
	// because the host running it needs a human -- expired host
	// credentials, and the command that renews them. The deployment's doing rather than the
	// task's, for the same reason; see EnvironmentOutcome.
	EndingEnvironment RunEnding = "environment"
	// EndingTaskClosed is a run cancelled because a human closed its task
	// underneath it. The other half of "cancelled", and the half nothing
	// needs to fix.
	EndingTaskClosed RunEnding = "task_closed"
	// EndingTurnStopped is a run cancelled because a user stopped the turn
	// mid-flight.
	EndingTurnStopped RunEnding = "turn_stopped"
	// EndingSetupFailed is a run that never reached its agent at all: a
	// checkout that would not clone, a capability that would not mint, a
	// sandbox that would not build.
	EndingSetupFailed RunEnding = "setup_failed"
	// EndingFailed is every other failure -- the agent CLI dying, its MCP
	// connection dropping, a finish path that could not complete.
	EndingFailed RunEnding = "failed"
	// EndingCancelled is a cancellation whose detail names neither the
	// wall-clock cap nor a closed task -- either of the two, with nothing
	// recorded to say which. It exists so that neither of those two
	// series is inflated by a run that might not belong to it.
	EndingCancelled RunEnding = "cancelled"
	// EndingUnrecorded is a finished run with no outcome recorded at all,
	// given a word of its own rather than counting under "".
	EndingUnrecorded RunEnding = "unrecorded"
)

// The sentences task_run.detail carries for the endings that are only
// distinguishable by it, and the substrings EndingOf reads them back by.
//
// A marker is a fragment rather than the whole sentence because detail is
// composed: orchestrator appends what the run got done before it ended
// (partialWorkSuffix), and a framework's own error text precedes the turn
// budget's. Matching a fragment that no other ending's sentence contains
// is what survives that composition.
// NothingNeededOutcome is the task_run.outcome a run that ended on the
// nothing_needed tool records (orchestrator.ProcessResult), and the
// word EndingOf reads back as EndingNothingNeeded.
const NothingNeededOutcome = "nothing_needed"

const (
	// RuntimeCapMarker is in every sentence RuntimeCapDetail builds.
	RuntimeCapMarker = "wall-clock limit"
	// TaskClosedDetail is the whole sentence, since it takes no
	// parameters, and doubles as its own marker.
	TaskClosedDetail = "the task was closed while this run was still live"
	// TurnStoppedMarker is in every sentence TurnStoppedDetail builds.
	TurnStoppedMarker = "stopped by user"
	// TurnStoppedDetail is the whole sentence recorded when a turn is interrupted.
	TurnStoppedDetail = "turn stopped by user"
	// TurnStoppedComment is the comment a person stopping a turn posts on
	// the task (ui.Client.Stop), which the task is then parked on.
	TurnStoppedComment = "Turn stopped by user"
	// TurnsExhaustedMarker is the phrase every agent.Framework uses when
	// a run runs out of turns without a final answer -- see
	// agent.MaxTurnsExceeded, which is the one place that sentence is
	// built, and pkg/agent's own test that this classifies it.
	TurnsExhaustedMarker = "exceeded max turns"
)

// RuntimeCapDetail is what a run cancelled by the wall-clock cap records
// in task_run.detail: the limit it ran past, named, because a human
// reading the attempt wants to know whether the run was close or nowhere
// near.
func RuntimeCapDetail(limit time.Duration) string {
	return fmt.Sprintf("the run exceeded its %s %s", limit, RuntimeCapMarker)
}

// SucceededOutcome reports whether a task_run.outcome is a run that did
// what it was dispatched to: "succeeded", or NothingNeededOutcome -- a run
// that looked and said nothing was needed, which ends a failure streak
// exactly as a success does.
func SucceededOutcome(outcome string) bool {
	return outcome == "succeeded" || outcome == NothingNeededOutcome || outcome == ParkedOutcome
}

// EndingOf reads a finished run's recorded outcome and detail back as the
// ending they describe.
//
// Detail is consulted only where the outcome is genuinely ambiguous, and
// only for markers this package also writes (or, for the turn budget, one
// agent.MaxTurnsExceeded owns and pkg/agent pins). An outcome this build
// does not know -- the vocabulary is open, and "orphaned" and
// "finish-failed" are both real -- reads as EndingFailed rather than as
// a guess: those are all runs that ended without producing what they were
// dispatched for, and a report that wants the exact word still has
// task_run.outcome itself (metrics.Runs.Outcomes counts them verbatim).
func EndingOf(outcome, detail string) RunEnding {
	switch outcome {
	case "":
		return EndingUnrecorded
	case "succeeded":
		return EndingSucceeded
	case "no_action":
		return EndingNoAction
	case NothingNeededOutcome:
		return EndingNothingNeeded
	case ParkedOutcome:
		return EndingParked
	case PausedOutcome:
		return EndingUsageLimit
	case CapacityOutcome:
		return EndingCapacity
	case InterruptedOutcome:
		return EndingInterrupted
	case EnvironmentOutcome:
		return EndingEnvironment
	case OOMOutcome:
		return EndingOOM
	case "setup-failed":
		return EndingSetupFailed
	case "cancelled":
		switch {
		case strings.Contains(detail, RuntimeCapMarker):
			return EndingRuntimeCap
		case strings.Contains(detail, TaskClosedDetail):
			return EndingTaskClosed
		case strings.Contains(detail, TurnStoppedMarker):
			return EndingTurnStopped
		}
		// A cancellation with neither marker is one of the two, and
		// nothing here can say which -- so it counts as neither rather
		// than inflating one of them.
		return EndingCancelled
	default:
		if strings.Contains(detail, TurnsExhaustedMarker) {
			return EndingTurnsExhausted
		}
		return EndingFailed
	}
}
