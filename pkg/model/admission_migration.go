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
	"fmt"
	"strings"
)

// admissionGrantDrops is migration 120's list renames: each retired
// admission grant name to nothing, so renameCapabilityListMulti drops it
// from a stored list. The migration's own copy.
var admissionGrantDrops = map[string][]string{
	"priority-dispatch": {},
	"durable":           {},
}

// MigrateAdmissionGrants is schema migration 120's Up
// (design/design/unified-grants.design.md §4.1, Q8/Q10): the
// priority-dispatch and durable grants become the admission task setting.
//
//   - Tasks, templates and schedules holding durable become
//     admission 'durable' (whether or not they also hold
//     priority-dispatch), those holding only priority-dispatch become
//     'bypass', and every grant row of either name is deleted.
//   - The deployment's and each repository's default capabilities lose
//     the two names, which raise default_admission instead.
//   - Standing sessions and repo_config.ci_capabilities lose them: every
//     session is durable by construction, and neither ever meant anything
//     to a CI job.
//   - The capability policy's rules and catalogue entries for the two
//     names are dropped, not converted (Q10).
//
// It is idempotent, so a grain/config import at the current schema runs
// it too: a repository written before 120 still naming the grants is
// accepted for one release and stored as admission.
func MigrateAdmissionGrants(ctx context.Context, tx *sql.Tx) error {
	for _, t := range []struct{ table, grants, owner string }{
		{"task", "task_grant", "task_id"},
		{"template", "template_grant", "template_id"},
		{"schedule", "schedule_grant", "schedule_id"},
	} {
		if _, err := tx.ExecContext(ctx, "UPDATE `"+t.table+"` SET `admission` = 'durable' WHERE `id` IN "+
			"(SELECT `"+t.owner+"` FROM `"+t.grants+"` WHERE `capability` = 'durable')"); err != nil {
			return fmt.Errorf("%s: moving durable grants into admission: %w", t.table, err)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE `"+t.table+"` SET `admission` = 'bypass' WHERE `admission` = 'standard' AND `id` IN "+
			"(SELECT `"+t.owner+"` FROM `"+t.grants+"` WHERE `capability` = 'priority-dispatch')"); err != nil {
			return fmt.Errorf("%s: moving priority-dispatch grants into admission: %w", t.table, err)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM `"+t.grants+"` WHERE `capability` IN ('priority-dispatch', 'durable')"); err != nil {
			return fmt.Errorf("%s: deleting admission grants: %w", t.grants, err)
		}
	}
	if err := foldDefaultAdmission(ctx, tx, "grain_config", []string{"id"}); err != nil {
		return err
	}
	if err := foldDefaultAdmission(ctx, tx, "repo_config", []string{"owner", "name"}); err != nil {
		return err
	}
	if err := rewriteConsoleSessionGrantsMulti(ctx, tx, admissionGrantDrops); err != nil {
		return err
	}
	if err := rewriteCapabilityListColumnMulti(ctx, tx, "repo_config", []string{"owner", "name"}, "ci_capabilities", admissionGrantDrops); err != nil {
		return err
	}
	return dropAdmissionPolicy(ctx, tx)
}

// foldDefaultAdmission moves the retired admission names out of each
// row's default_capabilities into its default_admission (the higher of
// the two).
func foldDefaultAdmission(ctx context.Context, tx *sql.Tx, table string, keys []string) error {
	var cols, where []string
	for _, k := range keys {
		cols = append(cols, "`"+k+"`")
		where = append(where, "`"+k+"` = ?")
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+strings.Join(cols, ",")+", `default_capabilities`, `default_admission` FROM `"+table+"`")
	if err != nil {
		return fmt.Errorf("%s.default_capabilities: reading: %w", table, err)
	}
	type update struct {
		key       []any
		list      string
		admission Admission
	}
	var updates []update
	for rows.Next() {
		key := make([]any, len(keys))
		dest := make([]any, 0, len(keys)+2)
		for i := range key {
			dest = append(dest, &key[i])
		}
		var stored string
		var admission Admission
		dest = append(dest, &stored, &admission)
		if err := rows.Scan(dest...); err != nil {
			rows.Close()
			return fmt.Errorf("%s.default_capabilities: reading: %w", table, err)
		}
		if rest, a, folded := FoldAdmissionNames(splitCapabilities(stored), admission); folded {
			updates = append(updates, update{key, joinCapabilities(rest), a})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("%s.default_capabilities: reading: %w", table, err)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, u := range updates {
		if _, err := tx.ExecContext(ctx, "UPDATE `"+table+"` SET `default_capabilities` = ?, `default_admission` = ? WHERE "+
			strings.Join(where, " AND "), append([]any{u.list, u.admission.String()}, u.key...)...); err != nil {
			return fmt.Errorf("%s.default_capabilities: rewriting: %w", table, err)
		}
	}
	return nil
}

// dropAdmissionPolicy removes the capability policy's rules, catalogue
// in-process entries and catalogue defaults naming the two retired
// admission grants (Q10: no deployment narrowed them, so nothing
// replaces them).
func dropAdmissionPolicy(ctx context.Context, tx *sql.Tx) error {
	var stored string
	err := tx.QueryRowContext(ctx, "SELECT `capability_policy` FROM `grain_config` WHERE `id` = 1").Scan(&stored)
	if err == sql.ErrNoRows || (err == nil && strings.TrimSpace(stored) == "") {
		return nil
	}
	if err != nil {
		return fmt.Errorf("grain_config.capability_policy: reading: %w", err)
	}
	policy, err := DecodeCapabilityPolicy(stored)
	if err != nil {
		return nil
	}
	changed := false
	kept := policy.Rules[:0]
	for _, r := range policy.Rules {
		if IsRetiredAdmissionGrant(r.Capability) {
			changed = true
			continue
		}
		kept = append(kept, r)
	}
	policy.Rules = kept
	if StripAdmissionFromCatalogue(policy.Catalogue) {
		changed = true
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

// StripAdmissionFromCatalogue removes priority-dispatch and durable from
// cat's InProcess entries and Defaults lists, returning whether anything
// changed.
func StripAdmissionFromCatalogue(cat *CapabilityCatalogue) bool {
	if cat == nil {
		return false
	}
	changed := false
	for name := range admissionGrantDrops {
		if _, ok := cat.InProcess[name]; ok {
			delete(cat.InProcess, name)
			changed = true
		}
	}
	drop := func(names []string) []string {
		if next, did := renameCapabilityListMulti(names, admissionGrantDrops); did {
			changed = true
			return next
		}
		return names
	}
	cat.Defaults.Task = drop(cat.Defaults.Task)
	cat.Defaults.CI = drop(cat.Defaults.CI)
	for repo, rd := range cat.Defaults.Repos {
		rd.Task, rd.CI = drop(rd.Task), drop(rd.CI)
		cat.Defaults.Repos[repo] = rd
	}
	return changed
}
