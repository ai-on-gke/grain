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

package cli

// TestRunLiveDispatchesAndOpensAPullRequest is the daemon's own version
// of tests/e2e/live_test.go's TestLiveIssueCompletesEndToEnd and
// pkg/orchestrator/live_test.go's TestRunCycleCompletesEndToEnd: the
// closest thing to actually running `grain daemon` this repo can check
// in. It calls run() itself -- the exact function daemon() (daemon.go,
// what main() calls for a "daemon" argv[1]) calls, not a stand-in --
// against a real embedded SQLite store, a real in-process git proxy, a
// real *github.RESTClient pointed at a local server standing in for
// GitHub (git smart-HTTP plus the handful of REST endpoints
// orchestrate.openPullRequest needs, served by githubsim.Sim), and the
// real Gemini API. Gated on GEMINI_API_KEY exactly like the other two,
// so it runs nowhere without a live key (including CI) and costs nothing
// when skipped:
//
//	GEMINI_API_KEY=... go test ./cmd/grain/... -run TestRunLiveDispatchesAndOpensAPullRequest -v -timeout 20m
//
// The -timeout is part of the invocation rather than an afterthought:
// how long a live run takes is the model's business and not this test's,
// so `go test -timeout` is what bounds one here -- see the pacing
// constants below and liveDaemonContext, which read it.
//
// One gap this test works around rather than hides: neither
// pkg/orchestrate.prepare() nor pkg/orchestrator.BuildPrompt tells a
// dispatched agent what URL to clone -- the sandbox's git credentials
// are scoped to the git proxy's own (ephemeral, local) host, which
// nothing puts in the prompt (see both tests/e2e/live_test.go's and
// pkg/orchestrator/live_test.go's own prompts, which hardcode the remote
// they built themselves for the same reason). Filed as its own issue
// rather than fixed here, since the daemon's run() is what this file
// tests, not pkg/orchestrate's prompt-building. The prompt below routes
// around it exactly the way a real dispatched agent would have to today:
// by reading its own ~/.git-credentials to discover the one host it has
// been handed a credential for.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
)

// liveGeminiModel is the model the live tests name explicitly: grain has
// no default model of its own, and these tests want the run they measure
// to be the same one from day to day.
const liveGeminiModel = "gemini-3.1-pro"

// syncedSim serializes this test's own reads of a *githubsim.Sim's
// fields against the daemon's concurrent requests into it. Sim.Request
// takes the Sim's own lock now that a run outlives the cycle that
// started it (orchestrator.InFlight) and several runs really do reach
// one client at once; what that lock does not cover is a test reading
// sim.PullRequests directly while an httptest.Server's request-handling
// goroutines are inside Request, which is what this wrapper is for.
type syncedSim struct {
	mu  sync.Mutex
	sim *githubsim.Sim
}

func (s *syncedSim) Request(method, path string, headers map[string]string, body []byte) (github.ApiResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sim.Request(method, path, headers, body)
}

func (s *syncedSim) pullRequestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sim.PullRequests)
}

// seedPassingCheck gives sim one completed, successful check run on
// branch -- the CI a dispatched run's own wait_for_checks call reads.
//
// Without it that call is a three-minute stall in the middle of every
// live run here, and one nothing in grain is doing wrong. The prompt
// BuildPrompt writes tells a run with a repo to call wait_for_checks
// after it pushes (orchestrator/run.go's own CI paragraph); the forked
// mcpserver really does register that tool against this sim
// (pkg/cli/mcpserver.go's pullRequestTools, wired from -github-host);
// the sim answers the check-runs endpoint with an empty list, because no
// test ever seeded one; and mcp.firstCheckGrace -- correctly -- gives an
// empty answer three minutes to become a real one before reporting "this
// repo has no CI". Three minutes was longer than the whole context either
// test used to give its daemon, so a run that followed its prompt
// exactly could not finish inside one.
//
// Seeding a check rather than shortening that grace is also the more
// representative of the two: firstCheckGrace lives in the forked
// mcpserver, a separate process no test can reach into, and a real target
// repository has CI -- which is the entire reason that paragraph is in
// the prompt.
func (s *syncedSim) seedPassingCheck(branch string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	success := "success"
	s.sim.CheckRuns[branch] = []github.CheckRun{{Name: "build", Status: "completed", Conclusion: &success}}
}

