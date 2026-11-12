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
	"log"
	"strconv"
	"strings"
	"time"
)

// The two wakes resume_later adds to the ones parks already had
// (design/design/durable-tasks.design.md#DT-9). A resume_later that names ci or
// message defers its own task with no wake time, or with its at
// (ui.Client.ResumeLater); what releases that deferral is the condition
// arriving -- a CI verdict (Store.DueResumeOnCI, which
// orchestrator.reconcileWake reads alongside live GitHub Actions checks),
// or a post to the task (Store.WakeOnMessage, which ui's comment path
// calls) -- or what releases any deferral: its wake time, the CI-wake
// stall safety net, or a person pressing Undefer.

// heldResume is one open requested suspension holding its task deferred:
// the deferral was made by the suspended run itself, between its start
// and its finish, so a deferral a person made since is not mistaken for
// one.
type heldResume struct {
	taskID   string
	resumeOn ResumeOn
	since    time.Time
}

// HeldCIResume describes one task currently deferred waiting on a CI wake
// (resume_later with when.ci).
type HeldCIResume struct {
	TaskID   string
	Repo     RepoRef
	Branch   string
	CIMode   CIMode
	TargetCI string
	// Head is the branch head recorded when the wait was taken
	// (ResumeOn.Head), "" when none was.
	Head  string
	Since time.Time
	// Recovered marks a hold read back from the comment resume_later
	// leaves (recoveredCIHolds) rather than from its suspension: one a
	// run made before Store.HoldResumeOn, whose finish lost it.
	Recovered bool
}

const actionsRunIDPrefix = "gha:"

// ParseGitHubActionsRunID parses "gha:<owner>:<name>:<run-id>" (or slashes
// in place of colons in the repo segment) into its repository string and
// positive GitHub Actions run ID.
func ParseGitHubActionsRunID(id string) (repo string, runID int64, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(id), actionsRunIDPrefix)
	if !found {
		return "", 0, false
	}
	at := strings.LastIndexByte(rest, ':')
	if at <= 0 {
		return "", 0, false
	}
	n, err := strconv.ParseInt(rest[at+1:], 10, 64)
	if err != nil || n <= 0 {
		return "", 0, false
	}
	repo = strings.ReplaceAll(rest[:at], ":", "/")
	if strings.Count(repo, "/") < 1 {
		return "", 0, false
	}
	return repo, n, true
}

func parseGitHubTargetRunID(defaultRepo, target string) (repo string, runID int64, ok bool) {
	if r, id, valid := ParseGitHubActionsRunID(target); valid {
		return r, id, true
	}
	n, err := strconv.ParseInt(strings.TrimSpace(target), 10, 64)
	if err != nil || n <= 0 || strings.TrimSpace(defaultRepo) == "" {
		return "", 0, false
	}
	return defaultRepo, n, true
}

