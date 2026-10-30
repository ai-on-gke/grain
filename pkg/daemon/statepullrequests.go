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
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/trustboundary"
)

type StateRepoPullRequestClient struct {
	Client  github.Client
	Manager *StateManager
	Store   *model.Store
}

var (
	_ github.Client             = (*StateRepoPullRequestClient)(nil)
	_ github.MergeQueuer        = (*StateRepoPullRequestClient)(nil)
	_ github.MergeRulesReader   = (*StateRepoPullRequestClient)(nil)
	_ github.DiffReader         = (*StateRepoPullRequestClient)(nil)
	_ github.FileReader         = (*StateRepoPullRequestClient)(nil)
	_ github.ReviewSubmitter    = (*StateRepoPullRequestClient)(nil)
	_ github.CollaboratorReader = (*StateRepoPullRequestClient)(nil)
	_ github.ReactionPoster     = (*StateRepoPullRequestClient)(nil)
	_ github.CheckRunWriter     = (*StateRepoPullRequestClient)(nil)
	_ github.CallerLabeler      = (*StateRepoPullRequestClient)(nil)
	_ github.JobLogTailer       = (*StateRepoPullRequestClient)(nil)
)

// WithCaller is this client with its forge requests charged to caller
// (github.WithCaller), still overlaying the native config repository.
func (c *StateRepoPullRequestClient) WithCaller(caller string) github.Client {
	out := *c
	out.Client = github.WithCaller(c.Client, caller)
	return &out
}

var errExternalConfigRepoNoNativePR = &github.Error{
	Status: 404,
	Body:   []byte("local grain/config repository is disabled when an external configuration repository is configured"),
}

var errInternalConfigRepoNoIssues = &github.Error{
	Status: 404,
	Body:   []byte("local grain/config repository has no GitHub issues"),
}

func (c *StateRepoPullRequestClient) isLocal(owner, name string) bool {
	if c == nil || c.Manager == nil || !c.Manager.isInternalConfigRepo() {
		return false
	}
	return model.IsConfigRepo(model.RepoRef{Owner: owner, Name: name})
}

func (c *StateRepoPullRequestClient) isDisabledConfigRepo(owner, name string) bool {
	if c == nil || c.Manager == nil || c.Manager.isInternalConfigRepo() {
		return false
	}
	return model.IsConfigRepo(model.RepoRef{Owner: owner, Name: name})
}

// withRepo runs fn against the deployment's own state repository, the
// checkout grain/config is answered from.
func (c *StateRepoPullRequestClient) withRepo(owner, name string, fn func(dir, base string) error) error {
	c.Manager.mu.Lock()
	defer c.Manager.mu.Unlock()
	if c.Manager.repo == nil {
		return errors.New("config repository is not available")
	}
	return fn(c.Manager.repo.Dir(), c.Manager.repo.Branch())
}

func localGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func localRef(branch string) (string, error) {
	if strings.TrimSpace(branch) == "" || strings.HasPrefix(branch, "-") {
		return "", fmt.Errorf("invalid branch name %q", branch)
	}
	out, err := exec.Command("git", "check-ref-format", "--branch", branch).Output()
	if err != nil || strings.TrimSpace(string(out)) != branch {
		return "", fmt.Errorf("invalid branch name %q", branch)
	}
	return "refs/heads/" + branch, nil
}

func localRefExists(dir, branch string) (bool, error) {
	ref, err := localRef(branch)
	if err != nil {
		return false, err
	}
	cmd := exec.Command("git", "-C", dir, "show-ref", "--verify", "--quiet", "--end-of-options", ref)
	err = cmd.Run()
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("checking branch %q: %w", branch, err)
}

func localResolve(dir, branch string) (string, error) {
	ref, err := localRef(branch)
	if err != nil {
		return "", err
	}
	return localGit(dir, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
}

func (c *StateRepoPullRequestClient) taskForBranch(branch string) (*model.Task, error) {
	tasks, err := c.Store.ListTasks(context.Background())
	if err != nil {
		return nil, err
	}
	for i := range tasks {
		// A review working on the reviewed change's own branch shares
		// that branch name; the pull request off it is the change's.
		if tasks[i].Target != nil && model.IsConfigRepo(*tasks[i].Target) && tasks[i].BranchName() == branch &&
			!pushesReviewedBranch(tasks[i]) {
			return &tasks[i], nil
		}
	}
	return nil, nil
}

// pushesReviewedBranch reports whether task is a review that works on the
// branch of the change it reviews (model.LinkPushesReviewedBranch).
func pushesReviewedBranch(task model.Task) bool {
	for _, l := range task.Links {
		if l.Kind == model.LinkPushesReviewedBranch {
			return true
		}
	}
	return false
}

func (c *StateRepoPullRequestClient) taskForNumber(number int) (*model.Task, error) {
	task, err := c.Store.GetTask(context.Background(), strconv.Itoa(number))
	if err != nil || task == nil {
		return task, err
	}
	if task.Target == nil || !model.IsConfigRepo(*task.Target) {
		return nil, fmt.Errorf("internal pull request %d does not target a config repository", number)
	}
	return task, nil
}

func internalPullRequestNumber(task *model.Task) (int, error) {
	number, err := strconv.Atoi(task.ID)
	if err != nil || number < 1 {
		return 0, fmt.Errorf("task %q cannot identify an internal pull request", task.ID)
	}
	return number, nil
}

func (c *StateRepoPullRequestClient) BranchExists(owner, name, branch string) (bool, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return false, errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.BranchExists(owner, name, branch)
	}
	var exists bool
	err := c.withRepo(owner, name, func(dir, _ string) error {
		var err error
		exists, err = localRefExists(dir, branch)
		return err
	})
	return exists, err
}