// githubHostServer serves one httptest.Server standing in for
// cfg.githubHost: REST API calls (/repos/...) go to sim, everything else
// -- the actual git smart-HTTP traffic the git proxy forwards -- goes to
// a real `git http-backend` over gitRoot, the same technique
// tests/e2e/harness_test.go's own gitHTTPBackend uses (duplicated here
// rather than exported, that file's own comment explains why).
//
// Nothing stands between the proxy and that backend, so the credential
// this deployment forwards under is worth exactly as much as any other:
// the backend serves whoever asks. A test that needs GitHub to refuse
// *grain's own* credential calls githubHostServerAuthed instead.
func githubHostServer(t *testing.T, sim *syncedSim, gitRoot string) string {
	t.Helper()
	return githubHostServerAuthed(t, sim, gitRoot, nil)
}

// githubHostServerAuthed is githubHostServer with github.com's own git
// auth layer in front of the backend: githubsim.GitUpstream, whose
// statuses and bodies were measured against the real thing, deciding
// whether the token the proxy forwarded under may read -- and, separately,
// write -- the repository at all, before the backend ever sees the
// request. tests/e2e/harness_test.go's withGitHubAuth puts the same layer
// in front of that package's backend, for the same reason: without it the
// only git failure a test here can arrange is the proxy's own refusal of
// a repo the task never named, and the commonest real one -- the repo is
// in scope, the proxy forwards, and GitHub refuses the credential this
// deployment chose -- is unreachable.
//
// tokens is what GitHub knows about each token, keyed the way GitUpstream
// keys them: by the token as it arrives, which is the Basic auth username
// gitproxy.RealForwarder puts it in. nil is githubHostServer's own
// arrangement, no auth layer and a backend that enforces nothing.
func githubHostServerAuthed(t *testing.T, sim *syncedSim, gitRoot string,
	tokens map[string]githubsim.GitAccess) string {

	t.Helper()
	server := httptest.NewServer(githubHostHandler(sim, gitRoot, tokens))
	t.Cleanup(server.Close)
	return strings.TrimPrefix(server.URL, "http://")
}

// githubHostHandler is what githubHostServerAuthed serves, for a test
// that has to listen somewhere other than loopback: the cluster test
// (daemon_runhost_kube_test.go), whose daemon is in a kind node.
func githubHostHandler(sim *syncedSim, gitRoot string, tokens map[string]githubsim.GitAccess) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		headers := map[string]string{}
		for k := range r.Header {
			headers[k] = r.Header.Get(k)
		}
		apiResp, err := sim.Request(r.Method, r.URL.RequestURI(), headers, body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for k, v := range apiResp.Headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(apiResp.Status)
		w.Write(apiResp.Body)
	})
	var git http.Handler = gitHTTPBackendLive(gitRoot)
	if tokens != nil {
		// A plain value read as it serves: unlike the e2e harness's own
		// wrapper, nothing here grants a credential anything mid-run, so
		// there is no map to swap and no lock to take around it.
		git = &githubsim.GitUpstream{Tokens: tokens, Backend: git}
	}
	mux.Handle("/", git)
	return mux
}

// gitHTTPBackendLive serves projectRoot over the smart-HTTP CGI contract
// git itself defines, by shelling out to `git http-backend` per request
// -- duplicated from tests/e2e/harness_test.go's gitHTTPBackend rather
// than exported (that file's own comment on why: an internal test
// helper in another package's _test.go is not importable, and this
// codebase prefers the duplication to a shared exported helper for
// exactly that reason).
func gitHTTPBackendLive(projectRoot string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		cmd := exec.Command("git", "http-backend")
		cmd.Env = append(os.Environ(),
			"GIT_PROJECT_ROOT="+projectRoot,
			"GIT_HTTP_EXPORT_ALL=1",
			"PATH_INFO="+r.URL.Path,
			"QUERY_STRING="+r.URL.RawQuery,
			"REQUEST_METHOD="+r.Method,
			"CONTENT_TYPE="+r.Header.Get("Content-Type"),
			"HTTP_CONTENT_ENCODING="+r.Header.Get("Content-Encoding"),
			"GATEWAY_INTERFACE=CGI/1.1",
			"SERVER_PROTOCOL=HTTP/1.1",
		)
		cmd.Stdin = bytes.NewReader(body)
		out, err := cmd.Output()
		if err != nil {
			http.Error(w, fmt.Sprintf("git http-backend: %v", err), http.StatusInternalServerError)
			return
		}

		sep := []byte("\r\n\r\n")
		headEnd := bytes.Index(out, sep)
		if headEnd == -1 {
			sep = []byte("\n\n")
			headEnd = bytes.Index(out, sep)
		}
		headerBlock := string(out[:headEnd])
		respBody := out[headEnd+len(sep):]

		status := http.StatusOK
		for _, line := range strings.Split(headerBlock, "\n") {
			line = strings.TrimRight(line, "\r")
			if line == "" {
				continue
			}
			key, value, _ := strings.Cut(line, ":")
			value = strings.TrimSpace(value)
			if strings.EqualFold(key, "Status") {
				if code, err := strconv.Atoi(strings.Fields(value)[0]); err == nil {
					status = code
				}
				continue
			}
			w.Header().Add(key, value)
		}
		w.WriteHeader(status)
		w.Write(respBody)
	}
}

