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
	"context"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/upgrade"
)

// releaseUpgradeSettle is how long a release tag has to stay the newest
// before the release-triggered upgrade acts on it. Releases cut close
// together -- a fix-forward an hour after the release it fixes -- then
// give one drain onto the later one rather than a drain for each. A var
// so a test need not wait it out.
var releaseUpgradeSettle = time.Hour

// autoUpgradeNotifier is who raises and clears FamilyUpgradeFailed.
var autoUpgradeNotifier = model.PrincipalString(model.AutoUpgradePrincipal)

// autoUpgradeExcerptLines and autoUpgradeExcerptBytes bound the log
// excerpt a failure notification quotes: the end of the failed step's
// output, which is where the error is.
const (
	autoUpgradeExcerptLines = 40
	autoUpgradeExcerptBytes = 4000
)

// reconcileReleaseUpgrade is the release-triggered automatic upgrade
// (model.AutoUpgradeOnRelease, releases playbook §0.5): once the
// deployment's image channel publishes a new release tag, it schedules
// an upgrade onto it through the same row an operator's "upgrade once
// idle" writes (model.ScheduledUpgrade), so reconcileScheduledUpgrade
// drains and cuts over exactly as it does for a person, honouring
// -upgrade-drain-window.
//
// Which tag is new is Config.LatestRelease's answer: the newest release
// on the registry the deployment's upgrades pull from, in its own
// edition, never a release candidate (-rc.N), nor anything marked
// -discarded or -retracted. What is scheduled is "latest" when
// AutoUpgradeChannel is "latest" and the exact tag otherwise.
//
// A tag is acted on once it has been the newest for
// releaseUpgradeSettle, and an automatic upgrade still draining is
// re-pointed at a newer release rather than followed by another, so two
// releases close together give one drain. Each release is acted on once
// (model.AutoUpgradeRelease.Release): a release the deployment already
// runs is recorded as such without holding the queue
// (Config.UpgradeUnchanged), and one whose upgrade failed is not tried
// again until a newer release replaces it.
//
// Before any of that it follows up the automatic upgrade it last
// scheduled, in either mode (watchAutoUpgrade): a failure raises one
// FamilyUpgradeFailed notification, a success clears them.
func reconcileReleaseUpgrade(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil || deps.Config.StartUpgrade == nil {
		return nil
	}
	if err := watchAutoUpgrade(ctx, deps, now); err != nil {
		return err
	}
	cfg, err := deps.Store.GetConfig(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading the automatic upgrade settings: %w", err)
	}
	if cfg == nil || cfg.AutoUpgradeMode() != model.AutoUpgradeOnRelease || deps.Config.LatestRelease == nil {
		return nil
	}
	newest, err := latestRelease(ctx, deps)
	if err != nil {
		// A registry that cannot be reached now is asked again next
		// tick; saying so every tick would bury the journal.
		return nil
	}
	if newest == "" {
		return nil
	}
	record, err := deps.Store.GetAutoUpgradeRelease(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: %w", err)
	}
	if record == nil {
		record = &model.AutoUpgradeRelease{}
	}
	if record.Release == newest {
		return nil
	}
	if record.Newest != newest {
		record.Newest, record.SeenAt = newest, now.UTC()
		if err := deps.Store.SetAutoUpgradeRelease(ctx, *record); err != nil {
			return fmt.Errorf("orchestrator: %w", err)
		}
		log.Printf("orchestrator: release %s is published; upgrading to it once it has stood for %s",
			newest, releaseUpgradeSettle)
	}
	if now.Sub(record.SeenAt) < releaseUpgradeSettle {
		return nil
	}
	target := releaseUpgradeTarget(cfg.AutoUpgradeChannel, newest)
	act := func(outcome string, scheduledAt *time.Time) error {
		record.Release, record.Target, record.ScheduledAt, record.Outcome = newest, target, scheduledAt, outcome
		if err := deps.Store.SetAutoUpgradeRelease(ctx, *record); err != nil {
			return fmt.Errorf("orchestrator: %w", err)
		}
		return nil
	}

	scheduled, err := deps.Store.GetScheduledUpgrade(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: %w", err)
	}
	if scheduled != nil {
		if scheduled.By != model.AutoUpgradePrincipal {
			// An operator's schedule is theirs; this release is looked
			// at again once it has gone.
			return nil
		}
		// An automatic upgrade still draining: point it at the newer
		// release and keep its place, so its drain window still counts
		// from when the queue was first held.
		at := scheduled.RequestedAt
		if scheduled.Branch != target {
			scheduled.Branch = target
			if err := deps.Store.SetScheduledUpgrade(ctx, *scheduled); err != nil {
				return fmt.Errorf("orchestrator: re-pointing the automatic upgrade at %s: %w", target, err)
			}
			log.Printf("orchestrator: the automatic upgrade still draining now goes to %s (release %s)", target, newest)
		}
		return act(model.AutoUpgradeScheduled, &at)
	}
	underway, err := upgradeUnderway(ctx, deps)
	if err != nil {
		return err
	}
	if underway {
		// An upgrade running now; the release is looked at again once it
		// ends, and the probe below finds out whether it was this one.
		return nil
	}
	if unchanged, why := autoUpgradeUnchanged(ctx, deps, target); unchanged {
		log.Printf("orchestrator: release %s is published, and this deployment already runs it", newest)
		return act(model.AutoUpgradeUnchanged, nil)
	} else if why != "" {
		log.Printf("orchestrator: scheduling the upgrade to release %s without knowing whether it is new: %s", newest, why)
	}
	at := now.UTC()
	if err := deps.Store.SetScheduledUpgrade(ctx, model.ScheduledUpgrade{
		Branch:      target,
		RequestedAt: at,
		By:          model.AutoUpgradePrincipal,
	}); err != nil {
		return fmt.Errorf("orchestrator: scheduling the upgrade to release %s: %w", newest, err)
	}
	log.Printf("orchestrator: release %s is published -- scheduled the automatic upgrade to %s", newest, target)
	return act(model.AutoUpgradeScheduled, &at)
}

