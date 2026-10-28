// This file drives open_pull_request the way a dispatched run really
// does: a real `grain mcpserver` subprocess, told which daemon and which
// task it serves, asked over real stdio MCP to open the pull request for
// that task's branch -- against a real ui.Server, a real
// orchestrator.OpenPullRequestForTask and a real githubsim behind it, so
// what comes back is a pull request that now exists rather than a fake's
// canned answer.
//
// mcpserver_pull_request_test.go next door makes the same argument for
// pull_request_status, and asserts one half of this tool already: that a
// bare `grain mcpserver -sandbox-root` does *not* advertise
// open_pull_request. Only that half. A wiring change that registered the
// tool nowhere at all would pass it, and pass every layer test too
// (pkg/mcp renders against a fake opener, pkg/ui carries a fake opener's
// answer over HTTP, pkg/orchestrator opens against a sim), because the
// -server/-task switch that joins them is three string comparisons in a
// flag block with no compiler anywhere near them. So the positive is
// here, alongside a real call through it and the two argv mistakes an
// operator or an agent framework can make.
//
// ui.Config.PullRequests is wired to an opener of this file's own rather
// than to cmd/grain's pullRequestOpener, which is package main and not
// importable. That conversion is covered where it lives, in
// pkg/cli/daemon_pull_request_test.go, against the same orchestrator
// and the same sim; what this file is for is everything outside that
// process: argv, tool registration, the stdio round trip, and the
// sentence the agent finally reads.
package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/github/githubsim"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/sqlite"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/ui"
)

// storeBackedOpener is ui.Config.PullRequests over the real
// orchestrator, standing in for the daemon's own pullRequestOpener
// (pkg/cli/daemon.go, package main and so out of reach from here). It
// converts the same way that one does, for the same reason: pkg/ui does
// not import pkg/orchestrator, so somebody has to.
type storeBackedOpener struct {
	store  *model.Store
	client github.Client
}

func (o storeBackedOpener) OpenForTask(ctx context.Context, taskID string) (ui.PullRequestStatus, error) {
	task, err := o.store.GetTask(ctx, taskID)
	if err != nil {
		return ui.PullRequestStatus{}, err
	}
	if task == nil {
		return ui.PullRequestStatus{}, fmt.Errorf("no task %s", taskID)
	}
	status, err := orchestrator.OpenPullRequestForTask(ctx, o.store, o.client, *task)
	if err != nil {
		return ui.PullRequestStatus{}, err
	}
	out := ui.PullRequestStatus{
		Repo:            task.Target.String(),
		Number:          status.PullRequest.Number,
		URL:             status.PullRequest.HTMLURL,
		ChecksAvailable: status.ChecksKnown,
		ChecksError:     status.ChecksError,
		Note:            status.Note,
	}
	for _, c := range status.Checks {
		check := ui.CheckStatus{Name: c.Name, Status: c.Status}
		if c.Conclusion != nil {
			check.Conclusion = *c.Conclusion
		}
		out.Checks = append(out.Checks, check)
	}
	return out, nil
}

// theOnlyPullRequest is the one pull request the sim has, read back
// through syncedSim's own lock (the subprocess's call arrives on an
// httptest.Server's goroutine, not this test's) and failing the test if
// there is not exactly one -- "opened a second one for the same head" is
// among the things these assertions are here to rule out.
func theOnlyPullRequest(t *testing.T, wire *syncedSim) githubsim.PullRequest {
	t.Helper()
	open := wire.openPullRequests()
	if len(open) != 1 {
		t.Fatalf("expected exactly one open pull request, got %+v", open)
	}
	return open[0]
}

// fileTaskForBranch puts one approved, repo-targeted task in the store --
// enough for OpenPullRequestForTask to have a repo and a branch to work
// from, which is all this test asks of the store.
func fileTaskForBranch(t *testing.T, ctx context.Context, store *model.Store, id string, repo model.RepoRef) {
	t.Helper()
	actor := human("operator")
	task := model.Task{
		ID: id, Intent: model.IntentImplement, Title: "fold the widget", Body: "please",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: actor}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: actor},
		Target:   &repo,
		Binding:  model.BindingDirective,
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("filing %s: %v", id, err)
	}
}

