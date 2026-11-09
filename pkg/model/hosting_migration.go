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
	"slices"
	"sort"
	"strings"
)

// MigrateHostingFromGrants moves console rows out of every grant store
// (task_grant, template_grant, schedule_grant,
// grain_config.default_capabilities, repo_config.default_capabilities,
// grain_config.console_sessions and console_session_sync) into the hosting /
// default_hosting columns and ConsoleSession.Hosting, and drops catalogue and
// policy entries for console.
//
// When warn is true (one-release state-repository import compatibility), a
// deprecation warning is logged for each converted legacy capability name.
func MigrateHostingFromGrants(ctx context.Context, tx *sql.Tx, warn bool) error {
	warnAliases := func(where string, aliases []string, mapped Hosting) {
		if !warn || len(aliases) == 0 {
			return
		}
		for _, a := range aliases {
			target, _ := LegacyHostingAlias(a)
			if mapped != "" {
				target = mapped
			}
			log.Printf("deprecation warning: %s uses retired capability %q; set hosting: %s instead", where, a, target)
		}
	}

	// 1. Move task_grant, template_grant and schedule_grant rows into hosting.
	if err := migrateGrantTableHosting(ctx, tx, "task_grant", "task_id", "task", true, warnAliases); err != nil {
		return err
	}
	if err := migrateGrantTableHosting(ctx, tx, "template_grant", "template_id", "template", false, warnAliases); err != nil {
		return err
	}
	if err := migrateGrantTableHosting(ctx, tx, "schedule_grant", "schedule_id", "schedule", false, warnAliases); err != nil {
		return err
	}
	if hasWait, err := migrationTableExists(ctx, tx, "task_grant_wait"); err != nil {
		return err
	} else if hasWait {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM `task_grant_wait` WHERE `capability` = ?",
			ConsoleCapability); err != nil {
			return fmt.Errorf("hosting migration: deleting task_grant_wait rows: %w", err)
		}
	}

	// 2. Rewrite grain_config: default_capabilities -> default_hosting,
	// console_sessions (move console capability into ConsoleSession.Hosting and
	// onto any materialized session tasks), capability_policy (drop catalogue
	// and policy entries for console, fold defaults).
	type configRow struct {
		id                  int
		defaultCapabilities string
		defaultHosting      string
		consoleSessions     string
		capabilityPolicy    string
	}
	cfgRows, err := tx.QueryContext(ctx,
		"SELECT `id`, `default_capabilities`, `default_hosting`, `console_sessions`, `capability_policy` FROM `grain_config` ORDER BY `id`")
	if err != nil {
		return fmt.Errorf("hosting migration: reading grain_config: %w", err)
	}
	var cfgs []configRow
	for cfgRows.Next() {
		var r configRow
		if err := cfgRows.Scan(&r.id, &r.defaultCapabilities, &r.defaultHosting, &r.consoleSessions, &r.capabilityPolicy); err != nil {
			cfgRows.Close()
			return fmt.Errorf("hosting migration: scanning grain_config: %w", err)
		}
		cfgs = append(cfgs, r)
	}
	if err := cfgRows.Err(); err != nil {
		cfgRows.Close()
		return fmt.Errorf("hosting migration: iterating grain_config: %w", err)
	}
	cfgRows.Close()

	repoPolicyHostings := map[string]Hosting{}
	var consoleHostedSessionIDs []string
	for _, r := range cfgs {
		changed := false
		nextDefaultCaps := r.defaultCapabilities
		nextDefaultHost := Hosting(strings.TrimSpace(r.defaultHosting))
		nextSessions := r.consoleSessions
		nextPolicy := r.capabilityPolicy

		caps := splitCapabilities(r.defaultCapabilities)
		keptCaps, mapped, aliases := ExtractLegacyHostingStrings(caps)
		if len(aliases) > 0 {
			warnAliases("grain_config.default_capabilities", aliases, mapped)
			nextDefaultCaps = joinCapabilities(keptCaps)
			nextDefaultHost = MaxHosting(nextDefaultHost, mapped)
			changed = true
		}

		if strings.TrimSpace(r.consoleSessions) != "" {
			sessions, err := DecodeConsoleSessions(r.consoleSessions)
			if err != nil {
				return fmt.Errorf("hosting migration: grain_config %d console_sessions: %w", r.id, err)
			}
			sessionsChanged := false
			for i := range sessions {
				if kept, smapped, sal := ExtractLegacyHostingStrings(sessions[i].Capabilities); len(sal) > 0 {
					warnAliases("console_session "+sessions[i].ID, sal, smapped)
					sessions[i].Capabilities = kept
					sessions[i].Hosting = MaxHosting(sessions[i].Hosting, smapped)
					sessionsChanged = true
				}
				if keptDecl, _, sal := ExtractLegacyHostingStrings(sessions[i].DeclinedDefaults); len(sal) > 0 {
					sessions[i].DeclinedDefaults = keptDecl
					sessionsChanged = true
				}
				if NormalizeHosting(sessions[i].Hosting) == HostingConsole && sessions[i].ID != "" &&
					!slices.Contains(consoleHostedSessionIDs, sessions[i].ID) {
					consoleHostedSessionIDs = append(consoleHostedSessionIDs, sessions[i].ID)
				}
			}
			if sessionsChanged {
				encoded, err := EncodeConsoleSessions(sessions)
				if err != nil {
					return err
				}
				nextSessions = encoded
				changed = true
			}
		}

		if strings.TrimSpace(r.capabilityPolicy) != "" {
			rewritten, policyDefaultHost, policyRepoHosts, policyChanged, err := stripHostingFromCapabilityPolicy(r.capabilityPolicy, warnAliases)
			if err != nil {
				return fmt.Errorf("hosting migration: grain_config %d capability_policy: %w", r.id, err)
			}
			if policyChanged {
				nextPolicy = rewritten
				if policyDefaultHost != "" {
					nextDefaultHost = MaxHosting(nextDefaultHost, policyDefaultHost)
				}
				for repoKey, h := range policyRepoHosts {
					repoPolicyHostings[repoKey] = MaxHosting(repoPolicyHostings[repoKey], h)
				}
				changed = true
			}
		}

		if changed {
			if _, err := tx.ExecContext(ctx,
				"UPDATE `grain_config` SET `default_capabilities` = ?, `default_hosting` = ?, `console_sessions` = ?, `capability_policy` = ? WHERE `id` = ?",
				nextDefaultCaps, string(nextDefaultHost), nextSessions, nextPolicy, r.id); err != nil {
				return fmt.Errorf("hosting migration: updating grain_config %d: %w", r.id, err)
			}
		}
	}

	// Ensure any materialized task for a console session whose capabilities
	// listed console (or whose Hosting is console) also carries hosting = console.
	for _, sid := range consoleHostedSessionIDs {
		tag := ConsoleSessionTag(sid)
		if _, err := tx.ExecContext(ctx,
			"UPDATE `task` SET `hosting` = ? WHERE EXISTS ("+
				"SELECT 1 FROM `task_tag` WHERE `task_tag`.`task_id` = `task`.`id` AND `task_tag`.`tag` = ?"+
				")",
			string(HostingConsole), tag); err != nil {
			return fmt.Errorf("hosting migration: updating console session %s task hosting: %w", sid, err)
		}
	}

	// 3. Strip console from console_session_sync snapshots and record Hosting.
	if hasSync, err := migrationTableExists(ctx, tx, "console_session_sync"); err != nil {
		return err
	} else if hasSync {
		type syncRow struct {
			taskID   string
			snapshot string
		}
		syncRows, err := tx.QueryContext(ctx, "SELECT `task_id`, `snapshot` FROM `console_session_sync` ORDER BY `task_id`")
		if err != nil {
			return fmt.Errorf("hosting migration: reading console_session_sync: %w", err)
		}
		var syncs []syncRow
		for syncRows.Next() {
			var r syncRow
			if err := syncRows.Scan(&r.taskID, &r.snapshot); err != nil {
				syncRows.Close()
				return fmt.Errorf("hosting migration: scanning console_session_sync: %w", err)
			}
			syncs = append(syncs, r)
		}
		if err := syncRows.Err(); err != nil {
			syncRows.Close()
			return fmt.Errorf("hosting migration: iterating console_session_sync: %w", err)
		}
		syncRows.Close()
		for _, r := range syncs {
			var snap ConsoleSessionConfigSnapshot
			if err := json.Unmarshal([]byte(r.snapshot), &snap); err != nil {
				continue
			}
			snapChanged := false
			if kept, smapped, sal := ExtractLegacyHostingStrings(snap.Capabilities); len(sal) > 0 {
				snap.Capabilities = kept
				snap.Hosting = MaxHosting(snap.Hosting, smapped)
				snapChanged = true
			}
			if kept, _, sal := ExtractLegacyHostingStrings(snap.OfferedDefaults); len(sal) > 0 {
				snap.OfferedDefaults = kept
				snapChanged = true
			}
			if snapChanged {
				encoded, err := json.Marshal(snap)
				if err != nil {
					return fmt.Errorf("hosting migration: encoding console_session_sync %s: %w", r.taskID, err)
				}
				if _, err := tx.ExecContext(ctx, "UPDATE `console_session_sync` SET `snapshot` = ? WHERE `task_id` = ?", string(encoded), r.taskID); err != nil {
					return fmt.Errorf("hosting migration: updating console_session_sync %s: %w", r.taskID, err)
				}
			}
		}
	}

	// 4. Rewrite repo_config: default_capabilities -> default_hosting, strip ci_capabilities,
	// and apply any repo-level defaults folded out of capability_policy.
	type repoRow struct {
		owner               string
		name                string
		defaultCapabilities string
		ciCapabilities      string
		defaultHosting      string
	}
	rRows, err := tx.QueryContext(ctx,
		"SELECT `owner`, `name`, `default_capabilities`, `ci_capabilities`, `default_hosting` FROM `repo_config` ORDER BY `owner`, `name`")
	if err != nil {
		return fmt.Errorf("hosting migration: reading repo_config: %w", err)
	}
	var repos []repoRow
	for rRows.Next() {
		var r repoRow
		if err := rRows.Scan(&r.owner, &r.name, &r.defaultCapabilities, &r.ciCapabilities, &r.defaultHosting); err != nil {
			rRows.Close()
			return fmt.Errorf("hosting migration: scanning repo_config: %w", err)
		}
		repos = append(repos, r)
	}
	if err := rRows.Err(); err != nil {
		rRows.Close()
		return fmt.Errorf("hosting migration: iterating repo_config: %w", err)
	}
	rRows.Close()

	matchedPolicyRepos := map[string]bool{}
	for _, r := range repos {
		ref := RepoFromStored(r.owner, r.name)
		repoLabel := ref.String()
		changed := false
		nextDefaultCaps := r.defaultCapabilities
		nextCICaps := r.ciCapabilities
		nextDefaultHost := Hosting(strings.TrimSpace(r.defaultHosting))

		if kept, mapped, aliases := ExtractLegacyHostingStrings(splitCapabilities(r.defaultCapabilities)); len(aliases) > 0 {
			warnAliases("repo_config("+repoLabel+").default_capabilities", aliases, mapped)
			nextDefaultCaps = joinCapabilities(kept)
			nextDefaultHost = MaxHosting(nextDefaultHost, mapped)
			changed = true
		}
		if keptCI, _, aliases := ExtractLegacyHostingStrings(splitCapabilities(r.ciCapabilities)); len(aliases) > 0 {
			warnAliases("repo_config("+repoLabel+").ci_capabilities", aliases, "")
			nextCICaps = joinCapabilities(keptCI)
			changed = true
		}
		for policyKey, h := range repoPolicyHostings {
			parsed, err := ParseRepo(policyKey)
			if err == nil && parsed.Same(ref) {
				matchedPolicyRepos[policyKey] = true
				if merged := MaxHosting(nextDefaultHost, h); merged != nextDefaultHost {
					nextDefaultHost = merged
					changed = true
				}
			}
		}

		if changed {
			if _, err := tx.ExecContext(ctx,
				"UPDATE `repo_config` SET `default_capabilities` = ?, `ci_capabilities` = ?, `default_hosting` = ? WHERE `owner` = ? AND `name` = ?",
				nextDefaultCaps, nextCICaps, string(nextDefaultHost), r.owner, r.name); err != nil {
				return fmt.Errorf("hosting migration: updating repo_config %s: %w", repoLabel, err)
			}
		}
	}

	// Any repo named only in capability_policy.catalogue.defaults.repos gets its
	// own repo_config row so its default hosting is preserved.
	var remainingKeys []string
	for k := range repoPolicyHostings {
		if !matchedPolicyRepos[k] {
			remainingKeys = append(remainingKeys, k)
		}
	}
	if len(remainingKeys) > 0 {
		targetRepoNames, err := targetReposIn(ctx, tx)
		if err != nil {
			return fmt.Errorf("hosting migration: reading target_repos: %w", err)
		}
		var targetRefs []RepoRef
		for _, tr := range targetRepoNames {
			if ref, err := ParseRepo(tr); err == nil {
				targetRefs = append(targetRefs, ref)
			}
		}
		canonicalRepo := func(r RepoRef) (RepoRef, bool) {
			for _, tr := range targetRefs {
				if tr.Same(r) {
					return tr, true
				}
			}
			return r, false
		}

		sort.Strings(remainingKeys)
		type pendingRepoHosting struct {
			repo      RepoRef
			canonical bool
			host      Hosting
		}
		var pendingRepos []pendingRepoHosting
		for _, k := range remainingKeys {
			parsed, err := ParseRepo(k)
			if err != nil {
				continue
			}
			h := repoPolicyHostings[k]
			if h == "" {
				continue
			}
			resolved, isCanonical := canonicalRepo(parsed)
			merged := false
			for i := range pendingRepos {
				if pendingRepos[i].repo.Same(resolved) {
					pendingRepos[i].host = MaxHosting(pendingRepos[i].host, h)
					if !pendingRepos[i].canonical {
						s := resolved.String()
						if isCanonical || s == strings.ToLower(s) {
							pendingRepos[i].repo = resolved
							pendingRepos[i].canonical = isCanonical
						}
					}
					merged = true
					break
				}
			}
			if !merged {
				pendingRepos = append(pendingRepos, pendingRepoHosting{
					repo:      resolved,
					canonical: isCanonical,
					host:      h,
				})
			}
		}
		for _, pr := range pendingRepos {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO `repo_config` (`owner`, `name`, `default_capabilities`, `default_hosting`) VALUES (?, ?, '', ?)",
				pr.repo.StoredOwner(), pr.repo.Name, string(pr.host)); err != nil {
				return fmt.Errorf("hosting migration: inserting repo_config %s: %w", pr.repo, err)
			}
		}
	}

	return nil
}

