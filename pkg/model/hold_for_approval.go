package model

import (
	"fmt"
	"slices"
	"strings"
)

// HeldForApprovalTag marks a proposal whose proposer's own permission
// would have filed it approved (AutoSubmits, ConsoleSession.TasksApproved)
// and whose propose_task call asked for it to wait for a person anyway
// (hold_for_approval).
//
// Auto-approval is a ceiling a run may stay under, not a decision taken
// out of its hands: the director, holding a session's permission to
// queue its own work, had no way to say "this one should wait for the
// operator" (grain/task-439). A held proposal is filed exactly as one the
// permission did not cover, and the tag is what lets the row say the
// difference -- that a person is being asked because the agent chose to
// ask, not because it could not do otherwise. A proposal held by a run
// whose permission covered nothing carries no tag: it would have waited
// anyway, and saying it was held would be claiming a choice nobody made.
//
// Per call and never inherited: the proposals a held task files in turn
// are decided by the ordinary rules, on that task's own permission.
const HeldForApprovalTag = "held-for-approval"

// HeldForApproval reports whether t carries HeldForApprovalTag.
func HeldForApproval(t Task) bool {
	return slices.Contains(t.Tags, HeldForApprovalTag)
}

// HeldForApprovalNote is the line appended to a held proposal's body, in
// the voice of AutoSubmitLimitNote and for its reason: the person
// approving it should know a decision was made, and by whom. "" for a
// proposal the permission would not have queued anyway.
//
// Here rather than in either package that files a proposal, so the
// end-of-run relay (orchestrator.relayProposedTasks) and the live call
// (ui.CreateTask) say it the same way.
func HeldForApprovalNote(proposer string) string {
	return fmt.Sprintf("\n\n---\n\ngrain: task %s, which proposed this, could have queued it "+
		"on its own permission and asked for it to wait for a person instead "+
		"(propose_task's hold_for_approval). Approve it if it should go ahead.", proposer)
}

// AutoApprovalRefusal is why neither permission that reaches the queue
// without a click covers child, a proposal parent filed: phrased to
// finish the sentence "filed unapproved: your permission does not cover
// ..." in what propose_task tells the run. sessionQueues is whether
// parent is a standing session its deployment lets queue its own work.
//
// "" means the permission does cover it -- whether it was then filed
// approved is AutoSubmitLimits' question, which this does not ask, since
// it needs the store and has its own note (AutoSubmitLimitNote).
func AutoApprovalRefusal(child, parent Task, sessionQueues bool) string {
	if escalated := EscalatedCapabilities(child, parent); len(escalated) > 0 {
		return fmt.Sprintf("it asks for %s, which your task does not hold",
			"`"+strings.Join(escalated, "`, `")+"`")
	}
	if writes := UngrantedWrites(child, parent); len(writes) > 0 {
		names := make([]string, 0, len(writes))
		for _, w := range writes {
			names = append(names, w.Repo.String())
		}
		return fmt.Sprintf("it may land work in %s, which your task was never given", strings.Join(names, ", "))
	}
	if reads := UngrantedReads(child, parent); len(reads) > 0 {
		names := make([]string, 0, len(reads))
		for _, r := range reads {
			names = append(names, r.String())
		}
		return fmt.Sprintf("it reads %s, which your task cannot read", strings.Join(names, ", "))
	}
	if sessionQueues || AutoSubmits(child, parent) {
		return ""
	}
	if !parent.AutoMerge && !parent.AutoApproveProposals {
		return "your task is not an auto-merge job and is not a standing session allowed to queue its own work"
	}
	return "it is not work of the shape your task may queue unreviewed: the same branch your " +
		"task lands on, holding no capability your task lacks"
}
