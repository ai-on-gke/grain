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

// The release-triggered automatic upgrade (model.AutoUpgradeOnRelease),
// reached through RunCycle like the daily one (autoupgrade_test.go) and
// the scheduled upgrade both are built on (scheduledupgrade_test.go,
// whose helpers these share).

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/upgrade"
)

// releaseUpgradeOn turns on upgrading when a release is published,
// following channel ("latest", or "" for the exact tag).
func releaseUpgradeOn(t *testing.T, store *model.Store, channel string) {
	t.Helper()
	cfg := model.DefaultConfig()
	cfg.AutoUpgradeOn = model.AutoUpgradeOnRelease
	cfg.AutoUpgradeChannel = channel
	if err := store.PutConfig(t.Context(), cfg); err != nil {
		t.Fatalf("configuring the release-triggered upgrade: %v", err)
	}
}

// releaseDeployment is a deployment's upgrader as the release-triggered
// upgrade sees it: the newest release on its registry, the upgrades it
// was asked to start, and the status the last of them ended in.
type releaseDeployment struct {
	newest  string
	started []string
	status  upgrade.Status
}

func (d *releaseDeployment) deps(store *model.Store, now time.Time) orchestrator.Deps {
	return orchestrator.Deps{
		Store: store,
		Config: orchestrator.Config{
			StartUpgrade: func(branch string) error {
				d.started = append(d.started, branch)
				started := now
				d.status = upgrade.Status{Branch: branch, Phase: upgrade.PhaseRunning, StartedAt: &started}
				return nil
			},
			UpgradeStatus: func() (upgrade.Status, error) { return d.status, nil },
			LatestRelease: func(context.Context) (string, error) { return d.newest, nil },
		},
	}
}

func (d *releaseDeployment) cycle(t *testing.T, store *model.Store, now time.Time) {
	t.Helper()
	if err := orchestrator.RunCycle(t.Context(), d.deps(store, now), now); err != nil {
		t.Fatalf("cycle: %v", err)
	}
}

// end finishes the upgrade in flight as phase, failing step with detail
// when it failed.
func (d *releaseDeployment) end(phase upgrade.Phase, step, detail string) {
	d.status.Phase = phase
	if phase == upgrade.PhaseFailed {
		d.status.Detail = detail
		d.status.Steps = []upgrade.Step{
			{Name: "pull image", State: upgrade.StepDone},
			{Name: step, State: upgrade.StepFailed, Detail: detail},
			{Name: "restart", State: upgrade.StepPending},
		}
	}
}

func openUpgradeFailures(t *testing.T, store *model.Store) []model.Notification {
	t.Helper()
	open, err := store.ListNotifications(t.Context(), model.NotificationFilter{Open: true, Family: model.FamilyUpgradeFailed})
	if err != nil {
		t.Fatalf("listing notifications: %v", err)
	}
	return open
}

// A release appears: once it has stood for the settle window an upgrade
// onto it is scheduled, once, by the automation; the queue drains; the
// upgrade starts when the last run finishes; and nothing is scheduled
// again for the same release, on this deployment or the one the
// upgrade restarts.
func TestAPublishedReleaseIsScheduledOnceDrainsAndCutsOver(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	releaseUpgradeOn(t, store, "latest")
	t0 := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	liveRun(t, store, "t1", false, t0.Add(-time.Hour))

	d := &releaseDeployment{newest: "v3.6.0"}
	d.cycle(t, store, t0)
	d.cycle(t, store, t0.Add(59*time.Minute))
	if scheduledUpgrade(t, store) != nil || len(d.started) != 0 {
		t.Fatalf("acted on v3.6.0 before it had settled: started %v", d.started)
	}

	d.cycle(t, store, t0.Add(time.Hour))
	scheduled := scheduledUpgrade(t, store)
	if scheduled == nil || scheduled.Branch != "latest" || scheduled.By != model.AutoUpgradePrincipal {
		t.Fatalf("scheduled %+v, want the automation's upgrade onto latest", scheduled)
	}
	if pause := pauseNow(t, store); pause == nil || pause.By != model.ScheduledUpgradePrincipal {
		t.Errorf("the queue is held by %+v, want the scheduled upgrade's hold", pause)
	}
	if len(d.started) != 0 {
		t.Fatalf("upgraded %v over a live run", d.started)
	}

	if err := store.FinishRun(ctx, "t1-1", t0.Add(61*time.Minute), "succeeded", ""); err != nil {
		t.Fatalf("finishing the run: %v", err)
	}
	d.cycle(t, store, t0.Add(62*time.Minute))
	if len(d.started) != 1 || d.started[0] != "latest" {
		t.Fatalf("once drained the deployment upgraded onto %v, want [latest]", d.started)
	}
	d.end(upgrade.PhaseOK, "", "")

	// The rest of the day, and the deployment the restart brings up.
	for _, at := range []time.Duration{63 * time.Minute, 3 * time.Hour, 30 * time.Hour} {
		d.cycle(t, store, t0.Add(at))
	}
	if len(d.started) != 1 || scheduledUpgrade(t, store) != nil {
		t.Fatalf("the same release was upgraded to again: %v", d.started)
	}
	record, err := store.GetAutoUpgradeRelease(ctx)
	if err != nil {
		t.Fatalf("reading the record: %v", err)
	}
	if record == nil || record.Release != "v3.6.0" || record.Outcome != model.AutoUpgradeSucceeded {
		t.Errorf("recorded %+v, want v3.6.0 upgraded to successfully", record)
	}
}

