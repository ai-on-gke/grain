package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// GetScheduledUpgrade reads the upgrade this deployment is waiting to
// apply, or nil (with no error) when none is pending -- see
// ScheduledUpgrade's own doc comment on why the absence of a row, rather
// than a zero value, is what "nothing is scheduled" is.
//
// One row, keyed the way pipeline_pause is: a deployment is either
// waiting to upgrade or it is not.
func (s *Store) GetScheduledUpgrade(ctx context.Context) (*ScheduledUpgrade, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT `branch`,`requested_at`,`actor_kind`,`actor_id` "+
			"FROM `scheduled_upgrade` WHERE `id` = 1")
	var u ScheduledUpgrade
	var requestedAt time.Time
	var kind string
	if err := row.Scan(&u.Branch, &requestedAt, &kind, &u.By.ID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading the scheduled upgrade: %w", err)
	}
	u.RequestedAt = requestedAt.UTC()
	u.By.Kind = PrincipalKind(kind)
	return &u, nil
}

// SetScheduledUpgrade records one, replacing whatever was already
// scheduled. Replacing rather than refusing, for PipelinePause's own
// reason: somebody asking for a different branch while a deployment is
// still draining has changed their mind, and the second answer is the
// one they mean.
func (s *Store) SetScheduledUpgrade(ctx context.Context, u ScheduledUpgrade) error {
	return s.write(ctx, "schedule an upgrade", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"REPLACE INTO `scheduled_upgrade` "+
				"(`id`,`branch`,`requested_at`,`actor_kind`,`actor_id`) VALUES (1,?,?,?,?)",
			u.Branch, u.RequestedAt.UTC(), string(u.By.Kind), u.By.ID)
		return err
	})
}

// ClearScheduledUpgrade forgets it, reporting whether there was
// anything to forget -- so a caller can tell "I cancelled it" from "there
// was nothing scheduled", which is what both the API and the reconciler
// that starts the upgrade say back to whoever asked.
//
// Both ends of a scheduled upgrade come through here: an operator
// cancelling one, and the reconciler clearing the row as it hands the
// branch to the upgrader. That is deliberate -- the row means "still
// waiting", so it has to stop meaning that the instant the wait is over,
// whichever way it ended.
func (s *Store) ClearScheduledUpgrade(ctx context.Context) (bool, error) {
	var cleared bool
	err := s.write(ctx, "cancel the scheduled upgrade", func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "DELETE FROM `scheduled_upgrade` WHERE `id` = 1")
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		cleared = n > 0
		return nil
	})
	return cleared, err
}

// BusyRunCount is how many runs this deployment is actually working
// through right now: every live run (LiveRunCount's own rows) except the
// ones that are live only because somebody has a session open.
//
// The distinction is the whole of what makes waiting for an idle
// deployment terminate. An interactive run -- a console session, the
// Director among them (model.ConsoleSession) -- is a run whose agent is
// sitting waiting for a person to type, and such a run can stay live for
// days without any work being in progress. Counting it as busy would
// mean a deployment with a standing session never drains, and an upgrade
// scheduled to wait for one never lands.
//
// LEFT JOIN, and a live run whose task row has somehow gone counts as
// busy: liveRunCounts takes the same reading of an unattributable run,
// and the cautious answer here is the one that makes the deployment look
// busier rather than the one that restarts on top of work nobody can see.
func (s *Store) BusyRunCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `task_run` AS `r` LEFT JOIN `task` AS `t` ON `t`.`id` = `r`.`task_id` "+
			"WHERE `r`.`finished_at` IS NULL AND COALESCE(`t`.`interactive`, 0) = 0").Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting busy runs: %w", err)
	}
	return n, nil
}

// LiveCIRunCount is how many runs of grain's own CI have not concluded
// -- the other kind of work a restart throws away (RecoverInterruptedCIRuns
// is what stamps them afterwards, and a branch whose checks were
// cancelled mid-flight is a pull request somebody has to push again).
//
// It is counted beside BusyRunCount rather than folded into it because a
// CI run is not a task_run at all: it belongs to a branch, it outlives
// the run that pushed it, and a deployment can be running CI with no
// agent working at all.
func (s *Store) LiveCIRunCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `ci_run` WHERE `conclusion` = ''").Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting live CI runs: %w", err)
	}
	return n, nil
}
