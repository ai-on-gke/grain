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
	"strconv"
	"time"
)

// NewSuiteID allocates a suite identity from its own sequence, distinct
// from every other id sequence here -- NewScheduleID's own doc comment
// on why.
func (s *Store) NewSuiteID(ctx context.Context) (id string, err error) {
	err = s.write(ctx, "allocate a suite id", func(tx *sql.Tx) error {
		id, err = newSuiteID(ctx, tx)
		return err
	})
	return id, err
}

func newSuiteID(ctx context.Context, tx *sql.Tx) (string, error) {
	res, err := tx.ExecContext(ctx,
		"INSERT INTO `suite_sequence` (`issued_at`) VALUES (?)", time.Now().UTC())
	if err != nil {
		return "", fmt.Errorf("allocating a suite id: %w", err)
	}
	n, err := res.LastInsertId()
	if err != nil {
		return "", fmt.Errorf("reading the allocated suite id: %w", err)
	}
	return "suite-" + strconv.FormatInt(n, 10), nil
}

const suiteColumns = "`id`,`name`,`mode`,`count`,`max_passes`,`require_approval`,`auto_merge`,`auto_approve_proposals`,`created_at`"

func scanSuite(scan func(...any) error) (Suite, error) {
	var s Suite
	var mode string
	if err := scan(&s.ID, &s.Name, &mode, &s.Count, &s.MaxPasses, &s.RequireApproval, &s.AutoMerge, &s.AutoApproveProposals, &s.CreatedAt); err != nil {
		return Suite{}, err
	}
	s.CreatedAt = s.CreatedAt.UTC()
	s.Mode = SuiteMode(mode)
	return s, nil
}

