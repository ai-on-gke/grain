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
	"strings"
	"time"
)

// SpoolCursor is how far the daemon has read one live run's run-host
// spool (pkg/runhost), and when it last advanced.
type SpoolCursor struct {
	RunID     string
	Offset    int64
	UpdatedAt time.Time
}

// SetSpoolCursor records that runID's spool has been read up to offset.
func (s *Store) SetSpoolCursor(ctx context.Context, runID string, offset int64, at time.Time) error {
	return s.write(ctx, "advance run "+runID+" spool cursor", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"INSERT INTO `task_run_spool_cursor` (`run_id`,`offset`,`updated_at`) VALUES (?,?,?) "+
				"ON CONFLICT (`run_id`) DO UPDATE SET `offset` = excluded.`offset`, `updated_at` = excluded.`updated_at`",
			runID, offset, at.UTC())
		return err
	})
}

// SpoolCursors is every persisted run-host cursor, keyed by run id: what
// the adoption pass reads at startup. A hostd cursor (HostdCursors) is
// not one of them.
func (s *Store) SpoolCursors(ctx context.Context) (map[string]SpoolCursor, error) {
	all, err := s.allSpoolCursors(ctx)
	out := map[string]SpoolCursor{}
	for k, c := range all {
		if !strings.HasPrefix(k, hostdCursorPrefix) {
			out[k] = c
		}
	}
	return out, err
}

// hostdCursorPrefix starts the key of a hostd cursor in the cursor table.
const hostdCursorPrefix = "hostd:"

// HostdCursorKey is the cursor table's key for runID's stdout cursor
// into a grain hostd spool (runhost.Hostd), kept apart from a run-host
// cursor so each adoption pass sees only the rows driven its own way. It
// is deleted with the run-host one when the run finishes.
func HostdCursorKey(runID string) string { return hostdCursorPrefix + runID }

// SetHostdCursor is SetSpoolCursor for a run driven through grain hostd.
func (s *Store) SetHostdCursor(ctx context.Context, runID string, offset int64, at time.Time) error {
	return s.SetSpoolCursor(ctx, HostdCursorKey(runID), offset, at)
}

// HostdCursors is every persisted hostd cursor, keyed by run id (with
// RunID the run's own id): what hostd's adoption pass reads at startup.
func (s *Store) HostdCursors(ctx context.Context) (map[string]SpoolCursor, error) {
	all, err := s.allSpoolCursors(ctx)
	out := map[string]SpoolCursor{}
	for k, c := range all {
		if run, ok := strings.CutPrefix(k, hostdCursorPrefix); ok {
			c.RunID = run
			out[run] = c
		}
	}
	return out, err
}

func (s *Store) allSpoolCursors(ctx context.Context) (map[string]SpoolCursor, error) {
	out := map[string]SpoolCursor{}
	err := each(ctx, s.db,
		"SELECT `run_id`,`offset`,`updated_at` FROM `task_run_spool_cursor`", nil,
		func(rows *sql.Rows) error {
			var c SpoolCursor
			if err := rows.Scan(&c.RunID, &c.Offset, &c.UpdatedAt); err != nil {
				return err
			}
			out[c.RunID] = c
			return nil
		})
	return out, err
}
