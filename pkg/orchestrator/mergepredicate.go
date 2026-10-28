package orchestrator

import (
	"context"
	"fmt"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// The merge predicate (grain/task-471, 1023): the whole of "may this change
// merge into that branch right now", in one place, short enough to hold
// in your head.
//
// # The predicate
//
// A change may merge into branch B iff it merges cleanly into B and has a
// green CI verdict on its own head (or an operator has recorded a force
// merge for that exact head commit).
//
// Merge batching (the gather window, the batch lock on the branch, and
// bisection) was removed in grain/task-1023: a change green on its own head
// that merges cleanly and passes the overlap rule (mergerecheck.go) merges
// immediately; one that overlaps what has landed since its tested tree runs
// a single re-check on the merge-queue ref and merges on green.

// mergeArm is which arm of the predicate a change merges under, and it is
// carried into the log lines and the rows so that "why did this land?" has
// an answer that is not a re-derivation.
type mergeArm string

const (
	// mergeArmOwnHead: this change is green on its own head.
	mergeArmOwnHead mergeArm = "own head"
	// mergeArmForced: an operator said so, with a reason, and grain
	// performed the merge rather than watching somebody do it underneath
	// it. The recorded exception.
	mergeArmForced mergeArm = "forced"
)

// mergeRequest is everything the predicate reads.
type mergeRequest struct {
	// taskID is the change asking.
	taskID string
	// branch is B, the branch it wants to merge into.
	branch string
	// clean is GitHub's answer to "does this merge into B without a
	// conflict". Not "is it up to date": a branch behind its base merges
	// as it stands (grain/task-198).
	clean bool
	// greenHead is whether the required suite has passed for this
	// change's own head commit -- what grainCIVerdictOf answers, about
	// one exact commit.
	greenHead bool
	// forced is the operator's override, nil in the ordinary case.
	forced *forcedMerge
	// head is the pull request's head commit as this cycle read it -- the
	// commit the merge below is pinned to. Only the forced arm reads it:
	// an override authorises the commit the operator reviewed and no
	// other (forcedMerge.headSHA).
	head string
}

// forcedMerge is an operator's recorded override: the one sanctioned
// exception to the predicate.
type forcedMerge struct {
	// reason is what the operator typed. Never empty: a request without
	// one is not a force merge (forcedMergeOf refuses it).
	reason string
	// by is who asked.
	by string
	// skipped is what was not green at the time, in words, so the record
	// says what the override skipped rather than only that there was one.
	skipped string
	// headSHA is the commit the operator was looking at when they asked
	// (Observation.ForceMergeHeadSHA). The override is for that commit:
	// a push after it is code nobody reviewed, and merging it on the
	// strength of an override given before it existed is the hole
	// bwsalmon/grain#2399 describes.
	headSHA string
}

// movedSince is whether the branch has a head other than the one the
// override was given for -- including a stamp that names no head at all,
// which was written before stamps carried one and so cannot say which
// commit anybody reviewed.
func (f forcedMerge) movedSince(head string) bool {
	return f.headSHA == "" || head == "" || f.headSHA != head
}

// forcedMergeMovedWhy is the refusal for an override whose branch has
// moved, in the words the row and the task's note both use.
func forcedMergeMovedWhy(f forcedMerge, head string) string {
	if f.headSHA == "" {
		return "the force merge does not record which commit was reviewed, so it authorises " +
			"nothing: re-review the pull request and force again"
	}
	return fmt.Sprintf("the branch has moved since the force merge: it was asked for %s and the "+
		"head is now %s, which nobody has reviewed -- re-review and force again",
		shortSHA(f.headSHA), shortSHA(head))
}

// mergeDecision is the predicate's answer: whether the change may merge,
// under which arm, and -- when it may not -- the reason in the words a
// person reading the row would use.
type mergeDecision struct {
	ok  bool
	arm mergeArm
	why string
}

// String renders the decision for a test failure or a log line.
func (d mergeDecision) String() string {
	if d.ok {
		return fmt.Sprintf("eligible (%s)", d.arm)
	}
	return "not eligible: " + d.why
}

// mayMerge is the predicate. Nothing else in this package may decide that
// a change is eligible to merge, and nothing in here may consult
// anything that is not in mergeRequest.
func mayMerge(r mergeRequest) mergeDecision {
	if r.forced != nil && r.forced.reason != "" {
		if r.forced.movedSince(r.head) {
			return mergeDecision{why: forcedMergeMovedWhy(*r.forced, r.head)}
		}
		if !r.clean {
			return mergeDecision{why: "it does not merge cleanly into " + r.branch +
				", which no override can change: the conflict has to be resolved on the branch"}
		}
		return mergeDecision{ok: true, arm: mergeArmForced,
			why: "forced by " + r.forced.by + ": " + r.forced.reason}
	}
	if !r.clean {
		return mergeDecision{why: "it does not merge cleanly into " + r.branch}
	}
	if !r.greenHead {
		return mergeDecision{why: "grain CI has not passed for this change's own head"}
	}
	return mergeDecision{ok: true, arm: mergeArmOwnHead}
}

// mergeDecisionFor asks the predicate about one queue entry.
func mergeDecisionFor(_ context.Context, _ *model.Store, e queueEntry,
	detail github.PullRequestDetail, health model.PrHealth, forced *forcedMerge) (mergeDecision, error) {

	return mayMerge(mergeRequest{
		taskID:    e.task.ID,
		branch:    detail.BaseRef,
		clean:     health != model.PrConflicted,
		greenHead: health == model.PrClean || e.ciMode == model.CIModeNone,
		forced:    forced,
		head:      detail.HeadSHA,
	}), nil
}
