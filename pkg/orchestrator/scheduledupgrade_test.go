package orchestrator_test

// An upgrade scheduled to happen once this deployment has finished what
// it is doing (grain/task-156), reached only through the exported
// RunCycle -- this package's tests never call an unexported reconciler
// directly. A cycle here needs nothing beyond a store, for the reason
// upgradecheck_test.go gives.

import (
	"errors"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// upgrades records what a cycle asked to be upgraded onto, standing in
// for the Upgrader a deployment hands orchestrator.Config.StartUpgrade.
// refuse, when set, is what Start says back -- the shape of an upgrade
// an operator already started by hand (upgrade.ErrUpgradeInProgress).
type upgrades struct {
	started []string
	refuse  error
}

func (u *upgrades) start(branch string) error {
	if u.refuse != nil {
		return u.refuse
	}
	u.started = append(u.started, branch)
	return nil
}

// runWithScheduledUpgrade runs one cycle against a store with an
// upgrader wired, and reports what that upgrader was asked to do.
func runWithScheduledUpgrade(t *testing.T, store *model.Store, up *upgrades, now time.Time) {
	t.Helper()
	deps := orchestrator.Deps{
		Store:  store,
		Config: orchestrator.Config{StartUpgrade: up.start},
	}
	if err := orchestrator.RunCycle(t.Context(), deps, now); err != nil {
		t.Fatalf("cycle: %v", err)
	}
}

// schedule asks for an upgrade of branch, the way POST /api/upgrade's
// own afterTasksFinish does.
func schedule(t *testing.T, store *model.Store, branch string, at time.Time) {
	t.Helper()
	if err := store.SetScheduledUpgrade(t.Context(), model.ScheduledUpgrade{
		Branch:      branch,
		RequestedAt: at,
		By:          model.Principal{Kind: model.PrincipalHuman, ID: "alice"},
	}); err != nil {
		t.Fatalf("scheduling an upgrade: %v", err)
	}
}

func scheduledUpgrade(t *testing.T, store *model.Store) *model.ScheduledUpgrade {
	t.Helper()
	scheduled, err := store.GetScheduledUpgrade(t.Context())
	if err != nil {
		t.Fatalf("reading the scheduled upgrade: %v", err)
	}
	return scheduled
}

func pauseNow(t *testing.T, store *model.Store) *model.PipelinePause {
	t.Helper()
	pause, err := store.GetPipelinePause(t.Context())
	if err != nil {
		t.Fatalf("reading the pause: %v", err)
	}
	return pause
}

// liveRun starts a run of a task and never finishes it -- a deployment
// with work in progress. interactive files the task as a console session
// instead, which is a live run nobody is waiting on.
func liveRun(t *testing.T, store *model.Store, id string, interactive bool, at time.Time) {
	t.Helper()
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	if err := store.PutTask(t.Context(), model.Task{
		ID: id, Intent: model.IntentImplement, Title: "work", Body: "please",
		Origin:      model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval:    &model.Attribution{Actor: human},
		Binding:     model.BindingDirective,
		Interactive: interactive,
	}); err != nil {
		t.Fatalf("filing task %s: %v", id, err)
	}
	if err := store.StartRun(t.Context(), model.Run{
		ID: id + "-1", TaskID: id, Attempt: 1, StartedAt: at,
	}, model.Limits{}); err != nil {
		t.Fatalf("starting a run for %s: %v", id, err)
	}
}

// TestAScheduledUpgradeHoldsTheQueueUntilTheWorkIsDone is the whole
// feature in one pass: the queue shuts the moment a schedule exists, the
// upgrade waits while a run is still going, and it starts -- with the
// schedule taken off the books -- the first cycle after that run ends.
func TestAScheduledUpgradeHoldsTheQueueUntilTheWorkIsDone(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	now := time.Now().UTC()

	liveRun(t, store, "t1", false, now)
	schedule(t, store, "main", now)

	runWithScheduledUpgrade(t, store, &upgrades{}, now.Add(time.Second))

	pause := pauseNow(t, store)
	if pause == nil {
		t.Fatal("a scheduled upgrade left the pipeline picking work up")
	}
	if pause.By != model.ScheduledUpgradePrincipal {
		t.Errorf("the queue is held by %+v, not the scheduled upgrade", pause.By)
	}
	if pause.UntilTaskID != "" {
		t.Errorf("the hold names task %q; it waits for the deployment, not for one task", pause.UntilTaskID)
	}
	if pause.Reason == "" {
		t.Error("the hold has no reason, so the banner has nothing to say")
	}

	// Still busy: nothing has been upgraded, and the schedule stands.
	up := &upgrades{}
	runWithScheduledUpgrade(t, store, up, now.Add(2*time.Second))
	if len(up.started) != 0 {
		t.Fatalf("upgraded %v while a run was still live", up.started)
	}
	if scheduledUpgrade(t, store) == nil {
		t.Fatal("the schedule was forgotten while it was still waiting")
	}

	if err := store.FinishRun(ctx, "t1-1", now.Add(time.Minute), "succeeded", ""); err != nil {
		t.Fatalf("finishing the run: %v", err)
	}

	runWithScheduledUpgrade(t, store, up, now.Add(2*time.Minute))
	if len(up.started) != 1 || up.started[0] != "main" {
		t.Fatalf("started %v, want one upgrade to main", up.started)
	}
	if scheduled := scheduledUpgrade(t, store); scheduled != nil {
		t.Errorf("the upgrade to %s is still scheduled after it started, so it would be applied twice",
			scheduled.Branch)
	}
	// The hold has changed hands: the upgrade owns it now, so its own
	// release -- or ReleaseUpgradeHold after the restart -- lifts it.
	if pause := pauseNow(t, store); pause == nil {
		t.Error("the queue opened as the upgrade started")
	} else if pause.By != orchestrator.UpgradePrincipal {
		t.Errorf("the hold is still %+v's; the upgrade should own it by now", pause.By)
	}
}

// TestAScheduledUpgradeDoesNotWaitForAConsoleSession: an interactive run
// is an agent waiting for somebody to type, which may never end. Waiting
// for one would mean a deployment with the Director open never upgrades.
func TestAScheduledUpgradeDoesNotWaitForAConsoleSession(t *testing.T) {
	t.Parallel()
	store, _ := openStore(t)
	now := time.Now().UTC()

	liveRun(t, store, "t1", true, now)
	schedule(t, store, "main", now)

	up := &upgrades{}
	runWithScheduledUpgrade(t, store, up, now.Add(time.Second))
	if len(up.started) != 1 {
		t.Fatalf("started %v, want the upgrade to go ahead with only a session open", up.started)
	}
}

// TestAScheduledUpgradeWaitsForGrainCI: a restart throws a CI run away
// too, and the branch it was checking has to be pushed again to get a
// verdict.
func TestAScheduledUpgradeWaitsForGrainCI(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	now := time.Now().UTC()

	if err := store.PutCIRun(ctx, model.CIRun{
		ID: "ci1", Key: "k1", Repo: "acme/widgets", Ref: "refs/heads/x",
		Commit: "deadbeef", Event: "push", StartedAt: now,
	}); err != nil {
		t.Fatalf("recording a CI run: %v", err)
	}
	schedule(t, store, "main", now)

	up := &upgrades{}
	runWithScheduledUpgrade(t, store, up, now.Add(time.Second))
	if len(up.started) != 0 {
		t.Fatalf("upgraded %v with a CI run still in flight", up.started)
	}

	if err := store.PutCIRun(ctx, model.CIRun{
		ID: "ci1", Key: "k1", Repo: "acme/widgets", Ref: "refs/heads/x",
		Commit: "deadbeef", Event: "push", StartedAt: now,
		FinishedAt: now.Add(time.Minute), Conclusion: "success",
	}); err != nil {
		t.Fatalf("concluding the CI run: %v", err)
	}
	runWithScheduledUpgrade(t, store, up, now.Add(2*time.Minute))
	if len(up.started) != 1 {
		t.Fatalf("started %v, want the upgrade once CI had concluded", up.started)
	}
}

// TestCancellingAScheduledUpgradeOpensTheQueue: the API only clears the
// row, so it is this reconciler that has to notice the hold it took is
// standing over nothing.
func TestCancellingAScheduledUpgradeOpensTheQueue(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	now := time.Now().UTC()

	liveRun(t, store, "t1", false, now)
	schedule(t, store, "main", now)
	runWithScheduledUpgrade(t, store, &upgrades{}, now.Add(time.Second))
	if pauseNow(t, store) == nil {
		t.Fatal("nothing held the queue, so there is nothing here to let go of")
	}

	if cancelled, err := store.ClearScheduledUpgrade(ctx); err != nil || !cancelled {
		t.Fatalf("cancelling: %v (cancelled %v)", err, cancelled)
	}
	runWithScheduledUpgrade(t, store, &upgrades{}, now.Add(2*time.Second))
	if pause := pauseNow(t, store); pause != nil {
		t.Errorf("the queue is still held (%s) after the upgrade was called off", pause.Reason)
	}
}

// TestAScheduledUpgradeRetakesTheHoldAfterARestart: ReleaseUpgradeHold
// runs at startup and cannot tell a scheduled upgrade's hold from an
// upgrade's own, so the hold has to be re-established by the first cycle
// -- which runs before dispatch, so nothing is picked up in the gap.
func TestAScheduledUpgradeRetakesTheHoldAfterARestart(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	now := time.Now().UTC()

	liveRun(t, store, "t1", false, now)
	schedule(t, store, "main", now)
	runWithScheduledUpgrade(t, store, &upgrades{}, now.Add(time.Second))

	if _, err := store.ClearPipelinePause(ctx); err != nil {
		t.Fatalf("lifting the hold the way a restart would: %v", err)
	}
	runWithScheduledUpgrade(t, store, &upgrades{}, now.Add(2*time.Second))
	if pause := pauseNow(t, store); pause == nil {
		t.Error("the queue was left open with an upgrade still waiting for it to empty")
	} else if pause.By != model.ScheduledUpgradePrincipal {
		t.Errorf("the queue is held by %+v, not the scheduled upgrade", pause.By)
	}
}

// TestAnUpgradeThatWillNotStartStaysScheduled: the one error worth
// retrying is an upgrade an operator already started by hand, which ends
// in a restart that would have discarded this schedule anyway -- but if
// it fails instead, the deployment is still idle and this should still
// happen.
func TestAnUpgradeThatWillNotStartStaysScheduled(t *testing.T) {
	t.Parallel()
	store, _ := openStore(t)
	now := time.Now().UTC()

	schedule(t, store, "main", now)
	up := &upgrades{refuse: errors.New("an upgrade is already in progress")}
	runWithScheduledUpgrade(t, store, up, now.Add(time.Second))

	scheduled := scheduledUpgrade(t, store)
	if scheduled == nil {
		t.Fatal("an upgrade that could not be started was forgotten")
	}
	if scheduled.Branch != "main" {
		t.Errorf("the schedule came back as %q", scheduled.Branch)
	}
	if pauseNow(t, store) == nil {
		t.Error("the queue opened while an upgrade was still waiting to be applied")
	}

	up.refuse = nil
	runWithScheduledUpgrade(t, store, up, now.Add(2*time.Second))
	if len(up.started) != 1 {
		t.Fatalf("started %v, want the upgrade on the retry", up.started)
	}
}

// TestAScheduledUpgradeLeavesAnOperatorsPauseAlone: the queue is shut
// either way, so there is nothing to gain from replacing it -- and
// replacing it would mean this reconciler lifting, when the schedule
// ends, a pause that was never its own.
func TestAScheduledUpgradeLeavesAnOperatorsPauseAlone(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	now := time.Now().UTC()

	operator := model.PipelinePause{
		PausedAt: now, Reason: "patching the host",
		By: model.Principal{Kind: model.PrincipalHuman, ID: "alice"},
	}
	if err := store.SetPipelinePause(ctx, operator); err != nil {
		t.Fatalf("pausing: %v", err)
	}
	schedule(t, store, "main", now)

	up := &upgrades{}
	runWithScheduledUpgrade(t, store, up, now.Add(time.Second))
	if pause := pauseNow(t, store); pause == nil || pause.Reason != operator.Reason {
		t.Fatalf("the operator's pause became %+v", pause)
	}
	if len(up.started) != 1 {
		t.Fatalf("started %v, want the upgrade to go ahead on an idle deployment", up.started)
	}
	// And the pause it did not take is the pause it does not relabel:
	// whatever happens to this upgrade, resuming is still the operator's.
	if pause := pauseNow(t, store); pause.By.Kind != model.PrincipalHuman {
		t.Errorf("the operator's pause was taken over by %+v", pause.By)
	}
}

// TestNoUpgraderMeansNoScheduleIsEverActedOn: a deployment configured
// for neither upgrade pipeline has nothing to apply, and holding its
// queue for an upgrade that can never happen would stop it working for
// good.
func TestNoUpgraderMeansNoScheduleIsEverActedOn(t *testing.T) {
	t.Parallel()
	store, _ := openStore(t)
	now := time.Now().UTC()

	schedule(t, store, "main", now)
	deps := orchestrator.Deps{Store: store}
	if err := orchestrator.RunCycle(t.Context(), deps, now.Add(time.Second)); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if pause := pauseNow(t, store); pause != nil {
		t.Errorf("a deployment that cannot upgrade held its queue anyway (%s)", pause.Reason)
	}
}

func TestScheduledUpgradeChannelSelection(t *testing.T) {
	t.Parallel()
	channels := []string{"latest", "v1.2.0", "rc/v1.2", "candidate", "main"}
	for _, channel := range channels {
		t.Run(channel, func(t *testing.T) {
			store, _ := openStore(t)
			now := time.Now().UTC()

			schedule(t, store, channel, now)
			up := &upgrades{}
			runWithScheduledUpgrade(t, store, up, now.Add(time.Second))

			if len(up.started) != 1 || up.started[0] != channel {
				t.Fatalf("started upgrades = %v, want [%s]", up.started, channel)
			}
			if scheduled := scheduledUpgrade(t, store); scheduled != nil {
				t.Errorf("scheduled upgrade still present: %+v", scheduled)
			}
		})
	}
}

// TestAScheduledUpgradeProceedsAfterDrainWindowElapses verifies that if an upgrade
// drain window is configured and has elapsed, the upgrade proceeds even if runs are active.
func TestAScheduledUpgradeProceedsAfterDrainWindowElapses(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	now := time.Now().UTC()

	liveRun(t, store, "t1", false, now)
	schedule(t, store, "main", now)

	up := &upgrades{}
	drainWindow := 5 * time.Minute

	// Cycle before drain window: should hold queue and not start upgrade.
	deps := orchestrator.Deps{
		Store: store,
		Config: orchestrator.Config{
			StartUpgrade:       up.start,
			UpgradeDrainWindow: drainWindow,
		},
	}
	if err := orchestrator.RunCycle(ctx, deps, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if len(up.started) != 0 {
		t.Fatalf("started %v before drain window elapsed", up.started)
	}

	// Cycle after drain window: should proceed with upgrade.
	if err := orchestrator.RunCycle(ctx, deps, now.Add(6*time.Minute)); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if len(up.started) != 1 || up.started[0] != "main" {
		t.Fatalf("started %v, want upgrade to proceed after drain window elapsed", up.started)
	}
}
