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

// PipelinePause is the deployment-wide "pick up no new work" gate: while
// one exists, dispatch starts nothing (dispatch.Paused), and everything
// else grain does -- syncing pull requests, advancing the merge queue,
// cutting releases, exporting the state repository -- carries on exactly
// as before. Runs already in flight are left alone: this stops the queue
// being drained, it does not cancel work that is already under way.
//
// It is the deliberate sibling of orchestrator.Pause, which is the same
// halt arrived at involuntarily when an agent's own usage limit refuses a
// run. The two differ in every way that matters to where the state
// lives. A usage-limit pause describes a window a provider named, expires
// on its own, and is entitled to be forgotten by a restart. This one is
// somebody's decision -- an operator taking the deployment out of service
// to work on the host, or grain itself holding the queue while it checks
// an upgrade it has just installed (orchestrator's upgrade check) -- and
// a decision must survive the very restart an upgrade performs. So it is
// a row, in a table of its own, rather than a field on a struct in one
// process's memory.
//
// A table of its own, and not a column on grain_config, for the reason
// staterepo.SettingsTables gives: the settings row is imported wholesale
// into a running daemon when a merged pull request changes it, and a
// table grain writes for itself must not be replaced underneath the loop
// that is writing it. Pausing is not configuration; the policy that
// decides when grain pauses itself is (Config.UpgradeCheckPausesPipeline).
//
// The zero value is not a pause -- Store.GetPipelinePause returns nil,
// not a zero PipelinePause, for a deployment that is running -- so a
// caller cannot mistake "nobody has paused anything" for "paused at the
// zero instant, by nobody, for no reason".
type PipelinePause struct {
	// PausedAt is when the gate closed. Display, mostly -- nothing
	// expires on it, since this pause has no window to sit out -- except
	// for the deadline orchestrator puts under a pause that is waiting on
	// a task (UntilTaskID below), which is measured from here.
	PausedAt time.Time
	// Reason is the sentence an operator sees on the banner and in
	// `grain pause`: why this deployment is not picking work up. Written
	// by whoever paused, and never empty in practice -- the API supplies
	// one when a caller does not.
	Reason string
	// By is who paused: a human through the UI or the CLI, or the
	// automation principal that files the upgrade check. It is what makes
	// "grain paused itself to check an upgrade" and "somebody paused this
	// deployment by hand" tell apart at a glance, which the resume path
	// also reads -- see UntilTaskID.
	By Principal
	// UntilTaskID, when set, names the one task this pause does not apply
	// to, and whose finishing lifts it. That is the whole of what makes
	// the upgrade check able to hold the queue while still running
	// itself: a pause with no exemption would stop the very task it was
	// taken for.
	//
	// Empty is an operator's own pause: nothing runs, and nothing but a
	// person lifts it.
	UntilTaskID string
}

// Exempts reports whether taskID is the one task this pause lets
// through. A nil pause exempts everything, which is what a deployment
// that is not paused means.
func (p *PipelinePause) Exempts(taskID string) bool {
	if p == nil {
		return true
	}
	return p.UntilTaskID != "" && p.UntilTaskID == taskID
}

// HeldForUpgrade reports whether p is one of the two holds this
// deployment's own upgrade paths take -- a running upgrade's, or a
// scheduled one's while it waits for the queue to drain -- as against an
// operator's decision or the upgrade check's own task-scoped pause.
//
// Naming no task is half of the test: the upgrade check pauses as
// UpgradePrincipal too, and its pause is lifted by the check finishing.
func (p *PipelinePause) HeldForUpgrade() bool {
	if p == nil || p.UntilTaskID != "" {
		return false
	}
	return p.By == UpgradePrincipal || p.By == ScheduledUpgradePrincipal
}

// AdmitsRunNow reports whether a task somebody asked to run now
// (Observation.RunNowAt) is started in spite of p -- "manual mode": an
// operator pauses the deployment and then starts exactly the tasks they
// choose, one by one (grain/task-399).
//
// Every pause admits it but an upgrade's hold. That hold shuts the queue
// in front of a restart which discards every run in flight, so a run
// started under it is a run thrown away, whoever asked for it. The
// upgrade check's own pause is passed: the upgrade is over by then, there
// is no restart coming, and the check only wants the deployment quiet --
// a person asking for one particular run has decided that run is worth
// more than the quiet, which is theirs to decide, exactly as resuming
// the check's pause outright already is.
func (p *PipelinePause) AdmitsRunNow() bool {
	return !p.HeldForUpgrade()
}
