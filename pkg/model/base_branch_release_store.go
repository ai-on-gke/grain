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

// GetBaseBranchRelease reads the release grain last started on one base
// branch, or nil (with no error) for a branch it has released nothing
// from -- which is every branch until a merge lands on one whose
// repository still publishes, and is read as "there is a release owed"
// rather than as an error. See BaseBranchRelease.
func (s *Store) GetBaseBranchRelease(ctx context.Context, repo RepoRef, branch string) (*BaseBranchRelease, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT `commit`,`run_id`,`started_at`,`finished_at`,`conclusion`,`detail`,"+
			"`failures`,`failing_since`,`attempts` "+
			"FROM `base_branch_release` WHERE `owner` = ? AND `name` = ? AND `branch` = ?",
		repo.StoredOwner(), repo.Name, branch)
	b := BaseBranchRelease{Repo: repo, Branch: branch}
	var startedAt time.Time
	var finishedAt, failingSince sql.NullTime
	if err := row.Scan(&b.Commit, &b.RunID, &startedAt, &finishedAt,
		&b.Conclusion, &b.Detail, &b.Failures, &failingSince, &b.Attempts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading base branch release for %s %s: %w", repo, branch, err)
	}
	b.StartedAt = startedAt.UTC()
	if finishedAt.Valid {
		at := finishedAt.Time.UTC()
		b.FinishedAt = &at
	}
	if failingSince.Valid {
		at := failingSince.Time.UTC()
		b.FailingSince = &at
	}
	return &b, nil
}

// ListBaseBranchRelease returns every base branch grain has published
// from, ordered so a listing is stable. It is what the reconciler reads
// to find the run it has in flight; the set is one row per base branch
// of a repository grain releases, which is a handful at most.
func (s *Store) ListBaseBranchRelease(ctx context.Context) ([]BaseBranchRelease, error) {
	var out []BaseBranchRelease
	err := each(ctx, s.db,
		"SELECT `owner`,`name`,`branch`,`commit`,`run_id`,`started_at`,`finished_at`,"+
			"`conclusion`,`detail`,`failures`,`failing_since`,`attempts` "+
			"FROM `base_branch_release` ORDER BY `owner`,`name`,`branch`", nil,
		func(rows *sql.Rows) error {
			var b BaseBranchRelease
			var startedAt time.Time
			var finishedAt, failingSince sql.NullTime
			if err := rows.Scan(&b.Repo.Owner, &b.Repo.Name, &b.Branch, &b.Commit, &b.RunID,
				&startedAt, &finishedAt, &b.Conclusion, &b.Detail,
				&b.Failures, &failingSince, &b.Attempts); err != nil {
				return err
			}
			b.Repo = RepoFromStored(b.Repo.Owner, b.Repo.Name)
			b.StartedAt = startedAt.UTC()
			if finishedAt.Valid {
				at := finishedAt.Time.UTC()
				b.FinishedAt = &at
			}
			if failingSince.Valid {
				at := failingSince.Time.UTC()
				b.FailingSince = &at
			}
			out = append(out, b)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing base branch releases: %w", err)
	}
	return out, nil
}

// PutBaseBranchRelease replaces what is known about one base branch's
// release wholesale -- one row per (repo, branch), the shape
// PutBaseBranchDispatch already has, and replaced for its reason: a
// fresh release supersedes the last one entirely, including how it
// ended, because what this row answers is always "has this branch been
// published from at the tree it is on now".
//
// The one thing that does not start again with each release is the run
// of failures on it (BaseBranchRelease.Failures): it is about the
// branch rather than about this attempt, so a caller writing a fresh
// row carries the old one's counters forward, which is what
// orchestrator.startBaseBranchRelease does. Writing zero there is
// saying "this branch is publishing again", and is the right thing to
// write only when one of its releases has succeeded.
func (s *Store) PutBaseBranchRelease(ctx context.Context, b BaseBranchRelease) error {
	return s.write(ctx, "record base branch release for "+b.Repo.String()+" "+b.Branch, func(tx *sql.Tx) error {
		var finishedAt, failingSince any
		if b.FinishedAt != nil {
			finishedAt = b.FinishedAt.UTC()
		}
		if b.FailingSince != nil {
			failingSince = b.FailingSince.UTC()
		}
		_, err := tx.ExecContext(ctx,
			"REPLACE INTO `base_branch_release` (`owner`,`name`,`branch`,`commit`,`run_id`,"+
				"`started_at`,`finished_at`,`conclusion`,`detail`,`failures`,`failing_since`,`attempts`) "+
				"VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
			b.Repo.StoredOwner(), b.Repo.Name, b.Branch, b.Commit, b.RunID,
			b.StartedAt.UTC(), finishedAt, b.Conclusion, b.Detail, b.Failures, failingSince, b.Attempts)
		return err
	})
}
