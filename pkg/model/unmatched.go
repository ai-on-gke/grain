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

// What a provider said when grain could not tell what it meant.
//
// pkg/agent reads a failed run's own account of itself against two lists
// of markers -- one for a usage limit (usagelimit.go), one for a
// provider with no capacity (capacity.go) -- and a match spares the task
// a failure it did not earn. Every marker in those lists is a phrase
// somebody else's product writes, which somebody else's product is free
// to reword; when one is reworded, nothing matches, the run is recorded
// as an ordinary failure, and no test anywhere notices, because every
// test asserts the wording as it was written down. What an operator sees
// instead is five ordinary failures in a row and a task retired
// (MaxConsecutiveFailures) for a provider's bad afternoon.
//
// An UnmatchedFailure is the row that makes that visible: for every run
// that failed and matched neither list, the tail of what it actually
// said. One row per failed run, written after the fact by
// orchestrator.RunDispatch and never updated, and read back a window at
// a time (UnmatchedFailures) -- because the thing worth noticing is not
// any single row but a week of them with the same unfamiliar sentence in
// it.

import (
	"context"
	"database/sql"
	"time"
)

// UnmatchedFailure is one failed run whose text matched none of grain's
// markers for a provider refusal.
//
// Text is bounded when it is written (agent.UnmatchedFailureTail), not
// here: this is model output, and the record has to have a size whatever
// a provider decides to say.
//
// Suspect is agent.ResemblesRefusal's loose read of that same text --
// "somebody looking for a marker that stopped matching should start
// here". It is stored rather than recomputed on read so that the
// judgement travels with the row: the loose vocabulary will itself be
// edited over time, and a report that re-judged old rows by today's
// words would quietly rewrite what grain thought last month.
type UnmatchedFailure struct {
	RunID     string
	TaskID    string
	Framework string
	At        time.Time
	Text      string
	Suspect   bool
}

// RecordUnmatchedFailure stores one, replacing anything already recorded
// for that run.
//
// Replacing rather than adding, for RecordRunTelemetry's own reason: the
// write happens once per run, and a retry of it must leave one row
// rather than two.
//
// Recording it must never cost a run -- the caller logs a failure and
// carries on. A run that has already failed is not made worse by grain
// failing to write down what it said, and an instrument that could fail
// the work it observes is one an operator would eventually turn off.
func (s *Store) RecordUnmatchedFailure(ctx context.Context, f UnmatchedFailure) error {
	return s.write(ctx, "record run "+f.RunID+"'s unmatched failure", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM `task_run_unmatched_failure` WHERE `run_id` = ?", f.RunID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			"INSERT INTO `task_run_unmatched_failure` "+
				"(`run_id`,`task_id`,`framework`,`at`,`text`,`suspect`) VALUES (?,?,?,?,?,?)",
			f.RunID, f.TaskID, f.Framework, f.At.UTC(), f.Text, f.Suspect)
		return err
	})
}

// UnmatchedFailures is every unmatched failure recorded at or after
// since, newest first, at most limit of them.
//
// Newest first because the question is "has something changed?", and a
// rewording announces itself at the recent end. Bounded by limit for the
// reason the text itself is bounded: the number of failures a week holds
// is the deployment's business and not this reader's, and a report is
// worth nothing if it cannot be rendered. A caller that passes a limit of
// zero or less gets DefaultUnmatchedFailureLimit.
//
// A window on `at` rather than on the run's own finished_at, which the
// census tables are windowed by: those rows are measurements of a run and
// belong to it, and these are log lines that happen to name one.
func (s *Store) UnmatchedFailures(ctx context.Context, since time.Time, limit int) ([]UnmatchedFailure, error) {
	if limit <= 0 {
		limit = DefaultUnmatchedFailureLimit
	}
	var out []UnmatchedFailure
	err := each(ctx, s.db,
		"SELECT `run_id`,`task_id`,`framework`,`at`,`text`,`suspect` "+
			"FROM `task_run_unmatched_failure` WHERE `at` >= ? "+
			"ORDER BY `at` DESC, `run_id` DESC LIMIT ?",
		[]any{since.UTC(), limit},
		func(rows *sql.Rows) error {
			var f UnmatchedFailure
			if err := rows.Scan(&f.RunID, &f.TaskID, &f.Framework, &f.At, &f.Text, &f.Suspect); err != nil {
				return err
			}
			f.At = f.At.UTC()
			out = append(out, f)
			return nil
		})
	return out, err
}

// DefaultUnmatchedFailureLimit is how many rows a reader that names no
// bound gets: enough that a week of a busy deployment's failures is
// whole, few enough that the answer stays a page rather than a dump.
const DefaultUnmatchedFailureLimit = 500
