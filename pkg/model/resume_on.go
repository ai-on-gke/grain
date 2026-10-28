package model

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// The two wakes resume_later adds to the ones parks already had
// (docs/design/durable-tasks.md#DT-9). A resume_later that names ci or
// message defers its own task with no wake time, or with its at
// (ui.Client.ResumeLater); what releases that deferral is the condition
// arriving -- a CI verdict (Store.DueResumeOnCI, which
// orchestrator.reconcileWake reads), or a post to the task
// (Store.WakeOnMessage, which ui's comment path calls) -- or what
// releases any deferral: its wake time, or a person pressing Undefer.

// heldResume is one open requested suspension holding its task deferred:
// the deferral was made by the suspended run itself, between its start
// and its finish, so a deferral a person made since is not mistaken for
// one.
type heldResume struct {
	taskID   string
	resumeOn ResumeOn
	since    time.Time
}

func (s *Store) heldResumes(ctx context.Context, taskID string) ([]heldResume, error) {
	query := "SELECT `su`.`task_id`, `su`.`resume_on`, `t`.`deferred_at` FROM `task_suspension` AS `su` " +
		"JOIN `task` AS `t` ON `t`.`id` = `su`.`task_id` " +
		"JOIN `task_run` AS `r` ON `r`.`id` = `su`.`run_id` " +
		"WHERE `su`.`resumed_at` IS NULL AND `su`.`kind` = ? AND `su`.`resume_on` IS NOT NULL " +
		"AND `t`.`deferred_at` IS NOT NULL AND `t`.`deferred_at` >= `r`.`started_at` " +
		"AND `t`.`deferred_at` <= `su`.`created_at`"
	args := []any{string(SuspendRequested)}
	if taskID != "" {
		query += " AND `su`.`task_id` = ?"
		args = append(args, taskID)
	}
	var out []heldResume
	err := each(ctx, s.db, query, args, func(rows *sql.Rows) error {
		var h heldResume
		var raw string
		if err := rows.Scan(&h.taskID, &raw, &h.since); err != nil {
			return err
		}
		r, err := ParseResumeOn(raw)
		if err != nil {
			return fmt.Errorf("reading task %s's resume_on: %w", h.taskID, err)
		}
		h.resumeOn = r
		out = append(out, h)
		return nil
	})
	return out, err
}

// DueResumeOnCI is every task held by a resume_later on grain CI whose
// verdict has come: the run it named has concluded, or -- for
// ResumeOnMyBranch -- a run on the task's own branch has concluded since
// the task was suspended.
func (s *Store) DueResumeOnCI(ctx context.Context) ([]string, error) {
	held, err := s.heldResumes(ctx, "")
	if err != nil {
		return nil, err
	}
	var due []string
	for _, h := range held {
		if h.resumeOn.CI == "" {
			continue
		}
		var n int
		if h.resumeOn.CI == ResumeOnMyBranch {
			task, err := s.GetTask(ctx, h.taskID)
			if err != nil {
				return nil, err
			}
			if task == nil {
				continue
			}
			err = s.db.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM `ci_run` WHERE `ref` = ? AND `conclusion` <> '' AND `finished_at` > ?",
				task.BranchName(), h.since.UTC()).Scan(&n)
			if err != nil {
				return nil, err
			}
		} else if err := s.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM `ci_run` WHERE `id` = ? AND `conclusion` <> ''", h.resumeOn.CI).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			due = append(due, h.taskID)
		}
	}
	return due, nil
}

// WakeOnMessage releases taskID if a resume_later with message: true is
// holding it, because somebody has just posted to it, and reports
// whether it did.
func (s *Store) WakeOnMessage(ctx context.Context, taskID string, now time.Time) (bool, error) {
	held, err := s.heldResumes(ctx, taskID)
	if err != nil {
		return false, err
	}
	for _, h := range held {
		if h.resumeOn.Message {
			return true, s.SetDeferred(ctx, taskID, nil, nil, now)
		}
	}
	return false, nil
}
