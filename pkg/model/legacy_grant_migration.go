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
// capabilities named in renames (design/design/unified-grants.design.md §4): every
// stored grant of one is rewritten to the api grant it became. That is
// the three grant tables (a row whose new name the same owner already
// holds is dropped rather than duplicated, keeping the one already
// there), the deployment's and each repository's default_capabilities,
// and the capabilities of grain_config.console_sessions -- the standing
// director and steward among them. Migrations 67-82 left
// repo_config.ci_capabilities alone, and migration 83 rewrites past
// retirements there via rewriteCapabilityListColumnMulti (a retirement
// after it, such as migration 84's self-repair or 86's corp-research, rewrites that column
// itself); earlier
// migrations do not change, since stores already past them will not
// re-run them.
//
// renames is the migration's own copy rather than retiredGrantReplacements,
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
		if hasWait, err := migrationTableExists(ctx, tx, "task_grant_wait"); err != nil {
			return err
		} else if hasWait {
			for from, tos := range renames {
				if len(tos) == 0 {
					continue
				}
				if _, err := tx.ExecContext(ctx,
					"UPDATE `task_grant_wait` SET `capability` = ? WHERE `capability` = ?",
					tos[0], from); err != nil {
					return fmt.Errorf("task_grant_wait: rewriting %s to %s: %w", from, tos[0], err)
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

// foldRetiredCatalogueInProcess is migration 90's Up: it moves retired 1-to-1
// preset capability entries in grain_config.capability_policy's catalogue from
// inProcess[<retired>] into apiScopes.presets[<preset>], merging restrictions
// conservatively when both exist, and rewrites retired task defaults in the
// catalogue's own defaults section.
func foldRetiredCatalogueInProcess(retiredPresets map[string]string, defaultRenames map[string][]string) func(ctx context.Context, tx *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
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
		changed := cat.foldRetiredInProcessMap(retiredPresets)
		if len(defaultRenames) > 0 {
			if next, did := renameCapabilityListMulti(cat.Defaults.Task, defaultRenames); did {
				cat.Defaults.Task = next
				changed = true
			}
			for repo, rd := range cat.Defaults.Repos {
				if next, did := renameCapabilityListMulti(rd.Task, defaultRenames); did {
					rd.Task = next
					cat.Defaults.Repos[repo] = rd
					changed = true
				}
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
			return fmt.Errorf("grain_config.capability_policy: rewriting: %w", err)
		}
		return nil
	}
}

// legacyRepoAccessRenames is migration 88's renames: every retired
// repo-read:<repo> and repo-write:<repo> stored anywhere a retirement
// rewrites, each to the repo#read / repo#write grant naming that
// repository (RepoReadGrant, RepoWriteGrant). The names are parameterized,
// so unlike every earlier retirement the table is read off the store
// rather than written out. A name whose repository does not parse is left
// alone, as it granted nothing before either.
func legacyRepoAccessRenames(ctx context.Context, tx *sql.Tx) (map[string][]string, error) {
	renames := map[string][]string{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if !strings.HasPrefix(name, RepoReadCapabilityPrefix) && !strings.HasPrefix(name, RepoWriteCapabilityPrefix) {
			return
		}
		if repo, ok := RepoReadTarget(name); ok {
			renames[name] = []string{RepoReadGrant(repo)}
		} else if repo, ok := RepoWriteTarget(name); ok {
			renames[name] = []string{RepoWriteGrant(repo)}
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
	for _, q := range []struct {
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
			var sessions []struct{ Capabilities []string }
			if json.Unmarshal([]byte(stored), &sessions) != nil {
				return
			}
			for _, s := range sessions {
				for _, name := range s.Capabilities {
					add(name)
				}
			}
		}},
	} {
		if err := scan(q.query, q.each); err != nil {
			return nil, fmt.Errorf("finding retired repository access grants: %w", err)
		}
	}
	return renames, nil
}

// retiredGrantWaitRenames is migration 129's frozen map of retired capability
// names that earlier rewriteLegacyGrantsMulti migrations (67-124) rewrote in
// task_grant without updating task_grant_wait.
var retiredGrantWaitRenames = map[string]string{
	"team-broadcast":              APIGrantPrefix + "team-broadcast",
	"dispatch-control":            APIGrantPrefix + "dispatch-control",
	"pipeline-control":            APIGrantPrefix + "pipeline-control",
	"memory":                      APIGrantPrefix + "memory",
	"release-notes":               APIGrantPrefix + "release-notes",
	"grain-log":                   GrainJournalGrant,
	"deployment-health":           APIGrantPrefix + "deployment:read",
	"db-read":                     GrainSQLReadGrant,
	"db-write":                    GrainSQLWriteGrant,
	"self-upgrade":                APIGrantPrefix + "self-upgrade",
	"test-quarantine":             APIGrantPrefix + "test-quarantine",
	"self-repair":                 GrainHostExecGrant,
	"corp-research":               CorpResearchGrant,
	"git-push-any-ref":            GitPushAnyRefGrant,
	"gcp-key":                     GCPKeySandboxAgentGrant,
	GrainCICapability:             GrainCIGrant,
	"claude-proxy":                ModelProxyGrant("claude"),
	"openai-proxy":                ModelProxyGrant("openai"),
	"gemini-proxy":                ModelProxyGrant("gemini"),
	"image-signing":               ImageSigningRef + "={}",
	"registry-push":               RegistryPushRef + "={}",
	CapabilityPackagePublish:      PackagePublishRef + "={}",
	"gemini-key":                  GCPGeminiKeyGrant,
	SelfDebugCapability:           APIGrantPrefix + "self-debug",
	"bootstrap-playbooks":         BootstrapPlaybooksGrant,
	"read-issues":                 GitHubReadIssuesGrant,
	LegacyGitHubSandboxCapability: GitHubSandboxRepoGrant,
}

func retiredGrantWaitTarget(cap string) (string, bool) {
	trimmed := strings.TrimSpace(cap)
	if to, ok := retiredGrantWaitRenames[trimmed]; ok {
		return to, true
	}
	if repo, ok := RepoReadTarget(trimmed); ok {
		return RepoReadGrant(repo), true
	}
	if repo, ok := RepoWriteTarget(trimmed); ok {
		return RepoWriteGrant(repo), true
	}
	if keyName, ok := GCPKeyName(trimmed); ok {
		if keyName == "" || keyName == "default" {
			return GCPKeySandboxAgentGrant, true
		}
		if validLegacyGCPKeyParam(keyName) {
			return GCPKeyServerGrant(keyName), true
		}
	}
	if credName, ok := LegacyGitCredentialName(trimmed); ok && ValidGitCredentialName(credName) {
		return RepoCredentialGrant(credName), true
	}
	return "", false
}

// migrateRetiredGrantWaits is schema migration 129's Up: rewrites any
// task_grant_wait rows left under a retired capability name by stores that
// migrated through 67-124 before rewriteLegacyGrantsMulti updated
// task_grant_wait.
func migrateRetiredGrantWaits(ctx context.Context, tx *sql.Tx) error {
	hasWait, err := migrationTableExists(ctx, tx, "task_grant_wait")
	if err != nil || !hasWait {
		return err
	}
	type waitRow struct {
		taskID, cap string
	}
	var waits []waitRow
	rows, err := tx.QueryContext(ctx, "SELECT `task_id`, `capability` FROM `task_grant_wait` ORDER BY `task_id`")
	if err != nil {
		return fmt.Errorf("task_grant_wait: reading rows: %w", err)
	}
	for rows.Next() {
		var r waitRow
		if err := rows.Scan(&r.taskID, &r.cap); err != nil {
			rows.Close()
			return fmt.Errorf("task_grant_wait: scanning row: %w", err)
		}
		waits = append(waits, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("task_grant_wait: iterating rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, w := range waits {
		if _, isAdm := LegacyAdmissionAlias(w.cap); isAdm {
			if _, err := tx.ExecContext(ctx, "DELETE FROM `task_grant_wait` WHERE `task_id` = ?", w.taskID); err != nil {
				return fmt.Errorf("task_grant_wait %s: deleting retired admission wait: %w", w.taskID, err)
			}
			continue
		}
		if _, isHost := LegacyHostingAlias(w.cap); isHost {
			if _, err := tx.ExecContext(ctx, "DELETE FROM `task_grant_wait` WHERE `task_id` = ?", w.taskID); err != nil {
				return fmt.Errorf("task_grant_wait %s: deleting retired hosting wait: %w", w.taskID, err)
			}
			continue
		}
		if to, ok := retiredGrantWaitTarget(w.cap); ok && to != w.cap {
			if _, err := tx.ExecContext(ctx,
				"UPDATE `task_grant_wait` SET `capability` = ? WHERE `task_id` = ?",
				to, w.taskID); err != nil {
				return fmt.Errorf("task_grant_wait %s: rewriting %s to %s: %w", w.taskID, w.cap, to, err)
			}
		}
	}
	return nil
}
