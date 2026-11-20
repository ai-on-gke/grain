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
	"github.com/ai-on-gke/grain/pkg/gates"
)

// QAGateEvidence reads a complete, bounded set for one task and exact head.
// Overflow is returned as incomplete, never as a complete empty collection.
// It does not reuse the paged QA UI or change any QA records.
func (s *Store) QAGateEvidence(ctx context.Context, taskID, head string) ([]QACheckRun, []QAFinding, bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, "SELECT `id`,`stage`,`check_id`,`arm`,`shadow`,`verdict`,`experiment_id`,`arm_id`,`flow_hash`,`qa_engine_rev`,`blast_tier`,`triage_decision`,SUBSTR(`detail_json`,1,8192),LENGTH(`detail_json`),`created_at` FROM `task_qa_check_run` WHERE `task_id` = ? AND `commit_sha` = ? ORDER BY `id` LIMIT 513", taskID, head)
	if err != nil {
		return nil, nil, false, err
	}
	runs := []QACheckRun{}
	complete := true
	for rows.Next() {
		r := QACheckRun{TaskID: taskID, CommitSHA: head}
		var shadow, size int
		if err = rows.Scan(&r.ID, &r.Stage, &r.CheckID, &r.Arm, &shadow, &r.Verdict, &r.ExperimentID, &r.ArmID, &r.FlowHash, &r.QAEngineRev, &r.BlastTier, &r.TriageDecision, &r.DetailJSON, &size, &r.CreatedAt); err != nil {
			break
		}
		r.Shadow = shadow != 0
		if size > 8192 {
			complete = false
			r.DetailJSON = ""
		}
		runs = append(runs, r)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return nil, nil, false, err
	}
	if len(runs) > 512 {
		complete = false
		runs = runs[:512]
	}
	findings := []QAFinding{}
	rows, err = tx.QueryContext(ctx, "SELECT f.`id`,f.`check_run_id`,f.`stage`,f.`rule_id`,f.`severity`,SUBSTR(f.`summary`,1,768),LENGTH(f.`summary`),f.`human_vote`,f.`created_at` FROM `task_qa_finding` f JOIN `task_qa_check_run` r ON r.`id` = f.`check_run_id` WHERE f.`task_id` = ? AND r.`task_id` = ? AND r.`commit_sha` = ? ORDER BY f.`id` LIMIT 513", taskID, taskID, head)
	if err != nil {
		return nil, nil, false, err
	}
	for rows.Next() {
		f := QAFinding{TaskID: taskID}
		var size int
		if err = rows.Scan(&f.ID, &f.CheckRunID, &f.Stage, &f.RuleID, &f.Severity, &f.Summary, &size, &f.HumanVote, &f.CreatedAt); err != nil {
			break
		}
		if size > 768 {
			complete = false
		}
		findings = append(findings, f)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return nil, nil, false, err
	}
	if len(findings) > 512 {
		complete = false
		findings = findings[:512]
	}
	return runs, findings, complete, tx.Commit()
}

// SuiteQualification reads only this declaration, rather than the deployment
// catalogue. The caller must already have authorized the subject repository.
func (s *Store) SuiteQualification(ctx context.Context, id string) (*SuiteQualification, error) {
	var q SuiteQualification
	var candidate string
	err := s.db.QueryRowContext(ctx, "SELECT `check_name`,`candidate`,`stage1_workflow`,`stage1_status`,`upstream` FROM `repo_declaration_qualification` WHERE `id` = ?", id).Scan(&q.Check, &candidate, &q.Stage1Workflow, &q.Stage1Status, &q.Upstream)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	q.Candidate = QualificationCandidate(candidate)
	return &q, nil
}

