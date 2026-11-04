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

// GetQualificationPlan reads repo's qualification plan, or nil (with no
// error) if nothing has configured one yet.
func (s *Store) GetQualificationPlan(ctx context.Context, repo RepoRef) (*QualificationPlan, error) {
	return getQualificationPlan(ctx, s.db, repo)
}

func getQualificationPlan(ctx context.Context, q querier, repo RepoRef) (*QualificationPlan, error) {
	var requireApproval, autoPromote bool
	err := q.QueryRowContext(ctx,
		"SELECT `require_approval`,`auto_promote` FROM `qualification_config` WHERE `owner` = ? AND `name` = ?",
		repo.Owner, repo.Name).Scan(&requireApproval, &autoPromote)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading qualification config for %s: %w", repo, err)
	}
	items, err := qualificationItemsOf(ctx, q, repo)
	if err != nil {
		return nil, err
	}
	return &QualificationPlan{
		Repo: repo, Configured: true,
		RequireApproval: requireApproval, AutoPromote: autoPromote,
		Items: items,
	}, nil
}

func qualificationItemsOf(ctx context.Context, q querier, repo RepoRef) ([]QualificationItem, error) {
	var out []QualificationItem
	err := each(ctx, q,
		"SELECT `template_id`,`repeat_count` FROM `qualification_item` "+
			"WHERE `owner` = ? AND `name` = ? ORDER BY `order_key`",
		[]any{repo.Owner, repo.Name},
		func(rows *sql.Rows) error {
			var it QualificationItem
			if err := rows.Scan(&it.TemplateID, &it.Repeat); err != nil {
				return err
			}
			out = append(out, it)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading qualification items for %s: %w", repo, err)
	}
	for i := range out {
		deps, err := qualificationItemDependsOnOf(ctx, q, repo, out[i].TemplateID)
		if err != nil {
			return nil, err
		}
		out[i].DependsOn = deps
	}
	return out, nil
}

func qualificationItemDependsOnOf(ctx context.Context, q querier, repo RepoRef, templateID string) ([]string, error) {
	var deps []string
	err := each(ctx, q,
		"SELECT `depends_on_template_id` FROM `qualification_item_depends_on` "+
			"WHERE `owner` = ? AND `name` = ? AND `template_id` = ? ORDER BY `depends_on_template_id`",
		[]any{repo.Owner, repo.Name, templateID},
		func(rows *sql.Rows) error {
			var dep string
			if err := rows.Scan(&dep); err != nil {
				return err
			}
			deps = append(deps, dep)
			return nil
		})
	return deps, err
}

// PutQualificationPlan replaces repo's qualification plan wholesale --
// one row per repo, replaced rather than diffed, and PutTask's own
// "child rows are deleted and re-inserted rather than diffed" for the
// items themselves. Callers are
// expected to have already run plan.Validate and confirmed every
// TemplateID still names a real template (ui.Client.PutQualificationPlan
// does both) -- this trusts the plan it is given rather than checking it
// twice.
func (s *Store) PutQualificationPlan(ctx context.Context, plan QualificationPlan) error {
	return s.write(ctx, "put qualification plan for "+plan.Repo.String(), func(tx *sql.Tx) error {
		return putQualificationPlan(ctx, tx, plan)
	})
}

func putQualificationPlan(ctx context.Context, tx *sql.Tx, plan QualificationPlan) error {
	owner, name := plan.Repo.Owner, plan.Repo.Name
	if _, err := tx.ExecContext(ctx,
		"REPLACE INTO `qualification_config` (`owner`,`name`,`require_approval`,`auto_promote`) VALUES (?,?,?,?)",
		owner, name, plan.RequireApproval, plan.AutoPromote); err != nil {
		return fmt.Errorf("writing qualification config for %s: %w", plan.Repo, err)
	}

	for _, table := range []string{"qualification_item_depends_on", "qualification_item"} {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM `"+table+"` WHERE `owner` = ? AND `name` = ?", owner, name); err != nil {
			return err
		}
	}

	for i, it := range plan.Items {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `qualification_item` (`owner`,`name`,`template_id`,`repeat_count`,`order_key`) VALUES (?,?,?,?,?)",
			owner, name, it.TemplateID, it.Repeat, float64(i)); err != nil {
			return fmt.Errorf("writing qualification item %q: %w", it.TemplateID, err)
		}
		for _, dep := range it.DependsOn {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO `qualification_item_depends_on` (`owner`,`name`,`template_id`,`depends_on_template_id`) VALUES (?,?,?,?)",
				owner, name, it.TemplateID, dep); err != nil {
				return err
			}
		}
	}
	return nil
}