func (c *StateRepoPullRequestClient) GetBranchHead(owner, name, branch string) (*github.BranchHead, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return nil, errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.GetBranchHead(owner, name, branch)
	}
	var head *github.BranchHead
	err := c.withRepo(owner, name, func(dir, _ string) error {
		exists, err := localRefExists(dir, branch)
		if err != nil || !exists {
			return err
		}
		ref, _ := localRef(branch)
		out, err := localGit(dir, "show", "-s", "--format=%H%x00%B", "--end-of-options", ref)
		if err != nil {
			return err
		}
		sha, message, _ := strings.Cut(out, "\x00")
		head = &github.BranchHead{SHA: strings.TrimSpace(sha), Message: strings.TrimSpace(message)}
		return nil
	})
	return head, err
}

func (c *StateRepoPullRequestClient) DefaultBranch(owner, name string) (string, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return "", errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.DefaultBranch(owner, name)
	}
	var branch string
	err := c.withRepo(owner, name, func(_, base string) error {
		branch = base
		return nil
	})
	return branch, err
}

func (c *StateRepoPullRequestClient) CompareCommits(owner, name, base, head string) ([]github.Commit, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return nil, errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.CompareCommits(owner, name, base, head)
	}
	var commits []github.Commit
	err := c.withRepo(owner, name, func(dir, _ string) error {
		baseSHA, err := localResolve(dir, base)
		if err != nil {
			return err
		}
		headSHA, err := localResolve(dir, head)
		if err != nil {
			return err
		}
		commits, err = localCommits(dir, baseSHA, headSHA)
		return err
	})
	return commits, err
}

// localCommits is the commits head has over base, oldest first.
func localCommits(dir, baseSHA, headSHA string) ([]github.Commit, error) {
	out, err := localGit(dir, "log", "--reverse", "--format=%H%x00%P%x00%B%x00%x1e", "--end-of-options", baseSHA+".."+headSHA)
	if err != nil {
		return nil, err
	}
	var commits []github.Commit
	for _, record := range strings.Split(out, "\x1e") {
		record = strings.TrimSpace(record)
		if record == "" {
			continue
		}
		fields := strings.SplitN(record, "\x00", 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("reading commit metadata for %s..%s", baseSHA, headSHA)
		}
		commits = append(commits, github.Commit{
			SHA: strings.TrimSpace(fields[0]), Message: strings.TrimSpace(fields[2]),
			Merge: len(strings.Fields(fields[1])) > 1,
		})
	}
	return commits, nil
}

// CompareRefs is GitHub's compare for a config repository, answered from
// its checkout: the status of head against base, their merge base, the
// commits head has over base, and the files it changes from the merge
// base. Without it the embedded forge client was asked, so the finish
// path's branchIsBehind sent every config-repository task's branch to
// GET /repos/grain/config/compare/... on the forge -- with the
// deployment's credential, about a repository no forge has -- and read
// the 404 as "not behind" (docs/test-runs/2026-10-15-team-config-ci-check.md).
//
// base and head are branch names or commit SHAs, as the callers pass
// both; one that does not resolve is GitHub's 404, which they already
// read as the comparison having nothing to say.
func (c *StateRepoPullRequestClient) CompareRefs(owner, name, base, head string) (github.Comparison, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return github.Comparison{}, errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.CompareRefs(owner, name, base, head)
	}
	var cmp github.Comparison
	err := c.withRepo(owner, name, func(dir, _ string) error {
		baseSHA, err := localResolveRef(dir, base)
		if err != nil {
			return err
		}
		headSHA, err := localResolveRef(dir, head)
		if err != nil {
			return err
		}
		counts, err := localGit(dir, "rev-list", "--left-right", "--count", "--end-of-options", baseSHA+"..."+headSHA)
		if err != nil {
			return err
		}
		var behind, ahead int
		if _, err := fmt.Sscan(counts, &behind, &ahead); err != nil {
			return fmt.Errorf("reading how %s and %s diverge: %q", base, head, counts)
		}
		switch {
		case ahead == 0 && behind == 0:
			cmp.Status = "identical"
		case behind == 0:
			cmp.Status = "ahead"
		case ahead == 0:
			cmp.Status = "behind"
		default:
			cmp.Status = "diverged"
		}
		if cmp.MergeBase, err = localGit(dir, "merge-base", "--end-of-options", baseSHA, headSHA); err != nil {
			return err
		}
		if cmp.Commits, err = localCommits(dir, baseSHA, headSHA); err != nil {
			return err
		}
		out, err := localGit(dir, "diff", "--raw", "-z", "--no-abbrev", "-M", "--end-of-options", cmp.MergeBase, headSHA)
		if err != nil {
			return err
		}
		cmp.Files = localComparedFiles(out)
		return nil
	})
	return cmp, err
}

// localResolveRef is localResolve for a branch name or a full commit SHA.
func localResolveRef(dir, ref string) (string, error) {
	if len(ref) == 40 && strings.Trim(ref, "0123456789abcdef") == "" {
		sha, err := localGit(dir, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
		if err != nil {
			return "", &github.Error{Status: 404, Body: []byte("no commit " + ref)}
		}
		return sha, nil
	}
	exists, err := localRefExists(dir, ref)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", &github.Error{Status: 404, Body: []byte("no branch " + ref)}
	}
	return localResolve(dir, ref)
}

