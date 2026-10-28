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
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Task branches are agent-authored: whoever ran the task controls every
// file on one, its .grain/workflows included. So nothing that treats a
// branch as one grain trusts -- a watched base branch, a trusted release
// run with the repository's release secrets mounted -- may ever apply to
// one (grain/task-949). IsTaskBranch and ReleaseBranches are the two
// halves of that line: the first says what a task branch looks like, the
// second which branches a release may run from at all.

// historicalTaskBranch is BranchName's grain/task-<id>, with an optional
// {slug} tail.
var historicalTaskBranch = regexp.MustCompile(`^grain/task-[0-9]+(-[a-z0-9-]*)?$`)

// anyDeploymentTaskBranch is DefaultTaskBranchTemplate's
// grain/<deployment>/task-<id>, for any deployment rather than this one:
// two deployments working in the same repository each push their own,
// and neither's is a branch the other should trust.
var anyDeploymentTaskBranch = regexp.MustCompile(`^grain/[a-z0-9]([a-z0-9-]*[a-z0-9])?/task-[0-9]+(-[a-z0-9-]*)?$`)

// IsTaskBranch reports whether name is a branch grain gives its tasks:
// the historical grain/task-<id>, grain/<deployment>/task-<id> for any
// deployment, or the deployment's own task branch template expanded for
// some numeric id (FormatTaskBranchName), with or without its {slug}.
func IsTaskBranch(template, instanceID, deploymentID, name string) bool {
	name = strings.TrimPrefix(strings.TrimSpace(name), "refs/heads/")
	if name == "" {
		return false
	}
	if historicalTaskBranch.MatchString(name) || anyDeploymentTaskBranch.MatchString(name) {
		return true
	}
	if template == "" || !strings.Contains(template, "{id}") {
		return false
	}
	pattern := regexp.QuoteMeta(template)
	pattern = strings.ReplaceAll(pattern, `-\{slug\}`, `(-[a-z0-9-]*)?`)
	pattern = strings.ReplaceAll(pattern, `\{slug\}`, `[a-z0-9-]*`)
	pattern = strings.ReplaceAll(pattern, `\{id\}`, `[0-9]+`)
	pattern = strings.ReplaceAll(pattern, `\{deployment\}`, regexp.QuoteMeta(deploymentID))
	pattern = strings.ReplaceAll(pattern, `\{instance\}`, regexp.QuoteMeta(instanceID))
	re, err := regexp.Compile("^" + pattern + "$")
	if err != nil {
		return false
	}
	return re.MatchString(name)
}

// IsTaskBranch is the package's IsTaskBranch with this deployment's own
// template, deployment id and installation id read from the store.
func (s *Store) IsTaskBranch(ctx context.Context, name string) (bool, error) {
	var template, deploymentID, instanceID string
	err := s.db.QueryRowContext(ctx,
		"SELECT `task_branch_template`, `deployment_id` FROM `grain_config` WHERE `id` = 1").Scan(&template, &deploymentID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("reading task branch template: %w", err)
	}
	err = s.db.QueryRowContext(ctx, "SELECT `id` FROM `grain_instance` WHERE `singleton` = 1").Scan(&instanceID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("reading grain instance id: %w", err)
	}
	return IsTaskBranch(template, instanceID, EffectiveDeploymentID(deploymentID, instanceID), name), nil
}

// ReleaseBranches is every branch of repo a trusted release may run
// from: its default branch (which the caller reads from GitHub and
// hands in; "" leaves it out), the branches it ports between
// (RepoConfig.PairedBranches), and both branches of each of its releases
// that has not merged yet. In that order, without repeats, and never a
// task branch, whatever a configuration says.
func (s *Store) ReleaseBranches(ctx context.Context, repo RepoRef, defaultBranch string) ([]string, error) {
	var out []string
	add := func(b string) {
		b = strings.TrimSpace(b)
		if b != "" && !slices.Contains(out, b) {
			out = append(out, b)
		}
	}
	add(defaultBranch)
	cfg, err := s.GetRepoConfig(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("reading %s's paired branches: %w", repo, err)
	}
	if cfg != nil {
		for _, b := range cfg.PairedBranches() {
			add(b)
		}
	}
	releases, err := s.ListReleases(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("reading %s's releases: %w", repo, err)
	}
	for _, rel := range releases {
		if rel.Status != ReleaseMerged {
			add(rel.ProdBranch())
			add(rel.LatestBranch())
		}
	}
	trusted := out[:0]
	for _, b := range out {
		task, err := s.IsTaskBranch(ctx, b)
		if err != nil {
			return nil, err
		}
		if !task {
			trusted = append(trusted, b)
		}
	}
	return trusted, nil
}

// PurgeTaskBranchBaseRows deletes every BaseBranchCI, BaseBranchRelease and
// BaseBranchDispatch row whose branch is a task branch (IsTaskBranch), reporting the
// repository and branch of each one it removed.
//
// Such a row is one a stacked review or fix merge used to write before
// grain/task-949 -- it merged into the reviewed task's branch, and that
// branch was then watched, judged, repaired and released as though it
// were a base. Nothing writes one now; this clears the ones already
// written -- each of which cost GitHub reads of a long-dead branch's
// head every cycle, enough to exhaust the deployment's hourly rate limit
// -- and is cheap enough to run every cycle so that one that somehow
// appears again does not survive the next.
func (s *Store) PurgeTaskBranchBaseRows(ctx context.Context) ([]string, error) {
	var removed []string
	ciRows, err := s.ListBaseBranchCI(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range ciRows {
		task, err := s.IsTaskBranch(ctx, row.Branch)
		if err != nil {
			return removed, err
		}
		if !task {
			continue
		}
		if err := s.deleteBaseBranchRow(ctx, "base_branch_ci", row.Repo, row.Branch); err != nil {
			return removed, err
		}
		removed = append(removed, row.Repo.String()+" "+row.Branch)
	}
	releaseRows, err := s.ListBaseBranchRelease(ctx)
	if err != nil {
		return removed, err
	}
	for _, row := range releaseRows {
		task, err := s.IsTaskBranch(ctx, row.Branch)
		if err != nil {
			return removed, err
		}
		if !task {
			continue
		}
		if err := s.deleteBaseBranchRow(ctx, "base_branch_release", row.Repo, row.Branch); err != nil {
			return removed, err
		}
		removed = append(removed, row.Repo.String()+" "+row.Branch+" (release)")
	}
	dispatchRows, err := s.ListBaseBranchDispatch(ctx)
	if err != nil {
		return removed, err
	}
	for _, row := range dispatchRows {
		task, err := s.IsTaskBranch(ctx, row.Branch)
		if err != nil {
			return removed, err
		}
		if !task {
			continue
		}
		if err := s.deleteBaseBranchRow(ctx, "base_branch_dispatch", row.Repo, row.Branch); err != nil {
			return removed, err
		}
		removed = append(removed, row.Repo.String()+" "+row.Branch+" (dispatch)")
	}
	return removed, nil
}

// deleteBaseBranchRow removes one repository's branch from one of the
// base-branch tables PurgeTaskBranchBaseRows clears.
func (s *Store) deleteBaseBranchRow(ctx context.Context, table string, repo RepoRef, branch string) error {
	return s.write(ctx, "forget "+table+" for "+repo.String()+" "+branch, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"DELETE FROM `"+table+"` WHERE `owner` = ? AND `name` = ? AND `branch` = ?",
			repo.StoredOwner(), repo.Name, branch)
		return err
	})
}