func suiteItemsOf(ctx context.Context, q querier, suiteID string) ([]SuiteItem, error) {
	var out []SuiteItem
	err := each(ctx, q,
		"SELECT `template_id` FROM `suite_item` WHERE `suite_id` = ? ORDER BY `order_key`",
		suiteID, func(rows *sql.Rows) error {
			var it SuiteItem
			if err := rows.Scan(&it.TemplateID); err != nil {
				return err
			}
			out = append(out, it)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading items of suite %s: %w", suiteID, err)
	}
	return out, nil
}

func getSuite(ctx context.Context, q querier, id string) (*Suite, error) {
	suite, err := scanSuite(q.QueryRowContext(ctx,
		"SELECT "+suiteColumns+" FROM `suite` WHERE `id` = ?", id).Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	items, err := suiteItemsOf(ctx, q, id)
	if err != nil {
		return nil, err
	}
	suite.Items = items
	return &suite, nil
}

// GetSuite returns a suite, or nil if there is none with that ID.
func (s *Store) GetSuite(ctx context.Context, id string) (*Suite, error) {
	return getSuite(ctx, s.db, id)
}

// ListSuites returns every suite, newest first.
func (s *Store) ListSuites(ctx context.Context) ([]Suite, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+suiteColumns+" FROM `suite` ORDER BY `created_at` DESC, `id` DESC")
	if err != nil {
		return nil, fmt.Errorf("listing suites: %w", err)
	}
	var out []Suite
	for rows.Next() {
		row, err := scanSuite(rows.Scan)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for i := range out {
		items, err := suiteItemsOf(ctx, s.db, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Items = items
	}
	return out, nil
}

// PutSuite inserts or replaces a suite wholesale -- putSchedule's own
// "child rows are deleted and re-inserted rather than diffed" for a
// suite's own items.
func (s *Store) PutSuite(ctx context.Context, suite Suite) error {
	return s.write(ctx, "put suite "+suite.ID,
		func(tx *sql.Tx) error { return putSuite(ctx, tx, suite) })
}

func putSuite(ctx context.Context, tx *sql.Tx, suite Suite) error {
	_, err := tx.ExecContext(ctx,
		"REPLACE INTO `suite` ("+suiteColumns+") VALUES (?,?,?,?,?,?,?,?,?)",
		suite.ID, suite.Name, string(suite.Mode), suite.Count, suite.MaxPasses,
		suite.RequireApproval, suite.AutoMerge, suite.AutoApproveProposals, suite.CreatedAt.UTC())
	if err != nil {
		return fmt.Errorf("writing suite %s: %w", suite.ID, err)
	}
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM `suite_item` WHERE `suite_id` = ?", suite.ID); err != nil {
		return err
	}
	for i, it := range suite.Items {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `suite_item` (`suite_id`,`template_id`,`order_key`) VALUES (?,?,?)",
			suite.ID, it.TemplateID, float64(i)); err != nil {
			return fmt.Errorf("writing suite item %q: %w", it.TemplateID, err)
		}
	}
	return nil
}

// UpdateSuite reads a suite, applies mutate, and writes it back --
// UpdateSchedule's own read-modify-write shape.
func (s *Store) UpdateSuite(ctx context.Context, id string, mutate func(*Suite) error) error {
	var missing bool
	err := s.write(ctx, "update suite "+id, func(tx *sql.Tx) error {
		missing = false
		suite, err := getSuite(ctx, tx, id)
		if err != nil {
			return err
		}
		if suite == nil {
			missing = true
			return nil
		}
		if err := mutate(suite); err != nil {
			return err
		}
		return putSuite(ctx, tx, *suite)
	})
	if err != nil {
		return err
	}
	if missing {
		return fmt.Errorf("updating suite %s: no such suite", id)
	}
	return nil
}

// DeleteSuite removes a suite outright -- DeleteSchedule's own doc
// comment gives the reasoning: a suite is only ever a standing
// declaration, so there is no history on the row itself worth keeping.
// A run already started from this suite is untouched -- it carries its
// own snapshot of everything it needs (model.SuiteRun's own doc
// comment) and does not join back to suite for anything.
func (s *Store) DeleteSuite(ctx context.Context, id string) error {
	return s.write(ctx, "delete suite "+id, func(tx *sql.Tx) error {
		// Both tables, in one transaction: suite_item carries no
		// foreign key onto suite (nothing in this schema does), so
		// dropping only the parent row would leave every item behind
		// for good -- rows SuitesUsingTemplate still finds and then has
		// to discard for naming a suite that no longer exists.
		if err := deleteCompanionRows(ctx, tx, suiteCompanions, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM `suite` WHERE `id` = ?", id)
		return err
	})
}

// SuitesUsingTemplate returns every suite with an item referencing
// templateID -- what ui.Client.DeleteTemplate checks before deleting
// one out from under a suite that still runs it,
// SchedulesUsingTemplate's own reasoning applied to this third caller
// of Template.
func (s *Store) SuitesUsingTemplate(ctx context.Context, templateID string) ([]Suite, error) {
	var ids []string
	err := each(ctx, s.db,
		"SELECT DISTINCT `suite_id` FROM `suite_item` WHERE `template_id` = ? ORDER BY `suite_id`",
		templateID, func(rows *sql.Rows) error {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("finding suites using template %s: %w", templateID, err)
	}
	out := make([]Suite, 0, len(ids))
	for _, id := range ids {
		suite, err := s.GetSuite(ctx, id)
		if err != nil {
			return nil, err
		}
		if suite != nil {
			out = append(out, *suite)
		}
	}
	return out, nil
}

// --- suite runs -----------------------------------------------

const suiteRunColumns = "`id`,`suite_id`,`suite_name`,`schedule_id`,`owner`,`repo`,`base`,`mode`,`count`,`max_passes`," +
	"`require_approval`,`auto_merge`,`auto_approve_proposals`,`status`,`last_error`,`created_at`,`completed_at`"

func scanSuiteRun(scan func(...any) error) (SuiteRun, error) {
	var r SuiteRun
	var mode, status string
	var scheduleID, lastError sql.NullString
	var completedAt sql.NullTime
	if err := scan(&r.ID, &r.SuiteID, &r.SuiteName, &scheduleID, &r.Target.Owner, &r.Target.Name, &r.Base,
		&mode, &r.Count, &r.MaxPasses, &r.RequireApproval, &r.AutoMerge, &r.AutoApproveProposals,
		&status, &lastError, &r.CreatedAt, &completedAt); err != nil {
		return SuiteRun{}, err
	}
	r.Target = RepoFromStored(r.Target.Owner, r.Target.Name)
	r.CreatedAt = r.CreatedAt.UTC()
	r.Mode = SuiteMode(mode)
	r.Status = SuiteRunStatus(status)
	r.ScheduleID = scheduleID.String
	r.LastError = lastError.String
	r.CompletedAt = timePtr(completedAt)
	return r, nil
}

func suiteRunItemsOf(ctx context.Context, q querier, runID int64) ([]SuiteItem, error) {
	var out []SuiteItem
	err := each(ctx, q,
		"SELECT `template_id` FROM `suite_run_item` WHERE `run_id` = ? ORDER BY `order_key`",
		runID, func(rows *sql.Rows) error {
			var it SuiteItem
			if err := rows.Scan(&it.TemplateID); err != nil {
				return err
			}
			out = append(out, it)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading items of suite run %d: %w", runID, err)
	}
	return out, nil
}

// suiteRunTaskStatusesOf reads every task instance a run's passes have
// instantiated so far, oldest first -- qualificationTaskStatusesOf's
// own join, with OpenedPullRequest and Proposed read straight off
// task_link rather than off task_observation: a task's own LinkFixes
// link is written the same cycle finishWithPullRequest opens its pull
// request, one cycle ahead of when SyncPullRequests would first set
// Observation.PrOpenedAt, and OutcomeOfPass reading it here is one less
// cycle of lag before SyncSuites can act on a pass that just produced a
// change.
func suiteRunTaskStatusesOf(ctx context.Context, q querier, runID int64) ([]SuiteTaskStatus, error) {
	var out []SuiteTaskStatus
	err := each(ctx, q,
		"SELECT `rt`.`task_id`,`rt`.`template_id`,`rt`.`template_name`,`rt`.`pass_number`,"+
			"(`t`.`approval_actor_kind` IS NOT NULL),`ts`.`state`,"+
			"EXISTS(SELECT 1 FROM `task_link` WHERE `task_id` = `rt`.`task_id` AND `kind` = ?),"+
			"EXISTS(SELECT 1 FROM `task_link` WHERE `kind` = ? AND `target` = `rt`.`task_id`) "+
			"FROM `suite_run_task` AS `rt` "+
			"JOIN `task` AS `t` ON `t`.`id` = `rt`.`task_id` "+
			"JOIN `task_state` AS `ts` ON `ts`.`task_id` = `rt`.`task_id` "+
			"WHERE `rt`.`run_id` = ? ORDER BY `rt`.`id`",
		[]any{string(LinkFixes), string(LinkProposedBy), runID},
		func(rows *sql.Rows) error {
			var st SuiteTaskStatus
			var state string
			if err := rows.Scan(&st.TaskID, &st.TemplateID, &st.TemplateName, &st.PassNumber,
				&st.Approved, &state, &st.OpenedPullRequest, &st.Proposed); err != nil {
				return err
			}
			st.State = State(state)
			out = append(out, st)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading tasks of suite run %d: %w", runID, err)
	}
	return out, nil
}

func getSuiteRun(ctx context.Context, q querier, id int64) (*SuiteRun, error) {
	run, err := scanSuiteRun(q.QueryRowContext(ctx,
		"SELECT "+suiteRunColumns+" FROM `suite_run` WHERE `id` = ?", id).Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	items, err := suiteRunItemsOf(ctx, q, id)
	if err != nil {
		return nil, err
	}
	run.Items = items
	tasks, err := suiteRunTaskStatusesOf(ctx, q, id)
	if err != nil {
		return nil, err
	}
	run.Tasks = tasks
	return &run, nil
}

// GetSuiteRun returns one run, fully hydrated, or nil if there is none
// with that ID.
func (s *Store) GetSuiteRun(ctx context.Context, id int64) (*SuiteRun, error) {
	return getSuiteRun(ctx, s.db, id)
}

func listSuiteRunIDs(ctx context.Context, q querier, query string, args any) ([]int64, error) {
	var ids []int64
	err := each(ctx, q, query, args, func(rows *sql.Rows) error {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
		return nil
	})
	return ids, err
}

// ListSuiteRuns returns every run, newest first -- what a "see the
// status of outstanding suite runs" view needs (bwsalmon/agents#642).
func (s *Store) ListSuiteRuns(ctx context.Context) ([]SuiteRun, error) {
	ids, err := listSuiteRunIDs(ctx, s.db,
		"SELECT `id` FROM `suite_run` ORDER BY `created_at` DESC, `id` DESC", nil)
	if err != nil {
		return nil, fmt.Errorf("listing suite runs: %w", err)
	}
	out := make([]SuiteRun, 0, len(ids))
	for _, id := range ids {
		run, err := getSuiteRun(ctx, s.db, id)
		if err != nil {
			return nil, err
		}
		if run != nil {
			out = append(out, *run)
		}
	}
	return out, nil
}

// ActiveSuiteRuns returns every run whose Status is still
// SuiteRunActive -- what SyncSuites polls each cycle,
// QualifiableActiveCandidates' own "only what this cycle might still
// need to do something for" shape.
func (s *Store) ActiveSuiteRuns(ctx context.Context) ([]SuiteRun, error) {
	ids, err := listSuiteRunIDs(ctx, s.db,
		"SELECT `id` FROM `suite_run` WHERE `status` = ? ORDER BY `id`", string(SuiteRunActive))
	if err != nil {
		return nil, fmt.Errorf("listing active suite runs: %w", err)
	}
	out := make([]SuiteRun, 0, len(ids))
	for _, id := range ids {
		run, err := getSuiteRun(ctx, s.db, id)
		if err != nil {
			return nil, err
		}
		if run != nil {
			out = append(out, *run)
		}
	}
	return out, nil
}

// SuiteRunOrigin is where a task a suite run filed came from: the run,
// the suite it ran (by its snapshotted name), which pass, and the
// template the task was instantiated from -- one suite_run_task row,
// read from the task's end rather than the run's.
type SuiteRunOrigin struct {
	RunID        int64
	SuiteName    string
	PassNumber   int
	TemplateName string
}

// SuiteRunOfTask returns the suite run that filed taskID, or nil for a
// task no suite run filed -- and for one whose run is no longer in the
// store, which joins against suite_run rather than trusting the
// suite_run_task row alone, so the task's page says "a suite filed
// this" without linking to a run that is not there.
func (s *Store) SuiteRunOfTask(ctx context.Context, taskID string) (*SuiteRunOrigin, error) {
	var o SuiteRunOrigin
	err := s.db.QueryRowContext(ctx,
		"SELECT `rt`.`run_id`,`r`.`suite_name`,`rt`.`pass_number`,`rt`.`template_name` "+
			"FROM `suite_run_task` AS `rt` "+
			"JOIN `suite_run` AS `r` ON `r`.`id` = `rt`.`run_id` "+
			"WHERE `rt`.`task_id` = ?", taskID).
		Scan(&o.RunID, &o.SuiteName, &o.PassNumber, &o.TemplateName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the suite run of task %s: %w", taskID, err)
	}
	return &o, nil
}

// HasActiveRunForSchedule reports whether scheduleID already has a run
// that has not finished -- the idempotency check a schedule firing a
// suite needs before starting another one, and the exact counterpart of
// HasOpenTaskWithTag for a schedule that files a task instead: a chore
// that runs long must not get a duplicate, and one that finishes early is
// still held to its own cadence rather than refiring immediately
// (docs/schedules.md).
func (s *Store) HasActiveRunForSchedule(ctx context.Context, scheduleID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `suite_run` WHERE `schedule_id` = ? AND `status` = ?",
		scheduleID, string(SuiteRunActive)).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("checking for an active run of schedule %s: %w", scheduleID, err)
	}
	return n > 0, nil
}

// MissingSuiteTemplateError is resolveSuiteTemplates' answer for an item
// whose template is no longer in the store: deleted from the templates
// page (which refuses only while a suite or a schedule still names it,
// not while a run already started from one does), or dropped from the
// repository that declared it. Unlike a store error it is not going to
// clear on the next try, which is why it has a type of its own:
// orchestrator.SyncSuites ends a run on it, rather than retrying a pass
// that can never be filed every cycle while the run reads "active".
type MissingSuiteTemplateError struct {
	TemplateID string
}

func (e *MissingSuiteTemplateError) Error() string {
	return fmt.Sprintf("template %s no longer exists", e.TemplateID)
}

// resolveSuiteTemplates resolves every item's own template fresh from
// the store -- CreateQualificationRun's own "not a stale copy"
// discipline, read before the write transaction that uses them opens,
// the same ordering that call already uses for the same reason: a
// template lookup inside the transaction would be a second connection
// reading while the first still holds sqlite's single writer lock.
func resolveSuiteTemplates(ctx context.Context, s *Store, items []SuiteItem) (map[string]Template, error) {
	templates := make(map[string]Template, len(items))
	for _, it := range items {
		if _, ok := templates[it.TemplateID]; ok {
			continue
		}
		tmpl, err := s.GetTemplate(ctx, it.TemplateID)
		if err != nil {
			return nil, fmt.Errorf("resolving template %s: %w", it.TemplateID, err)
		}
		if tmpl == nil {
			return nil, &MissingSuiteTemplateError{TemplateID: it.TemplateID}
		}
		templates[it.TemplateID] = *tmpl
	}
	return templates, nil
}

// fireSuitePass instantiates one pass of a run: every item, in order,
// composed by compose.go's own algorithm (composeSuitePass, which
// ui.Client.SuiteComposition previews with) and filed --
// CreateQualificationRun's own per-instance task shape, minus the
// per-item Repeat/DependsOn this feature has no DAG for (model.Suite's
// own doc comment on why: a suite repeats whole passes, not individual
// items, so nothing here needs one item to wait on another within the
// same pass).
//
// What the composition decides, and why, is ComposeSuiteTask's own doc
// comment: content from each item's template, placement from the run
// unless that template is bound to a repo of its own, and approval and
// auto-merge from the run, for every task in every pass of it.
func fireSuitePass(ctx context.Context, tx *sql.Tx, runID int64, items []SuiteItem,
	templates map[string]Template, target RepoRef, base string,
	requireApproval, autoMerge, autoApproveProposals bool, passNumber int, orderKey float64, now time.Time) error {

	composed, err := composeSuitePass(ctx, tx, items, templates, target, base, requireApproval, autoMerge, autoApproveProposals)
	if err != nil {
		return err
	}
	for i, it := range items {
		id, err := newTaskID(ctx, tx)
		if err != nil {
			return err
		}
		task := composed[i].Task(id, SuitePrincipal, ReasonSuite, now)
		task.OrderKey = orderKey
		orderKey += orderKeySpacing
		if err := putTask(ctx, tx, task); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `suite_run_task` (`run_id`,`task_id`,`template_id`,`template_name`,`pass_number`) VALUES (?,?,?,?,?)",
			runID, id, it.TemplateID, templates[it.TemplateID].Name, passNumber); err != nil {
			return err
		}
	}
	return nil
}

// CreateSuiteRun starts a new run of suite against target and base,
// filing its first pass immediately -- bwsalmon/agents#642's own "run
// the template against a repo and branch." suite's own Items/Mode/
// Count/MaxPasses/RequireApproval/AutoMerge are copied onto the run
// (model.SuiteRun's own doc comment on why), so editing suite after
// this call changes nothing about the run it just started.
func (s *Store) CreateSuiteRun(ctx context.Context, suite Suite, target RepoRef, base string, now time.Time) (SuiteRun, error) {
	return s.createSuiteRun(ctx, suite, target, base, "", now)
}

// CreateScheduledSuiteRun is CreateSuiteRun for a run a schedule fired
// rather than a human started -- identical in every respect except that
// the run records the schedule it came from (SuiteRun.ScheduleID's own
// doc comment on the two things that buys). Its own method rather than
// a fifth positional argument on CreateSuiteRun, since every existing
// caller starts a run by hand and has no schedule to name.
func (s *Store) CreateScheduledSuiteRun(ctx context.Context, suite Suite, target RepoRef, base, scheduleID string, now time.Time) (SuiteRun, error) {
	return s.createSuiteRun(ctx, suite, target, base, scheduleID, now)
}

func (s *Store) createSuiteRun(ctx context.Context, suite Suite, target RepoRef, base, scheduleID string, now time.Time) (SuiteRun, error) {
	templates, err := resolveSuiteTemplates(ctx, s, suite.Items)
	if err != nil {
		return SuiteRun{}, err
	}
	orderKey, err := s.OrderKeyForNewTask(ctx, false)
	if err != nil {
		return SuiteRun{}, err
	}

	var runID int64
	err = s.write(ctx, fmt.Sprintf("create suite run for %s", suite.ID), func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			"INSERT INTO `suite_run` "+
				"(`suite_id`,`suite_name`,`schedule_id`,`owner`,`repo`,`base`,`mode`,`count`,`max_passes`,"+
				"`require_approval`,`auto_merge`,`auto_approve_proposals`,`status`,`created_at`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
			suite.ID, suite.Name, nullable(scheduleID), target.StoredOwner(), target.Name, base,
			string(suite.Mode), suite.Count, suite.MaxPasses,
			suite.RequireApproval, suite.AutoMerge, suite.AutoApproveProposals, string(SuiteRunActive), now.UTC())
		if err != nil {
			return fmt.Errorf("recording suite run: %w", err)
		}
		runID, err = res.LastInsertId()
		if err != nil {
			return err
		}
		for i, it := range suite.Items {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO `suite_run_item` (`run_id`,`template_id`,`order_key`) VALUES (?,?,?)",
				runID, it.TemplateID, float64(i)); err != nil {
				return err
			}
		}
		return fireSuitePass(ctx, tx, runID, suite.Items, templates, target, base,
			suite.RequireApproval, suite.AutoMerge, suite.AutoApproveProposals, 1, orderKey, now)
	})
	if err != nil {
		return SuiteRun{}, err
	}
	run, err := getSuiteRun(ctx, s.db, runID)
	if err != nil {
		return SuiteRun{}, err
	}
	return *run, nil
}

