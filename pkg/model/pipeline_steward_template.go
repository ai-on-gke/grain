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
	"strconv"
	"strings"
)

// PipelineStewardEveryNMinutes is the cadence grain seeds the pipeline
// steward's schedule with (SystemScheduleRecurrence) -- the deployment's
// to change from then on, like every system schedule's. Six hours: the
// steward is woken by the pipeline.* notifications it subscribes to
// (PipelineStewardSubscriptions), so the schedule is only a safety wake
// for something no notification covers
// (docs/design/durable-tasks.md#DT-6).
const PipelineStewardEveryNMinutes = 6 * 60

// PipelineStewardSubscriptions are the notification families that wake
// the pipeline steward (Store.WakeNotificationSubscribers): every
// pipeline.* notification grain raises.
var PipelineStewardSubscriptions = []string{"pipeline.*"}

// StandingTemplateSubscriptions is the notification families the task a
// standing template (IsStandingTemplateID) files subscribes to, applied
// at every firing (Store.applyStandingDeclarations). Like
// IsStandingTemplateID, a property of the id rather than a Template
// field: a subscription is a way to wake a task holding grants, so only
// grain's own templates declare one.
func StandingTemplateSubscriptions(id string) []string {
	if id == PipelineStewardTemplateID {
		return PipelineStewardSubscriptions
	}
	return nil
}

// PipelineStewardMaxActions is how many filings, holds and pauses one wake
// of the pipeline steward may make, said in its prompt. A queue that needs
// more than this in one wake needs a person, and a steward that can
// file twenty tasks in one turn is a steward whose mistake costs twenty.
const PipelineStewardMaxActions = 5

// PipelineStewardAPIPresets are the agent API presets the pipeline steward
// holds (docs/agent-api.md's Presets), each granted as APIGrantPrefix +
// name:
//
//   - task-admin: comment on, reorder, retry and close tasks, add the
//     depends_on that parks one behind a repair, retry merges, and
//     start and re-run grain CI.
//   - dispatch-control: pause and resume dispatch, and run a task now.
//   - ci-read: read CI runs, logs, merge batches and base-branch gates.
//     task-admin already carries ci:read; it is named on its own so the
//     grant says what the job reads even if task-admin is ever narrowed.
//
// Deliberately not the release-manager preset: that one is for a person
// driving releases and carries releases:write and schedules:write.
// Cutting releases is an operator's choice to hand this job
// (docs/pipeline-steward.md), made by adding api:releases:write to the
// standing task, which a wake keeps (mergeStandingGrants). Nor anything
// dangerous -- merge:force, capabilities:admin, settings:write,
// secrets:write, deployment:* -- which only a person may grant and which
// a test holds this list to.
var PipelineStewardAPIPresets = []string{"task-admin", "dispatch-control", "ci-read"}

// pipelineStewardTemplate is the pipeline steward itself: a durable
// judgement agent, woken by notifications (docs/design/durable-tasks.md#DT-6).
//
//   - No Target. It changes nothing in any repository itself, and files
//     no repairs: the base-branch reconciler owns those. A hold is a
//     dependency or a pause, and every one of them is made through a
//     tool.
//   - Standing (IsStandingTemplateID). One task, re-woken by each firing
//     and each notification it subscribes to (Store.wakeStandingTask,
//     Store.WakeRestingTask) rather than one per firing. The handover it
//     writes each wake is what it remembers, cursor included.
//   - Durable (DurableCapability), with goal pipeline-healthy: on_met
//     rest, on_limit notify. A wake that ends rests the task on its goal
//     rather than completing it; no deadline or wait cap ends a wake, and
//     it is never held behind max_workers.
//   - Subscribed to pipeline.* (PipelineStewardSubscriptions), so a red
//     base, a stalled merge or task, a failing batch or a stale port
//     wakes it at once; the schedule is a six-hourly safety wake.
//   - Model JudgementModelName, resolved at dispatch to the deployment's
//     judgement model (ResolveJudgementModel), never the rotation unless
//     nothing resolves.
//   - NothingNeededAck, so a wake that ends on nothing_needed leaves the
//     task open to be woken again rather than closing it.
//   - The grants: the presets above, self-debug for the task readers and
//     read_pipeline_changes, durable, and priority-dispatch
//     (PriorityDispatchCapability), so that a congested pipeline -- the
//     thing this job exists to unstick -- cannot starve it of a worker,
//     park it behind a red branch's repair or hold it behind a pause it
//     may itself have to lift. Those last two are grants only a person
//     may otherwise give; grain gives them here by construction.
//     query_merge_queue needs no grant; every run has it.
func pipelineStewardTemplate(stateRepo RepoRef) *Template {
	grants := make([]Grant, 0, len(PipelineStewardAPIPresets)+3)
	for _, preset := range PipelineStewardAPIPresets {
		grants = append(grants, Grant{Capability: APIGrantPrefix + preset, Via: GrantByGrain})
	}
	// Named as a string rather than through pkg/capability/selfdebug's
	// own CapabilityName: that package imports this one.
	grants = append(grants, Grant{Capability: "self-debug", Via: GrantByGrain})
	grants = append(grants, Grant{Capability: PriorityDispatchCapability, Via: GrantByGrain})
	grants = append(grants, Grant{Capability: DurableCapability, Via: GrantByGrain})
	t := &Template{
		ID:                  PipelineStewardTemplateID,
		Name:                "Pipeline steward",
		Title:               "Pipeline steward",
		Body:                pipelineStewardBody(),
		NetworkAccess:       NetworkOpen,
		NothingNeededAction: NothingNeededAck,
		Grants:              grants,
		Goal:                PipelineStewardGoal(),
		Model:               JudgementModelName,
	}
	if stateRepo.Owner != "" {
		t.Reads = []RepoRef{stateRepo}
	}
	return t
}

