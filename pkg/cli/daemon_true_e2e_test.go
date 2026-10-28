package cli

// TestRunLiveWithGranuleAndRESTAPIOpensAPullRequest is bwsalmon/agents#373's
// "true e2e test": every real dependency this repo already has a live
// test for individually -- the real Gemini API
// (pkg/agent/antigravity's own live test), a real GitHub stand-in reached
// over real HTTP (githubsim, daemon_live_test.go), and granule-backed
// sandboxing (daemon_granule_wiring_test.go) -- combined in one run, with
// the task itself filed and approved through the real REST API
// (pkg/ui.HTTPClient against a really-listening pkg/ui.Server) instead of
// a direct store write, which is how a human or the CLI actually files
// one. No existing test combines more than two of these four; see that
// issue's own gap analysis (daemon_live_test.go's closest analog uses a
// plain "local" HostSandboxes slot and seeds its task with store.PutTask,
// skipping both granule and the REST API).
//
// It reuses daemon_live_test.go's githubHostServer/syncedSim/runLive/
// writeKeyFile and daemon_granule_wiring_test.go's fake granule/crictl
// binaries verbatim (same package, same techniques) rather than
// reinventing either. The one new piece of infrastructure is
// installFakeDockerBinaryWithHome, below: writeFakeDockerBinary's fake
// stands in for a real sshd well enough for ConfigureGitCredentials'
// relative-path writes (the only thing daemon_granule_wiring_test.go and
// granule_sandboxes_test.go ever run over it), but this test's dispatched
// agent also runs `git push` for real, and git's own
// credential.helper=store looks its credentials up at
// $HOME/.git-credentials -- left as this process's own real $HOME rather
// than the fake VM's home directory, that lookup would miss the
// credentials ConfigureGitCredentialsOverSSH just wrote.
//
// Beyond confirming the pull request itself, this also closes the loop a
// real deployment cares about: a simulated human merges it (a real git
// merge over the same bare repo, plus a real HTTP PUT to githubsim's own
// merge endpoint from a second, independent github.Client -- the same
// technique tests/e2e/cli_test.go uses for the scripted-agent version
// of this story), and the test then waits for the daemon's own next
// reconcile tick to notice the merge and close the task out, confirmed,
// again, through the REST API rather than the store.
//
// Gated on GEMINI_API_KEY exactly like the other live tests, so it costs
// nothing in ordinary unit runs. live-agent.yml supplies the key for the
// default-branch check-in variant below:
//
//	GEMINI_API_KEY=... go test ./cmd/grain/... -run TestRunLiveWithGranuleAndRESTAPIOpensAPullRequest -v -timeout 20m
//
// How long it waits for that live run is `go test -timeout`'s business
// rather than this file's, exactly as in daemon_live_test.go, whose
// pacing constants and liveDaemonContext/awaitFinishedRun helpers this
// shares -- see the comment on those constants for what the fixed windows
// they replaced cost.

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// firstPullRequestNumber returns sim's first pull request's number --
// added on this file's own copy of syncedSim (daemon_live_test.go), since
// this is the one test in this package that needs a second, independent
// github.Client to act on a pull request sim's own dispatch already
// opened, the same way tests/e2e/cli_test.go's own syncedSim does for
// its scripted-agent equivalent of this test.
func (s *syncedSim) firstPullRequestNumber() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sim.PullRequests[0].Number
}

// installFakeDockerBinaryWithHome installs a fake `docker` on PATH, the
// same shape writeFakeDockerBinary (daemon_granule_wiring_test.go) uses --
// run the argv after "granule exec --" against homeDir, standing in for a
// real sshd's own "start a fresh login session in $HOME" -- but also pins
// HOME to homeDir for that command, which writeFakeDockerBinary does not:
// see this file's own doc comment for why this test needs that and the
// other granule tests do not.
func installFakeDockerBinaryWithHome(t *testing.T, homeDir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake docker script is POSIX shell only")
	}
	dir := t.TempDir()
	install(t, dir, "docker", fmt.Sprintf(`#!/bin/sh
case "$1" in
exec)
  shift
  while [ $# -gt 0 ] && [ "$1" != "--" ]; do shift; done
  shift
  PATH=%[2]q:$PATH; export PATH
  cd %[1]q && HOME=%[1]q exec "$@"
  ;;
inspect)
  echo running
  ;;
*)
  echo "fake docker: unexpected subcommand: $*" >&2
  exit 1
  ;;
esac
`, homeDir, fakeGuestSudoDir(t)))
}