// Two releases an hour apart give one drain, onto the later one: the
// first has not settled when the second replaces it.
func TestTwoReleasesCloseTogetherGiveOneDrain(t *testing.T) {
	t.Parallel()
	store, _ := openStore(t)
	releaseUpgradeOn(t, store, "")
	t0 := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)

	d := &releaseDeployment{newest: "v3.6.0"}
	d.cycle(t, store, t0)
	d.newest = "v3.6.1"
	d.cycle(t, store, t0.Add(50*time.Minute))
	d.cycle(t, store, t0.Add(time.Hour))
	if len(d.started) != 0 {
		t.Fatalf("upgraded onto %v while the next release was still settling", d.started)
	}
	for _, at := range []time.Duration{110 * time.Minute, 2 * time.Hour, 5 * time.Hour} {
		d.cycle(t, store, t0.Add(at))
		d.end(upgrade.PhaseOK, "", "")
	}
	if len(d.started) != 1 || d.started[0] != "v3.6.1" {
		t.Fatalf("upgraded onto %v, want one upgrade onto the exact tag v3.6.1", d.started)
	}
}

// A release published while an automatic upgrade is still draining
// re-points that drain rather than queueing another behind it, and keeps
// its place in the drain window.
func TestANewerReleaseRepointsAnAutomaticDrain(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	releaseUpgradeOn(t, store, "")
	t0 := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	liveRun(t, store, "t1", false, t0.Add(-time.Hour))

	d := &releaseDeployment{newest: "v3.6.0"}
	d.cycle(t, store, t0)
	d.cycle(t, store, t0.Add(time.Hour))
	first := scheduledUpgrade(t, store)
	if first == nil || first.Branch != "v3.6.0" {
		t.Fatalf("scheduled %+v, want v3.6.0", first)
	}

	d.newest = "v3.6.1"
	d.cycle(t, store, t0.Add(70*time.Minute))
	d.cycle(t, store, t0.Add(130*time.Minute))
	again := scheduledUpgrade(t, store)
	if again == nil || again.Branch != "v3.6.1" || !again.RequestedAt.Equal(first.RequestedAt) {
		t.Fatalf("after v3.6.1 settled the schedule is %+v, want v3.6.1 requested at %s", again, first.RequestedAt)
	}

	if err := store.FinishRun(ctx, "t1-1", t0.Add(131*time.Minute), "succeeded", ""); err != nil {
		t.Fatalf("finishing the run: %v", err)
	}
	d.cycle(t, store, t0.Add(132*time.Minute))
	if len(d.started) != 1 || d.started[0] != "v3.6.1" {
		t.Fatalf("upgraded onto %v, want one upgrade onto v3.6.1", d.started)
	}
}

