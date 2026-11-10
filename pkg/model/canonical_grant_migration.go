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
	"fmt"
	"strings"
)

// CanonicalStoredGrant is the string slice 4 of
// design/design/unified-grants.design.md stores for a grant spelled s:
// api:<what>[@repo] and any non-canonical spelling of an api reference
// (={} for no arguments, an unsorted or unprinted {repo}) become
// https://grain.dev/capabilities/api#<what>[={"repo":"<owner/name>"}],
// repo-read:<repo> and repo-write:<repo> become the repo#read / repo#write
// grants naming that repository, and everything else -- a bare in-process
// name such as release-gate-read, a server method, a string none of the
// above parses -- is s unchanged. changed is false when nothing moved.
func CanonicalStoredGrant(s string) (out string, changed bool) {
	trimmed := strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(trimmed, APIGrantPrefix), IsAPIGrantRef(trimmed):
		g := CanonicalGrant(trimmed)
		if what, ok := apiMethodOf(g.Capability); !ok || what == "" {
			return s, false
		}
		out = g.Key()
	case strings.HasPrefix(trimmed, RepoReadCapabilityPrefix):
		repo, ok := RepoReadTarget(trimmed)
		if !ok {
			return s, false
		}
		out = RepoReadGrant(repo)
	case strings.HasPrefix(trimmed, RepoWriteCapabilityPrefix):
		repo, ok := RepoWriteTarget(trimmed)
		if !ok {
			return s, false
		}
		out = RepoWriteGrant(repo)
	default:
		return s, false
	}
	return out, out != s
}

// CanonicalStoredGrants is CanonicalStoredGrant over names, each result kept
// once where it first appears. changed is false when no name moved.
func CanonicalStoredGrants(names []string) (out []string, changed bool) {
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		to, did := CanonicalStoredGrant(name)
		changed = changed || did
		if seen[to] {
			changed = true
			continue
		}
		seen[to] = true
		out = append(out, to)
	}
	return out, changed
}

// migrateCanonicalGrants is schema migration 132's Up, slice 4 of
// unified-grants §3 ("migrate once"): every grant a store holds in a legacy
// spelling -- api:<what>[@repo], an api reference not in its canonical form,
// and any repo-read:/repo-write: migration 88 could not have seen (a
// state-repository import after it) -- is rewritten to CanonicalStoredGrant's
// reference in task_grant, template_grant, schedule_grant, task_grant_wait,
// grain_config.default_capabilities, repo_config.default_capabilities and
// ci_capabilities, capability_policy's catalogue defaults, and the console
// sessions' capabilities and declined defaults. A row whose reference its
// owner already holds is dropped rather than duplicated (rewriteLegacyGrantsMulti).
// The names are read off the store, as migration 88's are, since api grants
// are parameterized by scope and repo.
//
// A task's Target/Base, Reads and Writes stay their own columns and tables:
// they are projected as #target, #read and #write grants by TaskRepoGrants,
// and no task_grant row is written for them (see the design's slice 4 notes).
func migrateCanonicalGrants(ctx context.Context, tx *sql.Tx) error {
	return MigrateCanonicalGrants(ctx, tx)
}

// MigrateCanonicalGrants is migration 132's rewrite, exported for a
// state-repository import at the current schema: a grain/config shared with
// a binary from before slice 3, or edited by hand, can still carry api: and
// repo-read:/repo-write: spellings, which the import rewrites as one key
// with their references (pkg/staterepo importInto).
func MigrateCanonicalGrants(ctx context.Context, tx *sql.Tx) error {
	renames, err := canonicalGrantRenames(ctx, tx)
	if err != nil || len(renames) == 0 {
		return err
	}
	if err := rewriteLegacyGrantsMulti(renames)(ctx, tx); err != nil {
		return err
	}
	if err := rewriteCapabilityListColumnMulti(ctx, tx, "repo_config", []string{"owner", "name"}, "ci_capabilities", renames); err != nil {
		return err
	}
	if err := rewriteCatalogueDefaults(ctx, tx, renames); err != nil {
		return err
	}
	return rewriteConsoleSessionDeclinedDefaults(ctx, tx, renames)
}

