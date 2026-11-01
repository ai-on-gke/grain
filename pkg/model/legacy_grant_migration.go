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
	"slices"
	"strings"
)

// rewriteLegacyGrants is the Up of a migration retiring the in-process
// capabilities named in renames (docs/design/unified-grants.md §4): every
// stored grant of one is rewritten to the api grant it became. That is
// the three grant tables (a row whose new name the same owner already
// holds is dropped rather than duplicated, keeping the one already
// there), the deployment's and each repository's default_capabilities,
// and the capabilities of grain_config.console_sessions -- the standing
// director and steward among them. ci_capabilities is left alone: an api
// grant in a CI job's list is a token for CIAPI (pkg/capability/ciapi),
// which none of these ever were.
//
// renames is the migration's own copy rather than legacyGrantReplacements,
// so a later conversion adding to that table never changes what an
// earlier migration did.
func rewriteLegacyGrants(renames map[string]string) func(ctx context.Context, tx *sql.Tx) error {
	multi := make(map[string][]string, len(renames))
	for from, to := range renames {
		multi[from] = []string{to}
	}
	return rewriteLegacyGrantsMulti(multi)
}

// rewriteLegacyGrantsMulti is rewriteLegacyGrants for retirements where one
// legacy capability splits into one or more canonical grants (such as
// deployment-health -> [api:deployment:read, grain#health={}]).
func rewriteLegacyGrantsMulti(renames map[string][]string) func(ctx context.Context, tx *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
		for _, t := range []struct{ table, owner string }{
			{"task_grant", "task_id"},
			{"template_grant", "template_id"},
			{"schedule_grant", "schedule_id"},
		} {
			for from, tos := range renames {
				if len(tos) == 0 {
					continue
				}
				for _, extra := range tos[1:] {
					if _, err := tx.ExecContext(ctx,
						"INSERT INTO `"+t.table+"` (`"+t.owner+"`, `capability`, `via`, `folder`) "+
							"SELECT `"+t.owner+"`, ?, `via`, `folder` FROM `"+t.table+"` "+
							"WHERE `capability` = ? AND `"+t.owner+"` NOT IN "+
							"(SELECT `"+t.owner+"` FROM `"+t.table+"` WHERE `capability` = ?)",
						extra, from, extra); err != nil {
						return fmt.Errorf("%s: inserting %s for %s: %w", t.table, extra, from, err)
					}
				}
				to := tos[0]
				if _, err := tx.ExecContext(ctx, "DELETE FROM `"+t.table+"` WHERE `capability` = ? AND `"+t.owner+"` IN "+
					"(SELECT `"+t.owner+"` FROM `"+t.table+"` WHERE `capability` = ?)", from, to); err != nil {
					return fmt.Errorf("%s: dropping %s where %s is already held: %w", t.table, from, to, err)
				}
				if _, err := tx.ExecContext(ctx, "UPDATE `"+t.table+"` SET `capability` = ? WHERE `capability` = ?", to, from); err != nil {
					return fmt.Errorf("%s: rewriting %s to %s: %w", t.table, from, to, err)
				}
			}
		}
		if err := rewriteCapabilityListColumnMulti(ctx, tx, "grain_config", []string{"id"}, "default_capabilities", renames); err != nil {
			return err
		}
		if err := rewriteCapabilityListColumnMulti(ctx, tx, "repo_config", []string{"owner", "name"}, "default_capabilities", renames); err != nil {
			return err
		}
		return rewriteConsoleSessionGrantsMulti(ctx, tx, renames)
	}
}

// renameCapabilityListMulti is names with every entry of renames expanded in
// place and each name kept once, where it first appears. changed is false when
// nothing was renamed.
func renameCapabilityListMulti(names []string, renames map[string][]string) (out []string, changed bool) {
	for _, name := range names {
		if tos, ok := renames[name]; ok {
			changed = true
			for _, to := range tos {
				if !slices.Contains(out, to) {
					out = append(out, to)
				}
			}
			continue
		}
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out, changed
}

// rewriteCapabilityListColumnMulti rewrites a column holding a stored
// capability list (joinCapabilities' text) in every row of table, each
// row named by its key columns.
func rewriteCapabilityListColumnMulti(ctx context.Context, tx *sql.Tx, table string, keys []string, column string, renames map[string][]string) error {
	var cols, where []string
	for _, k := range keys {
		cols = append(cols, "`"+k+"`")
		where = append(where, "`"+k+"` = ?")
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+strings.Join(cols, ",")+", `"+column+"` FROM `"+table+"`")
	if err != nil {
		return fmt.Errorf("%s.%s: reading: %w", table, column, err)
	}
	type update struct {
		key  []any
		list string
	}
	var updates []update
	for rows.Next() {
		key := make([]any, len(keys))
		dest := make([]any, len(keys)+1)
		for i := range key {
			dest[i] = &key[i]
		}
		var stored string
		dest[len(keys)] = &stored
		if err := rows.Scan(dest...); err != nil {
			rows.Close()
			return fmt.Errorf("%s.%s: reading: %w", table, column, err)
		}
		if names, changed := renameCapabilityListMulti(splitCapabilities(stored), renames); changed {
			updates = append(updates, update{key, joinCapabilities(names)})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("%s.%s: reading: %w", table, column, err)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, u := range updates {
		if _, err := tx.ExecContext(ctx, "UPDATE `"+table+"` SET `"+column+"` = ? WHERE "+strings.Join(where, " AND "),
			append([]any{u.list}, u.key...)...); err != nil {
			return fmt.Errorf("%s.%s: rewriting: %w", table, column, err)
		}
	}
	return nil
}

// rewriteConsoleSessionGrantsMulti rewrites the Capabilities of each standing
// session in grain_config.console_sessions. The JSON is edited as JSON,
// every other field passed through exactly as stored, so the migration
// does not depend on what ConsoleSession looks like in whichever build
// runs it.
func rewriteConsoleSessionGrantsMulti(ctx context.Context, tx *sql.Tx, renames map[string][]string) error {
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
		// Left exactly as it is: DecodeConsoleSessions is what reports
		// a value that does not parse, the same with or without this.
		return nil
	}
	changed := false
	for _, s := range sessions {
		raw, ok := s["Capabilities"]
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
		s["Capabilities"], changed = data, true
	}
	if !changed {
		return nil
	}
	data, err := json.Marshal(sessions)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE `grain_config` SET `console_sessions` = ? WHERE `id` = 1", string(data)); err != nil {
		return fmt.Errorf("grain_config.console_sessions: rewriting: %w", err)
	}
	return nil
}
