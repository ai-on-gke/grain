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
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

// GCPKeyMissingSetMigrationNote formats the note recorded on a task, template
// or schedule when a gcp#gcp-key grant carried an "account" value that matches
// no configured permission set on https://grain.dev/capabilities/gcp. The
// grant is still rewritten (to rewritten, naming that set as its
// permission_set) so the state repository can load it; the note says the set
// it names has to be configured before the grant can mint anything.
func GCPKeyMissingSetMigrationNote(grant, rewritten, missingSet string) string {
	return fmt.Sprintf(
		"grain: gcp#gcp-key migration rewrote grant %s to %s, but permission set %q is not configured on %s; "+
			"configure that permission set or change the grant's \"permission_set\".",
		grant, rewritten, missingSet, GCPCapabilityServerID,
	)
}

// GCPPermissionSetsFromMCPServers extracts the ordered permission set names
// configured on the built-in gcp capability server (and any server IDs/aliases
// bound to it) from grain_config.mcp_servers JSON.
func GCPPermissionSetsFromMCPServers(mcpServersJSON string) (sets []string, serverIDs map[string]bool) {
	serverIDs = map[string]bool{
		GCPCapabilityServerID: true,
	}
	if strings.TrimSpace(mcpServersJSON) == "" {
		return nil, serverIDs
	}
	var servers map[string]protocol.ServerConfig
	if err := json.Unmarshal([]byte(mcpServersJSON), &servers); err != nil || len(servers) == 0 {
		return nil, serverIDs
	}
	ids := make([]string, 0, len(servers))
	if _, hasCanonical := servers[GCPCapabilityServerID]; hasCanonical {
		ids = append(ids, GCPCapabilityServerID)
	}
	var rest []string
	for id := range servers {
		if id != GCPCapabilityServerID {
			rest = append(rest, id)
		}
	}
	sort.Strings(rest)
	ids = append(ids, rest...)

	addSet := func(name string) {
		name = strings.TrimSpace(name)
		if name != "" && !slices.Contains(sets, name) {
			sets = append(sets, name)
		}
	}
	for _, id := range ids {
		entry := servers[id]
		if !isBuiltInGCPServerEntry(id, entry) {
			continue
		}
		serverIDs[id] = true
		for _, alias := range entry.Aliases {
			if a := strings.TrimSpace(alias); a != "" {
				serverIDs[a] = true
			}
		}
		var entrySets []string
		if rawSets, ok := entry.Config["permission_sets"].([]any); ok {
			for _, item := range rawSets {
				if m, ok := item.(map[string]any); ok {
					if n, _ := m["name"].(string); strings.TrimSpace(n) != "" {
						entrySets = append(entrySets, strings.TrimSpace(n))
					}
				}
			}
		}
		if len(entrySets) == 0 {
			for _, rawAcct := range legacyGCPServerAccounts(entry) {
				if n := legacyGCPAccountSetName(rawAcct); n != "" {
					entrySets = append(entrySets, n)
				}
			}
		}
		for _, n := range entrySets {
			addSet(n)
		}
	}
	return sets, serverIDs
}

func isBuiltInGCPServerEntry(id string, entry protocol.ServerConfig) bool {
	if id == GCPCapabilityServerID || strings.TrimSpace(entry.Builtin) == "gcp" {
		return true
	}
	if entry.OnHost() && entry.Command == "grain-gcp" {
		return true
	}
	if entry.Stdio() && filepath.Base(entry.Command) == "grain" && len(entry.Args) >= 2 &&
		entry.Args[0] == "capability" && entry.Args[1] == "gcp" {
		return true
	}
	return false
}

func legacyGCPServerAccounts(entry protocol.ServerConfig) []string {
	var out []string
	if rawList, ok := entry.Config["accounts"].([]any); ok {
		for _, v := range rawList {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	}
	for i := 0; i < len(entry.Args); i++ {
		arg := entry.Args[i]
		switch {
		case arg == "-account" || arg == "--account":
			if i+1 < len(entry.Args) {
				out = append(out, entry.Args[i+1])
				i++
			}
		case strings.HasPrefix(arg, "-account="):
			out = append(out, strings.TrimPrefix(arg, "-account="))
		case strings.HasPrefix(arg, "--account="):
			out = append(out, strings.TrimPrefix(arg, "--account="))
		}
	}
	return out
}

func legacyGCPAccountSetName(spec string) string {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return ""
	}
	name := ""
	if before, _, hasEq := strings.Cut(spec, "="); hasEq {
		name = strings.TrimSpace(before)
	}
	if name == "" || name == "default" {
		return GCPKeyDefaultPermissionSet
	}
	return name
}