// localComparedFiles reads `git diff --raw -z` into GitHub's words for
// each change.
func localComparedFiles(raw string) []github.ComparedFile {
	var files []github.ComparedFile
	fields := strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00")
	for i := 0; i+1 < len(fields); {
		meta := strings.Fields(strings.TrimPrefix(fields[i], ":"))
		if len(meta) < 5 {
			break
		}
		f := github.ComparedFile{SHA: meta[3], Filename: fields[i+1]}
		i += 2
		switch meta[4][0] {
		case 'A':
			f.Status = "added"
		case 'D':
			f.Status = "removed"
		case 'R':
			f.Status = "renamed"
			if i < len(fields) {
				f.PreviousFilename, f.Filename = f.Filename, fields[i]
				i++
			}
		default:
			f.Status = "modified"
		}
		files = append(files, f)
	}
	return files
}

func (c *StateRepoPullRequestClient) CreateBranch(owner, name, branch, sha string) error {
	if c.isDisabledConfigRepo(owner, name) {
		return errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.CreateBranch(owner, name, branch, sha)
	}
	return c.withRepo(owner, name, func(dir, _ string) error {
		ref, err := localRef(branch)
		if err != nil {
			return err
		}
		_, err = localGit(dir, "update-ref", "--end-of-options", ref, sha, strings.Repeat("0", 40))
		return err
	})
}

func (c *StateRepoPullRequestClient) UpdateBranch(owner, name, branch, sha string, force bool) error {
	if c.isDisabledConfigRepo(owner, name) {
		return errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.UpdateBranch(owner, name, branch, sha, force)
	}
	return c.withRepo(owner, name, func(dir, _ string) error {
		ref, err := localRef(branch)
		if err != nil {
			return err
		}
		if !force {
			current, err := localResolve(dir, branch)
			if err != nil {
				return err
			}
			_, err = localGit(dir, "merge-base", "--is-ancestor", "--end-of-options", current, sha)
			if err != nil {
				return fmt.Errorf("updating %s would not be a fast-forward: %w", branch, err)
			}
		}
		_, err = localGit(dir, "update-ref", "--end-of-options", ref, sha)
		return err
	})
}

func (c *StateRepoPullRequestClient) SetDefaultBranch(owner, name, branch string) error {
	if c.isDisabledConfigRepo(owner, name) {
		return errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.SetDefaultBranch(owner, name, branch)
	}
	return c.withRepo(owner, name, func(dir, defaultBase string) error {
		ref, err := localRef(branch)
		if err != nil {
			return err
		}
		if branch == defaultBase {
			return nil
		}
		_, err = localGit(dir, "symbolic-ref", "--end-of-options", "HEAD", ref)
		return err
	})
}

func (c *StateRepoPullRequestClient) DeleteBranch(owner, name, branch string) error {
	if c.isDisabledConfigRepo(owner, name) {
		return errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.DeleteBranch(owner, name, branch)
	}
	return c.withRepo(owner, name, func(dir, defaultBase string) error {
		ref, err := localRef(branch)
		if err != nil {
			return err
		}
		if branch == defaultBase {
			return fmt.Errorf("refusing to delete config repository default branch %q", branch)
		}
		exists, err := localRefExists(dir, branch)
		if err != nil {
			return err
		}
		if !exists {
			return &github.Error{Status: 404, Body: []byte("no branch " + branch)}
		}
		_, err = localGit(dir, "update-ref", "-d", "--end-of-options", ref)
		return err
	})
}

// FindOpenPullRequestForHead is forkhead.Finder's lookup: a fork head
// label resolved on GitHub. The config repository has no forks, so no
// label heads anything there.
func (c *StateRepoPullRequestClient) FindOpenPullRequestForHead(owner, name, head string) (*github.PullRequest, error) {
	if c.isDisabledConfigRepo(owner, name) || c.isLocal(owner, name) {
		return nil, nil
	}
	finder, ok := c.Client.(interface {
		FindOpenPullRequestForHead(owner, repo, head string) (*github.PullRequest, error)
	})
	if !ok {
		return nil, fmt.Errorf("this deployment's GitHub client cannot look up a pull request by its head label")
	}
	return finder.FindOpenPullRequestForHead(owner, name, head)
}

func (c *StateRepoPullRequestClient) FindOpenPullRequestForBranch(owner, name, branch string) (*github.PullRequest, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return nil, errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.FindOpenPullRequestForBranch(owner, name, branch)
	}
	task, err := c.taskForBranch(branch)
	if err != nil || task == nil {
		return nil, err
	}
	number, err := internalPullRequestNumber(task)
	if err != nil {
		return nil, err
	}
	detail, err := c.GetPullRequest(owner, name, number)
	if err != nil || detail.State != "open" {
		return nil, err
	}
	return &github.PullRequest{Number: number, HTMLURL: detail.HTMLURL}, nil
}

func (c *StateRepoPullRequestClient) CreatePullRequest(owner, name, head, base, title, body string) (github.PullRequest, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return github.PullRequest{}, errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.CreatePullRequest(owner, name, head, base, title, body)
	}
	task, err := c.taskForBranch(head)
	if err != nil {
		return github.PullRequest{}, err
	}
	if task == nil {
		return github.PullRequest{}, fmt.Errorf("no task owns config branch %q", head)
	}
	number, err := internalPullRequestNumber(task)
	if err != nil {
		return github.PullRequest{}, err
	}
	url := fmt.Sprintf("/repos/grain/config?base=%s&head=%s", base, head)
	return github.PullRequest{Number: number, HTMLURL: url}, nil
}

