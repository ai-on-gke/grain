package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"
)

// dispatchStandingOf reads taskID's DispatchStanding from the rows that
// decide it, with runNow as its Run now request -- passed in rather than
// read, because the one caller that snapshots it (startRun) has just
// spent the request off task_observation and the snapshot is of what the
// run spent. A task with no row has the zero standing.
func dispatchStandingOf(ctx context.Context, q querier, taskID string, runNow *time.Time) (DispatchStanding, error) {
	st := DispatchStanding{Grants: []string{}}
	if runNow != nil {
		t := runNow.UTC()
		st.RunNowAt = &t
	}
	err := q.QueryRowContext(ctx,
		"SELECT `t`.`order_key`, "+priorityDispatchTaskSQL+", "+directorTaskSQL+", "+
			"EXISTS (SELECT 1 FROM `task_tag` AS `rt` WHERE `rt`.`task_id` = `t`.`id` AND `rt`.`tag` LIKE ?) "+
			"FROM `task` AS `t` WHERE `t`.`id` = ?",
		append(append(priorityDispatchArgs(), string(ReasonConsoleSession), ConsoleSessionTagPrefix+"%"),
			baseRepairBranchTagPrefix+"%", taskID)...).
		Scan(&st.OrderKey, &st.PriorityDispatch, &st.Session, &st.FixesBaseBranch)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	if err != nil {
		return st, fmt.Errorf("reading task %s's dispatch standing: %w", taskID, err)
	}
	err = each(ctx, q, "SELECT `capability` FROM `task_grant` WHERE `task_id` = ?", []any{taskID},
		func(rows *sql.Rows) error {
			var c string
			if err := rows.Scan(&c); err != nil {
				return err
			}
			st.Grants = append(st.Grants, c)
			return nil
		})
	sort.Strings(st.Grants)
	return st, err
}

// DispatchStanding is taskID's dispatch standing as it is now, with the
// Run now request standing on it (Observation.RunNowAt).
func (s *Store) DispatchStanding(ctx context.Context, taskID string) (DispatchStanding, error) {
	obs, err := getObservation(ctx, s.db, taskID)
	if err != nil {
		return DispatchStanding{}, err
	}
	var runNow *time.Time
	if obs != nil {
		runNow = obs.RunNowAt
	}
	return dispatchStandingOf(ctx, s.db, taskID, runNow)
}

// standingJSON encodes a standing the one way it is stored.
func standingJSON(st DispatchStanding) (string, error) {
	b, err := json.Marshal(st)
	return string(b), err
}

// Suspend finishes s.RunID as a suspension of s.Kind at s.CreatedAt: the
// run's outcome (SuspensionKind.Outcome), its suspend_kind, the
// task_suspension row, the failure streak (which a suspension does not
// move) and the Run now request the run spent (settleRunNow, which puts
// it back for every kind) are all one transaction, so no crash can leave
// a suspended run whose task has lost its standing.
//
// It writes nothing a run never writes: not task.order_key, not the
// base-branch repair mark, not the task's grants (DT-3). s.Standing is
// ignored; the standing recorded is the one the run started with.
//
// An unknown run is not an error, as it is not for FinishRun.
func (s *Store) Suspend(ctx context.Context, susp Suspension) error {
	if !susp.Kind.Valid() {
		return fmt.Errorf("suspending run %s: %q is not a suspension kind", susp.RunID, susp.Kind)
	}
	return s.write(ctx, "suspend run "+susp.RunID, func(tx *sql.Tx) error {
		// A run already finished -- the agent's own exit is read off its
		// tool calls after the run is recorded (orchestrator.processResult)
		// -- keeps the time it finished at.
		at := susp.CreatedAt
		var finished sql.NullTime
		err := tx.QueryRowContext(ctx, "SELECT `finished_at` FROM `task_run` WHERE `id` = ?", susp.RunID).
			Scan(&finished)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if finished.Valid {
			at = finished.Time
		}
		if at.IsZero() {
			at = time.Now()
		}
		return finishRunTx(ctx, tx, susp.RunID, at, susp.Kind.Outcome(), susp.Detail, &susp)
	})
}

