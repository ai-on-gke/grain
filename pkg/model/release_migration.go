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

// releaseRenames is migration 101's renames: each retired release
// provider to its method of the in-process release server. The
// migration's own copy, as rewriteLegacyGrants asks, so a later method
// never changes what it did.
var releaseRenames = map[string][]string{
	"image-signing":   {ImageSigningRef + "={}"},
	"registry-push":   {RegistryPushRef + "={}"},
	"package-publish": {PackagePublishRef + "={}"},
}

// migrateReleaseGrants is schema migration 101's Up: the in-process
// image-signing, registry-push and package-publish providers are retired
// onto the in-process release server, so every stored grant of one --
// repo_config.ci_capabilities above all, where an operator enabled it
// for a repository's CI, and the three grant tables, the default
// capabilities, the standing sessions' and the capability policy's
// catalogue defaults wherever one was written -- is rewritten to its
// method. The method places what the provider placed, on the same
// events (model.CIGrantRule), so nothing a rewritten row grants
// changes.
func migrateReleaseGrants(ctx context.Context, tx *sql.Tx) error {
	if err := rewriteLegacyGrantsMulti(releaseRenames)(ctx, tx); err != nil {
		return err
	}
	if err := rewriteCapabilityListColumnMulti(ctx, tx, "repo_config", []string{"owner", "name"}, "ci_capabilities", releaseRenames); err != nil {
		return err
	}
	for name, to := range releaseRenames {
		if err := rewritePolicyRenames(ctx, tx, map[string][]string{name: to}, CapabilityGrantRef(to[0])); err != nil {
			return err
		}
	}
	return nil
}
