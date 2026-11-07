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

// MigrateAdmissionFromGrants moves priority-dispatch and durable rows out of
// every grant store (task_grant, template_grant, schedule_grant,
// grain_config.default_capabilities, repo_config.default_capabilities,
// grain_config.console_sessions and console_session_sync) into the admission /
// default_admission columns, drops catalogue and policy entries for both names,
// and ensures standing console session tasks carry admission = durable.
//
// When warn is true (one-release state-repository import compatibility), a
// deprecation warning is logged for each converted legacy capability name.
func MigrateAdmissionFromGrants(ctx context.Context, tx *sql.Tx, warn bool) error {
	warnAliases := func(where string, aliases []string, mapped Admission) {
		if !warn || len(aliases) == 0 {
			return
		}
		for _, a := range aliases {
			target, _ := LegacyAdmissionAlias(a)
			if mapped != "" {
				target = mapped
			}
			log.Printf("deprecation warning: %s uses retired capability %q; set admission: %s instead", where, a, target)
		}
	}

	// 1. Standing console session tasks hold admission = durable by construction.
	sessionRows, err := tx.QueryContext(ctx,
		"SELECT `id`, `admission` FROM `task` WHERE `origin_reason` = ? OR EXISTS ("+
			"SELECT 1 FROM `task_tag` WHERE `task_tag`.`task_id` = `task`.`id` AND `task_tag`.`tag` LIKE 'console-session:%'"+
			") ORDER BY `id`",
		string(ReasonConsoleSession))
	if err != nil {
		return fmt.Errorf("admission migration: reading console session tasks: %w", err)
	}
	var sessionTaskIDs []string
	for sessionRows.Next() {
		var id, adm string
		if err := sessionRows.Scan(&id, &adm); err != nil {
			sessionRows.Close()
			return fmt.Errorf("admission migration: scanning console session task: %w", err)
		}
		if NormalizeAdmission(Admission(adm)) != AdmissionDurable {
			sessionTaskIDs = append(sessionTaskIDs, id)
		}
	}
	if err := sessionRows.Err(); err != nil {
		sessionRows.Close()
		return fmt.Errorf("admission migration: iterating console session tasks: %w", err)
	}
	sessionRows.Close()
	for _, id := range sessionTaskIDs {
		if _, err := tx.ExecContext(ctx, "UPDATE `task` SET `admission` = ? WHERE `id` = ?", string(AdmissionDurable), id); err != nil {
			return fmt.Errorf("admission migration: setting task %s admission=durable: %w", id, err)
		}
	}

	// 2. Move task_grant, template_grant and schedule_grant rows into admission.
	if err := migrateGrantTableAdmission(ctx, tx, "task_grant", "task_id", "task", true, warnAliases); err != nil {
		return err
	}
	if err := migrateGrantTableAdmission(ctx, tx, "template_grant", "template_id", "template", false, warnAliases); err != nil {
		return err
	}
	if err := migrateGrantTableAdmission(ctx, tx, "schedule_grant", "schedule_id", "schedule", false, warnAliases); err != nil {
		return err
	}
	if hasWait, err := migrationTableExists(ctx, tx, "task_grant_wait"); err != nil {
		return err
	} else if hasWait {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM `task_grant_wait` WHERE `capability` IN (?, ?)",
			PriorityDispatchCapability, DurableCapability); err != nil {
			return fmt.Errorf("admission migration: deleting task_grant_wait rows: %w", err)
		}
	}

	// 3. Rewrite grain_config: default_capabilities -> default_admission,
	// console_sessions (strip retired grants), capability_policy (drop catalogue
	// and policy entries for priority-dispatch and durable, fold defaults).
	type configRow struct {
		id                  int
		defaultCapabilities string
		defaultAdmission    string
		consoleSessions     string
		capabilityPolicy    string
	}
	cfgRows, err := tx.QueryContext(ctx,
		"SELECT `id`, `default_capabilities`, `default_admission`, `console_sessions`, `capability_policy` FROM `grain_config` ORDER BY `id`")
	if err != nil {
		return fmt.Errorf("admission migration: reading grain_config: %w", err)
	}
	var cfgs []configRow
	for cfgRows.Next() {
		var r configRow
		if err := cfgRows.Scan(&r.id, &r.defaultCapabilities, &r.defaultAdmission, &r.consoleSessions, &r.capabilityPolicy); err != nil {
			cfgRows.Close()
			return fmt.Errorf("admission migration: scanning grain_config: %w", err)
		}
		cfgs = append(cfgs, r)
	}
	if err := cfgRows.Err(); err != nil {
		cfgRows.Close()
		return fmt.Errorf("admission migration: iterating grain_config: %w", err)
	}
	cfgRows.Close()

	repoPolicyAdmissions := map[string]Admission{}
	for _, r := range cfgs {
		changed := false
		nextDefaultCaps := r.defaultCapabilities
		nextDefaultAdm := Admission(strings.TrimSpace(r.defaultAdmission))
		nextSessions := r.consoleSessions
		nextPolicy := r.capabilityPolicy

		caps := splitCapabilities(r.defaultCapabilities)
		keptCaps, mapped, aliases := ExtractLegacyAdmissionStrings(caps)
		if len(aliases) > 0 {
			warnAliases("grain_config.default_capabilities", aliases, mapped)
			nextDefaultCaps = joinCapabilities(keptCaps)
			nextDefaultAdm = MaxAdmission(nextDefaultAdm, mapped)
			changed = true
		}

		if strings.TrimSpace(r.consoleSessions) != "" {
			sessions, err := DecodeConsoleSessions(r.consoleSessions)
			if err != nil {
				return fmt.Errorf("admission migration: grain_config %d console_sessions: %w", r.id, err)
			}
			sessionsChanged := false
			for i := range sessions {
				if kept, smapped, sal := ExtractLegacyAdmissionStrings(sessions[i].Capabilities); len(sal) > 0 {
					warnAliases("console_session "+sessions[i].ID, sal, smapped)
					sessions[i].Capabilities = kept
					sessionsChanged = true
				}
				if keptDecl, _, sal := ExtractLegacyAdmissionStrings(sessions[i].DeclinedDefaults); len(sal) > 0 {
					sessions[i].DeclinedDefaults = keptDecl
					sessionsChanged = true
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
			rewritten, policyDefaultAdm, policyRepoAdms, policyChanged, err := stripAdmissionFromCapabilityPolicy(r.capabilityPolicy, warnAliases)
			if err != nil {
				return fmt.Errorf("admission migration: grain_config %d capability_policy: %w", r.id, err)
			}
			if policyChanged {
				nextPolicy = rewritten
				if policyDefaultAdm != "" {
					nextDefaultAdm = MaxAdmission(nextDefaultAdm, policyDefaultAdm)
				}
				for repoKey, adm := range policyRepoAdms {
					repoPolicyAdmissions[repoKey] = MaxAdmission(repoPolicyAdmissions[repoKey], adm)
				}
				changed = true
			}
		}

		if changed {
			if _, err := tx.ExecContext(ctx,
				"UPDATE `grain_config` SET `default_capabilities` = ?, `default_admission` = ?, `console_sessions` = ?, `capability_policy` = ? WHERE `id` = ?",
				nextDefaultCaps, string(nextDefaultAdm), nextSessions, nextPolicy, r.id); err != nil {
				return fmt.Errorf("admission migration: updating grain_config %d: %w", r.id, err)
			}
		}
	}

	// 4. Strip priority-dispatch and durable from console_session_sync snapshots.
	if hasSync, err := migrationTableExists(ctx, tx, "console_session_sync"); err != nil {
		return err
	} else if hasSync {
		type syncRow struct {
			taskID   string
			snapshot string
		}
		syncRows, err := tx.QueryContext(ctx, "SELECT `task_id`, `snapshot` FROM `console_session_sync` ORDER BY `task_id`")
		if err != nil {
			return fmt.Errorf("admission migration: reading console_session_sync: %w", err)
		}
		var syncs []syncRow
		for syncRows.Next() {
			var r syncRow
			if err := syncRows.Scan(&r.taskID, &r.snapshot); err != nil {
				syncRows.Close()
				return fmt.Errorf("admission migration: scanning console_session_sync: %w", err)
			}
			syncs = append(syncs, r)
		}
		if err := syncRows.Err(); err != nil {
			syncRows.Close()
			return fmt.Errorf("admission migration: iterating console_session_sync: %w", err)
		}
		syncRows.Close()
		for _, r := range syncs {
			var snap ConsoleSessionConfigSnapshot
			if err := json.Unmarshal([]byte(r.snapshot), &snap); err != nil {
				continue
			}
			snapChanged := false
			if kept, _, sal := ExtractLegacyAdmissionStrings(snap.Capabilities); len(sal) > 0 {
				snap.Capabilities = kept
				snapChanged = true
			}
			if kept, _, sal := ExtractLegacyAdmissionStrings(snap.OfferedDefaults); len(sal) > 0 {
				snap.OfferedDefaults = kept
				snapChanged = true
			}
			if snapChanged {
				encoded, err := json.Marshal(snap)
				if err != nil {
					return fmt.Errorf("admission migration: encoding console_session_sync %s: %w", r.taskID, err)
				}
				if _, err := tx.ExecContext(ctx, "UPDATE `console_session_sync` SET `snapshot` = ? WHERE `task_id` = ?", string(encoded), r.taskID); err != nil {
					return fmt.Errorf("admission migration: updating console_session_sync %s: %w", r.taskID, err)
				}
			}
		}
	}

	// 5. Rewrite repo_config: default_capabilities -> default_admission, strip ci_capabilities,
	// and apply any repo-level defaults folded out of capability_policy.
	type repoRow struct {
		owner               string
		name                string
		defaultCapabilities string
		ciCapabilities      string
		defaultAdmission    string
	}
	rRows, err := tx.QueryContext(ctx,
		"SELECT `owner`, `name`, `default_capabilities`, `ci_capabilities`, `default_admission` FROM `repo_config` ORDER BY `owner`, `name`")
	if err != nil {
		return fmt.Errorf("admission migration: reading repo_config: %w", err)
	}
	var repos []repoRow
	for rRows.Next() {
		var r repoRow
		if err := rRows.Scan(&r.owner, &r.name, &r.defaultCapabilities, &r.ciCapabilities, &r.defaultAdmission); err != nil {
			rRows.Close()
			return fmt.Errorf("admission migration: scanning repo_config: %w", err)
		}
		repos = append(repos, r)
	}
	if err := rRows.Err(); err != nil {
		rRows.Close()
		return fmt.Errorf("admission migration: iterating repo_config: %w", err)
	}
	rRows.Close()

	matchedPolicyRepos := map[string]bool{}
	for _, r := range repos {
		ref := RepoFromStored(r.owner, r.name)
		repoLabel := ref.String()
		changed := false
		nextDefaultCaps := r.defaultCapabilities
		nextCICaps := r.ciCapabilities
		nextDefaultAdm := Admission(strings.TrimSpace(r.defaultAdmission))

		if kept, mapped, aliases := ExtractLegacyAdmissionStrings(splitCapabilities(r.defaultCapabilities)); len(aliases) > 0 {
			warnAliases("repo_config("+repoLabel+").default_capabilities", aliases, mapped)
			nextDefaultCaps = joinCapabilities(kept)
			nextDefaultAdm = MaxAdmission(nextDefaultAdm, mapped)
			changed = true
		}
		if keptCI, _, aliases := ExtractLegacyAdmissionStrings(splitCapabilities(r.ciCapabilities)); len(aliases) > 0 {
			warnAliases("repo_config("+repoLabel+").ci_capabilities", aliases, "")
			nextCICaps = joinCapabilities(keptCI)
			changed = true
		}
		for policyKey, adm := range repoPolicyAdmissions {
			parsed, err := ParseRepo(policyKey)
			if err == nil && parsed.Same(ref) {
				matchedPolicyRepos[policyKey] = true
				if merged := MaxAdmission(nextDefaultAdm, adm); merged != nextDefaultAdm {
					nextDefaultAdm = merged
					changed = true
				}
			}
		}

		if changed {
			if _, err := tx.ExecContext(ctx,
				"UPDATE `repo_config` SET `default_capabilities` = ?, `ci_capabilities` = ?, `default_admission` = ? WHERE `owner` = ? AND `name` = ?",
				nextDefaultCaps, nextCICaps, string(nextDefaultAdm), r.owner, r.name); err != nil {
				return fmt.Errorf("admission migration: updating repo_config %s: %w", repoLabel, err)
			}
		}
	}

	// Any repo named only in capability_policy.catalogue.defaults.repos gets its
	// own repo_config row so its default admission is preserved.
	var remainingKeys []string
	for k := range repoPolicyAdmissions {
		if !matchedPolicyRepos[k] {
			remainingKeys = append(remainingKeys, k)
		}
	}
	if len(remainingKeys) > 0 {
		targetRepoNames, err := targetReposIn(ctx, tx)
		if err != nil {
			return fmt.Errorf("admission migration: reading target_repos: %w", err)
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
		type pendingRepoAdmission struct {
			repo      RepoRef
			canonical bool
			adm       Admission
		}
		var pendingRepos []pendingRepoAdmission
		for _, k := range remainingKeys {
			parsed, err := ParseRepo(k)
			if err != nil {
				continue
			}
			adm := repoPolicyAdmissions[k]
			if adm == "" {
				continue
			}
			resolved, isCanonical := canonicalRepo(parsed)
			merged := false
			for i := range pendingRepos {
				if pendingRepos[i].repo.Same(resolved) {
					pendingRepos[i].adm = MaxAdmission(pendingRepos[i].adm, adm)
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
				pendingRepos = append(pendingRepos, pendingRepoAdmission{
					repo:      resolved,
					canonical: isCanonical,
					adm:       adm,
				})
			}
		}
		for _, pr := range pendingRepos {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO `repo_config` (`owner`, `name`, `default_capabilities`, `default_admission`) VALUES (?, ?, '', ?)",
				pr.repo.StoredOwner(), pr.repo.Name, string(pr.adm)); err != nil {
				return fmt.Errorf("admission migration: inserting repo_config %s: %w", pr.repo, err)
			}
		}
	}

	return nil
}

func migrateGrantTableAdmission(
	ctx context.Context,
	tx *sql.Tx,
	grantTable, fkCol, parentTable string,
	normalizeDefault bool,
	warnAliases func(where string, aliases []string, mapped Admission),
) error {
	if exists, err := migrationTableExists(ctx, tx, grantTable); err != nil || !exists {
		return err
	}
	rows, err := tx.QueryContext(ctx,
		"SELECT `"+fkCol+"`, `capability` FROM `"+grantTable+"` WHERE `capability` IN (?, ?) ORDER BY `"+fkCol+"`, `capability`",
		PriorityDispatchCapability, DurableCapability)
	if err != nil {
		return fmt.Errorf("admission migration: reading %s: %w", grantTable, err)
	}
	type hit struct {
		mapped  Admission
		aliases []string
	}
	hits := map[string]*hit{}
	var ids []string
	for rows.Next() {
		var id, capName string
		if err := rows.Scan(&id, &capName); err != nil {
			rows.Close()
			return fmt.Errorf("admission migration: scanning %s: %w", grantTable, err)
		}
		adm, ok := LegacyAdmissionAlias(capName)
		if !ok {
			continue
		}
		h := hits[id]
		if h == nil {
			h = &hit{}
			hits[id] = h
			ids = append(ids, id)
		}
		h.mapped = MaxAdmission(h.mapped, adm)
		if !slices.Contains(h.aliases, capName) {
			h.aliases = append(h.aliases, capName)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("admission migration: iterating %s: %w", grantTable, err)
	}
	rows.Close()

	for _, id := range ids {
		h := hits[id]
		warnAliases(parentTable+" "+id, h.aliases, h.mapped)
		var curRaw string
		err := tx.QueryRowContext(ctx, "SELECT `admission` FROM `"+parentTable+"` WHERE `id` = ?", id).Scan(&curRaw)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return fmt.Errorf("admission migration: reading %s %s: %w", parentTable, id, err)
		}
		cur := Admission(strings.TrimSpace(curRaw))
		if normalizeDefault {
			cur = NormalizeAdmission(cur)
		}
		next := MaxAdmission(cur, h.mapped)
		if next != cur || curRaw != string(next) {
			if _, err := tx.ExecContext(ctx, "UPDATE `"+parentTable+"` SET `admission` = ? WHERE `id` = ?", string(next), id); err != nil {
				return fmt.Errorf("admission migration: updating %s %s: %w", parentTable, id, err)
			}
		}
	}

	if len(ids) > 0 {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM `"+grantTable+"` WHERE `capability` IN (?, ?)",
			PriorityDispatchCapability, DurableCapability); err != nil {
			return fmt.Errorf("admission migration: deleting retired grants from %s: %w", grantTable, err)
		}
	}
	return nil
}

func stripAdmissionFromCapabilityPolicy(
	stored string,
	warnAliases func(where string, aliases []string, mapped Admission),
) (encoded string, defaultAdm Admission, repoAdms map[string]Admission, changed bool, err error) {
	policy, err := DecodeCapabilityPolicy(stored)
	if err != nil {
		return "", "", nil, false, err
	}
	repoAdms = map[string]Admission{}

	if len(policy.Rules) > 0 {
		keptRules := make([]CapabilityPolicyRule, 0, len(policy.Rules))
		for _, r := range policy.Rules {
			if adm, ok := LegacyAdmissionAlias(r.Capability); ok {
				warnAliases("capability_policy.rules", []string{strings.TrimSpace(r.Capability)}, adm)
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
		for _, retired := range []string{PriorityDispatchCapability, DurableCapability} {
			if _, ok := cat.InProcess[retired]; ok {
				adm, _ := LegacyAdmissionAlias(retired)
				warnAliases("capability_policy.catalogue.inProcess", []string{retired}, adm)
				delete(cat.InProcess, retired)
				if len(cat.InProcess) == 0 {
					cat.InProcess = nil
				}
				changed = true
			}
		}
		if kept, mapped, aliases := ExtractLegacyAdmissionStrings(cat.Defaults.Task); len(aliases) > 0 {
			warnAliases("capability_policy.catalogue.defaults.task", aliases, mapped)
			if len(kept) == 0 {
				cat.Defaults.Task = nil
			} else {
				cat.Defaults.Task = kept
			}
			defaultAdm = MaxAdmission(defaultAdm, mapped)
			changed = true
		}
		if kept, _, aliases := ExtractLegacyAdmissionStrings(cat.Defaults.CI); len(aliases) > 0 {
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
			if kept, mapped, aliases := ExtractLegacyAdmissionStrings(rd.Task); len(aliases) > 0 {
				warnAliases("capability_policy.catalogue.defaults.repos["+repoKey+"].task", aliases, mapped)
				if len(kept) == 0 {
					rd.Task = nil
				} else {
					rd.Task = kept
				}
				repoAdms[repoKey] = MaxAdmission(repoAdms[repoKey], mapped)
				rdChanged = true
			}
			if kept, _, aliases := ExtractLegacyAdmissionStrings(rd.CI); len(aliases) > 0 {
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
	return encoded, defaultAdm, repoAdms, true, nil
}

func migrationTableExists(ctx context.Context, tx *sql.Tx, name string) (bool, error) {
	var n int
	if err := tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `sqlite_master` WHERE `type` = 'table' AND `name` = ?",
		name).Scan(&n); err != nil {
		return false, fmt.Errorf("admission migration: checking table %s: %w", name, err)
	}
	return n > 0, nil
}
