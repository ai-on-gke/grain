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

// Sandbox tool calls the cluster's exec tunnel failed, mid-run.
//
// A run whose sandbox setup fails on the exec transport says so in its
// own outcome and detail (SetupFailureDetailsSince). A run that got past
// setup and then had its run_command or read_file fail the same way --
// the API server unable to dial the node's kubelet even after the exec
// runner's own retries -- carried on, and nothing recorded it: the
// failure was a tool result the agent read, and agent.Result is gone
// once the run is. An ExecTransportFailure is the count that outlives
// it, written once by orchestrator.RunDispatch after the run finished.

import (
	"context"
	"database/sql"
	"time"
)

// ExecTransportFailure is how many of one run's sandbox tool calls failed
// on the cluster's exec transport (mcp.ExecTransportFailed). At is when
// the run finished, which is when the count became known.
type ExecTransportFailure struct {
	RunID  string
	TaskID string
	At     time.Time
	Calls  int
}

// RecordExecTransportFailure stores f, replacing anything already
// recorded for that run -- RecordUnmatchedFailure's reason: a retried
// write must leave one row, since the calls are summed.
//
// The caller logs a failure and carries on: an instrument must not be
// able to fail the work it observes.
func (s *Store) RecordExecTransportFailure(ctx context.Context, f ExecTransportFailure) error {
	return s.write(ctx, "record run "+f.RunID+"'s exec-tunnel failures", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM `task_run_exec_failure` WHERE `run_id` = ?", f.RunID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			"INSERT INTO `task_run_exec_failure` (`run_id`,`task_id`,`at`,`calls`) VALUES (?,?,?,?)",
			f.RunID, f.TaskID, f.At.UTC(), f.Calls)
		return err
	})
}

// ExecTransportFailuresSince is every run recorded at or after since
// with tool calls the exec tunnel failed, newest first. It reads across
// tasks, like SetupFailureDetailsSince, because what it measures is the
// cluster rather than any one task.
func (s *Store) ExecTransportFailuresSince(ctx context.Context, since time.Time) ([]ExecTransportFailure, error) {
	var out []ExecTransportFailure
	err := each(ctx, s.db,
		"SELECT `run_id`,`task_id`,`at`,`calls` FROM `task_run_exec_failure` WHERE `at` >= ? "+
			"ORDER BY `at` DESC, `run_id` DESC",
		[]any{since.UTC()},
		func(rows *sql.Rows) error {
			var f ExecTransportFailure
			if err := rows.Scan(&f.RunID, &f.TaskID, &f.At, &f.Calls); err != nil {
				return err
			}
			f.At = f.At.UTC()
			out = append(out, f)
			return nil
		})
	return out, err
}
