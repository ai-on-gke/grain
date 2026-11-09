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

// RunNowSurvives reports whether a run that ended with outcome leaves the
// Run now request it spent (task_run.run_now_at) standing, and if it
// does, the words for how it ended -- "ended setup-failed before
// reaching its agent" -- that the note on the task is built from.
//
// A request is worth one run of the task (Observation.RunNowAt), and
// these are the endings that are not one: evidence about the deployment
// rather than about the task, after which the person who pressed Run
// now is still waiting for exactly what they asked for.
//
//   - "setup-failed": the run never reached its agent.
//   - PausedOutcome: a usage limit stopped it, whether it met the limit
//     or was cancelled by the pause another run began.
//   - CapacityOutcome: the provider had no room for it.
//   - InterruptedOutcome: the daemon stopped under it, or grain stopped
//     it to make room for a base-branch repair.
//   - OOMOutcome: the kernel killed its sandbox. A run past
//     MaxOOMRetries is recorded as an ordinary failure, so it spends
//     the request as any failure does.
//
// Those are also the whole of the runs grain cancels on its own account
// and the agent's work did not decide: a pause and a pre-emption are
// recorded as the two words above. The wall-clock cap is not among
// them: a run that hit it had its agent working for the whole budget,
// which is a run of the task, and putting the request back would spend
// another budget the same way.
//
// Everything else spends the request: a success, a failure,
// nothing_needed, and a cancellation, whether a person closed the task or
// stopped the turn. So does a suspension in which the agent ran its turn
// (SuspensionKind.KeepsRunNow): the agent's own resume_later or
// wait_for_task park (SuspendRequested) above all -- that run was the run
// the person asked for, and a request it handed back would stand, older
// than the run, for as long as the task stayed parked (task 3557, where
// the stamp kept the Run now button disabled and the task held to the
// park's resume time for a day). So does EnvironmentOutcome, though it is
// the deployment's doing too: a run that ends that way parks its task on
// a comment for a human who has to fix the host, and a request
// (Store.RunNow consults no blocker) would walk straight past that park
// into the same broken host. The human's reply is what queues it again.
func RunNowSurvives(outcome string) (ending string, ok bool) {
	switch outcome {
	case "setup-failed":
		return "ended setup-failed before reaching its agent", true
	case PausedOutcome:
		return "was paused on a usage limit", true
	case CapacityOutcome:
		return "ended with the provider out of capacity", true
	case InterruptedOutcome:
		return "was interrupted", true
	case OOMOutcome:
		return "was OOM-killed", true
	}
	return "", false
}

// KeepsRunNow reports whether a run suspended with k leaves the Run now
// request it spent standing, and if it does, the words for how it ended.
//
// A request is worth one run of the task. The kinds that keep it are the
// deployment's, the provider's or an operator's interruptions -- setup
// failing before the agent ran, a restart or pre-emption, an OOM kill, a
// usage limit, no capacity, a model-side failure, a restart to apply new
// grants, an operator's pause -- after which the person who pressed Run
// now is still waiting for what they asked for. The kinds in which the
// agent ran its turn -- requested (resume_later, wait_for_task's parks),
// deadline, idle and context -- spent it.
func (k SuspensionKind) KeepsRunNow() (ending string, ok bool) {
	switch k {
	case SuspendSetup:
		return "ended setup-failed before reaching its agent", true
	case SuspendRestart, SuspendOperatorPause:
		return "was interrupted", true
	case SuspendOOM:
		return "was OOM-killed", true
	case SuspendUsageLimit:
		return "was paused on a usage limit", true
	case SuspendCapacity:
		return "ended with the provider out of capacity", true
	case SuspendModel:
		return "ended on a model-side failure", true
	case SuspendCapability:
		return "was restarted to pick up changed capabilities", true
	}
	return "", false
}

// runNowKept is whether a run that ended with outcome, suspended with
// suspended (empty for none), gives back the request it spent, and the
// words for how it ended. A suspension is judged by its kind
// (KeepsRunNow), anything else by its outcome (RunNowSurvives).
func runNowKept(outcome, suspended string) (string, bool) {
	if suspended != "" {
		return SuspensionKind(suspended).KeepsRunNow()
	}
	return RunNowSurvives(outcome)
}

