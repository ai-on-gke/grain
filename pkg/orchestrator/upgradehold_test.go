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

package orchestrator_test

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// operator is a person's own pause, the one thing neither half of the
// hold is ever allowed to touch.
var operator = model.Principal{Kind: model.PrincipalHuman, ID: "alice"}

// The whole shape of the feature, both halves of it: the upgrade closes
// the gate, its restart carries this deployment past the process that
// closed it, and the startup pass on the far side is what opens it
// again. Without that pass the first successful upgrade would leave the
// deployment paused forever -- reconcilePipelinePause leaves a pause
// naming no task alone, on purpose, because that is what an operator's
// own pause looks like.
func TestAnUpgradeHoldsTheQueueAndTheNextStartupLetsItGo(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)

	release, err := orchestrator.UpgradeHold(store)("main")
	if err != nil {
		t.Fatalf("holding the queue for an upgrade: %v", err)
	}
	if release == nil {
		t.Fatal("holding the queue on an unpaused deployment gave back no release")
	}

	pause, err := store.GetPipelinePause(ctx)
	if err != nil {
		t.Fatalf("GetPipelinePause: %v", err)
	}
	if pause == nil {
		t.Fatal("the queue is not held while an upgrade is running")
	}
	if pause.By != orchestrator.UpgradePrincipal {
		t.Errorf("held by %+v, want %+v", pause.By, orchestrator.UpgradePrincipal)
	}
	if pause.UntilTaskID != "" {
		t.Errorf("the hold names task %q; it waits on nothing finishing", pause.UntilTaskID)
	}
	if pause.Reason == "" {
		t.Error("the hold gives no reason, and the banner has nothing to show")
	}

	// The process that took it is gone by now -- this is the next one.
	if err := orchestrator.ReleaseUpgradeHold(ctx, store); err != nil {
		t.Fatalf("ReleaseUpgradeHold: %v", err)
	}
	if pause, err := store.GetPipelinePause(ctx); err != nil {
		t.Fatalf("GetPipelinePause: %v", err)
	} else if pause != nil {
		t.Errorf("still held after the restart the hold was taken for: %q", pause.Reason)
	}
}

// The paths that end with this deployment still running the build it had
// -- a failed staging, a deployment with no restart command -- have no
// next startup to open the gate, so the upgrade opens it itself.
func TestAnUpgradeThatDoesNotRestartLetsItsOwnHoldGo(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)

	release, err := orchestrator.UpgradeHold(store)("main")
	if err != nil {
		t.Fatalf("holding the queue for an upgrade: %v", err)
	}
	release()

	if pause, err := store.GetPipelinePause(ctx); err != nil {
		t.Fatalf("GetPipelinePause: %v", err)
	} else if pause != nil {
		t.Errorf("still held after the upgrade let go: %q", pause.Reason)
	}
}

// SetPipelinePause replaces the single row, so an upgrade that took a
// hold over somebody else's pause would be holding a row it is entitled
// to delete -- and its release would lift a pause it never took. It
// takes no hold at all instead: the queue is shut either way.
func TestAnUpgradeTakesNoHoldOnADeploymentSomebodyHasAlreadyPaused(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)

	mine := model.PipelinePause{
		PausedAt: time.Now().UTC(),
		Reason:   "working on the host",
		By:       operator,
	}
	if err := store.SetPipelinePause(ctx, mine); err != nil {
		t.Fatalf("SetPipelinePause: %v", err)
	}

	release, err := orchestrator.UpgradeHold(store)("main")
	if err != nil {
		t.Fatalf("holding the queue for an upgrade: %v", err)
	}
	if release != nil {
		t.Error("took a hold on a deployment somebody had already paused")
	}

	pause, err := store.GetPipelinePause(ctx)
	if err != nil {
		t.Fatalf("GetPipelinePause: %v", err)
	}
	if pause == nil || pause.By != operator || pause.Reason != mine.Reason {
		t.Fatalf("the operator's pause was replaced: %+v", pause)
	}
}

