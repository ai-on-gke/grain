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
