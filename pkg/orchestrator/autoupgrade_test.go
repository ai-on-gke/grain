package orchestrator_test

// The daily automatic upgrade (model.Config.AutoUpgradeChannel and
// AutoUpgradeTime), reached through RunCycle like the scheduled upgrade
// it is built on (scheduledupgrade_test.go, whose helpers these share).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// autoUpgradeOn configures the daily upgrade onto channel at hhmm in zone.
func autoUpgradeOn(t *testing.T, store *model.Store, channel, hhmm, zone string) {
	t.Helper()
	cfg := model.DefaultConfig()
	cfg.AutoUpgradeChannel = channel
	cfg.AutoUpgradeTime = hhmm
	cfg.TimeZone = zone
	if err := store.PutConfig(t.Context(), cfg); err != nil {
		t.Fatalf("configuring the automatic upgrade: %v", err)
	}
}

func TestTheAutomaticUpgradeFiresOnceADayAtItsTime(t *testing.T) {
	t.Parallel()
	store, _ := openStore(t)
	autoUpgradeOn(t, store, "latest", "03:00", "UTC")
	day := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)

	up := &upgrades{}
	runWithScheduledUpgrade(t, store, up, day.Add(2*time.Hour+59*time.Minute))
	if len(up.started) != 0 || scheduledUpgrade(t, store) != nil {
		t.Fatalf("upgraded %v before the time of day came", up.started)
	}

	// Idle at three: the upgrade is scheduled and, with nothing to wait
	// for, started on the same tick.
	runWithScheduledUpgrade(t, store, up, day.Add(3*time.Hour))
	if len(up.started) != 1 || up.started[0] != "latest" {
		t.Fatalf("at the time of day the deployment upgraded onto %v, want [latest]", up.started)
	}
	if scheduledUpgrade(t, store) != nil {
		t.Error("the schedule outlived the upgrade it started")
	}

	// The rest of the day -- including the deployment the upgrade
	// restarted, which is a fresh upgrader reading the same store.
	again := &upgrades{}
	for _, at := range []time.Duration{3*time.Hour + time.Minute, 9 * time.Hour, 23*time.Hour + 59*time.Minute} {
		runWithScheduledUpgrade(t, store, again, day.Add(at))
	}
	if len(again.started) != 0 {
		t.Fatalf("the same day's upgrade fired again: %v", again.started)
	}

	// Tomorrow it fires again, at its time and not before.
	runWithScheduledUpgrade(t, store, again, day.Add(26*time.Hour))
	if len(again.started) != 0 {
		t.Fatalf("upgraded %v before tomorrow's time", again.started)
	}
	runWithScheduledUpgrade(t, store, again, day.Add(27*time.Hour))
	if len(again.started) != 1 {
		t.Fatalf("tomorrow's upgrade did not fire: %v", again.started)
	}

	run, err := store.GetAutoUpgradeRun(t.Context())
	if err != nil {
		t.Fatalf("reading the last automatic upgrade: %v", err)
	}
	if run == nil || run.Day != "2026-10-27" || run.Branch != "latest" {
		t.Errorf("the last automatic upgrade is recorded as %+v, want 2026-10-27 onto latest", run)
	}
}

// A busy deployment drains first: the automatic upgrade is a scheduled
// one, held for the runs in flight, and attributed to the automation.
func TestTheAutomaticUpgradeWaitsForRunsInFlight(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	autoUpgradeOn(t, store, "candidate", "03:00", "UTC")
	at := time.Date(2026, 10, 26, 3, 0, 0, 0, time.UTC)
	liveRun(t, store, "t1", false, at.Add(-time.Hour))

	up := &upgrades{}
	runWithScheduledUpgrade(t, store, up, at)
	if len(up.started) != 0 {
		t.Fatalf("upgraded %v over a live run", up.started)
	}
	scheduled := scheduledUpgrade(t, store)
	if scheduled == nil || scheduled.Branch != "candidate" {
		t.Fatalf("scheduled %+v, want an upgrade onto candidate", scheduled)
	}
	if scheduled.By != model.AutoUpgradePrincipal {
		t.Errorf("the upgrade is scheduled by %+v, not the automatic upgrade", scheduled.By)
	}
	if pause := pauseNow(t, store); pause == nil || pause.By != model.ScheduledUpgradePrincipal {
		t.Errorf("the queue is held by %+v, want the scheduled upgrade's hold", pause)
	}

	if err := store.FinishRun(ctx, "t1-1", at.Add(time.Minute), "succeeded", ""); err != nil {
		t.Fatalf("finishing the run: %v", err)
	}
	runWithScheduledUpgrade(t, store, up, at.Add(2*time.Minute))
	if len(up.started) != 1 || up.started[0] != "candidate" {
		t.Fatalf("once idle the deployment upgraded onto %v, want [candidate]", up.started)
	}
}

