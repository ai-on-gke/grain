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
)

// putPathTriggers makes repo's stored triggers exactly triggers, as read
// at commit -- inside SyncRepoDeclarations' own transaction, so a sync
// replaces them with everything else or not at all.
//
// The branch cursors are left alone: a sync happens on every move of the
// default branch, and clearing them there would restart every watch at
// the new head and skip the very merge that moved it.
func putPathTriggers(ctx context.Context, tx *sql.Tx, repo RepoRef, commit string, triggers []PathTrigger) error {
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM `repo_path_trigger` WHERE `owner` = ? AND `name` = ?", repo.StoredOwner(), repo.Name); err != nil {
		return err
	}
	for _, t := range triggers {
		rule, err := json.Marshal(t)
		if err != nil {
			return fmt.Errorf("encoding trigger %s: %w", t.Name, err)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `repo_path_trigger` (`owner`,`name`,`trigger`,`commit`,`rule`) VALUES (?,?,?,?,?)",
			repo.StoredOwner(), repo.Name, t.Name, commit, string(rule)); err != nil {
			return fmt.Errorf("storing trigger %s: %w", t.Name, err)
		}
	}
	return nil
}

// forgetPathTriggers removes repo's triggers and how far they had got,
// for a repository whose declarations are being forgotten outright.
func forgetPathTriggers(ctx context.Context, tx *sql.Tx, repo RepoRef) error {
	for _, table := range []string{"repo_path_trigger", "path_trigger_cursor", "repo_merge_rules"} {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM `"+table+"` WHERE `owner` = ? AND `name` = ?", repo.StoredOwner(), repo.Name); err != nil {
			return err
		}
	}
	return nil
}

// RepoPathTrigger is one stored trigger and the base commit its rule was
// read at -- what a filed task records as the rule's provenance.
type RepoPathTrigger struct {
	Repo    RepoRef
	Commit  string
	Trigger PathTrigger
}

// PathTriggers returns every trigger this deployment holds, ordered by
// repository and name. A repository that declares none has no rows,
// which is what keeps one with no `.grain/triggers` behaving exactly as
// before: there is nothing here for anything to act on.
func (s *Store) PathTriggers(ctx context.Context) ([]RepoPathTrigger, error) {
	var out []RepoPathTrigger
	err := each(ctx, s.db,
		"SELECT `owner`,`name`,`commit`,`rule` FROM `repo_path_trigger` ORDER BY `owner`,`name`,`trigger`", nil,
		func(rows *sql.Rows) error {
			var r RepoPathTrigger
			var rule string
			if err := rows.Scan(&r.Repo.Owner, &r.Repo.Name, &r.Commit, &rule); err != nil {
				return err
			}
			r.Repo = RepoFromStored(r.Repo.Owner, r.Repo.Name)
			if err := json.Unmarshal([]byte(rule), &r.Trigger); err != nil {
				return fmt.Errorf("decoding %s's trigger: %w", r.Repo, err)
			}
			out = append(out, r)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing path triggers: %w", err)
	}
	return out, nil
}

// PathTriggersOf is PathTriggers for one repository.
func (s *Store) PathTriggersOf(ctx context.Context, repo RepoRef) ([]RepoPathTrigger, error) {
	all, err := s.PathTriggers(ctx)
	if err != nil {
		return nil, err
	}
	var out []RepoPathTrigger
	for _, r := range all {
		if r.Repo.Same(repo) {
			out = append(out, r)
		}
	}
	return out, nil
}

// GetPathTriggerCursor is the commit repo's branch was last diffed up to
// for its merge triggers, or "" for a branch never diffed.
func (s *Store) GetPathTriggerCursor(ctx context.Context, repo RepoRef, branch string) (string, error) {
	var commit string
	err := s.db.QueryRowContext(ctx,
		"SELECT `commit` FROM `path_trigger_cursor` WHERE `owner` = ? AND `name` = ? AND `branch` = ?",
		repo.StoredOwner(), repo.Name, branch).Scan(&commit)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading %s %s's trigger cursor: %w", repo, branch, err)
	}
	return commit, nil
}

// PutPathTriggerCursor records that repo's branch has been diffed up to
// commit, and everything it fired filed.
func (s *Store) PutPathTriggerCursor(ctx context.Context, repo RepoRef, branch, commit string, now time.Time) error {
	return s.write(ctx, "record trigger cursor for "+repo.String()+" "+branch, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"REPLACE INTO `path_trigger_cursor` (`owner`,`name`,`branch`,`commit`,`updated_at`) VALUES (?,?,?,?,?)",
			repo.StoredOwner(), repo.Name, branch, commit, now.UTC())
		return err
	})
}

// TriggerPrincipal is the automation a path trigger's tasks are filed by.
// The repository the trigger was declared in is the task's target, which
// is the attribution a declared schedule's firing carries too.
var TriggerPrincipal = Principal{Kind: PrincipalAutomation, ID: "path-trigger"}

// ComposeTriggerTask is what one firing of a trigger files: the template
// it names, resolved now, placed against repo and base, landing approved
// only if the trigger says auto-approve -- with the template's declared
// fields and its grain-ci grant settled the way a schedule's are.
func (s *Store) ComposeTriggerTask(ctx context.Context, templateID string, repo RepoRef, base string,
	approved bool) (Composition, *Template, error) {
	tmpl, err := s.resolveTemplate(ctx, templateID, RepoRef{})
	if err != nil {
		return Composition{}, nil, fmt.Errorf("resolving template %s: %w", templateID, err)
	}
	if tmpl == nil {
		return Composition{}, nil, fmt.Errorf("template %s does not exist", templateID)
	}
	comp := ComposeTemplateTask(*tmpl, nil, repo, base, approved)
	declarations := customFieldDeclarations{q: s.db}
	fields, err := declarations.apply(ctx, comp.CustomFields)
	if err != nil {
		return Composition{}, nil, err
	}
	comp.CustomFields = fields
	if comp.Target != nil {
		usesGrainCI, err := querierRepoUsesGrainCI(ctx, s.db, *comp.Target)
		if err != nil {
			return Composition{}, nil, err
		}
		applyGrainCIGrant(&comp, usesGrainCI)
	}
	return comp, tmpl, nil
}
