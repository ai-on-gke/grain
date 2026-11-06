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

// selfDebugRenames is migration 115's renames: the retired self-debug
// capability to the api:self-debug preset (every task's record, metrics,
// pipeline and deployment status, which it already granted as an alias)
// and the in-process grain server's #source method (grain's own source
// checkout). The migration's own copy, as rewriteLegacyGrants asks, so a
// later change to SelfDebugGrants never changes what it did.
var selfDebugRenames = map[string][]string{
	"self-debug": {APIGrantPrefix + "self-debug", GrainCapabilityServerID + "#source={}"},
}

// migrateSelfDebugGrants is schema migration 115's Up: the in-process
// self-debug provider is retired onto the api:self-debug preset and
// https://grain.dev/capabilities/grain#source, so every stored grant of
// it -- the three grant tables, the deployment's and each repository's
// default capabilities, the standing sessions' capabilities,
// repo_config.ci_capabilities, and the capability policy's catalogue
// defaults wherever one was written -- is rewritten to both. Together
// they open exactly what self-debug did, so nothing a rewritten row
// grants changes. A policy rule cannot name the bare self-debug (rules
// name capability references), so only the catalogue's lists change
// there; a catalogue's in_process.self-debug entry keeps binding both
// grants (judgeRetiredInProcess).
func migrateSelfDebugGrants(ctx context.Context, tx *sql.Tx) error {
	if err := rewriteLegacyGrantsMulti(selfDebugRenames)(ctx, tx); err != nil {
		return err
	}
	if err := rewriteCapabilityListColumnMulti(ctx, tx, "repo_config", []string{"owner", "name"}, "ci_capabilities", selfDebugRenames); err != nil {
		return err
	}
	return rewritePolicyRenames(ctx, tx, selfDebugRenames, GrainCapabilityServerID+"#source")
}
