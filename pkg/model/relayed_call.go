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
)

// RecordRelayedCalls notes that grain has already acted on the tool calls
// fingerprints name while runID is still live (task_run_relayed_call):
// one count per entry, so two identical calls are two. The fingerprint is
// the caller's own (orchestrator's toolCallFingerprint); this only keeps
// the tally.
func (s *Store) RecordRelayedCalls(ctx context.Context, runID string, fingerprints []string) error {
	if runID == "" || len(fingerprints) == 0 {
		return nil
	}
	return s.write(ctx, "record relayed calls of run "+runID, func(tx *sql.Tx) error {
		for _, fp := range fingerprints {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO `task_run_relayed_call` (`run_id`,`fingerprint`,`count`) VALUES (?,?,1) "+
					"ON CONFLICT (`run_id`,`fingerprint`) DO UPDATE SET `count` = `task_run_relayed_call`.`count` + 1",
				runID, fp); err != nil {
				return err
			}
		}
		return nil
	})
}

// RelayedCalls is how many of runID's tool calls with each fingerprint
// grain has already acted on (RecordRelayedCalls). Empty for a run that
// recorded none.
func (s *Store) RelayedCalls(ctx context.Context, runID string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT `fingerprint`, `count` FROM `task_run_relayed_call` WHERE `run_id` = ?", runID)
	if err != nil {
		return nil, fmt.Errorf("reading relayed calls of run %s: %w", runID, err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var fp string
		var n int
		if err := rows.Scan(&fp, &n); err != nil {
			return nil, err
		}
		out[fp] = n
	}
	return out, rows.Err()
}