func (c *StateRepoPullRequestClient) UpdatePullRequestBody(owner, name string, number int, body string) error {
	if c.isDisabledConfigRepo(owner, name) {
		return errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.UpdatePullRequestBody(owner, name, number, body)
	}
	return nil
}

func (c *StateRepoPullRequestClient) UpdatePullRequestTitle(owner, name string, number int, title string) error {
	if c.isDisabledConfigRepo(owner, name) {
		return errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.UpdatePullRequestTitle(owner, name, number, title)
	}
	return nil
}

func (c *StateRepoPullRequestClient) GetPullRequest(owner, name string, number int) (github.PullRequestDetail, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return github.PullRequestDetail{}, errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.GetPullRequest(owner, name, number)
	}
	task, err := c.taskForNumber(number)
	if err != nil {
		return github.PullRequestDetail{}, err
	}
	if task == nil {
		return github.PullRequestDetail{}, fmt.Errorf("internal pull request %d does not exist", number)
	}
	branch := task.BranchName()
	base := task.Base
	var detail github.PullRequestDetail
	err = c.withRepo(owner, name, func(dir, defaultBase string) error {
		if base == "" {
			base = defaultBase
		}
		detail = github.PullRequestDetail{
			Number: number, Title: task.Title, Body: task.Body,
			HTMLURL: fmt.Sprintf("/repos/grain/config?base=%s&head=%s", base, branch),
			HeadRef: branch, BaseRef: base, State: "open",
		}
		if task.CreatedAt != nil {
			detail.CreatedAt = *task.CreatedAt
		}
		headExists, err := localRefExists(dir, branch)
		if err != nil {
			return err
		}
		if !headExists {
			detail.State = "closed"
			return nil
		}
		headSHA, err := localResolve(dir, branch)
		if err != nil {
			return err
		}
		baseSHA, err := localResolve(dir, base)
		if err != nil {
			return err
		}
		detail.HeadSHA = headSHA
		ancestor := exec.Command("git", "-C", dir, "merge-base", "--is-ancestor", "--end-of-options", headSHA, baseSHA).Run()
		if ancestor == nil {
			detail.State, detail.Merged = "closed", true
			return nil
		}
		var exit *exec.ExitError
		if !errors.As(ancestor, &exit) || exit.ExitCode() != 1 {
			return fmt.Errorf("checking whether internal pull request %d is merged: %w", number, ancestor)
		}
		cmd := exec.Command("git", "-C", dir, "merge-tree", "--write-tree", "--end-of-options", baseSHA, headSHA)
		if out, mergeErr := cmd.CombinedOutput(); mergeErr == nil {
			clean := true
			detail.Mergeable = &clean
			detail.MergeableState = "clean"
		} else if errors.As(mergeErr, &exit) && exit.ExitCode() == 1 {
			clean := false
			detail.Mergeable = &clean
			detail.MergeableState = "dirty"
		} else {
			return fmt.Errorf("checking mergeability of internal pull request %d: %w: %s", number, mergeErr, strings.TrimSpace(string(out)))
		}
		return nil
	})
	return detail, err
}

func (c *StateRepoPullRequestClient) MergeBranch(owner, name, base, head, commitMessage string) (github.MergeResult, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return github.MergeResult{}, errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.MergeBranch(owner, name, base, head, commitMessage)
	}
	var result github.MergeResult
	err := c.withRepo(owner, name, func(dir, _ string) error {
		baseSHA, err := localResolve(dir, base)
		if err != nil {
			return err
		}
		headSHA, err := localResolve(dir, head)
		if err != nil {
			return err
		}
		if exec.Command("git", "-C", dir, "merge-base", "--is-ancestor", "--end-of-options", headSHA, baseSHA).Run() == nil {
			return nil
		}
		out, err := localGit(dir, "merge-tree", "--write-tree", "--end-of-options", baseSHA, headSHA)
		if err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() == 1 {
				return &github.Error{Status: 409, Body: []byte("internal branches conflict")}
			}
			return err
		}
		fields := strings.Fields(out)
		if len(fields) == 0 {
			return errors.New("git merge-tree returned no tree")
		}
		tree := fields[0]
		message := strings.TrimSpace(commitMessage)
		if message == "" {
			message = fmt.Sprintf("Merge %s into %s", head, base)
		}
		commit, err := localGit(dir, "commit-tree", "-p"+baseSHA, "-p"+headSHA, "-m", message, "--end-of-options", tree)
		if err != nil {
			return err
		}
		baseRef, err := localRef(base)
		if err != nil {
			return err
		}
		if _, err := localGit(dir, "update-ref", "--end-of-options", baseRef, commit, baseSHA); err != nil {
			return err
		}
		result = github.MergeResult{Merged: true, SHA: commit}
		return nil
	})
	return result, err
}

