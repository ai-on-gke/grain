package orchestrator_test

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/github/githubsim"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/sqlite/sqlitetest"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

var baseTime = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

// TestMain routes this package's log at whichever test is running, and
// turns off two waits for every test in this package.
//
// The routing is log_test.go's -- see the file comment there for what it
// is for. It is done here because a test binary gets one TestMain and
// this package's was already this one.
//
// branchExistsSettled's re-check backoff -- see
// orchestrator.DisableBranchExistsSleep for why it is dead time here
// specifically. The retry behaviour itself is covered by
// TestBranchExistsSettledReChecksANegative, which stubs the same sleep
// itself and asserts the call count, so nothing here is left untested by
// skipping the wall clock.
//
// And the check-registration window -- the wait that keeps an empty check
// list from reading as clean until CI has had time to register (see
// orchestrator.SetCheckRegistrationWindow). Every test here drives a
// githubsim with no CI in it whatsoever, so leaving the window on would
// mean each one either waiting two minutes of real time for a check run
// that is never coming, or seeding a clock jump into an assertion about
// something else entirely. The tests that are about the window set it
// themselves and restore it: the sync_internal_test.go group around
// TestEmptyChecksSettledWaitsOutTheWindow, and
// TestSyncPullRequestsWaitsForCiToRegisterBeforeMergingAFreshPullRequest.
func TestMain(m *testing.M) {
	serveHostScriptIfAsked()
	orchestrator.RouteLogToTests()
	restoreSleep := orchestrator.DisableBranchExistsSleep()
	restoreWindow := orchestrator.SetCheckRegistrationWindow(0)
	restoreFirewall := orchestrator.StubHostFirewall()
	code := m.Run()
	restoreFirewall()
	restoreWindow()
	restoreSleep()
	os.Exit(code)
}

// openStore opens a real embedded SQLite store in a temp directory -- the
// same discipline every other package's own tests hold to (model/
// simulate_test.go's own doc comment: "Nothing here is a fake standing in
// for the store").
func openStore(t *testing.T) (*model.Store, context.Context) {
	t.Helper()
	// First, before the cleanups below and before whatever the caller
	// registers after us: the log has to be given back last of all. See
	// logToTest in log_test.go.
	orchestrator.LogToTest(t)
	store, _ := sqlitetest.Open(t)
	return store, context.Background()
}

type bareRepoTemplateEntry struct {
	rel  string
	dir  bool
	mode os.FileMode
	data []byte
}

var (
	seededBareReposMu sync.Mutex
	seededBareRepos   = map[string][]bareRepoTemplateEntry{}
)

