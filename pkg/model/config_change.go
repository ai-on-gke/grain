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
	"strings"
	"time"
)

// A configuration change a session proposed with propose_config_change
// (docs/design/config-repo-settings.md, section 4.3): the session pushed
// a branch of grain/config, the controller validated it, and the session
// is parked until a person approves or rejects it inline in the chat
// (ui's POST /api/tasks/{id}/config-change/approve and /reject).

// PendingConfigChange is a proposed grain/config change waiting on a
// person's decision, pinned to the commit they were shown.
type PendingConfigChange struct {
	// Branch is the proposal branch in grain/config, under
	// ConfigProposalBranchPrefix of the proposing task.
	Branch string `json:"branch"`
	// Base is the branch the change merges into; BaseSHA is its tip
	// when the change was proposed, the commit Diff is taken from.
	Base    string `json:"base,omitempty"`
	BaseSHA string `json:"baseSha"`
	// HeadSHA is Branch's tip as the person was shown it: approving
	// merges exactly this commit, and nothing if the branch has moved.
	HeadSHA     string `json:"headSha"`
	PRNumber    int    `json:"prNumber,omitempty"`
	Summary     string `json:"summary"`
	Description string `json:"description,omitempty"`
	// Files are the paths the change touches, Diff its unified diff
	// against the merge base with Base.
	Files      []string               `json:"files"`
	Diff       string                 `json:"diff"`
	Validation ConfigChangeValidation `json:"validation"`
	CreatedAt  time.Time              `json:"createdAt"`
}

// ConfigChangeValidation is what the pre-review suite
// (staterepo.ValidateCandidateConfig) said about a proposed change.
type ConfigChangeValidation struct {
	// OK is a change the suite passed: one a person may approve.
	OK bool `json:"ok"`
	// Error is why it did not pass, empty when it did.
	Error string `json:"error,omitempty"`
	// Warnings do not refuse the change but a reviewer should read them.
	Warnings []string `json:"warnings,omitempty"`
	// Liveness lists what would stop dispatch if this merged.
	Liveness []string `json:"liveness,omitempty"`
}

// ConfigProposalBranchPrefix is the branch namespace of grain/config a
// task holding grain/config in its Writes may push proposals to:
// grain/config/<task-id>/. The git proxy lets that task create, move and
// delete branches under it (and only under it); its own task branch is
// pushable as ever, and main never is.
func ConfigProposalBranchPrefix(taskID string) string {
	return "grain/config/" + taskID + "/"
}

// IsConfigProposalBranch reports whether branch is one of taskID's
// proposal branches: under its prefix, with a non-empty name after it.
func IsConfigProposalBranch(taskID, branch string) bool {
	prefix := ConfigProposalBranchPrefix(taskID)
	return taskID != "" && strings.HasPrefix(branch, prefix) && len(branch) > len(prefix)
}

// GitConfigProposalPrefix is the proposal branch prefix
// (ConfigProposalBranchPrefix) a sandbox's live run may push in
// grain/config, or "" when its task does not hold grain/config in its
// Writes, or nothing is live on the sandbox.
func (s *Store) GitConfigProposalPrefix(ctx context.Context, sandbox string) (string, error) {
	taskID, live, err := s.liveTaskID(ctx, sandbox)
	if err != nil || !live {
		return "", err
	}
	found := false
	err = each(ctx, s.db,
		"SELECT `owner`,`name` FROM `task_write` WHERE `task_id` = ?",
		taskID, func(rows *sql.Rows) error {
			var owner, name string
			if err := rows.Scan(&owner, &name); err != nil {
				return err
			}
			if IsConfigRepo(RepoFromStored(owner, name)) {
				found = true
			}
			return nil
		})
	if err != nil {
		return "", fmt.Errorf("reading write targets of task %s: %w", taskID, err)
	}
	if !found {
		return "", nil
	}
	return ConfigProposalBranchPrefix(taskID), nil
}
