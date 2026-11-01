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

// What a run's capability grants did for it.
//
// A capability server may answer release_<method> with audit records
// about the grant it is releasing (design/spec/capabilities/capabilities.spec.md §4.4.4):
// host-script's one record per call -- script, argv, exit code, duration,
// the refusal reason of a refused call -- is the first. Grain releases a
// run's grants once the run has ended, so these are written after the
// fact, the same shape task_run.transcript has, and read back by the
// task's attempt view (ui.Attempt.CapabilityAudit).

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// CapabilityAuditRecord is one record about one grant of a run.
//
// Record is the server's own JSON object, verbatim and untrusted. Note
// is grain's instead -- how many records a server left out, or why grain
// refused the audit it sent -- and a row carries one or the other. Grant
// is the grant's <capability_id>#<method> and AttemptID its attempt_id,
// so a record names the one grant it is about.
type CapabilityAuditRecord struct {
	RunID     string
	Seq       int
	Grant     string
	AttemptID string
	At        time.Time
	Record    string
	Note      string
}

// AppendRunCapabilityAudit adds records to runID's audit after any it
// already has, numbering them on from there.
func (s *Store) AppendRunCapabilityAudit(ctx context.Context, runID string, records []CapabilityAuditRecord) error {
	if len(records) == 0 {
		return nil
	}
	return s.write(ctx, "record run "+runID+"'s capability audit", func(tx *sql.Tx) error {
		var last sql.NullInt64
		if err := tx.QueryRowContext(ctx,
			"SELECT MAX(`seq`) FROM `task_run_capability_audit` WHERE `run_id` = ?", runID).Scan(&last); err != nil {
			return err
		}
		seq := int(last.Int64)
		for _, r := range records {
			seq++
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO `task_run_capability_audit` "+
					"(`run_id`,`seq`,`capability`,`attempt_id`,`at`,`record`,`note`) VALUES (?,?,?,?,?,?,?)",
				runID, seq, r.Grant, r.AttemptID, r.At.UTC(), r.Record, r.Note); err != nil {
				return err
			}
		}
		return nil
	})
}

// RunCapabilityAudit is every audit record of taskID's numbered attempt,
// in the order they were recorded, and whether that attempt exists.
func (s *Store) RunCapabilityAudit(ctx context.Context, taskID string, attempt int) ([]CapabilityAuditRecord, bool, error) {
	var runID string
	err := s.db.QueryRowContext(ctx,
		"SELECT `id` FROM `task_run` WHERE `task_id` = ? AND `attempt` = ?", taskID, attempt).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var out []CapabilityAuditRecord
	err = each(ctx, s.db,
		"SELECT `run_id`,`seq`,`capability`,`attempt_id`,`at`,`record`,`note` "+
			"FROM `task_run_capability_audit` WHERE `run_id` = ? ORDER BY `seq`",
		[]any{runID},
		func(rows *sql.Rows) error {
			var r CapabilityAuditRecord
			if err := rows.Scan(&r.RunID, &r.Seq, &r.Grant, &r.AttemptID, &r.At, &r.Record, &r.Note); err != nil {
				return err
			}
			r.At = r.At.UTC()
			out = append(out, r)
			return nil
		})
	return out, true, err
}

// RunCapabilityAuditCounts is how many audit records each of taskID's
// attempts has, by attempt number -- what a task's detail carries, the
// records themselves being their own fetch.
func (s *Store) RunCapabilityAuditCounts(ctx context.Context, taskID string) (map[int]int, error) {
	out := map[int]int{}
	err := each(ctx, s.db,
		"SELECT r.`attempt`, COUNT(*) FROM `task_run_capability_audit` a JOIN `task_run` r ON r.`id` = a.`run_id` "+
			"WHERE r.`task_id` = ? GROUP BY r.`attempt`",
		[]any{taskID},
		func(rows *sql.Rows) error {
			var attempt, n int
			if err := rows.Scan(&attempt, &n); err != nil {
				return err
			}
			out[attempt] = n
			return nil
		})
	return out, err
}
