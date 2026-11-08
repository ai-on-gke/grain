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

// HoldRequestedTag marks a proposal whose propose_task call asked for
// hold_for_approval, whether or not its proposer's permission would have
// queued it. HeldForApprovalTag is deliberately narrower -- it says a
// choice was made, so a proposal from a run whose permission covered
// nothing does not carry it -- but the request is the proposer saying
// "a person decides this", and that is what the agent approval rail
// (ui.agentApprovalRail) has to honour: without this tag an agent holding
// api:tasks:admin or dispatch:write could approve such a proposal for the
// person it was left for (docs/test-runs/2026-11-07-director-agent-api.md,
// finding 2). Per call and never inherited, like HeldForApprovalTag.
const HoldRequestedTag = "hold-requested"

// HoldRequested reports whether t's proposer asked for it to wait for a
// person (propose_task's hold_for_approval): t carries HoldRequestedTag,
// or HeldForApprovalTag from before HoldRequestedTag was recorded.
func HoldRequested(t Task) bool {
	return HeldForApproval(t) || slices.Contains(t.Tags, HoldRequestedTag)
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
//
// seeding is where child's default grants came from when it was filed
// (ProposalSeeding): a director proposal's defaults seeded unchanged
// from its own target repository or the deployment are not escalations
// (ProposalEscalations, director-capabilities.spec.md §4.2). nil
// measures every default reference as EscalatedCapabilities does.
//
// A proposal that would land work in this deployment's configuration
// repository (ConfigRepoProposalHold) is refused first and whatever
// parent holds: see that function for why.
func AutoApprovalRefusal(child, parent Task, sessionQueues bool, seeding *ProposalSeeding) string {
	if why := ConfigRepoProposalHold(child); why != "" {
		return why
	}
	if escalated, _ := ProposalEscalations(child, parent, sessionQueues, seeding); len(escalated) > 0 {
		asked, seeded := SeededEscalations(child, escalated)
		// A grant only a person may give is escalated whether or not
		// parent holds it (CapabilityEscalationNote), so it is not said
		// to be one parent lacks.
		var lacked, personal []string
		for _, c := range asked {
			if IsPersonOnlyGrant(c) {
				personal = append(personal, c)
			} else {
				lacked = append(lacked, c)
			}
		}
		var why []string
		if len(lacked) > 0 {
			why = append(why, fmt.Sprintf("it asks for %s, which your task does not hold",
				"`"+strings.Join(lacked, "`, `")+"`"))
		}
		if len(personal) > 0 {
			why = append(why, fmt.Sprintf("it asks for %s, which only a person may grant, "+
				"whether or not your task holds it", "`"+strings.Join(personal, "`, `")+"`"))
		}
		if len(seeded) > 0 {
			why = append(why, fmt.Sprintf("it is seeded with this deployment's default capability %s, "+
				"which your task does not hold -- you did not ask for it, and a person can approve "+
				"or remove it", "`"+strings.Join(seeded, "`, `")+"`"))
		}
		return strings.Join(why, "; and ")
	}
	if adm := NormalizeAdmission(child.Admission); adm.AtLeast(AdmissionBypass) {
		return fmt.Sprintf("it sets admission to `%s`, which only a person may give a task", adm)
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

// ConfigRepoProposalHold is why child, a proposal, must wait for a
// person whatever its proposer's permission: it targets this
// deployment's configuration repository (IsConfigRepo), or lists it
// among its writes. Phrased, like AutoApprovalRefusal, to finish "filed
// unapproved: your permission does not cover ..."; "" when it names the
// repository nowhere.
//
// Unconditional, and so asked before anything the proposer holds.
// grain/config holds this deployment's capability grants, schedules and
// secrets configuration, and the one sanctioned way a run changes it is
// propose_config_change: a candidate branch under
// ConfigProposalBranchPrefix, validated, classified by the trust
// boundary and landed only on a person's approval of its pinned head.
// That tool is keyed on the run holding grain/config in its Writes, and
// UngrantedWrites admits any child write its parent holds -- so without
// this, the director given propose_config_change would also have been
// given a way to queue, on its own permission (TasksApproved), a child
// task that pushes to grain/config with nobody looking at the diff
// first (director-capabilities spec §3.1.3, gap G5). The proposal is
// still filed as asked; a person decides whether it goes ahead.
//
// Here rather than in either package that files a proposal, so the
// end-of-run relay (orchestrator.relayProposedTasks) and the live call
// (ui.CreateTask) hold it the same way.
func ConfigRepoProposalHold(child Task) string {
	targets := child.Target != nil && IsConfigRepo(*child.Target)
	writes := slices.ContainsFunc(child.Writes, func(w WriteTarget) bool { return IsConfigRepo(w.Repo) })
	var what string
	switch {
	case targets && writes:
		what = "it targets and writes"
	case targets:
		what = "it targets"
	case writes:
		what = "it writes"
	default:
		return ""
	}
	return fmt.Sprintf("%s %s, this deployment's configuration repository, and a proposed task that "+
		"may land work there always waits for a person (propose a configuration change with "+
		"propose_config_change instead)", what, ConfigRepo())
}

// PersonOnlyProposalSettings is why child, a proposal parent filed, asks
// for something only a person may give it, phrased to finish "filed for
// a person to approve: ..." -- "" when it asks for nothing of the kind.
//
// Two settings a person may give any task they file, and a run may not
// give the work it files on its own permission:
//
//   - Admission above standard (`bypass` or `durable`), which lets the
//     task bypass queue limits or run without a wall-clock deadline.
//   - KeepRunning, which requeues the task every time its run exits and so
//     holds a sandbox for as long as the deployment runs. Filing one is a
//     person's decision just like editing an existing task.
//   - A Priority above both its proposer's own and DefaultTaskPriority,
//     which sorts it ahead of base-branch repairs and of the work people
//     put at the front of the queue (Store's taskQueueOrderSQL). Filing at
//     the default is not an escalation even below a low-priority proposer:
//     it is what every proposal that names no priority gets.
//
// Neither is refused: the run may know its follow-up should run forever
// or jump the queue, and a person can agree. Both keep the proposal from
// being queued on its proposer's permission, so somebody does.
//
// Here rather than in either package that files a proposal, so the
// end-of-run relay (orchestrator.relayProposedTasks) and the live call
// (ui.CreateTask) apply it the same way.
func PersonOnlyProposalSettings(child, parent Task) string {
	var why []string
	if adm := NormalizeAdmission(child.Admission); adm.AtLeast(AdmissionBypass) {
		why = append(why, fmt.Sprintf("it sets admission to `%s`, which only a person may give a task", adm))
	}
	if child.KeepRunning {
		why = append(why, "it asks to keep running (keep_running), restarting every time its run exits, "+
			"which only a person may ask of a task")
	}
	if ceiling := max(parent.Priority, DefaultTaskPriority); child.Priority > ceiling {
		why = append(why, fmt.Sprintf("it asks for priority %d, above the %d its proposer may give it "+
			"(the higher of its proposer's own priority and the default), which would dispatch it ahead "+
			"of work a person queued", child.Priority, ceiling))
	}
	return strings.Join(why, "; and ")
}
