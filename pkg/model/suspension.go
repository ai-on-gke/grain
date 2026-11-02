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

import "time"

// SuspensionKind is why a run stopped without its task being judged: the
// task is not done and nothing about the stop is evidence against it, so
// it is resumed rather than retried (design/design/durable-tasks.design.md#DT-2).
//
// The set is closed and this file is the whole of it. The constants
// below are read by orchestrator's TestEverySuspensionKindKeepsDispatchStanding
// straight out of this file's source, so a kind added here with no row
// in that test fails CI, and TestSuspensionKindsAreClassified fails when
// a run can be finished with an outcome that is neither one of these, a
// counted failure, nor a success (ClassifyOutcome).
//
// A run suspended with any kind:
//   - keeps its task's dispatch standing (DispatchStanding, DT-3): its Run
//     now request is put back, and nothing a run never writes -- order
//     key, base-branch repair mark, grants -- is touched;
//   - does not count towards, or break, its task's failure streak
//     (computeStreakForTask, Store.FailureStreak);
//   - leaves its CLI session suspended for the next run to join
//     (SuspendedSession, agent.RunConfig.ResumeSession).
type SuspensionKind string

const (
	// SuspendDeadline: the run clock ran out on a task that asked to be
	// resumed when it does (resume_on_deadline).
	SuspendDeadline SuspensionKind = "deadline"
	// SuspendIdle: the idle watchdog stopped a durable run.
	SuspendIdle SuspensionKind = "idle"
	// SuspendRestart: the daemon stopped under the run -- a shutdown drain
	// or recoverRun -- and did not adopt it. Recorded as InterruptedOutcome.
	SuspendRestart SuspensionKind = "restart"
	// SuspendSetup: a durable run's sandbox or setup failed. An ordinary
	// task's setup failure is still SetupFailedOutcome, a counted failure.
	SuspendSetup SuspensionKind = "setup"
	// SuspendOOM: the kernel killed the run's sandbox. Recorded as
	// OOMOutcome.
	SuspendOOM SuspensionKind = "oom"
	// SuspendUsageLimit: the account met its usage limit (orchestrator.Pause).
	// Recorded as PausedOutcome.
	SuspendUsageLimit SuspensionKind = "usage-limit"
	// SuspendCapacity: the provider had no capacity (agent.CapacityError).
	// Recorded as CapacityOutcome.
	SuspendCapacity SuspensionKind = "capacity"
	// SuspendModel: the agent CLI failed for a model-side reason that is
	// neither a usage limit nor capacity.
	SuspendModel SuspensionKind = "model"
	// SuspendCapability: a grant changed while the run was live, and the
	// run was restarted to pick it up.
	SuspendCapability SuspensionKind = "capability"
	// SuspendContext: the run's context was exhausted.
	SuspendContext SuspensionKind = "context"
	// SuspendOperatorPause: a person paused the task.
	SuspendOperatorPause SuspensionKind = "operator-pause"
	// SuspendRequested: the agent asked to stop and be resumed later
	// (resume_later, and wait_for_task's park and resume_at, which are
	// forms of it). Recorded as ParkedOutcome.
	SuspendRequested SuspensionKind = "requested"
)

// suspensionKinds is every SuspensionKind, in the order the design's
// table lists them.
var suspensionKinds = []SuspensionKind{
	SuspendDeadline, SuspendIdle, SuspendRestart, SuspendSetup, SuspendOOM,
	SuspendUsageLimit, SuspendCapacity, SuspendModel, SuspendCapability,
	SuspendContext, SuspendOperatorPause, SuspendRequested,
}

// SuspensionKinds is every SuspensionKind.
func SuspensionKinds() []SuspensionKind {
	return append([]SuspensionKind(nil), suspensionKinds...)
}

// Valid reports whether k is one of the declared kinds.
func (k SuspensionKind) Valid() bool {
	for _, known := range suspensionKinds {
		if k == known {
			return true
		}
	}
	return false
}

// SuspendedOutcome is the outcome of a run suspended with a kind that has
// no outcome of its own from before suspensions existed (Outcome).
const SuspendedOutcome = "suspended"

// Outcome is the task_run.outcome a run suspended with k is recorded
// under. The four kinds that existed as outcomes before there was a
// suspension record keep those words, so history and every reader of
// outcome read the same; every other kind is SuspendedOutcome, and the
// kind itself is task_run.suspend_kind.
func (k SuspensionKind) Outcome() string {
	switch k {
	case SuspendRestart:
		return InterruptedOutcome
	case SuspendOOM:
		return OOMOutcome
	case SuspendUsageLimit:
		return PausedOutcome
	case SuspendCapacity:
		return CapacityOutcome
	case SuspendRequested:
		// wait_for_task's park was the agent's own exit before resume_later
		// generalised it (DT-9), and its runs were recorded as parked.
		return ParkedOutcome
	}
	return SuspendedOutcome
}

// MaxDeadlineResumes is how many deadline suspensions in a row a task with
// Task.ResumeOnDeadline gets before its deadline counts as a failure again.
const MaxDeadlineResumes = 3

// DeadlineResumes is how many of runs' most recent ones in a row --
// newest last, as Store.Runs lists them -- were deadline suspensions,
// passing over other suspensions, which neither count nor break the run
// (it is the same rule a failure streak follows).
func DeadlineResumes(runs []Run) int {
	n := 0
	for i := len(runs) - 1; i >= 0; i-- {
		switch {
		case runs[i].FinishedAt == nil:
			continue
		case runs[i].SuspendKind == SuspendDeadline:
			n++
		case runs[i].SuspendKind != "":
			continue
		default:
			return n
		}
	}
	return n
}

