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

// modelProxyRenames is migration 100's renames: each retired
// <vendor>-proxy to model-proxy {vendor}. The migration's own copy, as
// rewriteLegacyGrants asks, so a later vendor never changes what it did.
var modelProxyRenames = map[string][]string{
	"claude-proxy": {ModelProxyRef + `={"vendor":"claude"}`},
	"openai-proxy": {ModelProxyRef + `={"vendor":"openai"}`},
	"gemini-proxy": {ModelProxyRef + `={"vendor":"gemini"}`},
}

// migrateModelProxyGrants is schema migration 100's Up: the in-process
// claude-proxy, openai-proxy and gemini-proxy providers are retired onto
// the in-process model-proxy server, so every stored grant of one is
// rewritten to model-proxy {vendor} -- the three grant tables, the
// deployment's and each repository's default capabilities, the standing
// sessions', repo_config.ci_capabilities, and the capability policy's
// rules and catalogue defaults. The server forwards with the same team
// connection the providers did, so nothing a rewritten row grants
// changes.
func migrateModelProxyGrants(ctx context.Context, tx *sql.Tx) error {
	if err := rewriteLegacyGrantsMulti(modelProxyRenames)(ctx, tx); err != nil {
		return err
	}
	if err := rewriteCapabilityListColumnMulti(ctx, tx, "repo_config", []string{"owner", "name"}, "ci_capabilities", modelProxyRenames); err != nil {
		return err
	}
	return rewritePolicyRenames(ctx, tx, modelProxyRenames, ModelProxyRef)
}

// rewritePolicyRenames rewrites grain_config.capability_policy for a
// retirement: a rule naming one of renames' old names names ruleRef, the
// method it became (a rule names a method, never its arguments), and the
// catalogue's task and CI defaults, the deployment's and each
// repository's, are renamed as every other stored list is.
func rewritePolicyRenames(ctx context.Context, tx *sql.Tx, renames map[string][]string, ruleRef string) error {
	return rewritePolicyRenamesOpt(ctx, tx, renames, ruleRef, false)
}

func rewritePolicyRenamesDenyOnly(ctx context.Context, tx *sql.Tx, renames map[string][]string, ruleRef string) error {
	return rewritePolicyRenamesOpt(ctx, tx, renames, ruleRef, true)
}

func rewritePolicyRenamesOpt(ctx context.Context, tx *sql.Tx, renames map[string][]string, ruleRef string, denyOnly bool) error {
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
		// Left as it is: DecodeCapabilityPolicy reports it on read, the
		// same with or without this.
		return nil
	}
	changed := false
	for i := range policy.Rules {
		if denyOnly && !policy.Rules[i].Deny {
			continue
		}
		if _, ok := renames[strings.TrimSpace(policy.Rules[i].Capability)]; ok {
			policy.Rules[i].Capability = ruleRef
			changed = true
		}
	}
	if cat := policy.Catalogue; cat != nil {
		rename := func(names []string) ([]string, bool) {
			if next, did := renameCapabilityListMulti(names, renames); did {
				return next, true
			}
			return names, false
		}
		var did bool
		if cat.Defaults.Task, did = rename(cat.Defaults.Task); did {
			changed = true
		}
		if cat.Defaults.CI, did = rename(cat.Defaults.CI); did {
			changed = true
		}
		for repo, rd := range cat.Defaults.Repos {
			var task, ci bool
			rd.Task, task = rename(rd.Task)
			rd.CI, ci = rename(rd.CI)
			if task || ci {
				cat.Defaults.Repos[repo] = rd
				changed = true
			}
		}
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