func (c *StateRepoPullRequestClient) MergePullRequest(owner, name string, number int, headSHA string) error {
	if c.isDisabledConfigRepo(owner, name) {
		return errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.MergePullRequest(owner, name, number, headSHA)
	}
	task, err := c.taskForNumber(number)
	if err != nil {
		return err
	}
	if task == nil {
		return fmt.Errorf("internal pull request %d does not exist", number)
	}
	branch := task.BranchName()
	err = c.withRepo(owner, name, func(dir, defaultBase string) error {
		base := task.Base
		if base == "" {
			base = defaultBase
		}
		currentHead, err := localResolve(dir, branch)
		if err != nil {
			return err
		}
		if headSHA != "" && currentHead != headSHA {
			return fmt.Errorf("internal pull request %d moved from %s to %s", number, headSHA, currentHead)
		}
		currentBranch, err := localGit(dir, "branch", "--show-current")
		if err != nil {
			return err
		}
		if currentBranch != base {
			return fmt.Errorf("config checkout is on %q, not pull request base %q", currentBranch, base)
		}
		status, err := localGit(dir, "status", "--porcelain")
		if err != nil {
			return err
		}
		if status != "" {
			return fmt.Errorf("config checkout has uncommitted changes")
		}
		_, err = localGit(dir, "merge", "--no-ff", "--no-edit", "--end-of-options", branch)
		if err != nil {
			// A branch can move after mergeability was observed. Do not leave
			// the controller checkout in a conflicted index when that race loses.
			_, _ = localGit(dir, "merge", "--abort")
		}
		return err
	})
	if err == nil {
		c.Manager.Nudge()
	}
	return err
}

func (c *StateRepoPullRequestClient) ClosePullRequest(owner, name string, number int) error {
	if c.isDisabledConfigRepo(owner, name) {
		return errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.ClosePullRequest(owner, name, number)
	}
	task, err := c.taskForNumber(number)
	if err != nil {
		return err
	}
	if task == nil {
		return nil
	}
	return c.withRepo(owner, name, func(dir, _ string) error {
		ref, err := localRef(task.BranchName())
		if err != nil {
			return err
		}
		_, err = localGit(dir, "update-ref", "-d", "--end-of-options", ref)
		return err
	})
}

// EmptyChecksAreSettled tells forge that an empty check list for the native
// config repository is the complete answer. Unlike GitHub, this controller has
// no asynchronous workflow publisher that might register a check after the
// branch appears, so applying the ordinary registration grace period would
// make every submitted config change report waiting_ci for two minutes.
func (c *StateRepoPullRequestClient) EmptyChecksAreSettled(owner, name string) bool {
	return c.isLocal(owner, name)
}

func (c *StateRepoPullRequestClient) ListCheckRuns(owner, name, ref string) ([]github.CheckRun, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return nil, errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.ListCheckRuns(owner, name, ref)
	}
	return nil, nil
}

func (c *StateRepoPullRequestClient) ListWorkflowRuns(owner, name, headSHA string) ([]github.CheckRun, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return nil, errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.ListWorkflowRuns(owner, name, headSHA)
	}
	return nil, nil
}

func (c *StateRepoPullRequestClient) ListPullRequestFiles(owner, name string, number int) ([]string, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return nil, errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.ListPullRequestFiles(owner, name, number)
	}
	task, err := c.taskForNumber(number)
	if err != nil || task == nil {
		return nil, err
	}
	var files []string
	err = c.withRepo(owner, name, func(dir, defaultBase string) error {
		base := task.Base
		if base == "" {
			base = defaultBase
		}
		out, err := localGit(dir, "diff", "--name-only", "--end-of-options", base+"..."+task.BranchName(), "--")
		if err != nil {
			return err
		}
		if out != "" {
			files = strings.Split(out, "\n")
		}
		return nil
	})
	return files, err
}

// CompareDiff and PullRequestDiff pass github.DiffReader through for
// MergeQueuer's reason: the security hold finds it by type assertion
// (orchestrator.carryClearance), and without them every head a trust-
// boundary change moved to -- a merge of its base, a CI repair -- was
// reviewed again from scratch. The controller's own configuration
// repository answers from its local checkout, the way
// ListPullRequestFiles does.
func (c *StateRepoPullRequestClient) CompareDiff(owner, name, base, head string) (string, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return "", errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		differ, ok := c.Client.(github.DiffReader)
		if !ok {
			return "", fmt.Errorf("%s/%s has no forge client that serves raw diffs", owner, name)
		}
		return differ.CompareDiff(owner, name, base, head)
	}
	var diff string
	err := c.withRepo(owner, name, func(dir, _ string) error {
		out, err := localDiff(dir, base, head)
		diff = out
		return err
	})
	return diff, err
}

func (c *StateRepoPullRequestClient) PullRequestDiff(owner, name string, number int) (string, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return "", errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		differ, ok := c.Client.(github.DiffReader)
		if !ok {
			return "", fmt.Errorf("%s/%s has no forge client that serves raw diffs", owner, name)
		}
		return differ.PullRequestDiff(owner, name, number)
	}
	task, err := c.taskForNumber(number)
	if err != nil {
		return "", err
	}
	if task == nil {
		return "", &github.Error{Status: 404, Body: []byte(fmt.Sprintf("no pull request #%d", number))}
	}
	var diff string
	err = c.withRepo(owner, name, func(dir, defaultBase string) error {
		base := task.Base
		if base == "" {
			base = defaultBase
		}
		out, err := localDiff(dir, base, task.BranchName())
		diff = out
		return err
	})
	return diff, err
}

