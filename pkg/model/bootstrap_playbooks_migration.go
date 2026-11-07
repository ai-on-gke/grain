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

// bootstrapPlaybooksRenames is migration 118's renames: the retired bare
// bootstrap-playbooks capability to config-map's built-in bootstrap map. Kept
// as the migration's own copy so later alias additions do not alter what
// migration 118 rewrote.
var bootstrapPlaybooksRenames = map[string][]string{
	"bootstrap-playbooks": {BootstrapPlaybooksGrant},
}

// migrateBootstrapPlaybooksGrants is schema migration 118's Up: the in-process
// bootstrap-playbooks provider is retired onto the built-in config-map server's
// bootstrap map, rewriting every stored grant across the three grant tables,
// deployment and repository default capabilities, standing console sessions,
// repo_config.ci_capabilities, and capability_policy deny rules and catalogue
// defaults. Non-deny rules are never rewritten onto ConfigMapRef because
// config-map#config-map is shared with operator config maps and secret maps
// (an AllowUntrusted rule on bootstrap-playbooks must not open secrets to
// untrusted runs).
func migrateBootstrapPlaybooksGrants(ctx context.Context, tx *sql.Tx) error {
	if err := rewriteLegacyGrantsMulti(bootstrapPlaybooksRenames)(ctx, tx); err != nil {
		return err
	}
	if err := rewriteCapabilityListColumnMulti(ctx, tx, "repo_config", []string{"owner", "name"}, "ci_capabilities", bootstrapPlaybooksRenames); err != nil {
		return err
	}
	return rewritePolicyRenamesDenyOnly(ctx, tx, bootstrapPlaybooksRenames, ConfigMapRef)
}
