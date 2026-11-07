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

// githubSandboxRenames is migration 124's renames: the retired bare
// github-sandbox capability to the in-process github server's sandbox-repo
// method. Kept as the migration's own copy so later alias additions do not
// alter what migration 124 rewrote.
var githubSandboxRenames = map[string][]string{
	LegacyGitHubSandboxCapability: {GitHubSandboxRepoGrant},
}

// migrateGitHubSandboxGrants is schema migration 124's Up: rewrites stored
// github-sandbox grants across the three grant tables, deployment and
// repository default_capabilities, repo_config.ci_capabilities, standing
// console_sessions and capability_policy rules/catalogue defaults to
// https://grain.dev/capabilities/github#sandbox-repo={}. The github server's
// sandbox-repo method is dedicated to what github-sandbox granted, so policy
// rules of every kind are renamed onto GitHubSandboxRepoRef.
func migrateGitHubSandboxGrants(ctx context.Context, tx *sql.Tx) error {
	if err := rewriteLegacyGrantsMulti(githubSandboxRenames)(ctx, tx); err != nil {
		return err
	}
	if err := rewriteCapabilityListColumnMulti(ctx, tx, "repo_config", []string{"owner", "name"}, "ci_capabilities", githubSandboxRenames); err != nil {
		return err
	}
	return rewritePolicyRenames(ctx, tx, githubSandboxRenames, GitHubSandboxRepoRef)
}
