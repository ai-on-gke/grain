// This file is the one test that runs a real `grain mcpserver` process
// and asks it, over real stdio MCP, what GitHub says about a branch --
// the chain pkg/cli/mcpserver.go's -github-insecure-http flag was
// added for and which nothing had ever driven end to end.
//
// Every link in that chain was already covered on its own:
// pkg/agent/{claude,antigravity} prove the right argv is built,
// pkg/cli/mcpserver_test.go proves those flags resolve to a reader
// scoped to one branch, and pkg/mcp/pullrequest_tools_test.go proves a
// scoped reader renders the right answer. What none of them touch is the
// join -- argv -> flag.Parse -> gitproxy.LoadCredentialSet ->
// github.NewRealTransport plus UseTLS -> github.NewClient -> the tool --
// where several of the couplings are string-typed and one is a bool with
// no compiler anywhere near it. A misspelled flag name, a credential
// ladder path that does not exist under a real data directory, or UseTLS
// inverted would each pass every test listed above and fail every real
// run, so this drives the whole thing as a subprocess against a
// githubsim served over plain HTTP: the very "local mock GitHub in a
// live test" the flag's own help text describes.
//
// The last subtest is the one an operator actually hits. pullRequestTools'
// doc comment promises nothing about GitHub access is fatal here, because
// every other tool this process serves is how an agent touches its
// sandbox at all -- exiting over unreadable CI would turn a missing
// credential into a run that cannot edit a file. That promise had no
// test; it does now, and it is checked by editing a file through the same
// process afterwards rather than by reading a message.
package e2e

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/github/githubsim"
	"github.com/bwsalmon/grain/pkg/mcp"
	"github.com/bwsalmon/grain/pkg/model"
)

// lockedBuffer collects a subprocess's stderr for a test to read after
// the fact. os/exec writes it from a goroutine of its own, so the plain
// bytes.Buffer every other subprocess helper in this package uses (they
// all read it only after Wait) is not enough here: these tests read
// stderr while the process is still running, to report what an operator
// would have been told when an assertion fails.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// authRecordingSim is a githubsim wrapper that records the Authorization
// header of every request it forwards.
//
// Without it, the credential half of this test proves nothing: githubsim
// serves any request regardless of who is asking, so a `grain mcpserver`
// that never found -data-dir's secrets/github at all would return exactly
// the same answer as one that did. What has to be checked is that the
// token an operator put on disk reached the wire -- the ladder path is a
// string built in pkg/cli/mcpserver.go (filepath.Join(dataDir,
// "secrets", "github")) with nothing but this to hold it to the layout a
// real deployment writes.
type authRecordingSim struct {
	inner github.Transport

	mu   sync.Mutex
	seen []string
}

func (s *authRecordingSim) Request(method, path string, headers map[string]string, body []byte) (github.ApiResponse, error) {
	s.mu.Lock()
	s.seen = append(s.seen, headers["Authorization"])
	s.mu.Unlock()
	return s.inner.Request(method, path, headers, body)
}

// takeAuthorizations returns the Authorization header of every request
// since the last call, and forgets them. Clearing rather than accumulating
// is safe because the subtests below share one server and run one at a
// time, and it is what lets each of them assert about its own process's
// credential rather than about every process the test has ever started.
func (s *authRecordingSim) takeAuthorizations() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := s.seen
	s.seen = nil
	return seen
}

// mcpProcess is one live `grain mcpserver` subprocess and the MCP client
// talking to it -- the same pairing agent/claude's --mcp-config produces
// when a real CLI forks this binary, minus the CLI.
type mcpProcess struct {
	*mcp.Client
	stderr *lockedBuffer
}