// An upgrade somebody already scheduled is the one the day gets: the
// automatic one neither replaces its branch nor fires behind it.
func TestTheAutomaticUpgradeLeavesAnOperatorsScheduleAlone(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	autoUpgradeOn(t, store, "latest", "03:00", "UTC")
	at := time.Date(2026, 10, 26, 3, 0, 0, 0, time.UTC)
	liveRun(t, store, "t1", false, at.Add(-time.Hour))
	schedule(t, store, "main", at.Add(-time.Minute))

	up := &upgrades{}
	runWithScheduledUpgrade(t, store, up, at)
	if scheduled := scheduledUpgrade(t, store); scheduled == nil || scheduled.Branch != "main" {
		t.Fatalf("the operator's schedule became %+v", scheduled)
	}
	if err := store.FinishRun(ctx, "t1-1", at.Add(time.Minute), "succeeded", ""); err != nil {
		t.Fatalf("finishing the run: %v", err)
	}
	runWithScheduledUpgrade(t, store, up, at.Add(2*time.Minute))
	runWithScheduledUpgrade(t, store, up, at.Add(3*time.Minute))
	if len(up.started) != 1 || up.started[0] != "main" {
		t.Fatalf("upgraded onto %v, want the operator's [main] and nothing after it", up.started)
	}
}

// The time of day is on the deployment's own clock.
func TestTheAutomaticUpgradeReadsTheDeploymentsTimeZone(t *testing.T) {
	t.Parallel()
	store, _ := openStore(t)
	autoUpgradeOn(t, store, "main", "03:00", "America/New_York")

	up := &upgrades{}
	// 06:59 UTC is 02:59 in New York on 26 October 2026 (EDT, UTC-4).
	runWithScheduledUpgrade(t, store, up, time.Date(2026, 10, 26, 6, 59, 0, 0, time.UTC))
	if len(up.started) != 0 {
		t.Fatalf("upgraded %v at 02:59 New York time", up.started)
	}
	runWithScheduledUpgrade(t, store, up, time.Date(2026, 10, 26, 7, 0, 0, 0, time.UTC))
	if len(up.started) != 1 {
		t.Fatalf("did not upgrade at 03:00 New York time: %v", up.started)
	}
}

func TestTheAutomaticUpgradeIsOffWithoutAChannel(t *testing.T) {
	t.Parallel()
	store, _ := openStore(t)
	autoUpgradeOn(t, store, "", "03:00", "UTC")

	up := &upgrades{}
	runWithScheduledUpgrade(t, store, up, time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC))
	if len(up.started) != 0 || scheduledUpgrade(t, store) != nil {
		t.Fatalf("an automatic upgrade with no channel upgraded onto %v", up.started)
	}
}

// runWithProbe is runWithScheduledUpgrade with Config.UpgradeUnchanged
// wired to probe.
func runWithProbe(t *testing.T, store *model.Store, up *upgrades, probe func(context.Context, string) (bool, error), now time.Time) {
	t.Helper()
	deps := orchestrator.Deps{
		Store:  store,
		Config: orchestrator.Config{StartUpgrade: up.start, UpgradeUnchanged: probe},
	}
	if err := orchestrator.RunCycle(t.Context(), deps, now); err != nil {
		t.Fatalf("cycle: %v", err)
	}
}

