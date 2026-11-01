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

package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// Session-proposed config changes (ui.ConfigChangeManager, ui's
// task_config_change.go) against the controller-local grain/config: the
// session pushed its proposal branch through the git proxy into this
// very checkout, so everything here is local git on m.repo.Dir(), under
// m.mu, the way StateRepoPullRequestClient's internal pull requests are.

var _ ui.ConfigChangeManager = (*StateManager)(nil)

func (m *StateManager) configChangeRepo() (dir, base string, err error) {
	if m.repo == nil {
		return "", "", errors.New("config repository is not available")
	}
	if remote, _ := m.externalConfigRepo(false); remote != "" {
		return "", "", fmt.Errorf("configuration is managed in the external repository %s: propose the change there", remote)
	}
	base = m.repo.Branch()
	if base == "" {
		base = "main"
	}
	return m.repo.Dir(), base, nil
}

// ProposeConfigChange implements ui.ConfigChangeManager: proposal's
// branch, diffed from where it left the base branch and validated as it
// would merge onto the base branch's tip now.
func (m *StateManager) ProposeConfigChange(ctx context.Context, proposal ui.ConfigChangeProposal) (*model.PendingConfigChange, error) {
	if m == nil {
		return nil, errors.New("config repository is not available")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	dir, base, err := m.configChangeRepo()
	if err != nil {
		return nil, err
	}
	head, err := localResolve(dir, proposal.Branch)
	if err != nil {
		return nil, fmt.Errorf("branch %s is not in %s: push it first: %w", proposal.Branch, model.ConfigRepo(), err)
	}
	if proposal.HeadSHA != "" && head != proposal.HeadSHA {
		return nil, fmt.Errorf("branch %s is at %s, not the %s that was pushed", proposal.Branch, shortCommitSHA(head), shortCommitSHA(proposal.HeadSHA))
	}
	baseSHA, err := localResolve(dir, base)
	if err != nil {
		return nil, fmt.Errorf("resolving the config repository's %s: %w", base, err)
	}
	mergeBase, err := localGit(dir, "merge-base", "--end-of-options", baseSHA, head)
	if err != nil {
		return nil, fmt.Errorf("branch %s shares no history with %s: %w", proposal.Branch, base, err)
	}
	names, err := localGit(dir, "diff", "--name-only", "--no-renames", "--end-of-options", mergeBase, head, "--")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(names, "\n") {
		if f = strings.TrimSpace(f); f != "" {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("branch %s changes nothing against %s: edit and commit something first", proposal.Branch, base)
	}
	diff, err := localDiff(dir, baseSHA, head)
	if err != nil {
		return nil, err
	}
	return &model.PendingConfigChange{
		Branch:      proposal.Branch,
		Base:        base,
		BaseSHA:     mergeBase,
		HeadSHA:     head,
		Summary:     proposal.Summary,
		Description: proposal.Description,
		Files:       files,
		Diff:        diff,
		Validation:  m.validateConfigMerge(ctx, dir, baseSHA, head),
	}, nil
}

// validateConfigMerge runs the pre-review suite over head merged onto
// baseSHA, as the tree approving it would leave.
func (m *StateManager) validateConfigMerge(ctx context.Context, dir, baseSHA, head string) model.ConfigChangeValidation {
	out, err := localGit(dir, "merge-tree", "--write-tree", "--end-of-options", baseSHA, head)
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return model.ConfigChangeValidation{Error: "the change conflicts with the current " + m.repo.Branch() +
				": rebase it onto origin/main and propose it again"}
		}
		return model.ConfigChangeValidation{Error: err.Error()}
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return model.ConfigChangeValidation{Error: "git merge-tree returned no tree"}
	}
	candidate, err := os.MkdirTemp("", "grain-config-change-")
	if err != nil {
		return model.ConfigChangeValidation{Error: err.Error()}
	}
	defer os.RemoveAll(candidate)
	if err := extractCommit(ctx, dir, fields[0], candidate); err != nil {
		return model.ConfigChangeValidation{Error: err.Error()}
	}
	report, err := staterepo.ValidateCandidateConfig(ctx, m.db, candidate)
	v := model.ConfigChangeValidation{OK: err == nil, Warnings: report.Check.Warnings, Liveness: report.Liveness}
	if err != nil {
		v.Error = err.Error()
	}
	return v
}

// MergeConfigChange implements ui.ConfigChangeManager: pending.HeadSHA,
// and nothing else, merged into the base branch of the checkout the sync
// applies from, after the branch is shown still to be there and the
// merge onto today's base branch still to validate. The proposal branch
// goes with it.
func (m *StateManager) MergeConfigChange(ctx context.Context, pending model.PendingConfigChange) (string, error) {
	if m == nil {
		return "", errors.New("config repository is not available")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	dir, base, err := m.configChangeRepo()
	if err != nil {
		return "", err
	}
	if pending.Base != "" && pending.Base != base {
		return "", fmt.Errorf("the change was proposed against %s, and grain/config now merges into %s", pending.Base, base)
	}
	head, err := localResolve(dir, pending.Branch)
	if err != nil || head != pending.HeadSHA {
		return "", ui.ErrConfigChangeMoved
	}
	baseSHA, err := localResolve(dir, base)
	if err != nil {
		return "", err
	}
	if v := m.validateConfigMerge(ctx, dir, baseSHA, head); !v.OK {
		return "", &ui.ConfigPullRequestInvalid{Err: errors.New(v.Error),
			Validation: staterepo.CandidateReport{Check: staterepo.CheckReport{Warnings: v.Warnings}, Liveness: v.Liveness}}
	}
	current, err := localGit(dir, "branch", "--show-current")
	if err != nil {
		return "", err
	}
	if current != base {
		return "", fmt.Errorf("config checkout is on %q, not %q", current, base)
	}
	if status, err := localGit(dir, "status", "--porcelain"); err != nil {
		return "", err
	} else if status != "" {
		return "", errors.New("config checkout has uncommitted changes")
	}
	message := fmt.Sprintf("%s\n\nApproved in the session that proposed it (%s).", pending.Summary, pending.Branch)
	if _, err := localGit(dir, "-c", "user.name=grain", "-c", "user.email=grain@localhost",
		"merge", "--no-ff", "-m", message, "--end-of-options", head); err != nil {
		_, _ = localGit(dir, "merge", "--abort")
		return "", err
	}
	merged, err := localGit(dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if ref, err := localRef(pending.Branch); err == nil {
		_, _ = localGit(dir, "update-ref", "-d", "--end-of-options", ref, head)
	}
	return merged, nil
}

// DiscardConfigChange implements ui.ConfigChangeManager: the proposal
// branch is deleted, wherever its tip now is.
func (m *StateManager) DiscardConfigChange(_ context.Context, pending model.PendingConfigChange) error {
	if m == nil {
		return errors.New("config repository is not available")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	dir, _, err := m.configChangeRepo()
	if err != nil {
		return err
	}
	exists, err := localRefExists(dir, pending.Branch)
	if err != nil || !exists {
		return err
	}
	ref, err := localRef(pending.Branch)
	if err != nil {
		return err
	}
	_, err = localGit(dir, "update-ref", "-d", "--end-of-options", ref)
	return err
}

func shortCommitSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
