// TestCLICreatesTaskWithAutoMergeAndSyncMergesItWithNoHuman is
// bwsalmon/agents#327's own scenario: the same rig as this package's own
// TestCLICreatesTaskAgentOpensPRAndUserMergeClosesIt -- a real grain CLI
// subprocess filing the task, a real orchestrator.RunCycle driving a
// scripted agent's push over a real local git server, against a real
// githubsim -- but with `-auto-merge` set on `grain create` in place of
// that test's own "step 3", where an independent github.Client stood in
// for a human clicking "Merge pull request". Here nothing stands in for a
// human at all: a second RunCycle's own SyncPullRequests
// (pkg/orchestrator/sync.go) must notice the pull request reads clean and
// call MergePullRequest itself, task.AutoMerge (set via -auto-merge) is
// what tells it that is allowed.
//
// pkg/orchestrator/sync_test.go's TestSyncPullRequestsAutoMergesACleanPullRequest
// proves this same decision against a hand-built store/sim state; this
// test proves it reached from a real CLI-filed task through a real agent
// push, the same gap TestCLICreatesTaskAgentOpensPRAndUserMergeClosesIt
// closed for the user-merge path.
package e2e

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/github/githubsim"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/ui"
)

// markPullRequestClean sets number's Mergeable field true directly --
// standing in for GitHub's own asynchronous mergeability computation
// finishing clean, the same way pkg/orchestrator/sync_test.go's own
// TestSyncPullRequestsAutoMergesACleanPullRequest does against its
// hand-built Sim, since nothing in this rig runs real CI checks that
// would otherwise settle it.
func (s *syncedSim) markPullRequestClean(number int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	clean := true
	for i := range s.sim.PullRequests {
		if s.sim.PullRequests[i].Number == number {
			s.sim.PullRequests[i].Mergeable = &clean
		}
	}
}

// pullRequestState reads number's own State field back -- what
// confirms MergePullRequest, not just this test's own bookkeeping,
// actually ran.
func (s *syncedSim) pullRequestState(number int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, pr := range s.sim.PullRequests {
		if pr.Number == number {
			return pr.State
		}
	}
	return ""
}

