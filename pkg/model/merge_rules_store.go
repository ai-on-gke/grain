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
)

// putMergeRules makes repo's stored merge rules exactly rules, as read at
// commit -- inside SyncRepoDeclarations' own transaction, so a sync
// replaces them with everything else or not at all. nil removes them.
//
// Rules that say exactly what the stored ones say keep the commit they
// were first read at. That commit is what every pull request's recorded
// classification is checked against (EffectiveMergePolicy.Rules), so
// restamping it with each new base head made every merge invalidate every
// open change's classification: the queue re-noted each of them, and
// batched none of them until it had classified them again -- by which time
// it had merged the head on its own (docs/test-runs/2026-10-05-merge-rules.md, F3).
func putMergeRules(ctx context.Context, tx *sql.Tx, repo RepoRef, commit string, rules *MergeRules) error {
	if rules != nil {
		same, err := sameMergeRules(ctx, tx, repo, rules)
		if err != nil {
			return err
		}
		if same {
			return nil
		}
	}
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM `repo_merge_rules` WHERE `owner` = ? AND `name` = ?", repo.StoredOwner(), repo.Name); err != nil {
		return err
	}
	if rules == nil {
		return nil
	}
	stored := *rules
	stored.Commit = commit
	b, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("encoding merge rules: %w", err)
	}
	_, err = tx.ExecContext(ctx,
		"INSERT INTO `repo_merge_rules` (`owner`,`name`,`commit`,`rules`) VALUES (?,?,?,?)",
		repo.StoredOwner(), repo.Name, commit, string(b))
	return err
}

// sameMergeRules reports whether repo's stored rules say what rules says,
// whatever commit either was read at.
func sameMergeRules(ctx context.Context, tx *sql.Tx, repo RepoRef, rules *MergeRules) (bool, error) {
	var text string
	err := tx.QueryRowContext(ctx,
		"SELECT `rules` FROM `repo_merge_rules` WHERE `owner` = ? AND `name` = ?",
		repo.StoredOwner(), repo.Name).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading %s's merge rules: %w", repo, err)
	}
	var prev MergeRules
	if err := json.Unmarshal([]byte(text), &prev); err != nil {
		return false, nil // unreadable: replaced
	}
	next := *rules
	prev.Commit, next.Commit = "", ""
	a, err := json.Marshal(prev)
	if err != nil {
		return false, err
	}
	b, err := json.Marshal(next)
	if err != nil {
		return false, fmt.Errorf("encoding merge rules: %w", err)
	}
	return string(a) == string(b), nil
}

// MergeRulesOf is repo's merge rules as its base declares them, or nil
// where it declares none.
func (s *Store) MergeRulesOf(ctx context.Context, repo RepoRef) (*MergeRules, error) {
	var commit, text string
	err := s.db.QueryRowContext(ctx,
		"SELECT `commit`,`rules` FROM `repo_merge_rules` WHERE `owner` = ? AND `name` = ?",
		repo.StoredOwner(), repo.Name).Scan(&commit, &text)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s's merge rules: %w", repo, err)
	}
	var rules MergeRules
	if err := json.Unmarshal([]byte(text), &rules); err != nil {
		return nil, fmt.Errorf("decoding %s's merge rules: %w", repo, err)
	}
	rules.Commit = commit
	return &rules, nil
}
