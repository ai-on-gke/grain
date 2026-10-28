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

// What the merge queue's gather window actually did (grain/task-440),
// which until this nothing recorded at all: a hold is process state
// (orchestrator.gatherHold), and its only visible trace was the
// `gathering` row on the held task, overwritten the moment the hold
// ended.
//
// The question it exists to answer is the one branch of
// docs/merge-queue-batching.md's decision rule that the batch counters
// beside it cannot answer. "Keep it" and "turn it off" are read off
// MergeBatchRecord -- batches formed, changes landed. "Shorten it if
// holds routinely run the full window and expire, because the CI run it
// was waiting for is simply slower than the window" is about the holds
// themselves, and most holds never become a batch, so there is no batch
// row for them to be counted on. grain/task-439 went to take that
// reading and found no evidence source at all.
//
// A record of its own rather than a field on a batch, for that reason: a
// hold is not a batch, and the ones worth counting most are exactly the
// ones no batch came of.
//
// Bounded rather than kept forever, like the batch history beside it.
// This is a sample to read a rate off, and it is read to tune a duration
// that is expected to change -- so rows ageing out is a feature: each one
// carries the window it was held under, and a history that kept
// everything would average the window before a change with the window
// after it.

// MergeGatherHistoryKept is how many ended holds keep their record. The
// same two hundred as MergeBatchHistoryKept, and cheaper per row: on a
// deployment where three merges in five land alone, this fills faster
// than the batch history does, which is the point -- the reading it is
// for wants a sample of holds, not of batches.
const MergeGatherHistoryKept = 200

// The three ways a hold ends, which are the three ends of
// orchestrator.applyGatherHolds. What they partition is "did the wait
// pay?": one of them is the wait paying, one is the wait being given up
// on early, and one is the wait being spent in full for nothing.
const (
	// MergeGatherBatched: the run the hold was waiting for reported a
	// pass and the change went into a batch. This is the wait paying off,
	// and MergeGatherRecord.Waited is how long it took to pay.
	MergeGatherBatched = "batched"
	// MergeGatherReleased: the hold ended before its window, because what
	// it was waiting for stopped being worth waiting for -- the run
	// reported something other than a pass, the entry stopped being a
	// candidate, or the queue behind it emptied. The change merges on the
	// next tick; the latency cost is Waited and it bought nothing.
	MergeGatherReleased = "released"
	// MergeGatherExpired: the window ran out with the run still going.
	// This is the case the window exists to bound, and a history that is
	// mostly these is the signal to shorten it: the run being waited for
	// is simply slower than the window, so the wait is latency with no
	// batch at the end of it.
	MergeGatherExpired = "expired"
)

// MergeGatherRecord is one hold that has ended, flattened to what a
// person asking "was the wait worth taking?" has to count.
//
// No head sha and no base branch: the head is what makes a hold
// once-only and is of no interest once the hold is over, and the branch
// is not known on every path a hold ends by (a hold is released from the
// queue entry alone, which has no pull request detail on it). What is
// here is the bet -- how long the wait was allowed to be, what it was
// waiting for -- and how it came out.
type MergeGatherRecord struct {
	// ID is the row's own autoincrement, which is also the order the
	// holds ended in.
	ID     int64
	Repo   RepoRef
	Number int
	// Waiting is the task whose grain CI run the change was being held
	// for, as the hold last read it. It is here so that a row can be
	// traced back to the two changes it was about; the counters do not
	// use it.
	Waiting string
	// Window is the bound this hold was armed under
	// (Config.MergeGatherWindow at the time). Recorded per hold rather
	// than assumed from the setting, because the whole reason to read
	// this history is to consider changing that setting, and a row from
	// before the change must not be read against the value after it.
	Window time.Duration
	// Outcome is one of the three above.
	Outcome string
	// ArmedAt and EndedAt bound the wait itself. EndedAt - ArmedAt is
	// what the change actually paid in latency, which for an expired hold
	// is the window plus up to one reconcile cycle -- the queue notices
	// the expiry on a tick, not on a timer.
	ArmedAt, EndedAt time.Time
}

// Waited is how long this change was actually held.
func (r MergeGatherRecord) Waited() time.Duration {
	if r.EndedAt.Before(r.ArmedAt) {
		return 0
	}
	return r.EndedAt.Sub(r.ArmedAt)
}