// RewriteGCPKeyGrant rewrites a gcp#gcp-key grant carrying deprecated
// "account" or "activate" arguments:
//   - account: X -> permission_set: X ("default" resolves to configuredSets[0]
//     unless a set literally named "default" is configured);
//   - activate is dropped;
//   - if X (or "default" when configuredSets is empty) matches no configured
//     permission set, the grant is still rewritten to permission_set: X and
//     missingSet is returned non-empty, so the caller records a migration
//     note. Keeping "account" would leave a grant that nothing loads: every
//     validation of the state repository refuses it (CheckGCPKeyGrantArgs).
func RewriteGCPKeyGrant(grant string, configuredSets []string, gcpServerIDs map[string]bool) (rewritten string, changed bool, missingSet string) {
	s := strings.TrimSpace(grant)
	if !IsCapabilityRef(s) {
		return grant, false, ""
	}
	spec, err := CapabilityGrantSpec(s)
	if err != nil || len(spec.With) == 0 {
		return grant, false, ""
	}
	ref, err := protocol.ParseRef(spec.Capability)
	if err != nil {
		return grant, false, ""
	}
	isGCP := ref.CapabilityID == GCPCapabilityServerID || (gcpServerIDs != nil && gcpServerIDs[ref.CapabilityID])
	if !isGCP || (ref.Method != "" && ref.Method != GCPKeyMethod) {
		return grant, false, ""
	}
	rawAcct, hasAcct := spec.With["account"]
	_, hasAct := spec.With["activate"]
	if !hasAcct && !hasAct {
		return grant, false, ""
	}

	newWith := make(map[string]any, len(spec.With))
	for k, v := range spec.With {
		if k == "account" || k == "activate" {
			continue
		}
		newWith[k] = v
	}

	if hasAcct && rawAcct != nil {
		acctStr, ok := rawAcct.(string)
		if !ok {
			acctStr = fmt.Sprint(rawAcct)
		}
		acct := strings.TrimSpace(acctStr)
		if acct != "" {
			target := acct
			if acct == "default" && !slices.Contains(configuredSets, "default") && len(configuredSets) > 0 {
				target = configuredSets[0]
			}
			if !slices.Contains(configuredSets, target) {
				missingSet = target
			}
			newWith["permission_set"] = target
		}
	}

	if setVal, ok := newWith["permission_set"].(string); ok && strings.TrimSpace(setVal) != "" {
		if _, hasPrimary := newWith["primary"]; !hasPrimary {
			newWith["primary"] = true
		}
	}

	outRef := ref
	if outRef.Method == "" {
		outRef.Method = GCPKeyMethod
	}
	data, err := json.Marshal(newWith)
	if err != nil {
		return grant, false, ""
	}
	out := outRef.String() + "=" + string(data)
	return out, out != grant, missingSet
}

// RewriteGCPKeyGrantSlice rewrites a slice of capability grant strings in
// order, deduplicating equal grants and reporting each grant rewritten to a
// permission set that is not configured via onMissing.
func RewriteGCPKeyGrantSlice(items []string, configuredSets []string, gcpServerIDs map[string]bool, onMissing func(grant, missingSet string)) ([]string, bool) {
	if len(items) == 0 {
		return items, false
	}
	changed := false
	seen := make(map[string]bool, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		next, c, missing := RewriteGCPKeyGrant(item, configuredSets, gcpServerIDs)
		if missing != "" && onMissing != nil {
			onMissing(item, missing)
		}
		if c {
			changed = true
		}
		if seen[next] {
			changed = true
			continue
		}
		seen[next] = true
		out = append(out, next)
	}
	return out, changed
}

