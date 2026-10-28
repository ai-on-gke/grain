// bwsalmon/agents#576: runDaemon's one-time slot-provisioning step
// (configuring each slot's git credentials -- and, for a granule-backed
// slot, creating that slot's VM) used to return straight out of
// runDaemon on its very first failure, which permanently wedged
// reconciliation for the rest of the process's life -- see
// daemon_ui_survives_failure_test.go's own doc comment on why that
// failure was already "logged, not fatal" one level up, in run(), but
// that only kept the UI alive, it never gave the failed step itself
// another chance.
//
// That step is gone: a sandbox belongs to one run now, so there is no
// per-slot provisioning to do at startup at all -- each run mints its own
// token and configures its own git as it prepares its sandbox
// (orchestrator's runOne), and a failure there finishes that one run
// rather than wedging the deployment. retryWithBackoff, the mechanism
// this file used to cover alongside reconcilerDown, went with its last
// caller.
//
// What is left is reconcilerDown (what GET /api/config reports once
// runDaemon does eventually give up), fast enough to run in CI without a
// real granule VM or a live GitHub/Gemini endpoint --
// daemon_granule_wiring_test.go and daemon_ui_survives_failure_test.go
// already cover the slower, more realistic end-to-end paths it sits
// inside of.
package daemon

import (
	"testing"
)

func TestReconcilerDownDefaultsFalseAndLatchesTrue(t *testing.T) {
	// Serial: it asserts on the value of a process-global latch, which
	// means nothing while anything else could be setting it.
	// reconcilerDown is process-global (mirroring orchestrator.
	// ChecksUnavailable's own doc comment on why it too is package-level
	// state rather than threaded through Deps): this only asserts the
	// zero value and the one-way latch, without needing a real runDaemon
	// failure to set it -- daemon_ui_survives_failure_test.go already
	// drives that end to end.
	t.Cleanup(func() { ReconcilerDown.Store(false) })
	ReconcilerDown.Store(false)
	if ReconcilerDown.Load() {
		t.Fatal("reconcilerDown started true, want false")
	}
	ReconcilerDown.Store(true)
	if !ReconcilerDown.Load() {
		t.Fatal("reconcilerDown did not latch true")
	}
}