// localDiff is base...head's patch in the local checkout, in the same
// form GitHub's raw diff takes. Untrimmed, unlike localGit: a patch's
// trailing newline is part of it.
func localDiff(dir, base, head string) (string, error) {
	for _, rev := range []string{base, head} {
		if strings.TrimSpace(rev) == "" || strings.HasPrefix(rev, "-") {
			return "", fmt.Errorf("invalid revision %q", rev)
		}
	}
	cmd := exec.Command("git", "-C", dir, "diff", "--no-color", "--no-ext-diff", "--full-index",
		"--end-of-options", base+"..."+head, "--")
	// A local read of a repository grain itself owns needs nothing of the
	// daemon's environment but where git is and its own configuration.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		detail := ""
		if errors.As(err, &exit) {
			detail = strings.TrimSpace(string(exit.Stderr))
		}
		return "", fmt.Errorf("git diff %s...%s: %w: %s", base, head, err, detail)
	}
	return string(out), nil
}

// ReadFile implements github.FileReader. For the local config repository
// (grain/config), settings/config-policy.yml is always loaded from the base
// branch (main / defaultBase), never from an unmerged pull request head, so a
// pull request cannot relax the auto-merge policy for itself before merging.
func (c *StateRepoPullRequestClient) ReadFile(owner, name, ref, filePath string) ([]byte, bool, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return nil, false, errExternalConfigRepoNoNativePR
	}
	clean := strings.TrimPrefix(strings.TrimPrefix(path.Clean("/"+strings.TrimSpace(filePath)), "/"), "./")
	if clean == "" || clean == "." {
		return nil, false, nil
	}
	if !c.isLocal(owner, name) {
		reader, ok := c.Client.(github.FileReader)
		if !ok {
			return nil, false, fmt.Errorf("%s/%s has no forge client that serves file contents", owner, name)
		}
		if clean == trustboundary.ConfigPolicyPath && c.Store != nil && c.Store.SettingsRepo != nil {
			settings := c.Store.SettingsRepo()
			if strings.EqualFold(settings.Owner, owner) && strings.EqualFold(settings.Name, name) {
				if def, err := c.Client.DefaultBranch(owner, name); err == nil && strings.TrimSpace(def) != "" {
					ref = def
				} else {
					ref = "main"
				}
			}
		}
		return reader.ReadFile(owner, name, ref, clean)
	}
	var (
		data   []byte
		exists bool
	)
	err := c.withRepo(owner, name, func(dir, defaultBase string) error {
		targetRef := strings.TrimSpace(ref)
		if targetRef == "" || clean == trustboundary.ConfigPolicyPath {
			targetRef = defaultBase
		}
		commitSHA, err := localResolveRef(dir, targetRef)
		if err != nil {
			var ghErr *github.Error
			if errors.As(err, &ghErr) && ghErr.Status == 404 {
				return nil
			}
			return err
		}
		blob, ok, err := localReadBlob(dir, commitSHA, clean)
		if err != nil {
			return err
		}
		data, exists = blob, ok
		return nil
	})
	return data, exists, err
}

func localReadBlob(dir, commitSHA, cleanPath string) ([]byte, bool, error) {
	spec := commitSHA + ":" + cleanPath
	kind, err := localGit(dir, "cat-file", "-t", "--end-of-options", spec)
	if err != nil || kind != "blob" {
		return nil, false, nil
	}
	cmd := exec.Command("git", "-C", dir, "cat-file", "blob", "--end-of-options", spec)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		detail := ""
		if errors.As(err, &exit) {
			detail = strings.TrimSpace(string(exit.Stderr))
		}
		return nil, false, fmt.Errorf("git cat-file blob %s: %w: %s", spec, err, detail)
	}
	return out, true, nil
}

func (c *StateRepoPullRequestClient) ListReviewComments(owner, name string, number int) ([]github.ReviewComment, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return nil, errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.ListReviewComments(owner, name, number)
	}
	return nil, nil
}

func (c *StateRepoPullRequestClient) ReplyToReviewComment(owner, name string, number, commentID int, body string) (int, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return 0, errExternalConfigRepoNoNativePR
	}
	if c.isLocal(owner, name) {
		return 0, nil
	}
	replier, ok := c.Client.(interface {
		ReplyToReviewComment(owner, repo string, number, commentID int, body string) (int, error)
	})
	if !ok {
		return 0, fmt.Errorf("%s/%s has no GitHub review comment replier", owner, name)
	}
	return replier.ReplyToReviewComment(owner, name, number, commentID, body)
}

func (c *StateRepoPullRequestClient) ListReviewThreads(owner, name string, number int) ([]github.ReviewThread, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return nil, errExternalConfigRepoNoNativePR
	}
	if c.isLocal(owner, name) {
		return nil, nil
	}
	lister, ok := c.Client.(interface {
		ListReviewThreads(owner, repo string, number int) ([]github.ReviewThread, error)
	})
	if !ok {
		return nil, fmt.Errorf("%s/%s has no GitHub review thread lister", owner, name)
	}
	return lister.ListReviewThreads(owner, name, number)
}

func (c *StateRepoPullRequestClient) ResolveReviewThread(owner, name, threadID string) error {
	if c.isDisabledConfigRepo(owner, name) {
		return errExternalConfigRepoNoNativePR
	}
	if c.isLocal(owner, name) {
		return nil
	}
	resolver, ok := c.Client.(interface {
		ResolveReviewThread(owner, repo, threadID string) error
	})
	if !ok {
		return fmt.Errorf("%s/%s has no GitHub review thread resolver", owner, name)
	}
	return resolver.ResolveReviewThread(owner, name, threadID)
}

