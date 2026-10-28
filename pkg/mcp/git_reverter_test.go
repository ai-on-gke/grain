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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmdArgs := []string{"-C", dir}
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.Command("git", cmdArgs...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\nOutput: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestGitTaskReverterRealRepo(t *testing.T) {
	dir := t.TempDir()

	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "config", "user.email", "test@example.com")

	// Create initial commit
	file1 := filepath.Join(dir, "file1.txt")
	if err := os.WriteFile(file1, []byte("initial\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "file1.txt")
	runGit(t, dir, "commit", "-m", "initial commit")

	// Create a commit for task-101
	file2 := filepath.Join(dir, "feature.txt")
	if err := os.WriteFile(file2, []byte("feature code\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "feature.txt")
	runGit(t, dir, "commit", "-m", "feat: add feature for task-101\n\nTask: task-101")
	featureSHA := runGit(t, dir, "rev-parse", "HEAD")

	// Create subsequent commit
	file3 := filepath.Join(dir, "file3.txt")
	if err := os.WriteFile(file3, []byte("another file\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "file3.txt")
	runGit(t, dir, "commit", "-m", "chore: subsequent commit")

	reverter := NewGitTaskReverter(dir)
	ctx := t.Context()

	// Revert by TaskID
	res, err := reverter.RevertTask(ctx, RevertTaskRequest{
		TaskID: "task-101",
		Reason: "broke production metrics",
	})
	if err != nil {
		t.Fatalf("RevertTask by taskID failed: %v", err)
	}
	if res.CommitSHA != featureSHA {
		t.Fatalf("res.CommitSHA = %q, want %q", res.CommitSHA, featureSHA)
	}
	if res.TaskID != "task-101" {
		t.Fatalf("res.TaskID = %q, want task-101", res.TaskID)
	}
	if _, err := os.Stat(file2); !os.IsNotExist(err) {
		t.Fatalf("expected feature.txt to be removed by revert, but it exists")
	}

	// Verify commit message
	commitMsg := runGit(t, dir, "log", "-n", "1", "--format=%B")
	if !strings.Contains(commitMsg, "revert: task task-101") {
		t.Errorf("commit message missing title: %s", commitMsg)
	}
	if !strings.Contains(commitMsg, "broke production metrics") {
		t.Errorf("commit message missing reason: %s", commitMsg)
	}
	if !strings.Contains(commitMsg, "Reverts-Task: task-101") {
		t.Errorf("commit message missing Reverts-Task: %s", commitMsg)
	}
	if !strings.Contains(commitMsg, "Reverts-Commit: "+featureSHA) {
		t.Errorf("commit message missing Reverts-Commit: %s", commitMsg)
	}
}

func TestGitTaskReverterByCommitSHA(t *testing.T) {
	dir := t.TempDir()

	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "config", "user.email", "test@example.com")

	// Commit 1
	f := filepath.Join(dir, "buggy.txt")
	if err := os.WriteFile(f, []byte("bad code\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "buggy.txt")
	runGit(t, dir, "commit", "-m", "feat: faulty change")
	sha := runGit(t, dir, "rev-parse", "HEAD")

	reverter := NewGitTaskReverter(dir)
	res, err := reverter.RevertTask(t.Context(), RevertTaskRequest{
		CommitSHA: sha,
		Reason:    "regression found in staging",
	})
	if err != nil {
		t.Fatalf("RevertTask by commit SHA failed: %v", err)
	}
	if res.CommitSHA != sha {
		t.Fatalf("res.CommitSHA = %q, want %q", res.CommitSHA, sha)
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatalf("expected buggy.txt to be reverted")
	}
}

// TestGitTaskReverterRejectsOptionInjection is the proof from #2420: an
// option-shaped commit_sha or task_id must be refused before git runs, so
// git never gets the chance to open a file named by --output.
func TestGitTaskReverterRejectsOptionInjection(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	f := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(f, []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "file.txt")
	runGit(t, dir, "commit", "-m", "initial commit")
	head := runGit(t, dir, "rev-parse", "HEAD")

	out := filepath.Join(t.TempDir(), "injected.txt")
	reverter := NewGitTaskReverter(dir)
	for _, req := range []RevertTaskRequest{
		{CommitSHA: "--output=" + out},
		{CommitSHA: "-h"},
		{CommitSHA: "HEAD"},
		{TaskID: "--output=" + out},
		{TaskID: "task-1 --all"},
		{TaskID: "grain/--all/task-1"},
		{TaskID: "grain/a/b/task-1"},
	} {
		req.Reason = "option injection"
		if _, err := reverter.RevertTask(t.Context(), req); err == nil || !strings.Contains(err.Error(), "is not a") {
			t.Fatalf("RevertTask(%+v) err = %v, want a shape error", req, err)
		}
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Fatalf("RevertTask(%+v) created %s", req, out)
		}
	}
	if got := runGit(t, dir, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD moved to %s", got)
	}

	// A well-formed SHA that names no commit is refused too, without git
	// writing anything.
	if _, err := reverter.RevertTask(t.Context(), RevertTaskRequest{CommitSHA: "deadbeef", Reason: "missing"}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("RevertTask(deadbeef) err = %v, want not found", err)
	}
}

func TestGitTaskReverterAbbreviatedSHA(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "commit", "--allow-empty", "-m", "base")
	f := filepath.Join(dir, "buggy.txt")
	if err := os.WriteFile(f, []byte("bad\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "buggy.txt")
	runGit(t, dir, "commit", "-m", "feat: faulty change")
	sha := runGit(t, dir, "rev-parse", "HEAD")

	res, err := NewGitTaskReverter(dir).RevertTask(t.Context(), RevertTaskRequest{
		CommitSHA: strings.ToUpper(sha[:10]),
		Reason:    "abbreviated",
	})
	if err != nil {
		t.Fatalf("RevertTask by abbreviated SHA failed: %v", err)
	}
	if res.CommitSHA != sha {
		t.Fatalf("res.CommitSHA = %q, want full %q", res.CommitSHA, sha)
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatalf("expected buggy.txt to be reverted")
	}
}

func TestGitTaskReverterMultiCommitMergePR(t *testing.T) {
	dir := t.TempDir()

	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "config", "user.email", "test@example.com")

	// Base commit on main branch
	baseFile := filepath.Join(dir, "base.txt")
	if err := os.WriteFile(baseFile, []byte("base content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "base.txt")
	runGit(t, dir, "commit", "-m", "chore: base commit")

	// Create feature branch with multiple commits
	runGit(t, dir, "checkout", "-b", "grain/task-202")

	f1 := filepath.Join(dir, "feature_part1.txt")
	if err := os.WriteFile(f1, []byte("part 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "feature_part1.txt")
	runGit(t, dir, "commit", "-m", "feat(part1): add first component")

	f2 := filepath.Join(dir, "feature_part2.txt")
	if err := os.WriteFile(f2, []byte("part 2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "feature_part2.txt")
	runGit(t, dir, "commit", "-m", "feat(part2): add second component")

	// Switch back to master/main and merge feature branch with a merge commit
	runGit(t, dir, "checkout", "-")
	runGit(t, dir, "merge", "--no-ff", "grain/task-202", "-m", "Merge pull request #555 from bwsalmon/grain/task-202")
	mergeSHA := runGit(t, dir, "rev-parse", "HEAD")

	// Verify both files exist before revert
	if _, err := os.Stat(f1); err != nil {
		t.Fatalf("expected feature_part1.txt to exist before revert: %v", err)
	}
	if _, err := os.Stat(f2); err != nil {
		t.Fatalf("expected feature_part2.txt to exist before revert: %v", err)
	}

	reverter := NewGitTaskReverter(dir)
	ctx := t.Context()

	// Revert the multi-commit merge by task ID
	res, err := reverter.RevertTask(ctx, RevertTaskRequest{
		TaskID: "grain/task-202",
		Reason: "flaky performance regression in load testing",
	})
	if err != nil {
		t.Fatalf("RevertTask on multi-commit merge failed: %v", err)
	}
	if res.CommitSHA != mergeSHA {
		t.Fatalf("res.CommitSHA = %q, want %q", res.CommitSHA, mergeSHA)
	}

	// Verify both files introduced in multi-commit PR were reverted
	if _, err := os.Stat(f1); !os.IsNotExist(err) {
		t.Fatalf("expected feature_part1.txt to be removed after revert")
	}
	if _, err := os.Stat(f2); !os.IsNotExist(err) {
		t.Fatalf("expected feature_part2.txt to be removed after revert")
	}

	// Verify base file still exists untouched
	if _, err := os.Stat(baseFile); err != nil {
		t.Fatalf("expected base.txt to still exist: %v", err)
	}

	// Verify revert commit attribution and message headers
	commitMsg := runGit(t, dir, "log", "-n", "1", "--format=%B")
	if !strings.Contains(commitMsg, "revert: task grain/task-202") {
		t.Errorf("commit message missing title with task ID: %s", commitMsg)
	}
	if !strings.Contains(commitMsg, "flaky performance regression in load testing") {
		t.Errorf("commit message missing reason: %s", commitMsg)
	}
	if !strings.Contains(commitMsg, "Reverts-Task: grain/task-202") {
		t.Errorf("commit message missing Reverts-Task header: %s", commitMsg)
	}
	if !strings.Contains(commitMsg, "Reverts-Commit: "+mergeSHA) {
		t.Errorf("commit message missing Reverts-Commit header: %s", commitMsg)
	}
}

// A branch in the form DefaultTaskBranchTemplate names,
// grain/<deployment>/task-<id> (grain/task-3091), is a task id revert_task
// accepts, and finds the merge of that branch the way grain/task-<id> does.
func TestGitTaskReverterDeploymentBranch(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "config", "user.email", "test@example.com")
	base := filepath.Join(dir, "base.txt")
	if err := os.WriteFile(base, []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "base.txt")
	runGit(t, dir, "commit", "-m", "chore: base commit")

	runGit(t, dir, "checkout", "-b", "grain/0a1b2c3d/task-303")
	feature := filepath.Join(dir, "feature.txt")
	if err := os.WriteFile(feature, []byte("feature\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "feature.txt")
	runGit(t, dir, "commit", "-m", "feat: add feature")
	runGit(t, dir, "checkout", "-")
	runGit(t, dir, "merge", "--no-ff", "grain/0a1b2c3d/task-303", "-m", "Merge pull request #556 from bwsalmon/grain/0a1b2c3d/task-303")
	mergeSHA := runGit(t, dir, "rev-parse", "HEAD")

	res, err := NewGitTaskReverter(dir).RevertTask(t.Context(), RevertTaskRequest{
		TaskID: "grain/0a1b2c3d/task-303",
		Reason: "regression",
	})
	if err != nil {
		t.Fatalf("RevertTask(grain/0a1b2c3d/task-303) = %v", err)
	}
	if res.CommitSHA != mergeSHA {
		t.Fatalf("res.CommitSHA = %q, want %q", res.CommitSHA, mergeSHA)
	}
	if _, err := os.Stat(feature); !os.IsNotExist(err) {
		t.Fatalf("feature.txt survived the revert")
	}
}

// A task id is not a prefix of another: reverting task 13 must not find
// the newer merge of task 130 (or a commit whose trailer names task 1300),
// and a deployment's grain/<deployment>/task-13 must not find another
// deployment's merge of its own task-13 on the same repository, which is
// what the deployment segment in DefaultTaskBranchTemplate is for.
func TestGitTaskReverterDoesNotTakeAnotherTasksMerge(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "commit", "--allow-empty", "-m", "chore: base commit")

	merge := func(branch, file, msg string) string {
		runGit(t, dir, "checkout", "-b", branch)
		if err := os.WriteFile(filepath.Join(dir, file), []byte(file+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		runGit(t, dir, "add", file)
		runGit(t, dir, "commit", "-m", "feat: "+file)
		runGit(t, dir, "checkout", "main")
		runGit(t, dir, "merge", "--no-ff", branch, "-m", msg)
		return runGit(t, dir, "rev-parse", "HEAD")
	}
	ours := merge("grain/0dc35ba9/task-13", "ours.txt", "Merge pull request #7 from acme/grain/0dc35ba9/task-13")
	merge("grain/5e6f7a8b/task-13", "theirs.txt", "Merge pull request #8 from acme/grain/5e6f7a8b/task-13")
	merge("grain/0dc35ba9/task-130", "longer.txt", "Merge pull request #9 from acme/grain/0dc35ba9/task-130\n\nOrigin: task 1300")

	for _, id := range []string{"grain/0dc35ba9/task-13"} {
		res, err := NewGitTaskReverter(dir).RevertTask(t.Context(), RevertTaskRequest{TaskID: id, Reason: "regression"})
		if err != nil {
			t.Fatalf("RevertTask(%s) = %v", id, err)
		}
		if res.CommitSHA != ours {
			subject := runGit(t, dir, "log", "-n", "1", "--format=%s", res.CommitSHA)
			t.Fatalf("RevertTask(%s) reverted %s (%q), want %s", id, res.CommitSHA, subject, ours)
		}
		runGit(t, dir, "reset", "--hard", "HEAD~1")
	}

	// A bare id cannot say which deployment it means, but it still must
	// not take task 130's merge, the newest commit naming "task-13".
	res, err := NewGitTaskReverter(dir).RevertTask(t.Context(), RevertTaskRequest{TaskID: "13", Reason: "regression"})
	if err != nil {
		t.Fatalf("RevertTask(13) = %v", err)
	}
	if subject := runGit(t, dir, "log", "-n", "1", "--format=%s", res.CommitSHA); !strings.HasSuffix(subject, "/task-13") {
		t.Fatalf("RevertTask(13) reverted %q, want a merge of a task-13 branch", subject)
	}
}

// A task id that is a prefix of a later task's id resolves to its own
// merge, not the later one: `task-355` is not a match inside
// `task-3555` (docs/test-runs/2026-10-14-port-settings-revert-porting.md,
// finding F3).
func TestGitTaskReverterTaskIDIsNotAPrefixMatch(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "base.txt")
	runGit(t, dir, "commit", "-m", "chore: base commit")

	merge := func(branch, file string) string {
		runGit(t, dir, "checkout", "-q", "-b", branch)
		if err := os.WriteFile(filepath.Join(dir, file), []byte(file+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		runGit(t, dir, "add", file)
		runGit(t, dir, "commit", "-m", "feat: add "+file)
		runGit(t, dir, "checkout", "-q", "main")
		runGit(t, dir, "merge", "--no-ff", branch, "-m", "Merge pull request #1 from bwsalmon/"+branch)
		return runGit(t, dir, "rev-parse", "HEAD")
	}
	want := merge("grain/eu/task-355", "short.txt")
	later := merge("grain/eu/task-3555", "long.txt")

	for _, id := range []string{"355", "task-355", "grain/eu/task-355"} {
		res, err := NewGitTaskReverter(dir).RevertTask(t.Context(), RevertTaskRequest{TaskID: id, Reason: "regression"})
		if err != nil {
			t.Fatalf("RevertTask(%s) = %v", id, err)
		}
		if res.CommitSHA != want {
			t.Errorf("RevertTask(%s) reverted %s, want task 355's merge %s (task 3555's is %s)", id, res.CommitSHA, want, later)
		}
		runGit(t, dir, "reset", "-q", "--hard", later)
	}
}

// A branch named with {slug} (model.FormatTaskBranchName) carries the
// task's title after its number. Its merge is still the task's: task 13
// finds grain/<deployment>/task-13-fix-the-parser, and still not the
// merge of task-130's slugged branch.
func TestGitTaskReverterFindsASluggedBranchMerge(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "base.txt")
	runGit(t, dir, "commit", "-m", "base")
	merge := func(branch, file, msg string) {
		runGit(t, dir, "checkout", "-b", branch)
		if err := os.WriteFile(filepath.Join(dir, file), []byte(branch+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, dir, "add", file)
		runGit(t, dir, "commit", "-m", "work on "+branch)
		runGit(t, dir, "checkout", "main")
		runGit(t, dir, "merge", "--no-ff", branch, "-m", msg)
	}
	merge("grain/0dc35ba9/task-13-fix-the-parser", "ours.txt", "Merge pull request #7 from acme/grain/0dc35ba9/task-13-fix-the-parser")
	merge("grain/0dc35ba9/task-130-add-a-flag", "longer.txt", "Merge pull request #9 from acme/grain/0dc35ba9/task-130-add-a-flag")

	for _, id := range []string{"13", "grain/0dc35ba9/task-13", "grain/0dc35ba9/task-13-fix-the-parser"} {
		res, err := NewGitTaskReverter(dir).RevertTask(t.Context(), RevertTaskRequest{TaskID: id, Reason: "regression"})
		if err != nil {
			t.Fatalf("RevertTask(%s) = %v", id, err)
		}
		if subject := runGit(t, dir, "log", "-n", "1", "--format=%s", res.CommitSHA); !strings.HasSuffix(subject, "/task-13-fix-the-parser") {
			t.Fatalf("RevertTask(%s) reverted %q, want the merge of task-13-fix-the-parser", id, subject)
		}
		runGit(t, dir, "reset", "--hard", "HEAD~1")
	}
}
