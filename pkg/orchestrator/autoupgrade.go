package orchestrator

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// reconcileAutoUpgrade is the daily automatic upgrade: once a day, at
// Config.AutoUpgradeTime on the deployment's own clock, it schedules an
// upgrade onto Config.AutoUpgradeChannel.
//
// It schedules rather than starts. The upgrade goes through the same
// row an operator's "upgrade once idle" writes (model.ScheduledUpgrade),
// so reconcileScheduledUpgrade -- the next reconciler in the cycle --
// holds the queue, lets the runs already under way finish, and cuts over
// when nothing is left to lose, honouring -upgrade-drain-window exactly
// as it does for a person's schedule. On an idle deployment that is the
// same tick. A target that turns out to carry the build already running
// ends as an unchanged upgrade (upgrade.Status.Unchanged), without a
// restart, and the hold goes with it. Config.UpgradeUnchanged, where it
// is set, finds most of those out before the hold rather than after the
// drain: a channel it says has nothing new is recorded as the day's
// upgrade and nothing is scheduled. Any doubt -- an error, no answer in
// time -- schedules as before, and staging decides.
//
// Each day fires once. The day is recorded (model.AutoUpgradeRun) the
// moment its upgrade is scheduled, which is what keeps the deployment the
// upgrade restarts from scheduling the same day's upgrade again. A day
// whose time has passed and that has not fired yet fires on the next
// tick -- a deployment that was down at three in the morning catches up
// when it comes back, and one whose operator turns the feature on in the
// afternoon gets its first upgrade then, not tomorrow. A day on which an
// upgrade is already running or scheduled when the time comes counts as
// done: that upgrade is the one the day wanted, and scheduling a second
// behind it would only replace an operator's choice of branch.
func reconcileAutoUpgrade(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil || deps.Config.StartUpgrade == nil {
		return nil
	}
	cfg, err := deps.Store.GetConfig(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading the automatic upgrade settings: %w", err)
	}
	if cfg == nil {
		return nil
	}
	branch := strings.TrimSpace(cfg.AutoUpgradeChannel)
	at := strings.TrimSpace(cfg.AutoUpgradeTime)
	if branch == "" || at == "" {
		return nil
	}
	day, due, err := autoUpgradeDue(now, at, cfg.TimeZone)
	if err != nil {
		// PUT /api/settings refuses a time it cannot read, so this is a
		// hand-edited row; saying so every tick would be noise, and the
		// settings pane shows the value for somebody to correct.
		return nil
	}
	if !due {
		return nil
	}
	last, err := deps.Store.GetAutoUpgradeRun(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: %w", err)
	}
	// >= rather than ==: "2006-01-02" sorts as it reads, so a day
	// recorded ahead of this one -- the zone was moved west since -- is
	// not a reason to fire again.
	if last != nil && last.Day >= day {
		return nil
	}
	record := model.AutoUpgradeRun{Day: day, Branch: branch, FiredAt: now.UTC()}

	underway, err := upgradeUnderway(ctx, deps)
	if err != nil {
		return err
	}
	if underway {
		if err := deps.Store.SetAutoUpgradeRun(ctx, record); err != nil {
			return fmt.Errorf("orchestrator: %w", err)
		}
		log.Printf("orchestrator: the automatic upgrade to %s for %s is not scheduled -- an upgrade is already running or scheduled",
			branch, day)
		return nil
	}
	if unchanged, why := autoUpgradeUnchanged(ctx, deps, branch); unchanged {
		if err := deps.Store.SetAutoUpgradeRun(ctx, record); err != nil {
			return fmt.Errorf("orchestrator: %w", err)
		}
		log.Printf("orchestrator: the automatic upgrade to %s for %s is not scheduled -- %s is already running the build it would upgrade to",
			branch, day, branch)
		return nil
	} else if why != "" {
		log.Printf("orchestrator: scheduling the automatic upgrade to %s without knowing whether it is new: %s", branch, why)
	}
	// Scheduled first and recorded second: a record that fails to write
	// leaves an upgrade scheduled, which the next tick reads as underway
	// and records then; the other order would lose the day's upgrade to a
	// failed schedule.
	if err := deps.Store.SetScheduledUpgrade(ctx, model.ScheduledUpgrade{
		Branch:      branch,
		RequestedAt: now.UTC(),
		By:          model.AutoUpgradePrincipal,
	}); err != nil {
		return fmt.Errorf("orchestrator: scheduling the automatic upgrade to %s: %w", branch, err)
	}
	if err := deps.Store.SetAutoUpgradeRun(ctx, record); err != nil {
		return fmt.Errorf("orchestrator: %w", err)
	}
	log.Printf("orchestrator: scheduled the automatic upgrade to %s for %s (%s %s)",
		branch, day, at, model.TimeZoneOrDefault(cfg.TimeZone))
	return nil
}

// autoUpgradeProbeTimeout is how long reconcileAutoUpgrade waits for
// Config.UpgradeUnchanged before scheduling regardless. A var so a test
// need not wait it out.
var autoUpgradeProbeTimeout = 10 * time.Second

// autoUpgradeUnchanged asks Config.UpgradeUnchanged whether branch is
// already what this deployment runs, waiting at most
// autoUpgradeProbeTimeout for the answer -- in a goroutine, so a probe
// that ignores its context still cannot hold the tick past that. Only a
// positive answer is true; why says what kept a probe that was asked
// from answering, for the journal, and is "" when there was no probe or
// it answered.
func autoUpgradeUnchanged(ctx context.Context, deps Deps, branch string) (unchanged bool, why string) {
	probe := deps.Config.UpgradeUnchanged
	if probe == nil {
		return false, ""
	}
	ctx, cancel := context.WithTimeout(ctx, autoUpgradeProbeTimeout)
	defer cancel()
	type answer struct {
		unchanged bool
		err       error
	}
	done := make(chan answer, 1)
	go func() {
		unchanged, err := probe(ctx, branch)
		done <- answer{unchanged, err}
	}()
	select {
	case a := <-done:
		if a.err != nil {
			return false, a.err.Error()
		}
		return a.unchanged, ""
	case <-ctx.Done():
		return false, fmt.Sprintf("no answer within %s", autoUpgradeProbeTimeout)
	}
}

// autoUpgradeDue says which day now falls on in zone, and whether that
// day's hhmm has been reached.
func autoUpgradeDue(now time.Time, hhmm, zone string) (day string, due bool, err error) {
	hour, minute, err := model.ParseAutoUpgradeTime(hhmm)
	if err != nil {
		return "", false, err
	}
	loc := model.LoadLocation(strings.TrimSpace(zone))
	local := now.In(loc)
	at := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
	return local.Format("2006-01-02"), !local.Before(at), nil
}
