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
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// GetMergeBatch reads the re-check in flight for one change on one base
// branch, or nil (with no error) where there is none. See MergeBatch.
func (s *Store) GetMergeBatch(ctx context.Context, repo RepoRef, branch, taskID string) (*MergeBatch, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT `ref`,`base_commit`,`commit`,`members`,`run_id`,`started_at`,`finished_at`,"+
			"`conclusion`,`detail`,`splits`,`queued`,`recheck_of`,`recheck_detail`,`recheck_at` FROM `merge_batch` "+
			"WHERE `owner` = ? AND `name` = ? AND `branch` = ? AND `task_id` = ?",
		repo.Owner, repo.Name, branch, taskID)
	b := MergeBatch{Repo: repo, Branch: branch, TaskID: taskID}
	var startedAt time.Time
	var finishedAt sql.NullTime
	var members string
	var recheckAt sql.NullTime
	if err := row.Scan(&b.Ref, &b.BaseCommit, &b.Commit, &members, &b.RunID, &startedAt,
		&finishedAt, &b.Conclusion, &b.Detail, &b.Splits, &b.Queued,
		&b.RecheckOf, &b.RecheckDetail, &recheckAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading the merge batch for %s %s: %w", repo, branch, err)
	}
	b.StartedAt = startedAt.UTC()
	if recheckAt.Valid {
		at := recheckAt.Time.UTC()
		b.RecheckAt = &at
	}
	if finishedAt.Valid {
		at := finishedAt.Time.UTC()
		b.FinishedAt = &at
	}
	decoded, err := decodeMergeBatchMembers(members)
	if err != nil {
		return nil, fmt.Errorf("reading the merge batch for %s %s: %w", repo, branch, err)
	}
	b.Members = decoded
	return &b, nil
}

// ListMergeBatches returns every re-check grain has in flight, ordered so
// a listing is stable. It is what the reconciler walks: one row per
// change being re-checked, which is a handful on the busiest deployment.
func (s *Store) ListMergeBatches(ctx context.Context) ([]MergeBatch, error) {
	var out []MergeBatch
	err := each(ctx, s.db,
		"SELECT `owner`,`name`,`branch`,`task_id`,`ref`,`base_commit`,`commit`,`members`,`run_id`,"+
			"`started_at`,`finished_at`,`conclusion`,`detail`,`splits`,`queued`,"+
			"`recheck_of`,`recheck_detail`,`recheck_at` "+
			"FROM `merge_batch` ORDER BY `owner`,`name`,`branch`,`task_id`", nil,
		func(rows *sql.Rows) error {
			var b MergeBatch
			var startedAt time.Time
			var finishedAt, recheckAt sql.NullTime
			var members string
			if err := rows.Scan(&b.Repo.Owner, &b.Repo.Name, &b.Branch, &b.TaskID, &b.Ref, &b.BaseCommit,
				&b.Commit, &members, &b.RunID, &startedAt, &finishedAt, &b.Conclusion,
				&b.Detail, &b.Splits, &b.Queued, &b.RecheckOf, &b.RecheckDetail, &recheckAt); err != nil {
				return err
			}
			b.StartedAt = startedAt.UTC()
			if recheckAt.Valid {
				at := recheckAt.Time.UTC()
				b.RecheckAt = &at
			}
			if finishedAt.Valid {
				at := finishedAt.Time.UTC()
				b.FinishedAt = &at
			}
			decoded, err := decodeMergeBatchMembers(members)
			if err != nil {
				return err
			}
			b.Members = decoded
			out = append(out, b)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing merge batches: %w", err)
	}
	return out, nil
}

// PutMergeBatch replaces the row for one change on one base branch
// wholesale, the shape PutBaseBranchCI has: a re-check is one indivisible
// statement about a tree, and the next re-check of that change supersedes
// all of it.
func (s *Store) PutMergeBatch(ctx context.Context, b MergeBatch) error {
	members, err := json.Marshal(b.Members)
	if err != nil {
		return fmt.Errorf("recording the merge batch for %s %s: %w", b.Repo, b.Branch, err)
	}
	return s.write(ctx, "record merge batch for "+b.Repo.String()+" "+b.Branch, func(tx *sql.Tx) error {
		var finishedAt, recheckAt any
		if b.FinishedAt != nil {
			finishedAt = b.FinishedAt.UTC()
		}
		if b.RecheckAt != nil {
			recheckAt = b.RecheckAt.UTC()
		}
		_, err := tx.ExecContext(ctx,
			"REPLACE INTO `merge_batch` (`owner`,`name`,`branch`,`task_id`,`ref`,`base_commit`,`commit`,"+
				"`members`,`run_id`,`started_at`,`finished_at`,`conclusion`,`detail`,`splits`,"+
				"`queued`,`recheck_of`,`recheck_detail`,`recheck_at`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
			b.Repo.Owner, b.Repo.Name, b.Branch, b.TaskID, b.Ref, b.BaseCommit, b.Commit,
			string(members), b.RunID, b.StartedAt.UTC(), finishedAt, b.Conclusion, b.Detail,
			b.Splits, b.Queued, b.RecheckOf, b.RecheckDetail, recheckAt)
		return err
	})
}

// DeleteMergeBatch forgets one change's re-check on one branch. A
// re-check is transient by nature -- it ends the moment its change
// merges, falls out, or the whole thing is abandoned -- and leaving the
// row behind would hold the change out of the queue for a tree nobody is
// testing.
func (s *Store) DeleteMergeBatch(ctx context.Context, repo RepoRef, branch, taskID string) error {
	return s.write(ctx, "clear merge batch for "+repo.String()+" "+branch+" "+taskID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"DELETE FROM `merge_batch` WHERE `owner` = ? AND `name` = ? AND `branch` = ? AND `task_id` = ?",
			repo.Owner, repo.Name, branch, taskID)
		return err
	})
}

// decodeMergeBatchMembers reads the members column, which is JSON rather
// than the comma-joined lists elsewhere in this file: a member is three
// fields, one of them a commit, and packing three fields into a delimited
// string is how a store grows a parser nobody meant to write.
func decodeMergeBatchMembers(s string) ([]MergeBatchMember, error) {
	if s == "" || s == "null" {
		return nil, nil
	}
	var out []MergeBatchMember
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, fmt.Errorf("decoding batch members: %w", err)
	}
	return out, nil
}
