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

// migrateGitHubCredentialGrants is schema migration 120's Up: rewrites stored
// github-credential:<name> grants across task_grant, template_grant,
// schedule_grant, grain_config.default_capabilities,
// grain_config.console_sessions, repo_config.default_capabilities,
// repo_config.ci_capabilities, and grain_config.capability_policy to
// https://grain.dev/capabilities/repo#credential={"credential":"<name>"}.
func migrateGitHubCredentialGrants(ctx context.Context, tx *sql.Tx) error {
	type configRow struct {
		id               int
		defaultCaps      string
		consoleSessions  string
		capabilityPolicy string
	}
	cfgRows, err := tx.QueryContext(ctx,
		"SELECT `id`, `default_capabilities`, `console_sessions`, `capability_policy` FROM `grain_config`")
	if err != nil {
		return fmt.Errorf("grain_config: reading for github-credential migration: %w", err)
	}
	var configs []configRow
	for cfgRows.Next() {
		var r configRow
		if err := cfgRows.Scan(&r.id, &r.defaultCaps, &r.consoleSessions, &r.capabilityPolicy); err != nil {
			cfgRows.Close()
			return fmt.Errorf("grain_config: scanning for github-credential migration: %w", err)
		}
		configs = append(configs, r)
	}
	if err := cfgRows.Err(); err != nil {
		cfgRows.Close()
		return fmt.Errorf("grain_config: iterating for github-credential migration: %w", err)
	}
	cfgRows.Close()

	renames := map[string][]string{}
	addLegacyName := func(capName string) {
		capName = strings.TrimSpace(capName)
		if credName, ok := LegacyGitCredentialName(capName); ok && ValidGitCredentialName(credName) {
			renames[capName] = []string{RepoCredentialGrant(credName)}
		}
	}

	for _, table := range []string{"task_grant", "template_grant", "schedule_grant"} {
		rows, err := tx.QueryContext(ctx,
			"SELECT DISTINCT `capability` FROM `"+table+"` WHERE `capability` LIKE 'github-credential:%'")
		if err != nil {
			return fmt.Errorf("%s: listing github-credential grants: %w", table, err)
		}
		for rows.Next() {
			var capName string
			if err := rows.Scan(&capName); err != nil {
				rows.Close()
				return fmt.Errorf("%s: scanning github-credential grant: %w", table, err)
			}
			addLegacyName(capName)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("%s: iterating github-credential grants: %w", table, err)
		}
		rows.Close()
	}

	repoRows, err := tx.QueryContext(ctx, "SELECT `default_capabilities`, `ci_capabilities` FROM `repo_config`")
	if err != nil {
		return fmt.Errorf("repo_config: reading capabilities for github-credential migration: %w", err)
	}
	for repoRows.Next() {
		var defCaps, ciCaps string
		if err := repoRows.Scan(&defCaps, &ciCaps); err != nil {
			repoRows.Close()
			return fmt.Errorf("repo_config: scanning capabilities: %w", err)
		}
		for _, c := range splitCapabilities(defCaps) {
			addLegacyName(c)
		}
		for _, c := range splitCapabilities(ciCaps) {
			addLegacyName(c)
		}
	}
	if err := repoRows.Err(); err != nil {
		repoRows.Close()
		return fmt.Errorf("repo_config: iterating capabilities: %w", err)
	}
	repoRows.Close()

	for _, cfg := range configs {
		for _, c := range splitCapabilities(cfg.defaultCaps) {
			addLegacyName(c)
		}
		if strings.TrimSpace(cfg.consoleSessions) != "" {
			var sessions []struct {
				Capabilities []string `json:"capabilities"`
			}
			if json.Unmarshal([]byte(cfg.consoleSessions), &sessions) == nil {
				for _, s := range sessions {
					for _, c := range s.Capabilities {
						addLegacyName(c)
					}
				}
			}
		}
		if strings.TrimSpace(cfg.capabilityPolicy) != "" {
			var pol CapabilityPolicy
			if json.Unmarshal([]byte(cfg.capabilityPolicy), &pol) == nil && pol.Catalogue != nil {
				for _, c := range pol.Catalogue.Defaults.Task {
					addLegacyName(c)
				}
				for _, c := range pol.Catalogue.Defaults.CI {
					addLegacyName(c)
				}
				for _, rd := range pol.Catalogue.Defaults.Repos {
					for _, c := range rd.Task {
						addLegacyName(c)
					}
					for _, c := range rd.CI {
						addLegacyName(c)
					}
				}
			}
		}
	}

	for _, cfg := range configs {
		if strings.TrimSpace(cfg.capabilityPolicy) == "" {
			continue
		}
		updatedPol, polChanged, err := rewriteCapabilityPolicyGitHubCredential(cfg.capabilityPolicy, renames)
		if err != nil {
			return fmt.Errorf("grain_config: rewriting capability_policy: %w", err)
		}
		if polChanged {
			if _, err := tx.ExecContext(ctx,
				"UPDATE `grain_config` SET `capability_policy` = ? WHERE `id` = ?",
				updatedPol, cfg.id); err != nil {
				return fmt.Errorf("grain_config: writing rewritten capability_policy: %w", err)
			}
		}
	}

	if len(renames) == 0 {
		return nil
	}
	if err := rewriteLegacyGrantsMulti(renames)(ctx, tx); err != nil {
		return err
	}
	return rewriteCapabilityListColumnMulti(ctx, tx, "repo_config", []string{"owner", "name"}, "ci_capabilities", renames)
}

func rewriteCapabilityPolicyGitHubCredential(rawPolicy string, renames map[string][]string) (string, bool, error) {
	var pol CapabilityPolicy
	if err := json.Unmarshal([]byte(rawPolicy), &pol); err != nil {
		return rawPolicy, false, nil
	}
	changed := false
	for i := range pol.Rules {
		c := strings.TrimSpace(pol.Rules[i].Capability)
		if name, ok := strings.CutPrefix(c, GitCredentialCapabilityPrefix); ok {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if name != "*" {
				pol.Rules[i].AllowUntrusted = false
			}
			pol.Rules[i].Capability = RepoCredentialRef
			changed = true
		}
	}
	if pol.Catalogue != nil {
		if next, c := renameCapabilityListMulti(pol.Catalogue.Defaults.Task, renames); c {
			pol.Catalogue.Defaults.Task = next
			changed = true
		}
		if next, c := renameCapabilityListMulti(pol.Catalogue.Defaults.CI, renames); c {
			pol.Catalogue.Defaults.CI = next
			changed = true
		}
		for repo, rd := range pol.Catalogue.Defaults.Repos {
			rc := false
			if next, c := renameCapabilityListMulti(rd.Task, renames); c {
				rd.Task = next
				rc = true
			}
			if next, c := renameCapabilityListMulti(rd.CI, renames); c {
				rd.CI = next
				rc = true
			}
			if rc {
				pol.Catalogue.Defaults.Repos[repo] = rd
				changed = true
			}
		}
	}
	if !changed {
		return rawPolicy, false, nil
	}
	out, err := json.Marshal(pol)
	if err != nil {
		return "", false, fmt.Errorf("encoding capability_policy: %w", err)
	}
	return string(out), true, nil
}
