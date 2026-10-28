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
	"strings"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/forge"
	"github.com/ai-on-gke/grain/pkg/model"
)

// Liveness by reason: the sub-cases of each state, and what grain does
// when one of them outlives its state's patience.
//
// liveness.go classifies states. A state is too coarse to be the unit of
// the rule (docs/design/task-liveness.md): waiting_ci is driven by
// reconcileTaskCI while a run is owed, by GitHub while GitHub's checks are
// pending, and -- before grain/task-418 -- by nothing at all once a person
// cancelled the run, all under one word and one row. Tasks 335, 351, 360
// and 373 sat in that last sub-case for hours on 2026-10-23 with a table
// above calling their state "driven".
//
// So every reason a state can be held for is declared here, as a constant
// whose value is "<state>/<reason>", and classified in reasons below. The
// guard in liveness_internal_test.go reads the constants off this file the
// way it reads the states off queuestatus.go and model/task.go: a reason
// added and not classified fails the build, and a state with no reason at
// all fails it too.
//
// # The escalation
//
// Every sub-case grain drives, or waits on from outside, says what should
// happen once its state's patience runs out with nothing having left it
// (stallEscalation). The daemon no longer acts on it by itself: the stall
// is detected and shown (Observation.MergeQueueStalledAt, the Inbox's
// merge_stalled group, list_grain_tasks' "(stalled)"), and the
// classification is what a person or system:pipeline-steward reads to
// decide who it belongs to.

// stallEscalation is what a sub-case past its patience is turned into.
type stallEscalation string

const (
	// stallToAgent is a stall the task's own agent can most likely clear
	// (retrigger_ci, wait_for_checks): a requeue, not a repair.
	stallToAgent stallEscalation = "agent"
	// stallToPerson marks the task as needing a person: the merge queue
	// gives up on it (blockMergeQueue), which writes merge_failed with a
	// note naming what did not act, puts the task in the inbox
	// (ui/src/state.js's completionPhase), and offers "Retry merge". For
	// the task lane, where there is no merge entry to block, it is the
	// inbox's stalled group (TaskStallNote, ui.Task.Stall) with the
	// sub-case's own control on the row.
	stallToPerson stallEscalation = "person"
	// stallToResource is a wait on something that has its own liveness:
	// the entry at the front of the queue, the base branch's repair task,
	// the merge-queue entry of a completed task. Escalating every waiter
	// as well as the thing waited on would block a whole queue for one
	// stuck head, so the escalation is the resource's, and the row names
	// the resource.
	stallToResource stallEscalation = "resource"
)

// reasonLiveness is one sub-case's way out.
type reasonLiveness struct {
	class livenessClass
	// resource is the named thing the task is waiting for, in a person's
	// words -- what will free it, not what the state is called. Required
	// for every class but livenessAwaitingPerson (where control is).
	resource string
	// driver is the identifier, in this package, of what leaves this
	// sub-case. Required for livenessDriven; the guard checks it exists.
	driver string
	// escalation is what happens past patience. Required for every class
	// but livenessAwaitingPerson, which is already a person's.
	escalation stallEscalation
	// control is the button that ends a person's wait. Required for
	// livenessAwaitingPerson, and for a task-lane stallToPerson (whose
	// inbox row is the escalation).
	control string
}