// QualificationPlansUsingTemplate returns every repo whose
// qualification plan has an item referencing templateID -- what
// ui.Client.DeleteTemplate checks before deleting one out from under a
// plan that still schedules from it, SchedulesUsingTemplate's own
// reasoning applied to this second caller of Template.
func (s *Store) QualificationPlansUsingTemplate(ctx context.Context, templateID string) ([]RepoRef, error) {
	var out []RepoRef
	err := each(ctx, s.db,
		"SELECT DISTINCT `owner`,`name` FROM `qualification_item` WHERE `template_id` = ? ORDER BY `owner`,`name`",
		templateID, func(rows *sql.Rows) error {
			var r RepoRef
			if err := rows.Scan(&r.Owner, &r.Name); err != nil {
				return err
			}
			out = append(out, r)
			return nil
		})
	return out, err
}

const qualificationRunColumns = "`id`,`owner`,`name`,`candidate_id`,`created_at`"

// CandidateQualificationRun returns candidateID's own qualification run,
// fully hydrated with every task instance's current progress, or nil if
// none has been created for it yet.
func (s *Store) CandidateQualificationRun(ctx context.Context, candidateID int64) (*QualificationRun, error) {
	return candidateQualificationRun(ctx, s.db, candidateID)
}

func candidateQualificationRun(ctx context.Context, q querier, candidateID int64) (*QualificationRun, error) {
	var run QualificationRun
	err := q.QueryRowContext(ctx,
		"SELECT "+qualificationRunColumns+" FROM `qualification_run` WHERE `candidate_id` = ?", candidateID,
	).Scan(&run.ID, &run.Repo.Owner, &run.Repo.Name, &run.CandidateID, &run.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading qualification run for candidate %d: %w", candidateID, err)
	}
	tasks, err := qualificationTaskStatusesOf(ctx, q, run.ID)
	if err != nil {
		return nil, err
	}
	run.Tasks = tasks
	return &run, nil
}