func runLive(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

// The clocks the live daemon tests in this package run on -- this one and
// daemon_true_e2e_test.go's, which shares every helper below.
//
// Only one of them is about the agent, and it is a ceiling rather than an
// expectation: liveRunBudget is how long the model is *allowed* to take,
// not how long it is assumed to take. What these tests actually wait for
// is the run reaching a terminal state (awaitFinishedRun), and every
// assertion after that is about what the finished run left behind rather
// than about the wall clock.
//
// That distinction is the point. Both tests used to hand the daemon a
// fixed context -- 200 and 240 seconds -- and carve fixed windows out of
// it, which made the model's pace an assertion nobody had written down.
// Against a live agy 1.1.26 on gemini-3.1-pro-high, single agent_response
// steps of 89 seconds were measured, alongside runs that pushed within 40
// seconds and runs that had not pushed after 180; three consecutive runs
// of the granule test gave one full pass, one "pushed, PR opened, task
// never closed" and one "never pushed", with nothing wrong in any of them
// but the clock. Worse than the flake was what it read as: "pushed but no
// pull request" is exactly what a broken finish path looks like, so a
// third of the runs of this suite accused grain of a regression it did
// not have.
//
//   - liveRunBudget bounds the daemon, and so the agent. Eight minutes is
//     several times what a healthy run needs and is there for the
//     pathological case only -- a run still going at the end of it is
//     reported as precisely that, and as nothing else.
//   - liveFinishWindow is grain's own pace, and the one window here that
//     measures something this repository controls: RunDispatch stamps a
//     run finished immediately before runOne calls ProcessResult
//     (cycle.go), so the pull request follows a couple of GitHub calls
//     behind the finished row -- never a model turn behind it.
//   - liveGitOps and liveMergeWindow are the same kind of window at the
//     far end of the granule test: a simulated human's real git merge, and
//     the daemon's own next reconcile tick noticing it. Both are grain's
//     and git's pace rather than an agent's.
//   - minLiveRunWait is the point below which there is no sense starting:
//     a wait this short cannot see even a fast run through, so a test
//     given less says so up front instead of failing on the clock later.
const (
	liveRunBudget       = 8 * time.Minute
	minLiveRunWait      = 1 * time.Minute
	liveFinishWindow    = 1 * time.Minute
	liveGitOps          = 1 * time.Minute
	liveMergeWindow     = 1 * time.Minute
	liveShutdownWait    = 30 * time.Second
	liveShutdownReserve = liveShutdownWait + 15*time.Second
	livePollInterval    = 2 * time.Second
)

// liveDaemonContext returns the context a live test's own run() call gets
// and the shorter one its wait for that run gets, both derived from `go
// test -timeout` rather than from a number written into the test.
//
// Two contexts, because the daemon has to outlive the run it dispatched:
// the pull request is opened by ProcessResult *after* framework.Run
// returns, and the granule test's merge and task-closure both need a
// daemon still ticking. inside is how much of the budget is kept back for
// that work, so a run that finishes at the very last moment of the wait
// still has all of it to be finished off in.
//
// reserve is the other end: what the test still has to do once the
// daemon's context is over -- cancelling it and waiting for run() to
// return -- kept clear of `go test`'s own deadline so that a test which
// runs out of time fails with its own diagnosis rather than with the
// panic and goroutine dump go test prints when it kills the binary. With
// no deadline at all (-timeout 0) there is nothing to subtract from and
// liveRunBudget stands alone.
func liveDaemonContext(t *testing.T, inside, reserve time.Duration) (context.Context, context.Context, context.CancelFunc) {
	t.Helper()
	budget := liveRunBudget
	if deadline, ok := t.Deadline(); ok {
		if left := time.Until(deadline) - reserve; left < budget {
			budget = left
		}
	}
	if budget < inside+minLiveRunWait {
		t.Fatalf("`go test -timeout` leaves %s for this live run, and it needs at least %s "+
			"(%s of waiting for the agent, %s of grain's own finishing afterwards). Re-run with a "+
			"larger -timeout -- 20m is comfortable for one live daemon test.",
			budget.Round(time.Second), (inside + minLiveRunWait).Round(time.Second),
			minLiveRunWait, inside.Round(time.Second))
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	runCtx, cancelWait := context.WithTimeout(ctx, budget-inside)
	return ctx, runCtx, func() { cancelWait(); cancel() }
}

// startLiveDaemon runs the daemon under test in the background, and
// returns both the error run() will eventually give back and a channel
// closed the moment it does.
//
// The second is what keeps a daemon that fell over from being reported as
// a slow agent. run() can return early on its own -- a store it cannot
// open, a UI address it cannot bind, a state repository this build must
// not overwrite -- and when it does there is nothing left writing to the
// store or serving the API the waits below poll. Without this they would
// spend the whole budget finding that out and then blame the model for
// it, which is the exact misdiagnosis this test's pacing is meant to
// stop making.
func startLiveDaemon(ctx context.Context, cfg daemon.Config) (<-chan error, <-chan struct{}) {
	done := make(chan error, 1)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		done <- daemon.Run(ctx, cfg)
	}()
	return done, stopped
}

// awaitFinishedRun blocks until taskID's latest run has been finished --
// the stamp RunDispatch writes on the row once framework.Run has returned
// -- and hands it back.
//
// This is what a live daemon test waits on in place of a wall-clock
// window, and it is the moment every assertion these tests make becomes
// decidable at once: the branch is either on the remote or it is not, and
// ProcessResult, which runOne calls immediately afterwards, has either
// opened the pull request or is a GitHub call away from it. Nothing after
// this point is waiting on the model.
//
// The error a spent budget produces is written for the reader who has to
// tell "the agent was slow" from "grain is broken", since that is the
// distinction the fixed windows this replaced could not draw: it says the
// run never finished, how long it had, and the last thing the run said
// about itself -- grain's own setup notes while the sandbox is being
// built, the agent's own update_status after that (model.Run.Activity).
func awaitFinishedRun(ctx context.Context, store *model.Store, taskID string, stopped <-chan struct{}) (model.Run, error) {
	started := time.Now()
	last := "it had not been dispatched at all"
	for {
		runs, err := store.Runs(context.WithoutCancel(ctx), taskID)
		switch {
		case err != nil:
			last = fmt.Sprintf("reading its runs back failed: %v", err)
		case len(runs) > 0:
			run := runs[len(runs)-1]
			if run.FinishedAt != nil {
				return run, nil
			}
			last = describeLiveRun(run)
		}
		select {
		case <-stopped:
			if ctx.Err() == nil {
				return model.Run{}, daemonStoppedEarly(taskID, last)
			}
			return model.Run{}, budgetSpent(taskID, time.Since(started), last)
		case <-ctx.Done():
			return model.Run{}, budgetSpent(taskID, time.Since(started), last)
		case <-time.After(livePollInterval):
		}
	}
}

// budgetSpent is what a wait for a live run says when it runs out, and it
// is written for the reader who has to tell "the agent was slow" from
// "grain is broken" -- the distinction the fixed windows this replaced
// could not draw. last is whatever the run was last known to be doing.
func budgetSpent(taskID string, waited time.Duration, last string) error {
	return fmt.Errorf(
		"no run of task %s finished in the %s this test waited: %s. That is the live agent's own "+
			"pace against a ceiling this test chose, not evidence of anything wrong in grain -- "+
			"`go test -timeout` is what sets the ceiling (liveDaemonContext), and the daemon's "+
			"own log above says what the run was doing with the time",
		taskID, waited.Round(time.Second), last)
}

// daemonStoppedEarly is the other way that wait ends: run() returned
// while the test was still waiting on it, which is a failure of the
// daemon and not of the agent's pace. stopLiveDaemon reports run()'s own
// error, so this says where to look rather than repeating it.
func daemonStoppedEarly(taskID, last string) error {
	return fmt.Errorf(
		"run() returned before any run of task %s finished: %s. The daemon stopping is what ended "+
			"this wait, not the budget -- run()'s own error is reported alongside this one",
		taskID, last)
}

// describeLiveRun words what a run that has not finished was last doing,
// for a wait that ran out of budget before it did.
func describeLiveRun(run model.Run) string {
	desc := fmt.Sprintf("attempt %d had been live for %s",
		run.Attempt, time.Since(run.StartedAt).Round(time.Second))
	if run.Activity != "" {
		desc += fmt.Sprintf(", last saying %q", run.Activity)
	}
	return desc
}

// pollUntil calls check until it answers true, window elapses or ctx is
// done, and reports whether it ever did.
//
// Every window it is called with is one of grain's own (liveFinishWindow,
// liveMergeWindow): the waits for the agent go through awaitFinishedRun
// instead, which is not a window at all.
func pollUntil(ctx context.Context, window time.Duration, check func() bool) bool {
	deadline := time.Now().Add(window)
	for {
		if check() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return check()
		case <-time.After(time.Second):
		}
	}
}

