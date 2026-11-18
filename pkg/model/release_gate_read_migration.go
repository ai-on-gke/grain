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
)

// releaseGateReadRenames is migration 153's renames: the retired bare
// release-gate-read CI capability alias to the canonical
// https://grain.dev/capabilities/api#release-gate-read preset reference. Kept
// as the migration's own copy so later changes to retiredGrantReplacements
// never alter what migration 153 rewrote.
var releaseGateReadRenames = map[string][]string{
	ReleaseGateReadCapability: {APIRef(ReleaseGateReadCapability)},
}

// migrateReleaseGateReadGrants is schema migration 153's Up: the bare
// release-gate-read CI alias (ciapi.Aliases) is retired onto
// https://grain.dev/capabilities/api#release-gate-read, so every stored grant
// of it -- the three grant tables, task_grant_wait, deployment and repository
// default_capabilities, repo_config.ci_capabilities, standing
// console_sessions (capabilities and declinedDefaults), and capability_policy
// rules and catalogue defaults -- is rewritten to the canonical reference, and
// any in_process.release-gate-read catalogue entry is folded conservatively
// into api_scopes.presets.release-gate-read.
func migrateReleaseGateReadGrants(ctx context.Context, tx *sql.Tx) error {
	if err := rewriteLegacyGrantsMulti(releaseGateReadRenames)(ctx, tx); err != nil {
		return err
	}
	if err := rewriteCapabilityListColumnMulti(ctx, tx, "repo_config", []string{"owner", "name"}, "ci_capabilities", releaseGateReadRenames); err != nil {
		return err
	}
	if err := rewriteConsoleSessionDeclinedDefaults(ctx, tx, releaseGateReadRenames); err != nil {
		return err
	}
	if err := rewritePolicyRenames(ctx, tx, releaseGateReadRenames, APIRef(ReleaseGateReadCapability)); err != nil {
		return err
	}
	return foldRetiredCatalogueInProcess(map[string]string{
		ReleaseGateReadCapability: ReleaseGateReadCapability,
	}, nil)(ctx, tx)
}