// startMCPServer forks bin's "mcpserver" subcommand with args and returns
// a client speaking MCP over its stdin/stdout.
//
// The cleanup is the assertion this file's "never fatal" subtest leans
// on: closing the client closes the process's stdin, which is how
// mcp.Serve is meant to end (it returns io.EOF, and mcpserver.go treats
// that as an ordinary close rather than a failure), so a non-zero exit
// status here means the process died of something -- exactly what a fatal
// pull_request_status would look like from the outside.
func startMCPServer(t *testing.T, bin string, args ...string) *mcpProcess {
	t.Helper()
	cmd := exec.Command(bin, append([]string{"mcpserver"}, args...)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe for grain mcpserver: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe for grain mcpserver: %v", err)
	}
	stderr := &lockedBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting grain mcpserver: %v", err)
	}

	p := &mcpProcess{Client: mcp.NewClient(stdin, stdout, stdin), stderr: stderr}
	t.Cleanup(func() {
		p.Close()
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("grain mcpserver exited with %v, want a clean exit once its stdin closed\nstderr:\n%s",
					err, stderr.String())
			}
		case <-time.After(30 * time.Second):
			_ = cmd.Process.Kill()
			t.Errorf("grain mcpserver did not exit within 30s of its stdin closing\nstderr:\n%s", stderr.String())
		}
	})
	return p
}

// toolNames is a tools/list response reduced to the set a test asserts
// on -- what the tool roster *is*, independent of the order the registry
// happens to hand it back in.
func toolNames(t *testing.T, p *mcpProcess) map[string]bool {
	t.Helper()
	infos, err := p.ListTools(context.Background())
	if err != nil {
		t.Fatalf("tools/list against grain mcpserver: %v\nstderr:\n%s", err, p.stderr.String())
	}
	names := map[string]bool{}
	for _, info := range infos {
		names[info.Name] = true
	}
	return names
}

// callPullRequestStatus calls the tool and returns its rendered answer,
// failing the test if the call itself did not round-trip. Whether the
// answer is an error is left to the caller: one subtest below wants an
// error answer and two want a real report, and all three are the tool
// answering rather than the process dying.
func callPullRequestStatus(t *testing.T, p *mcpProcess) *mcp.CallResult {
	t.Helper()
	res, err := p.CallTool(context.Background(), "pull_request_status", map[string]any{})
	if err != nil {
		t.Fatalf("tools/call pull_request_status: %v\nstderr:\n%s", err, p.stderr.String())
	}
	return res
}

// mcpPullRequestRepo seeds a bare repo with a default branch and one
// agent-style branch on top of it, and returns the bare repo's path and
// the branch's tip sha -- what everything pull_request_status renders is
// checked against, read out of git rather than asserted from a constant,
// since githubsim resolves that branch with a real `git rev-parse` (its
// own doc comment on why branch lookups are not canned).
func mcpPullRequestRepo(t *testing.T, upstream, owner, name, branch, subject string) (bare, sha string) {
	t.Helper()
	bare = initBareRepo(t, filepath.Join(upstream, owner, name+".git"), "main")

	seed := filepath.Join(t.TempDir(), "seed")
	run(t, upstream, "git", "clone", "-q", bare, seed)
	run(t, seed, "git", "config", "user.email", "seed@example.com")
	run(t, seed, "git", "config", "user.name", "seed")
	mustWriteFile(t, filepath.Join(seed, "README.md"), "hello\n")
	run(t, seed, "git", "add", "README.md")
	run(t, seed, "git", "commit", "-q", "-m", "initial commit")
	run(t, seed, "git", "push", "-q", "origin", "main")

	run(t, seed, "git", "checkout", "-q", "-b", branch)
	mustWriteFile(t, filepath.Join(seed, "NOTES.md"), "the agent was here\n")
	run(t, seed, "git", "add", "NOTES.md")
	run(t, seed, "git", "commit", "-q", "-m", subject)
	run(t, seed, "git", "push", "-q", "origin", branch)

	sha = strings.TrimSpace(runOutput(t, seed, "git", "rev-parse", "HEAD"))
	return bare, sha
}

// actionsLog is what GitHub serves for a job's log: every line prefixed
// with the same RFC3339 stamp. Written out here rather than seeded as
// bare lines because the stamp coming *off* again on the way to an agent
// is part of what these tests check (github.JobLogExcerpt).
func actionsLog(lines ...string) string {
	var b strings.Builder
	for _, line := range lines {
		b.WriteString("2026-01-02T03:04:05.1234567Z " + line + "\n")
	}
	return b.String()
}