// awaitFinishedAttempt is daemon_live_test.go's awaitFinishedRun read
// through the REST API instead of the store -- the same wait for the run
// to be over, made of the same GET /api/tasks/{id} this test uses for
// every other assertion it makes, since the whole point of this one is
// that an operator watching that API sees the story end.
//
// The error it returns when the budget runs out says the same thing its
// store-side twin's does, off the same two facts the API happens to carry
// in a different shape: the attempt's own progress (TaskDetail.Attempts)
// and whatever the task last said it was doing (Task.Activity).
func awaitFinishedAttempt(ctx context.Context, client *ui.HTTPClient, taskID string, stopped <-chan struct{}) (ui.Attempt, error) {
	started := time.Now()
	last := "it had not been dispatched at all"
	for {
		detail, err := client.GetTask(ctx, taskID)
		switch {
		case err != nil:
			if ctx.Err() == nil {
				last = fmt.Sprintf("reading it back over the REST API failed: %v", err)
			}
		case len(detail.Attempts) > 0:
			attempt := detail.Attempts[len(detail.Attempts)-1]
			if attempt.FinishedAt != nil {
				return attempt, nil
			}
			last = fmt.Sprintf("attempt %d had been live for %s",
				attempt.Number, time.Since(attempt.StartedAt).Round(time.Second))
			if detail.Activity != "" {
				last += fmt.Sprintf(", last saying %q", detail.Activity)
			}
		}
		select {
		case <-stopped:
			if ctx.Err() == nil {
				return ui.Attempt{}, daemonStoppedEarly(taskID, last)
			}
			return ui.Attempt{}, budgetSpent(taskID, time.Since(started), last)
		case <-ctx.Done():
			return ui.Attempt{}, budgetSpent(taskID, time.Since(started), last)
		case <-time.After(livePollInterval):
		}
	}
}

// freeTCPAddr returns a loopback address nothing is listening on yet, for
// cfg.uiAddr: startUIServer needs a concrete address up front to bind --
// run() reports back only an error, never whatever port a ":0" address
// actually bound to -- so this probes one and frees it immediately for
// run() to bind instead.
//
// The port comes from below the kernel's ephemeral range (32768-60999
// on Linux) rather than from "127.0.0.1:0". A port handed out of that
// range is up for grabs again the moment the probe closes, and between
// here and run()'s own bind every model proxy, cache and git proxy run()
// starts binds ":0", and every outbound connection in this process and
// in each parallel `go test` beside it takes an ephemeral port too. Under
// the nightly stress job one of them took the UI's port, run() returned
// "address already in use" to a channel nobody read until waitForUI gave
// up, and TestAnUpgradeNoteForAnotherBuildIsNotActedOn failed 60s later
// with "the UI/API server never came up". Nothing allocates below that
// range on its own, so a probed port there stays free unless another
// probe picks the same one at random. freeAlignedBlock scans
// 20480-32767, so this stays under it.
func freeTCPAddr(t *testing.T) string {
	t.Helper()
	const lo, hi = 10240, 20480
	for range 64 {
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(lo+rand.IntN(hi-lo)))
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			continue
		}
		if err := ln.Close(); err != nil {
			t.Fatalf("freeing the probed TCP port: %v", err)
		}
		return addr
	}
	t.Fatalf("no free TCP port between %d and %d on 127.0.0.1", lo, hi)
	return ""
}

