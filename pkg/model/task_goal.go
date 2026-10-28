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

// Goals: a task that ends when grain sees its goal met rather than when
// its agent stops (docs/design/durable-tasks.md#DT-4).
//
// A goal is declared on the task -- by its template (Template.Goal), by
// the propose_task call that filed it, by a person on the task page, or
// by grain itself for every base-branch repair -- and checked by the
// daemon (orchestrator.reconcileTaskGoals). A run ending does not
// complete a task with an unmet goal: it rests on the goal instead.
// Progress is accounted per finished run, and a goal that stops
// progressing raises task.no-progress:<task> and holds or backs off
// (GoalOnLimit).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// GoalKind is what a goal waits for.
type GoalKind string

const (
	// GoalBaseGreen is met when a base branch's grain CI is green at its
	// tip (and, with Publishing, its release has published since).
	GoalBaseGreen GoalKind = "base-green"
	// GoalPRMerged is met when a pull request merged.
	GoalPRMerged GoalKind = "pr-merged"
	// GoalTasksClosed is met when every named task closed (or merged).
	GoalTasksClosed GoalKind = "tasks-closed"
	// GoalPersonCloses is never met by the daemon: only a person closing
	// the task ends it.
	GoalPersonCloses GoalKind = "person-closes"
	// GoalPipelineHealthy is met when the pipeline steward would be quiet
	// and no pipeline.* notification is open.
	GoalPipelineHealthy GoalKind = "pipeline-healthy"
)

// GoalKinds is every GoalKind, in the design's order.
func GoalKinds() []GoalKind {
	return []GoalKind{GoalBaseGreen, GoalPRMerged, GoalTasksClosed, GoalPersonCloses, GoalPipelineHealthy}
}

// Valid reports whether k is a declared kind.
func (k GoalKind) Valid() bool { return slices.Contains(GoalKinds(), k) }

// GoalOnMet is what a met goal does to its task.
type GoalOnMet string

const (
	// GoalCloseOnMet closes the task with outcome GoalMetOutcome.
	GoalCloseOnMet GoalOnMet = "close"
	// GoalRestOnMet puts the task to rest: it stays open, deferred with
	// no wake time, until a person or a message moves it.
	GoalRestOnMet GoalOnMet = "rest"
)

// GoalOnLimit is what a goal that stopped progressing does.
type GoalOnLimit string

const (
	// GoalHoldOnLimit stops resuming: the task rests on its
	// task.no-progress notification until a person acts on it.
	GoalHoldOnLimit GoalOnLimit = "hold"
	// GoalNotifyOnLimit raises the notification and keeps going, backing
	// off exponentially (GoalLimitBackoff).
	GoalNotifyOnLimit GoalOnLimit = "notify"
)

// GoalMetOutcome is the outcome a task closed by its goal is recorded
// under: on its goal row (TaskGoal.Outcome) and in the closing note.
const GoalMetOutcome = "goal-met"

// FamilyNoProgress is the notification family a goal at its progress
// limit raises, one subject per task (NotificationSubject).
const FamilyNoProgress = "task.no-progress"

// MaxGoalBackoff caps GoalNotifyOnLimit's exponential backoff.
const MaxGoalBackoff = 6 * time.Hour