// recordSuspensionTx makes runID's task_run.suspend_kind and its
// task_suspension row agree with how the run has just been recorded as
// ending: susp when the finish named one (Store.Suspend), else the kind
// its outcome implies (SuspensionKindOfOutcome), else none -- in which
// case a suspension an earlier record of the same run wrote is withdrawn,
// since a run's outcome can be re-recorded (Store.SetRunOutcome).
func recordSuspensionTx(ctx context.Context, tx *sql.Tx, runID, taskID, outcome, detail string, at time.Time, susp *Suspension) error {
	if susp == nil {
		if kind, ok := SuspensionKindOfOutcome(outcome); ok {
			susp = &Suspension{Kind: kind, Detail: detail, CreatedAt: at}
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM `task_suspension` WHERE `run_id` = ?", runID); err != nil {
		return err
	}
	if susp == nil {
		_, err := tx.ExecContext(ctx, "UPDATE `task_run` SET `suspend_kind` = NULL WHERE `id` = ?", runID)
		return err
	}
	var standing sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT `standing` FROM `task_run` WHERE `id` = ?", runID).
		Scan(&standing); err != nil {
		return err
	}
	if !standing.Valid {
		// A run started before standings were recorded: snapshot what it
		// has now, with the request it spent, which is the best there is.
		var spent sql.NullTime
		if err := tx.QueryRowContext(ctx, "SELECT `run_now_at` FROM `task_run` WHERE `id` = ?", runID).
			Scan(&spent); err != nil {
			return err
		}
		st, err := dispatchStandingOf(ctx, tx, taskID, timePtr(spent))
		if err != nil {
			return err
		}
		if standing.String, err = standingJSON(st); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE `task_run` SET `suspend_kind` = ? WHERE `id` = ?",
		string(susp.Kind), runID); err != nil {
		return err
	}
	created := susp.CreatedAt
	if created.IsZero() || created.Before(at) {
		created = at
	}
	_, err := tx.ExecContext(ctx,
		"INSERT INTO `task_suspension` (`run_id`,`task_id`,`kind`,`detail`,`resume_on`,`resume_at`,"+
			"`standing`,`model_hint`,`created_at`) VALUES (?,?,?,?,?,?,?,?,?)",
		runID, taskID, string(susp.Kind), susp.Detail, nullable(susp.ResumeOn), timeOf(susp.ResumeAt),
		standing.String, nullable(susp.ModelHint), created.UTC())
	return err
}

// resumeSuspensionsTx marks taskID's open suspensions resumed by runID,
// inside startRun's transaction: the run that starts next is the one
// that resumes them, whatever started it.
func resumeSuspensionsTx(ctx context.Context, tx *sql.Tx, taskID, runID string, at time.Time) error {
	_, err := tx.ExecContext(ctx,
		"UPDATE `task_suspension` SET `resumed_at` = ?, `resumed_run_id` = ? "+
			"WHERE `task_id` = ? AND `resumed_at` IS NULL",
		at.UTC(), runID, taskID)
	return err
}

// Suspensions is every suspension of taskID's runs, oldest first.
func (s *Store) Suspensions(ctx context.Context, taskID string) ([]Suspension, error) {
	var out []Suspension
	err := each(ctx, s.db,
		"SELECT `run_id`,`kind`,`detail`,`resume_on`,`resume_at`,`standing`,`model_hint`,"+
			"`created_at`,`resumed_at`,`resumed_run_id` FROM `task_suspension` "+
			"WHERE `task_id` = ? ORDER BY `created_at`, `run_id`", []any{taskID},
		func(rows *sql.Rows) error {
			su := Suspension{TaskID: taskID}
			var kind, standing string
			var resumeOn, hint, resumedRun sql.NullString
			var resumeAt, resumedAt sql.NullTime
			if err := rows.Scan(&su.RunID, &kind, &su.Detail, &resumeOn, &resumeAt, &standing, &hint,
				&su.CreatedAt, &resumedAt, &resumedRun); err != nil {
				return err
			}
			su.Kind = SuspensionKind(kind)
			su.ResumeOn, su.ModelHint, su.ResumedRunID = resumeOn.String, hint.String, resumedRun.String
			su.ResumeAt, su.ResumedAt = timePtr(resumeAt), timePtr(resumedAt)
			if err := json.Unmarshal([]byte(standing), &su.Standing); err != nil {
				return fmt.Errorf("reading run %s's standing: %w", su.RunID, err)
			}
			out = append(out, su)
			return nil
		})
	return out, err
}

// SuspensionStandingJSON is the standing runID's suspension recorded, as
// it is stored, for a caller comparing it byte for byte; "" when the run
// was not suspended.
func (s *Store) SuspensionStandingJSON(ctx context.Context, runID string) (string, error) {
	var standing string
	err := s.db.QueryRowContext(ctx, "SELECT `standing` FROM `task_suspension` WHERE `run_id` = ?", runID).
		Scan(&standing)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return standing, err
}

// DispatchStandingJSON is DispatchStanding encoded the way a suspension
// stores it.
func (s *Store) DispatchStandingJSON(ctx context.Context, taskID string) (string, error) {
	st, err := s.DispatchStanding(ctx, taskID)
	if err != nil {
		return "", err
	}
	return standingJSON(st)
}

// RunStanding is the DispatchStanding runID started with
// (task_run.standing), or nil for a run started before standings were
// recorded, or an unknown one.
func (s *Store) RunStanding(ctx context.Context, runID string) (*DispatchStanding, error) {
	var raw sql.NullString
	err := s.db.QueryRowContext(ctx, "SELECT `standing` FROM `task_run` WHERE `id` = ?", runID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !raw.Valid {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st DispatchStanding
	if err := json.Unmarshal([]byte(raw.String), &st); err != nil {
		return nil, fmt.Errorf("reading run %s's standing: %w", runID, err)
	}
	return &st, nil
}

// failoverKinds are the suspensions that say the model, not the task, is
// what stopped the run, and are resumed on the next model of the task's
// rotation (docs/design/durable-tasks.md#DT-2).
var failoverKinds = map[SuspensionKind]bool{SuspendCapacity: true, SuspendUsageLimit: true, SuspendModel: true}

// IsFailover reports whether k is resumed on another model: capacity,
// usage-limit and model.
func (k SuspensionKind) IsFailover() bool { return failoverKinds[k] }

// MaxFailoverModels is how many different models may fail a task in a
// row before a person is told.
const MaxFailoverModels = 3

// FailedOverModels is the distinct models (task_suspension.model_hint)
// taskID's most recent runs were suspended off by a failover kind, most
// recent first: the run of them back to the last run that was not a
// suspension, passing over suspensions of other kinds.
func (s *Store) FailedOverModels(ctx context.Context, taskID string) ([]string, error) {
	var out []string
	stop := false
	err := each(ctx, s.db,
		"SELECT `r`.`suspend_kind`, `su`.`model_hint` FROM `task_run` AS `r` "+
			"LEFT JOIN `task_suspension` AS `su` ON `su`.`run_id` = `r`.`id` "+
			"WHERE `r`.`task_id` = ? AND `r`.`finished_at` IS NOT NULL ORDER BY `r`.`attempt` DESC",
		[]any{taskID}, func(rows *sql.Rows) error {
			var kind, hint sql.NullString
			if err := rows.Scan(&kind, &hint); err != nil {
				return err
			}
			switch {
			case stop:
			case !kind.Valid:
				stop = true
			case SuspensionKind(kind.String).IsFailover() && hint.String != "" && !slices.Contains(out, hint.String):
				out = append(out, hint.String)
			}
			return nil
		})
	return out, err
}

// SuspensionResumeAt is the earliest taskID may restart after its latest
// suspension (Suspension.ResumeAt), or nil when that suspension names no
// time, has already been resumed, or there is none. dispatch.Cycle holds
// a task until then: it is how a durable task's setup suspension waits
// out its backoff without a strike to back off from.
func (s *Store) SuspensionResumeAt(ctx context.Context, taskID string) (*time.Time, error) {
	var resumeAt sql.NullTime
	err := s.db.QueryRowContext(ctx,
		"SELECT `resume_at` FROM `task_suspension` WHERE `task_id` = ? AND `resumed_at` IS NULL "+
			"ORDER BY `created_at` DESC, `run_id` DESC LIMIT 1", taskID).Scan(&resumeAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading when task %s may resume: %w", taskID, err)
	}
	return timePtr(resumeAt), nil
}

// SuspensionsInARow is how many of taskID's most recent finished runs, in
// a row, were suspended with kind: the count a durable task's setup
// suspensions are held to (DurableSetupFailingAfter).
func (s *Store) SuspensionsInARow(ctx context.Context, taskID string, kind SuspensionKind) (int, error) {
	n, done := 0, false
	err := each(ctx, s.db,
		"SELECT COALESCE(`suspend_kind`, '') FROM `task_run` WHERE `task_id` = ? AND `finished_at` IS NOT NULL "+
			"ORDER BY `started_at` DESC, `attempt` DESC", []any{taskID},
		func(rows *sql.Rows) error {
			var k string
			if err := rows.Scan(&k); err != nil {
				return err
			}
			if done || SuspensionKind(k) != kind {
				done = true
				return nil
			}
			n++
			return nil
		})
	return n, err
}

// SetupFailureDetailsSince is the detail of every run, of any task, that
// finished at or after since without reaching its agent: a setup failure
// (SetupFailedOutcome) or a setup suspension. It is what the
// deployment-wide exec-tunnel alert counts (orchestrator's
// noteExecTunnelRate), which is why it reads across tasks.
func (s *Store) SetupFailureDetailsSince(ctx context.Context, since time.Time) ([]string, error) {
	var details []string
	err := each(ctx, s.db,
		"SELECT COALESCE(`detail`, '') FROM `task_run` WHERE `finished_at` IS NOT NULL AND `finished_at` >= ? "+
			"AND (`outcome` = ? OR `suspend_kind` = ?)", []any{since.UTC(), SetupFailedOutcome, string(SuspendSetup)},
		func(rows *sql.Rows) error {
			var d string
			if err := rows.Scan(&d); err != nil {
				return err
			}
			details = append(details, d)
			return nil
		})
	return details, err
}
