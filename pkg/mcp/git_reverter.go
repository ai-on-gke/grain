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

package mcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// GitTaskReverter implements TaskReverter by executing git commands against a workspace directory.
type GitTaskReverter struct {
	Workspace string
}

// NewGitTaskReverter creates a new GitTaskReverter for the specified workspace.
func NewGitTaskReverter(workspace string) *GitTaskReverter {
	return &GitTaskReverter{Workspace: workspace}
}

func (g *GitTaskReverter) RevertTask(ctx context.Context, req RevertTaskRequest) (RevertTaskResult, error) {
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		return RevertTaskResult{}, errors.New("reason is required and cannot be empty")
	}
	taskID := strings.TrimSpace(req.TaskID)
	commitSHA := strings.TrimSpace(req.CommitSHA)
	if taskID == "" && commitSHA == "" {
		return RevertTaskResult{}, errors.New("either task_id or commit_sha must be provided")
	}

	if err := validateRevertTarget(taskID, commitSHA); err != nil {
		return RevertTaskResult{}, err
	}

	targetSHA := commitSHA
	if targetSHA == "" {
		// Resolve commit for task ID
		sha, err := g.findCommitForTask(ctx, taskID)
		if err != nil {
			return RevertTaskResult{}, fmt.Errorf("finding commit for task %s: %w", taskID, err)
		}
		targetSHA = sha
	}
	// Pin the target to the full object name of a commit before it goes on
	// any other command line: git revert re-parses its revisions after
	// --end-of-options, so only a value that is plainly hex is safe there.
	resolved, err := g.git(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", targetSHA+"^{commit}")
	if err != nil {
		return RevertTaskResult{}, fmt.Errorf("commit %s not found in workspace", targetSHA)
	}
	targetSHA = strings.TrimSpace(resolved)
	if !fullSHAPattern.MatchString(targetSHA) {
		return RevertTaskResult{}, fmt.Errorf("commit resolved to unexpected object name %q", targetSHA)
	}
	if commitSHA != "" && taskID == "" {
		// Attempt to extract task ID from target commit message
		if foundTaskID, err := g.extractTaskIDFromCommit(ctx, targetSHA); err == nil && foundTaskID != "" {
			taskID = foundTaskID
		}
	}

	// Read summary line of target commit
	summary, err := g.git(ctx, "log", "-n", "1", "--format=%s", "--end-of-options", targetSHA, "--")
	if err != nil {
		return RevertTaskResult{}, fmt.Errorf("reading commit %s: %w", targetSHA, err)
	}
	summary = strings.TrimSpace(summary)

	// Revert the commit in workspace without committing immediately
	revertArgs := []string{"revert", "--no-commit"}
	// Check if target commit is a merge commit (has a second parent)
	if _, err := g.git(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", targetSHA+"^2"); err == nil {
		revertArgs = append(revertArgs, "-m", "1")
	}
	revertArgs = append(revertArgs, "--end-of-options", targetSHA)
	if _, err := g.git(ctx, revertArgs...); err != nil {
		// Abort any partial state
		_, _ = g.git(ctx, "revert", "--abort")
		return RevertTaskResult{}, fmt.Errorf("git revert failed (possibly merge conflict): %w", err)
	}

	// Form commit message
	shortTarget := targetSHA
	if len(shortTarget) > 8 {
		shortTarget = shortTarget[:8]
	}
	subject := fmt.Sprintf("revert: commit %s", shortTarget)
	if taskID != "" {
		subject = fmt.Sprintf("revert: task %s (%s)", taskID, shortTarget)
	}

	var msg strings.Builder
	msg.WriteString(subject + "\n\n")
	msg.WriteString(reason + "\n\n")
	if taskID != "" {
		msg.WriteString(fmt.Sprintf("Reverts-Task: %s\n", taskID))
	}
	msg.WriteString(fmt.Sprintf("Reverts-Commit: %s\n", targetSHA))

	// Commit revert
	if _, err := g.git(ctx, "commit", "-m", msg.String()); err != nil {
		return RevertTaskResult{}, fmt.Errorf("git commit failed: %w", err)
	}

	// Get new HEAD SHA
	newSHA, err := g.git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return RevertTaskResult{}, fmt.Errorf("resolving new HEAD: %w", err)
	}
	newSHA = strings.TrimSpace(newSHA)

	return RevertTaskResult{
		NewCommitSHA: newSHA,
		TaskID:       taskID,
		CommitSHA:    targetSHA,
		Reason:       reason,
		Summary:      summary,
	}, nil
}