// The merge lane's reasons. One block per state, in queuestatus.go's
// order.
const (
	// grain CI is required and has no run for the head commit yet.
	mergeReasonCINotStarted = "waiting_ci/grain_not_started"
	// grain CI's run for the head is queued: none of its jobs has a
	// machine.
	mergeReasonCIQueued = "waiting_ci/grain_queued_for_capacity"
	// grain CI's run for the head has machines and is going.
	mergeReasonCIRunning = "waiting_ci/grain_running"
	// The head's run errored or was cancelled -- by grain or by a
	// person -- and another is owed (forge.GrainCIReplacementDue).
	mergeReasonCIReplacementDue = "waiting_ci/replacement_due"
	// ...and grain has run it as often as it will (Spent).
	mergeReasonCIReplacementSpent = "waiting_ci/replacement_spent"
	// A run concluded with no verdict and no replacement rule for it.
	mergeReasonCINoVerdict = "waiting_ci/no_verdict"
	// GitHub's own checks are the gate and are still pending.
	mergeReasonGitHubChecksPending = "waiting_ci/github_checks_pending"

	mergeReasonQueueBehindHead    = "waiting_queue/behind_head"
	mergeReasonQueueChecksUnknown = "waiting_queue/checks_unknown"

	mergeReasonReviewRunning = "waiting_review/review_running"

	mergeReasonMergeCall = "merging/merge_call"

	mergeReasonUpdateBranch = "updating_branch/base_merge"

	mergeReasonBatchRun = "batching/batch_run"

	mergeReasonGatherWindow = "gathering/gather_window"

	mergeReasonUnfixableTurn = "checks_unfixable/awaiting_turn"

	mergeReasonGitHubApproval = "waiting_github_approval/approval"

	mergeReasonGitHubQueue = "github_merge_queue/github_queue"

	mergeReasonRepairTurn = "repair_needed/awaiting_turn"

	mergeReasonRepairWorker = "repair_queued/awaiting_worker"

	mergeReasonRepairRun = "repairing/repair_run"

	mergeReasonAgentRun = "agent_working/agent_run"

	mergeReasonGaveUp = "merge_failed/given_up"

	mergeReasonReadFailing = "unreadable/read_failing"
)

// mergeReasons classifies every merge-lane reason.
var mergeReasons = map[string]reasonLiveness{
	mergeReasonCINotStarted: {class: livenessDriven, driver: "reconcileTaskCI",
		resource:   "a grain CI run for the head commit, which reconcileTaskCI starts",
		escalation: stallToAgent},
	mergeReasonCIQueued: {class: livenessDriven, driver: "reconcileTaskCI",
		resource:   "a sandbox slot for grain CI's jobs",
		escalation: stallToPerson},
	mergeReasonCIRunning: {class: livenessDriven, driver: "reconcileTaskCI",
		resource:   "the grain CI run in flight on the head commit",
		escalation: stallToPerson},
	mergeReasonCIReplacementDue: {class: livenessDriven, driver: "reconcileTaskCI",
		resource:   "the replacement grain CI run reconcileTaskCI owes this commit",
		escalation: stallToAgent},
	mergeReasonCIReplacementSpent: {class: livenessDriven, driver: "escalateGivenUpGrainCI",
		resource:   "nothing: grain has stopped replacing runs for this commit",
		escalation: stallToPerson},
	mergeReasonCINoVerdict: {class: livenessDriven, driver: "reconcileTaskCI",
		resource:   "a grain CI verdict for the head commit",
		escalation: stallToAgent},
	mergeReasonGitHubChecksPending: {class: livenessAwaitingExternal,
		resource:   "GitHub's check runs on the head commit",
		escalation: stallToPerson},

	mergeReasonQueueBehindHead: {class: livenessDriven, driver: "queueOrder",
		resource:   "the entry at the front of this branch's merge queue",
		escalation: stallToResource},
	// A credential that cannot read checks is one fact about the whole
	// deployment (ChecksUnavailable), not about each pull request, so it is
	// not escalated once per entry: the row is flagged and the inbox's
	// stalled group carries it (mergeQueueStalled).
	mergeReasonQueueChecksUnknown: {class: livenessAwaitingExternal,
		resource:   "the deployment's GitHub credential being able to read checks",
		escalation: stallToResource},

	mergeReasonReviewRunning: {class: livenessDriven, driver: "reviewStateOf",
		resource:   "the pre-merge review task",
		escalation: stallToPerson},

	mergeReasonMergeCall: {class: livenessDriven, driver: "syncEntry",
		resource: "GitHub's answer to the merge call", escalation: stallToPerson},

	mergeReasonUpdateBranch: {class: livenessDriven, driver: "bringHeadUpToDate",
		resource: "GitHub's answer to the base merge", escalation: stallToPerson},

	mergeReasonBatchRun: {class: livenessDriven, driver: "advanceMergeRechecks",
		resource: "the batch CI run in flight", escalation: stallToPerson},

	mergeReasonGatherWindow: {class: livenessDriven, driver: "syncEntry",
		resource: "the gather window expiring", escalation: stallToPerson},

	mergeReasonUnfixableTurn: {class: livenessDriven, driver: "escalateUnfixableChecks",
		resource:   "this entry reaching the front of its queue, where it is handed over",
		escalation: stallToPerson},

	mergeReasonGitHubApproval: {class: livenessAwaitingExternal,
		resource:   "an approving review on GitHub",
		escalation: stallToPerson},

	mergeReasonGitHubQueue: {class: livenessAwaitingExternal,
		resource:   "GitHub's merge queue merging the merge group",
		escalation: stallToPerson},

	mergeReasonRepairTurn: {class: livenessDriven, driver: "requeueForRepair",
		resource: "the repair this entry is owed", escalation: stallToPerson},

	mergeReasonRepairWorker: {class: livenessDriven, driver: "reconcileDispatch",
		resource: "a worker slot for the repair run", escalation: stallToPerson},

	// The two below are a run that is executing. The merge lane does not
	// escalate a live run (escalateStalledEntry's !e.running); the task
	// lane's `running` patience is what watches it.
	mergeReasonRepairRun: {class: livenessDriven, driver: "ProcessResult",
		resource: "the repair run in flight", escalation: stallToPerson},

	mergeReasonAgentRun: {class: livenessDriven, driver: "ProcessResult",
		resource: "the agent run in flight", escalation: stallToPerson},

	mergeReasonGaveUp: {class: livenessAwaitingPerson, control: "Retry merge"},

	// The same deployment-wide fact as checks_unknown, one step worse.
	mergeReasonReadFailing: {class: livenessAwaitingExternal,
		resource:   "the deployment's GitHub credential being able to read this pull request",
		escalation: stallToResource},
}