func (c *StateRepoPullRequestClient) ListComments(owner, name string, number int) ([]github.Comment, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return nil, errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.ListComments(owner, name, number)
	}
	return nil, nil
}

func (c *StateRepoPullRequestClient) FailedJobLogs(owner, name, headSHA string) ([]github.JobLog, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return nil, errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.FailedJobLogs(owner, name, headSHA)
	}
	return nil, nil
}

// JobLogTail passes GitHub CI's failure-tail reads through to GitHub for
// any repository but the native config repository, which has no Actions.
func (c *StateRepoPullRequestClient) JobLogTail(owner, name string, jobID int64) (string, error) {
	if c.isDisabledConfigRepo(owner, name) || c.isLocal(owner, name) {
		return "", nil
	}
	tailer, ok := c.Client.(github.JobLogTailer)
	if !ok {
		return "", nil
	}
	return tailer.JobLogTail(owner, name, jobID)
}

func (c *StateRepoPullRequestClient) ActionsState(owner, name, headSHA string) (github.ActionsState, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return github.ActionsState{}, errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.ActionsState(owner, name, headSHA)
	}
	return github.ActionsState{}, nil
}

func (c *StateRepoPullRequestClient) RecentRunStarts(owner, name string, limit int) (github.RunStarts, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return github.RunStarts{}, errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.RecentRunStarts(owner, name, limit)
	}
	return github.RunStarts{}, nil
}

func (c *StateRepoPullRequestClient) CreateComment(owner, name string, number int, body string) (int, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return 0, errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.CreateComment(owner, name, number, body)
	}
	return 0, nil
}

func (c *StateRepoPullRequestClient) CreateReview(owner, name string, number int, body string, comments []github.NewReviewComment) (int, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return 0, errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.CreateReview(owner, name, number, body, comments)
	}
	return 0, nil
}

func (c *StateRepoPullRequestClient) MergedPullRequestForCommit(owner, name, sha string) (*github.PullRequest, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return nil, errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.MergedPullRequestForCommit(owner, name, sha)
	}
	// Internal pull requests are already tied to tasks and need no
	// commit-to-forge lookup for reset announcements.
	return nil, nil
}

func (c *StateRepoPullRequestClient) ListPullRequestCommits(owner, name string, number int) ([]github.Commit, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return nil, errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.ListPullRequestCommits(owner, name, number)
	}
	task, err := c.taskForNumber(number)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, fmt.Errorf("internal pull request %d does not exist", number)
	}
	var commits []github.Commit
	err = c.withRepo(owner, name, func(dir, defaultBase string) error {
		base := task.Base
		if base == "" {
			base = defaultBase
		}
		baseSHA, err := localResolve(dir, base)
		if err != nil {
			return err
		}
		headSHA, err := localResolve(dir, task.BranchName())
		if err != nil {
			return err
		}
		commits, err = localCommits(dir, baseSHA, headSHA)
		return err
	})
	return commits, err
}

func (c *StateRepoPullRequestClient) ListIssues(owner, name, label string) ([]github.Issue, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return nil, errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.ListIssues(owner, name, label)
	}
	return nil, nil
}

func (c *StateRepoPullRequestClient) GetIssue(owner, name string, number int) (github.Issue, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return github.Issue{}, errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.GetIssue(owner, name, number)
	}
	return github.Issue{}, errInternalConfigRepoNoIssues
}

func (c *StateRepoPullRequestClient) AddLabel(owner, name string, number int, label string) error {
	if c.isDisabledConfigRepo(owner, name) {
		return errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.AddLabel(owner, name, number, label)
	}
	return nil
}

func (c *StateRepoPullRequestClient) RemoveLabel(owner, name string, number int, label string) error {
	if c.isDisabledConfigRepo(owner, name) {
		return errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.RemoveLabel(owner, name, number, label)
	}
	return nil
}

func (c *StateRepoPullRequestClient) CloseIssue(owner, name string, number int) error {
	if c.isDisabledConfigRepo(owner, name) {
		return errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.CloseIssue(owner, name, number)
	}
	return nil
}

func (c *StateRepoPullRequestClient) ReopenIssue(owner, name string, number int) error {
	if c.isDisabledConfigRepo(owner, name) {
		return errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.ReopenIssue(owner, name, number)
	}
	return nil
}

func (c *StateRepoPullRequestClient) UpdateIssue(owner, name string, number int, title, body *string) error {
	if c.isDisabledConfigRepo(owner, name) {
		return errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.UpdateIssue(owner, name, number, title, body)
	}
	return errInternalConfigRepoNoIssues
}

func (c *StateRepoPullRequestClient) CreateIssue(owner, name, title, body string, labels []string) (github.Issue, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return github.Issue{}, errExternalConfigRepoNoNativePR
	}
	if !c.isLocal(owner, name) {
		return c.Client.CreateIssue(owner, name, title, body, labels)
	}
	return github.Issue{}, errInternalConfigRepoNoIssues
}

// EnqueuePullRequest and MergeQueueEntryOf pass github.MergeQueuer through
// to the forge client for every repository but the controller's own
// configuration repositories, which have no GitHub merge queue to join --
// and which never refuse a merge for want of one, so the merge queue
// never asks. Without them the wrapper would hide the forge's queue from
// the orchestrator, which finds it by type assertion.
func (c *StateRepoPullRequestClient) EnqueuePullRequest(owner, name string, number int, headSHA string) (github.MergeQueueEntry, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return github.MergeQueueEntry{}, errExternalConfigRepoNoNativePR
	}
	queuer, ok := c.Client.(github.MergeQueuer)
	if c.isLocal(owner, name) || !ok {
		return github.MergeQueueEntry{}, fmt.Errorf("%s/%s has no GitHub merge queue grain can add to", owner, name)
	}
	return queuer.EnqueuePullRequest(owner, name, number, headSHA)
}