func (g *GitTaskReverter) git(ctx context.Context, args ...string) (string, error) {
	cmdArgs := []string{"-C", g.Workspace}
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func (g *GitTaskReverter) findCommitForTask(ctx context.Context, taskID string) (string, error) {
	cleanID := strings.TrimPrefix(taskID, "grain/")
	// grain/<deployment>/task-123, the default branch form since
	// grain/task-3091: the deployment segment is not part of the id.
	if i := strings.LastIndex(cleanID, "/"); i >= 0 {
		cleanID = cleanID[i+1:]
	}
	cleanNum := strings.TrimPrefix(cleanID, "task-")
	// Try multiple grep patterns for task ID in git log, the most
	// specific first: a grain/<deployment>/task-<id> branch names this
	// deployment's merge and not that of another grain sharing the repo,
	// whose own task with the same number merged a branch that differs
	// only in the deployment segment.
	var patterns []string
	if strings.Count(taskID, "/") > 1 {
		patterns = append(patterns, taskID)
	}
	patterns = append(patterns,
		fmt.Sprintf("task-%s", cleanNum),
		fmt.Sprintf("task %s", cleanNum),
		taskID,
		cleanID,
	)
	for _, pattern := range patterns {
		// Each pattern has to start and end where the id does, so that
		// task 13 does not find the merge of task-130 or a "task 1300"
		// trailer, and a bare 355 is not found inside task-1355.
		// validateRevertTarget has already kept everything but
		// [A-Za-z0-9_/-] out of the id, so QuoteMeta's escapes are ERE's.
		// A hyphen and a slug may follow a task-<id> pattern: a branch
		// named with {slug} (model.FormatTaskBranchName) is
		// task-13-fix-the-parser, still task 13's, and task-130 still
		// does not match. Not a bare id, which would then find 10 in a
		// date like 2026-10-13.
		slug := ""
		if strings.Contains(pattern, "task-") {
			slug = "(-[a-z0-9][a-z0-9-]*)?"
		}
		re := "(^|[^A-Za-z0-9_])" + regexp.QuoteMeta(pattern) + slug + "([^A-Za-z0-9_-]|$)"
		out, err := g.git(ctx, "log", "-n", "1", "--extended-regexp", "--grep="+re, "--format=%H")
		if err == nil && strings.TrimSpace(out) != "" {
			return strings.TrimSpace(out), nil
		}
	}
	return "", fmt.Errorf("could not find any commit mentioning task %q in git log", taskID)
}

var (
	// commitSHAPattern is the shape revert_task accepts for commit_sha: an
	// abbreviated or full hex object name (SHA-1 or SHA-256), which can never
	// be read by git as an option.
	commitSHAPattern = regexp.MustCompile(`^[0-9a-fA-F]{4,64}$`)
	fullSHAPattern   = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	// taskIDPattern is the shape revert_task accepts for task_id: "123",
	// "task-123", "grain/task-123" or "grain/<deployment>/task-123".
	taskIDPattern = regexp.MustCompile(`^(grain/([a-z0-9][a-z0-9-]*/)?)?[A-Za-z0-9][A-Za-z0-9_-]*$`)
)

// validateRevertTarget refuses a task_id or commit_sha that is not shaped
// like one, so neither can reach git's command line as an option.
func validateRevertTarget(taskID, commitSHA string) error {
	if commitSHA != "" && !commitSHAPattern.MatchString(commitSHA) {
		return fmt.Errorf("commit_sha %q is not a hex commit SHA (4 to 64 hex digits)", commitSHA)
	}
	if taskID != "" && !taskIDPattern.MatchString(taskID) {
		return fmt.Errorf("task_id %q is not a task ID (e.g. \"123\" or \"grain/task-123\")", taskID)
	}
	return nil
}

var taskRegex = regexp.MustCompile(`(?i)(?:reverts-task|task)[-:\s]+([a-zA-Z0-9_\-]+)`)

func (g *GitTaskReverter) extractTaskIDFromCommit(ctx context.Context, commitSHA string) (string, error) {
	body, err := g.git(ctx, "log", "-n", "1", "--format=%B", "--end-of-options", commitSHA, "--")
	if err != nil {
		return "", err
	}
	matches := taskRegex.FindStringSubmatch(body)
	if len(matches) > 1 {
		return matches[1], nil
	}
	return "", nil
}
