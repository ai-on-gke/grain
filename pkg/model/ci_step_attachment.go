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
	"fmt"
	"time"
)

// CIStepAttachment is where one running CI step's command stands in its
// sandbox: the exec the sandbox's wrapper knows it by and how much of its
// output grain has read. A restarted grain reattaches the step by ExecID
// from Cursor rather than marking its run interrupted
// (docs/design/grain-on-gke.md#GKE-4).
type CIStepAttachment struct {
	RunID string
	// JobID is the job's PlannedJob.ID within the run.
	JobID string
	// StepID is the step's in-sandbox id (steps.StepIDFor), which names
	// the attempt as well as the step.
	StepID    string
	Sandbox   string
	ExecID    string
	Cursor    int64
	UpdatedAt time.Time
}

// PutCIStepAttachment records or moves a step's attachment.
func (s *Store) PutCIStepAttachment(ctx context.Context, a CIStepAttachment) error {
	if a.RunID == "" || a.JobID == "" || a.StepID == "" {
		return fmt.Errorf("model: a CI step attachment needs a run, a job and a step")
	}
	return s.write(ctx, "attach CI run "+a.RunID+" step "+a.StepID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"INSERT INTO `ci_step_attachment` (`run_id`,`job_id`,`step_id`,`sandbox`,`exec_id`,`cursor`,`updated_at`) "+
				"VALUES (?,?,?,?,?,?,?) "+
				"ON CONFLICT (`run_id`,`job_id`,`step_id`) DO UPDATE SET `sandbox` = excluded.`sandbox`, "+
				"`exec_id` = excluded.`exec_id`, `cursor` = excluded.`cursor`, `updated_at` = excluded.`updated_at`",
			a.RunID, a.JobID, a.StepID, a.Sandbox, a.ExecID, a.Cursor, a.UpdatedAt.UTC())
		return err
	})
}

// CIStepAttachments returns run's step attachments.
func (s *Store) CIStepAttachments(ctx context.Context, runID string) ([]CIStepAttachment, error) {
	var out []CIStepAttachment
	err := each(ctx, s.db,
		"SELECT `run_id`,`job_id`,`step_id`,`sandbox`,`exec_id`,`cursor`,`updated_at` "+
			"FROM `ci_step_attachment` WHERE `run_id` = ? ORDER BY `job_id`, `step_id`", []any{runID},
		func(rows *sql.Rows) error {
			var a CIStepAttachment
			if err := rows.Scan(&a.RunID, &a.JobID, &a.StepID, &a.Sandbox, &a.ExecID, &a.Cursor, &a.UpdatedAt); err != nil {
				return err
			}
			out = append(out, a)
			return nil
		})
	return out, err
}

// DeleteCIStepAttachments forgets a job's attachments once it has finished
// with its sandbox, or every job's in run when jobID is empty.
func (s *Store) DeleteCIStepAttachments(ctx context.Context, runID, jobID string) error {
	if runID == "" {
		return nil
	}
	return s.write(ctx, "detach CI run "+runID, func(tx *sql.Tx) error {
		if jobID == "" {
			_, err := tx.ExecContext(ctx, "DELETE FROM `ci_step_attachment` WHERE `run_id` = ?", runID)
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM `ci_step_attachment` WHERE `run_id` = ? AND `job_id` = ?", runID, jobID)
		return err
	})
}