// SuspensionKindOfOutcome is the kind a run finished with outcome is
// suspended with when its finish names none (Store.FinishRun): the
// outcomes Outcome keeps, ParkedOutcome among them. Every other outcome,
// SuspendedOutcome included, is not a suspension unless Store.Suspend
// says which.
func SuspensionKindOfOutcome(outcome string) (SuspensionKind, bool) {
	switch outcome {
	case InterruptedOutcome:
		return SuspendRestart, true
	case OOMOutcome:
		return SuspendOOM, true
	case PausedOutcome:
		return SuspendUsageLimit, true
	case CapacityOutcome:
		return SuspendCapacity, true
	case ParkedOutcome:
		return SuspendRequested, true
	}
	return "", false
}

// OutcomeClass is which of the ways a run can stop an outcome is.
type OutcomeClass int

const (
	// OutcomeUnclassified is an outcome nobody has declared, which
	// TestSuspensionKindsAreClassified refuses.
	OutcomeUnclassified OutcomeClass = iota
	// OutcomeSucceeded is SucceededOutcome: it ends a failure streak.
	OutcomeSucceeded
	// OutcomeSuspended is a suspension (SuspensionKind): it neither counts
	// nor ends a streak, and the task keeps its standing.
	OutcomeSuspended
	// OutcomeFailed counts towards the failure streak.
	OutcomeFailed
	// OutcomeHeld is EnvironmentOutcome alone: the host is broken, the
	// task is parked on a person to fix it, and it is not resumed by
	// itself, so it is not a suspension -- but nor is it this task's
	// failure, so it neither counts nor ends a streak.
	OutcomeHeld
)

// failedOutcomes is every outcome that counts towards a failure streak.
// "orphaned" is the word older daemons recorded for a run they found
// with no process.
var failedOutcomes = map[string]bool{
	"failed": true, SetupFailedOutcome: true, "cancelled": true, "no_action": true,
	"finish-failed": true, "orphaned": true,
}

// ClassifyOutcome says which way of stopping outcome is.
func ClassifyOutcome(outcome string) OutcomeClass {
	// A suspension first: ParkedOutcome is a success word too
	// (SucceededOutcome), but a park is the agent's own suspension
	// (SuspendRequested), which neither ends a streak nor counts.
	if _, ok := SuspensionKindOfOutcome(outcome); ok {
		return OutcomeSuspended
	}
	switch {
	case SucceededOutcome(outcome):
		return OutcomeSucceeded
	case outcome == SuspendedOutcome:
		return OutcomeSuspended
	case outcome == EnvironmentOutcome:
		return OutcomeHeld
	case failedOutcomes[outcome]:
		return OutcomeFailed
	}
	return OutcomeUnclassified
}

// DispatchStanding is what decides when a task is dispatched relative to
// the rest, as it stood when a run of it started (startRun) -- the
// snapshot DT-3's invariant is stated against: a task re-queued after a
// suspension has exactly this standing again.
//
// Only RunNowAt is ever restored from it (Store.Suspend puts the request
// back); everything else is derived from rows no run writes, and is here
// so a test, or a person reading a task's history, can compare what the
// task had with what it got. Its JSON is stored as it is, so two
// standings are equal exactly when their encodings are.
type DispatchStanding struct {
	// RunNowAt is the Run now request the run spent
	// (task_run.run_now_at), or nil.
	RunNowAt *time.Time `json:"run_now_at,omitempty"`
	// PriorityDispatch is HasPriorityDispatch: derived from the task's
	// grants and console-session tag, never spent.
	PriorityDispatch bool `json:"priority_dispatch"`
	// Session is IsConsoleSession.
	Session bool `json:"session"`
	// OrderKey is task.order_key, its place in the backlog.
	OrderKey float64 `json:"order_key"`
	// FixesBaseBranch is the base-branch repair mark (FixesBaseBranch).
	FixesBaseBranch bool `json:"fixes_base_branch"`
	// Grants is the capabilities the task holds, sorted.
	Grants []string `json:"grants"`
}

// Suspension is one task_suspension row: a run that stopped with Kind,
// and what its task is to be resumed on.
type Suspension struct {
	RunID  string
	TaskID string
	Kind   SuspensionKind
	Detail string
	// ResumeOn is the JSON wait target (WaitTarget) the task is resumed
	// on, or "" for the next cycle.
	ResumeOn string
	// ResumeAt is the earliest the task may restart, or nil.
	ResumeAt *time.Time
	// Standing is the run's DispatchStanding as it started.
	Standing DispatchStanding
	// ModelHint is the model or connection to resume on, for kinds
	// model and usage-limit.
	ModelHint    string
	CreatedAt    time.Time
	ResumedAt    *time.Time
	ResumedRunID string
}

// SuspensionNote is the one line a run's prompt opens with when the run
// before it was suspended (design/design/durable-tasks.design.md#DT-2, Continuity).
func SuspensionNote(kind SuspensionKind, detail string, at time.Time) string {
	what := string(kind)
	if detail != "" {
		what += ", " + detail
	}
	return "Your previous run was suspended (" + what + ") at " + at.UTC().Format(time.RFC3339) +
		" and this is a fresh run. Anything you did not push is gone."
}