// The task lane's reasons.
const (
	taskReasonReady          = "queued/ready"
	taskReasonBlocked        = "queued/blocked_on_task"
	taskReasonUsagePaused    = "queued/usage_limit_paused"
	taskReasonRunLive        = "running/live_run"
	taskReasonOnMergeQueue   = "completed/on_merge_queue"
	taskReasonMergeGaveUp    = "completed/merge_failed"
	taskReasonUnapproved     = "proposed/unapproved"
	taskReasonQuestion       = "awaiting_reply/question"
	taskReasonSubmit         = "awaiting_submit/not_auto_merge"
	taskReasonFailedRuns     = "failed/consecutive_failures"
	taskReasonDeferredNoWake = "deferred/no_wake_time"
	taskReasonDeferredWake   = "deferred/wake_time"
	taskReasonClosed         = "closed/closed"
	// A goal task at rest (model.TaskGoal): deferred, waiting for its
	// goal's condition, which reconcileTaskGoals checks every cycle. One
	// per goal kind, "deferred/awaiting-goal/<kind>".
	taskReasonGoalBaseGreen       = "deferred/awaiting-goal/base-green"
	taskReasonGoalPRMerged        = "deferred/awaiting-goal/pr-merged"
	taskReasonGoalTasksClosed     = "deferred/awaiting-goal/tasks-closed"
	taskReasonGoalPersonCloses    = "deferred/awaiting-goal/person-closes"
	taskReasonGoalPipelineHealthy = "deferred/awaiting-goal/pipeline-healthy"
)

// goalReasons is each goal kind's resting reason.
var goalReasons = map[model.GoalKind]string{
	model.GoalBaseGreen:       taskReasonGoalBaseGreen,
	model.GoalPRMerged:        taskReasonGoalPRMerged,
	model.GoalTasksClosed:     taskReasonGoalTasksClosed,
	model.GoalPersonCloses:    taskReasonGoalPersonCloses,
	model.GoalPipelineHealthy: taskReasonGoalPipelineHealthy,
}

