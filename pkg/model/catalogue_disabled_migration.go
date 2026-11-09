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
	"log"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

// CatalogueDisabledGrantMigrationNote formats the note recorded on a template
// or schedule when an upgrade drops a grant that the deployment's capability
// catalogue disables in settings/capabilities.yml.
func CatalogueDisabledGrantMigrationNote(grant, reason string) string {
	return fmt.Sprintf(
		"grain: capability catalogue migration dropped grant %s (%s); "+
			"re-enable the capability in settings/capabilities.yml before re-attaching it.",
		grant, reason,
	)
}

// EffectiveCatalogueForDisabledCheck returns a shallow copy of cat with
// grain_config.mcp_servers' Disabled and Aliases fields merged into Servers,
// matching what staterepo.ExportConfig writes into settings/capabilities.yml.
func EffectiveCatalogueForDisabledCheck(cat *CapabilityCatalogue, mcpServersJSON string) *CapabilityCatalogue {
	if cat == nil {
		return nil
	}
	var mcpServers map[string]protocol.ServerConfig
	if strings.TrimSpace(mcpServersJSON) != "" {
		_ = json.Unmarshal([]byte(mcpServersJSON), &mcpServers)
	}
	servers := make([]CatalogueServer, 0, len(cat.Servers)+len(mcpServers))
	seen := make(map[string]bool, len(cat.Servers))
	for _, s := range cat.Servers {
		id := strings.TrimSpace(s.ID)
		if srvCfg, ok := mcpServers[id]; ok && id != "" {
			enabled := !srvCfg.Disabled && (s.Enabled == nil || *s.Enabled)
			s.Enabled = &enabled
			if len(srvCfg.Aliases) > 0 {
				s.Aliases = srvCfg.Aliases
			}
		}
		if id != "" {
			seen[id] = true
		}
		servers = append(servers, s)
	}
	var extraIDs []string
	for id := range mcpServers {
		if !seen[id] {
			extraIDs = append(extraIDs, id)
		}
	}
	sort.Strings(extraIDs)
	for _, id := range extraIDs {
		srvCfg := mcpServers[id]
		enabled := !srvCfg.Disabled
		servers = append(servers, CatalogueServer{
			ID:      id,
			Enabled: &enabled,
			Aliases: srvCfg.Aliases,
		})
	}
	cp := *cat
	cp.Servers = servers
	return &cp
}

// DropCatalogueDisabledGrantsFromSlice drops every grant in items that cat
// disables (CapabilityCatalogue.DisabledReason), reporting each dropped grant
// to onDrop in order.
func DropCatalogueDisabledGrantsFromSlice(items []string, cat *CapabilityCatalogue, onDrop func(grant, reason string)) ([]string, bool) {
	if len(items) == 0 || cat == nil {
		return items, false
	}
	changed := false
	out := make([]string, 0, len(items))
	for _, item := range items {
		if reason, disabled := cat.DisabledReason(item); disabled {
			changed = true
			if onDrop != nil {
				onDrop(strings.TrimSpace(item), reason)
			}
			continue
		}
		out = append(out, item)
	}
	if !changed {
		return items, false
	}
	return out, true
}

// DropCatalogueDisabledFromDefaults drops grants that effective disables from
// cat.Defaults (Task, CI, and per-repository Task and CI).
func DropCatalogueDisabledFromDefaults(cat, effective *CapabilityCatalogue, onDrop func(where, grant, reason string)) bool {
	if cat == nil || effective == nil {
		return false
	}
	changed := false
	report := func(where string) func(string, string) {
		if onDrop == nil {
			return nil
		}
		return func(grant, reason string) { onDrop(where, grant, reason) }
	}
	if next, c := DropCatalogueDisabledGrantsFromSlice(cat.Defaults.Task, effective, report("capability_policy.catalogue.defaults.task")); c {
		cat.Defaults.Task = next
		changed = true
	}
	if next, c := DropCatalogueDisabledGrantsFromSlice(cat.Defaults.CI, effective, report("capability_policy.catalogue.defaults.ci")); c {
		cat.Defaults.CI = next
		changed = true
	}
	if len(cat.Defaults.Repos) > 0 {
		for _, repo := range sortedRepoDefaults(cat.Defaults.Repos) {
			rd := cat.Defaults.Repos[repo]
			rc := false
			if next, c := DropCatalogueDisabledGrantsFromSlice(rd.Task, effective, report("capability_policy.catalogue.defaults.repos["+repo+"].task")); c {
				rd.Task = next
				rc = true
			}
			if next, c := DropCatalogueDisabledGrantsFromSlice(rd.CI, effective, report("capability_policy.catalogue.defaults.repos["+repo+"].ci")); c {
				rd.CI = next
				rc = true
			}
			if rc {
				if len(rd.Task) == 0 && len(rd.CI) == 0 {
					delete(cat.Defaults.Repos, repo)
				} else {
					cat.Defaults.Repos[repo] = rd
				}
				changed = true
			}
		}
		if len(cat.Defaults.Repos) == 0 {
			cat.Defaults.Repos = nil
		}
	}
	return changed
}

