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
	"regexp"
	"strings"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

// geminiKeyRenames is migration 102's renames, the migration's own copy as
// rewriteLegacyGrants asks.
var geminiKeyRenames = map[string][]string{
	"gemini-key": {GCPGeminiKeyRef + "={}"},
}

// geminiKeyMinterSecret is the secret the in-process gemini-key minted
// with (gcpkey.DefaultMinterCredential), named here so migration 102 does
// not change if that constant ever does.
const geminiKeyMinterSecret = "$secret:gcp-key-minter"

// geminiKeyProjectPattern is gcpsets' project id pattern: a project the
// gcp server would refuse is not seeded, since one bad gemini_key fails
// the whole entry, permission sets included.
var geminiKeyProjectPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)

const geminiKeyMigrationNotice = "In-process gemini-key was migrated to gcp#gemini-key, minting with the gcp-key-minter secret. " +
	"Bare gemini-key remains accepted as an alias for one release; " +
	"update .grain declarations and external configs to https://grain.dev/capabilities/gcp#gemini-key."

// migrateGeminiKeyToCapabilityServer is schema migration 102's Up: the
// in-process gemini-key is retired onto the gcp server's gemini-key
// method.
//  1. Where grain_config.gcp_project is set -- when the in-process
//     provider was registered -- the gcp server entry in mcp_servers gets
//     gemini_key: {project}, minting with the gcp-key-minter secret the
//     provider minted with (seedGCPGeminiKey).
//  2. Every stored gemini-key grant is rewritten to gcp#gemini-key={}, as
//     migration 100 rewrote the model proxies.
func migrateGeminiKeyToCapabilityServer(ctx context.Context, tx *sql.Tx) error {
	type configRow struct {
		id         int
		project    string
		mcpServers string
	}
	rows, err := tx.QueryContext(ctx, "SELECT `id`, `gcp_project`, `mcp_servers` FROM `grain_config`")
	if err != nil {
		return fmt.Errorf("grain_config: reading for gemini-key migration: %w", err)
	}
	var configs []configRow
	for rows.Next() {
		var r configRow
		if err := rows.Scan(&r.id, &r.project, &r.mcpServers); err != nil {
			rows.Close()
			return fmt.Errorf("grain_config: scanning for gemini-key migration: %w", err)
		}
		configs = append(configs, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("grain_config: iterating for gemini-key migration: %w", err)
	}
	rows.Close()
	for _, cfg := range configs {
		project := strings.TrimSpace(cfg.project)
		if !geminiKeyProjectPattern.MatchString(project) {
			continue
		}
		updated, changed, err := seedGCPGeminiKey(cfg.mcpServers, project)
		if err != nil {
			return fmt.Errorf("grain_config: seeding gcp#gemini-key: %w", err)
		}
		if changed {
			if _, err := tx.ExecContext(ctx,
				"UPDATE `grain_config` SET `mcp_servers` = ? WHERE `id` = ?", updated, cfg.id); err != nil {
				return fmt.Errorf("grain_config: writing seeded mcp_servers: %w", err)
			}
		}
	}
	if err := rewriteLegacyGrantsMulti(geminiKeyRenames)(ctx, tx); err != nil {
		return err
	}
	if err := rewriteCapabilityListColumnMulti(ctx, tx, "repo_config", []string{"owner", "name"}, "ci_capabilities", geminiKeyRenames); err != nil {
		return err
	}
	return rewritePolicyRenames(ctx, tx, geminiKeyRenames, GCPGeminiKeyRef)
}

// seedGCPGeminiKey adds gemini_key: {project, admin?} to rawMCP's gcp
// server entry, minting with geminiKeyMinterSecret. The admin it names is
//   - the top-level one, when admin_credential is already that secret,
//   - the top-level one, set to that secret, when the entry configures no
//     admin and no permission set,
//   - the admins[] entry whose credential_env is bound to that secret,
//   - otherwise a new configured admin bound through GRAIN_GCP_MINTER_*,
//     the top-level admin kept as admins[] "default" so the sets already
//     there keep minting with it.
//
// An entry that already configures gemini_key is left as it is.
func seedGCPGeminiKey(rawMCP, project string) (string, bool, error) {
	servers := map[string]protocol.ServerConfig{}
	if strings.TrimSpace(rawMCP) != "" {
		if err := json.Unmarshal([]byte(rawMCP), &servers); err != nil {
			return "", false, fmt.Errorf("decoding mcp_servers: %w", err)
		}
	}
	entry, ok := servers[GCPCapabilityServerID]
	if !ok {
		entry = protocol.ServerConfig{Command: "grain", Args: []string{"capability", "gcp"}, Builtin: "gcp"}
	}
	if entry.Config == nil {
		entry.Config = map[string]any{}
	}
	if _, ok := entry.Config["gemini_key"]; ok {
		return rawMCP, false, nil
	}

	cred, _ := entry.Config["admin_credential"].(string)
	cred = strings.TrimSpace(cred)
	src, _ := entry.Config["admin_credential_source"].(string)
	src = strings.TrimSpace(src)
	rawAdmins, hasAdmins := entry.Config["admins"].([]any)
	sets, _ := entry.Config["permission_sets"].([]any)

	admin := ""
	switch {
	case !hasAdmins && cred == geminiKeyMinterSecret:
	case !hasAdmins && cred == "" && src == "" && len(sets) == 0:
		entry.Config["admin_credential_source"] = "configured"
		entry.Config["admin_credential"] = geminiKeyMinterSecret
	default:
		used := map[string]bool{}
		for _, item := range rawAdmins {
			m, _ := item.(map[string]any)
			name, _ := m["name"].(string)
			used[name] = true
			env, _ := m["credential_env"].(string)
			source, _ := m["credential_source"].(string)
			bound := env != "" && entry.Env[env] == geminiKeyMinterSecret
			// gcpsets gives a configured "default" the top-level
			// admin_credential when it names nothing of its own.
			inherits := env == "" && name == "default" && source == "configured" && cred == geminiKeyMinterSecret
			if admin == "" && (bound || inherits) {
				admin = name
			}
		}
		if admin != "" {
			break
		}
		admins := append([]any(nil), rawAdmins...)
		if len(rawAdmins) == 1 && !used["default"] {
			// gcpsets gives a set naming no admin the sole one, and once
			// there are two, "default" -- which this entry has none of --
			// so the sets here name the one they resolved to.
			sole, _ := rawAdmins[0].(map[string]any)
			name, _ := sole["name"].(string)
			for _, item := range sets {
				if set, ok := item.(map[string]any); ok {
					if a, _ := set["admin"].(string); a == "" {
						set["admin"] = name
					}
				}
			}
		}
		if !hasAdmins {
			// The top-level admin as gcpsets synthesizes it, so the sets
			// already here keep it once there are two admins.
			if src == "" {
				src = "ambient"
			}
			if src == "ambient" && cred != "" {
				src = "configured"
			}
			admins = append(admins, map[string]any{"name": "default", "credential_source": src})
			used["default"] = true
		}
		admin = uniqueMigratedAdminName(strings.TrimPrefix(geminiKeyMinterSecret, "$secret:"), used)
		env := migratedMinterEnvVar(admin)
		if entry.Env == nil {
			entry.Env = map[string]string{}
		}
		entry.Env[env] = geminiKeyMinterSecret
		admins = append(admins, map[string]any{
			"name":              admin,
			"credential_source": "configured",
			"credential_env":    env,
		})
		entry.Config["admins"] = admins
	}

	gemini := map[string]any{"project": project}
	if admin != "" {
		gemini["admin"] = admin
	}
	entry.Config["gemini_key"] = gemini
	if n := strings.TrimSpace(entry.Notice); n == "" {
		entry.Notice = geminiKeyMigrationNotice
	} else if !strings.Contains(n, geminiKeyMigrationNotice) {
		entry.Notice = n + " " + geminiKeyMigrationNotice
	}
	servers[GCPCapabilityServerID] = entry
	out, err := json.Marshal(servers)
	if err != nil {
		return "", false, fmt.Errorf("encoding mcp_servers: %w", err)
	}
	return string(out), true, nil
}