// RewriteGCPKeyCatalogue rewrites deprecated gcp#gcp-key arguments across a
// CapabilityCatalogue's defaults and server method restrictions.
func RewriteGCPKeyCatalogue(cat *CapabilityCatalogue, configuredSets []string, gcpServerIDs map[string]bool, onMissing func(where, grant, missingSet string)) bool {
	if cat == nil {
		return false
	}
	changed := false
	report := func(where string) func(string, string) {
		if onMissing == nil {
			return nil
		}
		return func(grant, missing string) { onMissing(where, grant, missing) }
	}
	if next, c := RewriteGCPKeyGrantSlice(cat.Defaults.Task, configuredSets, gcpServerIDs, report("capability_policy.catalogue.defaults.task")); c {
		cat.Defaults.Task = next
		changed = true
	}
	if next, c := RewriteGCPKeyGrantSlice(cat.Defaults.CI, configuredSets, gcpServerIDs, report("capability_policy.catalogue.defaults.ci")); c {
		cat.Defaults.CI = next
		changed = true
	}
	if len(cat.Defaults.Repos) > 0 {
		for _, repo := range sortedRepoDefaults(cat.Defaults.Repos) {
			rd := cat.Defaults.Repos[repo]
			rc := false
			if next, c := RewriteGCPKeyGrantSlice(rd.Task, configuredSets, gcpServerIDs, report("capability_policy.catalogue.defaults.repos["+repo+"].task")); c {
				rd.Task = next
				rc = true
			}
			if next, c := RewriteGCPKeyGrantSlice(rd.CI, configuredSets, gcpServerIDs, report("capability_policy.catalogue.defaults.repos["+repo+"].ci")); c {
				rd.CI = next
				rc = true
			}
			if rc {
				cat.Defaults.Repos[repo] = rd
				changed = true
			}
		}
	}
	mapSetValues := func(vals []string) []string {
		out := make([]string, 0, len(vals))
		for _, v := range vals {
			if v == "default" && !slices.Contains(configuredSets, "default") && len(configuredSets) > 0 {
				v = configuredSets[0]
			}
			if !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
		return out
	}
	for i := range cat.Servers {
		srv := &cat.Servers[i]
		id := strings.TrimSpace(srv.ID)
		if id != GCPCapabilityServerID && (gcpServerIDs == nil || !gcpServerIDs[id]) {
			continue
		}
		for j := range srv.Methods {
			m := &srv.Methods[j]
			if m.Name != GCPKeyMethod {
				continue
			}
			if len(m.Args) > 0 {
				if acctArg, has := m.Args["account"]; has {
					if _, hasPerm := m.Args["permission_set"]; !hasPerm {
						m.Args["permission_set"] = CatalogueArg{Allowed: mapSetValues(acctArg.Allowed)}
					}
					delete(m.Args, "account")
					changed = true
				}
				if _, has := m.Args["activate"]; has {
					delete(m.Args, "activate")
					changed = true
				}
				if len(m.Args) == 0 {
					m.Args = nil
				}
			}
			if m.Restrictions != nil && len(m.Restrictions.RequiresPersonWhen) > 0 {
				if acctVals, has := m.Restrictions.RequiresPersonWhen["account"]; has {
					merged := append([]string(nil), m.Restrictions.RequiresPersonWhen["permission_set"]...)
					for _, v := range mapSetValues(acctVals) {
						if !slices.Contains(merged, v) {
							merged = append(merged, v)
						}
					}
					m.Restrictions.RequiresPersonWhen["permission_set"] = merged
					delete(m.Restrictions.RequiresPersonWhen, "account")
					changed = true
				}
				if _, has := m.Restrictions.RequiresPersonWhen["activate"]; has {
					delete(m.Restrictions.RequiresPersonWhen, "activate")
					changed = true
				}
				if len(m.Restrictions.RequiresPersonWhen) == 0 {
					m.Restrictions.RequiresPersonWhen = nil
				}
			}
		}
	}
	return changed
}

// MigrateGCPKeyGrantArgs is migration 109: rewrites stored gcp#gcp-key grants
// carrying deprecated "account" or "activate" arguments across task_grant,
// task_grant_wait, template_grant, schedule_grant, repo_config, grain_config
// (default_capabilities, console_sessions, capability_policy) and
// persistent_sandbox. Grants whose "account" names an unconfigured permission
// set are rewritten to that set all the same, logged, and noted on their
// task, template or schedule. Migration 127 runs it again, for stores that
// ran 109 when it still kept those grants as written.
func MigrateGCPKeyGrantArgs(ctx context.Context, tx *sql.Tx) error {
	type cfgRow struct {
		id               int64
		mcpServers       string
		defaultCaps      string
		consoleSessions  string
		capabilityPolicy string
	}
	var configs []cfgRow
	cfgRows, err := tx.QueryContext(ctx,
		"SELECT `id`, `mcp_servers`, `default_capabilities`, `console_sessions`, `capability_policy` FROM `grain_config` ORDER BY `id`")
	if err != nil {
		return fmt.Errorf("grain_config: reading rows for gcp#gcp-key args migration: %w", err)
	}
	for cfgRows.Next() {
		var r cfgRow
		if err := cfgRows.Scan(&r.id, &r.mcpServers, &r.defaultCaps, &r.consoleSessions, &r.capabilityPolicy); err != nil {
			cfgRows.Close()
			return fmt.Errorf("grain_config: scanning row for gcp#gcp-key args migration: %w", err)
		}
		configs = append(configs, r)
	}
	if err := cfgRows.Err(); err != nil {
		cfgRows.Close()
		return fmt.Errorf("grain_config: iterating rows for gcp#gcp-key args migration: %w", err)
	}
	cfgRows.Close()

	configuredSets := []string(nil)
	gcpServerIDs := map[string]bool{GCPCapabilityServerID: true}
	for _, cfg := range configs {
		sets, ids := GCPPermissionSetsFromMCPServers(cfg.mcpServers)
		for _, s := range sets {
			if !slices.Contains(configuredSets, s) {
				configuredSets = append(configuredSets, s)
			}
		}
		for id := range ids {
			gcpServerIDs[id] = true
		}
	}

	logMissing := func(where, grant, missing string) {
		log.Printf("gcp#gcp-key migration: %s: rewrote grant %q to permission_set %q, which is not configured on %s",
			where, grant, missing, GCPCapabilityServerID)
	}

	for _, cfg := range configs {
		if updated, c := rewriteEncodedCapabilityList(cfg.defaultCaps, configuredSets, gcpServerIDs, func(grant, missing string) {
			logMissing("grain_config.default_capabilities", grant, missing)
		}); c {
			if _, err := tx.ExecContext(ctx,
				"UPDATE `grain_config` SET `default_capabilities` = ? WHERE `id` = ?",
				updated, cfg.id); err != nil {
				return fmt.Errorf("grain_config: updating default_capabilities: %w", err)
			}
		}

		if strings.TrimSpace(cfg.consoleSessions) != "" {
			var sessions []ConsoleSession
			if err := json.Unmarshal([]byte(cfg.consoleSessions), &sessions); err == nil {
				sessChanged := false
				for i := range sessions {
					sid := sessions[i].ID
					next, c := RewriteGCPKeyGrantSlice(sessions[i].Capabilities, configuredSets, gcpServerIDs, func(grant, missing string) {
						logMissing("console_session "+sid, grant, missing)
					})
					if c {
						sessions[i].Capabilities = next
						sessChanged = true
					}
				}
				if sessChanged {
					encoded, err := EncodeConsoleSessions(sessions)
					if err != nil {
						return fmt.Errorf("grain_config: encoding console_sessions: %w", err)
					}
					if _, err := tx.ExecContext(ctx,
						"UPDATE `grain_config` SET `console_sessions` = ? WHERE `id` = ?",
						encoded, cfg.id); err != nil {
						return fmt.Errorf("grain_config: updating console_sessions: %w", err)
					}
				}
			}
		}

		if strings.TrimSpace(cfg.capabilityPolicy) != "" {
			var pol CapabilityPolicy
			if err := json.Unmarshal([]byte(cfg.capabilityPolicy), &pol); err == nil && pol.Catalogue != nil {
				if RewriteGCPKeyCatalogue(pol.Catalogue, configuredSets, gcpServerIDs, logMissing) {
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
		}
	}

	// Rewrite repo_config.default_capabilities and repo_config.ci_capabilities.
	type repoRow struct {
		owner, name, defCaps, ciCaps string
	}
	var repos []repoRow
	rRows, err := tx.QueryContext(ctx,
		"SELECT `owner`, `name`, `default_capabilities`, `ci_capabilities` FROM `repo_config` ORDER BY `owner`, `name`")
	if err != nil {
		return fmt.Errorf("repo_config: reading rows for gcp#gcp-key args migration: %w", err)
	}
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
		newDef, cDef := rewriteEncodedCapabilityList(r.defCaps, configuredSets, gcpServerIDs, func(grant, missing string) {
			logMissing("repo_config "+repoLabel+" default_capabilities", grant, missing)
		})
		newCI, cCI := rewriteEncodedCapabilityList(r.ciCaps, configuredSets, gcpServerIDs, func(grant, missing string) {
			logMissing("repo_config "+repoLabel+" ci_capabilities", grant, missing)
		})
		if cDef || cCI {
			if _, err := tx.ExecContext(ctx,
				"UPDATE `repo_config` SET `default_capabilities` = ?, `ci_capabilities` = ? WHERE `owner` = ? AND `name` = ?",
				newDef, newCI, r.owner, r.name); err != nil {
				return fmt.Errorf("repo_config %s: updating capabilities: %w", repoLabel, err)
			}
		}
	}

	// Rewrite persistent_sandbox.capabilities.
	type sbRow struct {
		name, caps string
	}
	var sandboxes []sbRow
	sbRows, err := tx.QueryContext(ctx, "SELECT `name`, `capabilities` FROM `persistent_sandbox` ORDER BY `name`")
	if err != nil {
		return fmt.Errorf("persistent_sandbox: reading rows for gcp#gcp-key args migration: %w", err)
	}
	for sbRows.Next() {
		var r sbRow
		if err := sbRows.Scan(&r.name, &r.caps); err != nil {
			sbRows.Close()
			return fmt.Errorf("persistent_sandbox: scanning row: %w", err)
		}
		sandboxes = append(sandboxes, r)
	}
	if err := sbRows.Err(); err != nil {
		sbRows.Close()
		return fmt.Errorf("persistent_sandbox: iterating rows: %w", err)
	}
	sbRows.Close()

	for _, sb := range sandboxes {
		if updated, c := rewriteEncodedCapabilityList(sb.caps, configuredSets, gcpServerIDs, func(grant, missing string) {
			logMissing("persistent_sandbox "+sb.name, grant, missing)
		}); c {
			if _, err := tx.ExecContext(ctx,
				"UPDATE `persistent_sandbox` SET `capabilities` = ? WHERE `name` = ?",
				updated, sb.name); err != nil {
				return fmt.Errorf("persistent_sandbox %s: updating capabilities: %w", sb.name, err)
			}
		}
	}

	// Rewrite task_grant_wait.capability where applicable.
	type waitRow struct {
		taskID, cap string
	}
	var waits []waitRow
	wRows, err := tx.QueryContext(ctx, "SELECT `task_id`, `capability` FROM `task_grant_wait` ORDER BY `task_id`")
	if err != nil {
		return fmt.Errorf("task_grant_wait: reading rows: %w", err)
	}
	for wRows.Next() {
		var r waitRow
		if err := wRows.Scan(&r.taskID, &r.cap); err != nil {
			wRows.Close()
			return fmt.Errorf("task_grant_wait: scanning row: %w", err)
		}
		waits = append(waits, r)
	}
	if err := wRows.Err(); err != nil {
		wRows.Close()
		return fmt.Errorf("task_grant_wait: iterating rows: %w", err)
	}
	wRows.Close()
	for _, w := range waits {
		if next, c, _ := RewriteGCPKeyGrant(w.cap, configuredSets, gcpServerIDs); c {
			if _, err := tx.ExecContext(ctx,
				"UPDATE `task_grant_wait` SET `capability` = ? WHERE `task_id` = ?",
				next, w.taskID); err != nil {
				return fmt.Errorf("task_grant_wait %s: updating capability: %w", w.taskID, err)
			}
		}
	}

	// Rewrite task_grant, template_grant and schedule_grant, leaving notes on
	// owners whose grants now name an unconfigured permission set.
	for _, tbl := range []struct {
		grantTable string
		ownerCol   string
		ownerKind  string
	}{
		{"task_grant", "task_id", "task"},
		{"template_grant", "template_id", "template"},
		{"schedule_grant", "schedule_id", "schedule"},
	} {
		if err := migrateOwnerGrantTableGCPKeyArgs(ctx, tx, tbl.grantTable, tbl.ownerCol, tbl.ownerKind, configuredSets, gcpServerIDs, logMissing); err != nil {
			return err
		}
	}
	return nil
}

func rewriteEncodedCapabilityList(raw string, configuredSets []string, gcpServerIDs map[string]bool, onMissing func(grant, missing string)) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return raw, false
	}
	items := splitCapabilities(trimmed)
	next, changed := RewriteGCPKeyGrantSlice(items, configuredSets, gcpServerIDs, onMissing)
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

func migrateOwnerGrantTableGCPKeyArgs(
	ctx context.Context,
	tx *sql.Tx,
	grantTable, ownerCol, ownerKind string,
	configuredSets []string,
	gcpServerIDs map[string]bool,
	logMissing func(where, grant, missing string),
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
	present := map[[2]string]bool{}
	for rows.Next() {
		var r grantRow
		if err := rows.Scan(&r.ownerID, &r.capability); err != nil {
			rows.Close()
			return fmt.Errorf("%s: scanning grant: %w", grantTable, err)
		}
		all = append(all, r)
		present[[2]string{r.ownerID, r.capability}] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("%s: iterating grants: %w", grantTable, err)
	}
	rows.Close()

	now := time.Now().UTC()
	for _, r := range all {
		next, changed, missing := RewriteGCPKeyGrant(r.capability, configuredSets, gcpServerIDs)
		if missing != "" {
			logMissing(ownerKind+" "+r.ownerID, r.capability, missing)
			note := GCPKeyMissingSetMigrationNote(r.capability, next, missing)
			if err := recordOwnerMigrationNote(ctx, tx, ownerKind, r.ownerID, note, now); err != nil {
				return fmt.Errorf("%s %s: recording migration note: %w", ownerKind, r.ownerID, err)
			}
		}
		if !changed {
			continue
		}
		delete(present, [2]string{r.ownerID, r.capability})
		if present[[2]string{r.ownerID, next}] {
			if _, err := tx.ExecContext(ctx,
				"DELETE FROM `"+grantTable+"` WHERE `"+ownerCol+"` = ? AND `capability` = ?",
				r.ownerID, r.capability); err != nil {
				return fmt.Errorf("%s: deleting duplicate grant on %s: %w", grantTable, r.ownerID, err)
			}
			continue
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE `"+grantTable+"` SET `capability` = ? WHERE `"+ownerCol+"` = ? AND `capability` = ?",
			next, r.ownerID, r.capability); err != nil {
			return fmt.Errorf("%s: rewriting grant on %s: %w", grantTable, r.ownerID, err)
		}
		present[[2]string{r.ownerID, next}] = true
	}
	return nil
}

func recordOwnerMigrationNote(ctx context.Context, tx *sql.Tx, ownerKind, ownerID, note string, now time.Time) error {
	if ownerKind == "task" {
		var exists int
		err := tx.QueryRowContext(ctx,
			"SELECT 1 FROM `task_comment` WHERE `task_id` = ? AND `body` = ? LIMIT 1",
			ownerID, note).Scan(&exists)
		if err == nil {
			return nil
		}
		if err != sql.ErrNoRows {
			return err
		}
		_, err = tx.ExecContext(ctx,
			"INSERT INTO `task_comment` (`task_id`, `author_kind`, `author_id`, `author_behalf_kind`, `author_behalf_id`, "+
				"`body`, `created_at`, `handover`, `summary`, `author_source`, `author_behalf_source`) "+
				"VALUES (?, ?, ?, NULL, NULL, ?, ?, ?, '', '', '')",
			ownerID, string(GrainPrincipal.Kind), GrainPrincipal.ID, note, now, false)
		return err
	}
	// template and schedule carry their notes on body.
	var body string
	err := tx.QueryRowContext(ctx,
		"SELECT `body` FROM `"+ownerKind+"` WHERE `id` = ?", ownerID).Scan(&body)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if strings.Contains(body, note) {
		return nil
	}
	updated := note
	if strings.TrimSpace(body) != "" {
		updated = body + "\n\n---\n\n" + note
	}
	_, err = tx.ExecContext(ctx,
		"UPDATE `"+ownerKind+"` SET `body` = ? WHERE `id` = ?",
		updated, ownerID)
	return err
}