// qualificationTaskStatusesOf reads every task instance a run
// instantiated, oldest first (qualification_task's own insertion order,
// which is also CreateQualificationRun's own dependency-respecting
// order), joined against the task each names for its current approval
// and state.
func qualificationTaskStatusesOf(ctx context.Context, q querier, runID int64) ([]QualificationTaskStatus, error) {
	var out []QualificationTaskStatus
	err := each(ctx, q,
		"SELECT `qt`.`task_id`,`qt`.`template_id`,`qt`.`template_name`,`qt`.`instance_index`,`qt`.`repeat_count`,"+
			"(`t`.`approval_actor_kind` IS NOT NULL),`ts`.`state` "+
			"FROM `qualification_task` AS `qt` "+
			"JOIN `task` AS `t` ON `t`.`id` = `qt`.`task_id` "+
			"JOIN `task_state` AS `ts` ON `ts`.`task_id` = `qt`.`task_id` "+
			"WHERE `qt`.`run_id` = ? ORDER BY `qt`.`id`",
		runID, func(rows *sql.Rows) error {
			var st QualificationTaskStatus
			var state string
			if err := rows.Scan(&st.TaskID, &st.TemplateID, &st.TemplateName,
				&st.InstanceIndex, &st.Repeat, &st.Approved, &state); err != nil {
				return err
			}
			st.State = State(state)
			out = append(out, st)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading qualification tasks for run %d: %w", runID, err)
	}
	return out, nil
}

// QualifiableActiveCandidates returns every active candidate whose repo
// either has a qualification plan with at least one item (so it needs a
// run created) or already has one (so that run's own progress needs
// checking, for a plan whose AutoPromote might now apply) -- what the
// qualifications reconciler polls every cycle, PendingCandidates' own
// "every repo, oldest candidate first" shape applied to a different
// status.
func (s *Store) QualifiableActiveCandidates(ctx context.Context) ([]Candidate, error) {
	var out []Candidate
	err := each(ctx, s.db,
		"SELECT "+candidateColumns+" FROM `release_candidate` WHERE `status` = ? AND ("+
			"EXISTS (SELECT 1 FROM `qualification_item` WHERE `qualification_item`.`owner` = `release_candidate`.`owner` "+
			"AND `qualification_item`.`name` = `release_candidate`.`repo`) "+
			"OR EXISTS (SELECT 1 FROM `qualification_run` WHERE `qualification_run`.`candidate_id` = `release_candidate`.`id`)"+
			") ORDER BY `id`",
		[]any{string(CandidateActive)},
		func(rows *sql.Rows) error {
			c, err := scanCandidate(rows.Scan)
			if err != nil {
				return err
			}
			out = append(out, c)
			return nil
		})
	return out, err
}

// CreateQualificationRun instantiates plan's items against candidate as
// real tasks -- the issue's own "schedule these tasks and run them
// through against the rc". Every item's Template is resolved fresh from
// the store here, not from whatever content a UI had in hand when the
// plan was last saved (fireTaskSchedule's own "not a stale copy"
// discipline for bwsalmon/agents#516, applied again): a template edited
// since is what actually gets filed, and a template deleted out from
// under a plan (ui.Client.DeleteTemplate tries to prevent this, but
// nothing stops a row from disappearing some other way) fails this
// whole run's creation with a plain error, retried next cycle exactly
// like any other store error reconcileQualifications already tolerates.
//
// Every instance's Target is candidate.Repo and Base is
// candidate.Branch, never a template's own binding: the entire point of
// a qualification task is running against a branch that did not exist
// until this candidate was cut, which only the plan's own repo and this
// candidate's own branch can ever say. A template bound to some other
// repo (model.Template.Target) therefore cannot be qualified here at
// all, and fails the whole run's creation rather than being quietly
// aimed somewhere its binding says it does not belong --
// ui.Client.PutQualificationPlan already refuses to save such a plan,
// so this is the backstop for a template bound after the fact.
//
// A dependency between two items becomes a depends-on link from every
// instance of the dependent to every instance of the dependency, so the
// whole graph blocks the same way a hand-authored one would -- an item
// with Repeat 3 depending on one with Repeat 2 waits on both of the
// latter's instances before any of its own three start.
//
// Approval is unconditional at creation, never a question this call
// itself answers -- RequireApproval false lands every instance already
// approved (fireTaskSchedule's own "the schedule is itself the standing
// approval," here read off the plan instead), and true leaves every
// instance unapproved for ApproveQualificationRun to act on later as one
// bulk decision, the issue's own "require the user to approve submitting
// all of them."
//
// qualification_run's own unique index on candidate_id is the backstop
// against creating two runs for the same candidate: the caller
// (reconcileQualifications) only calls this once CandidateQualificationRun
// has already read nil, but that check and this write are not one
// transaction, so a concurrent daemon racing the same candidate fails
// here outright rather than silently doubling every task.
func (s *Store) CreateQualificationRun(ctx context.Context, candidate Candidate, plan QualificationPlan, now time.Time) (QualificationRun, error) {
	ordered, err := qualificationTopoOrder(plan.Items)
	if err != nil {
		return QualificationRun{}, fmt.Errorf("qualification plan for %s: %w", candidate.Repo, err)
	}

	templates := make(map[string]Template, len(ordered))
	for _, it := range ordered {
		tmpl, err := s.GetTemplate(ctx, it.TemplateID)
		if err != nil {
			return QualificationRun{}, fmt.Errorf("resolving template %s: %w", it.TemplateID, err)
		}
		if tmpl == nil {
			return QualificationRun{}, fmt.Errorf("template %s no longer exists", it.TemplateID)
		}
		if tmpl.Target != nil && *tmpl.Target != candidate.Repo {
			return QualificationRun{}, fmt.Errorf(
				"template %s is bound to %s, which is not the repo being qualified (%s)",
				it.TemplateID, tmpl.Target, candidate.Repo)
		}
		templates[it.TemplateID] = *tmpl
	}

	var orderKey float64
	if len(ordered) > 0 {
		orderKey, err = s.OrderKeyForNewTask(ctx, false)
		if err != nil {
			return QualificationRun{}, fmt.Errorf("resolving order key: %w", err)
		}
	}

	var runID int64
	err = s.write(ctx, fmt.Sprintf("create qualification run for candidate %d", candidate.ID), func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			"INSERT INTO `qualification_run` (`owner`,`name`,`candidate_id`,`created_at`) VALUES (?,?,?,?)",
			candidate.Repo.Owner, candidate.Repo.Name, candidate.ID, now.UTC())
		if err != nil {
			return fmt.Errorf("recording qualification run: %w", err)
		}
		runID, err = res.LastInsertId()
		if err != nil {
			return err
		}

		instances := make(map[string][]string, len(ordered))
		// Read at most once for the whole run, and not at all for a plan
		// whose templates say nothing about any declared field --
		// fireSuitePass' own use of it.
		declarations := customFieldDeclarations{q: tx}
		for _, it := range ordered {
			tmpl := templates[it.TemplateID]
			// What each instance carries, by compose.go's own algorithm
			// -- the template's content, pinned to the candidate's repo
			// and branch, landing the way the plan and the template say
			// (ComposeQualificationTask).
			comp := ComposeQualificationTask(tmpl, candidate.Repo, candidate.Branch, plan.RequireApproval)
			customFields, err := declarations.apply(ctx, comp.CustomFields)
			if err != nil {
				return err
			}
			comp.CustomFields = customFields
			if comp.Target != nil {
				usesGrainCI, err := querierRepoUsesGrainCI(ctx, tx, *comp.Target)
				if err != nil {
					return err
				}
				applyGrainCIGrant(&comp, usesGrainCI)
			}

			var links []Link
			for _, dep := range it.DependsOn {
				for _, depID := range instances[dep] {
					links = append(links, Link{Kind: LinkDependsOn, Target: depID})
				}
			}

			ids := make([]string, it.Repeat)
			for i := 0; i < it.Repeat; i++ {
				id, err := newTaskID(ctx, tx)
				if err != nil {
					return err
				}
				ids[i] = id

				task := comp.Task(id, QualificationPrincipal, ReasonQualification, now)
				// The one thing a repeated item's instances do not share:
				// which of the repeats they are, said in the title so a
				// task list can tell them apart.
				if it.Repeat > 1 {
					task.Title = fmt.Sprintf("%s (%d/%d)", comp.Title, i+1, it.Repeat)
				}
				task.Links = links
				task.OrderKey = orderKey
				orderKey += orderKeySpacing
				if err := putTask(ctx, tx, task); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx,
					"INSERT INTO `qualification_task` "+
						"(`run_id`,`task_id`,`template_id`,`template_name`,`instance_index`,`repeat_count`) "+
						"VALUES (?,?,?,?,?,?)",
					runID, id, it.TemplateID, tmpl.Name, i+1, it.Repeat); err != nil {
					return err
				}
			}
			instances[it.TemplateID] = ids
		}
		return nil
	})
	if err != nil {
		return QualificationRun{}, err
	}

	tasks, err := qualificationTaskStatusesOf(ctx, s.db, runID)
	if err != nil {
		return QualificationRun{}, err
	}
	return QualificationRun{ID: runID, CandidateID: candidate.ID, Repo: candidate.Repo, CreatedAt: now, Tasks: tasks}, nil
}

// ApproveQualificationRun approves every task instance in run as one
// action -- the issue's own "require the user to approve submitting all
// of them," exercised in bulk rather than one approval per instance. An
// instance already approved (only possible when the plan's
// RequireApproval was false at creation) is simply re-stamped with the
// same actor, which is harmless.
func (s *Store) ApproveQualificationRun(ctx context.Context, runID int64, a Attribution, approvedAt time.Time) error {
	return s.write(ctx, fmt.Sprintf("approve qualification run %d", runID), func(tx *sql.Tx) error {
		var taskIDs []string
		if err := each(ctx, tx, "SELECT `task_id` FROM `qualification_task` WHERE `run_id` = ?", runID,
			func(rows *sql.Rows) error {
				var id string
				if err := rows.Scan(&id); err != nil {
					return err
				}
				taskIDs = append(taskIDs, id)
				return nil
			}); err != nil {
			return err
		}
		for _, id := range taskIDs {
			if err := approve(ctx, tx, id, a, approvedAt); err != nil {
				return err
			}
		}
		return nil
	})
}
