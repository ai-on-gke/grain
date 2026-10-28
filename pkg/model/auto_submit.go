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
	"strings"
)

// SelfApproved reports whether t was queued on automation's own say-so:
// approved, dispatchable, and with nobody having looked at the proposal
// before it got there.
//
// Two approvals have that shape, and they are the two ways a proposal
// reaches the queue without a click -- AutoSubmitPrincipal, a proposal
// filed on the strength of its proposer's own permission to land
// unreviewed work (AutoSubmits), and ConsoleSessionPrincipal, one queued
// by a standing session whose deployment says it may queue its own work
// (ConsoleSession.TasksApproved).
//
// Narrower than "an automation actor on nobody's behalf", which is
// docs/data-model.md's general shape for an approval made by policy, and
// deliberately so: grain approves tasks of its own that nobody proposed
// -- a schedule's firing, an upgrade check, the review task
// orchestrator.SyncReviews files against a pull request -- and those are
// machinery a human configured once, bounded by the thing that triggers
// them, not an agent's choice to create work. Counting them as levels of
// self-approval would spend a task's depth and fan-out on tasks it never
// asked for. What this names is the chain AutoSubmitLimits bounds, so it
// names only the links an agent can add to it.
//
// A task nobody has approved at all is not self-approved either: it is
// sitting in the queue waiting for exactly the person this is about.
func SelfApproved(t Task) bool {
	if t.Approval == nil || t.Approval.OnBehalfOf != nil {
		return false
	}
	return t.Approval.Actor == AutoSubmitPrincipal || t.Approval.Actor == ConsoleSessionPrincipal
}

// AutoSubmitLimits bounds how far a self-approved tree of proposals may
// grow from the one human approval at the root of it.
//
// AutoSubmits is the permission: a task whose own commits land unread
// may queue follow-up work on the same branch without a click, because
// it could have written that work itself and merged it. What the
// permission alone does not bound is the *tree* -- a proposal filed that
// way inherits its parent's auto-merge too, so it may auto-submit in
// turn, and one approval could otherwise yield a self-approved tree of
// any size with nobody in the loop after the first. Each step still
// needs an agent to call propose_task, so this is a looping agent rather
// than an escalation of privilege: every task in such a tree is confined
// to the branch and the capabilities that first human granted. It is
// still the failure docs/data-model.md says is cheapest to catch at
// creation.
//
// Depth is how many self-approved tasks may stand between one human
// decision and the next, counted over LinkProposedBy (Store.
// AutoSubmitLimit walks it). A human approval breaks the chain and the
// count starts again from there, which is the property worth stating:
// nothing gets more than Depth levels deep without a person, ever, and a
// legitimately deep tree is still reachable one approval at a time.
//
// FanOut is how many self-approved proposals one task may have. Per
// task over its whole life rather than per run of it: a proposal carries
// no run identity to count against, both filing paths
// (orchestrator.relayProposedTasks and ui.Client.CreateTask) have to
// reach the same answer from the store alone, and a task still filing
// its ninth self-approved proposal two runs later is the same loop as
// one filing nine in a row. The two together bound a single approval at
// FanOut + FanOut^2 + ... self-approved tasks, which for the defaults
// below is thirty.
//
// Neither is a refusal. A proposal over either limit is still filed --
// with its body saying why it is waiting (AutoSubmitLimitNote) -- because
// the work an agent identified is not wrong for the tree having got deep,
// and a human reading a proposal that explains itself loses nothing. The
// limit costs a click, never the work.
type AutoSubmitLimits struct {
	Depth  int
	FanOut int
}

const (
	// DefaultAutoSubmitDepth is AutoSubmitLimits.Depth for a deployment
	// that has not chosen one -- docs/data-model.md's own "a limit of 2
	// levels": a task's proposal, and that proposal's own proposal, and
	// then a person. A child of a child of a child is almost always an
	// agent looping.
	DefaultAutoSubmitDepth = 2
	// DefaultAutoSubmitFanOut is AutoSubmitLimits.FanOut for a
	// deployment that has not chosen one. Five is above what splitting
	// one job into pieces takes and far below what a loop produces, and
	// the cost of guessing low is a click on the sixth rather than lost
	// work.
	DefaultAutoSubmitFanOut = 5
)

