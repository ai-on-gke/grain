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
	"slices"
	"strings"
	"time"
)

// GrantWait is why an otherwise-ready task has not been dispatched. It is an
// admission fact, not a run activity: no attempt or sandbox exists yet.
//
// Being derived rather than declared, it has three owners and no others:
// the admitter (orchestrator.grantAdmitter) derives it, StartRun clears
// it with the attempt it belongs to, and putTask invalidates it when the
// grants or target it was derived from change. Nothing else may leave
// one standing: see grain's former implementation notes, "Who invalidates a
// derived admission fact".
type GrantWait struct {
	TaskID     string
	Capability string
	Resource   string
	WaitingAt  time.Time
}

// SetGrantWait records the finite resource currently holding taskID at
// admission. Repeated probes preserve the first waiting time while refreshing
// the reason, so the UI reports how long the continuous wait has lasted.
func (s *Store) SetGrantWait(ctx context.Context, wait GrantWait) error {
	if wait.TaskID == "" || wait.Capability == "" || wait.Resource == "" {
		return errors.New("model: a grant wait needs a task, capability and resource")
	}
	if existing, err := s.GrantWaitOf(ctx, wait.TaskID); err == nil && existing != nil &&
		existing.Capability == wait.Capability && existing.Resource == wait.Resource {
		return nil
	}
	return s.write(ctx, "set task "+wait.TaskID+" grant wait", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"INSERT INTO `task_grant_wait` (`task_id`,`capability`,`resource`,`waiting_at`) VALUES (?,?,?,?) "+
				"ON CONFLICT (`task_id`) DO UPDATE SET `capability` = excluded.`capability`, `resource` = excluded.`resource` "+
				"WHERE `task_grant_wait`.`capability` <> excluded.`capability` OR `task_grant_wait`.`resource` <> excluded.`resource`",
			wait.TaskID, wait.Capability, wait.Resource, wait.WaitingAt.UTC())
		return err
	})
}

// SetQuotaWaits records QuotaWaitCapability waits for multiple tasks in one
// transaction, skipping tasks whose stored wait is already identical and
// leaving any non-quota wait (model, policy, team setup, suspension, scarce
// capability) untouched. When every candidate is unchanged or holds another
// wait, no write transaction is opened.
func (s *Store) SetQuotaWaits(ctx context.Context, waits []GrantWait) error {
	if len(waits) == 0 {
		return nil
	}
	// Deduplicate by task ID while preserving first-seen order and keeping the
	// last reason supplied for each task.
	order := make([]string, 0, len(waits))
	byID := make(map[string]GrantWait, len(waits))
	for _, w := range waits {
		if w.TaskID == "" || w.Resource == "" {
			continue
		}
		w.Capability = QuotaWaitCapability
		if _, seen := byID[w.TaskID]; !seen {
			order = append(order, w.TaskID)
		}
		byID[w.TaskID] = w
	}
	if len(order) == 0 {
		return nil
	}
	existing, err := s.GrantWaits(ctx)
	toWrite := make([]GrantWait, 0, len(order))
	for _, id := range order {
		w := byID[id]
		if err == nil {
			if cur, ok := existing[id]; ok && (cur.Capability != QuotaWaitCapability || cur.Resource == w.Resource) {
				continue
			}
		}
		toWrite = append(toWrite, w)
	}
	if len(toWrite) == 0 {
		return nil
	}
	return s.write(ctx, "set quota waits", func(tx *sql.Tx) error {
		const chunkSize = 100
		for start := 0; start < len(toWrite); start += chunkSize {
			chunk := toWrite[start:min(start+chunkSize, len(toWrite))]
			var b strings.Builder
			b.WriteString("INSERT INTO `task_grant_wait` (`task_id`,`capability`,`resource`,`waiting_at`) VALUES ")
			args := make([]any, 0, len(chunk)*4+1)
			for i, w := range chunk {
				if i > 0 {
					b.WriteByte(',')
				}
				b.WriteString("(?,?,?,?)")
				args = append(args, w.TaskID, QuotaWaitCapability, w.Resource, w.WaitingAt.UTC())
			}
			b.WriteString(" ON CONFLICT (`task_id`) DO UPDATE SET `resource` = excluded.`resource` " +
				"WHERE `task_grant_wait`.`capability` = ? AND `task_grant_wait`.`resource` <> excluded.`resource`")
			args = append(args, QuotaWaitCapability)
			if _, err := tx.ExecContext(ctx, b.String(), args...); err != nil {
				return err
			}
		}
		return nil
	})
}