// runMCPServerToExit runs `grain mcpserver` with args and waits for it to
// exit, returning its exit code and stderr. Stdin is /dev/null, so a
// process that got as far as serving reads EOF at once and exits 0 --
// which is what makes a *non-zero* status here evidence of a refusal
// rather than of a hang.
func runMCPServerToExit(t *testing.T, bin string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(bin, append([]string{"mcpserver"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return 0, stderr.String()
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("running grain mcpserver %v: %v\nstderr:\n%s", args, err, stderr.String())
	}
	return exit.ExitCode(), stderr.String()
}

func TestMCPServerOpensAPullRequestOverStdio(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not installed")
	}
	bin := buildGrainBinary(t)

	const owner, repoName = "acme", "widgets"
	const taskID = "77"
	branch := model.BranchName(taskID)

	upstream := t.TempDir()
	bare, sha := mcpPullRequestRepo(t, upstream, owner, repoName, branch, "teach the widget to fold")

	sim := githubsim.New(owner, repoName, bare, "main")
	// Checks against the branch's own tip, seeded by sha: what
	// open_pull_request is for is reading CI on the commit just pushed,
	// and a mixed roster is what stops "reported the checks" from being
	// trivially true of an empty answer.
	failure := "failure"
	sim.CheckRuns[sha] = []github.CheckRun{
		{Name: "unit-tests", Status: "completed", Conclusion: &failure},
		{Name: "integration", Status: "in_progress"},
	}
	wire := &syncedSim{sim: sim}

	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	fileTaskForBranch(t, ctx, store, taskID, model.RepoRef{Owner: owner, Name: repoName})

	// The daemon the subprocess is pointed at: a real ui.Server with a
	// real opener behind Config.PullRequests, which is exactly the
	// arrangement `grain daemon` builds (pullRequestGate over
	// livePullRequests over pullRequestOpener).
	daemonURL := newTestServer(t, ui.NewServer(withAccessCode(ui.Config{
		Actor:        ui.DefaultActor("operator"),
		Capabilities: ui.OfferedCapabilities(),
		PullRequests: storeBackedOpener{store: store, client: github.NewClient(wire, nil)},
	}), store))

	t.Run("tools/list advertises open_pull_request with -server and -task", func(t *testing.T) {
		p := startMCPServer(t, bin,
			"-sandbox-root", t.TempDir(), "-server", daemonURL, "-task", taskID)
		names := toolNames(t, p)
		if !names["open_pull_request"] {
			t.Errorf("tools/list is missing open_pull_request despite -server/-task; got %v", names)
		}
		// recreate_sandbox rides on the same -server/-task pair -- both
		// are writes the daemon makes on the run's behalf, so a process
		// given the flags for one has the flags for the other.
		if !names["recreate_sandbox"] {
			t.Errorf("tools/list is missing recreate_sandbox despite -server/-task; got %v", names)
		}
		// Beside, not instead of: registering the write tool must not
		// cost the run the tools it touches its sandbox with.
		for _, want := range []string{"run_command", "read_file", "write_file", "edit_file"} {
			if !names[want] {
				t.Errorf("tools/list is missing %q; got %v", want, names)
			}
		}
	})

	t.Run("calling it opens a real pull request and reports its checks", func(t *testing.T) {
		p := startMCPServer(t, bin,
			"-sandbox-root", t.TempDir(), "-server", daemonURL, "-task", taskID)
		res, err := p.CallTool(context.Background(), "open_pull_request", map[string]any{})
		if err != nil {
			t.Fatalf("tools/call open_pull_request: %v\nstderr:\n%s", err, p.stderr.String())
		}
		if res.IsError {
			t.Fatalf("open_pull_request answered with an error: %q\nstderr:\n%s", res.Text(), p.stderr.String())
		}

		// A pull request that now exists on the (simulated) remote, for
		// this task's branch -- not a rendered sentence about one.
		opened := theOnlyPullRequest(t, wire)
		if opened.Head != branch || opened.Base != "main" {
			t.Errorf("opened %+v, want the task's own branch against the repo's default", opened)
		}

		got := res.Text()
		for _, want := range []string{
			owner + "/" + repoName + "#" + fmt.Sprint(opened.Number),
			opened.HTMLURL,
			// The checks read against the head this branch is really
			// on, including the one that has not finished.
			"unit-tests: completed (failure)",
			"integration: in_progress",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("the report is missing %q; full report:\n%s", want, got)
			}
		}

		// Calling it again is what an agent watching its own CI does, and
		// it must not open a second pull request for the same head.
		again, err := p.CallTool(context.Background(), "open_pull_request", map[string]any{})
		if err != nil {
			t.Fatalf("tools/call open_pull_request again: %v\nstderr:\n%s", err, p.stderr.String())
		}
		if again.IsError {
			t.Fatalf("the second open_pull_request answered with an error: %q", again.Text())
		}
		if second := theOnlyPullRequest(t, wire); second.Number != opened.Number {
			t.Errorf("a second call reported #%d, want the same pull request (#%d)", second.Number, opened.Number)
		}
	})

	// Landed-work detection through the real tool (docs/test-plans/
	// 2026-10-04.md §4): the pull request just opened is squash-merged on
	// GitHub, which gives main one new commit and leaves the branch
	// "ahead" by commit for ever after. Asking again from the same task
	// must open nothing and say the work is already on main.
	t.Run("after a squash merge it opens no second pull request and says the work landed", func(t *testing.T) {
		opened := theOnlyPullRequest(t, wire)
		dir := t.TempDir()
		run(t, dir, "git", "clone", "-q", "--branch", "main", bare, "work")
		wd := dir + "/work"
		run(t, wd, "git", "config", "user.email", "human@example.com")
		run(t, wd, "git", "config", "user.name", "human")
		run(t, wd, "git", "fetch", "-q", "origin", branch)
		run(t, wd, "git", "merge", "-q", "--squash", "FETCH_HEAD")
		run(t, wd, "git", "commit", "-q", "-m", fmt.Sprintf("teach the widget to fold (#%d)", opened.Number))
		run(t, wd, "git", "push", "-q", "origin", "main")
		squash := strings.TrimSpace(runOutput(t, wd, "git", "rev-parse", "HEAD"))
		wire.mu.Lock()
		for i := range wire.sim.PullRequests {
			if wire.sim.PullRequests[i].Number == opened.Number {
				wire.sim.PullRequests[i].State = "closed"
				wire.sim.PullRequests[i].Merged = true
				wire.sim.PullRequests[i].MergeCommitSHA = squash
			}
		}
		total := len(wire.sim.PullRequests)
		wire.mu.Unlock()

		p := startMCPServer(t, bin,
			"-sandbox-root", t.TempDir(), "-server", daemonURL, "-task", taskID)
		res, err := p.CallTool(context.Background(), "open_pull_request", map[string]any{})
		if err != nil {
			t.Fatalf("tools/call open_pull_request after the squash: %v\nstderr:\n%s", err, p.stderr.String())
		}
		if got := wire.pullRequestCount(); got != total {
			t.Fatalf("a pull request was opened for work already on main: %d now, %d before", got, total)
		}
		if open := wire.openPullRequests(); len(open) != 0 {
			t.Fatalf("open pull requests after the squash: %+v, want none", open)
		}
		t.Logf("open_pull_request after the squash answered (error=%v):\n%s", res.IsError, res.Text())
		if got := res.Text(); !strings.Contains(got, "already landed on main") {
			t.Fatalf("the answer does not say the work landed:\n%s", got)
		}
		// The status the daemon answered with, as the tool quotes it
		// ("(409 Conflict)"): a bare "500" also matches the squash's
		// random sha, which the answer names (25500b0a67c5 failed a
		// candidate run on 10-14).
		if got := res.Text(); strings.Contains(got, "(500 ") || strings.Contains(got, "Internal Server Error") {
			t.Fatalf("work that already landed is reported as grain failing:\n%s", got)
		}
	})

	t.Run("-server without -task refuses to start", func(t *testing.T) {
		// Not a degraded server that answers "unavailable": a process
		// told which daemon to ask but not which task to ask about
		// cannot open anything, and starting it would hand the agent a
		// tool that only ever fails.
		code, stderr := runMCPServerToExit(t, bin, "-sandbox-root", t.TempDir(), "-server", daemonURL)
		if code != 2 {
			t.Errorf("grain mcpserver -server with no -task exited %d, want 2 -- the same "+
				"\"bad usage\" status every other flag check in mcpserver.go exits with\nstderr:\n%s",
				code, stderr)
		}
		if !strings.Contains(stderr, "-task is required with -server") {
			t.Errorf("stderr = %q, want the message -task's own flag help promises", stderr)
		}
	})

	t.Run("-task without -server refuses to start", func(t *testing.T) {
		code, stderr := runMCPServerToExit(t, bin, "-sandbox-root", t.TempDir(), "-task", taskID)
		if code != 2 {
			t.Errorf("grain mcpserver -task with no -server exited %d, want 2\nstderr:\n%s", code, stderr)
		}
		if !strings.Contains(stderr, "-server is required with -task") {
			t.Errorf("stderr = %q, want the message -server's own flag help promises", stderr)
		}
	})
}