// DefaultAutoSubmitLimits is the pair a deployment that has never chosen
// runs with.
func DefaultAutoSubmitLimits() AutoSubmitLimits {
	return AutoSubmitLimits{Depth: DefaultAutoSubmitDepth, FanOut: DefaultAutoSubmitFanOut}
}

// Resolved fills in whichever half was left unset with its default. A
// stored zero is "nobody chose" rather than "none allowed": the column
// defaults and DefaultConfig say the same thing in SQL and in Go, so a
// deployment upgrading across them keeps grain's own bounds, and turning
// self-approval off altogether is not this knob's job -- auto-submission
// follows auto-merge, and withholding that is how a deployment says no.
//
// A negative value reads the same way, for the reason ui.UpdateSettings
// refuses one at the door: it is a typo, and the safe reading of a typo
// in a bound is the bound grain ships with.
func (l AutoSubmitLimits) Resolved() AutoSubmitLimits {
	if l.Depth <= 0 {
		l.Depth = DefaultAutoSubmitDepth
	}
	if l.FanOut <= 0 {
		l.FanOut = DefaultAutoSubmitFanOut
	}
	return l
}

// AutoSubmitLimits is this deployment's pair, resolved.
func (c Config) AutoSubmitLimits() AutoSubmitLimits {
	return AutoSubmitLimits{Depth: c.MaxAutoSubmitDepth, FanOut: c.MaxAutoSubmitFanOut}.Resolved()
}

// AutoSubmitLimitsOf is Config.AutoSubmitLimits for a caller holding
// whatever Store.GetConfig returned, nil included -- a deployment with no
// settings row yet runs on grain's own bounds rather than on none.
func AutoSubmitLimitsOf(cfg *Config) AutoSubmitLimits {
	if cfg == nil {
		return DefaultAutoSubmitLimits()
	}
	return cfg.AutoSubmitLimits()
}

// AutoSubmitLimit names which bound a proposal has run into, and is
// AutoSubmitWithinLimits when it has run into neither.
type AutoSubmitLimit string

const (
	// AutoSubmitWithinLimits is the ordinary answer: this proposal is
	// inside both bounds and its proposer's permission decides it
	// (AutoSubmits).
	AutoSubmitWithinLimits AutoSubmitLimit = ""
	// AutoSubmitDepthReached is a proposal whose proposer is already the
	// last self-approved task the depth allows.
	AutoSubmitDepthReached AutoSubmitLimit = "depth"
	// AutoSubmitFanOutReached is a proposer that has already
	// self-approved as many proposals as the fan-out allows.
	AutoSubmitFanOutReached AutoSubmitLimit = "fan-out"
)

// AutoSubmitLimitNote is how a proposal filed over one of those bounds
// says so, in its own body, for the reason
// orchestrator.otherBranchAutoMergeNote exists: a proposal that would
// have been queued and was not, filed silently, leaves the person
// approving it with no idea a decision was made or that they are the
// decision.
//
// Written once here rather than in each of the two packages that file a
// proposal, so the two cannot drift into explaining the same limit
// differently.
func AutoSubmitLimitNote(proposer string, limit AutoSubmitLimit, limits AutoSubmitLimits) string {
	limits = limits.Resolved()
	var why string
	switch limit {
	case AutoSubmitDepthReached:
		why = fmt.Sprintf("this would be %d levels of self-approved work below the last "+
			"person to approve anything, and %d is the limit", limits.Depth+1, limits.Depth)
	case AutoSubmitFanOutReached:
		why = fmt.Sprintf("task %s has already had %d proposals queued that way, which is "+
			"the limit", proposer, limits.FanOut)
	default:
		return ""
	}
	return fmt.Sprintf("\n\n---\n\ngrain: task %s, which proposed this, is an auto-merge job, "+
		"so this would normally have been queued without anybody approving it. It is waiting "+
		"for you instead because %s. Nothing about the work was changed and nothing is wrong "+
		"with it -- the limit is there so one approval cannot grow an unbounded tree of work "+
		"nobody looked at. Approve it if it should go ahead.", proposer, why)
}