// settleRunNow decides, inside the transaction that records runID's
// outcome (FinishRun, Suspend, SetRunOutcome), what becomes of the Run now
// request that run spent, if it spent one: put back on the task with its
// original timestamp where runNowKept says the run never got to be the
// run that was asked for, and left spent otherwise. Done there rather
// than by anything that reads the outcome later so that, as with
// startRun's own clearing, no crash can fall between the outcome and the
// request: the two are one write.
//
// Not put back, whatever the outcome:
//   - on a task closed meanwhile, which Store.RunNow would not start;
//   - for a run that is no longer its task's latest -- a re-recorded
//     outcome on an old run (a refusal burst's earlier runs) is history,
//     and the request is a later run's to settle;
//   - over a request somebody has made since, which already stands;
//   - for a setup failure that brought the task's failure streak to
//     MaxConsecutiveFailures, or a durable task's DurableSetupFailingAfter'th
//     setup suspension in a row. A task that cannot get a sandbox that
//     many times running is one a person has to look at, and a request
//     that outlived it would go on starting it -- spaced out by
//     dispatch's backoff, but without end.
//
// A run that spent the request withdraws any stamp standing on the task
// that is no newer than the run's own start: the request it spent, put
// back by an earlier record of the same run (SetRunOutcome), or a stamp
// an older build left behind. A request somebody made while the run was
// live is newer than its start and stands.
//
// It reports whether it put a request back, having said so on the task.
func settleRunNow(ctx context.Context, tx *sql.Tx, runID, taskID string) (bool, error) {
	var spent, started, finished sql.NullTime
	var outcome, suspended sql.NullString
	var attempt int
	err := tx.QueryRowContext(ctx,
		"SELECT `run_now_at`,`started_at`,`finished_at`,`outcome`,`attempt`,`suspend_kind` FROM `task_run` WHERE `id` = ?", runID).
		Scan(&spent, &started, &finished, &outcome, &attempt, &suspended)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !finished.Valid {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var later int
	if err := tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `task_run` WHERE `task_id` = ? AND `attempt` > ?", taskID, attempt).
		Scan(&later); err != nil {
		return false, err
	}
	if later > 0 {
		return false, nil
	}
	ending, survives := runNowKept(outcome.String, suspended.String)
	if survives && outcome.String == SetupFailedOutcome {
		var streak sql.NullInt64
		if err := tx.QueryRowContext(ctx,
			"SELECT `streak_count` FROM `task_observation` WHERE `task_id` = ?", taskID).
			Scan(&streak); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		survives = streak.Int64 < MaxConsecutiveFailures
	}
	if survives && SuspensionKind(suspended.String) == SuspendSetup {
		n, err := suspensionsInARow(ctx, tx, taskID, SuspendSetup)
		if err != nil {
			return false, err
		}
		survives = n < DurableSetupFailingAfter
	}
	if !survives {
		// Compared here rather than in SQL: a stored time's text does not
		// order the way the time does on every driver.
		var standing sql.NullTime
		if err := tx.QueryRowContext(ctx,
			"SELECT `run_now_at` FROM `task_observation` WHERE `task_id` = ?", taskID).
			Scan(&standing); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		if standing.Valid && (spent.Valid && standing.Time.Equal(spent.Time) ||
			started.Valid && !standing.Time.After(started.Time)) {
			_, err := tx.ExecContext(ctx,
				"UPDATE `task_observation` SET `run_now_at` = NULL WHERE `task_id` = ?", taskID)
			return false, err
		}
		return false, nil
	}
	if !spent.Valid {
		return false, nil
	}
	res, err := tx.ExecContext(ctx,
		"UPDATE `task_observation` SET `run_now_at` = ? "+
			"WHERE `task_id` = ? AND `run_now_at` IS NULL AND `closed_at` IS NULL",
		spent.Time.UTC(), taskID)
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return false, err
	}
	// A paused task's request waits for the resume (TaskPause): RunNow
	// leaves it out until then, so the note says so rather than promising
	// the next cycle.
	when := "it starts ahead of the queue again once the retry backoff has passed"
	if p, err := getPause(ctx, tx, taskID); err != nil {
		return false, err
	} else if p != nil {
		when = "it starts ahead of the queue once the task is resumed"
	}
	if _, err := addComment(ctx, tx, Comment{
		TaskID: taskID,
		Author: Attribution{Actor: Principal{Kind: PrincipalAutomation, ID: "grain"}},
		Body: fmt.Sprintf("Run now kept: run %s %s before it could do the work, so the request "+
			"still stands and %s.", runID, ending, when),
		CreatedAt: finished.Time,
	}); err != nil {
		return false, err
	}
	return true, nil
}

// suspensionsInARow is Store.SuspensionsInARow inside a transaction.
func suspensionsInARow(ctx context.Context, q querier, taskID string, kind SuspensionKind) (int, error) {
	n, done := 0, false
	err := each(ctx, q,
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

// clearSpentRunNow withdraws the Run now stamps older builds left
// standing after a run had spent them (task 3557): one on a closed task,
// which Store.RunNow never starts and only blocks a request after a
// reopen; and one no newer than the start of its task's latest finished
// run, when that run spent it -- the request an agent's resume_later used
// to hand back. Store.Init runs it on every start, so a deployment's
// stuck tasks are released by the upgrade that carries this fix.
func (s *Store) clearSpentRunNow(ctx context.Context) error {
	return s.write(ctx, "clear spent run-now requests", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			"UPDATE `task_observation` SET `run_now_at` = NULL "+
				"WHERE `run_now_at` IS NOT NULL AND `closed_at` IS NOT NULL"); err != nil {
			return err
		}
		type stamp struct {
			taskID string
			at     time.Time
		}
		var stamps []stamp
		if err := each(ctx, tx,
			"SELECT `task_id`, `run_now_at` FROM `task_observation` WHERE `run_now_at` IS NOT NULL", nil,
			func(rows *sql.Rows) error {
				var st stamp
				if err := rows.Scan(&st.taskID, &st.at); err != nil {
					return err
				}
				stamps = append(stamps, st)
				return nil
			}); err != nil {
			return err
		}
		for _, st := range stamps {
			var started, finished sql.NullTime
			var outcome, suspended sql.NullString
			err := tx.QueryRowContext(ctx,
				"SELECT `started_at`, `finished_at`, `outcome`, `suspend_kind` FROM `task_run` "+
					"WHERE `task_id` = ? ORDER BY `attempt` DESC LIMIT 1", st.taskID).
				Scan(&started, &finished, &outcome, &suspended)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			if !started.Valid || !finished.Valid || st.at.After(started.Time) {
				continue
			}
			if _, kept := runNowKept(outcome.String, suspended.String); kept {
				continue
			}
			if _, err := tx.ExecContext(ctx,
				"UPDATE `task_observation` SET `run_now_at` = NULL WHERE `task_id` = ?", st.taskID); err != nil {
				return err
			}
		}
		return nil
	})
}