// branchPushed reports whether branch is on the bare repository standing
// in for the GitHub-hosted one -- read straight off the real repository
// rather than through sim, so no lock is involved.
func branchPushed(bare, branch string) bool {
	return exec.Command("git", "--git-dir", bare, "show-ref", "--verify", "--quiet", "refs/heads/"+branch).Run() == nil
}

// stopLiveDaemon cancels the daemon under test and waits for run() to
// return, asserting that it does so cleanly.
//
// Deferred by both live tests rather than written out at each exit: every
// assertion in them can end the test, and each one that did had to
// remember to stop the daemon first -- five times over in the granule
// test, which is five chances to leave a live agy running against a
// t.TempDir the framework is about to delete. Errorf rather than Fatalf,
// since this runs when the test is already on its way out.
func stopLiveDaemon(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run() returned an error after being cancelled: %v", err)
		}
	case <-time.After(liveShutdownWait):
		t.Errorf("run() did not return within %s of its context being cancelled", liveShutdownWait)
	}
}

func TestRunLiveDispatchesAndOpensAPullRequest(t *testing.T) {
	t.Parallel()
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY not set; skipping live grain daemon integration test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	const owner, repoName = "acme", "widgets"
	taskID := "graind-live"
	branch := model.BranchName(taskID)

	// A real bare repo standing in for the GitHub-hosted one, seeded
	// with one commit on main -- githubsim.Sim.BranchExists shells out
	// to git against this same path.
	upstream := t.TempDir()
	bare := initBareRepo(t, filepath.Join(upstream, owner, repoName+".git"))
	runLive(t, upstream, "git", "-C", bare, "config", "http.receivepack", "true")
	seedDir := filepath.Join(t.TempDir(), "seed")
	runLive(t, t.TempDir(), "git", "clone", bare, seedDir)
	runLive(t, seedDir, "git", "config", "user.email", "seed@example.com")
	runLive(t, seedDir, "git", "config", "user.name", "seed")
	if err := os.WriteFile(filepath.Join(seedDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runLive(t, seedDir, "git", "add", "README.md")
	runLive(t, seedDir, "git", "commit", "-q", "-m", "initial commit")
	runLive(t, seedDir, "git", "push", "origin", "main")

	sim := &syncedSim{sim: githubsim.New(owner, repoName, bare, "main")}
	// The CI this run's own wait_for_checks call will read once it has
	// pushed -- seeded before the daemon starts, since the run reaches
	// for it within a turn of pushing. See seedPassingCheck.
	sim.seedPassingCheck(branch)
	githubHost := githubHostServer(t, sim, upstream)

	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "secrets", "github"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedTestConnections(t, dataDir, "git:\n  - credential: anonymous\n")

	// Seed the task before starting the daemon: run() opens its own
	// store from cfg.dataDir and dispatches on its very first tick, with
	// no hook for a test to insert a task in between, so it has to
	// already be there -- the same "-data-dir already has work queued"
	// shape a real restart finds.
	store, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("seeding the store: %v", err)
	}
	task := model.Task{
		ID:     taskID,
		Intent: model.IntentImplement,
		Title:  "graind live test",
		Body: "Call your run_command tool exactly once with the single shell command below " +
			"(it already includes every step: discovering the one git host your sandbox has " +
			"credentials for, cloning, branching, editing, committing and pushing), then reply " +
			"with a short confirmation once it succeeds. Do not run anything else first.\n\n" +
			"HOST=$(sed -n 's#.*@\\([^/]*\\).*#\\1#p' ~/.git-credentials) && " +
			"git clone http://$HOST/" + owner + "/" + repoName + ".git work && " +
			"cd work && git checkout -b " + branch + " && " +
			"echo 'graind live test' >> NOTES.md && " +
			"git add NOTES.md && git commit -q -m 'graind live test' && " +
			"git push origin " + branch,
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "tester"}},
			Reason:      model.ReasonDirect,
		},
		Binding: model.BindingDirective,
		Target:  &model.RepoRef{Owner: owner, Name: repoName},
	}
	task.Approval = &model.Attribution{Actor: task.Origin.Attribution.Actor}
	if err := store.PutTask(context.Background(), task); err != nil {
		db.Close()
		t.Fatalf("seeding the task: %v", err)
	}
	// Held open past the seeding rather than closed after it: this
	// connection is also how the test watches the run's own row while the
	// daemon writes to it (awaitFinishedRun). Two connections on one
	// SQLite file is the arrangement this store is built for -- WAL, so a
	// reader never blocks on the writer, and a daemon, a UI and a CLI all
	// open the same file directly (pkg/model/sqlite's package comment).
	defer db.Close()

	// The daemon outlives the wait for its run by liveFinishWindow, since
	// the pull request this test is named for is opened by ProcessResult
	// after framework.Run returns -- see liveDaemonContext.
	ctx, runCtx, cancel := liveDaemonContext(t, liveFinishWindow, liveShutdownReserve)
	defer cancel()
	liveKeyFile := writeKeyFile(t, apiKey)
	liveCfg := daemon.Config{
		DataDir: dataDir, SandboxDir: t.TempDir(), MaxWorkers: 1, PollInterval: 5 * time.Second,
		GeminiAPIKeyFile: liveKeyFile,
		// The other half of the model selection, as -gemini-effort's
		// own default gives it to a real daemon: agy refuses a bare
		// family name with no --effort beside it, and a config literal
		// here parses no flags to fill it in.
		GeminiEffort: antigravity.DefaultEffort, MaxAgentTurns: 15,
		GithubHost: githubHost, GithubInsecureHTTP: true,
	}
	seedTestDaemonConnections(t, liveCfg, "models:\n  - id: gemini\n    framework: antigravity\n    models: ["+liveGeminiModel+"]\n"+
		"    credential_file: "+liveKeyFile+"\n")
	done, stopped := startLiveDaemon(ctx, liveCfg)
	defer stopLiveDaemon(t, cancel, done)

	// The one wait that is about the agent, and it waits for an event
	// rather than for a length of time: the run being over. Everything
	// below it is a fact by then, which is what makes each failure
	// message below able to name a cause.
	finished, err := awaitFinishedRun(runCtx, store, taskID, stopped)
	if err != nil {
		t.Fatal(err)
	}

	if !branchPushed(bare, branch) {
		t.Fatalf("branch %s is not on %s: the live run ended %q (%s) without pushing it",
			branch, bare, finished.Outcome, finished.Detail)
	}

	// openPullRequest runs when the run ends, not when the push lands:
	// cycle.go's runOne reaches finish.go's ProcessResult once
	// framework.Run has returned. So this window is not waiting on the
	// agent at all -- the agent is already gone -- only on grain's own
	// couple of GitHub calls, which is why it can be a window at all.
	if !pollUntil(ctx, liveFinishWindow, func() bool { return sim.pullRequestCount() > 0 }) {
		t.Fatalf("branch %s was pushed and its run ended %q (%s), but no pull request was "+
			"opened for it in the %s after that -- ProcessResult is what opens it (finish.go), "+
			"and it had the run's whole ending to do so",
			branch, finished.Outcome, finished.Detail, liveFinishWindow)
	}
}

func writeKeyFile(t *testing.T, key string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gemini-api-key")
	if err := os.WriteFile(path, []byte(key), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
