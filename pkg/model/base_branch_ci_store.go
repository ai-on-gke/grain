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
	"time"
)

// baseBranchCIStreakColumns are BaseBranchCI.RedStreak and the fields
// beside it, in the order every read and write here names them.
const baseBranchCIStreakColumns = "`red_streak`,`red_streak_jobs`,`red_streak_since`,`red_streak_tip`," +
	"`last_green_commit`,`red_streak_woken`"

// GetBaseBranchCI reads what grain CI last made of one branch changes
// merge into, or nil (with no error) for a branch nothing has merged into
// under grain's eye -- which is every branch on a deployment that has
// just upgraded across this, and is read as "not broken" rather than as
// "unknown, hold everything". See BaseBranchCI.
func (s *Store) GetBaseBranchCI(ctx context.Context, repo RepoRef, branch string) (*BaseBranchCI, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT `run_id`,`commit`,`started_at`,`finished_at`,`attempts`,`conclusion`,`detail`,"+
			"`merged_task_id`,`pending_task_id`,`repair_task_id`,`confirmation`,`confirm_run_id`,"+
			"`confirm_jobs`,`confirm_attempts`,`confirm_detail`,`covers`,`pending_merges`,"+
			"`run_requested_at`,`run_requested_by`,"+baseBranchCIStreakColumns+" "+
			"FROM `base_branch_ci` "+
			"WHERE `owner` = ? AND `name` = ? AND `branch` = ?",
		repo.StoredOwner(), repo.Name, branch)
	b := BaseBranchCI{Repo: repo, Branch: branch}
	var startedAt time.Time
	var finishedAt, requestedAt sql.NullTime
	var confirmJobs, streakJobs string
	if err := row.Scan(&b.RunID, &b.Commit, &startedAt, &finishedAt, &b.Attempts, &b.Conclusion, &b.Detail,
		&b.MergedTaskID, &b.PendingTaskID, &b.RepairTaskID, &b.Confirmation, &b.ConfirmRunID, &confirmJobs,
		&b.ConfirmAttempts, &b.ConfirmDetail, &b.Covers, &b.PendingMerges,
		&requestedAt, &b.RunRequestedBy,
		&b.RedStreak, &streakJobs, &b.RedStreakSince, &b.RedStreakTip, &b.LastGreenCommit, &b.RedStreakWoken); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading base branch CI for %s %s: %w", repo, branch, err)
	}
	b.StartedAt = startedAt.UTC()
	if finishedAt.Valid {
		at := finishedAt.Time.UTC()
		b.FinishedAt = &at
	}
	if requestedAt.Valid {
		at := requestedAt.Time.UTC()
		b.RunRequestedAt = &at
	}
	b.ConfirmJobs = splitCSV(confirmJobs)
	b.RedStreakJobs = splitCSV(streakJobs)
	return &b, nil
}