// ClearQuotaWaits removes QuotaWaitCapability waits for multiple tasks in one
// transaction, leaving any non-quota wait untouched and opening no write
// transaction when none of taskIDs currently carries a quota wait.
func (s *Store) ClearQuotaWaits(ctx context.Context, taskIDs []string) error {
	if len(taskIDs) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(taskIDs))
	unique := make([]string, 0, len(taskIDs))
	for _, id := range taskIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return nil
	}
	existing, err := s.GrantWaits(ctx)
	var toDelete []string
	if err == nil {
		for _, id := range unique {
			if cur, ok := existing[id]; ok && cur.Capability == QuotaWaitCapability {
				toDelete = append(toDelete, id)
			}
		}
	} else {
		toDelete = unique
	}
	if len(toDelete) == 0 {
		return nil
	}
	return s.write(ctx, "clear quota waits", func(tx *sql.Tx) error {
		const chunkSize = 200
		for start := 0; start < len(toDelete); start += chunkSize {
			chunk := toDelete[start:min(start+chunkSize, len(toDelete))]
			query := "DELETE FROM `task_grant_wait` WHERE `capability` = ? AND `task_id` IN (?" +
				strings.Repeat(",?", len(chunk)-1) + ")"
			args := make([]any, 0, len(chunk)+1)
			args = append(args, QuotaWaitCapability)
			for _, id := range chunk {
				args = append(args, id)
			}
			if _, err := tx.ExecContext(ctx, query, args...); err != nil {
				return err
			}
		}
		return nil
	})
}

// ClearGrantWait removes taskID's queued admission marker. StartRun also does
// this transactionally; this method covers a resource becoming available
// before another pre-run setup error is handled.
func (s *Store) ClearGrantWait(ctx context.Context, taskID string) error {
	if existing, err := s.GrantWaitOf(ctx, taskID); err == nil && existing == nil {
		return nil
	}
	return s.write(ctx, "clear task "+taskID+" grant wait", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM `task_grant_wait` WHERE `task_id` = ?", taskID)
		return err
	})
}

// ClearGrantWaitUnless is ClearGrantWait for a caller that owns only some
// of the waits a task can show: it leaves a wait recorded under any of
// keep in place, so one gate cannot erase the reason another gave.
func (s *Store) ClearGrantWaitUnless(ctx context.Context, taskID string, keep ...string) error {
	if len(keep) == 0 {
		return s.ClearGrantWait(ctx, taskID)
	}
	if existing, err := s.GrantWaitOf(ctx, taskID); err == nil &&
		(existing == nil || slices.Contains(keep, existing.Capability)) {
		return nil
	}
	query := "DELETE FROM `task_grant_wait` WHERE `task_id` = ? AND `capability` NOT IN (?" + strings.Repeat(",?", len(keep)-1) + ")"
	args := make([]any, 0, len(keep)+1)
	args = append(args, taskID)
	for _, capability := range keep {
		args = append(args, capability)
	}
	return s.write(ctx, "clear task "+taskID+" grant wait", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, query, args...)
		return err
	})
}

// GrantWaits returns every current queued admission marker keyed by task ID.
func (s *Store) GrantWaits(ctx context.Context) (map[string]GrantWait, error) {
	out := map[string]GrantWait{}
	err := each(ctx, s.db,
		"SELECT `task_id`,`capability`,`resource`,`waiting_at` FROM `task_grant_wait`", nil,
		func(rows *sql.Rows) error {
			var wait GrantWait
			if err := rows.Scan(&wait.TaskID, &wait.Capability, &wait.Resource, &wait.WaitingAt); err != nil {
				return err
			}
			out[wait.TaskID] = wait
			return nil
		})
	return out, err
}

// GrantWaitOf is GrantWaits for one task.
func (s *Store) GrantWaitOf(ctx context.Context, taskID string) (*GrantWait, error) {
	var wait GrantWait
	err := s.db.QueryRowContext(ctx,
		"SELECT `task_id`,`capability`,`resource`,`waiting_at` FROM `task_grant_wait` WHERE `task_id` = ?", taskID).
		Scan(&wait.TaskID, &wait.Capability, &wait.Resource, &wait.WaitingAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading task %s's grant wait: %w", taskID, err)
	}
	return &wait, nil
}
