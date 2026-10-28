package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// GetMergeBatch reads the batch in flight for one base branch, or nil
// (with no error) where there is none -- which is every branch on a
// deployment that has just upgraded across this, and every branch between
// batches. See MergeBatch.
func (s *Store) GetMergeBatch(ctx context.Context, repo RepoRef, branch string) (*MergeBatch, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT `ref`,`base_commit`,`commit`,`members`,`run_id`,`started_at`,`finished_at`,"+
			"`conclusion`,`detail`,`splits`,`queued`,`recheck_of`,`recheck_detail`,`recheck_at` FROM `merge_batch` "+
			"WHERE `owner` = ? AND `name` = ? AND `branch` = ?",
		repo.Owner, repo.Name, branch)
	b := MergeBatch{Repo: repo, Branch: branch}
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

// ListMergeBatches returns every batch grain has in flight, ordered so a
// listing is stable. It is what the reconciler walks: one row per base
// branch with a batch on it, which is a handful on the busiest
// deployment.
func (s *Store) ListMergeBatches(ctx context.Context) ([]MergeBatch, error) {
	var out []MergeBatch
	err := each(ctx, s.db,
		"SELECT `owner`,`name`,`branch`,`ref`,`base_commit`,`commit`,`members`,`run_id`,"+
			"`started_at`,`finished_at`,`conclusion`,`detail`,`splits`,`queued`,"+
			"`recheck_of`,`recheck_detail`,`recheck_at` "+
			"FROM `merge_batch` ORDER BY `owner`,`name`,`branch`", nil,
		func(rows *sql.Rows) error {
			var b MergeBatch
			var startedAt time.Time
			var finishedAt, recheckAt sql.NullTime
			var members string
			if err := rows.Scan(&b.Repo.Owner, &b.Repo.Name, &b.Branch, &b.Ref, &b.BaseCommit,
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

// PutMergeBatch replaces the row for one base branch wholesale, the shape
// PutBaseBranchCI has: a batch is one indivisible statement about a tree,
// and the next batch on that branch supersedes all of it.
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
			"REPLACE INTO `merge_batch` (`owner`,`name`,`branch`,`ref`,`base_commit`,`commit`,"+
				"`members`,`run_id`,`started_at`,`finished_at`,`conclusion`,`detail`,`splits`,"+
				"`queued`,`recheck_of`,`recheck_detail`,`recheck_at`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
			b.Repo.Owner, b.Repo.Name, b.Branch, b.Ref, b.BaseCommit, b.Commit,
			string(members), b.RunID, b.StartedAt.UTC(), finishedAt, b.Conclusion, b.Detail,
			b.Splits, b.Queued, b.RecheckOf, b.RecheckDetail, recheckAt)
		return err
	})
}

// DeleteMergeBatch forgets one branch's batch. A batch is transient by
// nature -- it ends the moment its members merge, fall out, or the whole
// thing is abandoned -- and leaving the last one's row behind would hold
// the next set of changes out of the queue for a tree nobody is testing.
func (s *Store) DeleteMergeBatch(ctx context.Context, repo RepoRef, branch string) error {
	return s.write(ctx, "clear merge batch for "+repo.String()+" "+branch, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"DELETE FROM `merge_batch` WHERE `owner` = ? AND `name` = ? AND `branch` = ?",
			repo.Owner, repo.Name, branch)
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