// taskReasons classifies every task-lane reason.
var taskReasons = map[string]reasonLiveness{
	taskReasonReady: {class: livenessDriven, driver: "reconcileDispatch",
		resource:   "a worker slot (max_workers) and a sandbox",
		escalation: stallToPerson, control: "Run now"},
	// A task waiting on another: what frees it is that task closing, and
	// that task's own liveness is the escalation (a stuck blocker is
	// flagged, handed over or escalated on its own row).
	taskReasonBlocked: {class: livenessDriven, driver: "reconcileDispatch",
		resource:   "the task it depends on closing",
		escalation: stallToResource},
	// Paused on a connection's usage limit: the window's end is a named
	// time, and dispatch resumes on the first cycle past it. Every
	// connection serving the model paused for hours is still a named,
	// timed resource -- the pause banner says until when -- so this
	// escalates to a person only past the backlog's own patience.
	taskReasonUsagePaused: {class: livenessDriven, driver: "reconcileDispatch",
		resource:   "the model connection's usage-limit window ending",
		escalation: stallToPerson, control: "Run now"},
	taskReasonRunLive: {class: livenessDriven, driver: "ProcessResult",
		resource:   "the run in flight",
		escalation: stallToPerson, control: "Cancel"},
	taskReasonOnMergeQueue: {class: livenessDriven, driver: "syncEntry",
		resource:   "the merge queue landing its pull request",
		escalation: stallToResource},
	// The badge still reads completed; the inbox shows it (completionPhase)
	// with the merge lane's control.
	taskReasonMergeGaveUp:    {class: livenessAwaitingPerson, control: "Retry merge"},
	taskReasonUnapproved:     {class: livenessAwaitingPerson, control: "Approve"},
	taskReasonQuestion:       {class: livenessAwaitingPerson, control: "Reply"},
	taskReasonSubmit:         {class: livenessAwaitingPerson, control: "Submit"},
	taskReasonFailedRuns:     {class: livenessAwaitingPerson, control: "Retry"},
	taskReasonDeferredNoWake: {class: livenessAwaitingPerson, control: "Undefer"},
	taskReasonDeferredWake: {class: livenessDriven, driver: "reconcileWake",
		resource:   "the wake time a person put on the deferral",
		escalation: stallToPerson, control: "Undefer"},
	taskReasonClosed: {class: livenessAwaitingPerson, control: "Reopen"},
	// A resting goal task waits for a named resource, its goal's
	// condition. Its driver is reconcileTaskGoals and its patience is the
	// goal's progress limit, which raises task.no-progress on its own
	// (applyGoalLimit) -- so the escalation is the resource's.
	taskReasonGoalBaseGreen: {class: livenessDriven, driver: "reconcileTaskGoals",
		resource: "its base branch going green", escalation: stallToResource, control: "Undefer"},
	taskReasonGoalPRMerged: {class: livenessDriven, driver: "reconcileTaskGoals",
		resource: "the pull request merging", escalation: stallToResource, control: "Undefer"},
	taskReasonGoalTasksClosed: {class: livenessDriven, driver: "reconcileTaskGoals",
		resource: "the tasks it names closing", escalation: stallToResource, control: "Undefer"},
	taskReasonGoalPersonCloses: {class: livenessAwaitingPerson, control: "Close"},
	taskReasonGoalPipelineHealthy: {class: livenessDriven, driver: "reconcileTaskGoals",
		resource: "the pipeline being healthy", escalation: stallToResource, control: "Undefer"},
}

// reasonState is the state a reason belongs to: the part of its value
// before the slash.
func reasonState(reason string) string {
	state, _, _ := strings.Cut(reason, "/")
	return state
}