// MergeGatherStats is a set of counters over that history -- the answer
// to "did the wait pay?", in the smallest set of numbers that can say
// either yes or no.
//
// Counting rather than statistics, like MergeBatchStats: totals and
// counts, from which a caller computes the two means it wants
// (BatchedWaited/Batched and ExpiredWaited/Expired). A caller that wants
// a distribution has the rows.
type MergeGatherStats struct {
	// Holds is how many ended holds the history holds, and Batched,
	// Released and Expired partition it by outcome.
	Holds    int
	Batched  int
	Released int
	Expired  int
	// Waited is the latency every one of those holds cost, summed;
	// BatchedWaited and ExpiredWaited are that same sum restricted to the
	// holds that paid off and to the holds that ran the full window.
	//
	// The two of them are what the "shorten it" branch of the rule is
	// read off. BatchedWaited/Batched is how long a wait that works
	// actually takes: a window much longer than that is buying nothing
	// but the expiries. ExpiredWaited is what those expiries cost.
	Waited        time.Duration
	BatchedWaited time.Duration
	ExpiredWaited time.Duration
	// LongestWindow is the largest window any of these holds was armed
	// under, so a reader can tell whether the history is about the
	// setting as it stands now or about one it has since been changed
	// from.
	LongestWindow time.Duration
	// FirstAt and LastAt bound the history, so a reader knows whether
	// these counters are about a week or about an hour.
	FirstAt *time.Time
	LastAt  *time.Time
}

// SummarizeMergeGathers counts a history of ended holds.
func SummarizeMergeGathers(records []MergeGatherRecord) MergeGatherStats {
	var s MergeGatherStats
	for _, r := range records {
		s.Holds++
		waited := r.Waited()
		s.Waited += waited
		if r.Window > s.LongestWindow {
			s.LongestWindow = r.Window
		}
		switch r.Outcome {
		case MergeGatherBatched:
			s.Batched++
			s.BatchedWaited += waited
		case MergeGatherReleased:
			s.Released++
		case MergeGatherExpired:
			s.Expired++
			s.ExpiredWaited += waited
		}
		at := r.EndedAt
		if s.FirstAt == nil || at.Before(*s.FirstAt) {
			first := at
			s.FirstAt = &first
		}
		if s.LastAt == nil || at.After(*s.LastAt) {
			last := at
			s.LastAt = &last
		}
	}
	return s
}

// RecordMergeGather appends one ended hold to the history and trims it
// back to MergeGatherHistoryKept rows in the same write, for
// RecordMergeBatch's reason: a history bounded by a separate sweep is
// unbounded whenever the sweep is not running.
//
// Never called for a hold still standing -- a row here is a wait that is
// over, which is what makes the counters countable.
func (s *Store) RecordMergeGather(ctx context.Context, r MergeGatherRecord) error {
	return s.write(ctx, "record ended merge gather hold for "+r.Repo.String(),
		func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO `merge_gather_history` (`owner`,`name`,`number`,`waiting`,"+
					"`window_ms`,`outcome`,`armed_at`,`ended_at`) VALUES (?,?,?,?,?,?,?,?)",
				r.Repo.Owner, r.Repo.Name, r.Number, r.Waiting,
				r.Window.Milliseconds(), r.Outcome,
				r.ArmedAt.UTC(), r.EndedAt.UTC()); err != nil {
				return err
			}
			// Newest kept by id, which is both the insertion order and the
			// order they ended in, so the bound never drops a row newer
			// than one it keeps.
			_, err := tx.ExecContext(ctx,
				"DELETE FROM `merge_gather_history` WHERE `id` NOT IN "+
					"(SELECT `id` FROM `merge_gather_history` ORDER BY `id` DESC LIMIT ?)",
				MergeGatherHistoryKept)
			return err
		})
}

// MergeGatherRecords is the whole kept history, newest first -- at most
// MergeGatherHistoryKept rows, so it is read entire rather than paged.
func (s *Store) MergeGatherRecords(ctx context.Context) ([]MergeGatherRecord, error) {
	var out []MergeGatherRecord
	err := each(ctx, s.db,
		"SELECT `id`,`owner`,`name`,`number`,`waiting`,`window_ms`,`outcome`,"+
			"`armed_at`,`ended_at` FROM `merge_gather_history` ORDER BY `id` DESC", nil,
		func(rows *sql.Rows) error {
			var r MergeGatherRecord
			var windowMS int64
			var armed, ended time.Time
			if err := rows.Scan(&r.ID, &r.Repo.Owner, &r.Repo.Name, &r.Number, &r.Waiting,
				&windowMS, &r.Outcome, &armed, &ended); err != nil {
				return err
			}
			r.Window = time.Duration(windowMS) * time.Millisecond
			r.ArmedAt, r.EndedAt = armed.UTC(), ended.UTC()
			out = append(out, r)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading the merge gather history: %w", err)
	}
	return out, nil
}
