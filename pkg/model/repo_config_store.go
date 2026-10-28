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
	"slices"
	"strings"
)

// GetRepoConfig reads repo's own configuration, or nil (with no error)
// when the repo has none -- the same "nothing has configured one yet"
// nil GetQualificationPlan returns, and the same reading
// ui.(*Client).defaultCapabilities takes: a repo with no row adds
// nothing to what the deployment already defaults.
func (s *Store) GetRepoConfig(ctx context.Context, repo RepoRef) (*RepoConfig, error) {
	return getRepoConfig(ctx, s.db, repo)
}

// RepoUsesGrainCI reports whether the repository is configured to use Grain CI.
func (s *Store) RepoUsesGrainCI(ctx context.Context, repo RepoRef) (bool, error) {
	return querierRepoUsesGrainCI(ctx, s.db, repo)
}

func querierRepoUsesGrainCI(ctx context.Context, q querier, repo RepoRef) (bool, error) {
	if IsConfigRepo(repo) {
		return true, nil
	}
	repoCfg, err := getRepoConfig(ctx, q, repo)
	if err != nil {
		return false, err
	}
	if repoCfg != nil && repoCfg.CIMode != "" {
		return repoCfg.CIMode == CIModeGrain, nil
	}
	if repoCfg != nil && repoCfg.IgnoreGitHubCI {
		return true, nil
	}
	return false, nil
}

func getRepoConfig(ctx context.Context, q querier, repo RepoRef) (*RepoConfig, error) {
	var cloneURL, defaultCapabilities, promptExtension, setupCommand, ciCapabilities, releaseSecrets, unfixableChecks, baseBranchCIJobs, postMergeDispatchJobs string
	var declaredPromptExtension, declaredSetupCommand, workOwnership, workOverrides, ciMode, repairTemplateID, portBranches, declaredPortBranches, portTypes, declaredPortTypes, gitCredential, mergeRecheck string
	var releaseWorkflowsDisabled, declarations, requireBranchUpToDate, ignoreGitHubCI, watchDefaultBranch, mergeOnGreen, baseRepairBypassesHumanGates bool
	err := q.QueryRowContext(ctx,
		"SELECT `clone_url`,`default_capabilities`,`prompt_extension`,`setup_command`,`ci_capabilities`,`release_workflows_disabled`,`work_ownership`,`work_overrides`,`release_secrets`,`declarations`,`require_branch_up_to_date`,`ignore_github_ci`,`watch_default_branch`,`unfixable_checks`,`base_branch_ci_jobs`,`post_merge_dispatch_jobs`,`declared_prompt_extension`,`declared_setup_command`,`ci_mode`,`repair_template_id`,`port_branches`,`declared_port_branches`,`port_types`,`declared_port_types`,`git_credential`,`merge_on_green`,`base_repair_bypasses_human_gates`,`merge_recheck` FROM `repo_config` WHERE `owner` = ? AND `name` = ?",
		repo.StoredOwner(), repo.Name).Scan(&cloneURL, &defaultCapabilities, &promptExtension, &setupCommand, &ciCapabilities, &releaseWorkflowsDisabled, &workOwnership, &workOverrides, &releaseSecrets, &declarations, &requireBranchUpToDate, &ignoreGitHubCI, &watchDefaultBranch, &unfixableChecks, &baseBranchCIJobs, &postMergeDispatchJobs, &declaredPromptExtension, &declaredSetupCommand, &ciMode, &repairTemplateID, &portBranches, &declaredPortBranches, &portTypes, &declaredPortTypes, &gitCredential, &mergeOnGreen, &baseRepairBypassesHumanGates, &mergeRecheck)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading repo config for %s: %w", repo, err)
	}
	return &RepoConfig{
		Repo:                         repo,
		CloneURL:                     cloneURL,
		DefaultCapabilities:          splitCapabilities(defaultCapabilities),
		CICapabilities:               splitCapabilities(ciCapabilities),
		Work:                         readWorkOwnership(workOwnership, workOverrides, releaseWorkflowsDisabled),
		ReleaseSecrets:               splitCSV(releaseSecrets),
		PromptExtension:              promptExtension,
		SetupCommand:                 setupCommand,
		Declarations:                 declarations,
		RequireBranchUpToDate:        requireBranchUpToDate,
		IgnoreGitHubCI:               ignoreGitHubCI,
		WatchDefaultBranch:           watchDefaultBranch,
		MergeOnGreen:                 mergeOnGreen,
		BaseRepairBypassesHumanGates: baseRepairBypassesHumanGates,
		MergeRecheck:                 MergeRecheckMode(mergeRecheck),
		UnfixableChecks:              splitCSV(unfixableChecks),
		BaseBranchCIJobs:             splitCSV(baseBranchCIJobs),
		PostMergeDispatchJobs:        splitCSV(postMergeDispatchJobs),
		DeclaredPromptExtension:      declaredPromptExtension,
		DeclaredSetupCommand:         declaredSetupCommand,
		CIMode:                       CIMode(ciMode),
		RepairTemplateID:             repairTemplateID,
		PortBranches:                 splitCSV(portBranches),
		DeclaredPortBranches:         splitCSV(declaredPortBranches),
		PortTypes:                    DecodePortTypes(portTypes),
		DeclaredPortTypes:            DecodePortTypes(declaredPortTypes),
		GitCredential:                gitCredential,
	}, nil
}