// SuiteRunHeader is the immutable run identity and pinned policy header, without
// expanding sibling tasks or their permission-filtered conversations.
func (s *Store) SuiteRunHeader(ctx context.Context, id int64) (*SuiteRun, error) {
	run, err := scanSuiteRun(s.db.QueryRowContext(ctx, "SELECT "+suiteRunColumns+" FROM `suite_run` WHERE `id` = ?", id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

// QualificationObservationFence captures only this task's mutable domain source
// versions. Remote heads are separate immutable inputs, never inferred from a
// task link or a previously recorded check's commit.
func (s *Store) QualificationObservationFence(ctx context.Context, id string) (string, error) {
	return qualificationObservationFence(ctx, s.db, id)
}
func qualificationObservationFence(ctx context.Context, q querier, id string) (string, error) {
	task, err := getTask(ctx, q, id)
	if err != nil {
		return "", err
	}
	if task == nil {
		return "", sql.ErrNoRows
	}
	return qualificationObservationFenceFrom(ctx, q, task)
}

func qualificationObservationFenceFrom(ctx context.Context, q querier, task *Task) (string, error) {
	if task == nil {
		return "", sql.ErrNoRows
	}
	id := task.ID
	root := ResolveQARootOrigin(task.WorkOrigin, task.ID)
	values := []any{gates.Digest(task)}
	queries := []struct {
		query string
		args  []any
	}{
		{"SELECT `qa` FROM `grain_config` WHERE `id` = 1", nil},
		{"SELECT `epoch`,`regime_id`,`arm`,`flow_id`,`assigned_at` FROM `task_qa_assignment` WHERE `root_origin` = ? ORDER BY `assigned_at`,`epoch` LIMIT 1", []any{root}},
		{"SELECT `experiment_id`,`segment`,`arm_id`,`flow_hash`,`qa_engine_rev`,`assigned_at` FROM `qa_enrollment` WHERE `root_origin` = ?", []any{root}},
		{"SELECT e.`id`,e.`revision`,e.`state`,e.`arms_json`,e.`scope_json`,e.`options_json`,e.`engine_json`,e.`segment` FROM `qa_experiment` e JOIN `qa_enrollment` n ON n.`experiment_id` = e.`id` WHERE n.`root_origin` = ?", []any{root}},
		{"SELECT COALESCE(MAX(`id`),0),COUNT(*) FROM `task_qa_check_run` WHERE `task_id` = ?", []any{id}},
		{"SELECT COALESCE(MAX(`id`),0),COUNT(*) FROM `task_qa_finding` WHERE `task_id` = ?", []any{id}},
		{"SELECT r.`id`,r.`suite_id`,r.`schedule_id`,r.`owner`,r.`repo` FROM `suite_run` r JOIN `suite_run_task` t ON t.`run_id` = r.`id` WHERE t.`task_id` = ?", []any{id}},
		{"SELECT d.`id`,d.`check_name`,d.`candidate`,d.`stage1_workflow`,d.`stage1_status`,d.`upstream` FROM `repo_declaration_qualification` d JOIN `suite_run` r ON r.`suite_id` = d.`id` JOIN `suite_run_task` t ON t.`run_id` = r.`id` WHERE t.`task_id` = ?", []any{id}},
	}
	if task.Target != nil {
		queries = append(queries, struct {
			query string
			args  []any
		}{"SELECT `qa_flow_ref` FROM `repo_config` WHERE `owner` = ? AND `name` = ?", []any{task.Target.StoredOwner(), task.Target.Name}})
	}
	for _, query := range queries {
		rows, err := q.QueryContext(ctx, query.query, query.args...)
		if err != nil {
			return "", err
		}
		cols, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			return "", err
		}
		var records [][]string
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err = rows.Scan(ptrs...); err != nil {
				break
			}
			record := make([]string, len(vals))
			for i, v := range vals {
				if b, ok := v.([]byte); ok {
					record[i] = string(b)
				} else {
					record[i] = fmt.Sprint(v)
				}
			}
			records = append(records, record)
		}
		if err == nil {
			err = rows.Err()
		}
		_ = rows.Close()
		if err != nil {
			return "", err
		}
		values = append(values, records)
	}
	return gates.Digest(values), nil
}
