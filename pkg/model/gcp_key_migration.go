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
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

const gcpKeyDefaultMaxLease = 24 * time.Hour

const gcpKeyMigrationNotice = "In-process gcp-key was migrated to gcp#gcp-key permission sets. " +
	"Bare gcp-key and gcp-key:<name> remain accepted as aliases for one release; " +
	"update .grain declarations and external configs to https://grain.dev/capabilities/gcp#gcp-key."

// GCPPoolEmails expands a configured service account email and pool size into
// the ordered list of service account emails the pool mints keys for, matching
// gcpkey.PoolEmails.
func GCPPoolEmails(email string, count int, projectID string) []string {
	email = strings.TrimSpace(email)
	projectID = strings.TrimSpace(projectID)
	if email == "" {
		return nil
	}
	if count <= 1 {
		if !strings.Contains(email, "@") && projectID != "" {
			return []string{fmt.Sprintf("%s@%s.iam.gserviceaccount.com", email, projectID)}
		}
		return []string{email}
	}
	prefix, domain, hasAt := strings.Cut(email, "@")
	if !hasAt && projectID != "" {
		domain = fmt.Sprintf("%s.iam.gserviceaccount.com", projectID)
	}
	prefix = strings.TrimSuffix(prefix, "-0")
	prefix = strings.TrimRight(prefix, "-")
	emails := make([]string, count)
	for i := 0; i < count; i++ {
		if domain != "" {
			emails[i] = fmt.Sprintf("%s-%d@%s", prefix, i, domain)
		} else {
			emails[i] = fmt.Sprintf("%s-%d", prefix, i)
		}
	}
	return emails
}

type seededGCPSet struct {
	name     string
	project  string
	accounts []string
	maxLease time.Duration
	keyPath  string
	secret   string
}

