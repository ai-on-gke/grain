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

// PriorityDispatchCapability is the grant that gives a task the admission
// console sessions get (docs/capabilities.md's priority-dispatch): a run
// of it is admitted outside every worker and merger count, ordered ahead
// of mergers and ordinary work, passes a pipeline pause other than an
// upgrade's own hold, is never parked behind a red base branch's repair,
// and is not held by a provider connection's concurrency limit. It still
// charges the sandbox budget, on the same priority policy a session's
// does (Limits.AdmitDirectorSandboxShape, Budget.AdmitsPriority).
//
// Only a person may grant it (IsPersonOnlyGrant): it lets the task jump
// the queue, so an agent can neither attach it to work it files nor pass
// it on from a task that holds it. grain's own system templates are the
// one exception, and only because they carry it by construction (the
// pipeline steward's, pipelineStewardTemplate).
//
// Named here rather than only in pkg/capability/prioritydispatch because
// the store has to recognise it: the identity is a SQL predicate
// (priorityDispatchTaskSQL) read inside StartRun's own transaction.
const PriorityDispatchCapability = "priority-dispatch"

// DurableCapability is the grant that makes a task durable
// (design/design/durable-tasks.design.md#DT-1, docs/capabilities.md's durable): a
// run of it has no wall-clock deadline and no wait-credit cap, is
// suspended by an idle watchdog rather than ended by a clock, counts
// under the deployment's durable-run ceiling (Limits.Durable) rather than
// max_workers, and has its setup failures suspended rather than counted.
// It implies PriorityDispatchCapability (HasPriorityDispatch).
//
// Person-only (IsPersonOnlyGrant), for the same reason priority-dispatch
// is and a stronger one: a durable run holds a sandbox for as long as its
// work lasts.
const DurableCapability = "durable"

// SetupFailedOutcome is a run's outcome when it never reached its agent:
// the sandbox could not be prepared, the checkout would not clone, a
// capability would not mint (orchestrator's setup guard).
const SetupFailedOutcome = "setup-failed"

// HasPriorityDispatch is the one answer to "is this task admitted the way
// a console session is": true for any task holding
// PriorityDispatchCapability and for every durable task (IsDurable), which
// durable implies (including console sessions). Every exemption that used
// to be a session's alone -- dispatch's ordering, counts and pause, the
// base-repair link, the per-connection concurrency limit -- asks this, and
// priorityDispatchTaskSQL is the same predicate for the store's queries,
// so the two cannot disagree.
func (t Task) HasPriorityDispatch() bool {
	for _, g := range t.Grants {
		if g.Capability == PriorityDispatchCapability || g.Capability == DurableCapability {
			return true
		}
	}
	return false
}

// IsDurable reports whether t holds DurableCapability: its runs have no
// deadline and are watched for idleness instead (orchestrator's
// durableDispatch), and it is dispatched with priority
// (HasPriorityDispatch) under its own ceiling (Limits.Durable).
func (t Task) IsDurable() bool {
	for _, g := range t.Grants {
		if g.Capability == DurableCapability {
			return true
		}
	}
	return false
}

// IsPersonOnlyGrant reports whether only a person may grant name: a
// dangerous api grant (IsDangerousGrant), priority-dispatch, or durable,
// which implies it. An agent
// holding one still cannot hand it to anything it files, and a proposal
// naming one waits for a person (EscalatedCapabilities).
func IsPersonOnlyGrant(name string) bool {
	return name == PriorityDispatchCapability || name == DurableCapability || IsDangerousGrant(name)
}

// priorityDispatchTaskSQL is HasPriorityDispatch for the store's own
// queries: a task holding PriorityDispatchCapability or DurableCapability.
// It expects `t` bound to the task row and takes priorityDispatchArgs, in
// that order.
const priorityDispatchTaskSQL = "EXISTS (" +
	"SELECT 1 FROM `task_grant` AS `pg` WHERE `pg`.`task_id` = `t`.`id` AND `pg`.`capability` IN (?, ?))"

// priorityDispatchArgs is priorityDispatchTaskSQL's parameters.
func priorityDispatchArgs() []any {
	return []any{PriorityDispatchCapability, DurableCapability}
}

// durableTaskSQL is IsDurable for the store's own queries. It expects `t`
// bound to the task row and takes DurableCapability.
const durableTaskSQL = "EXISTS (SELECT 1 FROM `task_grant` AS `dg` WHERE `dg`.`task_id` = `t`.`id` AND `dg`.`capability` = ?)"

// taskIsDurable answers durableTaskSQL for one task id.
func taskIsDurable(ctx context.Context, q querier, taskID string) (bool, error) {
	var durable bool
	err := q.QueryRowContext(ctx, "SELECT "+durableTaskSQL+" FROM `task` AS `t` WHERE `t`.`id` = ?",
		DurableCapability, taskID).Scan(&durable)
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
		"WHERE `r`.`finished_at` IS NULL AND "+durableTaskSQL, DurableCapability).Scan(&n)
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
		append(priorityDispatchArgs(), taskID)...).Scan(&priority)
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