func migrateGrantTableHosting(
	ctx context.Context,
	tx *sql.Tx,
	grantTable, fkCol, parentTable string,
	normalizeDefault bool,
	warnAliases func(where string, aliases []string, mapped Hosting),
) error {
	if exists, err := migrationTableExists(ctx, tx, grantTable); err != nil || !exists {
		return err
	}
	rows, err := tx.QueryContext(ctx,
		"SELECT `"+fkCol+"`, `capability` FROM `"+grantTable+"` WHERE `capability` = ? ORDER BY `"+fkCol+"`, `capability`",
		ConsoleCapability)
	if err != nil {
		return fmt.Errorf("hosting migration: reading %s: %w", grantTable, err)
	}
	type hit struct {
		mapped  Hosting
		aliases []string
	}
	hits := map[string]*hit{}
	var ids []string
	for rows.Next() {
		var id, capName string
		if err := rows.Scan(&id, &capName); err != nil {
			rows.Close()
			return fmt.Errorf("hosting migration: scanning %s: %w", grantTable, err)
		}
		hVal, ok := LegacyHostingAlias(capName)
		if !ok {
			continue
		}
		h := hits[id]
		if h == nil {
			h = &hit{}
			hits[id] = h
			ids = append(ids, id)
		}
		h.mapped = MaxHosting(h.mapped, hVal)
		if !slices.Contains(h.aliases, capName) {
			h.aliases = append(h.aliases, capName)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("hosting migration: iterating %s: %w", grantTable, err)
	}
	rows.Close()

	for _, id := range ids {
		h := hits[id]
		warnAliases(parentTable+" "+id, h.aliases, h.mapped)
		var curRaw string
		err := tx.QueryRowContext(ctx, "SELECT `hosting` FROM `"+parentTable+"` WHERE `id` = ?", id).Scan(&curRaw)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return fmt.Errorf("hosting migration: reading %s %s: %w", parentTable, id, err)
		}
		cur := Hosting(strings.TrimSpace(curRaw))
		if normalizeDefault {
			cur = NormalizeHosting(cur)
		}
		next := MaxHosting(cur, h.mapped)
		if next != cur || curRaw != string(next) {
			if _, err := tx.ExecContext(ctx, "UPDATE `"+parentTable+"` SET `hosting` = ? WHERE `id` = ?", string(next), id); err != nil {
				return fmt.Errorf("hosting migration: updating %s %s: %w", parentTable, id, err)
			}
		}
	}

	if len(ids) > 0 {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM `"+grantTable+"` WHERE `capability` = ?",
			ConsoleCapability); err != nil {
			return fmt.Errorf("hosting migration: deleting retired grants from %s: %w", grantTable, err)
		}
	}
	return nil
}

