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
	"sort"
	"strconv"
	"time"
)

// originForNewTask is the WorkOrigin putTask writes for a task whose
// caller left it empty. A task already stored keeps the one it has (an
// update built from scratch must not re-derive it); a new one filed by
// another task's run inherits that task's (treeOrigin); a new one
// filed against a GitHub issue (Task.Issue) has that issue; anything else
// has none, and is the root of whatever it goes on to file.
func originForNewTask(ctx context.Context, tx *sql.Tx, t Task) (WorkOrigin, error) {
	var stored string
	err := tx.QueryRowContext(ctx, "SELECT `origin_ref` FROM `task` WHERE `id` = ?", t.ID).Scan(&stored)
	switch {
	case err == nil:
		return WorkOrigin(stored), nil
	case !errors.Is(err, sql.ErrNoRows):
		return "", fmt.Errorf("reading origin of task %s: %w", t.ID, err)
	}
	if parent := t.Parent(); parent != "" {
		origin, err := treeOrigin(ctx, tx, parent)
		if err != nil {
			return "", fmt.Errorf("reading origin of task %s's parent %s: %w", t.ID, parent, err)
		}
		return origin, nil
	}
	if t.Issue > 0 && t.Target != nil {
		return GitHubOrigin(*t.Target, t.Issue), nil
	}
	return "", nil
}

// rootOfNamedOrigin is the origin putTask writes for a new task whose
// caller named one: a task origin is the tree that task belongs to
// (treeOrigin), not the task itself, so naming a task half way down a
// tree -- a run naming itself in propose_task's origin, say -- files the
// new task in that tree rather than starting a new one at the task named.
// A GitHub origin, and any origin on a task already stored, is kept as
// given.
func rootOfNamedOrigin(ctx context.Context, tx *sql.Tx, t Task) (WorkOrigin, error) {
	id := t.WorkOrigin.Task()
	if id == "" {
		return t.WorkOrigin, nil
	}
	var stored string
	err := tx.QueryRowContext(ctx, "SELECT `origin_ref` FROM `task` WHERE `id` = ?", t.ID).Scan(&stored)
	switch {
	case err == nil:
		return t.WorkOrigin, nil
	case !errors.Is(err, sql.ErrNoRows):
		return "", fmt.Errorf("reading origin of task %s: %w", t.ID, err)
	}
	if id == t.ID {
		return t.WorkOrigin, nil
	}
	origin, err := treeOrigin(ctx, tx, id)
	if err != nil {
		return "", fmt.Errorf("reading origin of task %s: %w", id, err)
	}
	return origin, nil
}

// treeOrigin is the origin of the tree task id belongs to -- the one a
// task id files inherits. That is id's own origin when it has one, and
// otherwise the origin of the task that filed id, and so on up: a task
// with no origin of its own is the root of its tree only when nobody
// filed it. Without the walk, a task filed by one stored without an
// origin (every tree that existed before origins did, whose root had
// no issue to backfill) would take its parent as its root, and each such
// parent would start a tree of its own. A task origin met on the way is
// resolved the same way, so one that names a task part way down a tree
// comes out at that tree's root. A task that is not stored is taken as a
// root.
func treeOrigin(ctx context.Context, tx *sql.Tx, id string) (WorkOrigin, error) {
	seen := map[string]bool{}
	for {
		if seen[id] {
			// A cycle in proposed-by links (never written, but a walk
			// that trusts stored rows must not spin on one).
			return TaskOrigin(id), nil
		}
		seen[id] = true
		var origin, parent string
		err := tx.QueryRowContext(ctx,
			"SELECT `t`.`origin_ref`, "+
				"COALESCE((SELECT MIN(`target`) FROM `task_link` WHERE `task_id` = `t`.`id` AND `kind` = ?), '') "+
				"FROM `task` `t` WHERE `t`.`id` = ?", string(LinkProposedBy), id).Scan(&origin, &parent)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return TaskOrigin(id), nil
		case err != nil:
			return "", err
		}
		o := WorkOrigin(origin)
		switch {
		case o.Task() != "" && o.Task() != id:
			id = o.Task()
		case o != "":
			return o, nil
		case parent != "":
			id = parent
		default:
			return TaskOrigin(id), nil
		}
	}
}

// TreeOrigin is the origin of the tree task id belongs to (treeOrigin):
// the tree a task stored with no origin of its own is drawn in. A task
// from before origins existed, filed by another, has none, and is still
// part way down its filer's tree rather than the root of one.
func (s *Store) TreeOrigin(ctx context.Context, id string) (WorkOrigin, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	return treeOrigin(ctx, tx, id)
}

// TasksWithOrigin is every task in origin's tree, in id order -- the rows
// the progress comment on a GitHub origin, and a task origin's tree in
// the task view, are drawn from.
func (s *Store) TasksWithOrigin(ctx context.Context, origin WorkOrigin) ([]Task, error) {
	if origin == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, "SELECT `id` FROM `task` WHERE `origin_ref` = ?", string(origin))
	if err != nil {
		return nil, fmt.Errorf("listing tasks with origin %s: %w", origin, err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := make([]Task, 0, len(ids))
	for _, id := range ids {
		t, err := s.GetTask(ctx, id)
		if err != nil {
			return nil, err
		}
		if t != nil {
			out = append(out, *t)
		}
	}
	sortTasksByID(out)
	return out, nil
}

// ChildTasks is every task t's runs filed (LinkProposedBy pointing at t),
// in id order.
func (s *Store) ChildTasks(ctx context.Context, id string) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT `task_id` FROM `task_link` WHERE `kind` = ? AND `target` = ?", string(LinkProposedBy), id)
	if err != nil {
		return nil, fmt.Errorf("listing tasks filed by %s: %w", id, err)
	}
	var ids []string
	for rows.Next() {
		var child string
		if err := rows.Scan(&child); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, child)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := make([]Task, 0, len(ids))
	for _, child := range ids {
		t, err := s.GetTask(ctx, child)
		if err != nil {
			return nil, err
		}
		if t != nil {
			out = append(out, *t)
		}
	}
	sortTasksByID(out)
	return out, nil
}