// GoalSpec is a goal's per-kind parameters (task_goal.spec). Only the
// fields its kind reads are kept (Normalize).
type GoalSpec struct {
	// base-green
	Repo       string `json:"repo,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Publishing bool   `json:"publishing,omitempty"`
	// pr-merged: a task's own pull request, or Repo and Number.
	Task   string `json:"task,omitempty"`
	Number int    `json:"number,omitempty"`
	// tasks-closed
	Tasks []string `json:"tasks,omitempty"`
	Until string   `json:"until,omitempty"`
	// pipeline-healthy
	Repos []string `json:"repos,omitempty"`
}

// GoalDecl is a goal as it is declared -- by a template, propose_task, a
// person or grain -- before it is a row with progress on it. A zero
// MaxResumes, MaxStall or OnLimit is its kind's default (GoalDefaults).
type GoalDecl struct {
	Kind       GoalKind    `json:"kind"`
	Spec       GoalSpec    `json:"spec"`
	OnMet      GoalOnMet   `json:"on_met,omitempty"`
	OnLimit    GoalOnLimit `json:"on_limit,omitempty"`
	MaxResumes int         `json:"max_resumes,omitempty"`
	// MaxStall is a duration in Go's syntax ("12h"), "" for the default.
	MaxStall string `json:"max_stall,omitempty"`
}

// GoalDefaults is kind's progress limit: how many resumes without
// progress, and how long without progress, before the limit is reached,
// and what reaching it does. Zero for both is no limit.
func GoalDefaults(kind GoalKind) (maxResumes int, maxStall time.Duration, onLimit GoalOnLimit) {
	switch kind {
	case GoalBaseGreen:
		return 6, 12 * time.Hour, GoalHoldOnLimit
	case GoalPRMerged, GoalTasksClosed:
		return 6, 24 * time.Hour, GoalHoldOnLimit
	case GoalPipelineHealthy:
		return 4, 6 * time.Hour, GoalNotifyOnLimit
	}
	// person-closes: a session nobody is talking to does not resume.
	return 0, 0, GoalHoldOnLimit
}

// Normalize checks d and fills its defaults, keeping only the spec
// fields its kind reads.
func (d *GoalDecl) Normalize() error {
	d.Kind = GoalKind(strings.TrimSpace(string(d.Kind)))
	if !d.Kind.Valid() {
		return fmt.Errorf("goal kind %q: want one of %s", d.Kind, goalKindList())
	}
	s := d.Spec
	var spec GoalSpec
	switch d.Kind {
	case GoalBaseGreen:
		repo, err := ParseRepo(strings.TrimSpace(s.Repo))
		if err != nil {
			return fmt.Errorf("goal base-green: %w", err)
		}
		spec.Repo, spec.Branch, spec.Publishing = repo.String(), strings.TrimSpace(s.Branch), s.Publishing
		if spec.Branch == "" {
			return errors.New("goal base-green needs a branch")
		}
	case GoalPRMerged:
		switch {
		case strings.TrimSpace(s.Task) != "":
			spec.Task = NormalizeTaskID(s.Task)
		case s.Repo != "" && s.Number > 0:
			repo, err := ParseRepo(strings.TrimSpace(s.Repo))
			if err != nil {
				return fmt.Errorf("goal pr-merged: %w", err)
			}
			spec.Repo, spec.Number = repo.String(), s.Number
		default:
			return errors.New("goal pr-merged needs a task, or a repo and a number")
		}
	case GoalTasksClosed:
		for _, t := range s.Tasks {
			if t = strings.TrimSpace(t); t != "" {
				spec.Tasks = append(spec.Tasks, NormalizeTaskID(t))
			}
		}
		if len(spec.Tasks) == 0 {
			return errors.New("goal tasks-closed needs at least one task")
		}
		switch s.Until {
		case "", "closed":
			spec.Until = "closed"
		case "merged":
			spec.Until = "merged"
		default:
			return fmt.Errorf("goal tasks-closed until %q: want closed or merged", s.Until)
		}
	case GoalPipelineHealthy:
		for _, r := range s.Repos {
			repo, err := ParseRepo(strings.TrimSpace(r))
			if err != nil {
				return fmt.Errorf("goal pipeline-healthy: %w", err)
			}
			spec.Repos = append(spec.Repos, repo.String())
		}
	}
	d.Spec = spec
	switch d.OnMet {
	case "":
		d.OnMet = GoalCloseOnMet
		if d.Kind == GoalPipelineHealthy {
			d.OnMet = GoalRestOnMet
		}
	case GoalCloseOnMet, GoalRestOnMet:
	default:
		return fmt.Errorf("goal on_met %q: want close or rest", d.OnMet)
	}
	defResumes, defStall, defLimit := GoalDefaults(d.Kind)
	switch d.OnLimit {
	case "":
		d.OnLimit = defLimit
	case GoalHoldOnLimit, GoalNotifyOnLimit:
	default:
		return fmt.Errorf("goal on_limit %q: want hold or notify", d.OnLimit)
	}
	if d.MaxResumes < 0 {
		return errors.New("goal max_resumes cannot be negative")
	}
	if d.MaxResumes == 0 {
		d.MaxResumes = defResumes
	}
	if strings.TrimSpace(d.MaxStall) == "" {
		if defStall > 0 {
			d.MaxStall = defStall.String()
		}
	} else if stall, err := time.ParseDuration(strings.TrimSpace(d.MaxStall)); err != nil || stall < 0 {
		return fmt.Errorf("goal max_stall %q: want a duration such as 12h", d.MaxStall)
	} else {
		d.MaxStall = stall.String()
	}
	return nil
}

func goalKindList() string {
	var out []string
	for _, k := range GoalKinds() {
		out = append(out, string(k))
	}
	return strings.Join(out, ", ")
}

// ParseGoalDecl reads a declaration out of JSON -- a template's goal
// column, a propose_task argument, a request body -- and normalizes it.
// "" is no goal (nil).
func ParseGoalDecl(raw string) (*GoalDecl, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var d GoalDecl
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return nil, fmt.Errorf("reading goal: %w", err)
	}
	if err := d.Normalize(); err != nil {
		return nil, err
	}
	return &d, nil
}

// JSON is d as a template's goal column stores it, "" for nil.
func (d *GoalDecl) JSON() string {
	if d == nil {
		return ""
	}
	b, err := json.Marshal(d)
	if err != nil {
		return ""
	}
	return string(b)
}

// TaskGoal is one task_goal row: a task's goal and the progress grain
// has seen towards it.
type TaskGoal struct {
	TaskID     string      `json:"taskId"`
	Kind       GoalKind    `json:"kind"`
	Spec       GoalSpec    `json:"spec"`
	OnMet      GoalOnMet   `json:"onMet"`
	OnLimit    GoalOnLimit `json:"onLimit"`
	DeclaredBy string      `json:"declaredBy"`
	DeclaredAt time.Time   `json:"declaredAt"`
	// MetAt is when the goal was last seen met; nil while it is not.
	MetAt *time.Time `json:"metAt,omitempty"`
	// Evidence is what the check saw when it was last met ("main green
	// at abc123"), or, while it is unmet, what it sees instead.
	Evidence string `json:"evidence,omitempty"`
	// Outcome is GoalMetOutcome once the goal has closed its task.
	Outcome string `json:"outcome,omitempty"`
	// Measure is the goal's own measure as the last check read it: what
	// "the goal's measure moved" is compared against.
	Measure              string        `json:"measure,omitempty"`
	ProgressAt           time.Time     `json:"progressAt"`
	ResumesSinceProgress int           `json:"resumesSinceProgress"`
	MaxResumes           int           `json:"maxResumes"`
	MaxStall             time.Duration `json:"maxStall"`
	LimitNotifiedAt      *time.Time    `json:"limitNotifiedAt,omitempty"`
	// CountedRunID is the last finished run whose progress has been
	// accounted, so each run is counted once.
	CountedRunID string `json:"countedRunId,omitempty"`
}

// Decl is g's declaration.
func (g TaskGoal) Decl() GoalDecl {
	d := GoalDecl{Kind: g.Kind, Spec: g.Spec, OnMet: g.OnMet, OnLimit: g.OnLimit, MaxResumes: g.MaxResumes}
	if g.MaxStall > 0 {
		d.MaxStall = g.MaxStall.String()
	}
	return d
}

// AtLimit reports whether g has gone too long without progress at now.
func (g TaskGoal) AtLimit(now time.Time) bool {
	if g.MaxResumes > 0 && g.ResumesSinceProgress >= g.MaxResumes {
		return true
	}
	return g.MaxStall > 0 && now.Sub(g.ProgressAt) > g.MaxStall
}

// GoalLimitBackoff is how long a GoalNotifyOnLimit goal waits before its
// next resume once it is past its limit: doubling from base for every
// resume past the limit, capped at MaxGoalBackoff.
func GoalLimitBackoff(g TaskGoal, base time.Duration) time.Duration {
	if base <= 0 {
		base = 15 * time.Minute
	}
	past := g.ResumesSinceProgress - g.MaxResumes
	if past < 0 {
		past = 0
	}
	d := base
	for i := 0; i < past && d < MaxGoalBackoff; i++ {
		d *= 2
	}
	return min(d, MaxGoalBackoff)
}

const taskGoalColumns = "`task_id`,`kind`,`spec`,`on_met`,`declared_by`,`declared_at`,`met_at`,`evidence`," +
	"`outcome`,`measure`,`progress_at`,`resumes_since_progress`,`max_resumes`,`max_stall`,`on_limit`," +
	"`limit_notified_at`,`counted_run_id`"

func scanTaskGoal(scan func(...any) error) (TaskGoal, error) {
	var g TaskGoal
	var kind, spec, onMet, onLimit string
	var metAt, notified sql.NullTime
	var stall int64
	if err := scan(&g.TaskID, &kind, &spec, &onMet, &g.DeclaredBy, &g.DeclaredAt, &metAt, &g.Evidence,
		&g.Outcome, &g.Measure, &g.ProgressAt, &g.ResumesSinceProgress, &g.MaxResumes, &stall, &onLimit,
		&notified, &g.CountedRunID); err != nil {
		return TaskGoal{}, err
	}
	g.Kind, g.OnMet, g.OnLimit = GoalKind(kind), GoalOnMet(onMet), GoalOnLimit(onLimit)
	g.MaxStall = time.Duration(stall) * time.Second
	g.MetAt, g.LimitNotifiedAt = timePtr(metAt), timePtr(notified)
	if err := json.Unmarshal([]byte(spec), &g.Spec); err != nil {
		return TaskGoal{}, fmt.Errorf("reading task %s's goal spec: %w", g.TaskID, err)
	}
	return g, nil
}

// SetTaskGoal declares taskID's goal, replacing any it had: the
// progress counters start again from now, and by is who declared it
// (PrincipalString).
func (s *Store) SetTaskGoal(ctx context.Context, taskID string, d GoalDecl, by string, now time.Time) error {
	if err := d.Normalize(); err != nil {
		return err
	}
	stall, _ := time.ParseDuration(d.MaxStall)
	spec, err := json.Marshal(d.Spec)
	if err != nil {
		return err
	}
	return s.write(ctx, "set task "+taskID+"'s goal", func(tx *sql.Tx) error {
		return setTaskGoalTx(ctx, tx, taskID, d, string(spec), stall, by, now)
	})
}

func setTaskGoalTx(ctx context.Context, tx *sql.Tx, taskID string, d GoalDecl, spec string,
	stall time.Duration, by string, now time.Time) error {

	if _, err := tx.ExecContext(ctx, "DELETE FROM `task_goal` WHERE `task_id` = ?", taskID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO `task_goal` ("+taskGoalColumns+") "+
		"VALUES (?,?,?,?,?,?,NULL,'','','',?,0,?,?,?,NULL,'')",
		taskID, string(d.Kind), spec, string(d.OnMet), by, now.UTC(), now.UTC(),
		d.MaxResumes, int64(stall/time.Second), string(d.OnLimit))
	return err
}

// goalFromTemplateTx declares a new task's goal from the template it was
// filed from (Template.Goal), inside putTask's transaction. A template
// with no goal, or one this build cannot read, declares nothing.
func goalFromTemplateTx(ctx context.Context, tx *sql.Tx, t Task) error {
	var raw string
	err := tx.QueryRowContext(ctx, "SELECT `goal` FROM `template` WHERE `id` = ?", t.TemplateID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading template %s's goal: %w", t.TemplateID, err)
	}
	d, err := ParseGoalDecl(raw)
	if err != nil || d == nil {
		return nil
	}
	spec, err := json.Marshal(d.Spec)
	if err != nil {
		return err
	}
	stall, _ := time.ParseDuration(d.MaxStall)
	at := time.Now()
	if t.CreatedAt != nil {
		at = *t.CreatedAt
	}
	return setTaskGoalTx(ctx, tx, t.ID, *d, string(spec), stall, "template:"+t.TemplateID, at)
}

// ClearTaskGoal removes taskID's goal, if it has one.
func (s *Store) ClearTaskGoal(ctx context.Context, taskID string) error {
	return s.write(ctx, "clear task "+taskID+"'s goal", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM `task_goal` WHERE `task_id` = ?", taskID)
		return err
	})
}

// TaskGoal is taskID's goal, or nil for a task with none.
func (s *Store) TaskGoal(ctx context.Context, taskID string) (*TaskGoal, error) {
	g, err := scanTaskGoal(s.db.QueryRowContext(ctx,
		"SELECT "+taskGoalColumns+" FROM `task_goal` WHERE `task_id` = ?", taskID).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading task %s's goal: %w", taskID, err)
	}
	return &g, nil
}

// OpenTaskGoals is the goal of every task not yet closed, by task id.
func (s *Store) OpenTaskGoals(ctx context.Context) ([]TaskGoal, error) {
	var out []TaskGoal
	err := each(ctx, s.db, "SELECT "+prefixed("g", taskGoalColumns)+" FROM `task_goal` AS `g` "+
		"LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `g`.`task_id` "+
		"WHERE `o`.`closed_at` IS NULL ORDER BY `g`.`task_id`", nil,
		func(rows *sql.Rows) error {
			g, err := scanTaskGoal(rows.Scan)
			if err != nil {
				return err
			}
			out = append(out, g)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing open goals: %w", err)
	}
	return out, nil
}

// prefixed qualifies every column of a column list with alias.
func prefixed(alias, columns string) string {
	parts := strings.Split(columns, ",")
	for i, p := range parts {
		parts[i] = "`" + alias + "`." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ",")
}

// SaveTaskGoalProgress writes back what a goal check changed: when it
// was met and on what evidence, its measure, its progress counters and
// limit notice. The declaration is untouched (SetTaskGoal).
func (s *Store) SaveTaskGoalProgress(ctx context.Context, g TaskGoal) error {
	return s.write(ctx, "record task "+g.TaskID+"'s goal progress", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE `task_goal` SET `met_at` = ?, `evidence` = ?, `outcome` = ?, "+
			"`measure` = ?, `progress_at` = ?, `resumes_since_progress` = ?, `limit_notified_at` = ?, "+
			"`counted_run_id` = ? WHERE `task_id` = ?",
			timeOf(g.MetAt), g.Evidence, g.Outcome, g.Measure, g.ProgressAt.UTC(), g.ResumesSinceProgress,
			timeOf(g.LimitNotifiedAt), g.CountedRunID, g.TaskID)
		return err
	})
}

// NoteGoalProgress resets taskID's progress counters at now, if it has
// a goal: a person acted on it (Continue, Reply, a new model), which the
// design counts as progress.
func (s *Store) NoteGoalProgress(ctx context.Context, taskID string, now time.Time) error {
	return s.write(ctx, "note task "+taskID+"'s goal progress", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE `task_goal` SET `progress_at` = ?, `resumes_since_progress` = 0, "+
			"`limit_notified_at` = NULL WHERE `task_id` = ?", now.UTC(), taskID)
		return err
	})
}

// BaseRepairTagPrefix is the fixed part of BaseRepairBranchTag, which
// every base-branch repair carries.
const BaseRepairTagPrefix = baseRepairBranchTagPrefix

// BaseRepairBranchOf is the repo and branch a base-branch repair repairs,
// read off its BaseRepairBranchTag.
func BaseRepairBranchOf(task Task) (RepoRef, string, bool) {
	for _, tag := range task.Tags {
		rest, ok := strings.CutPrefix(tag, baseRepairBranchTagPrefix)
		if !ok {
			continue
		}
		i := strings.LastIndex(rest, ":")
		if i <= 0 {
			continue
		}
		repo, err := ParseRepo(rest[:i])
		if err != nil || rest[i+1:] == "" {
			continue
		}
		return repo, rest[i+1:], true
	}
	return RepoRef{}, "", false
}

// OpenTasksWithTagPrefix is every task not closed carrying a tag that
// starts with prefix, oldest first.
func (s *Store) OpenTasksWithTagPrefix(ctx context.Context, prefix string) ([]string, error) {
	var out []string
	err := each(ctx, s.db,
		"SELECT DISTINCT `tg`.`task_id` FROM `task_tag` AS `tg` "+
			"JOIN `task_state` AS `st` ON `st`.`task_id` = `tg`.`task_id` "+
			"WHERE substr(`tg`.`tag`, 1, ?) = ? AND `st`.`state` != ? ORDER BY `tg`.`task_id`",
		[]any{len(prefix), prefix, string(StateClosed)},
		func(rows *sql.Rows) error {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
			return nil
		})
	return out, err
}

// TaskForPullRequest is the task whose own pull request (LinkFixes) is
// ref ("owner/name#N"), or "" for none.
func (s *Store) TaskForPullRequest(ctx context.Context, ref string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT `task_id` FROM `task_link` WHERE `kind` = ? AND `target` = ? "+
		"ORDER BY `task_id` LIMIT 1", string(LinkFixes), ref).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// RunMadeProgress reports whether runID did something a goal counts as
// progress: pushed a commit through grain's git proxy, or called one of
// taskTools (a task action -- filing, closing, retrying a task).
func (s *Store) RunMadeProgress(ctx context.Context, runID string, taskTools []string) (bool, error) {
	var pushes int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `credential_access_audit` "+
		"WHERE `run_id` = ? AND `action` = 'git-receive-pack'", runID).Scan(&pushes); err != nil {
		return false, fmt.Errorf("reading run %s's pushes: %w", runID, err)
	}
	if pushes > 0 {
		return true, nil
	}
	found := false
	err := each(ctx, s.db, "SELECT `tool`, `calls` FROM `task_run_tool` WHERE `run_id` = ?", []any{runID},
		func(rows *sql.Rows) error {
			var tool string
			var calls int
			if err := rows.Scan(&tool, &calls); err != nil {
				return err
			}
			if calls > 0 && slices.ContainsFunc(taskTools, func(t string) bool { return strings.HasSuffix(tool, t) }) {
				found = true
			}
			return nil
		})
	return found, err
}

// TaskGoalKinds is the goal kind of every task that has a goal, by task
// id: one read for a list of tasks.
func (s *Store) TaskGoalKinds(ctx context.Context) (map[string]GoalKind, error) {
	out := map[string]GoalKind{}
	err := each(ctx, s.db, "SELECT `task_id`, `kind` FROM `task_goal`", nil, func(rows *sql.Rows) error {
		var id, kind string
		if err := rows.Scan(&id, &kind); err != nil {
			return err
		}
		out[id] = GoalKind(kind)
		return nil
	})
	return out, err
}

// ParseGoalArg reads a goal out of a tool or request argument: the
// object itself or its JSON as a string. Nil, absent or "" is no goal.
func ParseGoalArg(v any) (*GoalDecl, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case string:
		return ParseGoalDecl(x)
	default:
		raw, err := json.Marshal(x)
		if err != nil {
			return nil, fmt.Errorf("reading goal: %w", err)
		}
		return ParseGoalDecl(string(raw))
	}
}