func TestRunLiveWithGranuleAndRESTAPIOpensAPullRequest(t *testing.T) {
	testRunLiveWithSandboxAndRESTAPIOpensAPullRequest(t, func(cfg *daemon.Config) {
		// The granule "VM": a fake granule/crictl/ssh trio, the same technique
		// daemon_granule_wiring_test.go and granule_sandboxes_test.go both use
		// to stand in for a real VM/sshd. Unlike either of those, this test's
		// dispatched agent runs its real tool calls through it.
		granuleStateDir := t.TempDir()
		vmHome := t.TempDir()
		workspace := filepath.Join(vmHome, "workspace")
		if err := os.MkdirAll(workspace, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFakeGranuleBinary(t, filepath.Join(t.TempDir(), "granule-argv.log"), 30080)
		installFakeDockerBinaryWithHome(t, vmHome)

		cfg.GranuleSandboxes = true
		cfg.GranuleStateDir = granuleStateDir
		cfg.GranuleSSHUser = "debian"
		cfg.GranuleExecKey = "/images/key"
		cfg.GranuleWorkspace = workspace
	})
}

// TestRunLiveWithARealIsolatedSandboxAndRESTAPIOpensAPullRequest is the
// check-in counterpart to the fake-granule test above. It drives the same real
// daemon, REST API, Gemini agent, git proxy and GitHub simulator, but the
// agent's tools land in an actual isolated sandbox. CI selects a granule VM
// when KVM is available and the local container backend otherwise.
//
// The explicit gate keeps an ordinary `go test ./...` free of Docker, KVM and
// model calls. Once selected, missing prerequisites fail rather than skip: a
// green check-in job must mean the requested backend and live agent both ran.
func TestRunLiveWithARealIsolatedSandboxAndRESTAPIOpensAPullRequest(t *testing.T) {
	backend := os.Getenv("GRAIN_LIVE_SANDBOX_TEST")
	if backend == "" {
		t.Skip("GRAIN_LIVE_SANDBOX_TEST is unset; skipping the check-in live sandbox test")
	}
	if backend != "granule" && backend != "container" {
		t.Fatalf("GRAIN_LIVE_SANDBOX_TEST=%q: expected granule or container", backend)
	}
	if runtime.GOOS != "linux" {
		t.Fatalf("GRAIN_LIVE_SANDBOX_TEST=%s requires Linux", backend)
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("GRAIN_LIVE_SANDBOX_TEST=%s requires docker: %v", backend, err)
	}
	if out, err := exec.Command("docker", "info").CombinedOutput(); err != nil {
		t.Fatalf("GRAIN_LIVE_SANDBOX_TEST=%s requires a reachable docker daemon: %v\n%s", backend, err, out)
	}

	image := os.Getenv("GRAIN_LIVE_SANDBOX_IMAGE")
	if image == "" {
		t.Fatalf("GRAIN_LIVE_SANDBOX_TEST=%s requires GRAIN_LIVE_SANDBOX_IMAGE", backend)
	}
	if out, err := exec.Command("docker", "image", "inspect", image).CombinedOutput(); err != nil {
		t.Fatalf("sandbox image %q is not present: %v\n%s", image, err, out)
	}
	gatewayOut, err := exec.Command("docker", "network", "inspect", "bridge", "-f", "{{(index .IPAM.Config 0).Gateway}}").CombinedOutput()
	if err != nil {
		t.Fatalf("inspecting the docker bridge gateway: %v\n%s", err, gatewayOut)
	}
	gateway := strings.TrimSpace(string(gatewayOut))
	if gateway == "" || net.ParseIP(gateway) == nil {
		t.Fatalf("docker bridge gateway = %q, want an IP address the sandbox can use to reach grain", gateway)
	}

	testRunLiveWithSandboxAndRESTAPIOpensAPullRequest(t, func(cfg *daemon.Config) {
		switch backend {
		case "container":
			cfg.ContainerSandboxes = true
			cfg.ContainerImage = image
			cfg.ContainerGitProxyHost = gateway
		case "granule":
			if _, err := os.Stat("/dev/kvm"); err != nil {
				t.Fatalf("GRAIN_LIVE_SANDBOX_TEST=granule requires /dev/kvm: %v", err)
			}
			granuleBin := filepath.Join(t.TempDir(), "granule")
			cmd := exec.Command("go", "build", "-o", granuleBin, "github.com/bwsalmon/granule/cmd/granule")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("building granule for the live sandbox test: %v\n%s", err, out)
			}
			t.Setenv("PATH", filepath.Dir(granuleBin)+string(os.PathListSeparator)+os.Getenv("PATH"))

			cfg.GranuleSandboxes = true
			cfg.GranuleStateDir = t.TempDir()
			cfg.GranuleSSHUser = "debian"
			cfg.GranuleWorkspace = "/home/debian"
			cfg.GranuleGitProxyHost = gateway
			cfg.GranuleCreateArgs = []string{
				"-kontur-image", image,
				"-guest-port", "22",
				"-guest-user", "debian",
				"-disk-mode=overlay",
			}
		}
	})
}

