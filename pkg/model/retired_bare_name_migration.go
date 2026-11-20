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
	"log"
	"slices"
	"strings"
)

// MigrateRetiredBareCapabilityNames is schema migration 153's Up, which an
// import of a dump from before 153 runs too (ApplyDataMigrations): it takes the
// bare retired capability names out of the capability lists nobody sees
// them in -- the deployment's default capabilities, each repository's
// default capabilities and each repository's CI capabilities
// (docs/test-runs/2026-11-14-retired-capability-aliases.md, F4).
//
// The retirement migrations (67 onwards) rewrote every such name they
// found, so one still stored was written by hand afterwards (SQL, or an
// API that kept it): GET /api/config leaves it out of
// defaultCapabilities and /next/repos/<owner>/<name>/ci does not list it,
// yet the row keeps it, so it could only be seen or removed by SQL. Each
// name is settled as saving the list today would settle it:
//
//   - a name a registered alias still rewrites (CanonicalLegacyGrant) is
//     rewritten to the grant it stands for, which it already granted;
//   - a name RetiredGrantRefusal refuses grants nothing any longer, so it
//     is dropped, and the log line names what it became. Rewriting it to
//     that grant instead would grant what the row has not granted since
//     its retirement, which no operator chose;
//   - a runtime retirement (local-scripts:<name>) is rewritten to its
//     runtime expansion when the daemon has set one
//     (SetRuntimeGrantExpansions), which it already granted, and left
//     otherwise: during Init what it became depends on declarations
//     only the daemon reads afterwards (migrateLocalScripts);
//   - anything else is left.
//
// Task, template and schedule grants are not touched: a retired name
// there is shown, and refused visibly when the task dispatches.
func MigrateRetiredBareCapabilityNames(ctx context.Context, tx *sql.Tx) error {
	for _, c := range []struct {
		table, column string
		keys          []string
	}{
		{"grain_config", "default_capabilities", []string{"id"}},
		{"repo_config", "default_capabilities", []string{"owner", "name"}},
		{"repo_config", "ci_capabilities", []string{"owner", "name"}},
	} {
		if err := rewriteCapabilityListColumn(ctx, tx, c.table, c.keys, c.column, func(key []any, names []string) ([]string, bool) {
			return settleRetiredBareNames(c.table+"."+c.column, key, names)
		}); err != nil {
			return err
		}
	}
	return nil
}

// settleRetiredBareNames is names with each bare retired name settled
// as MigrateRetiredBareCapabilityNames says, each kept once where it first
// appears; changed is false when none was. where and key name the row for
// the log.
func settleRetiredBareNames(where string, key []any, names []string) (out []string, changed bool) {
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if to := CanonicalLegacyGrant(trimmed); to != trimmed {
			changed = true
			log.Printf("model: %s %v: rewrote the retired capability name %s to %s", where, key, trimmed, to)
			if !slices.Contains(out, to) {
				out = append(out, to)
			}
			continue
		}
		// A runtime retirement (local-scripts:<name>) the daemon has
		// already worked out -- this Up running from ApplyDataMigrations
		// on a live daemon, after migrateLocalScripts -- becomes what it
		// became, as RewriteRetiredGrants writes it at startup; it is
		// not removed.
		if tos, ok := runtimeExpansion(trimmed); ok {
			changed = true
			log.Printf("model: %s %v: rewrote the retired capability name %s to %s", where, key, trimmed, strings.Join(tos, " and "))
			for _, to := range tos {
				if !slices.Contains(out, to) {
					out = append(out, to)
				}
			}
			continue
		}
		if msg, _, ok := RetiredGrantRefusal(trimmed); ok {
			changed = true
			log.Printf("model: %s %v: removed %s", where, key, msg)
			continue
		}
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out, changed
}
