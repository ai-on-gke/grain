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
	"slices"
	"strconv"
	"strings"
)

// PipelineStewardEveryNMinutes is the cadence grain seeds the pipeline
// steward's schedule with (SystemScheduleRecurrence) -- the deployment's
// to change from then on, like every system schedule's. Six hours: the
// steward is woken by the pipeline.* notifications it subscribes to
// (PipelineStewardSubscriptions), so the schedule is only a safety wake
// for something no notification covers
// (design/design/durable-tasks.design.md#DT-6).
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
// judgement agent, woken by notifications (design/design/durable-tasks.design.md#DT-6).
//
//   - No Target. It changes nothing in any repository itself, and files
//     no repairs: the base-branch reconciler owns those. A hold is a
//     dependency or a pause, and every one of them is made through a
//     tool.
//   - Standing (IsStandingTemplateID). One task, re-woken by each firing
//     and each notification it subscribes to (Store.wakeStandingTask,
//     Store.WakeRestingTask) rather than one per firing. The handover it
//     writes each wake is what it remembers, cursor included.
//   - Admission durable (AdmissionDurable), with goal pipeline-healthy: on_met
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
//   - The grants: the presets above, and what self-debug became
//     (api:self-debug for the task readers and read_pipeline_changes, and
//     grain#source). Its admission, durable (which includes bypass, what
//     the retired priority-dispatch grant gave), means a congested
//     pipeline -- the thing this job exists to unstick -- cannot starve it
//     of a worker, park it behind a red branch's repair or hold it behind
//     a pause it may itself have to lift. Only a person may otherwise
//     raise a task's admission above standard; grain sets it here by
//     construction (unified-grants.design.md §4.1).
//     query_merge_queue needs no grant; every run has it.
func pipelineStewardTemplate(stateRepo RepoRef) *Template {
	grants := make([]Grant, 0, len(PipelineStewardAPIPresets)+2)
	for _, preset := range PipelineStewardAPIPresets {
		grants = append(grants, Grant{Capability: APIRef(preset), Via: GrantByGrain})
	}
	// What self-debug became (SelfDebugGrants): its api preset for the
	// task readers and grain#source for grain's own source.
	for _, g := range SelfDebugGrants() {
		grants = append(grants, Grant{Capability: g, Via: GrantByGrain})
	}
	t := &Template{
		ID:                  PipelineStewardTemplateID,
		Name:                "Pipeline steward",
		Title:               "Pipeline steward",
		Body:                pipelineStewardBody(),
		NetworkAccess:       NetworkOpen,
		Admission:           AdmissionDurable,
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
// to (design/design/durable-tasks.design.md#DT-12).
const PipelineStewardReportLines = 15

// pipelineStewardBody is what the run is told. It is written around the
// four rules of design/design/durable-tasks.design.md#DT-12, against the ways a
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
		"repair and a stale port. For releases they judge each release candidate's " +
		"qualification, re-run its flakes and infrastructure casualties, raise " +
		"`pipeline.rc-failed`, `pipeline.release-escalated`, `pipeline.release-infra-stalled` " +
		"and `pipeline.flake-budget`, and keep the hold on the next candidate; the release " +
		"train cuts, discards and stages candidates. Never file a repair (`fixes_base_branch`) " +
		"yourself, never raise a `pipeline.*` notification again, and never cut, tag, approve " +
		"or promote a release.\n\n")

	b.WriteString("## Each wake\n\n")
	b.WriteString("Read and act through the `grain` CLI in your sandbox (`grain help`); only " +
		"`propose_task`, `resume_later`, `comment_on_issue` and the `grain-controller` MCP tools " +
		"(`grain_explore_scope` / `grain_invoke`) your dispatch-control preset serves (pausing, resuming, Run now) " +
		"are tool calls.\n\n")
	b.WriteString("1. **Work from deltas.** Read your handover's cursor, then run " +
		"`grain read deploy://pipeline-changes --cursor <cursor>` (no cursor: leave `--cursor` off). " +
		"Its last line is the command that reads the next page: run it while the page says more " +
		"changes follow, and hand over the cursor the last page ended with. " +
		"Read nothing else unless a change, or the note that woke you, names it. Do not sweep " +
		"`grain task list` or `grain ci list`; `grain merge-queue`, `grain ci log`, " +
		"`grain health` and `grain log` are for what a change names.\n" +
		"2. **Re-read before acting or naming.** Immediately before you act on a task, or " +
		"name it in your report, run `grain task read <id>`. " +
		"Never describe a task from memory or from your handover. Pass `expect` (state, " +
		"title prefix, kind) on every mutating call, so a stale belief is refused.\n" +
		"3. **Disagreement means stop.** If the record contradicts your handover or what you " +
		"believed, do not act on that object. Raise `grain notify` with subject " +
		"`agent.<your task id>:record-disagrees`, both versions in the body, and carry on " +
		"with the rest.\n" +
		"4. **Judge.** What is left for you is judgement: name the merge you suspect broke a " +
		"branch and say whether a revert is warranted (a revert is a `propose_task`, never " +
		"with `fixes_base_branch`); decide whether to deprioritise or pause work beyond the " +
		"automatic links, and lift what you placed once it is no longer needed; and read a " +
		"stalled pipeline and explain *why* in a notification a person can act on.\n" +
		"5. **Tell, do not decide.** A person decides through `grain notify`: a force-merge " +
		"as `request.force-merge:<task>`, a revert of a person's change as " +
		"`request.revert:<task or sha>` (with `task` set to the revert task if you filed " +
		"one). You cannot force-merge, and you must not look for a way to. To tell another " +
		"task's agent something (rebase after the repair, wait for a merge), use " +
		"`grain task message <id>`: it never parks that task.\n\n")

	writeRedRCSection(&b)

	b.WriteString("## Limits\n\n")
	b.WriteString("Your actions are: comment, reorder, retry, `grain notify`, `grain task message`, " +
		"and `propose_task` for a revert or a red release candidate's blocker. At most ")
	b.WriteString(strconv.Itoa(PipelineStewardMaxActions))
	b.WriteString(" of them in one wake. If the pipeline needs more than that, do the most " +
		"important ones and say what is left. A wake with nothing to judge ends at once, " +
		"with no actions: that is the normal case, not a failure. Do not touch work that is " +
		"healthy, and do not re-do what your handover says is already in hand.\n\n")

	b.WriteString("## Finish every wake\n\n")
	b.WriteString("1. Run `grain handover write --file <f>`: first the cursor the pipeline changes last ended " +
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
		"a person has to decide in a `grain notify` notification instead.")
	return b.String()
}