// ListBaseBranchCI returns every branch grain is watching this way,
// ordered so a listing is stable. It is what the reconciler walks: the
// set is one row per base branch that has had a merge, which is a handful
// even on a busy deployment.
func (s *Store) ListBaseBranchCI(ctx context.Context) ([]BaseBranchCI, error) {
	var out []BaseBranchCI
	err := each(ctx, s.db,
		"SELECT `owner`,`name`,`branch`,`run_id`,`commit`,`started_at`,`finished_at`,`attempts`,"+
			"`conclusion`,`detail`,`merged_task_id`,`pending_task_id`,`repair_task_id`,"+
			"`confirmation`,`confirm_run_id`,`confirm_jobs`,`confirm_attempts`,`confirm_detail`,"+
			"`covers`,`pending_merges`,`run_requested_at`,`run_requested_by`,"+baseBranchCIStreakColumns+" "+
			"FROM `base_branch_ci` ORDER BY `owner`,`name`,`branch`", nil,
		func(rows *sql.Rows) error {
			var b BaseBranchCI
			var startedAt time.Time
			var finishedAt, requestedAt sql.NullTime
			var confirmJobs, streakJobs string
			if err := rows.Scan(&b.Repo.Owner, &b.Repo.Name, &b.Branch, &b.RunID, &b.Commit,
				&startedAt, &finishedAt, &b.Attempts, &b.Conclusion, &b.Detail, &b.MergedTaskID,
				&b.PendingTaskID, &b.RepairTaskID, &b.Confirmation, &b.ConfirmRunID, &confirmJobs,
				&b.ConfirmAttempts, &b.ConfirmDetail, &b.Covers, &b.PendingMerges,
				&requestedAt, &b.RunRequestedBy,
				&b.RedStreak, &streakJobs, &b.RedStreakSince, &b.RedStreakTip, &b.LastGreenCommit, &b.RedStreakWoken); err != nil {
				return err
			}
			b.Repo = RepoFromStored(b.Repo.Owner, b.Repo.Name)
			b.StartedAt = startedAt.UTC()
			if finishedAt.Valid {
				at := finishedAt.Time.UTC()
				b.FinishedAt = &at
			}
			if requestedAt.Valid {
				at := requestedAt.Time.UTC()
				b.RunRequestedAt = &at
			}
			b.ConfirmJobs = splitCSV(confirmJobs)
			b.RedStreakJobs = splitCSV(streakJobs)
			out = append(out, b)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing base branch CI: %w", err)
	}
	return out, nil
}

// PutBaseBranchCI replaces what is known about one base branch wholesale
// -- one row per (repo, branch), replaced rather than diffed, the shape
// PutRepoConfig and PutPipelinePause already have.
//
// Replacing is what makes a fresh run the whole truth: a merge that
// starts a new run on this branch supersedes the last one's verdict,
// including a failure, because what is being asked is always "is the
// branch broken *now*".
func (s *Store) PutBaseBranchCI(ctx context.Context, b BaseBranchCI) error {
	return s.write(ctx, "record base branch CI for "+b.Repo.String()+" "+b.Branch, func(tx *sql.Tx) error {
		var finishedAt, requestedAt any
		if b.FinishedAt != nil {
			finishedAt = b.FinishedAt.UTC()
		}
		if b.RunRequestedAt != nil {
			requestedAt = b.RunRequestedAt.UTC()
		}
		_, err := tx.ExecContext(ctx,
			"REPLACE INTO `base_branch_ci` (`owner`,`name`,`branch`,`run_id`,`commit`,`started_at`,"+
				"`finished_at`,`attempts`,`conclusion`,`detail`,`merged_task_id`,`pending_task_id`,"+
				"`repair_task_id`,`confirmation`,`confirm_run_id`,`confirm_jobs`,`confirm_attempts`,"+
				"`confirm_detail`,`covers`,`pending_merges`,`run_requested_at`,"+
				"`run_requested_by`,"+baseBranchCIStreakColumns+") "+
				"VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
			b.Repo.StoredOwner(), b.Repo.Name, b.Branch, b.RunID, b.Commit, b.StartedAt.UTC(),
			finishedAt, b.Attempts, b.Conclusion, b.Detail, b.MergedTaskID, b.PendingTaskID, b.RepairTaskID,
			b.Confirmation, b.ConfirmRunID, joinCSV(b.ConfirmJobs), b.ConfirmAttempts, b.ConfirmDetail,
			b.Covers, b.PendingMerges, requestedAt, b.RunRequestedBy,
			b.RedStreak, joinCSV(b.RedStreakJobs), b.RedStreakSince, b.RedStreakTip, b.LastGreenCommit, b.RedStreakWoken)
		return err
	})
}

// RequestBaseBranchCIRun records that an operator has asked for a
// base-branch run on one branch grain is already watching
// (BaseBranchCI.RunRequestedAt), and returns the row as it then stands --
// or nil, with no error, for a branch there is no row for.
//
// It writes those two columns and nothing else, where everything else
// about this table replaces the row wholesale (PutBaseBranchCI). The
// difference is who is writing: the reconciler owns the row and is the
// only thing that ever holds a whole one, while this is a request
// arriving from an HTTP handler that read the row some milliseconds ago
// -- and a cycle that started a run in between must not have that run
// wiped out by a read-modify-write of a row it has already replaced.
func (s *Store) RequestBaseBranchCIRun(ctx context.Context, repo RepoRef, branch string,
	at time.Time, by string) (*BaseBranchCI, error) {

	err := s.write(ctx, "ask for a base branch run on "+repo.String()+" "+branch, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE `base_branch_ci` SET `run_requested_at` = ?, `run_requested_by` = ? "+
				"WHERE `owner` = ? AND `name` = ? AND `branch` = ?",
			at.UTC(), by, repo.StoredOwner(), repo.Name, branch)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("asking for a base branch run on %s %s: %w", repo, branch, err)
	}
	return s.GetBaseBranchCI(ctx, repo, branch)
}