// EscalatedCapabilities is the capabilities child asked for by name that
// parent -- the task whose propose_task call filed it -- does not hold
// itself, in the order child lists them.
//
// Only grants the proposal named (GrantByLabel) count. A deployment or
// repo default (GrantByDefault) is attached to every task filed there
// whoever files it, so a proposal carrying one has asked for nothing; a
// capability the run typed into its own propose_task call is a request,
// and one its parent never held is a request for authority nobody has
// given this tree of work yet.
//
// Such a proposal is still filed, holding what it asked for: a run may
// well know that the follow-up it is splitting out needs a credential it
// did not. What it may not be is queued without a person seeing it.
// AutoSubmits already refuses it through GrantsSubsetOf; this is the
// check for the approvals that do not go through AutoSubmits -- a
// standing session's (ConsoleSession.TasksApproved) -- and the list
// CapabilityEscalationNote puts in front of whoever decides.
func EscalatedCapabilities(child, parent Task) []string {
	held := make(map[string]bool, len(parent.Grants))
	for _, g := range parent.Grants {
		held[g.Capability] = true
	}
	var out []string
	for _, g := range child.Grants {
		if IsCapabilityRef(g.Capability) {
			continue
		}
		// A dangerous api scope, or priority-dispatch, is a person's to
		// grant, never an agent's (IsPersonOnlyGrant, docs/agent-api.md):
		// a proposal asking for one is an escalation even when its
		// proposer holds it.
		if g.Via != GrantByLabel || (held[g.Capability] && !IsPersonOnlyGrant(g.Capability)) {
			continue
		}
		held[g.Capability] = true
		out = append(out, g.Capability)
	}
	// A capability reference the parent's recorded grants do not cover
	// is an escalation however it came to be on the child, a default
	// included (docs/capabilities-spec.md §2.3.4).
	return append(out, UncoveredCapabilityRefs(child.Grants, parent.Grants)...)
}

// CapabilityEscalationNote is the line appended to a proposal's body when
// it asked for capabilities its proposer does not hold
// (EscalatedCapabilities), so that approving the work is visibly also
// approving those grants rather than doing so silently. "" for none.
func CapabilityEscalationNote(proposer string, caps []string) string {
	if len(caps) == 0 {
		return ""
	}
	return fmt.Sprintf("\n\n---\n\ngrain: this proposal asked for %s, which task %s, "+
		"which proposed it, does not hold itself. It was filed holding them, so it waits for "+
		"a person rather than being queued automatically: approving it grants those "+
		"capabilities as well as the work. Remove them from the task first if it should go "+
		"ahead without them.", "`"+strings.Join(caps, "`, `")+"`", proposer)
}

// UngrantedWritesNote is the line appended to a proposal that declared
// write targets the task proposing it does not hold itself. Those are
// permission the proposal would pass on to whatever it proposes in turn,
// so taking them on is a person's decision rather than something a run
// may hand itself -- the note names them so approving the work is
// visibly approving them too.
//
// Here rather than in either package that files a proposal, for the
// reason AutoSubmitLimitNote is: the end-of-run relay
// (orchestrator.relayProposedTasks) and the live call (ui.CreateTask)
// must explain the same refusal the same way.
func UngrantedWritesNote(proposer string, writes []WriteTarget) string {
	names := make([]string, 0, len(writes))
	for _, w := range writes {
		if w.Base != "" {
			names = append(names, fmt.Sprintf("`%s` in `%s`", w.Base, w.Repo))
		} else {
			names = append(names, fmt.Sprintf("the default branch of `%s`", w.Repo))
		}
	}
	return fmt.Sprintf("\n\n---\n\ngrain: this proposal declares %s as somewhere its work may land, "+
		"which task %s, which proposed it, was never given itself. It was filed with them, so it "+
		"waits for a person and does not merge on its own: approving it grants those write targets "+
		"as well as the work. Remove them from the task first if it should not have them.",
		strings.Join(names, ", "), proposer)
}

// UngrantedReadsNote is UngrantedWritesNote's counterpart for the
// read-only repositories a proposal asked for that the task proposing it
// could not fetch itself (UngrantedReads).
func UngrantedReadsNote(proposer string, reads []RepoRef) string {
	names := make([]string, 0, len(reads))
	for _, r := range reads {
		names = append(names, fmt.Sprintf("`%s`", r))
	}
	return fmt.Sprintf("\n\n---\n\ngrain: this proposal asks to read %s, which task %s, which "+
		"proposed it, could not read itself. It was filed with them, so it waits for a person: "+
		"approving it grants that read access as well as the work. Remove them from the task "+
		"first if it should not have them.",
		strings.Join(names, ", "), proposer)
}
