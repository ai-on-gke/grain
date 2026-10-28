package model

import "time"

// ScheduledUpgrade is an upgrade somebody asked for but did not want
// started yet: grain holds the queue, lets the work already under way
// finish, and cuts over once this deployment is idle (orchestrator's
// reconcileScheduledUpgrade).
//
// It exists because of what an upgrade costs while runs are live. An
// upgrade ends in a restart and a restart discards every run in flight
// -- the agents' work is lost and their tasks are queued again -- so the
// operator of a busy deployment either upgrades now and pays that, or
// sits watching the run count until it reaches zero and presses the
// button by hand. This is the third option: say the branch once, and let
// grain do the waiting.
//
// At most one is pending at a time, the same singleton shape
// PipelinePause has, and for the same reason: "is an upgrade scheduled?"
// is the existence of a row. Asking for a second one replaces the first
// -- the later request is the one the operator means, exactly as it is
// for a pause.
//
// It is grain's note to itself rather than configuration: the daemon
// writes it (clearing it the moment the upgrade actually starts) and a
// settings import must not replace it underneath that. See the
// scheduled_upgrade table in schema.go.
type ScheduledUpgrade struct {
	// Branch is what this deployment will be upgraded onto -- the same
	// value POST /api/upgrade takes, validated the same way before it is
	// ever recorded, so a branch name that could not be upgraded onto is
	// refused when it is asked for rather than hours later when nobody
	// is watching.
	Branch string
	// RequestedAt is when it was asked for. Display, and the age the UI
	// shows beside "waiting for 3 runs to finish" -- nothing expires on
	// it. A deployment that stays busy stays scheduled: the alternative,
	// quietly abandoning an upgrade an operator asked for because the
	// deployment was busy for a long time, is the one outcome nobody
	// wants (see orchestrator's own note on the deadline it deliberately
	// does not impose).
	RequestedAt time.Time
	// By is who asked -- the same Principal a pause records, for the same
	// reason: the banner and `grain pause` say who shut the queue, and
	// the answer here is "whoever scheduled this upgrade".
	//
	// Not always a person. An operator pressing the pane's button is
	// recorded as this deployment's own actor, but a task holding the
	// self-upgrade capability (pkg/capability/selfupgrade) schedules one
	// too, and that is recorded as {PrincipalAgent, <task id>} -- so the
	// pane can name the task that wanted the restart instead of an
	// operator who was not involved (POST /api/upgrade's requestedBy,
	// grain/task-353).
	By Principal
}

// ScheduledUpgradePrincipal is who holds the queue while a deployment
// waits to apply a scheduled upgrade -- the By of the PipelinePause
// orchestrator's reconcileScheduledUpgrade takes, and the thing that
// tells that hold apart from an operator's own pause and from the one a
// running upgrade takes (orchestrator.UpgradePrincipal).
//
// It lives here, beside PipelinePause itself, rather than with the
// reconciler that writes it, because two packages that cannot import
// each other both have to recognise it: the reconciler, which takes and
// releases it, and pkg/ui, where resuming a deployment that is only
// paused because it is waiting to upgrade has to call that schedule off
// as well -- otherwise the next tick shuts the queue again and the
// button appears not to work.
var ScheduledUpgradePrincipal = Principal{Kind: PrincipalAutomation, ID: "upgrade-schedule"}

// UpgradePrincipal is the automation principal a running upgrade acts as
// (orchestrator.UpgradePrincipal, which is this value): the By of the
// hold it takes while it runs, and of the check it files afterwards.
// Here, beside ScheduledUpgradePrincipal, for the same reason -- pkg/ui
// has to tell an upgrade's hold from an operator's pause too, to say
// whether Run now passes it (PipelinePause.AdmitsRunNow).
var UpgradePrincipal = Principal{Kind: PrincipalAutomation, ID: "upgrade"}
