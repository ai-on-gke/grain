package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// TaskPause is an operator's pause on one task: "stop working on this,
// let go of everything it holds, and keep its place". It is a flag beside
// a task's state rather than a state of its own, which is what lets a
// resume put the task back exactly where it was -- pausing writes none of
// the things a task's dispatch standing is made of (its approval,
// order_key, deferral and wake time, depends_on links, fixes-base-branch
// tags, a pending question, its merge-queue membership), so resuming has
// nothing to restore but this row's own absence. task_ready, RunNow,
// DueDeferrals and the merge queue all read its presence instead.
//
// What a pause does take away is a live run: a run is cancelled when its
// task is paused (orchestrator.reconcilePauses) and recorded as
// InterruptedOutcome, so task 837's rule gives back the run-now request
// it spent when it started (RunNowSurvives, settleRunNow) -- the pause
// keeps no copy of its own. A request still standing when the task is
// paused stays where it is: RunNow and StartRun ignore a paused task's,
// so it takes effect again on resume.
//
// Deliberately separate from deferral (Task.DeferredAt). Deferring means
// "put away": it hides a task from the default lists and is only offered
// before a task has ever run. A pause keeps the task in view, says who
// paused it, and is offered in every open state. Defer is not
// reimplemented as a special case of pause: the two states read
// differently to every list and filter that already knows 'deferred', a
// deferral's wake time is its own mechanism, and nothing would be simpler
// for folding them together -- so Defer keeps its callers and behaviour,
// and a deferred task can additionally be paused like any other.
type TaskPause struct {
	TaskID   string
	PausedAt time.Time
	PausedBy Principal
	// FromState is the state the task was in when it was paused. Shown to
	// people ("paused while running"); resuming does not need it, because
	// the state is still derived from everything that was left alone.
	FromState State
	// Immediate is a pause that skips the grace period: a live run is
	// cancelled on the next cycle rather than told to wrap up first.
	Immediate bool
	// GraceUntil is when a graceful pause of a running task stops waiting
	// for the run to wrap up and cancels it. Nil when nothing was running.
	GraceUntil *time.Time
	// RunID is the run that was live when the task was paused, if any.
	RunID string
}

// ErrTaskPaused is StartRun's refusal to record a run of a paused task.
var ErrTaskPaused = errors.New("model: the task is paused")

// PauseGracePeriod is how long a graceful pause waits for a running task's
// agent to commit, push and write its handover before the run is cancelled
// -- the default of Config.PauseGrace (grain_config is not consulted; a
// deployment that wants another sets the daemon's own flag).
const PauseGracePeriod = 2 * time.Minute

// OperatorPausedDetail is the task_run.detail of a run ended by an
// operator's pause. The outcome beside it is InterruptedOutcome: a pause
// is a person's decision about the deployment's work, not evidence about
// the task, so it must not count toward MaxConsecutiveFailures.
const OperatorPausedDetail = "paused by the operator; the task resumes its conversation when it is resumed"

// PauseMessage is what a graceful pause says into a running agent's
// session (delivered as the operator's own comment, which the run reads on
// its next tool result -- pkg/mcp's addenda).
func PauseMessage(grace time.Duration) string {
	return fmt.Sprintf("The operator has paused this task. Within the next %s, commit and push any work "+
		"you want kept, write your handover with write_handover, then stop. Do not start new work.",
		humanDuration(grace))
}