// latestRelease asks Config.LatestRelease, bounded the way
// autoUpgradeUnchanged bounds its probe.
func latestRelease(ctx context.Context, deps Deps) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, autoUpgradeProbeTimeout)
	defer cancel()
	type answer struct {
		tag string
		err error
	}
	done := make(chan answer, 1)
	go func() {
		tag, err := deps.Config.LatestRelease(ctx)
		done <- answer{strings.TrimSpace(tag), err}
	}()
	select {
	case a := <-done:
		return a.tag, a.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// releaseUpgradeTarget is what the release-triggered upgrade schedules
// for release: the moving "latest" tag when that is the channel the
// deployment follows, and the exact release tag otherwise.
func releaseUpgradeTarget(channel, release string) string {
	if strings.TrimSpace(channel) == "latest" {
		return "latest"
	}
	return release
}

// recordDailyAutoUpgrade notes the upgrade the daily automatic upgrade
// just scheduled onto branch, so watchAutoUpgrade reports how it ends.
func recordDailyAutoUpgrade(ctx context.Context, deps Deps, branch string, at time.Time) error {
	record, err := deps.Store.GetAutoUpgradeRelease(ctx)
	if err != nil {
		return err
	}
	if record == nil {
		record = &model.AutoUpgradeRelease{SeenAt: at}
	}
	at = at.UTC()
	record.Release, record.Target, record.ScheduledAt, record.Outcome = "", branch, &at, model.AutoUpgradeScheduled
	return deps.Store.SetAutoUpgradeRelease(ctx, *record)
}

// watchAutoUpgrade follows up the automatic upgrade last scheduled
// (model.AutoUpgradeRelease with Outcome AutoUpgradeScheduled): once the
// upgrade it became has ended, a failure raises one FamilyUpgradeFailed
// notification naming the step that failed and the end of its output,
// and a success clears every open one. Either way the record stops being
// "scheduled", so each upgrade is reported once.
//
// The upgrade is recognised by Config.UpgradeStatus: one onto the
// scheduled target that started no earlier than it was scheduled.
func watchAutoUpgrade(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Config.UpgradeStatus == nil {
		return nil
	}
	record, err := deps.Store.GetAutoUpgradeRelease(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: %w", err)
	}
	if record == nil || record.Outcome != model.AutoUpgradeScheduled || record.ScheduledAt == nil {
		return nil
	}
	status, err := deps.Config.UpgradeStatus()
	if err != nil || status.StartedAt == nil || status.StartedAt.Before(*record.ScheduledAt) ||
		status.Branch != record.Target {
		return nil
	}
	key := record.Release
	if key == "" {
		key = record.Target
	}
	switch status.Phase {
	case upgrade.PhaseFailed:
		if err := raiseUpgradeFailed(ctx, deps, key, record.Target, status, now); err != nil {
			return err
		}
		record.Outcome = model.AutoUpgradeFailed
	case upgrade.PhaseCancelled:
		record.Outcome = model.AutoUpgradeCancelled
	case upgrade.PhaseOK:
		if err := clearUpgradeFailed(ctx, deps); err != nil {
			return err
		}
		record.Outcome = model.AutoUpgradeSucceeded
	default:
		return nil
	}
	if err := deps.Store.SetAutoUpgradeRelease(ctx, *record); err != nil {
		return fmt.Errorf("orchestrator: %w", err)
	}
	return nil
}

// raiseUpgradeFailed raises FamilyUpgradeFailed for the automatic
// upgrade to target (key: the release it was for, or the branch) that
// ended as status.
func raiseUpgradeFailed(ctx context.Context, deps Deps, key, target string, status upgrade.Status, now time.Time) error {
	stage, detail := failedUpgradeStage(status)
	body := fmt.Sprintf("The automatic upgrade to `%s` failed at the **%s** stage, so this deployment is still on "+
		"the build it was running. It is not tried again until a newer release is published; "+
		"[Settings → Upgrade](/settings/upgrade) can retry it by hand.", target, stage)
	if excerpt := logExcerpt(detail); excerpt != "" {
		body += "\n\n```\n" + excerpt + "\n```"
	}
	_, err := raisePipelineNotification(ctx, deps.Store, model.Notification{
		Subject:  model.NotificationSubject(model.FamilyUpgradeFailed, key),
		Severity: model.NotificationWarning,
		Title:    fmt.Sprintf("Automatic upgrade to %s failed at %s", key, stage),
		Body:     body,
		RaisedBy: autoUpgradeNotifier,
	}, now)
	return err
}

// clearUpgradeFailed clears every open FamilyUpgradeFailed: an upgrade
// has since succeeded, so the deployment is past whatever broke.
func clearUpgradeFailed(ctx context.Context, deps Deps) error {
	open, err := deps.Store.ListNotifications(ctx, model.NotificationFilter{Open: true, Family: model.FamilyUpgradeFailed})
	if err != nil {
		return fmt.Errorf("orchestrator: %w", err)
	}
	for _, n := range open {
		if err := clearPipelineNotification(ctx, deps.Store, n.Subject, autoUpgradeNotifier); err != nil {
			return fmt.Errorf("orchestrator: clearing %s: %w", n.Subject, err)
		}
	}
	return nil
}

// failedUpgradeStage names the stage a failed upgrade stopped at, in an
// operator's words, and what that step had to say: the schema rehearsal
// (the pre-cutover `grain state check -migrate`), the image pull, the
// health check, or whichever other step failed. An upgrade with no
// failed step -- one interrupted before it wrote any -- is the upgrade
// as a whole.
func failedUpgradeStage(status upgrade.Status) (stage, detail string) {
	for _, step := range status.Steps {
		if step.State != upgrade.StepFailed {
			continue
		}
		detail = step.Detail
		if detail == "" {
			detail = status.Detail
		}
		switch step.Name {
		case "schema":
			return "schema rehearsal", detail
		case "pull image":
			return "pull", detail
		}
		return step.Name, detail
	}
	return "upgrade", status.Detail
}

// logExcerpt is the end of detail, at most autoUpgradeExcerptLines lines
// and autoUpgradeExcerptBytes bytes.
func logExcerpt(detail string) string {
	detail = strings.TrimSpace(detail)
	lines := strings.Split(detail, "\n")
	if len(lines) > autoUpgradeExcerptLines {
		lines = lines[len(lines)-autoUpgradeExcerptLines:]
	}
	out := strings.Join(lines, "\n")
	if len(out) > autoUpgradeExcerptBytes {
		start := len(out) - autoUpgradeExcerptBytes
		for start < len(out) && !utf8.RuneStart(out[start]) {
			start++
		}
		out = "…" + out[start:]
	}
	return strings.ReplaceAll(out, "```", "'''")
}
