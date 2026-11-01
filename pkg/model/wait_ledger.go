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
	"fmt"
	"time"
)

// SetRunWaitLedger replaces what runID's live run is recorded as having
// spent blocked on CI, per reporting source (task_run_wait_ledger): what
// a daemon leaving the run for the next one to adopt writes, so that the
// adopting daemon's clock starts from it rather than from zero. A source
// with no time is not written.
func (s *Store) SetRunWaitLedger(ctx context.Context, runID string, sources map[string]time.Duration) error {
	if runID == "" {
		return nil
	}
	return s.write(ctx, "record wait ledger of run "+runID, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM `task_run_wait_ledger` WHERE `run_id` = ?", runID); err != nil {
			return err
		}
		for source, d := range sources {
			if d <= 0 {
				continue
			}
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO `task_run_wait_ledger` (`run_id`,`source`,`blocked_ms`) VALUES (?,?,?)",
				runID, source, d.Milliseconds()); err != nil {
				return err
			}
		}
		return nil
	})
}

// RunWaitLedger is what SetRunWaitLedger last recorded for runID, keyed
// by source. Empty for a run that has none.
func (s *Store) RunWaitLedger(ctx context.Context, runID string) (map[string]time.Duration, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT `source`, `blocked_ms` FROM `task_run_wait_ledger` WHERE `run_id` = ?", runID)
	if err != nil {
		return nil, fmt.Errorf("reading wait ledger of run %s: %w", runID, err)
	}
	defer rows.Close()
	out := map[string]time.Duration{}
	for rows.Next() {
		var source string
		var ms int64
		if err := rows.Scan(&source, &ms); err != nil {
			return nil, err
		}
		out[source] = time.Duration(ms) * time.Millisecond
	}
	return out, rows.Err()
}