func testRunLiveWithSandboxAndRESTAPIOpensAPullRequest(t *testing.T, configure func(*daemon.Config)) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY not set; skipping live grain daemon integration test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if runtime.GOOS == "windows" {
		t.Skip("fake granule/crictl/ssh scripts are POSIX shell only")
	}

	const owner, repoName = "acme", "gizmos"
	// maxWorkers 1, so the seeded task's first attempt is the only
	// run this drives, and its sandbox is named after it.

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
	githubHost := githubHostServer(t, sim, upstream)

	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "secrets", "github"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedTestConnections(t, dataDir, "git:\n  - credential: anonymous\n")

	uiAddr := freeTCPAddr(t)

	// The daemon outlives the wait for its run by everything this test
	// still needs a daemon for once the agent is done: opening the pull
	// request (liveFinishWindow), the real git merge standing in for a
	// human's (liveGitOps), and the reconcile tick that closes the task
	// out (liveMergeWindow). See liveDaemonContext.
	ctx, runCtx, cancel := liveDaemonContext(t,
		liveFinishWindow+liveGitOps+liveMergeWindow, liveShutdownReserve)
	defer cancel()
	liveKeyFile := writeKeyFile(t, apiKey)
	cfg := daemon.Config{
		DataDir: dataDir, MaxWorkers: 1, PollInterval: 5 * time.Second,
		GeminiAPIKeyFile: liveKeyFile,
		// The other half of the model selection, as -gemini-effort's
		// own default gives it to a real daemon: agy refuses a bare
		// family name with no --effort beside it, and a config literal
		// here parses no flags to fill it in.
		GeminiEffort: antigravity.DefaultEffort, MaxAgentTurns: 15,
		GithubHost: githubHost, GithubInsecureHTTP: true,

		UiAddr: uiAddr, Actor: "tester",
	}
	seedTestDaemonConnections(t, cfg, "models:\n  - id: gemini\n    framework: antigravity\n    models: ["+liveGeminiModel+"]\n"+
		"    credential_file: "+liveKeyFile+"\n")
	configure(&cfg)
	done, stopped := startLiveDaemon(ctx, cfg)
	// Cancelling the daemon and waiting for run() to return, once, at the
	// end -- rather than in front of each of the six assertions below,
	// every one of which can end this test. See stopLiveDaemon.
	defer stopLiveDaemon(t, cancel, done)

	// File the task through the real REST API run() itself just started
	// serving -- not a direct store write (contrast daemon_live_test.go's
	// own store.PutTask) -- the way a human or the CLI actually would.
	// It is filed unapproved, then updated with its real push/branch
	// command and only then approved, both still over the REST API,
	// because the branch name (model.BranchName(task.ID)) depends on the
	// ID Store.NewTaskID allocates when CreateTask runs, which nothing
	// can predict beforehand; leaving the task unapproved until that
	// command is in place keeps the daemon's own poll loop from
	// dispatching it too early.
	client := ui.NewHTTPClient("http://" + uiAddr)
	var task ui.Task
	{
		deadline := time.Now().Add(daemonWaitLimit)
		for {
			created, err := client.CreateTask(ctx, ui.CreateTaskRequest{
				Title:       "grain true e2e test",
				Description: "filed by TestRunLiveWithGranuleAndRESTAPIOpensAPullRequest; updated below",
				Repo:        owner + "/" + repoName,
			})
			if err == nil {
				task = created
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("filing the task through the REST API: %v", err)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	branch := model.BranchName(task.ID)
	description := "Call your run_command tool exactly once with the single shell command " +
		"below (it already includes every step: discovering the one git host your sandbox " +
		"has credentials for, cloning, branching, editing, committing and pushing), then " +
		"reply with a short confirmation once it succeeds. Do not run anything else first.\n\n" +
		"HOST=$(sed -n 's#.*@\\([^/]*\\).*#\\1#p' ~/.git-credentials) && " +
		"git clone http://$HOST/" + owner + "/" + repoName + ".git work && " +
		"cd work && git checkout -b " + branch + " && " +
		"echo 'grain true e2e test' >> NOTES.md && " +
		"git add NOTES.md && git commit -q -m 'grain true e2e test' && " +
		"git push origin " + branch
	if _, err := client.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{Description: &description}); err != nil {
		t.Fatalf("updating the task through the REST API: %v", err)
	}
	// The CI this run's own wait_for_checks call will read once it has
	// pushed, seeded before the task is approved and so before anything
	// can be dispatched. See seedPassingCheck (daemon_live_test.go).
	sim.seedPassingCheck(branch)
	if err := client.Approve(ctx, task.ID); err != nil {
		t.Fatalf("approving the task through the REST API: %v", err)
	}

	// The one wait in this test that is about the agent, and it waits for
	// the run to be over rather than for a length of time -- through the
	// REST API, like every assertion that follows it (awaitFinishedAttempt).
	attempt, err := awaitFinishedAttempt(runCtx, client, task.ID, stopped)
	if err != nil {
		t.Fatal(err)
	}

	// The branch a successful live run, dispatched onto the granule-faked
	// sandbox, pushes -- read off the real bare repo, not sim, so no lock
	// is needed here. This is the first proof the whole chain worked:
	// REST API -> store -> orchestrator -> real Gemini agent -> granule's
	// SSH tools -> git proxy -> githubsim's git-http-backend.
	if !branchPushed(bare, branch) {
		t.Fatalf("branch %s is not on %s: the live run ended %q (%s) without pushing it",
			branch, bare, attempt.Outcome, attempt.Detail)
	}

	// openPullRequest runs when the run ends, not when the push lands
	// (cycle.go's runOne, through finish.go's ProcessResult), so this
	// window waits on grain's own GitHub calls and not on the agent,
	// which is already gone. Polled back through the REST API -- not sim
	// directly -- to confirm both that a PR was opened against githubsim
	// *and* that the daemon's own REST API surfaces it, closing the loop
	// this test is named for.
	prOpened := pollUntil(ctx, liveFinishWindow, func() bool {
		got, err := client.Task(ctx, task.ID)
		return err == nil && got.PullRequest != ""
	})
	if !prOpened {
		t.Fatalf("branch %s was pushed and its run ended %q (%s), but the REST API reported no "+
			"pull request for task %s in the %s after that",
			branch, attempt.Outcome, attempt.Detail, task.ID, liveFinishWindow)
	}
	if sim.pullRequestCount() == 0 {
		t.Fatalf("REST API reported a pull request for task %s but githubsim has none", task.ID)
	}
	prNumber := sim.firstPullRequestNumber()

	// A simulated human merges the pull request: a real git merge over
	// the same bare repo, plus a real HTTP PUT to githubsim's own merge
	// endpoint from a second, independent github.Client, standing in for
	// a person clicking "Merge pull request" on GitHub the way the REST
	// API already stood in for one filing the task in the first place.
	remote := "http://" + githubHost + "/" + owner + "/" + repoName + ".git"
	mergeDir := filepath.Join(t.TempDir(), "merge")
	runLive(t, t.TempDir(), "git", "clone", remote, mergeDir)
	runLive(t, mergeDir, "git", "config", "user.email", "github@example.com")
	runLive(t, mergeDir, "git", "config", "user.name", "github (simulated merge)")
	runLive(t, mergeDir, "git", "fetch", "origin", branch)
	runLive(t, mergeDir, "git", "checkout", "main")
	runLive(t, mergeDir, "git", "merge", "--no-ff", "origin/"+branch, "-m", "Merge "+branch)
	runLive(t, mergeDir, "git", "push", "origin", "main")

	userTransport := github.NewRealTransport(githubHost)
	userTransport.UseTLS = false
	userClient := github.NewClient(userTransport, nil)
	if err := userClient.MergePullRequest(owner, repoName, prNumber, ""); err != nil {
		t.Fatalf("submitting (merging) the pull request: %v", err)
	}

	// The daemon's own next reconcile tick syncs the now-merged pull
	// request and closes the task out -- confirmed, once more, through
	// the REST API rather than the store, so this test's whole final
	// assertion is exactly what an operator watching the same API would
	// see: the task they filed, dispatched, and had merged reads closed.
	//
	// Another of grain's own windows rather than the agent's: what has to
	// happen inside it is one poll interval and one reconcile pass, both
	// this test's own configuration (pollInterval above).
	closed := pollUntil(ctx, liveMergeWindow, func() bool {
		got, err := client.Task(ctx, task.ID)
		return err == nil && got.State == model.StateClosed
	})
	if !closed {
		t.Fatalf("task %s never read back as closed over the REST API in the %s after its pull "+
			"request was merged -- the run itself ended %q (%s) and its pull request was opened, "+
			"so what is left unproven here is the reconcile tick that notices a merge",
			task.ID, liveMergeWindow, attempt.Outcome, attempt.Detail)
	}
	if len(sim.sim.Issues) != 0 {
		t.Fatalf("expected no GitHub issues at all, got %+v", sim.sim.Issues)
	}
}