func (s *Store) heldResumes(ctx context.Context, taskID string) ([]heldResume, error) {
	query := "SELECT `su`.`task_id`, `su`.`resume_on`, `t`.`deferred_at` FROM `task_suspension` AS `su` " +
		"JOIN `task` AS `t` ON `t`.`id` = `su`.`task_id` " +
		"JOIN `task_run` AS `r` ON `r`.`id` = `su`.`run_id` " +
		"WHERE `su`.`resumed_at` IS NULL AND `su`.`resume_on` IS NOT NULL AND `su`.`resume_on` <> '' " +
		"AND `r`.`finished_at` IS NOT NULL " +
		"AND `t`.`deferred_at` IS NOT NULL AND `t`.`deferred_at` >= `r`.`started_at` " +
		"AND `t`.`deferred_at` <= `su`.`created_at`"
	var args []any
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

// TaskCIMode resolves the repository, branch, and effective gating CIMode
// for task (following CIOwner for in-place review tasks). If task has no
// target repository, repo is the zero RepoRef and mode is "".
func (s *Store) TaskCIMode(ctx context.Context, task Task) (repo RepoRef, branch string, mode CIMode, err error) {
	if ownerID, inPlace := task.CIOwner(); inPlace {
		if owner, oerr := s.GetTask(ctx, ownerID); oerr == nil && owner != nil && owner.Target != nil {
			task = *owner
		}
	}
	if task.Target == nil {
		return RepoRef{}, "", "", nil
	}
	repo = *task.Target
	branch = task.BranchName()
	if IsConfigRepo(repo) {
		return repo, branch, CIModeGrain, nil
	}
	eff, err := s.EffectiveRepoConfig(ctx, repo)
	if err != nil {
		return repo, branch, "", err
	}
	if eff.CIMode != "" {
		mode = eff.CIMode
	} else {
		mode = CIModeGitHub
		for _, capName := range eff.DefaultCapabilities {
			c := strings.TrimSpace(capName)
			if c == "grain-ci" || strings.HasPrefix(c, "https://grain.dev/capabilities/ci#grain-ci") {
				mode = CIModeGrain
				break
			}
		}
	}
	if rules, rerr := s.MergeRulesOf(ctx, repo); rerr == nil && rules != nil && !rules.Empty() {
		eff := rules.Effective(nil, false, mode)
		switch {
		case !eff.Mode.GitHub() && !eff.Mode.Grain():
			mode = CIModeNone
		case mode == CIModeGitHub && !eff.Mode.GitHub() && eff.Mode.Grain():
			mode = CIModeGrain
		}
	}
	return repo, branch, mode, nil
}

// HeldCIResumes lists every task currently deferred on a resume_later CI
// wake condition along with its resolved repository, branch, and CIMode:
// the ones its suspension records, and the ones only resume_later's own
// comment still remembers (recoveredCIHolds).
//
// One task that cannot be resolved is logged and passed over rather than
// failing the list: an error here used to stop every CI wake in the
// deployment, and the stall backstop behind them, for the sake of one.
func (s *Store) HeldCIResumes(ctx context.Context) ([]HeldCIResume, error) {
	held, err := s.heldResumes(ctx, "")
	if err != nil {
		return nil, err
	}
	var out []HeldCIResume
	seen := map[string]bool{}
	for _, h := range held {
		if h.resumeOn.CI == "" || seen[h.taskID] {
			continue
		}
		r, ok, err := s.resolveCIHold(ctx, h.taskID, h.resumeOn, h.since)
		if err != nil {
			log.Printf("model: task %s's CI wake (%s) cannot be resolved, skipping it this cycle: %v",
				h.taskID, h.resumeOn.CI, err)
			continue
		}
		if ok {
			seen[h.taskID] = true
			out = append(out, r)
		}
	}
	recovered, err := s.recoveredCIHolds(ctx, seen)
	if err != nil {
		log.Printf("model: reading back CI waits lost before they were recorded: %v", err)
	}
	return append(out, recovered...), nil
}

func (s *Store) resolveCIHold(ctx context.Context, taskID string, on ResumeOn, since time.Time) (HeldCIResume, bool, error) {
	task, err := s.GetTask(ctx, taskID)
	if err != nil || task == nil {
		return HeldCIResume{}, false, err
	}
	repo, branch, mode, err := s.TaskCIMode(ctx, *task)
	if err != nil {
		return HeldCIResume{}, false, err
	}
	if repo == (RepoRef{}) {
		branch = task.BranchName()
		mode = CIModeGrain
	}
	return HeldCIResume{
		TaskID:   taskID,
		Repo:     repo,
		Branch:   branch,
		CIMode:   mode,
		TargetCI: on.CI,
		Head:     on.Head,
		Since:    since.UTC(),
	}, true, nil
}

// resumeLaterCommentPrefix opens the comment ui.Client.ResumeLater leaves
// on a task as it defers it, and resumeLaterCICondition the CI condition
// within it (ResumeOn.Describe).
const (
	resumeLaterCommentPrefix = "grain: this task's run stopped with resume_later. It goes back in the queue once "
	resumeLaterCICondition   = "CI ("
)

// recoveredCIHolds is every task still deferred, with no wake time, by a
// resume_later that waited on CI but whose condition no suspension holds
// -- skipping the tasks in seen. Those are the deferrals runs made before
// Store.HoldResumeOn, whose run then ended without its result being
// processed: the condition survives only in the comment resume_later
// left at the instant it deferred the task, which is matched on that
// instant so a deferral somebody made since is not mistaken for one.
func (s *Store) recoveredCIHolds(ctx context.Context, seen map[string]bool) ([]HeldCIResume, error) {
	type candidate struct {
		taskID string
		since  time.Time
		body   string
		at     time.Time
	}
	var cands []candidate
	err := each(ctx, s.db,
		"SELECT `t`.`id`, `t`.`deferred_at`, `c`.`body`, `c`.`created_at` FROM `task` AS `t` "+
			"JOIN `task_comment` AS `c` ON `c`.`task_id` = `t`.`id` "+
			"WHERE `t`.`deferred_at` IS NOT NULL AND `t`.`deferred_until` IS NULL "+
			"AND `c`.`body` LIKE ?",
		[]any{resumeLaterCommentPrefix + "%"},
		func(rows *sql.Rows) error {
			var c candidate
			if err := rows.Scan(&c.taskID, &c.since, &c.body, &c.at); err != nil {
				return err
			}
			cands = append(cands, c)
			return nil
		})
	if err != nil {
		return nil, err
	}
	var out []HeldCIResume
	for _, c := range cands {
		if seen[c.taskID] {
			continue
		}
		if d := c.at.Sub(c.since); d > time.Second || d < -time.Second {
			continue
		}
		target, ok := ciTargetOfResumeLaterComment(c.body)
		if !ok {
			continue
		}
		open, err := s.heldResumes(ctx, c.taskID)
		if err != nil {
			return out, err
		}
		if len(open) > 0 {
			// Something is holding it already -- a message wait, say --
			// and it is that hold's to release.
			continue
		}
		r, ok, err := s.resolveCIHold(ctx, c.taskID, ResumeOn{CI: target}, c.since)
		if err != nil {
			log.Printf("model: task %s's lost CI wake (%s) cannot be resolved: %v", c.taskID, target, err)
			continue
		}
		if ok {
			seen[c.taskID] = true
			r.Recovered = true
			out = append(out, r)
		}
	}
	return out, nil
}

// ciTargetOfResumeLaterComment reads the CI target back out of the
// comment ui.Client.ResumeLater leaves.
func ciTargetOfResumeLaterComment(body string) (string, bool) {
	rest, ok := strings.CutPrefix(body, resumeLaterCommentPrefix)
	if !ok {
		return "", false
	}
	i := strings.Index(rest, resumeLaterCICondition)
	if i < 0 {
		return "", false
	}
	rest = rest[i+len(resumeLaterCICondition):]
	j := strings.Index(rest, ") has a verdict")
	if j <= 0 {
		return "", false
	}
	return strings.TrimSpace(rest[:j]), true
}

// DueCIResume reports whether h's recorded CI verdict in the store has
// arrived: from github_ci_job on ci_mode: github repositories, and from
// ci_run on ci_mode: grain repositories.
func (s *Store) DueCIResume(ctx context.Context, h HeldCIResume) (bool, error) {
	var n int
	switch h.CIMode {
	case CIModeGitHub:
		if h.TargetCI == ResumeOnMyBranch {
			err := s.db.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM `github_ci_job` WHERE LOWER(`repo`) = LOWER(?) AND `ref` = ? "+
					"AND `conclusion` <> '' AND COALESCE(`completed_at`, `recorded_at`) > ?",
				h.Repo.String(), h.Branch, h.Since.UTC()).Scan(&n)
			return n > 0, err
		}
		runRepo, runID, ok := parseGitHubTargetRunID(h.Repo.String(), h.TargetCI)
		if !ok {
			return false, nil
		}
		err := s.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM `github_ci_job` WHERE LOWER(`repo`) = LOWER(?) "+
				"AND (`run_id` = ? OR `check_run_id` = ?) AND `conclusion` <> ''",
			runRepo, runID, runID).Scan(&n)
		return n > 0, err

	case CIModeGrain:
		if h.TargetCI == ResumeOnMyBranch {
			err := s.db.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM `ci_run` WHERE `ref` = ? AND `conclusion` <> '' AND `finished_at` > ?",
				h.Branch, h.Since.UTC()).Scan(&n)
			return n > 0, err
		}
		err := s.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM `ci_run` WHERE `id` = ? AND `conclusion` <> ''",
			h.TargetCI).Scan(&n)
		return n > 0, err

	default:
		return false, nil
	}
}

