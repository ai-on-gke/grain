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
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// OpenConfigPullRequest is POST /api/state-repo/pull-requests for the
// controller-local grain/config (ui.ConfigPullRequestOpener): the change
// is applied to the base branch's tree, run through the pre-review suite
// (staterepo.ValidateCandidateConfig), committed to a branch of its own
// -- without touching the working tree the sync exports into -- and
// carried by a task targeting grain/config, finished and not submitted,
// which is what an internal pull request is (StateRepoPullRequestClient).
// It waits there for a person's Submit.
func (m *StateManager) OpenConfigPullRequest(ctx context.Context, req ui.ConfigPullRequestRequest) (ui.ConfigPullRequest, error) {
	if m == nil {
		return ui.ConfigPullRequest{}, errors.New("config repository is not available")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.repo == nil {
		return ui.ConfigPullRequest{}, errors.New("config repository is not available")
	}
	if remote, _ := m.externalConfigRepo(false); remote != "" {
		return ui.ConfigPullRequest{}, fmt.Errorf("configuration is managed in the external repository %s: open the pull request there", remote)
	}
	dir := m.repo.Dir()
	base := m.repo.Branch()
	if base == "" {
		base = "main"
	}
	baseSHA, err := localResolve(dir, base)
	if err != nil {
		return ui.ConfigPullRequest{}, fmt.Errorf("resolving the config repository's %s: %w", base, err)
	}

	candidate, err := os.MkdirTemp("", "grain-config-pr-")
	if err != nil {
		return ui.ConfigPullRequest{}, err
	}
	defer os.RemoveAll(candidate)
	if err := extractCommit(ctx, dir, baseSHA, candidate); err != nil {
		return ui.ConfigPullRequest{}, err
	}
	if err := staterepo.ApplyChanges(candidate, req.Changes); err != nil {
		return ui.ConfigPullRequest{}, &ui.ConfigPullRequestInvalid{Err: fmt.Errorf("the change does not apply to %s: %w", base, err)}
	}
	report, err := staterepo.ValidateCandidateConfig(ctx, m.db, candidate)
	if err != nil {
		return ui.ConfigPullRequest{}, &ui.ConfigPullRequestInvalid{Err: err, Validation: report}
	}

	store := model.New(m.db)
	id, err := store.NewTaskID(ctx)
	if err != nil {
		return ui.ConfigPullRequest{}, err
	}
	branch := model.BranchName(id)
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = configChangeTitle(req.Changes)
	}
	commit, err := commitChanges(ctx, dir, baseSHA, candidate, req.Changes, title, req.User)
	if err != nil {
		return ui.ConfigPullRequest{}, err
	}
	if _, err := localGit(dir, "update-ref", "--end-of-options", "refs/heads/"+branch, commit, ""); err != nil {
		return ui.ConfigPullRequest{}, fmt.Errorf("creating config branch %s: %w", branch, err)
	}

	target := model.ConfigRepo()
	actor := model.Principal{Kind: model.PrincipalHuman, ID: req.User}
	now := time.Now().UTC()
	ref := fmt.Sprintf("%s#%s", target.String(), id)
	task := model.Task{
		ID:        id,
		Intent:    model.IntentImplement,
		Origin:    model.Origin{Attribution: model.Attribution{Actor: actor}},
		Title:     title,
		Body:      req.Body,
		Approval:  &model.Attribution{Actor: actor},
		Target:    &target,
		Base:      base,
		Links:     []model.Link{{Kind: model.LinkFixes, Target: ref}},
		Creator:   req.User,
		CreatedAt: &now,
	}
	if err := store.PutTask(ctx, task); err != nil {
		_, _ = localGit(dir, "update-ref", "-d", "--end-of-options", "refs/heads/"+branch, commit)
		return ui.ConfigPullRequest{}, err
	}
	if err := store.Observe(ctx, model.Observation{TaskID: id, CompletedAt: &now, PrOpenedAt: &now}); err != nil {
		return ui.ConfigPullRequest{}, err
	}
	number, _ := internalPullRequestNumber(&task)
	return ui.ConfigPullRequest{
		TaskID:     id,
		Number:     number,
		Branch:     branch,
		Base:       base,
		Commit:     commit,
		URL:        fmt.Sprintf("/repos/grain/config?base=%s&head=%s", base, branch),
		Validation: report,
	}, nil
}

// configChangeTitle names a change by the files it edits.
func configChangeTitle(changes []staterepo.FileChange) string {
	paths := make([]string, 0, len(changes))
	for _, c := range changes {
		paths = append(paths, c.Path)
	}
	return "Change " + strings.Join(paths, ", ")
}

// extractCommit writes commit's tree into dir (git archive), leaving the
// repository's own working tree and index alone.
func extractCommit(ctx context.Context, repoDir, commit, dir string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "archive", "--format=tar", "--end-of-options", commit)
	// A local read of objects needs git on PATH and nothing else of the
	// daemon's environment.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_TERMINAL_PROMPT=0"}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git archive %s: %w: %s", commit, err, strings.TrimSpace(stderr.String()))
	}
	tr := tar.NewReader(bytes.NewReader(out))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(filepath.FromSlash(h.Name))
		if name == "." || strings.HasPrefix(name, ".."+string(filepath.Separator)) || filepath.IsAbs(name) {
			continue
		}
		dst := filepath.Join(dir, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			data, err := io.ReadAll(tr)
			if err != nil {
				return err
			}
			if err := os.WriteFile(dst, data, 0o644); err != nil {
				return err
			}
		}
	}
}

// commitChanges commits the changed files of candidate on top of base
// through a private index, so neither the working tree nor the index
// the sync uses is touched, and is the new commit.
func commitChanges(ctx context.Context, repoDir, base, candidate string, changes []staterepo.FileChange, title, user string) (string, error) {
	index, err := os.CreateTemp("", "grain-config-pr-index-")
	if err != nil {
		return "", err
	}
	index.Close()
	os.Remove(index.Name())
	defer os.Remove(index.Name())
	name := user
	if name == "" {
		name = "grain"
	}
	// Plumbing on a local repository needs git on PATH and nothing else
	// of the daemon's environment.
	env := []string{"PATH=" + os.Getenv("PATH"),
		"GIT_INDEX_FILE=" + index.Name(),
		"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=grain@localhost",
		"GIT_COMMITTER_NAME=grain", "GIT_COMMITTER_EMAIL=grain@localhost",
		"GIT_TERMINAL_PROMPT=0"}
	git := func(stdin []byte, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repoDir}, args...)...)
		cmd.Env = env
		if stdin != nil {
			cmd.Stdin = bytes.NewReader(stdin)
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out)), nil
	}
	if _, err := git(nil, "read-tree", base); err != nil {
		return "", err
	}
	for _, c := range changes {
		if c.Op == staterepo.ChangeDelete {
			if _, err := git(nil, "update-index", "--force-remove", "--", c.Path); err != nil {
				return "", err
			}
			continue
		}
		data, err := os.ReadFile(filepath.Join(candidate, filepath.FromSlash(c.Path)))
		if err != nil {
			return "", err
		}
		blob, err := git(data, "hash-object", "-w", "--stdin")
		if err != nil {
			return "", err
		}
		if _, err := git(nil, "update-index", "--add", "--cacheinfo", "100644,"+blob+","+c.Path); err != nil {
			return "", err
		}
	}
	tree, err := git(nil, "write-tree")
	if err != nil {
		return "", err
	}
	return git([]byte(title+"\n"), "commit-tree", tree, "-p", base)
}
