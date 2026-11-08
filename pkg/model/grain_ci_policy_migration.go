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

// grainCIPolicyRenames is migration 125's renames: the retired bare grain-ci
// to grain#ci. The migration's own copy, as rewriteLegacyGrants asks.
var grainCIPolicyRenames = map[string][]string{
	GrainCICapability: {GrainCIGrant},
}

// migrateGrainCIPolicy is schema migration 125's Up. Migration 96 retired
// the in-process grain-ci onto grain#ci and rewrote the grant tables, the
// default capability lists, the standing sessions and
// repo_config.ci_capabilities, but not grain_config.capability_policy: a
// catalogue whose task, CI or per-repository defaults named grain-ci kept
// the bare alias through every later upgrade, in place and through a
// state-repo import alike, while every other retirement (97, 100, 101, 103,
// 115, 118) rewrote the policy with the rest. This finishes 96's rewrite
// there, the way migration 100 rewrites the model proxies' (a rule naming
// grain-ci names grain#ci, and the catalogue's defaults are renamed as every
// other stored list is). grain#ci is what the bare name already resolved to,
// so nothing a rewritten entry grants changes.
func migrateGrainCIPolicy(ctx context.Context, tx *sql.Tx) error {
	return rewritePolicyRenames(ctx, tx, grainCIPolicyRenames, GrainCIRef)
}