// readWorkOwnership assembles RepoConfig.Work out of its own two
// columns, falling back to the one column that used to hold this
// decision.
//
// The fallback fires only for a row written before those columns
// existed -- both of them empty, which is a row nothing has said
// anything about since the upgrade -- and it reads the old bool as what
// it always meant: an explicit "this deployment does not publish from
// this repository", which is now the release item's own override. A row
// that has been written since is read from the new columns alone, so a
// later decision is never overruled by the mirror putRepoConfig leaves
// behind for rollback.
func readWorkOwnership(ownership, overrides string, legacyReleaseDisabled bool) WorkOwnership {
	if ownership == "" && overrides == "" {
		if legacyReleaseDisabled {
			return WorkOwnership{Overrides: map[WorkItem]Ownership{ReleaseItem: OwnershipOff}}
		}
		return WorkOwnership{}
	}
	// A value outside the three is a row written by a route that does
	// not exist; ParseOwnership's error is the honest answer and
	// OwnershipUnset -- "follow what a deployment does by default" -- is
	// the safe reading of it, because the alternative is a repository
	// whose work silently stops over an unreadable string.
	state, _ := ParseOwnership(ownership)
	return WorkOwnership{Default: state, Overrides: ParseWorkOverrides(overrides)}
}

// ListRepoConfigs returns every repo that has a configuration of its
// own, ordered by repo so a listing is stable rather than dependent on
// insertion order. Every entry is non-Empty: PutRepoConfig deletes a row
// that would say nothing, so this is exactly the set of repos that add
// something to the deployment-wide answer -- which is what
// ui.capabilityStatuses reports as CapabilityStatus.DefaultRepos and
// what GET /api/config hands the new-task form.
func (s *Store) ListRepoConfigs(ctx context.Context) ([]RepoConfig, error) {
	var out []RepoConfig
	err := each(ctx, s.db,
		"SELECT `owner`,`name`,`clone_url`,`default_capabilities`,`prompt_extension`,`setup_command`,`ci_capabilities`,`release_workflows_disabled`,`work_ownership`,`work_overrides`,`release_secrets`,`declarations`,`require_branch_up_to_date`,`ignore_github_ci`,`watch_default_branch`,`unfixable_checks`,`base_branch_ci_jobs`,`post_merge_dispatch_jobs`,`declared_prompt_extension`,`declared_setup_command`,`ci_mode`,`repair_template_id`,`port_branches`,`declared_port_branches`,`port_types`,`declared_port_types`,`git_credential`,`merge_on_green`,`base_repair_bypasses_human_gates`,`merge_recheck` FROM `repo_config` ORDER BY `owner`,`name`",
		nil,
		func(rows *sql.Rows) error {
			var c RepoConfig
			var defaultCapabilities, ciCapabilities, releaseSecrets, unfixableChecks, baseBranchCIJobs, postMergeDispatchJobs, ciMode, repairTemplateID, portBranches, declaredPortBranches, portTypes, declaredPortTypes string
			var workOwnership, workOverrides, mergeRecheck string
			var legacyReleaseDisabled bool
			if err := rows.Scan(&c.Repo.Owner, &c.Repo.Name, &c.CloneURL, &defaultCapabilities,
				&c.PromptExtension, &c.SetupCommand, &ciCapabilities, &legacyReleaseDisabled,
				&workOwnership, &workOverrides, &releaseSecrets,
				&c.Declarations, &c.RequireBranchUpToDate, &c.IgnoreGitHubCI, &c.WatchDefaultBranch,
				&unfixableChecks, &baseBranchCIJobs, &postMergeDispatchJobs,
				&c.DeclaredPromptExtension, &c.DeclaredSetupCommand, &ciMode, &repairTemplateID, &portBranches, &declaredPortBranches, &portTypes, &declaredPortTypes, &c.GitCredential, &c.MergeOnGreen, &c.BaseRepairBypassesHumanGates, &mergeRecheck); err != nil {
				return err
			}
			c.Repo = RepoFromStored(c.Repo.Owner, c.Repo.Name)
			c.MergeRecheck = MergeRecheckMode(mergeRecheck)
			c.Work = readWorkOwnership(workOwnership, workOverrides, legacyReleaseDisabled)
			c.DefaultCapabilities = splitCapabilities(defaultCapabilities)
			c.CICapabilities = splitCapabilities(ciCapabilities)
			c.ReleaseSecrets = splitCSV(releaseSecrets)
			c.UnfixableChecks = splitCSV(unfixableChecks)
			c.BaseBranchCIJobs = splitCSV(baseBranchCIJobs)
			c.PostMergeDispatchJobs = splitCSV(postMergeDispatchJobs)
			c.CIMode = CIMode(ciMode)
			c.RepairTemplateID = repairTemplateID
			c.PortBranches = splitCSV(portBranches)
			c.DeclaredPortBranches = splitCSV(declaredPortBranches)
			c.PortTypes = DecodePortTypes(portTypes)
			c.DeclaredPortTypes = DecodePortTypes(declaredPortTypes)
			out = append(out, c)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing repo configs: %w", err)
	}
	return out, nil
}

// PutRepoConfig replaces repo's own configuration wholesale -- one row
// per repo, replaced rather than diffed, the same shape PutConfig and
// PutQualificationPlan already have.
//
// A config that says nothing (RepoConfig.Empty) deletes the row instead
// of writing an empty one, so a repo whose last default capability is
// unticked leaves no trace behind to be listed, reported, or wondered
// about later.
func (s *Store) PutRepoConfig(ctx context.Context, c RepoConfig) error {
	return s.write(ctx, "update repo config for "+c.Repo.String(), func(tx *sql.Tx) error {
		return putRepoConfig(ctx, tx, c)
	})
}

func putRepoConfig(ctx context.Context, tx *sql.Tx, c RepoConfig) error {
	if c.Empty() {
		_, err := tx.ExecContext(ctx,
			"DELETE FROM `repo_config` WHERE `owner` = ? AND `name` = ?",
			c.Repo.StoredOwner(), c.Repo.Name)
		return err
	}
	// The legacy column is written as a mirror of what the release item
	// resolves to -- never read back while the two columns beside it say
	// anything, and there so that a deployment rolled back onto a binary
	// that knows only this column does not start publishing again from a
	// repository somebody deliberately switched off.
	legacyReleaseDisabled := !RunsWorkItem(&c, ReleaseItem)
	_, err := tx.ExecContext(ctx,
		"REPLACE INTO `repo_config` (`owner`,`name`,`clone_url`,`default_capabilities`,`prompt_extension`,`setup_command`,`ci_capabilities`,`release_workflows_disabled`,`work_ownership`,`work_overrides`,`release_secrets`,`declarations`,`require_branch_up_to_date`,`ignore_github_ci`,`watch_default_branch`,`unfixable_checks`,`base_branch_ci_jobs`,`post_merge_dispatch_jobs`,`declared_prompt_extension`,`declared_setup_command`,`ci_mode`,`repair_template_id`,`port_branches`,`declared_port_branches`,`port_types`,`declared_port_types`,`git_credential`,`merge_on_green`,`base_repair_bypasses_human_gates`,`merge_recheck`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
		c.Repo.StoredOwner(), c.Repo.Name, c.CloneURL, joinCapabilities(c.DefaultCapabilities), c.PromptExtension, c.SetupCommand, joinCapabilities(c.CICapabilities), legacyReleaseDisabled, string(c.Work.Default), EncodeWorkOverrides(c.Work.Overrides), joinCSV(c.ReleaseSecrets), c.Declarations, c.RequireBranchUpToDate, c.IgnoreGitHubCI, c.WatchDefaultBranch, joinCSV(c.UnfixableChecks), joinCSV(c.BaseBranchCIJobs), joinCSV(c.PostMergeDispatchJobs), c.DeclaredPromptExtension, c.DeclaredSetupCommand, string(c.CIMode), c.RepairTemplateID, joinCSV(c.PortBranches), joinCSV(c.DeclaredPortBranches), EncodePortTypes(c.PortTypes), EncodePortTypes(c.DeclaredPortTypes), c.GitCredential, c.MergeOnGreen, c.BaseRepairBypassesHumanGates, string(c.MergeRecheck))
	return err
}

// EncodePortTypes is how RepoConfig.PortTypes and DeclaredPortTypes are
// stored: one "<from>-><to>:<type>,<type>" entry per direction, sorted
// and space-separated. A git ref name can hold neither a space nor a
// colon, so no branch name can be mistaken for the separators.
func EncodePortTypes(m map[string][]string) string {
	var entries []string
	for dir, types := range m {
		if len(types) > 0 {
			entries = append(entries, dir+":"+strings.Join(types, ","))
		}
	}
	slices.Sort(entries)
	return strings.Join(entries, " ")
}

// DecodePortTypes reads EncodePortTypes back; nil for an empty column.
func DecodePortTypes(s string) map[string][]string {
	var out map[string][]string
	for _, entry := range strings.Fields(s) {
		dir, types, ok := strings.Cut(entry, ":")
		if !ok || dir == "" || types == "" {
			continue
		}
		if out == nil {
			out = map[string][]string{}
		}
		out[dir] = strings.Split(types, ",")
	}
	return out
}
