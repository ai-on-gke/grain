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
// stopped the turn -- but not a suspension, a park among them, which
// settleRunNow puts back whatever its outcome. So does EnvironmentOutcome, though it is
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

// settleRunNow is the store's RestoreRunNow, for every suspension kind
// as well as the outcomes RunNowSurvives names. It decides, inside the transaction that records runID's
// outcome (FinishRun, SetRunOutcome), what becomes of the Run now
// request that run spent, if it spent one: put back on the task with
// its original timestamp where RunNowSurvives says the run was not a run
// of the task, and left spent otherwise. Done there rather than by
// anything that reads the outcome later so that, as with startRun's own
// clearing, no crash can fall between the outcome and the request: the
// two are one write.
//
// Not put back, whatever the outcome:
//   - on a task closed meanwhile, which Store.RunNow would not start;
//   - for a run that is no longer its task's latest -- a re-recorded
//     outcome on an old run (a refusal burst's earlier runs) is history,
//     and the request is a later run's to settle;
//   - over a request somebody has made since, which already stands;
//   - for a setup failure that brought the task's failure streak to
//     MaxConsecutiveFailures. setup-failed is the one ending on the list
//     that counts towards the streak, and a task that cannot get a
//     sandbox five times in a row is one task_state stops dispatching.
//     A request that outlived that would go on starting it -- spaced
//     out by dispatch's retry backoff, but without end.
//
// A run whose outcome is re-recorded from one that put the request back
// to one that does not withdraws it again, where the request standing
// is still the one it put back.
//
// It reports whether it put a request back, having said so on the task.
func settleRunNow(ctx context.Context, tx *sql.Tx, runID, taskID string) (bool, error) {
	var spent, finished sql.NullTime
	var outcome, suspended sql.NullString
	var attempt int
	err := tx.QueryRowContext(ctx,
		"SELECT `run_now_at`,`finished_at`,`outcome`,`attempt`,`suspend_kind` FROM `task_run` WHERE `id` = ?", runID).
		Scan(&spent, &finished, &outcome, &attempt, &suspended)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (!spent.Valid || !finished.Valid) {
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
	ending, survives := RunNowSurvives(outcome.String)
	if suspended.Valid && !survives {
		// Every suspension keeps the request, whatever its kind
		// (design/design/durable-tasks.design.md#DT-3): the four RunNowSurvives
		// names have words of their own, and the rest are said by kind.
		ending, survives = fmt.Sprintf("was suspended (%s)", suspended.String), true
	}
	if survives && outcome.String == "setup-failed" {
		var streak sql.NullInt64
		if err := tx.QueryRowContext(ctx,
			"SELECT `streak_count` FROM `task_observation` WHERE `task_id` = ?", taskID).
			Scan(&streak); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		survives = streak.Int64 < MaxConsecutiveFailures
	}
	if !survives {
		_, err := tx.ExecContext(ctx,
			"UPDATE `task_observation` SET `run_now_at` = NULL WHERE `task_id` = ? AND `run_now_at` = ?",
			taskID, spent.Time.UTC())
		return false, err
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
	when := "the next cycle starts it ahead of the queue again"
	if p, err := getPause(ctx, tx, taskID); err != nil {
		return false, err
	} else if p != nil {
		when = "it starts ahead of the queue once the task is resumed"
	}
	if _, err := addComment(ctx, tx, Comment{
		TaskID: taskID,
		Author: Attribution{Actor: Principal{Kind: PrincipalAutomation, ID: "grain"}},
		Body: fmt.Sprintf("Run now kept: run %s %s. That was not a run of this task, so the request "+
			"still stands and %s.", runID, ending, when),
		CreatedAt: finished.Time,
	}); err != nil {
		return false, err
	}
	return true, nil
}