// mergeReasonOf is the sub-case an entry in state is held in. Total over
// mergeLane's states: an entry whose particulars match no narrower reason
// gets its state's broadest one, never "".
//
// runner may be nil; it is only asked whether a running grain CI run has
// any job with a machine yet.
func mergeReasonOf(e queueEntry, state string, runner ci.Runner) string {
	switch state {
	case mergeStateWaitingCI:
		return waitingCIReasonOf(e, runner)
	case mergeStateWaitingQueue:
		if e.health == model.PrUnknown {
			return mergeReasonQueueChecksUnknown
		}
		return mergeReasonQueueBehindHead
	case mergeStateWaitingReview:
		return mergeReasonReviewRunning
	case mergeStateMerging:
		return mergeReasonMergeCall
	case mergeStateUpdatingBranch:
		return mergeReasonUpdateBranch
	case mergeStateBatching:
		return mergeReasonBatchRun
	case mergeStateGathering:
		return mergeReasonGatherWindow
	case mergeStateChecksUnfixable:
		return mergeReasonUnfixableTurn
	case mergeStateWaitingGitHubApproval:
		return mergeReasonGitHubApproval
	case mergeStateGitHubQueue:
		return mergeReasonGitHubQueue
	case mergeStateRepairNeeded:
		return mergeReasonRepairTurn
	case mergeStateRepairQueued:
		return mergeReasonRepairWorker
	case mergeStateRepairing:
		return mergeReasonRepairRun
	case mergeStateWorking:
		return mergeReasonAgentRun
	case mergeStateFailed:
		return mergeReasonGaveUp
	case mergeStateUnreadable:
		return mergeReasonReadFailing
	}
	return ""
}

// waitingCIReasonOf splits waiting_ci by what is actually being waited
// for, off the same grainCIVerdict the row's sentence is written from.
func waitingCIReasonOf(e queueEntry, runner ci.Runner) string {
	grain := e.grain
	if !grain.Required {
		return mergeReasonGitHubChecksPending
	}
	if grain.Running {
		if status := ciStatusOf(e.obs); runner != nil && status != nil {
			if run, ok := runner.Get(status.RunID); ok && !run.Interrupted && !ciRunProgressed(run) {
				return mergeReasonCIQueued
			}
		}
		return mergeReasonCIRunning
	}
	if grain.Cancelled || grain.Errored {
		switch grain.Replacement {
		case forge.GrainCIReplacementDue:
			return mergeReasonCIReplacementDue
		case forge.GrainCIReplacementSpent:
			return mergeReasonCIReplacementSpent
		}
		return mergeReasonCINoVerdict
	}
	if grain.Passed || grain.Failed {
		// grain's verdict is in and something else is still pending (a
		// GitHub check counted beside it).
		return mergeReasonGitHubChecksPending
	}
	return mergeReasonCINotStarted
}

// stalledDriver is the classification whose driver a stall comment names:
// the sub-case's own where it has one, the state's otherwise.
func stalledDriver(state stateLiveness, reason reasonLiveness) stateLiveness {
	if reason.class == livenessAwaitingExternal {
		state.class = livenessAwaitingExternal
		return state
	}
	if reason.driver != "" && reason.driver != state.driver {
		return stateLiveness{class: livenessDriven, driver: reason.driver}
	}
	return state
}

// mergeReasonOfState is the sub-case a merge-queue state is recorded with
// when its writer did not work one out (queueStatus.reason): the state's
// broadest reason, as mergeReasonOf gives an entry with no particulars.
// Only the main pass writes waiting_ci, waiting_queue off health, and
// base_broken with a repair task, and it always names the reason; every
// other writer writes a state with one reason, or none ("").
func mergeReasonOfState(state string) string {
	return mergeReasonOf(queueEntry{}, state, nil)
}

