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

package orchestrator_test

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// pushCommitSaying lands one commit carrying message on branch, creating
// the branch if it is not there yet. The other helpers here push commits
// whose messages nobody reads; these tests are entirely about what those
// messages say.
func pushCommitSaying(t *testing.T, bare, branch, message string) {
	t.Helper()
	dir := t.TempDir()
	wd := filepath.Join(dir, "work")
	run(t, dir, "git", "clone", "-q", bare, wd)
	run(t, wd, "git", "config", "user.email", "agent@example.com")
	run(t, wd, "git", "config", "user.name", "agent")
	if err := exec.Command("git", "-C", wd, "checkout", "-q", branch).Run(); err != nil {
		run(t, wd, "git", "checkout", "-q", "-b", branch)
	}
	run(t, wd, "git", "commit", "-q", "--allow-empty", "-m", message)
	run(t, wd, "git", "push", "-q", "origin", branch)
}

// The description a reviewer reads is the agent's own account of its
// change: the commit messages on the branch, not a line of metadata.
func TestAPullRequestIsDescribedByItsOwnCommits(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	branch := model.BranchName(task.ID)
	pushCommitSaying(t, sim.BareRepo, branch,
		"Teach the parser about headers\n\nThe old one stopped at the first blank line.")

	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	body := prByHead(t, sim, branch).Body
	if !strings.HasPrefix(body, "Teach the parser about headers\n\nThe old one stopped at the first blank line.") {
		t.Fatalf("description = %q, want the commit message that explains the change", body)
	}
	if !strings.Contains(body, "Automated change for grain task t1.") {
		t.Fatalf("description = %q, want grain's own footer under it", body)
	}
}

// When a task is associated with an issue, the PR description includes
// the closing clause Fixes #<issue> above grain's footer.
func TestAPullRequestForTaskWithIssueIncludesFixesClause(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	task.Issue = 123
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	branch := model.BranchName(task.ID)
	pushCommitSaying(t, sim.BareRepo, branch,
		"Teach the parser about headers\n\nThe old one stopped at the first blank line.")

	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	body := prByHead(t, sim, branch).Body
	if !strings.Contains(body, "Fixes #123") {
		t.Fatalf("description = %q, want Fixes #123", body)
	}
	if !strings.Contains(body, "Automated change for grain task t1.") {
		t.Fatalf("description = %q, want grain's own footer under it", body)
	}
}

// A pull request is named after the change on its branch, not after the
// task that asked for it: the task here says "fix the thing" (filedTask),
// and what a reviewer sees in a list of pull requests is what was
// actually done.
func TestAPullRequestIsNamedByItsOwnCommits(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	branch := model.BranchName(task.ID)
	pushCommitSaying(t, sim.BareRepo, branch,
		"Teach the parser about headers\n\nThe old one stopped at the first blank line.")

	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	if got := prByHead(t, sim, branch).Title; got != "Teach the parser about headers" {
		t.Fatalf("title = %q, want the summary line of the commit that explains the change", got)
	}
}

