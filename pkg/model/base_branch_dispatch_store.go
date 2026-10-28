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

// GetBaseBranchDispatch reads what grain last dispatched on one base
// branch, or nil (with no error) for a branch it has dispatched nothing
// on -- which is every branch until a repository names
// RepoConfig.PostMergeDispatchJobs, and is read as "there is a dispatch
// owed" rather than as an error. See BaseBranchDispatch.
func (s *Store) GetBaseBranchDispatch(ctx context.Context, repo RepoRef, branch string) (*BaseBranchDispatch, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT `commit`,`jobs`,`run_id`,`started_at`,`finished_at`,`conclusion`,`detail` "+
			"FROM `base_branch_dispatch` WHERE `owner` = ? AND `name` = ? AND `branch` = ?",
		repo.StoredOwner(), repo.Name, branch)
	b := BaseBranchDispatch{Repo: repo, Branch: branch}
	var jobs string
	var startedAt time.Time
	var finishedAt sql.NullTime
	if err := row.Scan(&b.Commit, &jobs, &b.RunID, &startedAt, &finishedAt,
		&b.Conclusion, &b.Detail); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading base branch dispatch for %s %s: %w", repo, branch, err)
	}
	b.Jobs = splitCSV(jobs)
	b.StartedAt = startedAt.UTC()
	if finishedAt.Valid {
		at := finishedAt.Time.UTC()
		b.FinishedAt = &at
	}
	return &b, nil
}

// ListBaseBranchDispatch returns every base branch grain has dispatched
// something on, ordered so a listing is stable. It is what the
// reconciler reads to find the run it has in flight; the set is one row
// per base branch of a repository that names post-merge dispatch jobs,
// which is a handful at most.
func (s *Store) ListBaseBranchDispatch(ctx context.Context) ([]BaseBranchDispatch, error) {
	var out []BaseBranchDispatch
	err := each(ctx, s.db,
		"SELECT `owner`,`name`,`branch`,`commit`,`jobs`,`run_id`,`started_at`,`finished_at`,"+
			"`conclusion`,`detail` FROM `base_branch_dispatch` ORDER BY `owner`,`name`,`branch`", nil,
		func(rows *sql.Rows) error {
			var b BaseBranchDispatch
			var jobs string
			var startedAt time.Time
			var finishedAt sql.NullTime
			if err := rows.Scan(&b.Repo.Owner, &b.Repo.Name, &b.Branch, &b.Commit, &jobs, &b.RunID,
				&startedAt, &finishedAt, &b.Conclusion, &b.Detail); err != nil {
				return err
			}
			b.Repo = RepoFromStored(b.Repo.Owner, b.Repo.Name)
			b.Jobs = splitCSV(jobs)
			b.StartedAt = startedAt.UTC()
			if finishedAt.Valid {
				at := finishedAt.Time.UTC()
				b.FinishedAt = &at
			}
			out = append(out, b)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing base branch dispatches: %w", err)
	}
	return out, nil
}

// PutBaseBranchDispatch replaces what is known about one base branch's
// dispatch wholesale -- one row per (repo, branch), the shape
// PutBaseBranchCI already has, and replaced for its reason: a fresh
// dispatch supersedes the last one entirely, including how it ended,
// because what this row answers is always "has the work that follows
// this branch been asked for at the tree it is on now".
func (s *Store) PutBaseBranchDispatch(ctx context.Context, b BaseBranchDispatch) error {
	return s.write(ctx, "record base branch dispatch for "+b.Repo.String()+" "+b.Branch, func(tx *sql.Tx) error {
		var finishedAt any
		if b.FinishedAt != nil {
			finishedAt = b.FinishedAt.UTC()
		}
		_, err := tx.ExecContext(ctx,
			"REPLACE INTO `base_branch_dispatch` (`owner`,`name`,`branch`,`commit`,`jobs`,`run_id`,"+
				"`started_at`,`finished_at`,`conclusion`,`detail`) VALUES (?,?,?,?,?,?,?,?,?,?)",
			b.Repo.StoredOwner(), b.Repo.Name, b.Branch, b.Commit, joinCSV(b.Jobs), b.RunID,
			b.StartedAt.UTC(), finishedAt, b.Conclusion, b.Detail)
		return err
	})
}
