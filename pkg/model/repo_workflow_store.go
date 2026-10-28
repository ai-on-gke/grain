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
)

// GetRepoWorkflows reads every workflow declared for repo on grain's
// own side, ordered by path so a listing, an export and a plan all read
// the same way twice running -- the same order LoadDir gives a
// directory, which is what lets the two compose without either being
// the reason a run's jobs moved.
//
// A repo that declares none is not an error and not a nil to check
// for: it is an empty slice, exactly as a repository with no
// `.grain/workflows` directory declares nothing without failing
// (ci.LoadDir).
func (s *Store) GetRepoWorkflows(ctx context.Context, repo RepoRef) ([]RepoWorkflow, error) {
	var out []RepoWorkflow
	err := each(ctx, s.db,
		"SELECT `path`,`text` FROM `repo_workflow` WHERE `owner` = ? AND `name` = ? ORDER BY `path`",
		[]any{repo.StoredOwner(), repo.Name},
		func(rows *sql.Rows) error {
			w := RepoWorkflow{Repo: repo}
			if err := rows.Scan(&w.Path, &w.Text); err != nil {
				return err
			}
			out = append(out, w)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading workflows for %s: %w", repo, err)
	}
	return out, nil
}

// ListRepoWorkflows returns every workflow this deployment declares,
// for every repo, ordered by repo and then by path.
//
// What the repos listing needs to say which repos have any (a repo
// whose only configuration is a workflow is otherwise a repo with no
// way back to the page that edits it), and what a caller wanting all of
// them at once should use rather than a GetRepoWorkflows per repo.
func (s *Store) ListRepoWorkflows(ctx context.Context) ([]RepoWorkflow, error) {
	var out []RepoWorkflow
	err := each(ctx, s.db,
		"SELECT `owner`,`name`,`path`,`text` FROM `repo_workflow` ORDER BY `owner`,`name`,`path`",
		nil,
		func(rows *sql.Rows) error {
			var w RepoWorkflow
			if err := rows.Scan(&w.Repo.Owner, &w.Repo.Name, &w.Path, &w.Text); err != nil {
				return err
			}
			w.Repo = RepoFromStored(w.Repo.Owner, w.Repo.Name)
			out = append(out, w)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing repo workflows: %w", err)
	}
	return out, nil
}

// PutRepoWorkflows replaces the whole of repo's declared workflows --
// the set, not one of them, so a workflow left out of the call is a
// workflow deleted.
//
// Wholesale for the reason PutRepoConfig is wholesale: a set somebody
// edited is one thing they decided, and a per-path upsert would need a
// delete route beside it that the form has no natural place for. The
// replacement is one transaction, so a failed write leaves the previous
// set intact rather than half of the new one.
//
// Every path is validated here as well as by whatever called this
// (ValidateRepoWorkflowPath): the path is this table's key, and a row
// written under a spelling nothing else uses is a workflow that exists
// in the store and never runs. Nothing here parses the text -- that is
// ci.Parse's, run at the edge that can still show somebody the error
// (ui.SetRepoWorkflows) rather than in the store, which has no format
// of its own to enforce.
func (s *Store) PutRepoWorkflows(ctx context.Context, repo RepoRef, workflows []RepoWorkflow) error {
	seen := make(map[string]bool, len(workflows))
	for _, w := range workflows {
		if err := ValidateRepoWorkflowPath(w.Path); err != nil {
			return err
		}
		if seen[w.Path] {
			return fmt.Errorf("%q: declared twice for %s", w.Path, repo)
		}
		seen[w.Path] = true
	}
	return s.write(ctx, "update workflows for "+repo.String(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM `repo_workflow` WHERE `owner` = ? AND `name` = ?",
			repo.StoredOwner(), repo.Name); err != nil {
			return err
		}
		for _, w := range workflows {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO `repo_workflow` (`owner`,`name`,`path`,`text`) VALUES (?,?,?,?)",
				repo.StoredOwner(), repo.Name, w.Path, w.Text); err != nil {
				return err
			}
		}
		return nil
	})
}
