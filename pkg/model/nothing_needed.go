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

import "fmt"

// NothingNeededAction is what grain does with a task whose run ended on
// the nothing_needed tool -- an agent saying that, having looked, the task
// needs no change and no report anybody has to act on.
//
// Before the tool existed the only way to say that was comment_on_issue,
// which completes the task into awaiting_reply beside every investigation
// whose report somebody has to read, so deliberate no-ops piled up in the
// operator's inbox with nothing telling them apart. Closing is therefore
// the default; a task whose "nothing to do" still wants a human's eye
// says NothingNeededAck.
type NothingNeededAction string

const (
	// NothingNeededClose closes the task outright: Observation.ClosedAt
	// is stamped and it never shows in awaiting_reply. The empty string
	// means this too.
	NothingNeededClose NothingNeededAction = "close"
	// NothingNeededAck completes the task the way a closing comment
	// does, leaving it in awaiting_reply until somebody acknowledges it.
	NothingNeededAck NothingNeededAction = "ack"
)

// NothingNeededClosedNote is the sentence posted on a task
// NothingNeededClose closed, after the agent's own reason.
const NothingNeededClosedNote = "Closed: the agent found nothing needed."

// ClosesOnAnswer reports whether a run of t that ends on a closing note
// alone -- comment_on_issue, no pull request, no question -- closes t
// rather than completing it into awaiting_reply.
//
// That is true for a task another task's run filed (propose_task, or the
// agent API from a sandbox) whose NothingNeededAction is not
// NothingNeededAck. Such a task is the parent's report-only sub-task: its
// note is the parent's to read (WaitTarget.ClosingNote, which a parked
// parent's next prompt quotes) and its dependents' to build on, and a
// depends-on link is satisfied only by a close. Left in awaiting_reply,
// every dependent sat queued/blocked_on_task until a person closed a
// report nobody but the parent needed (grain/task-4384). A parent that
// does want a person to read and close its child says so with
// nothing_needed_action "ack", or a person-closes goal.
//
// A task a person filed, or a console session's proposal (which is a
// person's request relayed), keeps completing into awaiting_reply: its
// report is for the person who asked for it.
//
// So does a task that is not a report, whatever filed it: one a person
// sits with (Interactive, or hosted on a console), one that restarts when
// its run exits (KeepRunning), and one whose work is a change -- a review,
// or a task with a pull request or changelist linked (AwaitsAck). A
// changelist task's run can end on a summary note with no branch pushed,
// and closing it then would skip its review and Submit gate and unblock
// its dependents before the change landed.
func ClosesOnAnswer(t Task) bool {
	if t.NothingNeededAction.Resolved() == NothingNeededAck {
		return false
	}
	if t.Interactive || t.HasConsole() || t.KeepRunning || !AwaitsAck(t) {
		return false
	}
	p := t.Proposer
	if p.TaskID == "" || p.Attribution.Actor.Kind != PrincipalAgent {
		return false
	}
	return p.Attribution.Actor.Source != SourceSession
}

// AnsweredClosedNote is the sentence posted on a task ClosesOnAnswer
// closed, after the agent's own closing note.
const AnsweredClosedNote = "Closed: this report-only task answered; the task that filed it reads its closing note."

// Resolved is a, with "" read as its default.
func (a NothingNeededAction) Resolved() NothingNeededAction {
	if a == "" {
		return NothingNeededClose
	}
	return a
}

// ParseNothingNeededAction validates a value arriving from the API, a
// declaration or a proposal. "" is accepted and kept as "": it is the
// "nobody chose" that Resolved reads as NothingNeededClose.
func ParseNothingNeededAction(s string) (NothingNeededAction, error) {
	switch a := NothingNeededAction(s); a {
	case "", NothingNeededClose, NothingNeededAck:
		return a, nil
	default:
		return "", fmt.Errorf("nothing_needed_action %q: want %q or %q", s, NothingNeededClose, NothingNeededAck)
	}
}
