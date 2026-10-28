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

// GetPipelinePause reads the deployment's pause, or nil (with no error)
// when it is running -- see PipelinePause's own doc comment on why the
// absence of a row, rather than a zero value, is what "not paused" is.
//
// One row, keyed the way grain_config and grain_schema are: a deployment
// is either picking work up or it is not, so there is nothing here to
// key by anything else.
func (s *Store) GetPipelinePause(ctx context.Context) (*PipelinePause, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT `paused_at`,`reason`,`actor_kind`,`actor_id`,`until_task_id` "+
			"FROM `pipeline_pause` WHERE `id` = 1")
	var p PipelinePause
	var pausedAt time.Time
	var kind string
	if err := row.Scan(&pausedAt, &p.Reason, &kind, &p.By.ID, &p.UntilTaskID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading the pipeline pause: %w", err)
	}
	p.PausedAt = pausedAt.UTC()
	p.By.Kind = PrincipalKind(kind)
	return &p, nil
}

// SetPipelinePause closes the gate, replacing whatever it already said.
// Replacing rather than refusing is deliberate: the second caller is
// holding the more recent reason -- an operator pausing a deployment
// grain had already paused to check an upgrade is saying something the
// earlier pause does not, and it is their resume that will lift it.
func (s *Store) SetPipelinePause(ctx context.Context, p PipelinePause) error {
	return s.write(ctx, "pause the pipeline", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"REPLACE INTO `pipeline_pause` "+
				"(`id`,`paused_at`,`reason`,`actor_kind`,`actor_id`,`until_task_id`) "+
				"VALUES (1,?,?,?,?,?)",
			p.PausedAt.UTC(), p.Reason, string(p.By.Kind), p.By.ID, p.UntilTaskID)
		return err
	})
}

// ClearPipelinePauseTakenBy opens the gate only if the pause standing in
// it is one by took and is not waiting on a task -- the shape of a hold
// rather than of an operator's decision or of the upgrade check's own
// pause. The condition is in the DELETE rather than in a read followed
// by a write so that the two cannot interleave.
//
// It exists because letting go of a hold and lifting a pause are
// different acts, performed by different processes: an upgrade closes
// the gate before it restarts this deployment and the process that comes
// up afterwards is the one that opens it again
// (orchestrator.ReleaseUpgradeHold). In the gap somebody may have paused
// this deployment themselves, and that pause is not the arriving
// process's to lift -- hence the actor condition. It reports whether
// there was anything of by's to open, so a caller can say which of the
// two happened.
func (s *Store) ClearPipelinePauseTakenBy(ctx context.Context, by Principal) (bool, error) {
	var cleared bool
	err := s.write(ctx, "resume the pipeline", func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			"DELETE FROM `pipeline_pause` WHERE `id` = 1 AND `actor_kind` = ? AND `actor_id` = ? "+
				"AND `until_task_id` = ''",
			string(by.Kind), by.ID)
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

// ClearPipelinePauseUntil opens the gate only if the pause standing in
// it is still the one waiting on taskID -- the orchestrator's own
// auto-resume once that task has finished (or overrun its deadline).
// Like ClearPipelinePauseTakenBy, the condition is in the DELETE rather
// than in a read followed by a write: an operator who replaces the
// task-bound pause with their own between the reconciler's read and
// this write keeps their pause, which is never the reconciler's to lift.
// It reports whether anything was cleared.
func (s *Store) ClearPipelinePauseUntil(ctx context.Context, taskID string) (bool, error) {
	if taskID == "" {
		return false, nil
	}
	var cleared bool
	err := s.write(ctx, "resume the pipeline", func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			"DELETE FROM `pipeline_pause` WHERE `id` = 1 AND `until_task_id` = ?", taskID)
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

// ClearPipelinePause opens the gate again, reporting whether there was
// anything to open -- so a caller can tell "I resumed this deployment"
// from "it was already running", which is what both the CLI and the API
// say back to whoever asked.
func (s *Store) ClearPipelinePause(ctx context.Context) (bool, error) {
	var cleared bool
	err := s.write(ctx, "resume the pipeline", func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "DELETE FROM `pipeline_pause` WHERE `id` = 1")
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
