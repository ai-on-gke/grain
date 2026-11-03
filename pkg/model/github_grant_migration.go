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
	"strconv"
	"strings"
)

// migration103GitRepoRef and migration103SandboxGrant are migration 103's
// own copies of what the retired names became, so a later change to the
// constants never changes what it did.
const (
	migration103GitRepoRef   = "https://grain.dev/capabilities/git#git-repo"
	migration103SandboxGrant = "https://grain.dev/capabilities/github#sandbox-repo={}"
)

// githubGrantRenames is migration 103's renames, read off the store
// because github-credential:<name> is parameterized (as migration 88's
// repo-read:<repo> was): each github-credential:<name> to git#git-repo
// {credential: <name>, repo: "*"}, and github-sandbox to
// github#sandbox-repo.
//
// The repo is "*" -- every repository the task's run reaches, on every
// host -- because that is how the retired provider scoped it: the git
// proxy asked the store which credential the sandbox's live task named
// (Store.GitCredentialOverride) and the grant answered for whatever
// owner/repo the request was for, the task's own target, its writes and
// its reads alike. Rewriting it to the task's target alone would have
// sent a run's pushes to its other repositories out with the default
// credential instead, and a template's, a schedule's or a default
// capability list's grant has no task to take a target from at all. A
// name that is not a valid credential name is left alone; no provider
// was ever registered for one.
func githubGrantRenames(ctx context.Context, tx *sql.Tx) (map[string][]string, error) {
	renames := map[string][]string{"github-sandbox": {migration103SandboxGrant}}
	add := func(name string) {
		name = strings.TrimSpace(name)
		credential, ok := strings.CutPrefix(name, "github-credential:")
		if !ok || !ValidGitCredentialName(credential) {
			return
		}
		renames[name] = []string{migration103GitRepoRef + `={"credential":` + strconv.Quote(credential) + `,"repo":"*"}`}
	}
	if err := scanStoredGrantNames(ctx, tx, add); err != nil {
		return nil, fmt.Errorf("finding retired github-credential grants: %w", err)
	}
	// The capability policy's rules and catalogue defaults name them too.
	var stored string
	err := tx.QueryRowContext(ctx, "SELECT `capability_policy` FROM `grain_config` WHERE `id` = 1").Scan(&stored)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("grain_config.capability_policy: reading: %w", err)
	}
	if strings.TrimSpace(stored) != "" {
		if policy, err := DecodeCapabilityPolicy(stored); err == nil {
			for _, r := range policy.Rules {
				add(r.Capability)
			}
			if cat := policy.Catalogue; cat != nil {
				for _, list := range [][]string{cat.Defaults.Task, cat.Defaults.CI} {
					for _, name := range list {
						add(name)
					}
				}
				for _, rd := range cat.Defaults.Repos {
					for _, list := range [][]string{rd.Task, rd.CI} {
						for _, name := range list {
							add(name)
						}
					}
				}
			}
		}
	}
	return renames, nil
}

// migrateGitHubGrants is schema migration 103's Up: the in-process
// github-credential:<name> and github-sandbox providers are retired --
// the first onto the git server's git-repo {credential, repo}, which the
// git proxy reads off the live task as it read the bare grant, and the
// second onto the in-process github server's sandbox-repo, which creates
// the same repository with the same token. Every stored grant of one is
// rewritten (githubGrantRenames): the three grant tables, the
// deployment's and each repository's default capabilities, the standing
// sessions', repo_config.ci_capabilities, and the capability policy's
// rules (to the method's reference) and catalogue defaults.
func migrateGitHubGrants(ctx context.Context, tx *sql.Tx) error {
	renames, err := githubGrantRenames(ctx, tx)
	if err != nil {
		return err
	}
	if err := rewriteLegacyGrantsMulti(renames)(ctx, tx); err != nil {
		return err
	}
	if err := rewriteCapabilityListColumnMulti(ctx, tx, "repo_config", []string{"owner", "name"}, "ci_capabilities", renames); err != nil {
		return err
	}
	for name, to := range renames {
		if err := rewritePolicyRenames(ctx, tx, map[string][]string{name: to}, CapabilityGrantRef(to[0])); err != nil {
			return err
		}
	}
	return nil
}
