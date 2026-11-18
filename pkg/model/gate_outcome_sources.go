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
	"time"

	"github.com/ai-on-gke/grain/pkg/gates"
)

// GateOutcomeSource is diagnostic provenance emitted only after an ordinary
// domain reader/writer has verified a product or integration receipt. It has no
// lifecycle authority. No transcript, arbitrary body or remote snapshot is stored.
type GateOutcomeSource struct {
	Proof       string
	BaseHead    string
	RunID       string
	Product     string
	Resource    string
	Head        string
	State       string
	Policy      string
	Attribution string
	At          time.Time
}

const gateOutcomeSourceTable = "CREATE TABLE IF NOT EXISTS `gate_outcome_source` (`task_id` TEXT NOT NULL, `resource` TEXT NOT NULL, `at` DATETIME NOT NULL, `data` TEXT NOT NULL, PRIMARY KEY (`task_id`,`resource`))"

// RecordGateOutcomeSource is best effort and bounded independently of legacy
// writers. Failure cannot change their result, wake a task or request an action.
func (s *Store) RecordGateOutcomeSource(ctx context.Context, id string, source GateOutcomeSource) {
	if id == "" || source.Resource == "" || len(source.Resource) > 512 || len(source.Head) > 256 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	tx, release, err := s.beginGateObservation(ctx)
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback(); release() }()
	var previousRaw string
	readErr := tx.QueryRowContext(ctx, "SELECT `data` FROM `gate_outcome_source` WHERE `task_id` = ? AND `resource` = ?", id, source.Resource).Scan(&previousRaw)
	if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
		return
	}
	if readErr == nil {
		var previous GateOutcomeSource
		if json.Unmarshal([]byte(previousRaw), &previous) != nil {
			return
		}
		if previous.At.After(source.At) {
			return
		}
		if previous.At.Equal(source.At) {
			if previous.Proof == "ambiguous-head-order" {
				return
			}
			if source.State == "unknown" && previous.State != "unknown" {
				return
			}
			if previous.Head != "" && source.Head != "" && previous.Head != source.Head {
				source.State = "unknown"
				source.Head = ""
				source.Proof = "ambiguous-head-order"
			}
			if previous.Head == source.Head && (previous.State == "merged" || previous.State == "submitted" || previous.State == "equivalent") && source.State == "open" {
				return
			}
		}
	}
	raw, err := json.Marshal(source)
	if err != nil || len(raw) > 4096 {
		return
	}
	if string(raw) == previousRaw {
		return
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO `gate_outcome_source` (`task_id`,`resource`,`at`,`data`) VALUES (?,?,?,?) ON CONFLICT (`task_id`,`resource`) DO UPDATE SET `at`=excluded.`at`,`data`=excluded.`data` WHERE `gate_outcome_source`.`at` <= excluded.`at`", id, source.Resource, source.At, string(raw))
	if err != nil {
		return
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM `gate_outcome_source` WHERE `at` < ? OR (`at`,`task_id`,`resource`) <= (SELECT `at`,`task_id`,`resource` FROM `gate_outcome_source` ORDER BY `at` DESC,`task_id` DESC,`resource` DESC LIMIT 1 OFFSET 4096)", source.At.Add(-72*time.Hour)); err != nil {
		return
	}
	_ = tx.Commit()
}

// GateOutcomePolicy pins the local authorization and resource resolution policy
// used by the ordinary reader. A grant or workspace change invalidates reuse.
func GateOutcomePolicy(t Task) string {
	return gates.Digest(struct {
		Target                   *RepoRef
		ChangeTarget             *ChangeTarget
		Grants                   []Grant
		Implementation           string
		Intent                   Intent
		NothingNeededAction      NothingNeededAction
		Hosting                  Hosting
		Interactive, KeepRunning bool
		FilingReason             FilingReason
		Proposer                 TaskProposer
	}{t.Target, t.ChangeTarget, t.Grants, gates.Implementation, t.Intent, t.NothingNeededAction.Resolved(), NormalizeHosting(t.Hosting), t.Interactive, t.KeepRunning, t.Filing.Reason, t.Proposer})
}