// taskReasonOf is the sub-case a task in state is held in: the task
// lane's mergeReasonOf. Total over taskLane's states -- a task whose
// particulars match no narrower reason gets its state's broadest one --
// and "" only for a state the lane does not know.
//
// It reads only what a row already has to hand, so the UI can ask it on
// every read (TaskReason): the task itself for DeferredUntil, its
// observation for what the merge queue last said about it, the tasks it
// is still waiting on, and whether a usage-limit pause covers the
// connection it would be dispatched onto (Pause's Coverage, as
// gatedByPause asks it). obs may be nil.
//
// Of the queued sub-cases, an open dependency outranks a pause: the
// pause ends on a clock, and a task still waiting on another would not
// be dispatched when it did.
func taskReasonOf(t model.Task, state model.State, obs *model.Observation, openDeps []string, paused bool) string {
	switch state {
	case model.StateQueued:
		switch {
		case len(openDeps) > 0:
			return taskReasonBlocked
		case paused:
			return taskReasonUsagePaused
		}
		return taskReasonReady
	case model.StateRunning:
		return taskReasonRunLive
	case model.StateCompleted:
		// The merge queue's own giving up: blockMergeQueue stamps
		// MergeQueueBlockedAt and writes merge_failed with it, and the
		// task's badge reads completed through both.
		if obs != nil && (obs.MergeQueueBlockedAt != nil || obs.MergeQueueState == mergeStateFailed) {
			return taskReasonMergeGaveUp
		}
		return taskReasonOnMergeQueue
	case model.StateProposed:
		return taskReasonUnapproved
	case model.StateAwaitingReply:
		return taskReasonQuestion
	case model.StateAwaitingSubmit:
		return taskReasonSubmit
	case model.StateFailed:
		return taskReasonFailedRuns
	case model.StateDeferred:
		if reason, ok := goalReasons[t.GoalKind]; ok && t.DeferredUntil == nil {
			return reason
		}
		if t.DeferredUntil != nil {
			return taskReasonDeferredWake
		}
		return taskReasonDeferredNoWake
	case model.StateClosed:
		return taskReasonClosed
	}
	return ""
}

// ReasonLiveness is one sub-case as a row draws it: which reason, which of
// the three conditions it is in, what it is waiting on, and the button
// that ends a person's part in it. The zero value is "no sub-case".
type ReasonLiveness struct {
	// Reason is the sub-case, "<state>/<reason>".
	Reason string
	// Class is the condition, in livenessClass's words: "driven",
	// "awaiting a person" or "awaiting an external answer".
	Class string
	// Resource is what the wait is for, in a person's words; empty for a
	// sub-case that is a person's.
	Resource string
	// Driver is the component that ends a driven sub-case.
	Driver string
	// Control is the label of the button that ends the wait, where the
	// sub-case has one: a person's own, or a driven one whose escalation
	// is a person's ("Run now" on a queued task nothing dispatches).
	Control string
}

// reasonLivenessOf looks reason up in reasons.
func reasonLivenessOf(reasons map[string]reasonLiveness, reason string) ReasonLiveness {
	r, ok := reasons[reason]
	if !ok {
		return ReasonLiveness{}
	}
	return ReasonLiveness{Reason: reason, Class: string(r.class), Resource: r.resource,
		Driver: r.driver, Control: r.control}
}

// TaskReason is taskReasonOf for a reader outside this package, with its
// classification: what cmd/grain hands pkg/ui (ui.Config.Reason) beside
// TaskStallNote, for the same layering reason -- which sub-cases a state
// has and what ends each is this package's, and the UI draws it.
//
// The pause coverage is read off the task's grant wait (wait, nil for
// none) rather than asked of the Pause: dispatch records what held the
// task back on the cycle that held it (gatedByPause), against the
// connection, model and framework the task would actually have run on,
// which a reader outside the cycle cannot resolve for itself.
func TaskReason(t model.Task, state model.State, obs *model.Observation, openDeps []string,
	wait *model.GrantWait) ReasonLiveness {
	return reasonLivenessOf(taskReasons, taskReasonOf(t, state, obs, openDeps, usageLimitWait(wait)))
}

// usageLimitWait reports whether wait is the one dispatch records for a
// task a usage-limit pause covers: Coverage.Reason's sentence, written by
// gatedByPause, or dispatch's own where the pause wrote none. A quota
// wait for sandbox budget or worker capacity is neither.
func usageLimitWait(wait *model.GrantWait) bool {
	if wait == nil || wait.Capability != model.QuotaWaitCapability {
		return false
	}
	return wait.Resource == dispatch.ConnectionPausedWhy || strings.Contains(wait.Resource, pausedUntilWords)
}

// MergeReason is the classification of a merge-queue sub-case the cycle
// recorded (model.Observation.MergeQueueReason), or the zero value for a
// reason this build does not know -- a row written by a newer daemon, or
// by one from before reasons were recorded.
func MergeReason(reason string) ReasonLiveness {
	return reasonLivenessOf(mergeReasons, reason)
}