func (c *StateRepoPullRequestClient) MergeQueueEntryOf(owner, name string, number int) (*github.MergeQueueEntry, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return nil, errExternalConfigRepoNoNativePR
	}
	queuer, ok := c.Client.(github.MergeQueuer)
	if c.isLocal(owner, name) || !ok {
		return nil, fmt.Errorf("%s/%s has no GitHub merge queue grain can read", owner, name)
	}
	return queuer.MergeQueueEntryOf(owner, name, number)
}

// PublishFiles passes github.FilePublisher through for the same reason
// EnqueuePullRequest passes the merge queue through: the orchestrator
// finds it by type assertion. A controller-native configuration
// repository has no forge to serve the files from.
func (c *StateRepoPullRequestClient) PublishFiles(owner, name, ref, message string, at time.Time, files []github.PublishedFile) (string, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return "", errExternalConfigRepoNoNativePR
	}
	publisher, ok := c.Client.(github.FilePublisher)
	if c.isLocal(owner, name) || !ok {
		return "", fmt.Errorf("%s/%s has no forge grain can publish files to", owner, name)
	}
	return publisher.PublishFiles(owner, name, ref, message, at, files)
}

// MergeRequirements passes github.MergeRulesReader through to the forge client
// for repositories other than the controller's own configuration repositories,
// which have no GitHub branch-protection rules. Without this the wrapper would
// hide MergeRulesReader from the orchestrator, which finds it by type assertion.
func (c *StateRepoPullRequestClient) MergeRequirements(owner, name string, number int, base string) (github.MergeRequirements, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return github.MergeRequirements{}, errExternalConfigRepoNoNativePR
	}
	if c.isLocal(owner, name) {
		return github.MergeRequirements{}, nil
	}
	reader, ok := c.Client.(github.MergeRulesReader)
	if !ok {
		return github.MergeRequirements{}, fmt.Errorf("%s/%s has no GitHub merge rules reader", owner, name)
	}
	return reader.MergeRequirements(owner, name, number, base)
}

func (c *StateRepoPullRequestClient) SubmitReview(owner, name string, number int, event github.ReviewEvent, body string, comments []github.NewReviewComment) (int, error) {
	if c.isDisabledConfigRepo(owner, name) {
		return 0, errExternalConfigRepoNoNativePR
	}
	if c.isLocal(owner, name) {
		return c.CreateReview(owner, name, number, body, comments)
	}
	if sub, ok := c.Client.(github.ReviewSubmitter); ok {
		return sub.SubmitReview(owner, name, number, event, body, comments)
	}
	return c.Client.CreateReview(owner, name, number, body, comments)
}

func (c *StateRepoPullRequestClient) CollaboratorPermission(owner, name, username string) (string, error) {
	if c.isDisabledConfigRepo(owner, name) || c.isLocal(owner, name) {
		return "none", nil
	}
	reader, ok := c.Client.(github.CollaboratorReader)
	if !ok {
		return "none", fmt.Errorf("%s/%s has no GitHub collaborator permission reader", owner, name)
	}
	return reader.CollaboratorPermission(owner, name, username)
}

func (c *StateRepoPullRequestClient) CreateIssueCommentReaction(owner, name string, commentID int64, content string) error {
	if c.isDisabledConfigRepo(owner, name) || c.isLocal(owner, name) {
		return nil
	}
	poster, ok := c.Client.(github.ReactionPoster)
	if !ok {
		return fmt.Errorf("%s/%s has no GitHub reaction poster", owner, name)
	}
	return poster.CreateIssueCommentReaction(owner, name, commentID, content)
}

func (c *StateRepoPullRequestClient) CreateReviewCommentReaction(owner, name string, commentID int64, content string) error {
	if c.isDisabledConfigRepo(owner, name) || c.isLocal(owner, name) {
		return nil
	}
	poster, ok := c.Client.(github.ReactionPoster)
	if !ok {
		return fmt.Errorf("%s/%s has no GitHub reaction poster", owner, name)
	}
	return poster.CreateReviewCommentReaction(owner, name, commentID, content)
}

func (c *StateRepoPullRequestClient) CreateCheckRun(owner, name string, run github.CheckRunWrite) (int64, error) {
	if c.isDisabledConfigRepo(owner, name) || c.isLocal(owner, name) {
		return 0, nil
	}
	writer, ok := c.Client.(github.CheckRunWriter)
	if !ok {
		return 0, fmt.Errorf("%s/%s has no GitHub check run writer", owner, name)
	}
	return writer.CreateCheckRun(owner, name, run)
}

func (c *StateRepoPullRequestClient) UpdateCheckRun(owner, name string, checkRunID int64, run github.CheckRunWrite) error {
	if c.isDisabledConfigRepo(owner, name) || c.isLocal(owner, name) {
		return nil
	}
	writer, ok := c.Client.(github.CheckRunWriter)
	if !ok {
		return fmt.Errorf("%s/%s has no GitHub check run writer", owner, name)
	}
	return writer.UpdateCheckRun(owner, name, checkRunID, run)
}
