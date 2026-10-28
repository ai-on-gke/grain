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

// What the merge queue had to do over again because something else moved
// (grain/task-3227): the base merged into a head whose verdict had gone
// stale, and a suite run a second time -- after that merge, as an uncached
// recheck of a red that may be a flake, or on half of a batch that failed.
//
// Recorded as it happens rather than derived afterwards. The merges
// report's contention section (metrics.MergeQueueContention) used to read
// "rebases" off TaskTiming.FixTaskFiled, which is a repair rather than a
// rebase, and "retests" off MergeBatchRecord.Splits, which is a halving
// depth rather than a count of suites -- and the in-memory tracker meant to
// say both was never instantiated by anything, so neither was ever
// measured. Nothing else keeps these events: a refresh is one stamp on the
// observation that the next refresh overwrites, and a recheck replaces the
// verdict it re-checked.
//
// Bounded like the batch and gather histories beside it, and for their
// reason: this is a sample to read a rate off, not an audit log.

// MergeQueueContentionKept is how many events keep their record. Larger
// than MergeBatchHistoryKept, since one batch can cost several of these
// (a recheck, then a split, then a refresh of the member it blamed).
const MergeQueueContentionKept = 1000

// The two kinds of event, which are the two counters of
// metrics.RebaseRetestContention.
const (
	// MergeQueueRebase is the queue merging a pull request's base branch
	// into its head (orchestrator.refreshStaleHead, bringHeadUpToDate):
	// the change has to be judged again on a tree it did not have.
	MergeQueueRebase = "rebase"
	// MergeQueueRetest is a suite the queue ran again about a change it
	// had already judged once.
	MergeQueueRetest = "retest"
)

// Why an event happened, recorded beside its kind so that a rate can be
// read apart into what drove it.
const (
	// MergeQueueStaleHead: a red or stale verdict about a head that has
	// fallen behind its base (orchestrator.refreshStaleHead).
	MergeQueueStaleHead = "stale head"
	// MergeQueueUpToDate: the repository requires branches to be up to
	// date before they merge (orchestrator.bringHeadUpToDate).
	MergeQueueUpToDate = "up to date"
	// MergeQueueAfterRebase: the suite a rebase above started, recorded as
	// a retest in its own right since it is one -- the verdict the change
	// held is thrown away and CI starts over.
	MergeQueueAfterRebase = "after rebase"
	// MergeQueueFlakeRecheck: an uncached re-run of a red job before it is
	// charged to the change (orchestrator.startCIRecheck) or to a batch
	// (orchestrator.recheckMergeBatch).
	MergeQueueFlakeRecheck = "flake recheck"
	// MergeQueueBatchSplit: half of a failed batch tested again
	// (orchestrator.bisectMergeBatch).
	MergeQueueBatchSplit = "batch split"
)

// MergeQueueContentionEvent is one such event.
type MergeQueueContentionEvent struct {
	// ID is the row's own autoincrement, which is also the order the
	// events happened in.
	ID   int64
	Repo RepoRef
	// TaskID is the change the event was about, and "" for one about a
	// whole batch (a batch's recheck or split), which has no one member to
	// charge it to.
	TaskID string
	Kind   string
	Reason string
	At     time.Time
}

// RecordMergeQueueContention appends one event and trims the history back
// to MergeQueueContentionKept rows in the same write, for
// RecordMergeBatch's reason: a history bounded by a separate sweep is
// unbounded whenever the sweep is not running.
func (s *Store) RecordMergeQueueContention(ctx context.Context, e MergeQueueContentionEvent) error {
	return s.write(ctx, "record merge queue "+e.Kind+" for "+e.Repo.String(),
		func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO `merge_queue_contention` (`owner`,`name`,`task_id`,`kind`,`reason`,`at`) "+
					"VALUES (?,?,?,?,?,?)",
				e.Repo.Owner, e.Repo.Name, e.TaskID, e.Kind, e.Reason, e.At.UTC()); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx,
				"DELETE FROM `merge_queue_contention` WHERE `id` NOT IN "+
					"(SELECT `id` FROM `merge_queue_contention` ORDER BY `id` DESC LIMIT ?)",
				MergeQueueContentionKept)
			return err
		})
}

// MergeQueueContentionEvents is the whole kept history, newest first -- at
// most MergeQueueContentionKept rows, so it is read entire rather than
// paged, and windowed by the caller (metrics.Merges).
func (s *Store) MergeQueueContentionEvents(ctx context.Context) ([]MergeQueueContentionEvent, error) {
	var out []MergeQueueContentionEvent
	err := each(ctx, s.db,
		"SELECT `id`,`owner`,`name`,`task_id`,`kind`,`reason`,`at` "+
			"FROM `merge_queue_contention` ORDER BY `id` DESC", nil,
		func(rows *sql.Rows) error {
			var e MergeQueueContentionEvent
			var at time.Time
			if err := rows.Scan(&e.ID, &e.Repo.Owner, &e.Repo.Name, &e.TaskID, &e.Kind, &e.Reason, &at); err != nil {
				return err
			}
			e.At = at.UTC()
			out = append(out, e)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading the merge queue contention history: %w", err)
	}
	return out, nil
}