// MigrateCatalogueDisabledGrants drops stored default, template and schedule
// grants that the store's own capability catalogue disables, logging each drop
// and recording a migration note on affected templates and schedules. Stored
// task grants, console sessions and policy rules are left untouched: dispatch
// holds a task whose grant is disabled (orchestrator.catalogueHold), while
// grain/config export checks only defaults, templates and schedules
// (staterepo.checkCapabilities).
//
// Earlier builds accepted capability policy saves without checking existing
// deployment/repository defaults, templates or schedules (2026-11-04 F2,
// 2026-11-07 O1), leaving stores that migrated in place cleanly while their own
// state-repository export failed grain state check and prepare-upgrade.
func MigrateCatalogueDisabledGrants(ctx context.Context, tx *sql.Tx) error {
	type cfgRow struct {
		id               int64
		mcpServers       string
		defaultCaps      string
		capabilityPolicy string
	}
	cfgRows, err := tx.QueryContext(ctx,
		"SELECT `id`, `mcp_servers`, `default_capabilities`, `capability_policy` FROM `grain_config` ORDER BY `id`")
	if err != nil {
		return fmt.Errorf("grain_config: reading rows for catalogue disabled-grants migration: %w", err)
	}
	var configs []cfgRow
	for cfgRows.Next() {
		var r cfgRow
		if err := cfgRows.Scan(&r.id, &r.mcpServers, &r.defaultCaps, &r.capabilityPolicy); err != nil {
			cfgRows.Close()
			return fmt.Errorf("grain_config: scanning row for catalogue disabled-grants migration: %w", err)
		}
		configs = append(configs, r)
	}
	if err := cfgRows.Err(); err != nil {
		cfgRows.Close()
		return fmt.Errorf("grain_config: iterating rows for catalogue disabled-grants migration: %w", err)
	}
	cfgRows.Close()

	var effective *CapabilityCatalogue
	for _, cfg := range configs {
		if strings.TrimSpace(cfg.capabilityPolicy) == "" {
			continue
		}
		pol, err := DecodeCapabilityPolicy(cfg.capabilityPolicy)
		if err != nil || pol.Catalogue == nil {
			continue
		}
		effective = EffectiveCatalogueForDisabledCheck(pol.Catalogue, cfg.mcpServers)
		if cfg.id == 1 {
			break
		}
	}
	if effective == nil {
		return nil
	}

	logDrop := func(where, grant, reason string) {
		log.Printf("capability catalogue migration: %s: dropped disabled grant %q (%s)", where, grant, reason)
	}

	for _, cfg := range configs {
		rowEffective := effective
		var pol CapabilityPolicy
		hasCatalogue := false
		if strings.TrimSpace(cfg.capabilityPolicy) != "" {
			if decoded, err := DecodeCapabilityPolicy(cfg.capabilityPolicy); err == nil && decoded.Catalogue != nil {
				pol = decoded
				hasCatalogue = true
				rowEffective = EffectiveCatalogueForDisabledCheck(pol.Catalogue, cfg.mcpServers)
			}
		}
		if updated, c := dropEncodedCapabilityList(cfg.defaultCaps, rowEffective, func(grant, reason string) {
			logDrop("grain_config.default_capabilities", grant, reason)
		}); c {
			if _, err := tx.ExecContext(ctx,
				"UPDATE `grain_config` SET `default_capabilities` = ? WHERE `id` = ?",
				updated, cfg.id); err != nil {
				return fmt.Errorf("grain_config: updating default_capabilities: %w", err)
			}
		}
		if hasCatalogue && DropCatalogueDisabledFromDefaults(pol.Catalogue, rowEffective, logDrop) {
			encoded, err := EncodeCapabilityPolicy(pol)
			if err != nil {
				return fmt.Errorf("grain_config: encoding capability_policy: %w", err)
			}
			if _, err := tx.ExecContext(ctx,
				"UPDATE `grain_config` SET `capability_policy` = ? WHERE `id` = ?",
				encoded, cfg.id); err != nil {
				return fmt.Errorf("grain_config: updating capability_policy: %w", err)
			}
		}
	}

	type repoRow struct {
		owner, name, defCaps, ciCaps string
	}
	rRows, err := tx.QueryContext(ctx,
		"SELECT `owner`, `name`, `default_capabilities`, `ci_capabilities` FROM `repo_config` ORDER BY `owner`, `name`")
	if err != nil {
		return fmt.Errorf("repo_config: reading rows for catalogue disabled-grants migration: %w", err)
	}
	var repos []repoRow
	for rRows.Next() {
		var r repoRow
		if err := rRows.Scan(&r.owner, &r.name, &r.defCaps, &r.ciCaps); err != nil {
			rRows.Close()
			return fmt.Errorf("repo_config: scanning row: %w", err)
		}
		repos = append(repos, r)
	}
	if err := rRows.Err(); err != nil {
		rRows.Close()
		return fmt.Errorf("repo_config: iterating rows: %w", err)
	}
	rRows.Close()

	for _, r := range repos {
		repoLabel := r.owner + "/" + r.name
		newDef, cDef := dropEncodedCapabilityList(r.defCaps, effective, func(grant, reason string) {
			logDrop("repo_config "+repoLabel+" default_capabilities", grant, reason)
		})
		newCI, cCI := dropEncodedCapabilityList(r.ciCaps, effective, func(grant, reason string) {
			logDrop("repo_config "+repoLabel+" ci_capabilities", grant, reason)
		})
		if cDef || cCI {
			if _, err := tx.ExecContext(ctx,
				"UPDATE `repo_config` SET `default_capabilities` = ?, `ci_capabilities` = ? WHERE `owner` = ? AND `name` = ?",
				newDef, newCI, r.owner, r.name); err != nil {
				return fmt.Errorf("repo_config %s: updating capabilities: %w", repoLabel, err)
			}
		}
	}

	for _, tbl := range []struct {
		grantTable string
		ownerCol   string
		ownerKind  string
	}{
		{"template_grant", "template_id", "template"},
		{"schedule_grant", "schedule_id", "schedule"},
	} {
		if err := dropOwnerDisabledGrants(ctx, tx, tbl.grantTable, tbl.ownerCol, tbl.ownerKind, effective, logDrop); err != nil {
			return err
		}
	}
	return nil
}

