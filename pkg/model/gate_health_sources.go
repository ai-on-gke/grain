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
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ai-on-gke/grain/pkg/gates"
)

// Bounded diagnostic collector records, never a transport/API or evaluator
// back-call. No legacy view, queue or writer consumes these tables.
const gateHealthBaseTable = "CREATE TABLE IF NOT EXISTS `gate_health_base_source` (`repo` TEXT NOT NULL, `ref` TEXT NOT NULL, `at` DATETIME NOT NULL, `proof` TEXT NOT NULL, PRIMARY KEY (`repo`,`ref`))"
const gateHealthWorkflowTable = "CREATE TABLE IF NOT EXISTS `gate_health_workflow_source` (`repo` TEXT NOT NULL, `run_id` INTEGER NOT NULL, `attempt` INTEGER NOT NULL, `completed_at` DATETIME NOT NULL, `at` DATETIME NOT NULL, `proof` TEXT NOT NULL, PRIMARY KEY (`repo`,`run_id`,`attempt`))"

type GateBaseSource struct {
	PlanRevision         string
	ConfirmationRevision string
	RowRevision          string
	PolicyRevision       string
	Tip                  string
	ManagedRefs          []string
	Complete             bool
	Nonreproduction      bool
	At                   time.Time
}

func GateBaseRowRevision(b BaseBranchCI) string { return gates.Digest(b) }
func GateHealthPolicyRevision(c *RepoConfig) string {
	if c == nil {
		return ""
	}
	return gates.Digest(struct {
		Mode  CIMode
		Jobs  []string
		Watch bool
		Ports []string
	}{c.CIMode, c.BaseBranchCIJobs, c.WatchDefaultBranch, c.PortBranches})
}

// RecordGateBaseSource uses only the diagnostic writer. Failure must never
// fail legacy collection.
func (s *Store) RecordGateBaseSource(ctx context.Context, repo RepoRef, ref string, p GateBaseSource) error {
	if ref == "" || repo.Name == "" {
		return fmt.Errorf("missing base source identity")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	tx, release, err := s.beginGateObservation(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(); release() }()
	_, err = tx.ExecContext(ctx, "INSERT INTO `gate_health_base_source` (`repo`,`ref`,`at`,`proof`) VALUES (?,?,?,?) ON CONFLICT (`repo`,`ref`) DO UPDATE SET `at`=excluded.`at`,`proof`=excluded.`proof` WHERE `gate_health_base_source`.`at` <= excluded.`at`", repo.String(), ref, p.At, string(raw))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "DELETE FROM `gate_health_base_source` WHERE `at` < ? OR (`repo`,`ref`) NOT IN (SELECT `repo`,`ref` FROM `gate_health_base_source` ORDER BY `at` DESC,`repo`,`ref` LIMIT 256)", p.At.Add(-24*time.Hour))
	if err != nil {
		return err
	}
	return tx.Commit()
}

// RecordGateWorkflowSource records metadata and the raw complete job listing
// obtained by the existing forge collector. Synthetic job conclusions are never
// accepted here. Issuer/ref/head/event/attempt are pinned together.
func (s *Store) RecordGateWorkflowSource(ctx context.Context, r gates.WorkflowHealthRun, now time.Time) error {
	if r.Repository == "" || r.ID == 0 || r.Attempt < 1 || len(r.Jobs) > 512 {
		return fmt.Errorf("invalid or oversized workflow source")
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(raw) > 256*1024 {
		return fmt.Errorf("workflow proof too large")
	}
	tx, release, err := s.beginGateObservation(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(); release() }()
	// Keep the selection/pruning key in the same version as the proof. A replay
	// can resolve a missing completion time or invalidate a previously known one.
	sortAt := r.CompletedAt
	if sortAt.IsZero() {
		sortAt = now
	} // retain an incomplete newest source marker
	_, err = tx.ExecContext(ctx, "INSERT INTO `gate_health_workflow_source` (`repo`,`run_id`,`attempt`,`completed_at`,`at`,`proof`) VALUES (?,?,?,?,?,?) ON CONFLICT (`repo`,`run_id`,`attempt`) DO UPDATE SET `completed_at`=excluded.`completed_at`,`at`=excluded.`at`,`proof`=excluded.`proof` WHERE `gate_health_workflow_source`.`at` <= excluded.`at`", r.Repository, r.ID, r.Attempt, sortAt, now, string(raw))
	if err != nil {
		return err
	}
	// Seven-day maximum window; eviction never turns absence into success.
	if _, err = tx.ExecContext(ctx, "DELETE FROM `gate_health_workflow_source` WHERE `completed_at` < ?", now.Add(-7*24*time.Hour)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM `gate_health_workflow_source` WHERE `repo` = ? AND (`run_id`,`attempt`) NOT IN (SELECT `run_id`,`attempt` FROM `gate_health_workflow_source` WHERE `repo` = ? ORDER BY `completed_at` DESC,`run_id` DESC,`attempt` DESC LIMIT 128)", r.Repository, r.Repository); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM `gate_health_workflow_source` WHERE (`repo`,`run_id`,`attempt`) NOT IN (SELECT `repo`,`run_id`,`attempt` FROM `gate_health_workflow_source` ORDER BY `completed_at` DESC,`repo`,`run_id` DESC,`attempt` DESC LIMIT 1024)"); err != nil {
		return err
	}
	return tx.Commit()
}
func readGateBaseSource(ctx context.Context, q querier, repo RepoRef, ref string) (*GateBaseSource, error) {
	var raw string
	err := q.QueryRowContext(ctx, "SELECT `proof` FROM `gate_health_base_source` WHERE `repo` = ? AND `ref` = ?", repo.String(), ref).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p GateBaseSource
	if err = json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// MarkGateWorkflowDefaultPush is called only after the existing base collector
// authenticates a same-repository push to the current default branch. It does
// not reconstruct proof from job rows or free-form goal evidence.
func (s *Store) MarkGateWorkflowDefaultPush(ctx context.Context, repo RepoRef, id int64, attempt int, ref, commit string, now time.Time) error {
	tx, release, err := s.beginGateObservation(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(); release() }()
	var raw string
	err = tx.QueryRowContext(ctx, "SELECT `proof` FROM `gate_health_workflow_source` WHERE `repo` = ? AND `run_id` = ? AND `attempt` = ?", repo.String(), id, attempt).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var r gates.WorkflowHealthRun
	if err = json.Unmarshal([]byte(raw), &r); err != nil {
		return err
	}
	if r.Repository != repo.String() || r.HeadRepository != r.Repository || r.Event != "push" || r.Commit != commit || r.Ref != ref || r.ID != id || r.Attempt != attempt {
		return nil
	}
	r.VerifiedDefaultPush = true
	encoded, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE `gate_health_workflow_source` SET `proof` = ? WHERE `repo` = ? AND `run_id` = ? AND `attempt` = ? AND `proof` = ?", string(encoded), repo.String(), id, attempt, raw)
	if err != nil {
		return err
	}
	return tx.Commit()
}
