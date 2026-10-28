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

// Every merge grain performed because an operator said so rather than
// because the predicate allowed it (grain/task-479, and the predicate's
// own account of the exception in orchestrator's mergepredicate.go).
//
// The override is not new; recording it is. It was already being done by
// hand -- a person merging the pull request underneath grain, half a
// dozen times in a day -- which left no trace anywhere grain could read
// and no way to answer the one question an operator asked of their own
// control: "it should be rare". A rarity nobody counts is an opinion.
//
// So one row per forced merge that actually happened, written by the
// cycle that performed it, and bounded rather than kept forever -- the
// batch history beside it (MergeBatchRecord, grain/task-300) is the
// precedent, and the reasoning carries over unchanged: this is a sample
// to read a rate off, not an audit log. What is permanent is the note on
// the task and the note on the pull request, which is where "why did
// this land?" is answered for one change. These rows answer the other
// question, the one about the deployment: how often.

// ForceMergeHistoryKept is how many forced merges keep their record. The
// same two hundred as MergeBatchHistoryKept, and on a deployment where
// forcing stays as rare as it is meant to be that is years of them -- a
// history that has wrapped is itself the answer to "is this still rare?"
const ForceMergeHistoryKept = 200

// ForceMergeRecord is one merge that happened on an operator's override.
//
// It holds the whole of the override rather than pointing at the task:
// the reason, who asked, and what was not green at the time, because a
// task can be deleted and because a counter whose rows have to be joined
// against live tasks to mean anything is one nobody reads. The task id is
// here too, for the reader who wants to go and look.
type ForceMergeRecord struct {
	// ID is the row's own autoincrement, which is also the order the
	// merges happened in -- rows are appended as they are performed.
	ID     int64
	TaskID string
	// Repo and Number are the pull request that was merged, and Branch
	// the branch it was merged into: what it landed on is as much of the
	// blast radius as a row can carry.
	Repo   RepoRef
	Number int
	Branch string
	// Commit is the head that merged, so a row can still be traced to a
	// tree after the task has gone.
	Commit string
	// Reason is what the operator typed, By is who they were, and
	// Skipped is the predicate's own refusal at the time -- which check
	// was not green and what it said. Never empty, any of them, for a row
	// written by grain: a force merge with no reason is refused before it
	// reaches here (ui.Client.ForceMerge).
	Reason  string
	By      string
	Skipped string
	// RequestedAt is when the operator asked and MergedAt when grain
	// performed it. They are usually a cycle apart, and the gap is worth
	// keeping: a long one means the override sat waiting on something --
	// most often a conflict, which no override can authorise away.
	RequestedAt time.Time
	MergedAt    time.Time
}

// CountForceMergesSince is the counter the whole history exists for:
// how many of these rows happened at or after a cutoff. "It should be
// rare" is checkable as "N in the last seven days" and not otherwise.
//
// Records are counted by MergedAt rather than RequestedAt, because what
// is being counted is merges that bypassed the predicate rather than
// intentions to have one.
func CountForceMergesSince(records []ForceMergeRecord, cutoff time.Time) int {
	var n int
	for _, r := range records {
		if !r.MergedAt.Before(cutoff) {
			n++
		}
	}
	return n
}

// RecordForceMerge appends one forced merge to the history and trims it
// back to ForceMergeHistoryKept rows in the same write, for
// RecordMergeBatch's reason: a history bounded by a separate sweep is
// unbounded whenever the sweep is not running.
//
// Called only after GitHub has actually merged the pull request. A row
// here is a merge that happened, which is what makes the count a count of
// merges rather than of buttons pressed.
func (s *Store) RecordForceMerge(ctx context.Context, r ForceMergeRecord) error {
	return s.write(ctx, "record forced merge of "+r.Repo.String(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `force_merge_history` (`task_id`,`owner`,`name`,`number`,`branch`,"+
				"`commit`,`reason`,`requested_by`,`skipped`,`requested_at`,`merged_at`) "+
				"VALUES (?,?,?,?,?,?,?,?,?,?,?)",
			r.TaskID, r.Repo.Owner, r.Repo.Name, r.Number, r.Branch, r.Commit,
			r.Reason, r.By, r.Skipped, r.RequestedAt.UTC(), r.MergedAt.UTC()); err != nil {
			return err
		}
		// Newest kept by id, which is both the insertion order and the
		// order they happened in, so the bound never drops a row newer
		// than one it keeps.
		_, err := tx.ExecContext(ctx,
			"DELETE FROM `force_merge_history` WHERE `id` NOT IN "+
				"(SELECT `id` FROM `force_merge_history` ORDER BY `id` DESC LIMIT ?)",
			ForceMergeHistoryKept)
		return err
	})
}

// ForceMergeRecords is the whole kept history, newest first -- at most
// ForceMergeHistoryKept rows, so it is read entire rather than paged.
func (s *Store) ForceMergeRecords(ctx context.Context) ([]ForceMergeRecord, error) {
	var out []ForceMergeRecord
	err := each(ctx, s.db,
		"SELECT `id`,`task_id`,`owner`,`name`,`number`,`branch`,`commit`,`reason`,"+
			"`requested_by`,`skipped`,`requested_at`,`merged_at` "+
			"FROM `force_merge_history` ORDER BY `id` DESC", nil,
		func(rows *sql.Rows) error {
			var r ForceMergeRecord
			var requested, merged time.Time
			if err := rows.Scan(&r.ID, &r.TaskID, &r.Repo.Owner, &r.Repo.Name, &r.Number,
				&r.Branch, &r.Commit, &r.Reason, &r.By, &r.Skipped,
				&requested, &merged); err != nil {
				return err
			}
			r.RequestedAt, r.MergedAt = requested.UTC(), merged.UTC()
			out = append(out, r)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading the forced merge history: %w", err)
	}
	return out, nil
}