func copySeededBareRepo(t *testing.T, dst, branch string) string {
	t.Helper()
	seededBareReposMu.Lock()
	entries, ok := seededBareRepos[branch]
	if !ok {
		tmp, err := os.MkdirTemp("", "grain-sim-bare-template-")
		if err != nil {
			seededBareReposMu.Unlock()
			t.Fatal(err)
		}
		defer os.RemoveAll(tmp)
		bare := initBareRepo(t, filepath.Join(tmp, "repo.git"), branch)
		seed := filepath.Join(tmp, "seed")
		run(t, tmp, "git", "clone", "-q", bare, seed)
		run(t, seed, "git", "config", "user.email", "seed@example.com")
		run(t, seed, "git", "config", "user.name", "seed")
		run(t, seed, "git", "commit", "-q", "--allow-empty", "-m", "seed")
		run(t, seed, "git", "push", "-q", "origin", branch)
		err = filepath.WalkDir(bare, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, err := filepath.Rel(bare, path)
			if err != nil || rel == "." {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if d.IsDir() {
				entries = append(entries, bareRepoTemplateEntry{rel: rel, dir: true, mode: info.Mode().Perm()})
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entries = append(entries, bareRepoTemplateEntry{rel: rel, mode: info.Mode().Perm(), data: data})
			return nil
		})
		if err != nil {
			seededBareReposMu.Unlock()
			t.Fatal(err)
		}
		seededBareRepos[branch] = entries
	}
	seededBareReposMu.Unlock()

	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		target := filepath.Join(dst, e.rel)
		if e.dir {
			if err := os.MkdirAll(target, e.mode); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(target, e.data, e.mode); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

// newSim is githubsim's own test helper, restated here (package-private,
// deliberately duplicated -- see tests/e2e/harness_test.go's own
// comment on why the same choice is made throughout this codebase): a
// real bare git repo behind a Sim, wired into a real github.RESTClient.
func newSim(t *testing.T, owner, repo, branch string) (*githubsim.Sim, *github.RESTClient) {
	t.Helper()
	// Same claim as openStore's, for the tests that drive a simulated
	// GitHub without opening a store of their own.
	orchestrator.LogToTest(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	bare := copySeededBareRepo(t, filepath.Join(dir, "repo.git"), branch)

	sim := githubsim.New(owner, repo, bare, branch)
	// Most of this package's tests run in parallel against acme/widgets,
	// and grain keeps process-wide memory keyed by pull request (see
	// Sim.PullRequestBase): without a range of its own, one test's merge
	// refusal or gather hold on acme/widgets#9000 held another test's
	// head out of the queue.
	sim.PullRequestBase = githubsim.UniquePullRequestBase()
	return sim, github.NewClient(sim, nil)
}

// pushBranch pushes an empty commit on branch straight to bare, standing
// in for a real dispatch's own git push -- this package's own tests care
// about what happens once a branch is real, not about how it got there.
func pushBranch(t *testing.T, bare, branch string) {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "git", "clone", "-q", bare, "work")
	wd := filepath.Join(dir, "work")
	run(t, wd, "git", "config", "user.email", "agent@example.com")
	run(t, wd, "git", "config", "user.name", "agent")
	cmd := exec.Command("git", "-C", wd, "checkout", "-q", branch)
	if err := cmd.Run(); err != nil {
		run(t, wd, "git", "checkout", "-q", "-b", branch)
	}
	// Named after the branch, so two branches pushed off the same base in
	// the same test never share a commit sha. They used to: both are an
	// empty commit with the same message, the same author and -- inside
	// one second, which is all git's timestamps resolve -- the same
	// timestamp, so git hashed them to the same object. That was harmless
	// until the merge queue started reading checks by head sha rather
	// than by branch, at which point one branch's check runs
	// (Sim.CheckRuns, keyed by sha) were served for the other branch's
	// pull request too, and a test holding one pull request's CI queued
	// silently held every other pull request in the same sim queued with
	// it.
	run(t, wd, "git", "commit", "-q", "--allow-empty", "-m", "agent commit on "+branch)
	run(t, wd, "git", "push", "-q", "origin", branch)
}

// pushFile lands a real file on a branch that already exists, for the
// tests whose subject is what the branch changed rather than that it
// changed anything -- the merge queue now reads which files a pull
// request touches (orchestrator.touchedWorkflowFiles), and githubsim
// answers that off this repository rather than off a seeded list.
func pushFile(t *testing.T, bare, branch, path, content string) {
	t.Helper()
	dir := t.TempDir()
	wd := filepath.Join(dir, "work")
	run(t, dir, "git", "clone", "-q", "--branch", branch, bare, wd)
	run(t, wd, "git", "config", "user.email", "agent@example.com")
	run(t, wd, "git", "config", "user.name", "agent")
	full := filepath.Join(wd, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, wd, "git", "add", "-A")
	run(t, wd, "git", "commit", "-q", "-m", "write "+path+" on "+branch)
	run(t, wd, "git", "push", "-q", "origin", branch)
}

// branchSHA is what bare has branch pointing at, or "" if it has no such
// branch -- the same resolution a Sim does when it turns a pull
// request's head branch into the sha it keys check runs by.
func branchSHA(t *testing.T, bare, branch string) string {
	t.Helper()
	cmd := exec.Command("git", "--git-dir", bare, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// The helper's own contract above, checked rather than trusted: every
// merge-queue test here that seeds one branch's check runs depends on no
// other branch it pushed answering to the same sha, and two pushes
// inside one second is exactly the case a fixed commit message used to
// collapse into a single object. githubsim's own copy of pushBranch
// carries the same guard, for the same reason.
func TestPushBranchGivesEachBranchItsOwnSHA(t *testing.T) {
	t.Parallel()
	sim, _ := newSim(t, "acme", "widgets", "main")
	pushBranch(t, sim.BareRepo, "grain/task-1")
	pushBranch(t, sim.BareRepo, "grain/task-2")

	first, second := branchSHA(t, sim.BareRepo, "grain/task-1"), branchSHA(t, sim.BareRepo, "grain/task-2")
	if first == "" || second == "" {
		t.Fatalf("both branches should be on the remote: %q, %q", first, second)
	}
	if first == second {
		t.Fatalf("two branches share a commit sha (%s): the check runs a test seeds for one pull request are served for every other pull request in the sim, and a test holding one queue head's CI queued silently holds the whole queue with it", first)
	}
}

// pushAnotherCommit lands one more commit on a branch that already
// exists -- pushBranch creates one, this moves it, which is what a push
// arriving while the merge queue is mid-cycle does.
func pushAnotherCommit(t *testing.T, bare, branch string) {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "git", "clone", "-q", "--branch", branch, bare, "work")
	wd := filepath.Join(dir, "work")
	run(t, wd, "git", "config", "user.email", "agent@example.com")
	run(t, wd, "git", "config", "user.name", "agent")
	run(t, wd, "git", "commit", "-q", "--allow-empty", "-m", "a later push")
	run(t, wd, "git", "push", "-q", "origin", branch)
}

func run(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}
