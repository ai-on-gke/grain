// TestCLICreateWithBaseOpensPullRequestAgainstThatBranch is
// bwsalmon/agents#326's own scenario: pkg/orchestrator/directives_test.go
// already proves the /base directive parses into task.Base, and
// finish_test.go already proves task.Base is threaded into
// CreatePullRequest at the unit level -- nothing before this proved the
// base branch a task actually names is the one a pull request opens
// against, end to end, against a real githubsim.
//
// This reuses cli_test.go's own rig (syncedSim, githubHostServer,
// buildGrainCLI/runCLI) rather than building a second one, for the same
// reason that file gives for building its own in the first place: a
// subprocess CLI has no Go values to share with the test process.
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

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// firstPullRequestBase reads the base branch githubsim recorded for the
// first pull request it opened -- accessed with the same lock cli_test.go's
// own firstPullRequestNumber uses, since by the time a test calls this the
// grain CLI subprocess and the simulated merge may both have made requests
// through it.
func (s *syncedSim) firstPullRequestBase() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sim.PullRequests[0].Base
}

// releaseBranchPushScript is pushScript's own script, but rooted at the
// "release" branch rather than at whatever branch the clone checks out by
// default -- a scripted agent standing in for one told, via task.Base, to
// build its change on top of a named base branch rather than the repo's
// default.
func releaseBranchPushScript(remote, branch, taskID string) []antigravity.Step {
	cmd := "git clone " + remote + " work && cd work && " +
		"git checkout release && " +
		"git checkout -b " + branch + " && " +
		"echo 'change for " + taskID + "' >> NOTES.md && " +
		"git add NOTES.md && git commit -q -m 'agent commit for " + taskID + "' && " +
		"git push origin " + branch
	return []antigravity.Step{
		toolCall("run_command", map[string]any{"command": cmd}),
		finalText("pushed " + branch),
	}
}

// TestCLICreateWithBaseOpensPullRequestAgainstThatBranch seeds a repo
// with a second branch, "release", diverging from "main", files a task
// through the real CLI with `-base release`, drives it through a real
// orchestrator.RunCycle the way TestCLICreatesTaskAgentOpensPRAndUserMergeClosesIt
// does, and confirms two things a unit test can't: that the pull request
// githubsim actually recorded opened with base "release" rather than the
// repo's own default branch ("main"), and that submitting it lands the
// agent's change on "release" while leaving "main" untouched.
func TestCLICreateWithBaseOpensPullRequestAgainstThatBranch(t *testing.T) {
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

	// release diverges from main by one commit, so a branch built on top
	// of it -- and only a branch built on top of it -- carries this line.
	run(t, seed, "git", "checkout", "-b", "release")
	if err := os.WriteFile(filepath.Join(seed, "NOTES.md"), []byte("seed\nrelease-only line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "add", "NOTES.md")
	run(t, seed, "git", "commit", "-q", "-m", "release branch commit")
	run(t, seed, "git", "push", "origin", "release")

	// The repo's own default branch stays "main" -- the whole point is
	// that the PR opens against "release" despite that, because the task
	// named it explicitly.
	sim := &syncedSim{sim: githubsim.New(owner, repoName, bare, "main")}
	githubHost := githubHostServer(t, sim, upstream)

	// Step 1: file the task through the real CLI binary with -base
	// release, the real path an operator drives this through.
	storeDir := t.TempDir()
	created := runCLIStore(t, bin, storeDir,
		"-json",
		"create",
		"-title", "add a NOTES entry on release",
		"-body", "please add a line to NOTES.md, targeting the release branch",
		"-repo", owner+"/"+repoName,
		"-base", "release",
		"-approve",
	)
	var task ui.Task
	if err := json.Unmarshal([]byte(created), &task); err != nil {
		t.Fatalf("parsing grain create -json output: %v\n%s", err, created)
	}
	if task.ID == "" {
		t.Fatalf("grain create did not return a task id: %s", created)
	}
	if task.Base != "release" {
		t.Fatalf("task.Base as the CLI reports it = %q, want %q", task.Base, "release")
	}
	if task.State != model.StateQueued {
		t.Fatalf("state after create = %q, want queued", task.State)
	}

	// Step 2: a scripted agent builds its change on top of release, not
	// on top of whatever the clone checks out by default, and grain opens
	// the PR.
	remote := "http://" + githubHost + "/" + owner + "/" + repoName + ".git"
	sandboxes := credentialed(t, remote)

	branch := model.BranchName(task.ID)
	client := github.NewClient(sim, nil)
	deps := orchestrator.Deps{
		Client: client, Sandboxes: sandboxes, MaxWorkers: 1,
		Framework: scriptedFramework(releaseBranchPushScript(remote, branch, task.ID)),
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
		if st != model.StateAwaitingSubmit {
			t.Fatalf("state after the agent's push = %q, want awaiting_submit: the pull request is open and unsubmitted", st)
		}
	})
	if sim.pullRequestCount() != 1 {
		t.Fatalf("expected grain to have opened one pull request, got %d", sim.pullRequestCount())
	}
	prNumber := sim.firstPullRequestNumber()

	// The actual assertion this test exists for: the PR githubsim
	// recorded opened against "release", not the repo's own default
	// branch ("main").
	if gotBase := sim.firstPullRequestBase(); gotBase != "release" {
		t.Fatalf("pull request base = %q, want %q (the repo's default branch is %q)", gotBase, "release", "main")
	}

	// Step 3: the user submits the PR by merging the pushed branch into
	// release -- not main -- plus a real HTTP PUT to the merge endpoint,
	// the same two-part submission cli_test.go's own scenario drives.
	mergeParent := t.TempDir()
	run(t, mergeParent, "git", "clone", remote, "merge")
	mergeWd := filepath.Join(mergeParent, "merge")
	run(t, mergeWd, "git", "config", "user.email", "github@example.com")
	run(t, mergeWd, "git", "config", "user.name", "github (simulated merge)")
	run(t, mergeWd, "git", "fetch", "origin", branch)
	run(t, mergeWd, "git", "checkout", "release")
	run(t, mergeWd, "git", "merge", "--no-ff", "origin/"+branch, "-m", "Merge "+branch)
	run(t, mergeWd, "git", "push", "origin", "release")

	userTransport := github.NewRealTransport(githubHost)
	userTransport.UseTLS = false
	userClient := github.NewClient(userTransport, nil)
	if err := userClient.MergePullRequest(owner, repoName, prNumber, ""); err != nil {
		t.Fatalf("submitting (merging) the pull request: %v", err)
	}

	// Step 4: another cycle syncs the now-merged PR and closes the task.
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
			t.Fatalf("state after the merge = %q, want closed", st)
		}
	})

	// The other assertion this test exists for: the change landed on
	// release, and main was never touched.
	mainNotes := runOutput(t, upstream, "git", "--git-dir", bare, "show", "main:NOTES.md")
	if strings.Contains(mainNotes, "change for "+task.ID) {
		t.Fatalf("main:NOTES.md contains the agent's change, want it untouched by a release-targeted merge:\n%s", mainNotes)
	}
	releaseNotes := runOutput(t, upstream, "git", "--git-dir", bare, "show", "release:NOTES.md")
	if !strings.Contains(releaseNotes, "change for "+task.ID) {
		t.Fatalf("release:NOTES.md does not contain the agent's change:\n%s", releaseNotes)
	}
}