func humanDuration(d time.Duration) string {
	switch {
	case d >= time.Minute && d%time.Minute == 0:
		if d == time.Minute {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", int(d/time.Minute))
	default:
		return d.Round(time.Second).String()
	}
}

// ResumedNote is the paragraph a resumed task's next prompt carries.
func ResumedNote(p TaskPause, resumedAt time.Time) string {
	s := fmt.Sprintf("This task was paused by the operator at %s and has now been resumed (%s).",
		p.PausedAt.UTC().Format(time.RFC3339), resumedAt.UTC().Format(time.RFC3339))
	if p.RunID != "" {
		s += " The run that was in progress when it was paused was stopped and its sandbox released: " +
			"whatever was in that sandbox but not pushed is gone. Pick up from what was pushed and from " +
			"your handover."
	}
	return s
}

// Pause records p, or does nothing when the task is already paused
// (reporting false).
func (s *Store) Pause(ctx context.Context, p TaskPause) (bool, error) {
	var paused bool
	err := s.write(ctx, "pause task "+p.TaskID, func(tx *sql.Tx) error {
		paused = false
		var one int
		err := tx.QueryRowContext(ctx, "SELECT 1 FROM `task_pause` WHERE `task_id` = ?", p.TaskID).Scan(&one)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `task_pause` (`task_id`,`paused_at`,`paused_by_kind`,`paused_by_id`,`from_state`,"+
				"`immediate`,`grace_until`,`run_id`) VALUES (?,?,?,?,?,?,?,?)",
			p.TaskID, p.PausedAt.UTC(), string(p.PausedBy.Kind), p.PausedBy.ID, string(p.FromState),
			p.Immediate, timeOf(p.GraceUntil), nullable(p.RunID)); err != nil {
			return err
		}
		paused = true
		return nil
	})
	return paused, err
}

// SetPauseImmediate turns a graceful pause into an immediate one, for a
// second pause that says --now while the first is still waiting out its
// grace period.
func (s *Store) SetPauseImmediate(ctx context.Context, taskID string) error {
	return s.write(ctx, "pause task "+taskID+" now", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE `task_pause` SET `immediate` = 1 WHERE `task_id` = ?", taskID)
		return err
	})
}

// Resume deletes taskID's pause, returning the pause that was lifted (nil
// when the task was not paused). Nothing else needs restoring: see
// TaskPause.
func (s *Store) Resume(ctx context.Context, taskID string, now time.Time) (*TaskPause, error) {
	var lifted *TaskPause
	err := s.write(ctx, "resume task "+taskID, func(tx *sql.Tx) error {
		p, err := getPause(ctx, tx, taskID)
		if err != nil || p == nil {
			lifted = nil
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM `task_pause` WHERE `task_id` = ?", taskID); err != nil {
			return err
		}
		lifted = p
		return nil
	})
	return lifted, err
}

// GetPause returns taskID's pause, or nil when it is not paused.
func (s *Store) GetPause(ctx context.Context, taskID string) (*TaskPause, error) {
	return getPause(ctx, s.db, taskID)
}

// Pauses is every paused task's pause, in task ID order.
func (s *Store) Pauses(ctx context.Context) ([]TaskPause, error) {
	var out []TaskPause
	err := each(ctx, s.db, "SELECT "+pauseColumns+" FROM `task_pause` ORDER BY `task_id`", nil,
		func(rows *sql.Rows) error {
			p, err := scanPause(rows)
			if err != nil {
				return err
			}
			out = append(out, p)
			return nil
		})
	return out, err
}

// PausedSet is Pauses keyed by task, for callers that ask "is this one
// paused" about many tasks in a row.
func (s *Store) PausedSet(ctx context.Context) (map[string]TaskPause, error) {
	all, err := s.Pauses(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]TaskPause, len(all))
	for _, p := range all {
		out[p.TaskID] = p
	}
	return out, nil
}

const pauseColumns = "`task_id`,`paused_at`,`paused_by_kind`,`paused_by_id`,`from_state`,`immediate`," +
	"`grace_until`,`run_id`"

type rowScanner interface{ Scan(dest ...any) error }

func scanPause(row rowScanner) (TaskPause, error) {
	var p TaskPause
	var kind, from string
	var grace sql.NullTime
	var runID sql.NullString
	if err := row.Scan(&p.TaskID, &p.PausedAt, &kind, &p.PausedBy.ID, &from, &p.Immediate,
		&grace, &runID); err != nil {
		return TaskPause{}, err
	}
	p.PausedBy.Kind = PrincipalKind(kind)
	p.FromState = State(from)
	p.GraceUntil = timePtr(grace)
	p.RunID = runID.String
	return p, nil
}

func getPause(ctx context.Context, q querier, taskID string) (*TaskPause, error) {
	p, err := scanPause(q.QueryRowContext(ctx, "SELECT "+pauseColumns+" FROM `task_pause` WHERE `task_id` = ?", taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}
