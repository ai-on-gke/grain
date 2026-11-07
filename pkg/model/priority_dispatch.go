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
)

// PriorityDispatchCapability is the retired legacy capability name for
// AdmissionBypass, retained for one release as an input-only alias
// (design/design/unified-grants.design.md §4.1).
const PriorityDispatchCapability = "priority-dispatch"

// DurableCapability is the retired legacy capability name for
// AdmissionDurable, retained for one release as an input-only alias
// (design/design/unified-grants.design.md §4.1).
const DurableCapability = "durable"

// SetupFailedOutcome is a run's outcome when it never reached its agent:
// the sandbox could not be prepared, the checkout would not clone, a
// capability would not mint (orchestrator's setup guard).
const SetupFailedOutcome = "setup-failed"

// HasPriorityDispatch is the one answer to "is this task admitted the way
// a console session is": true for any task with admission >= bypass
// (AdmissionBypass or AdmissionDurable, including console sessions). Every
// exemption that used to be a session's alone -- dispatch's ordering,
// counts and pause, the base-repair link, the per-connection concurrency
// limit -- asks this, and priorityDispatchTaskSQL is the same predicate for
// the store's queries, so the two cannot disagree.
func (t Task) HasPriorityDispatch() bool {
	return NormalizeAdmission(t.Admission).AtLeast(AdmissionBypass)
}

// IsDurable reports whether t has AdmissionDurable: its runs have no
// deadline and are watched for idleness instead (orchestrator's
// durableDispatch), and it is dispatched with priority
// (HasPriorityDispatch) under its own ceiling (Limits.Durable).
func (t Task) IsDurable() bool {
	return NormalizeAdmission(t.Admission) == AdmissionDurable
}

// IsPersonOnlyGrant reports whether only a person may grant name: a
// dangerous api grant (IsDangerousGrant), or the one-release input aliases
// priority-dispatch and durable.
func IsPersonOnlyGrant(name string) bool {
	return name == PriorityDispatchCapability || name == DurableCapability || IsDangerousGrant(name)
}

// IsAdminOnlyGrant reports whether name is a grant only a direct human
// operator may approve or assign (AU-25.3).
func IsAdminOnlyGrant(name string) bool {
	return IsPersonOnlyGrant(name)
}

// priorityDispatchTaskSQL is HasPriorityDispatch for the store's own
// queries: a task whose admission is bypass or durable. It expects `t`
// bound to the task row. Parenthesised and COALESCEd so `NOT ` +
// priorityDispatchTaskSQL over a LEFT JOIN stays boolean.
const priorityDispatchTaskSQL = "(COALESCE(`t`.`admission`, 'standard') IN ('bypass', 'durable'))"

// durableTaskSQL is IsDurable for the store's own queries: a task whose
// admission is durable. It expects `t` bound to the task row.
const durableTaskSQL = "(COALESCE(`t`.`admission`, 'standard') = 'durable')"

// taskIsDurable answers durableTaskSQL for one task id.
func taskIsDurable(ctx context.Context, q querier, taskID string) (bool, error) {
	var durable bool
	err := q.QueryRowContext(ctx, "SELECT "+durableTaskSQL+" FROM `task` AS `t` WHERE `t`.`id` = ?",
		taskID).Scan(&durable)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading whether task %s is durable: %w", taskID, err)
	}
	return durable, nil
}

// liveDurableRuns is how many runs of durable tasks are live: what
// Limits.Durable is held against.
func liveDurableRuns(ctx context.Context, q querier) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM `task_run` AS `r` JOIN `task` AS `t` ON `t`.`id` = `r`.`task_id` "+
		"WHERE `r`.`finished_at` IS NULL AND "+durableTaskSQL).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting live durable runs: %w", err)
	}
	return n, nil
}

// LiveDurableRuns is liveDurableRuns against the store's own handle.
func (s *Store) LiveDurableRuns(ctx context.Context) (int, error) {
	return liveDurableRuns(ctx, s.db)
}

// IsDurable is Task.IsDurable for one task id, read from the rows.
func (s *Store) IsDurable(ctx context.Context, taskID string) (bool, error) {
	return taskIsDurable(ctx, s.db, taskID)
}

// taskHasPriorityDispatch answers priorityDispatchTaskSQL for one task id
// against either the store's own handle or the transaction that records
// its run. A task id with no row has none.
func taskHasPriorityDispatch(ctx context.Context, q querier, taskID string) (bool, error) {
	var priority bool
	err := q.QueryRowContext(ctx,
		"SELECT "+priorityDispatchTaskSQL+" FROM `task` AS `t` WHERE `t`.`id` = ?",
		taskID).Scan(&priority)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading whether task %s has priority dispatch: %w", taskID, err)
	}
	return priority, nil
}

// HasPriorityDispatch is Task.HasPriorityDispatch for one task id, read
// from the rows rather than a whole Task.
func (s *Store) HasPriorityDispatch(ctx context.Context, taskID string) (bool, error) {
	return taskHasPriorityDispatch(ctx, s.db, taskID)
}

// DurableCeilingSubject is the notification subject raised when a durable
// task cannot start because Limits.Durable is full
// (design/design/durable-tasks.design.md#DT-5).
func DurableCeilingSubject(team string) string { return "durable.ceiling:" + team }

// DurableSetupFailingSubject is the notification subject raised when a
// durable task's setup has been suspended DurableSetupFailingAfter times
// in a row.
func DurableSetupFailingSubject(taskID string) string { return "durable.setup-failing:" + taskID }

// DurableSetupFailingAfter is how many setup suspensions in a row raise
// DurableSetupFailingSubject.
const DurableSetupFailingAfter = 5