// writeRedRCSection is the steward's half of a red release candidate
// (design/playbook/releases.playbook.md §5 and §12): the judgement the
// release reconciler (orchestrator's releaseredrc.go) leaves to it once
// it has raised the notification.
func writeRedRCSection(b *strings.Builder) {
	marker := RCBlockerMarker("<rc_tag>")
	b.WriteString("## A red release candidate\n\n")
	b.WriteString("`pipeline.rc-failed:<rc_tag>` wakes you when a candidate `vX.Y.Z-rc.N` is confirmed " +
		"red. Grain has already re-run its flakes and infrastructure casualties, posted the verdict, and " +
		"put the failing items, the attempt number and the commit range `<floor>..<rc_sha>` in the " +
		"notification. The train tags the candidate `-discarded` and cuts `rc.(N+1)` from the next " +
		"known-good commit. What is left is yours:\n\n")
	b.WriteString("1. **Triage `<floor>..<rc_sha>`.** Read the failing items' tasks and logs the " +
		"notification names, then `git log --first-parent` over the range it gives (from `rc.2` on, " +
		"start with its \"new since\" range). Name the merge you suspect and the evidence for it. A " +
		"failure that reads as infrastructure or a flake is not a regression, so file nothing for it " +
		"and name `<rc_tag>` in your closing report so grain completes the triage hold.\n")
	b.WriteString("2. **File the fix or the revert, high priority.** As soon as qualification fails, " +
		"grain posts check run `" + ReleaseHoldCheckName + "` in progress on the red candidate's commit " +
		"while triage is pending, so `rc.(N+1)` is not cut before your wake finishes. For each suspect, `propose_task` a " +
		"revert, or a fix where a revert would lose more than it saves. Target `main`, pass " +
		"`at_front: true` (a priority above your own waits for a person), and start the title with `" +
		marker + "` exactly. That title keeps the hold. While any task with it is open, grain keeps check " +
		"run `" + ReleaseHoldCheckName + "` in progress on the red candidate's commit, so `rc.(N+1)` is " +
		"not cut onto a tree you know is broken. It completes by itself once they have all landed (or when " +
		"your closing report names `<rc_tag>` with no blocker filed, or the triage window elapses), so " +
		"there is nothing to lift. An open task that already fixes the cause holds nothing unless its " +
		"title has the marker: message it and name it in your report. If `main` is red too, the " +
		"base-branch repair is the fix. Do not file another; name `<rc_tag>` in your report, since a red `main` already stops the train " +
		"cutting.\n")
	b.WriteString("3. **From `rc.2` on, compare with the candidate before.** Every candidate is cut " +
		"from `main`'s newest known-good commit; there is no `release/vX.Y.Z` branch to backport to, so " +
		"fixes land on `main` only and the next cut picks them up. Compare the failure with the " +
		"previous candidate's (`pipeline.rc-failed:vX.Y.Z-rc.(N-1)`). If it failed on a regression the " +
		"previous one did not have, trunk churn broke it: triage the \"new since\" range. If it failed " +
		"the way the previous one did, that candidate's fix did not work. Say so, and file the next " +
		"fix the same way. A candidate tagged `-discarded` with `Discard-Reason: daily-cap` or " +
		"`Discard-Reason: held-over` was dropped by the daily cap without being judged: it is not red, " +
		"and there is nothing to triage.\n")
	b.WriteString("4. **Escalate after " + strconv.Itoa(ReleaseMaxRCAttempts) + " red candidates.** Once " +
		"`rc." + strconv.Itoa(ReleaseMaxRCAttempts) + "` is red (RELEASE_MAX_RC_ATTEMPTS), grain raises " +
		"`pipeline.release-escalated:vX.Y.Z` to the person's Inbox, once. The train does not stop: it " +
		"keeps cutting `rc." + strconv.Itoa(ReleaseMaxRCAttempts+1) + "`, `rc." +
		strconv.Itoa(ReleaseMaxRCAttempts+2) + "` and on from `main` until the day releases, and your " +
		"`" + RCBlockerMarker("<rc_tag>") + "` tasks still hold each next cut. Keep triaging each red " +
		"candidate as above. Put your triage beside the escalation with `grain notify` subject " +
		"`agent.<your task id>:release-escalated:vX.Y.Z`. Give the suspect commits of each red " +
		"candidate, whether they share a cause, and what would make the version converge. A person " +
		"decides.\n\n")
	b.WriteString("`pipeline.flake-budget:<repo>:<item>` is a qualification item that spent " +
		strconv.Itoa(FlakeBudgetRetries) + " flake retries in a day. Propose one de-flake task for it " +
		"(a quarantine entry is a person's review), unless one is already open. " +
		"`pipeline.release-infra-stalled:<rc_tag>` is a candidate still qualifying while an item keeps " +
		"failing on infrastructure. Read that item's last runs, name the runner, quota or credential " +
		"that is failing in a `grain notify` a person can act on, and file no code fix for it; once " +
		"the infrastructure is repaired, acknowledging or clearing `pipeline.release-infra-stalled:<rc_tag>` " +
		"starts a fresh Stage 2 qualification attempt if Stage 2 had concluded `neutral`.\n\n")
}

// UsesToolBudget reports whether t's runs are durable judgement wakes,
// which run under the per-wake tool budget (design/design/durable-tasks.design.md#DT-13,
// mcp's tool_budget.go): the pipeline steward's standing task, and any
// task holding the durable grant.
func UsesToolBudget(t Task) bool {
	return IsPipelineStewardFiring(t) || t.IsDurable()
}

// IsPipelineStewardFiring reports whether t is one of the pipeline
// steward's own firings, whose wakes start from the pipeline-changes
// cursor the last one handed over.
func IsPipelineStewardFiring(t Task) bool {
	return slices.Contains(t.Tags, ScheduleFiringTag(PipelineStewardTemplateID))
}