func dropEncodedCapabilityList(raw string, cat *CapabilityCatalogue, onDrop func(grant, reason string)) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || cat == nil {
		return raw, false
	}
	items := splitCapabilities(trimmed)
	next, changed := DropCatalogueDisabledGrantsFromSlice(items, cat, onDrop)
	if !changed {
		return raw, false
	}
	if strings.HasPrefix(trimmed, "[") {
		if next == nil {
			next = []string{}
		}
		data, err := json.Marshal(next)
		if err != nil {
			return joinCapabilities(next), true
		}
		return string(data), true
	}
	return joinCapabilities(next), true
}

func dropOwnerDisabledGrants(
	ctx context.Context,
	tx *sql.Tx,
	grantTable, ownerCol, ownerKind string,
	cat *CapabilityCatalogue,
	logDrop func(where, grant, reason string),
) error {
	type grantRow struct {
		ownerID, capability string
	}
	rows, err := tx.QueryContext(ctx,
		"SELECT `"+ownerCol+"`, `capability` FROM `"+grantTable+"` ORDER BY `"+ownerCol+"`, `capability`")
	if err != nil {
		return fmt.Errorf("%s: reading grants: %w", grantTable, err)
	}
	var all []grantRow
	for rows.Next() {
		var r grantRow
		if err := rows.Scan(&r.ownerID, &r.capability); err != nil {
			rows.Close()
			return fmt.Errorf("%s: scanning grant: %w", grantTable, err)
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("%s: iterating grants: %w", grantTable, err)
	}
	rows.Close()

	now := time.Now().UTC()
	for _, r := range all {
		reason, disabled := cat.DisabledReason(r.capability)
		if !disabled {
			continue
		}
		logDrop(ownerKind+" "+r.ownerID, r.capability, reason)
		note := CatalogueDisabledGrantMigrationNote(r.capability, reason)
		if err := recordOwnerMigrationNote(ctx, tx, ownerKind, r.ownerID, note, now); err != nil {
			return fmt.Errorf("%s %s: recording migration note: %w", ownerKind, r.ownerID, err)
		}
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM `"+grantTable+"` WHERE `"+ownerCol+"` = ? AND `capability` = ?",
			r.ownerID, r.capability); err != nil {
			return fmt.Errorf("%s: deleting disabled grant on %s: %w", grantTable, r.ownerID, err)
		}
	}
	return nil
}
