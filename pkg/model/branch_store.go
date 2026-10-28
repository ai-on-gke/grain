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

const branchColumns = "`id`,`owner`,`repo`,`name`,`from_branch`,`status`,`created_at`,`last_error`"

func scanBranch(scan func(...any) error) (Branch, error) {
	var b Branch
	var status string
	var lastError sql.NullString
	if err := scan(&b.ID, &b.Repo.Owner, &b.Repo.Name, &b.Name, &b.From, &status, &b.CreatedAt, &lastError); err != nil {
		return Branch{}, err
	}
	b.Status = BranchStatus(status)
	b.LastError = lastError.String
	return b, nil
}

// CreateBranch records a fresh request for name on repo -- the issue's
// own "create a new branch on a repo... This should just create the given
// branch in github," and grain/task-176's "if a branch already exists on
// a repo, we should be able to add it to grain." It only ever writes a
// row, declaring the intent; the branches reconciler
// (pkg/orchestrator.SyncBranches) is what talks to GitHub, and it either
// creates name at the repo's current default branch tip (Status
// BranchCreated) or, if the ref is already there, adopts it exactly as it
// stands (BranchAdopted).
//
// One call covers both because a caller cannot know which it is asking
// for: whether a name is already on GitHub is a fact only GitHub holds,
// and the answer can change between the form being drawn and the row
// being reconciled. So the request says which branch, not who is
// expected to have made it, and the status it lands on says which
// happened.
//
// name must satisfy ValidBranchName (ErrInvalidBranchName). Asking for
// a name repo already has a row for adds no second row -- see
// RequestBranch, which this is with the "was it new" answer dropped.
func (s *Store) CreateBranch(ctx context.Context, repo RepoRef, name string, now time.Time) (Branch, error) {
	b, _, err := s.RequestBranch(ctx, repo, name, now)
	return b, err
}

// RequestBranch is CreateBranch reporting whether it recorded a fresh
// row (true) or answered with the one repo already has for name (false).
//
// Unlike CreateRelease, a repeat is not an error, but it is not a second
// row either. Whatever the latest row for (repo, name) says, it is
// already the whole of the request: a pending one is still owed a
// reconcile, a created or adopted one is done, and a pending one whose
// reconcile failed is retried by the reconciler every cycle on its own.
// Asking again for that last one is still how a human retries it -- its
// LastError is cleared, so the repo page reads plain "pending" until the
// next attempt either lands or records why not -- but it stays the one
// row, rather than a second pending request for the same ref that the
// reconciler would then also have to settle.
func (s *Store) RequestBranch(ctx context.Context, repo RepoRef, name string, now time.Time) (Branch, bool, error) {
	return s.RequestBranchFrom(ctx, repo, name, "", now)
}

// RequestBranchFrom is RequestBranch for a branch to be cut from from
// rather than from the repo's default branch ("" still means the
// default) -- grain/task-2317's new-task form, which files a task onto a
// branch it asks grain to create from whichever one was picked.
//
// from must satisfy ValidBranchName when it is not empty
// (ErrInvalidBranchName). A repeat request for a name repo already has a
// row for answers with that row exactly as RequestBranch does: its From
// is left alone, since a pending row may be mid-reconcile and a created
// or adopted one already sits wherever it was cut.
func (s *Store) RequestBranchFrom(ctx context.Context, repo RepoRef, name, from string, now time.Time) (Branch, bool, error) {
	if !ValidBranchName(name) || (from != "" && !ValidBranchName(from)) {
		return Branch{}, false, ErrInvalidBranchName
	}
	var out Branch
	var fresh bool
	err := s.write(ctx, "create branch "+name+" for "+repo.String(), func(tx *sql.Tx) error {
		existing, err := scanBranch(tx.QueryRowContext(ctx,
			"SELECT "+branchColumns+" FROM `branch` WHERE `owner` = ? AND `repo` = ? AND `name` = ? ORDER BY `id` DESC LIMIT 1",
			repo.Owner, repo.Name, name).Scan)
		switch {
		case err == nil:
			if existing.Status == BranchPending && existing.LastError != "" {
				if _, err := tx.ExecContext(ctx,
					"UPDATE `branch` SET `last_error` = NULL WHERE `id` = ?", existing.ID); err != nil {
					return fmt.Errorf("retrying branch %q for %s: %w", name, repo, err)
				}
				existing.LastError = ""
			}
			out = existing
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("checking for branch %q on %s: %w", name, repo, err)
		}
		b := Branch{Repo: repo, Name: name, From: from, Status: BranchPending, CreatedAt: now}
		id, err := insertBranch(ctx, tx, b)
		if err != nil {
			return err
		}
		b.ID = id
		out = b
		fresh = true
		return nil
	})
	return out, fresh, err
}