// The rename is the point: a run that opened its pull request after one
// exploratory commit works out what the change is afterwards, and the
// name it settles on is the name the pull request ends up with. Rewriting
// the commit message is the whole of how an agent renames its own pull
// request -- there is no tool for it.
func TestAPullRequestOpenedEarlyIsRenamedAtTheFinish(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	branch := model.BranchName(task.ID)
	pushCommitSaying(t, sim.BareRepo, branch, "wip")

	openedMidRun(t, ctx, store, client, task)
	if got := prByHead(t, sim, branch).Title; got != "wip" {
		t.Fatalf("title at the early open = %q, want the commit that existed then", got)
	}

	pushCommitSaying(t, sim.BareRepo, branch,
		"Fix the off-by-one in the header parser\n\nIt stopped at the first blank line.")
	result := toolResult(agent.ToolCall{Name: "open_pull_request"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	if got := prByHead(t, sim, branch).Title; got != "Fix the off-by-one in the header parser" {
		t.Fatalf("title = %q, want the finished change's own name", got)
	}
}

// A branch grain cannot read says nothing about what to call the pull
// request, and the task's own title is what every pull request grain
// opened was called before there was anywhere else to look.
func TestAPullRequestFallsBackToItsTasksTitle(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	branch := model.BranchName(task.ID)
	pushCommitSaying(t, sim.BareRepo, branch, "Teach the parser about headers\n\nAn explanation.")

	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, noCompare{client}, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if got := prByHead(t, sim, branch).Title; got != task.Title {
		t.Fatalf("title = %q, want the task's own (%q)", got, task.Title)
	}
}

// The case that made grain's descriptions single-line again: a run opens
// its own pull request mid-flight to see CI, and everything it pushes
// afterwards -- including the work CI sent it back to do -- lands on a
// pull request described before any of it existed. The finish rewrites
// it.
func TestAPullRequestOpenedEarlyIsRedescribedAtTheFinish(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	branch := model.BranchName(task.ID)
	pushCommitSaying(t, sim.BareRepo, branch, "wip")

	openedMidRun(t, ctx, store, client, task)
	if got := prByHead(t, sim, branch).Body; !strings.Contains(got, "wip") {
		t.Fatalf("description at the early open = %q, want the commits that existed then", got)
	}

	// The run carries on: it reads CI, fixes what it found, and writes
	// the message that actually explains the change.
	pushCommitSaying(t, sim.BareRepo, branch,
		"Teach the parser about headers\n\nThe old one stopped at the first blank line.")
	result := toolResult(agent.ToolCall{Name: "open_pull_request"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	if len(sim.PullRequests) != 1 {
		t.Fatalf("expected the one pull request the run opened, got %+v", sim.PullRequests)
	}
	body := prByHead(t, sim, branch).Body
	if !strings.HasPrefix(body, "Teach the parser about headers") {
		t.Fatalf("description = %q, want the finished change's own account", body)
	}
	if !strings.Contains(body, "- wip") {
		t.Fatalf("description = %q, want every commit on the branch mentioned", body)
	}
}

// What a reviewer writes into a description is something grain cannot
// reconstruct from commit messages, so a refresh leaves it exactly where
// it found it.
func TestAHumanEditedDescriptionIsNeverOverwritten(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	branch := model.BranchName(task.ID)
	pushCommitSaying(t, sim.BareRepo, branch, "wip")

	pr := openedMidRun(t, ctx, store, client, task)
	const byHand = "Rewritten by a reviewer: this also needs the staging migration run first."
	if err := client.UpdatePullRequestBody(repo.Owner, repo.Name, pr.Number, byHand); err != nil {
		t.Fatalf("standing in for a human's edit: %v", err)
	}

	pushCommitSaying(t, sim.BareRepo, branch, "Teach the parser about headers\n\nAn explanation.")
	result := toolResult(agent.ToolCall{Name: "open_pull_request"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	if got := prByHead(t, sim, branch).Body; got != byHand {
		t.Fatalf("description = %q, want the human's own text untouched", got)
	}
}

// noCompare is a client whose commits cannot be read -- a credential
// without contents access, or GitHub having a bad minute.
type noCompare struct {
	github.Client
}

func (n noCompare) CompareCommits(owner, repo, base, head string) ([]github.Commit, error) {
	return nil, errors.New("no")
}

// A description is worth an API call and never worth failing a finish
// over: the pull request opens regardless, carrying the one line grain
// can always write.
func TestAPullRequestStillOpensWhenItsCommitsCannotBeRead(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	branch := model.BranchName(task.ID)
	pushCommitSaying(t, sim.BareRepo, branch, "Teach the parser about headers\n\nAn explanation.")

	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, noCompare{client}, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if got := prByHead(t, sim, branch).Body; got != "Automated change for grain task t1." {
		t.Fatalf("description = %q, want grain's plain one-line body", got)
	}
}

// The same failure on the refresh path leaves the description that is
// there alone: a read that failed is not a reason to replace a real
// description with a metadata line.
func TestAFailedCommitReadNeverDowngradesADescription(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	branch := model.BranchName(task.ID)
	pushCommitSaying(t, sim.BareRepo, branch, "Teach the parser about headers\n\nAn explanation.")

	openedMidRun(t, ctx, store, client, task)
	described := prByHead(t, sim, branch).Body
	if !strings.Contains(described, "An explanation.") {
		t.Fatalf("description at the early open = %q", described)
	}

	result := toolResult(agent.ToolCall{Name: "open_pull_request"})
	if err := orchestrator.ProcessResult(ctx, store, noCompare{client}, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if got := prByHead(t, sim, branch).Body; got != described {
		t.Fatalf("description = %q, want the one that was already there (%q)", got, described)
	}
}

func TestVeryLongTaskTitleCreatesValidSluggedBranchAndCappedPullRequest(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	cfg := model.NewDeploymentConfig()
	cfg.DeploymentID = "049f1e09"
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}

	longSentenceTitle := strings.Repeat("Investigate and repair intermittent streaming timeout across multi-region clusters 🚀 ", 30)
	t1 := model.Task{
		ID: "2077", Intent: model.IntentImplement, Title: longSentenceTitle, Body: "details",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human},
		Target:   &repo,
		Binding:  model.BindingDirective,
	}
	if err := store.PutTask(ctx, t1); err != nil {
		t.Fatalf("PutTask(2077): %v", err)
	}
	stored1, err := store.GetTask(ctx, "2077")
	if err != nil {
		t.Fatalf("GetTask(2077): %v", err)
	}
	wantBranch1 := "grain/049f1e09/task-2077-investigate-and-repair"
	if stored1.BranchName() != wantBranch1 || !model.ValidBranchName(stored1.BranchName()) {
		t.Fatalf("stored1.BranchName() = %q, want valid %q", stored1.BranchName(), wantBranch1)
	}
	pushCommitSaying(t, sim.BareRepo, stored1.BranchName(), "fix: handle multi-region timeout\n\nAdds retry backoff.")
	res := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, noCompare{client}, *stored1, res, "2077-1", baseTime); err != nil {
		t.Fatalf("ProcessResult with fallback to very long task title: %v", err)
	}
	pr1 := prByHead(t, sim, stored1.BranchName())
	if len(pr1.Title) > 256 || !strings.HasSuffix(pr1.Title, "...") {
		t.Fatalf("fallback PR title len = %d (%q), want capped title <= 256 ending in '...'", len(pr1.Title), pr1.Title)
	}
	if strings.ToValidUTF8(pr1.Title, "?") != pr1.Title {
		t.Fatalf("fallback PR title contains invalid UTF-8: %q", pr1.Title)
	}

	unbrokenWordTitle := strings.Repeat("supercalifragilisticexpialidocious", 60)
	t2 := model.Task{
		ID: "2078", Intent: model.IntentImplement, Title: unbrokenWordTitle, Body: "details",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human},
		Target:   &repo,
		Binding:  model.BindingDirective,
	}
	if err := store.PutTask(ctx, t2); err != nil {
		t.Fatalf("PutTask(2078): %v", err)
	}
	stored2, err := store.GetTask(ctx, "2078")
	if err != nil {
		t.Fatalf("GetTask(2078): %v", err)
	}
	wantBranch2 := "grain/049f1e09/task-2078-" + unbrokenWordTitle[:model.MaxBranchSlugLen]
	if stored2.BranchName() != wantBranch2 || !model.ValidBranchName(stored2.BranchName()) {
		t.Fatalf("stored2.BranchName() = %q, want valid %q", stored2.BranchName(), wantBranch2)
	}
	pushCommitSaying(t, sim.BareRepo, stored2.BranchName(), "feat: support unbroken tokens\n\nCaps slug cleanly.")
	if err := orchestrator.ProcessResult(ctx, store, client, *stored2, res, "2078-1", baseTime); err != nil {
		t.Fatalf("ProcessResult(2078): %v", err)
	}
	pr2 := prByHead(t, sim, stored2.BranchName())
	if pr2.Title != "feat: support unbroken tokens" {
		t.Fatalf("PR 2 title = %q, want commit summary", pr2.Title)
	}
}