func stripHostingFromCapabilityPolicy(
	stored string,
	warnAliases func(where string, aliases []string, mapped Hosting),
) (encoded string, defaultHost Hosting, repoHosts map[string]Hosting, changed bool, err error) {
	policy, err := DecodeCapabilityPolicy(stored)
	if err != nil {
		return "", "", nil, false, err
	}
	repoHosts = map[string]Hosting{}

	if len(policy.Rules) > 0 {
		keptRules := make([]CapabilityPolicyRule, 0, len(policy.Rules))
		for _, r := range policy.Rules {
			if h, ok := LegacyHostingAlias(r.Capability); ok {
				warnAliases("capability_policy.rules", []string{strings.TrimSpace(r.Capability)}, h)
				changed = true
				continue
			}
			keptRules = append(keptRules, r)
		}
		if len(keptRules) != len(policy.Rules) {
			if len(keptRules) == 0 {
				policy.Rules = nil
			} else {
				policy.Rules = keptRules
			}
		}
	}

	if cat := policy.Catalogue; cat != nil {
		if _, ok := cat.InProcess[ConsoleCapability]; ok {
			h, _ := LegacyHostingAlias(ConsoleCapability)
			warnAliases("capability_policy.catalogue.inProcess", []string{ConsoleCapability}, h)
			delete(cat.InProcess, ConsoleCapability)
			if len(cat.InProcess) == 0 {
				cat.InProcess = nil
			}
			changed = true
		}
		if kept, mapped, aliases := ExtractLegacyHostingStrings(cat.Defaults.Task); len(aliases) > 0 {
			warnAliases("capability_policy.catalogue.defaults.task", aliases, mapped)
			if len(kept) == 0 {
				cat.Defaults.Task = nil
			} else {
				cat.Defaults.Task = kept
			}
			defaultHost = MaxHosting(defaultHost, mapped)
			changed = true
		}
		if kept, _, aliases := ExtractLegacyHostingStrings(cat.Defaults.CI); len(aliases) > 0 {
			warnAliases("capability_policy.catalogue.defaults.ci", aliases, "")
			if len(kept) == 0 {
				cat.Defaults.CI = nil
			} else {
				cat.Defaults.CI = kept
			}
			changed = true
		}
		for repoKey, rd := range cat.Defaults.Repos {
			rdChanged := false
			if kept, mapped, aliases := ExtractLegacyHostingStrings(rd.Task); len(aliases) > 0 {
				warnAliases("capability_policy.catalogue.defaults.repos["+repoKey+"].task", aliases, mapped)
				if len(kept) == 0 {
					rd.Task = nil
				} else {
					rd.Task = kept
				}
				repoHosts[repoKey] = MaxHosting(repoHosts[repoKey], mapped)
				rdChanged = true
			}
			if kept, _, aliases := ExtractLegacyHostingStrings(rd.CI); len(aliases) > 0 {
				warnAliases("capability_policy.catalogue.defaults.repos["+repoKey+"].ci", aliases, "")
				if len(kept) == 0 {
					rd.CI = nil
				} else {
					rd.CI = kept
				}
				rdChanged = true
			}
			if rdChanged {
				if len(rd.Task) == 0 && len(rd.CI) == 0 {
					delete(cat.Defaults.Repos, repoKey)
				} else {
					cat.Defaults.Repos[repoKey] = rd
				}
				if len(cat.Defaults.Repos) == 0 {
					cat.Defaults.Repos = nil
				}
				changed = true
			}
		}
	}

	if !changed {
		return stored, "", nil, false, nil
	}
	encoded, err = EncodeCapabilityPolicy(policy)
	if err != nil {
		return "", "", nil, false, err
	}
	return encoded, defaultHost, repoHosts, true, nil
}
