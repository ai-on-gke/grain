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
	"fmt"
)

// ReleasesFromGitHubActions reports whether repo's releases are promoted
// by GitHub Actions (.github/workflows/promote-release.yml, dispatched on
// a person's Inbox Approve) rather than by grain CI's promote-release
// job: the question the release-pending reconciler and ui.Client's
// DecideRelease and RollbackRelease all ask.
//
// An explicit CI Mode decides it: "github" is Actions, anything else is
// not. A repository that never set one is an Actions repository -- the
// mode the repo page shows for it (RepoConfig.EffectiveCIMode) and the
// one the merge queue gates it on -- unless grain CI is in fact on for
// it: the config repo, the legacy IgnoreGitHubCI switch, or workflows it
// declares under .grain/workflows. Reading EffectiveRepoConfig's raw
// CIMode instead treated every unset repository as "not Actions", so no
// release.pending was ever raised for it and Approve answered "grain CI
// is not enabled" (docs/test-runs/2026-11-04-release-approval.md, F1).
func (s *Store) ReleasesFromGitHubActions(ctx context.Context, repo RepoRef) (bool, error) {
	if IsConfigRepo(repo) {
		return false, nil
	}
	cfg, err := s.GetRepoConfig(ctx, repo)
	if err != nil {
		return false, fmt.Errorf("reading %s's CI mode: %w", repo, err)
	}
	if cfg != nil && cfg.CIMode != "" {
		return cfg.CIMode == CIModeGitHub, nil
	}
	if cfg != nil && cfg.IgnoreGitHubCI {
		return false, nil
	}
	wfs, err := s.GetRepoWorkflows(ctx, repo)
	if err != nil {
		return false, fmt.Errorf("reading %s's grain CI workflows: %w", repo, err)
	}
	return len(wfs) == 0, nil
}