// A schema rehearsal that fails raises one notification naming the stage
// and quoting its output, and the release is not tried again however
// many ticks pass -- only a newer release is, and its success clears the
// notification.
func TestAFailedSchemaRehearsalNotifiesOnceAndWaitsForANewRelease(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	releaseUpgradeOn(t, store, "latest")
	t0 := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)

	d := &releaseDeployment{newest: "v3.6.0"}
	d.cycle(t, store, t0)
	d.cycle(t, store, t0.Add(time.Hour))
	if len(d.started) != 1 {
		t.Fatalf("started %v, want one upgrade onto latest", d.started)
	}
	d.end(upgrade.PhaseFailed, "schema",
		"grain state check -migrate: migrating schema 97 to 98: no such column: auto_upgrade_on")

	for i := 0; i < 20; i++ {
		d.cycle(t, store, t0.Add(time.Hour+time.Duration(i+1)*time.Minute))
	}
	if len(d.started) != 1 {
		t.Fatalf("the failed release was retried: %v", d.started)
	}
	open := openUpgradeFailures(t, store)
	if len(open) != 1 {
		t.Fatalf("%d upgrade.failed notifications open, want 1", len(open))
	}
	n := open[0]
	if n.Subject != "upgrade.failed:v3.6.0" || n.RaiseCount != 1 {
		t.Errorf("raised %s %d times, want upgrade.failed:v3.6.0 once", n.Subject, n.RaiseCount)
	}
	if !strings.Contains(n.Title, "schema rehearsal") || !strings.Contains(n.Body, "schema rehearsal") ||
		!strings.Contains(n.Body, "no such column: auto_upgrade_on") {
		t.Errorf("the notification does not name the stage and its output:\n%s\n%s", n.Title, n.Body)
	}

	// Dismissed by a person, it stays dismissed: the failure is reported
	// once, not once per tick.
	if _, err := store.ClearNotification(ctx, n.Subject, "human:alice"); err != nil {
		t.Fatalf("clearing: %v", err)
	}
	d.cycle(t, store, t0.Add(3*time.Hour))
	if len(openUpgradeFailures(t, store)) != 0 {
		t.Fatal("the failure was raised again after it was dismissed")
	}
	// A newer release is a new target: tried once it settles, and its
	// success clears whatever failure is still open.
	if _, err := store.RaiseNotification(ctx, model.Notification{
		Subject: n.Subject, Severity: model.NotificationWarning, Title: n.Title, RaisedBy: n.RaisedBy, RaisedAt: t0,
	}); err != nil {
		t.Fatalf("re-raising: %v", err)
	}
	d.newest = "v3.6.1"
	d.cycle(t, store, t0.Add(4*time.Hour))
	d.cycle(t, store, t0.Add(5*time.Hour))
	if len(d.started) != 2 || d.started[1] != "latest" {
		t.Fatalf("started %v, want the newer release tried", d.started)
	}
	d.end(upgrade.PhaseOK, "", "")
	d.cycle(t, store, t0.Add(5*time.Hour+time.Minute))
	if open := openUpgradeFailures(t, store); len(open) != 0 {
		t.Errorf("a successful upgrade left %d upgrade.failed notifications open", len(open))
	}
}

// A release the deployment already runs is recorded without holding the
// queue, and the daily schedule does not fire in release mode.
func TestAReleaseAlreadyRunningHoldsNothing(t *testing.T) {
	t.Parallel()
	store, _ := openStore(t)
	cfg := model.DefaultConfig()
	cfg.AutoUpgradeOn = model.AutoUpgradeOnRelease
	cfg.AutoUpgradeChannel = "latest"
	cfg.AutoUpgradeTime = "03:00"
	if err := store.PutConfig(t.Context(), cfg); err != nil {
		t.Fatalf("configuring: %v", err)
	}
	t0 := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	liveRun(t, store, "t1", false, t0.Add(-time.Hour))

	d := &releaseDeployment{newest: "v3.6.0"}
	run := func(at time.Time) {
		deps := d.deps(store, at)
		deps.Config.UpgradeUnchanged = func(context.Context, string) (bool, error) { return true, nil }
		if err := orchestrator.RunCycle(t.Context(), deps, at); err != nil {
			t.Fatalf("cycle: %v", err)
		}
	}
	run(t0)
	run(t0.Add(time.Hour))
	if scheduledUpgrade(t, store) != nil || pauseNow(t, store) != nil {
		t.Fatal("held the queue for a release this deployment already runs")
	}
	record, err := store.GetAutoUpgradeRelease(t.Context())
	if err != nil {
		t.Fatalf("reading the record: %v", err)
	}
	if record == nil || record.Release != "v3.6.0" || record.Outcome != model.AutoUpgradeUnchanged {
		t.Errorf("recorded %+v, want v3.6.0 unchanged", record)
	}
	if run, err := store.GetAutoUpgradeRun(t.Context()); err != nil || run != nil {
		t.Errorf("the daily upgrade fired in release mode: %+v, %v", run, err)
	}
}