// PipelineStewardGoal is the steward's goal: pipeline-healthy, resting
// when it is met and notifying when a run of wakes makes no progress.
func PipelineStewardGoal() *GoalDecl {
	return &GoalDecl{Kind: GoalPipelineHealthy, OnMet: GoalRestOnMet, OnLimit: GoalNotifyOnLimit}
}

// PipelineStewardReportLines is the most lines a wake's report may run
// to (docs/design/durable-tasks.md#DT-12).
const PipelineStewardReportLines = 15

// pipelineStewardBody is what the run is told. It is written around the
// four rules of docs/design/durable-tasks.md#DT-12, against the ways a
// standing job with write access went wrong on 768's first wake: it read
// everything and overflowed its context, described tasks from memory, and
// acted on what it believed rather than on the record.
func pipelineStewardBody() string {
	var b strings.Builder
	b.WriteString("You are grain's pipeline steward. Your job is the judgement the reconcilers " +
		"cannot make about this deployment's merge pipeline. You are one durable task: a " +
		"`pipeline.*` notification wakes you, and so does a six-hourly safety wake. Each wake " +
		"is a new run of this same task, so this conversation and your handover are your " +
		"memory.\n\n")

	b.WriteString("## What grain already does -- do not do it\n\n")
	b.WriteString("Reconcilers file exactly one repair for a confirmed red base branch, park work " +
		"behind it and release it on green, re-run CI that failed on a flake (and a known " +
		"flaky job failing alone, once), re-run CI a person cancelled once their time-bound " +
		"hold expires, retry stalled merges and batches, file ports, and raise `pipeline.*` " +
		"notifications for a red base, a stalled merge or task, a failing batch, a failing " +
		"repair and a stale port. Never file a repair (`fixes_base_branch`) yourself, and " +
		"never raise a `pipeline.*` notification again.\n\n")

	b.WriteString("## Each wake\n\n")
	b.WriteString("1. **Work from deltas.** Read your handover's cursor, then call " +
		"`read_pipeline_changes` with `after` set to it (no cursor: omit `after`). Read " +
		"nothing else unless a change, or the note that woke you, names it. Do not sweep " +
		"`list_grain_tasks` or `list_ci_runs`.\n" +
		"2. **Re-read before acting or naming.** Immediately before you act on a task, or " +
		"name it in your report, call `read_grain_task` with its id and `view: \"summary\"`. " +
		"Never describe a task from memory or from your handover. Pass `expect` (state, " +
		"title prefix, kind) on every mutating call, so a stale belief is refused.\n" +
		"3. **Disagreement means stop.** If the record contradicts your handover or what you " +
		"believed, do not act on that object. Raise `notify_operator` with subject " +
		"`agent.<your task id>:record-disagrees`, both versions in the body, and carry on " +
		"with the rest.\n" +
		"4. **Judge.** What is left for you is judgement: name the merge you suspect broke a " +
		"branch and say whether a revert is warranted (a revert is a `propose_task`, never " +
		"with `fixes_base_branch`); decide whether to deprioritise or pause work beyond the " +
		"automatic links, and lift what you placed once it is no longer needed; and read a " +
		"stalled pipeline and explain *why* in a notification a person can act on.\n" +
		"5. **Tell, do not decide.** A person decides through `notify_operator`: a force-merge " +
		"as `request.force-merge:<task>`, a revert of a person's change as " +
		"`request.revert:<task or sha>` (with `task` set to the revert task if you filed " +
		"one). You cannot force-merge, and you must not look for a way to. To tell another " +
		"task's agent something (rebase after the repair, wait for a merge), use " +
		"`message_task`: it never parks that task.\n\n")

	b.WriteString("## Limits\n\n")
	b.WriteString("Your actions are: comment, reorder, retry, `notify_operator`, `message_task`, " +
		"and `propose_task` for a revert. At most ")
	b.WriteString(strconv.Itoa(PipelineStewardMaxActions))
	b.WriteString(" of them in one wake. If the pipeline needs more than that, do the most " +
		"important ones and say what is left. A wake with nothing to judge ends at once, " +
		"with no actions: that is the normal case, not a failure. Do not touch work that is " +
		"healthy, and do not re-do what your handover says is already in hand.\n\n")

	b.WriteString("## Finish every wake\n\n")
	b.WriteString("1. Call `write_handover`: first the cursor `read_pipeline_changes` last ended " +
		"with, then every hold you placed and how to lift it, what you are waiting on, and " +
		"what you escalated. Replace it whole each time.\n" +
		"2. Finish with `comment_on_issue`: a report of at most ")
	b.WriteString(strconv.Itoa(PipelineStewardReportLines))
	b.WriteString(" lines, one claim per line, each ending with the id it was read from this " +
		"wake -- `[task 832]`, `[ci run …]`, `[batch …]`. A claim with nothing to cite is not " +
		"made. Or say in one line that nothing needed doing. Ending the run rests this task on " +
		"its goal; it does not close it.\n\n")
	b.WriteString("Never end on `ask_question`. A question parks this task, and while it is parked " +
		"nothing wakes you, so the pipeline goes unwatched until somebody answers. Put anything " +
		"a person has to decide in a `notify_operator` notification instead.")
	return b.String()
}

// UsesToolBudget reports whether t's runs are durable judgement wakes,
// which run under the per-wake tool budget (docs/design/durable-tasks.md#DT-13,
// mcp's tool_budget.go): the pipeline steward's standing task, and any
// task holding the durable grant.
func UsesToolBudget(t Task) bool {
	for _, tag := range t.Tags {
		if tag == ScheduleFiringTag(PipelineStewardTemplateID) {
			return true
		}
	}
	return t.IsDurable()
}