func TestCLICreatesTaskWithAutoMergeAndSyncMergesItWithNoHuman(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	bin := buildGrainCLI(t)

	const owner, repoName = "acme", "widgets"
	upstream := t.TempDir()
	bare := initBareRepo(t, filepath.Join(upstream, owner, repoName+".git"), "main")
	run(t, upstream, "git", "-C", bare, "config", "http.receivepack", "true")
	seedParent := t.TempDir()
	run(t, seedParent, "git", "clone", bare, "seed")
	seed := filepath.Join(seedParent, "seed")
	run(t, seed, "git", "config", "user.email", "seed@example.com")
	run(t, seed, "git", "config", "user.name", "seed")
	if err := os.WriteFile(filepath.Join(seed, "NOTES.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "add", "NOTES.md")
	run(t, seed, "git", "commit", "-q", "-m", "initial commit")
	run(t, seed, "git", "push", "origin", "main")

	sim := &syncedSim{sim: githubsim.New(owner, repoName, bare, "main")}
	githubHost := githubHostServer(t, sim, upstream)

	// Step 1: file the task through the real CLI, with -auto-merge set --
	// the operator's own way of opting a task into the merge queue
	// (pkg/orchestrator/sync.go's isQueueMember), rather than a store row
	// built with AutoMerge already true.
	storeDir := t.TempDir()
	created := runCLIStore(t, bin, storeDir,
		"-json",
		"create",
		"-title", "add a NOTES entry",
		"-body", "please add a line to NOTES.md",
		"-repo", owner+"/"+repoName,
		"-auto-merge",
		"-approve",
	)
	var task ui.Task
	if err := json.Unmarshal([]byte(created), &task); err != nil {
		t.Fatalf("parsing grain create -json output: %v\n%s", err, created)
	}
	if task.ID == "" {
		t.Fatalf("grain create did not return a task id: %s", created)
	}
	if !task.AutoMerge {
		t.Fatalf("grain create -auto-merge did not set AutoMerge on the task: %s", created)
	}
	if task.State != model.StateQueued {
		t.Fatalf("state after create = %q, want queued", task.State)
	}

	// Step 2: the agent generates the code and grain opens the PR, same
	// as the user-merge test's own step 2.
	remote := "http://" + githubHost + "/" + owner + "/" + repoName + ".git"
	sandboxes := credentialed(t, remote)

	branch := model.BranchName(task.ID)
	client := github.NewClient(sim, nil)
	deps := orchestrator.Deps{
		Client: client, Sandboxes: sandboxes, MaxWorkers: 1,
		Framework: scriptedFramework(pushScript(remote, branch, task.ID)),
	}

	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		deps.Store = store
		if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
			t.Fatalf("RunCycle (dispatch): %v", err)
		}
		st, err := store.State(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if st != model.StateCompleted {
			t.Fatalf("state after the agent's push = %q, want completed", st)
		}
	})
	if sim.pullRequestCount() != 1 {
		t.Fatalf("expected grain to have opened one pull request, got %d", sim.pullRequestCount())
	}
	prNumber := sim.firstPullRequestNumber()

	// Step 3: the pull request reads clean -- standing in for GitHub's
	// own asynchronous mergeability check settling, the only thing this
	// test does that a human normally would not need to.  Nobody merges
	// it, clicks anything, or pushes a merge commit: that is the whole
	// point of this test, as against this package's own
	// TestCLICreatesTaskAgentOpensPRAndUserMergeClosesIt.
	sim.markPullRequestClean(prNumber)

	// Step 4: a second cycle's own SyncPullRequests must notice the PR is
	// clean, see task.AutoMerge, and call MergePullRequest itself.
	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		deps.Store = store
		if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Minute)); err != nil {
			t.Fatalf("RunCycle (sync): %v", err)
		}
		st, err := store.State(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if st != model.StateClosed {
			t.Fatalf("state after auto-merge = %q, want closed", st)
		}
	})

	if got := sim.pullRequestState(prNumber); got != "closed" {
		t.Fatalf("pull request state = %q, want closed (SyncPullRequests should have merged it)", got)
	}

	// The branch really landed in main -- githubsim's own MergePullRequest
	// performs the merge for real, at the git level, the same as GitHub's
	// PUT .../merge does as a side effect of the API call SyncPullRequests
	// made, not something this test had to do by hand. runOutput itself
	// fails the test if git exits non-zero, i.e. if branch never landed.
	runOutput(t, upstream, "git", "--git-dir", bare, "merge-base", "--is-ancestor", string(branch), "main")
	mainLog := runOutput(t, upstream, "git", "--git-dir", bare, "log", "--format=%s", "main")
	if !strings.Contains(mainLog, "agent commit for "+task.ID) {
		t.Fatalf("main's own log after auto-merge = %q, want the agent's commit merged in", mainLog)
	}

	// The only thing this whole loop put on GitHub is the pull request.
	if len(sim.sim.Issues) != 0 {
		t.Fatalf("expected no GitHub issues at all, got %+v", sim.sim.Issues)
	}

	// Step 5: confirm the loop closed the way an operator would actually
	// see it -- by asking the CLI itself, in a fresh subprocess.
	got := runCLIStore(t, bin, storeDir,
		"-json",
		"get", task.ID,
	)
	var detail ui.TaskDetail
	if err := json.Unmarshal([]byte(got), &detail); err != nil {
		t.Fatalf("parsing grain get -json output: %v\n%s", err, got)
	}
	if detail.State != model.StateClosed {
		t.Fatalf("task %s state = %q as the CLI reports it, want closed", task.ID, detail.State)
	}
}