// An operator can pause a deployment during an upgrade -- on the paths
// where nothing restarts, the UI never stops serving -- and their pause
// replaces the hold in the one row there is. Neither half of the release
// may lift it: it says something the hold did not, and only a person
// lifts one.
func TestReleasingAnUpgradeHoldLeavesAPauseSomebodyTookDuringItAlone(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)

	release, err := orchestrator.UpgradeHold(store)("main")
	if err != nil {
		t.Fatalf("holding the queue for an upgrade: %v", err)
	}

	theirs := model.PipelinePause{
		PausedAt: time.Now().UTC(),
		Reason:   "stopping this while I look at the disk",
		By:       operator,
	}
	if err := store.SetPipelinePause(ctx, theirs); err != nil {
		t.Fatalf("SetPipelinePause: %v", err)
	}

	release()
	if err := orchestrator.ReleaseUpgradeHold(ctx, store); err != nil {
		t.Fatalf("ReleaseUpgradeHold: %v", err)
	}

	pause, err := store.GetPipelinePause(ctx)
	if err != nil {
		t.Fatalf("GetPipelinePause: %v", err)
	}
	if pause == nil {
		t.Fatal("the upgrade lifted an operator's own pause")
	}
	if pause.By != operator || pause.Reason != theirs.Reason {
		t.Errorf("pause is now %+v, want the operator's own back", pause)
	}
}

// The upgrade check's pause is taken by the same principal a hold is,
// and is the one thing that distinguishes them: it names the task it is
// waiting for. Lifting it here would let the queue past the very check
// it was taken for, and would do it on exactly the startup that check
// exists to examine.
func TestTheNextStartupLeavesTheUpgradeChecksOwnPauseAlone(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)

	check := model.PipelinePause{
		PausedAt:    time.Now().UTC(),
		Reason:      "checking the upgrade to main",
		By:          orchestrator.UpgradePrincipal,
		UntilTaskID: "42",
	}
	if err := store.SetPipelinePause(ctx, check); err != nil {
		t.Fatalf("SetPipelinePause: %v", err)
	}

	if err := orchestrator.ReleaseUpgradeHold(ctx, store); err != nil {
		t.Fatalf("ReleaseUpgradeHold: %v", err)
	}

	pause, err := store.GetPipelinePause(ctx)
	if err != nil {
		t.Fatalf("GetPipelinePause: %v", err)
	}
	if pause == nil || pause.UntilTaskID != check.UntilTaskID {
		t.Fatalf("the upgrade check's pause was lifted at startup: %+v", pause)
	}
}

// A scheduled upgrade's hold is the one pause an arriving upgrade takes
// over rather than deferring to: it was taken to keep the queue shut
// until exactly this upgrade could start (reconcileScheduledUpgrade), so
// treating it as somebody else's decision would leave this upgrade with
// no release to call -- and a scheduled upgrade that then failed would
// leave the deployment paused with nothing left to lift it.
func TestAnUpgradeTakesOverTheHoldAScheduledUpgradeWasKeeping(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)

	if err := store.SetPipelinePause(ctx, model.PipelinePause{
		PausedAt: time.Now().UTC(),
		Reason:   "waiting for this deployment to finish its work before upgrading to main",
		By:       model.ScheduledUpgradePrincipal,
	}); err != nil {
		t.Fatalf("SetPipelinePause: %v", err)
	}

	release, err := orchestrator.UpgradeHold(store)("main")
	if err != nil {
		t.Fatalf("holding the queue for an upgrade: %v", err)
	}
	if release == nil {
		t.Fatal("an upgrade starting out of its own schedule's hold got no release back")
	}

	pause, err := store.GetPipelinePause(ctx)
	if err != nil {
		t.Fatalf("GetPipelinePause: %v", err)
	}
	if pause == nil || pause.By != orchestrator.UpgradePrincipal {
		t.Fatalf("the hold did not change hands: %+v", pause)
	}

	// And the release works, which is the whole point of having one: an
	// upgrade that fails here leaves a deployment that picks work up.
	release()
	if pause, err := store.GetPipelinePause(ctx); err != nil {
		t.Fatalf("GetPipelinePause: %v", err)
	} else if pause != nil {
		t.Errorf("still held after a failed upgrade let go: %q", pause.Reason)
	}
}