// A channel whose build is the one already running is the day done: no
// schedule, so no hold on a busy deployment, and no upgrade tomorrow
// morning's tick has to repeat.
func TestTheAutomaticUpgradeSkipsAChannelWithNothingNew(t *testing.T) {
	t.Parallel()
	store, _ := openStore(t)
	autoUpgradeOn(t, store, "candidate", "03:00", "UTC")
	at := time.Date(2026, 10, 26, 3, 0, 0, 0, time.UTC)
	liveRun(t, store, "t1", false, at.Add(-time.Hour))

	var asked []string
	probe := func(_ context.Context, branch string) (bool, error) {
		asked = append(asked, branch)
		return true, nil
	}
	up := &upgrades{}
	runWithProbe(t, store, up, probe, at)
	if len(asked) != 1 || asked[0] != "candidate" {
		t.Fatalf("the probe was asked about %v, want [candidate]", asked)
	}
	if scheduled := scheduledUpgrade(t, store); scheduled != nil {
		t.Fatalf("scheduled %+v for a channel with nothing new", scheduled)
	}
	if pause := pauseNow(t, store); pause != nil {
		t.Errorf("the queue is held by %+v for a channel with nothing new", pause)
	}
	if len(up.started) != 0 {
		t.Errorf("upgraded onto %v", up.started)
	}
	run, err := store.GetAutoUpgradeRun(t.Context())
	if err != nil {
		t.Fatalf("reading the last automatic upgrade: %v", err)
	}
	if run == nil || run.Day != "2026-10-26" || run.Branch != "candidate" {
		t.Errorf("the day is recorded as %+v, want 2026-10-26 onto candidate", run)
	}

	runWithProbe(t, store, up, probe, at.Add(time.Hour))
	if len(asked) != 1 {
		t.Errorf("the same day asked again: %v", asked)
	}
}

// A probe that says the channel moved, or cannot say, schedules exactly
// as a deployment with no probe does.
func TestTheAutomaticUpgradeSchedulesWhenTheProbeSaysNoOrFails(t *testing.T) {
	t.Parallel()
	for name, probe := range map[string]func(context.Context, string) (bool, error){
		"changed": func(context.Context, string) (bool, error) { return false, nil },
		"error":   func(context.Context, string) (bool, error) { return true, errors.New("registry unreachable") },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store, _ := openStore(t)
			autoUpgradeOn(t, store, "candidate", "03:00", "UTC")
			at := time.Date(2026, 10, 26, 3, 0, 0, 0, time.UTC)
			liveRun(t, store, "t1", false, at.Add(-time.Hour))

			runWithProbe(t, store, &upgrades{}, probe, at)
			if scheduled := scheduledUpgrade(t, store); scheduled == nil || scheduled.Branch != "candidate" {
				t.Fatalf("scheduled %+v, want an upgrade onto candidate", scheduled)
			}
			if pause := pauseNow(t, store); pause == nil || pause.By != model.ScheduledUpgradePrincipal {
				t.Errorf("the queue is held by %+v, want the scheduled upgrade's hold", pause)
			}
		})
	}
}

// A probe that does not answer in time does not hold the tick: the
// upgrade is scheduled as if it had not been asked. Not parallel, for
// SetAutoUpgradeProbeTimeout.
func TestTheAutomaticUpgradeDoesNotWaitOnASlowProbe(t *testing.T) {
	defer orchestrator.SetAutoUpgradeProbeTimeout(50 * time.Millisecond)()
	store, _ := openStore(t)
	autoUpgradeOn(t, store, "candidate", "03:00", "UTC")
	at := time.Date(2026, 10, 26, 3, 0, 0, 0, time.UTC)
	liveRun(t, store, "t1", false, at.Add(-time.Hour))

	release := make(chan struct{})
	defer close(release)
	// Ignores its context, as a hung probe would.
	probe := func(context.Context, string) (bool, error) {
		<-release
		return true, nil
	}
	start := time.Now()
	runWithProbe(t, store, &upgrades{}, probe, at)
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("the cycle waited %s on the probe", took)
	}
	if scheduled := scheduledUpgrade(t, store); scheduled == nil || scheduled.Branch != "candidate" {
		t.Fatalf("scheduled %+v, want an upgrade onto candidate", scheduled)
	}
}
