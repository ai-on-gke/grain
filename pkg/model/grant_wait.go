package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// GrantWait is why an otherwise-ready task has not been dispatched. It is an
// admission fact, not a run activity: no attempt or sandbox exists yet.
//
// Being derived rather than declared, it has three owners and no others:
// the admitter (orchestrator.grantAdmitter) derives it, StartRun clears
// it with the attempt it belongs to, and putTask invalidates it when the
// grants or target it was derived from change. Nothing else may leave
// one standing: see docs/implementation-notes.md, "Who invalidates a
// derived admission fact".
type GrantWait struct {
	TaskID     string
	Capability string
	Resource   string
	WaitingAt  time.Time
}

// SetGrantWait records the finite resource currently holding taskID at
// admission. Repeated probes preserve the first waiting time while refreshing
// the reason, so the UI reports how long the continuous wait has lasted.
func (s *Store) SetGrantWait(ctx context.Context, wait GrantWait) error {
	if wait.TaskID == "" || wait.Capability == "" || wait.Resource == "" {
		return errors.New("model: a grant wait needs a task, capability and resource")
	}
	if existing, err := s.GrantWaitOf(ctx, wait.TaskID); err == nil && existing != nil &&
		existing.Capability == wait.Capability && existing.Resource == wait.Resource {
		return nil
	}
	return s.write(ctx, "set task "+wait.TaskID+" grant wait", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"INSERT INTO `task_grant_wait` (`task_id`,`capability`,`resource`,`waiting_at`) VALUES (?,?,?,?) "+
				"ON CONFLICT (`task_id`) DO UPDATE SET `capability` = excluded.`capability`, `resource` = excluded.`resource`",
			wait.TaskID, wait.Capability, wait.Resource, wait.WaitingAt.UTC())
		return err
	})
}

// ClearGrantWait removes taskID's queued admission marker. StartRun also does
// this transactionally; this method covers a resource becoming available
// before another pre-run setup error is handled.
func (s *Store) ClearGrantWait(ctx context.Context, taskID string) error {
	return s.write(ctx, "clear task "+taskID+" grant wait", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM `task_grant_wait` WHERE `task_id` = ?", taskID)
		return err
	})
}

// ClearGrantWaitUnless is ClearGrantWait for a caller that owns only some
// of the waits a task can show: it leaves a wait recorded under any of
// keep in place, so one gate cannot erase the reason another gave.
func (s *Store) ClearGrantWaitUnless(ctx context.Context, taskID string, keep ...string) error {
	if len(keep) == 0 {
		return s.ClearGrantWait(ctx, taskID)
	}
	query := "DELETE FROM `task_grant_wait` WHERE `task_id` = ? AND `capability` NOT IN (?" + strings.Repeat(",?", len(keep)-1) + ")"
	args := make([]any, 0, len(keep)+1)
	args = append(args, taskID)
	for _, capability := range keep {
		args = append(args, capability)
	}
	return s.write(ctx, "clear task "+taskID+" grant wait", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, query, args...)
		return err
	})
}

// GrantWaits returns every current queued admission marker keyed by task ID.
func (s *Store) GrantWaits(ctx context.Context) (map[string]GrantWait, error) {
	out := map[string]GrantWait{}
	err := each(ctx, s.db,
		"SELECT `task_id`,`capability`,`resource`,`waiting_at` FROM `task_grant_wait`", nil,
		func(rows *sql.Rows) error {
			var wait GrantWait
			if err := rows.Scan(&wait.TaskID, &wait.Capability, &wait.Resource, &wait.WaitingAt); err != nil {
				return err
			}
			out[wait.TaskID] = wait
			return nil
		})
	return out, err
}

// GrantWaitOf is GrantWaits for one task.
func (s *Store) GrantWaitOf(ctx context.Context, taskID string) (*GrantWait, error) {
	var wait GrantWait
	err := s.db.QueryRowContext(ctx,
		"SELECT `task_id`,`capability`,`resource`,`waiting_at` FROM `task_grant_wait` WHERE `task_id` = ?", taskID).
		Scan(&wait.TaskID, &wait.Capability, &wait.Resource, &wait.WaitingAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading task %s's grant wait: %w", taskID, err)
	}
	return &wait, nil
}