func TestMCPServerServesPullRequestStatusOverStdio(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	// buildGrainBinary itself fails rather than skips -- its existing
	// callers are live tests already gated on something else -- so the
	// toolchain check belongs here, matching cli_test.go's own
	// buildGrainCLI.
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not installed")
	}
	bin := buildGrainBinary(t)

	const owner, repoName = "acme", "widgets"
	const subject = "teach the widget to fold"
	branch := model.BranchName("59")

	upstream := t.TempDir()
	bare, sha := mcpPullRequestRepo(t, upstream, owner, repoName, branch, subject)

	sim := githubsim.New(owner, repoName, bare, "main")
	// A pull request already open for the branch, and a mixed check
	// roster against its tip: one failing, one passing, one still
	// running. All three matter -- the failing one is what the agent has
	// to be told about by name, and the other two are what stops "1
	// failing" from being trivially true of any answer at all.
	mergeable := true
	sim.PullRequests = append(sim.PullRequests, githubsim.PullRequest{
		Number:    4242,
		Title:     "fold the widget",
		Head:      branch,
		Base:      "main",
		HTMLURL:   "https://github.example/acme/widgets/pull/4242",
		State:     "open",
		Mergeable: &mergeable,
	})
	failure, success := "failure", "success"
	sim.CheckRuns[sha] = []github.CheckRun{
		{Name: "unit-tests", Status: "completed", Conclusion: &failure},
		{Name: "lint", Status: "completed", Conclusion: &success},
		{Name: "integration", Status: "in_progress"},
	}
	// ...and the Actions jobs behind them, because a check name alone is
	// where an agent's answer used to stop. Reading the failing job's own
	// log is three more endpoints deep (a commit's runs, that run's jobs,
	// that job's log), each of them a path only this test drives from a
	// real subprocess -- and the passing job is here so that "the answer
	// carries a log" cannot be satisfied by dumping every job's output.
	sim.WorkflowJobs[sha] = []githubsim.WorkflowJob{
		{Name: "unit-tests", Conclusion: "failure", Log: actionsLog(
			"--- FAIL: TestFold (0.00s)", "    fold_test.go:12: got 3 folds, want 4", "FAIL")},
		{Name: "lint", Conclusion: "success", Log: actionsLog("no issues found")},
	}
	// And the same roster for main's own tip, which is what
	// base_branch_status reads: "unit-tests" is red there too, so the
	// answer that tool gives is the one a run needs -- this failure
	// predates the branch. It is deliberately a *different* log, since
	// "the base's checks were read" is only proved by output the branch's
	// own answer could not have supplied.
	mainSHA := strings.TrimSpace(runOutput(t, upstream, "git", "-C", bare, "rev-parse", "main"))
	sim.CheckRuns[mainSHA] = []github.CheckRun{
		{Name: "unit-tests", Status: "completed", Conclusion: &failure},
		{Name: "lint", Status: "completed", Conclusion: &success},
	}
	sim.WorkflowJobs[mainSHA] = []githubsim.WorkflowJob{
		{Name: "unit-tests", Conclusion: "failure", Log: actionsLog(
			"--- FAIL: TestFold (0.00s)", "    fold_test.go:12: broken on main since Tuesday", "FAIL")},
	}
	synced := &syncedSim{sim: sim}
	wire := &authRecordingSim{inner: synced}
	host := githubHostServer(t, wire, upstream)

	// The credential ladder a real controller has: -data-dir's own
	// secrets/github, in the exact place gitproxy.LoadCredentialSet looks
	// for it. A real *.token rather than the "anonymous" every other test
	// in this tree configures, because a token is the only thing that
	// makes it observable from outside the process whether that path was
	// found at all -- see authRecordingSim.
	const ciToken = "ghp-not-a-real-token-for-a-sim"
	dataDir := t.TempDir()
	mustWriteFile(t, filepath.Join(dataDir, "secrets", "github", "credentials.json"), `{"*": "ci"}`)
	mustWriteFile(t, filepath.Join(dataDir, "secrets", "github", "ci.token"), ciToken+"\n")

	// Each subtest gets its own sandbox root, from its own t, since a
	// sandbox belongs to exactly one run and one of the subtests below
	// writes a file into it.
	serverArgs := func(t *testing.T, dataDir string, insecure bool) []string {
		t.Helper()
		args := []string{
			"-sandbox-root", t.TempDir(),
			"-data-dir", dataDir,
			"-pr-repo", owner + "/" + repoName,
			"-pr-branch", branch,
			"-github-host", host,
			// The daemon names the run's credential -- the deployment's
			// git connection for the repository -- since this process
			// no longer reads the credentials.json ladder itself
			// (docs/single-tenant-reset.md, stage 1).
			"-pr-credential", "ci",
		}
		if insecure {
			args = append(args, "-github-insecure-http")
		}
		return args
	}

	t.Run("tools/list advertises it beside the sandbox tools", func(t *testing.T) {
		p := startMCPServer(t, bin, serverArgs(t, dataDir, true)...)
		names := toolNames(t, p)
		for _, want := range []string{
			"run_command", "read_file", "write_file", "edit_file",
			"ask_question", "request_secret", "comment_on_issue", "nothing_needed", "write_handover",
			"propose_task", "add_review_comment",
			"pull_request_status", "wait_for_checks", "base_branch_status",
			"reply_to_pull_request_comment", "resolve_review_thread",
		} {
			if !names[want] {
				t.Errorf("tools/list is missing %q; got %v", want, names)
			}
		}
		// open_pull_request and recreate_sandbox are the two tools here
		// whose effect is a write, and both are registered only with
		// -server/-task, neither of which this process was given. A
		// roster advertising either anyway would be offering an agent a
		// tool that could only ever refuse.
		for _, unwanted := range []string{"open_pull_request", "recreate_sandbox"} {
			if names[unwanted] {
				t.Errorf("tools/list advertises %s without -server/-task; got %v", unwanted, names)
			}
		}
	})

	t.Run("the answer names the branch tip, the pull request and the failing check", func(t *testing.T) {
		p := startMCPServer(t, bin, serverArgs(t, dataDir, true)...)
		res := callPullRequestStatus(t, p)
		if res.IsError {
			t.Fatalf("pull_request_status answered with an error: %q\nstderr:\n%s", res.Text(), p.stderr.String())
		}
		got := res.Text()
		for _, want := range []string{
			// The tip, as githubsim resolved it out of the real bare
			// repo, shortened the way the tool renders it.
			sha[:7],
			`"` + subject + `"`,
			// The pull request the sim was set up with, and the base it
			// merges into.
			"#4242",
			"https://github.example/acme/widgets/pull/4242",
			"merges cleanly into main",
			// The checks, with the failing one named as failing rather
			// than folded into a count.
			"FAILING   unit-tests (failure)",
			"ok        lint (success)",
			"running   integration (in_progress)",
			"1 failing, 1 not finished, 1 otherwise done.",
			// And what the failing job printed, which is the difference
			// between a run that can fix the build and one that has to
			// go and guess what "unit-tests" meant.
			"--- FAIL: TestFold (0.00s)",
			"fold_test.go:12: got 3 folds, want 4",
			"/actions/runs/",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("pull_request_status answer is missing %q; full answer:\n%s", want, got)
			}
		}
		// The passing job's log is not carried: only what failed is worth
		// an agent's context window.
		if strings.Contains(got, "no issues found") {
			t.Errorf("pull_request_status carries a passing job's log; full answer:\n%s", got)
		}
		// Actions stamps every line of every log; the stamp says nothing
		// about the failure and comes off on the way here.
		if strings.Contains(got, "2026-01-02T03:04:05") {
			t.Errorf("pull_request_status carries Actions' per-line timestamps; full answer:\n%s", got)
		}

		// And it got there authenticated as the credential on disk: the
		// only evidence that mcpserver.go's own filepath.Join(dataDir,
		// "secrets", "github") names the layout an operator actually
		// writes, since the sim would have answered an anonymous caller
		// identically.
		auths := wire.takeAuthorizations()
		if len(auths) == 0 {
			t.Fatal("the mock GitHub saw no requests at all")
		}
		for _, auth := range auths {
			if auth != "token "+ciToken {
				t.Errorf("Authorization = %q, want the token from -data-dir's secrets/github ladder", auth)
			}
		}
	})

	t.Run("wait_for_checks answers over the same chain, without waiting", func(t *testing.T) {
		// The sim's roster already has a failing check on the branch's
		// tip, and a failure is what ends a wait immediately -- so this
		// exercises the whole argv -> flags -> credential -> transport
		// -> tool chain for the blocking tool without the test ever
		// sitting through a poll interval. A wait that came back with
		// anything other than the failure would mean it had not read the
		// sim at all.
		p := startMCPServer(t, bin, serverArgs(t, dataDir, true)...)
		res, err := p.CallTool(context.Background(), "wait_for_checks", map[string]any{
			"timeout_seconds": 30,
		})
		if err != nil {
			t.Fatalf("tools/call wait_for_checks: %v\nstderr:\n%s", err, p.stderr.String())
		}
		if res.IsError {
			t.Fatalf("wait_for_checks answered with an error: %q\nstderr:\n%s", res.Text(), p.stderr.String())
		}
		got := res.Text()
		for _, want := range []string{
			sha[:7],
			"FAILING   unit-tests (failure)",
			"CI has failed",
			// The log travels with the wait's verdict too -- the whole
			// reason a run calls this one instead of polling.
			"--- FAIL: TestFold (0.00s)",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("wait_for_checks answer is missing %q; full answer:\n%s", want, got)
			}
		}
		// wire accumulates until something takes them, and the
		// uncredentialed subtest below asserts on *everything* it finds
		// there -- so these credentialed reads are cleared here rather
		// than left to be read as that subtest's own.
		wire.takeAuthorizations()
	})

	t.Run("base_branch_status reads the repo's default branch over the same chain", func(t *testing.T) {
		// No -pr-base here, which is what an ordinary task passes: the
		// tool has to ask the sim for the repo's default branch, resolve
		// it to "main", read main's own tip out of the bare repo through
		// that same branch lookup, and report the checks against *that*
		// commit. Every one of those hops is a link this file exists to
		// drive for real rather than in a fake.
		p := startMCPServer(t, bin, serverArgs(t, dataDir, true)...)
		res, err := p.CallTool(context.Background(), "base_branch_status", map[string]any{})
		if err != nil {
			t.Fatalf("tools/call base_branch_status: %v\nstderr:\n%s", err, p.stderr.String())
		}
		if res.IsError {
			t.Fatalf("base_branch_status answered with an error: %q\nstderr:\n%s", res.Text(), p.stderr.String())
		}
		got := res.Text()
		for _, want := range []string{
			"main is acme/widgets's default branch",
			mainSHA[:7],
			"FAILING   unit-tests (failure)",
			// The conclusion the run cannot reach on its own, and the
			// base's own log to match its branch's failure against.
			"without your change in them",
			"broken on main since Tuesday",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("base_branch_status answer is missing %q; full answer:\n%s", want, got)
			}
		}
		// It reported the base, not the branch: the branch's tip appearing
		// here would mean the default-branch resolution had quietly
		// fallen back to the one branch the other tools read.
		if strings.Contains(got, sha[:7]) {
			t.Errorf("base_branch_status reported the run's own branch tip; full answer:\n%s", got)
		}
		wire.takeAuthorizations()
	})

	t.Run("without -github-insecure-http it reaches nothing at all", func(t *testing.T) {
		// The flag this whole file exists for. Dropped, the transport
		// speaks TLS at a plain-HTTP mock and every read fails -- which
		// is what makes the subtest above evidence that UseTLS is wired
		// the right way round rather than evidence that it is ignored.
		p := startMCPServer(t, bin, serverArgs(t, dataDir, false)...)
		res := callPullRequestStatus(t, p)
		if !res.IsError {
			t.Fatalf("pull_request_status succeeded over HTTPS against a plain-HTTP mock: %q", res.Text())
		}
		if strings.Contains(res.Text(), sha[:7]) {
			t.Errorf("pull_request_status still read the branch tip without -github-insecure-http: %q", res.Text())
		}
	})

	t.Run("a missing credential answers rather than killing the process", func(t *testing.T) {
		// The shape an operator really hits: a data directory with no
		// secrets/github under it at all. That is an unauthenticated read
		// of a public repo -- a supported configuration, per
		// pkg/cli/mcpserver_test.go -- so the tool must still answer,
		// and above all the process must still be serving the sandbox
		// tools that are an agent's only way to touch its workspace.
		emptyDataDir := t.TempDir()
		p := startMCPServer(t, bin, serverArgs(t, emptyDataDir, true)...)

		res := callPullRequestStatus(t, p)
		if res.IsError {
			t.Fatalf("pull_request_status answered with an error without a credential: %q\nstderr:\n%s",
				res.Text(), p.stderr.String())
		}
		if !strings.Contains(res.Text(), sha[:7]) {
			t.Errorf("pull_request_status answer is missing the branch tip %q; full answer:\n%s", sha[:7], res.Text())
		}
		// Unauthenticated, which is the whole point: no credential was
		// there to send, and the read happened anyway.
		for _, auth := range wire.takeAuthorizations() {
			if auth != "" {
				t.Errorf("Authorization = %q from a data directory with no secrets/github in it", auth)
			}
		}

		// "Nothing here is fatal" is a claim about the process, not about
		// this one tool's text, so it is checked by editing a file
		// through the same process afterwards -- the work a fatal
		// pull_request_status would have cost the run entirely. The
		// cleanup startMCPServer registered checks the other half: that
		// the process is still there to exit cleanly when its stdin
		// closes.
		write, err := p.CallTool(context.Background(), "write_file", map[string]any{
			"file_path": "still-alive.txt", "content": "the sandbox tools outlived the credential\n",
		})
		if err != nil {
			t.Fatalf("tools/call write_file after an uncredentialed pull_request_status: %v\nstderr:\n%s",
				err, p.stderr.String())
		}
		if write.IsError {
			t.Errorf("write_file answered with an error: %q", write.Text())
		}
	})

	// Last, since it seeds review comments every pull_request_status
	// answer above would otherwise carry.
	t.Run("review comments are listed, answered and resolved on the run's own pull request", func(t *testing.T) {
		line := 1
		synced.mu.Lock()
		sim.ReviewComments[4242] = []github.ReviewComment{
			{ID: 77, User: "gemini-code-assist", Body: "Consider a trailing newline here.", Path: "NOTES.md", Line: &line},
		}
		synced.mu.Unlock()

		p := startMCPServer(t, bin, serverArgs(t, dataDir, true)...)
		got := callPullRequestStatus(t, p).Text()
		for _, want := range []string{
			"comment 77 by @gemini-code-assist on NOTES.md:1, thread thread-77, unresolved",
			"Consider a trailing newline here.",
			"1 unresolved",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("pull_request_status answer is missing %q; full answer:\n%s", want, got)
			}
		}

		call := func(name string, args map[string]any) string {
			t.Helper()
			res, err := p.CallTool(context.Background(), name, args)
			if err != nil {
				t.Fatalf("tools/call %s: %v\nstderr:\n%s", name, err, p.stderr.String())
			}
			if res.IsError {
				t.Fatalf("%s answered with an error: %q\nstderr:\n%s", name, res.Text(), p.stderr.String())
			}
			return res.Text()
		}
		call("reply_to_pull_request_comment", map[string]any{"comment_id": 77, "body": "Added, thanks."})
		call("resolve_review_thread", map[string]any{"comment_id": 77})

		// A comment that is not on this pull request is refused, and
		// nothing is posted for it.
		res, err := p.CallTool(context.Background(), "reply_to_pull_request_comment",
			map[string]any{"comment_id": 999, "body": "not mine"})
		if err != nil {
			t.Fatalf("tools/call reply_to_pull_request_comment: %v", err)
		}
		if !res.IsError {
			t.Errorf("a reply to a comment not on the pull request was accepted: %q", res.Text())
		}

		synced.mu.Lock()
		comments := append([]github.ReviewComment(nil), sim.ReviewComments[4242]...)
		resolved := sim.ResolvedThreads["thread-77"]
		synced.mu.Unlock()
		if len(comments) != 2 || comments[1].InReplyTo != 77 || comments[1].Body != "Added, thanks." {
			t.Errorf("review comments after the reply = %+v, want the original and one reply to it", comments)
		}
		if !resolved {
			t.Error("thread-77 was not resolved")
		}

		got = callPullRequestStatus(t, p).Text()
		if !strings.Contains(got, "thread thread-77, resolved") || !strings.Contains(got, "0 unresolved") {
			t.Errorf("pull_request_status does not show the thread resolved; full answer:\n%s", got)
		}
	})
}