// DueResumeOnCI is every task held by a resume_later on CI whose recorded
// verdict has come: on ci_mode: github repositories, a concluded GitHub
// Actions / check verdict in github_ci_job; on ci_mode: grain repositories,
// a concluded grain CI run in ci_run.
func (s *Store) DueResumeOnCI(ctx context.Context) ([]string, error) {
	held, err := s.HeldCIResumes(ctx)
	if err != nil {
		return nil, err
	}
	var due []string
	for _, h := range held {
		ok, err := s.DueCIResume(ctx, h)
		if err != nil {
			return nil, err
		}
		if ok {
			due = append(due, h.TaskID)
		}
	}
	return due, nil
}

// HasCIRun reports whether grain's ci_run store contains a run with id.
func (s *Store) HasCIRun(ctx context.Context, id string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `ci_run` WHERE `id` = ?", id).Scan(&n)
	return n > 0, err
}

// LatestCIActivityAt returns the latest timestamp of any recorded CI
// activity for h's branch or run, or nil if none is recorded.
func (s *Store) LatestCIActivityAt(ctx context.Context, h HeldCIResume) (*time.Time, error) {
	switch h.CIMode {
	case CIModeGitHub:
		var started, completed sql.NullTime
		var recorded, updated time.Time
		var row *sql.Row
		if h.TargetCI == ResumeOnMyBranch {
			row = s.db.QueryRowContext(ctx,
				"SELECT `started_at`, `completed_at`, `recorded_at`, `updated_at` FROM `github_ci_job` "+
					"WHERE LOWER(`repo`) = LOWER(?) AND `ref` = ? "+
					"ORDER BY COALESCE(`completed_at`, `updated_at`, `recorded_at`) DESC LIMIT 1",
				h.Repo.String(), h.Branch)
		} else if runRepo, runID, ok := parseGitHubTargetRunID(h.Repo.String(), h.TargetCI); ok {
			row = s.db.QueryRowContext(ctx,
				"SELECT `started_at`, `completed_at`, `recorded_at`, `updated_at` FROM `github_ci_job` "+
					"WHERE LOWER(`repo`) = LOWER(?) AND (`run_id` = ? OR `check_run_id` = ?) "+
					"ORDER BY COALESCE(`completed_at`, `updated_at`, `recorded_at`) DESC LIMIT 1",
				runRepo, runID, runID)
		} else {
			return nil, nil
		}
		if err := row.Scan(&started, &completed, &recorded, &updated); errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
		best := recorded.UTC()
		if updated.After(best) {
			best = updated.UTC()
		}
		if started.Valid && started.Time.After(best) {
			best = started.Time.UTC()
		}
		if completed.Valid && completed.Time.After(best) {
			best = completed.Time.UTC()
		}
		return &best, nil

	case CIModeGrain:
		var started, finished sql.NullTime
		var row *sql.Row
		if h.TargetCI == ResumeOnMyBranch {
			row = s.db.QueryRowContext(ctx,
				"SELECT `started_at`, `finished_at` FROM `ci_run` WHERE `ref` = ? "+
					"ORDER BY COALESCE(`finished_at`, `started_at`) DESC LIMIT 1",
				h.Branch)
		} else {
			row = s.db.QueryRowContext(ctx,
				"SELECT `started_at`, `finished_at` FROM `ci_run` WHERE `id` = ? LIMIT 1",
				h.TargetCI)
		}
		if err := row.Scan(&started, &finished); errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
		var best time.Time
		if started.Valid {
			best = started.Time.UTC()
		}
		if finished.Valid && finished.Time.After(best) {
			best = finished.Time.UTC()
		}
		if best.IsZero() {
			return nil, nil
		}
		return &best, nil

	default:
		return nil, nil
	}
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