// With autoUpgradeMinIntervalHours > 0 (default 6), releases cut faster than
// that after a completed automatic upgrade wait until the interval has passed
// since it finished, then drain once straight onto whatever release is newest
// then -- and a release published inside the last hour of the interval still
// waits out its own settle window.
func TestMinIntervalCoalescesReleasesAfterACompletedUpgrade(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	releaseUpgradeOn(t, store, "")
	t0 := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)

	d := &releaseDeployment{newest: "v3.6.0"}
	d.cycle(t, store, t0)
	d.cycle(t, store, t0.Add(time.Hour))
	if len(d.started) != 1 || d.started[0] != "v3.6.0" {
		t.Fatalf("started %v, want [v3.6.0]", d.started)
	}
	finished := t0.Add(time.Hour + 5*time.Minute)
	d.end(upgrade.PhaseOK, "", "")
	d.status.FinishedAt = &finished
	d.cycle(t, store, t0.Add(time.Hour+6*time.Minute))

	// v3.6.1 at +2h (settled at +3h) and v3.6.2 at +4h (settled at +5h)
	// both arrive before finished + 6h (+7h05m): neither schedules a drain.
	d.newest = "v3.6.1"
	for _, at := range []time.Duration{2 * time.Hour, 3 * time.Hour, 3*time.Hour + 30*time.Minute} {
		d.cycle(t, store, t0.Add(at))
	}
	if len(d.started) != 1 || scheduledUpgrade(t, store) != nil {
		t.Fatalf("scheduled or started inside the 6h interval for v3.6.1: started=%v scheduled=%+v",
			d.started, scheduledUpgrade(t, store))
	}

	d.newest = "v3.6.2"
	for _, at := range []time.Duration{4 * time.Hour, 5 * time.Hour, 7*time.Hour + 4*time.Minute} {
		d.cycle(t, store, t0.Add(at))
	}
	if len(d.started) != 1 || scheduledUpgrade(t, store) != nil {
		t.Fatalf("scheduled or started before finished + 6h: started=%v scheduled=%+v",
			d.started, scheduledUpgrade(t, store))
	}

	// At finished + 6h (+7h05m), v3.6.2 has already stood for >3h: one
	// upgrade goes straight to v3.6.2, skipping v3.6.1 completely.
	d.cycle(t, store, t0.Add(7*time.Hour+5*time.Minute))
	if len(d.started) != 2 || d.started[1] != "v3.6.2" {
		t.Fatalf("at finished + 6h started %v, want [v3.6.0 v3.6.2]", d.started)
	}
	secondFinished := t0.Add(7*time.Hour + 10*time.Minute)
	d.end(upgrade.PhaseOK, "", "")
	d.status.FinishedAt = &secondFinished
	d.cycle(t, store, t0.Add(7*time.Hour+11*time.Minute))

	// v3.6.3 arrives 20 minutes before the next 6h interval ends (+12h50m vs
	// +13h10m): it still waits out its full 1h settle window until +13h50m.
	d.newest = "v3.6.3"
	d.cycle(t, store, t0.Add(12*time.Hour+50*time.Minute))
	d.cycle(t, store, t0.Add(13*time.Hour+10*time.Minute))
	if len(d.started) != 2 {
		t.Fatalf("acted on v3.6.3 before its 1h settle window: %v", d.started)
	}
	d.cycle(t, store, t0.Add(13*time.Hour+50*time.Minute))
	if len(d.started) != 3 || d.started[2] != "v3.6.3" {
		t.Fatalf("after both interval and settle started %v, want v3.6.3 third", d.started)
	}

	record, err := store.GetAutoUpgradeRelease(ctx)
	if err != nil {
		t.Fatalf("reading the record: %v", err)
	}
	if record == nil || record.SucceededAt == nil || !record.SucceededAt.Equal(secondFinished) {
		t.Errorf("record.SucceededAt = %v, want %s while third upgrade is still running",
			record, secondFinished)
	}
}