// migrateGCPKeyToCapabilityServer is schema migration 96's Up:
//  1. Seeds https://grain.dev/capabilities/gcp permission sets in
//     grain_config.mcp_servers from the deployment's in-process pool
//     (grain_config.gcp_project / gcp_service_account_email /
//     gcp_service_account_count -> "sandbox-agent") and from every gcp_key
//     row ("<name>", preserving its /home/debian/.gcp-service-account-<name>.json
//     key placement via key_path).
//  2. Rewrites stored gcp-key and gcp-key:<name> grants across task_grant,
//     template_grant, schedule_grant, grain_config.default_capabilities,
//     grain_config.console_sessions, repo_config.default_capabilities,
//     repo_config.ci_capabilities, and grain_config.capability_policy.
func migrateGCPKeyToCapabilityServer(ctx context.Context, tx *sql.Tx) error {
	type configRow struct {
		id               int
		gcpProject       string
		gcpSAEmail       string
		gcpSACount       int
		defaultCaps      string
		consoleSessions  string
		mcpServers       string
		capabilityPolicy string
	}
	cfgRows, err := tx.QueryContext(ctx,
		"SELECT `id`, `gcp_project`, `gcp_service_account_email`, `gcp_service_account_count`, "+
			"`default_capabilities`, `console_sessions`, `mcp_servers`, `capability_policy` FROM `grain_config`")
	if err != nil {
		return fmt.Errorf("grain_config: reading for gcp-key migration: %w", err)
	}
	var configs []configRow
	for cfgRows.Next() {
		var r configRow
		if err := cfgRows.Scan(&r.id, &r.gcpProject, &r.gcpSAEmail, &r.gcpSACount,
			&r.defaultCaps, &r.consoleSessions, &r.mcpServers, &r.capabilityPolicy); err != nil {
			cfgRows.Close()
			return fmt.Errorf("grain_config: scanning for gcp-key migration: %w", err)
		}
		configs = append(configs, r)
	}
	if err := cfgRows.Err(); err != nil {
		cfgRows.Close()
		return fmt.Errorf("grain_config: iterating for gcp-key migration: %w", err)
	}
	cfgRows.Close()

	type keyRow struct {
		name      string
		projectID string
		saEmail   string
		saCount   int
		minter    string
		maxAgeSec int64
	}
	kRows, err := tx.QueryContext(ctx,
		"SELECT `name`, `project_id`, `service_account_email`, `service_account_count`, `minter_credential`, `max_key_age_seconds` "+
			"FROM `gcp_key` ORDER BY `name`")
	if err != nil {
		return fmt.Errorf("gcp_key: reading for gcp-key migration: %w", err)
	}
	var keyRows []keyRow
	for kRows.Next() {
		var kr keyRow
		if err := kRows.Scan(&kr.name, &kr.projectID, &kr.saEmail, &kr.saCount, &kr.minter, &kr.maxAgeSec); err != nil {
			kRows.Close()
			return fmt.Errorf("gcp_key: scanning for gcp-key migration: %w", err)
		}
		keyRows = append(keyRows, kr)
	}
	if err := kRows.Err(); err != nil {
		kRows.Close()
		return fmt.Errorf("gcp_key: iterating for gcp-key migration: %w", err)
	}
	kRows.Close()

	renames := map[string][]string{
		"gcp-key":         {GCPKeySandboxAgentGrant},
		"gcp-key:default": {GCPKeySandboxAgentGrant},
	}
	addLegacyName := func(capName string) {
		capName = strings.TrimSpace(capName)
		if keyName, ok := GCPKeyName(capName); ok && keyName != "" && validLegacyGCPKeyParam(keyName) {
			renames[capName] = []string{GCPKeyServerGrant(keyName)}
		}
	}
	for _, kr := range keyRows {
		if n := strings.TrimSpace(kr.name); n != "" && n != "default" {
			addLegacyName(GCPKeyCapability(n))
		}
	}
	for _, table := range []string{"task_grant", "template_grant", "schedule_grant"} {
		rows, err := tx.QueryContext(ctx,
			"SELECT DISTINCT `capability` FROM `"+table+"` WHERE `capability` LIKE 'gcp-key:%'")
		if err != nil {
			return fmt.Errorf("%s: listing gcp-key grants: %w", table, err)
		}
		for rows.Next() {
			var capName string
			if err := rows.Scan(&capName); err != nil {
				rows.Close()
				return fmt.Errorf("%s: scanning gcp-key grant: %w", table, err)
			}
			addLegacyName(capName)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("%s: iterating gcp-key grants: %w", table, err)
		}
		rows.Close()
	}
	repoRows, err := tx.QueryContext(ctx, "SELECT `default_capabilities`, `ci_capabilities` FROM `repo_config`")
	if err != nil {
		return fmt.Errorf("repo_config: reading capabilities for gcp-key migration: %w", err)
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

	// Seed https://grain.dev/capabilities/gcp permission sets and rewrite
	// capability_policy on each grain_config row.
	for _, cfg := range configs {
		var seeds []seededGCPSet
		if proj, email := strings.TrimSpace(cfg.gcpProject), strings.TrimSpace(cfg.gcpSAEmail); proj != "" && email != "" {
			count := cfg.gcpSACount
			if count <= 0 {
				count = 1
			}
			seeds = append(seeds, seededGCPSet{
				name:     GCPKeyDefaultPermissionSet,
				project:  proj,
				accounts: GCPPoolEmails(email, count, proj),
				maxLease: gcpKeyDefaultMaxLease,
				secret:   (GCPKey{Name: "default"}).EffectiveMinterCredential(),
			})
		}
		for _, kr := range keyRows {
			proj, email := strings.TrimSpace(kr.projectID), strings.TrimSpace(kr.saEmail)
			if proj == "" || email == "" {
				continue
			}
			name := strings.TrimSpace(kr.name)
			if name == "" || strings.EqualFold(name, "default") {
				name = GCPKeyDefaultPermissionSet
			}
			count := kr.saCount
			if count <= 0 {
				count = 1
			}
			lease := gcpKeyDefaultMaxLease
			if kr.maxAgeSec > 0 {
				lease = time.Duration(kr.maxAgeSec) * time.Second
			}
			keyPath := ""
			if name != GCPKeyDefaultPermissionSet {
				keyPath = "/home/debian/.gcp-service-account-" + name + ".json"
			}
			seeds = append(seeds, seededGCPSet{
				name:     name,
				project:  proj,
				accounts: GCPPoolEmails(email, count, proj),
				maxLease: lease,
				keyPath:  keyPath,
				secret:   (GCPKey{Name: kr.name, MinterCredential: kr.minter}).EffectiveMinterCredential(),
			})
		}

		if len(seeds) > 0 {
			updatedMCP, mcpChanged, err := seedGCPServerPermissionSets(cfg.mcpServers, seeds)
			if err != nil {
				return fmt.Errorf("grain_config: seeding gcp permission sets: %w", err)
			}
			if mcpChanged {
				if _, err := tx.ExecContext(ctx,
					"UPDATE `grain_config` SET `mcp_servers` = ? WHERE `id` = ?",
					updatedMCP, cfg.id); err != nil {
					return fmt.Errorf("grain_config: writing seeded mcp_servers: %w", err)
				}
			}
		}

		if strings.TrimSpace(cfg.capabilityPolicy) != "" {
			updatedPol, polChanged, err := rewriteCapabilityPolicyGCPKey(cfg.capabilityPolicy, renames)
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
	}

	if err := rewriteLegacyGrantsMulti(renames)(ctx, tx); err != nil {
		return err
	}
	return rewriteCapabilityListColumnMulti(ctx, tx, "repo_config", []string{"owner", "name"}, "ci_capabilities", renames)
}

func seedGCPServerPermissionSets(rawMCP string, seeds []seededGCPSet) (string, bool, error) {
	servers := map[string]protocol.ServerConfig{}
	if strings.TrimSpace(rawMCP) != "" {
		if err := json.Unmarshal([]byte(rawMCP), &servers); err != nil {
			return "", false, fmt.Errorf("decoding mcp_servers: %w", err)
		}
	}
	entry, hadEntry := servers[GCPCapabilityServerID]
	if !hadEntry {
		entry = protocol.ServerConfig{
			Command: "grain",
			Args:    []string{"capability", "gcp"},
			Builtin: "gcp",
		}
	}
	if entry.Config == nil {
		entry.Config = map[string]any{}
	}

	var existingSets []any
	existingSetNames := map[string]bool{}
	if rawSets, ok := entry.Config["permission_sets"].([]any); ok {
		for _, item := range rawSets {
			existingSets = append(existingSets, item)
			if m, ok := item.(map[string]any); ok {
				if n, ok := m["name"].(string); ok && strings.TrimSpace(n) != "" {
					existingSetNames[strings.TrimSpace(n)] = true
				}
			}
		}
	}

	var toAdd []seededGCPSet
	for _, s := range seeds {
		if existingSetNames[s.name] {
			continue
		}
		existingSetNames[s.name] = true
		toAdd = append(toAdd, s)
	}
	if len(toAdd) == 0 {
		return rawMCP, false, nil
	}

	_, hadAdmins := entry.Config["admins"]
	_, hadAdminCred := entry.Config["admin_credential"]
	_, hadAdminSrc := entry.Config["admin_credential_source"]
	hadExistingSets := len(existingSets) > 0

	var distinctSecrets []string
	secretSeen := map[string]bool{}
	for _, s := range toAdd {
		if !secretSeen[s.secret] {
			secretSeen[s.secret] = true
			distinctSecrets = append(distinctSecrets, s.secret)
		}
	}

	// Single minter secret on a fresh/unconfigured gcp server entry uses the
	// standard top-level admin_credential_source + admin_credential fields.
	useTopLevelSingleAdmin := !hadExistingSets && !hadAdmins && !hadAdminCred && !hadAdminSrc && len(distinctSecrets) == 1
	adminBySecret := map[string]string{}

	if useTopLevelSingleAdmin {
		entry.Config["admin_credential_source"] = "configured"
		entry.Config["admin_credential"] = "$secret:" + distinctSecrets[0]
	} else {
		var admins []any
		usedAdminNames := map[string]bool{}
		if rawAdmins, ok := entry.Config["admins"].([]any); ok {
			for _, item := range rawAdmins {
				admins = append(admins, item)
				if m, ok := item.(map[string]any); ok {
					if n, ok := m["name"].(string); ok && n != "" {
						usedAdminNames[n] = true
					}
				}
			}
		} else if hadExistingSets || hadAdminCred || hadAdminSrc {
			src := "ambient"
			if v, ok := entry.Config["admin_credential_source"].(string); ok && strings.TrimSpace(v) != "" {
				src = strings.TrimSpace(v)
			} else if v, ok := entry.Config["admin_credential"].(string); ok && strings.TrimSpace(v) != "" {
				src = "configured"
			}
			admins = append(admins, map[string]any{
				"name":              "default",
				"credential_source": src,
			})
			usedAdminNames["default"] = true
		}

		if entry.Env == nil {
			entry.Env = map[string]string{}
		}
		for _, sec := range distinctSecrets {
			adminName := uniqueMigratedAdminName(sec, usedAdminNames)
			usedAdminNames[adminName] = true
			envName := migratedMinterEnvVar(adminName)
			entry.Env[envName] = "$secret:" + sec
			admins = append(admins, map[string]any{
				"name":              adminName,
				"credential_source": "configured",
				"credential_env":    envName,
			})
			adminBySecret[sec] = adminName
		}
		entry.Config["admins"] = admins
	}

	for _, s := range toAdd {
		accts := make([]any, len(s.accounts))
		for i, a := range s.accounts {
			accts[i] = a
		}
		item := map[string]any{
			"name":         s.name,
			"project":      s.project,
			"accounts":     accts,
			"max_lease":    s.maxLease.String(),
			"adopt_legacy": true,
		}
		if s.keyPath != "" {
			item["key_path"] = s.keyPath
		}
		if admin := adminBySecret[s.secret]; admin != "" {
			item["admin"] = admin
		}
		existingSets = append(existingSets, item)
	}
	entry.Config["permission_sets"] = existingSets
	if strings.TrimSpace(entry.Notice) == "" {
		entry.Notice = gcpKeyMigrationNotice
	}
	servers[GCPCapabilityServerID] = entry

	out, err := json.Marshal(servers)
	if err != nil {
		return "", false, fmt.Errorf("encoding mcp_servers: %w", err)
	}
	return string(out), true, nil
}

func uniqueMigratedAdminName(secret string, used map[string]bool) string {
	base := "minter"
	if suffix := strings.TrimPrefix(secret, "gcp-key-minter-"); suffix != secret && suffix != "" {
		base = "minter-" + sanitizeAdminToken(suffix)
	} else if secret != "gcp-key-minter" {
		base = "minter-" + sanitizeAdminToken(secret)
	}
	base = strings.TrimRight(base, "-")
	if base == "" {
		base = "minter"
	}
	if len(base) > 28 {
		base = strings.TrimRight(base[:28], "-")
	}
	candidate := base
	for i := 2; used[candidate]; i++ {
		suffix := fmt.Sprintf("-%d", i)
		maxBase := 31 - len(suffix)
		trimmed := base
		if len(trimmed) > maxBase {
			trimmed = strings.TrimRight(trimmed[:maxBase], "-")
		}
		candidate = trimmed + suffix
	}
	return candidate
}

func sanitizeAdminToken(s string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func migratedMinterEnvVar(adminName string) string {
	var b strings.Builder
	b.WriteString("GRAIN_GCP_MINTER_")
	for _, r := range strings.ToUpper(adminName) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func rewriteCapabilityPolicyGCPKey(rawPolicy string, renames map[string][]string) (string, bool, error) {
	var pol CapabilityPolicy
	if err := json.Unmarshal([]byte(rawPolicy), &pol); err != nil {
		return "", false, fmt.Errorf("decoding capability_policy: %w", err)
	}
	changed := false
	for i := range pol.Rules {
		c := strings.TrimSpace(pol.Rules[i].Capability)
		if c == "gcp-key" || strings.HasPrefix(c, "gcp-key:") {
			if name, ok := strings.CutPrefix(c, "gcp-key:"); ok {
				name = strings.TrimSpace(name)
				if name != "" && !strings.EqualFold(name, "default") {
					pol.Rules[i].AllowUntrusted = false
				}
			}
			pol.Rules[i].Capability = GCPKeyRef
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