// sortTasksByID orders tasks by numeric id, the order they were filed in.
func sortTasksByID(tasks []Task) {
	sort.SliceStable(tasks, func(i, j int) bool {
		a, aerr := strconv.Atoi(tasks[i].ID)
		b, berr := strconv.Atoi(tasks[j].ID)
		if aerr != nil || berr != nil {
			return tasks[i].ID < tasks[j].ID
		}
		return a < b
	})
}

// WorkOriginComment is the progress comment grain keeps on one GitHub
// origin: which comment it is, what it last said, and when grain last
// looked at whether it needed saying again.
type WorkOriginComment struct {
	Origin    WorkOrigin
	CommentID int64
	Body      string
	CheckedAt *time.Time
}

// GetWorkOriginComment is origin's progress comment, nil when grain has
// not posted one.
func (s *Store) GetWorkOriginComment(ctx context.Context, origin WorkOrigin) (*WorkOriginComment, error) {
	c := WorkOriginComment{Origin: origin}
	var checked sql.NullTime
	err := s.db.QueryRowContext(ctx,
		"SELECT `comment_id`, `body`, `checked_at` FROM `work_origin_comment` WHERE `origin` = ?",
		string(origin)).Scan(&c.CommentID, &c.Body, &checked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the progress comment on %s: %w", origin, err)
	}
	c.CheckedAt = timePtr(checked)
	return &c, nil
}

// PutWorkOriginComment records origin's progress comment.
func (s *Store) PutWorkOriginComment(ctx context.Context, c WorkOriginComment) error {
	_, err := s.db.ExecContext(ctx,
		"REPLACE INTO `work_origin_comment` (`origin`, `comment_id`, `body`, `checked_at`) VALUES (?,?,?,?)",
		string(c.Origin), c.CommentID, c.Body, timeOf(c.CheckedAt))
	if err != nil {
		return fmt.Errorf("recording the progress comment on %s: %w", c.Origin, err)
	}
	return nil
}

// GitHubWorkOrigins is every pull request or issue some task names as its
// origin -- the ones a progress comment may be owed on.
func (s *Store) GitHubWorkOrigins(ctx context.Context) ([]WorkOrigin, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT DISTINCT `origin_ref` FROM `task` WHERE `origin_ref` <> '' AND `origin_ref` NOT LIKE 'task:%' ORDER BY `origin_ref`")
	if err != nil {
		return nil, fmt.Errorf("listing work origins: %w", err)
	}
	defer rows.Close()
	var out []WorkOrigin
	for rows.Next() {
		var o string
		if err := rows.Scan(&o); err != nil {
			return nil, err
		}
		out = append(out, WorkOrigin(o))
	}
	return out, rows.Err()
}

// TaskForIssue is the id of a task that already stands for issue number
// of source in repo target, in any state -- open, held, running, done or
// closed -- and "" when there is none. It is what keeps the GitHub issue
// importer (orchestrator.reconcileIssueImports) from filing one issue
// twice.
//
// A task stands for the issue when its WorkOrigin names it, which every
// importer task does and every task a tree under one inherits, or -- when
// source is target itself -- when it was filed against the issue by
// number (Task.Issue), the way a GitHub mention files one. Task.Issue is
// a number in the task's own target, so it says nothing about an issue on
// another repository and is not consulted for one.
func (s *Store) TaskForIssue(ctx context.Context, source RepoRef, number int, target RepoRef) (string, error) {
	origin := GitHubOrigin(source, number)
	query := "SELECT `id` FROM `task` WHERE LOWER(`origin_ref`) = LOWER(?)"
	args := []any{string(origin)}
	if source.Same(target) {
		query += " OR (LOWER(`target_owner`) = LOWER(?) AND LOWER(`target_name`) = LOWER(?) AND `issue` = ?)"
		args = append(args, target.StoredOwner(), target.Name, number)
	}
	var id string
	err := s.db.QueryRowContext(ctx, query+" LIMIT 1", args...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("looking for a task for %s: %w", origin, err)
	}
	return id, nil
}

// OriginOnlyUnapprovedImports reports whether every task in origin's tree
// is a GitHub issue import nobody has approved yet (ReasonImported, no
// approval) -- and there is at least one. Such an origin is owed no
// progress comment: nothing has been agreed to work on it, and the
// issue's own thread, often on a repository this deployment does not own,
// is no place to announce a task that may never be approved.
func (s *Store) OriginOnlyUnapprovedImports(ctx context.Context, origin WorkOrigin) (bool, error) {
	var imports, others int
	err := s.db.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(CASE WHEN `origin_reason` = ? AND `approved_at` IS NULL THEN 1 ELSE 0 END), 0), "+
			"COALESCE(SUM(CASE WHEN `origin_reason` = ? AND `approved_at` IS NULL THEN 0 ELSE 1 END), 0) "+
			"FROM `task` WHERE `origin_ref` = ?",
		string(ReasonImported), string(ReasonImported), string(origin)).Scan(&imports, &others)
	if err != nil {
		return false, fmt.Errorf("reading the tasks under %s: %w", origin, err)
	}
	return imports > 0 && others == 0, nil
}
