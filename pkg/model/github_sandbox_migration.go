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

// githubSandboxRenames is migration 120's rename: the retired bare
// github-sandbox capability to the in-process github server's sandbox-repo
// method. Kept as the migration's own copy so later alias changes do not
// alter what migration 120 rewrote.
var githubSandboxRenames = map[string][]string{
	"github-sandbox": {GitHubSandboxRepoGrant},
}

// migrateGitHubSandboxGrants is schema migration 120's Up: the in-process
// github-sandbox provider is retired onto the in-process github server's
// sandbox-repo method, rewriting every stored grant across the three grant
// tables, deployment and repository default capabilities, standing console
// sessions, repo_config.ci_capabilities, and capability_policy rules and
// catalogue defaults.
func migrateGitHubSandboxGrants(ctx context.Context, tx *sql.Tx) error {
	if err := rewriteLegacyGrantsMulti(githubSandboxRenames)(ctx, tx); err != nil {
		return err
	}
	if err := rewriteCapabilityListColumnMulti(ctx, tx, "repo_config", []string{"owner", "name"}, "ci_capabilities", githubSandboxRenames); err != nil {
		return err
	}
	return rewritePolicyRenames(ctx, tx, githubSandboxRenames, GitHubSandboxRepoRef)
}