// canonicalGrantRenames is every legacy-spelled grant stored anywhere
// migrateCanonicalGrants rewrites, each to its CanonicalStoredGrant.
func canonicalGrantRenames(ctx context.Context, tx *sql.Tx) (map[string][]string, error) {
	renames := map[string][]string{}
	add := func(name string) {
		if to, changed := CanonicalStoredGrant(name); changed {
			renames[name] = []string{to}
		}
	}
	scan := func(query string, each func(string)) error {
		rows, err := tx.QueryContext(ctx, query)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v sql.NullString
			if err := rows.Scan(&v); err != nil {
				return err
			}
			each(v.String)
		}
		return rows.Err()
	}
	list := func(stored string) {
		for _, name := range splitCapabilities(stored) {
			add(name)
		}
	}
	queries := []struct {
		query string
		each  func(string)
	}{
		{"SELECT DISTINCT `capability` FROM `task_grant`", add},
		{"SELECT DISTINCT `capability` FROM `template_grant`", add},
		{"SELECT DISTINCT `capability` FROM `schedule_grant`", add},
		{"SELECT `default_capabilities` FROM `grain_config`", list},
		{"SELECT `default_capabilities` FROM `repo_config`", list},
		{"SELECT `ci_capabilities` FROM `repo_config`", list},
		{"SELECT `console_sessions` FROM `grain_config`", func(stored string) {
			var sessions []struct {
				Capabilities     []string
				DeclinedDefaults []string `json:"declinedDefaults"`
			}
			if json.Unmarshal([]byte(stored), &sessions) != nil {
				return
			}
			for _, s := range sessions {
				for _, name := range s.Capabilities {
					add(name)
				}
				for _, name := range s.DeclinedDefaults {
					add(name)
				}
			}
		}},
		{"SELECT `capability_policy` FROM `grain_config`", func(stored string) {
			policy, err := DecodeCapabilityPolicy(stored)
			if err != nil || policy.Catalogue == nil {
				return
			}
			cat := policy.Catalogue
			for _, name := range cat.Defaults.Task {
				add(name)
			}
			for _, name := range cat.Defaults.CI {
				add(name)
			}
			for _, rd := range cat.Defaults.Repos {
				for _, name := range rd.Task {
					add(name)
				}
				for _, name := range rd.CI {
					add(name)
				}
			}
		}},
	}
	if hasWait, err := migrationTableExists(ctx, tx, "task_grant_wait"); err != nil {
		return nil, err
	} else if hasWait {
		queries = append(queries, struct {
			query string
			each  func(string)
		}{"SELECT DISTINCT `capability` FROM `task_grant_wait`", add})
	}
	for _, q := range queries {
		if err := scan(q.query, q.each); err != nil {
			return nil, fmt.Errorf("finding legacy-spelled grants: %w", err)
		}
	}
	return renames, nil
}

// rewriteConsoleSessionDeclinedDefaults renames each console session's
// declinedDefaults, the default grants it opted out of, so a default
// rewritten beside it stays declined.
func rewriteConsoleSessionDeclinedDefaults(ctx context.Context, tx *sql.Tx, renames map[string][]string) error {
	var stored string
	err := tx.QueryRowContext(ctx, "SELECT `console_sessions` FROM `grain_config` WHERE `id` = 1").Scan(&stored)
	if err == sql.ErrNoRows || (err == nil && stored == "") {
		return nil
	}
	if err != nil {
		return fmt.Errorf("grain_config.console_sessions: reading: %w", err)
	}
	var sessions []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stored), &sessions); err != nil {
		return nil
	}
	changed := false
	for _, s := range sessions {
		raw, ok := s["declinedDefaults"]
		if !ok {
			continue
		}
		var names []string
		if err := json.Unmarshal(raw, &names); err != nil {
			continue
		}
		renamed, did := renameCapabilityListMulti(names, renames)
		if !did {
			continue
		}
		data, err := json.Marshal(renamed)
		if err != nil {
			return err
		}
		s["declinedDefaults"], changed = data, true
	}
	if !changed {
		return nil
	}
	data, err := json.Marshal(sessions)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE `grain_config` SET `console_sessions` = ? WHERE `id` = 1", string(data)); err != nil {
		return fmt.Errorf("grain_config.console_sessions: rewriting declined defaults: %w", err)
	}
	return nil
}

// rewriteCatalogueDefaults rewrites the capability policy's catalogue
// defaults (task, CI, and per-repository task and CI lists) without touching
// policy rules, which already hold capability reference patterns.
func rewriteCatalogueDefaults(ctx context.Context, tx *sql.Tx, renames map[string][]string) error {
	var stored string
	err := tx.QueryRowContext(ctx, "SELECT `capability_policy` FROM `grain_config` WHERE `id` = 1").Scan(&stored)
	if err == sql.ErrNoRows || (err == nil && strings.TrimSpace(stored) == "") {
		return nil
	}
	if err != nil {
		return fmt.Errorf("grain_config.capability_policy: reading: %w", err)
	}
	policy, err := DecodeCapabilityPolicy(stored)
	if err != nil || policy.Catalogue == nil {
		return nil
	}
	cat := policy.Catalogue
	rename := func(names []string) ([]string, bool) {
		if next, did := renameCapabilityListMulti(names, renames); did {
			return next, true
		}
		return names, false
	}
	var changed, did bool
	if cat.Defaults.Task, did = rename(cat.Defaults.Task); did {
		changed = true
	}
	if cat.Defaults.CI, did = rename(cat.Defaults.CI); did {
		changed = true
	}
	for repo, rd := range cat.Defaults.Repos {
		var task, ci bool
		rd.Task, task = rename(rd.Task)
		rd.CI, ci = rename(rd.CI)
		if task || ci {
			cat.Defaults.Repos[repo] = rd
			changed = true
		}
	}
	if !changed {
		return nil
	}
	encoded, err := EncodeCapabilityPolicy(policy)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE `grain_config` SET `capability_policy` = ? WHERE `id` = 1", encoded); err != nil {
		return fmt.Errorf("grain_config.capability_policy: rewriting catalogue defaults: %w", err)
	}
	return nil
}