func insertBranch(ctx context.Context, tx *sql.Tx, b Branch) (int64, error) {
	res, err := tx.ExecContext(ctx,
		"INSERT INTO `branch` (`owner`,`repo`,`name`,`from_branch`,`status`,`created_at`,`last_error`) VALUES (?,?,?,?,?,?,?)",
		b.Repo.Owner, b.Repo.Name, b.Name, b.From, string(b.Status), b.CreatedAt.UTC(), nullable(b.LastError))
	if err != nil {
		return 0, fmt.Errorf("creating branch %q for %s: %w", b.Name, b.Repo, err)
	}
	return res.LastInsertId()
}

// ListBranches returns every branch ever requested for repo, newest
// first -- what the repo page reads to show a requested branch's own
// status and, once the reconciler has tried, any error it hit.
func (s *Store) ListBranches(ctx context.Context, repo RepoRef) ([]Branch, error) {
	var out []Branch
	err := each(ctx, s.db,
		"SELECT "+branchColumns+" FROM `branch` WHERE `owner` = ? AND `repo` = ? ORDER BY `id` DESC",
		[]any{repo.Owner, repo.Name},
		func(rows *sql.Rows) error {
			b, err := scanBranch(rows.Scan)
			if err != nil {
				return err
			}
			out = append(out, b)
			return nil
		})
	return out, err
}

// PendingBranches returns every requested branch, across every repo,
// still BranchPending -- oldest first -- what the branches reconciler
// still owes a GitHub-side create.
func (s *Store) PendingBranches(ctx context.Context) ([]Branch, error) {
	var out []Branch
	err := each(ctx, s.db,
		"SELECT "+branchColumns+" FROM `branch` WHERE `status` = ? ORDER BY `id`",
		[]any{string(BranchPending)},
		func(rows *sql.Rows) error {
			b, err := scanBranch(rows.Scan)
			if err != nil {
				return err
			}
			out = append(out, b)
			return nil
		})
	return out, err
}

// MarkBranchCreated advances a BranchPending row to BranchCreated,
// clearing LastError -- the branches reconciler's report that it cut Name
// on GitHub itself.
func (s *Store) MarkBranchCreated(ctx context.Context, id int64) error {
	return s.markBranch(ctx, id, BranchCreated, "mark branch created")
}

// MarkBranchAdopted advances a BranchPending row to BranchAdopted,
// clearing LastError -- the branches reconciler's report that Name was
// already live on GitHub and grain has taken the existing ref as it
// stands rather than creating (or moving) anything.
func (s *Store) MarkBranchAdopted(ctx context.Context, id int64) error {
	return s.markBranch(ctx, id, BranchAdopted, "mark branch adopted")
}

func (s *Store) markBranch(ctx context.Context, id int64, status BranchStatus, what string) error {
	return s.write(ctx, what, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE `branch` SET `status` = ?, `last_error` = NULL WHERE `id` = ?",
			string(status), id)
		return err
	})
}

// MarkBranchError records why the branches reconciler's create has not
// landed yet, leaving Status unchanged (BranchPending) -- the next cycle
// retries the same create.
func (s *Store) MarkBranchError(ctx context.Context, id int64, message string) error {
	return s.write(ctx, "record branch error", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE `branch` SET `last_error` = ? WHERE `id` = ?", message, id)
		return err
	})
}