// Setting autoUpgradeMinIntervalHours to 0 turns the minimum interval off,
// and failed or unchanged releases do not start or reset a cooldown.
func TestMinIntervalZeroTurnsItOffAndFailedOrUnchangedDoNotResetIt(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	cfg := model.DefaultConfig()
	cfg.AutoUpgradeOn = model.AutoUpgradeOnRelease
	cfg.AutoUpgradeChannel = ""
	cfg.AutoUpgradeMinIntervalHours = 0
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("configuring: %v", err)
	}
	t0 := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)

	d := &releaseDeployment{newest: "v3.6.0"}
	d.cycle(t, store, t0)
	d.cycle(t, store, t0.Add(time.Hour))
	d.end(upgrade.PhaseOK, "", "")
	d.cycle(t, store, t0.Add(time.Hour+time.Minute))

	// With 0 (off), v3.6.1 published 30m later upgrades as soon as it settles.
	d.newest = "v3.6.1"
	d.cycle(t, store, t0.Add(90*time.Minute))
	d.cycle(t, store, t0.Add(150*time.Minute))
	if len(d.started) != 2 || d.started[1] != "v3.6.1" {
		t.Fatalf("with min interval 0 started %v, want [v3.6.0 v3.6.1]", d.started)
	}
	succeededAt := t0.Add(151 * time.Minute)
	d.end(upgrade.PhaseOK, "", "")
	d.status.FinishedAt = &succeededAt
	d.cycle(t, store, succeededAt)

	// Turn on a 4h minimum interval; after 4h, v3.6.2 runs and fails. The
	// failure must not reset SucceededAt, so v3.6.3 an hour later runs once
	// settled without waiting another 4h.
	cfg.AutoUpgradeMinIntervalHours = 4
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("enabling 4h interval: %v", err)
	}
	d.newest = "v3.6.2"
	d.cycle(t, store, succeededAt.Add(3*time.Hour))
	d.cycle(t, store, succeededAt.Add(4*time.Hour))
	if len(d.started) != 3 || d.started[2] != "v3.6.2" {
		t.Fatalf("after 4h started %v, want v3.6.2", d.started)
	}
	d.end(upgrade.PhaseFailed, "pull image", "manifest unknown")
	d.cycle(t, store, succeededAt.Add(4*time.Hour+time.Minute))

	d.newest = "v3.6.3"
	d.cycle(t, store, succeededAt.Add(4*time.Hour+10*time.Minute))
	d.cycle(t, store, succeededAt.Add(5*time.Hour+10*time.Minute))
	if len(d.started) != 4 || d.started[3] != "v3.6.3" {
		t.Fatalf("after failed v3.6.2, v3.6.3 should not wait another 4h: started %v", d.started)
	}
}

// A minimum interval too large for a time.Duration
// (autoUpgradeMinIntervalHours = math.MaxInt, which the settings API now
// refuses above model.MaxAutoUpgradeMinIntervalHours but a hand-edited
// config row or state repo can still carry) saturates rather than wrapping
// negative: after one completed automatic upgrade no further release is
// scheduled, however long it stands (docs/test-runs/2026-11-11-upgrades.md).
func TestMinIntervalHugeValueDoesNotWrap(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	cfg := model.DefaultConfig()
	cfg.AutoUpgradeOn = model.AutoUpgradeOnRelease
	cfg.AutoUpgradeChannel = ""
	cfg.AutoUpgradeMinIntervalHours = math.MaxInt
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("configuring: %v", err)
	}
	got, err := store.GetConfig(ctx)
	if err != nil || got.AutoUpgradeMinIntervalHours != math.MaxInt {
		t.Fatalf("stored interval = %v, %v; want math.MaxInt read back unchanged", got, err)
	}
	if d := got.AutoUpgradeMinInterval(); d <= 0 {
		t.Fatalf("AutoUpgradeMinInterval() = %v, want a positive saturated duration", d)
	}
	t0 := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)

	d := &releaseDeployment{newest: "v3.6.0"}
	d.cycle(t, store, t0)
	d.cycle(t, store, t0.Add(time.Hour))
	finished := t0.Add(time.Hour + time.Minute)
	d.end(upgrade.PhaseOK, "", "")
	d.status.FinishedAt = &finished
	d.cycle(t, store, finished)

	d.newest = "v3.6.1"
	for _, after := range []time.Duration{time.Hour, 2 * time.Hour, 24 * time.Hour, 50 * 365 * 24 * time.Hour} {
		d.cycle(t, store, finished.Add(after))
	}
	if len(d.started) != 1 {
		t.Fatalf("with a huge minimum interval started %v, want only [v3.6.0]", d.started)
	}
}
