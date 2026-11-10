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

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// AutoUpgradeRun is the last time the daily automatic upgrade
// (Config.AutoUpgradeChannel) fired: the day, on the deployment's own
// clock, whose window it was, so orchestrator's reconcileAutoUpgrade
// schedules each day's upgrade once and not again on every tick after
// it -- nor again on the deployment the upgrade itself restarts.
//
// Grain's note to itself rather than configuration, like
// ScheduledUpgrade: the auto_upgrade_run table is off SettingsTables.
type AutoUpgradeRun struct {
	// Day is the "2006-01-02" date, in Config.TimeZone, the firing
	// belonged to.
	Day string
	// Branch is what it scheduled, for the journal and the pane.
	Branch string
	// FiredAt is when.
	FiredAt time.Time
}

// GetAutoUpgradeRun reads the last firing, or nil (with no error) for a
// deployment whose automatic upgrade has never fired.
func (s *Store) GetAutoUpgradeRun(ctx context.Context) (*AutoUpgradeRun, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT `day`,`branch`,`fired_at` FROM `auto_upgrade_run` WHERE `id` = 1")
	var r AutoUpgradeRun
	if err := row.Scan(&r.Day, &r.Branch, &r.FiredAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading the last automatic upgrade: %w", err)
	}
	r.FiredAt = r.FiredAt.UTC()
	return &r, nil
}

// SetAutoUpgradeRun records a firing, replacing the one before it.
func (s *Store) SetAutoUpgradeRun(ctx context.Context, r AutoUpgradeRun) error {
	return s.write(ctx, "record the automatic upgrade for "+r.Day, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"REPLACE INTO `auto_upgrade_run` (`id`,`day`,`branch`,`fired_at`) VALUES (1,?,?,?)",
			r.Day, r.Branch, r.FiredAt.UTC())
		return err
	})
}

// AutoUpgradePrincipal is who schedules the daily automatic upgrade --
// the By of the ScheduledUpgrade orchestrator's reconcileAutoUpgrade
// records, so the pane and the journal say the schedule did it rather
// than naming an operator who was asleep.
var AutoUpgradePrincipal = Principal{Kind: PrincipalAutomation, ID: "auto-upgrade"}

// AutoUpgradeRelease is what the release-triggered automatic upgrade
// (Config.AutoUpgradeOn = AutoUpgradeOnRelease, orchestrator's
// reconcileReleaseUpgrade) has seen and done: the newest release tag on
// the deployment's image channel and since when, and the release it last
// acted on, with the upgrade it scheduled for it and how that ended.
//
// Grain's note to itself, like AutoUpgradeRun: the auto_upgrade_release
// table is off SettingsTables.
type AutoUpgradeRelease struct {
	// Newest is the newest release tag seen ("v3.6.0"), and SeenAt when
	// it was first seen as newest. A release is acted on only once it
	// has stayed newest for a while, so two releases close together
	// give one upgrade rather than two drains.
	Newest string
	SeenAt time.Time
	// Release is the release tag last acted on, and Target what was
	// scheduled for it ("latest", or Release itself). Once Release is
	// the newest tag nothing more is scheduled -- a failed upgrade is
	// retried only when a newer release arrives.
	Release string
	Target  string
	// ScheduledAt is when that upgrade was scheduled; nil when nothing
	// was (the deployment already ran it).
	ScheduledAt *time.Time
	// Outcome is how it ended: one of the AutoUpgradeOutcome values.
	Outcome string
}

// The values of AutoUpgradeRelease.Outcome.
const (
	// AutoUpgradeScheduled is an upgrade scheduled and not yet ended.
	AutoUpgradeScheduled = "scheduled"
	// AutoUpgradeUnchanged is a release the deployment already ran.
	AutoUpgradeUnchanged = "unchanged"
	// AutoUpgradeSucceeded is an upgrade that finished.
	AutoUpgradeSucceeded = "ok"
	// AutoUpgradeFailed is an upgrade that failed; a notification
	// (FamilyUpgradeFailed) says where.
	AutoUpgradeFailed = "failed"
	// AutoUpgradeCancelled is an upgrade cancelled by an operator.
	AutoUpgradeCancelled = "cancelled"
)

// GetAutoUpgradeRelease reads the record, or nil (with no error) for a
// deployment whose release-triggered upgrade has never looked.
func (s *Store) GetAutoUpgradeRelease(ctx context.Context) (*AutoUpgradeRelease, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT `newest`,`seen_at`,`release_tag`,`target`,`scheduled_at`,`outcome` FROM `auto_upgrade_release` WHERE `id` = 1")
	var r AutoUpgradeRelease
	var scheduled sql.NullTime
	if err := row.Scan(&r.Newest, &r.SeenAt, &r.Release, &r.Target, &scheduled, &r.Outcome); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading the release-triggered upgrade's record: %w", err)
	}
	r.SeenAt = r.SeenAt.UTC()
	if scheduled.Valid {
		t := scheduled.Time.UTC()
		r.ScheduledAt = &t
	}
	return &r, nil
}

// SetAutoUpgradeRelease writes the record, replacing the one before it.
func (s *Store) SetAutoUpgradeRelease(ctx context.Context, r AutoUpgradeRelease) error {
	var scheduled any
	if r.ScheduledAt != nil {
		scheduled = r.ScheduledAt.UTC()
	}
	return s.write(ctx, "record the release-triggered upgrade for "+r.Newest, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"REPLACE INTO `auto_upgrade_release` (`id`,`newest`,`seen_at`,`release_tag`,`target`,`scheduled_at`,`outcome`) VALUES (1,?,?,?,?,?,?)",
			r.Newest, r.SeenAt.UTC(), r.Release, r.Target, scheduled, r.Outcome)
		return err
	})
}