// FireNextPass instantiates run's own next pass, using the same item
// snapshot and settings CreateSuiteRun copied onto it -- what
// SyncSuites calls once OutcomeOfPass says the current pass finished
// short of stopping: SuiteCount short of Count passes, or
// SuiteUntilClean short of MaxPasses with no clean pass yet.
func (s *Store) FireNextPass(ctx context.Context, run SuiteRun, now time.Time) (SuiteRun, error) {
	templates, err := resolveSuiteTemplates(ctx, s, run.Items)
	if err != nil {
		return SuiteRun{}, err
	}
	orderKey, err := s.OrderKeyForNewTask(ctx, false)
	if err != nil {
		return SuiteRun{}, err
	}
	next := run.CurrentPass() + 1
	err = s.write(ctx, fmt.Sprintf("fire pass %d of suite run %d", next, run.ID), func(tx *sql.Tx) error {
		return fireSuitePass(ctx, tx, run.ID, run.Items, templates, run.Target, run.Base,
			run.RequireApproval, run.AutoMerge, run.AutoApproveProposals, next, orderKey, now)
	})
	if err != nil {
		return SuiteRun{}, err
	}
	updated, err := s.GetSuiteRun(ctx, run.ID)
	if err != nil {
		return SuiteRun{}, err
	}
	return *updated, nil
}

// CompleteSuiteRun marks run finished -- succeeded, with lastError
// empty, or failed, with lastError explaining why -- and stamps
// CompletedAt. SyncSuites is the only caller: once a run leaves
// SuiteRunActive it never becomes active again, so ActiveSuiteRuns
// never sees it a second time.
func (s *Store) CompleteSuiteRun(ctx context.Context, id int64, status SuiteRunStatus, lastError string, now time.Time) error {
	return s.write(ctx, fmt.Sprintf("complete suite run %d", id), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE `suite_run` SET `status` = ?, `last_error` = ?, `completed_at` = ? WHERE `id` = ?",
			string(status), nullable(lastError), now.UTC(), id)
		return err
	})
}
